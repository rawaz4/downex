// File kinds (a row's icon, judged from the extension), categories (the sidebar's
// groups: Settings.categories, DESKTOP-SPEC §2, plus "Other") and the status filters
// of the sidebar.

import type { Category, Download, DownloadState } from './api';
import { escapeBidi } from './text';

export type FileKind = 'video' | 'audio' | 'archives' | 'documents' | 'programs' | 'other';

export const FILE_KINDS: readonly FileKind[] = [
  'video',
  'audio',
  'archives',
  'documents',
  'programs',
  'other',
];

const EXTENSIONS: Record<Exclude<FileKind, 'other'>, readonly string[]> = {
  video: ['mp4', 'm4v', 'mkv', 'webm', 'avi', 'mov', 'wmv', 'flv', 'mpg', 'mpeg', 'ts', 'm2ts', '3gp', 'ogv'],
  audio: ['mp3', 'flac', 'wav', 'aac', 'ogg', 'oga', 'opus', 'm4a', 'wma', 'aif', 'aiff', 'alac', 'mka'],
  archives: ['zip', 'rar', '7z', 'tar', 'gz', 'tgz', 'bz2', 'tbz2', 'xz', 'txz', 'zst', 'lz', 'lzma', 'cab', 'iso', 'img'],
  documents: ['pdf', 'doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx', 'odt', 'ods', 'odp', 'rtf', 'txt', 'md', 'csv', 'epub', 'djvu'],
  programs: ['exe', 'msi', 'msix', 'appx', 'deb', 'rpm', 'apk', 'appimage', 'flatpak', 'snap', 'dmg', 'pkg', 'jar', 'bat', 'cmd', 'sh', 'run'],
};

const BY_EXTENSION: ReadonlyMap<string, FileKind> = new Map(
  (Object.keys(EXTENSIONS) as Exclude<FileKind, 'other'>[]).flatMap((kind) =>
    EXTENSIONS[kind].map((ext) => [ext, kind] as const),
  ),
);

