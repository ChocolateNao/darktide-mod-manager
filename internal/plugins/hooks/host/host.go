// Package host is the restricted capability surface a hook script runs against.
//
// Every language binding exposes the same operations and every one resolves
// through this Host type: a single construction site (New), the permission check
// before a provider is touched, and injected providers, so a script can never
// reach the network, a subprocess or the process environment.
package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

const (
	logLineTruncation = "…"

	// maxReadBytes is the largest file a script may read in one call.
	maxReadBytes = 4 << 20

	// permissionsKeyPrefix is the config namespace scripts may never write,
	// whatever their config_write grant: the permission model is not editable.
	permissionsKeyPrefix = "hooks.permissions"
)

const (
	// MaxLogLines and MaxLogLineLength bound the per-run log buffer so a chatty
	// script cannot grow memory without limit; both bindings expose them so a
	// script can ask how much of its output survives.
	MaxLogLines      = 200
	MaxLogLineLength = 4096
)

// DirEntry describes one directory entry returned by List.
type DirEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

// FileInfo describes one file returned by Stat.
type FileInfo struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	IsDir   bool   `json:"is_dir"`
	ModTime int64  `json:"mod_time"`
}

// StorageEntry describes one stored mod version returned by StorageList.
type StorageEntry struct {
	GameID  string `json:"game_id"`
	ModID   string `json:"mod_id"`
	Version string `json:"version"`
	Path    string `json:"path"`
}

// FileSystem is the file access a script gets. The default implementation
// talks to the operating system; tests inject their own.
type FileSystem interface {
	ReadFile(path string) ([]byte, error)
	ReadDir(path string) ([]DirEntry, error)
	Stat(path string) (FileInfo, error)
	WriteFile(path string, data []byte) error
	MkdirAll(path string) error
}

// ConfigAccess is the manager configuration a script may read or write.
type ConfigAccess interface {
	Get(key string) (string, error)
	Set(key, value string) error
	Values() (map[string]string, error)
}

// StorageAccess is the mod storage a script may inspect.
type StorageAccess interface {
	List(ctx context.Context) ([]StorageEntry, error)
	Path(ctx context.Context, gameID, modID, version string) (string, error)
}

// LogFunc receives one log line; level is info, warn, error or debug.
type LogFunc func(level, msg string)

// Providers are the injected dependencies of a Host. Nothing else reaches the
// script: whatever is nil is simply unavailable and reports as an error.
type Providers struct {
	FileSystem FileSystem
	Config     ConfigAccess
	Storage    StorageAccess
	Logger     LogFunc
	TempDir    string
}

// Options configure one Host for one script run.
type Options struct {
	Providers   Providers
	Ctx         context.Context
	BaseDir     string
	Env         []string
	Context     domain.HookContext
	Permissions domain.ScriptPermissions
}

// Host is the capability surface handed to a script. Construct it with New.
type Host struct {
	providers   Providers
	env         map[string]string
	baseDir     string
	logLines    []string
	hook        domain.HookContext
	permissions domain.ScriptPermissions
	dropped     int
	mu          sync.Mutex
}

// New is the single construction site for a Host: every runner goes through
// it, so no binding can hand a script an unconstrained view.
func New(opts Options) *Host {
	h := &Host{
		providers:   opts.Providers,
		permissions: opts.Permissions,
		hook:        opts.Context,
		env:         parseEnv(opts.Env),
		baseDir:     strings.TrimSpace(opts.BaseDir),
	}

	return h
}

// Permissions reports the effective permission set for this run.
func (h *Host) Permissions() domain.ScriptPermissions { return h.permissions }

// ContextValue returns the hook context exposed to the script.
func (h *Host) ContextValue() domain.HookContext { return h.hook }

