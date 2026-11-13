package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
)

// installed installs yt-dlp yt and Deno deno through Ensure.
func (e *testEnv) installed(t *testing.T, yt, deno string) {
	t.Helper()
	e.publishBoth(t, yt, deno)
	if err := e.ensure(t); err != nil {
		t.Fatalf("install %s/%s: %v", yt, deno, err)
	}
}

// check runs the daily check.
func (e *testEnv) check(t *testing.T) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return e.m.CheckUpdate(ctx)
}

func versionDirExists(e *testEnv, tool, version string) bool {
	_, err := os.Stat(filepath.Join(e.dir, tool, version))
	return err == nil
}

// TestUpdatesAndLeases: the daily check installs newer releases and keeps the previous
// one; a version that is neither current nor previous goes once no lease holds it.
func TestUpdatesAndLeases(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	e.installed(t, "2026.10.01", "2.5.0")
	l1, err := e.m.Acquire(context.Background())
	if err != nil || l1.YtDlpVersion != "2026.10.01" {
		t.Fatalf("lease: %+v, %v", l1, err)
	}
	// Nothing newer: nothing downloaded.
	if err := e.check(t); err != nil {
		t.Fatal(err)
	}
	if e.rs.hit("/"+repoYtDlpNightly+"/releases/download/2026.10.01/"+ytdlpSums) != 1 {
		t.Error("an up-to-date check downloaded the checksums")
	}
	e.clock.Advance(24 * time.Hour)
	e.rs.publishYtDlp(repoYtDlpNightly, "2026.10.02", testExeBytes(t), e.signer, e.clock.Now())
	if err := e.check(t); err != nil {
		t.Fatal(err)
	}
	if in := e.info(ToolYtDlp); in.Version != "2026.10.02" || in.LatestVersion != "2026.10.02" {
		t.Fatalf("after the first update: %+v", in)
	}
	st := loadState(e.m.lay)
	if st.YtDlp.Version != "2026.10.02" || st.YtDlp.Previous == nil || st.YtDlp.Previous.Version != "2026.10.01" {
		t.Fatalf("tools.json %+v", st.YtDlp)
	}
	e.clock.Advance(24 * time.Hour)
	e.rs.publishYtDlp(repoYtDlpNightly, "2026.10.03", testExeBytes(t), e.signer, e.clock.Now())
	if err := e.check(t); err != nil {
		t.Fatal(err)
	}
	if !versionDirExists(e, ToolYtDlp, "2026.10.01") {
		t.Fatal("a leased version was removed")
	}
	l1.Release()
	waitFor(t, "the unused version to be removed", func() bool { return !versionDirExists(e, ToolYtDlp, "2026.10.01") })
	for _, v := range []string{"2026.10.02", "2026.10.03"} {
		if !versionDirExists(e, ToolYtDlp, v) {
			t.Errorf("%s removed", v)
		}
	}
	// Deno is checked only every 30 days by the daily check.
	e.rs.publishDeno(t, "v2.5.1", testExeBytes(t), false)
	if err := e.check(t); err != nil || e.info(ToolDeno).Version != "2.5.0" {
		t.Errorf("Deno updated before 30 days: %v, %+v", err, e.info(ToolDeno))
	}
	e.clock.Advance(31 * 24 * time.Hour)
	if err := e.check(t); err != nil || e.info(ToolDeno).Version != "2.5.1" {
		t.Errorf("Deno after 30 days: %v, %+v", err, e.info(ToolDeno))
	}
	// A new major version of Deno is not installed automatically, but the user's check
	// for updates looks at once.
	e.rs.publishDeno(t, "v3.0.0", testExeBytes(t), false)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := e.m.Install(ctx, true); err != nil || e.info(ToolDeno).Version != "2.5.1" {
		t.Errorf("Deno 3: %v, %+v", err, e.info(ToolDeno))
	}
	if in := e.info(ToolDeno); in.LatestVersion != "3.0.0" {
		t.Errorf("latest Deno %q", in.LatestVersion)
	}
}

// TestAutoUpdateOffAndNoInstall: the daily check never installs a missing tool and
// does nothing with automatic updates off.
func TestAutoUpdateOffAndNoInstall(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	e.publishBoth(t, "2026.10.01", "2.5.0")
	if err := e.check(t); err != nil {
		t.Fatal(err)
	}
	if e.rs.total() != 0 || e.info(ToolYtDlp).State != StateMissing {
		t.Fatalf("check installed missing tools: %d requests, %+v", e.rs.total(), e.info(ToolYtDlp))
	}
	if err := e.ensure(t); err != nil {
		t.Fatal(err)
	}
	e.m.Configure(true, false, ChannelNightly)
	e.rs.publishYtDlp(repoYtDlpNightly, "2026.10.02", testExeBytes(t), e.signer, e.clock.Now())
	before := e.rs.total()
	if err := e.check(t); err != nil || e.rs.total() != before || e.info(ToolYtDlp).Version != "2026.10.01" {
		t.Errorf("auto-update off: %v, %d requests", err, e.rs.total()-before)
	}
}

