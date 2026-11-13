import { fireEvent, render, screen, within } from '@testing-library/svelte';
import type { ComponentProps } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Download } from '../api';
import {
  CUSTOM_LIMIT,
  addedText,
  dayTimeText,
  limitOptions,
  receivedOf,
  rowHost,
  rowSubline,
  selectionSummary,
  speedAxis,
  speedStats,
  statePill,
} from '../display';
import { formatClock, setFormatLocale } from '../format';
import type { SpeedSample } from '../stores/downloads.svelte';
import {
  GB,
  MB,
  MiB,
  PAGE,
  chooseOption,
  makeDownload,
  makeMediaDownload,
  makeMediaInfo,
  makeSiteDownload,
  makeSiteMediaInfo,
  optionLabels,
} from '../testing';
import DetailsPanel from './DetailsPanel.svelte';
import SpeedChart from './SpeedChart.svelte';

type PanelProps = Partial<ComponentProps<typeof DetailsPanel>>;

function handlers() {
  return {
    onrestart: vi.fn(),
    onresume: vi.fn(),
    onpause: vi.fn(),
    oncopy: vi.fn(),
    onopenfolder: vi.fn(),
    onconfirm: vi.fn(),
    onaddlogin: vi.fn(),
    onlimit: vi.fn(),
    onprogress: vi.fn(),
    onmore: vi.fn(),
    onselection: vi.fn(),
    ontoggle: vi.fn(),
  };
}

function show(download: Download | undefined, extra: PanelProps = {}) {
  const on = handlers();
  const utils = render(DetailsPanel, {
    props: {
      download,
      selectedCount: download ? 1 : 0,
      history: [],
      defaultConnections: 8,
      ...on,
      ...extra,
    },
  });
  const panel = screen.queryByRole('region');
  /** The value shown for a fact ("Video", "Output", …). */
  const fact = (term: string) => {
    const dt = panel ? within(panel).getAllByText(term).find((el) => el.tagName === 'DT') : undefined;
    return dt?.nextElementSibling?.textContent?.replace(/\s+/g, ' ').trim() ?? null;
  };
  return { ...utils, panel: panel!, fact, on };
}

afterEach(() => setFormatLocale('en-US'));

describe('DetailsPanel: media downloads', () => {
  it('shows the stream, its chosen tracks, the fragments and the output container', () => {
    const { panel, fact } = show(
      makeMediaDownload({
        id: 4,
        state: 'downloading',
        received: 120 * MiB,
        speedBps: 4 * MiB,
        connections: 6,
        media: makeMediaInfo({ fragmentsDone: 50 }),
      }),
    );
    expect(panel.querySelector('.badge.media')?.textContent?.trim()).toBe('HLS');
    expect(fact('Manifest')).toBe('https://media.example.org/talk/master.m3u8');
    expect(fact('Stream')).toBe('HLS · 10:00');
    expect(fact('Video')).toBe('1080p (Full HD) · H.264 · 5.0 Mbit/s · 1920×1080 · 29.97 fps');
    expect(fact('Audio')).toBe('English · 128 kbit/s · mp4a.40.2');
    expect(fact('Output')).toBe('MP4');
    expect(fact('Received')).toBe('126 MB · 25%');
    // the sub-line says what it is, with the quality in plain words
    expect(panel.querySelector('.sub')?.textContent).toBe('media.example.org · Video · 1080p · H.264 · AAC 128 kbit/s');
    expect(panel.querySelector('.big')?.textContent).toBe('4.2 MB/s');
    const bar = screen.getByRole('progressbar', { name: 'Fragments downloaded' });
    expect(bar.getAttribute('aria-valuenow')).toBe('25');
    expect(panel.textContent).toContain('50 of 200');
    expect(panel.textContent).not.toContain('Segments');
  });

  it('shows a stream’s estimated size and the time left from it', () => {
    const { panel, fact } = show(
      makeMediaDownload({
        id: 4,
        state: 'downloading',
        size: 2100 * MiB,
        sizeApprox: true,
        received: 700 * MiB,
        speedBps: 7 * MiB,
        connections: 6,
        media: makeMediaInfo({ fragmentsDone: 50 }),
      }),
    );
    expect(fact('Received')).toBe('734 MB of ~2.2 GB · 25%');
    expect(panel.querySelector('.when .left')?.textContent).toBe('3m 20s left');
    expect(panel.querySelector('.when .done')?.textContent).toMatch(/^Done ≈ \S/);
  });

  it('says a muxed track carries the sound and an undecided container is pending', () => {
    const { fact } = show(
      makeMediaDownload({ id: 5, state: 'queued', media: makeMediaInfo({ selectedVideo: 'v2', selectedAudio: '', container: '', fragmentsTotal: 0 }) }),
    );
    expect(fact('Audio')).toBe('In the video track');
    expect(fact('Output')).toBe('Decided when the stream is assembled');
    expect(screen.getByText('counted when the download starts')).toBeTruthy();
  });

  it('offers no retry for a stream refused as encrypted', () => {
    const { panel } = show(
      makeMediaDownload({ id: 6, state: 'failed', errorCode: 'encrypted', error: 'An initialization segment is encrypted' }),
    );
    expect(panel.textContent).toContain('does not download protected content');
    expect(screen.queryByRole('button', { name: /Retry/ })).toBeNull();
  });

  it('shows the connections overview for files', () => {
    const { panel, fact } = show(makeDownload({ id: 7, state: 'paused', received: MiB }));
    expect(within(panel).getByRole('heading', { name: /^Connections/ })).toBeTruthy();
    expect(panel.textContent).toContain('Not running');
    expect(fact('Address')).toContain('file-7.bin');
    expect(fact('Received')).toBe('1.0 MB of 10.5 MB · 10%');
    expect(screen.getByRole('progressbar', { name: 'Received' }).getAttribute('aria-valuenow')).toBe('10');
    expect(screen.queryByRole('progressbar', { name: 'Fragments downloaded' })).toBeNull();
  });
});

