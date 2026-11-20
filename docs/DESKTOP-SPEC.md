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
- **Standalone Confirm download window:** a hand-off the user just started in the browser is confirmed
  in a compact window of its own, as a download manager's dialog is (`downex-ui --confirm <id> --raise`,
  §7 "Above the browser"): a top-level window titled "Confirm download" with the system's title bar and
  its own taskbar button, about the dialog's size and fitted to it (as wide as the dialog, 44rem for a
  file, 56rem with quality cards; as high as its content, within the work area; no maximise button),
  holding the same dialog filling it (no scrim, no shadow, no close button of its own: the title bar
  closes it): the quality cards, the audio track, the part, file name, category, queue, Save to, and
  **Cancel download**, **Download later**, **Start download**. The main window is not shown, restored or
  brought forward: it stays hidden in the tray, minimised or wherever it is. The window shows one
  download at a time, oldest first: the ids asked for, and the browser's downloads awaiting confirmation
  that came in the same burst (ids from the oldest asked for on); a later `--confirm <id> --raise` goes
  to the open window. **Start download**, **Download later** and **Cancel download** go to the next one,
  and close the window when none is left; **Start download** also opens the download's progress window
  (when `show_progress_window` is on and it starts now), asked for before the confirm window closes, so
  that it opens even when the confirm window is the process's only window (Downex was not running, or
  only in the tray). The window waits for its own download (it never closes before the download it was
  opened for has been shown). Closing the window (its title bar, Escape)
  leaves the download, and any left in the burst, awaiting. A download confirmed or removed elsewhere
  leaves the window. While the window is open the main window (if it is open) opens no confirmation by
  itself and takes away its dialog of a download the window shows; the user can still review an
  awaiting download in the main window (its list, the details pane), as before. A `--confirm <id>`
  without `--raise` (other sources) is the main window's dialog as before. The Wails/WebView window
  (`-WebViewUI`) has no standalone window: it shows the confirmation in its main window.
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
- A hand-off whose link may continue a download whose link expired (§10) is held first: its window,
  progress window or notice comes only once downexd has checked it (at most 15 s); a held download
  awaiting confirmation is not asked about (`renew_check`). With `renew_links = ask` a matching one
  shows the offer card of §10 at the top of this dialog.
- A web page's video (docs/SITE-MEDIA-SPEC.md §5.4) is confirmed in the same dialog: it shows "Page", the
  title and "<site> video · <length>", and its quality picker lists every offered MP4 quality, best first,
  with sizes (exact, `~` for the site's estimates, else "about" from the bit rate); an audio select only
  when several MP4 audio tracks exist. A video listed without any audio track has no sound
  (SITE-MEDIA-SPEC §5.3, amendment 2026-11-18): its card's name and tooltip add "No sound", the audio
  line reads "This video has no sound." and Add's footer "· no sound". Another quality keeps the container, so the name keeps `.mp4` (a
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
- **Several videos** (amendment 2026-11-18, `SITE-MEDIA-SPEC.md` §3.12; `ui/views/add/items.go`, texts in
  `ui/core/media/items.go`): a web page with several videos (a carousel, a news page) shows the field
  "Video" with "Video 2 of 3" beside its label and a select of the page's videos ("Video 2", the title
  bidi-escaped, the length), and the note "2 other items not listed: photos, links or videos Downex
  cannot save." when the page has other items. The window never loads the page's thumbnails (the
  card's poster stays a placeholder). Confirm and Add → Video put it above the quality cards, in their
  column (Confirm without quality cards: under the video card), so Confirm fits the small windows at
  16 px text as the dialog of one video does. Choosing another video asks downexd for its qualities (ProbeMedia with `entry_id`, retried
  while it answers `tools_installing`/`site_resolving`; Confirm: "Getting video info…" beside the label,
  the buttons waiting): the card, the quality cards, the audio track and the part section follow the
  answer, and a proposed name follows the video ("Video by example 2.mp4" when another item has the
  same title; a name the user typed stays), made by downexd's own rule (`transport.MediaFileName`:
  "24/7 news" → "24_7 news.mp4", long titles shortened), so the file check never refuses it. Start download / Download later send
  `ConfirmRequest.entry_id` (downexd resolves the page again for it and makes the final name), Add
  sends `AddMediaRequest.entry_id`. A failure ("this item is no longer on the page") shows under the
  form and keeps the video shown. `downex media --site --item ID` does the same from the CLI.
- **Audio pages** (amendment 2026-11-18, `SITE-MEDIA-SPEC.md` §3.13; `ui/core/media` `AudioOffer`,
  `ui/views/add`): a page without any video (its probe's container `m4a` or `mp3`, audio tracks
  only) shows in Add → Video and Confirm download the hint "This page has no video: its audio is
  saved as .m4a." (".mp3" for an MP3) and the audio track: a select of the tracks ("Audio track",
  each with its codec, bit rate and size) when the page offers several, else a line, as for an
  audio-only stream; no quality cards and no Part section, the card's chip M4A or MP3. The chosen
  track is sent as the audio track (`audio_track`, no `video_track`), the proposed name ends in
  `.m4a`/`.mp3` (a typed name gets it too), and the download is filed under Audio. *Amended
  2026-11-18 (review):* Add → Video's button says "Add audio"; Confirm download's description is
  "Audio from the browser is waiting. Choose its quality and where it is saved, then start it." and
  its name hint "Size: … · saved as M4A" (or MP3); a page's items are tracks ("Track", "Track 1 of
  3", "Track 1 · title", "… links or audio Downex cannot save.").
