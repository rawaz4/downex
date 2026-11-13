<script lang="ts">
  // A single progress bar (DESIGN-SYSTEM.md §3.10): the tools toast, the Add dialog's setup
  // and the progress window without segments. 6 px in rows, 8 px elsewhere; the track is
  // --seg-track, the fill follows the state, and the percentage beside it (12.5/600, tabular)
  // is the value. Rows use SegmentBar.
  import type { DownloadState } from '../api';
  import { formatPercent } from '../format';
  import { untrack } from 'svelte';
  import { ticking } from '../motion.svelte';

  interface Props {
    /** 0..1, or NaN when the size is unknown. */
    fraction: number;
    state: DownloadState;
    label?: string;
    /**
     * Table rows: the bar is hidden from assistive technology and only the percentage
     * text is exposed, so a focused row is not re-announced on every progress update.
     */
    decorative?: boolean;
    /** How long the fill and percentage take to ease to a new report (ms). */
    ease?: number;
    /** sm 6 px, md 8 px (default: sm when decorative, else md). */
    size?: 'sm' | 'md';
  }

  let { fraction, state, label = 'Progress', decorative = false, ease = 360, size }: Props = $props();

  // The one easing of the bar: a frame tween of the shown fraction (fill and percentage);
  // the accessible value stays the reported one.
  const eased = ticking(() => fraction, untrack(() => ease));

  const indeterminate = $derived(!Number.isFinite(fraction));
  const pct = $derived(indeterminate ? 0 : Math.floor(fraction * 1000) / 10);
  const text = $derived(indeterminate || !Number.isFinite(eased.current) ? '' : formatPercent(eased.current));
  /** How much of the track the fill covers, 0..1. */
  const shown = $derived(
    indeterminate ? (state === 'downloading' ? 1 : 0) : Math.min(1, Math.max(0, Number.isFinite(eased.current) ? eased.current : fraction)),
  );
  const height = $derived(size ?? (decorative ? 'sm' : 'md'));
</script>

<div class="progress size-{height}">
  {#if decorative}
    <div class="bar state-{state}" class:indeterminate={indeterminate && state === 'downloading'} aria-hidden="true">
      <div class="fill" style:--f={shown}></div>
    </div>
  {:else}
    <div
      class="bar state-{state}"
      class:indeterminate={indeterminate && state === 'downloading'}
      role="progressbar"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={indeterminate ? undefined : Math.floor(pct)}
      aria-valuetext={indeterminate ? 'Size unknown' : formatPercent(fraction)}
    >
      <div class="fill" style:--f={shown}></div>
    </div>
  {/if}
  <span class="pct tnum"><bdi>{text}</bdi>{#if decorative && indeterminate}<span class="sr-only">Size unknown</span>{/if}</span>
</div>

<style>
  .progress {
    display: flex;
    align-items: center;
    gap: var(--sp-5);
    inline-size: 100%;
  }
  .bar {
    position: relative;
    flex: 1;
    block-size: 8px;
    border-radius: var(--r-pill);
    background: var(--seg-track);
    overflow: hidden;
  }
  .size-sm .bar {
    block-size: 6px;
  }
  /* A full-width fill slid in from the start edge: it moves on the compositor (no layout per
     frame) and keeps its rounded leading edge. The script eases it, so no transition here. */
  .fill {
    position: absolute;
    inset: 0;
    border-radius: inherit;
    background: var(--seg-fill);
    transform: translateX(calc((var(--f, 0) - 1) * 100%));
    transition: background-color var(--dur-2) ease;
  }
  :global([dir='rtl']) .fill {
    transform: translateX(calc((1 - var(--f, 0)) * 100%));
  }
  /* the leading edge of a running transfer (2 px, static; it travels with the fill) */
  .state-downloading:not(.indeterminate) .fill {
    box-shadow: inset -2px 0 0 var(--seg-edge);
  }
  :global([dir='rtl']) .state-downloading:not(.indeterminate) .fill {
    box-shadow: inset 2px 0 0 var(--seg-edge);
  }
  .state-paused .fill {
    background: var(--seg-paused);
  }
  .state-canceled .fill,
  .state-queued .fill {
    background: var(--seg-idle);
  }
  .state-failed .fill {
    background: var(--seg-failed);
  }
  /* unknown size while bytes arrive: a drifting hatch (still under reduced motion) */
  .indeterminate .fill {
    background-image: repeating-linear-gradient(-45deg, var(--seg-fill) 0 4px, var(--seg-track) 4px 8px);
    background-size: 11.32px 100%;
    animation: drift 1.2s linear infinite;
  }
  .pct {
    flex: none;
    inline-size: 3.2em;
    text-align: end;
    font-size: var(--type-cell);
    font-weight: 600;
    color: var(--ink);
  }
  .state-paused + .pct {
    color: var(--warn);
  }
  .state-failed + .pct {
    color: var(--danger);
  }
  @keyframes drift {
    to {
      background-position: 11.32px 0;
    }
  }
  @media (forced-colors: active) {
    .bar {
      border: 1px solid CanvasText;
    }
    .fill {
      forced-color-adjust: none;
      background: Highlight;
    }
  }
</style>
