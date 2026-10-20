package commands

import (
	"context"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
)

type App struct {
	base *base

	Mods    *ModCommands
	Sources *SourceCommands
	Game    *GameCommands
	Cache   *CacheCommands
	Config  *ConfigCommands
	Logs    *LogCommands
	Shares  *ShareCommands
	// P2PShares is separate: it is a different protocol, where a share is an
	// in-flight transfer with its own states.
	P2PShares *P2PShareCommands
	Actions   *ActionCommands
	// SourceLinks is its own set: it answers a question about a mod without
	// changing one.
	SourceLinks *SourceLinkCommands
	// Paths is the folder and file choosers, the only commands that talk to the
	// window runtime.
	Paths *PathCommands
}

// New builds the command surface over a client. It always returns a usable App:
// every method reports a nil client, because the frontend binds to these methods
// during startup.
func New(deps Deps) *App {
	shared := newBase(deps)

	return &App{
		base:        shared,
		Mods:        newModCommands(shared),
		Sources:     newSourceCommands(shared),
		Game:        newGameCommands(shared),
		Cache:       newCacheCommands(shared),
		Config:      newConfigCommands(shared),
		Logs:        newLogCommands(shared),
		Shares:      newShareCommands(shared),
		P2PShares:   newP2PShareCommands(shared),
		Actions:     newActionCommands(shared),
		SourceLinks: newSourceLinkCommands(shared),
		Paths:       newPathCommands(shared),
	}
}

// Startup rebinds every set to the Wails lifecycle context handed in by an
// OnStartup hook; operations run under it until Shutdown.
func (a *App) Startup(ctx context.Context) {
	if a == nil {
		return
	}

	a.base.SetContext(ctx)
}

// Shutdown cancels everything in flight and refuses further calls. It is
// idempotent and leaves the client to the container, so shutdown order does not
// depend on which closer ran.
func (a *App) Shutdown() {
	if a == nil {
		return
	}

	a.base.Shutdown()
}

// CancelAll stops whatever is running without shutting the window; this is the
// "Stop" button, and the command surface stays usable.
func (a *App) CancelAll() {
	if a == nil {
		return
	}

	a.base.CancelAll()
}

// Stopped reports whether Shutdown has been called.
func (a *App) Stopped() bool {
	if a == nil {
		return true
	}

	return a.base.Stopped()
}

// Context reports the context operations currently run under. It is never nil.
func (a *App) Context() context.Context {
	if a == nil {
		return context.Background()
	}

	return a.base.Context()
}

// Timeout reports the per-operation bound in force, in milliseconds, or zero
// when there is none.
func (a *App) Timeout() int64 {
	if a == nil {
		return 0
	}

	return a.base.Timeout().Milliseconds()
}

// EventTypes lists every event the manager publishes, so a frontend can bind to
// them by name without repeating the list.
func (a *App) EventTypes() []string {
	types := api.EventTypes()

	out := make([]string, 0, len(types))
	for _, t := range types {
		out = append(out, string(t))
	}

	return out
}

// HookTypes lists the script languages this build can run; a hook editor must
// ask, because a language with no registered runner fails every time it runs.
func (a *App) HookTypes() []string {
	return api.HookTypes()
}

// HookPhases lists the lifecycle points a mod can hook, in the order they run.
func (a *App) HookPhases() []string {
	return api.HookPhases()
}

// SupportsHookType reports whether one hook kind can run here.
func (a *App) SupportsHookType(hookType string) bool {
	if a == nil {
		return false
	}

	client, _, _, err := a.base.ready()
	if err != nil {
		return false
	}

	return client.SupportsHookType(a.base.Context(), hookType)
}

// HookTimeout reports how long one script would be allowed to run.
func (a *App) HookTimeout(script api.HookScript, mod *api.Mod) (float64, error) {
	if a == nil {
		return 0, errUninitialised()
	}

	client, ctx, cancel, err := a.base.ready()
	if err != nil {
		return 0, err
	}

	defer cancel()

	timeout := client.HookTimeout(ctx, script, mod)
	// Milliseconds: that is what a JavaScript caller can hand to setTimeout, and
	// a duration does not survive the bridge.
	return float64(timeout.Milliseconds()), nil
}

// ValidateHookScript checks one script before a frontend saves it, so a bad
// working directory is reported while the user is looking at the script.
func (a *App) ValidateHookScript(script api.HookScript, mod *api.Mod) error {
	if a == nil {
		return errUninitialised()
	}

	client, ctx, cancel, err := a.base.ready()
	if err != nil {
		return err
	}

	defer cancel()

	if err := client.ValidateHookScript(ctx, script, mod); err != nil {
		return a.base.fail(err, "validate hook script")
	}

	return nil
}

// RunHookScript executes one script by hand under the same containment rules a
// real hook run uses: same working-directory resolution, same environment
// allowlist, same timeout kill. It exists because a mod author asking whether a
// script works should not have to install the mod to find out. Output is not
// returned; a hook's writes reach the manager's log through the run's events.
func (a *App) RunHookScript(script api.HookScript, mod *api.Mod) error {
	if a == nil {
		return errUninitialised()
	}

	client, ctx, cancel, err := a.base.ready()
	if err != nil {
		return err
	}

	defer cancel()

	if err := client.RunHookScripts(ctx, []api.HookScript{script}, mod); err != nil {
		return a.base.fail(err, "run hook script")
	}

	return nil
}
