import { useEffect } from "react";

import { useUiStore } from "@/stores/ui.store";

/**
 * Registered once at the app root so every view gets them: a keyboard-only
 * palette, and text fields keep their keystrokes — typing "k" in a search box
 * must not open the palette (Escape excepted).
 */
export function useGlobalShortcuts(): void {
  const setPaletteOpen = useUiStore((state) => state.setPaletteOpen);
  const togglePalette = useUiStore((state) => state.togglePalette);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      // Ignore anything the browser or an OS accelerator already claimed.
      if (event.defaultPrevented) return;
      if (event.altKey) return;

      const typing = isTextEntry(event.target);

      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        togglePalette();
        return;
      }

      if (event.key === "Escape") {
        // Radix handles Escape for its own overlays; this only covers the
        // case where an overlay is open that Radix does not own.
        if (useUiStore.getState().paletteOpen) setPaletteOpen(false);
        return;
      }

      // "/" is the search shortcut everywhere except inside a field, where it is
      // just a character the user wants.
      if (typing) return;
      if (event.key === "/") event.preventDefault();
    };

    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [setPaletteOpen, togglePalette]);
}

function isTextEntry(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  if (target.isContentEditable) return true;

  const tag = target.tagName;
  return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT";
}
