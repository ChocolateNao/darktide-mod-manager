/**
 * The event pipeline: one `EventsOn` subscription for the whole application.
 *
 * The backend publishes every event under a single name with a `type`
 * discriminator, so there is one listener and one unsubscribe. Events become
 * *intents* expressed through `ManagerEventsSink`, which keeps the router
 * testable without a window.
 */

import { EventsOn } from "wailsjs/runtime/runtime";

import { hasWailsRuntime } from "@/bridge/dialogs";
import { t } from "@/lib/i18n";
import { mapLogLine } from "@/lib/mappers/log";
import { mapSessionOrNull } from "@/lib/mappers/share";
import type { LogLine, P2PShareSession } from "@/types/domain";
import {
  type EventType,
  parseEvent,
  type RawEvent,
  readCacheValue,
  readHookEventData,
  readInstallEventData,
  readLogLine,
  WEBVIEW_EVENT_NAME,
} from "@/types/events";

export type ActivityKind =
  "install" | "uninstall" | "update" | "hook" | "share";

export interface ActivityEntryInput {
  kind: ActivityKind;
  label: string;
  modId?: string;
}

/**
 * What the event pipeline is allowed to ask the rest of the app to do. Every
 * handler here must stay short: the Go bus delivers synchronously on the
 * goroutine that published the event, so a handler that did real work would slow
 * down the install it is reporting on.
 */
export interface ActivitySink {
  /** Starts a row in the status bar and returns its id. */
  begin(entry: ActivityEntryInput): string;
  /** `null` means indeterminate — the row shows a spinner. */
  setProgress(id: string, fraction: number | null): void;
  setDetail(id: string, detail: string): void;
  finish(id: string): void;
  fail(id: string, reason: string): void;
  /** The row an operation is already tracked under, so `completed` updates the row `started` created. */
  find(criteria: { kind: ActivityKind; modId?: string }): string | undefined;
}

export type CacheEventKind = "downloaded" | "pruned" | "cleared";
export type NotifyCategory =
  "install" | "updates" | "hooks" | "cache" | "sharing" | "errors";

/** One console entry a hook run produced, before it reaches the store. */
export interface HookConsoleEvent {
  kind: "start" | "output" | "done" | "failed" | "dropped";
  modId?: string;
  phase: string;
  type: string;
  lines?: string[];
  dropped?: number;
  error?: string;
}

export interface NotifyRequest {
  title: string;
  body?: string;
  category: NotifyCategory;
  /** Cache chatter is silent unless the user asked for it. */
  silent?: boolean;
}

export interface ManagerEventsSink {
  activity: ActivitySink;
  /** A completed mutation invalidates the queries it can have changed. */
  changed(
    scope: "mods" | "updates" | "conflicts" | "cache" | "sessions" | "config"
  ): void;
  /** Optimistic enable/disable arrives via the event, no refetch. */
  modEnabled?(modId: string, enabled: boolean): void;
  /** One line for the log tail, already mapped from the wire shape. */
  log(line: LogLine): void;
  hookConsole(event: HookConsoleEvent): void;
  notify(request: NotifyRequest): void;
  cache(kind: CacheEventKind, value: number | string | null): void;
  share(session: P2PShareSession): void;
  /** An event this build does not know about. Counted, never guessed at. */
  unhandled(type: string): void;
}

/**
 * The one place event names appear as strings. The record is exhaustive over
 * `EventType`, so an event added to `types/events.ts` and not routed here is
 * a type error, never a silently dropped line in the status bar.
 */
type EventFamily =
  | "install"
  | "uninstall"
  | "enabled"
  | "disabled"
  | "updated"
  | "update-started"
  | "update-completed"
  | "hook-started"
  | "hook-finished"
  | "hook-failed"
  | "hook-output"
  | "cache"
  | "log"
  | "share";

const EVENT_FAMILY: Readonly<Record<EventType, EventFamily>> = {
  "mod.install.started": "install",
  "mod.install.progress": "install",
  "mod.install.completed": "install",
  "mod.install.failed": "install",
  "mod.uninstall.started": "uninstall",
  "mod.uninstall.completed": "uninstall",
  "mod.enabled": "enabled",
  "mod.disabled": "disabled",
  "mod.updated": "updated",
  "mod.update.started": "update-started",
  "mod.update.completed": "update-completed",
  "hook.started": "hook-started",
  "hook.finished": "hook-finished",
  "hook.failed": "hook-failed",
  "hook.output": "hook-output",
  "cache.downloaded": "cache",
  "cache.pruned": "cache",
  "cache.cleared": "cache",
  log: "log",
  "share.created": "share",
  "share.offered": "share",
  "share.accepted": "share",
  "share.progress": "share",
  "share.completed": "share",
  "share.rejected": "share",
  "share.expired": "share",
  "share.failed": "share",
};

