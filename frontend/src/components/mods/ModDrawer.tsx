import { type ReactNode, useMemo } from "react";
import {
  Copy,
  ExternalLink,
  FileCode2,
  FileText,
  Folder,
  FolderOpen,
} from "lucide-react";
import { toast } from "sonner";

import * as api from "@/bridge/client";
import {
  copyToClipboard,
  describeBridgeFailure,
  revealPath,
} from "@/bridge/dialogs";
import { EmptyState } from "@/components/common/Surfaces";
import { ModHooksEditor } from "@/components/mods/ModHooksEditor";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Separator } from "@/components/ui/separator";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from "@/components/ui/sheet";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  useInstallMod,
  useRelinkMod,
  useWriteModManifest,
} from "@/hooks/mutations";
import {
  useModFilesQuery,
  useModHooksQuery,
  useSourceLinksQuery,
} from "@/hooks/queries";
import { formatBytes, formatDateTime, formatNumber } from "@/lib/format";
import { t } from "@/lib/i18n";
import { resolveLoadOrder } from "@/lib/loadOrder";
import {
  selectLinkStatus,
  selectLoadOrder,
  selectMod,
  selectMods,
  useModsStore,
} from "@/stores/mods.store";
import {
  MOD_DRAWER_TABS,
  type ModDrawerTab,
  useUiStore,
} from "@/stores/ui.store";
import type { Mod } from "@/types/domain";

const TAB_LABELS: Record<ModDrawerTab, string> = {
  details: "mods.tab.details",
  source: "mods.tab.source",
  hooks: "mods.tab.hooks",
  files: "mods.tab.files",
};

interface FieldProps {
  label: string;
  value: string;
}

function Field({ label, value }: FieldProps) {
  return (
    <div className="grid grid-cols-[8rem_minmax(0,1fr)] gap-2 text-sm">
      <span className="text-muted-foreground">{label}</span>
      <span className="wrap-break-word" data-selectable>
        {value || "—"}
      </span>
    </div>
  );
}