describe('DetailsPanel: video pages (SITE-MEDIA-SPEC §5.4)', () => {
  it('shows the page, the site, the chosen quality, the parts and the size in bytes', () => {
    const { panel, fact } = show(
      makeSiteDownload({
        id: 8,
        state: 'downloading',
        size: 460 * MiB,
        received: 115 * MiB,
        speedBps: 4 * MiB,
        connections: 4,
        media: makeSiteMediaInfo({ fragmentsTotal: 46, fragmentsDone: 11 }),
      }),
    );
    expect(panel.querySelector('.badge.media')?.textContent?.trim()).toBe('YouTube');
    expect(fact('Page')).toBe(PAGE);
    expect(fact('Site')).toBe('YouTube video · 10:00');
    expect(fact('Video')).toBe('1080p (Full HD) 60fps · H.264 · 472 MB · 1920×1080');
    expect(fact('Audio')).toBe('English original · AAC · 129 kbit/s · 10.5 MB');
    expect(fact('Output')).toBe('MP4');
    expect(fact('Received')).toBe('121 MB of 482 MB · 25%');
    expect(panel.querySelector('.sub')?.textContent).toBe('youtube.com · Video · 1080p · H.264 · AAC 129 kbit/s');
    expect(within(panel).getByRole('heading', { name: /^Parts/ }).textContent).toContain('11 of 46');
    expect(screen.getByRole('progressbar', { name: 'Parts downloaded' })).toBeTruthy();
    expect(panel.textContent).not.toContain('Manifest');
  });

  it('says it is getting the video info while yt-dlp reads the page', () => {
    const { panel } = show(makeSiteDownload({ id: 9, state: 'probing' }));
    expect(panel.querySelector('.badge.state-probing')?.textContent).toBe('Getting video info');
  });

  it('waits for the quality with its exact size before it starts', () => {
    const { panel, fact, on } = show(makeSiteDownload({ id: 10, state: 'awaiting_confirmation' }));
    expect(panel.textContent).toContain('This video from the browser waits until you choose its quality');
    expect(fact('Received')).toBe('Not started (482 MB)');
    void fireEvent.click(screen.getByRole('button', { name: 'Review…' }));
    expect(on.onconfirm).toHaveBeenCalledTimes(1);
  });

  it('explains a page that failed, without offering what cannot help', () => {
    const unavailable = show(makeSiteDownload({ id: 11, state: 'failed', errorCode: 'site_unavailable', error: 'this video is not available: Private video' }));
    expect(unavailable.panel.textContent).toContain('This video is not available (private, removed');
    expect(screen.getByRole('button', { name: /Retry/ })).toBeTruthy();
  });

  it('offers no site login for a video page that wants a sign-in, and no retry for a gone quality', () => {
    const { panel } = show(makeSiteDownload({ id: 12, state: 'failed', errorCode: 'auth_required', error: 'the site asks to sign in' }));
    expect(panel.textContent).toContain('The site wants you to sign in');
    expect(screen.queryByRole('button', { name: /Add site login/ })).toBeNull();
  });

  it('offers no retry for a quality the page no longer has', () => {
    show(makeSiteDownload({ id: 13, state: 'failed', errorCode: 'media_invalid', error: 'the chosen quality is gone' }));
    expect(screen.getByText('The chosen quality is no longer offered. Add the video again.')).toBeTruthy();
    expect(screen.queryByRole('button', { name: /Retry/ })).toBeNull();
  });
});

