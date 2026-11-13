import { describe, expect, it } from 'vitest';
import { DEFAULT_SETTINGS, type Settings } from './api';
import {
  categoryDrafts,
  checkDir,
  checkHostPattern,
  checkHostPort,
  checkProgramPath,
  checkSitePattern,
  checkSocks,
  errorsOnPage,
  firstErrorPage,
  pageOfError,
  SETTINGS_PAGES,
  integrationForm,
  networkForm,
  checkFilename,
  checkUrl,
  parseExtensions,
  parseRequestCap,
  requestCapText,
  REQUEST_CAP_RANGE_MB,
  splitUrls,
  validateAddForm,
  validateSettingsForm,
  validateStreamForm,
  videoForm,
  type AddForm,
  type SettingsForm,
} from './validate';

const MiB = 1024 * 1024;
const MB = 1000 * 1000;

const form = (patch: Partial<AddForm> = {}): AddForm => ({
  urls: 'https://downloads.example.com/a.zip',
  dir: '',
  filename: '',
  connections: '8',
  speedValue: '',
  speedUnit: 'MB/s',
  startPaused: false,
  ...patch,
});

describe('checkUrl', () => {
  it('accepts http and https URLs with a host', () => {
    expect(checkUrl('https://example.com/a').ok).toBe(true);
    expect(checkUrl('http://127.0.0.1:8080/x?y=1').ok).toBe(true);
  });

  it('rejects other schemes, garbage and oversized URLs', () => {
    expect(checkUrl('ftp://example.com/a')).toMatchObject({ ok: false, reason: expect.stringContaining('http') });
    expect(checkUrl('not a url').ok).toBe(false);
    expect(checkUrl(`https://example.com/${'a'.repeat(33 * 1024)}`)).toMatchObject({ ok: false, reason: 'is longer than 32,768 bytes' });
  });
});

describe('splitUrls', () => {
  it('splits on any whitespace and removes duplicates', () => {
    expect(splitUrls(' https://a.example/1\n\nhttps://a.example/2  https://a.example/1\r\n')).toEqual([
      'https://a.example/1',
      'https://a.example/2',
    ]);
  });
});

describe('paths and names', () => {
  it('accepts absolute local folders only', () => {
    expect(checkDir('')).toBeNull();
    expect(checkDir('C:\\Users\\you\\Downloads')).toBeNull();
    expect(checkDir('d:/data')).toBeNull();
    expect(checkDir('/home/you')).toBeNull();
    expect(checkDir('relative/dir')).toMatch(/absolute/);
    expect(checkDir('\\\\server\\share')).toMatch(/UNC/);
    expect(checkDir('//server/share')).toMatch(/UNC/);
  });

  it('refuses separators and control characters in file names', () => {
    expect(checkFilename('ok name.zip')).toBeNull();
    expect(checkFilename('a/b.zip')).not.toBeNull();
    expect(checkFilename('a\\b.zip')).not.toBeNull();
    expect(checkFilename('..')).not.toBeNull();
    expect(checkFilename('bad\u0007.zip')).not.toBeNull();
    expect(checkFilename('x'.repeat(256))).not.toBeNull();
  });
});

describe('validateAddForm', () => {
  it('builds one request per URL with the chosen options', () => {
    const r = validateAddForm(
      form({
        urls: 'https://a.example/1.iso\nhttps://a.example/2.iso',
        dir: ' /data/isos ',
        filename: 'ignored.iso',
        connections: '16',
        speedValue: '1.5',
        startPaused: true,
      }),
    );
    expect(r.ok).toBe(true);
    if (!r.ok) return;
    expect(r.requests).toEqual([
      { url: 'https://a.example/1.iso', dir: '/data/isos', filename: '', maxConnections: 16, speedLimit: 1.5 * MB, startPaused: true, source: 'ui' },
      { url: 'https://a.example/2.iso', dir: '/data/isos', filename: '', maxConnections: 16, speedLimit: 1.5 * MB, startPaused: true, source: 'ui' },
    ]);
  });

  it('keeps the file name for a single URL', () => {
    const r = validateAddForm(form({ filename: ' renamed.zip ' }));
    expect(r.ok && r.requests[0]?.filename).toBe('renamed.zip');
  });

  it('reports every invalid field', () => {
    const r = validateAddForm(
      form({ urls: 'ftp://x.example/a https://ok.example/b', dir: 'rel', connections: '33', speedValue: 'fast' }),
    );
    expect(r.ok).toBe(false);
    if (r.ok) return;
    expect(r.errors.urls).toContain('ftp://x.example/a');
    expect(r.errors.dir).toBeDefined();
    expect(r.errors.connections).toMatch(/1 to 32|1 and 32|from 1 to 32/);
    expect(r.errors.speed).toBeDefined();
    expect(r.errors.filename).toBeUndefined();
  });

  it('requires at least one URL and a whole number of connections', () => {
    const r = validateAddForm(form({ urls: '  \n ', connections: '2.5' }));
    expect(r.ok).toBe(false);
    if (r.ok) return;
    expect(r.errors.urls).toMatch(/at least one/);
    expect(r.errors.connections).toBeDefined();
  });

  it('summarises long lists of bad URLs', () => {
    const r = validateAddForm(form({ urls: 'a b c d e' }));
    expect(!r.ok && r.errors.urls).toContain('(+2 more)');
  });
});

