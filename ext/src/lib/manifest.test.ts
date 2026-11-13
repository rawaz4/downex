import { existsSync, readdirSync } from 'node:fs';
import { matchesGlob } from 'node:path';
import { afterEach, describe, expect, it, vi } from 'vitest';
import config, { dropStoreEntrypoints, FIREFOX_ID, FIREFOX_OLD_ID, HOST_PERMISSIONS, releaseVersion, STORE_MODE } from '../../wxt.config';
import content from '../entrypoints/content';
import videoUi from '../entrypoints/video-ui';
import video from '../entrypoints/video.content';
import youtube from '../entrypoints/youtube.content';
import { VIDEO_UI_SCRIPT } from './offer-types';

type ManifestFn = (env: { browser: string; manifestVersion: 3; mode: string; command: string }) => Record<string, unknown>;

function manifest(browser: string, mode = 'production'): Record<string, unknown> {
  const fn = config.manifest as unknown as ManifestFn;
  return fn({ browser, manifestVersion: 3, mode, command: 'build' });
}

/** The entrypoints WXT finds in src/entrypoints, by its names (`youtube.content.ts` → `youtube`). */
function entrypointNames(): string[] {
  return readdirSync(new URL('../entrypoints/', import.meta.url))
    .filter((f) => !f.endsWith('.test.ts'))
    .map((f) => f.replace(/(.content)?.ts$/, ''));
}

afterEach(() => {
  vi.unstubAllEnvs();
});

describe('manifest', () => {
  it.each(['chrome', 'edge', 'firefox'])('%s: asks for http(s) hosts only', (browser) => {
    const m = manifest(browser);
    expect(HOST_PERMISSIONS).toEqual(['http://*/*', 'https://*/*']);
    expect(m.host_permissions).toEqual(HOST_PERMISSIONS);
    expect(JSON.stringify(m)).not.toContain('<all_urls>');
    expect(m.incognito).toBe('not_allowed');
  });

  it.each(['chrome', 'firefox'])('%s: observes navigations (media lists) without blocking them', (browser) => {
    const perms = manifest(browser).permissions as string[];
    expect(perms).toContain('webNavigation');
    expect(perms).toContain('webRequest');
    expect(perms.includes('webRequestBlocking')).toBe(browser === 'firefox');
  });

  it('firefox: has its permanent add-on ID and declares the data it hands to Downex', () => {
    expect(FIREFOX_ID).toBe('extension@downex.app');
    expect(FIREFOX_OLD_ID).toBe('browser@downex.local');
    for (const mode of ['production', STORE_MODE]) {
      expect(manifest('firefox', mode).browser_specific_settings).toEqual({
        gecko: {
          id: 'extension@downex.app',
          // the first Firefox with the built-in data consent
          strict_min_version: '140.0',
          // Mozilla's names (Firefox built-in data consent); none optional, so never 'none'
          data_collection_permissions: {
            required: ['browsingActivity', 'websiteContent', 'websiteActivity', 'authenticationInfo'],
          },
        },
      });
    }
    for (const browser of ['chrome', 'edge']) expect(manifest(browser)).not.toHaveProperty('browser_specific_settings');
  });

  it('runs the click reporter on http(s) pages only', () => {
    expect(content.matches).toEqual(['http://*/*', 'https://*/*']);
    expect(content.allFrames).toBe(true);
  });

  it('runs the video button in the YouTube frames only, in the isolated world, without new permissions', () => {
    expect(youtube.matches).toEqual([
      'https://www.youtube.com/*',
      'https://m.youtube.com/*',
      'https://music.youtube.com/*',
      'https://www.youtube-nocookie.com/embed/*',
    ]);
    expect(youtube.excludeMatches).toEqual(['*://www.youtube.com/live_chat*']);
    expect(youtube.allFrames).toBe(true);
    expect(youtube.runAt).toBe('document_idle');
    expect(youtube.world ?? 'ISOLATED').toBe('ISOLATED');
    // Its CSS is inlined into a closed shadow root: no web-accessible resource reveals the extension.
    expect(youtube.cssInjectionMode).toBe('manual');
    for (const browser of ['chrome', 'firefox']) {
      const m = manifest(browser);
      expect(m.permissions).toEqual(
        browser === 'firefox'
          ? ['downloads', 'nativeMessaging', 'webRequest', 'webNavigation', 'cookies', 'contextMenus', 'storage', 'scripting', 'webRequestBlocking']
          : ['downloads', 'nativeMessaging', 'webRequest', 'webNavigation', 'cookies', 'contextMenus', 'storage', 'scripting'],
      );
      expect(m).not.toHaveProperty('web_accessible_resources');
      // Every page the button runs on is already a host permission.
      for (const pattern of (youtube.matches as string[] | undefined) ?? []) expect(pattern.startsWith('https://')).toBe(true);
    }
  });

  it('runs the video tracker in every http(s) frame and its about:blank children, but not on YouTube’s hosts', () => {
    expect(video.matches).toEqual(['http://*/*', 'https://*/*']);
    expect(video.allFrames).toBe(true);
    expect(video.matchAboutBlank).toBe(true);
    expect(video.runAt).toBe('document_idle');
    // youtube.content.ts has its own button there: never a second one.
    expect(video.excludeMatches).toEqual(['*://www.youtube.com/*', '*://m.youtube.com/*', '*://music.youtube.com/*', '*://www.youtube-nocookie.com/*']);
    for (const pattern of (youtube.matches as string[] | undefined) ?? []) {
      const host = new URL(pattern.replace('*', 'x')).host;
      expect((video.excludeMatches as string[]).some((e) => e === `*://${host}/*`)).toBe(true);
    }
    expect(video.world ?? 'ISOLATED').toBe('ISOLATED');
    expect(video.cssInjectionMode).toBe('manual');
    expect(video.matchOriginAsFallback ?? false).toBe(false);
    for (const browser of ['chrome', 'firefox']) {
      const m = manifest(browser);
      expect(m).not.toHaveProperty('web_accessible_resources');
      expect(JSON.stringify(m)).not.toContain('video-ui');
      for (const pattern of (video.matches as string[] | undefined) ?? []) expect(HOST_PERMISSIONS).toContain(pattern);
    }
  });

  it('builds the overlay UI as an unlisted script /video-ui.js that the background injects', () => {
    // WXT builds `entrypoints/<name>.ts` (neither a content script nor a page) to `/<name>.js`.
    expect(VIDEO_UI_SCRIPT).toBe('/video-ui.js');
    const dir = new URL('../entrypoints/', import.meta.url);
    expect(existsSync(new URL('video-ui.ts', dir))).toBe(true);
    expect(readdirSync(dir).filter((f) => f.startsWith('video-ui'))).toEqual(['video-ui.ts']);
    expect(typeof videoUi.main).toBe('function');
    expect(videoUi).not.toHaveProperty('matches');
  });
});

