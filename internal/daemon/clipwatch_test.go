package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"downex/internal/clipboard"
	downexv1 "downex/internal/rpc/downex/v1"
)

// clipFake is a clipManager and a ClipboardMonitor driven by the test.
type clipFake struct {
	mu       sync.Mutex
	settings Settings
	notices  chan Notice
	adds     []*downexv1.AddRequest
	addErr   error
	onText   func(string) // the running monitor's handler, nil while stopped
	starts   int
	stops    int
	startErr error
	canceled bool // the subscription ended
	reads    int  // Settings calls
}

func newClipFake(on bool) *clipFake {
	s := DefaultSettings()
	s.Desktop = DefaultDesktopSettings()
	s.Desktop.ClipboardMonitor = on
	return &clipFake{settings: s, notices: make(chan Notice, 64)}
}

func (f *clipFake) Add(_ context.Context, r *downexv1.AddRequest) (*downexv1.Download, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.addErr != nil {
		return nil, f.addErr
	}
	f.adds = append(f.adds, r)
	return &downexv1.Download{Id: int64(len(f.adds))}, nil
}

func (f *clipFake) Settings() Settings {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return f.settings.Clone()
}

func (f *clipFake) Subscribe() (<-chan Notice, func()) {
	return f.notices, func() {
		f.mu.Lock()
		f.canceled = true
		f.mu.Unlock()
	}
}

func (f *clipFake) monitor(onText func(string)) (func() error, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	if f.startErr != nil {
		return nil, f.startErr
	}
	f.onText = onText
	return func() error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.stops++
		f.onText = nil
		return nil
	}, nil
}

// setMonitor changes the setting and announces it as UpdateSettings does.
func (f *clipFake) setMonitor(on bool) {
	f.mu.Lock()
	f.settings.Desktop.ClipboardMonitor = on
	s := f.settings.Clone()
	f.mu.Unlock()
	f.notices <- Notice{Kind: NoticeSettings, Settings: s}
}

// copy hands text to the running monitor's handler (synchronously, like the
// monitor's delivery goroutine); it reports false when no monitor runs.
func (f *clipFake) copy(text string) bool {
	f.mu.Lock()
	h := f.onText
	f.mu.Unlock()
	if h == nil {
		return false
	}
	h(text)
	return true
}

func (f *clipFake) addedURLs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var urls []string
	for _, r := range f.adds {
		urls = append(urls, r.GetUrl())
	}
	return urls
}

func (f *clipFake) counts() (starts, stops int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts, f.stops
}

func runClipWatcher(t *testing.T, f *clipFake, clock *fakeClock) (cancel func(), done <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	w := newClipWatcher(f, testLogger(t), clock.Now)
	ch := make(chan struct{})
	go func() {
		defer close(ch)
		w.run(ctx, f.monitor)
	}()
	// The watcher has subscribed and applied the setting it read: later changes reach it
	// as notices only.
	waitUntil(t, "the watcher to read the settings", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.reads > 0
	})
	return cancel, ch
}

