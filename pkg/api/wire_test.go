package api

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/internal/platform"
	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/internal/storage"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests exercise wire.go, the only code in the module that opens files and
// connects services: a mistake here points the manager at the wrong directory.

// wireWorkspace is a private tree for one test: its own configuration file, cache,
// storage root, game installation and local-mod folder.
type wireWorkspace struct {
	configPath   string
	cacheRoot    string
	storageRoot  string
	databasePath string
	gameDir      string
	modsDir      string
	localDir     string
}

// options are the path overrides that keep a test off the developer's real files.
func (w wireWorkspace) options() []Option {
	return []Option{
		WithConfigPath(w.configPath),
		WithCachePath(w.cacheRoot),
		WithStoragePath(w.storageRoot),
	}
}

// newWireWorkspace points the platform's per-user directories at a temporary
// folder and returns the private tree the tests then work inside.
//
// The platform directories are redirected as well as overridden, because wire()
// resolves them before it reads any option: without this, a test with no path
// options at all would read and write the real user configuration.
func newWireWorkspace(t *testing.T) wireWorkspace {
	t.Helper()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	configHome := filepath.Join(base, "xdg-config")
	dataHome := filepath.Join(base, "xdg-data")

	cacheHome := filepath.Join(base, "xdg-cache")
	for _, dir := range []string{home, configHome, dataHome, cacheHome} {
		require.NoError(t, os.MkdirAll(dir, 0o755), "create %s", dir)
	}
	// Every variable the three supported platforms consult, so the workspace is
	// private whichever one the test happens to run on.
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_CACHE_HOME", cacheHome)
	t.Setenv("APPDATA", configHome)
	t.Setenv("LOCALAPPDATA", dataHome)

	w := wireWorkspace{
		configPath: filepath.Join(base, "config", "config.json"),
		cacheRoot:  filepath.Join(base, "cache"),
		// The storage root is "<data>/mods" in the real layout, so the tests keep
		// that shape: the database path is derived from its parent.
		storageRoot: filepath.Join(dataHome, "mods"),
		localDir:    filepath.Join(base, "local-mods"),
		gameDir:     filepath.Join(base, "Darktide"),
		modsDir:     filepath.Join(base, "Darktide", "mods"),
	}

	w.databasePath = filepath.Join(filepath.Dir(w.storageRoot), "mods.db")
	require.NoError(t, os.MkdirAll(w.localDir, 0o755), "create %s", w.localDir)

	return w
}

// makeGame creates an installation the detector accepts.
func (w wireWorkspace) makeGame(t *testing.T) {
	t.Helper()

	for _, dir := range []string{filepath.Join(w.gameDir, "data"), w.modsDir} {
		require.NoError(t, os.MkdirAll(dir, 0o755), "create %s", dir)
	}
	// The detector accepts a data directory, but a binary is what a real install
	// has and it keeps the fixture honest about what it is pretending to be.
	binary := filepath.Join(w.gameDir, domain.BinaryName)
	require.NoError(t, os.WriteFile(binary, []byte("MZ"), 0o644), "write %s", binary)
}

// saveConfig writes the configuration file the wiring will read.
func (w wireWorkspace) saveConfig(t *testing.T, mutate func(*domain.Config)) {
	t.Helper()

	cfg := domain.DefaultConfig()
	cfg.Storage.Path = w.storageRoot

	cfg.Cache.Path = w.cacheRoot
	if mutate != nil {
		mutate(&cfg)
	}

	require.NoError(t, storage.NewConfigStore(w.configPath).Save(&cfg), "save config")
}

// writeModArchive writes a real zip the local source can offer and the mod
// service can extract.
func (w wireWorkspace) writeModArchive(t *testing.T, name string) string {
	t.Helper()

	path := filepath.Join(w.localDir, name)
	body := []byte("hud pixels")

	file, err := os.Create(path)
	require.NoError(t, err, "create %s", path)

	archive := zip.NewWriter(file)

	entry, err := archive.Create("textures/hud.dds")
	require.NoError(t, err, "add zip entry")

	_, err = entry.Write(body)
	require.NoError(t, err, "write zip entry")

	require.NoError(t, archive.Close(), "close zip")

	require.NoError(t, file.Close(), "close %s", path)

	return path
}

