import { describe, expect, it } from 'vitest';
import type { FileOffer, StreamOffer } from './offer-types';
import { allRows, buildOfferRows, fileOrder, type OfferRowsInput, shortFailure, type StreamProbe, streamOrder, streamRefused } from './offer-rows';
import { failure, type VideoInfoResult, type VideoTrack } from './protocol';

const MB = 1_000_000;

function track(t: Partial<VideoTrack> & Pick<VideoTrack, 'id' | 'kind'>): VideoTrack {
  return { ext: 'mp4', codecs: '', width: 0, height: 0, frameRate: 0, bandwidth: 0, size: 0, sizeApprox: false, language: '', name: '', ...t };
}

function info(tracks: VideoTrack[], over: Partial<VideoInfoResult> = {}): VideoInfoResult {
  return { type: 'videoInfo', site: '', title: '', durationMs: 100_000, format: 'hls', container: 'mp4', selectedVideo: '', selectedAudio: '', tracks, ...over };
}

const file = (key: string, over: Partial<FileOffer> = {}): FileOffer => ({
  key,
  display: `cdn.example.com/${key}.mp4`,
  size: 10 * MB,
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
  display: `cdn.example.com/…/${key}/master.m3u8`,
  frame: 'this',
  matched: false,
  ...over,
});

const MASTER = info([
  track({ id: 'v1080', kind: 'video', codecs: 'avc1.640028', height: 1080, frameRate: 30, bandwidth: 5_000_000 }),
  track({ id: 'v720', kind: 'video', codecs: 'avc1.4d401f', height: 720, frameRate: 30, bandwidth: 2_500_000 }),
  track({ id: 'a1', kind: 'audio', codecs: 'mp4a.40.2', bandwidth: 128_000 }),
]);
/** The variant the player loaded: the same 720p as the master's. */
const VARIANT = info([
  track({ id: 'x720', kind: 'video', codecs: 'avc1.4d401e', height: 720, frameRate: 30, bandwidth: 2_400_000 }),
  track({ id: 'x480', kind: 'muxed', codecs: 'avc1.4d401e,mp4a.40.2', height: 480, frameRate: 30, bandwidth: 1_000_000 }),
]);

/** What downexd answers for a bare media playlist (a level, audio or subtitle playlist). */
const BARE = info([track({ id: 'v0', kind: 'muxed' })]);
/** A bare media playlist downexd measured (it has no BANDWIDTH): picture, codecs and an estimated size. */
const MEASURED = info(
  [track({ id: 'v0', kind: 'muxed', codecs: 'avc1.640028,mp4a.40.2', width: 1920, height: 1080, frameRate: 25, size: 2.1 * 1000 * MB, sizeApprox: true, name: '1080p (Full HD)' })],
  { durationMs: 3_907_000 },
);

const SITE = info(
  [
    track({ id: '137', kind: 'video', codecs: 'avc1.640028', height: 1080, frameRate: 30, size: 300 * MB, name: '1080p' }),
    track({ id: '18', kind: 'muxed', codecs: 'avc1.42001E, mp4a.40.2', height: 360, frameRate: 30, size: 40 * MB, name: '360p' }),
    track({ id: '140', kind: 'audio', codecs: 'mp4a.40.2', size: 5 * MB }),
  ],
  { site: 'Vimeo', format: 'http', selectedVideo: '137', selectedAudio: '140' },
);

function rows(over: Partial<OfferRowsInput>) {
  return buildOfferRows({ files: [], streams: [], probes: new Map(), ...over });
}

