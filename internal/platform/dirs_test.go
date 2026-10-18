package platform

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mapEnv(pairs map[string]string) Env {
	return func(key string) string { return pairs[key] }
}

func noEnv(string) string { return "" }

func TestResolveDirsWindows(t *testing.T) {
	cases := map[string]struct {
		home string
		env  map[string]string
		want AppDirs
	}{
		"known folders": {
			home: `C:\Users\tester`,
			env: map[string]string{
				"APPDATA":         `C:\Users\tester\AppData\Roaming`,
				"LOCALAPPDATA":    `C:\Users\tester\AppData\Local`,
				"USERPROFILE":     `C:\Users\tester`,
				"ProgramFiles":    `C:\Program Files`,
				"HOMEDRIVE":       "",
				"HOMEPATH":        "",
				"XDG_CONFIG_HOME": "",
			},
			want: AppDirs{
				Config: "C:/Users/tester/AppData/Roaming/darktide-mod-manager",
				Data:   "C:/Users/tester/AppData/Local/darktide-mod-manager",
				Cache:  "C:/Users/tester/AppData/Local/darktide-mod-manager/cache",
				Logs:   "C:/Users/tester/AppData/Local/darktide-mod-manager/logs",
			},
		},
		"blank appdata falls back to home": {
			home: `C:\Users\tester`,
			env:  map[string]string{"APPDATA": "", "LOCALAPPDATA": "   "},
			want: AppDirs{
				Config: "C:/Users/tester/AppData/Roaming/darktide-mod-manager",
				Data:   "C:/Users/tester/AppData/Local/darktide-mod-manager",
				Cache:  "C:/Users/tester/AppData/Local/darktide-mod-manager/cache",
				Logs:   "C:/Users/tester/AppData/Local/darktide-mod-manager/logs",
			},
		},
		"relative appdata is rejected": {
			home: `C:\Users\tester`,
			env:  map[string]string{"APPDATA": "Roaming", "LOCALAPPDATA": "."},
			want: AppDirs{
				Config: "C:/Users/tester/AppData/Roaming/darktide-mod-manager",
				Data:   "C:/Users/tester/AppData/Local/darktide-mod-manager",
				Cache:  "C:/Users/tester/AppData/Local/darktide-mod-manager/cache",
				Logs:   "C:/Users/tester/AppData/Local/darktide-mod-manager/logs",
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ResolveDirs(domain.PlatformWindows, tc.home, mapEnv(tc.env))
			require.NoError(t, err, "ResolveDirs")

			assert.Equal(t, tc.want, got, "ResolveDirs = %+v, want %+v", got, tc.want)
		})
	}
}

func TestResolveDirsWindowsDerivesHomeFromEnvironment(t *testing.T) {
	dirs, err := ResolveDirs(domain.PlatformWindows, "", mapEnv(map[string]string{
		"USERPROFILE": `D:\profiles\tester`,
		"APPDATA":     `D:\profiles\tester\AppData\Roaming`,
	}))
	require.NoError(t, err, "ResolveDirs")

	want := "D:/profiles/tester/AppData/Roaming/darktide-mod-manager"
	assert.Equal(t, want, dirs.Config, "Config = %q, want %q", dirs.Config, want)
}

func TestResolveDirsWindowsDrivePathPair(t *testing.T) {
	home, err := HomeDir(domain.PlatformWindows, mapEnv(map[string]string{
		"HOMEDRIVE": "E:",
		"HOMEPATH":  `\Users\tester`,
	}))
	require.NoError(t, err, "HomeDir")

	want := "E:/Users/tester"
	assert.Equal(t, want, home, "HomeDir = %q, want %q", home, want)
}

func TestResolveDirsWindowsRejectsPartialDrivePathPair(t *testing.T) {
	// HOMEDRIVE without HOMEPATH would otherwise join to "C:" and point at the
	// drive root, which is not a home directory.
	for name, env := range map[string]map[string]string{
		"drive only":         {"HOMEDRIVE": "C:"},
		"path only":          {"HOMEPATH": `\Users\tester`},
		"blank pair":         {"HOMEDRIVE": "  ", "HOMEPATH": "\t"},
		"empty pair":         {},
		"only usrprof empty": {"USERPROFILE": ""},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ResolveDirs(domain.PlatformWindows, "", mapEnv(env))
			require.Error(t, err, "ResolveDirs succeeded without a home directory, want error")
		})
	}
}

