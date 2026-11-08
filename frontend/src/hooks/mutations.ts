import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import * as api from "@/bridge/client";
import { describeBridgeFailure } from "@/bridge/dialogs";
import { notifyFailure } from "@/lib/errorToast";
import { t } from "@/lib/i18n";
import { modFamily, nxmModId, sameModPage } from "@/lib/modLink";
import { queryKeys } from "@/lib/queryClient";
import { useActivityStore } from "@/stores/activity.store";
import { useInstallPromptStore } from "@/stores/installPrompt.store";
import { useModsStore } from "@/stores/mods.store";
import type { ModUpdateInfo } from "@/types/domain";
import type { BridgeErrorCode } from "@/types/errors";

/**
 * Every write: bridge call first, invalidation second, slice-local state last.
 * Doing it the other way round paints a result the backend never agreed to.
 */

function report(error: unknown): void {
  notifyFailure(t("common.error"), error);
}

/** No failure event exists for every operation, so a rejected mutation closes its own row. */
function failRunning(
  kind: "install" | "uninstall" | "update",
  error: unknown,
  modId?: string
): void {
  const activity = useActivityStore.getState();
  for (const entry of activity.entries) {
    if (
      entry.kind === kind &&
      entry.state === "running" &&
      (modId === undefined || entry.modId === modId)
    ) {
      activity.fail(entry.id, describeBridgeFailure(error));
    }
  }
}

const MOD_PAGE_ERROR_CODES = new Set<BridgeErrorCode>([
  "DOWNLOAD_FAILED",
  "AUTH_REQUIRED",
  "AUTH_FAILED",
]);

/** Whether only the mod page can clear the failure: a refused download or an expired key. */
export function updateNeedsModPage(error: unknown): boolean {
  return MOD_PAGE_ERROR_CODES.has(api.toBridgeError(error).code);
}

function reportUpdateFailure(error: unknown, modId: string): void {
  if (!updateNeedsModPage(error)) {
    report(error);
    return;
  }

  notifyFailure(t("notify.updates.downloadFailed"), error, {
    action: {
      label: t("mods.openPage"),
      onClick: () => {
        void api.openModSource(modId, "source").catch(report);
      },
    },
  });
}

/** The invalidation map: a write moves more than the obvious query. */
type Scope = Exclude<
  keyof typeof queryKeys,
  "modHooks" | "search" | "logTail" | "actions" | "linksForMod"
>;

function invalidate(
  queryClient: ReturnType<typeof useQueryClient>,
  scopes: Scope[]
): void {
  for (const scope of scopes) {
    void queryClient.invalidateQueries({ queryKey: queryKeys[scope]() });
  }
}

/** An install that stopped to ask whether to update or downgrade is not a failure. */
class InstallDecisionPending extends Error {}

function directionOf(info: ModUpdateInfo) {
  if (info.available) return "update" as const;
  if (info.remoteOlder) return "downgrade" as const;
  return "reinstall" as const;
}

/** Raises the replacement prompt for an installed mod and stops the caller. */
async function requestReplacement({
  sourceId,
  modId,
  link,
}: {
  sourceId: string;
  modId: string;
  link?: string;
}): Promise<void> {
  const existing = useModsStore
    .getState()
    .mods.find((mod) => sameModPage(mod.id, modId) && !mod.external);

  if (!existing) return;

  const info = await api.checkModUpdate(existing.id);

  if (!info.remoteVersion) return;

  useInstallPromptStore.getState().request({
    mode: link ? "link" : "replace",
    link,
    sourceId,
    modId,
    name: existing.name || modId,
    currentVersion: info.localVersion || existing.version,
    targetVersion: info.remoteVersion,
    direction: directionOf(info),
  });

  throw new InstallDecisionPending();
}

export function useInstallMod() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async ({
      sourceId,
      modId,
      force = false,
    }: {
      sourceId: string;
      modId: string;
      force?: boolean;
    }) => {
      if (!force) await requestReplacement({ sourceId, modId });

      return api.installMod(sourceId, modId, force);
    },
    onSuccess: (mod) => {
      invalidate(queryClient, ["mods", "conflicts", "updates"]);

      if (!mod) return;

      const installed = new Set(
        useModsStore.getState().mods.map((entry) => modFamily(entry.id))
      );
      installed.add(modFamily(mod.id));
      const missing = mod.dependencies.filter(
        (dep) => !sameModPage(dep, mod.id) && !installed.has(modFamily(dep))
      );

      if (missing.length > 0) {
        toast.warning(t("mods.missingRequirements"), {
          description: missing.join(", "),
        });
      } else if (typeof mod.sourceData?.dependency_error === "string") {
        toast.warning(t("mods.dependencyLookupFailed"));
      }
    },
    onError: (error) => {
      if (error instanceof InstallDecisionPending) return;

      // `mod.install.failed` is never published by the backend, so rejection is
      // the only signal; otherwise the status bar spins forever.
      failRunning("install", error);
      report(error);
    },
  });
}