describe('store build (wxt build --mode store)', () => {
  it('leaves out the YouTube content script, and only in the store mode', () => {
    expect(STORE_MODE).toBe('store');
    const all = entrypointNames();
    expect(all).toContain('youtube');
    const full = all.map((name) => ({ name }));
    dropStoreEntrypoints('production', full);
    expect(full.map((e) => e.name)).toEqual(all);
    const reduced = all.map((name) => ({ name }));
    dropStoreEntrypoints(STORE_MODE, reduced);
    expect(reduced.map((e) => e.name)).toEqual(all.filter((n) => n !== 'youtube'));
    // The build calls it from WXT's entrypoints:found hook with WXT's mode.
    const hooks = config.hooks as unknown as Record<string, unknown>;
    const hook = hooks['entrypoints:found'] as (wxt: { config: { mode: string } }, infos: { name: string }[]) => void;
    const hooked = all.map((name) => ({ name }));
    hook({ config: { mode: STORE_MODE } }, hooked);
    expect(hooked).toEqual(reduced);
  });

  it('has no YouTube matches: the content scripts left run nowhere on YouTube', () => {
    // youtube.content.ts is gone; the click reporter and the video tracker are what remain (a new
    // content script must be added here, and so be looked at for the store build).
    const scripts = readdirSync(new URL('../entrypoints/', import.meta.url)).filter((f) => f === 'content.ts' || f.endsWith('.content.ts'));
    expect(scripts.sort()).toEqual(['content.ts', 'video.content.ts', 'youtube.content.ts']);
    for (const script of [content, video]) {
      for (const pattern of (script.matches as string[] | undefined) ?? []) expect(pattern).not.toMatch(/youtube/i);
    }
    // The video tracker keeps YouTube's hosts excluded even without youtube.content.ts there.
    expect(video.excludeMatches).toEqual(['*://www.youtube.com/*', '*://m.youtube.com/*', '*://music.youtube.com/*', '*://www.youtube-nocookie.com/*']);
    for (const browser of ['chrome', 'edge', 'firefox']) {
      const m = manifest(browser, STORE_MODE);
      expect(JSON.stringify(m)).not.toMatch(/youtube/i);
      expect(m).not.toHaveProperty('content_scripts');
      expect(m.host_permissions).toEqual(HOST_PERMISSIONS);
    }
  });

  it('carries no manifest "key": a store gives the ID itself', () => {
    for (const browser of ['chrome', 'edge']) {
      expect(manifest(browser)).toHaveProperty('key');
      const m = manifest(browser, STORE_MODE);
      expect(m).not.toHaveProperty('key');
      expect(m.minimum_chrome_version).toBe('116');
    }
  });
});