/** Lowercase extension of the last path component ("" when none). Ignores query/fragment. */
export function extensionOf(name: string): string {
  let base = name;
  const cut = base.search(/[?#]/);
  if (cut >= 0) base = base.slice(0, cut);
  const slash = Math.max(base.lastIndexOf('/'), base.lastIndexOf('\\'));
  if (slash >= 0) base = base.slice(slash + 1);
  const dot = base.lastIndexOf('.');
  if (dot <= 0 || dot === base.length - 1) return '';
  return base.slice(dot + 1).toLowerCase();
}

/** The kind of file a name has, by its extension (independent of the user's categories). */
export function classifyName(name: string): FileKind {
  return BY_EXTENSION.get(extensionOf(name)) ?? 'other';
}

type Named = Pick<Download, 'filename' | 'url' | 'finalUrl'>;

// Download objects are immutable (every update replaces the object), so names derived
// from one are cached per object. Sorting and searching 10,000 rows several times a
// second would otherwise parse URLs on every comparison.
const rawNames = new WeakMap<Named, string>();
const displayNames = new WeakMap<Named, string>();

/** The name as received: the filename, else the decoded last URL path segment, else the URL. */
export function rawName(d: Named): string {
  if (d.filename) return d.filename;
  let name = rawNames.get(d);
  if (name === undefined) {
    name = nameFromUrl(d.finalUrl || d.url);
    rawNames.set(d, name);
  }
  return name;
}

function nameFromUrl(url: string): string {
  try {
    const path = new URL(url).pathname;
    const last = decodeURIComponent(path.slice(path.lastIndexOf('/') + 1));
    if (last) return last;
  } catch {
    // fall through to the raw URL
  }
  return url;
}

/**
 * Name to show (and to sort and search by). Bidi control characters are made visible
 * ("⟨U+202E⟩") so a name cannot disguise its extension; actions use the download id,
 * never this string.
 */
export function displayName(d: Named): string {
  let name = displayNames.get(d);
  if (name === undefined) {
    name = escapeBidi(rawName(d));
    displayNames.set(d, name);
  }
  return name;
}

const kinds = new Map<string, FileKind>();

/** File kind of a download, memoised by name (lists recompute several times per second). */
export function kindOf(d: Named): FileKind {
  const name = d.filename || d.finalUrl || d.url;
  let kind = kinds.get(name);
  if (kind === undefined) {
    kind = classifyName(rawName(d));
    if (kinds.size > 100_000) kinds.clear();
    kinds.set(name, kind);
  }
  return kind;
}

/** Extensions the system may run rather than display when the file is opened. */
const EXECUTABLE = new Set([
  ...EXTENSIONS.programs,
  'appxbundle',
  'com',
  'scr',
  'pif',
  'cpl',
  'msc',
  'hta',
  'js',
  'jse',
  'vbs',
  'vbe',
  'wsf',
  'wsh',
  'ps1',
  'psm1',
  'lnk',
  'url',
  'reg',
  'command',
  'app',
  'desktop',
]);

/** True when opening the file could run code (judged from the real, unescaped name). */
export function isExecutableName(name: string): boolean {
  return EXECUTABLE.has(extensionOf(name));
}

// ---- categories ------------------------------------------------------------------

/** A configured category in filters and counts ("cat:" + its name, which may be anything). */
export type CategoryKey = `cat:${string}`;
/** Where a download is counted: one configured category, or "other" (none of them). */
export type CategoryBucket = CategoryKey | 'other';
/** The sidebar's category filter. */
export type CategoryFilter = CategoryBucket | 'all';

export const ALL_LABEL = 'All downloads';
export const OTHER_LABEL = 'Other';

export function categoryKey(name: string): CategoryKey {
  return `cat:${name}`;
}

export interface CategoryEntry {
  readonly key: CategoryKey;
  readonly name: string;
  /** Decides the icon and colour shown for it. */
  readonly kind: FileKind;
}

/** Normalises an extension as the daemon stores it: lower case, no leading dots. */
export function normalizeExtension(ext: string): string {
  return ext.trim().toLowerCase().replace(/^\.+/, '');
}

/**
 * The kind of a category: its name when that is one of the built-in kinds ("Video"),
 * else the kind most of its extensions have, else "other".
 */
export function categoryKind(c: Pick<Category, 'name' | 'extensions'>): FileKind {
  const named = c.name.trim().toLowerCase();
  if ((FILE_KINDS as readonly string[]).includes(named)) return named as FileKind;
  const votes = new Map<FileKind, number>();
  for (const ext of c.extensions) {
    const kind = BY_EXTENSION.get(normalizeExtension(ext));
    if (kind) votes.set(kind, (votes.get(kind) ?? 0) + 1);
  }
  let best: FileKind = 'other';
  let most = 0;
  for (const [kind, n] of votes) {
    if (n > most) {
      best = kind;
      most = n;
    }
  }
  return best;
}

type Categorised = Named & Pick<Download, 'category'>;

/**
 * Groups downloads by the configured categories. A download belongs to the category
 * the daemon chose for it (`Download.category`) while that category exists; one from
 * before categories, or whose category was renamed or removed, is grouped by its
 * extension like the daemon would (the first category listing it wins); the rest is
 * "other". Buckets are cached per Download object.
 */
export class CategoryIndex {
  readonly entries: readonly CategoryEntry[];
  readonly #byName = new Map<string, CategoryEntry>();
  readonly #byExtension = new Map<string, CategoryKey>();
  readonly #buckets = new WeakMap<Categorised, CategoryBucket>();

  constructor(categories: readonly Category[]) {
    const entries: CategoryEntry[] = [];
    for (const c of categories) {
      // The daemon accepts neither; a duplicate keeps the first entry.
      if (c.name === '' || this.#byName.has(c.name)) continue;
      const entry: CategoryEntry = { key: categoryKey(c.name), name: c.name, kind: categoryKind(c) };
      this.#byName.set(c.name, entry);
      entries.push(entry);
      for (const ext of c.extensions) {
        const e = normalizeExtension(ext);
        if (e && !this.#byExtension.has(e)) this.#byExtension.set(e, entry.key);
      }
    }
    this.entries = entries;
  }

  bucketOf(d: Categorised): CategoryBucket {
    let bucket = this.#buckets.get(d);
    if (bucket === undefined) {
      bucket =
        (d.category ? this.#byName.get(d.category)?.key : undefined) ??
        this.#byExtension.get(extensionOf(rawName(d))) ??
        'other';
      this.#buckets.set(d, bucket);
    }
    return bucket;
  }

  /** Whether the filter still names something (a removed category does not). */
  has(filter: CategoryFilter): boolean {
    return filter === 'all' || filter === 'other' || this.#byName.has(filter.slice(4));
  }

  labelOf(filter: CategoryFilter): string {
    if (filter === 'all') return ALL_LABEL;
    if (filter === 'other') return OTHER_LABEL;
    return filter.slice(4);
  }

  kindOf(filter: CategoryFilter): FileKind | 'all' {
    if (filter === 'all') return 'all';
    if (filter === 'other') return 'other';
    return this.#byName.get(filter.slice(4))?.kind ?? 'other';
  }
}

// ---- status filters --------------------------------------------------------------

export type StatusFilter = 'any' | 'awaiting' | 'active' | 'queued' | 'paused' | 'completed' | 'failed';

export const STATUS_FILTERS: readonly Exclude<StatusFilter, 'any'>[] = [
  'awaiting',
  'active',
  'queued',
  'paused',
  'completed',
  'failed',
];

export const STATUS_FILTER_LABELS: Record<StatusFilter, string> = {
  any: 'Any status',
  awaiting: 'To confirm',
  active: 'Active',
  queued: 'Queued',
  paused: 'Paused',
  completed: 'Completed',
  failed: 'Failed',
};

export function statusBucket(state: DownloadState): Exclude<StatusFilter, 'any'> | null {
  switch (state) {
    case 'awaiting_confirmation':
      return 'awaiting';
    case 'downloading':
    case 'probing':
      return 'active';
    case 'queued':
      return 'queued';
    case 'paused':
    case 'canceled':
      return 'paused';
    case 'completed':
      return 'completed';
    case 'failed':
      return 'failed';
    case 'unspecified':
      return null;
  }
}

export function matchesStatus(state: DownloadState, filter: StatusFilter): boolean {
  return filter === 'any' || statusBucket(state) === filter;
}
