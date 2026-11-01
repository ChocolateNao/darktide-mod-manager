import type { z } from "zod";

import {
  gameInfoListWireSchema,
  gameInstallationListWireSchema,
} from "@/lib/schemas/game";
import type { GameInfo, GameInstallation } from "@/types/domain";

type GameInfoWire = z.infer<typeof gameInfoListWireSchema.element>;
type InstallationWire = z.infer<typeof gameInstallationListWireSchema.element>;

const fromInstallationWire = (wire: InstallationWire): GameInstallation => ({
  gameId: wire.game_id,
  path: wire.path,
  source: wire.source,
  appId: wire.app_id,
  version: wire.version,
  modsPath: wire.mods_path,
  platform: wire.platform,
  notes: wire.notes,
  valid: wire.valid,
  detectedAt: wire.detected_at ?? null,
});

export const mapGameInfos = (raw: unknown): GameInfo[] =>
  gameInfoListWireSchema.parse(raw).map((wire) => ({
    id: wire.id,
    displayName: wire.display_name,
    description: wire.description,
    defaultInstallMethod: wire.default_install_method,
    installModes: wire.install_modes,
    isDefault: wire.is_default,
    isActive: wire.is_active,
  }));

export const mapInstallations = (raw: unknown): GameInstallation[] =>
  gameInstallationListWireSchema.parse(raw).map(fromInstallationWire);

export const mapInstallation = (raw: unknown): GameInstallation =>
  fromInstallationWire(gameInstallationListWireSchema.parse([raw])[0]);

export type { GameInfoWire, InstallationWire };
