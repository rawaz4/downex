// Capture rules (BROWSER-SPEC §3, REQUIREMENTS B2). Pure: no browser APIs.

export const RULES_STORAGE_KEY = 'rules';
/** Alt-click bypass window (BROWSER-SPEC §3). */
export const BYPASS_WINDOW_MS = 2000;

export interface CaptureRules {
  /** Global capture toggle. */
  enabled: boolean;
  /** Lower-case extensions without a leading dot, e.g. "zip" or "tar.gz". */
  extensions: string[];
  /** Minimum size in bytes; 0 captures any size. Unknown sizes are captured. */
  minSizeBytes: number;
  /**
   * Host patterns never captured, matched against the download's URL, final URL and the page
   * it was started on; `*` matches any run of characters, `*.x.com` also matches `x.com`.
   */
  excludedHosts: string[];
  /** DEFAULTS_VERSION the extension list was last brought up to (see parseRules). */
  defaultsVersion: number;
}

/** The default list of version 1, kept to tell which extensions later versions added. */
const DEFAULT_EXTENSIONS_V1: readonly string[] = [
  'zip', 'rar', '7z', 'gz', 'tgz', 'bz2', 'xz', 'zst', 'tar',
  'exe', 'msi', 'msix', 'apk', 'deb', 'rpm', 'pkg', 'appimage',
  'iso', 'img', 'dmg',
  'mp4', 'm4v', 'mkv', 'avi', 'mov', 'webm', 'wmv', 'flv',
  'mp3', 'flac', 'm4a', 'wav', 'ogg', 'opus', 'aac',
  'pdf', 'epub',
];

export const DEFAULT_EXTENSIONS: readonly string[] = [
  // archives (and the first part of split archives)
  'zip', 'zipx', 'rar', '7z', 'gz', 'tgz', 'bz2', 'tbz', 'tbz2', 'xz', 'txz', 'zst', 'tzst', 'tar',
  'lz', 'lzma', 'lzh', 'lha', 'arj', 'ace', 'cab', 'z', 'sit', 'sitx', '001',
  // disk images and virtual disks
  'iso', 'img', 'dmg', 'bin', 'cue', 'nrg', 'mdf', 'mds', 'vhd', 'vhdx', 'vmdk', 'vdi', 'qcow2',
  'ova', 'wim', 'esd', 'swm',
  // installers and packages
  'exe', 'msi', 'msix', 'msixbundle', 'appx', 'appxbundle', 'msu', 'apk', 'apks', 'xapk', 'aab',
  'ipa', 'deb', 'rpm', 'pkg', 'mpkg', 'appimage', 'flatpak', 'snap', 'jar',
  // video
  'mp4', 'm4v', 'mkv', 'avi', 'mov', 'webm', 'wmv', 'flv', 'f4v', 'mpg', 'mpeg', 'mpe', 'm2v',
  '3gp', '3g2', 'ts', 'm2ts', 'mts', 'vob', 'ogv', 'rm', 'rmvb', 'asf', 'divx',
  // audio
  'mp3', 'flac', 'm4a', 'm4b', 'wav', 'ogg', 'oga', 'opus', 'aac', 'wma', 'aif', 'aiff', 'ape',
  'mka', 'ra', 'amr', 'wv', 'dsf', 'dff',
  // documents and books
  'pdf', 'epub', 'mobi', 'azw', 'azw3', 'djvu', 'cbz', 'cbr', 'doc', 'docx', 'xls', 'xlsx', 'ppt',
  'pptx', 'pps', 'ppsx', 'odt', 'ods', 'odp',
  // large images
  'psd', 'tif', 'tiff',
  // data
  'dat', 'sql',
];

/**
 * Version of DEFAULT_EXTENSIONS. Stored rules from an older version gain the extensions added
 * since then once; an extension the user removed afterwards stays removed.
 */
export const DEFAULTS_VERSION = 2;

