import { describe, expect, it } from "vitest";

import {
  type ActivitySink,
  type ManagerEventsSink,
  type NotifyRequest,
  routeEvent,
} from "./events";

/** Records every intent the router expresses, so tests need no window, query
 * client or sonner to stub. */
function recorder() {
  const calls = {
    began: [] as { kind: string; label: string; modId?: string }[],
    details: [] as { id: string; detail: string }[],
    finished: [] as string[],
    failed: [] as { id: string; reason: string }[],
    changed: [] as string[],
    enabled: [] as { modId: string; enabled: boolean }[],
    notified: [] as NotifyRequest[],
    cached: [] as { kind: string; value: unknown }[],
    logged: [] as string[],
    console: [] as {
      kind: string;
      modId?: string;
      lines?: string[];
      error?: string;
    }[],
    shared: [] as string[],
    unhandled: [] as string[],
  };

  // A live-row registry, so `find` behaves the way the status bar does: a
  // `completed` event updates the row `started` opened.
  let nextId = 0;
  const live = new Map<string, { kind: string; modId?: string }>();
  const activity: ActivitySink = {
    begin: (entry) => {
      calls.began.push(entry);
      nextId += 1;
      live.set(`a${nextId}`, entry);
      return `a${nextId}`;
    },
    setProgress: () => undefined,
    setDetail: (id, detail) => calls.details.push({ id, detail }),
    finish: (id) => {
      calls.finished.push(id);
      live.delete(id);
    },
    fail: (id, reason) => {
      calls.failed.push({ id, reason });
      live.delete(id);
    },
    find: ({ kind, modId }) =>
      [...live.entries()].find(
        ([, entry]) =>
          entry.kind === kind && (modId === undefined || entry.modId === modId)
      )?.[0],
  };

  const sink: ManagerEventsSink = {
    activity,
    changed: (scope) => calls.changed.push(scope),
    modEnabled: (modId, enabled) => calls.enabled.push({ modId, enabled }),
    log: (line) => calls.logged.push(line.message),
    hookConsole: (event) =>
      calls.console.push({
        kind: event.kind,
        modId: event.modId,
        lines: event.lines,
        error: event.error,
      }),
    notify: (request) => calls.notified.push(request),
    cache: (kind, value) => calls.cached.push({ kind, value }),
    share: (session) => calls.shared.push(session.offerId),
    unhandled: (type) => calls.unhandled.push(type),
  };

  return { calls, sink };
}

function envelope(type: string, extra: Record<string, unknown> = {}) {
  return { type, timestamp: "2026-10-16T09:00:00Z", data: undefined, ...extra };
}

const SESSION_WIRE = {
  updated_at: "2026-10-16T09:00:00Z",
  created_at: "2026-10-16T09:00:00Z",
  state: "completed",
  offer_id: "offer-1",
  role: "recipient",
  game_id: "darktide",
  mod_ids: ["a", "b"],
  total_size: 2048,
  transferred_bytes: 2048,
  preview: {
    created_at: "2026-10-16T09:00:00Z",
    offer_id: "offer-1",
    game_id: "darktide",
    sender: "Ada",
    mods: [],
    files: [],
    total_size: 2048,
    expired: false,
  },
};

