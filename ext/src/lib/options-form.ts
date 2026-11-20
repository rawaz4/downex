// Options page rules editor (BROWSER-SPEC §3, §4): converts between stored rules and the
// form's text fields and validates user input. Pure: no DOM or browser APIs.

import { type CaptureRules, DEFAULTS_VERSION, hostMatches, normalizeExtension, normalizeHostPattern } from './rules';

/** The minimum size's units: decimal, as every size is shown (1 KB = 1,000 B, 1 MB = 1,000,000 B). */
export const SIZE_UNITS = { B: 1, KB: 1000, MB: 1000 ** 2, GB: 1000 ** 3 } as const;
export type SizeUnit = keyof typeof SIZE_UNITS;

export function isSizeUnit(v: string): v is SizeUnit {
  return Object.hasOwn(SIZE_UNITS, v);
}

/** The form's raw values. */
export interface RulesForm {
  enabled: boolean;
  /** Extensions separated by spaces, commas, semicolons or new lines. */
  extensions: string;
  minSize: string;
  minSizeUnit: string;
  /** Host patterns separated by new lines, spaces or commas. */
  excludedHosts: string;
}

export type RulesField = 'extensions' | 'minSize' | 'excludedHosts';

export type RulesFormResult =
  | { ok: true; rules: CaptureRules }
  | { ok: false; errors: Partial<Record<RulesField, string>> };

/** What the form reads from a typed size: Math.round(text × unit) bytes. */
function sizeBytes(text: string, unit: SizeUnit): number {
  return Math.round(Number(text) * SIZE_UNITS[unit]);
}

/**
 * The largest unit the size fills (1,500,000 B → 1.5 MB), with as many decimals as it needs, so the
 * form reads the shown value back as exactly the same bytes; 0 is shown in MB. A size saved in binary
 * units before stays exact (1 MiB → 1.048576 MB). A size no unit's decimal reads back exactly is
 * shown in bytes.
 */
export function splitSize(bytes: number): { value: number; unit: SizeUnit } {
  if (bytes === 0) return { value: 0, unit: 'MB' };
  for (const unit of ['GB', 'MB', 'KB'] as const) {
    if (bytes < SIZE_UNITS[unit]) continue;
    const value = bytes / SIZE_UNITS[unit];
    if (sizeBytes(String(value), unit) === bytes) return { value, unit };
  }
  return { value: bytes, unit: 'B' };
}

export function rulesToForm(r: CaptureRules): RulesForm {
  const size = splitSize(r.minSizeBytes);
  return {
    enabled: r.enabled,
    extensions: r.extensions.join(' '),
    minSize: String(size.value),
    minSizeUnit: size.unit,
    excludedHosts: r.excludedHosts.join('\n'),
  };
}

function splitList(text: string): string[] {
  return text.split(/[\s,;]+/).filter((s) => s !== '');
}

/** One or two dot-separated parts: the matcher compares the last one or two name parts. */
const EXTENSION_RE = /^[a-z0-9_+~-]+(\.[a-z0-9_+~-]+)?$/;

function quoteList(items: readonly string[]): string {
  const shown = items.slice(0, 5).map((s) => `"${s}"`).join(', ');
  return items.length > 5 ? `${shown} and ${items.length - 5} more` : shown;
}

/** The message for entries of a host list that are not hosts (excluded hosts, hidden sites). */
export function hostListError(bad: readonly string[]): string {
  return `Not a host name: ${quoteList(bad)}. Use e.g. example.com or *.example.com.`;
}

export function parseRulesForm(form: RulesForm): RulesFormResult {
  const errors: Partial<Record<RulesField, string>> = {};

  const extensions: string[] = [];
  const badExt: string[] = [];
  for (const raw of splitList(form.extensions)) {
    const e = normalizeExtension(raw);
    if (e === '') continue;
    if (!EXTENSION_RE.test(e)) badExt.push(raw);
    else if (!extensions.includes(e)) extensions.push(e);
  }
  if (badExt.length > 0) {
    errors.extensions = `Not a file extension: ${quoteList(badExt)}. Use letters and digits, optionally one dot (e.g. tar.gz).`;
  }

  let minSizeBytes = 0;
  const sizeText = form.minSize.trim();
  if (!isSizeUnit(form.minSizeUnit)) {
    errors.minSize = 'Choose a size unit.';
  } else if (sizeText !== '') {
    if (!/^\d+(\.\d+)?$/.test(sizeText)) {
      errors.minSize = 'Enter a number of 0 or more.';
    } else {
      const bytes = sizeBytes(sizeText, form.minSizeUnit);
      if (!Number.isSafeInteger(bytes)) errors.minSize = 'The size is too large.';
      else minSizeBytes = bytes;
    }
  }

  const excludedHosts: string[] = [];
  const badHosts: string[] = [];
  for (const raw of splitList(form.excludedHosts)) {
    const h = normalizeHostPattern(raw);
    if (h === undefined) badHosts.push(raw);
    else if (!excludedHosts.includes(h)) excludedHosts.push(h);
  }
  if (badHosts.length > 0) errors.excludedHosts = hostListError(badHosts);

  if (Object.keys(errors).length > 0) return { ok: false, errors };
  return {
    ok: true,
    rules: { enabled: form.enabled, extensions, minSizeBytes, excludedHosts, defaultsVersion: DEFAULTS_VERSION },
  };
}

