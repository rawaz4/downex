// Filtering, sorting and sidebar counts over the download list. The store refreshes
// these at the daemon's 4 batches per second, usually with only a few downloads
// changed, so derived keys are cached per Download object (objects are immutable:
// every update replaces one) and the sorted order is patched instead of rebuilt.

import { queueOf, type Download, type DownloadState } from './api';
import {
  STATUS_FILTERS,
  displayName,
  matchesStatus,
  statusBucket,
  type CategoryFilter,
  type CategoryIndex,
  type StatusFilter,
} from './categories';
import { etaSeconds, progressFraction } from './format';

export type SortKey =
  | 'name'
  | 'size'
  | 'progress'
  | 'speed'
  | 'eta'
  | 'connections'
  | 'status'
  | 'added';

export type SortDir = 'asc' | 'desc';

export interface SortSpec {
  readonly key: SortKey;
  readonly dir: SortDir;
}

export const DEFAULT_SORT: SortSpec = { key: 'added', dir: 'desc' };

export interface FilterSpec {
  readonly category: CategoryFilter;
  readonly status: StatusFilter;
  readonly search: string;
  /** Only the downloads of this queue (M6); null or absent = every queue. */
  readonly queue?: number | null;
}

interface SearchKeys {
  readonly name: string;
  readonly url: string;
  readonly finalUrl: string;
}

const searchKeys = new WeakMap<Download, SearchKeys>();

function searchKeysOf(d: Download): SearchKeys {
  let k = searchKeys.get(d);
  if (!k) {
    k = {
      name: displayName(d).toLowerCase(),
      url: d.url.toLowerCase(),
      finalUrl: d.finalUrl.toLowerCase(),
    };
    searchKeys.set(d, k);
  }
  return k;
}

/** Keeps the input order; `index` groups the downloads into categories. */
export function filterDownloads(list: readonly Download[], f: FilterSpec, index: CategoryIndex): Download[] {
  const needle = f.search.trim().toLowerCase();
  const queue = f.queue ?? null;
  if (f.category === 'all' && f.status === 'any' && !needle && queue === null) return list.slice();
  const out: Download[] = [];
  for (const d of list) {
    if (queue !== null && queueOf(d) !== queue) continue;
    if (f.category !== 'all' && index.bucketOf(d) !== f.category) continue;
    if (!matchesStatus(d.state, f.status)) continue;
    if (needle && !matchesSearch(d, needle)) continue;
    out.push(d);
  }
  return out;
}

function matchesSearch(d: Download, needle: string): boolean {
  const k = searchKeysOf(d);
  return k.name.includes(needle) || k.url.includes(needle) || (k.finalUrl !== '' && k.finalUrl.includes(needle));
}

const STATE_RANK: Record<DownloadState, number> = {
  downloading: 0,
  probing: 1,
  awaiting_confirmation: 2,
  queued: 3,
  paused: 4,
  failed: 5,
  canceled: 6,
  completed: 7,
  unspecified: 8,
};

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' });

export type Comparator = (a: Download, b: Download) => number;

export function liveSpeed(d: Download): number {
  return d.state === 'downloading' ? d.speedBps : 0;
}

export function liveConnections(d: Download): number {
  return d.state === 'downloading' ? d.connections : 0;
}

type NumericKey = Exclude<SortKey, 'name'>;

/** The number a download is ordered by; NaN = unknown (sorts last in both directions). */
function numericValue(d: Download, key: NumericKey): number {
  switch (key) {
    case 'size':
      return d.size;
    case 'progress':
      return progressFraction(d);
    case 'speed':
      return liveSpeed(d);
    case 'eta':
      return etaSeconds(d);
    case 'connections':
      return liveConnections(d);
    case 'status':
      // activity first, then queue position (always far below 1e9)
      return STATE_RANK[d.state] * 1e9 + d.queuePosition;
    case 'added':
      return d.createdAt;
  }
}

/** Whether an update moves a download under `key` (only then does the order change). */
export function sortValueChanged(prev: Download, next: Download, key: SortKey): boolean {
  if (key === 'name') return displayName(prev) !== displayName(next);
  return !Object.is(numericValue(prev, key), numericValue(next, key));
}

/**
 * A total order: ties break on id in the sort direction, so the order is stable across
 * refreshes, and unknown values (NaN) sort last in both directions.
 */
export function comparatorFor(sort: SortSpec): Comparator {
  const sign = sort.dir === 'asc' ? 1 : -1;
  if (sort.key === 'name') {
    return (a, b) => sign * (collator.compare(displayName(a), displayName(b)) || a.id - b.id);
  }
  const key = sort.key;
  return (a, b) => {
    const va = numericValue(a, key);
    const vb = numericValue(b, key);
    const na = Number.isNaN(va);
    const nb = Number.isNaN(vb);
    if (na !== nb) return na ? 1 : -1;
    const c = na || va === vb ? 0 : va < vb ? -1 : 1;
    return sign * (c || a.id - b.id);
  };
}

/** Sorts a copy (decorated once per item, so keys are not recomputed per comparison). */
export function sortDownloads(list: readonly Download[], sort: SortSpec): Download[] {
  const sign = sort.dir === 'asc' ? 1 : -1;
  if (sort.key === 'name') {
    const keyed = list.map((d) => ({ d, k: displayName(d) }));
    keyed.sort((a, b) => sign * (collator.compare(a.k, b.k) || a.d.id - b.d.id));
    return keyed.map((x) => x.d);
  }
  const key = sort.key;
  const keyed = list.map((d) => ({ d, v: numericValue(d, key) }));
  keyed.sort((a, b) => {
    const na = Number.isNaN(a.v);
    const nb = Number.isNaN(b.v);
    if (na !== nb) return na ? 1 : -1;
    const c = na || a.v === b.v ? 0 : a.v < b.v ? -1 : 1;
    return sign * (c || a.d.id - b.d.id);
  });
  return keyed.map((x) => x.d);
}

