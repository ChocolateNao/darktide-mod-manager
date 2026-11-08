import { describe, expect, it } from "vitest";

import { extractJson, prettyJson } from "@/lib/jsonText";

describe("extractJson", () => {
  it("parses a message that is only JSON", () => {
    const chunk = extractJson('{"status":403}');

    expect(chunk?.json).toEqual({ status: 403 });
    expect(chunk?.before).toBe("");
    expect(chunk?.after).toBe("");
  });

  it("splits the sentence around the body a provider appended", () => {
    const chunk = extractJson(
      'AUTH_FAILED: download is forbidden: {"type":"about:blank","status":403}'
    );

    expect(chunk?.before).toBe("AUTH_FAILED: download is forbidden: ");
    expect(chunk?.json).toEqual({ type: "about:blank", status: 403 });
    expect(chunk?.after).toBe("");
  });

  it("keeps text that follows the body", () => {
    const chunk = extractJson('Refused: {"status":403}. Try the mod page.');

    expect(chunk?.after).toBe(". Try the mod page.");
  });

  it("handles arrays and nested braces", () => {
    const chunk = extractJson('failed: [{"a":{"b":1}},2] done');

    expect(chunk?.json).toEqual([{ a: { b: 1 } }, 2]);
    expect(chunk?.after).toBe(" done");
  });

  it("ignores braces inside strings", () => {
    const chunk = extractJson('failed: {"message":"a } brace"}');

    expect(chunk?.json).toEqual({ message: "a } brace" });
  });

  it("returns null for plain text", () => {
    expect(extractJson("no such mod")).toBeNull();
    expect(extractJson("")).toBeNull();
    expect(extractJson("   ")).toBeNull();
  });

  it("returns null for an unterminated body", () => {
    expect(extractJson('failed: {"status":403')).toBeNull();
  });
});

describe("prettyJson", () => {
  it("indents the value", () => {
    expect(prettyJson({ a: 1 })).toBe('{\n  "a": 1\n}');
  });

  it("renders a value JSON cannot encode", () => {
    expect(prettyJson(undefined)).toBe("undefined");
  });
});
