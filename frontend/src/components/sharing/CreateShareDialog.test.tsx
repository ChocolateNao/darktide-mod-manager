import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { CreateShareDialog } from "@/components/sharing/CreateShareDialog";
import { t } from "@/lib/i18n";
import { hookPhases } from "@/lib/schemas/mod";
import { useModsStore } from "@/stores/mods.store";
import type { Mod, ModHooks } from "@/types/domain";

vi.mock("@/hooks/queries", () => ({
  useDefaultStunQuery: () => ({ data: [] }),
}));

vi.mock("@/hooks/mutations", () => ({
  useCreateShare: () => ({ mutateAsync: vi.fn(), isPending: false }),
}));

function mod(id: string, overrides: Partial<Mod> = {}): Mod {
  const base: Mod = {
    id,
    name: id.toUpperCase(),
    version: "1.0.0",
    author: "",
    description: "",
    sourceId: "local",
    sourceData: {},
    localPath: `/mods/${id}`,
    installedAt: null,
    updatedAt: null,
    enabled: true,
    external: false,
    priority: 0,
    tags: [],
    issues: [],
    dependencies: [],
    conflictsWith: [],
    hooks: Object.fromEntries(
      hookPhases.map((phase) => [phase, []])
    ) as unknown as ModHooks,
  };

  return { ...base, ...overrides };
}

describe("CreateShareDialog", () => {
  beforeEach(() => {
    useModsStore
      .getState()
      .setMods([mod("alpha"), mod("bravo", { external: true })]);
  });

  it("lists only mods the manager tracks", () => {
    render(<CreateShareDialog open onOpenChange={vi.fn()} />);

    expect(screen.getByText("ALPHA")).toBeInTheDocument();
    expect(screen.queryByText("BRAVO")).not.toBeInTheDocument();
  });

  it("filters the list from the search field", async () => {
    render(<CreateShareDialog open onOpenChange={vi.fn()} />);

    await userEvent
      .setup()
      .type(screen.getByLabelText(t("share.searchMods")), "zzz");

    expect(screen.queryByText("ALPHA")).not.toBeInTheDocument();
    expect(screen.getByText(t("share.noMatches"))).toBeInTheDocument();
  });

  it("edits the link lifetime with the number field", () => {
    render(<CreateShareDialog open onOpenChange={vi.fn()} />);

    expect(
      screen.getByRole("spinbutton", { name: t("share.expiryDays") })
    ).toHaveValue(7);
  });
});
