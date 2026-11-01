/**
 * The layer above the raw bridge calls: the desktop gestures the UI needs and
 * the Wails runtime owns — clipboard, window chrome, file drop — plus the policy
 * that turns a cancel into "leave the field alone" and a reset into "use the
 * platform default".
 */

import {
  BrowserOpenURL,
  CanResolveFilePaths,
  ClipboardGetText,
  ClipboardSetText,
  OnFileDrop,
  OnFileDropOff,
  WindowSetDarkTheme,
  WindowSetLightTheme,
} from "wailsjs/runtime/runtime";

import { chooseDirectory, chooseFile, revealDirectory } from "@/bridge/client";
import { BridgeError } from "@/types/errors";

/**
 * Sentinel meaning "the user wants the platform default". It mirrors
 * `commands.DefaultPathRequest` in `internal/commands/path.go`; that Go const is
 * unexported, so the value is written down in both places. A NUL cannot occur in
 * a path, which makes it safe as a request no real directory could ever mean.
 */
export const DEFAULT_PATH_REQUEST = "\u0000";

export type DirectoryChoice =
  | { kind: "chosen"; path: string }
  | { kind: "cleared" }
  | { kind: "cancelled" };

/**
 * Whether the Wails runtime is reachable at all.
 *
 * Wails injects `window.runtime` only into its own webview, so the bundle
 * served by `vite dev` has none and every runtime call throws. Desktop gestures
 * are optional chrome and must not take the window down.
 */
export function hasWailsRuntime(): boolean {
  return (
    typeof window !== "undefined" &&
    typeof (window as { runtime?: unknown }).runtime !== "undefined"
  );
}

/**
 * A folder field with three outcomes. Cancelling must leave the form untouched,
 * and "reset to the platform default" must be expressible even though `""`
 * already means the default in every `domain.Config` path field.
 */
export async function pickDirectory(start: string): Promise<DirectoryChoice> {
  const [path, cleared] = await chooseDirectory(start);
  if (cleared) return { kind: "cleared" };
  if (path === "") return { kind: "cancelled" };
  return { kind: "chosen", path };
}

export async function pickFile(
  start: string,
  patterns: string[] = []
): Promise<string | null> {
  const path = await chooseFile(start, patterns);
  return path === "" ? null : path;
}

/** Shows a folder in the file manager — a different job from choosing one. */
export const revealPath = revealDirectory;

/** Hands a link to the browser, with a tab fallback for a plain browser dev server. */
export function openLink(url: string): void {
  if (hasWailsRuntime()) {
    BrowserOpenURL(url);
    return;
  }

  window.open(url, "_blank", "noopener,noreferrer");
}

/** Copies text and reports whether the OS accepted it, so a UI can show a hint. */
export async function copyToClipboard(text: string): Promise<boolean> {
  try {
    return await ClipboardSetText(text);
  } catch {
    return false;
  }
}

/** Reads the clipboard; an unavailable clipboard yields an empty string. */
export async function pasteFromClipboard(): Promise<string> {
  try {
    return (await ClipboardGetText()) ?? "";
  } catch {
    return "";
  }
}

/* Wails v2.14 has no runtime focus call, so the webview's own events stand in,
   and starting focused keeps a silent Linux webview from raising notifications. */
let windowFocused = true;
const focusListeners = new Set<(focused: boolean) => void>();
let focusWired = false;

function ensureFocusWiring(): void {
  if (focusWired || typeof window === "undefined") return;
  focusWired = true;
  windowFocused = document.hasFocus();
  window.addEventListener("focus", () => setWindowFocused(true));
  window.addEventListener("blur", () => setWindowFocused(false));
}

function setWindowFocused(focused: boolean): void {
  if (windowFocused === focused) return;
  windowFocused = focused;
  for (const listener of focusListeners) listener(focused);
}

export function isWindowFocused(): boolean {
  ensureFocusWiring();
  return windowFocused;
}

export function onWindowFocusChange(
  listener: (focused: boolean) => void
): () => void {
  ensureFocusWiring();
  focusListeners.add(listener);
  return () => focusListeners.delete(listener);
}

/** Keeps the native title bar in step with the app theme instead of staying white. */
export function applyWindowTheme(dark: boolean): void {
  if (!hasWailsRuntime()) return;
  if (dark) {
    WindowSetDarkTheme();
    return;
  }
  WindowSetLightTheme();
}

export interface DroppedPaths {
  x: number;
  y: number;
  paths: string[];
}

/**
 * Window-level file drop, so the overlay appears wherever the user happens to
 * be. `useDropTarget` is off: the overlay is ours to draw.
 */
export function onFileDrop(handler: (drop: DroppedPaths) => void): () => void {
  // No runtime means no drops; the caller still gets the unsubscribe shape it
  // expects, so a mount effect does not need to know which window it is in.
  if (!hasWailsRuntime()) return () => undefined;
  OnFileDrop((x, y, paths) => handler({ x, y, paths }), false);
  return () => OnFileDropOff();
}

/* Asked once and cached: older WebKitGTK builds cannot turn a dropped `File`
   into a path, and a runtime that cannot be asked answers "no" for vite dev. */
let resolvablePaths: boolean | null = null;

export function canResolveDroppedPaths(): boolean {
  if (resolvablePaths === null) {
    resolvablePaths = hasWailsRuntime() && CanResolveFilePaths();
  }
  return resolvablePaths;
}

export function describeBridgeFailure(error: unknown): string {
  if (error instanceof BridgeError) return error.message;
  if (error instanceof Error) return error.message;
  return String(error);
}
