<script lang="ts">
  // The progress window of one download (DESKTOP-SPEC §7): "downex-ui --progress <id>",
  // a small window of its own process. Status and Speed limit tabs, the details of each
  // connection, the failure view, and the download complete dialog (§8), which is also
  // what a launch for a completed download shows. Opening the window marks the download
  // for that dialog (ProgressStore).
  import { onDestroy, onMount, tick, untrack } from 'svelte';
  import {
    LIMITS,
    isActive,
    isResumable,
    needsRestart,
    toApiError,
    type Api,
    type ConnectionInfo,
    type Download,
    type DownloadOptions,
    type DownloadState,
  } from './lib/api';
  import { displayName, isExecutableName, kindOf, rawName } from './lib/categories';
  import { errorHint, joinPath, rawJoinPath } from './lib/display';
  import { DASH, RATE_UNITS, formatPercent, formatSpeed, parseRate, progressFraction, splitRate, type RateUnit } from './lib/format';
  import { isSiteDownload, siteErrorHint } from './lib/media';
  import {
    DETAILS_STORAGE_KEY,
    PROGRESS_WINDOW,
    addressLabel,
    averageRateText,
    connectionReceived,
    connectionText,
    doneAtText,
    downloadedText,
    fragmentsText,
    headerPercent,
    isAssembling,
    isRefused,
    mediaFormatText,
    pieceNames,
    progressStatus,
    progressTitle,
    qualityDetail,
    qualityText,
    rateText,
    resumeSupport,
    sizeText,
    timeLeftText,
    timeTakenSummary,
    fitWindowHeight,
    windowFrame,
    type FitView,
  } from './lib/progress';
  import { ProgressStore } from './lib/stores/progress.svelte';
  import { escapeBidi, hasBidiControls } from './lib/text';
  import FileIcon from './lib/components/FileIcon.svelte';
  import FileName from './lib/components/FileName.svelte';
  import Icon from './lib/components/Icon.svelte';
  import OpenDialog from './lib/components/OpenDialog.svelte';
  import ProgressBar from './lib/components/ProgressBar.svelte';
  import SegmentBar from './lib/components/SegmentBar.svelte';
  import SegmentMap from './lib/components/SegmentMap.svelte';
  import Select, { type SelectOption } from './lib/components/Select.svelte';
  import StatusMark, { markOfState, type MarkKind } from './lib/components/StatusMark.svelte';
  import Tabs, { tabId, type TabOption } from './lib/components/Tabs.svelte';

  interface Props {
    api: Api;
    /** The download this window shows. */
    id: number;
    /** How often downexd is checked (tests shorten it). */
    statusMs?: number;
    /** How long typing in the limit fields waits before it applies them. */
    applyDelayMs?: number;
    /** The host has the system's "Open with" dialog (StartupView.openWith); without it, no Open with…. */
    openWith?: boolean;
  }

  let { api, id, statusMs, applyDelayMs = 600, openWith = true }: Props = $props();

  const uid = $props.id();
  // svelte-ignore state_referenced_locally
  const store = new ProgressStore(api, id);

  function readExpanded(): boolean {
    try {
      return localStorage.getItem(DETAILS_STORAGE_KEY) === '1';
    } catch {
      return false;
    }
  }

  let tab = $state<'status' | 'limits'>('status');
  let expanded = $state(readExpanded());
  /** The completion view replaced the status (it stays until the window closes). */
  let complete = $state(false);
  let busy = $state(false);
  let actionError = $state<string | null>(null);
  /** What was just copied (its button says so for a moment). */
  let copied = $state<'address' | 'path' | null>(null);
  /** The question before a program is opened: with its default app, or one the user picks. */
  let openPrompt = $state<'open' | 'with' | null>(null);
  let dontShowAgain = $state(false);
  let pageEl = $state<HTMLElement | undefined>();
  let completeEl = $state<HTMLElement | undefined>();
  /** The progress view's panel (Status, Speed limit): what scrolls there. */
  let panelEl = $state<HTMLElement | undefined>();
  let openButton = $state<HTMLButtonElement | undefined>();
  let prefError = $state<string | null>(null);
  let closing = false;
  let lastTitle = '';

  const d = $derived(store.download);
  const media = $derived(d?.kind === 'media');
  /** A web page's video (SITE-MEDIA-SPEC §5.4): "Page", parts, "Merging…". */
  const site = $derived(d !== null && isSiteDownload(d));
  /** What its pieces are called ("Fragments", a web page's "Parts"). */
  const pieces = $derived(d ? pieceNames(d) : { one: 'Fragment', many: 'Fragments' });
  /** The hint under a failure: a web page's own, else the shared one. */
  const failHint = $derived(d ? (site && siteErrorHint(d.errorCode)) || errorHint(d.errorCode) : '');
  const offline = $derived(!store.connected);
  const canPause = $derived(d !== null && isActive(d.state));
  const canResume = $derived(d !== null && isResumable(d.state) && !(d.state === 'failed' && (isRefused(d) || needsRestart(d))));
  const failed = $derived(d?.state === 'failed');

  onMount(() => {
    void store.start(statusMs);
  });
  onDestroy(() => {
    destroyed = true;
    fitObserver?.disconnect();
    store.dispose();
    clearTimeout(speedTimer);
    clearTimeout(connTimer);
    clearTimeout(copiedTimer);
  });

  function closeWindow(): void {
    if (closing) return;
    closing = true;
    api.window.close();
  }

  // The title bar: "<percent>% <file name>".
  $effect(() => {
    const title = d ? progressTitle(d) : 'Downex';
    if (title !== lastTitle) {
      lastTitle = title;
      api.window.setTitle(title);
    }
  });

  // The window opens collapsed; it shows the details at once when they were shown last
  // time and there is a running download to show them for. Collapsed, it fits its facts.
  let sized = false;
  $effect(() => {
    const cur = d;
    if (sized || !store.ready || cur === null) return;
    sized = true;
    if (cur.state === 'completed') return;
    if (untrack(() => expanded)) void resize(true);
    else void fitWindow();
  });

  // A download removed elsewhere takes its window along.
  $effect(() => {
    if (store.removed) closeWindow();
  });

  // Completion: the summary view (Settings.showCompleteDialog), or the window closes; the
  // summary also shows when the settings are unknown. The setting may have changed since
  // the window opened (in Settings, in another progress window, or the first read failed),
  // so it is read again first; the last one read decides when that fails. A download that
  // was complete when the window opened decides with the settings just read with it: it is
  // downexd's completion popup (launched only while the setting is on), or a window opened
  // as the download started that it outran (a small file), which closes while it is off.
  let deciding = false;
  let destroyed = false;
  /** The first completion was decided (later ones, after a restart, read the settings again). */
  let decidedOnce = false;
  $effect(() => {
    const cur = d;
    if (!cur || !store.ready) return;
    if (cur.state !== 'completed') {
      // Restarted (or reset elsewhere): back to its progress, at the size for it.
      if (untrack(() => complete)) {
        complete = false;
        void resize(untrack(() => expanded));
      }
      return;
    }
    if (untrack(() => complete) || deciding) return;
    const first = !decidedOnce;
    decidedOnce = true;
    if (first && untrack(() => store.firstState) === 'completed') {
      if (untrack(() => store.settings)?.showCompleteDialog === false) closeWindow();
      else showComplete();
      return;
    }
    deciding = true;
    void store.loadSettings().then((settings) => {
      deciding = false;
      // It may have been restarted (or removed) while the settings were read.
      if (destroyed || complete || store.download?.state !== 'completed') return;
      if (settings !== null && !settings.showCompleteDialog) closeWindow();
      else showComplete();
    });
  });

  function showComplete(): void {
    complete = true;
    openPrompt = null;
    // The details belong to the progress; the window is sized to the dialog (fitComplete).
    expanded = false;
    // Enter opens the file: the primary button has the focus.
    void tick().then(() => {
      if (complete && !openPrompt) openButton?.focus();
    });
  }

  // ---- the window fits what it shows (DESIGN-SYSTEM.md §4.8) ------------------------------
  // The completion view, and the progress view without its details, get the window height
  // their content needs: measured from layout boxes (fractional), plus the frame and a little
  // slack (the host rounds window sizes to device pixels), so neither shows a needless
  // scrollbar at 100, 125 or 150 % scaling. It fits again when the content changes size (a
  // message, a fact, larger text, a narrower window: ResizeObserver) and when the display's
  // scaling changes; not when the user resizes the window. Both are fitted exactly, the
  // collapsed view never below its usual 300 px; on the Speed limit tab (which needs no more
  // than a running download's Status) and on a switch between the tabs it only grows, so
  // switching never moves the window or its buttons. The expanded view keeps its 560 px, where
  // the connections scroll.

  let fitObserver: ResizeObserver | undefined;
  /** What the observer already watches. */
  const watched = new WeakSet<Element>();
  let fitQueued = false;
  /**
   * The title bar and frame, taken once (before this page resized its window) and again after
   * the scaling changes: right after a resize, the host may report the new outer size while
   * the page still has its old height, which would look like a taller frame.
   */
  let knownFrame: number | null = null;
  /** The tab the collapsed view was last fitted on (a switch to the other one only grows it). */
  let fittedTab: typeof tab | null = null;

  /** What the window shows now, if it is fitted to it. */
  function fitView(): FitView | null {
    if (complete) return 'complete';
    return !expanded && d !== null ? 'collapsed' : null;
  }

  function queueFit(): void {
    if (fitQueued || destroyed) return;
    fitQueued = true;
    void tick().then(() => {
      fitQueued = false;
      void fitWindow();
    });
  }

  const px = (v: string): number => {
    const n = Number.parseFloat(v);
    return Number.isFinite(n) ? n : 0;
  };

  /** The children that take room in the flow (not the sr-only status, nothing hidden). */
  function inFlow(parent: HTMLElement): HTMLElement[] {
    return [...parent.children].filter((el): el is HTMLElement => {
      if (!(el instanceof HTMLElement)) return false;
      const s = getComputedStyle(el);
      return s.display !== 'none' && s.position !== 'absolute' && s.position !== 'fixed';
    });
  }

  /** What a scrolling element's content spans, with its padding and borders (scrolling moves it all alike). */
  function contentHeight(el: HTMLElement): number {
    let top = Number.POSITIVE_INFINITY;
    let bottom = Number.NEGATIVE_INFINITY;
    for (const kid of inFlow(el)) {
      const r = kid.getBoundingClientRect();
      const s = getComputedStyle(kid);
      top = Math.min(top, r.top - px(s.marginTop));
      bottom = Math.max(bottom, r.bottom + px(s.marginBottom));
    }
    if (!(bottom > top)) return 0;
    const s = getComputedStyle(el);
    return bottom - top + px(s.paddingTop) + px(s.paddingBottom) + px(s.borderTopWidth) + px(s.borderBottomWidth);
  }

  /** The page height that shows all of `scroller`'s content without scrolling (0 = not measurable). */
  function neededHeight(page: HTMLElement, scroller: HTMLElement): number {
    const blocks = inFlow(page);
    let sum = 0;
    for (const el of blocks) sum += el === scroller ? contentHeight(el) : el.getBoundingClientRect().height;
    if (!(sum > 0)) return 0;
    const s = getComputedStyle(page);
    return sum + px(s.paddingTop) + px(s.paddingBottom) + px(s.rowGap) * Math.max(0, blocks.length - 1);
  }

  /** Watches what decides the height: the page's other blocks and the content of the part that scrolls. */
  function watch(page: HTMLElement, scroller: HTMLElement): void {
    if (typeof ResizeObserver === 'undefined') return;
    fitObserver ??= new ResizeObserver(() => queueFit());
    for (const el of [...inFlow(page).filter((b) => b !== scroller), ...inFlow(scroller)]) {
      if (watched.has(el)) continue;
      watched.add(el);
      fitObserver.observe(el);
    }
  }

  async function fitWindow(exact = false): Promise<void> {
    await tick();
    const view = fitView();
    const page = pageEl;
    const scroller = view === 'complete' ? completeEl : panelEl;
    if (view === null || destroyed || !page || !scroller) return;
    watch(page, scroller);
    const content = neededHeight(page, scroller);
    // Nothing measurable (no layout yet): the collapsed window keeps the size it opened with.
    if (view === 'collapsed' && !(content > 0) && !exact) return;
    const size = await api.window.getSize();
    if (destroyed || fitView() !== view) return;
    // The host sizes the whole window: the page plus its title bar and frame.
    const frame = (knownFrame ??= windowFrame(size?.height, window.innerHeight));
    const height = fitWindowHeight(view, content, frame);
    const width = Math.max(size?.width ?? PROGRESS_WINDOW.width, PROGRESS_WINDOW.width);
    // On the Speed limit tab, and on a switch between the tabs, it only grows: the tabs and
    // the buttons below them do not jump.
    const switched = view === 'collapsed' && fittedTab !== null && fittedTab !== tab;
    if (view === 'collapsed') fittedTab = tab;
    if (view === 'collapsed' && !exact && (tab !== 'status' || switched)) {
      if (window.innerHeight + frame >= height) return;
    } else if (size?.width === width && size.height === height) return;
    api.window.setSize(width, height);
  }

  // The completion view: when it appears, and when a message or the banner changes its height.
  $effect(() => {
    if (!complete) return;
    void [actionError, prefError, dontShowAgain, offline];
    void fitWindow();
  });

  // The collapsed progress view: another tab, a message or the banner.
  $effect(() => {
    void [tab, actionError, offline, failed];
    if (untrack(() => complete || expanded || !sized)) return;
    queueFit();
  });

  // The display's scaling changed (another monitor, a new setting): the frame and the
  // rounding change, so it fits again.
  $effect(() => {
    if (typeof matchMedia !== 'function') return;
    let mq: MediaQueryList | undefined;
    const onChange = (): void => {
      listen();
      knownFrame = null;
      queueFit();
    };
    const listen = (): void => {
      mq?.removeEventListener('change', onChange);
      mq = matchMedia(`(resolution: ${window.devicePixelRatio}dppx)`);
      mq.addEventListener('change', onChange);
    };
    listen();
    return () => mq?.removeEventListener('change', onChange);
  });

  /** Enter opens the file and Esc closes the window (DESKTOP-SPEC §8); buttons keep their own Enter. */
  function onKeydown(e: KeyboardEvent): void {
    if (!complete || openPrompt !== null || e.defaultPrevented || e.isComposing) return;
    if (e.key === 'Escape') {
      e.preventDefault();
      closeWindow();
      return;
    }
    if (e.key !== 'Enter' || e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
    const target = e.target;
    if (target instanceof Element && target.closest('button, a[href], select, textarea, input:not([type="checkbox"])')) return;
    e.preventDefault();
    open();
  }

  // Settings changed while the window was in the background (the default connections).
  function onWindowFocus(): void {
    if (store.connected) void store.loadSettings();
  }

  /** The details' fixed height, or the collapsed view fitted to its facts. */
  async function resize(details: boolean): Promise<void> {
    if (!details) return fitWindow(true);
    const size = await api.window.getSize();
    api.window.setSize(Math.max(size?.width ?? PROGRESS_WINDOW.width, PROGRESS_WINDOW.width), PROGRESS_WINDOW.expandedHeight);
  }

  function toggleDetails(): void {
    expanded = !expanded;
    try {
      localStorage.setItem(DETAILS_STORAGE_KEY, expanded ? '1' : '0');
    } catch {
      // storage unavailable: the choice lasts for this window
    }
    void resize(expanded);
  }

  async function act(fn: () => Promise<void>): Promise<boolean> {
    if (busy) return false;
    busy = true;
    actionError = null;
    try {
      await fn();
      return true;
    } catch (err) {
      actionError = toApiError(err).message;
      return false;
    } finally {
      busy = false;
    }
  }

  function togglePause(): void {
    if (offline || !d) return;
    if (canPause) void act(() => store.pause());
    else if (canResume) void act(() => store.resume());
  }

  /** Cancel: pauses the download (it stays in the list and can be resumed) and closes. */
  async function cancel(): Promise<void> {
    if (d && isActive(d.state)) {
      if (offline) return;
      if (!(await act(() => store.pause()))) return;
    }
    closeWindow();
  }

  function retry(): void {
    if (offline || !d) return;
    void act(() => store.resume(needsRestart(d)));
  }

  /** Copies the address, or the full path of the saved file (as it is, not as shown). */
  async function copy(what: 'address' | 'path'): Promise<void> {
    if (!d) return;
    try {
      await api.copyText(what === 'address' ? d.url : rawJoinPath(d.dir, rawName(d)));
      copied = what;
      clearTimeout(copiedTimer);
      copiedTimer = setTimeout(() => (copied = null), 2000);
    } catch (err) {
      actionError = `Could not copy: ${toApiError(err).message}`;
    }
  }
  let copiedTimer: ReturnType<typeof setTimeout> | undefined;

  // ---- tabs (Tabs: roving focus, arrows, Home and End) -------------------------------------

  /** Status and Speed limit; a Mint dot after "Speed limit" while a limit is on. */
  const TABS = $derived<TabOption<'status' | 'limits'>[]>([
    { value: 'status', label: 'Status' },
    { value: 'limits', label: 'Speed limit', dot: (d?.speedLimit ?? 0) > 0 },
  ]);
  const panelId = `${uid}-panel`;
  const UNIT_OPTIONS: SelectOption<RateUnit>[] = RATE_UNITS.map((u) => ({ value: u, label: u }));

  // ---- speed and connection limits ---------------------------------------------------

  let limitOn = $state(false);
  let limitValue = $state('');
  let limitUnit = $state<RateUnit>('KB/s');
  let limitError = $state<string | null>(null);
  let connValue = $state('');
  let connError = $state<string | null>(null);
  /** Pending applies of typed values (undefined = none). */
  let speedTimer: ReturnType<typeof setTimeout> | undefined;
  let connTimer: ReturnType<typeof setTimeout> | undefined;
  /**
   * The download's limits as far as this window knows (its reports, the answers to what it
   * sent, or what it sent while the answer is outstanding); null = unknown, so the next
   * apply sends. A blur after a change thus applies nothing twice.
   */
  let sentSpeed: number | null = null;
  let sentConns: number | null = null;
  /** The user changed the fields since they last showed the download's limits. */
  let speedEdited = false;
  let connEdited = false;
  /** SetDownloadOptions calls of each field still waiting for their answer. */
  let speedSending = 0;
  let connSending = 0;

  const speedLimitNow = $derived(d === null ? null : d.speedLimit);
  const connectionsNow = $derived(d === null ? null : d.maxConnections);

  function focused(...ids: string[]): boolean {
    const el = document.activeElement;
    return el instanceof HTMLElement && ids.includes(el.id);
  }

  /** The user is editing the speed: its field has the focus, or a typed value waits to apply. */
  function editingSpeed(): boolean {
    return speedTimer !== undefined || focused(`${uid}-limit`, `${uid}-unit`);
  }

  function editingConnections(): boolean {
    return connTimer !== undefined || focused(`${uid}-conns`);
  }

  /** The speed fields show the download's limit (they start from it and follow changes made elsewhere). */
  function showSpeed(bytes: number): void {
    sentSpeed = bytes;
    speedEdited = false;
    limitError = null;
    limitOn = bytes > 0;
    if (bytes > 0) {
      const r = splitRate(bytes);
      limitValue = r.value;
      limitUnit = r.unit;
    }
  }

  function showConnections(n: number): void {
    sentConns = n;
    connEdited = false;
    connError = null;
    connValue = n > 0 ? String(n) : '';
  }

  // A limit changed elsewhere ("downex set", another client) shows at once unless the user
  // is editing that field; its answer or the end of the edit shows it then.
  $effect(() => {
    const bytes = speedLimitNow;
    if (bytes === null) return;
    untrack(() => {
      if (speedSending > 0) return;
      if (editingSpeed()) sentSpeed = bytes;
      else showSpeed(bytes);
    });
  });
  $effect(() => {
    const n = connectionsNow;
    if (n === null) return;
    untrack(() => {
      if (connSending > 0) return;
      if (editingConnections()) sentConns = n;
      else showConnections(n);
    });
  });

  /** Sends a change of the download's limits; resolves to the answer, null when it failed. */
  async function sendOptions(opts: DownloadOptions): Promise<Download | null> {
    let answer: Download | null = null;
    const ok = await act(async () => {
      answer = await store.setOptions(opts);
    });
    return ok ? answer : null;
  }

  const defaultConnections = $derived(store.settings?.defaultMaxConnections ?? 0);

  /** The speed to apply: 0 = unlimited, null = the field is not valid. */
  function wantedSpeed(): number | null {
    if (!limitOn) return 0;
    const bytes = parseRate(limitValue, limitUnit, sentSpeed ?? 0);
    if (bytes === null) {
      limitError = 'Enter a number, such as 500 or 1.5';
      return null;
    }
    if (bytes <= 0) {
      limitError = 'Enter a speed above 0, or turn the limit off';
      return null;
    }
    limitError = null;
    return bytes;
  }

  /** Applies an edit of the speed fields; without one, they show the download's limit again. */
  function applySpeed(): void {
    clearTimeout(speedTimer);
    speedTimer = undefined;
    if (!speedEdited) {
      const cur = store.download;
      if (cur && speedSending === 0) showSpeed(cur.speedLimit);
      return;
    }
    const bytes = wantedSpeed();
    if (bytes === null || offline) return;
    speedEdited = false;
    if (bytes === sentSpeed) return;
    sentSpeed = bytes;
    speedSending++;
    void sendOptions({ speedLimit: bytes }).then((answer) => {
      speedSending--;
      if (answer === null) {
        // not applied: the fields keep the edit, and the next apply sends it again
        sentSpeed = null;
        speedEdited = true;
        return;
      }
      sentSpeed = answer.speedLimit;
      const cur = store.download;
      if (speedSending === 0 && cur && !editingSpeed()) showSpeed(cur.speedLimit);
    });
  }

  function applySpeedSoon(): void {
    speedEdited = true;
    clearTimeout(speedTimer);
    speedTimer = setTimeout(applySpeed, applyDelayMs);
  }

  function onLimitToggle(on: boolean): void {
    limitOn = on;
    speedEdited = true;
    if (on && limitValue.trim() === '') {
      limitValue = '500';
      limitUnit = 'KB/s';
    }
    applySpeed();
  }

  function onUnitChange(): void {
    speedEdited = true;
    applySpeed();
  }

  function wantedConnections(): number | null {
    const text = connValue.trim();
    if (text === '') {
      connError = null;
      return 0;
    }
    const n = Number(text);
    if (!/^\d+$/.test(text) || n < LIMITS.minConnections || n > LIMITS.maxConnections) {
      connError = `Enter a whole number from ${LIMITS.minConnections} to ${LIMITS.maxConnections}, or leave it empty for the default`;
      return null;
    }
    connError = null;
    return n;
  }

  function applyConnections(): void {
    clearTimeout(connTimer);
    connTimer = undefined;
    if (!connEdited) {
      const cur = store.download;
      if (cur && connSending === 0) showConnections(cur.maxConnections);
      return;
    }
    const n = wantedConnections();
    if (n === null || offline) return;
    connEdited = false;
    if (n === sentConns) return;
    sentConns = n;
    connSending++;
    void sendOptions({ maxConnections: n }).then((answer) => {
      connSending--;
      if (answer === null) {
        sentConns = null;
        connEdited = true;
        return;
      }
      // 0 asks for the default: the answer says which number that is.
      sentConns = answer.maxConnections;
      const cur = store.download;
      if (connSending === 0 && cur && !editingConnections()) showConnections(cur.maxConnections);
    });
  }

  function applyConnectionsSoon(): void {
    connEdited = true;
    clearTimeout(connTimer);
    connTimer = setTimeout(applyConnections, applyDelayMs);
  }

  function onEnter(e: KeyboardEvent, apply: () => void): void {
    if (e.key === 'Enter') {
      e.preventDefault();
      apply();
    }
  }

  // ---- completion --------------------------------------------------------------------

  /** A program, or a name that hides its type, is confirmed before anything opens it. */
  function needsPrompt(cur: Download): boolean {
    const raw = rawName(cur);
    return isExecutableName(raw) || hasBidiControls(raw);
  }

  function open(): void {
    if (!d || busy) return;
    if (needsPrompt(d)) {
      openPrompt = 'open';
      return;
    }
    void openFile();
  }

  async function openFile(): Promise<void> {
    if (await act(() => api.openFile(id))) closeWindow();
  }

  /** Open with…: the user picks the app (the system's dialog); the same checks come first. */
  function chooseApp(): void {
    if (!d || busy || !openWith) return;
    if (needsPrompt(d)) {
      openPrompt = 'with';
      return;
    }
    void openWithApp();
  }

  async function openWithApp(): Promise<void> {
    if (busy) return;
    busy = true;
    actionError = null;
    try {
      await api.openWith(id);
      closeWindow();
    } catch (err) {
      const e = toApiError(err);
      // Closing the system's dialog without a choice leaves this one as it was.
      if (e.code !== 'canceled') actionError = e.message;
    } finally {
      busy = false;
    }
  }

  async function openFolder(): Promise<void> {
    if (await act(() => api.openFolder(id))) closeWindow();
  }

  async function onDontShowAgain(checked: boolean): Promise<void> {
    dontShowAgain = checked;
    prefError = null;
    try {
      await store.setShowCompleteDialog(!checked);
    } catch (err) {
      dontShowAgain = !checked;
      prefError = `Could not save this choice: ${toApiError(err).message}`;
    }
  }

  const fraction = $derived(d ? progressFraction(d) : Number.NaN);
  const urlTail = $derived(d ? Math.max(0, d.url.length - 24) : 0);

  // ---- live retry countdown ------------------------------------------------------------

  /** The clock the countdowns read; it ticks only while a shown connection waits to retry. */
  let now = $state(Date.now());
  /** When each slot's current retry notice ("HTTP 503, retrying in 4 s") was first seen. */
  const retrySeen = new Map<number, { detail: string; at: number }>();
  const retryWait = /^(.+?),\s*retrying in (\d+(?:\.\d+)?)\s*s$/i;

  $effect(() => {
    if (!expanded || complete || !d?.connectionInfo.some((c) => c.state === 'retrying' && retryWait.test(c.detail))) return;
    now = Date.now();
    const timer = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(timer);
  });

  /** A connection's info; a wait in seconds counts down between the daemon's reports. */
  function connectionLine(c: ConnectionInfo, isMedia: boolean): string {
    const m = c.state === 'retrying' ? retryWait.exec(c.detail) : null;
    if (!m) {
      retrySeen.delete(c.index);
      return connectionText(c, isMedia, pieces.one);
    }
    let seen = retrySeen.get(c.index);
    if (seen?.detail !== c.detail) {
      seen = { detail: c.detail, at: Date.now() };
      retrySeen.set(c.index, seen);
    }
    const left = Math.max(0, Math.ceil(Number(m[2]) - Math.max(0, now - seen.at) / 1000));
    const text = `Retrying in ${left} s (${m[1]})`;
    return isMedia && c.fragment >= 0 ? `${pieces.one} ${c.fragment + 1} · ${text}` : text;
  }

  /** The colour of a state line: live Mint, paused amber, failed red, awaiting blue, else quiet. */
  function toneOf(state: DownloadState): 'live' | 'warn' | 'danger' | 'info' | 'done' | 'quiet' {
    switch (state) {
      case 'downloading':
      case 'probing':
        return 'live';
      case 'paused':
        return 'warn';
      case 'failed':
        return 'danger';
      case 'awaiting_confirmation':
        return 'info';
      case 'completed':
        return 'done';
      default:
        return 'quiet';
    }
  }

  /** The main window's mark for a download state or a connection's settled state. */
  function markOfAny(state: string): MarkKind {
    switch (state) {
      case 'receiving':
        return 'downloading';
      case 'error':
        return 'failed';
      case 'done':
        return 'completed';
      case 'idle':
      case 'unspecified':
        return 'canceled';
      default:
        return markOfState(state as DownloadState);
    }
  }
</script>

{#snippet stateMark(kind: string, size = 12)}
  <!-- the main window's marks (StatusMark) for the shared states; only the transient
       connection states (connecting/requesting/probing, retrying) have glyphs of their own -->
  {#if kind === 'probing' || kind === 'connecting' || kind === 'requesting'}
    <svg class="mark mark-connecting" viewBox="0 0 12 12" width={size} height={size} aria-hidden="true" focusable="false">
      <path class="spin" d="M6 1.75a4.25 4.25 0 1 1-4.25 4.25" />
    </svg>
  {:else if kind === 'retrying'}
    <svg class="mark mark-retrying" viewBox="0 0 12 12" width={size} height={size} aria-hidden="true" focusable="false">
      <path d="M9.9 6.1A3.9 3.9 0 1 1 8.7 3.2M9 1.5v2.25H6.75" />
    </svg>
  {:else}
    <StatusMark kind={markOfAny(kind)} {size} />
  {/if}
{/snippet}

{#snippet address(cur: Download)}
  <span class="url mono" title={escapeBidi(cur.url)}>
    <span class="url-head">{escapeBidi(cur.url.slice(0, urlTail))}</span><span class="url-tail"
      >{escapeBidi(cur.url.slice(urlTail))}</span
    >
  </span>
  <button type="button" class="icon-btn sm copy" aria-label="Copy address" title="Copy address" onclick={() => void copy('address')}>
    <Icon name={copied === 'address' ? 'check' : 'copy'} size={14} />
  </button>
{/snippet}

<svelte:window onfocus={onWindowFocus} onkeydown={onKeydown} />

<div class="pw" class:expanded={expanded && !complete} class:done={complete} bind:this={pageEl}>
  {#if offline}
    <p class="banner note warn" role="status">
      <Icon name="alert" size={16} />
      downexd is not reachable. This is the last known state; Downex keeps trying.
    </p>
  {/if}

  {#if d === null}
    <div class="message">
      {#if store.missing}
        <h1>This download no longer exists</h1>
        <p>It was removed from Downex.</p>
      {:else if store.loadError}
        <h1>Could not read this download</h1>
        <p><bdi>{escapeBidi(store.loadError)}</bdi></p>
        <button type="button" class="btn" onclick={() => void store.refresh()}>Try again</button>
      {:else}
        <p class="muted" role="status">Loading…</p>
      {/if}
      {#if store.missing || store.loadError}
        <div class="actions">
          <button type="button" class="btn primary" onclick={closeWindow}>Close</button>
        </div>
      {/if}
    </div>
  {:else if complete}
    {@const cur = d}
    {@const format = mediaFormatText(cur)}
    {@const quality = qualityText(cur)}
    <section class="complete" aria-labelledby="{uid}-done" aria-describedby="{uid}-done-name" bind:this={completeEl}>
      <header class="head done-head">
        <span class="done-tile" aria-hidden="true">
          <FileIcon kind={kindOf(cur)} size={40} />
          <span class="badge">
            <svg class="drawn" viewBox="0 0 16 16" width="10" height="10">
              <path d="M3.5 8.4l2.9 2.9 6-6.6" pathLength="1" />
            </svg>
          </span>
        </span>
        <div class="titles">
          <h1 id="{uid}-done">Download complete</h1>
          <p class="done-name" id="{uid}-done-name" title={displayName(cur)}><bdi>{displayName(cur)}</bdi></p>
        </div>
        <span class="figure tnum" aria-hidden="true">{sizeText(cur)}</span>
      </header>
      <dl class="facts">
        <dt>{addressLabel(cur)}</dt>
        <dd class="url-row">{@render address(cur)}</dd>
        <dt>Saved to</dt>
        <dd class="path-row">
          <span class="path mono" dir="auto">{joinPath(cur.dir, rawName(cur))}</span>
          <button type="button" class="icon-btn sm copy" aria-label="Copy path" title="Copy path" onclick={() => void copy('path')}>
            <Icon name={copied === 'path' ? 'check' : 'copy'} size={14} />
          </button>
        </dd>
        <dt>File size</dt>
        <dd class="tnum">{sizeText(cur)}</dd>
        <dt>Time taken</dt>
        <dd class="tnum">{timeTakenSummary(cur)}</dd>
        {#if format}
          <dt>Format</dt>
          <dd>{format}</dd>
        {/if}
        {#if quality}
          <dt>Quality</dt>
          <dd title={escapeBidi(qualityDetail(cur))}><bdi>{escapeBidi(quality)}</bdi></dd>
        {/if}
      </dl>
      <span class="sr-only" role="status">{copied === 'address' ? 'Address copied' : copied === 'path' ? 'Path copied' : ''}</span>
      <div class="pref">
        <label class="check">
          <input type="checkbox" checked={dontShowAgain} onchange={(e) => void onDontShowAgain(e.currentTarget.checked)} />
          Don’t show this dialog again
        </label>
        {#if dontShowAgain}
          <p class="hint">Downloads will no longer end with this dialog. Settings can turn it back on.</p>
        {/if}
        {#if prefError}
          <p class="error-text" role="alert"><bdi>{prefError}</bdi></p>
        {/if}
      </div>
      {#if actionError}
        <p class="note fail" role="alert"><bdi>{escapeBidi(actionError)}</bdi></p>
      {/if}
      <div class="actions">
        <button type="button" class="btn lg primary" onclick={open} disabled={busy} bind:this={openButton} aria-keyshortcuts="Enter">
          <Icon name="open" size={16} />
          Open
        </button>
        {#if openWith}
          <button type="button" class="btn lg" onclick={chooseApp} disabled={busy}>Open with…</button>
        {/if}
        <button type="button" class="btn lg" onclick={() => void openFolder()} disabled={busy}>
          <Icon name="folder" size={16} />
          Open folder
        </button>
        <span class="spacer"></span>
        <button type="button" class="btn lg ghost" onclick={closeWindow} aria-keyshortcuts="Escape">Close</button>
      </div>
    </section>
  {:else}
    {@const cur = d}
    {@const tone = toneOf(cur.state)}
    {@const pct = headerPercent(cur)}
    {@const doneAt = doneAtText(cur)}
    {@const avg = averageRateText(cur)}
    {@const quality = qualityText(cur)}
    <header class="head">
      <FileIcon kind={kindOf(cur)} size={32} />
      <div class="titles">
        <p class="name" title={displayName(cur)}><bdi><FileName name={displayName(cur)} /></bdi></p>
        <p class="state tone-{tone}" data-progress-state title={escapeBidi(progressStatus(cur))}>
          {@render stateMark(cur.state)}<bdi>{escapeBidi(progressStatus(cur))}</bdi>
        </p>
      </div>
      {#if pct}
        <span class="figure tnum" class:warn={cur.state === 'paused'} class:danger={cur.state === 'failed'}>{pct}</span>
      {/if}
    </header>

    <div
      class="bar"
      role="progressbar"
      aria-label="Download progress"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={Number.isFinite(fraction) ? Math.floor(Math.min(1, Math.max(0, fraction)) * 100) : undefined}
      aria-valuetext={Number.isFinite(fraction) ? formatPercent(fraction) : 'Size unknown'}
    >
      <SegmentBar download={cur} size="lg" />
    </div>

    <div class="tabs">
      <Tabs size="sm" flush bind:value={tab} options={TABS} idPrefix={uid} {panelId} ariaLabel="Download progress" />
    </div>

    <div class="panel" bind:this={panelEl}>
      {#if tab === 'status'}
        <div role="tabpanel" id={panelId} aria-labelledby={tabId(uid, 'status')} tabindex="-1">
          {#if failed && failHint}
            <p class="note fail fail-hint">{failHint}</p>
          {/if}
          <dl class="rows">
            <dt>{addressLabel(cur)}</dt>
            <dd class="wide url-row">
              {@render address(cur)}
              <span class="sr-only" role="status">{copied === 'address' ? 'Address copied' : ''}</span>
            </dd>
            <dt>File size</dt>
            <dd class="tnum">{sizeText(cur)}</dd>
            <dt>Downloaded</dt>
            <dd class="tnum">{downloadedText(cur)}</dd>
            {#if !failed}
              <!-- a failed download has no rate or time left: its note takes their place -->
              <dt>Transfer rate</dt>
              <!-- with the average of the whole download; cut in a narrow window: whole in its tooltip -->
              <dd
                class="tnum"
                class:live={cur.state === 'downloading' && cur.speedBps > 0}
                title={avg ? `${rateText(cur)} · avg ${avg}` : undefined}
                >{rateText(cur)}{#if avg}<span class="avg">{` · avg ${avg}`}</span>{/if}</dd
              >
              <dt>Time left</dt>
              <!-- cut in a narrow window with larger text: whole in its tooltip -->
              <dd class="tnum" title={doneAt ? `${timeLeftText(cur)} · Done ≈ ${doneAt}` : undefined}
                >{timeLeftText(cur)}{#if doneAt}<span class="done-at">{` · Done ≈ ${doneAt}`}</span>{/if}</dd
              >
            {/if}
            {#if media}
              <dt>{pieces.many}</dt>
              <dd class:tnum={fragmentsText(cur) !== ''}>{fragmentsText(cur) || 'Counted when it starts'}</dd>
            {/if}
            <dt>Resumable</dt>
            <dd>{resumeSupport(cur)}</dd>
            {#if quality}
              <dt>Quality</dt>
              <dd class="wide" title={escapeBidi(qualityDetail(cur))}><bdi>{escapeBidi(quality)}</bdi></dd>
            {/if}
            {#if cur.speedLimit > 0}
              <dt>Speed limit</dt>
              <dd class="limit tnum">{formatSpeed(cur.speedLimit)} for this download</dd>
            {/if}
          </dl>
        </div>
      {:else}
        <div role="tabpanel" id={panelId} aria-labelledby={tabId(uid, 'limits')} tabindex="-1" class="limits">
          <div class="setting">
            <label class="toggle">
              <input
                type="checkbox"
                role="switch"
                class="switch"
                checked={limitOn}
                onchange={(e) => onLimitToggle(e.currentTarget.checked)}
                disabled={offline}
              />
              Limit this download’s speed
            </label>
            <div class="limit-row">
              <input
                id="{uid}-limit"
                class="input sm tnum value"
                inputmode="decimal"
                aria-label="Speed limit"
                bind:value={limitValue}
                oninput={applySpeedSoon}
                onkeydown={(e) => onEnter(e, applySpeed)}
                onblur={applySpeed}
                disabled={!limitOn || offline}
                aria-invalid={limitError ? 'true' : undefined}
                aria-describedby="{uid}-limit-hint"
                autocomplete="off"
              />
              <div class="unit">
                <Select
                  id="{uid}-unit"
                  size="sm"
                  bind:value={limitUnit}
                  options={UNIT_OPTIONS}
                  ariaLabel="Speed limit unit"
                  onchange={onUnitChange}
                  disabled={!limitOn || offline}
                />
              </div>
            </div>
          </div>
          {#if limitError}
            <p class="error-text" role="alert">{limitError}</p>
          {/if}
          <p class="hint" id="{uid}-limit-hint">Applies at once; the global limit in Settings still applies.</p>
          <div class="setting">
            <span class="conns-label">
              <label for="{uid}-conns" class="label">Connections</label>
              <span class="hint" aria-hidden="true">{LIMITS.minConnections}–{LIMITS.maxConnections}</span>
            </span>
            <input
              id="{uid}-conns"
              class="input sm tnum conns"
              type="number"
              min={LIMITS.minConnections}
              max={LIMITS.maxConnections}
              step="1"
              value={connValue}
              oninput={(e) => {
                connValue = e.currentTarget.value;
                applyConnectionsSoon();
              }}
              onkeydown={(e) => onEnter(e, applyConnections)}
              onblur={applyConnections}
              placeholder={defaultConnections > 0 ? `Default (${defaultConnections})` : 'Default'}
              disabled={offline}
              aria-invalid={connError ? 'true' : undefined}
              aria-describedby="{uid}-conns-hint"
              title="Changes a running download as soon as it can, at the latest when it starts again"
            />
          </div>
          {#if connError}
            <p class="error-text" role="alert">{connError}</p>
          {/if}
          <p class="sr-only" id="{uid}-conns-hint">
            {LIMITS.minConnections}–{LIMITS.maxConnections}. Changes a running download as soon as it can, at the latest when it
            starts again.
          </p>
        </div>
      {/if}
    </div>

    {#if actionError}
      <p class="error-text action-error" role="alert"><bdi>{escapeBidi(actionError)}</bdi></p>
    {/if}

    <div class="actions">
      <button
        type="button"
        class="btn ghost details-toggle"
        aria-expanded={expanded}
        aria-controls="{uid}-details"
        onclick={toggleDetails}
      >
        <span class="chevron" class:up={expanded}><Icon name="chevron-down" size={14} /></span>
        {expanded ? 'Hide details' : 'Show details'}
      </button>
      <span class="spacer"></span>
      {#if failed}
        {#if !isRefused(cur)}
          <button type="button" class="btn primary" onclick={retry} disabled={busy} aria-disabled={offline ? 'true' : undefined}>
            <Icon name={needsRestart(cur) ? 'restart' : 'resume'} size={16} />
            {needsRestart(cur) ? 'Start over' : 'Retry'}
          </button>
        {/if}
        <button type="button" class="btn" onclick={closeWindow}>Close</button>
      {:else}
        <button
          type="button"
          class="btn"
          onclick={togglePause}
          disabled={busy || (!canPause && !canResume) || isAssembling(cur)}
          aria-disabled={offline ? 'true' : undefined}
        >
          <Icon name={canPause ? 'pause' : 'resume'} size={16} />
          {canPause ? 'Pause' : 'Resume'}
        </button>
        <button
          type="button"
          class="btn"
          onclick={() => void cancel()}
          disabled={busy}
          title={canPause ? 'Pauses the download and closes this window' : undefined}
        >
          {canPause ? 'Cancel' : 'Close'}
        </button>
      {/if}
    </div>

    {#if expanded}
      <section class="details" id="{uid}-details" aria-labelledby="{uid}-details-title">
        <h2 id="{uid}-details-title" class="caps">Start positions and progress by connection</h2>
        {#if media}
          <ProgressBar {fraction} state={cur.state} label="{pieces.many} downloaded" ease={500} />
        {:else}
          <SegmentMap download={cur} height={18} markers />
        {/if}
        <div class="table-wrap">
          <table class="conns-table">
            <thead>
              <tr>
                <th scope="col" class="num">N.</th>
                <th scope="col" class="num">Downloaded</th>
                <th scope="col" class="num">Speed</th>
                <th scope="col">Info</th>
              </tr>
            </thead>
            <tbody>
              {#each cur.connectionInfo as c, i (i)}
                <tr class="conn-{c.state}">
                  <td class="num">{c.index}</td>
                  <td class="num">{connectionReceived(c)}</td>
                  <td class="num">{c.state === 'receiving' && c.speedBps > 0 ? formatSpeed(c.speedBps) : DASH}</td>
                  <td><span class="info">{@render stateMark(c.state)}<bdi>{escapeBidi(connectionLine(c, media))}</bdi></span></td>
                </tr>
              {:else}
                <tr>
                  <td colspan="4" class="empty">
                    {cur.state === 'downloading'
                      ? isAssembling(cur)
                        ? site
                          ? 'Merging the video and its sound into one MP4 file; no connections.'
                          : 'Assembling the file; no connections.'
                        : 'No connections yet.'
                      : 'No connections while the download is not running.'}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      </section>
    {/if}
  {/if}
</div>

{#if openPrompt && d}
  {@const raw = rawName(d)}
  {@const mode = openPrompt}
  <OpenDialog
    name={displayName(d)}
    executable={isExecutableName(raw)}
    hiddenControls={hasBidiControls(raw)}
    withApp={mode === 'with'}
    onconfirm={() => void (mode === 'with' ? openWithApp() : openFile())}
    onclose={() => (openPrompt = null)}
  />
{/if}

<style>
  .pw {
    display: flex;
    flex-direction: column;
    gap: 10px;
    block-size: 100vh;
    block-size: 100dvh;
    padding-block: 12px;
    padding-inline: var(--sp-7);
    background: var(--surface);
    color: var(--ink);
    font-size: var(--type-cell);
    overflow: hidden;
  }
  .banner {
    flex: none;
    display: flex;
    align-items: center;
    gap: var(--sp-4);
    padding-block: 5px;
    color: var(--warn);
    font-size: var(--type-small);
  }
  .message {
    flex: 1;
    display: grid;
    align-content: center;
    justify-items: start;
    gap: var(--sp-5);
  }
  .message h1 {
    font: 650 var(--type-section) / 1.3 var(--font-display);
    letter-spacing: -0.01em;
  }
  .muted {
    color: var(--muted);
  }

  /* ---- header: tile, name over state, big figure ---- */
  .head {
    flex: none;
    display: flex;
    align-items: center;
    gap: var(--sp-6);
    min-inline-size: 0;
    min-block-size: 44px;
  }
  .head > :global(svg.tile) {
    flex: none;
  }
  .titles {
    display: grid;
    flex: 1 1 auto;
    gap: 3px;
    min-inline-size: 0;
  }
  .name {
    overflow: hidden;
    color: var(--ink);
    font: 650 var(--type-lead) / 1.3 var(--font-display);
    letter-spacing: -0.005em;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .state {
    display: flex;
    align-items: center;
    gap: 6px;
    min-inline-size: 0;
    font-size: var(--type-cell);
    font-weight: 600;
  }
  .state bdi {
    min-inline-size: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .tone-live,
  .tone-done {
    color: var(--accent-text);
  }
  .tone-warn {
    color: var(--warn);
  }
  .tone-danger {
    color: var(--danger);
  }
  .tone-info {
    color: var(--info);
  }
  .tone-quiet {
    color: var(--muted);
  }
  .tone-danger bdi {
    display: -webkit-box;
    -webkit-box-orient: vertical;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    white-space: normal;
    overflow-wrap: anywhere;
  }
  .figure {
    flex: none;
    color: var(--ink);
    font: 650 var(--type-figure) / 1 var(--font-display);
    letter-spacing: -0.02em;
  }
  .figure.warn {
    color: var(--warn);
  }
  .figure.danger {
    color: var(--danger);
  }

  .bar {
    flex: none;
    display: flex;
  }

  /* ---- tabs: a full-width row on its hairline (Tabs) ---- */
  .tabs {
    flex: none;
    min-inline-size: 0;
  }
  /* the window is fitted to the panel's content (fitWindow): it scrolls only in a window the user made smaller */
  .panel {
    flex: 1 1 auto;
    min-block-size: 0;
    overflow: hidden auto;
  }
  .expanded .panel {
    flex: none;
  }
  .rows,
  .facts {
    display: grid;
    grid-template-columns: max-content minmax(0, 1fr) max-content minmax(0, 1fr);
    column-gap: var(--sp-6);
    row-gap: 5px;
    margin: 0;
    align-items: center;
  }
  .facts {
    grid-template-columns: max-content minmax(0, 1fr);
    row-gap: 7px;
  }
  dt {
    color: var(--muted);
    white-space: nowrap;
  }
  dd {
    margin: 0;
    min-inline-size: 0;
    color: var(--ink);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  dd.wide {
    grid-column: 2 / -1;
  }
  /* a full-width row starts on a line of its own */
  dt:has(+ dd.wide) {
    grid-column: 1;
  }
  .facts dd {
    white-space: normal;
    overflow-wrap: anywhere;
  }
  dd.live {
    color: var(--accent-text);
    font-weight: 600;
  }
  .done-at {
    color: var(--muted);
  }
  .avg {
    color: var(--muted);
    font-weight: 400;
  }
  .limit {
    color: var(--accent-text);
  }
  .url-row,
  .path-row {
    display: flex;
    align-items: center;
    gap: var(--sp-2);
  }
  .facts .url-row {
    white-space: nowrap;
  }
  .url {
    display: flex;
    min-inline-size: 0;
    color: var(--ink-2);
    direction: ltr;
    unicode-bidi: isolate;
  }
  .url-head {
    min-inline-size: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .url-tail {
    flex: none;
    white-space: nowrap;
  }
  .path {
    min-inline-size: 0;
    color: var(--ink-2);
    overflow-wrap: anywhere;
  }
  .icon-btn.copy {
    inline-size: 22px;
    block-size: 22px;
    margin-block: -3px;
  }
  .fail-hint {
    margin-block-end: var(--sp-4);
    padding-block: 5px;
    color: var(--ink-2);
    font-size: var(--type-small);
  }

  /* ---- speed limit ---- */
  .limits {
    display: grid;
    gap: var(--sp-3);
    align-content: start;
  }
  .setting {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--sp-6);
    min-block-size: 30px;
  }
  .toggle {
    display: inline-flex;
    align-items: center;
    gap: var(--sp-5);
    color: var(--ink);
    font-weight: 500;
    cursor: pointer;
  }
  .setting .label {
    color: var(--ink);
    font-weight: 500;
  }
  .limit-row {
    display: flex;
    gap: var(--sp-3);
  }
  .limit-row .value {
    inline-size: 5.5rem;
  }
  .unit {
    inline-size: 6.25rem;
  }
  .input.conns {
    inline-size: 9.5rem;
  }
  .limits .hint {
    /* close to the limit it explains; the panel stays no taller than a running download's Status */
    margin-block: -3px -1px;
  }
  .conns-label {
    display: inline-flex;
    align-items: baseline;
    gap: var(--sp-4);
  }
  .conns-label .hint {
    margin: 0;
  }

  /* ---- actions ---- */
  .action-error {
    flex: none;
  }
  .actions {
    flex: none;
    display: flex;
    align-items: center;
    gap: var(--sp-4);
  }
  .spacer {
    flex: 1;
  }
  .details-toggle {
    padding-inline: 6px 9px;
    color: var(--muted);
  }
  .chevron {
    display: grid;
    transition: rotate var(--dur-3) var(--ease-out);
  }
  .chevron.up {
    rotate: 180deg;
  }

  /* ---- details: the map and the connections ---- */
  .details {
    flex: 1 1 auto;
    min-block-size: 0;
    display: flex;
    flex-direction: column;
    gap: var(--sp-5);
    padding-block-start: var(--sp-6);
    border-block-start: 1px solid var(--line);
  }
  .table-wrap {
    flex: 1 1 auto;
    min-block-size: 0;
    overflow: auto;
    border: 1px solid var(--line);
    border-radius: var(--r-md);
    background: var(--surface);
  }
  .conns-table {
    inline-size: 100%;
    border-collapse: collapse;
    font-size: var(--type-small);
  }
  .conns-table th {
    position: sticky;
    inset-block-start: 0;
    block-size: 26px;
    background: var(--surface-2);
    color: var(--muted);
    font-weight: 600;
    text-align: start;
    box-shadow: inset 0 -1px 0 var(--line);
  }
  .conns-table th,
  .conns-table td {
    padding-block: 0;
    padding-inline: var(--sp-5);
    white-space: nowrap;
  }
  .conns-table td {
    block-size: 26px;
    border-block-end: 1px solid var(--line-soft);
    color: var(--ink-2);
  }
  .conns-table tbody tr:last-child td {
    border-block-end: 0;
  }
  .conns-table td:last-child {
    inline-size: 100%;
    white-space: normal;
  }
  .num {
    text-align: end;
    font-variant-numeric: tabular-nums;
    inline-size: 1%;
  }
  .conns-table th.num {
    text-align: end;
  }
  .conn-receiving td:last-child {
    color: var(--ink);
  }
  .conn-retrying td:last-child {
    color: var(--warn);
  }
  .conn-error td:last-child {
    color: var(--danger);
  }
  .conn-done td,
  .conn-idle td {
    color: var(--faint);
  }
  .empty {
    color: var(--muted);
    text-align: center;
    padding-block: var(--sp-6);
  }
  .info {
    display: inline-flex;
    align-items: center;
    gap: var(--sp-3);
  }
  .conns-table tbody tr {
    transition: background-color var(--dur-2) ease;
    animation: fade-in 160ms ease-out;
  }
  .conns-table tbody tr:hover {
    background: var(--hover);
  }

  /* Transient connection glyphs (the settled states use StatusMark, as in the main window). */
  .mark {
    flex: none;
    fill: none;
    stroke: currentColor;
    stroke-width: 1.5;
    stroke-linecap: round;
    stroke-linejoin: round;
    overflow: visible;
  }
  .mark-connecting {
    color: var(--muted);
  }
  .mark-retrying {
    color: var(--warn);
  }
  .mark .spin {
    transform-box: view-box;
    transform-origin: 6px 6px;
    animation: spin 1s linear infinite;
  }

  /* ---- the download complete dialog ---- */
  /* Its natural height (the window is fitted to it, fitWindow); it scrolls only when the
     window is smaller (the user made it so, or the content passes maxHeight), never sideways. */
  .complete {
    flex: 0 1 auto;
    display: flex;
    flex-direction: column;
    gap: var(--sp-7);
    min-block-size: 0;
    overflow: hidden auto;
    animation: rise 220ms cubic-bezier(0.2, 0.8, 0.3, 1);
  }
  .done-head {
    align-items: center;
    gap: 14px;
    min-block-size: 52px;
  }
  .done-tile {
    position: relative;
    display: grid;
    flex: none;
  }
  .badge {
    position: absolute;
    inset-block-end: -3px;
    inset-inline-end: -4px;
    display: grid;
    place-items: center;
    inline-size: 16px;
    block-size: 16px;
    border: 2px solid var(--surface);
    border-radius: 50%;
    box-sizing: content-box;
    background: var(--accent);
    color: var(--accent-on);
    animation: pop 220ms cubic-bezier(0.2, 0.8, 0.3, 1) both;
  }
  .done-head h1 {
    color: var(--accent-text);
    font-size: var(--type-cell);
    font-weight: 600;
  }
  .done-name {
    display: -webkit-box;
    -webkit-box-orient: vertical;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    overflow: hidden;
    color: var(--ink);
    font: 650 var(--type-section) / 1.3 var(--font-display);
    letter-spacing: -0.01em;
    overflow-wrap: anywhere;
    user-select: text;
  }
  .complete .actions {
    flex-wrap: wrap;
    row-gap: var(--sp-3);
    padding-block-start: var(--sp-6);
    border-block-start: 1px solid var(--line);
  }
  .pref {
    display: grid;
    gap: var(--sp-3);
  }
  /* the check draws itself in once the badge has appeared */
  .drawn path {
    fill: none;
    stroke: currentColor;
    stroke-width: 2.4;
    stroke-linecap: round;
    stroke-linejoin: round;
    stroke-dasharray: 1;
    stroke-dashoffset: 1;
    animation: draw 420ms 120ms cubic-bezier(0.3, 0.7, 0.4, 1) forwards;
  }
  @keyframes spin {
    to {
      rotate: 360deg;
    }
  }
  @keyframes draw {
    to {
      stroke-dashoffset: 0;
    }
  }
  @keyframes pop {
    from {
      opacity: 0;
      scale: 0.6;
    }
  }
  @keyframes rise {
    from {
      opacity: 0;
      translate: 0 4px;
    }
  }
  @keyframes fade-in {
    from {
      opacity: 0;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .mark .spin,
    .badge,
    .complete,
    .conns-table tbody tr {
      animation: none;
    }
    .drawn path {
      animation: none;
      stroke-dashoffset: 0;
    }
    .chevron {
      transition: none;
    }
  }
  @media (forced-colors: active) {
    .badge {
      border-color: Canvas;
      forced-color-adjust: none;
      background: Highlight;
      color: HighlightText;
    }
  }
</style>
