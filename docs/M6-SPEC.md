# M6 specification — queues, scheduler, network, desktop integration, updates

All features work on Windows and macOS (Linux: compile + logic, platform hooks may be stubs).

## 1. Queues, scheduler, post-queue actions
- **Main queue** (id 1, `is_main`, cannot be deleted) replaces the implicit FIFO: every download belongs to
  exactly one queue (`Download.queue_id`). Existing downloads migrate to the main queue in their id order.
  The main queue is **running** by default (today's behaviour: queued downloads start automatically).
- **Other queues** are created by the user; they process only while running (Start/Stop or schedule).
  "Download later" in the confirmation dialog puts the download into the chosen queue in state `queued`
  but nothing starts until that queue runs (paused downloads stay paused).
- **Admission:** a queued download starts when its queue is running, the queue has fewer than
  `max_concurrent` (0 → global `max_concurrent_downloads`) running, and the global cap allows it. Order =
  the queue's `download_ids` order. Global cap applies across all queues.
- **Limits:** queue `speed_limit` is a limiter child of the global limiter; downloads are children of their
  queue's limiter (global → queue → download). *Polite mode (POLITE-SPEC §5):* queue levels hang below
  the polite level, itself below the global limiter (global → polite → queue → download). A download's limit as the engine sees it (slice size,
  ramp-up) is the tightest one of that chain; moving a running download to another queue (also by
  deleting its queue) puts it under the new queue's limiter at once.
- **Schedule** (local time, DST-safe: compute with `time.Date` in `time.Local` each tick): start at
  `start_minute` on the days in `weekdays` (or once on `once_date`), stop at `stop_minute` when
  `stop_enabled` (window may cross midnight). `repeat_minutes` > 0: while inside the window, every N
  minutes re-queue failed/unfinished downloads of the queue (sync mode). The scheduler sleeps until the next
  event (no polling faster than the next event; re-evaluate on resume from sleep and on clock changes).
  Missed starts while the machine was off/asleep run on resume if still inside the window. While any
  schedule is enabled the scheduler looks again at least every 15 min; a `once_date` is found however far
  ahead it is. A persisted schedule mark more than 1 min ahead of the clock means the clock was set back:
  the mark is dropped and the state derived again from the latest event at or before now.
  `wake_computer` (Windows): a waitable timer with resume=true for the next start while downexd runs.
- **Keep awake:** while a countdown runs, or a queue with a post action or an enabled schedule has
  running (or, while it runs, waiting) downloads, the user's own daemon keeps the system from idle sleep
  (Windows `PowerCreateRequest`/`PowerSetRequest(PowerRequestSystemRequired)`; macOS
  `caffeinate -i -w <pid>`; others unsupported). Other downloads leave idle sleep alone.
- **Queue finished** = it was running and has no queued/running downloads left (downloads awaiting
  confirmation, and downloads an operation is holding for a moment, count as work), at least one download
  completed during this run. Only a run that completes or fails can finish a queue: pausing, removing or
  moving downloads never does. → if `post_action` ≠ unspecified: emit `PostActionPending{deadline =
  now+60 s}` (Watch event; tray/menu-bar notification "Shutting down in 60 s"; the UI shows a countdown
  with Cancel; new work for the queue, including a download awaiting confirmation, cancels it) → after the
  deadline perform it unless `CancelPostAction` was called. The deadline is wall-clock time: a countdown
  found more than 15 s past it (the machine slept) is cancelled, not performed, also when the scheduler
  looks again after resume or a clock change; a power action that is performed cancels every other
  pending countdown, so one resume performs at most one. QUIT_DOWNEX = graceful shutdown;
  SLEEP/HIBERNATE/SHUTDOWN via platform: Windows `SetSuspendState` / `InitiateShutdownW`
  (SeShutdownPrivilege); macOS sleep `pmset sleepnow`, shutdown via `osascript -e 'tell application
  "System Events" to shut down'` (Automation permission prompt), hibernate unsupported on macOS
  (validation refuses it there); Linux logind D-Bus (stub). Never force-close other apps.
- **Store:** queues table (migration 5) with schedule/post_action, download order; downloads.queue_id.

