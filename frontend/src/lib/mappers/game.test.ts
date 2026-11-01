import { describe, expect, it } from "vitest";

import { mapGameInfos, mapInstallations } from "./game";

describe("game schemas and mappers", () => {
  it("reads the install modes the gate added so the UI can offer only what a game deploys", () => {
    const [game] = mapGameInfos([
      {
        id: "darktide",
        display_name: "Darktide",
        default_install_method: "mods_folder",
        install_modes: ["mods_folder", "game_root"],
        is_default: true,
        is_active: true,
      },
    ]);

    expect(game.installModes).toEqual(["mods_folder", "game_root"]);
    expect(game.defaultInstallMethod).toBe("mods_folder");
    expect(game.isActive).toBe(true);
  });

  it("defaults a game that predates the install-mode fields", () => {
    const [game] = mapGameInfos([
      {
        id: "other",
        display_name: "Other",
        is_default: false,
        is_active: false,
      },
    ]);

    expect(game.installModes).toEqual([]);
    expect(game.defaultInstallMethod).toBe("");
  });

  it("maps an installation with its detection time and validity", () => {
    const [found] = mapInstallations([
      {
        detected_at: "2026-10-15T09:00:00Z",
        game_id: "darktide",
        path: "/games/darktide",
        source: "steam",
        app_id: "1211180",
        version: "1.0",
        mods_path: "/games/darktide/mods",
        platform: "linux",
        valid: true,
      },
    ]);

    expect(found.source).toBe("steam");
    expect(found.modsPath).toBe("/games/darktide/mods");
    expect(found.valid).toBe(true);
    expect(found.detectedAt?.toISOString()).toBe("2026-10-15T09:00:00.000Z");
  });

  it("keeps an invalid detection, not dropping it", () => {
    // The Add Game dialog lists invalid detections alongside missing ones.
    const [found] = mapInstallations([
      { path: "/games/darktide", valid: false },
    ]);

    expect(found.valid).toBe(false);
    expect(found.gameId).toBeUndefined();
    expect(found.detectedAt).toBeNull();
  });

  it("refuses a payload with no id", () => {
    expect(() => mapGameInfos([{ display_name: "Darktide" }])).toThrow();
  });
});
