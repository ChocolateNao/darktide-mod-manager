import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { Save, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { z } from "zod";

import { NumberField } from "@/components/common/NumberField";
import { PathField } from "@/components/common/PathField";
import { FieldRow, FormSection, Hint } from "@/components/common/Surfaces";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import {
  useClearCache,
  usePruneCache,
  useSetConfigValue,
} from "@/hooks/mutations";
import { useCacheInfoQuery, useDefaultPathsQuery } from "@/hooks/queries";
import { formatBytes, formatDateTime } from "@/lib/format";
import { t } from "@/lib/i18n";
import { useConfigStore } from "@/stores/config.store";

const cacheSchema = z.object({
  path: z.string(),
  maxSizeGb: z.number().int().min(0),
  maxAgeDays: z.number().int().min(0),
  verifyHashes: z.boolean(),
});

type CacheValues = z.infer<typeof cacheSchema>;

export function CacheTab() {
  const config = useConfigStore((state) => state.config);
  const defaults = useDefaultPathsQuery();
  const setConfigValue = useSetConfigValue();
  const info = useCacheInfoQuery();
  const prune = usePruneCache();
  const clear = useClearCache();

  const form = useForm<CacheValues>({
    resolver: zodResolver(cacheSchema),
    values: {
      path: config?.cache.path ?? "",
      maxSizeGb: config?.cache.maxSizeGb ?? 5,
      maxAgeDays: config?.cache.maxAgeDays ?? 30,
      verifyHashes: config?.cache.verifyHashes ?? true,
    },
  });

  const save = form.handleSubmit(async (values) => {
    await Promise.all([
      setConfigValue.mutateAsync({ key: "cache.path", value: values.path }),
      setConfigValue.mutateAsync({
        key: "cache.max_size_gb",
        value: String(values.maxSizeGb),
      }),
      setConfigValue.mutateAsync({
        key: "cache.max_age_days",
        value: String(values.maxAgeDays),
      }),
      setConfigValue.mutateAsync({
        key: "cache.verify_hashes",
        value: String(values.verifyHashes),
      }),
    ]);
    toast.success(t("settings.saved"));
  });

  return (
    <form className="flex flex-col gap-6" onSubmit={save}>
      <FormSection
        title={t("settings.tab.cache")}
        description={
          info.data
            ? t("settings.cache.info", {
                count: info.data.fileCount,
                size: formatBytes(info.data.totalSize),
                oldest: formatDateTime(info.data.oldest),
              })
            : undefined
        }
      >
        <label className="flex flex-col gap-1.5">
          <span className="text-sm">{t("settings.cache.path")}</span>
          <PathField
            value={form.watch("path")}
            onChange={(value) =>
              form.setValue("path", value, { shouldDirty: true })
            }
            placeholder={
              defaults.data
                ? t("settings.pathDefault", { path: defaults.data.cache })
                : undefined
            }
          />
        </label>

        <div className="grid grid-cols-2 gap-3">
          <label className="flex flex-col gap-1.5">
            <span className="text-sm">{t("settings.cache.maxSize")}</span>
            <NumberField
              value={form.watch("maxSizeGb")}
              onChange={(next) =>
                form.setValue("maxSizeGb", next, { shouldDirty: true })
              }
              min={0}
            />
          </label>
          <label className="flex flex-col gap-1.5">
            <span className="text-sm">{t("settings.cache.maxAge")}</span>
            <NumberField
              value={form.watch("maxAgeDays")}
              onChange={(next) =>
                form.setValue("maxAgeDays", next, { shouldDirty: true })
              }
              min={0}
            />
          </label>
        </div>

        <FieldRow label={t("settings.cache.verify")} htmlFor="cache-verify">
          <Switch
            id="cache-verify"
            checked={form.watch("verifyHashes")}
            onCheckedChange={(on) =>
              form.setValue("verifyHashes", on, { shouldDirty: true })
            }
          />
        </FieldRow>

        {/* Prune and Clear are distinct operations: one frees space by age and
            size, the other throws the cache away. They share no code path. */}
        <div className="flex items-center gap-2">
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={() => prune.mutate({ maxSizeGb: 0, maxAgeDays: 0 })}
            loading={prune.isPending}
          >
            {t("settings.cache.prune")}
          </Button>
          <Button
            type="button"
            size="sm"
            variant="ghost"
            onClick={() => clear.mutate()}
            loading={clear.isPending}
          >
            <Trash2 className="size-4" />
            {t("settings.cache.clear")}
          </Button>
        </div>

        <Hint>{t("settings.restartRequired")}</Hint>
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