func mustBundle(t *testing.T, opts ...Option) *Bundle {
	t.Helper()

	bundle, err := NewBundle(context.Background(), opts...)
	require.NoError(t, err, "wire")

	t.Cleanup(func() { _ = bundle.Close() })

	return bundle
}

func TestBundleWiresEveryService(t *testing.T) {
	w := newWireWorkspace(t)
	w.saveConfig(t, nil)

	bundle := mustBundle(t, w.options()...)

	deps := bundle.Deps
	assert.NotNil(t, deps.Mods, "no mod service was wired")

	assert.NotNil(t, deps.Sources, "no source registry was wired")

	assert.NotNil(t, deps.Game, "no game locator was wired")

	assert.NotNil(t, deps.Cache, "no cache manager was wired")

	assert.NotNil(t, deps.Hooks, "no hook executor was wired")

	assert.NotNil(t, deps.Config, "no configuration store was wired")

	assert.NotNil(t, deps.Events, "no event bus was wired")

	assert.NotNil(t, deps.Clock, "no clock was wired")

	assert.NotNil(t, bundle.Config, "the bundle carries no configuration")

	assert.NotNil(t, bundle.Store, "the bundle carries no configuration store")

	assert.Equal(t, w.cacheRoot, bundle.CacheRoot, "cache root")

	assert.Equal(t, w.storageRoot, bundle.StorageRoot, "storage root")

	assert.Equal(t, w.databasePath, bundle.DatabasePath, "database path")

	assert.NotEmpty(
		t,
		bundle.Cleanups,
		"the bundle registers no cleanups, so closing it would leak the database",
	)
}

func TestBundleKeepsTheDatabaseOutOfThePayloadTree(t *testing.T) {
	w := newWireWorkspace(t)
	w.saveConfig(t, nil)

	bundle := mustBundle(t, w.options()...)

	// The local source scans folders, so a database inside <storage> would be offered
	// to the user as a mod.
	assert.False(t, domain.PathWithin(w.storageRoot, bundle.DatabasePath),
		"database path %q is inside the storage root %q",
		bundle.DatabasePath,
		w.storageRoot,
	)

	assert.Equal(t, "mods.db", filepath.Base(bundle.DatabasePath), "database path")
}

func TestBundleOpensTheDatabaseAndTheCacheOnDisk(t *testing.T) {
	w := newWireWorkspace(t)
	w.saveConfig(t, nil)

	mustBundle(t, w.options()...)

	_, err := os.Stat(w.databasePath)
	assert.NoError(t, err, "database was not created")

	_, err = os.Stat(w.cacheRoot)
	assert.NoError(t, err, "cache root was not created")
}

func TestBundleUsesThePathsFromTheConfigurationFile(t *testing.T) {
	w := newWireWorkspace(t)
	w.saveConfig(t, nil)

	// Only the configuration path is overridden; the cache and storage roots must
	// come from the file, which is the only place a user's own paths live.
	bundle := mustBundle(t, WithConfigPath(w.configPath))

	assert.Equal(t, w.cacheRoot, bundle.CacheRoot,
		"cache root from the configuration file")

	assert.Equal(t, w.storageRoot, bundle.StorageRoot,
		"storage root from the configuration file")
}

func TestBundleFallsBackToThePlatformDirectories(t *testing.T) {
	w := newWireWorkspace(t)

	// No configuration file, so every path has to come from the platform.
	bundle := mustBundle(t)

	dirs, err := platform.CurrentDirs()
	require.NoError(t, err, "resolve directories")

	assert.NotNil(t, bundle.Store)
	assert.Equal(t, dirs.ConfigFilePath(), bundle.Store.Path(), "configuration path")

	assert.Equal(t, dirs.Cache, bundle.CacheRoot, "cache root")

	assert.Equal(t, dirs.StorageRoot(), bundle.StorageRoot, "storage root")

	_ = w
}

func TestBundlePathOptionsBeatTheConfigurationFile(t *testing.T) {
	w := newWireWorkspace(t)
	w.saveConfig(t, nil)

	otherCache := filepath.Join(filepath.Dir(w.cacheRoot), "other-cache")
	otherStorage := filepath.Join(filepath.Dir(w.storageRoot), "other-mods")
	opts := append(w.options(), WithCachePath(otherCache), WithStoragePath(otherStorage))
	bundle := mustBundle(t, opts...)

	assert.Equal(t, otherCache, bundle.CacheRoot, "cache root")

	assert.Equal(t, otherStorage, bundle.StorageRoot, "storage root")

	assert.Equal(t, otherCache, bundle.Config.Cache.Path, "in-memory configuration cache path")
}

