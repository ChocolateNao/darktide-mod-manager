package manifest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	assert.Equal(t, "better_ui", m.Mod.ID, "identity = %+v", m.Mod)
	assert.Equal(t, "Better UI", m.Mod.Name, "identity = %+v", m.Mod)
	assert.Equal(t, "1.4.2", m.Mod.Version, "identity = %+v", m.Mod)

	assert.Equal(t, "someone", m.Mod.Author, "attribution = %+v", m.Mod)
	assert.Equal(t, "moves the health bar", m.Mod.Description, "attribution = %+v", m.Mod)

	assert.Equal(t, "1.0.3", m.Mod.GameVersion, "game version = %q", m.Mod.GameVersion)

	assert.Equal(t, []string{"core_libs"}, m.Mod.Dependencies, "dependencies = %v", m.Mod.Dependencies)

	assert.Equal(t, []string{"ugly_ui"}, m.Mod.ConflictsWith, "conflicts = %v", m.Mod.ConflictsWith)

	assert.Len(t, m.Mod.Tags, 2, "tags = %v", m.Mod.Tags)

	assert.Equal(t,
		"https://www.nexusmods.com/darktide/mods/12",
		m.Mod.SourceURL,
		"source url = %q",
		m.Mod.SourceURL,
	)

	assert.NotEmpty(t, m.Mod.UpdateURL, "the update link was dropped")
}

// A manifest travels with the mod: recording this machine's enabled flag or
// content hash would put one user's installation state into everyone else's
// copy.
func TestFromModLeavesMachineLocalStateOut(t *testing.T) {
	m := FromMod(installedMod())

	data, err := Encode(m)
	require.NoError(t, err, "encode: %v", err)

	for _, leaked := range []string{
		"deadbeef", "/games/darktide", "enabled", "priority", "game_root", "installed_at",
	} {
		assert.False(t, containsString(string(data), leaked), "the manifest contains %q:\n%s", leaked, data)
	}
}

func TestFromModHooksSurviveTheRoundTrip(t *testing.T) {
	dir := t.TempDir()

	_, err := Write(dir, FromMod(installedMod()))
	require.NoError(t, err, "write: %v", err)

	back, err := Read(dir)
	require.NoError(t, err, "read: %v", err)

	hooks, err := back.DomainHooks()
	require.NoError(t, err, "domain hooks: %v", err)

	original := installedMod().Hooks

	assert.Equal(t, original.Count(), hooks.Count(), "hook count = %d, want %d", hooks.Count(), original.Count())

	scripts := hooks.For(domain.PhasePostInstall)
	require.Len(t, scripts, 1, "post-install scripts = %d", len(scripts))

	assert.Equal(t, "print('installed')", scripts[0].Content, "post-install script = %+v", scripts[0])
	assert.Equal(t, "{{install_path}}", scripts[0].WorkingDir, "post-install script = %+v", scripts[0])

	assert.Equal(t, 30, scripts[0].Timeout, "post-install script details = %+v", scripts[0])
	require.Len(t, scripts[0].Env, 1, "post-install script details = %+v", scripts[0])
	assert.Equal(t, "MODE=live", scripts[0].Env[0], "post-install script details = %+v", scripts[0])

	assert.Equal(t, domain.LangLua, scripts[0].Language, "language = %q", scripts[0].Language)

	pre := hooks.For(domain.PhasePreEnable)
	require.Len(t, pre, 1, "pre-enable scripts = %+v", pre)
	assert.Equal(t, domain.LangJS, pre[0].Language, "pre-enable scripts = %+v", pre)
}

// The point of the projection: the manifest a recipient reads tells them where
// the mod lives, so they can fetch it themselves instead of trusting a peer.
func TestFromModSourceLinksSurviveTheRoundTrip(t *testing.T) {
	dir := t.TempDir()

	_, err := Write(dir, FromMod(installedMod()))
	require.NoError(t, err, "write: %v", err)

	back, err := Read(dir)
	require.NoError(t, err, "read: %v", err)

	assert.NotEmpty(t, back.Mod.SourceURL, "source links = %+v", back.Mod)
	assert.NotEmpty(t, back.Mod.UpdateURL, "source links = %+v", back.Mod)

	assert.Equal(t, "12", back.Mod.SourceData["mod_id"], "source data = %v", back.Mod.SourceData)
	assert.Equal(t, "true", back.Mod.SourceData["verified"], "source data = %v", back.Mod.SourceData)
}

