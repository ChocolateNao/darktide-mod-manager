import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { toast } from "sonner";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type * as BridgeClient from "@/bridge/client";
import {
  useCheckModUpdate,
  useInstallMod,
  useReorderMods,
  useSetConfigValue,
  useSetSourceApiKey,
  useToggleMod,
  useUpdateMod,
} from "@/hooks/mutations";
import { queryKeys } from "@/lib/queryClient";
import { useActivityStore } from "@/stores/activity.store";
import { useInstallPromptStore } from "@/stores/installPrompt.store";
import { useModsStore } from "@/stores/mods.store";
import type { Mod } from "@/types/domain";

vi.mock("sonner", () => ({ toast: { error: vi.fn() } }));

vi.mock("@/bridge/client", async (importOriginal) => ({
  ...(await importOriginal<typeof BridgeClient>()),
  checkModUpdate: vi.fn(),
  enableMod: vi.fn(async () => undefined),
  installMod: vi.fn(),
  openModSource: vi.fn(async () => undefined),
  reorderMods: vi.fn(async () => undefined),
  setConfigValue: vi.fn(async () => undefined),
  setSourceApiKey: vi.fn(async () => undefined),
  updateMod: vi.fn(async () => undefined),
}));

const api = await import("@/bridge/client");

function stubMod(id: string): Mod {
  return { id, name: id.toUpperCase() } as Mod;
}

function seedMods(): void {
  useModsStore.getState().setMods([stubMod("alpha"), stubMod("bravo")]);
  useModsStore.getState().setLoadOrder(["alpha", "bravo"]);
}

/**
 * A fresh client per test, with retries and background refetching off. The
 * default client refetches a whole minute's worth of queries on its own, which
 * is the wrong thing to have running underneath an assertion about which
 * queries a mutation told to re-run.
 */
function makeClient(): QueryClient {
  return new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
}

function wrapperFor(client: QueryClient) {
  interface WrapperProps {
    children: ReactNode;
  }

  return function Wrapper({ children }: WrapperProps) {
    return (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    );
  };
}

/**
 * Records every invalidation a mutation asks for, while still letting React
 * Query do the real work. `invalidateQueries` is the whole contract between a
 * write and the reads it makes stale — there is no other path — so spying on it
 * is how a test can say which reads a write promised to refresh.
 */
function watch(client: QueryClient) {
  const invalidations: unknown[][] = [];
  const original = client.invalidateQueries.bind(client);
  const spy = vi
    .spyOn(client, "invalidateQueries")
    .mockImplementation(async (filters = {}, options) => {
      invalidations.push([
        ...((filters as { queryKey?: unknown[] }).queryKey ?? []),
      ]);
      return original(filters, options);
    });

  return { invalidations, spy };
}

