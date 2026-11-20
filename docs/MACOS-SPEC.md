# macOS specification

Target: **macOS 13 Ventura or newer, one universal build (arm64 + x86_64)** — the minimum Go 1.27
supports (its linker stamps every binary with macOS 13.0). Chrome, Edge, Brave, Chromium and
Firefox integrate through the existing extension and native messaging; Safari is later (needs a
Safari Web Extension inside the app, built with Xcode).
No Apple Developer account yet: dev builds are ad-hoc signed (`codesign -s -`) and open via
right-click → Open; the build script supports Developer ID signing + notarization when credentials exist.
Nothing of this has run on a Mac yet; the first run follows [MACOS-TESTING.md](MACOS-TESTING.md).

## 1. Bundle layout
```
Downex.app/
  Contents/
    Info.plist                     CFBundleIdentifier com.downex.app, LSMinimumSystemVersion 13.0
    MacOS/Downex                   the Wails window (main mode)
    Helpers/
      downexd                      daemon (universal)
      downex                       CLI (universal; the user may symlink it into PATH)
      downex-nmhost                native-messaging relay (universal)
      Downex Progress.app/         same window binary + Info.plist with LSUIElement=true,
                                   CFBundleIdentifier com.downex.progress (no Dock icon per window)
    Library/LoginItems/
      Downex Agent.app/            Contents/MacOS/downexd-agent → runs downexd with the menu-bar icon;
                                   LSUIElement=true, CFBundleIdentifier com.downex.agent (owns notifications)
    Resources/
      extension/chrome-mv3, extension/firefox-mv3, EXTENSION.txt, icons
```
- `downexd` finds its siblings relative to its own executable (existing `client.FindDaemon` logic) and,
  inside a bundle, finds the window at `../MacOS/Downex` and the progress app at
  `Helpers/Downex Progress.app` (resolve via the executable path; never via the current directory).
  As built, `client.Layout` (`client.LayoutOf`/`FindLayout`, symbolic links resolved, names compared
  without case, the bundle may be renamed) is the one owner of these paths: from any executable of the
  bundle it gives `Dir` = `Contents/Helpers`, `Window`, `Progress` and `Agent`; outside a bundle the old
  sibling rules apply (`downex-ui` next to `downexd`).
- The menu-bar icon and notifications run in **Downex Agent.app** (an app bundle so
  `UNUserNotificationCenter` works and notifications show the Downex icon). Its executable is downexd built
  with cgo + Cocoa (`internal/tray` darwin implementation); a darwin build without cgo has no tray
  (`tray.New` returns `ErrNotSupported`, as on Linux, so that the daemon never holds browser hand-offs
  for an icon that cannot ask), which keeps the pure-Go cross-compile working.
- Autostart (decision of the integration): inside the bundle the clients (CLI, relay, window) start
  the agent's executable when it is installed, else `Helpers/downexd` (`client.FindDaemon`;
  `$DOWNEX_DAEMON` still wins), so the user's daemon has the menu-bar icon however it was started, as
  `downexd.exe` has its tray icon on Windows. Started without a GUI session (ssh) the agent runs
  without an icon.

## 2. Platform layer (`internal/platform`, darwin)
- `Preallocate`: `fcntl(F_PREALLOCATE)` (contiguous, then non-contiguous) + `ftruncate`; APFS/HFS+; on
  EOPNOTSUPP just `ftruncate`.
- `StartDetached`: `Setsid`, stdio to /dev/null. `StartApp(exe, args...)`: when `exe` is inside a `.app`
  (path contains `.app/Contents/MacOS/`), launch the bundle with `/usr/bin/open -n <bundle> --args …`
  (new instance, activated by Launch Services: it answers the user's action); otherwise like StartDetached.
  As built, `StartAppInBackground` (darwin only) adds `-g` for a window nobody asked for at that moment:
  downexd opens the progress app that way (a browser hand-off that starts at once, the download
  complete dialog: `cmd/downexd` `opensInBackground`), everything else (the menu-bar icon's Open and
  Settings…, a click on a notification, a hand-off to confirm, a progress window opened from the main
  window) activated. From macOS 14 activation is cooperative (an app that is not active is activated
  only when the active app yields to it), so a window process yields to Downex.app and the progress
  app before it opens a progress window (`allowForegroundHandoff`, §5).
- `DownloadsDir`: `~/Downloads`. `LogDir`: `~/Library/Logs/Downex` (0700).
- `SetLaunchAtLogin`: LaunchAgent `~/Library/LaunchAgents/com.downex.agent.plist` (Label, ProgramArguments
  = [<Agent.app>/Contents/MacOS/downexd-agent, --background], RunAtLoad true, KeepAlive false,
  ProcessType Interactive); remove the file to disable. Do not call `launchctl` (takes effect at next login).
  The file is 0644 and the user's (never written as root). downexd refuses to enable it while it runs
  from an App Translocation copy (`client.Layout.Translocated`: a path below `…/AppTranslocation/`),
  which goes away; disabling still works.
- `Elevated`: euid == 0 (as built also set-uid/set-gid, as on Linux).
- Store default path: `~/Library/Application Support/Downex/downex.db` (os.UserConfigDir).
- Rescue of a partial file (ENGINE-SPEC §7.4, DESKTOP-SPEC §11): downexd (Downex Agent, or
  `Helpers/downexd`) reads and renames the browser's partial file itself. Privacy protection checks the
  responsible process: in `~/Downloads`, `~/Desktop` or `~/Documents` it needs that folder's "Files and
  Folders" permission (downloads already go to `~/Downloads`, so the agent normally has it). EPERM or
  EACCES there is `rescue_no_access`, whose text adds " Allow Downex Agent in System Settings > Privacy
  & Security > Files and Folders.". The window's open panel grants the agent nothing (Downex is not
  sandboxed and passes only a path). `rename(2)` of a file another process holds open succeeds: a
  browser still writing could only write the same server bytes at the same offsets, the extension
  cancels a running entry before it erases it, and a partial that changed during the proof is
  refused (`rescue_locked`). `F_PREALLOCATE` keeps the data already in the file.
