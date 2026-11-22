//go:build darwin && cgo

package mac

// How a window opens, closes and takes the wheel, against today's Wails/WebKit window: in
// this process (the shared test App), or in a helper process of this test binary for what
// needs its own App and run loop (a modal loop, user defaults, full screen).

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/mac/popuptest"
)

// A stored size larger than the screen (a window left on a larger display, a hand-edited
// ui-window.json) opens fitted into the screen, as Windows fits its first rectangle into
// the work area: before, 10000 × 9000 points made a drawing surface past its 16384 pixels
// on a Retina screen, NewWindow failed and the main window never opened.
func TestNewWindowFitsTheScreen(t *testing.T) {
	maxW, maxH := popuptest.ScreenContentMax()
	w := openTestWindow(t, &recRoot{ground: kit.RGBA(200, 200, 200, 1)}, kit.Size{W: 10000, H: 9000})
	mainDo(func() {
		s := w.Size()
		if float64(s.W) > maxW+0.5 || float64(s.H) > maxH+0.5 {
			t.Errorf("content %v, the screen holds at most %.0f × %.0f", s, maxW, maxH)
		}
		if pw, ph, _ := w.pixelSize(); pw > maxTargetSide || ph > maxTargetSide {
			t.Errorf("surface %d × %d pixels", pw, ph)
		}
	})
}

// closesInAttach is a window root that closes its window in Attach (the kit contract
// allows it).
type closesInAttach struct{ recRoot }

func (r *closesInAttach) Attach(h kit.Host) {
	r.host = h
	h.Close()
}

// A window closed while it opens never shows, and NewWindow says so (as Windows' does):
// before, it was shown again after its close and NewWindow reported success.
func TestWindowClosedInAttachNeverShows(t *testing.T) {
	a := testApp(t)
	const title = "closed in attach"
	r := &closesInAttach{}
	var w *Window
	var err error
	mainDo(func() { w, err = a.NewWindow(r, WindowOptions{Title: title, Size: kit.Size{W: 300, H: 200}}) })
	if !errors.Is(err, kit.ErrClosed) || w != nil {
		t.Errorf("NewWindow: %v, %v; want ErrClosed and no window", w, err)
	}
	pump(200 * time.Millisecond)
	mainDo(func() {
		if n := popuptest.VisibleTitled(title); n != 0 {
			t.Errorf("%d windows on screen", n)
		}
		if r.detach != 1 {
			t.Errorf("detached %d times", r.detach)
		}
	})
}

// One notch of a mouse wheel scrolls as far as WebKit does on macOS (40 points, its
// Scrollbar::pixelsPerLineStep: a WKWebView fed this event scrolled 40 at once, with no
// animation), in the window and in a popup.
func TestWheelLineIsWebKitsStep(t *testing.T) {
	r := &recRoot{ground: kit.RGBA(220, 220, 220, 1)}
	w := openTestWindow(t, r, kit.Size{W: 400, H: 300})
	mainDo(func() { popuptest.ScrollView(w.ns, 200, 150, -1) }) // one notch down
	var got []kit.WheelEvent
	pumpUntil(2*time.Second, func() bool {
		got = nil
		r.mu.Lock()
		for _, e := range r.events {
			if we, ok := e.(kit.WheelEvent); ok {
				got = append(got, we)
			}
		}
		r.mu.Unlock()
		return len(got) > 0
	})
	if len(got) != 1 || got[0].Delta != kit.Pt(0, 40) || got[0].Mode != kit.WheelPrecise {
		t.Errorf("wheel events %+v, want one of (0, 40) to scroll at once", got)
	}
	if e, ok := popWheel(5, 5, 0, -1, false, 0); !ok || e.Delta != kit.Pt(0, -40) {
		t.Errorf("popup wheel %+v %v", e, ok)
	}
	if e, ok := popWheel(5, 5, 0, 12.5, true, 0); !ok || e.Delta != kit.Pt(0, 12.5) {
		t.Errorf("popup precise wheel %+v %v", e, ok)
	}
}

