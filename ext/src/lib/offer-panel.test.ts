// @vitest-environment happy-dom

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { BROWSER_FETCH_NOTE, PLAYED_SETTLE_MS } from './ui-format';
import type { FileOffer, FrameOffers, SiteOffer, StreamOffer } from './offer-types';
import {
  MAX_STREAM_PROBES,
  MAX_STREAM_PROBES_FOR_LENGTH,
  OFFER_POLL_MS,
  PLAYED_POLL_MS,
  OfferPanel,
  type OfferPanelDeps,
  OfferPanelDom,
  type OfferPanelView,
  type PanelTarget,
} from './offer-panel';
import { type AddedResult, failure, type Outcome, type VideoInfoResult, type VideoTrack } from './protocol';
import type { TrackPick } from './video-picker';

const MB = 1_000_000;

function track(t: Partial<VideoTrack> & Pick<VideoTrack, 'id' | 'kind'>): VideoTrack {
  return { ext: 'mp4', codecs: '', width: 0, height: 0, frameRate: 0, bandwidth: 0, size: 0, sizeApprox: false, language: '', name: '', ...t };
}

function info(tracks: VideoTrack[], over: Partial<VideoInfoResult> = {}): VideoInfoResult {
  return { type: 'videoInfo', site: '', title: '', durationMs: 60_000, format: 'hls', container: 'mp4', selectedVideo: '', selectedAudio: '', tracks, ...over };
}

const STREAM_INFO = info([
  track({ id: 'v720', kind: 'video', codecs: 'avc1.4d401f', height: 720, frameRate: 30, bandwidth: 2_000_000 }),
  track({ id: 'v360', kind: 'video', codecs: 'avc1.4d401e', height: 360, frameRate: 30, bandwidth: 700_000 }),
  track({ id: 'en', kind: 'audio', codecs: 'mp4a.40.2', bandwidth: 128_000, language: 'en', name: 'English' }),
  track({ id: 'de', kind: 'audio', codecs: 'mp4a.40.2', bandwidth: 128_000, language: 'de', name: 'Deutsch' }),
]);

const PLAYED_INFO = info(
  [
    track({ id: 'v720', kind: 'video', codecs: 'avc1', width: 720, height: 1280, size: 8 * MB, name: '720p (HD)' }),
    track({ id: 'v360', kind: 'video', codecs: 'avc1', width: 360, height: 640, size: 2 * MB, name: '360p' }),
    track({ id: 'a', kind: 'audio', codecs: 'mp4a', size: 1 * MB }),
  ],
  { site: 'Instagram', format: 'http', durationMs: 50_400, selectedVideo: 'v720', selectedAudio: 'a' },
);

const SITE_INFO = info(
  [
    track({ id: '1080', kind: 'video', codecs: 'avc1.640028', height: 1080, frameRate: 30, size: 300 * MB, name: '1080p' }),
    track({ id: 'a', kind: 'audio', codecs: 'mp4a.40.2', size: 5 * MB }),
  ],
  { site: 'Vimeo', title: 'From the site', format: 'http', selectedVideo: '1080', selectedAudio: 'a' },
);

const file = (key: string, over: Partial<FileOffer> = {}): FileOffer => ({
  key,
  display: `cdn.example.com/${key}.mp4`,
  size: 20 * MB,
  sizeKnown: true,
  filename: `${key}.mp4`,
  frame: 'this',
  matched: false,
  short: false,
  width: 0,
  height: 0,
  durationMs: -1,
  ...over,
});

const stream = (key: string, over: Partial<StreamOffer> = {}): StreamOffer => ({
  key,
  format: 'hls',
  display: `cdn.example.com/…/${key}.m3u8`,
  frame: 'this',
  matched: false,
  ...over,
});

const site = (over: Partial<SiteOffer> = {}): SiteOffer => ({
  key: 'page:https://www.example.com/watch/1',
  site: 'www.example.com',
  kind: 'page',
  pageUrl: 'https://www.example.com/watch/1',
  curated: false,
  ...over,
});

function offers(over: Partial<FrameOffers> = {}): FrameOffers {
  return { title: 'A ‮page', topHost: 'www.example.com', files: [], streams: [], features: { streams: true, video: true }, drm: false, live: false, ...over };
}

const TARGET: PanelTarget = { src: 'https://cdn.example.com/f1.mp4', mse: false, drm: false, live: false };

function setup(first: FrameOffers, over: Partial<OfferPanelDeps> = {}) {
  let current = first;
  const views: OfferPanelView[] = [];
  const offersCalls: [string, boolean][] = [];
  const probed: string[] = [];
  const added: [string, TrackPick][] = [];
  const siteAdds: TrackPick[] = [];
  let addAnswer: Outcome<AddedResult> = { ok: true, value: { type: 'added', downloadId: 7, awaiting: true } };
  let streamAnswer: (key: string) => Outcome<VideoInfoResult> = () => ({ ok: true, value: STREAM_INFO });
  let siteAnswer: Outcome<VideoInfoResult> = { ok: true, value: SITE_INFO };
  const siteProbe = vi.fn(async () => siteAnswer);
  const hideSite = vi.fn(async () => ({ hidden: true }));
  const panel = new OfferPanel({
    offers: async (src, mse) => {
      offersCalls.push([src, mse]);
      return { ok: true, value: current };
    },
    streamProbe: async (key) => {
      probed.push(key);
      return streamAnswer(key);
    },
    offerAdd: async (key, pick) => {
      added.push([key, pick]);
      return addAnswer;
    },
    siteProbe,
    siteAdd: async (_s, pick) => {
      siteAdds.push(pick);
      return { ok: true, value: { type: 'added', downloadId: 9, awaiting: false } };
    },
    hideSite,
    render: (v) => views.push(v),
    ...over,
  });
  const last = () => views[views.length - 1]!;
  const ids = (v = last()) => v.rows.map((r) => r.id);
  return {
    panel,
    views,
    last,
    ids,
    offersCalls,
    probed,
    added,
    siteAdds,
    siteProbe,
    hideSite,
    setOffers: (o: FrameOffers) => (current = o),
    answerAdd: (o: Outcome<AddedResult>) => (addAnswer = o),
    answerStreams: (fn: (key: string) => Outcome<VideoInfoResult>) => (streamAnswer = fn),
    answerSite: (o: Outcome<VideoInfoResult>) => (siteAnswer = o),
  };
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] });
});
afterEach(() => {
  vi.useRealTimers();
});