// The calls below are marked pure so that a bundle that does not use them (the tracker in every
// frame, which needs only the host patterns) leaves these tables out.
const ADDED_IN: Readonly<Record<number, readonly string[]>> = {
  2: /* @__PURE__ */ DEFAULT_EXTENSIONS.filter((e) => !DEFAULT_EXTENSIONS_V1.includes(e)),
};

export const DEFAULT_RULES: Readonly<CaptureRules> = /* @__PURE__ */ Object.freeze({
  enabled: true,
  extensions: /* @__PURE__ */ DEFAULT_EXTENSIONS.slice(),
  minSizeBytes: 0,
  excludedHosts: [],
  defaultsVersion: DEFAULTS_VERSION,
});

export function defaultRules(): CaptureRules {
  return { ...DEFAULT_RULES, extensions: [...DEFAULT_RULES.extensions], excludedHosts: [] };
}

/** Normalises one user-entered extension: trims, lower-cases, drops leading dots. */
export function normalizeExtension(ext: string): string {
  return ext.trim().toLowerCase().replace(/^\.+/, '');
}

/**
 * Parses stored rules; each invalid or missing field falls back to its default. A stored
 * extension list from an older DEFAULTS_VERSION (none stored: version 1) gains the defaults
 * added since.
 */
export function parseRules(raw: unknown): CaptureRules {
  const r = defaultRules();
  if (typeof raw !== 'object' || raw === null) return r;
  const o = raw as Record<string, unknown>;
  if (typeof o.enabled === 'boolean') r.enabled = o.enabled;
  if (Array.isArray(o.extensions)) {
    r.extensions = [
      ...new Set(o.extensions.filter((e): e is string => typeof e === 'string').map(normalizeExtension)),
    ].filter((e) => e !== '');
    const from = typeof o.defaultsVersion === 'number' && Number.isInteger(o.defaultsVersion) ? o.defaultsVersion : 1;
    for (let v = from + 1; v <= DEFAULTS_VERSION; v++) {
      for (const e of ADDED_IN[v] ?? []) if (!r.extensions.includes(e)) r.extensions.push(e);
    }
  }
  if (typeof o.minSizeBytes === 'number' && Number.isFinite(o.minSizeBytes) && o.minSizeBytes >= 0) {
    r.minSizeBytes = Math.floor(o.minSizeBytes);
  }
  if (Array.isArray(o.excludedHosts)) {
    r.excludedHosts = o.excludedHosts
      .filter((h): h is string => typeof h === 'string')
      .map((h) => h.trim().toLowerCase())
      .filter((h) => h !== '');
  }
  return r;
}

