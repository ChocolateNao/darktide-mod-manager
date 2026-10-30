package api

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// busNow is the pinned clock every event-bus test stamps with. Event timestamps
// are the only part of an Event the bus controls, so without a fixed clock the
// assertions would have to be "roughly now", which catches nothing.
var busNow = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// collector is a subscriber that records what it was given.
type collector struct {
	events []Event
	mu     sync.Mutex
}

func (c *collector) handle(e Event) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.events = append(c.events, e)
}

func (c *collector) snapshot() []Event {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]Event, len(c.events))
	copy(out, c.events)

	return out
}

func (c *collector) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.events)
}

// newTestBus returns a bus with a pinned clock.
func newTestBus() *EventBus {
	return NewEventBus().WithClock(func() time.Time { return busNow })
}

func TestEventBusDeliversToEverySubscriber(t *testing.T) {
	bus := newTestBus()
	first := &collector{}
	second := &collector{}

	bus.Subscribe(first.handle)
	bus.Subscribe(second.handle)

	bus.Publish(services.Event{
		Type:     services.EventModEnabled,
		ModID:    "better_ui",
		ModName:  "Better UI",
		SourceID: "vortex",
		Data:     "enabled",
	})

	for name, sub := range map[string]*collector{"first": first, "second": second} {
		events := sub.snapshot()
		require.Len(t, events, 1, "%s subscriber got events, want 1", name)

		got := events[0]
		assert.Equal(t, EventModEnabled, got.Type, "%s subscriber got type", name)

		assert.Equal(t, "better_ui", got.ModID, "%s subscriber got mod", name)

		assert.Equal(t, "Better UI", got.ModName, "%s subscriber got mod name", name)

		assert.Equal(t, "vortex", got.SourceID, "%s subscriber got source", name)

		assert.Equal(
			t,
			"enabled",
			got.Data,
			"%s subscriber got data, want the published string",
			name,
		)

		assert.True(t, got.Timestamp.Equal(busNow),
			"%s subscriber got timestamp %v, want the bus clock %v",
			name,
			got.Timestamp,
			busNow,
		)
	}
}

func TestEventBusUnsubscribeStopsDelivery(t *testing.T) {
	bus := newTestBus()
	sub := &collector{}
	unsubscribe := bus.Subscribe(sub.handle)

	bus.Publish(services.Event{Type: services.EventLog, Data: "first"})

	require.Equal(t, 1, sub.len(), "subscriber got events before unsubscribing, want 1")

	unsubscribe()
	bus.Publish(services.Event{Type: services.EventLog, Data: "second"})

	assert.Equal(
		t,
		1,
		sub.len(),
		"subscriber got events after unsubscribing, want the 1 from before",
	)

	assert.Equal(t, 0, bus.HandlerCount(), "bus reports handlers after unsubscribing, want 0")
}

func TestEventBusUnsubscribeIsIdempotent(t *testing.T) {
	// The natural mistake is an explicit unsubscribe plus a deferred one. The
	// second call must not remove somebody else's handler or panic.
	bus := newTestBus()
	sub := &collector{}
	bus.Subscribe(sub.handle)
	unsubscribe := bus.Subscribe(sub.handle)

	unsubscribe()
	unsubscribe()
	unsubscribe()

	assert.Equal(
		t,
		1,
		bus.HandlerCount(),
		"bus reports handlers, want the 1 that was never unsubscribed",
	)

	bus.Publish(services.Event{Type: services.EventLog})

	assert.Equal(t, 1, sub.len(), "the remaining subscriber got events, want 1")
}

func TestEventBusRefusesANilHandler(t *testing.T) {
	bus := newTestBus()

	unsubscribe := bus.Subscribe(nil)
	assert.Equal(t, 0, bus.HandlerCount(), "bus reports handlers after subscribing nil, want 0")
	// The returned function must still be safe to call, or the caller's
	// `defer unsubscribe()` pattern panics.
	unsubscribe()

	// Publishing to a bus with no handlers is the common start-up case and must
	// not panic or waste work.
	bus.Publish(services.Event{Type: services.EventLog, Data: "nobody listening"})
}

