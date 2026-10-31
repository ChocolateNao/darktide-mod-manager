import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type * as BridgeClient from "@/bridge/client";
import {
  useReorderMods,
  useSetConfigValue,
  useSetSourceApiKey,
} from "@/hooks/mutations";
import { queryKeys } from "@/lib/queryClient";
import { useModsStore } from "@/stores/mods.store";
import type { Mod } from "@/types/domain";

vi.mock("@/bridge/client", async (importOriginal) => ({
  ...(await importOriginal<typeof BridgeClient>()),
  reorderMods: vi.fn(async () => undefined),
  setConfigValue: vi.fn(async () => undefined),
  setSourceApiKey: vi.fn(async () => undefined),
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
