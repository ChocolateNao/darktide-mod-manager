import { z } from "zod";

import { optionalText, textList } from "./shared";

const scriptPermissionsWireSchema = z.object({
  fs_read: z.boolean().default(false),
  fs_write: z.boolean().default(false),
  config_read: z.boolean().default(false),
  config_write: z.boolean().default(false),
  storage_read: z.boolean().default(false),
  env_read: z.boolean().default(false),
  timeout_seconds: z.number().int().nullish(),
});

const hooksPermissionsWireSchema = z.object({
  default: scriptPermissionsWireSchema,
  games: z.record(z.string(), scriptPermissionsWireSchema).nullish(),
  mods: z.record(z.string(), scriptPermissionsWireSchema).nullish(),
});

const hooksWireSchema = z.object({
  permissions: hooksPermissionsWireSchema,
  // `domain.Duration` marshals as a Go duration string ("10s").
  default_timeout: z.string(),
  max_timeout: z.string(),
});

const uiWireSchema = z.object({
  theme: z.string().default("system"),
  language: z.string().default("en"),
  compact_mode: z.boolean().default(false),
});

const advancedWireSchema = z.object({
  enable_experimental: z.boolean().default(false),
});

const storageWireSchema = z.object({
  path: z.string().default(""),
  backup_retention_days: z.number().int().default(7),
});

const cacheWireSchema = z.object({
  path: z.string().default(""),
  max_size_gb: z.number().int().default(5),
  max_age_days: z.number().int().default(30),
  verify_hashes: z.boolean().default(true),
});

const securityWireSchema = z
  .object({
    allow_action_commands: z.boolean().default(true),
  })
  .default({ allow_action_commands: true });

const loggingWireSchema = z.object({
  level: z.string().default("info"),
  directory: optionalText,
  redact: textList,
  max_size_mb: z.number().int().default(10),
  max_backups: z.number().int().default(5),
  max_age_days: z.number().int().default(30),
  compress: z.boolean().default(true),
});

const autoUpdateWireSchema = z.object({
  // Optional on purpose: see `AutoUpdateConfig.notifyOnUpdate` in domain.ts.
  notify_on_update: z.boolean().nullish(),
  last_check: optionalText,
  enabled: z.boolean().default(false),
  check_interval_hours: z.number().int().default(24),
  download_automatically: z.boolean().default(false),
});

const sourceWireSchema = z.object({
  options: z.record(z.string(), z.string()).nullish(),
  api_key: optionalText,
  paths: textList,
  enabled: z.boolean().default(true),
});

const gameSettingsWireSchema = z.object({
  path: optionalText,
  source: optionalText,
  mods_folder_override: optionalText,
  install_method: optionalText,
  icon: optionalText,
  auto_detect: z.boolean().default(true),
});

const p2pSharingWireSchema = z.object({
  stun_servers: textList,
  turn_relays: textList,
  expiry_days: z.number().int().default(7),
  chunk_size_kb: z.number().int().nullish(),
});

const sharingWireSchema = z.object({ p2p: p2pSharingWireSchema });

const cloudSharingWireSchema = z.object({
  providers: z.record(z.string(), z.record(z.string(), z.string())).nullish(),
  default_provider: z.string().default(""),
  fallback_providers: textList,
  share_expiry_days: z.number().int().default(30),
  auto_install: z.boolean().default(false),
});

export const configWireSchema = z.object({
  hooks: hooksWireSchema,
  sources: z.record(z.string(), sourceWireSchema).default({}),
  games: z.record(z.string(), gameSettingsWireSchema).nullish(),
  active_game: z.string().default(""),
  ui: uiWireSchema,
  advanced: advancedWireSchema,
  storage: storageWireSchema,
  auto_update: autoUpdateWireSchema,
  cache: cacheWireSchema,
  cloud_sharing: cloudSharingWireSchema,
  sharing: sharingWireSchema,
  logging: loggingWireSchema,
  security: securityWireSchema,
});

export const configValueWireSchema = z.string();

export const cacheInfoWireSchema = z.object({
  path: z.string(),
  file_count: z.number().int().default(0),
  total_size: z.number().default(0),
  oldest: z.string().nullish(),
  newest: z.string().nullish(),
});

export const defaultPathsWireSchema = z.object({
  storage: z.string(),
  cache: z.string(),
});
