import { lazy, Suspense } from "react";

import {
  ActionDialogHost,
  ActionPalette,
} from "@/components/actions/ActionPalette";
import { EmptyState } from "@/components/common/Surfaces";
import { GameRail } from "@/components/layout/GameRail";
import { Header } from "@/components/layout/Header";
import { NavTabs } from "@/components/layout/NavTabs";
import { StatusBar } from "@/components/layout/StatusBar";
import { ModDrawer } from "@/components/mods/ModDrawer";
import { DropOverlay } from "@/components/sources/DropOverlay";
import { Toaster } from "@/components/ui/sonner";
import { t } from "@/lib/i18n";
import { useUiStore } from "@/stores/ui.store";
import AddModView from "@/views/AddModView";
import ModsView from "@/views/ModsView";

const LogsView = lazy(() => import("@/views/LogsView"));
const SettingsView = lazy(() => import("@/views/SettingsView"));
const ShareView = lazy(() => import("@/views/ShareView"));

/**
 * The rail, the active view, and the overlays above both.
 *
 * Views mount lazily because Share owns a blocking backend call and Logs a file
 * tail; Mods and Add Mod are eager since the user lands on them.
 */
interface ViewSlotProps {
  ready: boolean;
}

export function ViewSlot({ ready }: ViewSlotProps) {
  const view = useUiStore((state) => state.view);

  return (
    <>
      <GameRail />

      <div className="relative flex min-w-0 flex-1 flex-col">
        <Header />
        <NavTabs />

        <main className="min-h-0 flex-1 overflow-hidden">
          {/* A view mounted against an empty store would show "no mods", so wait for the first config load. */}
          {!ready ? (
            <EmptyState title={t("common.loading")} className="h-full" />
          ) : (
            <Suspense
              fallback={
                <EmptyState title={t("common.loading")} className="h-full" />
              }
            >
              {view === "mods" && <ModsView />}
              {view === "add-mod" && <AddModView />}
              {view === "share" && <ShareView />}
              {view === "settings" && <SettingsView />}
              {view === "logs" && <LogsView />}
            </Suspense>
          )}
        </main>

        <StatusBar />
        <DropOverlay />
      </div>

      <ModDrawer />
      {/* The run dialog is a sibling of the palette, so an action from either surface renders the same request. */}
      <ActionPalette />
      <ActionDialogHost />
      <Toaster />
    </>
  );
}
