// State of the popup's "This page" tab (VIDEO-DETECT-SPEC §5.3; MEDIA-SPEC §6.1): the tab's page
// videos (curated sites, YouTube, embeds), then its streams and direct files grouped per frame
// ("Streams and files", "Embedded players"). A frame showing a page video folds its network
// rows under it as "More sources (n)"; short files sit in "Short clips (n)" at the end. Streams
// Downex refused are marked "Protected", "Live" or "No MP4 video" without Download. Pure: the
// background requests and the DOM rendering are injected, so the behaviour runs under vitest.

import { mediaStorageKey } from './media';
import type { PageVideoEntry, TabMediaResponse } from './messages';
import type { FileEntry, StreamEntry } from './offer-types';
import { fileOrder } from './offer-rows';
import { type AddedResult, errorMessage, failure, type Outcome } from './protocol';
import {
  fileFailureText,
  fileLabel,
  formatLabel,
  type Label,
  mediaName,
  moreSourcesText,
  offerAddedText,
  REFUSAL_LABELS,
  shortClipsText,
  type StreamRefusal,
  streamRefusal,
} from './ui-format';

/** The groups' headings. */
export const GROUP_LABELS = { top: 'Streams and files', embed: 'Embedded players' } as const;

export const MEDIA_EMPTY_TEXT = 'No video seen in this tab yet. Start the video, then open this menu again.';
export const MEDIA_NO_TAB_TEXT = 'Not available for this tab.';

/** A stream row: the popup draws it with a quality picker (VideoPanel over `downex:tab-stream-probe`). */
export interface StreamRowInfo {
  entry: StreamEntry;
  /** "HLS" or "DASH". */
  format: string;
  /** Host and path tail of the manifest. */
  name: string;
  /** Downex refused the stream for good: no Download. */
  refusal?: StreamRefusal;
  /** "Protected", "Live" or "No MP4 video". */
  refusalLabel?: string;
}

/** Per-row state the popup keeps for a file while it is open. */
export interface FileRowState {
  sending?: boolean;
  /** The running hand-off was started elsewhere (another popup, a page's panel). */
  adopted?: boolean;
  result?: Outcome<AddedResult>;
}

export interface FileRowView {
  url: string;
  frame: 'top' | 'embed';
  quality: string;
  detail: string;
  size: string;
  label: string;
  /** Host and file name. */
  title: string;
  /** The file's name ("clip.mp4"): the row's name. */
  name: string;
  /** The file's host ("" when the address has none). */
  host: string;
  /** What the Download button stands for; the popup draws the matching style. */
  state: 'idle' | 'sending' | 'sent' | 'retry';
  button: { text: string; disabled: boolean; label: string };
  result?: Label;
}

export interface PopupGroup {
  frame: 'top' | 'embed';
  label: string;
  /** Folded under the frame's page video: "More sources (n)". */
  fold?: string;
  streams: StreamRowInfo[];
  files: FileRowView[];
}

export interface MediaPanelView {
  videos: PageVideoEntry[];
  groups: PopupGroup[];
  short: FileRowView[];
  /** "Short clips (n)". */
  shortLabel: string;
  /** A hidden-sites entry hides the buttons on the tab's host. */
  hiddenHere: boolean;
  host: string;
  /** Shown instead of rows: nothing seen yet, not a tab, or (error) the background failed. */
  note?: { text: string; error: boolean };
}

/** "cdn.example.com/clip.mp4" → host and name; the name falls back to the whole display. */
function splitDisplay(display: string): { host: string; name: string } {
  const cut = display.indexOf('/');
  if (cut < 0) return { host: '', name: display };
  const rest = display.slice(cut + 1);
  return { host: display.slice(0, cut), name: rest.split('/').pop() || display };
}

export function fileRowView(entry: FileEntry, state: FileRowState = {}): FileRowView {
  const l = fileLabel(entry);
  const where = splitDisplay(l.title);
  const view: FileRowView = {
    url: entry.url,
    frame: entry.frame,
    quality: l.quality,
    detail: l.detail,
    size: l.size,
    label: l.label,
    title: l.title,
    name: entry.filename.trim() || where.name,
    host: where.host,
    state: 'idle',
    button: { text: 'Download', disabled: false, label: `Download ${l.label}` },
  };
  if (state.sending) {
    view.state = 'sending';
    view.button = { ...view.button, text: 'Sending…', disabled: true };
    view.result = { text: 'Sending to Downex…', tone: 'warn' };
  } else if (state.result) {
    if (state.result.ok) {
      // Sent once per popup: a second click would add the same file again.
      view.state = 'sent';
      view.button = { ...view.button, text: 'Sent', disabled: true };
      view.result = { text: offerAddedText(state.result.value), tone: 'ok' };
    } else {
      view.state = 'retry';
      view.button = { ...view.button, text: 'Retry' };
      view.result = { text: fileFailureText(state.result.error), tone: state.result.error.code === 'expired' ? 'warn' : 'error' };
    }
  }
  return view;
}

export function streamRowInfo(entry: StreamEntry): StreamRowInfo {
  const info: StreamRowInfo = { entry, format: formatLabel(entry.format), name: mediaName(entry.url) };
  const probe = entry.probe;
  if (probe && 'result' in probe && !probe.result.ok) {
    const refusal = streamRefusal(probe.result.error);
    if (refusal) {
      info.refusal = refusal;
      info.refusalLabel = REFUSAL_LABELS[refusal];
    }
  }
  return info;
}

/**
 * The tab's network items grouped per frame (§5.3): streams (newest first) and files (largest
 * first) of the top frame, then of embedded players; a frame showing a curated or YouTube page
 * video folds its rows under it; short files of any frame at the end.
 */
