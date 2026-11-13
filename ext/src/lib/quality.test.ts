// The tables of internal/media/quality_test.go (qualityNameCases, trackNameCases), case for case:
// the three copies of the naming rule must agree, and internal/media/quality_sync_test.go checks
// that these tables are Go's and that ext/src/lib and ui/frontend/src/lib hold the same files.

import { describe, expect, it } from 'vitest';
import {
  classLabel,
  compareInClass,
  onePerClass,
  parseStandardName,
  pictureText,
  type QualityTrack,
  qualityClass,
  standardName,
  trackQuality,
} from './quality';

/** width, height, frame rate, HDR → the standard name (qualityNameCases). */
const NAMES: readonly [number, number, number, boolean, string][] = [
  // The user's 2.35:1 film, every rung of its ladder.
  [3840, 1634, 24, false, '4K (2160p)'],
  [2560, 1090, 24, false, '1440p (2K)'],
  [1920, 818, 24, false, '1080p (Full HD)'],
  [1280, 544, 24, false, '720p (HD)'],
  [854, 364, 24, false, '480p'],
  [640, 272, 24, false, '360p'],
  [426, 182, 24, false, '240p'],
  [256, 128, 24, false, '144p'],
  // 16:9 pictures are their height.
  [7680, 4320, 30, false, '8K (4320p)'],
  [3840, 2160, 30, false, '4K (2160p)'],
  [2560, 1440, 30, false, '1440p (2K)'],
  [1920, 1080, 30, false, '1080p (Full HD)'],
  [1280, 720, 30, false, '720p (HD)'],
  [854, 480, 30, false, '480p'],
  [640, 360, 30, false, '360p'],
  [426, 240, 30, false, '240p'],
  [256, 144, 30, false, '144p'],
  // The frame rate when it rounds to more than 30, and HDR.
  [1920, 1080, 60, false, '1080p (Full HD) 60fps'],
  [1920, 1080, 59.94, false, '1080p (Full HD) 60fps'],
  [1920, 1080, 29.97, false, '1080p (Full HD)'],
  [1920, 1080, 30.4, false, '1080p (Full HD)'], // rounds to 30
  [1920, 1080, 30.5, false, '1080p (Full HD) 31fps'],
  [1280, 720, 50, false, '720p (HD) 50fps'],
  [3840, 2160, 60, true, '4K (2160p) 60fps HDR'],
  [3840, 1600, 24, true, '4K (2160p) HDR'],
  [2560, 1440, 0, true, '1440p (2K) HDR'],
  // Other wide shapes.
  [7680, 3264, 24, false, '8K (4320p)'],
  [1920, 800, 24, false, '1080p (Full HD)'],
  // Portrait: the longer side scaled to 16:9.
  [1080, 1920, 30, false, '1080p (Full HD)'],
  [720, 1280, 60, false, '720p (HD) 60fps'],
  [2160, 3840, 30, false, '4K (2160p)'],
  [1080, 1350, 30, false, '1080p (Full HD)'],
  // Odd sizes: 4:3 by its height, square, nearest step with ties up.
  [1440, 1080, 25, false, '1080p (Full HD)'],
  [960, 720, 30, false, '720p (HD)'],
  [640, 480, 25, false, '480p'],
  [1080, 1080, 30, false, '1080p (Full HD)'],
  [720, 576, 25, false, '480p'], // 576: 96 from 480, 144 from 720
  [1024, 576, 30, false, '480p'], // the same 576
  [1066, 600, 30, false, '720p (HD)'], // 600: as far from 480 as from 720
  [1600, 900, 30, false, '1080p (Full HD)'], // 900: as far from 720 as from 1080
  [2048, 1080, 24, false, '1080p (Full HD)'], // DCI 2K: round(2048 × 9/16) = 1152
  [4096, 2160, 24, false, '4K (2160p)'],
  // Off the ladder.
  [15360, 8640, 30, false, '8K (4320p)'],
  [16384, 16384, 0, false, '8K (4320p)'],
  [320, 180, 30, false, '144p'],
  [176, 144, 15, false, '144p'],
  [160, 90, 30, false, '144p'],
  [100, 50, 0, false, '144p'],
  // Half a picture: the side that is known.
  [0, 720, 30, false, '720p (HD)'],
  [1920, 0, 30, false, '1080p (Full HD)'],
  // Unknown.
  [0, 0, 60, true, ''],
  [-1, -1, 0, false, ''],
];

