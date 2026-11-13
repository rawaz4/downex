// Package tools finds, downloads, verifies, installs and updates the two programs site
// media runs (docs/SITE-MEDIA-SPEC.md §4): yt-dlp, which turns a web page into a list
// of formats, and Deno, which runs yt-dlp's JavaScript challenge solver. Downex never
// bundles either; the user's own daemon downloads them from the publishers' GitHub
// releases on first use. The package also runs them (Exec, Environment).
//
//   - Discovery (discover.go): a copy on PATH first (yt-dlp at most 90 days old, Deno
//     at least 2.3.0), else the managed copy in the tools folder, whose size and SHA-256
//     must match tools.json (an onedir yt-dlp: also every file of its file list).
//   - Install (install.go, release.go, fetch.go): the latest tag from the redirect of
//     releases/latest, every file from that tag, over https only, with size and time
//     limits; yt-dlp and Deno in parallel and independently; one run at a time, owned by
//     the Manager and never by a request; automatic installs back off after failures.
//   - Verification (pgp.go, sums.go, zip.go): yt-dlp's SHA2-256SUMS must carry an
//     OpenPGP signature by the pinned yt-dlp key (checked with the standard library
//     only), no older than the installed copy's; Deno's zip and executable must match
//     their published SHA-256 and, on Windows, deno.exe must carry a trusted
//     Authenticode signature. Nothing downloaded runs before it is verified.
//   - Layout (paths.go, state.go, onedir.go): <tool>/<version>/ folders filled under a
//     temporary name and renamed into place, smoke-tested from there, then recorded
//     atomically in tools.json. yt-dlp is installed from upstream's onedir zip (the
//     executable and its _internal/ folder, which starts without unpacking Python on
//     every run) where the release's signed checksum list names one for the platform,
//     else as the onefile executable; the zip is checked against the list, unpacked
//     with every entry checked (zip.go), and its files recorded in a file list whose
//     SHA-256 tools.json keeps. A onefile copy installed before layouts were recorded is
//     replaced by its release's onedir build at the next update check. Leases keep the
//     folders an extraction runs from; versions that are neither current nor previous
//     are removed when no lease holds them, and a folder of the same version is
//     replaced only when no lease holds it, kept aside until the new copy works.
//     A folder left aside by a stop between the two renames is put back at start.
//   - Updates (update.go): the daily check, channel switches, anti-rollback, the
//     rollback of a version whose first extractions fail, the PATH copy fallback.
//   - Look-aheads (docs/SITE-MEDIA-SPEC.md §5.6): AcquireReady leases only copies that
//     are ready, never installs and never hands out a trial's copy; the resolver reports
//     nothing of a look-ahead no user's request joined.
//   - Processes (exec*.go, env.go): no shell, an allow-listed environment, a Windows job
//     object (no console window, the whole tree killed together) or a Unix process
//     group, time and output limits.
//
// Errors carry short texts users read and never URLs (Error, ErrorCode); Ensure,
// Install, Acquire and AcquireReady wrap the internal/sitemedia sentinels so that the daemon maps them
// to its error codes.
package tools
