import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type * as BridgeClient from "@/bridge/client";
import { InstallConflictDialog } from "@/components/mods/InstallConflictDialog";
import { t } from "@/lib/i18n";
import { useInstallPromptStore } from "@/stores/installPrompt.store";

const install = vi.hoisted(() => vi.fn());
const update = vi.hoisted(() => vi.fn());
const importLink = vi.hoisted(() => vi.fn());
const updatePending = vi.hoisted(() => ({ value: false }));

vi.mock("@/hooks/mutations", () => ({
  updateNeedsModPage: (error: unknown) =>
    String(error).includes("DOWNLOAD_FAILED"),
  useImportLink: () => ({ isPending: false, mutate: importLink }),
  useInstallMod: () => ({ isPending: false, mutate: install }),
  useUpdateMod: () => ({ isPending: updatePending.value, mutate: update }),
}));

vi.mock("@/bridge/client", async (importOriginal) => ({
  ...(await importOriginal<typeof BridgeClient>()),
  openModSource: vi.fn(async () => undefined),
}));

const api = await import("@/bridge/client");

function request(
  direction: "update" | "downgrade" | "reinstall",
  mode: "update" | "replace" | "link"
) {
  useInstallPromptStore.getState().request({
    mode,
    link: mode === "link" ? "nxm://darktide/mods/1/files/2" : undefined,
    sourceId: "nexus",
    modId: "alpha",
    name: "Alpha",
    currentVersion: "1.0.0",
    targetVersion: "2.0.0",
    direction,
  });
}

describe("InstallConflictDialog", () => {
  beforeEach(() => {
    install.mockReset();
    update.mockReset();
    importLink.mockReset();
    updatePending.value = false;
    vi.mocked(api.openModSource).mockClear();
    useInstallPromptStore.getState().clear();
  });

  it("installs with replacement when the user accepts an update", async () => {
    request("update", "replace");

    render(<InstallConflictDialog />);

    expect(
      screen.getByText(t("mods.installConflictTitle", { name: "Alpha" }))
    ).toBeInTheDocument();

    await userEvent
      .setup()
      .click(screen.getByRole("button", { name: t("mods.updateAction") }));

    expect(install).toHaveBeenCalledWith(
      { sourceId: "nexus", modId: "alpha", force: true },
      expect.anything()
    );
    expect(update).not.toHaveBeenCalled();
  });

  it("runs the update path for the status tag", async () => {
    request("update", "update");

    render(<InstallConflictDialog />);

    await userEvent
      .setup()
      .click(screen.getByRole("button", { name: t("mods.updateAction") }));

    expect(update).toHaveBeenCalledWith(
      { id: "alpha", quiet: true },
      expect.anything()
    );
    expect(install).not.toHaveBeenCalled();
  });

  it("shows that the update is running", () => {
    updatePending.value = true;
    request("update", "update");

    render(<InstallConflictDialog />);

    expect(
      screen.getByRole("button", { name: t("mods.updateAction") })
    ).toHaveAttribute("aria-busy", "true");
  });

  it("keeps the failure and the mod-page offer in the dialog", async () => {
    request("update", "update");
    update.mockImplementation((_variables, options) => {
      options?.onError?.(new Error("DOWNLOAD_FAILED: premium required"));
    });

    const user = userEvent.setup();
    render(<InstallConflictDialog />);

    await user.click(
      screen.getByRole("button", { name: t("mods.updateAction") })
    );

    expect(await screen.findByText(t("mods.updateFailed"))).toBeInTheDocument();
    expect(
      screen.getByText("DOWNLOAD_FAILED: premium required")
    ).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: t("mods.openPage") }));

    expect(api.openModSource).toHaveBeenCalledWith("alpha", "source");
  });

  it("renders a JSON body in the failure block", async () => {
    request("update", "update");
    update.mockImplementation((_variables, options) => {
      options?.onError?.(
        new Error(
          'DOWNLOAD_FAILED: forbidden: {"status":403,"title":"Forbidden"}'
        )
      );
    });

    const user = userEvent.setup();
    render(<InstallConflictDialog />);

    await user.click(
      screen.getByRole("button", { name: t("mods.updateAction") })
    );

    expect(await screen.findByText(/"status"/)).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: t("common.copy") })
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: t("mods.openPage") })
    ).toBeInTheDocument();
  });

  it("labels an older target as a downgrade", () => {
    request("downgrade", "replace");

    render(<InstallConflictDialog />);

    expect(
      screen.getByRole("button", { name: t("mods.downgradeAction") })
    ).toBeInTheDocument();
  });

  it("re-imports the link so its grant survives the replacement", async () => {
    request("update", "link");

    render(<InstallConflictDialog />);

    await userEvent
      .setup()
      .click(screen.getByRole("button", { name: t("mods.updateAction") }));

    expect(importLink).toHaveBeenCalledWith(
      { link: "nxm://darktide/mods/1/files/2", force: true },
      expect.anything()
    );
    expect(install).not.toHaveBeenCalled();
  });
});
