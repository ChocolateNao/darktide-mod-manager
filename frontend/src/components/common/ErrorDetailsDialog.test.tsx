import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type * as Dialogs from "@/bridge/dialogs";
import { copyToClipboard } from "@/bridge/dialogs";
import { ErrorDetailsDialog } from "@/components/common/ErrorDetailsDialog";
import { t } from "@/lib/i18n";
import { useErrorDetailsStore } from "@/stores/errorDetails.store";

vi.mock("@/bridge/dialogs", async (importOriginal) => ({
  ...(await importOriginal<typeof Dialogs>()),
  copyToClipboard: vi.fn(async () => true),
}));

const message =
  'AUTH_FAILED: download is forbidden: {"status":403,"title":"Forbidden"}';

describe("ErrorDetailsDialog", () => {
  beforeEach(() => {
    vi.mocked(copyToClipboard).mockClear();
    useErrorDetailsStore.setState({ details: null });
  });

  it("renders the sentence and the JSON body separately", async () => {
    render(<ErrorDetailsDialog />);

    act(() => {
      useErrorDetailsStore.getState().open({ title: "Update failed", message });
    });

    expect(await screen.findByText("Update failed")).toBeInTheDocument();
    expect(
      screen.getByText("AUTH_FAILED: download is forbidden:")
    ).toBeInTheDocument();
    expect(screen.getByText(/"status"/)).toBeInTheDocument();
  });

  it("copies the whole message and confirms it", async () => {
    render(<ErrorDetailsDialog />);

    act(() => {
      useErrorDetailsStore.getState().open({ title: "Update failed", message });
    });

    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: t("common.copy") }));

    expect(copyToClipboard).toHaveBeenCalledWith(message);
    expect(
      await screen.findByRole("button", { name: t("common.copied") })
    ).toBeInTheDocument();
  });

  it("shows plain text without inventing a document", () => {
    render(<ErrorDetailsDialog />);

    act(() => {
      useErrorDetailsStore.getState().open({
        title: "Update failed",
        message: "the network is down",
      });
    });

    expect(screen.getByText("the network is down")).toBeInTheDocument();
    expect(screen.getByText(t("errors.detailsHint"))).toBeInTheDocument();
  });
});
