# Desktop integration specification (M4.5 — Windows first)

## 1. Browser hand-off confirmation
- `Add` with `source = ADD_SOURCE_BROWSER` while `Settings.confirm_browser_downloads` is true creates the
  download in `DOWNLOAD_STATE_AWAITING_CONFIRMATION` (persisted; survives restarts; never started by
  the scheduler). Other sources (CLI, UI) are queued as before. A hand-off is held only while the daemon
  can ask the user: it can start the confirmation window (below), or its tray icon is shown (its
  notifications appear only then). Otherwise (Linux for now, `--no-tray`, `show_tray_icon` off, no
  `downex-ui`) it is queued (paused with `start_paused`) as before, and the log says why.
- The daemon proposes a category, folder and filename (§2) and then asks the user:
  if `downex-ui.exe` exists next to `downexd`, it runs `downex-ui.exe --confirm <id>` detached (the UI is
  single-instance, so a running window receives the arguments); otherwise it only notifies (§4).
- The UI shows a confirmation dialog per awaiting download (one at a time, oldest first; also on startup
  and whenever an awaiting download appears in Watch events): URL, editable file name, size hint
  (`expected_size`, else "unknown"), category selector (changes the proposed folder), folder + browse,
  buttons **Start download** (Confirm start=true), **Download later** (Confirm start=false → paused),
  **Cancel** (Remove). Closing the dialog leaves it awaiting.
