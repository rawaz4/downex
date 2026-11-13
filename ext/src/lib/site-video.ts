// Video pages Downex recognises by their address (SITE-MEDIA-SPEC §5.1, VIDEO-DETECT-SPEC §3.8):
// the only site knowledge the extension has. It names the video, canonicalises the address the
// daemon resolves and decides where a look-ahead may run; it never reads the page's scripts,
// player data or traffic. The shapes were written from the addresses these sites show in the
// address bar (CLEAN-ROOM.md), never from another program's extractor patterns. Pure.

import { storeExcludes } from './store-build';

/** YouTube and the curated video sites (VIDEO-DETECT-SPEC §3.8). */
export type SiteName =
  | 'youtube'
  | 'vimeo'
  | 'x'
  | 'facebook'
  | 'instagram'
  | 'tiktok'
  | 'dailymotion'
  | 'reddit'
  | 'twitch'
  | 'streamable';
export type SiteVideoKind = 'watch' | 'shorts' | 'embed' | 'live' | 'clip';

export interface SiteVideo {
  site: SiteName;
  /** The video id (YouTube: 11 characters), or a clip's own id. */
  id: string;
  kind: SiteVideoKind;
  /** Identity of the video across its addresses: `<site>:<id>` (`youtube:<id>`, `twitch:clip:<slug>`). */
  key: string;
  /** The canonical address sent to Downex (`https://www.youtube.com/watch?v=<id>`; a clip keeps its own). */
  pageUrl: string;
  /** Its qualities may be got ahead while the user watches (YouTube: `watch` and `shorts`; never an embed). */
  lookAhead: boolean;
}

/** Display names of the sites, shown for their rows whatever yt-dlp calls its extractor. */
export const SITE_NAMES: Readonly<Record<SiteName, string>> = {
  youtube: 'YouTube',
  vimeo: 'Vimeo',
  x: 'X',
  facebook: 'Facebook',
  instagram: 'Instagram',
  tiktok: 'TikTok',
  dailymotion: 'Dailymotion',
  reddit: 'Reddit',
  twitch: 'Twitch',
  streamable: 'Streamable',
};

/** The site of a video key (`vimeo:123` → `vimeo`); undefined for a generic `page:` key. */
export function siteOfKey(key: string): SiteName | undefined {
  const site = key.slice(0, key.indexOf(':'));
  return Object.hasOwn(SITE_NAMES, site) ? (site as SiteName) : undefined;
}

// ---------------------------------------------------------------------------------------
// YouTube (SITE-MEDIA-SPEC §5.1)

const VIDEO_ID = /^[A-Za-z0-9_-]{11}$/;
/** Clip ids are longer than video ids ("Ugkx…", 36 characters today). */
const CLIP_ID = /^[A-Za-z0-9_-]{12,64}$/;
const YOUTUBE_HOSTS: ReadonlySet<string> = new Set(['www.youtube.com', 'm.youtube.com', 'music.youtube.com', 'youtube.com']);
const NOCOOKIE_HOSTS: ReadonlySet<string> = new Set(['www.youtube-nocookie.com', 'youtube-nocookie.com']);
/** Path forms on the YouTube hosts: /<form>/<id>. */
const PATH_KINDS: Readonly<Record<string, SiteVideoKind>> = {
  shorts: 'shorts',
  embed: 'embed',
  live: 'live',
  v: 'embed',
};

function youtubeVideo(id: string, kind: SiteVideoKind): SiteVideo | undefined {
  if (!VIDEO_ID.test(id)) return undefined;
  return {
    site: 'youtube',
    id,
    kind,
    key: `youtube:${id}`,
    pageUrl: `https://www.youtube.com/watch?v=${id}`,
    lookAhead: kind === 'watch' || kind === 'shorts',
  };
}

