// Package di wires the manager's services for an entry point.
//
// There is exactly one wiring path, and it is not this package: pkg/api owns it
// (api.NewBundle). This package is the adapter that the GUI and the CLI use,
// because both need the individual services rather than the api.Client that
// wraps them — the Wails bindings expose one method per operation, and the cobra
// commands print the services' own types.
//
// The temptation to rebuild the graph here is worth naming, because it is the
// mistake this file is shaped to prevent. Constructing the pieces directly would
// work until the day pkg/api learned a new step (a source that needs the cache,
// a hook context that needs the config store) and only one of the two paths got
// it. A second wiring path is drift you discover in production; this file maps
// options and reads the result instead.
package di

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/ChocolateNao/darktide-mod-manager/internal/logging"
	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// Container is a wired graph plus the client that wraps it.
//
// The service fields are pointers rather than interfaces on purpose: there is one
// implementation of each and the interfaces they satisfy are already the seams
// the services themselves are tested through. An interface field here would only
// hide which concrete type a nil is supposed to mean.
type Container struct {
	Store          services.ConfigStore
	EventBus       *api.EventBus
	clock          services.Clock
	CacheManager   *services.CacheManager
	HookExecutor   *services.HookExecutor
	Config         *domain.Config
	SourceRegistry *services.SourceRegistry
	GameLocator    *services.GameLocator
	Client         *api.Client
	Logging        *logging.Service
	ModService     *services.ModService
	DatabasePath   string
	StorageRoot    string
	CacheRoot      string
	CleanupFuncs   []func() error
	mu             sync.Mutex
	closed         bool
}

// Options is what an entry point may say about the manager before it starts.
//
// It is deliberately smaller than api's option set. Anything here that api does
// not already accept would mean this package inventing policy that the public
// API then disagrees with; the fields that are here are the ones an entry point
// genuinely decides at launch.
type Options struct {
	// ConfigPath is the configuration file. Empty means the platform default
	// (see platform.CurrentDirs), which is what a normal launch wants.
	ConfigPath string
	// GamePath overrides the configured game folder and is validated before it
	// is stored, so a wrong path fails start-up instead of the first install.
	GamePath string
	// GameID selects the active game by its registry id for this process. An
	// unknown id fails start-up rather than silently running against the
	// configured game; switching the stored default is UseGame's job, not a
	// launch flag's.
	GameID string
	// CachePath overrides where archives are cached.
	CachePath string
	// StoragePath overrides where mod payloads are kept. A headless run (a test,
	// a scripted install) needs a private tree, and without it such a run would
	// write into the user's real storage.
	StoragePath string
	// SourceConfigs configures individual sources, keyed by source id. A block
	// merges into the configuration file's block rather than replacing it, so
	// supplying a token for one source leaves the others alone.
	SourceConfigs map[string]map[string]any
	// EventHandler is subscribed to the event bus at construction. The GUI uses
	// it to refresh a view; the CLI ignores it.
	EventHandler api.EventHandler
	// Logger receives start-up diagnostics, above all the report of a source
	// that could not be configured. It is also handed to the client, so a caller
	// that logs through api.Client.Logf and the wiring phase's own complaint both
	// end up in the same place.
	Logger api.Logger
	// LogLevel overrides logging.level for this run ("debug", "info", ...).
	//
	// It is an override rather than a setting because an entry point decides it
	// from the command line, and a flag is a request for this process rather than
	// a preference to store.
	LogLevel string
	// LogDir overrides where dmm.log is written. It must be absolute; a relative
	// path would depend on whatever directory the user happened to run from, and
	// two runs would quietly keep different logs.
	LogDir string
	// LogConsole mirrors log output to stderr as well as to the file. The CLI
	// turns it on for --verbose, where the point is to watch what happens.
	LogConsole bool
	// NoLogFile disables the file sink, leaving a console-only run. It is what
	// makes a log-heavy command safe to run on a machine where the log
	// directory is not writable.
	NoLogFile bool
}

// loggingOptions maps this package's logging fields onto the public API's.
//
// It is a method rather than a field translation done at the call site so there
// is one place that knows how an entry point's flags reach the log service: a
// GUI that sets the same options as the CLI should not need to remember a second
// spelling of the same intent.
func (o Options) loggingOptions() api.LoggingOptions {
	opts := api.LoggingOptions{
		Level:   o.LogLevel,
		Dir:     o.LogDir,
		Console: o.LogConsole,
		NoFile:  o.NoLogFile,
	}
	// The log directory is not trimmed-and-ignored like the paths above. It is
	// resolved against the filesystem, so a value of nothing but spaces has to be
	// reported rather than silently falling back to the default directory — the
	// user asked for somewhere specific and deserves to be told that they did not
	// get it.
	return opts
}

