import { Trash2 } from "lucide-react";

import { EmptyState } from "@/components/common/Surfaces";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { useClearLogs } from "@/hooks/mutations";
import { formatBytes, formatDateTime } from "@/lib/format";
import { t } from "@/lib/i18n";
import type { LogFile } from "@/types/domain";

interface LogFilesPanelProps {
  files: LogFile[];
}

export function LogFilesPanel({ files }: LogFilesPanelProps) {
  const clear = useClearLogs();

  return (
    <div className="flex h-full flex-col gap-2 p-3">
      <div className="flex items-center justify-between gap-2">
        <span className="text-sm font-medium">{t("logs.files")}</span>
        <div className="flex items-center gap-1">
          <Button
            size="sm"
            variant="outline"
            onClick={() => clear.mutate(false)}
            loading={clear.isPending}
          >
            <Trash2 className="size-3.5" />
            {t("logs.clearCurrent")}
          </Button>
          {/* Deleting every rotated file is irreversible, so it asks first;
              clearing the live file is not, so it does not. */}
          <AlertDialog>
            <AlertDialogTrigger asChild>
              <Button size="sm" variant="ghost" disabled={clear.isPending}>
                {t("logs.clearAll")}
              </Button>
            </AlertDialogTrigger>
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>{t("logs.clearAll")}</AlertDialogTitle>
                <AlertDialogDescription>
                  {t("logs.clearConfirm")}
                </AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
                <AlertDialogAction onClick={() => clear.mutate(true)}>
                  {t("common.confirm")}
                </AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          </AlertDialog>
        </div>
      </div>

      {files.length === 0 ? (
        <EmptyState title={t("logs.noFiles")} />
      ) : (
        <ul className="flex flex-col gap-1 text-xs" data-selectable>
          {files.map((file) => (
            <li
              key={file.path}
              className="flex items-center gap-2 rounded-md border px-2 py-1.5"
            >
              <span className="min-w-0 flex-1 truncate">{file.name}</span>
              {file.rotated ? (
                <Badge variant="outline">{t("logs.rotated")}</Badge>
              ) : null}
              {file.compressed ? (
                <Badge variant="outline">{t("logs.compressed")}</Badge>
              ) : null}
              <span className="w-20 shrink-0 text-right text-muted-foreground">
                {formatBytes(file.size)}
              </span>
              <span className="w-40 shrink-0 text-right text-muted-foreground">
                {formatDateTime(file.modTime)}
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
