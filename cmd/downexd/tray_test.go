package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	"downex/internal/daemon"
	"downex/internal/ipc"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/store"
	"downex/internal/testserver"
	"downex/internal/transport"
	"downex/internal/tray"
)

// fakeIcon records what the runner shows.
type fakeIcon struct {
	mu       sync.Mutex
	opts     tray.Options
	visible  []bool
	tips     []string
	balloons []string // "Title|Text", "!" prefixed for errors
	closed   bool
}

func (f *fakeIcon) SetVisible(v bool) { f.mu.Lock(); f.visible = append(f.visible, v); f.mu.Unlock() }
func (f *fakeIcon) SetTooltip(s string) {
	f.mu.Lock()
	f.tips = append(f.tips, s)
	f.mu.Unlock()
}

func (f *fakeIcon) Notify(b tray.Balloon) {
	s := b.Title + "|" + b.Text
	if b.Error {
		s = "!" + s
	}
	f.mu.Lock()
	f.balloons = append(f.balloons, s)
	f.mu.Unlock()
}

func (f *fakeIcon) Close() error { f.mu.Lock(); f.closed = true; f.mu.Unlock(); return nil }

func (f *fakeIcon) snapshot() (visible []bool, tips, balloons []string, closed bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.visible), slices.Clone(f.tips), slices.Clone(f.balloons), f.closed
}

// useFakeIcon makes startTray create a fakeIcon for the test.
func useFakeIcon(t *testing.T) *fakeIcon {
	t.Helper()
	icon := &fakeIcon{}
	prev := newTrayIcon
	newTrayIcon = func(opts tray.Options) (trayIcon, error) {
		icon.mu.Lock()
		icon.opts = opts
		icon.mu.Unlock()
		return icon, nil
	}
	t.Cleanup(func() { newTrayIcon = prev })
	return icon
}

// eventually polls cond until it holds or 20 s passed.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func newTestManager(t *testing.T) *daemon.Manager {
	t.Helper()
	tf, err := transport.NewHTTPFactory(transport.DefaultHTTPOptions())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	m, err := daemon.New(context.Background(), daemon.Config{
		Store: store.NewMemory(), Transport: tf,
		DownloadsDir: func() (string, error) { return dir, nil },
		Logger:       slog.New(slog.DiscardHandler), Version: "test", Endpoint: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.Shutdown(ctx)
	})
	return m
}

func (f *fakeIcon) command(c tray.Command) {
	f.mu.Lock()
	on := f.opts.OnCommand
	f.mu.Unlock()
	on(c)
}

