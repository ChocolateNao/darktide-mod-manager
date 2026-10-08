package platform

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// candidatePaths returns the paths of the candidates for a source.
func candidatePaths(cands []Candidate, source domain.GameSource) []string {
	var out []string

	for _, c := range cands {
		if c.Source == source {
			out = append(out, c.Path)
		}
	}

	return out
}

func hasCandidate(cands []Candidate, path string) bool {
	for _, c := range cands {
		if c.Path == path {
			return true
		}
	}

	return false
}

func TestGameCandidatesLinux(t *testing.T) {
	home := "/home/tester"
	cands := GameCandidates(domain.PlatformLinux, home, mapEnv(map[string]string{}), nil)

	want := []string{
		"/home/tester/.steam/steam/steamapps/common/Darktide",
		"/home/tester/.steam/steam/steamapps/compatdata/1361210/pfx/drive_c/Program Files (x86)/Steam/steamapps/common/Darktide",
		"/home/tester/.steam/root/steamapps/common/Darktide",
		"/home/tester/.local/share/Steam/steamapps/common/Darktide",
		"/home/tester/.var/app/com.valvesoftware.Steam/data/Steam/steamapps/common/Darktide",
		"/home/tester/.local/share/EpicGames/Darktide",
		"/home/tester/.EpicGames/Darktide",
		"/home/tester/.var/app/com.heroicgameslauncher.hgl/data/EpicGames/Darktide",
		"/home/tester/.local/share/heroic/prefixes/*/drive_c/Program Files (x86)/Steam/steamapps/common/Darktide",
		"/home/tester/.lutris/games/darktide/drive_c/Program Files (x86)/Steam/steamapps/common/Darktide",
		"/home/tester/.var/app/com.usebottles.bottles/data/bottles/bottles/*/drive_c/Program Files (x86)/Steam/steamapps/common/Darktide",
	}
	for _, p := range want {
		assert.True(t, hasCandidate(cands, p), "missing candidate %s", p)
	}

	// Every path must be absolute, which is what the detector insists on.
	for _, c := range cands {
		assert.True(t, strings.HasPrefix(c.Path, "/"), "candidate %q is not absolute", c.Path)

		assert.NotEqual(t, "", c.Notes, "candidate %q has no notes", c.Path)
	}

	// Proton is a distinct source so the UI can explain where the game lives.
	proton := candidatePaths(cands, domain.GameSourceProton)
	assert.Len(t, proton, 4, "proton candidates = %v, want one per Steam root", proton)

	for _, p := range proton {
		assert.Contains(t, p, domain.SteamAppID, "proton candidate %q does not name the app id", p)
	}
}

func TestGameCandidatesLinuxHonoursXDGAndHeroicEnv(t *testing.T) {
	cands := GameCandidates(domain.PlatformLinux, "/home/tester", mapEnv(map[string]string{
		"XDG_DATA_HOME":        "/data",
		"HEROIC_PREFIXES_PATH": "/heroic",
	}), nil)

	assert.True(
		t,
		hasCandidate(cands, "/data/EpicGames/Darktide"),
		"XDG_DATA_HOME not honoured for Epic",
	)

	assert.True(
		t,
		hasCandidate(
			cands,
			"/heroic/*/drive_c/Program Files (x86)/Steam/steamapps/common/Darktide",
		),
		"HEROIC_PREFIXES_PATH not honoured",
	)

	assert.False(
		t,
		hasCandidate(cands, "/home/tester/.local/share/EpicGames/Darktide"),
		"default data home still present when XDG_DATA_HOME is set",
	)
}

func TestGameCandidatesLinuxIgnoresRelativeXDG(t *testing.T) {
	cands := GameCandidates(domain.PlatformLinux, "/home/tester", mapEnv(map[string]string{
		"XDG_DATA_HOME":        "relative/data",
		"HEROIC_PREFIXES_PATH": "./prefixes",
	}), nil)
	assert.True(
		t,
		hasCandidate(cands, "/home/tester/.local/share/EpicGames/Darktide"),
		"relative XDG_DATA_HOME should fall back to the default",
	)

	assert.True(
		t,
		hasCandidate(
			cands,
			"/home/tester/.local/share/heroic/prefixes/*/drive_c/Program Files (x86)/Steam/steamapps/common/Darktide",
		),
		"relative HEROIC_PREFIXES_PATH should fall back to the default",
	)
}

