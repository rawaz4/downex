package about

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
)

// The cases of ui/frontend/src/lib/about.test.ts.

func TestOSLabel(t *testing.T) {
	for _, c := range [][3]string{
		{"windows", "amd64", "Windows x64"},
		{"windows", "arm64", "Windows on Arm"},
		{"darwin", "arm64", "macOS (Apple silicon)"},
		{"darwin", "amd64", "macOS (Intel)"},
		{"linux", "amd64", "Linux x64"},
		{"plan9", "mips", "plan9/mips"},
	} {
		if got := OSLabel(c[0], c[1]); got != c[2] {
			t.Errorf("OSLabel(%s, %s) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

func TestBuildLines(t *testing.T) {
	build := Build{Version: "0.8.2", OS: "windows", Arch: "amd64", GoVersion: "go1.27.0", WailsVersion: "v2.17.0"}
	if got := BuildLines(build); !slices.Equal(got, []string{"Go 1.27.0", "Wails 2.17.0"}) {
		t.Errorf("lines = %q", got)
	}
	noWails := build
	noWails.WailsVersion = ""
	if got := BuildLines(noWails); !slices.Equal(got, []string{"Go 1.27.0"}) {
		t.Errorf("without Wails = %q", got)
	}
	committed := build
	committed.Commit, committed.Modified = "0123456789abcdef0123", true
	if got := BuildLines(committed); !slices.Equal(got, []string{"Go 1.27.0", "Wails 2.17.0", "Commit 0123456789ab (with uncommitted changes)"}) {
		t.Errorf("with a commit = %q", got)
	}
}

func TestVersionsDiffer(t *testing.T) {
	for _, c := range []struct {
		w, d string
		want bool
	}{
		{"0.8.2", "0.8.1", true},
		{"0.8.2", "0.8.2", false},
		{"0.8.2", "v0.8.2", false},
		// nothing to compare with
		{"", "0.8.1", false},
		{"0.8.2", "", false},
	} {
		if got := VersionsDiffer(c.w, c.d); got != c.want {
			t.Errorf("VersionsDiffer(%q, %q) = %v", c.w, c.d, got)
		}
	}
}

func TestUpdatesState(t *testing.T) {
	supported := &downexv1.StatusResponse{UpdatesSupported: true}
	unsupported := &downexv1.StatusResponse{UpdatesSupported: false}
	const feed = "https://u.example/update.json"
	for _, c := range []struct {
		name   string
		status *downexv1.StatusResponse
		feed   string
		auto   bool
		want   Updates
	}{
		{"unreachable", nil, "", true, Updates{false, "Not known while downexd is not reachable"}},
		{"no signing key", unsupported, feed, true, Updates{false, "Off in this build (no signing key)"}},
		// about.test.ts: "an older downexd does not say: the address decides" ({} → no address).
		// downex.v1 cannot leave the bool out; an older downexd reads as unsupported, as
		// wails.ts bool() reads it today.
		{"older downexd", &downexv1.StatusResponse{}, "", true, Updates{false, "Off in this build (no signing key)"}},
		{"no address", supported, "", true, Updates{false, "Off (no update address)"}},
		{"every day", supported, feed, true, Updates{true, "On · checked every day"}},
		{"when asked", supported, feed, false, Updates{true, "On · checked when you ask"}},
	} {
		if got := UpdatesState(c.status, c.feed, c.auto); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestScrubDiagnostics(t *testing.T) {
	text := "downexd 0.8.2\nEndpoint: \\\\.\\pipe\\downex-9f3a\nUpdate: https://u.example/update.json\nOK\n"
	out := ScrubDiagnostics(text, []string{`\\.\pipe\downex-9f3a`, "https://u.example/update.json", ""})
	for _, gone := range []string{"downex-9f3a", "u.example"} {
		if strings.Contains(out, gone) {
			t.Errorf("%q still in %q", gone, out)
		}
	}
	for _, kept := range []string{"Endpoint: (hidden)", "downexd 0.8.2"} {
		if !strings.Contains(out, kept) {
			t.Errorf("%q not in %q", kept, out)
		}
	}
}

const notices = "THIRD-PARTY NOTICES\n" +
	"\n" +
	"Downex is built with software that other people wrote.\n" +
	"\n" +
	"==============================================================================\n" +
	"The Go Programming Language (standard library and runtime, go1.27.0)\n" +
	"==============================================================================\n" +
	"--- LICENSE ---\n" +
	"Copyright 2009 The Go Authors.\n" +
	"\n" +
	"==============================================================================\n" +
	"github.com/wailsapp/wails/v2 v2.17.0\n" +
	"==============================================================================\n" +
	"MIT License\n"

func TestParseNotices(t *testing.T) {
	n := ParseNotices(notices)
	if n.Intro != "THIRD-PARTY NOTICES\n\nDownex is built with software that other people wrote." {
		t.Errorf("intro = %q", n.Intro)
	}
	want := []NoticeSection{
		{"The Go Programming Language (standard library and runtime, go1.27.0)", "--- LICENSE ---\nCopyright 2009 The Go Authors."},
		{"github.com/wailsapp/wails/v2 v2.17.0", "MIT License"},
	}
	if !reflect.DeepEqual(n.Sections, want) {
		t.Errorf("sections = %q", n.Sections)
	}
	// keeps a file without sections whole, and reads Windows line ends
	if got := ParseNotices("Just text\r\nmore\r\n"); got.Intro != "Just text\nmore" || len(got.Sections) != 0 {
		t.Errorf("plain = %+v", got)
	}
}

func TestIntroBlocks(t *testing.T) {
	intro := strings.Join([]string{
		"THIRD-PARTY NOTICES",
		"",
		"Downex is built with software that other",
		"people wrote.",
		"",
		"Programs Downex downloads separately",
		"------------------------------------",
		"Downex does not include these programs.",
		"",
		"- yt-dlp (https://github.com/yt-dlp/yt-dlp): released into the public domain under",
		"  The Unlicense.",
		"- Deno: MIT License.",
	}, "\n")
	want := []NoticeBlock{
		{Para, "Downex is built with software that other people wrote."},
		{Heading, "Programs Downex downloads separately"},
		{Para, "Downex does not include these programs."},
		{Item, "yt-dlp (https://github.com/yt-dlp/yt-dlp): released into the public domain under The Unlicense."},
		{Item, "Deno: MIT License."},
	}
	if got := IntroBlocks(intro); !reflect.DeepEqual(got, want) {
		t.Errorf("blocks = %+v", got)
	}
	if got := IntroBlocks(""); len(got) != 0 {
		t.Errorf("empty = %+v", got)
	}
}