func TestBundleReportsAnUnreadableConfigurationFile(t *testing.T) {
	w := newWireWorkspace(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(w.configPath), 0o755), "create config dir")

	require.NoError(t, os.WriteFile(w.configPath, []byte("{not json"), 0o600), "write config")

	bundle, err := NewBundle(context.Background(), WithConfigPath(w.configPath))
	require.Error(t, err, "a configuration file that cannot be parsed was accepted")

	if bundle != nil {
		_ = bundle.Close()
	}

	assert.True(t, appErrors.IsAppError(err, appErrors.ErrCodeConfigInvalid),
		"error code = %v, want CONFIG_INVALID for an unparseable file",
		appErrors.CodeOf(err),
	)
}

func TestBundleReportsAnUnusableCachePath(t *testing.T) {
	w := newWireWorkspace(t)
	w.saveConfig(t, nil)

	// A regular file where the cache root should be: the cache cannot be opened,
	// and the bundle must fail rather than hand back a manager with no cache.
	blocker := filepath.Join(filepath.Dir(w.cacheRoot), "not-a-directory")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644), "write %s", blocker)

	bundle, err := NewBundle(context.Background(), append(w.options(), WithCachePath(blocker))...)
	require.Error(t, err, "a cache path pointing at a file was accepted")

	if bundle != nil {
		_ = bundle.Close()
	}

	assert.NotEqual(
		t,
		appErrors.CodeUnknown,
		appErrors.CodeOf(err),
		"error %v carries no code, so a caller cannot branch on it",
		err,
	)
}

func TestBundleStoresAValidatedGamePath(t *testing.T) {
	w := newWireWorkspace(t)
	w.saveConfig(t, nil)
	w.makeGame(t)

	opts := append(w.options(), WithGamePath(w.gameDir))
	bundle := mustBundle(t, opts...)

	assert.Equal(
		t,
		domain.CleanPath(w.gameDir),
		bundle.Config.ActiveSettings().Path,
		"in-memory game path",
	)
	// The path must survive to the file, or the next process would have to be
	// told again and a CLI invocation would prompt for something already known.
	reloaded, err := storage.NewConfigStore(w.configPath).Load()
	require.NoError(t, err, "reload config")

	assert.Equal(t, domain.CleanPath(w.gameDir), reloaded.ActiveSettings().Path, "stored game path")
}

func TestBundleRefusesABadGamePathWithoutPersistingIt(t *testing.T) {
	w := newWireWorkspace(t)
	w.saveConfig(t, nil)

	// Failing construction is the point: learning it is wrong after a download has
	// started has learned nothing useful.
	empty := filepath.Join(filepath.Dir(w.gameDir), "empty")
	require.NoError(t, os.MkdirAll(empty, 0o755), "create %s", empty)

	opts := append(w.options(), WithGamePath(empty))

	bundle, err := NewBundle(context.Background(), opts...)
	require.Error(t, err, "an empty directory was accepted as a game installation")

	if bundle != nil {
		_ = bundle.Close()
	}

	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeGameInvalid),
		"error code = %v, want GAME_INVALID for %q",
		appErrors.CodeOf(err),
		empty,
	)

	reloaded, loadErr := storage.NewConfigStore(w.configPath).Load()
	require.NoError(t, loadErr, "reload config")

	assert.Equal(t, "", reloaded.ActiveSettings().Path, "game path stored for a rejected path")
}

func TestBundleMergesSourceOptionsOverTheFile(t *testing.T) {
	w := newWireWorkspace(t)
	w.saveConfig(t, func(cfg *domain.Config) {
		cfg.Sources["nexus"] = domain.SourceConfig{Enabled: true, APIKey: "file-key"}
		cfg.Sources["github"] = domain.SourceConfig{Enabled: false}
	})

	opts := append(w.options(),
		WithSourceConfig("nexus", map[string]any{"api_key": "option-key"}),
		WithSourceConfig("nonexistent", map[string]any{"api_key": "nonexistent-key"}),
	)
	bundle := mustBundle(t, opts...)

	nexus, ok := bundle.Config.SourceConfigFor("nexus")
	require.True(t, ok, "the nexus block vanished")

	assert.Equal(t, "option-key", nexus.APIKey, "nexus api key")

	assert.True(t, nexus.Enabled, "nexus was disabled by a block that never mentioned enabled")

	github, ok := bundle.Config.SourceConfigFor("github")
	require.True(t, ok, "the github block vanished")

	assert.False(
		t,
		github.Enabled,
		"a block that only another source overrode lost an explicit disable",
	)

	_, ok = bundle.Config.SourceConfigFor("nonexistent")
	assert.True(t, ok, "the nonexistent block was not added")
}