// Log records one line in the run's log buffer and forwards it to the provider
// logger. The buffer bounds apply to every language identically.
func (h *Host) Log(level, msg string) {
	msg = truncate(msg, MaxLogLineLength)

	h.mu.Lock()
	if len(h.logLines) >= MaxLogLines {
		h.dropped++
	} else {
		h.logLines = append(h.logLines, msg)
	}

	h.mu.Unlock()

	if h.providers.Logger != nil {
		h.providers.Logger(level, msg)
	}
}

// Logs returns the buffered lines and how many were dropped to the cap.
func (h *Host) Logs() ([]string, int) {
	h.mu.Lock()
	defer h.mu.Unlock()

	out := make([]string, len(h.logLines))
	copy(out, h.logLines)

	return out, h.dropped
}

// Path returns one of the context paths by name: game, mods, mod, cache,
// temp, storage. Unknown names report false. Always available.
func (h *Host) Path(name string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "game":
		return h.hook.Game.Path, true
	case "mods":
		return h.hook.Game.ModsPath, true
	case "mod":
		return h.hook.Mod.Path, true
	case "cache":
		return h.hook.Config.CachePath, true
	case "temp":
		return h.hook.Config.TempPath, true
	case "storage":
		return h.hook.Config.StoragePath, true
	default:
		return "", false
	}
}

// ReadFile reads one file inside the allowed roots. Needs fs_read.
func (h *Host) ReadFile(path string) (string, error) {
	if !h.permissions.FSRead {
		return "", denied("fs_read")
	}

	fs, err := h.fileSystem()
	if err != nil {
		return "", err
	}

	resolved, err := h.resolve(path, false)
	if err != nil {
		return "", err
	}

	st, err := fs.Stat(resolved)
	if err != nil {
		return "", appErrors.Wrap(err, appErrors.ErrCodeHookFailed,
			"cannot stat %q", path)
	}

	if st.IsDir {
		return "", appErrors.New(appErrors.ErrCodeHookFailed,
			"%q is a directory", path)
	}

	if st.Size > maxReadBytes {
		return "", appErrors.New(appErrors.ErrCodeHookFailed,
			"%q is larger than the %d byte read limit", path, maxReadBytes)
	}

	data, err := fs.ReadFile(resolved)
	if err != nil {
		return "", appErrors.Wrap(err, appErrors.ErrCodeHookFailed,
			"cannot read %q", path)
	}

	return string(data), nil
}

// List lists one directory inside the allowed roots. Needs fs_read.
func (h *Host) List(path string) ([]DirEntry, error) {
	if !h.permissions.FSRead {
		return nil, denied("fs_read")
	}

	fs, err := h.fileSystem()
	if err != nil {
		return nil, err
	}

	resolved, err := h.resolve(path, false)
	if err != nil {
		return nil, err
	}

	entries, err := fs.ReadDir(resolved)
	if err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeHookFailed,
			"cannot list %q", path)
	}

	return entries, nil
}

// Exists reports whether a path inside the allowed roots exists. Needs fs_read.
func (h *Host) Exists(path string) (bool, error) {
	if !h.permissions.FSRead {
		return false, denied("fs_read")
	}

	fs, err := h.fileSystem()
	if err != nil {
		return false, err
	}

	resolved, err := h.resolve(path, false)
	if err != nil {
		return false, err
	}

	if _, err := fs.Stat(resolved); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}

		return false, appErrors.Wrap(err, appErrors.ErrCodeHookFailed,
			"cannot stat %q", path)
	}

	return true, nil
}

// Stat describes one path inside the allowed roots. Needs fs_read.
func (h *Host) Stat(path string) (FileInfo, error) {
	if !h.permissions.FSRead {
		return FileInfo{}, denied("fs_read")
	}

	fs, err := h.fileSystem()
	if err != nil {
		return FileInfo{}, err
	}

	resolved, err := h.resolve(path, false)
	if err != nil {
		return FileInfo{}, err
	}

	st, err := fs.Stat(resolved)
	if err != nil {
		return FileInfo{}, appErrors.Wrap(err, appErrors.ErrCodeHookFailed,
			"cannot stat %q", path)
	}

	return st, nil
}

