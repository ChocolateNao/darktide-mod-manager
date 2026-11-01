import { useState } from "react";
import { CheckCircle2, Loader2, Search, XCircle } from "lucide-react";

import { PathField } from "@/components/common/PathField";
import { ErrorNote, Hint } from "@/components/common/Surfaces";
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
import { Label } from "@/components/ui/label";
import { useSetGamePath, useSwitchGame } from "@/hooks/mutations";
import { useInstallationsQuery } from "@/hooks/queries";
import { t } from "@/lib/i18n";
import { selectGames, useGamesStore } from "@/stores/games.store";

type Step = "pick" | "detect" | "configure";

/**
 * The rail's `+`, as a wizard. List,
 * detect, validate, set — each only possible after the previous produced
 * something.
 */
interface AddGameDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

export function AddGameDialog({ open, onOpenChange }: AddGameDialogProps) {
  const games = useGamesStore(selectGames);
  const setActiveGameId = useGamesStore((state) => state.setActiveGameId);
  const installations = useInstallationsQuery(open);
  const setGamePath = useSetGamePath();
  const switchGame = useSwitchGame();

  const [step, setStep] = useState<Step>("pick");
  const [filter, setFilter] = useState("");
  const [gameId, setGameId] = useState<string | null>(null);
  const [chosen, setChosen] = useState<string | null>(null);
  const [probing, setProbing] = useState(false);
  const [probeError, setProbeError] = useState<unknown>(null);

  // A reused dialog must not remember the previous game's path, so state
  // resets on close — a remembered path silently reconfigures the wrong one.
  const handleOpenChange = (next: boolean) => {
    if (!next) {
      setStep("pick");
      setGameId(null);
      setChosen(null);
      setFilter("");
      setProbeError(null);
    }
    onOpenChange(next);
  };

  const visible = games.filter(
    (game) =>
      game.id.toLowerCase().includes(filter.toLowerCase()) ||
      game.displayName.toLowerCase().includes(filter.toLowerCase())
  );

  const detections = (installations.data ?? []).filter((installation) =>
    gameId ? installation.gameId === gameId : false
  );

  const detect = async () => {
    setProbing(true);
    setProbeError(null);
    try {
      await installations.refetch();
    } catch (error) {
      setProbeError(error);
    } finally {
      setProbing(false);
    }
  };

  const apply = async (path: string, detectGameId: string) => {
    await setGamePath.mutateAsync(path);
    setActiveGameId(detectGameId);
    switchGame.mutate(detectGameId);
    handleOpenChange(false);
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {/* The body scrolls while header and footer stay pinned: an installation
          list can push the footer off-screen. */}
      <DialogContent className="flex max-h-[85vh] flex-col sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{t("addGame.title")}</DialogTitle>
          <DialogDescription>
            {step === "pick" && t("addGame.pickHint")}
            {step === "detect" && t("addGame.detectHint")}
            {step === "configure" && t("addGame.configureHint")}
          </DialogDescription>
        </DialogHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto">
          {step === "pick" && (
            <div className="flex flex-col gap-2">
              <div className="relative">
                <Search className="absolute top-2.5 left-2 size-4 text-muted-foreground" />
                <Input
                  className="pl-8"
                  value={filter}
                  onChange={(event) => setFilter(event.target.value)}
                  placeholder={t("addGame.search")}
                  aria-label={t("addGame.search")}
                />
              </div>

              <ul className="max-h-72 divide-y overflow-y-auto rounded-md border">
                {visible.map((game) => (
                  <li key={game.id}>
                    <button
                      type="button"
                      className="flex w-full flex-col items-start gap-0.5 px-3 py-2 text-left hover:bg-accent"
                      onClick={() => {
                        setGameId(game.id);
                        setStep("detect");
                      }}
                    >
                      <span className="text-sm font-medium">
                        {game.displayName}
                      </span>
                      {game.description ? (
                        <span className="text-xs text-muted-foreground">
                          {game.description}
                        </span>
                      ) : null}
                      {game.installModes.length > 0 ? (
                        <span className="mt-1 flex flex-wrap gap-1">
                          {game.installModes.map((mode) => (
                            <span
                              key={mode}
                              className="rounded border px-1 text-[10px] text-muted-foreground"
                            >
                              {mode}
                            </span>
                          ))}
                        </span>
                      ) : null}
                    </button>
                  </li>
                ))}
                {visible.length === 0 && (
                  <li className="px-3 py-6 text-center text-xs text-muted-foreground">
                    {t("addGame.noMatches")}
                  </li>
                )}
              </ul>
            </div>
          )}

          {step === "detect" && (
            <div className="flex flex-col gap-3">
              {probeError ? <ErrorNote error={probeError} /> : null}

              <Button variant="secondary" onClick={detect} disabled={probing}>
                {probing && <Loader2 className="size-4 animate-spin" />}
                {t("addGame.scan")}
              </Button>

              {installations.data ? (
                <ul className="max-h-64 divide-y overflow-y-auto rounded-md border text-sm">
                  {detections.map((installation) => (
                    <li
                      key={installation.path}
                      className="flex items-center gap-2 px-3 py-2"
                    >
                      {installation.valid ? (
                        <CheckCircle2 className="size-4 shrink-0 text-success" />
                      ) : (
                        <XCircle className="size-4 shrink-0 text-destructive" />
                      )}
                      <span className="min-w-0 flex-1 truncate" data-selectable>
                        {installation.path}
                      </span>
                      <span className="shrink-0 text-xs text-muted-foreground">
                        {installation.source || t("common.unknown")}
                      </span>
                      <Button
                        size="sm"
                        disabled={!installation.valid}
                        onClick={() => {
                          setChosen(installation.path);
                          setStep("configure");
                        }}
                      >
                        {t("addGame.use")}
                      </Button>
                    </li>
                  ))}
                  {detections.length === 0 && (
                    <li className="px-3 py-6 text-center text-xs text-muted-foreground">
                      {t("addGame.noneFound")}
                    </li>
                  )}
                </ul>
              ) : null}

              <div className="flex items-center gap-2 text-xs text-muted-foreground">
                <span>{t("addGame.orManual")}</span>
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => {
                    setChosen("");
                    setStep("configure");
                  }}
                >
                  {t("addGame.enterPath")}
                </Button>
              </div>
            </div>
          )}

          {step === "configure" && gameId && (
            <div className="flex flex-col gap-3">
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="game-path">{t("addGame.path")}</Label>
                <PathField
                  id="game-path"
                  value={chosen ?? ""}
                  onChange={setChosen}
                  placeholder={t("addGame.pathPlaceholder")}
                />
              </div>
              <Hint>{t("addGame.pathHint")}</Hint>

              <Button
                loading={setGamePath.isPending || switchGame.isPending}
                onClick={() => {
                  if (chosen !== null) void apply(chosen, gameId);
                }}
              >
                {t("addGame.finish")}
              </Button>
            </div>
          )}
        </div>

        <DialogFooter>
          <Button
            variant="ghost"
            onClick={() => {
              if (step === "configure") setStep("detect");
              else if (step === "detect") setStep("pick");
              else handleOpenChange(false);
            }}
          >
            {step === "pick" ? t("common.close") : t("addGame.back")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
