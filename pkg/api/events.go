package api

import (
	"sync"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
)

// EventType names a thing that happened.
//
// The string values are part of this package's contract: a subscriber written
// against `mod.install.completed` keeps working across releases, so renaming a
// constant is a breaking change and the values are mirrored from
// services.EventType rather than invented here. Two of them are also worth
// reading twice — EventModInstallProgress is the only event a handler should
// expect in bursts, and EventLog carries anything the manager wants to say that
// has no more specific type.
type EventType string

const (
	// EventModInstallStarted is published before any work begins, so a UI can
	// show the mod as "installing" immediately rather than after the download.
	EventModInstallStarted EventType = "mod.install.started"
	// EventModInstallProgress reports movement through a long install.
	EventModInstallProgress EventType = "mod.install.progress"
	// EventModInstallCompleted carries the finished mod in Data.
	EventModInstallCompleted EventType = "mod.install.completed"
	// EventModInstallFailed carries the error that stopped the install.
	EventModInstallFailed EventType = "mod.install.failed"

	EventModUninstallStarted   EventType = "mod.uninstall.started"
	EventModUninstallCompleted EventType = "mod.uninstall.completed"

	EventModEnabled  EventType = "mod.enabled"
	EventModDisabled EventType = "mod.disabled"

	EventModUpdated         EventType = "mod.updated"
	EventModUpdateStarted   EventType = "mod.update.started"
	EventModUpdateCompleted EventType = "mod.update.completed"

	// EventHookStarted / EventHookFinished / EventHookFailed bracket a single
	// hook script. A hook that runs for minutes is the main reason a manager
	// looks hung, so these exist even though nothing in the mod lifecycle
	// depends on them.
	EventHookStarted  EventType = "hook.started"
	EventHookFinished EventType = "hook.finished"
	EventHookFailed   EventType = "hook.failed"

	EventCacheDownloaded EventType = "cache.downloaded"
	EventCachePruned     EventType = "cache.pruned"
	EventCacheCleared    EventType = "cache.cleared"

	// EventLog carries free-form text. Data is a string.
	EventLog EventType = "log"
)

// eventTypes lists every event this package documents, so a test can assert the
// services layer and the public layer agree without a second hand-written list
// that can drift.
var eventTypes = []EventType{
	EventModInstallStarted, EventModInstallProgress, EventModInstallCompleted,
	EventModInstallFailed, EventModUninstallStarted, EventModUninstallCompleted,
	EventModEnabled, EventModDisabled, EventModUpdated, EventModUpdateStarted,
	EventModUpdateCompleted, EventHookStarted, EventHookFinished, EventHookFailed,
	EventCacheDownloaded, EventCachePruned, EventCacheCleared, EventLog,
}

// EventTypes returns every event type the manager can publish, in a fresh slice
// so a caller sorting it cannot affect anyone else.
func EventTypes() []EventType {
	out := make([]EventType, len(eventTypes))
	copy(out, eventTypes)

	return out
}

// Event is one notification.
//
// ModID and SourceID are pulled out of Data's sibling fields rather than left
// buried in it so a subscriber can filter on them without unpacking a map — the
// common case is a progress window that shows one mod's events and ignores the
// rest.
//
// Data's concrete type depends on Type and is not part of this package's
// contract beyond "something printable": EventModInstallCompleted carries a
// domain.Mod, EventLog carries a string, the hook events carry an unexported
// struct. Treat it as opaque unless you are the manager itself.
type Event struct {
	Type      EventType
	Timestamp time.Time
	Data      any
	ModID     string
	SourceID  string
}

// EventHandler receives events. It runs on the goroutine that published the
// event, so a handler that blocks delays the operation that produced it — see
// EventBus.Publish.
type EventHandler func(Event)

// EventBus fans events out to subscribers.
//
// It implements services.EventSink, which is how a single client gets both
// roles: the services publish to it and the API's subscribers read from it, so
// there is no second, parallel path a caller could miss an event on.
type EventBus struct {
	handlers map[uint64]EventHandler
	now      func() time.Time
	order    []uint64
	nextID   uint64
	mu       sync.RWMutex
}