// WriteTemp writes a file under the temporary directory only and returns its
// absolute path. Needs fs_write; the path can never escape TempDir.
func (h *Host) WriteTemp(name, content string) (string, error) {
	if !h.permissions.FSWrite {
		return "", denied("fs_write")
	}

	temp := strings.TrimSpace(h.providers.TempDir)
	if temp == "" {
		temp = strings.TrimSpace(h.hook.Config.TempPath)
	}

	if temp == "" {
		return "", appErrors.New(appErrors.ErrCodeHookFailed,
			"no temporary directory is available")
	}

	rel := strings.TrimSpace(name)
	if rel == "" {
		return "", appErrors.New(appErrors.ErrCodeHookFailed,
			"writeTemp needs a file name")
	}

	target := filepath.Join(temp, filepath.FromSlash(rel))
	target = filepath.Clean(target)

	if !domain.PathWithin(temp, target) {
		return "", appErrors.New(appErrors.ErrCodeHookFailed,
			"writeTemp target %q escapes the temporary directory", name)
	}

	fs, err := h.fileSystem()
	if err != nil {
		return "", err
	}

	if err := fs.MkdirAll(filepath.Dir(target)); err != nil {
		return "", appErrors.Wrap(err, appErrors.ErrCodeHookFailed,
			"cannot create %q", filepath.Dir(target))
	}

	if err := fs.WriteFile(target, []byte(content)); err != nil {
		return "", appErrors.Wrap(err, appErrors.ErrCodeHookFailed,
			"cannot write %q", target)
	}

	return target, nil
}

// ConfigGet reads one configuration value. Needs config_read.
func (h *Host) ConfigGet(key string) (string, error) {
	if !h.permissions.ConfigRead {
		return "", denied("config_read")
	}

	if h.providers.Config == nil {
		return "", unavailable("config")
	}

	value, err := h.providers.Config.Get(strings.TrimSpace(key))
	if err != nil {
		return "", err
	}

	return value, nil
}

// ConfigGetAll reads every visible configuration value. Needs config_read.
func (h *Host) ConfigGetAll() (map[string]string, error) {
	if !h.permissions.ConfigRead {
		return nil, denied("config_read")
	}

	if h.providers.Config == nil {
		return nil, unavailable("config")
	}

	values, err := h.providers.Config.Values()
	if err != nil {
		return nil, err
	}

	return values, nil
}

// ConfigSet writes one configuration value. Needs config_write; the
// hooks.permissions namespace is rejected even then.
func (h *Host) ConfigSet(key, value string) error {
	if !h.permissions.ConfigWrite {
		return denied("config_write")
	}

	trimmed := strings.ToLower(strings.TrimSpace(key))
	if trimmed == permissionsKeyPrefix ||
		strings.HasPrefix(trimmed, permissionsKeyPrefix+".") {
		return appErrors.New(appErrors.ErrCodeHookDenied,
			"hook scripts may not modify %s.*", permissionsKeyPrefix)
	}

	if h.providers.Config == nil {
		return unavailable("config")
	}

	return h.providers.Config.Set(strings.TrimSpace(key), value)
}

// StorageList lists every stored mod version. Needs storage_read.
func (h *Host) StorageList(ctx context.Context) ([]StorageEntry, error) {
	if !h.permissions.StorageRead {
		return nil, denied("storage_read")
	}

	if h.providers.Storage == nil {
		return nil, unavailable("storage")
	}

	entries, err := h.providers.Storage.List(ctx)
	if err != nil {
		return nil, err
	}

	return entries, nil
}

// StoragePath resolves one stored mod version's folder. Needs storage_read.
func (h *Host) StoragePath(
	ctx context.Context, gameID, modID, version string,
) (string, error) {
	if !h.permissions.StorageRead {
		return "", denied("storage_read")
	}

	if h.providers.Storage == nil {
		return "", unavailable("storage")
	}

	path, err := h.providers.Storage.Path(ctx, gameID, modID, version)
	if err != nil {
		return "", err
	}

	return path, nil
}

