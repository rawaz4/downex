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
- Firefox add-on ID (gecko id): `extension@downex.app`, permanent from the first addons.mozilla.org
  upload. `downex-nmhost`'s `allowed_extensions` lists it first, then `browser@downex.local` for
  copies loaded by hand before the listing. The Firefox build needs Firefox 140 or later and declares
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
| `hello` | `protocol: 1`, `browser: "chrome"\|"edge"\|"firefox"\|"other"`, `extVersion` | `{type:"welcome", protocol:1, daemonVersion, features}` (`features: ["video", "prefetch", "streams"]`: `video` since site media, `prefetch` since the look-ahead, `streams` since video detection on every site) |
| `add` | `url`, `method` (`GET`/`POST`), `headers: [[name, value], …]`, `body?` (base64, ≤ 1 MiB, POST only), `filename?`, `referrer?`, `mime?`, `size?`, `startPaused?`, `deadlineMs?` | `{type:"added", downloadId, awaiting}` |
| `addMedia` | `manifestUrl`, `headers: [[name, value], …]`, `pageUrl?`, `title?` (≤ 512 chars), `videoTrack?`, `audioTrack?`, `deadlineMs?` | `{type:"added", downloadId, awaiting}`; errors `rejected` with message code `encrypted` / `live` when the stream cannot be downloaded |
| `probeStream` | `manifestUrl`, `headers?`, `pageUrl?`, `deadlineMs?` | `{type:"videoInfo", site:"", title, durationMs, format:"hls"\|"dash", container:"mp4", selectedVideo, selectedAudio, tracks:[…]}` (as `probeVideo`'s; never a URL) |
| `probeVideo` | `pageUrl`, `headers?` (only `Cookie`, `Referer` used), `prefetch?` (boolean: the look-ahead of §4), `deadlineMs?` | `{type:"videoInfo", site, title, durationMs, format, container:"mp4", selectedVideo, selectedAudio, tracks:[{id, kind, ext, codecs, width, height, frameRate, bandwidth, size, sizeApprox, language, name}]}` (≤ 200 tracks, ≤ 256 KiB, never format URLs) |
| `addVideo` | `pageUrl`, `headers?`, `videoTrack?`, `audioTrack?`, `title?`, `deadlineMs?` | `{type:"added", downloadId, awaiting}` |
| `status` | — | `{type:"status", running, active, queued}` |
| `ping` | — | `{type:"pong"}` |

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
- Context menus: "Download with Downex" (links, media), "Download all links with Downex" (page;
  collects `a[href]` http(s) links via `scripting.executeScript`, deduped, confirm if > 50).
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
  playback; hidden per site with `hiddenSites` (§5.4 there).
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
  - A look-ahead never carries the user's cookies: none is sent while "Send my sign-in cookies" is
    on (the click's probe carries them, so a look-ahead without them would be another request), and
    its headers never include `Cookie` whatever the option says by the time it goes out.
  - At most one look-ahead is in flight in the whole browser. While it runs only the newest request
    waits (an older waiting one is dropped); before a waiting one is sent, its tab must still show
    that video (`tabs.get`: same video, active, not private).
  - None is sent while the options do not allow it (`lookAheadAllowed`: `videoPrefetch` on, the
    Download button `videoButton` on, `useSignInForVideos` off; the content script stops counting
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
  shown, one video at a time and at most about one a minute, and pauses while the sign-in is allowed;
  and "Send my sign-in cookies when a video needs it" `useSignInForVideos`, off by default). Video
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

