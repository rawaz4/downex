//go:build windows

package win

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/win/internal/w32"

	"golang.org/x/sys/windows"
)

// modalLoop stands in for a modal loop that a posted function enters (a message box, a
// menu, a file dialog): it gets and dispatches the thread's messages itself, as the
// system's loops do, until done holds or the timeout passes. A goroutine keeps waking it.
func modalLoop(t *testing.T, a *App, w *Window, timeout time.Duration, done func() bool) {
	t.Helper()
	stop := make(chan struct{})
	defer close(stop)
	h := w.hwnd
	go func() {
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				w32.PostMessage(h, 0, 0, 0)
			}
		}
	}()
	msg := new(w32.MSG)
	deadline := time.Now().Add(timeout)
	for !done() && time.Now().Before(deadline) {
		if w32.GetMessage(msg) <= 0 {
			return
		}
		w32.TranslateMessage(msg)
		w32.DispatchMessage(msg)
	}
}

// Posted functions never nest: one that enters a modal loop lets the loop dispatch the
// window's msgPost, and what was posted meanwhile runs after it has returned, in order and
// all of it. (The double buffer of the old runPosts was swapped under its own range loop
// here: the window procedure panicked and the functions after it were lost.)
func TestPostedFunctionsNeverNest(t *testing.T) {
	root := &testRoot{}
	a, w := openTest(t, root, WindowOptions{})
	pump(t, a, w, "the first frame", 2*time.Second, func() bool { return w.painted })
	var order []string
	depth, maxDepth := 0, 0
	run := func(name string, f func()) func() {
		return func() {
			depth++
			maxDepth = max(maxDepth, depth)
			order = append(order, name)
			if f != nil {
				f()
			}
			depth--
		}
	}
	var inLoop atomic.Bool
	w.Post(run("f", func() {
		inLoop.Store(true)
		w.Post(run("g", nil)) // from inside the loop, as an answer from the daemon would come
		modalLoop(t, a, w, 300*time.Millisecond, func() bool { return false })
		if strings.Contains(strings.Join(order, " "), "g") {
			t.Error("a function posted during the modal loop ran inside it")
		}
	}))
	w.Post(run("X", nil))
	w.Post(run("Y", nil))
	pump(t, a, w, "every posted function", 2*time.Second, func() bool { return len(order) == 4 })
	if got := strings.Join(order, " "); got != "f X Y g" || maxDepth != 1 {
		t.Errorf("ran %q at depth up to %d, want \"f X Y g\" one at a time", got, maxDepth)
	}
	if w.depth != 0 || len(w.queue) != 0 {
		t.Errorf("after the posts: depth %d, %d queued", w.depth, len(w.queue))
	}
}

// The App's notices reach it through its message window, which a modal loop dispatches as
// any other window message: a settings change made while the loop runs is read then (a
// thread message would be dropped by the loop's DispatchMessage).
func TestNoticesSurviveModalLoops(t *testing.T) {
	root := &testRoot{}
	a, w := openTest(t, root, WindowOptions{})
	pump(t, a, w, "the first frame", 2*time.Second, func() bool { return w.painted })
	var reads atomic.Int32
	env := w.Environment()
	a.readEnv = func(uint32) kit.Environment { reads.Add(1); return env }
	ran := false
	w.Post(func() {
		ran = true
		if err := a.post(msgSettings); err != nil { // as the registry watch does
			t.Error(err)
		}
		modalLoop(t, a, w, 2*time.Second, func() bool { return reads.Load() > 0 })
		if reads.Load() == 0 {
			t.Error("the settings notice was lost in the modal loop")
		}
	})
	pump(t, a, w, "the posted function", 3*time.Second, func() bool { return ran })

	// Quit from inside a modal loop ends Run once the loop is over.
	w.Post(func() {
		modalLoop(t, a, w, 50*time.Millisecond, func() bool { return false })
		a.Quit()
		modalLoop(t, a, w, 50*time.Millisecond, func() bool { return false })
	})
	stop := make(chan struct{})
	defer close(stop)
	h := w.hwnd
	go func() { // a Run that does not return is ended, and fails the test below
		select {
		case <-stop:
		case <-time.After(3 * time.Second):
			w32.PostMessage(h, w32.WM_CLOSE, 0, 0)
		}
	}()
	start := time.Now()
	if err := a.Run(); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el > 2*time.Second || w.closed {
		t.Errorf("Run returned after %v (window closed: %v), not once the loop that called Quit was over", el, w.closed)
	}
}

// fillQueue posts WM_NULL to h until the thread's message queue is full (Windows' quota is
// 10,000 messages) and returns how many it posted.
func fillQueue(t *testing.T, h w32.HWND) int {
	t.Helper()
	for n := 0; n < 20000; n++ {
		if err := w32.PostMessage(h, 0, 0, 0); err != nil {
			if !errors.Is(err, windows.Errno(1816)) { // ERROR_NOT_ENOUGH_QUOTA
				t.Fatalf("PostMessage failed with %v, not the queue's quota", err)
			}
			return n
		}
	}
	t.Skip("the message queue never filled up")
	return 0
}

