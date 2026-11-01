import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  closestCenter,
  DndContext,
  type DragEndEvent,
  type DraggableAttributes,
  type DraggableSyntheticListeners,
  type DragStartEvent,
  KeyboardSensor,
  PointerSensor,
  useSensor,
  useSensors,
} from "@dnd-kit/core";
import {
  arrayMove,
  SortableContext,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import {
  ArrowUpCircle,
  ArrowUpDown,
  Download,
  EllipsisVertical,
  ExternalLink,
  FileCode2,
  GripVertical,
  RefreshCw,
  Trash2,
  TriangleAlert,
} from "lucide-react";

import * as api from "@/bridge/client";
import { EmptyState } from "@/components/common/Surfaces";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import {
  useBulkToggleMods,
  useBulkUninstallMods,
  useCancelUpdate,
  useRelinkMod,
  useReorderMods,
  useToggleMod,
  useUninstallMod,
  useUpdateMod,
} from "@/hooks/mutations";
import { useGameActionsForMod } from "@/hooks/useModActions";
import { type DragBounds, dragLimits } from "@/lib/dragBounds";
import { t } from "@/lib/i18n";
import { resolveLoadOrder } from "@/lib/loadOrder";
import { cn } from "@/lib/utils";
import { selectProgressFor, useActivityStore } from "@/stores/activity.store";
import {
  selectLinkStatus,
  selectLoadOrder,
  selectMods,
  selectSelection,
  selectUpdateFor,
  useModsStore,
} from "@/stores/mods.store";
import { useUiStore } from "@/stores/ui.store";
import type { Mod } from "@/types/domain";

type SortKey = "name" | "version" | "source" | "status" | "priority";
type BulkAction = "enable" | "disable" | "delete" | "deleteExternal";

const SORTABLE: ReadonlySet<SortKey> = new Set(["name", "version", "priority"]);

/**
 * A dense sortable table: a card hides the version and gives the drag handle
 * nowhere unambiguous to live. Sorting is disabled for columns the load order
 * owns, since dragging a row would otherwise rewrite the load order.
 * `priority` shows the resolved load order because `Mod.Priority` is a per-mod
 * integer nothing writes.
 */
export function ModTable() {
  const mods = useModsStore(selectMods);
  const loadOrder = useModsStore(selectLoadOrder);
  const selection = useModsStore(selectSelection);
  const clearSelection = useModsStore((state) => state.clearSelection);
  const reorder = useReorderMods();

  const [sort, setSort] = useState<{ key: SortKey; ascending: boolean }>({
    key: "priority",
    ascending: true,
  });

  /* Measured once at drag start: the box only changes on resize or selection
   * change, and re-measuring per move would render per move. */
  const viewportRef = useRef<HTMLDivElement | null>(null);
  const rowRefs = useRef(new Map<string, HTMLTableRowElement>());
  const [dragBounds, setDragBounds] = useState<{
    id: string;
    box: DragBounds;
  } | null>(null);

  const registerRow = useCallback(
    (id: string, node: HTMLTableRowElement | null) => {
      if (node) rowRefs.current.set(id, node);
      else rowRefs.current.delete(id);
    },
    []
  );

  const { ordered, positions } = useMemo(
    () => resolveLoadOrder(mods, loadOrder),
    [mods, loadOrder]
  );
  const rows = useMemo(
    () => sortMods(ordered, positions, sort),
    [ordered, positions, sort]
  );
  const ids = useMemo(() => rows.map((mod) => mod.id), [rows]);

  /* Reordering is only offered in load order: in any other sort the row order
   * is a presentation choice, and persisting it would rewrite the load order. */
  const reorderable = sort.key === "priority" && sort.ascending;

  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
    // Keyboard reorder is an accessibility requirement for a reorderable list:
    // the mod order is the load order and it has to be reachable without a pointer.
    useSensor(KeyboardSensor, {
      coordinateGetter: sortableKeyboardCoordinates,
    })
  );

  /* Refs are read at drag start and the row is measured straight from the DOM:
   * dnd-kit's own rect is null until it has measured the draggable. */
  const onDragStart = (event: DragStartEvent) => {
    const id = String(event.active.id);
    const viewport = viewportRef.current;
    const row = rowRefs.current.get(id);

    if (!viewport || !row) return;

    const box = viewport.getBoundingClientRect();
    const rect = row.getBoundingClientRect();

    setDragBounds({
      id,
      box: {
        top: box.top,
        bottom: box.bottom,
        rowTop: rect.top,
        rowHeight: rect.height,
      },
    });
  };

  const onDragEnd = (event: DragEndEvent) => {
    setDragBounds(null);

    const { active, over } = event;
    if (!over || active.id === over.id) return;

    const from = rows.findIndex((mod) => mod.id === active.id);
    const to = rows.findIndex((mod) => mod.id === over.id);
    if (from < 0 || to < 0) return;
    if (!reorderable) return;
    reorder.mutate(
      arrayMove(
        rows.map((mod) => mod.id),
        from,
        to
      )
    );
  };

  const [bulkAction, setBulkAction] = useState<BulkAction | null>(null);
  const [trashAvailable, setTrashAvailable] = useState(false);
  const bulkToggle = useBulkToggleMods();
  const bulkUninstall = useBulkUninstallMods();

  const selectedMods = useMemo(
    () => rows.filter((mod) => selection.includes(mod.id)),
    [rows, selection]
  );
  const known = selectedMods.filter((mod) => !mod.external);
  const external = selectedMods.filter((mod) => mod.external);
  const toEnable = known.filter((mod) => !mod.enabled);
  const toDisable = known.filter((mod) => mod.enabled);

  useEffect(() => {
    if (external.length === 0) return;
    let cancelled = false;

    api
      .trashAvailable()
      .then((available) => {
        if (!cancelled) setTrashAvailable(available);
      })
      .catch(() => {
        if (!cancelled) setTrashAvailable(false);
      });

    return () => {
      cancelled = true;
    };
  }, [external.length]);

  const runBulk = (action: BulkAction, trash = false) => {
    const ids =
      action === "deleteExternal"
        ? external.map((mod) => mod.id)
        : action === "delete"
          ? known.map((mod) => mod.id)
          : action === "enable"
            ? toEnable.map((mod) => mod.id)
            : toDisable.map((mod) => mod.id);

    const onSuccess =
      action === "delete" || action === "deleteExternal"
        ? clearSelection
        : undefined;

    if (action === "enable" || action === "disable") {
      bulkToggle.mutate({ ids, enable: action === "enable" }, { onSuccess });
    } else {
      bulkUninstall.mutate({ ids, trash }, { onSuccess });
    }

    setBulkAction(null);
  };

  if (mods.length === 0) {
    return (
      <EmptyState title={t("mods.empty")} description={t("mods.emptyHint")} />
    );
  }

  const allSelected = selection.length > 0 && selection.length === rows.length;
  const bulkCount =
    bulkAction === "deleteExternal"
      ? external.length
      : bulkAction === "delete"
        ? known.length
        : bulkAction === "enable"
          ? toEnable.length
          : bulkAction === "disable"
            ? toDisable.length
            : 0;
  const bulkCopy = bulkAction ? bulkMessage(bulkAction, bulkCount) : null;

  return (
    <div className="flex h-full flex-col">
      {selection.length > 0 && (
        <div className="flex items-center gap-2 border-b bg-muted/40 px-4 py-1.5 text-xs">
          <span>{t("mods.selected", { count: selection.length })}</span>
          <Button
            variant="ghost"
            size="sm"
            className="h-6"
            onClick={clearSelection}
          >
            {t("common.clear")}
          </Button>

          <div className="ml-auto flex items-center gap-1">
            {toEnable.length > 0 && (
              <Button
                variant="ghost"
                size="sm"
                className="h-6"
                onClick={() => setBulkAction("enable")}
              >
                {t("mods.bulkEnable", { count: toEnable.length })}
              </Button>
            )}
            {toDisable.length > 0 && (
              <Button
                variant="ghost"
                size="sm"
                className="h-6"
                onClick={() => setBulkAction("disable")}
              >
                {t("mods.bulkDisable", { count: toDisable.length })}
              </Button>
            )}
            {known.length > 0 && (
              <Button
                variant="ghost"
                size="sm"
                className="h-6 text-destructive"
                onClick={() => setBulkAction("delete")}
              >
                {t("mods.bulkDelete", { count: known.length })}
              </Button>
            )}
            {external.length > 0 && (
              <Button
                variant="ghost"
                size="sm"
                className="h-6 text-destructive"
                onClick={() => setBulkAction("deleteExternal")}
              >
                {t("mods.bulkDeleteExternal", { count: external.length })}
              </Button>
            )}
          </div>
        </div>
      )}

      <DndContext
        sensors={sensors}
        collisionDetection={closestCenter}
        onDragStart={onDragStart}
        onDragCancel={() => setDragBounds(null)}
        onDragEnd={onDragEnd}
      >
        {/* Outside the scroll box so the scrollbar runs past the rows, not the header. */}
        <div className="shrink-0 scrollbar-gutter-stable overflow-x-hidden overflow-y-auto">
          <Table className="density-row table-fixed">
            <TableHeader className="bg-card">
              <TableRow className="hover:bg-transparent">
                <TableHead className="w-10" />
                <TableHead className="w-8">
                  <Checkbox
                    aria-label={t("mods.selectAll")}
                    checked={allSelected}
                    onCheckedChange={(checked) => {
                      if (checked) useModsStore.getState().setSelection(ids);
                      else clearSelection();
                    }}
                  />
                </TableHead>
                <SortHead
                  label={t("mods.column.name")}
                  column="name"
                  sort={sort}
                  onSort={setSort}
                />
                <SortHead
                  label={t("mods.column.version")}
                  column="version"
                  sort={sort}
                  onSort={setSort}
                  className="w-24"
                />
                <SortHead
                  label={t("mods.column.source")}
                  column="source"
                  sort={sort}
                  onSort={setSort}
                  className="w-32"
                />
                <SortHead
                  label={t("mods.column.status")}
                  column="status"
                  sort={sort}
                  onSort={setSort}
                  className="w-24"
                />
                <SortHead
                  label={t("mods.column.priority")}
                  column="priority"
                  sort={sort}
                  onSort={setSort}
                  className="w-16"
                />
                <TableHead className="w-24 text-right">
                  {t("mods.column.actions")}
                </TableHead>
              </TableRow>
            </TableHeader>
          </Table>
        </div>

        <div
          ref={viewportRef}
          className="min-h-0 flex-1 scrollbar-gutter-stable overflow-auto"
        >
          <SortableContext items={ids} strategy={verticalListSortingStrategy}>
            <Table className="density-row table-fixed">
              <TableBody>
                {rows.map((mod) => (
                  <SortableModRow
                    key={mod.id}
                    mod={mod}
                    position={positions.get(mod.id) ?? null}
                    selected={selection.includes(mod.id)}
                    reorderable={reorderable}
                    bounds={dragBounds?.id === mod.id ? dragBounds.box : null}
                    registerRow={registerRow}
                  />
                ))}
              </TableBody>
            </Table>
          </SortableContext>
        </div>
      </DndContext>

      {bulkAction && bulkCopy && (
        <AlertDialog
          open
          onOpenChange={(open) => {
            if (!open) setBulkAction(null);
          }}
        >
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>{bulkCopy.title}</AlertDialogTitle>
              <AlertDialogDescription>{bulkCopy.hint}</AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
              {bulkAction === "deleteExternal" ? (
                <>
                  {trashAvailable && (
                    <AlertDialogAction
                      variant="outline"
                      onClick={() => runBulk("deleteExternal", true)}
                    >
                      {t("mods.moveToTrash")}
                    </AlertDialogAction>
                  )}
                  <AlertDialogAction
                    variant="destructive"
                    onClick={() => runBulk("deleteExternal", false)}
                  >
                    {t("mods.deletePermanently")}
                  </AlertDialogAction>
                </>
              ) : (
                <AlertDialogAction
                  variant={bulkAction === "delete" ? "destructive" : "default"}
                  onClick={() => runBulk(bulkAction)}
                >
                  {t("common.confirm")}
                </AlertDialogAction>
              )}
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      )}
    </div>
  );
}

