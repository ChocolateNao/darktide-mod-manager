import { useEffect, useRef, useState } from "react";
import { Check, Copy } from "lucide-react";

import { copyToClipboard } from "@/bridge/dialogs";
import { Button } from "@/components/ui/button";
import { t } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/**
 * A copy control that confirms.
 *
 * A share link is 1–3 KB and displayed truncated, so this button is the only
 * way to get it; the checkmark is what makes one click trustworthy.
 */
interface CopyButtonProps {
  text: string;
  label?: string;
  variant?: "outline" | "ghost" | "secondary" | "default";
  size?: "sm" | "icon";
  className?: string;
}

export function CopyButton({
  text,
  label,
  variant = "outline",
  size = "sm",
  className,
}: CopyButtonProps) {
  const [copied, setCopied] = useState(false);
  const timer = useRef<number | undefined>(undefined);

  useEffect(() => () => window.clearTimeout(timer.current), []);

  const copy = async () => {
    if (!(await copyToClipboard(text))) return;

    setCopied(true);
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setCopied(false), 1600);
  };

  const caption = copied ? t("common.copied") : (label ?? t("common.copy"));

  return (
    <Button
      type="button"
      variant={variant}
      size={size}
      onClick={copy}
      disabled={!text}
      aria-label={caption}
      className={cn(size === "sm" && "gap-1.5", className)}
    >
      {copied ? (
        <Check className="size-3.5 text-success" />
      ) : (
        <Copy className="size-3.5" />
      )}
      {size === "sm" ? <span>{caption}</span> : null}
    </Button>
  );
}

/**
 * The field a long opaque string is shown in: scrollable and selectable, so a
 * truncated secret stays recoverable by hand.
 */
interface LongValueProps {
  text: string;
  className?: string;
}

export function LongValue({ text, className }: LongValueProps) {
  return (
    <code
      className={cn(
        "max-h-24 min-w-0 flex-1 overflow-auto rounded-md border bg-muted/40 px-2 py-1.5",
        "font-mono text-xs leading-relaxed break-all whitespace-pre-wrap",
        className
      )}
      data-selectable
    >
      {text}
    </code>
  );
}
