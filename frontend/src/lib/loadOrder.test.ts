import { describe, expect, it } from "vitest";

import { resolveLoadOrder } from "./loadOrder";

import { hookPhases } from "@/lib/schemas/mod";
import type { Mod, ModHooks } from "@/types/domain";

function mod(id: string, overrides: Partial<Mod> = {}): Mod {
  return {
    id,
    name: id,
    version: "1.0.0",
    author: "",
    description: "",
    sourceId: "local",
    sourceData: {},
    localPath: `/mods/${id}`,
    installedAt: null,
    updatedAt: null,
    enabled: true,
    external: false,
    priority: 0,
    tags: [],
    issues: [],
    dependencies: [],
    conflictsWith: [],
    hooks: Object.fromEntries(
      hookPhases.map((phase) => [phase, []])
    ) as unknown as ModHooks,
    ...overrides,
  };
}

const names = (mods: Mod[]) => mods.map((entry) => entry.id);

describe("resolveLoadOrder", () => {
  it("puts the mods in the order the backend reported", () => {
    const { ordered, positions } = resolveLoadOrder(
      [mod("a"), mod("b"), mod("c")],
      ["c", "a", "b"]
    );

    expect(names(ordered)).toEqual(["c", "a", "b"]);
    expect(positions.get("c")).toBe(1);
    expect(positions.get("a")).toBe(2);
    expect(positions.get("b")).toBe(3);
  });

  it("keeps every mod, appending the ones the load order never placed", () => {
    const { ordered, positions } = resolveLoadOrder(
      [mod("a"), mod("late")],
      ["a"]
    );

    expect(names(ordered)).toEqual(["a", "late"]);
    expect(positions.get("late")).toBe(2);
  });

  it("ranks the unplaced mods by the backend's own tiebreak", () => {
    // mod_service.go sorts unplaced mods by priority descending, then id — the
    // table must match or its numbers lie about the load order.
    const { ordered } = resolveLoadOrder(
      [mod("a"), mod("low", { priority: 1 }), mod("high", { priority: 9 })],
      ["a"]
    );

    expect(names(ordered)).toEqual(["a", "high", "low"]);
  });

  it("breaks a priority tie by id so the order does not wobble", () => {
    const { ordered } = resolveLoadOrder(
      [mod("b", { priority: 5 }), mod("a", { priority: 5 })],
      []
    );

    expect(names(ordered)).toEqual(["a", "b"]);
  });

  it("ignores ids that no longer name an installed mod", () => {
    // A load order file outlives the mod it names, and a position counted over a
    // missing row is a number the user cannot do anything with.
    const { ordered, positions } = resolveLoadOrder(
      [mod("a"), mod("b")],
      ["uninstalled", "b"]
    );

    expect(names(ordered)).toEqual(["b", "a"]);
    expect(positions.get("b")).toBe(1);
  });

  it("survives a load order that names the same mod twice", () => {
    const { ordered, positions } = resolveLoadOrder(
      [mod("a"), mod("b")],
      ["a", "a", "b"]
    );

    expect(names(ordered)).toEqual(["a", "b"]);
    expect(positions.get("a")).toBe(1);
  });

  it("has no position for a mod it was never given", () => {
    const { positions } = resolveLoadOrder([mod("a")], []);

    expect(positions.get("ghost")).toBeUndefined();
  });
});
