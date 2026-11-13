// Form validation for the Add and Settings dialogs. Mirrors DAEMON-SPEC §3–§4 so the
// user gets immediate feedback; the daemon remains the authority and validates again.

import {
  LIMITS,
  type AddRequest,
  type Category,
  type HostRule,
  type ProxyMode,
  type Settings,
  type SiteLogin,
  type ToolsChannel,
} from './api';
import { normalizeExtension } from './categories';
import { formatCount, parseRate, splitRate, type RateUnit } from './format';

const encoder = new TextEncoder();

export type UrlCheck = { ok: true; url: string } | { ok: false; input: string; reason: string };

export function checkUrl(input: string): UrlCheck {
  if (encoder.encode(input).length > LIMITS.maxUrlBytes) {
    return { ok: false, input, reason: `is longer than ${formatCount(LIMITS.maxUrlBytes)} bytes` };
  }
  let parsed: URL;
  try {
    parsed = new URL(input);
  } catch {
    return { ok: false, input, reason: 'is not a valid URL' };
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    return { ok: false, input, reason: 'must start with http:// or https://' };
  }
  if (!parsed.hostname) return { ok: false, input, reason: 'has no host' };
  return { ok: true, url: input };
}

/** Splits a textarea into URLs (one per line or whitespace-separated), de-duplicated in order. */
export function splitUrls(text: string): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const token of text.split(/\s+/)) {
    if (token && !seen.has(token)) {
      seen.add(token);
      out.push(token);
    }
  }
  return out;
}

/** Absolute local directory: `X:\...` on Windows or `/...` on Linux. UNC paths are refused. */
export function checkDir(dir: string): string | null {
  if (dir === '') return null;
  if (/^[\\/]{2}/.test(dir)) return 'Network (UNC) and device paths are not supported';
  if (/^[A-Za-z]:[\\/]/.test(dir) || dir.startsWith('/')) return null;
  return 'Use an absolute folder path';
}

export function checkFilename(name: string): string | null {
  if (name === '') return null;
  if (/[\\/]/.test(name)) return 'A file name cannot contain / or \\';
  if (name === '.' || name === '..') return 'Not a valid file name';
  if (/[\u0000-\u001f\u007f]/.test(name)) return 'A file name cannot contain control characters';
  if (name.length > LIMITS.maxFilenameLength) {
    return `Keep the file name under ${LIMITS.maxFilenameLength + 1} characters`;
  }
  return null;
}

export function checkIntRange(text: string, min: number, max: number, label: string): string | null {
  const t = text.trim();
  if (!/^\d+$/.test(t)) return `${label} must be a whole number from ${min} to ${max}`;
  const n = Number(t);
  if (n < min || n > max) return `${label} must be from ${min} to ${max}`;
  return null;
}

export interface AddForm {
  readonly urls: string;
  readonly dir: string;
  readonly filename: string;
  readonly connections: string;
  readonly speedValue: string;
  readonly speedUnit: RateUnit;
  readonly startPaused: boolean;
  /** The queue to add to (M6); 0 or absent = the main queue. */
  readonly queueId?: number;
}

export interface AddFormErrors {
  urls?: string;
  dir?: string;
  filename?: string;
  connections?: string;
  speed?: string;
}

export type AddFormResult =
  | { readonly ok: true; readonly requests: AddRequest[] }
  | { readonly ok: false; readonly errors: AddFormErrors };

const MAX_LISTED_BAD_URLS = 3;

export function validateAddForm(form: AddForm): AddFormResult {
  const errors: AddFormErrors = {};
  const urls = splitUrls(form.urls);
  if (urls.length === 0) {
    errors.urls = 'Enter at least one URL';
  } else {
    const bad = urls.map(checkUrl).filter((c): c is Extract<UrlCheck, { ok: false }> => !c.ok);
    if (bad.length > 0) {
      const listed = bad
        .slice(0, MAX_LISTED_BAD_URLS)
        .map((b) => `"${truncate(b.input, 60)}" ${b.reason}`);
      const more = bad.length > MAX_LISTED_BAD_URLS ? ` (+${bad.length - MAX_LISTED_BAD_URLS} more)` : '';
      errors.urls = `${listed.join('; ')}${more}`;
    }
  }
  const dir = form.dir.trim();
  const dirError = checkDir(dir);
  if (dirError) errors.dir = dirError;

  const filename = urls.length > 1 ? '' : form.filename.trim();
  const filenameError = checkFilename(filename);
  if (filenameError) errors.filename = filenameError;

  const connError = checkIntRange(
    form.connections,
    LIMITS.minConnections,
    LIMITS.maxConnections,
    'Connections',
  );
  if (connError) errors.connections = connError;

  const speed = parseRate(form.speedValue, form.speedUnit);
  if (speed === null) errors.speed = 'Enter a number, or leave empty for no limit';

  if (Object.keys(errors).length > 0) return { ok: false, errors };

  const maxConnections = Number(form.connections.trim());
  const queueId = form.queueId ?? 0;
  const requests: AddRequest[] = urls.map((url) => ({
    url,
    dir,
    filename,
    maxConnections,
    speedLimit: speed ?? 0,
    startPaused: form.startPaused,
    source: 'ui',
    ...(queueId > 0 ? { queueId } : {}),
  }));
  return { ok: true, requests };
}

