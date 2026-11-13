import { afterEach, describe, expect, it, vi } from 'vitest';

const MiB = 1024 * 1024;

describe('ticking', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    vi.resetModules();
  });

  // A running download reports every 250 ms: easing each report would keep a frame loop (and a
  // layout and paint per frame) running for as long as it downloads.
  it('eases a lone change but shows a stream of changes at once, without a frame loop', async () => {
    vi.stubGlobal('matchMedia', (media: string) => ({
      matches: false,
      media,
      addEventListener() {},
      removeEventListener() {},
      addListener() {},
      removeListener() {},
    }));
    const frames: FrameRequestCallback[] = [];
    vi.stubGlobal('requestAnimationFrame', (f: FrameRequestCallback) => frames.push(f));
    vi.stubGlobal('cancelAnimationFrame', () => {});
    let now = 10_000;
    vi.spyOn(performance, 'now').mockImplementation(() => now);
    // motion.svelte.ts reads matchMedia when it loads: load it (and its users) again
    vi.resetModules();
    const { render } = await import('@testing-library/svelte');
    const { formatSpeed } = await import('./format');
    const { default: StatusBar } = await import('./components/StatusBar.svelte');

    const totals = (speed: number) => ({ speed, active: 1, queued: 0, count: 1 });
    const { container, rerender } = render(StatusBar, {
      props: {
        daemon: { ok: true, status: { version: '1', pid: 1, startedAt: 1, active: 1, queued: 0, endpoint: '' } },
        demo: false,
        totals: totals(1 * MiB),
        shown: 1,
        selected: 0,
      },
    });
    const shown = () => container.querySelector('.speed-value')?.textContent;
    expect(shown()).toBe(formatSpeed(1 * MiB));

    now += 5_000;
    await rerender({ totals: totals(5 * MiB) });
    expect(frames).toHaveLength(1); // a lone change eases
    expect(shown()).toBe(formatSpeed(1 * MiB));

    for (const speed of [6, 7, 8]) {
      now += 250;
      await rerender({ totals: totals(speed * MiB) });
      expect(shown()).toBe(formatSpeed(speed * MiB));
    }
    expect(frames).toHaveLength(1); // no frame was asked for while the stream lasted

    now += 5_000;
    await rerender({ totals: totals(2 * MiB) });
    expect(frames).toHaveLength(2); // once the stream stops, a change eases again
  });
});
