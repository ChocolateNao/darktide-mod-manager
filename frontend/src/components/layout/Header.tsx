import { Command, RefreshCw } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { useCancelAll, useRefreshMods } from "@/hooks/mutations";
import { t } from "@/lib/i18n";
import { useActivityStore } from "@/stores/activity.store";
import { selectActiveGame, useGamesStore } from "@/stores/games.store";
import { useUiStore } from "@/stores/ui.store";

/**
 * The title, the active game, and the actions that run something or re-read the
 * world.
 */
export function Header() {
  const game = useGamesStore(selectActiveGame);
  const setPaletteOpen = useUiStore((state) => state.setPaletteOpen);

  const refresh = useRefreshMods();
  const cancel = useCancelAll();
  const running = useActivityStore((state) =>
    state.entries.some((entry) => entry.state === "running")
  );

  return (
    <header className="flex h-12 shrink-0 items-center justify-between gap-4 border-b px-4">
      <div className="flex min-w-0 items-baseline gap-3">
        <span className="truncate text-sm font-semibold">
          {t("shell.appName")}
        </span>
        <span
          className="truncate text-xs font-medium"
          style={{ color: "var(--game-accent)" }}
        >
          {game?.displayName ?? t("header.noGame")}
        </span>
      </div>

      <div className="flex items-center gap-1">
        <Button
          type="button"
          variant="ghost"
          size="sm"
          onClick={() => setPaletteOpen(true)}
          aria-label={t("header.actions")}
        >
          <Command className="size-4" />
          <span className="hidden sm:inline">{t("header.actions")}</span>
          <kbd className="ml-1 hidden rounded border px-1 font-mono text-[10px] opacity-60 sm:inline">
            Ctrl K
          </kbd>
        </Button>

        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              type="button"
              variant="ghost"
              size="icon"
              aria-label={t("header.refresh")}
              disabled={refresh.isPending}
              onClick={() => refresh.mutate()}
            >
              <RefreshCw
                className={refresh.isPending ? "size-4 animate-spin" : "size-4"}
              />
            </Button>
          </TooltipTrigger>
          <TooltipContent>{t("header.refresh")}</TooltipContent>
        </Tooltip>

        <Button
          type="button"
          variant="outline"
          size="sm"
          // A stop button that always works is one people learn to ignore.
          disabled={!running}
          loading={cancel.isPending}
          onClick={() => cancel.mutate()}
        >
          {t("header.cancelAll")}
        </Button>
      </div>
    </header>
  );
}
