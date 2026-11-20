// Audio pages (SITE-MEDIA-SPEC §3.13): the curated audio sites' addresses (VIDEO-DETECT-SPEC
// §3.8), the rows of an answer without video, the popup's line and the refusal texts.
import { describe, expect, it } from 'vitest';
import { videoSubline } from '../entrypoints/popup/popup-view';
import { itemLabel, itemsCountText, itemsLeftOutText } from './item-strip';
import type { ProtocolError, VideoInfoResult, VideoTrack } from './protocol';
import { curatedPage, parseSiteVideo, siteOfHost, SITE_NAMES } from './site-video';
import { VIDEO_TEXTS, videoFailureText } from './ui-format';
import {
  AUDIO_QUALITY,
  audioOnlyList,
  codecName,
  defaultPick,
  fileTypeLabel,
  pickedAudio,
  pickSize,
  qualityRows,
  showsAudioChoice,
  withVideo,
} from './video-picker';

describe('audio sites (VIDEO-DETECT-SPEC §3.8)', () => {
  it.each([
    ['https://soundcloud.com/some-artist/a-track', 'soundcloud:some-artist/a-track', 'https://soundcloud.com/some-artist/a-track'],
    ['https://m.soundcloud.com/some-artist/a-track?in=x&si=y', 'soundcloud:some-artist/a-track', 'https://soundcloud.com/some-artist/a-track'],
    ['https://soundcloud.com/some_artist/A-Track_2/s-AbC123xyz', 'soundcloud:some_artist/A-Track_2', 'https://soundcloud.com/some_artist/A-Track_2/s-AbC123xyz'],
    ['https://some-artist.bandcamp.com/track/a-song', 'bandcamp:some-artist/a-song', 'https://some-artist.bandcamp.com/track/a-song'],
    ['https://www.mixcloud.com/SomeHost/a-mix-12-11-26/', 'mixcloud:SomeHost/a-mix-12-11-26', 'https://www.mixcloud.com/SomeHost/a-mix-12-11-26/'],
    ['https://m.mixcloud.com/SomeHost/a-mix', 'mixcloud:SomeHost/a-mix', 'https://www.mixcloud.com/SomeHost/a-mix/'],
  ])('%s is %s', (url, key, pageUrl) => {
    const v = parseSiteVideo(url);
    expect(v).toMatchObject({ key, kind: 'audio', pageUrl, lookAhead: false, resolver: 'yt-dlp' });
    expect(parseSiteVideo(v?.pageUrl)?.key).toBe(key);
  });

  it('knows SoundCloud share links as links', () => {
    expect(parseSiteVideo('https://on.soundcloud.com/AbCd1234')).toMatchObject({
      site: 'soundcloud',
      kind: 'link',
      key: 'soundcloud:link:AbCd1234',
      pageUrl: 'https://on.soundcloud.com/AbCd1234',
      lookAhead: false,
    });
  });

  it.each([
    'https://soundcloud.com/',
    'https://soundcloud.com/some-artist',
    'https://soundcloud.com/some-artist/tracks',
    'https://soundcloud.com/some-artist/sets/an-album',
    'https://soundcloud.com/some-artist/likes',
    'https://soundcloud.com/discover/sets/charts',
    'https://soundcloud.com/search/sounds',
    'https://soundcloud.com/you/library',
    'https://soundcloud.com/Some-Artist/a-track',
    'https://soundcloud.com/some-artist/a-track/not-a-secret',
    'https://soundcloud.com/some-artist/a-track/s-AbC/extra',
    'https://on.soundcloud.com/a',
    'https://some-artist.bandcamp.com/',
    'https://some-artist.bandcamp.com/album/an-album',
    'https://some-artist.bandcamp.com/track/a-song/extra',
    'https://bandcamp.com/track/a-song',
    'https://www.bandcamp.com/track/a-song',
    'https://a.b.bandcamp.com/track/a-song',
    'https://www.mixcloud.com/',
    'https://www.mixcloud.com/SomeHost/',
    'https://www.mixcloud.com/SomeHost/uploads/',
    'https://www.mixcloud.com/SomeHost/favorites/',
    'https://www.mixcloud.com/genres/deep-house/',
    'https://www.mixcloud.com/discover/trending/',
    'https://www.mixcloud.com/SomeHost/a-mix/extra/',
    'https://user:pw@soundcloud.com/some-artist/a-track',
    'ftp://soundcloud.com/some-artist/a-track',
  ])('%s is no track', (url) => {
    expect(parseSiteVideo(url)).toBeUndefined();
  });

  it('names the sites and owns their hosts; their other pages keep the generic target', () => {
    expect([SITE_NAMES.soundcloud, SITE_NAMES.bandcamp, SITE_NAMES.mixcloud]).toEqual(['SoundCloud', 'Bandcamp', 'Mixcloud']);
    expect(siteOfHost('soundcloud.com')).toBe('soundcloud');
    expect(siteOfHost('on.soundcloud.com')).toBe('soundcloud');
    expect(siteOfHost('some-artist.bandcamp.com')).toBe('bandcamp');
    expect(siteOfHost('bandcamp.com')).toBeUndefined();
    expect(siteOfHost('www.mixcloud.com')).toBe('mixcloud');
    expect(curatedPage('https://soundcloud.com/some-artist')).toBeUndefined();
    expect(curatedPage('https://www.mixcloud.com/SomeHost/uploads/')).toBeUndefined();
  });
});