// The click that brings a window forward from another app reaches the content only of a
// window asked AcceptsFirstMouse (the Confirm download and progress windows); a window's
// default, the main window's, keeps the Mac's rule: that click only brings it forward.
func TestFirstMouseIsTheWindowsOption(t *testing.T) {
	a := testApp(t)
	for _, on := range []bool{false, true} {
		var w *Window
		var err error
		mainDo(func() {
			w, err = a.NewWindow(&recRoot{ground: kit.RGBA(220, 220, 220, 1)},
				WindowOptions{Title: "first mouse", Size: kit.Size{W: 300, H: 200}, NoActivate: true, AcceptsFirstMouse: on})
		})
		if err != nil {
			t.Fatalf("NewWindow: %v", err)
		}
		var got bool
		mainDo(func() {
			got = popuptest.AcceptsFirstMouse(w.ns)
			w.Close()
		})
		pump(100 * time.Millisecond)
		if got != on {
			t.Errorf("AcceptsFirstMouse %v: the content view takes the first click: %v", on, got)
		}
	}
}

// ---- helper processes ------------------------------------------------------------------

const windowHelperEnv = "DXW_WINDOW_HELPER"

// runWindowHelper runs part in a helper process with extra arguments (user defaults read
// from the command line) and reports its "FAIL: " lines; "skip: " skips the test.
func runWindowHelper(t *testing.T, part string, args ...string) {
	t.Helper()
	if testing.Short() {
		t.Skip("opens windows")
	}
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^$"}, args...)...)
	cmd.Env = append(os.Environ(), windowHelperEnv+"="+part)
	out, err := cmd.CombinedOutput()
	text := string(out)
	for _, l := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(l, "FAIL: "):
			t.Error(strings.TrimPrefix(l, "FAIL: "))
		case strings.HasPrefix(l, "skip: "):
			t.Skip(strings.TrimPrefix(l, "skip: "))
		case strings.HasPrefix(l, "log: "):
			t.Log(strings.TrimPrefix(l, "log: "))
		}
	}
	if err != nil {
		t.Errorf("helper: %v\n%s", err, text)
	}
	if !strings.Contains(text, "helper: done") {
		t.Errorf("the helper did not finish:\n%s", text)
	}
}

// Progress windows share one process now: they never become tabs of one window, whatever
// System Settings prefers ("Prefer tabs when opening documents: Always" here), as today's
// windows, each a process of its own, never did.
func TestWindowsNeverTab(t *testing.T) {
	runWindowHelper(t, "tabs", "-AppleWindowTabbingMode", "always")
}

// Closing the last window while an alert's modal loop runs (the download removed while the
// risky-file question is up) ends the event loop once the alert is answered: before, the
// stop ended the alert's loop instead (its answer lost) and run went on with no window.
func TestLastWindowClosedDuringAModalAlert(t *testing.T) {
	runWindowHelper(t, "modal")
}

// Escape that nothing uses leaves full screen, as WebKit hands it to WailsWindow's
// cancelOperation:.
func TestEscapeLeavesFullScreen(t *testing.T) {
	runWindowHelper(t, "fullscreen")
}

// A panic in a root's Attach closes the window the program was told of (OnWindowMade) and
// NewWindow fails, as on Windows: before, the program could not know the window yet and the
// broken window was shown.
func TestPanicInAttachClosesTheWindow(t *testing.T) {
	runWindowHelper(t, "panic")
}

func init() {
	part := os.Getenv(windowHelperEnv)
	if part == "" {
		return
	}
	code := windowHelper(part)
	fmt.Println("helper: done")
	os.Exit(code)
}

// panicsInAttach is a root whose Attach panics.
type panicsInAttach struct{ recRoot }

func (r *panicsInAttach) Attach(kit.Host) { panic("attach failed") }

