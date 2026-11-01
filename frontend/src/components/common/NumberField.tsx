import { Minus, Plus } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { t } from "@/lib/i18n";
import { cn } from "@/lib/utils";

export type TimeUnit = "seconds" | "minutes" | "hours" | "days";

const SECONDS_PER_UNIT: Record<TimeUnit, number> = {
  seconds: 1,
  minutes: 60,
  hours: 3_600,
  days: 86_400,
};

const UNIT_LABEL: Record<TimeUnit, string> = {
  seconds: "unit.seconds",
  minutes: "unit.minutes",
  hours: "unit.hours",
  days: "unit.days",
};

/**
 * A number field with stepper buttons stacked to its right.
 *
 * With `unit`, the field edits a duration: `value` and `onChange` are seconds,
 * while `min`, `max` and `step` are in the selected unit.
 */
interface NumberFieldProps {
  value: number;
  onChange: (value: number) => void;
  min?: number;
  max?: number;
  step?: number;
  unit?: TimeUnit;
  placeholder?: string;
  disabled?: boolean;
  id?: string;
  "aria-label"?: string;
  className?: string;
}

export function NumberField({
  value,
  onChange,
  min,
  max,
  step = 1,
  unit,
  placeholder,
  disabled,
  id,
  "aria-label": ariaLabel,
  className,
}: NumberFieldProps) {
  const factor = unit ? SECONDS_PER_UNIT[unit] : 1;
  const shown = unit ? Math.round((value / factor) * 100) / 100 : value;

  const emit = (next: number) =>
    onChange(unit ? Math.round(next * factor) : next);

  const nudge = (direction: 1 | -1) => {
    const next = clamp(shown + direction * step, min, max);

    if (next !== shown) emit(next);
  };

  const atMin = min !== undefined && shown <= min;
  const atMax = max !== undefined && shown >= max;

  return (
    <div
      className={cn(
        "flex items-stretch rounded-md focus-within:ring-[3px] focus-within:ring-ring/50",
        className
      )}
    >
      <Input
        id={id}
        aria-label={ariaLabel}
        type="number"
        inputMode="decimal"
        value={Number.isFinite(shown) ? shown : ""}
        min={min}
        max={max}
        step={unit ? "any" : step}
        placeholder={placeholder}
        disabled={disabled}
        onChange={(event) => {
          const raw = Number.parseFloat(event.target.value);

          // An empty field is a half-typed number; clamping it here would rewrite
          // "1" to "10" the moment the backspace lands.
          if (Number.isNaN(raw)) return;
          emit(clamp(raw, min, max));
        }}
        className="h-9 [appearance:textfield] rounded-l-md rounded-r-none border-r-0 px-2 text-center tabular-nums focus-visible:border-input focus-visible:ring-0 [&::-webkit-inner-spin-button]:appearance-none [&::-webkit-outer-spin-button]:appearance-none"
      />

      {unit ? (
        <span className="flex items-center border-y border-l border-input bg-muted/40 px-1.5 text-xs text-muted-foreground">
          {t(UNIT_LABEL[unit])}
        </span>
      ) : null}

      <div className="flex h-9 w-7 shrink-0 flex-col overflow-hidden rounded-r-md border border-input bg-background">
        <Button
          type="button"
          variant="ghost"
          size="icon"
          aria-label="increase"
          disabled={disabled || atMax}
          onClick={() => nudge(1)}
          className="h-1/2 w-full rounded-none border-b border-input"
        >
          <Plus className="size-3" />
        </Button>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          aria-label="decrease"
          disabled={disabled || atMin}
          onClick={() => nudge(-1)}
          className="h-1/2 w-full rounded-none"
        >
          <Minus className="size-3" />
        </Button>
      </div>
    </div>
  );
}

function clamp(value: number, min?: number, max?: number): number {
  if (min !== undefined && value < min) return min;
  if (max !== undefined && value > max) return max;
  return value;
}
