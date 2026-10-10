package services

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/internal/games"
	"github.com/ChocolateNao/darktide-mod-manager/internal/games/gamestest"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// fakeStore is an in-memory ConfigStore. Load hands out a clone for the same
// reason the real store does: the caller owns what it receives, so a test can
// tell whether the service under test kept a pointer to the stored config and
// mutated it behind the store's back.
type fakeStore struct {
	loadErr error
	saveErr error
	cfg     *domain.Config
	saved   *domain.Config
	path    string
	saves   int
	changed bool
}

func newFakeStore(cfg *domain.Config) *fakeStore {
	if cfg == nil {
		defaults := domain.DefaultConfig()
		cfg = &defaults
	}

	return &fakeStore{path: "/tmp/config.json", cfg: cfg}
}

func (s *fakeStore) Path() string { return s.path }

func (s *fakeStore) Load() (*domain.Config, error) {
	if s.loadErr != nil {
		return nil, s.loadErr
	}

	return s.cfg.Clone(), nil
}

func (s *fakeStore) Save(cfg *domain.Config) error {
	s.saves++

	s.saved = cfg.Clone()
	if s.saveErr != nil {
		return s.saveErr
	}

	s.cfg = cfg.Clone()

	return nil
}

func (s *fakeStore) SaveIfChanged(cfg *domain.Config) (bool, error) {
	if s.saveErr != nil {
		return false, s.saveErr
	}

	s.saves++
	s.saved = cfg.Clone()
	s.cfg = cfg.Clone()
	s.changed = true

	return true, nil
}

// fakeDetector answers from canned function fields. A nil field answers with
// the zero value, which keeps individual tests from having to spell out the
// calls they do not care about.
type fakeDetector struct {
	find      func(context.Context) ([]domain.GameInstallation, error)
	validate  func(string) (*domain.GameInstallation, error)
	modsPath  func(*domain.GameInstallation, string) (string, error)
	seenPaths []string
	seenOver  []string
	seenCfg   []domain.GameSettings
	findCalls int
	mu        sync.Mutex
}

func (d *fakeDetector) FindInstallations(ctx context.Context) ([]domain.GameInstallation, error) {
	d.mu.Lock()
	d.findCalls++
	fn := d.find
	d.mu.Unlock()

	if fn == nil {
		return nil, nil
	}

	return fn(ctx)
}

func (d *fakeDetector) ValidateInstallation(path string) (*domain.GameInstallation, error) {
	d.mu.Lock()
	d.seenPaths = append(d.seenPaths, path)
	fn := d.validate
	d.mu.Unlock()

	if fn == nil {
		return nil, appErrors.New(appErrors.ErrCodeGameNotFound, "not found")
	}

	return fn(path)
}

func (d *fakeDetector) GetModsPath(
	install *domain.GameInstallation,
	override string,
) (string, error) {
	d.mu.Lock()
	d.seenOver = append(d.seenOver, override)
	fn := d.modsPath
	d.mu.Unlock()

	if fn == nil {
		return install.ModsPath, nil
	}

	return fn(install, override)
}

func (d *fakeDetector) calls() int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.findCalls
}

func TestMain(m *testing.M) {
	// Every locator lookup resolves the active game through the registry, so
	// the built-ins have to be registered before any test runs.
	games.Reset()
	games.EnsureBuiltins()

	os.Exit(m.Run())
}

// locatorWith builds a locator whose factory always returns det, and records the
// configuration each detector was built from.
func locatorWith(
	store ConfigStore,
	det InstallationDetector,
) (*GameLocator, *[]domain.GameSettings) {
	var seen []domain.GameSettings

	factory := func(_ domain.GameDefinition, cfg domain.GameSettings) InstallationDetector {
		seen = append(seen, cfg)
		return det
	}

	return NewGameLocatorWithFactory(store, factory), &seen
}

func install(path string) domain.GameInstallation {
	return domain.GameInstallation{Path: path, Source: domain.GameSourceManual, Valid: true}
}

func mustLocator(t *testing.T, store ConfigStore, det InstallationDetector) *GameLocator {
	t.Helper()

	if store == nil {
		store = newFakeStore(nil)
	}

	loc, _ := locatorWith(store, det)

	return loc
}

// ---------------------------------------------------------------------------
// Construction and configuration
// ---------------------------------------------------------------------------