function youtube(u: URL, host: string, segs: readonly string[]): SiteVideo | undefined {
  if (host === 'youtu.be') return segs.length === 1 ? youtubeVideo(segs[0] ?? '', 'watch') : undefined;
  if (NOCOOKIE_HOSTS.has(host)) {
    return segs.length === 2 && segs[0] === 'embed' ? youtubeVideo(segs[1] ?? '', 'embed') : undefined;
  }
  if (segs.length === 1 && segs[0] === 'watch') {
    const ids = u.searchParams.getAll('v');
    return ids.length === 1 ? youtubeVideo(ids[0] ?? '', 'watch') : undefined;
  }
  if (segs.length !== 2) return undefined;
  const [form = '', id = ''] = segs;
  if (form === 'clip') {
    if (!CLIP_ID.test(id)) return undefined;
    const page = new URL(`https://${host}/clip/${id}`);
    return { site: 'youtube', id, kind: 'clip', key: `youtube:${id}`, pageUrl: page.href, lookAhead: false };
  }
  const kind = Object.hasOwn(PATH_KINDS, form) ? PATH_KINDS[form] : undefined;
  return kind ? youtubeVideo(id, kind) : undefined;
}

// ---------------------------------------------------------------------------------------
// Curated sites (VIDEO-DETECT-SPEC §3.8)

/** A path segment that names a user, channel, page or slug (not an id). */
const NAME = /^[A-Za-z0-9._-]{1,100}$/;

const VIMEO_ID = /^[0-9]{1,12}$/;
const VIMEO_HASH = /^[0-9a-f]{6,20}$/;
const X_ID = /^[0-9]{1,20}$/;
const X_PHOTO_OR_VIDEO_N = /^[0-9]{1,3}$/;
const FACEBOOK_ID = /^[0-9]{1,20}$/;
const FB_WATCH_CODE = /^[A-Za-z0-9_-]{4,32}$/;
const INSTAGRAM_CODE = /^[A-Za-z0-9_-]{5,64}$/;
const TIKTOK_USER = /^@[A-Za-z0-9._]{1,64}$/;
const TIKTOK_ID = /^[0-9]{1,25}$/;
const DAILYMOTION_ID = /^[A-Za-z0-9]{5,12}$/;
const REDDIT_SUB = /^[A-Za-z0-9_]{2,32}$/;
const REDDIT_ID = /^[a-z0-9]{2,12}$/;
const TWITCH_ID = /^[0-9]{1,15}$/;
const TWITCH_CLIP = /^[A-Za-z0-9_-]{4,100}$/;
const STREAMABLE_ID = /^[a-z0-9]{4,12}$/;
/** Streamable's own pages, which have the form of a video id. */
const STREAMABLE_PAGES: ReadonlySet<string> = new Set([
  'about',
  'blog',
  'careers',
  'contact',
  'documentation',
  'help',
  'login',
  'pricing',
  'privacy',
  'settings',
  'signup',
  'terms',
  'upload',
]);

const hosts = (...names: string[]): ReadonlySet<string> => new Set(names);
const VIMEO_HOSTS = hosts('vimeo.com', 'www.vimeo.com');
const X_HOSTS = hosts('x.com', 'www.x.com', 'mobile.x.com', 'twitter.com', 'www.twitter.com', 'mobile.twitter.com');
const FACEBOOK_HOSTS = hosts('facebook.com', 'www.facebook.com', 'm.facebook.com', 'web.facebook.com');
const INSTAGRAM_HOSTS = hosts('instagram.com', 'www.instagram.com');
const TIKTOK_HOSTS = hosts('tiktok.com', 'www.tiktok.com', 'm.tiktok.com');
const DAILYMOTION_HOSTS = hosts('dailymotion.com', 'www.dailymotion.com');
const REDDIT_HOSTS = hosts('reddit.com', 'www.reddit.com', 'old.reddit.com', 'new.reddit.com');
const TWITCH_HOSTS = hosts('twitch.tv', 'www.twitch.tv', 'm.twitch.tv');
const STREAMABLE_HOSTS = hosts('streamable.com', 'www.streamable.com');

