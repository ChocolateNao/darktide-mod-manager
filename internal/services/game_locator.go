package services

import (
	"context"
	"sort"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/internal/games"
	"github.com/ChocolateNao/darktide-mod-manager/internal/platform"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/rs/zerolog"
)

// GameLocator answers "where is <game>, and where do its mods live?".
//
// It owns no detection logic of its own: the platform package probes the
// filesystem and this service layers the user's mutable configuration on top, so
// registering a game definition is enough to make the locator aware of it.
type GameLocator struct {
	store   ConfigStore
	factory DetectorFactory
	log     *zerolog.Logger
}

// NewGameLocator builds a locator that reads its configuration from store and
// probes the real machine.
func NewGameLocator(store ConfigStore) *GameLocator {
	return NewGameLocatorWithFactory(store, platformDetectorFactory())
}

// NewGameLocatorWithFactory builds a locator with an injected detector factory,
// which tests use so detection does not depend on the machine running them.
func NewGameLocatorWithFactory(store ConfigStore, factory DetectorFactory) *GameLocator {
	if factory == nil {
		factory = func(domain.GameDefinition, domain.GameSettings) InstallationDetector {
			return emptyDetector{}
		}
	}

	return &GameLocator{store: store, factory: factory, log: &nopLogger}
}

// platformDetectorFactory is the production wiring: a fresh platform.Detector
// per call, seeded with the configured path as an extra candidate.
func platformDetectorFactory() DetectorFactory {
	return func(
		game domain.GameDefinition,
		cfg domain.GameSettings,
	) InstallationDetector {
		home, err := platform.HomeDir(domain.CurrentPlatform(), platform.OSEnv())
		if err != nil {
			// A missing home directory is not fatal for detection: the configured path still has
			// to be probed.
			home = ""
		}

		return platform.NewDetector(
			game,
			domain.CurrentPlatform(),
			home,
			platform.OSEnv(),
			[]string{strings.TrimSpace(cfg.Path)},
		)
	}
}

// emptyDetector answers "nothing found" for a nil factory, so a misconfigured
// container fails with an empty result instead of a nil dereference.
type emptyDetector struct{}

func (emptyDetector) FindInstallations(context.Context) ([]domain.GameInstallation, error) {
	return nil, nil
}

func (emptyDetector) ValidateInstallation(string) (*domain.GameInstallation, error) {
	return nil, appErrors.New(appErrors.ErrCodeGameNotFound, "game detection is not available")
}

func (emptyDetector) GetModsPath(*domain.GameInstallation, string) (string, error) {
	return "", appErrors.New(appErrors.ErrCodeGameNotFound, "game detection is not available")
}

// Config returns a copy of the stored configuration. The service never hands out
// its own pointer: callers mutate what they get back, and a shared pointer would
// let a stray edit change the persisted configuration behind the store's back.
func (g *GameLocator) Config() (*domain.Config, error) {
	if g == nil || g.store == nil {
		return nil, appErrors.New(
			appErrors.ErrCodeConfigInvalid,
			"game locator has no configuration store",
		)
	}

	cfg, err := g.store.Load()
	if err != nil {
		return nil, err
	}

	if cfg == nil {
		return nil, appErrors.New(
			appErrors.ErrCodeConfigInvalid,
			"configuration store returned no configuration",
		)
	}

	return cfg.Clone(), nil
}

// activeDef resolves the registered GameDefinition for cfg's active game. An
// empty ActiveGame resolves to the registry default, so a fresh configuration
// lands on a real game instead of failing.
func activeDef(cfg *domain.Config) (domain.GameDefinition, error) {
	id := strings.TrimSpace(cfg.ActiveGame)
	if id == "" {
		id = games.DefaultID()
	}

	if id == "" {
		return nil, appErrors.New(appErrors.ErrCodeGameNotFound, "no games are registered")
	}

	def, ok := games.Get(id)
	if !ok {
		return nil, appErrors.New(appErrors.ErrCodeGameNotFound, "game %q is not registered", id)
	}

	return def, nil
}

