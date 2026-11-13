# Media specification (M5)

Downex downloads **unencrypted** HTTP streaming media the user is watching: HLS (RFC 8216 /
draft-pantos-hls-rfc8216bis) and MPEG-DASH (ISO/IEC 23009-1). It never decrypts, never fetches keys,
and refuses DRM-protected content. Its own code never deciphers site-specific signatures; video pages
are resolved only by running yt-dlp as an external process (`SITE-MEDIA-SPEC.md`).

## 1. Package `internal/media` (pure parsing, no network)
- `ParseHLS(base *url.URL, data []byte) (*HLSPlaylist, error)`: master playlists (`#EXT-X-STREAM-INF`
  variants with BANDWIDTH, AVERAGE-BANDWIDTH, RESOLUTION, CODECS, FRAME-RATE, AUDIO/SUBTITLES groups;
  `#EXT-X-MEDIA` renditions with TYPE, GROUP-ID, NAME, LANGUAGE, DEFAULT, URI) and media playlists
  (`#EXT-X-TARGETDURATION`, `#EXT-X-MEDIA-SEQUENCE`, `#EXTINF`, `#EXT-X-BYTERANGE` incl. implicit offsets,
  `#EXT-X-MAP` incl. BYTERANGE, `#EXT-X-DISCONTINUITY`, `#EXT-X-ENDLIST`, `#EXT-X-PLAYLIST-TYPE`,
  `#EXT-X-KEY`/`#EXT-X-SESSION-KEY`). Relative URIs resolved against `base`. Attribute lists parsed per
  RFC 8216 §4.2 (quoted strings, enumerated strings, decimal resolution, hex).
- `ParseDASH(base *url.URL, data []byte) (*MPD, error)`: static MPDs; Periods, AdaptationSets,
  Representations (id, bandwidth, width, height, codecs, mimeType, audioSamplingRate, lang),
  BaseURL inheritance, SegmentTemplate (`$RepresentationID$`, `$Number[%0Nd]$`, `$Bandwidth$`, `$Time$`,
  startNumber, timescale, duration, SegmentTimeline S@t/@d/@r incl. negative r to period end),
  SegmentList (SegmentURL media/mediaRange), SegmentBase (indexRange → needs sidx, report as such),
  initialization. `type="dynamic"` (live) → `ErrLive`.
- `Plan` — a uniform fragment plan for one selected track:
  `[]Fragment{URL string; ByteRange *[2]int64 (inclusive); Duration time.Duration; Index int; Init bool}`
  plus container kind (`ts`, `fmp4`, `webm`, `unknown`).
  `PlanHLS(media *HLSPlaylist)` and `PlanDASH(mpd *MPD, representationID string)`.
- Quality options: `Options(...) []Track{ID, Kind (video|audio|muxed), Bandwidth, Width, Height, Codecs,
  Language, Name}` for the picker; best-video + best-audio default pairing helper.

## 2. DRM / encryption refusal (`ErrEncrypted`)
- HLS: any `#EXT-X-KEY` or `#EXT-X-SESSION-KEY` whose METHOD is not `NONE` (incl. AES-128, SAMPLE-AES,
  SAMPLE-AES-CTR) anywhere in the playlist.
- DASH: any `ContentProtection` element at MPD, Period, AdaptationSet or Representation level.
- fMP4 init segments: `CheckInit(data []byte) error` walks ISO-BMFF boxes and returns `ErrEncrypted` when
  it finds `pssh`, `tenc`, `encv`, `enca` or `sinf` (bounded walk; malformed → error, never panic).

## 3. Limits (untrusted input)
- Playlist/MPD input ≤ 8 MiB; ≤ 100,000 segments per plan; ≤ 1,000 variants/representations;
  URIs ≤ 32 KiB; nesting depth bounded for XML; SegmentTimeline expansion capped before allocation.
- All parsers have native Go fuzz targets; never panic; errors are values.

## 4. Other packages (M5 part 2)
Extension stream detection (MIME/extension sniffing in observe-only webRequest), daemon media jobs
(fragment download on the shared engine/limiter, ordered assembly), remux, and the UI quality picker.

## 5. Remux (decision 2026-10-20: pure Go first)
- No external program in v1. fMP4 tracks (DASH, fMP4 HLS): concatenate init + fragments per track,
  then join video + audio into a progressive `.mp4` in pure Go with `github.com/Eyevinn/mp4ff` (MIT).
