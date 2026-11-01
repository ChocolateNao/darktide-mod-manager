import { ExternalLink } from "lucide-react";
import { toast } from "sonner";

import * as api from "@/bridge/client";
import { describeBridgeFailure } from "@/bridge/dialogs";
import { Button } from "@/components/ui/button";
import { t } from "@/lib/i18n";
import type { ModSourceLink } from "@/types/domain";

/**
 * "18 MiB over the wire" and "or take these from Nexus instead" are the same
 * decision, so both render side by side. The label comes from the Go-side
 * `fallback` enum the backend owns — the frontend never decides what it means.
 */
interface SourceFallbackButtonsProps {
  link: ModSourceLink;
  modName: string;
}

export function SourceFallbackButtons({
  link,
  modName,
}: SourceFallbackButtonsProps) {
  const label = t(`share.fallback.${link.fallback || "none"}`);

  // "No source link" renders as plain text, since there is nothing to open.
  if (link.fallback === "") {
    return (
      <span className="text-xs text-muted-foreground">
        {modName} —{label}
      </span>
    );
  }

  // Go opens the URL through its scheme allowlist; the webview never navigates.
  const open = async () => {
    try {
      await api.openModSource(link.modId, "auto");
    } catch (error) {
      toast.error(describeBridgeFailure(error));
    }
  };

  return (
    <Button variant="outline" size="sm" onClick={open}>
      <ExternalLink className="size-3.5" />
      {label}
    </Button>
  );
}
