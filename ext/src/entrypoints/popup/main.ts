// Toolbar popup (BROWSER-SPEC §4; docs/DESIGN-SYSTEM.md §4.11): a status chip for the relay and the
// daemon, then two tabs. "This page": the page's videos as cards with a quality select and Download
// (SITE-MEDIA-SPEC §5.2), the tab's HLS/DASH streams and direct MP4 files per frame (MEDIA-SPEC
// §6.1, VIDEO-DETECT-SPEC §5.3), and the site's switches: "Send downloads to Downex" (the inverse of
// the stored per-site exclusion, REQUIREMENTS B2), "Download buttons on videos" (§5.3) and "Pause
// capture in this tab"; the global capture switch sits in the footer. "Downloads": the recent
// hand-offs with their recorded results, and the capture log. While Downex cannot be reached the
// page's videos give way to a state that says why and how to fix it.

import { browser } from 'wxt/browser';
import { mountBrandMarks } from '../../assets/brand';
import { TRACE_STORAGE_KEY, parseTrace, type TraceEntry } from '../../lib/capture-trace';
import { HISTORY_STORAGE_KEY, parseHistory, type HandoffRecord } from '../../lib/history';
import { type FileRowView, MediaPanel, type MediaPanelView, type PopupGroup, type StreamRowInfo } from '../../lib/media-panel';
import { itemNoun, itemsLeftOutText } from '../../lib/item-strip';
import type { AddMediaMessage, PageVideoEntry, TabVideoAddMessage } from '../../lib/messages';
import { HIDE_TITLE, rescueRow, type FailedRow } from '../../lib/rescue';
import { VIDEO_OPTIONS_KEY, parseVideoOptions, type VideoOptions } from '../../lib/options-form';
import { errorMessage, type ConnectionStatus, type PoliteState } from '../../lib/protocol';
import { RULES_STORAGE_KEY, parseRules, siteHost as hostOfPage, type CaptureRules } from '../../lib/rules';
import { ask } from '../../lib/runtime';
import {
  cleanSiteText,
  connectionLabel,
  daemonLabel,
  fileKind,
  fileKindOfName,
  type Label,
  offerAddedText,
  recordName,
  siteButtonToggle,
  siteSendSwitch,
  sourceLabel,
  streamFailureText,
  videoFailureText,
} from '../../lib/ui-format';
import { MP4_NOTE, VideoPanel, type VideoPanelView } from '../../lib/video-panel';
import {
  addedMark,
  daemonNote,
  durationText,
  FILE_TILE_AUDIO_SMALL,
  FILE_TILES,
  handoffPill,
  handoffTime,
  hostMissing,
  keepUnits,
  listNoteExtra,
  offlineView,
  qualityOptions,
  rulesForSiteSwitch,
  sentence,
  showsNoCookies,
  SMALL_TILE_PX,
  statusChip,
  statusState,
  statusTooltip,
  streamNameParts,
  TILE_VIEWBOX,
  tileKind,
  type TileKind,
  videoSubline,
} from './popup-view';
import { SelectMenu } from './select-menu';

function byId<T extends HTMLElement>(id: string, type: new () => T): T {
  const el = document.getElementById(id);
  if (!(el instanceof type)) throw new Error(`missing #${id}`);
  return el;
}

const statusChipEl = byId('status-chip', HTMLSpanElement);
const statusTextEl = byId('status-text', HTMLSpanElement);
const optionsBtn = byId('open-options', HTMLButtonElement);
const tabPageBtn = byId('tab-page', HTMLButtonElement);
const tabDownloadsBtn = byId('tab-downloads', HTMLButtonElement);
const pageCountEl = byId('page-count', HTMLSpanElement);
const historyCountEl = byId('history-count', HTMLSpanElement);
const panelPageEl = byId('panel-page', HTMLElement);
const panelDownloadsEl = byId('panel-downloads', HTMLElement);
const pageFootEl = byId('page-foot', HTMLElement);

const offlineEl = byId('offline', HTMLDivElement);
const offlineTitleEl = byId('offline-title', HTMLHeadingElement);
const offlineTextEl = byId('offline-text', HTMLParagraphElement);
const offlineCommandEl = byId('offline-command', HTMLDivElement);
const offlineCodeEl = byId('offline-code', HTMLElement);
const offlineDetailEl = byId('offline-detail', HTMLParagraphElement);
const copyCommandBtn = byId('copy-command', HTMLButtonElement);
const checkAgainBtn = byId('check-again', HTMLButtonElement);
const daemonNoteEl = byId('daemon-note', HTMLDivElement);
const daemonNoteTextEl = byId('daemon-note-text', HTMLParagraphElement);
const daemonRetryBtn = byId('daemon-retry', HTMLButtonElement);

const mediaSectionEl = byId('media-section', HTMLDivElement);
const videosHeadEl = byId('videos-head', HTMLDivElement);
const videosHostEl = byId('videos-host', HTMLSpanElement);
const videosEl = byId('videos', HTMLUListElement);
const foldsEl = byId('folds', HTMLDivElement);
const findEl = byId('find-video', HTMLDivElement);
const findBtn = byId('find-video-btn', HTMLButtonElement);
const noCookiesEl = byId('no-cookies', HTMLSpanElement);
const mediaEmptyEl = byId('media-empty', HTMLParagraphElement);
const mediaEl = byId('media', HTMLDivElement);

const siteHostEl = byId('site-host', HTMLSpanElement);
const siteSendEl = byId('site-send', HTMLInputElement);
const siteHintEl = byId('site-hint', HTMLSpanElement);
const siteButtonsEl = byId('site-buttons', HTMLInputElement);
const siteButtonsHintEl = byId('site-buttons-hint', HTMLSpanElement);
const tabPausedEl = byId('tab-paused', HTMLInputElement);
const tabPausedHintEl = byId('tab-paused-hint', HTMLSpanElement);
const captureEl = byId('capture', HTMLInputElement);
const captureHintEl = byId('capture-hint', HTMLSpanElement);

const historyEl = byId('history', HTMLOListElement);
const failedSectionEl = byId('failed-section', HTMLElement);
const failedEl = byId('failed', HTMLOListElement);
const historyEmptyEl = byId('history-empty', HTMLParagraphElement);
const historyScopeEl = byId('history-scope', HTMLSpanElement);
const traceEl = byId('trace', HTMLOListElement);
const traceEmptyEl = byId('trace-empty', HTMLParagraphElement);

const TAB_PAUSED_HINT = tabPausedHintEl.textContent ?? '';
const SVG_NS = 'http://www.w3.org/2000/svg';

mountBrandMarks(document);

