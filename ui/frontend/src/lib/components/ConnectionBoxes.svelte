<script lang="ts">
  // The Connections column of the details pane (DESIGN-SYSTEM.md §3.15, from B): one box per
  // connection of a running download, each showing how far it is through its current range and
  // its own speed, adding up to "Together" (a stream's too; the fragment is in the tooltip). Without live
  // connections (paused, queued, finished, or downexd unreachable) the column shows the file's
  // static segment overview. The drawing is a progressbar named by what it measures; the speeds
  // and states are in each box's tooltip and in the text beside it.
  import type { Download } from '../api';
  import { connectionBoxes, receivedOf, showsConnections } from '../display';
  import { formatPercent, formatSpeed, progressFraction } from '../format';
  import { pieceNames } from '../progress';
  import { segmentCells } from '../segments';

  interface Props {
    download: Download;
    /** The daemon is unreachable: nothing shown is current. */
    stale?: boolean;
    /** The most connections it may use (its own limit, else the default), said while it is not running. */
    maxConnections?: number;
  }

  let { download: d, stale = false, maxConnections = 0 }: Props = $props();

  const media = $derived(d.kind === 'media');
  const pieces = $derived(pieceNames(d));
  const live = $derived(showsConnections(d, stale));
  const summary = $derived(live ? connectionBoxes(d.connectionInfo, { media, piece: pieces.one }) : null);
  const cells = $derived(live ? [] : segmentCells(d, 8));
  const fraction = $derived(progressFraction(d));
  const title = $derived(media ? pieces.many : 'Connections');

  const sub = $derived.by(() => {
    if (media) {
      const m = d.media;
      return m && m.fragmentsTotal > 0 ? `${m.fragmentsDone} of ${m.fragmentsTotal}` : 'counted when the download starts';
    }
    // nothing received of an unknown size says nothing
    const got = d.received > 0 || d.size >= 0 ? receivedOf(d.received, d.size, d.sizeApprox) : '';
    const n = live ? d.connectionInfo.length : d.state === 'downloading' ? d.connections : 0;
    const parts =
      n > 0
        ? [`${n} connection${n === 1 ? '' : 's'}`, got]
        : d.state === 'completed' || d.mode === 'single' || !(maxConnections > 0)
          ? [got]
          : [`Up to ${maxConnections}`, got];
    return parts.filter((x) => x !== '').join(' · ');
  });

  /** Without live connections: why. */
  const idleText = $derived.by(() => {
    if (stale) return 'Not current: downexd is unreachable';
    switch (d.state) {
      case 'completed':
        return 'All received';
      case 'awaiting_confirmation':
        return 'Not started';
      case 'downloading':
        return '';
      default:
        return 'Not running';
    }
  });

  const value = $derived(Number.isFinite(fraction) ? Math.floor(fraction * 100) : undefined);
  /** More than 8 boxes take two rows; the legend then gives way to them. */
  const twoRows = $derived(summary !== null && summary.boxes.length > 8);
</script>