describe('buildOfferRows', () => {
  it('orders streams, the matched file, then other files by size, and folds other frames and short files', () => {
    const r = rows({
      streams: [stream('s1'), stream('s2', { matched: true }), stream('s3', { frame: 'other' })],
      files: [
        file('f1', { size: 5 * MB }),
        file('f2', { size: 50 * MB }),
        file('f3', { matched: true, height: 720, size: 48 * MB }),
        file('f4', { short: true, size: 1 * MB }),
        file('f5', { frame: 'other', size: 70 * MB }),
        file('f6', { sizeKnown: false, size: 0 }),
      ],
      probes: new Map<string, StreamProbe>([['s1', { phase: 'listed', info: MASTER }]]),
    });
    expect(r.siteListed).toBe(false);
    expect(r.main.map((x) => x.id)).toEqual(['stream:s2', 'stream:s1:v1080', 'stream:s1:v720', 'file:f3', 'file:f2', 'file:f1', 'file:f6']);
    expect(r.other.map((x) => x.id)).toEqual(['stream:s3', 'file:f5', 'file:f4']);
    expect(r.more).toEqual([]);
    // The matched file is the default.
    expect(r.defaultId).toBe('file:f3');
    const matched = r.main.find((x) => x.id === 'file:f3')!;
    expect(matched.label).toBe('This video · 720p (HD) · MP4 · 48.0 MB · File');
    expect(r.main.find((x) => x.id === 'file:f1')!.label).toBe('MP4 file · 5.0 MB · File');
    expect(r.main.find((x) => x.id === 'file:f6')!.size).toBe('');
    expect(r.main.find((x) => x.id === 'file:f1')!.title).toBe('cdn.example.com/f1.mp4');
  });

  it('labels stream qualities with their estimate and tags them', () => {
    const r = rows({ streams: [stream('s1')], probes: new Map([['s1', { phase: 'listed', info: MASTER }]]) });
    const first = r.main[0]!;
    expect(first.label).toBe('1080p (Full HD) · H.264 · about 64.1 MB · Stream');
    expect([first.quality, first.detail, first.size]).toEqual(['1080p (Full HD)', 'H.264', 'about 64.1 MB']);
    expect(first.tag).toBe('Stream');
    expect(first.pick).toEqual({ video: 'v1080', audio: 'a1' });
    expect(first.key).toBe('s1');
    // The first stream quality is the default without a matched file or a site list.
    expect(r.defaultId).toBe('stream:s1:v1080');
  });

  it('merges the qualities of several streams, keeping the first stream’s track', () => {
    const r = rows({
      streams: [stream('master'), stream('variant')],
      probes: new Map<string, StreamProbe>([
        ['master', { phase: 'listed', info: MASTER }],
        ['variant', { phase: 'listed', info: VARIANT }],
      ]),
    });
    expect(r.main.map((x) => x.id)).toEqual(['stream:master:v1080', 'stream:master:v720', 'stream:variant:x480']);
    expect(r.main[2]!.pick).toEqual({ video: 'x480', audio: '' });
  });

  it('lists one quality per class across streams and within each, under standard names', () => {
    // An older downexd named variants by their picture ("1920x818") and listed several per class.
    const film = info(
      [
        track({ id: 'h1', kind: 'video', codecs: 'hvc1.1.6.L120.90', width: 1920, height: 818, frameRate: 24, bandwidth: 4_000_000, name: '1920x818' }),
        track({ id: 'a1', kind: 'video', codecs: 'avc1.640028', width: 1920, height: 818, frameRate: 24, bandwidth: 6_000_000, name: '1920x818' }),
        track({ id: 'a2', kind: 'video', codecs: 'avc1.64001f', width: 1280, height: 544, frameRate: 24, bandwidth: 3_000_000, name: '1280x544' }),
        track({ id: 'm2', kind: 'muxed', codecs: 'avc1.64001f,mp4a.40.2', width: 1280, height: 544, frameRate: 24, bandwidth: 3_200_000, name: '1280x544' }),
        track({ id: 'au', kind: 'audio', codecs: 'mp4a.40.2', bandwidth: 128_000 }),
      ],
      { selectedVideo: 'a1', selectedAudio: 'au' },
    );
    // Another stream of the frame with a 1080p of its own: the same class but another picture,
    // so another video (or another encoding), listed after the film's rows.
    const other = info([track({ id: 'o1', kind: 'video', codecs: 'avc1.640028', width: 1920, height: 1080, frameRate: 60, bandwidth: 8_000_000 })]);
    const r = rows({
      streams: [stream('film', { matched: true }), stream('other')],
      probes: new Map<string, StreamProbe>([
        ['film', { phase: 'listed', info: film }],
        ['other', { phase: 'listed', info: other }],
      ]),
    });
    expect(r.main.map((x) => [x.id, x.quality, x.detail, x.title])).toEqual([
      ['stream:film:a1', '1080p (Full HD)', 'H.264', '1920×818 · cdn.example.com/…/film/master.m3u8'],
      ['stream:film:a2', '720p (HD)', 'H.264', '1280×544 · cdn.example.com/…/film/master.m3u8'],
      ['stream:other:o1', '1080p (Full HD) 60fps', 'H.264', '1920×1080 · cdn.example.com/…/other/master.m3u8'],
    ]);
    expect(r.defaultId).toBe('stream:film:a1');
  });

  it('never hides a different video whose classes an earlier stream already lists', () => {
    // "Other videos on this page": two videos of other frames with the same classes but their own
    // pictures (16:9 and 2.4:1). Each keeps its rows; only the same encoding is merged.
    const a = info([
      track({ id: 'v0', kind: 'video', codecs: 'avc1.640028', width: 1920, height: 1080, frameRate: 30, bandwidth: 5e6, name: '1080p (Full HD)' }),
      track({ id: 'v1', kind: 'video', codecs: 'avc1.4d401f', width: 1280, height: 720, frameRate: 30, bandwidth: 2.5e6, name: '720p (HD)' }),
      track({ id: 'a', kind: 'audio', codecs: 'mp4a.40.2', bandwidth: 128_000 }),
    ]);
    const b = info([
      track({ id: 'v0', kind: 'video', codecs: 'avc1.640028', width: 1920, height: 800, frameRate: 30, bandwidth: 4e6, name: '1080p (Full HD)' }),
      track({ id: 'v1', kind: 'video', codecs: 'avc1.4d401f', width: 1280, height: 534, frameRate: 30, bandwidth: 2e6, name: '720p (HD)' }),
      track({ id: 'a', kind: 'audio', codecs: 'mp4a.40.2', bandwidth: 128_000 }),
    ]);
    const r = rows({
      streams: [stream('A', { frame: 'other' }), stream('B', { frame: 'other' }), stream('A2', { frame: 'other' })],
      probes: new Map<string, StreamProbe>([
        ['A', { phase: 'listed', info: a }],
        ['B', { phase: 'listed', info: b }],
        // The same video A under another address (another token): merged into A's rows.
        ['A2', { phase: 'listed', info: a }],
      ]),
    });
    expect(r.other.map((x) => x.id)).toEqual(['stream:A:v0', 'stream:A:v1', 'stream:B:v0', 'stream:B:v1']);
  });

  it('never makes a short clip the element plays (a pre-roll ad) the default when there is more', () => {
    const ad = file('ad', { matched: true, short: true, size: MB, durationMs: 15_000 });
    const r = rows({ streams: [stream('s1')], files: [ad], probes: new Map([['s1', { phase: 'listed', info: MASTER }]]) });
    expect(r.main.map((x) => x.id)).toEqual(['stream:s1:v1080', 'stream:s1:v720', 'file:ad']);
    expect(r.defaultId).toBe('stream:s1:v1080');
    const withFile = rows({ files: [ad, file('f1')] });
    expect(withFile.defaultId).toBe('file:f1');
    // Alone, it is the default.
    expect(rows({ files: [ad] }).defaultId).toBe('file:ad');
  });

  it('prefers a master playlist’s qualities to the bare media playlists the player loaded after it', () => {
    // The background's order: newest first, so the master (fetched first) comes last.
    const r = rows({
      streams: [stream('s3', { matched: true }), stream('s2', { matched: true }), stream('s1', { matched: true })],
      probes: new Map<string, StreamProbe>([
        ['s3', { phase: 'listed', info: BARE }],
        ['s2', { phase: 'listed', info: BARE }],
        ['s1', { phase: 'listed', info: MASTER }],
      ]),
    });
    expect(r.main.map((x) => x.id)).toEqual(['stream:s1:v1080', 'stream:s1:v720']);
    expect(r.defaultId).toBe('stream:s1:v1080');
  });

  it('keeps bare media playlists when nothing lists qualities, after the streams still asked', () => {
    const r = rows({
      streams: [stream('b1'), stream('p')],
      probes: new Map<string, StreamProbe>([
        ['b1', { phase: 'listed', info: BARE }],
        ['p', { phase: 'probing' }],
      ]),
    });
    expect(r.main.map((x) => x.id)).toEqual(['stream:p', 'stream:b1:v0']);
    expect(r.main[1]!.quality).toBe('Video and audio');
    expect(r.defaultId).toBe('stream:b1:v0');
    // A file of the frame is the default rather than a bare playlist.
    const withFile = rows({ streams: [stream('b1')], files: [file('f1')], probes: new Map([['b1', { phase: 'listed', info: BARE }]]) });
    expect(withFile.defaultId).toBe('file:f1');
  });

  it('lists a bare media playlist downexd measured with its quality and estimated size, still after a master’s qualities', () => {
    const alone = rows({ streams: [stream('b1', { matched: true })], probes: new Map([['b1', { phase: 'listed', info: MEASURED }]]) });
    expect(alone.main.map((x) => x.label)).toEqual(['1080p (Full HD) · H.264 + AAC · about 2.1 GB · Stream']);
    expect(alone.main[0]!.size).toBe('about 2.1 GB');
    expect(alone.defaultId).toBe('stream:b1:v0');
    // The master the player loaded first lists the qualities; the measured level playlist is left out.
    const both = rows({
      streams: [stream('b1', { matched: true }), stream('m', { matched: true })],
      probes: new Map<string, StreamProbe>([
        ['b1', { phase: 'listed', info: MEASURED }],
        ['m', { phase: 'listed', info: MASTER }],
      ]),
    });
    expect(both.main.map((x) => x.id)).toEqual(['stream:m:v1080', 'stream:m:v720']);
    expect(both.defaultId).toBe('stream:m:v1080');
  });

  it('shows a stream without a list as one row: probing, outdated (default pick) or failed (asks again)', () => {
    const r = rows({
      streams: [stream('a'), stream('b'), stream('c'), stream('d', { format: 'dash' })],
      probes: new Map<string, StreamProbe>([
        ['a', { phase: 'probing' }],
        ['b', { phase: 'failed', error: failure('outdated_downex', 'old relay').error }],
        ['c', { phase: 'failed', error: failure('daemon_unavailable', 'cannot start downexd').error }],
      ]),
    });
    expect(r.main.map((x) => [x.id, x.action, x.disabled, x.detail])).toEqual([
      ['stream:a', 'probe', true, 'Getting video info…'],
      ['stream:b', 'add', false, 'Update Downex to choose a stream quality.'],
      ['stream:c', 'probe', false, 'Downex is not running'],
      ['stream:d', 'probe', false, 'cdn.example.com/…/d/master.m3u8'],
    ]);
    expect(r.main[3]!.quality).toBe('DASH stream');
    // A failed stream's row says the reason's lead; its label and tooltip keep the whole reason.
    expect(r.main[2]!.label).toBe('HLS stream · Downex is not running: cannot start downexd · Stream');
    expect(r.main[2]!.title).toBe('Downex is not running: cannot start downexd · cdn.example.com/…/c/master.m3u8');
    expect(r.main[1]!.pick).toEqual({ video: '', audio: '' });
  });

  it('drops streams Downex refused and audio-only lists', () => {
    const refused = (reason: string): StreamProbe => ({ phase: 'failed', error: failure('rejected', `${reason}: x`, reason).error });
    const audioOnly: StreamProbe = { phase: 'listed', info: info([track({ id: 'a', kind: 'audio' })]) };
    const r = rows({
      streams: [stream('e'), stream('l'), stream('u'), stream('o'), stream('ok')],
      probes: new Map<string, StreamProbe>([
        ['e', refused('encrypted')],
        ['l', refused('live')],
        ['u', refused('media_unsupported')],
        ['o', audioOnly],
      ]),
    });
    expect(r.main.map((x) => x.id)).toEqual(['stream:ok']);
    expect(streamRefused(refused('media_invalid'))).toBe(true);
    expect(streamRefused({ phase: 'failed', error: failure('timeout', 'x').error })).toBe(false);
    expect(streamRefused(undefined)).toBe(false);
  });

  it('puts the site first once it lists qualities, folds the rest into More sources and drops same-size files', () => {
    const r = rows({
      site: { name: 'Vimeo', info: SITE, audio: '140' },
      streams: [stream('s1')],
      files: [file('dup', { size: 40 * MB, matched: true }), file('f2', { size: 12 * MB }), file('o', { frame: 'other' })],
      probes: new Map([['s1', { phase: 'listed', info: MASTER }]]),
    });
    expect(r.siteListed).toBe(true);
    expect(r.main.map((x) => x.label)).toEqual(['1080p (Full HD) · H.264 · 305 MB · Vimeo', '360p · H.264 + AAC · 40.0 MB · Vimeo']);
    // The picture is the tooltip of a quality, never its name.
    expect(r.main.map((x) => x.title)).toEqual([undefined, undefined]);
    const film = rows({
      site: {
        name: 'YouTube',
        audio: '140',
        info: info(
          [
            track({ id: '401', kind: 'video', codecs: 'av01.0.12M.08', width: 3840, height: 1634, frameRate: 24, size: 860 * MB, name: '4K (2160p)' }),
            track({ id: '140', kind: 'audio', codecs: 'mp4a.40.2', size: 5 * MB }),
          ],
          { site: 'YouTube', format: 'http', selectedVideo: '401', selectedAudio: '140' },
        ),
      },
    });
    expect(film.main.map((x) => [x.quality, x.detail, x.size, x.title])).toEqual([['4K (2160p)', 'AV1', '865 MB', '3840×1634']]);
    expect(r.main[0]!.pick).toEqual({ video: '137', audio: '140' });
    expect(r.main[1]!.pick).toEqual({ video: '18', audio: '' });
    expect(r.more.map((x) => x.id)).toEqual(['stream:s1:v1080', 'stream:s1:v720', 'file:f2', 'file:o']);
    expect(r.other).toEqual([]);
    expect(r.defaultId).toBe('site:137');
    expect(allRows(r)).toHaveLength(6);
  });

  it('has no default without rows, and falls back to the first usable row', () => {
    expect(rows({}).defaultId).toBeUndefined();
    expect(rows({ streams: [stream('s')], probes: new Map([['s', { phase: 'probing' }]]) }).defaultId).toBeUndefined();
    expect(rows({ files: [file('o', { frame: 'other' })] }).defaultId).toBe('file:o');
  });
});

describe('shortFailure', () => {
  it('keeps a failure’s lead or first sentence', () => {
    expect(shortFailure('Downex is not available: Specified native messaging host not found.')).toBe('Downex is not available');
    expect(shortFailure('Downex did not answer in time. Check its list before trying again.')).toBe('Downex did not answer in time.');
    expect(shortFailure('Update Downex to choose a stream quality.')).toBe('Update Downex to choose a stream quality.');
  });
});

describe('order helpers', () => {
  it('orders the frame’s streams first, matched first, keeping the given (newest first) order', () => {
    const order = streamOrder([stream('o', { frame: 'other' }), stream('a'), stream('m', { matched: true }), stream('b')]);
    expect(order.map((s) => s.key)).toEqual(['m', 'a', 'b', 'o']);
  });

  it('orders files matched first, then by size, short ones last', () => {
    const order = fileOrder([file('s', { short: true, size: 99 * MB }), file('a', { size: 1 }), file('b', { size: 2 }), file('m', { matched: true, size: 0, sizeKnown: false })]);
    expect(order.map((f) => f.key)).toEqual(['m', 'b', 'a', 's']);
  });
});
