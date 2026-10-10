package services

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/rs/zerolog"
)

// SourceBuildContext is handed to every source factory instead of the bare
// configuration block. Game carries the active game definition so a source can
// read its per-game defaults — Nexus's game domain, GitHub's search topic —
// without the plugin knowing which game is active; Config is that source's own
// configuration block, unchanged.
type SourceBuildContext struct {
	Game   domain.GameDefinition
	Config domain.SourceConfig
}

// SourceFactory builds a mod source from its build context. Plugins register
// one of these at init time so the core never has to know a concrete source
// type: adding Nexus or a private tracker means writing one function and
// calling RegisterSourceFactory, with no change here.
type SourceFactory func(SourceBuildContext) (domain.ModSource, error)

var (
	factoryMu sync.RWMutex
	// factories is keyed by plain source id (generic sources); gameFactories by
	// SourceKey(gameID, id) so a source that exists for one game cannot collide
	// with the same id on another game or with the generic fallback.
	factories     = map[string]SourceFactory{}
	gameFactories = map[string]SourceFactory{}
)

// SourceKey builds the lookup key for a game-scoped source factory, e.g.
// "darktide:racemenu-tool". Both halves are normalised the same way every
// other id lookup in this file normalises them, so a caller that writes
// "Darktide" and a plugin that registers "darktide" meet.
func SourceKey(gameID, id string) string {
	return normalizeGameID(gameID) + ":" + normalizeSourceID(id)
}

// normalizeGameID folds a game id to its canonical form.
func normalizeGameID(gameID string) string {
	return strings.ToLower(strings.TrimSpace(gameID))
}

// RegisterSourceFactory records the constructor for a generic source id — one
// that is available to every game. It is called from plugin init functions, so
// a duplicate id is a programming error rather than something a user can
// trigger — the last registration silently losing would produce a source that
// never gets used.
func RegisterSourceFactory(id string, factory SourceFactory) error {
	key := strings.TrimSpace(id)
	if key == "" {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "source id must not be empty")
	}

	if factory == nil {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "source %q has a nil factory", key)
	}

	factoryMu.Lock()
	defer factoryMu.Unlock()
	// The id is normalised the same way Get normalises it before looking a source
	// up. Storing "GitHub" under its literal spelling and looking it up as
	// "github" would make a plugin that registered a capitalised id silently
	// unreachable, which is exactly the kind of failure that only shows up as a
	// source that is listed but never appears.
	key = normalizeSourceID(key)
	if _, exists := factories[key]; exists {
		return appErrors.New(
			appErrors.ErrCodeSourceExists,
			"source %q already registered a factory",
			key,
		)
	}

	factories[key] = factory

	return nil
}

// RegisterGameSourceFactory records a constructor that only exists for one
// game. The registry prefers it over the generic factory of the same id when
// that game is active, so a game-unique source (or a game-specific build of a
// shared one) never has to compete with, or replace, the generic plugin.
func RegisterGameSourceFactory(gameID, id string, factory SourceFactory) error {
	game := normalizeGameID(gameID)
	if game == "" {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "game id must not be empty")
	}

	key := strings.TrimSpace(id)
	if key == "" {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "source id must not be empty")
	}

	if factory == nil {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "source %q has a nil factory", key)
	}

	factoryMu.Lock()
	defer factoryMu.Unlock()

	key = normalizeSourceID(key)

	full := game + ":" + key
	if _, exists := gameFactories[full]; exists {
		return appErrors.New(
			appErrors.ErrCodeSourceExists,
			"source %q already registered a factory for game %q",
			key,
			game,
		)
	}

	gameFactories[full] = factory

	return nil
}

// RegisteredFactoryIDs lists the generic source ids that have a factory,
// sorted, so callers and tests have a deterministic view of the plugin set.
// Game-scoped ids are listed by RegisteredGameFactoryIDs.
func RegisteredFactoryIDs() []string {
	factoryMu.RLock()
	defer factoryMu.RUnlock()

	out := make([]string, 0, len(factories))
	for id := range factories {
		out = append(out, id)
	}

	sort.Strings(out)

	return out
}

