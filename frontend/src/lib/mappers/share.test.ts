import { describe, expect, it } from "vitest";

import {
  mapAnswerLink,
  mapPreviewResponse,
  mapSessions,
  mapShareLink,
} from "./share";

// Offer keys are single letters to keep the URL small; guessing a name fails here.
const previewWire = {
  created_at: "2026-10-15T09:00:00Z",
  expires_at: "2026-10-22T09:00:00Z",
  offer_id: "offer-1",
  game_id: "darktide",
  sender: "andre",
  mods: [
    {
      archive: { id: "file-1", h: "abc", f: "zip", s: 2048 },
      name: "Cool Mod",
      version: "1.2.0",
      author: "someone",
      description: "does things",
      files: ["file-1"],
      deps: [],
    },
  ],
  files: [{ i: "file-1", n: "cool.zip", m: "coolmod", h: "abc", s: 2048 }],
  source_links: [{ m: "nexus-900-7001", s: "nexus", f: "browse" }],
  total_size: 2048,
  expired: false,
};

const sessionWire = {
  updated_at: "2026-10-15T09:05:00Z",
  created_at: "2026-10-15T09:00:00Z",
  preview: previewWire,
  expires_at: null,
  state: "transferring",
  fail_reason: "",
  link: "dmm://p2p/v1/abc",
  offer_id: "offer-1",
  role: "sender",
  game_id: "darktide",
  mod_ids: ["coolmod"],
  total_size: 2048,
  transferred_bytes: 1024,
};

describe("share mappers", () => {
  it("expands the single-letter offer payload", () => {
    const preview = mapPreviewResponse(previewWire);

    expect(preview.offerId).toBe("offer-1");
    expect(preview.sender).toBe("andre");
    expect(preview.totalSize).toBe(2048);
    expect(preview.files[0]).toEqual({
      fileId: "file-1",
      name: "cool.zip",
      modId: "coolmod",
      hashSha256: "abc",
      size: 2048,
    });
    expect(preview.mods[0].archive).toEqual({
      id: "file-1",
      hash: "abc",
      format: "zip",
      size: 2048,
    });
    expect(preview.mods[0].fileIds).toEqual(["file-1"]);
  });

  it("turns the RFC3339 timestamps into dates and an absent expiry into null", () => {
    const preview = mapPreviewResponse(previewWire);

    expect(preview.createdAt).toBeInstanceOf(Date);
    expect(preview.createdAt?.toISOString()).toBe("2026-10-15T09:00:00.000Z");
    expect(preview.expiresAt?.toISOString()).toBe("2026-10-22T09:00:00.000Z");
  });

  it("maps the source link's single-letter tags into the fallback enum", () => {
    const preview = mapPreviewResponse(previewWire);

    expect(preview.sourceLinks[0].modId).toBe("nexus-900-7001");
    expect(preview.sourceLinks[0].sourceId).toBe("nexus");
    // `browse` means there is a page but no direct download; the view labels it.
    expect(preview.sourceLinks[0].fallback).toBe("browse");
  });

  it("maps a session, turning an empty fail reason into an absent field", () => {
    const [session] = mapSessions([sessionWire]);

    expect(session.state).toBe("transferring");
    expect(session.role).toBe("sender");
    expect(session.modIds).toEqual(["coolmod"]);
    expect(session.failReason).toBeUndefined();
    expect(session.expiresAt).toBeNull();
    expect(session.preview?.mods[0].name).toBe("Cool Mod");
  });

  it("maps the sender's offer link", () => {
    const link = mapShareLink({
      created_at: "2026-10-15T09:00:00Z",
      link: "dmm://p2p/v1/abc",
      sender: "andre",
      offer: {
        c: "2026-10-15T09:00:00Z",
        o: "offer-1",
        g: "darktide",
        n: "andre",
        m: previewWire.mods,
        f: previewWire.files,
        l: previewWire.source_links,
        s: ["stun:stun.l.google.com:19302"],
      },
    });

    expect(link.offer.offerId).toBe("offer-1");
    expect(link.offer.stunServers).toEqual(["stun:stun.l.google.com:19302"]);
    expect(link.offer.files[0].name).toBe("cool.zip");
  });

  it("maps the recipient's answer link", () => {
    const answer = mapAnswerLink({
      created_at: "2026-10-15T09:10:00Z",
      link: "dmm://p2pa/v1/xyz",
      offer_id: "offer-1",
      receiver: "sam",
    });

    expect(answer.link).toBe("dmm://p2pa/v1/xyz");
    expect(answer.offerId).toBe("offer-1");
    expect(answer.receiver).toBe("sam");
  });

  it("fills an absent mod list, not leaving it undefined", () => {
    const [session] = mapSessions([
      { ...sessionWire, mod_ids: undefined, preview: undefined },
    ]);

    expect(session.modIds).toEqual([]);
    expect(session.preview).toBeUndefined();
  });
});
