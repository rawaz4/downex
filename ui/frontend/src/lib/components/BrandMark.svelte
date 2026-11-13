<script lang="ts" module>
  // The Downex logo (DESIGN-SYSTEM.md §2.6.4): the app icon of internal/brand, a "D" of four
  // download segments, square on the left and round on the right, whose third bar is split
  // with its last chunk still arriving. The same in both themes (it is the exe, taskbar, tray
  // and extension icon). Laid out like internal/brand's appTile and fit, snapped to whole
  // pixels at every size so that small marks stay sharp.

  export interface BrandBar {
    readonly x: number;
    readonly y: number;
    readonly w: number;
    readonly h: number;
    /** Radius of the round right end (0: square). */
    readonly r: number;
    /** The live chunk. */
    readonly head: boolean;
  }

  export interface BrandLayout {
    /** The tile's square, in pixels. */
    readonly x0: number;
    readonly x1: number;
    readonly radius: number;
    /** Width of the hairline edge (0: none, below 32 px). */
    readonly hairline: number;
    readonly bars: readonly BrandBar[];
  }

  /** The bars on the pixel grid at `u` pixels per master unit (internal/brand fit). */
  function fit(size: number, u: number): BrandBar[] {
    let h = Math.max(1, Math.round(24 * u));
    let g = Math.max(1, Math.round(10 * u));
    const limit = Math.round(126 * u) + 1;
    while (4 * h + 3 * g > limit) {
      if (g > 1 && (g - 10 * u >= h - 24 * u || h <= 2)) g--;
      else if (h > 1) h--;
      else break;
    }
    const w = Math.max(4, Math.round(116 * u));
    const short = Math.max(2, Math.round(80 * u));
    const head = Math.max(1, Math.round(32 * u));
    const mid = w - head - g;
    const top = Math.floor((size - (4 * h + 3 * g)) / 2);
    const left = Math.floor((size - w) / 2);
    const r = h >= 4 ? h / 2 : 0;
    const y = (i: number) => top + i * (h + g);
    return [
      { x: left, y: y(0), w: short, h, r, head: false },
      { x: left, y: y(1), w, h, r, head: false },
      { x: left, y: y(2), w: mid, h, r: 0, head: false },
      { x: left + mid + g, y: y(2), w: w - mid - g, h, r, head: true },
      { x: left, y: y(3), w: short, h, r, head: false },
    ];
  }

  /**
   * Below 32 px a full-bleed tile (radius 0.219 × size) with the mark 0.625 × size wide and no
   * hairline; from 32 px the tile is inset by 8/256 with radius 52/256, a hairline of
   * max(1, 1.5/256 × size), and the mark 116/256 of the size.
   */
  export function brandLayout(size: number): BrandLayout {
    const s = Math.max(1, Math.round(size));
    if (s < 32) {
      return { x0: 0, x1: s, radius: (3.5 / 16) * s, hairline: 0, bars: fit(s, (0.625 * s) / 116) };
    }
    return {
      x0: (8 / 256) * s,
      x1: (248 / 256) * s,
      radius: (52 / 256) * s,
      hairline: Math.max(1, (1.5 / 256) * s),
      bars: fit(s, s / 256),
    };
  }

  /** A bar as a path: square left end, half-round right end when it has a radius. */
  export function barPath(b: BrandBar): string {
    if (b.r <= 0) return `M${b.x} ${b.y}h${b.w}v${b.h}h${-b.w}z`;
    return `M${b.x} ${b.y}h${b.w - b.r}a${b.r} ${b.r} 0 0 1 0 ${b.h}h${-(b.w - b.r)}z`;
  }
</script>

<script lang="ts">
  interface Props {
    /** Pixels: 22 (popup), 24 (title bar), 32 and up for headers and empty states. */
    size?: number;
    /** Accessible name; decorative when omitted (the word "Downex" sits beside it). */
    title?: string;
  }

  let { size = 24, title }: Props = $props();
  const layout = $derived(brandLayout(size));
  const px = $derived(Math.max(1, Math.round(size)));
</script>

<svg
  class="brand-mark"
  width={px}
  height={px}
  viewBox="0 0 {px} {px}"
  role={title ? 'img' : undefined}
  aria-label={title}
  aria-hidden={title ? undefined : 'true'}
  focusable="false"
>
  {#if layout.hairline > 0}
    <rect
      x={layout.x0}
      y={layout.x0}
      width={layout.x1 - layout.x0}
      height={layout.x1 - layout.x0}
      rx={layout.radius}
      fill="var(--brand-hairline, #34403a)"
    />
    <rect
      x={layout.x0 + layout.hairline}
      y={layout.x0 + layout.hairline}
      width={layout.x1 - layout.x0 - 2 * layout.hairline}
      height={layout.x1 - layout.x0 - 2 * layout.hairline}
      rx={Math.max(0, layout.radius - layout.hairline)}
      fill="var(--brand-tile, #1a221e)"
    />
  {:else}
    <rect x={layout.x0} y={layout.x0} width={layout.x1 - layout.x0} height={layout.x1 - layout.x0} rx={layout.radius} fill="var(--brand-tile, #1a221e)" />
    <!-- a 1 px inner ring in the dark theme only (--brand-ring), so the tile keeps its silhouette on the dark chrome -->
    <rect
      class="ring"
      x={layout.x0 + 0.5}
      y={layout.x0 + 0.5}
      width={layout.x1 - layout.x0 - 1}
      height={layout.x1 - layout.x0 - 1}
      rx={Math.max(0, layout.radius - 0.5)}
      fill="none"
      stroke="var(--brand-ring, transparent)"
      stroke-width="1"
    />
  {/if}
  {#each layout.bars as bar, i (i)}
    <path d={barPath(bar)} fill={bar.head ? 'var(--brand-head, #cef1e0)' : 'var(--brand-bar, #93e0ba)'} />
  {/each}
</svg>

<style>
  .brand-mark {
    flex: none;
    display: block;
    shape-rendering: geometricPrecision;
  }
</style>
