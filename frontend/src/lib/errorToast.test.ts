import type { Action } from "sonner";
import { toast } from "sonner";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { notifyFailure } from "@/lib/errorToast";
import { useErrorDetailsStore } from "@/stores/errorDetails.store";

vi.mock("sonner", () => ({ toast: { error: vi.fn() } }));

describe("notifyFailure", () => {
  beforeEach(() => {
    vi.mocked(toast.error).mockClear();
    useErrorDetailsStore.setState({ details: null });
  });

  it("offers Details and opens the message when it carries JSON", () => {
    notifyFailure("Update failed", 'AUTH_FAILED: refused: {"status":403}');

    const [, options] = vi.mocked(toast.error).mock.calls[0];
    const action = options?.action as Action | undefined;

    expect(options?.description).toBe("AUTH_FAILED: refused:");
    expect(action?.label).toBe("Details");

    action?.onClick({} as never);

    expect(useErrorDetailsStore.getState().details).toEqual({
      title: "Update failed",
      message: 'AUTH_FAILED: refused: {"status":403}',
    });
  });

  it("hides a body that is the whole message", () => {
    notifyFailure("Update failed", '{"status":403}');

    const [, options] = vi.mocked(toast.error).mock.calls[0];

    expect(options?.description).toBe("The source returned an error document.");
    expect((options?.action as Action | undefined)?.label).toBe("Details");
  });

  it("shows no Details button for plain text", () => {
    notifyFailure("Update failed", "the network is down");

    const [, options] = vi.mocked(toast.error).mock.calls[0];

    expect(options?.action).toBeUndefined();
    expect(options?.cancel).toBeUndefined();
  });

  it("keeps the caller action and moves Details beside it", () => {
    const action: Action = { label: "Open mod page", onClick: vi.fn() };

    notifyFailure("Update failed", 'refused: {"status":403}', { action });

    const [, options] = vi.mocked(toast.error).mock.calls[0];

    expect(options?.action).toBe(action);

    const cancel = options?.cancel as Action | undefined;
    expect(cancel?.label).toBe("Details");
  });
});