- **A part of the video** (`MEDIA-SPEC.md` §8; `SITE-MEDIA-SPEC.md` §5.3/§5.4 for the texts): Confirm
  download and Add → Video (once qualities are found) have the section "Save only part of the video"
  (a check box; checked: Chapter when the video has chapters, From, To, the note and one line of
  error). Confirm sends `ConfirmRequest.clip` when the part differs from the proposal (0–0 = the whole
  video), and its name follows the part while the name is the proposed one; Add sends
  `AddMediaRequest.clip`. Tab order: the check box, then Chapter, From, To; Enter submits; the error is
  announced. `downex confirm <id> --from t --to t | --chapter N | --whole` does the same. While the
  part is on and valid, every size the dialog shows is the part's estimate with "~" (`ClipSize`, B4;
  the browser panel's numbers): each quality card (and its accessible name), the audio tracks, "Size:"
  and Add's footer, so the chosen card and "Size:" agree; unknown sizes stay unknown. The video card's
  length chip shows the part's length beside the whole's, "8:28 of 31:54" (read "8:28 selected of
  31:54", the part's times as its tooltip). Off, the whole video, or a form with an error: the whole
  video's sizes and length. They follow every edit of From, To and Chapter. The details pane shows a
  part's download the same way: "YouTube video · 8:28 of 31:54", and the part's sizes in its Video and
  Audio lines (the list, the progress window and notifications show downexd's size, already the part's).

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
  something changes; *Polite mode, POLITE-SPEC §8:* " · Polite: slowed" is appended while it slows
  downloads). Left double-click: open the UI. Menu: **Open Downex**, **Pause all**, **Resume all**,
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
- No balloon for a completion or failure that a progress window shows: a run whose progress window
  downexd opened, the confirming window opened (`ConfirmRequest.progress_window`), or that window
  marked while it ran, gets the download complete dialog (§8) instead. When that window cannot be
  opened, the balloon is shown after all.
- The final file check of a completed download (§9.7) produces "Check this download": "<name> is not
  what its name says." for DANGER (always, error style) and "<name> may not be the file you wanted."
  for CAUTION (only while `notify_on_complete` is on); a NOTE produces none.
- Awaiting-confirmation downloads that could not open the UI produce "Confirm download: <name>".
- A failure whose link expired (§10, `notify_on_failure`) reads "Link expired" with "<name>\nGive it a
  new link in Downex. The data received so far is kept." (without the last sentence when nothing was
  received). A download that continues with a new link downexd found for it produces "Download
  continues": "<name>\nIt continues with the new link." (`notify_on_complete`; never coalesced).
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

