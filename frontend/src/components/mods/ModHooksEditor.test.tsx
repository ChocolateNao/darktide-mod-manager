import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type * as BridgeClient from "@/bridge/client";
import { HookList, ModHooksEditor } from "@/components/mods/ModHooksEditor";
import { t } from "@/lib/i18n";
import { hookPhases } from "@/lib/schemas/mod";
import { useActivityStore } from "@/stores/activity.store";
import { useModsStore } from "@/stores/mods.store";
import type { HookPhase, HookScript, Mod, ModHooks } from "@/types/domain";

vi.mock("@/hooks/queries", () => ({
  useModHooksQuery: (modId: string) => ({
    data: hooksFor(modId),
    isLoading: false,
  }),
  useLifecycleVocabulary: () => ({ phases: { data: hookPhases } }),
}));

vi.mock("@/hooks/mutations", () => ({
  useSaveModHooks: () => ({ mutateAsync: vi.fn(async () => undefined) }),
}));

vi.mock("@/bridge/client", async (importOriginal) => ({
  ...(await importOriginal<typeof BridgeClient>()),
  validateHookScript: vi.fn(async () => undefined),
  runHookScript: vi.fn(async () => ({ output: "ok", error: "" })),
}));

const api = await import("@/bridge/client");

/* jsdom has no layout, so a Range has no client rects and CodeMirror throws
 * while measuring — nothing below it in the dialog would ever mount. */
for (const method of ["getClientRects", "getBoundingClientRect"] as const) {
  if (!(method in Range.prototype)) {
    Object.defineProperty(Range.prototype, method, {
      value: () => [],
      configurable: true,
    });
  }
}

const script = (overrides: Partial<HookScript> = {}): HookScript => ({
  language: "lua",
  content: "print('hi')",
  name: "greet",
  ...overrides,
});

function hooks(entries: Partial<Record<HookPhase, HookScript[]>>): ModHooks {
  return Object.fromEntries(
    hookPhases.map((phase) => [phase, entries[phase] ?? []])
  ) as unknown as ModHooks;
}

const fixture = hooks({ pre_install: [script()] });

function hooksFor(modId: string): ModHooks {
  return modId === "alpha" ? fixture : hooks({});
}

function mod(id: string): Mod {
  return {
    id,
    name: id.toUpperCase(),
    version: "1.0.0",
    author: "",
    description: "",
    sourceId: "local",
    sourceData: {},
    localPath: `/mods/${id}`,
    installedAt: null,
    updatedAt: null,
    enabled: true,
    external: false,
    priority: 0,
    tags: [],
    issues: [],
    dependencies: [],
    conflictsWith: [],
    hooks: hooks({}),
  } as Mod;
}

describe("HookList", () => {
  const noop = (): void => {};

  function renderList(overrides: Partial<Parameters<typeof HookList>[0]> = {}) {
    return render(
      <HookList
        modId="alpha"
        hooks={fixture}
        stages={hookPhases as unknown as HookPhase[]}
        onSelect={noop}
        selected={{ phase: null, index: null }}
        onToggle={noop}
        onDelete={noop}
        {...overrides}
      />
    );
  }

  it("gives pause the same ghost treatment as the row's other controls", () => {
    renderList();

    const row = screen.getByRole("listitem");
    const pause = within(row).getByRole("button", { name: t("hooks.pause") });
    const run = within(row).getByRole("button", {
      name: /run/i,
    });
    const remove = within(row).getByRole("button", { name: /delete/i });

    for (const button of [pause, run, remove]) {
      expect(button).toHaveAttribute("data-variant", "ghost");
      expect(button).toHaveAttribute("data-size", "sm");
    }
  });

  it("keeps the row's button widths equal whatever the state", () => {
    const { rerender } = renderList();

    const widths = () =>
      Array.from(screen.getByRole("listitem").querySelectorAll("button")).map(
        (button) => button.getAttribute("data-size")
      );

    const active = widths();
    rerender(
      <HookList
        modId="alpha"
        hooks={hooks({ pre_install: [script({ disabled: true })] })}
        stages={hookPhases as unknown as HookPhase[]}
        onSelect={noop}
        selected={{ phase: null, index: null }}
        onToggle={noop}
        onDelete={noop}
      />
    );

    expect(widths()).toEqual(active);
    expect(
      screen.getByRole("button", { name: t("hooks.resume") })
    ).toHaveAttribute("data-variant", "ghost");
  });

  it("says which of the two states the pause control is in", () => {
    const { rerender } = renderList();

    expect(screen.getByRole("button", { name: t("hooks.pause") })).toBeTruthy();

    rerender(
      <HookList
        modId="alpha"
        hooks={hooks({ pre_install: [script({ disabled: true })] })}
        stages={hookPhases as unknown as HookPhase[]}
        onSelect={noop}
        selected={{ phase: null, index: null }}
        onToggle={noop}
        onDelete={noop}
      />
    );

    expect(
      screen.getByRole("button", { name: t("hooks.resume") })
    ).toBeTruthy();
  });

  it("asks to pause a script that is running", async () => {
    const onToggle = vi.fn();
    renderList({ onToggle });

    await userEvent
      .setup()
      .click(screen.getByRole("button", { name: t("hooks.pause") }));

    expect(onToggle).toHaveBeenCalledWith("pre_install", 0);
  });
});

