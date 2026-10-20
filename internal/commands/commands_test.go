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

type command struct {
	call func(*App) error
	name string
}

func everyCommand() []command {
	return []command{
		{func(a *App) error { _, err := a.Mods.ListMods(); return err }, "Mods.ListMods"},
		{func(a *App) error { _, err := a.Mods.GetMod("m"); return err }, "Mods.GetMod"},
		{
			func(a *App) error { _, err := a.Mods.InstallMod("local", "m"); return err },
			"Mods.InstallMod",
		},
		{func(a *App) error { return a.Mods.UninstallMod("m") }, "Mods.UninstallMod"},
		{
			func(a *App) error { return a.Mods.UninstallModWithOptions("m", api.UninstallOptions{}) },
			"Mods.UninstallModWithOptions",
		},
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

		{func(a *App) error { _, err := a.Game.DetectGame(); return err }, "Game.DetectGame"},
		{func(a *App) error { _, err := a.Game.CurrentGame(); return err }, "Game.CurrentGame"},
		{func(a *App) error {
			_, err := a.Game.ValidateGamePath("/nowhere")
			return err
		}, "Game.ValidateGamePath"},
		{func(a *App) error { return a.Game.SetGamePath("/nowhere") }, "Game.SetGamePath"},
		{func(a *App) error { _, err := a.Game.GetModsPath(nil); return err }, "Game.GetModsPath"},

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
		app.Config == nil, "New left a set unset: %+v")

	assert.False(t, app.Stopped(), "a freshly built app must not report itself stopped")
}

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
		// error, so a cancellation is reported as a stopped request.
		assert.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeOperationCanceled),
			"cancelled operation error code = %v, want OPERATION_CANCELED (err: %v)",
			appErrors.CodeOf(err),
			err)
	case <-time.After(30 * time.Second):
		require.FailNow(t, "CancelAll did not release the operation")
	}
}

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
	default:
	}
	// And the surface still answers.
	{
		_, err := app.Sources.ListSources()
		assert.NoError(t, err, "ListSources after CancelAll: %v", err)
	}
}

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
	app.Shutdown()
	app.Shutdown()

	assert.True(t, app.Stopped(), "a repeated Shutdown must keep reporting stopped")
}

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

	app.Shutdown()
	app.Startup(t.Context())

	assert.False(t, app.Stopped(), "Startup must revive a surface a previous Startup shut down")
}

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

func TestFailedOperationsAreLogged(t *testing.T) {
	w := newWorkspace(t)
	logger := &recordingLogger{}
	c := w.container(t)
	app := New(Deps{Client: c.Client, Logger: logger})

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

func TestLoggerPanicIsContained(t *testing.T) {
	w := newWorkspace(t)
	c := w.container(t)
	app := New(Deps{Client: c.Client, Logger: panickingLogger{}})

	{
		_, err := app.Mods.GetMod("does-not-exist")
		require.Error(t, err, "GetMod reported success for a mod that is not installed")
	}
}

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
