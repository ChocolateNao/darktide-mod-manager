package manifest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// installedMod is a mod as the database holds it: every field filled, including
// the machine-local ones that must *not* reach the manifest.
func installedMod() *domain.Mod {
	return &domain.Mod{
		ID:            "better_ui",
		Name:          "Better UI",
		Version:       "1.4.2",
		Author:        "someone",
		Description:   "moves the health bar",
		GameVersion:   "1.0.3",
		GameID:        "darktide",
		SourceID:      "nexus",
		Dependencies:  []string{"core_libs"},
		ConflictsWith: []string{"ugly_ui"},
		Tags:          []string{"ui", "hud"},
		SourceURL:     "https://www.nexusmods.com/darktide/mods/12",
		DownloadURL:   "",
		UpdateURL:     "https://www.nexusmods.com/darktide/mods/12",
		SourceData: map[string]any{
			"mod_id":       float64(12),
			"file_count":   float64(3),
			"verified":     true,
			"description":  "from the source",
			"nested":       map[string]any{"a": 1},
			"missing":      nil,
			"download_key": "x",
		},
		Hash:          "deadbeef",
		LocalPath:     "/games/darktide/mods/better_ui",
		ModFolderName: "better_ui",
		InstallMode:   domain.InstallModeGameRoot,
		Priority:      42,
		Enabled:       true,
		Hooks: domain.ModHooks{
			PostInstall: []domain.HookScript{{
				Language:   domain.LangLua,
				Content:    "print('installed')",
				WorkingDir: "{{install_path}}",
				Env:        []string{"MODE=live"},
				Timeout:    30,
			}},
			PreEnable: []domain.HookScript{{
				Language: domain.LangJS,
				Content:  "console.log('enable')",
			}},
		},
	}
}

func TestFromModCarriesEverythingTheManifestCanHold(t *testing.T) {
	m := FromMod(installedMod())

	if m.Mod.ID != "better_ui" || m.Mod.Name != "Better UI" || m.Mod.Version != "1.4.2" {
		t.Fatalf("identity = %+v", m.Mod)
	}

	if m.Mod.Author != "someone" || m.Mod.Description != "moves the health bar" {
		t.Fatalf("attribution = %+v", m.Mod)
	}

	if m.Mod.GameVersion != "1.0.3" {
		t.Fatalf("game version = %q", m.Mod.GameVersion)
	}

	if len(m.Mod.Dependencies) != 1 || m.Mod.Dependencies[0] != "core_libs" {
		t.Fatalf("dependencies = %v", m.Mod.Dependencies)
	}

	if len(m.Mod.ConflictsWith) != 1 || m.Mod.ConflictsWith[0] != "ugly_ui" {
		t.Fatalf("conflicts = %v", m.Mod.ConflictsWith)
	}

	if len(m.Mod.Tags) != 2 {
		t.Fatalf("tags = %v", m.Mod.Tags)
	}

	if m.Mod.SourceURL != "https://www.nexusmods.com/darktide/mods/12" {
		t.Fatalf("source url = %q", m.Mod.SourceURL)
	}

	if m.Mod.UpdateURL == "" {
		t.Fatal("the update link was dropped")
	}
}

// A manifest travels with the mod. Recording this machine's enabled flag,
// install path or content hash would put one user's installation state into
// everyone else's copy, so those fields are left out on purpose.
func TestFromModLeavesMachineLocalStateOut(t *testing.T) {
	m := FromMod(installedMod())

	data, err := Encode(m)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	for _, leaked := range []string{
		"deadbeef", "/games/darktide", "enabled", "priority", "game_root", "installed_at",
	} {
		if containsString(string(data), leaked) {
			t.Fatalf("the manifest contains %q:\n%s", leaked, data)
		}
	}
}

func TestFromModHooksSurviveTheRoundTrip(t *testing.T) {
	dir := t.TempDir()

	_, err := Write(dir, FromMod(installedMod()))
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	back, err := Read(dir)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	hooks, err := back.DomainHooks()
	if err != nil {
		t.Fatalf("domain hooks: %v", err)
	}

	original := installedMod().Hooks

	if got, want := hooks.Count(), original.Count(); got != want {
		t.Fatalf("hook count = %d, want %d", got, want)
	}

	scripts := hooks.For(domain.PhasePostInstall)
	if len(scripts) != 1 {
		t.Fatalf("post-install scripts = %d", len(scripts))
	}

	if scripts[0].Content != "print('installed')" || scripts[0].WorkingDir != "{{install_path}}" {
		t.Fatalf("post-install script = %+v", scripts[0])
	}

	if scripts[0].Timeout != 30 || len(scripts[0].Env) != 1 || scripts[0].Env[0] != "MODE=live" {
		t.Fatalf("post-install script details = %+v", scripts[0])
	}

	if scripts[0].Language != domain.LangLua {
		t.Fatalf("language = %q", scripts[0].Language)
	}

	pre := hooks.For(domain.PhasePreEnable)
	if len(pre) != 1 || pre[0].Language != domain.LangJS {
		t.Fatalf("pre-enable scripts = %+v", pre)
	}
}

