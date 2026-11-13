import { render } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import type { Segment } from './api';
import SegmentBar, { toneOf } from './components/SegmentBar.svelte';
import { CELL_GAP_PX, MAX_CELLS, MIN_CELL_PX, barCells, cellsForWidth, rowCell, segmentCells } from './segments';
import { MiB, makeDownload, makeMediaDownload, makeMediaInfo } from './testing';

const SIZE = 64 * MiB;

/** `n` equal segments of the file, each with `received(i)` bytes. */
function split(n: number, received: (i: number, len: number) => number, size = SIZE): Segment[] {
  const step = Math.floor(size / n);
  return Array.from({ length: n }, (_, i) => {
    const start = i * step;
    const end = i === n - 1 ? size - 1 : (i + 1) * step - 1;
    return { start, end, received: received(i, end - start + 1) };
  });
}

describe('segmentCells', () => {
  it('shows at most 8 equal spans however many connections there are', () => {
    for (const n of [1, 3, 8, 16, 32, 64]) {
      const d = makeDownload({ id: 1, size: SIZE, state: 'downloading', segments: split(n, () => 0) });
      const cells = segmentCells(d);
      expect(cells).toHaveLength(Math.min(n, MAX_CELLS));
    }
    const sixteen = makeDownload({ id: 1, size: SIZE, state: 'downloading', segments: split(16, () => 0) });
    expect(segmentCells(sixteen, 4)).toHaveLength(4);
    expect(segmentCells(sixteen, 0)).toHaveLength(1);
  });

  it('fills each span with the received bytes that overlap it (a merge, not a sample)', () => {
    // 16 connections, every other one done: each of the 8 spans holds one done and one empty half.
    const d = makeDownload({ id: 1, size: SIZE, state: 'paused', segments: split(16, (i, len) => (i % 2 === 0 ? len : 0)) });
    const cells = segmentCells(d);
    expect(cells).toHaveLength(8);
    for (const c of cells) {
      expect(c.fraction).toBeCloseTo(0.5, 5);
      expect(c.live).toBe(false);
    }
  });

  it('sums a range that crosses span borders into each span it covers', () => {
    const e = SIZE / 8;
    // Eight segments of unequal length; the bar still shows eight equal spans of e bytes.
    const segments: Segment[] = [
      { start: 0, end: 3 * e - 1, received: 2.5 * e }, // spans 0 and 1 full, half of 2
      { start: 3 * e, end: 4 * e - 1, received: 0 },
      { start: 4 * e, end: 5 * e - 1, received: e },
      { start: 5 * e, end: 6 * e - 1, received: 0.5 * e },
      { start: 6 * e, end: 6.5 * e - 1, received: 0 },
      { start: 6.5 * e, end: 7 * e - 1, received: 0 },
      { start: 7 * e, end: 7.5 * e - 1, received: 0 },
      { start: 7.5 * e, end: 8 * e - 1, received: 0.25 * e },
    ];
    const d = makeDownload({ id: 1, size: SIZE, state: 'paused', segments });
    expect(segmentCells(d).map((c) => c.fraction)).toEqual([1, 1, 0.5, 0, 1, 0.5, 0, 0.25]);
    // Four spans of the same map: each sums the two spans above.
    expect(segmentCells(d, 4).map((c) => c.fraction)).toEqual([1, 0.25, 0.75, 0.125]);
  });

  it('marks the spans where a connection is writing, only while downloading', () => {
    // 4 connections, each a quarter into its range: fronts at 1/16, 5/16, 9/16, 13/16 of the file.
    const segments = split(4, (_, len) => len / 4);
    const running = segmentCells(makeDownload({ id: 1, size: SIZE, state: 'downloading', segments }));
    expect(running.map((c) => c.live)).toEqual([true, true, true, true]);
    expect(running.map((c) => c.fraction)).toEqual([0.25, 0.25, 0.25, 0.25]);
    // the same map with 8 spans would put them in spans 0, 2, 4 and 6 (8 cells need 8 segments)
    const eight = segmentCells(makeDownload({ id: 2, size: SIZE, state: 'downloading', segments: split(8, (i, len) => (i % 2 === 0 ? len / 2 : 0)) }));
    expect(eight.map((c) => c.live)).toEqual([true, true, true, true, true, true, true, true]);
    const paused = segmentCells(makeDownload({ id: 3, size: SIZE, state: 'paused', segments }));
    expect(paused.some((c) => c.live)).toBe(false);
    // a finished range has no front
    const done = segmentCells(makeDownload({ id: 4, size: SIZE, state: 'downloading', segments: split(2, (i, len) => (i === 0 ? len : 0)) }));
    expect(done).toEqual([
      { fraction: 1, live: false },
      { fraction: 0, live: true },
    ]);
  });

  it('snaps an almost complete span to full and reads an open end as the file end', () => {
    const d = makeDownload({
      id: 1,
      size: 1000,
      state: 'downloading',
      segments: [
        { start: 0, end: 499, received: 498 },
        { start: 500, end: -1, received: 250 },
      ],
    });
    const cells = segmentCells(d);
    expect(cells[0]).toEqual({ fraction: 1, live: true });
    expect(cells[1]?.fraction).toBeCloseTo(0.5, 5);
  });

  it('gives a stream and a download without segments one cell of its progress', () => {
    const stream = makeMediaDownload({
      id: 1,
      state: 'downloading',
      received: 30 * MiB,
      media: makeMediaInfo({ fragmentsTotal: 200, fragmentsDone: 50 }),
    });
    expect(segmentCells(stream)).toEqual([{ fraction: 0.25, live: true }]);
    const plain = makeDownload({ id: 2, size: 100, received: 40, state: 'paused' });
    expect(segmentCells(plain)).toEqual([{ fraction: 0.4, live: false }]);
  });

  it('shows an unknown size as one indeterminate cell, hatched while bytes arrive', () => {
    const arriving = makeDownload({ id: 1, size: -1, received: 5 * MiB, state: 'downloading', segments: [{ start: 0, end: -1, received: 5 * MiB }] });
    expect(segmentCells(arriving)).toEqual([{ fraction: 0, live: true, indeterminate: true }]);
    const waiting = makeDownload({ id: 2, size: -1, received: 0, state: 'queued' });
    expect(segmentCells(waiting)).toEqual([{ fraction: 0, live: false, indeterminate: true }]);
    const stream = makeMediaDownload({ id: 3, state: 'downloading', received: MiB, media: makeMediaInfo({ fragmentsTotal: 0 }) });
    expect(segmentCells(stream)[0]).toMatchObject({ indeterminate: true, live: true });
  });

  it('fills every cell of a completed download', () => {
    const d = makeDownload({ id: 1, size: SIZE, received: SIZE, state: 'completed', segments: split(4, (_, len) => len - 1) });
    expect(segmentCells(d)).toEqual(Array.from({ length: 4 }, () => ({ fraction: 1, live: false })));
  });
});

