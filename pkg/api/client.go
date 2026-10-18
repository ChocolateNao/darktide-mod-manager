// Package api is the mod manager's public Go surface: a thin, stable facade
// over the service layer holding a client, the operations a caller needs, an
// event bus, and the error codes those operations can produce. Nothing here
// implements behaviour — the services own that — so the API can change shape
// without the manager changing meaning.
//
// NewClient wires everything and returns a ready client; its options cover the
// paths that differ between machines, per-source configuration, an event handler
// and a logger:
//
//	client, err := api.NewClient(ctx, api.WithConfigPath(p), api.WithGamePath(g))
//	if err != nil {
//		return err
//	}
//	defer client.Close()
//
// NewClientFromServices is for callers inside the module that already hold the
// services — the GUI and the CLI, via internal/di. It exists so the wiring
// happens once: a second composition root would drift, and the two would end up
// disagreeing about where the configuration lives.
//
// Every method takes a context first and honours its deadline and cancellation;
// a cancelled operation reports ErrCodeOperationCanceled and leaves no partial
// state. Every operation returns an error carrying a Code, so branch with
// IsAppError rather than matching message text. Long-running operations publish
// to the client's bus: subscribe before doing work, because a handler runs on
// the publishing goroutine.
package api

import (
	"errors"
	"sync"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/actions"
	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// Config is the manager's configuration, an alias for the domain type so a
// value read from here can be handed to any package that speaks domain.Config.
type Config = domain.Config

// CacheInfo describes what the download cache currently holds. It is an alias
// for the domain type for the same reason Config is.
type CacheInfo = domain.CacheInfo

// Mod is a mod, aliased so a caller never imports two packages to hold and
// describe one mod.
type Mod = domain.Mod

// ModSourceInfo describes a registered source's capabilities. An alias for the same reason.
type ModSourceInfo = domain.ModSourceInfo

// GameInstallation describes one detected Darktide install.
type GameInstallation = domain.GameInstallation

// InstallShareOptions controls how a share is installed.
type InstallShareOptions struct {
	AutoInstall bool
}

// ClientDeps is the set of services a client drives.
//
// The fields are concrete service types rather than the narrow interfaces the
// services depend on: this is a wiring type for the composition root, not a
// surface a library consumer implements.
type ClientDeps struct {
	Mods    *services.ModService
	Sources *services.SourceRegistry
	Game    *services.GameLocator
	Cache   *services.CacheManager
	Hooks   *services.HookExecutor
	// Shares is the deprecated cloud sharing service, declared here rather than behind an
	// interface so the wiring type cannot drift from the composition root that fills it.
	//
	//nolint:staticcheck // deprecation is the point: the field stays reachable until cloud shares can no longer be revoked.
	Shares *services.ShareService
	// P2PShares is the peer-to-peer sharing service: Shares moves archives through
	// a storage provider, P2PShares moves them between two machines.
	P2PShares *services.P2PShareService
	// Actions is the table of code-defined per-game actions; listing them needs
	// only this.
	Actions *actions.Registry
	// ActionRunner executes those actions. Running one also needs a game locator, a
	// desktop opener and a command runner, so a listing-only client can leave it nil.
	ActionRunner *actions.Runner
	// URLs opens a web address in the user's browser. A client without a desktop — a test,
	// a container — can leave it nil and report that it cannot open links, not pretend it did.
	URLs   URLOpener
	Config services.ConfigStore
	// ActiveConfig is the configuration object the wiring loaded and mutated (WithGame's
	// override) — the same pointer its closures capture; UseGame updates it in place.
	ActiveConfig *domain.Config
	// Events receives everything the services publish. When nil the client makes
	// its own bus, so a caller wiring services by hand still gets events.
	Events *EventBus
	// Clock is the services' time source. nil means time.Now.
	Clock services.Clock
}

// Client is the manager's handle, safe for concurrent use. Every operation checks
// the client is open first, so a use-after-Close reports ErrCodeInvalidArgument
// instead of dereferencing a closed database.
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

// NewClientFromServices builds a client over already-constructed services. Path
// and source options are refused, not ignored: the services have already opened a
// configuration file, a database and a cache.
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

// Services returns the dependencies the client drives, for callers inside the
// module that need a service this package does not wrap. It hands back the same
// pointers the client uses, so it is not a second, independent view.
func (c *Client) Services() ClientDeps {
	if c == nil {
		return ClientDeps{}
	}

	return c.deps
}

// Events returns the client's event bus for a caller that subscribes after construction.
func (c *Client) Events() *EventBus {
	if c == nil {
		return nil
	}

	return c.events
}

// Logger returns the configured logger, never nil: a client built without
// WithLogger logs to a sink that discards everything.
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

// Close releases everything the client opened, in reverse acquisition order. Safe
// to call twice and from multiple goroutines; the second call returns the first
// call's result unchanged.
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
			// Unwrapped so a single failure keeps its code: a caller testing for
			// ErrCodeConfigIO should not have to unwrap a join to find it.
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

// errClosed is what every operation on a closed client returns. "Invalid
// argument" is the closest code this package has, and the message says so
// because a caller reaching it has a lifecycle bug, not bad data.
func errClosed() error {
	return appErrors.New(appErrors.ErrCodeInvalidArgument,
		"client is closed; create a new one instead of reusing a closed client")
}

// ready reports the error an operation should return when the client cannot be
// used: closed, or nil because a constructor's error was ignored. Both report the
// same thing instead of panicking.
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

// discardLogger is the logger a client uses when the caller supplied none, kept as
// a type so Logger() can promise to return something usable.
type discardLogger struct{}

func (discardLogger) Printf(string, ...any) {}

// Compile-time proof that the bus satisfies the services' sink, so a signature
// change fails here rather than at a wiring site.
var _ services.EventSink = (*EventBus)(nil)
