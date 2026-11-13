import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from './api';
import { mediaBadge, progressSummary, rowDescription, statusText } from './display';
import { etaSeconds, formatBitrate, formatDuration, progressFraction } from './format';
import {
  REFUSALS,
  SITE_RETRY_MS,
  SITE_TEXTS,
  audioOnlyOffered,
  audioTrackLabel,
  audioTrackTitle,
  audioTracks,
  cannotRetry,
  chosenTracks,
  codecName,
  codecNames,
  codecSummary,
  containerLabel,
  defaultPick,
  estimatedBytes,
  formatLabel,
  isSite,
  isSiteDownload,
  looksLikeManifest,
  mediaErrorText,
  mediaRefusal,
  needsAudio,
  needsToolsSettings,
  pickSize,
  pickSizeText,
  pickedAudio,
  qualityName,
  retrySite,
  siteErrorHint,
  siteErrorText,
  siteQualityText,
  siteRetry,
  siteVideoLabel,
  splitCode,
  siteQualityLabel,
  videoPictureText,
  videoTrackLabel,
  videoTracks,
  withVideo,
} from './media';
import { MiB, makeMediaDownload, makeMediaInfo, makeSiteDownload, makeSiteMediaInfo, makeTrack } from './testing';

afterEach(() => {
  vi.useRealTimers();
});

const info = makeMediaInfo();
const byId = (id: string) => info.tracks.find((t) => t.id === id)!;

