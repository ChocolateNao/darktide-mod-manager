import type { ConflictReport, ConflictResolution, Mod } from "@/types/domain";

export interface ConflictSides {
  /** The mod the backend resolves as "mod-a": the smaller id. */
  a?: Mod;
  /** The mod the backend resolves as "mod-b": the larger id. */
  b?: Mod;
  both: boolean;
}

/**
 * The backend sorts a report's two mods by id before resolving, so
 * "disable-mod-a" targets the smaller id, not `report.modA`. This mirrors that
 * sort so a button never names the wrong mod. An empty id sorts last.
 */
export function conflictSides(report: ConflictReport): ConflictSides {
  const { modA, modB } = report;
  const aId = modA?.id ?? "";
  const bId = modB?.id ?? "";
  const swap = aId === "" || (bId !== "" && bId < aId);

  return {
    a: swap ? modB : modA,
    b: swap ? modA : modB,
    both: modA != null && modB != null,
  };
}

/** A button the popup renders: either a conflict resolution or a direct fix. */
export type ConflictAction =
  | { kind: "resolve"; how: ConflictResolution }
  | { kind: "download"; sourceId: string; modId: string }
  | { kind: "enable"; modId: string }
  | { kind: "update"; modId: string };

const resolve = (how: ConflictResolution): ConflictAction => ({
  kind: "resolve",
  how,
});

function resolveBoth(): ConflictAction[] {
  return [
    resolve("keep-both"),
    resolve("disable-mod-a"),
    resolve("disable-mod-b"),
    resolve("uninstall-mod-a"),
    resolve("uninstall-mod-b"),
  ];
}

/** The actions that make sense for a report's type, in display order. */
export function actionsFor(report: ConflictReport): ConflictAction[] {
  const sides = conflictSides(report);
  const { type, modA, modB } = report;

  switch (type) {
    case "file_overlap":
      return sides.both
        ? resolveBoth()
        : [
            resolve("keep-both"),
            resolve("disable-mod-a"),
            resolve("uninstall-mod-a"),
          ];

    case "duplicate_id":
      // A shared payload folder can only be fixed by removing one side; disabling
      // leaves the folder collision in place.
      return sides.both
        ? [resolve("uninstall-mod-a"), resolve("uninstall-mod-b")]
        : [resolve("uninstall-mod-a")];

    case "declared":
      return sides.both
        ? [
            resolve("disable-mod-a"),
            resolve("disable-mod-b"),
            resolve("uninstall-mod-a"),
            resolve("uninstall-mod-b"),
          ]
        : [resolve("disable-mod-a"), resolve("uninstall-mod-a")];

    case "version_mismatch": {
      // The dependent (ModA) cannot use what is installed. Updating the
      // dependency is the fix; removing the dependent is the fallback.
      const actions: ConflictAction[] = [];
      if (modB) actions.push({ kind: "update", modId: modB.id });
      const dependentIsA = modA?.id === sides.a?.id;
      actions.push(resolve(dependentIsA ? "disable-mod-a" : "disable-mod-b"));
      actions.push(
        resolve(dependentIsA ? "uninstall-mod-a" : "uninstall-mod-b")
      );
      actions.push(resolve("keep-both"));
      return actions;
    }

    case "load_order":
      if (!modB) {
        // Missing dependency: install it, or drop the mod that needs it.
        const actions: ConflictAction[] = [];
        if (report.dependencyId && modA?.sourceId) {
          actions.push({
            kind: "download",
            sourceId: modA.sourceId,
            modId: report.dependencyId,
          });
        }
        actions.push(resolve("disable-mod-a"));
        actions.push(resolve("uninstall-mod-a"));
        return actions;
      }
      if (!modB.enabled) {
        // Installed but disabled: turning it back on resolves the conflict.
        return [
          { kind: "enable", modId: modB.id },
          resolve("disable-mod-a"),
          resolve("keep-both"),
        ];
      }
      // A cycle or a bad ordering: accept it or disable one side.
      return [
        resolve("keep-both"),
        resolve("disable-mod-a"),
        resolve("disable-mod-b"),
      ];

    default:
      return sides.both
        ? resolveBoth()
        : [
            resolve("keep-both"),
            resolve("disable-mod-a"),
            resolve("uninstall-mod-a"),
          ];
  }
}
