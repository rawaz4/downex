# Browser integration specification (M3, media detection M5)

One TypeScript (WXT) Manifest V3 extension for Chrome, Edge and Firefox hands downloads to
`downexd` through the native-messaging relay `downex-nmhost`. No kernel drivers, no injection,
no local network ports.

Status: M3 and M5 as `REQUIREMENTS.md` says. The video-detection additions (`VIDEO-DETECT-SPEC.md`:
`probeStream`, `addMedia` tracks, `added.awaiting`, the `streams` feature, file detection, the
Download button over every video, `hiddenSites`) were implemented on 2026-10-23. They are verified
with unit tests, fake-browser wiring tests and real-process relay tests (`VIDEO-DETECT-SPEC.md` §10),
and have **not yet run in a real browser**.

## 1. Identity
- Native host name: `com.downex.host`.
- Chromium extension ID is pinned with a manifest `key` (public key committed in `ext/`), so
  `allowed_origins` = `chrome-extension://<id>/` is stable for unpacked/dev builds.
  - **Release builds keep this ID (amendment 2026-11-20).** There is no Chrome Web Store listing,
    and the installer and the disk image ship the same unpacked `chrome-mv3` copy, built with this
    `key`, that Chrome, Brave and Chromium users load: its ID is the release ID. Leaving it out of
    `-Release`/`-r` builds would cut every one of those installations off. Nor would a second key
    make it harder to claim: an unpacked copy's ID follows from the public key in its own manifest,
    which ships with it, so any unpacked extension can take any such ID; only a store-installed
    extension's ID cannot be chosen, and loading an unpacked one needs Developer mode in that
    profile. The build scripts therefore build the IDs of `ext/EXTENSION_ID.txt` into release and
    development builds alike and refuse none. When a store listing replaces the unpacked copy,
    its ID goes on a line of its own in `EXTENSION_ID.txt`, and the development line can then be
    dropped from the release (the installer would ship the store link instead of the folder).
- Firefox add-on ID (gecko id): `extension@downex.app`, permanent from the first addons.mozilla.org
  upload, and the only one in `downex-nmhost`'s `allowed_extensions`. **Amendment 2026-11-20:**
  `browser@downex.local`, the ID of copies loaded by hand before the listing, is no longer allowed:
  `downex.local` is not a domain Downex owns, so any add-on could declare that ID. Every released
  build has `extension@downex.app`. `register` writes manifests without it, and `register --keep`
  (macOS) drops it from a manifest it otherwise keeps; only `--firefox-extension-id
  browser@downex.local` writes it again. The Firefox build needs Firefox 140 or later and declares
  `data_collection_permissions.required` = `browsingActivity`, `websiteContent`, `websiteActivity`,
  `authenticationInfo`.
- `downex-nmhost register --system` (all users) refuses a host whose executable, `downexd` next to
  it, or any directory above them anyone but administrators (Windows: SYSTEM, Administrators,
  TrustedInstaller; Linux: root, not group/other-writable) could replace; `--allow-user-writable`
  overrides that. On Windows the manifests live in `%ProgramData%\Downex\nmhost`, created (or
  given again) a protected DACL: full control for SYSTEM and Administrators, read for Users. A
  directory, manifest or backup there that someone else owns, a link/junction, or a hard-linked
  file is refused.

## 2. Native messaging protocol (extension ↔ downex-nmhost)
Framing per the browser spec: 32-bit length in native byte order + UTF-8 JSON. Host → browser
messages ≤ 1 MB; the host rejects browser → host messages larger than 2 MiB.

Every request carries a client-chosen `id` (string); every response echoes it.

