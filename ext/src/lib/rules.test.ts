import { describe, expect, it } from 'vitest';
import {
  BYPASS_WINDOW_MS,
  type CaptureCandidate,
  type CaptureRules,
  type ClickModifiers,
  DEFAULTS_VERSION,
  defaultRules,
  evaluateCapture,
  extensionsOfName,
  hostMatches,
  parseRules,
  siteExclusion,
  siteHost,
  withSiteExcluded,
} from './rules';

const NOW = 1_700_000_000_000;

function rules(over: Partial<CaptureRules> = {}): CaptureRules {
  return { ...defaultRules(), ...over };
}

function click(over: Partial<ClickModifiers> = {}): ClickModifiers {
  return { alt: false, ctrl: false, shift: false, meta: false, ts: NOW, ...over };
}

describe('evaluateCapture', () => {
  const cases: [string, CaptureCandidate, Partial<CaptureRules>, ClickModifiers | undefined, string][] = [
    ['zip by filename', { url: 'https://a.example/dl?id=1', filename: 'file.zip' }, {}, undefined, 'capture:zip'],
    ['exe by URL path', { url: 'https://a.example/setup.exe' }, {}, undefined, 'capture:exe'],
    ['final URL path', { url: 'https://a.example/get', finalUrl: 'https://cdn.example/x.iso' }, {}, undefined, 'capture:iso'],
    ['percent-encoded URL name', { url: 'https://a.example/my%20file.MKV' }, {}, undefined, 'capture:mkv'],
    ['by MIME', { url: 'https://a.example/download', mime: 'application/zip; charset=binary' }, {}, undefined, 'capture:zip'],
    ['compound extension', { url: 'https://a.example/x', filename: 'src.tar.gz' }, { extensions: ['tar.gz'] }, undefined, 'capture:tar.gz'],
    ['user extension normalised', { url: 'https://a.example/x.BIN' }, parseRules({ extensions: ['.BIN'] }), undefined, 'capture:bin'],
    ['html page is not captured', { url: 'https://a.example/index.html', mime: 'text/html' }, {}, undefined, 'skip:no_match'],
    ['no extension at all', { url: 'https://a.example/' }, {}, undefined, 'skip:no_match'],
    ['disabled', { url: 'https://a.example/a.zip' }, { enabled: false }, undefined, 'skip:disabled'],
    ['blob: never', { url: 'blob:https://a.example/123', filename: 'a.zip' }, {}, undefined, 'skip:unsupported_scheme'],
    ['data: never', { url: 'data:application/zip;base64,AAAA', filename: 'a.zip' }, {}, undefined, 'skip:unsupported_scheme'],
    ['blob final URL', { url: 'https://a.example/a.zip', finalUrl: 'blob:https://a.example/1' }, {}, undefined, 'skip:unsupported_scheme'],
    ['ftp', { url: 'ftp://a.example/a.zip' }, {}, undefined, 'skip:unsupported_scheme'],
    ['invalid URL', { url: 'not a url', filename: 'a.zip' }, {}, undefined, 'skip:unsupported_scheme'],
    ['incognito never', { url: 'https://a.example/a.zip', incognito: true }, {}, undefined, 'skip:incognito'],
    ['alt bypass within window', { url: 'https://a.example/a.zip' }, {}, click({ alt: true, ts: NOW - 500 }), 'skip:bypass'],
    ['alt bypass at window edge', { url: 'https://a.example/a.zip' }, {}, click({ alt: true, ts: NOW - BYPASS_WINDOW_MS }), 'skip:bypass'],
    ['alt bypass expired', { url: 'https://a.example/a.zip' }, {}, click({ alt: true, ts: NOW - BYPASS_WINDOW_MS - 1 }), 'capture:zip'],
    ['ctrl does not bypass', { url: 'https://a.example/a.zip' }, {}, click({ ctrl: true }), 'capture:zip'],
    ['future click ignored', { url: 'https://a.example/a.zip' }, {}, click({ alt: true, ts: NOW + 10_000 }), 'capture:zip'],
    ['excluded exact host', { url: 'https://a.example/a.zip' }, { excludedHosts: ['a.example'] }, undefined, 'skip:excluded_host'],
    ['excluded wildcard subdomain', { url: 'https://dl.a.example/a.zip' }, { excludedHosts: ['*.a.example'] }, undefined, 'skip:excluded_host'],
    ['excluded wildcard apex', { url: 'https://a.example/a.zip' }, { excludedHosts: ['*.a.example'] }, undefined, 'skip:excluded_host'],
    ['excluded final host', { url: 'https://a.example/a.zip', finalUrl: 'https://cdn.b.example/a.zip' }, { excludedHosts: ['cdn.*'] }, undefined, 'skip:excluded_host'],
    ['other host not excluded', { url: 'https://b.example/a.zip' }, { excludedHosts: ['*.a.example'] }, undefined, 'capture:zip'],
    ['below min size', { url: 'https://a.example/a.zip', size: 999 }, { minSizeBytes: 1000 }, undefined, 'skip:too_small'],
    ['at min size', { url: 'https://a.example/a.zip', size: 1000 }, { minSizeBytes: 1000 }, undefined, 'capture:zip'],
    ['unknown size passes', { url: 'https://a.example/a.zip', size: -1 }, { minSizeBytes: 1000 }, undefined, 'capture:zip'],
  ];

  it.each(cases)('%s', (_name, candidate, over, lastClick, expected) => {
    const d = evaluateCapture(rules(over), candidate, { now: NOW, lastClick });
    expect(d.capture ? `capture:${d.extension}` : `skip:${d.reason}`).toBe(expected);
  });

  it('leaves downloads of a paused tab to the browser', () => {
    const c: CaptureCandidate = { url: 'https://a.example/a.zip', tabId: 4 };
    expect(evaluateCapture(rules(), c, { now: NOW, tabPaused: true })).toEqual({ capture: false, reason: 'tab_paused' });
    expect(evaluateCapture(rules(), c, { now: NOW, tabPaused: false }).capture).toBe(true);
    // Never-captured downloads keep their more specific reason.
    expect(evaluateCapture(rules(), { ...c, incognito: true }, { now: NOW, tabPaused: true })).toEqual({
      capture: false,
      reason: 'incognito',
    });
  });

  it('checks the page a download was started on against the excluded hosts', () => {
    const c = (pageUrl: string): CaptureCandidate => ({ url: 'https://cdn.files.example/a.zip', pageUrl });
    const r = rules({ excludedHosts: ['site.example'] });
    expect(evaluateCapture(r, c('https://site.example/downloads'), { now: NOW })).toEqual({
      capture: false,
      reason: 'excluded_host',
    });
    expect(evaluateCapture(r, c('https://other.example/'), { now: NOW }).capture).toBe(true);
    // A page that is not http(s) neither excludes nor blocks the download.
    expect(evaluateCapture(r, c('chrome://newtab/'), { now: NOW }).capture).toBe(true);
    expect(evaluateCapture(r, c('not a url'), { now: NOW }).capture).toBe(true);
  });
});

