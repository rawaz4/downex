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
- `Add` creates the download in state `queued` (or `paused` when `start_paused`) and returns
  at once. The scheduler starts queued downloads FIFO by id while fewer than
  `max_concurrent_downloads` are running.
- `Pause` on a running download cancels its `engine.Run` context (progress committed) → `paused`.
  `Pause` on a queued download → `paused`. `Resume` → `queued` (the scheduler starts it).
  `Resume{restart:true}` calls `engine.Restart` first. Failed downloads can be resumed.
- `Remove` pauses first if running, deletes the record (and the file / `.dxpart` when
  `delete_file`; for a media download also its `<name>.dxparts` fragment directory), emits a `removed`
  event.
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
- `Watch` without ids also sends `tools` (docs/SITE-MEDIA-SPEC.md §3.1): the yt-dlp/Deno status at the
  end of the first batch, then after each change, within the same 4 batches per second.
- `Watch` without ids also sends `update` (field 8, `UpdateInfo`, docs/M6-SPEC.md §4): the result of
  the latest automatic update check, in the first batch once a check has run, then after each
  automatic check. Clients that do not know it ignore it.
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
  malformed or oversized manifest or fragment, or a track it does not have). A refused `AddMedia` or
  `ProbeMedia` is `InvalidArgument` (the stream cannot be downloaded) or `FailedPrecondition` (fetching
  the manifest failed: `network`, `tls`, `http_status`), its message starts with `<code>: ` and the code
  is also sent as the Connect error metadata `Downex-Error-Code`. Messages never repeat the manifest URL.
- Site media (docs/SITE-MEDIA-SPEC.md §3.2) adds `site_unsupported` (InvalidArgument), `site_unavailable`,
  `site_error`, `site_resolving` and `tools_installing` (both: retry), `tools_unavailable` and
  `site_media_off` (FailedPrecondition), and uses `auth_required` for sign-in walls; they map from the
  `internal/sitemedia` sentinels, live and from stored messages. A media job's full volume is `disk_full`.
- Logs keep only scheme, host and path of any URL (user information and query are redacted).
- RPC errors use Connect codes (`NotFound`, `InvalidArgument`, `FailedPrecondition`, `Unavailable`).

## 7. CLI as a client
- `downex add <url> [...]`, `ls`, `pause <id>`, `resume <id> [--restart]`, `rm <id>`, `watch [id...]`,
  `set <id> [--limit rate] [-c n]`, `daemon start|stop|status`. These autostart `downexd` (detached, no console window on Windows)
  when the endpoint is not reachable and wait up to 3 s for it.
- `downex get` keeps working standalone (in-process engine) for scripting.
- Units (owner's rule, 2026-10-31): every size and speed the CLI prints, and the tray tooltip, is in
  decimal units, one decimal: B, KB, MB, GB, TB (PB above), speeds B/s … GB/s; 1 KB = 1,000 B,
  1 MB = 1,000,000 B. Steps happen at 1000 and nothing shows "1000.0" of a unit (999,999 B is
  "1.0 MB"); exact byte counts stay exact ("316.0 MB (316000000 bytes)"). A rate typed after `--limit`
  or `--global-limit` reads K/KB, M/MB, G/GB as decimal (500K = 500,000 B/s) and KiB, MiB, GiB as binary
  (512KiB = 524,288 B/s). Stored values are bytes and bytes per second, so nothing stored changes.
  Limits of the protocol and of the code (above: 1 MiB, 32 KiB, 5 × 5 MiB) stay exact binary sizes.
