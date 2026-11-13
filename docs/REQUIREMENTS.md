# Downex — Requirements (v1.0)

Downex is a cross-platform (Windows, macOS, Linux) download manager with segmented,
resumable downloads, browser capture, media grabbing and scheduling.

## 1. Functional requirements

### 1.1 Download engine
- F1. HTTP and HTTPS downloads over HTTP/1.1 and HTTP/2; HTTP/3 is opt-in (later phase).
- F2. Segmented downloads: up to 32 connections per download (default 8, per-host overrides),
  dynamic splitting of the largest remaining segment (see `ENGINE-SPEC.md`).
- F3. Automatic single-connection fallback when the server does not support ranges.
- F4. Pause/resume across restarts and crashes; resume validates the remote file
  (`If-Range` with strong ETag, else Last-Modified) and restarts when it changed.
- F5. Speed limits: global, per queue, per download.
- F6. Retries with backoff; honours `Retry-After` on 429/503 and lowers per-host concurrency.
- F7. Proxies: HTTP (CONNECT), SOCKS5, SOCKS4/4a, system proxy (Windows WinHTTP incl. WPAD, macOS
  `scutil --proxy`, Linux environment), PAC (M6, `M6-SPEC.md` §2).
- F8. Server/proxy authentication: Basic, Digest; NTLM/Negotiate (M6: Windows SSPI as the signed-in
  user, elsewhere NTLM with a stored password); Kerberos outside Windows Negotiate later.
- F9. Filename from `Content-Disposition` (RFC 6266 / RFC 5987), else URL; safe filename
  sanitisation for Windows, macOS and Linux; no overwrite without user choice.
- F10. FTP/FTPS and SFTP segmented downloads (1.1).

### 1.2 Browser integration
- B1. One Manifest V3 extension (TypeScript) for Chrome, Edge and Firefox.
- B2. Capture rules: file-extension/MIME list, minimum size, include/exclude hosts,
  modifier-key bypass, per-tab disable.
- B3. Hand-off forwards URL, final URL, method, request headers (Cookie, Referer, User-Agent),
  POST body (bounded) and suggested filename.
- B4. Chromium: pause → verify → commit hand-off via the downloads API; the browser keeps the
  download if Downex declines, is not running, or times out. Firefox: blocking `onHeadersReceived`.
- B5. Context menu: "Download with Downex", "Download all links".
- B6. Transport: native messaging to `downex-nmhost` (stateless relay). No kernel drivers, no injection.

### 1.3 Media
- M1. Detect unencrypted HLS and DASH streams; quality picker.
- M2. Download fragments in parallel on the shared engine; merge audio + video with a
  stream-copy remux: pure Go first (`internal/remux` with mp4ff, decision 2026-10-20; MEDIA-SPEC §5),
  TS → MP4 is pure Go as well (`VIDEO-DETECT-SPEC.md`) and WebM is never offered, so Downex has no
  FFmpeg process.
- M3. Refuse encrypted/DRM content (`EXT-X-KEY` METHOD≠NONE, DASH ContentProtection, pssh/tenc boxes)
  with a clear message. Downex's own code never deciphers site-specific signatures or solves site
  challenges; it may run yt-dlp, a separately distributed third-party program downloaded from its
  publisher, as an external process to resolve a video page (`SITE-MEDIA-SPEC.md`).

### 1.4 Management & UX
- U1. Download list with categories (extension → folder rules).
- U2. Queues with concurrency and speed caps; scheduler (start/stop time, weekday mask, one-shot).
- U3. Post-queue actions: shutdown, sleep, hibernate (with cancellable countdown).
- U4. Clipboard URL monitoring (where the OS allows it).
- U5. Tray icon, notifications, single running instance, CLI (`downex add/ls/pause/resume/rm`).
- U6. Mark-of-the-Web (Windows) / `com.apple.quarantine` (macOS) / origin xattrs (Linux); optional
  antivirus scan hook.
- U7. Signed auto-update (where the install channel allows it).

## 2. Non-functional requirements
- N1. Platforms: Windows 10 1809+/11 (x64; Windows on Arm is not part of the release); Linux
  x64/arm64 (Ubuntu 22.04+, Debian 12+, Fedora 41+), KDE and GNOME, X11 and Wayland; **macOS 13
  Ventura or newer as one universal build (Apple Silicon arm64 + Intel x86_64)** — `Downex.app` in a DMG, menu-bar icon instead of the tray,
  LaunchAgent for launch at login, Chrome/Edge/Brave/Firefox integration (Safari later: it needs a
  Safari Web Extension inside the app). Releases are ad-hoc signed and not notarized (no Apple
  Developer account): a downloaded copy opens the first time via right-click → Open.
- N2. Footprint: resident daemon < 30 MB RSS and ~0% CPU when idle; UI is a separate on-demand process.
- N3. Throughput: at least on par with aria2c/curl on the benchmark matrix.
- N4. Integrity: zero corrupted files across 1,000 kill -9 / power-cut cycles; progress is committed
  only after data is flushed to disk.