func TestGameCandidatesDarwin(t *testing.T) {
	cands := GameCandidates(
		domain.PlatformDarwin,
		"/Users/tester",
		mapEnv(map[string]string{}),
		nil,
	)

	want := []string{
		"/Users/tester/Library/Application Support/Steam/steamapps/common/Darktide",
		"/Users/tester/Library/Application Support/Steam/steamapps/compatdata/1361210/pfx/drive_c/Program Files (x86)/Steam/steamapps/common/Darktide",
		"/Users/tester/Applications/Steam.app/Contents/Resources/steamapps/common/Darktide",
		"/Users/tester/Library/Application Support/CrossOver/Bottles/*/drive_c/Program Files (x86)/Steam/steamapps/common/Darktide",
		"/Users/tester/Library/Application Support/com.whiskyapp.whisky/bottles/*/drive_c/Program Files (x86)/Steam/steamapps/common/Darktide",
		"/Users/tester/Parallels/Darktide/drive_c/Program Files (x86)/Steam/steamapps/common/Darktide",
		"/Users/tester/Library/Application Support/Epic/Darktide",
	}
	for _, p := range want {
		assert.True(t, hasCandidate(cands, p), "missing candidate %s", p)
	}

	// Linux-only sources must not appear on macOS.
	for _, source := range []domain.GameSource{domain.GameSourceBottles, domain.GameSourceLutris, domain.GameSourceFlatpak} {
		got := candidatePaths(cands, source)
		assert.Empty(t, got, "%s candidates on macOS: %v", source, got)
	}

	var globs int

	for _, c := range cands {
		if c.Glob {
			globs++

			assert.Equal(
				t,
				1,
				strings.Count(c.Path, "*"),
				"glob candidate %q has %d wildcards, want 1",
				c.Path,
				strings.Count(c.Path, "*"),
			)
		}
	}

	assert.Equal(t, 2, globs, "glob candidates = %d, want 2 (CrossOver, Whisky)", globs)
}

func TestGameCandidatesWindows(t *testing.T) {
	cands := GameCandidates(domain.PlatformWindows, `C:\Users\tester`, mapEnv(map[string]string{
		"ProgramFiles(x86)": `C:\Program Files (x86)`,
		"ProgramFiles":      `C:\Program Files`,
		"SteamPath":         `D:\Steam`,
	}), nil)

	want := []string{
		"D:/Steam/steamapps/common/Darktide",
		"C:/Program Files (x86)/Steam/steamapps/common/Darktide",
		"C:/Program Files/Steam/steamapps/common/Darktide",
		"C:/Program Files (x86)/Epic Games/Darktide",
		"C:/Program Files/Epic Games/Darktide",
		"C:/Program Files (x86)/ModsEasy/Darktide",
	}
	for _, p := range want {
		assert.True(t, hasCandidate(cands, p), "missing candidate %s", p)
	}

	// Windows has no Proton prefix to probe, and no Unix-only sources.
	for _, source := range []domain.GameSource{
		domain.GameSourceProton, domain.GameSourceFlatpak, domain.GameSourceLutris,
		domain.GameSourceBottles, domain.GameSourceCrossOver, domain.GameSourceWhisky,
		domain.GameSourceParallels, domain.GameSourceHeroic,
	} {
		got := candidatePaths(cands, source)
		assert.Empty(t, got, "%s candidates on Windows: %v", source, got)
	}

	for _, c := range cands {
		assert.False(t, c.Glob, "unexpected glob candidate %q", c.Path)
	}
}

func TestGameCandidatesWindowsWithoutEnvironment(t *testing.T) {
	cands := GameCandidates(domain.PlatformWindows, "", noEnv, nil)
	assert.Len(t, cands, 0, "candidates = %v, want none with no environment and no home", cands)

	got := GameCandidates(domain.PlatformWindows, "", nil, nil)
	assert.Empty(t, got, "candidates with a nil env = %v, want none", got)
}

