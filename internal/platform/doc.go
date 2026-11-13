// Package platform isolates OS-specific behaviour behind small functions so that the
// engine, the daemon and the CLI stay portable:
//
//   - Preallocate sizes a download's part file, sparse where the file system allows it,
//     so that writes at any offset are cheap and existing data is kept.
//   - StartDetached starts a background process (the daemon) that outlives its caller,
//     with no console window on Windows and its own session on Linux and macOS.
//     StartApp starts a desktop window the same way, shown, and on macOS through
//     Launch Services when it is an application bundle's executable (activated;
//     StartAppInBackground, macOS only, for a window nobody asked for at that moment).
//   - Elevated reports a process running with more rights than its user (an elevated
//     administrator token on Windows, set-uid on Linux, also root on macOS), which the
//     daemon refuses.
//   - DownloadsDir and LogDir locate (and create) the user's well-known folders.
//   - SetLaunchAtLogin writes or removes the sign-in entry (a Run value, an XDG
//     autostart entry, a LaunchAgent).
//   - IsSparse reports whether a file is sparse, for tests and diagnostics.
//
// Windows, Linux and macOS are the supported platforms (platform_windows.go,
// platform_linux.go, platform_darwin.go; macos.go holds the OS-independent parts of the
// macOS code); other systems get portable fallbacks (platform_other.go).
package platform