/**
 * A track's name, width, height, frame rate → the name every UI shows (trackNameCases): downexd's
 * standard name as it is; an older downexd's name (a raw height, "1080p60", a manifest's
 * "1920x1080") or none named again from the picture; any other name kept where the picture is
 * unknown.
 */
const TRACK_NAMES: readonly [string, number, number, number, string][] = [
  ['4K (2160p) 60fps HDR', 3840, 1634, 60, '4K (2160p) 60fps HDR'],
  ['1080p (Full HD)', 1440, 1080, 30, '1080p (Full HD)'],
  ['480p', 854, 364, 30, '480p'], // a standard name that is not the raw height
  ['720p (HD)', 1920, 1080, 30, '720p (HD)'], // YouTube's own label, kept
  ['1634p', 3840, 1634, 24, '4K (2160p)'],
  ['1090p', 2560, 1090, 24, '1440p (2K)'],
  ['2160p60 HDR', 3840, 2160, 60, '4K (2160p) 60fps HDR'],
  ['1080p60', 1920, 1080, 60, '1080p (Full HD) 60fps'],
  ['1920x1080', 1920, 1080, 30, '1080p (Full HD)'],
  ['1920x1080', 1920, 1080, 30.2, '1080p (Full HD)'],
  ['HD', 1280, 720, 30, '720p (HD)'],
  ['', 1080, 1920, 30, '1080p (Full HD)'],
  ['', 1920, 0, 30, '1080p (Full HD)'],
  // An older downexd's raw height that looks like a standard name.
  ['480p', 1138, 480, 24, '720p (HD)'],
  ['360p', 852, 360, 24, '480p'],
  ['240p', 568, 240, 24, '360p'],
  ['480p HDR', 1138, 480, 24, '720p (HD) HDR'],
  ['480p', 0, 480, 24, '480p'],
  ['480p', 854, 480, 24, '480p'],
  // The picture unknown: the name as it is.
  ['', 0, 0, 30, ''],
  ['Main', 0, 0, 30, 'Main'],
  ['1634p', 0, 0, 24, '1634p'],
];

describe('standard quality names', () => {
  it.each(NAMES)('%i×%i at %d fps (HDR %s) is "%s"', (width, height, fps, hdr, want) => {
    expect(standardName(qualityClass(width, height), fps, hdr)).toBe(want);
  });

  it('labels each class and nothing else', () => {
    expect([4320, 2160, 1440, 1080, 720, 480, 360, 240, 144, 0, 1634].map(classLabel)).toEqual([
      '8K (4320p)',
      '4K (2160p)',
      '1440p (2K)',
      '1080p (Full HD)',
      '720p (HD)',
      '480p',
      '360p',
      '240p',
      '144p',
      '',
      '',
    ]);
  });

  it('takes apart standard names and nothing else', () => {
    expect(parseStandardName('4K (2160p) 60fps HDR')).toEqual({ cls: 2160, fps: 60, hdr: true });
    expect(parseStandardName('1080p (Full HD)')).toEqual({ cls: 1080, fps: 0, hdr: false });
    expect(parseStandardName(' 480p ')).toEqual({ cls: 480, fps: 0, hdr: false });
    expect(parseStandardName('8K (4320p) HDR')).toEqual({ cls: 4320, fps: 0, hdr: true });
    expect(parseStandardName('720p (HD) 50fps')).toEqual({ cls: 720, fps: 50, hdr: false });
    expect(parseStandardName('144p 120fps')).toEqual({ cls: 144, fps: 120, hdr: false });
    // An older downexd's names, a manifest's labels, an audio note, near misses.
    for (const old of [
      '1634p',
      '1080p60',
      '1080p HDR',
      '2160p60 HDR',
      '1920x1080',
      '4K',
      'HD',
      'English original',
      '',
      '720p (HD) 5fps',
      '720p (HD) 1000fps',
      '720p (HD) 060fps',
      '720p (HD) HDR 60fps',
      '720p (HD)  60fps',
      '720p (hd)',
    ]) {
      expect(parseStandardName(old)).toBeUndefined();
    }
    // Every name standardName makes is read back.
    for (const [w, h, fps, hdr, name] of NAMES) {
      if (!name) continue;
      expect(parseStandardName(name)).toEqual({ cls: qualityClass(w, h), fps: Math.round(fps) > 30 ? Math.round(fps) : 0, hdr });
    }
  });
});

