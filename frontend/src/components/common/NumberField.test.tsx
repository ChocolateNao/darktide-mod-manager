import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { NumberField } from "@/components/common/NumberField";

describe("NumberField", () => {
  it("puts the steppers after the input, increase above decrease", () => {
    render(<NumberField value={5} onChange={() => undefined} />);

    const [increase, decrease] = screen.getAllByRole("button");

    expect(increase).toHaveAttribute("aria-label", "increase");
    expect(decrease).toHaveAttribute("aria-label", "decrease");
    expect(increase.parentElement?.parentElement).toContainElement(
      screen.getByRole("spinbutton")
    );
  });

  it("converts a time unit to seconds for the caller", () => {
    const onChange = vi.fn();

    render(<NumberField value={120} onChange={onChange} unit="minutes" />);

    const input = screen.getByRole("spinbutton");
    expect(input).toHaveValue(2);
    expect(screen.getByText("min")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "increase" }));
    expect(onChange).toHaveBeenLastCalledWith(180);

    fireEvent.change(input, { target: { value: "4" } });
    expect(onChange).toHaveBeenLastCalledWith(240);
  });

  it("clamps the displayed value at the bounds", () => {
    const onChange = vi.fn();

    render(
      <NumberField
        value={60}
        onChange={onChange}
        unit="seconds"
        min={30}
        max={90}
        step={15}
      />
    );

    fireEvent.change(screen.getByRole("spinbutton"), {
      target: { value: "500" },
    });

    expect(onChange).toHaveBeenLastCalledWith(90);
  });
});