func TestGameLocatorNilFactoryFindsNothing(t *testing.T) {
	loc := NewGameLocatorWithFactory(newFakeStore(nil), nil)

	installs, err := loc.FindInstallations(context.Background())
	require.NoError(t, err, "nil factory should not error, got %v")

	assert.Empty(t, installs, "nil factory found %d installations, want 0", len(installs))

	assert.NotNil(t, installs, "expected an empty slice, got nil")
}

func TestGameLocatorWithoutStoreFails(t *testing.T) {
	loc := NewGameLocatorWithFactory(nil, nil)
	{
		_, err := loc.Config()
		assert.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeConfigInvalid),
			"Config without a store: got %v, want config-invalid",
			err,
		)
	}

	{
		err := loc.SetGamePath(
			context.Background(),
			"/games/darktide",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeConfigInvalid,
		), "SetGamePath without a store: got %v, want config-invalid", err)
	}
}

func TestGameLocatorStoreLoadFailurePropagates(t *testing.T) {
	store := newFakeStore(nil)
	store.loadErr = appErrors.New(appErrors.ErrCodeConfigIO, "disk on fire")
	loc := mustLocator(t, store, &fakeDetector{})

	{
		_, err := loc.FindInstallations(
			context.Background(),
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeConfigIO,
		), "got %v, want config-io", err)
	}
}

func TestGameLocatorConfigHandsOutACopy(t *testing.T) {
	store := newFakeStore(nil)
	loc := mustLocator(t, store, &fakeDetector{})

	cfg, err := loc.Config()
	require.NoError(t, err, "Config")

	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.Path = "/mutated" })

	again, err := loc.Config()
	require.NoError(t, err, "Config (second): %v", err)

	assert.NotEqual(
		t,
		"/mutated",
		again.ActiveSettings().Path,
		"mutating the returned config changed the stored one",
	)
}

// ---------------------------------------------------------------------------
// FindInstallations
// ---------------------------------------------------------------------------

func TestGameLocatorFindsNothingWithoutAutoDetect(t *testing.T) {
	det := &fakeDetector{}
	cfg := domain.DefaultConfig()
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.AutoDetect = false })
	loc := mustLocator(t, newFakeStore(&cfg), det)

	installs, err := loc.FindInstallations(context.Background())
	require.NoError(t, err, "FindInstallations")

	assert.Empty(t, installs, "got %d installations, want 0", len(installs))

	assert.Equal(t, 0, det.calls(), "auto-detect is off, but the probe ran %d time(s)", det.calls())
}

func TestGameLocatorAutoDetectRunsTheProbe(t *testing.T) {
	cfg := domain.DefaultConfig()
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.AutoDetect = true })

	det := &fakeDetector{find: func(context.Context) ([]domain.GameInstallation, error) {
		return []domain.GameInstallation{install("/games/darktide")}, nil
	}}
	loc := mustLocator(t, newFakeStore(&cfg), det)

	installs, err := loc.FindInstallations(context.Background())
	require.NoError(t, err, "FindInstallations")

	require.False(
		t,
		len(installs) != 1 || installs[0].Path != "/games/darktide",
		"unexpected installations: %+v",
		installs,
	)

	assert.Equal(t, 1, det.calls(), "probe ran %d time(s), want 1", det.calls())
}

func TestGameLocatorResultIsSortedByPath(t *testing.T) {
	cfg := domain.DefaultConfig()
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.AutoDetect = true })

	det := &fakeDetector{find: func(context.Context) ([]domain.GameInstallation, error) {
		return []domain.GameInstallation{
			install("/games/zulu"),
			install("/games/alpha"),
			install("/games/mike"),
		}, nil
	}}
	loc := mustLocator(t, newFakeStore(&cfg), det)

	installs, err := loc.FindInstallations(context.Background())
	require.NoError(t, err, "FindInstallations")

	want := []string{"/games/alpha", "/games/mike", "/games/zulu"}
	for i, path := range want {
		require.Equal(
			t,
			path,
			installs[i].Path,
			"installations[%d] = %q, want %q (full: %+v)",
			i,
			installs[i].Path,
			path,
			installs,
		)
	}
}

