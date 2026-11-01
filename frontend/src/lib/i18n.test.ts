import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

import { t } from "./i18n";

/**
 * Every user-visible string is supposed to go through `t()`, and `t()` returns
 * its own key when it cannot find one. That fallback is what makes a missing
 * message invisible in review and obvious in production, so the only place the
 * gap can be caught cheaply is here: read the sources, collect the keys, and
 * refuse the build when one is not in the table.
 */
// vitest runs with the project root as the working directory, which is the one
// place this path is known to resolve regardless of the jsdom URL shape.
const SOURCE_ROOT = join(process.cwd(), "src");

function sourceFiles(directory: string): string[] {
  return readdirSync(directory).flatMap((entry) => {
    const path = join(directory, entry);
    if (statSync(path).isDirectory()) return sourceFiles(path);
    return path.endsWith(".ts") || path.endsWith(".tsx") ? [path] : [];
  });
}

/**
 * `t("a.b")` and `t(\`a.${x}\`)` are both used; the second names a family.
 *
 * The guard before `t(` matters: without it `parseInt(`, `parseFloat(`,
 * `.split(` and `import(` all end in `t(` and their arguments read as keys.
 * Requiring a dot is the other half, since every key here is namespaced.
 */
function keysUsedIn(path: string): string[] {
  const source = readFileSync(path, "utf8");
  const keys = new Set<string>();
  const pattern = /(?:^|[^\w$.])t\(\s*[`"]([^`"$]*)[`"]/g;

  for (const match of source.matchAll(pattern)) {
    const key = match[1];
    if (key && key.includes(".")) keys.add(key);
  }

  return [...keys];
}

describe("i18n", () => {
  const keys = sourceFiles(SOURCE_ROOT)
    .filter((path) => !path.endsWith(".test.ts") && !path.endsWith(".test.tsx"))
    .flatMap(keysUsedIn)
    .sort();

  it("has no empty key table", () => {
    expect(keys.length).toBeGreaterThan(0);
    expect(t("definitely.not.a.real.key")).toBe("definitely.not.a.real.key");
  });

  it.each(keys)("resolves %s", (key) => {
    expect(t(key)).not.toBe(key);
  });
});
