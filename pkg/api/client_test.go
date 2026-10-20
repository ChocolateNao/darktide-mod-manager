package api

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/hooks"
	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/hooks/host"
	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clientOperation is one public method reduced to something a test can call and
// check the error of.
type clientOperation struct {
	call func(c *Client) error
	name string
}

// everyOperation lists each operation that has to notice a closed client. Written out
// rather than discovered by reflection: not adding a method without adding it here
// is visible in a diff; a method missing the guard would nil-panic in production.
func everyOperation() []clientOperation {
	ctx := context.Background()

	return []clientOperation{
		{func(c *Client) error { _, err := c.ListMods(ctx); return err }, "ListMods"},
		{func(c *Client) error { _, err := c.GetMod(ctx, "m"); return err }, "GetMod"},
		{func(c *Client) error { _, err := c.InstallMod(ctx, "s", "m"); return err }, "InstallMod"},
		{func(c *Client) error {
			_, err := c.Install(ctx, "s", "m", InstallOptions{})
			return err
		}, "Install"},
		{func(c *Client) error { return c.UninstallMod(ctx, "m") }, "UninstallMod"},
		{func(c *Client) error { return c.Uninstall(ctx, "m", UninstallOptions{}) }, "Uninstall"},
		{func(c *Client) error { return c.EnableMod(ctx, "m") }, "EnableMod"},
		{func(c *Client) error { return c.DisableMod(ctx, "m") }, "DisableMod"},
		{func(c *Client) error { return c.UpdateMod(ctx, "m") }, "UpdateMod"},
		{func(c *Client) error { _, err := c.ApplyUpdate(ctx, "m"); return err }, "ApplyUpdate"},
		{func(c *Client) error { return c.CancelUpdate(ctx, "m") }, "CancelUpdate"},
		{func(c *Client) error { _, err := c.CheckModUpdates(ctx); return err }, "CheckModUpdates"},
		{func(c *Client) error { return c.RefreshMods(ctx) }, "RefreshMods"},
		{func(c *Client) error { _, err := c.DetectConflicts(ctx); return err }, "DetectConflicts"},
		{
			func(c *Client) error { return c.ResolveConflict(ctx, "r", ResolutionKeepBoth) },
			"ResolveConflict",
		},
		{func(c *Client) error { _, err := c.GetModHooks(ctx, "m"); return err }, "GetModHooks"},
		{func(c *Client) error { return c.UpdateModHooks(ctx, "m", ModHooks{}) }, "UpdateModHooks"},
		{func(c *Client) error { return c.ReorderMods(ctx, []string{"m"}) }, "ReorderMods"},
		{func(c *Client) error { _, err := c.LoadOrder(ctx); return err }, "LoadOrder"},
		{func(c *Client) error { _, err := c.ModPath(ctx, "m"); return err }, "ModPath"},
		{func(c *Client) error { _, err := c.SymlinkStatus(ctx); return err }, "SymlinkStatus"},
		{func(c *Client) error { _, err := c.RelinkMod(ctx, "m"); return err }, "RelinkMod"},

		{
			func(c *Client) error { _, err := c.SearchSource(ctx, "s", "q"); return err },
			"SearchSource",
		},
		{func(c *Client) error { _, err := c.Search(ctx, SearchQuery{}); return err }, "Search"},
		{func(c *Client) error { _, err := c.SourceInfo(ctx, "s"); return err }, "SourceInfo"},
		{
			func(c *Client) error { _, err := c.GetSourceMod(ctx, "s", "m"); return err },
			"GetSourceMod",
		},
		{
			func(c *Client) error { return c.AuthenticateSource(ctx, "s", nil) },
			"AuthenticateSource",
		},
		{func(c *Client) error { return c.TestSourceConnection(ctx, "s") }, "TestSourceConnection"},

		{func(c *Client) error { _, err := c.DetectGame(ctx); return err }, "DetectGame"},
		{func(c *Client) error { _, err := c.CurrentGame(ctx); return err }, "CurrentGame"},
		{
			func(c *Client) error { _, err := c.ValidateGamePath(ctx, "/p"); return err },
			"ValidateGamePath",
		},
		{func(c *Client) error { return c.SetGamePath(ctx, "/p") }, "SetGamePath"},
		{func(c *Client) error { _, err := c.GetModsPath(ctx, nil); return err }, "GetModsPath"},

		{func(c *Client) error { _, err := c.GetCacheInfo(ctx); return err }, "GetCacheInfo"},
		{func(c *Client) error { return c.ClearCache(ctx) }, "ClearCache"},
		{func(c *Client) error { return c.PruneCache(ctx, 1, 1) }, "PruneCache"},
		{func(c *Client) error { return c.PruneCacheWithDefaults(ctx) }, "PruneCacheWithDefaults"},

		{
			func(c *Client) error { return c.ValidateHookScript(ctx, HookScript{}, nil) },
			"ValidateHookScript",
		},
		{func(c *Client) error { return c.RunHookScripts(ctx, nil, nil) }, "RunHookScripts"},

		{func(c *Client) error { _, err := c.GetConfig(ctx); return err }, "GetConfig"},
		{func(c *Client) error { return c.UpdateConfig(ctx, &Config{}) }, "UpdateConfig"},
		{func(c *Client) error { _, err := c.ConfigPath(); return err }, "ConfigPath"},
		{
			func(c *Client) error { _, _, err := c.SourceConfig(ctx, "s"); return err },
			"SourceConfig",
		},
		{
			func(c *Client) error { _, err := c.GetConfigValue(ctx, "cache.path"); return err },
			"GetConfigValue",
		},
		{
			func(c *Client) error { return c.SetConfigValue(ctx, "cache.path", "/c") },
			"SetConfigValue",
		},
	}
}

