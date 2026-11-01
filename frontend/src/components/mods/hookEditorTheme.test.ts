import { describe, expect, it } from "vitest";

import { hookApiCompletions } from "./hookEditorTheme";

describe("hookApiCompletions", () => {
  it("lists the dmm surface for both languages", () => {
    for (const language of ["lua", "javascript"] as const) {
      const labels = hookApiCompletions(language).map((item) => item.label);

      expect(labels).toContain("dmm.fs.readFile");
      expect(labels).toContain("dmm.context.mod.path");
    }
  });

  it("offers console only where it exists", () => {
    const js = hookApiCompletions("javascript").map((item) => item.label);
    const lua = hookApiCompletions("lua").map((item) => item.label);

    expect(js).toContain("console.log");
    expect(lua).not.toContain("console.log");
  });
});
