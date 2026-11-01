import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import type { UserEvent } from "@testing-library/user-event";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type * as BridgeClient from "@/bridge/client";
import {
  ActionDialogHost,
  ActionPalette,
} from "@/components/actions/ActionPalette";
import { useActionsStore } from "@/stores/actions.store";
import { useGamesStore } from "@/stores/games.store";
import { useUiStore } from "@/stores/ui.store";
import type { GameAction, GameActionResult } from "@/types/domain";

vi.mock("@/bridge/client", async (importOriginal) => ({
  ...(await importOriginal<typeof BridgeClient>()),
  listAvailableActions: vi.fn(),
  executeGameAction: vi.fn(),
}));

const api = await import("@/bridge/client");

const loadOrder: GameAction = {
  id: "darktide:validate_loadorder",
  gameId: "darktide",
  displayName: "Validate load order",
  description: "Reads mod_load_order.txt and reports duplicates.",
  category: "Diagnostics",
  icon: "list",
  platform: "any",
  options: [],
  requiresGame: true,
  dangerous: false,
};

const verbose: GameAction = {
  id: "darktide:open",
  gameId: "darktide",
  displayName: "Open game folder",
  description: "Reveals the install directory.",
  category: "",
  icon: "folder",
  platform: "any",
  options: [
    {
      name: "force",
      label: "Force",
      type: "bool",
      required: false,
      description: "Reopen even if already running.",
      default: false,
      choices: [],
      placeholder: "",
    },
  ],
  requiresGame: true,
  dangerous: false,
};

function result(overrides: Partial<GameActionResult> = {}): GameActionResult {
  return {
    success: true,
    message: "",
    output: "3 entries",
    data: null,
    ...overrides,
  };
}

/** The palette and the dialog it opens are siblings, so both are mounted: mounting
 * the palette alone would pass every assertion and still leave the run dialog unrendered. */
function renderActions() {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0 },
      mutations: { retry: false },
    },
  });

  interface WrapperProps {
    children: ReactNode;
  }

  function Wrapper({ children }: WrapperProps) {
    return (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    );
  }

  return render(
    <Wrapper>
      <ActionPalette />
      <ActionDialogHost />
    </Wrapper>
  );
}

/** The dialog's own close button carries the same "Close" name as the footer's,
 * so the footer is addressed by its slot. */
function footerOf(dialog: HTMLElement): HTMLElement {
  return dialog.querySelector('[data-slot="dialog-footer"]') as HTMLElement;
}

async function runValidate(user: UserEvent): Promise<void> {
  await user.click(
    within(await screen.findByRole("dialog")).getByRole("button", {
      name: /run validate load order/i,
    })
  );
}

async function pick(action: GameAction): Promise<void> {
  const user = userEvent.setup();
  useUiStore.getState().setPaletteOpen(true);
  renderActions();

  await user.click(await screen.findByText(action.displayName));
}