<div class="conns">
  <h3 class="head">
    {title}
    <span class="sub">{sub}</span>
  </h3>
  <div
    class="boxes"
    class:two-rows={twoRows}
    style:--per-row={Math.max(1, Math.min(8, summary ? summary.boxes.length : cells.length))}
    role="progressbar"
    aria-label={media ? `${pieces.many} downloaded` : 'Received'}
    aria-valuemin={0}
    aria-valuemax={100}
    aria-valuenow={value}
    aria-valuetext={value === undefined ? 'Unknown' : formatPercent(fraction)}
  >
    {#if summary}
      {#each summary.boxes as b (b.key)}
        <div class="slot" title={b.title}>
          <span class="box tone-{b.tone}" class:more={b.more !== undefined}>
            {#if b.more !== undefined}
              <span class="more-n">+{b.more}</span>
            {:else}
              <span class="fill" style:--f={b.fraction}></span>
            {/if}
          </span>
          <span class="speed" class:idle={b.speed <= 0}>{b.label}</span>
        </div>
      {/each}
    {:else}
      {#each cells as c, i (i)}
        <div class="slot">
          <span class="box" class:tone-live={c.live} class:hatch={c.indeterminate && c.live}>
            {#if !c.indeterminate}<span class="fill tone-{d.state}" style:--f={c.fraction}></span>{/if}
          </span>
        </div>
      {/each}
    {/if}
  </div>
  <p class="foot">
    {#if summary}
      <span class="together"
        >Together <b>{formatSpeed(summary.together)}</b>{#if summary.unit}<span class="unit">{` · each in ${summary.unit}`}</span>{/if}</span
      >
    {:else if idleText}
      <span class="together">{idleText}</span>
    {:else}
      <span class="together">Together <b>{formatSpeed(d.speedBps)}</b></span>
    {/if}
    <span class="legend" class:hidden={twoRows} aria-hidden="true">
      <!-- the static cells are in the state's tone (paused amber, failed red): so is the swatch -->
      <span><i class="sw sw-fill {summary ? '' : `tone-${d.state}`}"></i>Received</span>
      {#if summary}<span><i class="sw sw-edge"></i>Connection</span>{/if}
      <span><i class="sw sw-track"></i>Remaining</span>
    </span>
  </p>
</div>

<style>
  .conns {
    display: flex;
    flex-direction: column;
    gap: 9px;
    min-inline-size: 0;
  }
  .head {
    display: flex;
    align-items: baseline;
    gap: 8px;
    min-inline-size: 0;
    font-size: var(--type-cell);
    font-weight: 600;
    color: var(--ink-2);
    white-space: nowrap;
  }
  .sub {
    overflow: hidden;
    text-overflow: ellipsis;
    font-size: var(--type-small);
    font-weight: 400;
    color: var(--faint);
  }
  .boxes {
    display: grid;
    grid-template-columns: repeat(var(--per-row, 8), minmax(0, 1fr));
    gap: 6px 4px;
  }
  .slot {
    display: grid;
    gap: 3px;
    min-inline-size: 0;
  }
  .box {
    position: relative;
    block-size: 26px;
    overflow: hidden;
    border-radius: var(--r-xs);
    background: var(--seg-track);
  }
  /* Two rows of boxes in the short Compact pane: a little lower, so the speeds stay in view. */
  :global(html[data-density='compact']) .two-rows .box {
    block-size: 18px;
  }
  .fill {
    position: absolute;
    inset: 0;
    background: var(--seg-fill);
    transform: translateX(calc((var(--f, 0) - 1) * 100%));
    /* no slide: it steps with each progress report (see SegmentBar) */
  }
  :global([dir='rtl']) .fill {
    transform: translateX(calc((1 - var(--f, 0)) * 100%));
  }
  /* the live front: a static 2 px line where the connection writes */
  .tone-live .fill {
    box-shadow: inset -2px 0 0 var(--seg-edge);
  }
  :global([dir='rtl']) .tone-live .fill {
    box-shadow: inset 2px 0 0 var(--seg-edge);
  }
  .tone-retry {
    background: var(--warn-soft);
    box-shadow: inset 0 0 0 1px var(--seg-paused);
  }
  .tone-retry .fill {
    background: var(--seg-paused);
  }
  .tone-error {
    background: var(--danger-soft);
    box-shadow: inset 0 0 0 1px var(--seg-failed);
  }
  .tone-error .fill {
    background: var(--seg-failed);
  }
  .fill.tone-paused,
  .sw-fill.tone-paused {
    background: var(--seg-paused);
  }
  .fill.tone-failed,
  .sw-fill.tone-failed {
    background: var(--seg-failed);
  }
  .fill.tone-queued,
  .fill.tone-canceled,
  .sw-fill.tone-queued,
  .sw-fill.tone-canceled {
    background: var(--seg-idle);
  }
  .more {
    display: grid;
    place-items: center;
    background: var(--surface);
    box-shadow: inset 0 0 0 1px var(--line-strong);
  }
  .more-n {
    font-size: var(--type-small);
    font-weight: 600;
    color: var(--ink-2);
  }
  .hatch {
    background: repeating-linear-gradient(-45deg, var(--seg-fill) 0 4px, var(--seg-track) 4px 8px);
    background-size: 11.32px 100%;
    animation: drift 1.2s linear infinite;
  }
  @keyframes drift {
    to {
      background-position: 11.32px 0;
    }
  }
  /* A label may use the 4 px gaps beside its box ("#123" over a narrow box in a small window). */
  .speed {
    margin-inline: -2px;
    overflow: hidden;
    text-align: center;
    text-overflow: clip;
    white-space: nowrap;
    font-size: var(--type-meta);
    line-height: 1.2;
    color: var(--ink-2);
  }
  .speed.idle {
    color: var(--faint);
  }
  .foot {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    justify-content: space-between;
    gap: 4px 12px;
    font-size: var(--type-meta);
    color: var(--faint);
  }
  .together b {
    color: var(--ink);
    font-weight: 600;
    font-size: var(--type-small);
  }
  .legend {
    display: inline-flex;
    gap: 10px;
  }
  .legend.hidden {
    display: none;
  }
  .together .unit {
    color: var(--faint);
  }
  .legend > span {
    display: inline-flex;
    align-items: center;
    gap: 5px;
  }
  .sw {
    inline-size: 9px;
    block-size: 9px;
    border-radius: 2px;
  }
  .sw-fill {
    background: var(--seg-fill);
  }
  .sw-edge {
    background: var(--seg-edge);
  }
  .sw-track {
    background: var(--seg-track);
  }
  @media (forced-colors: active) {
    .box {
      border: 1px solid CanvasText;
      background: Canvas;
    }
    .fill {
      forced-color-adjust: none;
      background: Highlight;
      box-shadow: none;
    }
  }
</style>
