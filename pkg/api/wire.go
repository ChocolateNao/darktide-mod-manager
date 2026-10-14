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

// Bundle is a wired graph of services without a Client wrapped around it.
//
// internal/di returns one so the GUI and the CLI can hold the individual
// services — the Wails bindings and the cobra commands both need a narrower view
// than a client provides. The alternative, building the graph twice, is how two
// halves of the same program end up disagreeing about where the configuration
// lives.
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
	// Logging is the log fan-out for this process. It is exposed so a host (the
	// CLI, the Wails bindings) can tail the file, read the level and change it
	// without building a second log service. It is never nil after a successful
	// wire: a caller that cannot have a logger at all has bigger problems.
	Logging *logging.Service
	// Cleanups release everything that was opened, in acquisition order. A
	// Bundle does not own them; the caller decides when to run them, which is
	// why Close-on-the-Client is not the only option.
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

// NewClient wires the whole manager and wraps it in a client.
//
// The order of the work matters and is worth stating, because each step depends
// on the one before it:
//
//  1. resolve the platform directories, so every path has a default;
//  2. load the configuration, which is where the user's own path overrides live;
//  3. apply the options on top, and validate a supplied game path before storing
//     it — a bad path should fail construction, not the first install;
//  4. open the database, the cache and the deployer;
//  5. register the sources that are enabled, logging rather than failing for one
//     that is misconfigured.
//
// Step 5 is the only one that can fail partially, and it deliberately does not:
// the bundled sources are announced by their packages' init functions, so a
// source whose credentials are half-filled should still be listed, marked
// unauthenticated, rather than absent.
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
		// A nil context is a caller's mistake, but it is the kind of mistake made
		// once at start-up; treating it as Background keeps it from being a
		// panic in the middle of the wiring.
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

	// The registry must know the bundled games before the configuration is
	// resolved: active_game names a registry id, and an empty one means "the
	// default", which only the registry can answer.
	if err := games.EnsureBuiltins(); err != nil {
		return nil, err
	}

	// Wrapping the store rather than fixing up the loaded copy: every later
	// reader (the game locator, the settings API, the source registry) goes
	// through the same store, so a configuration that only the wiring path
	// healed would still be broken for all of them.
	store := services.NewGameStore(storage.NewConfigStore(configPath))

	cfg, err := store.Load()
	if err != nil {
		return nil, err
	}
	// Before defaults, so a config written before the logging block existed
	// keeps its advanced.log_level instead of silently reverting to info.
	migrateLevel(cfg)
	cfg.ApplyDefaults()

	// An explicit WithGame wins over whatever the file says, but only for a
	// game the registry knows: a typo on the command line should be reported,
	// not silently healed back to the default the user was trying to leave.
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
	// The per-source option blocks win over the file, but a block supplied here
	// must not erase one that only exists in the file — a caller setting one
	// source's token should not silently unconfigure the others.
	for id, block := range set.sourceConfigs {
		existing, found := cfg.SourceConfigFor(id)
		if !found {
			// A source the file never mentions counts as enabled, which is the
			// rule the registry, RegisterAll and EnabledSources all follow. It has
			// to be restated here: the zero value of a SourceConfig is
			// Enabled=false, so merging onto it as though it were a real block
			// would mean that configuring a source's paths switched the source
			// off. The symptom is invisible until someone asks for a folder of
			// mods, at which point the source reports that it has nowhere to look.
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
	// The database sits next to the storage root rather than inside it. Mod
	// payloads live in <storage>/<game>/<id>/<version>, so a database in there
	// would sit among the mod folders — and a user who pointed the local source
	// at their storage tree would find it offering a database file as a mod.
	databasePath := filepath.Join(filepath.Dir(storageRoot), "mods.db")

	events := NewEventBus()
	now := time.Now

	// Logging is built before anything that can produce a diagnostic, and closed
	// last, so a line written while the rest of the graph shuts down still
	// reaches the file. Its own failure is not fatal: an unusable log directory
	// degrades to console only inside logging.New.
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

	// The cloud providers sit behind the registry the same way the mod sources
	// do. Providers register their own factories, so the S3 adapter (and every
	// one after it) is built from the configuration here and reaches the
	// orchestrator only through the CloudProvider interface.
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

	// The registry is built before the cache because the cache's downloader is
	// the registry: a mod's bytes come from whichever source owns it, and every
	// bundled source both lists and downloads its own mods. Nothing the cache
	// needs depends on the sources — only the other way round.
	//
	// WithConfig hands it cfg, the merged configuration, rather than letting it
	// reload the file. Source overrides are deliberately not written back — a
	// token supplied on a command line should not end up in a file — so without
	// this the registry would report a source the caller switched off as enabled,
	// having correctly declined to build it.
	// The store resolved ActiveGame to a registered id while loading, so this
	// can only fail if the registry lost its builtins in between — refuse rather
	// than build every source against the wrong game.
	gameDef, ok := games.Get(cfg.ActiveGame)
	if !ok {
		_ = joinAll(cleanups)

		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"active game %q is not registered", cfg.ActiveGame)
	}

	registry := services.NewSourceRegistryWithStore(store).
		WithConfig(func() *domain.Config { return cfg }).
		WithLogger(logService.Logger())
	if err := sources.RegisterAll(registry, cfg.Sources, gameDef); err != nil {
		// Logged, not returned. The alternative — refusing to start because one
		// optional source is misconfigured — makes the manager unusable for a
		// problem the user can see and fix in the settings screen. The error text
		// names every broken source, so the message is actionable.
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
		// Read from the file on every policy question: the active game can
		// change while the process runs (UseGame), and the permission
		// scopes are keyed by that id — a snapshot taken here would answer
		// for the game that was active at start-up. The in-memory cfg is
		// the fallback for a transient read failure.
		ConfigProvider: func() domain.HooksConfig {
			if fresh, err := store.Load(); err == nil && fresh != nil {
				return fresh.Hooks
			}

			return cfg.Hooks
		},
		Platform: platformName,
		GamePath: func() string { return configuredGamePath(store) },
		// Read from the live configuration object on every run: WithGame
		// mutates it before any hook can run, and UseGame mutates it at
		// switch time, so the permission cap for the active game applies
		// without a restart.
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

	// Actions are registered from the games themselves: a game package is the
	// only thing that knows what is special about its game, so a new game
	// becomes supported here without editing this file.
	actionRegistry := actions.NewRegistry()

	for _, game := range games.All() {
		if err := actionRegistry.RegisterGame(game); err != nil {
			_ = joinAll(cleanups)

			return nil, appErrors.Wrap(err, appErrors.ErrCodeInvalidArgument,
				"registering the actions of game %q", game.ID())
		}
	}

	// One launcher serves both the action runner and the client's source-link
	// methods: "open this address in the user's browser" is the same act however
	// it was asked for, and two instances would mean two places to change the
	// allowlist or the platform's opener command.
	urlLauncher := &platform.URLLauncher{Platform: platformName}

	actionRunner, err := actions.NewRunner(actions.RunnerDeps{
		Registry: actionRegistry,
		// internal/games satisfies DefinitionLookup as it stands, so the runner
		// can describe an action belonging to a game the user has not made
		// active without the runner knowing how games are registered.
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
		Config:       p2pConfig(cfg),
		Clock:        now,
	})

	// Closing the service closes every peer connection and deletes every staged
	// archive, so it has to run before the mod service and the database go away.
	// Prepending rather than appending keeps that ordering true however the rest
	// of the wiring grows.
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

// p2pConfig builds the transport configuration for peer-to-peer sharing.
//
// The STUN servers the user configured are the default, and Google's public
// server is the last resort. A relay is never added automatically: a TURN server
// forwards the payload, which is the one thing this feature exists to avoid.
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
			// pion narrates its own internals at a volume that would bury a
			// CLI's output — "RTP stream is already closed" is what a
			// successful teardown looks like from inside the library. The
			// adapter writes into the one logger this process has, so that
			// volume lands in the log file and costs nothing until somebody
			// asks for it by lowering the level.
			PionLogger: logging.PionLogger(),
		},
		DefaultExpiry: expiry,
	}
}

