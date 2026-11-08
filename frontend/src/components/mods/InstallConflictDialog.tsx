import { useState } from "react";
import { Copy } from "lucide-react";

import * as api from "@/bridge/client";
import { copyToClipboard, describeBridgeFailure } from "@/bridge/dialogs";
import { ErrorText } from "@/components/common/ErrorText";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import {
  updateNeedsModPage,
  useImportLink,
  useInstallMod,
  useUpdateMod,
} from "@/hooks/mutations";
import { notifyFailure } from "@/lib/errorToast";
import { t } from "@/lib/i18n";
import {
  type InstallDirection,
  type PendingInstall,
  useInstallPromptStore,
} from "@/stores/installPrompt.store";

function verbFor(direction: InstallDirection): string {
  switch (direction) {
    case "update":
      return t("mods.updateAction");
    case "downgrade":
      return t("mods.downgradeAction");
    case "reinstall":
      return t("mods.reinstallAction");
  }
}

/** One dialog answers every install entry point: search, drawer dependency, drop, link, and the status tag. */
export function InstallConflictDialog() {
  const pending = useInstallPromptStore((state) => state.pending);
  const clear = useInstallPromptStore((state) => state.clear);
  const install = useInstallMod();
  const update = useUpdateMod();
  const importLink = useImportLink();
  const [failure, setFailure] = useState<{
    prompt: PendingInstall;
    error: unknown;
  } | null>(null);
  const [copied, setCopied] = useState(false);

  // A failure belongs to the prompt that produced it, so a new prompt cannot
  // inherit the previous one's error without an effect to reset it.
  const failureForPrompt =
    failure !== null && failure.prompt === pending ? failure.error : null;

  const direction = pending?.direction ?? "update";
  const busy =
    pending?.mode === "update"
      ? update.isPending
      : pending?.mode === "link"
        ? importLink.isPending
        : install.isPending;

  const openModPage = async () => {
    if (!pending) return;

    try {
      await api.openModSource(pending.modId, "source");
    } catch (error) {
      notifyFailure(t("common.error"), error);
    }
  };

  return (
    <AlertDialog
      open={pending !== null}
      onOpenChange={(next) => {
        if (!next && !busy) clear();
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>
            {t("mods.installConflictTitle", {
              name: pending?.name ?? "",
            })}
          </AlertDialogTitle>
          <AlertDialogDescription>
            {t("mods.installConflictHint", {
              current: pending?.currentVersion ?? "",
              target: pending?.targetVersion ?? "",
              action: verbFor(direction),
            })}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {failureForPrompt !== null && (
          <div className="flex min-w-0 flex-col gap-2 rounded-md border border-destructive/40 bg-destructive/10 p-3 text-sm">
            <span className="font-medium text-destructive">
              {t("mods.updateFailed")}
            </span>
            <ErrorText
              message={describeBridgeFailure(failureForPrompt)}
              className="text-muted-foreground"
            />
            <div className="flex flex-wrap gap-2">
              {updateNeedsModPage(failureForPrompt) && (
                <Button size="sm" variant="outline" onClick={openModPage}>
                  {t("mods.openPage")}
                </Button>
              )}
              <Button
                size="sm"
                variant="ghost"
                onClick={async () => {
                  setCopied(
                    await copyToClipboard(
                      describeBridgeFailure(failureForPrompt)
                    )
                  );
                }}
              >
                <Copy className="size-3.5" />
                {copied ? t("common.copied") : t("common.copy")}
              </Button>
            </div>
          </div>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>
            {t("common.cancel")}
          </AlertDialogCancel>
          <Button
            loading={busy}
            variant={direction === "downgrade" ? "destructive" : "default"}
            onClick={() => {
              if (!pending) return;

              if (pending.mode === "update") {
                update.mutate(
                  { id: pending.modId, quiet: true },
                  {
                    onSuccess: clear,
                    onError: (error) => setFailure({ prompt: pending, error }),
                  }
                );

                return;
              }

              if (pending.mode === "link" && pending.link) {
                importLink.mutate(
                  { link: pending.link, force: true },
                  { onSuccess: clear }
                );

                return;
              }

              install.mutate(
                {
                  sourceId: pending.sourceId,
                  modId: pending.modId,
                  force: true,
                },
                { onSuccess: clear }
              );
            }}
          >
            {verbFor(direction)}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