describe('a track’s quality', () => {
  it.each(TRACK_NAMES)('"%s" of %i×%i at %d fps shows as "%s"', (name, width, height, frameRate, want) => {
    expect(trackQuality({ name, width, height, frameRate }).name).toBe(want);
  });

  it('gives the class and HDR with the name', () => {
    const t = (name: string, width: number, height: number, frameRate = 30) => ({ name, width, height, frameRate });
    expect(trackQuality(t('4K (2160p) 60fps HDR', 3840, 1634, 60))).toEqual({ cls: 2160, name: '4K (2160p) 60fps HDR', hdr: true });
    expect(trackQuality(t('720p (HD)', 1920, 1080))).toEqual({ cls: 720, name: '720p (HD)', hdr: false });
    expect(trackQuality(t('2160p60 HDR', 3840, 2160, 60))).toEqual({ cls: 2160, name: '4K (2160p) 60fps HDR', hdr: true });
    expect(trackQuality(t('480p HDR', 1138, 480, 24))).toEqual({ cls: 720, name: '720p (HD) HDR', hdr: true });
    expect(trackQuality(t(' Main ', 0, 0))).toEqual({ cls: 0, name: 'Main', hdr: false });
  });

  it('gives the picture as the secondary detail', () => {
    expect(pictureText({ width: 3840, height: 1634 })).toBe('3840×1634');
    expect(pictureText({ width: 0, height: 720 })).toBe('');
  });
});

