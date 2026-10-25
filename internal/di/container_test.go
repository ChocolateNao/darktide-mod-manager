package di

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/internal/platform"
	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/internal/storage"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

// newWorkspace points every platform directory at one temporary tree.
//
// pkg/api calls platform.CurrentDirs() before it looks at any option, so a test
// supplying only some paths still reads the user's real configuration file for
// the rest, and one with no paths at all would write to it.
func newWorkspace(t *testing.T) *workspace {
	t.Helper()

	home := t.TempDir()
	for _, key := range []string{
		"HOME", "USERPROFILE",
		"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME",
		"APPDATA", "LOCALAPPDATA",
	} {
		t.Setenv(key, home)
	}

	w := &workspace{home: home}
	w.configPath = filepath.Join(home, "config", "config.json")
	w.cacheRoot = filepath.Join(home, "cache")
	// The database is derived as the parent of the storage root plus mods.db, so
	// the storage folder gets a parent of its own.
	w.storageRoot = filepath.Join(home, "data", "mods")
	w.databasePath = filepath.Join(home, "data", "mods.db")
	w.gameDir = filepath.Join(home, "game")
	w.modsDir = filepath.Join(w.gameDir, "mods")
	w.localDir = filepath.Join(home, "mods-to-install")

	return w
}

func (w *workspace) options() Options {
	return Options{
		ConfigPath:  w.configPath,
		CachePath:   w.cacheRoot,
		StoragePath: w.storageRoot,
	}
}

// makeGame builds a folder the detector accepts as an installation. The mods
// folder is what an install is deployed into; without it a mod is not enabled
// automatically.
func (w *workspace) makeGame(t *testing.T) {
	t.Helper()

	err := os.MkdirAll(filepath.Join(w.gameDir, "data"), 0o755)
	require.NoError(t, err, "create game data folder: %v", err)

	err = os.MkdirAll(w.modsDir, 0o755)
	require.NoError(t, err, "create game mods folder: %v", err)

	err = os.WriteFile(filepath.Join(w.gameDir, "Darktide.exe"), []byte("MZ"), 0o644)
	require.NoError(t, err, "write Darktide.exe: %v", err)
}

func (w *workspace) saveConfig(t *testing.T, mutate func(cfg *domain.Config)) {
	t.Helper()

	cfg := domain.DefaultConfig()
	if mutate != nil {
		mutate(&cfg)
	}

	w.writeConfig(t, &cfg)
}

func (w *workspace) writeConfig(t *testing.T, cfg *domain.Config) {
	t.Helper()

	err := os.MkdirAll(filepath.Dir(w.configPath), 0o755)
	require.NoError(t, err, "create config folder: %v", err)

	store := storage.NewConfigStore(w.configPath)
	err = store.Save(cfg)
	require.NoError(t, err, "save config: %v", err)
}

func (w *workspace) writeModArchive(t *testing.T, name, version string) string {
	t.Helper()

	err := os.MkdirAll(w.localDir, 0o755)
	require.NoError(t, err, "create local source folder: %v", err)

	path := filepath.Join(w.localDir, name+" "+version+".zip")

	file, err := os.Create(path)
	require.NoError(t, err, "create archive %s: %v", path)

	defer func() {
		err := file.Close()
		require.NoError(t, err, "close archive %s: %v", path, err)
	}()

	zw := zip.NewWriter(file)

	manifest := "[mod]\n" +
		"id = \"" + name + "\"\n" +
		"version = \"" + version + "\"\n" +
		"name = \"Better UI\"\n"
	err = writeZipEntry(zw, "mod.toml", manifest)
	require.NoError(t, err, "write manifest: %v", err)

	err = writeZipEntry(zw, "textures/hud.dds", "payload")
	require.NoError(t, err, "write payload: %v", err)

	err = zw.Close()
	require.NoError(t, err, "close archive %s: %v", path, err)

	return path
}

func writeZipEntry(zw *zip.Writer, name, body string) error {
	entry, err := zw.Create(name)
	if err != nil {
		return err
	}

	if _, err := entry.Write([]byte(body)); err != nil {
		return err
	}

	return nil
}

func mustContainer(t *testing.T, ctx context.Context, opts Options) *Container {
	t.Helper()

	container, err := NewContainer(ctx, opts)
	require.NoError(t, err, "NewContainer")

	t.Cleanup(func() { _ = container.Close() })

	return container
}

