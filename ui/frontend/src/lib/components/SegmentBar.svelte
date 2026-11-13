<script lang="ts" module>
  import type { DownloadState } from '../api';

  export type SegmentBarSize = 'row' | 'compact' | 'mini' | 'lg';
  export type SegmentTone = 'fill' | 'paused' | 'failed' | 'idle';

  /** The fill colour of a state (§3.12): live and done Mint, paused amber, failed red, waiting grey. */
  export function toneOf(state: DownloadState): SegmentTone {
    switch (state) {
      case 'downloading':
      case 'probing':
      case 'completed':
        return 'fill';
      case 'paused':
        return 'paused';
      case 'failed':
        return 'failed';
      default:
        return 'idle';
    }
  }
</script>

<script lang="ts">
  // The progress bar of rows and the progress window (DESIGN-SYSTEM.md §3.13). Rows (row, compact,
  // mini) draw one solid fill of the overall progress. The progress window (lg) draws the
  // connection map: at most 8 cells (fewer when narrow, each at least 12 px, never more than 10 px
  // chunks of fill; one cell when not running) with 2 px gaps that show the ground. A fill runs
  // from the inline start in the state's colour, with a static 2 px live edge where a connection is
  // writing. No sheen, no blinking. Decorative: the percentage beside it is the value.
  import { MAX_CELLS, barCells, rowCell, type SegmentSource } from '../segments';

  interface Props {
    download: SegmentSource;
    /** row 6 px, compact 4 px, mini 4 px × 90 px (palette), lg 8 px (progress window). */
    size?: SegmentBarSize;
    maxCells?: number;
    /** Overrides the colour that follows the state. */
    tone?: SegmentTone;
  }

  let { download, size = 'row', maxCells = MAX_CELLS, tone }: Props = $props();

  let width = $state(0);
  const cells = $derived(size === 'lg' ? barCells(download, width, maxCells) : [rowCell(download)]);
  const shownTone = $derived(tone ?? toneOf(download.state));
</script>

<div class="segbar size-{size} tone-{shownTone}" aria-hidden="true" bind:clientWidth={width} data-cells={cells.length}>
  {#each cells as cell, i (i)}
    <span class="cell" class:live={cell.live && !cell.indeterminate} class:hatch={cell.indeterminate && cell.live}>
      {#if !cell.indeterminate}
        <span class="fill" class:started={cell.fraction > 0} style:--f={cell.fraction}></span>
      {/if}
    </span>
  {/each}
</div>

<style>
  .segbar {
    --seg-h: 6px;
    --seg-r: 3px;
    --tone: var(--seg-fill);
    display: flex;
    gap: 2px;
    flex: 1 1 auto;
    min-inline-size: 0;
    block-size: var(--seg-h);
  }
  .size-compact {
    --seg-h: 4px;
    --seg-r: 2px;
  }
  .size-mini {
    --seg-h: 4px;
    --seg-r: 2px;
    flex: none;
    inline-size: 90px;
  }
  .size-lg {
    --seg-h: 8px;
    --seg-r: 3px;
  }
  .tone-paused {
    --tone: var(--seg-paused);
  }
  .tone-failed {
    --tone: var(--seg-failed);
  }
  .tone-idle {
    --tone: var(--seg-idle);
  }
  .cell {
    position: relative;
    flex: 1 1 0;
    min-inline-size: 2px;
    overflow: hidden;
    background: var(--seg-track);
  }
  /* rounded outer ends, square inner corners */
  .cell:first-child {
    border-start-start-radius: var(--seg-r);
    border-end-start-radius: var(--seg-r);
  }
  .cell:last-child {
    border-start-end-radius: var(--seg-r);
    border-end-end-radius: var(--seg-r);
  }
  /* A full-width fill slid in from the start edge: it moves on the compositor and keeps its
     2 px live edge whatever its share. A started cell shows at least 2 px. It steps with
     each progress report and does not slide: a 360 ms slide restarted every 250 ms kept the
     window animating for the whole download (2026-10-30: downloads ran about 5% slower
     with the window open). */
  .fill {
    position: absolute;
    inset: 0;
    background: var(--tone);
    transform: translateX(calc((var(--f, 0) - 1) * 100%));
    transition: background-color var(--dur-2) ease;
  }
  .fill.started {
    transform: translateX(max(calc((var(--f, 0) - 1) * 100%), calc(2px - 100%)));
  }
  .live .fill {
    box-shadow: inset -2px 0 0 var(--seg-edge);
  }
  :global([dir='rtl']) .fill {
    transform: translateX(calc((1 - var(--f, 0)) * 100%));
  }
  :global([dir='rtl']) .fill.started {
    transform: translateX(min(calc((1 - var(--f, 0)) * 100%), calc(100% - 2px)));
  }
  :global([dir='rtl']) .live .fill {
    box-shadow: inset 2px 0 0 var(--seg-edge);
  }
  /* Unknown size while bytes arrive: a hatch that drifts (still under reduced motion). */
  .hatch {
    background: repeating-linear-gradient(-45deg, var(--tone) 0 4px, var(--seg-track) 4px 8px);
    background-size: 11.32px 100%;
    animation: drift 1.2s linear infinite;
  }
  @keyframes drift {
    to {
      background-position: 11.32px 0;
    }
  }
  @media (forced-colors: active) {
    .cell {
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