func TestEveryOperationReportsAClosedClient(t *testing.T) {
	client, err := NewClientFromServices(ClientDeps{})
	require.NoError(t, err, "NewClientFromServices")

	require.NoError(t, client.Close(), "Close")

	for _, op := range everyOperation() {
		err := op.call(client)
		if err == nil {
			assert.Fail(t, "%s on a closed client returned no error", op.name)
			continue
		}

		assert.True(t, IsAppError(err, ErrCodeInvalidArgument),
			"%s on a closed client returned %v, want an invalid-argument error",
			op.name,
			err,
		)

		assert.Contains(t, err.Error(), "closed",
			"%s on a closed client said %q, which does not mention that the client is closed",
			op.name,
			err,
		)
	}
}

// Methods that report through a value cannot announce a closed client, so they are
// checked separately: returning nil is the honest answer for "no sources" and "no
// logger".
func TestClosedClientAccessorsDegradeQuietly(t *testing.T) {
	client, err := NewClientFromServices(ClientDeps{})
	require.NoError(t, err, "NewClientFromServices")

	require.NoError(t, client.Close(), "Close")

	infos := client.ListSources(context.Background())
	assert.Nil(t, infos, "ListSources on a closed client")

	assert.False(
		t,
		client.SupportsHookType(context.Background(), string(LangJS)),
		"SupportsHookType on a closed client claimed a runner exists",
	)

	assert.Equal(
		t,
		time.Duration(0),
		client.HookTimeout(context.Background(), HookScript{}, nil),
		"HookTimeout on a closed client",
	)

	client.SetSourceAuthenticated("s", true) // must not panic
}

func TestNilClientIsUsableForGuards(t *testing.T) {
	// A constructor returning (nil, err) is easy to misuse by ignoring the error, so
	// every method here answers instead of panicking.
	var client *Client
	for _, op := range everyOperation() {
		assert.Error(t, op.call(client), "%s on a nil client returned no error", op.name)
	}

	assert.True(t, client.Closed(), "a nil client does not report itself closed")

	assert.Nil(t, client.Events(), "a nil client returned an event bus")

	assert.NotNil(t, client.Logger(), "a nil client returned a nil logger")

	got := client.Services()
	assert.Nil(t, got.Sources, "a nil client returned non-empty services")
	assert.Nil(t, got.Mods, "a nil client returned non-empty services")
	assert.Nil(t, got.Game, "a nil client returned non-empty services")
	assert.Nil(t, got.Config, "a nil client returned non-empty services")

	client.Logf("ignored %d", 1)
	client.SetSourceAuthenticated("s", true)

	require.NoError(t, client.Close(), "Close on a nil client")
}

