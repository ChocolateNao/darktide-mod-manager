import { describe, expect, it } from "vitest";

import { goDuration } from "@/components/common/DurationField";

/*
 * Go's duration syntax is what `domain.Duration` parses and what a hand-edited
 * config.json holds, so the field renders exactly that.
 */
describe("goDuration", () => {
  it("writes zero as the bare word Go accepts", () => {
    expect(goDuration(0)).toBe("0s");
  });

  it("picks the largest unit that divides evenly", () => {
    expect(goDuration(3600)).toBe("1h");
    expect(goDuration(7200)).toBe("2h");
    expect(goDuration(60)).toBe("1m");
    expect(goDuration(90)).toBe("1m30s");
    expect(goDuration(45)).toBe("45s");
  });

  it("combines hours and minutes when neither alone is whole", () => {
    expect(goDuration(3660)).toBe("1h1m");
    expect(goDuration(5400)).toBe("1h30m");
  });

  it("keeps a value it cannot express in whole minutes, not rounding it", () => {
    // 3661 is 1h1m1s; dropping the seconds would silently shorten the timeout
    // every time the form was saved.
    expect(goDuration(3661)).toBe("1h1m1s");
  });

  it("answers zero for anything that is not a positive duration", () => {
    expect(goDuration(0)).toBe("0s");
    expect(goDuration(-5)).toBe("0s");
    expect(goDuration(Number.NaN)).toBe("0s");
  });
});
