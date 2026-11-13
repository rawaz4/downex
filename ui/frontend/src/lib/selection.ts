// Multi-selection model for the download table (click / Shift / Ctrl, keyboard).
// Pure and immutable: every operation returns a new Selection. `order` is the list
// of ids as currently displayed.

export interface Selection {
  readonly ids: ReadonlySet<number>;
  /** Fixed end of a Shift range. */
  readonly anchor: number | null;
  /** Keyboard cursor (the active row). */
  readonly focus: number | null;
}

export const EMPTY_SELECTION: Selection = Object.freeze({
  ids: new Set<number>(),
  anchor: null,
  focus: null,
});

export interface Modifiers {
  readonly shift?: boolean;
  /** Ctrl on Windows/Linux (Cmd is accepted too). */
  readonly toggle?: boolean;
}

/** Index lookup over the displayed order; built once per list change by the caller. */
export type IndexOf = (id: number) => number;

export function indexer(order: readonly number[]): IndexOf {
  const map = new Map<number, number>();
  order.forEach((id, i) => map.set(id, i));
  return (id) => map.get(id) ?? -1;
}

function range(order: readonly number[], from: number, to: number): number[] {
  const lo = Math.min(from, to);
  const hi = Math.max(from, to);
  return order.slice(lo, hi + 1);
}

export function selectOnly(id: number): Selection {
  return { ids: new Set([id]), anchor: id, focus: id };
}

/** Pointer selection. */
export function clickSelect(
  sel: Selection,
  order: readonly number[],
  indexOf: IndexOf,
  id: number,
  mods: Modifiers = {},
): Selection {
  const target = indexOf(id);
  if (target < 0) return sel;
  if (mods.shift) {
    const anchorId = sel.anchor !== null && indexOf(sel.anchor) >= 0 ? sel.anchor : id;
    const span = range(order, indexOf(anchorId), target);
    const ids = mods.toggle ? new Set([...sel.ids, ...span]) : new Set(span);
    return { ids, anchor: anchorId, focus: id };
  }
  if (mods.toggle) {
    const ids = new Set(sel.ids);
    if (ids.has(id)) ids.delete(id);
    else ids.add(id);
    return { ids, anchor: id, focus: id };
  }
  return selectOnly(id);
}

/** Right-click keeps a multi-selection that contains the row, else selects just it. */
export function contextSelect(sel: Selection, id: number): Selection {
  if (sel.ids.has(id)) return { ...sel, focus: id };
  return selectOnly(id);
}

export type Move = number | 'first' | 'last';

/**
 * Keyboard movement. Plain: move and select the row. Shift: extend from the anchor.
 * Toggle (Ctrl): move the cursor only, keeping the selection.
 */
export function moveFocus(
  sel: Selection,
  order: readonly number[],
  indexOf: IndexOf,
  move: Move,
  mods: Modifiers = {},
): Selection {
  if (order.length === 0) return sel;
  const current = sel.focus !== null ? indexOf(sel.focus) : -1;
  let next: number;
  if (move === 'first') next = 0;
  else if (move === 'last') next = order.length - 1;
  else if (current < 0) next = move > 0 ? 0 : order.length - 1;
  else next = Math.min(order.length - 1, Math.max(0, current + move));
  const id = order[next] as number;
  if (mods.shift) {
    const anchorId =
      sel.anchor !== null && indexOf(sel.anchor) >= 0 ? sel.anchor : current >= 0 ? sel.focus! : id;
    const span = range(order, indexOf(anchorId), next);
    const ids = mods.toggle ? new Set([...sel.ids, ...span]) : new Set(span);
    return { ids, anchor: anchorId, focus: id };
  }
  if (mods.toggle) return { ...sel, focus: id };
  return selectOnly(id);
}

/** Ctrl+Space: toggle the row under the cursor. */
export function toggleFocused(sel: Selection): Selection {
  if (sel.focus === null) return sel;
  const ids = new Set(sel.ids);
  if (ids.has(sel.focus)) ids.delete(sel.focus);
  else ids.add(sel.focus);
  return { ids, anchor: sel.focus, focus: sel.focus };
}

export function selectAll(sel: Selection, order: readonly number[]): Selection {
  if (order.length === 0) return EMPTY_SELECTION;
  const focus = sel.focus !== null && order.includes(sel.focus) ? sel.focus : (order[0] as number);
  return { ids: new Set(order), anchor: sel.anchor ?? focus, focus };
}

/**
 * Drops ids that are no longer listed. Returns the same object when nothing changed,
 * so reactive consumers are not invalidated needlessly.
 */
export function prune(sel: Selection, has: (id: number) => boolean): Selection {
  let changed = false;
  const ids = new Set<number>();
  for (const id of sel.ids) {
    if (has(id)) ids.add(id);
    else changed = true;
  }
  const anchor = sel.anchor !== null && has(sel.anchor) ? sel.anchor : null;
  const focus = sel.focus !== null && has(sel.focus) ? sel.focus : null;
  if (!changed && anchor === sel.anchor && focus === sel.focus) return sel;
  return { ids, anchor, focus };
}

/** Selected ids in displayed order. */
export function orderedSelection(sel: Selection, order: readonly number[]): number[] {
  if (sel.ids.size === 0) return [];
  return order.filter((id) => sel.ids.has(id));
}