// RegisteredGameFactoryIDs lists the plain source ids that have a factory
// registered for gameID, sorted. Keys are returned without the game prefix so
// the caller sees the same ids it would see for a generic source.
func RegisteredGameFactoryIDs(gameID string) []string {
	game := normalizeGameID(gameID)
	if game == "" {
		return nil
	}

	factoryMu.RLock()
	defer factoryMu.RUnlock()

	prefix := game + ":"
	seen := make(map[string]struct{})

	out := make([]string, 0)

	for key := range gameFactories {
		if !strings.HasPrefix(key, prefix) {
			continue
		}

		id := strings.TrimPrefix(key, prefix)
		if id == "" {
			continue
		}

		if _, dup := seen[id]; dup {
			continue
		}

		seen[id] = struct{}{}
		out = append(out, id)
	}

	sort.Strings(out)

	return out
}

// SourceFactoryFor returns the constructor registered for a source id without
// building anything. The registry uses it to tell "this source is not installed"
// from "this source is installed but misconfigured"; the plugin wiring in
// internal/plugins/sources uses it to build every registered source eagerly at
// start-up, which is what makes a misconfigured source visible in the UI instead
// of only when the user first clicks it.
//
// The id is matched case-insensitively and with surrounding space ignored, the
// same way every other lookup on the registry behaves.
func SourceFactoryFor(id string) (SourceFactory, bool) {
	key := normalizeSourceID(id)
	if key == "" {
		return nil, false
	}

	factoryMu.RLock()
	defer factoryMu.RUnlock()

	factory, ok := factories[key]

	return factory, ok
}

// SourceFactoryForGame returns the constructor registered for one game's
// source id, without building anything. It is the game-scoped counterpart of
// SourceFactoryFor and the first place the registry looks when a game is
// active.
func SourceFactoryForGame(gameID, id string) (SourceFactory, bool) {
	game := normalizeGameID(gameID)

	key := normalizeSourceID(id)
	if game == "" || key == "" {
		return nil, false
	}

	factoryMu.RLock()
	defer factoryMu.RUnlock()

	factory, ok := gameFactories[game+":"+key]

	return factory, ok
}

// UnregisterSourceFactory removes a registered factory and reports whether one
// was there. No production code needs it — a source stays available for the life
// of the process — but tests share one process-wide registry, so without a way
// to undo a registration every test would have to invent a fresh id and the set
// of ids would grow without bound.
func UnregisterSourceFactory(id string) bool {
	key := normalizeSourceID(id)
	if key == "" {
		return false
	}

	factoryMu.Lock()
	defer factoryMu.Unlock()

	if _, ok := factories[key]; !ok {
		return false
	}

	delete(factories, key)

	return true
}

// UnregisterGameSourceFactory removes a game-scoped registration and reports
// whether one was there. Like UnregisterSourceFactory it exists for tests.
func UnregisterGameSourceFactory(gameID, id string) bool {
	game := normalizeGameID(gameID)

	key := normalizeSourceID(id)
	if game == "" || key == "" {
		return false
	}

	factoryMu.Lock()
	defer factoryMu.Unlock()

	full := game + ":" + key
	if _, ok := gameFactories[full]; !ok {
		return false
	}

	delete(gameFactories, full)

	return true
}

// SourceRegistry holds the live mod sources. It is the only place the rest of
// the program resolves a source id to something it can search or download from.
//
// Two kinds of member coexist. Directly registered sources (Register) are live
// objects — the unit tests use this path, and so does any plugin that builds
// its source eagerly. Factory-registered sources are configured but lazy: the
// factory runs the first time the source is actually used, so a source whose
// credentials are missing does not break construction of the whole registry.
type SourceRegistry struct {
	store         ConfigStore
	sources       map[string]domain.ModSource
	instantiated  map[string]domain.ModSource
	failures      map[string]error
	authenticated map[string]bool
	config        ConfigProvider
	game          domain.GameDefinition
	log           *zerolog.Logger
	mu            sync.RWMutex
	buildMu       sync.Mutex
}

// NewSourceRegistry returns an empty registry.
func NewSourceRegistry() *SourceRegistry {
	return newSourceRegistry(nil)
}

// NewSourceRegistryWithStore returns a registry that hands each source's
// config block to that source's factory. Without a store every factory sees the
// default config, which is enough for sources that need no credentials.
func NewSourceRegistryWithStore(store ConfigStore) *SourceRegistry {
	return newSourceRegistry(store)
}

