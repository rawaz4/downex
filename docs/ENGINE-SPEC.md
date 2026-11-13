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

## 2. Probe and mode selection
1. The first worker sends `GET` with `Range: bytes=0-` and `Accept-Encoding: identity`.
2. `206` with a `Content-Range` whose start is `0` and whose total is known ⇒ **ranged** mode;
   size = total. The first worker keeps streaming from offset 0 on that same response.
3. `200`, a missing/unknown total, or `Accept-Ranges: none` ⇒ **single** mode: one connection,
   no splitting, resume disabled (restart from 0), surface a warning.
4. Record validators from the probe response (strong ETag preferred, else Last-Modified).
5. Filename: `Content-Disposition` (RFC 6266/5987, `filename*` preferred) else last URL path
   segment, then sanitised; never overwrite an existing file silently (append ` (n)`).
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
  again by yt-dlp, so its signed addresses are fresh. `existing_file` says what happens to the
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

## 8. Speed limiting
- Hierarchical token buckets: global → queue → download. Workers call `WaitN` for every read slice
  (16–64 KiB when capped, 64 KiB uncapped; burst ≥ slice). Waits are context-cancellable.

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

## 10. Errors & politeness
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
the connection).
