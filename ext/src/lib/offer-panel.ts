// The panel of a Download button over any video (VIDEO-DETECT-SPEC §2.6, §5.2): one list merged
// from three sources — the site's qualities (yt-dlp, through VideoPanel's state machine), the
// MP4 qualities of the frame's streams (`downex:stream-probe`), and the direct files — with the
// rows of offer-rows.ts. While open it asks for the frame's offers again every 2 s, so a stream
// or file that arrives later appears; rows keep their focus and selection by id. OfferPanel is
// the state (pure: requests, timers and rendering injected); OfferPanelDom draws it with DOM calls
// only (no HTML strings) inside the layer's closed shadow root.

import type { ClipCheck, ClipRange } from './clip';
import type { SiteHiddenResponse } from './messages';
import type { FrameOffers, OfferKey, SiteOffer, StreamOffer } from './offer-types';
import {
  allRows,
  bareAnswer,
  buildOfferRows,
  type OfferRow,
  type OfferRows,
  shortFailure,
  type StreamProbe,
  streamOrder,
  streamRefused,
} from './offer-rows';
import { type PartField, PartRow, PartTrimmer, type PartVideo, type PartView, sizeAt } from './part-row';
import { type AddedResult, errorMessage, failure, type Outcome, type ProtocolError, systemTimers, type Timers, type VideoInfoResult } from './protocol';
import { NETWORK_ONLY_TEXT, NETWORK_PLAYED_TEXT } from './site-video';
import {
  fileFailureText,
  gettingInfoText,
  hideOnText,
  initialCapital,
  mediaFailureText,
  moreSourcesText,
  OFFER_TEXTS,
  offerAddedText,
  otherVideosText,
  PLAYED_SETTLE_MS,
  PLAYED_WAIT_MS,
  siteNote,
  streamFailureText,
  streamRefusal,
  type Tone,
  VIDEO_TEXTS,
} from './ui-format';
import {
  cleanSiteText,
  closeButton,
  glyph,
  type GlyphName,
  goDisc,
  labelled,
  meter,
  noteLine,
  overflowCue,
  setGlyph,
  setText,
  skeleton,
  statusTone,
} from './video-overlay';
import { type ClickedVideo, clickedItem, ITEM_STRIP_CSS, ItemStrip } from './item-strip';
import { MP4_NOTE, VideoPanel, type VideoPanelItems, type VideoPanelView } from './video-panel';
import {
  audioLabel,
  audioTracks,
  clipSize,
  defaultAudio,
  isVideoTrack,
  needsAudio,
  pickSize,
  RECOMMENDED_TEXT,
  sizeLabel,
  trackById,
  type TrackPick,
} from './video-picker';

/** The panel asks for the frame's offers again this often while it is open. */
export const OFFER_POLL_MS = 2000;
/** … and this often while the Played source has not answered (its rows show as soon as it does). */
export const PLAYED_POLL_MS = 250;
/** Streams probed when the panel opens (matched first, then newest) … */
export const MAX_STREAM_PROBES = 4;
/**
 * … and, while every answer so far is a bare media playlist (a level the player loaded after its
 * master), older ones up to this many in all, so that the master is found.
 */
export const MAX_STREAM_PROBES_FOR_MASTER = 12;
/**
 * … and, while no answer has the clicked element's length (a grid of previews whose players all
 * fetched their streams; review 2026-11-18, a video search of 12 tiles and 50 playlists), one stream
 * of each group not asked yet (StreamOffer.group), up to this many in all, so that the clicked
 * video's stream is found; then the other streams of its group (its master).
 */
export const MAX_STREAM_PROBES_FOR_LENGTH = 24;
/** Below this frame width the rows wrap and the codec text is left out … */
export const NARROW_FRAME_PX = 320;
/** … and below this frame height only the default row is shown. */
export const SHORT_FRAME_PX = 200;

/** The clicked element, as checked at click time. */
export interface PanelTarget {
  /** `currentSrc` when http(s) (no fragment), else ''. */
  src: string;
  /** `currentSrc` is `blob:`. */
  mse: boolean;
  drm: boolean;
  live: boolean;
  /** Whether the element has played (read on every render); undefined: not known. */
  played?: () => boolean;
  /**
   * The element's own duration in ms (read on every render; 0 unknown): the Part row's duration
   * when the site or stream gives none (VIDEO-DETECT-SPEC §5.2 "Its duration").
   */
  durationMs?: () => number;
}

export type GroupId = 'more' | 'other';

export interface OfferRowView extends OfferRow {
  /** The default row (focused first). */
  selected: boolean;
}

export interface OfferGroupView {
  id: GroupId;
  label: string;
  expanded: boolean;
  rows: OfferRowView[];
}

export type OfferPanelPhase = 'loading' | 'ready' | 'sending' | 'sent' | 'refused';

export interface OfferPanelView {
  phase: OfferPanelPhase;
  /** The tab's title, else "Video". */
  title: string;
  rows: OfferRowView[];
  groups: OfferGroupView[];
  defaultId: string | undefined;
  status?: { text: string; tone: Tone; elapsed?: string };
  /** The site source's action under the status: try again. */
  action?: { kind: 'retry'; text: string };
  /** "Look for more qualities": the site source was not asked yet. */
  lookForMore: boolean;
  /** "Audio" of the default row's source, with several MP4 audio tracks. */
  audio?: { value: string; options: { id: string; label: string }[] };
  /** "Don't show on <host>" ("… on this site" in a sub-frame). */
  hide?: string;
  /** Small frames: rows wrap and the codec text is dropped … */
  narrow: boolean;
  /** … or only the default row is shown, with this note. */
  note?: string;
  busy: boolean;
  /**
   * The status reports work in progress (the offers loading, an add being sent, the site's
   * qualities being got or yt-dlp being set up): drawn as progress rather than as a warning,
   * whose tone it shares. Set only when true.
   */
  working?: true;
  /**
   * The Part row (MEDIA-SPEC §8, VIDEO-DETECT-SPEC §5.2): only for a stream or a page video, and
   * only when the relay lists `clips` (an older one would save the whole video).
   */
  part?: PartView;
  /**
   * The site video's page has several videos (SITE-MEDIA-SPEC §3.12, VIDEO-DETECT-SPEC §5.2
   * "Several videos"): the item row above the qualities.
   */
  items?: VideoPanelItems;
}

/** The Part row (part-row.ts, shared with the YouTube panel). */
export type { PartView } from './part-row';

export interface OfferPanelDeps {
  /** `downex:frame-offers` for the clicked element. */
  offers: (src: string, mse: boolean) => Promise<Outcome<FrameOffers>>;
  streamProbe: (key: OfferKey) => Promise<Outcome<VideoInfoResult>>;
  /** `downex:offer-add`: a file, an element file, or a stream with tracks ("" = default pick) and a part. */
  offerAdd: (key: OfferKey, pick: TrackPick, clip?: ClipRange) => Promise<Outcome<AddedResult>>;
  /** The site's qualities; with `entryId`, of that video of a page with several (SITE-MEDIA-SPEC §3.12). */
  siteProbe: (site: SiteOffer, entryId?: string) => Promise<Outcome<VideoInfoResult>>;
  siteAdd: (site: SiteOffer, pick: TrackPick, clip?: ClipRange, entryId?: string) => Promise<Outcome<AddedResult>>;
  /** "Don't show on <host>" (`downex:set-site-hidden`). */
  hideSite: () => Promise<SiteHiddenResponse | undefined>;
  render: (view: OfferPanelView) => void;
  timers?: Timers;
  /** The frame's viewport (small frames show less). */
  viewport?: () => { width: number; height: number };
  /**
   * The carousel slide the frame's address shows (addressSlide; undefined: none, or the site video
   * is a linked post's): with the clicked element's length, it picks the item of a page with several
   * videos the panel opens on (clickedItem).
   */
  itemPosition?: () => number | undefined;
  /** Whether the pointer is on the panel (its default row then never moves). */
  pointerOn?: () => boolean;
}

type SendState =
  | { rowId: string; phase: 'sending' }
  | { rowId: string; phase: 'sent'; text: string }
  | { rowId: string; phase: 'failed'; text: string; tone: Tone };

interface SiteSource {
  offer: SiteOffer;
  panel: VideoPanel;
  view: VideoPanelView;
  /** The site's qualities were asked for (curated page, a click, or no network row). */
  asked: boolean;
}