- MPEG-TS HLS (decision 2026-10-23, `VIDEO-DETECT-SPEC.md` §4): converted to MP4 by Downex's own pure-Go
  TS→MP4 remuxer (`internal/remux/mpegts` demultiplexer; H.264 and HEVC Annex B → `avcC`/`hvcC`, AAC ADTS →
  raw AAC + `esds`; no FFmpeg, no re-encoding), muxed variants and a TS video variant with a separate TS AAC
  rendition alike, into one progressive (or, above 4M samples, fragmented) `.mp4`. Codecs it does not convert
  (AC-3, E-AC-3, MP3, MPEG-2 video, packed audio) are `media_unsupported`. Downloads stored as `.ts` before
  2026-10-23 with a checkpoint keep the byte-for-byte join (`remux.ConcatTS`).
- WebM/Matroska is never offered (MP4 only, `SITE-MEDIA-SPEC.md` amendment 2026-10-22). No FFmpeg sidecar
  is planned.

## 6. End-to-end flow (M5 part 2)
1. **Detect (extension):** observe-only webRequest flags responses whose Content-Type is
   `application/vnd.apple.mpegurl`, `application/x-mpegurl`, `audio/mpegurl`, `application/dash+xml`, or
   whose URL path ends in `.m3u8`/`.mpd`, per tab (deduped by URL, max 50 per tab, cleared on navigation).
   Toolbar badge shows the count; the popup lists "Media on this page" (URL host + path tail, format,
   page title). Clicking one sends `addMedia` (BROWSER-SPEC §2) with the request headers the page used.
   Since 2026-10-23 detection is per frame and document, also lists direct MP4 files, and offers each
   stream's MP4 qualities in the popup and in a Download button over the page's `<video>`
   (`VIDEO-DETECT-SPEC.md` §2.4–§2.6, `probeStream` and `addMedia` tracks).
2. **Relay:** `downex-nmhost` maps `addMedia` → `DownloadService.AddMedia` (source BROWSER).
3. **Daemon `AddMedia`/`ProbeMedia`:** fetch the manifest (GET, same validation as Add; ≤ 8 MiB), parse with
   `internal/media`; for an HLS master also fetch the chosen variant's media playlist (and the audio
   rendition's). Encrypted → `InvalidArgument` with error_code `encrypted`; live → `live`. The download
   is created with `kind = MEDIA`, `media` filled (tracks, default pick via `DefaultPair`, format,
   duration, title), category Video (or Audio for audio-only), filename from `filename` or title or the
   manifest path (sanitised) + `.mp4` (fMP4) / `.ts` (TS). Browser hand-offs then await confirmation; the
   UI's dialog shows a quality picker (video tracks sorted by resolution/bandwidth, audio tracks by
   language/bandwidth) and `Confirm` carries `video_track`/`audio_track`.
   *Since 2026-10-23* (`VIDEO-DETECT-SPEC.md` §3.3) MANIFEST probes and adds are MP4-only for every
   client: `mediajob.ProbeMP4` lists only tracks that can be saved as MP4 (convertible TS included), an
   audio-only presentation is `media_unsupported`, the container is `mp4`, the name ends in `.mp4` and the
   category is Video. Only legacy records keep `.ts`.
4. **Media job (`internal/mediajob`):** for each selected track build the Plan; download fragments with
   the shared transport + limiter, concurrency `min(fragments, max_connections)` per job, each fragment
   into `<part dir>/<track>/<index>.frag` (byte ranges honoured), retries with backoff, `CheckInit` on
   every init segment before any media fragment (encrypted → fail with `encrypted`). Progress =
   fragments done/total + bytes; resume skips fragments recorded complete (persisted fragment bitmap,
   committed after fsync). When all fragments of all tracks are done → assemble.
   *Since 2026-10-28* (§7, "Transfer health"): the job keeps its connection slots from the manifest
   fetch to the last fragment (a slot's idle connections only into a phase that fetches from their
   hosts), and a response that delivers no byte for 20 s is closed and retried.
5. **Assemble/remux (`internal/remux`):** TS → concatenate in order into `<name>.ts`. fMP4 single track →
   init + fragments → `<name>.mp4` (fragmented MP4 is acceptable). fMP4 video + audio → one MP4 with both
   tracks via mp4ff (progressive preferred; a correctly multiplexed fragmented MP4 is acceptable). Output is
   written to `<name>.dxpart` then renamed; part dir removed after success.
   *Since 2026-10-23* (`VIDEO-DETECT-SPEC.md` §4, §4.8): TS (one muxed track, or TS video + TS audio) →
   converted into `<name>.mp4`, after an early `remux.CheckTS` of each TS track's first fragment
   (scrambled → `encrypted`, unconvertible → `media_unsupported`, unparsable → `media_invalid`). Only a
   legacy `.ts` record with a checkpoint (`LegacyTS`) is still concatenated into `<name>.ts`.