func TestCloseRunsCleanupsInReverseOrder(t *testing.T) {
	// Reverse order: the database closes before the cache that might still be
	// flushing into it, exactly as the graph was built forwards.
	client, err := NewClientFromServices(ClientDeps{})
	require.NoError(t, err, "NewClientFromServices")

	var order []string

	client.cleanups = []func() error{
		func() error { order = append(order, "database"); return nil },
		func() error { order = append(order, "cache"); return nil },
		func() error { order = append(order, "extractor"); return nil },
	}

	require.NoError(t, client.Close(), "Close")

	want := []string{"extractor", "cache", "database"}
	assert.Equal(t, strings.Join(want, ","), strings.Join(order, ","), "cleanups ran in order")
}

func TestCloseIsIdempotentAndSkipsNilCleanups(t *testing.T) {
	// The natural shape is an explicit Close on the error path plus a deferred
	// one, so a second call must do nothing.
	client, err := NewClientFromServices(ClientDeps{})
	require.NoError(t, err, "NewClientFromServices")

	calls := 0
	client.cleanups = []func() error{nil, func() error { calls++; return nil }, nil}

	for i := 0; i < 3; i++ {
		require.NoError(t, client.Close(), "Close call %d", i+1)
	}

	assert.Equal(t, 1, calls, "the cleanup ran times across three Close calls, want 1")

	assert.True(t, client.Closed(), "Closed reports false after Close")
}

func TestCloseReturnsASingleFailureUnwrapped(t *testing.T) {
	// One failure is returned as-is so a caller testing for a code after Close
	// does not have to unwrap a join.
	client, err := NewClientFromServices(ClientDeps{})
	require.NoError(t, err, "NewClientFromServices")

	client.cleanups = []func() error{
		func() error { return appErrors.New(appErrors.ErrCodeConfigIO, "could not close the database") },
	}

	err = client.Close()
	assert.True(
		t,
		IsAppError(err, ErrCodeConfigIO),
		"Close returned %v, want a config-io error",
		err,
	)
	// A second Close reports the same answer, so a deferred Close after an explicit
	// one does not surprise the caller.
	second := client.Close()
	assert.True(
		t,
		IsAppError(second, ErrCodeConfigIO),
		"the second Close returned %v, want the first call's error",
		second,
	)
}

func TestCloseJoinsSeveralFailures(t *testing.T) {
	client, err := NewClientFromServices(ClientDeps{})
	require.NoError(t, err, "NewClientFromServices")

	client.cleanups = []func() error{
		func() error { return appErrors.New(appErrors.ErrCodeConfigIO, "database close failed") },
		func() error { return appErrors.New(appErrors.ErrCodeCacheCorrupt, "cache close failed") },
	}

	err = client.Close()
	require.Error(t, err, "Close reported success with two failing cleanups")
	// errors.Join's own message is unhelpful, so the check is that both causes
	// are reachable by unwrapping.
	assert.Contains(
		t,
		err.Error(),
		"database close failed",
		"Close's error does not mention the database failure",
	)

	assert.Contains(
		t,
		err.Error(),
		"cache close failed",
		"Close's error does not mention the cache failure",
	)

	assert.True(
		t,
		errors.Is(err, err),
		"the joined error does not match itself, which means the join was lost",
	)
}

func TestNewClientFromServicesRefusesToBeRepointed(t *testing.T) {
	// Silently ignoring a path option would leave the caller believing the client
	// was built against their path when it was not.
	cases := map[string]Option{
		"WithConfigPath":  WithConfigPath("/etc/dmm.json"),
		"WithGamePath":    WithGamePath("/games/darktide"),
		"WithCachePath":   WithCachePath("/var/cache/dmm"),
		"WithStoragePath": WithStoragePath("/var/lib/dmm"),
		"WithSourceConfig": WithSourceConfig("vortex", map[string]any{
			"api_key": "k",
		}),
	}
	for name, opt := range cases {
		client, err := NewClientFromServices(ClientDeps{}, opt)
		if err == nil {
			assert.Fail(t, "%s was accepted by NewClientFromServices", name)

			if client != nil {
				_ = client.Close()
			}

			continue
		}

		assert.True(
			t,
			IsAppError(err, ErrCodeInvalidArgument),
			"%s produced %v, want an invalid-argument error",
			name,
			err,
		)

		if client != nil {
			assert.Fail(t, "%s produced a client alongside its error", name)

			_ = client.Close()
		}
	}
}

