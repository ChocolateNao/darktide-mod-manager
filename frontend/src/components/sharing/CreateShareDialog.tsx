import { useEffect, useMemo, useState } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";

import { NumberField } from "@/components/common/NumberField";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { useCreateShare } from "@/hooks/mutations";
import { useDefaultStunQuery } from "@/hooks/queries";
import { t } from "@/lib/i18n";
import { selectMods, useModsStore } from "@/stores/mods.store";
import { useSharingStore } from "@/stores/sharing.store";

/** Clears the threshold, and STUN stays a textarea because the backend already treats it as a slice. */
const createSchema = z.object({
  modIds: z.array(z.string()).min(1, { message: "share.noModsPicked" }),
  senderName: z.string().trim().max(64, { message: "actions.invalidValue" }),
  expiryDays: z.number().int().min(0).max(365),
  stunServers: z.string(),
  useDefaults: z.boolean(),
});

type CreateValues = z.infer<typeof createSchema>;

interface CreateShareDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

export function CreateShareDialog({
  open,
  onOpenChange,
}: CreateShareDialogProps) {
  const mods = useModsStore(selectMods);
  const setDraftOfferLink = useSharingStore((state) => state.setDraftOfferLink);
  const setFocus = useSharingStore((state) => state.setFocus);
  const stun = useDefaultStunQuery();
  const create = useCreateShare();
  const [defaultsApplied, setDefaultsApplied] = useState(false);
  const [query, setQuery] = useState("");

  /* External mods have no source record to send from. */
  const known = useMemo(() => mods.filter((mod) => !mod.external), [mods]);
  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase();
    if (!needle) return known;

    return known.filter((mod) => mod.name.toLowerCase().includes(needle));
  }, [known, query]);

  const form = useForm<CreateValues>({
    resolver: zodResolver(createSchema),
    defaultValues: {
      modIds: [],
      senderName: "",
      expiryDays: 7,
      stunServers: "",
      useDefaults: true,
    },
  });

  // The default STUN list is a backend decision (`DefaultSTUNServers`).
  useEffect(() => {
    if (defaultsApplied || !stun.data) return;
    setDefaultsApplied(true);
    form.setValue("stunServers", stun.data.join("\n"));
  }, [defaultsApplied, form, stun.data]);

  const selected = form.watch("modIds");
  const useDefaults = form.watch("useDefaults");
  const expiryDays = form.watch("expiryDays");

  const toggleMod = (id: string, on: boolean) => {
    form.setValue(
      "modIds",
      on ? [...selected, id] : selected.filter((entry) => entry !== id),
      { shouldDirty: true, shouldValidate: true }
    );
  };

  const submit = form.handleSubmit(async (values) => {
    const created = await create.mutateAsync({
      modIds: values.modIds,
      // Zero expiry days means "never", which the bridge spells as an empty string.
      expiresAt:
        values.expiryDays > 0
          ? new Date(Date.now() + values.expiryDays * 86_400_000).toISOString()
          : "",
      senderName: values.senderName,
      stunServers: values.useDefaults
        ? (stun.data ?? [])
        : values.stunServers
            .split("\n")
            .map((line) => line.trim())
            .filter(Boolean),
    });

    setDraftOfferLink(created.link);
    setFocus("send");
    onOpenChange(false);
  });

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{t("share.create")}</DialogTitle>
          <DialogDescription>{t("share.bothEnds")}</DialogDescription>
        </DialogHeader>

        <form className="flex flex-col gap-4" onSubmit={submit}>
          <div className="flex flex-col gap-2">
            <Input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder={t("share.searchMods")}
              aria-label={t("share.searchMods")}
            />
            <div
              className="max-h-56 overflow-y-auto rounded-md border"
              data-selectable
            >
              {known.length === 0 ? (
                <p className="p-3 text-sm text-muted-foreground">
                  {t("mods.empty")}
                </p>
              ) : filtered.length === 0 ? (
                <p className="p-3 text-sm text-muted-foreground">
                  {t("share.noMatches")}
                </p>
              ) : (
                filtered.map((mod) => (
                  <label
                    key={mod.id}
                    className="flex cursor-pointer items-center gap-3 border-b px-3 py-1.5 last:border-b-0"
                  >
                    <Switch
                      checked={selected.includes(mod.id)}
                      onCheckedChange={(on) => toggleMod(mod.id, on)}
                    />
                    <span className="min-w-0 flex-1 truncate text-sm">
                      {mod.name}
                    </span>
                    <span className="text-xs text-muted-foreground">
                      {mod.version}
                    </span>
                  </label>
                ))
              )}
            </div>
          </div>
          {/* Reserved line: showing the error must not shove the form down. */}
          <p className="min-h-4 text-xs text-destructive" aria-live="polite">
            {form.formState.errors.modIds ? t("share.noModsPicked") : null}
          </p>

          <div className="grid grid-cols-2 gap-3">
            <label className="flex flex-col gap-1.5">
              <span className="text-sm">{t("share.yourName")}</span>
              <Input
                {...form.register("senderName")}
                placeholder={t("common.name")}
              />
            </label>
            <label className="flex flex-col gap-1.5">
              <span className="text-sm">{t("share.expiryDays")}</span>
              <NumberField
                value={expiryDays}
                min={0}
                max={365}
                onChange={(next) =>
                  form.setValue("expiryDays", next, {
                    shouldDirty: true,
                    shouldValidate: true,
                  })
                }
                aria-label={t("share.expiryDays")}
              />
            </label>
          </div>

          <label className="flex items-center gap-2">
            <Switch
              checked={useDefaults}
              onCheckedChange={(on) =>
                form.setValue("useDefaults", on, { shouldDirty: true })
              }
            />
            <span className="text-sm">{t("share.useDefaultStun")}</span>
          </label>

          {!useDefaults ? (
            <label className="flex flex-col gap-1.5">
              <span className="text-sm">{t("share.stunServers")}</span>
              <textarea
                className="min-h-20 rounded-md border bg-transparent px-3 py-2 text-sm"
                value={form.watch("stunServers")}
                onChange={(event) =>
                  form.setValue("stunServers", event.target.value, {
                    shouldDirty: true,
                  })
                }
              />
            </label>
          ) : null}

          <p className="text-xs text-muted-foreground">{t("share.stunHint")}</p>

          <DialogFooter>
            <Button
              type="button"
              variant="ghost"
              onClick={() => onOpenChange(false)}
            >
              {t("common.cancel")}
            </Button>
            <Button
              type="submit"
              disabled={selected.length === 0}
              loading={create.isPending}
            >
              {t("share.create")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