/** MIME types of the default categories mapped to their usual extension. */
const MIME_EXTENSIONS: Readonly<Record<string, string>> = {
  'application/zip': 'zip',
  'application/x-zip-compressed': 'zip',
  'application/vnd.rar': 'rar',
  'application/x-rar-compressed': 'rar',
  'application/x-7z-compressed': '7z',
  'application/gzip': 'gz',
  'application/x-gzip': 'gz',
  'application/x-bzip2': 'bz2',
  'application/x-xz': 'xz',
  'application/zstd': 'zst',
  'application/x-tar': 'tar',
  'application/x-msdownload': 'exe',
  'application/vnd.microsoft.portable-executable': 'exe',
  'application/x-msi': 'msi',
  'application/x-ms-installer': 'msi',
  'application/vnd.android.package-archive': 'apk',
  'application/vnd.debian.binary-package': 'deb',
  'application/x-rpm': 'rpm',
  'application/x-iso9660-image': 'iso',
  'application/x-apple-diskimage': 'dmg',
  'video/mp4': 'mp4',
  'video/x-matroska': 'mkv',
  'video/x-msvideo': 'avi',
  'video/quicktime': 'mov',
  'video/webm': 'webm',
  'video/x-ms-wmv': 'wmv',
  'video/x-flv': 'flv',
  'audio/mpeg': 'mp3',
  'audio/flac': 'flac',
  'audio/x-flac': 'flac',
  'audio/mp4': 'm4a',
  'audio/x-m4a': 'm4a',
  'audio/wav': 'wav',
  'audio/x-wav': 'wav',
  'audio/ogg': 'ogg',
  'audio/opus': 'opus',
  'audio/aac': 'aac',
  'application/pdf': 'pdf',
  'application/epub+zip': 'epub',
  'application/x-cd-image': 'iso',
  'application/x-raw-disk-image': 'img',
  'application/x-bzip': 'bz2',
  'application/x-zstd': 'zst',
  'application/x-lzh-compressed': 'lzh',
  'application/vnd.ms-cab-compressed': 'cab',
  'application/java-archive': 'jar',
  'application/x-msdos-program': 'exe',
  'application/x-dosexec': 'exe',
  'application/x-debian-package': 'deb',
  'application/x-redhat-package-manager': 'rpm',
  'application/vnd.appimage': 'appimage',
  'video/mpeg': 'mpg',
  'video/3gpp': '3gp',
  'video/3gpp2': '3g2',
  'video/mp2t': 'ts',
  'video/ogg': 'ogv',
  'video/x-ms-asf': 'asf',
  'video/vnd.rn-realvideo': 'rm',
  'audio/x-ms-wma': 'wma',
  'audio/aiff': 'aiff',
  'audio/x-aiff': 'aiff',
  'audio/x-ape': 'ape',
  'audio/amr': 'amr',
  'audio/x-matroska': 'mka',
  'application/x-mobipocket-ebook': 'mobi',
  'image/vnd.djvu': 'djvu',
  'application/msword': 'doc',
  'application/vnd.openxmlformats-officedocument.wordprocessingml.document': 'docx',
  'application/vnd.ms-excel': 'xls',
  'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet': 'xlsx',
  'application/vnd.ms-powerpoint': 'ppt',
  'application/vnd.openxmlformats-officedocument.presentationml.presentation': 'pptx',
  'application/vnd.oasis.opendocument.text': 'odt',
  'application/vnd.oasis.opendocument.spreadsheet': 'ods',
  'application/vnd.oasis.opendocument.presentation': 'odp',
  'image/vnd.adobe.photoshop': 'psd',
  'image/tiff': 'tiff',
};

/** Essence of a MIME type: lower-case `type/subtype` without parameters. */
export function mimeEssence(mime: string | undefined): string | undefined {
  if (!mime) return undefined;
  const essence = mime.split(';', 1)[0]?.trim().toLowerCase();
  return essence ? essence : undefined;
}

/** Extension candidates of a file name: the last one and, if present, the last two ("tar.gz"). */
export function extensionsOfName(name: string | undefined): string[] {
  if (!name) return [];
  const base = name.split(/[\\/]/).pop() ?? '';
  const parts = base.toLowerCase().split('.');
  if (parts.length < 2) return [];
  const out: string[] = [];
  const last = parts[parts.length - 1];
  if (last) out.push(last);
  if (parts.length >= 3 && parts[0] !== '') {
    const two = parts.slice(-2).join('.');
    if (last) out.push(two);
  }
  return out;
}

function urlPathName(url: string | undefined): string | undefined {
  if (!url) return undefined;
  try {
    const path = new URL(url).pathname;
    const seg = path.split('/').pop();
    if (!seg) return undefined;
    try {
      return decodeURIComponent(seg);
    } catch {
      return seg;
    }
  } catch {
    return undefined;
  }
}

