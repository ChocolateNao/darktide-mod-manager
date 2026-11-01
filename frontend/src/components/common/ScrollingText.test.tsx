import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { ScrollingText } from "@/components/common/ScrollingText";

describe("ScrollingText", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("keeps short text on one copy", () => {
    vi.spyOn(HTMLElement.prototype, "scrollWidth", "get").mockReturnValue(50);
    vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(100);

    render(<ScrollingText>Short description</ScrollingText>);

    expect(screen.getAllByText("Short description")).toHaveLength(1);
  });

  it("duplicates overflowing text so the loop has no gap", () => {
    vi.spyOn(HTMLElement.prototype, "scrollWidth", "get").mockReturnValue(200);
    vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(100);

    render(<ScrollingText>Long description</ScrollingText>);

    expect(screen.getAllByText("Long description")).toHaveLength(2);
  });
});
