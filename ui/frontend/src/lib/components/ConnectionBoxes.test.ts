import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import type { ConnectionInfo } from '../api';
import { MB, makeDownload, makeMediaDownload, makeMediaInfo } from '../testing';
import { MAX_BOXES, connectionBoxes, showsConnections, speedFormatter, speedUnit } from '../display';
import ConnectionBoxes from './ConnectionBoxes.svelte';

const KB = 1000;

function conn(index: number, partial: Partial<ConnectionInfo> = {}): ConnectionInfo {
  return {
    index,
    state: 'receiving',
    start: (index - 1) * 10 * MB,
    end: index * 10 * MB - 1,
    received: 4 * MB,
    speedBps: 700 * KB + index * 10 * KB,
    detail: '',
    fragment: -1,
    ...partial,
  };
}

describe('connectionBoxes', () => {
  it('gives each connection a box with its own speed, adding up to Together', () => {
    const infos = [conn(3), conn(1), conn(2, { state: 'retrying', speedBps: 0, detail: 'HTTP 503, retrying in 3 s' }), conn(4)];
    const s = connectionBoxes(infos);
    expect(s.boxes.map((b) => b.key)).toEqual(['c1', 'c2', 'c3', 'c4']);
    expect(s.boxes.map((b) => b.tone)).toEqual(['live', 'retry', 'live', 'live']);
    expect(s.together).toBe(s.boxes.reduce((sum, b) => sum + b.speed, 0));
    expect(s.together).toBe((710 + 730 + 740) * KB);
    // under 1 MB/s each: whole KB/s, the unit said once (decimal units)
    expect(s.unit).toBe('KB/s');
    expect(s.boxes.map((b) => b.label)).toEqual(['710', '0', '730', '740']);
    expect(s.boxes[0]?.fraction).toBeCloseTo(0.4);
    expect(s.boxes[1]?.title).toBe('Connection 2: retrying (HTTP 503, retrying in 3 s)');
  });

  it('ends in a +N box beyond 16 connections that still adds up', () => {
    const infos = Array.from({ length: 20 }, (_, i) => conn(i + 1, { speedBps: MB }));
    const s = connectionBoxes(infos);
    expect(s.boxes).toHaveLength(MAX_BOXES);
    const more = s.boxes.at(-1)!;
    expect(more.more).toBe(5);
    expect(more.speed).toBe(5 * MB);
    expect(s.together).toBe(20 * MB);
    expect(s.unit).toBe('MB/s');
    expect(s.boxes[0]?.label).toBe('1.0');
  });

  it('picks the unit for the +N box too, so it never reads 1000 or more KB/s', () => {
    const infos = Array.from({ length: 32 }, (_, i) => conn(i + 1, { speedBps: 300 * KB }));
    const s = connectionBoxes(infos);
    const more = s.boxes.at(-1)!;
    expect(more.more).toBe(17);
    expect(more.speed).toBe(17 * 300 * KB);
    expect(s.unit).toBe('MB/s');
    expect(more.label).toBe('5.1');
    expect(s.boxes[0]?.label).toBe('0.3');
  });

  it('gives a stream’s connections their speeds too, the fragment in the tooltip', () => {
    const s = connectionBoxes(
      [conn(1, { fragment: 11 }), conn(2, { fragment: 12, state: 'requesting', speedBps: 0 }), conn(3, { fragment: 13 })],
      { media: true, piece: 'Fragment' },
    );
    expect(s.boxes.map((b) => b.label)).toEqual(['710', '0', '730']);
    expect(s.boxes[0]?.title).toBe('Connection 1 · fragment 12: receiving');
    expect(s.boxes[1]?.title).toBe('Connection 2 · fragment 13: connecting');
    expect(s.unit).toBe('KB/s');
    // the boxes add up to Together
    expect(s.together).toBe(s.boxes.reduce((sum, b) => sum + b.speed, 0));
    expect(s.together).toBe((710 + 730) * KB);
  });

  it('formats per-connection speeds in one unit', () => {
    expect(speedUnit([300 * KB, 2 * MB])).toBe('MB/s');
    expect(speedFormatter('MB/s')(1.25 * MB)).toBe('1.3');
    expect(speedFormatter('KB/s')(742.4 * KB)).toBe('742');
    // 999.5 KB/s would read "1000": MB/s from there
    expect(speedUnit([999_499])).toBe('KB/s');
    expect(speedUnit([999_500])).toBe('MB/s');
    expect(speedFormatter('MB/s')(999_500)).toBe('1.0');
    // 9.67 MiB/s
    expect(speedFormatter('MB/s')(10_139_730)).toBe('10.1');
  });

  it('shows live connections only for a running download downexd reports', () => {
    expect(showsConnections(makeDownload({ id: 1, state: 'downloading', connectionInfo: [conn(1)] }), false)).toBe(true);
    expect(showsConnections(makeDownload({ id: 1, state: 'downloading', connectionInfo: [conn(1)] }), true)).toBe(false);
    expect(showsConnections(makeDownload({ id: 1, state: 'paused', connectionInfo: [] }), false)).toBe(false);
  });
});

