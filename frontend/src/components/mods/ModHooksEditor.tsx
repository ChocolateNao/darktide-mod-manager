import { useEffect, useRef, useState } from "react";
import CodeMirror from "@uiw/react-codemirror";
import {
  CheckCircle2,
  Pause,
  Play,
  Plus,
  Save,
  Terminal,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";

import * as api from "@/bridge/client";
import { describeBridgeFailure } from "@/bridge/dialogs";
import { Hint } from "@/components/common/Surfaces";
import { hookExtensions, LANGUAGES } from "@/components/mods/hookEditorTheme";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useSaveModHooks } from "@/hooks/mutations";
import { useLifecycleVocabulary, useModHooksQuery } from "@/hooks/queries";
import { formatClock } from "@/lib/format";
import { t } from "@/lib/i18n";
import { mapHookScript } from "@/lib/mappers/mod";
import { hookPhases } from "@/lib/schemas/mod";
import { cn } from "@/lib/utils";
import { selectHookConsole, useActivityStore } from "@/stores/activity.store";
import { selectMod, useModsStore } from "@/stores/mods.store";
import type {
  HookPhase,
  HookScript,
  ModHooks,
  ScriptLanguage,
} from "@/types/domain";

const EMPTY_SCRIPT: HookScript = { language: "lua", content: "" };

/**
 * The hook editor. A hook set maps phases to script lists, so the editor edits
 * one script at a time: ten CodeMirror instances in a drawer is a wall of grey.
 * Adding a script goes through a phase picker because a hook on the wrong phase
 * is silently never called.
 */
interface ModHooksEditorProps {
  modId: string;
}

export function ModHooksEditor({ modId }: ModHooksEditorProps) {
  return <HooksForm key={modId} modId={modId} />;
}

interface HooksFormProps {
  modId: string;
}

function HooksForm({ modId }: HooksFormProps) {
  const saved = useModHooksQuery(modId);
  const save = useSaveModHooks();

  const [draft, setDraft] = useState<ModHooks | null>(null);
  const [phase, setPhase] = useState<HookPhase | null>(null);
  const [index, setIndex] = useState<number | null>(null);
  const [picking, setPicking] = useState(false);

  const phases = useLifecycleVocabulary().phases.data as
    HookPhase[] | undefined;
  // The backend's vocabulary is authoritative; the static list is the fallback
  // so the picker is never empty while that query is in flight.
  const stageNames = phases?.length ? phases : hookPhases;

  const value = draft ?? saved.data;
  const scripts = phase ? (value?.[phase] ?? []) : [];
  const active = index === null ? null : (scripts[index] ?? null);

  const patch = (next: ModHooks) => setDraft(next);

  const addScript = (target: HookPhase) => {
    // Every phase is a key, always: the Go type is a list per phase, and a
    // missing key is indistinguishable from an absent phase.
    const base =
      value ??
      (Object.fromEntries(
        hookPhases.map((name) => [name, []])
      ) as unknown as ModHooks);
    const updated: ModHooks = {
      ...base,
      [target]: [...(base[target] ?? []), EMPTY_SCRIPT],
    };

    setPicking(false);
    patch(updated);
    setPhase(target);
    setIndex((base[target] ?? []).length);
  };

  const removeAt = (target: HookPhase, position: number) => {
    const base = value as ModHooks;
    const updated: ModHooks = {
      ...base,
      [target]: base[target].filter((_, indexAt) => indexAt !== position),
    };

    patch(updated);
    if (phase === target && index === position) {
      setPhase(null);
      setIndex(null);
    }
  };

  const togglePaused = (target: HookPhase, position: number) => {
    const base = value as ModHooks;
    patch({
      ...base,
      [target]: base[target].map((script, indexAt) =>
        indexAt === position
          ? { ...script, disabled: !script.disabled }
          : script
      ),
    });
  };

  /* Persists one script. The whole hook set goes back because the Go type is a
   * map of phase to list and there is no partial write. */
  const persistScript = async (edited: HookScript) => {
    if (!phase || index === null) return;

    const updated: ModHooks = {
      ...(value as ModHooks),
      [phase]: scripts.map((entry, position) =>
        position === index ? edited : entry
      ),
    };

    patch(updated);
    await save.mutateAsync({ id: modId, hooks: updated });
    setDraft(null);
    toast.success(t("hooks.saved"));
  };

  return (
    <div className="flex flex-col gap-3 pb-4">
      <div className="flex justify-end">
        <Button size="sm" variant="secondary" onClick={() => setPicking(true)}>
          <Plus className="size-4" />
          {t("hooks.addScript")}
        </Button>
      </div>

      <HookList
        modId={modId}
        hooks={value ?? ({} as ModHooks)}
        stages={stageNames}
        selected={{ phase, index }}
        onSelect={(nextPhase, nextIndex) => {
          setPhase(nextPhase);
          setIndex(nextIndex);
        }}
        onToggle={togglePaused}
        onDelete={removeAt}
      />

      {/* The editor lives in a modal: inline, it reflowed the list every time its
          height changed, and a list that moves under the pointer is its own bug. */}
      <Dialog
        open={active !== null && phase !== null && index !== null}
        onOpenChange={(next) => {
          if (!next) {
            setPhase(null);
            setIndex(null);
          }
        }}
      >
        <DialogContent
          className="flex max-h-[90vh] flex-col sm:max-w-3xl"
          onOpenAutoFocus={(event) => event.preventDefault()}
        >
          <DialogHeader>
            <DialogTitle>{t("hooks.editTitle")}</DialogTitle>
            <DialogDescription>
              {phase ? phase.replace(/_/g, " ") : ""}
            </DialogDescription>
          </DialogHeader>

          <div className="flex min-h-0 flex-1 flex-col overflow-y-auto">
            {active && phase !== null && index !== null ? (
              // Keying on phase and index remounts the editor when the selected
              // script changes.
              <ScriptEditor
                key={`${phase}:${index}`}
                modId={modId}
                script={active}
                onRemove={() => {
                  removeAt(phase, index);
                  setPhase(null);
                  setIndex(null);
                }}
                onSave={persistScript}
              />
            ) : null}
          </div>
        </DialogContent>
      </Dialog>

      <Dialog open={picking} onOpenChange={setPicking}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{t("hooks.pickStage")}</DialogTitle>
            <DialogDescription>{t("hooks.pickStageHint")}</DialogDescription>
          </DialogHeader>
          <div className="grid max-h-72 grid-cols-2 gap-1.5 overflow-y-auto">
            {stageNames.map((name) => (
              <Button
                key={name}
                size="sm"
                variant="outline"
                className="justify-start capitalize"
                onClick={() => addScript(name)}
              >
                {name.replace(/_/g, " ")}
              </Button>
            ))}
          </div>
          <DialogFooter />
        </DialogContent>
      </Dialog>
    </div>
  );
}

