import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { Button } from "@/components/ui/button";

/** The `loading` contract: spinner, blocked click, unchanging label. */
describe("Button", () => {
  it("spins while loading and says so to assistive tech", () => {
    render(<Button loading>Install</Button>);

    const button = screen.getByRole("button", { name: /install/i });

    expect(button).toBeDisabled();
    expect(button).toHaveAttribute("aria-busy", "true");
    expect(button).toHaveAttribute("data-loading", "true");
    expect(button).toHaveClass("cursor-progress");
    expect(button.querySelector("svg")).not.toBeNull();
  });

  /* A CTA that swapped its label while running changed width and moved what sat beside it, so the label survives. */
  it("keeps its label while loading so the button cannot resize", () => {
    render(<Button loading>Run now</Button>);

    expect(screen.getByRole("button")).toHaveTextContent("Run now");
  });

  it("overlays the spinner instead of adding a column", () => {
    render(<Button loading>Install</Button>);

    const button = screen.getByRole("button");
    const spinner = button.querySelector("svg");
    const label = button.querySelector("span");

    expect(button).toHaveClass("relative");
    expect(spinner).toHaveClass("absolute");
    expect(label).toHaveClass("opacity-0");
  });

  it("is neither busy nor disabled when idle", () => {
    render(<Button>Save</Button>);

    const button = screen.getByRole("button", { name: /save/i });

    expect(button).not.toBeDisabled();
    expect(button).not.toHaveAttribute("aria-busy");
    expect(button).not.toHaveAttribute("data-loading");
  });

  it("blocks clicks while loading", () => {
    const onClick = vi.fn();

    render(
      <Button loading onClick={onClick}>
        Create
      </Button>
    );
    screen.getByRole("button").click();

    expect(onClick).not.toHaveBeenCalled();
  });

  it("still honours a disabled button that is not loading", () => {
    render(<Button disabled>Create</Button>);

    expect(screen.getByRole("button")).toBeDisabled();
  });

  /* Radix's `Slot` clones exactly one child, so a trigger cannot be handed the spinner and its icon at once. */
  it("leaves an asChild trigger child alone", () => {
    render(
      <Button asChild loading>
        <a href="/logs">Open logs</a>
      </Button>
    );

    const link = screen.getByRole("link", { name: /open logs/i });

    expect(link.tagName).toBe("A");
    expect(link.querySelector("svg")).toBeNull();
    expect(link).toHaveTextContent("Open logs");
  });
});
