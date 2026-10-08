package services

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSource is a domain.ModSource whose every behaviour is a func field, so a
// test can supply exactly the behaviour it cares about and leave the rest nil
// (the accessors then report a zero value, which is what the registry reads).
type fakeSource struct {
	search   func(ctx context.Context, q domain.SearchQuery) ([]domain.Mod, error)
	getMod   func(ctx context.Context, modID string) (*domain.Mod, error)
	auths    func(ctx context.Context, creds domain.AuthCredentials) error
	authed   func(ctx context.Context) error
	id       string
	name     string
	desc     string
	version  string
	calls    authCounters
	searches atomic.Int64
}

type authCounters struct {
	authErr error
	valErr  error
	creds   []domain.AuthCredentials
	auth    atomic.Int64
	valid   atomic.Int64
	authMu  sync.Mutex
}

func (f *fakeSource) ID() string          { return f.id }
func (f *fakeSource) Name() string        { return f.name }
func (f *fakeSource) Description() string { return f.desc }
func (f *fakeSource) Version() string     { return f.version }

func (f *fakeSource) SupportsSearch() bool { return f.search != nil }
func (f *fakeSource) SupportsAuth() bool   { return f.auths != nil || f.authed != nil }
func (f *fakeSource) RequiresAPIKey() bool { return f.auths != nil }
func (f *fakeSource) Search(ctx context.Context, q domain.SearchQuery) ([]domain.Mod, error) {
	f.searches.Add(1)

	if f.search == nil {
		return nil, nil
	}

	return f.search(ctx, q)
}

func (f *fakeSource) GetMod(ctx context.Context, modID string) (*domain.Mod, error) {
	if f.getMod == nil {
		return nil, appErrors.New(appErrors.ErrCodeModNotFound, "mod %q not found", modID)
	}

	return f.getMod(ctx, modID)
}

func (f *fakeSource) Download(
	ctx context.Context,
	mod *domain.Mod,
	destPath string,
) (string, error) {
	return destPath, nil
}

func (f *fakeSource) Authenticate(ctx context.Context, creds domain.AuthCredentials) error {
	f.calls.auth.Add(1)
	f.calls.authMu.Lock()
	if f.calls.authErr != nil {
		err := f.calls.authErr
		f.calls.authMu.Unlock()

		return err
	}

	f.calls.creds = append(f.calls.creds, creds)
	f.calls.authMu.Unlock()

	if f.auths != nil {
		return f.auths(ctx, creds)
	}

	return nil
}

func (f *fakeSource) ValidateCredentials(ctx context.Context) error {
	f.calls.valid.Add(1)

	if f.calls.valErr != nil {
		return f.calls.valErr
	}

	if f.authed != nil {
		return f.authed(ctx)
	}

	return nil
}

// lastCreds reports the credentials the last successful Authenticate saw.
func (f *fakeSource) lastCreds() domain.AuthCredentials {
	f.calls.authMu.Lock()
	defer f.calls.authMu.Unlock()

	if len(f.calls.creds) == 0 {
		return nil
	}

	return f.calls.creds[len(f.calls.creds)-1]
}

func (f *fakeSource) setAuthErr(err error) {
	f.calls.authMu.Lock()
	defer f.calls.authMu.Unlock()

	f.calls.authErr = err
}

// stubSource has no func fields at all: it is the "uncomparable dynamic type"
// case, since a struct with a map field cannot be compared with ==.
type stubSource struct {
	tags map[string]string
	id   string
}

func (s *stubSource) ID() string           { return s.id }
func (s *stubSource) Name() string         { return s.id }
func (s *stubSource) Description() string  { return "" }
func (s *stubSource) Version() string      { return "1" }
func (s *stubSource) SupportsSearch() bool { return false }
func (s *stubSource) SupportsAuth() bool   { return false }
func (s *stubSource) RequiresAPIKey() bool { return false }
func (s *stubSource) Search(context.Context, domain.SearchQuery) ([]domain.Mod, error) {
	return nil, nil
}

func (s *stubSource) GetMod(context.Context, string) (*domain.Mod, error) {
	return nil, nil
}

func (s *stubSource) Download(context.Context, *domain.Mod, string) (string, error) {
	return "", nil
}
func (s *stubSource) Authenticate(context.Context, domain.AuthCredentials) error { return nil }
func (s *stubSource) ValidateCredentials(context.Context) error                  { return nil }

// registerFactory installs a factory under a unique id and returns a cleanup
// that removes it, so tests never leak a factory into the next one.
func registerFactory(t *testing.T, id string, factory SourceFactory) {
	t.Helper()
	// A leftover from a previous test with the same name would make
	// RegisterSourceFactory fail; delete it rather than hiding that.
	forgetFactory(id)

	{
		err := RegisterSourceFactory(id, factory)
		require.NoError(t, err, "RegisterSourceFactory(%q) = %v", id, err)
	}

	t.Cleanup(func() { forgetFactory(id) })
}

func forgetFactory(id string) {
	factoryMu.Lock()
	defer factoryMu.Unlock()

	delete(factories, id)
}

// ---------------------------------------------------------------- factories

