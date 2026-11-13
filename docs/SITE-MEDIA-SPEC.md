# Site media specification (video pages through yt-dlp)

Downex saves the video of a web page that yt-dlp understands, YouTube first, as one file. yt-dlp is
used **only as an extractor**: it turns a page URL into a list of formats, each with a direct URL and
the HTTP headers it needs. Downex's own code downloads the chosen formats (segmented, several
connections, resumable, shown in the progress window) and joins them with `internal/remux`. This spec
extends `MEDIA-SPEC.md` (M5) and `M6-SPEC.md`; the decisions below are dated 2026-10-21 and are final
unless §9 lists them as open.

> **Amendment 2026-10-23 (user decision; supersedes the 2026-10-22 rule "every MP4 quality is
> listed" and overrides every other section where they disagree):** the YouTube list of a
> 3840x1634 film had 15 rows ("1634p MP4·AV1 860 MiB", "1090p AV1", "818p H.264", "818p AV1",
> "544p H.264", …, "272p MP4·H.264+AAC", "128p AV1"): too many, and wrongly named, since a 4K film
> must say 4K.
> 1. **Standard quality names everywhere**: YouTube and every yt-dlp site, HLS/DASH streams, the
>    overlay panel, the popup, the desktop confirm window, picker and details, and
>    `downex media --list`. A video is named by its **quality class**, never by its raw height.
>    - The class is the step of the ladder 4320, 2160, 1440, 1080, 720, 480, 360, 240, 144 nearest
>      to the picture's longer side scaled to 16:9: max(height, round(width × 9 / 16)) for a
>      landscape or square picture, max(width, round(height × 9 / 16)) for a portrait one (halves
>      round up; a tie between two steps goes to the higher). With one side known: the height, or
>      round(width × 9 / 16). With neither: no class and no name.
>    - YouTube only: yt-dlp's `format_note` names the class when its first word is a ladder label
>      (`2160p`, `1440p60`, `1080p60 HDR`, `1080p Premium`), unless the label only restates the
>      picture's raw height while the picture is of another class (a "480p" of 1138x480 is
>      720p; `media.LabeledClass`). Other sites' notes are not read.
>    - The name of a class: 4320 "8K (4320p)", 2160 "4K (2160p)", 1440 "1440p (2K)", 1080
>      "1080p (Full HD)", 720 "720p (HD)", the others "480p", "360p", "240p", "144p". Then
>      " <fps>fps" when the frame rate rounds to more than 30 (59.94 → " 60fps"; 30.4 states
>      none), then " HDR". Example: "4K (2160p) 60fps HDR".
>    - The picture's width x height stays available as secondary detail (a tooltip, the details,
>      the CLI's PICTURE column), never as the name.
>    - **Older names.** What every UI and the CLI show for a track (`media.TrackName`, the clients'
>      `trackQuality`): its name when that is a standard name, unless it is an older daemon's raw
>      height that looks like one (a class that is the picture's height while the picture is of
>      another class: "480p" for 1138x480 shows "720p (HD)"); else, with a known picture, the
>      standard name of the picture ("1634p", "1080p60", "2160p60 HDR", "1920x1080" and no name
>      are named again; HDR kept from the old name); else the name as it is. So `downex info` on a
>      0.7.2 record shows what the desktop and the extension show.
> 2. **One row per quality class** for site offers (yt-dlp). Among the usable MP4 videos of a class
>    exactly one is offered (§3.11), chosen in this order:
>    - video-only before muxed (a muxed format only for a class that has no video-only one)
>    - SDR before HDR (HDR only when the class has nothing else)
>    - the higher frame rate (rounded: 59.94 and 60 are equal)
>    - H.264 before HEVC before AV1 (H.264 plays everywhere)
>    - the higher bit rate, then the larger picture, the larger size, then the format yt-dlp lists
>      later (it lists formats worst first)
>
>    Other rules:
>    - Formats without a picture (and, for YouTube, without a ladder label) have no class: none of
>      them is left out for another, and each is offered, after the classes, in the same order.
>    - The codec still shows small on the row ("H.264", "AV1"). The audio select is unchanged
>      (languages, DRC). The default is the row of the best class.
>    - Manifest streams (HLS/DASH variants: the site fallback and every MANIFEST probe,
>      `sitemedia.ManifestTracks`) follow the same per-class rule (within a class: video-only,
>      frame rate, codec, bandwidth, picture, size, then the manifest's order). HDR is not known
>      of manifest tracks, so they are named without it. Variants without a RESOLUTION have no
>      class and are all listed, the higher bandwidth first.
>    - The formats the rule leaves out stay selectable by key or ID. A stored pick whose class now
>      offers another format still runs, and `downex media --site --video <format id>` works. The
>      probe then lists the chosen format **in place of** its class's row
>      (`sitemedia.Offer.Listed`), so every list keeps one row per class.
>    - The same holds for streams: a probe or add that names a variant the rule leaves out (by
>      key, else ID: `downex media <manifest> --video v1`, a stored pick) lists it in place of its
>      class's variant (`sitemedia.ManifestTracksFor`, used by `cmd/downexd` for every MANIFEST
>      probe and the site fallback), so it can be chosen; a named variant that is never offered
>      (another codec) is still refused.
>    - At most 200 tracks are offered: the longer of the video and audio lists loses its worst (a
>      video that goes stays selectable).
> 3. **One place per language.** In Go the rule is `internal/media/quality.go` (`QualityClass`,
>    `ClassName`, `QualityName`, `LabelClass`, `LabeledClass`, `ParseName`, `TrackName`). It is
>    used by:
>    - `internal/media`: the names of HLS variants and DASH video Representations; a DASH Label
>      gives way when the picture is known.
>    - `internal/sitemedia`: offers and their names.
>    - `internal/mediajob`: tracks measured by `describeTracks`.
>    - `cmd/downex`: `TrackName` for every video track it shows.
>
>    The extension (`ext/src/lib`) and the desktop (`ui/frontend/src/lib`) show the daemon's track
>    names and do not derive names from heights. Where they must name a picture themselves (an
>    older daemon, a File row from the element's picture), each uses one helper, `lib/quality.ts`
>    (the same file in both packages), tested with the two tables below case for case. They are
>    `internal/media/quality_test.go`'s `qualityNameCases` and `trackNameCases`, and
>    `internal/media/quality_sync_test.go` fails when the two copies of the helper or of its test
>    differ, or when their tables are not Go's. The clients reduce an older daemon's list by the
>    same order as the daemon (rounded frame rate, then bit rate, picture and size; the given
>    order last), keeping the daemon's pick as its class's row.
> 4. **CLI.** `downex media --list` shows the reduced list, with QUALITY (the name) and PICTURE
>    (width x height and frame rate) columns. `--all`, every format, is not offered: it needs a
>    ProbeMedia field (§9 #14).
>
> | Picture | fps | HDR | Class | Name |
> |---|---|---|---|---|
> | 3840x1634 | 24 | | 2160 | 4K (2160p) |
> | 2560x1090 | 24 | | 1440 | 1440p (2K) |
> | 1920x818 | 24 | | 1080 | 1080p (Full HD) |
> | 1280x544 | 24 | | 720 | 720p (HD) |
> | 854x364 | 24 | | 480 | 480p |
> | 640x272 | 24 | | 360 | 360p |
> | 426x182 | 24 | | 240 | 240p |
> | 256x128 | 24 | | 144 | 144p |
> | 7680x4320 | 30 | | 4320 | 8K (4320p) |
> | 3840x2160 | 30 | | 2160 | 4K (2160p) |
> | 2560x1440 | 30 | | 1440 | 1440p (2K) |
> | 1920x1080 | 30 | | 1080 | 1080p (Full HD) |
> | 1280x720 | 30 | | 720 | 720p (HD) |
> | 854x480 | 30 | | 480 | 480p |
> | 640x360 | 30 | | 360 | 360p |
> | 426x240 | 30 | | 240 | 240p |
> | 256x144 | 30 | | 144 | 144p |
> | 1920x1080 | 60 | | 1080 | 1080p (Full HD) 60fps |
> | 1920x1080 | 59.94 | | 1080 | 1080p (Full HD) 60fps |
> | 1920x1080 | 29.97 | | 1080 | 1080p (Full HD) |
> | 1920x1080 | 30.4 | | 1080 | 1080p (Full HD) |
> | 1920x1080 | 30.5 | | 1080 | 1080p (Full HD) 31fps |
> | 1280x720 | 50 | | 720 | 720p (HD) 50fps |
> | 3840x2160 | 60 | yes | 2160 | 4K (2160p) 60fps HDR |
> | 3840x1600 | 24 | yes | 2160 | 4K (2160p) HDR |
> | 2560x1440 | 0 | yes | 1440 | 1440p (2K) HDR |
> | 7680x3264 | 24 | | 4320 | 8K (4320p) |
> | 1920x800 | 24 | | 1080 | 1080p (Full HD) |
> | 1080x1920 (portrait) | 30 | | 1080 | 1080p (Full HD) |
> | 720x1280 (portrait) | 60 | | 720 | 720p (HD) 60fps |
> | 2160x3840 (portrait) | 30 | | 2160 | 4K (2160p) |
> | 1080x1350 (portrait) | 30 | | 1080 | 1080p (Full HD) |
> | 1440x1080 (4:3) | 25 | | 1080 | 1080p (Full HD) |
> | 960x720 (4:3) | 30 | | 720 | 720p (HD) |
> | 640x480 (4:3) | 25 | | 480 | 480p |
> | 1080x1080 | 30 | | 1080 | 1080p (Full HD) |
> | 720x576 | 25 | | 480 | 480p |
> | 1024x576 | 30 | | 480 | 480p |
> | 1066x600 | 30 | | 720 | 720p (HD) |
> | 1600x900 | 30 | | 1080 | 1080p (Full HD) |
> | 2048x1080 (DCI 2K) | 24 | | 1080 | 1080p (Full HD) |
> | 4096x2160 (DCI 4K) | 24 | | 2160 | 4K (2160p) |
> | 15360x8640 | 30 | | 4320 | 8K (4320p) |
> | 16384x16384 | 0 | | 4320 | 8K (4320p) |
> | 320x180 | 30 | | 144 | 144p |
> | 176x144 | 15 | | 144 | 144p |
> | 160x90 | 30 | | 144 | 144p |
> | 100x50 | 0 | | 144 | 144p |
> | 0x720 (height only) | 30 | | 720 | 720p (HD) |
> | 1920x0 (width only) | 30 | | 1080 | 1080p (Full HD) |
> | 0x0 | 60 | yes | 0 | "" |
> | -1x-1 | 0 | | 0 | "" |
>
> Odd sizes, decided: a 4:3 1440x1080 picture is 1080p (its height is its longer side at 16:9);
> 720x576 (PAL) and 1024x576 are 480p; 1066x600 is 720p and 1600x900 1080p (the tie goes up); DCI
> 2048x1080 is 1080p; a picture below 144 is 144p and one above 4320 is 8K; a width alone is
> scaled to 16:9 (1920x0 is 1080p).
>
> What a track shows (`media.TrackName`, the clients' `trackQuality`; `trackNameCases`):
>
> | Name given | Picture | fps | Shown |
> |---|---|---|---|
> | 4K (2160p) 60fps HDR | 3840x1634 | 60 | 4K (2160p) 60fps HDR |
> | 1080p (Full HD) | 1440x1080 | 30 | 1080p (Full HD) |
> | 480p | 854x364 | 30 | 480p |
> | 720p (HD) (YouTube's label) | 1920x1080 | 30 | 720p (HD) |
> | 1634p | 3840x1634 | 24 | 4K (2160p) |
> | 1090p | 2560x1090 | 24 | 1440p (2K) |
> | 2160p60 HDR | 3840x2160 | 60 | 4K (2160p) 60fps HDR |
> | 1080p60 | 1920x1080 | 60 | 1080p (Full HD) 60fps |
> | 1920x1080 | 1920x1080 | 30 | 1080p (Full HD) |
> | 1920x1080 | 1920x1080 | 30.2 | 1080p (Full HD) |
> | HD | 1280x720 | 30 | 720p (HD) |
> | (none) | 1080x1920 | 30 | 1080p (Full HD) |
> | (none) | 1920x0 | 30 | 1080p (Full HD) |
> | 480p (an older raw height) | 1138x480 | 24 | 720p (HD) |
> | 360p (an older raw height) | 852x360 | 24 | 480p |
> | 240p (an older raw height) | 568x240 | 24 | 360p |
> | 480p HDR (an older raw height) | 1138x480 | 24 | 720p (HD) HDR |
> | 480p | 0x480 | 24 | 480p |
> | 480p | 854x480 | 24 | 480p |
> | (none) | unknown | 30 | "" |
> | Main | unknown | 30 | Main |
> | 1634p | unknown | 24 | 1634p |

> **Amendment 2026-10-22 (user decision, overrides every other section):**
> - **MP4 only.** Every site download produces exactly one `.mp4` file. There is no audio-only
>   choice and no `.m4a`, `.webm`, `.mkv`, `.ts` or copied-other-container output. WebM (VP9/Opus)
>   and every non-MP4 format are simply not listed: no "Not offered"/`skipped` field, label, line or
>   CLI text exists anywhere. Proto, relay, extension, CLI and UI carry no `audio_only`/`audioOnly`/
>   `--audio-only` and no `skipped`.
> - **Every MP4 quality is listed and the user chooses.** *Superseded by the amendment of
>   2026-10-23 above: one row per quality class, named by its class.* The picker (overlay, popup,
>   desktop confirm window, `downex media --site --list`) lists every distinct offered MP4 video
>   quality — each resolution, frame rate, HDR and codec (H.264, AV1, HEVC) combination — best
>   first, each paired with the default MP4 audio (AAC, or AC-3/E-AC-3 in MP4); the default
>   selection is the best one (§3.11). An audio select appears only when several MP4 audio tracks
>   exist (languages, DRC).
> - **Manifest fallback** (§3.11) is used only when its probed container is `mp4` (fragmented MP4);
>   an MPEG-TS-only or audio-only page is `media_unsupported` ("No MP4 video on this page").
>   *Amended 2026-10-23 (VIDEO-DETECT-SPEC.md §3.3):* MPEG-TS tracks whose codecs the pure-Go
>   TS→MP4 remuxer converts are offered too, and are saved as one `.mp4`; an audio-only page stays
>   `media_unsupported`.
> - Where any later text still mentions audio only, `.m4a`, WebM, skipped or "Not offered", this
>   amendment wins; the contract-freeze step (§7 Step 0) removes those mentions first.

## 1. Scope and non-goals
- **In scope.** A "Download this video" button over the YouTube player and video entries under "Media
  on this page" in the extension popup, both with a quality picker that lists one MP4 video per
  quality class the page offers (amendment 2026-10-23). A page URL in the desktop Add dialog and in `downex media --site`. The daemon resolves
  the page with yt-dlp, the user confirms in the existing confirmation window (its quality picker
  reused), and the media job downloads the formats and merges them into one `.mp4`.
- **Who knows about sites.** All site knowledge lives in yt-dlp and in the JavaScript challenge solver
  it runs in Deno. Downex never contains an extractor, never interprets player JavaScript, never solves
  signature, `n` or proof-of-origin challenges, and never ports yt-dlp code, strings or data files.
  The one exception is the extension: it recognises YouTube video page URLs (§5.1) to place its
  button and to canonicalise the address it sends. It never reads the page's scripts, player data or
  traffic for that. Since 2026-10-23 it also recognises the public address shapes of a curated list
  of video sites (VIDEO-DETECT-SPEC.md §3.8: Vimeo, X, Facebook, Instagram, TikTok, Dailymotion,
  Reddit, Twitch, Streamable), only to name their videos, canonicalise the address it sends and
  decide where a look-ahead may run. Those shapes are written from the addresses the sites show,
  never from yt-dlp's extractors.
- **Policy amendment (Step 0, §7).** `CLEAN-ROOM.md` ("no site-specific signature deciphering"),
  `REQUIREMENTS.md` M3 and the `MEDIA-SPEC.md` preamble are reworded as follows. Downex's own code
  never deciphers signatures or solves site challenges. It may run yt-dlp, a separately distributed
  third-party program downloaded from its publisher, as an external process to resolve a page. The
  same rule already covers the FFmpeg sidecar.
- **Clean room.** The overlay button is Downex's own design: its mark (`internal/brand`) and the
  extension's colour tokens (`ext/src/assets/ui.css`). Nothing of another download manager's look,
  wording or icons is used.
- **Refused with a clear message:**
  - DRM-protected content (`_has_drm`, a format's `has_drm`, or protection boxes in a downloaded
    init) → `encrypted`. Downex never passes `--allow-unplayable-formats` and never fetches keys.
  - Live, upcoming and still-processing streams (`is_live`, or `live_status` `is_live`/`is_upcoming`/
    `post_live`) → `live`. Finished live streams (`was_live`) are ordinary videos.
- **Not in v1:**
  - playlists and channels (`media_unsupported`)
  - subtitles, thumbnails, chapters and metadata embedding
  - any transcoding
  - any output but one `.mp4`: formats that cannot be saved as MP4 are never listed (§3.11)
  - PO-token providers, yt-dlp plugins, `--cookies-from-browser`
  - bulk extraction (playlists, automatic probing of every page; the look-ahead of §5.6 resolves only
    the one video the user is watching)
  - FFmpeg

  The capture extension list for ordinary file downloads (for example `.iso` and `.dat`) belongs to
  BROWSER-SPEC §3, not to this spec. `ext/src/lib/rules.ts` already lists both (DEFAULTS_VERSION 2).
- **Platforms.** Windows first. macOS and Linux compile, get the paths of §3.9, and run the same
  logic, but are not verified at run time.
- **Licensing.**
  - yt-dlp's source is Unlicense, but its standalone executables are a GPLv3+ combined work
    (upstream `THIRD_PARTY_LICENSES.txt`). Deno is MIT.
  - Downex never bundles either tool in its installer or packages. Each user's Downex downloads them
    from the publishers' GitHub releases on first use (§4.2).
  - The Settings page names both licences and links to them (§5.4).
  - Whether a site's terms allow downloading is the user's responsibility. The Settings page says so
    in one line.

## 2. Architecture and data flow

### 2.1 Components
| Where | Role |
|---|---|
| ext `entrypoints/youtube.content.ts` | Overlay button and picker on YouTube players (§5.1) |
| ext background | Page-video detection by URL; probe cache; look-ahead (§5.6); `probeVideo`/`addVideo` relay requests (§3.5) |
| `downex-nmhost` | Maps `probeVideo` to `ProbeMedia{origin: SITE}` and `addVideo` to `AddMedia{origin: SITE}` |
| `internal/daemon` | Origin SITE in AddMedia/ProbeMedia/Confirm/runMedia; tools service; settings `media` (§3.3) |
| `internal/tools` | Finds, downloads, verifies, installs and updates yt-dlp and Deno; runs child processes (§4) |
| `internal/sitemedia` | Pure: yt-dlp JSON → validated formats → offers, keys, default pick, labels (§3.10, §3.11) |
| `internal/sitejob` | Runs yt-dlp (`Resolver`: argv, environment, cookie file, cache, error classification) |
| `cmd/downexd/site.go` | `daemon.SiteMedia`: resolver + sitemedia + mediajob, the only place they meet |
| `internal/mediajob` | `RunRanged`: direct formats as byte-range chunks into one file per track; remux or copy (§2.4) |
| `internal/engine`, `internal/transport` | Per-host cap on request length (`RequestCaps`); IP-family pinning (`WithNetwork`) |
| `ui/` | Add dialog page mode, picker additions (sizes, every MP4 quality), Settings → Video sites, texts |
| `cmd/downex` | `media --site`, `tools`, settings flags (§3.7) |

### 2.2 Flow
```
YouTube tab                downex-nmhost          downexd                                    yt-dlp + Deno
 button/popup ─probeVideo──▶ ProbeMedia{SITE} ──▶ Tools.Ensure ─▶ Site.Probe ─── -J page ───▶ formats (JSON)
             ◀─videoInfo─── ◀──────────────────── offers: one MP4 video per class, default pick
 pick ───────addVideo─────▶ AddMedia{SITE} ─────▶ record kind=MEDIA (awaiting | queued)
                                                   desktop confirm window (picker preset) → queued
                                                   runMedia: resolve again (cache ≤ 15 min) → keys
                                                   RunRanged ─ video data ◀── ranges ≤ 10 MiB ── googlevideo
                                                             └ audio data ◀── ranges ≤ 10 MiB ── googlevideo
                                                   remux → <name>.mp4 → completed
```
1. **Detect (extension).** By URL, not by traffic (§5.1). YouTube streams on-demand video through
   POST requests that the M5 detector cannot see.
2. **Probe.**
   - The user opens the picker. The background sends `probeVideo` to the relay, which sends
     `ProbeMedia{origin: SITE, page_url}` to the daemon.
   - The daemon checks `site_media`, then calls `Tools.Ensure` and then `Site.Probe` within the wait
     budget of §3.3.
   - The resolver returns a cached or fresh extraction. `sitemedia` turns it into an offer.
   - The answer is `MediaInfo` with the tracks (one MP4 video per quality class and the MP4 audio
    tracks) and the default pick.
   - The codes `tools_installing` and `site_resolving` mean "try again". The caller retries every
     2 s and joins the work that is still running.
   - While the user watches a video, a look-ahead (§5.6) has usually resolved the page already: the
     click gets the cached answer at once, or joins the look-ahead still running.
3. **Add.**
   - The user picks a row, and the extension sends `addVideo` → `AddMedia{origin: SITE, video_track,
     audio_track}`.
   - The daemon reuses the cached extraction, checks the pick and records a `kind = MEDIA` download:
     `url` is the page, `media.origin` is `site`, plus the format, tracks, selection, container and
     `expected_size`.
   - It then applies the M4.5 rule. With `confirm_browser_downloads` on and something able to ask the
     user, the download awaits confirmation: the desktop confirm window opens with the picker preset
     to the user's pick, and Confirm may still change it. Otherwise the download is queued.
4. **Run** (on a scheduler slot, like M5).
   - `runMedia` sets PROBING, waits for `Tools.Ensure(runCtx)`, then calls `Site.Run`.
   - `Site.Run` resolves again under the rules of §2.6 and finds the chosen tracks by Key.
   - Format `http`: `mediajob.RunRanged` (§2.4). Format `hls`/`dash`: `mediajob.Run` on the fresh
     `manifest_url` with the stored M5 keys (§3.11).
   - The state becomes DOWNLOADING with the first progress report.
5. **Merge** (§2.4). Remux joins two fMP4 tracks (or one muxed fMP4 track) into a progressive MP4.
   Copy saves a single muxed MP4 track as it is. Both write `<name>.dxpart`, then `output.json`,
   then do a no-replace rename, as in MEDIA-SPEC §7. COMPLETED follows, and the part directory is
   removed once the completion is stored.
6. **After completion.** The existing `finish` runs: the file mark with HostUrl = the page URL, the
   antivirus hook, notifications and the completion dialog.

### 2.3 One record, two ranged tracks
- A site video is **one** `kind = MEDIA` download whose job fetches each chosen format as a
  segmented, ranged download of one resource:
  - several connections spread over both tracks
  - chunks of at most the host's request length
  - a resume bitmap per track
- Two linked ordinary downloads plus a merge step were rejected for these reasons:
  - The store, List/Watch, queues, notices, categories and the completion dialog all assume that one
    record is one visible download.
  - `engine.Run` is tied to a record and renames to a final file.
  - Pause, remove and resume would have to cascade to both downloads.
  - The merge would need crash handling of its own.
- The M5 job already has a worker pool, limiter, checkpoints, progress, connection views, output
  record and no-replace rename. It lacked only a way to take pre-resolved resources.
- The engine still gets the per-host request cap of §2.4, so an ordinary download of a googlevideo
  URL behaves the same.

### 2.4 Downloading a direct format
- **Plan.** `media.PlanRanged` cuts each track into consecutive byte ranges of `ChunkBytes` (the last
  one shorter). Chunk 0 is marked Init.
- **Chunk size.**
  - The ceiling (`mediajob.ChunkSize`): the smallest non-zero value of the format's
    `downloader_options.http_chunk_size` and `HostMaxRequestBytes(host)`. If both are zero, 16 MiB.
    The result is clamped to 1–64 MiB.
  - Fitted to the connections (`mediajob.FitChunk`, amendment 2026-10-28): for a track of known
    size, k = min(the download's `max_connections` (default 8, at most 32),
    `HostMaxConnections(host)` when set). If k ≥ 2, the chunk is ceil(size / (2·k)) rounded up to
    64 KiB, at least 2 MiB, at most the ceiling. A short track then keeps every connection busy
    instead of one or two large chunks: 40 MiB over 8 connections is 16 chunks of 2.5 MiB (3 of
    16 MiB before); on googlevideo the 1 MiB request cap is the ceiling. A track of unknown size, or
    k = 1, keeps the ceiling. Requests never exceed the host's request cap.
  - The size is fixed for the life of a track's checkpoint, which records it as `ChunkBytes`: a
    resumed track keeps its checkpoint's size (also one written before the fit rule), and a
    renewal (§2.6) keeps the sizes.
- **Built-in host rule.** `*.googlevideo.com` gets `max_request_bytes` 1 MiB (1,048,576) and no
  connection cap, so a download uses its own `max_connections` (32 by default). It applies only to
  hosts that no user host rule matches. googlevideo slows every request down after its first
  second or so, not the connection count. Measured 2026-10-29 on the owner's ~84 Mbit/s line
  (48 MiB 1080p track): 4 connections of 10 MiB (the 0.8.x rule) 3.0 MiB/s, 8 connections of
  10 MiB 2.7, 32 of 4 MiB 6.2, 32 of 1 MiB 7.6-7.8, 32 of 512 KiB 7.6-8.7 MiB/s.
- **Ordinary downloads.** The engine applies the same cap through `engine.RequestCaps`:
  - every ranged request ends at most `cap` bytes after its start
  - the probe asks for `bytes=0-(cap-1)`
  - `serve()` already reopens the stream at the current position when it runs out
- **Unknown size.** A format without an exact `filesize` gets one `Range: bytes=0-0` request with
  its headers before planning; `Content-Range` gives the total. If the server ignores ranges, the
  track is one whole-resource request (one connection, restarted from 0 when interrupted).
- **Connections.**
  - At most min(pending chunks, the download's `max_connections` (default 32),
    `HostMaxConnections(host)` per host). Connections are opened only when they are first used.
  - Chunk 0 of each track of several chunks is fetched first (the init gate). No other chunk, and
    no chunk of a single-chunk track, starts before the init checks of all those tracks pass.
  - A check may pass early, on the received prefix of chunk 0 (below), so the other chunks start
    while chunk 0 is still arriving instead of after it (amendment 2026-10-28). A track resumed
    with chunk 0 durable is checked from its file and has no gate.
  - Then chunks are taken in order: video (or muxed) first, then audio.
  - A 429 or 503 halves that host's workers for the rest of the run (at least 1). `Retry-After` is
    honoured.
  - A chunk request that receives no body byte for 20 s is closed and retried (MEDIA-SPEC §7,
    "Transfer health": the same copier and watchdog as manifest fragments). Every retry of a chunk
    (a stall, a connection error, a 5xx, a renewal after 403/410) continues after the bytes the
    chunk already wrote (§2.5, "Chunk prefixes").
- **Rate limits.** The global → queue → download limiter chain, plus `HostLimiter(host)`.
- **IP family.**
  - If a format URL carries an `ip` query parameter that parses as an IP address, direct connections
    for that track dial only that family (`tcp4` or `tcp6`).
  - Through a proxy nothing is pinned, because the proxy dials.
  - This is an inference from YouTube's URL binding (§8.4).
- **Headers.** Each format's own headers (§6.2). Cookies go only to the track URL's own origin, and
  the transport's redirect rules drop them across origins.
- **`available_at`.** No request before the latest `available_at` of the chosen formats. A value more
  than 10 min ahead makes the format unusable. While waiting, the download stays PROBING.
- **Responses.**
  - A 206 must start at the requested byte and end at or before the requested end. A short 206
    continues, as in M5.
  - A 200 to chunk 0 whose length is the track size is accepted as the whole track in one stream:
    the track becomes unranged and restarts from 0 when interrupted.
  - A 200 to any other chunk, or to a retry of chunk 0 after an early pass started other chunks,
    is `ErrRangeMismatch` and is retried; the chunk keeps its prefix.
  - A 200 to chunk 0 asked for from inside the chunk (a resumed or retried chunk 0 with a prefix)
    drops chunk 0's prefix and is retried from byte 0, where the rule above may make the track
    whole (amendment 2026-10-29).
  - A 403 or 410 triggers the refresh of §2.6.
- **Init check (remux).** Chunk 0 is cut back to its complete top-level boxes. `ftyp` and a `moov`
  containing `mvex` must be among them. `media.CheckInit` then runs on the bytes up to the end of
  `moov`:
  - protection → `encrypted`
  - no `moov` or `mvex` (a progressive, non-fragmented MP4), or `moov` longer than the chunk →
    `media_unsupported`, before anything else is fetched
- **Init check (copy).** The muxed MP4 track gets `CheckInit` on its `moov`. If `moov` is complete in
  chunk 0, the check runs at once; otherwise (`moov` at the end) it runs on the finished file before
  it is published.
- **Early pass (amendment 2026-10-28).** While chunk 0 arrives as a 206, its worker counts the
  bytes written from byte 0 and tells the job at most once per 64 KiB. The job walks the top-level
  box headers of that prefix (within what the full check reads: chunk 0, at most 16 MiB) and runs
  the check once `moov` is complete in it:
  - copy: the first box is `ftyp` and `CheckInit` accepts everything up to the end of `moov`;
  - remux: an `ftyp` comes before `moov`, `CheckInit` accepts it and `moov` holds `mvex`.

  The prefix can only **pass**. Anything else (protection, no `mvex`, not an MP4, a `moov` larger
  than chunk 0, a copied file's `moov` at the end) is undecided: the job waits for the complete
  chunk 0, so every refusal and its error code are decided exactly as before, with no other
  request made. The full check of the complete chunk 0 still runs after an early pass, and its
  failure (for example a malformed box later in chunk 0) fails the job. Each new attempt at chunk 0
  (a retry, or a renewal after 403/410) continues after the prefix chunk 0 kept (§2.5) and walks
  that prefix again from byte 0 (amendment 2026-10-29: before, it counted from zero). A resumed run
  walks chunk 0's durable prefix before anything is requested, so a prefix that already holds the
  `moov` opens the gate at once. An early pass applies to 206 answers only: a 200 to chunk 0 still
  makes the track one whole request (below), but once an early pass has started other chunks, a
  200 to a retry of chunk 0 is retried like a 200 to a later chunk. Durable bits, checkpoints and
  resume are unchanged (§2.5): chunk 0's bit is set only after a sync that began after its last
  byte.
- **When a chunk fails the job** (for example a 403 again after the renewal, §2.6), every chunk
  still in flight is cancelled, chunk 0 of a track included. What each of them wrote is synced and
  recorded as its prefix (§2.5), and resume asks only for the rest (amendment 2026-10-29: before,
  it was fetched again in full). Every chunk whose response completed before the failure is
  synced and kept in the final checkpoint (unless the failure is that sync), and resume never
  fetches it again. (Before the amendment, chunk 0 of every track was always complete before any
  other chunk could fail; with the early pass a later chunk can fail while chunk 0 still arrives.)
- **Assembly.**
  - `OutputRemux`: `remux.Assemble` with `Input{Container: fmp4, Init: "", Fragments: [data]}` per
    track, video first. The result is an `.mp4`.
  - `OutputCopy` (one muxed MP4 track): the track's `data` file is renamed to `<name>.dxpart` on the
    same volume, so no copy is made. The result is an `.mp4`.

### 2.5 Resume and crash safety
- **Layout.** `<dir>/<name>.dxparts/<kind>-<id>/data`, one file per track (the id is mapped as in
  MEDIA-SPEC §7). It is created and preallocated to the track size (`platform.Preallocate`) and never
  truncated on resume. Chunks are written with `io.NewOffsetWriter`.
- **Durable bits.** A chunk's bit is set only after an `fsync` that began after its last byte was
  written. Syncs may be batched (at most every 1 s or 64 MiB), and no bit is set before its sync.
  Checkpoints are stored as in M5: every 2 s and when the run stops.
- **Chunk prefixes (amendment 2026-10-29, owner note: a paused YouTube video went back from 13% to
  10% and resumed there).** A chunk without its bit keeps what it received:
  - Its worker counts the bytes it wrote contiguously from the chunk's start (`wrote`; a byte counts
    once its write returned). A retry, a renewal and a resume ask for the rest of the chunk after
    them, never from its start. The 10 MiB googlevideo cap still splits what is left.
  - Order, as for bits and the engine's flush frontier: write → note `wrote` → `fsync` → record the
    noted length as the chunk's durable prefix (`part`) → store the checkpoint. A crash loses at most
    the growth that was not recorded yet; what the file holds beyond a recorded prefix is written
    again.
  - Prefixes are recorded with every sync of the track's file (the bit syncs above), at least every
    8 s while chunks only grow (the engine's flush interval; more often slowed the disk-bound case in
    the 2026-10-28 measurements), and always when the run stops (pause, failure, cancellation),
    after its workers have stopped.
  - Chunks with a prefix are taken first when a run starts, so that they are completed.
  - A track fetched as one whole resource (unranged, or made whole by a 200 to chunk 0) never has
    prefixes: it restarts from byte 0, as before.
  - `Progress.BytesDone` is the complete chunks plus every chunk's `wrote`: it never decreases across
    a stall, a retry, a renewal, a pause or a resume. It decreases only when a whole-resource
    request fails (it restarts from 0) or a 200 drops chunk 0's prefix. After the last sync of a run
    it equals the checkpoint's `durableBytes`.
- **Checkpoint.** `{trackId, total, done, key, chunkBytes, partial}` per track, plus `durableBytes`
  (the bytes of the done chunks and the prefixes) and `bytesTotal` (the exact size of the tracks
  when every one is known, else 0). `partial` maps a chunk index to its prefix length (JSON
  `omitempty`; checkpoints written before 0.8.2 have none and resume exactly as before, their chunks
  without bits in full; an older daemon ignores the field and fetches those chunks in full). Such an
  old checkpoint (no `durableBytes`, `bytesTotal` or `partial`) stored a bytes-done that counted chunks
  still arriving: while it is stopped, downexd shows the bytes of its done chunks instead (each
  min(`chunkBytes`, the track's size − its start); with an estimated size they only bound the stored
  value), which is where the resumed run starts, so the progress never drops on that resume. An
  entry is used only when it fits: the index is in the plan, the chunk has no bit, 0 < length <
  the chunk's length, and the track is ranged; any other entry is ignored, never an error. On
  resume, a track continues only when all of these hold:
  - its Key matches
  - `chunkBytes` matches
  - its `data` file exists with exactly the planned size

  Otherwise that track starts over (bits and prefixes) and the other one keeps its chunks. A
  renewal keeps the key (it includes the exact size and `lmt`, §3.11) and the chunk size, and
  `Content-Range` (start and total) is checked on every answer, so a prefix is never continued from
  another resource.
- **Not re-hashed.** Chunks with bits set and recorded prefixes are trusted. A chunk that was
  written but has no bit continues after its durable prefix (from its start without one).
- **Unranged tracks.** They restart from 0, because the server cannot resume them.
- **Output.**
  - The output record, the no-replace rename and `finishAssembled` are unchanged from MEDIA-SPEC §7,
    so a completion that was not stored is not downloaded again.
  - A kill during assembly leaves `.dxpart`; the next run rewrites it.
  - The disk peak is about twice the final size until the part directory is removed (§9).
- **Commands.** Restart (`Resume{restart:true}`) deletes the part directory and checkpoint, and
  Remove with `delete_file` deletes both, as for M5. A daemon restart re-queues PROBING and
  DOWNLOADING site downloads. The extraction cache is in memory only, so they resolve again.

### 2.6 URL expiry and re-resolution
- **Never stored.** Format URLs and their headers are never stored. The record keeps only the page
  URL, the track ids and keys, sizes and labels.
- **Cache.**
  - Kept in memory by `sitejob.Resolver`.
  - The cache key is the page URL with its fragment removed, the SHA-256 of the Cookie value, the
    Referer and the proxy route.
  - At most 64 entries (at least the extension's 50 kept answers, §5.2, so that look-aheads never
    push out a page whose qualities a browser still shows), least recently used dropped first, kept
    15 min.
  - An entry is reused for a run only when it is at most 15 min old and every chosen format URL whose
    expiry is known (`expire` query parameter) stays valid for at least 30 more minutes.
  - Probe, look-ahead (§5.6) and Add share entries, so a pick sent right after the picker opened
    does not run yt-dlp again.
- **Every run resolves.** Every run resolves again unless the cache entry qualifies.
  - The chosen tracks are found by **Key** (§3.11). The id alone never selects a track.
  - If a Key is missing, the run resolves once more with the cache bypassed, after 5 s.
  - If the Key is still missing, the run fails with `media_invalid` ("the chosen quality is no longer
    offered"). Downex never falls back to another quality.
  - If the site now offers only a different format kind (`http` ↔ `hls`/`dash`), the run also fails
    with `media_invalid`.
- **During a run.**
  - A 403 or 410 on any chunk calls `Refresh`, which re-runs yt-dlp with the cache bypassed. The URLs
    and headers of all tracks are replaced by Key, and the chunk is retried without counting the
    failed attempt.
  - `Refresh` also runs proactively when a URL's known expiry is less than 2 min away.
  - At most one refresh per 60 s and five per run.
  - If the same chunk fails with 403 again after a refresh, or a refresh fails, the run fails
    (`http_status`, or the refresh's own code) and keeps its progress. Resume works.

## 3. Contracts

### 3.1 Proto (`proto/downex/v1/downex.proto`, additive; regenerate `internal/rpc` with buf)
```proto
// Where a media download's presentation comes from (docs/SITE-MEDIA-SPEC.md).
enum MediaOrigin {
  MEDIA_ORIGIN_UNSPECIFIED = 0; // = MANIFEST: every client written before site media
  MEDIA_ORIGIN_MANIFEST = 1;    // an HLS/DASH manifest URL (MEDIA-SPEC)
  MEDIA_ORIGIN_SITE = 2;        // a web page resolved with yt-dlp
}
message MediaTrack {            // 1-9 unchanged
  int64 size = 10;              // bytes as the site declares them; 0 = unknown
  bool size_approx = 11;        // size is the site's estimate
  string ext = 12;              // the site's file type for this track: "mp4" | "m4a" ("" = unknown)
}
message MediaInfo {             // 1-10 unchanged. SITE: manifest_url = "", format = "http" | "hls" | "dash",
                                // container = "mp4" (always: every site download is one .mp4)
  MediaOrigin origin = 11;
  string site = 12;             // SITE: the site's display name, e.g. "YouTube"
  string page_url = 13;         // the page (SITE: the address resolved; MANIFEST: the page hint)
}
message AddMediaRequest {       // 1-11 unchanged
  MediaOrigin origin = 12;      // SITE: page_url is the address and manifest_url must be empty
}
message ProbeMediaRequest {
  string manifest_url = 1;
  repeated Header headers = 2;
  string page_url = 3;          // SITE: the page to resolve
  MediaOrigin origin = 4;
  bool prefetch = 5;            // SITE: a look-ahead while the user watches (§5.6; added 2026-10-22)
}
// ConfirmRequest is unchanged: video_track/audio_track pick any offered MP4 quality.
message HostRule {              // 1-3 unchanged
  int64 max_request_bytes = 4;  // 0 = no cap; else 1 MiB..1 GiB per ranged request to the host
}
message Settings {              // 1-23 unchanged
  bool site_media = 24;         // default true: resolve video pages with yt-dlp (downloaded on first use)
  bool tools_auto_update = 25;  // default true: daily check; updates only Downex's own yt-dlp and Deno
  string tools_channel = 26;    // "stable" | "nightly"; "" reads as the default "nightly"
}
enum ToolState {
  TOOL_STATE_UNSPECIFIED = 0;
  TOOL_STATE_MISSING = 1;
  TOOL_STATE_INSTALLING = 2;    // also while updating; a ready copy keeps working meanwhile
  TOOL_STATE_READY = 3;
  TOOL_STATE_FAILED = 4;
  TOOL_STATE_DISABLED = 5;      // site_media off, or no build for this platform (deno on musl, 32-bit, armv7)
}
message ToolInfo {
  string name = 1;              // "yt-dlp" | "deno"
  ToolState state = 2;
  string version = 3;           // of the copy in use ("" = none)
  string path = 4;              // absolute path of the copy in use
  bool managed = 5;             // true: Downex's tools folder; false: found on PATH
  string latest_version = 6;    // last seen release ("" = not checked)
  google.protobuf.Timestamp checked_at = 7;
  int64 bytes_done = 8;         // while INSTALLING
  int64 bytes_total = 9;        // while INSTALLING; -1 = unknown
  string error = 10;            // short text of the last failure, no URLs (≤ 200 bytes)
  string error_code = 11;       // network | tls | http_status | checksum | signature | authenticode |
                                // disk_full | unsupported_platform | smoke_test | internal
  string verified = 12;         // what Downex checked: "openpgp+sha256" | "sha256+authenticode" | "sha256" | "" (PATH copy)
}
message ToolsStatus { repeated ToolInfo tools = 1; bool enabled = 2; bool ready = 3; }
message GetToolsRequest {}
message GetToolsResponse { ToolsStatus status = 1; }
message InstallToolsRequest { bool update = 1; } // start or join the install; update=true also looks for newer releases
message InstallToolsResponse { ToolsStatus status = 1; } // when done, or at the caller's deadline (the install goes on)
service DaemonService {         // existing RPCs unchanged
  rpc GetTools(GetToolsRequest) returns (GetToolsResponse);
  rpc InstallTools(InstallToolsRequest) returns (InstallToolsResponse);
}
message Event { oneof kind {    // 1-6 unchanged
  ToolsStatus tools = 7;        // Watch without ids: in the first batch, then on each change (≤ 4 batches/s)
} }
```
- `ToolsStatus.ready` is true when yt-dlp is READY and Deno is READY or DISABLED for the platform.
- For SITE, `Download.url` is the page URL and `final_url` is empty.

### 3.2 Error codes
Each refusal message starts with `<code>: `, and the code is also sent as the `Downex-Error-Code`
metadata (MEDIA-SPEC §7). `Download.error_code` uses the same strings for failed runs, plus the file
codes (`http_status`, `network`, `tls`, `disk_full`, ...).

| Code | Connect code | Meaning |
|---|---|---|
| `encrypted` | InvalidArgument | DRM: `_has_drm` with no usable format, any chosen format with `has_drm`, or protection in an init |
| `live` | InvalidArgument | `is_live`, or `live_status` `is_live` / `is_upcoming` / `post_live` |
| `media_unsupported` | InvalidArgument | playlist/multi-video; no MP4 video on the page (§3.11); site media in a daemon without `Config.Site` |
| `media_invalid` | InvalidArgument | malformed or oversized yt-dlp output; a chosen track or Key the page no longer has |
| `site_unsupported` | InvalidArgument | yt-dlp found no video on the page ("Unsupported URL", `_type` url) |
| `site_unavailable` | FailedPrecondition | private, removed, geo-blocked, rate-limited by the site ("try again later") |
| `auth_required` | FailedPrecondition | sign-in, age check, members-only or bot check (existing M6 code) |
| `site_error` | FailedPrecondition | yt-dlp failed otherwise: unknown error, exit code 2, crash, 90 s timeout, proxy it cannot use |
| `site_resolving` | FailedPrecondition | retryable: the extraction was still running at the wait budget; it goes on and is cached |
| `tools_installing` | FailedPrecondition | retryable: yt-dlp/Deno are being downloaded; the message carries the progress |
| `tools_unavailable` | FailedPrecondition | install or verification failed, unsupported platform, or this daemon never downloads tools |
| `site_media_off` | FailedPrecondition | `Settings.site_media` is false |

- Sentinels live in `internal/sitemedia/errors.go` (pure), so the daemon never imports the job:
  `ErrSiteUnsupported`, `ErrSiteUnavailable`, `ErrAuthRequired`, `ErrSiteError`, `ErrSiteResolving`,
  `ErrToolsInstalling`, `ErrToolsUnavailable`, `ErrSiteMediaOff`. Their texts are the user-facing
  phrase after the code prefix (no package prefix), distinct and none containing another, so
  `codeFromMessage` can map a stored message back; callers wrap them as `fmt.Errorf("%w: %s",
  sentinel, reason)`, and `fmt.Errorf("%w (34%%)", ErrToolsInstalling)` gives §4.6's progress text.
  The same file has `OriginManifest = ""` and `OriginSite = "site"`, the only values of
  `model.Media.Origin`.
- `encrypted`, `live`, `media_unsupported` and `media_invalid` reuse the `internal/media` sentinels.
- `mediaErrorCode` and `codeFromMessage` (`internal/daemon/errors.go`) map all of them.

### 3.3 Daemon behaviour
- **AddMedia/ProbeMedia with `origin = SITE`.**
  - `page_url` is required (`validate.URL`, without user information: it reaches yt-dlp's argv).
    `manifest_url` must be empty (InvalidArgument).
  - Headers pass `validate.Headers`, and only two are used:
    - `Cookie`: the user's opt-in (§6.3); it goes into the cookie file
    - `Referer`: an http(s) URL without user information; it becomes `--referer`

    Every other header is ignored.
  - Track ids use `validate.TrackID`.
  - `site_media` off → `site_media_off`. `Config.Site == nil` → `media_unsupported`.
  - ProbeMedia with `prefetch` is a look-ahead (§5.6): it never calls `Tools.Ensure`
    (`tools_unavailable` at once while the tools are known not to be ready; while the copies have not
    been looked for yet since the start, `ToolsSnapshot.Discovering`, it goes on and the resolver's
    `AcquireReady` waits for that discovery) and never waits behind other extractions
    (`site_resolving` at once); `prefetch` without origin SITE is InvalidArgument.
- **Wait budget.**
  - It runs from receipt to min(caller deadline − 2 s, 25 s), or 25 s when the caller sets no
    deadline.
  - `Tools.Ensure` and `Site.Probe` share it. When it ends, the answer is `tools_installing` or
    `site_resolving`, while the install or extraction goes on (owned by the tools manager or the
    resolver, never by the request).
  - While yt-dlp or Deno is being installed, `tools_installing` with the progress comes after 1 s
    (`siteInstallWait`), not at the end of the budget, so that clients show the progress as it
    goes; a discovery of the copies is waited for within the budget.
  - With these short calls the relay's 30 s, the UI's 30 s `callTimeout` and the CLI's 30 s
    `rpcTimeout` all stay as they are; clients retry (§3.5, §3.7, §5.4).
- **Recording (AddMedia).**
  - The probe always runs before anything is recorded, as for manifests.
  - `model.Media` gets:
    - `Origin = "site"`, `Site`, `PageURL`
    - `Format` `http`/`hls`/`dash` and `Container` (§3.11)
    - `Tracks`, each with `Size`, `SizeApprox`, `Ext` and `Key`
    - `Title`

    It is stored as JSON, so no store migration is needed.
  - The name comes from the request's `filename`, else the yt-dlp title, else the request's `title`,
    else the page path's last segment, else `video`. It is sanitised, and `.mp4` is appended.
  - The category is Video.
  - `expected_size` is the sum of the chosen tracks' sizes (estimates allowed), or -1.
  - The "awaiting" rule is M5's (DESKTOP-SPEC §1).
- **Confirm (SITE, format `http`).**
  - `selectMediaTracks` behaves as in M5: the video track is any offered MP4 quality, and the audio
    track can be chosen only for a video-only pick.
  - The container stays `mp4` whatever the pick, so the file name keeps its extension.
    `expected_size` is recomputed from the new pick and the category stays.
  - Formats `hls`/`dash` follow MEDIA-SPEC §7 unchanged.
- **runMedia (SITE).**
  1. The state stays PROBING ("Getting video info…") through `Tools.Ensure(runCtx)`, the resolution
     and the `available_at` wait.
  2. It becomes DOWNLOADING with the first progress report.
  3. `Download.size` is `MediaProgress.BytesTotal` while running (when known), then the file size.
     Paused, failed or resolving again after it received bytes, it stays exact: the total the
     job stored with its checkpoint (`bytesTotal`), else the chosen tracks' sizes when every one is
     exact; with an estimated size the tracks' sizes (`size_approx`). It is never extrapolated
     from the parts, whose chunks are of uneven sizes (amendment 2026-10-29: a paused 306 MiB
     video showed "~504 MiB"). The stored bytes done are the checkpoint's `durableBytes` (for a
     checkpoint written before 0.8.2, the bytes of its done chunks, §2.5). This is the rule of a
     web page's direct formats only: a site's HLS/DASH stream (the manifest fallback) keeps the
     stream rules, an estimate ("~") while running and stopped alike, even when yt-dlp gave every
     track an exact size (the assembled MP4 differs from the tracks' sum).

  Pause cancels any step. Everything else (checkpoint storage, completion, removal of the part
  directory) is M5's. `MediaRunOptions` gains `HostRules`, so host rules now also cap M5 manifest
  jobs (M6-SPEC §2 is updated by the integration step).
- **Network.** The proxy for yt-dlp is the route the daemon's network settings give the page URL:
  `direct`, an HTTP proxy, or SOCKS4/4a/5 (§4.7).
- **Tools RPCs.**
  - `GetTools` returns the status. `InstallTools` starts or joins the install and returns when it
    ends or at the caller's deadline (UI: 15 min).
  - Errors: `site_media_off`; `tools_unavailable` when the install failed or when this daemon never
    downloads tools.
  - Tools changes are published as `Event.tools`.
- **Downloads by daemon kind.**
  - Only the user's own daemon (no `--db`/`--endpoint`/`$DOWNEX_ENDPOINT`, as for the tray) downloads
    and updates tools. The e2e hook of §8.3 is the only other case.
  - Other daemons use PATH copies or copies already in their tools folder. Otherwise they answer
    `tools_unavailable`.
- **Shutdown.** It kills running extractions and installs (their job objects or process groups) and
  removes their work directories.

### 3.4 Go contracts between lanes
```go
// internal/media/ranged.go (Step 0)
type RangedTrack struct {
	ID         string      // the site's format id
	Key        string      // stable identity across resolutions (§3.11)
	Kind       TrackKind   // KindVideo, KindAudio or KindMuxed
	Ext        string      // the site's file type: "mp4" or "m4a"
	URL        string      // absolute http(s) URL; never stored, never logged beyond scheme://host/path
	Headers    http.Header // request headers for this format (§6.2); Cookie only for its own domain
	Size       int64       // exact bytes; <= 0: unknown, one whole-resource request
	ChunkBytes int64       // MinChunkBytes..MaxChunkBytes
	Expires    time.Time   // URL expiry when known (zero = unknown)
	Network    string      // "" | "tcp4" | "tcp6": dial only this family for direct connections
	Unranged   bool        // no_resume: one connection, restarted from 0 when interrupted
}
const (MinChunkBytes = 1 << 20; MaxChunkBytes = 64 << 20; DefaultChunkBytes = 16 << 20; MaxRangedSize = 1 << 40)
func PlanRanged(t RangedTrack) (*Plan, error) // sets Plan.SingleResource (new field); ≤ MaxSegments fragments
// Ranges of ChunkBytes (the last shorter), fragment 0 marked Init; Size <= 0 or Unranged: one
// whole-resource fragment (ByteRange nil, Init, ChunkBytes unused). Container ContainerFMP4 for
// Ext "mp4"/"m4a". ErrMalformed: URL not absolute http(s) with a host, Kind/Network outside their
// values, ChunkBytes outside Min..Max; ErrTooLarge: URL > MaxURIBytes, Size > MaxRangedSize, more
// than MaxSegments ranges. Plan.MediaFragments counts every fragment of a SingleResource plan.
// Track (internal/media/tracks.go) gains Size int64, SizeApprox bool, Ext string (site tracks only;
// since 0.7.1 a manifest track the probe measured, VIDEO-DETECT-SPEC §3.3, has Size + SizeApprox).

// internal/mediajob (Lane C)
type RangedSelection struct {
	Tracks    []media.RangedTrack // the video or muxed track first, then the audio track; 1 or 2
	Output    string              // OutputRemux | OutputCopy (one muxed MP4 track)
	NotBefore time.Time           // no request before this time (available_at)
	// Refresh resolves again and returns the tracks with fresh URL, Headers, Expires and Network,
	// matched by Key (an error wrapping ErrInvalidSelection when a Key is gone). nil: 403/410 are final.
	Refresh func(ctx context.Context) ([]media.RangedTrack, error)
}
const (OutputRemux = "remux"; OutputCopy = "copy")
// RunRanged behaves like Run (checkpoints, progress, output.json, no-replace rename, cancellation);
// it returns the container written, always "mp4".
func RunRanged(ctx context.Context, opts Options, sel RangedSelection, resume *Checkpoint,
	onProgress func(Progress), onCheckpoint func(Checkpoint) error) (string, error)
type HostRules interface { // satisfied by engine.HostRules values; nil = no host rules
	HostMaxConnections(host string) int
	HostLimiter(host string) limiter.Limiter
}
// Options gains `HostRules HostRules` (Run and RunRanged). Step 0 adds the mirrored fields
// Progress.BytesTotal (after BytesDone; = daemon.MediaProgress.BytesTotal) and
// TrackCheckpoint.ChunkBytes `json:"chunkBytes,omitempty"` (= model.MediaTrackCheckpoint.ChunkBytes).
// 0.8.2 (§2.5, chunk prefixes) adds TrackCheckpoint.Partial map[int]int64 `json:"partial,omitempty"`
// and Checkpoint.DurableBytes, BytesTotal (omitempty), mirrored by model.MediaTrackCheckpoint.Partial
// and model.MediaCheckpoint.DurableBytes, BytesTotal; Clone copies Partial.

// internal/engine (Lane C): optional extension of HostRules, found by type assertion
type RequestCaps interface { HostMaxRequestBytes(host string) int64 } // 0 = no cap
// internal/transport (Lane C)
func WithNetwork(f Factory, network string) Factory // "" returns f; proxied connections unaffected

// internal/daemon/site.go (Lane D)
type SiteSelection struct {
	PageURL                string
	Cookie, Referer        string    // "" = none
	Proxy                  SiteProxy // the route for PageURL
	Format                 string    // "" (probe) | "http" | "hls" | "dash": the format recorded at Add
	VideoTrack, AudioTrack string
	VideoKey, AudioKey     string
	Prefetch               bool // Probe only: a look-ahead (§5.6), passed to sitejob.Request.Prefetch
}
type SiteProxy struct{ URL, Username, Password string } // URL "" = direct; never logged
type SiteProbe struct {
	MediaProbe  // Format "http"|"hls"|"dash"; Container "mp4"; Tracks; Default*; Duration; FragmentsTotal (0 for http)
	Site, Title string
}
type SiteMedia interface {
	// Probe returns by ctx's deadline; an extraction still running then fails with ErrSiteResolving.
	Probe(ctx context.Context, tf transport.Factory, sel SiteSelection) (*SiteProbe, error)
	Run(ctx context.Context, opts MediaRunOptions, sel SiteSelection, resume *model.MediaCheckpoint,
		onProgress func(MediaProgress), onCheckpoint func(model.MediaCheckpoint) error) (container string, err error)
}
type Tools interface {
	Configure(enabled, autoUpdate bool, channel string)
	Status() ToolsSnapshot                      // mirrors ToolsStatus
	Ensure(ctx context.Context) error           // nil when ready; ErrToolsInstalling at ctx end; ErrToolsUnavailable; ErrSiteMediaOff
	Install(ctx context.Context, update bool) (ToolsSnapshot, error)
	CheckUpdate(ctx context.Context) error      // the daily timer (tools_service.go)
	Subscribe(fn func()) (cancel func())
}
// Config gains Site SiteMedia and Tools Tools (nil = site media unavailable). MediaRunOptions gains
// HostRules engine.HostRules. MediaProgress gains BytesTotal (Step 0).

// internal/tools (Lane A)
func New(cfg Config) (*Manager, error)
type Config struct {
	Dir          string                        // tools folder, absolute (required)
	Client       *http.Client                  // https only, Downex's proxy (update.NewHTTPClient); nil = never download
	ReleaseBase  string                        // "https://github.com" (e2e: a local TLS server)
	YtDlpKeys    [][]byte                      // armored OpenPGP keys; nil = the embedded yt-dlp key
	LookPath     func(string) (string, error)  // exec.LookPath
	Now          func() time.Time
	GOOS, GOARCH string
}
// *Manager implements daemon.Tools (through an adapter in cmd/downexd) and:
func (m *Manager) Acquire(ctx context.Context) (*Lease, error) // the copies to run; holds their versions
func (m *Manager) AcquireReady(ctx context.Context) (*Lease, error) // a look-ahead's (§5.6): ready copies only, never an install or a trial's copy
func (m *Manager) Report(l *Lease, outcome Outcome)            // OK | SiteError | BadOptions: fallback/rollback (§4.5)
func (m *Manager) Close() error
type Lease struct{ YtDlp, Deno, YtDlpCache, DenoCache, WorkRoot, YtDlpVersion string; YtDlpManaged bool }
func (l *Lease) Release()
func Exec(ctx context.Context, s ExecSpec) (ExecResult, error) // §4.7 process rules
type ExecSpec struct {
	Path string; Args, Env []string; Dir string
	Timeout time.Duration; MaxStdout, MaxStderr int; MemoryLimit int64
}
type ExecResult struct{ Stdout, StderrTail []byte; ExitCode int; TimedOut, StdoutTruncated bool }

// internal/sitemedia (Lane B; errors.go is Step 0)
func Parse(data []byte, now time.Time) (*Info, error) // §3.10
func Offers(info *Info, pageHost string) (*Offer, error)
func Select(info *Info, off *Offer, video, audio, videoKey, audioKey string) (*Pick, error)
type Pick struct {
	Tracks []media.RangedTrack // ChunkBytes still 0: the caller applies §2.4
	Output, Container string   // Container is always "mp4"
	NotBefore time.Time
}
// internal/sitejob (Lane B)
func NewResolver(cfg ResolverConfig) *Resolver // Tools *tools.Manager; MaxConcurrent 2; TTL 15 min; 64 entries; Timeout 90 s; MaxOutput 32 MiB
func (r *Resolver) Resolve(ctx context.Context, req Request) (*sitemedia.Info, error) // joins running work; ErrSiteResolving at ctx end
type Request struct{ PageURL, Cookie, Referer string; Proxy Proxy; Fresh, Prefetch bool } // Prefetch: §5.6, not in the cache key
var ErrBusy error // wraps ErrSiteResolving: a look-ahead that would have to wait; nothing started
```

### 3.5 Native messaging (BROWSER-SPEC §2, protocol 1, additive)
- **`welcome`** gains `features: ["video", "prefetch"]` (`prefetch` since the look-ahead, §5.6). The
  extension reads at most 16 entries of at most 32 characters each. If `video` is missing, the
  extension says "Update Downex to download videos" instead of sending video requests; without
  `prefetch` it sends no look-ahead (an older relay would drop the mark, and the daemon would take it
  for a user's probe, which installs tools).
- **Errors.** `{code, message, reason?}`. `reason` is the daemon's `Downex-Error-Code` (at most 64
  characters, `[a-z_]`), forwarded for every request type. `message` keeps its `<code>: ` prefix.
- **`probeVideo`** has no side effects and is safe to retry.
  - Request: `{id, type, pageUrl, headers?, deadlineMs?, prefetch?}`.
    - `pageUrl`: http(s), `validate.URL`, at most 32 KiB.
    - `headers`: `[[name, value], …]` through `validate.Headers`; only `Cookie` (opt-in) and
      `Referer` (the embedding page) are meaningful.
    - `prefetch`: an optional boolean, true for a look-ahead (§5.6); missing or null is false, any
      other value `invalid_request`. `addVideo` ignores it.
  - The relay sends `ProbeMedia{origin: SITE, page_url, headers, prefetch}`. A look-ahead never starts
    downexd: with none running it is `daemon_unavailable` (the user may have quit Downex).
  - Answer:
    ```
    {type: "videoInfo", site, title, durationMs, format, container, selectedVideo, selectedAudio,
     tracks: [{id, kind, ext, codecs, width, height, frameRate, bandwidth, size, sizeApprox, language, name}]}
    ```
  - Limits: at most 200 tracks; `size` 0 means unknown. The answer never contains format URLs or
    yt-dlp headers.
- **`addVideo`** has a side effect.
  - Request: `{id, type, pageUrl, videoTrack?, audioTrack?, title?, headers?, deadlineMs?}`.
    `title` uses `validate.Title`.
  - The relay sends `AddMedia{origin: SITE, source: BROWSER, page_url, headers, title, video_track,
    audio_track}`.
  - Answer: `{type: "added", downloadId, awaiting}`, where `awaiting` is true when the download waits
    for confirmation.
  - The `minAddTime` (150 ms) rule of `add` applies.
- **Timing.**
  - Both requests are capped by `videoTimeout = 30 s` (deadlineMs only shortens it) and keep the
    200 ms `responseMargin`.
  - They run under their own semaphore `maxVideoInFlight = 4`, apart from `maxInFlight = 16`, so slow
    probes never delay capture adds.
  - Encoded answers stay at most 256 KiB.
- **Logging.** The relay logs the page URL redacted, the header count and the title length, never
  header values.

### 3.6 Extension runtime messages (validated in `isExtensionMessage`)
- **Content script → background:**
  - `downex:page-video-probe {url ≤ 32 KiB}` → `Outcome<VideoInfo>`. Local codes:
    `unsupported_page`, `outdated_downex`.
  - `downex:page-video-add {url, videoTrack, audioTrack}` → `Outcome<Added>`.
  - `downex:page-video-prefetch {url}` (not answered; added 2026-10-22): the top frame's dwell on a
    watch or Shorts video ended (§5.6). Taken only from frame 0 of an active tab.
- **Checks on content-script messages:**
  - `sender.tab.id ≥ 0`, not incognito
  - `url` parses with `parseSiteVideo` and its origin equals the sender frame's origin
  - for an embed frame, Referer = what the browser's default policy sends for `sender.tab.url`
    (http(s) only; `defaultPolicyReferer`: the origin alone across origins, never a fragment or user
    information; amended 2026-10-23, VIDEO-DETECT-SPEC §6.3). The daemon drops a Referer's fragment.
- **Popup → background** (`fromExtensionPage`):
  - `downex:tab-media` gains `videos: PageVideoEntry[]`. Each entry is `{key, site, kind,
    frame: 'top' | 'embed', pageUrl, title?, probe?, add?}`, with the probe or add state when one runs.
  - `downex:tab-video-probe {tabId, key}`
  - `downex:tab-video-add {tabId, key, videoTrack, audioTrack}`
  - `downex:find-page-video {tabId}`: the user-initiated generic path for any http(s) tab.

  The key is looked up again from `tabs.get` and `webNavigation.getAllFrames`; the popup's URL is
  never trusted.
- **Listeners.** New `onMessage` listeners are not `async`: they use `sendResponse` and
  `return true` (Chrome 155's `browser` namespace and Firefox both treat a returned Promise as the
  answer).

### 3.7 CLI
- **`downex media <url> --site …`** treats `<url>` as a web page. It takes the existing flags
  (`--list [--json]`, `--video ID`, `--audio ID`, `-o dir`, `--name f`, `--paused`, `--wait`,
  `--queue q`, `-H`).
  - `-H` is limited to `Cookie` and `Referer`; any other header is refused with a usage error.
  - On `tools_installing` or `site_resolving` it retries every 2 s for up to 5 min, printing one
    status line on the console: "Downloading yt-dlp and Deno: 34%" / "Getting video info… 12 s".
  - `--list` prints one row per offered track (one MP4 video per quality class, best first, then
    the MP4 audio tracks) with ID, KIND, QUALITY (the standard name, amendment 2026-10-23), PICTURE
    (width x height and frame rate), CODECS, SIZE (`~` for estimates), EXT and LANG, and marks the
    default with `*`. `--video ID` picks any listed quality, or another MP4 format of the page by its
    ID (the probe then lists it too).
- **`downex tools [status] [--json]`** prints per tool: state, version, managed copy or PATH and its
  path, how it was verified, the last check and the error.
- **`downex tools install`** (InstallTools{}) and **`downex tools update`** (InstallTools{update:true})
  follow the progress for up to 15 min.
- **`downex settings`** gains `--site-media on|off`, `--tools-auto-update on|off` and
  `--tools-channel stable|nightly`. Its view shows the `media` settings and the host rules'
  `max_request_bytes`.
- **`ls --json` / `info --json`:** the media view gains `origin`, `site`, `pageUrl`, and each track's
  `size`, `sizeApprox` and `ext`.
- **Stored state (amendment 2026-10-29, 0.8.2).** `info` (which always reads the database) and `ls`
  without a running downexd show a stopped download exactly as downexd's Get and List do: the size of
  §3.3 (a checkpoint's `bytesTotal`, a web page's probed size, a stream's extrapolated estimate, or
  unknown), its "~", and the stored bytes done (for a checkpoint written before 0.8.2, its done
  chunks, §2.5). Both take them from one owner, `internal/dlview`; downexd only adds a running job's
  progress. `ls` and `info` share the percent: of the bytes when the size is exact, else of the
  fragments (parts); `info` prints an estimate as "~316.0 MB (estimated)". Completed downloads show
  their file's size.

### 3.8 Settings
- `settings.json` gains `"media": {"site_media": true, "tools_auto_update": true,
  "tools_channel": "nightly"}`. A missing object or field reads as these defaults.
- An unknown channel is repaired to the default when loaded and refused (InvalidArgument) by
  UpdateSettings.
- `network.host_rules[].max_request_bytes` (0, or 1,048,576–1,073,741,824) is validated like the
  other host-rule fields. Built-in rules (§2.4) are not stored and not shown as user rules.
- The extension stores `videoOptions: {videoButton: true, useSignInForVideos: false,
  videoPrefetch: true}` in `storage.local`, never `storage.session`, because content scripts must be
  able to read it. `videoPrefetch` ("Prepare video qualities while you watch", §5.6) was added on
  2026-10-22; options stored before read it as true.

### 3.9 Tools folder
- **Location.** `platform.ToolsDir()`:
  - Windows: `%LOCALAPPDATA%\Downex\tools`
  - macOS: `~/Library/Application Support/Downex/tools`
  - Linux: `${XDG_DATA_HOME:-~/.local/share}/downex/tools`

  The rule `ToolsDirFor(goos, env)` is untagged and tested on every OS.
- **Which folder a daemon uses.** `downexd --tools-dir <abs>` wins. Otherwise the user's own daemon
  uses `platform.ToolsDir()` and other daemons use `<dir of --db>/tools`. Tests therefore never touch
  the real folder.
```
tools/
  tools.json                     current and previous versions, hashes, verification, bad versions (≤ 64 KiB)
  .lock                          exclusive file lock while installing, switching or cleaning
  yt-dlp/<version>/yt-dlp(.exe)  version = YYYY.MM.DD or YYYY.MM.DD.HHMMSS
  yt-dlp/<version>/_internal/    onedir copy (§4.2): the unpacked Python runtime next to the executable
  yt-dlp/<version>/downex-files.json  onedir copy: every unpacked file with its size and SHA-256
  deno/<version>/deno(.exe)      version = N.N.N
  cache/yt-dlp/                  --cache-dir (player data cache)
  cache/deno/                    DENO_DIR
  tmp/                           partial downloads; emptied at daemon start
  run/<16 hex>/                  one per yt-dlp run: tmp/ (TEMP/TMPDIR), cookies.txt (0600); removed after it
```
- **Permissions.** Folders are 0700 on Unix; on Windows they inherit the `%LOCALAPPDATA%` ACL.
  Executables are 0755 on Unix.
- **`tools.json`.** `{"version":1, "ytdlp":{…}, "deno":{…}}`. Each tool entry records: version,
  channel, relative file path, SHA-256, size, signature creation time, verification, install time,
  `previous`, and `bad`; yt-dlp's also `layout` (`"onefile"` or `"onedir"`; `""` = a copy installed
  before 2026-10-22, a onefile) and, for an onedir copy, `manifest` (the SHA-256 of
  `downex-files.json`). Discovery checks an onedir copy's executable and every listed file (size and
  SHA-256); files the list does not name are ignored.
  - It is written with the platform's atomic write (temp file, fsync, rename).
  - An unknown `version` value, or a file that does not parse, reads as empty, so the tools are
    installed again.

### 3.10 yt-dlp output (`internal/sitemedia.Parse`)
- **Input.** At most 32 MiB of stdout, one JSON object. yt-dlp writes ASCII (`ensure_ascii`).
  - Unknown fields are ignored.
  - A field of the wrong type, or a number that is not finite or is out of range, counts as absent.
  - A format that is then unusable is dropped, not fatal.
  - A structural JSON error or more than 2,000 formats → `media_invalid`.
- **Top level:**

| Field | Use and validation |
|---|---|
| `_type` | absent or `video`; `playlist`/`multi_video` → `media_unsupported`; `url`/`url_transparent` → `site_unsupported` |
| `id` | 1–256 printable bytes, required (key, cache) |
| `extractor_key` | `[A-Za-z0-9_]{1,64}`, required; display name `Youtube` → "YouTube", others as given |
| `title` | `validate.Title`; empty → `id` |
| `duration` | 0–10⁷ s, else 0 |
| `is_live`, `live_status` | refusal (§1); unknown `live_status` values are ignored |
| `_has_drm` | refusal when no usable format remains |
| `availability` | `needs_auth`/`premium_only`/`subscriber_only` with no usable format → `auth_required` |
| `formats` | ≤ 2,000 entries, below |
- **Per format:**

| Field | Use and validation |
|---|---|
| `format_id` | 1–64 printable bytes without spaces; later duplicates dropped |
| `ext` | `[a-z0-9]{1,16}` |
| `protocol` | `https`/`http` → direct; `m3u8_native`/`http_dash_segments` → manifest fallback (§3.11); anything else dropped |
| `url`, `manifest_url` | `validate.URL` (http(s), ≤ 32 KiB, a host); §6.1 address rule |
| `vcodec`, `acodec` | ≤ 128 printable bytes; `none` = absent. Both unknown: muxed for a video ext (`mp4 webm mov mkv m4v flv 3gp ts`), audio for an audio ext (`m4a mp3 ogg opus aac flac wav`), else dropped |
| `width`, `height` | 0–16,384 |
| `fps` | 0–1,000 |
| `tbr`, `vbr`, `abr` | kbit/s, 0–10⁶ (`bandwidth` = tbr × 1000) |
| `asr` | 0–10⁶ |
| `filesize` | 1–2⁴⁰, exact size |
| `filesize_approx` | 1–2⁴⁰, estimate (only when `filesize` is absent) |
| `container` | ≤ 32; `mp4_dash`/`m4a_dash` (any `*_dash` with ext mp4/m4a) = fragmented MP4 |
| `dynamic_range` | ≤ 16; anything but `SDR`/absent counts as HDR |
| `language` | ≤ 35 bytes of `[A-Za-z0-9-]`, else "" |
| `language_preference` | −1,000…1,000 |
| `format_note` | cleaned like a title, ≤ 128 characters |
| `http_headers` | object, ≤ 32 entries; §6.2 |
| `cookies` | string ≤ 16 KiB; §6.2 |
| `downloader_options.http_chunk_size` | 64 KiB–1 GiB, else ignored |
| `available_at` | Unix seconds; more than 10 min ahead → format dropped |
| `has_drm` | `true` or `"maybe"` → dropped and counted as protected |
| `no_resume` | true → `Unranged` |
| `request_data`, `impersonate`, `hls_aes`, `extra_param_to_segment_url`, `extra_param_to_key_url`, `is_from_start`, `fragments` (direct formats) | present → dropped |

- **From the URL query of direct formats**, used only as hints:
  - `expire`: decimal Unix seconds, within the next 7 days → `Expires`
  - `ip`: an IP literal → `Network`
  - `lmt`: decimal, ≤ 20 digits → part of the Key
- **Classification** (exit code ≠ 0, or no JSON):
  - The last `ERROR:` line of stderr, with the `[extractor] id:` prefix removed and case ignored, is
    matched against a table in `sitejob/classify.go`, tested with fixtures:
    - `encrypted`: "drm"
    - `live`: "live event will begin", "premieres in", "is live"
    - `auth_required`: "sign in", "log in", "login required", "confirm your age", "age-restricted",
      "not a bot", "members-only", "join this channel", "premium", "cookies"
    - `site_unavailable`: "private video", "video unavailable", "has been removed", "not available
      in your country", "geo", "content isn't available", "try again later", "rate-limit", "http
      error 429", "no longer available", "terminated", "copyright"
    - `site_unsupported`: "unsupported url"
  - Anything else is `site_error`.
  - Exit code 2 (bad options) is `site_error` and is reported to the tools manager (§4.5).
  - The user-visible reason is the cleaned line: URLs removed, bidi and control characters stripped,
    at most 200 bytes.

### 3.11 Offers, default pick, keys, labels (`internal/sitemedia`)
- **Usable direct formats** (protocol http/https, not dropped, URL allowed by §6.1; MP4 only):
  - **Pairable video**: video-only, `*_dash` container, ext `mp4`, vcodec `avc1`/`avc3`/`av01`/
    `hvc1`/`hev1`.
  - **Pairable audio**: audio-only, `*_dash` container, ext `m4a`/`mp4`, acodec `mp4a`/`ac-3`/`ec-3`.
  - **Muxed**: both codecs, ext `mp4`, vcodec and acodec of the lists above.
  - Everything else (WebM, VP9, Opus, other containers, audio-only pages) is not usable and is not
    mentioned anywhere.
- **What is offered (format `http`; amendment 2026-10-23):**
  - Video tracks: **one per quality class** (`media.QualityClass` of the picture; for YouTube
    `media.LabeledClass`: the class its `format_note` names when that is a ladder label that does
    not merely restate the raw height of a picture of another class) among the pairable video and
    muxed MP4 formats. Formats without either have no class: every one is offered, after the
    classes. Pairable videos count only when at least one pairable audio exists. Of a class the
    first in the order of "Default video" below is offered; the others are kept out of the list but
    stay selectable (`Select` finds them by key or ID, and `Offer.Listed` then lists the chosen one
    in place of its class's row).
  - Audio tracks are the pairable audios.
  - The tracks are ordered best first, videos then audios, at most 200 in all: the longer of the
    two lists loses its worst (a video that goes stays selectable, and is listed in place of the
    last offered video when chosen).
- **Format `hls`/`dash` (manifest fallback).** Used when no usable direct video or muxed format exists,
  and only when the probed container is `mp4`; otherwise `media_unsupported` ("No MP4 video on this
  page").
  - The `manifest_url` shared by the most `m3u8_native`/`http_dash_segments` formats is chosen; on a
    tie, the first.
  - It is probed with `mediajob.ProbeMP4` and the headers of one of those formats: only tracks that
    are fragmented MP4 with an initialization segment are listed (HLS: a media playlist with
    `EXT-X-MAP`, each fetched once, 4 at a time; DASH: no SegmentBase, no WebM/Matroska/MPEG-TS
    type), filtered and ordered by `sitemedia.ManifestTracks` (MP4 codecs only, one video per
    quality class, best class first and within a class in the order of the default video below
    (HDR unknown; then bandwidth, picture, size and the manifest's order), every video without a
    picture after the classes, then the audios, DEFAULT=YES first); the default is the first
    video. A probe that names a video the class rule leaves out lists it in place of its class's
    (`sitemedia.ManifestTracksFor`). Keys and IDs are M5's; names come from `internal/media`
    (`QualityName` of the picture).
  - *Amended 2026-10-23 (VIDEO-DETECT-SPEC.md §3.3, §4.8):* `ProbeMP4` also accepts HLS MPEG-TS
    media playlists (no `EXT-X-MAP`, or a TS one; not packed audio) whose `CODECS` pass
    `media.TSConvertible` (H.264, HEVC, AAC), with a second offer round when the best variant of a
    quality cannot be saved. Such tracks are converted to MP4 by the pure-Go TS→MP4 remuxer, so the
    probed container stays `mp4` and pages whose only formats are MPEG-TS HLS are offered too.
    `cmd/downexd/site.go` is unchanged.
  - Each run resolves the page and runs M5 on the fresh `manifest_url` with `Options.MP4Only`: a
    selection that would be saved as anything but an MP4 is refused (`media_unsupported`) before
    its fragments are fetched, and the daemon fails a site run that returns another container,
    removing that file, so nothing but an MP4 is ever saved under the `.mp4` name.
  - If neither path has anything → `encrypted` when protected formats were dropped, else
    `media_unsupported` ("No MP4 video on this page").
- **Default video** (the first in this order; amended 2026-10-23, and the same order picks the one
  video of each class):
  1. quality class, descending
  2. video-only before muxed
  3. SDR before HDR
  4. fps, rounded, descending
  5. `avc1`/`avc3` (H.264) > `hvc1`/`hev1` (HEVC) > `av01` (AV1)
  6. tbr, descending
  7. picture area, descending
  8. size, descending
  9. the format yt-dlp lists later (its `formats` are worst first)

  So the default is the best class Downex can save (never more than the source has), and each class
  prefers H.264. Every other offered class stays selectable. A page without any offered MP4 video
  (an audio-only page included) takes the manifest fallback above or is `media_unsupported`; audio
  alone is never a download.
- **Default audio** for a pairable video (the first in this order):
  1. original language: highest `language_preference`, then `format_note` containing "original"
  2. not DRC (`format_id` ending `-drc` or "DRC" in the note)
  3. abr/tbr, descending
  4. asr, descending
  5. `format_id`, ascending

  A muxed pick takes no audio.
- **Container and output** (the container is always `mp4`):
  - pair → remux
  - muxed `*_dash` mp4 → remux
  - other muxed mp4 → copy
- **Key.** `"s1-" + first 32 hex digits of SHA-256(extractor_key ‖ 0x00 ‖ id ‖ 0x00 ‖ format_id ‖
  0x00 ‖ language ‖ 0x00 ‖ exact filesize or "" ‖ 0x00 ‖ lmt or "")`. M5 keys are used for
  `hls`/`dash`.
- **Track fields.**
  - `ID` = `format_id`, `Bandwidth` = tbr × 1000.
  - `Codecs` = vcodec, acodec, or "vcodec, acodec" for muxed.
  - Video `Name` = `media.ClassName` of its class, its fps and HDR (amendment 2026-10-23), e.g.
    "4K (2160p)" for a 3840x1634 film or "1080p (Full HD) 60fps"; `Width`/`Height` keep the
    picture. Audio `Name` = the cleaned `format_note`.
  - `Size`/`SizeApprox`/`Ext` come from §3.10.
- **Codec names in all UIs:** `avc1`/`avc3` H.264, `av01` AV1, `hvc1`/`hev1` HEVC, `vp09`/`vp9` VP9,
  `mp4a` AAC, `opus` Opus, `ac-3` AC-3, `ec-3` E-AC-3.

## 4. Tool manager (`internal/tools`)

### 4.1 Discovery (PATH first)
- **yt-dlp:**
  1. A copy on PATH: `LookPath("yt-dlp")`; on Windows it must be an `.exe`, never `.bat`/`.cmd`/
     `.ps1`. `--version` must answer within 10 s with `YYYY.MM.DD[.N]`. It is accepted when that date
     is at most 90 days old (yt-dlp's own staleness rule) and Downex has not marked that version
     unsuitable (§4.5).
  2. The managed copy, checked at daemon start: size and SHA-256 against `tools.json`. A mismatch
     means it is installed again.
  3. None: install (§4.2).
- **Deno:**
  1. A copy on PATH whose `deno --version` first line `^deno (\d+\.\d+\.\d+)` is ≥ 2.3.0, yt-dlp's
     minimum.
  2. The managed copy.
  3. None: install.

  Node, Bun and QuickJS are never used, even when they are on PATH.
- **When.** Discovery uses the daemon's PATH. It runs again at daemon start, on a settings change,
  after an install, every 24 h, and when the PATH copy fails (§4.5).

### 4.2 First-use install
- **Trigger.** Started by `Ensure` (any site request) or `InstallTools`, only while `site_media` is on
  and the daemon may download (§3.3).
- **Ownership.** One install at a time (start or join), owned by the manager's context and never by
  a request. Progress is published as `Event.tools`.
- **Steps.**
  1. Find the latest tag of each tool from the redirect of `<base>/<repo>/releases/latest` (one
     request, no GitHub API, so no rate limit). The tag must match `^\d{4}\.\d{2}\.\d{2}(\.\d{6})?$`
     (yt-dlp) or `^v\d+\.\d+\.\d+$` (Deno).
  2. Fetch every file from `<base>/<repo>/releases/download/<tag>/<asset>`, so the files belong to one
     release even if "latest" moves meanwhile.
  3. yt-dlp and Deno install in parallel and independently: a failed Deno leaves yt-dlp installed.
- **Transfer (2026-11-09).** A release file of known size from a server that serves byte ranges
  (`Accept-Ranges: bytes`) and is at least 2 MiB comes in parts of at least 1 MiB, at most 8 at
  once (`internal/tools/fetch.go` `download`). Part 0 reads the first 200 response; every other part
  asks `<asset URL>` again with `Range` (no `If-Range`) over a transport of its own, so parts never
  share one HTTP/2 connection. A ranged answer must be 206 with exactly the part's `Content-Range`
  and the first response's size, else it is refused. A part whose connection breaks or stalls, or
  that gets a 5xx or 429, asks again from its next byte after 1 s, doubling to 15 s, and gives up
  after 5 tries in a row without a byte; one part that gives up ends the download, closing the
  others at once (the first response included). Without ranges or a known size the file is one
  stream that starts over. The joined file is hashed and checked against the release's sums as
  before. Why: GitHub's release CDN can be slow per request (0.2 MB/s measured on 2026-11-09 against
  9 MB/s from another host), and the single stream that started over made a first install take
  minutes and then fail.
- **Repositories.** yt-dlp stable `yt-dlp/yt-dlp`, nightly `yt-dlp/yt-dlp-nightly-builds` (same
  signing key), Deno `denoland/deno`.
- **Assets:**

| GOOS/GOARCH | yt-dlp asset | Deno asset |
|---|---|---|
| windows/amd64 | `yt-dlp.exe` | `deno-x86_64-pc-windows-msvc.zip` |
| windows/arm64 | `yt-dlp_arm64.exe` | `deno-aarch64-pc-windows-msvc.zip` |
| darwin/amd64, darwin/arm64 | `yt-dlp_macos` (universal) | `deno-x86_64-apple-darwin.zip` / `deno-aarch64-apple-darwin.zip` |
| linux/amd64 | `yt-dlp_linux` (glibc) / `yt-dlp_musllinux` | `deno-x86_64-unknown-linux-gnu.zip` (glibc only) |
| linux/arm64 | `yt-dlp_linux_aarch64` / `yt-dlp_musllinux_aarch64` | `deno-aarch64-unknown-linux-gnu.zip` (glibc only) |

  musl is detected by `/lib/ld-musl-*.so.1`. Where no Deno build exists (musl, Windows 32-bit,
  armv7), Deno is DISABLED with "no Deno build for this platform; YouTube offers fewer formats", and
  yt-dlp alone counts as ready.
- **Onedir yt-dlp (2026-10-22).** Where the release's `SHA2-256SUMS` lists one, the managed yt-dlp is
  upstream's unpacked onedir zip instead of the onefile asset above, because a onefile PyInstaller
  executable spends about 1 s unpacking itself on every run:
  - Windows `yt-dlp_win.zip` (amd64), `yt-dlp_win_arm64.zip`, `yt-dlp_win_x86.zip` (386); macOS
    `yt-dlp_macos.zip`; Linux `yt-dlp_linux.zip`/`yt-dlp_musllinux.zip`,
    `yt-dlp_linux_aarch64.zip`/`yt-dlp_musllinux_aarch64.zip`, and `yt-dlp_linux_armv7l.zip`
    (linux/arm; the 2026.08.19 release lists only this zip for it). Decided per release from the
    verified list; without a zip the onefile asset is used.
  - The zip is verified as in §4.3 before anything is unpacked, then unpacked with the safe zip
    extraction (§4.4) into its version folder. Its one root executable (named like the onefile asset)
    is written as `yt-dlp(.exe)` with `_internal/` beside it, so leases and `Exec` are unchanged.
  - An onedir build that fails to unpack or its smoke test falls back to the onefile asset of the same
    verified list (a tampered zip does not: it fails the checksum).
  - An existing onefile install keeps working, leases included, with no download at start or on
    `Ensure`; the next update check replaces it with its own release's onedir build (§4.5).
  - PATH copies are unchanged.
- **Limits.**
  - Sizes: yt-dlp asset ≤ 128 MiB; Deno zip ≤ 256 MiB, unpacked ≤ 512 MiB; `SHA2-256SUMS` and each
    `.sha256sum` ≤ 64 KiB; signature ≤ 16 KiB.
  - Time: 30 s for response headers; failure when less than 1 KiB/s arrives over 60 s; the whole
    install takes at most 15 min.
  - Space: at least 256 MiB free on the tools volume, else `disk_full`.

### 4.3 Verification (what upstream actually publishes; checked 2026-10-21)
- **yt-dlp.**
  - Each release has `SHA2-256SUMS` and a detached OpenPGP signature `SHA2-256SUMS.sig`. The
    signature is a binary v4 packet with RSA and SHA-512, signature class 0x00.
  - The signing key is "Simon Sawicki (yt-dlp signing key)": RSA-4096, fingerprint
    `AC0C BBE6 848D 6A87 3464 AF4E 57CF 6593 3B5A 7581`, no expiry. It is published at
    `github.com/yt-dlp/yt-dlp/blob/master/public.key` and also signs the nightly builds.
  - Research verified the stable 2026.08.19 and nightly 2026.10.10.233141 signatures.
  - Downex embeds the armored key (`internal/tools/ytdlp_key.asc`) and checks its fingerprint at
    start-up. The verifier uses the standard library only (`crypto/rsa`, `crypto/sha512`):
    1. Parse the signature packet: v4, type 0x00, public-key algorithm 1, hash algorithm 10, the
       hashed and unhashed subpackets.
    2. digest = SHA-512(SUMS ‖ the signature's first 6 + hashed-length bytes ‖ 0x04 0xFF ‖
       BE32(6 + hashed length)).
    3. The first two digest bytes must equal the packet's left-16 field.
    4. `rsa.VerifyPKCS1v15(key, crypto.SHA512, digest, sig)`, with the MPI left-padded to 512 bytes.
    5. The issuer-fingerprint subpacket (33) must equal a pinned key.
    6. The signature creation time must be ≥ the one stored for the installed copy (anti-rollback).
  - Then the line `<64 hex>␠␠<asset>` (GNU format) must give the SHA-256 of the downloaded asset.
  - The executable has no Authenticode signature. yt-dlp's own `-U` checks only the SHA-256 from the
    same release, so Downex never uses it.
- **Deno.**
  - Each asset has `<asset>.sha256sum`. Unix assets use GNU format; Windows assets use PowerShell
    `Format-List` output (`Hash : <UPPERHEX>`, CRLF). Both are parsed.
  - Where the release also publishes the hash of the unpacked executable (`deno-<target>.sha256sum`),
    it is checked too.
  - These come from the same place as the zip, so they prove integrity, not authorship.
  - On Windows, `deno.exe` is Authenticode-signed (since 2.3, Azure Artifact Signing). Downex calls
    `WinVerifyTrust` (WINTRUST_ACTION_GENERIC_VERIFY_V2, no UI); the chain must be trusted. The
    signer's subject is recorded and must equal the constant `denoSigner` once that is confirmed on a
    real `deno.exe` (§9). Until then, any trusted signer is accepted and the subject is shown.
  - On macOS and Linux the check is SHA-256 only.
- **`ToolInfo.verified`** reports exactly what was checked: `openpgp+sha256`, `sha256+authenticode`,
  `sha256`, or `""` for a PATH copy.
- **Not used in v1:** GitHub release attestations (Sigstore bundles, which both projects publish); they
  would need `sigstore-go` (§9).
- **Never executed unverified.** A downloaded file is never run before verification. A response that
  starts with `<!DOCTYPE html` or `<html` is refused as a GitHub error page.

### 4.4 Atomic install, leases, cleanup
1. Download to `tmp/<random>.part` and fsync it.
2. Verify it (§4.3).
3. Move it into `<tool>/<version>.new-<random>/`. A Deno zip must hold exactly one regular file at
   its root, named `deno` or `deno.exe`; any other entry, path separator or `..` is refused, and the
   unpacked size is capped. An onedir yt-dlp zip (§4.2) is unpacked entry by entry: `../` and `..\`
   traversal, absolute, drive and UNC paths, `:` streams, Windows device names, trailing dots or
   spaces, control characters, symlinks, duplicates ignoring case and file/folder clashes are refused,
   with at most 20,000 entries and 512 MiB declared or actual, and exactly one root executable;
   `downex-files.json` lists every unpacked file. On Unix, chmod 0755.
4. Rename the folder to `<tool>/<version>/`. If that folder already exists with the same files, it
   is reused. If it holds other files, it is replaced only when no lease holds it (the lease check and
   both renames under the manager's lock): it is set aside as `<version>.old-<random>` and removed
   after the switch, or put back when the new copy fails its smoke test; otherwise the install fails.
5. Smoke-test from the final path: `yt-dlp --version` must print the expected version within 60 s;
   `deno --version` must answer within 30 s.
6. Write `tools.json` atomically, then publish the change.

- **Crash safety.** A crash before step 6 leaves an unused folder, which is removed at the next start.
  A stop between the two renames of a replacement leaves the only copy as `<version>.old-<random>`:
  the next start renames it back when the current or previous version's folder is missing and exactly
  one such folder exists, before the cleanup.
- **Leases.** `Acquire` returns the paths in use and holds their version folders until `Release`.
  A version folder is deleted only when it is neither current nor previous and no lease holds it.
  Leftovers are removed at daemon start.
- **Never touched:** a running executable is never renamed or overwritten; PATH copies and anything
  outside the tools folder are never modified; downloaded tools never get the Mark of the Web or
  other file marks.
- **Lock.** `.lock` (an exclusive file lock) is held during install, switch and cleanup.

### 4.5 Updates and rollback
- **What is updated.** Only managed copies, never PATH copies. While a PATH copy serves, nothing is
  updated or installed.
- **Cadence** (own daemon, with `site_media` and `tools_auto_update` on):
  - first check 5 min after start (±1 min), then every 24 h (±1 h), timed like `update_service.go`
  - after an extraction with the managed yt-dlp fails as `site_error`, an extra check at most once
    per hour

  A check is one redirect request. A newer tag is installed as in §4.4 and switched to.
- **Channel.** Changing the channel installs the latest release of the new channel at once. Only then
  may the version go down.
- **Anti-rollback.** Otherwise a new yt-dlp must have a higher version and a signature creation time
  at least that of the installed one.
- **Rollback.**
  - If the smoke test fails, the new version is discarded and the current one stays.
  - After a switch the previous version is kept.
  - If the first 3 extractions with the new version all fail as `site_error`, the resolver retries
    the last page once with the previous version. If that succeeds, the manager switches back, marks
    the new version `bad` (never installed again; a newer tag is) and deletes it.
- **PATH copy fallback.** If a PATH yt-dlp exits with code 2, or fails as `site_error` where the
  managed copy (installed if needed) then succeeds, that PATH version is marked unsuitable until its
  `--version` changes.
- **Look-aheads (§5.6) take no part.** Their copies come from `AcquireReady` (never an install, never
  a trial's copy), and the outcome of a look-ahead that no user's request joined is not reported: it
  never starts a fallback or rollback trial, never counts toward the rollback's 3 failures and never
  triggers the extra update check.
- **Onedir migration (2026-10-22).** A managed onefile copy (`layout` `""` or `"onefile"`) is
  replaced at the next update check by its own release's onedir build (§4.2), even when no newer
  release exists. While a lease holds its folder the check skips the migration and tries again next
  time. When the release lists no zip, or the onedir build fails to unpack or run, the onefile copy
  stays and is marked `"onefile"`, and that release is not looked at again (a broken build also
  reports its error).
- **Deno.** The managed copy is checked every 30 days and updated within the same major version, or
  at once when it is below 2.3.0.
- **Failed updates** set `ToolInfo.error`/`error_code` and keep the working copy, with no
  notification. The next check tries again.

### 4.6 Offline behaviour and messages
- **Automatic installs** (started by `Ensure`) back off after a failure: 30 s, doubling up to 30 min.
  During that time requests answer `tools_unavailable` at once. `InstallTools` (a user action) ignores
  the backoff.
- **Offline with copies installed:** extraction is attempted and fails as the site's network error
  (`site_error`). Downloads already running keep their URLs until they expire.
- **Texts** (after the code prefix; shown by every UI):
  - `tools_installing`: "downloading yt-dlp and Deno (34%)"
  - `tools_unavailable`:
    - "could not download yt-dlp (network)"
    - "yt-dlp failed its signature check"
    - "deno.exe is not signed by its publisher"
    - "no yt-dlp build for this platform"
    - "this Downex daemon does not download tools"
  - `site_media_off`: "video downloads from web pages are turned off (Settings → Video sites)"

### 4.7 Invoking yt-dlp (`internal/sitejob`, run through `tools.Exec`)
- **Arguments** (exactly this, in this order):
```
<yt-dlp> --ignore-config --no-plugin-dirs --no-update --color no_color --encoding utf-8
         --compat-options no-certifi
         --no-js-runtimes --js-runtimes deno:<absolute deno path>   (only --no-js-runtimes without Deno)
         --cache-dir <tools>/cache/yt-dlp --socket-timeout 30
         [--proxy <url> | --proxy ""]  [--cookies <run>/cookies.txt]  [--referer <url>]
         --no-playlist --flat-playlist --ignore-no-formats-error
         --extractor-args youtube:skip=translated_subs,hls,dash   (youtube:skip=translated_subs: the
                                                                  re-resolve with manifests below)
         -J -- <page URL>
```
- **Why each part:**
  - `--ignore-config` and `--no-plugin-dirs`: no user or system configuration and no plugins.
  - `--no-update`: no update checks or warnings.
  - `--compat-options no-certifi`: yt-dlp trusts the operating system's roots, as Downex does
    (corporate TLS inspection). Lane B confirms the option against the pinned version in a gated test
    and removes it if yt-dlp lacks it.
  - The Deno path is given explicitly, and other runtimes are disabled.
  - `--flat-playlist`: a playlist page answers quickly as `_type: playlist`.
  - `--ignore-no-formats-error`: DRM and no-format pages answer with JSON, so they can be classified.
  - `youtube:skip=translated_subs`: shrinks the output. `hls,dash` (2026-10-22): YouTube's HLS and
    DASH manifests are not fetched, which saved about 0.7 s of a 4.6 s extraction on the user's
    machine; the direct formats come from the player response and are not affected.
  - `--`: ends option parsing before the URL.
- **Manifests on demand.** When a YouTube page (extractor key `Youtube`) resolved this way offers no
  direct MP4 video (`sitemedia.Offers` gives the manifest fallback or a refusal), the same copy
  resolves it once more with `youtube:skip=translated_subs`, so that the manifest fallback (§3.11)
  still works. That second result is the answer, whatever it is, and is cached under the page's key as
  usual; there is never a third run (a failure that counts against the copy still gets §4.5's other
  copy, with the manifests). Pages of other extractors, results yt-dlp or `Parse` refused (live,
  playlist, malformed) and pages with a direct MP4 video run once.
- **Proxy.**
  - Direct → `--proxy ""`.
  - A proxy without credentials → `--proxy http://h:p` or `socks5h://`/`socks4://`/`socks4a://`.
    Downex's SOCKS5 routes send host names to the proxy, so yt-dlp gets them as `socks5h://`
    (with `socks5://` yt-dlp would look the names up itself, outside the proxy).
  - A proxy with credentials → no `--proxy`; the child's environment gets `HTTP_PROXY`, `HTTPS_PROXY`
    and `ALL_PROXY` with the user information. Credentials never go in argv or logs.
  - NTLM/Negotiate proxies cannot be used by yt-dlp; the answer is `site_error` "the proxy needs a
    sign-in yt-dlp cannot do".
- **Environment** (built from an allow-list, not inherited):
  - Windows passes on `SystemRoot`, `SystemDrive`, `windir`, `PATHEXT`, `NUMBER_OF_PROCESSORS`,
    `PROCESSOR_ARCHITECTURE`, `USERPROFILE`, `HOMEDRIVE`, `HOMEPATH`, `APPDATA`, `LOCALAPPDATA`.
  - Unix passes on `HOME`, `LANG`, `LC_ALL`, `LC_CTYPE`, `TZ`.
  - Both pass on `SSL_CERT_FILE`/`SSL_CERT_DIR` when set.
  - macOS (2026-11-09): when neither is set, `SSL_CERT_FILE=/etc/ssl/cert.pem`, the system's root
    list. yt-dlp's Python cannot read the Keychain, so with `no-certifi` it trusted no root and every
    YouTube request failed with "unable to get local issuer certificate". Open: roots that an
    administrator or MDM added to the System keychain (corporate TLS inspection) are not in that
    file; such a user sets `SSL_CERT_FILE` for the daemon.
  - Downex sets `PATH=<deno dir>;<system dirs>`, `TEMP`/`TMP`/`TMPDIR=<run>/tmp`,
    `DENO_DIR=<tools>/cache/deno`, `DENO_NO_UPDATE_CHECK=1`, `NO_COLOR=1`, `PYTHONUTF8=1`.
  - e2etest builds also pass on `DOWNEX_E2E_*` (§8.3).
- **Process:**
  - no shell: `exec.Cmd` with absolute paths; the page URL is a validated argv element
  - stdin empty; working directory `<run>`
  - Windows: `CREATE_NO_WINDOW`, because downexd is a GUI-subsystem program
  - a Windows job object with `KILL_ON_JOB_CLOSE`, at most 32 active processes and a 2 GiB job memory
    limit, so the PyInstaller bootloader, Python and Deno die together
  - Unix: its own process group, killed with `SIGKILL` on timeout
- **Bounds.**
  - 90 s per extraction (the whole tree is killed); at most 2 extractions at once, others wait within
    their caller's context. A look-ahead (§5.6) starts only while no other extraction runs or waits
    and takes none of the 2 turns, so users' extractions that follow start beside it.
  - stdout ≤ 32 MiB (more: killed, `media_invalid`); the last 64 KiB of stderr are kept.
  - `--version` checks take 10 s (PATH) or 60 s (smoke test).
- **Cookie file.** Netscape format, 0600, in `<run>/` (0700). One line per cookie of the Cookie
  header: domain = the page host (not including subdomains), path `/`, secure when https, session
  expiry. yt-dlp rewrites it on exit, and the whole `<run>/` folder is deleted after the run, also
  after a kill. Leftovers are deleted at daemon start.
- **Exit codes.** 0 with JSON → `Parse`. 1 → classified (§3.10). 2 → `site_error`, reported.
  Anything else, or a timeout → `site_error`.

### 4.8 Licences
- The Settings page "Video sites" (§5.4) and README show:
  - "yt-dlp (Unlicense; the Windows/macOS/Linux executables are GPLv3+) is downloaded from
    github.com/yt-dlp; Deno (MIT) from github.com/denoland/deno"
  - links to both licence files
- Nothing is bundled.

## 5. UI

### 5.1 YouTube overlay button (extension)
The same button now also sits over the videos of every other site (VIDEO-DETECT-SPEC.md §5.1, a
generic tracker that never runs on YouTube's hosts), and the YouTube overlay follows the per-site
`hiddenSites` option (§5.4 there). `SiteVideo` gains `lookAhead` (true for `watch` and `shorts`).
- **Detection.** `ext/src/lib/site-video.ts` (pure) parses:
  - `www./m./music.youtube.com/watch?v=<id>`
  - `/shorts/<id>`, `/embed/<id>`, `/live/<id>`, `/v/<id>`
  - `youtube-nocookie.com/embed/<id>`
  - `youtu.be/<id>`
  - `/clip/<clip id>`

  An `<id>` matches `^[A-Za-z0-9_-]{11}$`. The result is `{site: 'youtube', id, kind, key:
  'youtube:<id>', pageUrl}`. `pageUrl` is canonical: `https://www.youtube.com/watch?v=<id>`, with
  `list`, `t`, `si` and `pp` dropped. A clip keeps its own URL without the fragment.
- **Content script.** `entrypoints/youtube.content.ts`, apart from the click reporter:
  - matches `https://www.youtube.com/*`, `https://m.youtube.com/*`, `https://music.youtube.com/*`,
    `https://www.youtube-nocookie.com/embed/*`; excludes `*://www.youtube.com/live_chat*`
  - `allFrames`, `document_idle`, isolated world
  - no new permissions (`wxt.config.ts` already has them)
- **SPA navigation.**
  - Navigation is followed through `yt-navigate-finish` on `document` (no event detail is read),
    WXT's `wxt:locationchange`, `popstate`/`pageshow`, and a capture-phase `loadstart` on `document`
    (a new video loaded into the player).
  - Each one re-parses `location.href`, re-mounts, and closes an open panel when the video key
    changed.
  - The button is hidden when the URL is not a video page (the miniplayer included). During ads the
    URL still names the right video.
- **Mounting.**
  - WXT `createShadowRootUi`: `mode: 'closed'`, `position: 'inline'`, CSS inlined
    (`cssInjectionMode: 'manual'`), so no web-accessible resource reveals the extension.
  - `autoMount` uses an anchor function: the `.html5-video-player` that contains the largest visible
    `<video>`, else that video's `offsetParent`. A player that is created again is followed.
  - The host is styled `position:absolute; inset:0; pointer-events:none; z-index:2147483000`; the
    button and panel get `pointer-events:auto`.
  - Inside the player, the button stays visible in fullscreen and moves with theater mode.
- **Button.**
  - A real `<button aria-label="Download this video with Downex" aria-haspopup="dialog"
    aria-expanded>` with Downex's mark and the text "Download", on a dark translucent pill.
  - Top-right, inset 12 px; in embeds 56 px from the top, to clear the player's own controls.
  - Shown for 3 s after pointer activity in the player, for 5 s after `loadstart`, on
    `:focus-visible` and while the panel is open.
  - Hidden with opacity, not `display:none`, so it stays focusable.
  - Respects `prefers-reduced-motion` and `forced-colors`.
  - Activation requires `event.isTrusted`.
- **Events.** `isolateEvents` covers `keydown keyup keypress click dblclick mousedown mouseup
  pointerdown pointerup contextmenu wheel touchstart touchend`, so the player's shortcuts, play toggle
  and double-click fullscreen never fire. `mousemove` passes, so the player's controls still hide on
  their own.
- **Panel.**
  - `popover="auto"`, `role="dialog"`, `aria-labelledby` the video title. It is positioned from the
    button's rectangle in script, without CSS anchor positioning (Firefox 140).
  - It closes on Esc (focus returns to the button), on light dismiss, on `fullscreenchange`, on
    navigation to another video and when the extension context is invalidated.
  - Content:
    - the title
    - a list of buttons, one per offered quality class, best first, each labelled with the track's
      `name` from the daemon, its codec as the small text and the size, such as "4K (2160p) · AV1 ·
      860 MB", "1080p (Full HD) 60fps · H.264 · 412 MB", "360p · H.264 + AAC · 16.0 MB" for a
      muxed one (video with its default audio; `~` for estimates; "MP4" as the small text only
      when the codec is unknown, since every quality is saved as one MP4; the picture's width x
      height, "3840×1634", as the row's tooltip, never its name; amendment 2026-10-23)
    - an audio select only when several MP4 audio tracks exist (languages, DRC)
    - an `aria-live` status line
  - Up and Down move between rows.
  - All site strings are set with `textContent`, with bidi controls (U+202A–202E, U+2066–2069)
    removed.
- **States:**
  - "Getting video info…" with elapsed seconds
  - "Preparing video downloads: downloading yt-dlp and Deno (34%)" (`tools_installing`, retried every
    2 s while the panel is open, for up to 5 min)
  - the list
  - "Sending…"
  - then "Confirm in Downex" (`awaiting`) or "Downloading in Downex"; the panel closes after 3 s
  - the error texts of §5.5
- **Options.** "Show a Download button over YouTube videos" (`videoButton`, default on).

### 5.2 Popup and context menu
- **Popup rows.** "Media on this page" lists page videos before the HLS/DASH streams:
  - the top frame from `tabs.get(tabId)`
  - embeds from `webNavigation.getAllFrames`, kept per tab by `onCommitted` for frames other than 0
- **Badge.** It counts streams plus page videos; `!` still wins.
- **Hidden rows.** On a tab with a page video, `*.googlevideo.com` manifest rows are hidden: they are
  live or SABR streams that would be refused.
- **Row.**
  - title, site and Download
  - "Choose quality" expands an inline picker that mirrors the desktop `MediaPicker`: a video select
    listing the offered quality classes by their names ("4K (2160p) · AV1 · 860 MB"; the picture
    as each option's `title`), an audio select only when needed, the size, Download
  - Download without choosing sends the default pick
  - its states follow §5.1
- **"Find the video on this page".** Shown for any other http(s) tab. It is user-initiated, sends the
  tab URL with `probeVideo`, never sends cookies, and is never automatic.
- **Probe cache** (`ext/src/lib/video-handoff.ts`):
  - in the background, shared by overlay, popup and tabs
  - key `${storeId}\n${siteKey}`
  - one probe per key, at most 2 at once
  - results and refusals kept 10 min (LRU of 50); other errors not kept
  - deadlines 28 s
  - shared with look-aheads (§5.6), which take neither probe slot; a click while the look-ahead of its
    video runs joins it (no second request) and asks Downex itself when the look-ahead's answer is
    not kept. A look-ahead's `site_unavailable` and `auth_required` are never kept (YouTube answers
    throttled look-aheads with them)
- **History.** It records `addVideo` results as source `video`.
- **Context menu.** "Download with Downex" on a link that `parseSiteVideo` recognises sends `addVideo`
  with the default pick (not `add`, which would save the HTML page).
- **Old relays.** With an old relay (no `features`), the button shows "Update Downex to download
  videos" and sends nothing.

### 5.3 Quality picker rules (shared)
- **Desktop picker** (`ui/frontend/src/lib/media.ts`):
  - It starts from the daemon's `selected_video`/`selected_audio`.
  - The video select lists the video and muxed tracks best first, one per quality class
    (amendment 2026-10-23), by the daemon's track `name`: "4K (2160p) · AV1 · 860 MB" for a web
    page, "1080p (Full HD) · H.264 · 5.0 Mbit/s" for a stream. Width x height ("1920×1080 ·
    29.97 fps" for a stream) is secondary detail: the option's tooltip, the picker's summary and
    the Details "Video" fact, never the name. The hint reads "N MP4 qualities, one per
    resolution, best first." A track whose name is not a standard one (an older daemon's raw
    height, a manifest's "1920x1080", none) is named by the frontend's one helper
    (`lib/quality.ts`, `trackQuality`, the amendment's tables), and an older daemon's longer list
    is reduced to one per class by the same order as the daemon's, its pick kept.
  - The audio select appears only for a video-only pick with more than one audio track.
  - Sizes: the sum of `size` when every chosen track has one (`~` if any is an estimate), else M5's
    bandwidth × duration estimate.
- **Extension picker.** `ext/src/lib/video-picker.ts` mirrors these rules in the extension's own
  package; nothing is imported across packages.

### 5.4 Desktop
- **Confirm dialog.**
  - Unchanged for M5. For SITE: an "Address" row labelled "Page", the title, the site name ("YouTube
    video"), and the picker listing the offered quality classes by name.
  - Confirm sends the chosen `videoTrack`/`audioTrack`; the file name keeps its `.mp4` extension.
- **Add dialog.**
  - Stream mode becomes "Video or stream": `looksLikeManifest(url)` selects MANIFEST, any other URL
    SITE.
  - "Find qualities" probes and retries on `tools_installing`/`site_resolving`, showing tools
    progress from `Event.tools` or "Getting video info… N s"; Cancel stops the retries.
- **Progress window and details.**
  - PROBING with SITE → "Getting video info…". Media fragments read "Part N" for format `http`.
  - "Merging…" while assembling (existing `isAssembling`).
  - The size and percentage come from `BytesTotal`: the bytes against `Download.size`, never the
    parts (format `http`; its chunks are uneven), indeterminate while the size is unknown
    (amendment 2026-10-29, `lib/format.ts`). A site's HLS or DASH stream counts its fragments like
    any stream. "Manifest" labels read "Page" for SITE (`DetailsPanel.svelte`, `ProgressWindow.svelte`).
  - The completion view shows "YouTube video · 1080p (Full HD) 60fps H.264 + AAC · MP4".
- **Settings → "Video sites"** (`SettingsVideo.svelte`, page id `video`):
  - switches "Download videos from web pages with yt-dlp" (`site_media`) and "Update automatically"
    (`tools_auto_update`)
  - a channel select (Stable, Nightly (recommended by yt-dlp))
  - a row per tool: version, "Downex copy" or "Found on PATH: <path>", how it was verified, last
    check, error
  - Install now / Check for updates (`InstallTools`, 15 min), with a progress bar fed by
    `Event.tools` (a `ToolsStore`)
  - the licence and terms lines of §1 and §4.8

  The Network page's host-rule editor gains "Max request (MiB)".
- **First-use toast.** The main window shows a progress toast while `Event.tools` reports
  INSTALLING, and on failure a toast with "Open settings". There is no modal first-use dialog.

### 5.5 Error texts (all UIs; the message after the code is appended where useful)
- `site_unsupported`: "Downex found no video on this page."
- `site_unavailable`: "This video is not available (private, removed, blocked in your country, or the
  site asks to try again later)."
- `auth_required`: "The site wants you to sign in (age check, members-only or bot check)." The
  extension adds "Allow Downex to use your browser sign-in for videos" (§6.3) and retries after the
  user agrees.
- `live`: "This is a live stream. Downex downloads only finished videos."
- `encrypted`: "This video is DRM-protected. Downex does not download protected content."
- `media_unsupported`: "No MP4 video on this page." For a playlist: "Open a single video
  of this playlist."
- `media_invalid`: "The chosen quality is no longer offered. Add the video again."
- `site_error`: "yt-dlp could not read this page: <reason>."
- `tools_unavailable`: "Downex could not set up yt-dlp/Deno: <reason>." with an "Open settings"
  action.
- `site_media_off`: the text of §4.6.
- An old daemon or relay: "Update Downex to download videos."

### 5.6 Look-ahead (amendment 2026-10-22)
*Amended 2026-10-23:* the look-ahead also runs on the curated video sites marked for it, with a
meaningful video required in the tab, the hidden-sites rule and per-site throttles
(VIDEO-DETECT-SPEC.md §3.8, §5.5). The rules below stay YouTube's.

The user found the 4–5 s between clicking Download and seeing the qualities annoying. Measured on the
user's machine: Downex itself adds about nothing; one `yt-dlp -J` takes 4.0–4.6 s, of which about 1 s
is the start of the onefile PyInstaller `yt-dlp.exe` (§4.2 onedir) and about 0.7 s YouTube's HLS/DASH
manifests (§4.7 skip); yt-dlp's cache barely matters; a second probe of the same video is instant (the
extraction cache of §2.6). So the qualities of the video being watched are fetched before the click:
- **When (extension).**
  - Only the top-frame watch or Shorts video of a **visible** tab, after the user has stayed on that
    video for **1500 ms**. SPA navigation (§5.1) restarts the wait, so swiping through Shorts sends
    nothing unless the user stays 1.5 s on one clip.
  - At most **one look-ahead in flight in the whole browser**; the newest video wins: an older one not
    yet sent is dropped.
  - Never when the option `videoPrefetch` ("Prepare video qualities while you watch", default on) is
    off, the Download button (`videoButton`) is off, or "Send my sign-in cookies" (`useSignInForVideos`)
    is on: a look-ahead never carries the user's cookies (§6.3), and one without them would not be the
    click's request. Never when the relay is not connected or does not list `prefetch`, or the daemon
    is not known to run (a `videoInfo` or `rejected` answer also says it runs); never after the daemon
    answered a look-ahead with `tools_installing`, `tools_unavailable` or `site_media_off` until the
    user clicks once; and never for 30 min after a look-ahead was answered `site_unavailable` or
    `auth_required` with YouTube's rate limit or bot check (HTTP 429, "rate-limit", "too many
    requests", "try again later", "not a bot"), whatever the user does.
  - **Budget.** At most 10 look-aheads at once, then one more per minute (at most about 60 an hour
    after the first 10); one that sent nothing (kept answer, probe running) costs nothing. Without it,
    Shorts watched a few seconds each would cause one extraction per clip.
  - The request is the click's `probeVideo` (the same page URL and Referer, never a Cookie, so the
    same cache entries) with `prefetch: true`.
  - Its result goes into the same probe cache (§5.2) as a click's, so the overlay, the popup and Add
    use it, except `site_unavailable` and `auth_required`, which a click asks Downex about again. A
    click while the look-ahead for that video is in flight joins it: no second yt-dlp run anywhere.
  - Details of the dwell, the checks of the content script's message and the scheduler: BROWSER-SPEC
    §4.
- **Relay.** `probeVideo` accepts an optional boolean `prefetch`, forwarded as `ProbeMedia.prefetch`
  (§3.5). A look-ahead never starts downexd (`daemon_unavailable` while none runs).
- **Daemon** (`ProbeMedia{origin: SITE, prefetch: true}`, §3.3):
  - It never installs or updates tools: it never calls `Tools.Ensure` and answers `tools_unavailable`
    at once while yt-dlp and Deno are known not to be ready (missing or being installed). While the
    copies have not been looked for yet since the daemon started, it waits for that discovery (which
    never downloads) within its budget, then answers as above. Only a user's request sets them up
    (§6.6, §9 #11).
  - It never waits behind or delays a user's request. The resolver answers it from its cache or joins
    the running extraction of the same page (the same cache key; `prefetch` is not part of it).
    Otherwise it starts an extraction only while no other extraction runs or waits, in a turn of its
    own outside the 2 of §4.7, so at most one look-ahead runs and users' extractions never wait for
    it; else it answers `site_resolving` at once (`sitejob.ErrBusy`) instead of queueing.
  - Unless a user's request joined it, its extraction is passive towards the tools manager: it takes
    only ready copies (`AcquireReady`: never an install, never a trial's alternative copy), its
    outcome is not reported (a report may start a fallback or rollback trial or an extra update
    check, §4.5), it never retries with another copy, and a failure that may be the copy's
    (`site_error`, bad options) is not cached, so the user's request that follows resolves the page
    itself with the fallback of a user's extraction. A user's request that joins it makes it a user's
    extraction from then on (its outcome is reported, its failure gets the other copy).
  - Otherwise it is a probe: the site-media check, the wait budget, the codes, the manifest re-resolve
    of §4.7, and its result (offer or refusal) fills the same extraction cache that the probe, Add and
    run that follow use.
- **Load.** At most one extraction per watched video every 15 min (the cache), one at a time, at
  most about 60 an hour (the budget), and none for 30 min after YouTube throttled one (§6.6).

## 6. Security
1. **Untrusted output.**
   - Everything yt-dlp prints, and everything from the page, is untrusted data, parsed under §3.10's
     bounds and fuzzed (`FuzzParse`).
   - Only the page URL is stored. Format URLs, headers and cookies live in memory for the run only.
   - Logs keep scheme, host and path of URLs. Errors never contain URLs.
   - Format and manifest URLs must be http(s) with a host. A host that is `localhost` or a loopback,
     link-local or unspecified IP literal is refused, unless the page URL is on that same host (the
     tests' local servers).
2. **Header allow-list.**
   - From `http_headers` only `User-Agent`, `Accept`, `Accept-Language`, `Referer`, `Origin`,
     `Sec-Fetch-Mode`, `Sec-Fetch-Dest`, `Sec-Fetch-Site` and at most 8 `X-[A-Za-z0-9-]{1,64}` headers
     are kept. Each value is printable and at most 4 KiB; the whole set passes `validate.Headers`.
   - Everything else is dropped: `Cookie`, `Authorization`, `Proxy-*`, `Host`, `Range`, `If-*`,
     `Content-*`, `Accept-Encoding`, hop-by-hop and framing headers.
   - The `Cookie` header is built only from the format's `cookies` field: cookies whose Domain matches
     the format URL's host (RFC 6265), whose Path matches, Secure only over https, not expired; at most
     50 cookies and 8 KiB. It goes only to that URL's origin.
3. **Cookies need consent.**
   - yt-dlp runs without the user's cookies by default.
   - The extension option "Send my sign-in cookies when a video needs it" (`useSignInForVideos`,
     default off; also offered after an `auth_required` refusal) makes probe/add carry the `Cookie`
     of the canonical page URL: `cookies.getAll` for that URL only, in its cookie store.
   - The option warns that the site may restrict accounts used this way.
   - Cookies go only to the local relay and then to a 0600 file deleted after the run. They are never
     logged and never stored.
   - The CLI passes cookies only through an explicit `-H "Cookie: …"`.
   - A look-ahead (§5.6) never carries cookies; none is sent while the option is on.
4. **Executables.**
   - Only verified managed copies (§4.3) or the user's own PATH copies run. Never a file from the
     download folder, the browser or a page.
   - Downex never installs Python, pip packages, Node or anything else. It never edits PATH and never
     writes outside its tools folder.
   - Children run with the allow-listed environment, no shell, a job object or process group, time
     and output limits, and no console window.
   - Deno runs under yt-dlp's own sandbox flags (no `--allow-*`).
5. **Paths.**
   - Output names come from `transport.SanitizeFilename`.
   - Tool paths are built only from validated version strings and fixed asset names. Zip entries are
     checked (§4.4).
   - The tools folder can be changed only by downexd's command line, never over RPC.
   - Cookie and work files live in private per-run folders, never next to downloads.
6. **Entry points.**
   - Tool installs start only from a user action: a probe or add the user clicked, or
     `InstallTools`. A look-ahead (§5.6) never starts one.
   - The tools RPCs exist only on the user-only endpoint (DAEMON-SPEC §2).
   - The extension never probes automatically, except the look-ahead of §5.6: the one video the user
     has watched for 1.5 s in a visible tab, one at a time, at most 10 at once and then one a minute
     (about 60 an hour), never with the user's cookies, paused 30 min after YouTube's rate limit or
     bot check answered one, off with its option or the Download button. Upstream gives YouTube's
     limit as about 300 videos per hour without an account; the budget keeps look-aheads well below
     it, leaving room for the user's own downloads.
   - A look-ahead never starts downexd (the relay answers `daemon_unavailable`).

## 7. Lane plan
Lanes run in parallel after Step 0. **Each file has exactly one owner.** Files that Step 0 creates or
edits pass to the lane named next to them. A lane that needs a file it does not own asks the
integration step.
- **Merge order:** Step 0 → Lanes A, C, D, E, F (any order) → Lane B (it imports `internal/tools`
  and `mediajob.RunRanged`) → Integration.
- **No disturbance:** no lane builds into `bin\`, runs the user's own daemon, writes the real tools
  folder, or commits.

**Step 0 — contract freeze** (one agent, before the lanes; `go build ./... && go test ./...` green):
- Policy: `docs/CLEAN-ROOM.md`, `docs/REQUIREMENTS.md` (M3 wording and a "Site media" milestone row),
  `docs/MEDIA-SPEC.md` (preamble sentence only).
- Proto: `proto/downex/v1/downex.proto` (§3.1), regenerated into `internal/rpc/downex/v1/downex.pb.go`
  and `internal/rpc/downex/v1/downexv1connect/downex.connect.go`.
- `internal/daemon/tools_rpc.go` (new; `GetTools`/`InstallTools` answer Unimplemented) → Lane D.
- `internal/model/model.go`: `Media.Origin`, `Media.Site`; `MediaTrack.Size`,
  `SizeApprox`, `Ext`; `MediaTrackCheckpoint.ChunkBytes`. Frozen afterwards.
- `internal/media/tracks.go` (`Track.Size`, `SizeApprox`, `Ext`), `internal/media/media.go`
  (`Plan.SingleResource`), `internal/media/ranged.go` + `ranged_test.go` (new; `RangedTrack`,
  `PlanRanged`). Frozen afterwards.
- Mirror fields only: `internal/mediajob/mediajob.go` (`Progress.BytesTotal`,
  `TrackCheckpoint.ChunkBytes`) → Lane C; `internal/daemon/media.go` (`MediaProgress.BytesTotal`) →
  Lane D.
- `internal/sitemedia/errors.go` + `errors_test.go` (new; origin constants and the sentinels of §3.2).
  Frozen afterwards.

**Lane A — tools manager**
- Files:
  - `internal/tools/` (new): `doc.go`, `paths.go`, `assets.go`, `release.go`, `fetch.go`, `pgp.go`,
    `ytdlp_key.asc`, `sums.go`, `zip.go`, `install.go`, `state.go`, `discover.go`, `manager.go`,
    `update.go`, `exec.go`, `exec_windows.go`, `exec_unix.go`, `env.go`, `errors.go`, their
    `*_test.go`, and `testdata/` (the real 2026.08.19 `SHA2-256SUMS`, `SHA2-256SUMS.sig`, `public.key`;
    sample Deno `.sha256sum` files in both formats).
  - `internal/platform/toolsdir.go` + `toolsdir_test.go` (new).
  - `internal/update/authenticode_windows.go`, `authenticode_other.go`,
    `authenticode_windows_test.go`: export `VerifyAuthenticode(path) (Signer, error)`, keeping the
    current caller's behaviour.
- Deliverables: §4 in full. `Manager` implements the method set of `daemon.Tools` plus
  `Acquire`/`Report`/`Close`; `Exec` and the environment allow-list.
- Tests:
  - OpenPGP: the real signature verifies; tampered SUMS, signature, wrong key or old creation time
    are refused; a signing test key generated in the test.
  - Both `.sha256sum` formats, the asset map for every GOOS/GOARCH, version parsing and ordering.
  - Zip: traversal, extra entries and size bombs are refused.
  - Install against an `httptest` TLS release server: progress, crash leftovers cleaned, leases keep
    folders, anti-rollback, bad-version rollback, backoff, channel switch.
  - PATH discovery with the test binary as a stand-in tool.
  - `Exec`: timeout kills a child tree (stand-in that spawns a grandchild), output caps, environment
    allow-list, no window flag.
  - No network.

**Lane B — site resolver and adapter**
- Files:
  - `internal/sitemedia/`: `info.go`, `parse.go`, `offers.go`, `pick.go`, `key.go`, `label.go`, their
    tests, `fuzz_test.go`, `testdata/*.json` (hand-written yt-dlp outputs, §3.10).
  - `internal/sitejob/` (new): `resolver.go`, `cache.go`, `args.go`, `env.go`, `cookies.go`,
    `classify.go` + tests, `testdata/stderr/*.txt`.
  - `cmd/downexd/site.go` + `site_test.go` (new; `daemon.SiteMedia`):
    - Probe: format `http` offers, or the M5 fallback through `mediajob.Probe`
    - Run: Key matching, chunk size from `opts.HostRules` (§2.4), `transport.WithNetwork`,
      `NotBefore`, `Refresh`, `RunRanged` or `mediajob.Run`
- Deliverables: §2.4–§2.6 on the resolver side, §3.10, §3.11, §4.7.
- Tests:
  - Parse fixtures: YouTube-like (avc1/av01/vp9/opus, drc, multi-language, HDR), muxed-only, audio-
    only (→ `media_unsupported`), HLS-only, DRM, live, playlist, malformed, oversized.
  - Fuzz.
  - Every distinct MP4 quality offered and no VP9/Opus/WebM format listed; default pick; keys stable
    across URL changes and different on size changes.
  - The exact argv for direct, HTTP-proxy, credentialed-proxy and cookie runs; cookie file content and
    mode.
  - The classification table; cache TTL, expiry and joining; a concurrency cap of 2.
  - A timeout kills the stand-in.
  - The adapter with a fake resolver and `internal/testserver`.
  - No network.

**Lane C — media engine**
- Files:
  - `internal/mediajob/`: `mediajob.go`, `run.go`, `fetch.go`, `output.go`, `ranged.go` (new),
    `hostcap.go` (new), `ranged_test.go` (new), and existing tests as needed.
  - `internal/engine/`: `hostrules.go`, `worker.go`, `probe.go`, `coordinator.go`,
    `requestcap_test.go` (new).
  - `internal/transport/`: `factory.go`, `network.go` (new), `network_test.go` (new).
- Deliverables:
  - `RunRanged`: single-file layout, the durable-bit rule, the init check, remux and copy, 200
    handling, `Refresh`, `NotBefore`, `BytesTotal`, host caps and limiter, 429 halving.
  - `Options.HostRules` for `Run` too.
  - `engine.RequestCaps` in worker and probe.
  - `transport.WithNetwork`.
- Tests (`internal/testserver`, a fake clock, remux fixtures built with mp4ff):
  - single-file fMP4 (`ftyp`+`moov/mvex`+`sidx`+`moof/mdat`) with chunk boundaries inside `moof`,
    video + audio → progressive MP4
  - a progressive (non-fragmented) MP4 refused after chunk 0
  - `pssh` → `encrypted`
  - copy mode
  - a 200-only server, 200 to chunk k
  - 403 → refresh → success; 403 twice → failure keeping progress; proactive refresh before expiry
  - kill/resume refetching only chunks without bits; a size or chunk-size change restarts that track
    only
  - per-host connection cap and request cap (every Range ≤ cap)
  - an engine download under a cap
  - `WithNetwork` dialing only tcp4

**Lane D — daemon and CLI**
- Files:
  - `internal/daemon/`: `site.go` (new), `tools_service.go` (new), `tools_rpc.go`,
    `settings_media.go` (new), `media.go`, `confirm.go`, `errors.go`, `convert.go`, `manager.go`,
    `settings.go`, `settings_network.go`, `hub.go`, `watch.go`.
  - Daemon tests: `site_fake_test.go`, `site_test.go`, `tools_service_test.go`,
    `settings_media_test.go` (new); `settings_network_test.go`, `watch_test.go`, `media_test.go`
    (extended).
  - `cmd/downex/`: `app.go`, `media.go`, `confirm.go`, `remote.go`, `tools.go` (new), `media_test.go`,
    `tools_test.go` (new), `fake_test.go`, `fake_m6_test.go`.
- Deliverables:
  - §3.2 and §3.3 in full; the `media` settings area; `max_request_bytes` and the built-in rule in
    `NetworkRuntime` (`HostMaxRequestBytes`); `Event.tools`.
  - The tools timer, modelled on `update_service.go`; `GetTools`/`InstallTools`.
  - The CLI of §3.7.
- Tests (fakes only):
  - SITE add → awaiting → Confirm with another MP4 quality (tracks, keys and size change; the
    container and the `.mp4` extension stay)
  - the wait budget gives `tools_installing`/`site_resolving` before the caller's deadline
  - every error code and its metadata
  - runMedia stays PROBING until the first progress; `BytesTotal` → size
  - settings defaults for old files, repair and refusal
  - built-in versus user host rules; Watch tools events (first batch, ≤ 4/s)
  - CLI retry loop, `--list` table, `tools` views

**Lane E — desktop UI**
- Files:
  - `ui/`: `daemon.go`, `daemon_site.go` (new), `app_site.go` (new: bindings `GetTools`,
    `InstallTools`), `app_test.go` (binding list), `fakes_test.go`, `fakes_m6_test.go`,
    `media_test.go`.
  - `ui/frontend/src/`: `App.svelte`, `ProgressWindow.svelte`, `lib/api.ts`, `lib/wails.ts`,
    `lib/mock.ts`, `lib/media.ts`, `lib/progress.ts`, `lib/validate.ts`, `lib/stores/tools.svelte.ts`
    (new), and in `lib/components/`: `AddDialog.svelte`, `ConfirmDialog.svelte`,
    `MediaPicker.svelte`, `DetailsPanel.svelte`, `SettingsDialog.svelte`, `SettingsNetwork.svelte`,
    `SettingsVideo.svelte` (new).
  - Frontend tests next to those files.
- Deliverables: §5.3 and §5.4 on the desktop, and the §5.5 texts in `media.ts`.
- Tests: `TestBindingsAreExactlyTheContract`, the Go fakes, vitest for
  - wire parsing of the new fields and `tools` events
  - the picker listing every MP4 quality with exact sizes
  - the Add dialog retry and cancel
  - the confirm quality switch (the `.mp4` name kept)
  - the settings page and host-rule field validation
  - progress texts

**Lane F — browser (relay and extension)**
- Files:
  - `cmd/downex-nmhost/`: `protocol.go`, `relay.go`, `protocol_test.go`, `relay_test.go`,
    `video_test.go` (new).
  - `ext/src/lib/`: `site-video.ts`, `video-picker.ts`, `video-handoff.ts`, `video-panel.ts`,
    `video-overlay.ts` (new, each with a `.test.ts`), `protocol.ts`, `messages.ts`, `runtime.ts`,
    `history.ts`, `ui-format.ts`, `options-form.ts`, `media.ts` (badge input only),
    `background-video.test.ts` (new), and the existing tests of the changed modules (including
    `manifest.test.ts`).
  - `ext/src/entrypoints/`: `youtube.content.ts` (new), `background.ts`, `popup/index.html`,
    `popup/main.ts`, `popup/style.css`, `options/index.html`, `options/main.ts`.
  - `ext/package.json` and `ext/pnpm-lock.yaml`, only to add `happy-dom` as a devDependency.
- Deliverables: §3.5, §3.6, §5.1, §5.2, §6.3 on the browser side.
- Tests:
  - Relay: parsing and limits, the 30 s cap and margins, a separate semaphore under load (capture adds
    still answered), `reason` forwarding, `features`, `awaiting`.
  - Extension:
    - URL patterns and canonicalisation
    - picker parity with the desktop rules
    - protocol requests, deadline adjustment, malformed `videoInfo` refused
    - sender validation, dedupe, concurrency cap, retry on `tools_installing`
    - context-menu routing
    - the manifest's new content script (matches, `allFrames`, no MAIN world, no new permissions)
    - overlay mount, Esc and focus return, isolated events (happy-dom)

**Integration step** (one agent, after all lanes):
- `cmd/downexd/`:
  - `main.go`: `--tools-dir`; `tools.New`, `sitejob.NewResolver` and the `site.go` adapter;
    `Config.Site`/`Tools`; the tools service only for the own daemon or the hook.
  - `tools.go` (new): an https client through the network proxy, like `update.go`.
  - `media.go`: pass `HostRules`.
  - `testhooks.go`, `testhooks_e2e.go`, `testhooks_release.go`: `DOWNEX_E2E_TOOLS_BASE`,
    `DOWNEX_E2E_YTDLP_KEY`, and `DOWNEX_E2E_*` passed on to tools.
- `internal/e2e/site_test.go` (new) and `internal/e2e/harness_test.go` (stand-in yt-dlp/Deno modes).
- `internal/store/conformance_test.go`: `fullMedia` sets the Step 0 model fields (`Origin`, `Site`,
  each track's `Size`/`SizeApprox`/`Ext`, a checkpoint's `ChunkBytes`), which its every-field
  round-trip test requires.
- `installer/downex.nsi`: the uninstaller's "delete user data" also removes
  `%LOCALAPPDATA%\Downex\tools`.
- Docs: DAEMON-SPEC §3/§6, BROWSER-SPEC §2/§4, DESKTOP-SPEC §1/§7, M6-SPEC §2 (`max_request_bytes`,
  host rules now apply to media), MEDIA-SPEC §7 (cross-reference), REQUIREMENTS status, the repository
  layout notes, README, and §10 of this spec.
- Gates:
  - `go build ./...`, `go vet ./...`, `CGO_ENABLED=1 go test -race ./...`
  - `GOOS=darwin`/`linux` builds and `go test -c` of the touched packages
  - `pnpm check && pnpm test` in `ext/` and `ui/frontend/`
  - `go vet -tags desktop,production ./ui`
  - `scripts/build-windows.ps1 -Out <temp dir>` (never `bin\`)

## 8. Test plan

### 8.1 Unit and package tests
Listed per lane in §7. Common rules:
- No test reaches the network.
- Servers are `internal/testserver` or `httptest` TLS servers trusted through injected roots.
- Tools are stand-ins: the test binary copied as `yt-dlp(.exe)`/`deno(.exe)`, its behaviour chosen
  by environment.
- Timing uses fake clocks.
- Every untrusted parser (`sitemedia.Parse`, `.sha256sum`/SUMS parsing, the OpenPGP packet reader,
  the relay's `videoInfo` in the extension) has a fuzz target or table of malformed inputs.

### 8.2 Real-process end-to-end (`internal/e2e/site_test.go`, skipped with `-short`)
Built downexd (e2etest tags), downex and downex-nmhost; a temporary `--db` and `--tools-dir`; a
gated, hit-counting local server that serves generated single-file fMP4 video (about 5 MiB) and
audio (about 1 MiB). The stand-in yt-dlp prints JSON whose page and format URLs point at that server.
1. **First-use install.** The release server (TLS, `DOWNEX_E2E_TOOLS_BASE`, `DOWNEX_E2E_ROOT_CA`)
   serves SUMS signed with a test key (`DOWNEX_E2E_YTDLP_KEY`) and the stand-in as the asset.
   - `downex media --site <page> --list` first sees `tools_installing`, then lists the tracks and
     default.
   - `downex tools --json` shows `openpgp+sha256`.
   - A tampered SUMS → `tools_unavailable` with code `signature`.
2. **PATH first.** With the stand-in on the daemon's PATH, the release server gets no hits.
3. **Download.**
   - A user host rule for `127.0.0.1` sets `max_request_bytes` 1 MiB and `max_connections` 2.
   - The server checks that every Range is at most 1 MiB and that at most 2 requests run at once.
   - The result is one progressive MP4 with both tracks (checked with mp4ff) and the expected
     durations.
4. **Crash and resume.** Kill downexd mid-download and restart it: only chunks without bits are
   fetched again (hit counts), and the output is byte-identical to an uninterrupted run.
5. **Expiry.**
   - The server answers 403 after N chunks.
   - The stand-in's second run returns new URL paths.
   - The download completes, with exactly one extra extraction (argv log).
6. **Refusals.** JSON with `_has_drm` and no formats → `encrypted`; `live_status: is_live` → `live`;
   `_type: playlist` → `media_unsupported`; stderr "Sign in to confirm your age" → `auth_required`;
   a `pssh` in the served init → `encrypted` before any other chunk.
7. **Browser hand-off.** Piped stdio to downex-nmhost:
   - `hello` → `features` includes `video`
   - `probeVideo` → `videoInfo` without URLs
   - `addVideo` → `added {awaiting: true}`; the stand-in `downex-ui` is started with `--confirm <id>`
   - `downex confirm <id> --video <another listed quality>` → an `.mp4` of that quality
8. **Settings.** `--site-media off` → `site_media_off`. The settings round trip keeps the `media`
   object.
9. **Process hygiene.** The stand-in records argv and environment variable names. The test checks the
   exact arguments of §4.7 and that no Cookie value or proxy password appears in argv.

### 8.3 Gated tests and hooks
- **e2etest-only hooks** (product builds compile `testhooks_release.go`, so none of them exist):
  - `DOWNEX_E2E_TOOLS_BASE=<https base>` replaces `https://github.com` and allows downloads in a test
    daemon
  - `DOWNEX_E2E_YTDLP_KEY=<armored key>` replaces the pinned key
  - `DOWNEX_E2E_FAKE_YTDLP=<json template>` and `DOWNEX_E2E_FAKE_LOG=<file>` drive the stand-in
- **Real network, opt-in, never by default:**
  - `DOWNEX_REAL_TOOLS=1 go test ./internal/tools -run Real` fetches the latest yt-dlp SUMS and
    signature and verifies them, and parses Deno's `.sha256sum`. `DOWNEX_REAL_TOOLS_INSTALL=1`
    installs both into a temporary folder and runs the smoke tests.
  - `DOWNEX_REAL_SITE_URL=<url> go test ./internal/sitejob -run Real` runs a real extraction with
    those tools and prints the offer. It also confirms `--compat-options no-certifi`.

### 8.4 Real-provider verification left to the user (to be reported as unverified until done)
- Real YouTube extraction with the managed yt-dlp and Deno: watch pages, Shorts, embeds, clips,
  multi-language audio, age-gated videos with and without the cookie opt-in, the bot check.
- Real googlevideo downloads with 4 connections and 10 MiB ranges: no 403 or 429 over a long video,
  and expiry refresh after about 6 h (pause overnight, then resume).
- The IP-family rule on a dual-stack network, and behaviour behind HTTP, SOCKS and NTLM proxies.
- A real GitHub install: download, signature, the Authenticode subject of `deno.exe` (to pin), the
  PyInstaller first-run delay, antivirus behaviour.
- The overlay in Chrome/Edge (including Chrome 155's `browser` namespace) and Firefox 140+:
  fullscreen, theater mode, miniplayer, SPA navigation, keyboard and screen readers, forced colours.
- Playback of merged files (AV1, HEVC and H.264 MP4) in the Windows Media Player, VLC and browsers.
- macOS and Linux at run time.

## 9. Open questions (with the recommended default)
1. **yt-dlp channel.** Nightly is upstream's recommendation for regular users and gets YouTube fixes
   within a day; stable can lag for weeks. → **Default `nightly`**, selectable in Settings, with the
   rollback of §4.5.
2. **Default quality above 1080p.** AV1 needs hardware support or the AV1 Video Extension on Windows.
   → **Highest resolution, H.264 preferred at equal height** (§3.11). Revisit if users report
   playback problems. *2026-10-23:* the default is the row of the best quality class; within a
   class H.264 is preferred over HEVC over AV1, so AV1 is offered only for a class without H.264
   or HEVC (on YouTube usually 1440p and 4K).
3. **VP9/Opus (WebM) qualities.** → **Decided 2026-10-22: never.** MP4 only; WebM is not listed.
4. **Connections to googlevideo.** → **The download's own** (32 by default; no built-in cap since
   2026-10-29, 1 MiB requests instead; user rules override). yt-dlp itself uses 1.
5. **Deno publisher pin.** → **Accept any trusted Authenticode signer and record it** until a real
   `deno.exe` shows the subject (expected "Deno Land Inc."), then pin it in `denoSigner`.
6. **Sigstore release attestations** (an independent check for Deno and yt-dlp). → **Not in v1.**
   Add with `sigstore-go` if Deno verification is judged too weak.
7. **PATH yt-dlp without the EJS component** (pip without `[default]`). → **Accepted, with the
   automatic fallback to the managed copy** (§4.5). There is no setting to force the managed copy in
   v1.
8. **Should the video button always open the desktop confirmation?** → **No.** It follows
   `confirm_browser_downloads`, like every hand-off. The browser picker already asked for the quality.
9. **A chosen quality that disappears after Add.** → **Fail with `media_invalid`** and let the user
   add again. Later, the download could return to "awaiting confirmation" with fresh tracks.
10. **Free-space check.** Remux needs about twice the final size. → **Yes**: before preallocating,
    `RunRanged` checks that the volume has room for both, plus 64 MiB, and fails with `disk_full`.
11. **Pre-installing tools when the button first appears.** → **No.** Install on the first click,
    following "download on first use". The look-ahead of §5.6 never installs them either.
12. **Playlists, subtitles, thumbnails, chapters.** → **Later specs.** Playlists would add one
    download per entry with a shared picker.
13. **Licence and terms notice before the first install.** → **No modal.** The overlay or toast
    states what is downloaded and from where, and the Settings page holds the licence and terms
    lines.
14. **Listing every format again** (`downex media --list --all`, amendment 2026-10-23). It needs an
    additive `ProbeMediaRequest` field (all formats) and a daemon path that lists the formats the
    one-per-class rule leaves out (`sitemedia.Offer` keeps them for `Select`). → **Not now.**
    `--video <format id>` already reaches them.

## 10. Status
Specified 2026-10-21; amended 2026-10-22 (MP4 only, every MP4 quality listed, the user chooses);
**implemented 2026-10-22 on Windows** (Step 0, Lanes A–F and the integration step), verified with
stand-in tools, local servers and fakes only. Nothing was committed.

*2026-10-23 (`VIDEO-DETECT-SPEC.md` §10).* The amendments of that spec are implemented:
- **Manifest fallback.** It now accepts convertible MPEG-TS HLS. `TestSiteTSFallback` covers a page
  whose only formats are TS `m3u8_native`: it is listed as MP4 qualities under the site name "Twitch"
  and saved as one `.mp4`.
- **Display names.** `sitemedia.SiteName` gives the curated sites theirs: `Twitter` → X, `TwitchVod`/
  `TwitchClips` → Twitch, `FacebookReel` → Facebook.
- **Extension.** It recognises the curated sites of that spec's §3.8. The look-ahead runs there (never
  on Facebook or Instagram) with per-site throttles. The Download button and `hiddenSites` apply to
  YouTube too.

None of this has run against the real sites yet.

*2026-10-23 (quality names, the amendment of that date).* Go side implemented:
- `internal/media/quality.go` holds the class and name rule, tested with the amendment's table.
  HLS variants and DASH video Representations are named by it (no more "1920x1080" names).
- `internal/sitemedia` offers one video per class: `classVideos`, the order of §3.11, and YouTube's
  ladder labels. `Select` still reaches the formats it leaves out, and `Offer.Listed` lists a chosen
  one in place of its class's row. `ManifestTracks` keeps one video per class.
- `internal/mediajob` names measured tracks with `media.QualityName`.
- `cmd/downexd/site.go` lists a chosen left-out format.
- `downex media --list` has QUALITY and PICTURE columns and names unnamed tracks from their picture.

Tests:
- `internal/sitemedia/testdata/film.json`: the user's 3840x1634 film becomes 8 rows: 4K AV1, 1440p
  AV1, and H.264 from 1080p down to 144p.
- `TestOffersOnePerClass` covers every tie-break.
- The e2e `talkPage` adds an AV1 format in 137's class that is never listed.

The extension and the desktop show the daemon's names (`lib/quality.ts` in each package, the same
file, for older daemons and File rows).

*2026-10-23 review of the quality names (fixed test-first):*
- **Streams.** A variant the class rule leaves out can be chosen again: `cmd/downexd` probes with
  `sitemedia.ManifestTracksFor`, which lists a named variant in place of its class's
  (`TestManifestTracksFor`, `TestMediaJobsProbeLeftOutVariant`). Variants without a RESOLUTION have
  no class and are all listed.
- **Formats without a picture** have no class: all are offered, and yt-dlp's order (worst first)
  breaks the last tie instead of the format ID, so the default is the best of them
  (`TestOffersNoPicture`). Videos count toward the 200-track cap (`TestOffersCapVideos`).
- **One row per class everywhere.** `Offer.Listed` puts a chosen left-out format in place of its
  class's row, so `downex media --list` no longer shows two rows of one name.
- **Older names.** `media.TrackName` (CLI) and the clients' `trackQuality` name an older daemon's
  raw heights from the picture, also those that look standard ("480p" of 1138x480 → "720p (HD)");
  YouTube's label gives way in the same case (`media.LabeledClass`).
- **One rule in the clients.** `lib/quality.ts` follows Go case for case: a frame rate is stated only
  when it rounds above 30, a width alone is a picture, the in-class order compares rounded frame
  rates and then picture and size. `internal/media/quality_sync_test.go` checks that the two copies
  are equal and that their tables are Go's.
- **Extension streams.** Streams are merged by the same encoding (kind, picture, frame rate, codec,
  HDR), no longer by class alone, so a second video whose classes the first already lists keeps
  its rows.

Still open: nothing has run against real YouTube yet.

*2026-10-28 (download speed of direct formats, §2.4 amendment).* Implemented test-first:
- **Fitted chunks.** `mediajob.FitChunk` (`hostcap.go`), applied by `cmd/downexd` `rangedTracks` to
  fresh tracks with the download's `max_connections`. A checkpoint's `ChunkBytes` still wins, so
  downloads started before keep their 16 MiB (or 10 MiB) chunks and resume byte-exact
  (`TestRunRangedResumesOldChunkSize`, `TestRangedTracksChunkSize`). `ChunkSize` is unchanged and
  is the ceiling. Tests: `TestFitChunk`, `TestSiteRunFitsChunks`,
  `TestRunRangedFittedChunksUseEveryConnection` (40 MiB over 8 connections: 16 requests, 8 at
  once).
- **One phase with an init gate and an early pass.** `RunRanged` no longer runs an init phase and
  then a second phase: `nextChunk` hands out only the gated tracks' chunk 0 until every track is
  open, and the early check (`earlyPass`, an incremental walk of box headers, at most one read of
  the bytes up to the end of `moov` per attempt) can open a track on chunk 0's prefix. A 200 to
  chunk 0 and an early pass exclude each other through one compare-and-swap (`headMode`). Tests
  in `ranged_early_test.go` (a)–(h): copy and remux start early with byte-exact output; a
  protected `moov` is refused with only chunk 0 requested; a large or trailing `moov` starts
  nothing early; a malformed box after an early pass still fails the job (`media_invalid`); a 200
  still makes the track whole, and a 200 after an early pass is retried; a broken attempt and a
  403 renewal count the prefix again; a resume with chunk 0 durable has no gate; a run stopped
  during chunk 0 never records its bit.
- **Stalls.** RunRanged's copiers run their stall watchdog (MEDIA-SPEC §7, "Transfer health") on
  the job's clock (`TestRunRangedStalledChunkIsRetried`: one stalled response, one extra request,
  byte-exact). After a stalled attempt the slot drops its idle connections, as `Run` does, so the
  retry dials anew (the same test checks it before the retry arrives).
- **Failure while chunk 0 arrives.** A permanent failure of another chunk cancels a chunk 0 still in
  flight (§2.4, "When a chunk fails the job"); completed chunks stay durable.
  `TestRunRangedFailsOn403AfterRefresh` refuses the third video chunk only once both chunk 0 bits
  are checkpointed, so it checks that guarantee deterministically (it had failed 11 of 200 runs
  when it still assumed that chunk 0 always finished first).
- The refusal "`moov` longer than the first part" (remux) now applies at the fitted chunk size,
  at least 2 MiB (16 or 10 MiB before). Fragmented MP4 `moov` boxes are a few KiB.
- **Measured** (emulated TLS network, 4 MiB/s per connection, 40 ms RTT; medians of 3 alternating
  runs, HTTP/1.1; HTTP/2 the same within 3%): a 64 MiB faststart MP4 (copy, 8 connections) took
  8.26 s before and 2.30 s now (3.6× faster), with 8 requests in flight 0.08 s after the first
  byte (4 at most before). A 60 MiB fMP4 video plus a 5 MiB audio track under the googlevideo rule
  (4 connections, 10 MiB requests) took 7.92 s before and 4.62 s now (0.58×), with all 4
  connections busy 0.08 s after the first byte (2.57 s before) and no request above 7.6 MiB.
  Outputs were byte-exact and the MP4 sample counts correct. Real YouTube is not measured.

*2026-10-29 (pause inside a chunk, owner note 1; §2.4, §2.5, §3.3 and §5.4 amendments).* Implemented
test-first:
- **Cause.** A pause dropped every chunk in flight (up to 4 × 10 MiB on googlevideo), the daemon
  stored the bytes of the latest progress (in-flight bytes included) as the bytes done, and while
  stopped it extrapolated them over the parts (37 uneven parts: "~504 MiB", "~10%"); the resumed
  run started from the synced chunks (10%). A stall or 5xx also restarted its chunk from byte 0.
  Normal downloads were unaffected; HLS/DASH kept their percentage (fragments) and dipped by at most
  their requests in flight.
- **mediajob.** `rtrack.wrote`/`part`, `syncDurable` (note, sync, record; every 8 s, with every bit
  sync, at stop), `TrackCheckpoint.Partial`, `Checkpoint.DurableBytes`/`BytesTotal`, `adopt` of
  valid prefixes, prefix chunks first, retries mid-chunk, the early check on a resumed prefix, the
  200 rule for a resumed chunk 0; `Run` reports `DurableBytes` too. Tests:
  `TestRunRangedPauseKeepsPartialChunk` (progress never decreases, final progress = `durableBytes`,
  the resume asks for `start+half…end`, byte-exact), `TestRunRangedPartialKillPoints` (after the
  write, after the sync, after the checkpoint, an older prefix), `TestRunRangedPartialNeedsSync`
  (every recorded prefix was in the file when a sync began; failing syncs record nothing),
  `TestRunRangedPartialAcrossRefresh`, `TestRunRangedPartialChunkZeroEarlyPass`,
  `TestRunRangedPartialWholeTrack`, `TestRunRangedOldCheckpointWithoutPartial`; updated
  `TestRunRangedStalledChunkIsRetried` (the retry asks for the rest after 512 KiB),
  `TestRunRangedResume` and `ranged_early_test.go` (e), (f), (h) (a retry, a renewal and a resume
  continue after chunk 0's prefix), `TestPauseResume`/`TestCheckpointDurable` (`Run`).
- **Daemon.** `onCheckpoint` stores `durableBytes`; `dlview.Size` keeps the exact size of a started
  site download in every state and never extrapolates its parts, while a site stream keeps its
  estimate across a pause (`internal/dlview` `TestSize`, daemon `TestMediaDownloadSize` for the
  running job's precedence, `liveMediaSize`, and `TestMediaStoresDurableBytes`); a stopped
  download with a checkpoint from 0.8.0/0.8.1 shows the bytes of its done chunks, where the resume
  starts (`dlview.Received`, `TestOldSiteCheckpointShowsItsDurableChunks`); `cmd/downexd` converts
  the new fields (`TestMediaCheckpointPartialConversion`). Desktop: `lib/format.ts` never uses the
  parts of format `http` (`format.test.ts`).
- **CLI (2026-10-29).** `info` of such a paused web page video showed "Size: unknown" and an
  inflated size while downexd's list showed its percentage of the exact size: the CLI's local views
  read `Download.size` and `bytesDone` as stored. The stored-state rules moved from the daemon to `internal/dlview`, which downexd and the
  CLI both use (§3.7). Tests: `cmd/downex` `TestInfoOldSiteCheckpoint`, `TestInfoDurableCheckpoint`,
  `TestStreamEstimateLocal` and `TestLocalViewMatchesDaemon` (a table of stored records of every kind,
  read by the CLI without downexd and by downexd's Manager on the same database: same size, "~",
  bytes done and `ls` table). The failure message of `--wait` reports a web page's direct video's
  kept progress as `pause` and the progress line do: its bytes of an exact size, else its parts,
  never fragments (it said "4/37 fragments"; `TestMediaFollow`).
- **End to end.** `TestSitePauseMidChunk` (pause while video chunk 2 is half received: the paused
  download shows 2.5 MiB of its exact size, the resumed run asks only for the rest of chunk 2 and
  never shows less, byte-exact), `TestPauseResumeNeverGoesBack` (a file and an HLS stream: received
  bytes and fragments never go back across pause and resume).
- Not verified: a real YouTube pause and resume, and a real power cut between the sync and the
  checkpoint (modelled by `TestRunRangedPartialKillPoints`).

**Implemented as specified**, with the deviations listed below:
- Contracts (Step 0): proto §3.1 (no `audio_only`, no `skipped`), `model.Media.Origin`/`Site`,
  `MediaTrack.Size`/`SizeApprox`/`Ext`, `MediaTrackCheckpoint.ChunkBytes`, `media.PlanRanged`,
  `internal/sitemedia/errors.go`; policy wording in `CLEAN-ROOM.md`, `REQUIREMENTS.md`, `MEDIA-SPEC.md`.
- `internal/tools` (§4): PATH discovery, first-use install from GitHub releases with the stdlib OpenPGP
  verifier, Deno sha256 + Authenticode, atomic version folders, leases, updates, anti-rollback,
  bad-version rollback, PATH fallback, `Exec` (job object / process group, allow-listed environment).
- `internal/sitemedia` (§3.10, §3.11): every distinct MP4 quality best first, H.264 preferred at equal
  height, MP4 audio only; WebM/VP9/Opus and audio-only pages are never offered.
- `internal/sitejob` (§2.6, §4.7): the exact argv, cookie file, proxy environment, cache, joining,
  2 concurrent runs, classification with 21 stderr fixtures.
- `internal/mediajob.RunRanged`, `engine.RequestCaps`, `transport.WithNetwork` (§2.4, §2.5); host rules
  for M5 `Run` too.
- Daemon and CLI (§3.2, §3.3, §3.7, §3.8): origin SITE in AddMedia/ProbeMedia/Confirm/runMedia, the
  wait budget, the error codes, the `media` settings, `max_request_bytes` and the built-in
  `*.googlevideo.com` rule, `Event.tools`, GetTools/InstallTools, the daily tools check;
  `downex media --site`, `downex tools`, the settings flags; `info` shows "video page (<site>)" and
  "Parts".
- Desktop (§5.3–§5.5): Add dialog page mode with retries, the picker with sizes and codec names, the
  confirm quality switch keeping `.mp4`, Settings → Video sites, the first-use toast, Page/Parts/
  Merging/"Getting video info…", progress by `BytesTotal` in the progress window, the details panel and
  the main list.
- Relay and extension (§3.5, §3.6, §5.1, §5.2): `probeVideo`/`addVideo`, `features`, `reason`,
  `awaiting`, the 4-slot video pool; the YouTube overlay, popup video rows, "Find the video on this
  page", the context-menu route, the options.
- Integration (§7): `cmd/downexd` `--tools-dir`, `tools.go` (tools manager, https client through the
  proxy, `toolsAdapter` to `daemon.Tools`), `Config.Site`/`Config.Tools`, the tools timer, shutdown order
  (downloads paused, then the resolver, then the manager); `testhooks*` (`DOWNEX_E2E_TOOLS_BASE`,
  `DOWNEX_E2E_YTDLP_KEY`, `DOWNEX_E2E_*` passed to tools); `platform.FreeSpace` wired into site runs
  (§9 #10); `mediajob: disk full` mapped to `disk_full`; the store fixture; the installer's "delete
  user data" names the tools folder; docs (DAEMON-SPEC, BROWSER-SPEC, DESKTOP-SPEC, M6-SPEC, MEDIA-SPEC,
  REQUIREMENTS, README, the layout notes).
- "Quicker" (user request 2026-10-22): one yt-dlp extraction per page serves the picker, the add and
  the start of the run (15-minute cache, joined callers); googlevideo downloads use 10 MiB ranges over
  4 connections; every client (desktop, extension panel and add loop, CLI) starts its next try 2 s
  after the start of the last one, so at once after a call that used the whole 25 s budget; during a
  first-use install the daemon answers `tools_installing` with the progress within about 1 s; video
  requests in the relay never delay capture adds.

**Review fixes (2026-10-22, after the integration step).** Each with a test written first:
- Manifest fallback: `mediajob.ProbeMP4` + `sitemedia.ManifestTracks` list only fragmented-MP4
  tracks with MP4 codecs, best first (no MPEG-TS variant, no WebM/VP9/Opus Representation, no
  subtitles); runs use `Options.MP4Only` (refused before fragments, after sniffing and after
  assembly); `runMedia` fails a site run whose container is not `mp4` (`media_unsupported`) and
  removes that output and its part folder.
- `RunRanged` resume: before a resumed track's file is recreated (or the folder of a track no longer
  chosen is removed) a checkpoint without its chunks is stored synchronously (a failed store fails the
  run with nothing changed), so a crash can never leave old bits over a new zero-filled file; a track
  file that exists but cannot be opened (after the sharing-violation retries) fails the run with its
  progress kept instead of starting the track over.
- A full volume while joining, recording or renaming the output is `mediajob: disk full`
  (`disk_full`, live and stored); `isNetworkError` no longer takes file errors (an errno outside a
  `*net.OpError`) for network errors (`internal`).
- A declared size smaller than the resource: an unranged track's 206 total or 200 length must equal
  it (`ErrRangeMismatch`), and a copied MP4's top-level boxes must fill the file exactly
  (`media.ErrMalformed`), so a cut file is never published.
- SOCKS5 reaches yt-dlp as `socks5h://` (argv and environment); `socks5h` is accepted by
  `sitejob.checkRequest`.
- The previous yt-dlp of a rollback trial is hashed again (SHA-256 and size) before it is offered; a
  changed copy ends the trial and never runs.
- `page_url` and `Referer` with user information are refused (daemon and resolver).
- Confirm of a site download pairs a video-only quality with the first audio in downexd's order
  (as the pickers and `sitemedia.Select` do); the extension picker does the same.
- Extension: after `auth_required` with the sign-in already allowed the panel offers "Try again" and
  such a refusal is not kept in the probe cache; an add that gives up with `tools_installing`/
  `site_resolving` says so and offers "Try again"; the overlay updates its rows in place and keeps the
  focus in the panel (back on the Download button when it closes after sending); the live status
  text stays "Getting video info…" with the seconds in an `aria-hidden` span (overlay and popup);
  a muxed row names both codecs ("H.264 + AAC").
- Desktop: each site quality is labelled with the size of the file it makes (video + paired audio),
  as in the browser. CLI: `tools install` failures read "Downex could not set up yt-dlp/Deno:
  <reason>".

**Quicker quality lists (amendment 2026-10-22; the user found the 4–5 s after Download annoying).**
Measured on the user's machine (§5.6): Downex adds about nothing, one extraction takes 4.0–4.6 s. Three
changes, made in lanes:
- Look-ahead (§5.6): proto `ProbeMediaRequest.prefetch` (field 5), the relay's `probeVideo.prefetch`
  (§3.5), the extension's look-ahead of the watched video with the option `videoPrefetch`. Daemon
  (`probeSite`): a look-ahead never calls `Tools.Ensure` (`tools_unavailable` at once while the tools
  are not ready), passes `SiteSelection.Prefetch` to the resolver, and `prefetch` without origin SITE
  is InvalidArgument. Resolver: `Request.Prefetch` is answered from the cache or joins the running
  extraction of its page; otherwise it starts one only on an idle resolver, in a turn of its own
  outside `MaxConcurrent`, else `ErrBusy` (`site_resolving`) at once; unless a user's request joined
  it, it never asks for another yt-dlp copy; its result fills the shared cache (`Prefetch` is not in
  the key).
- Onedir yt-dlp (§4.2) for the managed copy where the release's SUMS lists a zip.
- YouTube manifests skipped (§4.7): `youtube:skip=translated_subs,hls,dash`; a YouTube result without a
  direct MP4 video is resolved exactly once more with the manifests by the same copy, and that result is
  cached as usual.
- Tests (daemon, resolver and adapter; fakes and the stand-in yt-dlp only): a look-ahead never calls
  Ensure or Install, answers `tools_unavailable` at once for missing or installing tools and
  `site_resolving` at once when the resolver is busy; the resolver refuses a second look-ahead (its
  own cap of one) and one behind a user's extraction without running yt-dlp, a user's extraction
  starts beside a running look-ahead even with a single turn, a look-ahead joins a user's extraction
  and a user's request joins a look-ahead (also after the look-ahead's caller left), a look-ahead fills
  the cache a following request uses without a second run, and a failed look-ahead tries no other
  copy unless a user's request joined it; the exact argv with and without the manifests; a YouTube page without a direct MP4 video runs
  twice (the second time with the manifests, the same lease, both outcomes reported, the result
  cached), never three times, while other extractors, refused results and pages with a direct MP4 run
  once.
- Decisions: "busy with user work" is read as any other extraction running or waiting (yt-dlp and
  Deno compete for the CPU), so a look-ahead starts only on an idle resolver. The re-resolve with the
  manifests also follows a refusal by the offer (DRM, sign-in) on a YouTube page: only a run with the
  manifests can tell whether the fallback has an MP4, at the cost of a second run on such pages. (The
  lanes' notes on discovery and on `Acquire` starting an install were resolved by the integration
  step below.)
- Lanes: browser and relay (P), daemon and resolver with the manifest skip (Q), tools with the onedir
  build (R; tests: the asset map against the real 2026.08.19 SUMS, fresh onedir install and run of
  `_internal/`, discovery of changed or missing files, migration with the lease-held skip, a release
  without a zip and a broken build, a tampered zip, about 30 hostile zips).

**Integration of the quicker quality lists (2026-10-22).** Every lane's request applied (the e2e argv
of `TestSiteVideoFromPath`, the layout notes, DAEMON-SPEC and BROWSER-SPEC, §3.5, §3.6, §3.8, §3.9, §4.2, §4.4,
§4.5, §5.2, §5.6 of this spec); the review's findings checked one by one, each fix with a test written
first and seen failing:
- **Fixed (high): a look-ahead could install the managed yt-dlp.** A look-ahead whose PATH yt-dlp
  failed was still reported, which scheduled the managed copy's trial; the next look-ahead's `Acquire`
  then downloaded and installed it (reproduced through the real resolver and manager: 4 release
  requests from two look-aheads, also with automatic updates off). Now `tools.Manager.AcquireReady`
  gives a look-ahead only ready copies (never an install, never a fallback or rollback trial's copy,
  which stays for the next `Acquire`), and the resolver reports nothing of a look-ahead no user's
  request joined (a user who joins later turns it into a user's extraction: its outcome is reported
  then, and its failure gets the other copy). Tests: `TestAcquireReady`,
  `TestAcquireReadyLeavesTheRollbackTrial`, `TestLookAheadsNeverInstall` (real resolver and manager:
  no release request, no trial, no unsuitable mark; the user's request after them gets the fallback),
  and the resolver's `TestPrefetch*`.
- **Fixed (medium): a failed look-ahead answered the user's click for 30 s** without the other-copy
  retry a click gets. A look-ahead's failure that may be the copy's (`site_error`, bad options) is no
  longer cached unless a user's request joined it; page refusals still are
  (`TestPrefetchTriesNoOtherCopy`, `TestPrefetchCachesPageRefusals`).
- **Fixed (medium): a look-ahead restarted downexd after the user quit it.** The relay never starts
  downexd for `probeVideo{prefetch}` (`daemon_unavailable`; the extension then rechecks the status and
  stops look-aheads) (`TestPrefetchNeverStartsTheDaemon`).
- **Fixed (medium): after a welcome sent while downexd was starting, look-aheads stayed off.** A
  `videoInfo` answer or a `rejected` refusal (only downexd sends one) now marks downexd as running.
- **Fixed (medium): look-aheads carried the user's sign-in cookies.** None is sent while "Send my
  sign-in cookies" is on, and a look-ahead's headers never include `Cookie` (§6.3; the option's
  "for that video only" stays true). Look-aheads also need the Download button shown
  (`lookAheadAllowed`), which settles lane P's open product question.
- **Fixed (medium): no volume limit or back-off when YouTube throttles.** A budget of 10 look-aheads,
  then one a minute; a 30-minute pause after YouTube's rate limit or bot check answered one; a
  look-ahead's `site_unavailable`/`auth_required` is never kept in the extension's cache (a click asks
  Downex again); §6.6 no longer claims a limit that nothing enforced.
- **Fixed (low): a look-ahead right after downexd started got `tools_unavailable`** (and the extension
  backed off) while discovery had not found the copies yet. `tools.Snapshot.Discovering`
  (`daemon.ToolsSnapshot.Discovering`, not in the API) lets it through; `AcquireReady` waits for that
  discovery, which never downloads.
- **Fixed (low): a crash between `placeVersion`'s two renames lost the only copy** (the next start's
  cleanup removed `<version>.old-*`). The start now renames it back first
  (`TestStartupRestoresASetAsideCopy`).
- **Fixed (low): look-aheads could push pages out of the daemon's 32-entry cache** that the extension
  still showed (50 kept answers), so the Add re-resolved them: the cache keeps 64
  (`TestCacheHoldsWhatTheExtensionShows` reads the extension's constant). The Cookie re-read between a
  signed-in probe and its Add (a different cache key if YouTube rotated a cookie meanwhile) predates
  the look-ahead, which never sends cookies now; it is left as is.
- **Not changed (low): `placeVersion` holds the manager's lock across up to three rename retries**
  (about 0.55 s each on Windows, only when something holds a file open), which can delay `Status`,
  `Ensure` and `Acquire` during a same-version replacement such as the onedir migration. Moving the
  renames out of the lock needs a "being replaced" state that leases wait for; left for a later step.
- **Not done (optional):** an e2e case that publishes an onedir zip (the e2e release server publishes
  the onefile asset, which the manager still uses when no zip is listed; the onedir path is covered
  by the `internal/tools` package tests).

**Verification** (repeated in full after the review fixes, 2026-10-22; again after the integration of
the quicker quality lists). `go build ./...`, `go vet ./...`,
`CGO_ENABLED=1 go test -race ./...` (WinLibs GCC 15.2, all 34 packages including `internal/e2e`) and
`go test ./...` pass on Windows; darwin/arm64 and linux/amd64 (earlier also darwin/amd64 and
linux/arm64) build, vet and `go test -c` of the touched packages with `CGO_ENABLED=0`; `pnpm check` and
`pnpm test` pass in `ext/` (440 tests) and `ui/frontend/` (536 tests); `go vet -tags desktop,production
./ui` passes; `scripts/build-windows.ps1 -Version 0.6.1 -Out <temp dir>` builds every Windows artifact
(0.6.0 before). Real-process
end-to-end tests (`internal/e2e/site_test.go`, the test binary as the stand-in yt-dlp and Deno, chosen
by file name): PATH first (the release server gets no request), `--list` of every MP4 quality with the
default, a download under a host rule of 1 MiB requests and 2 connections into one progressive MP4 of
both tracks (checked with mp4ff), the exact argv of §4.7, the cookie only in the cookie file, the
allow-listed environment, no work folder left; kill and restart refetching only chunks without stored
bits, byte-identical to an uninterrupted download; 403 → one renewal (exactly one more extraction) →
completion; `encrypted`/`live`/`media_unsupported`/`auth_required`/`site_unsupported` refusals, a
`pssh` init failing after the first chunks only, `site_media_off` and the settings round trip; the
relay's `features`, `videoInfo` without addresses, `addVideo` awaiting with the window started,
`downex confirm --video` of another MP4 quality; the first-use install of yt-dlp from a TLS release
server (`tools_installing` with its progress, then the list; `openpgp+sha256`) and a bad signature
(`tools_unavailable`, tool code `signature`).

**Not verified** (§8.3, §8.4; to be reported as unverified until done): the gated real-network tests
(`DOWNEX_REAL_TOOLS`, `DOWNEX_REAL_TOOLS_INSTALL`, `DOWNEX_REAL_SITE_URL`) were not run; no real YouTube
extraction, googlevideo download, expiry after about 6 h, proxy (HTTP, SOCKS, NTLM 407) or dual-stack
check; no real GitHub install, Deno Authenticode subject, PyInstaller first-run delay or antivirus
check; no real browser (overlay, popup, Firefox, Chrome 155); no playback in real players; macOS and
Linux compile only. The 2026-10-22 speed-ups were not measured again with the real yt-dlp: the
savings of the onedir build and of the skipped manifests are the user's measurements, and YouTube
pages that need the manifest fallback were not tried for real. The look-ahead was not tried in a real
browser (SPA timing, Shorts swipes, visibility changes; the content script's wiring itself has no unit
test), nor against YouTube's real rate limit and bot-check messages (the extension recognises them by
yt-dlp's wording). The onedir zips were not checked against upstream's real files (root executable
names, no symlinks in the macOS and Linux zips, renaming the root executable): run the gated
`TestRealRelease`/`TestRealInstall` (`DOWNEX_REAL_TOOLS=1`, `DOWNEX_REAL_TOOLS_INSTALL=1`), at least on
Windows x64; a wrong guess falls back to the onefile from the same signed list. The Deno first-use install is covered by `internal/tools` package tests only (the
stand-in `deno.exe` carries no Authenticode signature, so the end-to-end install test keeps Deno on
PATH). Durations of the merged file are checked through sample counts and data, not separately.

**yt-dlp's signing key:** `internal/tools/ytdlp_key.asc` is upstream's `public.key` (fingerprint
`AC0C BBE6 848D 6A87 3464 AF4E 57CF 6593 3B5A 7581`, from
`https://raw.githubusercontent.com/yt-dlp/yt-dlp/master/public.key`), byte for byte; a managed
yt-dlp install whose checksums it does not verify fails safe with tool code `signature` ("yt-dlp
failed its signature check"). The real 2026.08.19 `SHA2-256SUMS`, `SHA2-256SUMS.sig` and
`public.key` are in `internal/tools/testdata/ytdlp-2026.08.19/`, which `TestEmbeddedYtDlpKey` and
`TestRealYtDlpSignature` check.

**Deviations** (decisions taken while implementing):
- Contracts (Step 0)
  - `Plan.MediaFragments()` counts every fragment of a SingleResource plan (fragment 0 is Init only to
    be fetched and checked first). `PlanRanged` also refuses a bad Kind, Network or URL (ErrMalformed)
    and checks ChunkBytes only for ranged tracks.
  - `sitemedia.OriginManifest` is `""` (old records match), `OriginSite` is `"site"`; sentinel texts
    carry no package prefix. Removed proto fields were never shipped and are not `reserved`. Formats the
    parser throws away are "dropped" (§3.10, §3.11).
- Tools manager (Lane A)
  - The embedded yt-dlp key was first a placeholder; it is upstream's key now (above). `deno.exe`'s signer is accepted from any trusted
    chain (`denoSigner` = "") and recorded in `tools.json`; `Info.Signer` has no API field.
  - `tools.Manager` returns `tools.Snapshot`; `cmd/downexd/tools.go` adapts it to `daemon.Tools`.
    `Snapshot.Ready` stays true while a ready copy is updated. `Install` returns `(status, nil)` at its
    context's end; the daemon's InstallTools accepts that as well as `ErrToolsInstalling`.
  - After `Report(SiteError|BadOptions)` the next `Acquire` may return another yt-dlp (managed copy or
    previous version); the resolver retries the page once with it. Assets also for windows/386
    (`yt-dlp_x86.exe`) and linux/arm (`yt-dlp_linux_armv7l`), both without Deno (DISABLED).
  - Unnamed failures use `internal` (oversized download, archive layout, bad tag), `http_status` (a web
    page instead of a file) and `network` (redirect to http). `ExecResult.ExitCode` is -1 when Exec
    killed the child; the memory limit applies on Windows only. `platform.ToolsDir` reads
    `%LOCALAPPDATA%` from the environment only. Extra files `sys_windows.go`, `sys_unix.go`,
    `sys_other.go`.
- Resolver and adapter (Lane B)
  - `ResolverConfig.Tools` is a small interface (`Acquire`, `AcquireReady` for look-aheads since
    2026-10-22, `Report`, `Env`). Refusals and failures are cached 30 s (tool-state errors,
    cancellations and shutdown never; since 2026-10-22 also not a look-ahead's failure that may be the
    copy's, unless a user's request joined it); an extraction still waiting for a
    slot is dropped when its last caller leaves; the cache key uses the proxy URL without credentials.
  - Classification checks `site_unavailable` before `auth_required` (YouTube's private-video text
    contains "Sign in"); "available in your country" is matched; a 407 is `site_error` "the proxy needs
    a sign-in yt-dlp cannot do"; reasons drop sentences with URLs or yt-dlp options. Exit code 2 is
    reported to the manager as BadOptions (users see `site_error`); stdout over 32 MiB is
    `media_invalid`.
  - `site.go` passes the factory unwrapped (RunRanged applies each track's `Network`); a checkpoint's
    chunk size wins on resume; the 5 s retry also covers the manifest fallback; a muxed `ext mp4` format
    without named codecs is offered; audio-only manifest formats are ignored when picking the fallback
    manifest.
- Media engine (Lane C)
  - `Options.FreeSpace` (used by RunRanged only; downexd passes `platform.FreeSpace`, which measures a
    missing folder at its nearest existing parent). `mediajob.ErrDiskFull` ("mediajob: disk full") is
    its own sentinel, mapped to `disk_full` by its text.
  - A whole-resource track records `ChunkBytes` 0; a `NotBefore` more than 10 min ahead is refused; a
    proactive renewal that fails is not fatal; a first 403 within 60 s of the last renewal waits for the
    gap; one video-only track is accepted for remux; range mismatches and a wrong-length 200 to chunk 0
    are permanent, a 200 to a later chunk is retried; part folders of tracks no longer selected are
    deleted on resume; copy mode writes the output record before its renames.
  - `Run` gets per-host connection caps, host limiters and request caps on byte-range fragments, but no
    429 halving. The engine's cap is the smaller of the original and the redirected host's.
- Daemon and CLI (Lane D)
  - A site download's Cookie is kept in memory only (at most 1024 downloads); a restarted daemon
    resolves without it. `tools_channel` "" is stored as "" and read as nightly. Without `Config.Tools`
    the tools RPCs answer Unimplemented with code `tools_unavailable`.
  - Any matching user host rule disables both built-in googlevideo values for that host. A manifest's
    MediaInfo keeps origin UNSPECIFIED (= MANIFEST). A site download stays PROBING until its first
    checkpoint after progress. yt-dlp gets SOCKS5 as `socks5h://` (the proxy resolves host names, as
    Downex's own SOCKS5 routes do; behaviour behind a real SOCKS proxy is unverified).
  - `media --json` always has `origin`; on a pipe the retry status is printed once per step.
- Desktop (Lane E)
  - The "audio select only with several audio tracks" rule also applies to manifests; audio labels do
    not repeat a language the name contains; Retry is hidden for `media_unsupported`, `media_invalid`
    and `site_unsupported`; "Add site login…" is hidden for a page's `auth_required`.
  - Retries stop after 16 min; a success after Cancel still counts. The first-use toast is its own
    element (standard toasts vanish after 9 s). `Api.openExternal` is frontend-only (https only). A name
    typed without `.mp4` gets it.
- Browser (Lane F)
  - The probe cache key includes whether cookies were sent; video options are re-read per request.
  - The overlay follows the player itself (WXT 0.21.4's `autoMount` throws with an element anchor) and
    isolates events itself; sizes use IEC units like the desktop; unknown track kinds are left out of
    `videoInfo`; the relay answers `daemon_unavailable` beyond 16 video requests.
  - `downex:find-page-video` returns the entry and the popup probes it next; `tools_unavailable` is text
    only in the extension (it cannot open Downex's settings).
- Integration
  - Every daemon gets a tools manager: the user's own daemon downloads (the default per-user folder);
    every other daemon uses `<dir of --db>/tools` without an HTTP client (PATH copies or copies already
    there), unless an e2etest build is given `DOWNEX_E2E_TOOLS_BASE`. A tools folder that cannot be
    used logs a warning and the daemon runs without site media (`media_unsupported`). The tools timer
    runs only in daemons that download.
  - `DOWNEX_E2E_YTDLP_KEY` takes the armored key or the path of a file holding it.
  - The stand-in tools are the e2e test binary told by its file name (`yt-dlp`, `deno`); `DOWNEX_E2E_FAKE_YTDLP`
    names a JSON file of answers per page path (stdout with `{{run}}` = the extraction number, stderr,
    exit code) rather than one template, and `DOWNEX_E2E_FAKE_LOG` receives argv, variable names, working
    folder and cookie-file content per run.
  - `ext/package.json` gained `"check": "pnpm run typecheck"` so that `pnpm check` exists in both
    frontends. The list's progress (`format.ts` `progressFraction`/`etaSeconds`) counts a site
    download's bytes once its size is known; that replaced the progress window's own `progressShare`.
  - The installer's "delete user data" removes the tools folder by name before the rest of
    `%LOCALAPPDATA%\Downex` (which already contained it).
  - Not done: folding the daemon tests' `newSiteHarness` into `newHarness` (optional). The stray
    `downex-nmhost.exe` and `ui.exe` (plain `go build` outputs of lanes) are gone from the repository
    root (checked again 2026-10-22).
- Open after the review (not changed):
  - The bad-version rollback (§4.5) still counts any 3 non-OK extractions of a new yt-dlp, whatever
    the pages, and marks it `Bad` for good: a site that stalls or breaks its first three extractions
    can pin the previous release until upstream ships a newer one. Counting only failures on several
    page hosts (the lease would need the page), or letting `Bad` expire, is a design change of
    `internal/tools` and `internal/sitejob` left for a later step.
  - A ranged track answered with `Content-Range: …/*` is not checked against its declared size past
    its end; a cut file is still caught when it is joined (remux) or copied (the box check).
