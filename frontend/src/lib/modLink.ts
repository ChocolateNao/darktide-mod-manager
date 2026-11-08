/** The launcher scheme or the "nxm link" chat form. */
const NXM_LINK =
  /^(?:nxm:\/\/|nxm link )[\w.-]+\/(?:mods\/)?(\d+)\/(?:files\/)?(\d+)\/?(?:\?.*)?$/i;

export function isNxmLink(value: string): boolean {
  return NXM_LINK.test(value.trim());
}

/** The manager keys a Nexus file as `nexus-<mod>-<file>`, which is what ParseModLink builds. */
export function nxmModId(value: string): string | null {
  const match = NXM_LINK.exec(value.trim());

  return match ? `nexus-${match[1]}-${match[2]}` : null;
}

/**
 * The mod page an id belongs to. Nexus ids name one file, so two files of the
 * same page share a family; other sources have no page part and stay their own.
 */
export function modFamily(id: string): string {
  const trimmed = id.trim();
  const match = /^nexus-(\d+)-\d+$/.exec(trimmed);

  return match ? `nexus-${match[1]}` : trimmed;
}

/** Whether two ids name the same mod page, even when they name different files. */
export function sameModPage(a: string, b: string): boolean {
  return modFamily(a) === modFamily(b);
}