func TestResolveDirsDarwin(t *testing.T) {
	dirs, err := ResolveDirs(domain.PlatformDarwin, "/Users/tester", mapEnv(map[string]string{
		// Darwin must ignore XDG entirely.
		"XDG_CONFIG_HOME": "/tmp/config",
		"XDG_DATA_HOME":   "/tmp/data",
		"XDG_CACHE_HOME":  "/tmp/cache",
	}))
	require.NoError(t, err, "ResolveDirs")

	want := AppDirs{
		Config: "/Users/tester/Library/Application Support/darktide-mod-manager",
		Data:   "/Users/tester/Library/Application Support/darktide-mod-manager",
		Cache:  "/Users/tester/Library/Caches/darktide-mod-manager",
		Logs:   "/Users/tester/Library/Logs/darktide-mod-manager",
	}
	assert.Equal(t, want, dirs, "ResolveDirs = %+v, want %+v", dirs, want)
}

func TestResolveDirsXDG(t *testing.T) {
	cases := map[string]struct {
		home string
		env  map[string]string
		want AppDirs
	}{
		"defaults under home": {
			home: "/home/tester",
			env:  map[string]string{},
			want: AppDirs{
				Config: "/home/tester/.config/darktide-mod-manager",
				Data:   "/home/tester/.local/share/darktide-mod-manager",
				Cache:  "/home/tester/.cache/darktide-mod-manager",
				Logs:   "/home/tester/.local/state/darktide-mod-manager/logs",
			},
		},
		"xdg overrides": {
			home: "/home/tester",
			env: map[string]string{
				"XDG_CONFIG_HOME": "/xdg/config",
				"XDG_DATA_HOME":   "/xdg/data",
				"XDG_CACHE_HOME":  "/xdg/cache",
				"XDG_STATE_HOME":  "/xdg/state",
			},
			want: AppDirs{
				Config: "/xdg/config/darktide-mod-manager",
				Data:   "/xdg/data/darktide-mod-manager",
				Cache:  "/xdg/cache/darktide-mod-manager",
				Logs:   "/xdg/state/darktide-mod-manager/logs",
			},
		},
		"xdg data home does not hold logs": {
			home: "/home/tester",
			env: map[string]string{
				"XDG_DATA_HOME": "/xdg/data",
			},
			want: AppDirs{
				Config: "/home/tester/.config/darktide-mod-manager",
				Data:   "/xdg/data/darktide-mod-manager",
				Cache:  "/home/tester/.cache/darktide-mod-manager",
				Logs:   "/home/tester/.local/state/darktide-mod-manager/logs",
			},
		},
		"relative xdg values ignored": {
			home: "/home/tester",
			env: map[string]string{
				"XDG_CONFIG_HOME": "config",
				"XDG_DATA_HOME":   "./data",
				"XDG_CACHE_HOME":  "",
				"XDG_STATE_HOME":  "state",
			},
			want: AppDirs{
				Config: "/home/tester/.config/darktide-mod-manager",
				Data:   "/home/tester/.local/share/darktide-mod-manager",
				Cache:  "/home/tester/.cache/darktide-mod-manager",
				Logs:   "/home/tester/.local/state/darktide-mod-manager/logs",
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ResolveDirs(domain.PlatformLinux, tc.home, mapEnv(tc.env))
			require.NoError(t, err, "ResolveDirs")

			assert.Equal(t, tc.want, got, "ResolveDirs = %+v, want %+v", got, tc.want)
		})
	}
}

func TestResolveDirsRequiresHome(t *testing.T) {
	for _, p := range []domain.Platform{domain.PlatformLinux, domain.PlatformDarwin, domain.PlatformFreeBSD} {
		_, err := ResolveDirs(p, "   ", mapEnv(nil))
		assert.Error(t, err, "ResolveDirs(%q, blank home) succeeded, want error", p)
	}
}

func TestResolveDirsNilEnvIsSafe(t *testing.T) {
	dirs, err := ResolveDirs(domain.PlatformLinux, "/home/tester", nil)
	require.NoError(t, err, "ResolveDirs")

	want := "/home/tester/.config/darktide-mod-manager"
	assert.Equal(t, want, dirs.Config, "Config = %q, want %q", dirs.Config, want)
}

