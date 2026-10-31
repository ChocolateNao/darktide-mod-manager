import { beforeEach, describe, expect, it } from "vitest";

import { useActionsStore } from "@/stores/actions.store";
import { useConfigStore } from "@/stores/config.store";
import { useGamesStore } from "@/stores/games.store";
import { useSharingStore } from "@/stores/sharing.store";
import { useUiStore } from "@/stores/ui.store";
import type {
  AppConfig,
  GameAction,
  GameInfo,
  GameInstallation,
  P2PShareSession,
} from "@/types/domain";

/** A store is module state, so every test starts from the empty one. */
function resetStores() {
  useActionsStore.getState().close();
  useConfigStore.setState({
    config: null,
    configPath: null,
    loadError: null,
    saveError: null,
  });
  useGamesStore.setState({
    games: [],
    installations: [],
    activeGameId: null,
    invalidGameIds: new Set<string>(),
  });
  useSharingStore.getState().reset();
  useSharingStore.setState({ sessions: [], focus: "send" });
}

const installation = (gameId: string, valid: boolean): GameInstallation => ({
  detectedAt: new Date("2026-10-15T00:00:00Z"),
  path: `/games/${gameId}`,
  source: "steam",
  appId: "1",
  version: "1.0",
  modsPath: `/games/${gameId}/mods`,
  platform: "linux",
  valid,
  gameId,
});

const game = (id: string): GameInfo => ({
  id,
  displayName: id,
  description: "",
  isDefault: false,
  isActive: false,
  installModes: [],
  defaultInstallMethod: "",
});

const session = (
  offerId: string,
  updatedAt: string,
  state = "created"
): P2PShareSession =>
  ({
    offerId,
    updatedAt: new Date(updatedAt),
    createdAt: new Date(updatedAt),
    state,
    role: "sender",
    gameId: "darktide",
    modIds: [],
    totalSize: 0,
    transferredBytes: 0,
  }) as unknown as P2PShareSession;

beforeEach(resetStores);

describe("games store", () => {
  it("derives the invalid set from the installations, not storing it", () => {
    useGamesStore
      .getState()
      .setInstallations([
        installation("darktide", false),
        installation("other", true),
      ]);

    expect([...useGamesStore.getState().invalidGameIds]).toEqual(["darktide"]);
  });

  it("recomputes validity from the installations it currently holds", () => {
    const state = useGamesStore.getState();
    state.setGames([game("darktide")]);
    state.setActiveGameId("darktide");
    state.recomputeValidity();

    expect(useGamesStore.getState().activeGameId).toBe("darktide");
    expect(useGamesStore.getState().invalidGameIds.size).toBe(0);
  });

  it("returns the active game definition and null before one is chosen", () => {
    useGamesStore.getState().setGames([game("darktide")]);
    expect(useGamesStore.getState().activeGameId).toBeNull();

    useGamesStore.getState().setActiveGameId("darktide");
    expect(useGamesStore.getState().games[0].id).toBe("darktide");
  });

  it("reports no active game when the id names a game the registry no longer has", () => {
    useGamesStore.getState().setGames([game("darktide")]);
    useGamesStore.getState().setActiveGameId("removed");

    const state = useGamesStore.getState();
    expect(
      state.games.find((entry) => entry.id === state.activeGameId)
    ).toBeUndefined();
  });
});

describe("config store", () => {
  const config = {
    activeGame: "darktide",
    ui: { compactMode: true },
  } as AppConfig;

  it("clears the load error when a config arrives", () => {
    useConfigStore.getState().setLoadError("boom");
    useConfigStore.getState().setConfig(config);

    expect(useConfigStore.getState().loadError).toBeNull();
    expect(useConfigStore.getState().config?.activeGame).toBe("darktide");
  });

  it("patches the cached copy without a round trip", () => {
    useConfigStore.getState().setConfig(config);
    useConfigStore
      .getState()
      .patchConfig((current) => ({ ...current, activeGame: "other" }));

    expect(useConfigStore.getState().config?.activeGame).toBe("other");
  });

  it("ignores a patch while there is nothing loaded to patch", () => {
    useConfigStore.getState().patchConfig(() => config);
    expect(useConfigStore.getState().config).toBeNull();
  });
});

describe("sharing store", () => {
  it("keeps the newest session first, because that is the row in view", () => {
    useSharingStore
      .getState()
      .setSessions([
        session("old", "2026-10-15T10:00:00Z"),
        session("new", "2026-10-15T12:00:00Z"),
      ]);

    expect(
      useSharingStore.getState().sessions.map((entry) => entry.offerId)
    ).toEqual(["new", "old"]);
  });

  it("upserts, so a state change moves the same row", () => {
    useSharingStore
      .getState()
      .setSessions([session("one", "2026-10-15T10:00:00Z")]);
    useSharingStore
      .getState()
      .upsertSession(session("one", "2026-10-15T11:00:00Z", "transferring"));

    const sessions = useSharingStore.getState().sessions;
    expect(sessions).toHaveLength(1);
    expect(sessions[0].state).toBe("transferring");
  });

  it("removes a session the backend discarded", () => {
    useSharingStore
      .getState()
      .setSessions([session("one", "2026-10-15T10:00:00Z")]);
    useSharingStore.getState().removeSession("one");

    expect(useSharingStore.getState().sessions).toHaveLength(0);
  });

  it("holds the draft links and clears them together on reset", () => {
    useSharingStore.getState().setDraftOfferLink("dmm://p2p/one");
    useSharingStore.getState().setDraftAnswerLink("dmm://p2pa/two");
    useSharingStore.getState().setFocus("receive");

    useSharingStore.getState().reset();

    const state = useSharingStore.getState();
    expect(state.draftOfferLink).toBeNull();
    expect(state.draftAnswerLink).toBeNull();
    // Reset clears the drafts but leaves the user on the half they chose.
    expect(state.focus).toBe("receive");
  });
});

describe("actions store", () => {
  const action = {
    id: "darktide:launch_game",
    displayName: "Launch",
  } as GameAction;

  it("keeps the request open so the palette can close underneath it", () => {
    useActionsStore.getState().open({ action, presetArgs: {} });

    expect(useActionsStore.getState().request?.action.id).toBe(
      "darktide:launch_game"
    );

    useActionsStore.getState().close();
    expect(useActionsStore.getState().request).toBeNull();
  });
});

describe("ui store", () => {
  it("holds the drawer tab that was asked for", () => {
    useUiStore.getState().openDrawer("mod-1", "files");
    expect(useUiStore.getState().drawerModId).toBe("mod-1");
    expect(useUiStore.getState().drawerTab).toBe("files");

    useUiStore.getState().setDrawerTab("hooks");
    expect(useUiStore.getState().drawerTab).toBe("hooks");

    useUiStore.getState().closeDrawer();
    expect(useUiStore.getState().drawerModId).toBeNull();
  });

  it("adopts the config theme, not keeping a second opinion", () => {
    useUiStore.getState().setTheme("light");
    useUiStore.getState().adoptConfig("dark", true);

    const state = useUiStore.getState();
    expect(state.theme).toBe("dark");
    expect(state.compact).toBe(true);
  });

  it("tracks the command palette as open chrome", () => {
    useUiStore.getState().setPaletteOpen(true);
    expect(useUiStore.getState().paletteOpen).toBe(true);
  });
});
