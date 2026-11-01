import { afterEach, describe, expect, it, vi } from "vitest";

import { notify, stopNotificationFunnel } from "./notify";

import { useNotificationsStore } from "@/stores/notifications.store";

const { toast } = vi.hoisted(() => ({ toast: vi.fn() }));

vi.mock("sonner", () => ({ toast }));
vi.mock("@/bridge/dialogs", () => ({ isWindowFocused: () => true }));
vi.mock("@/bridge/notifications", () => ({
  notificationAvailability: vi.fn(async () => true),
  sendNativeNotification: vi.fn(),
}));

describe("notify", () => {
  afterEach(() => {
    stopNotificationFunnel();
    vi.clearAllMocks();
    useNotificationsStore.setState({ pending: [] });
  });

  it("shows a focused notification immediately instead of waiting for coalescing", () => {
    notify({ title: "Installed", category: "install" });

    expect(toast).toHaveBeenCalledWith("Installed", {
      description: undefined,
    });
    expect(useNotificationsStore.getState().pending).toHaveLength(0);
  });
});
