/**
 * Per-game presentation. `pkg/domain` is deliberately presentation-free, so the
 * colour and the icon live here. Everything else falls back to the app accent
 * and the neutral pad icon, so a freshly installed game plugin is legible.
 */

import { Gamepad2, Ghost, type LucideIcon, Orbit, Swords } from "lucide-react";

export const APP_ACCENT = "#e0a458";

export const GAME_ICON_CHOICES: {
  id: string;
  icon: LucideIcon;
  label: string;
}[] = [
  { id: "gamepad", icon: Gamepad2, label: "Gamepad" },
  { id: "swords", icon: Swords, label: "Swords" },
  { id: "orbit", icon: Orbit, label: "Orbit" },
  { id: "ghost", icon: Ghost, label: "Ghost" },
];

const GAME_ACCENTS: Record<string, string> = {
  darktide: "#e0a458",
};

const GAME_ICONS: Record<string, LucideIcon> = {
  darktide: Swords,
};

export function gameAccent(gameId: string | null | undefined): string {
  if (!gameId) return APP_ACCENT;
  return GAME_ACCENTS[gameId] ?? APP_ACCENT;
}

/**
 * The icon a game is shown with. A game the user has personalised wins over the
 * built-in default, so `chosen` is the user's selection from the Games tab and
 * may name a choice the defaults do not carry.
 */
export function gameIcon(
  gameId: string | null | undefined,
  chosen?: string | null
): LucideIcon {
  if (chosen) {
    const picked = GAME_ICON_CHOICES.find((choice) => choice.id === chosen);
    if (picked) return picked.icon;
  }
  if (gameId && GAME_ICONS[gameId]) return GAME_ICONS[gameId];
  return Gamepad2;
}
