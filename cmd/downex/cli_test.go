package main

import (
	"bytes"
	"context"
	"flag"
	"io"
	"log/slog"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"downex/internal/engine"
)

func TestParseRate(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
	}{
		// K, M and G are decimal (1 K = 1,000), with or without "B" and "/s".
		{"0", 0}, {"1", 1}, {"1048576", 1 << 20}, {"500K", 500_000}, {"500k", 500_000},
		{"1M", 1_000_000}, {"1.5M", 1_500_000}, {"2G", 2_000_000_000}, {"1MB/s", 1_000_000},
		{"64kb", 64_000}, {" 2m ", 2_000_000}, {"100B", 100}, {"0.5K", 500}, {"0K", 0},
		{"500KB/s", 500_000}, {"1GB", 1_000_000_000}, {"9.67M", 9_670_000}, {"1000b/s", 1000},
		// KiB, MiB and GiB stay binary for old habits and scripts.
		{"512KiB", 512 << 10}, {"1kib", 1024}, {"1MiB/s", 1 << 20}, {"1.5MiB", 3 << 19},
		{"2GiB", 2 << 30}, {"0.5KiB", 512}, {"630MiB", 660_602_880},
	} {
		got, err := parseRate(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("parseRate(%q) = %d, %v; want %d", tc.in, got, err, tc.want)
		}
	}
	for _, in := range []string{"", "-1", "-1M", "abc", "1X", "M", "NaN", "Inf", "1e300G", "0.0001", "1..5M",
		"1Ki", "1Mi/s", "0.0001K", "MiB", "1KBB", "5iB", "5ib/s", "1.5ib/s", "iB"} {
		if got, err := parseRate(in); err == nil {
			t.Errorf("parseRate(%q) = %d, want an error", in, got)
		}
	}
	if got, err := parseRate("8191P"); err == nil {
		t.Errorf("unknown suffix accepted: %d", got)
	}
	if got, err := parseRate("9223372036G"); err != nil || got != 9_223_372_036_000_000_000 {
		t.Errorf("largest decimal rate: %d %v", got, err)
	}
	if _, err := parseRate("9223372037G"); err == nil {
		t.Error("decimal overflow not detected")
	}
	if got, err := parseRate("8589934591GiB"); err != nil || got != 8589934591<<30 {
		t.Errorf("largest binary rate: %d %v", got, err)
	}
	if _, err := parseRate("8589934592GiB"); err == nil {
		t.Error("binary overflow not detected")
	}
}

// The usage text says how rates are read and that what downex shows is decimal.
func TestUsageRates(t *testing.T) {
	for _, want := range []string{"optional decimal suffixes: 500K (500 KB/s = 500,000 B/s)",
		"KiB, MiB and GiB are binary (512KiB = 524,288 B/s)", "1 KB = 1,000 bytes"} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage does not contain %q", want)
		}
	}
	if strings.Contains(usage, "binary suffixes") {
		t.Error("usage still calls K, M and G binary")
	}
}

func TestParseArgsInterspersed(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		pos     []string
		out     string
		verbose bool
	}{
		{[]string{"URL", "-o", "dir", "-v"}, []string{"URL"}, "dir", true},
		{[]string{"-o", "dir", "URL"}, []string{"URL"}, "dir", false},
		{[]string{"--o=dir", "A", "B", "-v"}, []string{"A", "B"}, "dir", true},
		{[]string{"A", "--", "-o", "x"}, []string{"A", "-o", "x"}, ".", false},
		{nil, nil, ".", false},
	} {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		out := fs.String("o", ".", "")
		verbose := fs.Bool("v", false, "")
		pos, err := parseArgs(fs, tc.args)
		if err != nil || !slices.Equal(pos, tc.pos) || *out != tc.out || *verbose != tc.verbose {
			t.Errorf("parseArgs(%q) = %q, -o %q, -v %v, %v", tc.args, pos, *out, *verbose, err)
		}
	}
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if _, err := parseArgs(fs, []string{"URL", "-nope"}); err == nil {
		t.Error("unknown flag after a positional argument accepted")
	}
}

func TestHeaderFlag(t *testing.T) {
	var h headerFlag
	for _, v := range []string{"Cookie: a=1; b=2", "Referer:https://x.test/p?q=1", "X-Token:  v  ", "x-multi: 1", "X-Multi: 2", "Empty:"} {
		if err := h.Set(v); err != nil {
			t.Fatalf("Set(%q): %v", v, err)
		}
	}
	if h.h.Get("Cookie") != "a=1; b=2" || h.h.Get("Referer") != "https://x.test/p?q=1" ||
		h.h.Get("X-Token") != "v" || len(h.h.Values("X-Multi")) != 2 || len(h.h.Values("Empty")) != 1 {
		t.Errorf("headers = %v", h.h)
	}
	for _, v := range []string{"NoColon", ": v", "Bad Name: v", "Bad\x00: v", "N: a\r\nInjected: 1", "N: \x7f", "Ünï: v"} {
		if err := h.Set(v); err == nil {
			t.Errorf("Set(%q) accepted", v)
		}
	}
}