/**
 * The Add dialog's "Video or stream" mode: one address, an HLS/DASH manifest (MEDIA-SPEC §6)
 * or a web page whose video downexd resolves with yt-dlp (SITE-MEDIA-SPEC §5.4).
 */
export interface StreamForm {
  readonly url: string;
  readonly dir: string;
  readonly filename: string;
}

export interface StreamFormErrors {
  url?: string;
  dir?: string;
  filename?: string;
}

export type StreamFormResult =
  | { readonly ok: true; readonly url: string; readonly dir: string; readonly filename: string }
  | { readonly ok: false; readonly errors: StreamFormErrors };

export function validateStreamForm(form: StreamForm): StreamFormResult {
  const errors: StreamFormErrors = {};
  const url = form.url.trim();
  if (url === '') errors.url = 'Enter the address of a video page, or of an HLS (.m3u8) or DASH (.mpd) manifest';
  else {
    const check = checkUrl(url);
    if (!check.ok) errors.url = `The address ${check.reason}`;
  }
  const dir = form.dir.trim();
  const dirError = checkDir(dir);
  if (dirError) errors.dir = dirError;
  const filename = form.filename.trim();
  const filenameError = checkFilename(filename);
  if (filenameError) errors.filename = filenameError;
  if (Object.keys(errors).length > 0) return { ok: false, errors };
  return { ok: true, url, dir, filename };
}

/** A category as the Settings dialog edits it. */
export interface CategoryDraft {
  /** Identifies the editor row (never sent). */
  readonly key: number;
  readonly name: string;
  /** As typed: separated by spaces or commas; leading dots and capitals are allowed. */
  readonly extensions: string;
  readonly dir: string;
}

export interface CategoryDraftErrors {
  name?: string;
  extensions?: string;
  dir?: string;
}

const EXTENSION = /^[a-z0-9]+$/;

/** Splits typed extensions into the daemon's form (lower case, no dots), de-duplicated in order. */
export function parseExtensions(text: string): { extensions: string[]; invalid: string[] } {
  const extensions: string[] = [];
  const invalid: string[] = [];
  for (const token of text.split(/[\s,;]+/)) {
    if (!token) continue;
    const ext = normalizeExtension(token);
    if (ext.length > LIMITS.maxExtensionLength || !EXTENSION.test(ext)) invalid.push(token);
    else if (!extensions.includes(ext)) extensions.push(ext);
  }
  return { extensions, invalid };
}

export function formatExtensions(extensions: readonly string[]): string {
  return extensions.join(' ');
}

export function categoryDrafts(categories: readonly Category[], firstKey = 1): CategoryDraft[] {
  return categories.map((c, i) => ({
    key: firstKey + i,
    name: c.name,
    extensions: formatExtensions(c.extensions),
    dir: c.dir,
  }));
}

/**
 * Checks the categories as DESKTOP-SPEC §2 asks: unique non-empty names of at most 64
 * characters (unique ignoring case too, since their folders would be the same on
 * Windows), absolute folders or none, at most 32 categories and 200 extensions each.
 */
export function validateCategories(drafts: readonly CategoryDraft[]):
  | { readonly ok: true; readonly categories: Category[] }
  | { readonly ok: false; readonly list?: string; readonly rows: Record<number, CategoryDraftErrors> } {
  const rows: Record<number, CategoryDraftErrors> = {};
  const categories: Category[] = [];
  const seen = new Set<string>();
  let list: string | undefined;
  if (drafts.length > LIMITS.maxCategories) list = `Keep at most ${LIMITS.maxCategories} categories`;
  for (const draft of drafts) {
    const row: CategoryDraftErrors = {};
    const name = draft.name.trim();
    if (name === '') row.name = 'Enter a name';
    else if ([...name].length > LIMITS.maxCategoryNameLength) {
      row.name = `Keep the name under ${LIMITS.maxCategoryNameLength + 1} characters`;
    } else if (/[\u0000-\u001f\u007f]/.test(name)) row.name = 'A name cannot contain control characters';
    else if (seen.has(name.toLowerCase())) row.name = 'Another category has this name';
    seen.add(name.toLowerCase());

    const { extensions, invalid } = parseExtensions(draft.extensions);
    if (invalid.length > 0) {
      row.extensions = `Not an extension: ${invalid.slice(0, 3).map((x) => `"${truncate(x, 20)}"`).join(', ')} (letters and digits, up to ${LIMITS.maxExtensionLength})`;
    } else if (extensions.length > LIMITS.maxExtensionsPerCategory) {
      row.extensions = `Keep at most ${LIMITS.maxExtensionsPerCategory} extensions`;
    }

    const dir = draft.dir.trim();
    const dirError = checkDir(dir);
    if (dirError) row.dir = dirError;

    if (row.name || row.extensions || row.dir) rows[draft.key] = row;
    categories.push({ name, extensions, dir });
  }
  if (list || Object.keys(rows).length > 0) return { ok: false, list, rows };
  return { ok: true, categories };
}

