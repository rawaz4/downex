import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { tick } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import ProgressWindow from './ProgressWindow.svelte';
import { DEFAULT_SETTINGS, type Download, type Settings } from './lib/api';
import { DETAILS_STORAGE_KEY, PROGRESS_WINDOW } from './lib/progress';
import {
  KB,
  KiB,
  MB,
  MiB,
  PAGE,
  REFUSED,
  fakeApi,
  makeConnection,
  makeDownload,
  makeMediaDownload,
  makeMediaInfo,
  makeSiteDownload,
  makeSiteMediaInfo,
  chooseOption,
  type FakeApi,
} from './lib/testing';

afterEach(() => {
  vi.useRealTimers();
  localStorage.clear();
});

const SETTINGS: Settings = { ...DEFAULT_SETTINGS, defaultDir: '/home/you/Downloads', defaultMaxConnections: 8 };

function running(partial: Partial<Download> = {}): Download {
  return makeDownload({
    id: 7,
    url: 'https://downloads.example.com/releases/2026/very/long/path/to/the/file/distro-installer.iso',
    filename: 'distro-installer.iso',
    dir: '/home/you/Downloads/Archives',
    state: 'downloading',
    mode: 'ranged',
    size: 100 * MiB,
    received: 25 * MiB,
    speedBps: 5 * MiB,
    connections: 2,
    segments: [
      { start: 0, end: 50 * MiB - 1, received: 15 * MiB },
      { start: 50 * MiB, end: 100 * MiB - 1, received: 10 * MiB },
    ],
    connectionInfo: [
      makeConnection({ index: 1, start: 0, end: 50 * MiB - 1, received: 15 * MiB, speedBps: 3 * MiB }),
      makeConnection({ index: 2, state: 'retrying', start: 50 * MiB, end: 100 * MiB - 1, received: 10 * MiB, detail: 'HTTP 503, retrying in 4 s' }),
    ],
    ...partial,
  });
}

function show(d: Download | null = running(), settings: Settings = SETTINGS, props: { statusMs?: number } = {}) {
  const api = fakeApi(d ? [d] : [], settings);
  render(ProgressWindow, { props: { api, id: d?.id ?? 7, statusMs: props.statusMs ?? 60_000, applyDelayMs: 50 } });
  return api;
}

/** The value of a status row ("Downloaded", …). */
function row(term: string): string | null {
  const dt = screen.getAllByText(term).find((el) => el.tagName === 'DT');
  return dt?.nextElementSibling?.textContent?.replace(/\s+/g, ' ').trim() ?? null;
}

/** The header's state line (the mark and progressStatus; DESIGN-SYSTEM.md §4.8). */
function stateLine(): string | null {
  return document.querySelector('[data-progress-state]')?.textContent?.replace(/\s+/g, ' ').trim() ?? null;
}

/** The header's big percentage ("" when it shows none). */
function headerFigure(): HTMLElement | null {
  return document.querySelector<HTMLElement>('header.head .figure');
}

/** "15s · Done ≈ 11:59": the time left and when it should be done, on the locale clock. */
const doneBy = (left: string) => new RegExp(`^${left} · Done ≈ \\d{1,2}:\\d{2}`);

function update(api: FakeApi, id: number, patch: Partial<Download>): Download {
  const d = { ...api.items.get(id)!, ...patch };
  d satisfies Download;
  const next = { ...d, updatedAt: d.updatedAt + 1000 };
  api.items.set(id, next);
  api.emit([{ kind: 'updated', download: next }]);
  return next;
}

describe('ProgressWindow: status', () => {
  it('shows the live state of its download and titles the window "<percent>% <name>"', async () => {
    const api = show();
    expect(await screen.findByText('Receiving data…')).toBeTruthy();
    expect(api.win.title).toBe('25% distro-installer.iso');
    expect(row('File size')).toBe('105 MB');
    expect(row('Downloaded')).toBe('26.2 MB (25%)');
    expect(row('Transfer rate')).toBe('5.2 MB/s');
    expect(row('Time left')).toMatch(doneBy('15s'));
    expect(row('Resumable')).toBe('Yes');
    // The address keeps its end in view (middle ellipsis) and can be copied.
    expect(row('Address')).toBe('https://downloads.example.com/releases/2026/very/long/path/to/the/file/distro-installer.iso');
    expect(screen.getByRole('progressbar', { name: 'Download progress' }).getAttribute('aria-valuenow')).toBe('25');
    expect(api.calls).toContain('get');

    update(api, 7, { received: 60 * MiB });
    await tick();
    expect(api.win.title).toBe('60% distro-installer.iso');
    expect(row('Downloaded')).toBe('62.9 MB (60%)');
    // Another download's events do not matter here.
    api.emit([{ kind: 'updated', download: makeDownload({ id: 8, state: 'completed' }) }]);
    api.emit([{ kind: 'removed', id: 8 }]);
    await tick();
    expect(api.win.closed).toBe(0);
    expect(row('Downloaded')).toBe('62.9 MB (60%)');
  });

  it('shows the average speed of the whole download beside the live rate, once it has run a few seconds', async () => {
    const api = show(running({ activeMs: 1500 }));
    await screen.findByText('Receiving data…');
    // its first seconds: no average yet
    expect(row('Transfer rate')).toBe('5.2 MB/s');
    update(api, 7, { received: 40 * MiB, activeMs: 5000 });
    await tick();
    expect(row('Transfer rate')).toBe('5.2 MB/s · avg 8.4 MB/s');
    // cut in a narrow window with larger text: whole in its tooltip
    const rate = screen.getAllByText('Transfer rate').find((el) => el.tagName === 'DT')?.nextElementSibling;
    expect(rate?.getAttribute('title')).toBe('5.2 MB/s · avg 8.4 MB/s');
    update(api, 7, { state: 'paused', speedBps: 0 });
    await tick();
    expect(row('Transfer rate')).toBe('—');
  });

  it('shows only the name while the size is unknown, with an indeterminate bar', async () => {
    const api = show(running({ size: -1, mode: 'single', segments: [], connectionInfo: [] }));
    await screen.findByText('Receiving data…');
    expect(api.win.title).toBe('distro-installer.iso');
    expect(row('File size')).toBe('Unknown');
    expect(row('Resumable')).toBe('No');
    expect(screen.getByRole('progressbar').getAttribute('aria-valuetext')).toBe('Size unknown');
  });

  it('pauses and resumes; Cancel pauses and closes the window', async () => {
    const api = show();
    await fireEvent.click(await screen.findByRole('button', { name: 'Pause' }));
    await waitFor(() => expect(stateLine()).toBe('Paused'));
    expect(api.calls).toContain('pause');
    await fireEvent.click(screen.getByRole('button', { name: 'Resume' }));
    await waitFor(() => expect(stateLine()).toBe('Waiting in queue'));
    expect(api.args.get('resume')).toEqual([7, {}]);

    await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(api.win.closed).toBe(1));
    expect(api.calls.filter((c) => c === 'pause')).toHaveLength(2);
    expect(api.items.get(7)?.state).toBe('paused');
  });

  it('closes without pausing what is not running', async () => {
    const api = show(running({ state: 'paused', speedBps: 0 }));
    await fireEvent.click(await screen.findByRole('button', { name: 'Close' }));
    expect(api.win.closed).toBe(1);
    expect(api.calls).not.toContain('pause');
  });

  it('closes when its download is removed', async () => {
    const api = show();
    await screen.findByText('Receiving data…');
    api.emit([{ kind: 'removed', id: 7 }]);
    await tick();
    expect(api.win.closed).toBe(1);
  });

  it('says so when the download does not exist', async () => {
    const api = show(null);
    expect(await screen.findByRole('heading', { name: 'This download no longer exists' })).toBeTruthy();
    // The host reports a download it cannot watch as removed: the window stays to say why.
    api.emit([{ kind: 'removed', id: 7 }]);
    await tick();
    expect(api.win.closed).toBe(0);
    await fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    expect(api.win.closed).toBe(1);
  });

  it('shows a stream with its fragments, quality and the fragment of each connection', async () => {
    show(
      makeMediaDownload({
        id: 7,
        state: 'downloading',
        received: 40 * MiB,
        speedBps: 2 * MiB,
        media: makeMediaInfo({ fragmentsDone: 50 }),
        connectionInfo: [makeConnection({ index: 1, fragment: 50, received: 300 * KiB }), makeConnection({ index: 2, fragment: 51, state: 'requesting' })],
      }),
    );
    await screen.findByText('Receiving data…');
    expect(row('Manifest')).toBe('https://media.example.org/talk/master.m3u8');
    expect(row('Fragments')).toBe('50 / 200');
    // The human codec text; the picture and the raw codecs are its tooltip.
    expect(row('Quality')).toBe('1080p · H.264 · AAC 128 kbit/s');
    expect(screen.getAllByText('Quality').find((el) => el.tagName === 'DT')?.nextElementSibling?.getAttribute('title')).toMatch(
      /1920×1080.*English/,
    );
    await fireEvent.click(screen.getByRole('button', { name: 'Show details' }));
    const table = screen.getByRole('table');
    expect(within(table).getByText('Fragment 51 · Receiving data…')).toBeTruthy();
    expect(within(table).getByText('Fragment 52 · Sending request…')).toBeTruthy();
    expect(screen.getByRole('progressbar', { name: 'Fragments downloaded' })).toBeTruthy();
  });

  it('shows a stream’s estimated size with "~" and the time left from it, instead of "Unknown"', async () => {
    const api = show(
      makeMediaDownload({
        id: 7,
        state: 'downloading',
        size: 2100 * MiB,
        sizeApprox: true,
        received: 700 * MiB,
        speedBps: 7 * MiB,
        media: makeMediaInfo({ fragmentsDone: 50 }),
      }),
    );
    await screen.findByText('Receiving data…');
    expect(row('File size')).toBe('~2.2 GB');
    expect(row('Downloaded')).toBe('734 MB (25%)');
    expect(row('Time left')).toMatch(doneBy('3m 20s'));
    // The header's percentage says it is an estimate.
    expect(headerFigure()?.textContent).toBe('~25%');
    update(api, 7, { size: 2000 * MiB, received: 1000 * MiB, media: makeMediaInfo({ fragmentsDone: 100 }) });
    await waitFor(() => expect(row('File size')).toBe('~2.1 GB'));
    expect(row('Time left')).toMatch(doneBy('2m 23s'));
  });

  it('reports an unreachable downexd and reads the download again when it is back', async () => {
    vi.useFakeTimers();
    const api = show(running(), SETTINGS, { statusMs: 1000 });
    await vi.advanceTimersByTimeAsync(0);
    await tick();
    expect(stateLine()).toBe('Receiving data…');
    api.down = true;
    await vi.advanceTimersByTimeAsync(1000);
    await tick();
    expect(screen.getByText(/downexd is not reachable/)).toBeTruthy();
    const calls = api.calls.length;
    await fireEvent.click(screen.getByRole('button', { name: 'Pause' }));
    expect(api.calls.slice(calls).filter((c) => c !== 'status')).toEqual([]);

    api.down = false;
    api.items.set(7, { ...api.items.get(7)!, received: 80 * MiB, updatedAt: api.items.get(7)!.updatedAt + 5000 });
    await vi.advanceTimersByTimeAsync(1000);
    await tick();
    expect(screen.queryByText(/downexd is not reachable/)).toBeNull();
    expect(row('Downloaded')).toBe('83.9 MB (80%)');
  });
});

