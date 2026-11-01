/**
 * Pure helpers for the peer-to-peer share view.
 *
 * The Go session record exposes `Progress()` and `IsExpired()` as *methods*, and
 * a method does not cross the Wails bridge. Recomputing here means the progress
 * bar updates on every `share.progress` event with no round trip.
 */

import { P2P_ANSWER_PREFIX, P2P_OFFER_PREFIX } from "@/lib/schemas/share";
import type { P2PShareSession, P2PShareState } from "@/types/domain";

/** `Terminal()` in Go: the share is over and nothing further will happen to it. */
const TERMINAL_STATES: ReadonlySet<P2PShareState> = new Set([
  "completed",
  "rejected",
  "expired",
  "failed",
]);

export function isTerminalState(state: P2PShareState): boolean {
  return TERMINAL_STATES.has(state);
}

export function isTerminalSession(session: P2PShareSession): boolean {
  return isTerminalState(session.state);
}

/**
 * Fraction of a transfer that has completed. A share with no known size counts
 * as complete, because the alternative is a bar that sits at zero forever on a
 * transfer that is actually finished.
 */
export function shareProgress(
  session: Pick<P2PShareSession, "transferredBytes" | "totalSize">
): number {
  if (session.totalSize <= 0) return 1;
  return Math.min(Math.max(session.transferredBytes / session.totalSize, 0), 1);
}

/** `P2PShareLink.IsExpired()` in Go, computed against the session's own deadline. */
export function isShareExpired(
  session: Pick<P2PShareSession, "expiresAt">,
  now = new Date()
): boolean {
  if (!session.expiresAt) return false;
  return session.expiresAt.getTime() <= now.getTime();
}

/** Seconds until a share's link expires, or `null` when it never does. */
export function secondsUntilExpiry(
  session: Pick<P2PShareSession, "expiresAt">,
  now = new Date()
): number | null {
  if (!session.expiresAt) return null;
  return Math.max(
    0,
    Math.round((session.expiresAt.getTime() - now.getTime()) / 1000)
  );
}

/**
 * Normalises a pasted share link. Whitespace, a missing scheme and the CLI's
 * slashless scheme are the three mistakes a paste actually produces; all three
 * are recoverable here because the recipient reads out of a chat message, and a
 * leading space should not cost them the transfer.
 */
export function normaliseShareLink(
  raw: string,
  prefix = P2P_OFFER_PREFIX
): string | null {
  const trimmed = raw.trim().replace(/\s+/g, "");
  if (trimmed === "") return null;

  if (trimmed.toLowerCase().startsWith("dmm:")) {
    // `dmm share p2p` prints `dmm:p2p/…` slashless and a paste keeps the
    // slashes; both name the same offer, so the scheme is canonicalised.
    const canonical = trimmed.replace(/^dmm:\/*/i, "dmm://");
    return canonical.startsWith(prefix) ? canonical : null;
  }

  // Any other scheme means the paste was a different kind of link entirely;
  // wrapping it would hand the backend an offer id that cannot exist.
  if (/^[a-z][a-z0-9+.-]*:/i.test(trimmed)) return null;

  return `${prefix}${trimmed}`;
}

export function isOfferLink(raw: string): boolean {
  return normaliseShareLink(raw, P2P_OFFER_PREFIX) !== null;
}

export function isAnswerLink(raw: string): boolean {
  return normaliseShareLink(raw, P2P_ANSWER_PREFIX) !== null;
}

export type ShareTone = "neutral" | "active" | "success" | "warning" | "danger";

export interface ShareStateMeta {
  label: string;
  tone: ShareTone;
  /** True while the two ends are still talking; the view hides Cancel below it. */
  live: boolean;
}

const STATE_META: Record<P2PShareState, ShareStateMeta> = {
  created: { label: "Waiting for the other end", tone: "neutral", live: true },
  offered: { label: "Offer is being read", tone: "neutral", live: true },
  connecting: { label: "Connecting", tone: "active", live: true },
  transferring: { label: "Transferring", tone: "active", live: true },
  verifying: { label: "Verifying hashes", tone: "active", live: true },
  completed: { label: "Completed", tone: "success", live: false },
  // A refusal is an informed decision — the recipient saw the file list and
  // declined; the source links are the way forward.
  rejected: {
    label: "Declined — use the source links",
    tone: "warning",
    live: false,
  },
  expired: { label: "Expired", tone: "neutral", live: false },
  failed: { label: "Failed", tone: "danger", live: false },
};

export function shareStateMeta(state: P2PShareState): ShareStateMeta {
  return STATE_META[state];
}

/** Sums a preview's file sizes, which is what the recipient compares against. */
export function previewTotalSize(
  files: ReadonlyArray<{ size: number }>
): number {
  return files.reduce((total, file) => total + Math.max(0, file.size), 0);
}
