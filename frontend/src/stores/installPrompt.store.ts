import { create } from "zustand";

export type InstallDirection = "update" | "downgrade" | "reinstall";

export interface PendingInstall {
  /** `update` runs the update path; `replace` reinstalls from the source. */
  mode: "update" | "replace" | "link";
  /** Set for `link`: re-importing keeps the link's one-time grant. */
  link?: string;
  sourceId: string;
  modId: string;
  name: string;
  currentVersion: string;
  targetVersion: string;
  direction: InstallDirection;
}

interface InstallPromptState {
  pending: PendingInstall | null;
  request: (pending: PendingInstall) => void;
  clear: () => void;
}

/** A request any install entry point can raise; one dialog mounted at the root answers it. */
export const useInstallPromptStore = create<InstallPromptState>()((set) => ({
  pending: null,
  request: (pending) => set({ pending }),
  clear: () => set({ pending: null }),
}));
