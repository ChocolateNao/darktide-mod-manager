import { describe, expect, it } from "vitest";

import { actionsFor, conflictSides } from "./conflicts";

import { hookPhases } from "@/lib/schemas/mod";
import type { ConflictReport, Mod, ModHooks } from "@/types/domain";

function mod(id: string, overrides: Partial<Mod> = {}): Mod {
  return {
    id,
    name: id.toUpperCase(),
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

function report(
  type: ConflictReport["type"],
  modA: Mod | undefined,
  modB: Mod | undefined
): ConflictReport {
  return {
    id: "x",
    type,
    modA,
    modB,
    details: "",
    severity: "error",
    recommendation: "",
  };
}

describe("conflictSides", () => {
  it("sorts the two mods by id", () => {
    const sides = conflictSides(report("declared", mod("beta"), mod("alpha")));

    expect(sides.a?.id).toBe("alpha");
    expect(sides.b?.id).toBe("beta");
    expect(sides.both).toBe(true);
  });

  it("keeps the single mod as side a for a one-sided report", () => {
    const sides = conflictSides(report("load_order", mod("only"), undefined));

    expect(sides.a?.id).toBe("only");
    expect(sides.b).toBeUndefined();
    expect(sides.both).toBe(false);
  });
});

describe("actionsFor", () => {
  it("offers every resolution for a file overlap", () => {
    expect(actionsFor(report("file_overlap", mod("b"), mod("a")))).toEqual([
      { kind: "resolve", how: "keep-both" },
      { kind: "resolve", how: "disable-mod-a" },
      { kind: "resolve", how: "disable-mod-b" },
      { kind: "resolve", how: "uninstall-mod-a" },
      { kind: "resolve", how: "uninstall-mod-b" },
    ]);
  });

  it("offers only removal for duplicate ids", () => {
    expect(actionsFor(report("duplicate_id", mod("b"), mod("a")))).toEqual([
      { kind: "resolve", how: "uninstall-mod-a" },
      { kind: "resolve", how: "uninstall-mod-b" },
    ]);
  });

  it("offers disable and uninstall for a declared conflict", () => {
    expect(actionsFor(report("declared", mod("b"), mod("a")))).toEqual([
      { kind: "resolve", how: "disable-mod-a" },
      { kind: "resolve", how: "disable-mod-b" },
      { kind: "resolve", how: "uninstall-mod-a" },
      { kind: "resolve", how: "uninstall-mod-b" },
    ]);
  });

  it("updates the dependency on a version mismatch, even when it sorts last", () => {
    // modA is the dependent; here its id sorts last, so removal targets mod-b.
    expect(actionsFor(report("version_mismatch", mod("z"), mod("a")))).toEqual([
      { kind: "update", modId: "a" },
      { kind: "resolve", how: "disable-mod-b" },
      { kind: "resolve", how: "uninstall-mod-b" },
      { kind: "resolve", how: "keep-both" },
    ]);
  });

  it("updates the dependency on a version mismatch when it sorts first", () => {
    expect(actionsFor(report("version_mismatch", mod("a"), mod("z")))).toEqual([
      { kind: "update", modId: "z" },
      { kind: "resolve", how: "disable-mod-a" },
      { kind: "resolve", how: "uninstall-mod-a" },
      { kind: "resolve", how: "keep-both" },
    ]);
  });

  it("downloads a missing dependency, or removes the dependent", () => {
    const r = report("load_order", mod("x"), undefined);
    r.dependencyId = "missing";

    expect(actionsFor(r)).toEqual([
      { kind: "download", sourceId: "local", modId: "missing" },
      { kind: "resolve", how: "disable-mod-a" },
      { kind: "resolve", how: "uninstall-mod-a" },
    ]);
  });

  it("skips the download when the dependent has no source", () => {
    const r = report("load_order", mod("x", { sourceId: "" }), undefined);
    r.dependencyId = "missing";

    expect(actionsFor(r)).toEqual([
      { kind: "resolve", how: "disable-mod-a" },
      { kind: "resolve", how: "uninstall-mod-a" },
    ]);
  });

  it("enables an installed-but-disabled dependency", () => {
    expect(
      actionsFor(report("load_order", mod("a"), mod("b", { enabled: false })))
    ).toEqual([
      { kind: "enable", modId: "b" },
      { kind: "resolve", how: "disable-mod-a" },
      { kind: "resolve", how: "keep-both" },
    ]);
  });

  it("offers keep-both or a disable for a load-order or cycle", () => {
    expect(actionsFor(report("load_order", mod("b"), mod("a")))).toEqual([
      { kind: "resolve", how: "keep-both" },
      { kind: "resolve", how: "disable-mod-a" },
      { kind: "resolve", how: "disable-mod-b" },
    ]);
  });
});