export function popupMedia(
  r: Pick<TabMediaResponse, 'items' | 'files' | 'videos'>,
  states: ReadonlyMap<string, FileRowState> = new Map(),
): Pick<MediaPanelView, 'groups' | 'short' | 'shortLabel'> {
  const files = fileOrder(r.files);
  const groups: PopupGroup[] = [];
  for (const frame of ['top', 'embed'] as const) {
    const streams = r.items.filter((s) => s.frame === frame).map(streamRowInfo);
    const own = files.filter((f) => f.frame === frame && !f.short).map((f) => fileRowView(f, states.get(f.url)));
    if (streams.length + own.length === 0) continue;
    const group: PopupGroup = { frame, label: GROUP_LABELS[frame], streams, files: own };
    // Curated or YouTube page video of this frame: its network rows are "More sources".
    if (r.videos.some((v) => v.frame === frame && v.kind !== 'page')) group.fold = moreSourcesText(streams.length + own.length);
    groups.push(group);
  }
  const short = files.filter((f) => f.short).map((f) => fileRowView(f, states.get(f.url)));
  return { groups, short, shortLabel: shortClipsText(short.length) };
}

export interface MediaPanelDeps {
  /** The tab the popup was opened for; undefined when it has none (e.g. a browser page). */
  tabId: number | undefined;
  list: (tabId: number) => Promise<TabMediaResponse>;
  /** `downex:add-file`. */
  addFile: (tabId: number, url: string) => Promise<Outcome<AddedResult>>;
  render: (view: MediaPanelView) => void;
}

/** What the popup shows of a response; a live playlist fetched again only moves `lastSeen`, which is not shown. */
function shown(r: TabMediaResponse | undefined): string {
  if (!r) return '';
  return JSON.stringify([
    r.items.map((e) => [e.url, e.key, e.format, e.frame, e.pageTitle ?? '', e.pageUrl ?? '', e.sending ?? false, e.result ?? null, e.probe ?? null]),
    r.files,
    r.videos,
    r.hiddenHere,
    r.host,
  ]);
}

export class MediaPanel {
  #response: TabMediaResponse | undefined;
  #loaded = false;
  #error: string | undefined;
  readonly #states = new Map<string, FileRowState>();

  constructor(private readonly deps: MediaPanelDeps) {}

  /** The storage key whose changes mean new streams or files for this tab. */
  get storageKey(): string | undefined {
    return this.deps.tabId === undefined ? undefined : mediaStorageKey(this.deps.tabId);
  }

  get response(): TabMediaResponse | undefined {
    return this.#response;
  }

  view(): MediaPanelView {
    const r = this.#response ?? { items: [], files: [], videos: [], hiddenHere: false, host: '' };
    const view: MediaPanelView = { videos: r.videos, hiddenHere: r.hiddenHere, host: r.host, ...popupMedia(r, this.#states) };
    const empty = view.videos.length === 0 && view.groups.length === 0 && view.short.length === 0;
    if (this.#error !== undefined) view.note = { text: `Background unavailable: ${this.#error}`, error: true };
    else if (!empty) return view;
    else if (!this.#loaded && this.deps.tabId !== undefined) view.note = { text: 'Checking the page…', error: false };
    else view.note = { text: this.deps.tabId !== undefined ? MEDIA_EMPTY_TEXT : MEDIA_NO_TAB_TEXT, error: false };
    return view;
  }

  /** (Re)reads the tab's videos, streams and files; renders only when something shown changed. */
  async load(): Promise<void> {
    const tabId = this.deps.tabId;
    if (tabId === undefined) {
      this.#loaded = true;
      this.#render();
      return;
    }
    let r: TabMediaResponse;
    try {
      r = await this.deps.list(tabId);
    } catch (e) {
      this.#error = errorMessage(e);
      this.#render();
      return;
    }
    const hadError = this.#error !== undefined;
    this.#error = undefined;
    let adopted = false;
    for (const f of r.files) if (this.#adopt(f)) adopted = true;
    if (this.#loaded && !hadError && !adopted && shown(r) === shown(this.#response)) return;
    this.#loaded = true;
    this.#response = r;
    this.#render();
  }

  /** The Download button of a file row: one request at a time per file. */
  async downloadFile(url: string): Promise<void> {
    const tabId = this.deps.tabId;
    if (tabId === undefined || this.#states.get(url)?.sending) return;
    this.#states.set(url, { sending: true });
    this.#render();
    let result: Outcome<AddedResult>;
    try {
      result = await this.deps.addFile(tabId, url);
    } catch (e) {
      result = failure('internal', `Background unavailable: ${errorMessage(e)}`);
    }
    this.#states.set(url, { result });
    this.#render();
  }

  /**
   * Takes over what the background knows of a file's hand-off (made from a popup or a panel that
   * closed since) unless this popup's own request is still running; a running one it took over
   * is dropped when the background no longer knows it (Download again).
   */
  #adopt(f: FileEntry): boolean {
    const own = this.#states.get(f.url);
    if (own?.sending && !own.adopted) return false;
    if (!f.add) {
      if (!own?.adopted || !own.sending) return false;
      this.#states.delete(f.url);
      return true;
    }
    if ('running' in f.add) {
      if (own?.sending) return false;
      this.#states.set(f.url, { sending: true, adopted: true });
      return true;
    }
    if (own?.result && JSON.stringify(own.result) === JSON.stringify(f.add.result)) return false;
    this.#states.set(f.url, { result: f.add.result });
    return true;
  }

  #render(): void {
    this.deps.render(this.view());
  }
}