describe('site exclusion (popup "Disable on this site")', () => {
  it('derives the site host of http(s) pages with the options normalisation', () => {
    expect(siteHost('https://WWW.Example.com:8443/a?b#c')).toBe('www.example.com');
    expect(siteHost('http://bücher.de/')).toBe('xn--bcher-kva.de');
    expect(siteHost('http://[::1]:8080/')).toBe('[::1]');
    expect(siteHost('http://example.com./x')).toBe('example.com');
    expect(siteHost('chrome://extensions/')).toBeUndefined();
    expect(siteHost('about:blank')).toBeUndefined();
    expect(siteHost(undefined)).toBeUndefined();
  });

  it('separates exact entries from wildcard patterns', () => {
    const r = rules({ excludedHosts: ['example.com', 'https://example.com:8080/x', '*.example.com', 'other.org'] });
    expect(siteExclusion(r, 'example.com')).toEqual({
      excluded: true,
      exact: ['example.com', 'https://example.com:8080/x'],
      patterns: ['*.example.com'],
    });
    expect(siteExclusion(r, 'dl.example.com')).toEqual({ excluded: true, exact: [], patterns: ['*.example.com'] });
    expect(siteExclusion(r, 'example.net')).toEqual({ excluded: false, exact: [], patterns: [] });
  });

  it('adds the host once and removes only its exact entries', () => {
    const r = rules({ excludedHosts: ['*.cdn.example', 'keep.example'] });
    const added = withSiteExcluded(r, 'Site.Example', true);
    expect(added.excludedHosts).toEqual(['*.cdn.example', 'keep.example', 'site.example']);
    expect(withSiteExcluded(added, 'site.example', true).excludedHosts).toEqual(added.excludedHosts);
    expect(evaluateCapture(added, { url: 'https://site.example/a.zip' }, { now: NOW }).capture).toBe(false);
    expect(parseRules(added)).toEqual(added);

    const legacy = rules({ excludedHosts: ['site.example', 'https://site.example/', '*.site.example', 'keep.example'] });
    expect(withSiteExcluded(legacy, 'site.example', false).excludedHosts).toEqual(['*.site.example', 'keep.example']);
    // The input is never modified.
    expect(legacy.excludedHosts).toHaveLength(4);
    expect(withSiteExcluded(r, 'not a host!', true).excludedHosts).toEqual(r.excludedHosts);
  });
});