describe('validateSettingsForm', () => {
  const base: Settings = {
    ...DEFAULT_SETTINGS,
    defaultDir: '/home/you/Downloads',
    categories: [
      { name: 'Video', extensions: ['mp4', 'mkv'], dir: '' },
      { name: 'Books', extensions: ['epub'], dir: '/data/books' },
    ],
    launchAtLogin: true,
  };
  const ok: SettingsForm = {
    maxConcurrent: '3',
    speedValue: '',
    speedUnit: 'MB/s',
    defaultDir: '/home/you/Downloads',
    defaultConnections: '32',
    categories: categoryDrafts(base.categories),
    useCategoryFolders: true,
    confirmBrowserDownloads: true,
    notifyOnComplete: true,
    notifyOnFailure: true,
    launchAtLogin: true,
    showTrayIcon: true,
    showProgressWindow: true,
    showCompleteDialog: true,
  };

  it('writes back every field, unchanged ones included', () => {
    expect(validateSettingsForm(ok, base)).toEqual({ ok: true, settings: base });
    const r = validateSettingsForm({ ...ok, speedValue: '2', notifyOnComplete: false, showTrayIcon: false }, base);
    expect(r).toEqual({
      ok: true,
      settings: { ...base, globalSpeedLimit: 2 * MB, notifyOnComplete: false, showTrayIcon: false },
    });
    const progress = validateSettingsForm({ ...ok, showProgressWindow: false, showCompleteDialog: false }, base);
    expect(progress).toEqual({ ok: true, settings: { ...base, showProgressWindow: false, showCompleteDialog: false } });
  });

  it('keeps fields of the loaded settings that the form does not edit', () => {
    const newer = { ...base, futureField: 'kept' } as Settings;
    const r = validateSettingsForm(ok, newer);
    expect(r.ok && (r.settings as Settings & { futureField?: string }).futureField).toBe('kept');
  });

  it('edits categories: rename, extensions, folder, add and remove', () => {
    const [video, books] = ok.categories;
    const r = validateSettingsForm(
      {
        ...ok,
        categories: [
          { ...video!, name: ' Movies ', extensions: '.MP4, mkv  webm mkv' },
          { key: 99, name: 'Comics', extensions: 'cbz cbr', dir: 'C:\\Comics' },
        ],
      },
      base,
    );
    expect(r.ok && r.settings.categories).toEqual([
      { name: 'Movies', extensions: ['mp4', 'mkv', 'webm'], dir: '' },
      { name: 'Comics', extensions: ['cbz', 'cbr'], dir: 'C:\\Comics' },
    ]);
    expect(books).toBeDefined();
  });

  it('checks categories like the daemon', () => {
    const r = validateSettingsForm(
      {
        ...ok,
        categories: [
          { key: 1, name: 'Video', extensions: 'mp4', dir: '' },
          { key: 2, name: 'video', extensions: 'mp4.x', dir: 'relative' },
          { key: 3, name: '  ', extensions: 'x'.repeat(17), dir: '' },
          { key: 4, name: 'n'.repeat(65), extensions: '', dir: '' },
        ],
      },
      base,
    );
    expect(r.ok).toBe(false);
    if (r.ok) return;
    expect(r.errors.categoryRows?.[1]).toBeUndefined();
    expect(r.errors.categoryRows?.[2]).toMatchObject({ name: expect.any(String), extensions: expect.any(String), dir: expect.any(String) });
    expect(r.errors.categoryRows?.[3]).toMatchObject({ name: 'Enter a name', extensions: expect.any(String) });
    expect(r.errors.categoryRows?.[4]?.name).toMatch(/65/);

    const many = Array.from({ length: 33 }, (_, i) => ({ key: i, name: `C${i}`, extensions: '', dir: '' }));
    const tooMany = validateSettingsForm({ ...ok, categories: many }, base);
    expect(!tooMany.ok && tooMany.errors.categories).toMatch(/32/);
    const exts = Array.from({ length: 201 }, (_, i) => `e${i}`).join(' ');
    const tooLong = validateSettingsForm({ ...ok, categories: [{ key: 1, name: 'X', extensions: exts, dir: '' }] }, base);
    expect(!tooLong.ok && tooLong.errors.categoryRows?.[1]?.extensions).toMatch(/200/);
  });

  it('enforces the daemon ranges', () => {
    const r = validateSettingsForm({ ...ok, maxConcurrent: '17', defaultConnections: '0', defaultDir: '' }, base);
    expect(r.ok).toBe(false);
    if (r.ok) return;
    expect(Object.keys(r.errors).sort()).toEqual(['defaultConnections', 'defaultDir', 'maxConcurrent']);
  });
});

