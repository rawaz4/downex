# Video detection specification (every website, MP4 only)

Downex finds the videos of any web page: a "Download" button over each video the user can see in
any frame, filled at once from what the browser itself loaded (direct MP4 files, HLS and DASH
manifests), plus yt-dlp's qualities wherever yt-dlp knows the site. Every video saved from these
panels is one `.mp4`. HLS made of MPEG-TS segments is converted to MP4 by a new pure-Go remuxer, and
M5 manifest downloads of every client produce MP4 too.

This spec extends `BROWSER-SPEC.md` (extension, relay), `MEDIA-SPEC.md` (M5 manifests, remux) and
`SITE-MEDIA-SPEC.md` (yt-dlp, the YouTube overlay, the look-ahead). The decisions below are dated
2026-10-23 and are final unless §9 lists them as open. Where an older spec disagrees, this one wins;
Step 0 (§7) brings the older texts in line first.

> **User decisions (2026-10-23, final):**
> 1. **Any page, any frame.** A "Download" button over every visible, meaningful `<video>`, with the
>    YouTube overlay's look, generalised. There is a per-site "Don't show on <host>" and a global
>    option. There is never a button on DRM-protected playback (the element's `mediaKeys`, §2.5) or
>    on a live stream.
> 2. **Instant detection from the browser's own traffic**, per tab and frame, with no yt-dlp wait:
>    - Direct progressive video files (MP4/M4V only), matched to the element's `currentSrc` when
>      possible and grouped across range requests. Tiny files and ad-like short clips are
>      de-prioritised, not hidden.
>    - HLS and DASH manifests (the M5 detector).
>
>    Both are listed with quality and size in the button's panel and in the popup.
> 3. **yt-dlp for the sites it supports.** Any page URL can be resolved (SITE origin). The look-ahead
>    runs automatically only on a curated list of popular video sites (§3.8). Elsewhere yt-dlp runs
>    only on the user's click. Its qualities are merged with the network-detected items. YouTube keeps
>    working as now.
> 4. **MP4 only everywhere.** Every video saved from these panels is one `.mp4`. WebM and other
>    containers are not listed. HLS made of MPEG-TS segments is converted to MP4 by a new pure-Go
>    TS→MP4 remuxer: no FFmpeg, no re-encoding. Existing M5 HLS-TS downloads now produce MP4 too.
>    Input is untrusted: the remuxer is fuzzed and bounded.
> 5. **Direct MP4 files are ordinary downloads.** They go through `add` (segmented engine, ranged
>    resume) with the headers and referrer recorded from the request, not through a media job (§2.3).
> 6. **Clean room and privacy.**
>    - No other product's code, assets, wording, URL pseudo-schemes or message formats.
>    - No cookie is sent unless the existing rules already send it for the tab's requests.
>    - No automatic yt-dlp run on arbitrary sites: look-ahead only for the curated list, and the
>      user's click everywhere else.

> **Amendment 2026-11-18 (owner: "please dont rely on login at all … download whatever video user
> want and show download downex icon then download"; SITE-MEDIA-SPEC §2.9 "Save what's playing"):**
> - **A fourth source, Played** (§2.6). On any site but YouTube, the panel of a `<video>` that plays
>   `blob:` (MSE) lists "This video" from the files the tab's own player fetched (`fetch()`/XHR,
>   workers and media requests alike), matched to the element by its duration (SITE-MEDIA-SPEC §2.9).
>   It is the default on feeds, profiles, the For You page, reels and post viewers, and whenever the
>   Site source fails. On a curated video's own page the Site rows stay first, except on a site whose
>   video servers answer only browsers (TikTok), where Played is first.
> - **What the background records** (§2.4, amends decision 2 and "Segment-like files" for this log
>   only): `PlayedLog`, per tab, the media responses of every request type with their range-in-query
>   pieces folded into whole files. It is never sent anywhere but, on a click, to the local Downex.
> - **No dead ends** (§5.1, §5.6). "Open this video's own page to download it." and "Open this
>   video's comments, then click Download again: …" are removed. A panel without rows says what it
>   is doing ("Getting this video…", then "Downex hasn't seen this video load yet. It appears here as
>   soon as it plays."). A Site failure is never the panel's result while another row exists, and
>   the in-page panels never name a sign-in on any site but YouTube.
> - **Order** (§2.6). Played rows lead and hold the default, unless the frame's own address is the
>   video, its servers answer more than browsers and the site's rows offer a better picture class;
>   a grid tile that plays a `blob:` preview has the Played source of that preview.
> - **The click's own address** (§3.6). `downex:frame-offers` carries the frame's `location.href`;
>   the background uses it in place of the sender's address when both have the same origin, so a
>   post viewer opened with `pushState` is judged by its real address.

> **Amendment 2026-10-23 (quality names; user decision, SITE-MEDIA-SPEC.md "Amendment 2026-10-23"
> holds the full rule and the shared test table):**
> 1. **Standard quality names.** Every video row (Site, Stream, File, popup, desktop, CLI) is named by
>    its quality class, never by its raw height: "8K (4320p)", "4K (2160p)", "1440p (2K)",
>    "1080p (Full HD)", "720p (HD)", "480p", "360p", "240p", "144p", then " 60fps" (rounded fps over
>    30) and " HDR". The class is the ladder step nearest to the picture's longer side scaled to
>    16:9 (a 3840x1634 film is 4K). width x height is secondary detail (tooltip, details), not the
>    name.
> 2. **One row per class.** Site offers (yt-dlp) and stream offers (HLS/DASH variants,
>    `sitemedia.ManifestTracks`) keep one video per class, chosen in this order:
>    - video-only before muxed
>    - SDR before HDR (HDR is not known of manifest tracks)
>    - the higher frame rate (rounded)
>    - H.264 before HEVC before AV1
>    - the higher bit rate, then the larger picture and size
>
>    The default is the best class. Videos without a picture (an HLS variant without RESOLUTION)
>    have no class and are all listed, after the classes. The second offer round of §3.3 still
>    falls back to a class's next variant when its best one cannot be saved. A variant the rule
>    leaves out stays selectable: a probe or add that names it (by key, else ID) lists it in place
>    of its class's (`sitemedia.ManifestTracksFor`), as a site format left out is listed in place
>    of its class's row (`sitemedia.Offer.Listed`).
> 3. **Names come from the daemon.** `internal/media` names HLS/DASH tracks and `internal/sitemedia`
>    names site tracks, both with `internal/media/quality.go`. The extension and the desktop show the
>    track's `name` and derive one only where it is not a standard name: an older daemon (a raw
>    height, also one that looks standard such as "480p" of 1138x480), or a File row named from
>    the element's `videoWidth`/`videoHeight`. Each derives it with one helper, `lib/quality.ts`
>    (the same file in both packages, held to Go's tables by
>    `internal/media/quality_sync_test.go`); the CLI shows `media.TrackName`, the same rule.
> 4. **Streams of a panel** (§2.6) are merged by the same encoding (kind, picture, frame rate,
>    codec family, HDR), never by class alone: a second video whose qualities are of the classes
>    the first already lists keeps its rows.

## 1. Scope and non-goals
- **In scope.**
  - **Overlay.** A generic content script in every http(s) frame and its `about:blank`/`srcdoc`
    children. It tracks `<video>` elements and puts a button over each meaningful one (§2.5, §5.1).
    The panel UI is injected on demand.
  - **Network detection** in the background (§2.4):
    - progressive MP4/M4V files (new)
    - HLS/DASH manifests (M5, now with frame and document identity)
  - **One panel per button with three sources** (§2.6):
    - **File**: a direct MP4, handed off with `add`
    - **Stream**: an HLS/DASH manifest probed for its MP4 qualities, handed off with `addMedia` and
      tracks
    - **Site**: yt-dlp through `probeVideo`/`addVideo`
  - **Popup.** "Videos on this page" groups the same sources per frame (§5.3).
  - **Curated sites.** Their URL table drives naming, canonical addresses and the look-ahead (§3.8,
    §5.5).
  - **Relay** (§3.5): `probeStream`, tracks on `addMedia`, `awaiting` on `add` and `addMedia`, and
    the welcome feature `streams`.
  - **Daemon** (§3.3): MANIFEST probes and adds list and save MP4 only, for every client.
  - **Remuxer** (§4): `internal/remux/mpegts` (new) and TS→MP4 in `internal/remux`.
- **Site knowledge (amends SITE-MEDIA-SPEC §1).** Besides YouTube, the extension recognises the
  public address shapes of the curated sites of §3.8. It uses them only to name the video, to
  canonicalise the address it sends to Downex, and to decide where a look-ahead may run.
  - The shapes are written from the addresses those sites show in the address bar. They are never
    ported from yt-dlp's extractor patterns (`CLEAN-ROOM.md`).
  - From a page the extension reads only the address and the public state of `<video>` elements
    (§2.5): never the page's scripts, player configuration, storage or response bodies.
  - From the network it reads request and response metadata only (URL, method, status, type, frame,
    headers), never bodies.
- **Observe only.** The browser downloads nothing for Downex. Downex never replays the page's
  requests from the browser and never rewrites stream or file URLs (no removal of range, token or
  signature parameters). It never renames forbidden headers to get them past the browser, and never
  loads site rules from a server. The daemon fetches with the headers the browser actually sent (§6).
- **Clean room.** The button and panel keep Downex's own design: the mark (`internal/brand`), the
  overlay's colour tokens (`OVERLAY_CSS`) and Downex's own texts (§5.6). No other download manager's
  strings, icons, placement rules, panel layout or messages are used. This spec describes behaviour
  in its own words.
- **Never offered:**
  - **DRM.** An element whose `mediaKeys` is set, or that fired `encrypted`/`waitingforkey`, gets
    no button. The daemon still refuses protected manifests, inits and yt-dlp results with
    `encrypted` (MEDIA-SPEC §2, SITE-MEDIA-SPEC §3.2).
  - **Live.** An element whose `duration` is `Infinity` gets no button. The daemon's `live`
    refusal (no `EXT-X-ENDLIST`, dynamic MPD, yt-dlp `is_live`) stays authoritative.
  - **Camera and call streams.** An element with a `srcObject` (MediaStream) never gets a button.
  - **Private windows.** The extension is `not_allowed` there.
  - **Other containers.** WebM, Ogg, Matroska, QuickTime/MOV, FLV, MPEG-TS files and audio-only
    files are never listed, and neither are manifest tracks that cannot be saved as MP4.
  - **Script-fetched MP4.** MP4 responses to `xmlhttprequest`/`fetch`/`other` requests are never
    listed as files. They are segments or split audio/video byte ranges of a player that uses Media
    Source Extensions (MSE), and saving one would give a silent, mute or partial video. Such
    players are covered by their manifest (Stream) or by yt-dlp (Site). *Amended 2026-11-18 (SITE-MEDIA-SPEC
    §2.8):* their address and total size (`video/*`, `audio/*`, 200 or 206) are remembered per tab
    (`PlayedMedia`, storage.session, 32 entries, 30 min), never listed and never sent: a
    browser-fetched save uses the player's own address of the same resource (same path, same size).
  - **`blob:` and `data:` URLs** are never handed off.
- **Not in v1** (§9 has the defaults):
  - per-element permalink discovery on feed pages
  - a "refresh address" for expired direct-file downloads
  - content sniffing of direct files by the daemon
  - packed-audio renditions (raw ADTS with ID3 timestamps), and AC-3, E-AC-3, MP3 or MPEG-2 video
    inside TS
  - subtitles
  - frames reachable only with `match_origin_as_fallback` (`blob:`/`data:` frames)
  - media requested by a page's service worker (`tabId` −1)
  - audio-only downloads, transcoding, FFmpeg
  - ad-server host lists
  - DASH single-file (`SegmentBase`) Representations: an M5 limit that stays
    (`internal/mediajob/probe.go:465-476` refuses them, `planDASHTracks` probe.go:322-344 skips them)
- **Platforms.** Windows first. The extension is the same everywhere. Go code builds for darwin and
  linux and runs the same logic, unverified at run time there.

## 2. Architecture and data flow

### 2.1 Components
| Where | Role |
|---|---|
| ext `entrypoints/video.content.ts` (new) | Small tracker in every http(s) frame: meaningful `<video>` elements, element reports, curated dwell (§2.5, §5.5) |
| ext `entrypoints/video-ui.ts` (new, unlisted) | Overlay layer, buttons and merged panel; injected into a frame on demand by the background (§5.1) |
| ext `entrypoints/youtube.content.ts` | Unchanged YouTube overlay, which now also follows the per-site option |
| ext background | File and stream detection per tab, frame and document; redirect map; frame offers; stream probe cache; file, stream and site hand-offs; curated look-ahead; badge (§2.4, §3.6) |
| ext popup / options | Grouped "Videos on this page", quality pickers, per-site toggle; button and hidden-sites options (§5.3, §5.4) |
| `downex-nmhost` | `probeStream` → `ProbeMedia{MANIFEST}`; tracks on `addMedia`; `awaiting` on `add`/`addMedia`; feature `streams` (§3.5) |
| `internal/daemon` | MANIFEST probes and adds MP4 only; legacy `.ts` records; `MediaRunOptions.LegacyTS` (§3.3) |
| `internal/mediajob` | TS tracks planned as MP4 output; early TS check; `ProbeMP4` accepts convertible TS (§3.3, §4.8) |
| `internal/remux/mpegts` (new) | Pure, bounded MPEG-TS demultiplexer (ISO/IEC 13818-1) (§4.2) |
| `internal/remux` | TS→MP4 (H.264, HEVC, AAC) through the existing progressive/fragmented muxer; `ConcatTS` for legacy records (§4) |
| `internal/media` | `TSConvertible` codec rule (§3.4) |
| `internal/sitemedia` | Display names of the curated extractor keys (`label.go` `SiteName`); doc comments that named `mediajob.Probe` (§3.3, §3.8) |
| `internal/testmedia` (new) | Test-only generators: TS muxer, H.264/HEVC parameter sets, ADTS, progressive MP4 (§8) |
| `cmd/downexd/media.go` | MANIFEST probe = `ProbeMP4` + MP4 offer; `MP4Only`/`LegacyTS` per record (§3.3) |
| `cmd/downexd/site.go` | Unchanged: it already calls `ProbeMP4` (site.go:88) and sets `MP4Only` (site.go:213); TS fallbacks pass `checkManifestMP4` (site.go:301) once `ProbeMP4` accepts them |
| `ui/`, `cmd/downex` | Texts: `.mp4` for streams; legacy `.ts` shown as such (§5.7) |

### 2.2 Flows
```
any tab                     background                         downex-nmhost              downexd
 <video> ──meta──────────▶ frame videos (memory)
 traffic ─webRequest─────▶ files + streams per tab/frame/doc (storage.session, headers)
 click Download ─offers──▶ rows: File / Stream / Site (no relay call)
   stream row ─probe─────▶ stream cache ──probeStream────────▶ ProbeMedia{MANIFEST} ──▶ ProbeMP4: MP4 qualities
   site row ───probe─────▶ probe cache ───probeVideo─────────▶ ProbeMedia{SITE} ─────▶ yt-dlp (SITE-MEDIA)
   pick File ────────────▶ file hand-off ─add────────────────▶ Add{BROWSER} ─────────▶ engine → <name>.mp4
   pick Stream ──────────▶ media hand-off ─addMedia+tracks───▶ AddMedia{MANIFEST} ───▶ mediajob → remux → .mp4
   pick Site ────────────▶ video hand-off ─addVideo──────────▶ AddMedia{SITE} ───────▶ RunRanged/Run → .mp4
```
- **A. Direct MP4.**
  1. The page's `<video src>` (or a document showing the file) loads an MP4. `onResponseStarted`
     (types `media`, `main_frame`, `sub_frame`, `object`) classifies the response as a file
     (§2.4), with its total size from `Content-Range`, its first URL from the redirect map, and the
     headers the browser sent.
  2. The user clicks Download. The row "This video · 720p (HD) · MP4 · 48.0 MB" is there at once.
  3. A pick sends `add` with that request's headers (§3.7). With `confirm_browser_downloads` the
     desktop confirm window opens. The engine downloads with ranges and several connections and
     saves `<name>.mp4`.
- **B. Stream.**
  1. The page's player loads an `.m3u8`/`.mpd`, and the M5 detector records it for its frame.
  2. Opening the panel (a user click) probes up to 4 of the frame's streams through `probeStream`.
     The daemon's MP4-only `ProbeMP4` lists one MP4 video per quality class (amendment 2026-10-23),
     MPEG-TS included (it is converted).
  3. A pick sends `addMedia` with `videoTrack`/`audioTrack`. The media job downloads the fragments
     and saves fMP4 tracks as a progressive MP4, or converts TS tracks (§4).
- **C. Curated site** (§3.8).
  1. While the user watches, the top-frame tracker's dwell sends the look-ahead (§5.5), so the yt-dlp
     qualities are usually cached before the click.
  2. The panel shows the Site rows first, with the network rows under "More sources".
  3. A pick sends `addVideo` (SITE-MEDIA-SPEC unchanged).
- **D. Any other page.**
  1. The network rows show at once.
  2. "Look for more qualities" (a click) sends `probeVideo` with the frame's address. For an embedded
     frame the Referer is what the browser's default policy sends for the tab's page
     (`defaultPolicyReferer`: the origin alone across origins, never a fragment), never more.
  3. When the panel has no network row (an MSE player without a visible manifest), the click on the
     Download button itself starts that probe.
- **E. YouTube.** Unchanged: `youtube.content.ts`, look-ahead, site rows. The generic tracker never
  runs on YouTube's hosts, so there is never a second button.

### 2.3 Direct MP4 files are ordinary downloads
A progressive MP4 is already one finished file, so it needs no media job:
- the engine gives ranged multi-connection download, resume, host rules, the progress window and the
  completion dialog
- the daemon's `Add` already takes headers, `filename`, `mime`, `expected_size` and the browser
  confirmation rule
- a media job would add only a remux step that changes nothing

Neither path can renew a network-detected URL whose signature expired (§9 #5). To soften that, the
hand-off uses the first URL of the redirect chain when there is one: a redirector usually issues a
fresh signed URL to the engine's request (§3.7). Chromium and Firefox cannot apply EME to a
progressive file (EME needs MSE), so a listed file is never DRM-protected.

### 2.4 Network detection (background)
- **Listener.** The existing observe-only `webRequest.onResponseStarted` listener (`responseHeaders`,
  never blocking; `background.ts:388-411`, types `MEDIA_REQUEST_TYPES` = `xmlhttprequest`, `media`,
  `other`, `lib/media.ts:22`) gains the types `main_frame`, `sub_frame` and `object`. Each response
  runs through `detectMedia` (`lib/media.ts:112`; streams, M5 unchanged) and then the new pure
  `classifyFile` (`lib/frame-media.ts`). `ResponseDetailsLike` (`lib/media.ts:60`) gains `type`,
  `frameId`, `parentFrameId`, `documentId?`, `frameType?` and `urlClassification?`.
- **Common gates.** From `detectMedia` today: method GET; `tabId ≥ 0`; not incognito;
  `documentLifecycle` absent or `active`; status 2xx or 304; URL without fragment, at most 32 KiB.
  New for both classifiers: `frameType` (Chromium) not `fenced_frame`, and the URL is http(s).
- **File classification** (`classifyFile`). A response is a file only when all of these hold:
  - Its `type` is `media`, `main_frame`, `sub_frame` or `object`. `xmlhttprequest` and `other` are
    never files (§1).
  - Its status is 200 or 206. A 304 refreshes an existing item only.
  - Its Content-Type essence is `video/mp4` or `video/x-m4v`, or it is `application/octet-stream`,
    `binary/octet-stream` or missing while the URL path or the `Content-Disposition` filename
    (`parseContentDisposition`) ends in `.mp4` or `.m4v`.
  - It is never `text/*`, `video/webm`, `video/ogg`, `video/quicktime`, `video/x-matroska`,
    `video/mp2t`, `video/x-flv`, `audio/*` or `application/vnd.apple.mpegurl`, whatever the path
    says.
  - For `main_frame`/`sub_frame` the `Content-Disposition` is not `attachment`: an attachment is a
    browser download, which capture (BROWSER-SPEC §3) decides.
- **Size.**
  - From a 206: the total of `Content-Range: bytes a-b/TOTAL` (decimal, 1–2⁴⁰; `*` = unknown).
  - From a 200: `Content-Length` (1–2⁴⁰).
  - Otherwise unknown (0). The largest known value wins.
- **Redirects.** A new observe-only `onBeforeRedirect` listener (types `media`, `main_frame`,
  `sub_frame`, `object`) maps `requestId` to the first URL of the chain.
  - The map keeps at most 512 entries for 60 s and is cleared on `onCompleted`/`onErrorOccurred`.
  - The file keeps both `requestUrl` (the first URL) and `url` (the final one). The element's
    `currentSrc` (the URL before redirects) matches either.
- **Grouping.** The key is the final URL without fragment, within (tab, frame, document).
  - A response for a known key (the next range request of a `<video>`) only updates `lastSeen`,
    the size and the headers, in memory.
  - Storage is written, and `tabs.get` called, only when something shown changes: a new item, a size
    learned, the matched element. Today every manifest sighting writes the store and calls
    `tabs.get` (`background.ts:394-406`); for files that would be one call per range request.
  - When the query differs but origin + path + known total size are equal, the newer item replaces
    the older one: a server that adds per-request tokens.
- **Frames and documents.** Items record `frameId`, `parentFrameId` and, on Chromium, `documentId`;
  Firefox has no `documentId`.
  - `webNavigation.onCommitted` of a frame other than 0 clears that frame's files and streams; frame
    0 clears the tab, as today (`background.ts:225-238`; today a sub-frame commit only updates
    `PageVideoCounts`). A same-document navigation keeps them.
  - Items with an unknown frame (`frameId < 0`, e.g. a dedicated worker's request where the browser
    gives none) belong to the top frame.
- **A document that is the file.** A `main_frame`/`sub_frame` file is held as pending (at most 32,
  30 s) until that frame's `onCommitted` with the same URL; it is recorded after that commit's
  clearing. A response that never commits (a download, a blocked navigation) is dropped. Without
  this the commit would wipe the item it belongs to.
- **Headers.** The request headers come from the existing `onSendHeaders` store for media requests
  (`mediaRequests`, a `RequestContextStore` with the 60 s TTL, `background.ts:301`, `:377-379`,
  `lib/context.ts:90`). The listener already observes `main_frame`, `sub_frame` and `object`
  (`HEADER_TYPES`, `background.ts:108-113`); the store now records them too. They pass `filterHeaders`
  (`lib/context.ts:197`: `Range`/`If-Range` and hop-by-hop removed) and are kept with the item, so a
  click minutes later
  still has them. The `Cookie` stays only when the browser sent one with that request (§6). The
  headers of the first URL are kept when the chain has one.
- **Store.** `MediaStore` (`lib/media.ts:282`) gains the format `file` (§3.7).
  - Caps: at most 16 files per frame, 50 items per tab (all formats; `MAX_MEDIA_PER_TAB`,
    `lib/media.ts:24`), 100 tabs, and the shared 2 MiB budget (headers dropped first, then the
    oldest items).
  - Kept in `storage.session` and restored after a service-worker restart, as for streams.
- **De-prioritised ("short") files.** Listed last and not counted in the badge, but never hidden:
  - a known size under 2 MiB
  - the matched element's duration under 30 s
  - Firefox's `urlClassification` naming `tracking_ad` for the request
- **Segment-like files** (hidden). The fMP4 segments of native HLS playback arrive as `media`
  requests with `video/mp4`. When 4 or more file items of one frame share origin and directory within
  60 s, they are segments of a stream: they are marked `segment`, never listed and never counted.
  The stream's manifest row covers them. Until the fourth one arrives, such segments are under
  2 MiB and are therefore listed only as short files.
- **Element reports.** The tracker's `downex:frame-videos` (§3.6) is kept in memory per (tab, frame,
  document): at most 16 videos per frame and 32 frames per tab. A frame's commit or the tab's closing
  clears it. Reports label files (height, duration), mark the matched element and drive the badge's
  DRM/live exclusions (§5.3).

### 2.5 Which videos get a button (content)
- **Tracker** (`lib/video-tracker.ts`, run by `video.content.ts`).
  - Capture-phase listeners on `document` for `loadstart`, `loadedmetadata`, `durationchange`,
    `play`, `playing`, `resize`, `emptied`, `encrypted` and `waitingforkey`.
  - At start, the live collection `document.getElementsByTagName('video')`, checked again on media
    events and at most once a second on pointer activity.
  - One `IntersectionObserver` (threshold 0.5) and one `ResizeObserver` for the tracked videos.
  - At most 32 tracked videos per frame, kept in a `WeakMap`.
  - **No subtree `MutationObserver`,** except in the top frame of a compact social site
    (*amended 2026-11-18*, §5.1 "Social sites"): one whose callback only schedules a `check()`
    250 ms later. No layout read outside `requestAnimationFrame`, and only for videos whose button
    is shown (and once per link the pointer enters on those sites, §5.1 "Grid tiles").
- **Meaningful video.** All of these must hold:
  - `isConnected`, and `srcObject === null`. *Amended 2026-11-18 (social sites):* or a
    `srcObject` that is no MediaStream (a camera, a call, a screen has `getTracks`). Facebook's
    players play through Media Source Extensions in a worker, whose `MediaSourceHandle` is the
    element's `srcObject` and leaves `currentSrc` empty (a signed-out check of a public reel,
    2026-11-18); such a video is an MSE player (`mse: true`) like a `blob:` one. Before, no
    Facebook video got a button.
  - `currentSrc` is http(s) or `blob:`, or `readyState ≥ HAVE_METADATA`
  - rendered at least 200 × 112 CSS px and at least 50 % inside the viewport
  - `duration` is not `Infinity`
  - `mediaKeys === null`, and no `encrypted`/`waitingforkey` event has been seen on it
  - not decorative: not (`muted` and `loop` and no `controls` and duration under 30 s), which
    excludes background loops on landing pages
  - the button is allowed for the tab's site (§5.4)

  DRM and live are checked again at click time: `mediaKeys` can be set after the button appeared.
- **Decorative videos** (*amended 2026-11-18, in-page button on social sites*).
  - **Curated hosts.** In a top frame whose own address is on a curated site's host (§3.8, any
    path: feeds, profiles and home pages included; `siteOfHost`), no video is decorative. There,
    short, muted, looping clips without native controls are the site's videos (TikTok, Instagram, X
    and Pinterest play them that way). The rule reads the frame's own address, not the tab's: a
    cross-origin sub-frame cannot read the top address, and these sites play their videos in their
    own top frames. *Fix 2026-11-18:* an embedded frame on a curated host keeps the usual rule. A
    landing page's background loop is often such a player (`player.vimeo.com/video/<id>?background=1`,
    muted, looping, no controls); it is quiet (below), not a full video that counts in the badge.
  - **Other hosts.** A decorative video that would otherwise be meaningful (and is neither
    protected nor live) is *quiet*. It stays out of `meaningful`, so reports, the badge and the
    look-ahead do not count it. It still gets the mark alone (32 × 32 px), shown only while the
    pointer is over the video (§5.1 "Quiet videos"). The frame asks for the overlay UI for a quiet
    video only after the first trusted `pointermove` in the frame: a landing page nobody points at
    costs nothing more than before.
