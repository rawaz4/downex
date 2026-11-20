import { describe, expect, it } from 'vitest';
import {
  buttonAllowed,
  DEFAULT_VIDEO_OPTIONS,
  formatHiddenSites,
  hostListError,
  MAX_HIDDEN_SITE_CHARS,
  MAX_HIDDEN_SITES,
  mergeExcludedHosts,
  mergeExternalRules,
  parseHiddenSites,
  parseRulesForm,
  parseVideoOptions,
  rulesToForm,
  siteHidden,
  splitSize,
  VIDEO_OPTIONS_KEY,
  withSiteHidden,
  type RulesForm,
  type VideoOptions,
} from './options-form';
import { DEFAULTS_VERSION, defaultRules, normalizeHostPattern, parseRules } from './rules';

const base: RulesForm = { enabled: true, extensions: 'zip', minSize: '0', minSizeUnit: 'MB', excludedHosts: '' };

describe('splitSize', () => {
  it('uses the largest unit the size fills, with as many decimals as it needs', () => {
    expect(splitSize(0)).toEqual({ value: 0, unit: 'MB' });
    expect(splitSize(999)).toEqual({ value: 999, unit: 'B' });
    expect(splitSize(1000)).toEqual({ value: 1, unit: 'KB' });
    expect(splitSize(1500)).toEqual({ value: 1.5, unit: 'KB' });
    expect(splitSize(5 * 1000 ** 2)).toEqual({ value: 5, unit: 'MB' });
    expect(splitSize(2 * 1000 ** 3)).toEqual({ value: 2, unit: 'GB' });
    expect(splitSize(1_500_000)).toEqual({ value: 1.5, unit: 'MB' });
    expect(splitSize(123_456_789)).toEqual({ value: 123.456789, unit: 'MB' });
    // Saved in binary units before: still exact, in decimal units.
    expect(splitSize(1024)).toEqual({ value: 1.024, unit: 'KB' });
    expect(splitSize(5 * 1024 ** 2)).toEqual({ value: 5.24288, unit: 'MB' });
    expect(splitSize(1024 ** 4)).toEqual({ value: 1099.511627776, unit: 'GB' });
    // No unit's decimal reads back as the same bytes: shown in bytes.
    expect(splitSize(9_001_312_079_117_122)).toEqual({ value: 9_001_312_079_117_122, unit: 'B' });
  });

  it('shows a size the form reads back as exactly the same bytes', () => {
    const sizes = [0, 1, 999, 1000, 1024, 1500, 999_999, 1_048_576, 5 * 1024 ** 2, 123_456_789];
    sizes.push(1024 ** 4, 9_001_312_079_117_122, Number.MAX_SAFE_INTEGER);
    for (const minSizeBytes of sizes) {
      const r = parseRulesForm(rulesToForm({ ...defaultRules(), minSizeBytes }));
      expect(r.ok && r.rules.minSizeBytes).toBe(minSizeBytes);
    }
  });
});

describe('normalizeHostPattern', () => {
  it('accepts hosts, wildcards, ports, URLs and IDNs', () => {
    expect(normalizeHostPattern(' Example.COM ')).toBe('example.com');
    expect(normalizeHostPattern('*.example.com')).toBe('*.example.com');
    expect(normalizeHostPattern('cdn*.example.com')).toBe('cdn*.example.com');
    expect(normalizeHostPattern('https://dl.example.com/path?q#f')).toBe('dl.example.com');
    expect(normalizeHostPattern('example.com:8080')).toBe('example.com');
    expect(normalizeHostPattern('example.com.')).toBe('example.com');
    expect(normalizeHostPattern('*.bücher.de')).toBe('*.xn--bcher-kva.de');
    expect(normalizeHostPattern('192.168.1.10')).toBe('192.168.1.10');
    expect(normalizeHostPattern('[::1]:8080')).toBe('[::1]');
  });

  it('rejects entries that are not hosts', () => {
    expect(normalizeHostPattern('')).toBeUndefined();
    expect(normalizeHostPattern('https://')).toBeUndefined();
    expect(normalizeHostPattern('user@example.com')).toBeUndefined();
    expect(normalizeHostPattern('exa%mple.com')).toBeUndefined();
    expect(normalizeHostPattern('a..b')).toBeUndefined();
  });
});

