import { z } from "zod";

/** `GameAction` has no Go JSON tags, so it arrives PascalCase; a test asserts it. */
export const actionOptionWireSchema = z.object({
  Name: z.string(),
  Type: z.string(),
  Label: z.string().default(""),
  LabelKey: z.string().default(""),
  Description: z.string().default(""),
  DescriptionKey: z.string().default(""),
  Placeholder: z.string().default(""),
  PlaceholderKey: z.string().default(""),
  // A nil Go slice marshals as `null`.
  Choices: z
    .array(z.string())
    .nullish()
    .transform((value) => value ?? []),
  Required: z.boolean().default(false),
  Default: z.unknown().optional(),
});

export const gameActionWireSchema = z.object({
  ID: z.string(),
  GameID: z.string().default(""),
  DisplayName: z.string(),
  DisplayNameKey: z.string().default(""),
  Description: z.string().default(""),
  DescriptionKey: z.string().default(""),
  Category: z.string().default(""),
  CategoryKey: z.string().default(""),
  Icon: z.string().default(""),
  Platform: z.string().default("universal"),
  // A nil Go slice marshals as `null`, so `.default([])` alone would throw.
  Options: z
    .array(actionOptionWireSchema)
    .nullish()
    .transform((value) => value ?? []),
  RequiresGame: z.boolean().default(false),
  Dangerous: z.boolean().default(false),
});

export const gameActionListWireSchema = z.array(gameActionWireSchema);

export const actionDetailWireSchema = z.object({
  LabelKey: z.string().default(""),
  Label: z.string().default(""),
  Value: z.string().default(""),
});

export const gameActionResultWireSchema = z.object({
  Success: z.boolean().default(false),
  Message: z.string().default(""),
  MessageKey: z.string().default(""),
  MessageArgs: z.record(z.string(), z.unknown()).nullish(),
  Details: z.array(actionDetailWireSchema).nullish(),
  Output: z.string().default(""),
  Data: z.unknown().optional(),
});

export const actionOptionTypeSchema = z.enum([
  "string",
  "path",
  "bool",
  "number",
  "select",
]);

/**
 * Builds the zod schema for one action from Go's option declarations, so every
 * action validates through the same `@hookform/resolvers/zod` path.
 *
 * Values stay strings: Go (`internal/actions/options.go`) already coerces them.
 */
export function buildActionOptionSchema(
  options: readonly {
    name: string;
    type: string;
    choices: readonly string[];
    required: boolean;
  }[]
): z.ZodObject<Record<string, z.ZodType>> {
  const shape: Record<string, z.ZodType> = {};

  for (const option of options) {
    let field: z.ZodType;

    switch (option.type) {
      case "bool":
        field = z.boolean();
        break;
      case "select": {
        const choices = option.choices as [string, ...string[]];
        field = choices.length ? z.enum(choices) : z.string();
        break;
      }
      case "number":
        field = z.coerce.number();
        break;
      default:
        field = z.string();
    }

    // Requiring a value here puts the error under the field; whitespace counts as empty.
    shape[option.name] = option.required
      ? field.refine(
          (value) =>
            value !== undefined &&
            value !== null &&
            (typeof value !== "string" || value.trim() !== ""),
          { message: "Required" }
        )
      : field.optional();
  }

  return z.object(shape);
}

export type ActionFormValues = Record<string, unknown>;
