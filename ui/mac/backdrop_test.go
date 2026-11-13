//go:build darwin && cgo

package mac

import (
	"math"
	"testing"
	"time"

	"downex/ui/kit"
)

// BlurBackdrop (Dialog.svelte's backdrop-filter: blur(1.5px)): a sharp black and white edge
// inside the area comes out as a Gaussian of σ 1.5 DIP; pixels outside the area, or outside
// a clip, stay as drawn; opacity blends the blurred copy over the sharp one; and it draws
// nothing under a layer (ui/win's TestBlurBackdrop).
func TestBlurBackdrop(t *testing.T) {
	res := cvRes(t)
	white, black := kit.RGBA(255, 255, 255, 1), kit.RGBA(0, 0, 0, 1)
	for _, ppd := range []float32{1, 1.25, 2} {
		o := cvOff(t, res, int(120*ppd), int(60*ppd), ppd)
		edge := func(c kit.Canvas) {
			c.FillRect(kit.R(0, 0, 120, 60), white)
			c.FillRect(kit.R(60, 0, 120, 60), black) // the edge at x = 60 DIPs
		}
		at := func(x, y int) float64 { return float64(cvPix(t, o).RGBAAt(x, y).R) }
		px := func(dip float32) int { return int(math.Round(float64(dip * ppd))) }

		cvFrame(t, o, func(c kit.Canvas) {
			edge(c)
			c.(kit.BackdropFilter).BlurBackdrop(kit.R(20, 10, 100, 50), 1.5, 1)
		})
		img := cvPix(t, o)
		y := px(30)
		e := px(60)
		left, right := float64(img.RGBAAt(e-1, y).R), float64(img.RGBAAt(e, y).R)
		t.Logf("ppd %v: edge pixels %v | %v", ppd, left, right)
		if left > 235 || right < 20 || left < 128 || right > 128 {
			t.Errorf("ppd %v: edge pixels %v | %v, want blurred about the edge (sharp is 255 | 0)", ppd, left, right)
		}
		// The Gaussian's integral one σ (1.5 DIPs) inside the white side: Φ(1) ≈ 0.841.
		if v := float64(img.RGBAAt(e-px(1.5), y).R); math.Abs(v-255*0.841) > 14 {
			t.Errorf("ppd %v: one σ before the edge reads %v, want about %v", ppd, v, 255*0.841)
		}
		if v := float64(img.RGBAAt(e-px(6), y).R); v < 250 {
			t.Errorf("ppd %v: 6 DIPs before the edge reads %v, want white (the blur reaches about 4.5 DIPs)", ppd, v)
		}
		// Outside the area: sharp.
		if l, r := float64(img.RGBAAt(e-1, px(5)).R), float64(img.RGBAAt(e, px(5)).R); l != 255 || r != 0 {
			t.Errorf("ppd %v: above the area the edge reads %v | %v, want 255 | 0", ppd, l, r)
		}
		// The area's own edge fades (a hard border: outside it is transparent), so the
		// sharp pixels under it show through: the white side stays white at the left edge.
		if v := float64(img.RGBAAt(px(20), y).R); v < 250 {
			t.Errorf("ppd %v: at the area's left edge %v, want white", ppd, v)
		}
		// Inside a clip that leaves out the edge's lower half: the lower half stays sharp.
		cvFrame(t, o, func(c kit.Canvas) {
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
		// The clip stays in effect after the blur.
		cvFrame(t, o, func(c kit.Canvas) {
			edge(c)
			c.PushClip(kit.R(0, 0, 30, 60))
			c.(kit.BackdropFilter).BlurBackdrop(kit.R(0, 0, 120, 60), 1.5, 1)
			c.FillRect(kit.R(0, 0, 120, 60), black)
			c.Pop()
		})
		if v := at(px(50), px(30)); v != 255 {
			t.Errorf("ppd %v: outside the clip after the blur reads %v, want 255", ppd, v)
		}
		// Half opacity: half way between the sharp and the blurred values.
		cvFrame(t, o, func(c kit.Canvas) {
			edge(c)
			c.(kit.BackdropFilter).BlurBackdrop(kit.R(20, 10, 100, 50), 1.5, 0.5)
		})
		if l := at(e-1, y); math.Abs(l-(255+left)/2) > 3 {
			t.Errorf("ppd %v: at half opacity the edge's white side reads %v, want about %v", ppd, l, (255+left)/2)
		}
		// Under a layer or a rounded clip: nothing.
		for _, push := range []func(c kit.Canvas){
			func(c kit.Canvas) { c.PushOpacity(0.5) },
			func(c kit.Canvas) { c.PushRoundedClip(kit.RoundRect(kit.R(0, 0, 120, 60), 4)) },
		} {
			cvFrame(t, o, func(c kit.Canvas) {
				edge(c)
				push(c)
				c.(kit.BackdropFilter).BlurBackdrop(kit.R(20, 10, 100, 50), 1.5, 1)
				c.Pop()
			})
			if l, r := at(e-1, y), at(e, y); l != 255 || r != 0 {
				t.Errorf("ppd %v: under a layer the edge reads %v | %v, want sharp", ppd, l, r)
			}
		}
		// A partial frame blurs only inside its dirty area.
		cvFrame(t, o, edge)
		c, err := o.BeginFrame(kit.R(0, 0, 120, 20))
		if err != nil {
			t.Fatal(err)
		}
		edge(c)
		c.(kit.BackdropFilter).BlurBackdrop(kit.R(0, 0, 120, 60), 1.5, 1)
		if err := o.EndFrame(); err != nil {
			t.Fatal(err)
		}
		if l, r := at(e-1, px(40)), at(e, px(40)); l != 255 || r != 0 {
			t.Errorf("ppd %v: outside the dirty area the edge reads %v | %v, want sharp", ppd, l, r)
		}
		if l := at(e-1, px(10)); l > 235 {
			t.Errorf("ppd %v: inside the dirty area the edge reads %v, want blurred", ppd, l)
		}
	}
}

// What a full frame of the main window costs at Retina size (1200 × 760 DIPs at 2×): a
// fill of the whole target, and the dialog backdrop's blur over all of it (the frame of a
// dialog's fade). Logged, not judged: the numbers go to the report.
func TestMeasureFillAndBlur(t *testing.T) {
	if testing.Short() {
		t.Skip("a measurement")
	}
	res := cvRes(t)
	o := cvOff(t, res, 2400, 1520, 2)
	scene := func(c kit.Canvas) {
		c.FillRect(o.Bounds(), cvSurface)
		c.FillRect(kit.R(0, 0, 220, 760), kit.RGB(0xf4f7f5))
		c.FillRoundedRect(kit.RoundRect(kit.R(240, 60, 1180, 740), 12), kit.RGB(0xfbfcfb))
	}
	measure := func(name string, f func(c kit.Canvas)) time.Duration {
		cvFrame(t, o, f) // warm
		const n = 10
		best := time.Duration(math.MaxInt64)
		var sum time.Duration
		for range n {
			start := time.Now()
			cvFrame(t, o, f)
			d := time.Since(start)
			sum += d
			best = min(best, d)
		}
		t.Logf("%s: mean %.2f ms, best %.2f ms", name, float64(sum)/n/1e6, float64(best)/1e6)
		return sum / n
	}
	fill := measure("fill 1200×760 at 2×", func(c kit.Canvas) { c.FillRect(o.Bounds(), cvSurface) })
	sceneT := measure("three fills (a window's ground)", scene)
	blur := measure("ground + blur(1.5px) over all of it", func(c kit.Canvas) {
		scene(c)
		c.(kit.BackdropFilter).BlurBackdrop(o.Bounds(), 1.5, 0.7)
	})
	t.Logf("blur alone ≈ %.2f ms (fill %.2f ms)", float64(blur-sceneT)/1e6, float64(fill)/1e6)
}
