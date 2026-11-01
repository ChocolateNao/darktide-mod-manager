import { useState } from "react";
import { FolderOpen, ScrollText, Settings as SettingsIcon } from "lucide-react";

import { revealPath } from "@/bridge/dialogs";
import { Hint } from "@/components/common/Surfaces";
import { LogDetail } from "@/components/logs/LogDetail";
import { LogFilesPanel } from "@/components/logs/LogFilesPanel";
import { LogSettingsPopover } from "@/components/logs/LogSettingsPopover";
import { LogFilter, LogTail } from "@/components/logs/LogTail";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  useLogFilesQuery,
  useLogSettingsQuery,
  useLogTailQuery,
} from "@/hooks/queries";
import { t } from "@/lib/i18n";
import { logLevels } from "@/lib/schemas/log";
import { selectLogLines, useActivityStore } from "@/stores/activity.store";
import type { LogLine } from "@/types/domain";

const TAIL_LINES = 5_000;
const ALL_LEVELS = "__all__";

/**
 * `TailLogs` re-parses the log file on every call, so it is the one query with
 * no background floor — the `log` event already drives the tail, and a timer
 * would re-read the file for nothing.
 */
export default function LogsView() {
  /* TailLogs takes a minimum level, so a floor cannot show warn and error
     while hiding debug — levels are selected directly. */
  const [levels, setLevels] = useState<Set<string>>(() => new Set(logLevels));
  const [filter, setFilter] = useState("");
  const [picked, setPicked] = useState<LogLine | null>(null);

  useLogTailQuery(TAIL_LINES, "trace");
  const settings = useLogSettingsQuery();
  const files = useLogFilesQuery();
  const lines = useActivityStore(selectLogLines);
  const visible =
    levels.size === logLevels.length
      ? lines
      : lines.filter((line) => levels.has(line.level));

  return (
    <div className="flex h-full min-h-0 flex-col p-4">
      <header className="flex items-center justify-between gap-2">
        <h1 className="text-sm font-semibold">{t("logs.title")}</h1>
        <div className="flex items-center gap-2">
          <LogFilter value={filter} onChange={setFilter} />
          <LevelFilter levels={levels} onChange={setLevels} />
          <Button
            size="sm"
            variant="ghost"
            onClick={() => {
              const directory = settings.data?.directory;
              if (directory) void revealPath(directory);
            }}
            disabled={!settings.data?.directory}
          >
            <FolderOpen className="size-4" />
            {t("logs.openDirectory")}
          </Button>
          <LogSettingsPopover settings={settings.data} />
        </div>
      </header>

      <p className="mt-1 text-xs text-muted-foreground">
        {t("logs.records", { count: lines.length })}
        {levels.size === logLevels.length
          ? ""
          : ` · ${t("logs.showing", { count: visible.length })}`}
        {lines.length >= TAIL_LINES
          ? ` · ${t("logs.tailStops", { count: TAIL_LINES })}`
          : ""}
      </p>

      <div className="mt-3 grid min-h-0 flex-1 grid-cols-[minmax(0,1fr)_20rem] gap-3">
        <Tabs defaultValue="tail" className="flex min-h-0 flex-col">
          <TabsList>
            <TabsTrigger value="tail">
              <ScrollText className="size-3.5" />
              {t("logs.title")}
            </TabsTrigger>
            <TabsTrigger value="files">
              <SettingsIcon className="size-3.5" />
              {t("logs.files")}
            </TabsTrigger>
          </TabsList>

          <TabsContent
            value="tail"
            className="min-h-0 flex-1 rounded-md border"
          >
            <LogTail lines={visible} filter={filter} onPick={setPicked} />
          </TabsContent>

          <TabsContent
            value="files"
            className="min-h-0 flex-1 overflow-auto rounded-md border"
          >
            <LogFilesPanel files={files.data ?? []} />
          </TabsContent>
        </Tabs>

        <aside className="min-h-0 overflow-hidden rounded-md border">
          <LogDetail line={picked} />
        </aside>
      </div>

      <Hint>{t("logs.levelHint")}</Hint>
    </div>
  );
}

/** "All" is a real option — turning everything off happens, and this is the way back. */
interface LevelFilterProps {
  levels: Set<string>;
  onChange: (levels: Set<string>) => void;
}

function LevelFilter({ levels, onChange }: LevelFilterProps) {
  const all = levels.size === logLevels.length;

  const toggle = (level: string) => {
    const next = new Set(levels);

    if (next.has(level)) next.delete(level);
    else next.add(level);

    onChange(next);
  };

  return (
    <Select
      value={all ? ALL_LEVELS : Array.from(levels).sort().join(",")}
      onValueChange={(next) =>
        onChange(
          next === ALL_LEVELS
            ? new Set(logLevels)
            : new Set(next.split(",").filter(Boolean))
        )
      }
    >
      <SelectTrigger className="w-36">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={ALL_LEVELS}>{t("logs.levels.all")}</SelectItem>
        {logLevels.map((level) => (
          <SelectItem
            key={level}
            value={level}
            onSelect={() => toggle(level)}
            className="uppercase"
          >
            {level}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
