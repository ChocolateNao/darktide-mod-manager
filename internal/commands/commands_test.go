package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// command is one method of the command surface, reduced to a single call. The
// table exists so a method added later cannot be forgotten: it is the only thing
// that keeps "every method reports a broken wiring rather than panicking" a
// statement about the whole surface instead of about the methods someone
// remembered.
type command struct {
	call func(*App) error
	name string
}

// everyCommand lists every error-returning method a frontend can call. The one
// exception is SupportsHookType, which answers with a bool and has its own test.
func everyCommand() []command {
	return []command{
		{func(a *App) error { _, err := a.Mods.ListMods(); return err }, "Mods.ListMods"},
		{func(a *App) error { _, err := a.Mods.GetMod("m"); return err }, "Mods.GetMod"},
		{
			func(a *App) error { _, err := a.Mods.InstallMod("local", "m"); return err },
			"Mods.InstallMod",
		},
		{func(a *App) error { return a.Mods.UninstallMod("m") }, "Mods.UninstallMod"},
		{func(a *App) error {
			return a.Mods.UninstallModWithOptions("m", api.UninstallOptions{})
		}, "Mods.UninstallModWithOptions"},
		{func(a *App) error { return a.Mods.EnableMod("m") }, "Mods.EnableMod"},
		{func(a *App) error { return a.Mods.DisableMod("m") }, "Mods.DisableMod"},
		{func(a *App) error { _, err := a.Mods.GetModHooks("m"); return err }, "Mods.GetModHooks"},
		{func(a *App) error {
			return a.Mods.UpdateModHooks("m", api.ModHooks{})
		}, "Mods.UpdateModHooks"},
		{
			func(a *App) error { _, err := a.Mods.CheckModUpdates(); return err },
			"Mods.CheckModUpdates",
		},
		{func(a *App) error { return a.Mods.UpdateMod("m") }, "Mods.UpdateMod"},
		{func(a *App) error { return a.Mods.CancelUpdate("m") }, "Mods.CancelUpdate"},
		{func(a *App) error { return a.Mods.RefreshMods() }, "Mods.RefreshMods"},
		{
			func(a *App) error { _, err := a.Mods.DetectConflicts(); return err },
			"Mods.DetectConflicts",
		},
		{func(a *App) error {
			return a.Mods.ResolveConflict("file_overlap:a:b", "keep-both")
		}, "Mods.ResolveConflict"},
		{func(a *App) error { return a.Mods.ReorderMods([]string{"a", "b"}) }, "Mods.ReorderMods"},
		{func(a *App) error { _, err := a.Mods.LoadOrder(); return err }, "Mods.LoadOrder"},
		{func(a *App) error { _, err := a.Mods.ModPath("m"); return err }, "Mods.ModPath"},
		{func(a *App) error { _, err := a.Mods.SymlinkStatus(); return err }, "Mods.SymlinkStatus"},
		{func(a *App) error { _, err := a.Mods.RelinkMod("m"); return err }, "Mods.RelinkMod"},

		{
			func(a *App) error { _, err := a.Sources.ListSources(); return err },
			"Sources.ListSources",
		},
		{
			func(a *App) error { _, err := a.Sources.SourceInfo("local"); return err },
			"Sources.SourceInfo",
		},
		{func(a *App) error {
			_, err := a.Sources.SearchSource("local", "ui")
			return err
		}, "Sources.SearchSource"},
		{func(a *App) error {
			_, err := a.Sources.SearchAll(api.SearchQuery{Query: "ui"})
			return err
		}, "Sources.SearchAll"},
		{func(a *App) error {
			_, err := a.Sources.GetSourceMod("local", "m")
			return err
		}, "Sources.GetSourceMod"},
		{func(a *App) error {
			return a.Sources.AuthenticateSource("local", map[string]string{"token": "t"})
		}, "Sources.AuthenticateSource"},
		{func(a *App) error {
			return a.Sources.TestSourceConnection("local")
		}, "Sources.TestSourceConnection"},
		{func(a *App) error {
			return a.Sources.SetSourceAuthenticated("local", true)
		}, "Sources.SetSourceAuthenticated"},

		{func(a *App) error { _, err := a.Game.DetectGame(); return err }, "Game.DetectGame"},
		{func(a *App) error { _, err := a.Game.CurrentGame(); return err }, "Game.CurrentGame"},
		{func(a *App) error {
			_, err := a.Game.ValidateGamePath("/nowhere")
			return err
		}, "Game.ValidateGamePath"},
		{func(a *App) error { return a.Game.SetGamePath("/nowhere") }, "Game.SetGamePath"},
		{func(a *App) error {
			_, err := a.Game.GetModsPath(nil)
			return err
		}, "Game.GetModsPath"},

		{func(a *App) error { _, err := a.Cache.GetCacheInfo(); return err }, "Cache.GetCacheInfo"},
		{func(a *App) error { return a.Cache.ClearCache() }, "Cache.ClearCache"},
		{func(a *App) error { return a.Cache.PruneCache(1, 1) }, "Cache.PruneCache"},
		{func(a *App) error { return a.Cache.PruneWithDefaults() }, "Cache.PruneWithDefaults"},
		{func(a *App) error { _, err := a.Cache.CacheRoot(); return err }, "Cache.CacheRoot"},

		{func(a *App) error { _, err := a.Config.GetConfig(); return err }, "Config.GetConfig"},
		{func(a *App) error { return a.Config.UpdateConfig(&api.Config{}) }, "Config.UpdateConfig"},
		{func(a *App) error {
			_, err := a.Config.GetConfigValue("cache.max_size_gb")
			return err
		}, "Config.GetConfigValue"},
		{func(a *App) error {
			return a.Config.SetConfigValue("cache.max_size_gb", "1")
		}, "Config.SetConfigValue"},
		{func(a *App) error { _, err := a.Config.ConfigKeys(); return err }, "Config.ConfigKeys"},
		{func(a *App) error { _, err := a.Config.ConfigPath(); return err }, "Config.ConfigPath"},

		{func(a *App) error {
			_, err := a.HookTimeout(api.HookScript{}, nil)
			return err
		}, "App.HookTimeout"},
		{func(a *App) error {
			return a.ValidateHookScript(api.HookScript{Language: api.LangJS}, nil)
		}, "App.ValidateHookScript"},
	}
}