describe('ProgressWindow: header (DESIGN-SYSTEM.md §4.8)', () => {
  it('shows the file tile, the name, the state and the big percentage in the state colours', async () => {
    const api = show();
    await screen.findByText('Receiving data…');
    const head = document.querySelector('header.head')!;
    expect(head.querySelector('svg.tile')?.getAttribute('data-kind')).toBe('archives'); // an .iso
    expect(within(head as HTMLElement).getByText('distro-installer.iso')).toBeTruthy();
    expect(stateLine()).toBe('Receiving data…');
    expect(headerFigure()?.textContent).toBe('25%');
    expect(headerFigure()?.className).not.toMatch(/warn|danger/);
    // The bar is the segmented one (decorative); the progressbar around it carries the value.
    const bar = screen.getByRole('progressbar', { name: 'Download progress' });
    expect(bar.querySelector('.segbar')?.getAttribute('aria-hidden')).toBe('true');

    update(api, 7, { state: 'paused', speedBps: 0 });
    await tick();
    expect(stateLine()).toBe('Paused');
    expect(headerFigure()?.classList.contains('warn')).toBe(true);
    // A paused download has no time left, so nothing says when it is done.
    expect(row('Time left')).toBe('—');

    update(api, 7, { state: 'failed', error: 'HTTP 503 Service Unavailable', errorCode: 'http_status' });
    await tick();
    expect(stateLine()).toBe('Failed: HTTP 503 Service Unavailable');
    expect(headerFigure()?.classList.contains('danger')).toBe(true);

    // Unknown size: no percentage at all.
    update(api, 7, { state: 'downloading', size: -1, mode: 'single', segments: [] });
    await tick();
    expect(headerFigure()).toBeNull();
  });

  it('names a video download by its tile and says when a running download is done', async () => {
    show(
      makeMediaDownload({
        id: 7,
        state: 'downloading',
        size: 400 * MiB,
        received: 100 * MiB,
        speedBps: 10 * MiB,
        media: makeMediaInfo({ fragmentsDone: 50 }),
      }),
    );
    await screen.findByText('Receiving data…');
    expect(document.querySelector('header.head svg.tile')?.getAttribute('data-kind')).toBe('video');
    expect(row('Time left')).toMatch(doneBy('30s'));
  });

  it('marks the Speed limit tab while a limit is on, and the tabs control one panel', async () => {
    const api = show();
    const limits = await screen.findByRole('tab', { name: 'Speed limit' });
    // the Mint dot after the label (drawn, not read out)
    expect(limits.querySelector('.dot')).toBeNull();
    const panel = screen.getByRole('tabpanel');
    expect(limits.getAttribute('aria-controls')).toBe(panel.id);
    expect(panel.getAttribute('aria-labelledby')).toBe(screen.getByRole('tab', { name: 'Status' }).id);
    update(api, 7, { speedLimit: 512 * KiB });
    await tick();
    expect(limits.querySelector('.dot')?.getAttribute('aria-hidden')).toBe('true');
    // the name stays "Speed limit"
    expect(screen.getByRole('tab', { name: 'Speed limit' })).toBe(limits);
  });

  it('chooses the speed limit unit from the keyboard', async () => {
    const api = show(running({ speedLimit: 300 * KB }));
    await fireEvent.click(await screen.findByRole('tab', { name: 'Speed limit' }));
    const unit = screen.getByRole('combobox', { name: 'Speed limit unit' }) as HTMLButtonElement;
    expect(unit.value).toBe('KB/s');
    unit.focus();
    await fireEvent.keyDown(unit, { key: 'ArrowDown' });
    expect(unit.getAttribute('aria-expanded')).toBe('true');
    await fireEvent.keyDown(unit, { key: 'ArrowDown' });
    await fireEvent.keyDown(unit, { key: 'Enter' });
    expect(unit.value).toBe('MB/s');
    await waitFor(() => expect(api.args.get('setDownloadOptions')).toEqual([7, { speedLimit: 300 * MB }]));
  });
});