describe('track order and labels', () => {
  it('sorts video by picture size, then bandwidth; audio by language, then bandwidth', () => {
    expect(videoTracks(info.tracks).map((t) => t.id)).toEqual(['v1', 'v2', 'v0']);
    const tracks = [
      ...info.tracks,
      { ...byId('r0'), id: 'r2', bandwidth: 256_000, name: 'English (high)' },
      { ...byId('r0'), id: 'r3', language: '', name: 'Unlabelled' },
    ];
    // English before German (by the language's name), higher bandwidth first; undeclared last
    expect(audioTracks(tracks).map((t) => t.id)).toEqual(['r2', 'r0', 'r1', 'r3']);
  });

  it('names video by its standard quality, codecs and bitrate, the picture apart', () => {
    expect(videoTrackLabel(byId('v1'))).toBe('1080p (Full HD) · H.264 · 5.0 Mbit/s');
    // 960×540 is nearer to 480 than to 720; a muxed track names its sound's codec too.
    expect(videoTrackLabel(byId('v2'))).toBe('480p · H.264 + AAC · 1.2 Mbit/s');
    expect(videoTrackLabel({ ...byId('v0'), width: 0, height: 0, frameRate: 0, bandwidth: 0, codecs: '' })).toBe('Video');
    expect(videoTrackLabel({ ...byId('v2'), width: 0, height: 0, frameRate: 0, bandwidth: 0, codecs: '' })).toBe('Video and audio');
    expect(videoTrackLabel({ ...byId('v0'), width: 0, height: 720 })).toContain('720p (HD)');
    // A manifest's own label is replaced by the standard name of its picture.
    expect(videoTrackLabel({ ...byId('v1'), name: '1920x1080' })).toBe('1080p (Full HD) · H.264 · 5.0 Mbit/s');
    expect(videoTrackLabel({ ...byId('v1'), width: 3840, height: 1634, frameRate: 60, name: '' })).toBe('4K (2160p) 60fps · H.264 · 5.0 Mbit/s');
    expect(videoPictureText(byId('v1'))).toBe('1920×1080 · 29.97 fps');
    expect(videoPictureText(byId('v1'), true)).toBe('1920×1080');
    expect(videoPictureText({ ...byId('v1'), frameRate: 0 })).toBe('1920×1080');
    expect(videoPictureText({ ...byId('v1'), width: 0 })).toBe('');
  });

  it('lists one manifest quality per class: video only, SDR, frame rate, H.264, bitrate', () => {
    const tracks = [
      makeTrack({ id: 'hevc', kind: 'video', width: 1920, height: 1080, frameRate: 30, bandwidth: 4_000_000, codecs: 'hvc1.1.6.L120.90' }),
      makeTrack({ id: 'avc', kind: 'video', width: 1920, height: 1080, frameRate: 30, bandwidth: 6_000_000, codecs: 'avc1.640028' }),
      makeTrack({ id: 'wide', kind: 'video', width: 1920, height: 800, frameRate: 30, bandwidth: 3_000_000, codecs: 'avc1.640028' }),
      makeTrack({ id: 'mux', kind: 'muxed', width: 1280, height: 720, frameRate: 30, bandwidth: 3_000_000, codecs: 'avc1.4d401f,mp4a.40.2' }),
      makeTrack({ id: 'hd', kind: 'video', width: 1280, height: 720, frameRate: 30, bandwidth: 2_500_000, codecs: 'avc1.4d401f' }),
      makeTrack({ id: 'a', kind: 'audio', bandwidth: 128_000, codecs: 'mp4a.40.2' }),
    ];
    expect(videoTracks(tracks).map((t) => t.id)).toEqual(['avc', 'hd']);
    // The daemon's pick stays the quality of its class.
    expect(videoTracks(tracks, false, 'wide').map((t) => t.id)).toEqual(['wide', 'hd']);
    expect(videoTracks(tracks, false, 'mux').map((t) => t.id)).toEqual(['avc', 'mux']);
  });

  it('describes audio by name and language and bitrate', () => {
    expect(audioTrackLabel(byId('r0'))).toBe('English · 128 kbit/s · mp4a.40.2');
    expect(audioTrackLabel({ ...byId('r0'), name: 'Commentary' })).toBe('Commentary (English) · 128 kbit/s · mp4a.40.2');
    expect(audioTrackLabel({ ...byId('r1'), name: '' })).toBe('German · 96 kbit/s · mp4a.40.2');
    expect(audioTrackLabel({ ...byId('r1'), name: '', language: '', bandwidth: 0, codecs: '' })).toBe('Audio');
    // a page's track: yt-dlp's quality words in the format note are not a name
    expect(audioTrackTitle({ name: 'medium', language: '' }, true)).toBe('Audio');
    expect(audioTrackTitle({ name: 'low, DRC', language: 'en' }, true)).toBe('English · DRC');
    expect(audioTrackTitle({ name: 'English original (default), medium', language: 'en' }, true)).toBe('English original (default)');
    expect(audioTrackLabel({ ...byId('r0'), name: 'medium', language: '', codecs: 'mp4a.40.2', bandwidth: 129_000 }, true)).toBe(
      'Audio · AAC · 129 kbit/s',
    );
    // a manifest's rendition keeps its own name
    expect(audioTrackTitle({ name: 'medium', language: '' })).toBe('medium');
  });

  it('formats bitrates, lengths, formats and containers', () => {
    expect(formatBitrate(0)).toBe('');
    expect(formatBitrate(640)).toBe('640 bit/s');
    expect(formatBitrate(96_000)).toBe('96 kbit/s');
    expect(formatBitrate(999_499)).toBe('999 kbit/s');
    expect(formatBitrate(999_600)).toBe('1.0 Mbit/s');
    expect(formatBitrate(14_250_000)).toBe('14.3 Mbit/s');
    expect(formatBitrate(120_000_000)).toBe('120 Mbit/s');
    expect(formatDuration(0)).toBe('—');
    expect(formatDuration(65_400)).toBe('1:05');
    expect(formatDuration(5_400_250)).toBe('1:30:00');
    expect(formatLabel('hls')).toBe('HLS');
    expect(formatLabel('')).toBe('');
    expect(containerLabel('ts')).toBe('MPEG-TS');
    expect(containerLabel('mp4')).toBe('MP4');
    expect(containerLabel('')).toBe('');
  });
});

