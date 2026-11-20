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
  | 'streamable'
  | 'bluesky'
  | 'pinterest'
  | 'linkedin'
  | 'kick'
  | 'bilibili'
  | 'vk'
  | 'odysee'
  | 'bitchute'
  | 'ninegag'
  | 'imgur'
  | 'telegram'
  | 'tumblr'
  | 'rumble'
  | 'threads'
  | 'snapchat'
  | 'soundcloud'
  | 'bandcamp'
  | 'mixcloud';
/**
 * What an address shows (§3.8): `post` is a post page that may hold a video, `link` a share
 * address that redirects to one (sent as it is). Neither ever gets a look-ahead. `audio` is a
 * track of an audio site (SoundCloud, Bandcamp, Mixcloud; SITE-MEDIA-SPEC §3.13): a page without
 * video whose audio Downex saves; never a look-ahead, no in-page button.
 */
export type SiteVideoKind = 'watch' | 'shorts' | 'embed' | 'live' | 'clip' | 'post' | 'link' | 'audio';
/**
 * Who reads the video: yt-dlp, or nobody but the browser (`network`: yt-dlp has no working path
 * for the site; only the File and Stream rows of the browser's own traffic are offered).
 */
export type SiteResolver = 'yt-dlp' | 'network';

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
  resolver: SiteResolver;
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
  bluesky: 'Bluesky',
  pinterest: 'Pinterest',
  linkedin: 'LinkedIn',
  kick: 'Kick',
  bilibili: 'Bilibili',
  vk: 'VK',
  odysee: 'Odysee',
  bitchute: 'BitChute',
  ninegag: '9GAG',
  imgur: 'Imgur',
  telegram: 'Telegram',
  tumblr: 'Tumblr',
  rumble: 'Rumble',
  threads: 'Threads',
  snapchat: 'Snapchat',
  soundcloud: 'SoundCloud',
  bandcamp: 'Bandcamp',
  mixcloud: 'Mixcloud',
};

/** The text of a `network` site's video, where yt-dlp would be asked elsewhere (§3.8, §5.6). */
export const NETWORK_ONLY_TEXT = 'Downex can’t read this site’s videos yet. Play the video, then try again.';
/** … in a panel whose video already played and still gave the browser nothing to list (§5.6). */
export const NETWORK_PLAYED_TEXT = 'Downex can’t save this site’s videos yet.';
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
    resolver: 'yt-dlp',
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
    return { site: 'youtube', id, kind: 'clip', key: `youtube:${id}`, pageUrl: page.href, lookAhead: false, resolver: 'yt-dlp' };
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
const X_BROADCAST = /^[A-Za-z0-9]{8,20}$/;
const FACEBOOK_ID = /^[0-9]{1,20}$/;
const FACEBOOK_POST = /^(?:[0-9]{1,20}|pfbid[A-Za-z0-9]{10,80})$/;
const FB_WATCH_CODE = /^[A-Za-z0-9_-]{4,32}$/;
const INSTAGRAM_CODE = /^[A-Za-z0-9_-]{5,64}$/;
const INSTAGRAM_STORY = /^[0-9]{1,25}$/;
const TIKTOK_USER = /^@[A-Za-z0-9._-]{1,64}$/;
const TIKTOK_ID = /^[0-9]{1,25}$/;
/** The code of a TikTok or Reddit share address (`vm.tiktok.com/<code>`, `/t/<code>`, `/r/<sub>/s/<code>`). */
const SHARE_CODE = /^[A-Za-z0-9]{5,16}$/;
/** The code of a `pin.it/<code>` or `b23.tv/<code>` share address. */
const SHORT_SHARE_CODE = /^[A-Za-z0-9]{4,16}$/;
const DAILYMOTION_ID = /^[A-Za-z0-9]{5,12}$/;
const DAILYMOTION_PLAYER = /^[A-Za-z0-9]{1,20}\.html$/;
const REDDIT_SUB = /^[A-Za-z0-9_]{2,32}$/;
const REDDIT_ID = /^[a-z0-9]{2,12}$/;
const REDDIT_VIDEO = /^[a-z0-9]{5,20}$/;
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
const BLUESKY_HANDLE = /^[A-Za-z0-9.:_-]{3,253}$/;
const BLUESKY_POST = /^[a-z0-9]{10,16}$/;
const PINTEREST_ID = /^[0-9]{1,25}$/;
const LINKEDIN_SLUG = /^[A-Za-z0-9._%-]{3,300}$/;
const LINKEDIN_ACTIVITY = /^urn:li:activity:([0-9]{1,25})$/;
const KICK_VIDEO = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const KICK_CLIP = /^[A-Za-z0-9_]{4,64}$/;
const BILIBILI_ID = /^(?:BV[A-Za-z0-9]{10}|av[0-9]{1,12})$/;
const BILIBILI_PART = /^[1-9][0-9]{0,3}$/;
const VK_VIDEO = /^video(-?[0-9]{1,20}_[0-9]{1,20})$/;
const ODYSEE_CHANNEL = /^@[A-Za-z0-9._%-]{1,100}(?::[0-9a-f]{1,40})?$/;
const ODYSEE_NAME = /^[A-Za-z0-9._%-]{1,200}(?::[0-9a-f]{1,40})?$/;
const BITCHUTE_ID = /^[A-Za-z0-9_-]{6,20}$/;
const NINEGAG_ID = /^[A-Za-z0-9]{5,12}$/;
const IMGUR_ID = /^[A-Za-z0-9]{5,8}$/;
/** Imgur's own pages are lower-case words; a post id holds a digit or an upper-case letter. */
const IMGUR_NOT_A_WORD = /[0-9A-Z]/;
const IMGUR_GALLERY = /^[A-Za-z0-9_-]{5,200}$/;
const TELEGRAM_CHANNEL = /^[A-Za-z][A-Za-z0-9_]{3,31}$/;
const TELEGRAM_POST = /^[0-9]{1,10}$/;
const TUMBLR_BLOG = /^[a-z0-9-]{1,32}$/;
const TUMBLR_POST = /^[0-9]{1,20}$/;
/** Tumblr's own pages on www.tumblr.com, which have the form of `/<blog>/<id>`. */
const TUMBLR_PAGES: ReadonlySet<string> = new Set(['blog', 'dashboard', 'explore', 'following', 'likes', 'post', 'search', 'settings', 'tagged', 'www']);
/** A Rumble video id holds a digit, unlike its own pages (`/videos`, `/vids`). */
const RUMBLE_PAGE = /^(v(?=[a-z0-9]*[0-9])[a-z0-9]{4,10})(?:-[A-Za-z0-9_-]{1,200}\.html)?$/;
const RUMBLE_ID = /^v(?=[a-z0-9]*[0-9])[a-z0-9]{4,10}$/;
const THREADS_USER = /^@[A-Za-z0-9._]{1,30}$/;
const THREADS_CODE = /^[A-Za-z0-9_-]{5,64}$/;
const SNAPCHAT_ID = /^[A-Za-z0-9_-]{10,200}$/;
const SOUNDCLOUD_USER = /^[a-z0-9_-]{1,64}$/;
const SOUNDCLOUD_TRACK = /^[A-Za-z0-9_-]{1,200}$/;
const SOUNDCLOUD_SECRET = /^s-[A-Za-z0-9]{4,32}$/;
const SOUNDCLOUD_LINK = /^[A-Za-z0-9]{4,32}$/;
/** SoundCloud's own pages, which have the form of `/<user>/...`. */
const SOUNDCLOUD_PAGES: ReadonlySet<string> = new Set([
  'charts',
  'discover',
  'feed',
  'jobs',
  'messages',
  'mobile',
  'notifications',
  'pages',
  'people',
  'pro',
  'search',
  'settings',
  'signin',
  'stations',
  'stream',
  'tags',
  'terms-of-use',
  'upload',
  'you',
]);
/** A SoundCloud profile's tabs, which have the form of `/<user>/<track>`. */
const SOUNDCLOUD_TABS: ReadonlySet<string> = new Set([
  'albums',
  'comments',
  'followers',
  'following',
  'likes',
  'popular-tracks',
  'reposts',
  'sets',
  'spotlight',
  'tracks',
]);
const BANDCAMP_ARTIST = /^[a-z0-9-]{1,63}$/;
const BANDCAMP_SLUG = /^[A-Za-z0-9_-]{1,200}$/;
const MIXCLOUD_USER = /^[A-Za-z0-9_-]{1,64}$/;
/**
 * A mix's name as the address carries it: letters, digits, "_" and "-", and the percent-encoded
 * octets of a title in another script (the address bar shows them decoded; the tab's URL keeps
 * them encoded, and so does the key).
 */
