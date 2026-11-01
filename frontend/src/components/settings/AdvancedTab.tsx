import { useState } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { AlertTriangle, FolderOpen, Save, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { z } from "zod";

import { copyToClipboard, revealPath } from "@/bridge/dialogs";
import { FieldRow, FormSection } from "@/components/common/Surfaces";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Switch } from "@/components/ui/switch";
import {
  useDeleteAllData,
  useDeleteData,
  useSetConfigValue,
} from "@/hooks/mutations";
import { useConfigPathQuery, useConfigQuery } from "@/hooks/queries";
import { t } from "@/lib/i18n";

const advancedSchema = z.object({
  enableExperimental: z.boolean(),
});

type AdvancedValues = z.infer<typeof advancedSchema>;

export function AdvancedTab() {
  const config = useConfigQuery();
  const configPath = useConfigPathQuery();
  const setConfigValue = useSetConfigValue();
  const deleteAll = useDeleteAllData();
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [confirmStep, setConfirmStep] = useState(1);
  const [acknowledged, setAcknowledged] = useState(false);
  const [deleteDataOpen, setDeleteDataOpen] = useState(false);

  const form = useForm<AdvancedValues>({
    resolver: zodResolver(advancedSchema),
    values: {
      enableExperimental: config.data?.advanced.enableExperimental ?? false,
    },
  });

  const configPathValue = configPath.data ?? "";
  const configFolder = configPathValue.replace(/[\\/][^\\/]+$/, "");

  const save = form.handleSubmit(async () => {
    await setConfigValue.mutateAsync({
      key: "advanced.enable_experimental",
      value: String(form.getValues("enableExperimental")),
    });

    toast.success(t("settings.saved"));
  });

  const closeConfirm = () => {
    setConfirmOpen(false);
    setConfirmStep(1);
    setAcknowledged(false);
  };

  return (
    <form className="flex flex-col gap-6" onSubmit={save}>
      <FormSection title={t("settings.tab.advanced")}>
        <FieldRow
          label={t("settings.advanced.experimental")}
          htmlFor="advanced-experimental"
          details={t("settings.advanced.experimentalDetail")}
        >
          <Switch
            id="advanced-experimental"
            checked={form.watch("enableExperimental")}
            onCheckedChange={(on) =>
              form.setValue("enableExperimental", on, { shouldDirty: true })
            }
          />
        </FieldRow>
      </FormSection>

      <div className="flex items-center justify-between gap-3">
        <span className="text-xs text-muted-foreground">
          {t("settings.configPath")}
        </span>
        <div className="flex min-w-0 items-center gap-2">
          <code className="truncate text-xs" data-selectable>
            {configPathValue}
          </code>
          <Button
            type="button"
            size="sm"
            variant="ghost"
            disabled={!configPathValue}
            onClick={() => void copyToClipboard(configPathValue)}
          >
            {t("common.copyPath")}
          </Button>
          <Button
            type="button"
            size="sm"
            variant="ghost"
            disabled={!configFolder}
            onClick={() => void revealPath(configFolder)}
          >
            <FolderOpen className="size-3.5" />
            {t("settings.openConfigFolder")}
          </Button>
        </div>
      </div>

      <FormSection title={t("settings.advanced.dangerZone")}>
        <div className="flex flex-col gap-4">
          <FieldRow
            className="items-center"
            label={t("settings.advanced.deleteData")}
            details={t("settings.advanced.deleteDataDetail")}
          >
            <Button
              type="button"
              variant="outline"
              onClick={() => setDeleteDataOpen(true)}
            >
              <Trash2 className="size-4" />
              {t("settings.advanced.deleteData")}
            </Button>
          </FieldRow>
          <FieldRow
            className="items-center"
            label={t("settings.advanced.deleteAll")}
            details={t("settings.advanced.deleteAllDetail")}
          >
            <Button
              type="button"
              variant="destructive"
              onClick={() => setConfirmOpen(true)}
            >
              <Trash2 className="size-4" />
              {t("settings.advanced.deleteAll")}
            </Button>
          </FieldRow>
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
      <AlertDialog
        open={confirmOpen}
        onOpenChange={(next) => {
          if (!next) closeConfirm();
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="flex items-center gap-2">
              <AlertTriangle className="size-4 text-destructive" />
              {confirmStep === 1
                ? t("settings.advanced.deleteAllTitle")
                : t("settings.advanced.deleteAllConfirmTitle")}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {confirmStep === 1
                ? t("settings.advanced.deleteAllHint")
                : t("settings.advanced.deleteAllConfirmHint")}
            </AlertDialogDescription>
          </AlertDialogHeader>

          {confirmStep === 1 ? (
            <ul className="list-disc space-y-1 pl-5 text-sm text-muted-foreground">
              <li>{t("settings.advanced.deleteAllMods")}</li>
              <li>{t("settings.advanced.deleteAllCache")}</li>
              <li>{t("settings.advanced.deleteAllSettings")}</li>
            </ul>
          ) : (
            <label className="flex items-start gap-2 text-sm">
              <Checkbox
                checked={acknowledged}
                onCheckedChange={(checked) => setAcknowledged(checked === true)}
              />
              <span>{t("settings.advanced.deleteAllAcknowledge")}</span>
            </label>
          )}

          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            {confirmStep === 1 ? (
              <Button type="button" onClick={() => setConfirmStep(2)}>
                {t("common.continue")}
              </Button>
            ) : (
              <>
                <Button
                  type="button"
                  variant="ghost"
                  onClick={() => setConfirmStep(1)}
                >
                  {t("common.back")}
                </Button>
                <Button
                  type="button"
                  variant="destructive"
                  disabled={!acknowledged}
                  loading={deleteAll.isPending}
                  onClick={() =>
                    deleteAll.mutate(undefined, { onSuccess: closeConfirm })
                  }
                >
                  <Trash2 className="size-4" />
                  {t("settings.advanced.deleteAllConfirm")}
                </Button>
              </>
            )}
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      <DeleteDataDialog
        open={deleteDataOpen}
        onOpenChange={setDeleteDataOpen}
      />
    </form>
  );
}

interface DeleteDataDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

/**
 * The less destructive reset: the user picks mod data, the configuration file,
 * or both, then confirms once more before anything is removed.
 */
export function DeleteDataDialog({
  open,
  onOpenChange,
}: DeleteDataDialogProps) {
  const deleteData = useDeleteData();
  const [step, setStep] = useState(1);
  const [modData, setModData] = useState(false);
  const [config, setConfig] = useState(false);

  const close = () => {
    onOpenChange(false);
    setStep(1);
    setModData(false);
    setConfig(false);
  };

  return (
    <AlertDialog
      open={open}
      onOpenChange={(next) => {
        if (!next) close();
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle className="flex items-center gap-2">
            <AlertTriangle className="size-4 text-destructive" />
            {step === 1
              ? t("settings.advanced.deleteDataTitle")
              : t("settings.advanced.deleteDataConfirmTitle")}
          </AlertDialogTitle>
          <AlertDialogDescription>
            {step === 1
              ? t("settings.advanced.deleteDataHint")
              : t("settings.advanced.deleteDataConfirmHint")}
          </AlertDialogDescription>
        </AlertDialogHeader>

        {step === 1 ? (
          <div className="flex flex-col gap-3">
            <label className="flex items-start gap-2 text-sm">
              <Checkbox
                checked={modData}
                onCheckedChange={(checked) => setModData(checked === true)}
              />
              <span className="flex flex-col gap-0.5">
                <span>{t("settings.advanced.deleteDataMods")}</span>
                <span className="text-xs text-muted-foreground">
                  {t("settings.advanced.deleteDataModsDetail")}
                </span>
              </span>
            </label>
            <label className="flex items-start gap-2 text-sm">
              <Checkbox
                checked={config}
                onCheckedChange={(checked) => setConfig(checked === true)}
              />
              <span className="flex flex-col gap-0.5">
                <span>{t("settings.advanced.deleteDataConfig")}</span>
                <span className="text-xs text-muted-foreground">
                  {t("settings.advanced.deleteDataConfigDetail")}
                </span>
              </span>
            </label>
          </div>
        ) : (
          <ul className="list-disc space-y-1 pl-5 text-sm text-muted-foreground">
            {modData && <li>{t("settings.advanced.deleteDataMods")}</li>}
            {config && <li>{t("settings.advanced.deleteDataConfig")}</li>}
          </ul>
        )}

        <AlertDialogFooter>
          <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
          {step === 1 ? (
            <Button
              type="button"
              disabled={!modData && !config}
              onClick={() => setStep(2)}
            >
              {t("common.continue")}
            </Button>
          ) : (
            <>
              <Button type="button" variant="ghost" onClick={() => setStep(1)}>
                {t("common.back")}
              </Button>
              <Button
                type="button"
                variant="destructive"
                loading={deleteData.isPending}
                onClick={() =>
                  deleteData.mutate({ modData, config }, { onSuccess: close })
                }
              >
                <Trash2 className="size-4" />
                {t("settings.advanced.deleteDataConfirm")}
              </Button>
            </>
          )}
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
