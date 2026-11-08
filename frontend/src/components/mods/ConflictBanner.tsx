import { AlertTriangle, RefreshCw } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import { ScrollArea } from "@/components/ui/scroll-area";
import {
  useDetectConflicts,
  useInstallMod,
  useResolveConflict,
  useToggleMod,
  useUpdateMod,
} from "@/hooks/mutations";
import { actionsFor, type ConflictSides, conflictSides } from "@/lib/conflicts";
import { t } from "@/lib/i18n";
import { selectConflicts, useModsStore } from "@/stores/mods.store";
import type { ConflictReport, ConflictResolution, Mod } from "@/types/domain";

function displayName(mod?: Mod): string {
  const name = (mod?.name ?? "").trim();
  return name || mod?.id || "?";
}

function labelFor(how: ConflictResolution, sides: ConflictSides): string {
  switch (how) {
    case "keep-both":
      return t("conflicts.resolve.keep-both");
    case "disable-mod-a":
      return t("conflicts.resolve.disable-mod-a", {
        name: displayName(sides.a),
      });
    case "disable-mod-b":
      return t("conflicts.resolve.disable-mod-b", {
        name: displayName(sides.b),
      });
    case "uninstall-mod-a":
      return t("conflicts.resolve.uninstall-mod-a", {
        name: displayName(sides.a),
      });
    case "uninstall-mod-b":
      return t("conflicts.resolve.uninstall-mod-b", {
        name: displayName(sides.b),
      });
    default:
      return how;
  }
}

interface ResolutionButtonsProps {
  report: ConflictReport;
}

interface ActionButtonProps {
  label: string;
  destructive?: boolean;
  pending?: boolean;
  loading?: boolean;
  onClick: () => void;
}

function ActionButton({
  label,
  destructive = false,
  pending = false,
  loading = false,
  onClick,
}: ActionButtonProps) {
  return (
    <Button
      size="sm"
      variant={destructive ? "destructive" : "secondary"}
      disabled={pending}
      loading={loading}
      onClick={onClick}
    >
      <span className="inline-block max-w-40 truncate align-middle">
        {label}
      </span>
    </Button>
  );
}

function ResolutionButtons({ report }: ResolutionButtonsProps) {
  const resolve = useResolveConflict();
  const install = useInstallMod();
  const toggle = useToggleMod();
  const update = useUpdateMod();
  const sides = conflictSides(report);

  return (
    <div className="flex flex-wrap gap-1.5">
      {actionsFor(report).map((action) => {
        switch (action.kind) {
          case "resolve":
            return (
              <ActionButton
                key={action.how}
                label={labelFor(action.how, sides)}
                destructive={action.how.startsWith("uninstall")}
                pending={resolve.isPending}
                loading={
                  resolve.isPending && resolve.variables?.how === action.how
                }
                onClick={() =>
                  resolve.mutate({ reportId: report.id, how: action.how })
                }
              />
            );
          case "download":
            return (
              <ActionButton
                key={`download-${action.modId}`}
                label={t("mods.installRequirement")}
                pending={install.isPending}
                loading={
                  install.isPending && install.variables?.modId === action.modId
                }
                onClick={() =>
                  install.mutate({
                    sourceId: action.sourceId,
                    modId: action.modId,
                  })
                }
              />
            );
          case "enable":
            return (
              <ActionButton
                key={`enable-${action.modId}`}
                label={t("conflicts.resolve.enable", {
                  name: displayName(report.modB),
                })}
                pending={toggle.isPending}
                loading={
                  toggle.isPending &&
                  toggle.variables?.id === action.modId &&
                  toggle.variables?.enable
                }
                onClick={() =>
                  toggle.mutate({ id: action.modId, enable: true })
                }
              />
            );
          case "update":
            return (
              <ActionButton
                key={`update-${action.modId}`}
                label={t("conflicts.resolve.update", {
                  name: displayName(report.modB),
                })}
                pending={update.isPending}
                loading={
                  update.isPending && update.variables?.id === action.modId
                }
                onClick={() => update.mutate({ id: action.modId })}
              />
            );
        }
      })}
    </div>
  );
}

function ConflictList() {
  const conflicts = useModsStore(selectConflicts);

  return (
    <ScrollArea className="max-h-96">
      <ul className="divide-y">
        {conflicts.map((report) => {
          const sides = conflictSides(report);

          return (
            <li key={report.id} className="flex flex-col gap-2 p-3">
              <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                <Badge
                  variant={
                    report.severity === "error" ? "destructive" : "secondary"
                  }
                >
                  {t(`conflicts.type.${report.type}`)}
                </Badge>
                <span className="text-sm font-medium">
                  {displayName(sides.a)}
                  {sides.b ? ` \u2194 ${displayName(sides.b)}` : ""}
                </span>
              </div>
              <span className="text-sm text-muted-foreground">
                {report.details}
              </span>
              {report.recommendation ? (
                <p className="text-xs text-muted-foreground">
                  {report.recommendation}
                </p>
              ) : null}
              <ResolutionButtons report={report} />
            </li>
          );
        })}
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
            size="xs"
            className="text-destructive hover:text-destructive"
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
        size="xs"
        variant="ghost"
        loading={detect.isPending}
        onClick={() => detect.mutate()}
      >
        <RefreshCw className="size-3.5" />
        {t("conflicts.rescan")}
      </Button>
    </div>
  );
}
