import { describe, expect, it } from 'vitest';
import { isExtensionMessage, isFrameMessage, MAX_FRAME_VIDEOS, MAX_FRAME_VIDEOS_BYTES } from './messages';
import { OFFER_KEY_RE } from './offer-types';

describe('isExtensionMessage', () => {
  it('accepts the tab pause messages with valid tab IDs only', () => {
    expect(isExtensionMessage({ type: 'downex:tab-state', tabId: 3 })).toBe(true);
    expect(isExtensionMessage({ type: 'downex:set-tab-paused', tabId: 3, paused: true })).toBe(true);
    expect(isExtensionMessage({ type: 'downex:tab-state', tabId: -1 })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:tab-state', tabId: '3' })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:set-tab-paused', tabId: 3 })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:set-tab-paused', tabId: 1.5, paused: false })).toBe(false);
  });

  it('accepts the media messages with a valid tab and stream URL only', () => {
    expect(isExtensionMessage({ type: 'downex:tab-media', tabId: 2 })).toBe(true);
    expect(isExtensionMessage({ type: 'downex:tab-media', tabId: -2 })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:tab-media' })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:add-media', tabId: 2, url: 'https://a/b.m3u8' })).toBe(true);
    expect(isExtensionMessage({ type: 'downex:add-media', tabId: 2, url: '' })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:add-media', tabId: 2, url: 'x'.repeat(33 * 1024) })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:add-media', tabId: 2 })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:add-media', tabId: 'a', url: 'https://a/b.m3u8' })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:media-result', tabId: 2, url: 'https://a/b.m3u8' })).toBe(true);
    expect(isExtensionMessage({ type: 'downex:media-result', tabId: 2, url: '' })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:media-result', tabId: -1, url: 'https://a/b.m3u8' })).toBe(false);
  });

  it('keeps validating the other messages', () => {
    expect(isExtensionMessage({ type: 'downex:click', alt: true, ctrl: false, shift: false, meta: false, ts: 1 })).toBe(true);
    expect(isExtensionMessage({ type: 'downex:click', alt: true })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:get-state' })).toBe(true);
    expect(isExtensionMessage({ type: 'downex:links-confirm', batchId: 'b', accept: true })).toBe(true);
    expect(isExtensionMessage({ type: 'other' })).toBe(false);
    expect(isExtensionMessage(null)).toBe(false);
  });
});

describe('isExtensionMessage of page videos', () => {
  const url = 'https://www.youtube.com/watch?v=aqz-KE-bpKQ';
  it('accepts the content script requests with an address and track ids', () => {
    expect(isExtensionMessage({ type: 'downex:page-video-probe', url })).toBe(true);
    expect(isExtensionMessage({ type: 'downex:page-video-probe', url: '' })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:page-video-probe', url: 'x'.repeat(33 * 1024) })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:page-video-probe' })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:page-video-add', url, videoTrack: '299', audioTrack: '' })).toBe(true);
    expect(isExtensionMessage({ type: 'downex:page-video-add', url, videoTrack: 299, audioTrack: '' })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:page-video-add', url, videoTrack: 'v'.repeat(1025), audioTrack: '' })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:page-video-add', url, videoTrack: '299' })).toBe(false);
  });

  it('accepts the look-ahead of the content script with an address only', () => {
    expect(isExtensionMessage({ type: 'downex:page-video-prefetch', url })).toBe(true);
    expect(isExtensionMessage({ type: 'downex:page-video-prefetch', url: '' })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:page-video-prefetch', url: 5 })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:page-video-prefetch', url: 'x'.repeat(33 * 1024) })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:page-video-prefetch' })).toBe(false);
  });

  it('accepts the popup requests with a tab and a key', () => {
    expect(isExtensionMessage({ type: 'downex:tab-video-probe', tabId: 3, key: 'youtube:aqz-KE-bpKQ' })).toBe(true);
    expect(isExtensionMessage({ type: 'downex:tab-video-probe', tabId: 3, key: '' })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:tab-video-probe', tabId: -3, key: 'youtube:x' })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:tab-video-add', tabId: 3, key: 'page:' + url, videoTrack: '', audioTrack: '' })).toBe(
      true,
    );
    expect(isExtensionMessage({ type: 'downex:tab-video-add', tabId: 3, key: 'k', videoTrack: '', audioTrack: null })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:find-page-video', tabId: 3 })).toBe(true);
    expect(isExtensionMessage({ type: 'downex:find-page-video', tabId: '3' })).toBe(false);
  });
});

