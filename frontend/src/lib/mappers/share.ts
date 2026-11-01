import type { z } from "zod";

import { mapSourceLink } from "@/lib/mappers/mod";
import {
  p2pAnswerLinkWireSchema,
  p2pSessionListWireSchema,
  p2pShareLinkWireSchema,
  p2pSharePreviewWireSchema,
} from "@/lib/schemas/share";
import type {
  P2PAnswerLink,
  P2PSharedFile,
  P2PShareLink,
  P2PSharePreview,
  P2PShareSession,
  PackedMod,
  SharedMod,
} from "@/types/domain";

type PreviewWire = z.infer<typeof p2pSharePreviewWireSchema>;
type SharedModWire = PreviewWire["mods"][number];
type SessionWire = (typeof p2pSessionListWireSchema extends z.ZodArray<infer E>
  ? E
  : never)["_output"];

const mapFile = (wire: {
  i: string;
  n: string;
  m: string;
  h: string;
  s: number;
}): P2PSharedFile => ({
  fileId: wire.i,
  name: wire.n,
  modId: wire.m,
  hashSha256: wire.h,
  size: wire.s,
});

const mapPacked = (wire: {
  id: string;
  h: string;
  f: string;
  s: number;
}): PackedMod => ({
  id: wire.id,
  hash: wire.h,
  format: wire.f,
  size: wire.s,
});

const mapSharedMod = (wire: SharedModWire): SharedMod => ({
  archive: wire.archive ? mapPacked(wire.archive) : undefined,
  name: wire.name,
  version: wire.version,
  author: wire.author,
  description: wire.description,
  fileIds: wire.files ?? [],
  dependencies: wire.deps ?? [],
});

export const mapPreview = (wire: PreviewWire): P2PSharePreview => ({
  createdAt: wire.created_at ?? null,
  expiresAt: wire.expires_at ?? null,
  offerId: wire.offer_id,
  gameId: wire.game_id,
  sender: wire.sender,
  mods: wire.mods.map(mapSharedMod),
  files: wire.files.map(mapFile),
  sourceLinks: (wire.source_links ?? []).map(mapSourceLink),
  totalSize: wire.total_size,
  expired: wire.expired,
});

export const mapPreviewResponse = (raw: unknown): P2PSharePreview =>
  mapPreview(p2pSharePreviewWireSchema.parse(raw));

export const mapShareLink = (raw: unknown): P2PShareLink => {
  const wire = p2pShareLinkWireSchema.parse(raw);

  return {
    createdAt: wire.created_at ?? null,
    link: wire.link,
    sender: wire.sender,
    offer: {
      offerId: wire.offer.o,
      gameId: wire.offer.g,
      senderName: wire.offer.n,
      mods: wire.offer.m.map(mapSharedMod),
      files: wire.offer.f.map(mapFile),
      sourceLinks: (wire.offer.l ?? []).map(mapSourceLink),
      stunServers: wire.offer.s ?? [],
      createdAt: wire.offer.c ?? null,
      expiresAt: wire.offer.e ?? null,
    },
  };
};

export const mapAnswerLink = (raw: unknown): P2PAnswerLink => {
  const wire = p2pAnswerLinkWireSchema.parse(raw);

  return {
    createdAt: wire.created_at ?? null,
    link: wire.link,
    offerId: wire.offer_id,
    receiver: wire.receiver,
  };
};

const fromSessionWire = (wire: SessionWire): P2PShareSession => ({
  updatedAt: wire.updated_at ?? null,
  createdAt: wire.created_at ?? null,
  preview: wire.preview ? mapPreview(wire.preview) : undefined,
  expiresAt: wire.expires_at ?? null,
  state: wire.state,
  failReason: wire.fail_reason,
  link: wire.link,
  offerId: wire.offer_id,
  role: wire.role,
  gameId: wire.game_id,
  modIds: wire.mod_ids ?? [],
  totalSize: wire.total_size,
  transferredBytes: wire.transferred_bytes,
});

export const mapSessions = (raw: unknown): P2PShareSession[] =>
  p2pSessionListWireSchema.parse(raw).map(fromSessionWire);

export const mapSession = (raw: unknown): P2PShareSession =>
  fromSessionWire(p2pSessionListWireSchema.parse([raw])[0]);

/**
 * Maps one session without throwing, for the event router. A payload that is not
 * a session means the backend changed shape, and dropping the event beats
 * opening a row that cannot render.
 */
export const mapSessionOrNull = (raw: unknown): P2PShareSession | null => {
  const parsed = p2pSessionListWireSchema.safeParse([raw]);
  return parsed.success ? fromSessionWire(parsed.data[0]) : null;
};

export type { PreviewWire, SessionWire };
