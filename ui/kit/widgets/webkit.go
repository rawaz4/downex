package widgets

// [mac webkit] Where WebKit on macOS draws app.css differently from Chromium, under
// kit.Environment.WebKit (the Mac window; Windows never sets it, and nothing of this file
// runs there). Measured on WKWebView snapshots (macOS 26, 2026-11-10) of the demo and of
// test pages with the app's tokens, at 2x, light and dark.

import (
	"math"

	"downex/ui/kit"
)

// webkitLook reports whether p paints for WebKit's look (and not under forced colours,
// which WebKit on macOS never matches).
func webkitLook(p *kit.PaintContext, forced bool) bool { return p.Env.WebKit && !forced }

// macOS's check box as WebKit draws <input type=checkbox> with app.css's accent-color (the
// box keeps the native appearance): a superellipse |x|^n + |y|^n = 1 filling the box (n =
// 2.95, fitted to the 16 and 14 px boxes to 0.05 rms coverage). Unchecked: a ground
// (#ffffff, dark #1e1e1e) inset 0.5 px under a 1.5 px ring whose inner edge is squarer (n =
// 3.5), black at 50% (dark: white at 55%). Checked: the ground and the accent over the
// whole shape, the accent brought to a relative luminance of 0.5 (webkitCheckFill), and a
// check of round strokes 0.108 of the box wide through (0.264, 0.541), (0.441, 0.754) and
// (0.738, 0.297) of the box, white (black on a lighter fill). Disabled: the ring, the fill
// and the check at half their alpha over the same ground.
const (
	webkitCheckN        = 2.95
	webkitCheckInnerN   = 3.5
	webkitCheckRing     = 1.5
	webkitCheckGroundIn = 0.5
	webkitCheckMarkW    = 0.108
)

var (
	webkitCheckGroundLight = kit.RGB(0xffffff)
	webkitCheckGroundDark  = kit.RGB(0x1e1e1e)
	webkitCheckRingLight   = kit.Color{A: 0.5}
	webkitCheckRingDark    = kit.Color{R: 1, G: 1, B: 1, A: 0.547}
	webkitCheckMarkPoints  = [3]kit.Point{{X: 0.264, Y: 0.541}, {X: 0.441, Y: 0.754}, {X: 0.738, Y: 0.297}}
)

// webkitCheckPaths are a check box's paths at one size (box coordinates).
type webkitCheckPaths struct {
	size                       kit.Size
	shape, ground, ring, check kit.Path
}

func (w *webkitCheckPaths) release() {
	for _, p := range []kit.Path{w.shape, w.ground, w.ring, w.check} {
		if p != nil {
			p.Release()
		}
	}
	*w = webkitCheckPaths{}
}

// superellipse adds the closed figure |x−c|^n/a^n + |y−c|^n/b^n = 1 to d.
func superellipse(d *kit.PathData, r kit.Rect, n float64) {
	const steps = 192
	cx, cy := float64(r.Min.X+r.Max.X)/2, float64(r.Min.Y+r.Max.Y)/2
	a, b := float64(r.Width())/2, float64(r.Height())/2
	e := 2 / n
	for i := range steps {
		t := 2 * math.Pi * float64(i) / steps
		c, s := math.Cos(t), math.Sin(t)
		x := cx + a*math.Copysign(math.Pow(math.Abs(c), e), c)
		y := cy + b*math.Copysign(math.Pow(math.Abs(s), e), s)
		p := kit.Pt(kit.DIP(x), kit.DIP(y))
		if i == 0 {
			d.MoveTo(p)
		} else {
			d.LineTo(p)
		}
	}
	d.Close()
}