func TestGameLocatorDeduplicatesCaseInsensitively(t *testing.T) {
	cfg := domain.DefaultConfig()
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.AutoDetect = true })

	det := &fakeDetector{find: func(context.Context) ([]domain.GameInstallation, error) {
		return []domain.GameInstallation{
			install("/Games/Darktide"),
			install("/games/darktide"),
			install("/games/darktide/"),
		}, nil
	}}
	loc := mustLocator(t, newFakeStore(&cfg), det)

	installs, err := loc.FindInstallations(context.Background())
	require.NoError(t, err, "FindInstallations")

	require.Len(
		t,
		installs,
		1,
		"got %d installations, want 1 (duplicates differ only by case/trailing slash): %+v",
		len(installs),
		installs,
	)
}

func TestGameLocatorDropsEntriesWithoutAPath(t *testing.T) {
	cfg := domain.DefaultConfig()
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.AutoDetect = true })

	det := &fakeDetector{find: func(context.Context) ([]domain.GameInstallation, error) {
		return []domain.GameInstallation{
			install("   "),
			install("/games/darktide"),
		}, nil
	}}
	loc := mustLocator(t, newFakeStore(&cfg), det)

	installs, err := loc.FindInstallations(context.Background())
	require.NoError(t, err, "FindInstallations")

	require.False(
		t,
		len(installs) != 1 || installs[0].Path != "/games/darktide",
		"blank path should be dropped, got %+v",
		installs,
	)
}

func TestGameLocatorConfiguredPathWinsOverTheProbe(t *testing.T) {
	cfg := domain.DefaultConfig()
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.AutoDetect = true })
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.Path = "/games/darktide" })
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.Source = "steam" })

	configured := install("/games/darktide")
	configured.Version = "1.0.5000"
	configured.ModsPath = "/games/darktide/mods"
	det := &fakeDetector{
		validate: func(string) (*domain.GameInstallation, error) {
			out := configured
			return &out, nil
		},
		find: func(context.Context) ([]domain.GameInstallation, error) {
			// Same directory, different spelling and a guessed source. Neither
			// may overwrite what the user's own path resolved to.
			probed := install("/GAMES/Darktide/")
			probed.Source = domain.GameSourceEpic
			probed.Version = "0.0.0"

			return []domain.GameInstallation{probed}, nil
		},
	}
	loc := mustLocator(t, newFakeStore(&cfg), det)

	installs, err := loc.FindInstallations(context.Background())
	require.NoError(t, err, "FindInstallations")

	require.Len(t, installs, 1, "got %d installations, want 1: %+v", len(installs), installs)

	assert.Equal(
		t,
		"1.0.5000",
		installs[0].Version,
		"probe overwrote the configured version: %q",
		installs[0].Version,
	)

	assert.Equal(
		t,
		domain.GameSourceSteam,
		installs[0].Source,
		"source = %q, want steam",
		installs[0].Source,
	)
}

func TestGameLocatorInvalidConfiguredPathIsOmittedNotFatal(t *testing.T) {
	cfg := domain.DefaultConfig()
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.AutoDetect = true })
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.Path = "/games/gone" })

	det := &fakeDetector{
		validate: func(string) (*domain.GameInstallation, error) {
			return nil, appErrors.New(appErrors.ErrCodeGameNotFound, "gone")
		},
		find: func(context.Context) ([]domain.GameInstallation, error) {
			return []domain.GameInstallation{install("/games/darktide")}, nil
		},
	}
	loc := mustLocator(t, newFakeStore(&cfg), det)

	installs, err := loc.FindInstallations(context.Background())
	require.NoError(t, err, "a bad configured path must not fail the whole sweep")

	require.False(
		t,
		len(installs) != 1 || installs[0].Path != "/games/darktide",
		"unexpected installations: %+v",
		installs,
	)
}

func TestGameLocatorKeepsPartialResultsFromAFailedProbe(t *testing.T) {
	cfg := domain.DefaultConfig()
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.AutoDetect = true })

	det := &fakeDetector{find: func(context.Context) ([]domain.GameInstallation, error) {
		return []domain.GameInstallation{install("/games/darktide")},
			appErrors.New(appErrors.ErrCodeGameNotFound, "half the disk was unreadable")
	}}
	loc := mustLocator(t, newFakeStore(&cfg), det)

	installs, err := loc.FindInstallations(context.Background())
	require.NoError(t, err, "FindInstallations")

	require.Len(t, installs, 1, "partial results were dropped: %+v", installs)
}