describe("action surfaces", () => {
  beforeEach(() => {
    useGamesStore.getState().setActiveGameId("darktide");
    useActionsStore.getState().close();
    useUiStore.getState().setPaletteOpen(false);
    vi.mocked(api.listAvailableActions).mockResolvedValue([loadOrder, verbose]);
    vi.mocked(api.executeGameAction).mockResolvedValue(result());
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("names the scope and counts the actions instead of opening bare", async () => {
    renderActions();
    useUiStore.getState().setPaletteOpen(true);

    const dialog = await screen.findByRole("dialog", {
      name: /game actions/i,
    });
    expect(within(dialog).getAllByText(/actions for darktide/i)).toHaveLength(
      2
    );
    expect(dialog.querySelector('[data-slot="badge"]')?.textContent).toBe("2");
    expect(screen.getByRole("button", { name: /close/i })).toBeInTheDocument();
  });

  it("closes the palette when an action is picked", async () => {
    const user = userEvent.setup();
    renderActions();
    useUiStore.getState().setPaletteOpen(true);

    await user.click(await screen.findByText(loadOrder.displayName));

    await waitFor(() => expect(useUiStore.getState().paletteOpen).toBe(false));
    expect(screen.queryByRole("dialog", { name: /game actions/i })).toBeNull();
  });

  it("keeps the picked action for the dialog", async () => {
    const user = userEvent.setup();
    renderActions();
    useUiStore.getState().setPaletteOpen(true);

    await user.click(await screen.findByText(verbose.displayName));

    expect(useActionsStore.getState().request?.action.id).toBe(verbose.id);
    expect(await screen.findByRole("dialog")).toHaveTextContent(
      verbose.displayName
    );
  });

  /* cmdk matches fuzzily, so a jumble of letters would keep the other action on
   * screen. "duplicates" appears in one description and no letter of it in the other. */
  it("narrows the list to what the typed word appears in", async () => {
    const user = userEvent.setup();
    renderActions();
    useUiStore.getState().setPaletteOpen(true);

    await user.type(
      await screen.findByPlaceholderText(/type an action/i),
      "duplicates"
    );

    // cmdk re-renders its items from an external store, so the filter lands a
    // tick after the keystroke.
    await waitFor(() =>
      expect(screen.queryByText(verbose.displayName)).toBeNull()
    );
    expect(screen.getByText(loadOrder.displayName)).toBeInTheDocument();
  });

  it("says so when nothing matches what was typed", async () => {
    const user = userEvent.setup();
    renderActions();
    useUiStore.getState().setPaletteOpen(true);

    await user.type(
      await screen.findByPlaceholderText(/type an action/i),
      "qqqzzz"
    );

    expect(await screen.findByText(/declares no actions/i)).toBeInTheDocument();
  });

  it("says the catalogue is empty only when it really is", async () => {
    vi.mocked(api.listAvailableActions).mockResolvedValue([]);
    renderActions();
    useUiStore.getState().setPaletteOpen(true);

    expect(await screen.findByText(/declares no actions/i)).toBeInTheDocument();
  });

  it("offers a retry when the catalogue could not be read", async () => {
    vi.mocked(api.listAvailableActions).mockRejectedValue(new Error("nope"));
    renderActions();
    useUiStore.getState().setPaletteOpen(true);

    const retry = await screen.findByRole("button", { name: /retry/i });
    await userEvent.setup().click(retry);

    await waitFor(() =>
      expect(api.listAvailableActions).toHaveBeenCalledTimes(2)
    );
  });

  it("renders a form built from the declaration and runs it", async () => {
    await pick(verbose);

    expect(await screen.findByLabelText(/force/i)).toBeInTheDocument();
  });

  it("runs the action it was opened with", async () => {
    const user = userEvent.setup();
    renderActions();
    useActionsStore.getState().open({ action: loadOrder, presetArgs: {} });

    const dialog = await screen.findByRole("dialog");
    await user.click(
      within(dialog).getByRole("button", { name: /run validate load order/i })
    );

    await waitFor(() =>
      expect(api.executeGameAction).toHaveBeenCalledWith(loadOrder.id, {})
    );
  });

  it("shows the run in the button and keeps the label steady", async () => {
    const user = userEvent.setup();
    let release: (value: GameActionResult) => void = () => {};
    vi.mocked(api.executeGameAction).mockReturnValue(
      new Promise<GameActionResult>((resolve) => {
        release = resolve;
      })
    );

    renderActions();
    useActionsStore.getState().open({ action: loadOrder, presetArgs: {} });

    const run = within(await screen.findByRole("dialog")).getByRole("button", {
      name: /run validate load order/i,
    });
    await user.click(run);

    await waitFor(() =>
      expect(
        within(screen.getByRole("dialog")).getByRole("button", {
          name: /run validate load order/i,
        })
      ).toHaveAttribute("aria-busy", "true")
    );
    expect(
      within(screen.getByRole("dialog")).getByRole("button", {
        name: /run validate load order/i,
      })
    ).toHaveTextContent(/run validate load order/i);

    release(result());
  });

  it("shows a failed run instead of swallowing it", async () => {
    const user = userEvent.setup();
    vi.mocked(api.executeGameAction).mockRejectedValue(new Error("boom"));

    renderActions();
    useActionsStore.getState().open({ action: loadOrder, presetArgs: {} });

    await user.click(
      within(await screen.findByRole("dialog")).getByRole("button", {
        name: /run validate load order/i,
      })
    );

    expect(await screen.findByText(/boom/)).toBeInTheDocument();
    expect(screen.getByRole("dialog")).toHaveTextContent(
      /run validate load order/i
    );
  });

  it("replaces the form with the result and two real controls", async () => {
    const user = userEvent.setup();
    renderActions();
    useActionsStore.getState().open({ action: loadOrder, presetArgs: {} });

    await user.click(
      within(await screen.findByRole("dialog")).getByRole("button", {
        name: /run validate load order/i,
      })
    );

    expect(await screen.findByText("3 entries")).toBeInTheDocument();

    expect(
      within(footerOf(screen.getByRole("dialog"))).getByRole("button", {
        name: /run again/i,
      })
    ).toHaveAttribute("data-variant", "outline");

    await user.click(
      within(footerOf(screen.getByRole("dialog"))).getByRole("button", {
        name: /run again/i,
      })
    );
    await runValidate(user);

    expect(await screen.findByText("3 entries")).toBeInTheDocument();
  });

  it("closes on request from the footer", async () => {
    const user = userEvent.setup();
    renderActions();
    useActionsStore.getState().open({ action: loadOrder, presetArgs: {} });

    await runValidate(user);
    expect(await screen.findByText("3 entries")).toBeInTheDocument();

    await user.click(
      within(footerOf(screen.getByRole("dialog"))).getByRole("button", {
        name: /^close$/i,
      })
    );

    await waitFor(() => expect(useActionsStore.getState().request).toBeNull());
  });

  it("stays shut until something asks for an action", () => {
    renderActions();

    expect(screen.queryByRole("dialog")).toBeNull();
    expect(useActionsStore.getState().request).toBeNull();
  });
});
