# Downex browser extension

The browser half of Downex: it hands the browser's downloads, and the videos of the page you are
on, to the Downex download manager installed on the same computer. It talks only to that program,
through the browser's native messaging (host `com.downex.host`, the `downex-nmhost` relay that the
Downex installer registers). It has no server, sends nothing to the developer and loads no remote
code.

Written in TypeScript with [WXT](https://wxt.dev) as a Manifest V3 extension for Chromium browsers
(Chrome, Edge, Brave) and Firefox. Every file it is built from is in this directory; nothing outside
it is imported.

## Building from source (for add-on review)

The packages are built with:

| Tool    | Version                                                          |
| ------- | ---------------------------------------------------------------- |
| Node.js | 24.x (checked with 24.15.0)                                      |
| pnpm    | 12.14.0, as `packageManager` in `package.json` pins it           |

```sh
corepack enable                   # provides the pnpm version package.json names
pnpm install --frozen-lockfile    # the exact packages of pnpm-lock.yaml; runs `wxt prepare`
DOWNEX_VERSION=<version> pnpm build:firefox
```

The extension is then in `.output/firefox-mv3/`. Set `DOWNEX_VERSION` to the `version` of the
submitted package's `manifest.json` (without it the build carries `package.json`'s version), and the
files are the same as in the package. The build downloads nothing beyond the npm packages of the
lock file; the bundles are minified by Vite, nothing else is generated or obfuscated.

`pnpm test` runs the unit tests (Vitest) and `pnpm check` the type check.

## Builds and packages

| Command                  | Output                                   | For                                         |
| ------------------------ | ---------------------------------------- | ------------------------------------------- |
| `pnpm build`             | `.output/chrome-mv3/`                    | Chromium, unpacked (the installer's copy)   |
| `pnpm build:firefox`     | `.output/firefox-mv3/`                   | Firefox                                     |
| `pnpm build:store`       | `.output/chrome-mv3-store/`              | Chrome Web Store                            |
| `pnpm build:store:edge`  | `.output/edge-mv3-store/`                | Edge Add-ons                                |
| `pnpm zip:store`         | `.output/downex-<v>-chrome-store.zip`    | upload to the Chrome Web Store              |
| `pnpm zip:store:edge`    | `.output/downex-<v>-edge-store.zip`      | upload to Edge Add-ons                      |
| `pnpm zip:firefox`       | `.output/downex-<v>-firefox.zip` and `.output/downex-<v>-sources.zip` | upload to addons.mozilla.org, with its sources |
| `pnpm zip:stores`        | all three                                |                                             |

- **Version.** `DOWNEX_VERSION` (for example `1.4.0` or `v1.4.0`) sets the manifest version, so each
  upload carries the release's (1 to 4 numbers up to 65535). A pre-release (`1.4.0-beta.2`) is accepted
  only by the default unpacked Chromium build, where it becomes `version_name`; the store and Firefox
  builds refuse it, because a store would then refuse the final `1.4.0` as no newer. A version no store
  takes fails the build. Without it, `package.json`'s version is used.
- **Store build** (`--mode store`). The Chrome Web Store does not accept extensions that download
  from YouTube, so this variant leaves YouTube out: no Download button over YouTube's players (the
  `youtube` content script is not built), no YouTube video in the toolbar menu, no look-ahead, no
  context-menu hand-off of YouTube links and no listing of YouTube's streams (`src/lib/store-build.ts`).
  It also has no manifest `key`: a store gives the extension its ID itself. Everything else is the
  same as the default build, which stays as it was for the installer and Firefox.
- **Sources zip.** `zip:firefox` also writes the sources for AMO's review: this directory without
  `node_modules`, `.output`, hidden files, tests and `keys/` (which holds the development signing
  key, `keys/dev-key.pem`; it must never be uploaded anywhere).

## Extension IDs

The unpacked Chromium build's ID is pinned by the manifest `key` in `keys/public-key.txt`
(`pnpm keygen` makes the key pair; the private half stays in `keys/dev-key.pem`, never committed).
`EXTENSION_ID.txt` lists the Chromium IDs Downex accepts, one per line with `#` comments: this
development ID and, once the listings exist, the Chrome Web Store's and Edge Add-ons' IDs. The build
scripts bake them into `downex-nmhost` (`-X main.DefaultChromeExtensionIDs=<id>,<id>`), whose
`register` writes all of them into the host manifests. Firefox's add-on ID is
`extension@downex.app` (`FIREFOX_ID` in `wxt.config.ts`), permanent from the first upload to
addons.mozilla.org; `downex-nmhost` allows only it (`defaultFirefoxIDs` in `cmd/downex-nmhost`; the
earlier `browser@downex.local` is no longer allowed, see `docs/BROWSER-SPEC.md` §1). The Firefox build needs Firefox 140
or later and declares the data it hands to Downex for Firefox's built-in consent (`STORE.md`, AMO).

`STORE.md` has the store listings' privacy policy draft and permission justifications.
