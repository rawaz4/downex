//go:build darwin && cgo

package mac

// [render] Real windows on this Mac: two windows in one process share the Resources and
// have their own display links; an idle window has no ticks; an animating one ticks at the
// display's rate while the other stays idle; every resize shows a frame of the new size at
// once (never the old one stretched, never a blank strip); a minimised window draws
// nothing; closing one window keeps the other working.
//
// AppKit needs the process's main thread, which a test function does not run on, and a
// TestMain would clash with other parts' tests: so the test runs its own binary again with
// DXR_WINDOW_HELPER set, and that copy does the checks from an init function, on the main
// thread, before the testing package starts.

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"downex/ui/kit"
)

const rndHelperEnv = "DXR_WINDOW_HELPER"

func TestRndWindows(t *testing.T) {
	if testing.Short() {
		t.Skip("opens windows")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), rndHelperEnv+"=1")
	out, err := cmd.CombinedOutput()
	text := string(out)
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "rnd: ") {
			t.Log(strings.TrimPrefix(l, "rnd: "))
		}
		if strings.HasPrefix(l, "FAIL: ") {
			t.Error(strings.TrimPrefix(l, "FAIL: "))
		}
	}
	if err != nil {
		t.Errorf("helper: %v\n%s", err, text)
	}
	if !strings.Contains(text, "rnd: done") {
		t.Errorf("the helper did not finish:\n%s", text)
	}
}

func init() {
	if os.Getenv(rndHelperEnv) == "" {
		return
	}
	code := rndWindowHelper()
	os.Exit(code)
}

// rndTestRoot paints its colour; it counts frames and can animate (a frame every refresh).
type rndTestRoot struct {
	col     kit.Color
	h       kit.Host
	frames  int
	paints  int
	animate bool
}

func (r *rndTestRoot) Attach(h kit.Host) { r.h = h }
func (r *rndTestRoot) Detach()           {}
func (r *rndTestRoot) Frame(now time.Time) {
	r.frames++
	if r.animate {
		r.h.RequestFrame()
		r.h.Invalidate(kit.R(0, 0, 40, 40)) // a frame presented every refresh
	}
}
func (r *rndTestRoot) Paint(c kit.Canvas, dirty kit.Rect) { r.paints++; c.FillRect(dirty, r.col) }
func (r *rndTestRoot) Event(e kit.Event) bool             { return false }
func (r *rndTestRoot) FrameHit(p kit.Point) kit.FramePart { return kit.FrameClient }
func (r *rndTestRoot) Semantics() kit.SemanticTree        { return nil }