## 2. Network
- **Proxy** (`Settings.proxy`): SYSTEM (default) — Windows: WinHTTP `WinHttpGetIEProxyConfigForCurrentUser`
  incl. auto-detect/PAC via `WinHttpGetProxyForUrl` (a WinINET `socks=` entry is SOCKS4); macOS:
  `scutil --proxy` parsing (no cgo) for HTTP/HTTPS/SOCKS/PAC/exceptions; Linux: environment variables; a
  system SOCKS proxy serves only the schemes without a proxy of their own. NONE. MANUAL: http/https/socks
  (socks5, socks4, socks4a; SOCKS, when set, serves both) with bypass list (`<local>`, wildcards).
  Proxy URLs (`http_proxy`, `https_proxy`, `socks_proxy`) and `pac_url` never contain user information,
  in any mode: such a value is refused (the error names the field, never the value) and removed when
  read from an old settings file.
  PAC: fetch `pac_url` (≤ 1 MiB, http/https/file), evaluate `FindProxyForURL(url, host)` with the standard
  PAC helpers (isPlainHostName, dnsDomainIs, localHostOrDomainIs, isResolvable, isInNet, dnsResolve,
  myIpAddress, dnsDomainLevels, shExpMatch, weekdayRange, dateRange, timeRange) in an isolated goja
  runtime inside a helper process (downexd started again with `DOWNEX_PAC_HELPER`, so a hostile script
  cannot exhaust the daemon's memory: Windows job object ≤ 1 GiB commit, heap watchdog 256 MiB; a call
  that overruns its time is ended by killing the helper; the next helper starts after 1 s, doubling to
  60 s, reset on success; answers ≤ 64 KiB), 1 s timeout per call, result cache per host for 5 min (in the
  daemon); a script that fails means direct connections. For https/wss targets the script sees only
  `scheme://host[:port]/` (no path or query); http targets keep path and query, never user information or
  fragment. `shExpMatch` is false for a string over 16 KiB or a pattern over 4 KiB and never backtracks.
  Parse "PROXY h:p; SOCKS h:p; DIRECT" lists (≤ 32 entries) with fallback order: a route that cannot be
  reached is skipped, and tried last for 5 min.
  Proxy auth: Basic, plus NTLM/Negotiate for proxies on Windows via SSPI (current user, no password) and
  NTLM via go-ntlmssp with the stored password elsewhere. A 407 is answered only when it comes from the
  HTTP proxy that forwarded the request (a plain-http target) or to a CONNECT of that proxy; a 407 from an
  origin (direct, or inside a tunnel) is never answered. `Proxy-Authorization` goes only on requests
  forwarded through, or CONNECTs to, the proxy it is for; never on a redirect hop to an origin. Proxy
  logins are given only for proxies the resolver in effect returned (never in NONE mode): MANUAL gives
  the stored password only to the configured proxy; SYSTEM/PAC proxies get only the current-user login
  (Windows).
- **Server auth:** on 401 with `WWW-Authenticate`: Basic, Digest (RFC 7616: MD5, SHA-256, qop=auth,
  nonce count), NTLM/Negotiate (Windows SSPI current user; macOS/Linux NTLM via go-ntlmssp with a site
  login). Credentials come from the URL userinfo, then the first matching `site_logins` entry. Never send
  credentials to a host other than the one that challenged; never over plain HTTP to a different origin
  after a redirect. The authenticated retry goes straight to the URL that challenged (after a redirect:
  the redirect target, not the original URL); when that is another origin than the requested URL, the
  caller's `Authorization`, `Cookie` and `Cookie2` headers are dropped, and a 401 from yet another origin
  during the same exchange is not answered. If no credentials match, the download fails with
  `error_code = "auth_required"` and the UI offers to add a site login.
- **Secrets** (`SetSecret`/`ClearSecret`): Windows DPAPI-protected blob in the store (current user scope);
  macOS Keychain generic password via `/usr/bin/security` (service "Downex", account "<kind>:<id>"; the
  secret goes hex-encoded (`-X`) over the stdin of `security -i`, never on a command line, so a secret is
  at most about 1.9 KB there); Linux: 0600 file in the config dir with a logged warning (Secret Service
  later). Passwords never appear in Settings, logs, Watch, error texts or exports.