// EnvGet reads one variable from the executor-prepared environment. Needs
// env_read; the process environment is never consulted.
func (h *Host) EnvGet(key string) (string, error) {
	if !h.permissions.EnvRead {
		return "", denied("env_read")
	}

	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return "", appErrors.New(appErrors.ErrCodeHookFailed,
			"env.get needs a variable name")
	}

	if value, ok := h.env[trimmed]; ok {
		return value, nil
	}

	for name, value := range h.env {
		if strings.EqualFold(name, trimmed) {
			return value, nil
		}
	}

	return "", nil
}

// resolve turns a script-supplied path into an absolute path inside the allowed
// roots. allowTempOnly is unused by callers today but keeps a tighter rule for a
// write target visible in the one place that decides.
func (h *Host) resolve(path string, _ bool) (string, error) {
	raw := strings.TrimSpace(path)
	if raw == "" {
		return "", appErrors.New(appErrors.ErrCodeHookFailed,
			"path is empty")
	}

	resolved := raw
	if !filepath.IsAbs(resolved) {
		for _, base := range []string{h.baseDir, h.hook.Mod.Path, h.hook.Game.Path} {
			if strings.TrimSpace(base) == "" {
				continue
			}

			resolved = filepath.Join(base, filepath.FromSlash(raw))

			break
		}
	}

	resolved = filepath.Clean(resolved)

	roots := h.allowedRoots()
	for _, root := range roots {
		if domain.PathWithin(root, resolved) {
			return resolved, nil
		}
	}

	return "", appErrors.New(appErrors.ErrCodeHookFailed,
		"path %q is outside the allowed roots", raw)
}

// allowedRoots is the set of directories a script may touch: the mod, game and
// mods folders and the cache, temp and storage roots. Empty entries are skipped.
func (h *Host) allowedRoots() []string {
	candidates := []string{
		h.hook.Mod.Path,
		h.hook.Game.Path,
		h.hook.Game.ModsPath,
		h.hook.Config.CachePath,
		h.hook.Config.TempPath,
		h.hook.Config.StoragePath,
	}

	roots := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			roots = append(roots, trimmed)
		}
	}

	return roots
}

func (h *Host) fileSystem() (FileSystem, error) {
	if h.providers.FileSystem != nil {
		return h.providers.FileSystem, nil
	}

	return osFileSystem{}, nil
}

func denied(capability string) error {
	return appErrors.New(appErrors.ErrCodeHookDenied,
		"hook permission %q is not granted", capability)
}

func unavailable(feature string) error {
	return appErrors.New(appErrors.ErrCodeHookFailed,
		"%s access is not available in this run", feature)
}

func parseEnv(entries []string) map[string]string {
	out := make(map[string]string, len(entries))
	for _, entry := range entries {
		name, value, found := strings.Cut(entry, "=")
		if !found || name == "" {
			continue
		}

		out[name] = value
	}

	return out
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}

	return s[:limit] + logLineTruncation
}

// osFileSystem is the default FileSystem, talking to the operating system.
type osFileSystem struct{}

func (osFileSystem) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (osFileSystem) ReadDir(path string) ([]DirEntry, error) {
	items, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}

	entries := make([]DirEntry, 0, len(items))
	for _, item := range items {
		entry := DirEntry{Name: item.Name(), IsDir: item.IsDir()}
		if info, err := item.Info(); err == nil {
			entry.Size = info.Size()
		}

		entries = append(entries, entry)
	}

	return entries, nil
}

func (osFileSystem) Stat(path string) (FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return FileInfo{}, err
	}

	return FileInfo{
		Name:    info.Name(),
		Size:    info.Size(),
		IsDir:   info.IsDir(),
		ModTime: info.ModTime().Unix(),
	}, nil
}

func (osFileSystem) WriteFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}

func (osFileSystem) MkdirAll(path string) error { return os.MkdirAll(path, 0o750) }
