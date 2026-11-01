import { useState } from "react";
import { Search, Undo2 } from "lucide-react";

import { PreviewPanel } from "./PreviewPanel";

import { CopyButton, LongValue } from "@/components/common/CopyButton";
import { ErrorNote, Hint } from "@/components/common/Surfaces";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import {
  useAcceptShare,
  useAwaitShare,
  useInstallShare,
  usePreviewShare,
  useRejectShare,
} from "@/hooks/mutations";
import { t } from "@/lib/i18n";
import { normaliseShareLink } from "@/lib/p2p";
import { useSharingStore } from "@/stores/sharing.store";
import type { P2PShareSession } from "@/types/domain";

/**
 * The recipient half of the conversation. Always mounted: the answer link it
 * produces has to go back to the sender whether the user accepted or declined.
 */
interface ReceivePanelProps {
  session: P2PShareSession | null;
}

export function ReceivePanel({ session }: ReceivePanelProps) {
  const preview = useSharingStore((state) => state.preview);
  const setPreview = useSharingStore((state) => state.setPreview);
  const setFocus = useSharingStore((state) => state.setFocus);

  const readOffer = usePreviewShare();
  const accept = useAcceptShare();
  const decline = useRejectShare();
  const awaitTransfer = useAwaitShare();
  const install = useInstallShare();

  const [link, setLink] = useState("");
  const [linkError, setLinkError] = useState<string | null>(null);
  const [receiverName, setReceiverName] = useState("");
  const [answerLink, setAnswerLink] = useState<string | null>(null);
  const [awaiting, setAwaiting] = useState(false);

  // The answer link lives as long as its preview; leaving the view must not cancel a transfer.
  const revert = () => {
    setAnswerLink(null);
    setPreview(null);
  };

  const inspect = async () => {
    const normalised = normaliseShareLink(link);
    if (!normalised) {
      setLinkError(t("share.linkInvalid"));
      return;
    }

    setLinkError(null);
    setFocus("receive");

    try {
      const next = await readOffer.mutateAsync(normalised);
      // A different link means the previous answer no longer applies to it.
      setAnswerLink(null);
      setPreview(next);
    } catch {
      // Already reported by the mutation; the input keeps its text so a partial
      // paste can be corrected in place.
    }
  };

  const startReceiving = async (offerId: string) => {
    setAwaiting(true);
    // Blocks for the length of the transfer. Progress arrives on the share
    // events, so the panel is not frozen while it runs.
    await awaitTransfer.mutateAsync(offerId).catch(() => undefined);
    setAwaiting(false);
  };

  const acceptShare = async () => {
    if (!preview) return;

    const answer = await accept.mutateAsync({
      link: normaliseShareLink(link) ?? link,
      receiverName,
    });
    setAnswerLink(answer.link);
    await startReceiving(preview.offerId);
  };

  const rejectShare = async (reason: string) => {
    if (!preview) return;

    // A refusal still produces an answer link, and it still has to travel back.
    const answer = await decline.mutateAsync({
      link: normaliseShareLink(link) ?? link,
      reason,
    });
    setAnswerLink(answer.link);
  };

  const busy = accept.isPending || decline.isPending;
  const settled = answerLink !== null;

  return (
    <div className="flex flex-col gap-3">
      {answerLink ? (
        <div className="flex flex-col gap-1.5">
          <LongValue text={answerLink} />
          <CopyButton
            text={answerLink}
            label={t("share.copyAnswer")}
            variant="default"
            className="self-start"
          />
        </div>
      ) : null}

      {!settled && !preview ? (
        <>
          <Textarea
            value={link}
            onChange={(event) => setLink(event.target.value)}
            placeholder={t("share.pasteOffer")}
            className="min-h-24 font-mono text-xs"
            aria-invalid={linkError !== null}
            aria-label={t("share.pasteOffer")}
          />
          {linkError ? (
            <p className="text-xs text-destructive">{linkError}</p>
          ) : null}
          <Button
            onClick={inspect}
            disabled={link.trim() === ""}
            loading={readOffer.isPending}
          >
            <Search className="size-4" />
            {t("share.previewOffer")}
          </Button>
          {readOffer.isError ? <ErrorNote error={readOffer.error} /> : null}
          <Hint>{t("share.linkHint")}</Hint>
        </>
      ) : null}

      {!settled && preview ? (
        <>
          <PreviewPanel
            preview={preview}
            receiverName={receiverName}
            onReceiverNameChange={setReceiverName}
            onAccept={() => void acceptShare()}
            onReject={(reason) => void rejectShare(reason)}
            busy={busy}
            accepting={accept.isPending}
            error={
              accept.isError
                ? accept.error
                : decline.isError
                  ? decline.error
                  : null
            }
          />
          <Button
            variant="ghost"
            size="sm"
            className="self-start"
            onClick={revert}
            disabled={busy}
          >
            <Undo2 className="size-4" />
            {t("common.revert")}
          </Button>
        </>
      ) : null}

      {awaiting ? <Hint>{t("share.bothEndsHint")}</Hint> : null}
      {awaitTransfer.isError ? <ErrorNote error={awaitTransfer.error} /> : null}

      {settled && session?.state === "completed" ? (
        <Button
          onClick={() =>
            install.mutate({ offerId: session.offerId, enable: true })
          }
          loading={install.isPending}
        >
          {t("share.installAndEnable")}
        </Button>
      ) : null}
    </div>
  );
}
