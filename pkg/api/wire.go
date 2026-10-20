package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/actions"
	"github.com/ChocolateNao/darktide-mod-manager/internal/games"
	"github.com/ChocolateNao/darktide-mod-manager/internal/logging"
	"github.com/ChocolateNao/darktide-mod-manager/internal/platform"
	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/cloud"
	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/hooks"
	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/hooks/host"
	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/sources"
	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/internal/sharing/p2p"
	"github.com/ChocolateNao/darktide-mod-manager/internal/storage"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// Bundle is a wired graph of services without a Client wrapped around it; internal/di
// returns one so the Wails and cobra fronts can hold the individual services. Building
// the graph twice is how two halves of the same program disagree about where config lives.
type Bundle struct {
	// Deps is what a Client needs.
	Deps ClientDeps
	// Config is the configuration as loaded, with the option overrides applied.
	Config *domain.Config
	// Store is the configuration file.
	Store services.ConfigStore
	// DatabasePath is where the mod index lives.
	DatabasePath string
	// CacheRoot is where downloaded archives live.
	CacheRoot string
	// StorageRoot is where mod payloads live.
	StorageRoot string
	// Logging is the log fan-out for this process, exposed so a host (the CLI, the
	// Wails bindings) can tail the file, read the level and change it without building
	// a second log service. Never nil after a successful wire.
	Logging *logging.Service
	// Cleanups release everything that was opened, in acquisition order. A Bundle does not
	// own them: the caller decides when to run them, so Client.Close is not the only option.
	Cleanups []func() error
}

// Close runs the bundle's cleanups in reverse order and joins their failures.
func (b *Bundle) Close() error {
	if b == nil {
		return nil
	}

	var failures []error

	for i := len(b.Cleanups) - 1; i >= 0; i-- {
		if b.Cleanups[i] == nil {
			continue
		}

		if err := b.Cleanups[i](); err != nil {
			failures = append(failures, err)
		}
	}

	b.Cleanups = nil

	switch len(failures) {
	case 0:
		return nil
	case 1:
		return failures[0]
	default:
		return errors.Join(failures...)
	}
}

// NewClient wires the whole manager and wraps it in a client. The order is load-bearing:
// platform directories give every path a default, the configuration loads and options are
// applied on top (a validated game path fails construction rather than the first install),
// then database, cache and deployer open, and the enabled sources register.
//
// Only source registration can fail partially, and it deliberately does not: bundled
// sources announce themselves via their packages' init functions, so a source with
// half-filled credentials is listed unauthenticated rather than absent.
func NewClient(ctx context.Context, opts ...Option) (*Client, error) {
	set, err := applyOptions(opts)
	if err != nil {
		return nil, err
	}

	bundle, err := wire(ctx, set)
	if err != nil {
		return nil, err
	}

	clientOpts := make([]Option, 0, len(set.handlers)+1)
	for _, handler := range set.handlers {
		clientOpts = append(clientOpts, WithEventHandler(handler))
	}

	if set.logger != nil {
		clientOpts = append(clientOpts, WithLogger(set.logger))
	}

	client, err := NewClientFromServices(bundle.Deps, clientOpts...)
	if err != nil {
		_ = bundle.Close()
		return nil, err
	}

	client.cleanups = bundle.Cleanups

	return client, nil
}

// NewBundle wires the graph and returns it without a client.
//
// It is exported for internal/di, and it is the same code NewClient runs: a
// second wiring path is the drift this package exists to prevent.
func NewBundle(ctx context.Context, opts ...Option) (*Bundle, error) {
	set, err := applyOptions(opts)
	if err != nil {
		return nil, err
	}

	return wire(ctx, set)
}