func (w *webkitCheckPaths) make(res kit.Resources, sz kit.Size) bool {
	box := kit.RectAt(kit.Point{}, sz)
	in := func(v kit.DIP) kit.Rect { return kit.R(v, v, sz.W-v, sz.H-v) }
	var shape, ground, ring, check kit.PathData
	superellipse(&shape, box, webkitCheckN)
	superellipse(&ground, in(webkitCheckGroundIn), webkitCheckN)
	ring.Fill = kit.FillEvenOdd
	superellipse(&ring, box, webkitCheckN)
	superellipse(&ring, in(webkitCheckRing), webkitCheckInnerN)
	for i, p := range webkitCheckMarkPoints {
		q := kit.Pt(p.X*sz.W, p.Y*sz.H)
		if i == 0 {
			check.MoveTo(q)
		} else {
			check.LineTo(q)
		}
	}
	var err error
	out := webkitCheckPaths{size: sz}
	for _, c := range []struct {
		d  *kit.PathData
		to *kit.Path
	}{{&shape, &out.shape}, {&ground, &out.ground}, {&ring, &out.ring}, {&check, &out.check}} {
		if *c.to, err = res.NewPath(*c.d); err != nil {
			out.release()
			return false
		}
	}
	w.release()
	*w = out
	return true
}

// paintWebKit draws the box as WebKit does on macOS.
func (b *CheckBox) paintWebKit(p *kit.PaintContext, accent kit.Color, dark bool) {
	win := b.Window()
	if win == nil || win.Host() == nil {
		return
	}
	r := snapBox(p, 0).Rect
	sz := r.Size()
	if b.mac.size != sz || b.mac.shape == nil {
		if !b.mac.make(win.Host().Resources(), sz) {
			return
		}
	}
	ground, ring := webkitCheckGroundLight, webkitCheckRingLight
	if dark {
		ground, ring = webkitCheckGroundDark, webkitCheckRingDark
	}
	half := func(c kit.Color) kit.Color {
		if b.Disabled {
			c.A *= 0.5
		}
		return c
	}
	c := p.Canvas
	c.PushTransform(kit.Translation(r.Min.X, r.Min.Y))
	defer c.Pop()
	if !b.Checked {
		c.FillPath(b.mac.ground, ground)
		c.FillPath(b.mac.ring, half(ring))
		return
	}
	fill := webkitCheckFill(accent, dark)
	c.FillPath(b.mac.shape, ground)
	c.FillPath(b.mac.shape, half(fill))
	c.StrokePath(b.mac.check, kit.Stroke{Width: webkitCheckMarkW * sz.H, Cap: kit.CapRound, Join: kit.JoinRound}, half(webkitCheckMark(fill)))
}

func linear(v float32) float64 {
	c := float64(v)
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func gamma(c float64) float32 {
	if c <= 0.0031308 {
		return float32(c * 12.92)
	}
	return float32(1.055*math.Pow(c, 1/2.4) - 0.055)
}

func luminance(c kit.Color) float64 {
	return 0.2126*linear(c.R) + 0.7152*linear(c.G) + 0.0722*linear(c.B)
}

// webkitCheckFill is the checked box's fill for an accent-color: the accent with its
// linear-light channels scaled to a relative luminance of 0.5, down when lighter in the
// light scheme, up when darker in the dark scheme (unless a channel would pass 1: a pure
// red or blue is kept). Measured: #10804f stays in light and becomes #22d687 in dark,
// #3ccb8b (dark --accent) becomes #3fd491, #6fdcaa becomes #68cfa0 in light.
func webkitCheckFill(accent kit.Color, dark bool) kit.Color {
	l := luminance(accent)
	if l <= 0 || (!dark && l <= 0.5) || (dark && l >= 0.5) {
		return accent
	}
	k := 0.5 / l
	r, g, bl := linear(accent.R)*k, linear(accent.G)*k, linear(accent.B)*k
	if max(r, g, bl) > 1 {
		return accent
	}
	return kit.Color{R: gamma(r), G: gamma(g), B: gamma(bl), A: accent.A}
}

// webkitCheckMark is the check's colour on a fill: white, black on a fill lighter than
// luminance 0.5 (a light accent left as it is in the dark scheme).
func webkitCheckMark(fill kit.Color) kit.Color {
	if luminance(fill) > 0.505 {
		return kit.RGB(0x000000)
	}
	return kit.RGB(0xffffff)
}