func TestEventBusSurvivesANilReceiver(t *testing.T) {
	// A nil bus reaches a caller when a constructor's error was thrown away. Every
	// method has to tolerate it.
	var bus *EventBus

	unsubscribe := bus.Subscribe(func(Event) {})
	require.NotNil(t, unsubscribe, "Subscribe on a nil bus returned a nil unsubscribe function")
	unsubscribe()

	assert.Equal(t, 0, bus.HandlerCount(), "a nil bus reports handlers, want 0")

	bus.Publish(services.Event{Type: services.EventLog})

	assert.Nil(t, bus.WithClock(nil), "WithClock on a nil bus returned a bus")
}

func TestEventBusContainsAPanickingHandler(t *testing.T) {
	// One subscriber's bug must not take down the publishing operation or stop the other
	// subscribers.
	bus := newTestBus()
	before := &collector{}
	after := &collector{}

	bus.Subscribe(before.handle)
	bus.Subscribe(func(Event) { panic("subscriber bug") })
	bus.Subscribe(after.handle)

	bus.Publish(services.Event{Type: services.EventModEnabled, ModID: "better_ui"})

	for name, sub := range map[string]*collector{"before": before, "after": after} {
		assert.Equal(t, 1, sub.len(),
			"%s subscriber got events around a panicking handler, want 1",
			name,
		)
	}

	assert.Equal(
		t,
		3,
		bus.HandlerCount(),
		"bus reports handlers after a panic, want the 3 that were registered",
	)
}

func TestEventBusAllowsAHandlerToUnsubscribeItself(t *testing.T) {
	// Delivery happens under no lock, so a handler that tears itself down — the
	// shape of "stop after the first completed install" — must not deadlock.
	bus := newTestBus()

	var (
		seen        int
		mu          sync.Mutex
		unsubscribe func()
	)

	unsubscribe = bus.Subscribe(func(Event) {
		mu.Lock()
		seen++
		mu.Unlock()
		unsubscribe()
	})
	bus.Publish(services.Event{Type: services.EventLog, Data: "one"})
	bus.Publish(services.Event{Type: services.EventLog, Data: "two"})

	mu.Lock()
	defer mu.Unlock()

	assert.Equal(t, 1, seen, "self-unsubscribing handler ran times, want 1")

	assert.Equal(
		t,
		0,
		bus.HandlerCount(),
		"bus reports handlers after the handler unsubscribed itself, want 0",
	)
}

func TestEventBusAllowsSubscribingFromAHandler(t *testing.T) {
	bus := newTestBus()
	added := &collector{}

	bus.Subscribe(func(Event) {
		bus.Subscribe(added.handle)
	})
	bus.Publish(services.Event{Type: services.EventLog, Data: "first"})

	assert.Equal(
		t,
		2,
		bus.HandlerCount(),
		"bus reports handlers after one subscribed from a handler, want 2",
	)
	// The new handler must not receive the event that was already in flight, or a
	// subscriber added late would see history it was never there for.
	assert.Equal(t, 0, added.len(),
		"a handler added during delivery got events from that delivery, want 0",
	)

	bus.Publish(services.Event{Type: services.EventLog, Data: "second"})

	assert.Equal(t, 1, added.len(),
		"a handler added during delivery got events from the next one, want 1",
	)
}

func TestEventBusConvertsEveryServiceEventType(t *testing.T) {
	// The string values are part of this package's contract, so a rename on the
	// services side has to fail here, not silently break a subscriber.
	bus := newTestBus()
	sub := &collector{}
	bus.Subscribe(sub.handle)

	published := services.EventTypes()
	require.NotEmpty(t, published, "the services layer reports no event types")

	for _, eventType := range published {
		bus.Publish(services.Event{Type: eventType})
	}

	got := sub.snapshot()
	require.Len(t, got, len(published), "subscriber got events")

	documented := make(map[EventType]bool, len(got))
	for _, e := range got {
		documented[e.Type] = true
	}

	for _, eventType := range EventTypes() {
		assert.True(t, documented[eventType],
			"this package documents event type %q, which the services layer never publishes",
			eventType,
		)
	}
}

