# Using Downex

The reference for Downex's installers, its command-line client `downex`, the background
service `downexd`, the desktop window and the browser integration. The front page is the
[README](../README.md); the design specifications are the other files in this folder.

Contents: [Install (Windows)](#install-windows) · [macOS](#macos) · [Usage](#usage) ·
[The desktop window](#the-desktop-window) · [The daemon](#the-daemon-downexd) ·
[Queues](#queues-and-the-scheduler) · [Network](#network-proxies-logins-and-host-rules) ·
[Clipboard, file marks and virus scans](#clipboard-file-marks-and-virus-scans) ·
[Updates](#updates) · [Streaming media](#streaming-media-hls-and-dash) ·
[Videos of web pages](#videos-of-web-pages-yt-dlp) · [Videos on every website](#videos-on-every-website) ·
[Browser integration](#browser-integration) · [End-to-end tests](#end-to-end-tests)

## Install (Windows)

`Downex-Setup-<version>.exe` installs Downex for the current user, without administrator rights
(Windows 10 or later, x64; Windows on Arm is not part of the release):

- the programs `downexd.exe`, `downex.exe`, `downex-nmhost.exe`, `downex-ui.exe` and the browser
  extension folders `extension\chrome-mv3` and `extension\firefox-mv3` into
  `%LOCALAPPDATA%\Programs\Downex` (or the folder chosen in Setup);
- a Start-menu shortcut **Downex** (the window), optionally a desktop shortcut, and the entry
  **Downex** in *Settings → Apps* (publisher Downex, version, size, icon);
- the browser registration (`downex-nmhost.exe register`, see [Browser integration](#browser-integration)),
  and it starts `downexd --background`;
- optionally (unchecked by default) *Start Downex when I sign in*, which runs
  `downex.exe settings --launch-at-login on` (see [Launch at login](#launch-at-login)).

**First run.** Setup is not code-signed, so Windows may warn before it runs: when SmartScreen
says "Windows protected your PC", choose *More info* → *Run anyway*. On a PC with Smart App
Control turned on, Windows may refuse unsigned apps altogether.

The browser extension cannot be installed by Setup, and it is in no browser store yet: the
finish page links to `EXTENSION.txt` in the installation folder, which shows how to add it by
hand. In Chrome, Edge, Brave and Chromium, load `extension\chrome-mv3` with
*Developer mode → Load unpacked* and keep *Developer mode* on. Firefox loads
`extension\firefox-mv3\manifest.json` as a temporary add-on in `about:debugging` until it
closes (see [Browser integration](#browser-integration)).

**Upgrading.** Run the new Setup. Before it replaces any file it closes the Downex window and
stops the daemon with `downex daemon stop` (running downloads are paused, their progress is
saved, and they continue when the new daemon starts), then ends whatever else still runs from
the installation folder (for example a relay a browser started). Only programs that run from
that folder are touched. A file that stays in use is retried for 10 seconds; then Setup asks to
retry or cancel. An installation found in another folder is removed with its own uninstaller
first (keeping your data), so there is one installation per user.

**Uninstalling** (*Settings → Apps → Downex*, or `uninstall.exe` in the installation folder)
stops the window and the daemon, removes the browser registration (restoring a registration
it had replaced), the sign-in entry `HKCU\Software\Microsoft\Windows\CurrentVersion\Run\Downex`
when it starts this installation, the shortcuts, the program files and the Apps entry. It then
asks whether to delete your data as well: the download list and settings in `%APPDATA%\Downex`
and the logs and other state in `%LOCALAPPDATA%\Downex`. Downloaded files are never deleted.
Remove the extension in the browser yourself.

**Silent use** (NSIS conventions; `/D` must come last and is not quoted, even with spaces):

```powershell
Downex-Setup-1.0.0.exe /S                                   # install or upgrade, default folder
Downex-Setup-1.0.0.exe /S /D=C:\Users\me\Apps\Downex        # install into a folder
& "$env:LOCALAPPDATA\Programs\Downex\uninstall.exe" /S       # uninstall, keeping your data
```

Silent Setup uses the default components (desktop shortcut on, sign-in start off) and exits
with 0 on success and 2 when it was cancelled, for example because a file stayed in use.

**Building Setup.** `scripts\build-windows.ps1 -Installer` builds everything and then compiles
`installer\downex.nsi` with NSIS 3 (`winget install --id NSIS.NSIS -e`; `makensis` is found on
`PATH` or in its default folder) into `bin\Downex-Setup-<version>.exe` (or `-Out`):

```powershell
powershell -ExecutionPolicy Bypass -File scripts\build-windows.ps1 -Version 1.0.0 -Installer
```

`-Installer` cannot be combined with `-SkipExtension` or `-SkipUI`. The version is passed to
NSIS as `/DVERSION` (and its leading numbers as the file's version resource). The licence page
shows `installer\LICENSE.txt`: a short preamble with the source code's location, then the GNU
General Public License, version 3 or later (GPL-3.0-or-later), Downex's licence.

## macOS

One universal app (Apple silicon and Intel) for **macOS 13 Ventura or newer**, specified in
[MACOS-SPEC.md](MACOS-SPEC.md). Chrome, Edge, Brave, Chromium and Firefox work through
the same extension and relay as on Windows; Safari is not supported yet. The window is native
(`ui/native` over `ui/mac`: AppKit, Core Graphics and Core Text through cgo). It has been built
and run on Intel Macs; Apple silicon is built into the universal app but not yet verified.
`go test ./packaging/macos` checks the bundle templates and the build script's functions (bundle
layout, template rendering, signing order) on any OS with a bash.
[MACOS-TESTING.md](MACOS-TESTING.md) is the checklist for a run on a Mac: tests, build,
install, menu bar, notifications, browsers, progress windows, media, launch at login and
uninstall.

### Building on a Mac

Needs, on the Mac that builds:

- the **Xcode Command Line Tools** (`xcode-select --install`; clang, lipo, codesign, notarytool;
  the full Xcode is not needed);
- **Go 1.27** or later;
- **Node.js 24** with **pnpm** (`corepack enable pnpm`), for the browser extension;
- only for `-w` (the earlier Wails/WebKit window instead of the native one): the **Wails CLI
  v2.17.0**, `go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.0`, with
  `$(go env GOPATH)/bin` on `PATH`.

```bash
bash scripts/build-macos.sh -v 0.4.0          # [-o dir] [-w]; output: dist/macos/Downex.app and dist/macos/Downex-0.4.0.dmg
bash scripts/build-macos.sh -h                # all options
```

The script checks the tools first, then:

1. builds `downexd`, `downex` and `downex-nmhost` (with the ID from `ext/EXTENSION_ID.txt`) for
   arm64 and amd64 with `CGO_ENABLED=0` and joins each pair with `lipo -create`;
2. builds `downexd-agent`, the daemon with cgo (menu-bar icon and notifications through AppKit and
   UserNotifications), the same way, for macOS 13 (`-mmacosx-version-min=13.0`);
3. builds the extension (`pnpm build`, `pnpm build:firefox`) and the native window
   (`go build ./ui/native` with cgo for arm64 and amd64, joined with `lipo -create`; with `-w`,
   `wails build -platform darwin/universal` instead);
4. makes `AppIcon.icns` from `ui/build/appicon.png` (`sips`, `iconutil`);
5. assembles the bundle from the templates in [packaging/macos](../packaging/macos/README.md), with
   the version filled in (`CFBundleShortVersionString` gets its leading numbers: `0.4.0-beta.1` →
   `0.4.0`, `dev` → `0.0.0`):

   ```
   Downex.app/Contents/
     Info.plist                          com.downex.app
     MacOS/Downex                        the window
     Helpers/downexd, downex, downex-nmhost
     Helpers/Downex Progress.app         the window binary again, LSUIElement (com.downex.progress)
     Library/LoginItems/Downex Agent.app Contents/MacOS/downexd-agent, LSUIElement (com.downex.agent)
     Resources/AppIcon.icns, EXTENSION.txt, extension/chrome-mv3, extension/firefox-mv3
   ```

6. signs it inside out (the helpers, then the two nested apps, then `Downex.app`) with
   `codesign --force --options runtime` (hardened runtime) and
   [packaging/macos/entitlements.plist](../packaging/macos/entitlements.plist) (no App Sandbox),
   and checks it with `codesign --verify --deep --strict`;
7. writes `Downex-<version>.dmg` (`hdiutil`, compressed): the app, a link to `/Applications`
   and `Read Me.txt` with the steps below.

**Signing.** Without `DEVELOPER_ID` the signature is ad-hoc (`codesign -s -`): fine on the Mac
that built it, blocked by Gatekeeper elsewhere until the user approves it (below). With an Apple
Developer account, sign with a *Developer ID Application* certificate from the login keychain
and have Apple notarize the result:

```bash
security find-identity -v -p codesigning                    # the identities codesign can use
xcrun notarytool store-credentials downex-notary --apple-id <apple-id> --team-id <TEAMID>   # once; asks for an app-specific password
DEVELOPER_ID="Developer ID Application: <Name> (<TEAMID>)" NOTARY_PROFILE=downex-notary bash scripts/build-macos.sh -v 0.4.0
```

`DEVELOPER_ID` alone signs with a secure timestamp (also the disk image). `NOTARY_PROFILE`
(needs `DEVELOPER_ID`) submits the zipped app with `xcrun notarytool submit --wait`, staples the
ticket to the app, then does the same for the disk image, and checks both with `spctl`; a
rejected submission stops the script and prints the notary log.

**From Windows or Linux** only the pure-Go parts can be checked:

```bash
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/... ./internal/...   # and GOARCH=amd64; go vet likewise (not the cgo window)
go test ./packaging/macos      # templates and script functions; uses Git for Windows' bash on Windows
```

### Installing on a Mac

1. Open `Downex-<version>.dmg` and drag **Downex** onto **Applications**.
2. Open Downex from Applications. A notarized build opens at once. An ad-hoc signed (or
   signed but not notarized) build that was downloaded is blocked the first time:
   - **macOS 13 and 14:** right-click (Control-click) Downex in Applications → **Open** → **Open**.
   - **macOS 15 and later:** open it once and close the message, then *System Settings → Privacy
     & Security* → **Open Anyway** next to the message about Downex.

   Browsers start the relay, and the login item the agent, outside the app; if they are blocked
   too, or macOS calls Downex "damaged", remove the quarantine flag the download set:
   `xattr -dr com.apple.quarantine /Applications/Downex.app`. A build made on the same Mac carries
   no quarantine flag and opens directly.
3. Register the relay with the browsers. The window does this each time it opens
   (`register --keep`, for the browsers this user has opened). To do it by hand (for example
   after opening a new browser), run the command below (it writes the manifests of [MACOS-SPEC.md §4](MACOS-SPEC.md#4-browser-registration-downex-nmhost-register-darwin)
   with the relay's path inside the app; see [Browser integration](#browser-integration) for the flags):

   ```bash
   /Applications/Downex.app/Contents/Helpers/downex-nmhost register
   ```

4. Add the browser extension by hand (it is in no browser store yet). In Chrome, Edge, Brave
   and Chromium, load it from inside the app with *Developer mode* → *Load unpacked* →
   `/Applications/Downex.app/Contents/Resources/extension/chrome-mv3`, and keep
   *Developer mode* on. Firefox loads `Contents/Resources/extension/firefox-mv3/manifest.json`
   as a temporary add-on in `about:debugging` until it quits. The file dialogs show
   `Downex.app` as one item: press **⌘⇧G** and paste the path.
   `Contents/Resources/EXTENSION.txt` has the details (`open -e` it).
5. Optionally put the CLI on `PATH`:
   `sudo ln -sf /Applications/Downex.app/Contents/Helpers/downex /usr/local/bin/downex`.

**Uninstalling.** `downex settings --launch-at-login off`, `downex daemon stop` and
`downex-nmhost unregister` (all in `Contents/Helpers`), remove the extension in the browsers, then
move Downex.app to the Trash. The download list and settings in
`~/Library/Application Support/Downex` and the logs in `~/Library/Logs/Downex` stay until you
delete them; downloaded files are never deleted.

## Usage

```bash
# Through downexd, the per-user background daemon that runs downloads
downex add <url> [-o dir] [-c connections] [--limit rate] [-H "Name: value"]... [--name file] [--queue q] [--paused] [--wait]
downex ls [--json]
downex watch [id...]
downex pause <id>
downex resume <id> [--restart] [-c connections] [--limit rate] [--wait]
downex rm <id> [--delete-file]
downex set <id> [--limit rate] [-c connections]
downex media <manifest-url> --list [--json] [-H "Name: value"]...
downex media <manifest-url> [--video ID] [--audio ID] [-o dir] [--name file] [-H "Name: value"]... [--queue q] [--paused] [--wait]
downex media --site <page-url> --list [--json] [-H "Cookie: ..."] [-H "Referer: ..."]
downex media --site <page-url> [--video ID] [--audio ID] [-o dir] [--name file] [-H "Cookie: ..."] [--queue q] [--paused] [--wait]
downex tools [status] [--json] | install | update
downex confirm <id> [-o dir] [--name file] [--video ID] [--audio ID] [--queue q] [--later] [--wait]
downex queue ls|create|update|rm|start|stop|move|schedule ...
downex cancel-post <queue>
downex proxy [show] | set system|none|manual|pac ...
downex login [ls] | add <url-pattern> --user name | rm <id>
downex update check|install [--json]
downex settings [--max-concurrent n] [--global-limit rate] [--dir path] [--connections n]
                [--categories-folders on|off] [--confirm-browser on|off] [--notify-complete on|off]
                [--notify-failure on|off] [--launch-at-login on|off] [--tray on|off]
                [--progress-window on|off] [--complete-dialog on|off] [--clipboard on|off]
                [--mark-files on|off] [--av-scan on|off] [--av-command path] [--av-args args]
                [--update-feed url] [--auto-update on|off] [--site-media on|off]
                [--tools-auto-update on|off] [--tools-channel stable|nightly] [--json]
downex daemon start|stop|status [--json]

# In this process, without the daemon (for scripting)
downex get <url> [-o dir] [-c connections] [--limit rate] [-H "Name: value"]... [--name file] [--restart]
downex resume <id> --local [--restart] [-c connections] [--limit rate]
downex rm <id> --local [--delete-file]
downex info <id> [--json] [--endpoint path]
downex help [command]
downex --version
```

| Command | What it does |
|---|---|
| `add <url>` | Hands the URL to `downexd` and prints the new download's id on stdout. The daemon puts it at the end of the main queue (or of `--queue`) and starts queued downloads in order while fewer than `max-concurrent` run. `--paused` adds it paused. `--wait` follows its progress (one progress line) until it completes, fails or is paused elsewhere. |
| `ls` | Lists downloads: id, state, progress, size, name. Shows the daemon's live list while `downexd` runs; otherwise reads the database directly. It never starts the daemon. |
| `watch [id...]` | Live table of all (or the given) downloads: id, state, progress, speed, connections, ETA, redrawn in place at most 4 times per second, fitted to the terminal. When the output is not a terminal, a line is printed whenever a row changes. |
| `pause <id>` | Pauses a queued or running download; its progress is saved. |
| `resume <id>` | Queues a paused, failed or interrupted download again; it continues from its saved progress. `--wait` follows it like `add --wait`. |
| `rm <id>` | Removes a download (pausing it first if it runs). Its file, or its partial `.dxpart` file, is kept unless `--delete-file` is given. Removing a download that awaits confirmation cancels it. |
| `set <id>` | Changes a download's speed limit (`--limit`, `0` = unlimited) or maximum connections (`-c` or `--connections`, 1–32, `0` = the daemon's default) in `downexd`. A running download uses a new speed limit at once and a new connection limit as soon as it can (file downloads at once, by stopping the highest connections where they are without losing data; streams on their next start); otherwise they apply when it starts next. The values are saved with the download. Completed downloads are refused. |
| `media <manifest-url>` | Downloads an HLS (`.m3u8`) or DASH (`.mpd`) stream through `downexd` and prints the new download's id: the best video and its best audio unless `--video`/`--audio` choose others, joined into one `.mp4` (MPEG-TS streams are converted). `--list` only shows the tracks that can be saved as MP4 (`*` marks the default pick; `--json` for scripts). See [Streaming media](#streaming-media-hls-and-dash). |
| `media --site <page-url>` | Downloads the video of a web page (YouTube and the other sites yt-dlp knows) as one `.mp4`: `--list` shows every MP4 quality the page offers, best first (ID, kind, quality, codecs, size with `~` for the site's estimates, file type, language; `*` = default), and `--video` picks any of them. `-H` takes only `Cookie` and `Referer`. While downexd sets up yt-dlp or reads the page it retries every 2 s for up to 5 minutes with one status line. See [Videos of web pages](#videos-of-web-pages-yt-dlp). |
| `tools [status\|install\|update]` | Shows yt-dlp and Deno (state, version, Downex's copy or the one on PATH, how it was verified, the last check and error; `--json`), or downloads them now / looks for newer releases, following the progress for up to 15 minutes. |
| `confirm <id>` | Confirms a browser hand-off that awaits confirmation (state `awaiting` in `ls`): queues it, or with `--later` keeps it paused. `-o` and `--name` replace the proposed folder and file name; a name that is taken gets a ` (n)` suffix. For a stream, `--video` and `--audio` choose other tracks. `--wait` follows it like `add --wait`. |
| `queue …` | Lists, creates, changes, deletes, starts and stops [download queues](#queues-and-the-scheduler), moves downloads between them and sets their schedules (`downex help queue`). `--queue q` of `add`, `media` and `confirm` takes a queue id or name. |
| `cancel-post <queue>` | Cancels the countdown of a queue's [post-queue action](#post-queue-actions) (sleep, hibernate, shut down, quit). |
| `proxy`, `login` | Show or change the [proxy](#network-proxies-logins-and-host-rules) and the site logins; passwords are asked for without echo and kept in the system's credential store. |
| `update check\|install` | Checks the signed [update feed](#updates), or downloads, verifies and installs the newer version. |
| `settings` | Shows the daemon's settings and the download categories with their folders, or changes those given (every other setting is kept): `--max-concurrent` (1–16, default 3), `--global-limit` (all downloads together; `0` = unlimited), `--dir` (default directory of `add` without `-o`; default: your Downloads folder), `--connections` (default per download, 1–32, default 8), and the on/off switches `--categories-folders`, `--confirm-browser`, `--notify-complete`, `--notify-failure`, `--launch-at-login`, `--tray`, `--progress-window` (open a download's progress window when it starts from the window or the browser) and `--complete-dialog` (the download complete dialog for downloads whose progress window was opened; off: the progress window just closes and nothing pops up) (also `true`/`false`, `yes`/`no`, `1`/`0`). [Desktop integration](#clipboard-file-marks-and-virus-scans): `--clipboard`, `--mark-files`, `--av-scan`, `--av-command`, `--av-args`; [updates](#updates): `--update-feed`, `--auto-update`; [videos of web pages](#videos-of-web-pages-yt-dlp): `--site-media`, `--tools-auto-update`, `--tools-channel stable\|nightly`. |
| `daemon start\|stop\|status` | Starts `downexd` unless it runs; stops it (running downloads are paused and their progress is saved); or shows its pid, version, endpoint, uptime and active/queued counts. `status` exits with 1 when the daemon is not running. |
| `get <url>` | Adds a download and runs it in this process until it completes. Shows one progress line (percent, size, speed, connections, ETA) refreshed at most 4 times per second. |
| `resume <id> --local` | Continues a download in this process, in the foreground, like `get`. |
| `rm <id> --local` | Removes a download by changing the database directly. |
| `info <id>` | Shows one download from the database: URL, file, state, mode, validators, limits and its segments, when it first started and the time it took (running time without pauses; "so far" until it completes; `--json`: `startedAt`, `activeMs`), and whether it gets the download complete dialog (`completionDialog`). While `downexd` runs the download, also its live speed and running time and a table of its connections (slot, state, range, bytes received, speed, and the fragment of a stream; `--json`: `connectionInfo`). It never starts the daemon. |
| `--version` | Prints `downex <version>` (also `-version` and `version`). |

**Starting the daemon.** `add`, `watch`, `pause`, `resume`, `rm`, `confirm`, `settings` and `daemon start`
start `downexd` when nothing listens on its endpoint: in the background, without a console
window on Windows, with the same `--db` and `--endpoint` flags, and wait up to 3 seconds for it
to answer. `downexd` is looked up next to the `downex` executable (after resolving symbolic
links), or at `$DOWNEX_DAEMON`. If it cannot be started, the command says why and where the
daemon's log is.

Flags:

| Flag | Commands | Meaning |
|---|---|---|
| `-o dir` | add, get, media, confirm | Directory to save into. `get`: default `.`, created if needed. `add` and `media`: default the folder of the download's category, else the daemon's default directory (see [Categories](#categories-and-folders)); a relative path is relative to the current directory. `confirm`: default the proposed folder. |
| `-c n` | add, get, resume, set | Maximum connections, 1–32 (default 8). Connections ramp up 1→2→4→8 as the server keeps up. On `resume` and `set` this changes the stored setting (`set` also accepts `--connections n`). |
| `--limit rate` | add, get, resume, set | Speed limit in bytes per second with decimal suffixes `K`/`KB`, `M`/`MB`, `G`/`GB` (`500K` = 500,000 B/s, `1.5M` = 1,500,000 B/s); `KiB`, `MiB`, `GiB` are binary (`512KiB` = 524,288 B/s); `0` = unlimited. On `resume` and `set` this changes the stored setting. |
| `-H "Name: value"` | add, get, media | Extra request header, repeatable, for example `Cookie` or `Referer`. |
| `--name file` | add, get, media, confirm | Save as `file` instead of the name from `Content-Disposition` or the URL (`media`: the manifest; `.mp4` is added; `confirm`: instead of the proposed name). |
| `--paused` | add, media | Add the download paused instead of queueing it. |
| `--wait` | add, media, resume, confirm | Follow the download until it completes. Ctrl+C stops following; the download continues. |
| `--video ID`, `--audio ID` | media, confirm | Stream tracks to download, as `media --list` (or `info <id>`) shows them. |
| `--list` | media | Show the stream's tracks instead of downloading it. |
| `--restart` | get, resume | If the file on the server changed, discard the saved data and download it again from the beginning, instead of failing. |
| `--local` | resume, rm | Work in this process on the database, without `downexd` (refused while the daemon runs). |
| `--proxy url` | get, resume --local | `direct`, `http://[user:pass@]host:port` or `socks5://[user:pass@]host:port`. The default is the `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY` environment. |
| `-v` | get, resume --local | Print debug messages (ramp-up, splits, retries). |
| `--json` | ls, info, settings, daemon status | Machine-readable output. `Cookie`, `Authorization` and `Proxy-Authorization` header values are redacted. |
| `--db path` | all | Download database to use, also for an autostarted daemon. The default is `<user config dir>/Downex/downex.db`, for example `%AppData%\Downex\downex.db` or `~/.config/Downex/downex.db`. |
| `--endpoint path` | daemon commands | The daemon's endpoint: a named pipe on Windows (`\\.\pipe\…`), a unix socket on Linux. The default is per user, or `$DOWNEX_ENDPOINT`. |

`--db` and `--endpoint` may also be given before the command, as global flags
(`downex --endpoint \\.\pipe\downex-test ls`). Flags may come before or after the positional
arguments, and `-flag` and `--flag` are equivalent.

### Examples

```bash
id=$(downex add https://example.com/file.zip -o ./downloads -c 8)
downex add https://example.com/video.mp4 --limit 2M -H "Referer: https://example.com/" --wait
downex watch
downex pause "$id"
downex resume "$id" --limit 0
downex settings --max-concurrent 4 --global-limit 10M
downex rm "$id" --delete-file
downex daemon stop

downex media https://example.com/show/master.m3u8 --list
downex media https://example.com/show/master.m3u8 --video v1 -o ./videos --wait

downex get https://example.com/file.zip -o ./downloads   # no daemon involved
downex info 3 --json
```

### Pausing, resuming and changed files

- **Ctrl+C** pauses a `get` (or `resume --local`) download. Everything written so far is flushed
  to disk and committed before `downex` exits, and it prints the command to continue, such as
  `downex resume 3 --local`. Downloads run by `downexd` are paused with `downex pause <id>`;
  Ctrl+C in `watch`, `add --wait` or `resume --wait` only stops watching.
  A second Ctrl+C exits at once. Progress is still safe then, because saved progress never
  includes bytes that were not yet on disk.
- Files are written to `<name>.dxpart` and renamed to `<name>` when complete. Existing files are
  never overwritten: `name (1).ext` is used instead.
- A resumed download asks the server for only the missing ranges. It sends `If-Range` with the
  strong ETag, or else `Last-Modified`. If the file on the server has changed, the download fails
  with an explanation (`error_code` `validator_changed` from `downexd`) and keeps the saved data.
  Run `downex resume <id> --restart` to discard it and download the new version.
- Servers without byte-range support are downloaded over one connection. Such downloads cannot
  resume, so `resume` starts them over.
- Two `downex` processes never run the same download at the same time. The second one refuses
  with "download is in use by another downex process". `resume --local` and `rm --local` refuse
  to run while `downexd` is running, because the daemon manages the downloads then.

### Exit status

| Code | Meaning |
|---|---|
| 0 | Success (including `help`, and `resume` of an already completed download) |
| 1 | Error: bad arguments, network/HTTP failure, changed remote file, disk full, `downexd` cannot be started or reached, `daemon status` when it is not running, … |
| 130 | Interrupted by Ctrl+C or SIGTERM: a `get` download is paused and can be resumed; `watch` and `--wait` stop watching while `downexd` keeps downloading |

## The desktop window

The window is native: `ui/native`, drawn with Direct2D and DirectWrite in Win32 windows from
pure Go on Windows, and with AppKit, Core Graphics and Core Text (`ui/mac`, cgo) on macOS. It
talks to `downexd` like the CLI does: it lists the downloads live, adds, pauses, resumes and
removes them, edits the daemon's settings, and opens finished files.

**Build.** `scripts\build-windows.ps1` builds it (through `ui\native\build.ps1 -Product`) as
`bin\downex-ui.exe` on x64, and `scripts/build-macos.sh` as `Downex.app/Contents/MacOS/Downex`.
On Windows it is installed next to `downexd.exe` under another name than `Downex.exe`: Windows
file names ignore case, so `Downex.exe` would replace the CLI `downex.exe` in the same folder.
The native window runs on x64 only; with `-WebViewUI` (and for an ARM64 build, which is not
part of the release) the build script builds the earlier Wails/WebView2 window (`ui/`, with the
Svelte frontend in `ui/frontend`) instead, which needs the Wails CLI v2.17.0 and Node.js 24 with
pnpm. There is no Linux window yet.

**What it does.**

- **One process.** Starting Downex again brings the open window to the front: one process
  hosts the main window and every progress window (on macOS, *Downex Progress* hosts the
  progress windows), and a later start hands its request to it and exits.
- **The daemon.** The window starts `downexd` (next to its executable, or `$DOWNEX_DAEMON`) when it
  is not running, and again if it stops while the window is open; to stop the daemon for good,
  close the window first. It never starts the daemon while it runs as administrator: it then
  says so and asks to start Downex normally (or to run `downex daemon start` from a normal
  prompt). It uses the CLI's endpoint (`$DOWNEX_ENDPOINT`, else the per-user default) and
  `downexd`'s default database.
- **Live list.** It follows `DownloadService.Watch`. When the daemon stops or restarts, the window
  shows the last known list with a banner and subscribes again after 1 s, then 2, 4, 8 and every
  10 s; the full list it then receives also drops downloads that were removed meanwhile.
- **Opening files.** *Open* works only for completed downloads whose file is still where the daemon
  put it, and opens it with its default app. Programs, installers, scripts and shortcuts
  (`.exe`, `.msi`, `.bat`, `.cmd`, `.ps1`, `.vbs`, `.js`, `.lnk`, `.scr`, `.jar`, ..., and
  everything in `PATHEXT`) are opened only after a Yes in a native confirmation that shows the real
  file name; the name of a link and of its target both count. *Show in folder* selects the file
  (or the unfinished `.dxpart`) in Explorer, or opens the folder.
- **Confirming browser downloads.** Every download that awaits confirmation gets a dialog, one at
  a time, oldest first: URL, editable file name, size (or "unknown"), category (which changes the
  proposed folder), folder with *Browse*, and **Start download**, **Download later** (paused) or
  **Cancel download** (removed). Closing the dialog leaves the download waiting; the row's
  *Confirm download…* (context menu, Enter, double-click, or the details panel) brings it back,
  and the sidebar's *To confirm* filter lists them. `downexd` starts the window as
  `downex-ui.exe --confirm <id>`; an open window receives that request and comes to the front.
- **Categories.** The sidebar groups downloads by the categories of the daemon's settings, plus
  *Other*. The Settings dialog edits them (name, extensions, folder) and the desktop switches
  (category folders, confirmation, notifications, launch at login, tray icon).
- **Streams.** The Add dialog has a *Stream (HLS/DASH)* mode: *Find qualities* lists the
  stream's tracks (video by resolution, audio by language), *Add stream* adds it; a pasted or
  dropped `.m3u8`/`.mpd` address opens it in that mode. A stream handed off by the browser gets
  the same quality picker in its confirmation dialog. Stream rows show an HLS/DASH badge and count
  their progress in fragments; the details panel shows the tracks, the format and length.
- **Pause all / Resume all** in the toolbar and context menu; a message says how many changed.
  Resume all queues paused downloads only.
- **Queues** (0.4.0): the sidebar lists the [queues](#queues-and-the-scheduler) with a dot while they
  run and the number of unfinished downloads; clicking one filters the list, the gear opens the queue
  manager (create, rename, delete, simultaneous downloads, speed limit, *when done* action,
  schedule, Start/Stop, order by drag and drop or the arrow buttons). *Move to queue…* is in the
  context menu; the Add and confirmation dialogs choose the queue. A post-queue countdown shows a
  banner with **Cancel**.
- **Settings pages** *General* (notifications, progress windows, launch at login), *Downloads*,
  *Network* (proxy, host rules, site logins; passwords are stored in the system's credential
  store and never shown again), *Integration* (clipboard, file marks, virus scanner with
  *Browse…*), *Video sites*, *Appearance*, *Updates* (feed, automatic checks, *Check now*,
  *Install*) and *About*. A download that failed with `auth_required` offers **Add site login…**.
- **Progress windows**, see below.
- **Logs** go to `ui.log` next to `downexd.log` (rotated at 1 MiB at start-up), without URLs.

### Progress windows

Every download can have a small window of its own that follows it live
([DESKTOP-SPEC.md](DESKTOP-SPEC.md) §7). `downex-ui.exe --progress <id>` opens it in the
window process (starting that process when none runs); there is one per download, and opening it
again brings the open one to the front.

- **When it opens.** With *Show a progress window when I start a download* on (Settings →
  *General* → *Progress windows*; the default; `downex settings --progress-window off` turns it off): after *Start download* in the confirmation dialog, after
  adding one download in the main window that is not paused, and when a browser hand-off starts
  at once (confirmation off; `downexd` starts the window then, if `downex-ui.exe` is next to it).
  Double-click, Enter or *Show progress* on an unfinished download in the main list opens it
  whatever the setting. Queued downloads that start later, CLI downloads and Pause all / Resume all
  never open one.
- **Status tab.** The title is `<percent>% <file name>`. Address (with a copy button), status
  (*Connecting…*, *Receiving data…*, *Waiting in queue*, *Paused*, *Assembling…*, *Completed*,
  *Failed: …*), file size, downloaded bytes and percent, transfer rate, time left, whether it can
  resume, and for streams the fragments and the chosen quality; the download's own speed limit
  when one is set. **Pause / Resume**, and **Cancel**, which pauses the download and closes the
  window (it stays in the list).
- **Details** (*Show details*): a bar of the whole file with each segment's start marked and its
  received part filled, and one row per connection: number, bytes of its current range, and what
  it does (*Connecting…*, *Sending request…*, *Receiving data…*, *Retrying in N s (reason)*,
  *Idle*, *Finished*, *Error: …*; streams also show the fragment).
- **Speed limit tab.** *Limit this download's speed* with a value in KiB/s or MiB/s, and the
  maximum connections (1–32): applied at once to the running download, like `downex set`.
- **When it ends.** The [download complete dialog](#download-complete-dialog) (with
  `--complete-dialog` off the window just closes), or on failure the error with **Retry** (or
  *Start over* when the file on the server changed) and **Close**. The window closes when its
  download is removed. Closing the window (X) only closes it: the download goes on in `downexd`.

### Download complete dialog

A download whose progress window was opened at least once (the window tells `downexd` so; the
mark is saved with the download) gets a dialog when it ends, also when its window was closed
meanwhile ([DESKTOP-SPEC.md](DESKTOP-SPEC.md) §8): an open progress window turns into
it, otherwise `downexd` starts `downex-ui.exe --progress <id>` once. Queued, scheduled and CLI
downloads only get the tray notification, and nothing pops up while *Show the download complete
dialog for downloads I started* (Settings → *General* → *Progress windows*; `downex settings
--complete-dialog off`) is off.

- **Contents.** *Download complete* with the file name; the address (stream: the manifest) and
  the saved path, each with a copy button; the size; the time taken, from the first start to the
  completion without the pauses (or *Unknown* for downloads from before 0.3.2); for streams the
  format and quality.
- **Buttons.** **Open** (Enter), **Open with…** (the Windows *Open with* dialog: pick any app;
  the association changes only if you tick *Always use this app*), **Open folder** and
  **Close** (Esc); programs and other active content still ask first. *Don't show this dialog
  again* turns the setting off.
- **On failure** the same window shows the error with **Retry** and **Close**.

## The daemon: downexd

`downexd` is the per-user background process that owns and runs the downloads, with one shared
engine so that per-host connection limits apply across downloads. The daemon commands of
`downex` start it when needed (see above); it can also be started directly, for example from a
login script:

```bash
downexd [--db path] [--endpoint path] [--log-level debug|info|warn|error] [--log-dir dir] [--foreground]
        [--background] [--no-tray] [--allow-elevated]
downexd --version
```

| Flag | Meaning |
|---|---|
| `--db path` | Download database, shared with the CLI. Default: the CLI's default (`<user config dir>/Downex/downex.db`). |
| `--endpoint path` | Local endpoint to serve. Default: per user, or `$DOWNEX_ENDPOINT`. |
| `--log-level level` | `debug`, `info` (default), `warn` or `error`. |
| `--log-dir dir` | Directory of `downexd.log`. Default: `%LOCALAPPDATA%\Downex\logs` on Windows, `$XDG_STATE_HOME/downex` (`~/.local/state/downex`) on Linux. |
| `--foreground` | Also write the log to stderr, for running it in a terminal. |
| `--background` | Accepted for the sign-in entry and Setup; running in the background is the default anyway. |
| `--no-tray` | Never show the notification-area icon or its notifications. |
| `--allow-elevated` | Run even with administrator (elevated) rights. By default `downexd` refuses, because every program of the user could then use those rights through it. |
| `--version` | Print the version and exit. |

- **One daemon per user.** A second `downexd` for the same endpoint prints
  `downexd is already running (pid …)` and exits with 0.
  One database is served by one daemon only, whatever endpoint it uses.
- **Endpoint.** Windows: the named pipe `\\.\pipe\downex-<key>`, where the random key is
  created once in `%LOCALAPPDATA%\Downex\endpoint.key` (readable only by you, and not by sandboxed
  processes); accessible only to your account, remote clients rejected, and clients refuse a pipe
  that was not created by a normal (not sandboxed) process of yours. Linux:
  `$XDG_RUNTIME_DIR/downex/rpc.sock` (else `~/.local/state/downex/run/rpc.sock`) in a directory of
  mode 0700, socket mode 0600; connections from other users are refused. Clients speak Connect
  over HTTP/2 cleartext (`proto/downex/v1/downex.proto`).
- **Settings** (`downex settings`) are stored in `settings.json` next to the database.
- **Logs** are JSON lines in `downexd.log`, rotated at 5 MiB with four older files kept.
  Cookie, Authorization and Proxy-Authorization values, request bodies, and the user information
  and query of URLs are never logged.
- **Stopping.** `downex daemon stop`, Ctrl+C, SIGTERM, SIGHUP, closing its console window or (on
  Windows) signing out or shutting down pause the
  running downloads (their progress is committed) and store them as queued; the next start
  resumes them. After a crash or kill, downloads left running are queued again at the next start
  and continue from their last committed progress.
- **No two writers.** `downexd` and `downex get` / `--local` lock each download they work on
  (files in `<database>.locks/`), so a download is never run or removed by two processes at once.
- **Exit status:** 0 when stopped or already running, 1 on errors, 2 on bad arguments.

The desktop integration ([DESKTOP-SPEC.md](DESKTOP-SPEC.md)) belongs to the user's own
daemon: the one on the default database and endpoint. A `downexd` given another `--db`,
`--endpoint` or `$DOWNEX_ENDPOINT` (tests, scripts, other profiles) shows no icon and never
writes the sign-in entry.

### Browser downloads wait for confirmation

With `confirm_browser_downloads` on (the default; `downex settings --confirm-browser off` turns it
off), a download handed off by the browser extension is stored as *awaiting confirmation*: nothing
is fetched or created on disk, it survives restarts, and Pause/Resume do not apply to it. The daemon
proposes a category, folder and file name and starts `downex-ui.exe --confirm <id>` when the window
is installed next to `downexd` (at most once per 2 seconds; the window shows every waiting
download in turn); otherwise the notification *Confirm download: <name>* appears. Confirm it in
the window or with `downex confirm <id>`, cancel it with `downex rm <id>`. Downloads added with
the CLI or the window are never held. With confirmation off, a hand-off that starts at once opens
its [progress window](#progress-windows) (`downex-ui.exe --progress <id>`) while
`show_progress_window` is on.

### Categories and folders

Every download gets a category from its file name, else the URL's last path segment, else the
MIME type the browser reported. The defaults are Video, Audio, Archives, Documents and Programs
(see `downex settings`); the window edits them (at most 32, names unique and up to 64 characters,
extensions `[a-z0-9]{1,16}`, at most 200 each). With `use_category_folders` on (the default), a
download without a chosen folder goes to its category's folder, or `<default dir>\<category>`,
created when the download starts.

### Tray icon and notifications

The notification-area icon shows "Downex — N active, X/s" (updated at most once per second, only
when it changes). Double-click opens the window; the menu has **Open Downex**, **Pause all**,
**Resume all**, **Settings…** and **Quit Downex** (stops the daemon gracefully, like
`downex daemon stop`). Completed and failed downloads produce a notification (switchable with
`--notify-complete` / `--notify-failure`; more than 3 within 5 seconds become one summary);
clicking it opens the window. `downex settings --tray off` hides the icon, and its
notifications, at once. The icon comes back when Explorer restarts.

**macOS:** the icon is in the menu bar. It belongs to *Downex Agent*
(`Contents/Library/LoginItems/Downex Agent.app`, `downexd` built with AppKit), which is what the
window, the CLI and the relay start when no daemon runs, and what launch at login starts; the plain
`Contents/Helpers/downexd` (no icon) is used only when the agent is missing. The menu's first line
shows "N active · X/s", followed by the same items; there is no double-click. Notifications are
Downex Agent's macOS notifications (macOS asks to allow them on the first one; they are silent),
and clicking one opens the window. Logging out, or *Quit* in Activity Monitor, stops the daemon
gracefully like *Quit Downex*. Started without a GUI session (over ssh) the agent runs without an icon.

### Launch at login

`downex settings --launch-at-login on` makes the user's own daemon write
`HKCU\Software\Microsoft\Windows\CurrentVersion\Run\Downex` = `"<dir>\downexd.exe" --background`
(Linux: `~/.config/autostart/downex.desktop`; macOS: the LaunchAgent
`~/Library/LaunchAgents/com.downex.agent.plist`, which starts
`Downex Agent.app/Contents/MacOS/downexd-agent --background` from the next login on and is never
pointed at a temporary App Translocation copy of the app); `off` removes it. The daemon checks the entry at
every start. If it cannot be written, the setting is not changed and the command reports why.

## Queues and the scheduler

Every download belongs to exactly one queue ([M6-SPEC.md](M6-SPEC.md) §1). The **main
queue** (id 1) always exists, always runs and cannot be deleted; downloads from before 0.4.0 are in it
in their old order. Other queues (at most 64, names up to 64 characters) start their downloads only
while they run: `downex queue start` / `stop`, the window's queue manager, or their schedule. A new
queue is stopped.

```bash
downex queue create Night --max 2 --limit 5M --post sleep   # prints the queue's id
downex add https://example.com/big.iso --queue Night
downex queue schedule Night --start 01:00 --stop 07:00 --days mon-fri --wake
downex queue move 12 13 --to Night --pos 1                    # 1-based position, default: the end
downex queue ls
downex queue start Night; downex queue stop Night
downex queue rm Night                                         # its downloads move to the main queue
```

- **Admission.** A queued download starts when its queue runs, the queue runs fewer than its
  *simultaneous downloads* (`--max`, 1–16; 0 = the global `max-concurrent`) and fewer than
  `max-concurrent` downloads run in all. Each queue starts its downloads in its own order; running
  queues take turns.
- **Speed limits** nest: global → queue (`--limit`) → download; the tightest one applies. A queue's
  limit changes at once for its running downloads, and a running download moved to another queue
  (also when its queue is deleted) follows the new queue's limit at once.
- **Stopping** a queue (by hand or by its schedule) puts its running downloads back into the queue as
  *queued*, with their progress, so that the next start continues them; downloads you paused stay
  paused. A queue other than the main one stops by itself when it has finished its work.
- **Schedules** use local time and stay right across daylight-saving changes: start at `--start` on
  the `--days` given (default: daily) or once on `--once YYYY-MM-DD`, stop at `--stop` (the window may
  cross midnight). `--repeat n` queues the queue's failed and unfinished downloads again every n
  minutes inside the window. A start missed while the computer was off or asleep happens when it
  comes back, if the window is still open. `--wake` (Windows) wakes the computer from sleep for the
  start while `downexd` runs (a waitable timer armed a minute before; the power options must allow
  wake timers). `downexd` notices resume from sleep and clock or time-zone changes and looks again;
  a `--once` date weeks ahead is kept, and after the clock is set back the schedule is applied again
  from the corrected time.
- **"Download later"** in the confirmation dialog (or `downex confirm <id> --later --queue q`) puts a
  browser download into the chosen queue; it waits there until that queue runs.

### Post-queue actions

A queue that was running and has nothing left to do, after at least one of its downloads completed,
performs its *when done* action (`--post`): `quit` (stop `downexd`), `sleep`, `hibernate` (Windows
only), `shutdown`, or `none` (the default). Only a download that completes or fails can finish a
queue: pausing, removing or moving downloads never starts the action, and a download waiting for
your confirmation still counts as work. A 60-second countdown comes first: the tray (macOS: the
menu bar) shows "Shutting down in 60 s", the window a banner with **Cancel**, and
`downex cancel-post <queue>` cancels it. New work for the queue also cancels it. A countdown that
the computer slept through is cancelled rather than performed when it wakes up, and one wake-up
performs at most one power action. While such a queue (one with a *when done* action or a
schedule) has downloads to do, or a countdown runs, `downexd` keeps the computer from going to
sleep on its own (Windows power request, macOS `caffeinate`); other downloads do not. Windows sleeps or
hibernates with `SetSuspendState` and shuts down with `InitiateShutdownW` without forcing other
programs to close; macOS uses `pmset sleepnow` and asks System Events to shut down (macOS asks once
for the Automation permission). Only the user's own daemon performs power actions.

## Network: proxies, logins and host rules

[M6-SPEC.md](M6-SPEC.md) §2. The window's Settings → *Network* tab has everything below.

```bash
downex proxy                                          # show the mode
downex proxy set system                               # the default
downex proxy set manual --https proxy.lan:3128 --http proxy.lan:3128 --bypass "<local>;*.corp.example"
downex proxy set manual --socks socks5://127.0.0.1:1080 --user me    # asks for the password
downex proxy set pac https://wpad.example/proxy.pac
downex proxy set none
downex login add 'https://files.example.com/*' --user alice           # asks for the password
downex login ls; downex login rm <id>
```

- **Proxy modes.** *System* (default): Windows' proxy settings (WinHTTP, including *Automatically
  detect settings* and setup scripts; when no proxy is found, downloads connect directly), macOS
  `scutil --proxy` (HTTP, HTTPS, SOCKS, configuration URL, exceptions), Linux the `HTTP_PROXY` /
  `HTTPS_PROXY` / `ALL_PROXY` / `NO_PROXY` environment. *None*. *Manual*: an HTTP proxy for http://,
  one for https:// (tunnelled with CONNECT), or a SOCKS proxy (`socks5://`, `socks4://`,
  `socks4a://`) for both, with a bypass list (`;` or `,` separated: host names, `*.example.com`,
  `<local>` for names without a dot, CIDR ranges such as `10.0.0.0/8`, `host:port`;
  `<-loopback>` proxies even `localhost` and 127.0.0.1, which are otherwise always direct). *PAC*: a
  proxy auto-configuration script at an http, https or file URL (at most 1 MiB), run in an isolated
  JavaScript engine with the standard helper functions, at most 1 s per call, its answers kept per
  host for 5 minutes; `PROXY`, `HTTPS`, `SOCKS`/`SOCKS4`/`SOCKS5` and `DIRECT` entries are tried in
  order, and one that cannot be reached is tried last for 5 minutes. The script runs in a separate
  helper process (`downexd` started a second time, with a memory limit), so a script that loops or
  eats memory cannot take the daemon down; for `https://` downloads it sees only the scheme, host and
  port, never the path or query. A
  script that cannot be fetched or run means direct connections (the log says why).
- Proxy addresses and the PAC address never contain a user name or password (`downex proxy set`
  refuses them; use `--user`, which asks for the password and stores it like other passwords).
- **Proxy authentication:** Basic with the proxy's user name and stored password; on Windows also
  NTLM/Negotiate as the signed-in user, without a password. Proxy credentials go only to the proxy
  that asks for them and that the current proxy mode chose, never to a web server: the stored
  password only to the manual proxy; system and PAC proxies get only the Windows sign-in.
- **Site logins.** When a server answers 401, Downex answers Basic, Digest (MD5, SHA-256,
  SHA-512-256, qop `auth`/`auth-int`) or NTLM/Negotiate (Windows: as the signed-in user when the
  login has no password). The credentials come from the URL (`https://user:pass@host/…`), else the
  first site login whose pattern matches: a URL pattern such as `https://files.example.com/*`
  (scheme, host, port and path) or a host pattern such as `*.example.com`. They are sent only to the
  server that asked (after a redirect: straight to the address that asked, without the original
  site's cookies), and never over plain HTTP to another origin after a redirect. Without a match
  the download fails with `auth_required`, and the window offers **Add site login…**.
- **Passwords** are never in the settings, the log, Watch, error texts or exports: Windows keeps them
  DPAPI-encrypted (your account only) in the database, macOS in the login keychain (service
  "Downex"; handed to `security` on its input, never on a command line, which limits a password to
  about 1.9 KB), Linux in a file readable only by you next to the database (not encrypted; the log
  warns).
- **Host rules** (window: Settings → *Network*): for a host pattern (`files.example.com`,
  `*.example.com`; the most specific one wins), *max connections* per download to that host and a
  *speed limit* shared by all downloads to it.

## Clipboard, file marks and virus scans

[M6-SPEC.md](M6-SPEC.md) §3; window: Settings → *Integration*.

- **Clipboard monitor** (`--clipboard on`, off by default): a copied text that is one http(s) address,
  or up to 10 lines of them, whose file name ends in an extension of a category (`.zip`, `.mp4`, …) is
  offered like a browser download (it waits for confirmation when that is on). The same address is
  offered at most once in 10 minutes. A copied text with an address that holds a user name or
  password (`https://user:pass@host/…`) offers nothing. Text that password managers mark as private
  (Windows clipboard formats excluded from monitoring or history; macOS concealed and transient
  types) is never read.
  Windows: `downexd` listens for clipboard changes; macOS: Downex Agent checks every second; Linux:
  not yet.
- **Marking downloaded files** (`--mark-files`, on by default), before the download is announced
  complete, the way browsers do: Windows' Mark of the Web (`Zone.Identifier` stream, Internet zone,
  with the address and referrer without user name and password), macOS `com.apple.quarantine` and
  *Where from* (`kMDItemWhereFroms`), Linux `user.xdg.origin.url` / `user.xdg.referrer.url`. Where the
  file system cannot keep them (FAT32, network shares) the download still completes.
- **Virus scan** (`--av-scan on --av-command <absolute path> --av-args "[File]"`): after marking, the
  scanner runs on the file without a shell (`[File]` becomes the quoted path; Windows accepts only
  `.exe`/`.com` scanners), for at most 10 minutes. The download stays completed; when the scanner
  exits with a non-zero code, or cannot run, the download shows a note and a notification says
  "Scanner reported a problem with <name>". Example (Windows Defender):
  `--av-command "C:\Program Files\Windows Defender\MpCmdRun.exe" --av-args "-Scan -ScanType 3 -File [File]"`.

## Updates

[M6-SPEC.md](M6-SPEC.md) §4. Release builds have Downex's update feed built in
(`https://downex.app/update.json`); another one can be set with `downex settings --update-feed
https://…/downex.json` or in Settings → *Updates*. `downexd` checks the feed a minute after it
starts and then daily (`--auto-update off` stops that). When a check finds a new version, the
window shows a pop-up with *Update now* and *Later*, on Windows and macOS (the tray or menu bar
notification opens the window); *Update now* installs it inside the app, with no browser
download. `downex update check` checks now, `downex update install` installs.

- The feed is a JSON manifest with the version, notes and one package per system, signed with an
  ed25519 key (`<feed>.sig`). The public key is built into `downexd`
  (`$env:DOWNEX_UPDATE_PUBLIC_KEY` / `$DOWNEX_UPDATE_PUBLIC_KEY` at build time); a build without one,
  or a `dev` build, cannot check for updates. Nothing is trusted before the signature is checked, and
  the package must have the published size and SHA-256 (and on Windows a valid Authenticode signature
  when the running build has one).
- Windows: only a copy installed by Setup updates itself (Setup runs silently, `downexd` stops for it
  and Setup starts it again). macOS: the new app's code signature must be valid and have the running
  app's identifier; a build signed with a Developer ID also needs the same team, and an ad-hoc signed
  build (the releases) needs the new app's version to equal the one in the signed manifest. The new
  app is copied without the quarantine attribute, the bundle is replaced and Downex starts again.
  Linux: use the package manager.
- Until the feed is published, a check finds no update information: the automatic checks stay
  silent, and Settings → *Updates* says "No update information is published yet". *Later* puts off
  only that check's offer; the window offers the update again after the next automatic check.
- Releases: `go run ./scripts/release-sign keygen -out <file outside the repository>` once (it
  prints the public key); per release `asset -os windows -arch amd64 -url <https URL> <package>`
  prints the manifest entry of a package, `sign -key <file> <manifest.json>` (or
  `$DOWNEX_UPDATE_PRIVATE_KEY`) writes `<manifest.json>.sig`, `verify -pub <public key>
  <manifest.json>` checks it.

## Streaming media (HLS and DASH)

Downex downloads **unencrypted** HTTP streams: HLS playlists (`.m3u8`, RFC 8216) and static
MPEG-DASH manifests (`.mpd`). It never decrypts anything, fetches no keys and has no site-specific
extractors: encrypted or DRM-protected streams (`EXT-X-KEY`, DASH `ContentProtection`, or a
`pssh`/`tenc`/`encv`/`enca`/`sinf` box in an initialization segment) and live streams are refused
with a clear message. See [MEDIA-SPEC.md](MEDIA-SPEC.md).

```bash
downex media https://example.com/show/master.m3u8 --list     # the tracks; * = default pick
downex media https://example.com/show/master.m3u8 --wait     # best video + its best audio
downex media https://example.com/clip.mpd --video v360 --audio a128 -o ./videos --name clip
```

- **Adding.** `downexd` fetches and parses the manifest first (for an HLS master playlist also the
  chosen variant's and audio rendition's playlists). A refused stream adds nothing; its error code
  is `encrypted`, `live`, `media_unsupported` or `media_invalid` (also the `Downex-Error-Code` of the
  API error), or `network`/`http_status`/`tls` when the manifest could not be fetched. Only tracks that
  can be saved as MP4 are listed: a stream without one (audio only, WebM only, or MPEG-TS whose
  `CODECS` name AC-3, E-AC-3 or MP3) is `media_unsupported`. The download goes to the *Video* category
  and is named after `--name`, else the page title (browser hand-offs), else the manifest's path, with
  `.mp4`.
- **Downloading.** The fragments of the chosen tracks are fetched in parallel, up to the download's
  connection limit, with the shared speed limits and retries, into `<name>.dxparts` next to the
  file. Every initialization segment is checked for protection before any media fragment is
  fetched. Progress is counted in fragments (`ls`, `watch`, the window); the size is known once the
  file is assembled.
- **Pause and resume.** Fragments are recorded in a per-track bitmap only after they are on disk,
  so `downex pause`, a daemon restart or a crash loses at most the fragments in flight; `resume`
  fetches only the rest (`--restart` discards them).
- **Joining.** Everything becomes one progressive `.mp4`, in pure Go, with no external program and
  no re-encoding. Fragmented MP4 (DASH, fMP4 HLS) video and audio are joined with both tracks.
  MPEG-TS variants (H.264 or HEVC video with AAC audio, muxed or with a separate TS audio rendition,
  discontinuities such as spliced-in ads included) are converted by Downex's own TS→MP4 remuxer. A
  TS track's first segment is checked before the rest is fetched: scrambled TS fails as `encrypted`,
  codecs that cannot be converted as `media_unsupported`, data that is not TS as `media_invalid`. A
  download stored as `.ts` by an earlier Downex keeps its `.ts` join when it has progress; one that
  never started becomes an `.mp4` download. Not supported (`media_unsupported`): AC-3, E-AC-3 or MP3
  audio and MPEG-2 video inside TS, packed-audio renditions, single-file DASH (`SegmentBase`), WebM,
  and fMP4 streams whose initialization segment changes midway.
- **From the browser.** The extension lists the streams a tab plays (toolbar badge count, popup card
  *Videos on this page*, and the [Download button](#videos-on-every-website) over the player) with
  their MP4 qualities, and hands one to Downex with the page's cookies and Referer; with
  confirmation on, it waits in the window's dialog with the quality picker (or
  `downex confirm <id> --video ID`).

## Videos of web pages (yt-dlp)

Downex saves the video of a web page that [yt-dlp](https://github.com/yt-dlp/yt-dlp) understands,
YouTube first, as **one `.mp4` file**. yt-dlp only turns the page into a list of formats; Downex's
own engine downloads the chosen formats (several connections, resumable, shown in the progress
window) and joins them in pure Go. Every distinct MP4 quality the page offers (each resolution,
frame rate, HDR and codec: H.264, AV1, HEVC) is listed best first and **you choose**; the default is
the highest resolution, H.264 preferred at equal height. WebM (VP9/Opus) formats are never offered,
and there is no audio-only download. DRM-protected and live videos, playlists and channels are
refused. See [SITE-MEDIA-SPEC.md](SITE-MEDIA-SPEC.md).

```bash
downex media --site "https://www.youtube.com/watch?v=..." --list    # every MP4 quality; * = default
downex media --site "https://www.youtube.com/watch?v=..." --wait    # the default pick
downex media --site "https://www.youtube.com/watch?v=..." --video 136 -o ./videos
downex tools                                                        # yt-dlp and Deno
```

- **yt-dlp and Deno.** Copies on `PATH` are used first (yt-dlp at most 90 days old, Deno 2.3.0 or
  newer). Otherwise the user's own `downexd` downloads them on first use from the publishers'
  GitHub releases into `%LOCALAPPDATA%\Downex\tools` (macOS: `~/Library/Application
  Support/Downex/tools`; Linux: `~/.local/share/downex/tools`; `downexd --tools-dir` changes it),
  checks yt-dlp's OpenPGP-signed checksums and Deno's checksums and Authenticode signature, and
  keeps them updated daily (`--tools-auto-update`, channel `--tools-channel`, nightly by default).
  Nothing is bundled with Downex. yt-dlp (Unlicense; the Windows/macOS/Linux executables are
  GPLv3+, see its
  [THIRD_PARTY_LICENSES.txt](https://github.com/yt-dlp/yt-dlp/blob/master/THIRD_PARTY_LICENSES.txt))
  is downloaded from github.com/yt-dlp; Deno (MIT, see its
  [LICENSE.md](https://github.com/denoland/deno/blob/main/LICENSE.md)) from github.com/denoland/deno.
  Whether a site's terms allow downloading is your responsibility.
- **Quicker.** One yt-dlp run serves the picker, the add and the start of the download (its result
  is kept in memory for 15 minutes); the formats are downloaded in byte ranges of at most 10 MiB
  over up to 4 connections to YouTube's servers (a built-in host rule; your own host rules win).
  Once you have watched a YouTube video for 1.5 seconds, the extension has Downex get its qualities
  in the background, so *Download* lists them at once (extension option "Prepare video qualities
  while you watch", on by default: one video at a time, at most about one a minute after the first
  ten, never with your sign-in cookies, and it never starts Downex or installs yt-dlp by itself).
  Downex's own yt-dlp is upstream's unpacked ("onedir") build where a release has one, which starts
  about a second sooner than the single-file one, and YouTube's HLS/DASH manifests are read only for
  a video without a direct MP4 format.
- **Resume and expiry.** Each chosen format is one file in `<name>.dxparts`, with a bit per chunk
  set only after it is on disk; a pause, restart or crash keeps the chunks done. Format addresses
  are never stored: every run reads the page again (or uses a recent result) and finds the chosen
  quality by a stable key; expired addresses (403) are renewed during the download. A quality the
  page no longer offers fails with `media_invalid`; Downex never switches to another one.
- **Cookies.** yt-dlp runs without your cookies unless you pass them (`-H "Cookie: ..."`, or the
  extension option "Send my sign-in cookies when a video needs it"); they go only into a private
  file deleted after the run.
- **From the browser.** A *Download* button over YouTube players and video entries in the popup
  open a quality picker; with confirmation on, the download waits in the window's dialog, where
  another quality can still be chosen. On other sites the same button lists yt-dlp's qualities
  next to what the browser loaded (see [Videos on every website](#videos-on-every-website)).
- **Errors.** `site_unsupported` (no video found), `site_unavailable` (private, removed, blocked or
  rate-limited), `auth_required` (sign-in, age or bot check), `site_error` (yt-dlp failed),
  `tools_unavailable` (yt-dlp/Deno could not be set up; see `downex tools`), `site_media_off`
  (Settings → Video sites, or `downex settings --site-media on`); `tools_installing` and
  `site_resolving` mean "try again", which every client does by itself.

## Videos on every website

The extension puts a *Download* button over every visible video of any page, in any frame, and
lists what Downex can save there; every video saved this way is **one `.mp4`**. See
[VIDEO-DETECT-SPEC.md](VIDEO-DETECT-SPEC.md) (its §10 lists what is not yet verified).

- **Which videos.** A video at least 200 × 112 pixels and at least half visible, but not a muted,
  looping, short background clip without controls. Never on DRM-protected playback (the element has
  media keys), live streams, or camera and call streams; private windows are never touched.
- **What the panel lists**, at once, from the browser's own traffic of that tab and frame (no page
  scripts or response bodies are read, and the browser itself downloads nothing):
  - **Files**: direct MP4/M4V videos the page loaded (*This video* for the one playing), with the
    picture size and the size; short clips (under 2 MiB or 30 s) come last. A file is handed off as an
    ordinary segmented, resumable download with the headers the browser sent for it (its own
    `Cookie` only, never other cookies).
  - **Streams**: HLS and DASH manifests, probed for their MP4 qualities when the panel opens; MPEG-TS
    streams are converted.
  - **Site qualities** from yt-dlp: at once on popular video sites (Vimeo, X, Facebook, Instagram,
    TikTok, Dailymotion, Reddit, Twitch, Streamable, and YouTube as before); elsewhere after *Look for
    more qualities*, or when the panel has nothing else.
- **Look-ahead.** While you watch a video on Vimeo, X, TikTok, Dailymotion, Reddit, Twitch or
  Streamable for 1.5 seconds, Downex gets its qualities in the background, as on YouTube (never on
  Facebook or Instagram, never with cookies). Nowhere else does yt-dlp run without your click.
- **Turning it off.** Extension options "Show a Download button over videos" (every site) and "Don't
  show the button on these sites" (a host or `*.domain` per line); the panel's *Don't show on <host>*
  and the popup's switch for the tab's site do the same. The popup and the badge still list the
  videos.
- **Popup.** *Videos on this page* groups the page's videos, streams (*Choose quality*) and files,
  marks protected or unsupported streams, and folds short clips away.

## Browser integration

The extension in `ext/` (one Manifest V3 build for Chrome, Edge and Firefox) hands downloads to
`downexd` through `downex-nmhost`, a native-messaging host that the browser starts itself.
Nothing listens on a network port. See [BROWSER-SPEC.md](BROWSER-SPEC.md).

**With a release of Downex,** Setup installs and registers the relay (on macOS the window
registers it each time it opens; see step 3 of [Installing on a Mac](#installing-on-a-mac)). The
extension is not in any browser store yet: it is not on the Chrome Web Store, and the listings on
Firefox Add-ons and Edge Add-ons are planned but not ready. Add it by hand from the installation
(a step-by-step guide per browser is in the [README](../README.md#the-browser-extension)):

- **Chrome, Edge, Brave and Chromium:** open the browser's extensions page, turn on
  *Developer mode*, choose *Load unpacked* and select the `extension\chrome-mv3` folder in
  Downex's installation folder (on macOS `Downex.app/Contents/Resources/extension/chrome-mv3`).
  The ID shown must be `dlgpbdafcmciojddbbjikgeeefipejcn`. Keep *Developer mode* on, or the
  browser may turn the extension off.
- **Firefox:** load `extension\firefox-mv3\manifest.json` as a temporary add-on in
  `about:debugging#/runtime/this-firefox`. Firefox removes it when it closes, because release
  and Beta versions keep only add-ons that Mozilla has signed. Developer Edition, Nightly and ESR
  can install it for good as an `.xpi` with `xpinstall.signatures.required` set to `false`.

The steps below build and load everything from source.

**1. Build and install the relay** next to `downexd` (it starts the daemon from there, or from
`$DOWNEX_DAEMON`). The Chromium extension IDs the relay accepts are built in with `-ldflags`,
joined with commas; `ext/EXTENSION_ID.txt` holds them one per line (`#` starts a comment, blank
lines are skipped): the ID of the extension in this repository, which is pinned by the public key
in `ext/keys/`, and the IDs of its store listings:

```powershell
# Windows (PowerShell)
$ids = (Get-Content ext\EXTENSION_ID.txt | ForEach-Object { $_.Trim() } | Where-Object { $_ -and -not $_.StartsWith('#') }) -join ','
go build -ldflags "-X main.DefaultChromeExtensionIDs=$ids" -o bin\downex-nmhost.exe ./cmd/downex-nmhost
```

```bash
# Linux
ids=$(grep -v '^[[:space:]]*#' ext/EXTENSION_ID.txt | tr -d ' \t\r' | grep -v '^$' | paste -sd, -)
go build -ldflags "-X main.DefaultChromeExtensionIDs=$ids" -o bin/downex-nmhost ./cmd/downex-nmhost
```

**2. Build the extension** (Node.js 24 and pnpm):

```bash
cd ext
pnpm install
pnpm build            # Chrome and Edge: ext/.output/chrome-mv3
pnpm build:firefox    # Firefox:         ext/.output/firefox-mv3
```

**3. Load it unpacked.** Chrome: `chrome://extensions`, Edge: `edge://extensions` → enable
*Developer mode* → *Load unpacked* → select `ext/.output/chrome-mv3`. The ID shown must equal
`ext/EXTENSION_ID.txt`. Firefox (140 or later) loads it through
`about:debugging#/runtime/this-firefox` → *Load Temporary Add-on…* → select `ext/.output/firefox-mv3/manifest.json` (add-on ID `extension@downex.app`).

**4. Register the relay** with the browsers, once, from where it is installed (again after
moving it):

```bash
downex-nmhost register                                  # all browsers (Windows) / those with a profile (Linux, macOS)
downex-nmhost register --browsers chrome,edge --chrome-extension-id <id> [--chrome-extension-id <id2>]
downex-nmhost register --dry-run                        # print the plan only
downex-nmhost register --system                         # all users (administrator / root)
downex-nmhost unregister [--browsers list] [--system]
```

| Flag | Meaning |
|---|---|
| `--chrome-extension-id ID` | Chromium extension allowed to connect (repeatable). Default: the built-in ID; without one, `register` refuses and asks for the flag. |
| `--firefox-extension-id ID[,ID]` | Firefox add-ons allowed to connect (repeatable or comma-separated; replaces the default). Default `extension@downex.app,browser@downex.local`. |
| `--browsers list` | Any of `chrome,edge,chromium,brave,firefox`. Default: all on Windows; on Linux and macOS the browsers that have a profile directory for this user. |
| `--system` | Register for all users instead of the current user. |
| `--keep` | Keep what this relay's existing manifests allow: add the missing extension IDs, rewrite only manifests that are missing, name another path or lack an ID, and never replace a `.bak`. The Mac window runs `register --keep` at each start. |
| `--dry-run` | Print the plan without changing anything. |

`register` writes the host manifest `com.downex.host` (absolute path of `downex-nmhost`, type
`stdio`, `allowed_origins` `chrome-extension://<id>/` for Chromium browsers,
`allowed_extensions` for Firefox) and prints its plan before it acts:

- **Windows:** one manifest per browser in `%LOCALAPPDATA%\Downex\nmhost\` (`--system`:
  `%ProgramData%\Downex\nmhost\`), named by the default value of
  `HKCU\Software\Google\Chrome\NativeMessagingHosts\com.downex.host`, and likewise
  `Software\Microsoft\Edge\…`, `Software\Chromium\…`, `Software\BraveSoftware\Brave-Browser\…`,
  `Software\Mozilla\NativeMessagingHosts\com.downex.host` (`--system`: below `HKLM`).
- **macOS:** `com.downex.host.json` in `~/Library/Application Support/Google/Chrome/NativeMessagingHosts`,
  `…/Chromium/…`, `…/Microsoft Edge/…`, `…/BraveSoftware/Brave-Browser/…` and
  `…/Mozilla/NativeMessagingHosts`; with `--system`, `/Library/Google/Chrome/NativeMessagingHosts`,
  `/Library/Microsoft/Edge/…`, `/Library/Application Support/Chromium/…` and
  `/Library/Application Support/Mozilla/…` (Brave has no folder for all users). The manifests name
  `Downex.app/Contents/Helpers/downex-nmhost`; `register` refuses to run from a temporary App
  Translocation copy (move the app to Applications first), and `--system` needs a Downex.app that
  only root and the admin group can change (`sudo chown -R root:wheel /Applications/Downex.app`).
- **Linux:** `com.downex.host.json` in `~/.config/google-chrome/NativeMessagingHosts`,
  `~/.config/chromium/…`, `~/.config/microsoft-edge/…`, `~/.config/BraveSoftware/Brave-Browser/…`
  (`$XDG_CONFIG_HOME` is honoured) and `~/.mozilla/native-messaging-hosts`; with `--system`,
  `/etc/opt/chrome/native-messaging-hosts`, `/etc/chromium/…`, `/etc/opt/edge/…`,
  `/etc/opt/brave.com/brave/…` and `/usr/lib/mozilla/native-messaging-hosts`.

Running it again changes nothing when everything is in place. A different manifest that is
already there is kept as `<file>.bak`, a different registry value in the key's `DownexBackup`
value; `unregister` removes only entries that name `downex-nmhost`'s manifests and puts those
backups back.

**What the relay does.** It speaks the browser's framing (32-bit native-endian length + UTF-8
JSON; requests up to 2 MiB, responses up to 1 MiB) and answers `hello` (`welcome` with the
daemon's version), `status` (running, active, queued), `ping`, `add` (`added` with the
download id) and `addMedia` (a stream: `manifestUrl`, `headers`, `pageUrl`, `title`; `added`, or
`rejected` with a message that starts with the daemon's code, such as `encrypted: ...`; with
`videoTrack`/`audioTrack` it saves those qualities), `probeStream` (a stream's MP4 qualities as
`videoInfo`, never an address), and, for the videos of web pages, `probeVideo` (`videoInfo`: the
offered MP4 qualities, never format addresses) and `addVideo`; every `added` carries `awaiting`
(held for confirmation). The welcome lists the features `video`, `prefetch` and `streams`, and
errors carry the daemon's code as `reason`. Every request carries an `id` that its response echoes; requests are served
concurrently, so responses may arrive out of order. `add` fields are validated with the
daemon's limits (http/https URL ≤ 32 KiB, GET or POST, ≤ 100 headers / 64 KiB without CR/LF or
hop-by-hop headers, base64 body ≤ 1 MiB with POST only, file name without path separators) and
forwarded to `DownloadService.Add` as a browser hand-off (`source` BROWSER, so it waits for
confirmation unless that is turned off; the browser's `size` and `mime` go along as hints for the
size shown and the category); `downexd` is started when it is not running (`hello` starts it in
the background; `status` never does). Errors carry the codes `invalid_request`, `daemon_unavailable`,
`rejected` (refused by the daemon) and `internal`. A message without a usable `id`, an empty
or oversized one, or a broken frame ends the relay; the extension reconnects. The relay logs to
`nmhost.log` next to `downexd.log` (rotated at 1 MiB), with URLs reduced to scheme, host and

## End-to-end tests

The end-to-end tests in `internal/e2e` build `downex`, `downexd` and `downex-nmhost` into a
temporary directory (with `-race` when the tests run with it), start the daemon on a private
endpoint with a temporary database and drive it through the CLI binary: add and wait, the
max-concurrent queue, pause/resume, a hard kill and restart, a second instance, `daemon stop`
and autostart. They also start `downex-nmhost` with piped stdin/stdout as a browser does and
hand it `hello` and `add` messages, with a running daemon and with an autostarted one. The
desktop tests install `downexd` next to a stand-in `downex-ui` that records its arguments, and
check that a browser hand-off waits for confirmation (the daemon starts the window with
`--confirm <id>`, fetches nothing and keeps the download across a restart), that
`downex confirm --name` then saves it in its category's folder, that hand-offs are queued at
once with confirmation off (and that the daemon then starts the download's progress window with
`--progress <id>`, never for a CLI add or with `--progress-window off`), PauseAll / ResumeAll, and
that settings (custom categories included) survive the CLI, invalid updates and a restart. The
progress-window tests (`progress_test.go`) check a throttled multi-connection download's live
`connection_info` in a Watch and in `downex info --json`, that `downex set --limit 64K` slows a
running download to that rate (within ±30%) and is kept across a pause and a restart, and that
`downex set --connections 2` lowers a running download's open connections without losing data.
The M6 tests (`m6_test.go`, `m6_net_test.go`) also build `downexd` with `-tags e2etest`, whose test
hooks (a faster clock, a recording stand-in for sleep and shutdown, trusted test certificates, an
update key) never exist in product builds; they cover queue admission, a scheduled start,
post-queue actions, HTTP CONNECT and SOCKS5 proxies, Digest site logins, host rules, the Mark of
the Web and signed update feeds.
