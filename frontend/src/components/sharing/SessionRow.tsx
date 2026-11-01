import { Ban, Check, Download, Loader2 } from "lucide-react";

import { CopyButton } from "@/components/common/CopyButton";
import { Button } from "@/components/ui/button";
import { Progress } from "@/components/ui/progress";
import {
  useCancelShare,
  useDiscardShare,
  useInstallShare,
} from "@/hooks/mutations";
import { formatBytes, formatRelativeTime } from "@/lib/format";
import { t } from "@/lib/i18n";
import { isTerminalSession, shareProgress, shareStateMeta } from "@/lib/p2p";
import type { P2PShareSession } from "@/types/domain";

const TONE_TEXT = {
  neutral: "text-muted-foreground",
  active: "text-info",
  success: "text-success",
  warning: "text-warning",
  danger: "text-destructive",
} as const;

interface StateBadgeProps {
  session: P2PShareSession;
}

function StateBadge({ session }: StateBadgeProps) {
  const meta = shareStateMeta(session.state);

  return (
    <span
      className={`inline-flex items-center gap-1.5 text-xs ${TONE_TEXT[meta.tone]}`}
    >
      {meta.live ? <Loader2 className="size-3 animate-spin" /> : null}
      {t(`share.state.${session.state}`)}
    </span>
  );
}

interface SessionRowProps {
  session: P2PShareSession;
}

export function SessionRow({ session }: SessionRowProps) {
  const cancel = useCancelShare();
  const discard = useDiscardShare();
  const install = useInstallShare();

  const terminal = isTerminalSession(session);
  const progress = shareProgress(session);
  const modNames =
    session.preview?.mods.map((mod) => mod.name) ?? session.modIds;
  const label = modNames.length > 0 ? modNames.join(", ") : session.offerId;
  // A refusal carries its reason verbatim because the recipient chose it.
  const reason =
    session.state === "failed" || session.state === "rejected"
      ? session.failReason
      : null;

  return (
    <div className="rounded-md border px-3 py-2" data-selectable>
      <div className="flex items-center justify-between gap-3">
        <div className="min-w-0">
          <p className="truncate text-sm font-medium">{label}</p>
          <p className="mt-0.5 flex items-center gap-2 text-xs text-muted-foreground">
            <StateBadge session={session} />
            <span>
              {formatBytes(session.transferredBytes)} /
              {formatBytes(session.totalSize)}
            </span>
            <span>
              {session.role === "sender"
                ? t("share.role.sender")
                : t("share.role.recipient")}
            </span>
            {session.createdAt ? (
              <span>{formatRelativeTime(session.createdAt)}</span>
            ) : null}
          </p>
        </div>

        <div className="flex shrink-0 items-center gap-1">
          {session.state === "completed" && session.role !== "sender" ? (
            <Button
              size="sm"
              onClick={() =>
                install.mutate({ offerId: session.offerId, enable: true })
              }
              loading={install.isPending}
            >
              <Check className="size-3.5" />
              {t("share.installAndEnable")}
            </Button>
          ) : null}

          {session.state === "completed" && session.role === "sender" ? (
            <span className="inline-flex items-center gap-1 text-xs text-success">
              <Check className="size-3" />
              {t("share.done")}
            </span>
          ) : null}

          {/* Kept on the row so an abandoned share stays one click to copy. */}
          {session.link ? (
            <CopyButton
              text={session.link}
              label={t("share.copyOffer")}
              size="icon"
              variant="ghost"
            />
          ) : null}

          {!terminal ? (
            <Button
              size="sm"
              variant="ghost"
              onClick={() => cancel.mutate(session.offerId)}
              loading={cancel.isPending}
            >
              <Ban className="size-3.5" />
              {t("share.cancel")}
            </Button>
          ) : null}

          {terminal && session.role !== "sender" ? (
            <Button
              size="sm"
              variant="ghost"
              onClick={() => discard.mutate(session.offerId)}
              loading={discard.isPending}
            >
              <Download className="size-3.5" />
              {t("share.discard")}
            </Button>
          ) : null}
        </div>
      </div>

      {!terminal ? (
        <Progress
          value={progress * 100}
          className="mt-2 h-1"
          aria-label={t(`share.state.${session.state}`)}
        />
      ) : null}

      {reason ? (
        <p className="mt-1.5 text-xs text-muted-foreground">{reason}</p>
      ) : null}
    </div>
  );
}