/** A form control the user can edit; `minSize` covers its unit too. */
export type RulesFormField = 'enabled' | RulesField;

/**
 * The form after the stored rules changed elsewhere (the popup's capture switch or its
 * "Disable on <host>", another options page) from `before` to `after` while the user has
 * unsaved edits in the fields `edited`: what changed elsewhere is applied, the user's
 * edits are kept, and an edited host list gets the hosts added and removed elsewhere, so
 * saving the form does not silently undo the other change.
 */
export function mergeExternalRules(
  form: RulesForm,
  edited: ReadonlySet<RulesFormField>,
  before: CaptureRules,
  after: CaptureRules,
): RulesForm {
  const next = rulesToForm(after);
  return {
    enabled: before.enabled !== after.enabled || !edited.has('enabled') ? after.enabled : form.enabled,
    extensions: edited.has('extensions') ? form.extensions : next.extensions,
    minSize: edited.has('minSize') ? form.minSize : next.minSize,
    minSizeUnit: edited.has('minSize') ? form.minSizeUnit : next.minSizeUnit,
    excludedHosts: edited.has('excludedHosts')
      ? mergeExcludedHosts(form.excludedHosts, before.excludedHosts, after.excludedHosts)
      : next.excludedHosts,
  };
}

/**
 * The host list text with the patterns removed between `before` and `after` taken out and
 * the added ones appended (once). Every other entry stays as typed, invalid ones too.
 */
export function mergeExcludedHosts(text: string, before: readonly string[], after: readonly string[]): string {
  const removed = new Set(before.filter((h) => !after.includes(h)));
  const added = after.filter((h) => !before.includes(h));
  if (removed.size === 0 && added.length === 0) return text;
  const isRemoved = (entry: string) => {
    const h = normalizeHostPattern(entry);
    return h !== undefined && removed.has(h);
  };
  const lines = text.split('\n').flatMap((line) => {
    const entries = splitList(line);
    if (!entries.some(isRemoved)) return [line];
    const kept = entries.filter((e) => !isRemoved(e));
    return kept.length > 0 ? [kept.join(' ')] : [];
  });
  const present = new Set(lines.flatMap((l) => splitList(l).map(normalizeHostPattern)));
  const appended = added.filter((h) => !present.has(h));
  if (appended.length === 0) return lines.join('\n');
  const head = lines.join('\n');
  return head.trim() === '' ? appended.join('\n') : `${head.replace(/\n*$/, '')}\n${appended.join('\n')}`;
}

// ---------------------------------------------------------------------------------------
// Video options (SITE-MEDIA-SPEC §3.8, §6.3; VIDEO-DETECT-SPEC §3.7, §5.4)

/** storage.local key of the video options; never storage.session, which content scripts cannot read. */
export const VIDEO_OPTIONS_KEY = 'videoOptions';

/** Most entries `hiddenSites` keeps; more are dropped when read. */
export const MAX_HIDDEN_SITES = 500;
/** Longest `hiddenSites` entry (a DNS name's limit), normalised. */
export const MAX_HIDDEN_SITE_CHARS = 253;

export interface VideoOptions {
  /** "Show a Download button over videos": YouTube and every other site. */
  videoButton: boolean;
  /**
   * "Use my sign-in for videos that need it" (SITE-MEDIA-SPEC §6.3): a YouTube probe or add the
   * user clicked that Downex answers `auth_required` is asked once more with the site's cookies.
   * Off by default since the owner's "never rely on login" (2026-11-18); a stored value is kept.
   * Never used for any other site.
   */
  useSignInForVideos: boolean;
  /**
   * "Watch videos as they play" (SITE-MEDIA-SPEC §2.9), on by default: each tab's played media
   * files are remembered (addresses, sizes, never contents) so that Download saves the video the
   * user sees. Off: nothing is recorded, and panels have no Played source.
   */
  watchPlayed: boolean;
  /**
   * "Prepare video qualities while you watch": after 1.5 s on a video of a site that allows it
   * in a visible tab, its qualities are got in the background, so Download shows them at once
   * (BROWSER-SPEC §4, VIDEO-DETECT-SPEC §5.5).
   */
  videoPrefetch: boolean;
  /**
   * "Don't show the button on these sites": host patterns as in the capture rules' excluded
   * hosts (normalised by `normalizeHostPattern`, matched by `hostMatches` against the tab's
   * host), at most MAX_HIDDEN_SITES of at most MAX_HIDDEN_SITE_CHARS characters.
   */
  hiddenSites: string[];
}

