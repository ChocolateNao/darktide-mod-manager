import { describe, expect, it } from "vitest";

import { mapConfig } from "@/lib/mappers/config";

/**
 * The core design justifies having no `import.meta.env.DEV` branch by a measurement that this is
 * the largest thing the bridge returns that is parsed through the real schema.
 */
const SAMPLE_PAYLOAD = {
  hooks: {
    permissions: {
      default: {
        fs_read: true,
        fs_write: false,
        config_read: true,
        config_write: false,
        storage_read: true,
        env_read: false,
      },
      games: {
        darktide: {
          fs_read: true,
          fs_write: true,
          config_read: true,
          config_write: true,
          storage_read: true,
          env_read: true,
        },
      },
    },
    default_timeout: "10s",
    max_timeout: "30s",
  },
  sources: Object.fromEntries(
    Array.from({ length: 12 }, (_, index) => [
      `source-${index}`,
      {
        enabled: true,
        api_key: "secret",
        paths: ["/a", "/b"],
        options: { key: "value" },
      },
    ])
  ),
  games: Object.fromEntries(
    Array.from({ length: 8 }, (_, index) => [
      `game-${index}`,
      {
        path: "/games/x",
        source: "steam",
        mods_folder_override: "/mods",
        auto_detect: true,
      },
    ])
  ),
  active_game: "darktide",
  ui: { theme: "dark", language: "en", compact_mode: false },
  advanced: { enable_experimental: false },
  storage: { path: "/storage", backup_retention_days: 7 },
  cache: {
    path: "/cache",
    max_size_gb: 5,
    max_age_days: 30,
    verify_hashes: true,
  },
  cloud_sharing: {
    providers: { nexus: { url: "https://example.com" } },
    default_provider: "nexus",
    fallback_providers: ["github"],
    share_expiry_days: 30,
    auto_install: false,
  },
  sharing: {
    p2p: {
      stun_servers: ["stun:stun.l.google.com:19302"],
      turn_relays: [],
      expiry_days: 7,
      chunk_size_kb: 256,
    },
  },
  logging: {
    level: "info",
    directory: "/logs",
    redact: ["token"],
    max_size_mb: 10,
    max_backups: 5,
    max_age_days: 30,
    compress: true,
  },
  auto_update: {
    enabled: true,
    check_interval_hours: 24,
    download_automatically: false,
    notify_on_update: true,
    last_check: "2026-10-16T09:00:00Z",
  },
};

describe("bridge cost", () => {
  it("parses a full config in well under a millisecond", () => {
    // Warm the module so the measurement covers the schema alone.
    mapConfig(SAMPLE_PAYLOAD);

    const iterations = 200;
    const started = performance.now();
    for (let index = 0; index < iterations; index += 1) {
      mapConfig(SAMPLE_PAYLOAD);
    }
    const perParse = (performance.now() - started) / iterations;

    expect(perParse).toBeLessThan(1);
  });
});