func TestBundleBuildsASourceThatWasOnlyConfiguredThroughAnOption(t *testing.T) {
	w := newWireWorkspace(t)
	w.saveConfig(t, nil)
	w.writeModArchive(t, "Better UI 1.2.0.zip")

	// Naming a source in an option has to work, so this asserts behaviour: the local
	// source is offered a folder and must find the archive in it.
	opts := append(w.options(), WithSourceConfig("local", map[string]any{
		"paths": []any{w.localDir},
	}))
	bundle := mustBundle(t, opts...)

	source, err := bundle.Deps.Sources.Get("local")
	require.NoError(t, err, "the local source is not in the registry")

	mods, err := source.Search(context.Background(), domain.SearchQuery{Query: "better"})
	require.NoError(t, err, "search the local source")

	assert.Len(t, mods, 1, "the local source found mods in %s, want 1", w.localDir)

	assert.Equal(t, "betterui", mods[0].ID, "mod id derived from the archive name")
}

func TestBundleDoesNotPersistSourceOptions(t *testing.T) {
	w := newWireWorkspace(t)
	w.saveConfig(t, nil)

	opts := append(
		w.options(),
		WithSourceConfig("local", map[string]any{"paths": []any{w.localDir}}),
	)
	mustBundle(t, opts...)

	// Only the game's path is written back: the overrides are this process's, and a
	// launcher-supplied token is not ours to persist.
	reloaded, err := storage.NewConfigStore(w.configPath).Load()
	require.NoError(t, err, "reload config")

	_, ok := reloaded.SourceConfigFor("local")
	assert.False(
		t,
		ok,
		"a source block supplied as an option was written to the configuration file",
	)
}

func TestBundleRegistersEveryBundledSource(t *testing.T) {
	w := newWireWorkspace(t)
	w.saveConfig(t, nil)

	bundle := mustBundle(t, w.options()...)

	registered := map[string]bool{}
	for _, info := range mustInfos(t, bundle.Deps.Sources) {
		registered[info.ID] = true
	}
	// Membership, not a count: any test-only source another package registered
	// into the same process-global factory registry would make the count wrong.
	for _, id := range []string{"local", "github", "nexus"} {
		assert.True(t, registered[id], "bundled source %q is missing from the registry", id)
	}
}

func mustInfos(t *testing.T, registry *services.SourceRegistry) []domain.ModSourceInfo {
	t.Helper()
	return registry.Infos()
}

func TestBundleLogsButSurvivesAMisconfiguredSource(t *testing.T) {
	w := newWireWorkspace(t)
	w.saveConfig(t, nil)

	// The factory registry is process-wide, so a broken source has to be
	// registered under a name no bundled plugin uses.
	const brokenID = "wire-test-broken"
	require.NoError(t, services.RegisterSourceFactory(
		brokenID,
		func(services.SourceBuildContext) (domain.ModSource, error) {
			return nil, errBoomCause{}
		},
	), "register broken factory")

	t.Cleanup(func() { services.UnregisterSourceFactory(brokenID) })

	// The test reads the log the bundle built because that is the channel a user
	// actually sees.
	logDir := filepath.Join(t.TempDir(), "logs")
	opts := append(w.options(), WithLoggingOptions(LoggingOptions{Dir: logDir}))
	bundle := mustBundle(t, opts...)
	t.Cleanup(func() { _ = bundle.Close() })

	assert.NotNil(t, bundle, "one misconfigured source stopped the manager from starting")

	assert.NotNil(t, bundle.Logging, "wire produced no logging service")

	require.NoError(t, bundle.Close(), "close bundle")

	logged := readLogFile(t, bundle.Logging.Directory())
	assert.Contains(t, logged, brokenID, "log does not name the broken source")

	assert.Contains(
		t,
		logged,
		"credentials are half-filled",
		"log does not carry the factory's own reason",
	)
}

