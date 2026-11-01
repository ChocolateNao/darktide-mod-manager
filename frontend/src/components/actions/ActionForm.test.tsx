import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { ActionForm } from "@/components/actions/ActionForm";
import type { ActionOption, GameAction } from "@/types/domain";

function actionWith(options: ActionOption[]): GameAction {
  return {
    id: "test:action",
    gameId: "test",
    displayName: "Test action",
    description: "",
    category: "",
    icon: "",
    platform: "any",
    options,
    requiresGame: false,
    dangerous: false,
  };
}

describe("ActionForm layout", () => {
  it("keeps one error slot under a field before and after validation", async () => {
    const onSubmit = vi.fn();
    const action = actionWith([
      {
        name: "target",
        type: "string",
        label: "Target",
        description: "",
        placeholder: "",
        choices: [],
        required: true,
      },
    ]);

    render(<ActionForm action={action} onSubmit={onSubmit} />);

    const slot = document.querySelector('[data-slot="field-error"]');
    expect(slot).toHaveClass("min-h-4");
    expect(slot).toBeEmptyDOMElement();

    fireEvent.click(screen.getByRole("button", { name: /run test action/i }));

    await waitFor(() => expect(slot).toHaveTextContent("Required"));
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("gives a select the same width whatever option is chosen", () => {
    const action = actionWith([
      {
        name: "mode",
        type: "select",
        label: "Mode",
        description: "",
        placeholder: "Choose",
        choices: ["short", "a much longer choice"],
        required: true,
      },
    ]);

    render(<ActionForm action={action} onSubmit={() => undefined} />);

    expect(screen.getByRole("combobox")).toHaveClass("w-full");
  });
});
