import { useState } from "react";
import { Eye, EyeOff, Plug, Save } from "lucide-react";
import { toast } from "sonner";

import { PathField } from "@/components/common/PathField";
import { FieldRow, FormSection, Hint } from "@/components/common/Surfaces";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import {
  useClearSourceApiKey,
  useSetConfigValue,
  useSetSourceApiKey,
  useTestSourceConnection,
} from "@/hooks/mutations";
import { useSourcesQuery } from "@/hooks/queries";
import { t } from "@/lib/i18n";
import { useConfigStore } from "@/stores/config.store";
import type { ModSourceInfo } from "@/types/domain";

/** Credentials live here because two surfaces for one feature is where settings drift. */
interface SourceCardProps {
  source: ModSourceInfo;
}

function SourceCard({ source }: SourceCardProps) {
  const config = useConfigStore((state) => state.config);
  const setConfigValue = useSetConfigValue();
  const setKey = useSetSourceApiKey();
  const clearKey = useClearSourceApiKey();
  const test = useTestSourceConnection();

  const [paths, setPaths] = useState(config?.sources[source.id]?.paths ?? []);
  const [key, setKeyValue] = useState("");
  const [revealKey, setRevealKey] = useState(false);

  const addPath = () => {
    setPaths((current) => [...current, ""]);
  };

  const setPathAt = (index: number, value: string) => {
    setPaths((current) =>
      current.map((entry, at) => (at === index ? value : entry))
    );
  };

  const removePathAt = (index: number) => {
    setPaths((current) => current.filter((_, at) => at !== index));
  };

  const save = async () => {
    await setConfigValue.mutateAsync({
      key: `sources.${source.id}.enabled`,
      value: String(config?.sources[source.id]?.enabled ?? true),
    });

    // `paths` is a list key, so the whole list goes back as one joined string.
    const cleaned = paths.map((entry) => entry.trim()).filter(Boolean);
    await setConfigValue.mutateAsync({
      key: `sources.${source.id}.paths`,
      value: cleaned.join(","),
    });

    if (key.trim() !== "") {
      await setKey.mutateAsync({ sourceId: source.id, apiKey: key.trim() });
      setKeyValue("");
    }

    toast.success(t("settings.saved"));
  };

  return (
    <div className="flex flex-col gap-3 rounded-md border p-3">
      <div className="flex items-center justify-between gap-3">
        <div className="min-w-0">
          <p className="truncate text-sm font-medium">{source.name}</p>
          <p className="text-xs text-muted-foreground">{source.description}</p>
        </div>
        <div className="flex shrink-0 items-center gap-1.5">
          {source.authenticated ? (
            <Badge variant="secondary">
              {t("settings.sources.authenticated")}
            </Badge>
          ) : null}
          <Badge variant="outline">{source.version}</Badge>
        </div>
      </div>

      <FieldRow
        label={t("settings.sources.enabled")}
        htmlFor={`enabled-${source.id}`}
      >
        <Switch
          id={`enabled-${source.id}`}
          checked={config?.sources[source.id]?.enabled ?? true}
          onCheckedChange={(on) =>
            setConfigValue.mutate({
              key: `sources.${source.id}.enabled`,
              value: String(on),
            })
          }
        />
      </FieldRow>

      {source.requiresApiKey || source.hasApiKey ? (
        <FieldRow
          label={t("settings.sources.apiKey")}
          htmlFor={`key-${source.id}`}
          description={
            source.hasApiKey ? t("settings.sources.keyStored") : undefined
          }
        >
          <div className="flex items-center gap-2">
            <Input
              id={`key-${source.id}`}
              type={revealKey ? "text" : "password"}
              value={key}
              placeholder={
                source.hasApiKey ? t("settings.sources.keyStored") : ""
              }
              onChange={(event) => setKeyValue(event.target.value)}
            />
            <Button
              type="button"
              size="icon"
              variant="ghost"
              onClick={() => setRevealKey((on) => !on)}
              aria-label={t("settings.sources.revealKey")}
            >
              {revealKey ? (
                <EyeOff className="size-4" />
              ) : (
                <Eye className="size-4" />
              )}
            </Button>
            {source.hasApiKey ? (
              <Button
                type="button"
                size="sm"
                variant="ghost"
                onClick={() => clearKey.mutate(source.id)}
                disabled={clearKey.isPending}
              >
                {t("common.clear")}
              </Button>
            ) : null}
          </div>
        </FieldRow>
      ) : null}

      <div className="flex flex-col gap-2">
        <div className="flex items-center justify-between">
          <span className="text-sm">{t("settings.sources.paths")}</span>
          <Button type="button" size="sm" variant="ghost" onClick={addPath}>
            {t("common.add")}
          </Button>
        </div>
        {paths.length === 0 ? (
          <Hint>{t("settings.sources.noPaths")}</Hint>
        ) : null}
        {paths.map((entry, index) => (
          <div key={index} className="flex items-center gap-2">
            <div className="min-w-0 flex-1">
              <PathField
                value={entry}
                onChange={(value) => setPathAt(index, value)}
              />
            </div>
            <Button
              type="button"
              size="sm"
              variant="ghost"
              onClick={() => removePathAt(index)}
            >
              {t("common.remove")}
            </Button>
          </div>
        ))}
      </div>

      <div className="flex items-center gap-2">
        <Button
          type="button"
          size="sm"
          variant="outline"
          onClick={() => test.mutate(source.id)}
          loading={test.isPending}
        >
          <Plug className="size-3.5" />
          {t("settings.sources.test")}
        </Button>
        <Button
          type="button"
          size="sm"
          onClick={save}
          loading={setConfigValue.isPending || setKey.isPending}
        >
          <Save className="size-3.5" />
          {t("settings.save")}
        </Button>
      </div>
    </div>
  );
}

export function SourcesTab() {
  const sources = useSourcesQuery();

  return (
    <div className="flex flex-col gap-6">
      <FormSection
        title={t("settings.tab.sources")}
        description={t("settings.sources.hint")}
      >
        {sources.data?.map((source) => (
          <SourceCard key={source.id} source={source} />
        )) ?? (
          <p className="text-sm text-muted-foreground">{t("common.loading")}</p>
        )}
      </FormSection>
    </div>
  );
}