describe('DetailsPanel: visibility (DESIGN-SYSTEM.md §3.15)', () => {
  it('is not rendered when nothing is selected', () => {
    const { container } = show(undefined, { selectedCount: 0 });
    expect(screen.queryByRole('region')).toBeNull();
    expect(container.querySelector('.details')).toBeNull();
  });

  it('is a strip with the size and the selection’s actions when several are selected', async () => {
    const selected = [
      makeDownload({ id: 1, state: 'downloading', size: 3 * 1024 * MiB }),
      makeDownload({ id: 2, state: 'queued', size: 1.2 * 1024 * MiB }),
      makeDownload({ id: 3, state: 'completed', size: 0 }),
    ];
    // the store may still name a focused download: several selected win
    const { panel, on } = show(selected[0], { selectedCount: 3, selected });
    expect(panel.classList.contains('strip')).toBe(true);
    expect(within(panel).getByRole('heading').textContent).toBe('3 downloads selected · 4.5 GB');
    expect(panel.querySelector('.columns')).toBeNull();
    await fireEvent.click(within(panel).getByRole('button', { name: 'Pause' }));
    await fireEvent.click(within(panel).getByRole('button', { name: 'Remove' }));
    expect(on.onselection.mock.calls).toEqual([['pause'], ['remove']]);
    // nothing in the selection can resume
    const resume = within(panel).getByRole('button', { name: 'Resume' });
    expect(resume.getAttribute('aria-disabled')).toBe('true');
    await fireEvent.click(resume);
    expect(on.onselection).toHaveBeenCalledTimes(2);
  });

  it('shows only its header when collapsed, and offers to show the rest', async () => {
    const { panel, on } = show(makeDownload({ id: 2, state: 'downloading', speedBps: MiB, received: MiB }), { collapsed: true });
    expect(panel.classList.contains('collapsed')).toBe(true);
    expect(panel.querySelector('.content')?.hasAttribute('hidden')).toBe(true);
    const toggle = within(panel).getByRole('button', { name: 'Show details' });
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
    await fireEvent.click(toggle);
    expect(on.ontoggle).toHaveBeenCalledTimes(1);
  });
});

