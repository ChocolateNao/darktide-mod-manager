import { z } from "zod";

import { modSourceLinkWireSchema } from "./mod";
import { int, optionalText, textList, wireDate } from "./shared";

export const P2P_OFFER_PREFIX = "dmm://p2p/";
export const P2P_ANSWER_PREFIX = "dmm://p2pa/";

export const packedModWireSchema = z.object({
  id: z.string(),
  h: z.string(),
  f: z.string().default(""),
  s: int.default(0),
});

export const sharedModWireSchema = z.object({
  archive: packedModWireSchema.nullish(),
  name: z.string(),
  version: z.string().default(""),
  author: z.string().default(""),
  description: z.string().default(""),
  files: textList,
  deps: textList,
});

export const p2pSharedFileWireSchema = z.object({
  i: z.string(),
  n: z.string(),
  m: z.string().default(""),
  h: z.string().default(""),
  s: int.default(0),
});

export const p2pShareOfferWireSchema = z.object({
  c: wireDate,
  e: wireDate,
  o: z.string(),
  g: z.string().default(""),
  n: optionalText,
  m: z.array(sharedModWireSchema).default([]),
  f: z.array(p2pSharedFileWireSchema).default([]),
  l: z.array(modSourceLinkWireSchema).nullish(),
  s: textList,
});

export const p2pShareLinkWireSchema = z.object({
  created_at: wireDate,
  link: z.string(),
  sender: optionalText,
  offer: p2pShareOfferWireSchema,
});

export const p2pAnswerLinkWireSchema = z.object({
  created_at: wireDate,
  link: z.string(),
  offer_id: z.string(),
  receiver: optionalText,
});

export const p2pSharePreviewWireSchema = z.object({
  created_at: wireDate,
  expires_at: wireDate,
  offer_id: z.string(),
  game_id: z.string().default(""),
  sender: optionalText,
  mods: z.array(sharedModWireSchema).default([]),
  files: z.array(p2pSharedFileWireSchema).default([]),
  source_links: z.array(modSourceLinkWireSchema).nullish(),
  total_size: int.default(0),
  expired: z.boolean().default(false),
});

export const p2pShareStateSchema = z.enum([
  "created",
  "offered",
  "connecting",
  "transferring",
  "verifying",
  "completed",
  "rejected",
  "expired",
  "failed",
]);

export const p2pShareSessionWireSchema = z.object({
  updated_at: wireDate,
  created_at: wireDate,
  preview: p2pSharePreviewWireSchema.nullish(),
  expires_at: wireDate,
  state: p2pShareStateSchema.catch("failed"),
  fail_reason: optionalText,
  link: optionalText,
  offer_id: z.string(),
  role: z.string().default(""),
  game_id: z.string().default(""),
  mod_ids: textList,
  total_size: int.default(0),
  transferred_bytes: int.default(0),
});

export const p2pSessionListWireSchema = z.array(p2pShareSessionWireSchema);

/**
 * `ChooseDirectory` returns two values; a Wails multi-return crosses the bridge
 * as a positional array.
 */
export const chooseDirectoryResultWireSchema = z.tuple([
  z.string(),
  z.boolean(),
]);