- *Polite mode (POLITE-SPEC §2):* probes use unprivileged datagram ICMP sockets
  (`golang.org/x/net/icmp`, `udp4`/`udp6`, pure Go, no entitlement; a leading IPv4 header is
  stripped), else TCP connect probes. Only global addresses are probed, so the Local Network prompt
  never appears.

## 3. IPC (`internal/ipc`, darwin)
- Socket: `$TMPDIR/downex-<uid>/rpc.sock` (per-user TMPDIR from `confstr`/env; directory 0700, owner
  checked), fallback `~/Library/Caches/Downex/run/rpc.sock`. `sun_path` is limited to 104 bytes on macOS:
  refuse (with a clear error) and fall back when longer. Peer check: `LOCAL_PEERCRED` (xucred uid).
  As built: the temporary directory is `confstr(_CS_DARWIN_USER_TEMP_DIR)`, read without cgo with
  `/usr/bin/getconf DARWIN_USER_TEMP_DIR` once per process, `$TMPDIR` only when that fails, so every
  process of the user (launchd agent, Terminal, browser-started relay) finds the same socket; a
  `DOWNEX_ENDPOINT` override must be absolute and at most 103 bytes. While listening the daemon
  refreshes the times of the socket, its directory and the lock hourly, against the periodic cleanup
  of `/var/folders`.
- Lock: flock on `<dir>/daemon.lock` (existing unix code).

## 4. Browser registration (`downex-nmhost register`, darwin)
Per user (`~/Library/Application Support/…`): `Google/Chrome/NativeMessagingHosts`, `Chromium/…`,
`Microsoft Edge/NativeMessagingHosts`, `BraveSoftware/Brave-Browser/NativeMessagingHosts`,
`Mozilla/NativeMessagingHosts`. System (`--system`, root): `/Library/Google/Chrome/NativeMessagingHosts`,
`/Library/Microsoft/Edge/NativeMessagingHosts`, `/Library/Application Support/Chromium/NativeMessagingHosts`,
`/Library/Application Support/Mozilla/NativeMessagingHosts`. Manifest `path` = absolute path of
`Downex.app/Contents/Helpers/downex-nmhost`.
As built: Brave has no system folder, so `--system` leaves it out and refuses `--browsers brave`;
`register` refuses to run from an App Translocation copy; `--system` accepts a root-owned bundle that
only root and the wheel or admin group can write (`/Applications` is root:admin 0775), and refuses a
copy that belongs to the user (`sudo chown -R root:wheel Downex.app`, or `--allow-user-writable`). It
checks what runs as every user whose browser starts the relay: the relay, the daemon it starts (the
login item `Downex Agent.app`, else `Helpers/downexd`) and the windows that daemon opens
(`MacOS/Downex`, `Downex Progress.app`), each with every directory above it, also where symbolic links
lead, and where a missing one would be created; and everything in Downex.app (Launch Services reads
the bundles' `Info.plist` when it opens the windows), without following symbolic links
(`register_owner.go`, `ownerRules.checkHost` over `client.LayoutOf`; outside a bundle the relay,
`downexd` and `downex-ui` next to it).

## 5. Window (`ui/`, darwin)
- Wails mac options: standard title bar, follows the system appearance; About panel with version.
- `OpenFile` → `/usr/bin/open <path>`; `OpenFolder` → `/usr/bin/open -R <path>` (reveal in Finder);
  `OpenWith` → unsupported for now (the button is hidden on macOS); executable confirmation as on
  Windows for `.app`, `.command`, `.sh`, `.pkg`, `.dmg`-mounted apps etc. (`.app` directories count as
  executables).
- Progress windows: `OpenProgressWindow(id)` launches `Downex Progress.app` with `--progress <id>`
  through `platform.StartApp` (no extra Dock icons). The single-instance-per-id rule still applies.
- Menu: standard app menu (About, Settings…, Quit) via Wails menu API.
- As built (`ui/dock_darwin.go`/`.m`, cgo, every window process; Wails v2.17.0's delegate is corrected
  on the main thread once it is set, with `class_replaceMethod`):
  - The system's request to quit (logout, restart, shut down, the Dock's Quit) is postponed with
    `NSTerminateLater` while the window shuts down (`App.shutdown`, at most 3 s), then let through with
    `replyToApplicationShouldTerminate:YES`. Wails' own `applicationShouldTerminate:` answers
    `NSTerminateCancel`, which cancels the logout or shut down although the window quits right after.
  - Activation: before opening a progress window, the window (the active app) yields activation to
    Downex.app and the progress app (`yieldActivationToApplicationWithBundleIdentifier:`, macOS 14+,
    bundle identifiers read from the bundles); every window asks with `activate` on macOS 14+ at launch
    and when a second start hands over (Wails uses `activateIgnoringOtherApps:`, whose flag macOS 14
    ignores). A window that is not the active app 0.5 s after it opened or was handed over to (opened in
    the background, or its activation not granted) is put in front of the other apps' windows without
    being activated (`orderFrontRegardless`), so that a confirmation or a progress window never opens
    unseen behind the browser. macOS 13 keeps Wails' behaviour (the windows activate themselves).
    The macOS 14 calls are compiled only with the macOS 14 SDK or newer (Xcode 15+); with an older SDK
    they are left out.