/** The best quality class an answer lists: its picture's shorter side (0 none). */
function bestHeight(info: VideoInfoResult | undefined): number {
  return Math.max(0, ...(info?.tracks ?? []).filter(isVideoTrack).map((t) => (t.width > 0 && t.height > 0 ? Math.min(t.width, t.height) : t.height)));
}

/** A stream's length is the clicked element's: within max(1 s, 5 %) (§2.6 "Several HLS streams"). */
function sameLength(ms: number, el: number): boolean {
  return ms > 0 && el > 0 && Math.abs(ms - el) <= Math.max(1000, el / 20);
}

/** A stream's length is close enough to end the search: the played files' tolerance, max(0.3 s, 1.5 %). */
function closeLength(ms: number, el: number): boolean {
  return ms > 0 && el > 0 && Math.abs(ms - el) <= Math.max(300, el * 0.015);
}

export class OfferPanel {
  readonly #deps: OfferPanelDeps;
  readonly #timers: Timers;
  #open = false;
  #generation = 0;
  #target: PanelTarget | undefined;
  #offers: FrameOffers | undefined;
  #offersError: ProtocolError | undefined;
  #loading = false;
  readonly #probes = new Map<OfferKey, StreamProbe>();
  readonly #streamAudio = new Map<OfferKey, string>();
  /** The audio chosen for the Played source's qualities (undefined: its default). */
  #playedAudio: string | undefined;
  /** The default row the panel showed, kept while it is listed (a panel with a Played source). */
  #shownDefault: string | undefined;
  #site: SiteSource | undefined;
  #send: SendState | undefined;
  readonly #expanded = new Set<GroupId>();
  #pollTimer: unknown;
  #hiding = false;
  /** The Part row: open, the part's times, a time typed that cannot be used. */
  readonly #part = new PartRow();

  constructor(deps: OfferPanelDeps) {
    this.#deps = deps;
    this.#timers = deps.timers ?? systemTimers;
  }

  get isOpen(): boolean {
    return this.#open;
  }

