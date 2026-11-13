package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"downex/internal/sitemedia"
)

// TestAcquireReady: the acquire of a look-ahead (docs/SITE-MEDIA-SPEC.md §5.6) never
// installs, updates or waits for an install, never hands out the alternative copy of a
// fallback or rollback trial (the trial stays for the next Acquire), and waits only for
// a discovery that has not ended, which never downloads.
func TestAcquireReady(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Missing tools: tools_unavailable once discovery ended, nothing fetched or started.
	e := newTestEnv(t)
	e.publishBoth(t, "2026.10.20", "2.5.0")
	if s := e.m.Status(); !s.Discovering || s.Ready {
		t.Fatalf("before any discovery: %+v", s)
	}
	if l, err := e.m.AcquireReady(ctx); !errors.Is(err, sitemedia.ErrToolsUnavailable) || l != nil {
		t.Fatalf("AcquireReady of missing tools = %+v, %v; want tools_unavailable", l, err)
	}
	if n := e.rs.total(); n != 0 {
		t.Errorf("a look-ahead's acquire made %d release requests", n)
	}
	if s := e.m.Status(); s.Discovering || s.Tools[0].State != StateMissing {
		t.Errorf("after AcquireReady: %+v", s)
	}

	// Copies on PATH: the first discovery is waited for.
	e = newTestEnv(t)
	e.publishBoth(t, "2026.10.20", "2.5.0")
	bin := t.TempDir()
	path := standInAt(t, filepath.Join(bin, "2026.10.15"), "yt-dlp")
	e.setPath(ToolYtDlp, path)
	e.setPath(ToolDeno, standInAt(t, filepath.Join(bin, "2.5.0"), "deno"))
	l, err := e.m.AcquireReady(ctx)
	if err != nil || l.YtDlpManaged || !samePath(l.YtDlp, path) || l.Deno == "" {
		t.Fatalf("AcquireReady before discovery = %+v, %v; want the PATH copies", l, err)
	}
	// A failed PATH copy schedules the managed copy's trial; look-aheads keep the PATH
	// copy and never install the managed one.
	e.m.Report(l, SiteError)
	l.Release()
	for i := range 2 {
		l, err := e.m.AcquireReady(ctx)
		if err != nil || l.YtDlpManaged {
			t.Fatalf("look-ahead %d: %+v, %v; want the PATH copy", i, l, err)
		}
		l.Release()
	}
	if n := e.rs.total(); n != 0 {
		t.Errorf("look-aheads made %d release requests", n)
	}
	if in := e.info(ToolYtDlp); in.State != StateReady || in.Managed {
		t.Errorf("after the look-aheads: %+v", in)
	}
	// The trial is still the next user's.
	alt, err := e.m.Acquire(ctx)
	if err != nil || !alt.YtDlpManaged || alt.YtDlpVersion != "2026.10.20" {
		t.Fatalf("the user's Acquire after the look-aheads = %+v, %v; want the managed trial copy", alt, err)
	}
	alt.Release()

	e.m.Configure(false, true, ChannelNightly)
	if _, err := e.m.AcquireReady(ctx); !errors.Is(err, sitemedia.ErrSiteMediaOff) {
		t.Errorf("AcquireReady with site media off = %v", err)
	}
}

// TestAcquireReadyLeavesTheRollbackTrial: a new yt-dlp that failed its first
// extractions is tried against the previous one by the next Acquire, never by a
// look-ahead's.
func TestAcquireReadyLeavesTheRollbackTrial(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	e.installed(t, "2026.10.01", "2.5.0")
	e.rs.publishYtDlp(repoYtDlpNightly, "2026.10.02", testExeBytes(t), e.signer, e.clock.Now())
	if err := e.check(t); err != nil || e.info(ToolYtDlp).Version != "2026.10.02" {
		t.Fatalf("update: %v", err)
	}
	ctx := context.Background()
	for range rollbackAfter {
		l, err := e.m.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		e.m.Report(l, SiteError)
		l.Release()
	}
	ahead, err := e.m.AcquireReady(ctx)
	if err != nil || ahead.YtDlpVersion != "2026.10.02" {
		t.Fatalf("look-ahead lease %+v, %v; want the current version", ahead, err)
	}
	ahead.Release()
	alt, err := e.m.Acquire(ctx)
	if err != nil || alt.YtDlpVersion != "2026.10.01" {
		t.Fatalf("the user's Acquire = %+v, %v; want the previous version's trial", alt, err)
	}
	alt.Release()
}

// TestStartupRestoresASetAsideCopy: a daemon that stopped between the two renames of a
// same-version replacement left the only copy as <version>.old-*: the next start puts it
// back instead of removing it.
func TestStartupRestoresASetAsideCopy(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	e.installed(t, "2026.10.01", "2.5.0")
	final := filepath.Join(e.dir, ToolYtDlp, "2026.10.01")
	old := final + oldInfix + "0123456789abcdef"
	e.restart(t, func() {
		if err := os.Rename(final, old); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(final+newInfix+"fedcba9876543210", 0o700); err != nil {
			t.Fatal(err)
		}
	})
	before := e.rs.total()
	if err := e.ensure(t); err != nil {
		t.Fatal(err)
	}
	if n := e.rs.total() - before; n != 0 {
		t.Errorf("the copy was downloaded again (%d requests)", n)
	}
	if in := e.info(ToolYtDlp); !in.Managed || in.Version != "2026.10.01" {
		t.Errorf("after the restart: %+v", in)
	}
	for _, p := range []string{old, final + newInfix + "fedcba9876543210"} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left: %v", filepath.Base(p), err)
		}
	}
	// Two set-aside copies of one version: neither is guessed at.
	e.restart(t, func() {
		if err := os.Rename(final, final+oldInfix+"aaaaaaaaaaaaaaaa"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(final+oldInfix+"bbbbbbbbbbbbbbbb", 0o700); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := os.Stat(final); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a copy was put back from two candidates: %v", err)
	}
}
