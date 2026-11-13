// The video tracker of every http(s) frame (VIDEO-DETECT-SPEC §2.5, §5.1, §5.5): it follows the
// frame's `<video>` elements and, once one is worth a Download button, asks the background for
// the overlay UI (`/video-ui.js`, injected into this frame only on demand), so pages without a
// video cost nothing but this small script. It reads only the public state of video elements,
// never page text, scripts or storage. Isolated world; it draws nothing itself. YouTube's hosts
// are left to youtube.content.ts, so there is never a second button there. In the top frame of a
// curated video address it also runs the look-ahead dwell.

import { browser } from 'wxt/browser';
import { defineContentScript } from 'wxt/utils/define-content-script';
import { isFrameMessage } from '../lib/messages';
import { parseVideoOptions, VIDEO_OPTIONS_KEY } from '../lib/options-form';
import { PrefetchDwell, prefetchVideo } from '../lib/video-prefetch';
import { VideoTracker, VISIBLE_RATIO } from '../lib/video-tracker';

/** The hosts of youtube.content.ts (its `matches`), which has its own button. */
const YOUTUBE_EXCLUDES = ['*://www.youtube.com/*', '*://m.youtube.com/*', '*://music.youtube.com/*', '*://www.youtube-nocookie.com/*'];

/** An element's open or closed shadow root: Firefox's own method, Chromium's `dom` API. */
function openOrClosedShadowRoot(el: Element): ShadowRoot | null {
  const own = (el as Element & { openOrClosedShadowRoot?: () => ShadowRoot | null }).openOrClosedShadowRoot;
  if (typeof own === 'function') return own.call(el);
  const dom = (browser as unknown as { dom?: { openOrClosedShadowRoot?: (e: Element) => ShadowRoot | null } }).dom;
  if (typeof dom?.openOrClosedShadowRoot === 'function') return dom.openOrClosedShadowRoot(el);
  return el.shadowRoot;
}

export default defineContentScript({
  matches: ['http://*/*', 'https://*/*'],
  excludeMatches: YOUTUBE_EXCLUDES,
  allFrames: true,
  matchAboutBlank: true,
  runAt: 'document_idle',
  cssInjectionMode: 'manual',
  main(ctx) {
    const topFrame = window.top === window;
    const tracker = new VideoTracker({
      doc: document,
      send: (msg) => browser.runtime.sendMessage(msg),
      timers: {
        setTimeout: (cb, ms) => ctx.setTimeout(cb, ms),
        clearTimeout: (h) => clearTimeout(h as number),
        now: () => Date.now(),
      },
      intersection: (cb) =>
        typeof IntersectionObserver === 'function' ? new IntersectionObserver((entries) => cb(entries), { threshold: [0, VISIBLE_RATIO] }) : undefined,
      resize: (cb) => (typeof ResizeObserver === 'function' ? new ResizeObserver((entries) => cb(entries)) : undefined),
      shadowRoot: openOrClosedShadowRoot,
      topFrame,
      location: () => location.href,
      lookAheadVideo: (url) => prefetchVideo(url),
      readOptions: async () => parseVideoOptions((await browser.storage.local.get(VIDEO_OPTIONS_KEY))[VIDEO_OPTIONS_KEY]),
      dwell: (deps) => new PrefetchDwell(deps),
      visible: () => document.visibilityState === 'visible',
    });
    tracker.publish();
    ctx.onInvalidated(() => tracker.destroy());

    // The background asks for a deep scan when it saw media for this frame but no video.
    const onMessage = (msg: unknown) => {
      if (isFrameMessage(msg)) tracker.onFrameMessage(msg);
      return undefined;
    };
    browser.runtime.onMessage.addListener(onMessage);
    ctx.onInvalidated(() => browser.runtime.onMessage.removeListener(onMessage));

    // "Show a Download button over videos", the hidden sites, the look-ahead options.
    const onStorage = (changes: Record<string, { newValue?: unknown }>, area: string) => {
      const change = changes[VIDEO_OPTIONS_KEY];
      if (area === 'local' && change) tracker.optionsChanged(parseVideoOptions(change.newValue));
    };
    browser.storage.onChanged.addListener(onStorage);
    ctx.onInvalidated(() => browser.storage.onChanged.removeListener(onStorage));

    // Back from the back/forward cache: the background forgot the tab, so every frame reports again.
    ctx.addEventListener(window, 'pageshow', (e) => {
      if ((e as PageTransitionEvent).persisted) tracker.restored();
      else if (topFrame) tracker.navigated();
    });
    if (topFrame) {
      // A single-page app's navigation and back/forward: the dwell starts over.
      ctx.addEventListener(window, 'wxt:locationchange', () => tracker.navigated());
      ctx.addEventListener(window, 'popstate', () => tracker.navigated());
      ctx.addEventListener(document, 'visibilitychange', () => tracker.visibilityChanged());
    }
    tracker.start();
  },
});
