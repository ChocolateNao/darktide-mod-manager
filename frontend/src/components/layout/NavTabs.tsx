import {
  Boxes,
  Download,
  FileText,
  Share2,
  SlidersHorizontal,
} from "lucide-react";

import { Button } from "@/components/ui/button";
import { t } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import type { ViewId } from "@/stores/ui.store";
import { useUiStore, VIEWS } from "@/stores/ui.store";

const VIEW_ICONS = {
  mods: Boxes,
  "add-mod": Download,
  share: Share2,
  settings: SlidersHorizontal,
  logs: FileText,
} as const;

/**
 * The tab row answers the other half of the navigation question: which view.
 *
 * Labelled, because the rail is where each item has artwork and a name the user
 * already knows, and a row of five unlabelled icons is a guessing game.
 */
export function NavTabs() {
  const view = useUiStore((state) => state.view);
  const setView = useUiStore((state) => state.setView);

  return (
    <div
      role="tablist"
      aria-label={t("nav.label")}
      className="flex items-end gap-1 border-b px-4"
    >
      {VIEWS.map((id) => {
        const Icon = VIEW_ICONS[id];
        const selected = view === id;

        return (
          <Button
            key={id}
            role="tab"
            type="button"
            aria-selected={selected}
            variant="ghost"
            onClick={() => setView(id)}
            className={cn(
              "h-auto gap-1.5 rounded-none rounded-t-md border-b-2 px-3 py-2 font-normal transition-colors hover:bg-transparent hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring has-[svg]:px-3 dark:hover:bg-transparent",
              selected
                ? "border-(--game-accent) font-medium text-foreground"
                : "border-transparent text-muted-foreground hover:text-foreground"
            )}
          >
            <Icon className="size-4" />
            {NAV_LABELS[id]}
          </Button>
        );
      })}
    </div>
  );
}

/** Labels per view, keyed the same as `VIEW_ICONS`. */
const NAV_LABELS: Record<ViewId, string> = {
  mods: t("nav.mods"),
  "add-mod": t("nav.addMod"),
  share: t("nav.share"),
  settings: t("nav.settings"),
  logs: t("nav.logs"),
};
