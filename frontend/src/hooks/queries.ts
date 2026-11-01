import { useEffect, useRef } from "react";
import { useQuery } from "@tanstack/react-query";

import * as api from "@/bridge/client";
import { NO_REFETCH, queryKeys, UPDATE_CHECK_MS } from "@/lib/queryClient";
import { useActivityStore } from "@/stores/activity.store";
import { useConfigStore } from "@/stores/config.store";
import { useGamesStore } from "@/stores/games.store";
import { useModsStore } from "@/stores/mods.store";
import { useSharingStore } from "@/stores/sharing.store";
import { useUiStore } from "@/stores/ui.store";

// One home per value: the store, so an event patch and a refetch land in the
// same place. The ref keeps inline closures from looping "maximum update depth".
function useSync<T>(data: T | undefined, write: (value: T) => void): void {
  const writeRef = useRef(write);

  // Declared first so it has run by the time the write effect fires, on mount
  // as well as on every later update.
  useEffect(() => {
    writeRef.current = write;
  });

  useEffect(() => {
    if (data !== undefined) writeRef.current(data);
  }, [data]);
}

/**
 * The configuration, loaded once. It is also what the theme and compact-mode
 * pre-paint cache is reconciled against — `uiConfig` is the truth and the
 * persisted store is only a cache, so a settings edit has to win here.
 */
export function useConfigQuery() {
  const query = useQuery({
    queryKey: queryKeys.config(),
    queryFn: api.getConfig,
  });

  useSync(query.data, (config) => {
    useConfigStore.getState().setConfig(config);
    useConfigStore.getState().setLoadError(null);
    useUiStore.getState().adoptConfig(config.ui.theme, config.ui.compactMode);
  });

  return query;
}

export function useConfigPathQuery() {
  const query = useQuery({
    queryKey: queryKeys.configPath(),
    queryFn: api.configPath,
  });

  useSync(query.data, (path) => useConfigStore.getState().setConfigPath(path));

  return query;
}

export function useGamesQuery() {
  const query = useQuery({
    queryKey: queryKeys.games(),
    queryFn: api.listGames,
  });

  useSync(query.data, (games) => {
    useGamesStore.getState().setGames(games);
    const current = useGamesStore.getState().activeGameId;
    if (!current && games.length > 0) {
      const active = games.find((game) => game.isActive) ?? games[0];
      useGamesStore.getState().setActiveGameId(active.id);
    }
  });

  return query;
}

/**
 * Installations are probed, so they are refreshed on demand — the Add Game
 * dialog and the rail's warning dot — and otherwise by the 60-second floor.
 */
export function useInstallationsQuery(enabled = true) {
  const query = useQuery({
    queryKey: queryKeys.installations(),
    queryFn: api.detectGame,
    enabled,
  });

  useSync(query.data, (installations) => {
    useGamesStore.getState().setInstallations(installations);
    useGamesStore.getState().recomputeValidity();
  });

  return query;
}

/** Only fetched where it is shown. */
export function useCurrentGameQuery(enabled = true) {
  return useQuery({
    queryKey: queryKeys.currentGame(),
    queryFn: api.currentGame,
    enabled,
  });
}

/**
 * Conflicts are re-read after every mutating event through the invalidation
 * map in `bridge/events.ts`; caching them here keeps the banner from
 * re-probing on every render.
 */
export function useModsQuery(enabled = true) {
  const mods = useQuery({
    queryKey: queryKeys.mods(),
    queryFn: api.listMods,
    enabled,
  });
  const order = useQuery({
    queryKey: queryKeys.loadOrder(),
    queryFn: api.loadOrder,
    enabled,
  });
  const updates = useQuery({
    queryKey: queryKeys.updates(),
    queryFn: api.checkModUpdates,
    enabled,
    refetchInterval: UPDATE_CHECK_MS,
  });
  const conflicts = useQuery({
    queryKey: queryKeys.conflicts(),
    queryFn: api.detectConflicts,
    enabled,
  });
  const links = useQuery({
    queryKey: queryKeys.linkStatuses(),
    queryFn: api.symlinkStatus,
    enabled,
  });

  useSync(mods.data, (value) => useModsStore.getState().setMods(value));
  useSync(order.data, (value) => useModsStore.getState().setLoadOrder(value));
  useSync(updates.data, (value) => useModsStore.getState().setUpdates(value));
  useSync(conflicts.data, (value) =>
    useModsStore.getState().setConflicts(value)
  );
  useSync(links.data, (value) =>
    useModsStore.getState().setLinkStatuses(value)
  );

  return { mods, order, updates, conflicts, links };
}

export function useModHooksQuery(modId: string | null) {
  return useQuery({
    queryKey: queryKeys.modHooks(modId ?? ""),
    queryFn: () => api.getModHooks(modId ?? ""),
    enabled: modId !== null,
  });
}

export function useSourcesQuery(enabled = true) {
  return useQuery({
    queryKey: queryKeys.sources(),
    queryFn: api.listSources,
    enabled,
  });
}