describe('barCells', () => {
  // the Comfortable row's bar cell
  const ROW_PX = 118;
  const at = (state: 'downloading' | 'paused' | 'failed' | 'queued', share: number, n = 8) =>
    makeDownload({ id: 1, size: SIZE, received: Math.round(SIZE * share), state, segments: split(n, (_, len) => Math.round(len * share)) });

  it('draws a download that is not running as one cell of its progress, never a dotted line', () => {
    for (const state of ['paused', 'failed', 'queued'] as const) {
      const cells = barCells(at(state, 0.18), ROW_PX);
      expect(cells).toHaveLength(1);
      expect(cells[0]?.fraction).toBeCloseTo(0.18, 2);
      expect(cells[0]?.live).toBe(false);
      // with no width known as well
      expect(barCells(at(state, 0.18), 0)).toHaveLength(1);
    }
    expect(barCells(makeDownload({ id: 2, size: -1, received: 5, state: 'paused' }), ROW_PX)).toEqual([{ fraction: 0, live: false, indeterminate: true }]);
  });

  it('draws a running file as one solid fill of its progress, whatever the width', () => {
    for (const w of [0, 40, ROW_PX, 600]) {
      for (const share of [0.02, 0.18, 0.71]) {
        const d = at('downloading', share);
        expect(barCells(d, w)).toEqual([rowCell(d)]);
      }
    }
  });

  it('keeps a completed file one full cell', () => {
    const d = makeDownload({ id: 1, size: SIZE, received: SIZE, state: 'completed', segments: split(4, (_, len) => len) });
    expect(barCells(d, ROW_PX)).toEqual([rowCell(d)]);
    expect(rowCell(d).fraction).toBe(1);
  });
});

describe('cellsForWidth', () => {
  it('keeps every cell at least 12 px wide, at most 8, and all 8 when the width is unknown', () => {
    expect(MIN_CELL_PX).toBe(12);
    expect(CELL_GAP_PX).toBe(2);
    expect(cellsForWidth(0)).toBe(8);
    expect(cellsForWidth(10)).toBe(1);
    expect(cellsForWidth(12)).toBe(1);
    expect(cellsForWidth(26)).toBe(2); // 12 + 2 + 12
    expect(cellsForWidth(90)).toBe(6); // the palette's mini bar
    expect(cellsForWidth(110)).toBe(8);
    expect(cellsForWidth(400)).toBe(8);
    expect(cellsForWidth(400, 4)).toBe(4);
    for (let w = 1; w < 200; w++) {
      const n = cellsForWidth(w);
      if (n > 1) expect((w - (n - 1) * CELL_GAP_PX) / n).toBeGreaterThanOrEqual(MIN_CELL_PX);
    }
  });
});

