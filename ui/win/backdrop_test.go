//go:build windows

package win

import (
	"math"
	"runtime"
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/win/internal/w32"
)

// BlurBackdrop (Dialog.svelte's backdrop-filter: blur(1.5px)): a sharp black and white edge
// inside the area comes out as a Gaussian of σ 1.5 DIP; pixels outside the area, or outside
// a clip, stay as drawn; opacity blends the blurred copy over the sharp one; and it draws
// nothing under a layer.
func TestBlurBackdrop(t *testing.T) {
	res, err := NewResources()
	if err != nil {
		t.Skip(err)
	}
	defer res.Close()
	white, black := kit.RGBA(255, 255, 255, 1), kit.RGBA(0, 0, 0, 1)
	for _, ppd := range []float32{1, 1.25} {
		o := newOffscreen(t, res, 120, 60, ppd)
		edge := func(c kit.Canvas) {
			c.FillRect(kit.R(0, 0, 120, 60), white)
			c.FillRect(kit.R(60, 0, 120, 60), black) // the edge at x = 60 DIPs
		}
		// the value of pixel x (device pixels) on row y
		at := func(x, y int) float64 {
			img := pixels(t, o)
			return float64(img.RGBAAt(x, y).R)
		}
		px := func(dip float32) int { return int(math.Round(float64(dip * ppd))) }

		frame(t, o, func(c kit.Canvas) {
			edge(c)
			c.(kit.BackdropFilter).BlurBackdrop(kit.R(20, 10, 100, 50), 1.5, 1)
		})
		img := pixels(t, o)
		y := px(30)
		// The edge pixels follow the Gaussian's integral: the last white pixel before the
		// edge and the first black one meet in the middle.
		e := px(60)
		left, right := float64(img.RGBAAt(e-1, y).R), float64(img.RGBAAt(e, y).R)
		t.Logf("ppd %v: edge pixels %v | %v", ppd, left, right)
		if left > 235 || right < 20 || left < 128 || right > 128 {
			t.Errorf("ppd %v: edge pixels %v | %v, want blurred about the edge (sharp is 255 | 0)", ppd, left, right)
		}
		if v := float64(img.RGBAAt(e-px(6), y).R); v < 250 {
			t.Errorf("ppd %v: 6 DIPs before the edge reads %v, want white (the blur reaches about 4.5 DIPs)", ppd, v)
		}
		// Outside the area: sharp.
		if l, r := float64(img.RGBAAt(e-1, px(5)).R), float64(img.RGBAAt(e, px(5)).R); l != 255 || r != 0 {
			t.Errorf("ppd %v: above the area the edge reads %v | %v, want 255 | 0", ppd, l, r)
		}
		// Inside a clip that leaves out the edge's lower half: the lower half stays sharp.
		frame(t, o, func(c kit.Canvas) {
			edge(c)
			c.PushTransform(kit.Translation(0, 10))
			c.PushClip(kit.R(0, 0, 120, 20)) // window rows 10 to 30
			c.(kit.BackdropFilter).BlurBackdrop(kit.R(20, 0, 100, 40), 1.5, 1)
			c.Pop()
			c.Pop()
		})
		if l := at(e-1, px(20)); l > 235 {
			t.Errorf("ppd %v: inside the clip the edge's white side reads %v, want blurred", ppd, l)
		}
		if l, r := at(e-1, px(40)), at(e, px(40)); l != 255 || r != 0 {
			t.Errorf("ppd %v: below the clip the edge reads %v | %v, want sharp", ppd, l, r)
		}
		// The clips were pushed again: what follows is still clipped.
		frame(t, o, func(c kit.Canvas) {
			edge(c)
			c.PushClip(kit.R(0, 0, 30, 60))
			c.(kit.BackdropFilter).BlurBackdrop(kit.R(0, 0, 120, 60), 1.5, 1)
			c.FillRect(kit.R(0, 0, 120, 60), black)
			c.Pop()
		})
		if v := at(px(50), px(30)); v != 255 {
			t.Errorf("ppd %v: outside the clip after the blur reads %v, want 255 (the clip pushed again)", ppd, v)
		}
		// Half opacity: half way between the sharp and the blurred values.
		frame(t, o, func(c kit.Canvas) {
			edge(c)
			c.(kit.BackdropFilter).BlurBackdrop(kit.R(20, 10, 100, 50), 1.5, 0.5)
		})
		if l := at(e-1, y); math.Abs(l-(255+left)/2) > 3 {
			t.Errorf("ppd %v: at half opacity the edge's white side reads %v, want about %v", ppd, l, (255+left)/2)
		}
		// Under a layer: nothing.
		frame(t, o, func(c kit.Canvas) {
			edge(c)
			c.PushOpacity(0.5)
			c.(kit.BackdropFilter).BlurBackdrop(kit.R(20, 10, 100, 50), 1.5, 1)
			c.Pop()
		})
		if l, r := at(e-1, y), at(e, y); l != 255 || r != 0 {
			t.Errorf("ppd %v: under a layer the edge reads %v | %v, want sharp", ppd, l, r)
		}
		// Steady frames allocate nothing.
		allocs := testing.AllocsPerRun(20, func() {
			c, err := o.BeginFrame(o.Bounds())
			if err != nil {
				t.Fatal(err)
			}
			edge(c)
			c.(kit.BackdropFilter).BlurBackdrop(kit.R(20, 10, 100, 50), 1.5, 0.75)
			if err := o.EndFrame(); err != nil {
				t.Fatal(err)
			}
		})
		if allocs != 0 {
			t.Errorf("ppd %v: %v allocations per frame, want 0", ppd, allocs)
		}
	}
}

