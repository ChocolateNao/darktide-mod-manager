import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ApiKeyPrompt } from "./ApiKeyPrompt";

import { t } from "@/lib/i18n";

const { mutateAsync } = vi.hoisted(() => ({ mutateAsync: vi.fn() }));

vi.mock("@/hooks/mutations", () => ({
  useSetSourceApiKey: () => ({ mutateAsync, isPending: false }),
}));

vi.mock("@/hooks/queries", () => ({
  useSourcesQuery: () => ({ data: [] }),
}));

function renderPrompt() {
  const onSaved = vi.fn();

  render(
    <ApiKeyPrompt
      sourceId="nexus"
      open
      onOpenChange={vi.fn()}
      reason="reason"
      onSaved={onSaved}
    />
  );

  return onSaved;
}

async function saveKey(key: string) {
  const user = userEvent.setup();
  await user.type(screen.getByPlaceholderText(t("apikey.placeholder")), key);
  await user.click(screen.getByRole("button", { name: t("apikey.save") }));
}

describe("ApiKeyPrompt", () => {
  beforeEach(() => {
    mutateAsync.mockReset();
  });

  it("continues the pending action once the key is accepted", async () => {
    mutateAsync.mockResolvedValue(undefined);
    const onSaved = renderPrompt();

    await saveKey("secret");

    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    expect(mutateAsync).toHaveBeenCalledWith({
      sourceId: "nexus",
      apiKey: "secret",
    });
  });

  it("shows the refusal and does not continue when the key is rejected", async () => {
    mutateAsync.mockRejectedValue(new Error("bad key"));
    const onSaved = renderPrompt();

    await saveKey("wrong");

    await waitFor(() =>
      expect(screen.getByText("bad key")).toBeInTheDocument()
    );
    expect(onSaved).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });
});
