# Engine specification (M1)

All sizes are bytes; ranges are inclusive `[start, end]`.

## 1. Model
- A **download** has a request (URL, method, headers, optional body), a target directory and
  filename, a total size (`-1` when unknown), a **mode** (`unknown`, `ranged`, `single`), a state
  and remote **validators** (ETag + whether it is strong, Last-Modified).
- A download is covered by **segments**. Each segment has `start`, `end` (inclusive; `-1` = open-ended,
  only in single mode), `received` (bytes fetched in this session) and `durable` (bytes from `start`
  that are known to be flushed to disk and committed to the store). Invariant: `durable ≤ received`
  and committed state never claims bytes that were not flushed.
- Segments never overlap; their union plus the completed prefix of each covers `[0, size-1]`.
- Expired links (§7.2, store migration 9): a file download records its **page URL** (the page it
  was started from, http(s), ≤ 32 KiB, `""` = unknown; set from the add) and its **remote name**,
  the server's name for the file at the first probe (Content-Disposition, else the URL), before
  any `name (n).ext` variant; `""` before the probe and after a restart (`resetRemote`).

## 2. Probe and mode selection
1. The first worker sends `GET` with `Range: bytes=0-` and `Accept-Encoding: identity`.
2. `206` with a `Content-Range` whose start is `0` and whose total is known ⇒ **ranged** mode;
   size = total. The first worker keeps streaming from offset 0 on that same response.
3. `200`, a missing/unknown total, or `Accept-Ranges: none` ⇒ **single** mode: one connection,
   no splitting, resume disabled (restart from 0), surface a warning.
4. Record validators from the probe response (strong ETag preferred, else Last-Modified).
5. Filename: `Content-Disposition` (RFC 6266/5987, `filename*` preferred) else last URL path
   segment, then sanitised; never overwrite an existing file silently (append ` (n)`). When the
   name chosen this way held bidi embeddings, overrides or isolates that sanitising removed, the
   record's `name_controls` is set (DESKTOP-SPEC §9.5 N1). The probe response's Content-Type
   media type (lower case, without parameters) is stored as `served_type` for the file check;
   a fresh start or restart clears it first.
6. Later segment requests use bounded `Range: bytes=s-e` so a finished connection can be reused.

## 3. Connection budget and ramp-up
- `budget = clamp(remaining / 10_240, 1, perHostMax)`; `perHostMax` default **8**, hard cap **32**.
- The active target starts at **1**. A ramp-up **step** raises it to the whole budget at once
  (1→budget), on the 100 ms tick, only while **every** live worker has received more than 0
  bytes since it started; so the first step comes on the first tick after the probe's first
  bytes (≤ 100 ms after its headers), when ranges are proven and the speed is measured. A
  later step (the budget grew: a raised limit, an expired host penalty) comes at most **once
  per 250 ms**. Resumed downloads ramp up the same way, after their first worker's first bytes.
- Under an active speed cap a step only **doubles** the target (1→2→4→8…), at most once per
  250 ms, so that the 85% rule below stops at the connections the cap needs. So does every
  step after the server pushed back (§10): the target is lowered to the new per-host limit,
  and when that limit rises again (the penalty expired) it grows back by doubling, each step
  waiting until every connection receives, not with a new burst.
- The workers of one step get their segments planned (§4) **before any of them starts**, so
  each requests exactly the range it keeps; the first split of a step that opens several
  connections uses the midpoint (no early first split), so the probe keeps an even share.
- **Worth-it guard**: no step while the download would finish within **max(500 ms, 4·RTT)**
  at its current speed (remaining ÷ speed; RTT = the smallest observed request latency). A
  step needs that speed measured: the decision EWMA (§9) starts with the first 100 ms sample
  that saw bytes, so ticks before the first byte (a resumed download's connect, a server's
  time to first byte) do not count as a slow start that would let a nearly finished
  download open its whole budget.
- Ramp-up is disabled in single mode, and while a speed cap is active and the peak speed is
  already ≥ **85%** of the cap.
- Why at once (measured 2026-10-29 on a ~10 MiB/s line, Google CDN, 32 connections): doubling
  gated on every new connection's first byte took 3.2–4.4 s to have 32 requests out (each
  stage = TCP+TLS + CDN time to first byte of its slowest connection, 0.5–2 s); opening them
  on the probe's headers had them out at 0.35–0.44 s and reached 90% of the line 1.0–1.9 s
  sooner. Politeness is unchanged: the per-host limit (default 32), host rules, 429/503/421
  and refused-connection penalties (§10) and the budget bound every step.

