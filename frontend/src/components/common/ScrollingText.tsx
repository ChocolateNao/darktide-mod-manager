import { type ReactNode, useEffect, useRef, useState } from "react";

import { cn } from "@/lib/utils";

/**
 * Keeps text on one line; a description wider than its slot loops like a
 * ticker and pauses under the pointer.
 */
interface ScrollingTextProps {
  children: ReactNode;
  className?: string;
}

export function ScrollingText({ children, className }: ScrollingTextProps) {
  const viewport = useRef<HTMLSpanElement>(null);
  const copy = useRef<HTMLSpanElement>(null);
  const [scrolling, setScrolling] = useState(false);

  useEffect(() => {
    const check = () => {
      const box = viewport.current;
      const text = copy.current;

      if (box && text) setScrolling(text.scrollWidth > box.clientWidth);
    };

    check();

    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(check);
    if (viewport.current) observer.observe(viewport.current);

    return () => observer.disconnect();
  }, [children]);

  return (
    <span
      ref={viewport}
      className={cn(
        "group/marquee block overflow-hidden whitespace-nowrap",
        className
      )}
    >
      <span
        className={cn(
          "flex w-max",
          scrolling &&
            "group-hover/marquee:paused motion-safe:animate-[marquee_18s_linear_infinite]"
        )}
      >
        <span ref={copy} className="pr-8">
          {children}
        </span>
        {scrolling ? (
          <span aria-hidden className="pr-8">
            {children}
          </span>
        ) : null}
      </span>
    </span>
  );
}