func TestGameCandidatesExtrasComeFirst(t *testing.T) {
	cands := GameCandidates(domain.PlatformLinux, "/home/tester", noEnv, []string{
		"  /opt/games/Darktide  ",
		"",
		"   ",
	})
	require.NotEmpty(t, cands, "no candidates produced")

	assert.Equal(
		t,
		"/opt/games/Darktide",
		cands[0].Path,
		"first candidate = %q, want the manual path",
		cands[0].Path,
	)

	assert.Equal(
		t,
		domain.GameSourceManual,
		cands[0].Source,
		"first candidate source = %q, want %q",
		cands[0].Source,
		domain.GameSourceManual,
	)

	for _, c := range cands {
		assert.NotEqual(t, "", c.Path, "a blank extra produced a candidate")
	}
}

func TestExpandCandidates(t *testing.T) {
	root := t.TempDir()

	prefix := filepath.Join(root, "prefixes")
	for _, name := range []string{"zeta", "alpha", "beta"} {
		err := os.MkdirAll(filepath.Join(prefix, name, "drive_c"), 0o755)
		require.NoError(t, err)
	}

	pattern := filepath.ToSlash(filepath.Join(prefix, "*")) + "/drive_c"

	got := ExpandCandidates([]Candidate{
		{Path: pattern, Source: domain.GameSourceHeroic, Glob: true},
		{Path: "/nonexistent/*", Source: domain.GameSourceHeroic, Glob: true},
		{Path: "/plain/path", Source: domain.GameSourceManual},
		{Path: "/plain/path/", Source: domain.GameSourceManual},
		{Path: "", Source: domain.GameSourceManual},
	})

	require.Len(t, got, 4, "expanded = %d entries, want 4: %+v", len(got), got)
	// Matches come back sorted, which keeps detection order deterministic.
	assert.Contains(t, got[0].Path, "/alpha/", "glob results not sorted: %v", got)
	assert.Contains(t, got[1].Path, "/beta/", "glob results not sorted: %v", got)

	for _, c := range got {
		assert.False(t, c.Glob, "Glob flag survived expansion: %+v", c)
	}

	assert.Equal(t, "/plain/path", got[3].Path, "plain path = %q", got[3].Path)
}

func TestExpandCandidatesIgnoresMalformedGlobs(t *testing.T) {
	got := ExpandCandidates(
		[]Candidate{{Path: "/tmp/[unclosed", Glob: true}, {Path: "/not/a/glob", Glob: true}},
	)
	assert.Len(t, got, 0, "expanded = %+v, want nothing", got)

	got = ExpandCandidates(nil)
	assert.Empty(t, got, "ExpandCandidates(nil) = %+v", got)
}

// gameDir builds a fake installation on the real disk, which keeps the detector
// tests honest without needing a second filesystem implementation.
func gameDir(t *testing.T, modsSubdir string, files map[string]string) string {
	t.Helper()

	dir := t.TempDir()
	if modsSubdir != "" {
		err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(modsSubdir)), 0o755)
		require.NoError(t, err)
	}

	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		err := os.MkdirAll(filepath.Dir(p), 0o755)
		require.NoError(t, err)

		err = os.WriteFile(p, []byte(content), 0o644)
		require.NoError(t, err)
	}

	return dir
}

func TestValidateInstallationAccepts(t *testing.T) {
	cases := map[string]struct {
		files     map[string]string
		modsSub   string
		wantMods  string
		wantValid bool
	}{
		"windows executable only": {
			files: map[string]string{"Darktide.exe": "binary"},
		},
		"unix data folder": {
			files:    map[string]string{"data/mods/keep.txt": "x"},
			modsSub:  "data/mods",
			wantMods: "data/mods",
		},
		"unix mods folder at root": {
			files:    map[string]string{"mods/keep.txt": "x"},
			modsSub:  "mods",
			wantMods: "mods",
		},
		"proton pfx with executable": {
			files: map[string]string{"Darktide.exe": "binary"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := gameDir(t, tc.modsSub, tc.files)
			d := NewDetector(domain.PlatformLinux, dir, noEnv, nil)

			got, err := d.ValidateInstallation(dir)
			require.NoError(t, err, "ValidateInstallation")

			assert.True(t, got.Valid, "Valid = false")

			wantMods := filepath.ToSlash(filepath.Join(dir, domain.ModsDirName))
			if tc.wantMods != "" {
				wantMods = filepath.ToSlash(filepath.Join(dir, filepath.FromSlash(tc.wantMods)))
			}

			assert.Equal(
				t,
				wantMods,
				got.ModsPath,
				"ModsPath = %q, want %q",
				got.ModsPath,
				wantMods,
			)

			assert.Equal(
				t,
				filepath.ToSlash(dir),
				got.Path,
				"Path = %q, want %q",
				got.Path,
				filepath.ToSlash(dir),
			)

			assert.Equal(
				t,
				domain.GameSourceManual,
				got.Source,
				"Source = %q, want %q",
				got.Source,
				domain.GameSourceManual,
			)

			assert.Equal(t, domain.PlatformLinux, got.Platform, "Platform = %q", got.Platform)

			assert.False(t, got.DetectedAt.IsZero(), "DetectedAt is zero")
		})
	}
}

