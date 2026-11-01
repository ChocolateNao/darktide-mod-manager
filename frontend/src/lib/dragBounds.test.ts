import { describe, expect, it } from "vitest";

import { dragLimits } from "@/lib/dragBounds";

/** The scrolling box the table lives in; all numbers are viewport pixels,
 * the unit `getBoundingClientRect` and a translate are measured in. */
function container(top: number, bottom: number) {
  return { top, bottom };
}

describe("dragLimits", () => {
  it("lets a row travel anywhere inside the container", () => {
    // A 50px row at y=100 in a 0..300 container has 200px of room each way.
    const limits = dragLimits(container(0, 300), { top: 100, height: 50 });

    expect(limits.clamp(-200)).toBe(-100);
    expect(limits.clamp(200)).toBe(150);
    expect(limits.clamp(40)).toBe(40);
    expect(limits.clamp(-40)).toBe(-40);
  });

  it("stops a row at the bottom edge instead of letting it slide out", () => {
    // The last row: 250 + 50 == 300, so it cannot move down at all.
    const limits = dragLimits(container(0, 300), { top: 250, height: 50 });

    expect(limits.clamp(400)).toBe(0);
    expect(limits.clamp(10)).toBe(0);
  });

  it("stops a row at the top edge", () => {
    const limits = dragLimits(container(0, 300), { top: 0, height: 50 });

    expect(limits.clamp(-400)).toBe(0);
    expect(limits.clamp(-10)).toBe(0);
  });

  it("never locks a row still when the list is shorter than the container", () => {
    // The last row already touches the bottom: a naive range gives max 0 and
    // min -250, and dragging up would be refused.
    const limits = dragLimits(container(0, 300), { top: 100, height: 50 });

    expect(limits.min).toBeLessThanOrEqual(0);
    expect(limits.max).toBeGreaterThanOrEqual(0);
  });

  it("still allows upward travel when a row is already at the bottom edge", () => {
    const limits = dragLimits(container(0, 300), { top: 250, height: 50 });

    expect(limits.min).toBe(-250);
    expect(limits.clamp(-250)).toBe(-250);
  });

  it("treats a container offset from the viewport like any other", () => {
    // The table does not start at the top of the window.
    const limits = dragLimits(container(400, 700), { top: 500, height: 50 });

    expect(limits.clamp(-500)).toBe(-100);
    expect(limits.clamp(500)).toBe(150);
  });

  it("clamps to zero, not inverting the range when the row is taller than the container", () => {
    // `clamp` returns nonsense once min > max, so the range is pinned to zero.
    const limits = dragLimits(container(0, 100), { top: 0, height: 400 });

    expect(limits.min).toBeLessThanOrEqual(limits.max);
    expect(limits.clamp(-9999)).toBe(limits.min);
    expect(limits.clamp(9999)).toBe(limits.max);
  });

  it("returns a stable object whose clamp does not depend on call order", () => {
    const limits = dragLimits(container(0, 300), { top: 100, height: 50 });

    expect(limits.clamp(9999)).toBe(150);
    expect(limits.clamp(-9999)).toBe(-100);
    expect(limits.clamp(0)).toBe(0);
    expect(limits.clamp(9999)).toBe(150);
  });
});
