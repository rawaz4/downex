<script lang="ts" module>
  import type { FileKind } from '../categories';

  /**
   * The file kinds that have a tile: the desktop's categories, plus `images` for parity with
   * the extension (the desktop has no images category).
   */
  export type TileKind = FileKind | 'images';

  export const TILE_KINDS: readonly TileKind[] = ['video', 'audio', 'archives', 'documents', 'programs', 'images', 'other'];

  /** Whether a value names a tile (anything else is drawn as `other`). */
  export function isTileKind(v: string): v is TileKind {
    return (TILE_KINDS as readonly string[]).includes(v);
  }

  /**
   * The drawn area of the 32 grid: the tiles fill 2..30, so the box is cropped to it (more ink
   * at every size). The clapper's tilted arm reaches a little above it, so the SVG does not clip.
   */
  export const TILE_VIEWBOX = '2 2 28 28';
  /** At this size and below, the audio disc draws three bars instead of the waveform's five. */
  export const SMALL_TILE_PX = 20;
</script>

<script lang="ts">
  // The redesigned file-type tiles (DESIGN-SYSTEM.md §2.6.3): solid, two-tone (hue + deep)
  // with white details, and a silhouette of their own per kind, so they read apart at 16 px
  // even in greyscale. Never "a page with lines". Sizes: 16, 18, 20, 26, 32, 40, 48.
  interface Props {
    kind: TileKind;
    size?: number;
    /** Accessible name; when omitted the tile is decorative (the name is beside it). */
    title?: string;
  }

  let { kind, size = 24, title }: Props = $props();
  const shown = $derived<TileKind>(isTileKind(kind) ? kind : 'other');
</script>

<svg
  class="tile kind-{shown}"
  width={size}
  height={size}
  viewBox={TILE_VIEWBOX}
  style:--hue="var(--c-{shown})"
  style:--deep="var(--c-{shown}-deep)"
  role={title ? 'img' : undefined}
  aria-label={title}
  aria-hidden={title ? undefined : 'true'}
  focusable="false"
  data-kind={shown}
>
  {#if shown === 'video'}
    <g transform="rotate(-11 4.5 10.5)">
      <rect x="4.5" y="5" width="24" height="5.6" rx="1.6" fill="var(--deep)" />
      <path d="M9.2 5h3.4l-2.6 5.6H6.6zM16.2 5h3.4L17 10.6h-3.4zM23.2 5h3.4L24 10.6h-3.4z" fill="var(--tile-glyph)" />
    </g>
    <rect x="4.5" y="11.5" width="24" height="17" rx="3" fill="var(--hue)" />
    <path d="M14 16.1v7.8a.9.9 0 0 0 1.37.77l6.2-3.9a.9.9 0 0 0 0-1.54l-6.2-3.9A.9.9 0 0 0 14 16.1z" fill="var(--tile-glyph)" />
  {:else if shown === 'audio'}
    <circle cx="16" cy="16" r="13.5" fill="var(--hue)" />
    {#if size <= SMALL_TILE_PX}
      <!-- three bars with gaps of 1 px or more: five merge into a pause-like lozenge when small -->
      <path d="M11 12v8M16 9v14M21 12v8" stroke="var(--tile-glyph)" stroke-width="2.8" stroke-linecap="round" fill="none" />
    {:else}
      <path
        d="M9.6 13.8v4.4M12.8 10.8v10.4M16 8.6v14.8M19.2 11.4v9.2M22.4 13.4v5.2"
        stroke="var(--tile-glyph)"
        stroke-width="2.4"
        stroke-linecap="round"
        fill="none"
      />
    {/if}
  {:else if shown === 'archives'}
    <rect x="5" y="11" width="22" height="18" rx="2.8" fill="var(--hue)" />
    <rect x="3" y="4.5" width="26" height="8" rx="2.2" fill="var(--deep)" />
    <path d="M13.6 4.5h4.8v12.3l-2.4-1.5-2.4 1.5z" fill="var(--tile-glyph)" />
    <rect x="9.5" y="21.5" width="13" height="3" rx="1.5" fill="var(--tile-glyph)" fill-opacity=".9" />
  {:else if shown === 'documents'}
    <rect x="10" y="2.5" width="17.5" height="22" rx="2.6" fill="var(--deep)" />
    <path d="M7 6.5h10.2l6.3 6.3V27a2.5 2.5 0 0 1-2.5 2.5H7A2.5 2.5 0 0 1 4.5 27V9A2.5 2.5 0 0 1 7 6.5z" fill="var(--hue)" />
    <path d="M17.2 6.5l6.3 6.3h-4.1a2.2 2.2 0 0 1-2.2-2.2z" fill="var(--tile-glyph)" fill-opacity=".45" />
    <path d="M8.5 6.5h4.6v9.2l-2.3-1.8-2.3 1.8z" fill="var(--tile-glyph)" />
  {:else if shown === 'programs'}
    <path
      d="M16 3.2l11.1 6.4v12.8L16 28.8 4.9 22.4V9.6z"
      fill="var(--hue)"
      stroke="var(--hue)"
      stroke-width="2.4"
      stroke-linejoin="round"
    />
    <path
      d="M16 16.1l11.1-6.4v12.7L16 28.8z"
      fill="var(--deep)"
      stroke="var(--deep)"
      stroke-width="2.4"
      stroke-linejoin="round"
      opacity=".55"
    />
    <path
      d="M9.8 12.6l4.4 3.4-4.4 3.4M15.8 20.6h5.4"
      stroke="var(--tile-glyph)"
      stroke-width="2.4"
      stroke-linecap="round"
      stroke-linejoin="round"
      fill="none"
    />
  {:else if shown === 'images'}
    <rect x="3" y="6" width="26" height="20" rx="4" fill="var(--hue)" />
    <circle cx="10.6" cy="12.2" r="2.5" fill="var(--tile-glyph)" />
    <path d="M6.5 22.6l6.1-6.4 3.7 3.8 3.4-3.6 5.8 6.2v.4a1 1 0 0 1-1 1H7.5a1 1 0 0 1-1-1z" fill="var(--tile-glyph)" />
  {:else}
    <rect x="4" y="4" width="24" height="24" rx="7" fill="var(--hue)" />
    <circle cx="10.4" cy="16" r="2" fill="var(--tile-glyph)" />
    <circle cx="16" cy="16" r="2" fill="var(--tile-glyph)" />
    <circle cx="21.6" cy="16" r="2" fill="var(--tile-glyph)" />
  {/if}
</svg>

<style>
  .tile {
    flex: none;
    display: block;
    overflow: visible;
    forced-color-adjust: auto;
  }
</style>
