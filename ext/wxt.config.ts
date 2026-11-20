import { readFileSync } from 'node:fs';
import { defineConfig } from 'wxt';

/**
 * The store build's mode (`wxt build --mode store`, `pnpm build:store`): the variant for the
 * Chrome Web Store and Edge Add-ons, whose policies refuse YouTube downloaders. It leaves out
 * STORE_EXCLUDED_ENTRYPOINTS and the manifest "key", and src/lib/store-build.ts leaves YouTube
 * out of everything else. The default build (installer, Firefox) is unchanged.
 */
export const STORE_MODE = 'store';

/** Entrypoints the store build leaves out: the Download button over YouTube's players. */
export const STORE_EXCLUDED_ENTRYPOINTS: readonly string[] = ['youtube'];

/** Removes, in the store mode, the entrypoints it leaves out (WXT's `entrypoints:found` list). */
export function dropStoreEntrypoints(mode: string, infos: { name: string }[]): void {
  if (mode !== STORE_MODE) return;
  for (let i = infos.length - 1; i >= 0; i--) {
    if (STORE_EXCLUDED_ENTRYPOINTS.includes(infos[i]?.name ?? '')) infos.splice(i, 1);
  }
}

/** Chromium and Firefox take 1 to 4 dot-separated numbers of at most 65535, without leading zeros. */
const MANIFEST_VERSION = /^(0|[1-9][0-9]{0,4})(\.(0|[1-9][0-9]{0,4})){0,3}$/;

/**
 * The manifest version of a release, from DOWNEX_VERSION (the build scripts' version, such as
 * "1.4.0" or "v1.4.0-beta.2"): each store upload must carry a higher version than the last.
 * A pre-release keeps its numeric part as `version` and the whole tag as `version_name`
 * (Chromium shows it; WXT leaves it out for Firefox). Undefined without DOWNEX_VERSION, so
 * package.json's version stays. Throws for a version no store would take, failing the build.
 *
 * A pre-release (or any tag beyond the numbers, such as "+5") is refused for the store builds
 * and for Firefox: "1.4.0-beta.2" and its final "1.4.0" would both be version 1.4.0, which a
 * store refuses on the later upload and an installed copy never updates to. Only the default
 * Chromium build (unpacked, never uploaded) takes one, since its version_name tells them apart.
 */
export function releaseVersion(
  raw: string | undefined,
  target: { store: boolean; firefox: boolean },
): { version: string; version_name?: string } | undefined {
  if (!raw?.trim()) return undefined;
  const tag = raw.trim().replace(/^v/, '');
  const version = /^[0-9.]+/.exec(tag)?.[0].replace(/\.$/, '') ?? '';
  if (!MANIFEST_VERSION.test(version) || version.split('.').some((n) => Number(n) > 65535)) {
    throw new Error(`DOWNEX_VERSION ${JSON.stringify(raw)}: want 1 to 4 dot-separated numbers up to 65535, such as 1.4.0`);
  }
  if (tag === version) return { version };
  if (target.store || target.firefox) {
    const build = target.store ? 'the store build' : 'the Firefox build';
    throw new Error(
      `DOWNEX_VERSION ${JSON.stringify(raw)}: ${build} takes a release version only, such as ${version}: ` +
        `a pre-release would carry the same manifest version (${version}) as its final release, ` +
        'which the stores refuse and installed copies never update to',
    );
  }
  return { version, version_name: tag };
}

const PERMISSIONS = [
  'downloads',
  'nativeMessaging',
  'webRequest',
  // Top-level navigations clear a tab's detected streams (MEDIA-SPEC §6.1).
  'webNavigation',
  'cookies',
  'contextMenus',
  'storage',
  'scripting',
];

export const HOST_PERMISSIONS = ['http://*/*', 'https://*/*'];

/** Permissions the extension asks for at the moment a feature needs them. */
export const OPTIONAL_PERMISSIONS = ['notifications'];

/**
 * The Firefox add-on ID (gecko id), permanent from the first upload to addons.mozilla.org: AMO
 * ties the listing and every installed copy's updates to it. downex-nmhost allows it, and only it,
 * in the host manifest's allowed_extensions (the earlier browser@downex.local is no longer
 * allowed: Downex does not own that domain, BROWSER-SPEC §1).
 */
export const FIREFOX_ID = 'extension@downex.app';