func TestRegisterSourceFactoryRejectsBadInput(t *testing.T) {
	forgetFactory("dup")
	t.Cleanup(func() { forgetFactory("dup") })

	{
		err := RegisterSourceFactory(
			"",
			SourceFactory(func(domain.SourceConfig) (domain.ModSource, error) {
				return nil, nil
			}),
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeInvalidArgument,
		), "empty id: got %v, want ErrCodeInvalidArgument", err)
	}

	{
		err := RegisterSourceFactory(
			"   ",
			nil,
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeInvalidArgument,
		), "blank id with nil factory: got %v, want ErrCodeInvalidArgument", err)
	}

	noop := SourceFactory(func(domain.SourceConfig) (domain.ModSource, error) { return nil, nil })
	{
		err := RegisterSourceFactory("dup", noop)
		require.NoError(t, err, "first registration: %v", err)
	}

	{
		err := RegisterSourceFactory(
			"dup",
			noop,
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeSourceExists,
		), "duplicate id: got %v, want ErrCodeSourceExists", err)
	}
	// A duplicate is refused, not silently replaced, so the original factory
	// must still be the one that runs.
	{
		err := RegisterSourceFactory(
			"  dup  ",
			noop,
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeSourceExists,
		), "duplicate with padding: got %v, want ErrCodeSourceExists", err)
	}
}

func TestRegisteredFactoryIDsIsSortedAndDeduped(t *testing.T) {
	forgetFactory("zed")
	forgetFactory("alpha")
	forgetFactory("mid")
	t.Cleanup(func() {
		forgetFactory("zed")
		forgetFactory("alpha")
		forgetFactory("mid")
	})

	noop := SourceFactory(func(domain.SourceConfig) (domain.ModSource, error) { return nil, nil })

	for _, id := range []string{"zed", "alpha", "mid"} {
		{
			err := RegisterSourceFactory(id, noop)
			require.NoError(t, err, "register %q: %v", id, err)
		}
	}

	got := RegisteredFactoryIDs()

	want := []string{"alpha", "mid", "zed"}
	require.Len(t, got, len(want), "RegisteredFactoryIDs() = %v, want %v", got, want)

	for i := range want {
		require.Equal(t, want[i], got[i], "RegisteredFactoryIDs() = %v, want %v", got, want)
	}

	{
		_, ok := SourceFactoryFor("alpha")
		assert.True(t, ok, "SourceFactoryFor cannot find a registered id")
	}

	{
		_, ok := SourceFactoryFor("nope")
		assert.False(t, ok, "SourceFactoryFor found an id that was never registered")
	}

	{
		_, ok := SourceFactoryFor("  ALPHA  ")
		assert.True(t, ok, "SourceFactoryFor should normalise the id it is given")
	}

	{
		_, ok := SourceFactoryFor("   ")
		assert.False(t, ok, "SourceFactoryFor accepted a blank id")
	}
}

// ------------------------------------------------------------------- Register

func TestRegisterNormalizesIDAndRejectsDuplicates(t *testing.T) {
	r := NewSourceRegistry()

	first := &fakeSource{id: "  Steam  "}
	{
		err := r.Register(first)
		require.NoError(t, err, "Register: %v", err)
	}

	ids := r.IDs()
	require.False(t, len(ids) != 1 || ids[0] != "steam", "IDs() = %v, want [steam]", ids)
	// Lookup folds case and whitespace the same way.
	{
		_, err := r.Get("STEAM")
		assert.NoError(t, err, "Get(STEAM) after registering \"Steam\": %v", err)
	}

	{
		_, err := r.Get(" steam ")
		assert.NoError(t, err, "Get with padding: %v", err)
	}

	other := &fakeSource{id: "steam"}
	{
		err := r.Register(other)
		assert.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeSourceExists),
			"second distinct source with same id: got %v, want ErrCodeSourceExists",
			err,
		)
	}

	got, err := r.Get("steam")
	require.NoError(t, err)

	assert.Equal(t, first, got, "the refused registration replaced the original source")
}

func TestRegisterIsIdempotentForTheSameInstance(t *testing.T) {
	r := NewSourceRegistry()

	src := &fakeSource{id: "local"}
	{
		err := r.Register(src)
		require.NoError(t, err)
	}
	// Registering the same object twice happens for real: a plugin whose init
	// runs twice, or a DI container that rebuilds. Failing there would be
	// noise, not a useful signal.
	{
		err := r.Register(src)
		assert.NoError(t, err, "re-registering the same instance: %v", err)
	}

	assert.Len(t, r.IDs(), 1, "IDs() = %v, want exactly one entry", r.IDs())
}

func TestRegisterUncomparableSourceIsTreatedAsDistinct(t *testing.T) {
	r := NewSourceRegistry()
	first := &stubSource{id: "exotic", tags: map[string]string{"a": "1"}}
	second := &stubSource{id: "exotic", tags: map[string]string{"a": "2"}}

	{
		err := r.Register(first)
		require.NoError(t, err, "first: %v", err)
	}
	// sameSource must not panic on a type containing a map; it reports the
	// two as different and the duplicate is refused.
	{
		err := r.Register(second)
		assert.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeSourceExists),
			"second: got %v, want ErrCodeSourceExists",
			err,
		)
	}

	got, err := r.Get("exotic")
	require.NoError(t, err)

	assert.Equal(t, domain.ModSource(first), got, "the original exotic source was replaced")
}