func TestGameLocatorProbeFailureWithCanceledContextIsFatal(t *testing.T) {
	cfg := domain.DefaultConfig()
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.AutoDetect = true })

	ctx, cancel := context.WithCancel(context.Background())
	det := &fakeDetector{find: func(context.Context) ([]domain.GameInstallation, error) {
		cancel()
		return nil, appErrors.New(appErrors.ErrCodeGameNotFound, "stopped")
	}}
	loc := mustLocator(t, newFakeStore(&cfg), det)

	installs, err := loc.FindInstallations(ctx)
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeOperationCanceled),
		"got %v, want operation-canceled",
		err,
	)

	assert.Nil(t, installs, "a canceled sweep must not return installations: %+v", installs)
}

func TestGameLocatorStopsWhenTheContextIsCanceledMidSweep(t *testing.T) {
	cfg := domain.DefaultConfig()
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.AutoDetect = true })

	ctx, cancel := context.WithCancel(context.Background())
	det := &fakeDetector{find: func(context.Context) ([]domain.GameInstallation, error) {
		return []domain.GameInstallation{
			install("/games/alpha"),
			func() domain.GameInstallation {
				cancel()
				return install("/games/bravo")
			}(),
		}, nil
	}}
	loc := mustLocator(t, newFakeStore(&cfg), det)

	{
		_, err := loc.FindInstallations(
			ctx,
		)
		require.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeOperationCanceled,
		), "got %v, want operation-canceled", err)
	}
}

func TestGameLocatorAlreadyCanceledContextSkipsEverything(t *testing.T) {
	cfg := domain.DefaultConfig()
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.AutoDetect = true })
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.Path = "/games/darktide" })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	det := &fakeDetector{validate: func(string) (*domain.GameInstallation, error) {
		out := install("/games/darktide")
		return &out, nil
	}}
	loc := mustLocator(t, newFakeStore(&cfg), det)

	{
		_, err := loc.FindInstallations(
			ctx,
		)
		require.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeOperationCanceled,
		), "got %v, want operation-canceled", err)
	}

	assert.Empty(t, det.seenPaths, "a canceled context still probed %v", det.seenPaths)
}

func TestGameLocatorNilContextIsTreatedAsBackground(t *testing.T) {
	//nolint:staticcheck // exercising the nil-context guard on purpose
	installs, err := mustLocator(t, newFakeStore(nil), &fakeDetector{}).FindInstallations(nil)
	require.NoError(t, err, "nil context")

	assert.Len(t, installs, 0, "got %+v, want none", installs)
}

// ---------------------------------------------------------------------------
// Source hints
// ---------------------------------------------------------------------------

func TestGameLocatorAppliesTheConfiguredSourceHint(t *testing.T) {
	cfg := domain.DefaultConfig()
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.AutoDetect = true })
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.Path = "/games/darktide" })
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.Source = "  Steam  " })

	det := &fakeDetector{validate: func(string) (*domain.GameInstallation, error) {
		out := install("/games/darktide")
		return &out, nil
	}}
	loc := mustLocator(t, newFakeStore(&cfg), det)

	installs, err := loc.FindInstallations(context.Background())
	require.NoError(t, err, "FindInstallations")

	assert.Equal(
		t,
		domain.GameSourceSteam,
		installs[0].Source,
		"source = %q, want steam (the configured hint, trimmed)",
		installs[0].Source,
	)
}

func TestGameLocatorUnknownSourceHintFallsBackToManual(t *testing.T) {
	cfg := domain.DefaultConfig()
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.AutoDetect = true })
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.Path = "/games/darktide" })
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.Source = "not-a-launcher" })

	det := &fakeDetector{validate: func(string) (*domain.GameInstallation, error) {
		out := install("/games/darktide")
		return &out, nil
	}}
	loc := mustLocator(t, newFakeStore(&cfg), det)

	installs, err := loc.FindInstallations(context.Background())
	require.NoError(t, err, "FindInstallations")

	assert.Equal(
		t,
		domain.GameSourceManual,
		installs[0].Source,
		"source = %q, want manual",
		installs[0].Source,
	)
}

func TestGameLocatorKeepsASourceTheProbeAlreadyKnew(t *testing.T) {
	cfg := domain.DefaultConfig()
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.AutoDetect = true })
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.Source = "steam" })

	det := &fakeDetector{find: func(context.Context) ([]domain.GameInstallation, error) {
		found := install("/games/darktide")
		found.Source = domain.GameSourceLutris

		return []domain.GameInstallation{found}, nil
	}}
	loc := mustLocator(t, newFakeStore(&cfg), det)

	installs, err := loc.FindInstallations(context.Background())
	require.NoError(t, err, "FindInstallations")

	assert.Equal(
		t,
		domain.GameSourceLutris,
		installs[0].Source,
		"source = %q, want lutris; the hint must not overwrite a probe result",
		installs[0].Source,
	)
}