func TestFormatting(t *testing.T) {
	for n, want := range map[int64]string{-1: "?", 0: "0 B", 999: "999 B", 1000: "1.0 KB", 1023: "1.0 KB",
		1024: "1.0 KB", 1500: "1.5 KB", 1536: "1.5 KB", 999_949: "999.9 KB", 999_951: "1.0 MB", 999_999: "1.0 MB",
		1_000_000: "1.0 MB", 5 << 20: "5.2 MB", 999_951_000: "1.0 GB", 3 << 30: "3.2 GB",
		1_429_658_896: "1.4 GB", 660_602_880: "660.6 MB", 10_139_730: "10.1 MB", 1e12: "1.0 TB",
		1 << 40: "1.1 TB", 999_951_000_000_000: "1.0 PB", 999_949_000_000_000_000: "999.9 PB",
		999_951_000_000_000_000: "1.0 EB", 999_999_000_000_000_000: "1.0 EB", math.MaxInt64: "9.2 EB"} {
		if got := formatBytes(n); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", n, got, want)
		}
	}
	for _, tc := range []struct {
		rem   int64
		speed float64
		want  string
	}{
		{0, 0, "0s"}, {100, 0, "--"}, {100, 0.5, "--"}, {1000, 100, "10s"}, {1001, 100, "11s"},
		{6000, 100, "1m00s"}, {245 << 20, 1 << 20, "4m05s"}, {3720, 1, "1h02m"}, {1 << 50, 1, "--"},
	} {
		if got := formatETA(tc.rem, tc.speed); got != tc.want {
			t.Errorf("formatETA(%d, %v) = %q, want %q", tc.rem, tc.speed, got, tc.want)
		}
	}
	for d, want := range map[time.Duration]string{850 * time.Millisecond: "850ms", 3200 * time.Millisecond: "3.2s",
		42 * time.Second: "42s", 245 * time.Second: "4m05s", 62 * time.Minute: "1h02m"} {
		if got := formatDuration(d); got != want {
			t.Errorf("formatDuration(%v) = %q, want %q", d, got, want)
		}
	}
	if formatRate(0) != "unlimited" || formatRate(512<<10) != "524.3 KB/s" ||
		formatRate(10_139_730) != "10.1 MB/s" || formatRate(999) != "999 B/s" || formatRate(999_999) != "1.0 MB/s" {
		t.Error("formatRate")
	}
	p := engine.Progress{Size: 100 << 20, Received: 25 << 20, SpeedBps: 5 << 20, Connections: 8}
	if got, want := progressLine(p), " 25.0%  26.2 MB / 104.9 MB  5.2 MB/s  8 conn  ETA 15s"; got != want {
		t.Errorf("progressLine = %q, want %q", got, want)
	}
	p = engine.Progress{Size: -1, Received: 3 << 10, SpeedBps: 0, Connections: 1}
	if got, want := progressLine(p), "3.1 KB  0 B/s  1 conn"; got != want {
		t.Errorf("progressLine (unknown size) = %q, want %q", got, want)
	}
	if got := progressLine(engine.Progress{Size: 0}); !strings.HasPrefix(got, "100.0%") {
		t.Errorf("empty file progress = %q", got)
	}
	if len(progressLine(engine.Progress{Size: math.MaxInt64, Received: math.MaxInt64 / 3, SpeedBps: 1e12, Connections: 32})) >= 79 {
		t.Error("progress line may wrap on an 80-column terminal")
	}
}

func TestConsoleRedrawsOneLine(t *testing.T) {
	var buf bytes.Buffer
	c := &console{w: &buf, live: true}
	c.status("12345")
	c.status("123")
	c.printf("note %d\n", 1)
	c.status("ab")
	c.keep()
	c.keep()
	want := "\r12345" + "\r123  " + "\r   \r" + "note 1\n" + "\rab" + "\n"
	if got := buf.String(); got != want {
		t.Errorf("console output %q, want %q", got, want)
	}

	buf.Reset()
	quiet := &console{w: &buf}
	quiet.status("hidden")
	quiet.printf("shown\n")
	quiet.keep()
	if got := buf.String(); got != "shown\n" {
		t.Errorf("non-terminal console output %q", got)
	}
	if isTerminal(&buf) {
		t.Error("a buffer is not a terminal")
	}
}

