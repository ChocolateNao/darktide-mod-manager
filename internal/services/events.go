package services

import (
	"context"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// EventType names a thing that happened. The strings are part of the public
// contract: pkg/api re-exports them and the frontend switches on them, so they
// are declared once here rather than spelled out at each publish site.
type EventType string

const (
	EventModInstallStarted     EventType = "mod.install.started"
	EventModInstallProgress    EventType = "mod.install.progress"
	EventModInstallCompleted   EventType = "mod.install.completed"
	EventModInstallFailed      EventType = "mod.install.failed"
	EventModUninstallStarted   EventType = "mod.uninstall.started"
	EventModUninstallCompleted EventType = "mod.uninstall.completed"
	EventModEnabled            EventType = "mod.enabled"
	EventModDisabled           EventType = "mod.disabled"
	EventModUpdated            EventType = "mod.updated"
	EventModUpdateStarted      EventType = "mod.update.started"
	EventModUpdateCompleted    EventType = "mod.update.completed"

	EventHookStarted  EventType = "hook.started"
	EventHookFinished EventType = "hook.finished"
	EventHookFailed   EventType = "hook.failed"

	EventCacheDownloaded EventType = "cache.downloaded"
	EventCachePruned     EventType = "cache.pruned"
	EventCacheCleared    EventType = "cache.cleared"

	EventLog EventType = "log"
)

// allEventTypes lists every declared type in the order they are declared. It
// backs EventTypes, which exists so pkg/api can assert that the events it
// documents are exactly the events this layer publishes — a hand-written second
// list would drift, and the drift would be invisible until a subscriber stopped
// firing.
var allEventTypes = []EventType{
	EventModInstallStarted, EventModInstallProgress, EventModInstallCompleted,
	EventModInstallFailed, EventModUninstallStarted, EventModUninstallCompleted,
	EventModEnabled, EventModDisabled, EventModUpdated, EventModUpdateStarted,
	EventModUpdateCompleted, EventHookStarted, EventHookFinished, EventHookFailed,
	EventCacheDownloaded, EventCachePruned, EventCacheCleared, EventLog,
}

// EventTypes returns every event type this layer can publish, in a fresh slice so
// a caller sorting it cannot affect anyone else.
func EventTypes() []EventType {
	out := make([]EventType, len(allEventTypes))
	copy(out, allEventTypes)

	return out
}

// Event is a single notification. ModID and SourceID are pulled out of Data's
// sibling fields rather than left inside it so a subscriber can filter on them
// without unpacking a map.
type Event struct {
	Data     any
	Type     EventType
	ModID    string
	SourceID string
}

// EventSink receives events. The API layer adapts its EventBus onto it; tests
// use a slice-collecting fake. Publishing must never block or fail the caller,
// so an implementation is expected to drop or buffer events rather than error.
type EventSink interface {
	Publish(Event)
}

// canceledError maps a context cancellation onto the typed cancellation code
// callers check for. Local to services so every service reports cancellation
// the same way.
func canceledError(err error) error {
	if err == nil {
		return appErrors.New(appErrors.ErrCodeOperationCanceled, "operation canceled")
	}

	if err == context.Canceled || err == context.DeadlineExceeded {
		return appErrors.Wrap(err, appErrors.ErrCodeOperationCanceled, "operation canceled")
	}

	return appErrors.Wrap(err, appErrors.ErrCodeOperationCanceled, "operation stopped: %v", err)
}

// wrapDownloadError labels a download failure with a code that reflects what
// the source actually reported, so the UI can tell "the file is gone" from "the
// network is down".
func wrapDownloadError(mod *domain.Mod, err error) error {
	if err == nil {
		return nil
	}

	if code := appErrors.CodeOf(err); code != appErrors.CodeUnknown {
		// The source already produced a typed error; keep its code.
		return appErrors.Wrap(err, code, "download mod %q: %v", modLabel(mod), err)
	}

	return appErrors.Wrap(
		err,
		appErrors.ErrCodeDownloadFailed,
		"download mod %q: %v",
		modLabel(mod),
		err,
	)
}

// modLabel names a mod for an error message without dereferencing a nil.
func modLabel(mod *domain.Mod) string {
	if mod == nil || mod.ID == "" {
		return "<unknown>"
	}

	return mod.ID
}
