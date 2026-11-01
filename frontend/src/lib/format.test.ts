import { describe, expect, it } from "vitest";

import {
  formatBytes,
  formatCount,
  formatDurationSeconds,
  formatNumber,
  formatPercent,
  parseDurationSeconds,
} from "@/lib/format";

describe("formatBytes", () => {
  it("keeps small values exact and rounds large ones to one decimal", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(1024)).toBe("1 KiB");
    expect(formatBytes(1536)).toBe("1.5 KiB");
    expect(formatBytes(5 * 1024 ** 3)).toBe("5 GiB");
  });

  it("rounds only below ten units so 1023 B stays exact but 20 MiB stays short", () => {
    expect(formatBytes(1023)).toBe("1023 B");
    expect(formatBytes(20 * 1024 ** 2)).toBe("20 MiB");
  });
});

describe("formatCount", () => {
  it("pluralises against the count", () => {
    expect(formatCount(1, "mod")).toBe("1 mod");
    expect(formatCount(2, "mod")).toBe("2 mods");
    expect(formatCount(0, "conflict")).toBe("0 conflicts");
  });
});

describe("formatNumber", () => {
  it("groups thousands without inventing a noun", () => {
    expect(formatNumber(1234)).toBe("1,234");
    expect(formatNumber(12)).toBe("12");
  });
});

describe("parseDurationSeconds", () => {
  it("reads the forms Go's Duration marshals to", () => {
    expect(parseDurationSeconds("30s")).toBe(30);
    expect(parseDurationSeconds("1h30m")).toBe(5400);
    expect(parseDurationSeconds("0")).toBe(0);
    expect(parseDurationSeconds("45")).toBe(45);
  });

  it("refuses anything it cannot read in full, not guessing", () => {
    expect(parseDurationSeconds("")).toBeNull();
    expect(parseDurationSeconds("ten seconds")).toBeNull();
    expect(parseDurationSeconds("1h junk")).toBeNull();
  });
});

describe("formatDurationSeconds", () => {
  it("shows at most two units so a countdown stays short", () => {
    expect(formatDurationSeconds(45)).toBe("45s");
    expect(formatDurationSeconds(90)).toBe("1m 30s");
    expect(formatDurationSeconds(5400)).toBe("1h 30m");
    expect(formatDurationSeconds(90_000)).toBe("1d 1h");
  });
});

describe("formatPercent", () => {
  it("clamps a progress fraction into the range a bar can draw", () => {
    expect(formatPercent(0)).toBe("0%");
    expect(formatPercent(0.5)).toBe("50%");
    expect(formatPercent(1.4)).toBe("100%");
    expect(formatPercent(-1)).toBe("0%");
  });
});