// wire builds the graph from already-applied settings.
func wire(ctx context.Context, set *settings) (*Bundle, error) {
	if ctx == nil {
		// A nil context is a start-up-time caller mistake; treating it as Background
		// keeps it diagnosable rather than a panic mid-wiring.
		ctx = context.Background()
	}

	platformName := domain.CurrentPlatform()

	dirs, err := platform.CurrentDirs()
	if err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeNoStoragePath,
			"cannot locate the manager's directories: %v", err)
	}

	configPath := set.configPath
	if configPath == "" {
		configPath = dirs.ConfigFilePath()
	}

	// The registry must know the bundled games first: active_game names a registry id,
	// and an empty value means "the default", which only the registry can answer.
	if err := games.EnsureBuiltins(); err != nil {
		return nil, err
	}

	// Wrap the store rather than fix up the loaded copy: every later reader goes through
	// the same store, so a configuration only the wiring path healed would still be broken.
	store := services.NewGameStore(storage.NewConfigStore(configPath))

	cfg, err := store.Load()
	if err != nil {
		return nil, err
	}
	// Before defaults, so a config written before the logging block existed
	// keeps its advanced.log_level instead of silently reverting to info.
	migrateLevel(cfg)
	cfg.ApplyDefaults()

	// An explicit WithGame wins over the file, but only for a registered id: a command-line
	// typo should be reported, not silently healed back to the default being left behind.
	if id := strings.TrimSpace(set.gameID); id != "" {
		def, ok := games.Get(id)
		if !ok {
			return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
				"game %q is not registered", id)
		}

		cfg.ActiveGame = def.ID()

		if _, ok := cfg.GameSettingsFor(def.ID()); !ok {
			cfg.SetGameSettings(def.ID(), domain.DefaultGameSettings())
		}
	}

	if set.gamePath != "" {
		if err := persistGamePath(cfg, store, set.gamePath); err != nil {
			return nil, err
		}
	}

	if set.cachePath != "" {
		cfg.Cache.Path = set.cachePath
	}

	if set.storagePath != "" {
		cfg.Storage.Path = set.storagePath
	}
	// Per-source blocks win over the file, but must not erase a file-only block: setting
	// one source's token should not silently unconfigure the others.
	for id, block := range set.sourceConfigs {
		existing, found := cfg.SourceConfigFor(id)
		if !found {
			// Enabled is the rule RegisterAll and EnabledSources follow; restated because a
			// zero SourceConfig has Enabled=false, so merging onto it would switch the source off.
			existing = domain.SourceConfig{Enabled: true}
		}

		cfg.Sources[id] = mergeSourceBlock(existing, block, set.sourceEnabledStated[id])
	}

	cacheRoot := strings.TrimSpace(cfg.Cache.Path)
	if cacheRoot == "" {
		cacheRoot = dirs.Cache
	}

	storageRoot := strings.TrimSpace(cfg.Storage.Path)
	if storageRoot == "" {
		storageRoot = dirs.StorageRoot()
	}
	// Next to the storage root, not inside it: payloads live in
	// <storage>/<game>/<id>/<version>, so a database in there would be offered as a
	// mod to anyone pointing the local source at storage.
	databasePath := filepath.Join(filepath.Dir(storageRoot), "mods.db")

	events := NewEventBus()
	now := time.Now

	// Built before anything that can emit a diagnostic and closed last, so shutdown-time
	// lines still reach the file. Its own failure is not fatal: an unusable log directory
	// degrades to console-only inside logging.New.
	logService, logCleanup := buildLogging(set, cfg, events)
	cleanups := []func() error{logCleanup}

	db, err := storage.Open(ctx, databasePath)
	if err != nil {
		_ = joinAll(cleanups)
		return nil, err
	}

	cleanups = append(cleanups, db.Close)

	index, err := storage.NewSQLiteModRepositoryFromDB(db)
	if err != nil {
		_ = joinAll(cleanups)
		return nil, err
	}

	shareStore := storage.NewShareStore(db)

	// Providers register their own factories, so the S3 adapter (and every one after) is built
	// here from configuration and reaches the orchestrator only through CloudProvider.
	cloudRegistry := services.NewCloudRegistry()
	if err := cloud.RegisterConfigured(
		cloudRegistry,
		cfg.CloudSharing,
		*logService.Logger(),
	); err != nil {
		// Not fatal: sharing is optional, and a provider that will not build
		// simply does not appear in the picker.
		logService.Logger().Warn().Err(err).Msg("cloud providers not configured")
	}

	// The registry is built before the cache because the cache's downloader is the registry:
	// a mod's bytes come from whichever source owns it. WithConfig hands it the merged cfg
	// rather than letting it reload the file — source overrides are never written back, so
	// reloading would report a deliberately declined source as enabled.
	gameDef, ok := games.Get(cfg.ActiveGame)
	if !ok {
		// The store resolved ActiveGame to a registered id while loading, so failure here
		// means the registry lost its builtins: refuse rather than build for the wrong game.
		_ = joinAll(cleanups)

		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"active game %q is not registered", cfg.ActiveGame)
	}

	registry := services.NewSourceRegistryWithStore(store).
		WithConfig(func() *domain.Config { return cfg }).
		WithLogger(logService.Logger())
	if err := sources.RegisterAll(registry, cfg.Sources, gameDef); err != nil {
		// Logged, not returned: refusing to start over one misconfigured optional source makes
		// the manager unusable for a fix the user can make in settings. The message names each.
		logService.Logger().Warn().
			Err(err).
			Msg("some sources could not be configured")
	}

	cacheStore, err := storage.NewCache(cacheRoot)
	if err != nil {
		_ = joinAll(cleanups)
		return nil, err
	}

	cleanups = append(cleanups, cacheStore.Close)

	cacheManager, err := services.NewCacheManager(services.CacheManagerDeps{
		Cache: cacheStore,
		// Resolves each mod's source through the registry, so one cache serves
		// every provider instead of being pinned to whichever was configured.
		Download:  services.NewRegistryDownloader(registry),
		Config:    cfg.Cache,
		EventSink: events,
		Clock:     now,
		Logger:    logService.Logger(),
	})
	if err != nil {
		_ = joinAll(cleanups)
		return nil, err
	}

	locator := services.NewGameLocator(store).WithLogger(logService.Logger())

	hookExecutor := services.NewHookExecutor(services.HookExecutorDeps{
		Config: cfg.Hooks,
		// Re-read the file on every policy question: the active game can change while the
		// process runs (UseGame) and permission scopes are keyed by that id, so a snapshot
		// would answer for the start-up game. cfg is the fallback for a transient read failure.
		ConfigProvider: func() domain.HooksConfig {
			if fresh, err := store.Load(); err == nil && fresh != nil {
				return fresh.Hooks
			}

			return cfg.Hooks
		},
		Platform: platformName,
		GamePath: func() string { return configuredGamePath(store) },
		// Read from the live configuration on every run: WithGame/UseGame mutate it before
		// any hook runs, so the permission cap for the active game applies without a restart.
		GameID:  func() string { return cfg.ActiveGame },
		Context: hookContextFunc(store, cacheRoot, storageRoot),
		Events:  events,
		Clock:   now,
		Logger:  logService.Logger(),
	})

	// The one construction of the sandbox's providers: every runner reaches the
	// manager through exactly these four bridges and nothing else.
	providers := host.Providers{
		Config:  hookConfigAccess{store: store},
		Storage: hookStorageAccess{index: index, storageRoot: storageRoot},
		TempDir: os.TempDir(),
		Logger:  hookLogFunc(logService.Logger()),
	}

	if err := hooks.RegisterAll(hookExecutor, providers); err != nil {
		// A hook runner that will not register means no lifecycle can run
		// safely, which is fatal rather than a degraded source.
		_ = joinAll(cleanups)
		return nil, err
	}

	modService, err := services.NewModService(services.ModServiceDeps{
		Sources:   registryProvider{registry: registry},
		Cache:     cacheManager,
		Index:     index,
		Hooks:     hookExecutor,
		Locator:   locator,
		Deployer:  platform.NewDeployer(platformName),
		Extractor: storage.NewArchiveExtractor(),
		Config:    store,
		Events:    events,
		Clock:     now,
		Platform:  platformName,
		// Left empty unless the caller overrode it, so a configuration change to
		// storage.path takes effect without a restart. See settings.storagePath.
		StoragePath: set.storagePath,
		Logger:      logService.Logger(),
	})
	if err != nil {
		_ = joinAll(cleanups)
		return nil, err
	}

	// Actions are registered from the games themselves: a game package is the only thing
	// that knows what is special about its game, so a new game needs no edit to this file.
	actionRegistry := actions.NewRegistry()

	for _, game := range games.All() {
		if err := actionRegistry.RegisterGame(game); err != nil {
			_ = joinAll(cleanups)

			return nil, appErrors.Wrap(err, appErrors.ErrCodeInvalidArgument,
				"registering the actions of game %q", game.ID())
		}
	}

	// One launcher serves both the action runner and the source-link methods: "open this
	// address in a browser" is the same act whoever asks, and two instances mean two allowlists.
	urlLauncher := &platform.URLLauncher{Platform: platformName}

	actionRunner, err := actions.NewRunner(actions.RunnerDeps{
		Registry: actionRegistry,
		// games.Lookup already satisfies DefinitionLookup, so the runner can describe an
		// inactive game's action without knowing how games are registered.
		Definitions: games.Lookup{},
		Games:       locator,
		Opener:      urlLauncher,
		Commands:    actions.NewExecRunner(),
		// An action runs somebody else's program; its output belongs in the log
		// next to the rest of the run, not only in the panel that triggered it.
		Echo:     logging.ActionOutput(),
		Platform: platformName,
	})
	if err != nil {
		_ = joinAll(cleanups)
		return nil, err
	}

	shareService := services.NewShareService(services.ShareServiceDeps{
		Registry:     cloudRegistry,
		Store:        shareStore,
		GameLocator:  locator,
		ModInstaller: modService,
		Logger:       *logService.Logger(),
		Config: &services.ShareConfig{
			DefaultProvider:   cfg.CloudSharing.DefaultProvider,
			FallbackProviders: cfg.CloudSharing.FallbackProviders,
			ShareExpiryDays:   cfg.CloudSharing.ShareExpiryDays,
			AutoInstall:       cfg.CloudSharing.AutoInstall,
		},
		Clock: now,
	})

	p2pShareService := services.NewP2PShareService(services.P2PShareServiceDeps{
		GameLocator:  locator,
		ModInstaller: modService,
		Logger:       *logService.Logger(),
		Events:       events,
		Config:       p2pConfig(cfg),
		Clock:        now,
	})

	// Closing the service closes every peer connection and deletes staged archives, so it
	// must run before the mod service and database go away; prepending keeps that true.
	cleanups = append([]func() error{func() error {
		p2pShareService.Close()

		return nil
	}}, cleanups...)

	return &Bundle{
		Deps: ClientDeps{
			Mods:         modService,
			Sources:      registry,
			Game:         locator,
			Cache:        cacheManager,
			Hooks:        hookExecutor,
			Shares:       shareService,
			P2PShares:    p2pShareService,
			Actions:      actionRegistry,
			ActionRunner: actionRunner,
			URLs:         urlLauncher,
			Config:       store,
			// The same pointer the closures above capture, so a runtime switch
			// (UseGame) that mutates it is visible to every policy question.
			ActiveConfig: cfg,
			Events:       events,
			Clock:        now,
		},
		Config:       cfg,
		Store:        store,
		DatabasePath: databasePath,
		CacheRoot:    cacheRoot,
		StorageRoot:  storageRoot,
		Logging:      logService,
		Cleanups:     cleanups,
	}, nil
}