describe('parseExtensions', () => {
  it('normalises, de-duplicates and reports what is not an extension', () => {
    expect(parseExtensions(' .Tar, gz;;TGZ tar ')).toEqual({ extensions: ['tar', 'gz', 'tgz'], invalid: [] });
    expect(parseExtensions('ok b-ad ü').invalid).toEqual(['b-ad', 'ü']);
    expect(parseExtensions('')).toEqual({ extensions: [], invalid: [] });
  });
});

describe('M6 settings checks', () => {
  it('checks proxies, host patterns and program paths like downexd and the CLI', () => {
    // host:port as net.SplitHostPort reads it: "_" and a trailing dot are fine, http:// and https:// too.
    for (const good of ['proxy.example.com:8080', '[::1]:3128', '', 'corp_proxy:3128', 'proxy.example.com.:8080', 'http://proxy:80', 'HTTPS://proxy:443/', '10.0.0.1:3128']) {
      expect(checkHostPort(good), good).toBeNull();
    }
    for (const bad of ['proxy.example.com', 'proxy:0', 'proxy:65536', 'a b:80', 'socks5://proxy:1080', 'ftp://proxy:21', 'me:pw@proxy:8080', 'proxy:8080/path', 'a:b:80', ':8080']) {
      expect(checkHostPort(bad), bad).not.toBeNull();
    }
    for (const good of ['socks5://127.0.0.1:1080', 'SOCKS4a://proxy:1080', 'socks4://p:1080', 'socks5h://corp_socks:1080', 'socks://p.example.com.:1080']) {
      expect(checkSocks(good), good).toBeNull();
    }
    for (const bad of ['socks6://proxy:1080', 'proxy:1080', 'socks5://me:pw@proxy:1080', 'socks5://proxy', 'socks5://proxy:1080/x']) {
      expect(checkSocks(bad), bad).not.toBeNull();
    }
    // Host rule patterns as downexd's: a host, *.suffix, * and ? globs.
    for (const good of ['*.example.com', 'example.com', '*example.com', '*', 'cdn*.example.com', 'a?c.example.com', 'my_host', 'Example.COM']) {
      expect(checkHostPattern(good), good).toBeNull();
    }
    for (const bad of ['', 'example.com:80', 'https://example.com', 'a/b', 'a b', 'me@example.com', 'ex%41mple.com']) {
      expect(checkHostPattern(bad), bad).not.toBeNull();
    }
    // Site login patterns as downexd's: an http(s)/* address glob, or a host pattern.
    for (const good of ['https://example.com/*', '*://files.example.com/private/*', '*.example.com', 'example.com']) {
      expect(checkSitePattern(good), good).toBeNull();
    }
    for (const bad of ['', 'ftp://example.com/*', 'https://', 'example.com/path', 'https://example.com/a b']) {
      expect(checkSitePattern(bad), bad).not.toBeNull();
    }
    expect(checkProgramPath('C:\\Tools\\scan.exe')).toBeNull();
    expect(checkProgramPath('/usr/bin/clamscan')).toBeNull();
    expect(checkProgramPath('scan.exe')).not.toBeNull();
    expect(checkProgramPath('\\\\server\\share\\scan.exe')).not.toBeNull();
  });

  const base: Settings = { ...DEFAULT_SETTINGS, defaultDir: '/home/you/Downloads' };
  const general: SettingsForm = {
    maxConcurrent: '3',
    speedValue: '',
    speedUnit: 'MB/s',
    defaultDir: '/home/you/Downloads',
    defaultConnections: '32',
    categories: categoryDrafts(base.categories),
    useCategoryFolders: true,
    confirmBrowserDownloads: true,
    notifyOnComplete: true,
    notifyOnFailure: true,
    launchAtLogin: false,
    showTrayIcon: true,
    showProgressWindow: true,
    showCompleteDialog: true,
  };

  it('keeps the loaded M6 settings when a form has no M6 pages', () => {
    const loaded = { ...base, clipboardMonitor: true, updateFeedUrl: 'https://u.example/f.json' };
    expect(validateSettingsForm(general, loaded)).toEqual({ ok: true, settings: loaded });
  });

  it('builds the network settings and finds the page of each error', () => {
    const network = {
      ...networkForm(base, 100),
      proxyMode: 'manual' as const,
      httpProxy: ' proxy:8080 ',
      hostRules: [
        { key: 1, pattern: 'Example.com', connections: '', speedValue: '1', speedUnit: 'MB/s' as const, maxRequest: '', maxRequestBytes: 0 },
      ],
      siteLogins: [{ key: 2, id: '', urlPattern: 'https://example.com/*', username: 'me', hasPassword: false, password: 'x', clearPassword: false }],
    };
    const ok = validateSettingsForm({ ...general, network }, base);
    expect(ok).toMatchObject({
      ok: true,
      settings: {
        proxy: { mode: 'manual', httpProxy: 'proxy:8080', hasPassword: false },
        hostRules: [{ pattern: 'example.com', maxConnections: 0, speedLimit: MB, maxRequestBytes: 0 }],
        siteLogins: [{ id: '', urlPattern: 'https://example.com/*', username: 'me', hasPassword: false }],
      },
    });
    // The password never becomes part of the settings.
    expect(JSON.stringify(ok)).not.toContain('"x"');

    const bad = validateSettingsForm(
      {
        ...general,
        network: { ...network, httpProxy: '', proxyMode: 'manual', hostRules: [...network.hostRules, { ...network.hostRules[0]!, key: 3 }] },
        integration: { ...integrationForm(base), avScanEnabled: true },
        updates: { updateFeedUrl: 'ftp://u.example/f', autoCheckUpdates: true },
      },
      base,
    );
    expect(bad.ok).toBe(false);
    if (bad.ok) return;
    expect(bad.errors).toMatchObject({
      proxy: expect.stringMatching(/at least one proxy/),
      hostRuleRows: { 3: { pattern: 'Another rule has this host' } },
      avScanCommand: 'Choose the scanner program',
      updateFeedUrl: expect.stringMatching(/https/),
    });
    expect(firstErrorPage(bad.errors)).toBe('network');
    expect(firstErrorPage({ avScanArgs: 'x' })).toBe('integration');
    expect(firstErrorPage({ updateFeedUrl: 'x' })).toBe('updates');
    // The download fields are on Downloads (General has none that can be wrong).
    expect(firstErrorPage({ defaultDir: 'x', updateFeedUrl: 'x' })).toBe('downloads');
    expect(firstErrorPage({})).toBeNull();
    // PAC needs its address.
    const pac = validateSettingsForm({ ...general, network: { ...networkForm(base), proxyMode: 'pac' } }, base);
    expect(pac).toMatchObject({ ok: false, errors: { pacUrl: expect.stringMatching(/address/) } });
  });

  it('checks only the proxy fields of the chosen mode, and keeps the others as they are', () => {
    // As "downex proxy set system" leaves them: manual values (one of them odd) and a PAC address.
    const stored: Settings = {
      ...base,
      proxy: {
        ...base.proxy,
        mode: 'system',
        httpProxy: 'corp proxy',
        httpsProxy: 'corp_proxy:3128',
        socksProxy: 'proxy:1080',
        bypass: 'a\tb',
        pacUrl: 'ftp://wpad.example/proxy.pac',
        username: 'me',
      },
    };
    for (const mode of ['system', 'unspecified', 'none'] as const) {
      const r = validateSettingsForm({ ...general, network: { ...networkForm(stored), proxyMode: mode } }, stored);
      expect(r, mode).toMatchObject({
        ok: true,
        settings: { proxy: { mode, httpProxy: 'corp proxy', httpsProxy: 'corp_proxy:3128', socksProxy: 'proxy:1080', pacUrl: 'ftp://wpad.example/proxy.pac' } },
      });
    }
    // A user name with a control character is only checked where it is shown (not with no proxy).
    const user = { ...networkForm(stored), proxyUser: 'm\u0007e' };
    expect(validateSettingsForm({ ...general, network: { ...user, proxyMode: 'none' } }, stored).ok).toBe(true);
    expect(validateSettingsForm({ ...general, network: { ...user, proxyMode: 'system' } }, stored)).toMatchObject({ ok: false, errors: { proxyUser: expect.any(String) } });

    const manual = validateSettingsForm({ ...general, network: { ...networkForm(stored), proxyMode: 'manual' } }, stored);
    expect(manual.ok).toBe(false);
    if (manual.ok) return;
    expect(Object.keys(manual.errors).sort()).toEqual(['bypass', 'httpProxy', 'socksProxy']);
    const pac = validateSettingsForm({ ...general, network: { ...networkForm(stored), proxyMode: 'pac' } }, stored);
    expect(pac.ok).toBe(false);
    if (pac.ok) return;
    expect(Object.keys(pac.errors)).toEqual(['pacUrl']);

    // Host rules other clients may write.
    const rules = validateSettingsForm(
      {
        ...general,
        network: {
          ...networkForm(stored),
          hostRules: ['*', 'cdn*.example.com', 'my_host'].map((pattern, i) => ({
            key: i + 1,
            pattern,
            connections: '',
            speedValue: '',
            speedUnit: 'MB/s' as const,
            maxRequest: '',
            maxRequestBytes: 0,
          })),
        },
      },
      stored,
    );
    expect(rules.ok && rules.settings.hostRules.map((r) => r.pattern)).toEqual(['*', 'cdn*.example.com', 'my_host']);
  });

  it('puts the chosen queue into Add requests', () => {
    const r = validateAddForm(form({ queueId: 3 }));
    expect(r).toMatchObject({ ok: true, requests: [{ queueId: 3 }] });
    expect(validateAddForm(form()).ok && (validateAddForm(form()) as { requests: object[] }).requests[0]).not.toHaveProperty('queueId');
  });
});
describe('site media settings (SITE-MEDIA-SPEC §3.8, §5.4)', () => {
  const base: Settings = { ...DEFAULT_SETTINGS, defaultDir: '/home/you/Downloads', categories: [] };
  const general: SettingsForm = {
    maxConcurrent: '3',
    speedValue: '',
    speedUnit: 'MB/s',
    defaultDir: '/home/you/Downloads',
    defaultConnections: '32',
    categories: [],
    useCategoryFolders: true,
    confirmBrowserDownloads: true,
    notifyOnComplete: true,
    notifyOnFailure: true,
    launchAtLogin: false,
    showTrayIcon: true,
    showProgressWindow: true,
    showCompleteDialog: true,
  };

  it('reads a host rule request cap in MB, keeping a loaded value exactly', () => {
    expect(requestCapText(0)).toBe('');
    expect(requestCapText(10 * MB)).toBe('10');
    // a cap set in MiB before shows rounded, and stays exact while unchanged
    expect(requestCapText(10 * MiB)).toBe('10.486');
    expect(parseRequestCap('10.486', 10 * MiB)).toBe(10 * MiB);
    expect(parseRequestCap('10.486')).toBe(10_486_000);
    expect(requestCapText(1_500_000)).toBe('1.5');
    expect(parseRequestCap('')).toBe(0);
    expect(parseRequestCap(' 0 ')).toBe(0);
    expect(parseRequestCap('10')).toBe(10 * MB);
    expect(parseRequestCap('1.5')).toBe(1.5 * MB);
    expect(parseRequestCap('1024')).toBe(1024 * MB);
    // what downexd accepts: 1 MiB to 1 GiB, said as 1.05 to 1073 MB
    expect(REQUEST_CAP_RANGE_MB).toEqual({ min: '1.05', max: '1073' });
    expect(parseRequestCap('1.05')).toBe(1_050_000);
    expect(parseRequestCap('1073')).toBe(1_073_000_000);
    expect(parseRequestCap('1')).toBeNull();
    expect(parseRequestCap('1074')).toBeNull();
    expect(parseRequestCap('1.5', 1_500_000)).toBe(1_500_000); // unchanged: as loaded
    expect(parseRequestCap('0.5')).toBeNull();
    expect(parseRequestCap('1100')).toBeNull();
    expect(parseRequestCap('ten')).toBeNull();
    expect(parseRequestCap('-1')).toBeNull();
  });

  it('writes the Video sites page into the settings; the channel is nightly or stable', () => {
    expect(videoForm(DEFAULT_SETTINGS)).toEqual({ siteMedia: true, toolsAutoUpdate: true, toolsChannel: 'nightly' });
    const r = validateSettingsForm({ ...general, video: { siteMedia: false, toolsAutoUpdate: false, toolsChannel: 'stable' } }, base);
    expect(r).toMatchObject({ ok: true, settings: { siteMedia: false, toolsAutoUpdate: false, toolsChannel: 'stable' } });
    const odd = validateSettingsForm({ ...general, video: { siteMedia: true, toolsAutoUpdate: true, toolsChannel: 'beta' as 'stable' } }, base);
    expect(odd.ok && odd.settings.toolsChannel).toBe('nightly');
    // without the page, the loaded values stay
    const kept = validateSettingsForm(general, { ...base, siteMedia: false, toolsChannel: 'stable' });
    expect(kept).toMatchObject({ ok: true, settings: { siteMedia: false, toolsChannel: 'stable' } });
  });

  it('marks a request cap out of range on the Network page', () => {
    const network = {
      ...networkForm(base),
      hostRules: [{ key: 4, pattern: 'cdn.example.com', connections: '', speedValue: '', speedUnit: 'MB/s' as const, maxRequest: '0.25', maxRequestBytes: 0 }],
    };
    const r = validateSettingsForm({ ...general, network }, base);
    expect(r.ok).toBe(false);
    if (r.ok) return;
    expect(r.errors.hostRuleRows).toEqual({ 4: { maxRequest: 'Enter 1.05 to 1073 MB, or leave empty for no cap' } });
    expect(firstErrorPage(r.errors)).toBe('network');
  });

  it('asks for a video page or a manifest in the Add dialog', () => {
    expect(validateStreamForm({ url: ' ', dir: '', filename: '' })).toEqual({
      ok: false,
      errors: { url: 'Enter the address of a video page, or of an HLS (.m3u8) or DASH (.mpd) manifest' },
    });
    expect(validateStreamForm({ url: ' https://www.youtube.com/watch?v=aqz-KE-bpKQ ', dir: '', filename: '' })).toEqual({
      ok: true,
      url: 'https://www.youtube.com/watch?v=aqz-KE-bpKQ',
      dir: '',
      filename: '',
    });
  });
});