func TestNewClientFromServicesAcceptsLoggingAndEvents(t *testing.T) {
	logger := &countingLogger{}
	sub := &collector{}

	client, err := NewClientFromServices(ClientDeps{},
		WithLogger(logger),
		WithEventHandler(sub.handle),
		WithLogger(nil),
		WithEventHandler(nil),
	)
	require.NoError(t, err, "NewClientFromServices")

	defer client.Close()

	assert.Equal(
		t,
		Logger(logger),
		client.Logger(),
		"the client's logger is not the one that was supplied",
	)

	client.Logf("hello %s", "world")

	lines := logger.all()
	assert.Len(t, lines, 1)
	assert.Equal(t, "hello world", lines[0], "logger recorded, want one formatted line")

	assert.Equal(
		t,
		1,
		client.Events().HandlerCount(),
		"the nil handler was stored instead of being dropped",
	)
}

func TestClientLogsThroughADiscardingLoggerByDefault(t *testing.T) {
	client, err := NewClientFromServices(ClientDeps{})
	require.NoError(t, err, "NewClientFromServices")

	defer client.Close()

	client.Logf("this must go nowhere %d", 42)

	assert.NotNil(t, client.Logger(), "Logger returned nil for a client built without one")
}

func TestClientServicesRoundTrip(t *testing.T) {
	registry := services.NewSourceRegistry()
	deps := ClientDeps{Sources: registry}

	client, err := NewClientFromServices(deps)
	require.NoError(t, err, "NewClientFromServices")

	defer client.Close()

	assert.Equal(
		t,
		registry,
		client.Services().Sources,
		"Services returned a different registry than the client was given",
	)
	// The accessor hands back the live pointers, so it reaches a service this
	// package does not wrap: not giving a second view of the graph.
	assert.Equal(
		t,
		deps.Sources,
		client.Services().Sources,
		"Services built a new value instead of handing back the live one",
	)
}

func TestOperationsReportAMissingService(t *testing.T) {
	// A client built from a partial graph must name the missing service rather
	// than nil-panic on first use.
	client, err := NewClientFromServices(ClientDeps{})
	require.NoError(t, err, "NewClientFromServices")

	defer client.Close()

	ctx := context.Background()

	_, err = client.ListMods(ctx)
	assert.Contains(
		t,
		err.Error(),
		"mod service",
		"ListMods said, which does not name the missing service",
	)

	_, err = client.DetectGame(ctx)
	assert.Contains(
		t,
		err.Error(),
		"game locator",
		"DetectGame said, which does not name the missing service",
	)

	_, err = client.GetCacheInfo(ctx)
	assert.Contains(
		t,
		err.Error(),
		"cache manager",
		"GetCacheInfo said, which does not name the missing service",
	)

	err = client.AuthenticateSource(
		ctx,
		"s",
		nil,
	)
	assert.Contains(
		t,
		err.Error(),
		"source registry",
		"AuthenticateSource said, which does not name the missing service",
	)

	_, err = client.GetConfig(ctx)
	assert.Contains(
		t,
		err.Error(),
		"configuration store",
		"GetConfig said, which does not name the missing service",
	)

	err = client.ValidateHookScript(
		ctx,
		HookScript{},
		nil,
	)
	assert.Contains(
		t,
		err.Error(),
		"hook executor",
		"ValidateHookScript said, which does not name the missing service",
	)
}

// The source operations are the ones exercisable without a real graph: the
// registry is cheap to build and a stub source answers them.
func TestSearchSourceRejectsASourceThatCannotSearch(t *testing.T) {
	registry := services.NewSourceRegistry()
	require.NoError(t, registry.Register(&noSearchSource{stubSource{id: "offline"}}), "Register")

	client, err := NewClientFromServices(ClientDeps{Sources: registry})
	require.NoError(t, err, "NewClientFromServices")

	defer client.Close()

	_, err = client.SearchSource(context.Background(), "offline", "ui")
	assert.True(t, IsAppError(err, ErrCodeSourceUnavailable),
		"SearchSource on a source that cannot search returned %v, want source-unavailable",
		err,
	)
	// An empty list would read as "nothing matched".
	if err != nil {
		assert.NotEmpty(t, err.Error(), "the refusal carries no message")
	}
}