describe('rowCell', () => {
  it('draws a running file as one solid fill of its overall progress, however many connections', () => {
    for (const n of [1, 8, 16, 32]) {
      const d = makeDownload({ id: 1, size: SIZE, received: Math.round(SIZE * 0.71), state: 'downloading', segments: split(n, (_, len) => Math.round(len * 0.71)) });
      const cell = rowCell(d);
      expect(cell.fraction).toBeCloseTo(0.71, 2);
      expect(cell.live).toBe(true);
    }
  });

  it('follows the state: full when completed, still for paused, indeterminate for an unknown size', () => {
    expect(rowCell(makeDownload({ id: 1, size: SIZE, received: SIZE, state: 'completed', segments: split(4, (_, len) => len - 1) }))).toEqual({ fraction: 1, live: false });
    expect(rowCell(makeDownload({ id: 2, size: 100, received: 40, state: 'paused' }))).toEqual({ fraction: 0.4, live: false });
    expect(rowCell(makeDownload({ id: 3, size: -1, received: 5, state: 'downloading' }))).toEqual({ fraction: 0, live: true, indeterminate: true });
    expect(rowCell(makeDownload({ id: 4, size: -1, received: 0, state: 'queued' }))).toEqual({ fraction: 0, live: false, indeterminate: true });
    const stream = makeMediaDownload({ id: 5, state: 'downloading', received: 30 * MiB, media: makeMediaInfo({ fragmentsTotal: 200, fragmentsDone: 50 }) });
    expect(rowCell(stream)).toEqual({ fraction: 0.25, live: true });
  });
});

describe('SegmentBar', () => {
  it('draws a row as one solid cell, never a dashed line', () => {
    const d = makeDownload({ id: 1, size: SIZE, received: Math.round(SIZE * 0.71), state: 'downloading', segments: split(8, (_, len) => Math.round(len * 0.71)) });
    for (const size of ['row', 'compact', 'mini'] as const) {
      const { container } = render(SegmentBar, { props: { download: d, size } });
      const bar = container.querySelector('.segbar') as HTMLElement;
      expect(bar.querySelectorAll('.cell')).toHaveLength(1);
      expect(bar.querySelectorAll('.cell.live')).toHaveLength(1);
      expect(Number((bar.querySelector('.fill') as HTMLElement).style.getPropertyValue('--f'))).toBeCloseTo(0.71, 2);
    }
  });

  it('draws the progress window bar as one solid decorative fill with the live edge and the state tone', () => {
    const d = makeDownload({ id: 1, size: SIZE, received: SIZE / 2, state: 'downloading', segments: split(16, (_, len) => len / 2) });
    const { container } = render(SegmentBar, { props: { download: d, size: 'lg' } });
    const bar = container.querySelector('.segbar') as HTMLElement;
    expect(bar.getAttribute('aria-hidden')).toBe('true');
    expect(bar.classList.contains('tone-fill')).toBe(true);
    expect(bar.querySelectorAll('.cell')).toHaveLength(1);
    expect(bar.querySelectorAll('.cell.live')).toHaveLength(1);
    expect((bar.querySelector('.fill') as HTMLElement).style.getPropertyValue('--f')).toBe('0.5');
  });

  it('draws a paused download as one cell, however many connections it had', () => {
    const d = makeDownload({ id: 1, size: SIZE, received: SIZE * 0.18, state: 'paused', segments: split(8, (_, len) => len * 0.18) });
    const { container } = render(SegmentBar, { props: { download: d } });
    const bar = container.querySelector('.segbar') as HTMLElement;
    expect(bar.classList.contains('tone-paused')).toBe(true);
    expect(bar.querySelectorAll('.cell')).toHaveLength(1);
  });

  it('follows the state for its colour and draws a hatch for an unknown size', () => {
    expect(toneOf('paused')).toBe('paused');
    expect(toneOf('failed')).toBe('failed');
    expect(toneOf('queued')).toBe('idle');
    expect(toneOf('completed')).toBe('fill');
    const { container } = render(SegmentBar, {
      props: { download: makeDownload({ id: 1, size: -1, received: 10, state: 'downloading' }), size: 'compact' },
    });
    expect(container.querySelector('.segbar.size-compact .cell.hatch')).not.toBeNull();
    expect(container.querySelector('.fill')).toBeNull();
  });
});