export function useCheckModUpdate() {
  return useMutation({
    mutationFn: (id: string) => api.checkModUpdate(id),
    onSuccess: (info) => useModsStore.getState().setUpdate(info),
    onError: report,
  });
}

export function useImportLink() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async ({
      link,
      force = false,
    }: {
      link: string;
      force?: boolean;
    }) => {
      if (!force) {
        const modId = nxmModId(link);

        if (modId) await requestReplacement({ sourceId: "nexus", modId, link });
      }

      return api.importLink(link, force);
    },
    onSuccess: () => invalidate(queryClient, ["mods", "conflicts", "updates"]),
    onError: (error) => {
      if (!(error instanceof InstallDecisionPending)) report(error);
    },
  });
}

export function useUninstallMod() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({
      id,
      purgeCache,
      trash,
    }: {
      id: string;
      purgeCache: boolean;
      trash?: boolean;
    }) => api.uninstallMod(id, purgeCache, trash ?? false),
    // The backend may delete the files and then fail to sync the load-order
    // file; refetching on error keeps a dead row out of the table.
    onSettled: () => invalidate(queryClient, ["mods", "conflicts"]),
    onError: (error) => {
      failRunning("uninstall", error);
      report(error);
    },
  });
}

/**
 * Optimistic with a rollback: the round trip is long enough that waiting reads
 * as the control being broken, and a rejection must not be the last word.
 */
export function useToggleMod() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ id, enable }: { id: string; enable: boolean }) =>
      enable ? api.enableMod(id) : api.disableMod(id),
    onMutate: async ({ id, enable }) => {
      await queryClient.cancelQueries({ queryKey: queryKeys.mods() });
      const previous = useModsStore
        .getState()
        .mods.find((mod) => mod.id === id)?.enabled;
      useModsStore.getState().patchMod(id, { enabled: enable });
      return { id, previous };
    },
    onError: (error, _variables, context) => {
      if (context?.previous !== undefined) {
        useModsStore
          .getState()
          .patchMod(context.id, { enabled: context.previous });
      }

      if (api.toBridgeError(error).code === "MOD_BLOCKED") {
        notifyFailure(t("mods.blocked"), error);
        return;
      }

      report(error);
    },
    onSettled: () => invalidate(queryClient, ["mods", "conflicts"]),
  });
}

/** One round trip for a selection, so a bulk toggle invalidates once. */
export function useBulkToggleMods() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ ids, enable }: { ids: string[]; enable: boolean }) =>
      Promise.all(
        ids.map((id) => (enable ? api.enableMod(id) : api.disableMod(id)))
      ),
    onMutate: async ({ ids, enable }) => {
      await queryClient.cancelQueries({ queryKey: queryKeys.mods() });
      const previous = useModsStore
        .getState()
        .mods.filter((mod) => ids.includes(mod.id))
        .map((mod) => ({ id: mod.id, enabled: mod.enabled }));

      for (const id of ids) {
        useModsStore.getState().patchMod(id, { enabled: enable });
      }

      return previous;
    },
    onError: (error, _variables, previous) => {
      for (const entry of previous ?? []) {
        useModsStore.getState().patchMod(entry.id, { enabled: entry.enabled });
      }
      report(error);
    },
    onSettled: () => invalidate(queryClient, ["mods", "conflicts"]),
  });
}

export function useBulkUninstallMods() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ ids, trash }: { ids: string[]; trash?: boolean }) =>
      Promise.all(ids.map((id) => api.uninstallMod(id, false, trash ?? false))),
    onSettled: () => invalidate(queryClient, ["mods", "conflicts"]),
    onError: (error) => {
      failRunning("uninstall", error);
      report(error);
    },
  });
}

export function useUpdateMod() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ id }: { id: string; quiet?: boolean }) => api.updateMod(id),
    onSuccess: () => invalidate(queryClient, ["mods", "updates", "conflicts"]),
    onError: (error, { id, quiet }) => {
      failRunning("update", error, id);

      // The update dialog keeps its own failure and offer; a toast behind it
      // cannot be clicked while the modal holds pointer events.
      if (!quiet) reportUpdateFailure(error, id);
    },
  });
}

export function useCancelUpdate() {
  return useMutation({
    mutationFn: (id: string) => api.cancelUpdate(id),
    onError: report,
  });
}

export function useRefreshMods() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: () => api.refreshMods(),
    onSuccess: () => invalidate(queryClient, ["mods", "updates", "conflicts"]),
    onError: report,
  });
}

