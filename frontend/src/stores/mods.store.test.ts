import { beforeEach, describe, expect, it } from "vitest";

import { useModsStore } from "./mods.store";

import { hookPhases } from "@/lib/schemas/mod";
import type { Mod, ModHooks } from "@/types/domain";

function mod(id: string, overrides: Partial<Mod> = {}): Mod {
  const base: Mod = {
    id,
    name: id,
    version: "1.0.0",
    author: "",
    description: "",
    sourceId: "local",
    sourceData: {},
    localPath: `/mods/${id}`,
    installedAt: new Date("2026-10-15T00:00:00Z"),
    updatedAt: new Date("2026-10-15T00:00:00Z"),
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
  };

  // The override is a `Partial`, so spreading it widens every optional field back
  // to `| undefined`; the fixture only ever overrides with real values.
  return { ...base, ...overrides } as Mod;
}

describe("mods store", () => {
  beforeEach(() => {
    useModsStore.getState().setMods([]);
    useModsStore.getState().setLoadOrder([]);
    useModsStore.getState().clearSelection();
  });

  it("patches one row in place, which is what optimistic enable/disable needs", () => {
    useModsStore.getState().setMods([mod("a"), mod("b")]);

    useModsStore.getState().patchMod("b", { enabled: false });

    const rows = useModsStore.getState().mods;
    expect(rows[0].enabled).toBe(true);
    expect(rows[1].enabled).toBe(false);
  });

  it("leaves the other rows untouched by identity", () => {
    const first = mod("a");
    useModsStore.getState().setMods([first, mod("b")]);

    useModsStore.getState().patchMod("b", { enabled: false });

    expect(useModsStore.getState().mods[0]).toBe(first);
  });

  it("ignores a patch for a mod that is not in the list", () => {
    useModsStore.getState().setMods([mod("a")]);

    useModsStore.getState().patchMod("missing", { enabled: false });

    expect(useModsStore.getState().mods).toHaveLength(1);
  });

  it("reorders the rows to match a new load order", () => {
    useModsStore.getState().setMods([mod("a"), mod("b"), mod("c")]);

    useModsStore.getState().applyLoadOrder(["c", "a", "b"]);

    expect(useModsStore.getState().mods.map((entry) => entry.id)).toEqual([
      "c",
      "a",
      "b",
    ]);
    expect(useModsStore.getState().loadOrder).toEqual(["c", "a", "b"]);
  });

  it("drops ids the store does not know instead of leaving a hole", () => {
    useModsStore.getState().setMods([mod("a"), mod("b")]);

    useModsStore.getState().applyLoadOrder(["b", "gone", "a"]);

    expect(useModsStore.getState().mods.map((entry) => entry.id)).toEqual([
      "b",
      "a",
    ]);
  });

  it("keeps a mod the new order forgot, not making its row vanish", () => {
    // A mod installed while the reorder was in flight is not in the ids being
    // applied. The optimistic update must not delete its row until the refetch.
    useModsStore.getState().setMods([mod("a"), mod("b"), mod("late")]);

    useModsStore.getState().applyLoadOrder(["b", "a"]);

    expect(useModsStore.getState().mods.map((entry) => entry.id)).toEqual([
      "b",
      "a",
      "late",
    ]);
  });

  it("keeps priority as the backend reported it, because the file owns the order", () => {
    useModsStore
      .getState()
      .setMods([mod("a", { priority: 7 }), mod("b", { priority: 3 })]);

    useModsStore.getState().applyLoadOrder(["b", "a"]);

    const rows = useModsStore.getState().mods;
    expect(rows[0].id).toBe("b");
    expect(rows[0].priority).toBe(3);
    expect(rows[1].priority).toBe(7);
  });

  it("empties everything on a game switch, so no row survives into another game", () => {
    useModsStore.getState().setMods([mod("a")]);
    useModsStore.getState().setSelection(["a"]);

    useModsStore.getState().clearForGameSwitch();

    const state = useModsStore.getState();
    expect(state.mods).toEqual([]);
    expect(state.loadOrder).toEqual([]);
    expect(state.selection).toEqual([]);
    expect(state.linkStatuses.size).toBe(0);
  });

  it("toggles a selection without losing the rest", () => {
    useModsStore.getState().setSelection(["a", "b"]);

    useModsStore.getState().toggleSelection("b");
    expect(useModsStore.getState().selection).toEqual(["a"]);

    useModsStore.getState().toggleSelection("b");
    expect(useModsStore.getState().selection).toEqual(["a", "b"]);
  });
});