| Request `type` | Fields | Response |
|---|---|---|
| `hello` | `protocol: 1`, `browser: "chrome"\|"edge"\|"firefox"\|"other"`, `extVersion` | `{type:"welcome", protocol:1, daemonVersion, features}` (`features: ["video", "prefetch", "streams", "renew", "rescue", "clips"]`: `video` since site media, `prefetch` since the look-ahead, `streams` since video detection on every site, `renew` since expired links (DESKTOP-SPEC §10), `rescue` since the rescue of failed browser downloads (§6), `clips` since parts of a video (MEDIA-SPEC §8): `addMedia`/`addVideo` take `clipStartMs`/`clipEndMs` and `videoInfo` carries `chapters`; a client sends the part fields only to a relay that lists `clips`; `items` since pages with several videos (SITE-MEDIA-SPEC §3.12, amendment 2026-11-18): `probeVideo`/`addVideo` take `entryId` and `videoInfo` carries `entryId`, `items` and `itemsLeftOut`; a client sends `entryId` only to a relay that lists `items`; `signIn` since the automatic sign-in (SITE-MEDIA-SPEC §6.3, amendment 2026-11-18): `probeVideo`/`addVideo` take `cookieDomain`; a client sends it only to a relay that lists `signIn`) |
| `add` | `url`, `method` (`GET`/`POST`), `headers: [[name, value], …]`, `body?` (base64, ≤ 1 MiB, POST only), `filename?`, `referrer?`, `mime?`, `size?`, `startPaused?`, `deadlineMs?`, `pageUrl?` | `{type:"added", downloadId, awaiting, renewCheck}` |
| `addMedia` | `manifestUrl`, `headers: [[name, value], …]`, `pageUrl?`, `title?` (≤ 512 chars), `videoTrack?`, `audioTrack?`, `clipStartMs?`, `clipEndMs?` (integers, MEDIA-SPEC §8; an end needs a start), `deadlineMs?` | `{type:"added", downloadId, awaiting}`; errors `rejected` with message code `encrypted` / `live` when the stream cannot be downloaded |
| `probeStream` | `manifestUrl`, `headers?`, `pageUrl?`, `deadlineMs?` | `{type:"videoInfo", site:"", title, durationMs, format:"hls"\|"dash", container:"mp4", selectedVideo, selectedAudio, tracks:[…]}` (as `probeVideo`'s; never a URL) |
| `probeVideo` | `pageUrl`, `headers?` (only `Cookie`, `Referer` used), `prefetch?` (boolean: the look-ahead of §4), `entryId?` (one video of a page with several, ≤ 256 bytes of printable text; SITE-MEDIA-SPEC §3.12), `cookieDomain?` (the domain of the `Cookie`'s sign-in: a lowercase DNS name ≤ 253 bytes, else `invalid_request`; SITE-MEDIA-SPEC §6.3), `deadlineMs?` | `{type:"videoInfo", site, title, durationMs, format, container:"mp4", selectedVideo, selectedAudio, tracks:[{id, kind, ext, codecs, width, height, frameRate, bandwidth, size, sizeApprox, language, name}]}` (≤ 200 tracks, ≤ 256 KiB, never format URLs) |
| `addVideo` | `pageUrl`, `headers?`, `videoTrack?`, `audioTrack?`, `title?`, `clipStartMs?`, `clipEndMs?`, `entryId?`, `cookieDomain?`, `deadlineMs?` | `{type:"added", downloadId, awaiting}` |
| `rescue` | `url`, `headers: [[name, value], …]`, `target` (absolute path ≤ 32 KiB, no NUL), `bytesReceived` (integer 0..2^53−1), `totalBytes?` (−1 or ≥ 0), `startTime?` (Unix ms ≥ 0), `etag?`, `lastModified?` (≤ 1,024 printable characters), `filename?`, `startPaused` (boolean, required), `pageUrl?`, `deadlineMs?` | `{type:"rescued", downloadId, keptBytes}` (§6) |
| `resume` | `downloadId` (positive integer) | `{type:"resumed", downloadId}`: the first start of a rescue stored paused, nothing else (§6) |
| `status` | — | `{type:"status", running, active, queued, polite?, politeRate?}`; *Polite mode (POLITE-SPEC §8):* `polite` is "off", "idle", "watching", "slowed", "measuring", "no_signal" or "unavailable" (absent for an older daemon), `politeRate` the cap in bytes/s while slowed |
| `ping` | — | `{type:"pong"}` |
| `fetchTake` | `tickets` (1-64 strings of 32 lowercase hex), `waitMs?` (0-25000, default 20000) | `{type:"fetchTask", fetch?:{ticket, fetchId, url, start, end, total}, ended:[tickets]}` (SITE-MEDIA-SPEC §2.8) |
| `fetchPut` | `ticket`, `fetchId`, `seq` (integer ≥ 0), `head?:{status, contentType?, contentRange?, contentLength?}`, `data?` (base64, ≤ 256 KiB decoded), `end?`, `error?` (≤ 256 bytes) | `{type:"fetchAck", stop}` (SITE-MEDIA-SPEC §2.8) |

Errors: `{id, ok:false, error:{code, message, reason?}}` with codes `invalid_request`,
`daemon_unavailable`, `rejected`, `internal`; `reason` is the daemon's `Downex-Error-Code` (`[a-z_]{1,64}`)
whenever it sent one, for every request type, and the message keeps its `"<code>: "` prefix. Successful
responses carry `ok:true`.
The relay validates every field (same limits as DAEMON-SPEC §4), autostarts `downexd` when needed
(≤ 3 s), maps `add` to `DownloadService.Add`, and never logs cookie/authorization values or bodies.
- `hello` is answered within 1 s, never failing because of the daemon: it starts `downexd` in the
  background when none runs, and `daemonVersion` is `""` until the daemon answers.
- `add` is bounded by 15 s. `deadlineMs` (positive integer, optional) is how long the extension
  waits for the answer, timed from when it sends the request: the relay finishes autostart and
  `Add` 200 ms before it and never sends `DownloadService.Add` with less than 150 ms left, answering
  `daemon_unavailable` instead, so a download the browser kept is not also added to Downex.
- The relay sends `add` with `source = ADD_SOURCE_BROWSER`, `size` as `expected_size` (-1 → unknown)
  and `mime` as the MIME hint; with `Settings.confirm_browser_downloads` the daemon then holds the
  download for confirmation (DESKTOP-SPEC §1). `added.awaiting` (for `add` and `addMedia` too) is true
  when it is held; the extension reads a missing `awaiting` (an older relay) as unknown.
- Expired links (DESKTOP-SPEC §10): `add.pageUrl` is the page the link was found on (http or https,
  no user name, ≤ 32 KiB, validated like `referrer`; anything else is `invalid_request`), forwarded as
  `AddRequest.page_url` and logged as its host only. `added.renewCheck` is true when downexd checks
  first whether the link continues one of its downloads whose link expired (the add is held up to
  15 s; the extension never learns the outcome). Both are additive (protocol 1): an older relay
  ignores `pageUrl`, a missing `renewCheck` is unknown. The extension sends the context menu's page,
  the playing video file's tab, else the page or frame the browser names as the download's referrer,
  never one of a private window, and never asks for the list of expired downloads.
- `addMedia` (docs/MEDIA-SPEC.md §6) is bounded and timed like `add` (15 s, `deadlineMs`, the same
  margins); the daemon fetches and parses the manifest(s) before it answers, so the extension sends
  `deadlineMs: 15000`. The relay sends `DownloadService.AddMedia` with `source = ADD_SOURCE_BROWSER`, the
  headers, `page_url` and `title`; with `confirm_browser_downloads` the stream is held for confirmation
  with its tracks, like `add`. A stream the daemon refuses is answered `rejected` with the daemon's
  message, which starts with the stable code and a colon: `encrypted: …`, `live: …`,
  `media_unsupported: …`, `media_invalid: …`; the extension explains `encrypted` and `live` refusals.
  `videoTrack`/`audioTrack` (`validate.TrackID`; missing or `""` = the daemon's default pick) are the
  ids of a `probeStream` answer; the extension sends them only to a relay that lists `"streams"`.
- `probeStream` (docs/VIDEO-DETECT-SPEC.md §3.5) lists the MP4 qualities of a manifest a page loaded:
  the relay sends `ProbeMedia` with `origin = MANIFEST`, on the video pool with the video requests'
  30 s bound, and autostarts `downexd` (it is always a user's click). No side effects; refusals are
  `rejected` with `reason` as for every request. The extension sends it only to a relay that lists
  `"streams"`; otherwise stream rows offer the default pick and say "Update Downex to choose a stream
  quality."
- `probeVideo` and `addVideo` (docs/SITE-MEDIA-SPEC.md §3.5) are the videos of web pages: the relay sends
  `ProbeMedia` / `AddMedia` with `origin = SITE` and the page as `page_url` (`addVideo` also with
  `source = ADD_SOURCE_BROWSER` and the chosen tracks; empty tracks ask for the daemon's default pick).
  They are bounded by 30 s (`deadlineMs` may only shorten it; the 200 ms answer margin stays) and run on
  a pool of their own (4 at once, at most 16 waiting or running; beyond that `daemon_unavailable` at
  once), so slow page lookups never delay `add`. `awaiting` is true when the download waits for
  confirmation. `tools_installing` and `site_resolving` (in `reason`) ask the extension to retry every
  2 s. The log keeps the page URL without its query, the header count, the title length and the track
  ids. The extension sends video requests only to a relay whose welcome lists `"video"`; otherwise it
  says "Update Downex to download videos."
- Browser-fetched saves (docs/SITE-MEDIA-SPEC.md §2.8, amendment 2026-11-18): the welcome lists
  `browserFetch`. `addVideo` then takes `browserFetch: true`, forwarded as
  `AddMediaRequest.browser_fetch`; `added` carries `fetchTicket` when downexd made one; a `videoInfo`
  of a page whose video servers answer only browsers carries `browserFetch: true`. `fetchTake` and
  `fetchPut` move a download's requests to the browser and its bytes back: on a pool of their own (4
  at once, never one of the add or video slots), a take bounded by its wait plus 5 s and a put by
  30 s; neither starts downexd (`daemon_unavailable` when none runs). The log keeps the ticket's first
  8 characters, the fetch id, the seq, the status and the byte count, never an address or the bytes.
- Save what's playing (docs/SITE-MEDIA-SPEC.md §2.9, §3.5, amendment 2026-11-18): the welcome lists
  `played`. `probePlayed` and `addPlayed` carry the clicked video's played files (addresses, sizes,
  kind hints, first 64 KiB each as base64, whether the tab was needed) and the element's duration,
  picture and sound; they are forwarded as `ProbeMedia`/`AddMedia` with `origin = PLAYED` on the video
  pool. `addPlayed` answers `added` with `fetchTicket`. The extension sends them only to a relay that
  lists `played`; otherwise the panel says "Update Downex to save this video." The log keeps no
  address and no head byte.
- The user's sign-in (docs/SITE-MEDIA-SPEC.md §6.3, amendment 2026-11-18): a user's `probeVideo` or
  `addVideo` goes first without cookies; only an `auth_required` answer that is not a bot check ("not a
  bot", "fresh cookies") makes the extension send it once more, at once, with the `Cookie` of that
  site's sign-in domain (a curated site's fixed
  registrable domain, or a generic page's exact host) and, for a curated site, `cookieDomain`, which
  the relay forwards as `cookie_domain` (downexd checks it against the page again). The relay never
  logs header values or the cookie domain. A `probeVideo` with `prefetch: true` never carries a
  `Cookie` (downexd refuses one that does).
- `probeVideo` with `prefetch: true` is the extension's look-ahead (§4): the relay sends it as
  `ProbeMedia{prefetch: true}`, otherwise exactly like a user's probe (same pool, timing and answer),
  except that it never starts downexd: with none running it answers `daemon_unavailable` (the user
  may have quit Downex; only a user's request starts it). The daemon never installs tools for it
  (`tools_unavailable` while they are known not to be ready), never lets it wait behind or delay a
  user's probe (`site_resolving` at once while it works for one), joins an identical extraction and
  fills the same extraction cache (SITE-MEDIA-SPEC §5.6). A `prefetch` that is not a boolean is
  `invalid_request`; `addVideo` ignores the field. The log records `prefetch`. The extension sends a
  look-ahead only to a relay whose welcome lists `"prefetch"` (an older relay would drop the mark, and
  the daemon would take it for a user's probe, which installs tools); otherwise it fails locally with
  `unsupported` and nothing is sent.
- `rescue` (§6) sends `DownloadService.Rescue` with `source = ADD_SOURCE_BROWSER`, the target as
  `target_path` (downexd finds the partial next to it), the browser's count, total, start, validators,
  name and page; it is bounded by 30 s (`deadlineMs` may only shorten it, the 200 ms margin stays),
  autostarts `downexd` (a user's click) and does not use the video pool. `resume` sends
  `DownloadService.Resume` with `rescue_start` (bounded like `add`), which downexd accepts only for a
  rescue stored paused, once, within 10 minutes (DAEMON-SPEC §3 "Rescue"): the relay cannot start other
  downloads. Refusals are `rejected` with `reason` (`rescue_changed`, `rescue_not_found`, …). The log
  records the type, the redacted URL, the header count, the target's base name only, the counts,
  `startPaused`, the deadline and, on success, the kept bytes. The extension sends both only to a
  relay whose welcome lists `"rescue"`; otherwise the card and the notifications of §6 stay hidden.
- The extension sends nothing but `hello` until a `welcome` with `protocol == 1` arrives; requests made
  meanwhile wait (their timeout and `deadlineMs` still run from when they were made). Any other
  protocol value: the extension disconnects, fails the waiting requests with the local code
  `incompatible`, shows "Incompatible Downex" and retries with its normal backoff.

## 3. Capture (decide whether Downex takes a download)
Rules (stored in `storage.local`, editable on the options page):
- enabled (global toggle), file extensions list (defaults: archives, installers, disk images,
  video, audio, documents — e.g. zip rar 7z gz xz exe msi apk iso img dmg mp4 mkv avi mov webm
  mp3 flac m4a pdf epub), minimum size (default 0 = any), excluded hosts (wildcards), and
  "always ask the browser" for `blob:`/`data:`/incognito downloads (never captured).
- Bypass: holding **Alt** while clicking a link lets the browser download it (the content script
  reports the last click's modifiers to the background with a timestamp; valid for 2 s). Clicks are
  tracked per tab and frame; a download looks at its own tab's click, or at every tab's when its tab is
  unknown.
- Excluded hosts match the download URL's host, the final URL's host and the host of the page the
  download was started from (its http(s) referrer).
- Per-tab pause: tab ids in `storage.session` key `pausedTabs` (cleared when the tab closes, moved when
  the browser replaces it); such downloads are skipped with reason `tab_paused`. Messages
  `downex:tab-state {tabId} → {paused}` and `downex:set-tab-paused {tabId, paused} → {paused}`.
- Host permissions are `http://*/*` and `https://*/*` only.

Chromium (downloads API, pause → hand-off → commit):
1. `downloads.onDeterminingFilename`: run the rules synchronously; non-candidates → `suggest()` at once.
2. Candidate: keep the listener async (return `true`), `downloads.pause(id)`, build the `add`
   request using the request context below, send it.
3. `ok` → `downloads.cancel(id)` then `downloads.erase({id})`. Error or no answer within 3 s →
   `suggest()` + `downloads.resume(id)`: **the user never loses a download**.
4. `downloads.onCreated` fallback: a download that has no `onDeterminingFilename` 1 s after it was
   created is claimed, decided and handed off paused the same way (the two listeners share the
   claimed IDs, so a download is decided once).
5. Capture log (popup, `storage.session` key `captureTrace`, last 20 steps, URLs without query):
   the download-like responses `webRequest.onHeadersReceived` saw, both downloads events, each
   decision with its skip reason and each hand-off result. A download that could not be paused is
   also listed in the hand-off history.

Firefox: blocking `webRequest.onHeadersReceived` (main_frame/sub_frame) classifies attachments
(`Content-Disposition: attachment`, MIME, extension, length) and returns a Promise that resolves
`{cancel:true}` once `added` arrives, or `{}` after 1.5 s. `downloads.onCreated` is a fallback.

Request context (observe-only `webRequest`): `onSendHeaders` with `requestHeaders` +
`extraHeaders` (Chromium) records Cookie/Referer/User-Agent actually sent; `onBeforeRequest`
with `requestBody` records POST data. Keyed by URL with a 60 s TTL; fallback
`cookies.getAll({url})` for that URL only. The extension never reads cookie stores in bulk.
The context of a download is the request that returned the file: the original URL's, then the
final URL's. A POST answered by a redirect (a Cloudflare challenge or login form posting to the
page, then a 302 to the file) gives way to the later GET of the final URL from the same tab; a POST
kept by a 307/308 or without a redirect stays in the browser (`not_get`).

## 4. UI
- The popup's Downloads tab shows the card "Failed in the browser" above the recent hand-offs while
  it has rows and the relay serves rescues (§6).
- A hand-off answered with `renewCheck` shows the pill "Checking in Downex" (info) in the popup's
  history, with the tooltip "Downex is checking whether this link continues a stopped download."
  (DESKTOP-SPEC §10).
- Context menus: "Download with Downex" (links, media), "Download all links with Downex" (page;
  collects `a[href]` http(s) links via `scripting.executeScript`, deduped, confirm if > 50).
  *Amended 2026-11-18 (audio pages, SITE-MEDIA-SPEC §3.13):* a link to a SoundCloud, Bandcamp or
  Mixcloud track (VIDEO-DETECT-SPEC §3.8) is a curated link like a video's: "Download with Downex"
  and "Download video from this link with Downex" send its page with Downex's default pick, and
  downexd saves the page's audio (`.m4a` or `.mp3`); any other link to an audio page does the same
  through the generic target. No new item or permission.
  *Amended 2026-11-18* (VIDEO-DETECT-SPEC §3.8, §5.1), two more items:
  - **"Download video from this link with Downex"** (links, `targetUrlPatterns` http(s)): the page
    the link opens, saved as a video. A link the curated table knows (§3.8 there) goes as that
    site's video, exactly as "Download with Downex" sends it; any other link goes as a generic
    target (`page:<address>`, the link without its fragment): `addVideo` with Downex's default pick,
    never with cookies, with the default-policy Referer of the page the link is on (the origin alone
    across origins). Private windows, the store build's YouTube links and `network` sites (§3.8
    there) record a failure instead, with their text. "Download with Downex" keeps the file route
    for links the table does not know, so the user chooses between the page's video and the file.
    The store build has no such item: yt-dlp's reader of any page follows a YouTube player that the
    linked page may embed, which that build never hands over (as for "Find the video on this
    page" there); its curated links still go through "Download with Downex".
  - **"Show Downex download panel"** (`video`, in http(s) documents): sends that frame
    `downex:show-panel` (`tabs.sendMessage`, the menu's `frameId`), which opens the in-page panel
    for the right-clicked `<video>`, whatever its source (`blob:`/MSE players too). Match patterns
    cannot name `blob:` sources, so the item shows on every video, also those with an http(s)
    source, where it lists every source (files, streams, the site's qualities) rather than the one
    file "Download with Downex" saves. Nothing is sent in private windows; a frame without the
    tracker (YouTube's own hosts, a page loaded before the extension) or without a right-clicked
    video records a failure for the popup's history.
  - With two or more items for one target the browser shows them under one "Downex" entry.
- Toolbar popup: daemon status, capture on/off, last 10 hand-offs with result (an `added` one reads
  "Waiting for confirmation in Downex"), and a "This tab" card: **Disable on <host>** (adds or removes
  the exact host in the excluded hosts; locked on when a wildcard already excludes it) and **Pause
  capture in this tab**.
- Toolbar popup, "Media on this page" card (M5): the streams detected in the tab (§5), each with its
  format (HLS/DASH), host and the last two path segments (full URL as tooltip) and the page title; its
  Download button sends `addMedia` (one request per stream at a time; "Sent" after success, "Retry"
  after a failure that is not a refusal) and explains encrypted and live refusals. Video detection
  replaces it with "Videos on this page": site videos, then per frame its streams (with their MP4
  qualities) and direct MP4 files (docs/VIDEO-DETECT-SPEC.md §5.3).
- Videos of web pages (docs/SITE-MEDIA-SPEC.md §5.1, §5.2): a "Download" button over YouTube players
  (`youtube.content.ts`, a closed shadow root) and video rows first in the popup, each with a quality
  picker that lists every MP4 quality best first; "Find the video on this page" on other http(s) tabs;
  a context-menu link to a video page sends `addVideo`. On every other site a "Download" button sits
  over each visible, meaningful `<video>` of any frame, with one panel of file, stream and site
  qualities (docs/VIDEO-DETECT-SPEC.md §2.5, §2.6, §5.1, §5.2); never over DRM-protected or live
  playback; hidden per site with `hiddenSites` (§5.4 there). Both panels, YouTube's and this one,
  can save just part of the video when Downex lists `clips` (§5.2 there). *Amended 2026-11-18
  (owner decisions 2-4):* on TikTok, Instagram, Facebook, X, Reddit, Pinterest, Threads, Snapchat,
  LinkedIn, Bluesky, Tumblr, 9GAG, Imgur, VK and Telegram (and their embedded players) the button is
  a compact mark (28 px, 24 px on small videos) in a corner free of the site's own controls,
  widening to "Download" (and, once known from the background's memory, the best quality and size)
  on hover or focus, with a turning ring while it works and a check once Downex took the video;
  those sites' carousels, stories, grid tiles (a button on the hovered tile), embeds and videos
  that mount late get it too (docs/VIDEO-DETECT-SPEC.md §3.8, §5.1 "Social sites"). No new
  permission, message to Downex or cookie use; YouTube's button is unchanged.
- Look-ahead (`lib/video-prefetch.ts`): while the user watches, a video's qualities are got in the
  background, so Download (overlay or popup) lists them at once. Besides YouTube it runs on the
  curated video sites marked for it (docs/VIDEO-DETECT-SPEC.md §3.8, §5.5, with per-site throttles);
  the rules below are YouTube's.
  - The YouTube content script of the top frame counts a dwell of 1.5 s on a watch or Shorts video
    (never embeds, `/live/` pages or clips) while `document.visibilityState` is `visible`. Another
    video (SPA navigation, a Shorts swipe), a hidden tab or the option turned off start the count
    again, so Shorts swiped past are never reported; the address is read again when the time is up.
    Each continuous visible stay on a video is reported once: `downex:page-video-prefetch {url}`
    (not answered).
  - The background takes it only from frame 0 of a tab that is not private and is active in its
    window, about a watch or Shorts address on the sender's own origin (the checks of
    `downex:page-video-probe`). It sends `probeVideo` with `prefetch: true` and the headers a probe
    would carry, through the probe cache (SITE-MEDIA-SPEC §5.2): nothing when that video's answer is
    kept or a probe of it runs. The answer is kept like a probe's, so the overlay, the popup and
    `downex:tab-media` use it, except `site_unavailable` and `auth_required`: YouTube answers
    look-aheads it throttles with them, and a click must not inherit that for 10 minutes (it asks
    Downex, which keeps a refusal 30 s). A look-ahead takes neither of the cache's two probe slots.
  - A look-ahead never carries the user's cookies and is never asked again with them: its headers
    never include `Cookie`. Since the amendment of 2026-11-18 (SITE-MEDIA-SPEC §6.3) a click's first
    request goes without cookies too, so look-aheads run whether "Use my sign-in for videos that
    need it" is on or off: a look-ahead is the click's first request, made earlier.
  - At most one look-ahead is in flight in the whole browser. While it runs only the newest request
    waits (an older waiting one is dropped); before a waiting one is sent, its tab must still show
    that video (`tabs.get`: same video, active, not private).
  - None is sent while the options do not allow it (`lookAheadAllowed`: `videoPrefetch` on, the
    Download button `videoButton` on; the content script stops counting
    too), while the relay is not connected or does not list `"prefetch"`, or while downexd is not
    known to run (`daemonReady`; any `videoInfo` or `rejected` answer also says it runs, since the
    welcome may come while it starts); none after a look-ahead was answered `tools_installing`,
    `tools_unavailable` or `site_media_off` (the waiting one is dropped too) until the user asks for a
    video once (a probe or an add from the overlay, the popup or the context menu); and none for
    30 min after one was answered `site_unavailable` or `auth_required` with YouTube's rate limit or
    bot check in its message (HTTP 429, "rate-limit", "too many requests", "try again later", "not a
    bot"), whatever the user does. The back-offs live in the background's memory.
  - Budget: at most 10 look-aheads go out at once, then one more per minute (at most about 60 an
    hour after the first 10), whatever is watched; one that sent nothing (kept answer, probe
    running) costs nothing. With a 1.5 s dwell, Shorts would otherwise send one extraction per clip.
  - A user's probe while the look-ahead of that video runs joins it: no second request. When the
    look-ahead's answer is not one the cache keeps (`tools_unavailable`, `tools_installing`,
    `site_resolving`, a timeout …), the user's probe then asks Downex itself at once.
- Options page: rules editor; video options (`videoOptions` in `storage.local`: the overlay button
  `videoButton`, on by default; "Prepare video qualities while you watch" `videoPrefetch`, on by
  default, also for options stored before it existed, whose hint says it runs only with the button
  shown, one video at a time and at most about one a minute, and never sends the sign-in; and "Use
  my sign-in for videos that need it" `useSignInForVideos`, on by default from 2026-11-18 and **off by
  default** for new installs from the second 2026-11-18 amendment, a stored value kept, used for
  YouTube videos only: SITE-MEDIA-SPEC §6.3; and "Watch videos as they play" `watchPlayed`, on by
  default: the per-tab log of played media of SITE-MEDIA-SPEC §2.9). Video
  detection rewords the button "Show a Download button over videos" and adds "Don't show the button
  on these sites" `hiddenSites` (docs/VIDEO-DETECT-SPEC.md §5.4).
- Badge: `!` (global) when the relay/daemon is unavailable; otherwise a tab with detected streams or page
  videos shows their count (blue). `!` takes precedence over the count. Video detection counts groups
  and files by docs/VIDEO-DETECT-SPEC.md §5.3.

## 5. Streaming media detection (M5)
- An observe-only `webRequest.onResponseStarted` listener (types `xmlhttprequest`, `media`, `other`;
  `responseHeaders`, never blocking) flags a response as a stream when it is a GET from a tab (not a
  private window), its document is active (Chromium `documentLifecycle`; prerender and
  `pending_deletion` ignored), its status is 2xx or 304, and its Content-Type is one of MEDIA-SPEC §6
  step 1 or its URL path ends in `.m3u8`/`.mpd` (a path served as `text/html` is not flagged). The URL
  is kept without its fragment and must be at most 32 KiB.
- The headers the page sent for the manifest come from `onSendHeaders` (now also for `xmlhttprequest`,
  with `extraHeaders` on Chromium) in a second request-context store (60 s TTL, 512 entries), run
  through the same header filter as `add`. Without recorded headers the hand-off uses
  `cookies.getAll({url})` for the manifest URL (its cookie store) plus a default-policy Referer of the
  requesting frame or page; a `User-Agent` is added when missing.
- Per tab, newest first, deduplicated by URL (a repeat sighting moves it to the front and refreshes its
  headers), at most 50 per tab and 100 tabs, about 2 MiB in all (past it, the oldest entries lose their
  headers first, then are dropped). Kept in `storage.session` (key `media:<tabId>`; headers only there,
  in memory when only `storage.local` exists), restored after a service-worker restart. A tab's list is
  cleared by a top-level navigation (`webNavigation.onCommitted`, frame 0, not prerender; same-document
  navigations keep it), when the tab closes, moved when the browser replaces the tab, and cleared on
  browser start. This needs the `webNavigation` permission.
- An HLS master playlist and the variant playlists the player fetches are listed as separate streams;
  the daemon accepts either. `title` is the page title with control characters removed, whitespace
  collapsed, cut to at most 512 characters and 512 UTF-8 bytes; `pageUrl` only when http(s) and at most
  32 KiB.
- Runtime messages: `downex:tab-media {tabId} → {items}` (never with headers) and
  `downex:add-media {tabId, url} → outcome` (local error `expired` when the stream is no longer listed).
- Video detection (docs/VIDEO-DETECT-SPEC.md §2.4) extends this listener with the types `main_frame`,
  `sub_frame` and `object` and classifies progressive MP4/M4V files besides the streams (never for
  `xmlhttprequest`/`other`), records frame and document identity for both, follows redirect chains
  with an observe-only `onBeforeRedirect`, and clears a sub-frame's items on its commit. Its runtime
  messages (`lib/messages.ts`) are listed in §3.6 there.

## 6. Rescue failed browser downloads
When Chrome or Edge stops a download (state `interrupted`) or Firefox stops or pauses one, the
extension offers to finish it with Downex from the bytes the browser already saved. downexd proves
that the server still has the same file before it touches anything (ENGINE-SPEC §7.4), then moves
the browser's partial file (`<name>.crdownload`, `Unconfirmed N.crdownload`, `<name>.part`) into
its own part file in the same folder (a rename, never a copy) and fetches only the rest.

Status: implemented 2026-11-17 with unit tests, fake-browser wiring tests and real-process relay
tests; **not yet run in a real browser**.

- **R-E1. Offered downloads** (`lib/rescue.ts` `rescueOffer`): not in a private window; an http(s)
  address; a file name; `danger` absent, `safe` or `accepted` (a file the browser flagged is never
  offered: its safety check is not bypassed); not a POST (the saved context's method); Chromium:
  state `interrupted` with an error other than `USER_CANCELED`, `FILE_BLOCKED`,
  `FILE_VIRUS_INFECTED`, `FILE_SECURITY_CHECK_FAILED`, and still interrupted 3 s after the change
  (Chromium resumes some interruptions by itself); Firefox: state `interrupted`, paused or with an
  error other than `USER_CANCELED`. A download whose browser saved no byte is offered as a plain
  `add` ("Download with Downex"). Downloads the extension started itself are never offered.
- **R-E2. Rescue context.** When the browser keeps a download (`downloads.onCreated`, every download
  not started by the extension), the extension keeps how it was asked for: the recorded request's
  address, method and filtered headers (§3 "Request context") and the response's ETag and
  Last-Modified (an observe-only `onHeadersReceived` of download-like responses, kept 60 s by
  address and cookie store). Per download id, at most 20 for 24 h, in `storage.session` key
  `rescueContext` (headers carry cookies: memory only where a browser has no `storage.session`,
  never `storage.local`); dropped when the browser's entry is erased.
- **R-E3. Fields.** The saved context's headers and validators; without them the cookies of that
  one address (`cookies.getAll({url, storeId})`), the Referer the default policy derives from the
  download's referrer, and the User-Agent. `target` = the browser's final path, `bytesReceived`,
  `totalBytes` (−1 unknown), `startTime`, `pageUrl` (the referrer, http(s) only), `startPaused:
  true`, `deadlineMs: 30000`; the extension waits 32 s.
- **Flow of one click** (`finishWithDownex`): (a) the download is read again (`downloads.search`)
  and the flow stops with "This download is no longer in the browser's list." when it is no longer
  offered; (b) nothing saved, or "Download from the start with Downex": `add` with the download's
  context, and only after `added` is a running entry cancelled and the entry erased; (c) otherwise
  `rescue`, stored paused; (d) on `rescued`: an entry the browser runs again is cancelled, the entry
  is erased (the partial is already moved, so the erase deletes nothing of Downex's, and no browser
  auto-resume or clean-up can race the run), then `resume` sends the first start; (e) a failed
  `resume`: "Downex has it, paused. Resume it in Downex."; (f) a `rescued` after the extension's
  timeout still runs (d). A refused rescue changes nothing in the browser.
- **Popup card "Failed in the browser"** (Downloads tab, above the recent hand-offs; only with rows
  and a relay listing `rescue`): at most 5 rows, newest first (the browser's interrupted downloads,
  read again each time the popup opens or the list changes: this is also the start-up scan, so no
  notification is shown for them); each with its file tile, base name, "<reason> · <saved> of <total>
  saved" (or "<reason> · <saved> saved"; decimal units), the primary "Finish with Downex" (or
  "Download with Downex" without saved bytes) and "×" ("Hide from this list"; the ids in
  `storage.session` `rescueHidden` for the session). Reasons: NETWORK_FAILED/TIMEOUT/DISCONNECTED/
  SERVER_DOWN/INVALID_REQUEST "Network failed"; SERVER_UNAUTHORIZED/FORBIDDEN/CERT_PROBLEM "Refused
  by the server"; other SERVER_* "Server error"; FILE_NO_SPACE "Disk full"; FILE_TOO_LARGE "Too
  large for the disk"; other FILE_* "Could not write the file"; USER_SHUTDOWN "Browser closed";
  CRASH "Browser crashed"; Firefox paused without an error "Paused"; anything else "Stopped".
  While the flow runs: "Checking the file with the server…", the button disabled. Success:
  "Continuing in Downex from 412 MB" for 5 s, then the row leaves; the hand-off history gets a
  record with source `rescue` ("Finished from the browser", pill "Finished in Downex"). Failure:
  the text of R-E7 on the row; `rescue_locked`, network, a stopped or slow downexd and timeouts
  turn the button into "Retry"; `rescue_no_ranges`, `rescue_size_unknown`, `rescue_changed` and
  `rescue_unproven` into "Download from the start with Downex"; `link_expired` and the others
  offer nothing.
- **Notifications** (option "Offer to finish failed downloads with a notification" in the options
  page's "Failed downloads" section, `storage.local` `rescueOptions.rescueNotify`, default off; hint
  "Shows a notification when a download fails in the browser. The browser asks you to allow
  notifications first."). Turning it on asks for the optional `notifications` permission
  (`optional_permissions`; the required permissions are unchanged, so an update adds no warning); a
  refusal turns it off with "The browser did not allow notifications."; removing the permission
  turns it off. For a download that fails in this browser session: title "Download failed:
  <name>", message "<reason>. Downex can finish it from <saved>.", Chromium's button "Finish with
  Downex"; a click on the notification does the same (Firefox has no buttons). Results: "Continuing
  in Downex" / "<name> from <saved>", "Downex could not finish <name>" / the R-E7 text. At most one
  notification per download and 3 a minute (`downex-rescue:<downloadId>`); none for the downloads
  already interrupted when the popup or the worker starts.
- **R-E7. Texts** (also the desktop window's and the CLI's: `internal/rescuetext`, held to
  `lib/rescue.ts` by `TestExtensionRepeatsTexts`): `rescue_no_ranges` "This server cannot continue a
  download from the middle, so the saved part cannot be used."; `rescue_size_unknown` "The server
  does not say how big the file is, so Downex cannot check the saved part."; `rescue_changed` "The
  file on the server is not the one the browser started, so the saved part cannot be used.";
  `rescue_unproven` "The saved part holds too little that tells files apart, so Downex cannot check
  it."; `rescue_not_found` "The browser's partial file is gone. Browsers delete it when a download is
  cancelled."; `rescue_ambiguous` "More than one unfinished browser file could be this one. In Downex,
  use Rescue partial file… and choose it."; `rescue_locked` "The browser is still using the partial
  file. Try again in a few seconds."; `rescue_no_access` "Downex is not allowed to open the partial
  file." (macOS adds " Allow Downex Agent in System Settings > Privacy & Security > Files and
  Folders."); `rescue_empty` "The partial file is empty: there is nothing to finish.";
  `link_expired` "The address has expired. Start the download again on the web page.";
  `http_status` "The server refused the address. It may have expired: start the download again on
  the web page."; `auth_required` "The server asks for a sign-in. Add a site login in Downex, then
  try again."; `network` "Downex could not reach the server."; a stopped Downex, a timeout and other
  codes as the other hand-offs word them.
- Runtime messages (`lib/messages.ts`, extension pages only): `downex:failed-downloads {} →
  {available, items}` (never headers), `downex:finish-download {downloadId, fromStart?} → {row?}`,
  `downex:hide-failed {downloadId} → {}`.
- **Not verified** (real browsers, the owner): Chrome and Edge on Windows 11 (the `.crdownload`
  after each interrupt kind: a network loss, a killed server, a server error; `Unconfirmed
  N.crdownload` while a Save As prompt is open; that the erase after the move deletes nothing of
  Downex's; the auto-resume race; the notification's button and body; the permission prompt;
  parallel downloading, which writes slices at several offsets); Firefox 140+ on Windows and macOS
  (the `.part` name and folder, the 0-byte placeholder, paused = interrupted + paused, what erase
  does to an interrupted item's files, notifications without buttons).