export interface SettingsForm {
  readonly maxConcurrent: string;
  readonly speedValue: string;
  readonly speedUnit: RateUnit;
  /** The limit as loaded (bytes per second): kept exactly while its text is unchanged (parseRate). */
  readonly speedLimitBytes?: number;
  readonly defaultDir: string;
  readonly defaultConnections: string;
  readonly categories: readonly CategoryDraft[];
  readonly useCategoryFolders: boolean;
  readonly confirmBrowserDownloads: boolean;
  readonly notifyOnComplete: boolean;
  readonly notifyOnFailure: boolean;
  readonly launchAtLogin: boolean;
  readonly showTrayIcon: boolean;
  readonly showProgressWindow: boolean;
  readonly showCompleteDialog: boolean;
  /** Settings → Network (M6); absent = the loaded values are kept. */
  readonly network?: NetworkForm;
  /** Settings → Integration (M6); absent = kept. */
  readonly integration?: IntegrationForm;
  /** Settings → Updates (M6); absent = kept. */
  readonly updates?: UpdatesForm;
  /** Settings → Video sites (SITE-MEDIA-SPEC §5.4); absent = kept. */
  readonly video?: VideoForm;
}

export interface SettingsFormErrors {
  maxConcurrent?: string;
  speed?: string;
  defaultDir?: string;
  defaultConnections?: string;
  /** About the list of categories as a whole. */
  categories?: string;
  /** Per category, by CategoryDraft.key. */
  categoryRows?: Readonly<Record<number, CategoryDraftErrors>>;
  // ---- M6 ----
  proxy?: string;
  httpProxy?: string;
  httpsProxy?: string;
  socksProxy?: string;
  bypass?: string;
  pacUrl?: string;
  proxyUser?: string;
  /** Per host rule, by HostRuleDraft.key. */
  hostRuleRows?: Readonly<Record<number, HostRuleDraftErrors>>;
  /** Per site login, by SiteLoginDraft.key. */
  siteLoginRows?: Readonly<Record<number, SiteLoginDraftErrors>>;
  avScanCommand?: string;
  avScanArgs?: string;
  updateFeedUrl?: string;
}

// ---- Settings → Network, Integration, Updates (docs/M6-SPEC.md §2–§4) -----------------

/** A host rule as the Network page edits it. */
export interface HostRuleDraft {
  readonly key: number;
  readonly pattern: string;
  /** As typed; 0 or empty = the default. */
  readonly connections: string;
  readonly speedValue: string;
  readonly speedUnit: RateUnit;
  /** The limit as loaded (bytes per second): kept exactly while its text is unchanged (parseRate). */
  readonly speedLimitBytes?: number;
  /** The longest ranged request in MB, as typed; 0 or empty = no cap. */
  readonly maxRequest: string;
  /** The cap as loaded, in bytes: kept exactly while its text is unchanged. */
  readonly maxRequestBytes: number;
}

export interface HostRuleDraftErrors {
  pattern?: string;
  connections?: string;
  speed?: string;
  maxRequest?: string;
}

/**
 * A site login as the Network page edits it. Its password is never shown: a new one
 * waits in `password` until the settings are saved (then Api.setSecret), and
 * `clearPassword` removes the stored one.
 */
export interface SiteLoginDraft {
  readonly key: number;
  /** "" until downexd assigned one (a new login). */
  readonly id: string;
  readonly urlPattern: string;
  readonly username: string;
  /** A password is stored for it (as loaded). */
  readonly hasPassword: boolean;
  readonly password: string;
  readonly clearPassword: boolean;
}

export interface SiteLoginDraftErrors {
  urlPattern?: string;
  username?: string;
}

export interface NetworkForm {
  /** 'unspecified' is shown as (and means) system. */
  readonly proxyMode: ProxyMode;
  readonly httpProxy: string;
  readonly httpsProxy: string;
  readonly socksProxy: string;
  readonly bypass: string;
  readonly pacUrl: string;
  readonly proxyUser: string;
  readonly hostRules: readonly HostRuleDraft[];
  readonly siteLogins: readonly SiteLoginDraft[];
}