- **Host rules** (`host_rules`): most specific pattern wins; `max_connections` caps per-download
  connections for that host (in addition to the engine's 429/503 penalties); `speed_limit` is a per-host
  limiter shared by all downloads to that host. Since site media (docs/SITE-MEDIA-SPEC.md §2.4, §3.8)
  `max_request_bytes` (0 = no cap, else 1 MiB–1 GiB) caps every ranged request to the host: the engine's
  probe asks for `bytes=0-(cap-1)` and each worker's request ends at most `cap` bytes after its start
  (`engine.RequestCaps`); the rules also apply to media jobs (connections per host, the host limiter, the
  request cap on byte-range fragments, and the chunk size of a web page's video). `*.googlevideo.com`
  has a built-in rule (10 MiB requests, 4 connections) that applies only to hosts no user rule matches;
  it is not stored and not shown as a user rule.

## 3. Desktop integration
- **Clipboard monitor** (`clipboard_monitor`, default off): Windows `AddClipboardFormatListener` on a
  message-only window in downexd's tray thread; macOS `NSPasteboard` `changeCount` check every 1 s in the
  agent (cgo; no-op without cgo); Linux stub. On a new clipboard text that is exactly one http(s) URL (or a
  few lines of URLs) whose path ends with an extension of any category: `Add` with `source = CLIPBOARD`
  (confirmation applies like browser hand-offs, and so does the expired-link matching of
  `DAEMON-SPEC.md` §3 "New links"). A text with any URL that carries user information
  (`https://user:pass@host/…`, also `user@` or an empty `@`) offers nothing at all. Ignore clipboard
  content marked as excluded/sensitive
  (Windows `ExcludeClipboardContentFromMonitorProcessing`, `CanIncludeInClipboardHistory=0`; macOS
  `org.nspasteboard.ConcealedType`/`TransientType`). Same URL not offered twice within 10 minutes.
- **Mark downloaded files** (`mark_downloaded_files`, default on), applied to the final file before it is
  announced complete: Windows `Zone.Identifier` alternate data stream (`[ZoneTransfer]` ZoneId=3,
  ReferrerUrl, HostUrl — without credentials/query secrets? keep the URL but strip userinfo); macOS
  `com.apple.quarantine` = `0081;<hex unix time>;Downex;<UUID>` and `com.apple.metadata:kMDItemWhereFroms`
  (binary plist array [url, referrer]); Linux `user.xdg.origin.url` / `user.xdg.referrer.url`. Failures are
  logged, never fail the download (e.g. FAT32 has no ADS).
- **File check** (`check_downloaded_files`, default on; DESKTOP-SPEC §9): before the mark, a completed
  file download's bytes, name and served type are compared (and archive members listed, names only);
  the result is stored and shown, never acted on. The order in `finish` is check → mark → scan; the
  mark and the scan run whatever the check says.
- **Antivirus scan** (`av_scan_enabled` + `av_scan_command` absolute path + `av_scan_args`, `[File]`
  replaced by the quoted path; no shell): run after marking, timeout 10 min, the download is completed
  either way; exit code ≠ 0 → notification "Scanner reported a problem with <name>" and `Download.error`
  note (state stays completed). Downex never opens a downloaded file by itself (DESKTOP-SPEC §9.1 F2):
  any future "open when done" skips files after a failed scan and files whose check is DANGER or
  CAUTION.

## 4. Updates
- `update_feed_url` (https only) points to a JSON manifest `{"version","published_at","notes",
  "assets":[{"os","arch","url","sha256","size"}]}` and a detached ed25519 signature at
  `<feed_url>.sig` (base64 of the signature over the exact manifest bytes). The public key is compiled in
  (`-ldflags -X downex/internal/update.PublicKey=<base64>`); without a key, updates are disabled.
- `CheckForUpdate`: fetch (≤ 1 MiB), verify the signature first, parse, compare semver with the running
  version, pick the asset for runtime.GOOS/GOARCH (darwin: "universal"). `auto_check_updates`: check at
  start (after 60 s) and every 24 h. A feed that answers 404 or 410 is not published yet
  (`update.ErrNotPublished`): `CheckForUpdate` returns `NotFound` "No update information is published
  yet", which Settings shows as an info note, and an automatic check only logs it (Info).
- After every automatic check that finds an update, the tray/menu bar notification says "Click to
  open Downex and install it." (clicking it opens the window) and Watch without ids sends the check's
  `UpdateInfo` as the event `update` (field 8): in the first batch once a check has run, then after
  each automatic check. The window (Windows and macOS) shows it in a pop-up with **Later** and
  **Update now** (ModalUpdate, DESKTOP-SPEC §4) that runs `InstallUpdate`; it never opens a browser
  download. **Later** (also Escape and the close button) puts off only that check's offer: it is
  saved as `<version>@<checked_at ms>` under the prefs key `downex.updateLater`, so the offer comes
  back after the next automatic check (daily, or when downexd starts again).
- `InstallUpdate`: download the asset with the engine into the data dir, verify size + sha256 (and on
  Windows, when the running downexd is Authenticode signed, that the Setup carries a valid signature
  by the same publisher: the signing certificate's subject distinguished name, compared byte for
  byte as encoded, must equal the running downexd's; a renewed certificate keeps it, so it is
  compared rather than the thumbprint. A signer whose subject cannot be read is refused. An unsigned
  downexd, today's releases and development builds, leaves the Setup to the signed manifest, as
  before; amendment 2026-11-20), then Windows: run
  `Downex-Setup-<v>.exe /S /RELAUNCH /D=<the running installation's folder>` detached (the whole
  command line as one string: NSIS reads `/D=` only last and unquoted, spaces included) and shut down
  gracefully. Setup replaces that folder in place, opens the window again when it closed it
  (`/RELAUNCH`), and removes an installation that its Uninstall entry names in another folder with
  that one's uninstaller, so one installation remains (DESKTOP-SPEC §6). Amendment 2026-11-22 (1.1.2):
  before, the command line was `/S /RELAUNCH` and Setup installed into the folder its Uninstall entry
  named, or the default one without it. The daemon that starts Setup decides, so an update started
  by 1.1.1 still runs without `/D=`. macOS: mount the DMG, verify the new
  app's code signature (`codesign --verify --deep --strict`) and identifier, and then for a running
  build signed with a Developer ID its team; for an ad-hoc signed running build (the releases) the
  signed manifest is the trust anchor instead, and the new bundle's `CFBundleShortVersionString` must
  equal the manifest version's MAJOR.MINOR.PATCH (`DOWNEX_ALLOW_UNSIGNED_UPDATE` skips only this check,
  for tests). The bundle is copied with `ditto --noqtn` and `xattr -dr com.apple.quarantine` (a failure
  is only logged), replaced in place and relaunched; the main window reopens Downex.app after it
  exits. Linux: not supported (package managers).
- Leftovers (amendment 2026-11-22): downexd removes update packages (`Downex-Setup-*.exe`,
  `Downex-*.dmg`) and partial downloads (`*.part`) from its `updates` folders (also the roaming one
  an older daemon used) at its start and again before each automatic check, while no install runs
  or has committed: at the start, the Setup that installed and started this daemon usually still
  runs, and Windows keeps a running program's file. On macOS it also removes the staging folders an
  interrupted install left next to Downex.app (`.downex-update-<n>` and `.downex-update-<n>.old`,
  each a whole Downex.app) once they are 30 minutes old (another user's install may be staging
  one); it lists them while installs are held off and removes them after, so that an
  `InstallUpdate` never waits for that. **Residual:** a daemon ended during Stage leaves the disk
  image attached (`hdiutil`, `-nobrowse`: not shown in the Finder) at a `downex-update-*` folder of
  `$TMPDIR`; nothing detaches it before the Mac restarts or the user logs out (it holds no copy in
  Applications).
- `scripts/release-sign.go` (go run): generate a key pair, sign a manifest; private key never in the repo.
