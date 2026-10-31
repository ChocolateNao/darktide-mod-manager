/**
 * Installed mods and everything derived about them for the current game. The
 * slice is replaced wholesale on a game switch: every field is per-game, and a
 * stale entry surviving the switch is worse than a brief empty table.
 */

import { create } from "zustand";

import { resolveLoadOrder } from "@/lib/loadOrder";
import type {
  ConflictReport,
  Mod,
  ModLinkStatus,
  ModUpdateInfo,
} from "@/types/domain";

export interface ModsState {
  mods: Mod[];
  loadOrder: string[];
  updates: ModUpdateInfo[];
  conflicts: ConflictReport[];
  linkStatuses: Map<string, ModLinkStatus>;
  selection: string[];
  setMods: (mods: Mod[]) => void;
  setLoadOrder: (ids: string[]) => void;
  setUpdates: (updates: ModUpdateInfo[]) => void;
  setConflicts: (conflicts: ConflictReport[]) => void;
  setLinkStatuses: (statuses: ModLinkStatus[]) => void;
  setSelection: (ids: string[]) => void;
  toggleSelection: (id: string) => void;
  clearSelection: () => void;
  /** Patches one row in place — used by the optimistic enable/disable and by the event that confirms it. */
  patchMod: (id: string, patch: Partial<Mod>) => void;
  /** Replaces rows and load order together; a mod the new order omits keeps its row at the end. */
  applyLoadOrder: (ids: string[]) => void;
  clearForGameSwitch: () => void;
}

const EMPTY: Pick<
  ModsState,
  "mods" | "loadOrder" | "updates" | "conflicts" | "linkStatuses" | "selection"
> = {
  mods: [],
  loadOrder: [],
  updates: [],
  conflicts: [],
  linkStatuses: new Map(),
  selection: [],
};

export const useModsStore = create<ModsState>()((set) => ({
  ...EMPTY,

  setMods: (mods) => set({ mods }),
  setLoadOrder: (loadOrder) => set({ loadOrder }),
  setUpdates: (updates) => set({ updates }),
  setConflicts: (conflicts) => set({ conflicts }),
  setLinkStatuses: (statuses) =>
    set({ linkStatuses: new Map(statuses.map((s) => [s.modId, s])) }),
  setSelection: (selection) => set({ selection }),
  toggleSelection: (id) =>
    set((state) => ({
      selection: state.selection.includes(id)
        ? state.selection.filter((entry) => entry !== id)
        : [...state.selection, id],
    })),
  clearSelection: () => set({ selection: [] }),

  patchMod: (id, patch) =>
    set((state) => ({
      mods: state.mods.map((mod) =>
        mod.id === id ? { ...mod, ...patch } : mod
      ),
    })),

  applyLoadOrder: (ids) =>
    set((state) => ({
      loadOrder: ids,
      mods: resolveLoadOrder(state.mods, ids).ordered,
    })),

  clearForGameSwitch: () => set({ ...EMPTY, linkStatuses: new Map() }),
}));

export const selectMods = (state: ModsState) => state.mods;
export const selectLoadOrder = (state: ModsState) => state.loadOrder;
export const selectConflicts = (state: ModsState) => state.conflicts;
export const selectUpdates = (state: ModsState) => state.updates;
export const selectSelection = (state: ModsState) => state.selection;

export function selectMod(state: ModsState, id: string | null): Mod | null {
  if (!id) return null;
  return state.mods.find((mod) => mod.id === id) ?? null;
}

export function selectUpdateFor(
  state: ModsState,
  modId: string
): ModUpdateInfo | null {
  return state.updates.find((update) => update.modId === modId) ?? null;
}

/** Link status for one mod; the Files tab needs a stable "not checked" answer. */
export function selectLinkStatus(
  state: ModsState,
  modId: string
): ModLinkStatus | null {
  return state.linkStatuses.get(modId) ?? null;
}
