package main

import (
	"context"
	"embed"
	"fmt"
	"os"

	"github.com/ChocolateNao/darktide-mod-manager/internal/commands"
	"github.com/ChocolateNao/darktide-mod-manager/internal/di"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// The logger writes to standard error rather than to a file or to the
	// webview: start-up diagnostics have to be readable when the window never
	// opens, which is exactly when they matter most.
	logger := api.LoggerFunc(func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	})

	// The container is built before wails.Run, and therefore before the window
	// exists, because the command sets have to be bound to something and a
	// window that comes up with nothing behind it would show an empty interface
	// and no explanation. A configuration that cannot be loaded is fatal here
	// for the same reason: the alternative is a running window that cannot do
	// anything.
	container, err := di.NewContainer(context.Background(), di.Options{Logger: logger})
	if err != nil {
		fmt.Fprintf(os.Stderr, "darktide-mod-manager cannot start: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if err := container.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", err)
		}
	}()

	app := commands.New(commands.Deps{
		Client:  container.Client,
		Logger:  logger,
		Logging: container.Logging,
	})

	err = wails.Run(&options.App{
		Title:     "Darktide Mod Manager",
		Width:     1280,
		Height:    800,
		MinWidth:  940,
		MinHeight: 600,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		OnStartup:        app.Startup,
		// Wails passes a context to OnShutdown that the shutdown path has
		// already cancelled by the time it is called — the runtime is tearing
		// the window down — so it is dropped rather than stored. Cancelling the
		// command scope is the whole job here, and that needs no context.
		OnShutdown: func(context.Context) { app.Shutdown() },
		Bind: []any{
			app.Mods,
			app.Sources,
			app.Game,
			app.Cache,
			app.Config,
			app.Logs,
			&lifecycle{app: app},
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "darktide-mod-manager stopped: %v\n", err)
		os.Exit(1)
	}
}

// lifecycle is the part of the application the frontend needs that is not a
// command: the event and hook vocabulary it has to know about before it can
// draw anything, and the cancel button that stops a download in flight.
//
// It is a separate bound object rather than the commands.App itself because
// App.Startup takes a context.Context, which cannot cross the Wails bridge — the
// bridge has no way to hand a Go context to a JavaScript call, and the binding
// generator rejects the type outright.
type lifecycle struct {
	app *commands.App
}

// EventTypes lists the event names the frontend may subscribe to.
func (l *lifecycle) EventTypes() []string { return l.app.EventTypes() }

// HookTypes lists the hook script types the manager can run.
func (l *lifecycle) HookTypes() []string { return l.app.HookTypes() }

// HookPhases lists the lifecycle phases a hook can be attached to.
func (l *lifecycle) HookPhases() []string { return l.app.HookPhases() }

// SupportsHookType reports whether the manager can run a script of this type.
func (l *lifecycle) SupportsHookType(name string) bool {
	return l.app.SupportsHookType(name)
}

// HookTimeout reports how long a script may run, in milliseconds.
//
// Milliseconds rather than seconds because that is what the frontend's
// setTimeout takes, and converting in the browser would put a rounding step
// between the number the manager decided and the number the timer uses.
func (l *lifecycle) HookTimeout(script api.HookScript, mod *api.Mod) (float64, error) {
	return l.app.HookTimeout(script, mod)
}

// ValidateHookScript checks a script before the user saves it, so the settings
// form can refuse a broken one without a round trip through a real hook run.
func (l *lifecycle) ValidateHookScript(script api.HookScript, mod *api.Mod) error {
	return l.app.ValidateHookScript(script, mod)
}

// CancelAll stops every operation in flight.
func (l *lifecycle) CancelAll() { l.app.CancelAll() }

// Stopped reports whether the application is shutting down.
func (l *lifecycle) Stopped() bool { return l.app.Stopped() }