// TestAntiRollback: an older tag is never installed, nor a release whose signature is
// older than the installed one's.
func TestAntiRollback(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	e.installed(t, "2026.10.10", "2.5.0")
	signed := loadState(e.m.lay).YtDlp.SigCreated
	e.rs.publishYtDlp(repoYtDlpNightly, "2026.10.09", testExeBytes(t), e.signer, e.clock.Now())
	if err := e.check(t); err != nil || e.info(ToolYtDlp).Version != "2026.10.10" {
		t.Errorf("older tag: %v, %+v", err, e.info(ToolYtDlp))
	}
	e.rs.publishYtDlp(repoYtDlpNightly, "2026.10.11", testExeBytes(t), e.signer, signed.Add(-time.Hour))
	err := e.check(t)
	in := e.info(ToolYtDlp)
	if codeOf(err) != CodeSignature || in.Version != "2026.10.10" || in.State != StateReady || in.ErrorCode != CodeSignature {
		t.Errorf("older signature: %v, %+v", err, in)
	}
	if versionDirExists(e, ToolYtDlp, "2026.10.11") {
		t.Error("the refused version was installed")
	}
	// Ensure still answers: the working copy stays.
	if err := e.ensure(t); err != nil {
		t.Errorf("Ensure after a failed update: %v", err)
	}
}

// TestChannelSwitch: changing the channel installs the new channel's latest release at
// once, even an older version.
func TestChannelSwitch(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	e.installed(t, "2026.10.10.233141", "2.5.0")
	e.rs.publishYtDlp(repoYtDlpStable, "2026.08.19", testExeBytes(t), e.signer, e.clock.Now().Add(-60*24*time.Hour))
	e.m.Configure(true, true, ChannelStable)
	waitFor(t, "the stable release", func() bool { return e.info(ToolYtDlp).Version == "2026.08.19" })
	st := loadState(e.m.lay)
	if st.YtDlp.Channel != ChannelStable || st.YtDlp.Previous == nil || st.YtDlp.Previous.Version != "2026.10.10.233141" {
		t.Errorf("tools.json %+v", st.YtDlp)
	}
	// Back to nightly: the previous version folder is reused (same hash), no download.
	before := e.rs.hit("/" + repoYtDlpNightly + "/releases/download/2026.10.10.233141/" + ytdlpAsset())
	e.m.Configure(true, true, ChannelNightly)
	waitFor(t, "the nightly release", func() bool { return e.info(ToolYtDlp).Version == "2026.10.10.233141" })
	if after := e.rs.hit("/" + repoYtDlpNightly + "/releases/download/2026.10.10.233141/" + ytdlpAsset()); after != before+1 {
		t.Errorf("asset requests %d → %d", before, after)
	}
}

// TestRollbackBadVersion: a new yt-dlp whose first three extractions fail is tried
// against the previous one; when that succeeds the manager switches back and marks it
// bad for good.
func TestRollbackBadVersion(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	e.installed(t, "2026.10.01", "2.5.0")
	e.rs.publishYtDlp(repoYtDlpNightly, "2026.10.02", testExeBytes(t), e.signer, e.clock.Now())
	if err := e.check(t); err != nil || e.info(ToolYtDlp).Version != "2026.10.02" {
		t.Fatalf("update: %v", err)
	}
	ctx := context.Background()
	for i := range 3 {
		l, err := e.m.Acquire(ctx)
		if err != nil || l.YtDlpVersion != "2026.10.02" {
			t.Fatalf("lease %d: %+v, %v", i, l, err)
		}
		e.m.Report(l, SiteError)
		l.Release()
	}
	alt, err := e.m.Acquire(ctx)
	if err != nil || alt.YtDlpVersion != "2026.10.01" || !alt.YtDlpManaged {
		t.Fatalf("the previous version was not offered: %+v, %v", alt, err)
	}
	// Only one retry with the previous version.
	next, _ := e.m.Acquire(ctx)
	if next.YtDlpVersion != "2026.10.02" {
		t.Errorf("second lease %s", next.YtDlpVersion)
	}
	next.Release()
	e.m.Report(alt, OK)
	alt.Release()
	waitFor(t, "the rollback", func() bool { return e.info(ToolYtDlp).Version == "2026.10.01" })
	waitFor(t, "the bad version to be removed", func() bool { return !versionDirExists(e, ToolYtDlp, "2026.10.02") })
	if st := loadState(e.m.lay); !slices.Contains(st.YtDlp.Bad, "2026.10.02") || st.YtDlp.Version != "2026.10.01" {
		t.Errorf("tools.json %+v", st.YtDlp)
	}
	// The bad version is never installed again; a newer one is.
	if err := e.check(t); err != nil || e.info(ToolYtDlp).Version != "2026.10.01" {
		t.Errorf("bad version reinstalled: %v, %+v", err, e.info(ToolYtDlp))
	}
	e.rs.publishYtDlp(repoYtDlpNightly, "2026.10.03", testExeBytes(t), e.signer, e.clock.Now())
	if err := e.check(t); err != nil || e.info(ToolYtDlp).Version != "2026.10.03" {
		t.Errorf("newer version: %v, %+v", err, e.info(ToolYtDlp))
	}
}

