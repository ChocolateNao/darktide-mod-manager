/**
 * Which notifications the user wants, what the platform can do, and the buffer
 * that merges repeats. The toggles are a UI preference: `domain.Config` has no
 * notification block, and inventing a key the backend cannot see would leave a
 * setting the CLI can never read.
 */

import { create } from "zustand";
import { persist } from "zustand/middleware";

import type { NotifyCategory } from "@/bridge/events";

/** Its own key: two persisted stores on `dmm.ui.v1` would overwrite each
 * other. Versioned, and holds display preferences only — never a path or
 * credential. */
export const PERSISTED_NOTIFICATIONS_KEY = "dmm.notifications.v1";

export type NotificationToggleState = Record<NotifyCategory, boolean>;

const DEFAULT_TOGGLES: NotificationToggleState = {
  install: true,
  updates: true,
  hooks: true,
  cache: false,
  sharing: true,
  errors: true,
};

/** Three updated mods should surface as one notification; the window stays
 * short enough that a genuinely separate event gets its own. */
export const COALESCE_WINDOW_MS = 4_000;

export interface PendingNotification {
  key: string;
  request: {
    title: string;
    body?: string;
    category: NotifyCategory;
    silent?: boolean;
  };
  count: number;
  firstSeenAt: number;
  lastSeenAt: number;
}

export interface NotificationsState {
  toggles: NotificationToggleState;
  /** `null` until the first availability probe returns. */
  availability: boolean | null;
  /** macOS only; `null` elsewhere because the question does not apply. */
  authorized: boolean | null;
  pending: PendingNotification[];
  setToggle: (category: NotifyCategory, enabled: boolean) => void;
  setAvailability: (available: boolean) => void;
  setAuthorized: (authorized: boolean) => void;
  /** Records a notification; `true` means it merged and the caller raises nothing. */
  coalesce(request: PendingNotification["request"]): boolean;
  takeReady(now: number): PendingNotification[];
  clearPending: () => void;
}

/** The merge key: same category and title means the same message. */
function coalesceKey(request: PendingNotification["request"]): string {
  return `${request.category}|${request.title}`;
}

export const useNotificationsStore = create<NotificationsState>()(
  persist(
    (set, get) => ({
      toggles: { ...DEFAULT_TOGGLES },
      availability: null,
      authorized: null,
      pending: [],

      setToggle: (category, enabled) =>
        set((state) => ({
          toggles: { ...state.toggles, [category]: enabled },
        })),
      setAvailability: (availability) => set({ availability }),
      setAuthorized: (authorized) => set({ authorized }),

      coalesce: (request) => {
        const now = Date.now();
        const key = coalesceKey(request);
        const existing = get().pending.find((entry) => entry.key === key);

        if (!existing || now - existing.lastSeenAt > COALESCE_WINDOW_MS) {
          set((state) => ({
            pending: [
              ...state.pending.filter((entry) => entry.key !== key),
              { key, request, count: 1, firstSeenAt: now, lastSeenAt: now },
            ],
          }));
          return false;
        }

        set((state) => ({
          pending: state.pending.map((entry) =>
            entry.key === key
              ? { ...entry, count: entry.count + 1, lastSeenAt: now }
              : entry
          ),
        }));
        return true;
      },

      takeReady: (now) => {
        const due = get().pending.filter(
          (entry) => now - entry.lastSeenAt >= COALESCE_WINDOW_MS
        );
        if (due.length === 0) return [];
        const dueKeys = new Set(due.map((entry) => entry.key));
        set((state) => ({
          pending: state.pending.filter((entry) => !dueKeys.has(entry.key)),
        }));
        return due;
      },

      clearPending: () => set({ pending: [] }),
    }),
    {
      name: PERSISTED_NOTIFICATIONS_KEY,
      partialize: (state) => ({ toggles: state.toggles }),
    }
  )
);

export const selectToggles = (state: NotificationsState) => state.toggles;
export const selectAvailability = (state: NotificationsState) =>
  state.availability;

export function selectCategoryEnabled(
  state: NotificationsState,
  category: NotifyCategory
): boolean {
  return state.toggles[category] !== false && state.availability !== false;
}

export const selectMacAuthorized = (state: NotificationsState) =>
  state.authorized === true;
