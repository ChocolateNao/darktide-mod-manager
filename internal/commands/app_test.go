package commands

import (
	"strings"
	"testing"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/di"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEventTypesListsWhatTheManagerPublishes. A frontend binds to events by name,
// so the list has to be the real one rather than a copy the caller could drift
// away from.
func TestEventTypesListsWhatTheManagerPublishes(t *testing.T) {
	got := New(Deps{}).EventTypes()

	want := api.EventTypes()
	require.Len(
		t,
		got,
		len(want),
		"EventTypes has %d entries, the api package has %d",
		len(got),
		len(want),
	)

	for i := range want {
		assert.Equal(t, string(want[i]), got[i], "EventTypes[%d] = %q, want %q", i, got[i], want[i])
	}

	assert.True(
		t,
		contains(got, string(api.EventModInstallStarted)),
		"EventTypes omits %s",
		api.EventModInstallStarted,
	)

	assert.True(
		t,
		contains(got, string(api.EventCacheCleared)),
		"EventTypes omits %s",
		api.EventCacheCleared,
	)
}

// TestHookTypesAndPhasesMatchTheDomain: the hook editor offers what the executor
// can actually run, and offering a phase that does not exist means a script that
// never fires.
func TestHookTypesAndPhasesMatchTheDomain(t *testing.T) {
	app := New(Deps{})
	{
		got, want := app.HookTypes(), api.HookTypes()
		assert.True(t, equalStrings(got, want), "HookTypes = %v, want %v", got, want)
	}

	{
		got, want := app.HookPhases(), api.HookPhases()
		assert.True(t, equalStrings(got, want), "HookPhases = %v, want %v", got, want)
	}

	for _, hookType := range domain.ScriptLanguages() {
		assert.True(
			t,
			contains(app.HookTypes(), string(hookType)),
			"HookTypes omits the valid type %q",
			hookType,
		)
	}

	for _, phase := range domain.HookPhases() {
		assert.True(
			t,
			contains(app.HookPhases(), string(phase)),
			"HookPhases omits the valid phase %q",
			phase,
		)
	}
}

// TestSupportsHookTypeForEveryTypeThisBuildCanRun. Every type the domain considers
// valid must have a runner, or offering it in the editor produces a mod that fails
// every time it is enabled.
func TestSupportsHookTypeForEveryTypeThisBuildCanRun(t *testing.T) {
	app := newWorkspace(t).mustApp(t)
	for _, hookType := range domain.ScriptLanguages() {
		assert.True(
			t,
			app.SupportsHookType(string(hookType)),
			"no runner is registered for the valid hook type %q",
			hookType,
		)
	}

	assert.False(
		t,
		app.SupportsHookType("powershell"),
		"a hook type the domain rejects was reported as supported",
	)
}

// TestHookTimeoutIsReportedInMilliseconds: the number is what a frontend hands to
// setTimeout, and a duration would not survive the bridge as something JavaScript
// recognises.
func TestHookTimeoutIsReportedInMilliseconds(t *testing.T) {
	app := newWorkspace(t).mustApp(t)

	ms, err := app.HookTimeout(api.HookScript{Language: api.LangJS}, nil)
	require.NoError(t, err, "HookTimeout")

	{
		want := float64((10 * time.Second).Milliseconds())
		assert.Equal(t, want, ms, "default timeout = %v ms, want %v", ms, want)
	}

	// The default is a floor, so a smaller declared value is raised to it.
	ms, err = app.HookTimeout(api.HookScript{Language: api.LangJS, Timeout: 5}, nil)
	require.NoError(t, err, "HookTimeout with an override below the floor")

	assert.EqualValues(t, 10_000, ms, "timeout below the floor = %v ms, want 10000", ms)

	ms, err = app.HookTimeout(api.HookScript{Language: api.LangJS, Timeout: 15}, nil)
	require.NoError(t, err, "HookTimeout with an override above the floor")

	assert.EqualValues(t, 15_000, ms, "overridden timeout = %v ms, want 15000", ms)
}

// TestValidateHookScriptCatchesProblemsWhileTheFormIsOpen.
func TestValidateHookScriptCatchesProblemsWhileTheFormIsOpen(t *testing.T) {
	app := newWorkspace(t).mustApp(t)

	good := api.HookScript{Language: api.LangJS, Content: "console.log('hi')"}
	{
		err := app.ValidateHookScript(good, nil)
		assert.NoError(t, err, "a valid javascript script was refused: %v", err)
	}

	bad := api.HookScript{Language: "powershell", Content: "Get-ChildItem"}
	{
		err := app.ValidateHookScript(
			bad,
			nil,
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeHookInvalid,
		), "error = %v, want HOOK_INVALID for an unknown hook type", err)
	}
}

// TestEventsReachAHandlerSuppliedThroughTheContainer. This is the route a GUI
// uses, and it is the only way the command layer's work becomes visible to the
// frontend at all — a window that never hears that an install finished is a window
// that shows a spinner forever.
func TestEventsReachAHandlerSuppliedThroughTheContainer(t *testing.T) {
	w := newWorkspace(t)
	w.makeGame(t)
	w.writeConfig(t, func(cfg *domain.Config) {
		cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.Path = w.gameDir })
	})
	w.writeModArchive(t, "better_ui", "1.2.0")

	handler := &recordingHandler{}
	c := w.container(t, func(o *di.Options) { o.EventHandler = handler.handle })
	app := New(Deps{Client: c.Client})

	id := findLocalMod(t, app)
	{
		_, err := app.Mods.InstallMod("local", id)
		require.NoError(t, err, "InstallMod: %v", err)
	}

	for _, want := range []api.EventType{
		api.EventModInstallStarted,
		api.EventModInstallCompleted,
		api.EventModEnabled,
	} {
		assert.True(t, handler.has(want), "the install did not publish %s", want)
	}

	require.NotEqual(t, 0, handler.count(), "no events arrived at all")

	{
		err := app.Cache.ClearCache()
		require.NoError(t, err, "ClearCache: %v", err)
	}

	assert.True(
		t,
		handler.has(api.EventCacheCleared),
		"ClearCache did not reach the handler as %s",
		api.EventCacheCleared,
	)
}

// TestTimeoutIsReportedThroughTheAggregate covers the accessor the hook editor
// shows next to a slow operation.
func TestTimeoutIsReportedThroughTheAggregate(t *testing.T) {
	{
		got := New(Deps{}).Timeout()
		assert.Equal(
			t,
			DefaultTimeout.Milliseconds(),
			got,
			"default timeout = %d ms, want %d",
			got,
			DefaultTimeout.Milliseconds(),
		)
	}

	{
		got := New(Deps{Timeout: 90 * time.Second}).Timeout()
		assert.EqualValues(t, 90_000, got, "timeout = %d ms, want 90000", got)
	}

	{
		got := New(Deps{Timeout: -1}).Timeout()
		assert.EqualValues(t, 0, got, "timeout with no deadline = %d ms, want 0", got)
	}
}

// contains reports whether a string slice holds a value.
func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}

	return false
}

// equalStrings reports whether two string slices hold the same values in order.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if !strings.EqualFold(a[i], b[i]) {
			return false
		}
	}

	return true
}

// keep strings imported for the helpers above without an unused-import risk.
var _ = strings.TrimSpace
