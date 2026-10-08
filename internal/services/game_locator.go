package services

import (
	"context"
	"sort"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/internal/platform"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/rs/zerolog"
)

// GameLocator answers "where is Darktide, and where do its mods live?".
//
// It owns no detection logic of its own: the platform package probes the
// filesystem, and this service layers the user's configuration on top of that
// probe. Keeping the two apart matters because the configuration is mutable at
// run time while the probe is a pure function of the machine's layout.
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

// NewGameLocatorWithFactory builds a locator with an injected detector
// factory. Tests pass a factory returning a fake so detection results do not
// depend on the machine running them.
func NewGameLocatorWithFactory(store ConfigStore, factory DetectorFactory) *GameLocator {
	if factory == nil {
		factory = func(domain.GameConfig) InstallationDetector { return emptyDetector{} }
	}

	return &GameLocator{store: store, factory: factory, log: &nopLogger}
}

// platformDetectorFactory is the production wiring: a fresh platform.Detector
// per call, seeded with the configured path as an extra candidate.
func platformDetectorFactory() DetectorFactory {
	return func(cfg domain.GameConfig) InstallationDetector {
		home, err := platform.HomeDir(domain.CurrentPlatform(), platform.OSEnv())
		if err != nil {
			// A missing home directory is not fatal for detection: the
			// configured path still has to be probed, and the platform defaults
			// cover the common locations when they are reachable.
			home = ""
		}

		return platform.NewDetector(
			domain.CurrentPlatform(),
			home,
			platform.OSEnv(),
			[]string{strings.TrimSpace(cfg.Path)},
		)
	}
}

// emptyDetector answers "nothing found" for the degenerate case of a nil
// factory, so a misconfigured container fails with an empty result instead of a
// nil dereference.
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

// Config returns a copy of the stored configuration. The service never hands
// out its own pointer: callers (notably the UI) mutate what they get back, and a
// shared pointer would let a stray edit change the persisted configuration
// behind the store's back.
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

// detector builds a detector for the current configuration.
func (g *GameLocator) detector() (InstallationDetector, domain.GameConfig, error) {
	cfg, err := g.Config()
	if err != nil {
		return nil, domain.GameConfig{}, err
	}

	return g.factory(cfg.Game), cfg.Game, nil
}

// FindInstallations returns every Darktide installation the locator knows about.
//
// The configured path is probed first and always wins a collision: if the user
// pointed the manager at a path, that entry carries the source and mods folder
// the user chose, and a launcher probe of the same directory would overwrite it
// with a guess. An invalid configured path is *omitted* rather than fatal, so
// the UI can still show the installs detection did find; Current reports the
// problem instead.
//
// Detection is skipped entirely when the configuration disables auto-detect,
// because a user who turned it off is asking not to have their disk searched.
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

	det, game, err := g.detector()
	if err != nil {
		return nil, err
	}

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
			// A partially successful sweep is still useful: keep what the probe
			// managed to return alongside the configured entry. A cancelled
			// context is the exception — the caller stopped us on purpose.
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

	// The probe's "most likely source first" ordering is meaningful only within
	// the probe. Across the configured entry and the sweep it is not stable, so
	// the merged list is sorted by path to keep UI rendering reproducible.
	sort.SliceStable(ordered, func(i, j int) bool {
		return strings.ToLower(ordered[i].Path) < strings.ToLower(ordered[j].Path)
	})

	return ordered, nil
}

// preferredSource maps a configured launcher name onto a GameSource, falling
// back to "manual" for anything unrecognised. An empty or unrecognised value
// must not leak into the installation: source feeds display and update logic
// that switch on it, and an empty case there is a silent no-op.
func preferredSource(s string) domain.GameSource {
	if parsed, ok := domain.ParseGameSource(s); ok {
		return parsed
	}

	return domain.GameSourceManual
}

// withSourceHint fills in the launcher the user told us about, but only when the
// probe had nothing better to say. A directory found by the Steam sweep keeps
// its "steam" source; a path the user typed in becomes the launcher they named,
// because the probe cannot know and the UI switches on this value.
func withSourceHint(install *domain.GameInstallation, configured string) domain.GameInstallation {
	out := *install
	if out.Source == "" || out.Source == domain.GameSourceManual {
		out.Source = preferredSource(configured)
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

	det, game, err := g.detector()
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
			"no Darktide installation at %q",
			path,
		)
	}

	hinted := withSourceHint(install, game.Source)

	return &hinted, nil
}

// Current returns the installation the manager should operate on: the
// configured one when set, otherwise the first one detection found. The
// configured path wins explicitly rather than by sorting — FindInstallations
// sorts by path for stable rendering, and the user's own choice has no reason
// to sort first.
//
// It returns ErrGameNotFound rather than an empty slice so callers can render
// the "point me at your game" prompt without inspecting the error text.
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

	_, game, err := g.detector()
	if err != nil {
		return nil, err
	}

	if configured := strings.TrimSpace(game.Path); configured != "" {
		// A configured path that no longer validates is reported here instead of
		// being quietly replaced by some other installation: the user asked for
		// this one.
		return g.ValidateInstallation(configured)
	}

	installs, err := g.FindInstallations(ctx)
	if err != nil {
		return nil, err
	}

	if len(installs) == 0 {
		g.logger().Warn().
			Str("op", "game-detect").
			Str("reason", "no installation found").
			Msg("no Darktide installation was detected")

		return nil, appErrors.New(appErrors.ErrCodeGameNotFound,
			"no Darktide installation found; set the game path in the settings")
	}

	install := installs[0]
	g.logger().Debug().
		Str("op", "game-detect").
		Str("path", install.Path).
		Str("source", string(install.Source)).
		Int("candidates", len(installs)).
		Msg("using detected Darktide installation")

	return &install, nil
}

// GetModsPath resolves the mods folder for an installation, applying the
// configured override. It is the only supported way for the rest of the program
// to learn where mod files go.
func (g *GameLocator) GetModsPath(install *domain.GameInstallation) (string, error) {
	if install == nil {
		return "", appErrors.New(appErrors.ErrCodeGameInvalid, "no game installation given")
	}

	_, game, err := g.detector()
	if err != nil {
		return "", err
	}

	det := g.factory(game)

	return det.GetModsPath(install, strings.TrimSpace(game.ModsFolderOverride))
}

// SetGamePath validates a path and persists it as the configured installation.
//
// The path is validated *before* anything is written: a configuration that
// points at a directory which is not a game is worse than no configuration at
// all, because every later failure reports it as the user's mistake.
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
	updated.Game.Path = install.Path

	if _, err := g.store.SaveIfChanged(updated); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "save game path %q", path)
	}

	g.logger().Info().
		Str("op", "game-path").
		Str("path", install.Path).
		Msg("configured the Darktide installation")

	return nil
}

// WithLogger attaches a logger for the detection trace. Detection walks the
// filesystem, so the paths it considered are worth keeping when it comes back
// empty.
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
