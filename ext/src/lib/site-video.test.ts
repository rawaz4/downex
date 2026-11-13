import { describe, expect, it } from 'vitest';
import { genericPageUrl, genericVideoKey, httpAddress, parseSiteVideo, sameOrigin, siteOfKey, SITE_NAMES } from './site-video';

const ID = 'aqz-KE-bpKQ';
const WATCH = `https://www.youtube.com/watch?v=${ID}`;

describe('parseSiteVideo', () => {
  it.each([
    [`https://www.youtube.com/watch?v=${ID}`, 'watch'],
    [`https://www.youtube.com/watch?v=${ID}&list=PL123&index=4&t=42s&si=abc&pp=xyz`, 'watch'],
    [`https://m.youtube.com/watch?v=${ID}&feature=share`, 'watch'],
    [`https://music.youtube.com/watch?v=${ID}&list=RDAMVM`, 'watch'],
    [`https://youtube.com/watch?v=${ID}`, 'watch'],
    [`http://www.youtube.com/watch?v=${ID}`, 'watch'],
    [`https://www.youtube.com/watch?feature=share&v=${ID}#t=10`, 'watch'],
    [`https://www.youtube.com/shorts/${ID}`, 'shorts'],
    [`https://m.youtube.com/shorts/${ID}?si=x`, 'shorts'],
    [`https://www.youtube.com/embed/${ID}?autoplay=1`, 'embed'],
    [`https://www.youtube.com/live/${ID}?si=x`, 'live'],
    [`https://www.youtube.com/v/${ID}`, 'embed'],
    [`https://www.youtube-nocookie.com/embed/${ID}?rel=0`, 'embed'],
    [`https://youtube-nocookie.com/embed/${ID}`, 'embed'],
    [`https://youtu.be/${ID}?t=5&si=share`, 'watch'],
    [`https://WWW.YouTube.com/watch?v=${ID}`, 'watch'],
  ])('%s is a %s video with the canonical address', (url, kind) => {
    const lookAhead = kind === 'watch' || kind === 'shorts';
    expect(parseSiteVideo(url)).toEqual({ site: 'youtube', id: ID, kind, key: `youtube:${ID}`, pageUrl: WATCH, lookAhead });
  });

  it('keeps a clip at its own address, without the fragment', () => {
    const clip = 'UgkxW7Hy2v8bKXl5pz7bK3s5mE0aT1T9hF2d';
    expect(parseSiteVideo(`https://www.youtube.com/clip/${clip}?si=x#frag`)).toEqual({
      site: 'youtube',
      id: clip,
      kind: 'clip',
      key: `youtube:${clip}`,
      pageUrl: `https://www.youtube.com/clip/${clip}`,
      lookAhead: false,
    });
  });

  it.each([
    undefined,
    '',
    'not a url',
    'https://www.youtube.com/',
    'https://www.youtube.com/watch',
    `https://www.youtube.com/watch?v=${ID}x`,
    'https://www.youtube.com/watch?v=short',
    `https://www.youtube.com/watch?v=${ID}&v=${ID}`,
    'https://www.youtube.com/watch?v=dQw4w9WgXc!',
    `https://www.youtube.com/channel/UC${ID}`,
    'https://www.youtube.com/playlist?list=PL0123456789',
    `https://www.youtube.com/live_chat?v=${ID}`,
    `https://www.youtube.com/embed/${ID}/extra`,
    `https://www.youtube.com/shorts/`,
    `https://www.youtube.com/clip/short`,
    `https://youtu.be/${ID}/x`,
    `https://youtu.be/`,
    `https://www.youtube-nocookie.com/watch?v=${ID}`,
    `https://evil.example/watch?v=${ID}`,
    `https://www.youtube.com.evil.example/watch?v=${ID}`,
    `https://user:pw@www.youtube.com/watch?v=${ID}`,
    `ftp://www.youtube.com/watch?v=${ID}`,
    `javascript:alert('https://www.youtube.com/watch?v=${ID}')`,
  ])('%s is not a video page', (url) => {
    expect(parseSiteVideo(url)).toBeUndefined();
  });
});

