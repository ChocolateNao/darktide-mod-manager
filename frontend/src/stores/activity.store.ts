/**
 * Work in flight, plus the log tail — both ephemeral, append-only, and driven
 * by the event bus. The tail is a bounded ring because the Logs view streams
 * for an hour with no upper bound on the line count.
 */

import { create } from "zustand";

import type { ActivityKind, HookConsoleEvent } from "@/bridge/events";
import type { LogLine } from "@/types/domain";

/** One console entry, either a hook run milestone or a line the script printed. */
export interface HookConsoleEntry {
  id: number;
  at: number;
  kind: "start" | "output" | "done" | "failed" | "dropped" | "info" | "error";
  modId?: string;
  phase?: string;
  type?: string;
  text?: string;
  count?: number;
}

export interface ActivityEntry {
  id: string;
  kind: ActivityKind;
  label: string;
  modId?: string;
  /** `null` means indeterminate. */
  progress: number | null;
  detail?: string;
  state: "running" | "done" | "failed";
  failure?: string;
  startedAt: number;
}

export interface ActivityState {
  entries: ActivityEntry[];
  logLines: LogLine[];
  hookConsole: HookConsoleEntry[];
  /** Per-row timestamp of the last applied progress write, for the repaint floor. */
  lastProgressAt: Record<string, number>;
  begin: (entry: {
    kind: ActivityKind;
    label: string;
    modId?: string;
  }) => string;
  setProgress: (id: string, fraction: number | null) => void;
  setDetail: (id: string, detail: string) => void;
  finish: (id: string) => void;
  fail: (id: string, reason: string) => void;
  find: (criteria: {
    kind: ActivityKind;
    modId?: string;
  }) => string | undefined;
  prune: () => void;
  appendLog: (line: LogLine) => void;
  replaceLogTail: (lines: LogLine[]) => void;
  clearLog: () => void;
  appendHookConsole: (event: HookConsoleEvent) => void;
  appendHookConsoleLocal: (
    modId: string,
    kind: "info" | "error",
    text: string
  ) => void;
}

// 100 ms floor: a 1000 Hz producer would otherwise cost a thousand writes a
// second. Progress only — a state change must land immediately.
const PROGRESS_FLOOR_MS = 100;

/** Roughly an hour of chatty logging before the oldest lines fall off. */
const LOG_RING_LIMIT = 5_000;

/** A run prints at most 200 lines; this keeps several runs of history. */
const HOOK_CONSOLE_LIMIT = 1_000;

/** The disk snapshot and the live stream describe the same record. */
const lineKey = (line: LogLine) =>
  `${line.timestamp}\u0000${line.level}\u0000${line.message}\u0000${line.caller ?? ""}\u0000${line.modId ?? ""}`;

const lineTime = (line: LogLine) => Date.parse(line.timestamp) || 0;

let sequence = 0;
let consoleSequence = 0;

function nextId(): string {
  sequence += 1;
  return `activity-${sequence}`;
}

function consoleEntry(entry: Omit<HookConsoleEntry, "id">): HookConsoleEntry {
  consoleSequence += 1;
  return { id: consoleSequence, ...entry };
}

