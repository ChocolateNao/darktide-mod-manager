// Package di wires the manager's services for an entry point.
//
// The graph is built by pkg/api (api.NewBundle); this package is the adapter the
// GUI and the CLI use because both need the individual services. A second wiring
// path here would drift from pkg/api unnoticed.
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
// Service fields are pointers: each has one implementation, and the interfaces
// they satisfy are already the seams the services are tested through.
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
// Deliberately smaller than api's option set: an option api does not accept would
// invent policy the public API disagrees with.
type Options struct {
	ConfigPath    string
	GamePath      string
	GameID        string
	CachePath     string
	StoragePath   string
	SourceConfigs map[string]map[string]any
	EventHandler  api.EventHandler
	Logger        api.Logger
	LogLevel      string
	LogDir        string
	LogConsole    bool
	NoLogFile     bool
}

// loggingOptions maps this package's logging fields onto the public API's.
//
// A method so one place knows how an entry point's flags reach the log service.
func (o Options) loggingOptions() api.LoggingOptions {
	opts := api.LoggingOptions{
		Level:   o.LogLevel,
		Dir:     o.LogDir,
		Console: o.LogConsole,
		NoFile:  o.NoLogFile,
	}
	// An all-spaces log dir must be reported: the log service validates it.
	return opts
}

// NewContainer wires the manager and returns it.
//
// A nil context is treated as context.Background: it is a launch-time mistake,
// and refusing it teaches the caller nothing. Nothing is returned unless
// everything was wired, because a half-built container looks usable while, say,
// the database is already closed.
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
// closed handle. Close is idempotent; a second call returns success without
// re-running cleanups.
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
		// One failure is returned unwrapped so a caller can branch on its code.
		return failures[0]
	default:
		return errors.Join(failures...)
	}
}

// Closed reports whether Close has run. It is nil-receiver safe.
func (c *Container) Closed() bool {
	if c == nil {
		// A container that was never built has nothing to use, and callers check
		// before their first use.
		return true
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	return c.closed
}

// errClosed is the error a caller gets for reaching into a closed container.
//
// Its code is INVALID_ARGUMENT: nothing broke, the caller used a released object,
// and that is a programming error.
func errClosed() error {
	return appErrors.New(appErrors.ErrCodeInvalidArgument,
		"container is closed; build a new one instead of reusing a closed container")
}

// normalizePath trims an option path and reports whether there is one.
//
// A path read from a shell argument or a dialog often carries a trailing space,
// and "" here means "use the platform default", so an untrimmed blank would
// replace the user's configuration.
func normalizePath(path string) (string, bool) {
	trimmed := strings.TrimSpace(path)
	return trimmed, trimmed != ""
}
