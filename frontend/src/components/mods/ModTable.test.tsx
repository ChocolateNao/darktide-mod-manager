import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ModTable } from "@/components/mods/ModTable";
import { TooltipProvider } from "@/components/ui/tooltip";
import { t } from "@/lib/i18n";
import { hookPhases } from "@/lib/schemas/mod";
import { useModsStore } from "@/stores/mods.store";
import type { Mod, ModHooks } from "@/types/domain";

/* Every mutation the table reaches for is stubbed: what the table shows comes
 * from the store it reads. */
const reorderMutate = vi.hoisted(() => vi.fn());
const bulkToggleMutate = vi.hoisted(() => vi.fn());
const bulkUninstallMutate = vi.hoisted(() => vi.fn());

vi.mock("@/hooks/mutations", () => ({
  useBulkToggleMods: () => ({ mutate: bulkToggleMutate }),
  useBulkUninstallMods: () => ({ mutate: bulkUninstallMutate }),
  useCancelUpdate: () => ({ isPending: false, mutate: vi.fn() }),
  useRelinkMod: () => ({ isPending: false, mutate: vi.fn() }),
  useReorderMods: () => ({ mutate: reorderMutate }),
  useToggleMod: () => ({ isPending: false, mutate: vi.fn() }),
  useUninstallMod: () => ({ isPending: false, mutate: vi.fn() }),
  useUpdateMod: () => ({ isPending: false, mutate: vi.fn() }),
}));

vi.mock("@/hooks/useModActions", () => ({
  useGameActionsForMod: () => ({ actions: [], run: vi.fn() }),
}));

function mod(id: string, overrides: Partial<Mod> = {}): Mod {
  const base: Mod = {
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
    hooks: Object.fromEntries(
      hookPhases.map((phase) => [phase, []])
    ) as unknown as ModHooks,
  };

  return { ...base, ...overrides } as Mod;
}

function seed(mods: Mod[], loadOrder: string[]): void {
  useModsStore.getState().setMods(mods);
  useModsStore.getState().setLoadOrder(loadOrder);
}

/** The provider lives in `main.tsx`; tests mount the table on its own. */
function renderTable() {
  return render(
    <TooltipProvider>
      <ModTable />
    </TooltipProvider>
  );
}

/** The row's cells in order; naming the one under test beats counting. */
const PRIORITY = 6;

function rowFor(name: string): HTMLElement {
  const cell = screen.getByText(name).closest("tr");
  if (!cell) throw new Error(`no row for ${name}`);

  return cell as HTMLElement;
}

function handleFor(name: string): HTMLElement {
  return within(rowFor(name)).getByRole("button", {
    name: t("mods.reorder"),
  });
}