describe('curated video sites (VIDEO-DETECT-SPEC §3.8)', () => {
  type Row = [url: string, key: string, kind: string, pageUrl: string, lookAhead: boolean];
  const rows: Row[] = [
    // Vimeo
    ['https://vimeo.com/76979871', 'vimeo:76979871', 'watch', 'https://vimeo.com/76979871', true],
    ['https://www.vimeo.com/76979871/', 'vimeo:76979871', 'watch', 'https://vimeo.com/76979871', true],
    ['https://vimeo.com/76979871/8272103f6e?share=copy', 'vimeo:76979871', 'watch', 'https://vimeo.com/76979871/8272103f6e', true],
    ['https://vimeo.com/channels/staffpicks/76979871', 'vimeo:76979871', 'watch', 'https://vimeo.com/76979871', true],
    ['https://vimeo.com/groups/shortfilms/videos/76979871', 'vimeo:76979871', 'watch', 'https://vimeo.com/76979871', true],
    ['https://vimeo.com/showcase/9876543/video/76979871', 'vimeo:76979871', 'watch', 'https://vimeo.com/76979871', true],
    ['https://player.vimeo.com/video/76979871', 'vimeo:76979871', 'embed', 'https://player.vimeo.com/video/76979871', false],
    [
      'https://player.vimeo.com/video/76979871?h=8272103f6e&badge=0&autopause=0',
      'vimeo:76979871',
      'embed',
      'https://player.vimeo.com/video/76979871?h=8272103f6e',
      false,
    ],
    ['https://player.vimeo.com/video/76979871?h=NOT-A-HASH', 'vimeo:76979871', 'embed', 'https://player.vimeo.com/video/76979871', false],
    // X
    ['https://x.com/someone/status/1790000000000000001', 'x:1790000000000000001', 'watch', 'https://x.com/i/status/1790000000000000001', true],
    ['https://twitter.com/someone/status/1790000000000000001?s=20', 'x:1790000000000000001', 'watch', 'https://x.com/i/status/1790000000000000001', true],
    ['https://mobile.twitter.com/someone/status/17/video/1', 'x:17', 'watch', 'https://x.com/i/status/17', true],
    ['https://www.x.com/i/status/42', 'x:42', 'watch', 'https://x.com/i/status/42', true],
    ['https://mobile.x.com/i/web/status/42', 'x:42', 'watch', 'https://x.com/i/status/42', true],
    ['https://www.twitter.com/i/status/42/', 'x:42', 'watch', 'https://x.com/i/status/42', true],
    // Facebook
    ['https://www.facebook.com/watch?v=1234567890', 'facebook:1234567890', 'watch', 'https://www.facebook.com/watch/?v=1234567890', false],
    ['https://facebook.com/watch/?v=1234567890&ref=sharing', 'facebook:1234567890', 'watch', 'https://www.facebook.com/watch/?v=1234567890', false],
    ['https://m.facebook.com/videos/1234567890/', 'facebook:1234567890', 'watch', 'https://www.facebook.com/watch/?v=1234567890', false],
    ['https://web.facebook.com/SomePage/videos/1234567890', 'facebook:1234567890', 'watch', 'https://www.facebook.com/watch/?v=1234567890', false],
    ['https://www.facebook.com/Some.Page/videos/a-title-slug/1234567890/', 'facebook:1234567890', 'watch', 'https://www.facebook.com/watch/?v=1234567890', false],
    ['https://www.facebook.com/reel/987654321', 'facebook:987654321', 'watch', 'https://www.facebook.com/reel/987654321', false],
    ['https://fb.watch/aBc_-12/', 'facebook:aBc_-12', 'watch', 'https://fb.watch/aBc_-12/', false],
    // Instagram
    ['https://www.instagram.com/p/C1a2B3c4D5e/', 'instagram:C1a2B3c4D5e', 'watch', 'https://www.instagram.com/p/C1a2B3c4D5e/', false],
    ['https://instagram.com/some.user/p/C1a2B3c4D5e?igsh=x', 'instagram:C1a2B3c4D5e', 'watch', 'https://www.instagram.com/p/C1a2B3c4D5e/', false],
    ['https://www.instagram.com/reel/C1a2B3c4D5e/', 'instagram:C1a2B3c4D5e', 'watch', 'https://www.instagram.com/reel/C1a2B3c4D5e/', false],
    ['https://www.instagram.com/reels/C1a2B3c4D5e/', 'instagram:C1a2B3c4D5e', 'watch', 'https://www.instagram.com/reel/C1a2B3c4D5e/', false],
    ['https://www.instagram.com/tv/C1a2B3c4D5e', 'instagram:C1a2B3c4D5e', 'watch', 'https://www.instagram.com/tv/C1a2B3c4D5e/', false],
    // TikTok
    ['https://www.tiktok.com/@some.user_1/video/7300000000000000001?lang=en', 'tiktok:7300000000000000001', 'watch', 'https://www.tiktok.com/@some.user_1/video/7300000000000000001', true],
    ['https://m.tiktok.com/@u/video/7300000000000000001/', 'tiktok:7300000000000000001', 'watch', 'https://www.tiktok.com/@u/video/7300000000000000001', true],
    ['https://www.tiktok.com/embed/v2/7300000000000000001', 'tiktok:7300000000000000001', 'embed', 'https://www.tiktok.com/embed/v2/7300000000000000001', false],
    ['https://tiktok.com/embed/7300000000000000001', 'tiktok:7300000000000000001', 'embed', 'https://www.tiktok.com/embed/v2/7300000000000000001', false],
    // Dailymotion
    ['https://www.dailymotion.com/video/x8abc12', 'dailymotion:x8abc12', 'watch', 'https://www.dailymotion.com/video/x8abc12', true],
    ['https://dailymotion.com/video/x8abc12?playlist=x1', 'dailymotion:x8abc12', 'watch', 'https://www.dailymotion.com/video/x8abc12', true],
    ['https://www.dailymotion.com/embed/video/x8abc12?autoplay=1', 'dailymotion:x8abc12', 'embed', 'https://www.dailymotion.com/video/x8abc12', false],
    ['https://dai.ly/x8abc12', 'dailymotion:x8abc12', 'watch', 'https://www.dailymotion.com/video/x8abc12', true],
    // Reddit
    ['https://www.reddit.com/r/videos/comments/1abc2de/some_title/', 'reddit:1abc2de', 'watch', 'https://www.reddit.com/r/videos/comments/1abc2de/', true],
    ['https://old.reddit.com/r/videos/comments/1abc2de', 'reddit:1abc2de', 'watch', 'https://www.reddit.com/r/videos/comments/1abc2de/', true],
    ['https://new.reddit.com/r/Some_Sub/comments/1abc2de/title/comment9/?context=3', 'reddit:1abc2de', 'watch', 'https://www.reddit.com/r/Some_Sub/comments/1abc2de/', true],
    ['https://reddit.com/r/videos/comments/1abc2de/', 'reddit:1abc2de', 'watch', 'https://www.reddit.com/r/videos/comments/1abc2de/', true],
    // Twitch
    ['https://www.twitch.tv/videos/2000000001', 'twitch:2000000001', 'watch', 'https://www.twitch.tv/videos/2000000001', true],
    ['https://m.twitch.tv/videos/2000000001?t=1h2m', 'twitch:2000000001', 'watch', 'https://www.twitch.tv/videos/2000000001', true],
    ['https://www.twitch.tv/some_channel/clip/FunnyClipSlug-AbC_12', 'twitch:clip:FunnyClipSlug-AbC_12', 'clip', 'https://clips.twitch.tv/FunnyClipSlug-AbC_12', true],
    ['https://clips.twitch.tv/FunnyClipSlug-AbC_12', 'twitch:clip:FunnyClipSlug-AbC_12', 'clip', 'https://clips.twitch.tv/FunnyClipSlug-AbC_12', true],
    ['https://player.twitch.tv/?video=v2000000001&parent=blog.example', 'twitch:2000000001', 'embed', 'https://www.twitch.tv/videos/2000000001', false],
    // Streamable
    ['https://streamable.com/abc123', 'streamable:abc123', 'watch', 'https://streamable.com/abc123', true],
    ['https://www.streamable.com/abc123/', 'streamable:abc123', 'watch', 'https://streamable.com/abc123', true],
    ['https://streamable.com/e/abc123?autoplay=1', 'streamable:abc123', 'embed', 'https://streamable.com/abc123', false],
  ];

  it.each(rows)('%s is %s', (url, key, kind, pageUrl, lookAhead) => {
    const v = parseSiteVideo(url);
    expect(v).toMatchObject({ key, kind, pageUrl, lookAhead });
    expect(v?.key.startsWith(`${v.site}:`)).toBe(true);
    // The canonical address names the same video.
    expect(parseSiteVideo(v?.pageUrl)?.key).toBe(key);
  });

  it('never looks ahead on embeds, Facebook or Instagram', () => {
    for (const [url] of rows) {
      const v = parseSiteVideo(url);
      if (v?.kind === 'embed' || v?.site === 'facebook' || v?.site === 'instagram') expect(v.lookAhead).toBe(false);
    }
  });

  it.each([
    // Wrong hosts.
    'https://vimeo.co/76979871',
    'https://vimeo.com.evil.example/76979871',
    'https://notx.com/someone/status/42',
    'https://x.co/someone/status/42',
    'https://facebook.example/watch?v=1',
    'https://l.facebook.com/videos/1',
    'https://instagr.am/p/C1a2B3c4D5e/',
    'https://vm.tiktok.com/ZMabc123/',
    'https://dailymotion.example/video/x8abc12',
    'https://reddit.example/r/videos/comments/1abc2de/',
    'https://v.redd.it/abc123xyz',
    'https://twitch.example/videos/2000000001',
    'https://streamable.example/abc123',
    // Bad ids.
    'https://vimeo.com/abc',
    'https://vimeo.com/1234567890123',
    'https://vimeo.com/76979871/NOTHEX',
    'https://player.vimeo.com/video/abc',
    'https://x.com/someone/status/abc',
    'https://x.com/someone/status/123456789012345678901',
    'https://www.facebook.com/watch?v=abc',
    'https://www.facebook.com/watch?v=1&v=2',
    'https://www.facebook.com/reel/abc',
    'https://fb.watch/ab/',
    'https://www.instagram.com/p/abc/',
    'https://www.tiktok.com/@user/video/abc',
    'https://www.tiktok.com/user/video/7300000000000000001',
    'https://www.dailymotion.com/video/x8',
    'https://www.dailymotion.com/video/x8abc12_some-title',
    'https://www.reddit.com/r/v/comments/1abc2de/',
    'https://www.reddit.com/r/videos/comments/ABC/',
    'https://www.twitch.tv/videos/abc',
    'https://clips.twitch.tv/abc',
    'https://player.twitch.tv/?video=2000000001',
    'https://player.twitch.tv/?video=v1&video=v2',
    'https://streamable.com/ab',
    'https://streamable.com/ABC123',
    // Extra segments where none are allowed.
    'https://vimeo.com/76979871/8272103f6e/extra',
    'https://player.vimeo.com/video/76979871/extra',
    'https://x.com/someone/status/42/likes',
    'https://x.com/someone/status/42/photo/1',
    'https://www.facebook.com/videos/1234567890/extra/more',
    'https://www.instagram.com/p/C1a2B3c4D5e/extra',
    'https://www.tiktok.com/@u/video/7300000000000000001/extra',
    'https://www.dailymotion.com/video/x8abc12/extra',
    'https://www.twitch.tv/videos/2000000001/extra',
    'https://streamable.com/e/abc123/extra',
    // Channel, feed, timeline, profile and search pages.
    'https://vimeo.com/',
    'https://vimeo.com/channels/staffpicks',
    'https://x.com/',
    'https://x.com/home',
    'https://x.com/someone',
    'https://x.com/search?q=video',
    'https://twitter.com/i/status',
    'https://www.facebook.com/',
    'https://www.facebook.com/watch/',
    'https://www.facebook.com/SomePage',
    'https://www.facebook.com/SomePage/videos',
    'https://www.instagram.com/',
    'https://www.instagram.com/some.user/',
    'https://www.instagram.com/reels/',
    'https://www.instagram.com/explore/',
    'https://www.tiktok.com/',
    'https://www.tiktok.com/foryou',
    'https://www.tiktok.com/@someone',
    'https://www.dailymotion.com/',
    'https://www.reddit.com/',
    'https://www.reddit.com/r/videos/',
    'https://www.reddit.com/user/someone/',
    'https://www.twitch.tv/',
    'https://www.twitch.tv/some_channel',
    'https://www.twitch.tv/some_channel/videos',
    'https://player.twitch.tv/?channel=some_channel',
    'https://streamable.com/',
    'https://streamable.com/login',
    'https://streamable.com/upload',
    // User information and other schemes.
    'https://user:pw@vimeo.com/76979871',
    'https://user@www.reddit.com/r/videos/comments/1abc2de/',
    'ftp://vimeo.com/76979871',
    'javascript:alert("https://vimeo.com/76979871")',
    'data:text/html,https://streamable.com/abc123',
    'blob:https://vimeo.com/76979871',
  ])('%s is not a video page', (url) => {
    expect(parseSiteVideo(url)).toBeUndefined();
  });

  it('names every site and reads the site of a key', () => {
    expect(Object.values(SITE_NAMES)).toEqual([
      'YouTube',
      'Vimeo',
      'X',
      'Facebook',
      'Instagram',
      'TikTok',
      'Dailymotion',
      'Reddit',
      'Twitch',
      'Streamable',
    ]);
    expect(siteOfKey('vimeo:1')).toBe('vimeo');
    expect(siteOfKey('twitch:clip:abcd')).toBe('twitch');
    expect(siteOfKey('page:https://a.example/')).toBeUndefined();
    expect(siteOfKey('constructor:1')).toBeUndefined();
    expect(siteOfKey('nothing')).toBeUndefined();
  });
});

