import { describe, expect, it } from 'vitest';
import type { VideoInfoResult, VideoTrack } from './protocol';
import {
  audioLabel,
  codecName,
  codecsLabel,
  defaultPick,
  formatBytes,
  needsAudio,
  pickedAudio,
  pickSize,
  qualityKey,
  qualityName,
  qualityRows,
  showsAudioChoice,
  sizeLabel,
  videoTracks,
  withVideo,
} from './video-picker';

function track(t: Partial<VideoTrack> & Pick<VideoTrack, 'id' | 'kind'>): VideoTrack {
  return {
    ext: t.kind === 'audio' ? 'm4a' : 'mp4',
    codecs: '',
    width: 0,
    height: 0,
    frameRate: 0,
    bandwidth: 0,
    size: 0,
    sizeApprox: false,
    language: '',
    name: '',
    ...t,
  };
}

const MB = 1_000_000;

/**
 * A YouTube-like offer as an older downexd listed it: every MP4 quality (each resolution, frame
 * rate, HDR and codec) best first, named by height, then the audios.
 */
function youtube(): VideoInfoResult {
  return {
    type: 'videoInfo',
    site: 'YouTube',
    title: 'A video',
    durationMs: 212_000,
    format: 'http',
    container: 'mp4',
    selectedVideo: '299',
    selectedAudio: '140',
    tracks: [
      track({ id: '401', kind: 'video', codecs: 'av01.0.12M.08', width: 3840, height: 2160, frameRate: 60, bandwidth: 18e6, size: 1100 * MB, name: '2160p60' }),
      track({ id: '701', kind: 'video', codecs: 'av01.0.13M.10.0.110.09.16.09.0', width: 3840, height: 2160, frameRate: 60, bandwidth: 24e6, size: 1500 * MB, name: '2160p60 HDR' }),
      track({ id: '299', kind: 'video', codecs: 'avc1.64002a', width: 1920, height: 1080, frameRate: 60, bandwidth: 6e6, size: 380 * MB, name: '1080p60' }),
      track({ id: '399', kind: 'video', codecs: 'av01.0.09M.08', width: 1920, height: 1080, frameRate: 60, bandwidth: 3e6, size: 200 * MB, sizeApprox: true, name: '1080p60' }),
      track({ id: '136', kind: 'video', codecs: 'avc1.4d401f', width: 1280, height: 720, frameRate: 30, bandwidth: 2e6, name: '720p' }),
      track({ id: '18', kind: 'muxed', codecs: 'avc1.42001E, mp4a.40.2', width: 640, height: 360, frameRate: 30, bandwidth: 600_000, size: 16 * MB, name: '360p' }),
      track({ id: '140', kind: 'audio', codecs: 'mp4a.40.2', bandwidth: 129_000, size: 3 * MB, language: 'en', name: 'English original' }),
      track({ id: '140-drc', kind: 'audio', codecs: 'mp4a.40.2', bandwidth: 129_000, size: 3 * MB, language: 'en', name: 'English original, DRC' }),
      track({ id: '251-de', kind: 'audio', codecs: 'ec-3', bandwidth: 384_000, size: 9 * MB, sizeApprox: true, language: 'de', name: 'German' }),
    ],
  };
}

/**
 * The same offer as downexd lists it since the 2026-10-23 amendment: one quality per class under
 * its standard name, best first, then the audios.
 */
function youtubeNow(): VideoInfoResult {
  const now: Record<string, string> = { '401': '4K (2160p) 60fps', '299': '1080p (Full HD) 60fps', '136': '720p (HD)', '18': '360p' };
  const info = youtube();
  info.tracks = info.tracks.filter((t) => t.kind === 'audio' || now[t.id]).map((t) => (now[t.id] ? { ...t, name: now[t.id] ?? '' } : t));
  return info;
}

