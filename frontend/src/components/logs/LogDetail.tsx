import { copyToClipboard } from "@/bridge/dialogs";
import { Hint } from "@/components/common/Surfaces";
import { Button } from "@/components/ui/button";
import { formatDateTime, logFeature } from "@/lib/format";
import { t } from "@/lib/i18n";
import type { LogLine } from "@/types/domain";

/**
 * `Raw` is `json:"-"` on the Go side and the sink redacts what it keeps, so
 * there is no "copy unredacted" affordance here — the app cannot do that.
 */
interface LogDetailProps {
  line: LogLine | null;
}

export function LogDetail({ line }: LogDetailProps) {
  if (!line) {
    return <Hint className="p-3">{t("logs.selectHint")}</Hint>;
  }

  const entries = Object.entries(line.fields ?? {});

  // The message says "a hook failed"; stage and game live in structured fields.
  const field = (name: string): string | undefined => {
    const value = line.fields?.[name];
    return typeof value === "string" && value !== "" ? value : undefined;
  };

  const stage = field("phase") ?? field("stage");
  const game = field("game_id") ?? field("game");

  return (
    <div className="flex h-full min-w-0 flex-col gap-3 overflow-auto p-3 text-sm">
      <dl className="grid grid-cols-[6rem_minmax(0,1fr)] gap-x-3 gap-y-1 text-xs">
        <dt className="text-muted-foreground">{t("logs.feature")}</dt>
        <dd className="min-w-0 wrap-break-word" data-selectable>
          {logFeature(line)}
        </dd>
        <dt className="text-muted-foreground">{t("logs.mod")}</dt>
        <dd className="min-w-0 wrap-break-word" data-selectable>
          {line.modId || "—"}
        </dd>
        <dt className="text-muted-foreground">{t("logs.level")}</dt>
        <dd className="uppercase">{line.level}</dd>

        {/* "A hook failed" alone says nothing about which of the ten hook
            points went wrong; game and mod name make the line findable. */}
        {stage ? (
          <dt className="text-muted-foreground">{t("logs.hookStage")}</dt>
        ) : null}
        {stage ? (
          <dd className="min-w-0 wrap-break-word capitalize">
            {stage.replace(/_/g, " ")}
          </dd>
        ) : null}
        {game ? (
          <dt className="text-muted-foreground">{t("logs.game")}</dt>
        ) : null}
        {game ? (
          <dd className="min-w-0 wrap-break-word" data-selectable>
            {game}
          </dd>
        ) : null}
      </dl>

      <p
        className="min-w-0 rounded-md border px-2 py-1.5 text-xs wrap-break-word whitespace-pre-wrap"
        data-selectable
      >
        {line.message}
      </p>

      {entries.length > 0 ? (
        <div className="flex min-w-0 flex-col gap-1">
          <span className="text-xs text-muted-foreground">
            {t("logs.fields")}
          </span>
          <dl className="flex min-w-0 flex-col gap-1 text-xs" data-selectable>
            {entries.map(([key, value]) => (
              <div
                key={key}
                className="grid min-w-0 grid-cols-[7rem_minmax(0,1fr)] gap-2"
              >
                <dt className="min-w-0 wrap-break-word text-muted-foreground">
                  {key}
                </dt>
                <dd className="min-w-0 wrap-break-word">
                  {typeof value === "string" ? value : JSON.stringify(value)}
                </dd>
              </div>
            ))}
          </dl>
          <Hint>{t("logs.redacted")}</Hint>
        </div>
      ) : null}

      <Button
        variant="outline"
        size="sm"
        className="self-start"
        onClick={() => void copyToClipboard(line.message)}
      >
        {t("common.copy")}
      </Button>

      <span className="text-xs text-muted-foreground">
        {formatDateTime(new Date(line.timestamp))}
      </span>
    </div>
  );
}
