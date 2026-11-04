import { PackagePlus } from "lucide-react";

import { EmptyState, LoadingState } from "@/components/common/Surfaces";
import { PageHeader } from "@/components/layout/PageHeader";
import { ConflictBanner } from "@/components/mods/ConflictBanner";
import { ModTable } from "@/components/mods/ModTable";
import { Button } from "@/components/ui/button";
import { useModsQuery } from "@/hooks/queries";
import { t } from "@/lib/i18n";
import { selectActiveGameId, useGamesStore } from "@/stores/games.store";
import { useUiStore } from "@/stores/ui.store";

/**
 * Mods is the default view because it is the only one that answers "what has the
 * manager done to my game", so it is what an idle window shows.
 */
export default function ModsView() {
  const activeGameId = useGamesStore(selectActiveGameId);
  const { mods } = useModsQuery(activeGameId !== null);
  const isPending = mods.isPending;
  const setView = useUiStore((state) => state.setView);

  return (
    <div className="flex h-full flex-col">
      <ConflictBanner />

      <PageHeader title={t("mods.title")}>
        <Button size="sm" variant="ghost" onClick={() => setView("add-mod")}>
          <PackagePlus className="size-4" />
          {t("nav.addMod")}
        </Button>
      </PageHeader>

      <div className="min-h-0 flex-1">
        {isPending && activeGameId !== null ? (
          <LoadingState className="h-full" />
        ) : activeGameId === null ? (
          <EmptyState
            icon={PackagePlus}
            title={t("mods.noGame")}
            description={t("mods.noGameHint")}
            action={
              <Button size="sm" onClick={() => setView("add-mod")}>
                {t("nav.addMod")}
              </Button>
            }
          />
        ) : (
          <ModTable />
        )}
      </div>
    </div>
  );
}
