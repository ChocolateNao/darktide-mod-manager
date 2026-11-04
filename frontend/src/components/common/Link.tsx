import type { ReactNode } from "react";

import { openLink } from "@/bridge/dialogs";
import { cn } from "@/lib/utils";

/** External text link: the desktop browser owns the destination, not the webview. */
interface LinkProps {
  href: string;
  children: ReactNode;
  className?: string;
}

export function Link({ href, children, className }: LinkProps) {
  return (
    <a
      href={href}
      target="_blank"
      rel="noreferrer"
      className={cn(
        "rounded-sm underline underline-offset-2 transition-colors hover:text-primary focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none",
        className
      )}
      onClick={(event) => {
        event.preventDefault();
        openLink(href);
      }}
    >
      {children}
    </a>
  );
}