func TestRegisterRejectsNilAndBlankIDs(t *testing.T) {
	r := NewSourceRegistry()
	{
		err := r.Register(nil)
		assert.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
			"nil source: got %v, want ErrCodeInvalidArgument",
			err,
		)
	}

	for _, id := range []string{"", "   ", "\t\n"} {
		{
			err := r.Register(
				&fakeSource{id: id},
			)
			assert.True(t, appErrors.IsAppError(
				err,
				appErrors.ErrCodeInvalidArgument,
			), "id %q: got %v, want ErrCodeInvalidArgument", id, err)
		}
	}

	assert.Empty(t, r.IDs(), "IDs() = %v, want empty", r.IDs())
}

func TestUnregisterRemovesTheSourceAndKeepsAuthState(t *testing.T) {
	r := NewSourceRegistry()

	src := &fakeSource{id: "local"}
	{
		err := r.Register(src)
		require.NoError(t, err)
	}

	r.SetAuthenticated("LOCAL", true)

	assert.True(
		t,
		r.Unregister("local"),
		"Unregister reported nothing removed for a registered source",
	)

	assert.False(t, r.Unregister("local"), "second Unregister reported a removal")

	assert.False(t, r.Unregister("  "), "Unregister with a blank id reported a removal")

	{
		_, err := r.Get("local")
		assert.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeSourceNotFound),
			"Get after unregister: got %v, want ErrCodeSourceNotFound",
			err,
		)
	}

	assert.True(
		t,
		r.Authenticated("local"),
		"unregistering the source dropped the authentication state",
	)
	// Registering again must clear a stale "failed" state.
	r.SetAuthenticated("local", false)
	_ = r.Unregister("local")
}

// ---------------------------------------------------------------------- Get

func TestGetReportsUnknownSource(t *testing.T) {
	r := NewSourceRegistry()
	{
		_, err := r.Get("nope")
		assert.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeSourceNotFound),
			"unknown id: got %v, want ErrCodeSourceNotFound",
			err,
		)
	}

	for _, id := range []string{"", "   "} {
		{
			_, err := r.Get(id)
			assert.True(
				t,
				appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
				"id %q: got %v, want ErrCodeInvalidArgument",
				id,
				err,
			)
		}
	}
}

func TestGetBuildsFactorySourceLazilyAndOnce(t *testing.T) {
	var (
		built    atomic.Int64
		builtCfg atomic.Pointer[domain.SourceConfig]
	)

	registerFactory(t, "lazy", func(cfg domain.SourceConfig) (domain.ModSource, error) {
		built.Add(1)

		c := cfg
		builtCfg.Store(&c)

		return &fakeSource{id: "lazy"}, nil
	})

	cfg := domain.DefaultConfig()
	cfg.SetSource("lazy", domain.SourceConfig{Enabled: true, APIKey: "k"})
	store := newFakeStore(&cfg)
	r := NewSourceRegistryWithStore(store)

	// Nothing has asked for the source yet, so nothing has been built.
	{
		got := built.Load()
		require.Equal(t, int64(0), got, "factory ran before first use (%d builds)", got)
	}

	first, err := r.Get("lazy")
	require.NoError(t, err, "Get")

	second, err := r.Get("lazy")
	require.NoError(t, err, "second Get")

	assert.Equal(t, second, first, "the factory built a second source; session state would be lost")

	{
		got := built.Load()
		assert.Equal(t, int64(1), got, "factory ran %d times, want exactly 1", got)
	}

	assert.False(
		t,
		builtCfg.Load() == nil || builtCfg.Load().APIKey != "k",
		"factory did not receive the configured block: %+v",
		builtCfg.Load(),
	)
}

func TestGetCachesAndReplaysFactoryFailures(t *testing.T) {
	var attempts atomic.Int64

	registerFactory(t, "broken", func(domain.SourceConfig) (domain.ModSource, error) {
		attempts.Add(1)
		return nil, errors.New("no api key")
	})

	r := NewSourceRegistry()
	for i := 0; i < 3; i++ {
		_, err := r.Get("broken")
		require.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeSourceUnavailable),
			"attempt %d: got %v, want ErrCodeSourceUnavailable",
			i,
			err,
		)

		assert.Contains(
			t,
			err.Error(),
			"no api key",
			"attempt %d: error lost the factory's reason: %v",
			i,
			err,
		)
	}
	// One clear error per call, from one factory run: a factory that fails on
	// missing credentials would otherwise fail forever and slowly.
	{
		got := attempts.Load()
		assert.Equal(t, int64(1), got, "factory ran %d times, want 1", got)
	}

	// A later registration of a live source with the same id clears the failure.
	{
		err := r.Register(&fakeSource{id: "broken"})
		require.NoError(t, err)
	}

	{
		_, err := r.Get("broken")
		assert.NoError(t, err, "Get after a live registration: %v", err)
	}
}

