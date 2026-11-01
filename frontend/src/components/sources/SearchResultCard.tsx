import { Download, ExternalLink } from "lucide-react";

import { openLink } from "@/bridge/dialogs";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { formatBytes } from "@/lib/format";
import { t } from "@/lib/i18n";
import type { Mod } from "@/types/domain";

interface SearchResultCardProps {
  mod: Mod;
  installed: boolean;
  busy: boolean;
  /** True for the card whose own install is in flight, so one spinner. */
  installing: boolean;
  onInstall: (mod: Mod) => void;
}

export function SearchResultCard({
  mod,
  installed,
  busy,
  installing,
  onInstall,
}: SearchResultCardProps) {
  return (
    <div className="flex items-start gap-3 rounded-md border px-3 py-2">
      <div className="min-w-0 flex-1">
        <div className="flex items-baseline gap-2">
          <span className="truncate font-medium">{mod.name}</span>
          <span className="shrink-0 font-mono text-xs text-muted-foreground">
            {mod.version}
          </span>
          {installed ? (
            <Badge variant="secondary">{t("add.alreadyInstalled")}</Badge>
          ) : null}
        </div>

        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
          {mod.author ? (
            <span>{t("add.by", { author: mod.author })}</span>
          ) : null}
          {mod.sourceId ? <span>{mod.sourceId}</span> : null}
          {mod.downloadUrl ? (
            <span className="inline-flex items-center gap-1">
              <Download className="size-3" />
              {formatBytes(sizeOfDownloadHint(mod))}
            </span>
          ) : null}
          {mod.sourceUrl ? (
            <button
              type="button"
              onClick={() => openLink(mod.sourceUrl!)}
              className="inline-flex min-w-0 items-center gap-1 truncate underline-offset-2 hover:text-foreground hover:underline"
            >
              <ExternalLink className="size-3 shrink-0" />
              <span className="truncate">{mod.sourceUrl}</span>
            </button>
          ) : null}
        </div>

        {mod.description ? (
          <p className="mt-1 line-clamp-2 text-xs text-muted-foreground/80">
            {mod.description}
          </p>
        ) : null}
      </div>

      <Button
        size="sm"
        disabled={installed || busy}
        loading={installing}
        onClick={() => onInstall(mod)}
      >
        {t("add.install")}
      </Button>
    </div>
  );
}

/* The search payload has no size field, so the download row shows the archive
 * size when the source recorded one in `source_data` and nothing when it did not. */
function sizeOfDownloadHint(mod: Mod): number {
  const raw = mod.sourceData?.size_bytes ?? mod.sourceData?.file_size;
  return typeof raw === "number" && Number.isFinite(raw) ? raw : 0;
}