const CACHE_KIND: Readonly<Partial<Record<EventType, CacheEventKind>>> = {
  "cache.downloaded": "downloaded",
  "cache.pruned": "pruned",
  "cache.cleared": "cleared",
};

/** Reads the discriminator off a payload that failed validation, for reporting. */
function unknownEventName(raw: unknown): string {
  if (typeof raw !== "object" || raw === null) return "malformed";
  const type = (raw as { type?: unknown }).type;
  return typeof type === "string" ? type : "malformed";
}

function labelFor(event: RawEvent): string {
  return event.modName || event.modId || t("notify.unknownMod");
}

/**
 * The version a notification quotes when the backend published one. Read from
 * the same envelope every install event carries — a separate tracker could
 * claim a version the event did not carry.
 */
function updateVersion(event: RawEvent): string | undefined {
  return readInstallEventData(event.data).data?.version;
}

/**
 * Handles one raw event. Returns whether the event was understood — that
 * boolean is what the tests assert on.
 */
export function routeEvent(raw: unknown, sink: ManagerEventsSink): boolean {
  const event = parseEvent(raw);
  if (!event) {
    sink.unhandled(unknownEventName(raw));
    return false;
  }

  switch (EVENT_FAMILY[event.type]) {
    case "install":
      routeInstall(event, sink);
      return true;
    case "uninstall":
      sink.changed("mods");
      sink.changed("conflicts");
      if (event.type === "mod.uninstall.started") {
        sink.activity.begin({
          kind: "uninstall",
          label: labelFor(event),
          modId: event.modId,
        });
      } else {
        const id = sink.activity.find({
          kind: "uninstall",
          modId: event.modId,
        });
        if (id) sink.activity.finish(id);
        sink.notify({
          title: t("notify.install.uninstalled", { name: labelFor(event) }),
          category: "install",
        });
      }
      return true;
    case "enabled":
    case "disabled":
      // The row is patched in place: a refetch would fight the optimistic
      // update the click already made.
      if (event.modId)
        sink.modEnabled?.(event.modId, EVENT_FAMILY[event.type] === "enabled");
      sink.changed("conflicts");
      return true;
    case "update-started":
      sink.activity.begin({
        kind: "update",
        label: labelFor(event),
        modId: event.modId,
      });
      return true;
    case "update-completed":
      sink.changed("mods");
      sink.changed("updates");
      sink.changed("conflicts");
      sink.notify({
        title: t("notify.updates.completed", {
          name: labelFor(event),
          version: updateVersion(event) ?? "",
        }),
        category: "updates",
      });
      return true;
    case "updated":
      sink.changed("mods");
      sink.changed("updates");
      return true;
    case "hook-started": {
      const data = readHookEventData(event.data).data;
      const id = sink.activity.begin({
        kind: "hook",
        label: labelFor(event),
        modId: event.modId,
      });
      sink.activity.setDetail(
        id,
        `${data?.phase ?? "?"} · ${data?.type ?? "hook"}`
      );
      sink.hookConsole({
        kind: "start",
        modId: event.modId,
        phase: data?.phase ?? "",
        type: data?.type ?? "",
      });
      return true;
    }
    case "hook-finished": {
      const data = readHookEventData(event.data).data;
      const id = sink.activity.find({ kind: "hook", modId: event.modId });
      if (id) sink.activity.finish(id);
      sink.hookConsole({
        kind: "done",
        modId: event.modId,
        phase: data?.phase ?? "",
        type: data?.type ?? "",
      });
      return true;
    }
    case "hook-failed": {
      const data = readHookEventData(event.data).data;
      const reason = data?.error ?? t("notify.hookFailedWithoutReason");
      const id =
        sink.activity.find({ kind: "hook", modId: event.modId }) ??
        sink.activity.begin({
          kind: "hook",
          label: labelFor(event),
          modId: event.modId,
        });
      sink.activity.fail(id, reason);
      sink.hookConsole({
        kind: "failed",
        modId: event.modId,
        phase: data?.phase ?? "",
        type: data?.type ?? "",
        error: reason,
      });
      sink.notify({
        title: t("notify.hooks.failed", { phase: data?.phase ?? "?" }),
        body: reason,
        category: "hooks",
      });
      return true;
    }
    case "hook-output": {
      const data = readHookEventData(event.data).data;
      if (!data) {
        sink.unhandled(event.type);
        return false;
      }
      sink.hookConsole({
        kind: "output",
        modId: event.modId,
        phase: data.phase,
        type: data.type,
        lines: data.lines,
        dropped: data.dropped,
      });
      return true;
    }
    case "cache": {
      const kind = CACHE_KIND[event.type] ?? "cleared";
      sink.cache(kind, readCacheValue(event.data));
      sink.changed("cache");
      // A cache download is progress; announcing every file would make the
      // notification channel useless for the messages that matter.
      sink.notify({
        title: t(`notify.cache.${kind}`),
        category: "cache",
        silent: kind === "downloaded",
      });
      return true;
    }
    case "log": {
      const line = readLogLine(event.data);
      // An unparseable line is dropped, never shown half-formed: the file on
      // disk still has it, and `TailLogs` picks it up on the next paint.
      if (line.success) sink.log(mapLogLine(line.data));
      return line.success;
    }
    case "share":
      return routeShare(event, sink);
  }
}

