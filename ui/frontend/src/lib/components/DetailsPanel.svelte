<script lang="ts" module>
  /** What the multi-select strip offers for the selection. */
  export type SelectionAction = 'pause' | 'resume' | 'remove';
</script>

<script lang="ts">
  // The details pane (DESIGN-SYSTEM.md §3.15): nothing when nothing is selected, a 44 px strip
  // for several downloads, and for one its header (tile, name, what it is, state, actions) over
  // three columns: the facts, its connections (one box each, from B) and its speed (A's big
  // figures over a chart with a y-axis). The user can collapse it to its header.
  import type { Download } from '../api';
  import type { SpeedSample } from '../stores/downloads.svelte';
  import { MAIN_QUEUE_ID, isActive, isResumable, queueOf } from '../api';
  import Icon from './Icon.svelte';
  import FileIcon from './FileIcon.svelte';
  import FileName from './FileName.svelte';
  import StatusMark, { markOfState } from './StatusMark.svelte';
  import Select from './Select.svelte';
  import SpeedChart from './SpeedChart.svelte';
  import ConnectionBoxes from './ConnectionBoxes.svelte';
  import { density } from '../stores/density.svelte';
  import { displayName, kindOf } from '../categories';
  import {
    CUSTOM_LIMIT,
    addedText,
    dayTimeText,
    errorHint,
    limitOptions,
    speedParts,
    speedStats,
    joinPath,
    mediaBadge,
    rowSubline,
    selectionSummary,
    sizeHint,
    statePill,
  } from '../display';
  import {
    etaSeconds,
    formatBytes,
    formatClock,
    formatDateTime,
    formatDuration,
    formatEta,
    formatPercent,
    formatSize,
    formatSpeed,
    progressFraction,
  } from '../format';
  import {
    cannotRetry,
    chosenTracks,
    codecSummary,
    containerLabel,
    formatLabel,
    isSite,
    pickSize,
    pickSizeText,
    siteErrorHint,
    siteVideoLabel,
  } from '../media';
  import { addressLabel, timeTakenText } from '../progress';
  import { escapeBidi } from '../text';

  interface Props {
    download: Download | undefined;
    selectedCount: number;
    /** The selected downloads, for the multi-select strip's size and actions. */
    selected?: readonly Download[];
    history: readonly SpeedSample[];
    /** settings.defaultMaxConnections, shown when the download uses the daemon default. */
    defaultConnections: number;
    /** The daemon is unreachable: live values are not current and actions are unavailable. */
    stale?: boolean;
    /** Only the header is shown (the user's choice, kept in downex.detailsCollapsed). */
    collapsed?: boolean;
    /** The name of the download's queue; without it only the main queue is named. */
    queueName?: string;
    ontoggle?: () => void;
    onrestart: (d: Download) => void;
    onresume: (d: Download) => void;
    /** Pause in the header (shown when given). */
    onpause?: (d: Download) => void;
    oncopy: (d: Download) => void;
    onopenfolder: (d: Download) => void;
    /** Shows the confirmation dialog of a download awaiting confirmation. */
    onconfirm?: (d: Download) => void;
    /** A server asked for credentials (auth_required): offers to add a site login for it. */
    onaddlogin?: (d: Download) => void;
    /** A speed limit chosen in the pane (bytes per second; 0 = unlimited). */
    onlimit?: (d: Download, bytesPerSecond: number) => void;
    /** "Custom…" speed limit: the download's progress window (its Speed limit tab). */
    onprogress?: (d: Download) => void;
    /** ⋯: the row's context menu at these viewport coordinates. */
    onmore?: (x: number, y: number) => void;
    /** The multi-select strip's Pause, Resume and Remove. */
    onselection?: (action: SelectionAction) => void;
  }

  let {
    download,
    selectedCount,
    selected,
    history,
    defaultConnections,
    stale = false,
    collapsed = false,
    queueName,
    ontoggle,
    onrestart,
    onresume,
    onpause,
    oncopy,
    onopenfolder,
    onconfirm,
    onaddlogin,
    onlimit,
    onprogress,
    onmore,
    onselection,
  }: Props = $props();

  const uid = $props.id();
  const d = $derived(download);
  const live = $derived(d !== undefined && d.state === 'downloading' && !stale);
  /** The stream of a media download (MEDIA-SPEC §6), else null. */
  const media = $derived(d !== undefined && d.kind === 'media' ? d.media : null);
  /** A web page's video (SITE-MEDIA-SPEC §5.4): "Page", parts, its site and exact sizes. */
  const site = $derived(isSite(media));
  const tracks = $derived(media ? chosenTracks(media) : null);
  /** Before it starts: the size the requester announced, or a stream's (estimated) size. */
  const expected = $derived(
    d === undefined
      ? ''
      : media
        ? pickSizeText(pickSize(media, { video: media.selectedVideo, audio: media.selectedAudio }))
        : sizeHint(d) > 0
          ? `about ${formatBytes(sizeHint(d))}`
          : '',
  );
  /** Retrying cannot help: a protected or live stream, or a page without the MP4 video chosen. */
  const refused = $derived(d !== undefined && cannotRetry(d.errorCode));
  const hint = $derived(d === undefined ? '' : (site && siteErrorHint(d.errorCode)) || errorHint(d.errorCode));
  const pill = $derived(d ? statePill(d) : null);
  /** "youtube.com · Video · 1080p · H.264 · AAC 128 kbit/s". */
  const subline = $derived.by(() => {
    if (!d) return '';
    const quality = media ? codecSummary(media) : '';
    return [rowSubline(d), quality].filter((s) => s !== '').join(' · ');
  });
  const fraction = $derived(d ? progressFraction(d) : Number.NaN);
  const pct = $derived(formatPercent(fraction));

  const queueText = $derived.by(() => {
    if (!d) return '';
    const name = queueName ?? (queueOf(d) === MAIN_QUEUE_ID ? 'Main queue' : '');
    if (!name) return '';
    return `${escapeBidi(name)}${d.state === 'queued' && d.queuePosition > 0 ? ` · position ${d.queuePosition}` : ''}`;
  });

  // ---- speed limit ----
  let limit = $state(0);
  $effect(() => {
    limit = d?.speedLimit ?? 0;
  });
  const limits = $derived(limitOptions(d?.speedLimit ?? 0));
  const canLimit = $derived(d !== undefined && d.state !== 'completed' && onlimit !== undefined);

  function chooseLimit(v: number): void {
    if (!d) return;
    if (v === CUSTOM_LIMIT) {
      limit = d.speedLimit;
      onprogress?.(d);
      return;
    }
    onlimit?.(d, v);
  }

  // ---- speed figures ----
  const stats = $derived(speedStats(history));
  const eta = $derived(d && live ? etaSeconds(d) : Number.NaN);
  const big = $derived.by(() => {
    if (!d) return null;
    if (live) return speedParts(d.speedBps);
    if (d.state === 'completed' && d.activeMs > 0 && d.size > 0) return speedParts((d.size / d.activeMs) * 1000);
    return null;
  });
  const statsText = $derived.by(() => {
    const avg = formatSpeed(stats.avg);
    const peak = formatSpeed(stats.peak);
    const unit = (s: string) => s.slice(s.lastIndexOf(' ') + 1);
    const avgShown = unit(avg) === unit(peak) ? avg.slice(0, avg.lastIndexOf(' ')) : avg;
    return { avg: avgShown, peak, limit: d && d.speedLimit > 0 ? formatSpeed(d.speedLimit) : 'none' };
  });

  // ---- the body's overflow ----
  /**
   * The pane has a fixed height (DESIGN-SYSTEM.md §2.3): when its body scrolls (a media
   * download's extra facts, an error note), the last visible line fades out instead of being
   * cut, until the end is scrolled into view. Re-measured on scroll, resize and each update.
   */
  let overflowing = $state(false);
  /** The body scrolls at all: it then takes the keyboard focus, so the arrow keys scroll it (WCAG 2.1.1). */
  let scrollable = $state(false);
  function moreBelow(node: HTMLElement, _version: unknown) {
    const measure = () => {
      overflowing = node.scrollHeight - node.scrollTop - node.clientHeight > 1;
      scrollable = node.scrollHeight - node.clientHeight > 1;
    };
    const ro = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(measure);
    ro?.observe(node);
    node.addEventListener('scroll', measure, { passive: true });
    measure();
    return {
      update: measure,
      destroy() {
        ro?.disconnect();
        node.removeEventListener('scroll', measure);
      },
    };
  }

  /**
   * Instead of an empty chart, what a download that is not running did last (it has no speed in
   * the last minute): finished, paused at how much, stopped when, queued where, not started.
   */
  const stoppedText = $derived.by((): { lead: string; bold: string; tail: string } | null => {
    if (!d || live || stats.peak > 0) return null;
    switch (d.state) {
      case 'completed':
        return d.completedAt > 0 ? { lead: 'Finished ', bold: dayTimeText(d.completedAt), tail: '' } : { lead: 'Finished', bold: '', tail: '' };
      case 'paused':
        return { lead: 'Paused · ', bold: formatBytes(d.received), tail: d.size >= 0 ? ` of ${formatSize(d)}` : '' };
      case 'failed':
      case 'canceled':
        return d.updatedAt > 0 ? { lead: 'Stopped ', bold: dayTimeText(d.updatedAt), tail: '' } : { lead: 'Stopped', bold: '', tail: '' };
      case 'queued':
        return { lead: 'Queued', bold: d.queuePosition > 0 ? ` #${d.queuePosition}` : '', tail: '' };
      case 'awaiting_confirmation':
        return { lead: 'Not started', bold: '', tail: '' };
      default:
        return null;
    }
  });

  // ---- selection strip ----
  const many = $derived(selected ?? []);
  const canPauseMany = $derived(!stale && (selected ? many.some((x) => isActive(x.state)) : true));
  const canResumeMany = $derived(!stale && (selected ? many.some((x) => isResumable(x.state)) : true));

  function guard(fn: () => void): () => void {
    return () => {
      if (!stale) fn();
    };
  }

  function more(e: MouseEvent): void {
    const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
    onmore?.(r.left, r.bottom + 4);
  }
