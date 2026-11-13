import { fireEvent, render } from '@testing-library/svelte';
import { tick } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Download } from '../api';
import { createMockApi } from '../mock';
import { DENSITY_KEY, density } from '../stores/density.svelte';
import { DownloadsStore } from '../stores/downloads.svelte';
import { COLUMNS, ROW_PAD_END, ROW_PAD_START, fitColumns, inlineAction, progressCell, rowHeightFor } from '../display';
import DownloadTable from './DownloadTable.svelte';
import { MiB, fakeApi, makeDownload, makeMediaDownload, makeMediaInfo } from '../testing';

let cleanup: (() => void)[] = [];
afterEach(() => {
  for (const c of cleanup) c();
  cleanup = [];
  density.set('comfortable');
});

/** The table over a fixed list (fakeApi), with every optional prop it is given. */
async function table(list: readonly Download[], props: Record<string, unknown> = {}) {
  const api = fakeApi(list);
  const store = new DownloadsStore(api);
  cleanup.push(() => store.dispose());
  await store.start();
  const onaction = vi.fn();
  const utils = render(DownloadTable, { props: { store, onopen: vi.fn(), onmenu: vi.fn(), onadd: vi.fn(), onaction, ...props } });
  const cell = (id: number, key: string) => utils.container.querySelector<HTMLElement>(`#dl-cell-${id}-${key}`);
  const grid = utils.getByRole('grid');
  return { ...utils, api, store, cell, grid, onaction };
}

async function setup(count: number) {
  const api = createMockApi({ count, seed: 11, autoTick: false });
  const store = new DownloadsStore(api);
  cleanup.push(() => {
    store.dispose();
    api.dispose();
  });
  await store.start();
  const onopen = vi.fn();
  const onmenu = vi.fn();
  const utils = render(DownloadTable, { props: { store, onopen, onmenu, onadd: vi.fn() } });
  const grid = utils.getByRole('grid', { name: 'Downloads' });
  const rows = () => Array.from(grid.querySelectorAll<HTMLElement>('[role="row"][data-id]'));
  return { ...utils, api, store, grid, rows, onopen, onmenu };
}