## 6. Build (`scripts/build-macos.sh`, run on a Mac with Xcode CLT, Go 1.27, Node 24 + pnpm, Wails v2)
- `go build` each Go binary for arm64 and amd64 (`CGO_ENABLED=1` for downexd-agent; 0 for the others),
  `lipo -create` → universal; `wails build -platform darwin/universal`; assemble the bundle above from
  templates in `packaging/macos/`; `codesign --force --options runtime -s "${DEVELOPER_ID:--}"` inner
  binaries first, then bundles; `hdiutil create` → `Downex-<version>.dmg`; optional notarization with
  `xcrun notarytool submit --keychain-profile "$NOTARY_PROFILE" --wait` + `xcrun stapler staple` when the
  env vars are set.
- From Windows/Linux only the pure-Go parts can be compile-checked:
  `GOOS=darwin GOARCH=arm64|amd64 CGO_ENABLED=0 go build ./cmd/... ./internal/...` and `go vet`.
- As built (`bash scripts/build-macos.sh [-v version] [-o dir]`, default `dist/macos`): the executables
  are `Contents/MacOS/Downex`, `Contents/Helpers/{downexd,downex,downex-nmhost}`,
  `Contents/Helpers/Downex Progress.app/Contents/MacOS/Downex` (a byte copy of the window) and
  `Contents/Library/LoginItems/Downex Agent.app/Contents/MacOS/downexd-agent`; every bundle has
  `Contents/Resources/AppIcon.icns` (from `ui/build/appicon.png`, `CFBundleIconFile` AppIcon). Templates
  `packaging/macos/{Downex,DownexProgress,DownexAgent}-Info.plist`, `entitlements.plist`, `EXTENSION.txt`,
  `README-DMG.txt` with `@VERSION@`, `@BUNDLE_VERSION@` (leading numbers as x.y.z, `dev` → 0.0.0) and
  `@EXTENSION_ID@`. Only the window binary of Wails' `ui/build/bin/Downex.app` is used. cgo builds (agent,
  window) target `-mmacosx-version-min=13.0`. Signing: `xattr -cr`, then `--force --options runtime
  --entitlements packaging/macos/entitlements.plist` (hardened runtime, no sandbox, no `cs.*`
  exceptions) with `--timestamp` (Developer ID) or `--timestamp=none` (ad-hoc), helpers (`--identifier
  com.downex.<name>`) → Progress.app → Agent.app → Downex.app, then `codesign --verify --deep --strict`.
  DMG (`hdiutil -fs HFS+ -format UDZO`, volume "Downex <version>"): the app, an `/Applications` link and
  `Read Me.txt`; signed with a Developer ID. Notarization notarizes and staples the zipped app first (so a
  copy dragged out of the DMG carries its ticket), then the DMG, and checks both with `spctl`.
- Wails v2.17.0 sets `NSApplicationActivationPolicyRegular` in `applicationWillFinishLaunching`, which
  overrides `LSUIElement`: the progress process must switch itself back to
  `NSApplicationActivationPolicyAccessory` (main thread, after launch) to stay out of the Dock.