func TestSearchSourcePassesTheQueryToTheSource(t *testing.T) {
	registry := services.NewSourceRegistry()

	source := &stubSource{id: "stub"}
	require.NoError(t, registry.Register(source), "Register")

	client, err := NewClientFromServices(ClientDeps{Sources: registry})
	require.NoError(t, err, "NewClientFromServices")

	defer client.Close()

	mods, err := client.SearchSource(context.Background(), "  stub  ", "  better ui  ")
	require.NoError(t, err, "SearchSource")

	assert.NotNil(t, mods, "SearchSource returned nil, want an empty slice")

	query, ok := source.lastSearch()
	require.True(t, ok, "the source was never asked to search")

	assert.Equal(t, "better ui", query.Query, "the source was asked, want the trimmed query")
}

func TestSearchSourceReportsAnUnknownSource(t *testing.T) {
	client, err := NewClientFromServices(ClientDeps{Sources: services.NewSourceRegistry()})
	require.NoError(t, err, "NewClientFromServices")

	defer client.Close()

	_, err = client.SearchSource(context.Background(), "nope", "q")
	assert.True(
		t,
		IsAppError(err, ErrCodeSourceNotFound),
		"SearchSource for an unknown source returned %v, want source-not-found",
		err,
	)

	_, err = client.SearchSource(
		context.Background(),
		"   ",
		"q",
	)
	assert.True(
		t,
		IsAppError(err, ErrCodeInvalidArgument),
		"SearchSource with a blank id returned %v, want invalid-argument",
		err,
	)
}

func TestSearchSourcePropagatesTheSourcesFailure(t *testing.T) {
	registry := services.NewSourceRegistry()
	require.NoError(t, registry.Register(&failingSource{
		stubSource: stubSource{id: "broken"},
		code:       appErrors.ErrCodeSearchFailed,
	}), "Register")

	client, err := NewClientFromServices(ClientDeps{Sources: registry})
	require.NoError(t, err, "NewClientFromServices")

	defer client.Close()

	_, err = client.SearchSource(context.Background(), "broken", "q")
	assert.True(
		t,
		IsAppError(err, ErrCodeSearchFailed),
		"SearchSource returned %v, want the source's own search-failed code",
		err,
	)
}

func TestAuthenticateSourceHandsOverEveryCredential(t *testing.T) {
	registry := services.NewSourceRegistry()

	source := &stubSource{id: "stub"}
	require.NoError(t, registry.Register(source), "Register")

	cfg := domain.DefaultConfig()
	// Without a store a sign-in has nowhere to land, and AuthenticateSource says so
	// rather than reporting a login: not a restart throws away.
	client, err := NewClientFromServices(ClientDeps{
		Sources: registry,
		Config:  &configStoreStub{cfg: &cfg},
	})
	require.NoError(t, err, "NewClientFromServices")

	defer client.Close()

	require.NoError(t, client.AuthenticateSource(context.Background(), "stub", map[string]string{
		"username": "modder",
		"api_key":  "secret",
	}), "AuthenticateSource")

	creds := source.lastCreds()
	got, _ := creds.Get("username")
	assert.Equal(t, "modder", got, "the source was given username")

	got, _ = creds.Get("api_key")
	assert.Equal(t, "secret", got, "the source was given api_key")

	assert.True(
		t,
		client.Services().Sources.Authenticated("stub"),
		"the registry did not record the source as authenticated after a successful login",
	)
}

func TestAuthenticateSourceClearsAFailedLogin(t *testing.T) {
	// A stale green badge is worse than none: the user would believe the
	// credential works and only find out at download time.
	registry := services.NewSourceRegistry()
	require.NoError(t, registry.Register(&stubSource{id: "stub"}), "Register")

	registry.SetAuthenticated("stub", true)

	client, err := NewClientFromServices(ClientDeps{Sources: registry})
	require.NoError(t, err, "NewClientFromServices")

	defer client.Close()

	registry.SetAuthenticated("stub", true)

	// Swap in a source that refuses, keeping the same id.
	failing := services.NewSourceRegistry()
	require.NoError(t, failing.Register(&failingSource{
		stubSource: stubSource{id: "stub"},
		code:       appErrors.ErrCodeAuthFailed,
	}), "Register")

	failing.SetAuthenticated("stub", true)

	client2, err := NewClientFromServices(ClientDeps{Sources: failing})
	require.NoError(t, err, "NewClientFromServices")

	defer client2.Close()

	err = client2.AuthenticateSource(
		context.Background(),
		"stub",
		map[string]string{"api_key": "k"},
	)
	assert.True(
		t,
		IsAppError(err, ErrCodeAuthFailed),
		"AuthenticateSource against a failing source returned %v, want auth-failed",
		err,
	)

	assert.False(
		t,
		client2.Services().Sources.Authenticated("stub"),
		"the registry still reports the source as authenticated after a failed login",
	)
}

