import { describe, expect, it, vi } from 'vitest';
import {
  fileRowView,
  MEDIA_EMPTY_TEXT,
  MediaPanel,
  type MediaPanelView,
  popupMedia,
  streamRowInfo,
} from './media-panel';
import type { PageVideoEntry, TabMediaResponse } from './messages';
import type { FileEntry, StreamEntry } from './offer-types';
import { type AddedResult, failure, type Outcome } from './protocol';

const MB = 1_000_000;

const stream = (url: string, over: Partial<StreamEntry> = {}): StreamEntry => ({
  url,
  format: 'hls',
  firstSeen: 1,
  lastSeen: 1,
  key: 's1',
  frame: 'top',
  ...over,
});

const file = (url: string, over: Partial<FileEntry> = {}): FileEntry => ({
  url,
  key: 'f1',
  display: new URL(url).host + '/' + (new URL(url).pathname.split('/').pop() ?? ''),
  size: 10 * MB,
  sizeKnown: true,
  filename: 'clip.mp4',
  frame: 'top',
  matched: false,
  short: false,
  width: 0,
  height: 0,
  durationMs: -1,
  ...over,
});

const video = (over: Partial<PageVideoEntry> = {}): PageVideoEntry => ({
  key: 'vimeo:1',
  site: 'Vimeo',
  kind: 'watch',
  frame: 'top',
  pageUrl: 'https://vimeo.com/1',
  ...over,
});

function response(over: Partial<TabMediaResponse> = {}): TabMediaResponse {
  return { items: [], files: [], videos: [], hiddenHere: false, host: 'www.example.com', ...over };
}

function setup(opts: { tabId?: number; list?: () => Promise<TabMediaResponse>; addFile?: () => Promise<Outcome<AddedResult>> } = {}) {
  const views: MediaPanelView[] = [];
  const list = vi.fn(opts.list ?? (async () => response({ items: [stream('https://a/1.m3u8')] })));
  const addFile = vi.fn(opts.addFile ?? (async (): Promise<Outcome<AddedResult>> => ({ ok: true, value: { type: 'added', downloadId: 1, awaiting: true } })));
  const panel = new MediaPanel({ tabId: 'tabId' in opts ? opts.tabId : 5, list, addFile, render: (v) => views.push(v) });
  return { panel, views, list, addFile, last: () => views.at(-1)! };
}

