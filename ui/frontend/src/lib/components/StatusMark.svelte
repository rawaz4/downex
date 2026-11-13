<script lang="ts" module>
  import type { DownloadState } from '../api';
  import type { StatusFilter } from '../categories';

  /** Each status has its own colour and its own shape, so neither alone carries it. */
  export type MarkKind = 'downloading' | 'queued' | 'paused' | 'failed' | 'completed' | 'awaiting' | 'canceled';

  export function markOfState(state: DownloadState): MarkKind {
    switch (state) {
      case 'downloading':
      case 'probing':
        return 'downloading';
      case 'paused':
        return 'paused';
      case 'failed':
        return 'failed';
      case 'completed':
        return 'completed';
      case 'awaiting_confirmation':
        return 'awaiting';
      case 'canceled':
        return 'canceled';
      default:
        return 'queued';
    }
  }

  export function markOfFilter(filter: Exclude<StatusFilter, 'any'>): MarkKind {
    return filter === 'active' ? 'downloading' : filter;
  }
</script>

<script lang="ts">
  interface Props {
    kind: MarkKind;
    /** Pixels; the marks are drawn on a 14×14 grid (DESIGN-SYSTEM.md §2.6.2). */
    size?: number;
    /** Draw the check stroke in (a download that just completed). */
    morph?: boolean;
  }

  let { kind, size = 14, morph = false }: Props = $props();
</script>

<svg
  class="mark mark-{kind}"
  class:morph
  width={size}
  height={size}
  viewBox="0 0 14 14"
  aria-hidden="true"
  focusable="false"
>
  {#if kind === 'downloading'}
    <circle class="tone" cx="7" cy="7" r="6.2" />
    <path class="on" d="M7 3.9v5.2M4.8 7.1 7 9.3l2.2-2.2" />
  {:else if kind === 'queued'}
    <circle class="line" cx="7" cy="7" r="5.3" stroke-width="1.4" />
    <path class="line" d="M7 4.7V7l1.6 1" stroke-width="1.3" />
  {:else if kind === 'paused'}
    <rect class="tone" x="3.4" y="2.8" width="2.6" height="8.4" rx="0.9" />
    <rect class="tone" x="8" y="2.8" width="2.6" height="8.4" rx="0.9" />
  {:else if kind === 'failed'}
    <path class="tone" d="M7 1.2 12.8 7 7 12.8 1.2 7Z" />
    <path class="on" d="M7 4.4v3.1M7 9.4v.1" />
  {:else if kind === 'completed'}
    <path class="line check" d="M2.8 7.4l2.7 2.7L11.2 4.4" pathLength="1" />
  {:else if kind === 'awaiting'}
    <rect class="tone" x="1" y="1" width="12" height="12" rx="3.5" />
    <path class="on" d="M4 7h5.6M7.4 4.6 9.8 7 7.4 9.4" />
  {:else}
    <path class="line" d="M3.5 7h7" stroke-width="1.6" />
  {/if}
</svg>

<style>
  .mark {
    flex: none;
    display: block;
    forced-color-adjust: auto;
  }
  .tone {
    fill: currentColor;
  }
  .line {
    fill: none;
    stroke: currentColor;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  /* the glyph on a filled shape: arrow, "!" */
  .on {
    fill: none;
    stroke: var(--tone-on, #fff);
    stroke-width: 1.5;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  .mark-downloading .on {
    stroke: var(--accent-on);
  }
  .check {
    stroke-width: 1.7;
  }
  .mark-downloading {
    color: var(--accent);
  }
  .mark-queued {
    color: var(--faint);
  }
  .mark-paused {
    color: var(--warn);
  }
  .mark-failed {
    color: var(--danger);
  }
  .mark-completed {
    color: var(--faint);
  }
  .mark-awaiting {
    color: var(--info);
  }
  .mark-canceled {
    color: var(--faint);
  }
  .morph .check {
    stroke-dasharray: 1;
    stroke-dashoffset: 1;
    animation: draw 420ms var(--ease-out) 80ms forwards;
  }
  .morph {
    animation: settle 420ms var(--ease-out);
  }
  @keyframes draw {
    to {
      stroke-dashoffset: 0;
    }
  }
  @keyframes settle {
    from {
      scale: 0.6;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .morph .check {
      stroke-dashoffset: 0;
    }
  }
  @media (forced-colors: active) {
    .mark {
      color: CanvasText;
    }
    .on {
      stroke: Canvas;
    }
  }
</style>
