package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"downex/internal/sitemedia"
)

// TestDiscoverPathCopies: the stand-in on PATH serves both tools; the release server
// is never asked.
func TestDiscoverPathCopies(t *testing.T) {
	e := newTestEnv(t)
	bin := t.TempDir()
	yt := standInAt(t, filepath.Join(bin, "2026.10.20"), "yt-dlp")
	dn := standInAt(t, filepath.Join(bin, "2.5.1"), "deno")
	e.setPath(ToolYtDlp, yt)
	e.setPath(ToolDeno, dn)
	if err := e.ensure(t); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if n := e.rs.total(); n != 0 {
		t.Errorf("the release server got %d requests", n)
	}
	s := e.m.Status()
	if !s.Ready || !s.Enabled {
		t.Errorf("status %+v", s)
	}
	for _, in := range s.Tools {
		if in.State != StateReady || in.Managed || in.Verified != "" {
			t.Errorf("%s: %+v", in.Name, in)
		}
	}
	if in := e.info(ToolYtDlp); in.Version != "2026.10.20" || !samePath(in.Path, yt) {
		t.Errorf("yt-dlp %+v", in)
	}
	if in := e.info(ToolDeno); in.Version != "2.5.1" || !samePath(in.Path, dn) {
		t.Errorf("deno %+v", in)
	}
	l, err := e.m.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if !samePath(l.YtDlp, yt) || !samePath(l.Deno, dn) || l.YtDlpManaged || l.YtDlpVersion != "2026.10.20" ||
		l.WorkRoot != filepath.Join(e.dir, "run") || l.YtDlpCache != filepath.Join(e.dir, "cache", "yt-dlp") || l.DenoCache != filepath.Join(e.dir, "cache", "deno") {
		t.Errorf("lease %+v", l)
	}
}

// TestDiscoverRefusesPathCopies: stale, too old, non-executable and in-folder copies are
// not used; then a manager without downloads has nothing.
func TestDiscoverRefusesPathCopies(t *testing.T) {
	e := newTestEnv(t, func(c *Config, _ *hooks) { c.Client = nil })
	bin := t.TempDir()
	cases := []struct {
		name     string
		ytdlp    string
		deno     string
		ytdlpBad bool
	}{
		{"older than 90 days", standInAt(t, filepath.Join(bin, "2026.07.01"), "yt-dlp"), "", true},
		{"not a version", standInAt(t, filepath.Join(bin, "latest"), "yt-dlp"), "", true},
		{"deno below 2.3.0", "", standInAt(t, filepath.Join(bin, "2.2.9"), "deno"), false},
		{"inside the tools folder", standInAt(t, filepath.Join(e.dir, "yt-dlp", "2026.10.20"), "yt-dlp"), "", true},
	}
	if runtime.GOOS == "windows" {
		cmd := filepath.Join(bin, "yt-dlp.cmd")
		_ = os.WriteFile(cmd, []byte("@echo 2026.10.20\r\n"), 0o755)
		cases = append(cases, struct {
			name     string
			ytdlp    string
			deno     string
			ytdlpBad bool
		}{"a .cmd script", cmd, "", true})
	}
	for _, c := range cases {
		e.setPath(ToolYtDlp, c.ytdlp)
		e.setPath(ToolDeno, c.deno)
		if err := e.m.rediscover(context.Background()); err != nil {
			t.Fatal(err)
		}
		if in := e.info(ToolYtDlp); in.State == StateReady {
			t.Errorf("%s: yt-dlp %+v", c.name, in)
		}
		if in := e.info(ToolDeno); in.State == StateReady {
			t.Errorf("%s: deno %+v", c.name, in)
		}
	}
	err := e.ensure(t)
	if !errors.Is(err, sitemedia.ErrToolsUnavailable) || !errors.Is(err, ErrNoDownloads) {
		t.Fatalf("Ensure without downloads: %v", err)
	}
	if want := "yt-dlp and Deno are not set up: this Downex daemon does not download tools"; err.Error() != want {
		t.Errorf("text %q, want %q", err, want)
	}
	if _, err := e.m.Install(context.Background(), false); !errors.Is(err, ErrNoDownloads) {
		t.Errorf("Install without downloads: %v", err)
	}
}

// TestPathCopyAgeUsesClock: the 90-day rule follows the clock.
func TestPathCopyAgeUsesClock(t *testing.T) {
	e := newTestEnv(t)
	e.setPath(ToolYtDlp, standInAt(t, filepath.Join(t.TempDir(), "2026.08.01"), "yt-dlp"))
	if err := e.m.rediscover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if in := e.info(ToolYtDlp); in.State != StateReady {
		t.Fatalf("82 days old: %+v", in)
	}
	e.clock.Advance(10 * 24 * time.Hour)
	if err := e.m.rediscover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if in := e.info(ToolYtDlp); in.State == StateReady {
		t.Errorf("92 days old: %+v", in)
	}
}