- **Shadow DOM.** Media events are not composed, so players whose `<video>` sits in a shadow root
  are not seen by the document listeners.
  - When the background has seen media or manifest traffic for a frame that reported no video, it
    sends that frame `downex:frame-media-seen` (at most once per 5 s per frame).
  - The tracker then runs a bounded deep scan: at most 5,000 elements and 16 shadow levels.
    Chromium uses `browser.dom.openOrClosedShadowRoot(el)`, Firefox `el.openOrClosedShadowRoot()`.
    It attaches its listeners to each video it finds.
- **Element report.** `downex:frame-videos` goes to the background at most once a second per frame,
  only when something changed. It lists each tracked video (meaningful or not) with its `src` (http(s)
  only), size, duration, DRM and live state (§3.6), never page text.
- **On demand UI.** The tracker itself draws nothing and sends nothing until its frame has a video
  that is meaningful apart from the site rule (or a quiet one the pointer moved over, above). Then
  it asks `downex:overlay-state` (the site rule,
  §5.4) and, when buttons are allowed, `downex:video-ui`, on which the background injects
  `/video-ui.js` into that frame (§5.1). Pages without a video therefore parse only the tracker and
  cost the background nothing.

### 2.6 One panel, three sources
- **Sources of a frame's panel.** The background answers `downex:frame-offers` (§3.6) from memory,
  without the relay. A panel shows only what its own frame may know (§6.2): its own items, and the
  tab's title and host only in the top frame (a sub-frame gets neither: "Video", "Don't show on this
  site"). The popup, an extension page, lists every frame's items.
  - **files**: the sender frame's own files (the top frame's: its own too, not the other frames')
  - **streams**: the frame's own manifests
  - **site**: the curated page (or embed) video of the frame, or a generic target for the frame's
    address
  - an **element file** when the element's `currentSrc` is an http(s) URL whose path ends in `.mp4`
    or `.m4v` and no recorded file matches it (a response the extension did not see: cached, from a
    service worker, or loaded before the extension started). It has no recorded headers and an
    unknown size.
- **Matching.** A file matches the clicked element when its `requestUrl` or `url` (without
  fragment) equals the element's `currentSrc`. For a `blob:` element, the frame's streams count as
  "This video" when the frame has stream items. *Amendment 2026-11-18 (a grid of previews, 48
  playlists all marked):* with several streams in the frame, only those whose directory holds the
  segments the page fetched last (the clicked element's trace carries the directories from the
  page's resource timing, within 10 s of its newest segment, at most 8; else the frame's recorded
  segment files), or a playlist at least two levels deep above that directory, are matched; without such an association, every stream is, as
  before (`playingSegmentDirs`, `streamOfSegments` in `lib/frame-offers.ts`).