describe('popupMedia', () => {
  it('groups streams and files per frame, largest files first, short clips at the end', () => {
    const m = popupMedia(
      response({
        items: [stream('https://cdn.example.com/a/master.m3u8'), stream('https://player.example/b.mpd', { format: 'dash', frame: 'embed', key: 's2' })],
        files: [
          file('https://cdn.example.com/small.mp4', { size: 5 * MB }),
          file('https://cdn.example.com/big.mp4', { size: 90 * MB }),
          file('https://ads.example/ad.mp4', { short: true, size: MB, frame: 'embed' }),
          file('https://cdn.example.com/tiny.mp4', { short: true, size: 0.5 * MB }),
          file('https://player.example/e.mp4', { frame: 'embed', matched: true, height: 480 }),
        ],
      }),
    );
    expect(m.groups.map((g) => [g.frame, g.label, g.fold])).toEqual([
      ['top', 'Streams and files', undefined],
      ['embed', 'Embedded players', undefined],
    ]);
    expect(m.groups[0]!.streams.map((s) => [s.format, s.name])).toEqual([['HLS', 'cdn.example.com/a/master.m3u8']]);
    expect(m.groups[0]!.files.map((f) => f.label)).toEqual(['MP4 file · 90.0 MB', 'MP4 file · 5.0 MB']);
    expect(m.groups[1]!.files.map((f) => f.label)).toEqual(['This video · 480p · MP4 · 10.0 MB']);
    expect(m.short.map((f) => f.url)).toEqual(['https://ads.example/ad.mp4', 'https://cdn.example.com/tiny.mp4']);
    expect(m.shortLabel).toBe('Short clips (2)');
  });

  it('folds a frame’s network rows under its curated or YouTube page video', () => {
    const r = response({
      items: [stream('https://cdn.example.com/a.m3u8')],
      files: [file('https://cdn.example.com/a.mp4'), file('https://e.example/b.mp4', { frame: 'embed' })],
      videos: [video()],
    });
    const m = popupMedia(r);
    expect(m.groups.map((g) => [g.frame, g.fold])).toEqual([
      ['top', 'More sources (2)'],
      ['embed', undefined],
    ]);
    // "Find the video on this page" results do not fold anything.
    expect(popupMedia({ ...r, videos: [video({ kind: 'page', key: 'page:https://x/' })] }).groups[0]!.fold).toBeUndefined();
    expect(popupMedia({ ...r, videos: [video({ frame: 'embed' })] }).groups.map((g) => g.fold)).toEqual([undefined, 'More sources (1)']);
  });

  it('marks refused streams Protected, Live or No MP4 video', () => {
    const refused = (reason: string) => stream('https://a/x.m3u8', { probe: { result: failure('rejected', `${reason}: x`, reason) } });
    expect(streamRowInfo(refused('encrypted'))).toMatchObject({ refusal: 'encrypted', refusalLabel: 'Protected' });
    expect(streamRowInfo(refused('live'))).toMatchObject({ refusal: 'live', refusalLabel: 'Live' });
    expect(streamRowInfo(refused('media_unsupported'))).toMatchObject({ refusal: 'no_mp4', refusalLabel: 'No MP4 video' });
    expect(streamRowInfo(stream('https://a/x.m3u8', { probe: { result: failure('timeout', 'x') } })).refusal).toBeUndefined();
    expect(streamRowInfo(stream('https://a/x.m3u8', { probe: { running: true } })).refusal).toBeUndefined();
  });

  it('names a file row by its file name, else by its address’s last segment, with the host apart', () => {
    expect(fileRowView(file('https://cdn.example.com/v/a.mp4', { filename: 'Keynote 2026.mp4' }))).toMatchObject({
      name: 'Keynote 2026.mp4',
      host: 'cdn.example.com',
    });
    expect(fileRowView(file('https://cdn.example.com/v/a.mp4', { filename: '  ', display: 'cdn.example.com/a.mp4' }))).toMatchObject({
      name: 'a.mp4',
      host: 'cdn.example.com',
    });
    expect(fileRowView(file('https://cdn.example.com/', { filename: '', display: 'cdn.example.com' }))).toMatchObject({
      name: 'cdn.example.com',
      host: '',
    });
  });

  it('shows a file row’s Download state', () => {
    const f = file('https://cdn.example.com/clip.mp4', { matched: true, height: 720 });
    expect(fileRowView(f)).toMatchObject({
      label: 'This video · 720p (HD) · MP4 · 10.0 MB',
      title: 'cdn.example.com/clip.mp4',
      name: 'clip.mp4',
      host: 'cdn.example.com',
      state: 'idle',
      button: { text: 'Download', disabled: false, label: 'Download This video · 720p (HD) · MP4 · 10.0 MB' },
    });
    expect(fileRowView(f, { sending: true })).toMatchObject({ state: 'sending', button: { text: 'Sending…', disabled: true } });
    expect(fileRowView(f, { result: { ok: true, value: { type: 'added', downloadId: 1, awaiting: false } } })).toMatchObject({
      state: 'sent',
      result: { text: 'Downloading in Downex', tone: 'ok' },
    });
    expect(fileRowView(f, { result: failure('expired', 'gone') })).toMatchObject({
      state: 'retry',
      result: { text: 'This video is no longer listed. Play it again, then download it.', tone: 'warn' },
    });
  });
});