function curated(site: SiteName, id: string, kind: SiteVideoKind, pageUrl: string, lookAhead: boolean, key = `${site}:${id}`): SiteVideo {
  return { site, id, kind, key, pageUrl, lookAhead };
}

function vimeo(u: URL, host: string, segs: readonly string[]): SiteVideo | undefined {
  if (host === 'player.vimeo.com') {
    const [form, id = ''] = segs;
    if (segs.length !== 2 || form !== 'video' || !VIMEO_ID.test(id)) return undefined;
    const hash = u.searchParams.get('h');
    const page = `https://player.vimeo.com/video/${id}${hash && VIMEO_HASH.test(hash) ? `?h=${hash}` : ''}`;
    return curated('vimeo', id, 'embed', page, false);
  }
  if (!VIMEO_HOSTS.has(host)) return undefined;
  const watch = (id: string, hash?: string) =>
    VIMEO_ID.test(id) ? curated('vimeo', id, 'watch', `https://vimeo.com/${id}${hash ? `/${hash}` : ''}`, true) : undefined;
  const [a = '', b = '', c = '', d = ''] = segs;
  switch (segs.length) {
    case 1:
      return watch(a);
    case 2:
      return VIMEO_HASH.test(b) ? watch(a, b) : undefined;
    case 3:
      return a === 'channels' && NAME.test(b) ? watch(c) : undefined;
    case 4:
      if (a === 'groups' && c === 'videos' && NAME.test(b)) return watch(d);
      if (a === 'showcase' && c === 'video' && NAME.test(b)) return watch(d);
      return undefined;
    default:
      return undefined;
  }
}

function x(segs: readonly string[]): SiteVideo | undefined {
  const watch = (id: string) => (X_ID.test(id) ? curated('x', id, 'watch', `https://x.com/i/status/${id}`, true) : undefined);
  const [a = '', b = '', c = '', d = '', e = ''] = segs;
  if (segs.length === 4 && a === 'i' && b === 'web' && c === 'status') return watch(d);
  if (b !== 'status' || !NAME.test(a)) return undefined;
  if (segs.length === 3) return watch(c);
  if (segs.length === 5 && d === 'video' && X_PHOTO_OR_VIDEO_N.test(e)) return watch(c);
  return undefined;
}

function facebook(u: URL, host: string, segs: readonly string[]): SiteVideo | undefined {
  if (host === 'fb.watch') {
    const [code = ''] = segs;
    return segs.length === 1 && FB_WATCH_CODE.test(code) ? curated('facebook', code, 'watch', `https://fb.watch/${code}/`, false) : undefined;
  }
  if (!FACEBOOK_HOSTS.has(host)) return undefined;
  const watch = (id: string) =>
    FACEBOOK_ID.test(id) ? curated('facebook', id, 'watch', `https://www.facebook.com/watch/?v=${id}`, false) : undefined;
  const [a = '', b = '', c = '', d = ''] = segs;
  if (segs.length === 1 && a === 'watch') {
    const ids = u.searchParams.getAll('v');
    return ids.length === 1 ? watch(ids[0] ?? '') : undefined;
  }
  if (segs.length === 2 && a === 'reel') {
    return FACEBOOK_ID.test(b) ? curated('facebook', b, 'watch', `https://www.facebook.com/reel/${b}`, false) : undefined;
  }
  if (segs.length === 2 && a === 'videos') return watch(b);
  if (b !== 'videos' || !NAME.test(a)) return undefined;
  if (segs.length === 3) return watch(c);
  if (segs.length === 4 && NAME.test(c)) return watch(d);
  return undefined;
}