describe('DownloadTable', () => {
  it('renders only a window of rows for 10,000 downloads', async () => {
    const t0 = performance.now();
    const { grid, rows, store } = await setup(10_000);
    expect(store.visible).toHaveLength(10_000);
    expect(grid.getAttribute('aria-rowcount')).toBe('10001');
    expect(rows().length).toBeGreaterThan(0);
    expect(rows().length).toBeLessThanOrEqual(80);
    expect(rows()[0]?.getAttribute('aria-rowindex')).toBe('2');
    expect(performance.now() - t0).toBeLessThan(5000);
  });

  it('sorts from the column headers with aria-sort', async () => {
    const { getByRole, store } = await setup(50);
    const sizeHeader = getByRole('columnheader', { name: /Size/ });
    await fireEvent.click(getByRole('button', { name: 'Size' }));
    expect(store.sort).toEqual({ key: 'size', dir: 'desc' });
    expect(sizeHeader.getAttribute('aria-sort')).toBe('descending');
  });

  it('supports keyboard selection with an active descendant', async () => {
    const { grid, store } = await setup(50);
    grid.focus();
    await tick();
    expect(store.selection.focus).toBe(store.visibleIds[0]);
    expect(store.selection.ids.size).toBe(0);

    await fireEvent.keyDown(grid, { key: 'Home' });
    expect([...store.selection.ids]).toEqual([store.visibleIds[0]]);
    await fireEvent.keyDown(grid, { key: 'ArrowDown', shiftKey: true });
    await fireEvent.keyDown(grid, { key: 'ArrowDown', shiftKey: true });
    expect(store.selected.map((d) => d.id)).toEqual(store.visibleIds.slice(0, 3));
    expect(grid.getAttribute('aria-activedescendant')).toBe(`dl-row-${store.visibleIds[2]}`);
    const active = document.getElementById(`dl-row-${store.visibleIds[2]}`);
    expect(active?.getAttribute('aria-selected')).toBe('true');

    await fireEvent.keyDown(grid, { key: 'End' });
    expect(store.selection.focus).toBe(store.visibleIds.at(-1));
    await fireEvent.keyDown(grid, { key: 'Escape' });
    expect(store.selection.ids.size).toBe(0);
  });

  it('selects with click, ctrl-click and shift-click', async () => {
    const { rows, store } = await setup(50);
    const [r0, r1, r2, r3] = rows();
    await fireEvent.click(r0!);
    await fireEvent.click(r2!, { ctrlKey: true });
    expect(store.selected.map((d) => d.id)).toEqual([Number(r0!.dataset.id), Number(r2!.dataset.id)]);
    await fireEvent.click(r3!, { shiftKey: true });
    expect(store.selected.map((d) => d.id)).toEqual([r2, r3].map((r) => Number(r!.dataset.id)));
    expect(r1!.getAttribute('aria-selected')).toBe('false');
  });

  it('opens on Enter and double-click, and offers a context menu', async () => {
    const { grid, rows, store, onopen, onmenu } = await setup(50);
    const row = rows()[1]!;
    await fireEvent.dblClick(row);
    expect(onopen).toHaveBeenCalledWith(store.get(Number(row.dataset.id)));

    await fireEvent.contextMenu(row, { clientX: 40, clientY: 60 });
    expect(store.selected.map((d) => d.id)).toEqual([Number(row.dataset.id)]);
    expect(onmenu).toHaveBeenLastCalledWith(40, 60);

    grid.focus();
    await fireEvent.keyDown(grid, { key: 'Enter' });
    expect(onopen).toHaveBeenCalledTimes(2);
    await fireEvent.keyDown(grid, { key: 'F10', shiftKey: true });
    expect(onmenu).toHaveBeenCalledTimes(2);
  });

  it('follows live updates from the daemon', async () => {
    const { api, store, grid } = await setup(20);
    const running = store.all.find((d) => d.state === 'downloading');
    expect(running).toBeDefined();
    const before = running!.received;
    store.sortBy('status');
    api.tick(1000);
    await tick();
    expect(store.get(running!.id)!.received).toBeGreaterThan(before);
    const cell = grid.querySelector(`#dl-cell-${running!.id}-progress`);
    expect(cell?.textContent).toMatch(/\d+%/);
  });
});

