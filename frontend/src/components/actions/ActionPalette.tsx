import { useState } from "react";
import {
  Command,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "cmdk";
import {
  CornerDownLeft,
  Globe,
  Search,
  Terminal,
  TriangleAlert,
} from "lucide-react";

import { describeBridgeFailure } from "@/bridge/dialogs";
import { ActionForm } from "@/components/actions/ActionForm";
import { ActionResult } from "@/components/actions/ActionResult";
import { ScrollingText } from "@/components/common/ScrollingText";
import { ErrorNote } from "@/components/common/Surfaces";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { useRunGameAction } from "@/hooks/mutations";
import { useGameActions } from "@/hooks/useModActions";
import { t, tOr } from "@/lib/i18n";
import { useActionsStore } from "@/stores/actions.store";
import { useGamesStore } from "@/stores/games.store";
import { useUiStore } from "@/stores/ui.store";
import type { GameAction, GameActionResult } from "@/types/domain";

/**
 * The palette is the primary home for game actions: their set is per-game,
 * code-defined and platform-gated, so a fixed menu would need regenerating
 * every time a game plugin landed.
 */
export function ActionPalette() {
  const open = useUiStore((state) => state.paletteOpen);
  const setOpen = useUiStore((state) => state.setPaletteOpen);
  const { actions, isPending, isError, error, refetch } = useGameActions();
  const activeGameId = useGamesStore((state) => state.activeGameId);

  const openAction = useActionsStore((state) => state.open);

  /* Only a successful empty result may say the catalogue is empty: mid-flight
   * or failed reads would make a bridge error read as "this game declares nothing". */
  const showMessage = isPending || isError || actions.length === 0;

  const scope = activeGameId
    ? t("actions.scopeGame", { game: activeGameId })
    : t("actions.scopeAll");

  if (showMessage) {
    return (
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <Terminal className="size-4 text-(--game-accent)" />
              {t("actions.paletteLabel")}
            </DialogTitle>
            <DialogDescription>
              {isPending
                ? t("common.loading")
                : isError
                  ? describeBridgeFailure(error)
                  : t("actions.paletteEmpty")}
            </DialogDescription>
          </DialogHeader>

          {isError ? (
            <div className="flex justify-end">
              <Button
                variant="outline"
                loading={isPending}
                onClick={() => void refetch()}
              >
                {t("common.retry")}
              </Button>
            </div>
          ) : null}
        </DialogContent>
      </Dialog>
    );
  }

  /* Closing on pick avoids stacking two Radix overlays: the run dialog is a
   * sibling and would land underneath the still-mounted palette. */
  const pick = (action: GameAction) => {
    openAction({ action, presetArgs: {} });
    setOpen(false);
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent className="flex max-h-[80vh] flex-col gap-0 overflow-hidden p-0 sm:max-w-xl">
        <DialogHeader className="gap-1 border-b px-4 py-3 text-left">
          <DialogTitle className="flex items-center gap-2 pr-8 text-base">
            <Terminal className="size-4 shrink-0 text-(--game-accent)" />
            <span className="truncate">{t("actions.paletteLabel")}</span>
            <Badge
              variant="outline"
              className="ml-auto shrink-0 gap-1 font-mono text-[10px]"
            >
              {actions.length}
            </Badge>
          </DialogTitle>
          <DialogDescription className="truncate">{scope}</DialogDescription>
        </DialogHeader>

        <Command
          label={t("actions.paletteLabel")}
          loop
          className="bg-popover text-popover-foreground"
          // cmdk owns the arrow keys; the window keeps Escape.
          onKeyDown={(event) => {
            if (event.key === "Escape") setOpen(false);
          }}
        >
          <div className="flex items-center gap-2 border-b px-4">
            <Search className="size-4 shrink-0 text-muted-foreground" />
            <CommandInput
              autoFocus
              placeholder={t("actions.palettePlaceholder")}
              className="h-11 flex-1 bg-transparent outline-none placeholder:text-muted-foreground"
            />
          </div>

          <CommandList className="max-h-80 overflow-x-hidden overflow-y-auto p-1.5">
            <Command.Empty className="py-8 text-center text-sm text-muted-foreground">
              {t("actions.paletteEmpty")}
            </Command.Empty>

            {groupActions(actions).map(([category, group]) => (
              <CommandGroup
                key={category}
                heading={category}
                className="**:[[cmdk-group-heading]]:px-2 **:[[cmdk-group-heading]]:py-1.5 **:[[cmdk-group-heading]]:text-[10px] **:[[cmdk-group-heading]]:font-semibold **:[[cmdk-group-heading]]:tracking-wide **:[[cmdk-group-heading]]:text-muted-foreground **:[[cmdk-group-heading]]:uppercase"
              >
                {group.map((action) => (
                  <ActionItem
                    key={action.id}
                    action={action}
                    onPick={() => pick(action)}
                  />
                ))}
              </CommandGroup>
            ))}
          </CommandList>
        </Command>

        <div className="flex items-center justify-between gap-3 border-t px-4 py-2 text-xs text-muted-foreground">
          <span className="truncate">{scope}</span>
          <span className="flex shrink-0 items-center gap-3">
            <span className="flex items-center gap-1">
              <kbd className="rounded border px-1 font-mono text-[10px]">
                ↑↓
              </kbd>
              {t("actions.hintNavigate")}
            </span>
            <span className="flex items-center gap-1">
              <kbd className="rounded border px-1 font-mono text-[10px]">
                <CornerDownLeft className="size-2.5" />
              </kbd>
              {t("actions.hintRun")}
            </span>
          </span>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function groupActions(
  actions: readonly GameAction[]
): [string, GameAction[]][] {
  const groups = new Map<string, GameAction[]>();

  for (const action of actions) {
    const key = tOr(
      action.categoryKey,
      action.category || t("actions.uncategorised")
    );
    const bucket = groups.get(key);

    if (bucket) {
      bucket.push(action);
    } else {
      groups.set(key, [action]);
    }
  }

  return [...groups.entries()].sort(([left], [right]) =>
    left.localeCompare(right)
  );
}

interface ActionItemProps {
  action: GameAction;
  onPick: () => void;
}

function ActionItem({ action, onPick }: ActionItemProps) {
  const title = tOr(action.displayNameKey, action.displayName);
  const description = tOr(action.descriptionKey, action.description);

  return (
    <CommandItem
      value={`${title} ${action.displayName} ${action.id} ${description} ${action.category} ${action.options.map((option) => option.name).join(" ")}`}
      onSelect={onPick}
      className="flex cursor-pointer items-center gap-2.5 overflow-hidden rounded-md p-2 text-sm data-[selected=true]:bg-accent data-[selected=true]:text-accent-foreground"
    >
      {/* cmdk fuzzy-matches the `value` string, so it carries every word a user might type. */}
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="truncate">{title}</span>
        {description ? (
          <ScrollingText className="text-xs text-muted-foreground">
            {description}
          </ScrollingText>
        ) : null}
      </div>

      {action.requiresGame ? null : (
        <Badge
          variant="secondary"
          className="shrink-0 gap-1 text-[10px] font-normal"
          title={t("actions.universalHint")}
        >
          <Globe className="size-3" />
          {t("actions.universal")}
        </Badge>
      )}

      {action.dangerous ? (
        <TriangleAlert
          className="size-3.5 shrink-0 text-destructive"
          aria-label={t("actions.dangerous")}
        />
      ) : null}

      {action.options.length > 0 ? (
        <Badge
          variant="outline"
          className="shrink-0 gap-1 text-[10px] font-normal"
        >
          <Terminal className="size-3" />
          {action.options.length}
        </Badge>
      ) : null}
    </CommandItem>
  );
}

/**
 * One dialog for every action run, whichever surface started it. The form is
 * built from the declaration and the result replaces it in place, so the user
 * never loses what they typed by having to close and reopen.
 */
export function ActionDialogHost() {
  const request = useActionsStore((state) => state.request);
  const close = useActionsStore((state) => state.close);

  const action = request?.action;
  const title = action ? tOr(action.displayNameKey, action.displayName) : "";
  const description = action
    ? tOr(action.descriptionKey, action.description || action.category)
    : "";

  return (
    <Dialog open={Boolean(action)} onOpenChange={(next) => !next && close()}>
      <DialogContent className="flex max-h-[85vh] flex-col sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2 pr-8">
            <span className="truncate">{title}</span>
            {action?.dangerous ? (
              <Badge variant="destructive">{t("actions.dangerous")}</Badge>
            ) : null}
          </DialogTitle>
          {description ? (
            <DialogDescription>{description}</DialogDescription>
          ) : null}
        </DialogHeader>

        {/* Keying on the action id remounts the body for a new request, so the
            previous result disappears without an extra render on every open. */}
        <div className="min-h-0 flex-1 scrollbar-gutter-stable overflow-y-auto">
          {action ? (
            <ActionDialogBody
              key={action.id}
              action={action}
              presetArgs={request?.presetArgs}
            />
          ) : null}
        </div>
      </DialogContent>
    </Dialog>
  );
}

interface ActionDialogBodyProps {
  action: GameAction;
  presetArgs?: Record<string, unknown>;
}

function ActionDialogBody({ action, presetArgs }: ActionDialogBodyProps) {
  const [result, setResult] = useState<GameActionResult | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const runAction = useRunGameAction();
  const close = useActionsStore((state) => state.close);

  /* `mutateAsync` rejects, so awaiting it unguarded left an unhandled rejection
   * and the dialog never changed — a button that appears to do nothing. */
  const run = async (args: Record<string, unknown>) => {
    setFailure(null);

    try {
      setResult(await runAction.mutateAsync({ id: action.id, args }));
    } catch (error) {
      setFailure(describeBridgeFailure(error));
    }
  };

  return (
    <div className="flex min-h-0 flex-col gap-4">
      {failure ? <ErrorNote error={new Error(failure)} /> : null}

      {result ? (
        <>
          <ActionResult result={result} />
          {/* Real controls now that the run has finished: "Run again" is the one
              users usually want. */}
          <DialogFooter>
            <Button variant="ghost" onClick={close}>
              {t("common.close")}
            </Button>
            <Button
              variant="outline"
              onClick={() => {
                setFailure(null);
                setResult(null);
              }}
            >
              {t("actions.runAgain")}
            </Button>
          </DialogFooter>
        </>
      ) : (
        <ActionForm
          action={action}
          presetArgs={presetArgs}
          busy={runAction.isPending}
          onSubmit={run}
        />
      )}
    </div>
  );
}