export const DEFAULT_VIDEO_OPTIONS: Readonly<VideoOptions> = Object.freeze({
  videoButton: true,
  useSignInForVideos: false,
  watchPlayed: true,
  videoPrefetch: true,
  hiddenSites: Object.freeze([]) as readonly string[] as string[],
});

/** Longest raw entry normalised at all: a full URL may name the host. */
const MAX_HOST_ENTRY_CHARS = 4096;

/** The normalised pattern of a host list entry, or undefined when it is not one or too long. */
function hiddenSitePattern(entry: string): string | undefined {
  if (entry.length > MAX_HOST_ENTRY_CHARS) return undefined;
  const p = normalizeHostPattern(entry);
  return p !== undefined && p.length <= MAX_HIDDEN_SITE_CHARS ? p : undefined;
}

/** Stored video options; a missing or malformed field reads as its default. */
export function parseVideoOptions(raw: unknown): VideoOptions {
  const r = typeof raw === 'object' && raw !== null ? (raw as Record<string, unknown>) : {};
  const flag = (k: 'videoButton' | 'useSignInForVideos' | 'videoPrefetch' | 'watchPlayed'): boolean => {
    const v = r[k];
    return typeof v === 'boolean' ? v : DEFAULT_VIDEO_OPTIONS[k];
  };
  const hiddenSites = new Set<string>();
  if (Array.isArray(r.hiddenSites)) {
    for (const entry of r.hiddenSites as unknown[]) {
      if (hiddenSites.size >= MAX_HIDDEN_SITES) break;
      const p = typeof entry === 'string' ? hiddenSitePattern(entry) : undefined;
      if (p !== undefined) hiddenSites.add(p);
    }
  }
  return {
    videoButton: flag('videoButton'),
    useSignInForVideos: flag('useSignInForVideos'),
    watchPlayed: flag('watchPlayed'),
    videoPrefetch: flag('videoPrefetch'),
    hiddenSites: [...hiddenSites],
  };
}

/** Whether a site's hidden-sites entries hide the button on `host`. */
export interface SiteHidden {
  hidden: boolean;
  /** Entries without wildcards naming this host: the site toggles add and remove these. */
  exact: string[];
  /** Wildcard entries that match this host; only the options page changes them. */
  patterns: string[];
}

export function siteHidden(options: VideoOptions, host: string): SiteHidden {
  const matching = host === '' ? [] : options.hiddenSites.filter((p) => hostMatches(p, host));
  const exact = matching.filter((p) => !p.includes('*'));
  const patterns = matching.filter((p) => p.includes('*'));
  return { hidden: matching.length > 0, exact, patterns };
}

/**
 * Whether Download buttons may show over the videos of a tab whose page is on `host` (the tab's
 * host, so a site's embedded players follow the site; "" when unknown): `videoButton` is on and
 * no hidden-sites entry matches.
 */
export function buttonAllowed(options: VideoOptions, host: string): boolean {
  return options.videoButton && !siteHidden(options, host).hidden;
}

/**
 * Options with `host` added to (normalised, once) or its exact entries removed from the hidden
 * sites. Wildcard entries are kept either way, and nothing is added to a full list
 * (MAX_HIDDEN_SITES) or for an entry that is not a host: `siteHidden` of the result tells what
 * the user gets.
 */
export function withSiteHidden(options: VideoOptions, host: string, hidden: boolean): VideoOptions {
  const normalized = hiddenSitePattern(host);
  const sites = [...options.hiddenSites];
  if (normalized === undefined || normalized.includes('*')) return { ...options, hiddenSites: sites };
  const { exact } = siteHidden(options, normalized);
  if (!hidden) return { ...options, hiddenSites: sites.filter((p) => !exact.includes(p)) };
  if (exact.length === 0 && sites.length < MAX_HIDDEN_SITES) sites.push(normalized);
  return { ...options, hiddenSites: sites };
}

/** The options page's hidden-sites field: valid patterns (once), and the entries that are not hosts. */
export interface HiddenSitesForm {
  /** Normalised, without duplicates, at most MAX_HIDDEN_SITES. */
  sites: string[];
  /** Entries as typed that are not hosts (or longer than MAX_HIDDEN_SITE_CHARS); see hostListError. */
  invalid: string[];
  /** Valid entries beyond MAX_HIDDEN_SITES, which are not kept. */
  dropped: number;
}

/** Parses the hidden-sites field: one pattern per line (spaces, commas and semicolons also separate). */
export function parseHiddenSites(text: string): HiddenSitesForm {
  const sites = new Set<string>();
  const invalid: string[] = [];
  let dropped = 0;
  for (const raw of splitList(text)) {
    const p = hiddenSitePattern(raw);
    if (p === undefined) invalid.push(raw);
    else if (sites.has(p)) continue;
    else if (sites.size < MAX_HIDDEN_SITES) sites.add(p);
    else dropped++;
  }
  return { sites: [...sites], invalid, dropped };
}

/** The hidden-sites field's text for the stored patterns: one per line. */
export function formatHiddenSites(sites: readonly string[]): string {
  return sites.join('\n');
}