func TestGetRejectsFactoryThatBuildsTheWrongID(t *testing.T) {
	registerFactory(t, "typo", func(domain.SourceConfig) (domain.ModSource, error) {
		// A copy-paste slip in the plugin: the source is unusable because every
		// later lookup goes through the id it was asked for.
		return &fakeSource{id: "typo2"}, nil
	})

	r := NewSourceRegistry()

	_, err := r.Get("typo")
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeSourceUnavailable),
		"got %v, want ErrCodeSourceUnavailable",
		err,
	)

	assert.Contains(t, err.Error(), "typo2", "error should name the mismatched id: %v", err)
	// The mismatched source must not be reachable under any id.
	{
		got := r.IDs()
		assert.Equal(t, "typo", strings.Join(got, ","), "IDs() = %v, want only [typo]", got)
	}
}

func TestGetRejectsFactoryReturningNothing(t *testing.T) {
	registerFactory(t, "void", func(domain.SourceConfig) (domain.ModSource, error) {
		return nil, nil
	})

	r := NewSourceRegistry()
	// A (nil, nil) factory is a plugin bug; it must not escape as a nil
	// interface that panics inside the caller.
	{
		_, err := r.Get("void")
		assert.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeSourceUnavailable),
			"got %v, want ErrCodeSourceUnavailable",
			err,
		)
	}

	{
		_, err := r.Get("void")
		assert.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeSourceUnavailable),
			"replayed failure: got %v, want ErrCodeSourceUnavailable",
			err,
		)
	}
}

func TestGetFallsBackToTheDefaultConfigWithoutAStore(t *testing.T) {
	var (
		seen   domain.SourceConfig
		called bool
	)

	registerFactory(t, "nostore", func(cfg domain.SourceConfig) (domain.ModSource, error) {
		called = true
		seen = cfg

		return &fakeSource{id: "nostore"}, nil
	})

	r := NewSourceRegistry()
	{
		_, err := r.Get("nostore")
		require.NoError(t, err, "Get: %v", err)
	}

	require.True(t, called, "factory never ran")
	// DefaultConfig has no "nostore" block, so the factory sees the zero value.
	assert.False(t, seen.Enabled, "factory received an unexpectedly enabled block")
}

func TestGetToleratesAnUnloadableConfig(t *testing.T) {
	registerFactory(t, "badcfg", func(domain.SourceConfig) (domain.ModSource, error) {
		return &fakeSource{id: "badcfg"}, nil
	})

	store := newFakeStore(func() *domain.Config { c := domain.DefaultConfig(); return &c }())
	store.loadErr = appErrors.New(appErrors.ErrCodeConfigInvalid, "broken json")

	r := NewSourceRegistryWithStore(store)
	// A corrupt config file must not make every source unreachable: the
	// factory runs with defaults and the config error surfaces elsewhere.
	{
		_, err := r.Get("badcfg")
		assert.NoError(t, err, "Get with an unloadable config: %v", err)
	}
}

func TestConcurrentGetBuildsOneSharedSource(t *testing.T) {
	var built atomic.Int64

	registerFactory(t, "racy", func(domain.SourceConfig) (domain.ModSource, error) {
		built.Add(1)
		return &fakeSource{id: "racy"}, nil
	})

	r := NewSourceRegistry()

	const workers = 32

	results := make([]domain.ModSource, workers)

	var wg sync.WaitGroup

	start := make(chan struct{})

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			<-start

			src, err := r.Get("racy")
			if err != nil {
				assert.NoError(t, err, "worker %d: %v", i, err)
				return
			}

			results[i] = src
		}(i)
	}

	close(start)
	wg.Wait()

	for i, src := range results {
		if src == nil {
			continue
		}

		require.Equal(
			t,
			results[0],
			src,
			"worker %d got a different source instance than worker 0",
			i,
		)
	}

	{
		got := built.Load()
		assert.Equal(t, int64(1), got, "factory ran %d times, want exactly 1", got)
	}
}

// --------------------------------------------------------------------- List

func TestListIsSortedByIDAndSkipsUnresolvableSources(t *testing.T) {
	r := NewSourceRegistry()

	for _, id := range []string{"steam", "github", "nexus"} {
		{
			err := r.Register(&fakeSource{id: id})
			require.NoError(t, err)
		}
	}

	registerFactory(t, "broken", func(domain.SourceConfig) (domain.ModSource, error) {
		return nil, errors.New("boom")
	})
	// A broken source must not remove the healthy ones from the list.
	_, _ = r.Get("broken")

	got := idsOf(r.List())

	want := []string{"github", "nexus", "steam"}
	assert.Equal(
		t,
		strings.Join(want, ","),
		strings.Join(got, ","),
		"List() = %v, want %v",
		got,
		want,
	)

	// The broken source is still *known*, so the settings screen can show it.
	assert.True(
		t,
		containsString(r.IDs(), "broken"),
		"IDs() = %v, want it to include the broken source",
		r.IDs(),
	)
}

func TestListIsEmptyNotNilForAFreshRegistry(t *testing.T) {
	r := NewSourceRegistry()

	got := r.List()
	assert.NotNil(t, got, "List() = nil, want an empty slice so JSON encodes as []")

	assert.Empty(t, got, "List() = %v, want empty", got)

	{
		ids := r.IDs()
		assert.False(t, ids == nil || len(ids) != 0, "IDs() = %v, want an empty slice", ids)
	}

	{
		infos := r.Infos()
		assert.False(t, infos == nil || len(infos) != 0, "Infos() = %v, want an empty slice", infos)
	}
}