// TestRollbackTrialVerifiesThePrevious: the previous yt-dlp is offered for a rollback
// trial only when it still has its recorded SHA-256 and size; a changed copy is never
// run and the trial ends.
func TestRollbackTrialVerifiesThePrevious(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	e.installed(t, "2026.10.01", "2.5.0")
	e.rs.publishYtDlp(repoYtDlpNightly, "2026.10.02", testExeBytes(t), e.signer, e.clock.Now())
	if err := e.check(t); err != nil || e.info(ToolYtDlp).Version != "2026.10.02" {
		t.Fatalf("update: %v", err)
	}
	prev := filepath.Join(e.dir, ToolYtDlp, "2026.10.01", exeName(ToolYtDlp, runtime.GOOS))
	if err := os.WriteFile(prev, []byte("not the verified copy"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for range 3 {
		l, err := e.m.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		e.m.Report(l, SiteError)
		l.Release()
	}
	l, err := e.m.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if l.YtDlpVersion != "2026.10.02" || samePath(l.YtDlp, prev) {
		t.Errorf("a changed previous copy was offered: %s (%s)", l.YtDlpVersion, l.YtDlp)
	}
}

// TestNoRollbackAfterSuccess: a new version that succeeded once is not rolled back.
func TestNoRollbackAfterSuccess(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	e.installed(t, "2026.10.01", "2.5.0")
	e.rs.publishYtDlp(repoYtDlpNightly, "2026.10.02", testExeBytes(t), e.signer, e.clock.Now())
	if err := e.check(t); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, o := range []Outcome{SiteError, OK, SiteError, SiteError, SiteError} {
		l, _ := e.m.Acquire(ctx)
		if l.YtDlpVersion != "2026.10.02" {
			t.Fatalf("lease of %s", l.YtDlpVersion)
		}
		e.m.Report(l, o)
		l.Release()
	}
}

// TestPathFallback: a PATH yt-dlp that fails where the managed copy (installed for the
// purpose) succeeds is marked unsuitable; one that rejects the options is marked at once.
func TestPathFallback(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	e.publishBoth(t, "2026.10.20", "2.5.0")
	bin := t.TempDir()
	path := standInAt(t, filepath.Join(bin, "2026.10.15"), "yt-dlp")
	e.setPath(ToolYtDlp, path)
	ctx := context.Background()
	l, err := e.m.Acquire(ctx)
	if err != nil || l.YtDlpManaged || !samePath(l.YtDlp, path) {
		t.Fatalf("PATH lease %+v, %v", l, err)
	}
	if v := e.rs.hit("/" + repoYtDlpNightly + "/releases/latest"); v != 0 {
		t.Fatalf("yt-dlp looked for while a PATH copy serves (%d)", v)
	}
	e.m.Report(l, SiteError)
	l.Release()
	alt, err := e.m.Acquire(ctx)
	if err != nil || !alt.YtDlpManaged || alt.YtDlpVersion != "2026.10.20" {
		t.Fatalf("managed lease %+v, %v", alt, err)
	}
	e.m.Report(alt, OK)
	alt.Release()
	if in := e.info(ToolYtDlp); !in.Managed || in.Version != "2026.10.20" {
		t.Errorf("after the fallback: %+v", in)
	}
	st := loadState(e.m.lay)
	waitFor(t, "tools.json to record the PATH copy", func() bool {
		st = loadState(e.m.lay)
		return len(st.YtDlp.Unsuitable) == 1
	})
	if !samePath(st.YtDlp.Unsuitable[0].Path, path) || st.YtDlp.Unsuitable[0].Version != "2026.10.15" {
		t.Errorf("unsuitable %+v", st.YtDlp.Unsuitable)
	}
	// Discovery keeps it out until its --version changes.
	if err := e.m.rediscover(ctx); err != nil {
		t.Fatal(err)
	}
	if in := e.info(ToolYtDlp); !in.Managed {
		t.Errorf("the unsuitable PATH copy serves again: %+v", in)
	}
	newer := standInAt(t, filepath.Join(bin, "2026.10.21"), "yt-dlp")
	e.setPath(ToolYtDlp, newer)
	if err := e.m.rediscover(ctx); err != nil {
		t.Fatal(err)
	}
	if in := e.info(ToolYtDlp); in.Managed || in.Version != "2026.10.21" {
		t.Errorf("a new PATH version is not used: %+v", in)
	}
	// Exit code 2 (rejected options) marks it at once.
	l, _ = e.m.Acquire(ctx)
	e.m.Report(l, BadOptions)
	l.Release()
	if in := e.info(ToolYtDlp); !in.Managed {
		t.Errorf("after BadOptions: %+v", in)
	}
	// A PATH copy that fails where the managed copy fails too stays.
	e2 := newTestEnv(t)
	e2.publishBoth(t, "2026.10.20", "2.5.0")
	e2.setPath(ToolYtDlp, standInAt(t, filepath.Join(t.TempDir(), "2026.10.15"), "yt-dlp"))
	l, _ = e2.m.Acquire(ctx)
	e2.m.Report(l, SiteError)
	l.Release()
	alt, _ = e2.m.Acquire(ctx)
	e2.m.Report(alt, SiteError)
	alt.Release()
	if in := e2.info(ToolYtDlp); in.Managed || in.Version != "2026.10.15" {
		t.Errorf("PATH copy dropped although the managed copy failed too: %+v", in)
	}
	// A trial lease released without a report ends the trial.
	l, _ = e2.m.Acquire(ctx)
	e2.m.Report(l, SiteError)
	l.Release()
	alt, _ = e2.m.Acquire(ctx)
	alt.Release()
	if again, _ := e2.m.Acquire(ctx); again.YtDlpManaged {
		t.Errorf("trial still pending: %+v", again)
	} else {
		again.Release()
	}
}

// TestExtraCheckAfterSiteError: a site error with the managed yt-dlp looks for an
// update, at most once an hour.
func TestExtraCheckAfterSiteError(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	e.installed(t, "2026.10.01", "2.5.0")
	latest := "/" + repoYtDlpNightly + "/releases/latest"
	base := e.rs.hit(latest)
	report := func() {
		l, err := e.m.Acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		e.m.Report(l, SiteError)
		l.Release()
	}
	report()
	waitFor(t, "the extra check", func() bool { return e.rs.hit(latest) == base+1 && e.m.Status().Tools[0].State == StateReady })
	waitFor(t, "the run to end", func() bool {
		e.m.mu.Lock()
		defer e.m.mu.Unlock()
		return e.m.run == nil
	})
	report()
	time.Sleep(200 * time.Millisecond)
	if e.rs.hit(latest) != base+1 {
		t.Errorf("second extra check within the hour")
	}
	e.clock.Advance(61 * time.Minute)
	report()
	waitFor(t, "the next extra check", func() bool { return e.rs.hit(latest) == base+2 })
}

// TestDenoDisabledPlatform: without a Deno build, yt-dlp alone is ready.
func TestDenoDisabledPlatform(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(c *Config, _ *hooks) {
		c.GOARCH = map[string]string{"windows": "386", "linux": "arm", "darwin": "amd64"}[runtime.GOOS]
	})
	if e.m.assets.Deno != "" {
		t.Skip("this system's 32-bit variant has a Deno build")
	}
	if e.m.assets.YtDlp == "" {
		t.Skip("no yt-dlp build for the 32-bit variant of this system")
	}
	e.setPath(ToolYtDlp, standInAt(t, filepath.Join(t.TempDir(), "2026.10.20"), "yt-dlp"))
	if err := e.ensure(t); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	s := e.m.Status()
	dn := s.Tools[1]
	if !s.Ready || dn.State != StateDisabled || dn.ErrorCode != CodeUnsupportedPlatform || dn.Error != "no Deno build for this platform; YouTube offers fewer formats" {
		t.Errorf("status %+v", s)
	}
	l, err := e.m.Acquire(context.Background())
	if err != nil || l.Deno != "" {
		t.Errorf("lease %+v, %v", l, err)
	}
	l.Release()
}