// readLogFile returns the contents of the active log file, or "" when there is
// none. The file is opened only after the bundle was closed, so the sink has
// been flushed.
func readLogFile(t *testing.T, dir string) string {
	t.Helper()

	require.NotEmpty(t, dir, "the bundle logged to no directory")

	data, err := os.ReadFile(filepath.Join(dir, "dmm.log"))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}

		require.NoError(t, err, "read log file")
	}

	return string(data)
}

// errBoomCause is a plain error from a factory, so the failure has no app code
// and JoinErrors has to invent one.
type errBoomCause struct{}

func (errBoomCause) Error() string { return "credentials are half-filled" }

func TestBundleHonoursADisabledSourceBlock(t *testing.T) {
	w := newWireWorkspace(t)
	w.saveConfig(t, func(cfg *domain.Config) {
		cfg.Sources["nexus"] = domain.SourceConfig{Enabled: false}
	})

	bundle := mustBundle(t, w.options()...)

	// A disabled source stays in the registry as a describable entry so the
	// settings screen can show it; what must not happen is the factory running.
	infos := mustInfos(t, bundle.Deps.Sources)
	for _, info := range infos {
		if info.ID == "nexus" {
			assert.False(t, info.Enabled,
				"nexus reports itself enabled after being switched off in the configuration file",
			)
		}
	}
}

func TestBundleToleratesANilContext(t *testing.T) {
	w := newWireWorkspace(t)
	w.saveConfig(t, nil)

	//nolint:staticcheck // a nil context must not panic at start-up.
	bundle, err := NewBundle(
		nil,
		w.options()...)
	require.NoError(t, err, "wire with a nil context")

	t.Cleanup(func() { _ = bundle.Close() })

	assert.NotNil(t, bundle.Deps.Mods, "no mod service was wired")
}

func TestBundleCloseRunsCleanupsInReverseOrder(t *testing.T) {
	var order []string

	bundle := &Bundle{Cleanups: []func() error{
		func() error { order = append(order, "first"); return nil },
		nil,
		func() error { order = append(order, "second"); return nil },
	}}

	require.NoError(t, bundle.Close(), "close")

	assert.Equal(t, []string{"second", "first"}, order, "cleanups ran in order, want them reversed")

	assert.Nil(
		t,
		bundle.Cleanups,
		"Close left the cleanup list populated, so a second Close would run them again",
	)

	require.NoError(t, bundle.Close(), "second Close")
}

func TestBundleCloseReportsFailures(t *testing.T) {
	bundle := &Bundle{Cleanups: []func() error{
		func() error { return errBoomCause{} },
	}}

	err := bundle.Close()
	require.Error(t, err, "a failing cleanup was swallowed")
	// A single failure is returned unwrapped so a caller can still branch on the
	// code rather than having to unwrap a join.
	assert.Contains(
		t,
		err.Error(),
		"credentials are half-filled",
		"error = the cleanup's own reason",
	)

	many := &Bundle{Cleanups: []func() error{
		func() error { return errBoomCause{} },
		func() error { return appErrors.New(appErrors.ErrCodeConfigIO, "cache will not close") },
	}}

	joined := many.Close()
	require.Error(t, joined, "two failing cleanups produced no error")

	for _, want := range []string{"credentials are half-filled", "cache will not close"} {
		assert.Contains(t, joined.Error(), want, "joined error does not mention")
	}
}

func TestBundleCloseOnANilBundleIsHarmless(t *testing.T) {
	var bundle *Bundle
	require.NoError(t, bundle.Close(), "closing a nil bundle")
}

