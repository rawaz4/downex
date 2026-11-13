// Background: wires every listener synchronously at the top level of the worker so events
// that wake it are delivered (BROWSER-SPEC §3, §4; page videos: SITE-MEDIA-SPEC §3.6, §5.2;
// videos of every site: VIDEO-DETECT-SPEC §2.4, §2.6, §3.6, §5.3, §5.5).

import { browser } from 'wxt/browser';
import { defineBackground } from 'wxt/utils/define-background';
import {
  DownloadClaims,
  createCreatedFallbackListener,
  createDeterminingFilenameListener,
  candidateFromItem,
  requireDaemon,
  type DaemonGate,
  type DownloadItemLike,
  type DownloadsControl,
  type HandoffResult,
} from '../lib/capture-chromium';
import {
  AttemptMemory,
  classifyResponse,
  createDownloadCreatedListener,
  createHeadersReceivedListener,
  type BlockingResponseLike,
  type HeadersReceivedDetailsLike,
} from '../lib/capture-firefox';
import { CaptureTrace, traceUrl, type TraceStep } from '../lib/capture-trace';
import { RequestContextStore, encodeRequestBody, filterHeaders, findContext, toHeaderPairs } from '../lib/context';
import { createFileHandOff, type FileSource } from '../lib/file-handoff';
import {
  FILE_REQUEST_TYPES,
  FrameMediaSeen,
  FrameReports,
  PendingDocuments,
  RedirectMap,
  RefusedFiles,
  classifyFile,
  isMediaTraffic,
  refusedMp4Address,
} from '../lib/frame-media';
import { buildFrameOffers, resolveOfferKey, segmentOf, tabFileEntries, tabGroupCount, tabStreamEntries } from '../lib/frame-offers';
import { createHandOff, targetFromDownloadItem } from '../lib/handoff';
import { HandoffHistory, type HandoffSource, type StorageAreaLike } from '../lib/history';
import { ClickTracker } from '../lib/clicks';
import {
  MEDIA_REQUEST_TYPES,
  MediaStore,
  detectMedia,
  frameOf,
  isPageVideoStream,
  isStream,
  itemKey,
  tabBadge,
  type MediaItem,
  type MediaStorageArea,
  type RecordInput,
  type ResponseDetailsLike,
} from '../lib/media';
import { MediaSends, createMediaHandOff, type MediaSendState, type MediaTracks } from '../lib/media-handoff';
import {
  isExtensionMessage,
  type AddMediaResponse,
  type FindPageVideoResponse,
  type FrameMessage,
  type FrameOffersResponse,
  type LinksBatchResponse,
  type LinksConfirmResponse,
  type MediaResultResponse,
  type OfferAddResponse,
  type OverlayStateResponse,
  type SiteHiddenResponse,
  type StateResponse,
  type StreamProbeResponse,
  type TabMediaResponse,
  type TabStateResponse,
  type VideoAddResponse,
  type VideoProbeResponse,
  type VideoTaskState,
  type VideoUiResponse,
} from '../lib/messages';
import { VIDEO_UI_SCRIPT, type SiteOffer } from '../lib/offer-types';
import {
  VIDEO_OPTIONS_KEY,
  buttonAllowed,
  parseVideoOptions,
  siteHidden,
  withSiteHidden,
  type VideoOptions,
} from '../lib/options-form';
import {
  CONFIRM_THRESHOLD,
  PendingBatches,
  collectPageLinks,
  fileLinks,
  handOffLinks,
  isHttpUrl,
  uniqueHttpLinks,
} from '../lib/page-links';
import {
  FEATURE_PREFETCH,
  FEATURE_STREAMS,
  FEATURE_VIDEO,
  NATIVE_HOST_NAME,
  NativeClient,
  PROTOCOL_VERSION,
  errorMessage,
  failure,
  type AddedResult,
  type BrowserName,
  type ConnectionStatus,
  type NativePort,
} from '../lib/protocol';
import {
  DEFAULTS_VERSION,
  RULES_STORAGE_KEY,
  defaultRules,
  evaluateCapture,
  parseRules,
  siteHost,
  type CaptureCandidate,
  type CaptureDecision,
  type CaptureRules,
} from '../lib/rules';
import { createStreamProber } from '../lib/stream-probes';
import { PausedTabs } from '../lib/tab-pause';
import { httpAddress, parseSiteVideo } from '../lib/site-video';
import { STORE_LINK_MESSAGE, storeExcludes, storeExcludesRequest } from '../lib/store-build';
import {
  PageVideoCounts,
  createVideoHandOff,
  findEntry,
  frameVideo,
  senderTarget,
  tabTarget,
  tabVideoEntries,
  type FrameLike,
  type TabLike,
  type VideoTarget,
} from '../lib/video-handoff';
import {
  HeldLookAheads,
  lookAheadAllowed,
  lookAheadRequest,
  lookAheadSite,
  stillWatched,
  VideoLookAhead,
  type LookAheadRequest,
} from '../lib/video-prefetch';

const HTTP_URLS = ['http://*/*', 'https://*/*'];
const MENU_DOWNLOAD = 'downex-download';
const MENU_DOWNLOAD_ALL = 'downex-download-all';
const PRIVATE_WINDOW_MESSAGE = 'Downloads from private windows stay in the browser.';
/** Request types whose context is recorded (navigations and download-like fetches). */
const CONTEXT_TYPES = ['main_frame', 'sub_frame', 'other', 'media', 'object'] as const;
const BODY_TYPES: ReadonlySet<string> = new Set(['main_frame', 'sub_frame', 'other']);
const CONTEXT_TYPE_SET: ReadonlySet<string> = new Set(CONTEXT_TYPES);
/** Responses that may be a stream (a player's manifest) or a direct file (VIDEO-DETECT-SPEC §2.4). */
const RESPONSE_TYPES = [...new Set([...MEDIA_REQUEST_TYPES, ...FILE_REQUEST_TYPES])];
const RESPONSE_TYPE_SET: ReadonlySet<string> = new Set(RESPONSE_TYPES);
/** Requests whose headers are recorded: download contexts, manifest loads and played files. */
const HEADER_TYPES = [...new Set([...CONTEXT_TYPES, ...RESPONSE_TYPES])];
/**
 * Headers of a possible manifest or file request wait here from onSendHeaders until its response
 * shows whether it is one (onResponseStarted); segment requests pass through too, so it stays small.
 */
const MEDIA_REQUEST_TTL_MS = 60_000;
const MEDIA_REQUEST_MAX = 512;
const STREAM_GONE_MESSAGE = 'This stream is no longer listed for the tab. Reload the page and start the video again.';
/** An offer key that no longer resolves (VIDEO-DETECT-SPEC §5.6). */
const OFFER_GONE_MESSAGE = 'This video is no longer listed. Play it again, then download it.';
const NO_TAB_MESSAGE = 'Only a tab of this browser window can ask for its videos.';
/** Frames the generic tracker never runs in: youtube.content.ts owns them (VIDEO-DETECT-SPEC §5.1). */
const NO_TRACKER_HOSTS: ReadonlySet<string> = new Set(['www.youtube.com', 'm.youtube.com', 'music.youtube.com', 'www.youtube-nocookie.com']);

/** The host of the document that sent a request (Firefox `documentUrl`, Chromium `initiator`). */
function requesterHost(d: ResponseDetailsLike & { initiator?: string }): string | undefined {
  try {
    return new URL(d.documentUrl ?? d.initiator ?? '').hostname;
  } catch {
    return undefined;
  }
}
/** Least time between daemon status refreshes triggered by skipped captures. */
const DAEMON_REFRESH_MS = 2000;
/**
 * How long a closed tab's pause and opener outlive it. A tab opened only for a download
 * (target=_blank) is closed by the browser as the download starts, possibly before the
 * download is decided; tab IDs are not reused within a browser session.
 */
