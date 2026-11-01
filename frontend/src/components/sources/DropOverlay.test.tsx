import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { DropOverlay } from "./DropOverlay";

vi.mock("@/bridge/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/bridge/dialogs")>();
  return {
    ...actual,
    canResolveDroppedPaths: () => false,
    onFileDrop: () => () => undefined,
  };
});

function renderOverlay() {
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <DropOverlay />
    </QueryClientProvider>
  );
}

describe("DropOverlay", () => {
  it("cancels a URI-list folder drag instead of letting the webview navigate", () => {
    renderOverlay();

    const enter = fireEvent.dragEnter(window, {
      dataTransfer: { types: ["text/uri-list"], files: [] },
    });
    expect(screen.getByText("Drop to install")).toBeInTheDocument();
    const over = fireEvent.dragOver(window, {
      dataTransfer: { types: ["text/uri-list"], files: [] },
    });
    const drop = fireEvent.drop(window, {
      dataTransfer: { types: ["text/uri-list"], files: [] },
    });

    expect(enter).toBe(false);
    expect(over).toBe(false);
    expect(drop).toBe(false);
    expect(screen.queryByText("Drop to install")).toBeNull();
  });
});
