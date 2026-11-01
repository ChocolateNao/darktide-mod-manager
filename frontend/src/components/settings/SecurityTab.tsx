import { useState } from "react";

import { NumberField } from "@/components/common/NumberField";
import { FormSection, Hint } from "@/components/common/Surfaces";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { useSetConfigValue } from "@/hooks/mutations";
import { formatDurationSeconds, parseDurationSeconds } from "@/lib/format";
import { t } from "@/lib/i18n";
import { useConfigStore } from "@/stores/config.store";
import { selectGames, useGamesStore } from "@/stores/games.store";
import { selectMods, useModsStore } from "@/stores/mods.store";
import type { ScriptPermissionKey, ScriptPermissions } from "@/types/domain";

/* `hooks.permissions` is a three-level matrix merged by `For(gameID, modID)`, edited as a scoped editor. */
const PERMISSION_KEYS: ScriptPermissionKey[] = [
  "fsRead",
  "fsWrite",
  "configRead",
  "configWrite",
  "storageRead",
  "envRead",
];

const ALL_OFF: ScriptPermissions = {
  fsRead: false,
  fsWrite: false,
  configRead: false,
  configWrite: false,
  storageRead: false,
  envRead: false,
};

type Scope =
  | { kind: "default" }
  | { kind: "game"; id: string }
  | { kind: "mod"; id: string };

/** Mirrors `HooksPermissionsConfig.For`: the narrowest scope that is set wins. */
function effectivePermissions(
  config: ReturnType<typeof useConfigStore.getState>["config"],
  scope: Scope
): ScriptPermissions {
  const permissions = config?.hooks.permissions;
  if (!permissions) return ALL_OFF;

  if (scope.kind === "mod")
    return (
      permissions.mods[scope.id] ??
      permissions.games[config?.activeGame ?? ""] ??
      permissions.default
    );
  if (scope.kind === "game")
    return permissions.games[scope.id] ?? permissions.default;
  return permissions.default;
}

function scopeKey(scope: Scope): string {
  return scope.kind === "default" ? "default" : `${scope.kind}.${scope.id}`;
}

function permissionPrefix(scope: Scope): string {
  if (scope.kind === "default") return "hooks.permissions.default";
  if (scope.kind === "game") return `hooks.permissions.games.${scope.id}`;
  return `hooks.permissions.mods.${scope.id}`;
}

/** Wire name (`fs_read`) for a camelCase permission key. */
function wirePermission(key: ScriptPermissionKey): string {
  return key.replace(/[A-Z]/g, (letter) => `_${letter.toLowerCase()}`);
}

export function SecurityTab() {
  const config = useConfigStore((state) => state.config);
  const games = useGamesStore(selectGames);
  const mods = useModsStore(selectMods);
  const setConfigValue = useSetConfigValue();

  const [scope, setScope] = useState<Scope>({ kind: "default" });

  const effective = effectivePermissions(config, scope);
  const prefix = permissionPrefix(scope);

  const setPermission = (key: ScriptPermissionKey, on: boolean) => {
    setConfigValue.mutate({
      key: `${prefix}.${wirePermission(key)}`,
      value: String(on),
    });
  };

  const setTimeoutSeconds = (value: string) => {
    const seconds = parseDurationSeconds(value);
    if (seconds === null) return;

    setConfigValue.mutate({
      key: `${prefix}.timeout_seconds`,
      value: String(Math.round(seconds)),
    });
  };

  return (
    <div className="flex flex-col gap-6">
      <FormSection
        title={t("settings.security.commands")}
        description={t("settings.security.commandsHint")}
      >
        <label className="flex items-center justify-between gap-4 rounded-md border px-3 py-2">
          <span className="text-sm">
            {t("settings.security.allowCommands")}
          </span>
          <Switch
            checked={config?.security.allowActionCommands ?? true}
            onCheckedChange={(on) =>
              setConfigValue.mutate({
                key: "security.allow_action_commands",
                value: String(on),
              })
            }
          />
        </label>
      </FormSection>

      <FormSection
        title={t("settings.hooks.permissions")}
        description={t("settings.hooks.precedence")}
      >
        <div className="flex flex-col gap-3">
          <label className="flex w-72 flex-col gap-1.5">
            <span className="text-sm">{t("settings.hooks.scope")}</span>
            <Select
              value={scopeKey(scope)}
              onValueChange={(value) => {
                const [kind, ...rest] = value.split(".");
                const id = rest.join(".");
                if (kind === "default") setScope({ kind: "default" });
                else if (kind === "game") setScope({ kind: "game", id });
                else setScope({ kind: "mod", id });
              }}
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="default">
                  {t("settings.hooks.scopeDefault")}
                </SelectItem>
                {games.map((game) => (
                  <SelectItem key={game.id} value={`game.${game.id}`}>
                    {game.displayName}
                  </SelectItem>
                ))}
                {mods.map((mod) => (
                  <SelectItem key={mod.id} value={`mod.${mod.id}`}>
                    {mod.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </label>

          <div className="grid grid-cols-2 gap-3 sm:grid-cols-3">
            {PERMISSION_KEYS.map((key) => (
              <label
                key={key}
                className="flex items-center justify-between gap-2 rounded-md border px-3 py-2"
              >
                <span className="text-xs">{t(`settings.hooks.${key}`)}</span>
                <Switch
                  checked={effective[key]}
                  onCheckedChange={(on) => setPermission(key, on)}
                />
              </label>
            ))}
          </div>

          <div className="flex items-center gap-2">
            <label className="flex items-center gap-2">
              <span className="text-xs">{t("settings.hooks.timeout")}</span>
              <NumberField
                value={effective.timeoutSeconds ?? 0}
                onChange={(next) => setTimeoutSeconds(String(next))}
                min={0}
                step={5}
                unit="seconds"
                className="w-32"
              />
            </label>
            <span className="text-xs text-muted-foreground">
              {effective.timeoutSeconds
                ? t("settings.hooks.effectiveTimeout", {
                    time: formatDurationSeconds(effective.timeoutSeconds),
                  })
                : t("settings.hooks.inheritsTimeout")}
            </span>
          </div>
        </div>
      </FormSection>

      <Hint>{t("settings.hooks.scopeHint")}</Hint>
    </div>
  );
}
