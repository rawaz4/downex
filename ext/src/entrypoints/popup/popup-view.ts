// Pure rules of the popup (BROWSER-SPEC §4, docs/DESIGN-SYSTEM.md §4.11): the status chip and the
// states that replace the page's content, the hand-off pills, the per-site switch's stored rule, the
// file tiles and the small texts of the rows. The texts come from lib/ui-format; this only decides
// how the popup draws them. No DOM.

import type { HandoffRecord } from '../../lib/history';
import type { VideoOptions } from '../../lib/options-form';
import type { PoliteState } from '../../lib/protocol';
import { type CaptureRules, withSiteExcluded } from '../../lib/rules';
import {
  addedTone,
  type FileKind,
  HOST_FORBIDDEN_HINT,
  HOST_MISSING_HINT,
  initialCapital,
  type Label,
  recordResult,
  recordResultDetail,
} from '../../lib/ui-format';
import { type QualityRow, RECOMMENDED_TEXT } from '../../lib/video-picker';
import type { SelectMenuOption } from './select-menu';

/** A connection label drawn as a headline, its reason and the host-missing or host-refused fix. */
export interface StatusText {
  /** "Connected (Downex 0.7.3)", "Not available", "Incompatible Downex". */
  headline: string;
  /** What follows the first ": ", starting with a capital ("" when there is none). */
  detail: string;
  /** HOST_MISSING_HINT, split around its quoted command (drawn as code). */
  fix?: { before: string; code: string; after: string };
  /** HOST_FORBIDDEN_HINT's fix without its first clause ("Install the latest Downex, then …"). */
  refusedFix?: string;
}

/**
 * A result or reason as a sentence: some daemon reasons start lowercase ("the server answered …");
 * a lowercase program name ("yt-dlp could not read this page") stays as it is.
 */
export const sentence = initialCapital;

export function splitStatus(text: string): StatusText {
  let rest = text;
  let fix: StatusText['fix'];
  let refusedFix: string | undefined;
  if (rest.endsWith(HOST_FORBIDDEN_HINT)) {
    rest = rest.slice(0, -HOST_FORBIDDEN_HINT.length).trimEnd();
    const colon = HOST_FORBIDDEN_HINT.indexOf(': ');
    refusedFix = sentence(HOST_FORBIDDEN_HINT.slice(colon + 2));
  } else if (rest.endsWith(HOST_MISSING_HINT)) {
    rest = rest.slice(0, -HOST_MISSING_HINT.length).trimEnd();
    const m = /"([^"]+)"/.exec(HOST_MISSING_HINT);
    fix = m
      ? { before: HOST_MISSING_HINT.slice(0, m.index), code: m[1] ?? '', after: HOST_MISSING_HINT.slice(m.index + m[0].length) }
      : { before: HOST_MISSING_HINT, code: '', after: '' };
  }
  const at = rest.indexOf(': ');
  const out: StatusText = at > 0 ? { headline: rest.slice(0, at), detail: sentence(rest.slice(at + 2).trim()) } : { headline: rest, detail: '' };
  if (fix) out.fix = fix;
  if (refusedFix) out.refusedFix = refusedFix;
  return out;
}

/** What the popup's status shows: the relay first, then the daemon behind it. */
export type StatusState = 'checking' | 'connecting' | 'offline' | 'incompatible' | 'checking-daemon' | 'ready' | 'daemon-stopped' | 'daemon-error';

/** `connection`/`daemon` undefined: still being checked. */
export function statusState(connection: Label | undefined, incompatible: boolean, daemon: Label | undefined): StatusState {
  if (!connection) return 'checking';
  if (connection.tone === 'warn') return 'connecting';
  if (connection.tone === 'error') return incompatible ? 'incompatible' : 'offline';
  if (!daemon) return 'checking-daemon';
  if (daemon.tone === 'ok') return 'ready';
  return daemon.tone === 'warn' ? 'daemon-stopped' : 'daemon-error';
}

/** The setup hint a connection label ends with (HOST_MISSING_HINT or HOST_FORBIDDEN_HINT), if any. */
function setupHint(connection: Label | undefined): string | undefined {
  if (connection?.tone !== 'error') return undefined;
  return [HOST_MISSING_HINT, HOST_FORBIDDEN_HINT].find((h) => connection.text.endsWith(h));
}

/**
 * Downex is not set up for this extension: the browser cannot find its registration, or the
 * registration does not accept this extension's ID (the label carries one of the setup hints).
 */
export function hostMissing(connection: Label | undefined): boolean {
  return setupHint(connection) !== undefined;
}