func rndWindowHelper() int {
	var mu sync.Mutex
	failed := false
	fail := func(f string, args ...any) {
		mu.Lock()
		failed = true
		mu.Unlock()
		fmt.Printf("FAIL: "+f+"\n", args...)
	}
	logf := func(f string, args ...any) { fmt.Printf("rnd: "+f+"\n", args...) }

	a, err := NewApp(AppOptions{})
	if err != nil {
		fail("NewApp: %v", err)
		return 1
	}
	red, blue := kit.RGB(0xd02020), kit.RGB(0x2040d0)
	ra, rb := &rndTestRoot{col: red}, &rndTestRoot{col: blue}
	wa, err := a.NewWindow(ra, WindowOptions{Title: "rnd A", Size: kit.Size{W: 400, H: 300}, NoActivate: true})
	if err != nil {
		fail("window A: %v", err)
		return 1
	}
	wb, err := a.NewWindow(rb, WindowOptions{Title: "rnd B", Size: kit.Size{W: 300, H: 200}, NoActivate: true})
	if err != nil {
		fail("window B: %v", err)
		return 1
	}
	// The windows may open where nobody sees them (another Space, a screen in full-screen
	// use), where they would not paint: they count as shown, except in the hidden part.
	onScreen := wa.rndVisible()
	logf("the windows are on screen: %v", onScreen)
	wa.rnd.occluded, wb.rnd.occluded = 2, 2
	do := func(f func()) {
		done := make(chan struct{})
		a.Post(func() { defer close(done); f() })
		<-done
	}
	// shows checks that the layer shows a surface of the window's whole size in pixels and
	// that the target's last frame is the root's colour up to the far corner.
	shows := func(w *Window, col kit.Color, what string) {
		pw, ph, _ := w.pixelSize()
		sw, sh := w.rndLayerSurfaceSize()
		if sw != pw || sh != ph {
			fail("%s: the layer shows %d×%d, the window is %d×%d pixels", what, sw, sh, pw, ph)
		}
		img, err := (&Offscreen{t: w.target}).RGBA()
		if err != nil {
			fail("%s: %v", what, err)
			return
		}
		for _, p := range [][2]int{{0, 0}, {pw - 1, ph - 1}, {pw / 2, ph - 1}, {pw - 1, ph / 2}} {
			c := img.RGBAAt(p[0], p[1])
			if absDiff(c.R, cv8(col.R)) > 1 || absDiff(c.B, cv8(col.B)) > 1 {
				fail("%s: pixel %v is %v", what, p, c)
			}
		}
	}

	go func() {
		defer do(func() {
			if !wb.closed {
				wb.Close()
			}
			if !wa.closed {
				wa.Close()
			}
		})
		time.Sleep(400 * time.Millisecond)
		do(func() {
			if wa.target.res != a.res || wb.target.res != a.res {
				fail("the windows do not share the application's Resources")
			}
			if wa.target == wb.target || wa.target.chain == wb.target.chain {
				fail("the windows share a target")
			}
			if wa.rnd.presented == 0 || wb.rnd.presented == 0 {
				fail("nothing presented: %d, %d", wa.rnd.presented, wb.rnd.presented)
			}
			shows(wa, red, "A at first")
			shows(wb, blue, "B at first")
		})

		// Idle: after the spare surfaces are given back, no display link runs and no frame
		// is made.
		time.Sleep(rndIdleAfter + 500*time.Millisecond)
		var fa, fb int
		do(func() {
			if wa.rndLinkRunning() || wb.rndLinkRunning() {
				fail("a display link runs while idle: %v %v", wa.rndLinkRunning(), wb.rndLinkRunning())
			}
			for i, b := range wa.target.chain.bufs {
				if i != wa.target.chain.front && b.s != 0 && !b.volatile {
					fail("A's spare surface %d is not volatile when idle", i)
				}
			}
			fa, fb = ra.frames, rb.frames
		})
		time.Sleep(time.Second)
		do(func() {
			if ra.frames != fa || rb.frames != fb {
				fail("frames while idle: A %d, B %d", ra.frames-fa, rb.frames-fb)
			}
		})

		// Animating A: about one frame per refresh, B untouched.
		var t0 time.Time
		do(func() {
			fa, fb = ra.frames, rb.frames
			ra.animate = true
			t0 = time.Now()
			wa.RequestFrame()
		})
		time.Sleep(time.Second)
		do(func() {
			ra.animate = false
			n := ra.frames - fa
			rate := float64(n) / time.Since(t0).Seconds()
			logf("animating: %d frames in %v (%.0f a second), %d presented, %d surfaces; B %d", n, time.Since(t0).Round(time.Millisecond), rate, wa.rnd.presented, wa.target.chain.surfaces(), rb.frames-fb)
			if rate < 40 {
				fail("animating at %.0f frames a second", rate)
			}
			if rb.frames != fb {
				fail("B made %d frames while A animated", rb.frames-fb)
			}
		})
		time.Sleep(200 * time.Millisecond)
		do(func() {
			if wa.rndLinkRunning() {
				fail("A's display link still runs after the animation")
			}
		})
		time.Sleep(rndIdleAfter + 300*time.Millisecond)
		do(func() {
			if n := wa.target.chain.surfaces(); n > 2 {
				fail("A keeps %d surfaces when idle after the animation", n)
			}
		})
		// Idle for longer: one surface.
		time.Sleep(rndFreeAfter)
		do(func() {
			if n := wa.target.chain.surfaces(); n != 1 {
				fail("A keeps %d surfaces after %v idle, want 1", n, rndFreeAfter+rndIdleAfter)
			}
			shows(wa, red, "A after a long idle")
		})

		// The compositor holding every spare for a frame in the same refresh as the last
		// one: the frame waits a refresh and is then made (on a third surface); its area is
		// not lost.
		do(func() {
			ch := wa.target.chain
			ch.held = func(int) bool { return true }
			ch.now = func() time.Time { return ch.lastEnd } // every frame right after the last
			// the second surface (after the long idle)
			wa.Invalidate(kit.R(0, 0, 400, 300))
		})
		time.Sleep(100 * time.Millisecond)
		do(func() {
			ra.col = blue
			wa.Invalidate(kit.R(0, 0, 400, 300))
		})
		time.Sleep(300 * time.Millisecond)
		do(func() {
			ch := wa.target.chain
			ch.held, ch.now = nil, time.Now
			if ch.waits == 0 || ch.surfaces() != 3 {
				fail("held surfaces: %d waits, %d surfaces; want a wait and a third", ch.waits, ch.surfaces())
			}
			shows(wa, blue, "A after a held surface")
			ra.col = red
			wa.Invalidate(kit.R(0, 0, 400, 300))
		})
		time.Sleep(100 * time.Millisecond)
		// The shown surface unreadable for a copy: everything is painted again, never
		// stale pixels.
		var lost int
		do(func() {
			ch := wa.target.chain
			once := true
			ch.failLock = func(i int, readonly bool) bool {
				if once && readonly && i == ch.front {
					once = false
					return true
				}
				return false
			}
			lost = ra.paints
			ra.col = blue
			wa.Invalidate(kit.R(10, 10, 20, 20))
		})
		time.Sleep(300 * time.Millisecond)
		do(func() {
			wa.target.chain.failLock = nil
			if ra.paints-lost < 1 {
				fail("nothing painted after a lock failure")
			}
			shows(wa, blue, "A after a lock failure") // all of it again, in the new colour
			ra.col = red
			wa.Invalidate(kit.R(0, 0, 400, 300))
		})
		time.Sleep(100 * time.Millisecond)

		// Hidden (under another window when the windows are on screen, else marked hidden):
		// Frame runs about once a second, nothing is painted; what was invalid is painted
		// once the window shows again.
		var cover *Window
		real := false
		do(func() {
			if !wa.rndVisible() {
				logf("hidden: A is not on screen, so it is marked hidden")
				wa.rnd.occluded = 1
				return
			}
			c, err := a.NewWindow(&rndTestRoot{col: kit.RGB(0x808080)}, WindowOptions{Title: "rnd cover", Size: kit.Size{W: 1000, H: 800}, NoActivate: true})
			if err != nil {
				fail("cover: %v", err)
				return
			}
			c.rnd.occluded = 2
			cover, real = c, true
			wa.rnd.occluded = 0
		})
		hidden := false
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline) && !hidden; {
			time.Sleep(100 * time.Millisecond)
			do(func() { hidden = wa.rndHidden() })
		}
		if !hidden {
			fail("A is not reported hidden under a window that covers it")
		} else {
			var f0, p0, h0 int
			do(func() {
				f0, p0, h0 = ra.frames, ra.paints, wa.rnd.hiddenFrames
				ra.animate = true
				ra.col = blue
				wa.Invalidate(kit.R(0, 0, 400, 300))
				wa.RequestFrame()
			})
			time.Sleep(2500 * time.Millisecond)
			do(func() {
				ra.animate = false
				n := ra.frames - f0
				logf("hidden (real %v): %d frames in 2.5 s, %d of them hidden, %d paints", real, n, wa.rnd.hiddenFrames-h0, ra.paints-p0)
				if n < 2 || n > 4 {
					fail("hidden: %d frames in 2.5 s, want about one a second", n)
				}
				if ra.paints != p0 {
					fail("hidden: %d paints", ra.paints-p0)
				}
				p0 = ra.paints
				if real {
					cover.Close()
				} else {
					wa.rnd.occluded = 2
					wa.rndShown()
				}
			})
			shown := false
			var t0 = time.Now()
			for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline) && !shown; {
				time.Sleep(20 * time.Millisecond)
				do(func() { shown = ra.paints > p0 })
			}
			if !shown {
				fail("A did not paint when it showed again")
			} else {
				logf("painted %v after it showed again", time.Since(t0).Round(10*time.Millisecond))
				do(func() { shows(wa, blue, "A shown again") })
			}
		}
		do(func() {
			if cover != nil && !cover.closed {
				cover.Close()
			}
			wa.rnd.occluded = 2
			ra.col = red
			wa.Invalidate(kit.R(0, 0, 400, 300))
		})
		time.Sleep(100 * time.Millisecond)

		// A window released without closing (NewWindow's error path) lets its display link go.
		do(func() {
			if !rndTestLinkReleased() {
				fail("the display link of a window released without closing was kept")
			}
		})

		// Resizes, growing and shrinking: when Resize returns, the layer already shows a
		// frame of the new size.
		for i := 0; i < 24; i++ {
			s := kit.Size{W: 400 + float32(i%12)*37.5, H: 300 + float32(i%7)*23.25}
			if i >= 12 {
				s = kit.Size{W: 900 - float32(i-12)*41, H: 500 - float32(i-12)*17.5}
			}
			do(func() {
				wa.Resize(s)
				// AppKit keeps frames on whole points
				if math.Abs(float64(wa.size.W-s.W)) > 1 || math.Abs(float64(wa.size.H-s.H)) > 1 {
					fail("resize %d: the window is %v, want %v", i, wa.size, s)
				}
				shows(wa, red, fmt.Sprintf("resize %d to %v", i, s))
			})
		}

		// Minimised: an invalid area is not drawn until the window comes back.
		do(func() { wa.Command(kit.WindowMinimize) })
		mini := false
		for deadline := time.Now().Add(4 * time.Second); time.Now().Before(deadline) && !mini; {
			time.Sleep(100 * time.Millisecond)
			do(func() { mini = wa.state.Minimized })
		}
		if !mini {
			fail("A did not minimise")
		} else {
			var p int
			do(func() {
				p = ra.paints
				wa.Invalidate(kit.R(0, 0, 50, 50))
			})
			time.Sleep(500 * time.Millisecond)
			do(func() {
				if ra.paints != p {
					fail("A painted %d frames while minimised", ra.paints-p)
				}
				wa.rndDeminiaturize()
			})
			back := false
			for deadline := time.Now().Add(4 * time.Second); time.Now().Before(deadline) && !back; {
				time.Sleep(100 * time.Millisecond)
				do(func() { back = !wa.state.Minimized && ra.paints > p })
			}
			if !back {
				fail("A did not paint after it came back")
			}
		}

		// Closing A keeps B and the application, and stops A's idle timer.
		do(func() {
			wa.Invalidate(kit.R(0, 0, 10, 10))
		})
		time.Sleep(100 * time.Millisecond)
		do(func() {
			wa.Close()
			if wa.rnd.idle != nil && wa.rnd.idle.Stop() {
				fail("A's idle timer was still set after it closed")
			}
		})
		time.Sleep(300 * time.Millisecond)
		var p int
		do(func() {
			if !wa.closed || wb.closed {
				fail("after closing A: A closed %v, B closed %v", wa.closed, wb.closed)
			}
			if len(a.windows) != 1 {
				fail("%d windows left", len(a.windows))
			}
			p = rb.paints
			wb.Invalidate(kit.R(0, 0, 300, 200))
		})
		time.Sleep(300 * time.Millisecond)
		do(func() {
			if rb.paints == p {
				fail("B did not paint after A closed")
			}
			shows(wb, blue, "B after A closed")
		})
	}()
	go func() { // a hung run loop must not hang the test
		time.Sleep(60 * time.Second)
		fmt.Println("FAIL: the helper timed out")
		os.Exit(3)
	}()
	if err := a.Run(); err != nil {
		fail("Run: %v", err)
	}
	a.Close()
	fmt.Println("rnd: done")
	if failed {
		return 1
	}
	return 0
}