export interface IntegrationForm {
  readonly clipboardMonitor: boolean;
  readonly markDownloadedFiles: boolean;
  readonly avScanEnabled: boolean;
  readonly avScanCommand: string;
  readonly avScanArgs: string;
}

export interface UpdatesForm {
  readonly updateFeedUrl: string;
  readonly autoCheckUpdates: boolean;
}

/** Settings → Video sites (SITE-MEDIA-SPEC §5.4). */
export interface VideoForm {
  readonly siteMedia: boolean;
  readonly toolsAutoUpdate: boolean;
  readonly toolsChannel: ToolsChannel;
}

/** 1 MB = 1,000,000 B: the Network page edits request caps in decimal megabytes. */
const MB = 1000 * 1000;

/** A request cap in MB as the Network page shows it: "10", "10.486" (10 MiB); "" for none. */
export function requestCapText(bytes: number): string {
  return bytes > 0 ? String(Math.round((bytes / MB) * 1000) / 1000) : '';
}

/**
 * The caps downexd accepts, in MB as the page says them: "1.05" (LIMITS.minRequestBytes, 1 MiB,
 * rounded up) to "1073" (LIMITS.maxRequestBytes, 1 GiB, rounded down), so both ends parse.
 */
export const REQUEST_CAP_RANGE_MB = {
  min: String(Math.ceil((LIMITS.minRequestBytes / MB) * 100) / 100),
  max: String(Math.floor(LIMITS.maxRequestBytes / MB)),
} as const;

/**
 * A host rule's request cap as typed (MB): 0 or empty = no cap, else 1.05–1073 MB (1 MiB to
 * 1 GiB, what downexd accepts). The loaded value is kept exactly while its text is
 * unchanged. Null when it is not one.
 */
export function parseRequestCap(text: string, loadedBytes = 0): number | null {
  const t = text.trim();
  if (t === '' || /^0+(\.0*)?$/.test(t)) return 0;
  if (loadedBytes > 0 && t === requestCapText(loadedBytes)) return loadedBytes;
  if (!/^\d+(\.\d+)?$/.test(t)) return null;
  const bytes = Math.round(Number(t) * MB);
  return bytes >= LIMITS.minRequestBytes && bytes <= LIMITS.maxRequestBytes ? bytes : null;
}

export function hostRuleDrafts(rules: readonly HostRule[], firstKey = 1): HostRuleDraft[] {
  return rules.map((r, i) => {
    const rate = splitRate(r.speedLimit);
    return {
      key: firstKey + i,
      pattern: r.pattern,
      connections: r.maxConnections > 0 ? String(r.maxConnections) : '',
      speedValue: rate.value,
      speedUnit: rate.unit,
      speedLimitBytes: r.speedLimit,
      maxRequest: requestCapText(r.maxRequestBytes),
      maxRequestBytes: r.maxRequestBytes,
    };
  });
}

export function siteLoginDrafts(logins: readonly SiteLogin[], firstKey = 1): SiteLoginDraft[] {
  return logins.map((l, i) => ({
    key: firstKey + i,
    id: l.id,
    urlPattern: l.urlPattern,
    username: l.username,
    hasPassword: l.hasPassword,
    password: '',
    clearPassword: false,
  }));
}

/**
 * The Network page's editor of one site login: a new one (`key` null) or a login of the
 * list. It belongs to the Settings dialog, so that it outlives the page (switching pages)
 * and Save can apply what it holds.
 */
export interface SiteLoginEditor {
  key: number | null;
  urlPattern: string;
  username: string;
  password: string;
  clearPassword: boolean;
  /** The address pattern it opened with (a new login's may be proposed). */
  readonly openedWith: string;
}

export interface SiteLoginEditorErrors {
  urlPattern?: string;
  username?: string;
}

/** The editor of a new login, for an address pattern (auth_required proposes one). */
export function newLoginEditor(urlPattern = ''): SiteLoginEditor {
  return { key: null, urlPattern, username: '', password: '', clearPassword: false, openedWith: urlPattern };
}

/** The editor of a login of the list. */
export function loginEditorOf(l: SiteLoginDraft): SiteLoginEditor {
  return { key: l.key, urlPattern: l.urlPattern, username: l.username, password: '', clearPassword: l.clearPassword, openedWith: l.urlPattern };
}

/** The editor holds something the user entered or changed (closing it would lose it). */
export function loginEditorChanged(e: SiteLoginEditor, logins: readonly SiteLoginDraft[]): boolean {
  if (e.password !== '') return true;
  const l = e.key === null ? undefined : logins.find((x) => x.key === e.key);
  if (!l) return e.username.trim() !== '' || e.urlPattern.trim() !== e.openedWith.trim();
  return e.urlPattern.trim() !== l.urlPattern.trim() || e.username.trim() !== l.username.trim() || e.clearPassword !== l.clearPassword;
}

