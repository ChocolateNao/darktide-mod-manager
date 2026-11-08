import { useState } from "react";
import { Copy } from "lucide-react";

import { copyToClipboard } from "@/bridge/dialogs";
import { JsonViewer } from "@/components/common/JsonViewer";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { t } from "@/lib/i18n";
import { extractJson } from "@/lib/jsonText";
import { useErrorDetailsStore } from "@/stores/errorDetails.store";

/**
 * The full error text behind a toast. The JSON body is split out so the sentence
 * that explains it stays readable above the coloured document.
 */
export function ErrorDetailsDialog() {
  const details = useErrorDetailsStore((state) => state.details);
  const close = useErrorDetailsStore((state) => state.close);
  const [copied, setCopied] = useState(false);

  const json = details ? extractJson(details.message) : null;

  return (
    <Dialog
      open={details !== null}
      onOpenChange={(next) => {
        if (!next) {
          setCopied(false);
          close();
        }
      }}
    >
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{details?.title}</DialogTitle>
          <DialogDescription>
            {json?.before.trim() || t("errors.detailsHint")}
          </DialogDescription>
        </DialogHeader>

        {json ? <JsonViewer value={json.json} /> : null}
        {json?.after.trim() ? (
          <p className="text-sm wrap-anywhere text-muted-foreground">
            {json.after.trim()}
          </p>
        ) : null}
        {!json && details ? (
          <pre className="max-h-80 overflow-auto rounded-md border bg-muted/40 p-3 font-mono text-xs wrap-anywhere whitespace-pre-wrap">
            {details.message}
          </pre>
        ) : null}

        <DialogFooter>
          <Button
            variant="outline"
            onClick={async () => {
              if (!details) return;

              const ok = await copyToClipboard(details.message);
              setCopied(ok);
            }}
          >
            <Copy className="size-3.5" />
            {copied ? t("common.copied") : t("common.copy")}
          </Button>
          <DialogClose asChild>
            <Button>{t("common.close")}</Button>
          </DialogClose>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