export function useReorderMods() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (ids: string[]) => api.reorderMods(ids),
    // Row order is load order, so the local list moves with the drag itself.
    onMutate: (ids) => useModsStore.getState().applyLoadOrder(ids),
    onSuccess: () =>
      invalidate(queryClient, ["mods", "loadOrder", "conflicts"]),
    onError: (error) => {
      // A rejected write has to refetch, or the next drag is computed from an
      // order the backend never stored.
      invalidate(queryClient, ["mods", "loadOrder"]);
      report(error);
    },
  });
}

export function useRelinkMod() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (id: string) => api.relinkMod(id),
    onSuccess: () => invalidate(queryClient, ["mods", "linkStatuses"]),
    onError: report,
  });
}

export function useDetectConflicts() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: () => api.detectConflicts(),
    onSuccess: () => invalidate(queryClient, ["conflicts"]),
    onError: report,
  });
}

export function useResolveConflict() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({
      reportId,
      how,
    }: {
      reportId: string;
      how: Parameters<typeof api.resolveConflict>[1];
    }) => api.resolveConflict(reportId, how),
    onSuccess: () => invalidate(queryClient, ["mods", "conflicts", "updates"]),
    onError: report,
  });
}

export function useSaveModHooks() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({
      id,
      hooks,
    }: {
      id: string;
      hooks: Parameters<typeof api.updateModHooks>[1];
    }) => api.updateModHooks(id, hooks),
    onSuccess: (_data, { id }) => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.modHooks(id) });
      invalidate(queryClient, ["mods"]);
    },
    onError: report,
  });
}

export function useSetGamePath() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (path: string) => api.setGamePath(path),
    onSuccess: () =>
      invalidate(queryClient, [
        "config",
        "games",
        "installations",
        "currentGame",
      ]),
    onError: report,
  });
}

/**
 * Switching games clears the whole cache. A stale action list is worse than a
 * stale mod list: it offers a button that runs against the wrong game.
 */
export function useSwitchGame() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (id: string) => {
      await api.useGame(id);
      queryClient.clear();
      useModsStore.getState().clearForGameSwitch();
      useActivityStore.getState().clearLog();
    },
    onError: report,
  });
}

export function usePruneCache() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({
      maxSizeGb,
      maxAgeDays,
    }: {
      maxSizeGb: number;
      maxAgeDays: number;
    }) => api.pruneCache(maxSizeGb, maxAgeDays),
    onSuccess: () => invalidate(queryClient, ["cache"]),
    onError: report,
  });
}

export function useClearCache() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: () => api.clearCache(),
    onSuccess: () => invalidate(queryClient, ["cache"]),
    onError: report,
  });
}

/** Removes every mod, the cache and the settings; the UI starts from scratch. */
export function useDeleteAllData() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: () => api.deleteAllData(),
    onSuccess: () => {
      queryClient.clear();
      useModsStore.getState().clearForGameSwitch();
      useActivityStore.getState().clearLog();
      toast.success(t("settings.advanced.deleteAllDone"));
    },
    onError: report,
  });
}

/** Removes the selected parts of the application data, leaving the rest alone. */
export function useDeleteData() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ modData, config }: { modData: boolean; config: boolean }) =>
      api.deleteData(modData, config),
    onSuccess: (_result, { modData }) => {
      queryClient.clear();
      if (modData) {
        useModsStore.getState().clearForGameSwitch();
        useActivityStore.getState().clearLog();
      }
      toast.success(t("settings.advanced.deleteDataDone"));
    },
    onError: report,
  });
}

// Only the queries a key can actually affect are refetched: saving the logging
// form used to run a remote update check for every key it wrote.
function configScopes(key: string): Scope[] {
  const scopes: Scope[] = ["config"];

  if (key.startsWith("auto_update.")) scopes.push("updates");
  if (key.startsWith("cache.")) scopes.push("cache");
  if (key.startsWith("sources.")) scopes.push("sources");
  if (key.startsWith("games.") || key.startsWith("game.")) scopes.push("games");
  if (key.startsWith("logging.")) scopes.push("logSettings");

  return scopes;
}

/**
 * `SetConfigValue` writes one key and `UpdateConfig` writes the whole file.
 * Both end up in `["config"]` plus whatever the changed key feeds.
 */
export function useSetConfigValue() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ key, value }: { key: string; value: string }) =>
      api.setConfigValue(key, value),
    onSuccess: (_data, { key }) => {
      // A level change alters which records the file yields, so the tail is re-read.
      if (key.startsWith("logging.")) {
        void queryClient.invalidateQueries({ queryKey: ["log-tail"] });
      }
      invalidate(queryClient, configScopes(key));
    },
    onError: report,
  });
}

export function useSaveConfig() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (config: Parameters<typeof api.updateConfig>[0]) =>
      api.updateConfig(config),
    onSuccess: () =>
      invalidate(queryClient, [
        "config",
        "games",
        "sources",
        "cache",
        "updates",
      ]),
    onError: report,
  });
}

