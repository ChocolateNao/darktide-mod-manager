import { useState } from "react";
import { Ban, Check, FileWarning } from "lucide-react";

import { SourceFallbackButtons } from "./SourceFallbackButtons";

import { ErrorNote, Hint } from "@/components/common/Surfaces";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Textarea } from "@/components/ui/textarea";
import { formatBytes } from "@/lib/format";
import { t } from "@/lib/i18n";
import { previewTotalSize } from "@/lib/p2p";
import type { P2PSharePreview } from "@/types/domain";

/**
 * The recipient's informed decision: file names, sizes and hashes are published
 * up front, so "18 MiB over the wire" and "take these from Nexus instead" can be
 * compared side by side before a single byte moves.
 */
interface PreviewPanelProps {
  preview: P2PSharePreview;
  receiverName: string;
  onReceiverNameChange: (name: string) => void;
  onAccept: () => void;
  onReject: (reason: string) => void;
  busy: boolean;
  /** Which of the two buttons to spin. `busy` alone cannot say. */
  accepting: boolean;
  error: unknown;
}

export function PreviewPanel({
  preview,
  receiverName,
  onReceiverNameChange,
  onAccept,
  onReject,
  busy,
  accepting,
  error,
}: PreviewPanelProps) {
  const [reason, setReason] = useState("");

  return (
    <div className="flex min-h-0 flex-col gap-3">
      {error ? <ErrorNote error={error} /> : null}

      {preview.expired ? (
        <div className="flex items-center gap-2 rounded-md border border-destructive/50 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          <FileWarning className="size-4" />
          {t("share.expired")}
        </div>
      ) : null}

      <div className="flex items-center gap-2 text-xs text-muted-foreground">
        {preview.sender ? (
          <span>{t("share.from", { sender: preview.sender })}</span>
        ) : null}
        <span>
          {t("share.totalSize", {
            size: formatBytes(previewTotalSize(preview.files)),
          })}
        </span>
        <span>{t("share.fileCount", { count: preview.files.length })}</span>
      </div>

      <ScrollArea className="max-h-48 rounded-md border">
        <div data-selectable>
          {preview.mods.map((mod) => (
            <div key={mod.name} className="border-b px-3 py-2 last:border-b-0">
              <p className="truncate text-sm font-medium">{mod.name}</p>
              <p className="text-xs text-muted-foreground">
                {mod.version}
                {mod.author ? ` · ${mod.author}` : ""}
              </p>
              {mod.dependencies.length > 0 ? (
                <p className="mt-1 text-xs text-warning">
                  {t("share.deps")}:{mod.dependencies.join(", ")}
                </p>
              ) : null}
            </div>
          ))}
        </div>
      </ScrollArea>

      {preview.files.length > 0 ? (
        <details className="rounded-md border px-3 py-2">
          <summary className="cursor-pointer text-sm">
            {t("share.files")}
          </summary>
          <ul
            className="mt-2 flex flex-col gap-1 text-xs text-muted-foreground"
            data-selectable
          >
            {preview.files.map((file) => (
              <li key={file.fileId} className="flex justify-between gap-3">
                <span className="truncate">{file.name}</span>
                <span className="shrink-0">{formatBytes(file.size)}</span>
              </li>
            ))}
          </ul>
        </details>
      ) : null}

      {/* A `SharedMod` carries no mod id, so the two lists cannot be joined. */}
      {preview.sourceLinks.length > 0 ? (
        <div className="flex flex-col gap-1.5 rounded-md border px-3 py-2">
          <p className="text-xs font-medium">{t("share.alternatives")}</p>
          {preview.sourceLinks.map((link) => (
            <SourceFallbackButtons
              key={link.modId}
              link={link}
              modName={link.modId}
            />
          ))}
        </div>
      ) : null}

      <div className="flex flex-col gap-2">
        <Input
          value={receiverName}
          onChange={(event) => onReceiverNameChange(event.target.value)}
          placeholder={t("share.yourName")}
          aria-label={t("share.yourName")}
        />
        <div className="flex items-center gap-2">
          <Button
            onClick={onAccept}
            loading={accepting}
            disabled={busy || preview.expired}
          >
            <Check className="size-4" />
            {t("share.accept")}
          </Button>
          <Button
            variant="outline"
            onClick={() => onReject(reason)}
            loading={busy && !accepting}
            disabled={busy}
          >
            <Ban className="size-4" />
            {t("share.decline")}
          </Button>
        </div>
        <Textarea
          value={reason}
          onChange={(event) => setReason(event.target.value)}
          placeholder={t("share.declineReason")}
          className="min-h-14"
          aria-label={t("share.declineReason")}
        />
        <Hint>{t("share.answerHint")}</Hint>
      </div>
    </div>
  );
}
