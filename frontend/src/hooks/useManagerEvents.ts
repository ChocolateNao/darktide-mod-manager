import { useEffect } from "react";

import type { ManagerEventsSink } from "@/bridge/events";
import { subscribeToManagerEvents } from "@/bridge/events";
import { notify } from "@/lib/notify";
import { queryClient, queryKeys } from "@/lib/queryClient";
import { useActivityStore } from "@/stores/activity.store";
import { useModsStore } from "@/stores/mods.store";
import { useSharingStore } from "@/stores/sharing.store";

/**
 * The single place a backend event becomes a state change: everything above
 * reacts to stores and react-query, and nothing above it imports the Wails
 * runtime. One subscription, so one handler's bug cannot take the rest down.
 */
function buildSink(): ManagerEventsSink {
  const activity = () => useActivityStore.getState();

  return {
    activity: {
      begin: (entry) => activity().begin(entry),
      setProgress: (id, fraction) => activity().setProgress(id, fraction),
      setDetail: (id, detail) => activity().setDetail(id, detail),
      finish: (id) => activity().finish(id),
      fail: (id, reason) => activity().fail(id, reason),
      find: (criteria) => activity().find(criteria),
    },

    changed: (scope) => {
      void queryClient.invalidateQueries({ queryKey: queryKeys[scope]() });
    },

    // Patching the row keeps the toggle from flickering through a refetch,
    // which would read as the switch springing back before it settles.
    modEnabled: (modId, enabled) =>
      useModsStore.getState().patchMod(modId, { enabled }),

    log: (line) => activity().appendLog(line),

    hookConsole: (event) => activity().appendHookConsole(event),

    notify,

    cache: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.cache() });
    },

    share: (session) => useSharingStore.getState().upsertSession(session),

    // An unknown type means the backend added one: log it in devtools, silent
    // to the user.
    unhandled: (type) => {
      if (import.meta.env.DEV) console.debug("[events] no handler for", type);
    },
  };
}

export function useManagerEvents(): void {
  useEffect(() => subscribeToManagerEvents(buildSink()), []);
}