export interface CaptureCandidate {
  url: string;
  finalUrl?: string;
  /** Suggested or determined file name (a path is accepted; only its base name is used). */
  filename?: string;
  mime?: string;
  /** Size in bytes; undefined or negative when unknown. */
  size?: number;
  incognito?: boolean;
  /**
   * Page the download was started on (its referrer); its http(s) host is checked against the
   * excluded hosts too, so "Disable on this site" also covers downloads served from other hosts.
   */
  pageUrl?: string;
  /** Browser tab the download was started in; undefined when unknown. Not used by the rules. */
  tabId?: number;
}

export interface ClickModifiers {
  alt: boolean;
  ctrl: boolean;
  shift: boolean;
  meta: boolean;
  /** Epoch ms of the click. */
  ts: number;
}

export type SkipReason =
  | 'disabled'
  | 'unsupported_scheme'
  | 'incognito'
  /** Capture is paused in the download's tab (popup "Pause capture in this tab"). */
  | 'tab_paused'
  | 'bypass'
  | 'excluded_host'
  | 'no_match'
  | 'too_small'
  /** Started by this extension itself. */
  | 'own_download'
  /** Not an in-progress download. */
  | 'not_in_progress'
  /** downexd is not known to run; an autostart could outlast the hand-off timeout. */
  | 'daemon_unavailable'
  /** The browser sent something other than a GET (e.g. a one-time POST form). */
  | 'not_get';

export type CaptureDecision =
  | { capture: true; extension: string }
  | { capture: false; reason: SkipReason };

export interface CaptureEnv {
  now: number;
  /** The click that decides the Alt bypass for this download (see ClickTracker). */
  lastClick?: ClickModifiers | undefined;
  /** Capture is paused in the tab the download was started in. */
  tabPaused?: boolean | undefined;
}

/** All extension candidates from file name, URL path and MIME type, most specific first. */
export function candidateExtensions(c: CaptureCandidate): string[] {
  const out = [
    ...extensionsOfName(c.filename),
    ...extensionsOfName(urlPathName(c.finalUrl)),
    ...extensionsOfName(urlPathName(c.url)),
  ];
  const fromMime = MIME_EXTENSIONS[mimeEssence(c.mime) ?? ''];
  if (fromMime) out.push(fromMime);
  return [...new Set(out)];
}

export function isBypassActive(lastClick: ClickModifiers | undefined, now: number): boolean {
  if (!lastClick?.alt) return false;
  const age = now - lastClick.ts;
  return age >= 0 && age <= BYPASS_WINDOW_MS;
}

function httpHost(url: string | undefined): string | null | undefined {
  if (url === undefined || url === '') return undefined;
  try {
    const u = new URL(url);
    if (u.protocol !== 'http:' && u.protocol !== 'https:') return null;
    return u.hostname.toLowerCase().replace(/\.$/, '');
  } catch {
    return null;
  }
}