</script>

{#if d && selectedCount <= 1}
  <section class="details" class:collapsed aria-labelledby="{uid}-title">
    <header class="head">
      <FileIcon kind={kindOf(d)} size={collapsed ? 24 : 32} />
      <div class="titles">
        <div class="title-line">
          <h2 class="title" id="{uid}-title"><bdi><FileName name={displayName(d)} /></bdi></h2>
          {#if media}
            <span class="tag badge media"><Icon name="stream" size={11} /><bdi>{escapeBidi(mediaBadge(d))}</bdi></span>
          {/if}
        </div>
        {#if !collapsed && subline}
          <p class="sub" dir="auto">{subline}</p>
        {/if}
      </div>
      <div class="actions">
        {#if pill}
          <span class="pill {pill.tone} badge state-{d.state}"><StatusMark kind={markOfState(d.state)} size={12} />{pill.text}</span>
        {/if}
        {#if d.state === 'awaiting_confirmation' && onconfirm}
          <button type="button" class="btn sm primary" aria-disabled={stale ? 'true' : undefined} onclick={guard(() => onconfirm?.(d))}>
            Review…
          </button>
        {:else if isActive(d.state) && onpause}
          <button type="button" class="btn sm" aria-disabled={stale ? 'true' : undefined} onclick={guard(() => onpause?.(d))}>
            <Icon name="pause" size={14} />Pause
          </button>
        {:else if d.state === 'paused' || d.state === 'canceled'}
          <button type="button" class="btn sm" aria-disabled={stale ? 'true' : undefined} onclick={guard(() => onresume(d))}>
            <Icon name="resume" size={14} />Resume
          </button>
        {:else if d.state === 'completed'}
          <!-- Downloads it again (ENGINE-SPEC §7.1); the dialog asks what happens to the file. -->
          <button type="button" class="btn sm" aria-disabled={stale ? 'true' : undefined} onclick={guard(() => onrestart(d))}>
            <Icon name="restart" size={14} />Download again…
          </button>
        {/if}
        <button type="button" class="btn sm ghost wide-only" aria-label="Show in folder" title="Show in folder" onclick={() => onopenfolder(d)}>
          <Icon name="folder" size={15} /><span class="label">Show in folder</span>
        </button>
        <button type="button" class="btn sm ghost wide-only" aria-label="Copy link" title="Copy link" onclick={() => oncopy(d)}>
          <Icon name="copy" size={15} /><span class="label">Copy link</span>
        </button>
        {#if onmore}
          <button
            type="button"
            class="icon-btn sm"
            aria-label="More download actions"
            title="More actions"
            aria-haspopup="menu"
            onclick={more}
          >
            <Icon name="more" size={16} />
          </button>
        {/if}
        {#if ontoggle}
          <span class="vsep" aria-hidden="true"></span>
          <button
            type="button"
            class="icon-btn sm toggle"
            aria-expanded={!collapsed}
            aria-controls="{uid}-body"
            aria-label={collapsed ? 'Show details' : 'Hide details'}
            title={collapsed ? 'Show details' : 'Hide details'}
            onclick={ontoggle}
          >
            <span class="chevron" class:up={collapsed}><Icon name="chevron-down" size={16} /></span>
          </button>
        {/if}
      </div>
    </header>

    <!-- When the body scrolls it is a named region that takes the focus, so the keyboard scrolls it too. -->
    <!-- svelte-ignore a11y_no_noninteractive_tabindex (a scrolling region must take the focus for its keys: WCAG 2.1.1) -->
    <div
      class="content"
      class:more={overflowing}
      id="{uid}-body"
      hidden={collapsed}
      use:moreBelow={d}
      tabindex={scrollable ? 0 : undefined}
      role={scrollable ? 'region' : undefined}
      aria-label={scrollable ? 'Download details' : undefined}
    >
      {#if d.state === 'awaiting_confirmation'}
        <p class="note info notice">
          {site
            ? 'This video from the browser waits until you choose its quality, folder and file name.'
            : media
              ? 'This stream from the browser waits until you choose its quality, folder and file name.'
              : 'This download from the browser waits until you confirm its folder and file name.'}
        </p>
      {/if}
      {#if d.state === 'failed' || d.error}
        <div class="note fail error">
          <span class="err-icon"><Icon name="alert" size={16} /></span>
          <div class="error-body">
            <p class="error-message"><bdi>{d.error ? escapeBidi(d.error) : 'The download failed.'}</bdi></p>
            {#if hint}
              <p class="hint">{hint}</p>
            {/if}
          </div>
          {#if d.errorCode}
            <code class="code" title="Error code">{d.errorCode}</code>
          {/if}
          {#if d.errorCode === 'auth_required' && onaddlogin && !site}
            <button type="button" class="btn sm" aria-disabled={stale ? 'true' : undefined} onclick={guard(() => onaddlogin?.(d))}>
              <Icon name="add" size={14} />Add site login…
            </button>
          {/if}
          {#if d.errorCode === 'validator_changed'}
            <button type="button" class="btn sm" aria-disabled={stale ? 'true' : undefined} onclick={guard(() => onrestart(d))}>
              <Icon name="restart" size={14} />Restart…
            </button>
          {:else if d.state === 'failed' && !refused}
            <button type="button" class="btn sm" aria-disabled={stale ? 'true' : undefined} onclick={guard(() => onresume(d))}>
              <Icon name="resume" size={14} />Retry
            </button>
          {/if}
        </div>
      {/if}

      <div class="columns">
        <dl class="facts">
          <dt>{addressLabel(d)}</dt>
          <dd class="with">
            <span class="mono value" dir="auto" title={escapeBidi(d.url)}>{escapeBidi(d.url)}</span>
            <!-- a mouse convenience: "Copy link" in the header does the same -->
            <button type="button" class="mini" tabindex="-1" aria-hidden="true" title="Copy link" onclick={() => oncopy(d)}>
              <Icon name="copy" size={13} />
            </button>
          </dd>
          {#if d.finalUrl && d.finalUrl !== d.url}
            <dt>Final URL</dt>
            <dd class="with"><span class="mono value" dir="auto" title={escapeBidi(d.finalUrl)}>{escapeBidi(d.finalUrl)}</span></dd>
          {/if}
          <dt>Saved to</dt>
          <dd class="with">
            <span class="mono value" dir="auto" title={joinPath(d.dir, d.filename)}>{joinPath(d.dir, d.filename)}</span>
            <button type="button" class="mini" tabindex="-1" aria-hidden="true" title="Show in folder" onclick={() => onopenfolder(d)}>
              <Icon name="folder" size={13} />
            </button>
          </dd>
          <dt>Received</dt>
          <dd>
            {#if d.state === 'awaiting_confirmation'}
              Not started{expected ? ` (${expected})` : ''}
            {:else}
              <b>{formatBytes(d.received)}</b>{#if d.size >= 0}{' '}of {formatSize(d)}{:else if !media}{' '}of unknown size{/if}{pct ? ` · ${pct}` : ''}
            {/if}
          </dd>
          <dt id="{uid}-limit">Speed limit</dt>
          <dd class="with">
            {#if canLimit}
              <Select
                bind:value={limit}
                options={limits}
                size="inline"
                ariaLabel="Download speed limit"
                disabled={stale}
                onchange={chooseLimit}
              />
            {:else}
              {d.speedLimit > 0 ? formatSpeed(d.speedLimit) : 'Unlimited'}
            {/if}
          </dd>
          {#if queueText}
            <dt>Queue</dt>
            <dd><bdi>{queueText}</bdi></dd>
          {/if}
          <dt>Added</dt>
          <dd>{addedText(d)}</dd>
          {#if media}
            <dt>{site ? 'Site' : 'Stream'}</dt>
            <dd>
              <bdi>{site ? escapeBidi(siteVideoLabel(media)) : formatLabel(media.format) || 'Unknown format'}</bdi>{media.durationMs > 0
                ? ` · ${formatDuration(media.durationMs)}`
                : ''}
            </dd>
            {#if tracks?.video}
              <dt>Video</dt>
              <dd><bdi>{escapeBidi(tracks.video)}</bdi></dd>
            {/if}
            {#if tracks?.audio}
              <dt>Audio</dt>
              <dd><bdi>{escapeBidi(tracks.audio)}</bdi></dd>
            {/if}
            <dt>Output</dt>
            <dd>{containerLabel(media.container) || 'Decided when the stream is assembled'}</dd>
          {/if}
          {#if d.completedAt > 0}
            <dt>Completed</dt>
            <dd>{formatDateTime(d.completedAt)}</dd>
          {/if}
        </dl>

        <div class="col">
          <ConnectionBoxes download={d} {stale} maxConnections={d.maxConnections || defaultConnections} />
        </div>

        <div class="col speed">
          {#if big}
            <div class="figures">
              <p class="big"><span class="n">{big.n}</span>{' '}<span class="unit">{big.unit}</span></p>
              <div class="when">
                {#if live}
                  <span class="left">{Number.isFinite(eta) ? `${formatEta(eta)} left` : 'Time left unknown'}</span>
                  {#if Number.isFinite(eta)}
                    <span class="done">Done ≈ {formatClock(Date.now() + eta * 1000)}</span>
                  {/if}
                {:else}
                  <span class="left">Took {timeTakenText(d)}</span>
                  <span class="done">Average speed</span>
                {/if}
              </div>
            </div>
          {:else}
            <h3 class="col-head">Speed <span class="sub">{stale && d.state === 'downloading' ? 'not current' : 'last 60 s'}</span></h3>
          {/if}
          {#if stoppedText}
            <!-- not running, no speed to chart: what it did last instead of an empty chart -->
            <p class="finished">{stoppedText.lead}{#if stoppedText.bold}<b>{stoppedText.bold}</b>{/if}{stoppedText.tail}</p>
            {#if d.state !== 'completed'}
              <p class="stats">Limit <b>{statsText.limit}</b></p>
            {/if}
          {:else}
            <SpeedChart samples={history} height={density.compact ? 42 : big ? 60 : 72} />
            <p class="stats">
              {#if stats.peak > 0}
                Avg <b>{statsText.avg}</b> · Peak <b>{statsText.peak}</b> · Limit <b>{statsText.limit}</b>
              {:else}
                Limit <b>{statsText.limit}</b>
              {/if}
            </p>
          {/if}
        </div>
      </div>
    </div>
  </section>
{:else if selectedCount > 1}
  <section class="details strip" aria-labelledby="{uid}-title">
    <h2 class="strip-title" id="{uid}-title">{selected ? selectionSummary(many) : `${selectedCount} downloads selected`}</h2>
    {#if onselection}
      <div class="actions">
        <button type="button" class="btn sm ghost" aria-disabled={canPauseMany ? undefined : 'true'} onclick={() => canPauseMany && onselection?.('pause')}>
          <Icon name="pause" size={14} />Pause
        </button>
        <button type="button" class="btn sm ghost" aria-disabled={canResumeMany ? undefined : 'true'} onclick={() => canResumeMany && onselection?.('resume')}>
          <Icon name="resume" size={14} />Resume
        </button>
        <button type="button" class="btn sm ghost danger-text" aria-disabled={stale ? 'true' : undefined} onclick={guard(() => onselection?.('remove'))}>
          <Icon name="remove" size={14} />Remove
        </button>
      </div>
    {/if}
  </section>
{/if}

<style>
  .details {
    display: flex;
    flex-direction: column;
    block-size: var(--details-h);
    min-block-size: 0;
    box-sizing: border-box;
    background: var(--surface-2);
    border-block-start: 1px solid var(--line);
    container-type: inline-size;
    overflow: hidden;
    animation: open var(--dur-4) var(--ease-out);
  }
  @keyframes open {
    from {
      block-size: 0;
    }
  }
  .details.collapsed,
  .details.strip {
    block-size: 44px;
    animation: none;
  }
  /* ---- header ---- */
  .head {
    display: flex;
    align-items: center;
    gap: 12px;
    flex: none;
    min-inline-size: 0;
    padding-block: 14px 0;
    padding-inline: 20px 12px;
  }
  .collapsed .head {
    block-size: 43px;
    padding-block: 0;
  }
  .head > :global(.tile) {
    flex: none;
    align-self: flex-start;
  }
  .collapsed .head > :global(.tile) {
    align-self: center;
  }
  .titles {
    flex: 1;
    min-inline-size: 0;
  }
  .title-line {
    display: flex;
    align-items: center;
    gap: 8px;
    min-inline-size: 0;
  }
  .title {
    min-inline-size: 0;
    font: 650 var(--type-section) / 1.2 var(--font-display);
    letter-spacing: -0.01em;
    color: var(--ink);
    overflow-wrap: anywhere;
    display: -webkit-box;
    -webkit-box-orient: vertical;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    overflow: hidden;
    user-select: text;
  }
  .collapsed .title {
    -webkit-line-clamp: 1;
    line-clamp: 1;
  }
  .sub {
    margin-block-start: 3px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--muted);
    font-size: var(--type-small);
  }
  .actions {
    display: flex;
    align-items: center;
    gap: 4px;
    flex: none;
  }
  .actions .pill {
    margin-inline-end: 6px;
  }
  .vsep {
    inline-size: 1px;
    block-size: 18px;
    margin-inline: 4px;
    background: var(--line);
  }
  .toggle[aria-expanded] {
    background: transparent;
    color: var(--muted);
  }
  .toggle:hover {
    background: var(--hover);
    color: var(--ink);
  }
  .chevron {
    display: grid;
    transition: rotate var(--dur-3) var(--ease-out);
  }
  .chevron.up {
    rotate: 180deg;
  }
  @container (max-width: 820px) {
    .wide-only .label {
      display: none;
    }
    .wide-only {
      padding-inline: 6px;
    }
  }
  /* ---- body ---- */
  .content {
    flex: 1;
    min-block-size: 0;
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding-block: 12px;
    overflow: auto;
    overscroll-behavior: contain;
  }
  .content[hidden] {
    display: none;
  }
  .content:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }
  .content.more {
    -webkit-mask-image: linear-gradient(to bottom, #000 calc(100% - 22px), transparent);
    mask-image: linear-gradient(to bottom, #000 calc(100% - 22px), transparent);
  }
  .notice,
  .error {
    margin-inline: 20px 16px;
  }
  .notice {
    padding-block: 6px;
  }
  .error {
    display: flex;
    align-items: center;
    gap: 10px;
    padding-block: 5px;
    color: var(--danger);
  }
  .err-icon {
    display: grid;
    flex: none;
  }
  .error-body {
    flex: 1;
    min-inline-size: 0;
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    column-gap: 8px;
  }
  .error-message {
    font-weight: 600;
    overflow-wrap: anywhere;
  }
  .error .hint {
    color: var(--ink-2);
    font-size: var(--type-small);
  }
  .code {
    flex: none;
    padding: 1px 5px;
    border: 1px solid var(--danger-line);
    border-radius: var(--r-xs);
    font: var(--mono-weight) 0.6875rem / 1.4 var(--font-mono);
  }
  .columns {
    flex: 1;
    min-block-size: 0;
    display: grid;
    grid-template-columns: minmax(0, 1.32fr) minmax(0, 1fr) minmax(0, 0.92fr);
  }
  .facts {
    display: grid;
    grid-template-columns: 76px minmax(0, 1fr);
    gap: 6px 12px;
    align-content: start;
    margin: 0;
    padding-inline: 20px;
  }
  dt {
    color: var(--faint);
    font-size: var(--type-small);
    white-space: nowrap;
  }
  dd {
    min-inline-size: 0;
    margin: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--ink-2);
    font-size: var(--type-cell);
    line-height: 18px;
  }
  dd.with {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  dt {
    line-height: 18px;
  }
  dd b {
    color: var(--ink);
    font-weight: 600;
  }
  .value {
    min-inline-size: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--ink-2);
    user-select: text;
  }
  .mini {
    display: grid;
    place-items: center;
    flex: none;
    inline-size: 22px;
    block-size: 22px;
    padding: 0;
    border: 0;
    border-radius: var(--r-xs);
    background: none;
    color: var(--faint);
    cursor: pointer;
  }
  .mini:hover {
    background: var(--hover);
    color: var(--ink);
  }
  /* the inline select keeps the facts' 18 px rhythm */
  .facts :global(.select.size-inline) {
    margin-block: -4px;
    margin-inline-start: -7px;
  }
  .col {
    min-inline-size: 0;
    padding-inline: 20px;
    border-inline-start: 1px solid var(--line);
  }
  /* ---- speed ---- */
  .speed {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .figures {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 12px;
  }
  .big {
    display: flex;
    align-items: baseline;
    gap: 4px;
    font: 650 var(--type-figure) / 1 var(--font-display);
    letter-spacing: -0.02em;
    color: var(--ink);
    white-space: nowrap;
  }
  .big .unit {
    font: 500 var(--type-body) / 1 var(--font-ui);
    letter-spacing: 0;
    color: var(--muted);
  }
  .col-head {
    display: flex;
    align-items: baseline;
    gap: 8px;
    font-size: var(--type-cell);
    font-weight: 600;
    color: var(--ink-2);
  }
  .col-head .sub {
    font-size: var(--type-small);
    font-weight: 400;
    color: var(--faint);
  }
  .when {
    display: grid;
    justify-items: end;
    gap: 3px;
    min-inline-size: 0;
    text-align: end;
  }
  .left {
    font-size: var(--type-body);
    font-weight: 600;
    color: var(--ink);
    white-space: nowrap;
  }
  .done {
    font-size: var(--type-small);
    color: var(--muted);
    white-space: nowrap;
  }
  .stats {
    color: var(--faint);
    font-size: var(--type-meta);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .finished {
    margin: 0;
    padding-block-start: 10px;
    border-block-start: 1px solid var(--line-soft);
    color: var(--muted);
    font-size: var(--type-small);
  }
  .finished b {
    color: var(--ink-2);
    font-weight: 600;
  }
  .stats b {
    color: var(--ink-2);
    font-weight: 600;
  }
  @container (max-width: 820px) {
    .facts,
    .col {
      padding-inline: 14px;
    }
    .facts {
      padding-inline-start: 20px;
    }
  }
  /* A narrow pane: the facts and the speed side by side, the connections under them. */
  @container (max-width: 640px) {
    .columns {
      grid-template-columns: minmax(0, 1.2fr) minmax(0, 1fr);
      row-gap: 14px;
    }
    .col.speed {
      grid-column: 2;
      grid-row: 1;
    }
    .columns > .col:not(.speed) {
      grid-column: 1 / -1;
      grid-row: 2;
      border-inline-start: 0;
    }
  }
  /* Compact: the shorter pane keeps every fact in view. */
  :global(html[data-density='compact']) .details:not(.collapsed) .head {
    padding-block-start: 10px;
  }
  :global(html[data-density='compact']) .content {
    gap: 8px;
    padding-block: 8px;
  }
  :global(html[data-density='compact']) .facts {
    row-gap: 4px;
  }
  /* ---- the multi-select strip ---- */
  .strip {
    flex-direction: row;
    align-items: center;
    gap: 12px;
    padding-inline: 20px 12px;
  }
  .strip-title {
    flex: 1;
    min-inline-size: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: var(--type-body);
    font-weight: 600;
    color: var(--ink-2);
  }
</style>