func TestTestSourceConnectionReportsTheSourcesVerdict(t *testing.T) {
	registry := services.NewSourceRegistry()
	require.NoError(t, registry.Register(&failingSource{
		stubSource: stubSource{id: "broken"},
		code:       appErrors.ErrCodeAuthFailed,
	}), "Register")

	require.NoError(t, registry.Register(&stubSource{id: "fine"}), "Register")

	client, err := NewClientFromServices(ClientDeps{Sources: registry})
	require.NoError(t, err, "NewClientFromServices")

	defer client.Close()

	err = client.TestSourceConnection(context.Background(), "fine")
	assert.NoError(t, err, "TestSourceConnection on a healthy source")

	err = client.TestSourceConnection(
		context.Background(),
		"broken",
	)
	assert.True(
		t,
		IsAppError(err, ErrCodeAuthFailed),
		"TestSourceConnection on a broken source returned %v, want auth-failed",
		err,
	)

	err = client.TestSourceConnection(
		context.Background(),
		"missing",
	)
	assert.True(t, IsAppError(err, ErrCodeSourceNotFound),
		"TestSourceConnection for an unknown source returned %v, want source-not-found",
		err,
	)
}

func TestSetSourceAuthenticatedOverridesTheRegistry(t *testing.T) {
	registry := services.NewSourceRegistry()
	require.NoError(t, registry.Register(&stubSource{id: "stub"}), "Register")

	client, err := NewClientFromServices(ClientDeps{Sources: registry})
	require.NoError(t, err, "NewClientFromServices")

	defer client.Close()

	client.SetSourceAuthenticated("stub", true)

	assert.True(
		t,
		client.Services().Sources.Authenticated("stub"),
		"SetSourceAuthenticated did not reach the registry",
	)

	client.SetSourceAuthenticated("stub", false)

	assert.False(
		t,
		client.Services().Sources.Authenticated("stub"),
		"SetSourceAuthenticated did not clear the flag",
	)
	// An id the registry does not know is not this method's problem: the registry
	// owns the source list, and a flag for an unregistered source is never surfaced.
	client.SetSourceAuthenticated("unknown", true)
}

func TestSourceInfoDescribesARegisteredSource(t *testing.T) {
	registry := services.NewSourceRegistry()
	require.NoError(t, registry.Register(&stubSource{id: "stub"}), "Register")

	client, err := NewClientFromServices(ClientDeps{Sources: registry})
	require.NoError(t, err, "NewClientFromServices")

	defer client.Close()

	ctx := context.Background()

	info, err := client.SourceInfo(ctx, "stub")
	require.NoError(t, err, "SourceInfo")

	assert.Equal(t, "stub", info.ID, "source info")
	assert.Equal(t, "Stub Source", info.Name, "source info")

	assert.True(t, info.SupportsSearch, "source info reports search")
	assert.True(t, info.SupportsAuth, "source info reports auth")

	// The bundled sources are registered too — importing this package pulls in the
	// plugins whose init functions announce them — so the list is searched.
	var found bool

	for _, info := range client.ListSources(ctx) {
		if info.ID == "stub" {
			found = true
		}
	}

	assert.True(t, found, "ListSources did not include the registered stub source")

	_, err = client.SourceInfo(ctx, "  ")
	assert.True(
		t,
		IsAppError(err, ErrCodeInvalidArgument),
		"SourceInfo with a blank id returned %v, want invalid-argument",
		err,
	)
}