// TestTrayFollowsTheDaemon drives the tray runner with a real daemon (in-process API)
// and a fake icon: visibility and notification switches from the settings, the
// tooltip from the activity, notifications for completed, failed and unconfirmable
// downloads, and the menu commands.
func TestTrayFollowsTheDaemon(t *testing.T) {
	slow := testserver.New(testserver.Options{Size: 400 << 10, Seed: 7, Throttle: 160 << 10, ETag: `"slow"`})
	t.Cleanup(slow.Close)
	missing := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(missing.Close)
	m := newTestManager(t)
	icon := useFakeIcon(t)
	quit := make(chan struct{}, 1)
	var opens atomic.Int32
	openErr := errors.New("the Downex window is not installed next to downexd")
	stop := startTray(t.Context(), trayConfig{
		daemon: m,
		quit:   func() { quit <- struct{}{} },
		openUI: func() error {
			opens.Add(1)
			return openErr
		},
		tipInterval: 50 * time.Millisecond,
	})
	defer stop()

	// Defaults: the icon is shown with the idle tooltip.
	if icon.opts.Tooltip != "Downex — 0 active, 0 B/s" {
		t.Errorf("initial tooltip %q", icon.opts.Tooltip)
	}
	eventually(t, "the icon to be shown", func() bool { v, _, _, _ := icon.snapshot(); return slices.Equal(v, []bool{true}) })

	ctx := t.Context()
	dir := t.TempDir()
	add := func(r *downexv1.AddRequest) *downexv1.Download {
		t.Helper()
		r.Dir = dir
		d, err := m.Add(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	state := func(id int64) downexv1.DownloadState {
		d, err := m.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return d.GetState()
	}
	hasBalloon := func(want string) func() bool {
		return func() bool { _, _, b, _ := icon.snapshot(); return slices.Contains(b, want) }
	}

	// A running download shows in the tooltip.
	d := add(&downexv1.AddRequest{Url: slow.URL + "/files/slow.bin", MaxConnections: 1})
	eventually(t, "an active tooltip", func() bool {
		_, tips, _, _ := icon.snapshot()
		return slices.ContainsFunc(tips, func(s string) bool { return strings.HasPrefix(s, "Downex — 1 active, ") })
	})

	// Pause all and Resume all from the menu.
	icon.command(tray.CommandPauseAll)
	if s := state(d.GetId()); s != downexv1.DownloadState_DOWNLOAD_STATE_PAUSED {
		t.Fatalf("after Pause all the download is %v", s)
	}
	icon.command(tray.CommandResumeAll)
	if s := state(d.GetId()); s == downexv1.DownloadState_DOWNLOAD_STATE_PAUSED {
		t.Fatal("Resume all left the download paused")
	}

	// Completion and failure notifications; the tooltip returns to idle.
	eventually(t, "the completion notification", hasBalloon("Download complete|slow.bin"))
	eventually(t, "the idle tooltip", func() bool {
		_, tips, _, _ := icon.snapshot()
		return len(tips) > 0 && tips[len(tips)-1] == "Downex — 0 active, 0 B/s"
	})
	add(&downexv1.AddRequest{Url: missing.URL + "/gone/missing.bin"})
	eventually(t, "the failure notification", hasBalloon("!Download failed|missing.bin\nThe server answered HTTP 404 Not Found."))

	// A browser hand-off awaits confirmation, and there is no window to ask.
	a := add(&downexv1.AddRequest{Url: slow.URL + "/files/confirm-me.bin", Source: downexv1.AddSource_ADD_SOURCE_BROWSER})
	if a.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION {
		t.Fatalf("browser hand-off is %v", a.GetState())
	}
	eventually(t, "the confirmation notification", hasBalloon("Confirm download|confirm-me.bin"))

	// Settings apply live: notifications off, then the icon hidden.
	svc := daemon.NewDaemonService(m, nil)
	res, err := svc.GetSettings(ctx, connect.NewRequest(&downexv1.GetSettingsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	s := res.Msg.GetSettings()
	s.NotifyOnFailure = false
	if _, err := m.UpdateSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // the runner applies the settings notice
	_, _, before, _ := icon.snapshot()
	q := add(&downexv1.AddRequest{Url: missing.URL + "/quiet.bin"})
	eventually(t, "the second failure", func() bool {
		return state(q.GetId()) == downexv1.DownloadState_DOWNLOAD_STATE_FAILED
	})
	time.Sleep(100 * time.Millisecond)
	if _, _, after, _ := icon.snapshot(); len(after) != len(before) {
		t.Errorf("a failure was notified with notify_on_failure off: %q", after[len(before):])
	}
	s.ShowTrayIcon = false
	if _, err := m.UpdateSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the icon to be hidden", func() bool { v, _, _, _ := icon.snapshot(); return slices.Equal(v, []bool{true, false}) })
	// A hidden icon shows no notifications, and there is no window to ask: a browser
	// hand-off is queued rather than held where nobody would ever see it (GO-3).
	if b := add(&downexv1.AddRequest{Url: missing.URL + "/files/unasked.bin", Source: downexv1.AddSource_ADD_SOURCE_BROWSER,
		StartPaused: true}); b.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_PAUSED {
		t.Fatalf("browser hand-off with the icon hidden is %v", b.GetState())
	}

	// Open (and Settings…) start the window, debounced; a failure is reported.
	icon.command(tray.CommandOpen)
	icon.command(tray.CommandSettings)
	if n := opens.Load(); n != 1 {
		t.Errorf("window started %d times, want 1", n)
	}
	eventually(t, "the open failure", hasBalloon("!Cannot open Downex|the Downex window is not installed next to downexd"))

	// Quit stops the daemon gracefully.
	icon.command(tray.CommandQuit)
	select {
	case <-quit:
	case <-time.After(5 * time.Second):
		t.Fatal("Quit did not ask the daemon to stop")
	}

	stop()
	if _, _, _, closed := icon.snapshot(); !closed {
		t.Fatal("stop did not remove the icon")
	}
	stop() // idempotent
}

// fakeDaemon is a scripted trayDaemon.
type fakeDaemon struct {
	mu       sync.Mutex
	settings daemon.Settings
	activity daemon.Activity
	notices  chan daemon.Notice
	watches  int
	watch    func(ctx context.Context, n int, send func(*downexv1.WatchResponse) error) error
	pauseErr error
	shows    []bool // ShowNotices calls for the notices channel
}

func newFakeDaemon() *fakeDaemon {
	return &fakeDaemon{
		settings: daemon.Settings{ShowTrayIcon: true, NotifyOnComplete: true, NotifyOnFailure: true},
		notices:  make(chan daemon.Notice, 64),
	}
}

func (f *fakeDaemon) Settings() daemon.Settings                 { f.mu.Lock(); defer f.mu.Unlock(); return f.settings }
func (f *fakeDaemon) Subscribe() (<-chan daemon.Notice, func()) { return f.notices, func() {} }
func (f *fakeDaemon) Activity() daemon.Activity                 { f.mu.Lock(); defer f.mu.Unlock(); return f.activity }

func (f *fakeDaemon) setActivity(a daemon.Activity) { f.mu.Lock(); f.activity = a; f.mu.Unlock() }

func (f *fakeDaemon) ShowNotices(ch <-chan daemon.Notice, shows bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ch == (<-chan daemon.Notice)(f.notices) {
		f.shows = append(f.shows, shows)
	}
}

func (f *fakeDaemon) showCalls() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.shows)
}

func (f *fakeDaemon) Watch(ctx context.Context, _ []int64, send func(*downexv1.WatchResponse) error) error {
	f.mu.Lock()
	f.watches++
	n, w := f.watches, f.watch
	f.mu.Unlock()
	if w == nil {
		<-ctx.Done()
		return ctx.Err()
	}
	return w(ctx, n, send)
}

func (f *fakeDaemon) PauseAll(context.Context) (int, error)  { return 0, f.pauseErr }
func (f *fakeDaemon) ResumeAll(context.Context) (int, error) { return 2, nil }

func (f *fakeDaemon) watchCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.watches }

// TestTrayTooltipIsThrottled: a stream of changes updates the tooltip at most once per
// interval, ends on the latest text, and nothing more happens while nothing changes.
func TestTrayTooltipIsThrottled(t *testing.T) {
	icon := useFakeIcon(t)
	f := newFakeDaemon()
	const interval = 200 * time.Millisecond
	streamed := make(chan struct{})
	f.watch = func(ctx context.Context, _ int, send func(*downexv1.WatchResponse) error) error {
		for i := 1; i <= 40; i++ {
			f.setActivity(daemon.Activity{Active: i, SpeedBps: float64(i) * 1000})
			if err := send(&downexv1.WatchResponse{}); err != nil {
				return err
			}
			time.Sleep(25 * time.Millisecond) // 1 s in all
		}
		close(streamed)
		<-ctx.Done()
		return ctx.Err()
	}
	start := time.Now()
	stop := startTray(t.Context(), trayConfig{daemon: f, tipInterval: interval})
	defer stop()
	<-streamed
	eventually(t, "the latest tooltip", func() bool {
		_, tips, _, _ := icon.snapshot()
		return len(tips) > 0 && tips[len(tips)-1] == "Downex — 40 active, 40.0 KB/s"
	})
	elapsed := time.Since(start)
	_, tips, _, _ := icon.snapshot()
	if limit := int(elapsed/interval) + 1; len(tips) > limit || len(tips) < 3 {
		t.Errorf("%d tooltip updates in %v (interval %v): %q", len(tips), elapsed, interval, tips)
	}
	time.Sleep(3 * interval)
	if _, again, _, _ := icon.snapshot(); len(again) != len(tips) {
		t.Errorf("tooltip updated without changes: %q", again[len(tips):])
	}
}

// TestTrayNotificationsAndFailures: notices become balloons (coalesced), an awaiting
// download the window was asked about is not announced, a closed notice channel does
// not stop the runner, a failed Watch is reopened, and a failed Pause all is shown.
func TestTrayNotificationsAndFailures(t *testing.T) {
	icon := useFakeIcon(t)
	f := newFakeDaemon()
	f.pauseErr = connect.NewError(connect.CodeFailedPrecondition, errors.New("the store is closed"))
	f.watch = func(ctx context.Context, n int, send func(*downexv1.WatchResponse) error) error {
		if n == 1 {
			return errors.New("stream broke")
		}
		<-ctx.Done()
		return ctx.Err()
	}
	stop := startTray(t.Context(), trayConfig{daemon: f, retryInterval: 20 * time.Millisecond})
	defer stop()

	// An outcome that its progress window shows gets no balloon.
	f.notices <- daemon.Notice{Kind: daemon.NoticeFailed, InWindow: true, Download: &downexv1.Download{Id: 20, Filename: "shown.bin"}}
	f.notices <- daemon.Notice{Kind: daemon.NoticeCompleted, InWindow: true, Download: &downexv1.Download{Id: 21, Filename: "shown.zip"}}
	for i := int64(1); i <= 5; i++ {
		f.notices <- daemon.Notice{Kind: daemon.NoticeCompleted, Download: &downexv1.Download{Id: i, Filename: fmt.Sprintf("f%d.zip", i)}}
	}
	f.notices <- daemon.Notice{Kind: daemon.NoticeAwaiting, UILaunched: true, Download: &downexv1.Download{Id: 9, Filename: "asked.bin"}}
	f.notices <- daemon.Notice{Kind: daemon.NoticeAwaiting, Download: &downexv1.Download{Id: 10, Filename: "ask-me.bin"}}
	want := []string{"Download complete|f1.zip", "Download complete|f2.zip", "Download complete|f3.zip", "Confirm download|ask-me.bin"}
	eventually(t, "the notifications", func() bool { _, _, b, _ := icon.snapshot(); return len(b) >= len(want) })
	time.Sleep(100 * time.Millisecond)
	if _, _, b, _ := icon.snapshot(); !slices.Equal(b, want) {
		t.Fatalf("balloons %q, want %q (the rest of the burst is held)", b, want)
	}

	eventually(t, "the watch to be reopened", func() bool { return f.watchCount() >= 2 })

	icon.command(tray.CommandPauseAll)
	eventually(t, "the Pause all failure", func() bool {
		_, _, b, _ := icon.snapshot()
		return slices.Contains(b, "!Could not pause downloads|the store is closed")
	})
	icon.command(tray.CommandResumeAll) // succeeds: nothing shown
	_, _, b, _ := icon.snapshot()
	if last := b[len(b)-1]; strings.Contains(last, "resume") {
		t.Errorf("a successful Resume all was notified: %q", last)
	}

	// The daemon shuts down: its notices end, the runner keeps the icon until stopped.
	close(f.notices)
	time.Sleep(50 * time.Millisecond)
	if _, _, _, closed := icon.snapshot(); closed {
		t.Fatal("the icon closed when the notices ended")
	}
	stop()
	if _, _, _, closed := icon.snapshot(); !closed {
		t.Fatal("stop did not close the icon")
	}
}

// The tray tells the daemon whether its notifications can be seen (GO-3): before
// startTray returns while the icon is shown, then whenever show_tray_icon changes.
func TestTrayTellsWhetherItShowsNotifications(t *testing.T) {
	useFakeIcon(t)
	f := newFakeDaemon()
	stop := startTray(t.Context(), trayConfig{daemon: f})
	defer stop()
	if got := f.showCalls(); !slices.Equal(got, []bool{true}) {
		t.Fatalf("ShowNotices calls when startTray returned: %v, want [true]", got)
	}
	f.notices <- daemon.Notice{Kind: daemon.NoticeSettings, Settings: daemon.Settings{ShowTrayIcon: false}}
	eventually(t, "the hidden icon to be reported", func() bool { return slices.Equal(f.showCalls(), []bool{true, false}) })
	f.notices <- daemon.Notice{Kind: daemon.NoticeSettings, Settings: daemon.Settings{ShowTrayIcon: true, NotifyOnComplete: true}}
	eventually(t, "the shown icon to be reported", func() bool { return slices.Equal(f.showCalls(), []bool{true, false, true}) })

	hidden := newFakeDaemon()
	hidden.settings.ShowTrayIcon = false
	startTray(t.Context(), trayConfig{daemon: hidden})()
	if got := hidden.showCalls(); len(got) != 0 {
		t.Fatalf("ShowNotices calls of a tray whose icon is hidden: %v", got)
	}
}

func TestTrayNotSupported(t *testing.T) {
	prev := newTrayIcon
	t.Cleanup(func() { newTrayIcon = prev })
	newTrayIcon = func(tray.Options) (trayIcon, error) { return nil, tray.ErrNotSupported }
	f := newFakeDaemon()
	stop := startTray(t.Context(), trayConfig{daemon: f})
	stop()
	if f.watchCount() != 0 {
		t.Fatal("the runner ran without an icon")
	}
}

func TestOwnDaemonAndDesktopHooks(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == ipc.EnvEndpoint {
				return v
			}
			return ""
		}
	}
	if !ownDaemon(options{}, env("")) {
		t.Error("the default daemon is not the user's own")
	}
	for _, o := range []options{{db: "x.db"}, {endpoint: `\\.\pipe\other`}} {
		if ownDaemon(o, env("")) {
			t.Errorf("%+v is the user's own", o)
		}
	}
	if ownDaemon(options{}, env(`\\.\pipe\other`)) {
		t.Error("a daemon on $DOWNEX_ENDPOINT is the user's own")
	}

	var calls []string
	prev := setLaunchAtLogin
	t.Cleanup(func() { setLaunchAtLogin = prev })
	setLaunchAtLogin = func(enabled bool, exe string, args ...string) error {
		calls = append(calls, fmt.Sprint(enabled, " ", strings.HasSuffix(strings.ToLower(exe), ".test.exe") || strings.HasSuffix(exe, ".test"), " ", args))
		return nil
	}
	log := slog.New(slog.DiscardHandler)
	t.Setenv(ipc.EnvEndpoint, "")
	own := desktopHooks(options{}, log)
	if !own.tray || own.launchAtLogin == nil {
		t.Fatalf("own daemon: %+v", own)
	}
	if err := own.launchAtLogin(true); err != nil {
		t.Fatal(err)
	}
	if want := []string{"true true [--background]"}; !slices.Equal(calls, want) {
		t.Errorf("sign-in entry calls %q, want %q", calls, want)
	}
	if own.openConfirmation != nil {
		t.Error("a confirmation window without downex-ui next to downexd")
	}
	if d := desktopHooks(options{noTray: true}, log); d.tray || d.launchAtLogin == nil {
		t.Errorf("--no-tray: %+v", d)
	}
	if d := desktopHooks(options{db: "x.db"}, log); d.tray || d.launchAtLogin != nil {
		t.Errorf("another database: %+v", d)
	}
	if err := openUI(); err == nil || !strings.Contains(err.Error(), "not installed next to downexd") {
		t.Errorf("openUI without the window: %v", err)
	}
}

func TestTrayFlags(t *testing.T) {
	o, ok, code := parseFlags([]string{"--no-tray", "--background"}, &bytes.Buffer{}, &bytes.Buffer{})
	if !ok || code != exitOK || !o.noTray {
		t.Fatalf("--no-tray --background: %+v ok %v code %d", o, ok, code)
	}
	if o, ok, _ := parseFlags(nil, &bytes.Buffer{}, &bytes.Buffer{}); !ok || o.noTray {
		t.Fatalf("defaults: %+v", o)
	}
}