// TestEveryCommandReportsAMissingClient is the nil-safety net. A command set
// built without a client is a wiring mistake, and the reaction has to be an error
// the frontend can render: a panic here happens inside a call the frontend made,
// which in a desktop window means a window that cannot draw the message
// explaining why.
func TestEveryCommandReportsAMissingClient(t *testing.T) {
	app := New(Deps{})

	for _, cmd := range everyCommand() {
		t.Run(cmd.name, func(t *testing.T) {
			err := cmd.call(app)
			require.Error(t, err, "%s reported nothing when no client was configured", cmd.name)

			assert.True(
				t,
				appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
				"%s error code = %v, want INVALID_ARGUMENT (err: %v)",
				cmd.name,
				appErrors.CodeOf(err),
				err,
			)

			assert.Contains(
				t,
				err.Error(),
				"client",
				"%s error does not mention the missing client: %v",
				cmd.name,
				err,
			)
		})
	}
}

// TestEveryCommandSurvivesAnUninitialisedSet covers the other way to arrive here:
// a set whose shared state was never assigned, which is what a half-built
// aggregate looks like.
func TestEveryCommandSurvivesAnUninitialisedSet(t *testing.T) {
	app := &App{
		Mods:    &ModCommands{},
		Sources: &SourceCommands{},
		Game:    &GameCommands{},
		Cache:   &CacheCommands{},
		Config:  &ConfigCommands{},
	}

	for _, cmd := range everyCommand() {
		t.Run(cmd.name, func(t *testing.T) {
			err := cmd.call(app)
			require.Error(t, err, "%s reported nothing on an uninitialised set", cmd.name)

			assert.Contains(
				t,
				err.Error(),
				"not initialised",
				"%s error does not say the set is uninitialised: %v",
				cmd.name,
				err,
			)
		})
	}
}