// The point of the whole projection: the manifest a recipient reads tells them
// where the mod lives, so they can fetch it themselves instead of trusting a
// peer. Losing that on a round trip would make the file decorative.
func TestFromModSourceLinksSurviveTheRoundTrip(t *testing.T) {
	dir := t.TempDir()

	if _, err := Write(dir, FromMod(installedMod())); err != nil {
		t.Fatalf("write: %v", err)
	}

	back, err := Read(dir)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if back.Mod.SourceURL == "" || back.Mod.UpdateURL == "" {
		t.Fatalf("source links = %+v", back.Mod)
	}

	if back.Mod.SourceData["mod_id"] != "12" || back.Mod.SourceData["verified"] != "true" {
		t.Fatalf("source data = %v", back.Mod.SourceData)
	}
}

func TestWriteKeepsTheExistingManifestName(t *testing.T) {
	dir := t.TempDir()

	// A mod that already ships metadata.toml must not gain a competing
	// mod.toml: Read takes the first name in priority order, so a second file
	// would be silently ignored forever.
	if err := os.WriteFile(
		filepath.Join(dir, "metadata.toml"),
		[]byte("[mod]\nid = \"x\"\n"),
		0o600,
	); err != nil {
		t.Fatalf("seed manifest: %v", err)
	}

	path, err := Write(dir, FromMod(installedMod()))
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	if filepath.Base(path) != "metadata.toml" {
		t.Fatalf("wrote %s", path)
	}

	if _, err := os.Stat(filepath.Join(dir, Names[0])); !os.IsNotExist(err) {
		t.Fatal("a second manifest was created")
	}
}

func TestWriteRefusesAFolderThatIsNotThere(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")

	if _, err := Write(missing, FromMod(installedMod())); err == nil {
		t.Fatal("expected an error for a missing folder")
	}
}

func TestWriteRefusesAFileAsAFolder(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-folder")

	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	_, err := Write(file, FromMod(installedMod()))
	if !appErrors.IsAppError(err, appErrors.ErrCodeInvalidMod) {
		t.Fatalf("error = %v, want INVALID_MOD", err)
	}
}

func TestWriteRefusesABlankFolder(t *testing.T) {
	if _, err := Write("  ", FromMod(installedMod())); !appErrors.IsAppError(
		err,
		appErrors.ErrCodeInvalidArgument,
	) {
		t.Fatalf("error = %v, want INVALID_ARGUMENT", err)
	}
}

func TestEncodeRefusesNothing(t *testing.T) {
	if _, err := Encode(nil); !appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument) {
		t.Fatalf("error = %v, want INVALID_ARGUMENT", err)
	}
}

func TestFromModTolerantOfNothing(t *testing.T) {
	if m := FromMod(nil); m == nil || m.Mod.ID != "" {
		t.Fatal("a nil mod should project to an empty manifest")
	}
}

func TestSourceDataStringsRendersEveryShape(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{name: "nil", value: nil, want: ""},
		{name: "string", value: "text", want: "text"},
		{name: "bool", value: false, want: "false"},
		{name: "int", value: 7, want: "7"},
		{name: "int64", value: int64(8), want: "8"},
		{name: "float", value: 1.5, want: "1.5"},
		{name: "json number", value: json.Number("3"), want: "3"},
		{name: "object", value: map[string]any{"a": 1}, want: `{"a":1}`},
		{name: "slice", value: []any{1, "b"}, want: `[1,"b"]`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SourceDataValue(tc.value); got != tc.want {
				t.Fatalf("SourceDataValue(%v) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

// A value with no sensible spelling must not fail the whole write: the rest of
// the record is still worth having, and the placeholder says what happened.
func TestSourceDataValueFallsBackForUnmarshalableValues(t *testing.T) {
	if got := SourceDataValue(make(chan int)); got == "" {
		t.Fatal("expected a placeholder")
	}
}

func TestSourceDataStringsDropsAnEmptyMap(t *testing.T) {
	if got := SourceDataStrings(nil); got != nil {
		t.Fatalf("SourceDataStrings(nil) = %v", got)
	}

	if got := SourceDataStrings(map[string]any{}); got != nil {
		t.Fatalf("SourceDataStrings(empty) = %v", got)
	}
}

// A hook with no language cannot be written; skipping it keeps Write from
// emitting a file that fails to parse.
func TestFromModSkipsUnnamedHookScripts(t *testing.T) {
	mod := installedMod()
	mod.Hooks = domain.ModHooks{
		PostInstall: []domain.HookScript{{Content: "orphan"}},
	}

	m := FromMod(mod)
	if len(m.Hooks.PostInstall) != 0 {
		t.Fatalf("post install = %+v", m.Hooks.PostInstall)
	}

	if _, err := Encode(m); err != nil {
		t.Fatalf("encode: %v", err)
	}
}

func containsString(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}

			return false
		}()
}
