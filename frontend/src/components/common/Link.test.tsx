import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { Link } from "@/components/common/Link";

const openLink = vi.hoisted(() => vi.fn());

vi.mock("@/bridge/dialogs", () => ({ openLink }));

describe("Link", () => {
  it("renders underlined text and hands the browser the destination", () => {
    render(<Link href="https://example.com/mod">Read the docs</Link>);

    const link = screen.getByRole("link", { name: "Read the docs" });
    expect(link).toHaveAttribute("href", "https://example.com/mod");
    expect(link).toHaveClass("underline");
    expect(link).toHaveClass("hover:text-primary");

    fireEvent.click(link);

    expect(openLink).toHaveBeenCalledWith("https://example.com/mod");
  });
});
