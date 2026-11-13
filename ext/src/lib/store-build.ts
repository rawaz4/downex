// The store build (`wxt build --mode store`, `pnpm build:store`): the variant uploaded to the
// Chrome Web Store and Edge Add-ons, whose policies refuse extensions that download from
// YouTube. It leaves out youtube.content.ts (wxt.config.ts), and through storeExcludes the
// extension never recognises, lists, looks ahead at or hands over a YouTube video or a stream
// of YouTube's video host. The default build (the installer's copy and Firefox's add-on) keeps
// everything. Pure.

/**
 * Whether this is the store build. Vite writes the mode into the bundle; tests switch it with
 * `vi.stubEnv('MODE', 'store')`, which is why it is read on every call rather than once.
 */
export function storeBuild(): boolean {
  return import.meta.env.MODE === 'store';
}

/** The hosts of YouTube's pages and embeds, and of its video host (`*.googlevideo.com`). */
function youtubeHost(host: string): boolean {
  const h = host.toLowerCase().replace(/\.$/, '');
  const within = (domain: string) => h === domain || h.endsWith(`.${domain}`);
  return within('youtube.com') || within('youtube-nocookie.com') || h === 'youtu.be' || within('googlevideo.com');
}

/** A YouTube address (page, embed, short link or video host); false for anything that is not a URL. */
export function youtubeAddress(address: string | undefined): boolean {
  if (!address) return false;
  try {
    return youtubeHost(new URL(address).hostname);
  } catch {
    return false;
  }
}

/** The store build leaves this address alone: a YouTube page, embed, short link or stream. */
export function storeExcludes(address: string | undefined): boolean {
  return storeBuild() && youtubeAddress(address);
}

/**
 * A response the store build does not list: from YouTube's video host, or sent by a YouTube
 * frame (Firefox names its document, Chromium its origin as `initiator`). With no row for the
 * page video, these streams would otherwise surface in the popup, the panels and the badge.
 */
export function storeExcludesRequest(d: { url: string; documentUrl?: string; initiator?: string }): boolean {
  return storeExcludes(d.url) || storeExcludes(d.documentUrl) || storeExcludes(d.initiator);
}

/** The context menu's answer for a link the store build does not take. */
export const STORE_LINK_MESSAGE = 'Downex can’t take this link from the browser.';

/**
 * The options page's hints that name YouTube, as the store build words them (by element id in
 * options/index.html); the rest of each text is the default build's.
 */
export const STORE_OPTION_HINTS: Readonly<Record<string, string>> = {
  'video-button-hint': "On the sites you visit. The toolbar menu lists a page's videos either way.",
  'video-prefetch-hint':
    'After you stay 1.5 seconds on a video of Vimeo, X, TikTok, Dailymotion, Reddit, Twitch or Streamable, ' +
    'Downex gets its qualities in the background, so Download lists them at once. It runs only while the ' +
    'Download button is shown on that site, Downex is running and its video tools are set up: one video at ' +
    'a time, at most about one a minute. It never sends your sign-in cookies, so it pauses while sending ' +
    'them is allowed.',
};