func TestEventTypesIsACopy(t *testing.T) {
	first := EventTypes()
	require.NotEmpty(t, first, "EventTypes returned nothing")

	saved := first[0]
	first[0] = EventType("tampered")

	assert.Equal(
		t,
		saved,
		EventTypes()[0],
		"EventTypes handed out its own slice, so a caller can corrupt it for everyone else",
	)

	assert.NotEmpty(t, strings.TrimSpace(string(saved)), "EventTypes returned a blank event type")
}

func TestEventBusIsSafeUnderConcurrentPublishAndSubscribe(t *testing.T) {
	// The bus is shared by every goroutine running a mod operation, and the GUI
	// subscribes while installs are in flight. Run it under the race detector.
	bus := newTestBus()

	var wg sync.WaitGroup

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for j := 0; j < 50; j++ {
				bus.Publish(services.Event{Type: services.EventModInstallProgress, ModID: "m"})
			}
		}()
	}

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for j := 0; j < 25; j++ {
				sub := &collector{}
				unsubscribe := bus.Subscribe(sub.handle)
				bus.Publish(services.Event{Type: services.EventLog})
				unsubscribe()
			}
		}()
	}

	wg.Wait()

	assert.Equal(
		t,
		0,
		bus.HandlerCount(),
		"bus reports handlers after every subscriber left, want 0",
	)
}

func TestClientSubscribesHandlersBeforeAnythingIsWired(t *testing.T) {
	// The point of subscribing during construction is that a caller's very first
	// event arrives. This checks the wiring end of that.
	bus := newTestBus()
	sub := &collector{}

	client, err := NewClientFromServices(ClientDeps{Events: bus}, WithEventHandler(sub.handle))
	require.NoError(t, err, "NewClientFromServices")

	defer client.Close()

	assert.Equal(t, 1, bus.HandlerCount(), "bus reports handlers after construction, want 1")

	assert.Equal(
		t,
		bus,
		client.Events(),
		"client returned a different bus than the one it was given",
	)
}

func TestClientCreatesItsOwnBusWhenNoneIsGiven(t *testing.T) {
	client, err := NewClientFromServices(ClientDeps{})
	require.NoError(t, err, "NewClientFromServices")

	defer client.Close()

	assert.NotNil(t, client.Events(), "client has no event bus after being built without one")

	assert.Equal(
		t,
		client.Events(),
		client.Services().Events,
		"the client's services hold a different bus than the client exposes",
	)
}

// The services layer hands out its own clock, and the bus has to honour it or an
// event's timestamp disagrees with everything else the client reports.
func TestEventBusUsesTheServicesClockWhenOneIsSupplied(t *testing.T) {
	pinned := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	bus := newTestBus()
	bus.WithClock(func() time.Time { return pinned })

	sub := &collector{}
	bus.Subscribe(sub.handle)
	bus.Publish(services.Event{Type: services.EventLog, Data: domain.Mod{ID: "m"}})

	events := sub.snapshot()
	require.Len(t, events, 1, "subscriber got events, want 1")

	assert.True(
		t,
		events[0].Timestamp.Equal(pinned),
		"event timestamp is %v, want the clock's %v",
		events[0].Timestamp,
		pinned,
	)

	_, ok := events[0].Data.(domain.Mod)
	assert.True(t, ok, "event data is %T, want the domain.Mod that was published", events[0].Data)
}

// A context that is already done must stop an operation before it touches the
// services. The registry's own Get has no context, so this is checked in the
// adapter.
func TestRegistryProviderHonoursACancelledContext(t *testing.T) {
	registry := services.NewSourceRegistry()
	require.NoError(t, registry.Register(&stubSource{id: "stub"}), "Register")

	provider := registryProvider{registry: registry}

	_, err := provider.Get(context.Background(), "stub")
	require.NoError(t, err, "Get with a live context")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = provider.Get(ctx, "stub")
	assert.True(
		t,
		IsAppError(err, ErrCodeOperationCanceled),
		"Get with a cancelled context returned %v, want an operation-canceled error",
		err,
	)

	// A nil context is a caller's slip, not a reason for the wiring to fail.
	//nolint:staticcheck // the nil context is exactly what is under test.
	_, err = provider.Get(nil, "stub")
	assert.NoError(t, err, "Get with a nil context")
}