// buildLogging returns the log service for this process and its cleanup.
//
// A service supplied through WithLogging is adopted as-is — the caller built it
// deliberately, usually before the config was even readable — except that the
// event sink is attached so the GUI's log view works. Otherwise the service is
// built from the persisted logging block plus the per-invocation overrides.
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

// persistGamePath validates a caller-supplied game path and stores it.
//
// The validation happens here rather than lazily so a bad path is a construction
// failure. A caller that discovers it has the wrong path thirty seconds later,
// after a download has started, has learned nothing useful.
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

// configuredGamePath reads the stored game path, returning "" when it cannot be
// read. It is the hook executor's GamePath source and runs on every hook, so a
// configuration that has become unreadable degrades to "no game folder" instead of
// failing the hook with a configuration error the mod author cannot act on.
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

// hookContextFunc builds the per-script hook context.
//
// It reads the configuration on every call instead of closing over a snapshot:
// the game path is discovered and can change while the manager runs, and a hook
// context pointing at a stale install is how a script ends up writing into a
// folder the user deleted an hour ago. The struct is built fresh per call for
// the same reason — hooks can run concurrently.
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

// registryProvider adapts the source registry to the context-aware shape the mod
// service depends on.
//
// The registry's Get takes no context because building a source is local work —
// it reads configuration and constructs an object — so there is nothing for a
// context to interrupt. The mod service still wants the deadline honoured before
// it goes looking, so the cancellation check is made here rather than dropped.
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

// joinAll runs cleanups and discards their failures. It is for the error paths of
// the wiring itself, where the caller is about to be handed a more interesting
// failure and has nowhere to report a second one.
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