describe('DownloadTable: screen-reader semantics', () => {
  it('gives rows a stable name and keeps live values out of it', async () => {
    const { rows } = await setup(30);
    const row = rows()[0]!;
    const id = row.dataset.id;
    expect(row.getAttribute('aria-labelledby')).toBe(`dl-name-${id} dl-status-${id}`);
    expect(row.getAttribute('aria-describedby')).toBe(`dl-desc-${id}`);
    expect(document.getElementById(`dl-desc-${id}`)?.textContent).toMatch(/^Size .+, added .+/);
    // progress bars in rows are decorative; the percentage stays readable as cell text
    expect(row.querySelector('[role="progressbar"]')).toBeNull();
    const bars = [...document.querySelectorAll('[role="row"] .segbar')];
    expect(bars.length).toBeGreaterThan(0);
    for (const bar of bars) expect(bar.getAttribute('aria-hidden')).toBe('true');
  });

  it('keeps the sort buttons out of the Tab order', async () => {
    const { getAllByRole } = await setup(5);
    const sortButtons = getAllByRole('columnheader').map((h) => h.querySelector('button')!);
    expect(sortButtons).toHaveLength(COLUMNS.length);
    for (const b of sortButtons) expect(b.tabIndex).toBe(-1);
  });

  it('reaches the column headers with ArrowUp and sorts with Enter', async () => {
    const { grid, store } = await setup(20);
    grid.focus();
    await tick();
    await fireEvent.keyDown(grid, { key: 'ArrowUp' });
    expect(grid.getAttribute('aria-activedescendant')).toBe('dl-col-name');
    await fireEvent.keyDown(grid, { key: 'ArrowRight' });
    expect(grid.getAttribute('aria-activedescendant')).toBe('dl-col-size');
    await fireEvent.keyDown(grid, { key: 'Enter' });
    expect(store.sort).toEqual({ key: 'size', dir: 'desc' });
    await fireEvent.keyDown(grid, { key: ' ' });
    expect(store.sort).toEqual({ key: 'size', dir: 'asc' });
    await fireEvent.keyDown(grid, { key: 'ArrowDown' });
    expect(grid.getAttribute('aria-activedescendant')).toBe(`dl-cell-${store.selection.focus}-size`);
  });

  it('moves between cells with Left and Right', async () => {
    const { grid, store } = await setup(20);
    grid.focus();
    await tick();
    await fireEvent.keyDown(grid, { key: 'Home' });
    const id = store.selection.focus;
    expect(grid.getAttribute('aria-activedescendant')).toBe(`dl-row-${id}`);
    await fireEvent.keyDown(grid, { key: 'ArrowRight' });
    expect(grid.getAttribute('aria-activedescendant')).toBe(`dl-cell-${id}-name`);
    await fireEvent.keyDown(grid, { key: 'ArrowRight' });
    await fireEvent.keyDown(grid, { key: 'ArrowRight' });
    const cell = document.getElementById(grid.getAttribute('aria-activedescendant')!);
    expect(cell?.getAttribute('role')).toBe('gridcell');
    expect(cell?.getAttribute('aria-colindex')).toBe('3');
    // the column is kept while moving between rows
    await fireEvent.keyDown(grid, { key: 'ArrowDown' });
    expect(grid.getAttribute('aria-activedescendant')).toBe(`dl-cell-${store.selection.focus}-progress`);
    await fireEvent.keyDown(grid, { key: 'Escape' });
    expect(grid.getAttribute('aria-activedescendant')).toBe(`dl-row-${store.selection.focus}`);
    await fireEvent.keyDown(grid, { key: 'ArrowRight' });
    await fireEvent.keyDown(grid, { key: 'ArrowLeft' });
    await fireEvent.keyDown(grid, { key: 'ArrowLeft' });
    expect(grid.getAttribute('aria-activedescendant')).toBe(`dl-row-${store.selection.focus}`);
  });

  it('reads progress on demand with I', async () => {
    const { grid, store, getAllByRole } = await setup(20);
    const running = store.visible.findIndex((d) => d.state === 'downloading');
    grid.focus();
    await tick();
    await fireEvent.keyDown(grid, { key: 'Home' });
    for (let i = 0; i < running; i++) await fireEvent.keyDown(grid, { key: 'ArrowDown' });
    await fireEvent.keyDown(grid, { key: 'i' });
    await tick();
    const d = store.get(store.selection.focus!)!;
    const live = getAllByRole('status').find((s) => s.textContent?.startsWith(d.filename || ''));
    expect(live?.textContent).toMatch(/Downloading, \d+%, .+ of .+, .+\/s/);
  });
});

describe('DownloadTable: keyboard position', () => {
  it('starts at the first row in view when focused after scrolling with the mouse', async () => {
    const { grid, store } = await setup(500);
    const rowH = parseFloat(grid.style.getPropertyValue('--row-h'));
    expect(rowH).toBe(46);
    grid.scrollTop = 300 * rowH;
    await fireEvent.scroll(grid);
    grid.focus();
    await tick();
    expect(store.visibleIds.indexOf(store.selection.focus!)).toBe(300);
    expect(store.selection.ids.size).toBe(0);
    expect(grid.getAttribute('aria-activedescendant')).toBe(`dl-row-${store.selection.focus}`);
  });

  it('keeps the cursor row rendered after removing the rows around it', async () => {
    const { grid, store } = await setup(400);
    grid.focus();
    await tick();
    await fireEvent.keyDown(grid, { key: 'End' });
    await tick();
    const lastIds = store.visibleIds.slice(-3);
    store.click(lastIds[0]!);
    store.click(lastIds[2]!, { shift: true });
    await store.remove(lastIds, false);
    await tick();
    const focus = store.selection.focus;
    expect(focus).toBe(store.visibleIds.at(-1));
    expect(grid.getAttribute('aria-activedescendant')).toBe(`dl-row-${focus}`);
  });

  it('marks a stale list and blanks live values', async () => {
    const { grid, store, api } = await setup(20);
    const running = store.visible.find((d) => d.state === 'downloading')!; // a rendered row
    expect(grid.querySelector(`#dl-cell-${running.id}-speed`)?.textContent).not.toBe('');
    store.daemon = { ok: false, error: 'connection refused' };
    await tick();
    expect(grid.classList.contains('stale')).toBe(true);
    expect(grid.getAttribute('aria-label')).toBe('Downloads, last known state');
    expect(grid.querySelector(`#dl-cell-${running.id}-speed`)?.textContent).toBe('');
    expect(grid.querySelector(`#dl-cell-${running.id}-eta`)?.textContent).toBe('');
    void api;
  });
});

