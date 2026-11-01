import type { z } from "zod";

import { cacheInfoWireSchema, configWireSchema } from "@/lib/schemas/config";
import type { AppConfig, CacheInfo } from "@/types/domain";

type ConfigWire = z.infer<typeof configWireSchema>;
type PermissionsWire = ConfigWire["hooks"]["permissions"]["default"];

const mapPermissions = (wire: PermissionsWire) => ({
  fsRead: wire.fs_read,
  fsWrite: wire.fs_write,
  configRead: wire.config_read,
  configWrite: wire.config_write,
  storageRead: wire.storage_read,
  envRead: wire.env_read,
  timeoutSeconds: wire.timeout_seconds ?? undefined,
});

export const mapConfig = (raw: unknown): AppConfig => {
  const wire = configWireSchema.parse(raw);

  return {
    hooks: {
      permissions: {
        default: mapPermissions(wire.hooks.permissions.default),
        games: Object.fromEntries(
          Object.entries(wire.hooks.permissions.games ?? {}).map(
            ([id, value]) => [id, mapPermissions(value)]
          )
        ),
        mods: Object.fromEntries(
          Object.entries(wire.hooks.permissions.mods ?? {}).map(
            ([id, value]) => [id, mapPermissions(value)]
          )
        ),
      },
      // Kept as the Go duration string; the settings form parses it.
      defaultTimeout: wire.hooks.default_timeout,
      maxTimeout: wire.hooks.max_timeout,
    },
    sources: Object.fromEntries(
      Object.entries(wire.sources).map(([id, source]) => [
        id,
        {
          options: source.options ?? {},
          apiKey: source.api_key,
          paths: source.paths ?? [],
          enabled: source.enabled,
        },
      ])
    ),
    games: Object.fromEntries(
      Object.entries(wire.games ?? {}).map(([id, game]) => [
        id,
        {
          path: game.path,
          source: game.source,
          modsFolderOverride: game.mods_folder_override,
          installMethod: game.install_method,
          icon: game.icon,
          autoDetect: game.auto_detect,
        },
      ])
    ),
    activeGame: wire.active_game,
    ui: {
      theme: (wire.ui.theme as AppConfig["ui"]["theme"]) ?? "system",
      language: wire.ui.language,
      compactMode: wire.ui.compact_mode,
    },
    advanced: {
      enableExperimental: wire.advanced.enable_experimental,
    },
    storage: {
      path: wire.storage.path,
      backupRetentionDays: wire.storage.backup_retention_days,
    },
    autoUpdate: {
      // Deliberately `undefined` when Go sent `null`: the manager cannot tell
      // "absent" from "false", so the App tab has to.
      notifyOnUpdate: wire.auto_update.notify_on_update ?? undefined,
      lastCheck: wire.auto_update.last_check,
      enabled: wire.auto_update.enabled,
      checkIntervalHours: wire.auto_update.check_interval_hours,
      downloadAutomatically: wire.auto_update.download_automatically,
    },
    cache: {
      path: wire.cache.path,
      maxSizeGb: wire.cache.max_size_gb,
      maxAgeDays: wire.cache.max_age_days,
      verifyHashes: wire.cache.verify_hashes,
    },
    cloudSharing: {
      providers: wire.cloud_sharing.providers ?? {},
      defaultProvider: wire.cloud_sharing.default_provider,
      fallbackProviders: wire.cloud_sharing.fallback_providers ?? [],
      shareExpiryDays: wire.cloud_sharing.share_expiry_days,
      autoInstall: wire.cloud_sharing.auto_install,
    },
    sharing: {
      p2p: {
        stunServers: wire.sharing.p2p.stun_servers ?? [],
        turnRelays: wire.sharing.p2p.turn_relays ?? [],
        expiryDays: wire.sharing.p2p.expiry_days,
        chunkSizeKb: wire.sharing.p2p.chunk_size_kb ?? undefined,
      },
    },
    logging: {
      level: wire.logging.level,
      directory: wire.logging.directory,
      redact: wire.logging.redact,
      maxSizeMb: wire.logging.max_size_mb,
      maxBackups: wire.logging.max_backups,
      maxAgeDays: wire.logging.max_age_days,
      compress: wire.logging.compress,
    },
    security: {
      allowActionCommands: wire.security.allow_action_commands,
    },
  };
};

export const mapCacheInfo = (raw: unknown): CacheInfo => {
  const wire = cacheInfoWireSchema.parse(raw);

  return {
    path: wire.path,
    fileCount: wire.file_count,
    totalSize: wire.total_size,
    oldest: wire.oldest ? new Date(wire.oldest) : null,
    newest: wire.newest ? new Date(wire.newest) : null,
  };
};

export type { ConfigWire };
