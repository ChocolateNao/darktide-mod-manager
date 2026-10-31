import { useCallback, useMemo } from "react";

import { useAvailableActionsQuery } from "@/hooks/queries";
import { useActionsStore } from "@/stores/actions.store";
import { useGamesStore } from "@/stores/games.store";
import type { GameAction } from "@/types/domain";

/**
 * Categories a mod row may offer. Actions are declared per game, so the
 * frontend can only know which *categories* make a mod the subject — anything
 * outside this list stays in the palette, the surface for the whole set.
 */
const MOD_ACTION_CATEGORIES = new Set([
  "mod",
  "mods",
  "hooks",
  "install",
  "update",
  "uninstall",
  "source",
]);

function isModScoped(action: GameAction): boolean {
  return MOD_ACTION_CATEGORIES.has(action.category.trim().toLowerCase());
}

/**
 * Returns the whole query object, so a caller can tell "this game declares
 * nothing" from "we have not asked" — a bare `[]` once made the palette report
 * an empty catalogue for a parse error.
 */
export function useGameActions() {
  const activeGameId = useGamesStore((state) => state.activeGameId);
  const query = useAvailableActionsQuery(
    activeGameId ?? "",
    Boolean(activeGameId)
  );

  return useMemo(() => ({ ...query, actions: query.data ?? [] }), [query]);
}

export function useGameActionsForMod() {
  const { actions } = useGameActions();
  const open = useActionsStore((state) => state.open);

  const scoped = useMemo(() => actions.filter(isModScoped), [actions]);

  const run = useCallback(
    (actionId: string) => {
      const action = scoped.find((candidate) => candidate.id === actionId);

      if (action) {
        open({ action, presetArgs: {} });
      }
    },
    [open, scoped]
  );

  return { actions: scoped, run };
}