function instagram(segs: readonly string[]): SiteVideo | undefined {
  const post = (form: 'p' | 'reel' | 'tv', code: string) =>
    INSTAGRAM_CODE.test(code) ? curated('instagram', code, 'watch', `https://www.instagram.com/${form}/${code}/`, false) : undefined;
  const [a = '', b = '', c = ''] = segs;
  if (segs.length === 3) return b === 'p' && NAME.test(a) ? post('p', c) : undefined;
  if (segs.length !== 2) return undefined;
  if (a === 'p') return post('p', b);
  if (a === 'reel' || a === 'reels') return post('reel', b);
  if (a === 'tv') return post('tv', b);
  return undefined;
}

function tiktok(segs: readonly string[]): SiteVideo | undefined {
  const [a = '', b = '', c = ''] = segs;
  const embed = (id: string) =>
    TIKTOK_ID.test(id) ? curated('tiktok', id, 'embed', `https://www.tiktok.com/embed/v2/${id}`, false) : undefined;
  if (a === 'embed') {
    if (segs.length === 3 && b === 'v2') return embed(c);
    return segs.length === 2 ? embed(b) : undefined;
  }
  if (segs.length !== 3 || b !== 'video' || !TIKTOK_USER.test(a) || !TIKTOK_ID.test(c)) return undefined;
  return curated('tiktok', c, 'watch', `https://www.tiktok.com/${a}/video/${c}`, true);
}

function dailymotion(host: string, segs: readonly string[]): SiteVideo | undefined {
  const video = (id: string, kind: SiteVideoKind) =>
    DAILYMOTION_ID.test(id)
      ? curated('dailymotion', id, kind, `https://www.dailymotion.com/video/${id}`, kind === 'watch')
      : undefined;
  const [a = '', b = '', c = ''] = segs;
  if (host === 'dai.ly') return segs.length === 1 ? video(a, 'watch') : undefined;
  if (segs.length === 2 && a === 'video') return video(b, 'watch');
  if (segs.length === 3 && a === 'embed' && b === 'video') return video(c, 'embed');
  return undefined;
}

function reddit(segs: readonly string[]): SiteVideo | undefined {
  // /r/<sub>/comments/<id>[/<slug>...]: the trailing segments (title slug, comment) are ignored.
  const [a = '', sub = '', c = '', id = ''] = segs;
  if (segs.length < 4 || a !== 'r' || c !== 'comments' || !REDDIT_SUB.test(sub) || !REDDIT_ID.test(id)) return undefined;
  return curated('reddit', id, 'watch', `https://www.reddit.com/r/${sub}/comments/${id}/`, true);
}

function twitch(u: URL, host: string, segs: readonly string[]): SiteVideo | undefined {
  const vod = (id: string, kind: SiteVideoKind) =>
    TWITCH_ID.test(id) ? curated('twitch', id, kind, `https://www.twitch.tv/videos/${id}`, kind === 'watch') : undefined;
  const clip = (slug: string) =>
    TWITCH_CLIP.test(slug)
      ? curated('twitch', slug, 'clip', `https://clips.twitch.tv/${slug}`, true, `twitch:clip:${slug}`)
      : undefined;
  if (host === 'clips.twitch.tv') return segs.length === 1 ? clip(segs[0] ?? '') : undefined;
  if (host === 'player.twitch.tv') {
    const videos = u.searchParams.getAll('video');
    const v = videos.length === 1 ? (videos[0] ?? '') : '';
    return segs.length === 0 && v.startsWith('v') ? vod(v.slice(1), 'embed') : undefined;
  }
  if (!TWITCH_HOSTS.has(host)) return undefined;
  const [a = '', b = '', c = ''] = segs;
  if (segs.length === 2 && a === 'videos') return vod(b, 'watch');
  if (segs.length === 3 && b === 'clip' && NAME.test(a)) return clip(c);
  return undefined;
}

