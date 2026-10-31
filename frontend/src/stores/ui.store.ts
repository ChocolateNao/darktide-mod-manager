/**
 * Where the user is and how the window looks. Theme and density survive a
 * restart as a *pre-paint cache*: `domain.Config` owns the truth, and
 * `adoptConfig` overwrites this slice on load. The cache keeps the first frame
 * dark — an inline script in `index.html` reads it before React exists.
 */

import { create } from "zustand";
import { persist } from "zustand/middleware";

export const VIEWS = ["mods", "add-mod", "share", "settings", "logs"] as const;

export type ViewId = (typeof VIEWS)[number];

export type ThemePreference = "light" | "dark" | "system";

export type ModDrawerTab = "details" | "source" | "hooks" | "files";

export const MOD_DRAWER_TABS: ModDrawerTab[] = [
  "details",
  "source",
  "hooks",
  "files",
];

/** Versioned so a shape change ships as a new key. Preferences only — never a
 * mod id, path, credential or share link. */
export const PERSISTED_UI_KEY = "dmm.ui.v1";

export interface UiState {
  view: ViewId;
  theme: ThemePreference;
  compact: boolean;
  drawerModId: string | null;
  drawerTab: ModDrawerTab;
  paletteOpen: boolean;
  setView: (view: ViewId) => void;
  setTheme: (theme: ThemePreference) => void;
  setCompact: (compact: boolean) => void;
  openDrawer: (modId: string, tab?: ModDrawerTab) => void;
  closeDrawer: () => void;
  setDrawerTab: (tab: ModDrawerTab) => void;
  setPaletteOpen: (open: boolean) => void;
  togglePalette: () => void;
  /** Overwrites the cache with the authoritative config values. */
  adoptConfig: (theme: ThemePreference, compact: boolean) => void;
}

export const useUiStore = create<UiState>()(
  persist(
    (set) => ({
      view: "mods",
      theme: "dark",
      compact: false,
      drawerModId: null,
      drawerTab: "details",
      paletteOpen: false,

      setView: (view) => set({ view }),
      setTheme: (theme) => set({ theme }),
      setCompact: (compact) => set({ compact }),
      openDrawer: (modId, tab) =>
        set({ drawerModId: modId, drawerTab: tab ?? "details" }),
      closeDrawer: () => set({ drawerModId: null }),
      setDrawerTab: (drawerTab) => set({ drawerTab }),
      setPaletteOpen: (paletteOpen) => set({ paletteOpen }),
      togglePalette: () =>
        set((state) => ({ paletteOpen: !state.paletteOpen })),

      adoptConfig: (theme, compact) => set({ theme, compact }),
    }),
    {
      name: PERSISTED_UI_KEY,
      partialize: (state) => ({ theme: state.theme, compact: state.compact }),
    }
  )
);

export function resolveTheme(theme: ThemePreference): "light" | "dark" {
  if (theme === "system") {
    return typeof window !== "undefined" &&
      window.matchMedia?.("(prefers-color-scheme: light)").matches
      ? "light"
      : "dark";
  }
  return theme;
}

export const selectView = (state: UiState) => state.view;
export const selectTheme = (state: UiState) => state.theme;
export const selectCompact = (state: UiState) => state.compact;
export const selectDrawerModId = (state: UiState) => state.drawerModId;
