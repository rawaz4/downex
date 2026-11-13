import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { FILE_KINDS } from '../categories';
import BrandMark, { barPath, brandLayout } from './BrandMark.svelte';
import FileIcon, { TILE_KINDS, isTileKind, type TileKind } from './FileIcon.svelte';

function tile(kind: TileKind, size?: number, title?: string): SVGSVGElement {
  const { container } = render(FileIcon, { props: { kind, size, title } });
  return container.querySelector('svg') as SVGSVGElement;
}

/** What a tile is drawn of: its shapes in order, without coordinates. */
function silhouette(svg: SVGSVGElement): string {
  return Array.from(svg.querySelectorAll('rect, circle, path, g'))
    .map((el) => el.tagName.toLowerCase())
    .join(' ');
}

describe('FileIcon', () => {
  it('has a tile for every desktop category and images for the extension', () => {
    for (const k of FILE_KINDS) expect(TILE_KINDS).toContain(k);
    expect(TILE_KINDS).toContain('images');
    expect(isTileKind('video')).toBe(true);
    expect(isTileKind('spreadsheets')).toBe(false);
  });

  it('draws each kind in its own hue and deep tone, with a silhouette of its own', () => {
    const shapes = new Map<string, string>();
    for (const kind of TILE_KINDS) {
      const svg = tile(kind);
      expect(svg.getAttribute('viewBox')).toBe('2 2 28 28');
      expect(svg.dataset.kind).toBe(kind);
      expect(svg.style.getPropertyValue('--hue')).toBe(`var(--c-${kind})`);
      expect(svg.style.getPropertyValue('--deep')).toBe(`var(--c-${kind}-deep)`);
      shapes.set(kind, silhouette(svg));
    }
    // no two kinds are drawn the same way
    expect(new Set(shapes.values()).size).toBe(TILE_KINDS.length);
    expect(shapes.get('audio')?.startsWith('circle')).toBe(true); // a disc
    expect(shapes.get('video')?.startsWith('g')).toBe(true); // the clapper's tilted arm
  });

  it('never draws a page with text lines for documents (two sheets and a ribbon instead)', () => {
    const svg = tile('documents');
    const paths = Array.from(svg.querySelectorAll('path'));
    // every glyph is a filled shape: no stroked lines across the page
    for (const p of paths) expect(p.getAttribute('stroke')).toBeNull();
    expect(svg.querySelectorAll('rect')).toHaveLength(1); // the deep back sheet
    expect(svg.innerHTML).toContain('var(--deep)');
    expect(svg.innerHTML).toContain('var(--tile-glyph)');
  });

  it('draws the audio disc with three bars when small, so they never merge into a pause mark', () => {
    for (const size of [16, 18, 20]) {
      const small = tile('audio', size).querySelector('path')!;
      expect(small.getAttribute('d')).toBe('M11 12v8M16 9v14M21 12v8');
      expect(Number(small.getAttribute('stroke-width'))).toBeGreaterThanOrEqual(2.8);
    }
    // the five-bar waveform from 24 px up
    expect(tile('audio', 26).querySelector('path')?.getAttribute('d')?.match(/M/g)).toHaveLength(5);
  });

  it('is decorative by default and an image when it has a title', () => {
    const quiet = tile('archives', 16);
    expect(quiet.getAttribute('aria-hidden')).toBe('true');
    expect(quiet.getAttribute('width')).toBe('16');
    tile('programs', 32, 'Program');
    const named = screen.getByRole('img', { name: 'Program' });
    expect(named.getAttribute('width')).toBe('32');
    expect(named.getAttribute('aria-hidden')).toBeNull();
  });

  it('draws an unknown kind as other', () => {
    const svg = tile('nonsense' as TileKind);
    expect(svg.dataset.kind).toBe('other');
  });
});

describe('BrandMark', () => {
  it('is a full-bleed tile without a hairline below 32 px, the mark 0.625 of the size', () => {
    const l = brandLayout(24);
    expect(l).toMatchObject({ x0: 0, x1: 24, hairline: 0 });
    expect(l.radius).toBeCloseTo(0.21875 * 24, 5);
    expect(l.bars).toHaveLength(5);
    const width = Math.max(...l.bars.map((b) => b.x + b.w)) - Math.min(...l.bars.map((b) => b.x));
    expect(width).toBe(Math.round(0.625 * 24));
    // whole pixels, the third bar split with its last chunk the live head
    for (const b of l.bars) {
      for (const v of [b.x, b.y, b.w, b.h]) expect(Number.isInteger(v)).toBe(true);
    }
    expect(l.bars.filter((b) => b.head)).toHaveLength(1);
    expect(l.bars[3]?.head).toBe(true);
    expect(l.bars[3]?.y).toBe(l.bars[2]?.y);
  });

  it('is inset with a hairline from 32 px, the mark 116/256 of the size', () => {
    const l = brandLayout(256);
    expect(l.x0).toBe(8);
    expect(l.x1).toBe(248);
    expect(l.radius).toBe(52);
    expect(l.hairline).toBe(1.5);
    expect(brandLayout(32).hairline).toBe(1);
    // the master: bars 24 high with 10 gaps, 116 wide, the short bars 80, the head 32
    expect(l.bars.map((b) => [b.w, b.h])).toEqual([
      [80, 24],
      [116, 24],
      [74, 24],
      [32, 24],
      [80, 24],
    ]);
    expect(barPath(l.bars[0]!)).toBe(`M${l.bars[0]!.x} ${l.bars[0]!.y}h68a12 12 0 0 1 0 24h-68z`);
    expect(barPath(l.bars[2]!)).toBe(`M${l.bars[2]!.x} ${l.bars[2]!.y}h74v24h-74z`);
  });

  it('renders the same mark in both themes from the brand tokens, decorative unless named', () => {
    const { container } = render(BrandMark, { props: { size: 22 } });
    const svg = container.querySelector('svg') as SVGSVGElement;
    expect(svg.getAttribute('aria-hidden')).toBe('true');
    expect(svg.getAttribute('width')).toBe('22');
    expect(svg.innerHTML).toContain('var(--brand-tile');
    expect(svg.querySelectorAll('path')).toHaveLength(5);
    render(BrandMark, { props: { size: 48, title: 'Downex' } });
    const named = screen.getByRole('img', { name: 'Downex' });
    expect(named.innerHTML).toContain('var(--brand-hairline');
  });
});
