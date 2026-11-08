/**
 * Error messages carry the provider's response body as text. The JSON viewer
 * needs that body as its own value, while the sentence around it stays readable,
 * so this file splits the two.
 */

export interface JsonChunk {
  before: string;
  json: unknown;
  after: string;
}

/** Returns the first JSON object or array embedded in text, or null. */
export function extractJson(text: string): JsonChunk | null {
  const start = firstNonSpace(text);
  if (start < 0) return null;

  const whole = parseWhole(text);
  if (whole !== undefined) return { before: "", json: whole, after: "" };

  for (let i = start; i < text.length; i += 1) {
    if (text[i] !== "{" && text[i] !== "[") continue;

    const end = matchingBracket(text, i);
    if (end < 0) continue;

    try {
      return {
        before: text.slice(0, i),
        json: JSON.parse(text.slice(i, end + 1)),
        after: text.slice(end + 1),
      };
    } catch {
      // Not JSON after all; a later brace may be the real start.
    }
  }

  return null;
}

/** Pretty JSON for clipboard and plain-text fallbacks. */
export function prettyJson(value: unknown): string {
  try {
    return JSON.stringify(value, null, 2) ?? String(value);
  } catch {
    return String(value);
  }
}

function firstNonSpace(text: string): number {
  for (let i = 0; i < text.length; i += 1) {
    if (text[i].trim() !== "") return i;
  }

  return -1;
}

function parseWhole(text: string): unknown {
  const trimmed = text.trim();
  if (trimmed[0] !== "{" && trimmed[0] !== "[") return undefined;

  try {
    return JSON.parse(trimmed) as unknown;
  } catch {
    return undefined;
  }
}

/** Finds the closing bracket of the value that starts at start, skipping strings. */
function matchingBracket(text: string, start: number): number {
  const open = text[start];
  const close = open === "{" ? "}" : "]";

  let depth = 0;
  let inString = false;
  let escaped = false;

  for (let i = start; i < text.length; i += 1) {
    const char = text[i];

    if (inString) {
      if (escaped) escaped = false;
      else if (char === "\\") escaped = true;
      else if (char === '"') inString = false;

      continue;
    }

    if (char === '"') {
      inString = true;
      continue;
    }

    if (char === open) depth += 1;
    else if (char === close) {
      depth -= 1;
      if (depth === 0) return i;
    }
  }

  return -1;
}
