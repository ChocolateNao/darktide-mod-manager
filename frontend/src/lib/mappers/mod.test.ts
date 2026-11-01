import { describe, expect, it } from "vitest";

import { mapConfig } from "@/lib/mappers/config";
import { mapHooks } from "@/lib/mappers/mod";
import { configWireSchema } from "@/lib/schemas/config";
import { hookPhases } from "@/lib/schemas/mod";

/** Go always marshals the permission block, so every fixture needs one. */
const permissionsDefault = {
  fs_read: true,
  fs_write: false,
  config_read: true,
  config_write: false,
  storage_read: true,
  env_read: false,
};

const baseConfig = {
  hooks: {
    permissions: { default: permissionsDefault },
    default_timeout: "10s",
    max_timeout: "30s",
  },
  sources: {},
  games: {},
  active_game: "darktide",
  ui: { theme: "dark", language: "en", compact_mode: false },
  advanced: { enable_experimental: false },
  storage: { path: "", backup_retention_days: 7 },
  cache: { path: "", max_size_gb: 5, max_age_days: 30, verify_hashes: true },
  cloud_sharing: {
    providers: {},
    default_provider: "",
    fallback_providers: [],
    share_expiry_days: 30,
    auto_install: false,
  },
  sharing: { p2p: { stun_servers: [], turn_relays: [], expiry_days: 7 } },
  logging: {
    level: "info",
    max_size_mb: 10,
    max_backups: 5,
    max_age_days: 30,
    compress: true,
  },
};

describe("mapConfig", () => {
  it("keeps an unset notify_on_update distinct from an explicit false", () => {
    // JSON cannot tell an absent boolean from `false`, and `ApplyDefaults` leaves
    // an absent key alone, so collapsing them would switch notifications off.
    const inherited = mapConfig({
      ...baseConfig,
      auto_update: {
        enabled: true,
        check_interval_hours: 24,
        download_automatically: false,
      },
    });
    expect(inherited.autoUpdate.notifyOnUpdate).toBeUndefined();

    const explicitFalse = mapConfig({
      ...baseConfig,
      auto_update: {
        enabled: true,
        check_interval_hours: 24,
        download_automatically: false,
        notify_on_update: false,
      },
    });
    expect(explicitFalse.autoUpdate.notifyOnUpdate).toBe(false);
  });

  it("reads the permission block into camelCase with the default scope", () => {
    const mapped = mapConfig({
      ...baseConfig,
      auto_update: {
        enabled: false,
        check_interval_hours: 24,
        download_automatically: false,
      },
    });

    expect(mapped.hooks.permissions.default.fsRead).toBe(true);
    expect(mapped.hooks.permissions.default.envRead).toBe(false);
    expect(mapped.hooks.permissions.games).toEqual({});
    expect(mapped.security.allowActionCommands).toBe(true);
  });

  it("rejects a payload that lost a field, not producing a half-built config", () => {
    expect(
      configWireSchema.safeParse({ active_game: "darktide" }).success
    ).toBe(false);
  });
});

describe("mapHooks", () => {
  it("fills every phase so the editor can bind to one without a lookup guard", () => {
    const mapped = mapHooks({
      pre_install: [{ language: "lua", content: "print(1)" }],
    });

    // Phase names stay snake_case on both sides of the bridge: Go identifiers.
    expect(Object.keys(mapped).sort()).toEqual([...hookPhases].sort());
    expect(mapped.pre_install).toHaveLength(1);
    expect(mapped.post_update).toEqual([]);
  });

  it("reads the snake_case field names Go marshals", () => {
    const mapped = mapHooks({
      pre_enable: [
        {
          language: "javascript",
          content: "console.log(1)",
          working_dir: "/tmp",
          env: ["A=1"],
          timeout: 5,
        },
      ],
    });

    expect(mapped.pre_enable[0]).toMatchObject({
      language: "javascript",
      content: "console.log(1)",
      workingDir: "/tmp",
      env: ["A=1"],
      timeout: 5,
    });
  });
});