/** First index whose item sorts at or after `d`. */
function lowerBound(list: readonly Download[], d: Download, cmp: Comparator): number {
  let lo = 0;
  let hi = list.length;
  while (lo < hi) {
    const mid = (lo + hi) >>> 1;
    if (cmp(list[mid] as Download, d) < 0) lo = mid + 1;
    else hi = mid;
  }
  return lo;
}

/**
 * The full list in sort order, kept up to date incrementally. The owner reports every
 * change with touch() (before replacing or removing the object it holds), then reads
 * update(). When no changed download's sort value changed, the order is reused and
 * only the objects are swapped; otherwise just the changed ones move. Many changes,
 * a new sort or invalidate() fall back to a full sort.
 */
export class SortedView {
  #spec: SortSpec | null = null;
  #cmp: Comparator | null = null;
  #list: Download[] = [];
  /** id → the object #list holds for it (null: not in #list). */
  #dirty = new Map<number, Download | null>();
  #stale = true;
  /** Full sorts performed (tests and diagnostics). */
  fullSorts = 0;

  touch(id: number, prev: Download | undefined): void {
    if (!this.#stale && !this.#dirty.has(id)) this.#dirty.set(id, prev ?? null);
  }

  invalidate(): void {
    this.#stale = true;
    this.#dirty.clear();
  }

  /** The sorted list; a new array whenever something changed, the same one otherwise. */
  update(items: ReadonlyMap<number, Download>, spec: SortSpec): Download[] {
    const sameSpec = this.#spec !== null && this.#spec.key === spec.key && this.#spec.dir === spec.dir;
    if (sameSpec && !this.#stale && this.#dirty.size === 0) return this.#list;
    if (!sameSpec || this.#stale || this.#dirty.size > Math.max(64, this.#list.length / 8)) {
      return this.#full(items, spec);
    }
    const cmp = this.#cmp as Comparator;
    const list = this.#list.slice();
    const moved: Download[] = [];
    for (const [id, before] of this.#dirty) {
      const now = items.get(id);
      if (before) {
        const i = lowerBound(list, before, cmp);
        if (list[i] !== before) return this.#full(items, spec); // out of step: rebuild
        if (now && !sortValueChanged(before, now, spec.key)) {
          list[i] = now;
          continue;
        }
        list.splice(i, 1);
      }
      if (now) moved.push(now);
    }
    for (const d of moved) list.splice(lowerBound(list, d, cmp), 0, d);
    this.#dirty.clear();
    this.#list = list;
    return list;
  }

  #full(items: ReadonlyMap<number, Download>, spec: SortSpec): Download[] {
    this.fullSorts++;
    this.#spec = spec;
    this.#cmp = comparatorFor(spec);
    this.#list = sortDownloads(Array.from(items.values()), spec);
    this.#dirty.clear();
    this.#stale = false;
    return this.#list;
  }
}

export function toggleSort(current: SortSpec, key: SortKey): SortSpec {
  if (current.key === key) return { key, dir: current.dir === 'asc' ? 'desc' : 'asc' };
  // Text columns start ascending; quantities and dates start with the largest first.
  return { key, dir: key === 'name' || key === 'status' ? 'asc' : 'desc' };
}

/** Downloads per category filter: "all", every configured category and "other". */
export type CategoryCounts = ReadonlyMap<CategoryFilter, number>;
export type StatusCounts = Record<StatusFilter, number>;

export interface Counts {
  readonly categories: CategoryCounts;
  readonly statuses: StatusCounts;
}

/**
 * Faceted counts: each category count applies the selected status filter, and each
 * status count applies the selected category, so a click never leads to an
 * unexpected number of rows. The queue filter applies to both; search is not applied.
 */
export function countFacets(
  list: readonly Download[],
  category: CategoryFilter,
  status: StatusFilter,
  index: CategoryIndex,
  queue: number | null = null,
): Counts {
  const categories = new Map<CategoryFilter, number>([['all', 0]]);
  for (const e of index.entries) categories.set(e.key, 0);
  categories.set('other', 0);
  const statuses = zeroRecord<StatusFilter>(['any', ...STATUS_FILTERS]);
  for (const d of list) {
    if (queue !== null && queueOf(d) !== queue) continue;
    const cat = index.bucketOf(d);
    const bucket = statusBucket(d.state);
    if (matchesStatus(d.state, status)) {
      categories.set('all', (categories.get('all') ?? 0) + 1);
      categories.set(cat, (categories.get(cat) ?? 0) + 1);
    }
    if (category === 'all' || category === cat) {
      statuses.any++;
      if (bucket) statuses[bucket]++;
    }
  }
  return { categories, statuses };
}

/** Per queue, its downloads that still have something to do (all but completed ones). */
export function countQueues(list: readonly Download[]): Map<number, number> {
  const out = new Map<number, number>();
  for (const d of list) {
    if (d.state === 'completed') continue;
    const q = queueOf(d);
    out.set(q, (out.get(q) ?? 0) + 1);
  }
  return out;
}

function zeroRecord<K extends string>(keys: readonly K[]): Record<K, number> {
  const r = {} as Record<K, number>;
  for (const k of keys) r[k] = 0;
  return r;
}