  /** Opens for the element checked at click time; what is known of it stays. */
  open(target: PanelTarget): void {
    this.#open = true;
    this.#target = target;
    if (this.#send?.phase === 'sent') this.#send = undefined;
    this.#site?.panel.clearSent();
    if (this.#refusal() !== undefined) {
      this.#render();
      return;
    }
    if (this.#site?.asked) this.#site.panel.open();
    void this.#load(true);
  }

  /** Closed: no more polling or probing on its behalf; what is known stays. */
  close(): void {
    this.#open = false;
    this.#stopPoll();
    this.#site?.panel.close();
  }

  /** Another element: everything is forgotten. */
  reset(): void {
    this.#generation++;
    this.close();
    this.#site?.panel.reset();
    this.#site = undefined;
    this.#offers = undefined;
    this.#offersError = undefined;
    this.#loading = false;
    this.#probes.clear();
    this.#streamAudio.clear();
    this.#playedAudio = undefined;
    this.#shownDefault = undefined;
    this.#send = undefined;
    this.#expanded.clear();
    this.#target = undefined;
    this.#part.reset();
  }

  // --- user actions ----------------------------------------------------------------------------

  /** A row was activated (none: the default row). */
  async download(rowId?: string): Promise<void> {
    if (this.#busy()) return;
    const rows = this.#rows();
    const id = rowId ?? rows.defaultId;
    const row = allRows(rows).find((r) => r.id === id);
    if (!row || row.disabled) return;
    if (row.action === 'probe' && row.key !== undefined) {
      void this.#probeStream(row.key);
      return;
    }
    // A part with an error is never sent: the row shows the error (B3).
    const part = this.#partCheck();
    if (part?.error) {
      this.#render();
      return;
    }
    if (row.source === 'site') {
      await this.#site?.panel.download(row.pick.video);
      return;
    }
    if (row.key === undefined) return;
    const generation = this.#generation;
    this.#send = { rowId: row.id, phase: 'sending' };
    this.#render();
    let outcome: Outcome<AddedResult>;
    try {
      outcome =
        row.source === 'stream' && part?.clip ? await this.#deps.offerAdd(row.key, row.pick, part.clip) : await this.#deps.offerAdd(row.key, row.pick);
    } catch (e) {
      outcome = failure('internal', `Background unavailable: ${errorMessage(e)}`);
    }
    if (generation !== this.#generation) return;
    if (outcome.ok) {
      this.#send = { rowId: row.id, phase: 'sent', text: offerAddedText(outcome.value) };
    } else {
      const text = row.source === 'file' ? fileFailureText(outcome.error) : streamFailureText(outcome.error);
      const tone: Tone = streamRefusal(outcome.error) || outcome.error.code === 'expired' ? 'warn' : 'error';
      this.#send = { rowId: row.id, phase: 'failed', text, tone };
    }
    this.#render();
  }

  /**
   * Another video of the site video's page (SITE-MEDIA-SPEC §3.12): its qualities are got, and the
   * part is chosen again (another video has another length).
   */
  chooseItem(id: string): void {
    const site = this.#site;
    if (!site?.asked || this.#busy() || site.panel.entryId === id) return;
    this.#part.reset();
    site.panel.chooseItem(id);
  }

  /** "Look for more qualities": the site's qualities (yt-dlp) for the frame's address. */
  lookForMore(): void {
    if (!this.#site || this.#site.asked) return;
    this.#askSite();
  }

  toggleGroup(id: GroupId): void {
    if (this.#expanded.has(id)) this.#expanded.delete(id);
    else this.#expanded.add(id);
    this.#render();
  }

  /** The Part row's disclosure. */
  togglePart(): void {
    this.#part.toggle();
    this.#render();
  }

  /** A handle of the timeline moved (`push`: Start here, End here). */
  setPartMs(which: PartField, ms: number, push = false): void {
    const info = this.#partInfo();
    if (info && this.#part.setMs(which, ms, info.durationMs, push)) this.#render();
  }

  /** A time typed into a chip was committed. */
  editPart(which: PartField, text: string): void {
    const info = this.#partInfo();
    if (!info) return;
    this.#part.edit(which, text, info.durationMs);
    this.#render();
  }

  /** A chapter (1 first) becomes the part. */
  choosePartChapter(n: number): void {
    const info = this.#partInfo();
    if (!info) return;
    this.#part.chooseChapter(n, info.chapters ?? [], info.durationMs);
    this.#render();
  }

  /** "Whole video". */
  wholePart(): void {
    this.#part.whole();
    this.#render();
  }

  /**
   * The info the Part row works on: the default row's (a stream's probe, the site's answer). Without
   * a duration there (Instagram answers 0) it takes the clicked element's own; with neither, no row
   * (VIDEO-DETECT-SPEC §5.2 "Its duration").
   */
  #partInfo(): VideoInfoResult | undefined {
    if (!this.#offers?.features.clips) return undefined;
    const row = this.#defaultRow();
    let info: VideoInfoResult | undefined;
    // A played video is saved whole (SITE-MEDIA-SPEC §2.9 "Never"): no Part row.
    if (row?.source === 'played') return undefined;
    if (row?.source === 'site') info = this.#site?.panel.info;
    else if (row?.source === 'stream' && row.key !== undefined && row.action === 'add') {
      const probe = this.#probes.get(row.key);
      if (probe?.phase === 'listed') info = probe.info;
    }
    if (!info || info.durationMs > 0) return info;
    const own = this.#elementDuration();
    return own > 0 ? { ...info, durationMs: own } : undefined;
  }

  /** The clicked element's own duration (ms), 0 unknown or live. */
  #elementDuration(): number {
    if (this.#target?.live) return 0;
    let ms = 0;
    try {
      ms = this.#target?.durationMs?.() ?? 0;
    } catch {
      ms = 0;
    }
    return Number.isFinite(ms) && ms > 0 ? ms : 0;
  }

  /** The Part row's check: undefined while the row is closed or not shown. */
  #partCheck(): ClipCheck | undefined {
    return this.#part.check(this.#partInfo());
  }

  /** The row as drawn; its readout's size is the default row's, for the part ("~", clipSize). */
  #partView(): PartView | undefined {
    const info = this.#partInfo();
    if (!info) return undefined;
    const row = this.#defaultRow();
    const size = row ? sizeLabel(clipSize(pickSize(info, row.pick), this.#part.clip(info), info.durationMs)) : '';
    return this.#part.view(info, sizeAt(size, row?.quality ?? ''));
  }

  chooseAudio(id: string): void {
    const audio = this.view().audio;
    if (!audio?.options.some((o) => o.id === id)) return;
    const row = this.#defaultRow();
    if (row?.source === 'site') this.#site?.panel.chooseAudio(id);
    else if (row?.source === 'played') {
      this.#playedAudio = id;
      this.#render();
    } else if (row?.key !== undefined) {
      this.#streamAudio.set(row.key, id);
      this.#render();
    }
  }

  /** The site source's action: try again. */
  siteAction(): void {
    this.#site?.panel.retry();
  }

  /** "Don't show on <host>": true when the buttons are hidden on the host now. */
  async hide(): Promise<boolean> {
    if (this.#hiding) return false;
    this.#hiding = true;
    try {
      return (await this.#deps.hideSite())?.hidden === true;
    } catch {
      return false;
    } finally {
      this.#hiding = false;
    }
  }

  // --- loading -----------------------------------------------------------------------------------

  async #load(first: boolean): Promise<void> {
    const generation = this.#generation;
    const target = this.#target;
    if (!target) return;
    this.#stopPoll();
    if (first && this.#offers === undefined) {
      this.#loading = true;
      this.#render();
    }
    let outcome: Outcome<FrameOffers>;
    try {
      outcome = await this.#deps.offers(target.src, target.mse);
    } catch (e) {
      outcome = failure('internal', `Background unavailable: ${errorMessage(e)}`);
    }
    if (generation !== this.#generation || !this.#open) return;
    const firstAnswer = this.#offers === undefined;
    this.#loading = false;
    if (outcome.ok) {
      this.#offers = outcome.value;
      this.#offersError = undefined;
      this.#syncSite();
      this.#probeStreams();
      if (firstAnswer) this.#autoSite();
    } else if (this.#offers === undefined || outcome.error.code === 'expired') {
      this.#offersError = outcome.error;
    }
    this.#render();
    this.#schedulePoll();
  }

  #schedulePoll(): void {
    if (!this.#open || this.#pollTimer !== undefined) return;
    const played = this.#offers?.played;
    const pending = played !== undefined && !(played.probe && 'result' in played.probe) && played.waitingMs < PLAYED_WAIT_MS;
    this.#pollTimer = this.#timers.setTimeout(
      () => {
        this.#pollTimer = undefined;
        if (this.#open) void this.#load(false);
      },
      pending ? PLAYED_POLL_MS : OFFER_POLL_MS,
    );
  }

  #stopPoll(): void {
    if (this.#pollTimer !== undefined) this.#timers.clearTimeout(this.#pollTimer);
    this.#pollTimer = undefined;
  }

  /** The site source follows the offers' site (another key: another video). */
  #syncSite(): void {
    const offer = this.#offers?.site;
    if (!offer) {
      if (this.#site) this.#site.panel.reset();
      this.#site = undefined;
      return;
    }
    if (this.#site && this.#site.offer.key === offer.key) {
      this.#site.offer = offer;
    } else {
      this.#site?.panel.reset();
      const holder: { source?: SiteSource } = {};
      const deps: ConstructorParameters<typeof VideoPanel>[0] = {
        probe: (entryId) =>
          entryId ? this.#deps.siteProbe(holder.source?.offer ?? offer, entryId) : this.#deps.siteProbe(holder.source?.offer ?? offer),
        add: (pick, entryId) => {
          const site = holder.source?.offer ?? offer;
          const clip = this.#partCheck()?.clip;
          if (entryId) return this.#deps.siteAdd(site, pick, clip, entryId);
          return clip ? this.#deps.siteAdd(site, pick, clip) : this.#deps.siteAdd(site, pick);
        },
        render: (view) => {
          if (!holder.source) return;
          holder.source.view = view;
          if (this.#site === holder.source) this.#render();
        },
        itemHint: (items, leftOut) => {
          const seen: ClickedVideo = { durationMs: this.#elementDuration() };
          const slide = this.#deps.itemPosition?.();
          if (slide !== undefined) seen.slide = slide;
          return clickedItem(items, leftOut, seen);
        },
        timers: this.#timers,
      };
      const panel = new VideoPanel(deps);
      holder.source = { offer, panel, view: panel.view(), asked: false };
      this.#site = holder.source;
    }
    const probe = offer.probe;
    if (probe && 'result' in probe && this.#site.panel.phase === 'idle') {
      this.#site.panel.adopt(probe.result);
      this.#site.asked = true;
    }
  }

  /** On a curated page, or without any network item, the site is asked at once. */
  #autoSite(): void {
    const site = this.#site;
    const offers = this.#offers;
    if (!site || !offers || site.asked) return;
    if (site.offer.curated || (offers.files.length === 0 && offers.streams.length === 0)) this.#askSite();
  }

  #askSite(): void {
    const site = this.#site;
    if (!site) return;
    site.asked = true;
    if (this.#open) site.panel.open();
    this.#render();
  }

  /**
   * Probes the first MAX_STREAM_PROBES streams (matched first, then newest), and older ones (up
   * to MAX_STREAM_PROBES_FOR_MASTER) while every answer is a bare media playlist; answers already
   * known are adopted.
   */
  #probeStreams(): void {
    const streams = this.#offers?.streams ?? [];
    let started = streams.filter((s) => this.#probes.has(s.key)).length;
    const answers = [...this.#probes.values()];
    const wantMaster =
      answers.some((p) => p.phase === 'listed' && bareAnswer(p.info)) &&
      !answers.some((p) => p.phase === 'probing' || (p.phase === 'listed' && !bareAnswer(p.info)));
    for (const s of streams) {
      if (this.#probes.has(s.key) || !(s.probe && 'result' in s.probe)) continue;
      const r = s.probe.result;
      this.#probes.set(s.key, r.ok ? { phase: 'listed', info: r.value } : { phase: 'failed', error: r.error });
      started++;
    }
    // A grid of previews (§2.6): look for the stream of the clicked element's length.
    const el = this.#elementDuration();
    const search = el > 0 && streams.length > MAX_STREAM_PROBES && answers.some((p) => p.phase === 'listed') && !answers.some((p) => p.phase === 'probing');
    // Searching stops at a length within the matching tolerance of played files (max(0.3 s, 1.5 %)):
    // a grid's other tile can be within the looser one (a 11.03 s playlist for a 10.24 s tile).
    const found = search ? streams.find((s) => closeLength(this.#streamLength(s), el)) : undefined;
    if (found) {
      // Its group's other playlists (its master, with every quality and the sound), while none listed qualities.
      const group = streams.filter((s) => s.group !== undefined && s.group === found.group);
      if (group.some((s) => { const p = this.#probes.get(s.key); return p?.phase === 'listed' && !bareAnswer(p.info); })) return;
      for (const s of group.slice(0, MAX_STREAM_PROBES_FOR_MASTER)) if (!this.#probes.has(s.key)) void this.#probeStream(s.key);
      return;
    }
    if (search) {
      // A group is asked once one of its streams answered (a refusal leaves it to its next stream).
      const asked = new Set(streams.filter((s) => this.#probes.get(s.key)?.phase === 'listed').map((s) => s.group ?? s.key));
      for (const s of streamOrder(streams)) {
        if (started >= MAX_STREAM_PROBES_FOR_LENGTH) break;
        const g = s.group ?? s.key;
        if (this.#probes.has(s.key) || asked.has(g)) continue;
        asked.add(g);
        started++;
        void this.#probeStream(s.key);
      }
      return;
    }
    const limit = wantMaster ? MAX_STREAM_PROBES_FOR_MASTER : MAX_STREAM_PROBES;
    for (const s of streamOrder(streams)) {
      if (this.#probes.has(s.key) || started >= limit) continue;
      started++;
      void this.#probeStream(s.key);
    }
  }

  /** A stream's length as its answer says (0 unknown). */
  #streamLength(s: StreamOffer): number {
    const p = this.#probes.get(s.key);
    return p?.phase === 'listed' ? p.info.durationMs : 0;
  }

  async #probeStream(key: OfferKey): Promise<void> {
    if (this.#probes.get(key)?.phase === 'probing') return;
    const generation = this.#generation;
    this.#probes.set(key, { phase: 'probing' });
    this.#render();
    let outcome: Outcome<VideoInfoResult>;
    try {
      outcome = await this.#deps.streamProbe(key);
    } catch (e) {
      outcome = failure('internal', `Background unavailable: ${errorMessage(e)}`);
    }
    if (generation !== this.#generation) return;
    this.#probes.set(key, outcome.ok ? { phase: 'listed', info: outcome.value } : { phase: 'failed', error: outcome.error });
    if (this.#open) this.#probeStreams();
    this.#render();
  }

  // --- the view ------------------------------------------------------------------------------------

  #refusal(): 'encrypted' | 'live' | undefined {
    if (this.#target?.drm || this.#offers?.drm) return 'encrypted';
    if (this.#target?.live || this.#offers?.live) return 'live';
    return undefined;
  }

  #busy(): boolean {
    return this.#send?.phase === 'sending' || this.#site?.panel.phase === 'sending';
  }

  #sent(): boolean {
    return this.#send?.phase === 'sent' || this.#site?.view.phase === 'sent';
  }

  /** The Played source's answer once Downex matched the clicked video's files (§2.9). */
  #playedInfo(): VideoInfoResult | undefined {
    const probe = this.#offers?.played?.probe;
    return probe && 'result' in probe && probe.result.ok ? probe.result.value : undefined;
  }

  /**
   * Whether the Played source has not answered yet, for at most PLAYED_SETTLE_MS: the panel names no
   * default row meanwhile, so the default never changes under the user's pointer (§2.9 "The panel").
   */
  #playedSettling(): boolean {
    const played = this.#offers?.played;
    if (!played || played.waitingMs >= PLAYED_SETTLE_MS) return false;
    return !(played.probe && 'result' in played.probe);
  }

  /**
   * A panel with a Played source keeps the default row it showed while that row is listed, and
   * names none while the Played source settles (#playedSettling). Other panels (YouTube's among
   * them) are unchanged.
   */
  #steady(rows: OfferRows): OfferRows {
    if (!this.#offers?.played) return rows;
    const kept = this.#shownDefault;
    if (kept !== undefined && allRows(rows).some((r) => r.id === kept && !r.disabled)) {
      const best = allRows(rows).find((r) => r.recommended);
      // A higher quality with sound listed later (the site's, seconds after the played rows) is
      // recommended and the default, unless the user already chose a row or points at the panel.
      const still = this.#send !== undefined || this.#deps.pointerOn?.() === true;
      const keptRow = allRows(rows).find((r) => r.id === kept);
      const choice = keptRow?.source === 'played' || keptRow?.source === 'site';
      if (best && choice && best.id !== kept && !best.disabled && !still) return rows;
      rows.defaultId = kept;
      // The badge stays with the row shown recommended while the default is kept; it is never
      // taken from every row.
      if (allRows(rows).some((r) => r.id === kept && r.recommended)) {
        for (const r of allRows(rows)) if (r.recommended && r.id !== kept) delete r.recommended;
      }
    } else if (this.#playedSettling()) rows.defaultId = undefined;
    return rows;
  }

  /**
   * The frame's streams, a matched one whose answer says a length other than the clicked element's
   * (by more than max(1 s, 5 %)) no longer matched: another video's stream (a grid of previews).
   */
  #streamsOfElement(streams: readonly StreamOffer[]): StreamOffer[] {
    const el = this.#elementDuration();
    if (el === 0) return [...streams];
    // Once a matched stream says the element's own length, the matched ones that said nothing yet
    // are no longer matched either, but for its own group's: the clicked video's stream (the closest
    // in length) leads.
    let found: StreamOffer | undefined;
    for (const s of streams) {
      const ms = this.#streamLength(s);
      if (s.matched && sameLength(ms, el) && (!found || Math.abs(ms - el) < Math.abs(this.#streamLength(found) - el))) found = s;
    }
    return streams.map((s) => {
      const ms = this.#streamLength(s);
      if (!s.matched) return s;
      const other = found !== undefined && s.key !== found.key && (found.group === undefined || s.group !== found.group);
      if (ms > 0 ? !sameLength(ms, el) || other : other) return { ...s, matched: false };
      return s;
    });
  }

  /** The rows; with clip (the part being sent), their sizes are the part's. */
  #rows(clip?: ClipRange): OfferRows {
    return this.#steady(this.#builtRows(clip));
  }

  #builtRows(clip?: ClipRange): OfferRows {
    const offers = this.#offers;
    const site = this.#site;
    const info = site?.panel.info;
    const played = this.#playedInfo();
    return buildOfferRows({
      files: offers?.files ?? [],
      streams: this.#streamsOfElement(offers?.streams ?? []),
      site: site && info && info.tracks.some(isVideoTrack) ? { name: cleanSiteText(site.offer.site), info, audio: site.view.pick.audio } : undefined,
      probes: this.#probes,
      streamAudio: this.#streamAudio,
      clip,
      played:
        played && offers?.played ? { key: offers.played.key, info: played, audio: this.#playedAudio ?? defaultAudio(played) } : undefined,
      // The frame's own address is the video, its servers answer more than browsers, and it offers
      // a better picture than what played (measured 2026-11-18: an Instagram reel's site rows had
      // no picture at all, the played ones 720p and 360p) (§2.6).
      siteFirst:
        site !== undefined && site.offer.curated && !site.offer.linked && info?.browserFetch !== true && bestHeight(info) > bestHeight(played),
    });
  }

  #defaultRow(): OfferRow | undefined {
    const rows = this.#rows();
    return allRows(rows).find((r) => r.id === rows.defaultId);
  }

  view(): OfferPanelView {
    const offers = this.#offers;
    // A feed video's own post: the site's title for it, once known (a feed's title is not the video's).
    const linkedTitle = this.#site?.offer.linked ? cleanSiteText(this.#site.panel.info?.title.trim() ?? '') : '';
    const title = linkedTitle || cleanSiteText(offers?.title.trim() ?? '') || OFFER_TEXTS.untitled;
    const vp = this.#deps.viewport?.();
    const narrow = vp !== undefined && vp.width > 0 && vp.width < NARROW_FRAME_PX;
    const short = vp !== undefined && vp.height > 0 && vp.height < SHORT_FRAME_PX;
    const busy = this.#busy();
    const view: OfferPanelView = {
      phase: 'ready',
      title,
      rows: [],
      groups: [],
      defaultId: undefined,
      lookForMore: false,
      narrow,
      busy,
    };
    // A sub-frame is not told the tab's host; the background hides the tab's host either way.
    if (offers) view.hide = offers.topHost ? hideOnText(offers.topHost) : OFFER_TEXTS.hideHere;
    const refusal = this.#refusal();
    if (refusal) {
      view.phase = 'refused';
      view.status = { text: VIDEO_TEXTS[refusal], tone: 'warn' };
      return view;
    }
    if (this.#loading) {
      view.phase = 'loading';
      view.status = { text: gettingInfoText(0), tone: 'warn' };
      view.working = true;
      return view;
    }
    if (this.#offersError && !offers) {
      view.status = {
        text: this.#offersError.code === 'expired' ? OFFER_TEXTS.expired : mediaFailureText(this.#offersError),
        tone: 'error',
      };
      return view;
    }

    // The rows' sizes follow the part, as the readout's does (and the YouTube panel's rows).
    const rows = this.#rows(this.#partCheck()?.clip);
    const locked = busy || this.#sent();
    const decorate = (r: OfferRow): OfferRowView => ({ ...r, disabled: r.disabled || locked, selected: r.id === rows.defaultId });
    view.defaultId = rows.defaultId;
    if (this.#open && rows.defaultId !== undefined && offers?.played) this.#shownDefault = rows.defaultId;
    view.rows = rows.main.map(decorate);
    if (rows.more.length > 0) {
      view.groups.push({ id: 'more', label: moreSourcesText(rows.more.length), expanded: this.#expanded.has('more'), rows: rows.more.map(decorate) });
    }
    if (rows.other.length > 0) {
      view.groups.push({ id: 'other', label: otherVideosText(rows.other.length), expanded: this.#expanded.has('other'), rows: rows.other.map(decorate) });
    }
    const site = this.#site;
    view.lookForMore = site !== undefined && !site.asked;
    const anyRow = allRows(rows).length > 0;

    // Status: an add of a file or stream, else what the site source says, else the empty text,
    // else the MP4 note.
    const siteView = site?.asked ? site.view : undefined;
    // A failure of the site's source is never the panel's result while another row exists
    // (VIDEO-DETECT-SPEC §2.6, amendment 2026-11-18), and on any site but YouTube it never names a
    // sign-in (SITE-MEDIA-SPEC §6.3).
    const siteFailed = siteView?.phase === 'error';
    let siteStatus = siteView?.status && !(siteView.phase === 'list' && siteView.status.tone === 'ok') ? siteView.status : undefined;
    if (siteFailed && anyRow) siteStatus = undefined;
    else if (siteStatus && siteFailed && /sign[ -]?in|cookies/i.test(siteStatus.text)) siteStatus = { text: OFFER_TEXTS.siteFailed, tone: 'warn' };
    const playedOffer = offers?.played;
    const playedProbe = playedOffer?.probe;
    if (this.#send?.phase === 'sending') {
      view.status = { text: 'Sending…', tone: 'warn' };
      view.working = true;
    } else if (this.#send) view.status = { text: this.#send.text, tone: this.#send.phase === 'sent' ? 'ok' : this.#send.tone };
    else if (siteStatus) {
      view.status = siteStatus;
      if (siteView?.action) view.action = siteView.action;
      if (siteView?.phase === 'probing' || siteView?.phase === 'sending') view.working = true;
    } else if (!anyRow && this.#allStreamsLive()) view.status = { text: VIDEO_TEXTS.live, tone: 'warn' };
    else if (!anyRow && playedOffer) {
      // Save what's playing (SITE-MEDIA-SPEC §2.9): never a dead end. A refusal that waiting does
      // not change (protected, an older Downex) says so; otherwise the panel waits for the video.
      const refusal = playedProbe && 'result' in playedProbe && !playedProbe.result.ok ? playedProbe.result.error : undefined;
      if (refusal && streamRefusal(refusal) === 'encrypted') view.status = { text: VIDEO_TEXTS.encrypted, tone: 'warn' };
      else if (refusal?.code === 'outdated_downex') view.status = { text: refusal.message, tone: 'warn' };
      else if (playedOffer.waitingMs < PLAYED_WAIT_MS) {
        view.status = { text: OFFER_TEXTS.playedGetting, tone: 'warn' };
        view.working = true;
      } else view.status = { text: OFFER_TEXTS.playedNotSeen, tone: 'warn' };
    } else if (!anyRow) {
      const text = offers?.networkOnly ? this.#networkOnlyText() : OFFER_TEXTS.foundNothing;
      view.status = { text, tone: 'warn' };
    } else if (rows.defaultId === undefined && this.#playedSettling()) {
      // The rows are listed and clickable; the default waits for what is playing.
      view.status = { text: OFFER_TEXTS.playedGetting, tone: 'warn' };
      view.working = true;
    }
    else {
      // A failed stream's row says only the reason's lead: the whole reason is said here.
      const problem = this.#streamProblem();
      // The site's answer that needed the user's sign-in says so (SITE-MEDIA-SPEC §6.3), and one
      // the browser fetches (§2.8).
      const info = siteView ? site?.panel.info : undefined;
      const def = this.#defaultRow();
      const played = def?.source === 'played' ? this.#playedInfo() : undefined;
      let note = siteNote(info, MP4_NOTE);
      if (played) {
        // What played is saved; a video file without its sound says so (§5.5).
        const silent = def?.pick.audio === '' && trackById(played, def.pick.video)?.kind === 'video';
        note = silent ? `${OFFER_TEXTS.noSound} ${OFFER_TEXTS.playedNote}` : `${OFFER_TEXTS.playedNote} ${MP4_NOTE}`;
        if (played.browserFetch) note = siteNote({ browserFetch: true }, note);
      }
      view.status = problem ? { text: problem, tone: 'error' } : { text: note, tone: 'ok' };
    }

    view.phase = this.#send?.phase === 'sending' || siteView?.phase === 'sending' ? 'sending' : this.#sent() ? 'sent' : 'ready';
    const audio = this.#audioChoice(rows);
    if (audio) view.audio = audio;
    const part = this.#partView();
    if (part && !short) view.part = part;
    if (siteView?.items) view.items = siteView.items;

    if (short) {
      // A small frame: the default row only; the toolbar menu lists the rest.
      const first = [...view.rows, ...view.groups.flatMap((g) => g.rows)].find((r) => r.id === rows.defaultId) ?? view.rows[0];
      view.rows = first ? [first] : [];
      view.groups = [];
      view.note = OFFER_TEXTS.moreInToolbar;
    }
    return view;
  }

  /** A `network` site with no row: playing is advice only while the video has not played yet. */
  #networkOnlyText(): string {
    let played = false;
    try {
      played = this.#target?.played?.() === true;
    } catch {
      played = false;
    }
    return played ? NETWORK_PLAYED_TEXT : NETWORK_ONLY_TEXT;
  }

  /** The whole reason of the first failed stream whose row shows it shortened (shortFailure). */
  #streamProblem(): string | undefined {
    for (const s of streamOrder(this.#offers?.streams ?? [])) {
      const p = this.#probes.get(s.key);
      if (p?.phase !== 'failed' || streamRefused(p)) continue;
      const text = streamFailureText(p.error);
      if (shortFailure(text) !== text) return text;
    }
    return undefined;
  }

  /** Downex refused every stream of the frame as live (a player that does not say so itself). */
  #allStreamsLive(): boolean {
    const streams = this.#offers?.streams ?? [];
    return (
      streams.length > 0 &&
      streams.every((s) => {
        const p = this.#probes.get(s.key);
        return p?.phase === 'failed' && streamRefusal(p.error) === 'live';
      })
    );
  }

  /** The audio select of the default row's source: several MP4 audio tracks and a video-only pick. */
  #audioChoice(rows: OfferRows): OfferPanelView['audio'] {
    const row = allRows(rows).find((r) => r.id === rows.defaultId);
    if (row?.source === 'site') return this.#site?.view.audio;
    let info: VideoInfoResult | undefined;
    if (row?.source === 'played') info = this.#playedInfo();
    else if (row?.source === 'stream' && row.key !== undefined && row.action === 'add') {
      const probe = this.#probes.get(row.key);
      if (probe?.phase === 'listed') info = probe.info;
    }
    if (!info || !row) return undefined;
    const tracks = audioTracks(info);
    if (tracks.length < 2 || !needsAudio(info, row.pick.video)) return undefined;
    return { value: row.pick.audio, options: tracks.map((t) => ({ id: t.id, label: audioLabel(t) })) };
  }

  #render(): void {
    this.#deps.render(this.view());
  }
}

// ---------------------------------------------------------------------------------------------
// DOM

/**
 * The panel's own rules, after the shared overlay rules (OVERLAY_SHARED_CSS): each source named
 * once per run of rows, the folded groups, "Look for more qualities", the small-frame note and
 * the footer, and the narrow and short forms of embedded frames.
 */
export const OFFER_PANEL_CSS = `
.dx-offers .dx-t { display: none; }
.dx-sect { display: flex; align-items: center; gap: 6px; padding: 6px 10px 2px; font-size: 11.5px; font-weight: 600; line-height: 1.3; color: var(--p-muted); letter-spacing: 0.01em; }
.dx-sect svg { width: 13px; height: 13px; }
.dx-sect > span { min-width: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.dx-rows > .dx-sect:first-child { padding-top: 2px; }
.dx-group { display: grid; gap: 2px; }
.dx-group-toggle {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  min-height: 32px;
  padding: 0 10px;
  border: 0;
  border-radius: 10px;
  background: transparent;
  color: var(--p-muted);
  font: inherit;
  font-size: 12.5px;
  font-weight: 600;
  text-align: left;
  cursor: pointer;
}
.dx-group-toggle::before {
  content: '';
  flex: none;
  width: 6px;
  height: 6px;
  margin: 0 3px 0 1px;
  border-right: 1.6px solid currentColor;
  border-bottom: 1.6px solid currentColor;
  transform: rotate(-45deg);
  transition: transform 0.2s var(--ease);
}
.dx-group-toggle[aria-expanded='true']::before { transform: translate(0, -2px) rotate(45deg); }
.dx-group-toggle:hover { color: var(--p-fg); background: var(--p-hover); }
.dx-group-toggle:focus-visible { outline: 2px solid var(--p-focus); outline-offset: 1px; }
.dx-group > .dx-rows { margin-left: 14px; padding-left: 4px; border-left: 1px solid var(--p-line); }
.dx-more {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 7px;
  min-height: 34px;
  margin: 0;
  padding: 0 14px;
  border: 1px solid var(--p-line);
  border-radius: 999px;
  background: var(--p-chip);
  color: var(--p-accent);
  font: inherit;
  font-weight: 600;
  cursor: pointer;
}
.dx-more:hover:not(:disabled) { background: var(--p-hover); border-color: var(--p-current-edge); }
.dx-more:disabled { opacity: 0.5; cursor: default; }
.dx-more:focus-visible, .dx-hide:focus-visible { outline: 2px solid var(--p-focus); outline-offset: 2px; }
.dx-note { margin: 0; padding: 0 10px; font-size: 12px; color: var(--p-muted); }
.dx-offers[data-short='true'] .dx-items-list { display: none; }
.dx-foot { display: flex; align-items: center; padding: 4px 2px 0; border-top: 1px solid var(--p-line); }
.dx-hide {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  min-width: 0;
  min-height: 30px;
  padding: 0 8px;
  border: 0;
  border-radius: 999px;
  background: transparent;
  color: var(--p-muted);
  font: inherit;
  font-size: 12px;
  font-weight: 550;
  text-align: left;
  cursor: pointer;
}
.dx-hide:hover { color: var(--p-fg); background: var(--p-hover); }
.dx-hide svg { width: 14px; height: 14px; }
.dx-offers[data-narrow='true'] { width: calc(100vw - 16px); padding: 6px; border-radius: 14px; }
.dx-offers[data-narrow='true'] .dx-c { display: none; }
.dx-offers[data-narrow='true'] .dx-row { grid-template-areas: 'q s go'; }
.dx-offers[data-narrow='true'] .dx-s { align-self: center; }
/* Short frames (< 200 px): one line per part, so that the title, the default row, "Look for more
   qualities", the toolbar note and the footer fit in a 170 px frame (154 px of panel: about 151). */
.dx-offers[data-short='true'] { --gap: 3px; padding: 5px; border-radius: 14px; }
.dx-offers[data-short='true'] .dx-head { padding: 0 0 0 8px; }
.dx-offers[data-short='true'] .dx-title { -webkit-line-clamp: 1; font-size: 13px; }
.dx-offers[data-short='true'] .dx-close { width: 24px; height: 24px; }
.dx-offers[data-short='true'] .dx-sub, .dx-offers[data-short='true'] .dx-sect { display: none; }
.dx-offers[data-short='true'] .dx-row {
  grid-template-columns: minmax(0, auto) minmax(0, 1fr) auto 26px;
  grid-template-areas: 'q c s go';
  min-height: 36px;
  padding-top: 4px;
  padding-bottom: 4px;
}
.dx-offers[data-short='true'] .dx-c { -webkit-line-clamp: 1; }
.dx-offers[data-short='true'] .dx-s { align-self: center; }
.dx-offers[data-short='true'] .dx-more { align-self: flex-start; min-height: 24px; padding: 0 8px; border: 0; background: transparent; font-size: 12px; }
.dx-offers[data-short='true'] .dx-note { padding: 0 8px; font-size: 11.5px; line-height: 1.4; }
.dx-offers[data-short='true'] .dx-foot { padding-top: 2px; }
.dx-offers[data-short='true'] .dx-hide { min-height: 24px; }
@media (forced-colors: active) {
  .dx-group-toggle, .dx-hide { color: ButtonText; }
  .dx-group-toggle:focus-visible, .dx-more:focus-visible, .dx-hide:focus-visible { outline: 2px solid Highlight; }
  .dx-more { border: 1px solid ButtonText; background: ButtonFace; color: ButtonText; }
  .dx-foot, .dx-group > .dx-rows { border-color: CanvasText; }
}
`;

const SELECTABLE = 'button.dx-row, button.dx-group-toggle';

export interface OfferPanelDomDeps {
  doc: Document;
  /** Whether an activation came from the user (`event.isTrusted`; tests substitute it). */
  isTrusted?: (e: Event) => boolean;
  onRow: (id: string) => void;
  onGroup: (id: GroupId) => void;
  onAudio: (id: string) => void;
  onAction: () => void;
  onLookForMore: () => void;
  onHide: () => void;
  /** Esc or ×: `focusButton` returns the focus to the button. */
  onClose: (focusButton: boolean) => void;
  /** The Part row: its disclosure, a handle moved, a time typed, a chapter, "Whole video". */
  onPartToggle?: () => void;
  onPartSet?: (which: PartField, ms: number, push: boolean) => void;
  onPartEdit?: (which: PartField, text: string) => void;
  onPartChapter?: (n: number) => void;
  onPartWhole?: () => void;
  /** The clicked `<video>`, in this frame (the Part row's playhead, preview, Start/End here). */
  partVideo?: () => PartVideo | undefined;
  /** Another video of a page with several was chosen in the item row. */
  onItem?: (id: string) => void;
  timers?: Timers;
}

interface RowDom {
  button: HTMLButtonElement;
  li: HTMLLIElement;
  q: HTMLElement;
  /** The quality's text in `q`, and the "Recommended" badge after it while the row is recommended. */
  qt: HTMLElement;
  rec: HTMLElement;
  c: HTMLElement;
  s: HTMLElement;
  t: HTMLElement;
}

interface GroupDom {
  section: HTMLElement;
  toggle: HTMLButtonElement;
  list: HTMLUListElement;
}

/** The glyph of a source's run header. */
const SOURCE_GLYPH: Record<OfferRow['source'], GlyphName> = { site: 'site', stream: 'stream', file: 'file', played: 'mark' };

let nextPanelId = 0;

/** The panel's elements; the layer positions it and shows it as a popover. */
export class OfferPanelDom {
  readonly el: HTMLElement;
  readonly #doc: Document;
  readonly #deps: OfferPanelDomDeps;
  readonly #isTrusted: (e: Event) => boolean;
  readonly #titleEl: HTMLElement;
  /** The MP4 note under the title, while there are rows. */
  readonly #subEl: HTMLElement;
  readonly #skelEl: HTMLElement;
  readonly #rowsEl: HTMLUListElement;
  readonly #groups: Record<GroupId, GroupDom>;
  readonly #audioEl: HTMLLabelElement;
  readonly #audioSelect: HTMLSelectElement;
  readonly #part: PartTrimmer;
  readonly #items: ItemStrip;
  readonly #statusEl: HTMLElement;
  readonly #statusText: HTMLElement;
  readonly #elapsedEl: HTMLElement;
  readonly #meter: ReturnType<typeof meter>;
  readonly #actionEl: HTMLButtonElement;
  readonly #actionText: HTMLElement;
  readonly #moreEl: HTMLButtonElement;
  readonly #noteEl: HTMLElement;
  readonly #footEl: HTMLElement;
  readonly #hideEl: HTMLButtonElement;
  readonly #hideText: HTMLElement;
  readonly #rowEls = new Map<string, RowDom>();
  /** The run headers by list and run ("<source>\n<tag>\n<n>"), kept so that a refresh changes nothing. */
  readonly #sectEls = new WeakMap<HTMLUListElement, Map<string, HTMLLIElement>>();
  /** The row the user activated: marked busy while the panel is sending … */
  #activated: string | undefined;
  /** … and as sent (a check, at full strength) while the panel says it was sent. */
  #sentId: string | undefined;
  /** Marks the panel while part of its list is below the visible bottom. */
  readonly #updateMore: () => void;
  #lastPhase: OfferPanelPhase | undefined;
  #shown = false;
  #lastView: OfferPanelView | undefined;

  constructor(deps: OfferPanelDomDeps) {
    this.#deps = deps;
    const doc = deps.doc;
    this.#doc = doc;
    this.#isTrusted = deps.isTrusted ?? ((e) => e.isTrusted);
    const id = `dxo-${++nextPanelId}`;

    this.el = doc.createElement('div');
    this.el.id = `${id}-panel`;
    this.el.className = 'dx-panel dx-offers';
    this.el.setAttribute('popover', 'auto');
    this.el.setAttribute('role', 'dialog');
    this.el.setAttribute('aria-labelledby', `${id}-title`);
    this.el.tabIndex = -1;
    this.el.dataset.open = 'false';
    this.el.dataset.working = 'false';

    // The head: the title (the button beside the panel carries the mark), a drawn close, and the
    // MP4 note under them.
    const head = doc.createElement('div');
    head.className = 'dx-head';
    this.#titleEl = doc.createElement('h2');
    this.#titleEl.id = `${id}-title`;
    this.#titleEl.className = 'dx-title';
    this.#titleEl.textContent = OFFER_TEXTS.untitled;
    const close = closeButton(doc);
    this.#subEl = noteLine(doc);
    head.append(this.#titleEl, close, this.#subEl);
    this.#skelEl = skeleton(doc);
    this.#items = new ItemStrip({ doc, thumbnails: true, isTrusted: this.#isTrusted, onChoose: (itemId) => deps.onItem?.(itemId) });

    this.#rowsEl = doc.createElement('ul');
    this.#rowsEl.className = 'dx-rows';
    this.#rowsEl.setAttribute('aria-label', 'MP4 videos');
    const group = (gid: GroupId): GroupDom => {
      const section = doc.createElement('div');
      section.className = 'dx-group';
      section.hidden = true;
      const toggle = doc.createElement('button');
      toggle.type = 'button';
      toggle.className = 'dx-group-toggle';
      toggle.dataset.group = gid;
      toggle.setAttribute('aria-expanded', 'false');
      toggle.setAttribute('aria-controls', `${id}-${gid}`);
      const list = doc.createElement('ul');
      list.id = `${id}-${gid}`;
      list.className = 'dx-rows';
      list.hidden = true;
      section.append(toggle, list);
      return { section, toggle, list };
    };
    this.#groups = { more: group('more'), other: group('other') };

    this.#audioEl = doc.createElement('label');
    this.#audioEl.className = 'dx-audio';
    this.#audioEl.hidden = true;
    this.#audioSelect = doc.createElement('select');
    this.#audioEl.append('Audio', this.#audioSelect);
    this.#part = new PartTrimmer({
      doc,
      id,
      isTrusted: this.#isTrusted,
      onLayout: () => this.#updateMore(),
      handlers: {
        toggle: () => deps.onPartToggle?.(),
        set: (which, ms, push) => deps.onPartSet?.(which, ms, push),
        edit: (which, text) => deps.onPartEdit?.(which, text),
        chapter: (n) => deps.onPartChapter?.(n),
        whole: () => deps.onPartWhole?.(),
      },
      ...(deps.partVideo ? { video: deps.partVideo } : {}),
      ...(deps.timers ? { timers: deps.timers } : {}),
    });

    this.#statusEl = doc.createElement('p');
    this.#statusEl.className = 'dx-status';
    this.#statusEl.setAttribute('role', 'status');
    this.#statusEl.setAttribute('aria-live', 'polite');
    this.#statusText = doc.createElement('span');
    this.#statusText.className = 'dx-status-text';
    this.#elapsedEl = doc.createElement('span');
    this.#elapsedEl.className = 'dx-elapsed';
    this.#elapsedEl.setAttribute('aria-hidden', 'true');
    this.#statusEl.append(this.#statusText, this.#elapsedEl);
    this.#meter = meter(doc);

    this.#actionEl = doc.createElement('button');
    this.#actionEl.type = 'button';
    this.#actionEl.className = 'dx-action';
    this.#actionEl.hidden = true;
    this.#actionText = labelled(doc, this.#actionEl, 'retry');
    this.#moreEl = doc.createElement('button');
    this.#moreEl.type = 'button';
    this.#moreEl.className = 'dx-more';
    labelled(doc, this.#moreEl, 'more').textContent = OFFER_TEXTS.lookForMore;
    this.#moreEl.hidden = true;
    this.#noteEl = doc.createElement('p');
    this.#noteEl.className = 'dx-note';
    this.#noteEl.hidden = true;
    this.#footEl = doc.createElement('div');
    this.#footEl.className = 'dx-foot';
    this.#footEl.hidden = true;
    this.#hideEl = doc.createElement('button');
    this.#hideEl.type = 'button';
    this.#hideEl.className = 'dx-hide';
    this.#hideText = labelled(doc, this.#hideEl, 'hide');
    this.#footEl.append(this.#hideEl);

    this.el.append(
      head,
      this.#items.el,
      this.#skelEl,
      this.#rowsEl,
      this.#groups.more.section,
      this.#groups.other.section,
      this.#audioEl,
      this.#part.section,
      this.#statusEl,
      this.#meter.el,
      this.#actionEl,
      this.#moreEl,
      this.#noteEl,
      this.#footEl,
    );
    this.#updateMore = overflowCue(this.el);

    const trusted = (fn: (e: Event) => void) => (e: Event) => {
      if (this.#isTrusted(e)) fn(e);
    };
    close.addEventListener('click', trusted(() => deps.onClose(true)));
    this.el.addEventListener('keydown', (e) => this.#onKey(e));
    this.el.addEventListener(
      'click',
      trusted((e) => {
        const target = e.target as Element | null;
        const row = target?.closest?.('button.dx-row') as HTMLButtonElement | null;
        if (row?.dataset.id && !row.disabled) {
          this.#activated = row.dataset.id;
          deps.onRow(row.dataset.id);
          return;
        }
        const toggle = target?.closest?.('button.dx-group-toggle') as HTMLButtonElement | null;
        const gid = toggle?.dataset.group;
        if (gid === 'more' || gid === 'other') deps.onGroup(gid);
      }),
    );
    this.#audioSelect.addEventListener('change', () => deps.onAudio(this.#audioSelect.value));
    this.#actionEl.addEventListener('click', trusted(() => deps.onAction()));
    this.#moreEl.addEventListener('click', trusted(() => deps.onLookForMore()));
    this.#hideEl.addEventListener('click', trusted(() => deps.onHide()));
  }

  /** Whether the panel is shown (the layer opens and closes it). */
  set shown(v: boolean) {
    this.#shown = v;
    this.el.dataset.open = String(v);
    if (!v) {
      this.#activated = undefined;
      this.#sentId = undefined;
      // The player goes back where it was, unless the user took it over meanwhile.
      this.#part.stop();
    }
  }

  /** The panel's height or position changed (the layer placed it): the scroll cue follows. */
  updateOverflow(): void {
    this.#updateMore();
  }

  get shown(): boolean {
    return this.#shown;
  }

  render(view: OfferPanelView): void {
    const focused = this.#focusInPanel() ? this.#activeElement() : null;
    const hadRows = this.#rowsEl.childElementCount > 0 || this.#groups.more.list.childElementCount > 0;
    this.#lastView = view;
    setText(this.#titleEl, view.title);
    this.el.dataset.narrow = String(view.narrow);
    // A small frame (only the default row, with the toolbar note): a compact form that fits.
    this.el.dataset.short = String(view.note !== undefined);
    this.el.dataset.phase = view.phase;
    // The row the user activated shows the add (a turning ring) until the panel stops sending,
    // then, when it was sent, a check until the panel says something else. A site row is sent
    // after the site panel chose its quality (one render before "sending").
    if (view.phase !== 'sent') this.#sentId = undefined;
    if (this.#lastPhase === 'sending' && view.phase !== 'sending') {
      if (view.phase === 'sent') this.#sentId = this.#activated;
      this.#activated = undefined;
    }
    this.#lastPhase = view.phase;
    const used = new Set<string>();
    this.#renderList(this.#rowsEl, view.rows, used);
    for (const gid of ['more', 'other'] as const) {
      const g = view.groups.find((x) => x.id === gid);
      const dom = this.#groups[gid];
      dom.section.hidden = g === undefined;
      setText(dom.toggle, g?.label ?? '');
      dom.toggle.setAttribute('aria-expanded', String(g?.expanded ?? false));
      dom.list.hidden = !g?.expanded;
      this.#renderList(dom.list, g?.rows ?? [], used);
    }
    for (const id of [...this.#rowEls.keys()]) if (!used.has(id)) this.#rowEls.delete(id);

    this.#audioEl.hidden = view.audio === undefined;
    if (view.audio) {
      const signature = view.audio.options.map((o) => `${o.id}\n${o.label}`).join('\n\n');
      if (this.#audioSelect.dataset.signature !== signature) {
        this.#audioSelect.replaceChildren(
          ...view.audio.options.map((o) => {
            const opt = this.#doc.createElement('option');
            opt.value = o.id;
            opt.textContent = cleanSiteText(o.label);
            return opt;
          }),
        );
        this.#audioSelect.dataset.signature = signature;
      }
      if (this.#audioSelect.value !== view.audio.value) this.#audioSelect.value = view.audio.value;
      this.#audioSelect.disabled = view.busy;
    }
    this.#part.render(view.part, view.busy || view.phase === 'sent');
    this.#items.render(view.items, view.busy);
    // The live region changes only when its text does: the seconds beside it are not announced.
    setText(this.#statusText, initialCapital(view.status?.text ?? ''));
    setText(this.#elapsedEl, view.status?.elapsed ?? '');
    if (view.status) this.#statusEl.dataset.tone = statusTone(view.phase, view.status);
    else delete this.#statusEl.dataset.tone;
    // Work in progress is drawn as progress (a neutral line with a turning ring), not as a
    // warning; the tools download gets a meter, a first list still loading skeleton rows.
    const working = view.working === true;
    this.el.dataset.working = String(working);
    const metered = this.#meter.set(view.status?.text ?? '', working);
    const anyRow = view.rows.length > 0 || view.groups.length > 0;
    this.#skelEl.hidden = !(working && !anyRow && !view.busy && !metered);
    // The MP4 note is drawn under the title while there are rows. The status of a usable list
    // (phase `ready`, tone ok; the only ok status of that phase, see view()) is that note, with
    // what the site's answer adds (siteNote: the sign-in used, the browser fetch): drawn there in
    // full, while the live region keeps announcing it, visually hidden, so it is neither drawn
    // twice nor lost.
    this.#subEl.hidden = !anyRow;
    const note = view.phase === 'ready' && view.status?.tone === 'ok' && !working ? view.status.text : undefined;
    setText(this.#subEl, note ?? MP4_NOTE);
    if (note !== undefined) this.#statusEl.dataset.note = 'true';
    else delete this.#statusEl.dataset.note;
    this.#actionEl.hidden = view.action === undefined;
    setText(this.#actionText, view.action?.text ?? '');
    if (view.action) {
      this.#actionEl.dataset.kind = view.action.kind;
      setGlyph(this.#doc, this.#actionEl, 'retry');
    }
    this.#moreEl.hidden = !view.lookForMore;
    this.#moreEl.disabled = view.busy;
    this.#noteEl.hidden = view.note === undefined;
    setText(this.#noteEl, view.note ?? '');
    this.#footEl.hidden = view.hide === undefined;
    setText(this.#hideText, view.hide ?? '');

    // A list that arrived while the panel was open takes the focus to the default row.
    const hasRows = view.rows.length > 0;
    if (this.#shown && !hadRows && hasRows && (focused === null || focused === this.el)) this.focusInside();
    else this.#keepFocus(focused);
    this.#updateMore();
  }

  /** The default row, else the panel itself (where Up, Down and Esc still work). */
  focusInside(): void {
    const row = this.el.querySelector<HTMLButtonElement>('button.dx-row[aria-current="true"]:not(:disabled)');
    const first = this.#visibleSelectable().find((b) => !b.disabled);
    (row ?? first ?? this.el).focus();
  }

  get view(): OfferPanelView | undefined {
    return this.#lastView;
  }

  /**
   * The rows of `list`, made or updated in place, in their order. Each run of rows from one source
   * is headed once by that source's tag ("Stream", "File", the site's name) instead of a tag on
   * every row; the rows keep their `.dx-t` (hidden) for the row model. The headers are decorative
   * (the rows' labels name the source) and kept by run, so that a refresh moves nothing.
   */
  #renderList(list: HTMLUListElement, rows: readonly OfferRowView[], used: Set<string>): void {
    const busyId = this.#lastView?.phase === 'sending' ? this.#activated : undefined;
    let sects = this.#sectEls.get(list);
    if (!sects) {
      sects = new Map();
      this.#sectEls.set(list, sects);
    }
    const usedSects = new Set<string>();
    const runs = new Map<string, number>();
    const lis: HTMLLIElement[] = [];
    let runKey = '';
    for (const r of rows) {
      used.add(r.id);
      const tag = cleanSiteText(r.tag);
      const run = `${r.source}\n${tag}`;
      if (run !== runKey) {
        runKey = run;
        const n = (runs.get(run) ?? 0) + 1;
        runs.set(run, n);
        const key = `${run}\n${n}`;
        let sect = sects.get(key);
        if (!sect) {
          sect = this.#doc.createElement('li');
          sect.className = 'dx-sect';
          sect.setAttribute('aria-hidden', 'true');
          const text = this.#doc.createElement('span');
          text.textContent = tag;
          sect.append(glyph(this.#doc, SOURCE_GLYPH[r.source]), text);
          sects.set(key, sect);
        }
        usedSects.add(key);
        lis.push(sect);
      }
      let el = this.#rowEls.get(r.id);
      if (!el) {
        const li = this.#doc.createElement('li');
        const button = this.#doc.createElement('button');
        button.type = 'button';
        button.className = 'dx-row dx-offer';
        button.dataset.id = r.id;
        const q = this.#doc.createElement('span');
        q.className = 'dx-q';
        const qt = this.#doc.createElement('span');
        qt.className = 'dx-qt';
        const rec = this.#doc.createElement('span');
        rec.className = 'dx-rec';
        rec.textContent = RECOMMENDED_TEXT;
        q.append(qt);
        const c = this.#doc.createElement('span');
        c.className = 'dx-c';
        const s = this.#doc.createElement('span');
        s.className = 'dx-s';
        const t = this.#doc.createElement('span');
        t.className = 'dx-t';
        t.setAttribute('aria-hidden', 'true');
        button.append(q, c, s, t, goDisc(this.#doc));
        li.append(button);
        el = { li, button, q, qt, rec, c, s, t };
        this.#rowEls.set(r.id, el);
      }
      const label = r.recommended ? `${cleanSiteText(r.label)}, ${RECOMMENDED_TEXT}` : cleanSiteText(r.label);
      if (el.button.disabled !== r.disabled) el.button.disabled = r.disabled;
      if (el.button.getAttribute('aria-current') !== String(r.selected)) el.button.setAttribute('aria-current', String(r.selected));
      if (el.button.getAttribute('aria-label') !== label) el.button.setAttribute('aria-label', label);
      if (r.id === busyId) el.button.setAttribute('aria-busy', 'true');
      else el.button.removeAttribute('aria-busy');
      if (r.id === this.#sentId) el.button.dataset.sent = 'true';
      else delete el.button.dataset.sent;
      const title = r.title ? cleanSiteText(r.title) : '';
      if (el.button.title !== title) el.button.title = title;
      el.button.dataset.source = r.source;
      setText(el.qt, cleanSiteText(r.quality));
      if (r.recommended && el.rec.parentNode !== el.q) el.q.append(el.rec);
      else if (!r.recommended && el.rec.parentNode) el.rec.remove();
      setText(el.c, cleanSiteText(r.detail));
      setText(el.s, r.size);
      setText(el.t, tag);
      lis.push(el.li);
    }
    for (const key of [...sects.keys()]) if (!usedSects.has(key)) sects.delete(key);
    const current = [...list.children];
    if (current.length !== lis.length || current.some((li, i) => li !== lis[i])) list.replaceChildren(...lis);
  }

  #visibleSelectable(): HTMLButtonElement[] {
    return [...this.el.querySelectorAll<HTMLButtonElement>(SELECTABLE)].filter((b) => !b.closest('[hidden]'));
  }

  #onKey(e: KeyboardEvent): void {
    if (e.key === 'Escape') {
      e.preventDefault();
      this.#deps.onClose(true);
      return;
    }
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    // Up and Down keep their own meaning in a field, a select or a slider (the Part row's, Audio).
    if ((e.target as Element | null)?.closest?.('input, select, textarea, [role="slider"]')) return;
    const items = this.#visibleSelectable();
    if (items.length === 0) return;
    const i = items.indexOf(this.#activeElement() as HTMLButtonElement);
    const next = e.key === 'ArrowDown' ? (i < 0 ? 0 : Math.min(items.length - 1, i + 1)) : i < 0 ? items.length - 1 : Math.max(0, i - 1);
    e.preventDefault();
    items[next]?.focus();
  }

  #activeElement(): Element | null {
    return (this.el.getRootNode() as Document | ShadowRoot).activeElement ?? null;
  }

  #focusInPanel(): boolean {
    const active = this.#activeElement();
    return active !== null && this.el.contains(active);
  }

  /** Keeps the focus where it was while that can take it, else on the default row or the panel. */
  #keepFocus(was: Element | null): void {
    if (!was || !this.#shown) return;
    const usable = was.isConnected && !(was as HTMLButtonElement).disabled && !(was as HTMLElement).closest('[hidden]');
    if (usable) {
      if (this.#activeElement() !== was) (was as HTMLElement).focus();
      return;
    }
    this.focusInside();
  }
}