/** 24-unit line glyphs drawn for Downex (svg.icon strokes them); static markup only. */
const GLYPHS = {
  awaiting: '<circle cx="12" cy="12" r="8"/><path d="M12 8v4.3l2.8 1.7"/>',
  failed: '<circle cx="12" cy="12" r="8"/><path d="m9.5 9.5 5 5M14.5 9.5l-5 5"/>',
  sent: '<path d="m6.5 12.5 3.5 3.5 7.5-8"/>',
  warn: '<path d="M12 4.5 20.5 19h-17z"/><path d="M12 10v4M12 16.5v.01"/>',
  inbox: '<path d="M12 4v8.5M8.5 9.5 12 13l3.5-3.5"/><path d="M4.5 13.5v3.5a2.5 2.5 0 0 0 2.5 2.5h10a2.5 2.5 0 0 0 2.5-2.5v-3.5"/>',
  copy: '<rect x="8.5" y="8.5" width="11" height="11" rx="2"/><path d="M15.5 8.5V6a1.5 1.5 0 0 0-1.5-1.5H6A1.5 1.5 0 0 0 4.5 6v8A1.5 1.5 0 0 0 6 15.5h2.5"/>',
  play: '<path d="M8 5.9v12.2a.7.7 0 0 0 1.08.59l9.2-6.1a.7.7 0 0 0 0-1.18l-9.2-6.1A.7.7 0 0 0 8 5.9z"/>',
  stream: '<path d="M4.75 5.5h14.5a1 1 0 0 1 1 1v9a1 1 0 0 1-1 1H4.75a1 1 0 0 1-1-1v-9a1 1 0 0 1 1-1zM10.25 8.5v5l4.25-2.5zM4.5 19.5h3M10.5 19.5h3M16.5 19.5h3"/>',
  down: '<path d="M12 5v13.5M6.5 13 12 18.5l5.5-5.5"/>',
  'chev-down': '<path d="m7 10 5 5 5-5"/>',
  'chev-right': '<path d="m10 7 5 5-5 5"/>',
  key: '<circle cx="8" cy="15" r="3.5"/><path d="M10.5 12.5 19 4M15.5 7.5l2 2M13.5 9.5l1.5 1.5"/>',
  refresh: '<path d="M19.25 12a7.25 7.25 0 1 1-2.2-5.2M19.5 4.5v4h-4"/>',
} as const;

type GlyphName = keyof typeof GLYPHS;

