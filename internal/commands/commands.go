// Package commands exposes the manager's operations to a GUI as plain exported
// methods on a handful of structs.
//
// A Wails frontend binds to whatever methods the structs handed to wails.Run
// expose, so the shape of this package is dictated by that: a struct per
// subject, exported methods, and every argument and result a type the JavaScript
// side can hold. It holds nothing of its own — every method is one call into
// pkg/api — because the moment this package started deciding anything, the CLI
// and the GUI would be answering different questions.
//
// Three decisions shape this package:
//
//   - Contexts. Every call is derived from a cancellable scope the Wails
//     lifecycle and the UI both hold, see base.ready.
//   - Errors. Every method returns an error as its last result, and a
//     value-returning method returns the zero value beside it. Wails turns a
//     returned error into a rejected promise, so the frontend never receives a
//     half-filled result, and a Go caller gets the same guarantee.
//   - Nil handling. A command struct built without a client is a wiring
//     mistake, and every method reports it rather than panicking on the first
//     click: a frontend that cannot render an error dialog is worse than one
//     that never starts.
package commands

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/logging"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// DefaultTimeout bounds a single operation. It is deliberately generous: a
// timeout mid-download leaves a mod that is neither installed nor absent. It
// only stops a call that can never finish. A non-positive value means "no
// deadline" and a caller needing longer raises it in Deps.
const DefaultTimeout = 30 * time.Minute

// Deps is what a command set needs to do its job. Anything added here that api
// does not already accept would be a policy this package invented and pkg/api
// then contradicted.
type Deps struct {
	Logger  api.Logger
	Client  *api.Client
	Logging *logging.Service
	Timeout time.Duration
}

// base is the state every command set shares: the client, the logger, the
// timeout, and the cancellable scope. The scope is why this type exists — every
// operation ends in ErrCodeOperationCanceled when it is cancelled, which the
// frontend can tell from a real failure.
type base struct {
	logger  api.Logger
	scope   context.Context
	client  *api.Client
	logging *logging.Service
	stop    context.CancelFunc
	timeout time.Duration
	mu      sync.RWMutex
	stopped bool
}

// newBase normalises the dependencies and creates the initial scope, parented
// to context.Background() so a command set built without a lifecycle context —
// a test, or a headless caller — behaves exactly like one that was given one.
func newBase(deps Deps) *base {
	timeout := deps.timeout()
	b := &base{client: deps.Client, logger: deps.Logger, logging: deps.Logging, timeout: timeout}
	b.reset(context.Background())

	return b
}

// reset replaces the scope with a fresh child of parent, cancelling the old one
// first: every context derived from it would otherwise stay live forever. The
// stopped flag is untouched, so CancelAll revives the scope without reviving a
// set that Shutdown closed.
func (b *base) reset(parent context.Context) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.stop != nil {
		b.stop()
	}

	if parent == nil {
		parent = context.Background()
	}

	b.scope, b.stop = context.WithCancel(parent)
}

// Deps.timeout normalises the configured timeout. Zero means the default and a
// negative value means no deadline, which are different intentions from the same
// zero-valued struct.
func (d Deps) timeout() time.Duration {
	switch {
	case d.Timeout < 0:
		return 0
	case d.Timeout == 0:
		return DefaultTimeout
	default:
		return d.Timeout
	}
}

// SetContext rebinds the scope to a new parent, which is what a Wails startup
// hook hands over. Operations already running under the old scope keep their own
// derived contexts, so a restart tears down nothing in flight.
func (b *base) SetContext(ctx context.Context) {
	if b == nil {
		return
	}

	b.reset(ctx)
	b.mu.Lock()
	b.stopped = false
	b.mu.Unlock()
}

// Context reports the context operations currently run under. It is never nil.
func (b *base) Context() context.Context {
	if b == nil {
		return context.Background()
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.scope == nil {
		return context.Background()
	}

	return b.scope
}

// CancelAll stops whatever is running without shutting the set down. This is the
// "Stop" button: cancelling the scope makes every in-flight operation return
// ErrCodeOperationCanceled and leaves the command set usable, which is what a
// user pressing stop means.
func (b *base) CancelAll() {
	if b == nil {
		return
	}

	b.mu.RLock()
	stop := b.stop
	b.mu.RUnlock()

	if stop != nil {
		stop()
	}
	// The scope is spent, so the next call needs a live one. Creating it here
	// rather than in ready() keeps CancelAll from being the thing that has to
	// decide whether it is safe to revive a shut-down set.
	b.reset(context.Background())
}

// Shutdown cancels everything in flight and refuses further calls. It is
// idempotent and does not close the client: the container owns that, and a
// command set holding a second closer would produce two different orders of
// shutdown depending on which one ran.
func (b *base) Shutdown() {
	if b == nil {
		return
	}

	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		return
	}

	b.stopped = true
	stop := b.stop
	b.mu.Unlock()

	if stop != nil {
		stop()
	}
}

// Stopped reports whether Shutdown has been called.
func (b *base) Stopped() bool {
	if b == nil {
		return true
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	return b.stopped
}

// Timeout reports the per-operation bound in force, or zero when there is none.
func (b *base) Timeout() time.Duration {
	if b == nil {
		return DefaultTimeout
	}

	return b.timeout
}

// ready returns the client and a per-call context, or the error to report. The
// order matters for what the user sees: shut-down is reported before a missing
// client, because a caller that shut down did so on purpose.
func (b *base) ready() (*api.Client, context.Context, context.CancelFunc, error) {
	if b == nil {
		return nil, nil, nil, errUninitialised()
	}

	b.mu.RLock()
	stopped := b.stopped
	scope := b.scope
	timeout := b.timeout
	client := b.client
	b.mu.RUnlock()

	if stopped {
		return nil, nil, nil, errShutDown()
	}

	if client == nil {
		return nil, nil, nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"commands were built without an api client; check the wiring in main")
	}

	if scope == nil {
		scope = context.Background()
	}

	if timeout <= 0 {
		// No deadline: the scope alone bounds the call, so a caller that asked
		// for none is not given one behind its back.
		return client, scope, func() {}, nil
	}

	ctx, cancel := context.WithTimeout(scope, timeout)

	return client, ctx, cancel, nil
}

// errUninitialised and errShutDown are the two complaints that belong to this
// package rather than to the manager, so they are built in one place.
func errUninitialised() error {
	return appErrors.New(appErrors.ErrCodeInvalidArgument,
		"commands are not initialised")
}

func errShutDown() error {
	return appErrors.New(appErrors.ErrCodeInvalidArgument,
		"the application is shutting down; no further commands are accepted")
}

// logf records one line about an operation that did not succeed. It is
// best-effort: a logger that panics must not be able to turn a reported failure
// into a crash, and the error the caller receives is the one that matters.
func (b *base) logf(format string, args ...any) {
	if b == nil || b.logger == nil {
		return
	}

	defer func() { _ = recover() }()

	b.logger.Printf(format, args...)
}

// fail logs and returns an error, which is the shape almost every method wants
// and which keeps the logging decision in one place.
func (b *base) fail(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}

	detail := strings.TrimSpace(format)
	if detail != "" {
		b.logf("%s: %v", detail, err)
	} else {
		b.logf("%v", err)
	}

	return err
}