describe('fitColumns', () => {
  const widthOf = (c: (typeof COLUMNS)[number]) => c.width * 16;

  it('shows every column when there is room', () => {
    expect(fitColumns(1200, 16, widthOf).map((c) => c.key)).toEqual(COLUMNS.map((c) => c.key));
  });

  it('drops Added, Conn. and Time left first in a narrower window', () => {
    // a 1,000 px window − the 224 px sidebar − the rows' padding and the card's border
    expect(fitColumns(1000 - 224 - ROW_PAD_START - ROW_PAD_END - 2, 16, widthOf).map((c) => c.key)).toEqual([
      'name',
      'size',
      'progress',
      'speed',
      'status',
    ]);
  });

  it('keeps Name, Progress, Speed and Status in the 900 px minimum window', () => {
    expect(fitColumns(900 - 224 - ROW_PAD_START - ROW_PAD_END - 2, 16, widthOf).map((c) => c.key)).toEqual([
      'name',
      'progress',
      'speed',
      'status',
    ]);
  });

  it('never drops Name, Progress or Status', () => {
    expect(fitColumns(200, 16, widthOf).map((c) => c.key)).toEqual(['name', 'progress', 'status']);
  });

  it('accounts for resized columns', () => {
    const wide = (c: (typeof COLUMNS)[number]) => (c.key === 'status' ? 400 : c.width * 16);
    expect(fitColumns(1000, 16, wide).map((c) => c.key)).not.toContain('added');
  });
});

describe('DownloadTable: media downloads', () => {
  it('marks streams with a badge and counts their fragments as progress', async () => {
    const api = fakeApi([
      makeMediaDownload({ id: 1, state: 'downloading', received: 40 * MiB, speedBps: MiB, media: makeMediaInfo({ fragmentsDone: 60 }) }),
      makeMediaDownload({ id: 2, state: 'queued', media: makeMediaInfo({ format: 'dash' }) }),
      makeDownload({ id: 3, filename: 'plain.zip' }),
    ]);
    const store = new DownloadsStore(api);
    cleanup.push(() => store.dispose());
    await store.start();
    const utils = render(DownloadTable, { props: { store, onopen: vi.fn(), onmenu: vi.fn(), onadd: vi.fn() } });
    const cell = (id: number, key: string) => utils.container.querySelector(`#dl-cell-${id}-${key}`);
    expect(cell(1, 'name')?.querySelector('.media')?.textContent?.trim()).toBe('HLS');
    expect(cell(2, 'name')?.querySelector('.media')?.textContent?.trim()).toBe('DASH');
    expect(cell(3, 'name')?.querySelector('.media')).toBeNull();
    expect(document.getElementById('dl-desc-1')?.textContent).toMatch(/^Size unknown, category Video, added .+, HLS stream$/);
    expect(document.getElementById('dl-desc-3')?.textContent).not.toContain('stream');
    expect(cell(1, 'progress')?.textContent).toContain('30%');
  });

  it('shows a stream’s estimated size with "~", and its time left', async () => {
    const api = fakeApi([
      makeMediaDownload({ id: 1, state: 'downloading', size: 2100 * MiB, sizeApprox: true, received: 700 * MiB, speedBps: 7 * MiB,
        media: makeMediaInfo({ fragmentsDone: 50 }) }),
      makeMediaDownload({ id: 2, state: 'completed', size: 1900 * MiB, received: 1900 * MiB, media: makeMediaInfo({ fragmentsDone: 200 }) }),
    ]);
    const store = new DownloadsStore(api);
    cleanup.push(() => store.dispose());
    await store.start();
    const utils = render(DownloadTable, { props: { store, onopen: vi.fn(), onmenu: vi.fn(), onadd: vi.fn() } });
    const cell = (id: number, key: string) => utils.container.querySelector(`#dl-cell-${id}-${key}`);
    expect(cell(1, 'size')?.textContent?.trim()).toBe('~2.2 GB');
    expect(cell(1, 'eta')?.textContent?.trim()).toBe('3m 20s');
    expect(cell(2, 'size')?.textContent?.trim()).toBe('2.0 GB');
    expect(document.getElementById('dl-desc-1')?.textContent).toMatch(/^Size about 2\.2 GB, category Video, added .+, HLS stream$/);
  });
});