describe('MediaPanel', () => {
  it('lists the tab’s videos and says when there are none', async () => {
    const { panel, last, list } = setup();
    expect(panel.view().note?.text).toBe('Checking the page…');
    expect(panel.storageKey).toBe('media:5');
    await panel.load();
    expect(list).toHaveBeenCalledWith(5);
    expect(last().groups[0]!.streams[0]!.name).toBe('a/1.m3u8');
    expect(last().note).toBeUndefined();

    const empty = setup({ list: async () => response() });
    await empty.panel.load();
    expect(empty.last().note).toEqual({ text: MEDIA_EMPTY_TEXT, error: false });

    const videos = setup({ list: async () => response({ videos: [video()], hiddenHere: true }) });
    await videos.panel.load();
    expect(videos.last()).toMatchObject({ hiddenHere: true, host: 'www.example.com', videos: [expect.objectContaining({ key: 'vimeo:1' })] });
    expect(videos.last().note).toBeUndefined();

    const noTab = setup({ tabId: undefined });
    await noTab.panel.load();
    expect(noTab.list).not.toHaveBeenCalled();
    expect(noTab.last().note?.text).toBe('Not available for this tab.');
    expect(noTab.panel.storageKey).toBeUndefined();

    const broken = setup({ list: async () => Promise.reject(new Error('no receiver')) });
    await broken.panel.load();
    expect(broken.last().note).toEqual({ text: 'Background unavailable: no receiver', error: true });
  });

  it('renders again only when something shown changed (a live playlist fetched again does not)', async () => {
    let r = response({ items: [stream('https://a/1.m3u8')] });
    const { panel, views } = setup({ list: async () => r });
    await panel.load();
    expect(views).toHaveLength(1);
    r = response({ items: [stream('https://a/1.m3u8', { lastSeen: 99 })] });
    await panel.load();
    expect(views).toHaveLength(1);
    r = response({ items: [stream('https://a/1.m3u8')], files: [file('https://a/1.mp4')] });
    await panel.load();
    expect(views).toHaveLength(2);
  });

  it('hands a file over once per popup and shows where it went', async () => {
    let answer!: (o: Outcome<AddedResult>) => void;
    const { panel, last, addFile } = setup({
      list: async () => response({ files: [file('https://a/1.mp4')] }),
      addFile: () => new Promise((resolve) => (answer = resolve)),
    });
    await panel.load();
    const done = panel.downloadFile('https://a/1.mp4');
    expect(last().groups[0]!.files[0]!.state).toBe('sending');
    void panel.downloadFile('https://a/1.mp4');
    expect(addFile).toHaveBeenCalledTimes(1);
    answer({ ok: true, value: { type: 'added', downloadId: 2, awaiting: true } });
    await done;
    expect(addFile).toHaveBeenCalledWith(5, 'https://a/1.mp4');
    expect(last().groups[0]!.files[0]).toMatchObject({ state: 'sent', result: { text: 'Confirm in Downex' } });
  });

  it('takes over a file hand-off the background runs or answered, and drops a running one it forgot', async () => {
    let add: FileEntry['add'] = { running: true };
    const { panel, last } = setup({ list: async () => response({ files: [file('https://a/1.mp4', add === undefined ? {} : { add })] }) });
    await panel.load();
    expect(last().groups[0]!.files[0]!.state).toBe('sending');
    add = { result: { ok: true, value: { type: 'added', downloadId: 3 } } };
    await panel.load();
    expect(last().groups[0]!.files[0]).toMatchObject({ state: 'sent', result: { text: 'Sent to Downex' } });

    const gone = setup({ list: async () => response({ files: [file('https://a/1.mp4', { add: { running: true } })] }) });
    await gone.panel.load();
    expect(gone.last().groups[0]!.files[0]!.state).toBe('sending');
    gone.list.mockImplementation(async () => response({ files: [file('https://a/1.mp4')] }));
    await gone.panel.load();
    expect(gone.last().groups[0]!.files[0]!.state).toBe('idle');
  });
});
