// Progress bars (DESIGN-SYSTEM.md §3.13). List rows draw `rowCell`: one solid fill of the overall
// progress. The progress window draws `barCells`, a connection map of at most `maxCells` equal byte
// spans of the file, each filled with the share of its bytes received: one cell for a download that
// is not running, and never more cells than the width and the received ink allow, so that every
// cell is at least MIN_CELL_PX wide and holds at least 10 px of fill.

import type { Download, MediaInfo } from './api';
import { progressFraction } from './format';

/** One cell of the bar. */
export interface SegCell {
  /** 0..1 of the span received (0 for an indeterminate cell). */
  readonly fraction: number;
  /**
   * A connection's front lies in this span while the download runs (the cell gets a static
   * live edge). For an indeterminate cell: bytes are arriving (it is hatched).
   */
  readonly live: boolean;
  /** The size is unknown: the bar cannot show how much is there. */
  readonly indeterminate?: boolean;
}

/** What segmentCells reads of a download. */
export type SegmentSource = Pick<Download, 'size' | 'received' | 'state' | 'segments' | 'kind'> & {
  readonly media?: MediaInfo | null;
  readonly sizeApprox?: boolean;
};

/** The most cells a bar shows. */
export const MAX_CELLS = 8;
/** The narrowest a cell is drawn, and the gap between cells (px). */
export const MIN_CELL_PX = 12;
export const CELL_GAP_PX = 2;
/** A span received up to this share counts as complete (no sliver of track left). */
const SNAP = 0.995;

/**
 * At most `maxCells` equal byte spans of the file; each span's fill = its received bytes / its
 * length.
 *
 * - Media downloads and downloads without segments: one cell with the download's progress
 *   (`progressFraction`, fragments for a stream); indeterminate when that is unknown.
 * - Unknown size (`size <= 0`): one indeterminate cell, live (hatched) while downloading with
 *   bytes received.
 * - Otherwise n = clamp(segments, 1, maxCells) spans [k·size/n, (k+1)·size/n). A span receives
 *   the overlap of every segment's received range `[start, start + min(received, len))` (an open
 *   end means `size − 1`). Shares from 0.995 snap to 1. A span is live while the download runs
 *   and some unfinished segment's front `start + received` lies in it.
 *
 * A completed download's cells are full.
 */
export function segmentCells(d: SegmentSource, maxCells: number = MAX_CELLS): SegCell[] {
  const downloading = d.state === 'downloading';
  if (d.state === 'completed') {
    const n = d.kind !== 'media' && d.size > 0 ? clamp(d.segments.length, 1, Math.max(1, Math.floor(maxCells))) : 1;
    return Array.from({ length: n }, () => ({ fraction: 1, live: false }));
  }
  if (d.kind === 'media' || d.segments.length === 0) {
    const f = progressFraction(d);
    if (!Number.isFinite(f)) return [indeterminate(downloading && d.received > 0)];
    return [{ fraction: snap(f), live: downloading }];
  }
  if (!(d.size > 0)) return [indeterminate(downloading && d.received > 0)];

  const n = clamp(d.segments.length, 1, Math.max(1, Math.floor(maxCells)));
  const size = d.size;
  const got = new Array<number>(n).fill(0);
  const live = new Array<boolean>(n).fill(false);
  const bound = (k: number) => (k * size) / n;
  for (const s of d.segments) {
    const end = s.end < 0 ? size - 1 : Math.min(s.end, size - 1);
    const len = end - s.start + 1;
    if (!(len > 0)) continue;
    const received = Math.min(Math.max(0, s.received), len);
    const from = s.start;
    const to = s.start + received;
    if (to > from) {
      // Only the spans this range touches.
      const first = Math.max(0, Math.floor((from * n) / size));
      const last = Math.min(n - 1, Math.floor(((to - 1) * n) / size));
      for (let k = first; k <= last; k++) {
        const overlap = Math.min(to, bound(k + 1)) - Math.max(from, bound(k));
        if (overlap > 0) got[k] = (got[k] ?? 0) + overlap;
      }
    }
    if (downloading && received < len) {
      const front = s.start + received;
      const k = Math.min(n - 1, Math.max(0, Math.floor((front * n) / size)));
      live[k] = true;
    }
  }
  return got.map((bytes, k) => ({
    fraction: snap(bytes / (bound(k + 1) - bound(k))),
    live: live[k] ?? false,
  }));
}

/**
 * The one cell a list row draws (DESIGN-SYSTEM.md §3.13): the download's overall progress as a
 * single solid fill, however many connections it has, so a row never reads as a dashed line. The
 * connection map is the progress window's (`barCells`) and the details pane's Parts boxes.
 */
export function rowCell(d: SegmentSource): SegCell {
  if (d.state === 'completed') return { fraction: 1, live: false };
  const downloading = d.state === 'downloading';
  const f = progressFraction(d);
  if (!Number.isFinite(f)) return indeterminate(downloading && d.received > 0);
  return { fraction: snap(f), live: downloading };
}

/** The received ink each cell must at least have room for (px): never more cells than 10 px chunks of fill. */
export const FILL_PX_PER_CELL = 10;

/**
 * The cells of the progress window's bar (DESIGN-SYSTEM.md §3.13, owner's choice 2026-11-10): one
 * solid fill of the overall progress, its `rowCell`, in every state and for every kind of download,
 * as the list rows draw it. The per-connection view is the details' segment map. `width` and
 * `maxCells` are kept for the callers and not used.
 */
export function barCells(d: SegmentSource, width: number, maxCells: number = MAX_CELLS): SegCell[] {
  void width;
  void maxCells;
  return [rowCell(d)];
}

/** How many cells fit in `width` px with every cell at least MIN_CELL_PX wide (no width known: `maxCells`). */
export function cellsForWidth(width: number, maxCells: number = MAX_CELLS): number {
  if (!(width > 0)) return Math.max(1, Math.floor(maxCells));
  return clamp(Math.floor((width + CELL_GAP_PX) / (MIN_CELL_PX + CELL_GAP_PX)), 1, Math.max(1, Math.floor(maxCells)));
}

function indeterminate(live: boolean): SegCell {
  return { fraction: 0, live, indeterminate: true };
}

function snap(f: number): number {
  if (!Number.isFinite(f) || f <= 0) return 0;
  return f >= SNAP ? 1 : f;
}

function clamp(v: number, lo: number, hi: number): number {
  return Math.min(hi, Math.max(lo, v));
}