describe('ProgressWindow: settings', () => {
  const placeholder = () => (screen.getByLabelText('Connections') as HTMLInputElement).placeholder;

  it('reads the settings that failed at first, and again when downexd is back', async () => {
    vi.useFakeTimers();
    const api = fakeApi([running()], SETTINGS);
    const getSettings = api.getSettings;
    let failures = 1;
    api.getSettings = () => (failures-- > 0 ? Promise.reject(new Error(REFUSED)) : getSettings());
    render(ProgressWindow, { props: { api, id: 7, statusMs: 1000, applyDelayMs: 50 } });
    await vi.advanceTimersByTimeAsync(0);
    await tick();
    await fireEvent.click(screen.getByRole('tab', { name: 'Speed limit' }));
    expect(placeholder()).toBe('Default');
    await vi.advanceTimersByTimeAsync(1000);
    await tick();
    expect(placeholder()).toBe('Default (8)');

    api.down = true;
    await vi.advanceTimersByTimeAsync(1000);
    await tick();
    expect(screen.getByText(/downexd is not reachable/)).toBeTruthy();
    api.settings = { ...SETTINGS, defaultMaxConnections: 4 };
    api.down = false;
    await vi.advanceTimersByTimeAsync(1000);
    await tick();
    expect(screen.queryByText(/downexd is not reachable/)).toBeNull();
    expect(placeholder()).toBe('Default (4)');
  });

  it('reads the settings again when the window gets the focus', async () => {
    const api = show();
    await fireEvent.click(await screen.findByRole('tab', { name: 'Speed limit' }));
    await waitFor(() => expect(placeholder()).toBe('Default (8)'));
    api.settings = { ...SETTINGS, defaultMaxConnections: 12 };
    await fireEvent.focus(window);
    await waitFor(() => expect(placeholder()).toBe('Default (12)'));
  });
});

describe('ProgressWindow: the window fits what it shows (no needless scrollbar)', () => {
  const realInner = window.innerHeight;
  let observers: ResizeObserverCallback[] = [];
  let observed: Element[] = [];

  afterEach(() => {
    Object.defineProperty(window, 'innerHeight', { value: realInner, configurable: true, writable: true });
    vi.unstubAllGlobals();
    observers = [];
    observed = [];
  });

  /**
   * A layout for jsdom (which has none): the page's blocks get these heights (by class), and the
   * content of its scrolling part (the completion view, the progress panel) spans `content` px.
   * The page is 261 px tall in a 300 px window: a 39 px frame.
   */
  function layout(blocks: Record<string, number>, content: { value: number }) {
    Object.defineProperty(window, 'innerHeight', { value: 261, configurable: true, writable: true });
    vi.stubGlobal(
      'ResizeObserver',
      class {
        constructor(cb: ResizeObserverCallback) {
          observers.push(cb);
        }
        observe(el: Element) {
          observed.push(el);
        }
        unobserve() {}
        disconnect() {}
      },
    );
    const rect = (top: number, height: number) => ({ top, bottom: top + height, height, left: 0, right: 488, width: 488, x: 0, y: top, toJSON() {} }) as DOMRect;
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      const parent = this.parentElement;
      if (parent?.classList.contains('complete') || parent?.classList.contains('panel')) {
        // the scrolling content spans [12.25, 12.25 + content]
        if (parent.childElementCount === 1) return rect(12.25, content.value);
        if (this === parent.firstElementChild) return rect(12.25, 20);
        if (this === parent.lastElementChild) return rect(12.25 + content.value - 20, 20);
        return rect(40, 10);
      }
      if (parent?.classList.contains('pw')) {
        const key = Object.keys(blocks).find((k) => this.classList.contains(k));
        return rect(0, key ? blocks[key]! : 0);
      }
      return rect(0, 0);
    });
  }

  const refit = async () => {
    for (const cb of observers) cb([], {} as ResizeObserver);
    await new Promise((r) => setTimeout(r, 20));
  };

  /** Like show(), with a host that resizes the page with the window (the frame stays 39 px). */
  function showFitted(d: Download) {
    const api = fakeApi([d], SETTINGS);
    const set = api.window.setSize.bind(api.window);
    api.window.setSize = (width, height) => {
      set(width, height);
      Object.defineProperty(window, 'innerHeight', { value: height - 39, configurable: true, writable: true });
    };
    render(ProgressWindow, { props: { api, id: d.id, statusMs: 60_000, applyDelayMs: 50 } });
    return api;
  }

  it('sizes the complete window to its fractional content plus the frame and slack, and again when the content changes', async () => {
    const content = { value: 254.5 };
    layout({}, content);
    const api = showFitted(running({ state: 'completed', received: 100 * MiB, connectionInfo: [] }));
    await screen.findByRole('heading', { name: 'Download complete' });
    // 254.5 px of content in a window with a 39 px frame: never a pixel short (it scrolled by 1 at 125 %)
    const want = Math.ceil(254.5 + 39 + PROGRESS_WINDOW.fitSlack);
    await waitFor(() => expect(api.win.sizes.at(-1)).toEqual({ width: 520, height: want }));
    expect(want).toBeGreaterThan(254.5 + 39 + 1.9);
    // it watches the view's content, and fits again when it grows (a message, a larger font, a narrower window)
    expect(observed).toContain(document.querySelector('section.complete > header'));
    expect(observed).not.toContain(document.querySelector('section.complete'));
    content.value = 300.75;
    await refit();
    expect(api.win.sizes.at(-1)).toEqual({ width: 520, height: Math.ceil(300.75 + 39 + PROGRESS_WINDOW.fitSlack) });
  });

  it('keeps the frame it measured first: a host that reports the new size before the page has it does not move the window', async () => {
    const content = { value: 254.5 };
    layout({}, content);
    // this host resizes the page only later
    const api = show(running({ state: 'completed', received: 100 * MiB, connectionInfo: [] }));
    await screen.findByRole('heading', { name: 'Download complete' });
    const want = Math.ceil(254.5 + 39 + PROGRESS_WINDOW.fitSlack);
    await waitFor(() => expect(api.win.sizes).toEqual([{ width: 520, height: want }]));
    // the page is still 261 px tall in a window the host says is `want` tall: not a taller frame
    await refit();
    await refit();
    expect(api.win.sizes).toEqual([{ width: 520, height: want }]);
  });

  it('fits the collapsed progress window to its facts, never below its usual height nor smaller for the Speed limit tab', async () => {
    const content = { value: 107.5 };
    // header 44, bar 8, tabs 28, actions 30: with the panel's 107.5 px, 217.5 px (the page has no padding in jsdom)
    layout({ head: 44, bar: 8, tabs: 28, actions: 30 }, content);
    const api = showFitted(running());
    await screen.findByText('Receiving data…');
    const want = Math.ceil(44 + 8 + 28 + 30 + 107.5 + 39 + PROGRESS_WINDOW.fitSlack);
    expect(want).toBeLessThanOrEqual(PROGRESS_WINDOW.collapsedHeight);
    await new Promise((r) => setTimeout(r, 20));
    // it fits the usual 300 px window: nothing to do
    expect(api.win.sizes).toEqual([]);

    content.value = 180.5;
    await refit();
    const grown = Math.ceil(44 + 8 + 28 + 30 + 180.5 + 39 + PROGRESS_WINDOW.fitSlack);
    expect(api.win.sizes.at(-1)).toEqual({ width: 520, height: grown });
    // the panel's content and the blocks around it, not the panel or the page (the user may resize the window)
    expect(observed).toContain(screen.getByRole('tabpanel'));
    expect(observed).toContain(document.querySelector('header.head'));
    expect(observed).not.toContain(document.querySelector('.panel'));
    expect(observed).not.toContain(document.querySelector('.pw'));

    // the shorter Speed limit tab does not shrink it (no jump between the tabs)
    content.value = 60;
    await fireEvent.click(screen.getByRole('tab', { name: 'Speed limit' }));
    await refit();
    expect(api.win.sizes.at(-1)).toEqual({ width: 520, height: grown });

    // back on Status with fewer facts (a row went away): it fits them again, at least the usual height
    content.value = 120;
    await fireEvent.click(screen.getByRole('tab', { name: 'Status' }));
    await refit();
    expect(api.win.sizes.at(-1)).toEqual({ width: 520, height: PROGRESS_WINDOW.collapsedHeight });
  });
});

