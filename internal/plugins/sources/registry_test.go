package sources

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/internal/games/gamestest"
	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/sources/shared/github"
	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/sources/shared/local"
	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/sources/shared/nexus"
	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubSource is a domain.ModSource with an id the test chooses. Only ID and the
// capability flags matter to RegisterAll, so the search and download methods
// refuse rather than pretend: if a test ever needs them it should say so.
type stubSource struct {
	id string
}

func (s *stubSource) ID() string          { return s.id }
func (s *stubSource) Name() string        { return s.id }
func (s *stubSource) Description() string { return "" }
func (s *stubSource) Version() string     { return "test" }

func (s *stubSource) SupportsSearch() bool { return false }
func (s *stubSource) SupportsAuth() bool   { return false }
func (s *stubSource) RequiresAPIKey() bool { return false }

func (s *stubSource) Search(context.Context, domain.SearchQuery) ([]domain.Mod, error) {
	return nil, appErrors.New(appErrors.ErrCodeNotFound, "stub source %q does not search", s.id)
}

func (s *stubSource) GetMod(context.Context, string) (*domain.Mod, error) {
	return nil, appErrors.New(appErrors.ErrCodeModNotFound, "stub source %q has no mods", s.id)
}

func (s *stubSource) Download(context.Context, *domain.Mod, string) (string, error) {
	return "", appErrors.New(
		appErrors.ErrCodeDownloadFailed,
		"stub source %q does not download",
		s.id,
	)
}

func (s *stubSource) Authenticate(context.Context, domain.AuthCredentials) error { return nil }

func (s *stubSource) ValidateCredentials(context.Context) error { return nil }

// registerFactory adds a factory under a fresh id and removes it afterwards. The
// factory registry is process-wide and shared with every other test in this
// binary, so a test that left its factory behind would change the answer of a
// later RegisterAll that counts registered ids.
func registerFactory(t *testing.T, id string, factory services.SourceFactory) {
	t.Helper()

	_, exists := services.SourceFactoryFor(id)
	require.False(
		t,
		exists,
		"test source id %q is already registered; ids must be unique per test",
		id,
	)

	err := services.RegisterSourceFactory(id, factory)
	require.NoError(t, err, "register factory %q: %v", id, err)

	t.Cleanup(func() {
		assert.True(
			t,
			services.UnregisterSourceFactory(id),
			"cleanup: factory %q was not registered, so it was not removed",
			id,
		)
	})
}

// constantFactory always returns the same source, whatever the configuration.
func constantFactory(source domain.ModSource) services.SourceFactory {
	return func(services.SourceBuildContext) (domain.ModSource, error) { return source, nil }
}

// factoryCounter records how often a factory ran and with what configuration.
//
// It exists because "was this source registered?" cannot be answered through the
// registry: SourceRegistry.IDs reports every id the factory registry knows and
// SourceRegistry.List resolves each one, building whatever it has not built yet.
// Both would say yes for a source RegisterAll deliberately skipped. Counting the
// factory calls is the only way to see that a source was left alone.
type factoryCounter struct {
	calls []domain.SourceConfig
	mu    sync.Mutex
}

func (c *factoryCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.calls)
}

// countingFactory registers a factory that builds the given source and returns a
// counter for it.
func countingFactory(t *testing.T, id string, source domain.ModSource) *factoryCounter {
	t.Helper()

	counter := &factoryCounter{}
	capture := func(ctx services.SourceBuildContext) (domain.ModSource, error) {
		counter.mu.Lock()
		counter.calls = append(counter.calls, ctx.Config)
		counter.mu.Unlock()

		return source, nil
	}
	registerFactory(t, id, capture)

	return counter
}

func newRegistry(t *testing.T) *services.SourceRegistry {
	t.Helper()
	return services.NewSourceRegistry()
}

// liveIDs lists the sources that were actually built and registered.
//
// It cannot use SourceRegistry.IDs, which deliberately reports every id the
// factory registry knows about — including ones that were skipped, so a caller
// can see a source exists but was not built. Asking it "was this source
// registered?" therefore always says yes.
func liveIDs(t *testing.T, registry *services.SourceRegistry) []string {
	t.Helper()

	list := registry.List()

	out := make([]string, 0, len(list))
	for _, source := range list {
		out = append(out, source.ID())
	}

	return out
}