function routeInstall(event: RawEvent, sink: ManagerEventsSink): void {
  const data = readInstallEventData(event.data).data;
  const label = labelFor(event);

  switch (event.type) {
    case "mod.install.started": {
      const id = sink.activity.begin({
        kind: "install",
        label,
        modId: event.modId,
      });
      if (data?.version) sink.activity.setDetail(id, `v${data.version}`);
      return;
    }
    case "mod.install.progress": {
      // The bursty event: a store write only, and a spinner because the payload
      // carries a file count but no total.
      const id = sink.activity.find({ kind: "install", modId: event.modId });
      if (id && typeof data?.files === "number")
        sink.activity.setDetail(id, t("notify.files", { count: data.files }));
      return;
    }
    case "mod.install.completed": {
      sink.activity.finish(
        sink.activity.find({ kind: "install", modId: event.modId }) ??
          sink.activity.begin({ kind: "install", label, modId: event.modId })
      );
      sink.changed("mods");
      sink.changed("conflicts");
      sink.notify({
        title: t("notify.install.completed", { name: label }),
        body: data?.version ?? undefined,
        category: "install",
      });
      return;
    }
    case "mod.install.failed": {
      const reason = data?.path || t("notify.installIncomplete");
      sink.activity.fail(
        sink.activity.find({ kind: "install", modId: event.modId }) ??
          sink.activity.begin({ kind: "install", label, modId: event.modId }),
        reason
      );
      sink.changed("mods");
      sink.notify({
        title: t("notify.install.failed", { name: label }),
        body: reason,
        category: "install",
      });
      return;
    }
  }
}

function routeShare(event: RawEvent, sink: ManagerEventsSink): boolean {
  // The raw payload goes straight into the mapper: a reader would hand back an
  // already-parsed shape, and mapping that would re-read timestamps as strings.
  const session = mapSessionOrNull(event.data);
  if (!session) {
    // A share event with an unrecognisable payload is a backend change this
    // build does not understand; dropping it is safer than inventing a row.
    sink.unhandled(event.type);
    return false;
  }

  sink.share(session);

  switch (event.type) {
    case "share.completed":
      sink.changed("mods");
      sink.changed("conflicts");
      sink.changed("sessions");
      sink.notify({
        title: t("notify.share.completed", {
          count: session.modIds.length,
          sender: session.preview?.sender ?? t("notify.peer"),
        }),
        category: "sharing",
      });
      return true;
    case "share.rejected":
      sink.notify({
        title: t("notify.share.rejected", {
          sender: session.preview?.sender ?? t("notify.peer"),
        }),
        body: session.failReason ?? t("notify.declinedWithoutReason"),
        category: "sharing",
      });
      return true;
    case "share.failed":
      sink.notify({
        title: t("notify.share.failed", {
          reason: session.failReason ?? t("notify.unknownReason"),
        }),
        category: "sharing",
      });
      return true;
    default:
      // Progress-only states: the store already has the session, and the row
      // re-renders from it.
      return true;
  }
}

/**
 * Subscribes to the single backend event channel. The returned function is the
 * only unsubscribe the application needs.
 */
export function subscribeToManagerEvents(sink: ManagerEventsSink): () => void {
  // Without a Wails runtime there is nothing to subscribe to, and the caller's
  // unsubscribe has to stay callable either way.
  if (!hasWailsRuntime()) return () => undefined;
  return EventsOn(WEBVIEW_EVENT_NAME, (raw: unknown) => {
    routeEvent(raw, sink);
  });
}