describe('DetailsPanel: header actions', () => {
  it('pauses, shows in folder, copies and opens the row menu', async () => {
    const d = makeDownload({ id: 3, state: 'downloading', speedBps: MiB, received: 4 * MiB });
    const { panel, on } = show(d);
    expect(panel.querySelector('.pill')?.textContent).toBe('Downloading · 40%');
    await fireEvent.click(within(panel).getByRole('button', { name: 'Pause' }));
    await fireEvent.click(within(panel).getByRole('button', { name: 'Show in folder' }));
    await fireEvent.click(within(panel).getByRole('button', { name: 'Copy link' }));
    await fireEvent.click(within(panel).getByRole('button', { name: 'More download actions' }));
    expect(on.onpause).toHaveBeenCalledWith(d);
    expect(on.onopenfolder).toHaveBeenCalledWith(d);
    expect(on.oncopy).toHaveBeenCalledWith(d);
    expect(on.onmore).toHaveBeenCalledWith(expect.any(Number), expect.any(Number));
  });

  it('resumes a paused download and offers nothing while downexd is unreachable', async () => {
    const d = makeDownload({ id: 4, state: 'paused', received: MiB });
    const { panel, on, unmount } = show(d);
    await fireEvent.click(within(panel).getByRole('button', { name: 'Resume' }));
    expect(on.onresume).toHaveBeenCalledWith(d);
    unmount();
    const stale = show(d, { stale: true });
    const resume = within(stale.panel).getByRole('button', { name: 'Resume' });
    expect(resume.getAttribute('aria-disabled')).toBe('true');
    await fireEvent.click(resume);
    expect(stale.on.onresume).not.toHaveBeenCalled();
  });

  it('offers to download a completed download again (not while downexd is unreachable)', async () => {
    const d = makeDownload({ id: 7, state: 'completed', received: 10 * MiB, completedAt: 1_700_000_100_000 });
    const { panel, on, unmount } = show(d);
    expect(within(panel).queryByRole('button', { name: 'Resume' })).toBeNull();
    await fireEvent.click(within(panel).getByRole('button', { name: 'Download again…' }));
    expect(on.onrestart).toHaveBeenCalledWith(d);
    unmount();
    // only for completed ones
    const paused = show(makeDownload({ id: 8, state: 'paused' }));
    expect(within(paused.panel).queryByRole('button', { name: 'Download again…' })).toBeNull();
    paused.unmount();
    const stale = show(d, { stale: true });
    const again = within(stale.panel).getByRole('button', { name: 'Download again…' });
    expect(again.getAttribute('aria-disabled')).toBe('true');
    await fireEvent.click(again);
    expect(stale.on.onrestart).not.toHaveBeenCalled();
  });

  it('says where it came from and when', () => {
    setFormatLocale('en-GB', 'UTC');
    const now = Date.UTC(2026, 9, 28, 15, 0);
    const today = makeDownload({ id: 5, createdAt: Date.UTC(2026, 9, 28, 14, 2), source: 'browser' });
    expect(addedText(today, now)).toBe('Today, 14:02 · from the browser');
    expect(addedText({ ...today, createdAt: Date.UTC(2026, 9, 27, 9, 15), source: 'ui' }, now)).toBe('Yesterday, 9:15 · from the Downex window');
    expect(addedText({ ...today, createdAt: Date.UTC(2026, 9, 21, 18, 40), source: 'unspecified' }, now)).toBe('21 Oct, 18:40');
  });

  it('names the queue and the place in it', () => {
    const { fact } = show(makeDownload({ id: 6, state: 'queued', queuePosition: 2 }));
    expect(fact('Queue')).toBe('Main queue · position 2');
  });
});