describe('picking tracks', () => {
  it("starts from the daemon's pick, else the best tracks", () => {
    expect(defaultPick(info)).toEqual({ video: 'v1', audio: 'r0' });
    expect(defaultPick({ ...info, selectedVideo: '', selectedAudio: '' })).toEqual({ video: 'v1', audio: 'r0' });
    // a pick naming a track that is not offered falls back
    expect(defaultPick({ ...info, selectedVideo: 'r1', selectedAudio: 'v0' })).toEqual({ video: 'v1', audio: 'r0' });
    expect(defaultPick({ ...info, selectedVideo: 'v2', selectedAudio: 'r1' })).toEqual({ video: 'v2', audio: '' });
    expect(defaultPick({ ...info, tracks: info.tracks.filter((t) => t.kind === 'audio') })).toEqual({ video: '', audio: 'r0' });
    expect(defaultPick(null)).toEqual({ video: '', audio: '' });
  });

  it("keeps the daemon's audio-only pick, which leaves out videos it cannot save", () => {
    const audioOnly = { ...info, selectedVideo: '', selectedAudio: 'r1' };
    expect(defaultPick(audioOnly)).toEqual({ video: '', audio: 'r1' });
    expect(audioOnlyOffered(audioOnly)).toBe(true);
    // "" asks the daemon for its own pick: "no video" is that only when its pick has none
    expect(audioOnlyOffered(info)).toBe(false);
    expect(audioOnlyOffered({ ...info, selectedVideo: '', selectedAudio: '' })).toBe(false);
    expect(audioOnlyOffered({ ...info, selectedVideo: '', selectedAudio: 'v0' })).toBe(false);
    expect(needsAudio(info, '')).toBe(true);
    expect(pickedAudio(info, { video: '', audio: 'r1' })).toBe('r1');
    expect(withVideo(audioOnly, { video: 'v2', audio: 'r1' }, '')).toEqual({ video: '', audio: 'r1' });
    expect(withVideo(audioOnly, { video: 'v2', audio: '' }, '')).toEqual({ video: '', audio: 'r0' });
    // 96 kbit/s × 600 s / 8
    expect(estimatedBytes(info, { video: '', audio: 'r1' })).toBe(7_200_000);
  });

  it('needs no audio track with a muxed video, and picks one again after it', () => {
    expect(needsAudio(info, 'v1')).toBe(true);
    expect(needsAudio(info, 'v2')).toBe(false);
    expect(pickedAudio(info, { video: 'v2', audio: 'r1' })).toBe('');
    expect(pickedAudio(info, { video: 'v0', audio: 'r1' })).toBe('r1');
    const muxed = withVideo(info, { video: 'v1', audio: 'r1' }, 'v2');
    expect(muxed).toEqual({ video: 'v2', audio: 'r1' }); // kept for when the user goes back
    expect(withVideo(info, { video: 'v2', audio: '' }, 'v0')).toEqual({ video: 'v0', audio: 'r0' });
    expect(needsAudio({ tracks: info.tracks.filter((t) => t.kind !== 'audio') }, 'v1')).toBe(false);
  });

  it('estimates the size from bandwidth and length', () => {
    // (5,000,000 + 128,000) bit/s × 600 s / 8
    expect(estimatedBytes(info, { video: 'v1', audio: 'r0' })).toBe(384_600_000);
    expect(estimatedBytes(info, { video: 'v2', audio: 'r0' })).toBe(90_000_000); // muxed: its own sound
    expect(estimatedBytes({ ...info, durationMs: 0 }, { video: 'v1', audio: 'r0' })).toBe(-1);
    expect(estimatedBytes(info, { video: '', audio: '' })).toBe(-1);
  });

  it("labels a download's chosen tracks", () => {
    expect(chosenTracks(info)).toEqual({ video: '1080p (Full HD) · H.264 · 5.0 Mbit/s · 1920×1080 · 29.97 fps', audio: audioTrackLabel(byId('r0')) });
    expect(chosenTracks({ ...info, selectedVideo: 'v2', selectedAudio: '' }).audio).toBe('In the video track');
  });
});

describe('progress of a media download', () => {
  it('counts fragments, and estimates the time left from the bytes they took', () => {
    const d = makeMediaDownload({ id: 1, state: 'downloading', received: 100 * MiB, speedBps: 10 * MiB, media: makeMediaInfo({ fragmentsDone: 50 }) });
    expect(progressFraction(d)).toBe(0.25);
    expect(etaSeconds(d)).toBeCloseTo(30); // 400 MiB in all, 300 MiB left at 10 MiB/s
    expect(etaSeconds({ ...d, media: makeMediaInfo({ fragmentsDone: 0 }) })).toBeNaN();
    expect(progressFraction({ ...d, state: 'completed' })).toBe(1);
    expect(progressFraction({ ...d, media: makeMediaInfo({ fragmentsTotal: 0 }) })).toBeNaN(); // size -1
  });

  it('reads out fragments and marks the row as a stream', () => {
    const d = makeMediaDownload({ id: 1, state: 'downloading', received: 100 * MiB, speedBps: 10 * MiB, media: makeMediaInfo({ fragmentsDone: 50 }) });
    expect(progressSummary(d, false)).toBe('Keynote.mp4: Downloading, 25%, 50 of 200 fragments, 105 MB, 10.5 MB/s, 30s left');
    expect(rowDescription(d)).toMatch(/^Size unknown, category Video, added .+, HLS stream$/);
    expect(mediaBadge({ media: makeMediaInfo({ format: 'dash' }) })).toBe('DASH');
    expect(mediaBadge({ media: null })).toBe('Media');
  });

  it("measures a web page's video by its bytes in the list, as the progress window does", () => {
    // 115 of 460 MiB (downexd's BytesTotal): 25%, though 11 of 46 parts would be 23%
    const media = makeSiteMediaInfo({ fragmentsTotal: 46, fragmentsDone: 11 });
    const d = makeSiteDownload({ id: 1, state: 'downloading', size: 460 * MiB, received: 115 * MiB, speedBps: 23 * MiB, media });
    expect(progressFraction(d)).toBe(0.25);
    expect(etaSeconds(d)).toBeCloseTo(15); // 345 MiB left at 23 MiB/s
    expect(progressSummary(d, false)).toBe('A talk.mp4: Downloading, 25%, 121 MB of 482 MB, 24.1 MB/s, 15s left');
    // Before downexd knows the sizes the progress is indeterminate: uneven parts never measure it.
    const unsized = { ...d, size: -1 };
    expect(progressFraction(unsized)).toBeNaN();
    expect(progressSummary(unsized, false)).not.toMatch(/%/);
    expect(progressSummary(unsized, false)).toMatch(/^A talk\.mp4: Downloading, .*121 MB, 24\.1 MB\/s/);
  });

  it('names the site in the row and while yt-dlp reads the page', () => {
    const d = makeSiteDownload({ id: 1, state: 'probing' });
    expect(statusText(d)).toBe('Getting video info');
    expect(statusText(makeMediaDownload({ id: 2, state: 'probing' }))).toBe('Connecting');
    expect(mediaBadge(d)).toBe('YouTube');
    expect(mediaBadge({ media: makeSiteMediaInfo({ site: ' ' }) })).toBe('Video');
    expect(rowDescription(d)).toMatch(/^Size unknown, category Video, added .+, YouTube video$/);
  });
});