describe('parseRulesForm', () => {
  it('round-trips the defaults', () => {
    const r = parseRulesForm(rulesToForm(defaultRules()));
    expect(r).toEqual({ ok: true, rules: defaultRules() });
  });

  it('normalises and dedupes lists', () => {
    const r = parseRulesForm({
      ...base,
      enabled: false,
      extensions: ' .ZIP, iso;tar.gz\n\nzip  7z ',
      excludedHosts: 'Example.com\n*.cdn.example.net, example.com\n',
    });
    expect(r).toEqual({
      ok: true,
      rules: {
        enabled: false,
        extensions: ['zip', 'iso', 'tar.gz', '7z'],
        minSizeBytes: 0,
        excludedHosts: ['example.com', '*.cdn.example.net'],
        defaultsVersion: DEFAULTS_VERSION,
      },
    });
  });

  it('converts the minimum size to bytes', () => {
    const size = (minSize: string, minSizeUnit: string) => {
      const r = parseRulesForm({ ...base, minSize, minSizeUnit });
      return r.ok ? r.rules.minSizeBytes : r.errors.minSize;
    };
    expect(size('', 'MB')).toBe(0);
    expect(size('2', 'MB')).toBe(2_000_000);
    expect(size('1.5', 'KB')).toBe(1500);
    expect(size('1.5', 'GB')).toBe(1_500_000_000);
    expect(size(' 100 ', 'B')).toBe(100);
    expect(size('-1', 'MB')).toMatch(/number/);
    expect(size('1e3', 'MB')).toMatch(/number/);
    expect(size('abc', 'B')).toMatch(/number/);
    expect(size('1', 'TB')).toMatch(/unit/);
    expect(size('99999999999', 'GB')).toMatch(/too large/);
  });

  it('reports invalid entries per field', () => {
    const r = parseRulesForm({ ...base, extensions: 'zip a.b.c mp4 *exe', excludedHosts: 'ok.example\nbad host!' });
    expect(r.ok).toBe(false);
    if (r.ok) return;
    expect(r.errors.extensions).toContain('"a.b.c"');
    expect(r.errors.extensions).toContain('"*exe"');
    expect(r.errors.extensions).not.toContain('"mp4"');
    expect(r.errors.excludedHosts).toContain('"host!"');
    expect(r.errors.minSize).toBeUndefined();
  });

  it('produces rules that parseRules keeps unchanged', () => {
    const r = parseRulesForm({ ...base, extensions: 'zip tar.gz', minSize: '3', excludedHosts: '*.x.com' });
    expect(r.ok).toBe(true);
    if (r.ok) expect(parseRules(r.rules)).toEqual(r.rules);
  });
});

