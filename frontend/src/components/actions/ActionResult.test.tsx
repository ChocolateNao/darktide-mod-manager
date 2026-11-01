import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { ActionResult } from "@/components/actions/ActionResult";
import type { GameActionResult } from "@/types/domain";

function result(overrides: Partial<GameActionResult> = {}): GameActionResult {
  return {
    success: true,
    message: "",
    output: "",
    data: null,
    ...overrides,
  };
}

describe("ActionResult", () => {
  it("marks a completed action green", () => {
    const { container } = render(<ActionResult result={result()} />);

    expect(screen.getByText("The action finished.")).toBeInTheDocument();
    expect(container.querySelector("svg.text-success")).not.toBeNull();
  });

  it("marks a failed action red", () => {
    const { container } = render(
      <ActionResult
        result={result({ success: false, message: "Exit code 1" })}
      />
    );

    expect(screen.getByText("The action failed.")).toBeInTheDocument();
    expect(screen.getByText("Exit code 1")).toBeInTheDocument();
    expect(container.querySelector("svg.text-destructive")).not.toBeNull();
  });

  it("wraps arbitrary long data instead of widening the dialog", () => {
    const command = "x".repeat(500);

    const { container } = render(
      <ActionResult
        result={result({
          data: { steam_app_id: 550, command, args: ["-applaunch", command] },
        })}
      />
    );

    const data = container.querySelector("pre");

    expect(data).not.toBeNull();
    expect(data).toHaveClass("wrap-anywhere", "whitespace-pre-wrap");
    expect(data?.textContent).toContain(command);
    expect(container.textContent).toContain("steam_app_id");
  });

  it("wraps long command output", () => {
    const output = "y".repeat(500);

    const { container } = render(<ActionResult result={result({ output })} />);

    const pre = container.querySelector("pre");

    expect(pre).toHaveClass("wrap-anywhere", "whitespace-pre-wrap");
    expect(pre?.textContent).toBe(output);
  });

  it("translates a message key and labels its details", () => {
    render(
      <ActionResult
        result={result({
          message: "opened /mods",
          messageKey: "actions.darktide.openModsFolder.opened",
          messageArgs: { path: "/mods" },
          details: [
            {
              labelKey: "actions.darktide.launchGame.command",
              label: "Launch command",
              value: "steam steam://rungameid/1361210",
            },
          ],
        })}
      />
    );

    expect(screen.getByText("Opened /mods.")).toBeInTheDocument();
    expect(screen.getByText("Launch command")).toBeInTheDocument();
    expect(
      screen.getByText("steam steam://rungameid/1361210")
    ).toBeInTheDocument();
  });

  it("keeps Go's English when a key is unknown", () => {
    render(
      <ActionResult
        result={result({
          message: "opened /mods",
          messageKey: "actions.not.translated",
        })}
      />
    );

    expect(screen.getByText("opened /mods")).toBeInTheDocument();
  });

  it("omits the data pane when an action returns none", () => {
    const { container } = render(<ActionResult result={result()} />);

    expect(container.querySelector("pre")).toBeNull();
  });
});
