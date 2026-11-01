const MAX_ICON_BYTES = 512 * 1024;
const ACCEPTED_FORMATS = "PNG, JPEG, WebP, GIF, ICO or SVG";

export type RailIconErrorCode = "format" | "size" | "read";

/** Why a picked rail icon was rejected, in a form the settings UI can translate. */
export class RailIconError extends Error {
  constructor(
    readonly code: RailIconErrorCode,
    message: string
  ) {
    super(message);
    this.name = "RailIconError";
  }
}

/** A stored custom icon is a data URL; anything else is a built-in choice or blank. */
export function isCustomIcon(value?: string | null): boolean {
  return /^data:image\//.test(value ?? "");
}

/**
 * Reads a browser-picked image into a data URL, after checking the bytes rather
 * than trusting the extension or the file dialog's MIME guess.
 */
export async function readRailIcon(file: File): Promise<string> {
  if (file.size > MAX_ICON_BYTES) {
    throw new RailIconError(
      "size",
      `rail icons must be ${MAX_ICON_BYTES / 1024} KB or smaller`
    );
  }

  let bytes: Uint8Array;
  try {
    bytes = new Uint8Array(await file.arrayBuffer());
  } catch {
    throw new RailIconError("read", "the image could not be read");
  }

  const mime = detectMime(bytes);
  if (!mime) {
    throw new RailIconError("format", `expected ${ACCEPTED_FORMATS}`);
  }

  return `data:${mime};base64,${toBase64(bytes)}`;
}

function detectMime(bytes: Uint8Array): string | null {
  if (startsWith(bytes, [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])) {
    return "image/png";
  }
  if (startsWith(bytes, [0xff, 0xd8, 0xff])) {
    return "image/jpeg";
  }
  if (startsWith(bytes, [0x00, 0x00, 0x01, 0x00])) {
    return "image/x-icon";
  }

  const head = ascii(bytes, 0, 12);
  if (head.startsWith("GIF87a") || head.startsWith("GIF89a")) {
    return "image/gif";
  }
  if (head.startsWith("RIFF") && head.slice(8, 12) === "WEBP") {
    return "image/webp";
  }
  if (looksLikeSvg(bytes)) {
    return "image/svg+xml";
  }

  return null;
}

function looksLikeSvg(bytes: Uint8Array): boolean {
  let text = new TextDecoder()
    .decode(bytes)
    .replace(/^\uFEFF/, "")
    .trim();

  text = text.replace(/^<\?xml[^>]*\?>/i, "").trim();

  while (true) {
    if (text.startsWith("<!--")) {
      const end = text.indexOf("-->");
      if (end === -1) return false;
      text = text.slice(end + 3).trim();
      continue;
    }

    if (/^<!DOCTYPE/i.test(text)) {
      const end = text.indexOf(">");
      if (end === -1) return false;
      text = text.slice(end + 1).trim();
      continue;
    }

    break;
  }

  return /^<svg[\s>]/i.test(text);
}

function startsWith(bytes: Uint8Array, prefix: number[]): boolean {
  return prefix.every((byte, index) => bytes[index] === byte);
}

function ascii(bytes: Uint8Array, start: number, end: number): string {
  return String.fromCharCode(...bytes.subarray(start, end));
}

function toBase64(bytes: Uint8Array): string {
  let binary = "";
  const chunk = 0x8000;

  for (let index = 0; index < bytes.length; index += chunk) {
    binary += String.fromCharCode(...bytes.subarray(index, index + chunk));
  }

  return btoa(binary);
}