describe('refusals', () => {
  it('recognises the encrypted and live refusals', () => {
    expect(mediaRefusal(new ApiError('invalid_argument', 'encrypted: the stream is encrypted or DRM-protected'))).toBe('encrypted');
    expect(mediaRefusal('invalid_argument: media: content is encrypted or DRM-protected')).toBe('encrypted');
    expect(mediaRefusal('invalid_argument: live: the presentation is live')).toBe('live');
    expect(mediaRefusal('invalid_argument: manifest_url must be http(s)')).toBeNull();
    expect(mediaRefusal('unavailable: live connection lost')).toBeNull();
    expect(mediaErrorText('invalid_argument: encrypted')).toBe(REFUSALS.encrypted);
    expect(mediaErrorText('failed_precondition: manifest: 404')).toBe('manifest: 404');
  });

  it('tells manifests from other addresses', () => {
    expect(looksLikeManifest('https://cdn.example.com/v/master.m3u8?token=1')).toBe(true);
    expect(looksLikeManifest(' https://cdn.example.com/v/Manifest.MPD ')).toBe(true);
    expect(looksLikeManifest('https://cdn.example.com/v/video.mp4')).toBe(false);
    expect(looksLikeManifest('https://cdn.example.com/get?file=a.m3u8')).toBe(false);
    expect(looksLikeManifest('ftp://cdn.example.com/a.m3u8')).toBe(false);
    expect(looksLikeManifest('not a url')).toBe(false);
  });
});