func TestIDsListsEveryBundledSource(t *testing.T) {
	ids := IDs()
	if len(ids) == 0 {
		require.FailNow(t, "IDs() returned nothing; the source packages must register from init")
	}

	for _, want := range []string{github.SourceID, local.SourceID, nexus.SourceID} {
		assert.Contains(t, ids, want, "IDs() = %v, missing bundled source %q", ids, want)
	}
}

func TestIDsIsSortedAndStable(t *testing.T) {
	// Registration order must not depend on init order, so the ids come back
	// sorted. Two calls have to agree as well, or the source list in the UI
	// would reshuffle itself between refreshes.
	first := IDs()
	for i := 1; i < len(first); i++ {
		require.False(t, first[i-1] > first[i], "IDs() = %v, not sorted at index %d", first, i)
	}

	second := IDs()
	assert.Equal(
		t,
		strings.Join(second, ","),
		strings.Join(first, ","),
		"IDs() returned %v then %v; the order must not vary",
		first,
		second,
	)
}

// TestRegisterAllBuildsEveryBundledSource is the guard against the imports in
// this file being trimmed. Each source package registers from init, so dropping
// an import removes a source from the manager without anything else failing to
// compile.
func TestRegisterAllBuildsEveryBundledSource(t *testing.T) {
	registry := newRegistry(t)
	err := RegisterAll(registry, nil, nil)
	require.NoError(t, err, "RegisterAll over the bundled sources: %v", err)

	for _, want := range []string{github.SourceID, local.SourceID, nexus.SourceID} {
		source, err := registry.Get(want)
		if err != nil {
			assert.Fail(t, "bundled source %q is not registered: %v", want, err)
			continue
		}

		assert.Equal(
			t,
			want,
			source.ID(),
			"source registered under %q reports its own id as %q",
			want,
			source.ID(),
		)
	}
}

// TestRegisterAllIgnoresUnknownConfigurationBlocks guards the other direction: a
// config naming a source that is not compiled in must not be an error, because a
// config file survives an upgrade that removed a plugin.
func TestRegisterAllIgnoresUnknownConfigurationBlocks(t *testing.T) {
	registry := newRegistry(t)

	cfg := map[string]domain.SourceConfig{
		"not-a-real-source": {Enabled: true, APIKey: "k"},
	}
	err := RegisterAll(registry, cfg, nil)
	require.NoError(t, err, "RegisterAll with an unknown source block: %v", err)

	ids := liveIDs(t, registry)
	assert.Len(t, ids, len(IDs()), "registered %v, want exactly the bundled %v", ids, IDs())
}

func TestRegisterAllRejectsANilRegistry(t *testing.T) {
	// A nil registry is a wiring bug in the caller, not a user error. Failing
	// here means the container reports it at start-up rather than a later call
	// dereferencing nil.
	err := RegisterAll(nil, nil, nil)
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
		"RegisterAll(nil) error = %v, want %s",
		err,
		appErrors.ErrCodeInvalidArgument,
	)
}

func TestRegisterAllSkipsAnExplicitlyDisabledSource(t *testing.T) {
	const disabled = "registry-test-disabled"

	counter := countingFactory(t, disabled, &stubSource{id: disabled})

	registry := newRegistry(t)

	cfg := map[string]domain.SourceConfig{disabled: {Enabled: false}}
	err := RegisterAll(registry, cfg, nil)
	require.NoError(t, err, "RegisterAll with one disabled source: %v", err)

	assert.Equal(
		t,
		0,
		counter.count(),
		"the factory for the disabled source ran %d times, want 0",
		counter.count(),
	)
	// Every other source still has to be there: a disabled source must not cost
	// the user the ones they left enabled.
	assert.Contains(
		t,
		liveIDs(t, registry),
		github.SourceID,
		"registered %v, want the bundled sources alongside the disabled one",
		liveIDs(t, registry),
	)
}

