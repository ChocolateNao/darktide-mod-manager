import { describe, expect, it } from "vitest";

import { buildActionOptionSchema } from "@/lib/schemas/action";
import type { ActionOption } from "@/types/domain";

function option(overrides: Partial<ActionOption>): ActionOption {
  return {
    name: "field",
    type: "string",
    label: "Field",
    description: "",
    placeholder: "",
    required: false,
    choices: [],
    ...overrides,
  };
}

describe("buildActionOptionSchema", () => {
  it("renders a text option as a string", () => {
    const schema = buildActionOptionSchema([option({ name: "workshop_id" })]);
    expect(schema.safeParse({ workshop_id: "1234" }).success).toBe(true);
    expect(schema.safeParse({ workshop_id: 1234 }).success).toBe(false);
  });

  it("keeps a boolean instead ofaccepting the CLI's loose strings", () => {
    const schema = buildActionOptionSchema([
      option({ name: "force", type: "bool" }),
    ]);
    expect(schema.safeParse({ force: true }).success).toBe(true);
    expect(schema.safeParse({ force: "true" }).success).toBe(false);
  });

  it("coerces a number field, because the control is an input not a stepper", () => {
    const schema = buildActionOptionSchema([
      option({ name: "count", type: "number" }),
    ]);
    expect(schema.safeParse({ count: "42" }).success).toBe(true);
    expect(schema.safeParse({ count: "not a number" }).success).toBe(false);
  });

  it("restricts a select to the choices the backend declared", () => {
    const schema = buildActionOptionSchema([
      option({ name: "mode", type: "select", choices: ["fast", "safe"] }),
    ]);
    expect(schema.safeParse({ mode: "fast" }).success).toBe(true);
    expect(schema.safeParse({ mode: "reckless" }).success).toBe(false);
  });

  it("falls back to free text when a select declares no choices", () => {
    const schema = buildActionOptionSchema([
      option({ name: "mode", type: "select" }),
    ]);
    expect(schema.safeParse({ mode: "anything" }).success).toBe(true);
  });

  it("rejects a required option with no value", () => {
    const schema = buildActionOptionSchema([
      option({ name: "workshop_id", required: true }),
    ]);
    expect(schema.safeParse({ workshop_id: "1234" }).success).toBe(true);
    expect(schema.safeParse({ workshop_id: "" }).success).toBe(false);
    expect(schema.safeParse({ workshop_id: "   " }).success).toBe(false);
  });

  it("accepts an empty form when nothing is required", () => {
    const schema = buildActionOptionSchema([option({ name: "note" })]);
    expect(schema.safeParse({ note: "" }).success).toBe(true);
  });
});