describe('OfferPanel', () => {
  it('lists the matched file at once, probes up to 4 streams (matched first) and asks again every 2 s', async () => {
    const s = setup(
      offers({
        files: [file('f1', { matched: true, height: 720, size: 48 * MB }), file('f2', { short: true, size: MB })],
        streams: [stream('s1'), stream('s2'), stream('s3', { matched: true }), stream('s4'), stream('s5')],
      }),
    );
    s.panel.open(TARGET);
    expect(s.last()).toMatchObject({ phase: 'loading', status: { text: 'Getting video info…' } });
    await vi.advanceTimersByTimeAsync(0);
    expect(s.offersCalls).toEqual([['https://cdn.example.com/f1.mp4', false]]);
    expect(s.probed).toEqual(['s3', 's1', 's2', 's4']);
    expect(s.probed).toHaveLength(MAX_STREAM_PROBES);
    const v = s.last();
    expect(v.title).toBe('A page');
    expect(v.defaultId).toBe('file:f1');
    // Stream qualities are merged across streams; the fifth stream waits as one row.
    expect(s.ids()).toEqual(['stream:s3:v720', 'stream:s3:v360', 'stream:s5', 'file:f1']);
    expect(v.rows.find((r) => r.id === 'file:f1')).toMatchObject({ selected: true, label: 'This video · 720p (HD) · MP4 · 48.0 MB · File' });
    expect(v.groups).toEqual([{ id: 'other', label: 'Other videos on this page (1)', expanded: false, rows: [expect.objectContaining({ id: 'file:f2' })] }]);
    expect(v.status).toEqual({ text: 'Every quality is saved as one MP4 file.', tone: 'ok' });
    expect(v.lookForMore).toBe(false);
    expect(v.hide).toBe("Don't show on www.example.com");

    // A stream the player loaded later appears with the next answer.
    s.setOffers(offers({ files: [file('f1', { matched: true })], streams: [stream('s9')] }));
    await vi.advanceTimersByTimeAsync(OFFER_POLL_MS);
    expect(s.offersCalls).toHaveLength(2);
    expect(s.probed).toContain('s9');
    s.panel.close();
    await vi.advanceTimersByTimeAsync(OFFER_POLL_MS * 3);
    expect(s.offersCalls).toHaveLength(2);
  });

  it('probes older streams while every answer is a bare media playlist, until a master lists qualities', async () => {
    const BARE = info([track({ id: 'v0', kind: 'muxed' })]);
    const keys = ['s6', 's5', 's4', 's3', 's2', 's1'];
    const s = setup(offers({ streams: keys.map((k) => stream(k, { matched: true })) }));
    s.answerStreams((key) => ({ ok: true, value: key === 's1' ? STREAM_INFO : BARE }));
    s.panel.open({ src: '', mse: true, drm: false, live: false });
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(0);
    expect(s.probed).toEqual(keys);
    expect(s.ids()).toEqual(['stream:s1:v720', 'stream:s1:v360']);
    expect(s.last().defaultId).toBe('stream:s1:v720');
  });

  it('puts a stream whose length is not the clicked video\'s after the one whose is (a grid of previews)', async () => {
    const s = setup(offers({ streams: [stream('other', { matched: true }), stream('mine', { matched: true })] }));
    s.answerStreams((key) => ({ ok: true, value: { ...STREAM_INFO, durationMs: key === 'mine' ? 8_433 : 30_500 } }));
    s.panel.open({ src: '', mse: true, drm: false, live: false, durationMs: () => 8_433 });
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(0);
    expect(s.last().defaultId).toBe('stream:mine:v720');
    expect(s.ids()[0]).toBe('stream:mine:v720');
  });

  it('probes older streams until one has the clicked video’s length, which leads (a grid whose tiles all played)', async () => {
    // Review 2026-11-18: a video search of many tiles; the clicked tile's playlist is not among the newest.
    const keys = ['s10', 's9', 's8', 's7', 's6', 's5', 's4', 's3', 's2', 's1'];
    const s = setup(offers({ streams: keys.map((k) => stream(k, { matched: true })) }));
    s.answerStreams((key) => ({ ok: true, value: { ...STREAM_INFO, durationMs: key === 's2' ? 10_241 : 30_000 + Number(key.slice(1)) * 1_000 } }));
    s.panel.open({ src: '', mse: true, drm: false, live: false, durationMs: () => 10_241 });
    for (let i = 0; i < 6; i++) await vi.advanceTimersByTimeAsync(0);
    expect(s.probed).toContain('s2');
    expect(s.probed.length).toBeLessThanOrEqual(MAX_STREAM_PROBES_FOR_LENGTH);
    expect(s.last().defaultId).toBe('stream:s2:v720');
    expect(s.ids()[0]).toBe('stream:s2:v720');
  });

  it('asks one stream of each video’s group, then that group’s master, which leads (playlists named alike)', async () => {
    // Review 2026-11-18: 12 tiles, each a master `<name>.m3u8` and levels `<name>_720w.m3u8`.
    const BARE = (ms: number) => info([track({ id: 'v0', kind: 'muxed' })], { durationMs: ms });
    const groups = ['g6', 'g5', 'g4', 'g3', 'g2', 'g1'];
    const streams = groups.flatMap((g) => [stream(`${g}l`, { matched: true, group: g }), stream(`${g}m`, { matched: true, group: g })]);
    const s = setup(offers({ streams }));
    s.answerStreams((key) => {
      // g4 is within the looser stream tolerance (another tile of 11.03 s), g2 is the clicked tile's.
      const ms = key.startsWith('g2') ? 10_241 : key.startsWith('g4') ? 11_030 : 30_000;
      return { ok: true, value: key.endsWith('m') ? { ...STREAM_INFO, durationMs: ms } : BARE(ms) };
    });
    s.panel.open({ src: '', mse: true, drm: false, live: false, durationMs: () => 10_241 });
    for (let i = 0; i < 8; i++) await vi.advanceTimersByTimeAsync(0);
    expect(s.probed).toContain('g2m');
    // One per group while looking: never both streams of a group of another length.
    expect(s.probed.filter((k) => k.startsWith('g1'))).toHaveLength(1);
    expect(s.last().defaultId).toBe('stream:g2m:v720');
  });

  it('hands a file over with add and says where it went; the expiry text when the address failed', async () => {
    const s = setup(offers({ files: [file('f1', { matched: true })] }));
    s.panel.open(TARGET);
    await vi.advanceTimersByTimeAsync(0);
    const done = s.panel.download();
    expect(s.last()).toMatchObject({ phase: 'sending', busy: true, status: { text: 'Sending…' } });
    expect(s.last().rows.every((r) => r.disabled)).toBe(true);
    await done;
    expect(s.added).toEqual([['f1', { video: '', audio: '' }]]);
    expect(s.last()).toMatchObject({ phase: 'sent', status: { text: 'Confirm in Downex', tone: 'ok' } });
    // Opened again: the list can be used again.
    s.panel.close();
    s.panel.open(TARGET);
    await vi.advanceTimersByTimeAsync(0);
    expect(s.last().phase).toBe('ready');
    s.answerAdd(failure('rejected', 'http_status: the server answered 403', 'http_status'));
    await s.panel.download('file:f1');
    expect(s.last().status).toEqual({
      text: 'the server answered 403. The address may have expired. Play the video again, then download it.',
      tone: 'error',
    });
    s.answerAdd({ ok: true, value: { type: 'added', downloadId: 8 } });
    await s.panel.download('file:f1');
    expect(s.last().status?.text).toBe('Sent to Downex');
  });

  it('sends a stream quality with its tracks, and the default pick without the relay’s streams feature', async () => {
    const s = setup(offers({ streams: [stream('s1'), stream('s2')] }));
    s.answerStreams((key) => (key === 's1' ? { ok: true, value: STREAM_INFO } : failure('outdated_downex', 'Update Downex to choose a stream quality.')));
    s.panel.open({ ...TARGET, src: '', mse: true });
    await vi.advanceTimersByTimeAsync(0);
    expect(s.offersCalls).toEqual([['', true]]);
    expect(s.last().defaultId).toBe('stream:s1:v720');
    // Several MP4 audio tracks: "Audio" for the default row's stream.
    expect(s.last().audio).toEqual({ value: 'en', options: [expect.objectContaining({ id: 'en' }), expect.objectContaining({ id: 'de' })] });
    s.panel.chooseAudio('de');
    await s.panel.download('stream:s1:v360');
    expect(s.added).toEqual([['s1', { video: 'v360', audio: 'de' }]]);
    const outdated = s.last().rows.find((r) => r.id === 'stream:s2')!;
    expect(outdated.detail).toBe('Update Downex to choose a stream quality.');
    s.panel.close();
    s.panel.open({ ...TARGET, src: '', mse: true });
    await vi.advanceTimersByTimeAsync(0);
    await s.panel.download('stream:s2');
    expect(s.added.at(-1)).toEqual(['s2', { video: '', audio: '' }]);
  });

  it('asks a curated site at once and folds the network rows into More sources', async () => {
    const s = setup(offers({ site: site({ key: 'vimeo:1', site: 'Vimeo', kind: 'watch', curated: true }), files: [file('f1')] }));
    s.panel.open(TARGET);
    await vi.advanceTimersByTimeAsync(0);
    expect(s.siteProbe).toHaveBeenCalledTimes(1);
    const v = s.last();
    expect(v.rows.map((r) => r.label)).toEqual(['1080p (Full HD) · H.264 · 305 MB · Vimeo']);
    expect(v.groups.map((g) => [g.id, g.label, g.expanded])).toEqual([['more', 'More sources (1)', false]]);
    expect(v.defaultId).toBe('site:1080');
    expect(v.lookForMore).toBe(false);
    s.panel.toggleGroup('more');
    expect(s.last().groups[0]!.expanded).toBe(true);
    await s.panel.download('site:1080');
    expect(s.siteAdds).toEqual([{ video: '1080', audio: 'a' }]);
    expect(s.last()).toMatchObject({ phase: 'sent', status: { text: 'Downloading in Downex' } });
  });

  it('says the sign-in was used when the site\'s answer needed it (SITE-MEDIA-SPEC §6.3)', async () => {
    const reel = site({ key: 'instagram:AbCdEfGh', site: 'Instagram', kind: 'watch', pageUrl: 'https://www.instagram.com/reel/AbCdEfGh/', curated: true });
    const s = setup(offers({ site: reel }));
    s.answerSite({ ok: true, value: { ...SITE_INFO, signedIn: 'instagram.com' } });
    s.panel.open({ ...TARGET, src: '', mse: true });
    await vi.advanceTimersByTimeAsync(0);
    expect(s.ids()).toEqual(['site:1080']);
    expect(s.last().status).toEqual({ text: 'Used your sign-in for instagram.com. Every quality is saved as one MP4 file.', tone: 'ok' });
    // An answer kept by the background (the popup's probe) says it too.
    const kept = setup(offers({ site: { ...reel, probe: { result: { ok: true, value: { ...SITE_INFO, signedIn: 'instagram.com' } } } } }));
    kept.panel.open({ ...TARGET, src: '', mse: true });
    await vi.advanceTimersByTimeAsync(0);
    expect(kept.siteProbe).not.toHaveBeenCalled();
    expect(kept.last().status?.text).toBe('Used your sign-in for instagram.com. Every quality is saved as one MP4 file.');
    // An anonymous answer keeps the plain note.
    const plain = setup(offers({ site: reel }));
    plain.panel.open({ ...TARGET, src: '', mse: true });
    await vi.advanceTimersByTimeAsync(0);
    expect(plain.last().status?.text).toBe('Every quality is saved as one MP4 file.');
  });

  it('uses a site answer the background already has (the look-ahead), without asking again', async () => {
    const s = setup(offers({ site: site({ key: 'vimeo:1', site: 'Vimeo', curated: true, probe: { result: { ok: true, value: SITE_INFO } } }) }));
    s.panel.open(TARGET);
    await vi.advanceTimersByTimeAsync(0);
    expect(s.siteProbe).not.toHaveBeenCalled();
    expect(s.ids()).toEqual(['site:1080']);
  });

  it('lists qualities the site gives no picture or size for once (they read the same)', async () => {
    const bare = info(
      [
        track({ id: 'm1', kind: 'muxed', codecs: '' }),
        track({ id: 'm2', kind: 'muxed', codecs: '' }),
        track({ id: 'm3', kind: 'muxed', codecs: '' }),
      ],
      { site: 'Instagram', title: 'A reel', format: 'http', selectedVideo: 'm1', selectedAudio: '' },
    );
    const s = setup(offers({ site: site({ key: 'instagram:x', site: 'Instagram', curated: true, probe: { result: { ok: true, value: bare } } }) }));
    s.panel.open(TARGET);
    await vi.advanceTimersByTimeAsync(0);
    expect(s.ids()).toEqual(['site:m1']);
    expect(s.last().defaultId).toBe('site:m1');
  });

  it('asks yt-dlp elsewhere only after "Look for more qualities"', async () => {
    const s = setup(offers({ site: site(), files: [file('f1', { matched: true })] }));
    s.panel.open(TARGET);
    await vi.advanceTimersByTimeAsync(0);
    expect(s.siteProbe).not.toHaveBeenCalled();
    expect(s.last().lookForMore).toBe(true);
    s.panel.lookForMore();
    expect(s.last()).toMatchObject({ lookForMore: false, status: { text: 'Getting video info…' } });
    await vi.advanceTimersByTimeAsync(0);
    expect(s.siteProbe).toHaveBeenCalledTimes(1);
    expect(s.ids()).toEqual(['site:1080']);
    expect(s.last().groups[0]!.rows.map((r) => r.id)).toEqual(['file:f1']);
  });

  it('asks the site at once when nothing was seen on the network, and says when nothing is found', async () => {
    const s = setup(offers({ site: site() }));
    s.answerSite(failure('rejected', 'site_unsupported: no video', 'site_unsupported'));
    s.panel.open({ ...TARGET, src: '', mse: true });
    await vi.advanceTimersByTimeAsync(0);
    expect(s.siteProbe).toHaveBeenCalledTimes(1);
    expect(s.last().status).toEqual({ text: 'Downex found no video on this page.', tone: 'warn' });

    const none = setup(offers());
    none.panel.open(TARGET);
    await vi.advanceTimersByTimeAsync(0);
    expect(none.last()).toMatchObject({ rows: [], lookForMore: false, status: { text: 'Downex found no video it can save here.' } });

    // A network site (VIDEO-DETECT-SPEC §3.8): no site source, and its own text.
    const network = setup({ ...offers(), networkOnly: true });
    network.panel.open(TARGET);
    await vi.advanceTimersByTimeAsync(0);
    expect(network.siteProbe).not.toHaveBeenCalled();
    expect(network.last()).toMatchObject({
      rows: [],
      lookForMore: false,
      status: { text: 'Downex can’t read this site’s videos yet. Play the video, then try again.', tone: 'warn' },
    });
    // Once the video played (a blob or MSE player gave the browser nothing to list), playing is no advice.
    let played = false;
    const playing = setup({ ...offers(), networkOnly: true });
    playing.panel.open({ ...TARGET, src: '', mse: true, played: () => played });
    await vi.advanceTimersByTimeAsync(0);
    expect(playing.last().status?.text).toBe('Downex can’t read this site’s videos yet. Play the video, then try again.');
    played = true;
    await vi.advanceTimersByTimeAsync(OFFER_POLL_MS);
    expect(playing.last()).toMatchObject({ rows: [], status: { text: 'Downex can’t save this site’s videos yet.', tone: 'warn' } });
  });

  it('on a curated site’s feed: asks about the clicked video’s post at once and names it by the site’s title; says to open its page without one', async () => {
    // The post found from the video's links (VIDEO-DETECT-SPEC §5.1 "Feed videos").
    const post = site({ key: 'instagram:CODEaaa111', site: 'Instagram', kind: 'watch', pageUrl: 'https://www.instagram.com/p/CODEaaa111/', curated: true, linked: true });
    const linked = setup({ ...offers({ title: 'Instagram' }), site: post });
    linked.panel.open({ ...TARGET, src: '', mse: true });
    await vi.advanceTimersByTimeAsync(0);
    expect(linked.siteProbe).toHaveBeenCalledTimes(1);
    expect(linked.last().title).toBe('From the site');
    // Not linked: the tab's title stays.
    const own = setup({ ...offers({ title: 'Instagram' }), site: { ...post, linked: undefined } });
    own.panel.open({ ...TARGET, src: '', mse: true });
    await vi.advanceTimersByTimeAsync(0);
    expect(own.last().title).toBe('Instagram');

    // No post found: no Site source; the clicked video's Played source carries the click, and no
    // text sends the user to another page (SITE-MEDIA-SPEC §2.9).
    const feed = setup({ ...offers(), played: { key: 'p1', waitingMs: 0 } });
    feed.panel.open({ ...TARGET, src: '', mse: true });
    await vi.advanceTimersByTimeAsync(0);
    expect(feed.siteProbe).not.toHaveBeenCalled();
    expect(feed.last()).toMatchObject({ rows: [], lookForMore: false, status: { text: 'Getting this video…', tone: 'warn' }, working: true });
    feed.setOffers({ ...offers(), played: { key: 'p1', waitingMs: 21_000, probe: { running: true } } });
    await vi.advanceTimersByTimeAsync(OFFER_POLL_MS);
    expect(feed.last().status).toEqual({ text: 'Downex hasn’t seen this video load yet. It appears here as soon as it plays.', tone: 'warn' });
    // Once Downex matched the files: "Playing" rows, the default, saved by the Played key.
    feed.setOffers({ ...offers(), played: { key: 'p1', waitingMs: 22_000, probe: { result: { ok: true, value: PLAYED_INFO } } } });
    await vi.advanceTimersByTimeAsync(OFFER_POLL_MS);
    expect(feed.last().rows.map((r) => [r.source, r.tag, r.quality])).toEqual([
      ['played', 'Playing', '720p (HD)'],
      ['played', 'Playing', '360p'],
    ]);
    expect(feed.last().defaultId).toBe('played:v720');
    expect(feed.last().part).toBeUndefined();
    expect(feed.last().status?.text).toContain('Saved from what your browser played.');
    await feed.panel.download();
    expect(feed.added).toEqual([['p1', { video: 'v720', audio: 'a' }]]);
    // The browser's own rows are offered there as anywhere.
    const rows = setup({ ...offers({ files: [file('f1', { matched: true })] }) });
    rows.panel.open(TARGET);
    await vi.advanceTimersByTimeAsync(0);
    expect(rows.last().rows.map((r) => r.source)).toEqual(['file']);
  });

  it('names no default while the Played source settles, then keeps the default it showed (§2.9 "The panel")', async () => {
    // Rows from the browser, the Played source not answered yet: listed and clickable, no default.
    const t = setup({ ...offers({ files: [file('f1', { matched: true })] }), played: { key: 'p1', waitingMs: 0, probe: { running: true } } });
    t.panel.open({ ...TARGET, src: '', mse: true });
    await vi.advanceTimersByTimeAsync(0);
    expect(t.last()).toMatchObject({ defaultId: undefined, status: { text: 'Getting this video…' }, working: true });
    expect(t.last().rows.map((r) => r.source)).toEqual(['file']);
    // It answered: what played is the default, and its video file names its sound too.
    t.setOffers({ ...offers({ files: [file('f1', { matched: true })] }), played: { key: 'p1', waitingMs: 3_000, probe: { result: { ok: true, value: PLAYED_INFO } } } });
    // While it had not answered the panel asked every PLAYED_POLL_MS: its rows show at once.
    await vi.advanceTimersByTimeAsync(PLAYED_POLL_MS);
    expect(t.last().defaultId).toBe('played:v720');
    expect(t.last().rows.find((r) => r.id === 'played:v720')?.recommended).toBe(true);
    expect(t.last().rows[0]).toMatchObject({ detail: 'H.264 + AAC' });

    // Not answered within PLAYED_SETTLE_MS: the default is shown, and stays when the answer comes.
    const late = setup({ ...offers({ files: [file('f1', { matched: true })] }), played: { key: 'p1', waitingMs: 0 } });
    late.panel.open({ ...TARGET, src: '', mse: true });
    await vi.advanceTimersByTimeAsync(0);
    late.setOffers({ ...offers({ files: [file('f1', { matched: true })] }), played: { key: 'p1', waitingMs: PLAYED_SETTLE_MS } });
    await vi.advanceTimersByTimeAsync(OFFER_POLL_MS);
    expect(late.last().defaultId).toBe('file:f1');
    late.setOffers({
      ...offers({ files: [file('f1', { matched: true })] }),
      played: { key: 'p1', waitingMs: PLAYED_SETTLE_MS + 500, probe: { result: { ok: true, value: PLAYED_INFO } } },
    });
    await vi.advanceTimersByTimeAsync(OFFER_POLL_MS);
    expect(late.last().rows.map((r) => r.source)).toContain('played');
    expect(late.last().defaultId).toBe('file:f1');
  });

  it('moves the default and its badge to a higher quality with sound the site lists after the played rows', async () => {
    // Facebook's reel: the played 720p shows first, the site's 1080p with sound about 3 s later.
    const best = info(
      [
        track({ id: 'v1080', kind: 'video', codecs: 'av01', width: 1080, height: 1920, size: 15 * MB, name: '1080p (Full HD)' }),
        track({ id: 'a', kind: 'audio', codecs: 'mp4a', size: 1 * MB }),
      ],
      { site: 'Facebook', format: 'http', selectedVideo: 'v1080', selectedAudio: 'a' },
    );
    const run = async (pointerOn: boolean) => {
      let answer: (o: Outcome<VideoInfoResult>) => void = () => {};
      const post = site({ key: 'facebook:1', site: 'Facebook', kind: 'watch', curated: true, linked: true });
      const t = setup(
        { ...offers(), site: post, played: { key: 'p1', waitingMs: 0, probe: { result: { ok: true, value: PLAYED_INFO } } } },
        { siteProbe: () => new Promise((r) => (answer = r)), pointerOn: () => pointerOn },
      );
      t.panel.open({ ...TARGET, src: '', mse: true });
      await vi.advanceTimersByTimeAsync(0);
      expect(t.last().defaultId).toBe('played:v720');
      answer({ ok: true, value: best });
      await vi.advanceTimersByTimeAsync(0);
      return t.last();
    };
    const moved = await run(false);
    expect(moved.defaultId).toBe('site:v1080');
    expect(moved.rows.filter((r) => r.recommended).map((r) => r.id)).toEqual(['site:v1080']);
    // The pointer on the panel: the default stays, and a row still carries the badge.
    const kept = await run(true);
    expect(kept.defaultId).toBe('played:v720');
    expect(kept.rows.some((r) => r.recommended)).toBe(true);
  });

  it('puts what played before the site rows, and never shows the site failure while rows exist (§2.6)', async () => {
    const post = site({ key: 'instagram:abc', site: 'Instagram', kind: 'watch', curated: true, linked: true });
    const t = setup({ ...offers(), site: post, played: { key: 'p1', waitingMs: 0, probe: { result: { ok: true, value: PLAYED_INFO } } } });
    t.answerSite(failure('rejected', 'auth_required: login needed', 'auth_required'));
    t.panel.open({ ...TARGET, src: '', mse: true });
    await vi.advanceTimersByTimeAsync(0);
    expect(t.last().rows[0]?.source).toBe('played');
    expect(t.last().status?.text ?? '').not.toMatch(/sign[ -]?in|cookies|own page|comments/i);
    // The frame's own video page with a better picture from the site: the site's rows first.
    const own = setup({
      ...offers(),
      site: { ...post, linked: undefined },
      played: { key: 'p1', waitingMs: 0, probe: { result: { ok: true, value: PLAYED_INFO } } },
    });
    own.panel.open({ ...TARGET, src: '', mse: true });
    await vi.advanceTimersByTimeAsync(0);
    expect(own.last().rows[0]?.source).toBe('site');
    expect(own.last().groups[0]?.rows.map((r) => r.source)).toContain('played');
  });

  it('never words a panel to send the user elsewhere or to sign in (guard over its states)', async () => {
    const states: FrameOffers[] = [
      offers({ played: { key: 'p1', waitingMs: 0 } }),
      offers({ played: { key: 'p1', waitingMs: 60_000 } }),
      offers({ played: { key: 'p1', waitingMs: 0, probe: { result: failure('rejected', 'not_found_yet: no file', 'not_found_yet') } } }),
      offers({ played: { key: 'p1', waitingMs: 0, probe: { result: failure('outdated_downex', 'Update Downex to save this video.') } } }),
      offers({ site: site({ curated: true, key: 'instagram:x', kind: 'watch', site: 'Instagram' }) }),
      offers({ networkOnly: true }),
      offers(),
    ];
    for (const o of states) {
      const t = setup(o);
      t.answerSite(failure('rejected', 'auth_required: This video needs a sign-in.', 'auth_required'));
      t.panel.open({ ...TARGET, src: '', mse: true });
      await vi.advanceTimersByTimeAsync(0);
      for (const v of t.views) expect(v.status?.text ?? '').not.toMatch(/own page|comments|sign[ -]?in|log ?in|cookies/i);
    }
  });

  it('refuses a protected or live element without asking anything', async () => {
    for (const t of [
      { ...TARGET, drm: true },
      { ...TARGET, live: true },
    ]) {
      const s = setup(offers({ files: [file('f1')] }));
      s.panel.open(t);
      await vi.advanceTimersByTimeAsync(0);
      expect(s.offersCalls).toEqual([]);
      expect(s.last().phase).toBe('refused');
      expect(s.last().status?.text).toMatch(t.drm ? /DRM-protected/ : /live stream/);
    }
    // The background may know better (the element's report).
    const s = setup(offers({ drm: true, files: [file('f1')] }));
    s.panel.open(TARGET);
    await vi.advanceTimersByTimeAsync(0);
    expect(s.last()).toMatchObject({ phase: 'refused', rows: [] });
  });

  it('drops refused streams and shows a failed one’s text, asking again on its row', async () => {
    const s = setup(offers({ streams: [stream('drm'), stream('down')] }));
    s.answerStreams((key) => (key === 'drm' ? failure('rejected', 'encrypted: DRM', 'encrypted') : failure('daemon_unavailable', 'cannot start downexd')));
    s.panel.open(TARGET);
    await vi.advanceTimersByTimeAsync(0);
    expect(s.ids()).toEqual(['stream:down']);
    // The row says the reason's lead, the status the whole reason (not twice one block apart).
    expect(s.last().rows[0]!.detail).toBe('Downex is not running');
    expect(s.last().status).toEqual({ text: 'Downex is not running: cannot start downexd', tone: 'error' });
    s.answerStreams(() => ({ ok: true, value: STREAM_INFO }));
    await s.panel.download('stream:down');
    await vi.advanceTimersByTimeAsync(0);
    expect(s.ids()).toEqual(['stream:down:v720', 'stream:down:v360']);
  });

  it('says the video is live when Downex refused every stream as live', async () => {
    const s = setup(offers({ streams: [stream('a'), stream('b')] }));
    s.answerStreams(() => failure('rejected', 'live: the stream has no end', 'live'));
    s.panel.open({ src: '', mse: true, drm: false, live: false });
    await vi.advanceTimersByTimeAsync(0);
    expect(s.ids()).toEqual([]);
    expect(s.last().status).toEqual({ text: 'This is a live stream. Downex downloads only finished videos.', tone: 'warn' });
  });

  it('shows less in small frames', async () => {
    const narrow = setup(offers({ files: [file('f1', { matched: true }), file('f2')] }), { viewport: () => ({ width: 300, height: 400 }) });
    narrow.panel.open(TARGET);
    await vi.advanceTimersByTimeAsync(0);
    expect(narrow.last()).toMatchObject({ narrow: true });
    expect(narrow.ids()).toEqual(['file:f1', 'file:f2']);
    const short = setup(offers({ files: [file('f1', { matched: true }), file('f2')] }), { viewport: () => ({ width: 300, height: 180 }) });
    short.panel.open(TARGET);
    await vi.advanceTimersByTimeAsync(0);
    expect(short.ids()).toEqual(['file:f1']);
    expect(short.last().note).toBe('More in the Downex toolbar menu');
  });

  it('offers "Don’t show on this site" in a frame that is not told the tab’s host', async () => {
    const s = setup(offers({ title: '', topHost: '', files: [file('f1', { matched: true })] }));
    s.panel.open(TARGET);
    await vi.advanceTimersByTimeAsync(0);
    expect(s.last()).toMatchObject({ title: 'Video', hide: "Don't show on this site" });
  });

  it('says when its status reports work in progress: the offers, an add, the site’s qualities', async () => {
    let addDone: (o: Outcome<AddedResult>) => void = () => undefined;
    let siteDone: (o: Outcome<VideoInfoResult>) => void = () => undefined;
    const s = setup(offers({ site: site(), files: [file('f1', { matched: true })] }), {
      offerAdd: () =>
        new Promise((resolve) => {
          addDone = resolve;
        }),
      siteProbe: () =>
        new Promise((resolve) => {
          siteDone = resolve;
        }),
    });
    s.panel.open(TARGET);
    expect(s.last()).toMatchObject({ phase: 'loading', working: true });
    await vi.advanceTimersByTimeAsync(0);
    // A usable list: its status is the MP4 note, not progress.
    expect(s.last().phase).toBe('ready');
    expect(s.last().working).toBeUndefined();
    const sending = s.panel.download('file:f1');
    expect(s.last()).toMatchObject({ phase: 'sending', working: true });
    addDone({ ok: true, value: { type: 'added', downloadId: 1 } });
    await sending;
    expect(s.last().working).toBeUndefined();
    s.panel.close();
    s.panel.open(TARGET);
    await vi.advanceTimersByTimeAsync(0);
    s.panel.lookForMore();
    expect(s.last()).toMatchObject({ phase: 'ready', working: true, status: { text: 'Getting video info…' } });
    siteDone(failure('rejected', 'site_unsupported: no video', 'site_unsupported'));
    await vi.advanceTimersByTimeAsync(0);
    // The site's failure is never the result while another row exists (VIDEO-DETECT-SPEC §2.6,
    // amendment 2026-11-18): the file's row stays with its note.
    expect(s.last().status?.tone).toBe('ok');
    expect(s.last().rows.map((r) => r.source)).toEqual(['file']);
    expect(s.last().working).toBeUndefined();
    // A refusal is final, not work.
    const drm = setup(offers());
    drm.panel.open({ ...TARGET, drm: true });
    expect(drm.last().working).toBeUndefined();
  });

  it('hides the button on the site through the background', async () => {
    const s = setup(offers());
    expect(await s.panel.hide()).toBe(true);
    expect(s.hideSite).toHaveBeenCalledTimes(1);
  });

  it('forgets everything for another element', async () => {
    const s = setup(offers({ streams: [stream('s1')] }));
    s.panel.open(TARGET);
    await vi.advanceTimersByTimeAsync(0);
    s.panel.reset();
    s.panel.open(TARGET);
    await vi.advanceTimersByTimeAsync(0);
    expect(s.probed).toEqual(['s1', 's1']);
  });
});

