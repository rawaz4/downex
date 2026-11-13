package tools

import (
	"path/filepath"
	"runtime"
)

// yt-dlp release channels (Settings.tools_channel).
const (
	ChannelStable  = "stable"
	ChannelNightly = "nightly"
	// DefaultChannel is upstream's recommendation for regular users (§9).
	DefaultChannel = ChannelNightly
)

// NormalizeChannel returns channel, or DefaultChannel for "" and unknown values.
func NormalizeChannel(channel string) string {
	if channel == ChannelStable {
		return ChannelStable
	}
	return ChannelNightly
}

// GitHub repositories of the releases (docs/SITE-MEDIA-SPEC.md §4.2). Both yt-dlp
// repositories are signed with the same key.
const (
	repoYtDlpStable  = "yt-dlp/yt-dlp"
	repoYtDlpNightly = "yt-dlp/yt-dlp-nightly-builds"
	repoDeno         = "denoland/deno"
)

// ytdlpRepo is the repository of a yt-dlp channel.
func ytdlpRepo(channel string) string {
	if NormalizeChannel(channel) == ChannelStable {
		return repoYtDlpStable
	}
	return repoYtDlpNightly
}

// Files of a yt-dlp release besides the executables.
const (
	ytdlpSums    = "SHA2-256SUMS"
	ytdlpSumsSig = "SHA2-256SUMS.sig"
)

// Layouts of a managed yt-dlp (tools.json "layout"). Upstream builds yt-dlp with
// PyInstaller twice: as one self-extracting executable, which unpacks Python into a
// temporary folder on every start (about a second on Windows), and as a zip of the
// unpacked program ("onedir": the executable and its _internal/ folder), which starts at
// once. The zip is preferred wherever the release's signed SHA2-256SUMS lists one.
const (
	layoutOnefile = "onefile"
	layoutOnedir  = "onedir"
)

// platformAssets names the release files of one platform.
type platformAssets struct {
	YtDlp string // yt-dlp's onefile executable asset ("" = none)
	// YtDlpZip is yt-dlp's onedir zip asset ("" = none); YtDlpZipExe is the name of the
	// executable at the zip's root (upstream names it like the onefile asset).
	YtDlpZip    string
	YtDlpZipExe string
	Deno        string // Deno zip asset; "" = no build (the tool is DISABLED)
	DenoTarget  string // Deno's target triple, e.g. "x86_64-pc-windows-msvc"
}

// hasYtDlp reports whether upstream publishes a yt-dlp build for the platform.
func (a platformAssets) hasYtDlp() bool { return a.YtDlp != "" || a.YtDlpZip != "" }

// AssetNames returns the release assets of yt-dlp (its onefile executable) and Deno for
// goos/goarch ("" when upstream publishes no build); musl selects the musl builds on
// Linux, where Deno has none (docs/SITE-MEDIA-SPEC.md §4.2). The manager prefers yt-dlp's
// onedir zip where a release lists one (assetsFor) and falls back to this executable.
func AssetNames(goos, goarch string, musl bool) (ytdlp, deno string) {
	a := assetsFor(goos, goarch, musl)
	return a.YtDlp, a.Deno
}

// assetsFor returns the release files of goos/goarch. The yt-dlp names are those of
// upstream's SHA2-256SUMS (testdata/ytdlp-2026.08.19): every platform with a build has
// an onedir zip, whose root executable has the onefile asset's name (linux/arm: the
// 2026.08.19 release lists only the zip).
func assetsFor(goos, goarch string, musl bool) platformAssets {
	var a platformAssets
	switch goos + "/" + goarch {
	case "windows/amd64":
		a = platformAssets{YtDlp: "yt-dlp.exe", YtDlpZip: "yt-dlp_win.zip", DenoTarget: "x86_64-pc-windows-msvc"}
	case "windows/arm64":
		a = platformAssets{YtDlp: "yt-dlp_arm64.exe", YtDlpZip: "yt-dlp_win_arm64.zip", DenoTarget: "aarch64-pc-windows-msvc"}
	case "windows/386":
		a = platformAssets{YtDlp: "yt-dlp_x86.exe", YtDlpZip: "yt-dlp_win_x86.zip"}
	case "darwin/amd64":
		a = platformAssets{YtDlp: "yt-dlp_macos", YtDlpZip: "yt-dlp_macos.zip", DenoTarget: "x86_64-apple-darwin"}
	case "darwin/arm64":
		a = platformAssets{YtDlp: "yt-dlp_macos", YtDlpZip: "yt-dlp_macos.zip", DenoTarget: "aarch64-apple-darwin"}
	case "linux/amd64":
		a = platformAssets{YtDlp: "yt-dlp_linux", YtDlpZip: "yt-dlp_linux.zip", DenoTarget: "x86_64-unknown-linux-gnu"}
		if musl {
			a = platformAssets{YtDlp: "yt-dlp_musllinux", YtDlpZip: "yt-dlp_musllinux.zip"}
		}
	case "linux/arm64":
		a = platformAssets{YtDlp: "yt-dlp_linux_aarch64", YtDlpZip: "yt-dlp_linux_aarch64.zip", DenoTarget: "aarch64-unknown-linux-gnu"}
		if musl {
			a = platformAssets{YtDlp: "yt-dlp_musllinux_aarch64", YtDlpZip: "yt-dlp_musllinux_aarch64.zip"}
		}
	case "linux/arm":
		if !musl {
			a = platformAssets{YtDlp: "yt-dlp_linux_armv7l", YtDlpZip: "yt-dlp_linux_armv7l.zip"}
		}
	}
	if a.YtDlpZip != "" {
		a.YtDlpZipExe = a.YtDlp
	}
	if a.DenoTarget != "" {
		a.Deno = "deno-" + a.DenoTarget + ".zip"
	}
	return a
}

// denoExeSums is the optional checksum file of the unpacked executable of a Deno target.
func denoExeSums(target string) string { return "deno-" + target + ".sha256sum" }

// detectMusl reports a musl C library (its dynamic loader /lib/ld-musl-*.so.1) on Linux.
func detectMusl() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	m, _ := filepath.Glob("/lib/ld-musl-*.so.1")
	return len(m) > 0
}
