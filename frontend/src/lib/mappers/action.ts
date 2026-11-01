import type { z } from "zod";

import {
  gameActionListWireSchema,
  gameActionResultWireSchema,
} from "@/lib/schemas/action";
import type { GameAction, GameActionResult } from "@/types/domain";

type GameActionWire = z.infer<typeof gameActionListWireSchema.element>;

const mapAction = (wire: GameActionWire): GameAction => ({
  id: wire.ID,
  gameId: wire.GameID,
  displayName: wire.DisplayName,
  displayNameKey: wire.DisplayNameKey || undefined,
  description: wire.Description,
  descriptionKey: wire.DescriptionKey || undefined,
  category: wire.Category,
  categoryKey: wire.CategoryKey || undefined,
  icon: wire.Icon,
  platform: wire.Platform,
  requiresGame: wire.RequiresGame,
  dangerous: wire.Dangerous,
  options: wire.Options.map((option) => ({
    // Go looks options up by this snake_case name; the CLI uses a dashed form.
    name: option.Name,
    type: option.Type as GameAction["options"][number]["type"],
    label: option.Label,
    labelKey: option.LabelKey || undefined,
    description: option.Description,
    descriptionKey: option.DescriptionKey || undefined,
    placeholder: option.Placeholder,
    placeholderKey: option.PlaceholderKey || undefined,
    choices: option.Choices ?? [],
    required: option.Required,
    default: option.Default,
  })),
});

export const mapActions = (raw: unknown): GameAction[] =>
  gameActionListWireSchema.parse(raw).map(mapAction);

export const mapActionResponse = (raw: unknown): GameAction =>
  mapAction(gameActionListWireSchema.parse([raw])[0]);

export const mapActionResult = (raw: unknown): GameActionResult => {
  const wire = gameActionResultWireSchema.parse(raw);

  return {
    success: wire.Success,
    message: wire.Message,
    messageKey: wire.MessageKey || undefined,
    messageArgs: messageArgs(wire.MessageArgs),
    details: wire.Details?.map((detail) => ({
      labelKey: detail.LabelKey || undefined,
      label: detail.Label,
      value: detail.Value,
    })),
    output: wire.Output,
    data: wire.Data,
  };
};

function messageArgs(
  args: Record<string, unknown> | null | undefined
): Record<string, string | number> | undefined {
  if (!args) return undefined;

  return Object.fromEntries(
    Object.entries(args)
      .filter(
        ([, value]) => typeof value === "string" || typeof value === "number"
      )
      .map(([key, value]) => [key, value as string | number])
  );
}

export type { GameActionWire };