// A wake-up that cannot be posted (the queue is full) is not lost: Post fails and takes its
// function back, and the next Post wakes the window again; a frame asked for meanwhile still
// comes (the frame clock posts it again).
func TestWakeUpsAfterAFullQueue(t *testing.T) {
	root := &testRoot{}
	a, w := openTest(t, root, WindowOptions{})
	pump(t, a, w, "the first frame", 2*time.Second, func() bool { return w.painted })
	n := fillQueue(t, w.hwnd)
	t.Logf("the queue filled up after %d messages", n)
	lost := false
	if err := w.Post(func() { lost = true }); err == nil {
		t.Fatal("Post succeeded with a full queue")
	}
	frames := root.frames
	w.RequestFrame()
	time.Sleep(100 * time.Millisecond) // the clock tries meanwhile, and fails
	ran := false
	pump(t, a, w, "the queue to drain", 10*time.Second, func() bool { return w.Post(func() { ran = true }) == nil })
	pump(t, a, w, "the function posted after the queue drained", 2*time.Second, func() bool { return ran })
	if lost {
		t.Error("a function whose Post failed ran")
	}
	pump(t, a, w, "the frame asked for while the queue was full", 2*time.Second, func() bool { return root.frames > frames })
}

// A popup (WS_POPUP) gets WM_SIZE inside CreateWindowEx; the window is ready for it, and the
// popup opens at its client size, with no frame around it.
func TestPopupWindow(t *testing.T) {
	root := &testRoot{}
	a, w := openTest(t, root, WindowOptions{Style: w32.WS_POPUP, Size: kit.Size{W: 240, H: 160}})
	pump(t, a, w, "the first frame", 2*time.Second, func() bool { return w.painted })
	wr := w32.GetWindowRect(w.hwnd)
	if wr.Right-wr.Left != w.pixels(240) || wr.Bottom-wr.Top != w.pixels(160) || w.Size() != (kit.Size{W: 240, H: 160}) {
		t.Errorf("popup %dx%d px, size %v; want %dx%d px", wr.Right-wr.Left, wr.Bottom-wr.Top, w.Size(), w.pixels(240), w.pixels(160))
	}
}