describe('video pages: picker (SITE-MEDIA-SPEC §3.11, §5.3)', () => {
  const site = makeSiteMediaInfo();
  const track = (id: string) => site.tracks.find((t) => t.id === id)!;

  it('tells a web page from a manifest', () => {
    expect(isSite(site)).toBe(true);
    expect(isSite(info)).toBe(false);
    expect(isSite(null)).toBe(false);
    expect(isSiteDownload(makeSiteDownload({ id: 1 }))).toBe(true);
    expect(isSiteDownload(makeMediaDownload({ id: 2 }))).toBe(false);
    expect(siteVideoLabel(site)).toBe('YouTube video');
    expect(siteVideoLabel({ site: ' ' })).toBe('Video');
  });

  it('names the codecs the same way in every UI', () => {
    expect(codecName('avc1.640028')).toBe('H.264');
    expect(codecName('avc3.4d401f')).toBe('H.264');
    expect(codecName('av01.0.08M.08')).toBe('AV1');
    expect(codecName('hvc1.2.4.L153')).toBe('HEVC');
    expect(codecName('hev1.1.6.L93')).toBe('HEVC');
    expect(codecName('vp09.00.40.08')).toBe('VP9');
    expect(codecName('mp4a.40.2')).toBe('AAC');
    expect(codecName('opus')).toBe('Opus');
    expect(codecName('ac-3')).toBe('AC-3');
    expect(codecName('ec-3')).toBe('E-AC-3');
    expect(codecName('flac')).toBe('flac');
    expect(codecNames('avc1.42001E, mp4a.40.2')).toBe('H.264 + AAC');
    expect(codecNames('')).toBe('');
  });

  it('keeps downexd’s one quality per class (best first), never offering audio only', () => {
    expect(videoTracks(site.tracks, true).map((t) => t.id)).toEqual(['299', '136', '397', '18']);
    // the order downexd chose is not re-sorted by bitrate or size
    expect(videoTracks(site.tracks).map((t) => t.id)).toEqual(['299', '136', '397', '18']);
    expect(audioOnlyOffered({ ...site, selectedVideo: '', selectedAudio: '140' })).toBe(false);
    expect(defaultPick(site)).toEqual({ video: '299', audio: '140' });
    // no pick from downexd: the first quality and the first audio (its default order)
    expect(defaultPick({ ...site, selectedVideo: '', selectedAudio: '' })).toEqual({ video: '299', audio: '140' });
    expect(withVideo(site, { video: '18', audio: '' }, '136')).toEqual({ video: '136', audio: '140' });
  });

  it('labels each quality with its name, codec and the site’s size', () => {
    expect(videoTrackLabel(track('299'), true)).toBe('1080p (Full HD) 60fps · H.264 · 472 MB');
    expect(videoTrackLabel(track('397'), true)).toBe('480p · AV1 · ~157 MB');
    expect(videoTrackLabel(track('18'), true)).toBe('360p · H.264 + AAC · 600 kbit/s'); // no size: its bitrate
    expect(videoTrackLabel({ ...track('299'), name: '' }, true)).toBe('1080p (Full HD) 60fps · H.264 · 472 MB');
    expect(audioTrackLabel(track('140'), true)).toBe('English original · AAC · 129 kbit/s · 10.5 MB');
    expect(chosenTracks(site)).toEqual({ video: '1080p (Full HD) 60fps · H.264 · 472 MB · 1920×1080', audio: 'English original · AAC · 129 kbit/s · 10.5 MB' });
    expect(siteQualityText(site)).toBe('1080p (Full HD) 60fps H.264 + AAC');
    expect(siteQualityText({ ...site, selectedVideo: '18', selectedAudio: '' })).toBe('360p H.264 + AAC');
    expect(siteQualityText({ ...site, selectedVideo: '' })).toBe('');
  });

  it('names a quality by downexd’s standard name, else by its picture, never by its height', () => {
    expect(qualityName({ name: '4K (2160p) 60fps HDR', width: 3840, height: 1634, frameRate: 60 })).toBe('4K (2160p) 60fps HDR');
    // An older downexd named qualities by their height: the picture names them now.
    expect(qualityName({ name: '1634p', width: 3840, height: 1634, frameRate: 24 })).toBe('4K (2160p)');
    expect(qualityName({ name: '2160p HDR', width: 3840, height: 2160, frameRate: 30 })).toBe('4K (2160p) HDR');
    expect(qualityName({ name: '1080p60', width: 1920, height: 1080, frameRate: 60 })).toBe('1080p (Full HD) 60fps');
    expect(qualityName({ name: '', width: 0, height: 2160, frameRate: 30 })).toBe('4K (2160p)');
    expect(qualityName({ name: '', width: 0, height: 0, frameRate: 0 })).toBe('');
  });

  it('reduces an older downexd’s every-quality list of the user’s 3840×1634 film to one per class', () => {
    const old = makeSiteMediaInfo({
      selectedVideo: '401',
      tracks: [
        makeTrack({ id: '401', kind: 'video', width: 3840, height: 1634, frameRate: 24, codecs: 'av01.0.12M.08', name: '1634p', size: 860 * MiB }),
        makeTrack({ id: '701', kind: 'video', width: 3840, height: 1634, frameRate: 24, codecs: 'av01.0.12M.10', name: '1634p HDR', size: 990 * MiB }),
        makeTrack({ id: '137', kind: 'video', width: 1920, height: 818, frameRate: 24, codecs: 'avc1.640028', name: '818p', size: 200 * MiB }),
        makeTrack({ id: '399', kind: 'video', width: 1920, height: 818, frameRate: 24, codecs: 'av01.0.08M.08', name: '818p', size: 150 * MiB }),
        makeTrack({ id: '134', kind: 'video', width: 640, height: 272, frameRate: 24, codecs: 'avc1.4d4015', name: '272p', size: 30 * MiB }),
        makeTrack({ id: '18', kind: 'muxed', width: 640, height: 272, frameRate: 24, codecs: 'avc1.42001E, mp4a.40.2', name: '272p', size: 40 * MiB }),
        makeTrack({ id: '140', kind: 'audio', bandwidth: 129_000, codecs: 'mp4a.40.2', language: 'en', name: 'English original', size: 10 * MiB }),
      ],
    });
    const videos = videoTracks(old.tracks, true, old.selectedVideo);
    expect(videos.map((t) => [t.id, siteQualityLabel(old, defaultPick(old), t)])).toEqual([
      ['401', '4K (2160p) · AV1 · 912 MB'],
      ['137', '1080p (Full HD) · H.264 · 220 MB'],
      ['134', '360p · H.264 · 41.9 MB'],
    ]);
  });

  it('lists each quality with the size of the file it makes, as the browser picker does', () => {
    // The video with the audio it is paired with: 450 + 10 MiB, not the video alone.
    expect(siteQualityLabel(site, { video: '299', audio: '140' }, track('299'))).toBe('1080p (Full HD) 60fps · H.264 · 482 MB');
    expect(siteQualityLabel(site, { video: '299', audio: '140' }, track('397'))).toBe('480p · AV1 · ~168 MB');
    // A muxed quality carries its own sound: its own size (here bitrate × length).
    expect(siteQualityLabel(site, { video: '299', audio: '140' }, track('18'))).toBe('360p · H.264 + AAC · about 45.0 MB');
    // Without a length or sizes: the bitrate, as the track label says it.
    expect(siteQualityLabel({ ...site, durationMs: 0 }, { video: '299', audio: '140' }, track('18'))).toBe('360p · H.264 + AAC · 600 kbit/s');
  });

  it('adds the declared sizes when every chosen track has one, else estimates', () => {
    expect(pickSize(site, { video: '299', audio: '140' })).toEqual({ bytes: 460 * MiB, kind: 'exact' });
    expect(pickSize(site, { video: '397', audio: '140' })).toEqual({ bytes: 160 * MiB, kind: 'approx' });
    // the muxed quality has no size: bitrate × length
    expect(pickSize(site, { video: '18', audio: '140' })).toEqual({ bytes: 45_000_000, kind: 'estimate' });
    expect(pickSize({ ...site, durationMs: 0 }, { video: '18', audio: '' })).toEqual({ bytes: -1, kind: 'unknown' });
    expect(pickSizeText({ bytes: 460 * MiB, kind: 'exact' })).toBe('482 MB');
    expect(pickSizeText({ bytes: 160 * MiB, kind: 'approx' })).toBe('~168 MB');
    expect(pickSizeText({ bytes: 45_000_000, kind: 'estimate' })).toBe('about 45.0 MB');
    expect(pickSizeText({ bytes: -1, kind: 'unknown' })).toBe('');
    // a manifest's tracks have no sizes: the M5 estimate, as before
    expect(pickSize(info, { video: 'v1', audio: 'r0' })).toEqual({ bytes: 384_600_000, kind: 'estimate' });
    expect(pickSize({ tracks: [makeTrack({ id: 'a', kind: 'audio', size: 5 })], durationMs: 0 }, { video: '', audio: 'a' })).toEqual({ bytes: 5, kind: 'exact' });
  });

  it('says “about” for the size downexd measured of a manifest track without a bandwidth', () => {
    // A bare media playlist: no BANDWIDTH, its picture and size measured from its segments.
    const bare = makeMediaInfo({
      durationMs: 3_907_000,
      selectedVideo: 'v0',
      selectedAudio: '',
      tracks: [makeTrack({ id: 'v0', kind: 'muxed', width: 1920, height: 1080, frameRate: 25, codecs: 'avc1.640028,mp4a.40.2', name: '1080p (Full HD)', size: 2150 * MiB, sizeApprox: true })],
    });
    expect(pickSize(bare, { video: 'v0', audio: '' })).toEqual({ bytes: 2150 * MiB, kind: 'estimate' });
    expect(pickSizeText(pickSize(bare, { video: 'v0', audio: '' }))).toBe('about 2.3 GB');
    // Every segment a byte range: the exact size.
    const exact = { ...bare, tracks: [{ ...bare.tracks[0]!, size: 700 * MiB, sizeApprox: false }] };
    expect(pickSizeText(pickSize(exact, { video: 'v0', audio: '' }))).toBe('734 MB');
    // A web page's own estimate keeps its "~".
    expect(pickSize({ ...bare, origin: 'site' }, { video: 'v0', audio: '' })).toEqual({ bytes: 2150 * MiB, kind: 'approx' });
  });
});