// recordingHandler collects events. Events may arrive from any publishing
// goroutine, so the mutex is required.
type recordingHandler struct {
	events []api.Event
	mu     sync.Mutex
}

func (h *recordingHandler) handle(event api.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.events = append(h.events, event)
}

func (h *recordingHandler) types() []string {
	h.mu.Lock()
	defer h.mu.Unlock()

	out := make([]string, 0, len(h.events))
	for _, event := range h.events {
		out = append(out, string(event.Type))
	}

	return out
}

func (h *recordingHandler) has(want api.EventType) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, event := range h.events {
		if event.Type == want {
			return true
		}
	}

	return false
}

type recordingLogger struct {
	messages []string
	mu       sync.Mutex
}

func (l *recordingLogger) Printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.messages = append(l.messages, fmt.Sprintf(format, args...))
}

func (l *recordingLogger) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return strings.Join(l.messages, "\n")
}

var _ api.Logger = (*recordingLogger)(nil)

func TestNewContainerWiresEveryService(t *testing.T) {
	w := newWorkspace(t)
	container := mustContainer(t, context.Background(), w.options())

	assert.NotNil(t, container.ModService, "ModService is nil")

	assert.NotNil(t, container.SourceRegistry, "SourceRegistry is nil")

	assert.NotNil(t, container.GameLocator, "GameLocator is nil")

	assert.NotNil(t, container.CacheManager, "CacheManager is nil")

	assert.NotNil(t, container.HookExecutor, "HookExecutor is nil")

	assert.NotNil(t, container.Config, "Config is nil")

	assert.NotNil(t, container.Store, "Store is nil")

	assert.NotNil(t, container.Client, "Client is nil")

	assert.NotNil(t, container.EventBus, "EventBus is nil")

	assert.NotEmpty(
		t,
		container.CleanupFuncs,
		"CleanupFuncs is empty; nothing would release the database",
	)

	// The client must wrap the very same services; two graphs diverge invisibly.
	deps := container.Client.Services()
	assert.Equal(
		t,
		container.ModService,
		deps.Mods,
		"the client's mod service is not the container's",
	)

	assert.Equal(
		t,
		container.SourceRegistry,
		deps.Sources,
		"the client's source registry is not the container's",
	)

	assert.Equal(
		t,
		container.GameLocator,
		deps.Game,
		"the client's game locator is not the container's",
	)

	assert.Equal(
		t,
		container.CacheManager,
		deps.Cache,
		"the client's cache manager is not the container's",
	)

	assert.Equal(
		t,
		container.HookExecutor,
		deps.Hooks,
		"the client's hook executor is not the container's",
	)

	assert.Equal(
		t,
		container.Store,
		deps.Config,
		"the client's config store is not the container's",
	)

	assert.Equal(
		t,
		container.EventBus,
		deps.Events,
		"the client's event bus is not the container's",
	)

	// One bus for both paths, so a handler registered through either sees the
	// same events.
	assert.Equal(
		t,
		container.EventBus,
		container.Client.Events(),
		"the client publishes through a different event bus",
	)
}

func TestNewContainerUsesTheSuppliedPaths(t *testing.T) {
	w := newWorkspace(t)
	container := mustContainer(t, context.Background(), w.options())

	assert.Equal(
		t,
		w.cacheRoot,
		container.Config.Cache.Path,
		"config cache path = %q, want %q",
		container.Config.Cache.Path,
		w.cacheRoot,
	)

	assert.Equal(
		t,
		w.storageRoot,
		container.Config.Storage.Path,
		"config storage path = %q, want %q",
		container.Config.Storage.Path,
		w.storageRoot,
	)

	assert.Equal(
		t,
		w.cacheRoot,
		container.CacheRoot,
		"CacheRoot = %q, want %q",
		container.CacheRoot,
		w.cacheRoot,
	)

	assert.Equal(
		t,
		w.storageRoot,
		container.StorageRoot,
		"StorageRoot = %q, want %q",
		container.StorageRoot,
		w.storageRoot,
	)

	assert.Equal(
		t,
		w.databasePath,
		container.DatabasePath,
		"DatabasePath = %q, want %q",
		container.DatabasePath,
		w.databasePath,
	)

	// The database sits beside the payload tree: the local source scans folders
	// and would offer a database file to the user as a mod.
	assert.True(
		t,
		strings.HasPrefix(container.DatabasePath, filepath.Dir(container.StorageRoot)),
		"database %q is not beside the storage root %q",
		container.DatabasePath,
		container.StorageRoot,
	)

	assert.False(
		t,
		strings.HasPrefix(container.DatabasePath, container.StorageRoot+string(filepath.Separator)),
		"database %q sits inside the payload tree %q",
		container.DatabasePath,
		container.StorageRoot,
	)

	_, err := os.Stat(w.databasePath)
	assert.NoError(t, err, "the database was not created: %v", err)

	_, err = os.Stat(w.cacheRoot)
	assert.NoError(t, err, "the cache folder was not created: %v", err)
}

