import { createElement, useState } from "react";

import { gameIcon } from "@/lib/gameAccent";
import { isCustomIcon } from "@/lib/railIcon";
import { cn } from "@/lib/utils";

/** A game's rail mark: the custom image when one is stored, otherwise its icon choice. */
interface GameIconProps {
  gameId?: string | null;
  icon?: string | null;
  className?: string;
}

export function GameIcon({ gameId, icon, className }: GameIconProps) {
  const [failedIcon, setFailedIcon] = useState<string | null>(null);

  if (isCustomIcon(icon) && failedIcon !== icon) {
    return (
      <img
        src={icon ?? ""}
        alt=""
        onError={() => setFailedIcon(icon ?? null)}
        className={cn("shrink-0 object-contain", className)}
      />
    );
  }

  return createElement(gameIcon(gameId, icon), { className });
}