function glyph(name: GlyphName, className = ''): SVGSVGElement {
  const svg = document.createElementNS(SVG_NS, 'svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('class', `icon ${className}`.trim());
  svg.setAttribute('aria-hidden', 'true');
  svg.innerHTML = GLYPHS[name];
  return svg;
}

/** A file-type tile (DESIGN-SYSTEM.md §2.6.3): decorative, the name is beside it. */
function fileTile(kind: TileKind, size = 22): SVGSVGElement {
  const svg = document.createElementNS(SVG_NS, 'svg');
  svg.setAttribute('viewBox', TILE_VIEWBOX);
  svg.setAttribute('width', String(size));
  svg.setAttribute('height', String(size));
  svg.setAttribute('class', 'tile');
  svg.setAttribute('aria-hidden', 'true');
  svg.setAttribute('focusable', 'false');
  svg.dataset.kind = kind;
  svg.style.setProperty('--hue', `var(--c-${kind})`);
  svg.style.setProperty('--deep', `var(--c-${kind}-deep)`);
  svg.innerHTML = kind === 'audio' && size <= SMALL_TILE_PX ? FILE_TILE_AUDIO_SMALL : FILE_TILES[kind];
  return svg;
}

function span(className: string, ...content: (Node | string)[]): HTMLSpanElement {
  const el = document.createElement('span');
  el.className = className;
  el.append(...content);
  return el;
}

function spinner(): HTMLSpanElement {
  const s = span('spinner');
  s.setAttribute('aria-hidden', 'true');
  return s;
}

/** Rows already drawn: only new ones fade in. */
const seenHistory = new Set<string>();
const seenMedia = new Set<string>();

function markEnter(seen: Set<string>, key: string, el: HTMLElement): void {
  if (!seen.has(key)) el.classList.add('enter');
  seen.add(key);
}

/** The tab the popup was opened for. */
let activeTab: { id?: number; url?: string } = {};
/** Host the send switch changes; undefined while it is unavailable. */
let siteHost: string | undefined;
/** The page's videos, streams and files; set once the tab is known. */
let mediaPanel: MediaPanel | undefined;
/** The video options as stored (the button switch and the sign-in). */
let videoOptions: VideoOptions = parseVideoOptions(undefined);

function showHint(el: HTMLElement, text: string, tone: Label['tone'] | 'none' = 'none'): void {
  if (el.textContent !== text) el.textContent = text;
  el.classList.remove('tone-ok', 'tone-warn', 'tone-error');
  if (tone !== 'none') el.classList.add(`tone-${tone}`);
}

// --- Tabs: "This page" and "Downloads" (APG tabs, automatic activation) -----------------------

const TABS = [
  { tab: tabPageBtn, panel: panelPageEl },
  { tab: tabDownloadsBtn, panel: panelDownloadsEl },
] as const;

function selectTab(index: number, focus: boolean): void {
  TABS.forEach(({ tab, panel }, i) => {
    const on = i === index;
    tab.setAttribute('aria-selected', String(on));
    tab.tabIndex = on ? 0 : -1;
    panel.hidden = !on;
  });
  // The global capture switch belongs to the page's tab; the Downloads tab ends with the log.
  pageFootEl.hidden = index !== 0;
  if (focus) TABS[index]?.tab.focus();
}

for (const [i, { tab }] of TABS.entries()) {
  tab.addEventListener('click', () => selectTab(i, false));
  tab.addEventListener('keydown', (ev) => {
    const n = TABS.length;
    let next = -1;
    if (ev.key === 'ArrowRight') next = (i + 1) % n;
    else if (ev.key === 'ArrowLeft') next = (i - 1 + n) % n;
    else if (ev.key === 'Home') next = 0;
    else if (ev.key === 'End') next = n - 1;
    if (next < 0) return;
    ev.preventDefault();
    selectTab(next, true);
  });
}

function showCount(el: HTMLElement, n: number): void {
  el.hidden = n === 0;
  if (el.textContent !== String(n)) el.textContent = String(n);
}

// --- Status: the chip, the state that replaces the page's videos, the daemon's note -------------

/** The connection as last drawn (undefined: still checking), and whether it is a protocol mismatch. */
let connectionNow: Label | undefined;
let incompatibleNow = false;
/** The daemon's status as last answered (undefined: being asked), its active downloads and Polite mode's state. */
let daemonNow: Label | undefined;
let activeNow: number | undefined;
let politeNow: PoliteState | undefined;
/** A status request is running: "Check again" waits for it. */
let checking = false;

function showConnection(label: Label, incompatible = false): void {
  connectionNow = label;
  incompatibleNow = incompatible;
  renderStatus();
}

function renderConnection(s: ConnectionStatus): void {
  showConnection(connectionLabel(s), s.state !== 'connected' && s.state !== 'connecting' && s.incompatibleProtocol !== undefined);
}

function renderStatus(): void {
  const state = statusState(connectionNow, incompatibleNow, daemonNow);
  const chip = statusChip(state, { ...(activeNow !== undefined ? { active: activeNow } : {}), hostMissing: hostMissing(connectionNow) });
  statusChipEl.dataset.tone = chip.tone;
  // A live region: unchanged text is not written again (it would be read out again).
  if (statusTextEl.textContent !== chip.text) statusTextEl.textContent = chip.text;
  statusChipEl.title = statusTooltip(connectionNow, daemonNow, politeNow);

  const off = offlineView(state, connectionNow);
  // "Check again" found Downex: the focus goes to the tab instead of a button that is gone.
  if (off === undefined && !offlineEl.hidden && offlineEl.contains(document.activeElement)) tabPageBtn.focus();
  offlineEl.hidden = off === undefined;
  mediaSectionEl.hidden = off !== undefined;
  if (off) {
    offlineEl.dataset.kind = off.kind;
    offlineTitleEl.textContent = off.title;
    offlineTextEl.textContent = off.text;
    offlineCommandEl.hidden = off.command === undefined;
    offlineCodeEl.textContent = off.command ?? '';
    offlineDetailEl.hidden = off.detail === '';
    offlineDetailEl.textContent = off.detail;
  }

  const note = daemonNote(state, daemonNow);
  daemonNoteEl.hidden = note === undefined || off !== undefined;
  if (note) {
    daemonNoteEl.dataset.tone = note.tone;
    if (daemonNoteTextEl.textContent !== note.text) daemonNoteTextEl.textContent = note.text;
    daemonRetryBtn.hidden = !note.retry;
  }
  // aria-disabled, not disabled: a disabled button drops the keyboard focus to <body>, and the
  // hand-off to the tab above needs it still on the button when Downex is found.
  for (const b of [checkAgainBtn, daemonRetryBtn]) {
    if (checking) b.setAttribute('aria-disabled', 'true');
    else b.removeAttribute('aria-disabled');
  }
}

async function loadState(): Promise<void> {
  try {
    const state = await ask({ type: 'downex:get-state' });
    renderConnection(state.connection);
    renderHistory(state.history);
  } catch (e) {
    showConnection({ text: `Background unavailable: ${errorMessage(e)}`, tone: 'error' });
  }
}

async function loadDaemonStatus(): Promise<void> {
  checking = true;
  daemonNow = undefined;
  renderStatus();
  try {
    const o = await ask({ type: 'downex:query-status' });
    daemonNow = daemonLabel(o);
    activeNow = o.ok && o.value.running ? o.value.active : undefined;
    politeNow = o.ok && o.value.running ? o.value.polite : undefined;
  } catch (e) {
    daemonNow = { text: `Daemon status unavailable: ${errorMessage(e)}`, tone: 'error' };
    activeNow = undefined;
    politeNow = undefined;
  } finally {
    checking = false;
  }
  renderStatus();
  // The status request may have (re)connected the relay.
  await loadState();
}

checkAgainBtn.addEventListener('click', () => {
  if (!checking) void loadDaemonStatus();
});
daemonRetryBtn.addEventListener('click', () => {
  if (!checking) void loadDaemonStatus();
});

copyCommandBtn.addEventListener('click', () => {
  const text = offlineCodeEl.textContent ?? '';
  void navigator.clipboard
    .writeText(text)
    .then(() => {
      copyCommandBtn.title = 'Copied';
      copyCommandBtn.setAttribute('aria-label', 'Copied');
      setTimeout(() => {
        copyCommandBtn.title = 'Copy';
        copyCommandBtn.setAttribute('aria-label', 'Copy the command');
      }, 1500);
    })
    .catch(() => undefined);
});

optionsBtn.addEventListener('click', () => {
  void browser.runtime.openOptionsPage().then(() => window.close(), () => undefined);
});

// --- Downloads tab: recent hand-offs and the capture log ---------------------------------------

function renderHistory(list: readonly HandoffRecord[]): void {
  historyEmptyEl.hidden = list.length > 0;
  showCount(historyCountEl, list.length);
  const now = Date.now();
  historyEl.replaceChildren(
    ...list.map((rec) => {
      const li = document.createElement('li');
      li.className = 'hrow';
      markEnter(seenHistory, `${rec.ts}|${rec.url}`, li);

      const names = document.createElement('div');
      names.className = 'names';
      const name = span('fname clamp', recordName(rec));
      name.title = rec.url;
      const pill = handoffPill(rec);
      const p = span(`pill ${pill.tone}`, pill.text);
      p.title = pill.title;
      const time = document.createElement('time');
      time.dateTime = new Date(rec.ts).toISOString();
      time.textContent = handoffTime(rec.ts, now);
      const meta = span('hmeta', p, span('when', time, ` · ${sourceLabel(rec.source)}`));
      names.append(name, meta);
      if (pill.note) {
        const note = span('hnote', pill.note);
        note.title = pill.note;
        names.append(note);
      }
      li.append(fileTile(tileKind(fileKind(rec))), names);
      return li;
    }),
  );
}

// --- Downloads tab: downloads that failed in the browser (BROWSER-SPEC §6) -------------------

/** The rows last shown; a row the user acted on shows its flow's state until the answer. */
let failedRows: FailedRow[] = [];
let failedTimer: ReturnType<typeof setTimeout> | undefined;

function renderFailed(rows: readonly FailedRow[]): void {
  failedRows = [...rows];
  failedSectionEl.hidden = rows.length === 0;
  failedEl.replaceChildren(
    ...rows.map((row) => {
      const view = rescueRow(row);
      const li = document.createElement('li');
      li.className = 'hrow';
      const names = document.createElement('div');
      names.className = 'names';
      const name = span('fname clamp', row.name);
      name.title = row.name;
      const line = span(`hline tone-${view.tone}`, view.line);
      line.setAttribute('role', 'status');
      names.append(name, line);
      const acts = span('hacts');
      if (view.button) {
        const b = document.createElement('button');
        b.type = 'button';
        b.className = 'btn sm primary';
        b.textContent = view.button;
        b.disabled = view.busy || view.action === 'none';
        b.addEventListener('click', () => void finishFailed(row, view.action === 'from-start'));
        acts.append(b);
      }
      if (row.state?.phase !== 'checking' && row.state?.phase !== 'done') {
        const hide = document.createElement('button');
        hide.type = 'button';
        hide.className = 'icon-btn sm';
        hide.title = HIDE_TITLE;
        hide.setAttribute('aria-label', `${HIDE_TITLE}: ${row.name}`);
        hide.textContent = '×';
        hide.addEventListener('click', () => {
          void ask({ type: 'downex:hide-failed', downloadId: row.downloadId }).then(loadFailed, loadFailed);
        });
        acts.append(hide);
      }
      if (acts.childElementCount > 0) names.append(acts);
      li.append(fileTile(tileKind(fileKindOfName(row.name))), names);
      return li;
    }),
  );
  // A finished row leaves the card after a few seconds.
  if (failedTimer !== undefined) clearTimeout(failedTimer);
  failedTimer = rows.some((r) => r.state?.phase === 'done') ? setTimeout(() => void loadFailed(), 5_500) : undefined;
}

async function loadFailed(): Promise<void> {
  try {
    const r = await ask({ type: 'downex:failed-downloads' });
    renderFailed(r.available ? r.items : []);
  } catch {
    renderFailed([]);
  }
}

async function finishFailed(row: FailedRow, fromStart: boolean): Promise<void> {
  renderFailed(failedRows.map((r) => (r.downloadId === row.downloadId ? { ...r, state: { phase: 'checking' } } : r)));
  try {
    const r = await ask({ type: 'downex:finish-download', downloadId: row.downloadId, ...(fromStart ? { fromStart } : {}) });
    if (r.row) renderFailed(failedRows.map((x) => (x.downloadId === row.downloadId ? r.row! : x)));
  } catch {
    // The background stopped mid-request: the reload below shows the row's real state.
  } finally {
    await loadFailed();
  }
}

// The browser's own list changed (a download failed, resumed or was removed).
browser.downloads?.onChanged.addListener((delta) => {
  if (delta.state || delta.paused || delta.error) void loadFailed();
});
browser.downloads?.onErased.addListener(() => void loadFailed());

/** A capture log step's time, to the second (diagnostics). */
function traceTime(ts: number): string {
  const d = new Date(ts);
  const sameDay = d.toDateString() === new Date().toDateString();
  return sameDay
    ? d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' })
    : d.toLocaleString(undefined, { dateStyle: 'short', timeStyle: 'medium' });
}

/** The capture log: newest first, one line per step (BROWSER-SPEC §4). */
function renderTrace(list: readonly TraceEntry[]): void {
  traceEmptyEl.hidden = list.length > 0;
  traceEl.replaceChildren(
    ...list.map((e) => {
      const li = document.createElement('li');
      const time = span('meta', `${traceTime(e.ts)} · ${e.step}${e.downloadId !== undefined ? ` #${e.downloadId}` : ''}`);
      const detail = span('detail', e.detail);
      const url = span('url mono', e.url);
      li.append(time, detail, url);
      return li;
    }),
  );
}

async function loadTrace(): Promise<void> {
  const area = browser.storage.session ?? browser.storage.local;
  try {
    const got = await area.get(TRACE_STORAGE_KEY);
    renderTrace(parseTrace(got[TRACE_STORAGE_KEY]));
  } catch {
    renderTrace([]);
  }
}

// --- The site's switches and the global capture switch ------------------------------------

function renderCaptureHint(): void {
  captureHintEl.dataset.state = captureEl.checked ? 'on' : 'off';
}

function renderSiteHost(): void {
  const host = siteHost ?? (mediaPanel?.response?.host || hostOfPage(activeTab.url));
  siteHostEl.textContent = host || 'this page';
}

function renderRules(rules: CaptureRules): void {
  captureEl.checked = rules.enabled;
  renderCaptureHint();
  const t = siteSendSwitch(rules, activeTab.url);
  siteHost = t.host;
  siteSendEl.checked = t.checked;
  siteSendEl.disabled = t.disabled;
  showHint(siteHintEl, t.hint);
  renderSiteHost();
}

/** "Download buttons on videos": the tab's host, locked when a wildcard entry hides it. */
function renderSiteButtons(): void {
  const host = mediaPanel?.response?.host || hostOfPage(activeTab.url);
  const t = siteButtonToggle(videoOptions, host);
  siteButtonsEl.checked = t.checked;
  siteButtonsEl.disabled = t.disabled || activeTab.id === undefined;
  showHint(siteButtonsHintEl, t.hint);
  renderSiteHost();
}

async function loadVideoOptions(): Promise<void> {
  try {
    const got = await browser.storage.local.get(VIDEO_OPTIONS_KEY);
    videoOptions = parseVideoOptions(got[VIDEO_OPTIONS_KEY]);
  } catch {
    videoOptions = parseVideoOptions(undefined);
  }
  renderSiteButtons();
  updateFind();
}

async function loadActiveTab(): Promise<void> {
  try {
    const [tab] = await browser.tabs.query({ active: true, currentWindow: true });
    activeTab = {
      ...(tab?.id !== undefined && tab.id >= 0 ? { id: tab.id } : {}),
      ...(tab?.url ? { url: tab.url } : {}),
    };
  } catch {
    activeTab = {};
  }
}

async function loadRules(): Promise<void> {
  try {
    const got = await browser.storage.local.get(RULES_STORAGE_KEY);
    renderRules(parseRules(got[RULES_STORAGE_KEY]));
  } catch (e) {
    showHint(siteHintEl, `Could not read the rules: ${errorMessage(e)}`, 'error');
  }
}

async function loadTabPause(): Promise<void> {
  const tabId = activeTab.id;
  if (tabId === undefined) {
    showHint(tabPausedHintEl, 'Not available for this tab.');
    return;
  }
  try {
    tabPausedEl.checked = (await ask({ type: 'downex:tab-state', tabId })).paused;
    tabPausedEl.disabled = false;
  } catch (e) {
    showHint(tabPausedHintEl, `Background unavailable: ${errorMessage(e)}`, 'error');
  }
}

captureEl.addEventListener('change', () => {
  const enabled = captureEl.checked;
  renderCaptureHint();
  void (async () => {
    // Read the latest rules so edits made elsewhere are kept.
    const got = await browser.storage.local.get(RULES_STORAGE_KEY);
    await browser.storage.local.set({ [RULES_STORAGE_KEY]: { ...parseRules(got[RULES_STORAGE_KEY]), enabled } });
  })().catch((e: unknown) => {
    captureEl.checked = !enabled;
    captureHintEl.dataset.state = 'error';
    const err = captureHintEl.querySelector('.err-note') ?? captureHintEl.appendChild(span('err-note tone-error'));
    err.textContent = `Could not save: ${errorMessage(e)}`;
  });
});

siteSendEl.addEventListener('change', () => {
  const host = siteHost;
  const send = siteSendEl.checked;
  if (host === undefined) return;
  void (async () => {
    // Read the latest rules so edits made elsewhere are kept; storage.onChanged re-renders.
    const got = await browser.storage.local.get(RULES_STORAGE_KEY);
    await browser.storage.local.set({ [RULES_STORAGE_KEY]: rulesForSiteSwitch(parseRules(got[RULES_STORAGE_KEY]), host, send) });
  })().catch((e: unknown) => {
    siteSendEl.checked = !send;
    showHint(siteHintEl, `Could not save: ${errorMessage(e)}`, 'error');
  });
});

siteButtonsEl.addEventListener('change', () => {
  const tabId = activeTab.id;
  const show = siteButtonsEl.checked;
  if (tabId === undefined) return;
  siteButtonsEl.disabled = true;
  // The background changes the stored hidden sites; storage.onChanged draws the switch again.
  void ask({ type: 'downex:set-site-hidden', tabId, hidden: !show })
    .then((r) => {
      siteButtonsEl.checked = !r.hidden;
    })
    .catch((e: unknown) => {
      siteButtonsEl.checked = !show;
      showHint(siteButtonsHintEl, `Could not change: ${errorMessage(e)}`, 'error');
    })
    .finally(() => {
      siteButtonsEl.disabled = false;
      void loadVideoOptions();
    });
});

tabPausedEl.addEventListener('change', () => {
  const tabId = activeTab.id;
  const paused = tabPausedEl.checked;
  if (tabId === undefined) return;
  tabPausedEl.disabled = true;
  void ask({ type: 'downex:set-tab-paused', tabId, paused })
    .then((r) => {
      tabPausedEl.checked = r.paused;
      showHint(tabPausedHintEl, TAB_PAUSED_HINT);
    })
    .catch((e: unknown) => {
      tabPausedEl.checked = !paused;
      showHint(tabPausedHintEl, `Could not change: ${errorMessage(e)}`, 'error');
    })
    .finally(() => {
      tabPausedEl.disabled = false;
    });
});

browser.storage.onChanged.addListener((changes, area) => {
  const rules = changes[RULES_STORAGE_KEY];
  if (area === 'local' && rules) renderRules(parseRules(rules.newValue));
  const history = changes[HISTORY_STORAGE_KEY];
  if (history && (area === 'session' || area === 'local')) {
    renderHistory(parseHistory(history.newValue));
    // A hand-off answered (perhaps one sent from an earlier popup or a page's panel): its row
    // shows the answer.
    void mediaPanel?.load();
  }
  const video = changes[VIDEO_OPTIONS_KEY];
  if (area === 'local' && video) {
    videoOptions = parseVideoOptions(video.newValue);
    renderSiteButtons();
    updateFind();
  }
  const trace = changes[TRACE_STORAGE_KEY];
  if (trace && (area === 'session' || area === 'local')) renderTrace(parseTrace(trace.newValue));
  // Streams and files found while the popup is open (read through the background, without headers).
  const mediaKey = mediaPanel?.storageKey;
  if (mediaKey && changes[mediaKey] && (area === 'session' || area === 'local')) void mediaPanel?.load();
});

// --- This page: videos, streams and files (SITE-MEDIA-SPEC §5.2, VIDEO-DETECT-SPEC §5.3) --------

function renderMedia(view: MediaPanelView): void {
  delete mediaSectionEl.dataset.scanning;
  const tabId = activeTab.id;
  if (tabId !== undefined) renderVideos(tabId, view.videos);
  renderGroups(view);
  // With a page video listed (also one found in this popup), "nothing seen yet" says nothing useful.
  mediaEmptyEl.hidden = view.note === undefined || (!view.note.error && videoRows.size > 0);
  if (view.note) showHint(mediaEmptyEl, view.note.text, view.note.error ? 'error' : 'none');
  videosHostEl.textContent = view.host;
  renderSiteButtons();
  const items = view.groups.reduce((n, g) => n + g.streams.length + g.files.length, 0) + view.short.length;
  showCount(pageCountEl, videoRows.size + items);
}

async function loadMedia(): Promise<void> {
  mediaPanel = new MediaPanel({
    tabId: activeTab.id,
    list: (id) => ask({ type: 'downex:tab-media', tabId: id }),
    addFile: (id, url) => ask({ type: 'downex:add-file', tabId: id, url }),
    render: renderMedia,
  });
  await mediaPanel.load();
}

findBtn.addEventListener('click', () => void findVideo());

void loadActiveTab().then(() => Promise.all([loadRules(), loadTabPause(), loadVideoOptions(), loadMedia()]));
void loadState().then(loadDaemonStatus);
void loadTrace();
void loadFailed();
historyScopeEl.textContent = browser.storage.session ? 'This session' : '';

// A page video is a card: a poster (never the page's own image), its title on two lines, the
// site's line, the quality select (it gets the qualities when it is opened, as "Choose quality"
// did) and Download. A stream is a row with its Download and "Choose quality", which opens the same
// select under it. A stream Downex refused shows "Protected", "Live" or "No MP4 video" instead.

interface VideoRowDom {
  li: HTMLLIElement;
  name: HTMLSpanElement;
  sub: HTMLSpanElement;
  /** Cards only: the poster's duration. */
  duration?: HTMLSpanElement;
  /** Streams only: the format tag and the refusal pill. */
  tag?: HTMLSpanElement;
  refusal?: HTMLSpanElement;
  quality: SelectMenu;
  audio: SelectMenu;
  audioRow: HTMLDivElement;
  /** Cards only: the video of a page with several (SITE-MEDIA-SPEC §3.12) and the note on its other items. */
  items?: SelectMenu;
  itemsRow?: HTMLDivElement;
  /** The items row's label: "Video", or "Track" on an audio page. */
  itemsLabel?: HTMLElement;
  itemsNote?: HTMLSpanElement;
  /** Streams only: "Choose quality" and the part it opens. */
  choose?: HTMLButtonElement;
  picker?: HTMLDivElement;
  status: HTMLSpanElement;
  /** The status's text (announced) and the seconds spent getting the info (not announced). */
  statusText: HTMLSpanElement;
  statusElapsed: HTMLSpanElement;
  action: HTMLButtonElement;
  download: HTMLButtonElement;
}

/** What a row is about: a page video or a stream. */
interface RowSubject {
  /** Map key: the page video's key, or `stream:<manifest URL>`. */
  key: string;
  /** The source: the site's name, "HLS" or "DASH". */
  tag: string;
  /** Shown until the video's own title is known. */
  title: string;
  /** Tooltip of the title: the address. */
  tooltip: string;
  /** Page videos: an embedded player. */
  embedded: boolean;
  /** Streams: the manifest's host. */
  host: string;
  /** A stream Downex refused: its mark. */
  refusal?: string;
  stream: boolean;
}

interface VideoRow {
  subject: RowSubject;
  panel: VideoPanel;
  view: VideoPanelView;
  /** The user asked for the qualities (opened the select, or a stream's "Choose quality"). */
  expanded: boolean;
  /** Added by "Find the video on this page" in this popup (the background lists it once probed). */
  found: boolean;
  dom: VideoRowDom;
}

/** Page video rows by key, in the order drawn … */
const videoRows = new Map<string, VideoRow>();
/** … and stream rows by `stream:<url>`. */
const streamRows = new Map<string, VideoRow>();
let rowIds = 0;

/** The user asked for a row's qualities: Downex gets them now (VIDEO-DETECT-SPEC §6.1 4). */
function requestQualities(rows: Map<string, VideoRow>, key: string): void {
  const row = rows.get(key);
  if (!row) return;
  row.expanded = true;
  row.panel.open();
  drawVideoRow(row);
}

function videoRowDom(key: string, rows: Map<string, VideoRow>, stream: boolean): VideoRowDom {
  const id = ++rowIds;
  const li = document.createElement('li');
  li.className = stream ? 'frow stream' : 'vcard';
  markEnter(seenMedia, `video:${key}`, li);

  const name = span(stream ? 'fname-text clamp' : 'vtitle clamp');
  name.id = `row-${id}-name`;
  const sub = span(stream ? 'fsub' : 'vsub');
  const quality = new SelectMenu({
    id: `row-${id}-quality`,
    ariaLabel: 'Quality',
    ariaDescribedby: name.id,
    placeholder: 'Default quality',
    className: stream ? 'grow' : 'grow sm-26',
    onChange: (v) => rows.get(key)?.panel.chooseVideo(v),
    onRequest: () => requestQualities(rows, key),
  });
  const audio = new SelectMenu({
    id: `row-${id}-audio`,
    ariaLabel: 'Audio track',
    ariaDescribedby: name.id,
    className: stream ? 'grow' : 'grow sm-26',
    onChange: (v) => rows.get(key)?.panel.chooseAudio(v),
  });
  const audioRow = document.createElement('div');
  audioRow.className = 'audio-row';
  audioRow.hidden = true;
  audioRow.append(span('pick-label', 'Audio'), audio.root);

  const status = span('result');
  status.setAttribute('role', 'status');
  const statusText = document.createElement('span');
  const statusElapsed = span('elapsed');
  statusElapsed.setAttribute('aria-hidden', 'true');
  const action = document.createElement('button');
  action.type = 'button';
  action.className = 'btn link link-action';
  const download = document.createElement('button');
  download.type = 'button';

  const dom: VideoRowDom = { li, name, sub, quality, audio, audioRow, status, statusText, statusElapsed, action, download };
  if (stream) {
    const tag = span('tag', '');
    const refusal = span('pill warn refusal');
    refusal.hidden = true;
    const choose = document.createElement('button');
    choose.type = 'button';
    choose.className = 'icon-btn choose';
    choose.setAttribute('aria-expanded', 'false');
    choose.append(glyph('chev-down'));
    const picker = document.createElement('div');
    picker.className = 'picker';
    picker.hidden = true;
    picker.append(quality.root, audioRow);
    const main = document.createElement('div');
    main.className = 'row-main';
    const names = document.createElement('div');
    names.className = 'names';
    names.append(span('fname', name, tag), sub);
    main.append(fileTile('video'), names, refusal, choose, download);
    li.append(main, picker, status, action);
    choose.addEventListener('click', () => toggleStreamRow(rows, key));
    Object.assign(dom, { tag, refusal, choose, picker });
  } else {
    const duration = span('dur');
    duration.hidden = true;
    const poster = span('poster', span('disc', glyph('play', 'fill')), duration);
    poster.setAttribute('aria-hidden', 'true');
    const head = document.createElement('div');
    head.className = 'vhead';
    head.append(name, sub);
    const act = document.createElement('div');
    act.className = 'vact';
    act.append(quality.root, download);
    // A page with several videos: which one (VIDEO-DETECT-SPEC §5.2 "Several videos").
    const items = new SelectMenu({
      id: `row-${id}-item`,
      ariaLabel: 'Video of this page',
      ariaDescribedby: name.id,
      className: 'grow sm-26',
      onChange: (v) => rows.get(key)?.panel.chooseItem(v),
    });
    const itemsRow = document.createElement('div');
    itemsRow.className = 'audio-row items-row';
    itemsRow.hidden = true;
    const itemsLabel = span('pick-label', 'Video');
    itemsRow.append(itemsLabel, items.root);
    const itemsNote = span('vsub items-note');
    itemsNote.hidden = true;
    li.append(poster, head, itemsRow, itemsNote, act, audioRow, status, action);
    dom.duration = duration;
    Object.assign(dom, { items, itemsRow, itemsNote, itemsLabel });
  }

  download.addEventListener('click', () => void rows.get(key)?.panel.download());
  action.addEventListener('click', () => rows.get(key)?.panel.retry());
  return dom;
}

function drawStatus(row: VideoRow): void {
  const { dom, view, subject } = row;
  // The status shows what concerns the row: getting the info, sending, sent and refusals; the
  // list's own note ("Every quality is saved as one MP4 file.") is said by the select, and what
  // the site's answer adds to it (the sign-in used, the browser fetch) is shown as a note.
  const shown = row.expanded || !subject.stream || view.phase === 'sending' || view.phase === 'sent' || view.phase === 'error';
  const listNote = view.phase === 'list' && view.status?.tone === 'ok';
  const extra = listNote && view.status ? listNoteExtra(view.status.text, MP4_NOTE) : '';
  const status = !shown ? undefined : extra ? { text: extra } : listNote ? undefined : view.status;
  dom.status.hidden = status === undefined;
  if (status) {
    // Work in progress spins (neutral); an accepted hand-off is marked as in the history
    // (saved at once: mint tray; waiting for confirmation: blue clock); a list's note has no mark.
    let tone: string = 'tone' in status ? status.tone : 'info';
    let markKind: GlyphName | 'spinner' | 'note';
    if (!('tone' in status)) markKind = 'note';
    else if (view.phase === 'probing' || view.phase === 'sending') markKind = 'spinner';
    else if (view.phase === 'sent' && status.tone === 'ok') ({ tone, glyph: markKind } = addedMark(status.text));
    else markKind = status.tone === 'ok' ? 'sent' : status.tone === 'warn' ? 'warn' : 'failed';
    dom.status.className = `result tone-${tone}`;
    // The live line changes only when its text or mark does: the seconds of "Getting video
    // info…" are beside it, shown but not announced every second.
    if (dom.status.dataset.mark !== markKind || dom.status.firstChild === null) {
      dom.status.dataset.mark = markKind;
      const mark = markKind === 'note' ? [] : [markKind === 'spinner' ? spinner() : glyph(markKind)];
      dom.status.replaceChildren(...mark, dom.statusText, dom.statusElapsed);
    }
    const text = sentence(status.text);
    if (dom.statusText.textContent !== text) dom.statusText.textContent = text;
    const elapsed = 'elapsed' in status && status.elapsed ? ` ${status.elapsed}` : '';
    if (dom.statusElapsed.textContent !== elapsed) dom.statusElapsed.textContent = elapsed;
  }
  dom.action.hidden = view.action === undefined;
  const actionKey = view.action ? `${view.action.kind}\n${view.action.text}` : '';
  if (dom.action.dataset.key !== actionKey) {
    dom.action.dataset.key = actionKey;
    dom.action.replaceChildren(...(view.action ? [glyph('refresh'), view.action.text] : []));
  }
}

function drawDownload(row: VideoRow, title: string): void {
  const { dom, view, subject } = row;
  const b = dom.download;
  const st = view.phase === 'sending' ? 'sending' : view.phase === 'sent' ? 'sent' : 'idle';
  const listed = view.rows.length > 0;
  const quality = (row.expanded || !subject.stream) && listed ? view.rows.find((r) => r.selected)?.label : undefined;
  b.setAttribute('aria-label', quality ? `Download ${title}, ${quality}` : `Download ${title} in the default quality`);
  b.disabled = view.busy || view.phase === 'sent';
  if (b.dataset.st === st && b.firstChild) return;
  b.dataset.st = st;
  if (subject.stream) {
    // An icon button: its name says what it sends, its tooltip what it does.
    b.className = 'icon-btn dl';
    b.title = st === 'sending' ? 'Sending…' : st === 'sent' ? 'Sent' : 'Download';
    b.replaceChildren(st === 'sending' ? spinner() : glyph(st === 'sent' ? 'sent' : 'down'));
    return;
  }
  b.removeAttribute('title');
  b.className = st === 'idle' ? 'btn sm primary dl' : 'btn sm dl';
  if (st === 'sending') b.replaceChildren(spinner(), 'Sending…');
  else if (st === 'sent') b.replaceChildren(glyph('sent'), 'Sent');
  else b.replaceChildren(glyph('down'), 'Download');
}

function drawVideoRow(row: VideoRow): void {
  const { dom, view, subject } = row;
  dom.li.dataset.state = subject.refusal ? 'refused' : view.phase;
  const title = view.title || subject.title;
  if (dom.name.textContent !== title) dom.name.textContent = title;
  dom.name.title = subject.tooltip;

  if (subject.stream) {
    dom.tag!.textContent = subject.tag;
    const qualities = view.rows.length;
    const best = view.rows[0]?.quality;
    const facts = qualities > 0 ? [`${qualities} ${qualities === 1 ? 'quality' : 'qualities'}`, best ? `best ${best}` : ''] : [subject.host];
    dom.sub.textContent = ['Stream', ...facts].filter((p) => p !== '').join(' · ');
  } else {
    const best = view.rows[0]?.audio ? '' : (view.rows[0]?.quality ?? '');
    dom.sub.textContent = videoSubline(subject.tag, subject.embedded, best, view.fileType);
    const dur = durationText(row.panel.info?.durationMs ?? 0);
    dom.duration!.hidden = dur === '';
    dom.duration!.textContent = dur;
  }

  if (dom.items && dom.itemsRow && dom.itemsNote) {
    const items = view.items;
    dom.itemsRow.hidden = !items || items.options.length < 2;
    if (items && items.options.length > 1) {
      dom.items.setOptions(
        items.options.map((it, i) => {
          const o: { value: string; label: string; detail?: string; meta?: string; title?: string } = { value: it.id, label: `${i + 1} of ${items.options.length}` };
          const t = cleanSiteText(it.title.trim());
          if (t) {
            o.detail = t;
            o.title = t;
          }
          if (it.durationMs > 0) o.meta = durationText(it.durationMs);
          return o;
        }),
        items.options[items.index]?.id ?? '',
      );
      dom.items.setDisabled(view.busy || view.phase === 'sent');
    }
    const note = items ? itemsLeftOutText(items.leftOut, items.audio) : '';
    const noun = itemNoun(items?.audio);
    if (dom.itemsLabel && dom.itemsLabel.textContent !== noun) dom.itemsLabel.textContent = noun;
    dom.itemsNote.hidden = note === '';
    if (dom.itemsNote.textContent !== note) dom.itemsNote.textContent = note;
  }

  if (subject.refusal) {
    // Downex refused this stream for good: its mark, no picker and no Download.
    dom.refusal!.hidden = false;
    dom.refusal!.textContent = subject.refusal;
    dom.choose!.hidden = true;
    dom.picker!.hidden = true;
    dom.download.hidden = true;
    dom.status.hidden = true;
    dom.action.hidden = true;
    return;
  }
  if (dom.refusal) dom.refusal.hidden = true;
  if (dom.choose) dom.choose.hidden = false;
  dom.download.hidden = false;

  const listed = view.rows.length > 0;
  // The standard names, one per class ("4K (2160p)", then "AV1 · 1.9 GB"); the picture as tooltip.
  // An audio page's rows are its audio tracks (SITE-MEDIA-SPEC §3.13): the pick's audio is the chosen row.
  const chosen = view.rows[0]?.audio ? view.pick.audio : view.pick.video;
  dom.quality.setOptions(listed ? qualityOptions(view.rows) : [], listed ? chosen : '');
  dom.quality.setBusy(view.phase === 'probing');
  dom.quality.setDisabled(view.phase === 'sending' || view.phase === 'sent');
  dom.audioRow.hidden = view.audio === undefined;
  if (view.audio) {
    dom.audio.setOptions(
      view.audio.options.map((o) => ({ value: o.id, label: keepUnits(o.label) })),
      view.audio.value,
    );
    dom.audio.setDisabled(view.busy || view.phase === 'sent');
  }
  if (dom.picker && dom.choose) {
    dom.picker.hidden = !row.expanded;
    const expanded = String(row.expanded);
    if (dom.choose.getAttribute('aria-expanded') !== expanded) dom.choose.setAttribute('aria-expanded', expanded);
    dom.choose.setAttribute('aria-label', `Choose quality for ${title}`);
    dom.choose.title = row.expanded ? 'Hide qualities' : 'Choose quality';
  }
  drawStatus(row);
  drawDownload(row, title);
}

function toggleStreamRow(rows: Map<string, VideoRow>, key: string): void {
  const row = rows.get(key);
  if (!row) return;
  row.expanded = !row.expanded;
  if (row.expanded) row.panel.open();
  else {
    row.dom.quality.close(false);
    row.panel.close();
  }
  drawVideoRow(row);
  if (row.expanded) row.dom.quality.trigger.focus();
}

function pageSubject(entry: PageVideoEntry): RowSubject {
  return {
    key: entry.key,
    tag: entry.site,
    title: entry.title || (entry.frame === 'embed' ? 'Embedded video' : 'Video on this page'),
    tooltip: entry.pageUrl,
    embedded: entry.frame === 'embed',
    host: '',
    stream: false,
  };
}

function streamSubject(s: StreamRowInfo): RowSubject {
  const parts = streamNameParts(s.name);
  const subject: RowSubject = {
    key: `stream:${s.entry.url}`,
    tag: s.format,
    title: parts.name,
    tooltip: s.entry.url,
    embedded: false,
    host: parts.host,
    stream: true,
  };
  if (s.refusalLabel) subject.refusal = s.refusalLabel;
  return subject;
}

function newRow(rows: Map<string, VideoRow>, subject: RowSubject, panel: (render: (view: VideoPanelView) => void) => VideoPanel, found: boolean): VideoRow {
  const key = subject.key;
  const p = panel((view) => {
    const r = rows.get(key);
    if (!r) return;
    r.view = view;
    drawVideoRow(r);
  });
  const row = { subject, panel: p, view: p.view(), expanded: false, found } as VideoRow;
  row.dom = videoRowDom(key, rows, subject.stream);
  return row;
}

function dropRow(rows: Map<string, VideoRow>, key: string, row: VideoRow): void {
  row.panel.close();
  row.dom.quality.destroy();
  row.dom.audio.destroy();
  row.dom.items?.destroy();
  rows.delete(key);
}

function newVideoRow(tabId: number, entry: PageVideoEntry, found: boolean): VideoRow {
  const key = entry.key;
  return newRow(
    videoRows,
    pageSubject(entry),
    (render) =>
      new VideoPanel({
        probe: (entryId) => ask(entryId ? { type: 'downex:tab-video-probe', tabId, key, entryId } : { type: 'downex:tab-video-probe', tabId, key }),
        add: (pick, entryId) => {
          const msg: TabVideoAddMessage = { type: 'downex:tab-video-add', tabId, key, videoTrack: pick.video, audioTrack: pick.audio };
          if (entryId) msg.entryId = entryId;
          return ask(msg);
        },
        render,
        recommend: entry.site !== 'YouTube',
      }),
    found,
  );
}

function newStreamRow(tabId: number, s: StreamRowInfo): VideoRow {
  const url = s.entry.url;
  return newRow(
    streamRows,
    streamSubject(s),
    (render) =>
      new VideoPanel({
        probe: () => ask({ type: 'downex:tab-stream-probe', tabId, url }),
        add: (pick) => {
          const msg: AddMediaMessage = { type: 'downex:add-media', tabId, url };
          if (pick.video) msg.videoTrack = pick.video;
          if (pick.audio) msg.audioTrack = pick.audio;
          return ask(msg);
        },
        failureText: streamFailureText,
        addedText: offerAddedText,
        render,
      }),
    false,
  );
}

/** Draws the page's videos; rows already shown keep their select and state. */
function renderVideos(tabId: number, entries: readonly PageVideoEntry[]): void {
  const keys = new Set(entries.map((e) => e.key));
  for (const [key, row] of videoRows) {
    if (!keys.has(key) && !row.found) dropRow(videoRows, key, row);
  }
  const order: VideoRow[] = [];
  for (const entry of entries) {
    let row = videoRows.get(entry.key);
    if (!row) {
      row = newVideoRow(tabId, entry, false);
      videoRows.set(entry.key, row);
    }
    row.subject = pageSubject(entry);
    row.found = false;
    if (entry.probe && 'result' in entry.probe) row.panel.adopt(entry.probe.result);
    if (entry.add) row.panel.adoptAdd(entry.add);
    row.view = row.panel.view();
    order.push(row);
  }
  for (const row of videoRows.values()) if (row.found && !keys.has(row.subject.key)) order.unshift(row);
  for (const row of order) drawVideoRow(row);
  setChildren(
    videosEl,
    order.map((r) => r.dom.li),
  );
  videosHeadEl.hidden = order.length === 0;
  updateFind();
}

/** The stream's row, made once and updated with what the background knows. */
function streamRow(tabId: number, s: StreamRowInfo): VideoRow {
  const key = `stream:${s.entry.url}`;
  let row = streamRows.get(key);
  if (!row) {
    row = newStreamRow(tabId, s);
    streamRows.set(key, row);
  }
  row.subject = streamSubject(s);
  const probe = s.entry.probe;
  if (probe && 'result' in probe) row.panel.adopt(probe.result);
  if (s.entry.sending) row.panel.adoptAdd({ running: true });
  else if (s.entry.result) row.panel.adoptAdd({ result: s.entry.result });
  row.view = row.panel.view();
  drawVideoRow(row);
  return row;
}

/** A direct file's row: its name, what it is, the state of its hand-off and Download (`downex:add-file`). */
function fileRowDom(f: FileRowView): HTMLLIElement {
  const panel = mediaPanel;
  const li = document.createElement('li');
  li.className = 'frow file';
  li.dataset.state = f.state;
  markEnter(seenMedia, `file:${f.url}`, li);

  const names = document.createElement('div');
  names.className = 'names';
  const name = span('fname-text clamp', f.name);
  name.title = f.url;
  const sub = span('fsub', keepUnits([f.label, f.host].filter((p) => p !== '').join(' · ')));
  sub.title = f.url;
  names.append(span('fname', name), sub);

  const button = document.createElement('button');
  button.type = 'button';
  button.dataset.st = f.state;
  button.className = 'icon-btn dl';
  button.title = f.button.text;
  if (f.state === 'idle') button.append(glyph('down'));
  if (f.state === 'retry') button.append(glyph('refresh'));
  if (f.state === 'sending') button.append(spinner());
  if (f.state === 'sent') button.append(glyph('sent'));
  button.disabled = f.button.disabled;
  button.setAttribute('aria-label', f.button.label);
  button.addEventListener('click', () => void panel?.downloadFile(f.url));

  const main = document.createElement('div');
  main.className = 'row-main';
  main.append(fileTile('video'), names, button);
  li.append(main);
  if (f.result) {
    const result = document.createElement('span');
    result.setAttribute('role', 'status');
    let tone: string = f.result.tone;
    let mark: GlyphName | 'spinner';
    if (f.state === 'sending') mark = 'spinner';
    else if (f.result.tone === 'ok') ({ tone, glyph: mark } = addedMark(f.result.text));
    else mark = f.result.tone === 'warn' ? 'warn' : 'failed';
    result.className = `result tone-${tone}`;
    result.dataset.mark = mark;
    result.append(mark === 'spinner' ? spinner() : glyph(mark), span('text', sentence(f.result.text)));
    li.append(result);
  }
  return li;
}

/** Replaces `parent`'s children only when they differ: moving a focused row would blur it. */
function setChildren(parent: Element, items: readonly Element[]): void {
  const current = [...parent.children];
  if (current.length !== items.length || current.some((el, i) => el !== items[i])) parent.replaceChildren(...items);
}

/** Group containers by key, so that an opened fold stays open and rows keep their focus. */
const containers = new Map<string, HTMLElement>();

/** A folded group (`<details>`: "More sources (n)", "Short clips (n)"). */
function fold(key: string, label: string, items: HTMLLIElement[]): HTMLElement {
  let d = containers.get(key);
  if (!d) {
    d = document.createElement('details');
    d.className = 'fold';
    d.dataset.key = key;
    const ul = document.createElement('ul');
    ul.className = 'rows';
    const summary = document.createElement('summary');
    summary.append(glyph('chev-right'), span('label'));
    d.append(summary, ul);
    containers.set(key, d);
  }
  const summaryLabel = d.firstElementChild?.lastElementChild as HTMLElement;
  if (summaryLabel.textContent !== label) summaryLabel.textContent = label;
  setChildren(d.lastElementChild as HTMLUListElement, items);
  return d;
}

/** A frame's group under its heading ("Streams and files", "Embedded players"). */
function section(key: string, label: string, items: HTMLLIElement[]): HTMLElement {
  let el = containers.get(key);
  if (!el) {
    el = document.createElement('section');
    el.className = 'group';
    const head = document.createElement('h3');
    head.className = 'pp-sec';
    const ul = document.createElement('ul');
    ul.className = 'rows';
    el.append(head, ul);
    containers.set(key, el);
  }
  const h = el.firstElementChild as HTMLElement;
  if (h.textContent !== label) h.textContent = label;
  setChildren(el.lastElementChild as HTMLUListElement, items);
  return el;
}

function groupRows(tabId: number, g: PopupGroup): HTMLLIElement[] {
  return [...g.streams.map((s) => streamRow(tabId, s).dom.li), ...g.files.map(fileRowDom)];
}

/** The streams and files per frame: folded under the frame's page video, else under a heading. */
function renderGroups(view: MediaPanelView): void {
  const tabId = activeTab.id;
  if (tabId === undefined) return;
  const shownStreams = new Set(view.groups.flatMap((g) => g.streams.map((s) => `stream:${s.entry.url}`)));
  for (const [key, row] of streamRows) {
    if (!shownStreams.has(key)) dropRow(streamRows, key, row);
  }
  const folded: HTMLElement[] = [];
  const sections: HTMLElement[] = [];
  for (const g of view.groups) {
    const items = groupRows(tabId, g);
    if (g.fold) folded.push(fold(`fold:${g.frame}`, g.fold, items));
    else sections.push(section(`group:${g.frame}`, g.label, items));
  }
  if (view.short.length > 0) sections.push(fold('short', view.shortLabel, view.short.map(fileRowDom)));
  setChildren(foldsEl, folded);
  setChildren(mediaEl, sections);
}

/**
 * "Find the video on this page": shown for an http(s) tab without a recognised page video. Its
 * "No cookies sent" chip shows only while the sign-in option is off (DESIGN-SYSTEM.md §4.11).
 */
function updateFind(): void {
  let web = false;
  try {
    const p = new URL(activeTab.url ?? '').protocol;
    web = p === 'http:' || p === 'https:';
  } catch {
    web = false;
  }
  findEl.hidden = activeTab.id === undefined || !web || videoRows.size > 0;
  noCookiesEl.hidden = !showsNoCookies(videoOptions);
}

async function findVideo(): Promise<void> {
  const tabId = activeTab.id;
  if (tabId === undefined) return;
  findBtn.disabled = true;
  try {
    const r = await ask({ type: 'downex:find-page-video', tabId });
    if (!r.ok) {
      showHint(mediaEmptyEl, videoFailureText(r.error), 'warn');
      mediaEmptyEl.hidden = false;
      return;
    }
    const row = videoRows.get(r.value.key) ?? newVideoRow(tabId, r.value, true);
    videoRows.set(r.value.key, row);
    row.expanded = true;
    videosEl.prepend(row.dom.li);
    videosHeadEl.hidden = false;
    row.panel.open();
    drawVideoRow(row);
    updateFind();
    if (mediaPanel) renderMedia(mediaPanel.view());
    row.dom.quality.trigger.focus();
  } catch (e) {
    showHint(mediaEmptyEl, `Background unavailable: ${errorMessage(e)}`, 'error');
    mediaEmptyEl.hidden = false;
  } finally {
    findBtn.disabled = false;
  }
}
