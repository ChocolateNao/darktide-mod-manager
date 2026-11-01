import { z } from "zod";
import * as ActionCommands from "wailsjs/go/commands/ActionCommands";
import * as CacheCommands from "wailsjs/go/commands/CacheCommands";
import * as ConfigCommands from "wailsjs/go/commands/ConfigCommands";
import * as GameCommands from "wailsjs/go/commands/GameCommands";
import * as LogCommands from "wailsjs/go/commands/LogCommands";
import * as ModCommands from "wailsjs/go/commands/ModCommands";
import * as P2PShareCommands from "wailsjs/go/commands/P2PShareCommands";
import * as PathCommands from "wailsjs/go/commands/PathCommands";
import * as SourceCommands from "wailsjs/go/commands/SourceCommands";
import * as SourceLinkCommands from "wailsjs/go/commands/SourceLinkCommands";
import * as Lifecycle from "wailsjs/go/main/lifecycle";
import * as WireModels from "wailsjs/go/models";

import {
  mapActionResponse,
  mapActionResult,
  mapActions,
} from "@/lib/mappers/action";
import { mapCacheInfo, mapConfig } from "@/lib/mappers/config";
import {
  mapGameInfos,
  mapInstallation,
  mapInstallations,
} from "@/lib/mappers/game";
import { mapLogFiles, mapLogLines, mapLogSettings } from "@/lib/mappers/log";
import {
  mapConflicts,
  mapDroppedMod,
  mapHooks,
  mapHookScript,
  mapLinkStatuses,
  mapMod,
  mapModFiles,
  mapMods,
  mapSourceInfo,
  mapSourceInfos,
  mapSourceLinks,
  mapUpdates,
} from "@/lib/mappers/mod";
import {
  mapAnswerLink,
  mapPreviewResponse,
  mapSession,
  mapSessions,
  mapShareLink,
} from "@/lib/mappers/share";
import { toWireHooks, toWireSearchQuery } from "@/lib/mappers/toWire";
import { defaultPathsWireSchema } from "@/lib/schemas/config";
import {
  droppedPathResultWireSchema,
  modHooksWireSchema,
} from "@/lib/schemas/mod";
import type {
  ConflictResolution,
  GameInstallation,
  ModHooks,
  SearchQuery,
} from "@/types/domain";
import { BridgeError } from "@/types/errors";

/**
 * Wails turns a Go `(value, error)` return into a rejected promise carrying
 * whatever `errors.New` produced — a plain string. `pkg/errors` codes are
 * already flattened into that message by the time it reaches the webview, so
 * the code is recovered by matching the known set — a field that does not
 * exist. Every view matching message text breaks the moment one is reworded.
 */
const KNOWN_CODES = [
  "CONFIG_INVALID",
  "CONFIG_IO",
  "NOT_FOUND",
  "INVALID_ARGUMENT",
  "MOD_NOT_FOUND",
  "MOD_ALREADY_EXISTS",
  "MOD_NOT_INSTALLED",
  "INVALID_MOD",
  "DEPENDENCY_MISSING",
  "DEPENDENCY_CYCLE",
  "HOOK_FAILED",
  "HOOK_TIMEOUT",
  "HOOK_INVALID",
  "INSTALL_FAILED",
  "UNINSTALL_FAILED",
  "TRASH_UNAVAILABLE",
  "OPERATION_CANCELED",
  "NO_STORAGE_PATH",
  "GAME_NOT_FOUND",
  "GAME_INVALID",
  "MODS_PATH_NOT_DIR",
  "SOURCE_UNAVAILABLE",
  "SOURCE_NOT_FOUND",
  "SOURCE_EXISTS",
  "SOURCE_DISABLED",
  "SOURCE_REJECTED",
  "AUTH_FAILED",
  "AUTH_REQUIRED",
  "DOWNLOAD_FAILED",
  "SEARCH_FAILED",
  "INTEGRITY_CHECK_FAILED",
  "CACHE_MISS",
  "CACHE_CORRUPT",
  "RUNNER_NOT_FOUND",
  "HOOK_DENIED",
  "DUPLICATE",
  "ACTION_NOT_FOUND",
  "ACTION_UNAVAILABLE",
  "ACTION_FAILED",
  "SHARE_EXPIRED",
  "SHARE_INVALID",
  "SHARE_REVOKED",
  "SHARE_NOT_FOUND",
  "GAME_NOT_REGISTERED",
  "FILE_TOO_LARGE",
  "INTEGRITY_MISMATCH",
  "ALL_PROVIDERS_FAILED",
  "CLOUD_PROVIDER_NOT_FOUND",
  "CLOUD_AUTH_REQUIRED",
  "CLOUD_UPLOAD_FAILED",
  "CLOUD_DOWNLOAD_FAILED",
] as const;