// TestRegisterAllTreatsAMissingBlockAsEnabled is the fresh-install case. A
// manager that started with an empty source list would look broken, and there is
// nothing in a default config to opt in.
func TestRegisterAllTreatsAMissingBlockAsEnabled(t *testing.T) {
	for i, cfg := range []map[string]domain.SourceConfig{
		nil,
		{},
		{github.SourceID: {Enabled: true}},
	} {
		// Each pass needs its own id: the factories of a previous pass are still
		// registered, and this assertion is about *this* source.
		id := fmt.Sprintf("registry-test-no-block-%d", i)
		counter := countingFactory(t, id, &stubSource{id: id})

		registry := newRegistry(t)
		err := RegisterAll(registry, cfg, nil)
		require.NoError(t, err, "RegisterAll with a partial config: %v", err)

		assert.Equal(
			t,
			1,
			counter.count(),
			"config %v built the source with no block %d times, want 1; a missing block counts as enabled",
			cfg,
			counter.count(),
		)
	}
}

// TestRegisterAllAcceptsAMixedCaseConfigurationKey covers a hand-edited config
// naming "Registry-Test-Mixed" instead of the registry's own key. Ignoring it
// would leave the user with settings they can see in their file that the manager
// silently drops.
func TestRegisterAllAcceptsAMixedCaseConfigurationKey(t *testing.T) {
	const id = "registry-test-mixed-case"
	registerFactory(t, id, func(ctx services.SourceBuildContext) (domain.ModSource, error) {
		// The block reaching the factory is the assertion: the token has to be
		// there, not merely the source.
		if ctx.Config.APIKey != "tok-1" {
			return nil, appErrors.New(appErrors.ErrCodeSourceUnavailable,
				"factory for %q got APIKey %q, want %q", id, ctx.Config.APIKey, "tok-1")
		}

		return &stubSource{id: id}, nil
	})

	registry := newRegistry(t)

	cfg := map[string]domain.SourceConfig{
		"  Registry-Test-Mixed-Case  ": {Enabled: true, APIKey: "tok-1"},
	}
	err := RegisterAll(registry, cfg, nil)
	require.NoError(t, err, "RegisterAll with a mixed-case key: %v", err)

	assert.Contains(t, liveIDs(t, registry), id, "registered %v, want %q", liveIDs(t, registry), id)
}

// TestRegisterAllReportsAFailingSourceAndKeepsTheRest is the reason RegisterAll
// returns an error at all: one source the user has not configured must not stop
// the manager from managing the mods it already has.
func TestRegisterAllReportsAFailingSourceAndKeepsTheRest(t *testing.T) {
	const broken = "registry-test-broken"
	registerFactory(t, broken, func(services.SourceBuildContext) (domain.ModSource, error) {
		return nil, appErrors.New(appErrors.ErrCodeAuthFailed, "the api key is wrong")
	})

	registry := newRegistry(t)

	err := RegisterAll(registry, nil, nil)
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeSourceUnavailable),
		"RegisterAll error = %v, want it wrapped as %s",
		err,
		appErrors.ErrCodeSourceUnavailable,
	)

	assert.Contains(t, err.Error(), broken, "error %q does not name the source %q", err, broken)

	assert.Contains(
		t,
		err.Error(),
		"the api key is wrong",
		"error %q does not keep the factory's own reason",
		err,
	)

	for _, want := range []string{github.SourceID, local.SourceID, nexus.SourceID} {
		assert.Contains(
			t,
			liveIDs(t, registry),
			want,
			"registered %v, want %q to survive a sibling's failure",
			liveIDs(t, registry),
			want,
		)
	}
}

// TestRegisterAllNamesEveryBrokenSource covers the multi-failure path: one error
// naming only the first problem sends the user round the loop fixing one source
// at a time.
func TestRegisterAllNamesEveryBrokenSource(t *testing.T) {
	const (
		first  = "registry-test-broken-a"
		second = "registry-test-broken-b"
	)
	for _, id := range []string{first, second} {
		id := id
		registerFactory(t, id, func(services.SourceBuildContext) (domain.ModSource, error) {
			return nil, appErrors.New(appErrors.ErrCodeSourceUnavailable, "%s is unusable", id)
		})
	}

	err := RegisterAll(newRegistry(t), nil, nil)
	if err == nil {
		require.FailNow(t, "RegisterAll with two broken sources returned no error")
	}

	for _, id := range []string{first, second} {
		assert.Contains(t, err.Error(), id, "error %q does not name %q", err, id)
	}
}