describe('video pages: errors and retries (SITE-MEDIA-SPEC §3.2, §5.5)', () => {
  const err = (code: string, message: string) => new ApiError(code as 'failed_precondition', message);

  it('splits the stable code off a message', () => {
    expect(splitCode('site_error: yt-dlp could not read this page: HTTP 500')).toEqual({ code: 'site_error', rest: 'yt-dlp could not read this page: HTTP 500' });
    expect(splitCode('no code here')).toEqual({ code: '', rest: 'no code here' });
  });

  it('says every code as §5.5 does, with the reason where it helps', () => {
    expect(siteErrorText(err('invalid_argument', 'site_unsupported: no video found on this page'))).toBe(SITE_TEXTS.site_unsupported);
    expect(siteErrorText(err('failed_precondition', 'site_unavailable: this video is not available: private video'))).toBe(
      'This video is not available (private, removed, blocked in your country, or the site asks to try again later). Private video.',
    );
    expect(siteErrorText(err('failed_precondition', 'auth_required: the site asks to sign in'))).toBe(SITE_TEXTS.auth_required);
    expect(siteErrorText(err('invalid_argument', 'live: this is a live stream'))).toBe('This is a live stream. Downex downloads only finished videos.');
    expect(siteErrorText(err('invalid_argument', 'encrypted: DRM'))).toBe('This video is DRM-protected. Downex does not download protected content.');
    expect(siteErrorText(err('invalid_argument', 'media_unsupported: no MP4 video on this page'))).toBe('No MP4 video on this page.');
    expect(siteErrorText(err('invalid_argument', 'media_unsupported: a playlist'))).toBe('Open a single video of this playlist.');
    expect(siteErrorText(err('invalid_argument', 'media_invalid: no format 299'))).toBe('The chosen quality is no longer offered. Add the video again.');
    expect(siteErrorText(err('failed_precondition', 'site_error: yt-dlp could not read this page: HTTP Error 500'))).toBe(
      'yt-dlp could not read this page: HTTP Error 500.',
    );
    expect(siteErrorText(err('failed_precondition', 'site_error: yt-dlp could not read this page'))).toBe('yt-dlp could not read this page.');
    expect(siteErrorText(err('failed_precondition', 'tools_unavailable: yt-dlp and Deno are not set up: this Downex daemon does not download tools'))).toBe(
      'Downex could not set up yt-dlp/Deno: this Downex daemon does not download tools.',
    );
    expect(siteErrorText(err('failed_precondition', 'site_media_off: video downloads from web pages are turned off (Settings → Video sites)'))).toBe(
      'Video downloads from web pages are turned off (Settings → Video sites).',
    );
    expect(siteErrorText(err('failed_precondition', 'tools_installing: downloading yt-dlp and Deno (34%)'))).toBe('Downloading yt-dlp and Deno (34%)…');
    expect(siteErrorText(err('failed_precondition', 'site_resolving: still getting the video info'))).toBe('Getting video info…');
    expect(siteErrorText(err('unimplemented', 'not implemented'))).toBe('Update Downex to download videos.');
    expect(siteErrorText(err('unavailable', 'downexd is not running'))).toBe('downexd is not running');
    // the origin picks the texts
    expect(mediaErrorText('invalid_argument: live: this is a live stream', 'site')).toBe(SITE_TEXTS.live);
    expect(mediaErrorText('invalid_argument: live: this is a live stream')).toBe(REFUSALS.live);
    expect(needsToolsSettings('failed_precondition: tools_unavailable: x')).toBe(true);
    expect(needsToolsSettings('failed_precondition: site_media_off: x')).toBe(true);
    expect(needsToolsSettings('failed_precondition: site_error: x')).toBe(false);
  });

  it('recognises the answers that ask to try again', () => {
    expect(siteRetry('failed_precondition: tools_installing: downloading yt-dlp and Deno (34%)')).toEqual({ kind: 'tools_installing', fraction: 0.34 });
    expect(siteRetry('failed_precondition: tools_installing: downloading yt-dlp and Deno')).toEqual({ kind: 'tools_installing', fraction: Number.NaN });
    expect(siteRetry('failed_precondition: site_resolving: still getting the video info')).toEqual({ kind: 'site_resolving' });
    expect(siteRetry('failed_precondition: site_error: x')).toBeNull();
    expect(siteRetry('invalid_argument: site_resolving: x')).toBeNull();
  });

  it('hints at a failed download and knows when retrying cannot help', () => {
    expect(siteErrorHint('site_unavailable')).toBe(SITE_TEXTS.site_unavailable);
    expect(siteErrorHint('network')).toBe('');
    for (const code of ['encrypted', 'live', 'media_unsupported', 'media_invalid', 'site_unsupported']) expect(cannotRetry(code)).toBe(true);
    for (const code of ['site_unavailable', 'site_error', 'auth_required', 'tools_unavailable', 'network', '']) expect(cannotRetry(code)).toBe(false);
  });

  it('asks again at most every 2 s until an answer, joining the work that runs', async () => {
    vi.useFakeTimers();
    const answers: (string | number)[] = ['failed_precondition: tools_installing: downloading yt-dlp and Deno (10%)', 'failed_precondition: site_resolving: x', 42];
    const call = vi.fn(async () => {
      const next = answers.shift()!;
      if (typeof next === 'string') throw new Error(next);
      return next;
    });
    const waits: string[] = [];
    const done = retrySite(call, { onwait: (r) => waits.push(r.kind) });
    await vi.advanceTimersByTimeAsync(0);
    expect(call).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(SITE_RETRY_MS - 1);
    expect(call).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(call).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(SITE_RETRY_MS);
    expect(await done).toBe(42);
    expect(waits).toEqual(['tools_installing', 'site_resolving']);
  });

  it('passes other failures on at once, and stops when told to', async () => {
    vi.useFakeTimers();
    await expect(retrySite(async () => Promise.reject(new Error('failed_precondition: site_error: x')))).rejects.toThrow(/site_error/);
    const controller = new AbortController();
    const call = vi.fn(async () => Promise.reject(new Error('failed_precondition: site_resolving: x')));
    const done = retrySite(call, { signal: controller.signal });
    const result = done.then(
      () => 'resolved',
      (e: unknown) => (e instanceof ApiError ? e.code : 'other'),
    );
    await vi.advanceTimersByTimeAsync(500);
    controller.abort();
    await vi.advanceTimersByTimeAsync(10_000);
    expect(await result).toBe('canceled');
    expect(call).toHaveBeenCalledTimes(1);
  });

  it('gives up after its limit with the last answer', async () => {
    vi.useFakeTimers();
    const call = vi.fn(async () => Promise.reject(new Error('failed_precondition: site_resolving: x')));
    const done = retrySite(call).catch((e: unknown) => e);
    await vi.advanceTimersByTimeAsync(17 * 60_000);
    expect(await done).toMatchObject({ message: 'failed_precondition: site_resolving: x' });
    expect(call.mock.calls.length).toBeGreaterThan(400);
  });
});