## 4. Split rule (classic profile)
- `minChunk = 11_680`, or `116_800` when the download's current speed exceeds `116_800` B/s.
- `remaining(seg) = seg.end − (seg.start + seg.received) + 1`.
- A free worker may create a new segment only if `max(remaining) ≥ 2·minChunk + 3`
  (or the split is *forced*, see §5).
- Victim = segment with the largest remaining. Refuse when `remaining < 23_363` unless forced.
- `newStart = victim.end − remaining/2` (integer division). Shrink the victim with a
  compare-and-swap of `victim.end` to `newStart − 1`; if the victim's write position has already
  reached `newStart`, cancel the split. The new segment is `[newStart, oldEnd]`.
- **Tail-first** (first split only, for archive/container types: zip, 7z, rar, apk, jar, mp4,
  mov, m4a, mkv by extension or MIME): `newStart = end − 23_360`, or `end − 11_680` when
  `remaining < 46_721`. This fetches central directories / `moov` atoms early.
- **Early first split** (first split, other types): `newStart = min(mid, start + 10·(received + 11_680))`,
  except when the split is one of several made at once by a ramp-up step (§3): then the midpoint.

## 5. Slow, stalled and finished workers
- Slowest worker = the receiving worker with the largest idle time ≥ `max(2 s, 4·RTT)`.
- If nothing is splittable, a free worker **force-splits** the slowest segment (floor ignored).
- **End-game stealing**: when no owner is idle that long, a free worker force-splits (at the
  midpoint) the segment of an owner that is slow but still receiving, with all of these true:
  (1) it is receiving and no speed cap is active; (2) it received its first byte at least
  **1 s** ago; (3) its measured speed is below **¼** of the average live connection (download
  speed ÷ live connections); (4) it would need more than **1 s** for its remaining bytes at its
  own speed. The segment needs at least **46,720** bytes remaining (4·11,680, so the new half
  gets at least 23,360). Idle owners are taken first (largest idle), then the largest such
  ETA. Only a free worker steals (no connection is added for it), and only one that is not
  slow itself: its own measured speed is at least that ¼ of the average, so it is always
  faster than the owner it steals from (a slower stealer would hold the stolen half longer
  than the owner, and once later steals shrank it below 46,720 bytes nothing could take it
  back). A slow free worker still force-splits an idle owner's segment. Single mode never
  splits.
