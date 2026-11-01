import { type ReactNode, useEffect } from "react";

import { applyWindowTheme, onWindowFocusChange } from "@/bridge/dialogs";
import { gameAccent } from "@/lib/gameAccent";
import { useConfigStore } from "@/stores/config.store";
import { selectActiveGameId, useGamesStore } from "@/stores/games.store";
import {
  resolveTheme,
  selectCompact,
  selectTheme,
  useUiStore,
} from "@/stores/ui.store";

/**
 * The window.
 *
 * The rail picks the game, the tab row picks the view; everything between the
 * tab row and the status bar belongs to the mounted view.
 */
interface AppShellProps {
  children: ReactNode;
}

export function AppShell({ children }: AppShellProps) {
  const theme = useUiStore(selectTheme);
  const compact = useUiStore(selectCompact);
  const activeGameId = useGamesStore(selectActiveGameId);
  const configTheme = useConfigStore((state) => state.config?.ui.theme);

  // The document element carries theme and density so CSS can react, matching
  // the attributes index.html writes before React runs.
  useEffect(() => {
    const resolved = resolveTheme(theme);
    document.documentElement.classList.toggle("dark", resolved === "dark");
    document.documentElement.dataset.theme = resolved;
  }, [theme]);

  useEffect(() => {
    applyWindowTheme(resolveTheme(theme) === "dark");
  }, [theme]);

  useEffect(() => {
    const root = document.getElementById("root");
    if (root) root.dataset.compact = String(compact);
  }, [compact]);

  useEffect(() => {
    const root = document.getElementById("root");
    if (root)
      root.style.setProperty("--game-accent", gameAccent(activeGameId ?? ""));
  }, [activeGameId]);

  // Listed so the effect re-runs when the config lands and overwrites a stale
  // pre-paint cache: `ui.theme` is the truth, the persisted store is a cache.
  useEffect(() => {
    if (configTheme) useUiStore.getState().setTheme(configTheme);
  }, [configTheme]);

  useEffect(() => onWindowFocusChange(() => undefined), []);

  return (
    <div
      data-compact={compact}
      className="flex size-full overflow-hidden bg-background"
    >
      {children}
    </div>
  );
}