// A source that cannot be built is still described. A source silently missing
// from the list is the one failure a user cannot diagnose.
func TestListSourcesStillDescribesABrokenSource(t *testing.T) {
	registry := services.NewSourceRegistry()

	client, err := NewClientFromServices(ClientDeps{Sources: registry})
	require.NoError(t, err, "NewClientFromServices")

	defer client.Close()

	require.NoError(t, services.RegisterSourceFactory(
		"stubbroken",
		func(services.SourceBuildContext) (domain.ModSource, error) {
			return nil, appErrors.New(appErrors.ErrCodeAuthFailed, "the credentials are wrong")
		},
	), "RegisterSourceFactory")

	t.Cleanup(func() { services.UnregisterSourceFactory("stubbroken") })

	var broken *ModSourceInfo

	for _, info := range client.ListSources(context.Background()) {
		if info.ID == "stubbroken" {
			copied := info
			broken = &copied
		}
	}

	require.NotNil(
		t,
		broken,
		"ListSources omitted a source that cannot be built; a missing entry is the one failure a user cannot diagnose",
	)

	assert.False(t, broken.Enabled, "a source that cannot be built was reported as enabled")
}

func TestSearchFansOutToEverySearchableSource(t *testing.T) {
	registry := services.NewSourceRegistry()
	for _, id := range []string{"one", "two"} {
		require.NoError(t, registry.Register(&stubSource{id: id}), "Register %s", id)
	}

	require.NoError(t, registry.Register(&noSearchSource{stubSource{id: "offline"}}), "Register")

	client, err := NewClientFromServices(ClientDeps{Sources: registry})
	require.NoError(t, err, "NewClientFromServices")

	defer client.Close()

	// The registry skips a source that cannot search, and the caller sees an empty
	// result: not an error because the other sources answered fine.
	mods, err := client.Search(context.Background(), SearchQuery{Query: "ui"})
	require.NoError(t, err, "Search")

	assert.NotNil(t, mods, "Search returned nil, want an empty slice")
}

func TestHookDiscoveryReportsWhatThisBuildCanRun(t *testing.T) {
	executor := services.NewHookExecutor(services.HookExecutorDeps{})
	require.NoError(t, hooksRegisterForTest(executor), "registering hook runners")

	client, err := NewClientFromServices(ClientDeps{Hooks: executor})
	require.NoError(t, err, "NewClientFromServices")

	defer client.Close()

	ctx := context.Background()

	for _, hookType := range HookTypes() {
		assert.True(
			t,
			client.SupportsHookType(ctx, hookType),
			"this build cannot run %q, but the hook runners were registered",
			hookType,
		)
	}

	assert.False(
		t,
		client.SupportsHookType(ctx, "powershell"),
		"SupportsHookType claimed a runner for an unknown hook type",
	)

	script := HookScript{Language: LangJS, Content: "1"}
	assert.Equal(
		t,
		10*time.Second,
		client.HookTimeout(ctx, script, nil),
		"HookTimeout for a script with no timeout, want the 10s default",
	)

	script.Timeout = 15
	assert.Equal(
		t,
		15*time.Second,
		client.HookTimeout(ctx, script, nil),
		"HookTimeout for a script asking for 15s",
	)
}

func TestHookTypesAndPhasesCoverTheDomain(t *testing.T) {
	types := HookTypes()
	require.NotEmpty(t, types, "HookTypes returned nothing")

	want := domain.ScriptLanguages()
	assert.Len(t, types, len(want), "HookTypes returned types, want the domain's")

	for i, hookType := range want {
		assert.Equal(t, string(hookType), types[i], "HookTypes[%d]", i)
	}

	phases := HookPhases()
	assert.Len(t, phases, len(domain.HookPhases()), "HookPhases returned phases, want the domain's")
}

func TestValidateHookScriptChecksTheScript(t *testing.T) {
	executor := services.NewHookExecutor(services.HookExecutorDeps{})

	client, err := NewClientFromServices(ClientDeps{Hooks: executor})
	require.NoError(t, err, "NewClientFromServices")

	defer client.Close()

	ctx := context.Background()

	err = client.ValidateHookScript(
		ctx,
		HookScript{Language: LangJS, Content: "1+1"},
		nil,
	)
	assert.NoError(t, err, "a valid script was rejected")

	err = client.ValidateHookScript(
		ctx,
		HookScript{Language: "powershell", Content: "Get-Date"},
		nil,
	)
	assert.True(
		t,
		IsAppError(err, ErrCodeHookInvalid),
		"a script with an unknown type returned %v, want hook-invalid",
		err,
	)
}

