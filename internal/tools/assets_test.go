package tools

import "testing"

func TestAssetNames(t *testing.T) {
	cases := []struct {
		goos, goarch string
		musl         bool
		ytdlp, deno  string
	}{
		{"windows", "amd64", false, "yt-dlp.exe", "deno-x86_64-pc-windows-msvc.zip"},
		{"windows", "arm64", false, "yt-dlp_arm64.exe", "deno-aarch64-pc-windows-msvc.zip"},
		{"windows", "386", false, "yt-dlp_x86.exe", ""},
		{"darwin", "amd64", false, "yt-dlp_macos", "deno-x86_64-apple-darwin.zip"},
		{"darwin", "arm64", false, "yt-dlp_macos", "deno-aarch64-apple-darwin.zip"},
		{"linux", "amd64", false, "yt-dlp_linux", "deno-x86_64-unknown-linux-gnu.zip"},
		{"linux", "amd64", true, "yt-dlp_musllinux", ""},
		{"linux", "arm64", false, "yt-dlp_linux_aarch64", "deno-aarch64-unknown-linux-gnu.zip"},
		{"linux", "arm64", true, "yt-dlp_musllinux_aarch64", ""},
		{"linux", "arm", false, "yt-dlp_linux_armv7l", ""},
		{"linux", "arm", true, "", ""},
		{"linux", "riscv64", false, "", ""},
		{"freebsd", "amd64", false, "", ""},
		{"windows", "arm", false, "", ""},
	}
	for _, c := range cases {
		y, d := AssetNames(c.goos, c.goarch, c.musl)
		if y != c.ytdlp || d != c.deno {
			t.Errorf("AssetNames(%s, %s, musl=%v) = %q, %q, want %q, %q", c.goos, c.goarch, c.musl, y, d, c.ytdlp, c.deno)
		}
	}
	if a := assetsFor("windows", "amd64", false); denoExeSums(a.DenoTarget) != "deno-x86_64-pc-windows-msvc.sha256sum" {
		t.Errorf("exe sums name = %s", denoExeSums(a.DenoTarget))
	}
}

func TestChannels(t *testing.T) {
	for in, want := range map[string]string{"": ChannelNightly, "nightly": ChannelNightly, "stable": ChannelStable, "Stable": ChannelNightly, "master": ChannelNightly} {
		if got := NormalizeChannel(in); got != want {
			t.Errorf("NormalizeChannel(%q) = %q, want %q", in, got, want)
		}
	}
	if ytdlpRepo(ChannelStable) != "yt-dlp/yt-dlp" || ytdlpRepo(ChannelNightly) != "yt-dlp/yt-dlp-nightly-builds" || ytdlpRepo("") != "yt-dlp/yt-dlp-nightly-builds" {
		t.Error("ytdlpRepo maps the channels wrongly")
	}
}