/** A small deterministic generator for property tests (mulberry32). */
function random(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

describe('parseSiteVideo over random addresses', () => {
  const hosts = [
    'www.youtube.com',
    'youtu.be',
    'vimeo.com',
    'player.vimeo.com',
    'x.com',
    'twitter.com',
    'www.facebook.com',
    'fb.watch',
    'www.instagram.com',
    'www.tiktok.com',
    'www.dailymotion.com',
    'dai.ly',
    'www.reddit.com',
    'www.twitch.tv',
    'clips.twitch.tv',
    'player.twitch.tv',
    'streamable.com',
    'evil.example',
  ];
  const words = ['watch', 'status', 'i', 'web', 'video', 'videos', 'p', 'reel', 'reels', 'tv', 'embed', 'v2', 'r', 'comments', 'clip', 'e', 'shorts', 'live', '@u', '..', '%2F', ''];
  const chars = 'abcdefABCDEF0123456789_-.@%~:/?#&=';

  it('never throws, and every answer is canonical and keyed by its site', () => {
    const next = random(0x5eed);
    const pick = <T>(list: readonly T[]): T => list[Math.floor(next() * list.length)] as T;
    const token = () => {
      if (next() < 0.5) return pick(words);
      let s = '';
      const n = Math.floor(next() * 24);
      for (let i = 0; i < n; i++) s += pick(chars.split(''));
      return s;
    };
    let found = 0;
    for (let i = 0; i < 5000; i++) {
      const segs = Array.from({ length: Math.floor(next() * 6) }, token).join('/');
      const query = next() < 0.4 ? `?${pick(['v', 'h', 'video', 'x'])}=${token()}` : '';
      const url = `${pick(['https', 'http', 'ftp'])}://${pick(hosts)}/${segs}${query}`;
      const v = parseSiteVideo(url);
      if (!v) continue;
      found++;
      expect(v.key.startsWith(`${v.site}:`)).toBe(true);
      expect(v.pageUrl.startsWith('https://')).toBe(true);
      expect(parseSiteVideo(v.pageUrl)?.key).toBe(v.key);
      if (v.kind === 'embed') expect(v.lookAhead).toBe(false);
    }
    expect(found).toBeGreaterThan(0);
  });
});

describe('generic keys and addresses', () => {
  it('keys any http(s) page by its address without the fragment', () => {
    expect(genericVideoKey('https://video.example/watch/12?x=1#comments')).toBe('page:https://video.example/watch/12?x=1');
    expect(genericPageUrl('page:https://video.example/watch/12?x=1')).toBe('https://video.example/watch/12?x=1');
    expect(genericPageUrl(`youtube:${ID}`)).toBeUndefined();
    expect(genericVideoKey('chrome://settings')).toBeUndefined();
    expect(genericVideoKey('nope')).toBeUndefined();
  });

  it('compares origins and keeps http(s) Referers only', () => {
    expect(sameOrigin(WATCH, 'https://www.youtube.com/shorts/x')).toBe(true);
    expect(sameOrigin(WATCH, 'https://m.youtube.com/')).toBe(false);
    expect(sameOrigin(WATCH, undefined)).toBe(false);
    expect(httpAddress('https://blog.example/post')).toBe('https://blog.example/post');
    expect(httpAddress('file:///x')).toBeUndefined();
    expect(httpAddress('about:blank')).toBeUndefined();
  });
});