6. **CLI:** `downex media <manifest-url> [--list] [--video ID] [--audio ID] [-o dir] [--name f] [-H ...]`.
   `ls` and `info` show a stream that is not completed as downexd does, also without a running
   downexd (`info` always reads the database): its size is an estimate ("~", extrapolated from the
   durable fragments; unknown before the first one) and its percent counts fragments
   (`internal/dlview`, SITE-MEDIA-SPEC §3.7).

## 7. Status and deviations (M5 part 2, 2026-10-20)
Implemented as specified above, with these decisions and deviations. *Updated 2026-10-23:* the TS→MP4
remuxer, MP4-only MANIFEST probes and legacy `.ts` records of `VIDEO-DETECT-SPEC.md` are implemented
(its §10 has the gates and deviations); their notes below are marked with that date. *0.7.1
(2026-10-23):* TS segments behind a junk prefix (a CDN's 1×1 PNG served as `image/png`) are read from
their first packet; a bare media playlist's track is measured at probe time (an estimated size, its
picture, codecs and name); a stream's size is estimated while it downloads (`Download.size_approx`).
`VIDEO-DETECT-SPEC.md` §3.3, §4.2, §4.8 and §10 have the rules.
- **Packages and wiring.** `internal/mediajob` (`ProbeMP4`, `Run`; `Probe` was removed on 2026-10-23)
  and `internal/remux` (`Assemble`, `ConcatTS`, `CheckTS`; the demultiplexer is
  `internal/remux/mpegts`) are pure Go. The daemon depends on them only through its `MediaJobs`
  interface, which `cmd/downexd` fills with the real job (`cmd/downexd/media.go`).
- **Part directory.** Fragments go to `<dir>/<name>.dxparts/<kind>-<track id>/<index>.frag` (kind is
  `video`, `audio` or `muxed`; ids outside `[A-Za-z0-9_-]{1,64}` become `h` + 16 hex digits of their
  SHA-256), written as `.frag.tmp`, synced, then renamed. The output is `<dir>/<name>.dxpart`, renamed to
  `<name>` without replacing an existing file. Before that rename the job records the output (name, size,
  modification time, container) in `<part dir>/output.json`, synced; the daemon removes the part
  directory only after it has stored the completion, and a run that finds the recorded output (or its
  `.dxpart`, which it renames) completes without fetching anything — a completion the daemon failed to
  store is not downloaded again. `Remove` with `delete_file` deletes both; `Resume{restart:true}`
  discards the fragments and checkpoint.
- **Checkpoint.** Per track `{trackId, total, done bitmap, key}`, stored by the daemon in
  `media_checkpoint` (store schema v3), with `durableBytes`, the bytes of the durable fragments
  (0.8.2). The daemon stores those as the download's bytes done, never the bytes of requests in
  flight that the latest progress counted (`Progress.BytesDone`), so a paused stream shows the bytes
  of its stored fragments and a resumed run counts on from them. Initialization segments count as
  fragments (`fragments_total` includes them). A resumed track whose fragment count changed starts over; fragment URLs are not compared
  (a changed manifest with the same ids and counts resumes with the old fragments). Every `Run` re-fetches
  the manifest, which refreshes expired signed URLs.
- **Track keys.** HLS track ids are positions in the master playlist (`v<i>`, `r<i>`), which a server may
  reorder. Each HLS master track therefore also has a key: a digest of its kind and attributes (BANDWIDTH,
  RESOLUTION, CODECS, FRAME-RATE and the rendition groups of a variant; TYPE, GROUP-ID, NAME, LANGUAGE,
  ASSOC-LANGUAGE, CHANNELS, CHARACTERISTICS and INSTREAM-ID of a rendition; not the URI, which may carry
  session tokens), numbered among tracks of identical attributes. The daemon stores the keys with the
  probed tracks and passes those of the chosen tracks with every run, which selects the tracks by key
  wherever the master now lists them (their fragments move along); a key the manifest no longer has fails
  with `media_invalid` instead of downloading another quality. Checkpoints record the key, and a track of
  another key never resumes from it. DASH ids are stable and have no key; checkpoints and stored tracks
  from before keys existed are matched by id. Ids given to separate `AddMedia`/`ProbeMedia` calls (e.g.
  `downex media --list`, then `--video`) are still resolved against the manifest of the later call.