func TestNewClientInstallsAndEnablesAModEndToEnd(t *testing.T) {
	w := newWireWorkspace(t)
	w.makeGame(t)
	w.saveConfig(t, func(cfg *domain.Config) {
		cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.Path = w.gameDir })
		cfg.Sources["local"] = domain.SourceConfig{Enabled: true, Paths: []string{w.localDir}}
	})
	w.writeModArchive(t, "Better UI 1.2.0.zip")

	events := &collector{}
	opts := append(w.options(), WithEventHandler(events.handle))

	client, err := NewClient(context.Background(), opts...)
	require.NoError(t, err, "new client")

	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()

	mod, err := client.InstallMod(ctx, "local", "betterui")
	require.NoError(t, err, "install")

	assert.True(
		t,
		mod.Enabled,
		"the mod was installed but not enabled, although a game installation is configured",
	)

	wantPayload := domain.ModStoragePath(w.storageRoot, mod.GameID, mod.ID, mod.Version)
	assert.Equal(t, wantPayload, mod.LocalPath, "payload")

	_, err = os.Stat(filepath.Join(wantPayload, "textures", "hud.dds"))
	assert.NoError(t, err, "the extracted payload is missing")

	// Only the payload's own entries reach the game folder: the manifest stays in
	// storage where the manager can read it and the game cannot trip over it.
	_, err = os.Lstat(filepath.Join(w.modsDir, "textures"))
	assert.NoError(t, err, "the mod was not exposed to the game")

	_, err = os.Lstat(filepath.Join(w.modsDir, "mod.toml"))
	assert.Error(t, err, "mod.toml was deployed into the game folder")

	mods, err := client.ListMods(ctx)
	require.NoError(t, err, "list")

	assert.Len(t, mods, 1)
	assert.Equal(t, mod.ID, mods[0].ID, "ListMods returned mods, want exactly the installed one")

	installed, err := client.GetMod(ctx, mod.ID)
	require.NoError(t, err, "get")

	assert.Equal(t, "local", installed.SourceID, "source")

	require.NoError(t, client.DisableMod(ctx, mod.ID), "disable")

	_, err = os.Lstat(filepath.Join(w.modsDir, "textures"))
	assert.Error(t, err, "the mod is still in the game folder after being disabled")

	_, err = os.Stat(filepath.Join(wantPayload, "textures"))
	assert.NoError(t, err, "disabling removed the payload")

	require.NoError(t, client.EnableMod(ctx, mod.ID), "enable again")

	_, err = os.Lstat(filepath.Join(w.modsDir, "textures"))
	assert.NoError(t, err, "re-enabling did not restore the game entry")

	require.NoError(t, client.UninstallMod(ctx, mod.ID), "uninstall")

	_, err = os.Stat(wantPayload)
	assert.Error(t, err, "the payload survived an uninstall")

	_, err = os.Lstat(filepath.Join(w.modsDir, "textures"))
	assert.Error(t, err, "the game entry survived an uninstall")

	mods, err = client.ListMods(ctx)
	require.NoError(t, err, "list after uninstall")

	assert.Len(t, mods, 0, "mods survived the uninstall")

	seen := map[EventType]bool{}
	for _, event := range events.snapshot() {
		seen[event.Type] = true
	}

	for _, want := range []EventType{EventModInstallStarted, EventModInstallCompleted, EventModEnabled, EventModDisabled} {
		assert.True(t, seen[want], "no %q event reached the handler", want)
	}
}

func TestNewClientSurfacesTheCacheAndTheGame(t *testing.T) {
	w := newWireWorkspace(t)
	w.makeGame(t)
	w.saveConfig(t, func(cfg *domain.Config) {
		cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.Path = w.gameDir })
	})

	client, err := NewClient(context.Background(), w.options()...)
	require.NoError(t, err, "new client")

	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()

	info, err := client.GetCacheInfo(ctx)
	require.NoError(t, err, "cache info")

	assert.Equal(t, w.cacheRoot, info.Path, "cache path")

	assert.Equal(t, 0, info.FileCount, "a fresh cache reports files")

	game, err := client.CurrentGame(ctx)
	require.NoError(t, err, "current game")

	assert.Equal(t, domain.CleanPath(w.gameDir), game.Path, "game path")

	modsPath, err := client.GetModsPath(ctx, nil)
	require.NoError(t, err, "mods path")

	assert.Equal(t, domain.CleanPath(w.modsDir), domain.CleanPath(modsPath), "mods path")
}

func TestNewClientPersistsAConfigChangeThroughItsStore(t *testing.T) {
	w := newWireWorkspace(t)
	w.saveConfig(t, nil)

	client, err := NewClient(context.Background(), w.options()...)
	require.NoError(t, err, "new client")

	t.Cleanup(func() { _ = client.Close() })

	require.NoError(t, client.SetConfigValue(context.Background(), "ui.theme", "dark"), "set theme")

	reloaded, err := storage.NewConfigStore(w.configPath).Load()
	require.NoError(t, err, "reload config")

	assert.Equal(t, "dark", reloaded.UI.Theme, "theme on disk")
}

