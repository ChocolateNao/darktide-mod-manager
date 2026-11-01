/**
 * Asserts the forwarding: each generated call rejects, so a wrapper that reaches
 * the wrong function, or passes arguments in the wrong order, fails here. The
 * mappers have their own tests.
 */
import { describe, expect, it, vi } from "vitest";

import { hookPhases } from "@/lib/schemas/mod";
import type { ModHooks } from "@/types/domain";

type Stub = ReturnType<typeof vi.fn>;

/** One stub per generated function, created on first use so the table can name
 *  a function the test never happens to call. */
const generated: Record<string, Record<string, Stub>> = {};

/** Callable as well as assertable: the generated module calls it, the test
 *  inspects it, and `vi.fn()` alone only types as the latter. */
type GeneratedStub = Stub & ((...rest: unknown[]) => Promise<never>);

const stubFor = (moduleName: string, fnName: string): GeneratedStub => {
  const existing = generated[moduleName]?.[fnName] as GeneratedStub | undefined;
  if (existing) return existing;
  const created = vi
    .fn()
    .mockRejectedValue(new Error("wails stub")) as GeneratedStub;
  generated[moduleName] = { ...generated[moduleName], [fnName]: created };
  return created;
};

vi.mock("wailsjs/go/commands/ActionCommands", () => ({
  CurrentGameID: (...rest: unknown[]) =>
    stubFor("ActionCommands", "CurrentGameID")(...rest),
  ExecuteGameAction: (...rest: unknown[]) =>
    stubFor("ActionCommands", "ExecuteGameAction")(...rest),
  GetGameAction: (...rest: unknown[]) =>
    stubFor("ActionCommands", "GetGameAction")(...rest),
  ListActions: (...rest: unknown[]) =>
    stubFor("ActionCommands", "ListActions")(...rest),
  ListAvailableActions: (...rest: unknown[]) =>
    stubFor("ActionCommands", "ListAvailableActions")(...rest),
  Platform: (...rest: unknown[]) =>
    stubFor("ActionCommands", "Platform")(...rest),
}));
vi.mock("wailsjs/go/commands/CacheCommands", () => ({
  CacheRoot: (...rest: unknown[]) =>
    stubFor("CacheCommands", "CacheRoot")(...rest),
  ClearCache: (...rest: unknown[]) =>
    stubFor("CacheCommands", "ClearCache")(...rest),
  GetCacheInfo: (...rest: unknown[]) =>
    stubFor("CacheCommands", "GetCacheInfo")(...rest),
  PruneCache: (...rest: unknown[]) =>
    stubFor("CacheCommands", "PruneCache")(...rest),
  PruneWithDefaults: (...rest: unknown[]) =>
    stubFor("CacheCommands", "PruneWithDefaults")(...rest),
}));
vi.mock("wailsjs/go/commands/ConfigCommands", () => ({
  ConfigKeys: (...rest: unknown[]) =>
    stubFor("ConfigCommands", "ConfigKeys")(...rest),
  ConfigPath: (...rest: unknown[]) =>
    stubFor("ConfigCommands", "ConfigPath")(...rest),
  GetConfig: (...rest: unknown[]) =>
    stubFor("ConfigCommands", "GetConfig")(...rest),
  GetConfigValue: (...rest: unknown[]) =>
    stubFor("ConfigCommands", "GetConfigValue")(...rest),
  SetConfigValue: (...rest: unknown[]) =>
    stubFor("ConfigCommands", "SetConfigValue")(...rest),
  UpdateConfig: (...rest: unknown[]) =>
    stubFor("ConfigCommands", "UpdateConfig")(...rest),
}));
vi.mock("wailsjs/go/commands/GameCommands", () => ({
  CurrentGame: (...rest: unknown[]) =>
    stubFor("GameCommands", "CurrentGame")(...rest),
  DetectGame: (...rest: unknown[]) =>
    stubFor("GameCommands", "DetectGame")(...rest),
  GetModsPath: (...rest: unknown[]) =>
    stubFor("GameCommands", "GetModsPath")(...rest),
  SetGamePath: (...rest: unknown[]) =>
    stubFor("GameCommands", "SetGamePath")(...rest),
  SupportedGames: (...rest: unknown[]) =>
    stubFor("GameCommands", "SupportedGames")(...rest),
  UseGame: (...rest: unknown[]) => stubFor("GameCommands", "UseGame")(...rest),
  ValidateGamePath: (...rest: unknown[]) =>
    stubFor("GameCommands", "ValidateGamePath")(...rest),
}));
vi.mock("wailsjs/go/main/lifecycle", () => ({
  CancelAll: (...rest: unknown[]) => stubFor("Lifecycle", "CancelAll")(...rest),
  EventTypes: (...rest: unknown[]) =>
    stubFor("Lifecycle", "EventTypes")(...rest),
  HookPhases: (...rest: unknown[]) =>
    stubFor("Lifecycle", "HookPhases")(...rest),
  HookTimeout: (...rest: unknown[]) =>
    stubFor("Lifecycle", "HookTimeout")(...rest),
  HookTypes: (...rest: unknown[]) => stubFor("Lifecycle", "HookTypes")(...rest),
  Stopped: (...rest: unknown[]) => stubFor("Lifecycle", "Stopped")(...rest),
  SupportsHookType: (...rest: unknown[]) =>
    stubFor("Lifecycle", "SupportsHookType")(...rest),
  ValidateHookScript: (...rest: unknown[]) =>
    stubFor("Lifecycle", "ValidateHookScript")(...rest),
}));
vi.mock("wailsjs/go/commands/LogCommands", () => ({
  ClearLogs: (...rest: unknown[]) =>
    stubFor("LogCommands", "ClearLogs")(...rest),
  GetLogSettings: (...rest: unknown[]) =>
    stubFor("LogCommands", "GetLogSettings")(...rest),
  ListLogFiles: (...rest: unknown[]) =>
    stubFor("LogCommands", "ListLogFiles")(...rest),
  OpenLogDirectory: (...rest: unknown[]) =>
    stubFor("LogCommands", "OpenLogDirectory")(...rest),
  SetLogLevel: (...rest: unknown[]) =>
    stubFor("LogCommands", "SetLogLevel")(...rest),
  SetRedactFields: (...rest: unknown[]) =>
    stubFor("LogCommands", "SetRedactFields")(...rest),
  TailLogs: (...rest: unknown[]) => stubFor("LogCommands", "TailLogs")(...rest),
}));
vi.mock("wailsjs/go/commands/ModCommands", () => ({
  CancelUpdate: (...rest: unknown[]) =>
    stubFor("ModCommands", "CancelUpdate")(...rest),
  CheckModUpdates: (...rest: unknown[]) =>
    stubFor("ModCommands", "CheckModUpdates")(...rest),
  DetectConflicts: (...rest: unknown[]) =>
    stubFor("ModCommands", "DetectConflicts")(...rest),
  DisableMod: (...rest: unknown[]) =>
    stubFor("ModCommands", "DisableMod")(...rest),
  EnableMod: (...rest: unknown[]) =>
    stubFor("ModCommands", "EnableMod")(...rest),
  GetMod: (...rest: unknown[]) => stubFor("ModCommands", "GetMod")(...rest),
  GetModHooks: (...rest: unknown[]) =>
    stubFor("ModCommands", "GetModHooks")(...rest),
  ImportLink: (...rest: unknown[]) =>
    stubFor("ModCommands", "ImportLink")(...rest),
  InstallMod: (...rest: unknown[]) =>
    stubFor("ModCommands", "InstallMod")(...rest),
  ListMods: (...rest: unknown[]) => stubFor("ModCommands", "ListMods")(...rest),
  LoadOrder: (...rest: unknown[]) =>
    stubFor("ModCommands", "LoadOrder")(...rest),
  ModPath: (...rest: unknown[]) => stubFor("ModCommands", "ModPath")(...rest),
  RefreshMods: (...rest: unknown[]) =>
    stubFor("ModCommands", "RefreshMods")(...rest),
  RelinkMod: (...rest: unknown[]) =>
    stubFor("ModCommands", "RelinkMod")(...rest),
  ReorderMods: (...rest: unknown[]) =>
    stubFor("ModCommands", "ReorderMods")(...rest),
  ResolveConflict: (...rest: unknown[]) =>
    stubFor("ModCommands", "ResolveConflict")(...rest),
  ShareLinkForMod: (...rest: unknown[]) =>
    stubFor("ModCommands", "ShareLinkForMod")(...rest),
  SymlinkStatus: (...rest: unknown[]) =>
    stubFor("ModCommands", "SymlinkStatus")(...rest),
  UninstallModWithOptions: (...rest: unknown[]) =>
    stubFor("ModCommands", "UninstallModWithOptions")(...rest),
  UpdateMod: (...rest: unknown[]) =>
    stubFor("ModCommands", "UpdateMod")(...rest),
  UpdateModHooks: (...rest: unknown[]) =>
    stubFor("ModCommands", "UpdateModHooks")(...rest),
}));
vi.mock("wailsjs/go/commands/P2PShareCommands", () => ({
  AcceptShare: (...rest: unknown[]) =>
    stubFor("P2PShareCommands", "AcceptShare")(...rest),
  AwaitRejectedShare: (...rest: unknown[]) =>
    stubFor("P2PShareCommands", "AwaitRejectedShare")(...rest),
  AwaitShare: (...rest: unknown[]) =>
    stubFor("P2PShareCommands", "AwaitShare")(...rest),
  CancelShare: (...rest: unknown[]) =>
    stubFor("P2PShareCommands", "CancelShare")(...rest),
  CreateShare: (...rest: unknown[]) =>
    stubFor("P2PShareCommands", "CreateShare")(...rest),
  DefaultSTUNServers: (...rest: unknown[]) =>
    stubFor("P2PShareCommands", "DefaultSTUNServers")(...rest),
  DiscardShare: (...rest: unknown[]) =>
    stubFor("P2PShareCommands", "DiscardShare")(...rest),
  GetShareSession: (...rest: unknown[]) =>
    stubFor("P2PShareCommands", "GetShareSession")(...rest),
  InstallShare: (...rest: unknown[]) =>
    stubFor("P2PShareCommands", "InstallShare")(...rest),
  ListShareSessions: (...rest: unknown[]) =>
    stubFor("P2PShareCommands", "ListShareSessions")(...rest),
  PreviewShare: (...rest: unknown[]) =>
    stubFor("P2PShareCommands", "PreviewShare")(...rest),
  RejectShare: (...rest: unknown[]) =>
    stubFor("P2PShareCommands", "RejectShare")(...rest),
  SendShare: (...rest: unknown[]) =>
    stubFor("P2PShareCommands", "SendShare")(...rest),
}));
vi.mock("wailsjs/go/commands/PathCommands", () => ({
  ChooseDirectory: (...rest: unknown[]) =>
    stubFor("PathCommands", "ChooseDirectory")(...rest),
  ChooseFile: (...rest: unknown[]) =>
    stubFor("PathCommands", "ChooseFile")(...rest),
  RevealDirectory: (...rest: unknown[]) =>
    stubFor("PathCommands", "RevealDirectory")(...rest),
}));
vi.mock("wailsjs/go/commands/SourceCommands", () => ({
  AuthenticateSource: (...rest: unknown[]) =>
    stubFor("SourceCommands", "AuthenticateSource")(...rest),
  ClearSourceAPIKey: (...rest: unknown[]) =>
    stubFor("SourceCommands", "ClearSourceAPIKey")(...rest),
  GetSourceMod: (...rest: unknown[]) =>
    stubFor("SourceCommands", "GetSourceMod")(...rest),
  InspectDroppedPath: (...rest: unknown[]) =>
    stubFor("SourceCommands", "InspectDroppedPath")(...rest),
  ListSources: (...rest: unknown[]) =>
    stubFor("SourceCommands", "ListSources")(...rest),
  SearchAll: (...rest: unknown[]) =>
    stubFor("SourceCommands", "SearchAll")(...rest),
  SearchSource: (...rest: unknown[]) =>
    stubFor("SourceCommands", "SearchSource")(...rest),
  SetSourceAPIKey: (...rest: unknown[]) =>
    stubFor("SourceCommands", "SetSourceAPIKey")(...rest),
  SetSourceAuthenticated: (...rest: unknown[]) =>
    stubFor("SourceCommands", "SetSourceAuthenticated")(...rest),
  SourceInfo: (...rest: unknown[]) =>
    stubFor("SourceCommands", "SourceInfo")(...rest),
  TestSourceConnection: (...rest: unknown[]) =>
    stubFor("SourceCommands", "TestSourceConnection")(...rest),
}));
vi.mock("wailsjs/go/commands/SourceLinkCommands", () => ({
  LinkTargets: (...rest: unknown[]) =>
    stubFor("SourceLinkCommands", "LinkTargets")(...rest),
  ModSourceLink: (...rest: unknown[]) =>
    stubFor("SourceLinkCommands", "ModSourceLink")(...rest),
  ModSourceLinks: (...rest: unknown[]) =>
    stubFor("SourceLinkCommands", "ModSourceLinks")(...rest),
  OpenModSource: (...rest: unknown[]) =>
    stubFor("SourceLinkCommands", "OpenModSource")(...rest),
  WriteModManifest: (...rest: unknown[]) =>
    stubFor("SourceLinkCommands", "WriteModManifest")(...rest),
}));
vi.mock("wailsjs/go/models", () => ({
  domain: { ModHooks: { createFrom: (value: unknown) => value } },
}));