func TestValidateInstallationModsPathPrecedence(t *testing.T) {
	dir := gameDir(t, "", map[string]string{
		"Darktide.exe":         "binary",
		"data/mods/a.txt":      "x",
		"mods/b.txt":           "x",
		"b3data/temp/mods/c.d": "x",
	})
	d := NewDetector(domain.PlatformLinux, dir, noEnv, nil)

	got, err := d.ValidateInstallation(dir)
	require.NoError(t, err, "ValidateInstallation")

	want := filepath.ToSlash(filepath.Join(dir, "data", "mods"))
	assert.Equal(
		t,
		want,
		got.ModsPath,
		"ModsPath = %q, want %q (data/mods wins)",
		got.ModsPath,
		want,
	)

	// Without data/mods, the root mods folder is used.
	dir2 := gameDir(t, "", map[string]string{"Darktide.exe": "binary", "mods/b.txt": "x"})

	got2, err := d.ValidateInstallation(dir2)
	require.NoError(t, err)

	want = filepath.ToSlash(filepath.Join(dir2, "mods"))
	assert.Equal(t, want, got2.ModsPath, "ModsPath = %q, want %q", got2.ModsPath, want)
}

func TestValidateInstallationRejects(t *testing.T) {
	notAGame := gameDir(t, "", map[string]string{"readme.txt": "not a game"})
	isAGame := gameDir(t, "", map[string]string{"Darktide.exe": "binary"})
	d := NewDetector(domain.PlatformLinux, notAGame, noEnv, nil)

	cases := map[string]struct {
		path    string
		wantErr bool
	}{
		"empty":       {"", true},
		"blank":       {"   ", true},
		"relative":    {"relative/path", true},
		"missing dir": {filepath.Join(notAGame, "nope"), true},
		"not a game":  {notAGame, true},
		"game":        {isAGame, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := d.ValidateInstallation(tc.path)
			require.False(
				t,
				tc.wantErr && err == nil,
				"ValidateInstallation(%q) succeeded, want error",
				tc.path,
			)

			require.False(
				t,
				!tc.wantErr && err != nil,
				"ValidateInstallation(%q): %v",
				tc.path,
				err,
			)

			if err != nil {
				code := errors.CodeOf(err)

				switch name {
				case "relative", "not a game":
					assert.Equal(
						t,
						errors.ErrCodeGameInvalid,
						code,
						"code = %q, want %q",
						code,
						errors.ErrCodeGameInvalid,
					)
				case "missing dir":
					assert.Equal(
						t,
						errors.ErrCodeGameNotFound,
						code,
						"code = %q, want %q",
						code,
						errors.ErrCodeGameNotFound,
					)
				}
			}
		})
	}
}

func TestValidateInstallationNormalisesPath(t *testing.T) {
	dir := gameDir(t, "", map[string]string{"Darktide.exe": "binary"})
	d := NewDetector(domain.PlatformLinux, dir, noEnv, nil)

	// Trailing separators and duplicate separators are cleaned away.
	for _, in := range []string{dir + "/", dir + "//", dir + "/./"} {
		got, err := d.ValidateInstallation(in)
		require.NoError(t, err, "ValidateInstallation(%q): %v", in, err)

		assert.Equal(
			t,
			filepath.ToSlash(dir),
			got.Path,
			"Path for %q = %q, want %q",
			in,
			got.Path,
			filepath.ToSlash(dir),
		)
	}
}

