import { useEffect, useState } from "react";
import { Hourglass, Send, X } from "lucide-react";

import { CopyButton, LongValue } from "@/components/common/CopyButton";
import { Hint } from "@/components/common/Surfaces";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import {
  useAwaitRejectedShare,
  useCancelShare,
  useSendShare,
} from "@/hooks/mutations";
import { formatDurationSeconds } from "@/lib/format";
import { t } from "@/lib/i18n";
import { isAnswerLink, secondsUntilExpiry } from "@/lib/p2p";
import { useSharingStore } from "@/stores/sharing.store";
import type { P2PShareSession } from "@/types/domain";

/**
 * A pion peer connection cannot be restored from a stored offer, so this panel
 * is a wait: it holds the link, counts down to the expiry the session carries,
 * and takes the answer link back. A silent spinner would look like a stall.
 */
interface SendPanelProps {
  session: P2PShareSession | null;
  link: string | null;
}

export function SendPanel({ session, link }: SendPanelProps) {
  const send = useSendShare();
  const cancel = useCancelShare();
  const awaitRejection = useAwaitRejectedShare();

  const [answer, setAnswer] = useState("");
  const [answerError, setAnswerError] = useState<string | null>(null);

  const setDraftAnswerLink = useSharingStore(
    (state) => state.setDraftAnswerLink
  );
  const setDraftOfferLink = useSharingStore((state) => state.setDraftOfferLink);
  const setFocus = useSharingStore((state) => state.setFocus);

  const offerId = session?.offerId ?? null;

  // Derived from the session's own deadline, so a force-render suffices.
  const [, forceTick] = useState(0);
  useEffect(() => {
    const timer = window.setInterval(() => forceTick((tick) => tick + 1), 1000);
    return () => window.clearInterval(timer);
  }, []);

  // A refusal arrives asynchronously, so the sender blocks on it; the session
  // state it leaves behind is what the session row renders.
  useEffect(() => {
    if (!offerId) return;
    void awaitRejection.mutateAsync(offerId).catch(() => undefined);
    // Only the offer identity restarts the wait — the mutation object changes
    // identity on every render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [offerId]);

  if (!link) {
    return <Hint>{t("share.nothingToSend")}</Hint>;
  }

  const remaining = session ? secondsUntilExpiry(session) : null;

  const submit = () => {
    const trimmed = answer.trim();
    // A wrong link is a paste mistake and is caught here before any bridge call.
    if (!offerId || !isAnswerLink(trimmed)) {
      setAnswerError(t("share.answerInvalid"));
      return;
    }

    setAnswerError(null);
    setDraftAnswerLink(trimmed);
    send.mutate({ offerId, answerLink: trimmed });
  };

  const abandon = () => {
    if (offerId) cancel.mutate(offerId);
    setDraftOfferLink(null);
    setDraftAnswerLink(null);
    setFocus("send");
  };

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center justify-between gap-2">
        <Badge variant="outline">{t("share.awaitingAnswer")}</Badge>
        {remaining !== null ? (
          <Badge variant="secondary">
            <Hourglass className="size-3" />
            {t("share.expiresIn", { time: formatDurationSeconds(remaining) })}
          </Badge>
        ) : null}
      </div>

      <div className="flex flex-col gap-1.5">
        <LongValue text={link} />
        <CopyButton
          text={link}
          label={t("share.copyOffer")}
          variant="default"
          className="self-start"
        />
      </div>

      <Textarea
        value={answer}
        onChange={(event) => setAnswer(event.target.value)}
        placeholder={t("share.answerPlaceholder")}
        className="min-h-20 font-mono text-xs"
        aria-invalid={answerError !== null}
        aria-label={t("share.answerPlaceholder")}
      />
      {answerError ? (
        <p className="text-xs text-destructive">{answerError}</p>
      ) : null}

      <div className="flex items-center gap-2">
        <Button
          onClick={submit}
          disabled={answer.trim() === ""}
          loading={send.isPending}
        >
          <Send className="size-4" />
          {t("share.sendFiles")}
        </Button>
        <Button variant="ghost" onClick={abandon} loading={cancel.isPending}>
          <X className="size-4" />
          {t("share.cancel")}
        </Button>
      </div>

      <Hint>{t("share.bothEndsHint")}</Hint>
    </div>
  );
}