describe('DownloadTable: density (DESIGN-SYSTEM.md §2.3, §3.11)', () => {
  it('sizes rows from the root font: Comfortable 46 px, Compact 24 px at 16 px', () => {
    expect(rowHeightFor(16, false)).toBe(46);
    expect(rowHeightFor(16, true)).toBe(24);
    expect(rowHeightFor(12, false)).toBe(44);
    expect(rowHeightFor(12, true)).toBe(24);
    expect(rowHeightFor(20, false)).toBe(58);
    expect(rowHeightFor(20, true)).toBe(30);
  });

  it('follows the density store: two lines and 26 px tiles, or one line with the host in the tooltip', async () => {
    const d = makeDownload({ id: 1, url: 'https://www.example.com/pub/plan.xlsx', filename: 'plan.xlsx', state: 'downloading', received: MiB, speedBps: MiB });
    const { grid, cell, container } = await table([d, makeDownload({ id: 2 })]);
    expect(grid.style.getPropertyValue('--row-h')).toBe('46px');
    expect(cell(1, 'name')?.querySelector('.sub')?.textContent).toBe('example.com · Documents');
    expect(cell(1, 'name')?.querySelector('svg.tile')?.getAttribute('width')).toBe('26');
    expect(container.querySelector('[role="row"][aria-rowindex="3"]')?.getAttribute('style')).toContain('translateY(46px)');

    density.set('compact');
    await tick();
    expect(localStorage.getItem(DENSITY_KEY)).toBe('compact');
    expect(document.documentElement.dataset.density).toBe('compact');
    expect(grid.style.getPropertyValue('--row-h')).toBe('24px');
    expect(container.querySelector('[role="row"][aria-rowindex="3"]')?.getAttribute('style')).toContain('translateY(24px)');
    expect(cell(1, 'name')?.querySelector('.sub')).toBeNull();
    expect(cell(1, 'name')?.querySelector('svg.tile')?.getAttribute('width')).toBe('16');
    expect(document.getElementById('dl-name-1')?.getAttribute('title')).toBe('plan.xlsx\nexample.com · Documents');
  });

  it('keeps the header at 30 px', async () => {
    const { container } = await table([makeDownload({ id: 1 })]);
    expect(container.querySelector<HTMLElement>('.wrap')?.style.getPropertyValue('--header-h')).toBe('30px');
  });
});

