// The rows of "Download all links with Downex" (DESIGN-SYSTEM.md §4.12): each link as its file's
// tile, name and server, as everywhere else; the full address stays in the row's tooltip.

import { fileKindOfName, type FileKind } from '../../lib/ui-format';

export interface LinkRow {
  /** The file name: the last segment of the path, decoded ("" when the path has none). */
  readonly name: string;
  /** The server without "www." ("" when the address does not parse). */
  readonly host: string;
  readonly kind: FileKind;
}

export function linkRow(url: string): LinkRow {
  let name = '';
  let host = '';
  try {
    const u = new URL(url);
    host = u.hostname.replace(/^www\./i, '');
    const last = u.pathname.split('/').filter((p) => p !== '').pop() ?? '';
    try {
      name = decodeURIComponent(last);
    } catch {
      name = last;
    }
  } catch {
    name = '';
  }
  return { name, host, kind: fileKindOfName(name) };
}
