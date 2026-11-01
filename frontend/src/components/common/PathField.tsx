import { useCallback, useState } from "react";
import { Check, Copy, FolderOpen } from "lucide-react";
import { toast } from "sonner";

import {
  copyToClipboard,
  pickDirectory,
  pickFile,
  revealPath,
} from "@/bridge/dialogs";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { t } from "@/lib/i18n";
import { cn } from "@/lib/utils";

interface PathFieldProps {
  value: string;
  onChange: (next: string) => void;
  /** A blank value means "use the platform default" in every config path field. */
  placeholder?: string;
  file?: boolean;
  /** Extension patterns for the file dialog, e.g. `["*.toml"]`. */
  patterns?: string[];
  disabled?: boolean;
  id?: string;
}

/**
 * A path input with the three things a path needs: pick, reveal and copy.
 *
 * `""` means "use the platform default", so cancelling a dialog leaves the field
 * untouched while an explicit reset clears it; the bridge tells those apart.
 */
export function PathField({
  value,
  onChange,
  placeholder,
  file = false,
  patterns,
  disabled,
  id,
}: PathFieldProps) {
  const [copied, setCopied] = useState(false);

  const choose = useCallback(async () => {
    if (file) {
      const picked = await pickFile(value, patterns ?? []);
      // A cancelled file dialog is an empty string, and an empty path is a real
      // answer, so there is no `cleared` flag to read here.
      if (picked) onChange(picked);
      return;
    }

    const choice = await pickDirectory(value);
    if (choice.kind === "chosen") onChange(choice.path);
    if (choice.kind === "cleared") onChange("");
  }, [file, onChange, patterns, value]);

  const reset = useCallback(() => {
    // An explicit reset clears the field: that is "use the platform default" in
    // every `domain.Config` path.
    onChange("");
  }, [onChange]);

  const copy = useCallback(async () => {
    await copyToClipboard(value);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1500);
  }, [value]);

  const reveal = useCallback(async () => {
    if (!value) return;
    try {
      await revealPath(value);
    } catch (error) {
      toast.error(t("common.error"), { description: String(error) });
    }
  }, [value]);

  return (
    <div className="flex w-full items-center gap-1">
      <Input
        id={id}
        value={value}
        placeholder={placeholder}
        disabled={disabled}
        spellCheck={false}
        onChange={(event) => onChange(event.target.value)}
        className="font-mono text-xs"
      />
      <Button
        type="button"
        variant="outline"
        size="icon"
        title={t("path.browse")}
        aria-label={t("path.browse")}
        disabled={disabled}
        onClick={() => void choose()}
      >
        <FolderOpen className="size-4" />
      </Button>
      <Button
        type="button"
        variant="outline"
        size="icon"
        title={value ? t("path.reveal") : t("common.notAvailable")}
        aria-label={t("path.reveal")}
        disabled={disabled || !value}
        onClick={() => void reveal()}
      >
        <FolderOpen className="size-4 rotate-45" />
      </Button>
      <Button
        type="button"
        variant="outline"
        size="icon"
        title={t("common.copy")}
        aria-label={t("common.copy")}
        disabled={disabled || !value}
        onClick={() => void copy()}
      >
        {copied ? (
          <Check className="size-4 text-success" />
        ) : (
          <Copy className="size-4" />
        )}
      </Button>
      {!file && (
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className={cn("shrink-0", value === "" && "text-muted-foreground")}
          disabled={disabled || value === ""}
          onClick={reset}
        >
          {t("path.reset")}
        </Button>
      )}
    </div>
  );
}
