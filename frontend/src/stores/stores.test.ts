import { act } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";

import { useActivityStore } from "@/stores/activity.store";
import {
  COALESCE_WINDOW_MS,
  type NotificationToggleState,
  useNotificationsStore,
} from "@/stores/notifications.store";
import { useUiStore } from "@/stores/ui.store";

describe("activity store", () => {
  beforeEach(() => {
    useActivityStore.setState({
      entries: [],
      logLines: [],
      hookConsole: [],
      lastProgressAt: {},
    });
  });

  it("applies a 10 Hz floor to progress without throttling the other fields", () => {
    // The floor is measured from the epoch, so a burst of events costs at most
    // one store write per 100 ms, including the first after a fresh begin.
    const id = useActivityStore
      .getState()
      .begin({ kind: "install", label: "Installing" });
    const entry = () => useActivityStore.getState().entries[0];

    act(() => {
      useActivityStore.setState({
        lastProgressAt: { [id]: Date.now() - 1000 },
      });
      useActivityStore.getState().setProgress(id, 0.1);
      useActivityStore.getState().setProgress(id, 0.2);
    });

    expect(entry().progress).toBe(0.1);

    act(() => {
      useActivityStore.getState().setDetail(id, "downloading");
    });
    // `detail` carries no progress sampling, so it bypasses the throttle even
    // inside the same millisecond as the last progress write.
    expect(entry().detail).toBe("downloading");

    act(() => {
      useActivityStore.setState({
        lastProgressAt: { [id]: Date.now() - 1000 },
      });
      useActivityStore.getState().setProgress(id, 0.9);
    });
    expect(entry().progress).toBe(0.9);
  });

  it("finds a running entry by kind and mod so a view can show one row", () => {
    let first = "";
    let second = "";

    act(() => {
      first = useActivityStore
        .getState()
        .begin({ kind: "install", label: "A", modId: "mod-1" });
      second = useActivityStore
        .getState()
        .begin({ kind: "install", label: "B", modId: "mod-2" });
      useActivityStore
        .getState()
        .begin({ kind: "update", label: "C", modId: "mod-1" });
    });

    const store = useActivityStore.getState();
    expect(store.find({ kind: "install", modId: "mod-1" })).toBe(first);
    // An unscoped find takes the oldest running match, which is the one the
    // status bar should show before the user narrows it down.
    expect(store.find({ kind: "install" })).toBe(first);
    expect(store.find({ kind: "update", modId: "mod-1" })).not.toBe(second);
  });

  it("stops reporting a finished row so a re-fired event cannot reopen it", () => {
    let id = "";

    act(() => {
      id = useActivityStore.getState().begin({ kind: "install", label: "A" });
      useActivityStore.getState().finish(id);
    });

    expect(
      useActivityStore.getState().find({ kind: "install" })
    ).toBeUndefined();
    expect(useActivityStore.getState().entries[0].state).toBe("done");
  });

  it("bounds the log ring so an hour of tailing cannot grow the DOM for ever", () => {
    act(() => {
      for (let index = 0; index < 5200; index += 1) {
        useActivityStore.getState().appendLog({
          timestamp: new Date().toISOString(),
          level: "info",
          message: `line ${index}`,
        });
      }
    });

    const lines = useActivityStore.getState().logLines;
    expect(lines.length).toBeLessThanOrEqual(5000);
    expect(lines.at(-1)?.message).toBe("line 5199");
  });

  it("keeps records logged while the log view was closed", () => {
    act(() => {
      const store = useActivityStore.getState();
      store.appendLog({
        timestamp: "2026-10-22T03:07:32Z",
        level: "debug",
        message: "hook output",
      });
      store.appendLog({
        timestamp: "2026-10-22T03:07:31Z",
        level: "debug",
        message: "mod enabled",
      });
      store.replaceLogTail([
        {
          timestamp: "2026-10-22T03:07:30Z",
          level: "info",
          message: "mod folder deployed",
        },
        {
          timestamp: "2026-10-22T03:07:31Z",
          level: "debug",
          message: "mod enabled",
        },
      ]);
    });

    expect(
      useActivityStore.getState().logLines.map((line) => line.message)
    ).toEqual(["mod folder deployed", "mod enabled", "hook output"]);
  });

  it("keeps a hook run's start, output and dropped count in order", () => {
    act(() => {
      const store = useActivityStore.getState();
      const base = { modId: "mod-1", phase: "pre_install", type: "lua" };
      store.appendHookConsole({ ...base, kind: "start" });
      store.appendHookConsole({
        ...base,
        kind: "output",
        lines: ["a", "b"],
        dropped: 4,
      });
      store.appendHookConsole({ ...base, kind: "done" });
    });

    const entries = useActivityStore.getState().hookConsole;
    expect(entries.map((entry) => entry.kind)).toEqual([
      "start",
      "output",
      "output",
      "dropped",
      "done",
    ]);
    expect(entries[3].count).toBe(4);
    expect(entries[1].text).toBe("a");
  });
});

describe("notification coalescing", () => {
  beforeEach(() => {
    useNotificationsStore.setState({
      pending: [],
      availability: null,
      authorized: null,
    });
  });

  it("keeps the first message and counts the repeats", () => {
    const first = { title: "3 mods updated", category: "updates" as const };
    expect(useNotificationsStore.getState().coalesce(first)).toBe(false);

    for (let index = 0; index < 2; index += 1) {
      expect(useNotificationsStore.getState().coalesce(first)).toBe(true);
    }

    const pending = useNotificationsStore.getState().pending;
    expect(pending).toHaveLength(1);
    expect(pending[0]).toMatchObject({
      request: { title: "3 mods updated" },
      count: 3,
    });
  });

  it("does not merge two different messages", () => {
    useNotificationsStore
      .getState()
      .coalesce({ title: "Install failed", category: "install" });
    useNotificationsStore
      .getState()
      .coalesce({ title: "Hook failed", category: "hooks" });

    expect(useNotificationsStore.getState().pending).toHaveLength(2);
  });

  it("releases a message only once the window has passed", () => {
    useNotificationsStore
      .getState()
      .coalesce({ title: "Done", category: "install" });

    expect(useNotificationsStore.getState().takeReady(Date.now())).toHaveLength(
      0
    );
    expect(
      useNotificationsStore
        .getState()
        .takeReady(Date.now() + COALESCE_WINDOW_MS + 1)
    ).toHaveLength(1);
  });
});

describe("ui store", () => {
  it("starts the drawer at Details and honours an explicit tab", () => {
    act(() => {
      useUiStore.getState().openDrawer("mod-1", "hooks");
      useUiStore.getState().openDrawer("mod-2");
    });

    expect(useUiStore.getState().drawerModId).toBe("mod-2");
    // Re-opening without a tab lands on Details, so the tab of a different mod
    // is never inherited by accident.
    expect(useUiStore.getState().drawerTab).toBe("details");
  });

  it("closes the drawer without forgetting which view was open", () => {
    act(() => {
      useUiStore.getState().setView("share");
      useUiStore.getState().openDrawer("mod-1");
      useUiStore.getState().closeDrawer();
    });

    expect(useUiStore.getState().drawerModId).toBeNull();
    expect(useUiStore.getState().view).toBe("share");
  });
});

describe("notification toggles", () => {
  it("defaults the cache category off because a prune is not news", () => {
    const toggles = useNotificationsStore.getState()
      .toggles as NotificationToggleState;
    expect(toggles.cache).toBe(false);
    expect(toggles.install).toBe(true);
  });
});
