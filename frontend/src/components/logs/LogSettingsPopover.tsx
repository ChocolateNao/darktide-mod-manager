import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { Save, Settings as SettingsIcon } from "lucide-react";
import { z } from "zod";

import { NumberField } from "@/components/common/NumberField";
import { PathField } from "@/components/common/PathField";
import { FieldRow, FormSection, Hint } from "@/components/common/Surfaces";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import { ScrollArea } from "@/components/ui/scroll-area";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import {
  useSetConfigValue,
  useSetLogLevel,
  useSetRedactFields,
} from "@/hooks/mutations";
import { t } from "@/lib/i18n";
import { logLevels } from "@/lib/schemas/log";
import type { LogSettings } from "@/types/domain";

/**
 * Output and its configuration are one subject, so the level control lives next
 * to the tail it changes: two surfaces for one feature is where settings drift.
 */
const settingsSchema = z.object({
  level: z.enum(logLevels),
  directory: z.string(),
  maxSizeMb: z.number().int().min(1),
  maxBackups: z.number().int().min(0),
  maxAgeDays: z.number().int().min(0),
  compress: z.boolean(),
  redact: z.string(),
});

type SettingsValues = z.infer<typeof settingsSchema>;

interface LogSettingsPopoverProps {
  settings: LogSettings | undefined;
}

export function LogSettingsPopover({ settings }: LogSettingsPopoverProps) {
  const setLevel = useSetLogLevel();
  const setConfig = useSetConfigValue();
  const setRedact = useSetRedactFields();

  const form = useForm<SettingsValues>({
    resolver: zodResolver(settingsSchema),
    values: {
      level: (logLevels as readonly string[]).includes(settings?.level ?? "")
        ? (settings!.level as SettingsValues["level"])
        : "info",
      directory: settings?.directory ?? "",
      maxSizeMb: settings?.maxSizeMb ?? 10,
      maxBackups: settings?.maxBackups ?? 5,
      maxAgeDays: settings?.maxAgeDays ?? 30,
      compress: settings?.compress ?? true,
      redact: "",
    },
  });

  const save = form.handleSubmit(async (values) => {
    // The level applies live through its runtime call and is also persisted so
    // the next launch starts at the chosen severity. The rest is config only.
    await setLevel.mutateAsync(values.level);

    const writes: [string, string][] = [
      ["logging.level", values.level],
      ["logging.directory", values.directory],
      ["logging.max_size_mb", String(values.maxSizeMb)],
      ["logging.max_backups", String(values.maxBackups)],
      ["logging.max_age_days", String(values.maxAgeDays)],
      ["logging.compress", String(values.compress)],
    ];

    // Sequential, because each key re-reads and rewrites the same file.
    for (const [key, value] of writes) {
      await setConfig.mutateAsync({ key, value });
    }

    await setRedact.mutateAsync(
      values.redact
        .split("\n")
        .map((line) => line.trim())
        .filter(Boolean)
    );
  });

  const dirty = form.formState.isDirty;

  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button size="icon" variant="ghost" aria-label={t("logs.settings")}>
          <SettingsIcon className="size-4" />
        </Button>
      </PopoverTrigger>

      <PopoverContent align="end" className="w-xl p-0">
        <ScrollArea className="max-h-128">
          <form className="flex flex-col gap-4 p-4" onSubmit={save}>
            <FormSection
              title={t("logs.settings")}
              description={t("logs.levelHint")}
            >
              <FieldRow label={t("logs.level")} htmlFor="log-level">
                <Select
                  value={form.watch("level")}
                  onValueChange={(value) =>
                    form.setValue("level", value as SettingsValues["level"], {
                      shouldDirty: true,
                    })
                  }
                >
                  <SelectTrigger id="log-level" className="w-40">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {logLevels.map((level) => (
                      <SelectItem
                        key={level}
                        value={level}
                        className="uppercase"
                      >
                        {level}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </FieldRow>

              {/* Stacked so the path field escapes FieldRow's 14rem label column. */}
              <div className="flex flex-col gap-1">
                <Label htmlFor="log-directory">
                  {t("settings.storage.path")}
                </Label>
                <PathField
                  id="log-directory"
                  value={form.watch("directory")}
                  onChange={(value) =>
                    form.setValue("directory", value, { shouldDirty: true })
                  }
                />
                <Hint>{t("settings.restartRequired")}</Hint>
              </div>

              <div className="grid grid-cols-3 gap-3">
                <label className="flex flex-col gap-1.5">
                  <span className="text-sm">{t("logs.sizeMb")}</span>
                  <NumberField
                    value={form.watch("maxSizeMb")}
                    onChange={(next) =>
                      form.setValue("maxSizeMb", next, { shouldDirty: true })
                    }
                    min={1}
                  />
                </label>
                <label className="flex flex-col gap-1.5">
                  <span className="text-sm">{t("logs.backups")}</span>
                  <NumberField
                    value={form.watch("maxBackups")}
                    onChange={(next) =>
                      form.setValue("maxBackups", next, { shouldDirty: true })
                    }
                    min={0}
                  />
                </label>
                <label className="flex flex-col gap-1.5">
                  <span className="text-sm">{t("logs.maxAgeDays")}</span>
                  <NumberField
                    value={form.watch("maxAgeDays")}
                    onChange={(next) =>
                      form.setValue("maxAgeDays", next, { shouldDirty: true })
                    }
                    min={0}
                  />
                </label>
              </div>

              <FieldRow label={t("logs.compress")} htmlFor="log-compress">
                <Switch
                  id="log-compress"
                  checked={form.watch("compress")}
                  onCheckedChange={(on) =>
                    form.setValue("compress", on, { shouldDirty: true })
                  }
                />
              </FieldRow>

              <FieldRow
                label={t("logs.redact")}
                htmlFor="log-redact"
                description={t("logs.redactHint")}
              >
                <textarea
                  id="log-redact"
                  className="min-h-20 rounded-md border bg-transparent px-3 py-2 font-mono text-xs"
                  value={form.watch("redact")}
                  onChange={(event) =>
                    form.setValue("redact", event.target.value, {
                      shouldDirty: true,
                    })
                  }
                  placeholder={
                    settings?.hasRedactFields
                      ? t("settings.sources.apiKeyStored")
                      : ""
                  }
                />
              </FieldRow>
            </FormSection>

            <div className="flex justify-end">
              <Button
                type="submit"
                disabled={!dirty}
                loading={setLevel.isPending || setConfig.isPending}
              >
                <Save className="size-4" />
                {t("settings.save")}
              </Button>
            </div>
          </form>
        </ScrollArea>
      </PopoverContent>
    </Popover>
  );
}
