/**
 * The mods table's answer to "what order does this game load these mods in?".
 *
 * The explicit load order is the truth here; `Mod.Priority` is only the
 * tiebreak for mods it does not name, the same rule `syncLoadOrderFile` applies
 * when writing `mod_load_order.txt`.
 */

import type { Mod } from "@/types/domain";

export interface ResolvedLoadOrder {
  ordered: Mod[];
  /** 1-based load position per mod id. */
  positions: Map<string, number>;
}

export function resolveLoadOrder(
  mods: Mod[],
  loadOrder: string[]
): ResolvedLoadOrder {
  const byId = new Map(mods.map((mod) => [mod.id, mod]));
  const placed = new Set<string>();
  const ordered: Mod[] = [];

  for (const id of loadOrder) {
    if (placed.has(id)) continue;
    const mod = byId.get(id);
    if (!mod) continue;

    placed.add(id);
    ordered.push(mod);
  }

  const unplaced = mods.filter((mod) => !placed.has(mod.id));
  unplaced.sort((a, b) => b.priority - a.priority || a.id.localeCompare(b.id));
  ordered.push(...unplaced);

  const positions = new Map<string, number>();
  ordered.forEach((mod, index) => positions.set(mod.id, index + 1));

  return { ordered, positions };
}