/** These five build their own argument (model class, wire query, options
 * literal), so the arity is spelled out. */
const FORWARDED_ARITY: Record<string, number> = {
  ImportLink: 2,
  UninstallModWithOptions: 2,
  UpdateModHooks: 2,
  SearchAll: 1,
  GetModsPath: 1,
};

const emptyHooks = (): ModHooks =>
  Object.fromEntries(
    hookPhases.map((phase) => [phase, []])
  ) as unknown as ModHooks;

const wrappers: Record<string, [string, string, unknown[]]> = {
  listMods: ["ModCommands", "ListMods", []],
  getMod: ["ModCommands", "GetMod", ["mod-1"]],
  installMod: ["ModCommands", "InstallMod", ["nexus", "nexus-1-2"]],
  importLink: ["ModCommands", "ImportLink", ["dmm://p2p/x"]],
  shareLinkForMod: ["ModCommands", "ShareLinkForMod", ["mod-1"]],
  uninstallMod: ["ModCommands", "UninstallModWithOptions", ["mod-1", false]],
  enableMod: ["ModCommands", "EnableMod", ["mod-1"]],
  disableMod: ["ModCommands", "DisableMod", ["mod-1"]],
  checkModUpdates: ["ModCommands", "CheckModUpdates", []],
  updateMod: ["ModCommands", "UpdateMod", ["mod-1"]],
  cancelUpdate: ["ModCommands", "CancelUpdate", ["mod-1"]],
  refreshMods: ["ModCommands", "RefreshMods", []],
  detectConflicts: ["ModCommands", "DetectConflicts", []],
  resolveConflict: [
    "ModCommands",
    "ResolveConflict",
    ["file_overlap:a:b", "keep-both"],
  ],
  reorderMods: ["ModCommands", "ReorderMods", [["a", "b"]]],
  loadOrder: ["ModCommands", "LoadOrder", []],
  modPath: ["ModCommands", "ModPath", ["mod-1"]],
  symlinkStatus: ["ModCommands", "SymlinkStatus", []],
  relinkMod: ["ModCommands", "RelinkMod", ["mod-1"]],
  getModHooks: ["ModCommands", "GetModHooks", ["mod-1"]],
  updateModHooks: ["ModCommands", "UpdateModHooks", ["mod-1", emptyHooks()]],
  listSources: ["SourceCommands", "ListSources", []],
  sourceInfo: ["SourceCommands", "SourceInfo", ["mod-1"]],
  searchSource: ["SourceCommands", "SearchSource", ["nexus", "darktide"]],
  searchAll: ["SourceCommands", "SearchAll", [{ query: "q" }]],
  getSourceMod: ["SourceCommands", "GetSourceMod", ["nexus", "nexus-1-2"]],
  authenticateSource: ["SourceCommands", "AuthenticateSource", ["nexus", {}]],
  setSourceApiKey: ["SourceCommands", "SetSourceAPIKey", ["nexus", "x"]],
  clearSourceApiKey: ["SourceCommands", "ClearSourceAPIKey", ["nexus"]],
  testSourceConnection: ["SourceCommands", "TestSourceConnection", ["nexus"]],
  setSourceAuthenticated: [
    "SourceCommands",
    "SetSourceAuthenticated",
    ["nexus", true],
  ],
  inspectDroppedPath: ["SourceCommands", "InspectDroppedPath", ["/mods"]],
  listGames: ["GameCommands", "SupportedGames", []],
  detectGame: ["GameCommands", "DetectGame", []],
  currentGame: ["GameCommands", "CurrentGame", []],
  validateGamePath: ["GameCommands", "ValidateGamePath", ["/mods"]],
  setGamePath: ["GameCommands", "SetGamePath", ["/mods"]],
  useGame: ["GameCommands", "UseGame", ["mod-1"]],
  getModsPath: ["GameCommands", "GetModsPath", [{} as never]],
  getCacheInfo: ["CacheCommands", "GetCacheInfo", []],
  clearCache: ["CacheCommands", "ClearCache", []],
  pruneCache: ["CacheCommands", "PruneCache", [0, 0]],
  pruneWithDefaults: ["CacheCommands", "PruneWithDefaults", []],
  cacheRoot: ["CacheCommands", "CacheRoot", []],
  getConfig: ["ConfigCommands", "GetConfig", []],
  updateConfig: ["ConfigCommands", "UpdateConfig", [emptyHooks()]],
  getConfigValue: ["ConfigCommands", "GetConfigValue", ["logging.level"]],
  setConfigValue: [
    "ConfigCommands",
    "SetConfigValue",
    ["logging.level", "debug"],
  ],
  configKeys: ["ConfigCommands", "ConfigKeys", []],
  configPath: ["ConfigCommands", "ConfigPath", []],
  getLogSettings: ["LogCommands", "GetLogSettings", []],
  setLogLevel: ["LogCommands", "SetLogLevel", ["info"]],
  tailLogs: ["LogCommands", "TailLogs", [10, "info"]],
  listLogFiles: ["LogCommands", "ListLogFiles", []],
  openLogDirectory: ["LogCommands", "OpenLogDirectory", []],
  clearLogs: ["LogCommands", "ClearLogs", [true]],
  setRedactFields: ["LogCommands", "SetRedactFields", [["token"]]],
  createShare: ["P2PShareCommands", "CreateShare", [["a"], "", "kim", []]],
  previewShare: ["P2PShareCommands", "PreviewShare", ["dmm://p2p/x"]],
  acceptShare: ["P2PShareCommands", "AcceptShare", ["dmm://p2p/x", "sam"]],
  rejectShare: [
    "P2PShareCommands",
    "RejectShare",
    ["dmm://p2p/x", "no thanks"],
  ],
  awaitRejectedShare: ["P2PShareCommands", "AwaitRejectedShare", ["offer-1"]],
  sendShare: ["P2PShareCommands", "SendShare", ["offer-1", "dmm://p2pa/y"]],
  awaitShare: ["P2PShareCommands", "AwaitShare", ["offer-1"]],
  installShare: ["P2PShareCommands", "InstallShare", ["offer-1", true]],
  getShareSession: ["P2PShareCommands", "GetShareSession", ["offer-1"]],
  listShareSessions: ["P2PShareCommands", "ListShareSessions", []],
  cancelShare: ["P2PShareCommands", "CancelShare", ["offer-1"]],
  discardShare: ["P2PShareCommands", "DiscardShare", ["offer-1"]],
  defaultStunServers: ["P2PShareCommands", "DefaultSTUNServers", []],
  listActions: ["ActionCommands", "ListActions", ["darktide"]],
  listAvailableActions: [
    "ActionCommands",
    "ListAvailableActions",
    ["darktide", ""],
  ],
  getGameAction: ["ActionCommands", "GetGameAction", ["mod-1"]],
  executeGameAction: ["ActionCommands", "ExecuteGameAction", ["mod-1", {}]],
  actionGameId: ["ActionCommands", "CurrentGameID", []],
  actionPlatform: ["ActionCommands", "Platform", []],
  modSourceLinks: ["SourceLinkCommands", "ModSourceLinks", []],
  modSourceLink: ["SourceLinkCommands", "ModSourceLink", ["nexus-1-2"]],
  openModSource: ["SourceLinkCommands", "OpenModSource", ["nexus-1-2", "auto"]],
  linkTargets: ["SourceLinkCommands", "LinkTargets", []],
  writeModManifest: ["SourceLinkCommands", "WriteModManifest", ["nexus-1-2"]],
  chooseDirectory: ["PathCommands", "ChooseDirectory", ["/start"]],
  chooseFile: ["PathCommands", "ChooseFile", ["/start", ["*.zip"]]],
  revealDirectory: ["PathCommands", "RevealDirectory", ["/mods"]],
  eventTypes: ["Lifecycle", "EventTypes", []],
  hookTypes: ["Lifecycle", "HookTypes", []],
  hookPhaseNames: ["Lifecycle", "HookPhases", []],
  supportsHookType: ["Lifecycle", "SupportsHookType", ["x"]],
  hookTimeoutMs: ["Lifecycle", "HookTimeout", [{}, {}]],
  validateHookScript: ["Lifecycle", "ValidateHookScript", [{}, {}]],
  cancelAll: ["Lifecycle", "CancelAll", []],
  stopped: ["Lifecycle", "Stopped", []],
};