describe('mergeExternalRules (a change saved elsewhere while the form has unsaved edits)', () => {
  const stored = { ...defaultRules(), excludedHosts: ['a.example'] };
  const saved = (form: RulesForm) => {
    const r = parseRulesForm(form);
    if (!r.ok) throw new Error(JSON.stringify(r.errors));
    return r.rules;
  };

  it("keeps the popup's 'Disable on <host>' when another field is being edited", () => {
    const form = { ...rulesToForm(stored), extensions: 'zip iso mkv' };
    const after = { ...stored, excludedHosts: ['a.example', 'example.com'] };
    const merged = mergeExternalRules(form, new Set(['extensions']), stored, after);
    expect(merged.extensions).toBe('zip iso mkv');
    expect(saved(merged)).toEqual({ ...after, extensions: ['zip', 'iso', 'mkv'] });

    // and its removal
    const removed = mergeExternalRules(form, new Set(['extensions']), after, stored);
    expect(saved(removed).excludedHosts).toEqual(['a.example']);
  });

  it('applies the hosts added and removed elsewhere to an edited host list', () => {
    const form = { ...rulesToForm(stored), excludedHosts: ['A.example, mine.example', 'bad host!'].join('\n') };
    const added = mergeExternalRules(form, new Set(['excludedHosts']), stored, { ...stored, excludedHosts: ['a.example', 'example.com'] });
    expect(added.excludedHosts.split('\n')).toEqual(['A.example, mine.example', 'bad host!', 'example.com']);
    const removed = mergeExternalRules(form, new Set(['excludedHosts']), stored, { ...stored, excludedHosts: [] });
    // the user's own entries stay as typed, invalid ones too
    expect(removed.excludedHosts.split('\n')).toEqual(['mine.example', 'bad host!']);
  });

  it("takes other fields that changed elsewhere unless they are being edited", () => {
    const form = { ...rulesToForm(stored), enabled: false, minSize: '5' };
    const after = { ...stored, minSizeBytes: 1024, extensions: ['zip'] };
    const merged = mergeExternalRules(form, new Set(['enabled', 'minSize']), stored, after);
    expect(merged.enabled).toBe(false); // not changed elsewhere: the unsaved toggle stays
    expect(merged.minSize).toBe('5');
    expect(merged.extensions).toBe('zip');
    expect(mergeExternalRules(form, new Set(['enabled']), stored, { ...stored, enabled: false }).enabled).toBe(false);
    expect(mergeExternalRules({ ...form, enabled: true }, new Set(['enabled']), stored, { ...stored, enabled: false }).enabled).toBe(false);
  });
});

describe('mergeExcludedHosts', () => {
  it('appends new hosts once and removes only the removed ones', () => {
    expect(mergeExcludedHosts('', [], ['x.example'])).toBe('x.example');
    const lines = (...l: string[]) => l.join('\n');
    expect(mergeExcludedHosts(lines('x.example', ''), [], ['x.example'])).toBe(lines('x.example', ''));
    expect(mergeExcludedHosts(lines('one.example', '', 'two.example'), ['two.example'], [])).toBe(lines('one.example', ''));
    expect(mergeExcludedHosts('*.example.com', [], [])).toBe('*.example.com');
  });
});