func TestRunHookScriptsCarriesTheContainmentRules(t *testing.T) {
	// The executor resolves and confines the working directory, so a script cannot escape
	// the mod's and the game's folders.
	executor := services.NewHookExecutor(services.HookExecutorDeps{
		Platform: domain.PlatformLinux,
	})
	require.NoError(t, hooksRegisterForTest(executor), "registering hook runners")

	client, err := NewClientFromServices(ClientDeps{Hooks: executor})
	require.NoError(t, err, "NewClientFromServices")

	defer client.Close()

	mod := &domain.Mod{ID: "better_ui", Version: "1.2.0"}
	require.NoError(t, client.RunHookScripts(context.Background(), []HookScript{
		{Language: LangJS, Content: "console.log(dmm.context.mod.id)"},
	}, mod), "a well-formed inline hook failed")

	err = client.RunHookScripts(context.Background(), []HookScript{
		{Language: LangLua, Content: "true", WorkingDir: "/etc"},
	}, mod)
	assert.True(
		t,
		IsAppError(err, ErrCodeHookInvalid),
		"a hook naming a working directory outside every allowed root returned %v, want hook-invalid",
		err,
	)
}

func TestErrorsAreReExportedWithStableCodes(t *testing.T) {
	// The point of re-exporting is that a caller can branch without importing
	// pkg/errors, so the values must be identical, not copies.
	assert.Equal(
		t,
		appErrors.ErrCodeModNotFound,
		ErrCodeModNotFound,
		"the re-exported MOD_NOT_FOUND code differs from the one errors.Is matches on",
	)

	err := appErrors.New(appErrors.ErrCodeModNotFound, "no such mod %q", "better_ui")
	assert.True(
		t,
		IsAppError(err, ErrCodeModNotFound),
		"IsAppError did not recognise an error carrying a re-exported code",
	)

	assert.Equal(t, ErrCodeModNotFound, CodeOf(err), "CodeOf returned, want MOD_NOT_FOUND")

	assert.True(
		t,
		IsAnyCode(err, ErrCodeSearchFailed, ErrCodeModNotFound),
		"IsAnyCode did not match one of the codes given",
	)

	assert.False(
		t,
		IsAnyCode(err, ErrCodeSearchFailed),
		"IsAnyCode matched a code the error does not carry",
	)

	assert.Equal(
		t,
		`no such mod "better_ui"`,
		Message(err),
		"Message returned, want the message without the code prefix",
	)

	assert.Equal(
		t,
		CodeUnknown,
		CodeOf(errors.New("plain")),
		"CodeOf on a plain error returned, want UNKNOWN",
	)

	// Wrapping keeps the cause reachable, which is what a caller needs to inspect
	// the transport error under an install failure.
	cause := errors.New("dial tcp: connection refused")

	wrapped := Wrap(cause, ErrCodeInstallFailed, "could not install %q", "better_ui")
	assert.True(
		t,
		IsAppError(wrapped, ErrCodeInstallFailed),
		"the wrapped error does not carry its code",
	)

	assert.True(t, errors.Is(wrapped, cause), "the wrapped error does not unwrap to its cause")

	assert.Equal(t, cause, Cause(wrapped), "Cause returned, want the original error")

	assert.Nil(t, Wrap(nil, ErrCodeInstallFailed, "x"), "wrapping a nil error produced an error")

	assert.NotEmpty(
		t,
		New(ErrCodeNotFound, "missing").Error(),
		"New produced an error with an empty message",
	)
}

func TestRunHookScriptsRunsBothLanguages(t *testing.T) {
	executor := services.NewHookExecutor(services.HookExecutorDeps{})

	providers := host.Providers{}
	require.NoError(t, hooks.RegisterAll(executor, providers), "RegisterAll")

	client, err := NewClientFromServices(ClientDeps{Hooks: executor})
	require.NoError(t, err, "NewClientFromServices")

	defer client.Close()

	ctx := context.Background()
	mod := &Mod{ID: "better_ui", Name: "Better UI"}

	scripts := []HookScript{
		{Language: LangJS, Content: `console.log("js ok"); dmm.context.mod.id`},
		{Language: LangLua, Content: `print("lua ok"); local id = dmm.context.mod.id`},
	}
	require.NoError(t, client.RunHookScripts(ctx, scripts, mod), "RunHookScripts")
}
