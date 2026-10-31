/**
 * The whole configuration document. `domain.Config` is the truth; the slice
 * hands one normalised object to the settings tabs instead of letting each
 * parse the payload again, and holds the last failure so no form builds its
 * own error plumbing.
 */

import { create } from "zustand";

import type { AppConfig } from "@/types/domain";

export interface ConfigState {
  config: AppConfig | null;
  configPath: string | null;
  loadError: string | null;
  saveError: string | null;
  setConfig: (config: AppConfig) => void;
  setConfigPath: (path: string) => void;
  setLoadError: (message: string | null) => void;
  setSaveError: (message: string | null) => void;
  /** Applies a local patch to the cached copy without a round trip. */
  patchConfig(patch: (config: AppConfig) => AppConfig): void;
}

export const useConfigStore = create<ConfigState>()((set) => ({
  config: null,
  configPath: null,
  loadError: null,
  saveError: null,

  setConfig: (config) => set({ config, loadError: null }),
  setConfigPath: (configPath) => set({ configPath }),
  setLoadError: (loadError) => set({ loadError }),
  setSaveError: (saveError) => set({ saveError }),

  patchConfig: (patch) =>
    set((state) => (state.config ? { config: patch(state.config) } : state)),
}));

export const selectConfig = (state: ConfigState) => state.config;

export function selectActiveGameId(state: ConfigState): string | null {
  return state.config?.activeGame ?? null;
}