func TestHomeDir(t *testing.T) {
	cases := map[string]struct {
		platform domain.Platform
		env      map[string]string
		want     string
		wantErr  bool
	}{
		"unix uses HOME": {
			platform: domain.PlatformLinux,
			env:      map[string]string{"HOME": "/home/t/"},
			want:     "/home/t",
		},
		"unix ignores USERPROFILE": {
			platform: domain.PlatformLinux,
			env:      map[string]string{"USERPROFILE": "C:\\Users\\t"},
			wantErr:  true,
		},
		"darwin uses HOME": {
			platform: domain.PlatformDarwin,
			env:      map[string]string{"HOME": "/Users/t"},
			want:     "/Users/t",
		},
		"windows userprofile wins": {
			platform: domain.PlatformWindows,
			env: map[string]string{
				"USERPROFILE": "C:\\a",
				"HOMEDRIVE":   "D:",
				"HOMEPATH":    "\\b",
			},
			want: "C:/a",
		},
		"windows pair fallback": {
			platform: domain.PlatformWindows,
			env:      map[string]string{"HOMEDRIVE": "D:", "HOMEPATH": "\\b"},
			want:     "D:/b",
		},
		"windows nothing available": {platform: domain.PlatformWindows, env: nil, wantErr: true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := HomeDir(tc.platform, mapEnv(tc.env))
			if tc.wantErr {
				require.Error(t, err, "HomeDir = %q, want error", got)

				return
			}

			require.NoError(t, err, "HomeDir")

			assert.Equal(t, tc.want, got, "HomeDir = %q, want %q", got, tc.want)
		})
	}
}

func TestHomeDirNilEnv(t *testing.T) {
	_, err := HomeDir(domain.PlatformLinux, nil)
	require.Error(t, err, "HomeDir with nil env succeeded, want error")
}

func TestAppDirsPaths(t *testing.T) {
	dirs := AppDirs{
		Config: "/home/t/.config/darktide-mod-manager",
		Data:   "/home/t/.local/share/darktide-mod-manager",
		Cache:  "/home/t/.cache/darktide-mod-manager",
	}

	cases := map[string]struct{ got, want string }{
		"config file": {dirs.ConfigFilePath(), "/home/t/.config/darktide-mod-manager/config.json"},
		"index": {
			dirs.ModsIndexPath(),
			"/home/t/.local/share/darktide-mod-manager/mods_index.json",
		},
		"database":     {dirs.DatabasePath(), "/home/t/.local/share/darktide-mod-manager/mods.db"},
		"storage root": {dirs.StorageRoot(), "/home/t/.local/share/darktide-mod-manager/mods"},
	}
	for name, tc := range cases {
		assert.Equal(t, tc.want, tc.got, "%s = %q, want %q", name, tc.got, tc.want)
	}
}

func TestLooksAbsolute(t *testing.T) {
	cases := map[string]struct {
		in   string
		want bool
	}{
		"empty":               {"", false},
		"unix root":           {"/", true},
		"unix path":           {"/home/t", true},
		"windows drive":       {`C:\Users\t`, true},
		"windows drive slash": {`C:/Users/t`, true},
		"drive only":          {"C:", true},
		"unc":                 {`\\server\share`, true},
		"relative":            {"relative/path", false},
		"dot relative":        {"./x", false},
		"dotdot relative":     {"../x", false},
		"whitespace":          {"   ", false},
	}
	for name, tc := range cases {
		got := looksAbsolute(tc.in)
		assert.Equal(
			t,
			tc.want,
			got,
			"looksAbsolute(%q) [%s] = %v, want %v",
			tc.in,
			name,
			got,
			tc.want,
		)
	}
}

func TestCurrentDirs(t *testing.T) {
	dirs, err := CurrentDirs()
	if err != nil {
		require.False(
			t,
			runtime.GOOS == "linux" || runtime.GOOS == "darwin" || runtime.GOOS == "windows",
			"CurrentDirs: %v",
			err,
		)

		return
	}

	for _, e := range []struct{ name, path string }{{"Config", dirs.Config}, {"Data", dirs.Data}, {"Cache", dirs.Cache}} {
		name, path := e.name, e.path
		assert.NotEqual(t, "", path, "%s dir is empty", name)

		assert.True(
			t,
			strings.HasSuffix(path, AppName),
			"%s dir %q does not end with %q",
			name,
			path,
			AppName,
		)
	}
	// Logs live outside the data directory: each platform has its own convention
	// (Library/Logs on macOS, XDG_STATE_HOME elsewhere), so absoluteness is all.
	assert.True(t, filepath.IsAbs(dirs.Logs), "Logs dir %q is not absolute", dirs.Logs)

	want := filepath.ToSlash(filepath.Join(AppName, "logs"))
	assert.True(
		t,
		strings.HasSuffix(filepath.ToSlash(dirs.Logs), want),
		"Logs dir %q does not end in %q",
		dirs.Logs,
		want,
	)
}