describe('DownloadTable: cells by state (DESIGN-SYSTEM.md §3.12)', () => {
  it('shows a segmented bar of at most 8 cells with the percentage for an active row', async () => {
    const segments = Array.from({ length: 16 }, (_, i) => ({ start: i * 4 * MiB, end: (i + 1) * 4 * MiB - 1, received: 2 * MiB }));
    const { cell } = await table([
      makeDownload({ id: 1, state: 'downloading', size: 64 * MiB, received: 32 * MiB, speedBps: MiB, connections: 16, segments }),
    ]);
    const bar = cell(1, 'progress')?.querySelector('.segbar');
    expect(bar?.getAttribute('aria-hidden')).toBe('true');
    expect(Number(bar?.getAttribute('data-cells'))).toBeLessThanOrEqual(8);
    expect(cell(1, 'progress')?.querySelector('.pct')?.textContent).toBe('50%');
    expect(cell(1, 'connections')?.textContent?.trim()).toBe('16');
  });

  it('shows a completed row without a bar and only "Completed" (the date is in Added)', async () => {
    const { cell } = await table([makeDownload({ id: 1, state: 'completed', received: 10 * MiB, completedAt: 1_700_000_500_000 })]);
    expect(cell(1, 'progress')?.querySelector('.segbar')).toBeNull();
    expect(cell(1, 'progress')?.textContent?.trim()).toBe('');
    expect(cell(1, 'status')?.textContent?.trim()).toBe('Completed');
    expect(document.getElementById('dl-status-1')?.textContent).toBe('Completed');
    expect(cell(1, 'added')?.textContent?.trim()).not.toBe('');
  });

  it('keeps earlier progress of a queued row in grey, and none when it has none', async () => {
    const { cell } = await table([
      makeDownload({ id: 1, state: 'queued', received: 3 * MiB, queuePosition: 1 }),
      makeDownload({ id: 2, state: 'queued', queuePosition: 2 }),
    ]);
    expect(cell(1, 'progress')?.querySelector('.segbar')?.classList.contains('tone-idle')).toBe(true);
    expect(cell(1, 'progress')?.querySelector('.pct')?.textContent).toBe('30%');
    expect(cell(2, 'progress')?.querySelector('.segbar')).toBeNull();
    // "Queued #2" with the position set apart, read as one text
    expect(document.getElementById('dl-status-2')?.textContent).toBe('Queued #2');
    expect(cell(2, 'status')?.querySelector('.pos')?.textContent).toBe('#2');
  });

  it('offers Retry only on failures that retrying can help, and Restart… when the file changed', async () => {
    const { cell, onaction, store } = await table([
      makeDownload({ id: 1, state: 'failed', received: MiB, errorCode: 'http_status', error: 'HTTP 503 Service Unavailable' }),
      makeDownload({ id: 2, state: 'failed', received: MiB, errorCode: 'validator_changed' }),
      makeMediaDownload({ id: 3, state: 'failed', errorCode: 'encrypted' }),
      makeDownload({ id: 4, state: 'paused', received: MiB }),
    ]);
    const retry = cell(1, 'status')?.querySelector<HTMLButtonElement>('.inline-act');
    expect(retry?.textContent?.trim()).toBe('Retry');
    expect(retry?.hasAttribute('data-row-action')).toBe(true);
    expect(retry?.getAttribute('aria-hidden')).toBe('true');
    expect(retry?.tabIndex).toBe(-1);
    // the reason stays in the cell's tooltip; the status text stays the state
    expect(cell(1, 'status')?.getAttribute('title')).toBe('HTTP 503 Service Unavailable');
    expect(document.getElementById('dl-status-1')?.textContent).toBe('Failed');
    await fireEvent.click(retry!);
    expect(onaction).toHaveBeenLastCalledWith('resume', store.get(1));
    // a click on it is not a click on the row
    expect(store.selection.ids.size).toBe(0);

    const restart = cell(2, 'status')?.querySelector<HTMLButtonElement>('.inline-act');
    expect(restart?.textContent?.trim()).toBe('Restart…');
    await fireEvent.click(restart!);
    expect(onaction).toHaveBeenLastCalledWith('restart', store.get(2));

    expect(cell(3, 'status')?.querySelector('.inline-act')).toBeNull();
    expect(cell(4, 'status')?.querySelector('.inline-act')).toBeNull();
    expect(inlineAction({ state: 'failed', errorCode: 'site_unsupported' })).toBeNull();
    expect(inlineAction({ state: 'failed', errorCode: 'network' })).toEqual({ id: 'resume', label: 'Retry' });
  });

  it('offers no inline actions while downexd is unreachable', async () => {
    const { cell, store } = await table([makeDownload({ id: 1, state: 'failed', errorCode: 'network', received: MiB })]);
    store.daemon = { ok: false, error: 'connection refused' };
    await tick();
    expect(cell(1, 'status')?.querySelector('.inline-act')).toBeNull();
    expect(document.querySelector('[data-row-action]')).toBeNull();
  });

  it('offers Review › for a download to confirm', async () => {
    const { cell, onaction, store } = await table([makeDownload({ id: 1, state: 'awaiting_confirmation', size: -1, expectedSize: 2 * MiB })]);
    const review = cell(1, 'progress')?.querySelector<HTMLButtonElement>('.review');
    expect(review?.textContent?.trim()).toBe('Review');
    expect(review?.getAttribute('aria-hidden')).toBe('true');
    expect(review?.tabIndex).toBe(-1);
    await fireEvent.click(review!);
    expect(onaction).toHaveBeenCalledWith('confirm', store.get(1));
    // the size the browser announced, until it is probed
    expect(cell(1, 'size')?.textContent?.trim()).toBe('~2.1 MB');
    expect(document.getElementById('dl-status-1')?.textContent).toBe('Waiting for confirmation');
  });

  it('maps every state to its progress cell', () => {
    expect(progressCell({ state: 'downloading', received: 0 })).toBe('bar');
    expect(progressCell({ state: 'paused', received: 0 })).toBe('bar');
    expect(progressCell({ state: 'probing', received: 0 })).toBe('probe');
    expect(progressCell({ state: 'failed', received: 0 })).toBe('none');
    expect(progressCell({ state: 'failed', received: 1 })).toBe('bar');
    expect(progressCell({ state: 'awaiting_confirmation', received: 0 })).toBe('review');
    expect(progressCell({ state: 'completed', received: 9 })).toBe('none');
    expect(progressCell({ state: 'canceled', received: 9 })).toBe('none');
  });

  it('keeps the hover actions with their titles', async () => {
    const { container } = await table([makeDownload({ id: 1, state: 'downloading', received: MiB, speedBps: MiB })]);
    const titles = [...container.querySelectorAll('#dl-row-1 [data-row-action] button')].map((b) => b.getAttribute('title'));
    expect(titles).toEqual(['Pause', 'Remove…']);
  });
});

