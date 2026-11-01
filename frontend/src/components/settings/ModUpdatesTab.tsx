import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { Save } from "lucide-react";
import { toast } from "sonner";
import { z } from "zod";

import { NumberField } from "@/components/common/NumberField";
import { FieldRow, FormSection, Hint } from "@/components/common/Surfaces";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { useSetConfigValue } from "@/hooks/mutations";
import { formatDateTime } from "@/lib/format";
import { t } from "@/lib/i18n";
import { useConfigStore } from "@/stores/config.store";

/** `AutoUpdateConfig` asks sources whether a newer mod exists; the project has no self-updater, so the tab must not promise one. */
const updatesSchema = z.object({
  enabled: z.boolean(),
  checkIntervalHours: z.number().int().min(1).max(8760),
  downloadAutomatically: z.boolean(),
  // Tri-state: `ApplyDefaults` leaves an absent key alone, so JSON cannot tell
  // "unset" from false and a plain boolean would turn notifications off.
  notifyOnUpdate: z.enum(["inherit", "on", "off"]),
});

type UpdatesValues = z.infer<typeof updatesSchema>;

export function ModUpdatesTab() {
  const config = useConfigStore((state) => state.config);
  const setConfigValue = useSetConfigValue();

  const form = useForm<UpdatesValues>({
    resolver: zodResolver(updatesSchema),
    values: {
      enabled: config?.autoUpdate.enabled ?? false,
      checkIntervalHours: config?.autoUpdate.checkIntervalHours ?? 24,
      downloadAutomatically: config?.autoUpdate.downloadAutomatically ?? false,
      notifyOnUpdate:
        config?.autoUpdate.notifyOnUpdate === undefined
          ? "inherit"
          : config.autoUpdate.notifyOnUpdate
            ? "on"
            : "off",
    },
  });

  const notifyOnUpdate = form.watch("notifyOnUpdate");

  const save = form.handleSubmit(async (values) => {
    await setConfigValue.mutateAsync({
      key: "auto_update.enabled",
      value: String(values.enabled),
    });
    await setConfigValue.mutateAsync({
      key: "auto_update.check_interval_hours",
      value: String(values.checkIntervalHours),
    });
    await setConfigValue.mutateAsync({
      key: "auto_update.download_automatically",
      value: String(values.downloadAutomatically),
    });

    // "inherit" clears the key so the manager's own default applies again.
    if (values.notifyOnUpdate !== "inherit") {
      await setConfigValue.mutateAsync({
        key: "auto_update.notify_on_update",
        value: String(values.notifyOnUpdate === "on"),
      });
    }

    toast.success(t("settings.saved"));
  });

  const lastCheck = config?.autoUpdate.lastCheck
    ? new Date(config.autoUpdate.lastCheck)
    : null;

  return (
    <form className="flex flex-col gap-6" onSubmit={save}>
      <FormSection
        title={t("settings.tab.updates")}
        description={
          lastCheck
            ? t("settings.updates.lastCheck", {
                when: formatDateTime(lastCheck),
              })
            : undefined
        }
      >
        <FieldRow
          label={t("settings.updates.enabled")}
          htmlFor="updates-enabled"
        >
          <Switch
            id="updates-enabled"
            checked={form.watch("enabled")}
            onCheckedChange={(on) =>
              form.setValue("enabled", on, { shouldDirty: true })
            }
          />
        </FieldRow>

        <label className="flex flex-col gap-1.5">
          <span className="text-sm">{t("settings.updates.interval")}</span>
          <NumberField
            value={form.watch("checkIntervalHours")}
            onChange={(next) =>
              form.setValue("checkIntervalHours", next, { shouldDirty: true })
            }
            min={1}
            max={8760}
            step={6}
          />
        </label>

        <FieldRow
          label={t("settings.updates.download")}
          htmlFor="updates-download"
        >
          <Switch
            id="updates-download"
            checked={form.watch("downloadAutomatically")}
            onCheckedChange={(on) =>
              form.setValue("downloadAutomatically", on, { shouldDirty: true })
            }
          />
        </FieldRow>

        <div className="grid grid-cols-[minmax(0,14rem)_1fr] items-center gap-3">
          <span className="text-sm">{t("settings.updates.notify")}</span>
          {/* The shadcn Select: the native control ignores the theme and renders as a white box in a dark window. */}
          <Select
            value={notifyOnUpdate}
            onValueChange={(next) =>
              form.setValue(
                "notifyOnUpdate",
                next as UpdatesValues["notifyOnUpdate"],
                {
                  shouldDirty: true,
                }
              )
            }
          >
            <SelectTrigger className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="inherit">
                {t("settings.updates.notifyInherit")}
              </SelectItem>
              <SelectItem value="on">{t("common.yes")}</SelectItem>
              <SelectItem value="off">{t("common.no")}</SelectItem>
            </SelectContent>
          </Select>
        </div>

        <Hint>{t("settings.updates.notifyHint")}</Hint>
      </FormSection>

      <div className="flex justify-end">
        <Button
          type="submit"
          disabled={!form.formState.isDirty}
          loading={setConfigValue.isPending}
        >
          <Save className="size-4" />
          {t("settings.save")}
        </Button>
      </div>
    </form>
  );
}
