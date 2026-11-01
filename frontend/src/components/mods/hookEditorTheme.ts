import {
  autocompletion,
  type Completion,
  type CompletionContext,
} from "@codemirror/autocomplete";
import { javascript } from "@codemirror/lang-javascript";
import {
  HighlightStyle,
  StreamLanguage,
  syntaxHighlighting,
} from "@codemirror/language";
import { lua } from "@codemirror/legacy-modes/mode/lua";
import type { Extension } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { tags } from "@lezer/highlight";

import type { ScriptLanguage } from "@/types/domain";

/**
 * The hook editor's appearance. CodeMirror's defaults are tuned for a white
 * page, so on a dark surface they render as a bright block. Two HighlightStyles
 * do not merge — the later wins for every tag both define — so there is exactly
 * one style, with colours as CSS custom properties shared by light and dark.
 */
export const hookTheme: Extension = EditorView.theme(
  {
    "&": {
      color: "var(--foreground)",
      backgroundColor: "var(--code-bg)",
      height: "100%",
      fontSize: "12px",
    },
    ".cm-content": {
      caretColor: "var(--game-accent)",
      padding: "0.5rem 0",
      fontFamily: "var(--font-mono)",
    },
    ".cm-cursor, .cm-dropCursor": { borderLeftColor: "var(--game-accent)" },
    ".cm-selectionBackground, .cm-content ::selection": {
      backgroundColor:
        "color-mix(in oklab, var(--game-accent) 32%, transparent)",
    },
    ".cm-gutters": {
      backgroundColor: "var(--code-bg)",
      color: "var(--code-gutter)",
      border: "none",
      borderRight: "1px solid var(--border)",
    },
    ".cm-activeLine": {
      backgroundColor:
        "color-mix(in oklab, var(--game-accent) 7%, transparent)",
    },
    ".cm-activeLineGutter": {
      backgroundColor:
        "color-mix(in oklab, var(--game-accent) 7%, transparent)",
      color: "var(--muted-foreground)",
    },
    ".cm-lineNumbers .cm-gutterElement": { padding: "0 0.5rem 0 0.75rem" },
    ".cm-scroller": { fontFamily: "var(--font-mono)", lineHeight: "1.6" },
  },
  { dark: false }
);

const hookHighlighting = syntaxHighlighting(
  HighlightStyle.define([
    { tag: tags.comment, color: "var(--code-comment)", fontStyle: "italic" },
    {
      tag: tags.lineComment,
      color: "var(--code-comment)",
      fontStyle: "italic",
    },
    {
      tag: tags.blockComment,
      color: "var(--code-comment)",
      fontStyle: "italic",
    },
    { tag: tags.docComment, color: "var(--code-comment)", fontStyle: "italic" },

    { tag: [tags.keyword, tags.modifier], color: "var(--code-keyword)" },
    {
      tag: [tags.controlKeyword, tags.moduleKeyword],
      color: "var(--code-keyword)",
    },
    { tag: tags.definitionKeyword, color: "var(--code-keyword)" },

    {
      tag: [tags.string, tags.special(tags.string)],
      color: "var(--code-string)",
    },
    { tag: tags.regexp, color: "var(--code-string)" },
    { tag: tags.escape, color: "var(--code-number)" },

    {
      tag: [tags.number, tags.bool, tags.null, tags.atom],
      color: "var(--code-number)",
    },

    {
      tag: [tags.function(tags.variableName), tags.function(tags.propertyName)],
      color: "var(--code-function)",
    },
    {
      tag: [tags.typeName, tags.className, tags.namespace],
      color: "var(--code-type)",
    },
    { tag: tags.propertyName, color: "var(--code-property)" },
    { tag: tags.operator, color: "var(--code-operator)" },
    { tag: tags.punctuation, color: "var(--muted-foreground)" },
    { tag: tags.bracket, color: "var(--muted-foreground)" },

    { tag: tags.variableName, color: "var(--foreground)" },
    { tag: tags.invalid, color: "var(--destructive)" },
  ])
);

/** Line wrapping: a Lua function longer than the drawer should not scroll away. */
const wrapping = EditorView.lineWrapping;

/* The runner installs the whole `dmm` surface in both languages and refuses a
   call only when its permission is missing, so this list can be static. */
