import { JsonViewer } from "@/components/common/JsonViewer";
import { extractJson } from "@/lib/jsonText";
import { cn } from "@/lib/utils";

/**
 * An error message with the provider's JSON body lifted out. The sentence stays
 * as text; the document gets colour, wrapping, and its own scrolling box.
 */
interface ErrorTextProps {
  message: string;
  className?: string;
}

export function ErrorText({ message, className }: ErrorTextProps) {
  const json = extractJson(message);

  if (!json) {
    return <span className={cn("wrap-anywhere", className)}>{message}</span>;
  }

  return (
    <div className={cn("flex min-w-0 flex-col gap-2", className)}>
      {json.before.trim() ? (
        <span className="wrap-anywhere">{json.before.trim()}</span>
      ) : null}
      <JsonViewer value={json.json} />
      {json.after.trim() ? (
        <span className="wrap-anywhere">{json.after.trim()}</span>
      ) : null}
    </div>
  );
}