func TestValidateInstallationWithSource(t *testing.T) {
	dir := gameDir(t, "", map[string]string{"Darktide.exe": "binary"})
	d := NewDetector(domain.PlatformWindows, dir, noEnv, nil)

	got, err := d.ValidateInstallationWithSource(dir, domain.GameSourceSteam)
	require.NoError(t, err, "ValidateInstallationWithSource")

	assert.Equal(
		t,
		domain.GameSourceSteam,
		got.Source,
		"Source = %q, want %q",
		got.Source,
		domain.GameSourceSteam,
	)

	assert.Equal(
		t,
		domain.PlatformWindows,
		got.Platform,
		"Platform = %q, want windows",
		got.Platform,
	)
}

func TestValidateInstallationDetectsVersion(t *testing.T) {
	cases := map[string]struct {
		files map[string]string
		want  string
	}{
		"plain version file": {
			map[string]string{"Darktide.exe": "binary", "version.txt": "1.2.3\n"},
			"1.2.3",
		},
		"embedded version": {
			map[string]string{"Darktide.exe": "binary", "version.txt": "build 0.38.4917 done"},
			"0.38.4917",
		},
		"four part version": {
			map[string]string{"Darktide.exe": "binary", "version.txt": "2.1.0.1234"},
			"2.1.0.1234",
		},
		"no version": {
			map[string]string{"Darktide.exe": "binary", "version.txt": "unknown"},
			"",
		},
		"single number only": {
			map[string]string{"Darktide.exe": "binary", "version.txt": "12345"},
			"",
		},
		"missing marker": {map[string]string{"Darktide.exe": "binary"}, ""},
		"empty marker":   {map[string]string{"Darktide.exe": "binary", "version.txt": ""}, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := gameDir(t, "", tc.files)
			d := NewDetector(domain.PlatformLinux, dir, noEnv, nil)

			got, err := d.ValidateInstallation(dir)
			require.NoError(t, err, "ValidateInstallation")

			assert.Equal(t, tc.want, got.Version, "Version = %q, want %q", got.Version, tc.want)
		})
	}
}

func TestGetModsPath(t *testing.T) {
	install, err := (&Detector{}).ValidateInstallation(gameDir(t, "", map[string]string{
		"Darktide.exe":    "binary",
		"data/mods/a.txt": "x",
	}))
	require.NoError(t, err)

	d := NewDetector(domain.PlatformLinux, "", noEnv, nil)

	got, err := d.GetModsPath(install, "")
	require.NoError(t, err, "GetModsPath")

	assert.Equal(t, install.ModsPath, got, "GetModsPath = %q, want %q", got, install.ModsPath)

	override := gameDir(t, "custom", map[string]string{"keep": "x"})

	got, err = d.GetModsPath(install, override)
	require.NoError(t, err, "GetModsPath with override")

	assert.Equal(
		t,
		filepath.ToSlash(override),
		got,
		"override path = %q, want %q",
		got,
		filepath.ToSlash(override),
	)

	// A blank override is not an override.
	_, err = d.GetModsPath(install, "   ")
	assert.NoError(t, err, "blank override rejected: %v", err)
}

func TestGetModsPathRejects(t *testing.T) {
	dir := gameDir(t, "", map[string]string{"Darktide.exe": "binary", "data/mods/a.txt": "x"})
	d := NewDetector(domain.PlatformLinux, dir, noEnv, nil)

	install, err := d.ValidateInstallation(dir)
	require.NoError(t, err)

	cases := map[string]struct {
		install  *domain.GameInstallation
		override string
		wantCode errors.Code
	}{
		"nil install":       {nil, "", errors.ErrCodeGameInvalid},
		"relative override": {install, "mods", errors.ErrCodeGameInvalid},
		"missing override":  {install, "/definitely/not/here", errors.ErrCodeModsPathNotDir},
		"override is a file": {
			install,
			filepath.Join(dir, "Darktide.exe"),
			errors.ErrCodeModsPathNotDir,
		},
		"empty mods path": {&domain.GameInstallation{Path: dir}, "", errors.ErrCodeGameInvalid},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := d.GetModsPath(tc.install, tc.override)
			require.Error(t, err, "GetModsPath = %q, want error", got)

			code := errors.CodeOf(err)
			assert.Equal(t, tc.wantCode, code, "code = %q, want %q", code, tc.wantCode)
		})
	}
}

