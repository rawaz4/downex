import type { Download } from '../api';

/**
 * Decides which of two reports of a download is the newer one (ui/state reportOrder):
 * answers race with events, and the newer report wins. downexd's updatedAt decides first
 * (an answer without one is never older). Reports with the same updatedAt are ordered by
 * when the window learned them, since downexd changes some fields without moving updatedAt
 * (the queuePosition of every download a move or an admission passes): an event counts from
 * its arrival, an answer from when its call was sent (downexd read it after that), and a
 * stream's snapshot from the stream's previous batch (the host subscribes again only after
 * it, and downexd takes the snapshot after the subscription). A report never brings back a
 * download whose removal the window learned after the report's call was sent.
 */
export class ReportOrder {
  /** Counts the batches received, the calls sent and the removals. */
  #clock = 0;
  /** When the last batch arrived. */
  #stream = 0;
  /** When each held report was learned. */
  #held = new Map<number, number>();
  /** When downloads were removed, kept while a call is out (its answer may still report them). */
  #gone = new Map<number, number>();
  #out = 0;

  /** The order of the answer to a call sent now; landed must follow when it is back (or failed). */
  sent(): number {
    this.#out++;
    return ++this.#clock;
  }

  /** Ends a call of sent. */
  landed(): void {
    if (--this.#out === 0) this.#gone.clear();
  }

  /** The order of a batch arriving now (its events), and that of its snapshot events. */
  arrived(): { events: number; snapshot: number } {
    const snapshot = this.#stream;
    this.#stream = ++this.#clock;
    return { events: this.#stream, snapshot };
  }

  /** Whether d, learned at at, replaces prev (undefined when none is held). */
  newer(d: Download, prev: Download | undefined | null, at: number): boolean {
    const gone = this.#gone.get(d.id);
    if (gone !== undefined && at < gone) return false;
    if (!prev || d.updatedAt <= 0) return true;
    if (d.updatedAt !== prev.updatedAt) return d.updatedAt > prev.updatedAt;
    return at >= (this.#held.get(d.id) ?? 0);
  }

  /** When the held report of id was learned (0: before anything). */
  heldAt(id: number): number {
    return this.#held.get(id) ?? 0;
  }

  /** Records that the report of id learned at at is the one held. */
  hold(id: number, at: number): void {
    this.#held.set(id, at);
  }

  /** Keeps the held orders of these ids only (after a reload). */
  keepOnly(ids: ReadonlySet<number>): void {
    for (const id of this.#held.keys()) if (!ids.has(id)) this.#held.delete(id);
  }

  /** Records that id was removed, as learned at at (undefined: now). */
  remove(id: number, at?: number): void {
    const when = at ?? ++this.#clock;
    this.#held.delete(id);
    if (this.#out > 0) this.#gone.set(id, when);
  }
}
