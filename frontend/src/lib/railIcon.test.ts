import { describe, expect, it } from "vitest";

import { isCustomIcon, readRailIcon } from "@/lib/railIcon";

function fileOf(bytes: Uint8Array | string, name: string, type = "") {
  return new File([bytes as BlobPart], name, { type });
}

const encoder = new TextEncoder();

describe("readRailIcon", () => {
  it.each([
    {
      name: "png",
      file: new File(
        [new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])],
        "icon.png",
        { type: "image/png" }
      ),
      mime: "image/png",
    },
    {
      name: "jpeg",
      file: fileOf(new Uint8Array([0xff, 0xd8, 0xff, 0xe0]), "icon.jpg"),
      mime: "image/jpeg",
    },
    {
      name: "ico",
      file: fileOf(new Uint8Array([0x00, 0x00, 0x01, 0x00]), "icon.ico"),
      mime: "image/x-icon",
    },
    {
      name: "gif",
      file: fileOf(encoder.encode("GIF89a"), "icon.gif"),
      mime: "image/gif",
    },
    {
      name: "webp",
      file: fileOf(
        new Uint8Array([
          0x52, 0x49, 0x46, 0x46, 0x00, 0x00, 0x00, 0x00, 0x57, 0x45, 0x42,
          0x50,
        ]),
        "icon.webp"
      ),
      mime: "image/webp",
    },
    {
      name: "svg",
      file: fileOf(
        encoder.encode(
          '<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"></svg>'
        ),
        "icon.svg"
      ),
      mime: "image/svg+xml",
    },
  ])("accepts $name bytes even when the file type is blank", async (test) => {
    const url = await readRailIcon(test.file);

    expect(url.startsWith(`data:${test.mime};base64,`)).toBe(true);
    expect(isCustomIcon(url)).toBe(true);
  });

  it("rejects bytes that are not one of the supported formats", async () => {
    const file = fileOf(
      encoder.encode("not an image"),
      "icon.png",
      "image/png"
    );

    await expect(readRailIcon(file)).rejects.toMatchObject({ code: "format" });
  });

  it("rejects an image larger than the stored size budget", async () => {
    const file = fileOf(new Uint8Array(512 * 1024 + 1), "big.png", "image/png");

    await expect(readRailIcon(file)).rejects.toMatchObject({ code: "size" });
  });

  it("does not treat a built-in choice as a custom image", () => {
    expect(isCustomIcon("swords")).toBe(false);
    expect(isCustomIcon("")).toBe(false);
    expect(isCustomIcon(undefined)).toBe(false);
  });
});