export type ChipTone = 'ok' | 'warn' | 'done' | 'fail';

/** The header's status chip: "Connected · 3 active", "Connecting…", "Not running", … */
export interface StatusChip {
  text: string;
  tone: ChipTone;
}

export function statusChip(state: StatusState, opts: { active?: number; hostMissing?: boolean } = {}): StatusChip {
  switch (state) {
    case 'checking':
    case 'connecting':
      return { text: 'Connecting…', tone: 'warn' };
    case 'checking-daemon':
      return { text: 'Connected', tone: 'ok' };
    case 'ready': {
      const n = opts.active ?? 0;
      return { text: n > 0 ? `Connected · ${n} active` : 'Connected', tone: 'ok' };
    }
    case 'daemon-stopped':
      return { text: 'Not running', tone: 'done' };
    case 'offline':
      return opts.hostMissing ? { text: 'Not set up', tone: 'fail' } : { text: 'Not running', tone: 'done' };
    case 'incompatible':
      return { text: 'Update needed', tone: 'warn' };
    case 'daemon-error':
      return { text: 'Not responding', tone: 'warn' };
  }
}

export const DAEMON_UNKNOWN = 'Daemon status unavailable';

/**
 * The daemon's line (the chip's tooltip). The daemon is asked through the relay, so while the
 * relay itself fails the daemon's error is the relay's again: the line then only says the status is
 * unknown, muted, instead of a second error.
 */
export function daemonLine(connection: Label | undefined, daemon: Label): { text: string; tone: Label['tone'] | 'muted' } {
  if (connection?.tone === 'error' && daemon.tone === 'error') return { text: DAEMON_UNKNOWN, tone: 'muted' };
  return daemon;
}

/** The tooltip's sentence while Polite mode slows downloads (docs/POLITE-SPEC.md §8). */
export const POLITE_SLOWED = 'Polite: slowed to keep the line smooth.';

/**
 * The chip's tooltip: the relay's line, then the daemon's ("Connected (Downex 0.7.3). Daemon
 * running: …"), then Polite mode's while it slows downloads. The chip's text never changes for it.
 */
export function statusTooltip(connection: Label | undefined, daemon: Label | undefined, polite?: PoliteState): string {
  if (!connection) return 'Checking connection…';
  const hint = setupHint(connection);
  const relay = hint ? connection.text.slice(0, -hint.length).trimEnd() : connection.text;
  if (!daemon) return relay;
  const line = `${relay.replace(/[.]$/, '')}. ${daemonLine(connection, daemon).text}`;
  return polite === 'slowed' ? `${line.replace(/[.]$/, '')}. ${POLITE_SLOWED}` : line;
}

/** What replaces the page's videos while Downex cannot be reached (`popup-offline.png`). */
export interface OfflineView {
  kind: 'not-running' | 'not-set-up' | 'update' | 'background';
  title: string;
  text: string;
  /** The command to run, drawn in monospace under `text`. */
  command?: string;
  /** The browser's or relay's own words, small ("" none). */
  detail: string;
}

export const NOT_RUNNING_TEXT = 'Downex starts with the next download you send. Until then, downloads stay in the browser.';

export function offlineView(state: StatusState, connection: Label | undefined): OfflineView | undefined {
  if (!connection || (state !== 'offline' && state !== 'incompatible')) return undefined;
  const t = splitStatus(connection.text);
  if (state === 'incompatible') {
    return { kind: 'update', title: 'Downex needs an update', text: t.detail || 'Update Downex or this extension.', detail: '' };
  }
  if (t.fix) {
    // HOST_MISSING_HINT without its first clause, the command named and drawn under it:
    // "Install or reinstall Downex, or run this command, then press Check again."
    const colon = t.fix.before.indexOf(': ');
    const lead = sentence((colon >= 0 ? t.fix.before.slice(colon + 2) : t.fix.before).trimEnd());
    return {
      kind: 'not-set-up',
      title: 'Downex isn’t set up in this browser',
      text: `${lead} this command${t.fix.after}`,
      command: t.fix.code,
      detail: t.detail,
    };
  }
  if (t.refusedFix) {
    // Registered, but for other extension IDs: no command helps, a newer Downex does.
    return { kind: 'not-set-up', title: 'Downex doesn’t accept this extension', text: t.refusedFix, detail: t.detail };
  }
  if (t.headline === 'Background unavailable') {
    return { kind: 'background', title: 'The extension isn’t answering', text: 'Close this menu and open it again in a moment.', detail: t.detail };
  }
  return { kind: 'not-running', title: 'Downex isn’t running', text: NOT_RUNNING_TEXT, detail: t.detail };
}