export const useActivityStore = create<ActivityState>()((set, get) => ({
  entries: [],
  logLines: [],
  hookConsole: [],
  lastProgressAt: {},

  begin: (entry) => {
    const id = nextId();
    set((state) => ({
      entries: [
        ...state.entries,
        {
          id,
          kind: entry.kind,
          label: entry.label,
          modId: entry.modId,
          progress: null,
          state: "running",
          startedAt: Date.now(),
        },
      ],
    }));
    return id;
  },

  setProgress: (id, fraction) => {
    const now = Date.now();
    const last = get().lastProgressAt[id] ?? 0;
    if (now - last < PROGRESS_FLOOR_MS) return;
    set((state) => ({
      lastProgressAt: { ...state.lastProgressAt, [id]: now },
      entries: state.entries.map((entry) =>
        entry.id === id ? { ...entry, progress: fraction } : entry
      ),
    }));
  },

  setDetail: (id, detail) =>
    set((state) => ({
      entries: state.entries.map((entry) =>
        entry.id === id ? { ...entry, detail } : entry
      ),
    })),

  finish: (id) =>
    set((state) => ({
      entries: state.entries.map((entry) =>
        entry.id === id ? { ...entry, state: "done", progress: 1 } : entry
      ),
    })),

  fail: (id, failure) =>
    set((state) => ({
      entries: state.entries.map((entry) =>
        entry.id === id ? { ...entry, state: "failed", failure } : entry
      ),
    })),

  find: (criteria) => {
    const entry = get().entries.find(
      (candidate) =>
        candidate.state === "running" &&
        candidate.kind === criteria.kind &&
        (criteria.modId === undefined || candidate.modId === criteria.modId)
    );
    return entry?.id;
  },

  // Done and failed rows linger briefly so a fast sequence of operations is still
  // readable, then disappear; the log tail keeps the permanent record.
  prune: () =>
    set((state) => {
      const cutoff = Date.now() - 4_000;
      return {
        entries: state.entries.filter(
          (entry) => entry.state === "running" || entry.startedAt > cutoff
        ),
      };
    }),

  appendLog: (line) =>
    set((state) => {
      const logLines = [...state.logLines, line];
      return {
        logLines:
          logLines.length > LOG_RING_LIMIT
            ? logLines.slice(-LOG_RING_LIMIT)
            : logLines,
      };
    }),

  replaceLogTail: (logLines) =>
    set((state) => {
      const seen = new Set(logLines.map(lineKey));
      const live = state.logLines.filter((line) => !seen.has(lineKey(line)));
      // The cached snapshot predates records logged while the view was closed.
      const merged = [...logLines, ...live].sort(
        (left, right) => lineTime(left) - lineTime(right)
      );
      return { logLines: merged.slice(-LOG_RING_LIMIT) };
    }),

  clearLog: () => set({ logLines: [] }),

  appendHookConsole: (event) =>
    set((state) => {
      const at = Date.now();
      const base = {
        at,
        modId: event.modId,
        phase: event.phase,
        type: event.type,
      };
      const entries: HookConsoleEntry[] =
        event.kind === "output"
          ? (event.lines ?? []).map((text) =>
              consoleEntry({ ...base, kind: "output", text })
            )
          : event.kind === "dropped"
            ? [
                consoleEntry({
                  ...base,
                  kind: "dropped",
                  count: event.dropped ?? 0,
                }),
              ]
            : [consoleEntry({ ...base, kind: event.kind, text: event.error })];

      if (event.kind === "output" && event.dropped) {
        entries.push(
          consoleEntry({ ...base, kind: "dropped", count: event.dropped })
        );
      }

      return {
        hookConsole: [...state.hookConsole, ...entries].slice(
          -HOOK_CONSOLE_LIMIT
        ),
      };
    }),

  appendHookConsoleLocal: (modId, kind, text) =>
    set((state) => ({
      hookConsole: [
        ...state.hookConsole,
        consoleEntry({ at: Date.now(), modId, kind, text }),
      ].slice(-HOOK_CONSOLE_LIMIT),
    })),
}));

/**
 * The raw entry list. A selector that allocates returns a new reference on
 * every read, which zustand compares with `Object.is`, so React would re-render
 * forever. Components filter the plain array while rendering instead.
 */
export const selectEntries = (state: ActivityState) => state.entries;
export const selectLogLines = (state: ActivityState) => state.logLines;
export const selectHookConsole = (state: ActivityState) => state.hookConsole;

export function isRunning(entry: ActivityEntry): boolean {
  return entry.state === "running";
}

/** The newest in-flight row for one mod, which is what a row spinner shows. */
export function selectProgressFor(
  state: ActivityState,
  modId: string
): ActivityEntry | null {
  return (
    state.entries.find(
      (entry) => entry.state === "running" && entry.modId === modId
    ) ?? null
  );
}