describe('DetailsPanel: speed limit', () => {
  it('offers the presets, a limit set elsewhere and Custom…', () => {
    expect(limitOptions(0).map((o) => o.label)).toEqual(['Unlimited', '20 MB/s', '10 MB/s', '5 MB/s', '1 MB/s', 'Custom…']);
    expect(limitOptions(3 * MB).map((o) => o.value)).toEqual([0, 20 * MB, 10 * MB, 5 * MB, 3 * MB, MB, CUSTOM_LIMIT]);
    // a limit set in MiB/s before is its own item, in decimal units
    expect(limitOptions(10 * MiB).map((o) => o.label)).toEqual(['Unlimited', '20 MB/s', '10.5 MB/s', '10 MB/s', '5 MB/s', '1 MB/s', 'Custom…']);
  });

  it('sets a download’s limit, and Custom… opens its progress window', async () => {
    const d = makeDownload({ id: 7, state: 'downloading', speedBps: MiB, received: MiB, speedLimit: 5 * MB });
    const { on } = show(d);
    const select = screen.getByRole('combobox', { name: 'Download speed limit' });
    expect((select as HTMLButtonElement).value).toBe(String(5 * MB));
    expect(optionLabels(select)).toContain('Custom…');
    await chooseOption(select, 10 * MB);
    expect(on.onlimit).toHaveBeenCalledWith(d, 10 * MB);
    await chooseOption(select, CUSTOM_LIMIT);
    expect(on.onprogress).toHaveBeenCalledWith(d);
    // Custom… is an action, not a value: the select keeps the download's limit
    expect((select as HTMLButtonElement).value).toBe(String(5 * MB));
    expect(on.onlimit).toHaveBeenCalledTimes(1);
  });

  it('is text only for a completed download', () => {
    const { fact } = show(makeDownload({ id: 8, state: 'completed', received: 10 * MiB, speedLimit: MB }));
    expect(screen.queryByRole('combobox', { name: 'Download speed limit' })).toBeNull();
    expect(fact('Speed limit')).toBe('1.0 MB/s');
  });

  it('shows when a finished download finished instead of an empty speed chart', () => {
    const completedAt = new Date(2026, 9, 25, 18, 31).getTime();
    const { container } = show(makeDownload({ id: 9, state: 'completed', size: 10 * MiB, received: 10 * MiB, activeMs: 4000, completedAt }));
    const speed = container.querySelector<HTMLElement>('.col.speed')!;
    expect(within(speed).queryByRole('img')).toBeNull();
    // "Finished 25 Oct, 18:31" on the locale's own date and clock, without seconds
    expect(speed.querySelector('.finished')?.textContent).toBe(`Finished ${dayTimeText(completedAt)}`);
    expect(dayTimeText(completedAt, completedAt + 3_600_000)).toMatch(/^Today, /);
    expect(dayTimeText(completedAt)).not.toMatch(/:00$/);
  });

  it('says what a stopped download did last instead of an empty chart, with its limit', () => {
    const updatedAt = new Date(2026, 9, 25, 9, 5).getTime();
    const failed = show(makeDownload({ id: 11, state: 'failed', size: 100 * MiB, received: 18 * MiB, updatedAt, error: 'HTTP 503' }));
    let speed = failed.container.querySelector<HTMLElement>('.col.speed')!;
    expect(within(speed).queryByRole('img')).toBeNull();
    expect(speed.querySelector('.finished')?.textContent).toBe(`Stopped ${dayTimeText(updatedAt)}`);
    expect(speed.querySelector('.stats')?.textContent?.replace(/\s+/g, ' ').trim()).toBe('Limit none');
    failed.unmount();
    const paused = show(makeDownload({ id: 12, state: 'paused', size: 1300 * MiB, received: 252 * MiB }));
    speed = paused.container.querySelector<HTMLElement>('.col.speed')!;
    expect(within(speed).queryByRole('img')).toBeNull();
    expect(speed.querySelector('.finished')?.textContent).toBe('Paused · 264 MB of 1.4 GB');
    paused.unmount();
    const queued = show(makeDownload({ id: 13, state: 'queued', queuePosition: 2 }));
    expect(queued.container.querySelector('.col.speed .finished')?.textContent).toBe('Queued #2');
  });

  it('lets the keyboard scroll the body when it overflows', () => {
    const height = vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get').mockReturnValue(254);
    const client = vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(173);
    render(DetailsPanel, {
      props: { download: makeDownload({ id: 14, state: 'paused', size: 10 * MiB, received: MiB }), selectedCount: 1, history: [], defaultConnections: 8, ...handlers() },
    });
    const body = screen.getByRole('region', { name: 'Download details' });
    expect(body.classList.contains('content')).toBe(true);
    expect(body.tabIndex).toBe(0);
    height.mockRestore();
    client.mockRestore();
  });

  it('keeps the speed chart of a download that finished within the last minute', () => {
    const completedAt = Date.now();
    const t = Date.now();
    show(makeDownload({ id: 10, state: 'completed', size: 10 * MiB, received: 10 * MiB, completedAt }), {
      history: [{ t: t - 2000, v: 2 * MiB }, { t, v: 3 * MiB }],
    });
    expect(screen.getAllByRole('img', { name: /^Speed over the last minute/ })).toHaveLength(1);
  });
});