func TestClipWatcherOffersLinks(t *testing.T) {
	f := newClipFake(true)
	clock := newFakeClock()
	cancel, done := runClipWatcher(t, f, clock)
	waitUntil(t, "the monitor to start", func() bool { s, _ := f.counts(); return s == 1 })

	f.copy("https://example.com/a.zip")
	f.copy("https://example.com/a.zip")                                // the same link again: ignored
	f.copy("HTTPS://EXAMPLE.COM/a.zip#x")                              // still the same link
	f.copy("look at https://example.com/b.zip")                        // prose
	f.copy("https://example.com/page.html")                            // no category lists html
	f.copy("https://example.com/c.pdf\r\nhttps://example.com/d.mp4\n") // two links
	clock.advance(clipboard.DedupeWindow)
	f.copy("https://example.com/a.zip") // the window has passed
	want := []string{"https://example.com/a.zip", "https://example.com/c.pdf", "https://example.com/d.mp4", "https://example.com/a.zip"}
	if got := f.addedURLs(); !slices.Equal(got, want) {
		t.Fatalf("added %q, want %q", got, want)
	}
	f.mu.Lock()
	for _, r := range f.adds {
		if r.GetSource() != downexv1.AddSource_ADD_SOURCE_CLIPBOARD || r.GetDir() != "" || r.GetStartPaused() || len(r.GetHeaders()) != 0 {
			t.Errorf("Add request %v: want a plain clipboard add", r)
		}
	}
	f.mu.Unlock()

	cancel()
	<-done
	if starts, stops := f.counts(); starts != 1 || stops != 1 {
		t.Fatalf("monitor started %d and stopped %d times, want 1 and 1", starts, stops)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.canceled {
		t.Fatal("the notice subscription was not ended")
	}
}

func TestClipWatcherFollowsTheSetting(t *testing.T) {
	f := newClipFake(false)
	clock := newFakeClock()
	cancel, done := runClipWatcher(t, f, clock)
	defer func() { cancel(); <-done }()

	// Other notices and unchanged settings do nothing.
	f.notices <- Notice{Kind: NoticeCompleted, Download: &downexv1.Download{Id: 1}}
	f.setMonitor(false)
	f.setMonitor(true)
	waitUntil(t, "the monitor to start", func() bool { s, _ := f.counts(); return s == 1 })
	f.setMonitor(true) // already running
	if !f.copy("https://example.com/1.zip") {
		t.Fatal("no monitor running")
	}
	f.setMonitor(false)
	waitUntil(t, "the monitor to stop", func() bool { _, s := f.counts(); return s == 1 })
	if f.copy("https://example.com/2.zip") {
		t.Fatal("a monitor still runs after the setting was turned off")
	}
	f.setMonitor(true)
	waitUntil(t, "the monitor to restart", func() bool { s, _ := f.counts(); return s == 2 })
	if got := f.addedURLs(); !slices.Equal(got, []string{"https://example.com/1.zip"}) {
		t.Fatalf("added %q", got)
	}
}

func TestClipWatcherIgnoresTextsAfterTurnedOff(t *testing.T) {
	f := newClipFake(true)
	w := newClipWatcher(f, testLogger(t), time.Now)
	f.mu.Lock()
	f.settings.Desktop.ClipboardMonitor = false
	f.mu.Unlock()
	w.handle(context.Background(), "https://example.com/a.zip")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.mu.Lock()
	f.settings.Desktop.ClipboardMonitor = true
	f.mu.Unlock()
	w.handle(ctx, "https://example.com/b.zip")
	if got := f.addedURLs(); len(got) != 0 {
		t.Fatalf("added %q", got)
	}
}

func TestClipWatcherUsesTheCategories(t *testing.T) {
	f := newClipFake(true)
	f.settings.Categories = []Category{{Name: "Disk images", Extensions: []string{"vhdx"}}}
	w := newClipWatcher(f, testLogger(t), time.Now)
	w.handle(context.Background(), "https://example.com/a.zip\nhttps://example.com/disk.VHDX")
	if got := f.addedURLs(); !slices.Equal(got, []string{"https://example.com/disk.VHDX"}) {
		t.Fatalf("added %q", got)
	}
}

func TestClipWatcherAddFailure(t *testing.T) {
	f := newClipFake(true)
	f.addErr = errors.New("invalid_argument: url is longer than 8192 bytes")
	w := newClipWatcher(f, testLogger(t), time.Now)
	w.handle(context.Background(), "https://example.com/a.zip") // logged only
	f.addErr = nil
	w.handle(context.Background(), "https://example.com/a.zip") // offered once only
	if got := f.addedURLs(); len(got) != 0 {
		t.Fatalf("added %q", got)
	}
}

func TestClipWatcherUnsupported(t *testing.T) {
	f := newClipFake(true)
	f.startErr = fmt.Errorf("wrapped: %w", clipboard.ErrNotSupported)
	cancel, done := runClipWatcher(t, f, newFakeClock())
	waitUntil(t, "the start attempt", func() bool { s, _ := f.counts(); return s == 1 })
	f.setMonitor(false)
	f.setMonitor(true) // not tried again
	f.notices <- Notice{Kind: NoticeSettings, Settings: f.Settings()}
	cancel()
	<-done
	if starts, stops := f.counts(); starts != 1 || stops != 0 {
		t.Fatalf("monitor started %d and stopped %d times, want 1 and 0", starts, stops)
	}
}

func TestClipWatcherStartErrorRetried(t *testing.T) {
	f := newClipFake(true)
	f.startErr = errors.New("window creation failed")
	cancel, done := runClipWatcher(t, f, newFakeClock())
	defer func() { cancel(); <-done }()
	waitUntil(t, "the first start", func() bool { s, _ := f.counts(); return s == 1 })
	f.mu.Lock()
	f.startErr = nil
	f.mu.Unlock()
	f.setMonitor(true)
	waitUntil(t, "the retry", func() bool { s, _ := f.counts(); return s == 2 })
	if !f.copy("https://example.com/a.zip") {
		t.Fatal("no monitor running after the retry")
	}
}

func TestClipWatcherEndsWithTheManager(t *testing.T) {
	f := newClipFake(true)
	_, done := runClipWatcher(t, f, newFakeClock())
	waitUntil(t, "the monitor to start", func() bool { s, _ := f.counts(); return s == 1 })
	close(f.notices) // Shutdown closes every subscription
	select {
	case <-done:
	case <-time.After(waitTimeout):
		t.Fatal("the watcher did not end with the notices")
	}
	if _, stops := f.counts(); stops != 1 {
		t.Fatalf("monitor stopped %d times, want 1", stops)
	}
}

// TestWatchClipboardManager runs the watcher on a real Manager: it ends when the
// Manager shuts down.
func TestWatchClipboardManager(t *testing.T) {
	h := newHarness(t, harnessOptions{nobodyAsks: true})
	var mu sync.Mutex
	started := 0
	monitor := func(func(string)) (func() error, error) {
		mu.Lock()
		started++
		mu.Unlock()
		return func() error { return nil }, nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.m.WatchClipboard(context.Background(), monitor)
	}()
	if err := h.m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(waitTimeout):
		t.Fatal("WatchClipboard did not return after Shutdown")
	}
	mu.Lock()
	defer mu.Unlock()
	if started != 0 { // clipboard_monitor is off by default
		t.Fatalf("monitor started %d times with the setting off", started)
	}
}