describe('the list of qualities', () => {
  it('lists downexd’s qualities under its names, best first, with the size of each with its audio', () => {
    const info = youtubeNow();
    expect(videoTracks(info).map((t) => t.id)).toEqual(['401', '299', '136', '18']);
    expect(qualityRows(info, '140').map((r) => r.label)).toEqual([
      '4K (2160p) 60fps · AV1 · 1.1 GB',
      '1080p (Full HD) 60fps · H.264 · 383 MB',
      // No size of its own: bandwidth × duration of video and audio.
      '720p (HD) · H.264 · ~56.4 MB',
      // Muxed: its own audio, so its own size, and both codecs.
      '360p · H.264 + AAC · 16.0 MB',
    ]);
    expect(qualityRows(info, '140').map((r) => [r.quality, r.codecs, r.picture])).toEqual([
      ['4K (2160p) 60fps', 'AV1', '3840×2160'],
      ['1080p (Full HD) 60fps', 'H.264', '1920×1080'],
      ['720p (HD)', 'H.264', '1280×720'],
      ['360p', 'H.264 + AAC', '640×360'],
    ]);
    // An estimated audio makes every pair an estimate.
    expect(qualityRows(info, '251-de')[1]?.label).toBe('1080p (Full HD) 60fps · H.264 · ~389 MB');
  });

  it('reduces an older downexd’s list to one quality per class and names it by the picture', () => {
    const info = youtube();
    // 701 (HDR: an SDR 4K exists) and 399 (AV1: H.264 plays everywhere) are not listed.
    expect(videoTracks(info).map((t) => t.id)).toEqual(['401', '299', '136', '18']);
    expect(qualityRows(info, '140').map((r) => r.label)).toEqual(qualityRows(youtubeNow(), '140').map((r) => r.label));
    // Its own pick stays the quality of its class.
    expect(videoTracks({ ...info, selectedVideo: '399' }).map((t) => t.id)).toEqual(['401', '399', '136', '18']);
    expect(qualityRows({ ...info, selectedVideo: '701' }, '140')[0]?.label).toBe('4K (2160p) 60fps HDR · AV1 · 1.5 GB');
  });

  it('lists the classes best first whatever the order', () => {
    const info = youtube();
    info.tracks.reverse();
    expect(videoTracks(info).map((t) => t.id)).toEqual(['401', '299', '136', '18']);
  });

  it('names the user’s 3840×1634 film by its class, never its height', () => {
    const film: VideoInfoResult = {
      ...youtube(),
      selectedVideo: '401',
      tracks: [
        track({ id: '401', kind: 'video', codecs: 'av01.0.12M.08', width: 3840, height: 1634, frameRate: 24, size: 860 * MB, name: '1634p' }),
        track({ id: '400', kind: 'video', codecs: 'av01.0.12M.08', width: 2560, height: 1090, frameRate: 24, size: 400 * MB, name: '1090p' }),
        track({ id: '137', kind: 'video', codecs: 'avc1.640028', width: 1920, height: 818, frameRate: 24, size: 200 * MB, name: '818p' }),
        track({ id: '399', kind: 'video', codecs: 'av01.0.08M.08', width: 1920, height: 818, frameRate: 24, size: 150 * MB, name: '818p' }),
        track({ id: '134', kind: 'video', codecs: 'avc1.4d4015', width: 640, height: 272, frameRate: 24, size: 30 * MB, name: '272p' }),
        track({ id: '18', kind: 'muxed', codecs: 'avc1.42001E, mp4a.40.2', width: 640, height: 272, frameRate: 24, size: 40 * MB, name: '272p' }),
        track({ id: '140', kind: 'audio', codecs: 'mp4a.40.2', bandwidth: 129_000, size: 3 * MB, language: 'en', name: 'English original' }),
      ],
    };
    expect(qualityRows(film, '140').map((r) => r.label)).toEqual([
      '4K (2160p) · AV1 · 863 MB',
      '1440p (2K) · AV1 · 403 MB',
      '1080p (Full HD) · H.264 · 203 MB',
      '360p · H.264 · 33.0 MB',
    ]);
    expect(qualityRows(film, '140')[0]?.picture).toBe('3840×1634');
  });

  it('starts from the daemon pick, else the best video and audio', () => {
    const info = youtube();
    expect(defaultPick(info)).toEqual({ video: '299', audio: '140' });
    // Without the daemon's audio, the first audio in its order (original language, not DRC),
    // never the one of the highest bit rate.
    expect(defaultPick({ ...info, selectedVideo: '', selectedAudio: '' })).toEqual({ video: '401', audio: '140' });
    expect(defaultPick({ ...info, selectedVideo: '18' })).toEqual({ video: '18', audio: '' });
    expect(defaultPick({ ...info, selectedVideo: '140' })).toEqual({ video: '401', audio: '140' });
    expect(defaultPick(undefined)).toEqual({ video: '', audio: '' });
  });

  it('asks for an audio track only for a video-only pick with several MP4 audios', () => {
    const info = youtube();
    expect(needsAudio(info, '299')).toBe(true);
    expect(needsAudio(info, '18')).toBe(false);
    expect(showsAudioChoice(info, { video: '299', audio: '140' })).toBe(true);
    expect(showsAudioChoice(info, { video: '18', audio: '' })).toBe(false);
    const one = { ...info, tracks: info.tracks.filter((t) => t.id !== '140-drc' && t.id !== '251-de') };
    expect(showsAudioChoice(one, { video: '299', audio: '140' })).toBe(false);
  });

  it('keeps the chosen audio across videos and sends none with a muxed one', () => {
    const info = youtube();
    const pick = withVideo(info, { video: '299', audio: '251-de' }, '401');
    expect(pick).toEqual({ video: '401', audio: '251-de' });
    expect(withVideo(info, { video: '18', audio: '' }, '299')).toEqual({ video: '299', audio: '140' });
    // A muxed default pick names no audio: a video-only quality gets the first audio.
    const muxedDefault = { ...info, selectedVideo: '18', selectedAudio: '' };
    expect(withVideo(muxedDefault, defaultPick(muxedDefault), '136')).toEqual({ video: '136', audio: '140' });
    expect(pickedAudio(info, { video: '18', audio: '140' })).toBe('');
    expect(pickedAudio(info, { video: '299', audio: '140' })).toBe('140');
  });

  it('sizes a pick from declared sizes, else from bandwidth and duration', () => {
    const info = youtube();
    expect(pickSize(info, { video: '299', audio: '140' })).toEqual({ bytes: 383 * MB, approx: false });
    expect(pickSize(info, { video: '399', audio: '140' })).toEqual({ bytes: 203 * MB, approx: true });
    expect(pickSize(info, { video: '136', audio: '140' })).toEqual({ bytes: Math.round((2_129_000 * 212_000) / 8000), approx: true });
    expect(pickSize({ ...info, durationMs: 0 }, { video: '136', audio: '140' })).toBeUndefined();
    expect(pickSize(info, { video: 'gone', audio: '' })).toBeUndefined();
    expect(sizeLabel(undefined)).toBe('');
  });
});

