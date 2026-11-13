<script lang="ts">
  // Speed over the last minute (DESIGN-SYSTEM.md §3.10). `panel` (default): a 44 px chart with
  // a baseline and a "Now · Peak" caption. `mini`: 64 × 16 for the status bar, no axes, no
  // baseline, no caption. A Mint line over a Mint fill, redrawn once per sample: a slide
  // between samples kept a frame loop (an SVG layout and paint per frame) running for as long
  // as anything downloads. The chart is an image named by its values.
  import { HISTORY_MS, type SpeedSample } from '../stores/downloads.svelte';
  import { formatSpeed } from '../format';

  interface Props {
    samples: readonly SpeedSample[];
    variant?: 'panel' | 'mini';
    /** The image's name; by default "Speed over the last minute: now …, peak …". */
    label?: string;
  }

  let { samples, variant = 'panel', label: given }: Props = $props();

  const mini = $derived(variant === 'mini');
  const W = $derived(mini ? 64 : 240);
  const H = $derived(mini ? 16 : 44);

  const peak = $derived(samples.reduce((m, s) => Math.max(m, s.v), 0));
  const latest = $derived(samples.length > 0 ? (samples[samples.length - 1] as SpeedSample) : undefined);

  const paths = $derived.by(() => {
    if (!latest || samples.length < 2 || peak <= 0) return null;
    const from = latest.t - HISTORY_MS;
    // the mini chart keeps a pixel of air above the peak and draws down to its floor
    const top = mini ? 1.5 : 4;
    const bottom = mini ? 0.75 : 2;
    const pts: [number, number][] = [];
    for (const s of samples) {
      if (s.t < from) continue;
      const x = ((s.t - from) / HISTORY_MS) * W;
      const y = H - bottom - (s.v / peak) * (H - top - bottom);
      pts.push([x, y]);
    }
    const head = pts[0];
    const tail = pts[pts.length - 1];
    if (!head || !tail || pts.length < 2) return null;
    const line = pts.map(([x, y], i) => `${i === 0 ? 'M' : 'L'}${x.toFixed(1)} ${y.toFixed(1)}`).join('');
    return { line, area: `${line}L${tail[0].toFixed(1)} ${H}L${head[0].toFixed(1)} ${H}Z` };
  });

  const label = $derived(
    given ??
      (paths ? `Speed over the last minute: now ${formatSpeed(latest?.v ?? 0)}, peak ${formatSpeed(peak)}` : 'No speed samples yet'),
  );
</script>

{#snippet chart()}
  <svg class:mini viewBox="0 0 {W} {H}" width={mini ? W : undefined} height={mini ? H : undefined} preserveAspectRatio="none" role="img" aria-label={label}>
    {#if !mini}<line x1="0" x2={W} y1={H - 1} y2={H - 1} class="base" />{/if}
    {#if paths}
      <path d={paths.area} class="area" />
      <path d={paths.line} class="line" />
    {/if}
  </svg>
{/snippet}

{#if mini}
  {@render chart()}
{:else}
  <div class="spark">
    {@render chart()}
    <p class="caption">
      {#if paths}
        <span>Now <span class="tnum">{formatSpeed(latest?.v ?? 0)}</span></span>
        <span>Peak <span class="tnum">{formatSpeed(peak)}</span></span>
      {:else}
        <span>Speed history appears while downloading</span>
      {/if}
    </p>
  </div>
{/if}

<style>
  .spark {
    display: grid;
    gap: var(--sp-2);
  }
  svg {
    display: block;
    inline-size: 100%;
    block-size: 44px;
    border-radius: var(--r-xs);
    background: var(--surface-2);
    overflow: hidden;
  }
  svg.mini {
    flex: none;
    inline-size: 64px;
    block-size: 16px;
    border-radius: 0;
    background: none;
  }
  :global([dir='rtl']) svg {
    transform: scaleX(-1);
  }
  .base {
    stroke: var(--line);
    stroke-width: 1;
    vector-effect: non-scaling-stroke;
  }
  .area {
    fill: var(--chart-fill);
  }
  .line {
    fill: none;
    stroke: var(--chart-line);
    stroke-width: 1.5;
    stroke-linejoin: round;
    stroke-linecap: round;
    vector-effect: non-scaling-stroke;
  }
  .caption {
    display: flex;
    justify-content: space-between;
    gap: var(--sp-4);
    font-size: var(--type-small);
    color: var(--muted);
    font-variant-numeric: tabular-nums;
  }
  @media (forced-colors: active) {
    .line {
      stroke: CanvasText;
    }
    .area {
      fill: none;
    }
  }
</style>
