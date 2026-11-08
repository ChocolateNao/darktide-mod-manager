import { type ReactNode, useMemo } from "react";

import { prettyJson } from "@/lib/jsonText";
import { cn } from "@/lib/utils";

/**
 * Read-only JSON with syntax colour. Long lines wrap inside the padded box, so
 * a provider's one-line body cannot run into the frame.
 */
interface JsonViewerProps {
  value: unknown;
  className?: string;
}

const TOKEN =
  /("(?:\\.|[^"\\])*")(\s*:)?|\b(true|false|null)\b|(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)/g;

const KEY = "text-sky-600 dark:text-sky-400";
const STRING = "text-emerald-600 dark:text-emerald-400";
const LITERAL = "text-fuchsia-600 dark:text-fuchsia-400";
const NUMBER = "text-amber-600 dark:text-amber-400";
const PUNCTUATION = "text-muted-foreground";

function highlightLine(line: string, index: number) {
  const nodes: ReactNode[] = [];
  let last = 0;

  for (const match of line.matchAll(TOKEN)) {
    const start = match.index ?? 0;
    if (start > last) nodes.push(line.slice(last, start));

    const [text, quoted, colon, literal] = match;

    if (quoted) {
      nodes.push(
        <span key={`${index}-${start}-key`} className={colon ? KEY : STRING}>
          {quoted}
        </span>
      );

      if (colon) {
        nodes.push(
          <span key={`${index}-${start}-colon`} className={PUNCTUATION}>
            {colon}
          </span>
        );
      }
    } else {
      nodes.push(
        <span
          key={`${index}-${start}-value`}
          className={literal ? LITERAL : NUMBER}
        >
          {text}
        </span>
      );
    }

    last = start + text.length;
  }

  if (last < line.length) nodes.push(line.slice(last));

  return nodes;
}

export function JsonViewer({ value, className }: JsonViewerProps) {
  const lines = useMemo(() => prettyJson(value).split("\n"), [value]);

  return (
    <pre
      data-selectable
      className={cn(
        "max-h-80 min-w-0 overflow-auto rounded-md border bg-muted/40 p-3 font-mono text-xs leading-relaxed wrap-anywhere whitespace-pre-wrap",
        className
      )}
    >
      <code>
        {lines.map((line, index) => (
          <span key={index} className="block">
            {highlightLine(line, index)}
          </span>
        ))}
      </code>
    </pre>
  );
}