// TestEveryCommandReportsAShutDownApp checks the other end of the lifecycle. The
// complaint has to mention the shutdown: a frontend that has closed and then
// retries a call is looking for "the app is closing", not for an argument it
// believes it supplied.
func TestEveryCommandReportsAShutDownApp(t *testing.T) {
	w := newWorkspace(t)
	app := w.mustApp(t)
	app.Shutdown()

	for _, cmd := range everyCommand() {
		t.Run(cmd.name, func(t *testing.T) {
			err := cmd.call(app)
			require.Error(t, err, "%s ran after shutdown", cmd.name)

			assert.Contains(
				t,
				err.Error(),
				"shutting down",
				"%s error does not mention the shutdown: %v",
				cmd.name,
				err,
			)
		})
	}
}

// TestLifecycleMethodsTolerateANilApp. The lifecycle is called from a deferred
// shutdown and from a window-close handler, both of which can run with a nil
// receiver when the constructor's error was ignored.
func TestLifecycleMethodsTolerateANilApp(t *testing.T) {
	var app *App
	app.Startup(t.Context())
	app.Shutdown()
	app.CancelAll()

	assert.True(t, app.Stopped(), "a nil app must report itself stopped")

	assert.NotNil(t, app.Context(), "a nil app must still hand out a usable context")

	assert.EqualValues(t, 0, app.Timeout(), "nil app timeout = %d, want 0", app.Timeout())

	{
		got := app.EventTypes()
		assert.NotEmpty(t, got, "a nil app must still be able to list the event types")
	}
}

// TestUninitialisedSetAccessors checks the remaining nil-receiver paths on a set.
//
// The receiver here is non-nil with nothing assigned, which is the shape a
// half-built aggregate has. A nil *ModCommands is deliberately not covered: the
// lifecycle methods reach the embedded state, so calling one on a nil set
// dereferences the nil before the shared state can notice it. That is Go's rule
// for a promoted method, it can only arise from a caller's own nil variable, and
// hiding it behind twenty-five forwarding methods would be worse than saying so.
func TestUninitialisedSetAccessors(t *testing.T) {
	mods := &ModCommands{}
	mods.Shutdown()
	mods.CancelAll()

	assert.True(t, mods.Stopped(), "an uninitialised set must report itself stopped")

	assert.NotNil(t, mods.Context(), "an uninitialised set must still hand out a usable context")

	{
		got := mods.Timeout()
		assert.Equal(
			t,
			DefaultTimeout,
			got,
			"uninitialised set timeout = %s, want the default %s",
			got,
			DefaultTimeout,
		)
	}
}

// TestSupportsHookTypeNeedsAClient: this one answers with a bool rather than an
// error, because "this build cannot run go hooks" is the question a hook editor
// asks while drawing, not a failure. It still has to be false when there is no
// client, since an answer invented without one would be a guess.
func TestSupportsHookTypeNeedsAClient(t *testing.T) {
	assert.False(
		t,
		New(Deps{}).SupportsHookType(string(api.LangJS)),
		"a surface with no client claimed to support inline hooks",
	)

	var app *App
	assert.False(
		t,
		app.SupportsHookType(string(api.LangJS)),
		"a nil app claimed to support inline hooks",
	)
}

func TestNewBuildsEverySet(t *testing.T) {
	app := New(Deps{})
	require.NotNil(t, app, "New returned nothing")

	require.False(t, app.Mods == nil || app.Sources == nil || app.Game == nil || app.Cache == nil ||
		app.Config == nil, "New left a set unset: %+v", app)

	assert.False(t, app.Stopped(), "a freshly built app must not report itself stopped")
}

// TestEverySetSharesOneScope. Cancelling through the aggregate has to reach every
// set, because they are all driven by the same person: a window that has stopped
// responding has not stopped caring about a download it started.
func TestEverySetSharesOneScope(t *testing.T) {
	w := newWorkspace(t)
	app := w.mustApp(t)

	shared := app.base
	require.False(
		t,
		app.Mods.base != shared || app.Sources.base != shared || app.Game.base != shared ||
			app.Cache.base != shared || app.Config.base != shared,
		"the sets do not share one scope; CancelAll would leave some running",
	)
}