/* Every run leaves start/finish markers and its printed lines here. */
interface HookConsoleProps {
  modId: string;
}

function HookConsole({ modId }: HookConsoleProps) {
  const entries = useActivityStore(selectHookConsole);
  const mine = entries.filter((entry) => entry.modId === modId);
  const end = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const node = end.current?.parentElement;
    if (node) node.scrollTop = node.scrollHeight;
  }, [mine.length]);

  if (!mine.length) {
    return (
      <div className="flex h-48 items-center rounded-md border bg-muted/40 p-2 font-mono text-[11px] leading-relaxed">
        <p className="text-muted-foreground">{t("hooks.consoleEmpty")}</p>
      </div>
    );
  }

  return (
    <div
      className="h-48 overflow-auto rounded-md border bg-muted/40 p-2 font-mono text-[11px] leading-relaxed"
      data-selectable
    >
      {mine.map((entry) => (
        <div key={entry.id} className="flex min-w-0 gap-2">
          <span className="shrink-0 text-muted-foreground">
            {formatClock(new Date(entry.at))}
          </span>
          <span
            className={cn(
              "min-w-0 wrap-break-word whitespace-pre-wrap",
              (entry.kind === "failed" || entry.kind === "error") &&
                "text-destructive",
              entry.kind === "info" && "text-success",
              entry.kind === "dropped" && "text-warning",
              (entry.kind === "start" || entry.kind === "done") &&
                "text-muted-foreground"
            )}
          >
            {entry.kind === "start"
              ? t("hooks.consoleStarted", {
                  stage: entry.phase || "hook",
                  type: entry.type || "script",
                })
              : entry.kind === "done"
                ? t("hooks.consoleFinished")
                : entry.kind === "failed"
                  ? t("hooks.consoleFailed", { error: entry.text ?? "" })
                  : entry.kind === "dropped"
                    ? t("hooks.consoleDropped", { count: entry.count ?? 0 })
                    : entry.text}
          </span>
        </div>
      ))}
      <div ref={end} />
    </div>
  );
}
interface ScriptEditorProps {
  modId: string;
  script: HookScript;
  onRemove: () => void;
  onSave: (script: HookScript) => Promise<void>;
}