function audio(t: Partial<VideoTrack> & Pick<VideoTrack, 'id'>): VideoTrack {
  return {
    kind: 'audio',
    ext: 'm4a',
    codecs: 'mp4a.40.2',
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

/** SoundCloud-like answer: two AAC tracks, saved as .m4a. */
function audioPage(): VideoInfoResult {
  return {
    type: 'videoInfo',
    site: 'SoundCloud',
    title: 'A track',
    durationMs: 143_000,
    format: 'http',
    container: 'm4a',
    selectedVideo: '',
    selectedAudio: 'aac160',
    tracks: [
      audio({ id: 'aac160', bandwidth: 160_000, size: 2_900_000, sizeApprox: true }),
      audio({ id: 'aac96', bandwidth: 96_000, name: 'medium' }),
    ],
  };
}

describe('the rows of an audio page (SITE-MEDIA-SPEC §3.13)', () => {
  it('lists its audio tracks as rows, picked as audio without video', () => {
    const info = audioPage();
    expect(audioOnlyList(info)).toBe(true);
    expect(fileTypeLabel(info)).toBe('M4A');
    expect(defaultPick(info)).toEqual({ video: '', audio: 'aac160' });
    expect(qualityRows(info, '')).toEqual([
      { id: 'aac160', label: 'Audio · AAC · 160 kbit/s · ~2.9 MB', quality: AUDIO_QUALITY, codecs: 'AAC · 160 kbit/s', size: '~2.9 MB', picture: '', audio: true },
      { id: 'aac96', label: 'medium · AAC · 96 kbit/s · ~1.7 MB', quality: 'medium', codecs: 'AAC · 96 kbit/s', size: '~1.7 MB', picture: '', audio: true },
    ]);
    const pick = withVideo(info, defaultPick(info), 'aac96');
    expect(pick).toEqual({ video: '', audio: 'aac96' });
    expect(pickedAudio(info, pick)).toBe('aac96');
    expect(pickSize(info, pick)).toEqual({ bytes: 1_716_000, approx: true });
    // The rows are the audio: no audio select besides them.
    expect(showsAudioChoice(info, pick)).toBe(false);
  });

  it('names an MP3 page and its codec', () => {
    const info: VideoInfoResult = {
      ...audioPage(),
      site: 'Bandcamp',
      container: 'mp3',
      selectedAudio: 'mp3-128',
      tracks: [audio({ id: 'mp3-128', ext: 'mp3', codecs: 'mp3', bandwidth: 128_000, size: 5_573_712, sizeApprox: true })],
    };
    expect(fileTypeLabel(info)).toBe('MP3');
    expect(codecName('mp3')).toBe('MP3');
    expect(qualityRows(info, '')[0]?.label).toBe('Audio · MP3 · 128 kbit/s · ~5.6 MB');
    expect(videoSubline('Bandcamp', false, '', fileTypeLabel(info))).toBe('Bandcamp · MP3');
  });

  it('keeps a video page as it was', () => {
    expect(fileTypeLabel({ container: 'mp4' })).toBe('MP4');
    expect(fileTypeLabel(undefined)).toBe('MP4');
    expect(videoSubline('YouTube', false, '4K (2160p)')).toBe('YouTube · up to 4K · MP4');
    expect(audioOnlyList({ tracks: [] })).toBe(false);
  });
});

describe('refusals of an audio page', () => {
  const refused = (message: string): ProtocolError => ({ code: 'rejected', message });
  it.each([
    ['media_unsupported: media: unsupported operation: the audio is Opus or Vorbis, which Downex cannot save without converting it', VIDEO_TEXTS.audio_opus],
    ['media_unsupported: media: unsupported operation: no AAC or MP3 audio on this page', VIDEO_TEXTS.audio_none],
    ['media_unsupported: media: unsupported operation: no MP4 video on this page', VIDEO_TEXTS.media_unsupported],
  ])('%s', (message, text) => {
    expect(videoFailureText(refused(message))).toBe(text);
  });
});

describe('more of audio pages (SITE-MEDIA-SPEC §3.12, §3.13)', () => {
  it('knows a Mixcloud mix whose title is in another script (percent-encoded in the tab URL)', () => {
    const url = 'https://www.mixcloud.com/SomeHost/%D1%82%D0%B5%D1%81%D1%82-mix-17-11-26/';
    const v = parseSiteVideo(url);
    expect(v).toMatchObject({ key: 'mixcloud:SomeHost/%D1%82%D0%B5%D1%81%D1%82-mix-17-11-26', kind: 'audio', pageUrl: url });
    expect(parseSiteVideo(v?.pageUrl)?.key).toBe(v?.key);
    // Octets that are not a title's letters: a separator, a control character, broken UTF-8.
    for (const bad of ['a%2Fb', 'a%3Fb', 'a%00b', 'a%C3', 'a%25b']) {
      expect(parseSiteVideo(`https://www.mixcloud.com/SomeHost/${bad}/`)).toBeUndefined();
    }
  });

  const refused = (message: string): ProtocolError => ({ code: 'rejected', message });
  it.each([
    ['media_unsupported: media: unsupported operation: the audio is a raw AAC stream, which Downex cannot save without converting it', VIDEO_TEXTS.audio_raw],
    ['media_unsupported: media: unsupported operation: only a preview of this track is public, not the whole track', VIDEO_TEXTS.audio_preview],
    ['media_unsupported: media: unsupported operation: the page is a playlist; open a single track of it', VIDEO_TEXTS.playlist_tracks],
    ['media_unsupported: media: unsupported operation: the page is a playlist; open a single video of it', VIDEO_TEXTS.playlist],
  ])('%s', (message, text) => {
    expect(videoFailureText(refused(message))).toBe(text);
  });

  it('names the items of an audio page tracks', () => {
    const options = [
      { id: 'a', title: 'Turning', durationMs: 0, thumbnail: '', selected: true },
      { id: 'b', title: 'Other', durationMs: 61_000, thumbnail: '', selected: false },
    ];
    expect(itemsCountText({ options, index: 0, leftOut: 3, audio: true })).toBe('Track 1 of 2');
    expect(itemsCountText({ options, index: -1, leftOut: 0, audio: true })).toBe('2 tracks');
    expect(itemsCountText({ options, index: 0, leftOut: 0 })).toBe('Video 1 of 2');
    expect(itemsLeftOutText(3, true)).toBe('3 other items not listed: links or audio Downex cannot save.');
    expect(itemsLeftOutText(1, true)).toBe('1 other item not listed: a link or audio Downex cannot save.');
    expect(itemsLeftOutText(3)).toBe('3 other items not listed: photos, links or videos Downex cannot save.');
    expect(itemLabel(2, 'Other', 61_000, true)).toBe('Track 2: Other, 1:01');
  });
});