- downexd downloads over **HTTP/1.1** (`cmd/downexd` sets `transport.HTTPOptions.EnableHTTP2`
  false; measured 2026-10-29 on a ~84 Mbit/s line, one 1.33 GiB file in the order 1.1, 2, 2,
  1.1: HTTP/1.1 194.6 s, HTTP/2 204.2 s on average, and HTTP/2's end game ran 1–2 s longer).
  The engine still supports HTTP/2 for other users of the transport.
- HTTP/2 connections are health-checked by the transport: a PING after 15 s without a frame,
  and the connection is closed after 15 s without an answer
  (`transport.HTTPOptions.HTTP2HealthCheck`, default `DefaultHTTP2HealthCheck`). It complements
  the stall timeout below: a black-holed connection is not reused by the slot's next request.
- A worker idle past the **stall timeout** is cancelled; its remaining range is reassigned to a
  new connection (a replacement, §10). Once its current response delivered body bytes the
  timeout is **5 s** (`MidStallTimeout`), at least 4·RTT once a request latency is known and
  twice the longest wait for that response's bytes (its first ones counted from the headers),
  at most the stall timeout: over TLS a read returns whole records (up to 16 KiB), so a slow but
  steady connection below about 3.3 KB/s waits more than 5 s between reads. Before a request's
  first body byte (a slow server's time to first byte) it stays **20 s** (`StallTimeout`). This
  needs no free worker: a connection that goes silent mid-transfer while every other one is busy
  is replaced too (measured 2026-10-29 on a 32-connection download: a silent range sat idle for
  20 s). Idle times are measured on the monotonic clock (durations since the run started), so a
  system clock set forward stalls nothing. Single mode keeps 20 s: it cannot resume a response,
  so the download fails (`ErrStalled`) and starts over. No two workers ever write the same byte
  range.
- A worker that finishes its segment first tries to **take over** the next segment if that segment
  starts at `end+1` and its owner has received 0 bytes; otherwise it requests a new split.
- Shrinking a segment that is being streamed: the owner stops at the new end. HTTP/1.1: drain
  ≤ 64 KiB, else close the connection. HTTP/2: reset the stream, and with more than 64 KiB of
  the response left also retire the slot's connection: the server may already have sent up to
  the 4 MiB stream window (plus its socket buffers) of the reset stream, and the slot's next
  response would arrive only behind it. Measured on a ~10 MiB/s line with 32 connections:
  reused connections waited up to 12.4 s for their next response, and the last 10% of a
  146 MiB file took 6.1 s over HTTP/2 vs 2.4 s over HTTP/1.1 (which closes the connection).

## 6. Disk path
- One target file `<name>.dxpart`, preallocated to `size` (sparse where supported).
- Workers send `{offset, buf}` through a bounded channel to **one writer goroutine** (`WriteAt`).
- The writer flushes (`fdatasync`) every ~**8 s** or **256 MiB**, then commits all segments'
  `durable` offsets in **one** store transaction.
- Completion: final flush, close, atomic rename `<name>.dxpart → <name>` (retry with backoff on
  sharing violations).
- Single mode writes sequentially into the same `.dxpart`.

## 7. Resume
- Reload segments; each restarts at `start + durable`.
- Send `If-Range` with the strong ETag, else Last-Modified. A `200`, a changed size, or a `416`
  on a resumed range ⇒ the remote file changed ⇒ restart from scratch (CLI: ask / flag).

### 7.1 Restart and downloading again
- **Restart** (`Engine.Restart`, `ResumeRequest.restart`) discards the saved progress and remote
  validators; the next run probes and downloads from byte 0 under the reserved name. The reset
  record is stored before the part file is emptied.
- **A completed download** is restarted only on request (`RestartOptions.Completed`; the API: a
  `ResumeRequest` with `restart`, else FailedPrecondition; `existing_file` without `restart` is
  InvalidArgument). It is downloaded again from its address; a video of a web page is resolved
  again by yt-dlp, so its signed addresses are fresh. A part of a video (`MEDIA-SPEC.md` §8) stays
  the part; only what the last run reported of it (keyframe, fetched span) is forgotten. `existing_file` says what happens to the
  file:
  - **Keep both** (`EXISTING_FILE_KEEP_BOTH`, also `UNSPECIFIED`): the file stays and the new copy
    gets the first free variant, `name (1).ext`, reserved at once (files: its `.dxpart` is made with
    `O_EXCL`); the old file then belongs to no download.
  - **Replace** (`EXISTING_FILE_REPLACE`): the record keeps its name and is marked `ReplaceFile`
    (store migration 7, `replace_file`). The file stays untouched while the new copy downloads
    into `<name>.dxpart` (media: fragments, then `<name>.dxpart` assembled); only at completion does
    the part file replace it in one atomic rename (Windows `MoveFileEx` with
    `MOVEFILE_REPLACE_EXISTING | MOVEFILE_WRITE_THROUGH`, POSIX `rename(2)` + folder sync). A
    pause or failure leaves the file as it was and keeps the mark, so a later resume still
    replaces it; the mark is cleared at completion.
  - Only a **regular file** is ever replaced (`engine.Replaceable`: `Lstat`, not a folder, a symbolic
    link, a Windows reparse point or a device), and only one that is plausibly the download's own
    (`engine.OwnFile`, at the restart): it has the size the download completed with, and no other
    download in any state (completed ones included) names it. A completed download whose file was
    deleted may share its name with a later download's file, and the user may have put another
    file there: those are never replaced, and the new copy keeps both. At completion the rename
    replaces only a regular file that no other download names (`engine.MayReplace`; files in
    `promote`, media when the run picks its name); when the name holds anything else, at the
    restart or at completion, the new copy takes a free variant instead (media: the job's final
    rename, `mediajob.placeOutput`, fails with "file exists" rather than replace it). Every media
    path (manifest `Run`, a web page's `RunRanged` join and copy) publishes through `placeOutput`.
  - When the file is gone, there is nothing to keep or replace: the download runs again under its
    own name (a variant only if another download claimed the name meanwhile).
- Restarting a download that is not completed ignores `existing_file`: it keeps the choice it was
  restarted with.

### 7.2 Expired links
Applies to file downloads (`KindFile`); site videos keep their own refresh (SITE-MEDIA-SPEC §2.6)
and manifest streams are out of scope (MEDIA-SPEC §10).
- **E1. Classification** (`isExpiry`, the one classifier; checked after the pinned target's
  fallback of `pin.go`, so a pinned target's first 401/403/404/410 still retries the original URL
  once, and before the other status checks). A response says the link expired when:
  - (a) its status is 403 or 410;
  - (b) its status is 401 **without** any `WWW-Authenticate` challenge (with one it stays
    `ErrAuthRequired` / `auth_required`);
  - (c) the request was redirected (the final URL differs from the one sent, compared after
    parsing) and the final status is 404 (a plain 404 stays `http_status`: the file was removed);
  - (d) its status is 200 with `Content-Type` `text/html` or `application/xhtml+xml`, answering a
    range request of a ranged download (a segment, resumed or not). This amends §7: a 200 to a
    resumed range was "the remote file changed"; a 200 web page is now an expiry;
  - (e) the probe of a fresh download was redirected and its 2xx answer is a web page, for a
    download that is not an HTML file (its reserved or proposed name has an extension outside
    htm, html, xhtml, shtml, mht, mhtml and its MIME hint is not an HTML type; a download without
    a name yet never uses (e)).
  A 206 is never an expiry (its `Content-Range` is checked as usual).
- **E2. Error.** `ErrLinkExpired` ("engine: link expired") is permanent: in `isTransient`'s
  permanent list, never a throttle, never a refusal. Messages name the range and the answer,
  never the URL: `bytes %d-%d: HTTP 403 Forbidden: engine: link expired`,
  `bytes %d-%d: a web page (text/html) instead of the range: engine: link expired`,
  `probe: redirected to a web page (text/html): engine: link expired`.
- **E3. Ranged run.** The worker whose request is an expiry stops at once without retrying; its
  range keeps its durable prefix and becomes unowned. The run sets its `expired` flag: from then
  on no worker starts, no segment is split, no takeover happens, no stalled connection is
  replaced, and every worker checks the flag before each request (`serve`), ending without
  failing the download. Responses already streaming continue to the end of their range or until
  they break. When no worker is live, the download fails with the first expiry error and
  `abandon` commits the durable segments as for any failure; if the live streams finished every
  range, it completes normally.
- **E4. Single mode and the fresh probe.** An expiry fails the run at once; Run's single-mode
  restart loop does not retry it (permanent). Single mode keeps nothing (it cannot resume).
- **E5.** `fresh` records the remote name before it reserves the file name; `resetRemote` clears
  it.

### 7.3 Renewing a link
- **P1. `Engine.ProveLink(ctx, id, req, opts) (LinkProof, error)`** holds the download like
  Restart (`ErrAlreadyRunning` while it runs). It refuses with `ErrInvalidRequest` a media or
  completed download and a request other than a plain GET of an http(s) address ("only a GET link
  can continue a download"). It sends nothing and answers **Fresh** (nothing to splice: the
  renewal downloads from byte 0) when the download is not ranged, its size is unknown, it has no
  durable bytes, or its part file is missing or of another size.
- **P2. What is compared.** The durable runs `[Start, Start+Durable-1]` of the stored segments,
  adjacent runs merged, total T. **Full proof** when T ≤ 1 MiB in ≤ 8 runs: one request per run,
  every durable byte. Otherwise a **sampled proof** of N windows of ≤ 65,536 bytes, each inside one
  run: N = 4 when the new server's first answer carries a strong ETag equal to the stored strong
  ETag ("the server vouches"), else 8. In order: (1) the last 64 KiB of the run that starts at 0
  (the boundary), (2) the last 64 KiB of the run with the largest end, (3) the first 64 KiB of the
  run that starts at 0, (4..N) random windows: an offset drawn with `crypto/rand` uniformly over
  the durable bytes, aligned down to 4,096 (not below its run's start), clipped to its run. A
  window an earlier one contains is dropped (a random one is drawn again at most twice). The pure
  function `proofWindows(runs, n, rand)` holds this rule (`proof.go`, the one proof primitive).
- **P3. Requests.** `Range: bytes=a-b`, no `If-Range`, through a transport slot of the engine's
  factory (the same proxy, logins and host rules), ≤ 10 s each, sequential, at most 8, stopping
  at the first failure; the whole proof is bounded by the caller's context (downexd: 15 s for an
  automatic check, the RPC deadline for RenewLink). Every answer must be 206 with
  `Content-Range` exactly `a-b/total` and exactly `b-a+1` body bytes (read with that cap). An
  expiry (E1) is `ErrLinkExpired`; a 401 with a challenge or a 407 `ErrAuthRequired`; a 200 or
  another range `ErrLinkNoRanges`; another total `ErrLinkSize` ("%d bytes, expected %d"); any
  other status a `*StatusError`; transport errors as usual.
- **P4. Validators.** All answers of one proof must carry the same validators, else
  `ErrLinkChanged`. They are compared with the stored ones (as §6's `compareValidators`) only when
  the new final URL has the same origin as the stored final URL (else the stored request URL):
  a difference there is `ErrLinkChanged`. Across origins (mirrors, new signed CDN hosts) they are
  not compared.
- **P5. Name.** The first answer's name (Content-Disposition, else the URL) is compared with the
  stored remote name, else with the file name without a `" (n)"` variant (`StripVariant`),
  case-folded on Windows and macOS. A difference sets `LinkProof.NameDiffers`; it is not an error
  of ProveLink (callers decide).
- **P6. Bytes.** The part file is opened read-only and each answer is compared with `ReadAt` over
  `[a, b]`; the first difference is `ErrLinkMismatch` ("bytes %d-%d differ"). Nothing is written.
- **P7. Result.** A full proof matches when every run is equal **and** its runs that are not one
  repeated byte value hold at least min(4,096, T) bytes (durable bytes that are all zeros, such
  as an image's zeroed start, prove nothing). A sampled proof matches when every
  window is equal **and** at least 4,096 compared bytes lie in windows that are not one repeated
  byte value (a zero-filled hole proves nothing); otherwise `ErrLinkUnproven`, or Fresh with
  `ProveOptions.RestartIfUnproven`. The proof keeps a stamp of the record it was made for (size,
  validators, durable sum, segment count, state, `UpdatedAt`).
- **P8. `Engine.Renew(ctx, id, proof, start)`**, under the same hold, re-reads the record and
  refuses with `ErrLinkStale` when its stamp differs. Otherwise it stores the new request (GET, no
  body), the new final URL and the new server's validators (always replaced: the old ETag sent as
  `If-Range` to a new server would turn every range into a 200), clears the error and stores the
  download queued (`start`) or paused, in one update; the segments, mode, size, name, remote name,
  `ReplaceFile` and times are kept. A Fresh proof swaps the request, then does exactly Restart's
  reset (§7.1; the record is stored before the part file is emptied).
- **P9. Sentinels** (permanent, wrapped with `%w`): `ErrLinkMismatch`, `ErrLinkSize`,
  `ErrLinkChanged`, `ErrLinkNoRanges`, `ErrLinkUnproven`, `ErrLinkStale`. downexd maps them to the
  `renew_*` refusal codes (DAEMON-SPEC §6).
- **Limits of the proof.** A full proof compares every durable byte. A sampled proof compares at most 256 KiB
  (vouched) or 512 KiB of the durable data: another version of the same name and size that
  differs only in bytes no window covered could pass, though the boundary, head and random windows
  make an accidental match very unlikely and a server cannot predict the random ones; an
  origin-equal validator change refuses. It is not a whole-file checksum (REQUIREMENTS N4 note).

### 7.4 Adopting a partial file (rescue)
A download another program (a browser) left unfinished is continued from the bytes it saved:
`Engine.Rescue(ctx, RescueInput)` proves that the address serves the same file, then renames the
partial file into a new ranged download's part file. Nothing on disk changes before the proof
succeeds. Callers: downexd's `DownloadService.Rescue` (DAEMON-SPEC §3 "Rescue"), from the browser
relay (BROWSER-SPEC §6), the window's "Rescue partial file…" (DESKTOP-SPEC §11) and
`downex rescue`.
- **V1. Request.** A plain GET of an http(s) address without body (else `ErrInvalidRequest`);
  headers as for Add (DAEMON-SPEC §4). Every request is a bounded `Range: bytes=a-b` without
  `If-Range` (a changed file must read as changed, not as "no ranges"), through one transport
  slot of the engine's factory (proxy, logins, host rules, request caps), ≤ 10 s each
  (`ProofRequestTimeout`), sequential. A transient failure is retried after the engine's
  back-off (Retry-After honoured), at most 3 requests per window. Answers are classified by the
  proof primitive `fetchWindow` (§7.3 P3): an expiry (§7.2 E1) is `ErrLinkExpired`; a 401 with a
  challenge or a 407 `ErrAuthRequired`; a 200 or another range `ErrLinkNoRanges`; a range
  answered with an unknown total (`bytes a-b/*`) `ErrSizeUnknown` (it also matches
  `ErrLinkNoRanges`); a 416, a total other than the browser's, or one too short to hold the
  window `ErrLinkSize`; any other status a `*StatusError`.
- **V2. Trusted length L.** s = the partial's size (`Lstat`: a regular file, never a link, a
  folder, a device or a Windows reparse point). L = s, or the browser's count of good bytes
  (`Trusted`) when 0 < count < s: the bytes beyond it are discarded. A partial larger than the
  browser's count may hold holes (a browser writing slices at several offsets counts only what
  it received): its L is further cut to the start of its first 64 KiB-aligned block of 65,536
  zero bytes. L = 0 is `ErrPartialEmpty`.
- **V3. Windows.** L ≤ 1 MiB (`FullProofMaxBytes`): one window, every byte of [0, L) (a full
  proof). Otherwise the sampled windows of `proofWindows` (§7.3 P2) over the one run [0, L): the
  last 65,536 bytes before L first, then the first 65,536, then random windows (offsets from
  `crypto/rand`, aligned down to 4,096), 8 in all; 4 when the first answer carries the strong
  ETag the browser saw (§7.3 "vouched") and the partial may not hold holes. With the torn-tail
  retry of V5 (a second proof) at most 16 windows, 1 MiB in all.
- **V4. Validators.** The first answer fixes the server's total (it must equal the browser's
  total when that is known, else `ErrLinkSize`) and its validators. A validator the browser saw
  that the answer carries with another value (ETag compared opaquely, RFC 9110 weak comparison;
  Last-Modified as a date) is `ErrLinkChanged`; a validator the answer omits is not a change.
  Later answers must carry the first answer's validators (else `ErrLinkChanged`).
- **V5. Bytes.** Each answer is compared with the partial's bytes (`ReadAt` from a read-only
  handle); a difference is `ErrLinkMismatch` ("bytes a-b differ"). **Torn tail:** when only the
  first window of a sampled proof differs, the last bytes the browser wrote may not have reached
  its disk: the proof runs once more with L2 = ⌊(L − 4 MiB) / 4096⌋ · 4096 when L2 ≥ 65,536 (then
  L = L2), else the mismatch stands.
