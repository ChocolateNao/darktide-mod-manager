/**
 * The only place the Wails notification runtime is called.
 *
 * `InitializeNotifications` and `CleanupNotifications` are absent: Wails exposes
 * them to Go only, so `main.go` owns both ends of their lifetime. What is left
 * is asking whether notifications work, registering categories, and sending.
 */

import {
  CheckNotificationAuthorization,
  IsNotificationAvailable,
  RegisterNotificationCategory,
  RequestNotificationAuthorization,
  SendNotification,
  SendNotificationWithActions,
} from "wailsjs/runtime/runtime";

import { hasWailsRuntime } from "@/bridge/dialogs";

export const NOTIFICATION_CATEGORIES = [
  "install",
  "updates",
  "hooks",
  "cache",
  "sharing",
  "errors",
] as const;

export type NotificationCategoryId = (typeof NOTIFICATION_CATEGORIES)[number];

export interface NativeNotification {
  title: string;
  body?: string;
  subtitle?: string;
  category?: NotificationCategoryId;
}

/* Asked once and remembered: `IsNotificationAvailable` talks to the daemon, which
   on a Linux box without one can be slow enough to feel. The `try` is load-bearing. */
let availability: Promise<boolean> | null = null;

export function notificationAvailability(): Promise<boolean> {
  if (!availability) {
    availability = (async () => {
      if (!hasWailsRuntime()) return false;
      return IsNotificationAvailable().catch(() => false);
    })();
  }
  return availability;
}

/**
 * Authorisation is a macOS concept — on Windows and Linux these always resolve
 * to `true`. The App settings tab only shows the control on macOS for the same
 * reason: a button that appears on two platforms and does nothing is worse than
 * no button.
 */
export async function notificationAuthorization(): Promise<boolean> {
  try {
    return await CheckNotificationAuthorization();
  } catch {
    return false;
  }
}

export async function requestNotificationAuthorization(): Promise<boolean> {
  try {
    return await RequestNotificationAuthorization();
  } catch {
    return false;
  }
}

/**
 * Registers the action buttons the Go-side response handler dispatches. The
 * buttons are plain actions with no reply field because a reply field is not
 * available on Linux, and a share notification is worth showing on all three
 * platforms.
 */
export async function registerNotificationCategories(): Promise<void> {
  if (!hasWailsRuntime()) return;
  await Promise.all(
    NOTIFICATION_CATEGORIES.map((id) =>
      RegisterNotificationCategory({
        id,
        actions: [{ id: "open", title: "Show" }],
      }).catch(() => undefined)
    )
  );
}

export async function sendNativeNotification(
  notification: NativeNotification
): Promise<void> {
  if (!hasWailsRuntime()) return;
  const payload = {
    id: `${notification.category ?? "general"}-${Date.now()}`,
    title: notification.title,
    body: notification.body,
    subtitle: notification.subtitle,
  };

  // Buttons come from the *registered* category: this only picks which of the
  // two send functions to use.
  if (notification.category) {
    await SendNotificationWithActions({
      ...payload,
      categoryId: notification.category,
    });
    return;
  }
  await SendNotification(payload);
}

/** Test seam: forgets the cached availability so a suite can exercise both branches. */
export function resetNotificationAvailabilityCache(): void {
  availability = null;
}