func TestNewClientClosesWhatItOpened(t *testing.T) {
	w := newWireWorkspace(t)
	w.saveConfig(t, nil)

	client, err := NewClient(context.Background(), w.options()...)
	require.NoError(t, err, "new client")

	require.NoError(t, client.Close(), "close")

	assert.True(t, client.Closed(), "the client does not report itself closed")

	_, err = client.ListMods(context.Background())
	assert.Error(t, err, "a closed client still served a request")
	// The database handle is closed with the client, so a second client over the
	// same tree must work rather than tripping over a stale lock.
	second, err := NewClient(context.Background(), w.options()...)
	require.NoError(t, err, "re-open the same tree")

	require.NoError(t, second.Close(), "close the second client")
}

func TestConfiguredGamePathDegradesToBlank(t *testing.T) {
	store := &configStoreStub{cfg: configPtr(domain.DefaultConfig())}

	store.cfg.UpdateActiveSettings(
		func(gs *domain.GameSettings) { gs.Path = "  /games/darktide  " },
	)
	assert.Equal(t, "/games/darktide", configuredGamePath(store), "game path trimmed value")

	blank := &configStoreStub{cfg: configPtr(domain.DefaultConfig())}
	assert.Equal(t, "", configuredGamePath(blank), "game path blank")

	failing := &configStoreStub{loadErr: errBoomCause{}}
	assert.Equal(t, "", configuredGamePath(failing), "game path blank when the file cannot be read")

	assert.Equal(t, "", configuredGamePath(nil), "game path blank for a nil store")
}

func TestHookContextFuncRereadsTheConfiguration(t *testing.T) {
	w := newWireWorkspace(t)
	store := storage.NewConfigStore(w.configPath)
	cfg := domain.DefaultConfig()
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.Path = w.gameDir })

	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.Source = "steam" })
	require.NoError(t, store.Save(&cfg), "save config")

	build := hookContextFunc(store, w.cacheRoot, w.storageRoot)
	mod := &domain.Mod{ID: "better_ui", Name: "Better UI", Version: "1.2.0"}
	got := build(mod, domain.PhasePreInstall, 3)

	assert.Equal(t, "better_ui", got.Mod.ID, "mod id")

	assert.Equal(t, domain.PhasePreInstall, got.Phase, "phase")

	assert.Equal(t, 3, got.Index, "index")

	assert.Equal(t, w.cacheRoot, got.Config.CachePath, "cache path")

	assert.Equal(t, w.storageRoot, got.Config.StoragePath, "storage path")

	assert.NotEmpty(t, got.Config.TempPath, "no temporary path was published to the script")

	assert.Equal(t, domain.CleanPath(w.gameDir), got.Game.Path, "game path")

	assert.Equal(t, "steam", got.Game.Version, "game version")

	// The game path is discovered and can change while the manager runs, so the
	// context must not close over the snapshot taken at wiring time.
	moved := filepath.Join(filepath.Dir(w.gameDir), "Darktide (Steam)")

	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.Path = moved })
	require.NoError(t, store.Save(&cfg), "save config again")

	second := build(
		mod,
		domain.PhasePostInstall,
		0,
	)
	assert.Equal(t, domain.CleanPath(moved), second.Game.Path, "game path newly stored")

	// A nil mod is legitimate for a phase the script has no mod for.
	third := build(nil, domain.PhasePreInstall, 0)
	assert.Equal(t, "", third.Mod.ID, "a nil mod produced")

	// A configuration that has become unreadable degrades to "no game folder"
	// rather than failing the hook with an error the mod author cannot act on.
	require.NoError(t, os.WriteFile(w.configPath, []byte("{broken"), 0o600), "corrupt config")

	fourth := build(mod, domain.PhasePreInstall, 0)
	assert.Equal(t, "", fourth.Game.Path, "game path blank when the configuration cannot be read")

	assert.Equal(
		t,
		w.cacheRoot,
		fourth.Config.CachePath,
		"cache path survives an unreadable configuration",
	)
}

func TestHomeDirToleratesAnEmptyEnvironment(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")

	assert.Equal(t, "", homeDir(), "home blank with no home variable set")

	t.Setenv("HOME", filepath.Join(string(filepath.Separator), "home", "modder"))

	if domain.CurrentPlatform().IsWindows() {
		t.Setenv("USERPROFILE", filepath.Join(string(filepath.Separator), "home", "modder"))
	}

	assert.NotEmpty(t, homeDir(), "home empty with a home variable set")
}
