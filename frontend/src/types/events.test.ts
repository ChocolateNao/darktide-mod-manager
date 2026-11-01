import { describe, expect, it } from "vitest";

import { NOTIFICATION_CATEGORIES } from "@/bridge/notifications";
import { EVENT_TYPES, parseEvent, WEBVIEW_EVENT_NAME } from "@/types/events";

/** The mapping lives in the router, a row that stops firing shows up here. */
describe("event envelope", () => {
  it("names every type the backend publishes, share events included", () => {
    expect(EVENT_TYPES).toContain("share.progress");
    expect(EVENT_TYPES).toContain("share.rejected");
    expect(EVENT_TYPES).toHaveLength(27);
    expect(WEBVIEW_EVENT_NAME).toBe("mod-manager:event");
  });

  it("drops an unknown type instead of dispatching on a guess", () => {
    expect(
      parseEvent({ type: "mod.teleported", timestamp: "2026-10-16T00:00:00Z" })
    ).toBeNull();
  });

  it("drops a payload that is not an envelope at all", () => {
    expect(parseEvent(null)).toBeNull();
    expect(parseEvent("installing")).toBeNull();
    expect(parseEvent({ type: "log", timestamp: 5 })).toBeNull();
  });

  it("passes the fields the router is allowed to switch on", () => {
    const event = parseEvent({
      type: "mod.install.completed",
      timestamp: "2026-10-16T09:00:00Z",
      mod_id: "nexus-900-7001",
      mod_name: "Better UI",
      source_id: "nexus",
      data: { version: "1.2.0" },
    });

    expect(event).toMatchObject({
      type: "mod.install.completed",
      modId: "nexus-900-7001",
      modName: "Better UI",
      sourceId: "nexus",
    });
  });
});

describe("notification categories", () => {
  it("covers every category table maps an event to", () => {
    expect([...NOTIFICATION_CATEGORIES].sort()).toEqual(
      ["cache", "errors", "hooks", "install", "sharing", "updates"].sort()
    );
  });
});
