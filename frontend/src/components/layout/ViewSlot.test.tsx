import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type * as BridgeClient from "@/bridge/client";
import { ViewSlot } from "@/components/layout/ViewSlot";
import { useActionsStore } from "@/stores/actions.store";
import type { GameAction } from "@/types/domain";

/** Every sibling of the overlays is stubbed; the question is which overlays exist at the root. */
vi.mock("@/components/layout/GameRail", () => ({ GameRail: () => null }));
vi.mock("@/components/layout/Header", () => ({ Header: () => null }));
vi.mock("@/components/layout/NavTabs", () => ({ NavTabs: () => null }));
vi.mock("@/components/layout/StatusBar", () => ({ StatusBar: () => null }));
vi.mock("@/components/mods/ModDrawer", () => ({ ModDrawer: () => null }));
vi.mock("@/components/sources/DropOverlay", () => ({
  DropOverlay: () => null,
}));
vi.mock("@/components/ui/sonner", () => ({ Toaster: () => null }));
vi.mock("@/components/common/Surfaces", () => ({ EmptyState: () => null }));
vi.mock("@/views/ModsView", () => ({ default: () => null }));
vi.mock("@/views/AddModView", () => ({ default: () => null }));

vi.mock("@/bridge/client", async (importOriginal) => ({
  ...(await importOriginal<typeof BridgeClient>()),
  listAvailableActions: vi.fn(async () => []),
}));

const action: GameAction = {
  id: "darktide:validate_loadorder",
  gameId: "darktide",
  displayName: "Validate load order",
  description: "Reads mod_load_order.txt.",
  category: "Diagnostics",
  icon: "list",
  platform: "any",
  options: [],
  requiresGame: true,
  dangerous: false,
};

function renderSlot() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });

  return render(
    <QueryClientProvider client={client}>
      <ViewSlot ready />
    </QueryClientProvider>
  );
}

/** The run dialog used to be exported and never rendered, so a click did nothing; the mount is the fix worth testing. */
describe("ViewSlot overlays", () => {
  beforeEach(() => {
    useActionsStore.getState().close();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("mounts the action run dialog", async () => {
    renderSlot();

    expect(screen.queryByRole("dialog")).toBeNull();

    useActionsStore.getState().open({ action, presetArgs: {} });

    expect(await screen.findByRole("dialog")).toHaveTextContent(
      action.displayName
    );
  });

  it("closes the run dialog when its request goes away", async () => {
    renderSlot();
    useActionsStore.getState().open({ action, presetArgs: {} });
    await screen.findByRole("dialog");

    useActionsStore.getState().close();

    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });
});