describe('release version (DOWNEX_VERSION)', () => {
  const dev = { store: false, firefox: false };
  const targets = [dev, { store: true, firefox: false }, { store: false, firefox: true }, { store: true, firefox: true }];

  it('keeps package.json’s version without one', () => {
    for (const target of targets) {
      expect(releaseVersion(undefined, target)).toBeUndefined();
      expect(releaseVersion('  ', target)).toBeUndefined();
    }
    expect(manifest('chrome')).not.toHaveProperty('version');
  });

  it('takes a release version for every build', () => {
    for (const target of targets) {
      expect(releaseVersion('1.4.0', target)).toEqual({ version: '1.4.0' });
      expect(releaseVersion('v2.0', target)).toEqual({ version: '2.0' });
      expect(releaseVersion('65535.0.0.1', target)).toEqual({ version: '65535.0.0.1' });
    }
    vi.stubEnv('DOWNEX_VERSION', 'v1.4.0');
    for (const [browser, mode] of [
      ['chrome', 'production'],
      ['chrome', STORE_MODE],
      ['edge', STORE_MODE],
      ['firefox', 'production'],
    ] as const) {
      const m = manifest(browser, mode);
      expect(m.version, `${browser} ${mode}`).toBe('1.4.0');
      expect(m, `${browser} ${mode}`).not.toHaveProperty('version_name');
    }
  });

  it('takes a pre-release only for the default Chromium build: its numeric part, the whole tag as its name', () => {
    expect(releaseVersion('1.4.0-beta.2', dev)).toEqual({ version: '1.4.0', version_name: '1.4.0-beta.2' });
    expect(releaseVersion('v1.4.0+5', dev)).toEqual({ version: '1.4.0', version_name: '1.4.0+5' });
    vi.stubEnv('DOWNEX_VERSION', '1.4.0-beta.2');
    for (const browser of ['chrome', 'edge']) {
      expect(manifest(browser)).toMatchObject({ version: '1.4.0', version_name: '1.4.0-beta.2' });
    }
  });

  it('fails the store and Firefox builds for a pre-release: it would share its final release’s version', () => {
    for (const tag of ['1.4.0-beta.2', 'v1.4.0-rc1', '1.4.0+5', '1.4.0.']) {
      for (const target of targets.slice(1)) {
        expect(() => releaseVersion(tag, target), `${tag} ${JSON.stringify(target)}`).toThrow(
          /DOWNEX_VERSION .*release version only, such as 1\.4\.0/,
        );
      }
    }
    expect(() => releaseVersion('1.4.0-beta.2', { store: true, firefox: false })).toThrow(/the store build/);
    expect(() => releaseVersion('1.4.0-beta.2', { store: false, firefox: true })).toThrow(/the Firefox build/);
    vi.stubEnv('DOWNEX_VERSION', '1.4.0-beta.2');
    for (const [browser, mode] of [
      ['chrome', STORE_MODE],
      ['edge', STORE_MODE],
      ['firefox', 'production'],
      ['firefox', STORE_MODE],
    ] as const) {
      expect(() => manifest(browser, mode), `${browser} ${mode}`).toThrow(/DOWNEX_VERSION "1\.4\.0-beta\.2"/);
    }
  });

  it('fails the build for a version no store takes', () => {
    for (const bad of ['beta', '1.2.3.4.5', '01.2', '1.65536', '-1', 'v']) {
      for (const target of targets) {
        expect(() => releaseVersion(bad, target), bad).toThrow(/DOWNEX_VERSION/);
      }
    }
  });
});

describe('sources zip (AMO)', () => {
  it('never contains keys/: the development signing key lives there', () => {
    const excludes = config.zip?.excludeSources ?? [];
    expect(excludes).toContain('keys/**');
    // Every file there now, and the private key that `pnpm keygen` writes.
    const files = [...readdirSync(new URL('../../keys/', import.meta.url)), 'dev-key.pem'].map((f) => `keys/${f}`);
    for (const f of files) expect(excludes.some((g) => matchesGlob(f, g)), f).toBe(true);
    // The sources build without keys/: Firefox's manifest never reads the Chromium key.
    expect(manifest('firefox')).not.toHaveProperty('key');
  });
});
