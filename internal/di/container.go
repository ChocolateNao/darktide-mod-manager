// Package di wires the manager's services for an entry point.
//
// The graph itself is built by pkg/api (api.NewBundle); this package is the
// adapter the GUI and the CLI use, because both need the individual services
// rather than the api.Client that wraps them. Building a second wiring path here
// is drift nobody discovers until pkg/api learns a new step and only one of the
// two gets it.
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
// implementation of each, and the interfaces they satisfy are already the seams
// the services are tested through.
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
// It is deliberately smaller than api's option set: anything here api does not
// already accept would mean this package inventing policy the public API then
// disagrees with.
type Options struct {
	// ConfigPath is the configuration file. Empty means the platform default.
	ConfigPath string
	// GamePath overrides the configured game folder, validated before it is
	// stored so a wrong path fails start-up instead of the first install.
	GamePath string
	// GameID selects the active game for this process; an unknown id fails
	// start-up. Changing the stored default is UseGame's job.
	GameID string
	// CachePath overrides where archives are cached.
	CachePath string
	// StoragePath overrides where mod payloads are kept. A headless run needs a
	// private tree, or it writes into the user's real storage.
	StoragePath string
	// SourceConfigs configures individual sources, keyed by source id. A block
	// merges into the file's block, so one source's token leaves the others alone.
	SourceConfigs map[string]map[string]any
	// EventHandler is subscribed to the event bus at construction. The GUI uses
	// it to refresh a view; the CLI ignores it.
	EventHandler api.EventHandler
	// Logger receives start-up diagnostics, above all the report of a source that
	// could not be configured. It is handed to the client too, so Client.Logf and
	// the wiring phase's own complaint land in the same place.
	Logger api.Logger
	// LogLevel overrides logging.level for this run ("debug", "info", ...). It is
	// an override rather than a setting because a flag is a request for this
	// process rather than a preference to store.
	LogLevel string
	// LogDir overrides where dmm.log is written. It must be absolute: a relative
	// path would depend on the directory the user happened to run from.
	LogDir string
	// LogConsole mirrors log output to stderr as well as to the file.
	LogConsole bool
	// NoLogFile disables the file sink, leaving a console-only run. It is what
	// makes a log-heavy command safe where the log directory is not writable.
	NoLogFile bool
}

// loggingOptions maps this package's logging fields onto the public API's.
//
// It is a method so there is one place that knows how an entry point's flags
// reach the log service, rather than a second spelling per surface.
func (o Options) loggingOptions() api.LoggingOptions {
	opts := api.LoggingOptions{
		Level:   o.LogLevel,
		Dir:     o.LogDir,
		Console: o.LogConsole,
		NoFile:  o.NoLogFile,
	}
	// Unlike the paths above, an all-spaces log dir must be reported, not replaced.
	return opts
}

// NewContainer wires the manager and returns it.
//
// A nil context is treated as context.Background rather than refused: it is the
// sort of mistake made once at launch, and "context must not be nil" teaches the
// caller nothing. Nothing is returned unless everything was wired, because a
// half-built container looks usable while, say, the database is already closed.
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
		// The client refused the services, but the bundle owns open handles either
		// way.
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
// The client is closed before the services because a client cleanup may
// legitimately want to use a service; closing the database first would hand it a
// closed handle. Close is idempotent: the second call reports success rather
// than re-running cleanups.
func (c *Container) Close() error {
	if c == nil {
		// A defer after a failed construction holds nil; nothing was opened.
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
		// A container that was never built has nothing to use either way, and a
		// caller checking before its first use should not nil-check separately.
		return true
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	return c.closed
}

// errClosed is the error a caller gets for reaching into a closed container.
//
// Its code is INVALID_ARGUMENT rather than something failure-shaped: nothing
// broke, the caller used a released object, and that is a programming error.
func errClosed() error {
	return appErrors.New(appErrors.ErrCodeInvalidArgument,
		"container is closed; build a new one instead of reusing a closed container")
}

// normalizePath trims an option path and reports whether there is one.
//
// A path read from a shell argument or a dialog often carries a trailing space,
// and "" here means "use the platform default", so an untrimmed blank would
// replace the user's configuration with a default instead of meaning nothing.
func normalizePath(path string) (string, bool) {
	trimmed := strings.TrimSpace(path)
	return trimmed, trimmed != ""
}