describe('DownloadTable: empty states (DESIGN-SYSTEM.md §3.18)', () => {
  it('shows how to add a download, and where browser downloads come from', async () => {
    const onadd = vi.fn();
    const onaddvideo = vi.fn();
    const onextension = vi.fn();
    const { getByRole, getByText } = await table([], { onadd, onaddvideo, onextension });
    expect(getByRole('heading', { name: 'No downloads yet' })).toBeTruthy();
    expect(getByText(/Paste a link anywhere in this window \(Ctrl\+V\)/)).toBeTruthy();
    await fireEvent.click(getByRole('button', { name: 'Add download' }));
    await fireEvent.click(getByRole('button', { name: 'Add video or stream' }));
    await fireEvent.click(getByRole('button', { name: 'Get the browser extension' }));
    expect(onadd).toHaveBeenCalledTimes(1);
    expect(onaddvideo).toHaveBeenCalledTimes(1);
    expect(onextension).toHaveBeenCalledTimes(1);
  });

  it('leaves out the actions it is not given, and disables adding while downexd is unreachable', async () => {
    const { queryByRole, getByRole, store } = await table([]);
    expect(queryByRole('button', { name: 'Add video or stream' })).toBeNull();
    expect(queryByRole('button', { name: 'Get the browser extension' })).toBeNull();
    store.daemon = { ok: false, error: 'connection refused' };
    await tick();
    expect((getByRole('button', { name: 'Add download' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('says what matched nothing and clears the filters', async () => {
    const onclearfilters = vi.fn();
    const { getByRole, store } = await table([makeDownload({ id: 1, filename: 'plan.xlsx' })], { onclearfilters });
    store.setSearch('quarterly');
    await tick();
    expect(getByRole('heading').textContent).toMatch(/^No downloads match “.?quarterly.?”$/);
    await fireEvent.click(getByRole('button', { name: 'Clear filters' }));
    expect(onclearfilters).toHaveBeenCalledTimes(1);

    store.setSearch('');
    store.setStatusFilter('failed');
    await tick();
    expect(getByRole('heading').textContent).toBe('Nothing in Failed');
  });
});