func TestListPrefersRegisteredSourcesOverFactories(t *testing.T) {
	registerFactory(t, "both", func(domain.SourceConfig) (domain.ModSource, error) {
		return &fakeSource{id: "both", name: "from factory"}, nil
	})

	live := &fakeSource{id: "both", name: "registered"}

	r := NewSourceRegistry()
	{
		err := r.Register(live)
		require.NoError(t, err)
	}

	got := r.List()
	require.Len(t, got, 1, "List() = %v, want one entry", got)

	assert.Equal(
		t,
		"registered",
		got[0].Name(),
		"List() returned %q; an explicitly registered source must win over a factory",
		got[0].Name(),
	)
}

// -------------------------------------------------------------------- Infos

func TestInfosDescribesCapabilities(t *testing.T) {
	r := NewSourceRegistry()
	src := &fakeSource{id: "vortex", name: "Vortex Mods", desc: "d", version: "2"}
	src.auths = func(context.Context, domain.AuthCredentials) error { return nil }

	src.search = func(context.Context, domain.SearchQuery) ([]domain.Mod, error) { return nil, nil }
	{
		err := r.Register(src)
		require.NoError(t, err)
	}

	r.SetAuthenticated("vortex", true)

	infos := r.Infos()
	require.Len(t, infos, 1, "Infos() = %v, want one entry", infos)

	got := infos[0]
	assert.False(t, got.ID != "vortex" || got.Name != "Vortex Mods" || got.Description != "d" ||
		got.Version != "2", "unexpected identity fields: %+v", got)

	assert.False(
		t,
		!got.SupportsSearch || !got.SupportsAuth || !got.RequiresAPIKey,
		"capability flags lost: %+v",
		got,
	)

	assert.True(t, got.Enabled, "a registered source should be Enabled")

	assert.True(t, got.Authenticated, "recorded authentication state was not reported")
}

func TestInfoForUnresolvableSourceIsStillReturned(t *testing.T) {
	registerFactory(t, "down", func(domain.SourceConfig) (domain.ModSource, error) {
		return nil, errors.New("network down")
	})

	r := NewSourceRegistry()
	_, _ = r.Get("down")

	infos := r.Infos()
	require.False(
		t,
		len(infos) != 1 || infos[0].ID != "down",
		"Infos() = %v, want an entry for the failing source",
		infos,
	)
	// Present but plainly disabled: the UI needs to show why.
	assert.False(
		t,
		infos[0].Enabled || infos[0].Authenticated,
		"failing source reported as usable: %+v",
		infos[0],
	)
}

func TestInfoForBlankIDIsEmpty(t *testing.T) {
	r := NewSourceRegistry()

	for _, id := range []string{"", "  "} {
		{
			got := r.Info(id)
			assert.False(
				t,
				got.ID != "" || got.Enabled,
				"Info(%q) = %+v, want the zero value",
				id,
				got,
			)
		}
	}
}

func TestAuthenticatedIsFalseByDefaultAndForBlankIDs(t *testing.T) {
	r := NewSourceRegistry()
	assert.False(t, r.Authenticated("nothing"), "an unknown source reported as authenticated")

	r.SetAuthenticated("nothing", true)

	assert.True(t, r.Authenticated("nothing"), "SetAuthenticated did not take effect")

	r.SetAuthenticated("nothing", false)

	assert.False(t, r.Authenticated("nothing"), "SetAuthenticated(false) did not take effect")

	for _, id := range []string{"", "   "} {
		r.SetAuthenticated(id, true)

		assert.False(t, r.Authenticated(id), "blank id %q recorded authentication state", id)
	}
}

// ---------------------------------------------------------------- SearchAll

func TestSearchAllFansOutAcrossSources(t *testing.T) {
	r := NewSourceRegistry()
	a := &fakeSource{
		id: "a",
		search: func(_ context.Context, q domain.SearchQuery) ([]domain.Mod, error) {
			return []domain.Mod{{ID: "a1", SourceID: "a"}}, nil
		},
	}
	b := &fakeSource{
		id: "b",
		search: func(context.Context, domain.SearchQuery) ([]domain.Mod, error) {
			return []domain.Mod{{ID: "b1", SourceID: "b"}}, nil
		},
	}

	{
		err := r.Register(a)
		require.NoError(t, err)
	}

	{
		err := r.Register(b)
		require.NoError(t, err)
	}

	got, err := r.SearchAll(context.Background(), domain.SearchQuery{Query: "ui"})
	require.NoError(t, err, "SearchAll")

	require.Len(t, got, 2, "SearchAll returned %d mods, want 2: %+v", len(got), got)

	assert.False(
		t,
		a.searches.Load() != 1 || b.searches.Load() != 1,
		"search counts: a=%d b=%d, want 1 each",
		a.searches.Load(),
		b.searches.Load(),
	)
}