export function useSetSourceApiKey() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ sourceId, apiKey }: { sourceId: string; apiKey: string }) =>
      api.setSourceApiKey(sourceId, apiKey),
    onSuccess: () => {
      invalidate(queryClient, ["sources", "config"]);

      // Only a credential can change a search — the failed AUTH_REQUIRED search
      // must not survive the prompt it opened behind.
      void queryClient.invalidateQueries({ queryKey: ["search"] });
    },
    onError: report,
  });
}

export function useClearSourceApiKey() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (sourceId: string) => api.clearSourceApiKey(sourceId),
    onSuccess: () => invalidate(queryClient, ["sources", "config"]),
    onError: report,
  });
}

export function useTestSourceConnection() {
  return useMutation({
    mutationFn: (sourceId: string) => api.testSourceConnection(sourceId),
    onSuccess: () => toast.success(t("settings.sources.tested")),
    onError: report,
  });
}

export function useSetSourceAuthenticated() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({
      sourceId,
      authenticated,
    }: {
      sourceId: string;
      authenticated: boolean;
    }) => api.setSourceAuthenticated(sourceId, authenticated),
    onSuccess: () => invalidate(queryClient, ["sources", "config"]),
    onError: report,
  });
}

export function useCreateShare() {
  return useMutation({
    mutationFn: (variables: {
      modIds: string[];
      expiresAt: string;
      senderName: string;
      stunServers: string[];
    }) =>
      api.createShare(
        variables.modIds,
        variables.expiresAt,
        variables.senderName,
        variables.stunServers
      ),
    onError: report,
  });
}

export function usePreviewShare() {
  return useMutation({
    mutationFn: (link: string) => api.previewShare(link),
    onError: report,
  });
}

export function useAcceptShare() {
  return useMutation({
    mutationFn: (variables: { link: string; receiverName: string }) =>
      api.acceptShare(variables.link, variables.receiverName),
    onError: report,
  });
}

export function useRejectShare() {
  return useMutation({
    mutationFn: (variables: { link: string; reason: string }) =>
      api.rejectShare(variables.link, variables.reason),
    onError: report,
  });
}

/** `SendShare` and `AwaitShare` block for the length of the transfer. */
export function useSendShare() {
  return useMutation({
    mutationFn: ({
      offerId,
      answerLink,
    }: {
      offerId: string;
      answerLink: string;
    }) => api.sendShare(offerId, answerLink),
    onError: report,
  });
}

export function useAwaitShare() {
  return useMutation({
    mutationFn: (offerId: string) => api.awaitShare(offerId),
    onError: report,
  });
}

export function useAwaitRejectedShare() {
  return useMutation({
    mutationFn: (offerId: string) => api.awaitRejectedShare(offerId),
    onError: report,
  });
}

export function useInstallShare() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ offerId, enable }: { offerId: string; enable: boolean }) =>
      api.installShare(offerId, enable),
    onSuccess: () => invalidate(queryClient, ["mods", "conflicts", "sessions"]),
    onError: report,
  });
}

export function useCancelShare() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (offerId: string) => api.cancelShare(offerId),
    onSuccess: () => invalidate(queryClient, ["sessions"]),
    onError: report,
  });
}

export function useDiscardShare() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (offerId: string) => api.discardShare(offerId),
    onSuccess: () => invalidate(queryClient, ["sessions"]),
    onError: report,
  });
}

/**
 * An action can install, validate or launch, so every query it could have moved
 * is invalidated. The *result* goes to a dialog with the captured process
 * output, which a toast cannot hold.
 */
export function useRunGameAction() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ id, args }: { id: string; args: Record<string, unknown> }) =>
      api.executeGameAction(id, args),
    onSuccess: () => invalidate(queryClient, ["mods", "conflicts"]),
    onError: report,
  });
}

export function useSetLogLevel() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (level: string) => api.setLogLevel(level),
    onSuccess: () => invalidate(queryClient, ["logSettings"]),
    onError: report,
  });
}

export function useSetRedactFields() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (fields: string[]) => api.setRedactFields(fields),
    onSuccess: () => invalidate(queryClient, ["logSettings", "config"]),
    onError: report,
  });
}

export function useClearLogs() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (all: boolean) => api.clearLogs(all),
    onSuccess: () => {
      useActivityStore.getState().clearLog();
      invalidate(queryClient, ["logFiles", "logSettings"]);
    },
    onError: report,
  });
}

export function useWriteModManifest() {
  return useMutation({
    mutationFn: (modId: string) => api.writeModManifest(modId),
    onError: report,
  });
}

export function useCancelAll() {
  return useMutation({
    mutationFn: () => api.cancelAll(),
    onError: report,
  });
}
