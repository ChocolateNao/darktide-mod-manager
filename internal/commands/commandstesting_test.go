package commands

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/internal/di"
	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/sources/shared/local"
	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/internal/storage"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/require"
)

// workspace is a private manager tree for one test: its own config file, cache,
// database, storage, game folder and folder of local mods. Every variable
// platform.ResolveDirs consults is pointed at one temp directory, because the
// wiring resolves those before it reads any option — a test that set only a
// config path would still write the real user's configuration for everything else.
type workspace struct {
	home         string
	configPath   string
	cacheRoot    string
	storageRoot  string
	databasePath string
	gameDir      string
	modsDir      string
	localDir     string
}

func newWorkspace(t *testing.T) *workspace {
	t.Helper()
	root := t.TempDir()
	w := &workspace{
		home:        root,
		configPath:  filepath.Join(root, "cfg", "config.json"),
		cacheRoot:   filepath.Join(root, "cache"),
		storageRoot: filepath.Join(root, "data", "mods"),
		gameDir:     filepath.Join(root, "games", "Darktide"),
		modsDir:     filepath.Join(root, "games", "Darktide", "mods"),
		localDir:    filepath.Join(root, "local-mods"),
	}
	w.databasePath = filepath.Join(filepath.Dir(w.storageRoot), "mods.db")

	for _, key := range []string{
		"HOME", "USERPROFILE",
		"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME",
		"APPDATA", "LOCALAPPDATA",
	} {
		t.Setenv(key, root)
	}

	for _, dir := range []string{w.cacheRoot, w.storageRoot, w.localDir} {
		{
			err := os.MkdirAll(dir, 0o755)
			require.NoError(t, err, "prepare %s: %v", dir, err)
		}
	}

	return w
}

// options builds the DI options for this workspace. The network-backed sources
// are switched off by default, since letting a search reach api.github.com would
// make the suite depend on somebody's network; the local source works offline.
func (w *workspace) options() di.Options {
	opts := di.Options{
		ConfigPath:  w.configPath,
		CachePath:   w.cacheRoot,
		StoragePath: w.storageRoot,
		SourceConfigs: map[string]map[string]any{
			local.SourceID: {"paths": []string{w.localDir}},
		},
	}
	for _, id := range []string{"github", "nexus", "vortex"} {
		opts.SourceConfigs[id] = map[string]any{"enabled": false}
	}

	return opts
}

// container builds a real container over this workspace.
func (w *workspace) container(t *testing.T, mutate ...func(*di.Options)) *di.Container {
	t.Helper()

	opts := w.options()
	for _, m := range mutate {
		m(&opts)
	}

	c, err := di.NewContainer(t.Context(), opts)
	require.NoError(t, err, "build container")

	t.Cleanup(func() { _ = c.Close() })

	return c
}

// app builds a real container and the command surface over its client.
func (w *workspace) app(t *testing.T, mutate ...func(*di.Options)) (*App, *di.Container) {
	t.Helper()
	c := w.container(t, mutate...)

	return New(Deps{Client: c.Client}), c
}

// mustApp is app for the tests that only need the surface.
func (w *workspace) mustApp(t *testing.T, mutate ...func(*di.Options)) *App {
	t.Helper()
	a, _ := w.app(t, mutate...)

	return a
}

// makeGame creates a directory the detector accepts: binary, data folder, mods.
func (w *workspace) makeGame(t *testing.T) {
	t.Helper()

	{
		err := os.MkdirAll(filepath.Join(w.gameDir, "data"), 0o755)
		require.NoError(t, err, "create game folder: %v", err)
	}

	{
		err := os.MkdirAll(w.modsDir, 0o755)
		require.NoError(t, err, "create mods folder: %v", err)
	}

	{
		err := os.WriteFile(
			filepath.Join(w.gameDir, "Darktide.exe"),
			[]byte("MZ"),
			0o644,
		)
		require.NoError(t, err, "create game binary: %v", err)
	}
}

// writeConfig stores a configuration file, which is the only moment a value
// like game.path reaches the manager.
func (w *workspace) writeConfig(t *testing.T, mutate func(cfg *domain.Config)) {
	t.Helper()

	cfg := domain.DefaultConfig()

	cfg.Sources = map[string]domain.SourceConfig{}
	if mutate != nil {
		mutate(&cfg)
	}

	store := newConfigStore(t, w.configPath)
	{
		err := store.Save(&cfg)
		require.NoError(t, err, "write config: %v", err)
	}
}

// writeModArchive writes a real zip into the local mods folder. The name carries
// a space before the version because the local source strips a trailing " 1.2.0"
// from a file name to build an id, and an underscore would leave it in.
func (w *workspace) writeModArchive(t *testing.T, name, version string) string {
	t.Helper()

	path := filepath.Join(w.localDir, fmt.Sprintf("%s %s.zip", name, version))
	writeZip(t, path, map[string]string{
		"mod.toml": fmt.Sprintf("[mod]\nid = %q\nname = %q\nversion = %q\n",
			domain.SanitizeSegment(name), name, version),
		"textures/hud.dds": "payload for " + name,
	})

	return path
}