// ConfigProvider supplies the configuration the registry consults. A nil result
// means "no opinion", and the registry falls back to its store.
type ConfigProvider func() *domain.Config

// WithConfig makes the registry read one configuration in memory instead of
// reloading the file.
//
// It exists because the composition root merges the caller's overrides into the
// file's configuration and deliberately does not write that back — a token typed
// on a command line should not end up in a file — while RegisterAll is handed the
// merged value. Without this the registry would report a source the caller had
// switched off as enabled, having correctly declined to build it: the settings
// screen would show a green tick beside something the manager never uses.
func (r *SourceRegistry) WithConfig(provider ConfigProvider) *SourceRegistry {
	if r == nil {
		return r
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.config = provider

	return r
}

// WithLogger attaches a logger for the source lifecycle. Like WithConfig it is a
// fluent setter rather than a constructor argument: the registry is built in
// several places and the log service is assembled after the event bus, which the
// registry never sees.
func (r *SourceRegistry) WithLogger(logger *zerolog.Logger) *SourceRegistry {
	if r == nil {
		return r
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.log = loggerOf(logger)

	return r
}

// WithGame sets the game whose factories this registry resolves first and
// whose definition every factory is handed in its SourceBuildContext.
//
// One registry serves one active game: switching games means rebuilding the
// registry (the composition root drops the old one and wires a new one), so
// cached instances of the previous game's sources are never handed out under
// the new game's identity.
func (r *SourceRegistry) WithGame(def domain.GameDefinition) *SourceRegistry {
	if r == nil {
		return r
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.game = def

	return r
}

// RebuildForGame switches the active game and drops every cached instance and
// recorded failure so the next Get builds against the new definition. It is
// the in-place form of "drop the registry and wire a new one": callers that
// hold the registry pointer (the mod service, the cache downloader, the
// registry provider) keep working without a graph rebuild.
//
// Directly registered sources and remembered credentials survive — neither
// depends on which game is active. The caller is expected to re-register the
// game-scoped factories afterwards (sources.RegisterAll with the new def).
func (r *SourceRegistry) RebuildForGame(def domain.GameDefinition) {
	if r == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.game = def
	r.instantiated = map[string]domain.ModSource{}
	r.failures = map[string]error{}
}

// activeGame returns the game this registry resolves for, or nil when none was
// set (tests, and callers that only ever use generic sources).
func (r *SourceRegistry) activeGame() domain.GameDefinition {
	if r == nil {
		return nil
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.game
}

func newSourceRegistry(store ConfigStore) *SourceRegistry {
	return &SourceRegistry{
		sources:       map[string]domain.ModSource{},
		instantiated:  map[string]domain.ModSource{},
		failures:      map[string]error{},
		authenticated: map[string]bool{},
		store:         store,
		log:           &nopLogger,
	}
}

// Register adds an already-built source.
//
// Ids are matched case-insensitively: config files are hand-edited and the UI
// lowercases ids, so "Vortex" and "vortex" must not end up as two entries. The
// first registration of an id wins and the id is normalised to lower case, so a
// later duplicate is reported instead of silently replacing a working source.
func (r *SourceRegistry) Register(source domain.ModSource) error {
	if source == nil {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "cannot register a nil source")
	}

	id := strings.ToLower(strings.TrimSpace(source.ID()))
	if id == "" {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "source returned an empty id")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, ok := r.sources[id]; ok {
		if sameSource(existing, source) {
			// Registering the same instance twice is harmless; plugins that run
			// from several init paths should not fail over it.
			return nil
		}

		return appErrors.New(appErrors.ErrCodeSourceExists, "source %q is already registered", id)
	}

	r.sources[id] = source
	delete(r.failures, id)

	return nil
}

// Unregister removes a live source. It is what config changes and tests use to
// model "the user turned this source off". Factories are untouched, so the
// source can be built again later.
func (r *SourceRegistry) Unregister(id string) bool {
	key := normalizeSourceID(id)
	if key == "" {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	_, live := r.sources[key]
	delete(r.sources, key)
	delete(r.instantiated, key)
	delete(r.failures, key)
	// Authentication state is kept: the credentials did not change just because
	// the source object was dropped, and forcing a re-login on every reload
	// would be a regression for a source that authenticated successfully.
	return live
}

// Get resolves a source id to a usable source.
//
// Lookup order is: a directly registered source, then an already-instantiated
// factory source, then the factory — the active game's game-scoped factory
// first, the generic one after it. A factory failure is cached and replayed,
// because a factory that fails because an API key is missing will fail again
// on the next call and re-running it would turn one clear error into a stream
// of confusing ones.
func (r *SourceRegistry) Get(id string) (domain.ModSource, error) {
	key := normalizeSourceID(id)
	if key == "" {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument, "source id must not be empty")
	}

	r.mu.RLock()

	if source, ok := r.sources[key]; ok {
		r.mu.RUnlock()
		return source, nil
	}

	if source, ok := r.instantiated[key]; ok {
		r.mu.RUnlock()
		return source, nil
	}

	failure, failed := r.failures[key]
	r.mu.RUnlock()

	if failed {
		return nil, failure
	}

	return r.build(key)
}

// build runs the factory for key, at most once across all callers.
//
// The double-check inside buildMu is what makes "exactly once" hold: without it
// two goroutines that both missed the fast path would both call the factory and
// the registry would have to pick a winner, leaving one half-built source
// discarded and the loser holding a stale session.
func (r *SourceRegistry) build(key string) (domain.ModSource, error) {
	r.buildMu.Lock()
	defer r.buildMu.Unlock()

	// Another goroutine may have finished while this one waited.
	r.mu.RLock()

	if source, ok := r.sources[key]; ok {
		r.mu.RUnlock()
		return source, nil
	}

	if source, ok := r.instantiated[key]; ok {
		r.mu.RUnlock()
		return source, nil
	}

	r.mu.RUnlock()

	factory, ok := r.factoryFor(key)
	if !ok {
		return nil, appErrors.New(appErrors.ErrCodeSourceNotFound, "unknown mod source %q", key)
	}

	source, err := factory(SourceBuildContext{
		Game:   r.activeGame(),
		Config: r.configFor(key),
	})
	if err != nil {
		// Warn, not error: a misconfigured optional source is a state the user
		// fixes in the settings screen, and the failure is reported to them
		// there and through the error return. Every later lookup re-reports it,
		// so an error line here would repeat itself for the life of the process.
		r.logger().Warn().
			Err(err).
			Str("op", "source-build").
			Str("source", key).
			Msg("mod source could not be configured")

		return nil, r.rememberFailure(key, appErrors.Wrap(err, appErrors.ErrCodeSourceUnavailable,
			"mod source %q could not be configured: %v", key, err))
	}

	if source == nil {
		// A factory returning (nil, nil) is a bug in the plugin. Treat it as a
		// failure rather than handing a nil interface to the caller, which
		// would panic somewhere far away from the actual mistake.
		return nil, r.rememberFailure(key, appErrors.New(appErrors.ErrCodeSourceUnavailable,
			"mod source %q factory returned no source", key))
	}

	if got := normalizeSourceID(source.ID()); got != key {
		// A factory that builds the wrong id would make every later lookup miss
		// and the source unreachable through its own id.
		return nil, r.rememberFailure(key, appErrors.New(appErrors.ErrCodeSourceUnavailable,
			"mod source factory for %q returned a source with id %q", key, got))
	}

	logger := r.logger()

	r.mu.Lock()
	defer r.mu.Unlock()

	r.instantiated[key] = source
	logger.Debug().
		Str("op", "source-build").
		Str("source", key).
		Msg("mod source configured")

	return source, nil
}

// factoryFor resolves a plain source id to a factory: the active game's
// game-scoped registration first, then the generic one. The second result
// reports whether any factory exists at all.
func (r *SourceRegistry) factoryFor(key string) (SourceFactory, bool) {
	if def := r.activeGame(); def != nil {
		if factory, ok := SourceFactoryForGame(def.ID(), key); ok {
			return factory, true
		}
	}

	return SourceFactoryFor(key)
}

// logger returns the attached logger without assuming it was set: a registry
// built by NewSourceRegistry rather than the wiring path has none.
func (r *SourceRegistry) logger() *zerolog.Logger {
	if r == nil || r.log == nil {
		return &nopLogger
	}

	return r.log
}

// rememberFailure caches err against key so every later lookup reports the same
// clear cause, and returns it unchanged for convenience.
func (r *SourceRegistry) rememberFailure(key string, err error) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.failures[key] = err

	return err
}

// List returns every source that can currently be resolved, sorted by id.
//
// Sources whose factory fails are skipped rather than aborting the list: a
// broken Nexus key must not stop the UI from showing Steam and Local. The
// caller that cares about a specific source uses Get and gets the error.
func (r *SourceRegistry) List() []domain.ModSource {
	ids := r.IDs()

	out := make([]domain.ModSource, 0, len(ids))
	for _, id := range ids {
		source, err := r.Get(id)
		if err != nil {
			continue
		}

		out = append(out, source)
	}

	return out
}

// IDs returns every source id the registry knows about — directly registered
// plus every registered factory — sorted and deduplicated.
func (r *SourceRegistry) IDs() []string {
	r.mu.RLock()

	seen := make(map[string]struct{}, len(r.sources)+len(r.instantiated)+len(r.failures))
	for id := range r.sources {
		seen[id] = struct{}{}
	}

	for id := range r.instantiated {
		seen[id] = struct{}{}
	}

	for id := range r.failures {
		seen[id] = struct{}{}
	}

	r.mu.RUnlock()

	for _, id := range RegisteredFactoryIDs() {
		seen[id] = struct{}{}
	}

	// Game-scoped factories are visible under their plain id too: a source that
	// only exists for the active game still has to appear in the list the UI
	// renders.
	if def := r.activeGame(); def != nil {
		for _, id := range RegisteredGameFactoryIDs(def.ID()) {
			seen[id] = struct{}{}
		}
	}

	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}

	sort.Strings(out)

	return out
}

// Infos describes every known source for display. Unresolvable sources are
// still listed, with Enabled false, so the settings screen can show why.
func (r *SourceRegistry) Infos() []domain.ModSourceInfo {
	ids := r.IDs()
	// One snapshot of the configuration for the whole list. Reading it per source
	// would mean a settings file that cannot be loaded is parsed once per entry,
	// and a file edited mid-loop could report half the list from before the edit.
	cfg := r.configSnapshot()

	out := make([]domain.ModSourceInfo, 0, len(ids))
	for _, id := range ids {
		out = append(out, r.infoWith(id, cfg))
	}

	return out
}

// Has reports whether id names a source this registry knows about, whether or not
// it can be built.
//
// It is the non-building counterpart of Get, and it exists because describing a
// list of sources must not build any of them — a source switched off has to stay
// unbuilt — while a caller asking about a single id still needs to be able to tell
// "this source is switched off" from "there is no such source". Without it the
// two are the same empty description, and a frontend holding a stale row shows an
// empty settings page with no complaint.
func (r *SourceRegistry) Has(id string) bool {
	key := normalizeSourceID(id)
	if r == nil || key == "" {
		return false
	}

	r.mu.RLock()
	_, live := r.sources[key]
	_, built := r.instantiated[key]
	_, failed := r.failures[key]
	r.mu.RUnlock()

	if live || built || failed {
		return true
	}

	_, registered := SourceFactoryFor(key)
	if !registered {
		if def := r.activeGame(); def != nil {
			_, registered = SourceFactoryForGame(def.ID(), key)
		}
	}

	return registered
}

// Info describes one source. A source that cannot be built reports the
// Authenticated/Enabled flags as false rather than being omitted.
func (r *SourceRegistry) Info(id string) domain.ModSourceInfo {
	key := normalizeSourceID(id)
	return r.infoWith(key, r.configSnapshot())
}

// enabledByConfig reports whether the configuration leaves a source switched on.
// A missing block counts as enabled: a fresh install has no blocks at all, and a
// manager that started with no sources would look broken.
func enabledByConfig(cfg domain.Config, id string) bool {
	sc, ok := cfg.SourceConfigFor(id)
	return !ok || sc.Enabled
}

// infoWith describes one source against an already-loaded configuration.
//
// Enabled comes from the configuration rather than from whether a source object
// could be produced. A source the user switched off is deliberately never built
// (see the sources registry), so reporting it as enabled would show a green tick
// next to a source the manager will not use.
func (r *SourceRegistry) infoWith(id string, cfg domain.Config) domain.ModSourceInfo {
	key := normalizeSourceID(id)

	info := domain.ModSourceInfo{ID: key}
	if key == "" {
		return info
	}

	info.Enabled = enabledByConfig(cfg, key)
	// Read from the configuration rather than from the source object: "is a key
	// stored" is a question about the settings file, and answering it from memory
	// would report the key of this process only, so the same installation would
	// show a stored key to the CLI and none to the GUI.
	if block, ok := cfg.SourceConfigFor(key); ok {
		info.HasAPIKey = strings.TrimSpace(block.APIKey) != ""
	}

	source, err := r.Get(key)
	if err != nil {
		// The configuration switches a source on, but a source the manager cannot
		// produce is not enabled in any sense that matters — and reporting it as
		// enabled would show a green tick beside a source that cannot be used.
		info.Enabled = false
		return info
	}

	info.Name = source.Name()
	info.Description = source.Description()
	info.Version = source.Version()
	info.SupportsSearch = source.SupportsSearch()
	info.SupportsAuth = source.SupportsAuth()
	info.RequiresAPIKey = source.RequiresAPIKey()
	info.Authenticated = r.Authenticated(key)

	return info
}

// SetAuthenticated records the result of an authentication attempt so the UI
// can show a source as connected without re-running a network call.
func (r *SourceRegistry) SetAuthenticated(id string, authenticated bool) {
	key := normalizeSourceID(id)
	if key == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.authenticated[key] = authenticated
}

// Authenticated reports the last recorded authentication state for a source.
// Sources that were never tested report false.
func (r *SourceRegistry) Authenticated(id string) bool {
	key := normalizeSourceID(id)
	if key == "" {
		return false
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.authenticated[key]
}

// SearchAll fans a query out to every enabled, resolvable source.
//
// A source that fails is skipped and the remaining results are still returned:
// one unreachable launcher must not hide the mods another one has. Only when
// *every* source failed does the call return an error, because then the
// problem is the network or the configuration, not one bad source.
func (r *SourceRegistry) SearchAll(
	ctx context.Context,
	query domain.SearchQuery,
) ([]domain.Mod, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	enabled := make(map[string]bool)
	for _, source := range r.EnabledSources() {
		enabled[source.ID()] = true
	}

	sources := r.List()
	if len(sources) == 0 {
		return nil, appErrors.New(
			appErrors.ErrCodeSourceUnavailable,
			"no mod sources are available",
		)
	}

	searchable := make([]domain.ModSource, 0, len(sources))
	for _, source := range sources {
		// A source the configuration switched off is not asked. That is the whole
		// point of the switch: a user who turned a launcher off because its
		// anonymous quota is spent should not have it queried by a search box, and
		// a source that needs no credential should not send one.
		if !enabled[source.ID()] {
			continue
		}

		if source.SupportsSearch() {
			searchable = append(searchable, source)
		}
	}

	if len(searchable) == 0 {
		return nil, appErrors.New(
			appErrors.ErrCodeSearchFailed,
			"no available mod source supports search",
		)
	}
	// domain.SearchAll owns the concurrency, the partial-failure tolerance and
	// the "every provider failed" rule; the registry only decides which sources
	// take part.
	return domain.SearchAll(ctx, searchable, query)
}

// Authenticate resolves a source and forwards credentials to it, remembering
// the outcome for the settings screen.
func (r *SourceRegistry) Authenticate(
	ctx context.Context,
	id string,
	creds domain.AuthCredentials,
) error {
	if ctx == nil {
		ctx = context.Background()
	}

	source, err := r.Get(id)
	if err != nil {
		return err
	}

	if !source.SupportsAuth() {
		return appErrors.New(
			appErrors.ErrCodeAuthRequired,
			"mod source %q does not support authentication",
			normalizeSourceID(id),
		)
	}

	if err := source.Authenticate(ctx, creds); err != nil {
		r.SetAuthenticated(id, false)
		// The cause is carried by Wrap and rendered by AppError.Error, so it must
		// not also be interpolated into the message: doing so prints it twice,
		// once inside the message and once after the colon.
		return appErrors.Wrap(err, appErrors.ErrCodeAuthFailed,
			"authenticate mod source %q", normalizeSourceID(id))
	}

	r.SetAuthenticated(id, true)

	return nil
}

// ForgetCredentials drops the credential a source is holding, without
// contacting it, and forgets that it was ever authenticated.
//
// Deleting a key is two operations and both are needed. The caller removes it
// from the settings file, which is what stops it coming back on the next launch;
// this is what stops the already-built source from sending it until then.
//
// A source that takes no credential has nothing to forget and says so. So does a
// plugin that supports authentication but cannot drop what it was given: telling
// the user the key is gone when the process is still using it is the one outcome
// worse than not offering the operation at all.
func (r *SourceRegistry) ForgetCredentials(id string) error {
	key := normalizeSourceID(id)
	if key == "" {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "source id must not be empty")
	}

	source, err := r.Get(key)
	if err != nil {
		return err
	}

	if !source.SupportsAuth() {
		return appErrors.New(
			appErrors.ErrCodeAuthRequired,
			"mod source %q does not support authentication",
			key,
		)
	}

	forgetter, ok := source.(domain.CredentialForgetter)
	if !ok {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"mod source %q cannot forget its credentials", key)
	}

	forgetter.ForgetCredentials()
	r.SetAuthenticated(key, false)
	r.logger().Info().
		Str("op", "source-forget").
		Str("source", key).
		Msg("forgot the stored mod source credential")

	return nil
}

