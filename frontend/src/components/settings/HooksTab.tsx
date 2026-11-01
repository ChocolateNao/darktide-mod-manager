import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { Save } from "lucide-react";
import { toast } from "sonner";
import { z } from "zod";

import { FormSection } from "@/components/common/Surfaces";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useSetConfigValue } from "@/hooks/mutations";
import { parseDurationSeconds } from "@/lib/format";
import { t } from "@/lib/i18n";
import { useConfigStore } from "@/stores/config.store";

const hooksSchema = z.object({
  defaultTimeout: z.string().min(1),
  maxTimeout: z.string().min(1),
});

type HooksValues = z.infer<typeof hooksSchema>;

export function HooksTab() {
  const config = useConfigStore((state) => state.config);
  const setConfigValue = useSetConfigValue();

  const form = useForm<HooksValues>({
    resolver: zodResolver(hooksSchema),
    values: {
      defaultTimeout: config?.hooks.defaultTimeout ?? "10s",
      maxTimeout: config?.hooks.maxTimeout ?? "30s",
    },
  });

  const save = form.handleSubmit(async (values) => {
    const defaultSeconds = parseDurationSeconds(values.defaultTimeout);
    const maxSeconds = parseDurationSeconds(values.maxTimeout);

    if (defaultSeconds === null || maxSeconds === null) {
      toast.error(t("settings.hooks.badDuration"));
      return;
    }

    // `hooks.default_timeout` and `hooks.max_timeout` are Duration values, so
    // they go back as Go duration strings (e.g. "10s").
    await setConfigValue.mutateAsync({
      key: "hooks.default_timeout",
      value: values.defaultTimeout,
    });
    await setConfigValue.mutateAsync({
      key: "hooks.max_timeout",
      value: values.maxTimeout,
    });
    toast.success(t("settings.saved"));
  });

  return (
    <form className="flex flex-col gap-6" onSubmit={save}>
      <FormSection title={t("settings.hooks.timeouts")}>
        <div className="grid grid-cols-2 gap-3">
          <label className="flex flex-col gap-1.5">
            <span className="text-sm">
              {t("settings.hooks.defaultTimeout")}
            </span>
            <Input {...form.register("defaultTimeout")} />
          </label>
          <label className="flex flex-col gap-1.5">
            <span className="text-sm">{t("settings.hooks.maxTimeout")}</span>
            <Input {...form.register("maxTimeout")} />
          </label>
        </div>
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
