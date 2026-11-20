// The "Download" button over YouTube players (SITE-MEDIA-SPEC §5.1). It follows the page's
// own navigation and the player, asks the background for the video's MP4 qualities and hands
// the pick to Downex. In the top frame it also tells the background when the user has stayed
// 1.5 s on a watch or Shorts video in a visible tab, so the qualities are ready before the click
// (BROWSER-SPEC §4). Only the frame's address is used: never the page's scripts, player data
// or traffic. Isolated world; the UI lives in a closed shadow root with inlined CSS, so no
// web-accessible resource reveals the extension. The button follows "Show a Download button
// over videos" and the hidden sites through `downex:overlay-state`, which decides by the tab's
// host (VIDEO-DETECT-SPEC §5.4). The panel's Part row (save only part of the video) shows when
// `downex:video-features` says the connected Downex takes parts; its timeline reads, seeks,
// plays and pauses the player's own <video> element (nothing else of the player).

import { browser } from 'wxt/browser';
import { createShadowRootUi } from 'wxt/utils/content-script-ui/shadow-root';
import { defineContentScript } from 'wxt/utils/define-content-script';
import { clipFields } from '../lib/clip';
import {
  isFrameMessage,
  type OverlayStateMessage,
  type OverlayStateResponse,
  type PageVideoAddMessage,
  type PageVideoPrefetchMessage,
  type PageVideoProbeMessage,
  type ShowPanelResponse,
  type VideoAddResponse,
  type VideoFeaturesMessage,
  type VideoFeaturesResponse,
  type VideoProbeResponse,
} from '../lib/messages';
import { DEFAULT_VIDEO_OPTIONS, parseVideoOptions, VIDEO_OPTIONS_KEY, type VideoOptions } from '../lib/options-form';
import { errorMessage, failure } from '../lib/protocol';
import { parseSiteVideo } from '../lib/site-video';
import {
  findPlayer,
  OVERLAY_CSS,
  playerVideo,
  REVEAL_ACTIVITY_MS,
  REVEAL_LOAD_MS,
  VideoOverlay,
  youtubeButtonEnabled,
} from '../lib/video-overlay';
import { lookAheadAllowed, PrefetchDwell } from '../lib/video-prefetch';

/** Page changes in the DOM (a player created again) are looked at no more often than this. */
const MUTATION_SYNC_MS = 500;

