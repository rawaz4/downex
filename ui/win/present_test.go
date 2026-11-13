//go:build windows

package win

import (
	"fmt"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/win/internal/w32"
)

// edgeRoot is testRoot with a strip along the right edge of the window, so that a frame
// made for one width can be told from a frame made for another.
type edgeRoot struct{ testRoot }

var edgeColor = kit.RGB(0xd0306f)

func (r *edgeRoot) Paint(c kit.Canvas, dirty kit.Rect) {
	r.testRoot.Paint(c, dirty)
	s := r.host.Size()
	// Rows like the main window's list, so that a frame is about as much work for the GPU.
	for y := kit.DIP(80); y < s.H; y += 36 {
		box := kit.RoundRect(kit.R(16, y, s.W-40, y+32), 6)
		c.DrawShadow(box, kit.Shadow{Offset: kit.Pt(0, 4), Blur: 24, Color: kit.Color{A: 0.3}})
		c.FillRoundedRect(box, kit.RGB(0xdfe6e2))
		for x := kit.DIP(24); x < s.W-120; x += 110 {
			c.DrawText(r.label, kit.Pt(x, y+6), kit.RGB(0x0f1613))
		}
	}
	c.FillRect(kit.R(s.W-24, 0, s.W, s.H), edgeColor)
}

