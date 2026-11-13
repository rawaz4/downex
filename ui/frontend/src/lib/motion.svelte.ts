// Motion helpers: small, interruptible animations that are skipped entirely when the
// viewer asked for reduced motion (or the platform cannot animate, as in tests).

import { untrack } from 'svelte';

const reduceQuery: MediaQueryList | null =
  typeof window !== 'undefined' && typeof window.matchMedia === 'function'
    ? window.matchMedia('(prefers-reduced-motion: reduce)')
    : null;

/** Animations may run: the platform can animate and reduced motion was not asked for. */
export function motionOk(): boolean {
  return reduceQuery !== null && !reduceQuery.matches && typeof requestAnimationFrame === 'function';
}

/**
 * A number that eases to each new value of `fn` (speeds, percentages, time left), so it
 * ticks rather than jumps. Unknown values (NaN, ±Infinity) are shown at once, and so is
 * everything when motion is off. A value that changes again within 2·ms of its last change
 * (a running download's progress, reported every 250 ms) is also shown at once: easing it
 * would keep a frame loop, with a layout and paint per frame, going for as long as the
 * download runs (measured 2026-10-29: about a CPU core in the WebView2 renderer and two
 * thirds of one in its GPU process for one download, and slower downloads with it).
 * Call during component initialisation.
 */
export function ticking(fn: () => number, ms = 360): { readonly current: number } {
  const tween = new Ease(fn());
  $effect.pre(() => {
    const target = fn();
    untrack(() => tween.to(target, ms));
  });
  $effect(() => () => tween.stop());
  return tween;
}

const cubicOut = (t: number) => 1 - (1 - t) ** 3;

/** One eased number; `to` restarts from wherever it is now. */
class Ease {
  current = $state(0);
  #frame = 0;
  /** performance.now() of the latest change of the target. */
  #changed = Number.NEGATIVE_INFINITY;

  constructor(value: number) {
    this.current = value;
  }

  to(target: number, ms: number): void {
    const from = this.current;
    this.stop();
    const now = performance.now();
    const streaming = now - this.#changed < 2 * ms;
    if (target !== from) this.#changed = now;
    if (streaming || !motionOk() || !Number.isFinite(target) || !Number.isFinite(from) || from === target) {
      this.current = target;
      return;
    }
    const start = performance.now();
    const step = (now: number) => {
      const t = Math.min(1, (now - start) / ms);
      this.current = t >= 1 ? target : from + (target - from) * cubicOut(t);
      this.#frame = t >= 1 ? 0 : requestAnimationFrame(step);
    };
    this.#frame = requestAnimationFrame(step);
  }

  stop(): void {
    if (this.#frame) cancelAnimationFrame(this.#frame);
    this.#frame = 0;
  }
}

/**
 * Tracks which ids appear and disappear between readings of a list. The first reading
 * once the list is ready only records what is there. Newly seen ids are also kept in
 * `fresh` for `holdMs` (while motion is allowed), so their rows can play an arrival.
 */
export class Arrivals {
  fresh = $state.raw<ReadonlySet<number>>(new Set());
  #known = new Set<number>();
  #seeded = false;
  readonly #holdMs: number;

  constructor(holdMs = 900) {
    this.#holdMs = holdMs;
  }

  update(ids: Iterable<number>, ready: boolean): { added: number[]; removed: number[] } {
    if (!ready) return { added: [], removed: [] };
    const now = new Set(ids);
    const added: number[] = [];
    const removed: number[] = [];
    if (this.#seeded) {
      for (const id of now) if (!this.#known.has(id)) added.push(id);
      for (const id of this.#known) if (!now.has(id)) removed.push(id);
    }
    this.#known = now;
    this.#seeded = true;
    if (added.length > 0 && motionOk()) {
      untrack(() => {
        this.fresh = new Set([...this.fresh, ...added]);
      });
      setTimeout(() => {
        const next = new Set(this.fresh);
        for (const id of added) next.delete(id);
        this.fresh = next;
      }, this.#holdMs);
    }
    return { added, removed };
  }
}