describe("routeEvent", () => {
  it("reports an unknown event, not pretending to handle it", () => {
    const { calls, sink } = recorder();

    expect(routeEvent(envelope("mod.exploded"), sink)).toBe(false);
    expect(calls.unhandled).toEqual(["mod.exploded"]);
  });

  it("reports a malformed payload by name", () => {
    const { calls, sink } = recorder();

    expect(routeEvent({ nothing: true }, sink)).toBe(false);
    expect(calls.unhandled).toEqual(["malformed"]);
  });

  // One case per row of the notification table: the table is the specification
  // of the funnel.
  const NOTIFY_CASES = [
    { type: "mod.install.completed", category: "install", modId: "coolmod" },
    { type: "mod.install.failed", category: "install", modId: "coolmod" },
    { type: "mod.uninstall.completed", category: "install", modId: "coolmod" },
    { type: "mod.update.completed", category: "updates", modId: "coolmod" },
    { type: "hook.failed", category: "hooks", modId: "coolmod" },
    { type: "cache.pruned", category: "cache" },
    { type: "share.completed", category: "sharing" },
    { type: "share.rejected", category: "sharing" },
    { type: "share.failed", category: "sharing" },
  ] as const;

  for (const entry of NOTIFY_CASES) {
    it(`routes ${entry.type} to the ${entry.category} notification category`, () => {
      const { calls, sink } = recorder();
      const raw = envelope(entry.type, {
        mod_id: "modId" in entry ? entry.modId : undefined,
        data: entry.type.startsWith("share.")
          ? SESSION_WIRE
          : entry.type === "hook.failed"
            ? { phase: "post_install", type: "lua", error: "boom" }
            : { version: "1.2.0", path: "/tmp/coolmod" },
      });

      expect(routeEvent(raw, sink)).toBe(true);
      expect(calls.notified).toHaveLength(1);
      expect(calls.notified[0].category).toBe(entry.category);
      expect(calls.notified[0].title).not.toBe("");
    });
  }

  it("labels a notification with the mod name, not its id", () => {
    const { calls, sink } = recorder();

    routeEvent(
      envelope("mod.install.completed", {
        mod_id: "nexus-900-7001",
        mod_name: "Better UI",
        data: { version: "1.2.0" },
      }),
      sink
    );

    expect(calls.notified[0].title).toBe("Better UI installed");
  });

  it("feeds hook output and failure text to the console", () => {
    const { calls, sink } = recorder();

    routeEvent(
      envelope("hook.output", {
        mod_id: "coolmod",
        data: {
          phase: "post_install",
          type: "lua",
          lines: ["hello", "world"],
          dropped: 2,
        },
      }),
      sink
    );
    routeEvent(
      envelope("hook.failed", {
        mod_id: "coolmod",
        data: { phase: "post_install", type: "lua", error: "boom" },
      }),
      sink
    );

    expect(calls.console).toEqual([
      {
        kind: "output",
        modId: "coolmod",
        lines: ["hello", "world"],
        error: undefined,
      },
      {
        kind: "failed",
        modId: "coolmod",
        lines: undefined,
        error: "boom",
      },
    ]);
  });

  it("keeps a cache download silent and announces a prune", () => {
    const { calls, sink } = recorder();

    routeEvent(envelope("cache.downloaded", { data: "/cache/a.zip" }), sink);
    routeEvent(envelope("cache.pruned", { data: 4096 }), sink);

    expect(calls.notified[0].silent).toBe(true);
    expect(calls.notified[1].silent).toBe(false);
    expect(calls.cached).toEqual([
      { kind: "downloaded", value: "/cache/a.zip" },
      { kind: "pruned", value: 4096 },
    ]);
  });

  it("patches the row on enable/disable instead of refetching the list", () => {
    const { calls, sink } = recorder();

    routeEvent(envelope("mod.enabled", { mod_id: "coolmod" }), sink);
    routeEvent(envelope("mod.disabled", { mod_id: "coolmod" }), sink);

    expect(calls.enabled).toEqual([
      { modId: "coolmod", enabled: true },
      { modId: "coolmod", enabled: false },
    ]);
    expect(calls.changed).toEqual(["conflicts", "conflicts"]);
  });

  it("closes the install row the started event opened", () => {
    const { calls, sink } = recorder();

    routeEvent(
      envelope("mod.install.started", {
        mod_id: "coolmod",
        data: { version: "1.0.0" },
      }),
      sink
    );
    routeEvent(
      envelope("mod.install.completed", {
        mod_id: "coolmod",
        data: { version: "1.0.0" },
      }),
      sink
    );

    expect(calls.began).toHaveLength(1);
    expect(calls.finished).toEqual(["a1"]);
    expect(calls.changed).toEqual(["mods", "conflicts"]);
  });

  it("closes the uninstall row the started event opened", () => {
    const { calls, sink } = recorder();

    routeEvent(
      envelope("mod.uninstall.started", {
        mod_id: "coolmod",
        data: {},
      }),
      sink
    );
    routeEvent(
      envelope("mod.uninstall.completed", {
        mod_id: "coolmod",
        data: {},
      }),
      sink
    );

    expect(calls.began).toHaveLength(1);
    expect(calls.finished).toEqual(["a1"]);
    expect(calls.notified[0].category).toBe("install");
  });

  it("fails the install row when the install fails", () => {
    const { calls, sink } = recorder();

    routeEvent(
      envelope("mod.install.failed", {
        mod_id: "coolmod",
        data: { path: "no such file" },
      }),
      sink
    );

    expect(calls.failed).toHaveLength(1);
    expect(calls.notified[0].body).toBe("no such file");
  });

  it("writes progress into the open row and announces nothing", () => {
    const { calls, sink } = recorder();

    routeEvent(
      envelope("mod.install.started", { mod_id: "coolmod", data: {} }),
      sink
    );
    routeEvent(
      envelope("mod.install.progress", {
        mod_id: "coolmod",
        data: { files: 12 },
      }),
      sink
    );

    expect(calls.began).toHaveLength(1);
    expect(calls.details).toEqual([{ id: "a1", detail: "12 files" }]);
    expect(calls.notified).toHaveLength(0);
  });

  it("stores a log line only when it parses", () => {
    const { calls, sink } = recorder();
    const line = {
      timestamp: "2026-10-16T09:00:00Z",
      level: "error",
      message: "could not read config",
    };

    expect(routeEvent(envelope("log", { data: line }), sink)).toBe(true);
    expect(routeEvent(envelope("log", { data: { nope: true } }), sink)).toBe(
      false
    );
    expect(calls.logged).toEqual(["could not read config"]);
  });

  it("upserts the session on every share transition without announcing the quiet ones", () => {
    const { calls, sink } = recorder();

    // `connecting` and `transferring` are session states: the backend folds
    // them into `share.accepted` and `share.progress`.
    for (const [type, state] of [
      ["share.created", "created"],
      ["share.offered", "offered"],
      ["share.accepted", "connecting"],
      ["share.progress", "transferring"],
    ] as const) {
      routeEvent(envelope(type, { data: { ...SESSION_WIRE, state } }), sink);
    }

    expect(calls.shared).toHaveLength(4);
    expect(calls.notified).toHaveLength(0);
  });

  it("drops a share event whose payload is not a session", () => {
    const { calls, sink } = recorder();

    expect(
      routeEvent(envelope("share.progress", { data: { nonsense: true } }), sink)
    ).toBe(false);
    expect(calls.unhandled).toEqual(["share.progress"]);
  });
});