/**
 * Applies the editor to the list: a new login is added (with a key from `newKey`), an
 * edited one replaced; its password waits in the draft until the settings are saved.
 * `key` is the login's key in the list.
 */
export function applyLoginEditor(
  logins: readonly SiteLoginDraft[],
  e: SiteLoginEditor,
  newKey: () => number,
): { ok: true; siteLogins: SiteLoginDraft[]; key: number } | { ok: false; errors: SiteLoginEditorErrors } {
  const urlPattern = e.urlPattern.trim();
  const username = e.username.trim();
  const errors: SiteLoginEditorErrors = {};
  const pe = checkSitePattern(urlPattern);
  if (pe) errors.urlPattern = urlPattern === '' ? 'Enter the address it is for' : pe;
  if (username === '') errors.username = 'Enter the user name';
  else if (CONTROL.test(username)) errors.username = 'A user name cannot contain control characters';
  if (errors.urlPattern || errors.username) return { ok: false, errors };
  const existing = e.key === null ? undefined : logins.find((l) => l.key === e.key);
  if (!existing) {
    const key = newKey();
    return {
      ok: true,
      key,
      siteLogins: [...logins, { key, id: '', urlPattern, username, hasPassword: false, password: e.password, clearPassword: false }],
    };
  }
  return {
    ok: true,
    key: existing.key,
    siteLogins: logins.map((l) =>
      l === existing
        ? { ...l, urlPattern, username, password: e.password || l.password, clearPassword: e.password ? false : e.clearPassword }
        : l,
    ),
  };
}

export function networkForm(s: Settings, firstKey = 1): NetworkForm {
  return {
    proxyMode: s.proxy.mode,
    httpProxy: s.proxy.httpProxy,
    httpsProxy: s.proxy.httpsProxy,
    socksProxy: s.proxy.socksProxy,
    bypass: s.proxy.bypass,
    pacUrl: s.proxy.pacUrl,
    proxyUser: s.proxy.username,
    hostRules: hostRuleDrafts(s.hostRules, firstKey),
    siteLogins: siteLoginDrafts(s.siteLogins, firstKey + s.hostRules.length),
  };
}

export function integrationForm(s: Settings): IntegrationForm {
  return {
    clipboardMonitor: s.clipboardMonitor,
    markDownloadedFiles: s.markDownloadedFiles,
    avScanEnabled: s.avScanEnabled,
    avScanCommand: s.avScanCommand,
    avScanArgs: s.avScanArgs,
  };
}

export function updatesForm(s: Settings): UpdatesForm {
  return { updateFeedUrl: s.updateFeedUrl, autoCheckUpdates: s.autoCheckUpdates };
}

export function videoForm(s: Settings): VideoForm {
  return { siteMedia: s.siteMedia, toolsAutoUpdate: s.toolsAutoUpdate, toolsChannel: s.toolsChannel };
}

const CONTROL = /[\u0000-\u001f\u007f]/;
/** A host as a proxy address names it: a name or address ("_" and a trailing dot are fine). */
const HOST_NAME = /^[A-Za-z0-9\-._~!$&'()*+,;=\u0080-\uffff]+$/;
const IPV6 = /^[0-9A-Fa-f:.]+$/;

/**
 * "host:port" as net.SplitHostPort reads it (as `downex proxy set manual --http` checks
 * it): a bracketed IPv6 address or a host without ':', '/', '@' or spaces, and a port from
 * 1 to 65535. Null when it is one.
 */
function hostPortProblem(text: string): string | null {
  let host: string;
  let port: string;
  const bracket = /^\[([^\]]*)\]:(.*)$/.exec(text);
  if (bracket) {
    host = bracket[1] ?? '';
    port = bracket[2] ?? '';
    if (!IPV6.test(host)) return 'bad host';
  } else {
    const i = text.lastIndexOf(':');
    if (i < 0) return 'no port';
    host = text.slice(0, i);
    port = text.slice(i + 1);
    if (host.includes('@')) return 'login';
    if (!HOST_NAME.test(host)) return 'bad host';
  }
  if (!/^\d{1,5}$/.test(port)) return 'no port';
  const n = Number(port);
  return n >= 1 && n <= 65535 ? null : 'bad port';
}

/**
 * The address of an HTTP proxy as downexd and the CLI take it: host:port, optionally as
 * an http:// or https:// (a proxy reached over TLS) address; null when fine (or empty).
 */
export function checkHostPort(text: string): string | null {
  const t = text.trim();
  if (t === '') return null;
  const scheme = /^([A-Za-z][A-Za-z0-9+.-]*):\/\//.exec(t);
  const kind = scheme?.[1]?.toLowerCase();
  if (kind !== undefined && kind.startsWith('socks')) return 'Enter a SOCKS proxy in the SOCKS field';
  if (kind !== undefined && kind !== 'http' && kind !== 'https') return 'Enter host:port, for example proxy.example.com:8080';
  const rest = scheme ? t.slice(scheme[0].length).replace(/\/$/, '') : t;
  const problem = hostPortProblem(rest);
  if (problem === 'login') return 'Enter the user name below, not in the address';
  return problem === null ? null : 'Enter host:port, for example proxy.example.com:8080';
}