const KNOWN_CODE_PATTERN = new RegExp(`\\b(${KNOWN_CODES.join("|")})\\b`);

export function toBridgeError(cause: unknown): BridgeError {
  if (cause instanceof BridgeError) return cause;

  const message =
    typeof cause === "string"
      ? cause
      : cause instanceof Error
        ? cause.message
        : ((cause as { message?: string } | null)?.message ??
          "The command failed.");

  const code = KNOWN_CODE_PATTERN.exec(message)?.[1];

  return new BridgeError(
    (code as BridgeError["code"]) ?? "UNKNOWN",
    // The code is already in the message; repeating it there helps when the
    // whole string ends up in a toast or a log line.
    code ? message : `${message}`,
    cause
  );
}

/**
 * One transport helper for every command. The mapper parses the wire shape it
 * knows about, so there is no second schema declaration to keep in step, and
 * `parse` runs in every environment — the DEV/prod split was rejected.
 */
async function invoke<T>(
  fn: () => Promise<unknown>,
  map: (raw: unknown) => T
): Promise<T> {
  try {
    return map(await fn());
  } catch (error) {
    throw toBridgeError(error);
  }
}

export const listMods = () => invoke(() => ModCommands.ListMods(), mapMods);

export const getMod = (id: string) =>
  invoke(() => ModCommands.GetMod(id), mapMod);

export const installMod = (sourceId: string, modId: string) =>
  invoke(() => ModCommands.InstallMod(sourceId, modId), mapMod);

/** The `nxm://` and Vortex forms a user actually has in a clipboard. */
export const importLink = (link: string) =>
  invoke(
    () =>
      ModCommands.ImportLink(link, {
        Force: false,
        PurgeCache: false,
        SourceData: {},
      }),
    mapMod
  );

export const shareLinkForMod = (id: string) =>
  invoke(
    () => ModCommands.ShareLinkForMod(id),
    (raw) => z.string().parse(raw)
  );

export const uninstallMod = (id: string, purgeCache = false, trash = false) =>
  invoke(
    () =>
      ModCommands.UninstallModWithOptions(id, {
        PurgeCache: purgeCache,
        Trash: trash,
      }),
    (raw) => z.null().parse(raw ?? null)
  );

export const trashAvailable = () =>
  invoke(
    () => ModCommands.TrashAvailable(),
    (raw) => z.boolean().parse(raw)
  );

export const enableMod = (id: string) =>
  invoke(
    () => ModCommands.EnableMod(id),
    (raw) => z.null().parse(raw ?? null)
  );

export const disableMod = (id: string) =>
  invoke(
    () => ModCommands.DisableMod(id),
    (raw) => z.null().parse(raw ?? null)
  );

export const checkModUpdates = () =>
  invoke(() => ModCommands.CheckModUpdates(), mapUpdates);

export const updateMod = (id: string) =>
  invoke(
    () => ModCommands.UpdateMod(id),
    (raw) => z.null().parse(raw ?? null)
  );

export const cancelUpdate = (id: string) =>
  invoke(
    () => ModCommands.CancelUpdate(id),
    (raw) => z.null().parse(raw ?? null)
  );

export const refreshMods = () =>
  invoke(
    () => ModCommands.RefreshMods(),
    (raw) => z.null().parse(raw ?? null)
  );

export const detectConflicts = () =>
  invoke(() => ModCommands.DetectConflicts(), mapConflicts);

export const resolveConflict = (reportId: string, how: ConflictResolution) =>
  invoke(
    () => ModCommands.ResolveConflict(reportId, how),
    (raw) => z.null().parse(raw ?? null)
  );

export const reorderMods = (ids: string[]) =>
  invoke(
    () => ModCommands.ReorderMods(ids),
    (raw) => z.null().parse(raw ?? null)
  );

export const loadOrder = () =>
  invoke(
    () => ModCommands.LoadOrder(),
    (raw) => z.array(z.string()).parse(raw)
  );

export const modPath = (id: string) =>
  invoke(
    () => ModCommands.ModPath(id),
    (raw) => z.string().parse(raw)
  );