describe('Settings pages of errors (DESIGN-SYSTEM.md §4.9)', () => {
  it('lists the eight pages in the order of the side navigation, About last', () => {
    expect(SETTINGS_PAGES).toEqual(['general', 'downloads', 'network', 'integration', 'video', 'appearance', 'updates', 'about']);
  });

  it('shows each field on its page', () => {
    for (const k of ['maxConcurrent', 'speed', 'defaultDir', 'defaultConnections', 'categories', 'categoryRows'] as const) {
      expect(pageOfError(k)).toBe('downloads');
    }
    for (const k of ['proxy', 'httpProxy', 'httpsProxy', 'socksProxy', 'bypass', 'pacUrl', 'proxyUser', 'hostRuleRows', 'siteLoginRows'] as const) {
      expect(pageOfError(k)).toBe('network');
    }
    expect(pageOfError('avScanCommand')).toBe('integration');
    expect(pageOfError('avScanArgs')).toBe('integration');
    expect(pageOfError('updateFeedUrl')).toBe('updates');
  });

  it('picks the first page with an error, in navigation order', () => {
    expect(firstErrorPage({ updateFeedUrl: 'x', avScanArgs: 'x' })).toBe('integration');
    expect(firstErrorPage({ updateFeedUrl: 'x', proxy: 'x', categoryRows: { 1: { name: 'x' } } })).toBe('downloads');
    expect(firstErrorPage({ speed: 'x' })).toBe('downloads');
    // An absent value is no error.
    expect(firstErrorPage({ speed: undefined })).toBeNull();
  });

  it('keeps the errors of one page', () => {
    const errors = { speed: 'a', httpProxy: 'b', avScanArgs: 'c', updateFeedUrl: 'd' };
    expect(errorsOnPage(errors, 'downloads')).toEqual({ speed: 'a' });
    expect(errorsOnPage(errors, 'network')).toEqual({ httpProxy: 'b' });
    expect(errorsOnPage(errors, 'integration')).toEqual({ avScanArgs: 'c' });
    expect(errorsOnPage(errors, 'updates')).toEqual({ updateFeedUrl: 'd' });
    expect(errorsOnPage(errors, 'general')).toEqual({});
    expect(errorsOnPage(errors, 'appearance')).toEqual({});
  });
});