- **V6. Informative bytes.** As §7.3 P7: a sampled proof needs at least 4,096 compared bytes in
  windows that are not one repeated byte value, a full proof min(4,096, L); otherwise
  `ErrLinkUnproven` (zeros prove nothing).
- **V7. Stability.** The partial's (size, mtime) are read before the first request and again
  just before the rename: a change means another program still writes it, `ErrPartialLocked`.
- **V8. Kept from the proof.** The first answer's validators (the server's own, used as
  `If-Range` from then on), final URL, total, content type (`ServedType`) and name
  (Content-Disposition, else the URL: `RemoteName`, so an expired link can be renewed later,
  §7.3).
- **A1. Folder.** The download is saved in the partial's own folder (same volume: the move is
  a rename, never a copy). downexd checks the folder as Add's (DAEMON-SPEC §4). A partial that a
  stored download names as its file or its part file (`<name>.dxpart`; names compared as the file
  system does) is refused with `ErrInvalidRequest` before any request: adopting it would take the
  file from under that download.
- **A2. Name.** The requested name (sanitised), else the partial's base name without a
  trailing `.crdownload` or `.part` (case-insensitive); a provisional `Unconfirmed N.crdownload`
  (or a name empty without its suffix) gives way to the browser's final path's base name, else
  the server's name. A name taken by another file gets the usual variant, `name (1).ext`.