// NewEventBus returns an empty bus. The zero value is not usable; a Client
// always supplies one.
func NewEventBus() *EventBus {
	return &EventBus{handlers: make(map[uint64]EventHandler), now: time.Now}
}

// WithClock replaces the timestamp source. It is for tests: an event's
// Timestamp is the only part of an Event the bus controls, and without this a
// test can only assert it is "roughly now".
func (b *EventBus) WithClock(now func() time.Time) *EventBus {
	if b == nil {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if now == nil {
		now = time.Now
	}

	b.now = now

	return b
}

// Subscribe registers a handler and returns the function that removes it.
//
// The unsubscribe function is returned rather than the handler's id because a
// caller almost always wants `defer unsubscribe()`, and a public id invites
// bookkeeping the closure makes unnecessary. Calling it more than once is
// harmless, which matters because the obvious mistake is calling it both from a
// defer and from a shutdown path.
//
// A nil handler is refused by returning a no-op unsubscribe rather than an
// error: there is nothing useful a caller can do about it, and failing here
// would make "subscribe if you have a handler" the caller's problem.
func (b *EventBus) Subscribe(handler EventHandler) func() {
	if b == nil {
		return func() {}
	}

	if handler == nil {
		return func() {}
	}

	b.mu.Lock()
	id := b.nextID

	b.nextID++
	if b.handlers == nil {
		b.handlers = make(map[uint64]EventHandler)
	}

	b.handlers[id] = handler
	b.order = append(b.order, id)
	b.mu.Unlock()

	var once sync.Once

	return func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()

			if _, live := b.handlers[id]; !live {
				return
			}

			delete(b.handlers, id)

			for i, existing := range b.order {
				if existing == id {
					b.order = append(b.order[:i:i], b.order[i+1:]...)
					break
				}
			}
		})
	}
}

// HandlerCount reports how many handlers are subscribed. It exists so a test can
// assert that unsubscribing actually removed one.
func (b *EventBus) HandlerCount() int {
	if b == nil {
		return 0
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	return len(b.handlers)
}

// Publish delivers a service event to every subscriber, stamping the public
// Event with a timestamp on the way through.
//
// Delivery is synchronous and in subscription order. That is a deliberate
// choice: an asynchronous bus would let a subscriber see mod.install.completed
// before mod.install.started, and a progress UI that renders out of order is
// worse than one that renders slightly late. The cost is that a handler which
// blocks delays the operation publishing it, which is why the EventHandler
// documentation says so plainly.
//
// A handler that panics does not stop the other handlers, and does not take
// down the operation that published: the manager is halfway through an install
// and a third-party subscriber's bug is not a reason to leave a mod half
// deployed. The panic is recovered and dropped, which trades a visible crash in
// the subscriber for a silent one — the subscriber's own test suite is where
// that belongs.
func (b *EventBus) Publish(e services.Event) {
	if b == nil {
		return
	}

	b.mu.RLock()
	now := b.now

	handlers := make([]EventHandler, 0, len(b.order))
	for _, id := range b.order {
		if h, ok := b.handlers[id]; ok {
			handlers = append(handlers, h)
		}
	}

	b.mu.RUnlock()

	if len(handlers) == 0 || now == nil {
		return
	}

	event := Event{
		Type:      EventType(e.Type),
		Timestamp: now(),
		Data:      e.Data,
		ModID:     e.ModID,
		SourceID:  e.SourceID,
	}
	// The snapshot is taken under the read lock and delivered under none, so a
	// handler may subscribe or unsubscribe — including unsubscribing itself —
	// without deadlocking. A handler added by another handler mid-delivery is not
	// in this snapshot, so it does not receive the event that is already in
	// flight, but it does receive the next one.
	for _, h := range handlers {
		deliver(h, event)
	}
}

// deliver calls one handler, containing its panic.
func deliver(h EventHandler, event Event) {
	defer func() {
		// The value is discarded deliberately: there is no logger here, and
		// logging from inside a recover would risk the same failure again.
		_ = recover()
	}()

	h(event)
}
