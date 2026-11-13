//go:build darwin && cgo

package mac

import (
	"bytes"
	"encoding/binary"
	"errors"
	stdimage "image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"

	"downex/ui/kit"
)

// The helpers are prefixed cv so that they never meet the text part's test helpers.

// cvShots is where tests write their PNGs: $DOWNEX_MAC_SHOTS when set, else a temporary
// folder.
func cvShots(t testing.TB) string {
	if d := os.Getenv("DOWNEX_MAC_SHOTS"); d != "" {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		return d
	}
	return t.TempDir()
}

func cvRes(t testing.TB) *Resources {
	t.Helper()
	r, err := NewResources()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	return r
}

func cvOff(t testing.TB, r *Resources, w, h int, ppd float32) *Offscreen {
	t.Helper()
	o, err := NewOffscreen(r, w, h, ppd, OffscreenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(o.Close)
	return o
}

// cvFrame draws one whole frame of o with paint.
func cvFrame(t testing.TB, o *Offscreen, paint func(c kit.Canvas)) {
	t.Helper()
	c, err := o.BeginFrame(o.Bounds())
	if err != nil {
		t.Fatal(err)
	}
	paint(c)
	if err := o.EndFrame(); err != nil {
		t.Fatal(err)
	}
}

func cvPix(t testing.TB, o *Offscreen) *stdimage.RGBA {
	t.Helper()
	img, err := o.RGBA()
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// Studio tokens (app.css, light theme) used by the scenes.
var (
	cvSurface = kit.RGB(0xffffff)
	cvInk     = kit.RGB(0x0f1613)
	cvAccent  = kit.RGB(0x1b7f4c)
)

func cv8(v float32) uint8 { return uint8(min(max(v, 0), 1)*255 + 0.5) }

func absDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

// cvNear checks an opaque pixel against want within tol per channel.
func cvNear(t *testing.T, name string, got color.RGBA, want kit.Color, tol int) {
	t.Helper()
	w := color.RGBA{cv8(want.R), cv8(want.G), cv8(want.B), 255}
	if absDiff(got.R, w.R) > tol || absDiff(got.G, w.G) > tol || absDiff(got.B, w.B) > tol {
		t.Errorf("%s: pixel %v, want %v (±%d)", name, got, w, tol)
	}
}

// Every primitive draws where it is asked to, in device pixels at 125% and at 200%
// (Retina): ui/win's TestCanvasPrimitives without the text.
func TestCanvasPrimitives(t *testing.T) {
	r := cvRes(t)
	icon, err := r.NewSVGPath("M5 12.5l4.5 4.5L19 7.5", kit.FillNonZero) // Icon.svelte's check
	if err != nil {
		t.Fatal(err)
	}
	defer icon.Release()
	tri, err := r.NewSVGPath("M0 0h20v20z", kit.FillNonZero)
	if err != nil {
		t.Fatal(err)
	}
	defer tri.Release()
	red, blue, green := kit.RGB(0xd03030), kit.RGB(0x3050d0), kit.RGB(0x20a050)
	for _, ppd := range []float32{1.25, 2} {
		o := cvOff(t, r, int(400*ppd), int(300*ppd), ppd)
		cvFrame(t, o, func(c kit.Canvas) {
			c.FillRect(o.Bounds(), cvSurface)
			c.FillRect(kit.R(10, 10, 50, 50), red)
			c.StrokeRect(kit.R(60, 10, 100, 50).Inset(kit.UniformInsets(1)), kit.Stroke{Width: 2}, blue)
			c.FillRoundedRect(kit.RoundRect(kit.R(110, 10, 150, 50), 999), green) // a pill: radii fit
			c.FillRoundedRect(kit.RRect{Rect: kit.R(160, 10, 200, 50), Radii: kit.Radii{TopLeft: 20}}, red)
			c.StrokeRoundedRect(kit.RoundRect(kit.R(210, 10, 250, 50), 8).Inset(1), kit.Stroke{Width: 2}, blue)
			c.FillEllipse(kit.R(260, 10, 300, 50), green)
			c.StrokeEllipse(kit.R(310, 10, 350, 50), kit.Stroke{Width: 3}, red)
			c.DrawLine(kit.Pt(360, 30), kit.Pt(390, 30), kit.Stroke{Width: 4, Cap: kit.CapRound}, blue)
			c.DrawLine(kit.Pt(10, 70), kit.Pt(190, 70), kit.Stroke{Width: 2, Dash: kit.Dash{On: 3, Off: 3}}, cvInk)

			c.PushTransform(kit.Scaling(2, 2).Then(kit.Translation(10, 80))) // a 24-unit icon at 48 DIPs
			c.StrokePath(icon, kit.Stroke{Width: 1.6, Cap: kit.CapRound, Join: kit.JoinRound}, cvAccent)
			c.Pop()
			c.PushTransform(kit.Translation(70, 85))
			c.FillPath(tri, red)
			c.Pop()

			c.PushClip(kit.R(110, 80, 130, 120))
			c.FillRect(kit.R(100, 80, 150, 120), blue) // only 110..130 shows
			c.Pop()
			c.PushRoundedClip(kit.RoundRect(kit.R(160, 80, 200, 120), 20))
			c.FillRect(kit.R(150, 70, 210, 130), green) // a circle shows
			c.Pop()
			c.PushOpacity(0.5)
			c.FillRect(kit.R(210, 80, 250, 120), cvInk)
			c.FillRect(kit.R(210, 80, 250, 120), cvInk) // one layer: no double blend
			c.Pop()
			c.FillGradient(kit.R(260, 80, 390, 120), kit.LinearGradient{Start: kit.Pt(260, 0), End: kit.Pt(390, 0),
				Stops: []kit.GradientStop{{Offset: 0, Color: red}, {Offset: 1, Color: blue}}})

			c.DrawShadow(kit.RoundRect(kit.R(20, 160, 120, 220), 12), kit.Shadow{Offset: kit.Pt(0, 4), Blur: 12, Color: kit.RGBA(16, 28, 22, 0.5)})
			c.FillRoundedRect(kit.RoundRect(kit.R(20, 160, 120, 220), 12), cvSurface)
			// app.css's focus ring, 0 0 0 1px --focus over 0 0 0 4px --accent-soft
			c.DrawShadow(kit.RoundRect(kit.R(152, 160, 248, 220), 8), kit.Shadow{Spread: 4, Color: kit.RGBA(27, 127, 76, 0.4)})
			c.DrawShadow(kit.RoundRect(kit.R(152, 160, 248, 220), 8), kit.Shadow{Spread: 1, Color: cvAccent})
			c.DrawShadow(kit.RoundRect(kit.R(280, 160, 380, 220), 8), kit.Shadow{Spread: 1, Inset: true, Color: blue})
		})
		if err := o.WritePNG(filepath.Join(cvShots(t), "primitives@"+ppdName(ppd)+".png")); err != nil {
			t.Fatal(err)
		}
		img := cvPix(t, o)
		at := func(x, y float32) color.RGBA { return img.RGBAAt(int(x*ppd), int(y*ppd)) }
		near := func(name string, x, y float32, want kit.Color, tol int) {
			t.Helper()
			cvNear(t, ppdName(ppd)+" "+name, at(x, y), want, tol)
		}
		white := cvSurface
		near("fill", 30, 30, red, 0)
		near("outside the fill", 55, 30, white, 0)
		near("stroke", 61, 30, blue, 2)
		near("inside the stroke", 80, 30, white, 2)
		near("pill", 130, 30, green, 2)
		near("pill's rounded-off corner", 111, 11, white, 8)
		near("one round corner", 161, 11, white, 8)
		near("square corner", 199, 11, red, 2)
		near("ellipse", 280, 30, green, 2)
		near("ellipse corner", 261, 11, white, 8)
		near("stroked ellipse's ring", 310, 30, red, 10)
		near("inside the stroked ellipse", 330, 30, white, 2)
		near("round cap beyond the line's end", 391, 30, blue, 40)
		near("dash", 11.5, 70, cvInk, 10)
		near("gap", 14.5, 70, white, 10)
		near("clip keeps", 120, 100, blue, 2)
		near("clip cuts", 105, 100, white, 2)
		near("rounded clip keeps", 180, 100, green, 2)
		near("rounded clip cuts", 162, 82, white, 8)
		near("opacity 0.5", 230, 100, kit.Mix(cvInk, 0.5, white), 2)
		near("gradient start", 261, 100, red, 12)
		near("gradient end", 389, 100, blue, 12)
		near("gradient middle", 325, 100, kit.Mix(red, 0.5, blue), 3)
		near("transformed path", 75, 88, red, 4) // the triangle's upper-right half
		near("transformed path's other half", 72, 102, white, 8)
		near("icon stroke", 10+2*9.5, 80+2*17, cvAccent, 60)
		near("shadow below the card", 70, 226, kit.Mix(kit.RGBA(16, 28, 22, 1), 0.2, white), 40)
		near("card over its shadow", 70, 190, white, 1)
		near("focus ring, 1 px", 151.6, 190, cvAccent, 3)
		near("focus ring's soft spread", 149, 190, kit.Mix(kit.RGB(0x1b7f4c), 0.4, white), 3)
		near("inside the ring", 153, 190, white, 2)
		near("inset hairline", 280.4, 190, blue, 30)
		near("inside the inset hairline", 282, 190, white, 2)
	}
}

func ppdName(ppd float32) string {
	switch ppd {
	case 1:
		return "100"
	case 1.25:
		return "125"
	case 2:
		return "200"
	}
	return "x"
}

// Colours are sRGB and reach the bitmap unconverted; source-over blends in gamma-encoded
// sRGB (as WebKit and CSS do); an edge at half a device pixel covers half of it.
func TestCanvasColourAndCoverage(t *testing.T) {
	r := cvRes(t)
	o := cvOff(t, r, 40, 20, 1)
	cvFrame(t, o, func(c kit.Canvas) {
		c.FillRect(o.Bounds(), cvSurface)
		c.FillRect(kit.R(0, 0, 10, 10), kit.RGB(0x336699))
		c.FillRect(kit.R(10, 0, 20, 10), kit.RGBA(0x20, 0x60, 0xa0, 0.5))
		c.FillRect(kit.R(20.5, 0, 30, 10), cvInk) // pixel 20 half covered
		c.FillRect(kit.R(30, 0, 40, 10), kit.Color{R: 1, A: 0})
	})
	img := cvPix(t, o)
	if p := img.RGBAAt(5, 5); p != (color.RGBA{0x33, 0x66, 0x99, 255}) {
		t.Errorf("#336699 drew %v", p)
	}
	cvNear(t, "half alpha over white", img.RGBAAt(15, 5), kit.Mix(kit.RGB(0x2060a0), 0.5, cvSurface), 1)
	cvNear(t, "half-covered pixel", img.RGBAAt(20, 5), kit.Mix(cvInk, 0.5, cvSurface), 2)
	cvNear(t, "fully covered", img.RGBAAt(21, 5), cvInk, 0)
	cvNear(t, "transparent fill", img.RGBAAt(35, 5), cvSurface, 0)
	// Both rows below were not drawn on: the opaque target starts black.
	if p := img.RGBAAt(5, 15); p != (color.RGBA{255, 255, 255, 255}) {
		t.Errorf("the background fill: %v", p)
	}

	// A transparent target holds premultiplied alpha.
	tr, err := NewOffscreen(r, 4, 4, 1, OffscreenOptions{Transparent: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	cvFrame(t, tr, func(c kit.Canvas) { c.FillRect(kit.R(0, 0, 2, 4), kit.RGBA(255, 0, 0, 0.5)) })
	img = cvPix(t, tr)
	if p := img.RGBAAt(0, 0); absDiff(p.R, 128) > 1 || p.G != 0 || absDiff(p.A, 128) > 1 {
		t.Errorf("half red on transparent: %v, want premultiplied (128, 0, 0, 128)", p)
	}
	if p := img.RGBAAt(3, 0); p.A != 0 {
		t.Errorf("unpainted pixel of a transparent target: %v", p)
	}
	// A popup's target starts every frame transparent inside its area.
	tr.t.ClearFrames = true
	c, _ := tr.BeginFrame(kit.R(0, 0, 1, 4))
	c.FillRect(kit.R(0, 0, 4, 4), kit.RGBA(0, 0, 255, 0.5))
	if err := tr.EndFrame(); err != nil {
		t.Fatal(err)
	}
	img = cvPix(t, tr)
	if p := img.RGBAAt(0, 0); p.R != 0 || absDiff(p.B, 128) > 1 || absDiff(p.A, 128) > 1 {
		t.Errorf("a cleared frame's pixel: %v, want only the new half blue", p)
	}
	if p := img.RGBAAt(1, 0); absDiff(p.R, 128) > 1 {
		t.Errorf("outside the cleared area: %v, want the previous half red", p)
	}
}

// Only the dirty area, rounded out to whole device pixels, is redrawn; the rest keeps the
// previous frame, through any number of frames.
func TestCanvasDirtyKeepsTheRest(t *testing.T) {
	r := cvRes(t)
	o := cvOff(t, r, 80, 60, 2) // 40 × 30 DIPs
	cvFrame(t, o, func(c kit.Canvas) { c.FillRect(o.Bounds(), cvAccent) })
	c, err := o.BeginFrame(kit.R(10.3, 10.3, 20.2, 20.2)) // rounds out to 10..20.5 at 2×
	if err != nil {
		t.Fatal(err)
	}
	c.FillRect(o.Bounds(), kit.RGB(0xff0000))
	if err := o.EndFrame(); err != nil {
		t.Fatal(err)
	}
	c, _ = o.BeginFrame(kit.R(30, 0, 40, 5))
	c.FillRect(kit.R(30, 0, 40, 30), kit.RGB(0x0000ff))
	if err := o.EndFrame(); err != nil {
		t.Fatal(err)
	}
	img := cvPix(t, o)
	red := color.RGBA{255, 0, 0, 255}
	if p := img.RGBAAt(20, 20); p != red {
		t.Errorf("first device pixel of the rounded area: %v", p)
	}
	if p := img.RGBAAt(40, 40); p != red {
		t.Errorf("last device pixel of the rounded area (20.5 DIPs out): %v", p)
	}
	acc := color.RGBA{cv8(cvAccent.R), cv8(cvAccent.G), cv8(cvAccent.B), 255}
	for _, pt := range [][2]int{{19, 20}, {20, 19}, {41, 30}, {30, 41}, {5, 5}, {70, 50}, {70, 12}} {
		if p := img.RGBAAt(pt[0], pt[1]); p != acc {
			t.Errorf("pixel %v outside the dirty areas changed: %v", pt, p)
		}
	}
	if p := img.RGBAAt(70, 5); p != (color.RGBA{0, 0, 255, 255}) {
		t.Errorf("the second dirty area: %v", p)
	}
}

// Push and Pop that do not balance are closed and reported; the frame is still shown.
func TestCanvasUnbalancedPushes(t *testing.T) {
	r := cvRes(t)
	o := cvOff(t, r, 40, 40, 1)
	c, _ := o.BeginFrame(o.Bounds())
	c.PushClip(kit.R(0, 0, 10, 10))
	c.PushOpacity(0.5)
	c.PushTransform(kit.Translation(1, 1))
	c.PushRoundedClip(kit.RoundRect(kit.R(0, 0, 10, 10), 3))
	c.FillRect(o.Bounds(), cvAccent)
	if err := o.EndFrame(); !errors.Is(err, kit.ErrInvalid) || errors.Is(err, kit.ErrDeviceLost) {
		t.Fatalf("EndFrame with four pushes open: %v", err)
	}
	c, _ = o.BeginFrame(o.Bounds())
	c.Pop()
	if err := o.EndFrame(); !errors.Is(err, kit.ErrInvalid) {
		t.Fatalf("EndFrame after a pop without push: %v", err)
	}
	cvFrame(t, o, func(c kit.Canvas) { c.FillRect(o.Bounds(), cvAccent) })
	if p := cvPix(t, o).RGBAAt(20, 20); p.G < 100 {
		t.Fatalf("the next frame did not draw (the graphics state leaked?): %v", p)
	}
	// Drawing outside a frame draws nothing and does not crash.
	c.FillRect(o.Bounds(), kit.RGB(0xff0000))
	c.Pop()
	if p := cvPix(t, o).RGBAAt(20, 20); p.R == 255 {
		t.Fatalf("a canvas drew after its frame: %v", p)
	}
	if _, err := o.BeginFrame(o.Bounds()); err != nil {
		t.Fatal(err)
	}
	if _, err := o.BeginFrame(o.Bounds()); !errors.Is(err, kit.ErrInvalid) {
		t.Errorf("BeginFrame inside a frame: %v", err)
	}
	if err := o.EndFrame(); err != nil {
		t.Fatal(err)
	}
	if err := o.EndFrame(); !errors.Is(err, kit.ErrInvalid) {
		t.Errorf("EndFrame without a frame: %v", err)
	}
}

// Transforms: a child transform applies first; a rotated rectangle clip clips to its
// bounds on the screen; a non-finite or flat transform draws nothing until popped;
// strokes scale with the transform.
func TestCanvasTransforms(t *testing.T) {
	r := cvRes(t)
	o := cvOff(t, r, 100, 100, 1)
	red := kit.RGB(0xff0000)
	cvFrame(t, o, func(c kit.Canvas) {
		c.FillRect(o.Bounds(), cvSurface)
		c.PushTransform(kit.Translation(50, 50))
		c.PushTransform(kit.Rotation(45))
		c.PushClip(kit.R(-10, -10, 10, 10)) // the diamond's bounds: ±14.1
		c.FillRect(kit.R(-40, -40, 40, 40), red)
		c.Pop()
		c.Pop()
		c.Pop()
		c.PushTransform(kit.AffineOf(float32(math.NaN()), 0, 0, 1, 0, 0))
		c.FillRect(o.Bounds(), cvInk)
		c.PushClip(kit.R(0, 0, 5, 5))
		c.Pop()
		c.Pop()
		c.PushTransform(kit.Scaling(0, 1))
		c.FillRect(o.Bounds(), cvInk)
		c.Pop()
		c.PushTransform(kit.Scaling(4, 4))
		c.DrawLine(kit.Pt(1, 22), kit.Pt(20, 22), kit.Stroke{Width: 1}, cvInk) // 4 px thick at y 86..90
		c.Pop()
	})
	img := cvPix(t, o)
	cvNear(t, "inside the rotated clip's bounds, outside the rotated square", img.RGBAAt(37, 50), red, 0)
	cvNear(t, "outside the bounds", img.RGBAAt(34, 50), cvSurface, 0)
	cvNear(t, "nothing drawn under a non-finite transform", img.RGBAAt(2, 2), cvSurface, 0)
	cvNear(t, "scaled stroke", img.RGBAAt(40, 86), cvInk, 0)
	cvNear(t, "scaled stroke's edge", img.RGBAAt(40, 89), cvInk, 0)
	cvNear(t, "beyond the scaled stroke", img.RGBAAt(40, 91), cvSurface, 0)
}

// Repeating gradients (the progress hatch: repeating-linear-gradient(-45deg, a 0 4px,
// b 4px 8px)) repeat with hard edges along their line, wherever the phase puts them;
// interpolation is premultiplied (a transparent stop takes its neighbour's colour).
func TestCanvasGradients(t *testing.T) {
	r := cvRes(t)
	o := cvOff(t, r, 200, 60, 1)
	a, b := kit.RGB(0x1b7f4c), kit.RGB(0xd0f0e0)
	for _, phase := range []float32{0, 3.3} {
		stops := []kit.GradientStop{{Offset: 0, Color: a}, {Offset: 0.5, Color: a}, {Offset: 0.5, Color: b}, {Offset: 1, Color: b}}
		cvFrame(t, o, func(c kit.Canvas) {
			c.FillRect(o.Bounds(), cvSurface)
			// horizontal: one period is 8 DIPs from x = phase
			c.FillGradient(kit.R(0, 0, 200, 20), kit.LinearGradient{Start: kit.Pt(phase, 0), End: kit.Pt(phase+8, 0), Repeat: true, Stops: stops})
			// the hatch at -45°
			s := kit.Pt(phase, 40)
			const k = 0.70710678
			c.FillGradient(kit.R(0, 20, 200, 40), kit.LinearGradient{Start: s, End: kit.Pt(s.X-8*k, s.Y-8*k), Repeat: true, Stops: stops})
			// premultiplied: transparent blue to red: no purple in between
			c.FillGradient(kit.R(0, 40, 200, 60), kit.LinearGradient{Start: kit.Pt(0, 0), End: kit.Pt(200, 0),
				Stops: []kit.GradientStop{{Offset: 0, Color: kit.RGB(0x0000ff).WithAlpha(0)}, {Offset: 1, Color: kit.RGB(0xff0000)}}})
		})
		img := cvPix(t, o)
		// CoreGraphics samples a gradient through a table of colours, which can move a hard
		// edge by a fraction of a pixel: pixels whose centre is within half a pixel of an
		// edge may take either colour.
		nearEdge := func(x int) bool {
			u := math.Mod(float64(x)+0.5-float64(phase), 4)
			if u < 0 {
				u += 4
			}
			return u < 0.5 || u > 3.5
		}
		for x := 0; x < 190; x++ {
			if nearEdge(x) {
				continue
			}
			if p, q := img.RGBAAt(x, 10), img.RGBAAt(x+8, 10); p != q {
				t.Errorf("phase %v: x %d and %d differ: %v %v (not periodic)", phase, x, x+8, p, q)
				break
			}
		}
		// pixel centres: x+0.5-phase mod 8 in [0, 4) is a, [4, 8) is b
		for x := 0; x < 24; x++ {
			u := math.Mod(float64(x)+0.5-float64(phase), 8)
			if u < 0 {
				u += 8
			}
			if nearEdge(x) {
				continue
			}
			want := a
			if u >= 4 {
				want = b
			}
			cvNear(t, "hatch band", img.RGBAAt(x, 10), want, 1)
		}
		// the -45° hatch's colour is constant along its stripes, which run up to the right
		for x := 20; x < 150; x++ {
			p, q := img.RGBAAt(x, 25), img.RGBAAt(x+1, 24)
			if absDiff(p.R, q.R) > 2 || absDiff(p.G, q.G) > 2 {
				t.Errorf("phase %v: the hatch is not constant along its stripes at %d: %v %v", phase, x, p, q)
				break
			}
		}
		mid := img.RGBAAt(100, 50)
		if mid.B > 0x90 {
			t.Errorf("premultiplied interpolation: the middle of transparent blue → red is %v, want no blue", mid)
		}
		cvNear(t, "half-way of transparent → red over white", mid, kit.Mix(kit.RGB(0xff0000), 0.5, cvSurface), 3)
	}
	// One stop, or no line: the last colour.
	cvFrame(t, o, func(c kit.Canvas) {
		c.FillGradient(kit.R(0, 0, 10, 10), kit.LinearGradient{Start: kit.Pt(0, 0), End: kit.Pt(0, 0),
			Stops: []kit.GradientStop{{Offset: 0, Color: a}, {Offset: 1, Color: b}}})
	})
	cvNear(t, "a gradient without a line", cvPix(t, o).RGBAAt(5, 5), b, 0)
	if n := len(o.t.gradients); n > 4 {
		t.Errorf("%d gradients cached, want the phases to share theirs", n)
	}
}

// A blurred box-shadow falls off monotonically away from the box, as a Gaussian of
// σ = blur / 2, is cut out under the box (a translucent fill does not darken), and an inset
// one shows only inside.
func TestCanvasShadowFalloff(t *testing.T) {
	r := cvRes(t)
	for _, ppd := range []float32{1, 2} {
		o := cvOff(t, r, int(200*ppd), int(120*ppd), ppd)
		black := kit.RGBA(0, 0, 0, 1)
		cvFrame(t, o, func(c kit.Canvas) {
			c.FillRect(o.Bounds(), cvSurface)
			c.DrawShadow(kit.RoundRect(kit.R(40, 40, 80, 80), 6), kit.Shadow{Blur: 12, Color: black})
			c.DrawShadow(kit.RoundRect(kit.R(120, 20, 180, 100), 6), kit.Shadow{Blur: 8, Spread: 2, Inset: true, Color: black})
		})
		img := cvPix(t, o)
		row := int(60 * ppd)
		prev := -1
		for x := int(80 * ppd); x < int(100*ppd); x++ {
			v := int(img.RGBAAt(x, row).R)
			if v < prev {
				t.Errorf("ppd %v: the shadow darkens away from the box at x %d (%d after %d)", ppd, x, v, prev)
				break
			}
			prev = v
		}
		// At the box's edge an infinite straight edge's Gaussian is half: 1 - 0.5·α.
		edge := float64(img.RGBAAt(int(80*ppd), row).R)
		if edge < 110 || edge > 150 {
			t.Errorf("ppd %v: just outside the edge %v, want about 128", ppd, edge)
		}
		// σ = 6: 1σ out the shadow is Φ(-1) ≈ 0.159 → 255·(1-0.159) ≈ 214
		one := float64(img.RGBAAt(int(86*ppd), row).R)
		if math.Abs(one-214) > 12 {
			t.Errorf("ppd %v: one σ out reads %v, want about 214", ppd, one)
		}
		if v := img.RGBAAt(int(60*ppd), row); v != (color.RGBA{255, 255, 255, 255}) {
			t.Errorf("ppd %v: under the box the shadow shows: %v", ppd, v)
		}
		if v := img.RGBAAt(int(110*ppd), row); v.R < 250 {
			t.Errorf("ppd %v: 3σ+ away still shadowed: %v", ppd, v)
		}
		// inset: dark at the inner edge, light in the middle, nothing outside
		if v := img.RGBAAt(int(120*ppd)+1, int(60*ppd)); v.R > 150 {
			t.Errorf("ppd %v: inset shadow at the edge: %v", ppd, v)
		}
		if v := img.RGBAAt(int(150*ppd), int(60*ppd)); v.R < 240 {
			t.Errorf("ppd %v: inset shadow in the middle: %v", ppd, v)
		}
		if v := img.RGBAAt(int(118*ppd), int(60*ppd)); v.R != 255 {
			t.Errorf("ppd %v: inset shadow outside the box: %v", ppd, v)
		}
		if err := o.WritePNG(filepath.Join(cvShots(t), "shadows@"+ppdName(ppd)+".png")); err != nil {
			t.Fatal(err)
		}
	}
	// A large box draws its mask in nine pieces; it matches a straight edge's profile.
	o := cvOff(t, r, 600, 400, 1)
	cvFrame(t, o, func(c kit.Canvas) {
		c.FillRect(o.Bounds(), cvSurface)
		c.DrawShadow(kit.RoundRect(kit.R(50, 50, 550, 350), 8), kit.Shadow{Offset: kit.Pt(0, 4), Blur: 12, Color: kit.RGBA(0, 0, 0, 0.5)})
	})
	img := cvPix(t, o)
	if a, b := img.RGBAAt(300, 356), img.RGBAAt(120, 356); absDiff(a.R, b.R) > 1 {
		t.Errorf("the stretched middle of a large mask differs from its side: %v %v", a, b)
	}
	if v := img.RGBAAt(300, 356); v.R > 240 || v.R < 150 {
		t.Errorf("below the large box: %v", v)
	}
	for _, m := range o.t.shadows {
		if m.l.DX == 0 || m.l.DY == 0 {
			t.Errorf("the large box's mask was not shrunk: %+v", m.l)
		}
	}
}

// Images decode through ImageIO (PNG, JPEG, GIF, BMP), draw their src part into dst,
// and sort their failures: an unknown format is ErrUnsupported, broken data ErrInvalid.
func TestCanvasImages(t *testing.T) {
	r := cvRes(t)
	src := stdimage.NewNRGBA(stdimage.Rect(0, 0, 4, 4))
	for i := 0; i < 16; i++ {
		src.Set(i%4, i/4, color.NRGBA{200, 30, 30, 255})
	}
	src.Set(0, 0, color.NRGBA{30, 30, 200, 255})
	var pngData, jpgData, gifData bytes.Buffer
	if err := png.Encode(&pngData, src); err != nil {
		t.Fatal(err)
	}
	big := stdimage.NewRGBA(stdimage.Rect(0, 0, 16, 16))
	for i := range big.Pix {
		big.Pix[i] = [4]uint8{200, 30, 30, 255}[i%4]
	}
	if err := jpeg.Encode(&jpgData, big, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	if err := gif.Encode(&gifData, src, nil); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"PNG": pngData.Bytes(), "JPEG": jpgData.Bytes(), "GIF": gifData.Bytes(), "BMP": cvBMP(src)} {
		img, err := r.NewImage(data)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		w, h := img.PixelSize()
		o := cvOff(t, r, 40, 40, 1)
		cvFrame(t, o, func(c kit.Canvas) {
			c.FillRect(o.Bounds(), cvSurface)
			c.DrawImage(img, kit.R(0, 0, 1, 1), kit.R(0, 0, 20, 20)) // the top-left pixel, scaled
			c.DrawImage(img, kit.Rect{}, kit.R(20, 20, 40, 40))      // all of it
		})
		px := cvPix(t, o)
		if name != "JPEG" {
			if w != 4 || h != 4 {
				t.Errorf("%s: size %d×%d", name, w, h)
			}
			if p := px.RGBAAt(10, 10); p.B < 150 || p.R > 80 {
				t.Errorf("%s: src part: %v, want blue", name, p)
			}
		}
		if p := px.RGBAAt(35, 35); p.R < 150 || p.B > 80 {
			t.Errorf("%s: whole image: %v, want red", name, p)
		}
		if p := px.RGBAAt(30, 10); p != (color.RGBA{255, 255, 255, 255}) {
			t.Errorf("%s: drew outside dst: %v", name, p)
		}
		img.Release()
		img.Release()
		if w, h := img.PixelSize(); w != 0 || h != 0 {
			t.Errorf("%s: a released image keeps its size", name)
		}
		cvFrame(t, o, func(c kit.Canvas) { c.DrawImage(img, kit.Rect{}, o.Bounds()) }) // nothing, no crash
	}
	if _, err := r.NewImage([]byte("definitely not an image")); !errors.Is(err, kit.ErrUnsupported) {
		t.Errorf("unknown format: %v, want ErrUnsupported", err)
	}
	var b bytes.Buffer
	png.Encode(&b, stdimage.NewNRGBA(stdimage.Rect(0, 0, 64, 64)))
	if _, err := r.NewImage(b.Bytes()[:40]); !errors.Is(err, kit.ErrInvalid) {
		t.Errorf("truncated PNG: %v, want ErrInvalid", err)
	}
	if _, err := r.NewImage([]byte("\x89PNG\r\n\x1a\nbroken")); !errors.Is(err, kit.ErrInvalid) {
		t.Errorf("broken PNG: %v, want ErrInvalid", err)
	}
	if _, err := r.NewImage(nil); !errors.Is(err, kit.ErrInvalid) {
		t.Errorf("no data: %v", err)
	}
	if r.live.images != 0 {
		t.Errorf("%d images not released", r.live.images)
	}
}

// cvBMP is img as a 24-bit bottom-up BMP.
func cvBMP(img *stdimage.NRGBA) []byte {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	stride := (3*w + 3) &^ 3
	var b bytes.Buffer
	le := binary.LittleEndian
	b.WriteString("BM")
	binary.Write(&b, le, uint32(54+stride*h))
	binary.Write(&b, le, uint32(0))
	binary.Write(&b, le, uint32(54))
	binary.Write(&b, le, uint32(40))
	binary.Write(&b, le, int32(w))
	binary.Write(&b, le, int32(h))
	binary.Write(&b, le, uint16(1))
	binary.Write(&b, le, uint16(24))
	binary.Write(&b, le, [6]uint32{0, uint32(stride * h), 2835, 2835, 0, 0})
	for y := h - 1; y >= 0; y-- {
		row := make([]byte, stride)
		for x := 0; x < w; x++ {
			c := img.NRGBAAt(x, y)
			row[3*x], row[3*x+1], row[3*x+2] = c.B, c.G, c.R
		}
		b.Write(row)
	}
	return b.Bytes()
}

// Paths: SVG arcs (a circle of two arcs), the even-odd rule, open figures stroked open, a
// segment without a move, and non-finite points refused.
func TestCanvasPaths(t *testing.T) {
	r := cvRes(t)
	circle, err := r.NewSVGPath("M10 20a10 10 0 1 0 20 0a10 10 0 1 0-20 0z", kit.FillNonZero)
	if err != nil {
		t.Fatal(err)
	}
	defer circle.Release()
	ring, err := r.NewSVGPath("M40 10h20v20h-20zM45 15h10v10h-10z", kit.FillEvenOdd)
	if err != nil {
		t.Fatal(err)
	}
	defer ring.Release()
	var d kit.PathData
	d.MoveTo(kit.Pt(70, 30))
	d.LineTo(kit.Pt(70, 10))
	d.LineTo(kit.Pt(90, 10)) // open: no line back from (90, 10) to (70, 30)
	open, err := r.NewPath(d)
	if err != nil {
		t.Fatal(err)
	}
	defer open.Release()
	if b := circle.(*path).Bounds(); math.Abs(float64(b.Min.X-10)) > 0.01 || math.Abs(float64(b.Max.X-30)) > 0.01 ||
		math.Abs(float64(b.Min.Y-10)) > 0.01 || math.Abs(float64(b.Max.Y-30)) > 0.01 {
		t.Errorf("the arcs' circle has bounds %v, want (10, 10)-(30, 30)", b)
	}
	o := cvOff(t, r, 100, 40, 1)
	cvFrame(t, o, func(c kit.Canvas) {
		c.FillRect(o.Bounds(), cvSurface)
		c.FillPath(circle, cvInk)
		c.FillPath(ring, cvInk)
		c.StrokePath(open, kit.Stroke{Width: 2}, cvInk)
	})
	img := cvPix(t, o)
	cvNear(t, "circle", img.RGBAAt(20, 20), cvInk, 0)
	cvNear(t, "outside the circle's corner", img.RGBAAt(11, 11), cvSurface, 0)
	cvNear(t, "even-odd ring", img.RGBAAt(42, 20), cvInk, 0)
	cvNear(t, "even-odd hole", img.RGBAAt(50, 20), cvSurface, 0)
	cvNear(t, "open figure's side", img.RGBAAt(70, 20), cvInk, 60)
	cvNear(t, "open figure's missing side", img.RGBAAt(80, 20), cvSurface, 0)

	d.Reset()
	d.MoveTo(kit.Pt(float32(math.Inf(1)), 0))
	if _, err := r.NewPath(d); !errors.Is(err, kit.ErrInvalid) {
		t.Errorf("an infinite point: %v", err)
	}
	if _, err := r.NewSVGPath("M1 2X", kit.FillNonZero); !errors.Is(err, kit.ErrInvalid) {
		t.Errorf("bad SVG: %v", err)
	}
	other := cvRes(t)
	p, _ := other.NewSVGPath("M0 0h100v40h-100z", kit.FillNonZero)
	cvFrame(t, o, func(c kit.Canvas) { c.FillPath(p, kit.RGB(0xff0000)) }) // another Resources' path: nothing
	if px := cvPix(t, o).RGBAAt(5, 5); px.R == 255 && px.G == 0 {
		t.Error("a path of other resources drew")
	}
	p.Release()
	if r.live.paths != 3 {
		t.Errorf("%d paths live, want 3", r.live.paths)
	}
}

// Dashes: SVG's stroke-dasharray from the figure's start with the cap on every dash; round
// caps with 1 1... and with 0 2 draw dots.
func TestCanvasDashes(t *testing.T) {
	r := cvRes(t)
	o := cvOff(t, r, 120, 40, 2)
	cvFrame(t, o, func(c kit.Canvas) {
		c.FillRect(o.Bounds(), cvSurface)
		c.DrawLine(kit.Pt(2, 5), kit.Pt(58, 5), kit.Stroke{Width: 2, Cap: kit.CapRound, Dash: kit.Dash{On: 0, Off: 4}}, cvInk)
		c.DrawLine(kit.Pt(2, 15), kit.Pt(58, 15), kit.Stroke{Width: 2, Dash: kit.Dash{On: 3, Off: 3}}, cvInk)
	})
	img := cvPix(t, o)
	cvNear(t, "a dot", img.RGBAAt(2*2, 2*5), cvInk, 30)
	cvNear(t, "between dots", img.RGBAAt(2*4, 2*5), cvSurface, 0)
	cvNear(t, "the next dot", img.RGBAAt(2*6, 2*5), cvInk, 30)
	cvNear(t, "3 3 dash", img.RGBAAt(2*3+1, 2*15), cvInk, 0)
	cvNear(t, "3 3 gap", img.RGBAAt(2*6+1, 2*15), cvSurface, 0)
}

// The PNG an offscreen target writes decodes to its pixels.
func TestCanvasEncodePNG(t *testing.T) {
	r := cvRes(t)
	for _, tr := range []bool{false, true} {
		o, err := NewOffscreen(r, 16, 8, 1, OffscreenOptions{Transparent: tr})
		if err != nil {
			t.Fatal(err)
		}
		cvFrame(t, o, func(c kit.Canvas) { c.FillRect(kit.R(0, 0, 8, 8), kit.RGBA(255, 0, 0, 1)) })
		data, err := o.EncodePNG()
		if err != nil {
			t.Fatal(err)
		}
		dec, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if got := color.NRGBAModel.Convert(dec.At(2, 2)).(color.NRGBA); got != (color.NRGBA{255, 0, 0, 255}) {
			t.Errorf("transparent=%v: red pixel decodes as %v", tr, got)
		}
		right := color.NRGBAModel.Convert(dec.At(12, 2)).(color.NRGBA)
		if tr && right.A != 0 || !tr && right != (color.NRGBA{0, 0, 0, 255}) {
			t.Errorf("transparent=%v: unpainted pixel decodes as %v", tr, right)
		}
		o.Close()
		o.Close()
		if _, err := o.BeginFrame(kit.R(0, 0, 1, 1)); !errors.Is(err, kit.ErrClosed) {
			t.Errorf("BeginFrame after Close: %v", err)
		}
	}
}

// A simulated loss is reported as kit.ErrDeviceLost, clears the target and its caches, and
// the next whole frame draws the same picture.
func TestCanvasSimulatedLoss(t *testing.T) {
	r := cvRes(t)
	o := cvOff(t, r, 120, 80, 2)
	scene := func(c kit.Canvas) {
		c.FillRect(o.Bounds(), cvSurface)
		c.FillGradient(kit.R(0, 0, 60, 10), kit.LinearGradient{Start: kit.Pt(0, 0), End: kit.Pt(60, 0),
			Stops: []kit.GradientStop{{Offset: 0, Color: cvAccent}, {Offset: 1, Color: cvSurface}}})
		c.DrawShadow(kit.RoundRect(kit.R(10, 20, 40, 30), 4), kit.Shadow{Offset: kit.Pt(0, 2), Blur: 6, Color: kit.RGBA(0, 0, 0, 0.3)})
		c.FillRoundedRect(kit.RRect{Rect: kit.R(10, 20, 40, 30), Radii: kit.Radii{TopLeft: 4, BottomRight: 4}}, cvSurface)
	}
	cvFrame(t, o, scene)
	before := cvPix(t, o)
	o.SimulateDeviceLoss()
	c, err := o.BeginFrame(o.Bounds())
	if err != nil {
		t.Fatal(err)
	}
	c.FillRect(o.Bounds(), kit.RGB(0xff00ff))
	if err := o.EndFrame(); !errors.Is(err, kit.ErrDeviceLost) {
		t.Fatalf("EndFrame after a loss: %v", err)
	}
	if len(o.t.gradients) != 0 || len(o.t.shadows) != 0 {
		t.Fatal("the caches were not dropped")
	}
	cvFrame(t, o, scene)
	if !bytes.Equal(before.Pix, cvPix(t, o).Pix) {
		t.Fatal("the frame after the loss differs")
	}
}

// Resize keeps the pixels at the same scale and drops shadow masks at a new one.
func TestCanvasResize(t *testing.T) {
	r := cvRes(t)
	o := cvOff(t, r, 20, 20, 1)
	cvFrame(t, o, func(c kit.Canvas) {
		c.FillRect(o.Bounds(), cvAccent)
		c.DrawShadow(kit.RoundRect(kit.R(4, 4, 10, 10), 2), kit.Shadow{Blur: 4, Color: kit.RGBA(0, 0, 0, 0.3)})
	})
	if err := o.t.Resize(30, 10, 1); err != nil {
		t.Fatal(err)
	}
	img := cvPix(t, o)
	cvNear(t, "kept pixel", img.RGBAAt(1, 1), cvAccent, 0)
	if p := img.RGBAAt(25, 5); p != (color.RGBA{0, 0, 0, 255}) {
		t.Errorf("the grown area: %v, want the start colour", p)
	}
	if len(o.t.shadows) == 0 {
		t.Error("masks dropped at the same scale")
	}
	if err := o.t.Resize(60, 20, 2); err != nil {
		t.Fatal(err)
	}
	if len(o.t.shadows) != 0 {
		t.Error("masks kept at a new scale")
	}
	if s := o.Size(); s.W != 30 || s.H != 10 {
		t.Errorf("size %v", s)
	}
	im := o.t.CGImage()
	if im == 0 {
		t.Fatal("no CGImage")
	}
	cgImageRelease(im)
}

// A frame of cached resources allocates nothing on the Go heap.
func TestCanvasSteadyFrameDoesNotAllocate(t *testing.T) {
	r := cvRes(t)
	o := cvOff(t, r, 400, 200, 2)
	icon, err := r.NewSVGPath("M5 12.5l4.5 4.5L19 7.5", kit.FillNonZero)
	if err != nil {
		t.Fatal(err)
	}
	defer icon.Release()
	stops := []kit.GradientStop{{Offset: 0, Color: cvAccent}, {Offset: 0.5, Color: cvAccent}, {Offset: 0.5, Color: cvSurface}, {Offset: 1, Color: cvSurface}}
	phase := float32(0)
	scene := func(c kit.Canvas) {
		c.FillRect(o.Bounds(), cvSurface)
		c.DrawShadow(kit.RoundRect(kit.R(20, 20, 120, 80), 8), kit.Shadow{Offset: kit.Pt(0, 4), Blur: 12, Color: kit.RGBA(0, 0, 0, 0.2)})
		c.DrawShadow(kit.RoundRect(kit.R(20, 20, 120, 80), 8), kit.Shadow{Spread: 1, Color: cvAccent})
		c.PushRoundedClip(kit.RoundRect(kit.R(20, 20, 120, 80), 8))
		c.FillRect(kit.R(20, 20, 120, 80), cvSurface)
		c.PushOpacity(0.8)
		c.PushTransform(kit.Scaling(2, 2).Then(kit.Translation(30, 30)))
		c.StrokePath(icon, kit.Stroke{Width: 1.6, Cap: kit.CapRound, Join: kit.JoinRound}, cvAccent)
		c.Pop()
		c.Pop()
		c.Pop()
		c.FillGradient(kit.R(140, 20, 380, 28), kit.LinearGradient{Start: kit.Pt(140+phase, 28), End: kit.Pt(140+phase-5.657, 28-5.657), Repeat: true, Stops: stops})
		c.(kit.BackdropFilter).BlurBackdrop(kit.R(0, 100, 400, 200), 1.5, 0.6)
		c.DrawLine(kit.Pt(10, 150), kit.Pt(390, 150), kit.Stroke{Width: 1, Dash: kit.Dash{On: 3, Off: 3}}, cvInk)
	}
	cvFrame(t, o, scene)
	phase = 1.5
	cvFrame(t, o, scene)
	allocs := testing.AllocsPerRun(20, func() {
		phase += 0.4
		if phase > 8 {
			phase -= 8
		}
		c, err := o.BeginFrame(o.Bounds())
		if err != nil {
			t.Fatal(err)
		}
		scene(c)
		if err := o.EndFrame(); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Errorf("%v allocations per frame, want 0", allocs)
	}
}