func TestConsoleHandler(t *testing.T) {
	var buf bytes.Buffer
	con := &console{w: &buf}
	log := slog.New(newConsoleHandler(con, slog.LevelWarn)).With("download", 3)
	log.Debug("hidden")
	log.Info("hidden too")
	log.Warn("connection stalled", "idle", 20*time.Second, "url", "http://x.test/a b")
	log.WithGroup("g").Error("failed", "err", "boom", slog.Group("sub", "k", ""))
	want := "warning: connection stalled (download=3 idle=20s url=\"http://x.test/a b\")\n" +
		"error: failed (download=3 g.err=boom g.sub.k=\"\")\n"
	if got := buf.String(); got != want {
		t.Errorf("log output:\n%q\nwant\n%q", got, want)
	}
}

func TestProgressViewCoalesces(t *testing.T) {
	var buf bytes.Buffer
	con := &console{w: &buf, live: true}
	v := newProgressView(con, nil, 1, true)
	for i := range 1000 {
		v.update(engine.Progress{Size: 1000, Received: int64(i + 1), Connections: 1})
	}
	last, seen := v.stop()
	if !seen || last.Received != 1000 {
		t.Fatalf("last event = %+v (seen %v), want the final one", last, seen)
	}
	if n := strings.Count(buf.String(), "\r"); n > 3 {
		t.Errorf("%d redraws for a burst of events, want at most a few", n)
	}
	if !strings.HasSuffix(buf.String(), "\n") || !strings.Contains(buf.String(), "100.0%") {
		t.Errorf("final line not kept: %q", buf.String())
	}
}

func TestCommandLineErrors(t *testing.T) {
	ctx := context.Background()
	db := t.TempDir() + "/x.db"
	for _, tc := range []struct {
		args []string
		code int
		out  string // expected in stdout+stderr
	}{
		{nil, exitError, "Usage:"},
		{[]string{"help"}, exitOK, "downex get <url>"},
		{[]string{"--help"}, exitOK, "Exit status"},
		{[]string{"help", "get"}, exitOK, "-limit"},
		{[]string{"get", "-h"}, exitOK, "-restart"},
		{[]string{"frobnicate"}, exitError, `unknown command "frobnicate"`},
		{[]string{"--version"}, exitOK, "downex dev\n"},
		{[]string{"-version"}, exitOK, "downex dev\n"},
		{[]string{"version"}, exitOK, "downex dev\n"},
		{[]string{"help"}, exitOK, "downex --version"},
		{[]string{"get", "--db", db}, exitError, "expects exactly one URL"},
		{[]string{"get", "http://x.test/", "--limit", "fast", "--db", db}, exitError, "invalid rate"},
		{[]string{"get", "http://x.test/", "-c", "33", "--db", db}, exitError, "-c must be between 1 and 32"},
		{[]string{"get", "http://x.test/", "-H", "bad", "--db", db}, exitError, `want "Name: value"`},
		{[]string{"get", "ftp://x.test/f", "--db", db}, exitError, "unsupported URL"},
		{[]string{"get", "http://x.test/", "--proxy", "gopher://p:1", "--db", db}, exitError, "proxy"},
		{[]string{"resume", "abc", "--db", db}, exitError, `invalid download id "abc"`},
		{[]string{"resume", "--db", db}, exitError, "expects exactly one download id"},
		{[]string{"resume", "7", "--local", "--db", db}, exitError, "no download with id 7"},
		{[]string{"info", "7", "--db", db}, exitError, "no download with id 7"},
		{[]string{"rm", "7", "--local", "--db", db}, exitError, "no download with id 7"},
		{[]string{"ls", "extra", "--db", db}, exitError, "takes no arguments"},
		{[]string{"ls", "--db", db}, exitOK, "No downloads."},
		{[]string{"ls", "--json", "--db", db}, exitOK, "[]"},
	} {
		r := runCLI(ctx, tc.args...)
		if r.code != tc.code || !strings.Contains(r.stdout+r.stderr, tc.out) {
			t.Errorf("downex %q: %v\nwant exit %d and %q", tc.args, r, tc.code, tc.out)
		}
	}
}

func TestResumeCommandQuoting(t *testing.T) {
	if got := resumeCommand(4, ""); got != "downex resume 4" {
		t.Errorf("resumeCommand = %q", got)
	}
	got := resumeCommand(4, "/tmp/my db/x.db", "--restart")
	if !strings.HasPrefix(got, "downex resume 4 --restart --db \"") || !strings.HasSuffix(got, "x.db\"") {
		t.Errorf("resumeCommand with spaces = %q", got)
	}
	if shellQuote("plain/path.db") != "plain/path.db" || shellQuote("") != `""` || shellQuote(`a"b`) != `"a\"b"` {
		t.Error("shellQuote")
	}
}