func windowHelper(part string) int {
	failed := false
	fail := func(f string, args ...any) {
		failed = true
		fmt.Printf("FAIL: "+f+"\n", args...)
	}
	go func() { // a hung run loop must not hang the test
		time.Sleep(40 * time.Second)
		fmt.Println("FAIL: the helper timed out")
		os.Exit(3)
	}()
	var a *App
	var made *Window
	o := AppOptions{Regular: true}
	if part == "panic" {
		o.OnWindowMade = func(w *Window) { made = w }
		o.OnPanic = func(v any) {
			if pw := a.PanicWindow(); pw != nil && pw == made {
				pw.Close()
			}
		}
	}
	a, err := NewApp(o)
	if err != nil {
		fail("NewApp: %v", err)
		return 1
	}
	defer a.Close()
	open := func(title string) *Window {
		w, err := a.NewWindow(&recRoot{ground: kit.RGBA(230, 230, 230, 1)}, WindowOptions{Title: title, Size: kit.Size{W: 400, H: 200}})
		if err != nil {
			fail("NewWindow: %v", err)
			return nil
		}
		return w
	}
	do := func(f func()) {
		done := make(chan struct{})
		a.Post(func() { defer close(done); f() })
		<-done
	}
	waitFor := func(d time.Duration, ok func() bool) bool {
		for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
			var v bool
			do(func() { v = ok() })
			if v {
				return true
			}
		}
		return false
	}

	switch part {
	case "panic":
		w, err := a.NewWindow(&panicsInAttach{}, WindowOptions{Title: "panics", Size: kit.Size{W: 300, H: 200}})
		if !errors.Is(err, kit.ErrClosed) || w != nil {
			fail("NewWindow after a panic in Attach: %v, %v; want ErrClosed", w, err)
		}
		if made == nil {
			fail("OnWindowMade was not told")
		}
		if n := popuptest.VisibleTitled("panics"); n != 0 {
			fail("%d windows on screen", n)
		}
		return code(failed)

	case "tabs":
		w1 := open("tab one")
		if w1 == nil {
			return 1
		}
		go func() {
			time.Sleep(500 * time.Millisecond)
			var w2 *Window
			do(func() { w2 = open("tab two") })
			time.Sleep(700 * time.Millisecond)
			do(func() {
				if w2 != nil {
					if n := popuptest.TabbedWindows(w2.ns); n > 1 {
						fail("the second window is a tab of a window of %d tabs", n)
					}
					fmt.Printf("log: tabbed windows %d and %d\n", popuptest.TabbedWindows(w1.ns), popuptest.TabbedWindows(w2.ns))
					w2.Close()
				}
				w1.Close()
			})
		}()

	case "modal":
		w := open("modal owner")
		if w == nil {
			return 1
		}
		popuptest.RunAlert(1500 * time.Millisecond)
		go func() {
			time.Sleep(400 * time.Millisecond)
			do(func() {
				if popuptest.AlertDone() != 0 {
					fail("the alert ended before the window closed")
				}
				w.Close() // the last window: the event loop is to end, after the alert
			})
		}()
		t0 := time.Now()
		if err := a.Run(); err != nil {
			fail("Run: %v", err)
		}
		if r := popuptest.AlertDone(); r != -1001 {
			fail("the alert's response %d, want its own answer (aborted, -1001), not the loop's stop (-1000)", r)
		}
		if d := time.Since(t0); d < 1400*time.Millisecond {
			fail("Run ended after %v, before the alert was answered", d)
		}
		return code(failed)

	case "fullscreen":
		w := open("full screen")
		if w == nil {
			return 1
		}
		go func() {
			defer do(func() {
				if !w.closed {
					w.Close()
				}
			})
			time.Sleep(500 * time.Millisecond)
			do(func() { popuptest.ToggleFullScreen(w.ns) })
			if !waitFor(6*time.Second, func() bool { return popuptest.FullScreen(w.ns) }) {
				fmt.Println("skip: the window did not enter full screen here")
				return
			}
			time.Sleep(1200 * time.Millisecond) // the animation ends
			do(func() {
				w.postKey(0x35, true, "\x1b")
				w.postKey(0x35, false, "\x1b")
			})
			if !waitFor(6*time.Second, func() bool { return !popuptest.FullScreen(w.ns) }) {
				fail("still in full screen after Escape")
			}
			time.Sleep(1200 * time.Millisecond) // the animation ends before the window closes
		}()
	}
	if err := a.Run(); err != nil {
		fail("Run: %v", err)
	}
	return code(failed)
}

func code(failed bool) int {
	if failed {
		return 1
	}
	return 0
}