export const listModFiles = (id: string) =>
  invoke(() => ModCommands.ListModFiles(id), mapModFiles);

export const symlinkStatus = () =>
  invoke(() => ModCommands.SymlinkStatus(), mapLinkStatuses);

export const relinkMod = (id: string) =>
  invoke(
    () => ModCommands.RelinkMod(id),
    (raw) => z.array(z.string()).parse(raw)
  );

export const getModHooks = (id: string) =>
  invoke(
    () => ModCommands.GetModHooks(id),
    (raw) => mapHooks(modHooksWire(raw))
  );

/**
 * `wailsjs/go/models` declares `domain.ModHooks` as a *class* whose constructor
 * rehydrates nested values through `convertValues`. A plain object is therefore
 * not assignable to the generated signature, and `createFrom` is the supported way
 * to hand the binding one — it is also what turns each nested `HookScript` into
 * the instance the bridge expects.
 */
export const updateModHooks = (id: string, hooks: ModHooks) =>
  invoke(
    () =>
      ModCommands.UpdateModHooks(
        id,
        WireModels.domain.ModHooks.createFrom(toWireHooks(hooks))
      ),
    (raw) => z.null().parse(raw ?? null)
  );

export { mapHookScript };

export const listSources = () =>
  invoke(() => SourceCommands.ListSources(), mapSourceInfos);

export const sourceInfo = (id: string) =>
  invoke(() => SourceCommands.SourceInfo(id), mapSourceInfo);

export const searchSource = (sourceId: string, query: string) =>
  invoke(() => SourceCommands.SearchSource(sourceId, query), mapMods);

export const searchAll = (query: SearchQuery) =>
  invoke(() => SourceCommands.SearchAll(toWireSearchQuery(query)), mapMods);

export const getSourceMod = (sourceId: string, modId: string) =>
  invoke(() => SourceCommands.GetSourceMod(sourceId, modId), mapMod);

export const authenticateSource = (
  sourceId: string,
  credentials: Record<string, string>
) =>
  invoke(
    () => SourceCommands.AuthenticateSource(sourceId, credentials),
    ignoreVoid
  );

export const setSourceApiKey = (sourceId: string, apiKey: string) =>
  invoke(() => SourceCommands.SetSourceAPIKey(sourceId, apiKey), ignoreVoid);

export const clearSourceApiKey = (sourceId: string) =>
  invoke(() => SourceCommands.ClearSourceAPIKey(sourceId), ignoreVoid);

export const testSourceConnection = (sourceId: string) =>
  invoke(() => SourceCommands.TestSourceConnection(sourceId), ignoreVoid);

export const setSourceAuthenticated = (
  sourceId: string,
  authenticated: boolean
) =>
  invoke(
    () => SourceCommands.SetSourceAuthenticated(sourceId, authenticated),
    ignoreVoid
  );

/**
 * A dropped path is resolved into a named mod *before* anything is installed.
 */
export const inspectDroppedPath = (path: string) =>
  invoke(
    () => SourceCommands.InspectDroppedPath(path),
    (raw) => {
      const result = droppedPathResultWireSchema.parse(raw);
      return {
        mod: result.mod ? mapDroppedMod(result.mod) : null,
        recognised: result.recognised,
      };
    }
  );

export const listGames = () =>
  invoke(() => GameCommands.SupportedGames(), mapGameInfos);

export const detectGame = () =>
  invoke(() => GameCommands.DetectGame(), mapInstallations);

export const currentGame = () =>
  invoke(
    () => GameCommands.CurrentGame(),
    (raw) => (raw === null || raw === undefined ? null : mapInstallation(raw))
  );

export const validateGamePath = (path: string) =>
  invoke(() => GameCommands.ValidateGamePath(path), mapInstallation);

export const setGamePath = (path: string) =>
  invoke(() => GameCommands.SetGamePath(path), ignoreVoid);

/** Invalidates every game-scoped key: actions, source links and shares are per-game. */
export const useGame = (id: string) =>
  invoke(() => GameCommands.UseGame(id), ignoreVoid);

/** Takes the whole installation object — the picker keeps it. */
export const getModsPath = (install: GameInstallation) =>
  invoke(
    () => GameCommands.GetModsPath(install as never),
    (raw) => z.string().parse(raw)
  );

export const getCacheInfo = () =>
  invoke(() => CacheCommands.GetCacheInfo(), mapCacheInfo);

export const clearCache = () =>
  invoke(() => CacheCommands.ClearCache(), ignoreVoid);