/** One page of results. `SEARCH_PAGE_SIZE` is the ceiling the backend honours. */
export const SEARCH_PAGE_SIZE = 50;

/**
 * `searchAll` is called because `searchSource` takes a bare string and cannot
 * paginate: a Nexus query through it returned the whole trending feed with no
 * page two.
 *
 * Keying by text *and* page keeps two live queries from clobbering each other.
 */
export function useSearchQuery(query: {
  sourceId?: string;
  text: string;
  gameVersion?: string;
  page?: number;
}) {
  const trimmed = query.text.trim();
  const active = trimmed.length > 0;
  const page = Math.max(1, query.page ?? 1);

  return useQuery({
    queryKey: queryKeys.search(
      `${query.sourceId ?? "all"}:${query.gameVersion ?? ""}:${trimmed}:${page}`
    ),
    enabled: active,
    queryFn: () =>
      api.searchAll({
        query: trimmed,
        gameVersion: query.gameVersion,
        page,
        pageSize: SEARCH_PAGE_SIZE,
      }),
  });
}

/** Per-mod and changed only by a reinstall, so it is a query of its own. */
export function useModFilesQuery(modId: string) {
  return useQuery({
    queryKey: ["mod-files", modId],
    queryFn: () => api.listModFiles(modId),
    enabled: modId !== "",
    // A reinstall is the only thing that changes the answer, and it arrives as
    // an event, so this query skips the shared poll floor.
    refetchInterval: NO_REFETCH.refetchInterval,
  });
}

export function useCacheInfoQuery() {
  return useQuery({ queryKey: queryKeys.cache(), queryFn: api.getCacheInfo });
}

export function useLogSettingsQuery() {
  return useQuery({
    queryKey: queryKeys.logSettings(),
    queryFn: api.getLogSettings,
  });
}

export function useLogFilesQuery() {
  return useQuery({
    queryKey: queryKeys.logFiles(),
    queryFn: api.listLogFiles,
  });
}

/**
 * No background refetch: `TailLogs` re-parses the log file on every call, and
 * the `log` event already appends to the same ring buffer, so a floor here
 * would be a second mechanism racing the first.
 */
export function useLogTailQuery(
  count: number,
  minLevel: string,
  enabled = true
) {
  const query = useQuery({
    queryKey: queryKeys.logTail(count, minLevel),
    queryFn: () => api.tailLogs(count, minLevel),
    enabled,
    // The file changed while the view was unmounted, so every open re-reads it.
    staleTime: 0,
    refetchOnMount: "always",
    ...NO_REFETCH,
  });

  useSync(query.data, (lines) =>
    useActivityStore.getState().replaceLogTail(lines)
  );

  return query;
}

/**
 * Populates the session list once. Everything after this arrives on the
 * `share.*` events — a poll alongside them would hide a dropped event behind a
 * timer that still looks like it is working.
 */
export function useShareSessionsQuery(enabled = true) {
  const query = useQuery({
    queryKey: queryKeys.sessions(),
    queryFn: api.listShareSessions,
    enabled,
  });

  useSync(query.data, (sessions) =>
    useSharingStore.getState().setSessions(sessions)
  );

  return query;
}

export function useDefaultStunQuery() {
  return useQuery({
    queryKey: ["stun-default"],
    queryFn: api.defaultStunServers,
  });
}

/** Actions are per game and platform gated, so both are part of the cache key. */
export function useAvailableActionsQuery(gameId: string, enabled = true) {
  return useQuery({
    queryKey: queryKeys.actions(gameId, "available"),
    queryFn: () => api.listAvailableActions(gameId, ""),
    // An empty game id means "every action on this platform"; gating on gameId
    // left the palette empty in the first-run state of no configured game.
    enabled,
  });
}

export function useGameActionQuery(actionId: string | null) {
  return useQuery({
    queryKey: [...queryKeys.actions(actionId ?? "", ""), "detail", actionId],
    queryFn: () => api.getGameAction(actionId ?? ""),
    enabled: actionId !== null,
  });
}

export function useSourceLinksQuery() {
  return useQuery({
    queryKey: queryKeys.sourceLinks(),
    queryFn: api.modSourceLinks,
  });
}

export function useLinkTargetsQuery() {
  return useQuery({ queryKey: ["link-targets"], queryFn: api.linkTargets });
}

export function useLifecycleVocabulary() {
  const types = useQuery({ queryKey: ["vocab-types"], queryFn: api.hookTypes });
  const phases = useQuery({
    queryKey: ["vocab-phases"],
    queryFn: api.hookPhaseNames,
  });

  return { types, phases };
}

/** The folders used when a settings path is empty; read-only. */
export function useDefaultPathsQuery() {
  return useQuery({
    queryKey: queryKeys.defaultPaths(),
    queryFn: api.defaultPaths,
    ...NO_REFETCH,
  });
}
