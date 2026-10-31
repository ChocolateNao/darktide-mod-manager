/**
 * Peer-to-peer share sessions and the half of the conversation the user is
 * holding now. Draft links live here so closing the view cannot cancel a
 * session, and they are never persisted — a peer connection cannot be restored
 * from a stored offer across a restart.
 */

import { create } from "zustand";

import type { P2PSharePreview, P2PShareSession } from "@/types/domain";

export type ShareRole = "send" | "receive";

export interface SharingState {
  sessions: P2PShareSession[];
  focus: ShareRole;
  draftOfferLink: string | null;
  draftAnswerLink: string | null;
  preview: P2PSharePreview | null;
  setSessions: (sessions: P2PShareSession[]) => void;
  upsertSession: (session: P2PShareSession) => void;
  removeSession: (offerId: string) => void;
  setFocus: (focus: ShareRole) => void;
  setDraftOfferLink: (link: string | null) => void;
  setDraftAnswerLink: (link: string | null) => void;
  setPreview: (preview: P2PSharePreview | null) => void;
  reset: () => void;
}

/** Newest first, so a session that just changed state is the row in view. */
function byRecency(left: P2PShareSession, right: P2PShareSession): number {
  const leftAt = left.updatedAt?.getTime() ?? left.createdAt?.getTime() ?? 0;
  const rightAt = right.updatedAt?.getTime() ?? right.createdAt?.getTime() ?? 0;
  return rightAt - leftAt;
}

export const useSharingStore = create<SharingState>()((set) => ({
  sessions: [],
  focus: "send",
  draftOfferLink: null,
  draftAnswerLink: null,
  preview: null,

  setSessions: (sessions) => set({ sessions: [...sessions].sort(byRecency) }),

  upsertSession: (session) =>
    set((state) => {
      const rest = state.sessions.filter(
        (entry) => entry.offerId !== session.offerId
      );
      return { sessions: [session, ...rest].sort(byRecency) };
    }),

  removeSession: (offerId) =>
    set((state) => ({
      sessions: state.sessions.filter((entry) => entry.offerId !== offerId),
    })),

  setFocus: (focus) => set({ focus }),
  setDraftOfferLink: (draftOfferLink) => set({ draftOfferLink }),
  setDraftAnswerLink: (draftAnswerLink) => set({ draftAnswerLink }),
  setPreview: (preview) => set({ preview }),

  reset: () =>
    set({ draftOfferLink: null, draftAnswerLink: null, preview: null }),
}));

export const selectSessions = (state: SharingState) => state.sessions;
export const selectPreview = (state: SharingState) => state.preview;
export const selectShareFocus = (state: SharingState) => state.focus;

export function selectSession(
  state: SharingState,
  offerId: string | null
): P2PShareSession | null {
  if (!offerId) return null;
  return state.sessions.find((session) => session.offerId === offerId) ?? null;
}

export function selectActiveSession(
  state: SharingState
): P2PShareSession | null {
  return state.sessions[0] ?? null;
}