// TestCancelAllReachesAnOperationInFlight is the reason the scope exists. The
// stub signals when it is genuinely inside the call, so the assertion that it was
// still blocked cannot pass by accident, and the cancellation is the only thing
// that can release it.
func TestCancelAllReachesAnOperationInFlight(t *testing.T) {
	const sourceID = "commands-blocking"

	stub := registerBlockingSource(t, sourceID)

	w := newWorkspace(t)
	app := w.mustApp(t)

	done := make(chan error, 1)

	go func() {
		_, err := app.Sources.GetSourceMod(sourceID, "anything")
		done <- err
	}()

	select {
	case <-stub.entered:
	case <-time.After(30 * time.Second):
		require.FailNow(t, "the operation never reached the source")
	}

	select {
	case err := <-done:
		require.FailNow(t, "the operation returned before anything cancelled it: %v", err)
	default:
	}

	app.CancelAll()

	select {
	case err := <-done:
		require.Error(t, err, "a cancelled operation reported success")
		// The code has to survive the registry's lookup and the source's own
		// error: a cancellation that arrives as a generic failure is one a
		// frontend reports to the user as "something went wrong" instead of
		// quietly dropping the request the user just stopped.
		assert.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeOperationCanceled),
			"cancelled operation error code = %v, want OPERATION_CANCELED (err: %v)",
			appErrors.CodeOf(err),
			err,
		)
	case <-time.After(30 * time.Second):
		require.FailNow(t, "CancelAll did not release the operation")
	}
}

// TestCancelAllLeavesTheSurfaceUsable is what makes CancelAll a stop button rather
// than a shutdown in disguise.
func TestCancelAllLeavesTheSurfaceUsable(t *testing.T) {
	w := newWorkspace(t)
	app := w.mustApp(t)

	before := app.Context()
	app.CancelAll()

	select {
	case <-before.Done():
	default:
		require.FailNow(t, "CancelAll did not cancel the scope that was in force")
	}

	assert.False(t, app.Stopped(), "CancelAll must not shut the surface down")

	after := app.Context()
	require.NotEqual(t, before, after, "CancelAll must hand out a fresh scope")

	select {
	case <-after.Done():
		require.FailNow(t, "CancelAll left the replacement scope already cancelled")
	default:
	}
	// And the surface still answers.
	{
		_, err := app.Sources.ListSources()
		assert.NoError(t, err, "ListSources after CancelAll: %v", err)
	}
}

// TestCancelAllDoesNotReviveAShutDownSurface pins the one asymmetry: a stop button
// must not be able to reopen a closing window.
func TestCancelAllDoesNotReviveAShutDownSurface(t *testing.T) {
	w := newWorkspace(t)
	app := w.mustApp(t)
	app.Shutdown()
	app.CancelAll()

	require.True(t, app.Stopped(), "CancelAll revived a shut-down surface")

	{
		_, err := app.Config.GetConfig()
		assert.Error(t, err, "a surface revived by CancelAll answered a call")
	}
}

// TestShutdownCancelsTheScopeAndIsIdempotent.
func TestShutdownCancelsTheScopeAndIsIdempotent(t *testing.T) {
	w := newWorkspace(t)
	app := w.mustApp(t)
	scope := app.Context()
	app.Shutdown()

	select {
	case <-scope.Done():
	default:
		require.FailNow(t, "Shutdown did not cancel the scope")
	}

	assert.True(t, app.Stopped(), "Stopped must report true after Shutdown")
	// Twice more, to prove the second call neither panics on a nil cancel nor
	// revives anything.
	app.Shutdown()
	app.Shutdown()

	assert.True(t, app.Stopped(), "a repeated Shutdown must keep reporting stopped")
}

// TestStartupRebindsTheScope: the context a Wails startup hook hands over is the
// one operations run under, so cancelling it has to stop them.
func TestStartupRebindsTheScope(t *testing.T) {
	w := newWorkspace(t)
	app := w.mustApp(t)
	before := app.Context()

	parent, cancel := context.WithCancel(t.Context())
	app.Startup(parent)

	after := app.Context()
	require.NotEqual(t, before, after, "Startup did not replace the scope")

	select {
	case <-before.Done():
	default:
		require.FailNow(t, "Startup left the previous scope live, leaking its context")
	}

	cancel()

	select {
	case <-after.Done():
	case <-time.After(10 * time.Second):
		require.FailNow(t, "cancelling the lifecycle context did not reach the scope")
	}
}