func TestGetModsPathMissingDefaultFolder(t *testing.T) {
	// An installation whose mods folder was deleted after detection must not
	// silently hand back a path that does not exist.
	dir := gameDir(t, "", map[string]string{"Darktide.exe": "binary"})
	d := NewDetector(domain.PlatformLinux, dir, noEnv, nil)

	install, err := d.ValidateInstallation(dir)
	require.NoError(t, err)

	if _, err := d.GetModsPath(install, ""); err == nil {
		require.FailNow(t, "GetModsPath succeeded with a deleted mods folder")
	} else if code := errors.CodeOf(err); code != errors.ErrCodeModsPathNotDir {
		assert.Fail(t, "code = %q, want %q", code, errors.ErrCodeModsPathNotDir)
	}
}

func TestFindInstallations(t *testing.T) {
	home := t.TempDir()

	steamGame := filepath.Join(home, ".steam", "steam", "steamapps", "common", "Darktide")
	err := os.MkdirAll(steamGame, 0o755)
	require.NoError(t, err)

	err = os.WriteFile(filepath.Join(steamGame, "Darktide.exe"), []byte("x"), 0o644)
	require.NoError(t, err)
	// A prefix that exists but holds no game must be ignored.
	emptyPrefix := filepath.Join(
		home,
		".steam",
		"steam",
		"steamapps",
		"compatdata",
		domain.SteamAppID,
		"pfx",
	)
	err = os.MkdirAll(emptyPrefix, 0o755)
	require.NoError(t, err)
	// A folder full of unrelated files must be ignored too.
	err = os.MkdirAll(filepath.Join(home, ".local", "share", "EpicGames", "Darktide"), 0o755)
	require.NoError(t, err)

	d := NewDetector(domain.PlatformLinux, home, noEnv, nil)

	found, err := d.FindInstallations(context.Background())
	require.NoError(t, err, "FindInstallations")

	require.Len(t, found, 1, "found %d installations, want 1: %+v", len(found), found)

	assert.Equal(
		t,
		domain.GameSourceSteam,
		found[0].Source,
		"Source = %q, want %q",
		found[0].Source,
		domain.GameSourceSteam,
	)

	assert.True(
		t,
		strings.EqualFold(found[0].Path, filepath.ToSlash(steamGame)),
		"Path = %q, want %q",
		found[0].Path,
		filepath.ToSlash(steamGame),
	)
}

func TestFindInstallationsEmptyAndNilContext(t *testing.T) {
	d := NewDetector(domain.PlatformLinux, t.TempDir(), noEnv, nil)
	//nolint:staticcheck // passing a nil context is exactly the edge case under test.
	found, err := d.FindInstallations(nil)
	require.NoError(t, err, "FindInstallations with a nil context")

	assert.Len(t, found, 0, "found %+v, want none", found)

	found, err = d.FindInstallations(context.Background())
	assert.False(
		t,
		err != nil || len(found) != 0,
		"FindInstallations on a bare home = %+v, %v",
		found,
		err,
	)
}

