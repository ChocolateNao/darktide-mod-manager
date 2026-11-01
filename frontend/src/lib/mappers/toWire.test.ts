import { describe, expect, it } from "vitest";

import { toConfigValue, toWireHooks, toWireSearchQuery } from "./toWire";

import { hookPhases } from "@/lib/schemas/mod";
import type { ModHooks, SearchQuery } from "@/types/domain";

const emptyHooks = (): ModHooks =>
  Object.fromEntries(
    hookPhases.map((phase) => [phase, []])
  ) as unknown as ModHooks;

describe("toWireHooks", () => {
  it("sends every phase, so clearing one cannot leave a stale list behind", () => {
    const wire = toWireHooks(emptyHooks());

    expect(Object.keys(wire).sort()).toEqual([...hookPhases].sort());
  });

  it("converts a script to the snake_case keys Go reads", () => {
    const hooks = emptyHooks();
    hooks.pre_install = [
      {
        language: "lua",
        content: "print(1)",
        workingDir: "/tmp/mods",
        env: ["A=1"],
        timeout: 30,
        name: "Refetch ui",
        disabled: true,
      },
    ];

    expect(toWireHooks(hooks).pre_install[0]).toEqual({
      language: "lua",
      content: "print(1)",
      working_dir: "/tmp/mods",
      env: ["A=1"],
      timeout: 30,
      name: "Refetch ui",
      disabled: true,
    });
  });

  it("sends an absent environment as an empty list, not undefined", () => {
    const hooks = emptyHooks();
    hooks.post_update = [{ language: "javascript", content: "" }];

    const wire = toWireHooks(hooks);
    expect(wire.post_update[0].env).toEqual([]);
    expect(wire.post_update[0].working_dir).toBeUndefined();
    expect(wire.post_update[0].timeout).toBeUndefined();
  });

  it("round-trips through the response mapper without losing a script", async () => {
    const [{ mapHooks }, { modHooksWireSchema }] = await Promise.all([
      import("./mod"),
      import("@/lib/schemas/mod"),
    ]);

    const hooks = emptyHooks();
    // Every field, because a round trip is where a hook silently loses its name.
    hooks.pre_enable = [
      {
        language: "lua",
        content: "-- hi",
        workingDir: "/w",
        env: [],
        timeout: 15,
        name: "Refetch ui",
        disabled: true,
      },
    ];

    const parsed = mapHooks(modHooksWireSchema.parse(toWireHooks(hooks)));

    expect(parsed.pre_enable).toEqual(hooks.pre_enable);
  });
});

describe("toWireSearchQuery", () => {
  it("sends the snake_case names the Go struct tags declare", () => {
    const query: SearchQuery = {
      query: "darktide",
      gameVersion: "4.1",
      sortBy: "downloads",
      tags: ["ui"],
      page: 2,
      pageSize: 50,
    };

    expect(toWireSearchQuery(query)).toEqual({
      query: "darktide",
      game_version: "4.1",
      sort_by: "downloads",
      tags: ["ui"],
      page: 2,
      page_size: 50,
    });
  });

  it("sends an empty tag list when none were chosen", () => {
    expect(toWireSearchQuery({ query: "x" }).tags).toEqual([]);
  });
});

describe("toConfigValue", () => {
  it("formats booleans the way strconv.ParseBool reads them", () => {
    expect(toConfigValue(true)).toBe("true");
    expect(toConfigValue(false)).toBe("false");
  });

  it("joins list values, which is how the sources paths key is stored", () => {
    expect(toConfigValue(["/a", "/b"])).toBe("/a,/b");
  });

  it("sends an empty string for an absent value, which clears the key", () => {
    expect(toConfigValue(undefined)).toBe("");
    expect(toConfigValue(null)).toBe("");
  });

  it("passes numbers and text through unchanged", () => {
    expect(toConfigValue(30)).toBe("30");
    expect(toConfigValue("10s")).toBe("10s");
  });
});
