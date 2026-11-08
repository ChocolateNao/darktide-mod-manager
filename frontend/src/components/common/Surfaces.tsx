import type { ComponentType, ReactNode } from "react";
import { AlertCircle, Inbox, Info, Loader2 } from "lucide-react";

import { describeBridgeFailure } from "@/bridge/dialogs";
import { ErrorText } from "@/components/common/ErrorText";
import { Button } from "@/components/ui/button";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { t } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/**
 * Shared states for a data-backed surface: empty, error and loading.
 * Defined once so every view writes "nothing here" the same way.
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
 * A pending surface. The spinner and label occupy the space the empty or error
 * state will use, so a view does not need a separate layout while it waits.
 */
interface LoadingStateProps {
  label?: string;
  className?: string;
}

export function LoadingState({ label, className }: LoadingStateProps) {
  return (
    <div
      className={cn(
        "flex flex-col items-center justify-center gap-2 p-10 text-center",
        className
      )}
    >
      <Loader2
        aria-hidden
        className="size-5 animate-spin text-muted-foreground"
      />
      <p className="text-sm text-muted-foreground">
        {label ?? t("common.loading")}
      </p>
    </div>
  );
}

/** Shows the bridge's own message and Go error code, so nothing is guessed or hidden. */
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
      <div className="min-w-0 flex-1 text-xs">
        <ErrorText message={describeBridgeFailure(error)} />
      </div>
      {onRetry && (
        <Button
          type="button"
          variant="link"
          size="xs"
          onClick={onRetry}
          className="h-auto shrink-0 rounded-sm p-0 font-normal text-current underline underline-offset-2 hover:no-underline focus-visible:ring-2 focus-visible:ring-ring"
        >
          {t("common.retry")}
        </Button>
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
                <Button
                  type="button"
                  variant="ghost"
                  size="icon-xs"
                  aria-label={details}
                  className="size-auto rounded-full p-0 text-muted-foreground transition-colors hover:bg-transparent hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring dark:hover:bg-transparent"
                >
                  <Info className="size-3.5" />
                </Button>
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