describe('ProgressWindow: details', () => {
  it('shows and hides the connections, resizing the window and remembering the choice', async () => {
    const api = show();
    await fireEvent.click(await screen.findByRole('button', { name: 'Show details' }));
    await waitFor(() => expect(api.win.sizes.at(-1)).toEqual({ width: 520, height: 560 }));
    expect(localStorage.getItem(DETAILS_STORAGE_KEY)).toBe('1');
    expect(screen.getByRole('heading', { name: 'Start positions and progress by connection' })).toBeTruthy();
    const rows = within(screen.getByRole('table')).getAllByRole('row');
    // Each connection with its own speed (a waiting one has none).
    expect(rows.map((r) => [...r.children].map((c) => c.textContent?.trim()).join(' '))).toEqual([
      'N. Downloaded Speed Info',
      '1 15.7 MB 3.1 MB/s Receiving data…',
      '2 10.5 MB — Retrying in 4 s (HTTP 503)',
    ]);
    expect(screen.getByRole('img', { name: /Segment map: 2 segments, 2 active, 25% received/ })).toBeTruthy();

    // Connections come and go with the updates.
    update(api, 7, { connectionInfo: [makeConnection({ index: 1, state: 'done', received: 50 * MiB })] });
    await tick();
    expect(within(screen.getByRole('table')).getByText('Finished')).toBeTruthy();

    await fireEvent.click(screen.getByRole('button', { name: 'Hide details' }));
    await waitFor(() => expect(api.win.sizes.at(-1)).toEqual({ width: 520, height: 300 }));
    expect(screen.queryByRole('table')).toBeNull();
    expect(localStorage.getItem(DETAILS_STORAGE_KEY)).toBe('0');
  });

  it('opens with the details when they were shown last time, keeping a wider window wide', async () => {
    localStorage.setItem(DETAILS_STORAGE_KEY, '1');
    const api = fakeApi([running()], SETTINGS);
    api.win.size = { width: 700, height: 300 };
    render(ProgressWindow, { props: { api, id: 7, statusMs: 60_000 } });
    await waitFor(() => expect(api.win.sizes).toEqual([{ width: 700, height: 560 }]));
    expect(await screen.findByRole('table')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Hide details' }).getAttribute('aria-expanded')).toBe('true');
  });

  it('shows no details for a download that does not exist or is complete, whatever was remembered', async () => {
    localStorage.setItem(DETAILS_STORAGE_KEY, '1');
    const missing = show(null);
    await screen.findByRole('heading', { name: 'This download no longer exists' });
    const done = fakeApi([running({ id: 8, state: 'completed', received: 100 * MiB })], SETTINGS);
    render(ProgressWindow, { props: { api: done, id: 8, statusMs: 60_000 } });
    await screen.findByRole('heading', { name: 'Download complete' });
    await new Promise((r) => setTimeout(r, 20));
    expect(missing.win.sizes).toEqual([]);
    // Sized to the dialog (its default height where nothing can be measured), never to the details.
    expect(done.win.sizes).toEqual([{ width: 520, height: PROGRESS_WINDOW.completeHeight }]);
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('counts a retry wait down between reports and starts again from a new report', async () => {
    vi.useFakeTimers();
    localStorage.setItem(DETAILS_STORAGE_KEY, '1');
    const api = show(running(), SETTINGS, { statusMs: 60_000 });
    await vi.advanceTimersByTimeAsync(0);
    await tick();
    const info = () => within(screen.getByRole('table')).getAllByRole('row')[2]?.lastElementChild?.textContent?.trim();
    expect(info()).toBe('Retrying in 4 s (HTTP 503)');
    await vi.advanceTimersByTimeAsync(2000);
    await tick();
    expect(info()).toBe('Retrying in 2 s (HTTP 503)');
    await vi.advanceTimersByTimeAsync(5000);
    await tick();
    expect(info()).toBe('Retrying in 0 s (HTTP 503)');
    update(api, 7, {
      connectionInfo: [makeConnection({ index: 2, state: 'retrying', received: 10 * MiB, detail: 'HTTP 503, retrying in 8 s' })],
    });
    await tick();
    expect(within(screen.getByRole('table')).getAllByRole('row')[1]?.lastElementChild?.textContent?.trim()).toBe('Retrying in 8 s (HTTP 503)');
  });

  it('keeps the connections of the last update when an answer in between has none', async () => {
    const api = show();
    await fireEvent.click(await screen.findByRole('button', { name: 'Show details' }));
    // Only Watch updates carry connections; SetDownloadOptions answers without them.
    api.items.set(7, { ...api.items.get(7)!, connectionInfo: [] });
    await fireEvent.click(screen.getByRole('tab', { name: 'Speed limit' }));
    await fireEvent.click(screen.getByRole('switch', { name: /Limit this/ }));
    await waitFor(() => expect(api.calls).toContain('setDownloadOptions'));
    await tick();
    expect(within(screen.getByRole('table')).getAllByRole('row')).toHaveLength(3);
    // An update without connections empties the table.
    update(api, 7, { connectionInfo: [] });
    await tick();
    expect(within(screen.getByRole('table')).getByText('No connections yet.')).toBeTruthy();
  });
});

describe('ProgressWindow: speed limit', () => {
  it('applies the speed limit at once and shows it as active', async () => {
    const api = show();
    await fireEvent.click(await screen.findByRole('tab', { name: 'Speed limit' }));
    const box = screen.getByRole('switch', { name: /Limit this download’s speed/ });
    await fireEvent.click(box);
    await waitFor(() => expect(api.args.get('setDownloadOptions')).toEqual([7, { speedLimit: 500 * KB }]));

    const value = screen.getByRole('textbox', { name: 'Speed limit' });
    await fireEvent.input(value, { target: { value: '2' } });
    await chooseOption(screen.getByRole('combobox', { name: 'Speed limit unit' }), 'MB/s');
    await waitFor(() => expect(api.args.get('setDownloadOptions')).toEqual([7, { speedLimit: 2 * MB }]));

    await fireEvent.input(value, { target: { value: 'fast' } });
    await fireEvent.keyDown(value, { key: 'Enter' });
    expect(screen.getByRole('alert').textContent).toContain('Enter a number');
    await fireEvent.input(value, { target: { value: '1.5' } });
    await fireEvent.keyDown(value, { key: 'Enter' });
    await waitFor(() => expect(api.args.get('setDownloadOptions')).toEqual([7, { speedLimit: 1.5 * MB }]));

    await fireEvent.click(screen.getByRole('tab', { name: /Status/ }));
    expect(row('Speed limit')).toBe('1.5 MB/s for this download');

    await fireEvent.click(screen.getByRole('tab', { name: /Speed limit/ }));
    await fireEvent.click(screen.getByRole('switch', { name: /Limit this download’s speed/ }));
    await waitFor(() => expect(api.args.get('setDownloadOptions')).toEqual([7, { speedLimit: 0 }]));
    await fireEvent.click(screen.getByRole('tab', { name: 'Status' }));
    expect(screen.queryAllByText('Speed limit').filter((el) => el.tagName === 'DT')).toEqual([]);
  });

  it('applies typed values after a pause, and the connection limit', async () => {
    const api = show(running({ speedLimit: 256 * KB, maxConnections: 4 }));
    await fireEvent.click(await screen.findByRole('tab', { name: /Speed limit/ }));
    expect((screen.getByRole('switch', { name: /Limit this/ }) as HTMLInputElement).checked).toBe(true);
    const value = screen.getByRole('textbox', { name: 'Speed limit' }) as HTMLInputElement;
    expect(value.value).toBe('256');
    await fireEvent.input(value, { target: { value: '300' } });
    // Only the window's mark for the complete dialog has been sent so far.
    expect(api.args.get('setDownloadOptions')).toEqual([7, { completionDialog: true }]);
    expect(api.calls.filter((c) => c === 'setDownloadOptions')).toHaveLength(1);
    await waitFor(() => expect(api.args.get('setDownloadOptions')).toEqual([7, { speedLimit: 300 * KB }]));

    const conns = screen.getByLabelText('Connections') as HTMLInputElement;
    expect(conns.value).toBe('4');
    await fireEvent.input(conns, { target: { value: '40' } });
    await fireEvent.keyDown(conns, { key: 'Enter' });
    expect(screen.getByRole('alert').textContent).toContain('from 1 to 32');
    await fireEvent.input(conns, { target: { value: '16' } });
    await fireEvent.keyDown(conns, { key: 'Enter' });
    await waitFor(() => expect(api.args.get('setDownloadOptions')).toEqual([7, { maxConnections: 16 }]));
    // Empty = the daemon's default.
    await fireEvent.input(conns, { target: { value: '' } });
    await fireEvent.blur(conns);
    await waitFor(() => expect(api.args.get('setDownloadOptions')).toEqual([7, { maxConnections: 0 }]));
    expect(conns.placeholder).toBe('Default (8)');
  });

  it('follows limits changed elsewhere while the fields are not being edited', async () => {
    const api = show();
    await fireEvent.click(await screen.findByRole('tab', { name: 'Speed limit' }));
    const box = () => screen.getByRole('switch', { name: /Limit this/ }) as HTMLInputElement;
    const value = () => screen.getByRole('textbox', { name: 'Speed limit' }) as HTMLInputElement;
    const unit = () => screen.getByRole('combobox', { name: 'Speed limit unit' }) as HTMLButtonElement;
    const conns = () => screen.getByLabelText('Connections') as HTMLInputElement;
    const sent = () => api.calls.filter((c) => c === 'setDownloadOptions').length;
    expect(box().checked).toBe(false);

    // "downex set 7 --limit 1M -c 6" while the window is open.
    update(api, 7, { speedLimit: MB, maxConnections: 6 });
    await tick();
    expect(box().checked).toBe(true);
    expect(value().value).toBe('1');
    expect(unit().value).toBe('MB/s');
    expect(conns().value).toBe('6');
    // Unticking removes the limit in force.
    await fireEvent.click(box());
    await waitFor(() => expect(api.args.get('setDownloadOptions')).toEqual([7, { speedLimit: 0 }]));

    // A limit set here and removed elsewhere: the fields follow, and leaving them sends nothing.
    await fireEvent.click(box());
    await fireEvent.input(value(), { target: { value: '2' } });
    await fireEvent.keyDown(value(), { key: 'Enter' });
    await waitFor(() => expect(api.args.get('setDownloadOptions')).toEqual([7, { speedLimit: 2 * MB }]));
    await tick();
    const before = sent();
    update(api, 7, { speedLimit: 0, maxConnections: 3 });
    await tick();
    expect(box().checked).toBe(false);
    expect(conns().value).toBe('3');
    await fireEvent.blur(value());
    await fireEvent.blur(conns());
    await new Promise((r) => setTimeout(r, 80));
    expect(sent()).toBe(before);
    await fireEvent.click(screen.getByRole('tab', { name: 'Status' }));
    expect(screen.queryAllByText('Speed limit').filter((el) => el.tagName === 'DT')).toEqual([]);
  });

  it('keeps a field the user is editing, and shows what the daemon applied', async () => {
    const api = show(running({ speedLimit: 256 * KB, maxConnections: 4 }));
    // Like downexd: 0 connections = its default, which the answer reports.
    const setOptions = api.setDownloadOptions;
    api.setDownloadOptions = async (id, opts) => {
      const d = await setOptions(id, opts);
      if (opts.maxConnections !== 0) return d;
      const applied = { ...d, maxConnections: 8 };
      api.items.set(id, applied);
      return applied;
    };
    await fireEvent.click(await screen.findByRole('tab', { name: /Speed limit/ }));
    const value = screen.getByRole('textbox', { name: 'Speed limit' }) as HTMLInputElement;
    const conns = screen.getByLabelText('Connections') as HTMLInputElement;
    const sent = () => api.calls.filter((c) => c === 'setDownloadOptions').length;

    // Typing waits for its timer; a change from elsewhere does not overwrite it.
    value.focus();
    await fireEvent.input(value, { target: { value: '300' } });
    update(api, 7, { speedLimit: MB });
    await tick();
    expect(value.value).toBe('300');
    await waitFor(() => expect(api.args.get('setDownloadOptions')).toEqual([7, { speedLimit: 300 * KB }]));
    // Nor while the field has the focus without an edit; leaving it shows the limit in force.
    update(api, 7, { speedLimit: 2 * MB });
    await tick();
    expect(value.value).toBe('300');
    const before = sent();
    value.blur();
    await waitFor(() => expect(value.value).toBe('2'));
    expect((screen.getByRole('combobox', { name: 'Speed limit unit' }) as HTMLButtonElement).value).toBe('MB/s');
    expect(sent()).toBe(before);

    // An empty connections field asks for the default: the field shows what was applied.
    await fireEvent.input(conns, { target: { value: '' } });
    await fireEvent.keyDown(conns, { key: 'Enter' });
    await waitFor(() => expect(api.args.get('setDownloadOptions')).toEqual([7, { maxConnections: 0 }]));
    await waitFor(() => expect(conns.value).toBe('8'));
    await fireEvent.blur(conns);
    await new Promise((r) => setTimeout(r, 80));
    expect(sent()).toBe(before + 1);
  });

  it('switches tabs with the arrow keys', async () => {
    show();
    const status = await screen.findByRole('tab', { name: 'Status' });
    status.focus();
    await fireEvent.keyDown(status, { key: 'ArrowRight' });
    const limits = screen.getByRole('tab', { name: /Speed limit/ });
    expect(limits.getAttribute('aria-selected')).toBe('true');
    expect(document.activeElement).toBe(limits);
    await fireEvent.keyDown(limits, { key: 'ArrowRight' });
    expect(status.getAttribute('aria-selected')).toBe('true');
  });
});

describe('ProgressWindow: completion and failure', () => {
  it('turns into the completion view; "Don’t show this dialog again" writes every setting back', async () => {
    const settings: Settings = { ...SETTINGS, notifyOnComplete: false, launchAtLogin: true };
    const api = show(running(), settings);
    await screen.findByText('Receiving data…');
    update(api, 7, { state: 'completed', received: 100 * MiB, connectionInfo: [] });
    expect(await screen.findByRole('heading', { name: 'Download complete' })).toBeTruthy();
    const region = screen.getByRole('region', { name: 'Download complete' });
    expect(region.textContent).toContain('distro-installer.iso');
    expect(region.textContent).toContain('/home/you/Downloads/Archives');
    expect(region.textContent).toContain('105 MB');
    expect(api.win.title).toBe('100% distro-installer.iso');
    expect(api.win.closed).toBe(0);

    await fireEvent.click(screen.getByRole('checkbox', { name: 'Don’t show this dialog again' }));
    await waitFor(() => expect(api.args.get('updateSettings')).toEqual([{ ...settings, showCompleteDialog: false }]));
    // Read first, then written back whole.
    expect(api.calls.slice(-2)).toEqual(['getSettings', 'updateSettings']);
    // The view stays although the setting is now off.
    expect(screen.getByRole('heading', { name: 'Download complete' })).toBeTruthy();
    await fireEvent.click(screen.getByRole('checkbox', { name: 'Don’t show this dialog again' }));
    await waitFor(() => expect(api.args.get('updateSettings')).toEqual([{ ...settings, showCompleteDialog: true }]));

    await fireEvent.click(screen.getByRole('button', { name: 'Open' }));
    await waitFor(() => expect(api.win.closed).toBe(1));
    expect(api.calls).toContain('openFile');
  });

  it('closes by itself on completion when the summary is off', async () => {
    const api = show(running(), { ...SETTINGS, showCompleteDialog: false });
    await screen.findByText('Receiving data…');
    update(api, 7, { state: 'completed', received: 100 * MiB });
    await waitFor(() => expect(api.win.closed).toBe(1));
  });

  it('reads the settings again when its download completes', async () => {
    // Turned off after the window opened (in Settings, or in another progress window).
    const off = show();
    await screen.findByText('Receiving data…');
    off.settings = { ...SETTINGS, showCompleteDialog: false };
    update(off, 7, { state: 'completed', received: 100 * MiB, connectionInfo: [] });
    await waitFor(() => expect(off.win.closed).toBe(1));
    expect(screen.queryByRole('heading', { name: 'Download complete' })).toBeNull();
  });

  it('shows the summary that was turned on after the window opened', async () => {
    const api = show(running(), { ...SETTINGS, showCompleteDialog: false });
    await screen.findByText('Receiving data…');
    api.settings = { ...SETTINGS, showCompleteDialog: true };
    update(api, 7, { state: 'completed', received: 100 * MiB, connectionInfo: [] });
    expect(await screen.findByRole('heading', { name: 'Download complete' })).toBeTruthy();
    expect(api.win.closed).toBe(0);
  });

  it('decides with the settings it has when they cannot be read at completion', async () => {
    const api = show(running(), { ...SETTINGS, showCompleteDialog: false });
    await screen.findByText('Receiving data…');
    api.getSettings = () => Promise.reject(new Error(REFUSED));
    update(api, 7, { state: 'completed', received: 100 * MiB, connectionInfo: [] });
    await waitFor(() => expect(api.win.closed).toBe(1));
  });

  it('closes on completion when the settings could only be read late', async () => {
    // downexd was still starting when the window opened: the first read failed.
    const api = fakeApi([running()], { ...SETTINGS, showCompleteDialog: false });
    const getSettings = api.getSettings;
    let failures = 1;
    api.getSettings = () => (failures-- > 0 ? Promise.reject(new Error(REFUSED)) : getSettings());
    render(ProgressWindow, { props: { api, id: 7, statusMs: 60_000 } });
    await screen.findByText('Receiving data…');
    update(api, 7, { state: 'completed', received: 100 * MiB, connectionInfo: [] });
    await waitFor(() => expect(api.win.closed).toBe(1));
    expect(screen.queryByRole('heading', { name: 'Download complete' })).toBeNull();
  });

  it('shows the summary of a download that was complete when it opened', async () => {
    // "downex-ui --progress <id>" from downexd when a marked download completed (§8).
    const api = show(running({ state: 'completed', received: 100 * MiB }));
    expect(await screen.findByRole('heading', { name: 'Download complete' })).toBeTruthy();
    expect(screen.queryByRole('tab')).toBeNull();
    await fireEvent.click(screen.getByRole('button', { name: 'Open folder' }));
    await waitFor(() => expect(api.win.closed).toBe(1));
    expect(api.calls).toContain('openFolder');
  });

  it('closes when its download was complete before it opened and the summary is off', async () => {
    // Opened as the download started (a browser hand-off, Start download), and a small
    // file was done before this window's first read: nothing pops up (§7, §8).
    const api = show(running({ state: 'completed', received: 100 * MiB }), { ...SETTINGS, showCompleteDialog: false });
    await waitFor(() => expect(api.win.closed).toBe(1));
    expect(screen.queryByRole('heading', { name: 'Download complete' })).toBeNull();
  });

  it('reads the settings again when a download complete at its opening is restarted and completes', async () => {
    const api = show(running({ state: 'completed', received: 100 * MiB, connectionInfo: [] }));
    await screen.findByRole('heading', { name: 'Download complete' });
    update(api, 7, { state: 'downloading', received: 0 });
    await screen.findByText('Receiving data…');
    api.settings = { ...SETTINGS, showCompleteDialog: false };
    update(api, 7, { state: 'completed', received: 100 * MiB, connectionInfo: [] });
    await waitFor(() => expect(api.win.closed).toBe(1));
    expect(screen.queryByRole('heading', { name: 'Download complete' })).toBeNull();
  });

  it('shows the summary of a download complete when it opened if the settings cannot be read', async () => {
    const api = fakeApi([running({ state: 'completed', received: 100 * MiB })], { ...SETTINGS, showCompleteDialog: false });
    api.getSettings = () => Promise.reject(new Error(REFUSED));
    render(ProgressWindow, { props: { api, id: 7, statusMs: 60_000 } });
    expect(await screen.findByRole('heading', { name: 'Download complete' })).toBeTruthy();
    expect(api.win.closed).toBe(0);
  });

  it('asks before it opens a program', async () => {
    const api = show(running({ state: 'completed', received: 100 * MiB, filename: 'setup-x64.exe' }));
    await fireEvent.click(await screen.findByRole('button', { name: 'Open' }));
    const dialog = await screen.findByRole('alertdialog', { name: 'Run this program?' });
    expect(api.calls).not.toContain('openFile');
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Run' }));
    await waitFor(() => expect(api.calls).toContain('openFile'));
  });

  it('stays open on failure with Retry and Close', async () => {
    const api = show();
    await screen.findByText('Receiving data…');
    update(api, 7, { state: 'failed', error: 'Connection reset by the server', errorCode: 'network', speedBps: 0 });
    await waitFor(() => expect(stateLine()).toBe('Failed: Connection reset by the server'));
    expect(screen.getByText(/Resuming keeps the data received so far/)).toBeTruthy();
    expect(api.win.closed).toBe(0);
    await fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    await waitFor(() => expect(api.args.get('resume')).toEqual([7, {}]));
    await waitFor(() => expect(stateLine()).toBe('Waiting in queue'));
  });

  it('starts over after the file changed, and offers no retry for a refused stream', async () => {
    const api = show(running({ state: 'failed', error: 'The file on the server changed', errorCode: 'validator_changed' }));
    await fireEvent.click(await screen.findByRole('button', { name: 'Start over' }));
    await waitFor(() => expect(api.args.get('resume')).toEqual([7, { restart: true }]));

    update(api, 7, { state: 'failed', error: 'The stream is encrypted', errorCode: 'encrypted' });
    await waitFor(() => expect(stateLine()).toBe('Failed: The stream is encrypted'));
    expect(screen.queryByRole('button', { name: /Retry|Start over/ })).toBeNull();
    await fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    expect(api.win.closed).toBe(1);
  });
});

describe('ProgressWindow: download complete dialog (DESKTOP-SPEC §8)', () => {
  const T0 = Date.UTC(2026, 9, 20, 8, 0, 0);

  function done(partial: Partial<Download> = {}): Download {
    return running({
      state: 'completed',
      received: 100 * MiB,
      speedBps: 0,
      connections: 0,
      segments: [],
      connectionInfo: [],
      completionDialog: true,
      startedAt: T0,
      completedAt: T0 + 30 * 60_000,
      activeMs: 754_250,
      ...partial,
    });
  }

  /** Renders the window after `setup` has changed the fake host. */
  function showWith(d: Download, setup: (api: FakeApi) => void = () => {}): FakeApi {
    const api = fakeApi([d], SETTINGS);
    setup(api);
    render(ProgressWindow, { props: { api, id: d.id, statusMs: 60_000, applyDelayMs: 50 } });
    return api;
  }

  const sent = (api: FakeApi) => api.calls.filter((c) => c === 'setDownloadOptions').length;

  it('marks its download for the dialog once, when it first knows it', async () => {
    const api = show();
    await screen.findByText('Receiving data…');
    await waitFor(() => expect(api.items.get(7)?.completionDialog).toBe(true));
    expect(api.args.get('setDownloadOptions')).toEqual([7, { completionDialog: true }]);
    update(api, 7, { received: 50 * MiB });
    update(api, 7, { state: 'paused', speedBps: 0 });
    await tick();
    await fireEvent.focus(window);
    await new Promise((r) => setTimeout(r, 20));
    expect(sent(api)).toBe(1);
  });

  it('sends no mark for a download that is complete or marked already', async () => {
    const complete = show(done({ completionDialog: false }));
    await screen.findByRole('heading', { name: 'Download complete' });
    expect(complete.calls).not.toContain('setDownloadOptions');
  });

  it('sends no mark for a download marked by an earlier window', async () => {
    const api = show(running({ completionDialog: true }));
    await screen.findByText('Receiving data…');
    await new Promise((r) => setTimeout(r, 20));
    expect(sent(api)).toBe(0);
  });

  it('marks again after downexd could not be reached, but not after a refusal', async () => {
    let attempts = 0;
    const api = showWith(running(), (a) => {
      const setOptions = a.setDownloadOptions;
      a.setDownloadOptions = (id, opts) => {
        if (!opts.completionDialog) return setOptions(id, opts);
        attempts++;
        return attempts === 1 ? Promise.reject(new Error(REFUSED)) : setOptions(id, opts);
      };
    });
    await screen.findByText('Receiving data…');
    await tick();
    expect(attempts).toBe(1);
    expect(api.items.get(7)?.completionDialog).toBe(false);
    update(api, 7, { received: 30 * MiB });
    await waitFor(() => expect(api.items.get(7)?.completionDialog).toBe(true));
    expect(attempts).toBe(2);

    let refusals = 0;
    showWith(running({ id: 9 }), (a) => {
      a.setDownloadOptions = () => {
        refusals++;
        return Promise.reject(new Error('failed_precondition: download 9 is being removed'));
      };
    });
    await waitFor(() => expect(refusals).toBe(1));
    await new Promise((r) => setTimeout(r, 20));
    expect(refusals).toBe(1);
  });

  it('shows the address, the saved file, its size and the time taken, with copy buttons', async () => {
    const copied: string[] = [];
    const api = showWith(done(), (a) => {
      a.copyText = async (text) => {
        copied.push(text);
      };
    });
    const region = await screen.findByRole('region', { name: 'Download complete' });
    expect(within(region).getByText('distro-installer.iso')).toBeTruthy();
    expect(row('Address')).toBe('https://downloads.example.com/releases/2026/very/long/path/to/the/file/distro-installer.iso');
    expect(row('Saved to')).toBe('/home/you/Downloads/Archives/distro-installer.iso');
    expect(row('File size')).toBe('105 MB');
    // 100 MiB in 754.25 s of running
    expect(row('Time taken')).toBe('12m 34s · average 139 KB/s');
    // A file has no stream format or quality.
    expect(screen.queryByText('Format')).toBeNull();
    expect(screen.queryByText('Quality')).toBeNull();

    await fireEvent.click(within(region).getByRole('button', { name: 'Copy address' }));
    await fireEvent.click(within(region).getByRole('button', { name: 'Copy path' }));
    expect(copied).toEqual([
      'https://downloads.example.com/releases/2026/very/long/path/to/the/file/distro-installer.iso',
      '/home/you/Downloads/Archives/distro-installer.iso',
    ]);
    expect(screen.getByRole('status').textContent).toBe('Path copied');
    // The window is sized to the dialog (its default height, as jsdom has no layout).
    await waitFor(() => expect(api.win.sizes.at(-1)).toEqual({ width: 520, height: PROGRESS_WINDOW.completeHeight }));
    expect(screen.getAllByRole('button').map((b) => b.textContent?.trim())).toEqual(
      expect.arrayContaining(['Open', 'Open with…', 'Open folder', 'Close']),
    );
  });

  it('counts from the first start without the running time, and shows a stream’s format and quality', async () => {
    showWith(
      makeMediaDownload({
        id: 7,
        state: 'completed',
        size: 300 * MiB,
        received: 300 * MiB,
        media: makeMediaInfo({ fragmentsDone: 200 }),
        startedAt: T0,
        completedAt: T0 + 3723_000,
        activeMs: 0,
      }),
    );
    await screen.findByRole('heading', { name: 'Download complete' });
    expect(row('Manifest')).toBe('https://media.example.org/talk/master.m3u8');
    expect(row('Saved to')).toBe('/home/you/Downloads/Keynote.mp4');
    expect(row('Time taken')).toBe('1h 02m 03s');
    expect(row('Format')).toBe('HLS stream, saved as MP4');
    expect(row('Quality')).toBe('1080p · H.264 · AAC 128 kbit/s');
  });

  it('shows the time taken with the average speed of the whole download, pauses excluded', async () => {
    show(done({ size: 1368 * MiB, received: 1368 * MiB, activeMs: 180_000 }));
    await screen.findByRole('heading', { name: 'Download complete' });
    expect(row('Time taken')).toBe('3m 00s · average 8.0 MB/s');
  });

  it('says the time taken is unknown when downexd did not record it', async () => {
    show(done({ activeMs: 0, startedAt: 0 }));
    await screen.findByRole('heading', { name: 'Download complete' });
    expect(row('Time taken')).toBe('Unknown');
  });

  it('opens with an app the user picks; closing that choice keeps the dialog', async () => {
    const api = show(done());
    const choose = await screen.findByRole('button', { name: 'Open with…' });
    const openWith = api.openWith;

    api.openWith = async () => {
      throw new Error('canceled: no app was chosen');
    };
    await fireEvent.click(choose);
    await tick();
    expect(api.win.closed).toBe(0);
    expect(screen.queryByRole('alert')).toBeNull();

    // A system without the dialog says so.
    api.openWith = async () => {
      throw new Error('unimplemented: choosing an app to open a file with is not available on this system yet');
    };
    await fireEvent.click(choose);
    expect((await screen.findByRole('alert')).textContent).toContain('not available on this system yet');
    expect(api.win.closed).toBe(0);

    api.openWith = openWith;
    await fireEvent.click(choose);
    await waitFor(() => expect(api.win.closed).toBe(1));
    expect(api.args.get('openWith')).toEqual([7]);
    expect(api.calls).not.toContain('openFile');
  });

  it('does not offer Open with… where the host has no such dialog (macOS)', async () => {
    const api = fakeApi([done()], SETTINGS);
    render(ProgressWindow, { props: { api, id: 7, statusMs: 60_000, applyDelayMs: 50, openWith: false } });
    const region = await screen.findByRole('region', { name: 'Download complete' });
    const buttons = within(region)
      .getAllByRole('button')
      .map((b) => b.textContent?.trim());
    expect(buttons).toEqual(expect.arrayContaining(['Open', 'Open folder', 'Close']));
    expect(buttons).not.toContain('Open with…');
    await fireEvent.click(within(region).getByRole('button', { name: 'Open folder' }));
    await waitFor(() => expect(api.calls).toContain('openFolder'));
    expect(api.calls).not.toContain('openWith');
  });

  it('asks before a program is opened with another app', async () => {
    const api = show(done({ filename: 'setup-x64.exe' }));
    await fireEvent.click(await screen.findByRole('button', { name: 'Open with…' }));
    let dialog = await screen.findByRole('alertdialog', { name: 'Open this program with another app?' });
    // Esc closes the question, not the window.
    await fireEvent.keyDown(dialog, { key: 'Escape' });
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull());
    expect(api.win.closed).toBe(0);
    expect(api.calls).not.toContain('openWith');

    await fireEvent.click(screen.getByRole('button', { name: 'Open with…' }));
    dialog = await screen.findByRole('alertdialog', { name: 'Open this program with another app?' });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Choose app…' }));
    await waitFor(() => expect(api.calls).toContain('openWith'));
    expect(api.calls).not.toContain('openFile');
  });

  it('opens the file on Enter, with the focus on Open', async () => {
    const api = show(done());
    const open = await screen.findByRole('button', { name: 'Open' });
    await waitFor(() => expect(document.activeElement).toBe(open));
    // Enter on another button is that button's.
    await fireEvent.keyDown(screen.getByRole('button', { name: 'Open folder' }), { key: 'Enter' });
    expect(api.calls).not.toContain('openFile');
    await fireEvent.keyDown(document.body, { key: 'Enter' });
    await waitFor(() => expect(api.calls).toContain('openFile'));
    await waitFor(() => expect(api.win.closed).toBe(1));
  });

  it('closes on Esc, which the progress view leaves alone', async () => {
    const api = show();
    await screen.findByText('Receiving data…');
    await fireEvent.keyDown(document.body, { key: 'Escape' });
    expect(api.win.closed).toBe(0);
    update(api, 7, { state: 'completed', received: 100 * MiB, connectionInfo: [] });
    await screen.findByRole('heading', { name: 'Download complete' });
    await fireEvent.keyDown(document.body, { key: 'Escape' });
    expect(api.win.closed).toBe(1);
    expect(api.calls).not.toContain('openFile');
  });

  it('keeps the box as it was when the choice cannot be saved', async () => {
    const api = show(done());
    api.updateSettings = async () => {
      throw new Error('unavailable: downexd is not running');
    };
    const box = (await screen.findByRole('checkbox', { name: 'Don’t show this dialog again' })) as HTMLInputElement;
    await fireEvent.click(box);
    expect((await screen.findByRole('alert')).textContent).toContain('Could not save this choice: downexd is not running');
    expect(box.checked).toBe(false);
  });

  it('returns to the progress, at its size, when the download starts again', async () => {
    const api = show(done());
    await screen.findByRole('heading', { name: 'Download complete' });
    await waitFor(() => expect(api.win.sizes.at(-1)).toEqual({ width: 520, height: PROGRESS_WINDOW.completeHeight }));
    update(api, 7, { state: 'queued', received: 0 });
    await waitFor(() => expect(stateLine()).toBe('Waiting in queue'));
    await waitFor(() => expect(api.win.sizes.at(-1)).toEqual({ width: 520, height: PROGRESS_WINDOW.collapsedHeight }));
  });
});

describe('ProgressWindow: video pages (SITE-MEDIA-SPEC §5.4)', () => {
  function site(partial: Partial<Download> = {}): Download {
    return makeSiteDownload({
      id: 7,
      state: 'downloading',
      dir: '/home/you/Downloads/Video',
      size: 460 * MiB,
      received: 115 * MiB,
      speedBps: 4 * MiB,
      media: makeSiteMediaInfo({ fragmentsTotal: 46, fragmentsDone: 11 }),
      connectionInfo: [makeConnection({ index: 1, fragment: 11, received: 2 * MiB }), makeConnection({ index: 2, fragment: 12, state: 'connecting' })],
      ...partial,
    });
  }

  it('says "Getting video info…" while yt-dlp reads the page, then shows the page, parts and size', async () => {
    const api = show(site({ state: 'probing', size: -1, received: 0, connectionInfo: [] }));
    await screen.findByText('Getting video info…');
    expect(row('Page')).toBe(PAGE);
    expect(screen.queryByText('Manifest')).toBeNull();
    update(api, 7, site());
    await screen.findByText('Receiving data…');
    expect(row('File size')).toBe('482 MB');
    expect(row('Parts')).toBe('11 / 46');
    expect(row('Quality')).toBe('1080p · H.264 · AAC 129 kbit/s');
    expect(api.win.title).toBe('25% A talk.mp4');
    await fireEvent.click(screen.getByRole('button', { name: 'Show details' }));
    const table = screen.getByRole('table');
    expect(within(table).getByText('Part 12 · Receiving data…')).toBeTruthy();
    expect(within(table).getByText('Part 13 · Connecting…')).toBeTruthy();
    expect(screen.getByRole('progressbar', { name: 'Parts downloaded' })).toBeTruthy();
  });

  it('says "Merging…" while the video and its sound become one MP4', async () => {
    show(site({ received: 460 * MiB, speedBps: 0, connectionInfo: [], media: makeSiteMediaInfo({ fragmentsTotal: 46, fragmentsDone: 46 }) }));
    await screen.findByText('Merging…');
    expect(row('Transfer rate')).toBe('—');
    await fireEvent.click(screen.getByRole('button', { name: 'Show details' }));
    expect(screen.getByText('Merging the video and its sound into one MP4 file; no connections.')).toBeTruthy();
  });

  it('ends with the site, quality and MP4 in the completion view', async () => {
    const api = show(site());
    await screen.findByText('Receiving data…');
    update(api, 7, { state: 'completed', received: 460 * MiB, connectionInfo: [], media: makeSiteMediaInfo({ fragmentsTotal: 46, fragmentsDone: 46 }) });
    await screen.findByRole('region', { name: 'Download complete' });
    expect(row('Page')).toBe(PAGE);
    // The format and the quality as the human codec text, on rows of their own.
    expect(row('Format')).toBe('YouTube video, saved as MP4');
    expect(row('Quality')).toBe('1080p · H.264 · AAC 129 kbit/s');
  });

  it('explains a page that failed and offers no retry for a gone quality', async () => {
    show(site({ state: 'failed', errorCode: 'media_invalid', error: 'the chosen quality is gone', connectionInfo: [] }));
    await screen.findByText(/Failed:/);
    expect(screen.getByText('The chosen quality is no longer offered. Add the video again.')).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull();
  });
});
