# Daemon specification (M2)

`downexd` is a per-user background process that owns all downloads. Clients (CLI, UI,
native-messaging relay) talk to it through `proto/downex/v1/downex.proto` (Connect over
HTTP/2 cleartext) on a local endpoint from `internal/ipc`.

## 1. Process model
- One daemon per user: `ipc.AcquireInstanceLock` + exclusive endpoint. A second start exits
  with code 0 and the message "already running" when the existing daemon answers `Status`.
- No services, drivers, elevation or injection. Runs with the user's own rights: `downexd` refuses
  to start elevated (Windows: an elevated split token or System integrity; Linux: set-uid/set-gid)
  unless `--allow-elevated` is given, and the CLI never autostarts it from an elevated process.
- One daemon per database too: `downexd` holds `<db>.locks/daemon.lock`, whatever its endpoint.
- One `engine.Engine` instance shared by all downloads, so per-host connection limits and
  429/503 penalties apply across downloads.
- Resources (2026-10-29/30): on Windows `downexd` runs in the above-normal priority class
  (`platform.RaiseDownloadPriority`), so downloads keep their speed while the processor is busy;
  it uses a few percent of one core. Its Go runtime keeps at most 4 processors and collects at
  half the live heap (`platform.TuneMemory`, unless GOMAXPROCS/GOGC are set) and returns freed
  memory to the system each idle minute. No window may keep working while downloads run
  (`ui/frontend/scripts/check-idle-render.mjs`).
- Logs: `%LOCALAPPDATA%\Downex\logs\downexd.log` (Windows) / `$XDG_STATE_HOME/downex/downexd.log`
  (default `~/.local/state/downex/`), size-rotated (5 × 5 MiB). Never log Cookie, Authorization,
  Proxy-Authorization values or request bodies.
- Store: the same SQLite database as the CLI (`store.DefaultPath()`), overridable with `--db`.

## 2. Endpoint security
- Windows: named pipe `\\.\pipe\downex-<key>`, where `<key>` is 32 random hex digits created once per
  user in `%LOCALAPPDATA%\Downex\endpoint.key` (owner-only DACL, Medium no-read-up label), so the
  name cannot be predicted by other accounts or sandboxed processes. Created with a DACL granting
  access **only to the current user's SID** (and nothing else), `PIPE_REJECT_REMOTE_CLIENTS`,
  first-instance flag. Clients verify the server before sending anything: pipe owner = current
  user, pipe integrity label and server process (user, integrity) not below min(client, Medium);
  otherwise the error is `ipc.ErrForeignEndpoint`. The instance mutex is checked the same way
  (squatted is not "already running"). Overrides must be `\\.\pipe\<name>` with no path
  separators and not `.` or `..`.
- Linux: `$XDG_RUNTIME_DIR/downex/rpc.sock` (fallback `$XDG_STATE_HOME/downex/run/rpc.sock`, default
  `~/.local/state/downex/run/`, never pre-creatable by other users; `/tmp/downex-<uid>/rpc.sock`
  only when that path is too long for a socket address), directory
  mode 0700 owned by the user (refuse to use it otherwise), socket 0600, and an `SO_PEERCRED`
  check rejecting other UIDs.
- `DOWNEX_ENDPOINT` overrides the path (tests, profiles).