describe('codecSummary', () => {
  it('says a quality in plain words: class, video codec, audio codec and bitrate', () => {
    const site = makeSiteMediaInfo();
    expect(codecSummary(site)).toBe('1080p · H.264 · AAC 129 kbit/s');
    expect(codecSummary(site, { video: '397', audio: '140' })).toBe('480p · AV1 · AAC 129 kbit/s');
    const hls = makeMediaInfo();
    expect(codecSummary(hls)).toBe('1080p · H.264 · AAC 128 kbit/s');
    expect(codecSummary(hls, { video: 'v0', audio: 'r1' })).toBe('360p · H.264 · AAC 96 kbit/s');
  });

  it("names a muxed track's second codec as its audio, without a bitrate of its own", () => {
    const site = makeSiteMediaInfo();
    expect(codecSummary(site, { video: '18', audio: '140' })).toBe('360p · H.264 · AAC');
    // 960×540 is in the 480p class (the class rule, never the raw height)
    expect(codecSummary(makeMediaInfo(), { video: 'v2', audio: '' })).toBe('480p · H.264 · AAC');
  });

  it('shortens 4K and frame-rate names to their class, and describes audio-only picks', () => {
    const uhd = makeTrack({ id: 'u', kind: 'video', width: 3840, height: 2160, frameRate: 60, codecs: 'av01.0.12M.08', name: '4K (2160p) 60fps' });
    const audio = makeTrack({ id: 'a', kind: 'audio', bandwidth: 160_000, codecs: 'opus' });
    const info = makeMediaInfo({ tracks: [uhd, audio], selectedVideo: 'u', selectedAudio: 'a' });
    expect(codecSummary(info)).toBe('4K · AV1 · Opus 160 kbit/s');
    expect(codecSummary(info, { video: '', audio: 'a' })).toBe('Opus 160 kbit/s');
    const bare = makeMediaInfo({ tracks: [makeTrack({ id: 'x', kind: 'audio' })], selectedVideo: '', selectedAudio: 'x' });
    expect(codecSummary(bare)).toBe('');
  });
});