/** "socks5://host:port" (socks5h, socks4, socks4a too, as downexd takes them); null when fine (or empty). */
export function checkSocks(text: string): string | null {
  const t = text.trim();
  if (t === '') return null;
  const m = /^socks(?:5h?|4a?)?:\/\/(.*)$/i.exec(t);
  const problem = m ? hostPortProblem((m[1] ?? '').replace(/\/$/, '')) : 'scheme';
  if (problem === 'login') return 'Enter the user name below, not in the address';
  return problem === null ? null : 'Enter socks5://host:port (socks4:// and socks4a:// work too)';
}

/**
 * A host rule pattern as downexd takes it: a host, "*.example.com", "*" or a glob with *
 * and ? (no scheme, port, path or spaces); letters of any case (it is stored in lower case).
 */
export function checkHostPattern(text: string): string | null {
  const t = text.trim().toLowerCase();
  if (t === '') return 'Enter a host, such as example.com or *.example.com';
  return /^[a-z0-9.\-_*?]+$/.test(t) ? null : 'Use a host such as example.com; * and ? match any characters';
}

/**
 * A site login pattern as downexd takes it: an http, https or * address glob such as
 * "https://example.com/*", or a host pattern such as "*.example.com".
 */
export function checkSitePattern(text: string): string | null {
  const t = text.trim();
  if (t === '') return 'Enter the address it is for, such as https://example.com/*';
  if (/\s/.test(t) || CONTROL.test(t)) return 'An address pattern cannot contain spaces';
  const i = t.indexOf('://');
  if (i >= 0) {
    const scheme = t.slice(0, i).toLowerCase();
    if ((scheme === 'http' || scheme === 'https' || scheme === '*') && t.length > i + 3) return null;
  } else if (checkHostPattern(t) === null) return null;
  return 'Use an address such as https://example.com/*, or a host such as *.example.com';
}

/** An absolute path of a program (Windows "C:\…" or "/…"). */
export function checkProgramPath(text: string): string | null {
  const t = text.trim();
  if (t === '') return null;
  if (/^[\\/]{2}/.test(t)) return 'Network (UNC) paths are not supported';
  return /^[A-Za-z]:[\\/]./.test(t) || /^\/./.test(t) ? null : 'Use the absolute path of the program';
}

/** An address with one of `schemes` ("https:"), or null when fine. */
function checkAddress(text: string, schemes: readonly string[], what: string): string | null {
  let url: URL;
  try {
    url = new URL(text);
  } catch {
    return `${what} is not a valid address`;
  }
  if (!schemes.includes(url.protocol)) return `${what} must start with ${schemes.map((s) => `${s}//`).join(' or ')}`;
  if (url.protocol !== 'file:' && !url.hostname) return `${what} has no host`;
  return null;
}

