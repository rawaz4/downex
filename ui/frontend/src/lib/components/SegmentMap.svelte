<script lang="ts" module>
  import type { Download, Segment } from '../api';

  export interface SegmentSummary {
    readonly total: number;
    readonly active: number;
    readonly done: number;
  }

  function segmentLength(s: Segment, size: number): number {
    const end = s.end < 0 ? size - 1 : s.end;
    return end - s.start + 1;
  }

  export function summarizeSegments(d: Download): SegmentSummary {
    let active = 0;
    let done = 0;
    for (const s of d.segments) {
      const len = segmentLength(s, d.size);
      if (d.size > 0 && s.received >= len) done++;
      else if (d.state === 'downloading') active++;
    }
    return { total: d.segments.length, active, done };
  }

  /**
   * Draws the byte-range map: each segment's received part from its start offset. With
   * `markers`, each segment's start is also marked with a tick (the progress window's
   * "start positions" view).
   */
  export function drawSegments(
    canvas: HTMLCanvasElement,
    d: Download,
    width: number,
    height: number,
    markers = false,
  ): void {
    const ctx = canvas.getContext('2d');
    if (!ctx) return;
    const dpr = window.devicePixelRatio || 1;
    canvas.width = Math.max(1, Math.round(width * dpr));
    canvas.height = Math.max(1, Math.round(height * dpr));
    const css = getComputedStyle(canvas);
    const color = (name: string, fallback: string) => css.getPropertyValue(name).trim() || fallback;
    const pending = color('--seg-track', '#dce3df');
    const done = color('--seg-fill', '#178553');
    const front = color('--seg-edge', '#0b4a2f');
    const gap = color('--surface', '#fff');
    const marker = color('--seg-marker', front);

    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    if (css.direction === 'rtl') {
      ctx.translate(width, 0);
      ctx.scale(-1, 1);
    }
    ctx.clearRect(0, 0, width, height);
    ctx.fillStyle = pending;
    ctx.fillRect(0, 0, width, height);

    if (d.state === 'completed') {
      ctx.fillStyle = done;
      ctx.fillRect(0, 0, width, height);
      return;
    }
    if (d.size <= 0) {
      // Unknown size: hatch the bar to show bytes are arriving without a known end.
      if (d.received <= 0) return;
      ctx.strokeStyle = done;
      ctx.lineWidth = 3;
      for (let x = -height; x < width + height; x += 9) {
        ctx.beginPath();
        ctx.moveTo(x, height);
        ctx.lineTo(x + height, 0);
        ctx.stroke();
      }
      return;
    }

    const scale = width / d.size;
    const segments: readonly Segment[] =
      d.segments.length > 0 ? d.segments : [{ start: 0, end: d.size - 1, received: d.received }];
    for (const s of segments) {
      const len = segmentLength(s, d.size);
      const x0 = s.start * scale;
      const x1 = (s.start + len) * scale;
      const xr = Math.min(x1, x0 + Math.max(0, s.received) * scale);
      ctx.fillStyle = done;
      ctx.fillRect(x0, 0, xr - x0, height);
      if (d.state === 'downloading' && s.received < len) {
        ctx.fillStyle = front;
        ctx.fillRect(Math.max(x0, xr - 2), 0, Math.min(2, x1 - x0), height);
      }
      if (s.start > 0 && x1 - x0 >= 4 && !markers) {
        ctx.fillStyle = gap;
        ctx.fillRect(x0 - 0.5, 0, 1, height);
      }
    }
    if (markers) {
      // A tick at each start position, with a notch on top so that neighbours stay apart.
      ctx.fillStyle = marker;
      for (const s of segments) {
        const x = Math.min(width - 2, Math.max(0, s.start * scale - 1));
        ctx.fillRect(x, 0, 2, height);
        ctx.beginPath();
        ctx.moveTo(x - 3, 0);
        ctx.lineTo(x + 5, 0);
        ctx.lineTo(x + 1, 5);
        ctx.closePath();
        ctx.fill();
      }
    }
  }
</script>

<script lang="ts">
  import { formatPercent, progressFraction } from '../format';
  import { theme } from '../stores/theme.svelte';

  interface Props {
    download: Download;
    height?: number;
    /** Mark each segment's start position. */
    markers?: boolean;
  }

  let { download, height = 22, markers = false }: Props = $props();

  let canvas: HTMLCanvasElement | undefined = $state();
  let width = $state(0);

  const summary = $derived(summarizeSegments(download));
  const label = $derived.by(() => {
    const pct = formatPercent(progressFraction(download));
    if (download.state === 'completed') return 'Segment map: download complete';
    if (download.size <= 0) return 'Segment map: size unknown, single connection';
    const parts = [`${summary.total || 1} segment${summary.total === 1 ? '' : 's'}`];
    if (summary.active > 0) parts.push(`${summary.active} active`);
    return `Segment map: ${parts.join(', ')}, ${pct} received`;
  });

  $effect(() => {
    const d = download;
    const w = width;
    void theme.resolved;
    if (canvas && w > 0) drawSegments(canvas, d, w, height, markers);
  });
</script>

<div class="map" role="img" aria-label={label} bind:clientWidth={width}>
  <canvas bind:this={canvas} aria-hidden="true" style:block-size="{height}px"></canvas>
</div>

<style>
  .map {
    inline-size: 100%;
    min-inline-size: 0;
    border: 1px solid var(--line);
    border-radius: var(--r-xs);
    overflow: hidden;
  }
  @media (forced-colors: active) {
    .map {
      border-color: CanvasText;
    }
  }
  canvas {
    display: block;
    inline-size: 100%;
  }
</style>