function recordField(value: unknown): Record<string, unknown> | null {
  return typeof value === "object" && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function textField(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function numberField(value: unknown): number | null {
  const parsed = typeof value === "number" ? value : Number(value);
  return Number.isFinite(parsed) ? parsed : null;
}

const STATUS_LABELS: Record<string, string> = {
  published: "mods.status.published",
  not_published: "mods.status.notPublished",
  hidden: "mods.status.hidden",
  under_moderation: "mods.status.underModeration",
  removed: "mods.status.removed",
  removed_by_staff: "mods.status.removedByStaff",
};

/* `openModSource` returns the fallback alongside any error, so a Nexus mod with
 * no direct download still answers with the page. */
async function openSource(modId: string, target: string): Promise<void> {
  try {
    await api.openModSource(modId, target);
  } catch (error) {
    toast.error(describeBridgeFailure(error));
  }
}

interface DetailsTabProps {
  mod: Mod;
}

function DetailsTab({ mod }: DetailsTabProps) {
  const writeManifest = useWriteModManifest();
  const install = useInstallMod();
  const mods = useModsStore(selectMods);
  const installed = new Set(mods.map((item) => item.id));
  const loadOrder = useModsStore(selectLoadOrder);
  const { positions } = useMemo(
    () => resolveLoadOrder(mods, loadOrder),
    [mods, loadOrder]
  );

  const stats = recordField(mod.sourceData.stats);
  const uploader = recordField(mod.sourceData.uploader);
  const category = recordField(mod.sourceData.mod_category);
  const adultContent = mod.sourceData.adult_content;
  const status = textField(mod.sourceData.status);
  const statusLabel = STATUS_LABELS[status];
  const modVersion = textField(mod.sourceData.mod_version);
  const nexusTags = Array.isArray(mod.sourceData.nexus_tags)
    ? mod.sourceData.nexus_tags
        .map((tag) => textField(recordField(tag)?.name))
        .filter(Boolean)
        .join(", ")
    : "";
  const downloads = numberField(stats?.downloads);
  const uniqueDownloads = numberField(stats?.unique_downloads);
  const views = numberField(stats?.views);
  const endorsements = numberField(stats?.endorsements);
  const uploaderName = textField(uploader?.name);
  const categoryName = textField(category?.name);

  return (
    <ScrollArea className="h-full pr-3">
      <div className="flex flex-col gap-3 pb-4">
        {mod.description ? <p className="text-sm">{mod.description}</p> : null}

        <Field label={t("mods.field.version")} value={mod.version} />
        <Field label={t("mods.field.author")} value={mod.author ?? ""} />
        <Field label={t("mods.field.source")} value={mod.sourceId} />
        {modVersion ? (
          <Field label={t("mods.field.modVersion")} value={modVersion} />
        ) : null}
        {typeof adultContent === "boolean" ? (
          <Field
            label={t("mods.field.adultContent")}
            value={t(adultContent ? "common.yes" : "common.no")}
          />
        ) : null}
        {status ? (
          <Field
            label={t("mods.field.status")}
            value={statusLabel ? t(statusLabel) : status}
          />
        ) : null}
        {categoryName ? (
          <Field label={t("mods.field.category")} value={categoryName} />
        ) : null}
        {uploaderName ? (
          <Field label={t("mods.field.uploader")} value={uploaderName} />
        ) : null}
        {downloads !== null ? (
          <Field
            label={t("mods.field.downloads")}
            value={formatNumber(downloads)}
          />
        ) : null}
        {uniqueDownloads !== null ? (
          <Field
            label={t("mods.field.uniqueDownloads")}
            value={formatNumber(uniqueDownloads)}
          />
        ) : null}
        {views !== null ? (
          <Field label={t("mods.field.views")} value={formatNumber(views)} />
        ) : null}
        {endorsements !== null ? (
          <Field
            label={t("mods.field.endorsements")}
            value={formatNumber(endorsements)}
          />
        ) : null}
        {nexusTags ? (
          <Field label={t("mods.field.tags")} value={nexusTags} />
        ) : null}
        <Field
          label={t("mods.field.gameVersion")}
          value={mod.gameVersion ?? ""}
        />
        <Field
          label={t("mods.field.installMode")}
          value={
            mod.installMode ? t(`mods.installMode.${mod.installMode}`) : ""
          }
        />
        <Field
          label={t("mods.field.priority")}
          value={positions.get(mod.id)?.toString() ?? ""}
        />
        <Field label={t("mods.field.hash")} value={mod.hash ?? ""} />
        <Field label={t("mods.field.path")} value={mod.localPath} />
        <Field
          label={t("mods.field.installedAt")}
          value={formatDateTime(mod.installedAt)}
        />
        <Field
          label={t("mods.field.updatedAt")}
          value={formatDateTime(mod.updatedAt)}
        />

        {mod.tags.length > 0 ? (
          <div className="flex flex-wrap gap-1">
            {mod.tags.map((tag) => (
              <Badge key={tag} variant="secondary">
                {tag}
              </Badge>
            ))}
          </div>
        ) : null}

        {mod.dependencies.length > 0 ? (
          <div className="flex flex-col gap-1">
            <span className="text-xs font-medium text-muted-foreground">
              {t("mods.field.requirements")}
            </span>
            {mod.dependencies.map((dep) => {
              const present = installed.has(dep);

              return (
                <div key={dep} className="flex items-center gap-2">
                  <Badge variant={present ? "secondary" : "destructive"}>
                    {present
                      ? t("mods.requirementInstalled")
                      : t("mods.requirementMissing")}
                  </Badge>
                  <code className="min-w-0 flex-1 truncate text-xs">{dep}</code>
                  {!present && mod.sourceId ? (
                    <Button
                      size="sm"
                      variant="outline"
                      loading={
                        install.isPending && install.variables?.modId === dep
                      }
                      onClick={() =>
                        install.mutate({ sourceId: mod.sourceId, modId: dep })
                      }
                    >
                      {t("mods.installRequirement")}
                    </Button>
                  ) : null}
                </div>
              );
            })}
          </div>
        ) : null}

        {typeof mod.sourceData.dependency_error === "string" ? (
          <p className="text-xs text-destructive">
            {t("mods.dependencyLookupFailed")}
          </p>
        ) : null}

        <div className="flex flex-wrap gap-1">
          {mod.conflictsWith.map((other) => (
            <Badge key={other} variant="destructive">
              {t("mods.field.conflictsWith")} {other}
            </Badge>
          ))}
        </div>

        <Separator />

        {/* Manifest is a button: it is one call that returns a path, and a tab
            holding a single button is a click to find it. */}
        <Button
          variant="secondary"
          size="sm"
          className="self-start"
          loading={writeManifest.isPending}
          onClick={async () => {
            try {
              const path = await writeManifest.mutateAsync(mod.id);
              toast.success(t("mods.manifestWritten", { path }));
            } catch {
              // the mutation already reported the failure
            }
          }}
        >
          <FileText className="size-4" />
          {t("mods.writeManifest")}
        </Button>
      </div>
    </ScrollArea>
  );
}

interface SourceTabProps {
  mod: Mod;
}

function SourceTab({ mod }: SourceTabProps) {
  const { data: links } = useSourceLinksQuery();
  const link = links?.get(mod.id) ?? null;

  if (!link) {
    return (
      <EmptyState
        icon={ExternalLink}
        title={t("mods.source.missing")}
        description={t("mods.source.missingHint")}
      />
    );
  }

  const rows = [
    { label: t("mods.source.page"), url: link.sourceUrl, target: "source" },
    {
      label: t("mods.source.download"),
      url: link.downloadUrl,
      target: "download",
    },
    { label: t("mods.source.update"), url: link.updateUrl, target: "update" },
  ].filter((row) => Boolean(row.url));

  return (
    <div className="flex flex-col gap-3 pb-4">
      {rows.length === 0 ? (
        <EmptyState
          icon={ExternalLink}
          title={t("mods.source.none")}
          description={t("mods.source.noneHint")}
        />
      ) : (
        rows.map((row) => (
          <div key={row.url} className="flex items-center gap-2">
            <div className="min-w-0 flex-1">
              <Label>{row.label}</Label>
              <p
                className="truncate text-xs text-muted-foreground"
                data-selectable
              >
                {row.url}
              </p>
            </div>
            {/* Opening goes through Go: the frontend cannot navigate, and
                `openModSource` runs the URL past the scheme allowlist first. */}
            <Button
              size="sm"
              variant="secondary"
              onClick={() => void openSource(mod.id, row.target)}
            >
              <ExternalLink className="size-4" />
              {t("mods.source.open")}
            </Button>
            <Button
              size="icon"
              variant="ghost"
              aria-label={t("common.copy")}
              onClick={() => void copyToClipboard(row.url ?? "")}
            >
              <Copy className="size-4" />
            </Button>
          </div>
        ))
      )}
      <p className="text-xs text-muted-foreground">
        {t("mods.source.openHint")}
      </p>
    </div>
  );
}

interface FilesTabProps {
  mod: Mod;
}

function FilesTab({ mod }: FilesTabProps) {
  const status = useModsStore((state) => selectLinkStatus(state, mod.id));
  const relink = useRelinkMod();
  // Keyed by the mod so switching mods does not show the previous mod's files
  // for the length of a request.
  const files = useModFilesQuery(mod.id);

  return (
    <div className="flex flex-col gap-3 pb-4">
      <div className="flex flex-wrap gap-2">
        <Button
          size="sm"
          variant="secondary"
          onClick={() => void copyToClipboard(mod.localPath)}
        >
          {t("common.copyPath")}
        </Button>
        <Button
          size="sm"
          variant="secondary"
          onClick={() => void revealPath(mod.localPath)}
        >
          <FolderOpen className="size-4" />
          {t("path.reveal")}
        </Button>
        {status && status.damaged > 0 ? (
          <Button
            size="sm"
            variant="secondary"
            loading={relink.isPending}
            onClick={() => relink.mutate(mod.id)}
          >
            {t("mods.relinkDamaged", { count: status.damaged })}
          </Button>
        ) : null}
      </div>

      {/* Two lists: contained files are what the mod ships, and most are never
          linked, so SymlinkStatus alone cannot say what is in the mod. */}
      <section className="flex flex-col gap-1.5">
        <h3 className="text-xs font-semibold tracking-wide text-muted-foreground uppercase">
          {t("mods.files.contained")}
        </h3>
        {files.isPending ? (
          <p className="text-sm text-muted-foreground">{t("common.loading")}</p>
        ) : (files.data ?? []).length === 0 ? (
          <p className="text-sm text-muted-foreground">
            {t("mods.files.containedEmpty")}
          </p>
        ) : (
          <ul className="flex flex-col gap-0.5 text-xs" data-selectable>
            {(files.data ?? []).map((file) => (
              <li key={file.path} className="flex items-center gap-2">
                {file.isDir ? (
                  <Folder className="size-3.5 shrink-0 text-(--game-accent)" />
                ) : (
                  <FileCode2 className="size-3.5 shrink-0 text-muted-foreground" />
                )}
                <span className="truncate">{file.path}</span>
                {file.isDir ? null : (
                  <span className="ml-auto shrink-0 text-muted-foreground tabular-nums">
                    {formatBytes(file.size)}
                  </span>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>

      <section className="flex flex-col gap-1.5">
        <h3 className="text-xs font-semibold tracking-wide text-muted-foreground uppercase">
          {t("mods.files.deployed")}
        </h3>
        {status && Object.keys(status.entries).length > 0 ? (
          <ul className="flex flex-col gap-1 text-xs" data-selectable>
            {Object.entries(status.entries).map(([path, state]) => (
              <li key={path} className="flex items-center gap-2">
                <Badge
                  variant={state === "ok" ? "secondary" : "destructive"}
                  className="shrink-0 font-mono"
                >
                  {t(`mods.linkState.${state}`)}
                </Badge>
                <span className="truncate text-muted-foreground">{path}</span>
              </li>
            ))}
          </ul>
        ) : (
          <p className="text-sm text-muted-foreground">
            {t("mods.files.none")}
          </p>
        )}
      </section>
    </div>
  );
}

interface DrawerBodyProps {
  mod: Mod;
}

function DrawerBody({ mod }: DrawerBodyProps) {
  const tab = useUiStore((state) => state.drawerTab);
  const setTab = useUiStore((state) => state.setDrawerTab);
  const hooks = useModHooksQuery(mod.id);

  return (
    <Tabs
      value={tab}
      onValueChange={(value) => setTab(value as ModDrawerTab)}
      className="flex h-full flex-col"
    >
      <TabsList className="mx-4 mt-2 grid w-auto grid-cols-4">
        {MOD_DRAWER_TABS.map((id) => (
          <TabsTrigger key={id} value={id}>
            {t(TAB_LABELS[id])}
          </TabsTrigger>
        ))}
      </TabsList>

      <div className="min-h-0 flex-1 px-4 py-3">
        <TabsContent value="details" className="h-full">
          <DetailsTab mod={mod} />
        </TabsContent>
        <TabsContent value="source" className="h-full">
          <SourceTab mod={mod} />
        </TabsContent>
        <TabsContent value="hooks" className="h-full">
          {hooks.isPending ? (
            <p className="text-sm text-muted-foreground">
              {t("common.loading")}
            </p>
          ) : (
            <ModHooksEditor modId={mod.id} />
          )}
        </TabsContent>
        <TabsContent value="files" className="h-full">
          <FilesTab mod={mod} />
        </TabsContent>
      </div>
    </Tabs>
  );
}

export interface ModDrawerProps {
  /** Optional trigger; the row menu normally opens the drawer imperatively. */
  children?: ReactNode;
}

/**
 * One drawer with four tabs: "everything known about this mod" is one gesture,
 * and the Source tab is the only place a mod's origin is visible now that
 * Sources moved into Settings.
 */
export function ModDrawer({ children }: ModDrawerProps) {
  const modId = useUiStore((state) => state.drawerModId);
  const close = useUiStore((state) => state.closeDrawer);
  const mod = useModsStore((state) =>
    modId ? selectMod(state, modId) : undefined
  );

  return (
    <Sheet
      open={modId !== null}
      onOpenChange={(open) => {
        if (!open) close();
      }}
    >
      {children ? <SheetTrigger asChild>{children}</SheetTrigger> : null}
      <SheetContent side="right" className="flex w-2xl flex-col p-0">
        <SheetHeader className="px-4 pt-4">
          <SheetTitle className="flex items-center gap-2">
            <FileCode2 className="size-4" />
            {mod?.name ?? modId}
          </SheetTitle>
          <SheetDescription>
            {mod ? `${mod.version} · ${mod.sourceId}` : t("common.loading")}
          </SheetDescription>
        </SheetHeader>

        {mod ? (
          <DrawerBody mod={mod} />
        ) : (
          <EmptyState icon={FolderOpen} title={t("mods.notFound")} />
        )}
      </SheetContent>
    </Sheet>
  );
}