function validateNetwork(
  form: NetworkForm,
  errors: SettingsFormErrors,
): { proxy: Settings['proxy']; hostRules: HostRule[]; siteLogins: SiteLogin[] } | null {
  const before = Object.keys(errors).length;
  const mode = form.proxyMode;
  const http = form.httpProxy.trim();
  const https = form.httpsProxy.trim();
  const socks = form.socksProxy.trim();
  const pac = form.pacUrl.trim();
  // Only the fields of the chosen mode are shown and used (as downexd checks them); the
  // others are kept as they are, like `downex proxy set system` keeps them.
  if (mode === 'manual') {
    const e1 = checkHostPort(http);
    if (e1) errors.httpProxy = e1;
    const e2 = checkHostPort(https);
    if (e2) errors.httpsProxy = e2;
    const e3 = checkSocks(socks);
    if (e3) errors.socksProxy = e3;
    if (!http && !https && !socks) errors.proxy = 'Enter at least one proxy, or choose another option';
    if (CONTROL.test(form.bypass)) errors.bypass = 'The list cannot contain control characters';
  }
  if (mode === 'pac') {
    const e = pac === '' ? 'Enter the address of the proxy configuration script' : checkAddress(pac, ['http:', 'https:', 'file:'], 'The script address');
    if (e) errors.pacUrl = e;
  }
  if (mode !== 'none' && CONTROL.test(form.proxyUser)) errors.proxyUser = 'A user name cannot contain control characters';

  const ruleRows: Record<number, HostRuleDraftErrors> = {};
  const hostRules: HostRule[] = [];
  const patterns = new Set<string>();
  for (const r of form.hostRules) {
    const row: HostRuleDraftErrors = {};
    const pattern = r.pattern.trim().toLowerCase();
    const pe = checkHostPattern(pattern);
    if (pe) row.pattern = pe;
    else if (patterns.has(pattern)) row.pattern = 'Another rule has this host';
    patterns.add(pattern);
    const conn = r.connections.trim();
    const ce = conn === '' ? null : checkIntRange(conn, 0, LIMITS.maxConnections, 'Connections');
    if (ce) row.connections = ce;
    const speed = parseRate(r.speedValue, r.speedUnit, r.speedLimitBytes);
    if (speed === null) row.speed = 'Enter a number, or leave empty for no limit';
    const cap = parseRequestCap(r.maxRequest, r.maxRequestBytes);
    if (cap === null) row.maxRequest = `Enter ${REQUEST_CAP_RANGE_MB.min} to ${REQUEST_CAP_RANGE_MB.max} MB, or leave empty for no cap`;
    if (row.pattern || row.connections || row.speed || row.maxRequest) ruleRows[r.key] = row;
    hostRules.push({ pattern, maxConnections: conn === '' ? 0 : Number(conn), speedLimit: speed ?? 0, maxRequestBytes: cap ?? 0 });
  }
  if (Object.keys(ruleRows).length > 0) errors.hostRuleRows = ruleRows;

  const loginRows: Record<number, SiteLoginDraftErrors> = {};
  const siteLogins: SiteLogin[] = [];
  const seen = new Set<string>();
  for (const l of form.siteLogins) {
    const row: SiteLoginDraftErrors = {};
    const urlPattern = l.urlPattern.trim();
    const username = l.username.trim();
    const pe = checkSitePattern(urlPattern);
    if (pe) row.urlPattern = pe;
    // The editor asks for a user name; one stored without (by another client) is kept.
    if (CONTROL.test(username)) row.username = 'A user name cannot contain control characters';
    const key = `${urlPattern.toLowerCase()}\n${username}`;
    if (!row.urlPattern && !row.username && seen.has(key)) row.urlPattern = 'Another login has this address and user name';
    seen.add(key);
    if (row.urlPattern || row.username) loginRows[l.key] = row;
    siteLogins.push({ id: l.id, urlPattern, username, hasPassword: l.hasPassword });
  }
  if (Object.keys(loginRows).length > 0) errors.siteLoginRows = loginRows;
  if (Object.keys(errors).length > before) return null;
  return {
    proxy: {
      mode,
      httpProxy: http,
      httpsProxy: https,
      socksProxy: socks,
      bypass: form.bypass.trim(),
      pacUrl: pac,
      username: form.proxyUser.trim(),
      hasPassword: false, // set from the loaded settings by the caller
    },
    hostRules,
    siteLogins,
  };
}

function validateIntegration(form: IntegrationForm, errors: SettingsFormErrors): IntegrationForm | null {
  const before = Object.keys(errors).length;
  const command = form.avScanCommand.trim();
  const ce = checkProgramPath(command);
  if (ce) errors.avScanCommand = ce;
  else if (form.avScanEnabled && command === '') errors.avScanCommand = 'Choose the scanner program';
  if (CONTROL.test(form.avScanArgs)) errors.avScanArgs = 'The arguments cannot contain control characters';
  else if (form.avScanArgs.length > LIMITS.maxScanArgsLength) errors.avScanArgs = 'The arguments are too long';
  if (Object.keys(errors).length > before) return null;
  return { ...form, avScanCommand: command, avScanArgs: form.avScanArgs.trim() };
}

function validateUpdates(form: UpdatesForm, errors: SettingsFormErrors): UpdatesForm | null {
  const url = form.updateFeedUrl.trim();
  if (url !== '') {
    const e = checkAddress(url, ['https:'], 'The update address');
    if (e) {
      errors.updateFeedUrl = e;
      return null;
    }
  }
  return { updateFeedUrl: url, autoCheckUpdates: form.autoCheckUpdates };
}

/**
 * A page of the Settings window (DESIGN-SYSTEM.md §4.9), in the order of its side navigation.
 * Errors are shown on Downloads, Network, Integration and Updates (General, Video sites,
 * Appearance and About have no field that can be wrong).
 */
export type SettingsPage = 'general' | 'downloads' | 'network' | 'integration' | 'video' | 'appearance' | 'updates' | 'about';

/** A Settings field the window can open with the keyboard on it (the status bar's "Custom…" limit). */
export type SettingsField = 'globalSpeedLimit';

/** The pages in the order of the side navigation. */
export const SETTINGS_PAGES: readonly SettingsPage[] = ['general', 'downloads', 'network', 'integration', 'video', 'appearance', 'updates', 'about'];

const DOWNLOADS_ERRORS: readonly (keyof SettingsFormErrors)[] = [
  'maxConcurrent',
  'speed',
  'defaultDir',
  'defaultConnections',
  'categories',
  'categoryRows',
];