function ScriptEditor({
  modId,
  script: initial,
  onRemove,
  onSave,
}: ScriptEditorProps) {
  const mod = useModsStore((state) => selectMod(state, modId));
  const appendConsole = useActivityStore(
    (state) => state.appendHookConsoleLocal
  );
  const [script, setScript] = useState<HookScript>(initial);
  const [consoleOpen, setConsoleOpen] = useState(true);
  const [running, setRunning] = useState(false);
  const [saving, setSaving] = useState(false);
  const [validating, setValidating] = useState(false);

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3">
      <div className="flex shrink-0 flex-wrap items-center gap-2">
        <Select
          value={script.language}
          onValueChange={(next) =>
            setScript((current) => ({
              ...current,
              language: next as ScriptLanguage,
            }))
          }
        >
          <SelectTrigger className="w-36">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {Object.entries(LANGUAGES).map(([value, label]) => (
              <SelectItem key={value} value={value}>
                {label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <Button variant="ghost" onClick={onRemove}>
          <Trash2 className="size-4" />
          {t("hooks.remove")}
        </Button>
      </div>

      {/* The editor takes the room the dialog has, and the min-height stops collapse. */}
      <div className="min-h-64 flex-1 overflow-hidden rounded-md border">
        <CodeMirror
          value={script.content}
          height="100%"
          theme="none"
          extensions={hookExtensions(script.language)}
          onChange={(next) =>
            setScript((current) => ({ ...current, content: next }))
          }
          basicSetup={{
            lineNumbers: true,
            foldGutter: false,
            autocompletion: false,
          }}
        />
      </div>

      <div className="flex shrink-0 flex-wrap items-center gap-2">
        <Button
          size="sm"
          loading={saving}
          onClick={async () => {
            setSaving(true);
            try {
              await onSave(script);
            } finally {
              setSaving(false);
            }
          }}
        >
          <Save className="size-4" />
          {t("hooks.save")}
        </Button>
        <Button
          size="sm"
          variant="secondary"
          loading={validating}
          disabled={!mod}
          onClick={async () => {
            if (!mod) return;
            setValidating(true);
            setConsoleOpen(true);
            try {
              await api.validateHookScript(mapHookScript(script), mod);
              appendConsole(modId, "info", t("hooks.valid"));
            } catch (error) {
              appendConsole(
                modId,
                "error",
                t("hooks.validateFailed", {
                  error: describeBridgeFailure(error),
                })
              );
            } finally {
              setValidating(false);
            }
          }}
        >
          <CheckCircle2 className="size-4" />
          {t("hooks.validate")}
        </Button>
        <Button
          size="sm"
          variant="secondary"
          className="min-w-28"
          loading={running}
          disabled={!mod}
          onClick={async () => {
            if (!mod) return;
            setRunning(true);
            setConsoleOpen(true);
            try {
              await api.runHookScript(mapHookScript(script), mod);
            } catch {
              // The runner publishes the failure with its message; the console
              // shows it when the event lands.
            } finally {
              setRunning(false);
            }
          }}
        >
          <Play className="size-4" />
          {t("hooks.run")}
        </Button>
        <Button
          size="sm"
          variant="ghost"
          onClick={() => setConsoleOpen((open) => !open)}
        >
          <Terminal className="size-4" />
          {t("hooks.console")}
        </Button>
      </div>

      {consoleOpen ? (
        <div className="min-h-0 shrink-0">
          <HookConsole modId={modId} />
        </div>
      ) : null}
    </div>
  );
}

/**
 * The list of every hook on the mod, across all ten stages, so "what does this
 * mod actually do when I install it?" is answered in one glance.
 */
interface HookListProps {
  modId: string;
  hooks: ModHooks;
  stages: HookPhase[];
  onSelect: (phase: HookPhase, index: number) => void;
  selected: { phase: HookPhase | null; index: number | null };
  onToggle: (phase: HookPhase, index: number) => void;
  onDelete: (phase: HookPhase, index: number) => void;
}

export function HookList({
  modId,
  hooks,
  stages,
  onSelect,
  selected,
  onToggle,
  onDelete,
}: HookListProps) {
  const rows = stages.flatMap((phase) =>
    (hooks[phase] ?? []).map((script, position) => ({
      phase,
      position,
      script,
      label:
        script.name?.trim() || `${phase.replace(/_/g, " ")} ${position + 1}`,
    }))
  );

  if (rows.length === 0) {
    return <Hint>{t("hooks.empty")}</Hint>;
  }

  return (
    <ul className="flex flex-col gap-1.5">
      {rows.map((row) => {
        const isSelected =
          selected.phase === row.phase && selected.index === row.position;
        const paused = row.script.disabled === true;

        return (
          <li
            key={`${row.phase}:${row.position}`}
            className={cn(
              "flex items-center gap-2 rounded-md border px-2.5 py-2",
              isSelected && "border-(--game-accent) bg-accent/30",
              paused && "opacity-60"
            )}
          >
            {/* The dot answers "is this running"; a paused hook stays listed and
                editable, just dimmed, because it still exists and simply does not fire. */}
            <span
              title={paused ? t("hooks.paused") : t("hooks.active")}
              aria-label={paused ? t("hooks.paused") : t("hooks.active")}
              className={cn(
                "size-2 shrink-0 rounded-full",
                paused ? "bg-muted-foreground" : "bg-success"
              )}
            />

            <div className="min-w-0 flex-1">
              <p className="truncate text-sm font-medium">{row.label}</p>
              <p className="text-[11px] text-muted-foreground">
                {row.phase.replace(/_/g, " ")} ·{" "}
                {LANGUAGES[row.script.language]}
              </p>
            </div>

            <Button
              size="sm"
              variant="ghost"
              onClick={() => onSelect(row.phase, row.position)}
              disabled={isSelected}
            >
              {t("hooks.edit")}
            </Button>
            {/* Ghost, like the delete and run buttons beside it: a filled pause
                read as the row's primary action. */}
            <Button
              size="sm"
              variant="ghost"
              aria-label={paused ? t("hooks.resume") : t("hooks.pause")}
              title={paused ? t("hooks.resume") : t("hooks.pause")}
              className="text-muted-foreground hover:text-foreground"
              onClick={() => onToggle(row.phase, row.position)}
            >
              {paused ? (
                <Play className="size-3.5" />
              ) : (
                <Pause className="size-3.5" />
              )}
            </Button>
            <ConfirmDelete
              onConfirm={() => onDelete(row.phase, row.position)}
              trigger={
                <Button
                  size="sm"
                  variant="ghost"
                  aria-label={t("hooks.delete")}
                  title={t("hooks.delete")}
                >
                  <Trash2 className="size-3.5 text-destructive" />
                </Button>
              }
            />
            <HookRunButton modId={modId} script={row.script} />
          </li>
        );
      })}
    </ul>
  );
}

/** Runs one hook straight from its row, for "does this work?" without editing. */
interface HookRunButtonProps {
  modId: string;
  script: HookScript;
}

function HookRunButton({ modId, script }: HookRunButtonProps) {
  const mod = useModsStore((state) => selectMod(state, modId));
  const [running, setRunning] = useState(false);

  return (
    <Button
      size="sm"
      variant="ghost"
      aria-label={t("hooks.run")}
      title={t("hooks.run")}
      loading={running}
      disabled={!mod}
      onClick={async () => {
        if (!mod) return;
        setRunning(true);
        try {
          await api.runHookScript(mapHookScript(script), mod);
          toast.success(t("hooks.ran"));
        } catch (error) {
          toast.error(describeBridgeFailure(error));
        } finally {
          setRunning(false);
        }
      }}
    >
      <Play className="size-3.5" />
    </Button>
  );
}

/** Deleting a hook is the one irreversible thing here, so it asks first. */
interface ConfirmDeleteProps {
  trigger: React.ReactNode;
  onConfirm: () => void;
}

function ConfirmDelete({ trigger, onConfirm }: ConfirmDeleteProps) {
  const [open, setOpen] = useState(false);

  return (
    <AlertDialog open={open} onOpenChange={setOpen}>
      <AlertDialogTrigger asChild>{trigger}</AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t("hooks.delete")}</AlertDialogTitle>
          <AlertDialogDescription>
            {t("hooks.deleteHint")}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
          <AlertDialogAction onClick={onConfirm}>
            {t("hooks.delete")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
