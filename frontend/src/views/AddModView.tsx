import { useId, useState } from "react";
import {
  ClipboardPaste,
  Link2,
  MousePointerSquareDashed,
  Search,
  TrendingUp,
} from "lucide-react";

import { openLink, pasteFromClipboard } from "@/bridge/dialogs";
import {
  EmptyState,
  ErrorNote,
  FormSection,
  Hint,
} from "@/components/common/Surfaces";
import {
  ApiKeyPrompt,
  needsCredential,
} from "@/components/sources/ApiKeyPrompt";
import { SearchResultCard } from "@/components/sources/SearchResultCard";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { useImportLink, useInstallMod } from "@/hooks/mutations";
import {
  SEARCH_PAGE_SIZE,
  useSearchQuery,
  useSourcesQuery,
} from "@/hooks/queries";
import { t } from "@/lib/i18n";
import { selectMods, useModsStore } from "@/stores/mods.store";
import type { Mod } from "@/types/domain";

const ALL_SOURCES = "__all__";

export default function AddModView() {
  const installedIds = useModsStore(selectMods);
  const installed = new Set(installedIds.map((mod) => mod.id));

  const [text, setText] = useState("");
  const [sourceId, setSourceId] = useState<string>(ALL_SOURCES);
  const [gameVersion, setGameVersion] = useState("");
  const [link, setLink] = useState("");
  const [page, setPage] = useState(1);

  const sources = useSourcesQuery();
  const install = useInstallMod();

  const search = useSearchQuery({
    text,
    sourceId: sourceId === ALL_SOURCES ? undefined : sourceId,
    gameVersion: gameVersion.trim() || undefined,
    page,
  });

  /* Derived from the failure itself: AUTH_REQUIRED turns a Nexus refusal into a prompt. */
  const credentialFailure =
    search.isError && needsCredential(search.error) ? search.error : null;
  const [dismissed, setDismissed] = useState<unknown>(null);
  const showKeyPrompt =
    credentialFailure !== null && credentialFailure !== dismissed;

  const results = search.data ?? [];
  // A short page is the only end-of-list signal the bridge gives us: it returns a
  // bare array, so a full page is the only thing that proves there is more.
  const hasMore = results.length >= SEARCH_PAGE_SIZE;

  // Editing the query starts at page one again: the old page index belongs to
  // the previous result set.
  const editQuery = (setter: (value: string) => void) => (value: string) => {
    setter(value);
    setPage(1);
  };

  const runInstall = (mod: Mod) =>
    install.mutate(
      { sourceId: mod.sourceId, modId: mod.id },
      {
        onError: () => {
          // Nexus rejects anonymous downloads; the mod page is the useful fallback.
          if (mod.sourceUrl) openLink(mod.sourceUrl);
        },
      }
    );

  return (
    <div className="flex h-full flex-col">
      <div className="border-b px-4 py-2">
        <h1 className="text-sm font-semibold">{t("add.title")}</h1>
      </div>

      <div className="min-h-0 flex-1 overflow-auto p-4">
        <div className="mx-auto flex max-w-4xl flex-col gap-6">
          {/* The link box is first: a user with an `nxm://` URI in their
              clipboard is past searching. */}
          <LinkPanel link={link} onLinkChange={setLink} />

          <section className="flex min-w-0 flex-col gap-3">
            <div className="flex flex-wrap items-end gap-2">
              <div className="min-w-52 flex-1">
                <label
                  className="mb-1 block text-xs text-muted-foreground"
                  htmlFor="add-search"
                >
                  {t("add.searchPlaceholder")}
                </label>
                <Input
                  id="add-search"
                  value={text}
                  placeholder={t("add.searchPlaceholder")}
                  onChange={(event) => editQuery(setText)(event.target.value)}
                />
              </div>

              <div className="w-44">
                <label
                  className="mb-1 block text-xs text-muted-foreground"
                  htmlFor="add-source"
                >
                  {t("add.source")}
                </label>
                <Select
                  value={sourceId}
                  onValueChange={(next) => {
                    setSourceId(next);
                    setPage(1);
                  }}
                >
                  <SelectTrigger id="add-source" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value={ALL_SOURCES}>
                      {t("add.searchAll")}
                    </SelectItem>
                    {(sources.data ?? []).map((source) => (
                      <SelectItem key={source.id} value={source.id}>
                        {source.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>

              <div className="w-40">
                <label
                  className="mb-1 block text-xs text-muted-foreground"
                  htmlFor="add-version"
                >
                  {t("add.gameVersion")}
                </label>
                <Input
                  id="add-version"
                  value={gameVersion}
                  onChange={(event) =>
                    editQuery(setGameVersion)(event.target.value)
                  }
                />
              </div>
            </div>

            <div className="flex items-center justify-between">
              <span className="text-xs font-semibold tracking-wide text-muted-foreground uppercase">
                {t("add.results")}
              </span>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => void search.refetch()}
                disabled={!text.trim()}
                loading={search.isFetching}
              >
                {t("common.refresh")}
              </Button>
            </div>

            {/* Nexus has no search endpoint — its API offers a five-entry
                trending feed only — so queries there match against that. */}
            {(sourceId === "nexus" ||
              (sourceId === ALL_SOURCES &&
                (sources.data ?? []).some(
                  (source) => source.id === "nexus"
                ))) &&
            text.trim() ? (
              <p className="flex items-start gap-2 rounded-md border border-warning/40 bg-warning/10 px-3 py-2 text-xs text-warning">
                <TrendingUp className="mt-0.5 size-3.5 shrink-0" />
                {t("add.nexusTrendingOnly")}
              </p>
            ) : null}

            {search.isError ? (
              <ErrorNote
                error={search.error}
                onRetry={() => void search.refetch()}
              />
            ) : null}

            {!text.trim() ? (
              <EmptyState
                icon={Search}
                title={t("add.searchFirst")}
                description={t("add.dropHint")}
              />
            ) : null}

            {text.trim() &&
            !search.isPending &&
            results.length === 0 &&
            !search.isError ? (
              <EmptyState icon={Search} title={t("add.empty")} />
            ) : null}

            <div className="flex flex-col gap-2">
              {results.map((mod) => (
                <SearchResultCard
                  key={`${mod.sourceId}:${mod.id}`}
                  mod={mod}
                  installed={installed.has(mod.id)}
                  busy={install.isPending}
                  installing={
                    install.isPending && install.variables?.modId === mod.id
                  }
                  onInstall={runInstall}
                />
              ))}
            </div>

            {hasMore ? (
              <Button
                variant="outline"
                className="self-start"
                loading={search.isFetching}
                onClick={() => setPage((current) => current + 1)}
              >
                {t("add.loadMore")}
              </Button>
            ) : null}
          </section>

          <ApiKeyPrompt
            sourceId="nexus"
            open={showKeyPrompt}
            onOpenChange={(next) => {
              if (!next) setDismissed(search.error);
            }}
            reason={t("apikey.reason.search")}
          />
        </div>
      </div>
    </div>
  );
}

/** One field — under the react-hook-form threshold — so it stays `useState`. */
interface LinkPanelProps {
  link: string;
  onLinkChange: (value: string) => void;
}

/* Mirrors domain.ParseModLink: the launcher scheme or the "nxm link" chat form,
   with the legacy game/mod/file path or the canonical mods/<id>/files/<id> one. */
const NXM_LINK =
  /^(?:nxm:\/\/|nxm link )[\w.-]+\/(?:mods\/)?\d+\/(?:files\/)?\d+\/?(?:\?.*)?$/i;

function isNxmLink(value: string): boolean {
  return NXM_LINK.test(value);
}

function LinkPanel({ link, onLinkChange }: LinkPanelProps) {
  const textareaId = useId();
  const importLink = useImportLink();
  const [missingKey, setMissingKey] = useState(false);
  const trimmed = link.trim();
  const valid = isNxmLink(trimmed);

  /* Nexus links need credentials; prompting here beats discovery via Settings. */
  const onImportError = (error: unknown) => {
    if (needsCredential(error)) setMissingKey(true);
  };

  return (
    <FormSection title={t("add.linkTitle")} description={t("add.linkHint")}>
      <label className="flex flex-col gap-1.5" htmlFor={textareaId}>
        <span className="sr-only">{t("add.linkTitle")}</span>
        <Textarea
          id={textareaId}
          rows={1}
          value={link}
          placeholder={t("add.linkPlaceholder")}
          onChange={(event) => onLinkChange(event.target.value)}
        />
      </label>

      {trimmed && !valid ? (
        <span className="text-xs text-destructive">{t("add.linkFormat")}</span>
      ) : null}

      <div className="flex flex-wrap items-center gap-2">
        <Button
          size="sm"
          variant="outline"
          onClick={() => {
            void pasteFromClipboard().then((text) => {
              // An unreadable clipboard reports empty, which must not wipe a
              // link the user already typed.
              if (text.trim()) onLinkChange(text.trim());
            });
          }}
        >
          <ClipboardPaste className="size-4" />
          {t("add.pasteLink")}
        </Button>

        <Button
          size="sm"
          disabled={!valid}
          loading={importLink.isPending}
          onClick={() => {
            importLink.mutate(trimmed, {
              onSuccess: () => onLinkChange(""),
              onError: onImportError,
            });
          }}
        >
          <Link2 className="size-4" />
          {t("add.installFromLink")}
        </Button>
      </div>

      <Hint>
        <MousePointerSquareDashed className="size-3.5 shrink-0" />
        {t("add.dropHint")}
      </Hint>

      <ApiKeyPrompt
        sourceId="nexus"
        open={missingKey}
        onOpenChange={setMissingKey}
        reason={t("apikey.reason.link")}
      />
    </FormSection>
  );
}