func TestNewContainerFallsBackToThePlatformDirectories(t *testing.T) {
	w := newWorkspace(t)

	dirs, err := platform.CurrentDirs()
	require.NoError(t, err, "platform.CurrentDirs")

	container := mustContainer(t, context.Background(), Options{})

	// The file keeps the user's empty paths; resolved locations live on the
	// container, where CacheRoot is where the archives really are.
	assert.Equal(
		t,
		"",
		container.Config.Cache.Path,
		"the empty configuration cache path was rewritten to %q",
		container.Config.Cache.Path,
	)

	assert.Equal(
		t,
		dirs.Cache,
		container.CacheRoot,
		"CacheRoot = %q, want %q",
		container.CacheRoot,
		dirs.Cache,
	)

	assert.Equal(
		t,
		dirs.StorageRoot(),
		container.StorageRoot,
		"StorageRoot = %q, want %q",
		container.StorageRoot,
		dirs.StorageRoot(),
	)

	if got, err := container.Client.ConfigPath(); err != nil {
		assert.Fail(t, "ConfigPath: %v", err)
	} else if want := dirs.ConfigFilePath(); got != want {
		assert.Fail(t, "ConfigPath = %q, want %q", got, want)
	}

	_ = w
}

func TestNewContainerIgnoresBlankPaths(t *testing.T) {
	newWorkspace(t)

	dirs, err := platform.CurrentDirs()
	require.NoError(t, err, "platform.CurrentDirs")

	// An all-spaces path is no opinion; passing one would replace the user's
	// configuration with a default.
	container := mustContainer(t, context.Background(), Options{
		ConfigPath:  "   ",
		CachePath:   "\t",
		StoragePath: " \n ",
	})

	assert.Equal(
		t,
		dirs.Cache,
		container.CacheRoot,
		"CacheRoot = %q, want the platform cache %q",
		container.CacheRoot,
		dirs.Cache,
	)

	assert.Equal(
		t,
		dirs.StorageRoot(),
		container.StorageRoot,
		"StorageRoot = %q, want %q",
		container.StorageRoot,
		dirs.StorageRoot(),
	)
}

func TestNewContainerPersistsAValidGamePath(t *testing.T) {
	w := newWorkspace(t)
	w.makeGame(t)
	opts := w.options()
	opts.GamePath = w.gameDir

	container := mustContainer(t, context.Background(), opts)

	assert.Equal(
		t,
		w.gameDir,
		container.Config.ActiveSettings().Path,
		"in-memory game path = %q, want %q",
		container.Config.ActiveSettings().Path,
		w.gameDir,
	)
	// On disk too, so the next launch starts from the same answer.
	reloaded := storage.NewConfigStore(w.configPath)

	cfg, err := reloaded.Load()
	require.NoError(t, err, "reload config")

	assert.Equal(
		t,
		w.gameDir,
		cfg.ActiveSettings().Path,
		"stored game path = %q, want %q",
		cfg.ActiveSettings().Path,
		w.gameDir,
	)
}

