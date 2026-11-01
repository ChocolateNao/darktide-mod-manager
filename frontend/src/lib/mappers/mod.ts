import type { z } from "zod";

import type {
  conflictReportWireSchema,
  hookScriptWireSchema,
  modHooksWireSchema,
  modLinkStatusWireSchema,
  modUpdateWireSchema,
} from "@/lib/schemas/mod";
import {
  conflictListWireSchema,
  droppedModWireSchema,
  hookPhases,
  modFileListWireSchema,
  modLinkStatusListWireSchema,
  modListWireSchema,
  modSourceInfoListWireSchema,
  modSourceInfoWireSchema,
  modSourceLinkMapWireSchema,
  modSourceLinkWireSchema,
  modUpdateListWireSchema,
  modWireSchema,
} from "@/lib/schemas/mod";
import type {
  ConflictReport,
  DroppedMod,
  HookScript,
  Mod,
  ModFile,
  ModHooks,
  ModLinkStatus,
  ModSourceInfo,
  ModSourceLink,
  ModUpdateInfo,
  ScriptLanguage,
} from "@/types/domain";

type ModWire = z.infer<typeof modWireSchema>;
type HookScriptWire = z.infer<typeof hookScriptWireSchema>;
type ModHooksWire = z.infer<typeof modHooksWireSchema>;
type ConflictWire = z.infer<typeof conflictReportWireSchema>;

type LinkStatusWire = z.infer<typeof modLinkStatusWireSchema>;
type SourceInfoWire = z.infer<typeof modSourceInfoWireSchema>;
type SourceLinkWire = z.infer<typeof modSourceLinkWireSchema>;

const asLanguage = (value: string): ScriptLanguage =>
  value === "javascript" || value === "js" ? "javascript" : "lua";

export const mapHookScript = (wire: HookScriptWire): HookScript => ({
  language: asLanguage(wire.language),
  content: wire.content,
  workingDir: wire.working_dir,
  env: wire.env,
  timeout: wire.timeout ?? undefined,
  name: wire.name,
  disabled: wire.disabled,
});

/**
 * Every phase is a key even when Go omitted it, so the editor can index by phase
 * without a presence check and a half-configured mod cannot crash it.
 */
export const mapHooks = (wire: ModHooksWire | null | undefined): ModHooks => {
  const source = wire ?? {};

  return Object.fromEntries(
    hookPhases.map((phase) => [phase, (source[phase] ?? []).map(mapHookScript)])
  ) as ModHooks;
};

const fromModWire = (wire: ModWire): Mod => ({
  id: wire.id,
  name: wire.name,
  version: wire.version,
  author: wire.author,
  description: wire.description,
  sourceId: wire.source_id,
  sourceData: wire.source_data ?? {},
  gameId: wire.game_id,
  gameVersion: wire.game_version,
  modFolderName: wire.mod_folder_name,
  installMode: wire.install_mode,
  hash: wire.hash,
  localPath: wire.local_path,
  sourceUrl: wire.source_url,
  downloadUrl: wire.download_url,
  updateUrl: wire.update_url,
  dependencies: wire.dependencies ?? [],
  conflictsWith: wire.conflicts_with ?? [],
  tags: wire.tags ?? [],
  issues: wire.issues ?? [],
  priority: wire.priority,
  enabled: wire.enabled,
  external: wire.external,
  installedAt: wire.installed_at ?? null,
  updatedAt: wire.updated_at ?? null,
  hooks: mapHooks(wire.hooks),
});

export const mapMods = (raw: unknown): Mod[] =>
  modListWireSchema.parse(raw).map(fromModWire);

export const mapConflicts = (raw: unknown): ConflictReport[] =>
  conflictListWireSchema.parse(raw).map((wire) => ({
    id: wire.id,
    type: wire.type,
    modA: wire.mod_a ? fromModWire(wire.mod_a) : undefined,
    modB: wire.mod_b ? fromModWire(wire.mod_b) : undefined,
    details: wire.details,
    severity: wire.severity,
    recommendation: wire.recommendation,
  }));

export const mapUpdates = (raw: unknown): ModUpdateInfo[] =>
  modUpdateListWireSchema.parse(raw).map((wire) => ({
    modId: wire.mod_id,
    localVersion: wire.local_version,
    remoteVersion: wire.remote_version,
    sourceId: wire.source_id,
    available: wire.available,
    releaseNotes: wire.release_notes,
    downloadUrl: wire.download_url,
    changelogUrl: wire.changelog_url,
  }));

export const mapModFiles = (raw: unknown): ModFile[] =>
  modFileListWireSchema.parse(raw).map((wire) => ({
    path: wire.path,
    name: wire.name,
    size: wire.size,
    isDir: wire.is_dir,
  }));

export const mapLinkStatuses = (raw: unknown): ModLinkStatus[] =>
  modLinkStatusListWireSchema.parse(raw).map((wire) => ({
    entries: wire.entries,
    modId: wire.mod_id,
    damaged: wire.damaged,
  }));

// Single-letter tags keep a share link pasteable; this is the only place that knows them.
const fromSourceLinkWire = (wire: SourceLinkWire): ModSourceLink => ({
  modId: wire.m,
  sourceId: wire.s,
  sourceUrl: wire.p,
  downloadUrl: wire.d,
  updateUrl: wire.u,
  fallback: wire.f,
});

export const mapSourceLinks = (raw: unknown): Map<string, ModSourceLink> => {
  const wire = modSourceLinkMapWireSchema.parse(raw);
  return new Map(
    Object.entries(wire).map(([modId, link]) => [
      modId,
      fromSourceLinkWire(link),
    ])
  );
};

const fromSourceInfoWire = (wire: SourceInfoWire): ModSourceInfo => ({
  id: wire.id,
  name: wire.name,
  description: wire.description,
  version: wire.version,
  supportsSearch: wire.supports_search,
  supportsAuth: wire.supports_auth,
  requiresApiKey: wire.requires_api_key,
  enabled: wire.enabled,
  authenticated: wire.authenticated,
  hasApiKey: wire.has_api_key,
});

export const mapSourceInfos = (raw: unknown): ModSourceInfo[] =>
  modSourceInfoListWireSchema.parse(raw).map(fromSourceInfoWire);

/**
 * Single-object mappers take `unknown` and parse it themselves, so a caller
 * cannot hand an unvalidated object to a component.
 */
export const mapMod = (raw: unknown): Mod =>
  fromModWire(modWireSchema.parse(raw));

export const mapSourceLink = (raw: unknown): ModSourceLink =>
  fromSourceLinkWire(modSourceLinkWireSchema.parse(raw));

export const mapSourceInfo = (raw: unknown): ModSourceInfo =>
  fromSourceInfoWire(modSourceInfoWireSchema.parse(raw));

export type { ConflictWire, LinkStatusWire, SourceInfoWire, SourceLinkWire };
export type ModUpdateWire = z.infer<typeof modUpdateWireSchema>;
/** The overlay only needs a name and a version to name the mod it found. */
export function mapDroppedMod(raw: unknown): DroppedMod {
  const wire = droppedModWireSchema.parse(raw);
  return {
    sourceId: wire.source_id,
    modId: wire.mod_id,
    name: wire.name,
    version: wire.version,
    author: wire.author,
    path: wire.path,
  };
}
