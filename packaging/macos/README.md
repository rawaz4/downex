# macOS packaging

Templates that `scripts/build-macos.sh` assembles `Downex.app` and its disk image from
(docs/MACOS-SPEC.md §1 and §6; how to build and install: the
[macOS section of the usage guide](../../docs/USAGE.md#macos)).

| File | Becomes |
|---|---|
| `Downex-Info.plist` | `Downex.app/Contents/Info.plist` — `com.downex.app`, executable `Downex` (the window) |
| `DownexProgress-Info.plist` | `Contents/Helpers/Downex Progress.app/Contents/Info.plist` — `com.downex.progress`, executable `Downex` (a copy of the window binary), `LSUIElement` |
| `DownexAgent-Info.plist` | `Contents/Library/LoginItems/Downex Agent.app/Contents/Info.plist` — `com.downex.agent`, executable `downexd-agent` (downexd built with cgo), `LSUIElement` |
| `entitlements.plist` | the entitlements of every signed binary and bundle (`codesign --entitlements`) |
| `EXTENSION.txt` | `Contents/Resources/EXTENSION.txt` — loading the browser extension on a Mac |
| `README-DMG.txt` | `Read Me.txt` at the top of the disk image — install steps |

All bundles use `LSMinimumSystemVersion` 13.0 and the icon `Contents/Resources/AppIcon.icns`,
which the script makes from `ui/build/appicon.png`. Wails' own `ui/build/darwin/Info.plist`
(created by `wails build` when it is missing) is not used: the script takes only the window
binary from `ui/build/bin/Downex.app`.

**Placeholders.** The script replaces exactly these, and stops when a template holds another
`@NAME@`:

- `@VERSION@` — the `-v` version as given (`0.4.0-beta.1`, default `dev`);
- `@BUNDLE_VERSION@` — its leading numbers as `x.y.z` (`0.4.0`; `dev` → `0.0.0`), because
  `CFBundleShortVersionString` and `CFBundleVersion` must be period-separated integers;
- `@EXTENSION_ID@` — the first Chromium extension ID in `ext/EXTENSION_ID.txt` (one per line, `#`
  comments), the ID of the extension folder in the app; `downex-nmhost` allows all of them.

**Entitlements.** Hardened runtime (`codesign --options runtime`, required for notarization)
without any `com.apple.security.cs.*` exception: Go, AppKit and WKWebView (whose JavaScript JIT
runs in WebKit's own processes) need none. No App Sandbox (`app-sandbox` false): Downex writes
anywhere the user chooses, starts its helpers and reaches its daemon over a Unix socket.
`network.client` takes effect only inside the sandbox and is there for the day the app is
sandboxed; `network.server` is not needed, because nothing listens on a network port. The file
has no XML comments: the kernel's entitlements parser ends a `<!` declaration at the first `>`.

**Tests.** `go test ./packaging/macos` (any OS) parses every plist strictly, checks the keys
above, and runs the script's functions in bash with stand-ins for the macOS tools: version and
template rendering, the bundle layout of `assemble` file by file, and the inside-out order and
flags of `sign_app`. The tests that need bash are skipped without one (on Windows they use
Git for Windows' bash, or `$DOWNEX_TEST_BASH`). `layout_test.go` ties the bundle to the paths the
programs look for at run time (`internal/client`'s `Layout`: window, progress app, login item and
helpers), so that the templates and the code cannot drift apart.
