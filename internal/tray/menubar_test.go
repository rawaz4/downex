package tray

import (
	"bytes"
	"errors"
	"fmt"
	"image/png"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"downex/internal/notify"
)

// fakeBackend records the backend calls of a menuBar; serveMain plays the main thread.
type fakeBackend struct {
	wakes chan struct{}

	mu        sync.Mutex
	calls     []string
	showFails int // the next n shows fail
	created   bool
	released  bool
	problems  []string // contract violations
}

func newFakeBackend() *fakeBackend { return &fakeBackend{wakes: make(chan struct{}, 16)} }

func (f *fakeBackend) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.released {
		f.problems = append(f.problems, s+" after release")
	}
	f.calls = append(f.calls, s)
}

func (f *fakeBackend) wake() {
	f.mu.Lock()
	if f.released {
		f.problems = append(f.problems, "wake after release")
	}
	if len(f.wakes) > 0 {
		f.problems = append(f.problems, "a second wake while one is pending")
	}
	f.mu.Unlock()
	select {
	case f.wakes <- struct{}{}:
	default:
		panic("wake would block")
	}
}

func (f *fakeBackend) show(tip string) error {
	f.record("show " + tip)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.showFails > 0 {
		f.showFails--
		return errors.New("no menu bar")
	}
	f.created = true
	return nil
}

func (f *fakeBackend) hide() error { f.record("hide"); return nil }

func (f *fakeBackend) setTip(tip string) error { f.record("tip " + tip); return nil }

func (f *fakeBackend) post(b Balloon) error {
	f.record(fmt.Sprintf("post %s|%s", b.Title, b.Text))
	return nil
}

func (f *fakeBackend) destroy() { f.record("destroy") }

func (f *fakeBackend) release() {
	f.record("release")
	f.mu.Lock()
	f.released = true
	f.mu.Unlock()
}

// take returns and clears the recorded calls.
func (f *fakeBackend) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.calls
	f.calls = nil
	return c
}

func (f *fakeBackend) check(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.problems {
		t.Error(p)
	}
}

// serveMain drains mb on a goroutine of its own, as the main thread's event loop
// does, until the test ends.
func serveMain(t *testing.T, mb *menuBar, f *fakeBackend) {
	quit, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-f.wakes:
				mb.drain()
			case <-quit:
				return
			}
		}
	}()
	t.Cleanup(func() { close(quit); <-done })
}

// onMain runs fn on the main thread and waits for it.
func onMain[T any](t *testing.T, mb *menuBar, fn func(*machine) T) T {
	t.Helper()
	ch := make(chan T, 1)
	mb.call(func(m *machine) { ch <- fn(m) })
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatal("the main thread does not answer")
		panic("unreachable")
	}
}

// settle waits until the main thread has applied every earlier request.
func settle(t *testing.T, mb *menuBar) {
	t.Helper()
	onMain(t, mb, func(*machine) bool { return true })
}

func expectBackend(t *testing.T, what string, mb *menuBar, f *fakeBackend, want ...string) {
	t.Helper()
	settle(t, mb)
	if got := f.take(); !slices.Equal(got, want) {
		t.Errorf("%s: backend calls %q, want %q", what, got, want)
	}
}

func TestMenuBarShowsTooltipAndNotifications(t *testing.T) {
	f := newFakeBackend()
	mb := newMenuBar(Options{Tooltip: "Downex — 0 active, 0 B/s"}, f)
	serveMain(t, mb, f)
	defer mb.close()

	mb.setTooltip("Downex — 1 active, 1 B/s")
	mb.notify(Balloon{Title: "dropped"})
	expectBackend(t, "while hidden", mb, f) // remembered or dropped, nothing shown

	mb.setVisible(true)
	expectBackend(t, "show", mb, f, "show Downex — 1 active, 1 B/s")
	mb.setTooltip("Downex — 2 active, 5.0 MB/s")
	mb.notify(Balloon{Title: "Download complete", Text: "a.zip"})
	mb.notify(Balloon{Title: "Download failed", Text: "b.zip\nThe disk is full.", Error: true})
	expectBackend(t, "tooltip and notifications", mb, f,
		"tip Downex — 2 active, 5.0 MB/s", "post Download complete|a.zip", "post Download failed|b.zip\nThe disk is full.")

	mb.setVisible(false)
	mb.notify(Balloon{Title: "hidden"})
	expectBackend(t, "hide", mb, f, "hide")
	mb.setVisible(true)
	expectBackend(t, "shown again", mb, f, "show Downex — 2 active, 5.0 MB/s")

	// Requests made while the main thread is busy are applied together by one drain.
	busy, gate := make(chan struct{}), make(chan struct{})
	mb.call(func(*machine) { close(busy); <-gate })
	<-busy
	for i := range 20 {
		mb.setTooltip(fmt.Sprintf("Downex — %d active, 0 B/s", i))
	}
	close(gate)
	expectBackend(t, "burst", mb, f, "tip Downex — 19 active, 0 B/s")
	f.check(t)
}