describe("ModTable load order", () => {
  beforeEach(() => {
    useModsStore.getState().clearSelection();
    useModsStore.getState().setUpdates([]);
    seed([], []);
  });

  /* `Mod.Priority` is a per-mod integer nothing writes; reordering rewrites
   * `mod_load_order.txt`. */
  it("numbers rows by where the load order puts them", () => {
    seed([mod("alpha"), mod("bravo")], ["bravo", "alpha"]);

    renderTable();

    const bravoCells = within(rowFor("BRAVO")).getAllByRole("cell");
    const alphaCells = within(rowFor("ALPHA")).getAllByRole("cell");

    expect(bravoCells[PRIORITY]).toHaveTextContent("1");
    expect(alphaCells[PRIORITY]).toHaveTextContent("2");
  });

  it("fixes column widths so a changing status badge cannot resize the table", () => {
    seed([mod("alpha")], ["alpha"]);

    const { container } = renderTable();
    const headers = container.querySelectorAll("thead th");

    expect(container.querySelector("table")).toHaveClass("table-fixed");
    expect(headers[3]).toHaveClass("w-24");
    expect(headers[4]).toHaveClass("w-32");
    expect(headers[5]).toHaveClass("w-24");
    expect(headers[6]).toHaveClass("w-16");
  });

  it("marks a mod whose payload has issues", () => {
    seed([mod("alpha", { issues: ["multiple_manifests"] })], ["alpha"]);

    renderTable();

    expect(
      within(rowFor("ALPHA")).getByRole("img", { name: t("mods.issues") })
    ).toBeInTheDocument();
  });

  it("shows an update icon instead of a text badge", () => {
    seed([mod("alpha")], ["alpha"]);
    useModsStore.getState().setUpdates([
      {
        modId: "alpha",
        localVersion: "1.0.0",
        remoteVersion: "2.0.0",
        sourceId: "local",
        available: true,
      },
    ]);

    renderTable();

    expect(
      within(rowFor("ALPHA")).getByRole("img", {
        name: t("mods.updateAvailable"),
      })
    ).toBeInTheDocument();
    expect(
      within(rowFor("ALPHA")).queryByText(t("mods.updateAvailable"))
    ).not.toBeInTheDocument();
  });

  it("shortens the external disabled badge and keeps the full text as a tooltip", () => {
    seed([mod("alpha", { external: true, enabled: false })], ["alpha"]);

    renderTable();

    const badge = within(rowFor("ALPHA")).getByText(t("mods.externalDisabled"));
    expect(badge).toHaveTextContent("Disabled");
    expect(badge).toHaveAttribute("title", t("mods.externalDisabledHint"));
  });

  it("numbers a mod the load order never named after the ones it did", () => {
    seed([mod("alpha"), mod("bravo"), mod("charlie")], ["charlie"]);

    renderTable();

    expect(
      within(rowFor("CHARLIE")).getAllByRole("cell")[PRIORITY]
    ).toHaveTextContent("1");
    expect(
      within(rowFor("ALPHA")).getAllByRole("cell")[PRIORITY]
    ).toHaveTextContent("2");
    expect(
      within(rowFor("BRAVO")).getAllByRole("cell")[PRIORITY]
    ).toHaveTextContent("3");
  });

  /* The backend appends every unlisted mod to `mod_load_order.txt`, so an empty
   * load order still gives each mod a real place. */
  it("numbers a mod the load order has never seen", () => {
    seed([mod("alpha")], []);

    renderTable();

    const cells = within(rowFor("ALPHA")).getAllByRole("cell");
    expect(cells[PRIORITY]).toHaveTextContent("1");
    expect(cells[PRIORITY]).toHaveAttribute("title", t("mods.priorityHint"));
  });

  /* A 24px button in the middle of the cell is too small a drag target. */
  it("makes the whole leading cell the drag handle", () => {
    seed([mod("alpha")], ["alpha"]);

    renderTable();

    const handle = screen.getByRole("button", {
      name: t("mods.reorder"),
    });

    expect(handle).toHaveClass("absolute", "inset-0");
    expect(handle.parentElement).toHaveClass("relative");
  });

  /* A `<tr>` is statically positioned, so `z-*` on the dragged row is ignored and
   * it paints underneath the sticky header. */
  it("positions the row so a dragged one can paint over the header", () => {
    seed([mod("alpha")], ["alpha"]);

    renderTable();

    expect(rowFor("ALPHA")).toHaveClass("relative");
  });

  /* A dragged row follows the pointer, and dnd-kit has no idea how big the list
   * is — left unclamped it slides out through the box's edge and gets clipped. */
  describe("ModTable drag bounds", () => {
    const ROW = 50;
    const MODS = ["alpha", "bravo", "charlie", "delta", "echo"];

    /** Set per test: where the scrolling box sits on the page. */
    let viewport = { top: 0, bottom: 150 };

    /* jsdom has no layout, so every rect is zero and nothing could be bounded.
     * Five rows of 50px, the box showing three. */
    function stubLayout(): void {
      const box = (top: number, height: number) =>
        ({
          x: 0,
          y: top,
          top,
          left: 0,
          bottom: top + height,
          right: 400,
          width: 400,
          height,
          toJSON: () => ({}),
        }) as DOMRect;

      vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(
        function (this: Element) {
          if (
            this.tagName === "DIV" &&
            String(this.className).includes("overflow-auto")
          ) {
            return box(viewport.top, viewport.bottom - viewport.top);
          }

          if (
            this.tagName === "TR" &&
            this.parentElement?.tagName === "TBODY"
          ) {
            const index = Array.prototype.indexOf.call(
              this.parentElement.children,
              this
            );

            return box(index * ROW, ROW);
          }

          return box(0, 0);
        }
      );
    }

    /** Drags a row to `y` and reads the translate mid-drag; the button is
     * released afterwards, or the next test starts with the pointer still held. */
    async function offsetWhileDragging(
      name: string,
      y: number
    ): Promise<number> {
      const handle = handleFor(name);
      const user = userEvent.setup();

      await user.pointer([
        { keys: "[MouseLeft>]", target: handle, coords: { x: 5, y: 0 } },
        // A first small move clears the sensor's activation distance; the second
        // move is the one that actually translates.
        { target: handle, coords: { x: 5, y: 10 } },
        { target: handle, coords: { x: 5, y } },
      ]);

      const offset = offsetOf(name);
      await user.pointer([{ keys: "[/MouseLeft]" }]);

      return offset;
    }

    function offsetOf(name: string): number {
      const transform = rowFor(name).style.transform;
      const match = /translate3d\(\s*[^,]+,\s*(-?[\d.]+)px/.exec(transform);

      if (!match)
        throw new Error(`no vertical translate on ${name}: ${transform}`);

      return Number(match[1]);
    }

    beforeEach(() => {
      useModsStore.getState().clearSelection();
      seed(
        MODS.map((id) => mod(id)),
        MODS
      );
      viewport = { top: 0, bottom: 150 };
      stubLayout();
    });

    afterEach(async () => {
      // dnd-kit removes its click suppressor on a 50ms timer; let it run before
      // the next test clicks anything.
      await new Promise((resolve) => setTimeout(resolve, 60));
      vi.restoreAllMocks();
    });

    it("stops a row dragged past the bottom of the list", async () => {
      renderTable();

      expect(await offsetWhileDragging("ALPHA", 225)).toBe(100);
    });

    it("stops a row dragged past the top of the list", async () => {
      renderTable();

      expect(await offsetWhileDragging("BRAVO", -200)).toBe(-50);
    });

    it("leaves a row alone while it is still inside the list", async () => {
      renderTable();

      expect(await offsetWhileDragging("ALPHA", 75)).toBe(75);
    });
  });

  it("gives every mod a square-cornered selection box", () => {
    seed([mod("alpha")], ["alpha"]);

    renderTable();

    expect(
      within(rowFor("ALPHA")).getByRole("checkbox", {
        name: t("mods.select"),
      })
    ).toHaveClass("rounded-[4px]");
  });

  it("selects a mod from its row checkbox", async () => {
    seed([mod("alpha")], ["alpha"]);

    renderTable();

    const user = userEvent.setup();
    await user.click(
      within(rowFor("ALPHA")).getByRole("checkbox", {
        name: t("mods.select"),
      })
    );

    expect(useModsStore.getState().selection).toEqual(["alpha"]);
  });

  it("offers a bulk action per kind of selected mod", async () => {
    seed(
      [mod("alpha"), mod("bravo", { external: true, enabled: false })],
      ["alpha"]
    );

    renderTable();

    const user = userEvent.setup();
    for (const name of ["ALPHA", "BRAVO"]) {
      await user.click(
        within(rowFor(name)).getByRole("checkbox", {
          name: t("mods.select"),
        })
      );
    }

    expect(
      screen.getByRole("button", {
        name: t("mods.bulkDisable", { count: 1 }),
      })
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: t("mods.bulkDelete", { count: 1 }) })
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", {
        name: t("mods.bulkDeleteExternal", { count: 1 }),
      })
    ).toBeInTheDocument();
  });

  it("confirms a bulk delete before running it", async () => {
    seed([mod("alpha")], ["alpha"]);

    renderTable();

    const user = userEvent.setup();
    await user.click(
      within(rowFor("ALPHA")).getByRole("checkbox", {
        name: t("mods.select"),
      })
    );
    await user.click(
      screen.getByRole("button", { name: t("mods.bulkDelete", { count: 1 }) })
    );

    expect(
      screen.getByRole("alertdialog", {
        name: t("mods.bulkDeleteTitle", { count: 1 }),
      })
    ).toBeInTheDocument();
    expect(bulkUninstallMutate).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: t("common.confirm") }));

    expect(bulkUninstallMutate).toHaveBeenCalledWith(
      { ids: ["alpha"], trash: false },
      { onSuccess: expect.any(Function) }
    );
  });

  it("lifts the dragged row clear of the sticky header", async () => {
    seed([mod("alpha"), mod("bravo")], ["alpha", "bravo"]);

    renderTable();

    /* The header is `z-40`; a dragged row below that paints underneath it. Keyboard
     * sorting stands in for a pointer drag and needs no layout engine. */
    const handle = handleFor("BRAVO");
    const user = userEvent.setup();

    await user.tab();
    handle.focus();
    await user.keyboard("{ }");
    await user.keyboard("{ArrowUp}");

    expect(rowFor("BRAVO")).toHaveClass("z-50");

    await user.keyboard("{ }");
  });

  it("reorders the load order from the keyboard", async () => {
    seed([mod("alpha"), mod("bravo")], ["alpha", "bravo"]);

    renderTable();

    const user = userEvent.setup();
    handleFor("BRAVO").focus();
    await user.keyboard("{ }");
    await user.keyboard("{ArrowUp}");
    await user.keyboard("{ }");

    expect(reorderMutate).toHaveBeenCalledWith(["bravo", "alpha"]);
  });

  it("offers the drag handle only while the table is in load order", async () => {
    seed([mod("alpha"), mod("bravo")], ["alpha", "bravo"]);

    renderTable();

    expect(handleFor("ALPHA")).not.toHaveAttribute("aria-disabled", "true");

    await userEvent
      .setup()
      .click(screen.getByRole("button", { name: t("mods.column.name") }));

    expect(handleFor("ALPHA")).toHaveAttribute("aria-disabled", "true");
    expect(handleFor("ALPHA")).toHaveAttribute(
      "title",
      t("mods.reorderNeedsLoadOrder")
    );
    expect(handleFor("ALPHA")).toHaveClass("cursor-not-allowed");
  });
});
