import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it } from "vitest";

import { StatusBar } from "@/components/layout/StatusBar";
import { TooltipProvider } from "@/components/ui/tooltip";
import { t } from "@/lib/i18n";
import { useActivityStore } from "@/stores/activity.store";
import { useModsStore } from "@/stores/mods.store";
import type { Mod, ModHooks, ModUpdateInfo } from "@/types/domain";

function mod(id: string, issues: string[] = []): Mod {
  return {
    id,
    name: id.toUpperCase(),
    version: "1.0.0",
    author: "",
    description: "",
    sourceId: "nexus",
    sourceData: {},
    localPath: "",
    dependencies: [],
    conflictsWith: [],
    tags: [],
    issues,
    priority: 0,
    enabled: true,
    external: false,
    installedAt: null,
    updatedAt: null,
    hooks: {} as ModHooks,
  };
}

function update(modId: string): ModUpdateInfo {
  return {
    modId,
    localVersion: "1.0.0",
    remoteVersion: "2.0.0",
    sourceId: "nexus",
    available: true,
  };
}

function renderBar() {
  return render(
    <TooltipProvider>
      <StatusBar />
    </TooltipProvider>
  );
}

describe("StatusBar", () => {
  beforeEach(() => {
    useActivityStore.setState({ entries: [] });
    useModsStore.getState().clearForGameSwitch();
  });

  it("summarises updates by count and names them on hover", async () => {
    useModsStore.getState().setMods([mod("alpha"), mod("bravo")]);
    useModsStore.getState().setUpdates([update("alpha")]);

    const user = userEvent.setup();
    renderBar();

    expect(
      screen.queryByText("ALPHA", { exact: false })
    ).not.toBeInTheDocument();

    await user.hover(
      screen.getByRole("button", {
        name: t("status.updates", { count: "1" }),
      })
    );

    expect(await screen.findByRole("tooltip")).toHaveTextContent("ALPHA");
  });

  it("summarises mod issues with the same hover list", async () => {
    useModsStore
      .getState()
      .setMods([mod("alpha", ["multiple_manifests"]), mod("bravo")]);

    const user = userEvent.setup();
    renderBar();

    await user.hover(
      screen.getByRole("button", {
        name: t("status.issues", { count: "1" }),
      })
    );

    expect(await screen.findByRole("tooltip")).toHaveTextContent(
      t("mods.issue.multipleManifests")
    );
  });

  it("truncates a long list and counts the rest", async () => {
    const ids = Array.from({ length: 12 }, (_, index) => `mod-${index}`);

    useModsStore.getState().setMods(ids.map((id) => mod(id)));
    useModsStore.getState().setUpdates(ids.map(update));

    const user = userEvent.setup();
    renderBar();

    await user.hover(
      screen.getByRole("button", {
        name: t("status.updates", { count: "12" }),
      })
    );

    expect(await screen.findByRole("tooltip")).toHaveTextContent(
      t("status.more", { count: "2" })
    );
  });
});
