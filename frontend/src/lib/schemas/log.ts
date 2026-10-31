import { z } from "zod";

import { int, optionalText, wireDate } from "./shared";

export const logLevelSchema = z.enum([
  "trace",
  "debug",
  "info",
  "warn",
  "error",
]);

export const logLevels = logLevelSchema.options;

export const logLineWireSchema = z.object({
  timestamp: z.string(),
  level: z.string(),
  caller: optionalText,
  message: z.string(),
  mod_id: optionalText,
  // `Raw` is `json:"-"`, and the sink has already redacted what it kept.
  fields: z.record(z.string(), z.unknown()).nullish(),
});

export const logTailWireSchema = z.array(logLineWireSchema);

export const logFileWireSchema = z.object({
  name: z.string(),
  path: z.string(),
  size: int.default(0),
  mod_time: wireDate,
  rotated: z.boolean().default(false),
  compressed: z.boolean().default(false),
});

export const logFileListWireSchema = z.array(logFileWireSchema);

export const logSettingsWireSchema = z.object({
  level: z.string(),
  directory: z.string().default(""),
  file: z.string().default(""),
  max_size_mb: int.default(10),
  max_backups: int.default(5),
  max_age_days: int.default(30),
  compress: z.boolean().default(true),
  console_only: z.boolean().default(false),
  has_redact_fields: z.boolean().default(false),
});
