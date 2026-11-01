import { useEffect, useRef } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";
import { Search } from "lucide-react";

import { EmptyState } from "@/components/common/Surfaces";
import { Input } from "@/components/ui/input";
import { formatClock, logFeature } from "@/lib/format";
import { t } from "@/lib/i18n";
import type { LogLine } from "@/types/domain";

const LEVEL_TEXT: Record<string, string> = {
  trace: "text-muted-foreground",
  debug: "text-muted-foreground",
  info: "text-info",
  warn: "text-warning",
  error: "text-destructive",
  fatal: "text-destructive",
};

/**
 * The one list in this app that grows without bound: the tail appends for as
 * long as the app is open. Virtualising it keeps the DOM node count flat, which
 * is why every append stays cheap.
 */
interface LogTailProps {
  lines: LogLine[];
  filter: string;
  onPick: (line: LogLine) => void;
}

export function LogTail({ lines, filter, onPick }: LogTailProps) {
  const viewport = useRef<HTMLDivElement>(null);
  const pinned = useRef(true);

  const virtualiser = useVirtualizer({
    count: lines.length,
    getScrollElement: () => viewport.current,
    estimateSize: () => 20,
    overscan: 20,
  });

  // Following the tail is only correct while the user is at the bottom; scrolling
  // up is a request to stop moving it under them.
  useEffect(() => {
    if (pinned.current) virtualiser.scrollToIndex(lines.length - 1);
  }, [lines.length, virtualiser]);

  if (lines.length === 0) {
    return <EmptyState icon={Search} title={t("logs.empty")} />;
  }

  return (
    <div
      ref={viewport}
      className="h-full overflow-auto font-mono text-xs"
      onScroll={(event) => {
        const element = event.currentTarget;
        pinned.current =
          element.scrollHeight - element.scrollTop - element.clientHeight < 24;
      }}
    >
      <div
        style={{ height: virtualiser.getTotalSize(), position: "relative" }}
        data-selectable
      >
        {virtualiser.getVirtualItems().map((row) => {
          const line = lines[row.index];
          if (!line) return null;

          const needle = filter.toLowerCase();
          const matches =
            needle === "" ||
            line.message.toLowerCase().includes(needle) ||
            (line.caller ?? "").toLowerCase().includes(needle) ||
            logFeature(line).toLowerCase().includes(needle);

          return (
            <button
              key={row.key}
              type="button"
              className={`absolute left-0 flex w-full items-baseline gap-2 px-2 text-left hover:bg-muted/40 ${
                matches ? "" : "opacity-30"
              }`}
              style={{
                height: row.size,
                transform: `translateY(${row.start}px)`,
              }}
              onClick={() => onPick(line)}
            >
              <span className="shrink-0 text-muted-foreground">
                {formatClock(new Date(line.timestamp))}
              </span>
              <span
                className={`w-12 shrink-0 uppercase ${LEVEL_TEXT[line.level] ?? ""}`}
              >
                {line.level}
              </span>
              <span className="w-40 shrink-0 truncate text-muted-foreground">
                {logFeature(line)}
              </span>
              <span className="min-w-0 flex-1 truncate">{line.message}</span>
            </button>
          );
        })}
      </div>
    </div>
  );
}

interface LogFilterProps {
  value: string;
  onChange: (value: string) => void;
}

export function LogFilter({ value, onChange }: LogFilterProps) {
  return (
    <Input
      value={value}
      onChange={(event) => onChange(event.target.value)}
      placeholder={t("logs.showFrom")}
      className="w-56"
      aria-label={t("logs.showFrom")}
    />
  );
}