// activeSettings is the game settings for a resolved definition. The active
// game's block may live under the unresolved "" key of a fresh configuration, so
// it is read the way ActiveSettings reads it; other games fall back to defaults.
func activeSettings(cfg *domain.Config, def domain.GameDefinition) domain.GameSettings {
	active := strings.TrimSpace(cfg.ActiveGame)
	if active == "" {
		active = games.DefaultID()
	}

	if def.ID() == active {
		return cfg.ActiveSettings()
	}

	if settings, ok := cfg.GameSettingsFor(def.ID()); ok {
		return settings
	}

	return domain.DefaultGameSettings()
}

// Active resolves the game the manager currently operates on: its definition
// from the registry plus its stored settings. Persisting that resolved id is the
// composition root's job (it rewrites the configuration on load), so reading
// never writes.
func (g *GameLocator) Active() (domain.GameDefinition, domain.GameSettings, error) {
	cfg, err := g.Config()
	if err != nil {
		return nil, domain.GameSettings{}, err
	}

	def, err := activeDef(cfg)
	if err != nil {
		return nil, domain.GameSettings{}, err
	}

	return def, activeSettings(cfg, def), nil
}

// SetActive switches the manager to another registered game. The id is
// validated against the registry first: persisting a game nothing can build a
// detector for would strand every later lookup.
func (g *GameLocator) SetActive(id string) error {
	if g == nil || g.store == nil {
		return appErrors.New(
			appErrors.ErrCodeConfigInvalid,
			"game locator has no configuration store",
		)
	}

	def, ok := games.Get(id)
	if !ok {
		return appErrors.New(appErrors.ErrCodeGameNotFound, "game %q is not registered", id)
	}

	cfg, err := g.store.Load()
	if err != nil {
		return err
	}

	if cfg == nil {
		return appErrors.New(
			appErrors.ErrCodeConfigInvalid,
			"configuration store returned no configuration",
		)
	}

	updated := cfg.Clone()
	updated.ActiveGame = def.ID()

	if _, err := g.store.SaveIfChanged(updated); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "save active game %q", id)
	}

	g.logger().Info().
		Str("op", "game-active").
		Str("game", def.ID()).
		Msg("switched the active game")

	return nil
}

// detector builds a detector for the active game's configuration.
func (g *GameLocator) detector() (
	InstallationDetector,
	domain.GameDefinition,
	domain.GameSettings,
	error,
) {
	def, settings, err := g.Active()
	if err != nil {
		return nil, nil, domain.GameSettings{}, err
	}

	return g.factory(def, settings), def, settings, nil
}

// FindInstallations returns every selected game installation the locator knows about.
//
// The configured path always wins a collision: it carries the source and mods
// folder the user chose, and a launcher probe of the same directory would
// overwrite it with a guess. An invalid configured path is omitted rather than
// fatal, so the UI can still show the installs detection did find; Current
// reports the problem instead.
//
// Detection is skipped when the configuration disables auto-detect.
func (g *GameLocator) FindInstallations(ctx context.Context) ([]domain.GameInstallation, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if err := ctx.Err(); err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeOperationCanceled,
			"game detection canceled",
		)
	}

	det, def, game, err := g.detector()
	if err != nil {
		return nil, err
	}

	return installsFor(ctx, det, def, game)
}

// Installations returns the installations of every registered game, each stamped
// with the game it belongs to. Unlike FindInstallations this is not scoped to the
// active game: the UI needs one scan to show what is installed where. One game's
// probe failing does not abandon the others; a cancelled context stops the scan.
func (g *GameLocator) Installations(ctx context.Context) ([]domain.GameInstallation, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if err := ctx.Err(); err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeOperationCanceled,
			"game detection canceled",
		)
	}

	cfg, err := g.Config()
	if err != nil {
		return nil, err
	}

	var out []domain.GameInstallation

	for _, def := range games.All() {
		if err := ctx.Err(); err != nil {
			return nil, appErrors.Wrap(
				err,
				appErrors.ErrCodeOperationCanceled,
				"game detection canceled",
			)
		}

		settings := activeSettings(cfg, def)

		installs, err := installsFor(ctx, g.factory(def, settings), def, settings)
		if err != nil {
			// installsFor only fails on a cancelled context, which stops the
			// whole scan rather than skipping the game.
			return nil, err
		}

		out = append(out, installs...)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].GameID != out[j].GameID {
			return out[i].GameID < out[j].GameID
		}

		return strings.ToLower(out[i].Path) < strings.ToLower(out[j].Path)
	})

	if out == nil {
		out = []domain.GameInstallation{}
	}

	return out, nil
}