- The proposed name is the request's filename, else the URL's last path segment when its extension
  belongs to a category, else empty (the engine names the file from the server's answer when it starts).
  The daemon starts the UI at most once per 2 s (with the oldest id of a burst), and only when it was
  given no `--endpoint` (the window finds the daemon on the default endpoint or `$DOWNEX_ENDPOINT` only).
- `Pause`/`Resume` of an awaiting download return `FailedPrecondition`; `Remove` cancels it.
- `Confirm` validates dir/filename like `Add` (DAEMON-SPEC §4; the proposed folder and every category's
  folder are trusted like Add's category folders: no local-drive check, created with their parents when
  the download starts, refused only when they exist as a file), reserves the name (first free
  "name (n).ext"), then queues or pauses. A confirmed folder that is another category's folder changes
  `Download.category` to that category (`ConfirmRequest` has no category field).
  `Confirm` on a download that is not awaiting returns `FailedPrecondition`.
- The CLI gets `downex confirm <id> [-o dir] [--name f] [--later]` (testing/scripting).
- A web page's video (docs/SITE-MEDIA-SPEC.md §5.4) is confirmed in the same dialog: it shows "Page", the
  title and "<site> video · <length>", and its quality picker lists every offered MP4 quality, best first,
  with sizes (exact, `~` for the site's estimates, else "about" from the bit rate); an audio select only
  when several MP4 audio tracks exist. Another quality keeps the container, so the name keeps `.mp4` (a
  name typed without it gets `.mp4`). `downex confirm --video <id>` does the same. The Add dialog's
  "Video or stream" mode takes a manifest address (HLS/DASH) or a web page, retries every 2 s while
  downexd answers `tools_installing` or `site_resolving` (showing the yt-dlp/Deno download progress or
  "Getting video info… N s"), and Cancel stops the retries. Settings → Video sites holds `site_media`,
  `tools_auto_update`, the yt-dlp channel, each tool's version, origin (Downex copy or PATH), check and
  problem, "Install now"/"Check for updates", and the licence and terms lines. While the first download
  of yt-dlp and Deno runs, the main window shows a progress toast (a failure toast offers "Open
  settings"). In the main list a site download's badge is the site ("YouTube"), its state while yt-dlp
  reads the page is "Getting video info", and its progress counts bytes once downexd knows the tracks'
  total.

## 2. Categories and folders
- Defaults (`Settings.categories`, editable in the UI; extensions lower-case, no dots):
  Video `mp4 mkv avi mov webm wmv flv m4v mpg mpeg ts 3gp` · Audio `mp3 flac m4a aac wav ogg opus wma` ·
  Archives `zip rar 7z gz xz bz2 tar tgz zst iso img dmg` · Documents `pdf epub doc docx xls xlsx ppt
  pptx odt ods txt csv md` · Programs `exe msi msix appx apk deb rpm appimage bat cmd ps1 jar`.
- The category is chosen at `Add` from the request filename, else the URL path's last segment, else the
  MIME hint (`video/*`, `audio/*`, common archive/document/program types); first matching category wins.
  Stored in `Download.category`. The MIME hint is lower-cased without parameters; a malformed or
  non-ASCII hint is dropped, control characters or more than 255 bytes are refused (InvalidArgument).
  `expected_size` -1 means unknown (stored as 0).
- When the request has no `dir` and `use_category_folders` is true, the folder is the category's `dir`
  or `<default_dir>/<category name>`; otherwise `default_dir`. Folders are created when the download
  starts. Settings validation: unique non-empty names (≤ 64 chars), absolute local dirs (DAEMON-SPEC §4
  rules), ≤ 32 categories, ≤ 200 extensions each, extensions `[a-z0-9]{1,16}`.
- Settings are replaced as a whole: every client reads, modifies and writes back all fields. A settings
  file written by an older daemon gets the defaults for missing fields (bools above default to true
  except `launch_at_login`).

## 3. Tray (downexd)
- Windows: notification-area icon with tooltip "Downex — N active, X/s" (updated ≤ 1/s, only while
  something changes). Left double-click: open the UI. Menu: **Open Downex**, **Pause all**, **Resume all**,
  separator, **Settings…** (opens the UI), **Quit Downex** (graceful Shutdown). `show_tray_icon` toggles it
  live. Re-create the icon when Explorer restarts (`TaskbarCreated`). Only the user's own daemon (no
  `--db`, `--endpoint`, `$DOWNEX_ENDPOINT`) shows it; `--no-tray` turns it off.
- `PauseAll` pauses queued, then running downloads; `ResumeAll` queues paused downloads only (failed and
  awaiting ones stay). Both return the count.
- Linux (StatusNotifierItem) is later; until then the tray is a no-op there.
- The tray must not raise idle CPU: no polling loops faster than 5 s; event-driven updates.

## 4. Notifications
- On completion (`notify_on_complete`) and failure (`notify_on_failure`), shown as the tray icon's
  notification balloon (Windows 10/11 present it as a toast). Title "Download complete" / "Download
  failed"; text = file name (bidi/control characters stripped) and, for failures, the short error.
- Coalesce bursts: more than 3 within 5 s become one "N downloads completed" message.
- Awaiting-confirmation downloads that could not open the UI produce "Confirm download: <name>".
- Clicking a notification opens the UI.
- An available update (M6-SPEC §4) produces "Click to open Downex and install it." after each
  automatic check that finds one. The native window (Windows and macOS) offers it in a pop-up
  (`ModalUpdate`, `ui/views/dialogs/update.go`) with the Restart dialog's look: "Downex X is
  available", the installed version, the release notes when the manifest has any, **Later** and
  **Update now**. It opens only when no other dialog or menu is open, no browser hand-off
  confirmation waits and downexd is reachable; it takes no focus and ignores **Update now** for its
  first 600 ms. It is withdrawn when a later check no longer offers the update, but never while
  installing. A failed install shows "The update could not be installed: <reason>" and can be tried
  again. On macOS, after a successful install the main window reopens Downex.app once it has exited,
  and every main-window start inside Downex.app (not translocated, not a dev build) registers the
  relay with the browsers in the background (`downex-nmhost register --keep`): it writes missing
  manifests and rewrites those that name another relay or lack a built-in extension ID, keeps
  every ID a manifest already allows (one added by hand) and never replaces a `.bak`; "Register
  with browsers again" in Settings → Integration still writes the built-in IDs only.

## 5. Launch at login
- `launch_at_login = true` writes `HKCU\Software\Microsoft\Windows\CurrentVersion\Run\Downex` =
  `"<dir>\downexd.exe" --background` (Linux: `~/.config/autostart/downex.desktop`); false removes it.
  Applied by the daemon whenever the setting changes and verified at daemon start.
- Windows starts Run entries like programs the user opens, which gives a console program a console
  window of its own (open while it runs; closing it stops it). Releases build `downexd.exe` as a
  GUI-subsystem program (`-H windowsgui`); a console build started with `--background` in a console of
  its own continues as a detached copy at once. A GUI build run from a terminal with `--foreground`
  (or `--help`, `--version`) attaches to that terminal for its output.

## 6. Installer (Windows, NSIS, per-user, no admin)
- Installs `downexd.exe`, `downex.exe`, `downex-nmhost.exe`, `downex-ui.exe` and the `extension\` folders
  into `%LOCALAPPDATA%\Programs\Downex`; Start-menu shortcut "Downex" → `downex-ui.exe`; optional desktop
  shortcut; HKCU Uninstall entry (DisplayName "Downex", Publisher "Downex", version, size, icon).
- After copying: `downex-nmhost.exe register` (current user) and start `downexd --background`.
- Upgrade: stop the UI and `downex daemon stop` before replacing files.
- Uninstall: stop UI + daemon, `downex-nmhost.exe unregister`, remove the Run entry, shortcuts, files and
  the Uninstall key; ask before deleting user data (`%APPDATA%\Downex`, logs).
- The browser extension cannot be installed silently outside the browser stores; the installer's finish
  page links to instructions for loading `extension\chrome-mv3` (dev builds) until store listings exist.

## 7. Per-download progress window
A small window per download (one per download id; reopening focuses the existing one) that shows the
live state of that download, in Downex's own visual style.

**When it opens** (only while `Settings.show_progress_window` is true):
- the user clicks **Start download** in the confirmation dialog, or adds a download in the main window
  without "start paused";
- a browser hand-off starts at once (confirmation off) — the daemon opens it;
- (0.8.2) the user resumes or restarts **exactly one** download in the main window (the toolbar, its row's
  action, Space, the context menu, the details pane, the command palette, or the Restart / Download
  again dialog for one download), the call succeeds, and the download starts now: the answer is not
  paused and not queued behind `max_concurrent_downloads` (`queue_position` > 0), like an add;
- the user double-clicks (or presses Enter on) an unfinished download (not completed, not awaiting
  confirmation) in the main list, or chooses "Show progress" from its context menu (this works
  regardless of the setting).
Queued/scheduled downloads that start later (including a hand-off queued behind
`max_concurrent_downloads`), CLI downloads, adds of several URLs at once, PauseAll/ResumeAll, resuming
or restarting several selected downloads, queue and scheduler starts, automatic retries, and Resume or
Retry inside an already open progress window never open windows.

**Process model:** `downex-ui.exe --progress <id>` opens that download's progress window (default
520×300 collapsed / 520×560 with details, resizable, min 480×260), one per download id (a second launch
for the same id focuses the existing window). The daemon opens it with `platform.StartApp`; the main
window with a binding. Windows native window (amd64): one process hosts the main window and every
progress window; a later launch hands its arguments to it and exits, each window closes on its own and
the last one ends the process; another program's WM_CLOSE (the installer's CloseMainWindow) or the end
of the session closes every window (`ui/native/glue` package doc). Wails v2 (macOS, arm64,
`-WebViewUI`) has one window per process: each progress window is its own small process, and all of
them use the same WebView2 user-data folder and identical environment options so they share one
WebView2 browser process.

**Memory while out of use (Windows, 0.9.2, the Wails window):** every Downex window (each its own process) checks once a
second whether it is in use: not minimised, and the foreground window belongs to its process (the window
itself or its own dialog). After 10 s out of use it pulses WebView2's memory target (`ICoreWebView2_19`)
to low for 3 s and back to normal, and again every minute while it stays out of use; in use, the target
is normal at once (`ui/webview_trim.go`, `ui/webview_windows.go`). Kept low, WebView2 keeps squeezing a
window that still draws (a download behind another app) and it pages straight back in: ~1,200 page
faults/s and +3 points of CPU, so a pulse. Measured on the owner's PC against 0.9.1: downloading behind
another app 183–185 → 110–125 MB after the first pulse, minimised and downloading 180–187 → 116–124 MB,
minimised idle 138–142 → 93–108 MB; CPU within run-to-run noise. Wails v2 keeps the WebView unexported:
`webviewOf` reads its frontend's `chromium` and `mainWindow` by name and exact type (v2.17.0) and does
nothing when that shape is not there. The memory-target slots are called directly (WebView2.h: get 119,
put 120; the target is read first and must be normal or low): go-webview2's `pkg/webview2` panics at init
with Go 1.27. Extra browser arguments are not possible: Wails' loader clears
`WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS`.

**Status tab:** title `<percent>% <file name>` (just the name until the size is known); rows: URL
(middle-ellipsis, copy button), Status (Connecting… / Receiving data… / Paused / Waiting in queue /
Assembling… (media) / Completed / Failed: <reason>), File size, Downloaded `<bytes> (<pct>%)`,
Transfer rate (once the download has run 3 s in all, with the average speed of the whole download,
`received` / `active_ms`: "5.8 MB/s · avg 7.6 MB/s", whole in its tooltip), Time left, Resume
capability (Yes = ranged, No = single connection, Unknown before the probe). Media downloads also show fragments `done / total` and the chosen quality. A web page's video
(docs/SITE-MEDIA-SPEC.md §5.4) says "Page" for the address, "Getting video info…" while yt-dlp reads the
page, "Parts"/"Part N" for its byte ranges and "Merging…" while its tracks are joined; its size and
percentage come from downexd's byte total (`BytesTotal`), and its completion view reads e.g. "YouTube
video · 1080p (Full HD) 60fps H.264 + AAC · MP4" (the standard quality name, SITE-MEDIA-SPEC.md
amendment 2026-10-23). A stream's size while it downloads (0.7.1) is downexd's estimate
(`Download.size_approx`): File size "~2.1 GB" and an estimated time left, the percentage from its
fragments; the list, the details panel and the CLI show it the same way.
Overall progress bar (indeterminate while the size is unknown).
Buttons: **Show details / Hide details**, **Pause / Resume**, **Cancel** (pauses the download and closes
the window; the download stays in the list and can be resumed).

**Details:** "Start positions and progress by connection": a horizontal bar of the whole file where each
segment's start is marked and its received part filled (from `Download.segments`), plus a table with one
row per worker connection from `Download.connection_info`: N., Downloaded (bytes of its current range),
Info (state text: Connecting…, Sending request…, Receiving data…, Retrying in N s (reason), Idle,
Finished, Error: reason). Media: Info shows the current fragment number.

**Speed limit tab:** "Limit this download's speed" checkbox + value with KB/s or MB/s (decimal
units, 1 KB/s = 1,000 bytes per second, like every size and speed the window shows; a unit typed
with the number wins: K/KB, M/MB, G/GB decimal, KiB/MiB/GiB binary; a limit stored in MiB/s before
shows rounded and is kept exactly until edited); applied immediately via `SetDownloadOptions` (live for running
downloads), and shown as active in the status tab.
Optional connections limit (1–32) applied via `SetDownloadOptions` (takes effect when the engine can:
at once for a running file download, on the next start for a stream).

**Completion view** (when `show_complete_dialog` is true; otherwise the window closes on completion):
"Download complete", file name and folder, size, **Open**, **Open folder**, **Close**, and a
"Don't show this again" checkbox that turns `show_complete_dialog` off. Executables keep the existing
open-confirmation rules. On failure the window stays open with the error and **Retry** (Resume) / **Close**.

**Engine/daemon:** the engine reports per-worker state (slot, current range, received, state, retry
reason/delay, speed) with its progress events; the daemon maps it to `connection_info` (≤ 4 Hz, in Get,
List and Watch only for downloads that are running; never persisted) and implements `SetDownloadOptions`
(validate like Add; persist; apply the speed limit to the running download's limiter at once and the
connection limit through `Engine.SetMaxConnections`, which lowers by stopping the highest connections
without losing data). The CLI has `downex set <id> [--limit rate] [-c n]`, and `downex info` shows the live
connections of a download that a running daemon is downloading.

## 8. Download complete dialog
The completion view of §7, extended so it also appears when the progress window was closed.

- **Who gets it:** downloads the user started interactively — any download whose progress window was
  opened at least once (the UI marks it via `SetDownloadOptions{completion_dialog: true}`; persisted
  as `Download.completion_dialog`). Queue/scheduled/CLI downloads only get the tray notification.
  Nothing pops up while `Settings.show_complete_dialog` is off.
- **Closing the progress window (X) only closes the window**: the download keeps running in the daemon.
  **Cancel** still means pause + close.
- **On completion:** if the download's progress window is open it switches to the completion view;
  otherwise the daemon opens `downex-ui.exe --progress <id>` (via `platform.StartApp`), which for a
  completed download shows the completion view directly. On failure of such a download the same
  window opens with the error, **Retry** and **Close**.
- **Contents:** heading "Download complete"; rows Address (URL, middle-ellipsis, copy button), Saved to
  (full path, copy button), File size, Time taken (from first start to completion, excluding pauses
  when known; with `active_ms` also the average speed of the whole download on the same line:
  "3m 00s · average 7.6 MB/s"; `downex info` prints it as "Average speed:" after "Time taken:";
  a restart or "Download again" starts the time over with the bytes; a completed stream's average
  is its file's size over the time, so it can differ from the live one by the container's overhead),
  and for media: format and quality. Buttons **Open**, **Open with…**, **Open folder**,
  **Close**; checkbox "Don't show this dialog again" (turns `show_complete_dialog` off with a full
  settings round-trip). Enter = Open, Esc = Close.
- **Open with…:** Windows `SHOpenWithDialog` (shell32) with `OAIF_EXEC`, so the user picks an app
  (never through `cmd.exe`); the existing executable-confirmation rules apply first. Linux: later.
- **One window per download** (the §7 single-instance rule), so a completion popup never duplicates an
  open progress window.