// TestRegisterAllReportsAFactoryThatReturnsNothing covers a plugin that reports
// success and hands back a nil source. Registering it would make every later
// lookup panic.
func TestRegisterAllReportsAFactoryThatReturnsNothing(t *testing.T) {
	const empty = "registry-test-nil-source"
	registerFactory(t, empty, func(services.SourceBuildContext) (domain.ModSource, error) {
		return nil, nil
	})

	err := RegisterAll(newRegistry(t), nil, nil)
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeSourceUnavailable),
		"RegisterAll error = %v, want %s",
		err,
		appErrors.ErrCodeSourceUnavailable,
	)

	assert.Contains(t, err.Error(), empty, "error %q does not name %q", err, empty)
}

// TestRegisterAllReportsASourceItCannotRegister covers a factory returning a
// source the registry refuses — a blank id, here. It has to be reported: a
// source the user configured that never appears is exactly the silent failure
// this function exists to prevent.
func TestRegisterAllReportsASourceItCannotRegister(t *testing.T) {
	const bad = "registry-test-blank-id"
	registerFactory(t, bad, func(services.SourceBuildContext) (domain.ModSource, error) {
		return &stubSource{id: "   "}, nil
	})

	err := RegisterAll(newRegistry(t), nil, nil)
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeSourceUnavailable),
		"RegisterAll error = %v, want it wrapped as %s",
		err,
		appErrors.ErrCodeSourceUnavailable,
	)

	assert.Contains(t, err.Error(), bad, "error %q does not name %q", err, bad)
}

// TestRegisterAllIsQuietWhenASourceIsAlreadyRegistered covers the second
// container in one process, or a test that registers first. The outcome is what
// the caller wanted, so it is not a failure.
func TestRegisterAllIsQuietWhenASourceIsAlreadyRegistered(t *testing.T) {
	const id = "registry-test-already-there"
	registerFactory(t, id, constantFactory(&stubSource{id: id}))

	registry := newRegistry(t)
	if err := registry.Register(&stubSource{id: id}); err != nil {
		require.FailNow(t, "pre-register %q: %v", id, err)
	}

	err := RegisterAll(registry, nil, nil)
	require.NoError(t, err, "RegisterAll over an already-registered source: %v", err)

	if ids := liveIDs(t, registry); !contains(ids, id) || !contains(ids, github.SourceID) {
		assert.Fail(t, "registered %v, want both %q and the bundled sources", ids, id)
	}
}

func TestRegisterEnabledIsRegisterAllWithoutBlocks(t *testing.T) {
	registry := newRegistry(t)
	err := RegisterEnabled(registry, nil)
	require.NoError(t, err, "RegisterEnabled: %v", err)

	want := IDs()

	got := registry.IDs()
	assert.Equal(
		t,
		strings.Join(want, ","),
		strings.Join(got, ","),
		"RegisterEnabled registered %v, want %v",
		got,
		want,
	)
}

func TestEnabled(t *testing.T) {
	cfg := map[string]domain.SourceConfig{
		"github": {Enabled: false},
		"NEXUS":  {Enabled: true},
		"  ":     {Enabled: false},
	}

	if Enabled(cfg, "github") {
		assert.Fail(t, "Enabled(github) = true, want false")
	}
	// The id is normalised on the way in, so a caller that trimmed and
	// lower-cased it must reach the same answer.
	if !Enabled(cfg, "  Nexus  ") {
		assert.Fail(t, "Enabled(Nexus) = false, want true; the id is trimmed and lower-cased")
	}
	// A missing block counts as enabled, and so does a blank key nobody can ask
	// for.
	assert.True(
		t,
		Enabled(cfg, local.SourceID),
		"Enabled(%s) = false, want true for a source with no block",
		local.SourceID,
	)

	if !Enabled(nil, "anything") {
		assert.Fail(t, "Enabled(nil) = false, want true")
	}
}

