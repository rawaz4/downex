// Confirmation of browser hand-offs (DESKTOP-SPEC §1): which download awaiting
// confirmation the dialog shows next, and the folder that a category proposes.

import type { Download, Settings } from './api';

/** Downloads awaiting confirmation, oldest first. */
export function awaitingInOrder(list: readonly Download[]): Download[] {
  return list
    .filter((d) => d.state === 'awaiting_confirmation')
    .sort((a, b) => a.createdAt - b.createdAt || a.id - b.id);
}

/**
 * The download whose confirmation dialog comes next: `preferred` (the user asked for
 * it) while it awaits confirmation, else the oldest one the user has not closed the
 * dialog of (`dismissed`), else none.
 */
export function nextConfirmation(
  list: readonly Download[],
  dismissed: ReadonlySet<number>,
  preferred: number | null = null,
): number | null {
  const awaiting = awaitingInOrder(list);
  if (preferred !== null && awaiting.some((d) => d.id === preferred)) return preferred;
  return awaiting.find((d) => !dismissed.has(d.id))?.id ?? null;
}

/** dir/name with the separator that dir already uses. */
export function joinDir(dir: string, name: string): string {
  if (!dir) return name;
  const sep = dir.includes('\\') && !dir.includes('/') ? '\\' : '/';
  return dir.endsWith(sep) ? `${dir}${name}` : `${dir}${sep}${name}`;
}

/**
 * The folder for a download of category `name` ("" = none) that names no folder of
 * its own (DESKTOP-SPEC §2): with category folders on, the category's folder or
 * <defaultDir>/<name>; otherwise the default folder.
 */
export function categoryFolder(
  settings: Pick<Settings, 'categories' | 'defaultDir' | 'useCategoryFolders'>,
  name: string,
): string {
  const category = name ? settings.categories.find((c) => c.name === name) : undefined;
  if (!settings.useCategoryFolders || !category) return settings.defaultDir;
  return category.dir || joinDir(settings.defaultDir, category.name);
}

/**
 * How long a confirmation dialog that opened by itself (a browser hand-off, not the
 * user's request) ignores its buttons and Enter: input meant for what was in front
 * before it appeared must not start, defer or cancel the download.
 */
export const CONFIRM_GUARD_MS = 600;
