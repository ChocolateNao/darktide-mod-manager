import type { z } from "zod";

import {
  logFileListWireSchema,
  logLineWireSchema,
  logSettingsWireSchema,
  logTailWireSchema,
} from "@/lib/schemas/log";
import type { LogFile, LogLine, LogSettings } from "@/types/domain";

type SettingsWire = z.infer<typeof logSettingsWireSchema>;
type LineWire = z.infer<typeof logLineWireSchema>;
type FileWire = z.infer<typeof logFileListWireSchema.element>;

export const mapLogSettings = (raw: unknown): LogSettings => {
  const wire: SettingsWire = logSettingsWireSchema.parse(raw);

  return {
    level: wire.level,
    directory: wire.directory,
    file: wire.file,
    maxSizeMb: wire.max_size_mb,
    maxBackups: wire.max_backups,
    maxAgeDays: wire.max_age_days,
    compress: wire.compress,
    consoleOnly: wire.console_only,
    hasRedactFields: wire.has_redact_fields,
  };
};

export const mapLogLine = (raw: unknown): LogLine => {
  const wire: LineWire = logLineWireSchema.parse(raw);

  return {
    timestamp: wire.timestamp,
    level: wire.level,
    caller: wire.caller,
    message: wire.message,
    modId: wire.mod_id,
    fields: wire.fields ?? undefined,
  };
};

export const mapLogLines = (raw: unknown): LogLine[] =>
  logTailWireSchema.parse(raw).map((wire: LineWire) => mapLogLine(wire));

export const mapLogFiles = (raw: unknown): LogFile[] =>
  logFileListWireSchema.parse(raw).map((wire: FileWire) => ({
    name: wire.name,
    path: wire.path,
    size: wire.size,
    modTime: wire.mod_time ?? null,
    rotated: wire.rotated,
    compressed: wire.compressed,
  }));

export type { FileWire, LineWire, SettingsWire };