describe('video options', () => {
  it('reads missing or malformed fields as their defaults', () => {
    // Preparing the qualities while the user watches is on by default, also for options stored
    // before it existed; options stored before the hidden sites existed hide none. The sign-in is off
    // by default and for YouTube only (SITE-MEDIA-SPEC §6.3, second amendment 2026-11-18); watching
    // what plays is on (§2.9).
    const defaults = { videoButton: true, useSignInForVideos: false, watchPlayed: true, videoPrefetch: true, hiddenSites: [] };
    expect(parseVideoOptions(undefined)).toEqual(defaults);
    expect(parseVideoOptions({ videoButton: false, useSignInForVideos: 'yes', videoPrefetch: 0, watchPlayed: 'no', hiddenSites: 'a.example' })).toEqual({
      ...defaults,
      videoButton: false,
    });
    expect(parseVideoOptions({ useSignInForVideos: true, extra: 1 })).toEqual({ ...defaults, useSignInForVideos: true });
    expect(parseVideoOptions({ videoPrefetch: false })).toEqual({ ...defaults, videoPrefetch: false });
    expect(parseVideoOptions({ watchPlayed: false })).toEqual({ ...defaults, watchPlayed: false });
    // A stored value is kept either way: an earlier explicit on stays on (no migration).
    expect(parseVideoOptions({ videoButton: true, useSignInForVideos: true, watchPlayed: true, videoPrefetch: true, hiddenSites: [] }).useSignInForVideos).toBe(true);
    expect(parseVideoOptions({ videoButton: true, useSignInForVideos: false, watchPlayed: true, videoPrefetch: true, hiddenSites: [] }).useSignInForVideos).toBe(false);
    expect(DEFAULT_VIDEO_OPTIONS.useSignInForVideos).toBe(false);
    expect(DEFAULT_VIDEO_OPTIONS.watchPlayed).toBe(true);
    expect(parseVideoOptions('x')).toEqual(DEFAULT_VIDEO_OPTIONS);
    expect(DEFAULT_VIDEO_OPTIONS.hiddenSites).toEqual([]);
    expect(Object.isFrozen(DEFAULT_VIDEO_OPTIONS.hiddenSites)).toBe(true);
    expect(VIDEO_OPTIONS_KEY).toBe('videoOptions');
  });

  it('reads the hidden sites normalised, once, dropping invalid entries', () => {
    const o = parseVideoOptions({
      hiddenSites: ['Example.COM', '*.cdn.example.net', 'example.com', 'bad host!', 7, null, 'user@x.example', '', 'x'.repeat(254)],
    });
    expect(o.hiddenSites).toEqual(['example.com', '*.cdn.example.net']);
    // A fresh array every time: callers may change their copy.
    expect(parseVideoOptions(undefined).hiddenSites).not.toBe(parseVideoOptions(undefined).hiddenSites);
  });

  it('keeps at most 500 hidden sites, each at most 253 characters', () => {
    const many = Array.from({ length: 600 }, (_, i) => `h${i}.example`);
    const o = parseVideoOptions({ hiddenSites: many });
    expect(o.hiddenSites).toHaveLength(MAX_HIDDEN_SITES);
    expect(o.hiddenSites[0]).toBe('h0.example');
    expect(o.hiddenSites.at(-1)).toBe('h499.example');
    const long = `${'a'.repeat(63)}.${'b'.repeat(63)}.${'c'.repeat(63)}.${'d'.repeat(61)}`;
    expect(long).toHaveLength(MAX_HIDDEN_SITE_CHARS);
    expect(parseVideoOptions({ hiddenSites: [long, `${long}e`] }).hiddenSites).toEqual([long]);
  });
});

describe('buttonAllowed', () => {
  const options = (videoButton: boolean, ...hiddenSites: string[]): VideoOptions => ({
    videoButton,
    useSignInForVideos: false,
    watchPlayed: true,
    videoPrefetch: true,
    hiddenSites,
  });

  it('follows the switch and the hidden sites of the host', () => {
    expect(buttonAllowed(options(true), 'video.example')).toBe(true);
    expect(buttonAllowed(options(false), 'video.example')).toBe(false);
    expect(buttonAllowed(options(true, 'video.example'), 'video.example')).toBe(false);
    expect(buttonAllowed(options(true, 'video.example'), 'VIDEO.example.')).toBe(false);
    expect(buttonAllowed(options(true, 'video.example'), 'www.video.example')).toBe(true);
    expect(buttonAllowed(options(true, '*.video.example'), 'www.video.example')).toBe(false);
    expect(buttonAllowed(options(true, '*.video.example'), 'video.example')).toBe(false);
    expect(buttonAllowed(options(true, '*.video.example'), 'other.example')).toBe(true);
    // YouTube follows the same rule.
    expect(buttonAllowed(options(true, 'www.youtube.com'), 'www.youtube.com')).toBe(false);
    // An unknown host hides nothing.
    expect(buttonAllowed(options(true, 'video.example'), '')).toBe(true);
  });
});