func TestMenuBarQueuedNotificationsAreBounded(t *testing.T) {
	f := newFakeBackend()
	mb := newMenuBar(Options{Tooltip: "tip"}, f)
	mb.setVisible(true)
	for i := range menuBarMaxQueued + 3 {
		mb.notify(Balloon{Title: fmt.Sprint(i)})
	}
	serveMain(t, mb, f) // the main thread comes late
	settle(t, mb)
	got := f.take()
	if len(got) != 1+menuBarMaxQueued || got[0] != "show tip" || got[1] != "post 3|" || got[len(got)-1] != "post 10|" {
		t.Errorf("backend calls %q: want the show and the latest %d notifications", got, menuBarMaxQueued)
	}
	mb.close()
	f.check(t)
}

func TestMenuBarCommands(t *testing.T) {
	f := newFakeBackend()
	got := make(chan Command, 16)
	release := make(chan struct{})
	mb := newMenuBar(Options{OnCommand: func(c Command) {
		got <- c
		<-release
	}}, f)
	serveMain(t, mb, f)

	// The menu's choices reach OnCommand one at a time, in order; dispatch never
	// blocks the main thread, and unknown commands are ignored.
	mb.dispatch(CommandPauseAll, 0, Command(99), CommandResumeAll)
	if c := <-got; c != CommandPauseAll {
		t.Fatalf("first command %v", c)
	}
	start := time.Now()
	for range 20 { // OnCommand is busy: the queue fills, the rest is dropped
		mb.dispatch(CommandQuit)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("dispatch blocked for %v", d)
	}
	release <- struct{}{}
	if c := <-got; c != CommandResumeAll {
		t.Fatalf("second command %v", c)
	}
	close(release)
	n := 0
	for {
		select {
		case c := <-got:
			if c != CommandQuit {
				t.Fatalf("unexpected command %v", c)
			}
			n++
			continue
		case <-time.After(200 * time.Millisecond):
		}
		break
	}
	if n == 0 || n > cap(mb.cmds) {
		t.Errorf("%d queued commands ran, want 1..%d", n, cap(mb.cmds))
	}
	if err := mb.close(); err != nil {
		t.Fatal(err)
	}
	f.check(t)
}

func TestMenuBarClose(t *testing.T) {
	f := newFakeBackend()
	mb := newMenuBar(Options{Tooltip: "tip"}, f)
	serveMain(t, mb, f)
	mb.setVisible(true)
	expectBackend(t, "show", mb, f, "show tip")

	if err := mb.close(); err != nil {
		t.Fatal(err)
	}
	if got := f.take(); !slices.Equal(got, []string{"hide", "destroy", "release"}) {
		t.Errorf("close: backend calls %q", got)
	}
	select {
	case <-mb.done:
	default:
		t.Fatal("close returned before the final drain")
	}
	// Everything afterwards is ignored, and nothing wakes the main thread again.
	mb.setVisible(false)
	mb.setTooltip("x")
	mb.notify(Balloon{Title: "x"})
	mb.call(func(*machine) { t.Error("a call ran after close") })
	if err := mb.close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if got := f.take(); len(got) != 0 {
		t.Errorf("after close: backend calls %q", got)
	}
	f.check(t)

	// A tray closed before it was ever shown only releases the backend.
	f2 := newFakeBackend()
	mb2 := newMenuBar(Options{}, f2)
	serveMain(t, mb2, f2)
	if err := mb2.close(); err != nil {
		t.Fatal(err)
	}
	if got := f2.take(); !slices.Equal(got, []string{"destroy", "release"}) {
		t.Errorf("close of a hidden tray: backend calls %q", got)
	}
	f2.check(t)
}

func TestMenuBarCloseWithoutMainThread(t *testing.T) {
	f := newFakeBackend()
	mb := newMenuBar(Options{}, f)
	mb.closeTimeout = 50 * time.Millisecond
	if err := mb.close(); err == nil || !strings.Contains(err.Error(), "main thread") {
		t.Fatalf("close without a main thread: %v", err)
	}
	if err := mb.close(); err == nil {
		t.Fatal("the second close forgot the failure")
	}
}