func TestSearchAllSkipsSourcesThatDoNotSupportIt(t *testing.T) {
	r := NewSourceRegistry()
	searchable := &fakeSource{
		id: "yes",
		search: func(context.Context, domain.SearchQuery) ([]domain.Mod, error) {
			return []domain.Mod{{ID: "m", SourceID: "yes"}}, nil
		},
	}
	// No search func, so SupportsSearch is false.
	{
		err := r.Register(&fakeSource{id: "no"})
		require.NoError(t, err)
	}

	{
		err := r.Register(searchable)
		require.NoError(t, err)
	}

	got, err := r.SearchAll(context.Background(), domain.SearchQuery{Query: "x"})
	require.NoError(t, err, "SearchAll")

	assert.False(
		t,
		len(got) != 1 || got[0].ID != "m",
		"SearchAll = %+v, want only the searchable source's result",
		got,
	)
}

func TestSearchAllFailsWhenNothingIsRegistered(t *testing.T) {
	r := NewSourceRegistry()
	{
		_, err := r.SearchAll(
			context.Background(),
			domain.SearchQuery{Query: "x"},
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeSourceUnavailable,
		), "empty registry: got %v, want ErrCodeSourceUnavailable", err)
	}
}

func TestSearchAllFailsWhenNoSourceSupportsSearch(t *testing.T) {
	r := NewSourceRegistry()
	{
		err := r.Register(&fakeSource{id: "local"})
		require.NoError(t, err)
	}

	_, err := r.SearchAll(context.Background(), domain.SearchQuery{Query: "x"})
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeSearchFailed),
		"got %v, want ErrCodeSearchFailed",
		err,
	)
}

func TestSearchAllToleratesOneFailingSource(t *testing.T) {
	r := NewSourceRegistry()
	good := &fakeSource{
		id: "good",
		search: func(context.Context, domain.SearchQuery) ([]domain.Mod, error) {
			return []domain.Mod{{ID: "found", SourceID: "good"}}, nil
		},
	}
	bad := &fakeSource{
		id: "bad",
		search: func(context.Context, domain.SearchQuery) ([]domain.Mod, error) {
			return nil, errors.New("gateway timeout")
		},
	}

	{
		err := r.Register(good)
		require.NoError(t, err)
	}

	{
		err := r.Register(bad)
		require.NoError(t, err)
	}

	got, err := r.SearchAll(context.Background(), domain.SearchQuery{Query: "x"})
	require.NoError(t, err, "one failing source should not fail the whole search")

	assert.False(
		t,
		len(got) != 1 || got[0].ID != "found",
		"SearchAll = %+v, want the healthy source's result",
		got,
	)
}

func TestSearchAllAcceptsANilContext(t *testing.T) {
	r := NewSourceRegistry()
	//nolint:staticcheck // A nil context is exactly what is under test.
	{
		err := r.Register(
			&fakeSource{
				id: "a",
				search: func(context.Context, domain.SearchQuery) ([]domain.Mod, error) {
					return nil, nil
				},
			},
		)
		require.NoError(t, err)
	}

	//nolint:staticcheck // A nil context is exactly what is under test.
	{
		_, err := r.SearchAll(nil, domain.SearchQuery{Query: "x"})
		assert.NoError(t, err, "SearchAll(nil ctx): %v", err)
	}
}

// ------------------------------------------------------- authentication flow

func TestAuthenticateForwardsCredentialsAndRecordsSuccess(t *testing.T) {
	src := &fakeSource{id: "vortex"}
	src.auths = func(context.Context, domain.AuthCredentials) error { return nil }

	r := NewSourceRegistry()
	{
		err := r.Register(src)
		require.NoError(t, err)
	}

	creds := domain.AuthCredentials{"api_key": " secret "}
	{
		err := r.Authenticate(context.Background(), "VORTEX", creds)
		require.NoError(t, err, "Authenticate: %v", err)
	}

	got := src.lastCreds()
	{
		v, ok := got.Get("api_key")
		assert.False(t, !ok || v != "secret", "credentials were not forwarded: %+v", got)
	}

	assert.True(t, r.Authenticated("vortex"), "successful authentication was not recorded")

	assert.True(
		t,
		r.Info("vortex").Authenticated,
		"Info did not report the recorded authentication",
	)
}

func TestAuthenticateRecordsFailure(t *testing.T) {
	src := &fakeSource{id: "vortex"}

	src.auths = func(context.Context, domain.AuthCredentials) error { return nil }
	{
		err := src.Authenticate(context.Background(), nil)
		require.NoError(t, err)
	}

	r := NewSourceRegistry()
	{
		err := r.Register(src)
		require.NoError(t, err)
	}

	src.setAuthErr(errors.New("invalid key"))

	err := r.Authenticate(context.Background(), "vortex", nil)
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeAuthFailed),
		"got %v, want ErrCodeAuthFailed",
		err,
	)

	assert.Contains(t, err.Error(), "invalid key", "error lost the source's reason: %v", err)
	// A failure must clear a previously successful state, or the UI would keep
	// showing a green badge for a source that no longer works.
	assert.False(
		t,
		r.Authenticated("vortex"),
		"failed authentication left the source marked authenticated",
	)
}

func TestAuthenticateRejectsUnknownSourceAndUnsupportedSources(t *testing.T) {
	r := NewSourceRegistry()
	{
		err := r.Authenticate(
			context.Background(),
			"nope",
			nil,
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeSourceNotFound,
		), "unknown source: got %v, want ErrCodeSourceNotFound", err)
	}

	// A local-folder source has nothing to authenticate against; asking it to
	// is a caller mistake and says so.
	{
		err := r.Register(&fakeSource{id: "local"})
		require.NoError(t, err)
	}

	{
		err := r.Authenticate(
			context.Background(),
			"local",
			nil,
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeAuthRequired,
		), "source without auth support: got %v, want ErrCodeAuthRequired", err)
	}
}