- **Remux memory.** The assembler holds the samples of one movie fragment per input at a time (at most
  4M samples, 16 bytes each), plus the progressive sample tables (bounded by the 4M-sample fragmented
  fallback). The movie fragments of one file may describe at most as much sample data as its `mdat`
  boxes hold, and at most 1M samples of the default size 0 (which occupy no file bytes at all).
- **Requests.** `Cookie`, `Cookie2` and `Authorization` are sent only to the manifest URL's origin;
  `Referer`, `User-Agent` and other headers to every host. Retries: exponential backoff with jitter,
  `Retry-After` honoured, 408/421/425/429/5xx and network errors transient. Error messages never contain
  URLs.
- **Refusals.** Error codes (Download.error_code, and the code of a refused `AddMedia`/`ProbeMedia`, which
  is `InvalidArgument` for a stream that cannot be downloaded and `FailedPrecondition` for a failed fetch):
  `encrypted`, `live`, `media_unsupported`, `media_invalid`, plus the file codes (`http_status`, `network`,
  `tls`, ...). Refusal messages start with `<code>: ` and the code is also sent as the Connect metadata
  `Downex-Error-Code`. Protection found only in an init segment (`pssh`, `tenc`, `encv`, `enca`, `sinf`)
  fails the download with `encrypted` before any media fragment is requested.
- **Not supported yet (`media_unsupported`, refused at probe time where the manifest tells):** a separate
  MPEG-TS audio rendition next to a TS video variant (§5 says "saved alongside"; there is no way yet to
  report a second output file), single-file DASH Representations (`SegmentBase`, with or without
  `indexRange`), WebM/Matroska, raw ADTS/packed audio renditions, fMP4 without an initialization segment,
  and an initialization segment that changes mid-stream (a new `EXT-X-MAP` after a discontinuity,
  multi-Period DASH with different inits). Multi-Period DASH with identical inits is joined as one track.
  *Since 2026-10-23* (`VIDEO-DETECT-SPEC.md` §4.1) a TS video variant with a separate TS AAC rendition is
  supported: both are converted into one MP4. TS with a new `EXT-X-MAP` after a discontinuity is converted
  too (each later map starts a discontinuity domain). The other limits stay.
- **Remux.** fMP4 output is progressive (`ftyp`, `moov`, `mdat`); track IDs renumbered 1..N in input order
  (video first), sample descriptions copied unchanged, text/metadata tracks dropped, tracks aligned on their
  first `tfdt` (a later start becomes an empty edit), negative composition offsets shifted into the edit
  list, timestamp jumps over 10 s treated as discontinuities. Above 4M samples the output is a fragmented
  MP4 instead. A `tfhd` `base_data_offset` is read relative to the fragment file. The source edit list's
  `segment_duration` is ignored (encoder trimming at the end is not applied).
- **Selection.** Without `video_track`/`audio_track` the default pick is `DefaultPair`; for DASH the
  pick prefers Representations that can be assembled. A chosen video track without a chosen audio track
  gets the best audio for it; a muxed track takes no separate audio. `Confirm` with other tracks does not
  re-probe: `fragments_total` is 0 until the download runs, and the extension and container stay as probed.
