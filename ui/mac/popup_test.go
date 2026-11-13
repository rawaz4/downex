//go:build darwin && cgo

package mac

// [popups] Tests of popup windows, tooltips, drops and dialogs on real AppKit windows (this
// Mac's GUI session). TestMain keeps the process's main thread for AppKit: tests run their
// AppKit work there through mainDo and let it handle events with pump.

import (
	"os"
	"sync"
	"testing"
	"time"

	"downex/ui/kit"
)

var mainFuncs = make(chan func())

func TestMain(m *testing.M) {
	done := make(chan int)
	go func() { done <- m.Run() }()
	for {
		select {
		case f := <-mainFuncs:
			f()
		case code := <-done:
			os.Exit(code)
		}
	}
}

// mainDo runs f on the main thread and waits for it.
func mainDo(f func()) {
	ch := make(chan struct{})
	mainFuncs <- func() {
		defer close(ch)
		f()
	}
	<-ch
}

// pump lets the main thread handle events, posts and frames for d.
func pump(d time.Duration) { mainDo(func() { pumpEvents(d) }) }

// pumpUntil pumps until ok (checked on the main thread) or the deadline.
func pumpUntil(d time.Duration, ok func() bool) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		var done bool
		mainDo(func() { pumpEvents(20 * time.Millisecond); done = ok() })
		if done {
			return true
		}
	}
	return false
}

var (
	testAppOnce sync.Once
	testAppV    *App
)

// testApp is the process's App (made on the main thread at first use).
func testApp(t *testing.T) *App {
	t.Helper()
	testAppOnce.Do(func() {
		mainDo(func() {
			a, err := NewApp(AppOptions{Regular: true})
			if err != nil {
				t.Fatalf("NewApp: %v", err)
			}
			testAppV = a
		})
	})
	if testAppV == nil {
		t.Fatal("no App")
	}
	return testAppV
}

// recRoot is a root that paints its ground and records what it hears.
type recRoot struct {
	mu     sync.Mutex
	host   kit.Host
	ground kit.Color
	box    kit.Rect // painted when not empty (a popup's box)
	events []kit.Event
	frames int
	detach int
	onKey  func(kit.KeyEvent) bool
}

func (r *recRoot) Attach(h kit.Host) { r.host = h }
func (r *recRoot) Detach()           { r.detach++ }
func (r *recRoot) Frame(time.Time)   { r.frames++ }
func (r *recRoot) Paint(c kit.Canvas, dirty kit.Rect) {
	if r.ground.A > 0 {
		c.FillRect(dirty, r.ground)
	}
	if !r.box.Empty() {
		c.FillRect(r.box, kit.RGBA(40, 160, 120, 1))
	}
}
func (r *recRoot) Event(e kit.Event) bool {
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
	if k, ok := e.(kit.KeyEvent); ok && r.onKey != nil {
		return r.onKey(k)
	}
	return false
}
func (r *recRoot) FrameHit(kit.Point) kit.FramePart { return 0 }
func (r *recRoot) Semantics() kit.SemanticTree      { return nil }

func (r *recRoot) pointers(a kit.PointerAction) []kit.PointerEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []kit.PointerEvent
	for _, e := range r.events {
		if p, ok := e.(kit.PointerEvent); ok && p.Action == a {
			out = append(out, p)
		}
	}
	return out
}

func (r *recRoot) count(f func(kit.Event) bool) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if f(e) {
			n++
		}
	}
	return n
}

// openTestWindow opens a window of root on the main thread.
func openTestWindow(t *testing.T, root kit.Root, size kit.Size) *Window {
	t.Helper()
	a := testApp(t)
	var w *Window
	var err error
	mainDo(func() { w, err = a.NewWindow(root, WindowOptions{Title: "popup test", Size: size}) })
	if err != nil {
		t.Fatalf("NewWindow: %v", err)
	}
	mainDo(w.makeKey)
	if !pumpUntil(2*time.Second, func() bool { return bool(w.isKeyNow()) }) {
		t.Log("the test window did not become key (another application is active)")
	}
	t.Cleanup(func() {
		mainDo(func() { w.Close() })
		pump(100 * time.Millisecond)
	})
	return w
}
