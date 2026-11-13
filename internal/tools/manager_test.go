package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/sitemedia"
)

// TestEnsureJoinsOneInstall: concurrent callers share one install.
func TestEnsureJoinsOneInstall(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	e.publishBoth(t, "2026.10.20", "2.5.0")
	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Go(func() {
			if i%2 == 0 {
				errs[i] = e.ensure(t)
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			_, errs[i] = e.m.Install(ctx, false)
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("caller %d: %v", i, err)
		}
	}
	if n := e.rs.hit("/" + repoYtDlpNightly + "/releases/download/2026.10.20/" + ytdlpAsset()); n != 1 {
		t.Errorf("asset downloaded %d times", n)
	}
}

// TestCloseDuringInstall: Close cancels a running install, leaves no partial download
// and refuses later calls.
func TestCloseDuringInstall(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	e.publishBoth(t, "2026.10.20", "2.5.0")
	e.rs.gate(repoYtDlpNightly, "2026.10.20", ytdlpAsset()) // never opened
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_ = e.m.Ensure(ctx)
	waitFor(t, "a partial download", func() bool { return e.info(ToolYtDlp).BytesDone > 0 })
	done := make(chan struct{})
	go func() {
		_ = e.m.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Close did not return")
	}
	if entries, _ := os.ReadDir(filepath.Join(e.dir, "tmp")); len(entries) != 0 {
		t.Errorf("tmp holds %v", entries)
	}
	if err := e.ensure(t); !errors.Is(err, ErrClosed) || !errors.Is(err, sitemedia.ErrToolsUnavailable) {
		t.Errorf("Ensure after Close: %v", err)
	}
	if _, err := e.m.Acquire(context.Background()); !errors.Is(err, ErrClosed) {
		t.Errorf("Acquire after Close: %v", err)
	}
	if err := e.m.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// TestInstallWaitsForFolderLock: another process holding the tools folder's lock holds
// installs back until it lets go.
func TestInstallWaitsForFolderLock(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	e.publishBoth(t, "2026.10.20", "2.5.0")
	f, err := os.OpenFile(filepath.Join(e.dir, lockName), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if busy, err := tryLock(f); err != nil || busy {
		t.Fatalf("tryLock: %v, %v", busy, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := e.m.Ensure(ctx); !errors.Is(err, sitemedia.ErrToolsInstalling) {
		t.Fatalf("Ensure while locked: %v", err)
	}
	if n := e.rs.total(); n != 0 {
		t.Errorf("%d requests while the folder was locked", n)
	}
	if err := unlockFile(f); err != nil {
		t.Fatal(err)
	}
	if err := e.ensure(t); err != nil {
		t.Errorf("Ensure after unlock: %v", err)
	}
}

// TestSubscribe: subscribers hear about changes until they cancel.
func TestSubscribe(t *testing.T) {
	e := newTestEnv(t)
	var mu sync.Mutex
	n := 0
	cancel := e.m.Subscribe(func() {
		mu.Lock()
		n++
		mu.Unlock()
	})
	e.m.Configure(true, false, ChannelStable)
	waitFor(t, "a notification", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return n > 0
	})
	cancel()
	cancel()
	mu.Lock()
	before := n
	mu.Unlock()
	e.m.Configure(false, false, ChannelStable)
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if n != before {
		t.Errorf("notified after cancel")
	}
}

func TestNewValidates(t *testing.T) {
	for name, cfg := range map[string]Config{
		"relative dir":   {Dir: "tools"},
		"http base":      {Dir: t.TempDir(), ReleaseBase: "http://github.com"},
		"base w/ query":  {Dir: t.TempDir(), ReleaseBase: "https://github.com/?x=1"},
		"bad custom key": {Dir: t.TempDir(), YtDlpKeys: [][]byte{[]byte("not a key")}},
	} {
		if m, err := New(cfg); err == nil {
			m.Close()
			t.Errorf("%s: accepted", name)
		}
	}
	m, err := New(Config{Dir: t.TempDir(), ReleaseBase: "https://example.test/"})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if m.cfg.ReleaseBase != "https://example.test" || m.fetch != nil {
		t.Errorf("config %+v", m.cfg)
	}
}

// TestDisableDuringInstall: turning site media off stops the install without a backoff
// or an error; turning it on again installs at once.
func TestDisableDuringInstall(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	e.publishBoth(t, "2026.10.20", "2.5.0")
	open := e.rs.gate(repoYtDlpNightly, "2026.10.20", ytdlpAsset())
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_ = e.m.Ensure(ctx)
	waitFor(t, "a partial download", func() bool { return e.info(ToolYtDlp).BytesDone > 0 })
	e.m.Configure(false, true, "")
	waitFor(t, "the install to stop", func() bool {
		e.m.mu.Lock()
		defer e.m.mu.Unlock()
		return e.m.run == nil
	})
	open()
	e.m.Configure(true, true, "")
	if err := e.ensure(t); err != nil {
		t.Fatalf("Ensure after turning site media on again: %v", err)
	}
	if in := e.info(ToolYtDlp); in.Error != "" || in.State != StateReady {
		t.Errorf("yt-dlp %+v", in)
	}
}

// TestNoYtDlpBuild: a platform without a yt-dlp build can use a PATH copy only.
func TestNoYtDlpBuild(t *testing.T) {
	e := newTestEnv(t, func(c *Config, _ *hooks) { c.GOOS, c.GOARCH = "linux", "riscv64" })
	err := e.ensure(t)
	if codeOf(err) != CodeUnsupportedPlatform || err.Error() != "yt-dlp and Deno are not set up: no yt-dlp build for this platform" {
		t.Fatalf("Ensure: %v", err)
	}
	s := e.m.Status()
	if s.Tools[0].State != StateFailed || s.Tools[0].ErrorCode != CodeUnsupportedPlatform || s.Tools[1].State != StateDisabled || e.rs.total() != 0 {
		t.Errorf("status %+v", s)
	}
}

// TestStateMatchesProto: State converts to ToolState by value.
func TestStateMatchesProto(t *testing.T) {
	for s, want := range map[State]downexv1.ToolState{
		StateUnspecified: downexv1.ToolState_TOOL_STATE_UNSPECIFIED,
		StateMissing:     downexv1.ToolState_TOOL_STATE_MISSING,
		StateInstalling:  downexv1.ToolState_TOOL_STATE_INSTALLING,
		StateReady:       downexv1.ToolState_TOOL_STATE_READY,
		StateFailed:      downexv1.ToolState_TOOL_STATE_FAILED,
		StateDisabled:    downexv1.ToolState_TOOL_STATE_DISABLED,
	} {
		if downexv1.ToolState(s) != want {
			t.Errorf("State %d is ToolState %v, want %v", s, downexv1.ToolState(s), want)
		}
	}
}