export const deleteAllData = () =>
  invoke(() => ConfigCommands.DeleteAllData(), ignoreVoid);

export const deleteData = (modData: boolean, config: boolean) =>
  invoke(
    () => ConfigCommands.DeleteData({ ModData: modData, Config: config }),
    ignoreVoid
  );

export const pruneCache = (maxSizeGb: number, maxAgeDays: number) =>
  invoke(() => CacheCommands.PruneCache(maxSizeGb, maxAgeDays), ignoreVoid);

export const pruneWithDefaults = () =>
  invoke(() => CacheCommands.PruneWithDefaults(), ignoreVoid);

export const cacheRoot = () =>
  invoke(
    () => CacheCommands.CacheRoot(),
    (raw) => z.string().parse(raw)
  );

export const getConfig = () =>
  invoke(() => ConfigCommands.GetConfig(), mapConfig);

export const defaultPaths = () =>
  invoke(
    () => ConfigCommands.DefaultPaths(),
    (raw) => defaultPathsWireSchema.parse(raw)
  );

export const updateConfig = (config: unknown) =>
  invoke(() => ConfigCommands.UpdateConfig(config as never), ignoreVoid);

export const getConfigValue = (key: string) =>
  invoke(
    () => ConfigCommands.GetConfigValue(key),
    (raw) => z.string().parse(raw)
  );

export const setConfigValue = (key: string, value: string) =>
  invoke(() => ConfigCommands.SetConfigValue(key, value), ignoreVoid);

export const configKeys = () =>
  invoke(
    () => ConfigCommands.ConfigKeys(),
    (raw) => z.array(z.string()).parse(raw)
  );

export const configPath = () =>
  invoke(
    () => ConfigCommands.ConfigPath(),
    (raw) => z.string().parse(raw)
  );

export const getLogSettings = () =>
  invoke(() => LogCommands.GetLogSettings(), mapLogSettings);

export const setLogLevel = (level: string) =>
  invoke(() => LogCommands.SetLogLevel(level), ignoreVoid);

export const tailLogs = (count: number, minLevel: string) =>
  invoke(() => LogCommands.TailLogs(count, minLevel), mapLogLines);

export const listLogFiles = () =>
  invoke(() => LogCommands.ListLogFiles(), mapLogFiles);

export const openLogDirectory = () =>
  invoke(() => LogCommands.OpenLogDirectory(), ignoreVoid);

export const clearLogs = (all: boolean) =>
  invoke(() => LogCommands.ClearLogs(all), ignoreVoid);

export const setRedactFields = (fields: string[]) =>
  invoke(() => LogCommands.SetRedactFields(fields), ignoreVoid);

export const createShare = (
  modIds: string[],
  expiresAt: string,
  senderName: string,
  stunServers: string[]
) =>
  invoke(
    () =>
      P2PShareCommands.CreateShare(modIds, expiresAt, senderName, stunServers),
    mapShareLink
  );

export const previewShare = (link: string) =>
  invoke(() => P2PShareCommands.PreviewShare(link), mapPreviewResponse);

export const acceptShare = (link: string, receiverName: string) =>
  invoke(() => P2PShareCommands.AcceptShare(link, receiverName), mapAnswerLink);

export const rejectShare = (link: string, reason: string) =>
  invoke(() => P2PShareCommands.RejectShare(link, reason), mapAnswerLink);

export const awaitRejectedShare = (offerId: string) =>
  invoke(() => P2PShareCommands.AwaitRejectedShare(offerId), ignoreVoid);

export const sendShare = (offerId: string, answerLink: string) =>
  invoke(() => P2PShareCommands.SendShare(offerId, answerLink), ignoreVoid);

export const awaitShare = (offerId: string) =>
  invoke(() => P2PShareCommands.AwaitShare(offerId), ignoreVoid);

export const installShare = (offerId: string, enable: boolean) =>
  invoke(() => P2PShareCommands.InstallShare(offerId, enable), mapMods);

export const getShareSession = (offerId: string) =>
  invoke(() => P2PShareCommands.GetShareSession(offerId), mapSession);

export const listShareSessions = () =>
  invoke(() => P2PShareCommands.ListShareSessions(), mapSessions);

export const cancelShare = (offerId: string) =>
  invoke(() => P2PShareCommands.CancelShare(offerId), ignoreVoid);

export const discardShare = (offerId: string) =>
  invoke(() => P2PShareCommands.DiscardShare(offerId), ignoreVoid);

