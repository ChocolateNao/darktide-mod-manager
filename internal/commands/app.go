package commands

import (
	"context"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
)

// App is everything a Wails frontend binds to.
//
// The bindings come from the methods of the structs registered with wails.Run, so
// registering five sets would mean five places to remember. App holds them and
// adds the three window-level calls: Startup, Shutdown, CancelAll.
//
// Every set shares one scope, so cancelling an install also stops a running
// search: they share one driver, and a window that has stopped responding has not
// stopped caring.
type App struct {
	base *base

	Mods    *ModCommands
	Sources *SourceCommands
	Game    *GameCommands
	Cache   *CacheCommands
	Config  *ConfigCommands
	Logs    *LogCommands
	Shares  *ShareCommands
	// P2PShares is separate because it is a different protocol with different states:
	// a peer-to-peer share is in flight, not a record in a table.
	P2PShares *P2PShareCommands
	Actions   *ActionCommands
	// SourceLinks is its own set because it answers a question about a mod rather
	// than changing one.
	SourceLinks *SourceLinkCommands
	// Paths is the folder and file choosers, the only commands that talk to the
	// window runtime rather than the manager's data.
	Paths *PathCommands
}

// New builds the command surface over a client. It always returns a usable App:
// a nil client is reported by every method rather than panicking, because the
// frontend binds to these methods during startup.
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

// Startup rebinds every set to the Wails lifecycle context. It is what a Wails
// OnStartup hook calls, and the context it is given is what the operations run
// under until Shutdown.
func (a *App) Startup(ctx context.Context) {
	if a == nil {
		return
	}

	a.base.SetContext(ctx)
}

// Shutdown cancels everything in flight and refuses further calls. It is
// idempotent, and does not close the client: the container owns that, so closing
// here too would leave the order of release to whichever ran first.
func (a *App) Shutdown() {
	if a == nil {
		return
	}

	a.base.Shutdown()
}

// CancelAll stops whatever is running without shutting the window. This is the
// "Stop" button; the command surface stays usable afterwards.
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

// Timeout reports the per-operation bound in force, in milliseconds, or zero when
// there is none. A frontend that wants to warn about a long operation has no
// other way to learn what this build considers long.
func (a *App) Timeout() int64 {
	if a == nil {
		return 0
	}

	return a.base.Timeout().Milliseconds()
}

// EventTypes lists every event the manager publishes, so a frontend can bind to
// them by name rather than repeating the list.
func (a *App) EventTypes() []string {
	types := api.EventTypes()

	out := make([]string, 0, len(types))
	for _, t := range types {
		out = append(out, string(t))
	}

	return out
}

// HookTypes lists the script languages this build can run. A frontend offering
// a hook editor must ask first: a language with no registered runner would
// accept the script and fail every time it runs.
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
	// Milliseconds, because that is what a JavaScript caller can hand to setTimeout;
	// a duration does not survive the bridge.
	return float64(timeout.Milliseconds()), nil
}

// ValidateHookScript checks one script before a frontend saves it, so the user is
// told about a bad working directory while they are looking at the script rather
// than at the next failed install.
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

// RunHookScript executes one script by hand, under the same containment rules a
// real hook run uses — same working-directory resolution, same environment
// allowlist, same timeout kill.
//
// It exists because "does this script actually work?" is the first question a
// mod author asks about a hook, and installing the mod to find out is a poor
// answer. Its output is not returned: a hook's writes go to the manager's log,
// which the console panel reads from the events the run already publishes.
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