/** A one-line note above the page's videos while the relay works but downexd does not answer. */
export function daemonNote(state: StatusState, daemon: Label | undefined): { text: string; tone: 'muted' | 'warn'; retry: boolean } | undefined {
  if (state === 'daemon-stopped') return { text: 'Downex isn’t running. It starts with the next download you send.', tone: 'muted', retry: false };
  if (state === 'daemon-error' && daemon) {
    const t = splitStatus(daemon.text);
    return { text: t.detail ? `Downex isn’t answering: ${t.detail}` : 'Downex isn’t answering.', tone: 'warn', retry: true };
  }
  return undefined;
}

/** A size keeps its unit on its line: "48.2 MB" never wraps between the number and "MB". */
export function keepUnits(s: string): string {
  return s.replace(/(\d) (B|KB|MB|GB|TB|kbit\/s|Mbit\/s)(?![\p{L}\p{N}])/gu, '$1 $2');
}

/**
 * A stream's address as a row shows it until its title is known: the host for the sub-line and the
 * path's tail for the name ("cdn.example.com/…/hls/master.m3u8" → "hls/master.m3u8").
 */
export function streamNameParts(display: string): { name: string; host: string } {
  const cut = display.indexOf('/');
  if (cut <= 0) return { name: display, host: '' };
  const rest = display.slice(cut + 1).replace(/^…\//, '');
  return { name: rest || display, host: display.slice(0, cut) };
}

export type MarkTone = 'ok' | 'info' | 'warn' | 'error';
export type MarkGlyph = 'inbox' | 'awaiting' | 'copy' | 'failed';

/** An accepted hand-off's mark: Downex saves it at once (ok), or waits for the user (info; addedTone). */
export function addedMark(text: string): { tone: MarkTone; glyph: MarkGlyph } {
  return addedTone(text) === 'ok' ? { tone: 'ok', glyph: 'inbox' } : { tone: 'info', glyph: 'awaiting' };
}

/** A history row's mark, matching recordResult's text (and addedMark for the same message). */
export function handoffMark(rec: HandoffRecord): { tone: MarkTone; glyph: MarkGlyph } {
  if (!rec.ok) return { tone: 'error', glyph: 'failed' };
  if (rec.source === 'rescue') return { tone: 'ok', glyph: 'inbox' };
  if (rec.renewCheck) return { tone: 'info', glyph: 'awaiting' };
  if (rec.awaiting === false) return { tone: 'ok', glyph: 'inbox' };
  if (rec.duplicate) return { tone: 'warn', glyph: 'copy' };
  return { tone: 'info', glyph: 'awaiting' };
}

export type PillTone = 'ok' | 'info' | 'warn' | 'fail';

/** The pill of a hand-off on the Downloads tab: what was recorded, never live progress. */
export interface HandoffPill {
  text: string;
  tone: PillTone;
  /** The whole result as a tooltip ("Waiting for confirmation in Downex. Downex asks where …"). */
  title: string;
  /** A line under it: the failure's reason, or the browser's own copy ("" none). */
  note: string;
}

const PILL_TEXT: Readonly<Record<MarkTone, string>> = {
  ok: 'Sent to Downex',
  info: 'Waiting in Downex',
  warn: 'Already in Downex',
  error: 'Not sent',
};

/** The pill of a hand-off Downex checks against its downloads whose link expired (DESKTOP-SPEC §10). */
export const RENEW_CHECK_PILL = 'Checking in Downex';
export const RENEW_CHECK_TITLE = 'Downex is checking whether this link continues a stopped download.';

export function handoffPill(rec: HandoffRecord): HandoffPill {
  if (rec.ok && rec.source === 'rescue') {
    return { text: recordResult(rec), tone: 'ok', title: `${recordResult(rec)}. ${recordResultDetail(rec) ?? ''}`.trim(), note: '' };
  }
  if (rec.ok && rec.renewCheck) return { text: RENEW_CHECK_PILL, tone: 'info', title: RENEW_CHECK_TITLE, note: '' };
  const { tone } = handoffMark(rec);
  const result = recordResult(rec);
  const detail = recordResultDetail(rec);
  let note = '';
  if (!rec.ok) note = result.startsWith('Failed: ') ? sentence(result.slice('Failed: '.length)) : '';
  else if (tone === 'warn') note = 'The browser kept its own copy too.';
  return {
    text: PILL_TEXT[tone],
    tone: tone === 'error' ? 'fail' : tone,
    title: [result.replace(/[.]?$/, '.'), detail].filter((p): p is string => !!p).join(' '),
    note,
  };
}

/** "14:02" today, "Yesterday 13:41", else a short date and time. */
export function handoffTime(ts: number, now = Date.now(), locale?: string): string {
  const d = new Date(ts);
  const time = d.toLocaleTimeString(locale, { hour: '2-digit', minute: '2-digit' });
  const today = new Date(now);
  if (d.toDateString() === today.toDateString()) return time;
  const yesterday = new Date(today.getFullYear(), today.getMonth(), today.getDate() - 1);
  if (d.toDateString() === yesterday.toDateString()) return `Yesterday ${time}`;
  return d.toLocaleString(locale, { dateStyle: 'short', timeStyle: 'short' });
}

/** "5:17", "1:02:03" ("" unknown). */
export function durationText(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '';
  const s = Math.round(ms / 1000);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = String(s % 60).padStart(2, '0');
  return h > 0 ? `${h}:${String(m).padStart(2, '0')}:${sec}` : `${m}:${sec}`;
}

/** A quality's name without its note: "4K (2160p)" → "4K", "1080p (Full HD) 60fps" → "1080p 60fps". */
export function shortQuality(name: string): string {
  return name.replace(/\s*\([^)]*\)/g, '').replace(/\s+/g, ' ').trim() || name;
}