func TestWriteKeepsTheExistingManifestName(t *testing.T) {
	dir := t.TempDir()

	// A mod that already ships metadata.toml must not gain a competing mod.toml: Read
	// takes the first name in priority order, so a second file is ignored forever.
	err := os.WriteFile(
		filepath.Join(dir, "metadata.toml"),
		[]byte("[mod]\nid = \"x\"\n"),
		0o600,
	)
	require.NoError(t, err, "seed manifest: %v", err)

	path, err := Write(dir, FromMod(installedMod()))
	require.NoError(t, err, "write: %v", err)

	assert.Equal(t, "metadata.toml", filepath.Base(path), "wrote %s", path)

	_, err = os.Stat(filepath.Join(dir, Names[0]))
	assert.True(t, os.IsNotExist(err), "a second manifest was created")
}

func TestWriteRefusesAFolderThatIsNotThere(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")

	_, err := Write(missing, FromMod(installedMod()))
	assert.Error(t, err, "expected an error for a missing folder")
}

func TestWriteRefusesAFileAsAFolder(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-folder")

	err := os.WriteFile(file, []byte("x"), 0o600)
	require.NoError(t, err, "write file: %v", err)

	_, err = Write(file, FromMod(installedMod()))
	assert.True(t, appErrors.IsAppError(err, appErrors.ErrCodeInvalidMod), "error = %v, want INVALID_MOD", err)
}

func TestWriteRefusesABlankFolder(t *testing.T) {
	_, err := Write("  ", FromMod(installedMod()))
	assert.True(t, appErrors.IsAppError(
		err,
		appErrors.ErrCodeInvalidArgument,
	), "error = %v, want INVALID_ARGUMENT", err)
}

func TestEncodeRefusesNothing(t *testing.T) {
	_, err := Encode(nil)
	assert.True(t, appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument), "error = %v, want INVALID_ARGUMENT", err)
}

func TestFromModTolerantOfNothing(t *testing.T) {
	m := FromMod(nil)
	assert.NotNil(t, m, "a nil mod should project to an empty manifest")
	assert.Empty(t, m.Mod.ID, "a nil mod should project to an empty manifest")
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
			got := SourceDataValue(tc.value)
			assert.Equal(t, tc.want, got, "SourceDataValue(%v) = %q, want %q", tc.value, got, tc.want)
		})
	}
}

// A value with no sensible spelling must not fail the whole write: the rest of
// the record is still worth having, and the placeholder says what happened.
func TestSourceDataValueFallsBackForUnmarshalableValues(t *testing.T) {
	got := SourceDataValue(make(chan int))
	assert.NotEmpty(t, got, "expected a placeholder")
}

func TestSourceDataStringsDropsAnEmptyMap(t *testing.T) {
	assert.Nil(t, SourceDataStrings(nil), "SourceDataStrings(nil) = %v", SourceDataStrings(nil))

	assert.Nil(t, SourceDataStrings(map[string]any{}), "SourceDataStrings(empty) = %v", SourceDataStrings(map[string]any{}))
}

// A hook with no language cannot be written; skipping it keeps Write from
// emitting a file that fails to parse.
func TestFromModSkipsUnnamedHookScripts(t *testing.T) {
	mod := installedMod()
	mod.Hooks = domain.ModHooks{
		PostInstall: []domain.HookScript{{Content: "orphan"}},
	}

	m := FromMod(mod)
	assert.Empty(t, m.Hooks.PostInstall, "post install = %+v", m.Hooks.PostInstall)

	_, err := Encode(m)
	require.NoError(t, err, "encode: %v", err)
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
