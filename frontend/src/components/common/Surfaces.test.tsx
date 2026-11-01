import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import { FieldRow } from "@/components/common/Surfaces";
import { TooltipProvider } from "@/components/ui/tooltip";

function renderRow(details?: string) {
  return render(
    <TooltipProvider>
      <FieldRow label="Theme" details={details}>
        <input aria-label="theme" />
      </FieldRow>
    </TooltipProvider>
  );
}

describe("FieldRow", () => {
  it("renders no details trigger when details are omitted", () => {
    renderRow();

    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  it("shows the provided details in a tooltip on hover", async () => {
    const user = userEvent.setup();
    const details = "Applies on the next launch.";

    renderRow(details);

    await user.hover(screen.getByRole("button", { name: details }));

    expect(await screen.findByRole("tooltip")).toHaveTextContent(details);
  });
});
