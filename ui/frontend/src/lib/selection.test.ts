import { describe, expect, it } from 'vitest';
import {
  EMPTY_SELECTION,
  clickSelect,
  contextSelect,
  indexer,
  moveFocus,
  orderedSelection,
  prune,
  selectAll,
  selectOnly,
  toggleFocused,
  type Selection,
} from './selection';

const order = [10, 20, 30, 40, 50];
const idx = indexer(order);
const ids = (s: Selection) => [...s.ids].sort((a, b) => a - b);

describe('pointer selection', () => {
  it('plain click selects one row and sets anchor and focus', () => {
    const s = clickSelect(selectOnly(10), order, idx, 30);
    expect(ids(s)).toEqual([30]);
    expect(s.anchor).toBe(30);
    expect(s.focus).toBe(30);
  });

  it('ctrl-click toggles rows', () => {
    let s = clickSelect(EMPTY_SELECTION, order, idx, 20);
    s = clickSelect(s, order, idx, 40, { toggle: true });
    expect(ids(s)).toEqual([20, 40]);
    s = clickSelect(s, order, idx, 20, { toggle: true });
    expect(ids(s)).toEqual([40]);
    expect(s.focus).toBe(20);
  });

  it('shift-click selects the range from the anchor, in either direction', () => {
    let s = clickSelect(EMPTY_SELECTION, order, idx, 20);
    s = clickSelect(s, order, idx, 40, { shift: true });
    expect(ids(s)).toEqual([20, 30, 40]);
    expect(s.anchor).toBe(20);
    s = clickSelect(s, order, idx, 10, { shift: true });
    expect(ids(s)).toEqual([10, 20]);
  });

  it('ctrl+shift-click adds a range to the selection', () => {
    let s = clickSelect(EMPTY_SELECTION, order, idx, 10);
    s = clickSelect(s, order, idx, 30, { toggle: true });
    s = clickSelect(s, order, idx, 50, { shift: true, toggle: true });
    expect(ids(s)).toEqual([10, 30, 40, 50]);
  });

  it('shift-click without an anchor selects just the row', () => {
    const s = clickSelect(EMPTY_SELECTION, order, idx, 30, { shift: true });
    expect(ids(s)).toEqual([30]);
  });

  it('ignores ids that are not displayed', () => {
    const s = selectOnly(10);
    expect(clickSelect(s, order, idx, 999)).toBe(s);
  });

  it('right-click keeps a selection containing the row', () => {
    const multi = selectAll(EMPTY_SELECTION, order);
    expect(ids(contextSelect(multi, 30))).toEqual(order);
    expect(contextSelect(multi, 30).focus).toBe(30);
    expect(ids(contextSelect(selectOnly(10), 30))).toEqual([30]);
  });
});

describe('keyboard selection', () => {
  it('starts at the first or last row', () => {
    expect(moveFocus(EMPTY_SELECTION, order, idx, 1).focus).toBe(10);
    expect(moveFocus(EMPTY_SELECTION, order, idx, -1).focus).toBe(50);
  });

  it('moves and clamps', () => {
    let s = selectOnly(20);
    s = moveFocus(s, order, idx, 1);
    expect(ids(s)).toEqual([30]);
    s = moveFocus(s, order, idx, 10);
    expect(s.focus).toBe(50);
    s = moveFocus(s, order, idx, 'first');
    expect(ids(s)).toEqual([10]);
    s = moveFocus(s, order, idx, -1);
    expect(s.focus).toBe(10);
  });

  it('shift extends from the anchor and can shrink back', () => {
    let s = selectOnly(20);
    s = moveFocus(s, order, idx, 1, { shift: true });
    s = moveFocus(s, order, idx, 1, { shift: true });
    expect(ids(s)).toEqual([20, 30, 40]);
    s = moveFocus(s, order, idx, -1, { shift: true });
    expect(ids(s)).toEqual([20, 30]);
    s = moveFocus(s, order, idx, 'last', { shift: true });
    expect(ids(s)).toEqual([20, 30, 40, 50]);
  });

  it('ctrl moves only the cursor; ctrl+space toggles under it', () => {
    let s = selectOnly(10);
    s = moveFocus(s, order, idx, 2, { toggle: true });
    expect(ids(s)).toEqual([10]);
    expect(s.focus).toBe(30);
    s = toggleFocused(s);
    expect(ids(s)).toEqual([10, 30]);
    s = toggleFocused(s);
    expect(ids(s)).toEqual([10]);
  });

  it('does nothing on an empty list', () => {
    expect(moveFocus(EMPTY_SELECTION, [], indexer([]), 1)).toBe(EMPTY_SELECTION);
  });
});

describe('select all, prune and order', () => {
  it('selects every displayed row and keeps the cursor', () => {
    const s = selectAll(selectOnly(30), order);
    expect(ids(s)).toEqual(order);
    expect(s.focus).toBe(30);
    expect(selectAll(selectOnly(30), [])).toBe(EMPTY_SELECTION);
  });

  it('prunes ids that disappeared, returning the same object when nothing changed', () => {
    const s = selectAll(selectOnly(30), order);
    expect(prune(s, () => true)).toBe(s);
    const pruned = prune(s, (id) => id !== 30 && id !== 40);
    expect(ids(pruned)).toEqual([10, 20, 50]);
    expect(pruned.focus).toBeNull();
  });

  it('returns selected ids in displayed order', () => {
    let s = clickSelect(EMPTY_SELECTION, order, idx, 50);
    s = clickSelect(s, order, idx, 10, { toggle: true });
    expect(orderedSelection(s, order)).toEqual([10, 50]);
  });
});