const NETWORK_ERRORS: readonly (keyof SettingsFormErrors)[] = [
  'proxy',
  'httpProxy',
  'httpsProxy',
  'socksProxy',
  'bypass',
  'pacUrl',
  'proxyUser',
  'hostRuleRows',
  'siteLoginRows',
];

const INTEGRATION_ERRORS: readonly (keyof SettingsFormErrors)[] = ['avScanCommand', 'avScanArgs'];

/** The page that shows the field of an error. */
export function pageOfError(key: keyof SettingsFormErrors): SettingsPage {
  if (DOWNLOADS_ERRORS.includes(key)) return 'downloads';
  if (NETWORK_ERRORS.includes(key)) return 'network';
  if (INTEGRATION_ERRORS.includes(key)) return 'integration';
  return 'updates';
}

/** The errors shown on one page. */
export function errorsOnPage(errors: SettingsFormErrors, page: SettingsPage): SettingsFormErrors {
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(errors)) {
    if (v !== undefined && pageOfError(k as keyof SettingsFormErrors) === page) out[k] = v;
  }
  return out as SettingsFormErrors;
}

/** The first page (in the order of the side navigation) that has an error, or null. */
export function firstErrorPage(errors: SettingsFormErrors): SettingsPage | null {
  const pages = new Set(
    (Object.keys(errors) as (keyof SettingsFormErrors)[]).filter((k) => errors[k] !== undefined).map(pageOfError),
  );
  return SETTINGS_PAGES.find((p) => pages.has(p)) ?? null;
}

export type SettingsFormResult =
  | { readonly ok: true; readonly settings: Settings }
  | { readonly ok: false; readonly errors: SettingsFormErrors };

/**
 * Validates the Settings dialog. The result starts from `base`, the settings as they
 * were loaded, so that fields the dialog does not show are written back unchanged.
 */
export function validateSettingsForm(form: SettingsForm, base: Settings): SettingsFormResult {
  const errors: SettingsFormErrors = {};
  const concurrentError = checkIntRange(
    form.maxConcurrent,
    LIMITS.minConcurrent,
    LIMITS.maxConcurrent,
    'Simultaneous downloads',
  );
  if (concurrentError) errors.maxConcurrent = concurrentError;
  const speed = parseRate(form.speedValue, form.speedUnit, form.speedLimitBytes);
  if (speed === null) errors.speed = 'Enter a number, or leave empty for no limit';
  const dir = form.defaultDir.trim();
  if (dir === '') errors.defaultDir = 'Choose a default folder';
  else {
    const dirError = checkDir(dir);
    if (dirError) errors.defaultDir = dirError;
  }
  const connError = checkIntRange(
    form.defaultConnections,
    LIMITS.minConnections,
    LIMITS.maxConnections,
    'Connections',
  );
  if (connError) errors.defaultConnections = connError;
  const categories = validateCategories(form.categories);
  if (!categories.ok) {
    if (categories.list) errors.categories = categories.list;
    if (Object.keys(categories.rows).length > 0) errors.categoryRows = categories.rows;
  }
  const network = form.network ? validateNetwork(form.network, errors) : null;
  const integration = form.integration ? validateIntegration(form.integration, errors) : null;
  const updates = form.updates ? validateUpdates(form.updates, errors) : null;
  // The channel comes from a select: anything else is the default, as downexd reads it.
  const video: VideoForm | null = form.video
    ? { ...form.video, toolsChannel: form.video.toolsChannel === 'stable' ? 'stable' : 'nightly' }
    : null;
  if (Object.keys(errors).length > 0 || !categories.ok) return { ok: false, errors };
  return {
    ok: true,
    settings: {
      ...base,
      ...(network
        ? {
            // A mode the user did not change stays as stored ('unspecified' = system).
            proxy: { ...network.proxy, hasPassword: base.proxy.hasPassword },
            hostRules: network.hostRules,
            siteLogins: network.siteLogins,
          }
        : {}),
      ...(integration ?? {}),
      ...(updates ?? {}),
      ...(video ?? {}),
      maxConcurrentDownloads: Number(form.maxConcurrent.trim()),
      globalSpeedLimit: speed ?? 0,
      defaultDir: dir,
      defaultMaxConnections: Number(form.defaultConnections.trim()),
      categories: categories.categories,
      useCategoryFolders: form.useCategoryFolders,
      confirmBrowserDownloads: form.confirmBrowserDownloads,
      notifyOnComplete: form.notifyOnComplete,
      notifyOnFailure: form.notifyOnFailure,
      launchAtLogin: form.launchAtLogin,
      showTrayIcon: form.showTrayIcon,
      showProgressWindow: form.showProgressWindow,
      showCompleteDialog: form.showCompleteDialog,
    },
  };
}

function truncate(s: string, n: number): string {
  return s.length > n ? `${s.slice(0, n - 1)}\u2026` : s;
}