/**
 * What a video row's list note says beyond the MP4 note, which the quality select says (siteNote,
 * SITE-MEDIA-SPEC §2.8, §6.3: the sign-in used, the browser fetch); "" when nothing.
 */
export function listNoteExtra(note: string, mp4Note: string): string {
  return note.replace(mp4Note, ' ').replace(/\s+/g, ' ').trim();
}

/**
 * A video card's sub-line: "YouTube · up to 4K · MP4", "Vimeo · Embedded · MP4"; an audio page's
 * "SoundCloud · M4A" (SITE-MEDIA-SPEC §3.13: `fileType`, no best quality).
 */
export function videoSubline(site: string, embedded: boolean, best: string, fileType = 'MP4'): string {
  return [site, embedded ? 'Embedded' : '', best ? `up to ${shortQuality(best)}` : '', fileType].filter((p) => p !== '').join(' · ');
}

/** The quality rows as the select lists them: the class's name, then its codecs and size. */
export function qualityOptions(
  rows: readonly (Pick<QualityRow, 'id' | 'quality' | 'codecs' | 'size' | 'picture'> & { recommended?: boolean })[],
): SelectMenuOption[] {
  return rows.map((r) => {
    const o: { value: string; label: string; detail?: string; meta?: string; title?: string; badge?: string } = { value: r.id, label: r.quality };
    if (r.recommended) o.badge = RECOMMENDED_TEXT;
    if (r.codecs) o.detail = r.codecs;
    if (r.size) o.meta = keepUnits(r.size);
    if (r.picture) o.title = r.picture;
    return o;
  });
}

/**
 * Whether "Find the video on this page" shows its "No cookies sent" chip: only while the sign-in
 * option ("Send my sign-in cookies when a video needs it") is off (DESIGN-SYSTEM.md §4.11).
 */
export function showsNoCookies(options: Pick<VideoOptions, 'useSignInForVideos'>): boolean {
  return !options.useSignInForVideos;
}

/**
 * The stored rules after the "Send downloads to Downex" switch: off excludes the host, on lets it
 * in again; the same stored rule "Disable on this site" wrote.
 */
export function rulesForSiteSwitch(rules: CaptureRules, host: string, send: boolean): CaptureRules {
  return withSiteExcluded(rules, host, !send);
}

// --- File tiles (DESIGN-SYSTEM.md §2.6.3) ---------------------------------------------------

/** The tiles' kinds: the desktop's categories plus `images`. */
export type TileKind = 'video' | 'audio' | 'archives' | 'documents' | 'programs' | 'images' | 'other';

const TILE_OF: Readonly<Record<FileKind, TileKind>> = {
  video: 'video',
  audio: 'audio',
  archive: 'archives',
  document: 'documents',
  program: 'programs',
  image: 'images',
  other: 'other',
};

export function tileKind(kind: FileKind): TileKind {
  return TILE_OF[kind];
}

/** The drawn area of the tiles' 32 grid (2..30), as the desktop crops it: more ink at every size. */
export const TILE_VIEWBOX = '2 2 28 28';
/** At this size and below the audio disc draws three bars (FILE_TILE_AUDIO_SMALL), as on the desktop. */
export const SMALL_TILE_PX = 20;
/** The audio disc for small tiles: three bars with gaps of 1 px or more, never a pause-like lozenge. */
export const FILE_TILE_AUDIO_SMALL =
  '<circle cx="16" cy="16" r="13.5" fill="var(--hue)"/>' +
  '<path d="M11 12v8M16 9v14M21 12v8" stroke="var(--tile-glyph)" stroke-width="2.8" stroke-linecap="round" fill="none"/>';