// TestEnabledNormalizesConfigurationKeys is the same hand-edited-config hazard
// as TestRegisterAllAcceptsAMixedCaseConfigurationKey, on the other function. If
// only one of the two normalises, the UI can show a source as enabled while
// RegisterAll skips it.
func TestEnabledNormalizesConfigurationKeys(t *testing.T) {
	cfg := map[string]domain.SourceConfig{"GitHub": {Enabled: false}}
	assert.False(
		t,
		Enabled(cfg, "github"),
		"Enabled = true, want false; the configuration key must be matched case-insensitively",
	)
	// A padded id still has to reach the same block — matching it proves the
	// lookup found "GitHub", because the fallback for a source with no block is
	// true.
	if Enabled(cfg, " GITHUB ") {
		assert.Fail(t, "Enabled = true, want false; a padded id must still match the block")
	}
}

func TestNormalizeBlocks(t *testing.T) {
	got := normalizeBlocks(nil)
	assert.Empty(t, got, "normalizeBlocks(nil) = %v, want empty", got)

	cfg := map[string]domain.SourceConfig{
		"  GitHub ": {APIKey: "exact"},
		"nexus":     {APIKey: "lower"},
		"   ":       {APIKey: "blank"},
		"VORTEX":    {APIKey: "upper"},
		"":          {APIKey: "empty"},
		"DUP":       {APIKey: "upper-case-spelling"},
		"dup":       {APIKey: "lower-case-spelling"},
	}
	got = normalizeBlocks(cfg)

	for _, key := range []string{"github", "nexus", "dup"} {
		_, ok := got[key]
		assert.True(t, ok, "normalizeBlocks lost key %q; got %v", key, sortedKeys(got))
	}

	for _, key := range []string{"", "   "} {
		_, ok := got[key]
		assert.False(t, ok, "normalizeBlocks kept the blank key %q", key)
	}
	// Two spellings of one id is a mistake in the file. Sorted order decides the
	// winner, and "DUP" sorts before "dup", so it is the one that has to survive
	// — otherwise the answer would be whichever spelling map iteration reached
	// first, and could change between starts.
	assert.Equal(
		t,
		"upper-case-spelling",
		got["dup"].APIKey,
		"normalizeBlocks kept %q for id %q, want %q (the first in sorted order)",
		got["dup"].APIKey,
		"dup",
		"upper-case-spelling",
	)
	// The padded key's value has to survive the normalisation intact.
	assert.Equal(
		t,
		"exact",
		got["github"].APIKey,
		"normalizeBlocks changed the block for %q: %+v",
		"github",
		got["github"],
	)
}

func TestNormalizeBlocksIsStableAcrossRuns(t *testing.T) {
	// The sorted-key walk is what makes a duplicated id deterministic; a test
	// that ran once would not catch a regression back to map iteration. The
	// comparison has to sort too, since the keys come out of a map.
	cfg := map[string]domain.SourceConfig{
		"Zeta": {APIKey: "z"}, "alpha": {APIKey: "a"}, "MID": {APIKey: "m"},
		"beta": {APIKey: "b"}, "Gamma": {APIKey: "g"}, "gamma": {APIKey: "g2"},
	}

	want := strings.Join(sortedKeys(normalizeBlocks(cfg)), ",")
	for i := 0; i < 8; i++ {
		got := strings.Join(sortedKeys(normalizeBlocks(cfg)), ",")
		require.Equal(t, want, got, "run %d produced %q, want %q", i, got, want)
	}
}

// TestRegisterAllIsSafeUnderConcurrentUse is about the container, not the
// sources: two entry points may each build a container in the same process, and
// both will walk the factory registry at once.
func TestRegisterAllIsSafeUnderConcurrentUse(t *testing.T) {
	const shared = "registry-test-concurrent"
	registerFactory(t, shared, constantFactory(&stubSource{id: shared}))

	var wg sync.WaitGroup

	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			errs[i] = RegisterAll(newRegistry(t), nil, nil)
		}(i)
	}

	wg.Wait()

	for i, err := range errs {
		assert.NoError(t, err, "goroutine %d: RegisterAll: %v", i)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}

	return false
}

func sortedKeys(blocks map[string]domain.SourceConfig) []string {
	out := make([]string, 0, len(blocks))
	for key := range blocks {
		out = append(out, key)
	}

	sort.Strings(out)

	return out
}

// ----------------------------------------------------- game-aware RegisterAll