// A window larger than its monitor's work area opens inside it, title bar on screen,
// whether it is placed or centred.
func TestInitialPlacementFitsTheWorkArea(t *testing.T) {
	mon := w32.MonitorFromPoint(w32.Point{}, w32.MONITOR_DEFAULTTOPRIMARY)
	mi, _ := w32.GetMonitorInfo(mon)
	work := mi.Work
	for _, c := range []struct {
		name string
		opts WindowOptions
	}{
		{"placed", WindowOptions{Size: kit.Size{W: 20000, H: 20000}, Placed: true, At: w32.Point{X: work.Left + 40, Y: work.Top + 40}}},
		{"centred", WindowOptions{Size: kit.Size{W: 20000, H: 20000}}},
		{"centred, small", WindowOptions{Size: kit.Size{W: 300, H: 200}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := &testRoot{}
			runtimeLock(t)
			a, err := NewApp(AppOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			root.t = t
			c.opts.NoActivate = true
			w, err := a.NewWindow(root, c.opts)
			if err != nil {
				t.Fatal(err)
			}
			wr := w32.GetWindowRect(w.hwnd)
			if wr.Left < work.Left || wr.Top < work.Top || wr.Right > work.Right || wr.Bottom > work.Bottom {
				t.Errorf("window %+v, outside the work area %+v", wr, work)
			}
			if !c.opts.Placed {
				cx, cy := (wr.Left+wr.Right)/2, (wr.Top+wr.Bottom)/2
				wx, wy := (work.Left+work.Right)/2, (work.Top+work.Bottom)/2
				if d := max(cx-wx, wx-cx, cy-wy, wy-cy); d > 1 {
					t.Errorf("centre %d,%d, work area's %d,%d", cx, cy, wx, wy)
				}
			}
		})
	}
}

var procGetForegroundWindow = user32.NewProc("GetForegroundWindow")

// Resize of a minimised window sets the size it restores to (SetWindowPlacement, whose
// structure had the wrong length and failed), keeps it minimised and activates nothing.
func TestResizeWhileMinimised(t *testing.T) {
	root := &testRoot{}
	a, w := openTest(t, root, WindowOptions{Size: kit.Size{W: 320, H: 200}})
	pump(t, a, w, "the first frame", 2*time.Second, func() bool { return w.painted })
	fg, _, _ := procGetForegroundWindow.Call()
	w32.ShowWindow(w.hwnd, 7) // SW_SHOWMINNOACTIVE
	pump(t, a, w, "the minimised state", time.Second, func() bool { return w.WindowState().Minimized })
	w.Resize(kit.Size{W: 400, H: 260})
	if w.hostErr != nil {
		t.Fatal(w.hostErr)
	}
	p := new(w32.WINDOWPLACEMENT)
	if err := w32.GetWindowPlacement(w.hwnd, p); err != nil {
		t.Fatal(err)
	}
	ww, wh := w.outerSize(w.pixels(400), w.pixels(260), w.sysDPI())
	if n := p.NormalPosition; n.Right-n.Left != ww || n.Bottom-n.Top != wh {
		t.Errorf("restored rectangle %dx%d, want %dx%d", n.Right-n.Left, n.Bottom-n.Top, ww, wh)
	}
	if !w32.IsIconic(w.hwnd) {
		t.Error("Resize restored the minimised window")
	}
	if now, _, _ := procGetForegroundWindow.Call(); now != fg {
		t.Errorf("the foreground window changed from %#x to %#x", fg, now)
	}
	w32.ShowWindow(w.hwnd, w32.SW_SHOWNOACTIVATE)
	pump(t, a, w, "the restored size", time.Second, func() bool { return w.Size() == kit.Size{W: 400, H: 260} })
}

// A GPU that keeps losing the device falls back to the software renderer instead of making
// a new GPU target at every frame, tries the GPU again once the fallback's time is over,
// and when even the software target keeps losing it, the window repaints only after a pause.
func TestRepeatedDeviceLossFallsBack(t *testing.T) {
	root := &testRoot{}
	a, w := openTest(t, root, WindowOptions{})
	pump(t, a, w, "the first frame", 2*time.Second, func() bool { return w.painted })
	if w.Renderer() != "hardware" {
		t.Skipf("the window draws with %s, not the GPU", w.Renderer())
	}
	losing := true
	root.onFrame = func() { w.target.dev.simulateLoss = losing }
	w.Invalidate(kit.R(0, 0, 5, 5))
	pump(t, a, w, "the fallback", 3*time.Second, func() bool { return w.target.usedSW })
	t.Logf("software after %d losses, %d targets made", w.target.dev.losses, w.target.dev.targetsMade)
	if made := w.target.dev.targetsMade; made != lossLimit+1 { // the first, and one after each loss but the last
		t.Errorf("%d targets made before the software one, want %d", made-1, lossLimit)
	}

	// The software target loses it too: a pause, not a loop of repaints.
	paints := len(root.paints)
	pumpFor(t, a, w, 700*time.Millisecond, func() bool { return false })
	if n := len(root.paints) - paints; n > 2*lossLimit {
		t.Errorf("%d repaints in 700 ms while every frame loses the device", n)
	}

	// The GPU's turn comes again.
	losing = false
	root.onFrame = nil
	pump(t, a, w, "a frame on software", 3*time.Second, func() bool { return w.painted && w.target.dev.rt != nil })
	w.target.swUntil = time.Now().Add(-time.Second)
	paints = len(root.paints)
	w.Invalidate(kit.R(0, 0, 5, 5))
	pump(t, a, w, "the GPU again", 3*time.Second, func() bool { return w.Renderer() == "hardware" && len(root.paints) > paints })
	if got := root.paints[len(root.paints)-1]; got != kit.RectAt(kit.Point{}, w.Size()) {
		t.Errorf("the first frame on the new target painted %v, not the whole window", got)
	}
	if w.target.backoff != swBackoff {
		t.Errorf("backoff %v, want %v", w.target.backoff, swBackoff)
	}
}

// The auto-hide cache answers from what it knows, reads a new monitor on a goroutine and
// says so when that changes the frame; WM_NCCALCSIZE never waits for the taskbar.
func TestAutoHideCache(t *testing.T) {
	var answer atomic.Value
	answer.Store([4]bool{w32.ABE_BOTTOM: true})
	var reads, notes atomic.Int32
	read := func(w32.Rect) [4]bool { defer reads.Add(1); return answer.Load().([4]bool) }
	c := newAutoHideCache(read, func() { notes.Add(1) })
	mon := windows.Handle(1)
	rc := w32.Rect{Right: 1920, Bottom: 1080}
	if _, ok := c.edges(mon, rc); ok {
		t.Fatal("a monitor never read is known")
	}
	c.edges(mon, rc) // a second miss while it is read reads it once
	wait := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(2 * time.Second); !cond(); {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(time.Millisecond)
		}
	}
	wait("the answer", func() bool { _, ok := c.edges(mon, rc); return ok })
	if e, _ := c.edges(mon, rc); e != answer.Load().([4]bool) || reads.Load() != 1 {
		t.Errorf("edges %v after %d reads", e, reads.Load())
	}
	wait("the notice", func() bool { return notes.Load() == 1 })

	// A refresh that finds the same says nothing; one that finds a change does.
	idle := func() bool { c.mu.Lock(); defer c.mu.Unlock(); return len(c.reading) == 0 }
	c.refresh()
	wait("the refresh", func() bool { return reads.Load() == 2 && idle() })
	if notes.Load() != 1 {
		t.Error("a refresh that found the same bars sent a notice")
	}
	answer.Store([4]bool{})
	c.refresh()
	wait("the changed refresh", func() bool { return notes.Load() == 2 })
	if e, _ := c.edges(mon, rc); e != ([4]bool{}) {
		t.Errorf("edges %v after the bar went", e)
	}
	// A monitor whose rectangle changed is read again.
	if _, ok := c.edges(mon, w32.Rect{Right: 2560, Bottom: 1600}); ok {
		t.Error("a monitor of a new size answered from the old one")
	}
	wait("the last read", idle)
}