function streamable(segs: readonly string[]): SiteVideo | undefined {
  const video = (id: string, kind: 'watch' | 'embed') =>
    STREAMABLE_ID.test(id) && !STREAMABLE_PAGES.has(id)
      ? curated('streamable', id, kind, `https://streamable.com/${id}`, kind === 'watch')
      : undefined;
  const [a = '', b = ''] = segs;
  if (segs.length === 1) return video(a, 'watch');
  if (segs.length === 2 && a === 'e') return video(b, 'embed');
  return undefined;
}

/**
 * The video an address shows: YouTube's (`www./m./music.youtube.com/watch?v=<id>`, `/shorts/<id>`,
 * `/embed/<id>`, `/live/<id>`, `/v/<id>`, `youtube-nocookie.com/embed/<id>`, `youtu.be/<id>`,
 * `/clip/<clip id>`) and the curated sites' of VIDEO-DETECT-SPEC §3.8. Undefined for anything else
 * (channels, feeds, timelines, profiles, the home pages, live channels, addresses with user
 * information or another scheme).
 */
export function parseSiteVideo(raw: string | undefined): SiteVideo | undefined {
  if (!raw) return undefined;
  let u: URL;
  try {
    u = new URL(raw);
  } catch {
    return undefined;
  }
  if (u.protocol !== 'https:' && u.protocol !== 'http:') return undefined;
  if (u.username || u.password) return undefined;
  const host = u.hostname.toLowerCase();
  const segs = u.pathname.split('/').filter((s) => s !== '');
  if (YOUTUBE_HOSTS.has(host) || NOCOOKIE_HOSTS.has(host) || host === 'youtu.be') {
    // The store build knows no YouTube video: no row, look-ahead or menu hand-off follows. The
    // mode is spelled out (storeBuild's test) so that the minifier drops youtube() from that build.
    return import.meta.env.MODE === 'store' ? undefined : youtube(u, host, segs);
  }
  if (VIMEO_HOSTS.has(host) || host === 'player.vimeo.com') return vimeo(u, host, segs);
  if (X_HOSTS.has(host)) return x(segs);
  if (FACEBOOK_HOSTS.has(host) || host === 'fb.watch') return facebook(u, host, segs);
  if (INSTAGRAM_HOSTS.has(host)) return instagram(segs);
  if (TIKTOK_HOSTS.has(host)) return tiktok(segs);
  if (DAILYMOTION_HOSTS.has(host) || host === 'dai.ly') return dailymotion(host, segs);
  if (REDDIT_HOSTS.has(host)) return reddit(segs);
  if (TWITCH_HOSTS.has(host) || host === 'clips.twitch.tv' || host === 'player.twitch.tv') return twitch(u, host, segs);
  if (STREAMABLE_HOSTS.has(host)) return streamable(segs);
  return undefined;
}

/**
 * Key of the user-initiated "Find the video on this page" probe of any other page (§5.2). The
 * store build has none for YouTube's pages, which yt-dlp would resolve all the same.
 */
export function genericVideoKey(pageUrl: string): string | undefined {
  if (storeExcludes(pageUrl)) return undefined;
  try {
    const u = new URL(pageUrl);
    if (u.protocol !== 'https:' && u.protocol !== 'http:') return undefined;
    u.hash = '';
    return `page:${u.href}`;
  } catch {
    return undefined;
  }
}

/** The page address of a generic key; undefined for a site key. */
export function genericPageUrl(key: string): string | undefined {
  return key.startsWith('page:') ? key.slice('page:'.length) : undefined;
}

/** Whether two addresses have the same origin (scheme, host and port). */
export function sameOrigin(a: string | undefined, b: string | undefined): boolean {
  if (!a || !b) return false;
  try {
    return new URL(a).origin === new URL(b).origin;
  } catch {
    return false;
  }
}

/** An http(s) address, for a Referer; undefined otherwise. */
export function httpAddress(v: string | undefined): string | undefined {
  if (!v) return undefined;
  try {
    const p = new URL(v).protocol;
    return p === 'http:' || p === 'https:' ? v : undefined;
  } catch {
    return undefined;
  }
}
