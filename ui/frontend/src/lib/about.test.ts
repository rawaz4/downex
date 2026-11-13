import { describe, expect, it } from 'vitest';
import { buildLines, introBlocks, osLabel, parseNotices, scrubDiagnostics, updatesState, versionsDiffer } from './about';

describe('Settings → About: what this copy is', () => {
  it('names the system as people know it', () => {
    expect(osLabel({ os: 'windows', arch: 'amd64' })).toBe('Windows x64');
    expect(osLabel({ os: 'windows', arch: 'arm64' })).toBe('Windows on Arm');
    expect(osLabel({ os: 'darwin', arch: 'arm64' })).toBe('macOS (Apple silicon)');
    expect(osLabel({ os: 'darwin', arch: 'amd64' })).toBe('macOS (Intel)');
    expect(osLabel({ os: 'linux', arch: 'amd64' })).toBe('Linux x64');
    expect(osLabel({ os: 'plan9', arch: 'mips' })).toBe('plan9/mips');
  });

  it('lists the build: Go, Wails, and the commit only when there is one', () => {
    const build = { version: '0.8.2', os: 'windows', arch: 'amd64', goVersion: 'go1.27.0', wailsVersion: 'v2.17.0', commit: '', commitTime: 0, modified: false };
    expect(buildLines(build)).toEqual(['Go 1.27.0', 'Wails 2.17.0']);
    expect(buildLines({ ...build, wailsVersion: '' })).toEqual(['Go 1.27.0']);
    expect(buildLines({ ...build, commit: '0123456789abcdef0123', modified: true })).toEqual([
      'Go 1.27.0',
      'Wails 2.17.0',
      'Commit 0123456789ab (with uncommitted changes)',
    ]);
  });

  it('warns when the window and downexd are different versions', () => {
    expect(versionsDiffer('0.8.2', '0.8.1')).toBe(true);
    expect(versionsDiffer('0.8.2', '0.8.2')).toBe(false);
    expect(versionsDiffer('0.8.2', 'v0.8.2')).toBe(false);
    // nothing to compare with
    expect(versionsDiffer('', '0.8.1')).toBe(false);
    expect(versionsDiffer('0.8.2', '')).toBe(false);
  });

  it('says whether updates can work, and why not', () => {
    expect(updatesState(null, '', true)).toEqual({ on: false, text: 'Not known while downexd is not reachable' });
    expect(updatesState({ updatesSupported: false }, 'https://u.example/update.json', true)).toEqual({ on: false, text: 'Off in this build (no signing key)' });
    // an older downexd does not say: the address decides
    expect(updatesState({}, '', true)).toEqual({ on: false, text: 'Off (no update address)' });
    expect(updatesState({ updatesSupported: true }, '', true)).toEqual({ on: false, text: 'Off (no update address)' });
    expect(updatesState({ updatesSupported: true }, 'https://u.example/update.json', true)).toEqual({ on: true, text: 'On · checked every day' });
    expect(updatesState({ updatesSupported: true }, 'https://u.example/update.json', false)).toEqual({ on: true, text: 'On · checked when you ask' });
  });
});

describe('Settings → About: diagnostics', () => {
  it('never copies what the page knows must stay private, even if the host let it through', () => {
    const text = 'downexd 0.8.2\nEndpoint: \\\\.\\pipe\\downex-9f3a\nUpdate: https://u.example/update.json\nOK\n';
    const out = scrubDiagnostics(text, ['\\\\.\\pipe\\downex-9f3a', 'https://u.example/update.json', '']);
    expect(out).not.toContain('downex-9f3a');
    expect(out).not.toContain('u.example');
    expect(out).toContain('Endpoint: (hidden)');
    expect(out).toContain('downexd 0.8.2');
  });
});

describe('Settings → About: open-source notices', () => {
  const NOTICES = [
    'THIRD-PARTY NOTICES',
    '',
    'Downex is built with software that other people wrote.',
    '',
    '==============================================================================',
    'The Go Programming Language (standard library and runtime, go1.27.0)',
    '==============================================================================',
    '--- LICENSE ---',
    'Copyright 2009 The Go Authors.',
    '',
    '==============================================================================',
    'github.com/wailsapp/wails/v2 v2.17.0',
    '==============================================================================',
    'MIT License',
    '',
  ].join('\n');

  it('splits the file into its introduction and one section per component', () => {
    const n = parseNotices(NOTICES);
    expect(n.intro).toBe('THIRD-PARTY NOTICES\n\nDownex is built with software that other people wrote.');
    expect(n.sections.map((s) => s.title)).toEqual([
      'The Go Programming Language (standard library and runtime, go1.27.0)',
      'github.com/wailsapp/wails/v2 v2.17.0',
    ]);
    expect(n.sections[0]?.body).toBe('--- LICENSE ---\nCopyright 2009 The Go Authors.');
    expect(n.sections[1]?.body).toBe('MIT License');
  });

  it('reflows the introduction into headings, paragraphs and list items (without the file’s own title)', () => {
    const intro = [
      'THIRD-PARTY NOTICES',
      '',
      'Downex is built with software that other',
      'people wrote.',
      '',
      'Programs Downex downloads separately',
      '------------------------------------',
      'Downex does not include these programs.',
      '',
      '- yt-dlp (https://github.com/yt-dlp/yt-dlp): released into the public domain under',
      '  The Unlicense.',
      '- Deno: MIT License.',
    ].join('\n');
    expect(introBlocks(intro)).toEqual([
      { kind: 'para', text: 'Downex is built with software that other people wrote.' },
      { kind: 'heading', text: 'Programs Downex downloads separately' },
      { kind: 'para', text: 'Downex does not include these programs.' },
      { kind: 'item', text: 'yt-dlp (https://github.com/yt-dlp/yt-dlp): released into the public domain under The Unlicense.' },
      { kind: 'item', text: 'Deno: MIT License.' },
    ]);
    expect(introBlocks('')).toEqual([]);
  });

  it('keeps a file without sections whole, and reads Windows line ends', () => {
    expect(parseNotices('Just text\r\nmore\r\n')).toEqual({ intro: 'Just text\nmore', sections: [] });
  });
});
