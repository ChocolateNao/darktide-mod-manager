import type { ComponentType, ReactNode } from "react";
import { AlertCircle, Inbox, Info } from "lucide-react";

import { describeBridgeFailure } from "@/bridge/dialogs";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { t } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/**
 * The three states every data-backed surface can be in, in one place.
 *
 * As components they keep "nothing here" written once across nine views.
 */

interface EmptyStateProps {
  /** A lucide icon passed as a component; no call site sizes or colours it. */
  icon?: ComponentType<{ className?: string }>;
  title: string;
  description?: string;
  action?: ReactNode;
  className?: string;
}

export function EmptyState({
  icon,
  title,
  description,
  action,
  className,
}: EmptyStateProps) {
  const Glyph = icon;
  return (
    <div
      className={cn(
        "flex flex-col items-center justify-center gap-2 p-10 text-center",
        className
      )}
    >
      <div className="text-muted-foreground/60">
        {Glyph ? <Glyph className="size-8" /> : <Inbox className="size-8" />}
      </div>
      <p className="text-sm font-medium">{title}</p>
      {description && (
        <p className="max-w-md text-xs text-muted-foreground">{description}</p>
      )}
      {action}
    </div>
  );
}

/**
 * An error worth showing in place, with the bridge's own message and Go error
 * code so nothing is guessed or hidden.
 */
interface ErrorNoteProps {
  error: unknown;
  onRetry?: () => void;
  className?: string;
}

export function ErrorNote({ error, onRetry, className }: ErrorNoteProps) {
  return (
    <div
      role="alert"
      className={cn(
        "flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive",
        className
      )}
    >
      <AlertCircle className="mt-0.5 size-3.5 shrink-0" />
      <span className="min-w-0 flex-1 wrap-break-word">
        {describeBridgeFailure(error)}
      </span>
      {onRetry && (
        <button
          type="button"
          onClick={onRetry}
          className="shrink-0 underline underline-offset-2 hover:no-underline"
        >
          {t("common.retry")}
        </button>
      )}
    </div>
  );
}

/** A muted aside used for the "this needs a restart" style of note. */
interface HintProps {
  children: ReactNode;
  className?: string;
}

export function Hint({ children, className }: HintProps) {
  return (
    <p
      className={cn("text-xs leading-relaxed text-muted-foreground", className)}
    >
      {children}
    </p>
  );
}

/** A labelled block inside a settings form, so every tab reads the same way. */
interface FormSectionProps {
  title: string;
  description?: string;
  children: ReactNode;
  className?: string;
}

export function FormSection({
  title,
  description,
  children,
  className,
}: FormSectionProps) {
  return (
    <section className={cn("flex flex-col gap-3", className)}>
      <div className="flex flex-col gap-0.5">
        <h3 className="text-sm font-semibold">{title}</h3>
        {description && <Hint>{description}</Hint>}
      </div>
      <div className="flex flex-col gap-4">{children}</div>
    </section>
  );
}

/** A label/control row, which is the shape every settings field takes. */
interface FieldRowProps {
  label: string;
  htmlFor?: string;
  description?: string;
  details?: string;
  children: ReactNode;
  className?: string;
}

export function FieldRow({
  label,
  htmlFor,
  description,
  details,
  children,
  className,
}: FieldRowProps) {
  return (
    <div
      className={cn(
        "grid grid-cols-[minmax(0,14rem)_1fr] items-start gap-4",
        className
      )}
    >
      <div className="flex flex-col gap-0.5 pt-1">
        <div className="flex items-center gap-1.5">
          <label htmlFor={htmlFor} className="text-sm">
            {label}
          </label>
          {details ? (
            <Tooltip>
              <TooltipTrigger asChild>
                <button
                  type="button"
                  aria-label={details}
                  className="rounded-full text-muted-foreground transition-colors hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
                >
                  <Info className="size-3.5" />
                </button>
              </TooltipTrigger>
              <TooltipContent className="max-w-xs">{details}</TooltipContent>
            </Tooltip>
          ) : null}
        </div>
        {description && <Hint>{description}</Hint>}
      </div>
      <div className="min-w-0">{children}</div>
    </div>
  );
}