describe('the site toggle of the hidden sites', () => {
  const stored: VideoOptions = {
    videoButton: true,
    useSignInForVideos: true,
    watchPlayed: true,
    videoPrefetch: false,
    hiddenSites: ['a.example', '*.b.example'],
  };

  it('reports exact and wildcard entries for a host', () => {
    expect(siteHidden(stored, 'a.example')).toEqual({ hidden: true, exact: ['a.example'], patterns: [] });
    expect(siteHidden(stored, 'x.b.example')).toEqual({ hidden: true, exact: [], patterns: ['*.b.example'] });
    expect(siteHidden(stored, 'c.example')).toEqual({ hidden: false, exact: [], patterns: [] });
  });

  it('adds the exact host once and removes only exact entries', () => {
    const added = withSiteHidden(stored, 'C.example', true);
    expect(added).toEqual({ ...stored, hiddenSites: ['a.example', '*.b.example', 'c.example'] });
    expect(withSiteHidden(added, 'c.example', true).hiddenSites).toEqual(added.hiddenSites);
    expect(withSiteHidden(added, 'c.example', false).hiddenSites).toEqual(stored.hiddenSites);
    // A wildcard stays: the host remains hidden, which siteHidden reports.
    const shown = withSiteHidden(stored, 'x.b.example', false);
    expect(shown.hiddenSites).toEqual(stored.hiddenSites);
    expect(siteHidden(shown, 'x.b.example').hidden).toBe(true);
    // Not a host, or a pattern: nothing changes.
    expect(withSiteHidden(stored, 'bad host!', true).hiddenSites).toEqual(stored.hiddenSites);
    expect(withSiteHidden(stored, '*.c.example', true).hiddenSites).toEqual(stored.hiddenSites);
    // The input is never changed.
    expect(stored.hiddenSites).toEqual(['a.example', '*.b.example']);
  });

  it('does not add beyond the limit', () => {
    const full = { ...stored, hiddenSites: Array.from({ length: MAX_HIDDEN_SITES }, (_, i) => `h${i}.example`) };
    const r = withSiteHidden(full, 'new.example', true);
    expect(r.hiddenSites).toEqual(full.hiddenSites);
    expect(siteHidden(r, 'new.example').hidden).toBe(false);
  });
});

describe('the hidden sites editor', () => {
  it('parses patterns like the excluded hosts', () => {
    expect(parseHiddenSites('')).toEqual({ sites: [], invalid: [], dropped: 0 });
    expect(parseHiddenSites(' Example.com\n*.cdn.example.net, example.com\nhttps://v.example/watch?x=1\n\n')).toEqual({
      sites: ['example.com', '*.cdn.example.net', 'v.example'],
      invalid: [],
      dropped: 0,
    });
    expect(parseHiddenSites('ok.example\nbad host!\nuser@x.example')).toEqual({
      sites: ['ok.example', 'bad'],
      invalid: ['host!', 'user@x.example'],
      dropped: 0,
    });
    const long = `${'a'.repeat(63)}.${'b'.repeat(63)}.${'c'.repeat(63)}.${'d'.repeat(62)}`;
    expect(parseHiddenSites(long).invalid).toEqual([long]);
  });

  it('counts the valid entries beyond the limit', () => {
    const text = Array.from({ length: 503 }, (_, i) => `h${i}.example`).join('\n');
    const r = parseHiddenSites(`${text}\nh0.example`);
    expect(r.sites).toHaveLength(MAX_HIDDEN_SITES);
    expect(r.dropped).toBe(3);
  });

  it('formats the stored list one per line, and the two round-trip', () => {
    const sites = ['example.com', '*.cdn.example.net'];
    expect(formatHiddenSites(sites)).toBe('example.com\n*.cdn.example.net');
    expect(formatHiddenSites([])).toBe('');
    expect(parseHiddenSites(formatHiddenSites(sites)).sites).toEqual(sites);
    expect(parseVideoOptions({ hiddenSites: parseHiddenSites(formatHiddenSites(sites)).sites }).hiddenSites).toEqual(sites);
  });

  it('words invalid hosts like the excluded hosts', () => {
    const r = parseRulesForm({ ...base, excludedHosts: 'bad host!' });
    expect(r.ok).toBe(false);
    if (r.ok) return;
    expect(hostListError(['host!'])).toBe(r.errors.excludedHosts);
    expect(hostListError(['a', 'b', 'c', 'd', 'e', 'f', 'g'])).toBe(
      'Not a host name: "a", "b", "c", "d", "e" and 2 more. Use e.g. example.com or *.example.com.',
    );
  });
});
