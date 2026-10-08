// Package platform answers every "what does the host operating system do?"
// question the rest of the backend needs: where the config/data/cache
// directories live, which shell runs a hook, how a mod folder is exposed to
// the game, and where a Darktide installation might be hiding.
//
// Nothing in this package reads global process state implicitly. The platform,
// the home directory and the environment are injected, so all behaviour —
// including Windows and macOS behaviour — is reachable from tests on any host.
package platform

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// AppName is the directory name used under every per-user base directory.
const AppName = "darktide-mod-manager"

// Env reads an environment variable. os.Getenv satisfies it; tests pass a map.
type Env func(key string) string

// OSEnv reads the real process environment.
func OSEnv() Env { return os.Getenv }

// AppDirs holds the resolved per-platform directories. All four paths are
// absolute and already include AppName.
type AppDirs struct {
	// Config holds config.json.
	Config string
	// Data holds the mod index and the manager's own storage.
	Data string
	// Cache holds downloaded archives and other regenerable data.
	Cache string
	// Logs holds rotated log files.
	Logs string
}

// ResolveDirs computes the application directories for a platform.
//
// Resolution follows each platform's convention: Known Folders on Windows,
// Application Support on macOS, the XDG Base Directory spec elsewhere. When an
// environment variable is unset or empty the documented fallback under the home
// directory is used, so the manager still works with a bare environment.
func ResolveDirs(p domain.Platform, home string, getenv Env) (AppDirs, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}

	home = strings.TrimSpace(home)
	if p.IsWindows() && home == "" {
		home = windowsHome(getenv)
	}

	if home == "" {
		return AppDirs{}, errors.New(
			errors.ErrCodeNoStoragePath,
			"cannot determine the home directory for platform %q: set HOME (or USERPROFILE) and retry",
			p,
		)
	}

	var dirs AppDirs

	switch {
	case p.IsWindows():
		appData := envPath(getenv, "APPDATA", home, "AppData", "Roaming")
		localAppData := envPath(getenv, "LOCALAPPDATA", home, "AppData", "Local")
		dirs.Config = domain.JoinPath(appData, AppName)
		dirs.Data = domain.JoinPath(localAppData, AppName)
		dirs.Cache = domain.JoinPath(dirs.Data, "cache")
		// Windows has no dedicated log location; %LOCALAPPDATA% is where a
		// desktop app's own files belong.
		dirs.Logs = domain.JoinPath(dirs.Data, "logs")
	case p.IsDarwin():
		appSupport := domain.JoinPath(home, "Library", "Application Support", AppName)
		dirs.Config = appSupport
		dirs.Data = appSupport
		dirs.Cache = domain.JoinPath(home, "Library", "Caches", AppName)
		// Logs are user-facing diagnostics, not application data, so they go
		// where Console.app and every other native tool looks for them.
		dirs.Logs = domain.JoinPath(home, "Library", "Logs", AppName)
	default:
		dirs.Config = xdgPath(getenv, "XDG_CONFIG_HOME", home, ".config")
		dirs.Data = xdgPath(getenv, "XDG_DATA_HOME", home, ".local", "share")
		dirs.Cache = xdgPath(getenv, "XDG_CACHE_HOME", home, ".cache")
		// XDG_STATE_HOME is the spec's home for state that should persist but
		// need not be synced or backed up, which is exactly what a log is. It is
		// newer than the variables above, so ~/.local/state is the fallback.
		dirs.Logs = xdgPath(getenv, "XDG_STATE_HOME", home, ".local", "state")
		dirs.Logs = domain.JoinPath(dirs.Logs, "logs")
	}

	return dirs, nil
}

// CurrentDirs resolves the application directories for the running host.
func CurrentDirs() (AppDirs, error) {
	home, err := HomeDir(domain.CurrentPlatform(), OSEnv())
	if err != nil {
		return AppDirs{}, err
	}

	return ResolveDirs(domain.CurrentPlatform(), home, OSEnv())
}

// HomeDir returns the user's home directory from the environment. Windows is
// resolved from USERPROFILE or the HOMEDRIVE/HOMEPATH pair; everything else from
// HOME.
func HomeDir(p domain.Platform, getenv Env) (string, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}

	var home string
	if p.IsWindows() {
		home = windowsHome(getenv)
	} else {
		home = envValue(getenv, "HOME")
	}

	if home == "" {
		return "", errors.New(errors.ErrCodeNoStoragePath,
			"home directory is not set: set %s and retry", homeEnvName(p))
	}

	return domain.CleanPath(home), nil
}

func windowsHome(getenv Env) string {
	if profile := envValue(getenv, "USERPROFILE"); profile != "" {
		return profile
	}

	drive := envValue(getenv, "HOMEDRIVE")

	path := envValue(getenv, "HOMEPATH")
	if drive != "" && path != "" {
		return drive + path
	}
	// A partial pair (drive without path, or vice versa) is unusable: joining it
	// would silently produce a path such as "C:" that resolves to the drive root.
	return ""
}

func homeEnvName(p domain.Platform) string {
	if p.IsWindows() {
		return "USERPROFILE (or HOMEDRIVE plus HOMEPATH)"
	}

	return "HOME"
}

// envValue reads a variable and treats a blank value as unset. Windows
// environments routinely carry empty variables, and joining an empty base
// directory would silently relocate the app to the drive root.
func envValue(getenv Env, key string) string {
	return strings.TrimSpace(getenv(key))
}

// envPath reads an environment variable as a base directory, falling back to the
// given segments under home when the variable is blank or not absolute.
func envPath(getenv Env, key, home string, fallback ...string) string {
	if v := envValue(getenv, key); looksAbsolute(v) {
		return domain.CleanPath(v)
	}

	return domain.JoinPath(append([]string{home}, fallback...)...)
}

// xdgPath applies the XDG Base Directory spec: use the variable when it holds an
// absolute path, otherwise use the specified fallback under home. A relative
// XDG value is ignored on purpose, since resolving it against the process
// working directory would make the manager's data location depend on where it
// happens to be started. AppName is always appended.
func xdgPath(getenv Env, key, home string, fallback ...string) string {
	base := envPath(getenv, key, home, fallback...)
	if base == "" {
		return ""
	}

	return domain.JoinPath(base, AppName)
}

// looksAbsolute reports whether a path is absolute under either this host's
// rules or Windows' rules. Windows detection is needed because a config written
// on Windows is inspected on Linux when the storage is shared, and because tests
// for Windows directory layouts run on Unix hosts.
func looksAbsolute(p string) bool {
	if p == "" {
		return false
	}

	if filepath.IsAbs(p) {
		return true
	}

	c := domain.CleanPath(p)
	if strings.HasPrefix(c, "/") {
		return true
	}
	// UNC path: //server/share
	if strings.HasPrefix(c, "//") {
		return true
	}
	// Drive-qualified path: C:/... or C:
	if len(c) >= 2 && c[1] == ':' && isASCIILetter(c[0]) {
		return true
	}

	return false
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// ConfigFilePath returns the path of config.json inside the config directory.
func (d AppDirs) ConfigFilePath() string { return domain.JoinPath(d.Config, "config.json") }

// ModsIndexPath returns the path of the mod index inside the data directory.
func (d AppDirs) ModsIndexPath() string { return domain.JoinPath(d.Data, "mods_index.json") }

// DatabasePath returns the SQLite database path inside the data directory.
func (d AppDirs) DatabasePath() string { return domain.JoinPath(d.Data, "mods.db") }

// StorageRoot returns the directory that holds downloaded mod payloads.
func (d AppDirs) StorageRoot() string { return domain.JoinPath(d.Data, "mods") }
