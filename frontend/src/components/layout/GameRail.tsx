import { useState } from "react";
import { Plus } from "lucide-react";

import { GameIcon } from "@/components/common/GameIcon";
import { AddGameDialog } from "@/components/games/AddGameDialog";
import { Button } from "@/components/ui/button";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { useSwitchGame } from "@/hooks/mutations";
import { useGamesQuery } from "@/hooks/queries";
import { gameAccent } from "@/lib/gameAccent";
import { t } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { selectConfig, useConfigStore } from "@/stores/config.store";
import {
  selectActiveGameId,
  selectGames,
  useGamesStore,
} from "@/stores/games.store";
import { selectUpdates, useModsStore } from "@/stores/mods.store";
import type { GameSettings } from "@/types/domain";

/** A stable empty object so the rail selector keeps one reference when unset. */
const EMPTY_GAMES: Record<string, GameSettings> = {};

/**
 * The rail answers one question: which game.
 *
 * Icons only, since the rail has room for artwork and every icon carries a
 * tooltip. Settings lives in the tab row, so there is no second gear here.
 */
export function GameRail() {
  useGamesQuery();

  const games = useGamesStore(selectGames);
  const activeGameId = useGamesStore(selectActiveGameId);
  const invalidGameIds = useGamesStore((state) => state.invalidGameIds);
  const updates = useModsStore(selectUpdates);
  const gameSettings = useConfigStore(
    (state) => selectConfig(state)?.games ?? EMPTY_GAMES
  );
  const [addGameOpen, setAddGameOpen] = useState(false);

  const switchGame = useSwitchGame();

  // `CheckModUpdates` answers for the active game only — it has no game column
  // to attribute a result to — so the dot appears on that one game alone.
  const activeHasUpdates =
    activeGameId !== null && updates.some((info) => info.available);

  const choose = (gameId: string) => {
    if (gameId === activeGameId) return;
    switchGame.mutate(gameId, {
      onSuccess: () => useGamesStore.getState().setActiveGameId(gameId),
    });
  };

  return (
    <nav
      aria-label={t("rail.label")}
      className="flex w-17 shrink-0 flex-col items-center gap-1 border-r bg-rail py-3"
    >
      {games.map((game) => {
        const active = game.id === activeGameId;
        const accent = gameAccent(game.id);

        return (
          <Tooltip key={game.id}>
            <TooltipTrigger asChild>
              <button
                type="button"
                aria-current={active ? "true" : undefined}
                aria-label={game.displayName}
                disabled={switchGame.isPending}
                onClick={() => choose(game.id)}
                className={cn(
                  "relative flex size-11 items-center justify-center rounded-md transition-colors",
                  "focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none",
                  active ? "bg-rail-active" : "hover:bg-rail-active/60"
                )}
                style={active ? { color: accent } : undefined}
              >
                {active && (
                  <span
                    aria-hidden
                    className="absolute inset-y-1.5 -left-3.25 w-0.75 rounded-full"
                    style={{ backgroundColor: accent }}
                  />
                )}
                <GameIcon
                  gameId={game.id}
                  icon={gameSettings[game.id]?.icon}
                  className="size-5"
                />
                {invalidGameIds.has(game.id) && (
                  <span
                    title={t("rail.invalidInstallation")}
                    aria-label={t("rail.invalidInstallation")}
                    className="absolute top-1.5 right-1.5 size-2 rounded-full bg-warning"
                  />
                )}
                {active && activeHasUpdates && (
                  <span
                    title={t("rail.updatesAvailable")}
                    aria-label={t("rail.updatesAvailable")}
                    className="absolute right-1.5 bottom-1.5 size-2 rounded-full bg-info"
                  />
                )}
              </button>
            </TooltipTrigger>
            <TooltipContent side="right">{game.displayName}</TooltipContent>
          </Tooltip>
        );
      })}

      <div className="mt-auto">
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              type="button"
              variant="ghost"
              size="icon"
              aria-label={t("rail.addGame")}
              onClick={() => setAddGameOpen(true)}
            >
              <Plus className="size-5" />
            </Button>
          </TooltipTrigger>
          <TooltipContent side="right">{t("rail.addGame")}</TooltipContent>
        </Tooltip>
      </div>

      <AddGameDialog open={addGameOpen} onOpenChange={setAddGameOpen} />
    </nav>
  );
}