describe("bridge client", () => {
  it("exposes every wrapper the table names", async () => {
    const api = (await import("./client")) as unknown as Record<
      string,
      unknown
    >;

    for (const name of Object.keys(wrappers)) {
      expect(typeof api[name], `${name} is missing from the bridge`).toBe(
        "function"
      );
    }
  });

  for (const [name, [moduleName, fnName, args]] of Object.entries(wrappers)) {
    it(`${name} forwards to ${moduleName}.${fnName}`, async () => {
      const api = (await import("./client")) as unknown as Record<
        string,
        (...rest: unknown[]) => Promise<unknown>
      >;
      const stub = stubFor(moduleName, fnName);
      stub.mockClear();

      await expect(api[name](...args)).rejects.toThrow("wails stub");

      expect(stub).toHaveBeenCalledTimes(1);
      // Extra arguments mean the wrapper invented something Go never receives.
      expect(stub.mock.calls[0].length).toBe(
        FORWARDED_ARITY[fnName] ?? args.length
      );
      args.forEach((value, index) => {
        if (FORWARDED_ARITY[fnName] !== undefined && index === args.length - 1)
          return;
        expect(stub.mock.calls[0][index]).toEqual(value);
      });
    });
  }

  it("turns a Go error into a BridgeError", async () => {
    const api = await import("./client");
    const stub = stubFor("ModCommands", "GetMod");
    stub.mockRejectedValueOnce(
      new Error("something went wrong: MOD_NOT_FOUND")
    );
    stub.mockClear();

    await expect(api.getMod("missing")).rejects.toMatchObject({
      name: "BridgeError",
    });
  });

  it("maps a recognised dropped path result", async () => {
    const api = await import("./client");
    const stub = stubFor("SourceCommands", "InspectDroppedPath");
    stub.mockClear();
    stub.mockResolvedValueOnce({
      mod: {
        source_id: "local",
        mod_id: "betterui",
        name: "Better UI",
        version: "1.2.0",
        author: "",
        path: "/mods/Better UI",
      },
      recognised: true,
    } as never);

    await expect(api.inspectDroppedPath("/mods")).resolves.toEqual({
      mod: {
        sourceId: "local",
        modId: "betterui",
        name: "Better UI",
        version: "1.2.0",
        author: undefined,
        path: "/mods/Better UI",
      },
      recognised: true,
    });
  });
});