**Above the browser:** a download the user starts in the browser (source browser: an add, a media or
video add, a played video, a rescue) shows its window at once above the window the user works in:
its progress window while `show_progress_window` is on and it starts at once, its standalone Confirm
download window (§1) while `confirm_browser_downloads` is on. Otherwise (both off, or the add waits in its
queue) nothing shows: the main window is never shown, restored or brought forward for a browser add.
downexd starts these with `--raise` (`downex-ui --progress <id> --raise`, `--confirm <id> --raise`);
only such a start raises a window, and so does the download complete dialog (§8) of a browser download
whose progress window showed that run. A clipboard add, a CLI or window add, and the complete dialog of
any other run never do: they open as before, and Windows only flashes their taskbar button when the user is busy
in another app. The native-messaging host, started by the browser, hands the foreground right on
(`AllowSetForegroundWindow(ASFW_ANY)`) before it relays an add or a rescue; when Windows still refuses
the foreground, the `--raise` window is restored and placed at the top of the normal windows without
activation (`HWND_TOPMOST` then `HWND_NOTOPMOST`, `SWP_NOACTIVATE`), so it is never left always on top
and the keyboard focus stays in the browser. macOS asks for the activation and orders the window front
regardless of it (`orderFrontRegardless`) when it is not granted.

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
While the download is unfinished with a file check of DANGER or CAUTION (§9.1: its EARLY result, or
before its first bytes the NAME stage), its note (§9.7) sits under the rows; an EARLY note ends with
"Downex checks it again when the download completes."
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
"Don't show this again" checkbox that turns `show_complete_dialog` off. Executables, names with hidden
text-direction characters and files whose file check is DANGER (§9.7) keep the open-confirmation rules.
On failure the window stays open with the error and **Retry** (Resume) / **Close**.

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
  as `Download.completion_dialog`). downexd marks it itself, at the start of the run, when it opens
  the progress window (§7) or when a confirmation says its window opens it
  (`ConfirmRequest.progress_window`), so a quick download or failure never outruns the window's mark. Queue/scheduled/CLI downloads only get the tray notification.
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
  and for media: format and quality, and for a part of a video the row "Part" ("1:20 – 2:45", tooltip
  "Decoding starts at the keyframe at 1:17.4; players that ignore edit lists start there.";
  `MEDIA-SPEC.md` §8, `SITE-MEDIA-SPEC.md` §5.4; the details pane shows it too); a file check (§9) adds the row "Contents" ("PNG image · named
  as JPEG image") and, for DANGER or CAUTION, its note between the rows and the checkbox. Buttons
  **Open**, **Open with…**, **Open folder**, **Close**; checkbox "Don't show this dialog again" (turns
  `show_complete_dialog` off with a full settings round-trip). Enter = Open, Esc = Close. For a DANGER
  file, Open (Enter) and Open with… go through the Open dialog (§9.7). The daemon never opens the
  dialog only because of a file check: who gets it is unchanged.
- **Open with…:** Windows `SHOpenWithDialog` (shell32) with `OAIF_EXEC`, so the user picks an app
  (never through `cmd.exe`); the existing executable-confirmation rules apply first. Linux: later.
- **One window per download** (the §7 single-instance rule), so a completion popup never duplicates an
  open progress window.

## 9. File check (disguise detector)
Downex warns when a downloaded file is not what its name says: a program named like a video, a web
page (an error or sign-in page) saved instead of the file, an archive that holds a program behind a
media name. Code: `internal/filecheck` (pure Go, the standard library and `internal/model` only).

### 9.1 Purpose, scope and stages
- **F1. Advisory and read-only.** The check never changes, renames, moves, quarantines or deletes a
  file, never blocks or fails a download and never changes its state. Nothing leaves the computer
  (no network, no telemetry). Logs carry the download id, level, reason and content id, never file
  or member names. No text ever says a file is "safe" or "clean" because of a check.
- **F2. Never auto-open.** Downex opens a downloaded file only on the user's Open or Open with…. Any
  later "open when done" behaviour skips DANGER and CAUTION files and files with an alarming scan
  (M6-SPEC §3).
- **Levels:** NONE (nothing said), NOTE (details and CLI only), CAUTION (a warning; Open is not held),
  DANGER (a red warning, a notification, Open and Open with… ask first).
- **Stages:** NAME (the name, the served type or the requester's MIME hint; computed by downexd on
  every read of a download that is not completed and has no EARLY result; never stored), EARLY (the
  first 64 KiB of a running download, `engine.Progress.Head`, ENGINE-SPEC §9; kept in downexd's memory
  until the next run starts), FINAL (the completed file: head, tail and archive listing; stored with
  the record). FINAL replaces EARLY, EARLY replaces NAME; a completed download shows only its FINAL
  result (`filecheck.Shown`). Downloads completed before this feature or while the setting was off
  have none.
- **Kinds:** file downloads get all three stages; media downloads (HLS/DASH, web pages' videos), whose
  MP4 Downex assembles itself, get the NAME stage only.

### 9.2 Signatures
`filecheck.Sniff` reads the head (S = strong, W = weak; a weak match never causes CAUTION or DANGER):
- **Programs:** `pe` (S): "MZ", e_lfanew = u32le@0x3C with 0x40 ≤ e_lfanew ≤ len−24 and "PE\0\0"
  there; a DLL when u16le@(e_lfanew+22) & 0x2000; "MZ" without that is `mz` (W). `elf` (S): 7F "ELF",
  bytes 4 and 5 ∈ {1,2}, byte 6 = 1. `macho` (S): FEEDFACE/FEEDFACF/CEFAEDFE/CFFAEDFE. `macho-fat`
  (S): CAFEBABE/CAFEBABF with u32be@4 in 1..30 and (u32be@8 & 0xFFFFFF) ∈ {7,12,18}; CAFEBABE with
  u16be@6 ≥ 45 is `java-class` (not a program: it does not run when opened). `script` (S): "#!",
  spaces or tabs, "/". Compound files (D0 CF 11 E0 A1 B1 1A E1): the root entry at
  (first directory sector + 1) << sector shift (9 or 12) has the CLSID 000C1084-… (`msi`), 000C1086-…
  (`msp`) or 000C1082-… (`mst`), else it is `ole` (a document); the final check reads the root entry
  when it lies beyond the head. `lnk` (S): u32le@0 = 0x4C and the shell-link CLSID at 4. `xar` (S):
  "xar!". `deb` (S): "!<arch>\n" + "debian-binary" at 8 (plain "!<arch>\n" is `ar`, W). `rpm` (S):
  ED AB EE DB.
- **Archives** (all S): `zip` (PK 03 04 / 05 06 / 07 08), `crx` ("Cr24", a program container), `rar5`
  (52 61 72 21 1A 07 01 00), `rar4` (52 61 72 21 1A 07 00), `7z` (37 7A BC AF 27 1C), `gzip`
  (1F 8B 08), `xz` (FD "7zXZ" 00), `bzip2` ("BZh"[1-9] + 31 41 59 26 53 59), `zstd` (28 B5 2F FD),
  `tar` ("ustar" at 257), `cab` ("MSCF" + 4 zero bytes).
- **Disk images** (all S; checked first, so a hybrid image's boot code is not read as anything else):
  `udf` ("BEA01" at 32769 and "NSR02"/"NSR03" in a following 2,048-byte descriptor), `iso9660`
  ("CD001" at 32769, 34817 or 36865), `vhdx` ("vhdxfile"), `vhd` ("conectix" at 0, or at size−512 in
  the final check's tail). DMG and CHM are not recognised: no public specification is cited for
  their structures, so their content is unknown (which never warns); their extensions still count
  for the name rules.
- **Documents:** `pdf` ("%PDF-" starting within bytes 0..1023), `rtf` ("{\rtf"), `ole` (above).
- **Media:** `isobmff` (u32be@0 ≥ 8 or = 1, box type at 4 ∈ ftyp styp moov moof mdat free skip wide
  sidx pnot uuid; the ftyp major brand gives the family: heic heix hevc hevx heim heis mif1 msf1 →
  HEIF image, avif avis → AVIF image, "M4A " "M4B " "M4P " "F4A " → audio, "qt  " → QuickTime,
  3gp*/3g2* → 3GPP, "crx " → camera raw image, else an MP4 video), `jp2`, `webm`/`matroska` (1A 45 DF
  A3; DocType 0x4282 in the first 64 bytes "webm" or not), RIFF `riff-wave` / `riff-avi` /
  `riff-webp` (an image) / `riff-acon` (an image), `ogg`, `flac`, `asf`, `flv` ("FLV" 01), `mpeg-ps`
  (00 00 01 BA), `mpegts` (0x47 at 0, 188, 376; checked after GIF), `m2ts` (0x47 at 4, 196, 388),
  `id3` ("ID3" + version 2..4); W: `mpeg-audio` (frame sync, layer ≠ 0, bitrate index ≠ 15, sample
  rate index ≠ 3), `adts` (FF F1 / FF F9).
- **Images:** `png`, `jpeg` (FF D8 FF), `gif` (GIF87a/GIF89a), `tiff` (II*\0 / MM\0*), `psd` ("8BPS");
  W: `bmp` ("BM" + 4 zero bytes at 6), `ico` (00 00 01 00, count 1..255). A UTF-16 BOM is text.
- **Web and text:** `html` (after an optional UTF-8 BOM and 09 0A 0C 0D 20, one of WHATWG MIME
  Sniffing's HTML patterns, case-insensitive, followed by 0x20 or 0x3E; "<?xml" with "<html" in the
  first KiB), `svg` ("<svg" or "<!DOCTYPE svg" at the start or right after leading complete
  "<!-- … -->" comments and whitespace (a licence header of any length within the head), or "<svg"
  in the first KiB after "<?xml"; checked before the HTML patterns),
  `xml` ("<?xml"), `text` (a UTF-16 BOM, or valid UTF-8 with a cut last rune allowed, no NUL and no C0
  controls but 09 0A 0C 0D 1B: S at 64 bytes or more, or when the head is the whole file; W at 16..63
  bytes of a longer file; nothing below). Anything else is unknown.

### 9.3 Archive listing and bounds (FINAL only)
- **ZIP** (PKWARE APPNOTE): the end of central directory record is searched backwards in the tail
  (its comment must fit); Zip64 through the locator at EOCD−20 and the Zip64 record (just before the
  locator, else at its stored offset); multi-disk archives: listing incomplete. Directory start =
  record position − directory size; a difference from the stored offset is prepended data (a
  self-extractor). The first directory entry must carry 50 4B 01 02. Entries: name length u16@28,
  extra u16@30, comment u16@32; names are UTF-8 when bit 11 is set or the bytes are valid UTF-8
  (many archivers write UTF-8 without the flag), else IBM code page 437; names ending in "/" are
  folders. A ZIP is also looked for at the end of a PDF, JPEG, PNG, GIF or ISO-BMFF file (rule A3).
- **RAR 5.0** (RARLAB): blocks from offset 8 (header CRC32, vint header size, type, flags, extra and
  data sizes); type 2 file headers give the names; type 4 (encrypted headers) stops the walk
  (incomplete); type 5 ends it; a bad header CRC stops it. RAR4, 7z, gzip, xz, bzip2, zstd and tar
  are not listed.
- **CAB** (MS-CAB): cFiles = u16le@28 and coffFiles = u32le@16, then CFFILE entries (16 bytes and a
  NUL-terminated name; attribute 0x80 = UTF-8, else Latin-1).
- **Container kinds** from member names, program containers first (several are Open Packaging files
  that also carry "[Content_Types].xml"): AndroidManifest.xml → apk; AppxManifest.xml or
  AppxMetadata/ → appx; extension.vsixmanifest → vsix; a root *.nuspec → nupkg; Payload/*.app/ →
  ipa; *.dist-info/ → whl; META-INF/MANIFEST.MF → jar; a root manifest.json → xpi (a browser
  extension); [Content_Types].xml → ooxml; content.xml + META-INF/manifest.xml → odf;
  META-INF/container.xml → epub. Program containers (apk, jar, appx, ipa, xpi, nupkg, vsix, whl,
  crx) are programs; their members are not judged.
- **Members:** a program member's base name has a run extension (§9.4) or it lies inside
  "*.app/Contents/MacOS/"; a disguised member is a program member whose base name breaks N2 or N3 or
  whose path holds bidi controls. The first disguised member is kept (else the first program
  member) with the number of program members; a member name is kept to 255 bytes. In an EPUB
  (the container kind, not only the name), plain ".js" members are not program members: EPUB 3
  content documents may carry JavaScript that the reading app runs, and the system never opens
  them as programs (a disguised one still counts).
- **F3. Bounds:** head 65,536 bytes (`filecheck.HeadBytes` = `engine.HeadBytes`); tail
  min(size, 65,557); ZIP directory ≤ 32 MiB and ≤ 65,536 entries; RAR5 ≤ 10,000 headers, ≤ 4 MiB of
  header bytes, each ≤ 2 MiB; CAB ≤ 65,535 entries and ≤ 16 MiB; compound files one directory
  sector; a final check ≤ 30 s (`filecheck.Timeout`), its context checked before every read. A read
  error, the deadline or a bound judges what was found so far (`listing_incomplete` when a listing
  stopped); a read error before the head gives no result (logged, nothing stored).

### 9.4 Claims, run extensions, decoys
- **Claim** (`internal/filecheck/claims.go`): the name's last extension (lower case, after trailing
  dots and spaces are trimmed, as Windows does) names the contents it accepts; e.g. mp4/m4v/m4a/mov/
  3gp → ISO-BMFF video or audio; mkv/webm → Matroska or WebM; mp3 → ID3 or MPEG audio; jpg → JPEG;
  ico → icon or PNG; pdf → PDF; doc/xls → OLE, RTF, HTML, XML or text (Office web exports);
  ppt/msg/pub/vsd → OLE; docx/xlsx/pptx/vsdx → a ZIP (plain or OOXML) or OLE (a password-protected
  file is a compound file); odt/ods/odp/odg → ZIP or ODF; epub → ZIP or EPUB; text names (txt log md
  csv json yaml ini srt vtt m3u8 nfo torrent …) → text, HTML, XML, SVG or a script; html/htm/mht →
  HTML, text, XML; ts/m2t → transport stream, text, XML, HTML or a script (TypeScript and Qt
  translations use .ts); zip/zipx/cbz and apk/jar/xpi/appx/msix/nupkg/vsix/whl/ipa → any ZIP; crx →
  crx or ZIP; rar/cbr → RAR; iso → ISO 9660 or UDF; img/dmg → those or unknown; exe/dll/scr/cpl/sys →
  PE or MZ; msi/msp/mst → installers or OLE; com → anything; so/bin/run/appimage → ELF, script or
  unknown; sh/bash/zsh/command → script or text; py/pl/rb/ps1/bat/cmd/vbs/js → text or script. Any
  other extension, or none: no claim; a specific served Content-Type then claims (video/ and audio/
  any media, image/ any image, text/ text; mapped types such as application/zip or application/pdf
  as their extension); generic types (application/octet-stream, binary/octet-stream,
  application/force-download, application/x-download, application/unknown, "") claim nothing.
- **Run extensions** (programs, scripts, installers, shortcuts and disk images the system runs,
  mounts or follows when opened): exe scr com pif cpl msi msp msix msixbundle appx appxbundle
  appinstaller application gadget msc bat cmd ps1 psm1 vbs vbe js jse wsf wsh wsc hta sct jar lnk url
  website scf reg chm hlp settingcontent-ms library-ms search-ms xll diagcab iso img vhd vhdx dmg pkg
  mpkg app command tool terminal workflow scpt scptd applescript webloc fileloc inetloc sh run appimage
  desktop. `ui/host`'s `TestRunExtsAreRisky` keeps each of them one that the open gate asks about
  (RiskProgram or RiskContent); the macOS-only ones are listed there as exceptions.
- **Decoys** (the inner extension of a double extension): pdf doc docx docm xls xlsx xlsm ppt pptx
  pptm odt ods odp rtf txt csv epub jpg jpeg png gif bmp webp heic tif tiff mp4 m4v mov avi mkv webm
  wmv flv mpg mpeg 3gp mp3 m4a wav flac aac ogg wma zip rar 7z html htm.

### 9.5 Verdict rules
Name rules (every stage):
- **N1.** The proposed name held U+202A–U+202E or U+2066–U+2069, which sanitising removed
  (`name_controls`; U+200E, U+200F and U+061C never count): DANGER `name_hid_direction` when the
  final extension is a run extension, else NOTE `name_had_controls`. downexd notes it at Add (the
  requested name, else the URL's last segment) and the engine when it picks the name
  (Content-Disposition or URL); a Confirm with another name clears it.
- **N2.** Double extension: outer = the last extension, inner = the token before it with trailing
  whitespace and filler trimmed (U+0020, U+00A0, U+2000–U+200A, U+202F, U+205F, U+3000, U+3164,
  U+2800). Outer a run extension and inner a decoy: DANGER `double_extension`, or `name_padding`
  when filler was trimmed.
- **N3.** A run of ≥ 3 filler characters in a name whose outer extension is a run extension: DANGER
  `name_padding`.
- **N4.** Outer a run extension and the served type (at the NAME stage the requester's MIME hint) is
  video/*, audio/*, image/*, application/pdf or an Office type: DANGER `served_as_other`; at EARLY
  and FINAL not when the content is a strong match of the served type's family (an .exe that really
  is an MP4 is R7's NOTE).

Content rules (EARLY and FINAL), when the claim does not accept a strong content. Families: program
(pe, mz, elf, Mach-O, msi/msp/mst, lnk, xar, deb, rpm and the program containers), script, disk,
archive, web (html, xml, svg), text, media, image, document (pdf, ole, rtf, ooxml, odf, epub). The
first that matches:
- **R1.** Content program, claim not a program: DANGER `program_disguised`.
- **R2.** Content script, claim media, image, document, archive or disk: DANGER `program_disguised`.
- **R3.** Content disk, claim media, image, document, text or web: DANGER `disk_image_disguised`;
  claim archive: CAUTION `archive_disguised`.
- **R4.** Content archive, claim media, image or document: DANGER `archive_with_program` when it holds
  program members, else CAUTION `archive_disguised` (the listing incomplete for formats that are not
  listed); claim text or web: NOTE `other_type`; any other claim: NOTE `archive_kind`.
- **R5.** Content web, claim media, image, document, archive, disk or program: CAUTION `web_page`.
- **R6.** Content text, claim media, image, document, archive, disk or a binary program (exe dll sys
  scr cpl ocx efi mui msi msp mst lnk so bin run appimage dylib pkg deb rpm): CAUTION
  `text_not_binary`.
- **R7.** Anything else not accepted: NOTE `other_type`. Weak and unknown contents: nothing.

Archive rules (FINAL), on accepted contents:
- **A1.** A ZIP-based document claim (OOXML, ODF, EPUB extensions) with program members: DANGER
  `program_inside_document`.
- **A2.** A plain archive claim (zip, zipx, cbz, rar, cbr, cab) with a disguised member: DANGER
  `disguised_member` (plain program members are nothing: setup.zip holding setup.exe is normal).
- **A3.** A PDF, JPEG, PNG, GIF or ISO-BMFF head with a valid ZIP directory at the end: DANGER
  `appended_archive_with_program` with program members, else NOTE `appended_archive`. A PE head with
  a ZIP at its end (a self-extractor) is never judged by A3.

**Combining:** the highest level wins; on a tie the first of N1–N4, R1–R7, A1–A3. One reason, one
member and one program count are reported. Kept false-alarm guards (each tested): docx/xlsx/pptx =
ZIP; encrypted xlsx = OLE; xls = HTML or XML; doc = RTF; apk/jar/xpi/appx/nupkg/whl = ZIP; .ts = text
or TS; .m4a named .mp3, a .jpg that is WebP or PNG, a .gif that is MP4 → NOTE; setup.zip holding
setup.exe, a self-extracting .exe, tar.gz, jquery.min.js, python-3.12.0-amd64.exe, an RTL name with
RLM, a .txt starting with "#!/bin/sh", a raw .img, a hybrid .iso, an EPUB 3 that carries scripts and
an .svg that starts with a long licence comment → nothing.

### 9.6 Texts
Headings: DANGER "This file is not what its name says"; CAUTION "This may not be the file you wanted";
NOTE none. Summaries (`filecheck.Summary`; labels carry their article; names in “” with bidi controls
shown as ⟨U+…⟩):
- `program_disguised`, `archive_disguised`: "This file is {content}, not {claim}."
- `double_extension`: "“{name}” is {outer} whose name imitates {inner}."
- `name_padding`: "“{name}” is {outer} whose real extension is hidden behind spaces."
- `name_hid_direction`: "The name this file came with used hidden characters that reverse text, a trick
  to make {outer} look like another type of file. Downex removed them; the real name is “{name}”."
- `name_had_controls`: "The name this file came with contained hidden text-direction characters;
  Downex removed them."
- `served_as_other`: "The server said this was {served} ({served type}), but it is {content}."
  ({served}: video/* "a video", audio/* "an audio file", image/* "an image", application/pdf "a PDF
  document", text/html "a web page", text/* "a text file", Office types "a document"; {content} is
  the name's label at the NAME stage.)
- `archive_with_program`: "This file is {content}, not {claim}, and it holds a program: “{member}”."
  (" and {n−1} more" before the period when it holds more programs.)
- `program_inside_document`: "This {claim} holds a program: “{member}”." (likewise)
- `disguised_member`: "This archive holds “{member}”, a program whose name imitates {inner}." ("another
  type of file" without a decoy)
- `appended_archive_with_program`: "This {claim} also carries a ZIP archive with a program inside:
  “{member}”."; `appended_archive`: "This {claim} also carries a ZIP archive."
- `disk_image_disguised`: "This file is {content}, not {claim}. A disc image can hold programs."
- `web_page`: "This file is a web page, not {claim}. The server may have sent an error or sign-in
  page instead of the file; check the address or sign in on the site, then download it again."
- `text_not_binary`: "This file is plain text, not {claim}. The server may have sent a message instead
  of the file."
- `other_type`, `archive_kind`: "This file is {content}, though its name says {claim}."

Detail (`filecheck.Detail`): DANGER "Files that pretend to be another type are a common way to spread
malware. Don’t rename or run it unless you trust where it came from."; a stopped listing adds "Downex
could not list every file inside it."; an EARLY DANGER or CAUTION adds "Downex checks it again when the
download completes." Labels (`claims.go`): e.g. "a Windows program", "a Windows program library
(DLL)", "a Linux program", "a Mac program", "a script", "a Windows installer", "a Windows shortcut",
"a ZIP archive", "a RAR archive", "a disc image (ISO)", "a virtual hard disk", "a PDF document", "an
Office document (97–2003)", "a Word / Excel / PowerPoint document", "an MP4 video", "an MPEG-4 audio
file", "a WebM video", "an MP3 audio file", "a PNG image", "a JPEG image", "a web page", "a text
file"; claims from extensions read e.g. "a Word document", "an Excel workbook".

### 9.7 What the window, the CLI and the tray do
- **NOTE:** the details' and complete view's row "Contents" ("PNG image · named as JPEG image") and
  the CLI only.
- **CAUTION:** also a warn note (heading, summary, detail) in the details pane, the progress window's
  Status tab (EARLY, or NAME before it) and the complete view (FINAL); a completed row reads "Check
  file" in the warn ink with the warn alert mark (an unfinished one, by its EARLY or NAME result,
  keeps its status text and gets the mark), and stays
  in the Completed filter; the row's tooltip is the summary; the notification while
  `notify_on_complete` is on. Open is not held.
- **DANGER:** everything CAUTION shows, in the danger ink, fail note and danger alert mark; the
  details note adds "Remove and delete file…" (the Remove dialog with the file's deletion chosen;
  nothing is deleted without it); the notification always (error style); Open and Open with… always
  ask first, from every path (row Enter or double-click, toolbar, menus, details, palette, the
  complete view's Enter: one gate, `categories.NeedsOpenConfirm`): the Open dialog titled "This file
  is not what its name says", its description the summary, a blank line and the detail, the danger
  icon, Cancel focused first, the action "Open anyway", "Run anyway" (a program name) or "Choose app
  anyway…" (Open with…). A program that is not disguised keeps "Run this program?". The host's
  native question for programs (`ui/host` Files) still follows, as for every program. The Wails
  window asks a native question with the same title and text before it opens a DANGER file.
- **Confirm dialog:** under File name a fail note with the DANGER heading and the NAME stage's summary
  of the name as it stands (the daemon's result for the proposed name, an edited name checked in
  the window with the MIME hint); DANGER only; the buttons and the default button do not change.
- **Settings → Integration → Safety**, first row: "Warn when a file is not what its name says", hint
  "Downex compares the start of each downloaded file, and the list of files inside archives, with its
  name, and asks before opening one that pretends to be another type. It never changes or deletes
  files."
- **CLI:** `info` rows "Contents" ("Windows program (named as MP4 video)") and "File check" ("Danger:
  {summary}", "Caution: …", "Note: …"; EARLY "Danger (while downloading): …"); `ls` status "check
  file" for a completed DANGER or CAUTION download, `--json` `fileCheck`; `settings --check-files
  on|off`; `downex check <file|id> [--json]` (local and read-only, never starts downexd: "File:",
  "Contents:", "Named as:", "Server said:" for a download, "Result: {Level}: {summary}" or "Result:
  no warning"; exit status 0 when the check ran, 1 on an error; a download must be completed); `get`
  and `resume --local` check the completed file, store the result and print "Warning: {summary}" for
  DANGER or CAUTION. The CLI reads `check_files` from downexd's settings file next to the database.
- **Tray / menu bar:** "Check this download" (§4).

### 9.8 Mark of the Web and the antivirus scan
- Order in `finish`: file check → mark (`mark_downloaded_files`) → scan (`av_scan_enabled`).
- The mark is written whatever the check says (ZoneId=3, quarantine and origin attributes), so the
  system's and Office's protections still apply when the shell opens the file; the scan runs
  whatever the check says, and its note stays in `Download.error`. Both are shown when both alarm,
  each with its own notification.

### 9.9 Setting, storage, reason codes
- `check_downloaded_files` (Settings field 27; `settings.json` `desktop.check_files`; default true,
  also when missing from an old file). While it is off downexd computes nothing and Get/List/Watch
  leave `file_check` empty, also for stored results, which stay in the database and show again
  when it is turned back on; turning it off or on sends every download again on Watch (`updated`),
  so no window keeps an outdated warning. Downloads that complete while it is off are never checked later
  (`downex check` can check them).
- Proto: `Download.file_check` (33), message `FileCheck` (level, stage, reason, content,
  content_label, claim_label, served_type, member, program_members, listing_incomplete, summary,
  detail), enums `FileCheckLevel` and `FileCheckStage`. Store migration 8: `served_type`,
  `name_controls`, `file_check` (JSON, written only by `Store.SetFileCheck`; inserts store none and
  updates keep it, the `queue_id` rule; invalid JSON reads as none). A completed run's check
  replaces the stored one; a completion whose check did not run (off, media, unreadable) clears it.
- Reason codes ([a-z_]{1,64}, stable): program_disguised, double_extension, name_padding,
  name_hid_direction, name_had_controls, served_as_other, archive_with_program,
  program_inside_document, disguised_member, appended_archive_with_program, appended_archive,
  disk_image_disguised, web_page, text_not_binary, archive_disguised, archive_kind, other_type. No
  new RPC errors.

### 9.10 Sources (public)
Microsoft PE/COFF specification; System V ABI (ELF); Apple's Mach-O file format reference (magic
numbers only); [MS-CFB]; [MS-SHLLINK]; [MS-CAB]; the Microsoft VHD Image Format Specification;
[MS-VHDX]; PKWARE APPNOTE.TXT; RARLAB "RAR 5.0 archive format"; 7-Zip's 7zFormat.txt (signature only);
RFC 1952 (gzip); the xz file format; RFC 8878 (Zstandard); POSIX ustar; ECMA-119; ECMA-167/OSTA UDF;
ISO 32000 (PDF); ISO/IEC 14496-12 (ISO-BMFF); RFC 8794 and RFC 9559 (EBML, Matroska); RFC 3533 (Ogg);
RFC 9639 (FLAC); ISO/IEC 13818-1 (transport stream); ISO/IEC 11172-3 (MPEG audio header); ID3v2; W3C
PNG; ITU T.81 and JFIF; GIF89a; the WebP container (RIFF) specification; the ASF specification;
WHATWG MIME Sniffing; the xar format documentation; the LSB RPM format; deb(5); IBM code page 437.

**Not verified** (§9): a false-alarm corpus run of `downex check` over at least 500 benign real files
with no CAUTION or DANGER, and known disguised samples made locally all flagged; real archives (a
RAR5 from a real archiver, an SDK CAB, a real MSI, .lnk and ISO, a universal Mac app in a ZIP); a
browser hand-off whose Content-Disposition holds U+202E (whether the browser strips it before the
relay sends `filename`); the notes, rows and dialogs on the owner's Windows PC at 100–150 % and with
text scaling, and on a Mac (render, VoiceOver reading the note as an alert, the notification);
Defender holding a file at completion; the final check on a slow network share.

## 10. Expired links
A download whose link stops working (it expired, was revoked, or now leads to a web page) keeps its
data and waits for a new link: engine rules in ENGINE-SPEC §7.2-7.3, daemon rules in DAEMON-SPEC §3
("New links"), §5 and §6. Ordinary file downloads only: site videos keep their own refresh
(SITE-MEDIA-SPEC §2.6), streams are out of scope (MEDIA-SPEC §10). The native window (Windows and
macOS, `ui/views`) has everything below; the WebView window does not.

### 10.1 What the window shows
- **List**: status "Link expired" (failed, `error_code` `link_expired`) in place of "Failed"; inline
  action "New link…" (`renew`) in place of "Retry". A held add (`renew_check`) reads "Checking link…".
- **Details pane**: the error note shows the stored error, the code tag `link_expired` and the hint
  "The link stopped working: it expired, was revoked or now leads to a web page. The data received so
  far is kept. Give the download a new link, or open its page and download it again: Downex continues
  it when the new link serves the same file." (without data: "The link stopped working: it expired, was
  revoked or now leads to a web page. Give the download a new link, or open its page and download it
  again."). Buttons: **New link…** (icon `link`), **Open page** (icon `external`, only when the page is
  known), **Retry** (the old link once more).
- **Context menu** of exactly one selected file download that is paused or failed: "New link…" (`renew`,
  icon `link`), then "Open page" (`open-page`, icon `external`) when its page is known, after
  "Restart…". **Palette**: "New link…" (`new-link`) and "Open download page" (`open-page`), right after
  "Download again…" (`commands_test` pins the order).
- **Progress window**: "Failed: Link expired"; **New link…** (primary), **Open page** (when known),
  **Close**. New link… shows the main window's New link dialog: the progress window starts the main
  window with `--renew <id>`, which a running main window takes over (the one window host on Windows;
  Downex.app's instance hand-off on macOS) like `--confirm`.
- **Toasts**: "“<name>” continues with the new link.", "“<name>” downloads again from the start with the
  new link.", "Downex could not open the page.".

### 10.2 New link dialog
`ModalRenew`, small dialog, not an alert, icon `link`.
- Title "New link". Description "Downex continues “<name>” from <received> of <size> after checking that
  the new link serves the same file."; when nothing was received "Downex downloads “<name>” from the new
  link.".
- Field "New link" (its accessible name too), placeholder "https://…", prefilled when the clipboard
  holds exactly one http(s) address (the Add dialog's paste rule). Footer link button "Open the download
  page" when the page is known: it opens the browser and the dialog stays.
- Buttons **Cancel** (Esc) and **Continue download** (Enter). While checking, the primary reads
  "Checking…", the dialog is busy and the field read-only.
- Success: the dialog closes with the toast of §10.1 (restarted: the second one). When the download is
  renewed elsewhere while it is open (seen as queued or downloading without an error code), it closes
  with the first toast.
- Refusals, an inline danger note under the field (a live region), nothing changed:
  `renew_name` "The new link names the file “<new>”. Downex keeps the name “<old>”." (primary "Continue
  anyway": sends `allow_name_change`); `renew_unproven` "Too little of the file can be compared to be sure
  this is the same file." (primary "Download from the start": sends `restart_if_unproven`);
  `renew_mismatch` "This link serves a different file: its bytes differ from what was downloaded.";
  `renew_size` "This link's file is <new size>; this download is <size>."; `renew_changed` "The server
  reports another version of the file. Restart the download to get the new version.";
  `renew_no_ranges` "This link's server sends only whole files, so the download cannot continue where it
  stopped. Restart the download to use it."; `link_expired` "This link does not work either: it expired
  or leads to a web page."; `auth_required` "The server asks for a user name and password. Add a site
  login for it, then try again."; InvalidArgument or a link that is not http(s) "Enter an http or https
  link."; anything else the call's message. Editing the link clears the note.

### 10.3 Confirmation offer card
With `renew_links = ask`, an add that proved to serve the same file as a stopped download awaits
confirmation with `renew_offer`. At the top of the confirmation dialog's body (file downloads, inside
the 44rem dialog) a NoteInfo card: title "Same file as a stopped download", text "“<old name>” stopped
at <received> of <size> because its link expired. This link serves the same file." (with another name:
"…This link serves the same data under the name “<new name>”."), button **Continue that download**
(Primary, small): `RenewLink{id: old, from_download: this, start: true, allow_name_change: true}`, then
the dialog closes with the success toast; a refusal shows in the dialog's failure note. The footer stays
as it is: **Start download** downloads this one separately (the offer is dropped).

### 10.4 Open page
`Platform.OpenPage`: only an http or https address with a host, no user information, no control or
space characters, at most 32 KiB, re-serialised from its parsed form (`ui/core/pageurl`), then the
shell's "open" (Windows `ShellExecute`) or `NSWorkspace` (macOS, `dxpOpenURL`). Never automatic: the user
always clicks. `OpenExternal` stays https-only.

### 10.5 Setting
Settings → Downloads (both skins), Select **Expired links**: "Continue with a matching new link"
(`auto`, default), "Ask before continuing" (`ask`), "Don't look for new links" (`off`); hint "Downex
checks that the new link serves the same bytes before it continues a download." CLI
`downex settings --renew-links auto|ask|off`; the window's full settings round trip keeps it.

### 10.6 Browser extension
`add` sends `pageUrl` (the context menu's page, the video file's tab, else the page or frame the
browser names as the referrer; http(s) only, without user name or fragment); `added.renewCheck` shows
the pill "Checking in Downex" (info) with the tooltip "Downex is checking whether this link continues a
stopped download." The extension never learns the outcome and gets no list of expired downloads
(BROWSER-SPEC §2, §4).

**Not verified** (§10): real signed links (an object store's presigned link answering 403, a
file-sharing site answering 403 or redirecting to an HTML page, a release-asset link redirected to an
expiring object host, a CDN answering 410, a host answering 401 without a challenge); the renewed link captured in Chrome, Edge and Firefox 140 within
the 3 s hand-off budget; the offer card and the New link dialog on Windows 11 (100 %/125 %, Narrator)
and macOS 13+ (VoiceOver); Open page in the default browser on both systems; the tray and menu-bar
texts; the progress window's New link… reaching the main window on both systems; a long real download
whose live streams outlive the expiry; the Settings row's fit at 20 px text on the real window.

## 11. Rescue partial file
A download a browser left unfinished (its `.crdownload` or `.part` file) is finished by Downex from
the bytes already saved: engine rules in ENGINE-SPEC §7.4, daemon rules in DAEMON-SPEC §3 ("Rescue"),
§4 and §6, the extension's card and notifications in BROWSER-SPEC §6. The native window (Windows and
macOS, `ui/views`) has everything below; the WebView window does not.

### 11.1 Where the command lives
- **Add** split button's menu, third item: "Rescue partial file…" (icon `restart`), after "Add
  downloads…" and "Add video or stream…".
- **Palette**: `rescue` "Rescue partial file…", right after "Add video or stream…" (`commands_test`
  pins the order); keywords "crdownload part partial browser failed interrupted continue resume
  finish"; description "Finishes a download a browser left unfinished (.crdownload, .part)"; disabled
  with "downexd is not reachable" while stale (the Add rule).

### 11.2 Dialog
`ModalRescue`, 44rem (a file's Confirm), icon `restart`, title "Rescue partial file", description
beside the icon: "Downex continues a download a browser left unfinished, from the bytes already
saved. It first checks that the server still has the same file."
- **Partial file**: the chosen path (mono, at most two lines with an ellipsis, the whole path as its
  tooltip, text-direction controls shown escaped) and **Choose…** (`Platform.PickFile`, title "Choose
  the partial file", in the chosen file's folder, else `default_dir`). After a choice its size is read
  off the UI thread; the hint reads "412.0 MB saved · Chrome partial file (.crdownload)", "· Firefox
  partial file (.part)" or "· partial file". A file that cannot be read: "Downex cannot read this
  file."; an empty one: "This file is empty: there is nothing to finish."
- **Address**: placeholder "https://…"; hint "The download's address. Chrome: right-click it on the
  downloads page, Copy link address. Firefox: right-click it in the downloads list, Copy Download
  Link."
- **Save as**: prefilled with the partial's name without `.crdownload` or `.part`; empty for
  "Unconfirmed N.crdownload", with the placeholder "Named by the server".
- Footer hint "Saved in the partial file's folder."; **Cancel** (Esc) and **Rescue** (Enter, primary,
  disabled until a readable, non-empty file and an http(s) address are given). Without a file:
  "Choose the partial file."; with another address: "Enter the download's address (http or https)."
- While waiting the primary reads "Checking…", the dialog is busy and its fields are read-only.
- Refusals show inline, a live region, nothing changed: under **Partial file** for
  `rescue_not_found`, `rescue_ambiguous`, `rescue_locked`, `rescue_no_access`, `rescue_empty` and
  `invalid_request` (the dialog checks the address itself: not a regular file, or another
  download's own file); under **Address** for the others; the texts of BROWSER-SPEC §6 (R-E7; `internal/rescuetext`), else
  downexd's message without its code.
- Success: the dialog closes, the toast "Continuing “<name>” from <kept>", the list selects the new
  download, and its progress window opens when `show_progress_window` is on and it starts at once
  (the rule of a Resume from the window; downexd never opens one for this rescue).
- It fits 1190×752 and 900×600 at 16 and 20 px text, light and dark, with a long path and an error
  shown (`TestRescueDialogFits`).

### 11.3 Details pane
Row "Rescued" = "<bytes> from a partial file" while `rescued_bytes` > 0, also after completion
(cleared by a restart). A rescued download fails, resumes and restarts as any other file download
(`validator_changed` on a later resume uses the Restart flow).

**Not verified** (§11): the dialog and the picker on the real Windows 11 and macOS windows (fit,
Narrator, VoiceOver); a rescue of a real Chrome, Edge or Firefox partial file chosen in the dialog;
macOS's privacy prompt for Downex Agent in Downloads, Desktop and Documents (MACOS-SPEC §2).