// ---------------------------------------------------------------------------
// ValidateInstallation / Current
// ---------------------------------------------------------------------------

func TestGameLocatorValidateRejectsABlankPath(t *testing.T) {
	det := &fakeDetector{}
	loc := mustLocator(t, newFakeStore(nil), det)

	for _, path := range []string{"", "   ", "\t\n"} {
		{
			_, err := loc.ValidateInstallation(
				path,
			)
			assert.True(t, appErrors.IsAppError(
				err,
				appErrors.ErrCodeGameInvalid,
			), "ValidateInstallation(%q): got %v, want game-invalid", path, err)
		}
	}

	assert.Empty(t, det.seenPaths, "a blank path was still probed: %v", det.seenPaths)
}

func TestGameLocatorValidateRejectsANilInstallation(t *testing.T) {
	// A detector returning (nil, nil) must not sail through as a nil
	// installation the caller dereferences later.
	det := &fakeDetector{validate: func(string) (*domain.GameInstallation, error) {
		return nil, nil
	}}
	loc := mustLocator(t, newFakeStore(nil), det)

	install, err := loc.ValidateInstallation("/games/darktide")
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeGameNotFound),
		"got %v, want game-not-found",
		err,
	)

	assert.Nil(t, install, "expected no installation alongside the error, got %+v", install)
}

func TestGameLocatorValidatePropagatesDetectorErrors(t *testing.T) {
	det := &fakeDetector{validate: func(string) (*domain.GameInstallation, error) {
		return nil, appErrors.New(appErrors.ErrCodeGameInvalid, "does not look like Darktide")
	}}
	loc := mustLocator(t, newFakeStore(nil), det)

	{
		_, err := loc.ValidateInstallation(
			"/games/notdarktide",
		)
		require.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeGameInvalid,
		), "got %v, want game-invalid", err)
	}
}

func TestGameLocatorCurrentPrefersTheConfiguredPath(t *testing.T) {
	cfg := domain.DefaultConfig()
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.AutoDetect = true })
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.Path = "/games/darktide" })

	det := &fakeDetector{
		validate: func(string) (*domain.GameInstallation, error) {
			out := install("/games/darktide")
			return &out, nil
		},
		find: func(context.Context) ([]domain.GameInstallation, error) {
			// Sorts before the configured path, so picking "the first hit" would
			// be wrong here.
			return []domain.GameInstallation{install("/games/aaa")}, nil
		},
	}
	loc := mustLocator(t, newFakeStore(&cfg), det)

	current, err := loc.Current(context.Background())
	require.NoError(t, err, "Current")

	assert.Equal(
		t,
		"/games/darktide",
		current.Path,
		"Current picked %q, want the configured path",
		current.Path,
	)
}

func TestGameLocatorCurrentReportsNothingFound(t *testing.T) {
	cfg := domain.DefaultConfig()
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.AutoDetect = true })
	loc := mustLocator(t, newFakeStore(&cfg), &fakeDetector{})

	current, err := loc.Current(context.Background())
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeGameNotFound),
		"got %v, want game-not-found",
		err,
	)

	assert.Nil(t, current, "expected no installation alongside the error, got %+v", current)
}

// ---------------------------------------------------------------------------
// GetModsPath
// ---------------------------------------------------------------------------

func TestGameLocatorGetModsPathRejectsNil(t *testing.T) {
	loc := mustLocator(t, newFakeStore(nil), &fakeDetector{})
	{
		_, err := loc.GetModsPath(nil)
		require.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeGameInvalid),
			"got %v, want game-invalid",
			err,
		)
	}
}

func TestGameLocatorGetModsPathForwardsTheTrimmedOverride(t *testing.T) {
	cfg := domain.DefaultConfig()
	cfg.UpdateActiveSettings(
		func(gs *domain.GameSettings) { gs.ModsFolderOverride = "  /games/darktide/custom-mods  " },
	)

	det := &fakeDetector{
		modsPath: func(_ *domain.GameInstallation, override string) (string, error) {
			return override, nil
		},
	}
	loc := mustLocator(t, newFakeStore(&cfg), det)

	got, err := loc.GetModsPath(&domain.GameInstallation{Path: "/games/darktide"})
	require.NoError(t, err, "GetModsPath")

	assert.Equal(
		t,
		"/games/darktide/custom-mods",
		got,
		"mods path = %q, want the trimmed override",
		got,
	)
}