func TestNewContainerRejectsAnInvalidGamePath(t *testing.T) {
	w := newWorkspace(t)

	empty := filepath.Join(w.home, "not-a-game")
	err := os.MkdirAll(empty, 0o755)
	require.NoError(t, err, "create decoy folder: %v", err)

	opts := w.options()
	opts.GamePath = empty

	container, err := NewContainer(context.Background(), opts)
	if err == nil {
		_ = container.Close()

		require.FailNow(t, "a folder that is not a game installation was accepted")
	}

	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeGameInvalid),
		"error = %v, want ErrCodeGameInvalid",
		err,
	)

	assert.Nil(t, container, "a container was returned alongside the error")

	// The cache and database are opened before the game path is validated; a failure
	// here must release them or the next launch finds a stale lock.
	_, err = os.Stat(w.databasePath)
	assert.Error(t, err, "%s was created by a failed start-up", w.databasePath)

	_, err = os.Stat(w.cacheRoot)
	assert.Error(t, err, "the cache folder %s was created by a failed start-up", w.cacheRoot)

	_, err = os.Stat(w.configPath)
	assert.Error(t, err, "the configuration file %s was created by a failed start-up", w.configPath)
}

func TestNewContainerRejectsABlankSourceID(t *testing.T) {
	w := newWorkspace(t)
	opts := w.options()
	opts.SourceConfigs = map[string]map[string]any{"   ": nil}

	container, err := NewContainer(context.Background(), opts)
	if err == nil {
		_ = container.Close()

		require.FailNow(t, "a blank source id was accepted")
	}

	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
		"error = %v, want ErrCodeInvalidArgument",
		err,
	)

	assert.Contains(
		t,
		err.Error(),
		"source id",
		"error %q does not say what was wrong with it",
		err,
	)
}

func TestNewContainerConfiguresTheSourcesItIsGiven(t *testing.T) {
	w := newWorkspace(t)
	opts := w.options()
	opts.SourceConfigs = map[string]map[string]any{
		"local": {"paths": []any{w.localDir}},
	}

	container := mustContainer(t, context.Background(), opts)

	// Read from the container's config: the client's GetConfig reads the file, and
	// source options are kept in memory.
	block, ok := container.Config.SourceConfigFor("local")
	require.True(t, ok, "the local source has no configuration block")

	assert.False(
		t,
		len(block.Paths) != 1 || block.Paths[0] != w.localDir,
		"local paths = %v, want [%s]",
		block.Paths,
		w.localDir,
	)

	assert.True(t, block.Enabled, "a source block that never mentioned enabled came out disabled")
}

func TestNewContainerLogsSourcesItCouldNotConfigure(t *testing.T) {
	w := newWorkspace(t)

	// One unhappy optional plugin must not stop launch, or the settings screen
	// becomes unreachable.
	const brokenID = "di-test-broken"
	if err := services.RegisterSourceFactory(
		brokenID,
		func(services.SourceBuildContext) (domain.ModSource, error) {
			return nil, appErrors.New(appErrors.ErrCodeAuthFailed, "credentials are half-filled")
		},
	); err != nil {
		require.FailNow(t, "register the broken source: %v", err)
	}

	t.Cleanup(func() { services.UnregisterSourceFactory(brokenID) })

	opts := w.options()
	opts.SourceConfigs = map[string]map[string]any{brokenID: nil}

	container := mustContainer(t, context.Background(), opts)

	// Read the log file: a source that cannot be configured is a warning, and
	// warnings belong where a user with the same broken credentials next month
	// will still find them.
	reported := readLogFile(t, container.Logging.Directory())
	require.NotEqual(t, "", reported, "a source that failed to configure was not reported anywhere")

	assert.Contains(t, reported, brokenID, "the report does not name the source: %q", reported)

	assert.Contains(
		t,
		reported,
		"credentials are half-filled",
		"the report does not carry the reason the source gave: %q",
		reported,
	)
}

// readLogFile returns the contents of dir's dmm.log. A missing log reads as
// empty, since the assertions are about what is missing.
func readLogFile(t *testing.T, dir string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(dir, "dmm.log"))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}

		require.FailNow(t, "read the log file: %v", err)
	}

	return string(data)
}

func TestContainerDeliversEventsToTheSuppliedHandler(t *testing.T) {
	w := newWorkspace(t)
	handler := &recordingHandler{}
	opts := w.options()
	opts.EventHandler = handler.handle

	container := mustContainer(t, context.Background(), opts)

	// Clearing an empty cache is the cheapest operation that still publishes, so if
	// the handler were wired to a different bus this is where it would show.
	err := container.Client.ClearCache(context.Background())
	require.NoError(t, err, "ClearCache: %v", err)

	assert.True(
		t,
		handler.has(api.EventCacheCleared),
		"the handler saw %v, want %s",
		handler.types(),
		api.EventCacheCleared,
	)
}

