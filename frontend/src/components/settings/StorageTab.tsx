import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { Save } from "lucide-react";
import { toast } from "sonner";
import { z } from "zod";

import { NumberField } from "@/components/common/NumberField";
import { PathField } from "@/components/common/PathField";
import { FormSection } from "@/components/common/Surfaces";
import { Button } from "@/components/ui/button";
import { useSetConfigValue } from "@/hooks/mutations";
import { useDefaultPathsQuery } from "@/hooks/queries";
import { t } from "@/lib/i18n";
import { useConfigStore } from "@/stores/config.store";

const storageSchema = z.object({
  path: z.string(),
  backupRetentionDays: z.number().int().min(0),
});

type StorageValues = z.infer<typeof storageSchema>;

/**
 * The form library earns its place here: the storage path is resolved once at
 * startup, so an unnoticed mistake costs a restart. Save stays disabled until
 * something is dirty, which keeps the two states distinct.
 */
export function StorageTab() {
  const config = useConfigStore((state) => state.config);
  const defaults = useDefaultPathsQuery();
  const setConfigValue = useSetConfigValue();

  const form = useForm<StorageValues>({
    resolver: zodResolver(storageSchema),
    values: {
      path: config?.storage.path ?? "",
      backupRetentionDays: config?.storage.backupRetentionDays ?? 7,
    },
  });

  const save = form.handleSubmit(async (values) => {
    await Promise.all([
      setConfigValue.mutateAsync({ key: "storage.path", value: values.path }),
      setConfigValue.mutateAsync({
        key: "storage.backup_retention_days",
        value: String(values.backupRetentionDays),
      }),
    ]);
    toast.success(t("settings.saved"));
  });

  return (
    <form className="flex flex-col gap-6" onSubmit={save}>
      <FormSection title={t("settings.tab.storage")}>
        <label className="flex flex-col gap-1.5">
          <span className="text-sm">{t("settings.storage.path")}</span>
          <PathField
            value={form.watch("path")}
            onChange={(value) =>
              form.setValue("path", value, { shouldDirty: true })
            }
            placeholder={
              defaults.data
                ? t("settings.pathDefault", { path: defaults.data.storage })
                : undefined
            }
          />
        </label>

        <label className="flex flex-col gap-1.5">
          <span className="text-sm">{t("settings.storage.retention")}</span>
          <NumberField
            value={form.watch("backupRetentionDays")}
            onChange={(next) =>
              form.setValue("backupRetentionDays", next, { shouldDirty: true })
            }
            min={0}
          />
        </label>
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
