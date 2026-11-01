import { QueryClient } from "@tanstack/react-query";

import { BRIDGE_ERROR_CODES, BridgeError } from "@/types/errors";

/**
 * How often a cheap read re-runs itself. The event bus is the real invalidation
 * signal; a `config.json` hand-edited while the app runs only shows up on this
 * poll, so a minute of staleness is the price. Event-driven reads (the log tail)
 * opt out with `refetchInterval: false`.
 */
export const BACKGROUND_REFETCH_MS = 60_000;

/**
 * Update checks leave the app for every source, so they run on a slower floor
 * than local reads.
 */
export const UPDATE_CHECK_MS = 5 * 60_000;

/** Codes worth a second attempt: the backend was busy. */
const TRANSIENT_CODES = new Set([
  "CACHE_CORRUPT",
  "DOWNLOAD_FAILED",
  "SEARCH_FAILED",
  "SOURCE_UNAVAILABLE",
]);

/** A missing mod is permanent: retrying repeats the same NOT_FOUND and delays
 * the error the user needs to read. */
function shouldRetry(failureCount: number, failure: unknown): boolean {
  if (failure instanceof BridgeError) {
    if (failure.code === "NOT_FOUND" || failure.code === "MOD_NOT_FOUND")
      return false;
    return TRANSIENT_CODES.has(failure.code) && failureCount < 2;
  }
  return failureCount < 2;
}

/**
 * The defaults are wrong for a push-driven backend, so every one is set
 * deliberately. Wails commands are in-process calls: no server announces
 * changes, window blur changes nothing, there is no network to reconnect to.
 * The event bus is the invalidation signal.
 */
export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: Infinity,
      gcTime: 5 * 60_000,
      refetchOnWindowFocus: false,
      refetchOnMount: false,
      refetchOnReconnect: false,
      refetchInterval: BACKGROUND_REFETCH_MS,
      retry: shouldRetry,
    },
    mutations: {
      retry: false,
    },
  },
});

/** Opt-out for the handful of reads the floor must not touch. */
export const NO_REFETCH = { refetchInterval: false } as const;

export const queryKeys = {
  config: () => ["config"] as const,
  configPath: () => ["config-path"] as const,
  defaultPaths: () => ["config", "default-paths"] as const,
  games: () => ["games"] as const,
  installations: () => ["installations"] as const,
  currentGame: () => ["current-game"] as const,
  mods: () => ["mods"] as const,
  loadOrder: () => ["load-order"] as const,
  updates: () => ["updates"] as const,
  conflicts: () => ["conflicts"] as const,
  linkStatuses: () => ["link-statuses"] as const,
  modHooks: (modId: string) => ["mods", modId, "hooks"] as const,
  sources: () => ["sources"] as const,
  search: (query: string) => ["search", query] as const,
  cache: () => ["cache"] as const,
  logSettings: () => ["log-settings"] as const,
  logFiles: () => ["log-files"] as const,
  logTail: (count: number, minLevel: string) =>
    ["log-tail", count, minLevel] as const,
  sessions: () => ["p2p-sessions"] as const,
  actions: (gameId: string, platform: string) =>
    ["actions", gameId, platform] as const,
  sourceLinks: () => ["source-links"] as const,
  linksForMod: (modId: string) => ["source-links", modId] as const,
};

export { BRIDGE_ERROR_CODES };
