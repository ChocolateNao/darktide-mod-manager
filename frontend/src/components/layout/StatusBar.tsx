import { AlertTriangle, CheckCircle2, Loader2, XCircle } from "lucide-react";

import { formatNumber, formatPercent } from "@/lib/format";
import { t } from "@/lib/i18n";
import {
  isRunning,
  selectEntries,
  useActivityStore,
} from "@/stores/activity.store";
import { useConfigStore } from "@/stores/config.store";
import { useModsStore } from "@/stores/mods.store";

/**
 * What the application is doing, how much is left, and how big the library is.
 * Always present so the content above never shifts.
 */
export function StatusBar() {
  const inProgress = useActivityStore(selectEntries).filter(isRunning);
  const mods = useModsStore((state) => state.mods);
  const enabled = mods.filter((mod) => mod.enabled).length;
  const logLevel = useConfigStore((state) => state.config?.logging.level ?? "");

  return (
    <footer className="flex h-7 shrink-0 items-center gap-4 border-t px-4 text-[11px] text-muted-foreground">
      <div className="flex min-w-0 flex-1 items-center gap-3">
        {inProgress.length === 0 ? (
          <span className="flex items-center gap-1.5">
            <CheckCircle2 className="size-3 text-success" />
            {t("status.idle")}
          </span>
        ) : (
          inProgress.slice(0, 3).map((entry) => (
            <span key={entry.id} className="flex items-center gap-1.5">
              {entry.state === "failed" ? (
                <XCircle className="size-3 text-destructive" />
              ) : entry.progress === null ? (
                <Loader2 className="size-3 animate-spin" />
              ) : (
                <span
                  aria-hidden
                  className="block h-1 w-10 overflow-hidden rounded-full bg-muted"
                >
                  <span
                    className="block h-full rounded-full bg-(--game-accent)"
                    style={{ width: formatPercent(entry.progress) }}
                  />
                </span>
              )}
              <span className="truncate">{entry.label}</span>
              {entry.failure && (
                <AlertTriangle
                  className="size-3 shrink-0 text-warning"
                  aria-label={entry.failure}
                />
              )}
            </span>
          ))
        )}
        {inProgress.length > 3 && (
          <span>+{formatNumber(inProgress.length - 3)}</span>
        )}
      </div>

      <span className="shrink-0 tabular-nums">
        {t("status.modsEnabled", {
          enabled: formatNumber(enabled),
          total: formatNumber(mods.length),
        })}
      </span>
      {logLevel && <span className="shrink-0 uppercase">{logLevel}</span>}
    </footer>
  );
}
