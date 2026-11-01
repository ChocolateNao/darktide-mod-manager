import { hookPhases } from "@/lib/schemas/mod";
import type {
  HookPhase,
  HookScript,
  ModHooks,
  SearchQuery,
} from "@/types/domain";

type WireHookScript = {
  language: string;
  content: string;
  working_dir?: string;
  env: string[];
  timeout?: number;
  name?: string;
  disabled?: boolean;
};

/**
 * Request-direction mappers. Posting camelCase where Go reads snake_case would
 * silently lose fields.
 */
const toWireHookScript = (script: HookScript): WireHookScript => ({
  language: script.language,
  content: script.content,
  working_dir: script.workingDir,
  env: script.env ?? [],
  timeout: script.timeout,
  name: script.name,
  disabled: script.disabled ?? false,
});

export const toWireHooks = (
  hooks: ModHooks
): Record<HookPhase, WireHookScript[]> =>
  Object.fromEntries(
    hookPhases.map((phase: HookPhase) => [
      phase,
      (hooks[phase] ?? []).map(toWireHookScript),
    ])
  ) as Record<HookPhase, WireHookScript[]>;

export const toWireSearchQuery = (query: SearchQuery) => ({
  query: query.query,
  game_version: query.gameVersion,
  sort_by: query.sortBy,
  tags: query.tags ?? [],
  page: query.page,
  page_size: query.pageSize,
});

/**
 * `SetConfigValue` takes every setting as a string and interprets it per key —
 * `strconv.ParseBool` for booleans, `strconv.Atoi` for numbers — so formatting
 * lives here.
 */
export const toConfigValue = (value: unknown): string => {
  if (typeof value === "boolean") return value ? "true" : "false";
  if (Array.isArray(value)) return value.join(",");
  if (value === undefined || value === null) return "";
  return String(value);
};
