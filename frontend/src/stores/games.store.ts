/**
 * Games and installations. Game definitions come from the Go registry, so a new
 * game plugin shows up in the rail without a frontend change — this slice only
 * ever mirrors what the backend reported.
 */

import { create } from "zustand";

import type { GameInfo, GameInstallation } from "@/types/domain";

export interface GamesState {
  games: GameInfo[];
  installations: GameInstallation[];
  activeGameId: string | null;
  /** Game whose installation did not validate, for the rail's warning dot. */
  invalidGameIds: ReadonlySet<string>;
  setGames: (games: GameInfo[]) => void;
  setInstallations: (installations: GameInstallation[]) => void;
  setActiveGameId: (gameId: string | null) => void;
  recomputeValidity: () => void;
}

function invalidIds(installations: GameInstallation[]): Set<string> {
  const invalid = new Set<string>();
  for (const installation of installations) {
    if (installation.gameId && !installation.valid)
      invalid.add(installation.gameId);
  }
  return invalid;
}

export const useGamesStore = create<GamesState>()((set, get) => ({
  games: [],
  installations: [],
  activeGameId: null,
  invalidGameIds: new Set<string>(),

  setGames: (games) => set({ games }),
  setInstallations: (installations) =>
    set({ installations, invalidGameIds: invalidIds(installations) }),

  setActiveGameId: (activeGameId) => set({ activeGameId }),

  // Validity is derived: recomputed on demand, since a second source of truth
  // would drift from the installations it summarises.
  recomputeValidity: () =>
    set({ invalidGameIds: invalidIds(get().installations) }),
}));

export const selectActiveGameId = (state: GamesState) => state.activeGameId;
export const selectGames = (state: GamesState) => state.games;
export const selectInstallations = (state: GamesState) => state.installations;

export function selectActiveGame(state: GamesState): GameInfo | null {
  const id = state.activeGameId;
  if (!id) return null;
  return state.games.find((game) => game.id === id) ?? null;
}
