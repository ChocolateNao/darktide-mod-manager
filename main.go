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
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// The logger writes to standard error; it does not write to a file or the
	// webview: start-up diagnostics must be readable when the window never opens.
	logger := api.LoggerFunc(func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	})

	// The container is built before wails.Run, before the window exists, because
	// the command sets must bind to something; a config that cannot load is fatal.
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

	// The command sets log through the manager's own logger, because every failed
	// command is what a user pastes into a support request and stderr is not read.
	commandLogger := api.NewZerologLogger(container.Logging.Logger())

	app := commands.New(commands.Deps{
		Client:  container.Client,
		Logger:  commandLogger,
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
		DragAndDrop: &options.DragAndDrop{
			EnableFileDrop: true,
		},
		OnStartup: func(ctx context.Context) {
			startNotifications(ctx)
			forwardEvents(ctx, container.Client)
			app.Startup(ctx)
		},
		// Wails hands OnShutdown a context the shutdown path has already
		// cancelled, so it is dropped; cancelling the command scope needs none.
		OnShutdown: func(ctx context.Context) {
			// CleanupNotifications closes the D-Bus connection the notification
			// channel opened on, which process exit would not do tidily on Linux.
			wailsruntime.CleanupNotifications(ctx)
			app.Shutdown()
		},
		Bind: []any{
			app.Mods,
			app.Sources,
			app.Game,
			app.Cache,
			app.Config,
			app.Logs,
			app.P2PShares,
			app.Actions,
			app.SourceLinks,
			app.Paths,
			&lifecycle{app: app},
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "darktide-mod-manager stopped: %v\n", err)
		os.Exit(1)
	}
}

// forwardEvents republishes every manager event into the webview under one name,
// so the frontend has a single listener and a single unsubscribe. The handler
// only marshals: the bus delivers on the publishing goroutine, so a slow
// subscriber there would stall the install reporting progress. The subscription
// is left alone — the client owns the bus and its Close tears everything down.
func forwardEvents(ctx context.Context, client *api.Client) {
	if client == nil {
		return
	}

	client.Events().Subscribe(func(e api.Event) {
		wailsruntime.EventsEmit(ctx, webviewEventName, e)
	})
}

// webviewEventName is the single Wails event channel the manager publishes on.
const webviewEventName = "mod-manager:event"

// startNotifications opens the native notification channel: InitializeNotifications establishes the connection the
// JavaScript side borrows.
func startNotifications(ctx context.Context) {
	if err := wailsruntime.InitializeNotifications(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "native notifications unavailable: %v\n", err)
	}
}

// lifecycle is the part of the application the frontend needs that is not a
// command: the event and hook vocabulary it must know before it can draw, and the
// cancel button that stops a download. It is bound separately from App because
// App.Startup takes a context.Context, which the binding generator rejects.
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

// HookTimeout reports how long a script may run, in milliseconds. That is the
// unit the frontend's setTimeout takes; converting in the browser would put a
// rounding step between the manager's decision and the timer.
func (l *lifecycle) HookTimeout(script api.HookScript, mod *api.Mod) (float64, error) {
	return l.app.HookTimeout(script, mod)
}

// ValidateHookScript checks a script before the user saves it, so the settings
// form can refuse a broken one without a round trip through a real hook run.
func (l *lifecycle) ValidateHookScript(script api.HookScript, mod *api.Mod) error {
	return l.app.ValidateHookScript(script, mod)
}

// RunHookScript executes one script under the same containment rules a real hook
// run uses. It is bound so a mod author can ask "does this work?" from the editor
// instead of installing the mod. Output is not returned: a hook's writes go to the
// manager's log, which the console panel already reads from the hook events.
func (l *lifecycle) RunHookScript(script api.HookScript, mod *api.Mod) error {
	return l.app.RunHookScript(script, mod)
}

// CancelAll stops every operation in flight.
func (l *lifecycle) CancelAll() { l.app.CancelAll() }

// Stopped reports whether the application is shutting down.
func (l *lifecycle) Stopped() bool { return l.app.Stopped() }
