import { useEffect } from "react";

import { registerNotificationCategories } from "@/bridge/notifications";
import { AppShell } from "@/components/layout/AppShell";
import { ViewSlot } from "@/components/layout/ViewSlot";
import {
  useConfigQuery,
  useGamesQuery,
  useInstallationsQuery,
  useModsQuery,
} from "@/hooks/queries";
import { useGlobalShortcuts } from "@/hooks/useGlobalShortcuts";
import { useManagerEvents } from "@/hooks/useManagerEvents";
import { primeNotificationFunnel, stopNotificationFunnel } from "@/lib/notify";
import { useGamesStore } from "@/stores/games.store";

/**
 * The one place the app's background work is started.
 *
 * Everything arrives over a single `mod-manager:event` channel; the queries
 * below keep a 60-second floor, so a dropped event is a minute of staleness.
 */
export function App() {
  useManagerEvents();
  useGlobalShortcuts();

  const config = useConfigQuery();
  const games = useGamesQuery();
  const hasGame = Boolean(games.data?.length);

  useModsQuery(hasGame);
  useInstallationsQuery(hasGame);

  useEffect(() => {
    void registerNotificationCategories();
    void primeNotificationFunnel();

    return () => {
      stopNotificationFunnel();
    };
  }, []);

  // The installation list is what decides whether a rail entry gets a warning
  // dot, and it arrives after the games do.
  useEffect(() => {
    if (games.data) useGamesStore.getState().recomputeValidity();
  }, [games.data]);

  return (
    <AppShell>
      <ViewSlot ready={Boolean(config.data && games.data)} />
    </AppShell>
  );
}