const dmmCompletions: Completion[] = [
  { label: "dmm", type: "variable", detail: "hook runtime" },
  { label: "dmm.context", type: "property", detail: "run context" },
  { label: "dmm.context.game.path", type: "property", detail: "string" },
  { label: "dmm.context.game.version", type: "property", detail: "string" },
  { label: "dmm.context.game.mods_path", type: "property", detail: "string" },
  {
    label: "dmm.context.config.cache_path",
    type: "property",
    detail: "string",
  },
  {
    label: "dmm.context.config.temp_path",
    type: "property",
    detail: "string",
  },
  {
    label: "dmm.context.config.storage_path",
    type: "property",
    detail: "string",
  },
  { label: "dmm.context.phase", type: "property", detail: "string" },
  { label: "dmm.context.mod.id", type: "property", detail: "string" },
  { label: "dmm.context.mod.name", type: "property", detail: "string" },
  { label: "dmm.context.mod.version", type: "property", detail: "string" },
  { label: "dmm.context.mod.path", type: "property", detail: "string" },
  { label: "dmm.context.mod.enabled", type: "property", detail: "boolean" },
  { label: "dmm.context.index", type: "property", detail: "number" },
  { label: "dmm.log.info", type: "method", detail: "(...args)" },
  { label: "dmm.log.warn", type: "method", detail: "(...args)" },
  { label: "dmm.log.error", type: "method", detail: "(...args)" },
  { label: "dmm.log.debug", type: "method", detail: "(...args)" },
  { label: "dmm.limits.maxLogLines", type: "property", detail: "200" },
  { label: "dmm.limits.maxLogLineLength", type: "property", detail: "4096" },
  { label: "dmm.paths.game", type: "property", detail: "string" },
  { label: "dmm.paths.mods", type: "property", detail: "string" },
  { label: "dmm.paths.mod", type: "property", detail: "string" },
  { label: "dmm.paths.cache", type: "property", detail: "string" },
  { label: "dmm.paths.temp", type: "property", detail: "string" },
  { label: "dmm.paths.storage", type: "property", detail: "string" },
  { label: "dmm.fs.readFile", type: "method", detail: "(path) -> string" },
  { label: "dmm.fs.list", type: "method", detail: "(path) -> DirEntry[]" },
  { label: "dmm.fs.exists", type: "method", detail: "(path) -> boolean" },
  { label: "dmm.fs.stat", type: "method", detail: "(path) -> FileInfo" },
  {
    label: "dmm.fs.writeTemp",
    type: "method",
    detail: "(name, content) -> string",
  },
  { label: "dmm.config.get", type: "method", detail: "(key) -> string" },
  { label: "dmm.config.getAll", type: "method", detail: "() -> map" },
  { label: "dmm.config.set", type: "method", detail: "(key, value)" },
  {
    label: "dmm.storage.list",
    type: "method",
    detail: "() -> StorageEntry[]",
  },
  {
    label: "dmm.storage.path",
    type: "method",
    detail: "(gameID, modID, version) -> string",
  },
  { label: "dmm.env.get", type: "method", detail: "(key) -> string" },
  { label: "print", type: "function", detail: "(...args)" },
];

const consoleCompletions: Completion[] = [
  { label: "console.log", type: "method", detail: "(...args)" },
  { label: "console.info", type: "method", detail: "(...args)" },
  { label: "console.warn", type: "method", detail: "(...args)" },
  { label: "console.error", type: "method", detail: "(...args)" },
];

/** The completions a hook language gets; console is JavaScript-only. */
export function hookApiCompletions(language: ScriptLanguage): Completion[] {
  return language === "javascript"
    ? [...dmmCompletions, ...consoleCompletions]
    : dmmCompletions;
}

function completeHookApi(language: ScriptLanguage) {
  const options = hookApiCompletions(language);

  return (context: CompletionContext) => {
    const word = context.matchBefore(/[\w.$]*/);
    if (!word || (word.from === word.to && !context.explicit)) return null;

    return { from: word.from, options };
  };
}

export function hookExtensions(language: ScriptLanguage): Extension[] {
  return [
    hookTheme,
    hookHighlighting,
    wrapping,
    hookLanguage(language),
    autocompletion({ override: [completeHookApi(language)] }),
  ];
}

/**
 * The language support for a hook. Lua is a legacy mode, so it arrives as a
 * `StreamParser` and has to be defined before use.
 */
export function hookLanguage(language: ScriptLanguage): Extension {
  return language === "javascript" ? javascript() : StreamLanguage.define(lua);
}

export const LANGUAGES: Record<ScriptLanguage, string> = {
  lua: "Lua",
  javascript: "JavaScript",
};