func TestRegisterAllHandsTheActiveGameToEveryFactory(t *testing.T) {
	const id = "game-context-probe"

	var gotGameID string

	registerFactory(t, id, func(ctx services.SourceBuildContext) (domain.ModSource, error) {
		if ctx.Game != nil {
			gotGameID = ctx.Game.ID()
		}

		return &stubSource{id: id}, nil
	})

	registry := newRegistry(t)
	require.NoError(t, RegisterAll(registry, nil, gamestest.Def{IDValue: "test-game"}),
		"RegisterAll with a game")

	{
		_, err := registry.Get(id)
		require.NoError(t, err, "Get(%q)", id)
	}

	assert.Equal(t, "test-game", gotGameID,
		"RegisterAll must hand the active game to each factory")
}

// registerGameFactory installs a game-scoped factory for the duration of one
// test. The factory registry is process-wide, so without the cleanup a later
// RegisterAll would build a source this test invented.
func registerGameFactory(t *testing.T, gameID, id string, factory services.SourceFactory) {
	t.Helper()

	err := services.RegisterGameSourceFactory(gameID, id, factory)
	require.NoError(t, err, "register game factory %q/%q: %v", gameID, id, err)

	t.Cleanup(func() {
		assert.True(t, services.UnregisterGameSourceFactory(gameID, id),
			"cleanup: game factory %q/%q was not registered, so it was not removed",
			gameID, id)
	})
}

func TestRegisterAllBuildsGameScopedFactories(t *testing.T) {
	const id = "only-for-test-game"

	registerGameFactory(
		t,
		"test-game",
		id,
		func(services.SourceBuildContext) (domain.ModSource, error) {
			return &stubSource{id: id}, nil
		},
	)

	{
		registry := newRegistry(t)
		require.NoError(t, RegisterAll(registry, nil, gamestest.Def{IDValue: "test-game"}),
			"RegisterAll for the owning game")

		source, err := registry.Get(id)
		require.NoError(t, err, "the owning game must get its own source: %v", err)
		assert.Equal(t, id, source.ID())
	}

	{
		registry := newRegistry(t)
		require.NoError(t, RegisterAll(registry, nil, gamestest.Def{IDValue: "other-game"}),
			"RegisterAll for another game")

		_, err := registry.Get(id)
		assert.Equal(t, appErrors.ErrCodeSourceNotFound, appErrors.CodeOf(err),
			"a game that does not own the source must not see it (err %v)", err)
	}
}

func TestRegisterAllPrefersTheGamesFactoryOverTheGenericOne(t *testing.T) {
	const id = "overridden-source"

	registerFactory(t, id, func(services.SourceBuildContext) (domain.ModSource, error) {
		return &stubSource{id: id}, nil
	})
	registerGameFactory(
		t,
		"test-game",
		id,
		func(services.SourceBuildContext) (domain.ModSource, error) {
			return &overriddenSource{id: id}, nil
		},
	)

	registry := newRegistry(t)
	require.NoError(t, RegisterAll(registry, nil, gamestest.Def{IDValue: "test-game"}),
		"RegisterAll with both factories registered")

	source, err := registry.Get(id)
	require.NoError(t, err, "Get(%q)", id)
	assert.Equal(t, "game", source.Version(),
		"the game-scoped factory must win over the generic one")
}

// overriddenSource reports a different Version from stubSource so a test can
// tell which of two factories built a source with the same id.
type overriddenSource struct{ id string }

func (s *overriddenSource) ID() string          { return s.id }
func (s *overriddenSource) Name() string        { return s.id }
func (s *overriddenSource) Description() string { return "" }
func (s *overriddenSource) Version() string     { return "game" }

func (s *overriddenSource) SupportsSearch() bool { return false }
func (s *overriddenSource) SupportsAuth() bool   { return false }
func (s *overriddenSource) RequiresAPIKey() bool { return false }

func (s *overriddenSource) Search(
	context.Context, domain.SearchQuery,
) ([]domain.Mod, error) {
	return nil, nil
}

func (s *overriddenSource) GetMod(context.Context, string) (*domain.Mod, error) {
	return nil, nil
}

func (s *overriddenSource) Download(context.Context, *domain.Mod, string) (string, error) {
	return "", nil
}

func (s *overriddenSource) Authenticate(context.Context, domain.AuthCredentials) error {
	return nil
}

func (s *overriddenSource) ValidateCredentials(context.Context) error { return nil }
