import { describe, expect, it } from "vitest";

import {
  isAnswerLink,
  isOfferLink,
  isShareExpired,
  isTerminalState,
  normaliseShareLink,
  previewTotalSize,
  secondsUntilExpiry,
  shareProgress,
} from "@/lib/p2p";
import { P2P_ANSWER_PREFIX, P2P_OFFER_PREFIX } from "@/lib/schemas/share";

/** The same cases the Go tests pin: the bridge carries raw bytes and a
 * deadline, so each is a place the two sides could disagree about "done". */
describe("shareProgress", () => {
  it("treats an unknown total as complete", () => {
    expect(shareProgress({ transferredBytes: 0, totalSize: 0 })).toBe(1);
    expect(shareProgress({ transferredBytes: 50, totalSize: 0 })).toBe(1);
  });

  it("clamps a producer that over-reports", () => {
    expect(shareProgress({ transferredBytes: 50, totalSize: 200 })).toBe(0.25);
    expect(shareProgress({ transferredBytes: 400, totalSize: 200 })).toBe(1);
    expect(shareProgress({ transferredBytes: -5, totalSize: 200 })).toBe(0);
  });
});

describe("isTerminalState", () => {
  it("marks exactly the four states the protocol cannot leave", () => {
    for (const state of [
      "rejected",
      "expired",
      "failed",
      "completed",
    ] as const) {
      expect(isTerminalState(state)).toBe(true);
    }
    for (const state of [
      "created",
      "offered",
      "connecting",
      "transferring",
      "verifying",
    ] as const) {
      expect(isTerminalState(state)).toBe(false);
    }
  });
});

describe("isShareExpired", () => {
  const now = new Date("2026-10-16T12:00:00Z");

  it("treats a share with no deadline as never expired", () => {
    expect(isShareExpired({ expiresAt: null }, now)).toBe(false);
  });

  it("compares against the session's own deadline", () => {
    expect(
      isShareExpired({ expiresAt: new Date("2026-10-16T11:59:59Z") }, now)
    ).toBe(true);
    expect(
      isShareExpired({ expiresAt: new Date("2026-10-16T12:00:01Z") }, now)
    ).toBe(false);
  });
});

describe("secondsUntilExpiry", () => {
  const now = new Date("2026-10-16T12:00:00Z");

  it("rounds a partial minute to whole seconds and never goes negative", () => {
    expect(secondsUntilExpiry({ expiresAt: null }, now)).toBeNull();
    expect(
      secondsUntilExpiry({ expiresAt: new Date("2026-10-16T12:00:40Z") }, now)
    ).toBe(40);
    expect(
      secondsUntilExpiry({ expiresAt: new Date("2026-10-16T11:00:00Z") }, now)
    ).toBe(0);
  });
});

describe("normaliseShareLink", () => {
  it("repairs the two mistakes a paste actually makes", () => {
    expect(normaliseShareLink(`  ${P2P_OFFER_PREFIX}abc123  `)).toBe(
      `${P2P_OFFER_PREFIX}abc123`
    );
    expect(normaliseShareLink("abc123")).toBe(`${P2P_OFFER_PREFIX}abc123`);
    expect(normaliseShareLink(`dmm:p2p/abc`)).toBe(`${P2P_OFFER_PREFIX}abc`);
  });

  it("refuses a link of the wrong kind, not sending it to the backend", () => {
    expect(normaliseShareLink("https://example.com")).toBeNull();
    expect(normaliseShareLink("   ")).toBeNull();
    expect(normaliseShareLink(`${P2P_ANSWER_PREFIX}abc`)).toBeNull();
  });
});

describe("link classification", () => {
  it("tells an offer from an answer, since each half only accepts one", () => {
    expect(isOfferLink(`${P2P_OFFER_PREFIX}abc`)).toBe(true);
    expect(isAnswerLink(`${P2P_OFFER_PREFIX}abc`)).toBe(false);
    expect(isAnswerLink(`${P2P_ANSWER_PREFIX}abc`)).toBe(true);
    expect(isOfferLink(`${P2P_ANSWER_PREFIX}abc`)).toBe(false);
  });
});

describe("previewTotalSize", () => {
  it("sums the published files and ignores a negative size", () => {
    expect(previewTotalSize([])).toBe(0);
    expect(previewTotalSize([{ size: 10 }, { size: -4 }, { size: 5 }])).toBe(
      15
    );
  });
});