describe("useSetConfigValue", () => {
  let client: QueryClient;

  beforeEach(() => {
    client = makeClient();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("does not recheck updates for a logging key", async () => {
    const { invalidations } = watch(client);

    const { result } = renderHook(() => useSetConfigValue(), {
      wrapper: wrapperFor(client),
    });
    await result.current.mutateAsync({
      key: "logging.level",
      value: "debug",
    });

    expect(invalidations).toContainEqual(queryKeys.config());
    expect(invalidations).toContainEqual(queryKeys.logSettings());
    expect(invalidations).toContainEqual(["log-tail"]);
    expect(invalidations).not.toContainEqual(queryKeys.updates());
  });

  it("rechecks updates when an auto-update setting changes", async () => {
    const { invalidations } = watch(client);

    const { result } = renderHook(() => useSetConfigValue(), {
      wrapper: wrapperFor(client),
    });
    await result.current.mutateAsync({
      key: "auto_update.enabled",
      value: "true",
    });

    expect(invalidations).toContainEqual(queryKeys.updates());
  });
});

describe("useSetSourceApiKey", () => {
  let client: QueryClient;

  beforeEach(() => {
    client = makeClient();
    vi.mocked(api.setSourceApiKey).mockClear();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("hands the key to the backend, not storing it locally", async () => {
    const { result } = renderHook(() => useSetSourceApiKey(), {
      wrapper: wrapperFor(client),
    });

    await result.current.mutateAsync({
      sourceId: "nexus",
      apiKey: "abc",
    });

    expect(api.setSourceApiKey).toHaveBeenCalledWith("nexus", "abc");
  });

  it("refreshes the stored-key and config views", async () => {
    const { invalidations } = watch(client);

    const { result } = renderHook(() => useSetSourceApiKey(), {
      wrapper: wrapperFor(client),
    });
    await result.current.mutateAsync({ sourceId: "nexus", apiKey: "abc" });

    await waitFor(() =>
      expect(invalidations).toContainEqual(queryKeys.sources())
    );
    expect(invalidations).toContainEqual(queryKeys.config());
  });

  // The loop this fixes: a failed AUTH_REQUIRED search stays mounted behind the
  // prompt, so it must be re-run once the credential lands.
  it("re-runs searches so a failed one cannot resurrect the prompt", async () => {
    const { invalidations, spy } = watch(client);

    const { result } = renderHook(() => useSetSourceApiKey(), {
      wrapper: wrapperFor(client),
    });
    await result.current.mutateAsync({ sourceId: "nexus", apiKey: "abc" });

    // Keys carry the search text, so invalidating one query would leave the rest
    // stale — match the prefix instead.
    expect(spy).toHaveBeenCalledWith({ queryKey: ["search"] });
    expect(invalidations).toContainEqual(["search"]);
  });

  it("reports a failure instead of leaving the key looking saved", async () => {
    const failure = new Error("bad key");
    vi.mocked(api.setSourceApiKey).mockRejectedValueOnce(failure);

    const { invalidations, spy } = watch(client);
    const { result } = renderHook(() => useSetSourceApiKey(), {
      wrapper: wrapperFor(client),
    });

    await expect(
      result.current.mutateAsync({ sourceId: "nexus", apiKey: "nope" })
    ).rejects.toBe(failure);

    // Nothing to invalidate — a key the backend refused changed nothing, and
    // invalidating anyway would re-run the search that is about to fail again.
    expect(spy).not.toHaveBeenCalled();
    expect(invalidations).toEqual([]);
  });
});

describe("useReorderMods", () => {
  const client = makeClient();

  it("moves the rows before the write lands", async () => {
    seedMods();

    const { result } = renderHook(() => useReorderMods(), {
      wrapper: wrapperFor(client),
    });

    await act(() => result.current.mutateAsync(["bravo", "alpha"]));

    expect(useModsStore.getState().loadOrder).toEqual(["bravo", "alpha"]);
    expect(useModsStore.getState().mods.map((mod) => mod.id)).toEqual([
      "bravo",
      "alpha",
    ]);
  });

  it("refetches the real order when the write is refused", async () => {
    // The optimistic order only ever lived in the store — leaving it would make
    // the next drag compute from an order the backend never stored.
    const failure = new Error("no");
    vi.mocked(api.reorderMods).mockRejectedValueOnce(failure);
    seedMods();

    const { invalidations } = watch(client);
    const { result } = renderHook(() => useReorderMods(), {
      wrapper: wrapperFor(client),
    });

    await expect(result.current.mutateAsync(["bravo", "alpha"])).rejects.toBe(
      failure
    );

    expect(invalidations).toContainEqual(["mods"]);
    expect(invalidations).toContainEqual(["load-order"]);
  });
});

describe("useInstallMod", () => {
  let client: QueryClient;

  beforeEach(() => {
    client = makeClient();
    useInstallPromptStore.getState().clear();
    useModsStore
      .getState()
      .setMods([{ ...stubMod("alpha"), version: "1.0.0", sourceId: "nexus" }]);
    vi.mocked(api.checkModUpdate).mockReset();
    vi.mocked(api.installMod).mockReset();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("asks before replacing an installed mod with a newer version", async () => {
    vi.mocked(api.checkModUpdate).mockResolvedValue({
      modId: "alpha",
      localVersion: "1.0.0",
      remoteVersion: "2.0.0",
      sourceId: "nexus",
      available: true,
    });

    const { result } = renderHook(() => useInstallMod(), {
      wrapper: wrapperFor(client),
    });

    await act(async () => {
      await result.current
        .mutateAsync({ sourceId: "nexus", modId: "alpha" })
        .catch(() => undefined);
    });

    expect(api.installMod).not.toHaveBeenCalled();
    expect(useInstallPromptStore.getState().pending).toEqual({
      mode: "replace",
      sourceId: "nexus",
      modId: "alpha",
      name: "ALPHA",
      currentVersion: "1.0.0",
      targetVersion: "2.0.0",
      direction: "update",
    });
  });

  it("names an older remote version a downgrade", async () => {
    vi.mocked(api.checkModUpdate).mockResolvedValue({
      modId: "alpha",
      localVersion: "1.0.0",
      remoteVersion: "0.9.0",
      sourceId: "nexus",
      available: false,
      remoteOlder: true,
    });

    const { result } = renderHook(() => useInstallMod(), {
      wrapper: wrapperFor(client),
    });

    await act(async () => {
      await result.current
        .mutateAsync({ sourceId: "nexus", modId: "alpha" })
        .catch(() => undefined);
    });

    expect(useInstallPromptStore.getState().pending?.direction).toBe(
      "downgrade"
    );
  });

  it("treats an equal version as a reinstall instead of a downgrade", async () => {
    vi.mocked(api.checkModUpdate).mockResolvedValue({
      modId: "alpha",
      localVersion: "1.0.0",
      remoteVersion: "1.0",
      sourceId: "nexus",
      available: false,
    });

    const { result } = renderHook(() => useInstallMod(), {
      wrapper: wrapperFor(client),
    });

    await act(async () => {
      await result.current
        .mutateAsync({ sourceId: "nexus", modId: "alpha" })
        .catch(() => undefined);
    });

    expect(useInstallPromptStore.getState().pending?.direction).toBe(
      "reinstall"
    );
  });

  it("installs with replacement once the decision is forced", async () => {
    vi.mocked(api.installMod).mockResolvedValue({
      ...stubMod("alpha"),
      dependencies: [],
      sourceData: {},
    } as Awaited<ReturnType<typeof api.installMod>>);

    const { result } = renderHook(() => useInstallMod(), {
      wrapper: wrapperFor(client),
    });

    await act(async () => {
      await result.current.mutateAsync({
        sourceId: "nexus",
        modId: "alpha",
        force: true,
      });
    });

    expect(api.checkModUpdate).not.toHaveBeenCalled();
    expect(api.installMod).toHaveBeenCalledWith("nexus", "alpha", true);
  });

  it("asks before installing another file of an installed mod page", async () => {
    useModsStore.getState().setMods([
      {
        ...stubMod("nexus-8-7495"),
        version: "1.3.2",
        sourceId: "nexus",
        sourceData: { mod_id: "8" },
      },
    ]);
    vi.mocked(api.checkModUpdate).mockResolvedValue({
      modId: "nexus-8-7495",
      localVersion: "1.3.2",
      remoteVersion: "26.10.22",
      sourceId: "nexus",
      available: true,
    });

    const { result } = renderHook(() => useInstallMod(), {
      wrapper: wrapperFor(client),
    });

    await act(async () => {
      await result.current
        .mutateAsync({ sourceId: "nexus", modId: "nexus-8-191620" })
        .catch(() => undefined);
    });

    expect(api.installMod).not.toHaveBeenCalled();
    expect(api.checkModUpdate).toHaveBeenCalledWith("nexus-8-7495");
    expect(useInstallPromptStore.getState().pending).toEqual(
      expect.objectContaining({
        mode: "replace",
        modId: "nexus-8-191620",
        currentVersion: "1.3.2",
        targetVersion: "26.10.22",
      })
    );
  });
});

describe("useUpdateMod", () => {
  beforeEach(() => {
    useActivityStore.setState({ entries: [] });
    vi.mocked(toast.error).mockClear();
    vi.mocked(api.updateMod).mockReset();
    vi.mocked(api.updateMod).mockResolvedValue(null);
    vi.mocked(api.openModSource).mockClear();
  });

  it("fails the running row and offers the mod page when the download is refused", async () => {
    const failure = "DOWNLOAD_FAILED: premium required";
    vi.mocked(api.updateMod).mockRejectedValueOnce(failure);

    const rowId = useActivityStore
      .getState()
      .begin({ kind: "update", label: "Alpha", modId: "nexus-8-7495" });

    const client = makeClient();
    const { result } = renderHook(() => useUpdateMod(), {
      wrapper: wrapperFor(client),
    });

    await act(async () => {
      await result.current
        .mutateAsync({ id: "nexus-8-7495" })
        .catch(() => undefined);
    });

    expect(
      useActivityStore.getState().entries.find((entry) => entry.id === rowId)
        ?.state
    ).toBe("failed");

    const [, options] = vi.mocked(toast.error).mock.calls[0];
    const action = options?.action;
    expect(action).toBeDefined();

    if (typeof action === "object" && action !== null && "onClick" in action) {
      action.onClick(new MouseEvent("click") as never);
    } else {
      throw new Error("expected the failure toast to offer an action");
    }

    expect(api.openModSource).toHaveBeenCalledWith("nexus-8-7495", "source");
  });

  it("leaves the failure to the caller when quiet", async () => {
    const failure = "DOWNLOAD_FAILED: premium required";
    vi.mocked(api.updateMod).mockRejectedValueOnce(failure);

    const rowId = useActivityStore
      .getState()
      .begin({ kind: "update", label: "Alpha", modId: "nexus-8-7495" });

    const client = makeClient();
    const { result } = renderHook(() => useUpdateMod(), {
      wrapper: wrapperFor(client),
    });

    await act(async () => {
      await result.current
        .mutateAsync({ id: "nexus-8-7495", quiet: true })
        .catch(() => undefined);
    });

    expect(
      useActivityStore.getState().entries.find((entry) => entry.id === rowId)
        ?.state
    ).toBe("failed");
    expect(toast.error).not.toHaveBeenCalled();
  });
});

describe("useCheckModUpdate", () => {
  beforeEach(() => {
    useModsStore.getState().setUpdates([]);
  });

  it("stores the single checked mod's update", async () => {
    const client = makeClient();
    vi.mocked(api.checkModUpdate).mockResolvedValue({
      modId: "alpha",
      localVersion: "1.0.0",
      remoteVersion: "1.1.0",
      sourceId: "nexus",
      available: true,
    });

    const { result } = renderHook(() => useCheckModUpdate(), {
      wrapper: wrapperFor(client),
    });

    await act(async () => {
      await result.current.mutateAsync("alpha");
    });

    expect(useModsStore.getState().updates).toEqual([
      expect.objectContaining({ modId: "alpha", remoteVersion: "1.1.0" }),
    ]);
  });
});

describe("useToggleMod", () => {
  beforeEach(() => {
    seedMods();
    useModsStore.getState().patchMod("alpha", { enabled: false });
    vi.mocked(toast.error).mockClear();
    vi.mocked(api.enableMod).mockReset();
  });

  it("rolls the row back and explains a blocked enable", async () => {
    vi.mocked(api.enableMod).mockRejectedValueOnce(
      new Error(
        'MOD_BLOCKED: mod "alpha" cannot be enabled: alpha requires ghost, which is not installed'
      )
    );

    const client = makeClient();
    const { result } = renderHook(() => useToggleMod(), {
      wrapper: wrapperFor(client),
    });

    await act(async () => {
      await result.current
        .mutateAsync({ id: "alpha", enable: true })
        .catch(() => undefined);
    });

    expect(
      useModsStore.getState().mods.find((mod) => mod.id === "alpha")?.enabled
    ).toBe(false);

    const [title, options] = vi.mocked(toast.error).mock.calls[0];
    expect(title).toBe(
      "This mod cannot be enabled: a blocking issue is unresolved"
    );
    expect(options?.description).toContain("requires ghost");
  });
});
