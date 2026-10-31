import { z } from "zod";

import { logLineWireSchema } from "@/lib/schemas/log";

/**
 * Compile-time copy of `lifecycle.EventTypes()` that the router switches on; a
 * mismatch shows up as an unhandled event.
 */
export const EVENT_TYPES = [
  "mod.install.started",
  "mod.install.progress",
  "mod.install.completed",
  "mod.install.failed",
  "mod.uninstall.started",
  "mod.uninstall.completed",
  "mod.enabled",
  "mod.disabled",
  "mod.updated",
  "mod.update.started",
  "mod.update.completed",
  "hook.started",
  "hook.finished",
  "hook.failed",
  "hook.output",
  "cache.downloaded",
  "cache.pruned",
  "cache.cleared",
  "log",
  "share.created",
  "share.offered",
  "share.accepted",
  "share.progress",
  "share.completed",
  "share.rejected",
  "share.expired",
  "share.failed",
] as const;

export type EventType = (typeof EVENT_TYPES)[number];

/** The one channel every backend event arrives on. */
export const WEBVIEW_EVENT_NAME = "mod-manager:event";

const installEventDataSchema = z.object({
  version: z.string().default(""),
  path: z.string().default(""),
  files: z.number().int().optional(),
  enabled: z.boolean().optional(),
});

const hookEventDataSchema = z.object({
  phase: z.string().default(""),
  type: z.string().default(""),
  error: z.string().optional(),
  index: z.number().int().optional(),
  // `time.Duration` has no JSON marshaller of its own, so this is nanoseconds.
  timeout: z.number().optional(),
  lines: z.array(z.string()).default([]),
  dropped: z.number().int().default(0),
});

export const rawEventWireSchema = z.object({
  type: z.string(),
  timestamp: z.string().default(""),
  data: z.unknown().optional(),
  mod_id: z.string().optional(),
  mod_name: z.string().optional(),
  source_id: z.string().optional(),
});

export type RawEvent = {
  type: EventType;
  timestamp: Date;
  data: unknown;
  modId: string;
  modName: string;
  sourceId: string;
};

/**
 * `Data` is documented as opaque outside the manager, so the router parses it
 * per type and treats a parse failure as a payload that says nothing useful.
 * An unrecognised shape is still an event; refusing it would lose the mod id
 * sitting beside the data.
 */
export function parseEvent(raw: unknown): RawEvent | null {
  const wire = rawEventWireSchema.safeParse(raw);
  if (!wire.success) return null;

  const { data, mod_id, mod_name, source_id, timestamp, type } = wire.data;

  if (!(EVENT_TYPES as readonly string[]).includes(type)) return null;

  return {
    type: type as EventType,
    timestamp: timestamp ? new Date(timestamp) : new Date(),
    data,
    modId: mod_id ?? "",
    modName: mod_name ?? "",
    sourceId: source_id ?? "",
  };
}

export function readInstallEventData(data: unknown) {
  return installEventDataSchema.safeParse(data);
}

export function readHookEventData(data: unknown) {
  return hookEventDataSchema.safeParse(data);
}

/**
 * A `log` event carries a whole `logging.LogLine`: `internal/logging`'s event
 * writer parses each line and publishes the struct, so the live stream and
 * `TailLogs` produce rows of the same shape.
 */
export function readLogLine(data: unknown) {
  return logLineWireSchema.safeParse(data);
}

/** Cache events carry a bare value; `cache.cleared` carries nothing at all. */
export function readCacheValue(data: unknown): string | number | null {
  if (typeof data === "string" || typeof data === "number") return data;
  return null;
}