- **Order of rows** (`lib/offer-rows.ts`, shared by the panel and the popup):
  1. Site qualities, once the site source has a list (curated pages, YouTube, or after "Look for
     more qualities").
  2. Stream qualities: the frame's streams, matched first, then newest. Each stream lists one
     quality per class (amendment 2026-10-23). Their tracks are merged and deduplicated by the same
     encoding, (kind, picture, frame rate rounded, codec family, HDR) (`qualityKey`; tracks of
     unknown picture by kind, frame rate, codec family and name), keeping the first stream's track.
     So a master playlist and the variant the player loaded give one list, while a different video
     whose qualities are of the same classes (another frame's, an ad) keeps its own rows.
  3. Files matching the element ("This video").
  4. The frame's other files, largest first.
  5. A collapsed group of short files. (Until the 2026-10-23 review the top frame also listed the
     other frames' items here as "Other videos on this page (n)"; they are now in the popup only.)

  Once the Site source lists tracks, rows 2–5 move into a collapsed "More sources (n)". A file whose
  exact byte size equals a Site track's size is a duplicate and is dropped.
- **Default row** (focused, and sent by the popup's Download without a choice): the matched file,
  else the site's default, else the first stream quality.
- **Probing.** Opening the panel is a user click:
  - Up to 4 of the frame's streams are probed (matched first, then newest), through the background's
    stream cache (§3.7).
  - The site source is probed at once on curated pages (usually answered from the look-ahead's
    cache) and when the panel has no network row. Elsewhere it waits for "Look for more qualities".
  - A frame with no row at all says "Getting video info…" while the site probe runs.
- **Live updates.** While the panel is open it asks `downex:frame-offers` again every 2 s, so a
  stream or file that arrives later (the player started after the click) appears. Rows keep their
  focus and selection by key.

## 3. Contracts

### 3.1 Proto
No new message or field. `ProbeMediaRequest{origin MANIFEST, manifest_url, headers, page_url}`,
`AddMediaRequest.video_track/audio_track/page_url/title`, `AddRequest.expected_size/mime/filename` and
`MediaInfo` already carry everything (`proto/downex/v1/downex.proto`: `MediaInfo` :56,
`AddMediaRequest` :190, `ProbeMediaRequest` :211). Step 0 changes comments only, then regenerates
`internal/rpc` with buf (`buf.gen.yaml`; `buf`, `protoc-gen-go` and `protoc-gen-connect-go` are in
`%USERPROFILE%\go\bin`; protoc-gen-go embeds no comments, so only Go doc comments change):
- `MediaInfo.container` (:65, today `"mp4" | "ts" | ""`): `"mp4"` for every new download; `"ts"`
  only on legacy records (§3.3); `""` unknown yet.
- `AddMediaRequest.filename` (:194, today "the daemon appends .mp4/.ts"): "the daemon appends .mp4".

### 3.2 Error codes
There are no new codes. Existing codes gain these meanings:
- `media_unsupported`:
  - a manifest without an MP4-savable video ("No MP4 video in this stream")
  - an audio-only manifest
  - a TS whose program has no H.264/HEVC video or no AAC (ADTS) audio, found at probe time from
    `CODECS` or after the first fragment (§4.8)
  - several access units in one PES, a sample-rate change, and the other `ErrUnsupported` rows of
    §4.7
  - an HLS track whose data is neither TS nor fMP4 (packed audio, §9 #4), found when its first media
    fragment is sniffed (§4.8)
- `encrypted`: a scrambled TS packet or PES, or a SAMPLE-AES stream type, found by `CheckTS` or
  during conversion.
- `media_invalid`: TS that does not parse (no PAT/PMT in the first fragment, sync lost beyond the
  resync window, a pass-2 mismatch) and the `ErrMalformed` rows of §4.7.

These already map without daemon changes: `media.ErrEncrypted` → `encrypted` and anything wrapping
`errors.ErrUnsupported` (as `remux.ErrUnsupported` does, `internal/remux/remux.go:73`) →
`media_unsupported` (`mediaErrorCode`, `internal/daemon/errors.go:87-110`); `remux.ErrMalformed` →
`media_invalid` by its text "remux: malformed input" (`mediaMessageCodes`, errors.go:117-123; the
daemon does not import remux). So `mpegts`'s own sentinels must reach the daemon wrapped as remux's,
and mediajob must keep wrapping with `%w`.

### 3.3 Daemon behaviour
- **MANIFEST probes are MP4-only for every client** (browser, CLI, desktop).
  - `cmd/downexd/media.go`'s `Probe` (media.go:22-29, today `mediajob.Probe`) calls
    `mediajob.ProbeMP4(ctx, tf, sel, sitemedia.ManifestTracksFor(sel.VideoTrack, sel.VideoKey))`
    (signature unchanged: `internal/mediajob/probe.go:134`; `ManifestTracksFor` is `ManifestTracks`
    with a named variant the class rule leaves out in place of its class's, 2026-10-23 review). Tracks that cannot be
    saved as MP4 are not listed: WebM Representations, codecs outside the MP4 lists, TS tracks whose
    `CODECS` fail `media.TSConvertible`, SegmentBase Representations, fMP4 without init.
  - **TS candidates** (Lane B, `planHLSTracks` probe.go:242, whose `planOne` today keeps only fMP4
    with init): a media playlist whose plan container is `ts` (`hlsContainer`,
    `internal/media/plan.go:83`: a TS EXT-X-MAP or `.ts`-like segment names) or `unknown` without
    EXT-X-MAP (assumed TS, `assumedContainer` probe.go:605), when `media.TSConvertible(track.Codecs)`
    holds. A playlist whose first segment path ends in `.aac`, `.ac3`, `.ec3` or `.mp3` is packed audio
    (§9 #4) and is not a candidate.
  - **A second offer round.** `ManifestTracks` keeps one video per quality class (amendment
    2026-10-23: video-only, frame rate, H.264 > HEVC > AV1, bandwidth, picture, size; videos
    without a picture all kept; `ManifestTracksFor` keeps a named one in place of its class's)
    before anything is planned. When planning refuses a track for its container or codecs, ProbeMP4
    calls `offer` once more on the candidates without the refused ones and plans only the newly
    offered tracks (at most two rounds), so a quality whose best variant cannot be saved falls back to
    its next variant instead of disappearing.
  - The answer's `Container` is `"mp4"`, and the default is the first listed video (ManifestTracks
    order: the best class first, H.264 preferred within a class).
  - A presentation without such a video is `media_unsupported` ("No MP4 video in this stream"), an
    audio-only one included (§9 #1).
  - `mediajob.Probe` (probe.go:50) is removed together with its callers: `cmd/downexd/media.go:23`,
    `cmd/downexd/site_test.go:513` and the mediajob tests (`identity_test.go`, `mediajob_test.go`).
    `resolve` stays: `Run` uses it.
  - Cost: ProbeMP4 fetches every offered media playlist (≤ 4 at once, `probeParallel` probe.go:116),
    where `Probe` fetched only the selected ones (§9 #15).
  - **Measured tracks (0.7.1).** An HLS video or muxed track without `BANDWIDTH` (above all a bare
    media playlist's single track) is measured (`describeTracks`, `internal/mediajob/probesize.go`):
    at most 4 tracks per probe (the default video first), at most 4 requests per track, one at a
    time on one connection, with the headers the job's own segment fetches carry, within
    min(8 s, half the time left to the probe's deadline). The first, middle and last-but-one media
    segments are sampled (`Range: bytes=0-0`, or `bytes=0-524287` of the first when its picture or
    codecs are undeclared); a HEAD only after a successful answer that named no length; a 429/503
    ends the measuring. Size: the sum of byte ranges or samples, else bytes per second × duration;
    always `SizeApprox` (the MP4 written differs). The first segment's start (behind any junk prefix,
    §4.2) gives width, height, frame rate, `CODECS` (only when `TSConvertible`) and a name
    ("1080p (Full HD)", `media.QualityName`; a name the declared picture gave gains the measured
    frame rate); when it shows a start the early check refuses (`remux.CheckTS`: scrambled, unconvertible),
    nothing more of that stream is requested. Failures only drop the measurement. A look-ahead
    (SITE-MEDIA-SPEC §5.6) measures nothing (`ProbeMP4Playlists`).
- **Pairing.** A TS video is paired only with TS audio (or none, when muxed). An fMP4 video is paired
  only with fMP4 audio. A video-only track is listed when the presentation has an audio track it can
  be paired with, or no audio track at all (a silent video); otherwise it is not listed.
- **AddMedia (MANIFEST)** (`internal/daemon/media.go:229`). The probe above runs before anything is
  recorded. `video_track` and `audio_track` are resolved against this probe's tracks (MEDIA-SPEC §7:
  ids of separate calls are resolved against the later manifest). The record's `Container` is
  `"mp4"` and its name ends in `.mp4`; `mediaFileName` (media.go:557) already replaces
  `.ts`/`.m3u8`. `mediaFromProbe` (media.go:458, today `"mp4"` or `"ts"`) accepts only `"mp4"`.
- **runMedia (MANIFEST)** (media.go:624).
  - `MediaRunOptions.LegacyTS = rec.Media.Container == "ts" && rec.Media.Checkpoint != nil` (the
    legacy rule below).
  - `cmd/downexd/media.go` sets `mediajob.Options{MP4Only: !LegacyTS, LegacyTS: LegacyTS}` (today it
    sets neither, media.go:31-36).
  - A non-legacy run that returns any container but `"mp4"` fails with `media_unsupported`, and its
    output and part folder are removed. This is the SITE rule (`runMedia`, media.go:748-757), now for
    both origins.
- **Legacy records.** Downloads stored before this change have container `"ts"` and a `.ts` name.
  - **With a checkpoint** (paused or failed midway; its part folder `<name>.ts.dxparts` is derived from
    the name, `MediaPartsDir` media.go:133): `LegacyTS`. They keep the byte-for-byte join
    (`remux.ConcatTS`) and their `.ts` name; they are not renamed or converted.
  - **Without a checkpoint** (never started, awaiting confirmation, queued, or restarted:
    `restartMedia` media.go:832 clears the checkpoint and does **not** probe): nothing on disk belongs
    to the name yet (runMedia's own rule, media.go:644-665). The run stores `Container: "mp4"` and
    reserves `mediaFileName(<stored name>, "", "", "mp4")` with `freeName` before it starts, then runs
    as an MP4 download (`MP4Only`). So a restart turns a legacy record into an MP4 download.
- **SITE manifest fallback** (SITE-MEDIA-SPEC §3.11). `ProbeMP4` now accepts convertible TS, so pages
  whose only formats are MPEG-TS HLS (expected on some Twitch and Dailymotion pages; unverified) are
  offered too. The saved file is still one `.mp4`. `cmd/downexd/site.go` needs no change;
  `TestSiteManifestFallback` and `TestSiteManifestFallbackOffersMP4Only`
  (`cmd/downexd/site_test.go:458`, `:497`), which assert that TS is refused, are rewritten (Lane B).
- **Confirm (MANIFEST).** Unchanged (`selectMediaTracks` media.go:913, called from
  `confirm.go`); the container stays `"mp4"`. A legacy record awaiting confirmation becomes MP4 at
  its first run (above).
- **Category.** Video, because every output now has a video track.
- **`Add` (direct MP4)** is unchanged:
  - the requested `filename` wins over `Content-Disposition` (the engine chooses a name only when
    none was given, `internal/engine/engine.go:57-59`, `chooseName` :424)
  - a cross-origin redirect drops `Cookie`/`Authorization` (`internal/transport/redirect.go:46-51`)
  - `mime` `video/mp4` selects the Video category
  - `expected_size` comes from the item
  - the `confirm_browser_downloads` rule applies
  - capture rules do not apply, since this is an explicit action, like the context menu

### 3.4 Go contracts between lanes
```go
// internal/media/tsconv.go (Step 0, frozen)
// TSConvertible reports whether every codec an RFC 6381 CODECS value names is one the TS→MP4
// remuxer converts: avc1/avc3 (H.264), hvc1/hev1 (HEVC), mp4a.40.2/mp4a.40.5/mp4a.40.29/mp4a.40.1
// (AAC carried as ADTS). "" (unknown) is true: CheckTS decides after the first fragment (§4.8).
func TSConvertible(codecs string) bool

// internal/remux (Step 0 declares; Lane A implements). Input exists (remux.go:53); Step 0 adds
// Breaks and Language only.
type Input struct {
	Kind      Kind
	Container string   // "ts" or "fmp4"
	// Init: for TS, mediajob never sets it (an EXT-X-MAP section is the first entry of Fragments,
	// trackState.input, internal/mediajob/run.go:152-163); when set it is read as a leading file, as
	// concatTS does today (remux.go:224-245). Breaks never count it.
	Init      string
	Fragments []string
	// Breaks are the indexes into Fragments of fragments preceded by a discontinuity
	// (media.Fragment.Discontinuity = EXT-X-DISCONTINUITY or a skipped EXT-X-GAP, and every TS
	// EXT-X-MAP entry after the first): sorted, unique, each in 1..len(Fragments)-1. TS only.
	Breaks []int
	// Language is the track's language (BCP 47 or ISO 639-2/T) for mdhd; "" = und. Used only when
	// the stream itself names none.
	Language string
}
// Assemble: TS inputs now produce "mp4" (§4.1). The signature is unchanged.
func Assemble(ctx context.Context, inputs []Input, outPath string) (container string, err error)
// ConcatTS is the legacy byte-for-byte join of one TS input into outPath (created, synced; removed
// on failure or cancellation), the behaviour Assemble had for one TS input before 2026-10-23.
// Step 0 exports it over the existing unexported concatTS/copyTSFile (remux.go:224-279) and the
// create/flush/sync/remove wrapper of Assemble (remux.go:142-172), and Step 0's Assemble keeps
// calling the same code for one TS input (output "ts") until Lane A replaces that branch.
func ConcatTS(ctx context.Context, in Input, outPath string) error
// CheckTS inspects the start of a track's transport stream: the EXT-X-MAP section (if any)
// followed by the first media fragment, at most MaxCheckTSBytes. It returns nil when Assemble
// can convert a track of kind k:
//   - an error wrapping media.ErrEncrypted for a scrambled packet or PES of a stream it would
//     keep, or a SAMPLE-AES stream type (0xDB, 0xCF, 0xC1) for its role
//   - ErrUnsupported when the first program lacks what k needs (video and muxed: H.264 or HEVC;
//     audio: AAC ADTS; muxed audio present but not AAC ADTS) or uses a limit of §4.7
//   - ErrMalformed when no PAT and PMT are found in the bytes
// Step 0's stub returns nil.
func CheckTS(prefix []byte, k Kind) error
const MaxCheckTSBytes = 1 << 20

// internal/remux/mpegts (Lane A; new, pure; imports only internal/media for ErrEncrypted)
type StreamType uint8 // ISO/IEC 13818-1 Table 2-34 (H.264 0x1B, HEVC 0x24, AAC ADTS 0x0F, ...)
type Stream struct{ PID uint16; Type StreamType; Language string }
type Program struct{ Number, PMTPID, PCRPID uint16; Streams []Stream }
func Probe(prefix []byte) (Program, error) // first program of the first PAT; ErrMalformed, media.ErrEncrypted
type Unit struct {
	PID          uint16
	Type         StreamType
	PTS, DTS     int64 // 33-bit raw values; -1 = absent
	Data         []byte // valid until the next call to Next
	File         int    // index of the file the unit started in
	RandomAccess bool   // the adaptation field's random_access_indicator
}
type Demuxer struct{ /* ... */ }
// NewDemuxer reads the files in order (each starts at offset 0) and yields the PES units of
// the streams keep accepts, as they complete. It keeps no unit beyond MaxPESBytes.
func NewDemuxer(files []string, keep func(Program, Stream) bool) *Demuxer
func (d *Demuxer) Next(ctx context.Context) (Unit, error) // io.EOF at the end
func (d *Demuxer) Program() (Program, bool)               // the program in effect
func (d *Demuxer) Close() error
var ErrMalformed, ErrUnsupported error // mpegts's own sentinels; remux wraps them as its ErrMalformed/ErrUnsupported

// internal/mediajob (Step 0 adds the fields to Options, mediajob.go:152-207; Lane B implements;
// withDefaults, mediajob.go:238, fills CheckTS and ConcatTS with the remux functions, as it does
// for Assemble)
type Options struct {
	// ... existing fields ...
	// LegacyTS keeps a record stored before 2026-10-23 as a byte-for-byte .ts: a single TS track
	// is joined with ConcatTS instead of converted, and CheckTS is not run (§3.3). It ignores MP4Only.
	LegacyTS bool
	// CheckTS replaces remux.CheckTS (tests); ConcatTS replaces remux.ConcatTS (tests).
	CheckTS  func(prefix []byte, k remux.Kind) error
	ConcatTS func(ctx context.Context, in remux.Input, outPath string) error
}

// internal/daemon (Step 0 adds the field to MediaRunOptions, media.go:100-109; Lane B implements).
// MediaRunOptions is not converted to mediajob.Options by type conversion (cmd/downexd/media.go
// copies fields), so the addition compiles alone; SiteMedia.Run (cmd/downexd/site.go) ignores it.
type MediaRunOptions struct {
	// ... existing fields ...
	LegacyTS bool // a "ts" record with a checkpoint (stored before 2026-10-23): keep the .ts join
}
```
- `MediaProbe.Container` (media.go:82) and the `MediaJobs.Run` doc comment (media.go:51-61) read
  "mp4" (and "ts" for legacy runs only). `daemon.MediaProbe` stays field-for-field equal to
  `mediajob.Info` (converted directly in `cmd/downexd/media.go:27` and `site.go:95`).
- `outputContainer`/`expectedContainer` (`internal/mediajob/probe.go:615-660`) are pure functions of
  the track containers today; Lane B gives them the legacy flag rather than a second copy.
- `internal/testmedia` (Lane A) is a non-test package that only tests import. The integration gate
  checks that `go list -deps ./cmd/... ./ui` never contains it.
- Until Lane A lands, the real `Assemble` still concatenates one TS input and returns `"ts"`, which
  the new non-legacy rule refuses after assembly. Lane B's tests therefore use fakes for `Assemble`
  and `CheckTS` (`testOptions`/`fakeAssembler`, `internal/mediajob/helpers_test.go:214-264`); the real
  TS path is first run by the integration step.

### 3.5 Native messaging (BROWSER-SPEC §2, protocol 1, additive)
- **`welcome.features`** (`cmd/downex-nmhost/protocol.go:57`, today `["video", "prefetch"]`,
  asserted with length 2 in `video_test.go:131-133`) becomes `["video", "prefetch", "streams"]`. The
  extension sends `probeStream` and track-carrying `addMedia` only to a relay that lists `streams`.
  An older relay ignores unknown fields (`decodeFields` is plain `json.Unmarshal`, protocol.go:158),
  so `addMedia` tracks would silently fall back to the default pick.
- **Without `streams`**, the stream rows show one "Download" (the daemon's default pick, `addMedia`
  without tracks; the desktop confirm window's picker still applies), and their quality says "Update
  Downex to choose a stream quality."
- **`probeStream`** (new; no side effects, safe to retry):
  - Request: `{id, type, manifestUrl, headers?, pageUrl?, deadlineMs?}`. The fields are validated
    like `addMedia`'s: `validate.URL` (http(s), ≤ 32 KiB), `validHeaders`, and `pageUrl` as there.
  - The relay sends `ProbeMedia{origin: MANIFEST, manifest_url, headers, page_url}` through the
    existing `daemonBackend.probeMedia` (relay.go:499; autostart because `prefetch` is false). The
    daemon ignores `page_url` for MANIFEST probes (`ProbeMedia`, `internal/daemon/media.go:345`).
  - It runs on the video pool (`request.video()` protocol.go:73 gains `typeProbeStream`;
    `maxVideoInFlight` 4, `maxVideoWaiting` 16, relay.go:27-30), capped by `videoTimeout` (30 s,
    relay.go:37; `deadlineMs` only shortens it, `deadline` protocol.go:473), with the 200 ms
    `responseMargin`. It autostarts downexd: it is always a user's click.
  - Answer: `{type: "videoInfo", site: "", title, durationMs, format: "hls"|"dash", container: "mp4",
    selectedVideo, selectedAudio, tracks: [...]}`, encoded by the existing `videoInfo()`
    (protocol.go:529). At most `maxVideoTracks` 200 tracks and `maxVideoResponseBytes` 256 KiB
    (protocol.go:29-30); never a URL.
  - Errors are `rejected` with `reason` (`encrypted`, `live`, `media_unsupported`, `media_invalid`,
    `http_status`, `network`, `tls`, ...), as for every request.
  - The log keeps the manifest URL as scheme, host and path only, and the header count.
- **`addMedia`** (`parseAddMedia` protocol.go:308) gains optional `videoTrack` and `audioTrack`
  (`validate.TrackID`, `internal/validate/validate.go:61`, as `parseAddVideo` uses it at
  protocol.go:418-421; missing or `""` = the default pick), sent as
  `AddMediaRequest.video_track/audio_track`. The log records the track ids. It stays on the add path
  (`addTimeout` 15 s, relay.go:33), although the daemon now plans every offered media playlist before
  it records (§3.3, §9 #15).
- **`added`** (the answer to `add` and `addMedia`, relay.go:243-264) gains `awaiting`: true when the
  download state is `AWAITING_CONFIRMATION`, as `addVideo` already does (relay.go:287-290). The
  extension treats a missing `awaiting` as unknown ("Sent to Downex").
- `add` is otherwise unchanged (15 s, `deadlineMs`, `minAddTime`).
- **Parts (2026-11, `MEDIA-SPEC.md` §8).** `addMedia` takes `clipStartMs`/`clipEndMs` like `addVideo`
  (SITE-MEDIA-SPEC §3.5), sent only to a relay that lists `clips` (`NativeClient` answers
  `outdated_downex` "Update Downex to save part of a video." instead of sending one to an older relay,
  which would save the whole video). `probeStream` answers carry `chapters: []` (streams have none).

### 3.6 Extension runtime messages (validated in `isExtensionMessage`, Step 0)
Every listener is non-`async` and uses `sendResponse` with `return true` (SITE-MEDIA-SPEC §3.6). The
background never acts on a URL or key from a content script or the popup without looking it up again
in its own per-tab store, bound to the sender's frame (§6).
- **Content script → background:**
  - `downex:frame-videos {videos: FrameVideoMeta[]}`: not answered; at most 16 entries and 64 KiB.
  - `downex:video-ui {}` → `{ok}`: inject `/video-ui.js` into the sender's frame. The target is
    `{tabId, frameIds: [frameId]}`, plus `documentIds: [documentId]` on Chromium. At most once per
    document, and refused for private tabs or a sender without a tab.
  - `downex:overlay-state {}` → `{button: boolean, host: string}`: whether buttons may show for
    the sender's tab, from `videoButton` and the hidden sites matched against the host of
    `sender.tab.url` (a sub-frame cannot read the top host itself).
  - `downex:frame-offers {src: string, mse: boolean}` → `Outcome<FrameOffers>`. `src` is the
    clicked element's `currentSrc` when http(s) (≤ 32 KiB), else `""`. It is used only to match
    items. *Amended 2026-11-18:* an optional `post` (an http(s) address, ≤ 32 KiB) is the clicked
    feed video's own address (§5.1 "Feed videos"). The background uses it only when it parses as a
    curated video (not a `network` site) on the sender frame's own origin and the frame's own
    address is not a curated video. The answer's `site` is then that video with `linked: true`.
    A frame on a curated host whose own address is no video and that names no usable `post` gets
    no `site` but `feed: <site>` (the curated site's name, for the panel's text; *fix 2026-11-18*).
    Only an address that lists videos counts as such a frame (§5.1 "Feed videos", "Which pages are
    feeds"); a curated host's other addresses keep the generic target.
  - `downex:stream-probe {key}` → `Outcome<VideoInfoResult>`. Local code `outdated_downex` when the
    relay lacks `streams`; `expired` when the key no longer resolves.
  - `downex:offer-add {key, videoTrack?, audioTrack?}` → `Outcome<AddedResult>`: a file key →
    `add` (§3.7); a stream key → `addMedia` (tracks only with `streams`); an element-file key → `add`
    without recorded headers.
  - `downex:page-video-probe {url}` / `downex:page-video-add {url, videoTrack, audioTrack}`
    (existing) are generalised. `url` must parse with the curated table (§3.8) on the sender frame's
    own origin, or equal the sender frame's own address without fragment (a generic target, key
    `page:<address>`). The headers are as in SITE-MEDIA-SPEC §6.3: never a Cookie for generic
    targets, and for embeds the Referer is the default-policy Referer of `sender.tab.url` (the
    origin alone across origins; never its path, query or fragment).
  - `downex:page-video-prefetch {url}` (existing) is generalised to curated sites with
    `lookAhead: true` (§5.5).
  - `downex:set-site-hidden {hidden: boolean}` → `{hidden}`: adds or removes the exact host of
    `sender.tab.url` in `videoOptions.hiddenSites` (§5.4).
- **Background → content script** (`tabs.sendMessage(tabId, m, {frameId})`):
  - `downex:frame-media-seen {}`: media traffic for a frame that reported no video (§2.5).
  - `downex:show-panel {}` → `{shown: boolean}` (*amended 2026-11-18*, §5.1 "Menu"): the user
    chose "Show Downex download panel" on a `<video>` of that frame. The tracker answers `shown:
    false` when it holds no video right-clicked in the last 60 s that is still in the page.
- **Popup → background** (`fromExtensionPage`):
  - `downex:tab-media {tabId}` → `TabMediaResponse` (today `{items: MediaEntry[], videos:
    PageVideoEntry[]}`, `lib/messages.ts:187-190`) becomes:
    - `items: StreamEntry[]` (below): each stream with its `key`, `frame: 'top'|'embed'` and `probe?`;
      the add state stays `sending?`/`result?` of `MediaEntry`
    - `files: FileEntry[]`
    - `hiddenHere: boolean`, `host: string`
  - `downex:tab-stream-probe {tabId, url}` → `Outcome<VideoInfoResult>`.
  - `downex:add-media {tabId, url, videoTrack?, audioTrack?}`: the existing message gains the tracks.
  - `downex:add-file {tabId, url}` → `Outcome<AddedResult>`: `url` is looked up among the tab's file
    items, never fetched as given.
  - `downex:set-site-hidden {tabId, hidden}` → `{hidden}`: the popup's "This tab" toggle.
- **Types** (`ext/src/lib/offer-types.ts`, Step 0, frozen):
```ts
export const UI_PROTOCOL = 1;                 // tracker ↔ video-ui handshake version
export const VIDEO_UI_SCRIPT = '/video-ui.js'; // WXT unlisted script output (entrypoints/video-ui.ts)
export interface FrameVideoMeta {
  id: number;                 // tracker-local, 1..2^31-1
  src: string;                // currentSrc when http(s) (no fragment, ≤ 32 KiB), else ''
  mse: boolean;               // currentSrc is blob:
  width: number; height: number;           // rendered CSS px, rounded
  videoWidth: number; videoHeight: number; // intrinsic size, 0 unknown
  durationMs: number;         // -1 unknown, -2 live (Infinity)
  drm: boolean; visible: boolean; decorative: boolean; meaningful: boolean;
}
export type OfferKey = string; // ^[fse][0-9]{1,12}$ : f = file item, s = stream item, e = element file
export interface FileOffer {
  key: OfferKey; display: string;  // host + last path segment (the full URL is not sent to the page UI)
  size: number; sizeKnown: boolean; filename: string;
  frame: 'this' | 'other'; matched: boolean; short: boolean;
  width: number; height: number; durationMs: number; // from the matched element, else 0 / -1
  add?: VideoTaskState<AddedResult>;
}
export interface StreamOffer {
  key: OfferKey; format: 'hls' | 'dash'; display: string;
  frame: 'this' | 'other'; matched: boolean;
  probe?: VideoTaskState<VideoInfoResult>; add?: VideoTaskState<AddedResult>;
}
export interface SiteOffer {
  key: string; site: string; kind: SiteVideoKind | 'page'; pageUrl: string; curated: boolean;
  probe?: VideoTaskState<VideoInfoResult>; add?: VideoTaskState<AddedResult>;
}
export interface FrameOffers {
  title: string; topHost: string;
  files: FileOffer[]; streams: StreamOffer[]; site?: SiteOffer;
  features: { streams: boolean; video: boolean };
  drm: boolean; live: boolean; // the clicked element is protected / live
}
export interface FileEntry extends Omit<FileOffer, 'frame'> { url: string; frame: 'top' | 'embed' }
// MediaEntry (lib/media.ts:156) keeps its fields; Lane D must not change its shape.
export interface StreamEntry extends MediaEntry {
  key: OfferKey; frame: 'top' | 'embed';
  probe?: VideoTaskState<VideoInfoResult>;
}
```
  *Amended 2026-11-18 (feed videos, §5.1):* `SiteOffer.linked?: true` (the Site source is the
  clicked video's own post, found from its links, not the frame's address) and
  `FrameOffers.feed?: true` (a curated site's page that is no video, and no post was found for the
  clicked video). Both are additive and missing from an older background. The tracker's
  `TrackedVideo` (tracker ↔ overlay UI, the same build) gains `quiet?: boolean` (§2.5); it is not
  part of `FrameVideoMeta`, so the reports do not change.
  `FileEntry.url` exists for the popup only (an extension page, never a web page). `offer-types.ts`
  imports `VideoTaskState` from `messages.ts`, `AddedResult`/`VideoInfoResult` from `protocol.ts`,
  `SiteVideoKind` from `site-video.ts` and `MediaEntry` from `media.ts` as types only; `messages.ts`
  imports its types back (type-only cycles are fine).

### 3.7 Extension modules and stored data
- **`MediaItem`** (`lib/media.ts:135`) gains:
  - `format: ItemFormat`, a new `type ItemFormat = MediaFormat | 'file'`. `MediaFormat`
    (`lib/media.ts:11`, `'hls' | 'dash'`) stays as it is: `ui-format.ts` (`formatLabel`, :203) and the
    popup read it, and `MediaEntry`/`toMediaEntry` stay stream-only.
  - `frameId`, `parentFrameId`, `documentId?`
  - `requestUrl?` (the first URL of a redirect chain)
  - `size` (0 = unknown), `mime?`, `filename?` (Content-Disposition)
  - `reqType` (`media` | `main_frame` | `sub_frame` | `object` | `xmlhttprequest` | `other`)
  - `short`, `segment`
  - `seq`: a per-tab sequence number for keys

  `parseItem` accepts the file format, and items stored before this change read with frame 0.
- **Keys.** `f<seq>`/`s<seq>`/`e<id>`. A key is valid only for the sender's tab, and, for a
  sub-frame, only for items of that frame (and document, on Chromium). The top frame may use every
  item of its tab.
- **`lib/file-handoff.ts` (new)** builds `add` fields from a file item:
  - **`url`**: `requestUrl` when present (§2.3), else `url`.
  - **`headers`**: the item's recorded headers for that URL through `filterHeaders`, plus a
    `User-Agent` when missing. There is no `cookies.getAll` fallback for files (§6, §9 #3), unlike
    `createHandOff`/`createMediaHandOff` (`background.ts:415-430`). When no headers were recorded, the
    hand-off has no Cookie and a default-policy `Referer` of the frame (`defaultPolicyReferer`,
    `lib/context.ts:385`). *Correction 2026-11-18 (review; owner: "never rely on login"):* a File
    or Stream row chosen in a video's panel or in the popup (`addFile`, `addMedia`, `probeStream`)
    carries no `Cookie` and no `Authorization`, recorded or read with `cookies.getAll`, unless "Use
    my sign-in for videos that need it" (SITE-MEDIA-SPEC §6.3) is on; a download the browser started
    and a link sent from the context menu (`createHandOff`) keep theirs.
  - **`referrer`**: the frame's address (http(s)).
  - **`filename`**, the first that applies, then cleaned (`cleanTitle`, `lib/media-handoff.ts:36`,
    then cut to 200 characters), with `.m4v` replaced by `.mp4` and `.mp4` appended when missing:
    - the `Content-Disposition` filename (`parseContentDisposition`, `lib/capture-firefox.ts:75`)
    - the URL's last path segment when meaningful: not generic (`video`, `index`, `file`, `media`,
      `download`, `play`, `stream`), not only digits, not 24 or more hex digits
    - the tab title
    - `video`
  - **`mime`**: `video/mp4`. **`size`**: the item's size or −1. **`deadlineMs`**: 15000.
  - History source: `video-file`. One hand-off per (tab, key) at a time (`MediaSends`,
    `lib/media-handoff.ts:194`), with the result kept 5 min (`MEDIA_RESULT_KEEP_MS`, :172).
- **Element files.** The same, without headers, with `url` = the element's `src`.
- **`lib/stream-probes.ts` (new).** The `probeStream` cache:
  - key `${storeId}\n${manifestUrl}`
  - at most 2 probes in flight in the whole browser
  - answers and refusals (`encrypted`, `live`, `media_unsupported`, `media_invalid`) kept 10 min, at
    most 50 entries; other errors not kept
  - `deadlineMs` 28000
  - the headers are those of the stream hand-off (`buildAddMediaFields`, M5 rules unchanged)

  A refused stream disappears from the panel and the badge, and the popup marks it "Protected",
  "Live" or "No MP4 video".
- **`lib/frame-offers.ts` (new)** builds `FrameOffers` for a sender (§2.6) and resolves keys.
- **Badge and look-ahead modules:**
  - `lib/video-handoff.ts`: `senderTarget`, `findPageVideos` and `PageVideoCounts` read the curated
    table.
  - `lib/video-prefetch.ts`: `prefetchVideo`, `stillWatched`, `lookAheadRequest` and the throttle are
    per site. The signatures of `lookAheadAllowed(o)` (:31), `prefetchVideo(url)` (:36), `DwellView`,
    `dwellKey` and `PrefetchDwell` (:45-119) stay as they are: `youtube.content.ts` and the new
    tracker (Lane E) call them. The site rule of §5.4 is added by the callers, not by a new
    parameter.
  - `lib/site-video.ts`: the curated table (§3.8). `parseSiteVideo(url)` keeps its signature.
- **`lib/protocol.ts`:**
  - `FEATURE_STREAMS = 'streams'`
  - `ProbeStreamFields {manifestUrl, headers, pageUrl?, deadlineMs?}`, a `probeStream` member of
    `RequestParams` (:124) and `ResultFor` (:206) answered by `videoInfo`
  - `NativeClient.probeStream` (`NativeClient` :554). `VIDEO_REQUESTS` (:527, the requests that need
    `video`) gains it; the `deadlineMs` rewrite (:720-731) and the daemon-unavailable recheck (:788)
    cover it. Without `streams` it is not sent: the local code `outdated_downex` with "Update Downex
    to choose a stream quality."
  - `AddMediaFields` (:50) gains `videoTrack?` and `audioTrack?`, which are dropped when `streams` is
    missing
  - `added.awaiting` is parsed for every `added` (`AddedResult` :144 already has it for `addVideo`)
- **`lib/history.ts`.** `HandoffSource` (`lib/history.ts:13`) gains `video-file` (Step 0, together with
  its label "Video file" in `ui-format.ts`'s `SOURCES`, :66, a `Record<HandoffSource, string>` that
  would not typecheck otherwise). Stream adds stay `media`, also from the overlay.
- **Exports one extension lane uses from the other's files** keep their signatures (only Step 0 may
  change them):
  - Lane E uses from Lane D's files: `MediaEntry`, `MediaFormat`, `mediaStorageKey`, `toMediaEntry`
    (`lib/media.ts`; `media-panel.ts:4`, `ui-format.ts:4`), `parseHistory`/`HandoffRecord`
    (`history.ts`), `VIDEO_RETRY_MS`/`VIDEO_RETRY_LIMIT_MS` (`video-handoff.ts:45-47`;
    `video-panel.ts:28`), `cleanTitle`, the `video-prefetch.ts` exports above and `parseSiteVideo`.
  - Lane D uses from Lane E's files: `isVideoRetry` (`ui-format.ts`; `video-handoff.ts:34`).
- **Options** (`storage.local` `videoOptions`, `lib/options-form.ts:163-198`, Step 0):
  - `videoButton` (existing) is reworded "Show a Download button over videos" (today "… over YouTube
    videos", `entrypoints/options/index.html:81`). It covers YouTube and every other site.
  - `hiddenSites: string[]` (new, default `[]`): host patterns as in the capture rules' excluded
    hosts (`normalizeHostPattern` `lib/rules.ts:376`, `hostMatches` :333; exact host or
    `*.domain`). At most 500 entries, each at most 253 characters; invalid entries are dropped when
    read.
  - `buttonAllowed(options, host)` is true when `videoButton` is on and no hidden pattern matches.
  - Stored options without the new field read it as empty. `VideoOptions` literals elsewhere
    (`background-prefetch.test.ts:264-273`, `video-handoff.test.ts:355-395`) gain the field in Step 0.

### 3.8 Curated video sites (`lib/site-video.ts`)
- **Purpose.** These sites get a name, a canonical address and, where marked, the look-ahead.
  Everything about their videos still comes from yt-dlp.
- **Source.** Each shape below was written from the addresses the site shows in the browser. Hosts
  match exactly (lower-cased). Paths match segment by segment; a trailing slash and extra trailing
  segments are ignored where noted. Query parameters are dropped unless named.
- **Key and address.** `key` = `<site>:<id>` (Twitch clips `twitch:clip:<slug>`). The `pageUrl` sent
  to Downex is the canonical address below.
- **Kinds.** `watch`, `clip` or `embed`. Embeds never get a look-ahead. Their probe carries the
  default-policy `Referer` of the tab's page (`siteTarget`, §3.6). *Amended 2026-11-18:* also
  `post` (a post page that may hold a video: Facebook, Bluesky, LinkedIn, 9GAG, Imgur, Telegram,
  Tumblr and Threads posts, Pinterest pins) and `link` (a share address that redirects to a video,
  such as `vm.tiktok.com/<code>`: sent as it is, for context-menu links; a tab never stays on it).
  Neither ever gets a look-ahead.
- **Resolver** (*amended 2026-11-18*). Every row is resolved with yt-dlp (`resolver: 'yt-dlp'`)
  except Threads, Snapchat, Rumble and Kick VODs (`resolver: 'network'`), where yt-dlp has no
  working path today (its reader of any page answers "Unsupported URL" for Threads; its Snapchat
  reader fails to read the page; every Rumble address, page or embed, answered HTTP 403 (bot
  protection) and two of two public Kick VODs answered "NOT_FOUND" in the real checks of
  2026-11-18, while the browser plays them; Kick clips list their MP4 and stay `yt-dlp`). A `network` video is never sent to Downex as a page: no Site source in the panel and no
  "Look for more qualities" (the panel's File and Stream rows, from the browser's own traffic,
  still work; with none, its empty text reads "Downex can't read this site's videos yet. Play the
  video, then try again."), no popup page-video row, no "Find the video on this page" for that
  address, no badge count, no look-ahead, and a context-menu link to it records that text as the
  hand-off's failure. Content scripts asking `downex:page-video-probe`/`-add` about it get
  `unsupported_page` with that text.
- **Hosts** (*amended 2026-11-18*). `siteOfHost(host)` names the site whose table row lists that
  host, whatever the path: the one owner of the host column. `parseSiteVideo` dispatches on it,
  and so do the decorative exemption (§2.5) and the feed rule (§5.1). In the store build YouTube's
  hosts name no site, as before.

| Site (name) | Hosts | Shapes → id | Kind | Canonical `pageUrl` | Look-ahead |
|---|---|---|---|---|---|
| `youtube` (YouTube) | as SITE-MEDIA-SPEC §5.1 | unchanged | unchanged | unchanged | watch, shorts |
| `vimeo` (Vimeo) | `vimeo.com`, `www.vimeo.com` | `/<id>`, `/<id>/<hash>`, `/channels/<c>/<id>`, `/groups/<g>/videos/<id>`, `/showcase/<s>/video/<id>`; id `[0-9]{1,12}`, hash `[0-9a-f]{6,20}` | watch | `https://player.vimeo.com/video/<id>[?h=<hash>]` (*amended 2026-11-18*; was `https://vimeo.com/<id>[/<hash>]`, which yt-dlp answers anonymously with "only works when logged-in" today, while the player's address lists a public video's qualities; a private or embed-restricted video answers HTTP 401 there, and a FairPlay one is refused as `encrypted`, as before) | yes |
| | `player.vimeo.com` | `/video/<id>` (query `h` kept) | embed | `https://player.vimeo.com/video/<id>[?h=<hash>]` | no |
| `x` (X) | `x.com`, `twitter.com`, and `www.`/`mobile.` of both | `/<user>/status/<id>[/video/<n>]`, `/i/status/<id>`, `/i/web/status/<id>`; id `[0-9]{1,20}` | watch | `https://x.com/i/status/<id>` | yes |
| | same | `/i/broadcasts/<id>`; id `[A-Za-z0-9]{8,20}`; key `x:broadcast:<id>` (*2026-11-18*) | watch | `https://x.com/i/broadcasts/<id>` | no (it may be live) |
| `facebook` (Facebook) | `facebook.com`, `www.`, `m.`, `web.` | `/watch?v=<id>`, `/watch/?v=<id>`, `/videos/<id>`, `/<page>/videos/<id>`, `/<page>/videos/<slug>/<id>`; id `[0-9]{1,20}` | watch | `https://www.facebook.com/watch/?v=<id>` | no |
| | same | `/reel/<id>` | watch | `https://www.facebook.com/reel/<id>` | no |
| | same | `/watch/live/?v=<id>`, `/video.php?v=<id>` (*2026-11-18*) | watch | `https://www.facebook.com/watch/?v=<id>` | no |
| | same | `/<page>/posts/<post>`, `/groups/<g>/posts/<post>`, `/groups/<g>/permalink/<post>`; post `[0-9]{1,20}` or `pfbid[A-Za-z0-9]{10,80}`; key `facebook:post:<post>` (*2026-11-18*) | post | `https://www.facebook.com/<page>/posts/<post>` (groups: `https://www.facebook.com/groups/<g>/posts/<post>`) | no |
| | same | `/permalink.php?story_fbid=<post>&id=<n>`, `/story.php?story_fbid=<post>&id=<n>`; `n` `[0-9]{1,20}`, each parameter once (*2026-11-18*) | post | `https://www.facebook.com/permalink.php?story_fbid=<post>&id=<n>` | no |
| | `fb.watch` | `/<code>`; code `[A-Za-z0-9_-]{4,32}` | watch | `https://fb.watch/<code>/` | no |
| `instagram` (Instagram) | `instagram.com`, `www.instagram.com` | `[/<user>]/p/<code>`, `[/<user>]/reel/<code>`, `/reels/<code>`, `[/<user>]/tv/<code>`; code `[A-Za-z0-9_-]{5,64}` (the `/<user>` forms of reel and tv: *2026-11-18*) | watch | `https://www.instagram.com/<p, reel or tv>/<code>/` (`reels` → `reel`) | no |
| `tiktok` (TikTok) | `tiktok.com`, `www.`, `m.` | `/@<user>/video/<id>`; user `[A-Za-z0-9._-]{1,64}` (`-`: *2026-11-18*), id `[0-9]{1,25}` | watch | `https://www.tiktok.com/@<user>/video/<id>` | yes |
| | same | `/embed/v2/<id>`, `/embed/<id>` | embed | `https://www.tiktok.com/embed/v2/<id>` | no |
| | same; `vm.tiktok.com`, `vt.tiktok.com` | `/t/<code>` (on the first hosts), `/<code>` (on the short hosts); code `[A-Za-z0-9]{5,16}`; key `tiktok:link:<code>` (*2026-11-18*) | link | `https://www.tiktok.com/t/<code>/`, `https://vm.tiktok.com/<code>/` or `https://vt.tiktok.com/<code>/`, as found | no |
| `dailymotion` (Dailymotion) | `dailymotion.com`, `www.` | `/video/<id>`; id `[A-Za-z0-9]{5,12}` | watch | `https://www.dailymotion.com/video/<id>` | yes |
| | same | `/embed/video/<id>` | embed | the same canonical address | no |
| | `geo.dailymotion.com` | `/player.html?video=<id>`, `/player/<player>.html?video=<id>`; player `[A-Za-z0-9]{1,20}` (*2026-11-18*) | embed | the same canonical address | no |
| | `dai.ly` | `/<id>` | watch | the same canonical address | yes |
| `reddit` (Reddit) | `reddit.com`, `www.`, `old.`, `new.` | `/r/<sub>/comments/<id>[/<slug>...]`; sub `[A-Za-z0-9_]{2,32}`, id `[a-z0-9]{2,12}` | watch | `https://www.reddit.com/r/<sub>/comments/<id>/` | no (*amended 2026-11-18*, was yes) |
| | same | `/r/<sub>/s/<code>`; code `[A-Za-z0-9]{5,16}`; key `reddit:link:<code>` (*2026-11-18*) | link | `https://www.reddit.com/r/<sub>/s/<code>` | no |
| | `v.redd.it` | `/<id>`; id `[a-z0-9]{5,20}`; key `reddit:v:<id>` (*2026-11-18*) | watch | `https://v.redd.it/<id>` | no |
| `twitch` (Twitch) | `twitch.tv`, `www.`, `m.` | `/videos/<id>`; id `[0-9]{1,15}` | watch | `https://www.twitch.tv/videos/<id>` | yes |
| | same | `/<channel>/clip/<slug>`, and on `m.twitch.tv` `/clip/<slug>` (*2026-11-18*); slug `[A-Za-z0-9_-]{4,100}` | clip | `https://clips.twitch.tv/<slug>` | yes |
| | `clips.twitch.tv` | `/<slug>` | clip | the same | yes |
| | `player.twitch.tv` | `/?video=v<id>` | embed | `https://www.twitch.tv/videos/<id>` | no |
| `streamable` (Streamable) | `streamable.com`, `www.` | `/<id>`, `/e/<id>` (embed); id `[a-z0-9]{4,12}`, not one of `about`, `blog`, `careers`, `contact`, `documentation`, `help`, `login`, `pricing`, `privacy`, `settings`, `signup`, `terms`, `upload` | watch / embed | `https://streamable.com/<id>` | watch only |

*Amended 2026-11-18: more curated sites.* Written from the addresses these sites show in the
address bar and in their share buttons (CLEAN-ROOM). None gets a look-ahead: they resolve on a click
only (Tumblr often answers HTTP 403 to yt-dlp, which Downex reports and never works around; Rumble
always did and is a `network` site; the others were not checked for being cheap and anonymous).

| Site (name) | Hosts | Shapes → id (key) | Kind | Canonical `pageUrl` |
|---|---|---|---|---|
| `bluesky` (Bluesky) | `bsky.app` | `/profile/<handle>/post/<id>`; handle `[A-Za-z0-9.:_-]{3,253}`, id `[a-z0-9]{10,16}` (`bluesky:<handle>/<id>`) | post | `https://bsky.app/profile/<handle>/post/<id>` |
| `pinterest` (Pinterest) | `pinterest.com`, `www.` | `/pin/<id>`; id `[0-9]{1,25}` | post | `https://www.pinterest.com/pin/<id>/` |
| | `pin.it` | `/<code>`; code `[A-Za-z0-9]{4,16}` (`pinterest:link:<code>`) | link | `https://pin.it/<code>` |
| `linkedin` (LinkedIn) | `linkedin.com`, `www.` | `/posts/<slug>`; slug `[A-Za-z0-9._%-]{3,300}` (`linkedin:<slug>`) | post | `https://www.linkedin.com/posts/<slug>` |
| | same | `/feed/update/urn:li:activity:<id>`; id `[0-9]{1,25}` (`linkedin:activity:<id>`) | post | `https://www.linkedin.com/feed/update/urn:li:activity:<id>/` |
| `kick` (Kick) | `kick.com`, `www.` | `/<channel>/videos/<uuid>`; uuid 8-4-4-4-12 hex digits; `resolver: 'network'` | watch | `https://kick.com/<channel>/videos/<uuid>` |
| | same | `/<channel>?clip=<id>`, `/<channel>/clips/<id>`; id `[A-Za-z0-9_]{4,64}` (`kick:clip:<id>`) | clip | `https://kick.com/<channel>?clip=<id>` |
| `bilibili` (Bilibili) | `bilibili.com`, `www.`, `m.` | `/video/<id>`; id `BV[A-Za-z0-9]{10}` or `av[0-9]{1,12}`; query `p` (`[1-9][0-9]{0,3}`, a part of a multi-part video) kept above 1, any other `p` ignored (`bilibili:<id>[:p<n>]`) | watch | `https://www.bilibili.com/video/<id>/[?p=<n>]` |
| | `b23.tv` | `/<code>`; code `[A-Za-z0-9]{4,16}` (`bilibili:link:<code>`) | link | `https://b23.tv/<code>` |
| `vk` (VK) | `vk.com`, `www.`, `m.`; `vkvideo.ru`, `www.` | `/video<owner>_<id>`; owner `-?[0-9]{1,20}`, id `[0-9]{1,20}` | watch | `https://vk.com/video<owner>_<id>` |
| `odysee` (Odysee) | `odysee.com` | `/@<channel>/<name>`; channel `[A-Za-z0-9._%-]{1,100}` and name `[A-Za-z0-9._%-]{1,200}`, each optionally followed by `:<claim>` (`[0-9a-f]{1,40}`) (`odysee:@<channel>/<name>`) | watch | `https://odysee.com/@<channel>/<name>` |
| `bitchute` (BitChute) | `bitchute.com`, `www.` | `/video/<id>`, `/embed/<id>` (embed); id `[A-Za-z0-9_-]{6,20}` | watch / embed | `https://www.bitchute.com/video/<id>/` |
| `ninegag` (9GAG) | `9gag.com` | `/gag/<id>`; id `[A-Za-z0-9]{5,12}` | post | `https://9gag.com/gag/<id>` |
| `imgur` (Imgur) | `imgur.com`, `www.`, `m.` | `/<id>`; id `[A-Za-z0-9]{5,8}` with a digit or an upper-case letter (Imgur's own pages are lower-case words) | post | `https://imgur.com/<id>` |
| | same | `/gallery/<slug>`; slug `[A-Za-z0-9_-]{5,200}` (`imgur:gallery:<slug>`) | post | `https://imgur.com/gallery/<slug>` |
| `telegram` (Telegram) | `t.me` | `/<channel>/<n>`; channel `[A-Za-z][A-Za-z0-9_]{3,31}`, n `[0-9]{1,10}` (`telegram:<channel>/<n>`) | post | `https://t.me/<channel>/<n>` |
| `tumblr` (Tumblr) | `<blog>.tumblr.com` (blog `[a-z0-9-]{1,32}`, not `www`) | `/post/<id>[/<slug>]`; id `[0-9]{1,20}` (`tumblr:<blog>/<id>`) | post | `https://<blog>.tumblr.com/post/<id>` |
| | `www.tumblr.com` | `/<blog>/<id>[/<slug>]`; blog not one of Tumblr's own pages `blog`, `dashboard`, `explore`, `following`, `likes`, `post`, `search`, `settings`, `tagged` | post | the same |
| `rumble` (Rumble) | `rumble.com`, `www.` | `/v<id>-<slug>.html`, `/v<id>`, `/embed/v<id>` (embed); id `[a-z0-9]{4,10}`, slug `[A-Za-z0-9_-]{1,200}` (`rumble:v<id>`); `resolver: 'network'` | watch / embed | `https://rumble.com/` plus the address's own first segment; embeds `https://rumble.com/embed/v<id>/` |
| `threads` (Threads) | `threads.com`, `threads.net`, `www.` of both | `/@<user>/post/<code>`; user `[A-Za-z0-9._]{1,30}`, code `[A-Za-z0-9_-]{5,64}` (`threads:<code>`); `resolver: 'network'` | post | `https://www.threads.com/@<user>/post/<code>` |
| `snapchat` (Snapchat) | `snapchat.com`, `www.` | `/spotlight/<id>`; id `[A-Za-z0-9_-]{10,200}`; `resolver: 'network'` | watch | `https://www.snapchat.com/spotlight/<id>` |

*Amended 2026-11-18: audio sites* (`SITE-MEDIA-SPEC.md` §3.13, owner decision). Written from the
addresses these sites show in the address bar (CLEAN-ROOM). Their pages have no video: downexd offers
their audio (`.m4a` or `.mp3`). Kind `audio`: the popup's page row shows from the address alone (no
video ever plays there), there is no in-page button and no look-ahead.

| Site (name) | Hosts | Shapes → id (key) | Kind | Canonical `pageUrl` |
|---|---|---|---|---|
| `soundcloud` (SoundCloud) | `soundcloud.com`, `www.`, `m.` | `/<user>/<track>`, `/<user>/<track>/s-<secret>` (a private share); user `[a-z0-9_-]{1,64}`, track `[A-Za-z0-9_-]{1,200}`, secret `[A-Za-z0-9]{4,32}`; user not one of SoundCloud's own pages `charts`, `discover`, `feed`, `jobs`, `messages`, `mobile`, `notifications`, `pages`, `people`, `pro`, `search`, `settings`, `signin`, `stations`, `stream`, `tags`, `terms-of-use`, `upload`, `you`; track not a profile tab `albums`, `comments`, `followers`, `following`, `likes`, `popular-tracks`, `reposts`, `sets`, `spotlight`, `tracks` (`soundcloud:<user>/<track>`) | audio | `https://soundcloud.com/<user>/<track>[/s-<secret>]` |
| | `on.soundcloud.com` | `/<code>`; code `[A-Za-z0-9]{4,32}` (`soundcloud:link:<code>`) | link | `https://on.soundcloud.com/<code>` |
| `bandcamp` (Bandcamp) | `<artist>.bandcamp.com` (artist `[a-z0-9-]{1,63}`, not `www`) | `/track/<slug>`; slug `[A-Za-z0-9_-]{1,200}` (`bandcamp:<artist>/<slug>`); an album (`/album/<slug>`) is several tracks and is not matched | audio | `https://<artist>.bandcamp.com/track/<slug>` |
| `mixcloud` (Mixcloud) | `mixcloud.com`, `www.`, `m.` | `/<user>/<mix>`; user `[A-Za-z0-9_-]{1,64}` not one of Mixcloud's own pages `about`, `categories`, `dashboard`, `discover`, `genres`, `live`, `messages`, `notifications`, `player`, `pro`, `search`, `select`, `settings`, `tag`, `upload`, `widget`; mix `[A-Za-z0-9_-]{1,200}` (*amended 2026-11-18:* or, for a title in another script, which the tab's URL keeps percent-encoded, `([A-Za-z0-9_-]|%XX){1,600}` whose octets decode to UTF-8 text without `/`, `?`, `#`, `%`, `\` or a control character; the key and page URL keep the encoded form) not a profile tab `favorites`, `followers`, `following`, `history`, `listens`, `playlists`, `posts`, `reposts`, `shows`, `stream`, `uploads` (`mixcloud:<user>/<mix>`) | audio | `https://www.mixcloud.com/<user>/<mix>/` |

*Amended 2026-11-18 (social sites, §5.1 "Social sites"): stories and embeds.* Stories from the
address bar of Instagram's story viewer; embeds from the frame addresses these sites' own "Embed"
codes put in a page, which the address bar shows when such a frame is opened on its own
(CLEAN-ROOM). None gets a look-ahead. Each embed has the key of the post it shows, so the caches are
shared with the post's own page.

| Site | Hosts | Shapes → id (key) | Kind | Canonical `pageUrl` |
|---|---|---|---|---|
| `instagram` | `instagram.com`, `www.` | `/stories/<user>/<id>`; user `[A-Za-z0-9._-]{1,100}` (not `highlights`), id `[0-9]{1,25}` (`instagram:story:<id>`). Stories need a sign-in: the click's second request carries it (SITE-MEDIA-SPEC §6.3) | post | `https://www.instagram.com/stories/<user>/<id>/` |
| | same | `/stories/highlights/<id>`; id `[0-9]{1,25}` (`instagram:highlight:<id>`): several videos, listed by the entry picker (SITE-MEDIA-SPEC §3.12) | post | `https://www.instagram.com/stories/highlights/<id>/` |
| | same | `[/<user>]/p/<code>/embed`, `…/embed/captioned` (`instagram:<code>`) | post | `https://www.instagram.com/p/<code>/` |
| | same | `[/<user>]/reel/<code>/embed`, `/tv/<code>/embed`, `…/embed/captioned` (`instagram:<code>`) | embed | `https://www.instagram.com/<reel or tv>/<code>/` |
| `x` | `platform.twitter.com` | `/embed/Tweet.html?id=<id>`; id `[0-9]{1,20}`, once (`x:<id>`) | post | `https://x.com/i/status/<id>` |
| `facebook` | `facebook.com`, `www.`, `m.`, `web.` | `/plugins/video.php?href=<address>`, `/plugins/post.php?href=<address>`: the `href` parameter (once) is a Facebook address of this table other than a plugin's, and the row is that address's (its key and canonical address; `video.php`: kind `embed`) | embed / post | that address's |
| `reddit` | `embed.reddit.com` | `/r/<sub>/comments/<id>[/<slug>...]` as on `www.reddit.com` (`reddit:<id>`) | post | `https://www.reddit.com/r/<sub>/comments/<id>/` |
| `pinterest` | `assets.pinterest.com` | `/ext/embed.html?id=<id>`; id `[0-9]{1,25}`, once (`pinterest:<id>`) | post | `https://www.pinterest.com/pin/<id>/` |
| `bluesky` | `embed.bsky.app` | `/embed/<handle>/app.bsky.feed.post/<id>`; handle and id as on `bsky.app` (`bluesky:<handle>/<id>`) | post | `https://bsky.app/profile/<handle>/post/<id>` |

- **Compact sites** (*2026-11-18*, `compactSite`): `tiktok`, `instagram`, `facebook`, `x`,
  `reddit`, `pinterest`, `threads`, `snapchat`, `linkedin`, `bluesky`, `tumblr`, `ninegag`,
  `imgur`, `vk`, `telegram`. A frame on any of their hosts (`siteOfHost`) gets the compact button
  (§5.1 "Social sites"); the top frame also the late-mount observer and grid tiles.
- **Tiles** (*2026-11-18*, `videoTile`): the curated addresses whose shape always names a video,
  so that a picture tile linking to one is a video's tile: TikTok `/@<user>/video/<id>`, Instagram
  `[/<user>]/reel/<code>`, `/reels/<code>` and `[/<user>]/tv/<code>` (not `/p/`, which may be
  photos), X `/<user>/status/<id>/video/<n>` (a media grid's video; `/photo/<n>` is not
  matched), Facebook `/reel/<id>`, `/videos/…`, `/watch/?v=<id>` and `/video.php?v=<id>`, and VK
  `/video<owner>_<id>`; on a compact site, never a `network` site's. The tile's target is that
  address's row.
- **Never matched:**
  - a Twitch channel page `/<channel>` (live)
  - X, Facebook or Reddit timelines, profiles and search pages
  - TikTok and Instagram feeds
  - any address with user information
  - any site's private messages (`/direct/…`, `/messages/…`, `/i/chat`, `/chat/…` and the like
    are neither videos nor feeds, §5.1 "DMs")
  - *2026-11-18:* TikTok photo posts (no video), and Twitch channel addresses with a `clip` query
    (not seen in the address bar). *Amended 2026-11-18 (social sites):* Instagram stories and
    highlights were in this list while Downex had no sign-in for pages; since the automatic
    sign-in (SITE-MEDIA-SPEC §6.3) they are the rows above.
- **Look-ahead "no".** Instagram and Facebook: anonymous extraction there mostly answers
  `auth_required` and invites rate limits on the user's address. They resolve on click only.
  *Amended 2026-11-18:* Reddit too (anonymous yt-dlp runs answer "Account authentication is
  required" today), every `post` and `link`, X broadcasts, and every site added on 2026-11-18.
- **Display names** (`SITE_NAMES`, `lib/site-video.ts:20`, today `{youtube: 'YouTube'}`): YouTube,
  Vimeo, X, Facebook, Instagram, TikTok, Dailymotion, Reddit, Twitch, Streamable. The daemon keeps
  yt-dlp's `extractor_key` display rule (`sitemedia.SiteName`, `internal/sitemedia/label.go:15`:
  "Youtube" → "YouTube", any other key as it is); Lane B adds, as a data table there, the keys whose
  own spelling is not the site's name: `Twitter` → "X", `TwitchVod`/`TwitchClips` → "Twitch",
  `FacebookReel` → "Facebook". The extension shows its own `SITE_NAMES` for curated rows either way.
  *Amended 2026-11-18:* also `TwitterBroadcast` → "X", `KickClip`/`KickVOD` → "Kick", `BiliBili` →
  "Bilibili", `NineGag` → "9GAG", `TelegramEmbed` → "Telegram", `LBRY` → "Odysee", `FoxNews` →
  "Fox News", `FacebookPluginsVideo` → "Facebook", `InstagramStory` → "Instagram", `Soundcloud` →
  "SoundCloud" (audio sites); and a page read
  by yt-dlp's readers of any page (`Generic`, `HTML5MediaEmbed`) is named by its host
  (`sitemedia.PageSiteName`: lower case, no port, no leading `www.`), not by the key.
  *Amended 2026-11-19:* the keys that are a site's name of several words run together are spelled
  out: `ApplePodcasts` → "Apple Podcasts", `GooglePodcasts` → "Google Podcasts", `AppleTrailers` →
  "Apple Trailers", `ABCNews` → "ABC News", `CBSNews` → "CBS News", `NBCNews` → "NBC News", `SkyNews`
  → "Sky News", `WashingtonPost` → "Washington Post", `NYTimes` → "New York Times", `BBCCoUk` → "BBC",
  `TedTalk` → "TED", `DailyMotion` → "Dailymotion", `RumbleEmbed` → "Rumble", `BandcampAlbum` →
  "Bandcamp", `SoundcloudPlaylist`/`SoundcloudSet` → "SoundCloud", `PinterestCollection` →
  "Pinterest"; any other key is still shown as it is.
- **Types** (Step 0): `SiteName` (`lib/site-video.ts:5`) becomes the union of the ten names;
  `SiteVideoKind` (:6, `watch | shorts | embed | live | clip`) already holds the curated kinds;
  `SiteVideo` gains `lookAhead: boolean` (YouTube: true for `watch` and `shorts`, as
  `prefetchVideo` decides today). *Amended 2026-11-18:* `SiteName` adds `bluesky`, `pinterest`,
  `linkedin`, `kick`, `bilibili`, `vk`, `odysee`, `bitchute`, `ninegag`, `imgur`, `telegram`,
  `tumblr`, `rumble`, `threads` and `snapchat`, named in `SITE_NAMES` Bluesky, Pinterest, LinkedIn,
  Kick, Bilibili, VK, Odysee, BitChute, 9GAG, Imgur, Telegram, Tumblr, Rumble, Threads and
  Snapchat; `SiteVideoKind` adds `post` and `link`; `SiteVideo` gains `resolver: 'yt-dlp' |
  'network'`. *Audio sites (2026-11-18):* `SiteName` adds `soundcloud`, `bandcamp` and `mixcloud`
  (SoundCloud, Bandcamp, Mixcloud) and `SiteVideoKind` adds `audio`.
- **Tests.** Every row has positive cases (each host and shape) and negative cases: wrong host,
  bad id, extra segments where none are allowed, channel and feed pages, user information,
  `javascript:` and non-http schemes.

### 3.9 CLI and desktop
- **`downex media <manifest>`** lists only MP4 qualities, one video per quality class; `--list`
  shows QUALITY (the standard name, `media.TrackName`) and PICTURE (width x height, frame rate)
  columns (amendment 2026-10-23). `--video ID` also reaches a variant the class rule leaves out.
  TS variants
  are listed with their codecs, an audio-only manifest is `media_unsupported`, and outputs are
  `.mp4`. The help texts that say "(or .ts)" (`cmd/downex/media.go:34`, `:63`) say ".mp4".
- **Desktop.** The Add dialog's stream hint (`ui/frontend/src/lib/components/AddDialog.svelte:534`,
  today ".mp4 or .ts" for manifests) reads "The extension (.mp4) is added for you." in both modes.
  `containerLabel('ts')` (`ui/frontend/src/lib/media.ts:33`) stays "MPEG-TS" for legacy records; the
  mock's TS streams become MP4 (`mock.ts:481`, `:641`; `mock.test.ts:381`, `:442`). The progress
  window keeps its assembling text while converting, conversion included (§9 #9): "Assembling…" for
  streams and "Merging…" for site downloads (`ui/frontend/src/lib/progress.ts:80`, unchanged).
- **Parts (2026-11).** `downex:offer-add` and `downex:page-video-add` may carry `clipStartMs` and
  `clipEndMs` (integers of ms within 168 h; an end needs a start and comes after it, or is 0 = to the
  end); the background passes them to `addMedia`/`addVideo`. `FrameOffers.features.clips` says whether
  the relay takes a part (MEDIA-SPEC §8); the YouTube overlay, which asks for no frame offers, asks
  `downex:video-features` (answered `{clips}` from the connected relay's welcome, false while none is
  connected; content scripts only).

## 4. TS → MP4 remuxer (`internal/remux`, `internal/remux/mpegts`)
Implemented from ISO/IEC 13818-1 (ITU-T H.222.0: packets, adaptation field, PSI with CRC-32, PES,
PTS/DTS), ISO/IEC 14496-10 Annex B and 14496-15 (avc1/avcC, hvc1/hvcC), ISO/IEC 23008-2 (HEVC NAL
types), ISO/IEC 13818-7 and 14496-3 (ADTS, AudioSpecificConfig), 14496-14 (esds), ISO/IEC 14496-12
(ISO-BMFF) and RFC 8216 §3.2, §4.3.2.3 (TS segments, discontinuities). The demuxer is Downex's own,
with no third-party TS library. `github.com/Eyevinn/mp4ff` (MIT, already a dependency) builds the
codec configuration and sample-entry boxes. Every mp4ff parser call runs under `recover()`, as
`parseInit` does.

### 4.1 Inputs, outputs and roles
- **`Assemble` accepts:**
  - one TS input of any kind
  - a TS video input plus a TS audio input
  - the fMP4 combinations as before

  TS mixed with fMP4 stays `ErrUnsupported`. TS inputs always produce `"mp4"`: progressive, or
  fragmented above `maxProgressiveSamples` (the existing rule).
- **Roles by kind:**
  - `KindMuxed`: the first video stream and the first audio stream of the first program, in PMT order
  - `KindVideo`: video only; audio streams in the same TS are ignored, because a separate rendition
    was chosen
  - `KindAudio`: audio only
  - A muxed input without audio gives a video-only MP4.
- **Track ids.** Output track ids are 1..N, video first. The `mdhd` language comes from the PMT's
  ISO 639 language descriptor, else `Input.Language`, else `und`.
- **`ftyp`, `moov` before `mdat`, `co64`/largesize, edit lists:** as the fMP4 path (MEDIA-SPEC §7).
- **Output.** `<name>.dxpart`, synced, removed on failure or cancellation (the existing wrapper).

### 4.2 Transport stream demultiplexing (`mpegts`)
- **Packets.** 188 bytes with sync byte `0x47`.
  - Each fragment file starts at its first packet, `mpegts.FindStart` (0.7.1): offset 0 when the file
    starts as a transport stream (sync bytes at 0 and 188, and not a GIF), else the first offset below
    64 KiB with sync bytes at it and the next four packet starts. What precedes it is a junk prefix
    (some CDNs serve every segment behind a complete PNG/JPEG/GIF/WebP image, as `image/png`), which
    the demuxer, the early check and `ConcatTS` leave out; fragment files keep the bytes as received.
    A file without such a start is read from 0 and fails as before. A trailing partial packet is
    dropped.
  - On a missing sync byte, a resync scan of at most 64 KiB looks for an offset with `0x47` there
    and at +188 and +376. This also skips data placed before the first packet. Failing that:
    `ErrMalformed`.
  - Packets with `transport_error_indicator` and null packets (PID 0x1FFF) are skipped.
  - Adaptation fields are length-checked; `random_access_indicator` is noted and PCR ignored.
- **Continuity.** Per PID:
  - a duplicate packet (same counter, with payload) is dropped
  - a gap discards the PES being assembled on that PID (damaged) and is counted
  - the expectation is reset at each file start
- **Scrambling.** `transport_scrambling_control ≠ 0` on a kept PID → `media.ErrEncrypted`.
- **PSI.** PAT (PID 0, table 0x00) and PMT (table 0x02) sections are reassembled across packets
  (`pointer_field`).
  - `section_length` at most 1021, at most 4 KiB per section in reassembly; sections with a wrong
    CRC-32/MPEG-2 are ignored.
  - The first program with `program_number ≠ 0` is used. At most 64 elementary streams are read per
    PMT.
  - A new PMT version: same stream types per role with new PIDs → followed; a different type for a
    kept role → `ErrUnsupported`.
- **Stream types:**
  - kept by role: `0x1B` H.264, `0x24` HEVC, `0x0F` AAC ADTS
  - `0xDB`, `0xCF`, `0xC1` (SAMPLE-AES variants) → `media.ErrEncrypted` when they would fill a role
  - `0x11` LATM, `0x01`/`0x02` MPEG-1/2 video, `0x03`/`0x04` MPEG audio, `0x81` AC-3, `0x87` E-AC-3,
    and `0x06` with an AC-3/E-AC-3 descriptor → `ErrUnsupported` when they would fill a role
  - `0x15` ID3, `0x86` SCTE-35 and other private data are ignored
- **PES.**
  - Start code prefix `0x000001` and `PES_header_data_length` are bounds-checked.
  - `PES_scrambling_control ≠ 0` → `media.ErrEncrypted`.
  - PTS/DTS are the 33-bit values; the marker bits are not enforced.
  - A unit completes at the next `payload_unit_start_indicator` on its PID, when
    `PES_packet_length` is reached, or at the end of the last file. An open unit continues across a
    file boundary.
  - Each unit is at most `MaxPESBytes` (32 MiB); more is `ErrMalformed`, so a stream that never
    starts a new PES cannot grow memory.

### 4.3 Video (H.264, HEVC)
- **One PES is one access unit**, which is how HLS packages video. A unit with two or more access
  unit delimiters is `ErrUnsupported` ("several access units per PES"), rather than mistimed.
- **Annex B → length-prefixed.** NAL units are split at 3- and 4-byte start codes and written with
  4-byte lengths; emulation-prevention bytes are kept unchanged.
  - Removed:
    - H.264: AUD (9), SPS (7), PPS (8), SPS extension (13), filler (12), end of sequence (10),
      end of stream (11)
    - HEVC: VPS (32), SPS (33), PPS (34), AUD (35), EOS (36), EOB (37), FD (38)
  - Kept: slices and SEI.
  - A unit left without a slice is dropped.
- **Parameter sets.** The latest set per id (SPS/PPS, plus VPS for HEVC), each at most 64 KiB, forms
  the active state.
  - The first sample entry is built from the state at the first sync sample.
  - Whenever the state (ids and bytes) changes, a new sample entry (`sdi`) starts at the next sample.
  - At most 32 entries per track; beyond that `ErrUnsupported`.
  - Sample entries:
    - `avc1` with `avcC` (`mp4.CreateAvcC`), width and height from the SPS (mp4ff's SPS parser)
    - `hvc1` with `hvcC` arrays marked complete (`mp4.CreateHvcC`), so the in-band sets can be
      removed
- **Sync samples.** H.264 IDR (5). HEVC IRAP (16–23).
  - Units before the first sync sample of each discontinuity domain are dropped.
  - HEVC RASL pictures (8, 9) that follow a domain's first CRA are dropped.

### 4.4 Audio (AAC ADTS)
- **ADTS frames.**
  - syncword `0xFFF`, layer 0
  - `sampling_frequency_index` 0–12
  - `channel_configuration` 1–7; 0 means a PCE → `ErrUnsupported`
  - `number_of_raw_data_blocks_in_frame` 0, else `ErrUnsupported`
  - `frame_length` at least the header (7 or 9 bytes; a CRC is skipped)

  A frame split across PES units is carried over, at most 8 KiB.
- **Samples.** Each sample is one frame's raw data block (the header removed).
- **Sample entry.** `mp4a` with an `esds` (`mp4.CreateEsdsBox`) holding an AudioSpecificConfig of
  object type = ADTS profile + 1, the ADTS frequency index and the channel configuration. SBR stays
  implicit.
- **Timing.** The timescale is the sample rate. Sample rates above 65,535 Hz (88.2 and 96 kHz) are
  `ErrUnsupported`, because the MP4 sample entry's field is 16 bits. Every frame is 1024 ticks.
- **Changes.** A change of rate, channels or profile within a track is `ErrUnsupported`.

### 4.5 Timing
All rules are pure functions in `remux/tstime.go`, with table-driven tests.
- **Unwrapping.** 33-bit PTS/DTS are unwrapped per input against the input's last unwrapped value: the
  representative nearest to it, within ±2³². A missing DTS equals the PTS.
- **Video.**
  - Timescale 90,000, so every TS value is exact. The composition offset is PTS − DTS.
  - Decode times must increase strictly; a repeat or step back of up to 1 ms is clamped to the
    previous value + 1. The existing negative-offset shift and edit list absorb what that does to the
    offsets.
  - Each duration is the next DTS − this DTS. The last sample repeats the previous duration (3,000
    ticks if it is the only one).
- **Audio.**
  - A PES's frames take PES PTS + k × 1024 ticks, converted to the sample rate with
    round-to-nearest.
  - The track runs continuously at +1024 per frame.
  - A frame more than 512 ticks later than expected is a gap. Up to 10 s, the previous sample is
    lengthened, as the fMP4 path does.
  - A frame more than 512 ticks earlier is an overlap, and frames are dropped until time catches up.
  - Beyond 10 s either way it is a discontinuity.
- **Discontinuity domains.** `Input.Breaks` split each input into domains, matched by ordinal across
  inputs. All domain arithmetic is in 90 kHz units, converted per track with round-to-nearest.
  - `P_d` = the earliest presentation time of a kept sample in domain d over all tracks.
  - `E_d` = the latest presentation end in domain d over all tracks, on the output timeline.
  - Offsets: `offset_0 = −P_0`, and `offset_d = E_{d−1} − P_d`. So the output starts at the earliest
    presentation, there are no gaps between domains, and video and audio keep their relative offset
    inside every domain.
  - An input with more domains than another stitches its extra domains to its own end.
- **Unlabelled jumps.** A decode-time step below 0 or above 10 s inside a domain is stitched per
  track to that track's own end, as `timeline.place` does for fMP4.
- **Alignment.** Each track starts at its first presentation time on the shared timeline:
  - the empty edit = that time − the earliest track's (1 ms movie timescale)
  - the video's media edit starts at its first presented PTS − first DTS, after the shift

  This deliberately differs from the fMP4 path's decode-time alignment, which would put a B-frame
  video's picture after its sound.

### 4.6 Two passes, memory and disk
- **No joined `.ts` and no spill files.** The fragment files are read where they are, so the peak
  disk use stays fragments + output, as on the fMP4 path.
- **Pass 1** (tables).
  - The demuxer reads each input's files domain by domain, and every unit is converted by the same
    function pass 2 uses.
  - Only metadata is kept: per-sample size, duration, offset and sync flag (the existing 16-byte
    entries), sample entries and domain bounds.
  - Samples are batched into runs of at most 1 s or 16 MiB, ending early at an `sdi` change or a
    domain end, and fed to the existing `collect`.
  - Then `finish` runs. A track with no sample is dropped; no track at all is `ErrMalformed`.
- **Pass 2, progressive.**
  - `moov` is written first, then for each chunk in `chunkOrder` that chunk's samples are pulled from
    the track's replay reader. Each track has its own replay reader, which reads all of its input's
    files in order and reassembles only its PID.
  - Every sample's size is checked against the pass-1 table. A mismatch is `ErrMalformed`
    ("fragments changed between passes").
  - Before pass 2, each fragment file's size and modification time must equal what pass 1 saw.
- **Pass 2, fragmented** (above 4M samples, e.g. VODs of a day or more at 60 fps). Replay readers
  produce in-memory runs of at most 1 s or 16 MiB each, placed with pass 1's domain offsets. This is
  required in v1.
- **Abstractions.** The muxer's chunk data source and run source become interfaces:
  - fMP4 copies `(file, offset, size)` ranges, unchanged
  - TS pulls regenerated samples

  The fMP4 output stays byte-identical. Its existing tests and `TestAgreesWithMP4FFDefragmenter` must
  pass unchanged.
- **Memory.** The existing tables (bounded by the 4M-sample rule), plus per replay reader one PES of
  at most 32 MiB and one converted access unit, plus the parameter-set state (≤ 64 KiB per set, ≤ 32
  entries).
- **Cancellation.** `ctx` is checked per file and every 1 MiB.
- **Cost.** A muxed TS is read three times (pass 1, then two replay readers) and written once. This
  is acceptable for v1 (§9 #9).

### 4.7 Limits and errors
| Limit | Value | Beyond |
|---|---|---|
| PES unit | 32 MiB | `ErrMalformed` |
| PSI section in reassembly | 4 KiB (`section_length` ≤ 1021) | section ignored |
| Elementary streams per PMT | 64 | the rest ignored |
| Parameter set | 64 KiB | `ErrMalformed` |
| Sample entries per track | 32 | `ErrUnsupported` |
| ADTS carry-over | 8 KiB | `ErrMalformed` |
| Resync scan | 64 KiB | `ErrMalformed` |
| Run | 1 s or 16 MiB | a new run |
| Progressive samples | 4M (existing) | fragmented output |
| `CheckTS` prefix | 1 MiB | only the first MiB is read |
| Unlabelled jump | < 0 or > 10 s | stitched |

Errors are values, never panics. `ErrMalformed` maps to `media_invalid`, `ErrUnsupported` to
`media_unsupported`, and `media.ErrEncrypted` to `encrypted`. Messages say what and where (input,
fragment index, PID), never a URL.

### 4.8 Early check and job integration (`internal/mediajob`)
- **Plan.** `expectedContainer`/`outputContainer` (`internal/mediajob/probe.go:615`, `:637`; today
  one TS track → `"ts"`, TS + TS → `ErrUnsupported`) return `"mp4"` for one TS track and for TS video
  plus TS audio. Under `LegacyTS` they return `"ts"` for one TS track, and TS video plus TS audio
  stays `media_unsupported`. `setContainer` (run.go:636) and `assemble` (run.go:665) use the same
  rule.
- **Input.** `trackState.input()` (run.go:152) fills `Breaks` from `media.Fragment.Discontinuity`
  (`internal/media/media.go:122`; the index in `Fragments`, excluding 0) and from every TS EXT-X-MAP
  entry after the first, and `Language` from the track (`media.Track.Language`, kept on mediajob's
  `track`, probe.go:26, which has no language today). A TS EXT-X-MAP section stays an entry of
  `Fragments`, as today.
- **Early check.** In the init phase (`run` run.go:319: `pending(true)`, then `checkInits` run.go:587)
  a TS track, or an HLS track whose container is still unknown, also fetches its first media fragment.
  An unknown container is sniffed from it first (`sniff`/`setContainer`, run.go:622-653): neither TS
  nor fMP4, packed audio among them, is `media_unsupported`. Then `Options.CheckTS` (default
  `remux.CheckTS`) runs on the first MiB of the EXT-X-MAP section (if any) followed by that fragment,
  before any other fragment of any track is requested, as `checkInits` does for fMP4:
  - scrambled → `encrypted`
  - unconvertible → `media_unsupported`
  - unparsable → `media_invalid`

  Under `LegacyTS` nothing of this runs (the join accepts what it accepted before). The sniff and
  the check read each fragment from its first packet (§4.2); fMP4 behind a junk prefix is refused.
- **`ProbeMP4`.** It accepts the TS candidates of §3.3 (an HLS media playlist without EXT-X-MAP, or
  with a TS map, not packed audio) when `media.TSConvertible(track.Codecs)` holds, with the second
  offer round of §3.3. Its output container is `"mp4"`. Of the segments, only the bounded requests
  that measure a track without `BANDWIDTH` are made at probe time (§3.3, 0.7.1).
  - **fMP4 tracks (0.7.2, `probeinit.go`).** When the playlists of a measured fMP4 track declare
    neither its picture nor its codecs, the job first fetches its EXT-X-MAP section (≤ 512 KiB, one of
    the track's 4 requests; a HEAD is dropped before a size sample is). The first segment is read
    as `bytes=0-65535` for its `moof` boxes only when it is sampled anyway. No request is made for
    the frame rate alone.
  - **Reading the init.** `media.CheckInit` runs first. A section cut at 512 KiB is checked again
    with the boxes the cut leaves open closed (`closeCut`: only sizes change), so protection in the
    part read counts. Then one linear pass reads the boxes needed, and mp4ff decodes only leaf boxes,
    one at a time (`mvhd`, `tkhd`, `mdhd`, `hdlr`, `trex`, `avcC`/`hvcC`/`av1C`/`esds`; `tfhd`/`trun`
    of each `moof` up to the first with a `traf` of the video track). Never a container with its
    children: mp4ff takes time quadratic in nesting depth there, and boxes nested 60,000 deep took
    18 s.
  - **Not described.** An init that `remux.parseInit` refuses as malformed:
    - no `mvhd`
    - a track without `tkhd`/`mdia`/`mdhd`/`hdlr`
    - track ID 0, or an ID used twice
    - a video or audio track without a sample entry, or with timescale 0
    - a box it reads that mp4ff cannot decode
  - **Encrypted.** An encrypted init refuses the track: no codecs, no size, no further requests.
    Its picture is still read.
- **`MP4Only`** (`checkMP4`, run.go:656). It now accepts TS selections (they produce MP4) and still
  refuses WebM and other containers before fragments, after sniffing and after assembly.
- **Assembly** (run.go:665-716). Under `LegacyTS` a single TS track is joined with
  `Options.ConcatTS` (default `remux.ConcatTS`); otherwise `Options.Assemble`.
- **Progress.** No new progress during conversion: the job stays at 100 % fragments while the UI
  shows its assembling text (§3.9). Since 0.7.1 `Progress.BytesTotal` estimates the file once each
  track has a durable media fragment (durable bytes, plus byte ranges or bytes per second of media ×
  duration for the rest), always `BytesTotalApprox`; the daemon sends it as `Download.size` with
  `size_approx` (else the stored fragments extrapolated, else the probe's size or bit rate ×
  duration), and every UI shows "~2.1 GB" or "about" and an estimated time left while percentages
  still come from fragments. A completed download shows its file's exact size.

## 5. UI

### 5.1 Overlay on any video (extension)
- **Content scripts:**
  - `video.content.ts`:
    - matches `http://*/*` and `https://*/*`, with `allFrames: true`, `matchAboutBlank: true` and
      `runAt: 'document_idle'`
    - `excludeMatches` the hosts of `youtube.content.ts` (which matches `https://www.youtube.com/*`,
      `https://m.youtube.com/*`, `https://music.youtube.com/*` and
      `https://www.youtube-nocookie.com/embed/*`, `youtube.content.ts:28-34`): `*://www.youtube.com/*`,
      `*://m.youtube.com/*`, `*://music.youtube.com/*`, `*://www.youtube-nocookie.com/*`
    - isolated world, no new permission, no web-accessible resource (`wxt.config.ts` unchanged;
      `manifest.test.ts` already asserts both for the YouTube script)
  - `video-ui.ts` is a WXT unlisted script (`defineUnlistedScript`) built to `/video-ui.js`. The
    background injects it with `scripting.executeScript({target, files: [VIDEO_UI_SCRIPT]})`, which
    the `scripting` and http(s) host permissions already allow (`wxt.config.ts` `PERMISSIONS`,
    `HOST_PERMISSIONS`); files injected this way need no web-accessible resource.
- **Handshake.**
  - The tracker publishes its API on the isolated world's `globalThis[Symbol.for('downex.video')]`
    as `{version: UI_PROTOCOL, videos(), onChange(fn), send(msg), invalidated}`. The page never sees
    the extension's isolated world.
  - The UI script mounts only when the version matches.
  - An orphaned tracker (extension reloaded or updated) removes its layer on
    `ctx.onInvalidated`.
- **Layer.** One fixed-position host per frame, appended to `document.documentElement`, with a
  closed shadow root:
  - `:host{position:fixed; inset:0; pointer-events:none; z-index:2147483647; contain:layout}`
  - buttons and panel get `pointer-events:auto`
  - nothing is inserted into the site's player, so its layout and event handlers are untouched
  - styles: `OVERLAY_CSS` (`lib/video-overlay.ts:58`) starts with a `:host` rule for a host inside the
    YouTube player (`position:absolute; inset:0`, z-index 2147483000). Lane E splits it into that host
    rule and the shared rules (tokens, pill, panel), so both overlays share one set of rules and the
    layer combines them with its own fixed `:host` rule above (`LAYER_HOST_CSS`, unchanged). The
    "Glass" restyle (2026-10-25) changed the shared rules on purpose; `video-overlay.test.ts` pins
    `OVERLAY_CSS` by length and SHA-256, so every later change to it is deliberate too. They are applied
    with `adoptedStyleSheets` (`CSSStyleSheet.replaceSync`), with a `<style>` element where
    constructable sheets are missing or assigning them throws (Firefox content-script X-rays). The
    YouTube overlay keeps WXT's `createShadowRootUi` (`youtube.content.ts:94`), which the injected
    script cannot use (no `ContentScriptContext`).
  - `isolateEvents` (`lib/video-overlay.ts:49`, `ISOLATED_EVENTS` :15) on the host
- **Fullscreen.** On `fullscreenchange` the host moves into `document.fullscreenElement` when that is
  not the `<video>` itself, and back afterwards. A fullscreen `<video>` element shows the browser's
  own controls only; the popup still lists it.
- **Placement.**
  - Each button sits at the video's top-right corner, inset 12 px, positioned from
    `video.getBoundingClientRect()` in `requestAnimationFrame`. It is updated on scroll (capture,
    passive), resize, `ResizeObserver` and `IntersectionObserver` callbacks, only while shown.
  - Below 360 × 200 px of video it is the mark alone (32 × 32 px), inset 8 px.
  - At most 8 buttons per frame: the largest visible videos.
- **Button.**
  - The YouTube overlay's button (`VideoOverlay`, `lib/video-overlay.ts:279`, `BUTTON_LABEL` :38): a
    real `<button aria-label="Download this video with Downex" aria-haspopup="dialog" aria-expanded>`
    with the mark and the text "Download", on the same translucent pill.
  - Shown for 3 s after pointer movement over the video's rectangle, for 5 s after `play`/
    `loadedmetadata`, on `:focus-visible` and while its panel is open. It is hidden with opacity,
    so it stays focusable.
  - *Fix 2026-11-18 (owner's report: on Instagram the button hid as soon as the pointer reached
    it).* While the pointer is on a compact button itself (`pointerenter` until `pointerleave`, or
    its last position in the frame lies on the button: a pointer that stops on the spot the moment
    the button is revealed gets no `pointerenter` until it moves again) it stays shown, however long the pointer rests there, and it is shown for 3 s more after the
    pointer leaves it. While the pointer is on a button, that button is the one under the pointer
    as long as the page shows its video (the layer's own elements never count as covering a
    video): a prompt or viewer the page lays over the whole video hides it all the same, checked
    again every 3 s while the pointer rests there. The YouTube pill keeps its usual timing (3 s
    after the last movement). A hidden button takes no pointer, so the pointer that comes straight
    onto its spot is judged by the video under the pointer (below), which counts a page layer
    over a strip of the video as not covering it. Before, a pointer resting
    on the button hid it after 3 s (its hidden state takes no pointer, so it could not be clicked),
    and on a compact site the element the page has under the button (an Instagram post's header
    layer, which belongs to the whole post, not to the video's player) was taken for something
    covering the video, which hid the button the moment the pointer reached it.
  - It respects `prefers-reduced-motion` and `forced-colors`, and activation requires
    `event.isTrusted`.
- **Quiet videos** (*amended 2026-11-18*, §2.5). A quiet video (a decorative loop on a host that is
  not curated) gets a button of the usual kind with these differences:
  - It is always the mark alone (32 × 32 px, inset 8 px).
  - It is never shown on `play`/`loadedmetadata`.
  - It shows while the pointer is over the video's rectangle and hides as soon as the pointer
    leaves it, unless it has the focus or its panel is open.
  - It counts against the 8 buttons of a frame after every meaningful video.
  - *Fix 2026-11-18.* It never covers the page's own controls. Before placing it (in the animation
    frame), the layer hit-tests the mark's box (`document.elementsFromPoint` at its corners and
    centre, past the layer). Under it may lie a link, a button, a form control or an element with
    a control role (`button`, `link`, `checkbox`, `switch`, `slider`, `tab`, `menuitem`). When one
    is there, the mark goes to the video's top-left corner instead. When one is there too, the
    video gets no mark (the menu's "Show Downex download panel" still opens its panel). Two kinds
    of such element are the video's own surface and do not count: one that contains the video,
    and one laid over half of the video's visible part or more (a tile's link). A real check on
    2026-11-18 (a landing page with a pause button in its hero's top-right corner): the mark went
    to the top-left corner and a click on the pause button paused the hero.
- **Click.** DRM and live are checked again (§2.5), then `downex:frame-offers` is called and the
  panel opens.
- **Feed videos** (*amended 2026-11-18*). A curated site's feed, profile, search or home page is
  not a video address, so its frame has no Site source of its own. On a click (or the menu's
  "Show Downex download panel"), when the frame's own address is not a curated video (§3.8), the
  layer looks for the clicked video's own address (`lib/feed-link.ts`):
  - **The walk.** It starts at the `<video>` and goes up at most 16 ancestors (from a shadow root to
    its host). At each ancestor it looks at the ancestor itself when it is an `<a href>`, else at
    its `a[href]` descendants. *Fix 2026-11-18:* the bound was 10. A public Facebook page's video
    (signed out, 2026-11-18) had its own `/videos/<id>` link 11 levels up and got none.
  - **Where it stops.** The walk stops and takes nothing at the first ancestor that holds a
    `<video>` other than the clicked one: that ancestor's links may be a neighbouring post's (a feed
    card without a post link of its own, below another card that has one). It also stops at an
    ancestor with more than 64 `a[href]` descendants, a page section, which it does not judge from
    part of its links (*fix 2026-11-18*; before, the first 64 decided). *Amended 2026-11-18
    (social sites):* a hidden slide of the same carousel does not stop it (§5.1 "Carousels").
  - **Which links count.** It reads each link's resolved `href` and parses it with the curated
    table. A link counts only when it is on the frame's own origin and is not a `network` site's.
    Examples: an Instagram `/p/` or `/reel/` link, TikTok `/@<user>/video/`, X `/status/`, a
    Facebook `/reel/`, `/videos/` or post link, a Pinterest `/pin/`.
  - **Which ancestor decides.** The lowest ancestor with any such link decides. Its links must all
    name one video (one key, however many links). Links to several videos mean the walk reached a
    list of posts, and nothing is taken.
  - **What it reads.** It reads link addresses only, never page text, scripts or player data
    (CLEAN-ROOM.md). It runs at click time only, never while the user browses.
  - **Where the address goes.** It goes to the background (`downex:frame-offers {post}`, then
    `downex:page-video-probe`/`-add {url}`) and from there to the local Downex only.
  - **What the background does with it.** The panel's Site source is that video, with the key its
    own page has, so the look-ahead and probe caches are shared. It is asked at once, as on a
    curated page.
    - It carries no tab title (a feed's title is not the video's) and no look-ahead. In the top
      frame it carries no Referer either (same origin, as on the post's own page); a feed in an
      embedded frame gets an embed's default-policy Referer (§3.6).
    - A quiet video's mark stays shown for 3 s after its panel closes, then follows the pointer
      again.
    - The sign-in follows SITE-MEDIA-SPEC §6.3 (amendment 2026-11-18): the post is asked without
      cookies first, and once more with the site's sign-in only after `auth_required`.
    - The panel's title is the site's title for the video once the site answered.
  - **Which pages are feeds** (*fix 2026-11-18*, `curatedPage`). From the addresses these sites
    show in the address bar: each curated site's home page, and per site its feeds, searches and
    profiles. Examples: TikTok `/foryou`, `/following`, `/explore`, `/@<user>`; Instagram
    `/explore/…`, `/<user>/`, `/<user>/reels/`; X `/home`, `/search`, `/<user>`,
    `/<user>/media`; Facebook `/watch/`, `/search/…`, `/<page>`, `/<page>/videos`; Reddit
    `/r/<name>/` and its orders, `/user/<name>/`; Pinterest's searches, profiles and boards;
    YouTube's `/feed/…`, `/results`, `/@<handle>…`; Bluesky `/profile/<handle>`; LinkedIn
    `/feed/`. Any other address of a curated host that the table (§3.8) does not parse keeps the
    generic target: an episode, a clip, an album, a story, a post the table does not know, a
    live channel. Before this fix every such address was taken for a feed, which removed "Look for
    more qualities" from single-video pages.
  - **No post found** (*amended 2026-11-18*, SITE-MEDIA-SPEC §2.9). The frame then gets no Site
    source (yt-dlp cannot read a feed as one video), and the clicked video's **Played** source
    carries the click: the files the tab's player fetched for it. The texts that sent the user to the
    video's own page or to TikTok's comments are removed. On TikTok, opening a For You video's
    comments changes the address bar to the video's own address (`pushState`); the frame's address
    read at the click (`downex:frame-offers {href}`) then names that video, and its Site source is
    asked beside Played; the comments panel being open or not makes no difference to Played.
  - **Not verified** with the extension on the real sites in a real browser (§8.4). Only the
    walk itself ran on a real page (the TikTok check above).
- **Menu** (*amended 2026-11-18*, BROWSER-SPEC §4). The context menu's "Show Downex download panel"
  on a `<video>` (any source: `blob:` and MSE players included) opens the same panel for that video
  in its own frame:
  - The tracker remembers the target of the last trusted `contextmenu` event (capture phase,
    passive) when it is a `<video>` (`composedPath()[0]`, so a video in an open shadow root
    counts), else the tracked video under the pointer (its `getBoundingClientRect()`, read once per
    right-click: a video in a closed shadow root, which the event does not name), for 60 s. It
    never prevents the event or the page's own menu.
  - On `downex:show-panel` it tracks that video if it is not tracked yet (making room as on a media
    event), marks it requested, asks for the overlay UI if it did not yet (`downex:video-ui`), and
    answers `{shown: true}` once the UI is in the frame; with no such video, or when the UI could
    not be put in (asked again on the next request), `{shown: false}` and the background records
    "Right-click the video itself, then choose Show Downex download panel." for the popup's
    history. *Amended 2026-11-18 (review):* a right-click whose event named no video and that hit
    no tracked one keeps its point; on `downex:show-panel` the tracker runs its bounded deep scan
    once (open and closed shadow roots) and looks under that point again, so a closed root's video
    that never played is found.
  - The background lets the overlay UI into a frame where buttons are not allowed only while that
    frame answers the menu's `downex:show-panel` (at most 30 s): the user's menu choice, never a
    page's own message, decides. Other `downex:video-ui` requests keep the button rules.
  - YouTube's frames (`youtube.content.ts`) answer `downex:show-panel` by opening their own panel
    when their button is shown; otherwise, and in the store build, the background records "On
    YouTube, use the Downex toolbar menu to download this video." (store build: "Downex can't show
    its panel on this site."), not the reload advice, which never helps there.
  - The layer mounts for a requested video even where buttons are not allowed (the option off, a
    hidden site) or the video is not meaningful (small, decorative): the user asked for it. It
    gets its button (revealed) and its panel opens at once. Other videos keep the usual rules: on a
    site where buttons are not allowed, the requested one is the only button, and the layer
    unmounts again after its panel closes.
  - DRM and live are refused by the panel as on a click; private windows get no menu action.
- **Social sites: the compact button and every video surface** (*amended 2026-11-18, owner
  decisions 2-4 of that day*). The owner asked for a smaller button on social sites that fits
  their vertical players and feed cards and never covers their own controls, for a button on
  every kind of video these sites show, and for touches that set Downex apart. YouTube's button
  (`youtube.content.ts`) is unchanged, and so is every other site's.
  - **Which frames** (`compactSite`, §3.8 "Compact sites"). A frame whose own address is on the
    host of TikTok, Instagram, Facebook, X, Reddit, Pinterest, Threads, Snapchat, LinkedIn,
    Bluesky, Tumblr, 9GAG, Imgur, VK or Telegram, top frames and these sites' embedded players on
    other pages alike. The video-platform rows of §3.8 (Vimeo, Dailymotion, Twitch, Kick,
    Bilibili, Streamable, Odysee, BitChute, Rumble) keep the usual button: their players are large
    and wide, with their controls along the bottom.
  - **Look.** Every video button of such a frame is the mark alone on the same smoked-glass
    pill: 34 px with an inset of 10 px over a video at least 260 × 220 px, else 28 px with an
    inset of 6 px (feed cards, grid tiles, thumbnails); the orb's disc is 28 px (24 px) with a
    16 px (14 px) mark. *Amended 2026-11-19 (owner: "little small ... i dont want too way big"):*
    moderately bigger than the first 28 px (24 px) form, still placed only in a free corner.
    On `:hover`, `:focus-visible`, while its panel is open and while it shows a
    state (below) it widens, away from the corner it sits in, to show its label ("Download", or
    the hint below); never wider than the video's visible width less both insets, the label cut
    with an ellipsis before that. It is drawn in the fixed layer like every button, so the
    page's layout never moves; `prefers-reduced-motion` removes the widening's and the ring's
    motion (the label shows at once, the ring stands still); forced colours and reduced
    transparency follow the shared rules. Shown and hidden as any meaningful video's button
    (3 s after pointer movement over the video, 5 s after `play`/`loadedmetadata`, focus, open
    panel); the quiet mark (above) of other hosts is unchanged.
  - **A free corner** (`placeCompact`). In the animation frame that places it, while it is shown
    or the pointer is over its video, the layer hit-tests the button's box as for the quiet mark
    (the page's controls; the video's own surfaces do not count, and here also `[tabindex="0"]`
    elements). It takes the first free one of: the visible part's top-right corner, its
    top-left corner, the right edge just under the control that took the top-right corner, the
    left edge under the one that took the top-left corner (each only within the upper 45 % of the
    visible part), the middle of the right edge, the middle of the left edge. When none is free
    there is no button (the menu's "Show Downex download panel" still opens the panel). The
    choice is kept relative to the video and checked again only when the video's size changed by
    more than 4 px or after 500 ms while shown, never every frame: scrolling moves the video and
    the site's controls together.
  - **The video under the pointer.** Where the pointer is over the rectangles of several
    videos, or over one video covered by something else (a post viewer opened over a feed, a
    story over its tray), only the video the page shows there gets its button: the layer reads
    `document.elementsFromPoint` at the pointer once per placement (past the layer) and reveals
    the video whose player holds that element, the nearest one when several do. A video's player
    is the video and its ancestors, up to 12 levels (from a shadow root to its host), while they
    stay about its size (at most 1.25 times its width and height plus 8 px; or a card that
    letterboxes it, within that bound on one axis and on the other at most 16/9 of its own kept
    side plus 8 px, or, when it keeps the video's height, at most the viewport's width plus 8 px,
    such as a portrait video's wide card on a vertical feed, *amended 2026-11-18*; a wrapper with
    no area is passed over): a feed or a page around it is not its player. *Fix 2026-11-18
    (owner's report: TikTok For You showed no button on portrait videos):* a For You card follows
    the window's width, not the video's (measured: a 514x914 video in a 1634x914 card at a
    1906x946 viewport, a maximised 1080p window at 125 %), so a 16:9 frame of the kept height
    alone rejected it, and the card's overlay, the element at the pointer, counted as covering
    the video. When no player holds the element at the pointer, the video the page shows at the
    centre of its visible part (its player holds the element topmost there) is the one under the
    pointer: a layer the page lays over a strip of the video (an Instagram post's header, where
    the button sits) does not hide its button, while a viewer or prompt over the whole video
    does. With none, no button is revealed there. A button revealed
    without the pointer (a video that starts or loads, §5.1 "Button") is checked the same way at
    its own spot: a video that starts under a post viewer gets no button over the viewer. Compact
    frames only. The pointer on a button keeps it while the page shows its video (§5.1 "Button",
    fix 2026-11-18).
  - **Videos that mount late or are recycled.** In the top frame of a compact site the tracker
    also runs one `MutationObserver` (`childList`, `subtree`) on the document. Its callback does
    no work of its own: it schedules one `check()` of the live collection 250 ms later (one
    pending at most), so a feed's videos that mount without playing (a scroll with the keyboard)
    get their buttons without pointer movement. A player that reuses its `<video>` for the next
    post (`emptied`, a new `currentSrc`) drops that video's hint and state below; the post is
    found again on the next click. Removed videos are let go at that check (IntersectionObserver
    and ResizeObserver entries removed, their buttons removed by the layer). No timers run while
    nothing changes, and nothing reads layout for videos whose button is not shown.
  - **Feeds, reels and Shorts-style players, stories, modals.** Feed videos keep "Feed videos"
    above; vertical players are videos like any other (their buttons are the 34 px form); a story
    on its own address (§3.8 Instagram `/stories/…`) is that address's video, the largest shown
    one (the active story) gets the button; a post viewer opened over a feed changes the address
    bar to the post (Instagram, X), so its video is the frame's own and the feed under it gets no
    button (the video under the pointer, above).
  - **Carousels.** *The feed walk's stop rule is amended:* another `<video>` under an ancestor
    does not stop the walk when it is a slide of the same carousel as the clicked one: it is
    tracked and known not to be shown (its IntersectionObserver ratio below 0.5, a slide moved
    out of the carousel's clip), it has a size, and its rectangle has the clicked video's top and
    height within 2 px (one horizontal row). A neighbouring card of a feed (another top) or a
    shown video beside it (a grid, a masonry) still stops the walk. The panel opens on the
    clicked slide where the page shows which (SITE-MEDIA-SPEC §3.12 "Which item").
  - **Grid tiles** (profile grids and other tiles that show a picture, not a video). In the top
    frame of a compact site the tracker listens to trusted `pointerover` events (capture, passive).
    When the pointer enters a link (`a[href]`, `composedPath`) whose address is a curated video on
    the frame's origin with a shape that always names a video (`videoTile`, §3.8 "Tiles"), that
    holds no `<video>` with a button of its own and is at least 96 × 96 px (read once per
    entered link), that link is the frame's tile (`tilePost`): the overlay UI is asked for as
    for a quiet video, and the layer puts one compact button at the tile's free corner (on a
    tile, any of the link's first 64 descendants in the corner that is not half of the tile or
    more takes it too, such as the tile's own "reel" badge, which a real profile grid draws at
    its top-right without taking the pointer, so a hit test passes through it) while the
    pointer is over it (it hides as soon as the pointer leaves, unless focused or its panel is
    open). Its panel is a feed video's panel whose post is the tile's own address (no walk),
    with no element (no File rows of its own, no part from the element, never DRM or live at
    click time: the site's answer says). A `<video>` that mounts in the tile (a preview on
    hover) and gets a button of its own (big enough, §2.5) takes over: the tile's button goes,
    the video's own comes. A preview smaller than that (a 148 px tile's) leaves the tile its
    button and its post. No tile is taken in a frame whose own address is a video (the "You
    may like" and creator tiles under a video page: the page is that video, as the feed under
    a post viewer) or a site's private messages ("DMs" below), and a tile goes when the frame
    navigates to such an address (*amended 2026-11-18*). Only the link's resolved address is
    read, locally; nothing leaves the frame before the click.
    Tiles are a pointer shortcut: keyboard users open the post and use its button.
  - **Embeds.** These sites' embedded players on other pages are frames of the embed addresses of
    §3.8 ("Embeds"): the Site source is the embedded post, with an embed's default-policy Referer
    (§3.6), the compact button, and no look-ahead.
  - **DMs.** Private messages are never a surface: no address of a site's messages is in §3.8,
    and their pages are not feeds (`curatedPage`; X `/messages` and `/i/chat`, Facebook
    `/messages`, Instagram `/direct`, TikTok `/messages`, Reddit `/chat` and `/message`, LinkedIn
    `/messaging`, Bluesky `/messages`, which the profile shapes would otherwise take;
    `messagesPage`), so their videos get no Site source and no post is looked for: the feed walk
    takes nothing there, the background takes no post a panel names there, and no grid tile is
    taken. They get no generic target either ("Look for more qualities", the popup's "Find the
    video on this page"), which would ask yt-dlp about the conversation's address. The
    browser's own File and Stream rows are listed as on any page.
  - **What the button tells** (compact buttons only):
    - *The best quality and size, once known.* "Download · 1080p · 24 MB" (the default row's
      quality up to " (" and its size, "~" kept for estimates) when it widens; where its widest
      form is too narrow for that (a grid tile, a small card) "1080p · 24 MB", then "Download"
      (and "Sent" for "Sent to Downex"), judged at 7 px a character. Known from the
      panel's own answer for that video, or, on a frame whose own address is a curated video (or
      a video whose post a click found before), from the background's memory
      (`downex:frame-offers`, which sends nothing to Downex, so no request at all, with or
      without cookies), asked at most once per 20 s per button and only when the pointer reveals
      it. A look-ahead (§5.5) fills that memory on the sites marked for it. Never a request of its
      own, never with the sign-in. The accessible name becomes "Download this video with Downex,
      1080p, 24 MB".
    - *Work in progress.* While its panel gets the qualities or sends one, a ring turns around
      the mark (`data-state="working"`; it stands still with reduced motion).
    - *Sent.* Once Downex took the video, the button widens to "Sent to Downex" with a check for
      3 s, then keeps a small check on the mark (`data-state="sent"`) for that video: the user
      sees in the feed which videos were already sent. Downex only took it: the extension never
      learns the download's own progress (the relay has no such answer), so it never says
      "Saved".
    - These live in the layer's memory only, per video and its source (`currentSrc`) or post, and
      go with the page.
  - **Part of the video.** The compact panel is the same panel: its *Part of the video* row
    (the timeline trimmer, "Start here"/"End here" at the playhead) saves a part in one more click
    where Downex takes parts, unchanged.
  - **Not verified** on the real sites in a real browser: each site's controls and markup
    (corners, carousels, tiles, stories, modals), embeds, the late-mount observer's cost on long
    feeds. A local harness with stand-in pages of each surface, light and dark, was checked
    (§8.4).

### 5.2 Panel
- **Recommended** (*amended 2026-11-19, owner: "mark the highest quality as recommend download
  when qualities loaded"*). When a panel lists at least two qualities of the site and of what
  played (Site and Played rows), the highest one with sound (`highestWithSound`: the largest
  picture, width × height, of a muxed track or a video paired with an audio track; the first of
  equals; without one with sound, the largest) carries the badge "Recommended" (said in the
  row's label) and is the default row. The popup's video rows of sites other than YouTube do the
  same (`VideoPanelDeps.recommend`: the badge in the quality list, the pick preselected);
  YouTube's panels keep Downex's default pick. A panel with a Played source keeps the default it
  showed (§2.9 "The panel"): a quality listed later moves neither the selection nor the badge.
- **Quick rows** (*amended 2026-11-19, owner: "the loading of getting video links takes little
  extra seconds"*). While the Played source has not answered (within PLAYED_WAIT_MS), the panel
  asks for the frame's offers every 250 ms (`PLAYED_POLL_MS`) instead of every 2 s, so what played
  is listed as soon as Downex matched it; the site's qualities (yt-dlp) fill in when they come.
- **Shell.**
  - `popover="auto"`, `role="dialog"`, labelled by the title (the tab's title, else "Video"),
    positioned from the button's rectangle.
  - It closes on Esc (focus returns to the button), light dismiss, `fullscreenchange`, the element's
    removal or `emptied`, a navigation of the frame, or the extension's invalidation.
  - Rows are buttons in the order of §2.6. Up and Down move between them (not inside a field, a
    select or a slider: the Part row's, "Audio").
- **Row labels** (picker rules of SITE-MEDIA-SPEC §5.3; `qualityRows` for Site and Stream):
  - Site: "1080p (Full HD) 60fps · H.264 · 412 MB", with the site name as the source tag.
  - Stream: "1080p (Full HD) · H.264 · about 412 MB" (bandwidth × duration), tagged "Stream".
  - Site and Stream rows show the track's `name` from the daemon (amendment 2026-10-23), one row per
    quality class; the small text is the codec ("H.264", "AV1", "H.264 + AAC" for a muxed one;
    "MP4" only when the codec is unknown, since every quality is saved as one MP4); width x height
    is the row's tooltip (a Stream row's: "1920×1080 · <address>").
  - File: "720p (HD) · MP4 · 48.2 MB", tagged "File". The name comes from the matched element's
    `videoWidth`/`videoHeight` through the extension's one naming helper (the shared table);
    otherwise the label is "MP4 file · 48.2 MB" with the URL's last segment as tooltip. A matched
    file reads "This video".
  - Sizes use decimal units like the desktop (amendment 2026-10-31: 1 KB = 1,000 B, 1 MB =
    1,000,000 B; one decimal below 100; a size that would round to 1000 of a unit is shown in the
    next one, 999,999 B as "1.0 MB"). An unknown size is left out.
- **Actions:**
  - "Look for more qualities" (Site source not yet asked, non-curated page)
  - "Choose audio" (only when several MP4 audio tracks exist, as now)
  - "Don't show on <host>" in the footer (`downex:set-site-hidden`, then the layer removes itself
    from every frame of the tab after the next `overlay-state` check)
- **States.** Per source, from `VideoPanel`'s state machine for Site (probing, `tools_installing`
  retries every 2 s up to 5 min, sign-in action) and per row for Stream and File: "Sending…", then
  "Confirm in Downex" (`awaiting`) or "Downloading in Downex"; the panel closes 3 s after a success.
  A failed source shows its text (§5.6) in place of its rows, and the other sources stay usable.
- **Small frames.** Below 320 px width the rows wrap and the codec text is dropped. Below 200 px
  height only the default row is shown, with "More in the Downex toolbar menu".
- **Text safety.** Titles and site strings are set with `textContent`, without bidi controls
  (`cleanSiteText`). No HTML strings are used anywhere (Trusted Types safe).
- **Several videos** (amendment 2026-11-18, `SITE-MEDIA-SPEC.md` §3.12; `lib/item-strip.ts`, state in
  `VideoPanel`): when the page video's answer lists two or more items (a carousel, a news page), an item
  row sits under the title, above the rows: "Video 2 of 3", one 44 px tile per video (the page's
  thumbnail, else its number, with its length), the chosen one checked; Left/Right/Up/Down and
  Home/End move between tiles and choose (one tab stop, `role="radiogroup"` of `role="radio"`
  buttons named "Video 2: <title>, 0:12"). With other items left out, the note "2 other items not
  listed: photos, links or videos Downex cannot save." (also with one video), wrapped under the count
  (never cut). The panel opens on the video the user clicked when what the page shows names one
  (`clickedItem`: the element's length, else an Instagram post address's slide; SITE-MEDIA-SPEC
  §3.12 "The video the user clicked"), else on the page's default. Choosing another item asks for its
  qualities (`downex:page-video-probe` with `entryId`; the background caches each item apart, and the
  panel's own probe state stays the default item's), the Part row starts again (another length), and
  Download sends the item (`downex:page-video-add` with `entryId`). The row stays while the item's
  qualities are got, and after a failure ("this item is no longer on the page"), so another can be
  chosen. Thumbnails are `<img>` elements (never a CSS URL) with `referrerpolicy="no-referrer"`,
  `loading="lazy"`, `alt=""`, only for https addresses without user information; one that fails to load
  leaves the number. A short frame shows the count without the tiles. Clicks and keys the page makes
  (`isTrusted` false) are ignored. The popup lists the same items as a "Video" select ("1 of 3", the
  title, the length; no thumbnails) under the card, with the note, and sends `entryId` with
  `downex:tab-video-probe`/`-add`. The YouTube panel has no row: a YouTube watch page is always one
  video. `NativeClient` refuses an `entryId` to a relay without `items` (`outdated_downex` "Update
  Downex to choose a video of this page.").
- **Part of the video** (2026-11, `MEDIA-SPEC.md` §8; `offer-panel.ts`, the row's state, elements and
  rules in `lib/part-row.ts` (shared with the YouTube panel, below), pure rules in `lib/clip.ts`):
  below the quality rows (and the audio select), only when the relay lists `clips`
  (`FrameOffers.features.clips`) and the default row is a stream's or the page video's (never a direct
  file), and not in a short frame.
  - **Its duration** (*amended 2026-11-18*): the default row's info's `durationMs` when it is above 0.
    Otherwise, as for Instagram, which reports 0, it is the clicked element's own duration: its
    `duration` when finite and above 0, read on every render; never a live element's. When neither
    is known, there is no row. Before this, a zero-length timeline was drawn. With the element's
    duration, the trimmer's video check (below) holds by construction, and the daemon checks the
    part against no duration (MEDIA-SPEC §8 C2: an unknown duration is not checked).

  A disclosure button "Part of the video" (`aria-expanded`, a
  scissors mark, a chevron) with its summary "Whole video" beside the label (which never wraps); open,
  the readout below says the part and the summary fades out (amendment 2026-11-17). It stays closed by default, and closed, the whole video is sent. Opened (the body grows to its height,
  its parts rising in turn, the part's fill sweeping in and the handles popping in; none of it under
  `prefers-reduced-motion`), the row is a **timeline trimmer** (amendment 2026-11-17, replacing the
  From/To fields with Now and the Chapter select):
  - **Readout.** The two times big ("1:20 → 2:45", 18 px, tabular figures), "Whole video" (resets the
    part; shown only with a part), and under them "1:25 selected · ~78 MB at 1080p": the part's length
    and the default row's size for the part (`clipSize`, below) with its quality's short name; "Whole
    video · 754 MB at 1080p" without a part. While a typed time cannot be used, its error takes that line's
    place (below). The site's and the streams' quality rows show the part's estimate too
    (`buildOfferRows` with the part), so the rows and the readout agree (amendment 2026-11-17).
  - **Track.** The whole video as a rail with ruler ticks. The row's body (the track, the chapter chips,
    the buttons) reads left to right whatever the page's direction (`dir="ltr"`; a chapter's title
    keeps its own, `dir="auto"`), Start here before End here as the handles. The part between two Mint handles joined by a frame is filled with a Mint
    gradient, the rest dimmed; the playhead (a line with a dot) is the page video's `currentTime`,
    followed on `timeupdate`/`seeked`. Chapters are segments of the rail separated by 3 px gaps (2 px past 12
    chapters; past 24, short ticks in place of the ruler's); the mouse over the track shows a bubble
    with the time under it on the handles' grid and its chapter ("Testing · 7:47"), the segment lit.
    The bubbles have room of their own above the rail (`--tip`), so they never cover the readout. A part
    is drawn at least 10 px wide (`--vs`, `--ve`), so a short part of a long video shows between its
    handles. Positions are fractions in custom properties (`--s`, `--e`, `--p`, `--t`) and
    glide (0.34 s) except while a handle is dragged. Tokens `--p-trim`, `--p-trim-hi`,
    `--p-trim-grip`, `--p-trim-dim`, `--p-trim-tick`, `--p-trim-halo` in both schemes.
  - **Pointer.** A press on a handle grabs it where it is (no jump); a press elsewhere on the track
    takes the nearer handle there. The handle follows the pointer (mouse, touch or pen; pointer
    capture, `touch-action: none`, so the page does not scroll) on a grid of whole seconds (tenths
    under 20 s; the very end kept), grows while held, with a Mint bubble of its time above it. The
    handle hit areas reach 8–10 px past the handles. A double-click on a chapter's segment takes that
    chapter; a press needs `isTrusted`.
  - **Keyboard.** Each handle is `role="slider"` (named "Start" and "End", `aria-valuenow`/`min`/`max`
    in seconds, each bounded by the other handle, `aria-valuetext` "Start 1:20"): arrows ±1 s
    (Right/Up later), Shift+arrows ±10 s, Page Up/Down ±1 min, Home/End the bounds; the bubble shows
    while keys move it. Keys with Alt, Ctrl or Meta are left to the browser (Alt+Left is Back). Up and
    Down stay with the slider (the panel does not move to a row), and no key
    reaches the page or the player (`isolateEvents`).
  - **Rules.** The handles never cross and keep `MIN_CLIP_MS` (1 s) between them (`PartRow.setMs`).
    "Start here" and "End here" set a handle from the page video's position and push the other one
    away instead (the end to the video's end, the start to 0). The state holds the part in ms (end 0
    = to the end); `checkRange` (`checkClip`'s rules for ms) checks it.
  - **Chapters.** A chip per chapter under the track ("1 Intro", named "1. Intro (0:00–1:20)",
    `aria-pressed` while the part is exactly that chapter), in a strip that scrolls sideways with faded
    edges where more chips are out of view; a chip chosen scrolls into view.
  - **Typing.** Each time is a chip; a click (or Enter) turns it into a text field (named "From"/"To",
    "Time, like 1:20"), Enter or leaving it commits, Esc cancels (and does not close the panel). A
    committed time is checked with the desktop's rules (SITE-MEDIA-SPEC §5.3: a mistyped time, the
    start after the end, shorter than 1 s, an end past the video's; within 1 s past the end is the end;
    empty is the start or the end), in the trimmer's words ("Enter a time like 1:20 or 1:02:03.", "The
    start must be before the end.", "The part must be at least 1 second long.", "The video ends at
    12:34."; amendment 2026-11-17). One that does not fit is kept as typed in its chip (red, `aria-invalid`
    on the field, both chips described by the error line, `role="alert"`, which shows right under the
    times in place of the length, the track dimmed and Preview disabled meanwhile), the handles staying
    at the last good times, until the next change; errors show only after a commit, never while typing.
  - **The page's video.** "Preview", "Start here" and "End here" and the playhead show only with the
    clicked `<video>` (read in the frame that owns it: the overlay host's own element). The trimmer
    uses only that element's `currentTime`, `paused`, `seeking`, `play()`, `pause()` and their events
    (`PartPlayer`; never the page's scripts or player data, CLEAN-ROOM.md). A drag (or a key) pauses the
    video and seeks it to the moved handle (one seek at a time, the latest waiting for `seeked`), after
    remembering its position and whether it played; the handle let go, its frame stays 1.2 s and the
    video goes back where it was, playing or paused as before. Preview seeks to the start and plays,
    checks every 40 ms and pauses at the end (then the same 1.2 s and back); pressed again ("Stop", the
    button `aria-pressed`) it goes back at once. Closing the row or the panel goes back at once. A
    play, pause or seek the user makes in the player meanwhile is their choice to stay: the video is
    left where they put it; the element's own pause at its media's end during a preview (`ended`
    follows) is the preview's end. The element is the video only while its media's `duration` is within
    2 s (or 2 %) of the video's: an ad played in the same element, or no media yet (NaN), shows no
    playhead, Preview or Start/End here and is neither read nor driven (checked again on
    `durationchange`, `loadedmetadata` and `emptied`). Other media loading into the element (`emptied`,
    `loadstart`) ends a hold without touching it, and in the YouTube panel another video (a new key)
    lets the hold go instead of putting the player back (amendment 2026-11-17).
  - **Sending.** Activating a quality row sends that quality with the part (`downex:offer-add` /
    `downex:page-video-add` with `clipStartMs`, and `clipEndMs` unless the part runs to the end:
    `clipFields`); a row activated while an error shows sends nothing. Every control of the row, the
    disclosure too, is disabled while a pick is being sent and after it was sent (the track ignores the
    pointer, the sliders get `aria-disabled` and leave the tab order), so the row keeps showing the
    part that went. The popup's media list gets no part controls: Confirm download in Downex offers
    them. `lib/clip.ts` repeats `media.ParseClipTime`; its table `CLIP_TIMES` is held to Go by
    `internal/media` `TestClipTimesMatchExtension`.
  - **Forced colours.** The rail and chips are outlined in CanvasText/ButtonText, the part filled and
    the handles drawn in Highlight, the playhead in CanvasText; focus rings are Highlight.
- **The YouTube panel's Part row** (2026-11; `VideoOverlay`, SITE-MEDIA-SPEC §5.1): the same row
  (`lib/part-row.ts`: the trimmer above, its texts and rules), under the quality rows and the "Audio"
  select, before the status line. Under Audio rather than between the rows and Audio: Audio belongs
  to the rows (it changes their sizes and what each one sends), while the part is a separate choice
  about whatever row is activated next; it also keeps the order of the panel over any video. Shown
  only while the qualities are listed with a known duration (`videoInfo.durationMs > 0`; never for a
  live stream or an unknown length) and the connected Downex lists `clips` (`downex:video-features`,
  asked when the panel opens and again when the qualities arrive, since Downex may connect only for
  them); an older Downex gets no row (and the relay client would refuse the fields with
  `outdated_downex`). The YouTube panel has no short form: it is never taller than the room left in
  the frame (the popover cannot draw past it), and with less than 160 px under the button (a small
  embedded player) it goes over the button (`data-covered` hides the button meanwhile), down to the
  frame's bottom; there it scrolls, with the overflow fade, so the row is never dropped or drawn out
  of sight. Opening the row scrolls the panel (smoothly, unless reduced motion) so the whole row
  shows; frames up to 420 px tall get a slimmer track and no end labels.
  - The timeline's video is the player's own `<video>` (`.html5-main-video` of the player the overlay
    is mounted in, else its first `<video>`; `playerVideo`).
  - While the row is open with a valid part, every row's size is the part's estimate, always "~"
    (the desktop's `ClipSize`, SITE-MEDIA-SPEC §5.3: the size × (To − From + 5 s, rounded up to a
    second) ÷ the duration; `clipSize` in `video-picker.ts`), in the visible size and the row's name.
  - A row activated while an error shows sends nothing: the chip the error is about (the one typed
    into) opens as a field and takes the focus. Another video (a new key) closes and empties the row.

### 5.3 Popup and badge
- **"Videos on this page"** (it replaces "Media on this page"):
  - Site videos first, as now: curated or YouTube page videos, and embeds from
    `webNavigation.getAllFrames`.
  - Then one group per frame ("This page", or "Embedded: <host>").
  - Each group holds its stream rows (format, host and path tail, "Choose quality" probes on expand
    with `downex:tab-stream-probe`) and its file rows (label of §5.2, Download →
    `downex:add-file`).
  - Short files sit in a collapsed "Short clips (n)" at the end. Segment-like files are never shown.
- **Folding.** A frame whose address is a curated video, or the top frame of a YouTube page, folds
  its network rows under that video's row as "More sources (n)". `*.googlevideo.com` manifests stay
  hidden on YouTube tabs.
- **Protected and live.** A frame whose reported elements are all DRM shows its rows as "Protected"
  without Download. One whose elements are all live is left out.
- **"This tab" card** gains "Show Download buttons on <host>" (`downex:set-site-hidden`). It is
  locked on, with the reason, when a wildcard pattern hides the host.
- **"Find the video on this page"** stays for tabs without a curated or YouTube video.
- **Audio pages** (*amended 2026-11-18*, `SITE-MEDIA-SPEC.md` §3.13). An answer whose `container`
  is `m4a` or `mp3` lists audio tracks only: the rows are "Audio" with the codec, rate and size
  ("Audio · AAC · 96 kbit/s · 1.7 MB"; the track's note leads when it has one), a row sends its track
  as the audio of the pick (no video), the card's line says the file type ("SoundCloud · M4A"), and no
  Part row is shown. A curated `audio` address (§3.8) shows its page row and counts in the badge from
  its address alone.
- **Posts and share links** (*amended 2026-11-18, review*). A `post` or `link` address (§3.8) only
  may hold a video: most posts and pins are text or images. Its page-video row and its badge count
  appear only once a frame of the tab reported a video that plays (`FrameReports.playable`, as the
  look-ahead requires); until then the tab offers "Find the video on this page". Other kinds count
  from their address alone.
- **Badge** (blue count; `!` still wins). It counts groups:
  - each page video (curated or YouTube), which absorbs its frame's network items
  - each frame with at least one stream that was not refused
  - each file that is neither short nor segment-like

  Not counted:
  - frames whose reported videos are all DRM or all live
  - refused streams
  - short and segment-like files

  The hidden-sites option hides buttons only; the popup and the badge still list and count.

### 5.4 Options and per-site off
- **Options page "Videos":**
  - "Show a Download button over videos" (`videoButton`, default on)
  - "Don't show the button on these sites" (`hiddenSites`, one pattern per line, the same syntax and
    validation messages as the excluded hosts)
  - the existing "Prepare video qualities while you watch" (its hint now names the curated sites)
  - "Use my sign-in for videos that need it" (`useSignInForVideos`, on by default since
    2026-11-18; SITE-MEDIA-SPEC §6.3)
- **Which host.** The decision uses the **tab's** host, so a site's embedded players follow the
  site. Hiding applies to YouTube too.
- **Updates.** Content scripts re-ask `downex:overlay-state` on `storage.onChanged` of `videoOptions`.
  A hidden site's layers are removed; a shown one mounts again.

### 5.5 Look-ahead on curated sites (amends SITE-MEDIA-SPEC §5.6, BROWSER-SPEC §4)
- **Dwell.**
  - The top frame's tracker runs `PrefetchDwell` (`lib/video-prefetch.ts:73`; 1.5 s, visible,
    unchanged) with `DwellView.enabled = lookAheadAllowed(options) && buttonAllowed(options, its own
    host)`. `dwellKey` keeps its rule; Lane D widens `prefetchVideo` (:36) to curated `watch`/`clip`
    videos with `lookAhead: true` (YouTube: `watch`/`shorts`, as today).
  - The dwell restarts (`sync()`) on SPA navigation (`wxt:locationchange`, `popstate`, `pageshow`),
    on `visibilitychange` and on a new video in the frame.
  - YouTube keeps its own dwell in `youtube.content.ts:74-91` (no video condition).
- **Background.**
  - `lookAheadRequest` (:131) accepts curated look-ahead sites under the same checks (frame 0, active
    tab, not private, the sender's own origin).
  - On sites other than YouTube the background also requires a meaningful, non-DRM, non-live video in
    some frame of the tab, from the element reports (§2.4): a curated address without a playing
    video (a text-only Reddit post, a deleted video) costs no yt-dlp run. The video may sit in a
    sub-frame, which is why the background, not the top frame, decides this.
  - The global budget (10, then 1 per minute) and the single look-ahead in flight are unchanged.
  - The background's `enabled` check (`background.ts:445`, today `lookAheadAllowed(options)`) also
    requires `buttonAllowed(options, host of the tab)`. `lookAheadAllowed` itself keeps its
    signature (§3.7).
- **Throttles per site.**
  - `site_unavailable` or `auth_required` with a rate-limit or bot-check wording pauses look-aheads
    for **that site** for 30 min.
  - On sites other than YouTube, a plain `auth_required` pauses that site for 30 min too: such
    sites need a sign-in the look-ahead never sends.
  - A refusal on one site never pauses another.
  - `tools_installing`/`tools_unavailable`/`site_media_off` still pause all look-aheads until the
    user's next video request.
- **No cookies.** A look-ahead never carries cookies (unchanged).
- **Pointing at the button** (*amended 2026-11-19*). When the pointer enters a Download button of
  the top frame of a curated video address whose dwell is still being counted, the dwell ends at
  once (`PrefetchDwell.now`, `TrackerApi.lookAheadNow`): the same look-ahead request goes out, under
  every rule above (options, visibility, the background's checks, budget and throttles), so the
  click usually finds the qualities already got. Nothing else changes; a stay already reported is
  not reported again.

### 5.6 Texts (extension; with SITE-MEDIA-SPEC §5.5)
- Group and action texts:
  - "This video", "More sources (n)", "Other videos on this page (n)", "Short clips (n)"
  - "Look for more qualities", "Don't show on <host>", "Show Download buttons on <host>"
- Source tags: "File", "Stream", or the site name.
- `media_unsupported` for a stream: "No MP4 video in this stream."
- Without the `streams` feature: "Update Downex to choose a stream quality."
- `expired` (the item is no longer listed): "This video is no longer listed. Play it again, then
  download it."
- A file `add` that fails with `http_status`/`network`: the code's text followed by "The address may
  have expired. Play the video again, then download it."
- `encrypted` and `live` (also for elements refused at click time): the SITE-MEDIA-SPEC §5.5 texts.
- An element without any source: "Downex found no video it can save here." plus "Look for more
  qualities" when yt-dlp was not asked yet. On a `network` site (§3.8, *2026-11-18*): "Downex can't
  read this site's videos yet. Play the video, then try again." while the video has not played
  (its `played` ranges are empty and its position is 0), else "Downex can't save this site's videos
  yet." (a `blob:` or MSE player that gave the browser nothing to list; playing again never
  helps). *Amended 2026-11-18:* these two texts stay only for elements without a Played source
  (Firefox, and links of the context menu). The feed texts "Open this video's own page to download
  it." and "Open this video's comments, …" are removed (§5.1 "Feed videos").
- Played (SITE-MEDIA-SPEC §2.9, *2026-11-18*): the source tag "Playing"; "Getting this video…"
  while its files are not known; after 20 s "Downex hasn't seen this video load yet. It appears here
  as soon as it plays."; "Update Downex to save this video." for a relay without `played`; a Site
  source that failed while another row exists says nothing; one that failed with no row left says
  "Downex couldn't get this video from the site." on any site but YouTube.
- Compact buttons (§5.1 "Social sites", *2026-11-18*): the label "Download", widened with the
  hint "Download · 1080p · 24 MB" once known; "Sent to Downex" after Downex took the video;
  accessible name "Download this video with Downex" (with the hint: ", 1080p, 24 MB"; sent:
  ", sent to Downex"); a tile's "Download the video of this post with Downex".

### 5.7 Desktop and CLI
See §3.9. The completion view of a stream reads "HLS stream, saved as MP4" (`mediaFormatText`,
`ui/frontend/src/lib/progress.ts:220`, unchanged; the "· quality ·" form is for site downloads only).
A legacy record still reads "…, saved as MPEG-TS".

## 6. Security and privacy
0. **What is played** (*amendment 2026-11-18*, SITE-MEDIA-SPEC §2.9). `PlayedLog` keeps, per tab and
   for 30 minutes, the address, total, type hint, frame and request times of the media responses
   its pages fetched; never a body, a header value or a cookie; never of a private window; never the
   extension's own requests. It leaves the browser only on a click, to the local Downex, as the
   addresses and the first 64 KiB of at most 16 of those files (read again by the extension, as the
   player read them). The option "Watch videos as they play" turns it off. A Played ticket is bound
   to exactly those addresses.
1. **Untrusted input everywhere.**
   - Response headers, URLs, element state, content-script messages, relay answers and every byte of
     a TS fragment are untrusted.
   - The parsers are bounded (§2.4, §3.6, §4.7) and fuzzed (§8.2).
   - The content script's messages are validated in `isExtensionMessage` (types, lengths, counts).
2. **Keys, not addresses.**
   - A content script or the popup names items by key (or by URL looked up in the tab's store). The
     background acts only on items it recorded itself for that tab, frame and document.
   - The one exception is the element file (§2.6). It is built only from the element's own
     `currentSrc`, http(s) with an `.mp4`/`.m4v` path, never carries recorded headers or cookies, and
     is sent only on the user's click.
   - The feed post address (§5.1 "Feed videos", *2026-11-18*) is the other one. It is a link's
     `href` near the clicked video, read only on the click. The background takes it only as a
     curated video address on the sender frame's own origin: the same check as
     `downex:page-video-probe`'s `url`, so a page can name nothing through it that it could not
     name through that message. It goes only to the local Downex.
   - Headers and full URLs never reach a web page's DOM: the panel lives in a closed shadow root and
     shows host + last path segment.
   - A closed shadow root is an isolation boundary for the DOM API, **not a confidentiality
     boundary**: page script can read the text of a shown panel (Chromium: `window.find` plus
     `Selection.modify`). So everything a panel shows is treated as readable by the page that hosts
     it: a panel lists only its own frame's items, and only the top frame's panel names the tab's
     title and host.
3. **Cookies.**
   - A file hand-off carries the `Cookie` the browser itself sent with that request (recorded by
     `onSendHeaders`, with `extraHeaders` on Chromium), and nothing else. It never uses
     `cookies.getAll` (§9 #3).
   - Streams keep the M5 rules (BROWSER-SPEC §5).
   - yt-dlp probes keep SITE-MEDIA-SPEC §6.3 (amendment 2026-11-18): never a Cookie for a
     look-ahead; a user's probe or add first without one, then once more with the site's sign-in
     (a curated site's fixed domain, a generic page's exact host) only after `auth_required` and
     only with "Use my sign-in for videos that need it" on.
   - In the daemon, cookies go only to the origin they came from (MEDIA-SPEC §7; the engine's
     redirect rules for files).
4. **What runs automatically.** Nothing reaches Downex while the user only browses, except the
   curated look-ahead (§5.5). Downex runs only when the user acts:
   - **Stream probes:** when a panel opens (a click), or when the user expands a popup row.
   - **yt-dlp on other pages:** only on a click, and never with cookies.
   - **Hand-offs:** every hand-off is a click.
   - **Daemon start:** a look-ahead never starts downexd (unchanged).
5. **Storage.**
   - Items with their headers live in `storage.session` (cleared with the browser session, inside
     the 2 MiB budget, headers dropped first). Element reports stay in memory only.
   - `hiddenSites` lives in `storage.local`.
   - The history keeps URLs without query, as today.
6. **Page isolation.**
   - The tracker and UI run in the isolated world.
   - The panel's text is readable by its page (§6.2): it holds nothing the frame's own page could
     not know.
   - Events on the layer are isolated, and only trusted clicks act.
   - Nothing is inserted into the site's player.
   - There are no web-accessible resources, so a page cannot tell that the extension is installed
     until it shows a button.
7. **Remuxer.** It writes only `<name>.dxpart` in the download folder and reads only the job's
   fragment files. It runs no external program and never decrypts.
8. **Clean room.** The curated shapes, overlay, wording and remuxer come from this spec, public
   standards and the sites' public addresses (§3.8). Reviews check that no other product's strings,
   icons or flows appear in code, comments, tests or commit messages.

## 7. Lane plan
Lanes run in parallel after Step 0. **Each file has exactly one owner among the lanes.** Files that
Step 0 creates or edits pass to the lane named next to them; files marked *frozen* are not edited
after Step 0. No file is in two lanes. The docs Step 0 edits and the e2e/README/layout-notes files are
edited again only by the integration step, after every lane. A lane that needs a file it does not
own asks the integration step.
- **Merge order:** Step 0 → Lanes A, B, C, D, E (any order) → Integration.
  - Lane B's unit tests use `Options.CheckTS`/`Assemble`/`ConcatTS` fakes (Lane A's real converter
    and `internal/testmedia` are not there yet), Lane C needs nothing from B (the wire contract of
    §3.5 and the existing `ProbeMedia` suffice), and Lanes D and E meet only in Step 0's frozen types
    and the exports listed in §3.7, whose signatures stay. So no lane waits for another.
  - The real TS path (Lane A's `Assemble` under Lane B's job) is first exercised end to end by the
    integration step.
  - The lanes share one working tree. A lane runs its own packages' tests (`go test ./<pkg>/...`,
    `pnpm exec vitest run <its files>`) while it works; the whole-tree gates (`go build ./...`,
    `pnpm check`) are run at the end of the lane and again by the integration step.
- **No disturbance:**
  - no lane builds into `bin\`, starts the user's own daemon or touches the real tools folder
  - nothing is committed
  - tests use temporary folders (the shell's AppData is virtualised)
  - no test reaches the network

**Step 0 — contract freeze** (one agent; afterwards `go build ./... && go vet ./... && go test
./...` and `cd ext; pnpm check; pnpm test` pass). Step 0 also makes every existing consumer of a
changed type compile, editing only what the build or typecheck requires; those files then pass to
their lane as listed below.
- **Docs (policy and pointers):**
  - `docs/BROWSER-SPEC.md` §2 table: `probeStream`, `addMedia` tracks, `added.awaiting`, feature
    `streams`.
  - BROWSER-SPEC §4 and §5: pointers to this spec, plus the file types of §2.4.
  - `docs/SITE-MEDIA-SPEC.md` §1: the curated-sites exception. §3.11: the manifest fallback accepts
    convertible TS. §5.1 and §5.6: pointers to §5.1 and §5.5 here.
  - `docs/MEDIA-SPEC.md` §5: pure-Go TS→MP4 replaces the TS/FFmpeg note. §6 steps 1, 3 and 5, and
    §7: TS video + TS audio now supported, the `<video>` sentence, legacy `.ts`.
  - `docs/CLEAN-ROOM.md`: one line saying site URL shapes come from public addresses, never from
    yt-dlp's extractors.
  - `docs/REQUIREMENTS.md`: a milestone row "Video detection on every site".
- **Proto:** comments of §3.1 in `proto/downex/v1/downex.proto`, regenerated into
  `internal/rpc/downex/v1/` with `buf generate` (doc comments only).
- **Go:**
  - `internal/media/tsconv.go` + `tsconv_test.go` (new; `TSConvertible`), *frozen*.
  - `internal/remux/remux.go`: `Input.Breaks`, `Input.Language`, exported `ConcatTS` (the existing
    `concatTS`/`copyTSFile` and the output wrapper behind it, unchanged; `Assemble` still uses it for
    one TS input); `internal/remux/checkts.go` (new; `CheckTS` and `MaxCheckTSBytes`, stub returning
    nil) → **Lane A**.
  - `internal/mediajob/mediajob.go`: `Options.LegacyTS`, `CheckTS`, `ConcatTS` (fields only; the
    defaults in `withDefaults`) → **Lane B**.
  - `internal/daemon/media.go`: `MediaRunOptions.LegacyTS` (field only) → **Lane B**.
- **Extension:**
  - `ext/src/lib/offer-types.ts` (new; §3.6 types and constants, `StreamEntry` included), *frozen*.
  - `ext/src/lib/messages.ts` + `messages.test.ts`: every message of §3.6 with validation, and the
    new `TabMediaResponse`, *frozen*.
  - `ext/src/lib/options-form.ts` + `options-form.test.ts`: `VideoOptions.hiddenSites`,
    `parseVideoOptions`, `buttonAllowed`, `parseHiddenSites(text) → {sites, invalid}`,
    `formatHiddenSites`, *frozen*.
  - `ext/src/lib/protocol.ts`: `FEATURE_STREAMS`, `ProbeStreamFields`, the `probeStream` request and
    result types, the track fields of `AddMediaFields` (types only) → **Lane D**.
  - `ext/src/lib/site-video.ts`: widened types (the `SiteName` union of §3.8, `SiteVideo.lookAhead`,
    `SITE_NAMES` with all ten names), with the YouTube parser unchanged apart from setting
    `lookAhead` → **Lane D**.
  - `ext/src/lib/history.ts`: `HandoffSource` gains `video-file` → **Lane D**.
  - `ext/src/lib/ui-format.ts`: the `SOURCES` label of `video-file` ("Video file") → **Lane E**.
  - Compile fixes only (→ **Lane D**): `ext/src/entrypoints/background.ts` (the `downex:tab-media`
    answer gets `files: []`, `hiddenHere: false`, `host` and stream `key`/`frame` placeholders),
    `ext/src/lib/background-prefetch.test.ts` and `ext/src/lib/video-handoff.test.ts` (`VideoOptions`
    literals gain `hiddenSites: []`), and any other Lane D file `pnpm check` names.
  - Compile fixes only (→ **Lane E**): any Lane E file `pnpm check` names (e.g. `popup/main.ts`
    reading `TabMediaResponse`).

**Lane A — TS→MP4 remuxer** (Go; the largest lane)
- **Files:**
  - `internal/remux/mpegts/` (new): `doc.go`, `packet.go`, `psi.go`, `pes.go`, `demux.go`,
    `probe.go`, `errors.go`, their tests, `fuzz_test.go`, `testdata/fuzz/`.
  - `internal/remux/`: `remux.go`, `checkts.go`, `mp4.go`, `scan.go`, `init.go`, `fragment.go`,
    `ts.go`, `tsvideo.go`, `tsaudio.go`, `tstime.go` (new), and every `*_test.go`, `testdata/`.
  - `internal/testmedia/` (new): `doc.go`, `bits.go` (bit writer, exp-Golomb, emulation prevention),
    `h264.go`, `hevc.go` (parameter sets and access units), `adts.go`, `ts.go` (PAT/PMT with CRC,
    PES, PCR, continuity counters, adaptation stuffing, scrambling bits, a chosen stream type),
    `hls.go` (playlists with discontinuities), `mp4.go` (progressive MP4 via mp4ff), and their tests.
- **Deliverables:** §4 in full: `mpegts`, `CheckTS`, `Assemble` for TS (H.264, HEVC, AAC; muxed and
  separate audio; discontinuities; progressive and fragmented), `ConcatTS` unchanged.
- **Tests:**
  - Muxed H.264 + AAC → progressive MP4, decoded with mp4ff:
    - stts, ctts, stss, edts, stsc, stsz, stco
    - avcC dimensions; esds
    - every payload equal to the generator's length-prefixed access unit or raw AAC frame
  - Cases:
    - B-frames (PTS ≠ DTS)
    - a start PTS just below 2³³ (wrap)
    - two discontinuities with timestamp jumps (A/V offset kept within 1 ms, no gap)
    - a continuity gap (that PES dropped)
    - units before the first IDR dropped
    - a parameter-set change → second `sdi`
    - HEVC with VPS/SPS/PPS → `hvc1`/`hvcC`
    - TS video + separate TS audio rendition
    - audio-only TS
    - an AC-3 stream type → `ErrUnsupported`
    - scrambled packets and PES → `media.ErrEncrypted`
    - filler bytes → `ErrMalformed`
    - data before the first packet (resync)
    - several AUDs in one PES → `ErrUnsupported`
  - Bounds tests (`bounds_test.go` style): an endless PES, many tiny ADTS frames, PSI floods.
  - Fragmented fallback with `maxProgressiveSamples` lowered; `co64` through `largeThreshold`;
    cancellation removes the output.
  - The fMP4 suite unchanged and green.

**Lane B — job, daemon, CLI, desktop texts** (Go + small UI)
- **Files:**
  - `internal/mediajob/`: `mediajob.go`, `probe.go`, `run.go`, `output.go`, and every
    `internal/mediajob/*_test.go` (`mediajob_test.go`, `identity_test.go`, `probemp4_test.go`,
    `helpers_test.go`, `workers_test.go` (it serves `.ts` segments of filler bytes), `ranged_test.go`,
    `ranged_safety_test.go`, `ts_test.go` (new)). `testOptions` (`helpers_test.go:252`) installs a
    passing `CheckTS` fake by default, so filler TS keeps working once Lane A's real `CheckTS` lands.
  - `internal/sitemedia/`: `offers.go` (doc comments naming `mediajob.Probe`, offers.go:22), `info.go`,
    `label.go` and their tests (`offers_test.go`, `label_test.go`; curated display names).
  - `internal/daemon/`: `media.go`, `confirm.go`, `media_test.go`, `media_fake_test.go`,
    `media_adapter_test.go`.
  - `cmd/downexd/`: `media.go`, `media_test.go`, `site_test.go` (its `mediajob.Probe` call, :513, and
    the two TS-refusal tests, :458 and :497, which now expect an MP4 offer).
  - `cmd/downex/`: `media.go`, `media_test.go`.
  - `ui/frontend/src/lib/`: `media.ts`, `mock.ts`, `api.ts` (comment), `components/AddDialog.svelte`,
    and the tests `media.test.ts`, `mock.test.ts`.
- **Deliverables:** §3.3 (ProbeMP4 for MANIFEST, TS candidates, the second offer round, pairing,
  legacy rule, removal of `mediajob.Probe`), §3.9, §4.8, the display names of §3.8.
- **Tests (fakes; no real TS needed):**
  - MANIFEST probe lists only MP4-savable tracks (WebM Representation, AC-3-in-TS `CODECS`,
    audio-only → `media_unsupported`), TS candidates with and without `CODECS`, packed-audio
    playlists left out, container `"mp4"`.
  - Second offer round: a 720p TS variant with `ac-3` above a 720p TS variant with AAC lists the AAC
    one.
  - Pairing TS/fMP4; a silent video-only presentation is listed.
  - `CheckTS` fake refusing → no further fragment fetched (hit counts) with each code; an unknown
    container sniffed as neither TS nor fMP4 → `media_unsupported` before other fragments.
  - `LegacyTS`: a stored `"ts"` record with a checkpoint resumes through `ConcatTS` without `CheckTS`
    and keeps its name; a `"ts"` record without a checkpoint (never started, or after `restartMedia`)
    runs as MP4 under a `.mp4` name reserved with `freeName`.
  - A non-legacy run returning `"ts"` fails and removes its output and part folder.
  - `cmd/downexd`: the site manifest fallback offers and saves a TS variant as MP4 (with the fake
    assembler).
  - CLI `--list` without TS/WebM-only tracks; UI hint and mock.

**Lane C — relay** (Go)
- **Files:** `cmd/downex-nmhost/`: `protocol.go`, `relay.go`, `protocol_test.go`, `relay_test.go`,
  `media_test.go`, `video_test.go` (asserts exactly two features today, :131-133), `daemon_test.go`,
  `helpers_test.go`, `stream_test.go` (new).
- **Deliverables:** §3.5.
- **Tests:**
  - `probeStream` parsing and limits; the MANIFEST `ProbeMedia` sent.
  - The pool shared with video requests (capture adds still answered under load); the 30 s cap and
    margin.
  - `videoInfo` encoding without URLs; `reason` forwarding.
  - `addMedia` tracks (`validate.TrackID`, missing = default).
  - `awaiting` on `add`/`addMedia`; `features` contains `streams`.

**Lane D — extension background** (TypeScript)
- **Files:**
  - `ext/src/entrypoints/background.ts`
  - `ext/src/lib/`:
    - changed: `media.ts`, `media-handoff.ts`, `video-handoff.ts`, `video-prefetch.ts`,
      `site-video.ts`, `protocol.ts`, `history.ts`, `context.ts`
    - new: `frame-media.ts`, `frame-offers.ts`, `stream-probes.ts`, `file-handoff.ts`
    - tests: the `.test.ts` of each (`media.test.ts`, `media-handoff.test.ts`,
      `video-handoff.test.ts`, `video-prefetch.test.ts`, `site-video.test.ts`, `protocol.test.ts`,
      `context.test.ts`, and new ones for the new modules), plus `background-media.test.ts`,
      `background-video.test.ts`, `background-prefetch.test.ts` and `background-frames.test.ts` (new)
  - Not owned and not changed: `capture-firefox.ts` (`parseContentDisposition` is only imported),
    `rules.ts` (`normalizeHostPattern`, `hostMatches`), `handoff.ts`, `runtime.ts`.
- **Deliverables:** §2.4, §2.6 (the background side), §3.6 (handlers), §3.7, §3.8, §5.3 (badge and
  `tab-media` data), §5.5 (background, `prefetchVideo` for curated sites), §6.2–§6.5. The exports Lane
  E uses (§3.7) keep their signatures.
- **Tests:**
  - **Classification:** every type, MIME and status row of §2.4, including XHR MP4 never listed,
    attachments, `fenced_frame` and prerender.
  - **Detection details:** `Content-Range` totals; redirect map (`currentSrc` matches `requestUrl`);
    range grouping without storage writes; query-token merge.
  - **Frames:** frame and document clearing; main-frame pending until commit.
  - **Store:** caps and budget; short and segment-like rules; Firefox `urlClassification`.
  - **Keys:** bound to frames; a sub-frame cannot use the top frame's keys.
  - **Hand-offs:**
    - file fields: first URL, recorded Cookie only, no `cookies.getAll`, filename rules, `.m4v` →
      `.mp4`
    - stream probe cache (2 in flight, refusals kept)
    - `addMedia` tracks only with `streams`
    - `awaiting`
  - **Curated table** positives and negatives; per-site throttles; Instagram/Facebook never
    looked ahead; no look-ahead without a meaningful video reported in the tab.
  - **Other:** UI injection once per document; `frame-media-seen` rate; badge counting rules.

**Lane E — extension page UI** (TypeScript)
- **Files:**
  - `ext/src/entrypoints/`: `video.content.ts` (new), `video-ui.ts` (new, unlisted),
    `youtube.content.ts`, `popup/index.html`, `popup/main.ts`, `popup/style.css`,
    `options/index.html`, `options/main.ts`, `options/style.css`.
  - `ext/src/lib/`:
    - new: `video-tracker.ts`, `overlay-host.ts`, `offer-panel.ts`, `offer-rows.ts`
    - changed: `video-overlay.ts` (the `OVERLAY_CSS` split of §5.1), `video-panel.ts`,
      `video-picker.ts`, `media-panel.ts`, `ui-format.ts`, `manifest.test.ts`
    - every `.test.ts` of these (`video-overlay.test.ts`, `video-panel.test.ts`,
      `video-picker.test.ts`, `media-panel.test.ts`, `ui-format.test.ts`, and new ones)
  - `ext/src/assets/ui.css` if needed.
  - Not owned and not changed: `entrypoints/content.ts` (click reporter), `entrypoints/confirm/`.
- **Deliverables:** §2.5, §2.6 (rows), §5.1–§5.4, §5.5 (the tracker's dwell), §5.6. Tests of the
  tracker inject the curated parse and the dwell, so they do not depend on Lane D's table. The
  exports Lane D uses (§3.7) keep their signatures.
- **Tests (happy-dom, with injected observer, rectangle, timer and `sendMessage` providers):**
  - Meaningful-video rules: DRM (`mediaKeys`, `encrypted`), live, `srcObject`, size, visibility,
    decorative; the re-check at click.
  - At most 8 buttons; no subtree `MutationObserver` (asserted); the deep scan bound.
  - Placement from rectangles; fullscreen re-parenting.
  - `isolateEvents`; Esc and focus return.
  - Row order, dedupe and default row; "More sources" folding; polling every 2 s keeps focus.
  - Hidden-site action; popup groups, short clips, "Protected"; options editor.
  - YouTube honours `hiddenSites`.
  - The manifest: the new content script's matches, `all_frames`, `match_about_blank`,
    `exclude_matches`, no MAIN world, no `web_accessible_resources`, permissions unchanged, and
    `video-ui.js` produced as an unlisted script (the build step checks that file).

**Integration step** (one agent, after all lanes):
- **e2e:** `internal/e2e/media_test.go` (TS tests switched to `internal/testmedia`: the filler
  `tsSegment` (:481) cannot be converted; `TestMediaHLSTransportStream` (:639) now expects
  `episode.mp4` and container `"mp4"`), `internal/e2e/videodetect_test.go` (new; §8.3 #1–#10, #10
  reusing the stand-in yt-dlp helpers of `site_test.go`, which stays unchanged),
  `internal/e2e/nmhost_test.go`, `internal/e2e/harness_test.go` (§8.3).
- **Fix-ups across lanes:** whatever the joint gates show (e.g. Lane B tests that relied on Step 0's
  concatenating `Assemble`), in the failing lane's files.
- **Docs:** §10 of this spec; the repository layout notes (`mpegts`, `testmedia`, extension modules);
  BROWSER-SPEC, MEDIA-SPEC and SITE-MEDIA-SPEC status lines; `REQUIREMENTS.md` (the M5 row's "HLS
  master + MPEG-TS → `.ts`" and the new milestone row); README (`.ts` mentions at :18, :312, :342,
  :812, :821).
- **Gates:**
  - `go build ./...`, `go vet ./...`, `CGO_ENABLED=1 go test -race ./...` (WinLibs GCC 15.2)
  - `GOOS=darwin GOARCH=arm64` and `GOOS=linux GOARCH=amd64` builds with `CGO_ENABLED=0`, plus
    `go test -c` of the touched packages
  - `go list -deps ./cmd/... ./ui` without `internal/testmedia`
  - `cd ext; pnpm check; pnpm test; pnpm build` (`video-ui.js` present, no
    `web_accessible_resources`)
  - `cd ui/frontend; pnpm check; pnpm test`
  - `go vet -tags desktop,production ./ui`
  - `scripts/build-windows.ps1 -Out <temp dir>` (never `bin\`)

## 8. Test plan

### 8.1 Unit and package tests
Listed per lane in §7. Common rules:
- No test reaches the network; servers are `httptest` or `internal/testserver`.
- Generated media only: `internal/testmedia` and mp4ff. No ffprobe or other external tool.
- Fake clocks wherever timing matters (look-ahead budgets, panel polling, short-clip windows).
- The extension uses happy-dom. Layout, `IntersectionObserver`, `ResizeObserver`, EME and
  `openOrClosedShadowRoot` are injected, because happy-dom has none of them for real.
- Every untrusted input has a table of malformed cases: response headers, element reports,
  messages, relay answers, TS bytes.

### 8.2 Fuzz targets (native Go fuzzing; corpora seeded from `internal/testmedia`)
- `mpegts.FuzzDemux`: bytes as one or two fragment files. It must never panic, allocation is bounded
  (the `bounds_test.go` `TotalAlloc` pattern), and every unit is at most 32 MiB.
- `mpegts.FuzzProbe` and `remux.FuzzCheckTS`: never panic; only the declared errors.
- `remux.FuzzAnnexB`: the converted size equals the computed size, with no start code left in
  sample data.
- `remux.FuzzADTS`: frame splitting across arbitrary PES cuts equals splitting the whole stream.
- `remux.FuzzTSTwoPass`: pass-1 sizes equal pass-2 bytes for any input that passes pass 1; the
  output, when produced, decodes with mp4ff.
- `remux.FuzzTSTime`: unwrapping and domain offsets are monotonic and never overflow.
- The extension's pure parsers (`classifyFile`, `Content-Range`, Content-Disposition, the curated
  table, message validation) get property-style tests over random strings in vitest.

### 8.3 Real-process end-to-end (`internal/e2e`, skipped with `-short`)
- **Setup.** Built `downexd` (e2etest tags), `downex` and `downex-nmhost`, a temporary `--db`, and a
  gated, hit-counting local server (`streamServer`) serving fixtures generated in the test.
1. **TS → MP4 (CLI).** An HLS master with two TS variants (H.264 + AAC muxed, B-frames, a start PTS
   near 2³³):
   - `downex media <master> --wait` saves `episode.mp4`
   - `checkMP4` verifies `avc1` + `mp4a`, sample counts, avcC dimensions, payload equality and the
     duration within one frame
   - only the chosen variant's segments are fetched, each once
2. **Separate audio rendition.** A TS video variant + a TS AAC rendition (`EXT-X-MEDIA`) give one MP4
   with both tracks, whose start offset matches the generated one (±1 ms).
3. **Discontinuities.** Three domains with timestamp jumps give a continuous MP4 whose duration is
   the sum of the domains.
4. **Early refusals.**
   - A muxed TS with an AC-3 stream type and no `CODECS` → `media_unsupported` after the first
     segment only (hit counts).
   - Scrambled packets → `encrypted` likewise.
   - A master whose `CODECS` name AC-3 → refused at probe time, nothing fetched.
5. **Legacy.** A record seeded with container `"ts"` through `internal/store` before downexd starts
   (with a partial checkpoint and fragments in `<name>.ts.dxparts`) resumes into a byte-identical
   `.ts`. A second seeded `"ts"` record without a checkpoint completes as `<name>.mp4`.
6. **Pause, kill, resume** of a TS download: only fragments without a stored bit are fetched again,
   and the MP4 is byte-identical to an uninterrupted run (conversion is deterministic).
7. **Browser relay** (piped stdio):
   - `hello` → `features` includes `streams`
   - `probeStream` of the master → `videoInfo` with `container: "mp4"`, `site: ""`, no URL
   - `addMedia` with `videoTrack` = the lower quality → `added {awaiting: true}`, and the stand-in
     `downex-ui` is started with `--confirm <id>`
   - `downex confirm <id>` → an `.mp4` of that quality (picture size checked)
8. **Direct MP4 (relay `add`).**
   - One server serves `/v/clip` and requires the Cookie there; it answers with a 302 to a second
     server (another port, so another origin) that serves a generated progressive MP4 (about 6 MiB) at
     `/cdn/clip.mp4?sig=…`, with ranges.
   - `add {url: /v/clip, headers: [Cookie, Referer, User-Agent], filename: "Clip.mp4", mime:
     "video/mp4", size}` → `added {awaiting}`.
   - After confirm: several ranged connections; the file `Clip.mp4` is byte-identical; the Cookie
     reached only the first server (the engine drops it on a cross-origin hop,
     `internal/transport/redirect.go:46-51`).
9. **MP4-only manifests.** A DASH MPD with WebM and MP4 Representations lists only MP4. An audio-only
   HLS → `media_unsupported` through CLI and relay alike.
10. **Site fallback with TS.** The stand-in yt-dlp (`fakeTool`, `DOWNEX_E2E_FAKE_YTDLP`, downexd
    from `bin.downexdHooks`) returns only `m3u8_native` TS formats for a page. `downex media --site`
    lists its MP4 qualities and saves an `.mp4` (SITE-MEDIA-SPEC §3.11 as amended).

The extension itself has no real-browser e2e. Its logic is covered by the package tests above, and
the browser behaviour is checked by the user (§8.4).

### 8.4 Real-provider checks left to the user (reported as unverified until done)
- **Browsers.** Chrome/Edge (including the `browser` namespace) and Firefox 140+ on real pages:
  - **Players:** `<video src>` MP4 sites; HLS-TS players (news sites, Dailymotion); DASH players;
    players in iframes and `srcdoc`/`about:blank` frames (including the background's
    `scripting.executeScript` of `video-ui.js` into them); shadow-DOM players (web-component
    players).
  - **Page conditions:** pages with strict CSP (Firefox style injection); transformed `<html>`
    (fixed positioning); fullscreen of a container and of the bare `<video>`.
  - **Detection:** native HLS playback (segment-like rule); worker and service-worker players
    (missed by design); ads before videos (short-clip rule).
- **Curated sites:** Vimeo (public and unlisted), X, Facebook, Instagram, TikTok, Dailymotion,
  Reddit, Twitch VODs and clips, Streamable, and the sites added on 2026-11-18 (§3.8):
  - the look-ahead timing and per-site throttles
  - `auth_required` behaviour without and with the sign-in option: the automatic second request
    with the site's cookies (SITE-MEDIA-SPEC §6.3) was checked against local stand-in pages only,
    never with a real account
  - the address shapes against what the sites show today
  - Threads and Snapchat: whether the browser fetches a whole MP4 there (File rows) at all
  - Rumble and Kick VODs (`network`): whether yt-dlp reads them from other networks (Rumble
    answered HTTP 403 for every address, Kick VODs "NOT_FOUND", from one machine on 2026-11-18),
    and whether their players give the browser a Stream row
- **In-page button on social sites** (2026-11-18, §2.5, §5.1): the button on TikTok, Instagram,
  Facebook, X and Pinterest feeds and post pages. Things to check:
  - whether the feed walk finds the clicked video's own post with today's markup on each site
    (TikTok For You is expected to find none and say so: the comments hint, then the button on the
    video's own address)
  - the panel's size over vertical players
  - quiet marks on landing pages with background loops
  - the Part row's duration from the element on Instagram
  - *the compact button (2026-11-18, §5.1 "Social sites")*: its free corner beside each site's
    own controls (like, comment and share rails, mute, menus, captions, progress bars), its size
    over feed cards, grid tiles and vertical players, carousels (the shown slide's own post), grid
    tiles (Instagram, X media, TikTok and Facebook profiles), Instagram stories and highlights
    with a real sign-in, post viewers opened over a feed, embeds of these sites on other pages,
    and the late-mount observer's cost on long feeds. Checked only in a local harness of stand-in
    pages (each surface, light and dark, at desktop and phone sizes) with the real tracker and
    layer.
- **Context menu** (2026-11-18): "Download video from this link with Downex" and "Show Downex
  download panel" in Chrome, Edge and Firefox (the grouping under "Downex", `blob:` players, players
  that cover their `<video>` with their own layer, closed shadow roots, sub-frames).
- **Signed CDN files.** Expiry of signed direct-file URLs during long downloads, and whether the
  first URL of a redirect chain renews them.
- **Playback of converted MP4s** in Windows Media Player / Films & TV, VLC, Chrome, Firefox and
  QuickTime:
  - H.264 with B-frames, HEVC, 5.1 AAC
  - streams with discontinuities (ads spliced in)
  - conversion of multi-GB VODs (time, disk, memory) and outputs over 4 GiB
- **Platforms.** macOS and Linux at run time.

## 9. Open questions (with the recommended default)
1. **Audio-only manifests from the CLI/desktop.** → **Refused** (`media_unsupported`), consistent with
   the 2026-10-22 "no audio-only" amendment. Revisit if users want podcasts from HLS.
2. **Legacy `.ts` records with a checkpoint.** → **Kept as `.ts`** (`LegacyTS`). Converting them
   would change their name and part folder under a stored checkpoint. Those without one: #16.
3. **Cookie fallback for direct files.** → **None:** only the Cookie the browser sent. The M5
   `cookies.getAll` fallback stays for streams only. With third-party cookies blocked it could send
   cookies the browser itself would not send.
4. **Packed-audio renditions, AC-3/E-AC-3/MP3 in TS.** → **Later** (phase 2 of the remuxer:
   `Dac3`/`dec3`, esds OTI 0x6B, the ID3 PRIV `com.apple.streaming.transportStreamTimestamp`). Until
   then `media_unsupported`, filtered at probe time where `CODECS` tells.
5. **Renewing expired direct-file URLs.** → **Later.** It needs a daemon RPC for a fresh URL from the
   extension, or a SITE resolve. The first URL of a redirect chain is used meanwhile.
6. **Per-element permalinks on feed pages** (the nearest `a[href]` of the clicked video on X,
   Facebook or Reddit timelines). → **Not in v1.** It reads page DOM beyond `<video>` (a policy
   question). Feeds get network rows plus yt-dlp on the tab's address.
7. **Daemon content sniff for direct files** (a WebM served as `video/mp4` would be saved as `.mp4`).
   → **Not in v1.** It is rare; an engine check of `ftyp` after the first chunk is a later
   candidate.
8. **Thresholds.** → Short = under 2 MiB or under 30 s; decorative = muted + loop + no controls +
   under 30 s; segment-like = 4 or more files in one directory. To be tuned after real-site checks.
9. **Conversion progress.** → **The existing assembling text without a percentage in v1**
   ("Assembling…" for streams, "Merging…" for site downloads, `progress.ts:80`). Later, an assembly
   progress callback could show "Converting to MP4… n %", along with a single-reader pass 2 that reads
   a muxed TS once instead of three times.
10. **`match_origin_as_fallback`** (players in `blob:`/`data:` frames). → **Not in v1.** Chromium
    needs 119+, and the frames are rare.
11. **Sign-in cookies per site** (so look-aheads keep working where the option is off). → **Keep
    the one global option.** Since 2026-11-18 (SITE-MEDIA-SPEC §6.3) look-aheads run with it on
    too: the click's first request is anonymous, and the sign-in goes only after `auth_required`.
12. **A visible "Protected" button on DRM videos** instead of none. → **No button** (user decision 1).
    The popup says "Protected".
13. **HEVC in TS.** → **In v1** (the same scheme as H.264 with `hvcC`). Lane A may deliver it after
    H.264 within the lane, never left out.
14. **Ad detection by host lists.** → **No.** Only size, duration and Firefox's own classification.
15. **Probe cost of MANIFEST adds.** `ProbeMP4` plans every offered media playlist (≤ 4 at once), and
    `AddMedia` re-probes before it records, inside the relay's 15 s `addTimeout`; `Probe` fetched
    only the selected ones. → **Keep `addMedia` on the add path with 15 s in v1**: masters have a
    handful of variants, and a timeout cancels the daemon's probe (nothing is recorded). Revisit
    (move `addMedia` to `videoTimeout`) if real checks show timeouts on slow servers.
16. **Legacy `"ts"` records that never started.** → **They become MP4 downloads at their first run**
    (§3.3): nothing on disk belongs to their name yet, so only records with a checkpoint keep the
    `.ts` join.

## 10. Status
Specified 2026-10-23; **implemented 2026-10-23 on Windows** (Step 0, Lanes A–E and the integration
step), then **revised by a review round the same day** (below). It is verified with unit, package and fuzz tests, simulated browsers (happy-dom and fake-browser
wiring tests), and real-process end-to-end tests with generated streams, local servers and the stand-in
yt-dlp. **It has not run in a real browser, on a real site, or in a real player** (§8.4). Nothing was
committed.

**Gates (rerun after the review round, 2026-10-23).**
- Go:
  - `go build ./...`, `go vet ./...` and `gofmt -l` are clean.
  - `CGO_ENABLED=1 go test -race ./...` (WinLibs GCC 15.2) passes: 36 packages, e2e included.
  - `GOOS=darwin GOARCH=arm64` and `GOOS=linux GOARCH=amd64` with `CGO_ENABLED=0`: `go build ./cmd/...
    ./internal/... ./ui` passes, as does `go test -c` of e2e, remux, mpegts, testmedia, media, mediajob,
    sitemedia, daemon, downexd, downex and downex-nmhost.
  - `go list -deps ./cmd/... ./ui` (also with `-tags desktop,production`) has no `internal/testmedia`.
  - `go vet -tags desktop,production ./ui` is clean.
- Extension: `pnpm check` is clean and `pnpm test` passes 35 files and 863 tests. `pnpm build` and
  `pnpm build:firefox` produce `video-ui.js`, and neither manifest has `web_accessible_resources`.
- Desktop: `cd ui/frontend; pnpm check; pnpm test` pass (536 tests).
- `scripts/build-windows.ps1 -Out <temp dir>` builds every program and both extensions (each with
  `video-ui.js`). Never into `bin\`: the programs there stay those of the last build the user made.

**Implemented as specified**, with these additions and deviations:
- **Step 0** froze more than the minimum list, so no lane needed a contract change:
  - `OFFER_KEY_RE`
  - `MAX_FRAME_VIDEOS`/`MAX_FRAME_VIDEOS_BYTES` (the 64 KiB is the JSON length) and `isFrameVideoMeta`
  - `FrameMessage`/`isFrameMessage`
  - `siteHidden`/`withSiteHidden`/`hostListError` and `HiddenSitesForm.dropped`
  - `TabMediaResponse.videos` (the popup's site rows)

  `TSConvertible` reads the AAC object type as a decimal number of at most three digits (`mp4a.40.02`
  is AAC LC) and skips the subtitle and caption formats a variant may also name (`wvtt`, `stpp`,
  `c608`, ...; review round).
- **Lane A (remuxer).**
  - `mpegts` also has:
    - `NewBytesDemuxer`, and the options `WithProgramCheck` and `WithTimestampsOnly`
    - `Unit.AfterLoss` (the next unit after a continuity gap) and `Stream.Dolby`
    - `Stats`, and an `*Error` carrying file and PID
  - Choices within the spec:
    - A PES cut by a new unit start or by the end of the data is emitted as it is.
    - A header-only ADTS frame gives no sample (a timing gap).
    - A unit without a PTS takes the input's last unwrapped time; `|PTS − DTS| > 10 s` counts as a
      missing PTS.
    - A domain that would start before the previous sample is delayed in decode time by up to 10 s;
      beyond that it is stitched to the track's end.
  - Limits:
    - Parameter sets are capped at 65,535 bytes (the 16-bit avcC/hvcC length), the whole
      parameter-set state at 256 KiB.
    - A video or muxed input whose video yields no sample (no IDR or IRAP picture) is
      `ErrUnsupported` ("no decodable video"), never an audio-only MP4 (review round; until then it
      gave one).
  - Pass 2 reads the other stream of a muxed input in timestamps-only mode, so its unwrapping equals
    pass 1's.
  - An HLS `LANGUAGE` that is not three letters becomes mdhd `und` plus an `elng` box (no ISO 639-1
    mapping).
  - Multi-layer HEVC is untested.
- **Lane B (job, daemon, CLI, desktop).**
  - `mediajob.Run` (unless `LegacyTS`) refuses, before any fetch, an HLS TS track that `ProbeMP4`
    would not list (unconvertible `CODECS`, packed audio), so a key that names one fails early.
  - The daemon also refuses an audio-only MANIFEST probe result. The desktop mock still accepts
    audio-only manifests.
  - `ProbeMP4` lists an audio track only next to a listed video-only track of its container, so the
    separate renditions of a presentation of muxed variants are no longer listed.
  - With only an audio track named, the default video is the first video-only track of its container.
  - Rare mixed-container presentations (TS and fMP4 variants side by side): the confirm-time
    `selectMediaTracks` does not know containers, so confirming a TS video next to stored fMP4 audio
    fails at run time with `media_unsupported`.
- **Lane C (relay).**
  - `probeStream` decodes only its own fields, so a wrong-typed field of another request on it is
    ignored.
  - Its log records the manifest (scheme, host, path), the header count and `deadline_ms`, not the
    page.
  - The daemon ignores `page_url` on MANIFEST probes, as §3.5 says.
- **Lane D (background).**
  - On Chromium, `executeScript` targets `{tabId, documentIds: [documentId]}` alone. Chromium rejects
    `documentIds` together with `frameIds` (§3.6 named both). Firefox uses `{tabId, frameIds}`.
  - A file hand-off's `referrer` is the Referer the browser actually sent (or the default-policy one),
    not the raw frame address. The relay adds `referrer` as a Referer when none is present, which
    would otherwise send more than the browser did.
  - `MediaItem` has no separate `mime`: `contentType` holds the essence.
  - The badge counts a curated page video even when the page plays none (§5.3 literally; only the
    look-ahead requires a reported video). *2026-11-18:* posts and share links count only with a
    reported video that plays (§5.3 "Posts and share links").
  - Segment-like rule: 4 or more distinct MP4s of one directory within 60 s in one frame are hidden.
  - When the same URL is in two frames, URL-based popup lookups take the most recently seen item. Key
    lookups are exact.
  - Stream history records keep the full manifest URL, as in M5; only file records drop the query
    (§6.5 says "without query, as today").
  - `frame-media-seen` is not sent when the requester is a YouTube host (no generic tracker runs
    there).
  - The M5 race stays: a subresource answered before its document's `onCommitted` can be cleared by
    that commit.
- **Lane E (page UI).**
  - The popup groups frames as "This page" and "Embedded players", not "Embedded: <host>": the frozen
    `StreamEntry`/`FileEntry` carry only `frame: 'top'|'embed'`.
  - Element reports start with the first video the user may be watching, DRM and live ones included
    (the popup's "Protected" and the badge need them). `overlay-state`/`video-ui` wait for a fully
    meaningful video.
  - Once the site source lists tracks, the matched file folds into "More sources", so the default row
    is the site's default.
  - The panel's single Audio choice applies to the default row's source.
  - The button's rectangle is read once per panel opening, in the click handler.
  - The tracker bundle (`content-scripts/video.js`, every frame) is about 21 KiB: the tracker, the
    options parser, the dwell and the curated-site table. `rules.ts`'s default-extension tables are
    no longer in it (its table-building calls are marked pure; review round).
- **Integration.**
  - e2e §8.3:
    - #1 is `TestMediaHLSTransportStream` (`media_test.go`, whose filler `tsSegment` gave way to
      `internal/testmedia` streams).
    - #2–#10 are in `videodetect_test.go`: `TestMediaTSAudioRendition`, `TestMediaTSDiscontinuities`,
      `TestMediaTSEarlyRefusals`, `TestMediaLegacyTS`, `TestMediaTSPauseKillResume`,
      `TestStreamRelayProbeAndAdd`, `TestDirectMP4HandOff`, `TestMediaMP4Only` and
      `TestSiteTSFallback`.
    - `nmhost_test.go` asserts `awaiting` on `add` and adds `TestNativeMessagingStreamAutostart`.
  - Deviations in those tests:
    - #4: the media playlist of a master whose `CODECS` name AC-3 is read. AC-3 in fragmented MP4
      would be saved, and only the playlist tells TS from fMP4. No segment is fetched.
    - #5: the legacy record is made by today's Downex (paused after 2 segments), then rewritten
      through `internal/store` to container `"ts"`, a `.ts` name and a `.ts.dxparts` folder; the
      checkpoint format is unchanged since M5.
    - #8: the CDN serves about 2 MiB/s per response, so the engine's ramp-up opens several
      connections on a local server.
    - The probe's autostart is tested without `hello`, which warms downexd itself (BROWSER-SPEC §2).
  - Fix-ups in other lanes' files:
    - `ui/frontend/src/lib/components/AddDialog.test.ts` (the ".mp4" hint)
    - `cmd/downex/app.go` (the `media` overview said ".mp4 (or .ts)")
    - `cmd/downex/watch.go` (the run-time `media_unsupported` text now says the tracks cannot be
      saved as one MP4)
- **Open.**
  - The tracker reports at most once a second and a click does not flush a pending report, so a
    file that only the element names (`e<id>`) can be missing from the panel's first answer. The
    panel's 2 s polling adds it.
  - Not verified, §8.4:
    - real browsers: Chrome/Edge/Firefox, `executeScript` into `about:blank`/`srcdoc` frames,
      Firefox `urlClassification` and `onCommitted` parent frames, the `<style>` fallback under
      Firefox X-rays, the Popover API in a closed shadow root, `openOrClosedShadowRoot`, fullscreen
      web-component players, a transformed `<html>`
    - the curated sites and their address shapes, and the look-ahead timing and throttles
    - signed CDN expiry
    - playback of converted MP4s in real players, multi-GB conversions
    - macOS and Linux at run time

**Review round (2026-10-23).** A review of the implemented tree found 28 issues: 1 high, 10
medium, 17 low. Each was checked again against the tree. Each fix has a test that failed before it.
- **Remuxer (`internal/remux`):**
  - Separate TS video and audio renditions whose starts straddle the 2³³ wrap: `placeDomains`
    moves each input by the multiple of 2³³ that brings its earliest presentation nearest to the
    first input's (`reconcileInputs`). Also covered in e2e §8.3 #2 (`TestMediaTSAudioRendition`,
    "wrapped").
  - A video or muxed input without an IDR or IRAP picture: refused (`media_unsupported`), never an
    audio-only "video" (also `mediajob` `TestRunTSWithoutDecodableVideoFails`).
  - An access unit split over several PES packets (only the first with a PTS): a unit without a PTS
    that continues the unit before it is joined to it. "Continues" means: no leading start code, the
    unit before has no slice yet, or its first NAL unit cannot begin an access unit. The slice check
    of a joined unit looks only at each new piece; scanning the whole unit for every piece was
    quadratic, and 8 MiB took minutes (`TestTSContinuationUnitsAreLinear`).
  - Unlabelled timestamp jumps inside a domain: `inputJumps` finds them per input from the
    timestamps alone and moves every stream of the input by one offset:
    - a video decode time more than 1 ms back
    - an audio PES more than 1 s back
    - either one more than 10 s ahead

    So video and audio stay together. Before, video was stitched while audio dropped frames.
    Pass 2 follows the same timestamps, so both passes agree. An audio PES that comes before the
    video shows a jump of less than 1 s is still dropped as an overlap.
  - A muxed input whose first program has no audio takes the audio of a later program: a role's
    type is fixed where the role first appears.
  - Parameter-set churn costs work in proportion to the change: SHA-256 digests per set and an XOR
    fingerprint of the state identify sample entries. The TS fuzz targets assert allocation bounds.
- **Job (`internal/mediajob`, `internal/media`):**
  - `TSConvertible` skips subtitle and caption codecs and reads `mp4a.40.02` as AAC LC.
  - HLS media playlists of `.vtt`/`.webvtt`/`.srt`/`.ttml`/`.dfxp` segments are not TS candidates.
- **Daemon:** a SITE probe's or add's Referer loses its fragment (`siteURL`).
- **Extension:**
  - **Embed probes.** Their Referer is the default-policy one (§3.6), no longer the tab's full
    address.
  - **What a panel shows.** A panel lists only its own frame's items, and a sub-frame gets neither
    the tab's title nor its host (§2.6, §6.2).
  - **Stream rows.**
    - A bare media playlist (downexd's single `muxed` track of height 0, or of bandwidth 0 once
      measured, 0.7.1) is listed only while no stream of the frame lists qualities, and is never
      the default row.
    - While every answer so far is a bare playlist, the panel probes up to 12 of the frame's
      streams, so that the master is found.
    - Stream rows read "1080p", not "1920x1080". *Amended 2026-10-23:* they read the daemon's
      standard name, "1080p (Full HD)", and a 3840x1634 stream "4K (2160p)".
  - **Default row.** A short clip the element plays (a pre-roll ad) is never the default row while
    there is another.
  - **Look-ahead.** A curated look-ahead that comes before the tab's first playable element report
    is held for 30 s (`HeldLookAheads`) and runs when that report arrives.
  - **Buttons.**
    - Pointer movement places the buttons again before it reveals one, and they are placed every
      frame while the panel is open.
    - A button not shown lets clicks through to the page.
    - The host goes back after the page removed it.
    - A video that started before its button existed shows the button for the rest of the 5 s.
    - In a frame too short for the panel below its button, the panel goes over the button.
  - **Live.**
    - An element whose finite duration grew three times by 1 s or more is live.
    - An MSE element whose frame's streams Downex all refused as live shows the live text.
  - **Tracked videos.** At the cap of 32, a video with a media event, or an untracked one met while
    going round the page on pointer activity, takes the place of a tracked video known to be
    off-screen.
  - **Segments.**
    - The segment rule applies only to files of at most 16 MiB within 60 s of the run. It no longer
      sticks to a directory.
    - A file an element plays is never taken for a segment.
  - **Element files.** An `.mp4` address whose response was seen and refused (a WebM or MOV) is not
    offered as an element file.
  - **Deep scans.** `downex:frame-media-seen` waits twice as long after each time, up to 5 min, and
    audio traffic no longer triggers it.
  - **Back/forward cache.** After a restore every frame reports again.
  - **Tracker bundle.** `rules.ts`'s tables are no longer in it.
- **Open from the review** (low; not fixed):
  - **AAC change in a later domain.** An AAC rate, channel or profile change in a later
    discontinuity domain (an inserted ad) still fails at assembly, after every fragment was
    fetched. The early check reads only the first domain's start.
  - **Domains without a sync picture.** A domain whose segments carry no IDR/IRAP picture (open-GOP
    content after an ad break) loses its video, so its picture freezes. Recovery-point SEI is not
    taken as a sync start.
  - **Sub-frame buttons.** In a sub-frame partly scrolled out of the top-level viewport, the button
    is placed in the frame's own viewport and can be off-screen.
  - **Popup folds.** The popup still folds every embedded frame's rows under any curated embed and
    shows the folds after the whole list. This needs frame identity in `StreamEntry`/`FileEntry`.
  - **Outstream ads.** Outstream ad players (muted, no controls, under 30 s, not looping) still get a
    button. Hiding them would also hide muted autoplay clips with custom controls.
  - **Live detection (PLAUSIBLE).** The live rule for hls.js players and the bfcache fix rest on
    browser behaviour not checked in a real browser (§8.4).

**0.7.1 (2026-10-23): three bugs reported on 0.7.0, fixed in three lanes, an integration step and a
review round.** Verified with generated fixtures only (a real-shaped 120-byte 1×1 PNG, GIF and JPEG
prefixes, bare media playlists with and without byte ranges); not checked against the reported sites.
- **A. TS segments disguised as images.** A CDN served every segment as `image/png`: a complete 1×1 PNG,
  then the transport stream, which 0.7.0 refused after the first fragment ("neither MPEG-TS nor
  fragmented MP4"). `mpegts.FindStart` (§4.2) finds each file's first packet; the demuxer, the sniff,
  the early check and `ConcatTS` read from there. Fragment files keep the bytes as received (byte
  ranges and resume unchanged); `BytesDone` counts them as received. fMP4 behind a prefix stays
  refused.
- **B. Bare media playlists without size or quality.** `ProbeMP4` measures an HLS video or muxed track
  without `BANDWIDTH` (§3.3): an estimated size, and from the first segment its picture, frame rate,
  `CODECS` and a name ("1080p", since 2026-10-23 "1080p (Full HD)"). The extension's rows, the
  popup, the desktop confirm window ("Size: about 2.1 GB") and `downex media --list` (SIZE "~2.1 GB") show them.
- **C. "File size Unknown" while a stream downloads.** `Progress.BytesTotal` estimates the file
  (§4.8); `Download.size_approx` (proto field 32) marks the estimate; the list, the details panel, the
  progress window ("~2.1 GB", time left) and the CLI show it as one. A completed download shows its
  file's size.
- **Review round fixes** (each test seen failing first):
  - A stream's total and a measured track's size are always approximate: they are the fragments'
    bytes, while the file is an MP4 written anew (about 10 % smaller in tests).
  - The CLI (`watch`, `--wait` lines, `ls`, the pause message) shows an estimate as "~", with an ETA
    marked "~", and keeps the fragments' percentage; before, it showed 100 % and "ETA 0s" midway.
  - `FindStart` reads a file from 0 when it starts as a transport stream (sync bytes at 0 and 188,
    not a GIF): a damaged packet among the first five no longer loses the PAT/PMT (0.7.0's
    behaviour).
  - The probe measures at most 4 tracks (default video first), one request at a time on one
    connection; it reads each head at once and keeps no bytes; a HEAD follows only a successful
    answer without a length; a 429/503 ends the measuring; a start `CheckTS` refuses ends the
    stream's requests (§8.3 #4 again fetches nothing after the first segment of a refused stream).
  - A look-ahead's probe fetches no media (`ProbeMP4Playlists`).
  - A paused or failed run emits its progress, without the bytes of abandoned requests, before its
    last checkpoint, so the stored bytes (and the paused size estimate) are the durable ones.
  - e2e `streamServer` and `TestSiteManifestFallback` tell the probe's bounded requests from the job's
    fetches.
- **Open (low):** measurements are not cached between the panel's probes, `ProbeStream` and
  `AddMedia` (a bare playlist's first 512 KiB can be read up to three times; the panel probes up to 12
  level playlists while it looks for a master); the probe's requests do not share the engine's
  per-host gate or limiter (one at a time, ≤ 512 KiB, below every allowed request-length cap); the
  estimate counts received bytes, image prefixes included.
- **Gates (0.7.1, after the review round):** `go build ./...`, `go vet ./...`, `gofmt -l` clean;
  `CGO_ENABLED=1 go test -race ./...` passes (36 packages, e2e included); `darwin/arm64` and
  `linux/amd64` with `CGO_ENABLED=0`: `go build ./...` and `go test -c` of the touched packages pass;
  30 s each of `FuzzDemux`, `FuzzProbe`, `FuzzCheckTS`, `FuzzTSTwoPass`; extension `pnpm check`,
  `pnpm test` (864 tests); desktop `pnpm check`, `pnpm test` (545 tests); `go vet -tags
  desktop,production ./ui`; `scripts/build-windows.ps1 -Version 0.7.1 -Out <temp dir>` (never `bin\`).

**0.7.2 (2026-10-23): bare fMP4 playlists described, then a review round.** Reported on 0.7.1: a bare
HLS media playlist of fragmented MP4 (movy.sx: `#EXT-X-VERSION:6`, an EXT-X-MAP on another host,
segments that begin with a `moof`) got a measured size but no picture, frame rate, codecs or name.
`downex media --list` showed QUALITY "-" and CODECS "-". Verified with mp4ff-built fixtures and
mp4ff's own test inits only. Not checked against the reported site.
- **Init described.** `probeinit.go` reads the EXT-X-MAP section and the first segment's `moof`
  boxes (§4.8). The reported shape lists as 1920x1080, 25 fps, `avc1.640028,mp4a.40.2`, "1080p".
- **CLI.** A track with `BANDWIDTH` and no size shows SIZE "~" + bandwidth × duration ÷ 8, formatted
  as other sizes. This applies to `media --list`, `media --site --list` and `info`.
- **Review round fixes** (each test seen failing first):
  - **Nesting (medium).** An init with boxes nested 60,000 deep where `CheckInit` does not look (481
    KB) took 18 s of CPU in mp4ff's whole-`moov` decode. That is past the probe's 8 s budget, so
    the track lost its size. Now one linear pass reads the boxes, and mp4ff decodes leaf boxes only
    (`TestDescribeInitNestedBoxes`, `TestProbeMP4DescribeNestedInit`).
  - **Malformed inits (low).** Inits that `remux.parseInit` refuses were described with codecs:
    timescale 0, track ID 0 or twice, a missing `mdhd`, `mvhd` or sample entry. They are no longer
    described.
  - **Frame rate (low).** The frame rate is taken from the first `moof` with a `traf` of the video
    track. Before, a segment written one `moof` per track, audio first, gave none.
  - **Large encrypted inits (low).** An encrypted init larger than 512 KiB was not flagged.
    `CheckInit` refuses the cut `moov` before it looks inside. Now `closeCut` closes it first.
- **Open (low):**
  - **Run path.** `remux.parseInit` still decodes the whole `moov` with mp4ff, so the same nested
    init costs about 18 s of CPU once the user starts the download.
  - **Parity.** The probe applies `parseInit`'s explicit rules to the boxes it reads. A malformed
    box it does not read (an `elst`, a sample-entry child other than the codec configuration) is
    still described, while Run's full decode refuses it. One owner of these rules, shared with
    remux, would close this.
  - **e2e classifier.** The `probeRequest` classifier (`internal/e2e/media_test.go`) does not know
    the new `bytes=0-65535` read.
- **Gates (0.7.2, after the review round):**
  - `go build ./...`, `go vet ./...` and `gofmt -l internal cmd` are clean.
  - `CGO_ENABLED=1 go test -race ./...` passes: 36 packages, e2e included. The 11 that do not
    depend on the change came from the test cache.
  - `darwin/arm64` and `linux/amd64` with `CGO_ENABLED=0`: `go build ./...` passes, as does
    `go test -c` of `internal/mediajob` and `cmd/downex`.
  - `FuzzDescribeInit` ran for 45 s: 3.08M executions, no failures.
  - `scripts/build-windows.ps1 -Version 0.7.2 -SkipExtension -Out <temp dir>` (never `bin\`).
  - Extension and desktop frontend checks were not rerun; no file of theirs changed.

**Quality names (amendment 2026-10-23 at the top; SITE-MEDIA-SPEC.md holds the rule and its
table).** Go side implemented: `internal/media/quality.go`; HLS variants and DASH video
Representations are named by it; `sitemedia.ManifestTracks` keeps one video per quality class;
`describeTracks` names measured tracks with it; `downex media --list` shows QUALITY and PICTURE.
Gates for this change: `go vet ./...` and `gofmt` clean; `go test -short ./...` passes;
`CGO_ENABLED=1 go test -race` of media, sitemedia, mediajob, downex and downexd passes; e2e passes
(all of it, and the site, video, stream and media tests under `-race`); `darwin/arm64` and
`linux/amd64` builds and `go test -c` of the touched packages pass. The extension and desktop
lanes (showing the daemon's names, one helper each for pictures they name themselves) are separate
and were not part of this step. Nothing ran against a real site or player.

**Quality names, the clients and a review round (2026-10-23).** The extension and the desktop show
the daemon's names: `lib/quality.ts`, the same file in both packages, names what no daemon named
(older daemons, File rows) and reduces an older daemon's list by the daemon's order. A review then
found 7 issues (2 medium, 4 low, 1 cosmetic), fixed test-first:
- (medium) Streams of a panel were merged by class alone, which hid a second video whose classes the
  first already listed: they are merged by the same encoding again (§2.6, `qualityKey`).
- (medium) An HLS/DASH variant the class rule left out could not be chosen (`--video v1`, a stored
  pick): `sitemedia.ManifestTracksFor` lists a named variant in place of its class's for every
  MANIFEST probe and the site fallback; variants without a RESOLUTION have no class and are all
  listed.
- (low) Site formats without a picture collapsed to one row, possibly the worst: none is left out
  now, and yt-dlp's order breaks the last tie.
- (low) `lib/quality.ts` differed from Go in three places (a frame rate between 30 and 30.5 was
  stated, a width alone named nothing, the in-class order compared raw frame rates and stopped at
  the bit rate): it follows Go's tables case for case, checked by
  `internal/media/quality_sync_test.go`.
- (low) An older daemon's raw heights that look standard ("480p" of 1138x480) were shown as they
  are: they are named from the picture (`media.TrackName`, `trackQuality`), and YouTube's label
  gives way in the same case (`media.LabeledClass`).
- (low) The CLI named old records otherwise than the desktop: it shows `media.TrackName` too.
- (cosmetic) A chosen left-out format was listed after its class's row (two rows of one name in
  `downex media --list`): it is listed in place of it.

Nothing ran against a real site, browser or player.