describe('isExtensionMessage of the video detection', () => {
  const meta = (over: Record<string, unknown> = {}) => ({
    id: 1,
    src: 'https://cdn.example/v/clip.mp4?sig=1',
    mse: false,
    width: 640,
    height: 360,
    videoWidth: 1280,
    videoHeight: 720,
    durationMs: 61_000,
    drm: false,
    visible: true,
    decorative: false,
    meaningful: true,
    ...over,
  });
  const videos = (...v: unknown[]) => ({ type: 'downex:frame-videos', videos: v });

  it('accepts element reports of well-formed videos only', () => {
    expect(isExtensionMessage(videos())).toBe(true);
    expect(isExtensionMessage(videos(meta()))).toBe(true);
    expect(isExtensionMessage(videos(meta({ src: '', mse: true, durationMs: -2, videoWidth: 0, videoHeight: 0 })))).toBe(true);
    expect(isExtensionMessage(videos(meta({ src: 'http://a.example/v.mp4', durationMs: -1, drm: true })))).toBe(true);
    expect(isExtensionMessage(videos(meta({ id: 2 ** 31 - 1 })))).toBe(true);
    expect(isExtensionMessage({ type: 'downex:frame-videos' })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:frame-videos', videos: meta() })).toBe(false);
    expect(isExtensionMessage(videos(null))).toBe(false);
  });

  it.each([
    ['id 0', { id: 0 }],
    ['id 2^31', { id: 2 ** 31 }],
    ['a fractional id', { id: 1.5 }],
    ['a blob: src', { src: 'blob:https://a.example/1' }],
    ['a data: src', { src: 'data:video/mp4;base64,AAAA' }],
    ['a src with a fragment', { src: 'https://a.example/v.mp4#t=5' }],
    ['a relative src', { src: '/v.mp4' }],
    ['a src over 32 KiB', { src: `https://a.example/${'x'.repeat(32 * 1024)}` }],
    ['a negative width', { width: -1 }],
    ['an infinite height', { height: Infinity }],
    ['a fractional videoWidth', { videoWidth: 1.5 }],
    ['a videoHeight above 65535', { videoHeight: 65_536 }],
    ['a duration below -2', { durationMs: -3 }],
    ['a NaN duration', { durationMs: NaN }],
    ['a string flag', { drm: 'no' }],
    ['a missing flag', { meaningful: undefined }],
  ])('refuses an element report with %s', (_, over) => {
    expect(isExtensionMessage(videos(meta(over)))).toBe(false);
  });

  it('bounds element reports to 16 videos, unique ids and 64 KiB', () => {
    const many = Array.from({ length: 16 }, (_, i) => meta({ id: i + 1 }));
    expect(isExtensionMessage(videos(...many))).toBe(true);
    expect(isExtensionMessage(videos(...many, meta({ id: 17 })))).toBe(false);
    expect(isExtensionMessage(videos(meta(), meta()))).toBe(false);
    const big = (id: number) => meta({ id, src: `https://a.example/${'x'.repeat(30 * 1024)}` });
    expect(isExtensionMessage(videos(big(1), big(2)))).toBe(true);
    expect(isExtensionMessage(videos(big(1), big(2), big(3)))).toBe(false);
    expect(MAX_FRAME_VIDEOS).toBe(16);
    expect(MAX_FRAME_VIDEOS_BYTES).toBe(64 * 1024);
  });

  it('accepts the requests of the tracker and the overlay UI', () => {
    expect(isExtensionMessage({ type: 'downex:video-ui' })).toBe(true);
    expect(isExtensionMessage({ type: 'downex:overlay-state' })).toBe(true);
    expect(isExtensionMessage({ type: 'downex:frame-offers', src: '', mse: true })).toBe(true);
    expect(isExtensionMessage({ type: 'downex:frame-offers', src: 'https://a.example/v.mp4', mse: false })).toBe(true);
    expect(isExtensionMessage({ type: 'downex:frame-offers', src: 'blob:https://a.example/1', mse: true })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:frame-offers', src: 'https://a.example/v.mp4#x', mse: false })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:frame-offers', src: 'https://a.example/v.mp4' })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:frame-offers', mse: false })).toBe(false);
  });

  it('accepts stream probes of stream keys and adds of any offer key', () => {
    expect(isExtensionMessage({ type: 'downex:stream-probe', key: 's1' })).toBe(true);
    expect(isExtensionMessage({ type: 'downex:stream-probe', key: 's123456789012' })).toBe(true);
    expect(isExtensionMessage({ type: 'downex:stream-probe', key: 's1234567890123' })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:stream-probe', key: 'f1' })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:stream-probe', key: 's' })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:stream-probe', key: 's-1' })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:stream-probe' })).toBe(false);
    for (const key of ['f1', 's22', 'e333']) {
      expect(isExtensionMessage({ type: 'downex:offer-add', key })).toBe(true);
    }
    expect(isExtensionMessage({ type: 'downex:offer-add', key: 's2', videoTrack: '1080p', audioTrack: '' })).toBe(true);
    expect(isExtensionMessage({ type: 'downex:offer-add', key: 's2', videoTrack: 'v'.repeat(1025) })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:offer-add', key: 's2', audioTrack: 5 })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:offer-add', key: 'x2' })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:offer-add', key: 'youtube:abc' })).toBe(false);
    expect(OFFER_KEY_RE.test('e1')).toBe(true);
  });

  it('accepts the site toggle from a content script and from the popup', () => {
    expect(isExtensionMessage({ type: 'downex:set-site-hidden', hidden: true })).toBe(true);
    expect(isExtensionMessage({ type: 'downex:set-site-hidden', tabId: 4, hidden: false })).toBe(true);
    expect(isExtensionMessage({ type: 'downex:set-site-hidden', tabId: -4, hidden: false })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:set-site-hidden', tabId: '4', hidden: false })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:set-site-hidden' })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:set-site-hidden', hidden: 1 })).toBe(false);
  });

  it('accepts the popup requests for streams and files of a tab', () => {
    const url = 'https://cdn.example/master.m3u8';
    expect(isExtensionMessage({ type: 'downex:tab-stream-probe', tabId: 2, url })).toBe(true);
    expect(isExtensionMessage({ type: 'downex:tab-stream-probe', tabId: 2, url: '' })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:tab-stream-probe', tabId: -2, url })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:add-file', tabId: 2, url: 'https://cdn.example/v.mp4' })).toBe(true);
    expect(isExtensionMessage({ type: 'downex:add-file', tabId: 2, url: 'x'.repeat(33 * 1024) })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:add-file', url: 'https://cdn.example/v.mp4' })).toBe(false);
    // add-media gains optional tracks
    expect(isExtensionMessage({ type: 'downex:add-media', tabId: 2, url, videoTrack: '3', audioTrack: 'a1' })).toBe(true);
    expect(isExtensionMessage({ type: 'downex:add-media', tabId: 2, url, videoTrack: '3' })).toBe(true);
    expect(isExtensionMessage({ type: 'downex:add-media', tabId: 2, url, videoTrack: 3 })).toBe(false);
    expect(isExtensionMessage({ type: 'downex:add-media', tabId: 2, url, audioTrack: 'a'.repeat(1025) })).toBe(false);
    // media-result takes no tracks, and ignores extra fields as before
    expect(isExtensionMessage({ type: 'downex:media-result', tabId: 2, url, videoTrack: 3 })).toBe(true);
  });

  it('never takes a message for the content scripts as one for the background', () => {
    expect(isExtensionMessage({ type: 'downex:frame-media-seen' })).toBe(false);
  });
});

describe('isFrameMessage', () => {
  it('accepts the messages the background sends to a frame only', () => {
    expect(isFrameMessage({ type: 'downex:frame-media-seen' })).toBe(true);
    expect(isFrameMessage({ type: 'downex:frame-offers', src: '', mse: false })).toBe(false);
    expect(isFrameMessage({ type: 'other' })).toBe(false);
    expect(isFrameMessage({})).toBe(false);
    expect(isFrameMessage(null)).toBe(false);
    expect(isFrameMessage('downex:frame-media-seen')).toBe(false);
  });
});