// writeZip writes a real zip, because ModService's behaviour depends on what is
// actually in the container.
func writeZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()

	{
		err := os.MkdirAll(filepath.Dir(path), 0o755)
		require.NoError(t, err, "create archive folder: %v", err)
	}

	file, err := os.Create(path)
	require.NoError(t, err, "create archive %s: %v", path)

	defer func() { _ = file.Close() }()

	writer := zip.NewWriter(file)
	for name, body := range entries {
		entry, err := writer.Create(name)
		require.NoError(t, err, "add archive entry %s: %v", name)

		{
			_, err := entry.Write([]byte(body))
			require.NoError(t, err, "write archive entry %s: %v", name, err)
		}
	}

	{
		err := writer.Close()
		require.NoError(t, err, "finish archive %s: %v", path, err)
	}
}

// newConfigStore builds the real store, so a written config is the same
// persistence the manager reads.
func newConfigStore(t *testing.T, path string) services.ConfigStore {
	t.Helper()
	return storage.NewConfigStore(path)
}

// recordingLogger collects what the command layer logged.
type recordingLogger struct {
	lines []string
	mu    sync.Mutex
}

func (l *recordingLogger) Printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *recordingLogger) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	out := make([]string, len(l.lines))
	copy(out, l.lines)

	return out
}

func (l *recordingLogger) contains(substr string) bool {
	for _, line := range l.snapshot() {
		if strings.Contains(line, substr) {
			return true
		}
	}

	return false
}

// recordingHandler collects the events a command's work published.
type recordingHandler struct {
	events []api.Event
	mu     sync.Mutex
}

func (h *recordingHandler) handle(e api.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.events = append(h.events, e)
}

func (h *recordingHandler) has(want api.EventType) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, e := range h.events {
		if e.Type == want {
			return true
		}
	}

	return false
}

func (h *recordingHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()

	return len(h.events)
}

// blockingSource never returns until its context is done, which is the only way
// to test that cancelling the scope reaches the work rather than a field nobody
// reads.
type blockingSource struct {
	entered chan struct{}
	id      string
	once    sync.Once
}

func newBlockingSource(id string) *blockingSource {
	return &blockingSource{id: id, entered: make(chan struct{})}
}

func (s *blockingSource) ID() string          { return s.id }
func (s *blockingSource) Name() string        { return "blocking" }
func (s *blockingSource) Description() string { return "blocks until cancelled" }
func (s *blockingSource) Version() string     { return "1.0.0" }

func (s *blockingSource) SupportsSearch() bool { return true }
func (s *blockingSource) SupportsAuth() bool   { return true }
func (s *blockingSource) RequiresAPIKey() bool { return false }

func (s *blockingSource) Search(ctx context.Context, _ domain.SearchQuery) ([]domain.Mod, error) {
	s.once.Do(func() { close(s.entered) })
	<-ctx.Done()

	return nil, canceledAsOperation()
}

func (s *blockingSource) GetMod(ctx context.Context, _ string) (*domain.Mod, error) {
	s.once.Do(func() { close(s.entered) })
	<-ctx.Done()

	return nil, canceledAsOperation()
}

func (s *blockingSource) Download(context.Context, *domain.Mod, string) (string, error) {
	return "", appErrors.New(appErrors.ErrCodeSourceUnavailable, "this source downloads nothing")
}

func (s *blockingSource) Authenticate(context.Context, domain.AuthCredentials) error { return nil }

func (s *blockingSource) ValidateCredentials(context.Context) error { return nil }

// canceledAsOperation reports a cancelled context as the manager's own code, so a
// test can pin that it is not laundered into a generic failure on the way back.
func canceledAsOperation() error {
	return appErrors.New(appErrors.ErrCodeOperationCanceled, "cancelled by the test")
}

// registerBlockingSource installs a blocking source and removes it afterwards,
// so a test cannot leak a source into the next one.
func registerBlockingSource(t *testing.T, id string) *blockingSource {
	t.Helper()

	stub := newBlockingSource(id)
	{
		err := services.RegisterSourceFactory(
			id,
			func(services.SourceBuildContext) (domain.ModSource, error) {
				return stub, nil
			},
		)
		require.NoError(t, err, "register blocking source %q: %v", id, err)
	}

	t.Cleanup(func() { services.UnregisterSourceFactory(id) })

	return stub
}

var (
	_ domain.ModSource = (*blockingSource)(nil)
	_ api.Logger       = (*recordingLogger)(nil)
	_ api.EventHandler = (*recordingHandler)(nil).handle
)