describe('hostMatches', () => {
  it.each([
    ['example.com', 'example.com', true],
    ['EXAMPLE.com', 'example.COM', true],
    ['example.com', 'www.example.com', false],
    ['*.example.com', 'a.b.example.com', true],
    ['*.example.com', 'example.com', true],
    ['*.example.com', 'badexample.com', false],
    ['*example.com', 'badexample.com', true],
    ['https://example.com:8443/path', 'example.com', true],
    ['cdn*.example.net', 'cdn12.example.net', true],
    ['a.b', 'aXb', false],
    ['', 'example.com', false],
  ] as const)('%s vs %s', (pattern, host, want) => {
    expect(hostMatches(pattern, host)).toBe(want);
  });
});

describe('extensionsOfName', () => {
  it('returns last and compound extensions', () => {
    expect(extensionsOfName('a/b/c.tar.gz')).toEqual(['gz', 'tar.gz']);
    expect(extensionsOfName('C:\\x\\file.ZIP')).toEqual(['zip']);
    expect(extensionsOfName('.bashrc')).toEqual(['bashrc']);
    expect(extensionsOfName('README')).toEqual([]);
  });
});

describe('parseRules', () => {
  it('falls back per field', () => {
    const r = parseRules({ enabled: false, extensions: 'zip', minSizeBytes: -5, excludedHosts: [' A.com ', 3, ''] });
    expect(r.enabled).toBe(false);
    expect(r.extensions).toEqual(defaultRules().extensions);
    expect(r.minSizeBytes).toBe(0);
    expect(r.excludedHosts).toEqual(['a.com']);
  });
  it('defaults for garbage', () => {
    expect(parseRules(null)).toEqual(defaultRules());
    expect(parseRules('x')).toEqual(defaultRules());
  });
  it('dedupes and normalises extensions', () => {
    expect(parseRules({ extensions: ['ZIP', '.zip', ' rar ', ''], defaultsVersion: DEFAULTS_VERSION }).extensions).toEqual([
      'zip',
      'rar',
    ]);
  });

  it('brings a stored list from an older default version up to date once', () => {
    // Version 1 stored no version; the user had removed "pdf" and added "xyz".
    const v1 = defaultRules().extensions.filter((e) => ['zip', 'iso', 'mp4'].includes(e));
    const migrated = parseRules({ extensions: [...v1, 'xyz'] });
    expect(migrated.defaultsVersion).toBe(DEFAULTS_VERSION);
    expect(migrated.extensions.slice(0, 4)).toEqual(['zip', 'iso', 'mp4', 'xyz']);
    expect(migrated.extensions).toEqual(expect.arrayContaining(['dat', 'bin', '001', 'docx', 'ts']));
    expect(migrated.extensions).not.toContain('pdf');
    // Saved again at the current version, a removed new default stays removed.
    const kept = parseRules({ ...migrated, extensions: migrated.extensions.filter((e) => e !== 'dat') });
    expect(kept.extensions).not.toContain('dat');
  });

  it('captures the new default types, by name or by MIME type', () => {
    const r = defaultRules();
    for (const name of ['disk.iso', 'payload.dat', 'firmware.bin', 'game.7z.001', 'clip.m2ts', 'book.mobi', 'sheet.xlsx']) {
      expect(evaluateCapture(r, { url: `https://a.example/${name}`, filename: name }, { now: NOW })).toMatchObject({ capture: true });
    }
    expect(
      evaluateCapture(r, { url: 'https://a.example/get?id=1', mime: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document' }, { now: NOW }),
    ).toEqual({ capture: true, extension: 'docx' });
  });
});
