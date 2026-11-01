import { Apple } from "lucide-react";
import { toast } from "sonner";

import {
  NOTIFICATION_CATEGORIES,
  requestNotificationAuthorization,
} from "@/bridge/notifications";
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
import { useConfigQuery } from "@/hooks/queries";
import { t } from "@/lib/i18n";
import { selectConfig, useConfigStore } from "@/stores/config.store";
import {
  selectAvailability,
  selectMacAuthorized,
  selectToggles,
  useNotificationsStore,
} from "@/stores/notifications.store";

const THEMES = ["light", "dark", "system"] as const;

/**
 * Three toggles plus a theme: no form library and no
 * Save button, since each control writes through immediately.
 */
export function AppTab() {
  const config = useConfigStore(selectConfig);
  useConfigQuery();
  const setConfigValue = useSetConfigValue();

  const toggles = useNotificationsStore(selectToggles);
  const authorized = useNotificationsStore(selectMacAuthorized);
  const availability = useNotificationsStore(selectAvailability);
  const setToggle = useNotificationsStore((state) => state.setToggle);
  const setAuthorized = useNotificationsStore((state) => state.setAuthorized);

  const write = (key: string, value: string) => {
    setConfigValue.mutate(
      { key, value },
      { onSuccess: () => toast.success(t("settings.saved")) }
    );
  };

  const askMacOS = async () => {
    const granted = await requestNotificationAuthorization();
    setAuthorized(granted);
    if (!granted) toast.error(t("settings.notify.macDenied"));
  };

  return (
    <div className="flex flex-col gap-6">
      <FormSection title={t("settings.tab.app")}>
        <FieldRow label={t("settings.theme")} htmlFor="ui-theme">
          <Select
            value={config?.ui.theme ?? "system"}
            onValueChange={(value) => write("ui.theme", value)}
          >
            <SelectTrigger id="ui-theme" className="w-48">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {THEMES.map((theme) => (
                <SelectItem key={theme} value={theme}>
                  {t(`settings.theme.${theme}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </FieldRow>

        <FieldRow label={t("settings.language")} htmlFor="ui-language">
          <Select
            value={config?.ui.language ?? "en"}
            onValueChange={(value) => write("ui.language", value)}
          >
            <SelectTrigger id="ui-language" className="w-48">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="en">English</SelectItem>
            </SelectContent>
          </Select>
        </FieldRow>

        <FieldRow label={t("settings.compact")} htmlFor="ui-compact">
          <Switch
            id="ui-compact"
            checked={config?.ui.compactMode ?? false}
            onCheckedChange={(on) => write("ui.compact_mode", String(on))}
          />
        </FieldRow>
      </FormSection>

      <FormSection
        title={t("settings.notifications")}
        description={t("settings.notify.intro")}
      >
        {NOTIFICATION_CATEGORIES.map((category) => (
          <FieldRow
            key={category}
            label={t(`settings.notify.category.${category}`)}
            htmlFor={`notify-${category}`}
          >
            <Switch
              id={`notify-${category}`}
              checked={toggles[category]}
              onCheckedChange={(on) => setToggle(category, on)}
            />
          </FieldRow>
        ))}

        {/* `RequestNotificationAuthorization` is a no-op off macOS, so the control is macOS-only. */}
        {availability === false ? (
          <Hint>{t("settings.notify.unavailable")}</Hint>
        ) : null}

        {typeof navigator !== "undefined" && /Mac/.test(navigator.userAgent) ? (
          <FieldRow
            label={t("settings.notify.macAuthorize")}
            htmlFor="notify-authorize"
          >
            <div className="flex items-center gap-2">
              <Button
                id="notify-authorize"
                size="sm"
                variant="outline"
                onClick={askMacOS}
              >
                <Apple className="size-4" />
                {t("settings.notify.macAuthorize")}
              </Button>
              <span className="text-xs text-muted-foreground">
                {authorized === null
                  ? t("settings.notify.macUnknown")
                  : authorized
                    ? t("settings.notify.macAuthorized")
                    : t("settings.notify.macDenied")}
              </span>
            </div>
          </FieldRow>
        ) : null}
      </FormSection>
    </div>
  );
}