// p2pConfig builds the transport configuration for peer-to-peer sharing. The STUN servers
// the user configured come first and Google's public server is the last resort. A relay is
// never added automatically: a TURN server forwards the payload, what this feature avoids.
func p2pConfig(cfg *domain.Config) *services.P2PShareConfig {
	servers := []string(nil)

	var (
		expiry    time.Duration
		chunkSize int
	)

	if cfg != nil {
		for _, server := range cfg.Sharing.P2P.STUNServers {
			if trimmed := strings.TrimSpace(server); trimmed != "" {
				servers = append(servers, trimmed)
			}
		}

		if cfg.Sharing.P2P.ExpiryDays > 0 {
			expiry = time.Duration(cfg.Sharing.P2P.ExpiryDays) * 24 * time.Hour
		}

		if cfg.Sharing.P2P.ChunkSizeKB > 0 {
			chunkSize = cfg.Sharing.P2P.ChunkSizeKB * 1024
		}
	}

	servers = append(servers, p2p.DefaultSTUNServers()...)

	return &services.P2PShareConfig{
		Peer: p2p.Config{
			STUNServers: servers,
			ChunkSize:   chunkSize,
			// pion narrates its internals loudly enough to bury CLI output — "RTP stream is already
			// closed" is normal teardown — so the adapter writes into the one process logger; the
			// noise costs nothing until somebody lowers the level.
			PionLogger: logging.PionLogger(),
		},
		DefaultExpiry: expiry,
	}
}