describe("ModHooksEditor", () => {
  beforeEach(() => {
    useModsStore.getState().setMods([mod("alpha")]);
    useActivityStore.setState({ hookConsole: [] });
  });

  /** Opens the editor on the fixture's only script and returns the document the
   * dialog lives in: it is portalled, so the verdict lives outside `render`'s container. */
  async function openEditor(): Promise<Document> {
    const user = userEvent.setup();
    render(<ModHooksEditor modId="alpha" />);

    await user.click(
      await screen.findByRole("button", { name: t("hooks.edit") })
    );

    return document;
  }

  it("opens the console with the editor", async () => {
    await openEditor();

    expect(screen.getByText(t("hooks.consoleEmpty"))).toBeInTheDocument();
  });

  it("prints what the script writes", async () => {
    const user = userEvent.setup();
    await openEditor();

    await user.click(screen.getByRole("button", { name: /run now/i }));
    useActivityStore.getState().appendHookConsole({
      kind: "output",
      modId: "alpha",
      phase: "pre_install",
      type: "lua",
      lines: ["hello from lua"],
    });

    expect(await screen.findByText("hello from lua")).toBeInTheDocument();
  });

  it("keeps a validation error whole in the console", async () => {
    const user = userEvent.setup();
    const reason =
      `a very long explanation ${"with detail ".repeat(20)}`.trim();
    vi.mocked(api.validateHookScript).mockRejectedValueOnce(new Error(reason));

    await openEditor();
    await user.click(screen.getByRole("button", { name: t("hooks.validate") }));

    const message = await screen.findByText(
      /Validation failed: a very long explanation/
    );
    expect(message).toHaveTextContent(reason);
    expect(message.className).not.toContain("truncate");
  });

  it("closes and reopens the console from its own button", async () => {
    const user = userEvent.setup();

    await openEditor();
    await user.click(screen.getByRole("button", { name: t("hooks.console") }));

    expect(screen.queryByText(t("hooks.consoleEmpty"))).toBeNull();

    await user.click(screen.getByRole("button", { name: t("hooks.console") }));

    expect(screen.getByText(t("hooks.consoleEmpty"))).toBeInTheDocument();
  });

  it("shows the run in the button and leaves the label alone", async () => {
    const user = userEvent.setup();
    let release: () => void = () => {};
    vi.mocked(api.runHookScript).mockReturnValue(
      new Promise<null>((resolve) => {
        release = () => resolve(null);
      })
    );

    await openEditor();
    const button = screen.getByRole("button", { name: /run now/i });
    await user.click(button);

    await waitFor(() => expect(button).toBeDisabled());
    expect(button).toHaveTextContent(t("hooks.run"));

    release();
  });
});