func TestGameLocatorGetModsPathWithoutOverrideUsesTheInstallation(t *testing.T) {
	det := &fakeDetector{
		modsPath: func(install *domain.GameInstallation, override string) (string, error) {
			assert.Empty(t, override, "override = %q, want empty", override)

			return install.ModsPath, nil
		},
	}
	loc := mustLocator(t, newFakeStore(nil), det)

	got, err := loc.GetModsPath(
		&domain.GameInstallation{Path: "/games/darktide", ModsPath: "/games/darktide/mods"},
	)
	require.NoError(t, err, "GetModsPath")

	assert.Equal(t, "/games/darktide/mods", got, "mods path = %q, want the installation's own", got)
}

func TestGameLocatorGetModsPathPropagatesDetectorErrors(t *testing.T) {
	det := &fakeDetector{modsPath: func(*domain.GameInstallation, string) (string, error) {
		return "", appErrors.New(appErrors.ErrCodeModsPathNotDir, "no mods folder")
	}}
	loc := mustLocator(t, newFakeStore(nil), det)

	{
		_, err := loc.GetModsPath(
			&domain.GameInstallation{Path: "/games/darktide"},
		)
		require.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeModsPathNotDir,
		), "got %v, want mods-path-not-dir", err)
	}
}

// ---------------------------------------------------------------------------
// SetGamePath
// ---------------------------------------------------------------------------

func TestGameLocatorSetGamePathPersistsTheValidatedPath(t *testing.T) {
	cfg := domain.DefaultConfig()
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.AutoDetect = true })
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.Source = "steam" })
	store := newFakeStore(&cfg)
	det := &fakeDetector{validate: func(path string) (*domain.GameInstallation, error) {
		out := install(strings.TrimSpace(path))
		out.Source = domain.GameSourceEpic

		return &out, nil
	}}
	loc := mustLocator(t, store, det)

	{
		err := loc.SetGamePath(context.Background(), "  /games/darktide  ")
		require.NoError(t, err, "SetGamePath: %v", err)
	}

	require.Equal(t, 1, store.saves, "saved %d time(s), want 1", store.saves)

	assert.Equal(
		t,
		"/games/darktide",
		store.saved.ActiveSettings().Path,
		"persisted path = %q, want the cleaned one",
		store.saved.ActiveSettings().Path,
	)
	// Source is a *filter* on auto-detection, not a record of the last install,
	// so pointing the manager at a directory must not silently narrow detection.
	assert.Equal(
		t,
		domain.GameSourceSteam,
		store.saved.ActiveSettings().Source,
		"configured source filter changed to %q",
		store.saved.ActiveSettings().Source,
	)

	assert.True(
		t,
		store.saved.ActiveSettings().AutoDetect,
		"SetGamePath turned auto-detect off; it should only touch the path",
	)
}

func TestGameLocatorSetGamePathRejectsAnInvalidPathWithoutSaving(t *testing.T) {
	store := newFakeStore(nil)
	det := &fakeDetector{validate: func(string) (*domain.GameInstallation, error) {
		return nil, appErrors.New(appErrors.ErrCodeGameInvalid, "does not look like Darktide")
	}}
	loc := mustLocator(t, store, det)

	{
		err := loc.SetGamePath(
			context.Background(),
			"/games/notdarktide",
		)
		require.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeGameInvalid,
		), "got %v, want game-invalid", err)
	}

	assert.Equal(
		t,
		0,
		store.saves,
		"an invalid path was still persisted (%d write(s))",
		store.saves,
	)
}

func TestGameLocatorSetGamePathReportsAWriteFailure(t *testing.T) {
	store := newFakeStore(nil)
	store.saveErr = appErrors.New(appErrors.ErrCodeConfigIO, "read-only filesystem")
	det := &fakeDetector{validate: func(string) (*domain.GameInstallation, error) {
		out := install("/games/darktide")
		return &out, nil
	}}
	loc := mustLocator(t, store, det)

	err := loc.SetGamePath(context.Background(), "/games/darktide")
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeConfigIO),
		"got %v, want config-io",
		err,
	)
}