/**
 * Firefox's built-in data consent (Firefox 140 and later; extensionworkshop.com, "Firefox
 * built-in data consent", and the Add-on Policies, which apply to data sent to a native
 * application too). Everything here goes only to the Downex program on this computer, but
 * through native messaging, so it is declared, and the user accepts it when installing:
 * - browsingActivity: the address and title of the page a download or video comes from, and,
 *   with "Prepare video qualities while you watch" (on unless turned off), the address of a
 *   video page the user stays on, sent in the background. Background transmission needs
 *   explicit consent: required types are that consent, given in Firefox's install prompt;
 *   the option still turns the look-ahead off. An optional type for the look-ahead alone would
 *   add nothing: the consent is per type, and downloads need the page's address anyway
 *   (Referer, the video's page).
 * - websiteContent: the request headers of a download, cookies included, its form body, and
 *   the cookies of the one address being downloaded.
 * - websiteActivity: the downloads the user starts, handed over instead of the browser's.
 * - authenticationInfo: an Authorization header a download was sent with (a user name and
 *   password for HTTP Basic sign-in).
 * The extension cannot work without them, so none is optional. Not a data type: the hello
 * message on each connection names the browser and this extension's version for Downex to
 * choose the protocol (protocol negotiation inside the required function; ext/STORE.md says
 * so to the reviewer).
 */
export const FIREFOX_DATA_COLLECTION = {
  required: ['browsingActivity', 'websiteContent', 'websiteActivity', 'authenticationInfo'],
};

/**
 * The oldest Firefox the add-on installs on: 140, the first with the built-in data consent
 * above, so no Firefox installs it without asking (an older one would need a consent page of
 * the add-on's own).
 */
export const FIREFOX_MIN_VERSION = '140.0';

export default defineConfig({
  srcDir: 'src',
  manifestVersion: 3,
  manifest: ({ browser, mode }) => {
    const firefox = browser === 'firefox';
    const store = mode === STORE_MODE;
    return {
      name: 'Downex',
      description: 'Hands downloads to the Downex download manager.',
      ...releaseVersion(process.env.DOWNEX_VERSION, { store, firefox }),
      permissions: firefox ? [...PERMISSIONS, 'webRequestBlocking'] : PERMISSIONS,
      // Asked for only when the user turns on "Offer to finish failed downloads with a
      // notification" (BROWSER-SPEC §6): an update adds no new warning.
      optional_permissions: OPTIONAL_PERMISSIONS,
      // Everything the extension touches is http(s): webRequest observation and blocking,
      // cookies.getAll for one URL, scripting on http(s) pages, the active tab's URL.
      host_permissions: HOST_PERMISSIONS,
      // Private-window downloads are never captured (BROWSER-SPEC §3); keeping the
      // extension out of private windows also keeps their requests and cookies out of it.
      incognito: 'not_allowed',
      action: { default_title: 'Downex' },
      ...(firefox
        ? {
            browser_specific_settings: {
              gecko: {
                id: FIREFOX_ID,
                strict_min_version: FIREFOX_MIN_VERSION,
                // Data goes only to the local native host, never to the developer; Mozilla's
                // policies count that as transmission all the same.
                data_collection_permissions: FIREFOX_DATA_COLLECTION,
              },
            },
          }
        : {
            // BROWSER-SPEC §1: an unpacked Chromium build's ID is pinned by the manifest "key"
            // (base64 DER SubjectPublicKeyInfo, generated by `pnpm keygen`). A store gives the
            // ID itself and takes no "key", so the store build has none. Read only here, so
            // Firefox's sources build without keys/ (left out of the sources zip below).
            ...(store ? {} : { key: readFileSync(new URL('./keys/public-key.txt', import.meta.url), 'utf8').trim() }),
            minimum_chrome_version: '116',
          }),
    };
  },
  hooks: {
    'entrypoints:found': (wxt, infos) => dropStoreEntrypoints(wxt.config.mode, infos),
  },
  zip: {
    // downex-1.4.0-chrome-store.zip, downex-1.4.0-edge-store.zip, downex-1.4.0-firefox.zip and
    // downex-1.4.0-sources.zip (Firefox's, for AMO's source review).
    name: 'downex',
    artifactTemplate: '{{name}}-{{versionName}}-{{browser}}{{modeSuffix}}.zip',
    sourcesTemplate: '{{name}}-{{versionName}}-sources{{modeSuffix}}.zip',
    // keys/ holds the development signing key (dev-key.pem, gitignored but on disk): it must
    // never reach a store. The sources build without it (the "key" is read for Chromium only).
    excludeSources: ['keys/**'],
  },
});
