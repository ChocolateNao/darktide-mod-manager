/**
 * Keeping a dragged row inside the list it belongs to.
 *
 * dnd-kit hands back a raw pointer delta with no idea how big the list is, so a
 * row follows the pointer out through the clipping container and is sliced in
 * half. The offset is bounded before it becomes a transform.
 */

export interface ContainerEdge {
  top: number;
  bottom: number;
}

export interface RowBox {
  top: number;
  height: number;
}

export interface DragLimits {
  /** The furthest up a row may travel, as a translate. Never above 0. */
  min: number;
  /** The furthest down a row may travel, as a translate. Never below 0. */
  max: number;
  clamp: (offset: number) => number;
}

/**
 * One drag's worth of measurement: the container it must stay inside, plus the
 * row being dragged. The row's fields are prefixed because `top` means two
 * different things in the same object — the container's top edge and the row's
 * own top edge are rarely the same number, and reading `bounds.top` when the
 * caller meant the row is the kind of bug that only shows up on a scrolled list.
 */
export interface DragBounds {
  top: number;
  bottom: number;
  rowTop: number;
  rowHeight: number;
}

export function dragLimits(container: ContainerEdge, row: RowBox): DragLimits {
  /*
   * Either edge can go negative when the row already touches it — a row at the
   * bottom has a max of zero and must still drag upward, so the floor widens
   * the range, never closes it.
   */
  const min = Math.min(0, container.top - row.top);
  const max = Math.max(0, container.bottom - row.height - row.top);

  return {
    min,
    max,
    clamp: (offset: number) => Math.min(max, Math.max(min, offset)),
  };
}
