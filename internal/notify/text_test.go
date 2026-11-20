package notify

import (
	"strings"
	"testing"
	"unicode/utf8"

	downexv1 "downex/internal/rpc/downex/v1"
)

func TestClean(t *testing.T) {
	for in, want := range map[string]string{
		"report.pdf":                        "report.pdf",
		"  spaced   name .zip ":             "spaced name .zip",
		"evil\u202Egnp.exe":                 "evilgnp.exe", // right-to-left override
		"a\u2066b\u2067c\u2068d\u2069e.txt": "abcde.txt",   // isolates
		"x\u200Ey\u200Fz\u061C.bin":         "xyz.bin",     // marks
		"zero\u200Bwidth\uFEFF.iso":         "zerowidth.iso",
		"line\nbreak\r\ntab\there":          "line break tab here",
		"para\u2028graph\u2029end":          "para graph end",
		"bell\a\x00\x1b[31mred":             "bell[31mred",
		"c1\u0085next\u009bx":               "c1 nextx",
		"bad\xffutf8\xc3":                   "badutf8",
		"日本語の資料.pdf":                        "日本語の資料.pdf",
		"émoji 🎉.png":                       "émoji 🎉.png",
		"\u202E\u202E":                      "",
	} {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDisplayName(t *testing.T) {
	long := strings.Repeat("a", 300) + ".zip"
	for _, c := range []struct {
		d    *downexv1.Download
		want string
	}{
		{&downexv1.Download{Id: 1, Filename: "file.zip", Url: "https://h/other.bin"}, "file.zip"},
		{&downexv1.Download{Id: 2, Filename: "\u202E", Url: "https://h/dir/from%20url.bin?token=SECRET#frag"}, "from url.bin"},
		{&downexv1.Download{Id: 3, Url: "https://example.org/?token=SECRET"}, "example.org"},
		{&downexv1.Download{Id: 4, Url: "https://example.org"}, "example.org"},
		{&downexv1.Download{Id: 5, Url: "::not a url"}, "Download 5"},
		{&downexv1.Download{Id: 6}, "Download 6"},
	} {
		if got := DisplayName(c.d); got != c.want {
			t.Errorf("DisplayName(%v) = %q, want %q", c.d, got, c.want)
		}
	}
	got := DisplayName(&downexv1.Download{Filename: long})
	if utf8.RuneCountInString(got) != maxNameRunes || !strings.HasSuffix(got, "…") {
		t.Errorf("long name = %q (%d runes)", got, utf8.RuneCountInString(got))
	}
}

func TestShortError(t *testing.T) {
	for _, c := range []struct{ code, msg, want string }{
		{"disk_full", "write: There is not enough space on the disk.", "The disk is full."},
		{"tls", "x509: certificate signed by unknown authority", "The secure connection failed."},
		{"network", `Get "https://h/p?token=SECRET": dial tcp: timeout`, "A network error interrupted the download."},
		{"validator_changed", "remote file changed", "The file changed on the server; restart the download."},
		{"stalled", "", "The transfer stalled."},
		{"invalid_request", "", "The request could not be sent."},
		{"http_status", "probe: unexpected HTTP status 404 Not Found", "The server answered HTTP 404 Not Found."},
		{"http_status", "unexpected HTTP status 599 ", "The server answered HTTP 599."},
		{"http_status", "range ignored", "The server's response could not be used."},
		{"internal", "open C:\\dl\\x.dxpart: Access is denied.\nmore detail", "open C:\\dl\\x.dxpart: Access is denied."},
		{"", `fetch "https://user:pw@cdn.example.com/a/b?sig=SECRET" failed`, `fetch "https://cdn.example.com" failed`},
		{"future_code", "\u202Ecaution\x07", "caution"},
		{"", "", "Unknown error."},
	} {
		if got := ShortError(c.code, c.msg); got != c.want {
			t.Errorf("ShortError(%q, %q) = %q, want %q", c.code, c.msg, got, c.want)
		}
	}
	if got := ShortError("", strings.Repeat("x", 500)); utf8.RuneCountInString(got) != maxErrorRunes {
		t.Errorf("long error has %d runes", utf8.RuneCountInString(got))
	}
}

func TestTooltipAndFormatBytes(t *testing.T) {
	if got, want := Tooltip(Summary{}), "Downex — 0 active, 0 B/s"; got != want {
		t.Errorf("idle tooltip = %q, want %q", got, want)
	}
	if got, want := Tooltip(Summary{Active: 2, Speed: 2_400_000, PoliteSlowed: true}), "Downex — 2 active, 2.4 MB/s · Polite: slowed"; got != want {
		t.Errorf("Tooltip while Polite mode slows = %q, want %q", got, want)
	}
	if got, want := Tooltip(Summary{Active: 2, Speed: 1_500_000}), "Downex — 2 active, 1.5 MB/s"; got != want {
		t.Errorf("tooltip = %q, want %q", got, want)
	}
	for n, want := range map[float64]string{
		// Decimal units (1 KB = 1000 B); a value that would round to 1000.0 of a unit is
		// shown in the next one.
		-5: "0 B", 0: "0 B", 512: "512 B", 999: "999 B", 999.4: "999 B", 999.6: "1.0 KB", 1000: "1.0 KB",
		1023.6: "1.0 KB", 1536: "1.5 KB", 999_949: "999.9 KB", 999_951: "1.0 MB", 999_999: "1.0 MB",
		5 << 20: "5.2 MB", 10_139_730: "10.1 MB", 660_602_880: "660.6 MB", 1_429_658_896: "1.4 GB",
		3 << 30: "3.2 GB", 1e12: "1.0 TB", 999_951e9: "1.0 PB", 1 << 50: "1.1 PB", 999_949e12: "999.9 PB",
		999_951e12: "1.0 EB", 1 << 60: "1.2 EB",
	} {
		if got := FormatBytes(n); got != want {
			t.Errorf("FormatBytes(%v) = %q, want %q", n, got, want)
		}
	}
}