describe('OfferPanelDom', () => {
  const trusted = new WeakSet<Event>();
  function dom(view: OfferPanelView) {
    document.body.replaceChildren();
    const host = document.createElement('div');
    document.body.append(host);
    const shadow = host.attachShadow({ mode: 'closed' });
    const calls: string[] = [];
    const d = new OfferPanelDom({
      doc: document,
      isTrusted: (e) => trusted.has(e),
      onRow: (id) => calls.push(`row:${id}`),
      onGroup: (id) => calls.push(`group:${id}`),
      onAudio: (id) => calls.push(`audio:${id}`),
      onAction: () => calls.push('action'),
      onLookForMore: () => calls.push('more'),
      onHide: () => calls.push('hide'),
      onClose: (f) => calls.push(`close:${f}`),
    });
    shadow.append(d.el);
    d.shown = true;
    d.render(view);
    const click = (el: Element, isTrusted = true) => {
      const e = new MouseEvent('click', { bubbles: true, composed: true });
      if (isTrusted) trusted.add(e);
      el.dispatchEvent(e);
    };
    const key = (el: Element, k: string) => el.dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true, composed: true }));
    const rows = () => [...d.el.querySelectorAll<HTMLButtonElement>('button.dx-row')];
    return { d, shadow, calls, click, key, rows };
  }

  async function panelView(o: FrameOffers): Promise<{ panel: OfferPanel; views: OfferPanelView[] }> {
    const views: OfferPanelView[] = [];
    let current = o;
    const panel = new OfferPanel({
      offers: async () => ({ ok: true, value: current }),
      streamProbe: async () => ({ ok: true, value: STREAM_INFO }),
      offerAdd: async () => ({ ok: true, value: { type: 'added', downloadId: 1 } }),
      siteProbe: async () => ({ ok: true, value: SITE_INFO }),
      siteAdd: async () => ({ ok: true, value: { type: 'added', downloadId: 1 } }),
      hideSite: async () => ({ hidden: true }),
      render: (v) => views.push(v),
    });
    (panel as unknown as { setOffers: (x: FrameOffers) => void }).setOffers = (x) => (current = x);
    panel.open(TARGET);
    await vi.advanceTimersByTimeAsync(0);
    return { panel, views };
  }

  it('draws rows with their source tags, the default current, and folded groups', async () => {
    const { views } = await panelView(
      offers({ files: [file('f1', { matched: true, height: 720 }), file('o', { frame: 'other', display: 'b.example/‮x.mp4' })], streams: [stream('s1')] }),
    );
    const { d, rows, calls, click } = dom(views.at(-1)!);
    expect(d.el.getAttribute('role')).toBe('dialog');
    expect(d.el.getAttribute('popover')).toBe('auto');
    expect(d.el.querySelector('h2')!.textContent).toBe('A page');
    expect(rows().map((r) => [r.getAttribute('aria-label'), r.getAttribute('aria-current')])).toEqual([
      ['720p (HD) · H.264 · about 16.0 MB · Stream', 'false'],
      ['360p · H.264 · about 6.2 MB · Stream', 'false'],
      ['This video · 720p (HD) · MP4 · 20.0 MB · File', 'true'],
      ['MP4 file · 20.0 MB · File', 'false'],
    ]);
    expect(rows().map((r) => r.querySelector('.dx-t')!.textContent)).toEqual(['Stream', 'Stream', 'File', 'File']);
    // A quality's name, its codec small, its size.
    expect(rows().map((r) => [r.querySelector('.dx-q')!.textContent, r.querySelector('.dx-c')!.textContent])).toEqual([
      ['720p (HD)', 'H.264'],
      ['360p', 'H.264'],
      ['This video', '720p (HD) · MP4'],
      ['MP4 file', ''],
    ]);
    // Site text is shown without bidi controls; the group's rows are hidden until opened.
    expect(rows()[3]!.title).toBe('b.example/x.mp4');
    expect(rows()[3]!.closest('ul')!.hidden).toBe(true);
    const toggle = d.el.querySelector<HTMLButtonElement>('button.dx-group-toggle[data-group="other"]')!;
    expect(toggle.textContent).toBe('Other videos on this page (1)');
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
    click(rows()[2]!, false);
    expect(calls).toEqual([]);
    click(rows()[2]!);
    click(toggle);
    click(d.el.querySelector('.dx-hide')!);
    expect(calls).toEqual(['row:file:f1', 'group:other', 'hide']);
    expect(d.el.querySelector('.dx-hide')!.textContent).toBe("Don't show on www.example.com");
  });

  it('keeps the focused row across the 2 s refresh', async () => {
    const o = offers({ files: [file('f1', { matched: true }), file('f2')], streams: [stream('s1')] });
    const { panel, views } = await panelView(o);
    const { d, shadow, rows } = dom(views.at(-1)!);
    const focused = rows()[1]!;
    focused.focus();
    expect(shadow.activeElement).toBe(focused);
    // A file arrives: a refresh renders again; the same row keeps the focus and its element.
    (panel as unknown as { setOffers: (x: FrameOffers) => void }).setOffers(offers({ files: [file('f0', { size: 99 * MB }), file('f1', { matched: true }), file('f2')], streams: [stream('s1')] }));
    await vi.advanceTimersByTimeAsync(OFFER_POLL_MS);
    d.render(views.at(-1)!);
    expect(rows().map((r) => r.dataset.id)).toEqual(['stream:s1:v720', 'stream:s1:v360', 'file:f1', 'file:f0', 'file:f2']);
    expect(rows()[1]).toBe(focused);
    expect(shadow.activeElement).toBe(focused);
  });

  it('moves with Up and Down over rows and group toggles, and closes on Esc', async () => {
    const { views } = await panelView(offers({ files: [file('f1', { matched: true }), file('o', { frame: 'other' })] }));
    const { d, shadow, key, rows, calls } = dom(views.at(-1)!);
    d.focusInside();
    expect(shadow.activeElement).toBe(rows()[0]);
    key(rows()[0]!, 'ArrowDown');
    expect((shadow.activeElement as HTMLElement).className).toBe('dx-group-toggle');
    key(shadow.activeElement!, 'ArrowDown');
    expect((shadow.activeElement as HTMLElement).className).toBe('dx-group-toggle');
    key(shadow.activeElement!, 'ArrowUp');
    expect(shadow.activeElement).toBe(rows()[0]);
    key(rows()[0]!, 'Escape');
    expect(calls).toEqual(['close:true']);
  });

  it('shows the site status with its action, "Look for more qualities" and the small-frame note', () => {
    const base: OfferPanelView = { phase: 'ready', title: 'T', rows: [], groups: [], defaultId: undefined, lookForMore: true, narrow: true, busy: false };
    const { d, click, calls } = dom({ ...base, status: { text: 'Getting video info…', tone: 'warn', elapsed: '3 s' }, action: { kind: 'retry', text: 'Try again' }, note: 'More in the Downex toolbar menu' });
    expect(d.el.querySelector('.dx-status-text')!.textContent).toBe('Getting video info…');
    expect(d.el.querySelector('.dx-elapsed')!.textContent).toBe('3 s');
    expect(d.el.dataset.narrow).toBe('true');
    const more = d.el.querySelector<HTMLButtonElement>('.dx-more')!;
    expect(more.hidden).toBe(false);
    expect(more.textContent).toBe('Look for more qualities');
    expect(d.el.querySelector<HTMLElement>('.dx-note')!.textContent).toBe('More in the Downex toolbar menu');
    click(more);
    click(d.el.querySelector('.dx-action')!);
    expect(calls).toEqual(['more', 'action']);
    expect((d.el.querySelector('.dx-foot') as HTMLElement).hidden).toBe(true);
  });

  it('draws the head without a second mark and the MP4 note under the title, announced once', async () => {
    const { views } = await panelView(offers({ files: [file('f1', { matched: true, height: 720 })], streams: [stream('s1')] }));
    const { d } = dom(views.at(-1)!);
    expect(d.el.querySelector('.dx-head .dx-tile')).toBeNull();
    const close = d.el.querySelector<HTMLButtonElement>('.dx-close')!;
    expect(close.getAttribute('aria-label')).toBe('Close');
    expect(close.textContent).toBe('');
    expect(close.querySelector('svg')).not.toBeNull();
    const sub = d.el.querySelector<HTMLElement>('.dx-head .dx-sub')!;
    expect(sub.hidden).toBe(false);
    expect(sub.getAttribute('aria-hidden')).toBe('true');
    expect(sub.textContent).toBe('Every quality is saved as one MP4 file.');
    // The live region keeps the note (it is announced as before) but is not drawn twice.
    const status = d.el.querySelector<HTMLElement>('.dx-status')!;
    expect(status.dataset.note).toBe('true');
    expect(status.querySelector('.dx-status-text')!.textContent).toBe('Every quality is saved as one MP4 file.');
    // Every row ends with a download disc.
    for (const r of d.el.querySelectorAll('button.dx-row')) expect(r.querySelector('.dx-go[aria-hidden="true"] svg')).not.toBeNull();
  });

  it('draws the whole site note under the title: the sign-in used and the browser fetch (SITE-MEDIA-SPEC §2.8, §6.3)', async () => {
    const { views } = await panelView(offers({ files: [file('f1', { matched: true, height: 720 })] }));
    const note = `Used your sign-in for media.example. Every quality is saved as one MP4 file. ${BROWSER_FETCH_NOTE}`;
    const v = views.at(-1)!;
    const { d } = dom({ ...v, status: { text: note, tone: 'ok' } });
    const sub = d.el.querySelector<HTMLElement>('.dx-head .dx-sub')!;
    expect(sub.hidden).toBe(false);
    expect(sub.textContent).toBe(note);
    expect(d.el.querySelector<HTMLElement>('.dx-status')!.dataset.note).toBe('true');
    // Another status leaves the plain MP4 note there.
    d.render({ ...v, status: { text: 'Sending…', tone: 'ok' }, working: true });
    expect(sub.textContent).toBe('Every quality is saved as one MP4 file.');
  });

  it('names each source once per run of rows, not on every row', async () => {
    const { views } = await panelView(
      offers({ files: [file('f1', { matched: true, height: 720 }), file('f2'), file('o', { frame: 'other' })], streams: [stream('s1')] }),
    );
    const { d } = dom(views.at(-1)!);
    const list = d.el.querySelector<HTMLUListElement>('ul.dx-rows')!;
    // Headers before the first row of each run; the rows keep their tags for the model and tests.
    const items = [...list.children].map((li) => (li.classList.contains('dx-sect') ? `# ${li.textContent}` : li.querySelector('.dx-q')!.textContent));
    expect(items).toEqual(['# Stream', '720p (HD)', '360p', '# File', 'This video', 'MP4 file']);
    for (const h of list.querySelectorAll('.dx-sect')) {
      expect(h.getAttribute('aria-hidden')).toBe('true');
      expect(h.querySelector('svg')).not.toBeNull();
    }
    expect([...list.querySelectorAll('button.dx-row')].map((r) => r.querySelector('.dx-t')!.textContent)).toEqual(['Stream', 'Stream', 'File', 'File']);
    // A folded group has its own headers.
    const toggle = d.el.querySelector('button.dx-group-toggle[data-group="other"]')!;
    const other = d.el.querySelector<HTMLUListElement>(`#${toggle.getAttribute('aria-controls')}`)!;
    expect([...other.querySelectorAll('.dx-sect')].map((h) => h.textContent)).toEqual(['File']);
    // A refresh with the same runs keeps the elements (a focused row keeps its focus).
    const before = [...list.children];
    d.render(views.at(-1)!);
    expect([...list.children]).toEqual(before);
  });

  it('marks the row being sent and draws work in progress', async () => {
    const o = offers({ files: [file('f1', { matched: true }), file('f2')] });
    const { views } = await panelView(o);
    const ready = views.at(-1)!;
    const { d, click, rows, calls } = dom(ready);
    expect(d.el.dataset.working).toBe('false');
    click(rows()[1]!);
    expect(calls).toEqual(['row:file:f2']);
    const lock = (r: OfferPanelView['rows'][number]) => ({ ...r, disabled: true });
    d.render({ ...ready, phase: 'sending', busy: true, working: true, rows: ready.rows.map(lock), status: { text: 'Sending…', tone: 'warn' } });
    expect(d.el.dataset.working).toBe('true');
    expect(rows().map((r) => r.getAttribute('aria-busy'))).toEqual([null, 'true']);
    expect(d.el.querySelector<HTMLElement>('.dx-status')!.dataset.note).toBeUndefined();
    expect(d.el.querySelector<HTMLButtonElement>('.dx-more')!.disabled).toBe(true);
    d.render({ ...ready, phase: 'sent', status: { text: 'Confirm in Downex', tone: 'ok' } });
    expect(rows().every((r) => !r.hasAttribute('aria-busy'))).toBe(true);
    expect(d.el.dataset.working).toBe('false');
    // The ok status of a sent video is shown, not taken for the note; waiting for the user in
    // Downex is the info tone, as in the popup.
    expect(d.el.querySelector<HTMLElement>('.dx-status')!.dataset.note).toBeUndefined();
    expect(d.el.querySelector<HTMLElement>('.dx-status')!.dataset.tone).toBe('info');
    // The row that was sent keeps a check while the panel says so; the next view drops it.
    expect(rows().map((r) => r.dataset.sent)).toEqual([undefined, 'true']);
    d.render(ready);
    expect(rows().every((r) => r.dataset.sent === undefined)).toBe(true);
  });

  it('starts the status with a capital, leaving a lowercase program name as it is', () => {
    const base: OfferPanelView = { phase: 'ready', title: 'T', rows: [], groups: [], defaultId: undefined, lookForMore: false, narrow: false, busy: false };
    const { d } = dom({ ...base, status: { text: 'the server answered 403 Forbidden.', tone: 'error' } });
    const text = () => d.el.querySelector('.dx-status-text')!.textContent;
    expect(text()).toBe('The server answered 403 Forbidden.');
    d.render({ ...base, status: { text: 'yt-dlp could not read this page: HTTP Error 403: Forbidden.', tone: 'error' } });
    expect(text()).toBe('yt-dlp could not read this page: HTTP Error 403: Forbidden.');
  });

  it('draws skeleton rows while loading, a meter for the tools, glyphs on the actions and a compact short frame', () => {
    const base: OfferPanelView = { phase: 'loading', title: 'T', rows: [], groups: [], defaultId: undefined, lookForMore: false, narrow: false, busy: false };
    const { d } = dom({ ...base, working: true, status: { text: 'Getting video info…', tone: 'warn' } });
    const skel = d.el.querySelector<HTMLElement>('.dx-skel')!;
    expect(skel.hidden).toBe(false);
    expect(skel.getAttribute('aria-hidden')).toBe('true');
    expect(d.el.querySelector<HTMLElement>('.dx-sub')!.hidden).toBe(true);
    expect(d.el.querySelector<HTMLElement>('.dx-meter')!.hidden).toBe(true);
    d.render({ ...base, phase: 'ready', working: true, status: { text: 'Preparing video downloads: downloading yt-dlp and Deno (34%)', tone: 'warn' } });
    const meter = d.el.querySelector<HTMLElement>('.dx-meter')!;
    expect(meter.hidden).toBe(false);
    expect(meter.querySelector<HTMLElement>('span')!.style.width).toBe('34%');
    expect(skel.hidden).toBe(true);
    d.render({
      ...base,
      phase: 'ready',
      lookForMore: true,
      hide: "Don't show on this site",
      status: { text: 'Downex did not answer in time. Try again.', tone: 'error' },
      action: { kind: 'retry', text: 'Try again' },
    });
    expect(skel.hidden).toBe(true);
    expect(meter.hidden).toBe(true);
    for (const [sel, text] of [
      ['.dx-action', 'Try again'],
      ['.dx-more', 'Look for more qualities'],
      ['.dx-hide', "Don't show on this site"],
    ] as const) {
      const el = d.el.querySelector<HTMLButtonElement>(sel)!;
      expect(el.textContent).toBe(text);
      expect(el.querySelector('svg'), sel).not.toBeNull();
    }
    expect(d.el.querySelector<HTMLElement>('.dx-action')!.dataset.kind).toBe('retry');
    expect(d.el.dataset.short).toBe('false');
    d.render({ ...base, phase: 'ready', note: 'More in the Downex toolbar menu' });
    expect(d.el.dataset.short).toBe('true');
  });
});