describe('ConnectionBoxes', () => {
  it('draws one box per connection and the sum', () => {
    const d = makeDownload({
      id: 1,
      state: 'downloading',
      size: 80 * MB,
      received: 32 * MB,
      connections: 8,
      connectionInfo: Array.from({ length: 8 }, (_, i) => conn(i + 1, { speedBps: 0.8 * MB })),
    });
    const { container } = render(ConnectionBoxes, { props: { download: d } });
    expect(container.querySelectorAll('.box')).toHaveLength(8);
    expect(screen.getByRole('heading').textContent).toMatch(/^Connections\s+8 connections · 32\.0 of 80\.0 MB$/);
    expect(container.querySelector('.together')?.textContent?.replace(/\s+/g, ' ').trim()).toBe('Together 6.4 MB/s · each in KB/s');
    expect(screen.getByRole('progressbar', { name: 'Received' }).getAttribute('aria-valuenow')).toBe('40');
  });

  it('takes two rows beyond 8 connections, without the legend', () => {
    const d = makeDownload({
      id: 1,
      state: 'downloading',
      connections: 12,
      connectionInfo: Array.from({ length: 12 }, (_, i) => conn(i + 1)),
    });
    const { container } = render(ConnectionBoxes, { props: { download: d } });
    expect(container.querySelectorAll('.box')).toHaveLength(12);
    expect(container.querySelector('.boxes')?.classList.contains('two-rows')).toBe(true);
    expect(container.querySelector('.legend')?.classList.contains('hidden')).toBe(true);
  });

  it('shows the segment overview and "Not running" without live connections', () => {
    const d = makeDownload({
      id: 1,
      state: 'paused',
      size: 80 * MB,
      received: 20 * MB,
      segments: Array.from({ length: 16 }, (_, i) => ({ start: i * 5 * MB, end: (i + 1) * 5 * MB - 1, received: (5 * MB) / 4 })),
    });
    const { container } = render(ConnectionBoxes, { props: { download: d, maxConnections: 16 } });
    // 16 segments merge into at most 8 boxes
    expect(container.querySelectorAll('.box')).toHaveLength(8);
    expect(container.textContent).toContain('Not running');
    expect(screen.getByRole('heading').textContent).toContain('Up to 16 · 20.0 of 80.0 MB');
  });

  it('counts a stream’s fragments', () => {
    const d = makeMediaDownload({
      id: 2,
      state: 'downloading',
      received: 30 * MB,
      media: makeMediaInfo({ fragmentsDone: 12, fragmentsTotal: 340 }),
      connectionInfo: [conn(1, { fragment: 12 }), conn(2, { fragment: 13 })],
    });
    const { container } = render(ConnectionBoxes, { props: { download: d } });
    expect(screen.getByRole('heading').textContent).toMatch(/^Fragments\s+12 of 340$/);
    expect([...container.querySelectorAll('.speed')].map((e) => e.textContent)).toEqual(['710', '720']);
    expect([...container.querySelectorAll('.slot')].map((e) => e.getAttribute('title'))).toEqual([
      'Connection 1 · fragment 13: receiving',
      'Connection 2 · fragment 14: receiving',
    ]);
    expect(container.querySelector('.foot')?.textContent).toContain('Together 1.4 MB/s · each in KB/s');
    expect(screen.getByRole('progressbar', { name: 'Fragments downloaded' })).toBeTruthy();
  });

  it('says a completed download is all there', () => {
    render(ConnectionBoxes, { props: { download: makeDownload({ id: 3, state: 'completed', received: 10 * MB }) } });
    expect(screen.getByText('All received')).toBeTruthy();
  });
});