describe('labels', () => {
  it('names codecs as every UI does', () => {
    expect(['avc1.640028', 'avc3.4d', 'av01.0.08M.08', 'hvc1.2.4.L153', 'hev1.1', 'vp09.00.40.08', 'vp9', 'mp4a.40.2', 'opus', 'ac-3', 'ec-3', 'mp4v.20'].map(codecName)).toEqual([
      'H.264',
      'H.264',
      'AV1',
      'HEVC',
      'HEVC',
      'VP9',
      'VP9',
      'AAC',
      'Opus',
      'AC-3',
      'E-AC-3',
      'mp4v',
    ]);
    expect(codecsLabel({ codecs: 'avc1.42001E, mp4a.40.2' })).toBe('H.264 + AAC');
    expect(codecsLabel({ codecs: '' })).toBe('');
  });

  it('names a quality by downexd’s standard name, else by its picture, never by its height', () => {
    const q = (name: string, width: number, height: number, frameRate: number, kind: 'video' | 'muxed' = 'video') =>
      qualityName({ name, width, height, frameRate, kind });
    expect(q('4K (2160p) 60fps HDR', 3840, 2160, 60)).toBe('4K (2160p) 60fps HDR');
    expect(q('4K (2160p)', 3840, 1634, 24)).toBe('4K (2160p)');
    // An older downexd's names.
    expect(q('1634p', 3840, 1634, 24)).toBe('4K (2160p)');
    expect(q('2160p60 HDR', 3840, 2160, 60)).toBe('4K (2160p) 60fps HDR');
    expect(q('', 1920, 1080, 59.94)).toBe('1080p (Full HD) 60fps');
    expect(q('', 1280, 720, 30)).toBe('720p (HD)');
    expect(q('', 0, 0, 0, 'muxed')).toBe('Video and audio');
    expect(q('', 0, 0, 0)).toBe('Video');
    // An older downexd named HLS variants by their picture size.
    expect(q('1920x1080', 1920, 1080, 30)).toBe('1080p (Full HD)');
    expect(q('1280x720', 1280, 720, 60, 'muxed')).toBe('720p (HD) 60fps');
  });

  it('knows the same encoding in two sources: kind, picture, frame rate, codec family and HDR', () => {
    const a = { kind: 'video' as const, width: 1280, height: 720, frameRate: 29.97, codecs: 'avc1.4d401f', name: '' };
    // A master playlist and the variant the player loaded: the same quality.
    expect(qualityKey(a)).toBe(qualityKey({ ...a, frameRate: 30, codecs: 'avc1.64001f', name: '720p (HD)' }));
    expect(qualityKey(a)).toBe(qualityKey({ ...a, name: '1280x720' }));
    // Another video, or another encoding of it, is another quality even in the same class.
    expect(qualityKey(a)).not.toBe(qualityKey({ ...a, width: 1280, height: 534 }));
    expect(qualityKey(a)).not.toBe(qualityKey({ ...a, frameRate: 60 }));
    expect(qualityKey(a)).not.toBe(qualityKey({ ...a, codecs: 'hvc1.1.6.L93.B0' }));
    expect(qualityKey(a)).not.toBe(qualityKey({ ...a, kind: 'muxed', codecs: 'avc1.4d401f,mp4a.40.2' }));
    expect(qualityKey(a)).not.toBe(qualityKey({ ...a, name: '720p (HD) HDR' }));
    // Of an unknown picture: kind, frame rate, codec family and name.
    const u = { ...a, width: 0, height: 0 };
    expect(qualityKey(u)).toBe(qualityKey({ ...u, frameRate: 30, codecs: 'avc1.64001f' }));
    expect(qualityKey(u)).not.toBe(qualityKey({ ...u, kind: 'muxed' }));
    expect(qualityKey(u)).not.toBe(qualityKey(a));
    // A height alone is a picture too (an HLS variant whose RESOLUTION only an older downexd lost).
    const h = { ...a, width: 0 };
    expect(qualityKey(h)).toBe(qualityKey({ ...h, frameRate: 30 }));
    expect(qualityKey(h)).not.toBe(qualityKey(a));
  });

  it('formats sizes as the desktop does, in decimal units', () => {
    expect([0, 999, 1000, 1536, 412 * MB, 1.9 * 1000 * MB, 1023.97 * MB].map(formatBytes)).toEqual([
      '0 B',
      '999 B',
      '1.0 KB',
      '1.5 KB',
      '412 MB',
      '1.9 GB',
      '1.0 GB',
    ]);
    expect(formatBytes(-1)).toBe('');
    expect(formatBytes(Number.NaN)).toBe('');
  });

  it('uses 1 KB = 1,000 B and never shows 1000 of a unit', () => {
    const cases: [number, string][] = [
      [1_429_658_896, '1.4 GB'], // one decimal below 100, as before (was "1.3 GiB")
      [10_139_730, '10.1 MB'], // 9.67 MiB
      [660_602_880, '661 MB'], // 630 MiB
      [1_048_576, '1.0 MB'], // 1 MiB
      [99_949, '99.9 KB'],
      [99_950, '100 KB'], // would round to "100.0 KB": no decimal from 100, as for 100,000
      [99_999, '100 KB'],
      [100_000, '100 KB'],
      [99_950_000, '100 MB'],
      [99_999_999_999, '100 GB'],
      [99_960_000_000_000, '100 TB'],
      [999_499, '999 KB'],
      [999_500, '1.0 MB'], // would round to "1000 KB"
      [999_999, '1.0 MB'],
      [1_000_000, '1.0 MB'],
      [999_999_999, '1.0 GB'],
      [999.6, '1.0 KB'], // would round to "1000 B"
      [1e12, '1.0 TB'],
      [1.5e15, '1500 TB'], // the largest unit has no next one
    ];
    expect(cases.map(([b]) => formatBytes(b))).toEqual(cases.map(([, s]) => s));
    expect(sizeLabel({ bytes: 660_602_880, approx: true })).toBe('~661 MB');
  });

  it('labels audio tracks with their language when the name lacks it', () => {
    const [en, drc, de] = youtube().tracks.filter((t) => t.kind === 'audio');
    expect(audioLabel(en!)).toBe('English original · AAC · 129 kbit/s');
    expect(audioLabel(drc!)).toBe('English original, DRC · AAC · 129 kbit/s');
    expect(audioLabel(de!)).toBe('German · E-AC-3 · 384 kbit/s');
    expect(audioLabel(track({ id: 'x', kind: 'audio', language: 'fr', name: 'Commentary', codecs: 'mp4a.40.2' }))).toBe(
      'Commentary (French) · AAC',
    );
    expect(audioLabel(track({ id: 'y', kind: 'audio' }))).toBe('Audio');
  });
});