## 3. Queue
- Settings (persisted in the store or a `settings.json` next to the DB):
  `max_concurrent_downloads` (default 3, 1–16), `global_speed_limit` (0), `default_dir`
  (the user's Downloads folder), `default_max_connections` (32; 1–32).
- *Polite mode (POLITE-SPEC §7):* Settings 40 `polite_mode` (default false) and 41
  `polite_target_ms` (0 = 30 ms; else 10–200), kept in `settings.json` as the `"polite"` object
  (missing = defaults; an invalid target is repaired to 0 and logged). `StatusResponse.polite`
  (field 20) is Polite mode's status.
- `Add` creates the download in state `queued` (or `paused` when `start_paused`) and returns
  at once. The scheduler starts queued downloads FIFO by id while fewer than
  `max_concurrent_downloads` are running.
- `Pause` on a running download cancels its `engine.Run` context (progress committed) → `paused`.
  `Pause` on a queued download → `paused`. `Resume` → `queued` (the scheduler starts it).
  `Resume{restart:true}` calls `engine.Restart` first. Failed downloads can be resumed.
- `Remove` pauses first if running, deletes the record (and the file / `.dxpart` when
  `delete_file`; for a media download also its `<name>.dxparts` fragment directory), emits a `removed`
  event. *Amended 2026-11-19:* the `<name>.dxparts` directory goes without `delete_file` too (nothing
  resumes it once the record is gone; a failure to delete it then is only logged). A media download
  that fails with nothing resumable (no durable fragment, chunk prefix or head in its checkpoint and
  no `output.json`) has its `<name>.dxparts` deleted when it fails; one with resumable progress keeps
  it until it completes, restarts or is removed. Only that directory is deleted, never a user file.
- Media downloads (M5, docs/MEDIA-SPEC.md §6–§7): `AddMedia` validates like `Add`, then fetches and
  parses the manifest (bounded by 60 s and the caller's deadline) before it records anything; the
  download has `kind = MEDIA`, `size = -1` until it is assembled, and `media` (tracks, selection, format,
  duration, container, title, fragment counts). `ProbeMedia` does the same without recording. The
  scheduler runs media downloads in the same slots, limiter and pause/resume/remove intents as files,
  through the daemon's `MediaJobs`; their checkpoints (per-track fragment bitmaps) are stored as they
  come, so crash recovery and shutdown requeue them where they stopped. `Confirm` may choose other
  tracks (`video_track`, `audio_track`). `Resume{restart:true}` deletes the fragments and checkpoint.
- Site media (docs/SITE-MEDIA-SPEC.md §3.3): `AddMedia`/`ProbeMedia` with `origin = SITE` take a web
  page (`page_url`; `manifest_url` must be empty; of the headers only `Cookie` and `Referer` are used)
  and resolve it with yt-dlp through `Config.Site` once `Config.Tools` has yt-dlp and Deno ready, within
  a wait budget of min(caller deadline − 2 s, 25 s): `tools_installing`/`site_resolving` ask the caller
  to retry while the install or extraction goes on. `ProbeMedia{prefetch: true}` (origin SITE only,
  else InvalidArgument) is the extension's look-ahead of the video being watched (SITE-MEDIA-SPEC §5.6):
  it never installs tools or starts a fallback or rollback trial (`tools_unavailable` at once while
  they are known not to be ready; it waits only for the first discovery of the copies), never waits
  behind or delays a user's request (`site_resolving` at once), and fills the extraction cache the
  following probe, add and run use. The record is one `kind = MEDIA` download whose
  `url` is the page and whose `media` has `origin = site`, the site, every offered MP4 quality (size,
  estimate flag, file type, key) and the pick; its runs stay `probing` until the first progress, and
  `size` is the tracks' total (`BytesTotal`) while running. A `Cookie` is kept in memory only. Settings
  gain the `media` object (`site_media`, `tools_auto_update`, `tools_channel`; missing = defaults true,
  true, "nightly"; an unknown channel is repaired on load and refused by `UpdateSettings`).
  `GetTools`/`InstallTools` report and start the tools' install (InstallTools returns 2 s before the
  caller's deadline while the install goes on); only the user's own daemon (or an e2etest build given a
  release server) downloads tools, every other daemon uses copies on PATH or in its `--tools-dir`
  (default: `tools` next to its database). Host rules gain `max_request_bytes` (0, or 1 MiB–1 GiB) and
  now cap media jobs too; `*.googlevideo.com` has a built-in rule (1 MiB requests, no connection cap) for
  hosts no user rule matches.
- **New links** (docs/ENGINE-SPEC.md §7.2-7.3, docs/DESKTOP-SPEC.md §10). `Add.page_url` (http(s),
  no user information, ≤ 32 KiB, else InvalidArgument) is stored with a file download; `Download.page_url`
  is it, else the request's `Referer` when that names more than an origin (`dlview.PageURL`; media:
  `media.page_url`). A download whose link expired fails with `link_expired` and keeps its data; no new
  state exists.
  - **R1. `RenewLink{id, url | from_download, headers, allow_name_change, restart_if_unproven, start,
    page_url}`**: NotFound for an unknown id; InvalidArgument for a media download ("a stream or web
    page video cannot take a new link"), for a url or headers that fail §4 (GET only), without a link,
    and for `from_download` = id; FailedPrecondition when the download is completed, awaiting
    confirmation, running (also probing: "download %d is running; pause it first"), being removed or
    locked by another process. Allowed: PAUSED, QUEUED (taken out of the queue for the check, put back
    when it is refused) and FAILED with any code (the same proof applies).
  - **R2. Headers.** With `url`: the given headers, else the stored ones without `Cookie`, `Cookie2`
    and `Authorization` when the new URL is on another origin (`engine.RenewHeaders`). With
    `from_download`: that download must be a file download AWAITING_CONFIRMATION and not running;
    its stored request is used and its page replaces the old one when set; after the renewal it is
    removed with its (empty or absent) part file and a `removed` event is published.
  - **R3.** downexd holds the download's operation slot and its cross-process lock for the whole
    proof and renewal (and the slot of `from_download`, taken first); other operations on it wait.
  - **R4.** `engine.ProveLink`, then `engine.Renew`; another name without `allow_name_change` is
    refused (`renew_name`). Success: the entry is read again, queued when `start` (else paused),
    published as `updated`; the response is `{download, restarted, name_differs}`; the log has INFO
    "download renewed" with the id, the new host only, `restarted` and the compared bytes.
  - **Automatic matching** (`renew.go`). **A1.** Adds from the browser, the clipboard or the Add
    dialog (sources BROWSER, CLIPBOARD, UI) of a plain GET are matched while `renew_links` is not
    `off`; never CLI adds, `AddMedia` or site videos. **A2.** Candidates: this daemon's file downloads
    (not foreign, not locked elsewhere) FAILED with `link_expired`, ranged, with durable bytes, not
    running and not in another operation; at most 3, newest first. **A3.** Without network: the add's
    name (its filename hint, else its URL's last segment) without a `" (n)"` variant equals the
    candidate's remote name or its file name without variant (case-folded on Windows and macOS), and
    a size hint, when given, equals the candidate's size. **A4.** With a candidate, Add records the new
    download exactly as without (awaiting or queued, name reserved) and holds it: it is not queued,
    asked about or shown, its queue counts it as work, `renew_check` is true, and Add returns at once
    (the relay answers `added.renewCheck`). **A5.** A goroutine proves the candidates in order with the
    add's request (15 s in all), holding each candidate's slot and lock (a busy one is skipped); the
    first proof that matches wins (a different name counts as no match in `auto`). **A6.** Pause,
    Resume, Remove, Confirm, SetDownloadOptions and RenewLink (also with `from_download`) of the held
    download end its check without a renewal (Remove and Confirm without asking for anything); a
    queued add goes back into its queue at once and the scheduler runs, whatever the operation then
    does. PauseAll pauses a queued held add too.
    **A7. `auto`:** `engine.Renew(candidate, start)`, the add is removed (with its empty part file),
    the candidate is queued, `NoticeLinkRenewed` goes to the tray, and the candidate's progress window
    opens when the add's would have (a hand-off that starts now with `show_progress_window` on).
    **A8. `ask`:** while something can ask the user, the add is stored AWAITING_CONFIRMATION (a queued
    or paused one too; its empty part file goes, its record keeps the name) with an offer
    `renew_offer {download_id, filename, size, received, name_differs}` and the confirmation is
    requested; the offer is in memory only (lost on restart) and dropped when the candidate is
    removed, completed or renewed. **A9.** No match, a timeout or an error: the check ends and the add
    goes on exactly as without it (queued, asked about, its window opened). **A10.** Shutdown cancels
    the checks; a held record persists in its policy state, so after a crash it runs or waits as an
    ordinary add, which never splices.
  - Setting `renew_links` (Settings 28, `settings.json` `desktop.renew_links`: `"auto"` (default,
    written as nothing), `"ask"`, `"off"`; an unknown value is repaired to auto on load and refused by
    `UpdateSettings` with "renew_links must be auto, ask or off").
- **Rescue** (docs/ENGINE-SPEC.md §7.4; `rescue.go`). `Rescue` adopts a partial file a browser left
  unfinished and continues it. It answers within the caller's deadline (the relay's 30 s, the
  window's call timeout).
  - **F1. A partial path** must be absolute and clean, a regular file (not a link or a reparse point).
    For source BROWSER it must end in `.crdownload` or `.part` and lie in the target's folder when a
    target is given; UI and CLI accept any regular file. A stored download's own file or part file is
    refused (`invalid_request`, InvalidArgument; ENGINE-SPEC §7.4 A1).
  - **F2. Only a target T** (BROWSER): `T.crdownload`, then `T.part`, the first regular file wins.
    Else the folder of T is listed (at most 10,000 entries, more is `rescue_ambiguous`) for names
    `Unconfirmed <1-10 digits>.crdownload` that are regular files of at least max(1, bytes_received)
    bytes, at most bytes_received + 16 MiB when that count is known, and modified no earlier than 5 s
    before `started_at_ms` when known: one is the partial, none is `rescue_not_found`, more is
    `rescue_ambiguous`.
  - **F3. Placeholder:** T when the partial is `T.part`, T is a regular 0-byte file, `started_at_ms` is
    known and T was modified no earlier than 5 s before it (removed by the engine, §7.4 A3).
  - **F4.** A permission error (EACCES/EPERM, on macOS also the privacy settings) of any stat or
    listing is `rescue_no_access`; a missing file `rescue_not_found`.
  - **D1.** The partial's folder passes Add's `dir` rules (§4; `default_dir` stays trusted). Rescues of
    one partial path are serialised: a second finds the partial gone (`rescue_not_found`).
  - **D2.** The new download is a file download in mode RANGED with the server's size, `received` =
    the kept bytes, `rescued_bytes` (Download 37; store migration 10), the source sent, the category
    chosen as for Add, in the requested queue at its end, QUEUED, or PAUSED with `start_paused`. It
    is never held for confirmation (the user chose it; `confirm_browser_downloads` does not apply) and
    never matched against expired links. The record exists from the engine's step (b) but is not
    listed or offered until the adoption returns (a List never adopts it as a foreign record).
  - **D3. Progress window.** downexd never opens one for a UI or CLI rescue (the window opens its own,
    §7 of DESKTOP-SPEC). A BROWSER rescue stored paused waits for its first start: `Resume` with
    `rescue_start` (ResumeRequest 6), which downexd accepts only for such a rescue, once, within 10
    minutes of the rescue (FailedPrecondition otherwise; with `restart` InvalidArgument), so the relay
    cannot start other downloads. That start opens the progress window when the download starts at
    once and `show_progress_window` is on, as a browser hand-off does; one that waits in its queue does
    not. The mark is in memory only: after a restart of downexd the rescue is an ordinary paused
    download.
  - **D4. Log:** INFO "download rescued" with the id, the redacted URL, the folder, the name, the kept
    bytes, the size, the windows compared, whether a validator matched, the source and the state; a
    refusal INFO "rescue refused" with the partial's base name and the error without URLs. Never
    cookies or header values.
- On daemon start, downloads left in `downloading`/`probing` (crash or kill) become `queued`.
- `Shutdown` (and SIGINT/SIGTERM/SIGHUP/console close; on Windows also the end of the user session,
  received by a hidden window as WM_QUERYENDSESSION/WM_ENDSESSION, which is held until the daemon
  has stopped; console close and session end use a ~3 s pause budget) pauses every running download, waits for
  commits (bounded, ~10 s), closes the store and exits 0. Paused-by-shutdown downloads are
  re-queued on next start (`queued` state is persisted for them).
- The global speed limit is a parent `limiter.Limiter`; each download's limit is a child.

## 4. Validation of client input (untrusted: the browser relay forwards web data)
- URL: `http`/`https` only, ≤ 32 KiB, must parse with a host.
- Method: GET or POST (others rejected). Body ≤ 1 MiB, only with POST.
- Headers: ≤ 64 KiB total, ≤ 100 entries, no CR/LF, no hop-by-hop or framing headers
  (`Host`, `Content-Length`, `Transfer-Encoding`, `Connection`, `Range`, `If-Range`, …).
- `dir`: absolute, cleaned; created if missing (only below an existing directory). On Windows it
  must be a local drive path `X:\...`: no UNC, `\\?\`, `\\.\` or `\??\` forms, no reserved device
  names, no component ending in a dot or space, no `:` (streams). The system Downloads folder
  (possibly a redirected network share) stays trusted as `default_dir`.
- `filename`: sanitised with `transport.SanitizeFilename`; never contains a path separator.
- `max_connections` 0–32, `speed_limit` ≥ 0. Violations return `CodeInvalidArgument`.
- `Rescue` (§3): `url`, `headers`, `filename`, `max_connections`, `speed_limit`, `queue_id` and
  `page_url` as Add (always GET, no body); source BROWSER, UI or CLI; `partial_path` and `target_path`
  absolute, ≤ 32 KiB, no NUL; BROWSER needs one of them, UI and CLI `partial_path`; `bytes_received`
  ≥ 0, `expected_size` −1 or ≥ 0, `started_at_ms` ≥ 0; `etag` and `last_modified` ≤ 1,024 bytes
  without control characters (a leading `W/` marks the ETag weak). A BROWSER `partial_path` must end
  in `.crdownload` or `.part` and lie in the target's folder.
- A part of a video (`AddMediaRequest.clip`, `ConfirmRequest.clip`; MEDIA-SPEC §8 C1/C2): `start_ms` and
  `end_ms` 0 to 168 h (604,800,000 ms), `end_ms` 0 = to the end, else after `start_ms` by at least 1 s
  (`validate.ClipMs`, shared with the relay); against a known duration it may end at most 1 s after it.
  `chapter` is cleaned like a title and cut to 200 bytes. Chapters of a video (`validate.Chapters`): at
  most 512, titles at most 200 bytes. Refusals are InvalidArgument with code `clip_invalid` (a legacy
  `.ts` record: `media_unsupported`).
- *Polite mode:* `polite_target_ms` 0 or 10–200, else `CodeInvalidArgument` "polite_target_ms must be 0
  or between 10 and 200, got N" (POLITE-SPEC rule 19).

## 5. Events
- `Watch` first sends a `snapshot` for every watched download, then `updated` events, batched
  and coalesced per download to at most **4 batches per second**; `removed` when deleted.
- A slow client must never block downloads: per-subscriber bounded buffer; when full, coalesce
  (keep latest per id) — never block the engine.
- A running download carries `connection_info` (one entry per worker connection, DESKTOP-SPEC §7) in
  Get, List and Watch; it is never stored. `SetDownloadOptions` changes `speed_limit` /
  `max_connections` with the rules of §4 (`FailedPrecondition` for a completed download or one being
  removed); a running download applies the speed limit at once and a file download also the
  connection limit, and the values are saved when its run stops. It also sets `completion_dialog`
  (DESKTOP-SPEC §8), which alone is accepted for a completed download too.
- `Download.file_check` (DESKTOP-SPEC §9): the NAME stage is computed on every Get, List and Watch of a
  download that is not completed and has no EARLY result; the EARLY result (from the run's first
  `engine.Progress.Head`) is kept in memory until the next run starts and published as `updated`; the
  FINAL result is stored when a run completes the download. All of them are left out while
  `check_downloaded_files` is off; changing that setting publishes every download as `updated`. No
  new event kind and no new RPC.
- `Download.renew_check` (a held add, §3 A4) and `Download.renew_offer` (§3 A8) are computed on every
  Get, List and Watch; the end of a check publishes the add as `updated` (or `removed` when it continued
  another download in `auto` mode), and the continued download as `updated`. No new event kind.
- `Watch` without ids also sends `tools` (docs/SITE-MEDIA-SPEC.md §3.1): the yt-dlp/Deno status at the
  end of the first batch, then after each change, within the same 4 batches per second.
- `Watch` without ids also sends `update` (field 8, `UpdateInfo`, docs/M6-SPEC.md §4): the result of
  the latest automatic update check, in the first batch once a check has run, then after each
  automatic check. Clients that do not know it ignore it.
- *Polite mode (POLITE-SPEC §7):* `Watch` without ids also sends `polite` (field 20, `PoliteStatus`):
  the first batch ends with one (after `tools` and `update`), later batches carry one after a
  change, within the same 4 batches per second.
- `started_at` (the first start) and `active_ms` (the running time of all runs, without pauses) are
  stored when a run stops; Get, List and Watch of a running download add the current run. A restart
  (`restart`, also of a completed download, and of a running one: the run it stops is not counted)
  clears both with the progress, so the time starts over with the bytes. The log's
  INFO "download completed" record carries `bytes`, `active_ms` and `avg_bps` (bytes / active time,
  rounded, `dlview.AverageBps`; 0 when no running time is known; a stream's `bytes` is its file's
  size), so whole downloads can be compared. A run
  that completes or fails a download marked `completion_dialog` starts its window once
  (`downex-ui --progress <id>`) while `show_complete_dialog` is on.

## 6. Errors
- Map engine errors to stable `error_code` strings: `validator_changed`, `disk_full`, `tls`,
  `http_status`, `network`, `invalid_request`, `stalled`, `internal`. `validator_changed` means
  "continue only with `Resume{restart:true}`": the remote file changed, or the saved progress no
  longer matches the part file (`engine.ErrCorruptCheckpoint`).
- Media downloads add `encrypted` (encrypted or DRM-protected content), `live` (a live presentation),
  `media_unsupported` (a stream or track combination that cannot be saved yet) and `media_invalid` (a
  malformed or oversized manifest or fragment, or a track it does not have), and `clip_invalid` (a part of
  a video that cannot be saved, MEDIA-SPEC §8 C2). A refused `AddMedia`, `Confirm` (a part) or
  `ProbeMedia` is `InvalidArgument` (the stream cannot be downloaded) or `FailedPrecondition` (fetching
  the manifest failed: `network`, `tls`, `http_status`), its message starts with `<code>: ` and the code
  is also sent as the Connect error metadata `Downex-Error-Code`. Messages never repeat the manifest URL.
- Site media (docs/SITE-MEDIA-SPEC.md §3.2) adds `site_unsupported` (InvalidArgument), `site_unavailable`,
  `site_error`, `site_resolving` and `tools_installing` (both: retry), `tools_unavailable` and
  `site_media_off` (FailedPrecondition), and uses `auth_required` for sign-in walls; they map from the
  `internal/sitemedia` sentinels, live and from stored messages. A media job's full volume is `disk_full`.
- Expired links (docs/ENGINE-SPEC.md §7.2): `link_expired` (stored on a download: the link stopped
  working; the data received so far is kept) from `engine.ErrLinkExpired`, live and from stored messages,
  checked before `http_status`. Refusals of `RenewLink`, never stored: `renew_mismatch`, `renew_size`,
  `renew_changed`, `renew_no_ranges`, `renew_unproven` (from the engine's proof sentinels) and
  `renew_name` (downexd: another name without `allow_name_change`), plus `link_expired`,
  `auth_required`, `http_status`, `network` and `tls` of the proof's requests; all FailedPrecondition with
  the message prefix `<code>: ` and `Downex-Error-Code` metadata. `engine.ErrLinkStale` is
  FailedPrecondition "download %d changed during the check; try again".
- Rescue (docs/ENGINE-SPEC.md §7.4, §3 "Rescue"): refusals, never stored, with the message prefix
  `<code>: ` and `Downex-Error-Code` metadata: `rescue_no_ranges` (the server sends only whole files),
  `rescue_size_unknown` (ranges without a total), `rescue_changed` (other bytes, another size,
  another validator), `rescue_unproven` (too little that tells files apart), `rescue_not_found`,
  `rescue_ambiguous`, `rescue_locked` (another program still writes or holds the partial),
  `rescue_no_access` (FailedPrecondition), `rescue_empty` (InvalidArgument); plus `link_expired`,
  `auth_required`, `http_status`, `network`, `tls` of the proof's requests and `disk_full` of the
  adoption. Messages never contain the URL or a header value; they may name the partial's base name
  and byte offsets ("rescue_changed: bytes 431964160-432029695 differ: …"). A rescued run fails with
  the ordinary codes.
- Logs keep only scheme, host and path of any URL (user information and query are redacted).
- RPC errors use Connect codes (`NotFound`, `InvalidArgument`, `FailedPrecondition`, `Unavailable`).

## 7. CLI as a client
- `downex add <url> [...]`, `ls`, `pause <id>`, `resume <id> [--restart]`, `rm <id>`, `watch [id...]`,
  `set <id> [--limit rate] [-c n]`, `daemon start|stop|status`. These autostart `downexd` (detached, no console window on Windows)
  when the endpoint is not reachable and wait up to 3 s for it.
- `downex get` keeps working standalone (in-process engine) for scripting.
- `downex renew <id> <url> [-H "Name: value"]... [--name-ok] [--restart-if-needed] [--later] [--wait]
  [--local]` calls RenewLink (`--local`: `engine.ProveLink`/`Renew` on the store and runs it, only while
  downexd is not running). Outputs: "Download 12 continues with the new link (4.2 MB of 9.9 MB kept).",
  "Download 12 downloads again from the start with the new link.", renew_name: "downex: error: the new
  link names the file "b.zip"; download 12 is "a.zip". Run again with --name-ok to continue anyway.",
  renew_unproven: "downex: error: too little of download 12 can be compared with the new link. Run again
  with --restart-if-needed to download it from the start."; other refusals print downexd's message.
  `settings --renew-links auto|ask|off`. `downex add` is never matched automatically.
- `downex rescue <partial-file> <url> [-H "Name: value"]... [--name file] [-c connections] [--limit rate]
  [--queue q] [--paused] [--wait]` calls Rescue with source CLI and the partial's absolute path (bound
  2 min). Output: "Rescued 412.0 MB of 1.2 GB from disk.iso.crdownload. Download 57 continues as
  disk.iso."; a refusal prints "downex rescue: <text>" (the rescue codes' shared texts,
  docs/BROWSER-SPEC.md §6, else downexd's message without its code) and exits 1. `info` adds
  "Rescued: 412.0 MB from a partial file".
- Units (owner's rule, 2026-10-31): every size and speed the CLI prints, and the tray tooltip, is in
  decimal units, one decimal: B, KB, MB, GB, TB (PB above), speeds B/s … GB/s; 1 KB = 1,000 B,
  1 MB = 1,000,000 B. Steps happen at 1000 and nothing shows "1000.0" of a unit (999,999 B is
  "1.0 MB"); exact byte counts stay exact ("316.0 MB (316000000 bytes)"). A rate typed after `--limit`
  or `--global-limit` reads K/KB, M/MB, G/GB as decimal (500K = 500,000 B/s) and KiB, MiB, GiB as binary
  (512KiB = 524,288 B/s). Stored values are bytes and bytes per second, so nothing stored changes.
  Limits of the protocol and of the code (above: 1 MiB, 32 KiB, 5 × 5 MiB) stay exact binary sizes.