const MIXCLOUD_MIX = /^(?:[A-Za-z0-9_-]|%[0-9A-Fa-f]{2}){1,600}$/;
/** Mixcloud's own pages, which have the form of `/<user>/<mix>`. */
const MIXCLOUD_PAGES: ReadonlySet<string> = new Set([
  'about',
  'categories',
  'dashboard',
  'discover',
  'genres',
  'live',
  'messages',
  'notifications',
  'player',
  'pro',
  'search',
  'select',
  'settings',
  'tag',
  'upload',
  'widget',
]);
/** A Mixcloud profile's tabs, which have the form of `/<user>/<mix>`. */
const MIXCLOUD_TABS: ReadonlySet<string> = new Set([
  'favorites',
  'followers',
  'following',
  'history',
  'listens',
  'playlists',
  'posts',
  'reposts',
  'shows',
  'stream',
  'uploads',
]);

const hosts = (...names: string[]): ReadonlySet<string> => new Set(names);
const VIMEO_HOSTS = hosts('vimeo.com', 'www.vimeo.com');
const X_HOSTS = hosts('x.com', 'www.x.com', 'mobile.x.com', 'twitter.com', 'www.twitter.com', 'mobile.twitter.com');
const FACEBOOK_HOSTS = hosts('facebook.com', 'www.facebook.com', 'm.facebook.com', 'web.facebook.com');
const INSTAGRAM_HOSTS = hosts('instagram.com', 'www.instagram.com');
const TIKTOK_HOSTS = hosts('tiktok.com', 'www.tiktok.com', 'm.tiktok.com');
const TIKTOK_SHORT_HOSTS = hosts('vm.tiktok.com', 'vt.tiktok.com');
const DAILYMOTION_HOSTS = hosts('dailymotion.com', 'www.dailymotion.com');
const REDDIT_HOSTS = hosts('reddit.com', 'www.reddit.com', 'old.reddit.com', 'new.reddit.com');
const TWITCH_HOSTS = hosts('twitch.tv', 'www.twitch.tv', 'm.twitch.tv');
const STREAMABLE_HOSTS = hosts('streamable.com', 'www.streamable.com');
const PINTEREST_HOSTS = hosts('pinterest.com', 'www.pinterest.com');
const LINKEDIN_HOSTS = hosts('linkedin.com', 'www.linkedin.com');
const KICK_HOSTS = hosts('kick.com', 'www.kick.com');
const BILIBILI_HOSTS = hosts('bilibili.com', 'www.bilibili.com', 'm.bilibili.com');
const VK_HOSTS = hosts('vk.com', 'www.vk.com', 'm.vk.com', 'vkvideo.ru', 'www.vkvideo.ru');
const BITCHUTE_HOSTS = hosts('bitchute.com', 'www.bitchute.com');
const IMGUR_HOSTS = hosts('imgur.com', 'www.imgur.com', 'm.imgur.com');
const RUMBLE_HOSTS = hosts('rumble.com', 'www.rumble.com');
const THREADS_HOSTS = hosts('threads.com', 'www.threads.com', 'threads.net', 'www.threads.net');
const SNAPCHAT_HOSTS = hosts('snapchat.com', 'www.snapchat.com');
const SOUNDCLOUD_HOSTS = hosts('soundcloud.com', 'www.soundcloud.com', 'm.soundcloud.com');
const MIXCLOUD_HOSTS = hosts('mixcloud.com', 'www.mixcloud.com', 'm.mixcloud.com');

function curated(
  site: SiteName,
  id: string,
  kind: SiteVideoKind,
  pageUrl: string,
  lookAhead: boolean,
  key = `${site}:${id}`,
  resolver: SiteResolver = 'yt-dlp',
): SiteVideo {
  return { site, id, kind, key, pageUrl, lookAhead, resolver };
}

/** A share address that redirects to a video: sent as it is, never looked ahead. */
function shareLink(site: SiteName, code: string, pageUrl: string, shape = SHARE_CODE): SiteVideo | undefined {
  return shape.test(code) ? curated(site, code, 'link', pageUrl, false, `${site}:link:${code}`) : undefined;
}

/** The one value of a query parameter given exactly once; undefined otherwise. */
function single(u: URL, name: string): string | undefined {
  const all = u.searchParams.getAll(name);
  return all.length === 1 ? all[0] : undefined;
}