// blurRoot paints edgeRoot's frame, then blurs a band across its rows as a dialog's
// backdrop does, and counts its frames.
type blurRoot struct {
	edgeRoot
	blurs int
}

func (r *blurRoot) Paint(c kit.Canvas, dirty kit.Rect) {
	r.edgeRoot.Paint(c, dirty)
	if f, ok := c.(kit.BackdropFilter); ok {
		f.BlurBackdrop(kit.R(0, 44, r.host.Size().W, r.host.Size().H), 1.5, 1)
		r.blurs++
	}
}

// The blur on the real window targets (the GPU's and the software one): the frames end
// without an error and the device is kept (no loss, one target made), a partial repaint
// blurs again, and where the screen shows the window the strip's sharp edge is blurred.
func TestBlurBackdropInAWindow(t *testing.T) {
	for _, sw := range []bool{false, true} {
		name := "hardware"
		if sw {
			name = "software"
		}
		t.Run(name, func(t *testing.T) {
			root := &blurRoot{}
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
			w, err := a.NewWindow(root, WindowOptions{Software: sw, CustomFrame: true, Placed: true, NoActivate: true,
				At: w32.Point{X: mi.Work.Left + 40, Y: mi.Work.Top + 40}, Size: kit.Size{W: 800, H: 500}})
			if err != nil {
				t.Fatal(err)
			}
			pump(t, a, w, "a blurred frame", 2*time.Second, func() bool { return root.blurs > 0 && w.painted })
			if got := w.Renderer(); got != name {
				skipOrRequire(t, "renderer is "+got+", not "+name)
			}
			w.Invalidate(kit.R(300, 300, 340, 340)) // a partial repaint
			n := root.blurs
			pump(t, a, w, "a partial repaint", 2*time.Second, func() bool { return root.blurs > n })
			if err := w.target.lastErr; err != nil {
				t.Errorf("frame error: %v", err)
			}
			if d := w.target.dev; d.losses != 0 || d.targetsMade != 1 || d.backdrop.failed || d.backdrop.out == nil {
				t.Errorf("device: %d losses, %d targets made, blur failed %v, output %v", d.losses, d.targetsMade, d.backdrop.failed, d.backdrop.out != nil)
			}
			img, err := capture(w)
			if err != nil {
				t.Fatal(err)
			}
			// The strip's left edge (24 DIPs from the right), on the band's last row.
			ppd := w.ppd()
			x, y := img.Bounds().Dx()-int(math.Round(float64(24*ppd))), img.Bounds().Dy()-int(8*ppd)
			before, inside, far := img.RGBAAt(x-1, y), img.RGBAAt(x+int(4*ppd), y), img.RGBAAt(x-int(8*ppd), y)
			between := func(v, a, b uint8) bool { return v > min(a, b) && v < max(a, b) }
			if !between(before.R, far.R, inside.R) || !between(before.G, far.G, inside.G) || !between(before.B, far.B, inside.B) {
				t.Errorf("%s: the pixel before the strip %v is not between the background %v and the strip %v: not blurred", name, before, far, inside)
			}
			// The blurred copy keeps its colours (on the GPU an unset alpha once drew it about
			// twice too bright): 4 DIPs into the 24-DIP strip is the strip's colour.
			if k := edgeColor; math.Abs(float64(inside.R)-float64(k.R*255)) > 3 || math.Abs(float64(inside.G)-float64(k.G*255)) > 3 || math.Abs(float64(inside.B)-float64(k.B*255)) > 3 {
				t.Errorf("%s: inside the strip %v, want the strip's colour %v", name, inside, k)
			}
			t.Logf("%s: background %v, before the strip %v, inside %v; (40,20) %v, (40,y) %v, size %v", name, far, before, inside, img.RGBAAt(40, 20), img.RGBAAt(40, y), img.Bounds())
		})
	}
}