func TestFindInstallationsHonoursCancelledContext(t *testing.T) {
	home := t.TempDir()

	game := filepath.Join(home, ".steam", "steam", "steamapps", "common", "Darktide")
	err := os.MkdirAll(game, 0o755)
	require.NoError(t, err)

	err = os.WriteFile(filepath.Join(game, "Darktide.exe"), []byte("x"), 0o644)
	require.NoError(t, err)

	d := NewDetector(domain.PlatformLinux, home, noEnv, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	found, err := d.FindInstallations(ctx)
	require.Error(t, err, "FindInstallations with a cancelled context = %+v, want error", found)

	assert.Len(t, found, 0, "found %+v after cancellation, want none", found)

	code := errors.CodeOf(err)
	assert.Equal(
		t,
		errors.ErrCodeOperationCanceled,
		code,
		"code = %q, want %q",
		code,
		errors.ErrCodeOperationCanceled,
	)
}

func TestFindInstallationsDeduplicates(t *testing.T) {
	home := t.TempDir()

	game := filepath.Join(home, ".steam", "steam", "steamapps", "common", "Darktide")
	err := os.MkdirAll(game, 0o755)
	require.NoError(t, err)

	err = os.WriteFile(filepath.Join(game, "Darktide.exe"), []byte("x"), 0o644)
	require.NoError(t, err)
	// The same path, spelled differently, must be reported once.
	d := NewDetector(domain.PlatformLinux, home, noEnv, []string{game + "/", game})

	found, err := d.FindInstallations(context.Background())
	require.NoError(t, err)

	assert.Len(t, found, 1, "found %d installations, want 1: %+v", len(found), found)
	// The manual candidate wins, because it is probed first.
	assert.Equal(
		t,
		domain.GameSourceManual,
		found[0].Source,
		"Source = %q, want %q",
		found[0].Source,
		domain.GameSourceManual,
	)
}

func TestNewDetectorResolvesHomeFromEnvironment(t *testing.T) {
	d := NewDetector(
		domain.PlatformLinux,
		"",
		mapEnv(map[string]string{"HOME": "/home/tester/"}),
		nil,
	)
	assert.Equal(t, "/home/tester", d.home, "home = %q, want /home/tester", d.home)

	assert.Equal(t, "/home/tester/", d.getenv("HOME"), "getenv not retained")

	assert.Equal(t, domain.PlatformLinux, d.Platform(), "Platform() = %q", d.Platform())

	// With no HOME the detector still works, it just searches nothing.
	got := NewDetector(domain.PlatformLinux, "", noEnv, nil)
	assert.Equal(t, "", got.home, "home = %q, want empty", got.home)
}

func TestNewDetectorWithFSNilFallsBackToReal(t *testing.T) {
	d := NewDetectorWithFS(domain.PlatformLinux, "/home/tester", noEnv, nil, nil)
	assert.NotNil(t, d.fs, "filesystem is nil")
}

func TestExtractVersion(t *testing.T) {
	cases := map[string]struct {
		in   string
		want string
	}{
		"simple":        {"1.2.3", "1.2.3"},
		"surrounded":    {"version=4.5.6 build", "4.5.6"},
		"leading dots":  {"...1.2.3...", "1.2.3"},
		"multiple runs": {"offset 12345 then 6.7.8", "6.7.8"},
		"no dots":       {"12345", ""},
		"letters only":  {"beta", ""},
		"empty":         {"", ""},
		"dots only":     {"...", ""},
		"mixed":         {"1.2.3-alpha", "1.2.3"},
	}
	for name, tc := range cases {
		got := extractVersion(tc.in)
		assert.Equal(
			t,
			tc.want,
			got,
			"%s: extractVersion(%q) = %q, want %q",
			name,
			tc.in,
			got,
			tc.want,
		)
	}
}

func TestIsVersionLike(t *testing.T) {
	cases := map[string]struct {
		in   string
		want bool
	}{
		"1.2":   {"1.2", true},
		"1":     {"1", false},
		"":      {"", false},
		".":     {".", false},
		"a.b":   {"a.b", false},
		"1.2.a": {"1.2.a", false},
		"1..2":  {"1..2", true},
	}
	for name, tc := range cases {
		got := isVersionLike(tc.in)
		assert.Equal(
			t,
			tc.want,
			got,
			"%s: isVersionLike(%q) = %v, want %v",
			name,
			tc.in,
			got,
			tc.want,
		)
	}
}

func TestDetectVersionSkipsOversizedMarkers(t *testing.T) {
	// A version.txt larger than the read limit must not be slurped into memory.
	dir := gameDir(t, "", map[string]string{
		"Darktide.exe": "binary",
		"version.txt":  strings.Repeat("x", 128*1024),
	})
	d := NewDetector(domain.PlatformLinux, dir, noEnv, nil)

	got, err := d.ValidateInstallation(dir)
	require.NoError(t, err, "ValidateInstallation")

	assert.Equal(
		t,
		"",
		got.Version,
		"Version = %q, want empty for an oversized marker",
		got.Version,
	)
}

func TestDetectorDetectedAtIsInjectable(t *testing.T) {
	// Sanity check that the detector stamps a timestamp, since services sort by
	// it. Uses the real clock through the default constructor.
	dir := gameDir(t, "", map[string]string{"Darktide.exe": "binary"})
	d := NewDetector(domain.PlatformLinux, dir, noEnv, nil)
	before := time.Now().Add(-time.Second)

	got, err := d.ValidateInstallation(dir)
	require.NoError(t, err)

	assert.False(
		t,
		got.DetectedAt.Before(before) || got.DetectedAt.After(time.Now().Add(time.Second)),
		"DetectedAt = %v, want roughly now",
		got.DetectedAt,
	)
}
