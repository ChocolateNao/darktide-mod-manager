import { describe, expect, it } from "vitest";

import { mapLogFiles, mapLogLines, mapLogSettings } from "./log";

// `Raw` is `json:"-"`, so the redacted `Fields` map is all there is to read.
describe("log mappers", () => {
  const lineWire = {
    timestamp: "2026-10-15T09:00:00Z",
    level: "info",
    caller: "mod_service",
    message: "installed coolmod",
    mod_id: "coolmod",
    fields: { version: "1.2.0", api_key: "***" },
    Raw: "the whole original line",
  };

  it("maps a line and drops the Raw field entirely", () => {
    const [line] = mapLogLines([lineWire]);

    expect(line.level).toBe("info");
    expect(line.caller).toBe("mod_service");
    expect(line.modId).toBe("coolmod");
    expect(line.fields?.api_key).toBe("***");
    expect("raw" in line).toBe(false);
  });

  it("keeps a caller-less line usable", () => {
    const [line] = mapLogLines([
      { ...lineWire, caller: undefined, mod_id: undefined },
    ]);

    expect(line.caller).toBeUndefined();
    expect(line.modId).toBeUndefined();
  });

  it("maps the settings the popover edits", () => {
    const settings = mapLogSettings({
      level: "debug",
      directory: "/home/andre/.local/share/darktide-mod-manager/logs",
      file: "dmm.log",
      max_size_mb: 10,
      max_backups: 5,
      max_age_days: 30,
      compress: true,
      console_only: false,
      has_redact_fields: true,
    });

    expect(settings.level).toBe("debug");
    expect(settings.maxSizeMb).toBe(10);
    // The redaction list is never returned, only whether one is configured.
    expect(settings.hasRedactFields).toBe(true);
  });

  it("maps log files with their rotation and compression flags", () => {
    const files = mapLogFiles([
      {
        mod_time: "2026-10-14T09:00:00Z",
        name: "dmm.log",
        path: "/logs/dmm.log",
        size: 1024,
        rotated: false,
        compressed: false,
      },
      {
        mod_time: "2026-10-13T09:00:00Z",
        name: "dmm.log.1.gz",
        path: "/logs/dmm.log.1.gz",
        size: 2048,
        rotated: true,
        compressed: true,
      },
    ]);

    expect(files[0].rotated).toBe(false);
    expect(files[1].compressed).toBe(true);
    expect(files[0].modTime?.toISOString()).toBe("2026-10-14T09:00:00.000Z");
  });
});