// buildLogging returns the log service for this process and its cleanup. A WithLogging
// service is adopted as-is — the caller built it deliberately, usually before the config
// was readable — except that the event sink is attached so the GUI's log view works.
func buildLogging(
	set *settings,
	cfg *domain.Config,
	events *EventBus,
) (*logging.Service, func() error) {
	service := set.logging
	if service == nil {
		service = logging.New(loggingConfig(cfg, set.loggingOpts))
	}

	service.SetEvents(events)

	return service, service.Close
}

// persistGamePath validates a caller-supplied game path and stores it. Validation is up
// front so a bad path is a construction failure rather than a discovery made mid-download.
func persistGamePath(cfg *domain.Config, store services.ConfigStore, path string) error {
	gameID := strings.TrimSpace(cfg.ActiveGame)
	if gameID == "" {
		gameID = games.DefaultID()
	}

	def, _ := games.Get(gameID)

	detector := platform.NewDetector(def, domain.CurrentPlatform(), homeDir(), platform.OSEnv(),
		[]string{path})

	install, err := detector.ValidateInstallation(path)
	if err != nil {
		return err
	}
	// Store the cleaned path the detector confirmed rather than the caller's
	// spelling, so the file and the value the locator will probe agree.
	cfg.UpdateActiveSettings(func(settings *domain.GameSettings) {
		settings.Path = install.Path
	})
	updated := cfg.Clone()
	updated.ApplyDefaults()

	if _, err := store.SaveIfChanged(updated); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO,
			"could not store the game path: %v", err)
	}

	return nil
}