func TestAuthenticateAcceptsANilContext(t *testing.T) {
	r := NewSourceRegistry()
	src := &fakeSource{id: "vortex"}

	src.auths = func(context.Context, domain.AuthCredentials) error { return nil }
	{
		err := r.Register(src)
		require.NoError(t, err)
	}
	//nolint:staticcheck // A nil context is exactly what is under test.
	{
		err := r.Authenticate(nil, "vortex", nil)
		assert.NoError(t, err, "Authenticate(nil ctx): %v", err)
	}
}

func TestValidateCredentialsSkipsSourcesWithoutAuth(t *testing.T) {
	r := NewSourceRegistry()

	src := &fakeSource{id: "local"}
	{
		err := r.Register(src)
		require.NoError(t, err)
	}

	{
		err := r.ValidateCredentials(context.Background(), "local")
		assert.NoError(t, err, "a source with no credentials to validate should succeed: %v", err)
	}

	assert.Equal(
		t,
		int64(0),
		src.calls.valid.Load(),
		"ValidateCredentials was called on a source that has no auth",
	)
}

func TestValidateCredentialsReportsSourceFailures(t *testing.T) {
	src := &fakeSource{id: "vortex"}
	src.auths = func(context.Context, domain.AuthCredentials) error { return nil }
	src.authed = func(context.Context) error { return errors.New("token expired") }

	r := NewSourceRegistry()
	{
		err := r.Register(src)
		require.NoError(t, err)
	}

	err := r.ValidateCredentials(context.Background(), "vortex")
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeAuthFailed),
		"got %v, want ErrCodeAuthFailed",
		err,
	)

	assert.Contains(t, err.Error(), "token expired", "error lost the source's reason: %v", err)
}

func TestValidateCredentialsPropagatesUnknownSource(t *testing.T) {
	r := NewSourceRegistry()
	{
		err := r.ValidateCredentials(
			context.Background(),
			"nope",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeSourceNotFound,
		), "got %v, want ErrCodeSourceNotFound", err)
	}
	//nolint:staticcheck // A nil context is exactly what is under test.
	{
		err := r.ValidateCredentials(
			nil,
			"nope",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeSourceNotFound,
		), "nil ctx changed the error: %v", err)
	}
}

// ---------------------------------------------------------- enabled sources

func TestEnabledSourcesHonoursTheConfigFlag(t *testing.T) {
	cfg := domain.DefaultConfig()
	cfg.SetSource("steam", domain.SourceConfig{Enabled: false})
	cfg.SetSource("local", domain.SourceConfig{Enabled: true})
	// "absent" has no block at all.
	r := NewSourceRegistryWithStore(newFakeStore(&cfg))

	for _, id := range []string{"steam", "local", "absent"} {
		{
			err := r.Register(&fakeSource{id: id})
			require.NoError(t, err)
		}
	}

	got := idsOf(r.EnabledSources())

	want := []string{"absent", "local"}
	assert.Equal(
		t,
		strings.Join(want, ","),
		strings.Join(got, ","),
		"EnabledSources() = %v, want %v",
		got,
		want,
	)
}

func TestEnabledSourcesUsesDefaultsWithoutAStore(t *testing.T) {
	r := NewSourceRegistry()
	{
		err := r.Register(&fakeSource{id: "steam"})
		require.NoError(t, err)
	}
	// With no store every source counts as enabled; a fresh install should show
	// its bundled sources without needing a config file.
	{
		got := idsOf(r.EnabledSources())
		assert.Len(t, got, 1, "EnabledSources() = %v, want [steam]", got)
	}
}

// -------------------------------------------------------------------- helpers

func idsOf(sources []domain.ModSource) []string {
	out := make([]string, 0, len(sources))
	for _, s := range sources {
		out = append(out, s.ID())
	}

	return out
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}

	return false
}

// A source the user switched off is deliberately never built, so reporting it as
// enabled would show a green tick beside a source the manager will not use.
func TestInfoReportsTheConfigurationRatherThanTheBuild(t *testing.T) {
	registerFactory(t, "switchable", func(domain.SourceConfig) (domain.ModSource, error) {
		return &fakeSource{id: "switchable"}, nil
	})

	cfg := domain.DefaultConfig()
	cfg.Sources["switchable"] = domain.SourceConfig{Enabled: false}

	on := NewSourceRegistryWithStore(newFakeStore(&cfg))
	{
		got := on.Info("switchable")
		assert.False(
			t,
			got.Enabled,
			"a source disabled in the configuration is reported as enabled",
		)
	}

	cfg.Sources["switchable"] = domain.SourceConfig{Enabled: true}

	off := NewSourceRegistryWithStore(newFakeStore(&cfg))
	{
		got := off.Info("switchable")
		assert.True(
			t,
			got.Enabled,
			"a source switched on in the configuration is reported as disabled",
		)
	}
	// A block nobody wrote counts as enabled: a fresh install has no blocks at
	// all, and a manager that started with no sources would look broken.
	fresh := NewSourceRegistryWithStore(newFakeStore(&domain.Config{}))
	{
		got := fresh.Info("switchable")
		assert.True(t, got.Enabled, "a source with no configuration block is reported as disabled")
	}
	// A source that cannot be produced is not enabled in any sense, whatever the
	// configuration says, so that the settings screen can show why.
	// TestInfoForUnresolvableSourceIsStillReturned covers that half.
}

