/**
 * The one place anything is raised at the user.
 *
 * A focused window gets a toast — a native notification for something the user
 * is watching is pure interruption. A backgrounded window gets native, when the
 * platform can deliver. Repeats inside a short window merge into one.
 */

import { toast } from "sonner";

import { isWindowFocused } from "@/bridge/dialogs";
import type { NotifyRequest } from "@/bridge/events";
import {
  notificationAvailability,
  sendNativeNotification,
} from "@/bridge/notifications";
import { formatCount } from "@/lib/format";
import {
  COALESCE_WINDOW_MS,
  useNotificationsStore,
} from "@/stores/notifications.store";

const FLUSH_INTERVAL_MS = 500;

let flushTimer: ReturnType<typeof setInterval> | null = null;

function ensureFlushTimer(): void {
  if (flushTimer !== null) return;
  flushTimer = setInterval(() => {
    const store = useNotificationsStore.getState();
    for (const entry of store.takeReady(Date.now())) {
      deliver({
        ...entry.request,
        title:
          entry.count > 1
            ? `${entry.request.title} (${formatCount(entry.count, "time")})`
            : entry.request.title,
      });
    }
  }, FLUSH_INTERVAL_MS);
}

/** Stops the drain timer. Called when the app unmounts so tests and reloads are clean. */
export function stopNotificationFunnel(): void {
  if (flushTimer === null) return;
  clearInterval(flushTimer);
  flushTimer = null;
}

/**
 * Warm caches that are expensive to ask for on every notification: whether the
 * platform can deliver, and whether the user switched a category off.
 */
export async function primeNotificationFunnel(): Promise<void> {
  ensureFlushTimer();
  const available = await notificationAvailability();
  useNotificationsStore.getState().setAvailability(available);
}

function deliver(request: NotifyRequest): void {
  const store = useNotificationsStore.getState();
  if (store.toggles[request.category] === false) return;
  if (request.silent) return;

  if (isWindowFocused()) {
    toast(request.title, { description: request.body });
    return;
  }

  if (store.availability === false) {
    toast(request.title, { description: request.body });
    return;
  }

  void sendNativeNotification({
    title: request.title,
    body: request.body,
    category: request.category,
  });
}

/**
 * The only way a component raises anything. Messages that repeat inside the
 * coalescing window are merged and delivered once.
 */
export function notify(request: NotifyRequest): void {
  ensureFlushTimer();
  // A focused window is watching the event happen; the coalescing window only
  // makes sense for notifications the user will read later.
  if (isWindowFocused()) {
    deliver(request);
    return;
  }
  useNotificationsStore.getState().coalesce(request);
}

/**
 * A notification about an event the user is watching right now, so it should
 * never wait out the coalescing window.
 */
export function notifyNow(request: NotifyRequest): void {
  ensureFlushTimer();
  const store = useNotificationsStore.getState();
  store.clearPending();
  deliver(request);
}

export const COALESCE_WINDOW = COALESCE_WINDOW_MS;
