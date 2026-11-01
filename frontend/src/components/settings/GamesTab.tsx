import { type ChangeEvent, useRef, useState } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { ImagePlus, Save } from "lucide-react";
import { toast } from "sonner";
import { z } from "zod";

import { GameIcon } from "@/components/common/GameIcon";
import { PathField } from "@/components/common/PathField";
import { FieldRow, FormSection, Hint } from "@/components/common/Surfaces";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { useSetConfigValue, useSwitchGame } from "@/hooks/mutations";
import { GAME_ICON_CHOICES } from "@/lib/gameAccent";
import { t } from "@/lib/i18n";
import { isCustomIcon, RailIconError, readRailIcon } from "@/lib/railIcon";
import { cn } from "@/lib/utils";
import { useConfigStore } from "@/stores/config.store";
import { selectGames, useGamesStore } from "@/stores/games.store";
import { useUiStore } from "@/stores/ui.store";

/** `games.<id>.*` is a config-key pattern, so the form follows whatever games the registry reports. */
const gamesSchema = z.object({
  activeGame: z.string(),
  entries: z.record(
    z.string(),
    z.object({ path: z.string(), icon: z.string(), autoDetect: z.boolean() })
  ),
});

type GamesValues = z.infer<typeof gamesSchema>;

export function GamesTab() {
  const config = useConfigStore((state) => state.config);
  const games = useGamesStore(selectGames);
  const setView = useUiStore((state) => state.setView);
  const setConfigValue = useSetConfigValue();
  const switchGame = useSwitchGame();
  const [pendingGame, setPendingGame] = useState<string | null>(null);
  const iconInput = useRef<HTMLInputElement>(null);
  const iconTarget = useRef<string | null>(null);

  const ids = games.map((game) => game.id);

  const form = useForm<GamesValues>({
    resolver: zodResolver(gamesSchema),
    values: {
      activeGame: config?.activeGame ?? "",
      entries: Object.fromEntries(
        ids.map((id) => [
          id,
          {
            path: config?.games[id]?.path ?? "",
            icon: config?.games[id]?.icon ?? "",
            autoDetect: config?.games[id]?.autoDetect ?? true,
          },
        ])
      ),
    },
  });

  const save = form.handleSubmit(async (values) => {
    // One key per game: the games block is sparse, so a whole-config write
    // would clobber an unsaved edit from another tab.
    for (const id of ids) {
      const entry = values.entries[id];
      if (!entry) continue;

      await setConfigValue.mutateAsync({
        key: `games.${id}.path`,
        value: entry.path,
      });
      await setConfigValue.mutateAsync({
        key: `games.${id}.auto_detect`,
        value: String(entry.autoDetect),
      });
      // Blank is the stored form of "use the built-in default", so it is written:
      // skipping it would pin a previously chosen icon with no way back.
      await setConfigValue.mutateAsync({
        key: `games.${id}.icon`,
        value: entry.icon,
      });
    }

    toast.success(t("settings.saved"));
  });

  const activate = (id: string) => {
    setPendingGame(id);
    switchGame.mutate(id, { onSettled: () => setPendingGame(null) });
  };

  const chooseCustomIcon = (id: string) => {
    iconTarget.current = id;
    iconInput.current?.click();
  };

  const loadCustomIcon = async (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    const id = iconTarget.current;
    event.target.value = "";

    if (!file || !id) return;

    try {
      const dataUrl = await readRailIcon(file);
      form.setValue(`entries.${id}.icon`, dataUrl, { shouldDirty: true });
    } catch (error) {
      const description =
        error instanceof RailIconError
          ? {
              format: t("settings.games.iconFormat"),
              size: t("settings.games.iconTooLarge"),
              read: t("settings.games.iconReadFailed"),
            }[error.code]
          : t("settings.games.iconReadFailed");

      toast.error(t("settings.games.customIcon"), { description });
    }
  };

  return (
    <form className="flex flex-col gap-6" onSubmit={save}>
      <input
        ref={iconInput}
        type="file"
        accept="image/png,image/jpeg,image/webp,image/gif,image/x-icon,image/svg+xml"
        className="hidden"
        onChange={loadCustomIcon}
      />

      <FormSection
        title={t("settings.tab.games")}
        description={t("settings.games.hint")}
      >
        {ids.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            {t("settings.games.none")}
          </p>
        ) : (
          ids.map((id) => {
            const game = games.find((entry) => entry.id === id);
            const active = config?.activeGame === id;
            const iconValue = form.watch(`entries.${id}.icon`) ?? "";
            const customChosen = isCustomIcon(iconValue);

            return (
              <div
                key={id}
                className="flex flex-col gap-2 rounded-md border px-3 py-2"
              >
                <div className="flex items-center justify-between gap-3">
                  <div className="flex min-w-0 items-center gap-2">
                    <span className="truncate text-sm font-medium">
                      {game?.displayName ?? id}
                    </span>
                    {active ? (
                      <Badge>{t("settings.games.active")}</Badge>
                    ) : null}
                  </div>
                  {!active ? (
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      disabled={pendingGame !== null}
                      loading={switchGame.isPending}
                      onClick={() => {
                        activate(id);
                        setView("mods");
                      }}
                    >
                      {t("settings.games.use")}
                    </Button>
                  ) : null}
                </div>

                <label className="flex flex-col gap-1.5">
                  <span className="text-sm">{t("settings.games.path")}</span>
                  <PathField
                    value={form.watch(`entries.${id}.path`) ?? ""}
                    onChange={(value) =>
                      form.setValue(`entries.${id}.path`, value, {
                        shouldDirty: true,
                      })
                    }
                  />
                </label>

                <FieldRow
                  label={t("settings.games.icon")}
                  htmlFor={`icon-${id}`}
                >
                  <div className="flex flex-col gap-1.5">
                    <div className="flex flex-wrap items-center gap-1.5">
                      {GAME_ICON_CHOICES.map((choice) => {
                        const chosen = iconValue === choice.id;
                        const Icon = choice.icon;

                        return (
                          <button
                            key={choice.id}
                            type="button"
                            id={
                              choice.id === GAME_ICON_CHOICES[0].id
                                ? `icon-${id}`
                                : undefined
                            }
                            aria-label={choice.label}
                            aria-pressed={chosen}
                            title={choice.label}
                            onClick={() =>
                              form.setValue(
                                `entries.${id}.icon`,
                                chosen ? "" : choice.id,
                                {
                                  shouldDirty: true,
                                }
                              )
                            }
                            className={cn(
                              "flex size-9 items-center justify-center rounded-md border transition-colors",
                              "focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none",
                              chosen
                                ? "border-(--game-accent) bg-accent text-accent-foreground"
                                : "hover:bg-accent/60"
                            )}
                          >
                            <Icon className="size-4" />
                          </button>
                        );
                      })}

                      <button
                        type="button"
                        aria-label={t("settings.games.customIcon")}
                        aria-pressed={customChosen}
                        title={t("settings.games.customIcon")}
                        onClick={() => chooseCustomIcon(id)}
                        className={cn(
                          "flex size-9 items-center justify-center rounded-md border transition-colors",
                          "focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none",
                          customChosen
                            ? "border-(--game-accent) bg-accent text-accent-foreground"
                            : "hover:bg-accent/60"
                        )}
                      >
                        {customChosen ? (
                          <GameIcon
                            gameId={id}
                            icon={iconValue}
                            className="size-4"
                          />
                        ) : (
                          <ImagePlus className="size-4" />
                        )}
                      </button>
                    </div>
                    <Hint>{t("settings.games.iconHint")}</Hint>
                  </div>
                </FieldRow>

                <FieldRow
                  label={t("settings.games.autoDetect")}
                  htmlFor={`auto-${id}`}
                >
                  <Switch
                    id={`auto-${id}`}
                    checked={form.watch(`entries.${id}.autoDetect`) ?? true}
                    onCheckedChange={(on) =>
                      form.setValue(`entries.${id}.autoDetect`, on, {
                        shouldDirty: true,
                      })
                    }
                  />
                </FieldRow>
              </div>
            );
          })
        )}
      </FormSection>

      <Hint>{t("settings.games.restartHint")}</Hint>

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