// ValidateCredentials checks that a source's stored credentials still work.
func (r *SourceRegistry) ValidateCredentials(ctx context.Context, id string) error {
	if ctx == nil {
		ctx = context.Background()
	}

	source, err := r.Get(id)
	if err != nil {
		return err
	}

	if !source.SupportsAuth() {
		// Nothing to validate is not a failure: the source is usable as-is.
		return nil
	}

	if err := source.ValidateCredentials(ctx); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeAuthFailed,
			"credentials for mod source %q are not valid: %v", normalizeSourceID(id), err)
	}

	return nil
}

// EnabledSources returns only the sources the config block marks as enabled.
// A source with no config block at all counts as enabled, so a fresh install
// shows its bundled sources without having to write a config file first.
func (r *SourceRegistry) EnabledSources() []domain.ModSource {
	cfg := r.configSnapshot()
	out := make([]domain.ModSource, 0)

	for _, source := range r.List() {
		sc, ok := cfg.SourceConfigFor(normalizeSourceID(source.ID()))
		if !ok || sc.Enabled {
			out = append(out, source)
		}
	}

	return out
}

// configFor reads one source's config block, tolerating a missing store or a
// load failure: a source that cannot be configured gets the zero block and the
// plugin decides whether that is usable.
func (r *SourceRegistry) configFor(id string) domain.SourceConfig {
	cfg := r.configSnapshot()
	sc, _ := cfg.SourceConfigFor(id)

	return sc
}