- N5. Security: TLS always validated (no downgrade, no InsecureSkipVerify); IPC endpoints restricted to
  the current user; all untrusted parsers fuzzed.
- N6. Per-user install without admin rights by default.
- N7. Accessibility (keyboard, screen readers) and i18n including RTL.

## 3. Development requirements
- Go 1.27+, Node 24 LTS + pnpm, Git, Wails v2 CLI, WebView2 (Windows) / webkit2gtk-4.1 (Linux).
- Later: buf (protobuf), NSIS + WiX v5, GoReleaser + nfpm, flatpak-builder, an Ubuntu 22.04/24.04
  test machine or WSL2 distro.
- Release: no paid code signing (the Windows Setup is unsigned, macOS builds are ad-hoc signed);
  the extension is to be listed on addons.mozilla.org and Edge Add-ons and is loaded unpacked
  in Chrome, Brave and Chromium (no Chrome Web Store listing). Later: CI on GitHub Actions.

## 4. Milestones
| Milestone | Scope | Status |
|---|---|---|
| M1 Engine MVP | `internal/*` engine, transport, store, limiter; `downex get` CLI with resume; tests | Done |
| M2 Daemon + IPC | `downexd`, user-only named pipe / unix socket, CLI as client | Done on Windows (unit, package and real-process end-to-end tests pass with `-race`); Linux builds and its tests compile, but they have not run on Linux yet |
| M3 Browser | MV3 extension + `downex-nmhost`, host registration on both OSes | Done on Windows (unit + piped-stdio end-to-end tests), including per-site and per-tab disable (B2) in the popup; not yet tried in a real browser |
| M4 Desktop UI | Wails v2 + Svelte window, tray, notifications, installers alpha | Window done (`downex-ui.exe`); the rest in M4.5 |
| M4.5 Desktop integration (`DESKTOP-SPEC.md`) | browser hand-off confirmation, categories and folders (U1), tray + notifications (U5), launch at login, per-user NSIS installer (N6) | Done on Windows: unit, package and real-process end-to-end tests (`internal/e2e/desktop_test.go`: nmhost hand-off → awaiting → window started with `--confirm <id>` → `downex confirm` → category folder; PauseAll/ResumeAll; settings round trip) pass with `-race`; installer tested with silent install/upgrade/uninstall only. Linux: launch at login (XDG autostart) compiles but has not run; Linux tray (StatusNotifierItem) and packages later |
| M5 Media (`MEDIA-SPEC.md`) | HLS/DASH parsers, fragment plan, media jobs, remux, DRM refusal, stream detection in the extension, quality picker | Done on Windows: unit, package, fuzz and real-process end-to-end tests (`internal/e2e/media_test.go` with generated streams: DASH fMP4 video + audio → one progressive MP4; HLS master + MPEG-TS → one `.mp4`, converted since 2026-10-23; `EXT-X-KEY`, live and `pssh`-in-init refusals; pause + kill + restart resuming without re-fetching durable fragments; browser `addMedia` hand-off → awaiting → `downex confirm --video`) pass with `-race`. Remux is pure Go (mp4ff for fMP4, Downex's own TS demultiplexer for MPEG-TS), no FFmpeg; every manifest download is MP4 only (only legacy `.ts` records with progress keep their `.ts` join); single-file DASH, WebM, AC-3/E-AC-3/MP3 or MPEG-2 inside TS, packed audio and changing fMP4 init segments are refused as unsupported (MEDIA-SPEC §7). Not yet tried in a real browser or checked in real players; Linux compiles only |
| M5.5 macOS port (`MACOS-SPEC.md`) | universal `Downex.app` (arm64 + x86_64, macOS 13+) with the daemon, CLI and relay in `Contents/Helpers`, progress windows without Dock icons, the menu-bar icon and notifications in Downex Agent.app (which clients autostart), LaunchAgent, browser registration, `/usr/bin/open` file handling, app menu; `scripts/build-macos.sh` (ad-hoc or Developer ID signing, notarization, DMG) | Written on Windows, **not yet run on a Mac**: darwin/arm64 and darwin/amd64 build and vet with CGO_ENABLED=0, the darwin test binaries compile, the OS-independent rules (paths, LaunchAgent plist, socket-path limit, bundle layout, manifest folders, open arguments, menu-bar and main-thread logic) are unit-tested on Windows, and `go test ./packaging/macos` checks the templates and the build script's functions with stand-in tools. The Objective-C (menu-bar icon, notifications, the progress windows' Dock policy) has never been compiled. First run: `MACOS-TESTING.md` |
| M6 Power features (`M6-SPEC.md`) | queues, scheduler, post-actions (U2, U3), proxies/PAC, auth, host rules, secrets (F7, F8), clipboard monitor, file marks, antivirus hook (U4, U6), signed updater (U7) | Done on Windows (0.4.0; review fixes in 0.4.1: credentials only for the challenging origin or the forwarding proxy, PAC scripts in a memory-limited helper process, no user information in proxy URLs or clipboard links, keychain secrets over stdin, queues finished only by completions, overdue countdowns cancelled, schedules after clock changes, nested limits, keep-awake while a queue with a post action or schedule works): unit, package and real-process end-to-end tests pass with `-race` (`internal/e2e/m6_test.go`, `m6_net_test.go`: two queues with their own caps under the global cap, a stopped queue holding its downloads, a scheduled start on an accelerated test clock, a post-queue sleep performed once after the countdown by a recording stand-in, a cancelled shutdown, the quit action; downloads through an HTTP CONNECT proxy and a SOCKS5 proxy; Digest with a site login whose password is stored with `SetSecret`, `auth_required` without or with a wrong one; a host rule capping connections at 1; `Zone.Identifier` on a completed file; an update check against a signed test feed, a tampered manifest refused). Not verified: real sleep/hibernate/shutdown and wake timers (never performed by tests), the keep-awake request's effect on real idle sleep, SSPI NTLM/Negotiate against a domain server, WPAD networks, a real Setup update (release builds carry the update key and the built-in feed, which is published after the first release), macOS (compile-checked only; `MACOS-TESTING.md` §12a–12d), Linux power actions (stubs) and clipboard (none) |
| Site media (`SITE-MEDIA-SPEC.md`) | video pages (YouTube first) resolved with yt-dlp and Deno, which Downex downloads, verifies and updates on first use; one `.mp4` per video, every MP4 quality listed for the user to choose; segmented, resumable ranged downloads with URL refresh; overlay button and popup entries in the extension, page mode in the Add dialog, `downex media --site`, `downex tools`, Settings → Video sites | Done on Windows against stand-ins (2026-10-22, `SITE-MEDIA-SPEC.md` §10): MP4 only, every MP4 quality listed for the user to choose; unit, package, fuzz and real-process end-to-end tests (`internal/e2e/site_test.go`: stand-in yt-dlp/Deno on PATH or installed from a TLS release server with a test OpenPGP key, 1 MiB/2-connection host rule, one progressive MP4, kill + resume without refetching stored chunks, 403 renewal, refusals, the browser hand-off with `downex confirm --video`) pass with `-race`. The embedded yt-dlp signing key is upstream's `public.key`, pinned by its fingerprint (`internal/tools/ytdlp_key.asc`). Not verified: real YouTube/googlevideo, real GitHub installs, real browsers, real players, proxies; macOS and Linux compile only |
| Video detection on every site (`VIDEO-DETECT-SPEC.md`) | a Download button over every visible, meaningful `<video>` in any frame (never DRM or live; per-site and global off); direct MP4 files, HLS and DASH manifests detected from the browser's own traffic per tab and frame, with qualities and sizes in the button's panel and the popup; yt-dlp qualities merged in (look-ahead on curated video sites, a click elsewhere); MP4 only everywhere: pure-Go TS→MP4 remuxer (H.264, HEVC, AAC), so M5 HLS-TS downloads produce `.mp4` too; direct files handed off as ordinary segmented downloads | Done on Windows against local servers and stand-ins (2026-10-23, `VIDEO-DETECT-SPEC.md` §10): unit, package and fuzz tests (remuxer: muxed and separate TS, B-frames, 2³³ wrap, discontinuities, HEVC, refusals, bounds; extension: classification, frames and documents, keys, hand-offs, curated table, tracker, overlay, panel and popup in happy-dom and a fake browser) and real-process end-to-end tests (`internal/e2e/videodetect_test.go`, `media_test.go`, `nmhost_test.go`: TS → MP4 through the CLI with separate audio, discontinuities, early refusals, legacy `.ts` records, pause + kill + resume; the relay's `probeStream` and `addMedia` tracks with confirmation; a direct MP4 handed off with `add` over several ranged connections with the cookie kept from the CDN; MP4-only manifests; a site with TS-only HLS through the stand-in yt-dlp) pass with `-race`. Not verified: a real browser (Chrome, Edge, Firefox), the curated sites, signed CDN files, playback of converted MP4s in real players, macOS and Linux at run time |
| M7 Hardening → 1.0 | benchmarks, fuzzing, security review, packaging for all channels | Planned |

M2 is specified in `DAEMON-SPEC.md`, M3 in `BROWSER-SPEC.md`, M4.5 in `DESKTOP-SPEC.md`, M5 in
`MEDIA-SPEC.md` (deviations in its §7), M6 in `M6-SPEC.md`, site media in `SITE-MEDIA-SPEC.md`, video
detection on every site in `VIDEO-DETECT-SPEC.md`, the macOS port in `MACOS-SPEC.md` (its first-run checklist is
`MACOS-TESTING.md`). The
end-to-end tests (`internal/e2e`) run the built `downex`, `downexd` and `downex-nmhost`
executables (and a stand-in `downex-ui` that records how the daemon starts it);
`go test -short ./...` skips them.
