import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { Save } from "lucide-react";
import { toast } from "sonner";
import { z } from "zod";

import { NumberField } from "@/components/common/NumberField";
import { FieldRow, FormSection, Hint } from "@/components/common/Surfaces";
import { Button } from "@/components/ui/button";
import { useSaveConfig } from "@/hooks/mutations";
import { t } from "@/lib/i18n";
import { useConfigStore } from "@/stores/config.store";

const sharingSchema = z.object({
  stunServers: z.string(),
  turnRelays: z.string(),
  expiryDays: z.number().int().min(0),
  chunkSizeKb: z.number().int().min(0),
});

type SharingValues = z.infer<typeof sharingSchema>;

const lines = (value: string[]) => value.join("\n");

/**
 * This tab saves the whole config because `sharing.p2p.*` has no settable key
 * on the backend: the per-key writer validates a fixed key list.
 */
export function SharingTab() {
  const config = useConfigStore((state) => state.config);
  const saveConfig = useSaveConfig();

  const form = useForm<SharingValues>({
    resolver: zodResolver(sharingSchema),
    values: {
      stunServers: lines(config?.sharing.p2p.stunServers ?? []),
      turnRelays: lines(config?.sharing.p2p.turnRelays ?? []),
      expiryDays: config?.sharing.p2p.expiryDays ?? 7,
      chunkSizeKb: config?.sharing.p2p.chunkSizeKb ?? 0,
    },
  });

  const save = form.handleSubmit(async (values) => {
    if (!config) return;

    await saveConfig.mutateAsync({
      ...config,
      sharing: {
        p2p: {
          stunServers: split(values.stunServers),
          turnRelays: split(values.turnRelays),
          expiryDays: values.expiryDays,
          chunkSizeKb: values.chunkSizeKb > 0 ? values.chunkSizeKb : undefined,
        },
      },
    });
    toast.success(t("settings.saved"));
  });

  return (
    <form className="flex flex-col gap-6" onSubmit={save}>
      <FormSection title={t("settings.tab.sharing")}>
        <FieldRow
          label={t("settings.sharing.stun")}
          htmlFor="sharing-stun"
          description={t("settings.sharing.stunHint")}
        >
          <textarea
            id="sharing-stun"
            className="min-h-20 rounded-md border bg-transparent px-3 py-2 font-mono text-xs"
            value={form.watch("stunServers")}
            onChange={(event) =>
              form.setValue("stunServers", event.target.value, {
                shouldDirty: true,
              })
            }
          />
        </FieldRow>

        <FieldRow
          label={t("settings.sharing.turn")}
          htmlFor="sharing-turn"
          description={t("settings.sharing.turnHint")}
        >
          <textarea
            id="sharing-turn"
            className="min-h-16 rounded-md border bg-transparent px-3 py-2 font-mono text-xs"
            value={form.watch("turnRelays")}
            onChange={(event) =>
              form.setValue("turnRelays", event.target.value, {
                shouldDirty: true,
              })
            }
          />
        </FieldRow>

        <div className="grid grid-cols-2 gap-3">
          <label className="flex flex-col gap-1.5">
            <span className="text-sm">{t("settings.sharing.expiry")}</span>
            <NumberField
              value={form.watch("expiryDays")}
              onChange={(next) =>
                form.setValue("expiryDays", next, { shouldDirty: true })
              }
              min={0}
            />
          </label>
          <label className="flex flex-col gap-1.5">
            <span className="text-sm">{t("settings.sharing.chunk")}</span>
            <NumberField
              value={form.watch("chunkSizeKb")}
              onChange={(next) =>
                form.setValue("chunkSizeKb", next, { shouldDirty: true })
              }
              min={0}
            />
          </label>
        </div>

        {/* Cloud sharing was deprecated when peer-to-peer landed, so a revoke button would be dead. */}
        <Hint>{t("settings.sharing.cloudReadOnly")}</Hint>
      </FormSection>

      <div className="flex justify-end">
        <Button
          type="submit"
          disabled={!form.formState.isDirty}
          loading={saveConfig.isPending}
        >
          <Save className="size-4" />
          {t("settings.save")}
        </Button>
      </div>
    </form>
  );
}

function split(value: string): string[] {
  return value
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean);
}