func (r *SourceRegistry) configSnapshot() domain.Config {
	// The supplied configuration wins over the file. The composition root builds
	// one configuration in memory — the file with whatever the caller passed on a
	// command line merged over it — and the registry has to judge "is this source
	// switched on?" against the same values RegisterAll was given. Reading the
	// file instead meant a source the caller had disabled was never built and then
	// reported as enabled, which is the one thing a settings screen must not get
	// wrong.
	if r.config != nil {
		if cfg := r.config(); cfg != nil {
			return *cfg
		}
	}

	if r.store == nil {
		return domain.DefaultConfig()
	}

	cfg, err := r.store.Load()
	if err != nil || cfg == nil {
		return domain.DefaultConfig()
	}

	return *cfg
}

// normalizeSourceID folds an id to its canonical form. Ids are compared
// case-insensitively and trimmed because they come from hand-edited config and
// from user input in the CLI.
func normalizeSourceID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

// sameSource reports whether two registrations are the same object.
//
// Comparing interface values panics when the dynamic type is not comparable,
// and a source implemented as a struct containing a map or a func would be such
// a value. Comparable is checked first so an exotic plugin type is merely
// treated as a distinct source rather than crashing the registry.
func sameSource(a, b domain.ModSource) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}

	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	if va.Type() != vb.Type() || !va.Comparable() {
		return false
	}

	return va.Interface() == vb.Interface()
}