describe('one quality per class', () => {
  const v = (id: string, over: Partial<QualityTrack>): QualityTrack => ({
    id,
    kind: 'video',
    name: '',
    width: 0,
    height: 0,
    frameRate: 24,
    codecs: 'avc1.640028',
    bandwidth: 1_000_000,
    size: 0,
    ...over,
  });
  const film = (w: number, h: number) => ({ width: w, height: h });

  /**
   * The user's 3840×1634 film as an older downexd listed it (15 rows): H.264 and AV1 at each
   * class, HDR copies, a muxed 18, and the audios.
   */
  const userFilm: QualityTrack[] = [
    v('401', { ...film(3840, 1634), codecs: 'av01.0.12M.08', bandwidth: 14e6, name: '1634p' }),
    v('701', { ...film(3840, 1634), codecs: 'av01.0.13M.10.0.110.09.16.09.0', bandwidth: 18e6, name: '1634p HDR' }),
    v('400', { ...film(2560, 1090), codecs: 'av01.0.12M.08', bandwidth: 7e6, name: '1090p' }),
    v('700', { ...film(2560, 1090), codecs: 'av01.0.12M.10.0.110.09.16.09.0', bandwidth: 9e6, name: '1090p HDR' }),
    v('137', { ...film(1920, 818), codecs: 'avc1.640028', bandwidth: 4e6, name: '818p' }),
    v('399', { ...film(1920, 818), codecs: 'av01.0.08M.08', bandwidth: 2e6, name: '818p' }),
    v('136', { ...film(1280, 544), codecs: 'avc1.4d401f', bandwidth: 2e6, name: '544p' }),
    v('398', { ...film(1280, 544), codecs: 'av01.0.05M.08', bandwidth: 1e6, name: '544p' }),
    v('135', { ...film(854, 364), codecs: 'avc1.4d401e', bandwidth: 1e6, name: '364p' }),
    v('397', { ...film(854, 364), codecs: 'av01.0.04M.08', bandwidth: 6e5, name: '364p' }),
    v('134', { ...film(640, 272), codecs: 'avc1.4d4015', bandwidth: 6e5, name: '272p' }),
    v('18', { ...film(640, 272), kind: 'muxed', codecs: 'avc1.42001E, mp4a.40.2', bandwidth: 7e5, name: '272p' }),
    v('396', { ...film(640, 272), codecs: 'av01.0.01M.08', bandwidth: 4e5, name: '272p' }),
    v('395', { ...film(426, 182), codecs: 'av01.0.00M.08', bandwidth: 2e5, name: '182p' }),
    v('394', { ...film(256, 108), codecs: 'av01.0.00M.08', bandwidth: 1e5, name: '108p' }),
    v('140', { kind: 'audio', codecs: 'mp4a.40.2', bandwidth: 129_000, name: 'English original' }),
  ];

  it('lists the user’s film once per class with the codec that plays everywhere', () => {
    const rows = onePerClass(userFilm);
    expect(rows.map((t) => [t.id, trackQuality(t).name])).toEqual([
      ['401', '4K (2160p)'], // AV1 is the only SDR 4K: the HDR copy is dropped
      ['400', '1440p (2K)'],
      ['137', '1080p (Full HD)'], // H.264 before AV1
      ['136', '720p (HD)'],
      ['135', '480p'],
      ['134', '360p'], // the video-only H.264, not the muxed 18
      ['395', '240p'],
      ['394', '144p'],
    ]);
  });

  it('keeps downexd’s own pick of its class', () => {
    expect(onePerClass(userFilm, '399').map((t) => t.id)).toEqual(['401', '400', '399', '136', '135', '134', '395', '394']);
    expect(onePerClass(userFilm, '18').map((t) => t.id)).toContain('18');
    expect(onePerClass(userFilm, 'gone')).toEqual(onePerClass(userFilm));
  });

  it('chooses within a class as downexd does: video only, SDR, frame rate, H.264, bitrate, picture, size', () => {
    const hd = film(1920, 1080);
    const muxed = v('m', { ...hd, kind: 'muxed', codecs: 'avc1.640028,mp4a.40.2', bandwidth: 9e6 });
    const av1 = v('a', { ...hd, codecs: 'av01.0.08M.08' });
    const hevc = v('h', { ...hd, codecs: 'hvc1.1.6.L120.90' });
    const avc = v('v', { ...hd, codecs: 'avc1.640028' });
    const avcFast = v('f', { ...hd, codecs: 'avc1.640028', bandwidth: 2e6 });
    const av1Sixty = v('s', { ...hd, codecs: 'av01.0.09M.08', frameRate: 60 });
    const hdr = v('x', { ...hd, codecs: 'avc1.640028', frameRate: 60, name: '1080p (Full HD) 60fps HDR' });
    expect(onePerClass([muxed, av1])[0]?.id).toBe('a');
    expect(onePerClass([muxed])[0]?.id).toBe('m'); // the only one of its class
    expect(onePerClass([hdr, av1])[0]?.id).toBe('a');
    expect(onePerClass([hdr])[0]?.id).toBe('x'); // only HDR exists
    expect(onePerClass([av1, av1Sixty, avc])[0]?.id).toBe('s');
    expect(onePerClass([av1, hevc, avc])[0]?.id).toBe('v');
    expect(onePerClass([av1, hevc])[0]?.id).toBe('h');
    expect(onePerClass([avc, avcFast])[0]?.id).toBe('f');
    expect(compareInClass(avc, { ...avc, id: 'w' })).toBe(0);
    // The frame rate is compared rounded: H.264 at 59.94 before AV1 at 60.
    const avc5994 = v('n', { ...hd, codecs: 'avc1.64002a', frameRate: 59.94, bandwidth: 4e6 });
    const av160 = v('o', { ...hd, codecs: 'av01.0.09M.08', frameRate: 60, bandwidth: 3e6 });
    expect(onePerClass([av160, avc5994])[0]?.id).toBe('n');
    // Equal otherwise: the larger picture, then the larger size, then the order given.
    const small = v('small', { ...film(1440, 1080) });
    expect(onePerClass([small, avc])[0]?.id).toBe('v');
    expect(onePerClass([v('s1', { ...hd, size: 100 }), v('s2', { ...hd, size: 200 })])[0]?.id).toBe('s2');
    expect(onePerClass([v('first', hd), v('second', hd)])[0]?.id).toBe('first');
  });

  it('orders classes best first and keeps tracks of unknown picture after them', () => {
    const unknownA = v('u1', { name: 'Main' });
    const unknownB = v('u2', { kind: 'muxed' });
    const tracks = [unknownA, v('sd', film(640, 360)), unknownB, v('hd', film(1280, 720)), v('fhd', { ...film(1080, 1920), name: '' })];
    expect(onePerClass(tracks).map((t) => t.id)).toEqual(['fhd', 'hd', 'sd', 'u1', 'u2']);
  });

  it('names downexd’s one-per-class list as it comes', () => {
    const listed = [
      v('401', { ...film(3840, 1634), codecs: 'av01.0.12M.08', name: '4K (2160p)' }),
      v('137', { ...film(1920, 818), name: '1080p (Full HD)' }),
      v('18', { ...film(640, 272), kind: 'muxed', codecs: 'avc1.42001E, mp4a.40.2', name: '360p' }),
    ];
    expect(onePerClass(listed, '401')).toEqual(listed);
  });
});
