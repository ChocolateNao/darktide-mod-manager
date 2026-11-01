/**
 * Presentation helpers. Every number, byte count and duration the user reads
 * goes through this file so that formatting decisions are made once — the
 * alternative is `toFixed(1) + " MB"` appearing in nine components and drifting.
 */

const BYTE_UNITS = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"] as const;

/** The subsystem that wrote a line: its `op` field, falling back to the caller. */
export function logFeature(line: {
  fields?: Record<string, unknown>;
  caller?: string;
}): string {
  const op = line.fields?.op;
  if (typeof op === "string" && op.trim()) return op;

  return line.caller?.trim() || "app";
}

/** Formats a byte count for humans. Share sizes need one decimal at most. */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return "—";
  if (bytes < 1024) return `${Math.round(bytes)} B`;

  let value = bytes;
  let unitIndex = 0;
  while (value >= 1024 && unitIndex < BYTE_UNITS.length - 1) {
    value /= 1024;
    unitIndex += 1;
  }
  // One decimal beats sub-kilobyte noise like "18.07 MiB"; `Number` drops the
  // trailing zero, since "1.0 KiB" reads like a measurement that cannot decide.
  return `${Number(value.toFixed(value < 10 ? 1 : 0))} ${BYTE_UNITS[unitIndex]}`;
}

export function formatCount(
  count: number,
  singular: string,
  plural = `${singular}s`
): string {
  return `${count} ${count === 1 ? singular : plural}`;
}

/** A grouped number on its own, for where the noun comes from a translated string. */
export function formatNumber(value: number): string {
  return new Intl.NumberFormat().format(value);
}

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

/** Formats a wall-clock distance the way a log line wants it: "3 minutes ago". */
export function formatRelativeTime(
  from: Date | null,
  now = new Date()
): string {
  if (!from) return "—";

  const elapsed = now.getTime() - from.getTime();
  if (Number.isNaN(elapsed)) return "—";
  if (elapsed < 0) return "just now";

  if (elapsed < MINUTE) return "just now";
  if (elapsed < HOUR) return `${Math.floor(elapsed / MINUTE)}m ago`;
  if (elapsed < DAY) return `${Math.floor(elapsed / HOUR)}h ago`;
  if (elapsed < 30 * DAY) return `${Math.floor(elapsed / DAY)}d ago`;
  return formatDateTime(from);
}

/** Formats an absolute timestamp in the user's locale; 24h clock to keep logs sortable. */
export function formatDateTime(at: Date | null): string {
  if (!at || Number.isNaN(at.getTime())) return "—";
  return new Intl.DateTimeFormat(undefined, {
    year: "numeric",
    month: "short",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  }).format(at);
}

/** Formats just the clock part, which is all the log tail column has room for. */
export function formatClock(at: Date | null): string {
  if (!at || Number.isNaN(at.getTime())) return "—";
  return new Intl.DateTimeFormat(undefined, {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  }).format(at);
}

const DURATION_UNITS: Array<[label: string, seconds: number]> = [
  ["d", 86400],
  ["h", 3600],
  ["m", 60],
  ["s", 1],
];

/**
 * Parses a `time.Duration` string ("30s", "1h30m", "0") into seconds. The Go
 * side marshals `domain.Duration` as its `String()`; `Number.parseInt` would
 * read "1h30m" as one second.
 */
export function parseDurationSeconds(text: string): number | null {
  const trimmed = text.trim();
  // An empty field returns null so the caller refuses the save instead of
  // writing a timeout of no time at all.
  if (trimmed === "") return null;
  if (trimmed === "0") return 0;

  const sign = trimmed.startsWith("-") ? -1 : 1;
  const rest = trimmed.replace(/^[+-]/, "");
  if (rest === "") return null;

  // A bare number is seconds in Go's own notation ("90").
  const bare = /^(\d+(?:\.\d+)?)$/.exec(rest);
  if (bare) return sign * Number(bare[1]);

  const unitSeconds = new Map(
    DURATION_UNITS.map(([label, size]) => [label, size])
  );
  const pattern = /(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)/gy;
  let seconds = 0;
  let consumed = 0;
  for (const match of rest.matchAll(pattern)) {
    const size = unitSeconds.get(match[2]);
    if (size === undefined) return null;
    seconds += Number(match[1]) * size;
    consumed += match[0].length;
  }
  // Anything the pattern did not consume means the input is malformed;
  // null lets the caller fall back and refuse a broken save.
  if (consumed !== rest.length) return null;
  return sign * seconds;
}

/** Renders seconds as the shortest sensible duration: "45s", "10m", "2h", "7d". */
export function formatDurationSeconds(seconds: number | null): string {
  if (seconds === null || !Number.isFinite(seconds)) return "—";
  if (seconds === 0) return "none";

  const sign = seconds < 0 ? "-" : "";
  let rest = Math.abs(seconds);
  const parts: string[] = [];
  for (const [label, size] of DURATION_UNITS) {
    const value = Math.floor(rest / size);
    if (value > 0) {
      parts.push(`${value}${label}`);
      rest -= value * size;
    }
    if (parts.length === 2) break;
  }
  return sign + (parts.join(" ") || "0s");
}

/** Percentage as a whole number, clamped — a progress bar should never read "103%". */
export function formatPercent(fraction: number): string {
  if (!Number.isFinite(fraction)) return "0%";
  return `${Math.round(Math.min(Math.max(fraction, 0), 1) * 100)}%`;
}