// NewContainer wires the manager and returns it.
//
// A nil context is tolerated (treated as context.Background) rather than
// refused: it is the sort of mistake made once at launch, and a start-up that
// fails with "context must not be nil" teaches the caller nothing the panic
// would not have.
//
// Nothing is returned unless everything was wired. A half-built container is
// worse than none — its fields would look usable while, say, the database was
// already closed — so every failure closes what it had opened before returning.
func NewContainer(ctx context.Context, opts Options) (*Container, error) {
	apiOptions, err := opts.apiOptions()
	if err != nil {
		return nil, err
	}

	bundle, err := api.NewBundle(ctx, apiOptions...)
	if err != nil {
		return nil, err
	}

	client, err := api.NewClientFromServices(bundle.Deps, opts.clientOptions()...)
	if err != nil {
		// The client refused the services — the only way that happens today is a
		// re-pointing option, which clientOptions never supplies, but the bundle
		// owns open handles either way.
		_ = bundle.Close()
		return nil, err
	}

	return &Container{
		ModService:     bundle.Deps.Mods,
		SourceRegistry: bundle.Deps.Sources,
		GameLocator:    bundle.Deps.Game,
		CacheManager:   bundle.Deps.Cache,
		HookExecutor:   bundle.Deps.Hooks,
		Config:         bundle.Config,
		Store:          bundle.Store,
		Client:         client,
		EventBus:       bundle.Deps.Events,
		Logging:        bundle.Logging,
		DatabasePath:   bundle.DatabasePath,
		CacheRoot:      bundle.CacheRoot,
		StorageRoot:    bundle.StorageRoot,
		CleanupFuncs:   bundle.Cleanups,
		clock:          bundle.Deps.Clock,
	}, nil
}

// Close releases everything the container owns, newest first.
//
// The client is closed before the services, and that order is the point: a
// client cleanup may legitimately want to use a service (flushing a log, telling
// a handler the manager is going away), and closing the database first would
// hand it a closed handle.
//
// Close is idempotent. The second call reports success rather than re-running
// cleanups, because a double-close of a SQLite handle is not something an entry
// point should have to reason about, and "closing again failed" is not a fact
// anyone can act on.
func (c *Container) Close() error {
	if c == nil {
		// A nil container is what a caller holds when construction failed and it
		// deferred the close anyway. Nothing was opened, so there is nothing to
		// report; refusing here would force every caller to nil-check before a
		// defer that can only ever be right.
		return nil
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}

	c.closed = true
	client := c.Client
	cleanups := c.CleanupFuncs
	c.CleanupFuncs = nil
	c.mu.Unlock()

	var failures []error

	if client != nil {
		if err := client.Close(); err != nil {
			failures = append(failures, err)
		}
	}

	for i := len(cleanups) - 1; i >= 0; i-- {
		if cleanups[i] == nil {
			continue
		}

		if err := cleanups[i](); err != nil {
			failures = append(failures, err)
		}
	}

	switch len(failures) {
	case 0:
		return nil
	case 1:
		// One failure is returned as it is so a caller can still branch on the
		// error code; wrapping it would bury the only information there is.
		return failures[0]
	default:
		return errors.Join(failures...)
	}
}

// Closed reports whether Close has run. It is nil-receiver safe.
func (c *Container) Closed() bool {
	if c == nil {
		// A container that was never built is not a closed container, but there
		// is nothing to use either way, and a caller checking before its first
		// use should not have to nil-check separately.
		return true
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	return c.closed
}

// errClosed is the error a caller gets for reaching into a container that has
// already been closed.
//
// Its code is INVALID_ARGUMENT rather than something failure-shaped: nothing
// about the manager broke, the caller used a released object, and that is a
// programming error rather than an operational one.
func errClosed() error {
	return appErrors.New(appErrors.ErrCodeInvalidArgument,
		"container is closed; build a new one instead of reusing a closed container")
}

// normalizePath trims an option path and reports whether there is one.
//
// The trimming matters: a path read from a shell argument or a dialog often
// carries a trailing space, and "" here means "use the platform default", so an
// untrimmed blank would silently replace the user's configuration with a
// default instead of being recognised as no opinion at all.
func normalizePath(path string) (string, bool) {
	trimmed := strings.TrimSpace(path)
	return trimmed, trimmed != ""
}
