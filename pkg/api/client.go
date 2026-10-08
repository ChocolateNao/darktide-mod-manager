// Package api is the mod manager's public Go surface.
//
// It is a thin, stable facade over the service layer: a client, a set of
// operations grouped the way a caller thinks about them (mods, sources, game,
// cache, hooks, configuration), an event bus, and the error codes those
// operations can produce. Nothing in here implements behaviour — the services
// own that — so the API can change shape without the manager changing meaning.
//
// # Construction
//
// NewClient wires everything and returns a ready client. Options cover the three
// paths that differ between machines, the per-source configuration, an event
// handler and a logger:
//
//	client, err := api.NewClient(ctx,
//	    api.WithConfigPath("/etc/darktide/config.json"),
//	    api.WithGamePath("C:/Games/Darktide"),
//	    api.WithSourceConfig("vortex", map[string]any{"api_key": "…"}),
//	    api.WithEventHandler(func(e api.Event) { log.Printf("%s %s", e.Type, e.ModID) }),
//	)
//	if err != nil {
//	    return err
//	}
//	defer client.Close()
//
// NewClientFromServices is for callers inside the module that already hold the
// services — the GUI and the CLI, via internal/di. It exists so the wiring
// happens once: a second composition root would drift, and the two would end up
// disagreeing about where the configuration lives.
//
// # Errors
//
// Every operation returns an error carrying a Code. Branch on the code with
// IsAppError rather than matching message text:
//
//	if api.IsAppError(err, api.ErrCodeModNotFound) {
//	    // the source does not have it, or it was never installed
//	}
//
// # Context
//
// Every method takes a context first and honours its deadline and cancellation.
// A cancelled operation reports ErrCodeOperationCanceled and leaves no partial
// state behind: an interrupted install removes its staging directory and writes
// no database row.
//
// # Events
//
// Long-running operations publish to the client's bus. Subscribe before doing
// work; a handler runs on the goroutine that published the event.
package api

import (
	"errors"
	"sync"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// Config is the manager's configuration. It is an alias for the domain type
// rather than a wrapper, so a value read from here can be handed to any other
// package that speaks domain.Config without a conversion step.
type Config = domain.Config

// CacheInfo describes what the download cache currently holds. It is an alias
// for the domain type for the same reason Config is.
type CacheInfo = domain.CacheInfo

// Mod is a mod. It is an alias so that a caller never has to import two packages
// to hold and describe one mod.
type Mod = domain.Mod

// ModSourceInfo describes a registered source's capabilities. An alias for the
// same reason.
type ModSourceInfo = domain.ModSourceInfo

// GameInstallation describes one detected Darktide install.
type GameInstallation = domain.GameInstallation

// ClientDeps is the set of services a client drives.
//
// The fields are the concrete service types rather than the narrow interfaces
// the services themselves depend on. That is a deliberate trade: ClientDeps is a
// wiring type used by the composition root, not a surface a library consumer
// implements, and narrowing it would mean restating a dozen one-method
// interfaces that nothing would ever satisfy except the real services. An
// external consumer builds a client with NewClient instead.
type ClientDeps struct {
	Mods    *services.ModService
	Sources *services.SourceRegistry
	Game    *services.GameLocator
	Cache   *services.CacheManager
	Hooks   *services.HookExecutor
	Config  services.ConfigStore
	// Events receives everything the services publish. When nil the client
	// creates its own bus, so a caller wiring services by hand still gets
	// events.
	Events *EventBus
	// Clock is the services' time source. nil means time.Now.
	Clock services.Clock
}

// Client is the manager's handle. It is safe for concurrent use.
//
// Every operation checks that the client is open first, so a use-after-Close
// reports ErrCodeInvalidArgument naming the mistake instead of dereferencing a
// closed database.
type Client struct {
	deps      ClientDeps
	logger    Logger
	closeErr  error
	events    *EventBus
	now       func() time.Time
	cleanups  []func() error
	mu        sync.RWMutex
	closeOnce sync.Once
	closed    bool
}

// NewClientFromServices builds a client over already-constructed services.
//
// Only WithEventHandler and WithLogger have any effect here. The path and source
// options are refused rather than ignored: a client built from services has
// already opened a configuration file, a database and a cache, and quietly
// pretending a path option applied would leave the caller believing it did.
func NewClientFromServices(deps ClientDeps, opts ...Option) (*Client, error) {
	set, err := applyOptions(opts)
	if err != nil {
		return nil, err
	}

	if set.configPath != "" || set.gamePath != "" || set.cachePath != "" ||
		set.storagePath != "" || len(set.sourceConfigs) > 0 {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"a client built from services cannot be re-pointed with WithConfigPath, "+
				"WithGamePath, WithCachePath, WithStoragePath or WithSourceConfig; "+
				"use api.NewClient instead")
	}

	events := deps.Events
	if events == nil {
		events = NewEventBus()
		deps.Events = events
	}

	if deps.Clock == nil {
		deps.Clock = time.Now
	}

	logger := set.logger
	if logger == nil {
		logger = discardLogger{}
	}

	c := &Client{deps: deps, events: events, logger: logger, now: deps.Clock}
	for _, handler := range set.handlers {
		events.Subscribe(handler)
	}

	return c, nil
}