// TestStartupToleratesANilContext: a startup hook that passes nothing must not
// take the window down with it.
func TestStartupToleratesANilContext(t *testing.T) {
	w := newWorkspace(t)
	app := w.mustApp(t)
	//nolint:staticcheck // Passing nil is the mistake being exercised.
	app.Startup(nil)

	require.NotNil(t, app.Context(), "a nil lifecycle context left no usable scope")

	{
		_, err := app.Sources.ListSources()
		assert.NoError(t, err, "the surface did not survive a nil lifecycle context: %v", err)
	}
	// And a second Startup revives a shut-down surface, which is what a frontend
	// reopening the window needs.
	app.Shutdown()
	app.Startup(t.Context())

	assert.False(t, app.Stopped(), "Startup must revive a surface a previous Startup shut down")
}

// TestTimeoutIsReportedInMilliseconds pins the number a frontend shows next to a
// slow operation, and the three ways Deps can express a bound.
func TestTimeoutIsReportedInMilliseconds(t *testing.T) {
	cases := []struct {
		deps    Deps
		name    string
		comment string
		want    int64
	}{
		{Deps{}, "unset uses the default", "", DefaultTimeout.Milliseconds()},
		{Deps{Timeout: 0}, "zero means the default", "", DefaultTimeout.Milliseconds()},
		{Deps{Timeout: 90 * time.Second}, "explicit wins", "", 90_000},
		{
			Deps{Timeout: -time.Second},
			"negative means no deadline",
			"a negative value is how a caller says the scope alone should bound it",
			0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			{
				got := New(tc.deps).Timeout()
				assert.Equal(t, tc.want, got, "Timeout() = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestFailedOperationsAreLogged: a frontend has no log file to read, so the one
// line naming the operation is the only trace a bug report can carry.
func TestFailedOperationsAreLogged(t *testing.T) {
	w := newWorkspace(t)
	logger := &recordingLogger{}
	c := w.container(t)
	app := New(Deps{Client: c.Client, Logger: logger})

	// No such mod is the cheapest real failure there is.
	{
		_, err := app.Mods.GetMod("does-not-exist")
		require.Error(t, err, "GetMod reported success for a mod that is not installed")
	}

	lines := logger.snapshot()
	require.Len(t, lines, 1, "logged %d lines, want exactly one: %q", len(lines), lines)

	assert.False(
		t,
		!strings.Contains(lines[0], "get mod") || !strings.Contains(lines[0], "does-not-exist"),
		"log line does not name the operation: %q",
		lines[0],
	)

	{
		_, err := app.Cache.GetCacheInfo()
		assert.NoError(t, err, "GetCacheInfo on an empty cache: %v", err)
	}

	assert.Len(t, logger.snapshot(), 1, "a successful operation was logged: %q", logger.snapshot())
}

// TestLoggerPanicIsContained: a logger is caller-supplied, and one that panics
// must not turn a reported failure into a crash.
func TestLoggerPanicIsContained(t *testing.T) {
	w := newWorkspace(t)
	c := w.container(t)
	app := New(Deps{Client: c.Client, Logger: panickingLogger{}})

	{
		_, err := app.Mods.GetMod("does-not-exist")
		require.Error(t, err, "GetMod reported success for a mod that is not installed")
	}
}

// TestNoLoggerIsFine: a nil logger is the common case and must be silent.
func TestNoLoggerIsFine(t *testing.T) {
	w := newWorkspace(t)

	app := w.mustApp(t)
	{
		_, err := app.Mods.GetMod("does-not-exist")
		require.Error(t, err, "GetMod reported success for a mod that is not installed")
	}
}

type panickingLogger struct{}

func (panickingLogger) Printf(string, ...any) { panic("logger exploded") }

// TestContextIsNeverNil covers the accessor the tests above rely on.
func TestContextIsNeverNil(t *testing.T) {
	w := newWorkspace(t)
	{
		got := w.mustApp(t).Context()
		assert.NotNil(t, got, "Context returned nil")
	}

	{
		err := got1(w.mustApp(t).Context())
		assert.NoError(t, err, "the reported context is already done: %v", err)
	}
}

func got1(ctx context.Context) error { return ctx.Err() }