/**
 * The tile bodies on a 32 grid, exactly as the desktop draws them: each kind has its own
 * silhouette, solid and two-tone (`--hue`, `--deep`) with white details, never a page with lines.
 */
export const FILE_TILES: Readonly<Record<TileKind, string>> = {
  video:
    '<g transform="rotate(-11 4.5 10.5)"><rect x="4.5" y="5" width="24" height="5.6" rx="1.6" fill="var(--deep)"/>' +
    '<path d="M9.2 5h3.4l-2.6 5.6H6.6zM16.2 5h3.4L17 10.6h-3.4zM23.2 5h3.4L24 10.6h-3.4z" fill="var(--tile-glyph)"/></g>' +
    '<rect x="4.5" y="11.5" width="24" height="17" rx="3" fill="var(--hue)"/>' +
    '<path d="M14 16.1v7.8a.9.9 0 0 0 1.37.77l6.2-3.9a.9.9 0 0 0 0-1.54l-6.2-3.9A.9.9 0 0 0 14 16.1z" fill="var(--tile-glyph)"/>',
  audio:
    '<circle cx="16" cy="16" r="13.5" fill="var(--hue)"/>' +
    '<path d="M9.6 13.8v4.4M12.8 10.8v10.4M16 8.6v14.8M19.2 11.4v9.2M22.4 13.4v5.2" stroke="var(--tile-glyph)" stroke-width="2.4" stroke-linecap="round" fill="none"/>',
  archives:
    '<rect x="5" y="11" width="22" height="18" rx="2.8" fill="var(--hue)"/>' +
    '<rect x="3" y="4.5" width="26" height="8" rx="2.2" fill="var(--deep)"/>' +
    '<path d="M13.6 4.5h4.8v12.3l-2.4-1.5-2.4 1.5z" fill="var(--tile-glyph)"/>' +
    '<rect x="9.5" y="21.5" width="13" height="3" rx="1.5" fill="var(--tile-glyph)" fill-opacity=".9"/>',
  documents:
    '<rect x="10" y="2.5" width="17.5" height="22" rx="2.6" fill="var(--deep)"/>' +
    '<path d="M7 6.5h10.2l6.3 6.3V27a2.5 2.5 0 0 1-2.5 2.5H7A2.5 2.5 0 0 1 4.5 27V9A2.5 2.5 0 0 1 7 6.5z" fill="var(--hue)"/>' +
    '<path d="M17.2 6.5l6.3 6.3h-4.1a2.2 2.2 0 0 1-2.2-2.2z" fill="var(--tile-glyph)" fill-opacity=".45"/>' +
    '<path d="M8.5 6.5h4.6v9.2l-2.3-1.8-2.3 1.8z" fill="var(--tile-glyph)"/>',
  programs:
    '<path d="M16 3.2l11.1 6.4v12.8L16 28.8 4.9 22.4V9.6z" fill="var(--hue)" stroke="var(--hue)" stroke-width="2.4" stroke-linejoin="round"/>' +
    '<path d="M16 16.1l11.1-6.4v12.7L16 28.8z" fill="var(--deep)" stroke="var(--deep)" stroke-width="2.4" stroke-linejoin="round" opacity=".55"/>' +
    '<path d="M9.8 12.6l4.4 3.4-4.4 3.4M15.8 20.6h5.4" stroke="var(--tile-glyph)" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" fill="none"/>',
  images:
    '<rect x="3" y="6" width="26" height="20" rx="4" fill="var(--hue)"/>' +
    '<circle cx="10.6" cy="12.2" r="2.5" fill="var(--tile-glyph)"/>' +
    '<path d="M6.5 22.6l6.1-6.4 3.7 3.8 3.4-3.6 5.8 6.2v.4a1 1 0 0 1-1 1H7.5a1 1 0 0 1-1-1z" fill="var(--tile-glyph)"/>',
  other:
    '<rect x="4" y="4" width="24" height="24" rx="7" fill="var(--hue)"/>' +
    '<circle cx="10.4" cy="16" r="2" fill="var(--tile-glyph)"/><circle cx="16" cy="16" r="2" fill="var(--tile-glyph)"/>' +
    '<circle cx="21.6" cy="16" r="2" fill="var(--tile-glyph)"/>',
};