func TestMenuBarRetriesTheStatusItem(t *testing.T) {
	f := newFakeBackend()
	f.showFails = 2
	mb := newMenuBar(Options{Tooltip: "tip"}, f)
	mb.retryInterval = 20 * time.Millisecond
	serveMain(t, mb, f)
	defer mb.close()

	mb.setVisible(true)
	mb.notify(Balloon{Title: "waits"})
	deadline := time.Now().Add(10 * time.Second)
	for !onMain(t, mb, func(m *machine) bool { return m.added }) {
		if time.Now().After(deadline) {
			t.Fatal("the status item was never added")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := f.take(); !slices.Equal(got, []string{"show tip", "show tip", "show tip", "post waits|"}) {
		t.Errorf("retries: backend calls %q", got)
	}
	if onMain(t, mb, func(*machine) bool { return mb.retryTimer != nil }) {
		t.Error("the retry timer still runs after the item was added")
	}

	// Hidden while retrying: the retries stop.
	f.mu.Lock()
	f.showFails = 1000
	f.mu.Unlock()
	mb.setVisible(false)
	mb.setVisible(true)
	time.Sleep(50 * time.Millisecond)
	mb.setVisible(false)
	settle(t, mb)
	f.take()
	time.Sleep(100 * time.Millisecond)
	settle(t, mb)
	if got := f.take(); len(got) != 0 {
		t.Errorf("hidden: still retrying: %q", got)
	}
	f.check(t)
}

func TestMenuStatus(t *testing.T) {
	for _, c := range []struct {
		s    notify.Summary
		want string
	}{
		{notify.Summary{}, "0 active · 0 B/s"},
		{notify.Summary{Active: 2, Speed: 1_500_000}, "2 active · 1.5 MB/s"},
		{notify.Summary{Active: 1, Speed: 10_139_730}, "1 active · 10.1 MB/s"}, // 9.67 MiB/s
		{notify.Summary{Active: 12, Speed: 512}, "12 active · 512 B/s"},
	} {
		tip := notify.Tooltip(c.s)
		if got := menuStatus(tip); got != c.want {
			t.Errorf("menuStatus(%q) = %q, want %q", tip, got, c.want)
		}
	}
	for _, other := range []string{"", "Downex tray test", "Something else, here"} {
		if got := menuStatus(other); got != other {
			t.Errorf("menuStatus(%q) = %q, want it unchanged", other, got)
		}
	}
}

func TestCText(t *testing.T) {
	for in, want := range map[string]string{
		"Settings…":            "Settings…",
		"a\x00b":               "ab",
		"bad\xffbyte":          "badbyte",
		"two\nlines":           "two\nlines",
		"Downex — 1 active, 0": "Downex — 1 active, 0",
	} {
		if got := cText(in); got != want {
			t.Errorf("cText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTemplateIcon(t *testing.T) {
	for _, px := range []int{templatePoints, 2 * templatePoints} {
		img := templateIcon(px)
		opaque := 0
		for y := range px {
			for x := range px {
				c := img.NRGBAAt(x, y)
				if c.R != 0 || c.G != 0 || c.B != 0 {
					t.Fatalf("%d px: pixel %d,%d is %v, want black (a template image)", px, x, y, c)
				}
				if c.A == 255 {
					opaque++
				}
			}
		}
		for _, p := range [][2]int{{0, 0}, {px - 1, 0}, {0, px - 1}, {px - 1, px - 1}} {
			if a := img.NRGBAAt(p[0], p[1]).A; a != 0 {
				t.Errorf("%d px: corner %v alpha %d, want transparent", px, p, a)
			}
		}
		// The mark's square left edge: the bars start at the same column.
		if a := int(img.NRGBAAt(px/4, px/2-px/8).A) + int(img.NRGBAAt(px/4, px/2+px/8).A); a == 0 {
			t.Errorf("%d px: no bar at the left edge", px)
		}
		if opaque < px*px/8 {
			t.Errorf("%d px: only %d opaque pixels", px, opaque)
		}
		dec, err := png.Decode(bytes.NewReader(templatePNG(px)))
		if err != nil {
			t.Fatal(err)
		}
		if b := dec.Bounds(); b.Dx() != px || b.Dy() != px {
			t.Errorf("%d px: PNG is %v", px, b)
		}
	}
}