// openEdge is openTest for an edgeRoot.
func openEdge(t *testing.T, root *edgeRoot, opts WindowOptions) (*App, *Window) {
	t.Helper()
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)
	a, err := NewApp(AppOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	root.t = t
	mon := w32.MonitorFromPoint(w32.Point{}, w32.MONITOR_DEFAULTTOPRIMARY)
	mi, _ := w32.GetMonitorInfo(mon)
	opts.At, opts.Placed, opts.NoActivate = w32.Point{X: mi.Work.Left + 40, Y: mi.Work.Top + 40}, true, true
	opts.Size = kit.Size{W: 1200, H: 760}
	w, err := a.NewWindow(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	return a, w
}

var (
	gdi32        = syscall.NewLazyDLL("gdi32.dll")
	procGetPixel = gdi32.NewProc("GetPixel")
)

// screenPixel is the composed screen's colour at p (physical pixels), as 0x00BBGGRR.
func screenPixel(p w32.Point) uint32 {
	dc := w32.GetDC(0)
	defer w32.ReleaseDC(0, dc)
	r, _, _ := syscall.SyscallN(procGetPixel.Addr(), uintptr(dc), uintptr(p.X), uintptr(p.Y))
	return uint32(r)
}

func near(c uint32, k kit.Color) bool {
	d := func(v uint32, f float32) bool {
		x := int(v&0xff) - int(f*255+0.5)
		return x > -24 && x < 24
	}
	return d(c, k.R) && d(c>>8, k.G) && d(c>>16, k.B)
}

// A resize of a shown window paints the new size and waits for the compositor before
// SetWindowPos returns, so that the first frame composed after it already is the new one:
// the screen shows the right-edge strip at the new edge. The GPU target waits once per
// size change, for the GPU and then the compositor; the software one draws through GDI and
// never waits. This is the flicker
// gate's step test in small (grow and shrink by 40 × 24 px from another thread, one
// DwmFlush, a copy of the screen). Its scene is lighter than the preview's: without the
// wait it still passes on the RTX 4070 it was written on, so it guards the outcome and the
// wait, and the gate's script on the preview is what shows the lag (51 to 57 of 80 steps
// without the wait, 0 with it).
func TestResizeShowsTheNewFrame(t *testing.T) {
	for _, sw := range []bool{false, true} {
		name := "hardware"
		if sw {
			name = "software"
		}
		t.Run(name, func(t *testing.T) {
			root := &edgeRoot{}
			a, w := openEdge(t, root, WindowOptions{Software: sw, CustomFrame: true})
			pump(t, a, w, "the first frame", 2*time.Second, func() bool { return w.painted })
			// A native field that moves with the size, as the title bar's search field does.
			f, err := w.NewTextField(kit.TextFieldOptions{Style: body, Color: kit.RGB(0x0f1613), Background: kit.RGB(0xffffff),
				Placeholder: "Search downloads", PlaceholderColor: kit.RGB(0x56625c)}, kit.TextFieldCallbacks{})
			if err != nil {
				t.Fatal(err)
			}
			defer f.Release()
			place := func(s kit.Size) { f.SetBounds(kit.R(s.W/2-200, 12, s.W/2+200, 40)) }
			place(w.Size())
			root.onEvent = func(e kit.Event) bool {
				if se, ok := e.(kit.SizeEvent); ok {
					place(se.Size)
				}
				return false
			}
			if got := w.Renderer(); got != name {
				skipOrRequire(t, "renderer is "+got+", not "+name)
			}
			// On top of the others, without activation, so that the screen shows it.
			w32.SetWindowPos(w.hwnd, 0, 0, 0, 0, w32.SWP_NOMOVE|w32.SWP_NOSIZE|w32.SWP_NOACTIVATE)
			const steps = 20
			waits, gpuWaits, paints := w.target.composeWaits, w.target.gpuWaits, len(root.paints)
			// The steps come from another thread, as the system's sizing and the gate's
			// script send them, while this one runs the window's loop.
			h, edge := w.hwnd, int32(12*w.ppd())
			var done atomic.Bool
			var stale int
			var covered bool
			var log []string
			go func() {
				runtime.LockOSThread()
				defer runtime.UnlockOSThread()
				defer done.Store(true)
				wr := w32.GetWindowRect(h)
				for i := 0; i < steps; i++ {
					dx, dy := int32(40), int32(24)
					if i%2 == 1 {
						dx, dy = 0, 0
					}
					w32.SetWindowPos(h, 0, 0, wr.Right-wr.Left+dx, wr.Bottom-wr.Top+dy, w32.SWP_NOMOVE|w32.SWP_NOZORDER|w32.SWP_NOACTIVATE)
					w32.DwmFlush()
					rc := w32.GetClientRect(h)
					p := w32.ClientToScreen(h, w32.Point{X: rc.Right - edge, Y: rc.Bottom / 2})
					if i == 0 && w32.GetAncestor(w32.WindowFromPoint(p), w32.GA_ROOT) != h {
						covered = true
						return
					}
					if c := screenPixel(p); !near(c, edgeColor) {
						stale++
						log = append(log, fmt.Sprintf("step %d: the first composed frame shows %06x at the new right edge, not the strip", i, c))
					}
				}
			}()
			pump(t, a, w, "the resize steps", 20*time.Second, done.Load)
			for _, l := range log {
				t.Log(l)
			}
			if covered {
				skipOrRequire(t, "another window covers the test window: the screen cannot show its frames")
			}
			if got := len(root.paints) - paints; got < steps {
				t.Errorf("%d size changes made %d paints, want one each at least", steps, got)
			}
			waited := w.target.composeWaits - waits
			if sw && waited != 0 {
				t.Errorf("the software target waited for the compositor %d times, want 0", waited)
			}
			if !sw && waited != steps {
				t.Errorf("the GPU target waited for the compositor %d times in %d size changes", waited, steps)
			}
			if got := w.target.gpuWaits - gpuWaits; !sw && got != steps {
				t.Errorf("the GPU target waited for the GPU %d times in %d size changes (last error: %v)", got, steps, w.target.syncErr)
			}
			// The gate allows 2 of 80; measured 0 of 80 on both monitors. A loaded PC may
			// miss a refresh now and then.
			if stale > 2 {
				t.Errorf("%d of %d resize steps first showed the previous frame", stale, steps)
			}
			t.Logf("%s: %d of %d first composed frames showed the previous frame", name, stale, steps)
		})
	}
}
