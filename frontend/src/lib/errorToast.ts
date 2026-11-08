import { type Action, toast } from "sonner";

import { describeBridgeFailure } from "@/bridge/dialogs";
import { t } from "@/lib/i18n";
import { extractJson } from "@/lib/jsonText";
import { useErrorDetailsStore } from "@/stores/errorDetails.store";

interface FailureToastOptions {
  description?: string;
  /** The caller's own action, used instead of Details when the message is plain. */
  action?: Action;
}

interface ToastBody {
  description: string;
  details?: Action;
}

/**
 * Prepares a toast message. A JSON body moves to a Details dialog because toast
 * text cannot hold a document, and the sentence stays readable on its own.
 */
export function toastBody(title: string, message: string): ToastBody {
  const json = extractJson(message);
  if (!json) return { description: message };

  return {
    description:
      [json.before.trim(), json.after.trim()].filter(Boolean).join(" ") ||
      t("errors.jsonBody"),
    details: {
      label: t("mods.details"),
      onClick: () => useErrorDetailsStore.getState().open({ title, message }),
    },
  };
}

/**
 * One error toast shape. A caller action keeps the primary slot, so Details
 * moves to the secondary slot: Sonner offers one primary button only.
 */
export function notifyFailure(
  title: string,
  error: unknown,
  options: FailureToastOptions = {}
): void {
  const message = options.description ?? describeBridgeFailure(error);
  const body = toastBody(title, message);

  toast.error(title, {
    description: body.description,
    action: options.action ?? body.details,
    cancel: options.action ? body.details : undefined,
  });
}