describe('OfferPanel: a page with several videos (SITE-MEDIA-SPEC §3.12)', () => {
  const items = [
    { id: 'p1', title: 'Video by example', durationMs: 12_000, thumbnail: 'https://img.example/1.jpg' },
    { id: 'p2', title: 'Video by example', durationMs: 7_000, thumbnail: 'https://img.example/2.jpg' },
  ];
  const itemInfo = (id: string) => ({ ...SITE_INFO, site: 'Instagram', title: `Video ${id}`, entryId: id, items, itemsLeftOut: 1 });

  it('shows the item row over the site rows, gets another item and sends it with the pick', async () => {
    const siteAdds: [TrackPick, string | undefined][] = [];
    const siteProbe = vi.fn(async (_s: SiteOffer, entryId?: string) => ({ ok: true, value: itemInfo(entryId ?? 'p1') }) as Outcome<VideoInfoResult>);
    const s = setup(offers({ site: site({ key: 'instagram:POST', site: 'Instagram', kind: 'watch', curated: true }) }), {
      siteProbe,
      siteAdd: async (_s, pick, _clip, entryId) => {
        siteAdds.push([pick, entryId]);
        return { ok: true, value: { type: 'added', downloadId: 9, awaiting: false } };
      },
    });
    s.panel.open(TARGET);
    await vi.advanceTimersByTimeAsync(0);
    expect(s.last().items).toMatchObject({ index: 0, leftOut: 1 });
    expect(s.last().items?.options.map((o) => o.id)).toEqual(['p1', 'p2']);
    s.panel.chooseItem('p2');
    await vi.advanceTimersByTimeAsync(0);
    expect(siteProbe).toHaveBeenLastCalledWith(expect.objectContaining({ key: 'instagram:POST' }), 'p2');
    expect(s.last().items?.index).toBe(1);
    await s.panel.download();
    expect(siteAdds).toEqual([[{ video: '1080', audio: 'a' }, 'p2']]);
  });

  it('opens on the video the user clicked: by its length, else by the slide the address shows', async () => {
    const curatedSite = () => offers({ site: site({ key: 'instagram:POST', site: 'Instagram', kind: 'watch', curated: true }) });
    const run = async (target: PanelTarget, leftOut: number, position: number | undefined) => {
      const siteProbe = vi.fn(async (_s: SiteOffer, entryId?: string) => ({ ok: true, value: { ...itemInfo(entryId ?? 'p1'), itemsLeftOut: leftOut } }) as Outcome<VideoInfoResult>);
      const s = setup(curatedSite(), { siteProbe, itemPosition: () => position });
      s.panel.open(target);
      await vi.advanceTimersByTimeAsync(0);
      await vi.advanceTimersByTimeAsync(0);
      return { probes: siteProbe.mock.calls.map((c) => c[1]), index: s.last().items?.index };
    };
    // The clicked element's length names one item: that one, though the page's default is p1.
    expect(await run({ ...TARGET, durationMs: () => 7_200 }, 1, undefined)).toEqual({ probes: [undefined, 'p2'], index: 1 });
    // No length: the address's slide, when every slide is a listed video (nothing left out).
    expect(await run(TARGET, 0, 2)).toEqual({ probes: [undefined, 'p2'], index: 1 });
    // With items left out (photos among the slides) the slide says nothing; nor does a length that
    // fits no item, or one that fits the default.
    expect(await run(TARGET, 1, 2)).toEqual({ probes: [undefined], index: 0 });
    expect(await run({ ...TARGET, durationMs: () => 30_000 }, 1, undefined)).toEqual({ probes: [undefined], index: 0 });
    expect(await run({ ...TARGET, durationMs: () => 12_000 }, 0, 2)).toEqual({ probes: [undefined], index: 0 });
  });

  it('draws the row in the panel and passes a chosen tile on', () => {
    const chosen: string[] = [];
    const d = new OfferPanelDom({
      doc: document,
      isTrusted: () => true,
      onRow: () => undefined,
      onGroup: () => undefined,
      onAudio: () => undefined,
      onAction: () => undefined,
      onLookForMore: () => undefined,
      onHide: () => undefined,
      onClose: () => undefined,
      onItem: (id) => chosen.push(id),
    });
    document.body.replaceChildren(d.el);
    const view: OfferPanelView = {
      phase: 'ready',
      title: 'Post',
      rows: [],
      groups: [],
      defaultId: undefined,
      lookForMore: false,
      narrow: false,
      busy: false,
      items: { options: items.map((it, i) => ({ ...it, selected: i === 0 })), index: 0, leftOut: 0 },
    };
    d.render(view);
    const strip = d.el.querySelector<HTMLElement>('.dx-items')!;
    expect(strip.hidden).toBe(false);
    expect(d.el.firstElementChild?.nextElementSibling).toBe(strip);
    strip.querySelectorAll<HTMLButtonElement>('button.dx-item')[1]!.click();
    expect(chosen).toEqual(['p2']);
    d.render({ ...view, items: undefined });
    expect(strip.hidden).toBe(true);
  });
});
