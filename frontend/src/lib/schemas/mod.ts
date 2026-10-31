import { z } from "zod";

import { int, optionalText, textList, wireDate } from "./shared";

export const hookPhaseSchema = z.enum([
  "pre_install",
  "post_install",
  "pre_enable",
  "post_enable",
  "pre_disable",
  "post_disable",
  "pre_uninstall",
  "post_uninstall",
  "pre_update",
  "post_update",
]);

export const hookScriptWireSchema = z.object({
  language: z.string(),
  content: z.string(),
  working_dir: optionalText,
  env: textList,
  timeout: z.number().nullish(),
  name: optionalText,
  disabled: z.boolean().optional(),
});

export const hookPhases = hookPhaseSchema.options;

export const modHooksWireSchema = z.object({
  pre_install: z.array(hookScriptWireSchema).nullish(),
  post_install: z.array(hookScriptWireSchema).nullish(),
  pre_enable: z.array(hookScriptWireSchema).nullish(),
  post_enable: z.array(hookScriptWireSchema).nullish(),
  pre_disable: z.array(hookScriptWireSchema).nullish(),
  post_disable: z.array(hookScriptWireSchema).nullish(),
  pre_uninstall: z.array(hookScriptWireSchema).nullish(),
  post_uninstall: z.array(hookScriptWireSchema).nullish(),
  pre_update: z.array(hookScriptWireSchema).nullish(),
  post_update: z.array(hookScriptWireSchema).nullish(),
});

export const modWireSchema = z.object({
  id: z.string(),
  name: z.string(),
  version: z.string().default(""),
  author: z.string().default(""),
  description: z.string().default(""),
  source_id: z.string().default(""),
  source_data: z.record(z.string(), z.unknown()).nullish(),
  game_id: optionalText,
  game_version: optionalText,
  mod_folder_name: optionalText,
  install_mode: optionalText,
  hash: optionalText,
  local_path: z.string().default(""),
  source_url: optionalText,
  download_url: optionalText,
  update_url: optionalText,
  dependencies: textList,
  conflicts_with: textList,
  tags: textList,
  issues: textList,
  priority: int.default(0),
  enabled: z.boolean().default(false),
  external: z.boolean().default(false),
  installed_at: wireDate,
  updated_at: wireDate,
  hooks: modHooksWireSchema.nullish(),
});

export const modListWireSchema = z.array(modWireSchema);

export const conflictReportWireSchema = z.object({
  // Stamped by the Go gate. Without it `ResolveConflict` has nothing to take.
  id: z.string(),
  type: z.string(),
  mod_a: modWireSchema.nullish(),
  mod_b: modWireSchema.nullish(),
  details: z.string().default(""),
  severity: z.string().default("warning"),
  recommendation: z.string().default(""),
});

export const conflictListWireSchema = z.array(conflictReportWireSchema);

export const modUpdateWireSchema = z.object({
  mod_id: z.string(),
  local_version: z.string().default(""),
  remote_version: z.string().default(""),
  source_id: z.string().default(""),
  release_notes: optionalText,
  download_url: optionalText,
  changelog_url: optionalText,
  available: z.boolean().default(false),
});

export const modUpdateListWireSchema = z.array(modUpdateWireSchema);

export const linkStateSchema = z.enum([
  "missing",
  "foreign",
  "ok",
  "broken",
  "retargeted",
]);

/** `commands.ModFile` — Go's struct is tagged, so this arrives snake_case. */
export const modFileWireSchema = z.object({
  path: z.string(),
  name: z.string(),
  size: z.number(),
  is_dir: z.boolean(),
});

export const modFileListWireSchema = z.array(modFileWireSchema);

export const modLinkStatusWireSchema = z.object({
  entries: z.record(z.string(), linkStateSchema).default({}),
  mod_id: z.string(),
  damaged: int.default(0),
});

export const modLinkStatusListWireSchema = z.array(modLinkStatusWireSchema);

export const modSourceLinkWireSchema = z.object({
  m: z.string(),
  s: optionalText,
  p: optionalText,
  d: optionalText,
  u: optionalText,
  f: z
    .string()
    .nullish()
    .transform((value) =>
      value === "download" || value === "browse" || value === "update"
        ? value
        : ""
    ),
});

export const modSourceLinkMapWireSchema = z.record(
  z.string(),
  modSourceLinkWireSchema
);

export const searchQueryWireSchema = z.object({
  query: z.string(),
  game_version: optionalText,
  sort_by: optionalText,
  tags: textList,
  page: int.optional(),
  page_size: int.optional(),
});

export const modSourceInfoWireSchema = z.object({
  id: z.string(),
  name: z.string(),
  description: z.string().default(""),
  version: z.string().default(""),
  supports_search: z.boolean().default(false),
  supports_auth: z.boolean().default(false),
  requires_api_key: z.boolean().default(false),
  enabled: z.boolean().default(true),
  authenticated: z.boolean().default(false),
  has_api_key: z.boolean().default(false),
});

export const modSourceInfoListWireSchema = z.array(modSourceInfoWireSchema);

/** The only struct the gate left untagged; it carries no other fields. */
export const uninstallOptionsWireSchema = z.object({
  PurgeCache: z.boolean(),
  Trash: z.boolean().optional(),
});

export const installOptionsWireSchema = z.object({
  Enable: z.boolean().optional(),
  SourceData: z.record(z.string(), z.unknown()).optional(),
  Force: z.boolean().optional(),
  PurgeCache: z.boolean().optional(),
});
export const droppedModWireSchema = z.object({
  source_id: z.string(),
  mod_id: z.string(),
  name: z.string(),
  version: z.string().default(""),
  author: optionalText,
  path: z.string(),
});

/**
 * Wails only supports one or two return values, so the command returns a single
 * result object carrying the mod and the recognised flag.
 */
export const droppedPathResultWireSchema = z.object({
  mod: droppedModWireSchema.nullable(),
  recognised: z.boolean(),
});