const CLOSED_TAB_GRACE_MS = 10_000;

/** Firefox-only fields of webRequest details, tabs and menus (absent in Chromium). */
interface BrowsingContextInfo {
  incognito?: boolean;
  cookieStoreId?: string;
}

/** The runtime.MessageSender fields the frame handlers read (documentId: Chromium). */
interface FrameSenderInfo {
  url?: string;
  frameId?: number;
  documentId?: string;
  tab?: TabLike;
}

/** A content script's frame, checked: a real tab that is not private. */
interface SenderFrame {
  tab: TabLike;
  tabId: number;
  frameId: number;
  documentId?: string;
  /** The frame's address (`sender.url`). */
  frameUrl?: string;
}

function senderFrame(sender: FrameSenderInfo): SenderFrame | undefined {
  const tab = sender.tab;
  const tabId = tab?.id;
  if (!tab || tabId === undefined || tabId < 0 || tab.incognito) return undefined;
  const f: SenderFrame = { tab, tabId, frameId: sender.frameId ?? 0 };
  if (typeof sender.documentId === 'string' && sender.documentId !== '') f.documentId = sender.documentId;
  if (sender.url) f.frameUrl = sender.url;
  return f;
}

/** A hand-off's state for a page (a running one, else the kept answer). */
function taskState(s: MediaSendState): VideoTaskState<AddedResult> | undefined {
  if (s.sending) return { running: true };
  return s.result ? { result: s.result } : undefined;
}

/** Firefox's blocking onHeadersReceived accepts a Promise; the shared typings do not say so. */
interface BlockingHeadersEvent {
  addListener(
    callback: (details: HeadersReceivedDetailsLike) => BlockingResponseLike | Promise<BlockingResponseLike>,
    filter: { urls: string[]; types?: string[] },
    extraInfoSpec?: string[],
  ): void;
}

function log(...args: unknown[]): void {
  console.warn('[downex]', ...args);
}

