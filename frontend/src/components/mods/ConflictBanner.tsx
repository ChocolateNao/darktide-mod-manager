import { AlertTriangle, RefreshCw } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import { ScrollArea } from "@/components/ui/scroll-area";
import { useDetectConflicts, useResolveConflict } from "@/hooks/mutations";
import { t } from "@/lib/i18n";
import { selectConflicts, useModsStore } from "@/stores/mods.store";
import type { ConflictReport, ConflictResolution } from "@/types/domain";

/** `api.ParseConflictResolution` refuses anything outside this set. */
const RESOLUTIONS: readonly ConflictResolution[] = [
  "keep-both",
  "disable-mod-a",
  "disable-mod-b",
  "uninstall-mod-a",
  "uninstall-mod-b",
];

interface ResolutionButtonsProps {
  report: ConflictReport;
}

function ResolutionButtons({ report }: ResolutionButtonsProps) {
  const resolve = useResolveConflict();

  return (
    <div className="flex flex-wrap gap-1.5">
      {RESOLUTIONS.map((how) => (
        <Button
          key={how}
          size="sm"
          variant={how.startsWith("uninstall") ? "destructive" : "secondary"}
          disabled={resolve.isPending}
          loading={resolve.isPending && resolve.variables?.how === how}
          onClick={() => resolve.mutate({ reportId: report.id, how })}
        >
          {t(`conflicts.resolve.${how}`)}
        </Button>
      ))}
    </div>
  );
}

function ConflictList() {
  const conflicts = useModsStore(selectConflicts);

  return (
    <ScrollArea className="max-h-96">
      <ul className="divide-y">
        {conflicts.map((report) => (
          <li key={report.id} className="flex flex-col gap-2 p-3">
            <div className="flex items-start gap-2">
              <Badge
                variant={
                  report.severity === "error" ? "destructive" : "secondary"
                }
              >
                {t(`conflicts.type.${report.type}`)}
              </Badge>
              <span className="flex-1 text-sm">{report.details}</span>
            </div>
            {report.recommendation ? (
              <p className="text-xs text-muted-foreground">
                {report.recommendation}
              </p>
            ) : null}
            <ResolutionButtons report={report} />
          </li>
        ))}
      </ul>
    </ScrollArea>
  );
}

/**
 * Conflicts are a banner: the report only exists in the context of the mod list,
 * and a page would be a sixth destination for the tab row to explain. The
 * popover keeps resolution one click from the row it affects.
 */
export function ConflictBanner() {
  const conflicts = useModsStore(selectConflicts);
  const detect = useDetectConflicts();

  if (conflicts.length === 0) {
    return null;
  }

  const errors = conflicts.filter(
    (report) => report.severity === "error"
  ).length;

  return (
    <div className="flex items-center gap-2 border-b bg-destructive/10 px-4 py-1.5">
      <Popover>
        <PopoverTrigger asChild>
          <Button
            variant="ghost"
            size="sm"
            className="h-7 text-destructive hover:text-destructive"
          >
            <AlertTriangle className="size-4" />
            {t("conflicts.summary", { count: conflicts.length, errors })}
          </Button>
        </PopoverTrigger>
        <PopoverContent align="start" className="w-lg p-0">
          <div className="flex items-center gap-2 border-b px-3 py-2">
            <AlertTriangle className="size-4 text-destructive" />
            <span className="text-sm font-medium">{t("conflicts.title")}</span>
          </div>
          <ConflictList />
        </PopoverContent>
      </Popover>

      <Button
        size="sm"
        variant="ghost"
        className="h-7"
        loading={detect.isPending}
        onClick={() => detect.mutate()}
      >
        <RefreshCw className="size-3.5" />
        {t("conflicts.rescan")}
      </Button>
    </div>
  );
}