describe('SpeedChart: the y-axis (DESIGN-SYSTEM.md §3.15)', () => {
  it('rounds the top up to 1, 2 or 5 of a unit (decimal: KB/s, MB/s) and labels half of it', () => {
    expect(speedAxis(5.9 * MB).labels).toEqual(['10 MB/s', '5 MB/s', '0']);
    // 5.9 MiB/s = 6.2 MB/s
    expect(speedAxis(5.9 * MiB).labels).toEqual(['10 MB/s', '5 MB/s', '0']);
    expect(speedAxis(11.2 * MB).labels).toEqual(['20 MB/s', '10 MB/s', '0']);
    expect(speedAxis(1.4 * MB).labels).toEqual(['2 MB/s', '1 MB/s', '0']);
    expect(speedAxis(931 * 1000).labels).toEqual(['1 MB/s', '0.5 MB/s', '0']);
    expect(speedAxis(999_999).labels).toEqual(['1 MB/s', '0.5 MB/s', '0']);
    expect(speedAxis(300 * 1000).labels).toEqual(['500 KB/s', '250 KB/s', '0']);
    expect(speedAxis(5 * MB).max).toBe(5 * MB);
    expect(speedAxis(0).labels).toEqual(['1 MB/s', '0.5 MB/s', '0']);
  });

  it('draws the last minute with its axis labels, average and peak', () => {
    const t = 1_800_000_000_000;
    const samples: SpeedSample[] = [
      { t: t - 90_000, v: 50 * MB }, // older than a minute: left out
      { t: t - 40_000, v: 4 * MB },
      { t: t - 20_000, v: 6 * MB },
      { t, v: 5 * MB },
    ];
    const stats = speedStats(samples);
    expect(stats.peak).toBe(6 * MB);
    expect(stats.avg).toBe(5 * MB);
    const { container } = render(SpeedChart, { props: { samples } });
    expect([...container.querySelectorAll('.tick')].map((e) => e.textContent)).toEqual(['10 MB/s', '5 MB/s', '0']);
    expect(container.querySelector('path.line')).not.toBeNull();
    expect(container.querySelector('circle.dot')).not.toBeNull();
    expect(screen.getByRole('img').getAttribute('aria-label')).toBe(
      'Speed over the last minute: now 5.0 MB/s, average 5.0 MB/s, peak 6.0 MB/s',
    );
  });

  it('says so when there is nothing to draw', () => {
    const { container } = render(SpeedChart, { props: { samples: [] } });
    expect(container.querySelector('path.line')).toBeNull();
    expect(container.textContent).toContain('No speed in the last minute');
  });
});

describe('display: row and pane texts', () => {
  it('names the host without www and the category, or the kind', () => {
    expect(rowHost(makeDownload({ id: 1, url: 'https://www.example.com/a.zip' }))).toBe('example.com');
    expect(rowHost(makeDownload({ id: 1, url: 'https://a.example/x', finalUrl: 'https://cdn.example.net/x' }))).toBe('cdn.example.net');
    expect(rowHost(makeSiteDownload({ id: 1 }))).toBe('youtube.com');
    expect(rowHost(makeDownload({ id: 1, url: 'not a url' }))).toBe('');
    expect(rowSubline(makeDownload({ id: 1, url: 'https://files.example.com/plan.xlsx', filename: 'plan.xlsx' }))).toBe('files.example.com · Documents');
    expect(rowSubline(makeDownload({ id: 1, url: 'https://files.example.com/x.bin', category: 'Books' }))).toBe('files.example.com · Books');
  });

  it('says the unit once where both numbers share it', () => {
    expect(receivedOf(1.43 * GB, 3.4 * GB)).toBe('1.4 of 3.4 GB');
    expect(receivedOf(900 * MB, 3.4 * GB)).toBe('900 MB of 3.4 GB');
    expect(receivedOf(700 * MB, 2100 * MB, true)).toBe('700 MB of ~2.1 GB');
    // 1,429,658,896 B of 3.4 GB
    expect(receivedOf(1_429_658_896, 3.4 * GB)).toBe('1.4 of 3.4 GB');
    expect(receivedOf(5 * MB, -1)).toBe('5.0 MB');
  });

  it('puts the state and its progress in the pill', () => {
    expect(statePill(makeDownload({ id: 1, state: 'downloading', received: 4.2 * MiB }))).toEqual({ tone: 'ok', text: 'Downloading · 42%' });
    expect(statePill(makeDownload({ id: 1, state: 'paused', received: MiB }))).toEqual({ tone: 'warn', text: 'Paused · 10%' });
    expect(statePill(makeDownload({ id: 1, state: 'failed', errorCode: 'validator_changed' }))).toEqual({ tone: 'fail', text: 'Needs restart' });
    expect(statePill(makeDownload({ id: 1, state: 'awaiting_confirmation' }))).toEqual({ tone: 'info', text: 'Waiting for confirmation' });
    expect(statePill(makeDownload({ id: 1, state: 'completed' }))).toEqual({ tone: 'done', text: 'Completed' });
  });

  it('sums the selection’s known sizes', () => {
    expect(selectionSummary([makeDownload({ id: 1 }), makeDownload({ id: 2 })])).toBe('2 downloads selected · 21.0 MB');
    expect(selectionSummary([makeDownload({ id: 1, size: -1 }), makeDownload({ id: 2, size: -1 })])).toBe('2 downloads selected');
    expect(selectionSummary([makeDownload({ id: 1 }), makeDownload({ id: 2, size: -1 })])).toBe('2 downloads selected · ~10.5 MB');
    expect(formatClock(0)).toBe('—');
  });
});