- **A3. Placeholder.** Firefox keeps a 0-byte file at the final path while it writes
  `<name>.part`: when the caller names it (`Placeholder`, in the partial's folder), it is
  removed before the name is reserved, only while it is still a regular file of 0 bytes.
- **A4. Order of writes.** (a) reserve the name: `<name>.dxpart` created empty with `O_EXCL`;
  (b) create the record (state PROBING, mode unknown, size −1) and hold its id like a run
  (`ErrAlreadyRunning` for anything else, so a list that adopts records and a user's pause or
  remove cannot act on it); (c) check V7, then rename the partial onto `<name>.dxpart` (Windows
  `MoveFileEx` REPLACE_EXISTING|WRITE_THROUGH, retried for about 7 s on a sharing violation,
  then `ErrPartialLocked`; POSIX `rename(2)`), and sync the folder; (d) cut the part file to L,
  extend it to the server's size (`Preallocate` keeps [0, L) on every OS), sync; (e) commit in
  one `SaveCheckpoint`: mode RANGED, the size, validators and final URL of V8, `RescuedBytes` =
  L, state QUEUED (PAUSED when asked), one segment [0, size−1] with L durable bytes.
- **A5. Rollback.** A failure after (a) removes the reserved part file, or, after (c), renames
  it back to the partial's path (cut to L) when that path is free, and deletes the record; the
  caller sees the cause (`ErrDiskFull` from (d) as `disk_full`).
- **A6. Crash.** Between (b) and (e) a crash leaves a PROBING record: the next start downloads
  it from byte 0 (the rescue is lost, never corrupted). (e) is the commit point.
- **A7.** L = size is allowed: every byte is durable, and the first run completes at once.
- **A8. Afterwards.** The rescued download is an ordinary ranged download: resume follows §7
  (`If-Range` with the server's validators; a 200, a 416 or another size ⇒ `validator_changed`).
  `RescuedBytes` is kept after completion and cleared by every reset of the remote state
  (`resetRemote`: Restart, and a fresh start after a crash).
- **Sentinels** (permanent): `ErrSizeUnknown`, `ErrPartialEmpty`, `ErrPartialLocked`, with the
  proof's `ErrLinkMismatch`, `ErrLinkSize`, `ErrLinkChanged`, `ErrLinkNoRanges`,
  `ErrLinkUnproven`; downexd maps them to the `rescue_*` codes (DAEMON-SPEC §6).
- **Limits.** As §7.3's: a sampled proof compares at most 512 KiB of the partial, so another
  version of the same name and size that differs only outside the windows could pass; strong
  validators, the browser's total and every later range's `If-Range` narrow that further. A
  server with unknown totals (`bytes a-b/*`) is refused: continuing a single-connection
  ("chained", §2) download from an offset is not supported.

## 8. Speed limiting
- Hierarchical token buckets: global → queue → download. Workers call `WaitN` for every read slice
  (16–64 KiB when capped, 64 KiB uncapped; burst ≥ slice). Waits are context-cancellable.
- *Polite mode (POLITE-SPEC §5):* the daemon puts a polite level between global and queue
  (global → polite → queue → download). The engine sees it only through `Limit()`, like any user
  limit (capped slices, ramp-up stopping at 85 %); no engine behaviour changes. "Polite mode" is
  not §10's politeness (the host penalty).

## 9. Timing & events
- Speeds are recomputed every 100 ms; progress events are emitted at most 4 Hz; checkpoints
  every ~2.5 s (bounded by the flush rule in §6). No goroutines/tickers for idle downloads.
- Decisions (ramp-up's worth-it guard and 85% rule, minChunk, end-game stealing) use an EWMA
  with a **1 s** time constant, per download and per connection, starting with the first
  sample that saw bytes.
- The **shown speed** (`Progress.SpeedBps`, each connection's `WorkerView.SpeedBps`) is the one
  every client displays and divides the remaining bytes by for its ETA; the daemon forwards it
  unchanged. It is an EWMA whose time constant is `clamp(t/4, 200 ms, 5 s)`, t = time since
  the current run of bytes began: it follows a ramp-up as it happens and then holds steady
  through the line's own noise. A change that lasts is followed within about a second: when a
  0.5 s EWMA of the same bytes (the detector) has been more than **25%** from the shown speed
  for **1 s**, the shown speed moves with a **1 s** time constant until the two are within
  12.5% again (a limit, a lasting dip, a recovery). 2 s until 2026-10-29 (replayed then on 60 s
  of real-line timelines: CV 0.068 / 0.192 / 0.082 vs 0.090 / 0.215 / 0.107 with a 1 s EWMA); the
  owner saw 2 s wander 6–9 MiB/s on a line averaging 8, and a ±20% wobble of 2 s at a time is
  now shown within ±6% (`TestShownSpeedHoldsThroughWobble`; 2 s: ±9%). A reference download
  manager, measured the same evening, shows its rate about 8% above the line's (1.08×); Downex
  shows the line's (0.97×). Bytes are folded in over the whole gap since the previous bytes and the value
  holds in between, so a slow connection whose bytes arrive in TLS records seconds apart shows
  its rate rather than a saw-tooth through 0. The first bytes, and the bytes after a stall
  (more than **4 s** without bytes), show their real rate at once; bytes after a shorter
  silence show their rate over it. It shows **0** once no bytes came for its silence:
  `max(floor, 2.5 × the recent gap between bytes)`, at most **10 s**, where the floor is
  **1 s** (under a speed cap: the gap its pacing explains, 2 capped slices per connection
  sharing it, if longer) and the recent gap is the longest gap between bytes, forgotten
  over 2 s. It shows 0 at once when the download stops (paused, failed or complete).
- **Head** (`Progress.Head`, for the file check of DESKTOP-SPEC §9): the file's first
  min(`HeadBytes` = 65,536, size) bytes (fewer only when a single-mode download of unknown size
  completed shorter: then the whole file, read at completion). It is set in exactly one event per
  run, the first after those bytes were **written** to the part file (not a durability claim:
  the writer reads them back once its written spans cover [0, n)); a resumed run whose durable
  segments already cover them reads them from the part file for its **first** event; a run that
  stops before then has none. Every other event carries nil. The writer adds one comparison per
  write and one read per run; the hand-over to the coordinator never blocks the writer.

## 10. Errors & politeness
- *Glossary:* the per-host cap lowered below is the **host penalty** (`Engine.HostPenalized`
  reports it by host name); it is unrelated to Polite mode (POLITE-SPEC), which sends no TCP
  probes to a penalized host.
- Typed sentinel errors (e.g. `ErrRangeIgnored`, `ErrValidatorChanged`, `ErrTLS`, `ErrDiskFull`).
  Partial progress is always preserved.
- Honour `Retry-After` on 429/503; reduce the per-host cap on 429/503/421, and on a request
  whose connection was refused, reset or closed before any response while another connection
  of the download receives (a server at its per-client connection limit; timeouts and name
  or route failures do not count). Each such response lowers the cap to the live connections
  less the refused one, for `HostPenaltyTTL` (5 min), and stops the refused connection at
  once when more are live than allowed: its range goes back to the pool, it does not retry.
  A wave of refusals (a ramp-up step beyond the server's limit) thus ends at the connections
  the server serves; a burst of 32 against a server that serves 4 costs the 28 refused
  requests once, then the 4 carry the download. A request that follows a response served to
  the same worker, or replaces a connection the download just closed, adds no connection:
  the worker was served a response since its last request (finished, closed with bytes
  left, or broken), or it was started for a worker cancelled with a request out (a
  takeover, a stall: either timeout of §5). The server may still count the old response (a
  finished one a moment longer: the client can read its last byte before the server has
  finalised it, a load balancer counts with a lag; an abandoned one until the server
  notices), so the first refusal of such a request is retried (after its Retry-After)
  without lowering the cap; a refused retry counts. Otherwise every connection that moves on
  to its next range, and every replacement, at the server's limit would cost a connection
  for 5 minutes: against a server that serves 3 and answers 429 beyond, a download allowed
  5 went 5 → 4 → 3 → 2 → 1 and finished at 1 (measured 2026-11-10 on a local server that
  counts a finished response 300 ms longer: 3.1 MiB/s, 4.7–4.8 MiB/s with the cap kept at
  3; the Mac saw the same descent against OVH's speed-test server, not re-measured since).
- Never downgrade TLS or disable certificate verification.
- `ErrSizeUnknown`, `ErrPartialEmpty` and `ErrPartialLocked` (§7.4) are permanent refusals of a
  rescue; nothing on disk changed when they are returned.
- `ErrLinkExpired` (§7.2) is permanent and is not a throttle: an expiry stops new requests but
  lets the responses already streaming finish their ranges; a per-connection 403/410 therefore no
  longer fails the download at once.

## 11. Conformance tests
Table-driven tests assert the constants above (11,680 / 116,800 / 23,363 / 2·minChunk+3 /
tail 23,360 vs 11,680 at 46,721 / ramp-up to the budget at once, doubling under a cap, later
steps at most every 250 ms, doubling after a pushback / worth-it guard 500 ms on a measured
speed / 85% / shown speed 200 ms–5 s, following a lasting 25% change after 1 s, 0 after max(1 s, 2.5 recent gaps) without bytes, at most
10 s / end-game stealing ¼, 1 s first byte, 1 s ETA, 46,720, no stealer slower than ¼ /
refused connections lower the per-host cap like 503, a refused next request after a served
response does not / stall 5 s after body bytes (at least
twice the response's longest wait for bytes), else and in single mode 20 s / flush 8 s or
256 MiB)
against a fake clock and an `httptest` range server with fault injection (slow, stalled,
200-only, ETag change, 416, 429, a per-client connection limit answering 503 or 429 or closing
the connection, signed links that expire: 403, 410, 401 without a challenge, a redirect to an
HTML page).
Expired links (§7.2-7.3): the classification table of E1 (`TestIsExpiry`); an expiry mid-download
keeps the durable bytes, sends no request after the first refusal beyond those in flight and lets
live streams finish; the proof constants 65,536 / 1 MiB / 8 runs / 4 or 8 windows / 4,096
informative bytes / 10 s per request; the window rule (`TestProofWindows`); match on another
origin, `ErrLinkChanged` on the same origin, `ErrLinkMismatch`, `ErrLinkSize`, `ErrLinkNoRanges`,
`ErrLinkExpired`, `NameDiffers`, `ErrLinkUnproven` on flat data, Fresh without a part file,
`ErrLinkStale`; an expire → renew → resume cycle byte-exact over 20 seeds.
Rescue (§7.4): the last window first and no `If-Range` while proving; 65,536-byte windows, at
most 8 (4 vouched), one torn-tail retry 4 MiB lower (4,096-aligned), a full proof up to 1 MiB;
the browser's count and holes cut L; every refusal (another file, another ETag or date, no
ranges, 416, unknown total, another total, a shorter file, an expiry, flat data) leaves the
partial, the folder and the store as they were; the placeholder, a taken name and a provisional
name; a partial written during the proof is `ErrPartialLocked`, also a Windows handle without
`FILE_SHARE_DELETE`; the record is held during adoption; kill points (a PROBING record runs
fresh, a failure after the rename renames back); a rescued run fetches nothing below L and is
byte-exact; Restart clears `RescuedBytes`.