func TestContainerInstallsThroughItsOwnPaths(t *testing.T) {
	w := newWorkspace(t)
	w.makeGame(t)
	// The local source derives identity from the file name and strips a trailing
	// " 1.2.0", so an underscore-separated name would keep the version in the id.
	w.writeModArchive(t, "better_ui", "1.2.0")

	w.saveConfig(t, func(cfg *domain.Config) {
		cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.Path = w.gameDir })
	})

	opts := w.options()
	opts.SourceConfigs = map[string]map[string]any{"local": {"paths": []any{w.localDir}}}
	container := mustContainer(t, context.Background(), opts)

	mod, err := container.Client.InstallMod(context.Background(), "local", "better_ui")
	require.NoError(t, err, "InstallMod")

	assert.Equal(t, "local", mod.SourceID, "SourceID = %q, want local", mod.SourceID)

	assert.True(
		t,
		mod.Enabled,
		"the mod was not enabled even though a game mods folder was configured",
	)
	// The payload must land under the container's storage root; that proves the
	// option reached the mod service.
	want := domain.ModStoragePath(container.StorageRoot, mod.GameID, mod.ID, mod.Version)
	assert.Equal(t, want, mod.LocalPath, "LocalPath = %q, want %q", mod.LocalPath, want)

	_, err = os.Stat(filepath.Join(mod.LocalPath, "textures", "hud.dds"))
	assert.NoError(t, err, "the payload was not extracted: %v", err)
	// And the manifest must not be deployed: the game has no use for it, and a
	// mod folder is the one place a second manager would read it from.
	_, err = os.Stat(filepath.Join(w.modsDir, "mod.toml"))
	assert.Error(t, err, "mod.toml was deployed into the game folder")

	_, err = os.Stat(filepath.Join(w.modsDir, "textures"))
	assert.NoError(t, err, "the mod was not deployed into the game folder: %v", err)

	listed, err := container.Client.ListMods(context.Background())
	require.NoError(t, err, "ListMods")

	assert.False(
		t,
		len(listed) != 1 || listed[0].ID != mod.ID,
		"ListMods = %v, want the one installed mod %q",
		listed,
		mod.ID,
	)
}

func TestContainerCloseReleasesEverything(t *testing.T) {
	w := newWorkspace(t)

	container, err := NewContainer(context.Background(), w.options())
	require.NoError(t, err, "NewContainer")

	assert.False(t, container.Closed(), "a freshly built container reports itself closed")

	err = container.Close()
	require.NoError(t, err, "Close: %v", err)

	assert.True(t, container.Closed(), "Closed is false after Close")

	assert.Nil(
		t,
		container.CleanupFuncs,
		"CleanupFuncs still holds %d entries after Close",
		len(container.CleanupFuncs),
	)

	assert.True(t, container.Client.Closed(), "the client was not closed")
	// The client must refuse to work after the database is released.
	_, err = container.Client.ListMods(context.Background())
	assert.Error(t, err, "a closed client still served a request")

	// A second manager over the same tree must be able to open the database,
	// which it cannot do if the first one's handle is still live.
	second, err := NewContainer(context.Background(), w.options())
	require.NoError(t, err, "the database was not released by Close")

	err = second.Close()
	assert.NoError(t, err, "closing the second container: %v", err)
}

func TestContainerCloseIsIdempotent(t *testing.T) {
	w := newWorkspace(t)

	container, err := NewContainer(context.Background(), w.options())
	require.NoError(t, err, "NewContainer")

	for attempt := 1; attempt <= 3; attempt++ {
		err := container.Close()
		require.NoError(t, err, "Close attempt %d: %v", attempt, err)
	}
}

func TestContainerCloseOnNothingIsNotAFailure(t *testing.T) {
	// A caller that defers the close and ignores the construction error holds a nil
	// container, so an error here would only force a nil check.
	var container *Container

	err := container.Close()
	assert.NoError(t, err, "closing a nil container: %v", err)

	assert.True(t, container.Closed(), "a nil container does not report itself closed")
}