// ------------------------------------------------------- stored credentials

// forgettingSource is a fakeSource that can also drop its credential. It is a
// separate type rather than a field on fakeSource so that "this source can
// forget" stays a property of the plugin rather than something every test double
// quietly has.
type forgettingSource struct {
	*fakeSource
	forgotten atomic.Int64
}

func (f *forgettingSource) ForgetCredentials() { f.forgotten.Add(1) }

// TestInfoReportsAStoredKeyFromTheConfiguration: whether a key is stored is a
// question about the settings file. Answering it from the source object would
// report the key of the running process only, so the same installation would
// show a stored key in the CLI and none in the GUI.
func TestInfoReportsAStoredKeyFromTheConfiguration(t *testing.T) {
	withKey := domain.DefaultConfig()
	withKey.Sources["nexus"] = domain.SourceConfig{Enabled: true, APIKey: "  stored-key  "}
	// The block is written by whoever saved the key, so it may have been spelled
	// with a capital letter. Reading has to fold the id or the answer depends on
	// which surface asked.
	withKey.Sources["GitHub"] = domain.SourceConfig{Enabled: true, APIKey: "gh-key"}

	blank := domain.DefaultConfig()
	blank.Sources["nexus"] = domain.SourceConfig{Enabled: true, APIKey: "   "}

	tests := []struct {
		name string
		cfg  *domain.Config
		id   string
		want bool
	}{
		{name: "stored key", cfg: &withKey, id: "nexus", want: true},
		{name: "blank key is not a key", cfg: &blank, id: "nexus", want: false},
		{name: "no block", cfg: &domain.Config{}, id: "nexus", want: false},
		{name: "folded id", cfg: &withKey, id: "github", want: true},
		{name: "someone else's key", cfg: &withKey, id: "nexus-extra", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewSourceRegistryWithStore(newFakeStore(tc.cfg))
			{
				err := r.Register(&fakeSource{id: tc.id})
				require.NoError(t, err)
			}

			{
				got := r.Info(tc.id).HasAPIKey
				assert.Equal(
					t,
					tc.want,
					got,
					"Info(%q).HasAPIKey = %v, want %v",
					tc.id,
					got,
					tc.want,
				)
			}
		})
	}
}

func TestForgetCredentialsClearsTheLiveSourceAndTheRecordedState(t *testing.T) {
	src := &forgettingSource{fakeSource: &fakeSource{
		id:    "nexus",
		auths: func(context.Context, domain.AuthCredentials) error { return nil },
	}}

	r := NewSourceRegistry()
	{
		err := r.Register(src)
		require.NoError(t, err)
	}
	// The registry caches the built source for the life of the process, so a
	// forgotten key has to be dropped from that object too: clearing the file
	// alone would leave the key in memory until the app was restarted.
	{
		err := r.Authenticate(
			t.Context(),
			"nexus",
			domain.AuthCredentials{"api_key": "k"},
		)
		require.NoError(t, err)
	}

	require.True(t, r.Authenticated("nexus"), "authentication was not recorded")

	for range 2 {
		{
			err := r.ForgetCredentials("nexus")
			require.NoError(t, err, "ForgetCredentials() = %v, want nil", err)
		}
	}

	{
		got := src.forgotten.Load()
		assert.Equal(
			t,
			int64(2),
			got,
			"forgotten %d times, want 2 (forgetting twice must be harmless)",
			got,
		)
	}

	assert.False(
		t,
		r.Authenticated("nexus"),
		"a forgotten source is still reported as authenticated",
	)
}

func TestForgetCredentialsRefusesWhatItCannotForget(t *testing.T) {
	authOnly := &fakeSource{id: "legacy"}
	authOnly.auths = func(context.Context, domain.AuthCredentials) error { return nil }

	tests := []struct {
		name string
		id   string
		want appErrors.Code
	}{
		{name: "blank id", id: "  ", want: appErrors.ErrCodeInvalidArgument},
		{name: "unknown source", id: "nope", want: appErrors.ErrCodeSourceNotFound},
		{name: "source without authentication", id: "plain", want: appErrors.ErrCodeAuthRequired},
		{name: "source that cannot forget", id: "legacy", want: appErrors.ErrCodeInvalidArgument},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewSourceRegistry()
			{
				err := r.Register(&stubSource{id: "plain"})
				require.NoError(t, err)
			}

			{
				err := r.Register(authOnly)
				require.NoError(t, err)
			}

			err := r.ForgetCredentials(tc.id)
			{
				code := appErrors.CodeOf(err)
				assert.Equal(
					t,
					tc.want,
					code,
					"ForgetCredentials(%q) code = %q, want %q (err %v)",
					tc.id,
					code,
					tc.want,
					err,
				)
			}
		})
	}
}