interface SortHeadProps {
  label: string;
  column: SortKey;
  sort: { key: SortKey; ascending: boolean };
  onSort: (next: { key: SortKey; ascending: boolean }) => void;
  className?: string;
}

function SortHead({ label, column, sort, onSort, className }: SortHeadProps) {
  const active = sort.key === column;

  return (
    <TableHead className={className}>
      <button
        type="button"
        className={cn(
          "flex items-center gap-1 focus-visible:outline-none",
          active && "font-semibold text-foreground"
        )}
        onClick={() =>
          onSort({ key: column, ascending: active ? !sort.ascending : true })
        }
      >
        {label}
        {SORTABLE.has(column) && <ArrowUpDown className="size-3 opacity-50" />}
      </button>
    </TableHead>
  );
}

interface SortableModRowProps {
  mod: Mod;
  position: number | null;
  selected: boolean;
  reorderable: boolean;
  bounds: DragBounds | null;
  registerRow: (id: string, node: HTMLTableRowElement | null) => void;
}

function SortableModRow({
  mod,
  position,
  selected,
  reorderable,
  bounds,
  registerRow,
}: SortableModRowProps) {
  const {
    attributes,
    listeners,
    setNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({
    id: mod.id,
    disabled: !reorderable,
  });

  /*
   * Two refs on one element: dnd-kit's for hit-testing, and the table's so the
   * drag handler can find this row again by id when the drag begins.
   */
  const attach = useCallback(
    (node: HTMLTableRowElement | null) => {
      registerRow(mod.id, node);
      setNodeRef(node);
    },
    [mod.id, registerRow, setNodeRef]
  );

  /*
   * dnd-kit reports a new transform object on every pointer move, so it is
   * reduced to a string here. The memoized row below compares that string and
   * skips the render when the row did not actually move.
   */
  const transformCss = useMemo(() => {
    if (!transform) return undefined;
    if (!isDragging || !bounds) return CSS.Transform.toString(transform);

    /* A rect read mid-drag already includes the previous frame's transform, so
     * the range would slide with the row. */
    const { clamp } = dragLimits(
      { top: bounds.top, bottom: bounds.bottom },
      { top: bounds.rowTop, height: bounds.rowHeight }
    );

    // `x` is dropped: the list sorts on one axis, so a sideways offset has
    // nothing to be right about.
    return `translate3d(0, ${clamp(transform.y ?? 0)}px, 0)`;
  }, [transform, isDragging, bounds]);

  return (
    <ModRow
      mod={mod}
      position={position}
      selected={selected}
      reorderable={reorderable}
      attach={attach}
      attributes={attributes}
      listeners={listeners}
      transformCss={transformCss}
      transition={transition}
      isDragging={isDragging}
    />
  );
}

/*
 * dnd-kit re-renders every sortable on each pointer move, so the row is
 * memoized on the transform it actually renders.
 */
interface ModRowProps {
  mod: Mod;
  position: number | null;
  selected: boolean;
  reorderable: boolean;
  attach: (node: HTMLTableRowElement | null) => void;
  attributes: DraggableAttributes;
  listeners: DraggableSyntheticListeners;
  transformCss: string | undefined;
  transition: string | undefined;
  isDragging: boolean;
}

const ModRow = memo(function ModRow({
  mod,
  position,
  selected,
  reorderable,
  attach,
  attributes,
  listeners,
  transformCss,
  transition,
  isDragging,
}: ModRowProps) {
  const toggleSelection = useModsStore((state) => state.toggleSelection);

  return (
    <TableRow
      ref={attach}
      style={
        transformCss ? { transform: transformCss, transition } : { transition }
      }
      className={cn(
        // `relative` is load-bearing: a `<tr>` is statically positioned, so `z-*`
        // on it is ignored and the dragged row paints underneath the sticky header.
        "relative",
        isDragging &&
          "z-50 bg-accent shadow-lg ring-1 ring-accent-foreground/20",
        selected && !isDragging && "bg-muted/50"
      )}
    >
      {/* The handle is the whole leading cell: `absolute inset-0` inside a
          `relative` cell fills it, so the grab area is the full row height. */}
      <TableCell className="relative w-10 p-0">
        <button
          type="button"
          aria-label={t("mods.reorder")}
          title={reorderable ? undefined : t("mods.reorderNeedsLoadOrder")}
          className={cn(
            "absolute inset-0 flex touch-none items-center justify-center transition-colors focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:outline-none focus-visible:ring-inset",
            reorderable
              ? "cursor-grab text-muted-foreground/70 hover:bg-accent/60 hover:text-foreground focus-visible:bg-accent/60 active:cursor-grabbing"
              : "cursor-not-allowed text-muted-foreground/30"
          )}
          {...attributes}
          {...listeners}
        >
          <GripVertical className="size-4" />
        </button>
      </TableCell>
      <TableCell className="w-8">
        <Checkbox
          aria-label={t("mods.select")}
          checked={selected}
          onCheckedChange={() => toggleSelection(mod.id)}
        />
      </TableCell>
      <ModCells mod={mod} position={position} />
    </TableRow>
  );
});

interface ModCellsProps {
  mod: Mod;
  position: number | null;
}

function issueSummary(issues: string[]): string {
  return issues
    .map((issue) =>
      issue === "multiple_manifests" ? t("mods.issue.multipleManifests") : issue
    )
    .join(" · ");
}

const ModCells = memo(function ModCells({ mod, position }: ModCellsProps) {
  const toggle = useToggleMod();
  const update = useUpdateMod();
  const cancelUpdate = useCancelUpdate();
  const uninstall = useUninstallMod();
  const relink = useRelinkMod();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [trashAvailable, setTrashAvailable] = useState(false);
  const updateInfo = useModsStore((state) => selectUpdateFor(state, mod.id));
  const linkStatus = useModsStore((state) => selectLinkStatus(state, mod.id));
  const activity = useActivityStore((state) =>
    selectProgressFor(state, mod.id)
  );
  const modActions = useGameActionsForMod();

  const damaged = linkStatus?.damaged ?? 0;

  const enableSwitch = (
    <Switch
      size="default"
      checked={mod.enabled}
      disabled={mod.external || toggle.isPending}
      aria-label={
        mod.enabled
          ? `${t("mods.disable")} ${mod.name}`
          : `${t("mods.enable")} ${mod.name}`
      }
      onCheckedChange={(on) => toggle.mutate({ id: mod.id, enable: on })}
    />
  );

  return (
    <>
      {/* The enable switch sits with the name: it is the control a user reaches
          for most often, and at the row edge it competes with four other buttons. */}
      <TableCell className="max-w-xs">
        <div className="flex min-w-0 items-center gap-2.5">
          {mod.external ? (
            <Tooltip>
              {/* A disabled switch swallows pointer events, so the span carries them. */}
              <TooltipTrigger asChild>
                <span tabIndex={0} className="inline-flex">
                  {enableSwitch}
                </span>
              </TooltipTrigger>
              <TooltipContent>{t("mods.externalHint")}</TooltipContent>
            </Tooltip>
          ) : (
            enableSwitch
          )}
          <span
            className={cn(
              "min-w-0 truncate font-medium",
              mod.enabled ? "text-mod-on" : "text-mod-off"
            )}
          >
            {mod.name}
          </span>
          {mod.external && (
            <Badge
              variant="outline"
              className="shrink-0 border-muted-foreground/40"
            >
              {t("mods.external")}
            </Badge>
          )}
        </div>
      </TableCell>
      <TableCell className="w-24 font-mono text-xs">
        {mod.version || "—"}
      </TableCell>
      <TableCell className="w-32 text-xs text-muted-foreground">
        {mod.sourceId || "—"}
      </TableCell>
      <TableCell className="w-24">
        <div className="flex flex-wrap items-center gap-1">
          {updateInfo?.available && (
            <Tooltip>
              <TooltipTrigger asChild>
                <span
                  tabIndex={0}
                  role="img"
                  aria-label={t("mods.updateAvailable")}
                  className="inline-flex text-info"
                >
                  <ArrowUpCircle className="size-4" />
                </span>
              </TooltipTrigger>
              <TooltipContent>{t("mods.updateAvailable")}</TooltipContent>
            </Tooltip>
          )}
          {mod.issues.length > 0 && (
            <Tooltip>
              <TooltipTrigger asChild>
                <span
                  tabIndex={0}
                  role="img"
                  aria-label={t("mods.issues")}
                  className="inline-flex text-warning"
                >
                  <TriangleAlert className="size-4" />
                </span>
              </TooltipTrigger>
              <TooltipContent>{issueSummary(mod.issues)}</TooltipContent>
            </Tooltip>
          )}
          {activity && (
            <Badge
              variant="outline"
              className="max-w-full border-(--game-accent)/60 text-center whitespace-normal"
            >
              {t("mods.busy")}
            </Badge>
          )}
          {damaged > 0 && (
            <Badge
              variant="outline"
              className="max-w-full border-warning/60 text-center whitespace-normal text-warning"
            >
              {t("mods.damaged", { count: damaged })}
            </Badge>
          )}
          {mod.external && !mod.enabled && (
            <Badge
              variant="outline"
              title={t("mods.externalDisabledHint")}
              className="max-w-full border-warning/60 text-center whitespace-normal text-warning"
            >
              {t("mods.externalDisabled")}
            </Badge>
          )}
        </div>
      </TableCell>
      {/* The load position, 1-based: 1 is the first mod the game loads. It is the
          number the backend's tiebreak cannot change for a named mod. */}
      <TableCell
        className="w-16 text-xs text-muted-foreground tabular-nums"
        title={t("mods.priorityHint")}
      >
        {position ?? "—"}
      </TableCell>
      <TableCell className="w-24 text-right">
        <div className="flex items-center justify-end gap-1">
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                variant="ghost"
                size="icon"
                className="size-7"
                aria-label={t("mods.more")}
              >
                <EllipsisVertical className="size-4" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-56">
              {!mod.external && updateInfo?.available ? (
                <>
                  <DropdownMenuItem
                    disabled={update.isPending}
                    onSelect={() => update.mutate(mod.id)}
                  >
                    <Download className="size-4" />
                    {t("mods.updateTo", { version: updateInfo.remoteVersion })}
                  </DropdownMenuItem>
                  <DropdownMenuItem
                    disabled={cancelUpdate.isPending}
                    onSelect={() => cancelUpdate.mutate(mod.id)}
                  >
                    <RefreshCw className="size-4" />
                    {t("mods.cancelUpdate")}
                  </DropdownMenuItem>
                  <DropdownMenuSeparator />
                </>
              ) : null}

              {!mod.external &&
                modActions.actions.map((action) => (
                  <DropdownMenuItem
                    key={action.id}
                    onSelect={() => modActions.run(action.id)}
                  >
                    <ExternalLink className="size-4" />
                    {action.displayName}
                  </DropdownMenuItem>
                ))}
              {!mod.external && modActions.actions.length > 0 && (
                <DropdownMenuSeparator />
              )}

              {!mod.external && (
                <DropdownMenuItem
                  onSelect={() => useUiStore.getState().openDrawer(mod.id)}
                >
                  <FileCode2 className="size-4" />
                  {t("mods.details")}
                </DropdownMenuItem>
              )}

              {!mod.external && damaged > 0 && (
                <DropdownMenuItem
                  disabled={relink.isPending}
                  onSelect={() => relink.mutate(mod.id)}
                >
                  <RefreshCw className="size-4" />
                  {t("mods.relink")}
                </DropdownMenuItem>
              )}

              {!mod.external && <DropdownMenuSeparator />}

              <DropdownMenuItem
                className="text-destructive focus:text-destructive"
                disabled={uninstall.isPending}
                onSelect={() => {
                  setConfirmDelete(true);
                  if (mod.external) {
                    api
                      .trashAvailable()
                      .then(setTrashAvailable)
                      .catch(() => setTrashAvailable(false));
                  }
                }}
              >
                <Trash2 className="size-4" />
                {t("mods.uninstall")}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </TableCell>
      <AlertDialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {mod.external
                ? t("mods.deleteExternalTitle", { name: mod.name })
                : t("mods.uninstallConfirm", { name: mod.name })}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {mod.external
                ? t("mods.deleteExternalHint")
                : t("mods.deleteKnownHint")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            {mod.external ? (
              <>
                {trashAvailable && (
                  <AlertDialogAction
                    variant="outline"
                    onClick={() =>
                      uninstall.mutate({
                        id: mod.id,
                        purgeCache: false,
                        trash: true,
                      })
                    }
                  >
                    {t("mods.moveToTrash")}
                  </AlertDialogAction>
                )}
                <AlertDialogAction
                  variant="destructive"
                  onClick={() =>
                    uninstall.mutate({
                      id: mod.id,
                      purgeCache: false,
                      trash: false,
                    })
                  }
                >
                  {t("mods.deletePermanently")}
                </AlertDialogAction>
              </>
            ) : (
              <AlertDialogAction
                variant="destructive"
                onClick={() =>
                  uninstall.mutate({ id: mod.id, purgeCache: false })
                }
              >
                {t("mods.uninstall")}
              </AlertDialogAction>
            )}
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
});

function bulkMessage(
  action: BulkAction,
  count: number
): { title: string; hint: string } {
  switch (action) {
    case "enable":
      return {
        title: t("mods.bulkEnableTitle", { count }),
        hint: t("mods.bulkEnableHint"),
      };
    case "disable":
      return {
        title: t("mods.bulkDisableTitle", { count }),
        hint: t("mods.bulkDisableHint"),
      };
    case "delete":
      return {
        title: t("mods.bulkDeleteTitle", { count }),
        hint: t("mods.bulkDeleteHint"),
      };
    case "deleteExternal":
      return {
        title: t("mods.bulkDeleteExternalTitle", { count }),
        hint: t("mods.bulkDeleteExternalHint"),
      };
  }
}

/* `status` sorts by enabled state, the distinction the column exists to make;
 * `priority` reverses the resolved load order. */
function sortMods(
  mods: Mod[],
  positions: Map<string, number>,
  sort: { key: SortKey; ascending: boolean }
): Mod[] {
  const direction = sort.ascending ? 1 : -1;
  const byName = (a: Mod, b: Mod) => a.name.localeCompare(b.name);

  const sorted = [...mods];
  sorted.sort((a, b) => {
    switch (sort.key) {
      case "name":
        return direction * byName(a, b);
      case "version":
        return (
          direction *
          a.version.localeCompare(b.version, undefined, { numeric: true })
        );
      case "source":
        return direction * a.sourceId.localeCompare(b.sourceId) || byName(a, b);
      case "status":
        return (
          direction * (Number(a.enabled) - Number(b.enabled)) || byName(a, b)
        );
      case "priority":
        return (
          direction *
          ((positions.get(a.id) ?? Number.MAX_SAFE_INTEGER) -
            (positions.get(b.id) ?? Number.MAX_SAFE_INTEGER))
        );
    }
  });
  return sorted;
}
