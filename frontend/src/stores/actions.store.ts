import { create } from "zustand";

import type { GameAction } from "@/types/domain";

/** The action to run plus the context the calling surface already had — the
 * palette presets nothing, a mod row presets its mod. */
export interface ActionRequest {
  action: GameAction;
  presetArgs: Record<string, unknown>;
}

interface ActionState {
  request: ActionRequest | null;
  open: (request: ActionRequest) => void;
  close: () => void;
}

/**
 * An action request is a domain object, so it gets its own store: it has to
 * survive the palette closing underneath it and stay re-openable from a mod
 * row without the two surfaces knowing about each other.
 */
export const useActionsStore = create<ActionState>()((set) => ({
  request: null,
  open: (request) => set({ request }),
  close: () => set({ request: null }),
}));

export const selectActionRequest = (state: ActionState) => state.request;
