import { CheckCircle2, XCircle } from "lucide-react";

import { ErrorText } from "@/components/common/ErrorText";
import { JsonViewer } from "@/components/common/JsonViewer";
import { t, tOr } from "@/lib/i18n";
import { extractJson } from "@/lib/jsonText";
import { cn } from "@/lib/utils";
import type { ActionDetail, GameActionResult } from "@/types/domain";

interface ResultBlockProps {
  children: string;
  muted?: boolean;
}

function ResultBlock({ children, muted }: ResultBlockProps) {
  return (
    <div
      className={cn(
        "max-h-64 w-full min-w-0 overflow-auto rounded-md border",
        muted && "bg-muted"
      )}
    >
      <pre
        className="w-full max-w-full min-w-0 p-3 font-mono text-xs wrap-anywhere whitespace-pre-wrap"
        data-selectable
      >
        {children}
      </pre>
    </div>
  );
}

/**
 * An action's own words can be JSON too: a script that prints a document, or a
 * data field Go shaped for the UI. Show the document instead of its source.
 */
function ValueBlock({ value }: { value: unknown }) {
  if (typeof value !== "string") return <JsonViewer value={value} />;

  const json = extractJson(value);
  if (!json) return <ResultBlock>{value}</ResultBlock>;

  return (
    <div className="flex min-w-0 flex-col gap-2">
      {json.before.trim() ? (
        <ResultBlock muted>{json.before.trim()}</ResultBlock>
      ) : null}
      <JsonViewer value={json.json} />
      {json.after.trim() ? (
        <ResultBlock muted>{json.after.trim()}</ResultBlock>
      ) : null}
    </div>
  );
}

interface ResultDetailsProps {
  details?: ActionDetail[];
}

function DetailValue({ value }: { value: string }) {
  return <ErrorText message={value} />;
}

function ResultDetails({ details }: ResultDetailsProps) {
  if (!details?.length) return null;

  return (
    <dl className="grid grid-cols-[minmax(0,auto)_minmax(0,1fr)] gap-x-4 gap-y-1 text-sm">
      {details.map((detail, index) => (
        <div key={`${detail.label}-${index}`} className="contents">
          <dt className="text-muted-foreground">
            {tOr(detail.labelKey, detail.label)}
          </dt>
          <dd className="min-w-0 font-mono text-xs wrap-anywhere">
            <DetailValue value={detail.value} />
          </dd>
        </div>
      ))}
    </dl>
  );
}

/** One result view for every action; only `success` changes the treatment. */
interface ActionResultProps {
  result: GameActionResult;
}

export function ActionResult({ result }: ActionResultProps) {
  const message = tOr(result.messageKey, result.message, result.messageArgs);

  return (
    <div className="flex min-w-0 flex-col gap-3">
      <div
        role="status"
        className={cn(
          "flex items-start gap-2.5 rounded-md border p-3",
          result.success
            ? "border-success/30 bg-success/10"
            : "border-destructive/30 bg-destructive/10"
        )}
      >
        {result.success ? (
          <CheckCircle2
            aria-hidden
            className="mt-0.5 size-5 shrink-0 text-success"
          />
        ) : (
          <XCircle
            aria-hidden
            className="mt-0.5 size-5 shrink-0 text-destructive"
          />
        )}
        <div className="min-w-0">
          <p className="text-sm font-medium">
            {result.success ? t("actions.succeeded") : t("actions.failed")}
          </p>
          {message ? (
            <div className="mt-0.5 text-sm text-muted-foreground">
              <ErrorText message={message} />
            </div>
          ) : null}
        </div>
      </div>

      <ResultDetails details={result.details} />

      {result.output ? <ValueBlock value={result.output} /> : null}

      {result.data !== undefined && result.data !== null ? (
        <ValueBlock value={result.data} />
      ) : null}
    </div>
  );
}