/** Host of a user pattern: accepts bare hosts, host:port and full URLs. */
function patternHost(pattern: string): string {
  let p = pattern.trim().toLowerCase();
  const scheme = p.indexOf('://');
  if (scheme >= 0) p = p.slice(scheme + 3);
  p = p.split(/[/?#]/, 1)[0] ?? '';
  if (!p.startsWith('[')) p = p.replace(/:\d*$/, '');
  return p.replace(/\.$/, '');
}

export function hostMatches(pattern: string, host: string): boolean {
  const p = patternHost(pattern);
  const h = host.toLowerCase().replace(/\.$/, '');
  if (p === '') return false;
  if (!p.includes('*')) return p === h;
  if (p.startsWith('*.') && h === p.slice(2)) return true;
  const re = new RegExp(
    '^' + p.split('*').map((s) => s.replace(/[.+?^${}()|[\]\\]/g, '\\$&')).join('.*') + '$',
  );
  return re.test(h);
}

/** Decides whether Downex takes a download. Never captures blob:/data:/non-HTTP or incognito. */
export function evaluateCapture(rules: CaptureRules, c: CaptureCandidate, env: CaptureEnv): CaptureDecision {
  if (!rules.enabled) return { capture: false, reason: 'disabled' };
  const hosts = [httpHost(c.url), httpHost(c.finalUrl)];
  if (hosts[0] === undefined || hosts.includes(null)) return { capture: false, reason: 'unsupported_scheme' };
  if (c.incognito) return { capture: false, reason: 'incognito' };
  if (env.tabPaused) return { capture: false, reason: 'tab_paused' };
  if (isBypassActive(env.lastClick, env.now)) return { capture: false, reason: 'bypass' };
  // The page only adds a host to check: a page that is not http(s) never blocks a capture.
  for (const h of [...hosts, httpHost(c.pageUrl)]) {
    if (h && rules.excludedHosts.some((p) => hostMatches(p, h))) {
      return { capture: false, reason: 'excluded_host' };
    }
  }
  const wanted = new Set(rules.extensions.map(normalizeExtension));
  const extension = candidateExtensions(c).find((e) => wanted.has(e));
  if (!extension) return { capture: false, reason: 'no_match' };
  if (rules.minSizeBytes > 0 && c.size !== undefined && c.size >= 0 && c.size < rules.minSizeBytes) {
    return { capture: false, reason: 'too_small' };
  }
  return { capture: true, extension };
}

// ---------------------------------------------------------------------------------------
// Excluded hosts: one normalisation for the options page and the popup's site toggle.

/**
 * Normalises a host pattern: accepts bare hosts, `*` wildcards, host:port and full URLs, and
 * returns the lower-case ASCII host (IDNs in punycode, as the browser reports hosts), or
 * undefined when the entry is not a host.
 */
export function normalizeHostPattern(entry: string): string | undefined {
  let p = entry.trim();
  const scheme = p.indexOf('://');
  if (scheme >= 0) p = p.slice(scheme + 3);
  p = p.split(/[/?#]/, 1)[0] ?? '';
  if (p === '' || p.includes('@')) return undefined;
  let host: string;
  try {
    host = new URL(`http://${p}`).hostname;
  } catch {
    return undefined;
  }
  host = host.replace(/\.$/, '');
  if (/^\[[0-9a-f:.]+\]$/.test(host)) return host;
  return /^[a-z0-9*_-]+(\.[a-z0-9*_-]+)*$/.test(host) ? host : undefined;
}

/** The host "Disable on this site" stores for a page: http(s) pages only, else undefined. */
export function siteHost(pageUrl: string | undefined): string | undefined {
  const h = httpHost(pageUrl);
  return h ? normalizeHostPattern(h) : undefined;
}

export interface SiteExclusion {
  /** Downloads from or started on this host are not captured. */
  excluded: boolean;
  /** Entries without wildcards naming this host: the popup's toggle adds and removes these. */
  exact: string[];
  /** Wildcard entries that match this host; only the options page changes them. */
  patterns: string[];
}

export function siteExclusion(rules: CaptureRules, host: string): SiteExclusion {
  const matching = rules.excludedHosts.filter((p) => hostMatches(p, host));
  const exact = matching.filter((p) => !p.includes('*'));
  const patterns = matching.filter((p) => p.includes('*'));
  return { excluded: matching.length > 0, exact, patterns };
}

/**
 * Rules with `host` added to (normalised, once) or its exact entries removed from the
 * excluded hosts. Wildcard entries are kept either way.
 */
export function withSiteExcluded(rules: CaptureRules, host: string, excluded: boolean): CaptureRules {
  const normalized = normalizeHostPattern(host);
  if (normalized === undefined || normalized.includes('*')) return { ...rules, excludedHosts: [...rules.excludedHosts] };
  const { exact } = siteExclusion(rules, normalized);
  let excludedHosts: string[];
  if (excluded) excludedHosts = exact.length > 0 ? [...rules.excludedHosts] : [...rules.excludedHosts, normalized];
  else excludedHosts = rules.excludedHosts.filter((p) => !exact.includes(p));
  return { ...rules, excludedHosts };
}
