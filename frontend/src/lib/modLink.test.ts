import { describe, expect, it } from "vitest";

import { modFamily, sameModPage } from "@/lib/modLink";

describe("modFamily", () => {
  it("collapses the files of one Nexus page", () => {
    expect(modFamily("nexus-8-7495")).toBe("nexus-8");
    expect(modFamily("nexus-8-191620")).toBe("nexus-8");
  });

  it("leaves ids without a page part alone", () => {
    expect(modFamily("github-owner-repo")).toBe("github-owner-repo");
    expect(modFamily("nexus-8")).toBe("nexus-8");
    expect(modFamily("nexus-abc-123")).toBe("nexus-abc-123");
  });
});

describe("sameModPage", () => {
  it("matches two files of one mod page", () => {
    expect(sameModPage("nexus-8-7495", "nexus-8-191620")).toBe(true);
  });

  it("separates different mod pages", () => {
    expect(sameModPage("nexus-8-7495", "nexus-1450-8149575")).toBe(false);
  });
});