func TestDepsRefusesAClosedContainer(t *testing.T) {
	w := newWorkspace(t)

	container, err := NewContainer(context.Background(), w.options())
	require.NoError(t, err, "NewContainer")

	deps, err := container.Deps()
	require.NoError(t, err, "Deps")

	assert.Equal(t, container.ModService, deps.Mods, "Deps returned a different mod service")

	assert.NotNil(
		t,
		deps.Clock,
		"Deps dropped the clock; a second client would silently use the wall clock",
	)

	err = container.Close()
	require.NoError(t, err, "Close: %v", err)

	deps, err = container.Deps()
	if err == nil {
		assert.Fail(t, "a closed container handed out its services")
	} else {
		assert.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
			"error = %v, want ErrCodeInvalidArgument",
			err,
		)

		assert.Contains(
			t,
			err.Error(),
			"closed",
			"error %q does not say the container is closed",
			err,
		)
	}

	assert.Nil(t, deps.Mods, "a refused Deps still returned services")
}

func TestDepsOnANilContainerIsRefused(t *testing.T) {
	var container *Container

	_, err := container.Deps()
	assert.Error(t, err, "a nil container handed out its services")
}

func TestOptionsMapOntoAPIOptions(t *testing.T) {
	opts := Options{
		ConfigPath:  "/tmp/config.json",
		GamePath:    "/games/darktide",
		CachePath:   "/tmp/cache",
		StoragePath: "/tmp/mods",
		SourceConfigs: map[string]map[string]any{
			"github": nil,
			"nexus":  nil,
		},
	}

	got, err := opts.apiOptions()
	require.NoError(t, err, "apiOptions")

	assert.Len(
		t,
		got,
		7,
		"apiOptions returned %d options, want 7 (4 paths, 2 sources and logging)",
		len(got),
	)

	// Blank paths are no opinion at all, so they contribute nothing. The logging
	// options are the exception: they are the defaults,
	// and an unset option would leave pkg/api to guess what the config file says.
	blank := Options{ConfigPath: "  ", CachePath: "\t"}

	got, err = blank.apiOptions()
	require.NoError(t, err, "apiOptions")

	assert.Len(
		t,
		got,
		1,
		"blank paths produced %d options, want only the logging defaults",
		len(got),
	)
}

func TestOptionsRejectABlankSourceID(t *testing.T) {
	opts := Options{SourceConfigs: map[string]map[string]any{"nexus": nil, " \t ": nil}}
	if _, err := opts.apiOptions(); err == nil {
		require.FailNow(t, "a blank source id was accepted")
	} else if !appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument) {
		assert.Fail(t, "error = %v, want ErrCodeInvalidArgument", err)
	}
}

func TestOptionsMapSourcesInASortedOrder(t *testing.T) {
	// api.Option is opaque, so order is pinned by effect: eight runs over the same map
	// must build the same options.
	opts := Options{SourceConfigs: map[string]map[string]any{
		"vortex": nil, "local": nil, "nexus": nil, "github": nil,
	}}

	var want []api.Option

	for attempt := 0; attempt < 8; attempt++ {
		got, err := opts.apiOptions()
		require.NoError(t, err, "apiOptions")

		if want == nil {
			want = got
			continue
		}

		require.Len(
			t,
			got,
			len(want),
			"attempt %d produced %d options, first attempt produced %d",
			attempt,
			len(got),
			len(want),
		)
	}
	// The ids themselves are in sorted order in the source map we build from.
	ids := make([]string, 0, len(opts.SourceConfigs))
	for id := range opts.SourceConfigs {
		ids = append(ids, id)
	}

	sort.Strings(ids)

	assert.Equal(t, "github,local,nexus,vortex", strings.Join(ids, ","), "sorted ids = %v", ids)
}

func TestClientOptionsSkipWhatWasNotSupplied(t *testing.T) {
	if got := (Options{}).clientOptions(); len(got) != 0 {
		assert.Fail(t, "an empty Options produced %d client options, want none", len(got))
	}

	got := (Options{EventHandler: (&recordingHandler{}).handle, Logger: &recordingLogger{}}).clientOptions()
	assert.Len(t, got, 2, "clientOptions returned %d options, want 2", len(got))
}