function detectBrowser(): BrowserName {
  if (import.meta.env.FIREFOX) return 'firefox';
  const ua = navigator.userAgent;
  if (/\bEdg\//.test(ua)) return 'edge';
  if (/\bChrom(e|ium)\//.test(ua)) return 'chrome';
  return 'other';
}

export default defineBackground(() => {
  const isFirefox = import.meta.env.FIREFOX;
  const ownId = browser.runtime.id;

  // --- Rules (storage.local), cached for synchronous decisions --------------------------
  let rules: CaptureRules | undefined;
  const rulesReady: Promise<CaptureRules> = browser.storage.local
    .get(RULES_STORAGE_KEY)
    .then((got) => (rules ??= parseRules(got[RULES_STORAGE_KEY])))
    .catch((e: unknown) => {
      log('reading rules failed', e);
      return (rules ??= defaultRules());
    });
  browser.storage.onChanged.addListener((changes, area) => {
    const change = changes[RULES_STORAGE_KEY];
    if (area === 'local' && change) rules = parseRules(change.newValue);
  });
  browser.runtime.onInstalled.addListener(() => {
    void browser.storage.local.get(RULES_STORAGE_KEY).then((got) => {
      const stored = got[RULES_STORAGE_KEY] as { defaultsVersion?: unknown } | undefined;
      if (stored === undefined) {
        return browser.storage.local.set({ [RULES_STORAGE_KEY]: defaultRules() });
      }
      // An update with new default extensions: store them, so the options page lists them.
      if (stored.defaultsVersion !== DEFAULTS_VERSION) {
        return browser.storage.local.set({ [RULES_STORAGE_KEY]: parseRules(stored) });
      }
      return undefined;
    });
    void createMenus().catch((e: unknown) => log('creating context menus failed', e));
  });

  // --- Per-tab state: Alt-click bypass and "Pause capture in this tab" (B2) ---------------
  // storage.session: cleared with the browser session (local only on browsers without it).
  const sessionArea = (browser.storage.session ?? browser.storage.local) as unknown as StorageAreaLike;
  const clicks = new ClickTracker();
  const pausedTabs = new PausedTabs(sessionArea);
  // A tab opened from another one (a link with a new tab as target, a middle- or
  // Ctrl-click, window.open) sends the requests of downloads started there: it inherits
  // the opener's pause, and its downloads look at the opener's Alt-click.
  browser.tabs.onCreated.addListener((tab) => {
    const { id, openerTabId } = tab;
    if (id === undefined || id < 0 || openerTabId === undefined || openerTabId < 0) return;
    clicks.setOpener(id, openerTabId);
    pausedTabs.inherit(openerTabId, id).catch((e: unknown) => log('storing the paused tabs failed', e));
  });
  browser.tabs.onRemoved.addListener((tabId) => {
    media.clearTab(tabId).catch(logMediaError);
    mediaSends.clearTab(tabId);
    fileSends.clearTab(tabId);
    pageVideos.clearTab(tabId);
    videoAdds.clearTab(tabId);
    reports.clearTab(tabId);
    pendingDocuments.clearTab(tabId);
    mediaSeen.clearTab(tabId);
    heldLookAheads.forget(tabId);
    refusedFiles.clearTab(tabId);
    injected.delete(tabId);
    setTimeout(() => {
      clicks.forgetTab(tabId);
      pausedTabs.forget(tabId).catch((e: unknown) => log('forgetting a paused tab failed', e));
    }, CLOSED_TAB_GRACE_MS);
  });
  browser.tabs.onReplaced?.addListener((addedTabId, removedTabId) => {
    clicks.forgetTab(removedTabId);
    pausedTabs.replace(removedTabId, addedTabId).catch((e: unknown) => log('moving a paused tab failed', e));
    media.replace(removedTabId, addedTabId).catch(logMediaError);
    mediaSends.clearTab(removedTabId);
    fileSends.clearTab(removedTabId);
    pageVideos.replace(removedTabId, addedTabId);
    videoAdds.clearTab(removedTabId);
    reports.replace(removedTabId, addedTabId);
    pendingDocuments.clearTab(removedTabId);
    mediaSeen.clearTab(removedTabId);
    const ui = injected.get(removedTabId);
    injected.delete(removedTabId);
    if (ui) injected.set(addedTabId, ui);
    showTabBadge(addedTabId);
  });
  // Another document in a frame (not a same-document history change): that frame's items and
  // element reports, and those of the frames below it, are gone; the top frame's: the tab's.
  // Prerendered pages commit in frames other than 0; Chromium also names their lifecycle.
  // A document that is a file was held until this commit and is recorded after the clearing.
  // Page videos follow the top frame's address and the players committed in other frames.
  browser.webNavigation.onCommitted.addListener((d) => {
    if (d.tabId < 0 || d.documentLifecycle === 'prerender') return;
    const commit = d as typeof d & { documentId?: string; parentFrameId?: number };
    const file = pendingDocuments.take(d.tabId, d.frameId, d.url);
    if (file && commit.documentId) file.documentId = commit.documentId;
    if (d.frameId !== 0) {
      pageVideos.setFrame(d.tabId, d.frameId, d.url);
      const cleared = reports.committed(d.tabId, d.frameId, commit.parentFrameId);
      const ui = injected.get(d.tabId);
      for (const f of cleared) for (const doc of [...(ui ?? [])]) if (doc.startsWith(`${f}\n`)) ui?.delete(doc);
      media.clearFrames(d.tabId, cleared).catch(logMediaError);
      refusedFiles.clearFrames(d.tabId, cleared);
      if (file) recordItem(file);
      showTabBadge(d.tabId);
      return;
    }
    pageVideos.setTop(d.tabId, d.url, true);
    videoAdds.clearTab(d.tabId);
    reports.committed(d.tabId, 0);
    pendingDocuments.clearTab(d.tabId);
    mediaSeen.clearTab(d.tabId);
    heldLookAheads.forget(d.tabId);
    refusedFiles.clearTab(d.tabId);
    injected.delete(d.tabId);
    media.clearTab(d.tabId).catch(logMediaError);
    mediaSends.clearTab(d.tabId);
    fileSends.clearTab(d.tabId);
    if (file) recordItem(file);
    showTabBadge(d.tabId);
  });
  // A single-page site (YouTube) moves to another video without a new document.
  browser.webNavigation.onHistoryStateUpdated.addListener((d) => {
    if (d.frameId !== 0 || d.tabId < 0) return;
    pageVideos.setTop(d.tabId, d.url, false);
    showTabBadge(d.tabId);
  });
  // A new browser session reuses tab IDs (matters only where storage.session is missing).
  browser.runtime.onStartup.addListener(() => {
    pausedTabs.clear().catch((e: unknown) => log('clearing paused tabs failed', e));
    media.clearAll().catch(logMediaError);
    mediaSends.clearAll();
    fileSends.clearAll();
    pageVideos.clearAll();
    videoAdds.clearAll();
    reports.clearAll();
    injected.clear();
  });

  const decide = (c: CaptureCandidate): CaptureDecision | Promise<CaptureDecision> => {
    const run = (r: CaptureRules) => {
      const now = Date.now();
      return evaluateCapture(r, c, {
        now,
        lastClick: clicks.bypassClick(c.tabId, now),
        // A request that names no tab: the tab of the latest link click (as for Alt).
        tabPaused: pausedTabs.isPaused(c.tabId ?? clicks.lastClickTab(now)),
      });
    };
    if (rules && pausedTabs.loaded) return run(rules);
    return Promise.all([rulesReady, pausedTabs.ready]).then(([r]) => run(rules ?? r));
  };
  const decideItem = (item: DownloadItemLike): CaptureDecision | Promise<CaptureDecision> => {
    if (item.byExtensionId === ownId) return { capture: false, reason: 'own_download' };
    if (item.state !== undefined && item.state !== 'in_progress') {
      return { capture: false, reason: 'not_in_progress' };
    }
    // Decided before pausing: a POST cannot be safely replayed by the daemon (B4).
    const ctx = findContext(contexts, targetFromDownloadItem(item));
    if (ctx && ctx.method !== 'GET') return { capture: false, reason: 'not_get' };
    // Download items name no tab: the tab that sent the request, when it was recorded.
    const candidate = candidateFromItem(item);
    if (ctx?.tabId !== undefined) candidate.tabId = ctx.tabId;
    return decide(candidate);
  };

  // --- Streams, files and page videos seen per tab and frame (MEDIA-SPEC §6.1,
  // SITE-MEDIA-SPEC §5.2, VIDEO-DETECT-SPEC §2.4, §5.3) ---------------------------------------
  // Badge precedence: the relay error `!` (global) over a tab's count, see tabBadge. The count is
  // the tab's groups (tabGroupCount): page videos (which take in their frame's network items),
  // frames with a stream that was not refused, and files that are neither short nor segments.
  let relayDown = false;
  const pageVideos = new PageVideoCounts();
  // The trackers' element reports (memory only) and the per-frame state that goes with them.
  const reports = new FrameReports();
  const mediaSeen = new FrameMediaSeen();
  // Curated look-aheads that came before the tab's first playable video report (§5.5).
  const heldLookAheads = new HeldLookAheads();
  // .mp4 addresses whose responses were not MP4: never offered as an element's file (§2.6).
  const refusedFiles = new RefusedFiles();
  const redirects = new RedirectMap();
  const pendingDocuments = new PendingDocuments<RecordInput>();
  /** Per tab: the documents (`<frameId>\n<documentId>`) the overlay UI was injected into. */
  const injected = new Map<number, Set<string>>();
  const showTabBadge = (tabId: number) => {
    const count = tabGroupCount({
      items: media.items(tabId),
      pageVideos: pageVideos.frameKeys(tabId),
      reports: reports.tab(tabId),
      refused: (i) => streamProber.refused(i),
    });
    const b = tabBadge(count, relayDown);
    Promise.all([
      // text null: the tab shows the global badge again.
      browser.action.setBadgeText({ tabId, text: b.text as string }),
      browser.action.setBadgeBackgroundColor({ tabId, color: b.color }),
    ]).catch(() => undefined); // the tab is gone
  };
  // Headers (cookies) are written only to storage.session, which browsers hold in memory.
  const media = new MediaStore(sessionArea as unknown as MediaStorageArea, {
    persistHeaders: browser.storage.session !== undefined,
    onChange: (tabId) => showTabBadge(tabId),
  });
  const mediaRequests = new RequestContextStore({
    ttlMs: MEDIA_REQUEST_TTL_MS,
    maxEntries: MEDIA_REQUEST_MAX,
    maxStoredBodyBytes: 0,
  });
  const logMediaError = (e: unknown) => log('storing the detected streams failed', e);
  // The daemon reads the manifests before it answers: a hand-off outlives the popup that sent it.
  // One per stream (item key) at a time: the popup and an overlay panel join the same one.
  const mediaSends = new MediaSends({ keep: (o) => o.ok || o.error.code !== 'expired' });
  // The adds of direct files, per file key (or element key and frame).
  const fileSends = new MediaSends({ keep: (o) => o.ok || o.error.code !== 'expired' });
  /** The hand-off state of an item key or an element's send key. */
  const sendState = (tabId: number, sendKey: string) =>
    taskState((sendKey.startsWith('s') ? mediaSends : fileSends).state(tabId, sendKey));

  /** Records an item and, for a new one, the page it was seen on (looked up once). */
  const recordItem = (input: RecordInput) => {
    media
      .record(input)
      .then((r) => {
        if (!r?.added) return undefined;
        return browser.tabs.get(input.tabId).then(
          (tab) =>
            media.annotate(input.tabId, r.key, {
              ...(tab.url ? { url: tab.url } : {}),
              ...(tab.title ? { title: tab.title } : {}),
            }),
          () => undefined, // the tab closed meanwhile
        );
      })
      .catch(logMediaError);
  };

  /**
   * Media traffic of a frame that reported no video: its tracker looks into its shadow roots
   * (VIDEO-DETECT-SPEC §2.5), at most once per FRAME_MEDIA_SEEN_MS per frame.
   */
  const tellMediaSeen = (d: ResponseDetailsLike) => {
    if (d.tabId < 0 || d.type === 'main_frame' || d.type === 'sub_frame') return;
    if (NO_TRACKER_HOSTS.has(requesterHost(d) ?? '')) return;
    const { frameId } = frameOf(d);
    if ((reports.frame(d.tabId, frameId)?.videos.length ?? 0) > 0 || !mediaSeen.due(d.tabId, frameId)) return;
    const message: FrameMessage = { type: 'downex:frame-media-seen' };
    Promise.resolve(browser.tabs.sendMessage(d.tabId, message, { frameId })).catch(() => undefined); // no tracker there
  };

  // --- Native relay ----------------------------------------------------------------------
  const updateBadge = (s: ConnectionStatus) => {
    if (s.state === 'connecting') return;
    const daemonDown = s.state === 'connected' && s.daemonRunning === false;
    const down = s.state !== 'connected' || daemonDown;
    if (down !== relayDown) {
      relayDown = down;
      for (const tabId of new Set([...media.counts().map(([t]) => t), ...pageVideos.tabs()])) showTabBadge(tabId);
    }
    const reason = daemonDown ? 'downexd is not running' : s.lastError;
    const title = down
      ? `Downex is not available${reason ? `: ${reason}` : ''}`
      : `Downex ${s.daemonVersion ?? ''}`.trim();
    Promise.all([
      browser.action.setBadgeText({ text: down ? '!' : '' }),
      down ? browser.action.setBadgeBackgroundColor({ color: '#d93025' }) : undefined,
      browser.action.setTitle({ title }),
    ]).catch((e: unknown) => log('badge update failed', e));
  };

  const client = new NativeClient({
    connect: () => browser.runtime.connectNative(NATIVE_HOST_NAME) as unknown as NativePort,
    hello: () => ({
      protocol: PROTOCOL_VERSION,
      browser: detectBrowser(),
      extVersion: browser.runtime.getManifest().version,
    }),
    disconnectReason: (port) =>
      browser.runtime.lastError?.message ?? (port as { error?: { message?: string } }).error?.message,
    onStatus: updateBadge,
  });

  // Capture only while downexd is known to run (BROWSER-SPEC §3): otherwise the browser
  // keeps the download at once and the daemon state is refreshed for the next one.
  let lastRefresh = 0;
  const daemonGate: DaemonGate = {
    ready: () => client.daemonReady,
    refresh: () => {
      const now = Date.now();
      if (now - lastRefresh < DAEMON_REFRESH_MS) return;
      lastRefresh = now;
      void client.queryStatus();
    },
  };
  const captureDecide = requireDaemon(decide, daemonGate);
  const captureDecideItem = requireDaemon(decideItem, daemonGate);

  // --- Request context (observe-only webRequest) -----------------------------------------
  // Keyed by cookie store (Firefox containers); private-window requests are never recorded
  // (the manifest also keeps the extension out of private windows).
  const contexts = new RequestContextStore();
  const contextFilter = { urls: HTTP_URLS, types: [...CONTEXT_TYPES] };
  browser.webRequest.onBeforeRequest.addListener(
    (d) => {
      const info = d as BrowsingContextInfo;
      if (!info.incognito && d.method.toUpperCase() === 'POST' && BODY_TYPES.has(d.type)) {
        contexts.recordBody(d.url, encodeRequestBody(d.requestBody), info.cookieStoreId);
      }
      return undefined;
    },
    contextFilter,
    ['requestBody'],
  );
  browser.webRequest.onSendHeaders.addListener(
    (d) => {
      const info = d as BrowsingContextInfo;
      if (info.incognito) return;
      const headers = toHeaderPairs(d.requestHeaders);
      if (CONTEXT_TYPE_SET.has(d.type)) contexts.recordHeaders(d.url, d.method, headers, info.cookieStoreId, d.tabId);
      if (RESPONSE_TYPE_SET.has(d.type) && d.tabId >= 0 && d.method.toUpperCase() === 'GET') {
        mediaRequests.recordHeaders(d.url, d.method, headers, info.cookieStoreId, d.tabId);
      }
    },
    { urls: HTTP_URLS, types: HEADER_TYPES },
    isFirefox ? ['requestHeaders'] : ['requestHeaders', 'extraHeaders'],
  );

  // Observe-only: the first URL of each redirect chain of a file (VIDEO-DETECT-SPEC §2.4).
  const fileFilter = { urls: HTTP_URLS, types: [...FILE_REQUEST_TYPES] };
  browser.webRequest.onBeforeRedirect.addListener((d) => {
    if (d.tabId >= 0 && !(d as BrowsingContextInfo).incognito) redirects.redirected(d.requestId, d.url);
  }, fileFilter);
  browser.webRequest.onCompleted.addListener((d) => redirects.done(d.requestId), fileFilter);
  browser.webRequest.onErrorOccurred.addListener((d) => redirects.done(d.requestId), fileFilter);

  // Observe-only: a manifest or a direct MP4 file is listed for its tab and frame with the headers
  // its request was sent with (a file: those of the first URL of its redirect chain).
  browser.webRequest.onResponseStarted.addListener(
    (d) => {
      const details = d as unknown as ResponseDetailsLike;
      if (storeExcludesRequest(d as { url: string; documentUrl?: string; initiator?: string })) return;
      const stream = detectMedia(details);
      const file = stream ? undefined : classifyFile(details);
      const det = stream ?? file;
      const refused = det ? undefined : refusedMp4Address(details);
      if (refused) refusedFiles.add(details.tabId, frameOf(details).frameId, refused);
      if (det) {
        const input: RecordInput = { ...det, seenAt: Date.now() };
        const first = file ? redirects.first(details.requestId) : undefined;
        if (first && first !== det.url) input.requestUrl = first;
        const sent = mediaRequests.get(first ?? d.url, det.storeId);
        if (sent?.headers) input.headers = filterHeaders(sent.headers);
        // A document that is the file: recorded once its frame committed (that clears the frame).
        if (file && (file.reqType === 'main_frame' || file.reqType === 'sub_frame')) pendingDocuments.add(input);
        else recordItem(input);
      }
      if (det || isMediaTraffic(details)) tellMediaSeen(details);
    },
    { urls: HTTP_URLS, types: RESPONSE_TYPES },
    ['responseHeaders'],
  );

  // --- Hand-off --------------------------------------------------------------------------
  const history = new HandoffHistory(sessionArea);
  const handOff = createHandOff({
    client,
    contexts,
    getCookies: (url, storeId) => browser.cookies.getAll(storeId ? { url, storeId } : { url }),
    userAgent: navigator.userAgent,
    history,
    onError: (e) => log('history write failed', e),
  });

  const mediaHandOff = createMediaHandOff({
    client,
    getCookies: (url, storeId) => browser.cookies.getAll(storeId ? { url, storeId } : { url }),
    userAgent: navigator.userAgent,
    history,
    onError: (e) => log('history write failed', e),
  });
  // Direct files: only the headers the browser sent, never cookies.getAll (VIDEO-DETECT-SPEC §6.3).
  const fileHandOff = createFileHandOff({
    client,
    userAgent: navigator.userAgent,
    history,
    onError: (e) => log('history write failed', e),
  });
  const streamProber = createStreamProber({
    client,
    getCookies: (url, storeId) => browser.cookies.getAll(storeId ? { url, storeId } : { url }),
    userAgent: navigator.userAgent,
  });
  /** A stream's MP4 qualities (a user's click); a refusal takes it out of the badge. */
  const probeStream = async (tabId: number, item: MediaItem) => {
    const outcome = await streamProber.probe(item);
    showTabBadge(tabId);
    return outcome;
  };
  const addStream = (tabId: number, item: MediaItem, tracks: MediaTracks) =>
    mediaSends.run(tabId, itemKey(item), () => mediaHandOff(item, tracks));
  const addFile = (tabId: number, sendKey: string, src: FileSource) => fileSends.run(tabId, sendKey, () => fileHandOff(src));
  /** The hand-off source of a recorded file, played in a frame at `frameUrl`. */
  const fileSource = (item: MediaItem, frameUrl: string | undefined, title: string | undefined): FileSource => {
    const src: FileSource = { url: item.url, size: item.size };
    if (item.requestUrl) src.requestUrl = item.requestUrl;
    if (item.headers) src.headers = item.headers;
    if (item.filename) src.filename = item.filename;
    const frame = item.frameUrl ?? frameUrl;
    if (frame) src.frameUrl = frame;
    const pageTitle = item.pageTitle ?? title;
    if (pageTitle) src.pageTitle = pageTitle;
    return src;
  };
  /** The address of a frame of a tab, as the browser knows it now. */
  const frameAddress = async (tabId: number, frameId: number): Promise<string | undefined> => {
    try {
      return ((await browser.webNavigation.getFrame({ tabId, frameId })) as { url?: string } | null | undefined)?.url;
    } catch {
      return undefined; // the frame or the tab is gone
    }
  };

  // --- Page videos (SITE-MEDIA-SPEC §3.5, §5) -------------------------------------------
  // The options are read from storage.local for every request: the overlay writes the sign-in
  // option there itself and asks again at once.
  const readVideoOptions = async (): Promise<VideoOptions> => {
    try {
      return parseVideoOptions((await browser.storage.local.get(VIDEO_OPTIONS_KEY))[VIDEO_OPTIONS_KEY]);
    } catch {
      return parseVideoOptions(undefined);
    }
  };
  // Look-aheads while the user watches (BROWSER-SPEC §4, VIDEO-DETECT-SPEC §5.5): one at a time in
  // the whole browser, only to a relay that serves them while downexd is known to run, into the
  // same probe cache; not on a hidden site; on sites other than YouTube only while some frame of
  // the tab reported a meaningful video that is neither protected nor live.
  const playsVideo = (req: LookAheadRequest) => lookAheadSite(req.target) === 'youtube' || reports.playable(req.tabId);
  const lookAhead = new VideoLookAhead({
    enabled: async (req) => {
      const options = await readVideoOptions();
      return lookAheadAllowed(options) && buttonAllowed(options, req.host);
    },
    ready: () => client.daemonReady && (client.status.features ?? []).includes(FEATURE_PREFETCH),
    run: (target) => videoHandOff.lookAhead(target),
    stillShown: async (req) => {
      try {
        return stillWatched((await browser.tabs.get(req.tabId)) as TabLike, req.target) && playsVideo(req);
      } catch {
        return false; // the tab closed meanwhile
      }
    },
    onError: (e) => log('look-ahead failed', errorMessage(e)),
  });
  const videoHandOff = createVideoHandOff({
    client,
    getCookies: (url, storeId) => browser.cookies.getAll(storeId ? { url, storeId } : { url }),
    options: readVideoOptions,
    history,
    onError: (e) => log('history write failed', e),
    // A click on Download (overlay, popup, menu) lets look-aheads go out again after a back-off.
    onUserRequest: () => lookAhead.userAsked(),
  });
  // One add per page video of a tab at a time (a popup opened again joins it); answers are kept
  // for a popup opened again, and forgotten with the tab's document.
  const videoAdds = new MediaSends();
  const addVideo = (tabId: number, target: VideoTarget, videoTrack: string, audioTrack: string) =>
    videoAdds.run(tabId, target.key, () => videoHandOff.add(target, videoTrack, audioTrack));
  const addState = (tabId: number, key: string): VideoTaskState<AddedResult> | undefined => taskState(videoAdds.state(tabId, key));
  /** A tab's frames, as the browser knows them now. */
  const tabFrames = async (tabId: number | undefined): Promise<FrameLike[]> => {
    if (tabId === undefined || tabId < 0) return [];
    try {
      return ((await browser.webNavigation.getAllFrames({ tabId })) ?? []) as FrameLike[];
    } catch {
      return []; // the tab closed meanwhile, or has no frames to list
    }
  };
  /** A tab and its frames, as the browser knows them now. */
  const tabAndFrames = async (tabId: number): Promise<{ tab: TabLike; frames: FrameLike[] }> => {
    const tab = (await browser.tabs.get(tabId)) as TabLike;
    return { tab, frames: await tabFrames(tabId) };
  };
  const lookUp = async (tabId: number, key: string) => {
    try {
      const { tab, frames } = await tabAndFrames(tabId);
      return tabTarget(tab, frames, key);
    } catch (e) {
      return failure('expired', `The tab is gone: ${errorMessage(e)}`);
    }
  };
  // Tabs open before the worker started count their page videos from their address.
  browser.tabs
    .query({})
    .then((tabs) => {
      for (const t of tabs ?? []) {
        if (t.id === undefined || t.id < 0 || !t.url || t.incognito) continue;
        pageVideos.setTop(t.id, t.url, true);
        if (pageVideos.count(t.id) > 0) showTabBadge(t.id);
      }
    })
    .catch(() => undefined);

  // --- Frames of any site (VIDEO-DETECT-SPEC §2.6, §3.6, §5.1, §5.4) -----------------------

  /** Injects the overlay UI into a tracker's frame, once per document; false when refused or failed. */
  const injectVideoUi = async (f: SenderFrame): Promise<boolean> => {
    const doc = `${f.frameId}\n${f.documentId ?? ''}`;
    if (injected.get(f.tabId)?.has(doc)) return true;
    if (!buttonAllowed(await readVideoOptions(), siteHost(f.tab.url) ?? '')) return false;
    let docs = injected.get(f.tabId);
    if (docs?.has(doc)) return true;
    if (!docs) {
      docs = new Set();
      injected.set(f.tabId, docs);
    }
    docs.add(doc);
    const files: [typeof VIDEO_UI_SCRIPT] = [VIDEO_UI_SCRIPT];
    try {
      // Chromium injects into the asking document only (documentIds may not go with frameIds).
      if (f.documentId && !isFirefox) await browser.scripting.executeScript({ target: { tabId: f.tabId, documentIds: [f.documentId] }, files });
      else await browser.scripting.executeScript({ target: { tabId: f.tabId, frameIds: [f.frameId] }, files });
      return true;
    } catch (e) {
      injected.get(f.tabId)?.delete(doc);
      log('injecting the video overlay failed', errorMessage(e));
      return false;
    }
  };

  /** What a frame's panel lists (VIDEO-DETECT-SPEC §2.6), from memory: never a relay call. */
  const frameOffers = async (f: SenderFrame, src: string, mse: boolean): Promise<FrameOffersResponse> => {
    const items = await media.list(f.tabId);
    const probeState = await videoHandOff.probeState();
    const video = frameVideo(f.tab, f.frameUrl, f.frameId, await tabFrames(f.tabId));
    let site: SiteOffer | undefined;
    if (video) {
      const probe = probeState(video.target);
      const found = probe && 'result' in probe && probe.result.ok ? probe.result.value.site : '';
      site = { key: video.target.key, site: (!video.curated && found) || video.site, kind: video.kind, pageUrl: video.target.pageUrl, curated: video.curated };
      if (probe) site.probe = probe;
      const add = addState(f.tabId, video.target.key);
      if (add) site.add = add;
    }
    const features = client.status.features ?? [];
    const youtube = parseSiteVideo(f.tab.url)?.site === 'youtube';
    const value = buildFrameOffers({
      sender: f,
      src,
      mse,
      title: f.tab.title ?? '',
      topHost: siteHost(f.tab.url) ?? '',
      items,
      reports: reports.tab(f.tabId),
      ...(site ? { site } : {}),
      features: { streams: features.includes(FEATURE_STREAMS), video: features.includes(FEATURE_VIDEO) },
      streamProbe: (i) => streamProber.state(i),
      refused: (i) => streamProber.refused(i),
      ...(youtube ? { hideStream: (i: MediaItem) => isPageVideoStream(i.url) } : {}),
      addState: (k) => sendState(f.tabId, k),
      rejected: (u) => refusedFiles.has(f.tabId, f.frameId, u),
    });
    return { ok: true, value };
  };

  /** Adds or removes the exact host in the hidden sites (writes serialised), and says what holds then. */
  let optionsWrite: Promise<unknown> = Promise.resolve();
  const setSiteHidden = (host: string | undefined, hidden: boolean): Promise<SiteHiddenResponse> => {
    const run = optionsWrite.then(async (): Promise<SiteHiddenResponse> => {
      if (!host) return { hidden: false };
      const raw = (await browser.storage.local.get(VIDEO_OPTIONS_KEY))[VIDEO_OPTIONS_KEY];
      const options = parseVideoOptions(raw);
      const next = withSiteHidden(options, host, hidden);
      if (next.hiddenSites.join('\n') !== options.hiddenSites.join('\n')) {
        const stored = typeof raw === 'object' && raw !== null && !Array.isArray(raw) ? (raw as Record<string, unknown>) : {};
        await browser.storage.local.set({ [VIDEO_OPTIONS_KEY]: { ...stored, ...next } });
      }
      return { hidden: siteHidden(next, host).hidden };
    });
    optionsWrite = run.catch(() => undefined);
    return run.catch(async (e: unknown) => {
      log('storing the hidden sites failed', errorMessage(e));
      return { hidden: host ? siteHidden(await readVideoOptions(), host).hidden : false };
    });
  };

  const batches = new PendingBatches(sessionArea);

  /** Records a menu action that could not be handed off, so the popup shows why. */
  const recordFailure = (url: string, source: HandoffSource, code: string, message: string) => {
    history
      .add({ ts: Date.now(), url, source, ok: false, error: { code, message } })
      .catch((e: unknown) => log('history write failed', e));
  };

  const sendLinks = (links: readonly string[], referrer: string | undefined, storeId: string | undefined) =>
    handOffLinks(links, (url) =>
      handOff(
        { url, ...(referrer ? { referrer } : {}), ...(storeId ? { storeId } : {}) },
        { source: 'page-links' },
      ),
    );

  const downloads: DownloadsControl = {
    pause: (id) => browser.downloads.pause(id),
    resume: (id) => browser.downloads.resume(id),
    cancel: (id) => browser.downloads.cancel(id),
    erase: (query) => browser.downloads.erase(query),
  };
  const handOffItem = (item: DownloadItemLike, timeoutMs: number) =>
    handOff(targetFromDownloadItem(item), { source: 'capture', timeoutMs });
  const onCaptureError = (e: unknown) => log('capture failed', errorMessage(e));

  if (isFirefox) {
    const attempts = new AttemptMemory();
    (browser.webRequest.onHeadersReceived as unknown as BlockingHeadersEvent).addListener(
      createHeadersReceivedListener({
        decide: captureDecide,
        handOff: (target, timeoutMs) => handOff(target, { source: 'capture', timeoutMs }),
        attempts,
        onError: onCaptureError,
      }),
      { urls: HTTP_URLS, types: ['main_frame', 'sub_frame'] },
      ['blocking', 'responseHeaders'],
    );
    browser.downloads.onCreated.addListener(
      createDownloadCreatedListener({
        downloads,
        decide: captureDecideItem,
        handOff: handOffItem,
        attempts,
        ownExtensionId: ownId,
        onError: onCaptureError,
      }),
    );
  } else {
    // Capture log (popup): every step is also written to the worker's console.
    const trace = new CaptureTrace(sessionArea);
    const traceStep = (step: TraceStep, url: string, detail: string, downloadId?: number) => {
      console.info('[downex] capture', step, downloadId ?? '', traceUrl(url), detail);
      trace
        .add({ ts: Date.now(), step, url, detail, ...(downloadId !== undefined ? { downloadId } : {}) })
        .catch((e: unknown) => log('capture log write failed', e));
    };
    const itemUrl = (item: DownloadItemLike) => item.finalUrl || item.url;
    const onDecision = (item: DownloadItemLike, d: CaptureDecision) =>
      traceStep('decision', itemUrl(item), d.capture ? `capture (${d.extension})` : `left to the browser: ${d.reason}`, item.id);
    const onCaptureResult = (item: DownloadItemLike, r: HandoffResult) => {
      traceStep('result', itemUrl(item), r.handedOff ? 'handed off to Downex' : `kept by the browser: ${r.code}: ${r.message}`, item.id);
      // A download that could not be paused never reached handOff, which records the others.
      if (!r.handedOff && r.code === 'not_paused') recordFailure(itemUrl(item), 'capture', r.code, r.message);
    };

    // A download-like response, as the network saw it: tells a download the browser never
    // reported to the downloads API apart from one the rules skipped.
    browser.webRequest.onHeadersReceived.addListener(
      (d) => {
        const c = classifyResponse(d as unknown as HeadersReceivedDetailsLike);
        if (c) {
          const size = c.candidate.size !== undefined ? `, ${c.candidate.size} bytes` : '';
          traceStep('network', d.url, `${d.statusCode} ${c.candidate.mime ?? 'no type'}${size}`);
        }
        return undefined;
      },
      { urls: HTTP_URLS, types: ['main_frame', 'sub_frame'] },
      ['responseHeaders'],
    );

    const claims = new DownloadClaims();
    const deps = {
      downloads,
      decide: captureDecideItem,
      handOff: handOffItem,
      claims,
      onDecision,
      onResult: onCaptureResult,
      onError: onCaptureError,
    };
    browser.downloads.onCreated.addListener((item) => {
      const size = item.totalBytes > 0 ? `${item.totalBytes} bytes` : 'size unknown';
      traceStep('created', itemUrl(item), `${item.state}, ${item.mime || 'no type'}, ${size}`, item.id);
    });
    browser.downloads.onCreated.addListener(createCreatedFallbackListener(deps));
    const determining = createDeterminingFilenameListener(deps);
    browser.downloads.onDeterminingFilename.addListener((item, suggest) => {
      traceStep('determining', itemUrl(item), item.filename ? `file name ${item.filename}` : 'no file name yet', item.id);
      return determining(item, suggest);
    });
  }

  // --- Messages from content scripts and extension pages ---------------------------------
  const extensionOrigin = browser.runtime.getURL('/');
  browser.runtime.onMessage.addListener((msg: unknown, sender, sendResponse) => {
    if (sender.id !== ownId || !isExtensionMessage(msg)) return undefined;
    // Content scripts report the web page's URL; extension pages (popup, options, the
    // confirmation page, also when shown in a tab) report the extension's own origin.
    const fromExtensionPage = (sender.url ?? '').startsWith(extensionOrigin);
    switch (msg.type) {
      case 'downex:click': {
        // Only content scripts report clicks; each tab and frame keeps its own last click.
        const tabId = sender.tab?.id;
        if (fromExtensionPage || tabId === undefined || tabId < 0) return undefined;
        const now = Date.now();
        clicks.record(
          tabId,
          sender.frameId ?? 0,
          { alt: msg.alt, ctrl: msg.ctrl, shift: msg.shift, meta: msg.meta, ts: Math.min(msg.ts, now) },
          now,
        );
        return undefined;
      }
      case 'downex:tab-state':
        if (!fromExtensionPage) return undefined;
        void pausedTabs.ready.then(() =>
          sendResponse({ paused: pausedTabs.isPaused(msg.tabId) } satisfies TabStateResponse),
        );
        return true;
      case 'downex:set-tab-paused': {
        if (!fromExtensionPage) return undefined;
        const { tabId, paused } = msg;
        void pausedTabs
          .set(tabId, paused)
          .catch((e: unknown) => log('storing the paused tabs failed', e))
          .then(() => sendResponse({ paused: pausedTabs.isPaused(tabId) } satisfies TabStateResponse));
        return true;
      }
      case 'downex:get-state':
        if (!fromExtensionPage) return undefined;
        history
          .list()
          .catch(() => [])
          .then((h) => sendResponse({ connection: client.status, history: h } satisfies StateResponse));
        return true;
      case 'downex:query-status':
        if (!fromExtensionPage) return undefined;
        void client.queryStatus().then(sendResponse);
        return true;
      case 'downex:tab-media': {
        if (!fromExtensionPage) return undefined;
        const { tabId } = msg;
        void (async (): Promise<TabMediaResponse> => {
          const items = await media.list(tabId).catch((): MediaItem[] => []);
          const probeState = await videoHandOff.probeState();
          let host = '';
          let title = '';
          const videos = await tabAndFrames(tabId).then(
            ({ tab, frames }) => {
              host = siteHost(tab.url) ?? '';
              title = tab.title ?? '';
              return tabVideoEntries(tab, frames, probeState, (key) => addState(tabId, key));
            },
            () => [],
          );
          // With a page video, googlevideo manifests (live or SABR streams Downex would refuse) are not listed.
          const hide = videos.some((v) => v.kind !== 'page');
          const streams = items.filter((i) => isStream(i) && !(hide && isPageVideoStream(i.url)));
          const options = await readVideoOptions();
          const tabReports = reports.tab(tabId);
          return {
            items: tabStreamEntries(streams, tabReports, (i) => mediaSends.state(tabId, itemKey(i)), (i) => streamProber.state(i)),
            files: tabFileEntries(items, tabReports, title, (k) => sendState(tabId, k)),
            videos,
            hiddenHere: host !== '' && siteHidden(options, host).hidden,
            host,
          };
        })().then(sendResponse);
        return true;
      }
      case 'downex:tab-stream-probe': {
        if (!fromExtensionPage) return undefined;
        const { tabId, url } = msg;
        void media
          .get(tabId, url)
          .then((item): Promise<StreamProbeResponse> | StreamProbeResponse =>
            item ? probeStream(tabId, item) : failure('expired', STREAM_GONE_MESSAGE),
          )
          .then(sendResponse);
        return true;
      }
      case 'downex:add-file': {
        if (!fromExtensionPage) return undefined;
        const { tabId, url } = msg;
        void (async (): Promise<OfferAddResponse> => {
          const item = await media.get(tabId, url, 'file');
          if (!item || segmentOf(item, reports.frame(tabId, item.frameId))) return failure('expired', OFFER_GONE_MESSAGE);
          const frameUrl = item.frameUrl ?? (await frameAddress(tabId, item.frameId));
          return addFile(tabId, itemKey(item), fileSource(item, frameUrl, undefined));
        })().then(sendResponse);
        return true;
      }
      case 'downex:frame-videos': {
        // A tracker's element report: memory only, not answered.
        const f = fromExtensionPage ? undefined : senderFrame(sender);
        if (!f) return undefined;
        if (reports.set(f.tabId, f.frameId, f.documentId, msg.videos, Date.now())) showTabBadge(f.tabId);
        if (reports.playable(f.tabId)) {
          const held = heldLookAheads.take(f.tabId, Date.now());
          if (held) lookAhead.request(held);
        }
        return undefined;
      }
      case 'downex:video-ui': {
        if (fromExtensionPage) return undefined;
        const f = senderFrame(sender);
        if (!f) {
          sendResponse({ ok: false } satisfies VideoUiResponse);
          return undefined;
        }
        void injectVideoUi(f).then((ok) => sendResponse({ ok } satisfies VideoUiResponse));
        return true;
      }
      case 'downex:overlay-state': {
        // The tab's host decides, so a site's embedded players follow the site.
        if (fromExtensionPage) return undefined;
        const f = senderFrame(sender);
        const host = siteHost(f?.tab.url) ?? '';
        void readVideoOptions().then((o) =>
          sendResponse({ button: f !== undefined && buttonAllowed(o, host), host } satisfies OverlayStateResponse),
        );
        return true;
      }
      case 'downex:frame-offers': {
        if (fromExtensionPage) return undefined;
        const f = senderFrame(sender);
        if (!f) {
          sendResponse(failure('unsupported_page', NO_TAB_MESSAGE) satisfies FrameOffersResponse);
          return undefined;
        }
        void frameOffers(f, msg.src, msg.mse).then(sendResponse);
        return true;
      }
      case 'downex:stream-probe': {
        if (fromExtensionPage) return undefined;
        const f = senderFrame(sender);
        if (!f) {
          sendResponse(failure('unsupported_page', NO_TAB_MESSAGE) satisfies StreamProbeResponse);
          return undefined;
        }
        void (async (): Promise<StreamProbeResponse> => {
          const r = resolveOfferKey(msg.key, await media.list(f.tabId), reports.tab(f.tabId), f, (u) => refusedFiles.has(f.tabId, f.frameId, u));
          return r?.kind === 'stream' ? probeStream(f.tabId, r.item) : failure('expired', OFFER_GONE_MESSAGE);
        })().then(sendResponse);
        return true;
      }
      case 'downex:offer-add': {
        if (fromExtensionPage) return undefined;
        const f = senderFrame(sender);
        if (!f) {
          sendResponse(failure('unsupported_page', NO_TAB_MESSAGE) satisfies OfferAddResponse);
          return undefined;
        }
        const tracks: MediaTracks = {};
        if (msg.videoTrack) tracks.videoTrack = msg.videoTrack;
        if (msg.audioTrack) tracks.audioTrack = msg.audioTrack;
        void (async (): Promise<OfferAddResponse> => {
          const r = resolveOfferKey(msg.key, await media.list(f.tabId), reports.tab(f.tabId), f, (u) => refusedFiles.has(f.tabId, f.frameId, u));
          if (!r) return failure('expired', OFFER_GONE_MESSAGE);
          if (r.kind === 'stream') return addStream(f.tabId, r.item, tracks);
          if (r.kind === 'file') return addFile(f.tabId, itemKey(r.item), fileSource(r.item, f.frameUrl, f.tab.title));
          // The element's own MP4 address: no recorded headers, so no cookies (§6.2).
          const src: FileSource = { url: r.video.src, size: 0 };
          if (f.frameUrl) src.frameUrl = f.frameUrl;
          if (f.tab.title) src.pageTitle = f.tab.title;
          return addFile(f.tabId, r.sendKey, src);
        })().then(sendResponse);
        return true;
      }
      case 'downex:set-site-hidden': {
        const { hidden } = msg;
        if (fromExtensionPage) {
          // The popup's "This tab" toggle names its tab.
          const { tabId } = msg;
          if (tabId === undefined) return undefined;
          void browser.tabs
            .get(tabId)
            .then(
              (t) => siteHost(t.url),
              () => undefined,
            )
            .then((host) => setSiteHidden(host, hidden))
            .then(sendResponse);
          return true;
        }
        // A panel's "Don't show on <host>": the host of its own tab, never another.
        const f = senderFrame(sender);
        if (!f || msg.tabId !== undefined) return undefined;
        void setSiteHidden(siteHost(f.tab.url), hidden).then(sendResponse);
        return true;
      }
      case 'downex:page-video-probe': {
        // The overlay of a content script: only its own frame's video (senderTarget).
        if (fromExtensionPage) return undefined;
        void tabFrames(sender.tab?.id)
          .then((frames): Promise<VideoProbeResponse> | VideoProbeResponse => {
            const target = senderTarget(sender, msg.url, frames);
            return target.ok ? videoHandOff.probe(target.value) : target;
          })
          .then(sendResponse);
        return true;
      }
      case 'downex:page-video-prefetch': {
        // The top frame's content script: the user stays on its video. Not answered.
        if (fromExtensionPage) return undefined;
        // On a curated site whose tab has reported no playable video yet, it is held back until
        // one does (the player may create its <video> late, or in a sub-frame).
        const request = lookAheadRequest(sender, msg.url);
        if (request && playsVideo(request)) lookAhead.request(request);
        else if (request) heldLookAheads.hold(request, Date.now());
        return undefined;
      }
      case 'downex:page-video-add': {
        if (fromExtensionPage) return undefined;
        const tabId = sender.tab?.id;
        void tabFrames(tabId)
          .then((frames): Promise<VideoAddResponse> | VideoAddResponse => {
            const target = senderTarget(sender, msg.url, frames);
            if (!target.ok || tabId === undefined) return target.ok ? failure('unsupported_page', 'No tab.') : target;
            return addVideo(tabId, target.value, msg.videoTrack, msg.audioTrack);
          })
          .then(sendResponse);
        return true;
      }
      case 'downex:tab-video-probe': {
        if (!fromExtensionPage) return undefined;
        void lookUp(msg.tabId, msg.key)
          .then((t): Promise<VideoProbeResponse> | VideoProbeResponse => (t.ok ? videoHandOff.probe(t.value) : t))
          .then(sendResponse);
        return true;
      }
      case 'downex:tab-video-add': {
        if (!fromExtensionPage) return undefined;
        const { tabId, key, videoTrack, audioTrack } = msg;
        void lookUp(tabId, key)
          .then((t): Promise<VideoAddResponse> | VideoAddResponse => (t.ok ? addVideo(tabId, t.value, videoTrack, audioTrack) : t))
          .then(sendResponse);
        return true;
      }
      case 'downex:find-page-video': {
        if (!fromExtensionPage) return undefined;
        void tabAndFrames(msg.tabId)
          .then(
            ({ tab, frames }): FindPageVideoResponse => findEntry(tab, frames),
            (e: unknown): FindPageVideoResponse => failure('expired', `The tab is gone: ${errorMessage(e)}`),
          )
          .then(sendResponse);
        return true;
      }
      case 'downex:add-media': {
        if (!fromExtensionPage) return undefined;
        const { tabId, url } = msg;
        const tracks: MediaTracks = {};
        if (msg.videoTrack) tracks.videoTrack = msg.videoTrack;
        if (msg.audioTrack) tracks.audioTrack = msg.audioTrack;
        // One per stream at a time: a popup opened again (or an overlay panel) joins the hand-off still running.
        void media
          .get(tabId, url)
          .catch(() => undefined)
          .then((item): Promise<AddMediaResponse> | AddMediaResponse =>
            item ? addStream(tabId, item, tracks) : failure('expired', STREAM_GONE_MESSAGE),
          )
          .then(sendResponse);
        return true;
      }
      case 'downex:media-result':
        if (!fromExtensionPage) return undefined;
        void media
          .get(msg.tabId, msg.url)
          .catch(() => undefined)
          .then((item) => (item ? mediaSends.follow(msg.tabId, itemKey(item)) : undefined))
          .then((result) => sendResponse((result ? { result } : {}) satisfies MediaResultResponse));
        return true;
      case 'downex:links-batch':
        if (!fromExtensionPage) return undefined;
        batches
          .get(msg.batchId)
          .catch(() => undefined)
          .then((b) => sendResponse((b ?? null) satisfies LinksBatchResponse));
        return true;
      case 'downex:links-confirm': {
        if (!fromExtensionPage) return undefined;
        const { batchId, accept } = msg;
        void (async (): Promise<LinksConfirmResponse> => {
          const b = await batches.take(batchId).catch(() => undefined);
          if (!b) return { status: 'expired' };
          if (!accept) return { status: 'cancelled' };
          return { status: 'done', ...(await sendLinks(b.links, b.referrer, b.storeId)) };
        })().then(sendResponse);
        return true;
      }
    }
  });

  // --- Context menus (BROWSER-SPEC §4) ---------------------------------------------------

  async function createMenus(): Promise<void> {
    await browser.contextMenus.removeAll();
    const reportError = () => {
      const e = browser.runtime.lastError;
      if (e) log('context menu', e.message);
    };
    browser.contextMenus.create(
      {
        id: MENU_DOWNLOAD,
        title: 'Download with Downex',
        contexts: ['link', 'image', 'video', 'audio'],
        targetUrlPatterns: HTTP_URLS,
      },
      reportError,
    );
    browser.contextMenus.create(
      {
        id: MENU_DOWNLOAD_ALL,
        title: 'Download all links with Downex',
        contexts: ['page', 'frame'],
        documentUrlPatterns: HTTP_URLS,
      },
      reportError,
    );
  }

  type MenuInfo = Parameters<Parameters<typeof browser.contextMenus.onClicked.addListener>[0]>[0];
  type MenuTab = Parameters<Parameters<typeof browser.contextMenus.onClicked.addListener>[0]>[1];

  const onDownloadMenu = async (info: MenuInfo, tab: MenuTab) => {
    const url = [info.linkUrl, info.srcUrl].find(isHttpUrl);
    if (!url) return;
    if (tab?.incognito) return recordFailure(url, 'menu', 'unsupported', PRIVATE_WINDOW_MESSAGE);
    // The store build knows no YouTube video, and its page is no file: never sent as one.
    if (storeExcludes(url)) return recordFailure(url, 'menu', 'unsupported', STORE_LINK_MESSAGE);
    const page = info.frameUrl ?? info.pageUrl ?? tab?.url;
    const storeId = (tab as BrowsingContextInfo | undefined)?.cookieStoreId;
    // A link to a video page: the video with Downex's default pick, never the HTML page.
    const video = parseSiteVideo(info.linkUrl);
    if (video) {
      const target: VideoTarget = { key: video.key, pageUrl: video.pageUrl, storeId: storeId ?? '', generic: false };
      const referer = httpAddress(page);
      if (referer) target.referer = referer;
      await videoHandOff.add(target, '', '');
      return;
    }
    await handOff(
      { url, ...(isHttpUrl(page) ? { referrer: page } : {}), ...(storeId ? { storeId } : {}) },
      { source: 'menu' },
    );
  };

  const openConfirmation = async (batchId: string, windowId: number | undefined) => {
    const url = `${browser.runtime.getURL('/confirm.html')}?batch=${encodeURIComponent(batchId)}`;
    try {
      await browser.windows.create({ url, type: 'popup', width: 560, height: 620 });
    } catch {
      await browser.tabs.create(windowId !== undefined ? { url, windowId } : { url });
    }
  };

  const onDownloadAllMenu = async (info: MenuInfo, tab: MenuTab) => {
    const page = info.frameUrl ?? info.pageUrl ?? tab?.url ?? '';
    if (tab?.id === undefined || tab.id < 0) return;
    if (tab.incognito) return recordFailure(page, 'page-links', 'unsupported', PRIVATE_WINDOW_MESSAGE);
    let links: string[];
    try {
      const results = await browser.scripting.executeScript({
        target: { tabId: tab.id, frameIds: [info.frameId ?? 0] },
        func: collectPageLinks,
      });
      links = uniqueHttpLinks(results.flatMap((r) => (Array.isArray(r.result) ? (r.result as unknown[]) : [])));
    } catch (e) {
      return recordFailure(page, 'page-links', 'unsupported', `Cannot read the links of this page: ${errorMessage(e)}`);
    }
    if (links.length === 0) return recordFailure(page, 'page-links', 'no_links', 'No http(s) links on this page.');
    // Only links to files of the capture list: page, logout and action links are never sent.
    const files = fileLinks(links, rules ?? (await rulesReady));
    if (files.length === 0) {
      return recordFailure(page, 'page-links', 'no_links', `None of the ${links.length} links on this page is a file of the capture list.`);
    }
    const referrer = isHttpUrl(page) ? page : undefined;
    const storeId = (tab as BrowsingContextInfo).cookieStoreId;
    if (files.length <= CONFIRM_THRESHOLD) {
      await sendLinks(files, referrer, storeId);
      return;
    }
    const batchId = crypto.randomUUID();
    try {
      await batches.put({
        id: batchId,
        links: files,
        createdAt: Date.now(),
        ...(referrer ? { referrer } : {}),
        ...(storeId ? { storeId } : {}),
      });
    } catch (e) {
      return recordFailure(page, 'page-links', 'internal', `Cannot keep the links for confirmation: ${errorMessage(e)}`);
    }
    await openConfirmation(batchId, tab.windowId);
  };

  browser.contextMenus.onClicked.addListener((info, tab) => {
    const run =
      info.menuItemId === MENU_DOWNLOAD
        ? onDownloadMenu
        : info.menuItemId === MENU_DOWNLOAD_ALL
          ? onDownloadAllMenu
          : undefined;
    run?.(info, tab).catch((e: unknown) => log('context menu action failed', errorMessage(e)));
  });

  client.start();
});
