import { z } from "zod";

import { optionalText, wireDate } from "./shared";

export const gameInfoWireSchema = z.object({
  id: z.string(),
  display_name: z.string(),
  description: optionalText,
  default_install_method: z.string().default(""),
  install_modes: z.array(z.string()).default([]),
  is_default: z.boolean().default(false),
  is_active: z.boolean().default(false),
});

export const gameInfoListWireSchema = z.array(gameInfoWireSchema);

export const gameInstallationWireSchema = z.object({
  game_id: optionalText,
  path: z.string(),
  source: z.string().default(""),
  app_id: z.string().default(""),
  version: z.string().default(""),
  mods_path: z.string().default(""),
  platform: z.string().default(""),
  notes: optionalText,
  valid: z.boolean().default(false),
  detected_at: wireDate,
});

export const gameInstallationListWireSchema = z.array(
  gameInstallationWireSchema
);

/**
 * `GetModsPath` takes the whole installation, so the picker that configures a
 * game keeps the object it validated.
 */
export const modsPathResultWireSchema = z.string();
