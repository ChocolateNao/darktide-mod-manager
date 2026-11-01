import { useState } from "react";
import { Download, Plus, Share2 } from "lucide-react";

import { EmptyState } from "@/components/common/Surfaces";
import { CreateShareDialog } from "@/components/sharing/CreateShareDialog";
import { ReceivePanel } from "@/components/sharing/ReceivePanel";
import { SendPanel } from "@/components/sharing/SendPanel";
import { SessionRow } from "@/components/sharing/SessionRow";
import { Button } from "@/components/ui/button";
import { ScrollArea } from "@/components/ui/scroll-area";
import { useShareSessionsQuery } from "@/hooks/queries";
import { t } from "@/lib/i18n";
import {
  selectActiveSession,
  selectSessions,
  selectShareFocus,
  useSharingStore,
} from "@/stores/sharing.store";

/**
 * Two panels, both mounted: the protocol requires it. The sender holds the offer,
 * so the recipient cannot simply refuse without producing an answer link, and
 * that link has to go back either way.
 */
export default function ShareView() {
  // Events keep the store current after the query, so the list is read from it.
  useShareSessionsQuery();
  const sessions = useSharingStore(selectSessions);
  const focus = useSharingStore(selectShareFocus);
  const setFocus = useSharingStore((state) => state.setFocus);
  const draftOfferLink = useSharingStore((state) => state.draftOfferLink);
  const active = useSharingStore(selectActiveSession);

  const [creating, setCreating] = useState(false);

  const senderSession = active?.role === "sender" ? active : null;
  const recipientSession = active?.role === "recipient" ? active : null;

  return (
    <div className="flex h-full min-h-0 flex-col gap-4 p-4">
      <p className="max-w-3xl text-sm text-muted-foreground">
        {t("share.bothEnds")}
      </p>

      <div className="grid min-h-0 flex-1 grid-cols-2 gap-4">
        <section
          className={`flex min-h-0 flex-col gap-3 rounded-lg border p-3 ${
            focus === "send" ? "border-game-accent" : ""
          }`}
          onFocus={() => setFocus("send")}
        >
          <header className="flex items-center justify-between gap-2">
            <h2 className="flex items-center gap-2 text-sm font-semibold">
              <Share2 className="size-4" />
              {t("share.send")}
            </h2>
            <Button
              size="sm"
              variant="outline"
              onClick={() => setCreating(true)}
            >
              <Plus className="size-3.5" />
              {t("share.create")}
            </Button>
          </header>
          <ScrollArea className="min-h-0 flex-1">
            {/* The draft link is shown before its session arrives, because the
                session only appears once the created event lands. */}
            <SendPanel
              session={senderSession}
              link={senderSession?.link ?? draftOfferLink}
            />
          </ScrollArea>
        </section>

        <section
          className={`flex min-h-0 flex-col gap-3 rounded-lg border p-3 ${
            focus === "receive" ? "border-game-accent" : ""
          }`}
          onFocus={() => setFocus("receive")}
        >
          <header className="flex items-center gap-2">
            <h2 className="flex items-center gap-2 text-sm font-semibold">
              <Download className="size-4" />
              {t("share.receive")}
            </h2>
          </header>
          <ScrollArea className="min-h-0 flex-1">
            <ReceivePanel session={recipientSession} />
          </ScrollArea>
        </section>
      </div>

      <section className="flex min-h-0 flex-col gap-2">
        <h3 className="text-sm font-semibold">{t("share.sessions")}</h3>
        <ScrollArea className="h-48">
          {sessions.length > 0 ? (
            <div className="flex flex-col gap-2 pr-3">
              {sessions.map((session) => (
                <SessionRow key={session.offerId} session={session} />
              ))}
            </div>
          ) : (
            <EmptyState title={t("share.noSessions")} />
          )}
        </ScrollArea>
      </section>

      <CreateShareDialog open={creating} onOpenChange={setCreating} />
    </div>
  );
}