// installsFor runs the configured-first probe for one game: the configured
// path is probed first and always wins a collision, the auto-detect sweep
// follows when enabled, and every result is stamped with the game id.
func installsFor(
	ctx context.Context,
	det InstallationDetector,
	def domain.GameDefinition,
	game domain.GameSettings,
) ([]domain.GameInstallation, error) {
	// Ordered map: the configured entry is inserted first and later duplicates
	// are skipped, which is what gives it precedence without a second pass.
	ordered := make([]domain.GameInstallation, 0, 4)
	seen := make(map[string]bool, 4)
	add := func(install domain.GameInstallation) {
		// Trim before cleaning: CleanPath only folds separators, so a path that
		// is nothing but whitespace would survive as a distinct "installation".
		key := strings.ToLower(domain.CleanPath(strings.TrimSpace(install.Path)))
		if key == "" || key == "." || seen[key] {
			return
		}

		seen[key] = true
		install.GameID = def.ID()

		ordered = append(ordered, install)
	}

	if configured := strings.TrimSpace(game.Path); configured != "" {
		if install, err := det.ValidateInstallation(configured); err == nil && install != nil {
			add(withSourceHint(install, game.Source))
		}
	}

	if game.AutoDetect {
		found, err := det.FindInstallations(ctx)
		if err != nil {
			// A partially successful sweep is still useful, so keep what the
			// probe returned; a cancelled context is the exception.
			if ctx.Err() != nil {
				return nil, appErrors.Wrap(
					ctx.Err(),
					appErrors.ErrCodeOperationCanceled,
					"game detection canceled",
				)
			}
		}

		for _, install := range found {
			if err := ctx.Err(); err != nil {
				return nil, appErrors.Wrap(
					err,
					appErrors.ErrCodeOperationCanceled,
					"game detection canceled",
				)
			}

			add(withSourceHint(&install, game.Source))
		}
	}

	// The probe's "most likely source first" ordering holds only within the
	// probe, so the merged list is sorted by path to keep rendering reproducible.
	sort.SliceStable(ordered, func(i, j int) bool {
		return strings.ToLower(ordered[i].Path) < strings.ToLower(ordered[j].Path)
	})

	return ordered, nil
}

// withSourceHint fills in the launcher the user told us about, but only when the
// probe had nothing better to say: a directory found by the Steam sweep keeps its
// "steam" source, while a path the user typed in becomes the launcher they named.
// An empty or unrecognised hint becomes "manual", because display and update
// logic switch on this value and an unknown case there is a silent no-op.
func withSourceHint(
	install *domain.GameInstallation,
	configured domain.GameSource,
) domain.GameInstallation {
	out := *install

	hint := domain.GameSourceManual
	if parsed, ok := domain.ParseGameSource(strings.TrimSpace(string(configured))); ok {
		hint = parsed
	}

	if out.Source == "" || out.Source == domain.GameSourceManual {
		out.Source = hint
	}

	return out
}

// ValidateInstallation checks a path the user typed in. It goes through the
// same detector as FindInstallations so the two agree on what counts as a game
// installation.
func (g *GameLocator) ValidateInstallation(path string) (*domain.GameInstallation, error) {
	if strings.TrimSpace(path) == "" {
		return nil, appErrors.New(appErrors.ErrCodeGameInvalid, "game path is empty")
	}

	det, def, game, err := g.detector()
	if err != nil {
		return nil, err
	}

	install, err := det.ValidateInstallation(path)
	if err != nil {
		return nil, err
	}

	if install == nil {
		// A detector that returns (nil, nil) would otherwise sail through as a
		// nil installation the caller dereferences later.
		return nil, appErrors.New(
			appErrors.ErrCodeGameNotFound,
			"no %s installation at %q",
			def.DisplayName(),
			path,
		)
	}

	hinted := withSourceHint(install, game.Source)
	if hinted.GameID == "" {
		hinted.GameID = def.ID()
	}

	return &hinted, nil
}

