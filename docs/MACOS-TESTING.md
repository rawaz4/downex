# First run on a real Mac — test checklist

The macOS port ([MACOS-SPEC.md](MACOS-SPEC.md)) was written and compile-checked on Windows only:
`GOOS=darwin` builds and `go vet` for arm64 and amd64 (CGO_ENABLED=0), the darwin test binaries
compiled, and the OS-independent rules (paths, plists, socket-path limits, bundle layout, signing
order) unit-tested. **Nothing has run on macOS yet, and the Objective-C (menu-bar icon,
notifications, the windows' Dock policy, logout and activation hooks) has never been compiled.** This checklist is
the first run. Work through it in order and write down what you see, especially every item marked
**Record**. When a step fails, keep going where you can and collect the files listed in
[What to send back](#what-to-send-back).

`H=/Applications/Downex.app/Contents/Helpers` below (`export H=...` in Terminal).

## 0. What you need

- [ ] A Mac with macOS 13 Ventura or newer, logged in at the desktop (not only over ssh) with
      an administrator account. Ideally one Apple silicon and one Intel Mac (or Rosetta 2 on
      Apple silicon for the x86_64 half: `softwareupdate --install-rosetta`).
- [ ] Xcode Command Line Tools (`xcode-select --install`), Go 1.27+, Node.js 24 with pnpm
      (`corepack enable pnpm`), Wails CLI v2.17.0
      (`go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.0`, `$(go env GOPATH)/bin` on PATH).
- [ ] Chrome and Firefox (Edge, Brave and Chromium if you have them).
- [ ] The repository copied to the Mac. It was written on Windows: check that
      `file scripts/build-macos.sh packaging/macos/*.plist` reports no "CRLF" (convert with
      `sed -i '' $'s/\r$//' <file>` if it does).
- **Record:** `sw_vers`, `uname -m`, `go version`, `clang --version | head -1`,
  `xcrun --show-sdk-version` (the window's macOS 14 activation calls need SDK 14 or newer).

## 1. Tests on the Mac (before building the app)

From the repository root:

- [ ] `go vet ./...` and `go build ./...`
- [ ] `go test ./...` — pure Go. New darwin tests: `internal/platform` (detached start, close-on-exec,
      `F_PREALLOCATE`, sparse files, LaunchAgent written into a temporary `$HOME` and checked with
      `plutil`), `internal/ipc` (endpoint from `getconf DARWIN_USER_TEMP_DIR`, the 103/104-byte
      socket-path boundary, `LOCAL_PEERCRED`), `internal/dlock`, `internal/client`
      (`TestFindDaemonInBundle`), and the real-process tests in `internal/e2e`.
      **Record** each skip message (`go test -v ./internal/platform ./internal/ipc | grep -i skip`):
      a skipped `TestPreallocateReservesBlocks` means APFS does not report the reservation in
      `st_blocks`.
- [ ] `CGO_ENABLED=1 go test -race ./...` — this also compiles the Objective-C of `internal/tray`
      and of the clipboard monitor (`internal/clipboard/clipboard_darwin.m`, M6).
      **Record** every compiler error or warning from `tray_darwin.m` and `clipboard_darwin.m`.
- [ ] M6 tests that only run on a Mac or another unix system: `go test -count=1 -v ./internal/filemark`
      (`filemark_unix_test.go` writes and reads back `com.apple.quarantine` and `kMDItemWhereFroms`
      on a file of the temporary folder), `-run TestRelaunchScript ./internal/update` (the `/bin/sh`
      relaunch helper of an installed update), `./internal/secrets` and `./internal/sysproxy`
      (the `scutil --proxy` parser; the real `scutil` and keychain paths are exercised in §12b).
      **Record** each skip message.
- [ ] `CGO_ENABLED=1 go test -count=1 -v -run TestMenuBarSmoke ./internal/tray` from Terminal at the
      desktop: a Downex icon appears in the menu bar for a moment (the test drives its menu and
      removes it). It is skipped with `-short` and without a GUI session.
- [ ] `go test ./packaging/macos` (templates and the build script's functions with stand-ins).
- [ ] The real window host: `(cd ui/frontend && pnpm install && pnpm build)`, then
      `go vet -tags desktop,production ./ui` (needs cgo: this compiles `ui/dock_darwin.go`,
      `ui/dock_darwin.m` and Wails). **Record** every compiler error or warning from `dock_darwin.m`.
- [ ] `(cd ui/frontend && pnpm check && pnpm test)` and `(cd ext && pnpm install && pnpm test)`.

## 2. Build

- [ ] `bash scripts/build-macos.sh -v 0.4.0-dev` → `dist/macos/Downex.app` and
      `dist/macos/Downex-0.4.0-dev.dmg`. **Record** any warning, in particular
      "downexd-agent does not link AppKit" (the agent was built without its menu-bar icon).
- [ ] Every executable is universal: `lipo -info` on `Contents/MacOS/Downex`,
      `Contents/Helpers/{downexd,downex,downex-nmhost}`,
      `Contents/Helpers/Downex Progress.app/Contents/MacOS/Downex` and
      `Contents/Library/LoginItems/Downex Agent.app/Contents/MacOS/downexd-agent` says `x86_64 arm64`.
- [ ] `otool -L ".../downexd-agent"` lists AppKit, UserNotifications and CoreGraphics;
      `otool -L .../Helpers/downexd` lists only libSystem (pure Go).
- [ ] `codesign --verify --deep --strict --verbose=2 dist/macos/Downex.app` passes;
      `codesign -d --entitlements - dist/macos/Downex.app` shows the two entitlements of
      `packaging/macos/entitlements.plist`.
- [ ] `vtool -show-build` (or `otool -l | grep -A3 LC_BUILD_VERSION`) on every executable (the window,
      the progress app, the agent and `Contents/Helpers/{downexd,downex,downex-nmhost}`) shows
      minos 13.0 for both architectures (Go 1.27 stamps 13.0 on the pure-Go helpers too).
- [ ] On Apple silicon with Rosetta: `arch -x86_64 dist/macos/Downex.app/Contents/Helpers/downexd --version`.

## 3. Install

- [ ] Open the DMG: the volume "Downex 0.4.0-dev" shows Downex, an Applications link and
      `Read Me.txt`. Drag Downex onto Applications, eject the DMG.
- [ ] Gatekeeper (only for a build that came from another Mac, by AirDrop or a browser download,
      so that it carries `com.apple.quarantine`): macOS 13–14 right-click → Open → Open; macOS 15+
      open once, then System Settings → Privacy & Security → Open Anyway. **Record** whether the
      relay (started by the browser) or the agent (started at login) is blocked afterwards, and
      whether `xattr -dr com.apple.quarantine /Applications/Downex.app` was needed.
- [ ] Optional: open a quarantined copy directly from `~/Downloads` or the DMG (App Translocation).
      `"<that copy>/Contents/Helpers/downex-nmhost" register` must refuse with the App Translocation
      message, and `downex settings --launch-at-login on` against a daemon started from it must
      fail with "Downex runs from a temporary copy…" (the login item is never pointed at the copy).

## 4. First start of the window

- [ ] Open Downex from Applications: one window, one Dock icon. **Record** the time to first paint.
- [ ] App menu: *About Downex* (shows the version), *Settings…* ⌘, (opens Settings), *Hide* ⌘H,
      *Quit* ⌘Q; *File → Close Window* ⌘W; the standard *Edit* menu works in text fields (⌘C, ⌘V,
      ⌘A in the Add dialog's URL field); the *Window* menu.
- [ ] Standard title bar; the window follows the system appearance (switch Light/Dark in
      System Settings → Appearance while it is open; also start it in Dark).
- [ ] The window started the daemon. `ps -axo pid,command | grep -i [d]ownex` shows
      `…/Downex Agent.app/Contents/MacOS/downexd-agent` — clients autostart the login item
      (the daemon with the menu-bar icon), not `Helpers/downexd`.
- [ ] `$H/downex daemon status` reports running, version 0.4.0-dev and the endpoint.
- [ ] The endpoint: `T=$(getconf DARWIN_USER_TEMP_DIR); ls -la "$T/downex-$(id -u)"` shows
      `rpc.sock` and `daemon.lock` in a `drwx------` directory of yours;
      `printf %s "$T/downex-$(id -u)/rpc.sock" | wc -c` is at most 103. **Record** the length.
- [ ] `ls -ld ~/Library/Logs/Downex ~/Library/Application\ Support/Downex` → both exist, the logs
      directory `drwx------`; `downexd.log` and `ui.log` are written there.
- [ ] Quit the window (⌘Q): the daemon keeps running (`downex daemon status`).
- [ ] Open the window again; right-click its Dock icon → *Quit*: it quits (`ui.log` ends with
      "quit requested by the system").
- [ ] **Logout, restart and shut down with windows open.** Open the window and a progress window
      (§9), then  → *Log Out*: macOS must not say that Downex cancelled it, and both processes end
      (`ui.log`: "quit requested by the system" once per window). Log in again; repeat with
      *Restart…* (and, if you can, *Shut Down…*) with the main window and two progress windows open.
      **Record** any "Downex cancelled …" or "… not responding" message, and how long it waited.

## 5. Menu-bar icon (Downex Agent)

- [ ] The icon is in the menu bar (arrow over a bar; black on a light menu bar, white on a dark
      one and when highlighted); the agent has no Dock icon. **Record** how it looks on a Retina
      and a non-Retina display if you have both.
- [ ] Hovering shows "Downex — N active, X/s". The menu: a disabled first line "N active · X/s",
      then *Open Downex*, *Pause all*, *Resume all*, *Settings…*, *Quit Downex*.
- [ ] Start a long download (`$H/downex add <url of a large file>`) and keep the menu open: the
      first line updates while the menu is open.
- [ ] *Pause all* / *Resume all* work; *Open Downex* and *Settings…* open the window and bring it
      to the front, also when it is already open behind other apps or minimised. **Record** on
      macOS 14+ whether it really comes to the front (cooperative activation) and whether it gets the
      keyboard focus, or only shows in front of the other windows (see §9a).
- [ ] `$H/downex settings --tray off` removes the icon at once; `--tray on` brings it back.
- [ ] *Quit Downex*: downloads are paused and saved, `downex daemon status` says not running,
      `downexd.log` ends with a clean shutdown; the process exits within ~15 s.
- [ ] Start it again (open the window). Quit `downexd-agent` from Activity Monitor (Quit, not
      Force Quit): same graceful shutdown. Log out with a download running and log in again: its
      progress was kept. **Record** whether logout waited for the agent or showed "an app
      prevented logout".
- [ ] App Nap: close all windows, let a long download run for a few minutes; Activity Monitor's
      *App Nap* column says No for downexd-agent and the speed does not drop.
- [ ] Without a GUI session: `$H/downex daemon stop`, then `ssh localhost "$H/downex daemon start"`
      (Remote Login on): the agent runs without an icon and does not crash (`downexd.log`). Stop it
      again and open the window, which starts one with the icon.

## 6. Notifications

- [ ] The first completed download asks "… would like to send you notifications"; allow it.
      **Record** the name and icon in the request and in the notification ("Downex Agent" or
      "Downex"; the Downex icon expected).
- [ ] "Download complete" with the file name, silent; it also shows while the window is the
      active app. A failed download (a URL that returns 404) shows "Download failed" and a short
      error. More than 3 completions within 5 s give one summary.
- [ ] Clicking a notification opens the window.
- [ ] Click an older notification in Notification Center while the agent is not running (quit it
      first): the agent starts, and the window opens.
- [ ] Turn notifications off (System Settings → Notifications → Downex Agent): nothing is shown,
      one warning in `downexd.log`, no crash. Turn them back on.

## 7. Browser registration

- [ ] `$H/downex-nmhost register --dry-run` prints the plan; `$H/downex-nmhost register` writes
      `com.downex.host.json` into `~/Library/Application Support/Google/Chrome/NativeMessagingHosts`
      (and Chromium, `Microsoft Edge`, `BraveSoftware/Brave-Browser`, `Mozilla` for the browsers you
      have run). Each manifest's `path` is `/Applications/Downex.app/Contents/Helpers/downex-nmhost`.
      Running `register` again changes nothing.
- [ ] Optional, all users: `sudo $H/downex-nmhost register --system --dry-run` refuses while
      Downex.app belongs to you (expected). After `sudo chown -R root:wheel /Applications/Downex.app`
      it writes `/Library/Google/Chrome/NativeMessagingHosts`, `/Library/Microsoft/Edge/…`,
      `/Library/Application Support/Chromium/…` and `/Library/Application Support/Mozilla/…`
      (Brave has no system folder). Undo with `sudo $H/downex-nmhost unregister --system`.
- [ ] Optional, after the step above: `sudo chown "$USER" "/Applications/Downex.app/Contents/Library/LoginItems"`;
      `sudo $H/downex-nmhost register --system --dry-run` must refuse and name `…/Contents/Library/LoginItems`
      (the login item that the relay starts is checked too). The same for
      `/Applications/Downex.app/Contents/MacOS` and `…/Contents/Info.plist`. Undo with
      `sudo chown -R root:wheel /Applications/Downex.app`.
- [ ] Chrome: `chrome://extensions` → Developer mode → Load unpacked → ⌘⇧G →
      `/Applications/Downex.app/Contents/Resources/extension/chrome-mv3`. The ID equals
      `ext/EXTENSION_ID.txt`, and the popup shows Downex connected (daemon version).
- [ ] Firefox (140 or later): the add-on from addons.mozilla.org, or until it is listed
      `about:debugging#/runtime/this-firefox` → Load Temporary Add-on… →
      `…/extension/firefox-mv3/manifest.json` (ID `extension@downex.app`); the popup shows Downex
      connected.

## 8. Capture test

- [ ] In Chrome, click a link to a large file (50 MB or more, a server with range support). The
      browser hands it over; the window comes up with the confirmation dialog (started by the
      daemon as `open -n Downex.app --args --confirm <id>`, or the open window comes forward).
- [ ] *Start download*: its progress window opens. Pause/Resume, the speed limit tab and the
      details (connections) work. On completion the dialog shows; *Open* opens the file with its
      default app, *Open folder* reveals it in the Finder, and *Open with…* is not shown.
- [ ] **Downloads folder access.** **Record** whether macOS asks for access to the Downloads
      folder, which process it names (Downex, Downex Agent or downexd), and whether a download
      into `~/Downloads` fails with "operation not permitted" when access was denied.
- [ ] `$H/downex daemon stop`, then capture again: the relay starts the agent (with the icon).
- [ ] The same with Firefox, and Edge/Brave/Chromium if available. `nmhost.log` in
      `~/Library/Logs/Downex` lists each session without full URLs.
- [ ] With `$H/downex settings --confirm-browser off` a capture starts at once and opens its
      progress window; turn it back on.

## 9. Progress windows

- [ ] Each progress window is a separate "Downex Progress" process (Activity Monitor) with **no
      Dock icon**. **Record** whether a Dock icon flashes briefly when one opens (Wails sets the
      regular policy at launch; the window then switches itself to an accessory app) or stays.
- [ ] Double-click an unfinished download in the main list: its progress window opens; doing it
      again brings the same window forward (one per download).
- [ ] Closing a progress window ends its process; the download goes on.

## 9a. Focus (macOS 14 Sonoma and 15 Sequoia; also once on 13)

From macOS 14 an app that is not active can only ask to be activated. Downex opens its windows
with `open -n` when they answer the user, with `open -n -g` for the progress windows the daemon
opens by itself, yields activation before the window opens a progress window, and puts a window
that did not become active in front of the others without the focus (on macOS 13 every
window activates itself, as Wails does, so there the progress windows below take the focus too).
For each case **Record** on every macOS version: does the window come to the front, does it get the keyboard focus (type ⌘A or
press Esc), and which app was active before.

- [ ] Window closed, Chrome in front: menu-bar icon → *Open Downex*. Expected: the window opens in
      front (with the focus, or at least in front of Chrome).
- [ ] The same with the window already open behind Chrome, and with it minimised.
- [ ] Click a "Download complete" notification, window closed and window open behind Chrome.
- [ ] Browser hand-off with confirmation on (§8), window closed and window open behind Chrome:
      the confirmation dialog must be visible without clicking anything.
- [ ] Browser hand-off with `--confirm-browser off`: the new progress window shows in front of
      Chrome; Chrome keeps the focus (typing goes on in Chrome).
- [ ] In the main window, double-click a running download: its progress window opens in front of
      the main window, with the focus. Again with it open: it comes forward.
- [ ] Download complete dialog (a marked download completes while you type in another app): it
      shows in front, and typing goes on in the other app.

<!-- [popups] -->
## 9b. Native window: tooltips, lists, dialogs (macOS 13, 14 and 15)

The native window's tooltips put an AppKit tooltip rectangle under the pointer when the text
changes (public NSView API; checked on screen on macOS 26 only). **Record** on each older macOS
whether this still works there.

- [ ] Hover a truncated file name in the main list and keep still: its whole name shows after the
      usual tooltip delay (about 1.5 s), in the system's tooltip look. Move straight to another
      truncated name without leaving the window: its name shows too, sooner.
- [ ] Open a Select (Confirm → Category): the list opens just under its box; the arrow keys
      move the highlight, Esc closes it, a click outside closes it. Resting two fingers on the
      trackpad outside the list does **not** close it; scrolling does.
- [ ] With VoiceOver on, open a Select and a menu: VoiceOver reads the list or menu and follows
      the highlight as the arrows move it.
- [ ] Settings → choose a folder: the picker opens as a sheet; close the window with ⌘W while it
      is open (or quit from the menu-bar icon): Downex does not hang, and the next picker opens.
<!-- [/popups] -->

## 10. Opening downloaded files

- [ ] *Open* on a `.dmg`, `.pkg`, `.command`, `.sh` and a file with the execute bit asks for
      confirmation first; an `.app` folder counts as a program; *Open folder* on a folder named
      `Something.app` selects it in its parent instead of opening it.
- [ ] *Open* on a file type without an app shows "no app is set to open … on this Mac".
- [ ] **Record** `xattr -l <downloaded file>`: with *Mark downloaded files* on (the default; M6) a
      completed download has `com.apple.quarantine` = `0081;<hex Unix time>;Downex;<UUID>` and
      `com.apple.metadata:kMDItemWhereFroms` (a binary plist: `mdls -name kMDItemWhereFroms <file>`
      lists the address without user name or password, then the referrer). Opening a downloaded
      `.app` or `.dmg` then shows Gatekeeper's "downloaded from the Internet" prompt with Downex
      as the source. **Record** that prompt's wording. With
      `$H/downex settings --mark-files off` a new download has neither attribute.

## 11. Streaming media

- [ ] `$H/downex media <unencrypted HLS .m3u8 or DASH .mpd URL> --list` shows the tracks;
      `$H/downex media <url> --wait` writes one `.mp4` (fMP4 streams) or `.ts` (MPEG-TS). Play it
      (QuickTime for `.mp4`; `.ts` needs VLC or IINA).
- [ ] In the browser, a page with such a stream: the popup lists it under "Media on this page";
      the hand-off shows the quality picker in the confirmation dialog; the file is joined.
- [ ] Pause a stream download, quit the agent, start it again, resume: fragments already on disk
      are not fetched again.

## 12. Launch at login

- [ ] `$H/downex settings --launch-at-login on` writes `~/Library/LaunchAgents/com.downex.agent.plist`
      (`-rw-r--r--`, yours). `plutil -lint` passes and `plutil -p` shows Label `com.downex.agent`,
      ProgramArguments
      `[/Applications/Downex.app/Contents/Library/LoginItems/Downex Agent.app/Contents/MacOS/downexd-agent, --background]`,
      RunAtLoad true, KeepAlive false, ProcessType Interactive. `launchctl` is not called.
- [ ] **Record** the "Background Items Added" notification and the name shown in
      System Settings → General → Login Items → Allow in the Background.
- [ ] Log out and in: the agent runs (`ps`), the icon is there, no window opens.
- [ ] Move Downex.app (for example into a subfolder of Applications), open it: the daemon rewrites
      the LaunchAgent with the new path at its start. Move it back.
- [ ] `--launch-at-login off` removes the file; after the next login nothing starts.

## 12a. Queues, schedules and post-queue actions (M6)

- [ ] `$H/downex queue create Night --max 1`, add two downloads with `--queue Night`: both stay
      *queued*; `$H/downex queue start Night` runs them one at a time; the window's sidebar shows
      the queue with its running dot.
- [ ] `$H/downex queue schedule Night --start <in 2 minutes> --stop <in 10 minutes>`: the queue starts
      at that minute (`downexd.log`: `"queue started"` with `"by":"schedule"`) and stops at the stop
      time; its running downloads go back to *queued*.
- [ ] Sleep and wake: set a start a few minutes ahead, put the Mac to sleep (Apple menu → Sleep)
      until after that minute, wake it. The queue starts within about 30 s of waking (downexd
      notices the jump of the wall clock; there is no wake timer on macOS). Also after changing
      the time zone in System Settings → General → Date & Time. **Record** how long it took.
- [ ] Post-queue action **Quit Downex** (`$H/downex queue update Night --post quit`): when the
      queue has finished, the menu bar shows the notification "Quitting Downex in 60 s", the window
      shows a countdown banner with *Cancel*; `$H/downex cancel-post Night` (or *Cancel*) stops it.
      Without cancelling, the agent quits after 60 s.
- [ ] **Sleep** (`--post sleep`): after the countdown the Mac sleeps (`pmset sleepnow`).
- [ ] **Shut down** (`--post shutdown`, with nothing unsaved open): after the countdown macOS asks
      for permission: "Downex Agent wants to control System Events" (Automation). **Record** the
      prompt and what happens after *OK* (System Events shuts down; apps with unsaved changes may
      stop it, Downex never forces them) and after *Don't Allow* (`downexd.log` says why).
- [ ] `--post hibernate` is refused on macOS ("post_action hibernate is not supported on this
      system"), and the window's queue dialog does not offer it.
- [ ] Keep awake (0.4.1): while `Night` (with a post action) has a download running, `pmset -g
      assertions` lists a `caffeinate` PreventUserIdleSystemSleep assertion; it is gone a few seconds
      after the queue finished and its countdown ended or was cancelled. A download in the main queue
      alone adds no assertion. **Record** the `pmset -g assertions` lines.
- [ ] Sleep through a countdown (0.4.1): let a `--post sleep` queue finish, put the Mac to sleep
      within the 60 s, wake it a few minutes later: the countdown is reported cancelled, and the Mac
      does not go to sleep again.

## 12b. Network (M6)

- [ ] System proxy: System Settings → Network → Details → Proxies: set a web (HTTP) and secure
      (HTTPS) proxy, or a SOCKS proxy, or a configuration (PAC) URL, plus *Bypass* hosts. A download
      in the default proxy mode (`$H/downex proxy` shows `system`) goes through it; a bypassed host
      does not. **Record** `scutil --proxy` and the result.
- [ ] `$H/downex proxy set manual --https <host:port>` and `--socks socks5://<host:port>`;
      `$H/downex proxy set pac <url>`; `$H/downex proxy set none`. `--socks socks5://u:p@<host:port>`
      is refused (the error names the field, not the password).
- [ ] PAC helper (0.4.1): with a working PAC URL, a download goes through the proxy the script
      names, and `ps -ax -o pid,ppid,command | grep downexd` shows a second `downexd-agent` (or
      `downexd`) whose parent is the daemon: the script's helper process. No second menu-bar icon or
      Dock icon appears for it. A script with `while(true){}` in `FindProxyForURL` makes downloads go
      direct and the daemon keeps running. **Record** the `ps` lines.
- [ ] Site login: `$H/downex login add 'https://<host>/*' --user <name>` asks for the password
      (no echo). Keychain Access (login keychain) then shows a password item *Downex* with the account
      `site_login:<id>` (the proxy password: `proxy`); `security find-generic-password -s Downex -a
      site_login:<id>` finds it. A download from that server, which asks for Basic or Digest
      authentication, completes; with a wrong password it fails with `auth_required`, and the
      window offers *Add site login…*. **Record** whether macOS asked to allow access to the keychain
      item, and for which program.
- [ ] Keychain input (0.4.1): while `login add` stores a password, `ps -ax -o command | grep
      security` shows only `security -i` (the password goes over its input, never on the command
      line). A password longer than about 1.9 KB is refused. **Record** the item's *Access Control*
      list in Keychain Access, and whether a password with non-ASCII characters (for example `pässwörd`)
      still works for a download (a known open question: `find-generic-password -w` may print it
      as hex).
- [ ] `$H/downex login rm <id>` removes the keychain item. A private daemon (`--db`) keeps its
      passwords in `<its folder>/secrets` instead, never in the keychain.

## 12c. Clipboard, virus scan (M6)

- [ ] `$H/downex settings --clipboard on`; copy the address of a `.zip` file in a browser: within a
      second Downex offers it like a browser hand-off (the confirmation dialog). Copy it again: not
      offered again within 10 minutes. Copying ordinary text, a link without a known extension, or
      a link with a user name and password (`https://u:p@host/f.zip`) does nothing. **Record** whether macOS shows a paste permission prompt ("Downex Agent would
      like to paste from …"), on which macOS version, and what *Don't Allow* does.
- [ ] A password copied from a password manager that marks it concealed (for example the Passwords
      app or Keychain Access → *Copy Password*) is not read (`downexd.log` shows nothing for it).
- [ ] Without the agent (only `$H/downexd` runs) the clipboard is not watched; the log says so once.
- [ ] Virus scan: `$H/downex settings --av-scan on --av-command /usr/bin/false --av-args '[File]'`:
      every completed download stays completed, with the note and the notification "Scanner
      reported a problem with <name>". With `/usr/bin/true` nothing is shown. Turn it off again.

## 12d. Updates (M6)

Needs a build with `DOWNEX_UPDATE_PUBLIC_KEY` (see `scripts/build-macos.sh`), a newer signed build
in a DMG, and a feed made with `go run ./scripts/release-sign` (`asset`, then `sign`) served over
https.

- [ ] `$H/downex settings --update-feed https://<host>/downex.json`, `$H/downex update check`:
      shows the newer version. A manifest changed after signing is refused ("signature").
- [ ] `$H/downex update install` (Downex.app in /Applications, not translocated): the DMG is
      downloaded and checked, mounted (`hdiutil`; it never appears in Finder), the new app's code
      signature must have the running app's team; then the bundle is replaced and the agent starts
      again in the background. **Record** the log lines of the install, `codesign -dv
      /Applications/Downex.app`, and whether anything was left in /Applications
      (`ls -a /Applications | grep -i downex`: a `.downex-update-*` folder) or still mounted
      (`hdiutil info`).
- [ ] An ad-hoc signed running build (no `DEVELOPER_ID`) installs an ad-hoc signed update whose
      `CFBundleShortVersionString` equals the manifest's version, without any environment variable,
      and refuses one whose version differs. The installed bundle has no `com.apple.quarantine`
      attribute (`xattr -lr /Applications/Downex.app`), Downex starts again, and the window shows
      the pop-up (*Update now* / *Later*) after an automatic check finds an update.

## 13. Security checks (optional)

- [ ] A second user account: its daemon has its own socket directory; that user's
      `downex --endpoint "<your rpc.sock>" ls` cannot reach your daemon.
- [ ] `sudo $H/downex daemon start` (no daemon running) refuses to start one from an elevated
      process; `sudo $H/downexd` refuses without `--allow-elevated`.

## 14. Uninstall

- [ ] `$H/downex settings --launch-at-login off`, `$H/downex daemon stop`,
      `$H/downex-nmhost unregister` (and `--system` with sudo if used); remove the extension in each
      browser; quit the window; move Downex.app to the Trash.
- [ ] No Downex process is left; the manifests and `~/Library/LaunchAgents/com.downex.agent.plist`
      are gone. What stays until you delete it: `~/Library/Application Support/Downex`,
      `~/Library/Logs/Downex` and `$(getconf DARWIN_USER_TEMP_DIR)downex-$(id -u)`.
- [ ] To see the first-run prompts again on the next test: `tccutil reset All com.downex.agent`
      and `tccutil reset All com.downex.app`.

## What to send back

- The checklist with every **Record** item and every failure.
- `~/Library/Logs/Downex/` (`downexd.log`, `ui.log`, `nmhost.log`; they hold no full URLs).
- Crash reports: `~/Library/Logs/DiagnosticReports/` (`downexd-agent*`, `Downex*`, `downexd*`,
  `downex-nmhost*`).
- The output of the failing `go test` runs and of `scripts/build-macos.sh`.
- For a menu-bar or notification problem: `log show --last 10m --predicate 'process == "downexd-agent"'`.