func TestGameLocatorSetGamePathHonorsACanceledContext(t *testing.T) {
	store := newFakeStore(nil)
	det := &fakeDetector{}
	loc := mustLocator(t, store, det)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	{
		err := loc.SetGamePath(
			ctx,
			"/games/darktide",
		)
		require.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeOperationCanceled,
		), "got %v, want operation-canceled", err)
	}

	assert.Equal(t, 0, store.saves, "a canceled call still wrote the config")
}

func TestGameLocatorSetGamePathToleratesANilContext(t *testing.T) {
	store := newFakeStore(nil)
	det := &fakeDetector{validate: func(string) (*domain.GameInstallation, error) {
		out := install("/games/darktide")
		return &out, nil
	}}
	loc := mustLocator(t, store, det)

	//nolint:staticcheck // exercising the nil-context guard on purpose
	{
		err := loc.SetGamePath(nil, "/games/darktide")
		require.NoError(t, err, "nil context: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Detector factory
// ---------------------------------------------------------------------------

func TestGameLocatorRebuildsTheDetectorForEveryLookup(t *testing.T) {
	// The user can repoint the game from the UI; a cached detector would keep
	// folding the old path in as an extra candidate.
	store := newFakeStore(nil)
	loc, seen := locatorWith(store, &fakeDetector{
		validate: func(string) (*domain.GameInstallation, error) {
			out := install("/games/darktide")
			return &out, nil
		},
	})

	{
		_, err := loc.FindInstallations(context.Background())
		require.NoError(t, err, "FindInstallations: %v", err)
	}

	store.cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.Path = "/games/second" })

	{
		_, err := loc.FindInstallations(context.Background())
		require.NoError(t, err, "FindInstallations: %v", err)
	}

	require.Len(t, *seen, 2, "detector built %d time(s), want one per lookup", len(*seen))

	assert.False(
		t,
		(*seen)[0].Path != "" || (*seen)[1].Path != "/games/second",
		"factory saw %q then %q, want the config to change between calls",
		(*seen)[0].Path,
		(*seen)[1].Path,
	)
}

// ---------------------------------------------------------------------------
// Production wiring
// ---------------------------------------------------------------------------

func TestGameLocatorDefaultFactoryProbesTheConfiguredPath(t *testing.T) {
	// Exercises platformDetectorFactory end to end: the configured path has to
	// reach the real probe as an extra candidate, or a user with a game outside
	// every known launcher directory can never be helped.
	dir := t.TempDir()

	for _, sub := range []string{"data", domain.ModsDirName} {
		{
			err := os.MkdirAll(filepath.Join(dir, sub), 0o755)
			require.NoError(t, err, "prepare fake install: %v", err)
		}
	}

	cfg := domain.DefaultConfig()
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.AutoDetect = false })
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.Path = dir })
	loc := NewGameLocator(newFakeStore(&cfg))

	installs, err := loc.FindInstallations(context.Background())
	require.NoError(t, err, "FindInstallations")

	require.Len(t, installs, 1, "got %d installations, want 1: %+v", len(installs), installs)

	assert.True(t, installs[0].Valid, "installation should be marked valid")

	{
		want := filepath.Join(dir, domain.ModsDirName)
		assert.Equal(
			t,
			want,
			installs[0].ModsPath,
			"mods path = %q, want %q",
			installs[0].ModsPath,
			want,
		)
	}

	assert.Equal(
		t,
		domain.GameSourceManual,
		installs[0].Source,
		"source = %q, want manual (the default source hint is %q)",
		installs[0].Source,
		cfg.ActiveSettings().Source,
	)
}

func TestGameLocatorDefaultFactoryRejectsAForeignDirectory(t *testing.T) {
	loc := NewGameLocator(newFakeStore(nil))

	// An empty temp directory has neither Darktide.exe nor data/ nor mods/.
	install, err := loc.ValidateInstallation(t.TempDir())
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeGameInvalid),
		"got %v, want game-invalid",
		err,
	)

	assert.Nil(t, install, "expected no installation alongside the error, got %+v", install)
}