// Current returns the installation the manager should operate on: the configured
// one when set, otherwise the first one detection found. The configured path wins
// explicitly rather than by sorting, because FindInstallations sorts by path for
// stable rendering. It returns ErrGameNotFound rather than an empty slice so
// callers can render the "point me at your game" prompt without inspecting the
// error text.
func (g *GameLocator) Current(ctx context.Context) (*domain.GameInstallation, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if err := ctx.Err(); err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeOperationCanceled,
			"game detection canceled",
		)
	}

	_, def, game, err := g.detector()
	if err != nil {
		return nil, err
	}

	if configured := strings.TrimSpace(game.Path); configured != "" {
		// A stale configured path is reported rather than swapped for another
		// installation: the user asked for this one.
		return g.ValidateInstallation(configured)
	}

	installs, err := g.FindInstallations(ctx)
	if err != nil {
		return nil, err
	}

	if len(installs) == 0 {
		g.logger().Warn().
			Str("op", "game-detect").
			Str("game", def.ID()).
			Str("reason", "no installation found").
			Msg("no game installation was detected")

		return nil, appErrors.New(appErrors.ErrCodeGameNotFound,
			"no %s installation found; set the game path in the settings",
			def.DisplayName())
	}

	install := installs[0]
	g.logger().Debug().
		Str("op", "game-detect").
		Str("game", def.ID()).
		Str("path", install.Path).
		Str("source", string(install.Source)).
		Int("candidates", len(installs)).
		Msg("using detected game installation")

	return &install, nil
}

// GetModsPath resolves the mods folder for an installation, applying the
// configured override.
func (g *GameLocator) GetModsPath(install *domain.GameInstallation) (string, error) {
	if install == nil {
		return "", appErrors.New(appErrors.ErrCodeGameInvalid, "no game installation given")
	}

	_, def, game, err := g.detector()
	if err != nil {
		return "", err
	}

	det := g.factory(def, game)

	return det.GetModsPath(install, strings.TrimSpace(game.ModsFolderOverride))
}

// SetGamePath validates a path and persists it as the configured installation.
// Validation happens before any write, because a configuration pointing at a
// non-game directory makes every later failure report the user's mistake.
func (g *GameLocator) SetGamePath(ctx context.Context, path string) error {
	if g == nil || g.store == nil {
		return appErrors.New(
			appErrors.ErrCodeConfigInvalid,
			"game locator has no configuration store",
		)
	}

	if ctx == nil {
		ctx = context.Background()
	}

	if err := ctx.Err(); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeOperationCanceled, "set game path canceled")
	}

	install, err := g.ValidateInstallation(path)
	if err != nil {
		return err
	}

	cfg, err := g.store.Load()
	if err != nil {
		return err
	}

	if cfg == nil {
		return appErrors.New(
			appErrors.ErrCodeConfigInvalid,
			"configuration store returned no configuration",
		)
	}

	updated := cfg.Clone()
	updated.UpdateActiveSettings(func(settings *domain.GameSettings) {
		settings.Path = install.Path
	})

	if _, err := g.store.SaveIfChanged(updated); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "save game path %q", path)
	}

	g.logger().Info().
		Str("op", "game-path").
		Str("path", install.Path).
		Msg("configured the Darktide installation")

	return nil
}

// WithLogger attaches a logger for the detection trace: detection walks the
// filesystem, so the paths it considered are worth keeping.
func (g *GameLocator) WithLogger(logger *zerolog.Logger) *GameLocator {
	if g == nil {
		return g
	}

	g.log = loggerOf(logger)

	return g
}

// logger returns the attached logger, tolerating a locator built by a test that
// never attached one.
func (g *GameLocator) logger() *zerolog.Logger {
	if g == nil || g.log == nil {
		return &nopLogger
	}

	return g.log
}
