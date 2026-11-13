// The panel of a Download button over any video (VIDEO-DETECT-SPEC §2.6, §5.2): one list merged
// from three sources — the site's qualities (yt-dlp, through VideoPanel's state machine), the
// MP4 qualities of the frame's streams (`downex:stream-probe`), and the direct files — with the
// rows of offer-rows.ts. While open it asks for the frame's offers again every 2 s, so a stream
// or file that arrives later appears; rows keep their focus and selection by id. OfferPanel is
// the state (pure: requests, timers and rendering injected); OfferPanelDom draws it with DOM calls
// only (no HTML strings) inside the layer's closed shadow root.

import type { SiteHiddenResponse } from './messages';
import type { FrameOffers, OfferKey, SiteOffer } from './offer-types';
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
import { type AddedResult, errorMessage, failure, type Outcome, type ProtocolError, systemTimers, type Timers, type VideoInfoResult } from './protocol';
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
import { MP4_NOTE, VideoPanel, type VideoPanelView } from './video-panel';
import { audioLabel, audioTracks, isVideoTrack, needsAudio, type TrackPick } from './video-picker';

/** The panel asks for the frame's offers again this often while it is open. */
export const OFFER_POLL_MS = 2000;
/** Streams probed when the panel opens (matched first, then newest) … */
export const MAX_STREAM_PROBES = 4;
/**
 * … and, while every answer so far is a bare media playlist (a level the player loaded after its
 * master), older ones up to this many in all, so that the master is found.
 */
export const MAX_STREAM_PROBES_FOR_MASTER = 12;
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
  /** The site source's action under the status: allow the sign-in, or try again. */
  action?: { kind: 'allow-sign-in' | 'retry'; text: string };
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
}

export interface OfferPanelDeps {
  /** `downex:frame-offers` for the clicked element. */
  offers: (src: string, mse: boolean) => Promise<Outcome<FrameOffers>>;
  streamProbe: (key: OfferKey) => Promise<Outcome<VideoInfoResult>>;
  /** `downex:offer-add`: a file, an element file, or a stream with tracks ("" = default pick). */
  offerAdd: (key: OfferKey, pick: TrackPick) => Promise<Outcome<AddedResult>>;
  siteProbe: (site: SiteOffer) => Promise<Outcome<VideoInfoResult>>;
  siteAdd: (site: SiteOffer, pick: TrackPick) => Promise<Outcome<AddedResult>>;
  /** "Don't show on <host>" (`downex:set-site-hidden`). */
  hideSite: () => Promise<SiteHiddenResponse | undefined>;
  /** Turns on the sign-in option after `auth_required` on a curated site. */
  allowSignIn?: () => Promise<void>;
  signInAllowed?: () => boolean;
  render: (view: OfferPanelView) => void;
  timers?: Timers;
  /** The frame's viewport (small frames show less). */
  viewport?: () => { width: number; height: number };
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
  #site: SiteSource | undefined;
  #send: SendState | undefined;
  readonly #expanded = new Set<GroupId>();
  #pollTimer: unknown;
  #hiding = false;

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
    this.#send = undefined;
    this.#expanded.clear();
    this.#target = undefined;
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
      outcome = await this.#deps.offerAdd(row.key, row.pick);
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

  chooseAudio(id: string): void {
    const audio = this.view().audio;
    if (!audio?.options.some((o) => o.id === id)) return;
    const row = this.#defaultRow();
    if (row?.source === 'site') this.#site?.panel.chooseAudio(id);
    else if (row?.key !== undefined) {
      this.#streamAudio.set(row.key, id);
      this.#render();
    }
  }

