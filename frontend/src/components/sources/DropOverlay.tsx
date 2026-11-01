import { useEffect, useRef, useState } from "react";
import { FolderDown, Loader2, PackageOpen } from "lucide-react";

import * as api from "@/bridge/client";
import {
  canResolveDroppedPaths,
  type DroppedPaths,
  onFileDrop,
} from "@/bridge/dialogs";
import { ErrorNote } from "@/components/common/Surfaces";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { useInstallMod } from "@/hooks/mutations";
import { t } from "@/lib/i18n";
import type { DroppedMod } from "@/types/domain";

/**
 * Window-level drop target. A drag is an ambiguous gesture, so the
 * release resolves the path through the local
 * source first and names the mod and version for confirmation.
 */
export function DropOverlay() {
  const [open, setOpen] = useState(false);
  const [path, setPath] = useState<string | null>(null);
  const [resolved, setResolved] = useState<DroppedMod | null>(null);
  const [rejected, setRejected] = useState<string | null>(null);
  const [checking, setChecking] = useState(false);
  const [dragging, setDragging] = useState(false);
  const dragDepth = useRef(0);
  // Read once as a lazy initialiser: the answer is a property of the webview
  // build and cannot change while the window is open.
  const [canResolve] = useState(canResolveDroppedPaths);
  const install = useInstallMod();

  useEffect(() => {
    // WebKitGTK reports a folder drag as text/uri-list rather than Files. Any
    // data transfer is treated as a file drop so the webview cannot navigate
    // to the dropped folder instead of letting Wails resolve it.
    const hasDropData = (event: DragEvent) => {
      const transfer = event.dataTransfer;
      if (!transfer) return false;
      const types = Array.from(transfer.types);
      return (
        types.includes("Files") ||
        types.includes("text/uri-list") ||
        transfer.files.length > 0
      );
    };

    const enter = (event: DragEvent) => {
      // Always cancel the webview default; a file-manager drag must never
      // replace the app with a directory listing.
      event.preventDefault();
      if (!hasDropData(event)) return;

      dragDepth.current += 1;
      setDragging(true);
    };

    const over = (event: DragEvent) => {
      event.preventDefault();
      if (!hasDropData(event)) return;

      if (event.dataTransfer) event.dataTransfer.dropEffect = "copy";
    };

    const leave = (event: DragEvent) => {
      if (!hasDropData(event) && dragDepth.current === 0) return;

      dragDepth.current = Math.max(0, dragDepth.current - 1);
      if (dragDepth.current === 0) setDragging(false);
    };

    const drop = (event: DragEvent) => {
      event.preventDefault();
      dragDepth.current = 0;
      setDragging(false);
    };

    // Capture phase: the Wails drop handler may stop propagation after the
    // native drop, and the overlay still has to disappear.
    window.addEventListener("dragenter", enter, true);
    window.addEventListener("dragover", over, true);
    window.addEventListener("dragleave", leave, true);
    window.addEventListener("dragend", drop, true);
    window.addEventListener("drop", drop, true);

    return () => {
      window.removeEventListener("dragenter", enter, true);
      window.removeEventListener("dragover", over, true);
      window.removeEventListener("dragleave", leave, true);
      window.removeEventListener("dragend", drop, true);
      window.removeEventListener("drop", drop, true);
    };
  }, []);

  useEffect(() => {
    return onFileDrop(async (dropped: DroppedPaths) => {
      dragDepth.current = 0;
      setDragging(false);

      const [first] = dropped.paths;
      if (!first) return;

      setPath(first);
      setResolved(null);
      setRejected(null);
      setChecking(true);
      setOpen(true);

      try {
        const outcome = await api.inspectDroppedPath(first);
        if (outcome.recognised && outcome.mod) {
          setResolved(outcome.mod);
        } else {
          setRejected(first);
        }
      } catch {
        // A backend failure here is not worth a second dialog: the path stays on
        // screen so the user can see what failed and drop it again if they like.
        setRejected(first);
      } finally {
        setChecking(false);
      }
    });
  }, []);

  const close = () => {
    setOpen(false);
    setPath(null);
    setResolved(null);
    setRejected(null);
  };

  return (
    <>
      {dragging ? (
        <div className="pointer-events-none absolute inset-0 z-30 flex items-center justify-center bg-background/80 p-6 backdrop-blur-sm">
          <div className="flex size-full flex-col items-center justify-center gap-3 rounded-xl border-2 border-dashed border-(--game-accent) bg-background/60">
            <FolderDown className="size-10 text-(--game-accent)" />
            <p className="text-base font-medium">{t("drop.dragging")}</p>
            <p className="text-sm text-muted-foreground">
              {t("drop.draggingHint")}
            </p>
          </div>
        </div>
      ) : null}

      <Dialog
        open={open}
        onOpenChange={(next) => {
          if (!next) close();
        }}
      >
        <DialogContent className="sm:max-w-lg" showCloseButton={false}>
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <FolderDown className="size-4 text-(--game-accent)" />
              {t("drop.title")}
            </DialogTitle>
            <DialogDescription>
              {canResolve ? t("drop.hint") : t("drop.unsupported")}
            </DialogDescription>
          </DialogHeader>

          {checking ? (
            <p className="flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="size-4 animate-spin" />
              {t("drop.inspecting")}
            </p>
          ) : resolved ? (
            <div className="rounded-md border p-3">
              <div className="flex items-baseline gap-2">
                <PackageOpen className="size-4 text-muted-foreground" />
                <span className="text-sm font-medium">{resolved.name}</span>
                {resolved.version ? (
                  <span className="text-xs text-muted-foreground">
                    v{resolved.version}
                  </span>
                ) : null}
              </div>
              <p className="mt-1 text-xs break-all text-muted-foreground">
                {resolved.path}
              </p>
            </div>
          ) : rejected ? (
            <ErrorNote
              error={new Error(t("drop.unrecognised", { path: rejected }))}
            />
          ) : null}

          <DialogFooter>
            <Button variant="ghost" onClick={close}>
              {t("common.cancel")}
            </Button>
            <Button
              disabled={!resolved}
              loading={install.isPending}
              onClick={() => {
                if (!resolved) return;
                install.mutate(
                  { sourceId: resolved.sourceId, modId: resolved.modId },
                  { onSuccess: close }
                );
              }}
            >
              {t("drop.install")}
            </Button>
          </DialogFooter>
          {path && checking === false && !resolved ? (
            <p className="text-[11px] text-muted-foreground/70">
              {t("drop.unrecognisedHint")}
            </p>
          ) : null}
        </DialogContent>
      </Dialog>
    </>
  );
}
