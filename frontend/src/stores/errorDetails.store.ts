import { create } from "zustand";

/**
 * The error a toast sent its Details button to. Kept outside the toast because
 * Sonner unmounts the toast as soon as it dismisses, which is exactly when the
 * dialog is still open.
 */
export interface ErrorDetails {
  title: string;
  message: string;
}

interface ErrorDetailsState {
  details: ErrorDetails | null;
  open: (details: ErrorDetails) => void;
  close: () => void;
}

export const useErrorDetailsStore = create<ErrorDetailsState>()((set) => ({
  details: null,
  open: (details) => set({ details }),
  close: () => set({ details: null }),
}));