func TestNewContainerToleratesANilContext(t *testing.T) {
	w := newWorkspace(t)
	//nolint:staticcheck // SA1012: passing a nil context is exactly what is under test.
	container, err := NewContainer(nil, w.options())
	require.NoError(t, err, "NewContainer with a nil context")

	require.NotNil(t, container.Client, "no client was built")

	err = container.Close()
	assert.NoError(t, err, "Close: %v", err)
}

func TestNewContainerRejectsAnUnreadableConfiguration(t *testing.T) {
	w := newWorkspace(t)
	err := os.MkdirAll(filepath.Dir(w.configPath), 0o755)
	require.NoError(t, err, "create config folder: %v", err)

	if err := os.WriteFile(w.configPath, []byte("{not json"), 0o644); err != nil {
		require.FailNow(t, "write broken config: %v", err)
	}

	container, err := NewContainer(context.Background(), w.options())
	if err == nil {
		_ = container.Close()

		require.FailNow(t, "an unparseable configuration was accepted")
	}

	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeConfigInvalid),
		"error = %v, want ErrCodeConfigInvalid",
		err,
	)
}

func TestNewContainerRegistersTheBundledSources(t *testing.T) {
	w := newWorkspace(t)
	container := mustContainer(t, context.Background(), w.options())

	// Membership, because the bundled sources come from package init functions
	// and a count breaks when a fourth is added.
	registered := make([]string, 0, 3)

	for _, want := range []string{"github", "local", "nexus"} {
		if _, err := container.SourceRegistry.Get(want); err == nil {
			registered = append(registered, want)
		}
	}

	assert.Equal(
		t,
		"github,local,nexus",
		strings.Join(registered, ","),
		"registered sources = %v",
		registered,
	)
}

func TestNormalizePath(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{in: "", wantOK: false},
		{in: "   ", wantOK: false},
		{in: "\t\n", wantOK: false},
		{in: " /games/darktide ", want: "/games/darktide", wantOK: true},
		{in: "/games/darktide", want: "/games/darktide", wantOK: true},
		{in: ".", want: ".", wantOK: true},
	}
	for _, tc := range cases {
		got, ok := normalizePath(tc.in)
		assert.False(
			t,
			ok != tc.wantOK || got != tc.want,
			"normalizePath(%q) = (%q, %t), want (%q, %t)",
			tc.in,
			got,
			ok,
			tc.want,
			tc.wantOK,
		)
	}
}

func TestContainerFieldsSurviveAReloadOfTheConfiguration(t *testing.T) {
	w := newWorkspace(t)
	w.saveConfig(t, func(cfg *domain.Config) {
		cfg.Cache.MaxSizeGB = 11
		cfg.Sources["nexus"] = domain.SourceConfig{APIKey: "from-file"}
	})
	container := mustContainer(t, context.Background(), w.options())

	assert.Equal(
		t,
		11,
		container.Config.Cache.MaxSizeGB,
		"cache budget = %d, want the 11 from the file",
		container.Config.Cache.MaxSizeGB,
	)

	block, ok := container.Config.SourceConfigFor("nexus")
	assert.True(
		t,
		ok || block.APIKey != "from-file",
		"the nexus block from the file was lost: %+v (found = %t)",
		block,
		ok,
	)
	// The cache manager was built from that same configuration.
	assert.Equal(
		t,
		11,
		container.CacheManager.Config().MaxSizeGB,
		"the cache manager's budget = %d, want 11",
		container.CacheManager.Config().MaxSizeGB,
	)
}

func TestContainerIsUsableFromMoreThanOnePlatform(t *testing.T) {
	// The container holds no platform-specific state, so only the deployer it
	// inherited is worth checking.
	w := newWorkspace(t)
	container := mustContainer(t, context.Background(), w.options())

	if _, err := container.Client.GetModsPath(context.Background(), nil); err != nil {
		// The error is the point: the call reached the real locator and did not
		// panic on a half-built graph.
		assert.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeGameNotFound),
			"error = %v, want ErrCodeGameNotFound",
			err,
		)
	}

	if runtime.GOOS == "windows" {
		// The deployer's mode is the only platform-dependent thing here, and it
		// is decided by platform.SupportsSymlinks.
		t.Log("running on Windows; symlink deployment may fall back to copying")
	}
}