func TestGameLocatorNilFactoryFailsEveryProbeCleanly(t *testing.T) {
	loc := NewGameLocatorWithFactory(newFakeStore(nil), nil)

	{
		_, err := loc.ValidateInstallation(
			"/games/darktide",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeGameNotFound,
		), "ValidateInstallation: got %v, want game-not-found", err)
	}

	{
		_, err := loc.GetModsPath(
			&domain.GameInstallation{Path: "/games/darktide"},
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeGameNotFound,
		), "GetModsPath: got %v, want game-not-found", err)
	}
}

// ---------------------------------------------------------------------------
// Active game registry
// ---------------------------------------------------------------------------

// restoreGames re-registers the built-ins after a test that mutated the
// process-wide registry, so later tests resolve the default game again.
func restoreGames(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		games.Reset()
		games.EnsureBuiltins()
	})
}

func TestGameLocatorActiveResolvesTheRegistryDefault(t *testing.T) {
	games.Reset()
	games.EnsureBuiltins()
	restoreGames(t)

	loc := mustLocator(t, newFakeStore(nil), &fakeDetector{})

	def, settings, err := loc.Active()
	require.NoError(t, err)
	assert.Equal(t, "darktide", def.ID(), "empty config resolves the default game")
	assert.Equal(t, domain.DefaultGameSettings(), settings, "defaults apply until configured")
}

func TestGameLocatorActiveRejectsAnUnknownGame(t *testing.T) {
	games.Reset()
	games.EnsureBuiltins()
	restoreGames(t)

	cfg := domain.DefaultConfig()
	cfg.ActiveGame = "nope"
	loc := mustLocator(t, newFakeStore(&cfg), &fakeDetector{})

	_, _, err := loc.Active()
	require.Error(t, err, "an unregistered active game must not resolve")
	assert.True(t, appErrors.IsAppError(err, appErrors.ErrCodeGameNotFound),
		"Active: got %v, want game-not-found", err)
}

func TestGameLocatorSetActivePersistsTheRegisteredID(t *testing.T) {
	games.Reset()
	games.EnsureBuiltins()
	restoreGames(t)
	require.NoError(t, games.Register(gamestest.Def{IDValue: "sietch"}))

	store := newFakeStore(nil)
	loc := mustLocator(t, store, &fakeDetector{})

	require.NoError(t, loc.SetActive("SIETCH"), "ids resolve case-insensitively")
	require.NotNil(t, store.saved, "SetActive writes the configuration")
	assert.Equal(t, "sietch", store.saved.ActiveGame, "the registered id is persisted")

	saves := store.saves
	err := loc.SetActive("missing")
	require.Error(t, err, "an unregistered game must not become active")
	assert.True(t, appErrors.IsAppError(err, appErrors.ErrCodeGameNotFound),
		"SetActive: got %v, want game-not-found", err)
	assert.Equal(t, saves, store.saves, "a rejected switch must not write")
}

func TestGameLocatorInstallationsStampsEveryGame(t *testing.T) {
	games.Reset()
	games.EnsureBuiltins()
	restoreGames(t)
	require.NoError(t, games.Register(gamestest.Def{IDValue: "sietch"}))

	det := &fakeDetector{
		find: func(context.Context) ([]domain.GameInstallation, error) {
			return []domain.GameInstallation{
				{Path: "/games/shared", Valid: true},
			}, nil
		},
	}
	loc := mustLocator(t, newFakeStore(nil), det)

	installs, err := loc.Installations(context.Background())
	require.NoError(t, err)

	var ids []string
	for _, install := range installs {
		ids = append(ids, install.GameID)
		assert.Equal(t, "/games/shared", install.Path, "path survives stamping")
		assert.True(t, install.Valid, "valid survives stamping")
	}

	var want []string
	for _, def := range games.All() {
		want = append(want, def.ID())
	}

	assert.Equal(t, want, ids, "one entry per registered game, grouped by game id")
}

func TestGameLocatorFindInstallationsStampsTheActiveGame(t *testing.T) {
	games.Reset()
	games.EnsureBuiltins()
	restoreGames(t)

	det := &fakeDetector{
		find: func(context.Context) ([]domain.GameInstallation, error) {
			return []domain.GameInstallation{{Path: "/games/one", Valid: true}}, nil
		},
	}
	loc := mustLocator(t, newFakeStore(nil), det)

	installs, err := loc.FindInstallations(context.Background())
	require.NoError(t, err)
	require.Len(t, installs, 1, "one detected install")
	assert.Equal(t, "darktide", installs[0].GameID, "the active game id is stamped")
}
