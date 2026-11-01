import { describe, expect, it } from "vitest";

import { mapActionResponse, mapActionResult, mapActions } from "./action";

/** These Go structs carry no JSON tags, so they arrive with Go's field names. */
const actionWire = {
  ID: "darktide:open_workshop",
  GameID: "darktide",
  DisplayName: "Open Workshop",
  Description: "Opens the Steam community page",
  Category: "source",
  Icon: "globe",
  Platform: "universal",
  RequiresGame: false,
  Dangerous: false,
  Options: [
    {
      Name: "workshop_id",
      Type: "string",
      Label: "Workshop id",
      Description: "",
      Placeholder: "123456",
      Choices: null,
      Required: true,
      Default: null,
    },
  ],
};

describe("action mappers", () => {
  it("maps the PascalCase action declaration", () => {
    const [action] = mapActions([actionWire]);

    expect(action.id).toBe("darktide:open_workshop");
    expect(action.gameId).toBe("darktide");
    expect(action.displayName).toBe("Open Workshop");
    expect(action.platform).toBe("universal");
    expect(action.requiresGame).toBe(false);
    expect(action.dangerous).toBe(false);
  });

  it("keeps the snake_case option name, which is what Go looks options up by", () => {
    const [action] = mapActions([actionWire]);
    const [option] = action.options;

    expect(option.name).toBe("workshop_id");
    expect(option.type).toBe("string");
    expect(option.required).toBe(true);
  });

  it("fills an absent choices list with an empty array", () => {
    const [action] = mapActions([actionWire]);

    expect(action.options[0].choices).toEqual([]);
  });

  it("drops the handler function field, not failing on it", () => {
    // `Handler` is a funcptr that never serialises; a stray one must not break us.
    const [action] = mapActions([{ ...actionWire, Handler: "anything" }]);

    expect(action.id).toBe("darktide:open_workshop");
  });

  it("maps a single action from a one-element list", () => {
    expect(mapActionResponse(actionWire).displayName).toBe("Open Workshop");
  });

  it("maps a result, leaving `data` as unknown for the renderer to guard", () => {
    const result = mapActionResult({
      Success: true,
      Message: "Load order is valid",
      Output: "checked 12 entries",
      Data: {
        path: "/mods/mod_load_order.txt",
        present: true,
        entries: [],
        errors: [],
        duplicate: false,
      },
    });

    expect(result.success).toBe(true);
    expect(result.message).toBe("Load order is valid");
    expect(result.output).toBe("checked 12 entries");
    expect(result.data).toMatchObject({ present: true });
  });

  it("rejects a payload missing a declared field", () => {
    const incomplete = { ...actionWire, DisplayName: undefined };

    expect(() => mapActions([incomplete])).toThrow();
  });
});
// A nil Go slice marshals as `null`, which `.default([])` does not cover.
describe("a nil slice on the wire", () => {
  const withNullOptions = {
    ...actionWire,
    Options: null,
  };

  it("reads a null Options as no options", () => {
    const [mapped] = mapActions([withNullOptions]);

    expect(mapped.options).toEqual([]);
    expect(mapped.id).toBe(actionWire.ID);
  });

  it("reads a null Choices on an option as an empty choice list", () => {
    const [mapped] = mapActions([
      {
        ...actionWire,
        Options: [{ ...actionWire.Options[0], Choices: null }],
      },
    ]);

    expect(mapped.options[0].choices).toEqual([]);
  });

  it("still rejects a payload that is missing something it must have", () => {
    expect(() =>
      mapActions([{ ...actionWire, DisplayName: undefined }])
    ).toThrow();
  });
});
