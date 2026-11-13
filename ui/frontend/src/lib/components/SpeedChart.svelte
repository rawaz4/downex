<script lang="ts">
  // The details pane's speed over the last minute (DESIGN-SYSTEM.md §3.15): a Mint line over a
  // Mint fill, dashed grid lines at the top and the middle of a y-axis drawn at the end (0, half
  // and the peak rounded up to 1/2/5), and a dot on the newest sample. Decorative with a text
  // alternative: the image is named by its values; the pane prints them under it too.
  import { HISTORY_MS, type SpeedSample } from '../stores/downloads.svelte';
  import { speedAxis, speedStats } from '../display';
  import { formatSpeed } from '../format';

  interface Props {
    samples: readonly SpeedSample[];
    /** Height in px of the plot. */
    height?: number;
    /** The image's name; by default "Speed over the last minute: …". */
    label?: string;
  }

  let { samples, height = 56, label: given }: Props = $props();

  let measured = $state(0);
  /** jsdom (and a first paint) measure nothing: draw at a nominal width. */
  const W = $derived(measured > 0 ? measured : 220);
  const H = $derived(height);
  const TOP = 5;
  const bottom = $derived(H - 1);

  const stats = $derived(speedStats(samples));
  const axis = $derived(speedAxis(stats.peak));
  const yOf = (v: number) => bottom - (Math.min(v, axis.max) / axis.max) * (bottom - TOP);
  const mid = $derived(yOf(axis.max / 2));

  const paths = $derived.by(() => {
    const latest = samples.at(-1);
    if (!latest || stats.shown.length < 2) return null;
    const from = latest.t - HISTORY_MS;
    // keep the end dot inside the plot
    const right = W - 4;
    const pts = stats.shown.map((s) => [((s.t - from) / HISTORY_MS) * right, yOf(s.v)] as const);
    const head = pts[0];
    const tail = pts[pts.length - 1];
    if (!head || !tail) return null;
    const line = pts.map(([x, y], i) => `${i === 0 ? 'M' : 'L'}${x.toFixed(1)} ${y.toFixed(1)}`).join('');
    return { line, area: `${line}L${tail[0].toFixed(1)} ${bottom}L${head[0].toFixed(1)} ${bottom}Z`, end: tail };
  });

  const label = $derived(
    given ??
      (paths
        ? `Speed over the last minute: now ${formatSpeed(samples.at(-1)?.v ?? 0)}, average ${formatSpeed(stats.avg)}, peak ${formatSpeed(stats.peak)}`
        : 'Speed over the last minute: no samples yet'),
  );
</script>

<div class="chart" style:--h="{H}px">
  <div class="plot" bind:clientWidth={measured}>
    {#if !paths}<span class="none" aria-hidden="true">No speed in the last minute</span>{/if}
    <svg width={W} height={H} viewBox="0 0 {W} {H}" role="img" aria-label={label}>
      <line class="grid" x1="0" x2={W} y1={TOP} y2={TOP} />
      <line class="grid" x1="0" x2={W} y1={mid} y2={mid} />
      <line class="base" x1="0" x2={W} y1={bottom} y2={bottom} />
      {#if paths}
        <path class="area" d={paths.area} />
        <path class="line" d={paths.line} />
        <circle class="dot" cx={paths.end[0]} cy={paths.end[1]} r="3" />
      {/if}
    </svg>
  </div>
  <div class="axis" class:blank={!paths} aria-hidden="true">
    <!-- the widest label sizes the column; the ticks sit on their lines -->
    <span class="sizer">{axis.labels[0]}</span>
    <span class="sizer">{axis.labels[1]}</span>
    <span class="tick" style:top="{TOP}px">{axis.labels[0]}</span>
    <span class="tick" style:top="{mid}px">{axis.labels[1]}</span>
    <span class="tick" style:top="{bottom}px">{axis.labels[2]}</span>
  </div>
</div>

<style>
  .chart {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    column-gap: 8px;
    block-size: var(--h);
  }
  .plot {
    position: relative;
    min-inline-size: 0;
    overflow: hidden;
  }
  .none {
    position: absolute;
    inset: 0;
    display: grid;
    place-items: center;
    color: var(--faint);
    font-size: var(--type-meta);
  }
  svg {
    display: block;
  }
  .grid {
    stroke: var(--chart-grid);
    stroke-width: 1;
    stroke-dasharray: 3 3;
  }
  .base {
    stroke: var(--chart-grid);
    stroke-width: 1;
  }
  .area {
    fill: var(--chart-fill);
  }
  .line {
    fill: none;
    stroke: var(--chart-line);
    stroke-width: 1.6;
    stroke-linejoin: round;
    stroke-linecap: round;
  }
  .dot {
    fill: var(--surface);
    stroke: var(--chart-line);
    stroke-width: 1.6;
  }
  .axis {
    position: relative;
    display: grid;
    align-content: start;
    justify-items: end;
    block-size: var(--h);
    color: var(--faint);
    font-size: 0.6875rem;
    line-height: 1;
    white-space: nowrap;
  }
  .sizer {
    grid-area: 1 / 1;
    visibility: hidden;
    block-size: 0;
  }
  .blank .tick:not(:last-child) {
    visibility: hidden;
  }
  .tick {
    position: absolute;
    inset-inline-end: 0;
    translate: 0 -50%;
  }
  @media (forced-colors: active) {
    .line,
    .dot {
      stroke: CanvasText;
    }
    .area {
      fill: none;
    }
  }
</style>