// homeDir resolves the home directory, tolerating the failure that leaves the
// detector with fewer candidate locations rather than failing the wiring.
func homeDir() string {
	home, err := platform.HomeDir(domain.CurrentPlatform(), platform.OSEnv())
	if err != nil {
		return ""
	}

	return home
}

// configuredGamePath reads the stored game path, returning "" when it cannot be read.
// It runs on every hook, so an unreadable configuration degrades to "no game folder"
// rather than failing the hook with an error the mod author cannot act on.
func configuredGamePath(store services.ConfigStore) string {
	if store == nil {
		return ""
	}

	cfg, err := store.Load()
	if err != nil || cfg == nil {
		return ""
	}

	return strings.TrimSpace(cfg.ActiveSettings().Path)
}

// hookContextFunc builds the per-script hook context, reading the configuration on every
// call instead of closing over a snapshot: the game path is discovered and can change while
// the manager runs, and a context pointing at a stale install is how a script writes into
// a folder the user deleted an hour ago. The struct is fresh per call — hooks run concurrently.
func hookContextFunc(
	store services.ConfigStore,
	cacheRoot, storageRoot string,
) services.HookContextFunc {
	return func(mod *domain.Mod, phase domain.HookPhase, index int) domain.HookContext {
		paths := services.HookContextPaths{
			CachePath:   cacheRoot,
			TempPath:    os.TempDir(),
			StoragePath: storageRoot,
		}

		if store != nil {
			if cfg, err := store.Load(); err == nil && cfg != nil {
				settings := cfg.ActiveSettings()
				paths.GamePath = strings.TrimSpace(settings.Path)
				paths.GameVersion = strings.TrimSpace(string(settings.Source))
			}
		}

		return paths.Context(mod, phase, index)
	}
}

// registryProvider adapts the source registry to the context-aware shape the mod service
// depends on. The registry's Get takes no context because building a source is local work;
// the cancellation check lives here so the deadline is honoured before any lookup.
type registryProvider struct {
	registry *services.SourceRegistry
}

func (p registryProvider) Get(ctx context.Context, id string) (domain.ModSource, error) {
	if ctx != nil && ctx.Err() != nil {
		return nil, appErrors.New(appErrors.ErrCodeOperationCanceled,
			"operation stopped before %q could be resolved", id)
	}

	return p.registry.Get(id)
}

// joinAll runs cleanups and discards their failures: it is for the wiring's own error
// paths, where the caller is about to be handed a more interesting failure to report.
func joinAll(cleanups []func() error) error {
	var failures []error

	for i := len(cleanups) - 1; i >= 0; i-- {
		if cleanups[i] == nil {
			continue
		}

		if err := cleanups[i](); err != nil {
			failures = append(failures, err)
		}
	}

	return errors.Join(failures...)
}
