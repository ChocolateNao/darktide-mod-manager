import { useState } from "react";

import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { formatDurationSeconds, parseDurationSeconds } from "@/lib/format";
import { t } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/**
 * A duration field that shows what it means.
 *
 * The backend speaks Go's duration syntax because `domain.Duration` is a string
 * on the wire; raw text stays local so a half-typed "1" is not rewritten.
 */
interface DurationFieldProps {
  valueSeconds: number;
  onChange: (seconds: number) => void;
  label?: string;
  disabled?: boolean;
  className?: string;
  id?: string;
}

export function DurationField({
  valueSeconds,
  onChange,
  label,
  disabled,
  className,
  id,
}: DurationFieldProps) {
  const [draft, setDraft] = useState<string | null>(null);
  const [lastValue, setLastValue] = useState(valueSeconds);

  /* Drop the draft when the value changes underneath: adjusting state during render saves a second render. */
  if (valueSeconds !== lastValue) {
    setLastValue(valueSeconds);
    setDraft(null);
  }

  const parsed = draft === null ? valueSeconds : parseDurationSeconds(draft);

  const invalid = draft !== null && parsed === null;

  return (
    <div className={cn("flex flex-col gap-1", className)}>
      {label ? <Label htmlFor={id}>{label}</Label> : null}

      <div className="flex items-stretch">
        <Input
          id={id}
          inputMode="decimal"
          value={draft ?? formatDurationSeconds(valueSeconds)}
          disabled={disabled}
          aria-invalid={invalid}
          onChange={(event) => {
            const next = event.target.value;
            setDraft(next);

            const seconds = parseDurationSeconds(next);
            if (seconds !== null) onChange(seconds);
          }}
          onBlur={() => setDraft(null)}
          className="h-9 rounded-r-none tabular-nums"
        />
        <span
          className={cn(
            "flex h-9 items-center rounded-r-none border border-l-0 px-2 text-xs",
            "bg-muted/40 text-muted-foreground tabular-nums"
          )}
        >
          {invalid
            ? t("duration.invalid")
            : t("duration.seconds", { count: parsed ?? 0 })}
        </span>
      </div>

      {/* The Go form is what the config file holds, so a hand-edited `config.json` shows the same value. */}

      <p className="text-[11px] text-muted-foreground">
        {t("duration.wire", { value: goDuration(valueSeconds) })}
      </p>
    </div>
  );
}

/**
 * Renders seconds in Go's syntax, which `domain.Duration` parses and a
 * hand-edited config file contains.
 */
export function goDuration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return "0s";

  let rest = Math.floor(seconds);
  let out = "";

  const hours = Math.floor(rest / 3600);
  if (hours > 0) {
    out += `${hours}h`;
    rest -= hours * 3600;
  }

  const minutes = Math.floor(rest / 60);
  if (minutes > 0) {
    out += `${minutes}m`;
    rest -= minutes * 60;
  }

  // Seconds are appended so 3661 stays "1h1m1s": rounding to "1h1m" would
  // shorten a timeout by a second every time the form was saved.
  if (rest > 0) out += `${rest}s`;

  return out || "0s";
}
