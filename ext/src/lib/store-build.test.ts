import { readFileSync } from 'node:fs';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { genericVideoKey, parseSiteVideo } from './site-video';
import { STORE_OPTION_HINTS, storeBuild, storeExcludes, storeExcludesRequest, youtubeAddress } from './store-build';
import { findEntry, frameVideo, genericTarget, senderTarget, tabTarget, tabVideoEntries } from './video-handoff';

const WATCH = 'https://www.youtube.com/watch?v=aqz-KE-bpKQ';
const YOUTUBE_PAGES = [
  WATCH,
  'https://m.youtube.com/shorts/aqz-KE-bpKQ',
  'https://music.youtube.com/watch?v=aqz-KE-bpKQ',
  'https://www.youtube.com/embed/aqz-KE-bpKQ',
  'https://www.youtube-nocookie.com/embed/aqz-KE-bpKQ',
  'https://youtu.be/aqz-KE-bpKQ',
  'https://www.youtube.com/clip/UgkxAbCdEfGhIjKlMnOpQrStUvWxYz012345',
];

const store = () => vi.stubEnv('MODE', 'store');

afterEach(() => {
  vi.unstubAllEnvs();
});

describe('store build', () => {
  it('is the "store" mode only', () => {
    expect(storeBuild()).toBe(false);
    store();
    expect(storeBuild()).toBe(true);
  });

  it('knows YouTube’s pages, embeds, short links and video host, and nothing that only looks like them', () => {
    for (const a of [...YOUTUBE_PAGES, 'https://rr3---sn-abc.googlevideo.com/videoplayback?id=1', 'https://www.youtube.com', 'https://YouTube.com./']) {
      expect(youtubeAddress(a), a).toBe(true);
    }
    for (const a of ['https://notyoutube.com/watch?v=aqz-KE-bpKQ', 'https://youtube.com.example/x', 'https://vimeo.com/1', 'not a url', '', undefined]) {
      expect(youtubeAddress(a), String(a)).toBe(false);
    }
    // Only the store build leaves them out.
    expect(storeExcludes(WATCH)).toBe(false);
    store();
    expect(storeExcludes(WATCH)).toBe(true);
    expect(storeExcludes('https://vimeo.com/1')).toBe(false);
  });

  it('recognises no YouTube video, and no generic page video on YouTube, while other sites stay', () => {
    for (const page of YOUTUBE_PAGES) expect(parseSiteVideo(page), page).toBeDefined();
    store();
    for (const page of YOUTUBE_PAGES) {
      expect(parseSiteVideo(page), page).toBeUndefined();
      expect(genericVideoKey(page), page).toBeUndefined();
    }
    expect(parseSiteVideo('https://vimeo.com/76979871')).toMatchObject({ site: 'vimeo', lookAhead: true });
    expect(genericVideoKey('https://www.example.com/clip#t=1')).toBe('page:https://www.example.com/clip');
  });

  it('gives no frame or content script a YouTube target: no row, look-ahead or hand-off', () => {
    const tab = { id: 1, url: 'https://www.example.com/post', title: 'Post' };
    const embed = 'https://www.youtube-nocookie.com/embed/aqz-KE-bpKQ';
    expect(frameVideo(tab, embed, 3, [])).toMatchObject({ curated: true, site: 'YouTube' });
    store();
    expect(frameVideo(tab, embed, 3, [])).toBeUndefined();
    expect(frameVideo({ ...tab, url: WATCH }, WATCH, 0, [])).toBeUndefined();
    expect(frameVideo(tab, tab.url, 0, [])).toMatchObject({ curated: false, kind: 'page' });
    const sender = { url: WATCH, origin: 'https://www.youtube.com', frameId: 0, tab: { id: 1, url: WATCH } };
    expect(senderTarget(sender, WATCH)).toMatchObject({ ok: false, error: { code: 'unsupported_page' } });
  });

  it('finds no video on a page that embeds a YouTube player: yt-dlp would follow the embed', () => {
    const page = 'https://www.example.com/post';
    const tab = { id: 1, url: `${page}#c`, title: 'Post' };
    const frames = [
      { frameId: 0, url: page },
      { frameId: 2, url: 'https://ads.example/slot' },
      { frameId: 3, url: 'https://www.youtube-nocookie.com/embed/aqz-KE-bpKQ' },
    ];
    const key = `page:${page}`;
    const none = () => undefined;
    const top = { url: tab.url, origin: 'https://www.example.com', frameId: 0, tab };
    const ad = { url: 'https://ads.example/slot', origin: 'https://ads.example', frameId: 2, tab };
    // The default build finds it (and lists the embed's YouTube video besides).
    expect(genericTarget(tab, frames)).toMatchObject({ key, generic: true });
    expect(findEntry(tab, frames)).toMatchObject({ ok: true, value: { key, kind: 'page' } });
    expect(tabTarget(tab, frames, key)).toMatchObject({ ok: true, value: { key } });
    expect(frameVideo(tab, tab.url, 0, frames)).toMatchObject({ curated: false, kind: 'page' });
    expect(senderTarget(top, page, frames)).toMatchObject({ ok: true, value: { key } });
    expect(senderTarget(top, page)).toMatchObject({ ok: true, value: { key } });

    store();
    // Popup: no "Find", no row looked up again, no entry from an earlier probe.
    expect(genericTarget(tab, frames)).toBeUndefined();
    expect(findEntry(tab, frames)).toMatchObject({ ok: false, error: { code: 'unsupported_page' } });
    expect(tabTarget(tab, frames, key)).toMatchObject({ ok: false, error: { code: 'expired' } });
    const probed = () => ({ result: { ok: true as const, value: { type: 'videoInfo' } as never } });
    expect(tabVideoEntries(tab, frames, probed, none)).toEqual([]);
    // Overlay panels: neither the page's frame nor another frame of the tab offers its address.
    expect(frameVideo(tab, tab.url, 0, frames)).toBeUndefined();
    expect(frameVideo(tab, ad.url, 2, frames)).toBeUndefined();
    expect(senderTarget(top, page, frames)).toMatchObject({ ok: false, error: { code: 'unsupported_page' } });
    expect(senderTarget(ad, ad.url, frames)).toMatchObject({ ok: false, error: { code: 'unsupported_page' } });
    // Frames not known count as a YouTube frame.
    expect(senderTarget(top, page)).toMatchObject({ ok: false, error: { code: 'unsupported_page' } });

    // Without the YouTube frame the page is found as before, curated players included.
    const plain = frames.filter((f) => f.frameId !== 3);
    expect(findEntry(tab, plain)).toMatchObject({ ok: true, value: { key } });
    expect(tabTarget(tab, plain, key)).toMatchObject({ ok: true, value: { key } });
    expect(frameVideo(tab, tab.url, 0, plain)).toMatchObject({ curated: false, kind: 'page' });
    expect(senderTarget(top, page, plain)).toMatchObject({ ok: true, value: { key } });
    expect(frameVideo(tab, 'https://player.vimeo.com/video/76979871', 4, frames)).toMatchObject({ curated: true, site: 'Vimeo' });
  });

  it('lists no stream of YouTube’s video host or of a YouTube frame', () => {
    const stream = { url: 'https://rr3---sn-abc.googlevideo.com/videoplayback?id=1' };
    const fromYouTube = { url: 'https://cdn.example.com/a.m3u8', initiator: 'https://www.youtube.com' };
    const fromEmbed = { url: 'https://cdn.example.com/a.m3u8', documentUrl: 'https://www.youtube-nocookie.com/embed/aqz-KE-bpKQ' };
    const other = { url: 'https://cdn.example.com/a.m3u8', initiator: 'https://www.example.com', documentUrl: 'https://www.example.com/' };
    for (const d of [stream, fromYouTube, fromEmbed, other]) expect(storeExcludesRequest(d)).toBe(false);
    store();
    for (const d of [stream, fromYouTube, fromEmbed]) expect(storeExcludesRequest(d)).toBe(true);
    expect(storeExcludesRequest(other)).toBe(false);
  });

  it('words the options page’s hints without YouTube, the rest as the default build says it', () => {
    const html = readFileSync(new URL('../entrypoints/options/index.html', import.meta.url), 'utf8');
    const hint = (id: string) => {
      const m = new RegExp(`<span id="${id}"[^>]*>([^<]*)</span>`).exec(html);
      expect(m, id).not.toBeNull();
      return (m?.[1] ?? '').replace(/\s+/g, ' ').trim();
    };
    for (const [id, text] of Object.entries(STORE_OPTION_HINTS)) {
      expect(hint(id), id).toMatch(/YouTube/);
      expect(text, id).not.toMatch(/youtube/i);
    }
    expect(STORE_OPTION_HINTS['video-prefetch-hint']).toBe(hint('video-prefetch-hint').replace('YouTube, ', ''));
  });
});