  /** The site source's action: allow the sign-in, or try again. */
  siteAction(): void {
    const site = this.#site;
    if (!site) return;
    if (site.view.action?.kind === 'allow-sign-in') void site.panel.allowSignIn();
    else site.panel.retry();
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
    this.#pollTimer = this.#timers.setTimeout(() => {
      this.#pollTimer = undefined;
      if (this.#open) void this.#load(false);
    }, OFFER_POLL_MS);
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
        probe: () => this.#deps.siteProbe(holder.source?.offer ?? offer),
        add: (pick) => this.#deps.siteAdd(holder.source?.offer ?? offer, pick),
        render: (view) => {
          if (!holder.source) return;
          holder.source.view = view;
          if (this.#site === holder.source) this.#render();
        },
        timers: this.#timers,
      };
      // Sign-in cookies go only with curated sites' videos (SITE-MEDIA-SPEC §6.3).
      if (offer.curated && this.#deps.allowSignIn) deps.allowSignIn = this.#deps.allowSignIn;
      if (offer.curated && this.#deps.signInAllowed) deps.signInAllowed = this.#deps.signInAllowed;
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
    const limit = wantMaster ? MAX_STREAM_PROBES_FOR_MASTER : MAX_STREAM_PROBES;
    for (const s of streamOrder(streams)) {
      if (this.#probes.has(s.key)) continue;
      if (s.probe && 'result' in s.probe) {
        const r = s.probe.result;
        this.#probes.set(s.key, r.ok ? { phase: 'listed', info: r.value } : { phase: 'failed', error: r.error });
        started++;
        continue;
      }
      if (started >= limit) continue;
      started++;
      void this.#probeStream(s.key);
    }
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

  #rows(): OfferRows {
    const offers = this.#offers;
    const site = this.#site;
    const info = site?.panel.info;
    return buildOfferRows({
      files: offers?.files ?? [],
      streams: offers?.streams ?? [],
      site: site && info && info.tracks.some(isVideoTrack) ? { name: cleanSiteText(site.offer.site), info, audio: site.view.pick.audio } : undefined,
      probes: this.#probes,
      streamAudio: this.#streamAudio,
    });
  }

  #defaultRow(): OfferRow | undefined {
    const rows = this.#rows();
    return allRows(rows).find((r) => r.id === rows.defaultId);
  }

  view(): OfferPanelView {
    const offers = this.#offers;
    const title = cleanSiteText(offers?.title.trim() ?? '') || OFFER_TEXTS.untitled;
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

    const rows = this.#rows();
    const locked = busy || this.#sent();
    const decorate = (r: OfferRow): OfferRowView => ({ ...r, disabled: r.disabled || locked, selected: r.id === rows.defaultId });
    view.defaultId = rows.defaultId;
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
    const siteStatus = siteView?.status && !(siteView.phase === 'list' && siteView.status.tone === 'ok') ? siteView.status : undefined;
    if (this.#send?.phase === 'sending') {
      view.status = { text: 'Sending…', tone: 'warn' };
      view.working = true;
    } else if (this.#send) view.status = { text: this.#send.text, tone: this.#send.phase === 'sent' ? 'ok' : this.#send.tone };
    else if (siteStatus) {
      view.status = siteStatus;
      if (siteView?.action) view.action = siteView.action;
      if (siteView?.phase === 'probing' || siteView?.phase === 'sending') view.working = true;
    } else if (!anyRow && this.#allStreamsLive()) view.status = { text: VIDEO_TEXTS.live, tone: 'warn' };
    else if (!anyRow) view.status = { text: OFFER_TEXTS.foundNothing, tone: 'warn' };
    else {
      // A failed stream's row says only the reason's lead: the whole reason is said here.
      const problem = this.#streamProblem();
      view.status = problem ? { text: problem, tone: 'error' } : { text: MP4_NOTE, tone: 'ok' };
    }

    view.phase = this.#send?.phase === 'sending' || siteView?.phase === 'sending' ? 'sending' : this.#sent() ? 'sent' : 'ready';
    const audio = this.#audioChoice(rows);
    if (audio) view.audio = audio;

    if (short) {
      // A small frame: the default row only; the toolbar menu lists the rest.
      const first = [...view.rows, ...view.groups.flatMap((g) => g.rows)].find((r) => r.id === rows.defaultId);
      view.rows = first ? [first] : [];
      view.groups = [];
      view.note = OFFER_TEXTS.moreInToolbar;
    }
    return view;
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
    if (row?.source !== 'stream' || row.key === undefined || row.action !== 'add') return undefined;
    const probe = this.#probes.get(row.key);
    if (probe?.phase !== 'listed') return undefined;
    const info = probe.info;
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
}

interface RowDom {
  button: HTMLButtonElement;
  li: HTMLLIElement;
  q: HTMLElement;
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
const SOURCE_GLYPH: Record<OfferRow['source'], GlyphName> = { site: 'site', stream: 'stream', file: 'file' };

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
      this.#skelEl,
      this.#rowsEl,
      this.#groups.more.section,
      this.#groups.other.section,
      this.#audioEl,
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
    // (phase `ready`, tone ok; the only ok status of that phase, see view()) is that note: the
    // live region keeps announcing it, visually hidden, so it is neither drawn twice nor lost.
    this.#subEl.hidden = !anyRow;
    if (view.phase === 'ready' && view.status?.tone === 'ok' && !working) this.#statusEl.dataset.note = 'true';
    else delete this.#statusEl.dataset.note;
    this.#actionEl.hidden = view.action === undefined;
    setText(this.#actionText, view.action?.text ?? '');
    if (view.action) {
      this.#actionEl.dataset.kind = view.action.kind;
      setGlyph(this.#doc, this.#actionEl, view.action.kind === 'allow-sign-in' ? 'key' : 'retry');
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
        const c = this.#doc.createElement('span');
        c.className = 'dx-c';
        const s = this.#doc.createElement('span');
        s.className = 'dx-s';
        const t = this.#doc.createElement('span');
        t.className = 'dx-t';
        t.setAttribute('aria-hidden', 'true');
        button.append(q, c, s, t, goDisc(this.#doc));
        li.append(button);
        el = { li, button, q, c, s, t };
        this.#rowEls.set(r.id, el);
      }
      const label = cleanSiteText(r.label);
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
      setText(el.q, cleanSiteText(r.quality));
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