export const defaultStunServers = () =>
  invoke(
    () => P2PShareCommands.DefaultSTUNServers(),
    (raw) => z.array(z.string()).parse(raw)
  );

export const listActions = (gameId: string) =>
  invoke(() => ActionCommands.ListActions(gameId), mapActions);

export const listAvailableActions = (gameId: string, platform: string) =>
  invoke(
    () => ActionCommands.ListAvailableActions(gameId, platform),
    mapActions
  );

export const getGameAction = (id: string) =>
  invoke(() => ActionCommands.GetGameAction(id), mapActionResponse);

export const executeGameAction = (id: string, args: Record<string, unknown>) =>
  invoke(() => ActionCommands.ExecuteGameAction(id, args), mapActionResult);

export const actionGameId = () =>
  invoke(
    () => ActionCommands.CurrentGameID(),
    (raw) => z.string().parse(raw)
  );

export const actionPlatform = () =>
  invoke(
    () => ActionCommands.Platform(),
    (raw) => z.string().parse(raw)
  );

export const modSourceLinks = () =>
  invoke(() => SourceLinkCommands.ModSourceLinks(), mapSourceLinks);

export const modSourceLink = (modId: string) =>
  invoke(
    () => SourceLinkCommands.ModSourceLink(modId),
    (raw) => mapSourceLinks({ [modId]: raw }).get(modId)
  );

/** Returns the fallback alongside any error, so a page link can be offered instead. */
export const openModSource = (modId: string, target: string) =>
  invoke(
    () => SourceLinkCommands.OpenModSource(modId, target),
    (raw) => (typeof raw === "string" ? (raw as never) : undefined)
  );

export const linkTargets = () =>
  invoke(
    () => SourceLinkCommands.LinkTargets(),
    (raw) => z.array(z.string()).parse(raw)
  );

export const writeModManifest = (modId: string) =>
  invoke(
    () => SourceLinkCommands.WriteModManifest(modId),
    (raw) => z.string().parse(raw)
  );

/**
 * `ChooseDirectory` returns two values and the generated declaration claims
 * one; a Wails multi-return crosses as a positional array. `cleared` separates
 * "reset to the platform default" from "the user cancelled".
 */
export const chooseDirectory = (start: string) =>
  invoke(
    () => PathCommands.ChooseDirectory(start),
    (raw) => z.tuple([z.string(), z.boolean()]).parse(raw)
  );

export const chooseFile = (start: string, patterns: string[]) =>
  invoke(
    () => PathCommands.ChooseFile(start, patterns),
    (raw) => z.string().parse(raw)
  );

export const revealDirectory = (path: string) =>
  invoke(() => PathCommands.RevealDirectory(path), ignoreVoid);

export const eventTypes = () =>
  invoke(
    () => Lifecycle.EventTypes(),
    (raw) => z.array(z.string()).parse(raw)
  );

export const hookTypes = () =>
  invoke(
    () => Lifecycle.HookTypes(),
    (raw) => z.array(z.string()).parse(raw)
  );

export const hookPhaseNames = () =>
  invoke(
    () => Lifecycle.HookPhases(),
    (raw) => z.array(z.string()).parse(raw)
  );

export const supportsHookType = (name: string) =>
  invoke(
    () => Lifecycle.SupportsHookType(name),
    (raw) => z.boolean().parse(raw)
  );

/** Milliseconds, so the value goes straight into a timer. */
export const hookTimeoutMs = (script: unknown, mod: unknown) =>
  invoke(
    () => Lifecycle.HookTimeout(script as never, mod as never),
    (raw) => z.number().parse(raw)
  );

export const validateHookScript = (script: unknown, mod: unknown) =>
  invoke(
    () => Lifecycle.ValidateHookScript(script as never, mod as never),
    ignoreVoid
  );

/** Runs one script under the same containment a real hook run uses. */
export const runHookScript = (script: unknown, mod: unknown) =>
  invoke(
    () => Lifecycle.RunHookScript(script as never, mod as never),
    ignoreVoid
  );

export const cancelAll = () => invoke(() => Lifecycle.CancelAll(), ignoreVoid);

export const stopped = () =>
  invoke(
    () => Lifecycle.Stopped(),
    (raw) => z.boolean().parse(raw)
  );

const ignoreVoid = (raw: unknown) => z.null().parse(raw ?? null);

const modHooksWire = (raw: unknown) => modHooksWireSchema.parse(raw);
