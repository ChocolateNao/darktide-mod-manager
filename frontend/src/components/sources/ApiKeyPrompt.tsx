import { useState } from "react";
import { KeyRound } from "lucide-react";

import { describeBridgeFailure } from "@/bridge/dialogs";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { useSetSourceApiKey } from "@/hooks/mutations";
import { useSourcesQuery } from "@/hooks/queries";
import { t } from "@/lib/i18n";
import { useUiStore } from "@/stores/ui.store";
import { isBridgeErrorCode } from "@/types/errors";

/**
 * The bridge carries the Go error code, so this compares codes: a reworded
 * message must not decide whether the user gets a dialog.
 */
export function needsCredential(error: unknown): boolean {
  return (
    typeof error === "object" &&
    error !== null &&
    "code" in error &&
    isBridgeErrorCode((error as { code: string }).code, "AUTH_REQUIRED")
  );
}

/**
 * The prompt appears at the point of failure — the user came here to install a
 * mod — and starts empty because `ModSourceInfo` reports `hasApiKey`.
 */
interface ApiKeyPromptProps {
  sourceId: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  reason: string;
}

export function ApiKeyPrompt({
  sourceId,
  open,
  onOpenChange,
  reason,
}: ApiKeyPromptProps) {
  const sources = useSourcesQuery();
  const save = useSetSourceApiKey();
  const setView = useUiStore((state) => state.setView);
  const [apiKey, setApiKey] = useState("");
  const [failure, setFailure] = useState<string | null>(null);

  const source = (sources.data ?? []).find((entry) => entry.id === sourceId);
  const name = source?.name ?? sourceId;

  const close = (next: boolean) => {
    if (!next) {
      setApiKey("");
      setFailure(null);
    }
    onOpenChange(next);
  };

  const submit = async () => {
    const key = apiKey.trim();
    if (!key) return;

    setFailure(null);

    try {
      await save.mutateAsync({ sourceId, apiKey: key });
      close(false);
    } catch (error) {
      setFailure(describeBridgeFailure(error));
    }
  };

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <KeyRound className="size-4 text-(--game-accent)" />
            {t("apikey.title", { name })}
          </DialogTitle>
          <DialogDescription>{reason}</DialogDescription>
        </DialogHeader>

        <div className="flex flex-col gap-2">
          <label className="flex flex-col gap-1.5">
            <span className="text-sm">
              {t("apikey.label")} <span className="text-destructive">*</span>
            </span>
            <Input
              value={apiKey}
              onChange={(event) => setApiKey(event.target.value)}
              placeholder={t("apikey.placeholder")}
              aria-invalid={failure !== null}
              autoFocus
              onKeyDown={(event) => {
                if (event.key === "Enter") void submit();
              }}
            />
          </label>

          <p className="text-xs text-muted-foreground">{t("apikey.hint")}</p>

          {failure ? (
            <p className="text-xs text-destructive">{failure}</p>
          ) : null}
        </div>

        <DialogFooter>
          <Button
            variant="ghost"
            onClick={() => {
              close(false);
              setView("settings");
            }}
          >
            {t("apikey.openSettings")}
          </Button>
          <Button
            onClick={submit}
            disabled={apiKey.trim() === ""}
            loading={save.isPending}
          >
            {t("apikey.save")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