- **Extension (§6 step 1).** Detection also needs the `webNavigation` permission (top-level navigation
  clears a tab's list); an HLS master and the variant playlists the player fetches are listed as separate
  streams (the daemon accepts either); `<video>` context-menu entries are not offered (players mostly use
  `blob:` URLs). *Since 2026-10-23* a Download button over each visible, meaningful `<video>` of any
  frame lists the frame's streams, its direct MP4 files and the page's yt-dlp qualities instead
  (`VIDEO-DETECT-SPEC.md` §2.5, §2.6, §5.1).
- **Legacy `.ts` records (2026-10-23).** Downloads stored with container `"ts"` before TS→MP4 existed keep
  their `.ts` name and byte-for-byte join (`remux.ConcatTS`, `MediaRunOptions.LegacyTS`) when they have a
  checkpoint; one without a checkpoint (never started, or restarted) runs as an MP4 download under a
  `.mp4` name (`VIDEO-DETECT-SPEC.md` §3.3).
- **Verification.** Unit and package tests, and real-process end-to-end tests
  (`internal/e2e/media_test.go`, generated streams: DASH fMP4 video + audio → progressive MP4 with both
  tracks; HLS master + TS → byte-equal `.ts`; `EXT-X-KEY`, live and `pssh`-in-init refusals; pause, kill,
  restart and resume without re-fetching durable fragments; browser `addMedia` hand-off → awaiting →
  `downex confirm --video` → that track's picture size) pass with `-race` on Windows. Not yet verified:
  playback in real players, a real browser, Linux at run time, inputs over 4 GiB.
  *2026-10-23:* the TS streams of the e2e tests are generated with `internal/testmedia` (H.264 with
  B-frames, AAC, timestamps wrapping past 2³³). An HLS master of TS variants is now converted into
  `episode.mp4`, checked sample by sample with mp4ff. `internal/e2e/videodetect_test.go` covers a
  separate TS audio rendition, discontinuities, early refusals (AC-3, scrambled, AC-3 `CODECS`), legacy
  `.ts` records, pause/kill/resume of a TS download (byte-identical to an uninterrupted run), MP4-only
  manifests and the relay's `probeStream`. All of it passes with `-race` on Windows. Converted files
  have not been played in real players yet.
- **Transfer health (2026-10-28).** Three rules keep a media job moving at full speed:
  - *No data for 20 s.* Every response body is read through one copier (`mediajob` `fetch.go`), shared
    by manifests, HLS/DASH fragments and site-video chunks (`RunRanged`). While the copier waits in a
    read for the next byte, a watchdog measures the wait (polling every 5 s); after 20 s without a byte
    (the engine's stall timeout, `ENGINE-SPEC.md` §5) it closes the response, and the request fails as
    transient (`no data received` in the connection's detail). It is retried like any network error:
    within `MaxRetries` consecutive failures, with the usual backoff, so a server is never hammered.
    Waits on the speed limiters and writes to disk never count as idle, and a slow body that keeps
    delivering (even one byte every few seconds) never trips it. A retried fragment starts over (its
    bytes are taken back from the progress); a retried site-video chunk continues after the bytes it
    already wrote (`SITE-MEDIA-SPEC.md` §2.5, chunk prefixes, 2026-10-29). After a stall,
    the slot (of `Run` and of `RunRanged`) also drops its idle connections (a stalled HTTP/1.1
    connection is closed with its response anyway), so the retry does not reuse a connection that
    may be just as dead.
  - *HTTP/2 health check.* An HTTP/2 connection that receives no frame for 15 s sends a PING and is
    closed when no answer arrives within 15 s more (`transport.HTTPOptions.HTTP2HealthCheck`, default
    `DefaultHTTP2HealthCheck`; negative disables). A black-holed connection therefore fails its reads
    instead of hanging, and is never reused for a retry. It applies to every slot of the shared
    transport (file downloads too); HTTP/1.1 is not affected, and nothing else of HTTP/2 changes (one
    connection per slot, no multiplexing across slots, default flow-control windows).
  - *Warm slots.* `Run` keeps its transport slots for the whole job: the slot that fetched the
    manifest becomes fetch slot 1, and fetch worker *k* of every phase (initialization segments and
    the early TS check, the late TS check, the media fragments) uses slot *k*, created when first
    needed. A slot serves one worker at a time and a job has at most `MaxConnections` slots. A slot
    keeps its idle connections into a phase only when that phase uses it and every host it sent a
    request to (or was redirected to last) since it last dropped them is a host of the phase's
    fragments; every other slot drops them before the phase starts, all slots drop them after the last
    phase (before assembling) and when `Run` returns, however it returns. So the job never holds a
    connection the current phase cannot use, and never more than `MaxConnections` (and the hosts'
    caps) because of the warm slots: when the manifest, the initialization segments and the media
    fragments share a host, their connections fetch media fragments too instead of being dropped
    and dialled again (a two-track TS job over 8 connections opens 8 TCP connections, not 11); a
    manifest or initialization segment on another host, or behind a redirect to another host, costs
    a new connection as before. Host caps, the header policy (credentials only to the manifest's
    origin) and the retries are unchanged.
- **Site media (2026-10-22, `SITE-MEDIA-SPEC.md` §10).** Web pages resolved with yt-dlp reuse this
  job: their direct formats are downloaded by `mediajob.RunRanged` (one preallocated `data` file per
  track in `<name>.dxparts/<kind>-<id>/`, byte-range chunks of at most the host's request length,
  checkpoints `{trackId, total, done, key, chunkBytes}`; since 0.8.2 also the durable prefix of each
  chunk without its bit, `partial`), and a manifest-only page by `Run` on the fresh
  manifest URL with the stored keys. Host rules (connections, speed, `max_request_bytes`) now also apply
  to `Run`, and a media job's full volume fails with `disk_full`.