export default defineContentScript({
  matches: [
    'https://www.youtube.com/*',
    'https://m.youtube.com/*',
    'https://music.youtube.com/*',
    'https://www.youtube-nocookie.com/embed/*',
  ],
  excludeMatches: ['*://www.youtube.com/live_chat*'],
  allFrames: true,
  runAt: 'document_idle',
  cssInjectionMode: 'manual',
  async main(ctx) {
    let options: VideoOptions = { ...DEFAULT_VIDEO_OPTIONS };
    const embedded = window.top !== window || location.pathname.startsWith('/embed/');
    /** The background's `downex:overlay-state` (the tab's host decides); undefined until it answers. */
    let overlayState: OverlayStateResponse | undefined;
    const buttonShown = () => youtubeButtonEnabled(options, overlayState, window.top === window ? location.hostname : undefined);

    const send = async <R>(msg: PageVideoProbeMessage | PageVideoAddMessage): Promise<R> => {
      try {
        const answer = (await browser.runtime.sendMessage(msg)) as R | undefined;
        if (answer !== undefined) return answer;
        return failure('disconnected', 'Downex’s extension did not answer. Reload the page.') as R;
      } catch (e) {
        // The extension was reloaded or updated: this script is orphaned.
        return failure('disconnected', `Downex’s extension is not available: ${errorMessage(e)}`) as R;
      }
    };

    /** The player the overlay is mounted in (findPlayer), or null. */
    let anchor: HTMLElement | null = null;
    const overlay = new VideoOverlay(document, {
      probe: () => send<VideoProbeResponse>({ type: 'downex:page-video-probe', url: location.href }),
      add: (pick, clip) => {
        const msg: PageVideoAddMessage = { type: 'downex:page-video-add', url: location.href, videoTrack: pick.video, audioTrack: pick.audio };
        // A part of the video (MEDIA-SPEC §8): no end = to the end.
        if (clip) Object.assign(msg, clipFields(clip));
        return send<VideoAddResponse>(msg);
      },
      clips: async () => {
        const msg: VideoFeaturesMessage = { type: 'downex:video-features' };
        try {
          const answer = (await browser.runtime.sendMessage(msg)) as VideoFeaturesResponse | undefined;
          return answer?.clips === true;
        } catch {
          return false; // extension reloaded: this script is orphaned
        }
      },
      // The Part row's timeline: the video of the player the panel belongs to (its own <video>).
      video: () => playerVideo(anchor),
    });

    // The look-ahead: only the top frame's own page counts, while the tab is shown.
    const topFrame = window.top === window;
    const dwell = new PrefetchDwell({
      read: () => ({
        url: topFrame ? location.href : undefined,
        visible: document.visibilityState === 'visible',
        enabled: lookAheadAllowed(options) && buttonShown(),
      }),
      fire: (url) => {
        const msg: PageVideoPrefetchMessage = { type: 'downex:page-video-prefetch', url };
        try {
          browser.runtime.sendMessage(msg).catch(() => undefined);
        } catch {
          // extension reloaded: this orphaned script can no longer reach the background
        }
      },
      timers: { setTimeout: (cb, ms) => ctx.setTimeout(cb, ms), clearTimeout: (h) => clearTimeout(h as number) },
    });
    ctx.onInvalidated(() => dwell.stop());

    const ui = await createShadowRootUi(ctx, {
      name: 'downex-video',
      position: 'inline',
      mode: 'closed',
      css: OVERLAY_CSS,
      anchor: () => anchor,
      append: 'last',
      onMount: (container, shadow) => overlay.attach(container, shadow),
    });
    ctx.onInvalidated(() => overlay.destroy());

    // "Show Downex download panel" on this frame's player (VIDEO-DETECT-SPEC §5.1 "Menu"): its
    // own panel opens when its button is shown; otherwise the background says what to use.
    const onMessage = (msg: unknown, _sender: unknown, sendResponse: (r: ShowPanelResponse) => void) => {
      if (!isFrameMessage(msg) || msg.type !== 'downex:show-panel') return undefined;
      sync();
      if (anchor && ui.shadowHost.isConnected) overlay.open();
      sendResponse({ shown: overlay.isOpen });
      return undefined;
    };
    browser.runtime.onMessage.addListener(onMessage);
    ctx.onInvalidated(() => browser.runtime?.onMessage?.removeListener(onMessage)); // gone once orphaned

    /** Re-reads the address and the player: mounts, moves or removes the overlay. */
    const sync = () => {
      if (ctx.isInvalid) return;
      dwell.sync();
      const video = parseSiteVideo(location.href);
      const shown = buttonShown();
      overlay.setEnabled(shown);
      overlay.setVideo(video?.key, embedded);
      const next = video && shown ? findPlayer(document) : null;
      if (next === anchor && (next === null || ui.shadowHost.isConnected)) return;
      anchor = next;
      if (next) ui.mount();
      else ui.remove();
    };

    let pending = false;
    const syncSoon = () => {
      if (pending) return;
      pending = true;
      ctx.setTimeout(() => {
        pending = false;
        sync();
      }, MUTATION_SYNC_MS);
    };

    /** Asks whether buttons may show in this tab; without an answer the global option decides. */
    const askOverlayState = async () => {
      const msg: OverlayStateMessage = { type: 'downex:overlay-state' };
      let answer: OverlayStateResponse | undefined;
      try {
        answer = (await browser.runtime.sendMessage(msg)) as OverlayStateResponse | undefined;
      } catch {
        answer = undefined; // extension reloaded: this script is orphaned
      }
      if (ctx.isInvalid) return;
      overlayState = typeof answer?.button === 'boolean' ? answer : { button: options.videoButton, host: '' };
      sync();
    };

    try {
      options = parseVideoOptions((await browser.storage.local.get(VIDEO_OPTIONS_KEY))[VIDEO_OPTIONS_KEY]);
    } catch {
      // defaults
    }
    const onStorage = (changes: Record<string, { newValue?: unknown }>, area: string) => {
      const change = changes[VIDEO_OPTIONS_KEY];
      if (area !== 'local' || !change) return;
      options = parseVideoOptions(change.newValue);
      sync();
      // The button or the hidden sites may have changed: the background decides again.
      void askOverlayState();
    };
    browser.storage.onChanged.addListener(onStorage);
    ctx.onInvalidated(() => browser.storage?.onChanged?.removeListener(onStorage)); // gone once orphaned

    // YouTube is a single-page app: its own event, WXT's history watcher, back/forward and the
    // page cache all mean another address; a `loadstart` (it does not bubble) a new video.
    ctx.addEventListener(document, 'yt-navigate-finish' as keyof DocumentEventMap, sync);
    ctx.addEventListener(window, 'wxt:locationchange', sync);
    ctx.addEventListener(window, 'popstate', sync);
    ctx.addEventListener(window, 'pageshow', sync);
    // Hidden, the dwell stops; shown again, it starts over.
    ctx.addEventListener(document, 'visibilitychange', () => dwell.sync());
    ctx.addEventListener(
      document,
      'loadstart',
      () => {
        sync();
        overlay.reveal(REVEAL_LOAD_MS);
      },
      { capture: true },
    );
    ctx.addEventListener(
      document,
      'pointermove',
      (e) => {
        if (anchor && e.target instanceof Node && anchor.contains(e.target)) overlay.reveal(REVEAL_ACTIVITY_MS);
      },
      { capture: true, passive: true },
    );
    // A player created again (theater mode, the miniplayer, a new watch page) is followed.
    const observer = new MutationObserver(syncSoon);
    observer.observe(document.documentElement, { childList: true, subtree: true });
    ctx.onInvalidated(() => observer.disconnect());
    sync();
    void askOverlayState();
  },
});
