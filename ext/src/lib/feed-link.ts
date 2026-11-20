// The clicked feed video's own address (VIDEO-DETECT-SPEC §5.1 "Feed videos"): on a curated site's
// feed, profile or home page the frame's address is not a video, so the panel looks for the link of
// the post the clicked `<video>` belongs to. Only at click time, only link addresses (`href`), never
// page text, scripts or player data (CLEAN-ROOM.md). The background checks the address again
// (curated, on the frame's own origin) before it uses it. Pure apart from reading the DOM.

import { messagesPage, parseSiteVideo, sameOrigin } from './site-video';

/**
 * Ancestors of the clicked video the walk looks at. A public Facebook page's video sat 11 levels
 * below its post link in a check of 2026-11-18; the walk stops earlier at any ancestor holding
 * another video, so a deep bound cannot reach a neighbouring post.
 */
export const FEED_MAX_ANCESTORS = 16;
/** Links an ancestor may hold; one with more is a page section, not a post, and nothing is taken. */
export const FEED_MAX_LINKS = 64;

/** The URL without its fragment. */
function withoutFragment(url: string): string {
  const i = url.indexOf('#');
  return i < 0 ? url : url.slice(0, i);
}

/** The parent element, or the host of the shadow root the element sits in. */
function up(el: Element): Element | null {
  if (el.parentElement) return el.parentElement;
  const root = el.parentNode;
  return root !== null && root.nodeType === 11 ? ((root as Partial<ShadowRoot>).host ?? null) : null;
}

/** A link's resolved address (`href` of an `<a>`), or undefined. */
function linkOf(a: Element): string | undefined {
  const href = (a as HTMLAnchorElement).href;
  return typeof href === 'string' && href !== '' ? href : undefined;
}

/** Most px a carousel slide's top and height may differ from the clicked video's (one row). */
const ROW_SLACK_PX = 2;

/**
 * What the walk may ask the tracker about another `<video>` (VIDEO-DETECT-SPEC §5.1 "Carousels"):
 * whether it is shown (its IntersectionObserver ratio at least 0.5), undefined when not known (not
 * tracked, or no ratio yet).
 */
export interface FeedWalkDeps {
  shown?: (video: Element) => boolean | undefined;
}

/**
 * Whether `other` is a hidden slide of the clicked `video`'s carousel: known not to be shown, with
 * a size, its top and height those of the clicked video within ROW_SLACK_PX (one horizontal row).
 */
function carouselSlide(other: Element, video: Element, deps: FeedWalkDeps): boolean {
  if (deps.shown?.(other) !== false) return false;
  const a = video.getBoundingClientRect();
  const b = other.getBoundingClientRect();
  if (!(a.width > 0 && a.height > 0 && b.width > 0 && b.height > 0)) return false;
  return Math.abs(a.top - b.top) <= ROW_SLACK_PX && Math.abs(a.height - b.height) <= ROW_SLACK_PX;
}

/**
 * Whether `el` holds a `<video>` other than `video` (in its own tree, not in shadow roots) that is
 * not a hidden slide of the same carousel (`slides`: those already judged so).
 */
function holdsOtherVideo(el: Element, video: Element, deps: FeedWalkDeps, slides: Set<Element>): boolean {
  const videos = el.getElementsByTagName('video');
  for (let i = 0; i < videos.length; i++) {
    const v = videos[i];
    if (!v || v === video || slides.has(v)) continue;
    if (!carouselSlide(v, video, deps)) return true;
    slides.add(v);
  }
  return false;
}

/**
 * The address of the post the clicked `video` belongs to, for a frame at `frameUrl` whose own address
 * is not a curated video; undefined when the frame's address is one (the page is the video) or a
 * site's private messages (no post is looked for there, §5.1 "DMs"), when no
 * link near the video names a curated video on the frame's own origin, or when the nearest links
 * name several videos (the walk reached a list of posts). Up to FEED_MAX_ANCESTORS ancestors; the
 * lowest ancestor with a recognised link decides. The walk stops, taking nothing, at an ancestor
 * that holds another `<video>` (its links may be another post's) or more than FEED_MAX_LINKS links
 * (a page section, judged from all of its links or not at all). A hidden slide of the clicked
 * video's own carousel (`deps.shown`, carouselSlide) does not stop it (§5.1 "Carousels"); layout is
 * read for that only, at click time.
 */
export function feedPostLink(video: Element, frameUrl: string, deps: FeedWalkDeps = {}): string | undefined {
  if (parseSiteVideo(frameUrl) !== undefined || messagesPage(frameUrl)) return undefined;
  const recognised = (href: string | undefined): { key: string; url: string } | undefined => {
    if (!href || !sameOrigin(href, frameUrl)) return undefined;
    const v = parseSiteVideo(href);
    return v && v.resolver !== 'network' ? { key: v.key, url: withoutFragment(href) } : undefined;
  };
  const slides = new Set<Element>();
  let el = up(video);
  for (let depth = 0; el && depth < FEED_MAX_ANCESTORS; depth++, el = up(el)) {
    if (holdsOtherVideo(el, video, deps, slides)) return undefined;
    if (el.localName === 'a') {
      const own = recognised(linkOf(el));
      if (own) return own.url;
    }
    const found = new Map<string, string>();
    const links = el.querySelectorAll('a[href]');
    if (links.length > FEED_MAX_LINKS) return undefined;
    for (let i = 0; i < links.length; i++) {
      const link = links[i];
      const r = link ? recognised(linkOf(link)) : undefined;
      if (r && !found.has(r.key)) found.set(r.key, r.url);
    }
    if (found.size === 1) return found.values().next().value;
    if (found.size > 1) return undefined;
  }
  return undefined;
}
