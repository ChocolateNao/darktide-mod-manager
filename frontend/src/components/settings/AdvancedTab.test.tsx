import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type * as BridgeClient from "@/bridge/client";
import { DeleteDataDialog } from "@/components/settings/AdvancedTab";

vi.mock("@/bridge/client", async (importOriginal) => ({
  ...(await importOriginal<typeof BridgeClient>()),
  deleteData: vi.fn(),
}));

const api = await import("@/bridge/client");

function renderDialog() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });

  interface WrapperProps {
    children: ReactNode;
  }

  function Wrapper({ children }: WrapperProps) {
    return (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    );
  }

  return render(<DeleteDataDialog open onOpenChange={() => {}} />, {
    wrapper: Wrapper,
  });
}

beforeEach(() => {
  vi.mocked(api.deleteData).mockReset().mockResolvedValue(null);
});

describe("DeleteDataDialog", () => {
  it("will not continue until something is selected", async () => {
    const user = userEvent.setup();
    renderDialog();

    const next = screen.getByRole("button", { name: "Continue" });
    expect(next).toBeDisabled();

    await user.click(screen.getAllByRole("checkbox")[1]);
    expect(next).toBeEnabled();
  });

  it("deletes only the selected data after the final confirmation", async () => {
    const user = userEvent.setup();
    renderDialog();

    await user.click(screen.getAllByRole("checkbox")[1]);
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await user.click(
      screen.getByRole("button", { name: "Delete selected data" })
    );

    expect(api.deleteData).toHaveBeenCalledWith(false, true);
  });
});