// Services returns the dependencies the client drives. It is for callers inside
// the module — internal/commands and the CLI — that need a service this package
// does not wrap. It hands back the same pointers the client uses, so it is not a
// way to obtain a second, independent view.
func (c *Client) Services() ClientDeps {
	if c == nil {
		return ClientDeps{}
	}

	return c.deps
}

// Events returns the client's event bus, so a caller that subscribes later than
// construction can still reach it.
func (c *Client) Events() *EventBus {
	if c == nil {
		return nil
	}

	return c.events
}

// Logger returns the configured logger. It is never nil: a client built without
// WithLogger logs to a sink that discards everything, so a caller can pass the
// result straight to something that wants a logger.
func (c *Client) Logger() Logger {
	if c == nil || c.logger == nil {
		return discardLogger{}
	}

	return c.logger
}

// Logf writes a line through the configured logger.
func (c *Client) Logf(format string, args ...any) {
	if c == nil {
		return
	}

	c.Logger().Printf(format, args...)
}

// Close releases everything the client opened: the database, the cache, any
// hook subprocesses, in the reverse of the order they were acquired.
//
// It is safe to call twice and from more than one goroutine, which matters
// because the natural shape is an explicit Close on the error path plus a
// deferred one. The second call returns the first call's result unchanged so a
// caller that checks both sees the same answer.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}

	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		cleanups := c.cleanups
		c.mu.Unlock()

		var failures []error

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
			c.closeErr = nil
		case 1:
			// Returned unwrapped so a single failure keeps its code: a caller
			// testing for ErrCodeConfigIO after Close should not have to unwrap a
			// join to find it.
			c.closeErr = failures[0]
		default:
			c.closeErr = errors.Join(failures...)
		}
	})

	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.closeErr
}

// Closed reports whether Close has been called.
func (c *Client) Closed() bool {
	if c == nil {
		return true
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.closed
}

// errClosed is what every operation on a closed client returns. It says
// "invalid argument" because that is the closest code this package has, and it
// says so in the message because a caller reaching it has a lifecycle bug rather
// than bad data — nothing about their request is wrong.
func errClosed() error {
	return appErrors.New(appErrors.ErrCodeInvalidArgument,
		"client is closed; create a new one instead of reusing a closed client")
}

// ready reports whether the client can be used, and the error to return when it
// cannot. It covers both the closed case and a nil client, so an operation on a
// nil *Client reports the same thing instead of panicking — a nil client reaches
// a caller when a constructor returned an error and the error was ignored.
func (c *Client) ready() error {
	if c == nil {
		return errClosed()
	}

	c.mu.RLock()
	closed := c.closed
	c.mu.RUnlock()

	if closed {
		return errClosed()
	}

	return nil
}

// clock returns the client's time source, tolerating a nil client so a test can
// stamp events without a full graph.
func (c *Client) clock() func() time.Time {
	if c == nil || c.now == nil {
		return time.Now
	}

	return c.now
}

// discardLogger is the logger a client uses when the caller supplied none. It is
// a type rather than a nil check at every call site so that Logger() can promise
// to return something usable.
type discardLogger struct{}

func (discardLogger) Printf(string, ...any) {}

// Compile-time proof that the bus satisfies the services' sink. If a future
// change to either signature breaks this, the failure belongs here rather than
// at a wiring site.
var _ services.EventSink = (*EventBus)(nil)