function vimeo(u: URL, host: string, segs: readonly string[]): SiteVideo | undefined {
  // Both forms go to Downex as the player's address: yt-dlp reads it anonymously, whereas the
  // page's own address answers "only works when logged-in" today (§3.8, 2026-11-18).
  const player = (id: string, hash: string | undefined) =>
    `https://player.vimeo.com/video/${id}${hash && VIMEO_HASH.test(hash) ? `?h=${hash}` : ''}`;
  if (host === 'player.vimeo.com') {
    const [form, id = ''] = segs;
    if (segs.length !== 2 || form !== 'video' || !VIMEO_ID.test(id)) return undefined;
    return curated('vimeo', id, 'embed', player(id, u.searchParams.get('h') ?? undefined), false);
  }
  if (!VIMEO_HOSTS.has(host)) return undefined;
  const watch = (id: string, hash?: string) => (VIMEO_ID.test(id) ? curated('vimeo', id, 'watch', player(id, hash), true) : undefined);
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

function x(u: URL, host: string, segs: readonly string[]): SiteVideo | undefined {
  if (host === 'platform.twitter.com') {
    // An embedded post (§3.8 "Embeds", 2026-11-18): it may hold a video, or not.
    const id = segs.length === 2 && segs[0] === 'embed' && segs[1] === 'Tweet.html' ? single(u, 'id') : undefined;
    return id !== undefined && X_ID.test(id) ? curated('x', id, 'post', `https://x.com/i/status/${id}`, false) : undefined;
  }
  const watch = (id: string) => (X_ID.test(id) ? curated('x', id, 'watch', `https://x.com/i/status/${id}`, true) : undefined);
  const [a = '', b = '', c = '', d = '', e = ''] = segs;
  if (segs.length === 3 && a === 'i' && b === 'broadcasts') {
    // A broadcast may still be live: never looked ahead.
    return X_BROADCAST.test(c) ? curated('x', c, 'watch', `https://x.com/i/broadcasts/${c}`, false, `x:broadcast:${c}`) : undefined;
  }
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
  const watch = (id: string | undefined) =>
    id !== undefined && FACEBOOK_ID.test(id) ? curated('facebook', id, 'watch', `https://www.facebook.com/watch/?v=${id}`, false) : undefined;
  const post = (id: string, pageUrl: string) =>
    FACEBOOK_POST.test(id) ? curated('facebook', id, 'post', pageUrl, false, `facebook:post:${id}`) : undefined;
  const [a = '', b = '', c = '', d = ''] = segs;
  if (segs.length === 2 && a === 'plugins' && (b === 'video.php' || b === 'post.php')) {
    // An embedded video or post (§3.8 "Embeds", 2026-11-18): the Facebook address it names.
    const inner = facebookPluginTarget(single(u, 'href'));
    if (!inner) return undefined;
    return b === 'video.php' ? { ...inner, kind: inner.kind === 'watch' ? 'embed' : inner.kind } : inner;
  }
  if (segs.length === 1 && (a === 'watch' || a === 'video.php')) return watch(single(u, 'v'));
  if (segs.length === 2 && a === 'watch' && b === 'live') return watch(single(u, 'v'));
  if (segs.length === 1 && (a === 'permalink.php' || a === 'story.php')) {
    const story = single(u, 'story_fbid');
    const owner = single(u, 'id');
    if (story === undefined || owner === undefined || !FACEBOOK_ID.test(owner)) return undefined;
    return post(story, `https://www.facebook.com/permalink.php?story_fbid=${story}&id=${owner}`);
  }
  if (segs.length === 2 && a === 'reel') {
    return FACEBOOK_ID.test(b) ? curated('facebook', b, 'watch', `https://www.facebook.com/reel/${b}`, false) : undefined;
  }
  if (segs.length === 2 && a === 'videos') return watch(b);
  if (segs.length === 4 && a === 'groups' && NAME.test(b) && (c === 'posts' || c === 'permalink')) {
    return post(d, `https://www.facebook.com/groups/${b}/posts/${d}`);
  }
  if (segs.length === 3 && b === 'posts' && NAME.test(a) && a !== 'groups') return post(c, `https://www.facebook.com/${a}/posts/${c}`);
  if (b !== 'videos' || !NAME.test(a)) return undefined;
  if (segs.length === 3) return watch(c);
  if (segs.length === 4 && NAME.test(c)) return watch(d);
  return undefined;
}

/**
 * The video a Facebook embed's `href` names: a Facebook address of the table on one of its main
 * hosts, never another plugin's (no nesting) and never `fb.watch`.
 */
function facebookPluginTarget(href: string | undefined): SiteVideo | undefined {
  if (!href) return undefined;
  let inner: URL;
  try {
    inner = new URL(href);
  } catch {
    return undefined;
  }
  const host = inner.hostname.toLowerCase();
  if (!FACEBOOK_HOSTS.has(host) || inner.pathname.split('/').includes('plugins')) return undefined;
  const v = parseSiteVideo(inner.href);
  return v?.site === 'facebook' ? v : undefined;
}

/** A carousel slide in an Instagram post's address: 1 to 999, no leading zero. */
const SLIDE = /^[1-9][0-9]{0,2}$/;

/**
 * The carousel slide an Instagram post's address shows, 1 first: the browser's address of a post
 * whose carousel was moved ends `?img_index=3` (SITE-MEDIA-SPEC §3.12 "Which item": the panel
 * opens on the item the user looks at). Undefined for any other address, a post address without
 * it, or a value that is not one small whole number.
 */
export function addressSlide(raw: string | undefined): number | undefined {
  if (!raw || siteOfAddress(raw) !== 'instagram' || !parseSiteVideo(raw)) return undefined;
  let values: string[];
  try {
    values = new URL(raw).searchParams.getAll('img_index');
  } catch {
    return undefined;
  }
  return values.length === 1 && SLIDE.test(values[0]!) ? Number(values[0]) : undefined;
}

/**
 * An Instagram address: a post, reel or tv video, its embed (`…/embed`, `…/embed/captioned`; a
 * `/p/` embed is a `post`, it may be photos), a story or a highlight (§3.8, 2026-11-18).
 */
function instagram(segs: readonly string[]): SiteVideo | undefined {
  const [a = '', b = '', c = ''] = segs;
  if (a === 'stories') {
    if (segs.length !== 3 || !INSTAGRAM_STORY.test(c)) return undefined;
    if (b === 'highlights') {
      return curated('instagram', c, 'post', `https://www.instagram.com/stories/highlights/${c}/`, false, `instagram:highlight:${c}`);
    }
    return NAME.test(b) ? curated('instagram', c, 'post', `https://www.instagram.com/stories/${b}/${c}/`, false, `instagram:story:${c}`) : undefined;
  }
  const n = segs.length;
  const tail = n >= 2 && segs[n - 2] === 'embed' && segs[n - 1] === 'captioned' ? 2 : n >= 1 && segs[n - 1] === 'embed' ? 1 : 0;
  const v = instagramPost(tail ? segs.slice(0, n - tail) : segs);
  if (!v || !tail) return v;
  return { ...v, kind: v.pageUrl.includes('/p/') ? 'post' : 'embed' };
}

function instagramPost(segs: readonly string[]): SiteVideo | undefined {
  const post = (form: 'p' | 'reel' | 'tv', code: string) =>
    INSTAGRAM_CODE.test(code) ? curated('instagram', code, 'watch', `https://www.instagram.com/${form}/${code}/`, false) : undefined;
  const [a = '', b = '', c = ''] = segs;
  if (segs.length === 3) {
    if (!NAME.test(a)) return undefined;
    return b === 'p' || b === 'reel' || b === 'tv' ? post(b, c) : undefined;
  }
  if (segs.length !== 2) return undefined;
  if (a === 'p') return post('p', b);
  if (a === 'reel' || a === 'reels') return post('reel', b);
  if (a === 'tv') return post('tv', b);
  return undefined;
}

function tiktok(host: string, segs: readonly string[]): SiteVideo | undefined {
  const [a = '', b = '', c = ''] = segs;
  if (TIKTOK_SHORT_HOSTS.has(host)) return segs.length === 1 ? shareLink('tiktok', a, `https://${host}/${a}/`) : undefined;
  if (segs.length === 2 && a === 't') return shareLink('tiktok', b, `https://www.tiktok.com/t/${b}/`);
  const embed = (id: string) =>
    TIKTOK_ID.test(id) ? curated('tiktok', id, 'embed', `https://www.tiktok.com/embed/v2/${id}`, false) : undefined;
  if (a === 'embed') {
    if (segs.length === 3 && b === 'v2') return embed(c);
    return segs.length === 2 ? embed(b) : undefined;
  }
  if (segs.length !== 3 || b !== 'video' || !TIKTOK_USER.test(a) || !TIKTOK_ID.test(c)) return undefined;
  return curated('tiktok', c, 'watch', `https://www.tiktok.com/${a}/video/${c}`, true);
}

function dailymotion(u: URL, host: string, segs: readonly string[]): SiteVideo | undefined {
  const video = (id: string | undefined, kind: SiteVideoKind) =>
    id !== undefined && DAILYMOTION_ID.test(id)
      ? curated('dailymotion', id, kind, `https://www.dailymotion.com/video/${id}`, kind === 'watch')
      : undefined;
  const [a = '', b = '', c = ''] = segs;
  if (host === 'dai.ly') return segs.length === 1 ? video(a, 'watch') : undefined;
  if (host === 'geo.dailymotion.com') {
    const player = (segs.length === 1 && a === 'player.html') || (segs.length === 2 && a === 'player' && DAILYMOTION_PLAYER.test(b));
    return player ? video(single(u, 'video'), 'embed') : undefined;
  }
  if (segs.length === 2 && a === 'video') return video(b, 'watch');
  if (segs.length === 3 && a === 'embed' && b === 'video') return video(c, 'embed');
  return undefined;
}

function reddit(host: string, segs: readonly string[]): SiteVideo | undefined {
  if (host === 'v.redd.it') {
    const [id = ''] = segs;
    return segs.length === 1 && REDDIT_VIDEO.test(id) ? curated('reddit', id, 'watch', `https://v.redd.it/${id}`, false, `reddit:v:${id}`) : undefined;
  }
  const [a = '', sub = '', c = '', id = ''] = segs;
  if (a !== 'r' || !REDDIT_SUB.test(sub)) return undefined;
  const embed = host === 'embed.reddit.com';
  if (segs.length === 4 && c === 's' && !embed) return shareLink('reddit', id, `https://www.reddit.com/r/${sub}/s/${id}`);
  // /r/<sub>/comments/<id>[/<slug>...]: the trailing segments (title slug, comment) are ignored.
  // No look-ahead: anonymous yt-dlp runs answer that the site asks to sign in (§3.8, 2026-11-18).
  // The embedded post (embed.reddit.com, 2026-11-18) may hold a video, or not.
  if (segs.length < 4 || c !== 'comments' || !REDDIT_ID.test(id)) return undefined;
  return curated('reddit', id, embed ? 'post' : 'watch', `https://www.reddit.com/r/${sub}/comments/${id}/`, false);
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
    const v = single(u, 'video') ?? '';
    return segs.length === 0 && v.startsWith('v') ? vod(v.slice(1), 'embed') : undefined;
  }
  if (!TWITCH_HOSTS.has(host)) return undefined;
  const [a = '', b = '', c = ''] = segs;
  if (segs.length === 2 && a === 'videos') return vod(b, 'watch');
  if (segs.length === 2 && a === 'clip' && host === 'm.twitch.tv') return clip(b);
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

// Sites added on 2026-11-18: they resolve on a click only (no look-ahead).

function bluesky(host: string, segs: readonly string[]): SiteVideo | undefined {
  const [a = '', handle = '', c = '', id = ''] = segs;
  // The embedded post (embed.bsky.app/embed/<handle>/app.bsky.feed.post/<id>, 2026-11-18).
  const shape = host === 'embed.bsky.app' ? a === 'embed' && c === 'app.bsky.feed.post' : a === 'profile' && c === 'post';
  if (segs.length !== 4 || !shape || !BLUESKY_HANDLE.test(handle) || !BLUESKY_POST.test(id)) return undefined;
  return curated('bluesky', id, 'post', `https://bsky.app/profile/${handle}/post/${id}`, false, `bluesky:${handle}/${id}`);
}

function pinterest(u: URL, host: string, segs: readonly string[]): SiteVideo | undefined {
  const [a = '', b = ''] = segs;
  if (host === 'pin.it') return segs.length === 1 ? shareLink('pinterest', a, `https://pin.it/${a}`, SHORT_SHARE_CODE) : undefined;
  if (host === 'assets.pinterest.com') {
    // The embedded pin (/ext/embed.html?id=<id>, 2026-11-18).
    const id = segs.length === 2 && a === 'ext' && b === 'embed.html' ? single(u, 'id') : undefined;
    return id !== undefined && PINTEREST_ID.test(id) ? curated('pinterest', id, 'post', `https://www.pinterest.com/pin/${id}/`, false) : undefined;
  }
  if (segs.length !== 2 || a !== 'pin' || !PINTEREST_ID.test(b)) return undefined;
  return curated('pinterest', b, 'post', `https://www.pinterest.com/pin/${b}/`, false);
}

function linkedin(segs: readonly string[]): SiteVideo | undefined {
  const [a = '', b = '', c = ''] = segs;
  if (segs.length === 2 && a === 'posts' && LINKEDIN_SLUG.test(b)) return curated('linkedin', b, 'post', `https://www.linkedin.com/posts/${b}`, false);
  const activity = segs.length === 3 && a === 'feed' && b === 'update' ? LINKEDIN_ACTIVITY.exec(c)?.[1] : undefined;
  return activity
    ? curated('linkedin', activity, 'post', `https://www.linkedin.com/feed/update/urn:li:activity:${activity}/`, false, `linkedin:activity:${activity}`)
    : undefined;
}

function kick(u: URL, segs: readonly string[]): SiteVideo | undefined {
  const [channel = '', b = '', c = ''] = segs;
  if (!NAME.test(channel)) return undefined;
  const clip = (id: string | undefined) =>
    id !== undefined && KICK_CLIP.test(id) ? curated('kick', id, 'clip', `https://kick.com/${channel}?clip=${id}`, false, `kick:clip:${id}`) : undefined;
  if (segs.length === 1) return clip(single(u, 'clip'));
  if (segs.length === 3 && b === 'clips') return clip(c);
  // A VOD: yt-dlp answers NOT_FOUND for every one today, while the browser plays its HLS (§3.8).
  if (segs.length === 3 && b === 'videos' && KICK_VIDEO.test(c)) {
    return curated('kick', c, 'watch', `https://kick.com/${channel}/videos/${c}`, false, undefined, 'network');
  }
  return undefined;
}

function bilibili(u: URL, host: string, segs: readonly string[]): SiteVideo | undefined {
  const [a = '', b = ''] = segs;
  if (host === 'b23.tv') return segs.length === 1 ? shareLink('bilibili', a, `https://b23.tv/${a}`, SHORT_SHARE_CODE) : undefined;
  if (segs.length !== 2 || a !== 'video' || !BILIBILI_ID.test(b)) return undefined;
  // `p` names a part of a multi-part video; the first part is the video's own address.
  const p = single(u, 'p');
  const part = p !== undefined && BILIBILI_PART.test(p) && p !== '1' ? p : undefined;
  return part
    ? curated('bilibili', b, 'watch', `https://www.bilibili.com/video/${b}/?p=${part}`, false, `bilibili:${b}:p${part}`)
    : curated('bilibili', b, 'watch', `https://www.bilibili.com/video/${b}/`, false);
}

function vk(segs: readonly string[]): SiteVideo | undefined {
  const id = segs.length === 1 ? VK_VIDEO.exec(segs[0] ?? '')?.[1] : undefined;
  return id ? curated('vk', id, 'watch', `https://vk.com/video${id}`, false) : undefined;
}

function odysee(segs: readonly string[]): SiteVideo | undefined {
  const [channel = '', name = ''] = segs;
  if (segs.length !== 2 || !ODYSEE_CHANNEL.test(channel) || !ODYSEE_NAME.test(name)) return undefined;
  const path = `${channel}/${name}`;
  return curated('odysee', path, 'watch', `https://odysee.com/${path}`, false);
}

function bitchute(segs: readonly string[]): SiteVideo | undefined {
  const [a = '', b = ''] = segs;
  if (segs.length !== 2 || (a !== 'video' && a !== 'embed') || !BITCHUTE_ID.test(b)) return undefined;
  return curated('bitchute', b, a === 'video' ? 'watch' : 'embed', `https://www.bitchute.com/video/${b}/`, false);
}

function ninegag(segs: readonly string[]): SiteVideo | undefined {
  const [a = '', b = ''] = segs;
  return segs.length === 2 && a === 'gag' && NINEGAG_ID.test(b) ? curated('ninegag', b, 'post', `https://9gag.com/gag/${b}`, false) : undefined;
}

function imgur(segs: readonly string[]): SiteVideo | undefined {
  const [a = '', b = ''] = segs;
  if (segs.length === 1 && IMGUR_ID.test(a) && IMGUR_NOT_A_WORD.test(a)) return curated('imgur', a, 'post', `https://imgur.com/${a}`, false);
  if (segs.length === 2 && a === 'gallery' && IMGUR_GALLERY.test(b)) {
    return curated('imgur', b, 'post', `https://imgur.com/gallery/${b}`, false, `imgur:gallery:${b}`);
  }
  return undefined;
}

function telegram(segs: readonly string[]): SiteVideo | undefined {
  const [channel = '', n = ''] = segs;
  if (segs.length !== 2 || !TELEGRAM_CHANNEL.test(channel) || !TELEGRAM_POST.test(n)) return undefined;
  return curated('telegram', `${channel}/${n}`, 'post', `https://t.me/${channel}/${n}`, false);
}

function tumblr(host: string, segs: readonly string[]): SiteVideo | undefined {
  const post = (blog: string, id: string) =>
    TUMBLR_BLOG.test(blog) && !TUMBLR_PAGES.has(blog) && TUMBLR_POST.test(id)
      ? curated('tumblr', `${blog}/${id}`, 'post', `https://${blog}.tumblr.com/post/${id}`, false)
      : undefined;
  const [a = '', b = ''] = segs;
  if (segs.length < 2 || segs.length > 3) return undefined;
  if (host === 'www.tumblr.com') return post(a, b);
  const blog = host.slice(0, -'.tumblr.com'.length);
  return a === 'post' ? post(blog, b) : undefined;
}

/** Rumble answers yt-dlp with HTTP 403 (bot protection) for every address: the browser's traffic only (§3.8). */
function rumble(segs: readonly string[]): SiteVideo | undefined {
  const [a = '', b = ''] = segs;
  if (segs.length === 2 && a === 'embed') {
    return RUMBLE_ID.test(b) ? curated('rumble', b, 'embed', `https://rumble.com/embed/${b}/`, false, undefined, 'network') : undefined;
  }
  const id = segs.length === 1 ? RUMBLE_PAGE.exec(a)?.[1] : undefined;
  return id ? curated('rumble', id, 'watch', `https://rumble.com/${a}`, false, undefined, 'network') : undefined;
}

function threads(segs: readonly string[]): SiteVideo | undefined {
  const [user = '', b = '', code = ''] = segs;
  if (segs.length !== 3 || b !== 'post' || !THREADS_USER.test(user) || !THREADS_CODE.test(code)) return undefined;
  return curated('threads', code, 'post', `https://www.threads.com/${user}/post/${code}`, false, undefined, 'network');
}

function snapchat(segs: readonly string[]): SiteVideo | undefined {
  const [a = '', id = ''] = segs;
  if (segs.length !== 2 || a !== 'spotlight' || !SNAPCHAT_ID.test(id)) return undefined;
  return curated('snapchat', id, 'watch', `https://www.snapchat.com/spotlight/${id}`, false, undefined, 'network');
}

/** A SoundCloud track (SITE-MEDIA-SPEC §3.13): `/<user>/<track>[/s-<secret>]`, or a share link. */
function soundcloud(host: string, segs: readonly string[]): SiteVideo | undefined {
  if (host === 'on.soundcloud.com') {
    return segs.length === 1 ? shareLink('soundcloud', segs[0] ?? '', `https://on.soundcloud.com/${segs[0]}`, SOUNDCLOUD_LINK) : undefined;
  }
  const [user = '', track = '', secret] = segs;
  if (segs.length < 2 || segs.length > 3 || !SOUNDCLOUD_USER.test(user) || SOUNDCLOUD_PAGES.has(user)) return undefined;
  if (!SOUNDCLOUD_TRACK.test(track) || SOUNDCLOUD_TABS.has(track)) return undefined;
  if (secret !== undefined && !SOUNDCLOUD_SECRET.test(secret)) return undefined;
  const path = secret === undefined ? `${user}/${track}` : `${user}/${track}/${secret}`;
  return curated('soundcloud', `${user}/${track}`, 'audio', `https://soundcloud.com/${path}`, false);
}

/** A Bandcamp track (`<artist>.bandcamp.com/track/<slug>`); an album is several tracks. */
function bandcamp(host: string, segs: readonly string[]): SiteVideo | undefined {
  const artist = host.slice(0, -'.bandcamp.com'.length);
  const [a = '', slug = ''] = segs;
  if (segs.length !== 2 || a !== 'track' || !BANDCAMP_ARTIST.test(artist) || artist === 'www' || !BANDCAMP_SLUG.test(slug)) return undefined;
  return curated('bandcamp', `${artist}/${slug}`, 'audio', `https://${artist}.bandcamp.com/track/${slug}`, false);
}

/**
 * Whether a path segment's percent-encoded octets decode to UTF-8 text without a separator or a
 * control character ("/", "?", "#", "%", "\", C0, DEL, C1): a title's letters, not another path.
 */
function textOctets(seg: string): boolean {
  if (!seg.includes('%')) return true;
  let s: string;
  try {
    s = decodeURIComponent(seg);
  } catch {
    return false;
  }
  for (const ch of s) {
    const c = ch.codePointAt(0) ?? 0;
    if (c < 0x20 || (c >= 0x7f && c <= 0x9f) || '/?#%\\'.includes(ch)) return false;
  }
  return true;
}

/** A Mixcloud mix (`/<user>/<mix>/`). */
function mixcloud(segs: readonly string[]): SiteVideo | undefined {
  const [user = '', mix = ''] = segs;
  if (segs.length !== 2 || !MIXCLOUD_USER.test(user) || MIXCLOUD_PAGES.has(user.toLowerCase())) return undefined;
  if (!MIXCLOUD_MIX.test(mix) || !textOctets(mix) || MIXCLOUD_TABS.has(mix.toLowerCase())) return undefined;
  return curated('mixcloud', `${user}/${mix}`, 'audio', `https://www.mixcloud.com/${user}/${mix}/`, false);
}

/**
 * The site whose row of VIDEO-DETECT-SPEC §3.8 lists `host` (lower-cased), whatever the path: the one
 * owner of the table's host column. The decorative exemption (§2.5) and the feed rule (§5.1) read it
 * too. The store build names no site for YouTube's hosts. Undefined for any other host.
 */
export function siteOfHost(host: string): SiteName | undefined {
  if (YOUTUBE_HOSTS.has(host) || NOCOOKIE_HOSTS.has(host) || host === 'youtu.be') {
    return import.meta.env.MODE === 'store' ? undefined : 'youtube';
  }
  if (VIMEO_HOSTS.has(host) || host === 'player.vimeo.com') return 'vimeo';
  if (X_HOSTS.has(host) || host === 'platform.twitter.com') return 'x';
  if (FACEBOOK_HOSTS.has(host) || host === 'fb.watch') return 'facebook';
  if (INSTAGRAM_HOSTS.has(host)) return 'instagram';
  if (TIKTOK_HOSTS.has(host) || TIKTOK_SHORT_HOSTS.has(host)) return 'tiktok';
  if (DAILYMOTION_HOSTS.has(host) || host === 'dai.ly' || host === 'geo.dailymotion.com') return 'dailymotion';
  if (REDDIT_HOSTS.has(host) || host === 'v.redd.it' || host === 'embed.reddit.com') return 'reddit';
  if (TWITCH_HOSTS.has(host) || host === 'clips.twitch.tv' || host === 'player.twitch.tv') return 'twitch';
  if (STREAMABLE_HOSTS.has(host)) return 'streamable';
  if (host === 'bsky.app' || host === 'embed.bsky.app') return 'bluesky';
  if (PINTEREST_HOSTS.has(host) || host === 'pin.it' || host === 'assets.pinterest.com') return 'pinterest';
  if (LINKEDIN_HOSTS.has(host)) return 'linkedin';
  if (KICK_HOSTS.has(host)) return 'kick';
  if (BILIBILI_HOSTS.has(host) || host === 'b23.tv') return 'bilibili';
  if (VK_HOSTS.has(host)) return 'vk';
  if (host === 'odysee.com') return 'odysee';
  if (BITCHUTE_HOSTS.has(host)) return 'bitchute';
  if (host === '9gag.com') return 'ninegag';
  if (IMGUR_HOSTS.has(host)) return 'imgur';
  if (host === 't.me') return 'telegram';
  if (host.endsWith('.tumblr.com') && host.split('.').length === 3) return 'tumblr';
  if (RUMBLE_HOSTS.has(host)) return 'rumble';
  if (THREADS_HOSTS.has(host)) return 'threads';
  if (SNAPCHAT_HOSTS.has(host)) return 'snapchat';
  if (SOUNDCLOUD_HOSTS.has(host) || host === 'on.soundcloud.com') return 'soundcloud';
  if (host.endsWith('.bandcamp.com') && host.split('.').length === 3) return 'bandcamp';
  if (MIXCLOUD_HOSTS.has(host)) return 'mixcloud';
  return undefined;
}

/** The site of an address's host (siteOfHost); undefined off http(s) or for an address that does not parse. */
export function siteOfAddress(raw: string | undefined): SiteName | undefined {
  if (!raw) return undefined;
  try {
    const u = new URL(raw);
    return u.protocol === 'https:' || u.protocol === 'http:' ? siteOfHost(u.hostname.toLowerCase()) : undefined;
  } catch {
    return undefined;
  }
}

/** Sites none of whose addresses yt-dlp reads today (§3.8 "Resolver"; Kick's clips it does read). */
const NETWORK_SITES: ReadonlySet<SiteName> = new Set<SiteName>(['threads', 'snapchat', 'rumble']);

/** One-segment pages of a site that list videos (feeds, searches, explore pages), lower-cased. */
const FEED_FIRST: Partial<Record<SiteName, ReadonlySet<string>>> = {
  youtube: new Set(['feed', 'results', 'channel', 'c', 'user', 'hashtag', 'gaming']),
  vimeo: new Set(['watch', 'search']),
  x: new Set(['home', 'explore', 'search', 'notifications']),
  facebook: new Set(['watch', 'search']),
  instagram: new Set(['explore']),
  tiktok: new Set(['foryou', 'following', 'friends', 'explore', 'search', 'tag', 'music', 'live']),
  reddit: new Set(['best', 'hot', 'new', 'top', 'rising', 'search']),
  twitch: new Set(['directory', 'search']),
  bluesky: new Set(['search', 'hashtag', 'feeds', 'notifications']),
  pinterest: new Set(['search', 'ideas', 'today']),
  linkedin: new Set(['feed', 'search']),
};

/**
 * Each site's private messages (first path segment, lower-cased): never a feed, so no video there is
 * looked for a post or offered a Site source (VIDEO-DETECT-SPEC §5.1 "DMs", 2026-11-18).
 */
const MESSAGE_FIRST: Partial<Record<SiteName, ReadonlySet<string>>> = {
  x: new Set(['messages']),
  facebook: new Set(['messages']),
  instagram: new Set(['direct']),
  tiktok: new Set(['messages']),
  reddit: new Set(['chat', 'message']),
  linkedin: new Set(['messaging']),
  bluesky: new Set(['messages']),
};

/**
 * Whether a curated site's path (`segs`: its segments, lower-cased) is its private messages: a first
 * segment of MESSAGE_FIRST, or X's `/i/chat` (§3.8 "Never matched", §5.1 "DMs").
 */
function messageShape(site: SiteName, segs: readonly string[]): boolean {
  const [a = '', b = ''] = segs;
  if (MESSAGE_FIRST[site]?.has(a)) return true;
  return site === 'x' && a === 'i' && b === 'chat';
}

/**
 * Whether an address is a curated site's private messages (messageShape): never a surface, not even
 * for the generic target ("Look for more qualities", "Find the video on this page"), which would ask
 * yt-dlp about the conversation's address (VIDEO-DETECT-SPEC §5.1 "DMs").
 */
export function messagesPage(url: string | undefined): boolean {
  const site = siteOfAddress(url);
  if (!url || site === undefined) return false;
  try {
    const segs = new URL(url).pathname.split('/').filter((x) => x !== '');
    return messageShape(site, segs.map((x) => x.toLowerCase()));
  } catch {
    return false;
  }
}

/** Reddit's listing orders (`/r/<name>/<order>`). */
const REDDIT_ORDERS: ReadonlySet<string> = new Set(['hot', 'new', 'top', 'rising', 'controversial', 'best']);

/**
 * Whether a curated site's address that is no video (`segs`: its path segments) is a page that
 * lists videos (VIDEO-DETECT-SPEC §5.1 "Feed videos"): the site's home page, its feeds and searches,
 * and its profiles, as these sites show them in the address bar. Any other address on the host (an
 * episode, a clip, an album, a story, a post the table does not parse) is not taken for one.
 */
function feedShape(site: SiteName, segs: readonly string[]): boolean {
  if (segs.length === 0) return true;
  const lower = segs.map((x) => x.toLowerCase());
  const [a = '', b = '', c = ''] = lower;
  const n = segs.length;
  if (messageShape(site, lower)) return false;
  if (FEED_FIRST[site]?.has(a)) return true;
  switch (site) {
    case 'youtube':
      return a.startsWith('@');
    case 'tiktok':
      return n === 1 && a.startsWith('@');
    case 'instagram':
      // /reels/ (the reels feed), a profile /<name>/ and its tabs.
      return (n === 1 && NAME.test(a)) || (n === 2 && (b === 'reels' || b === 'tagged'));
    case 'x':
      return (n === 1 && NAME.test(a)) || (n === 2 && a === 'i' && b === 'bookmarks') || (n === 2 && ['media', 'likes', 'with_replies', 'highlights'].includes(b));
    case 'facebook':
      // /reel/ (the reels feed), a page or profile /<name> (not a /<name>.php post address) and
      // its videos and reels tabs.
      return (n === 1 && NAME.test(a) && !a.endsWith('.php')) || (n === 2 && (b === 'videos' || b === 'reels'));
    case 'reddit':
      if (a === 'r') return n === 2 || (n === 3 && REDDIT_ORDERS.has(c));
      if (a === 'user' || a === 'u') return n === 2 || (n === 3 && ['submitted', 'posts', 'overview'].includes(c));
      return false;
    case 'pinterest':
      // A profile /<name>/ and its boards /<name>/<board>/.
      return n <= 2 && a !== 'pin';
    case 'linkedin':
      return n === 2 && (a === 'in' || a === 'company');
    case 'bluesky':
      return n === 2 && a === 'profile';
    default:
      return false;
  }
}

/**
 * What a frame at `url` shows when it is a curated site's page that is no video: `feed` on a page
 * that lists videos (a feed, a profile, a search or a home page; feedShape, VIDEO-DETECT-SPEC §5.1
 * "Feed videos"), `network` on any page of a site yt-dlp cannot read at all. Undefined on any other
 * host, on a video address and on a curated host's other pages (they keep the generic target).
 */
export function curatedPage(url: string | undefined): 'feed' | 'network' | undefined {
  const site = siteOfAddress(url);
  if (!url || site === undefined || parseSiteVideo(url) !== undefined) return undefined;
  if (NETWORK_SITES.has(site)) return 'network';
  let segs: string[];
  try {
    segs = new URL(url).pathname.split('/').filter((x) => x !== '');
  } catch {
    return undefined;
  }
  return feedShape(site, segs) ? 'feed' : undefined;
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
  switch (siteOfHost(host)) {
    case 'youtube':
      // The store build knows no YouTube video: no row, look-ahead or menu hand-off follows. The
      // mode is spelled out (storeBuild's test) so that the minifier drops youtube() from that build.
      return import.meta.env.MODE === 'store' ? undefined : youtube(u, host, segs);
    case 'vimeo':
      return vimeo(u, host, segs);
    case 'x':
      return x(u, host, segs);
    case 'facebook':
      return facebook(u, host, segs);
    case 'instagram':
      return instagram(segs);
    case 'tiktok':
      return tiktok(host, segs);
    case 'dailymotion':
      return dailymotion(u, host, segs);
    case 'reddit':
      return reddit(host, segs);
    case 'twitch':
      return twitch(u, host, segs);
    case 'streamable':
      return streamable(segs);
    case 'bluesky':
      return bluesky(host, segs);
    case 'pinterest':
      return pinterest(u, host, segs);
    case 'linkedin':
      return linkedin(segs);
    case 'kick':
      return kick(u, segs);
    case 'bilibili':
      return bilibili(u, host, segs);
    case 'vk':
      return vk(segs);
    case 'odysee':
      return odysee(segs);
    case 'bitchute':
      return bitchute(segs);
    case 'ninegag':
      return ninegag(segs);
    case 'imgur':
      return imgur(segs);
    case 'telegram':
      return telegram(segs);
    case 'tumblr':
      return tumblr(host, segs);
    case 'rumble':
      return rumble(segs);
    case 'threads':
      return threads(segs);
    case 'snapchat':
      return snapchat(segs);
    case 'soundcloud':
      return soundcloud(host, segs);
    case 'bandcamp':
      return bandcamp(host, segs);
    case 'mixcloud':
      return mixcloud(segs);
    case undefined:
      return undefined;
  }
}

/**
 * The social sites whose frames get the compact button (VIDEO-DETECT-SPEC §3.8 "Compact sites",
 * §5.1 "Social sites"): their videos are vertical players, feed cards and tiles crowded with the
 * site's own controls. The video platforms keep the usual button.
 */
const COMPACT_SITES: ReadonlySet<SiteName> = new Set<SiteName>([
  'tiktok',
  'instagram',
  'facebook',
  'x',
  'reddit',
  'pinterest',
  'threads',
  'snapchat',
  'linkedin',
  'bluesky',
  'tumblr',
  'ninegag',
  'imgur',
  'vk',
  'telegram',
]);

/** Whether a frame at `url` is on a compact site's host (any path: feeds, posts, embeds). */
export function compactSite(url: string | undefined): boolean {
  const site = siteOfAddress(url);
  return site !== undefined && COMPACT_SITES.has(site);
}

/**
 * The video a grid tile's link names (§3.8 "Tiles"): a compact site's address whose shape always
 * names a video, so a picture tile linking to it is a video's (TikTok `/@<user>/video/<id>`,
 * Instagram reels and tv, X `/<user>/status/<id>/video/<n>`, Facebook's video and reel addresses,
 * VK videos). Undefined for anything else: an Instagram `/p/` post may be photos, an X status
 * without `/video/<n>` may be text.
 */
export function videoTile(raw: string | undefined): SiteVideo | undefined {
  const v = parseSiteVideo(raw);
  if (!v || v.resolver === 'network' || !COMPACT_SITES.has(v.site)) return undefined;
  let u: URL;
  try {
    u = new URL(raw ?? '');
  } catch {
    return undefined;
  }
  const segs = u.pathname.split('/').filter((s) => s !== '');
  switch (v.site) {
    case 'tiktok':
      return v.kind === 'watch' ? v : undefined;
    case 'instagram':
      return v.kind === 'watch' && segs.some((s) => s === 'reel' || s === 'reels' || s === 'tv') ? v : undefined;
    case 'x':
      return v.kind === 'watch' && segs.length === 5 && segs[3] === 'video' ? v : undefined;
    case 'facebook':
      return v.kind === 'watch' && u.hostname.toLowerCase() !== 'fb.watch' ? v : undefined;
    case 'vk':
      return v;
    default:
      return undefined;
  }
}

/**
 * The post a grid tile's link (`href`, resolved) names in a frame at `frameUrl` (§5.1 "Grid tiles"),
 * without its fragment: a videoTile address on the frame's own origin. Undefined when the frame's own
 * address is a video (the page is that video: the tiles under it, "You may like" and the creator's
 * other videos, get no button, as the feed under a post viewer) or the site's private messages (§5.1
 * "DMs"), and for any other link.
 */
export function tilePost(href: string, frameUrl: string): string | undefined {
  if (!sameOrigin(href, frameUrl) || videoTile(href) === undefined) return undefined;
  if (parseSiteVideo(frameUrl) !== undefined || messagesPage(frameUrl)) return undefined;
  const i = href.indexOf('#');
  return i < 0 ? href : href.slice(0, i);
}

/**
 * Whether Downex may be asked to read the video of an address with yt-dlp: false for a `network`
 * site's video (§3.8), true for anything else (a curated video, YouTube's, or any other page).
 */
export function ytDlpReads(url: string | undefined): boolean {
  return parseSiteVideo(url)?.resolver !== 'network';
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

// ---------------------------------------------------------------------------------------
// The user's sign-in (SITE-MEDIA-SPEC §6.3, amendment 2026-11-18)

/**
 * Each site's sign-in domains: registrable domains (never a public suffix, which downexd checks
 * again), one of which must hold the canonical page's host. A share link on another domain
 * (`fb.watch`, `pin.it`, `b23.tv`, `v.redd.it`) gets no sign-in. Kept in step with
 * internal/validate's TestCuratedSignInDomains.
 */
export const SIGN_IN_DOMAINS: Readonly<Record<SiteName, readonly string[]>> = {
  youtube: ['youtube.com'],
  vimeo: ['vimeo.com'],
  x: ['x.com', 'twitter.com'],
  facebook: ['facebook.com'],
  instagram: ['instagram.com'],
  tiktok: ['tiktok.com'],
  dailymotion: ['dailymotion.com'],
  reddit: ['reddit.com'],
  twitch: ['twitch.tv'],
  streamable: ['streamable.com'],
  bluesky: ['bsky.app'],
  pinterest: ['pinterest.com'],
  linkedin: ['linkedin.com'],
  kick: ['kick.com'],
  bilibili: ['bilibili.com'],
  vk: ['vk.com', 'vkvideo.ru'],
  odysee: ['odysee.com'],
  bitchute: ['bitchute.com'],
  ninegag: ['9gag.com'],
  imgur: ['imgur.com'],
  telegram: ['t.me'],
  tumblr: ['tumblr.com'],
  rumble: ['rumble.com'],
  threads: ['threads.com', 'threads.net'],
  snapchat: ['snapchat.com'],
  soundcloud: ['soundcloud.com'],
  bandcamp: ['bandcamp.com'],
  mixcloud: ['mixcloud.com'],
};

/**
 * Whose cookies a page's sign-in may carry (§6.3): `domain` and its subdomains for a curated
 * site, or exactly `domain` (the page's host) for any other page (`exact`).
 */
export interface SignInScope {
  domain: string;
  exact: boolean;
}

/**
 * The sign-in scope of a probe or add of `pageUrl` under `key`: a curated (or YouTube) key takes
 * its site's sign-in domain that holds the page's host, none when no domain does; a generic
 * `page:` key on a curated site's own host (an Instagram story's "Look for more qualities") takes
 * that site's domain as well, any other generic page its exact host. Undefined off http(s) and
 * for an unknown key.
 */
export function signInScope(key: string, pageUrl: string): SignInScope | undefined {
  let host: string;
  try {
    const u = new URL(pageUrl);
    if (u.protocol !== 'https:' && u.protocol !== 'http:') return undefined;
    host = u.hostname.toLowerCase();
  } catch {
    return undefined;
  }
  if (!host) return undefined;
  const generic = key.startsWith('page:');
  const site = generic ? siteOfHost(host) : siteOfKey(key);
  const domain = site && SIGN_IN_DOMAINS[site].find((d) => host === d || host.endsWith(`.${d}`));
  if (domain) return { domain, exact: false };
  return generic ? { domain: host, exact: true } : undefined;
}

/**
 * Whether a cookie the browser holds (its `domain`: a host, or a domain with a leading dot) is
 * within a sign-in scope: the scope's domain or a subdomain of it, or exactly the host for an
 * `exact` scope (a cookie set for a parent domain is left out there).
 */
export function inSignInScope(cookieDomain: string, scope: SignInScope): boolean {
  const d = cookieDomain.toLowerCase().replace(/^\./, '');
  return scope.exact ? d === scope.domain : d === scope.domain || d.endsWith(`.${scope.domain}`);
}
