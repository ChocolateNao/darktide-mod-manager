import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { GameIcon } from "@/components/common/GameIcon";

describe("GameIcon", () => {
  it("renders a stored custom image", () => {
    const { container } = render(
      <GameIcon gameId="darktide" icon="data:image/png;base64,iVBORw0KGgo=" />
    );

    expect(container.querySelector("img")).toHaveAttribute(
      "src",
      "data:image/png;base64,iVBORw0KGgo="
    );
  });

  it("renders a built-in choice as an icon", () => {
    const { container } = render(<GameIcon gameId="darktide" icon="swords" />);

    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("svg")).not.toBeNull();
  });
});
