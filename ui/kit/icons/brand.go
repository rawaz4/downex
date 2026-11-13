package icons

import (
	"fmt"
	"math"

	"downex/ui/kit"
	"downex/ui/kit/tokens"
)

// brandSource is BrandMark.svelte's layout, white space collapsed: fit, brandLayout and
// barPath, which Brand and BrandBar.path port (the same rules as internal/brand's fit and
// appTile). The drift test compares it with the component; when the component changes,
// port the change and copy its source here.
const brandSource = `function fit(size: number, u: number): BrandBar[] { let h = Math.max(1, Math.round(24 * u)); let g = Math.max(1, Math.round(10 * u)); const limit = Math.round(126 * u) + 1; while (4 * h + 3 * g > limit) { if (g > 1 && (g - 10 * u >= h - 24 * u || h <= 2)) g--; else if (h > 1) h--; else break; } const w = Math.max(4, Math.round(116 * u)); const short = Math.max(2, Math.round(80 * u)); const head = Math.max(1, Math.round(32 * u)); const mid = w - head - g; const top = Math.floor((size - (4 * h + 3 * g)) / 2); const left = Math.floor((size - w) / 2); const r = h >= 4 ? h / 2 : 0; const y = (i: number) => top + i * (h + g); return [ { x: left, y: y(0), w: short, h, r, head: false }, { x: left, y: y(1), w, h, r, head: false }, { x: left, y: y(2), w: mid, h, r: 0, head: false }, { x: left + mid + g, y: y(2), w: w - mid - g, h, r, head: true }, { x: left, y: y(3), w: short, h, r, head: false }, ]; }|function brandLayout(size: number): BrandLayout { const s = Math.max(1, Math.round(size)); if (s < 32) { return { x0: 0, x1: s, radius: (3.5 / 16) * s, hairline: 0, bars: fit(s, (0.625 * s) / 116) }; } return { x0: (8 / 256) * s, x1: (248 / 256) * s, radius: (52 / 256) * s, hairline: Math.max(1, (1.5 / 256) * s), bars: fit(s, s / 256), }; }|function barPath(b: BrandBar): string { if (b.r <= 0) return ` + "`M${b.x} ${b.y}h${b.w}v${b.h}h${-b.w}z`" + `; return ` + "`M${b.x} ${b.y}h${b.w - b.r}a${b.r} ${b.r} 0 0 1 0 ${b.h}h${-(b.w - b.r)}z`" + `; }`

// BrandBar is one segment of the logo, in pixels of its size: a square left end and, when R
// is above 0, a half-round right end of radius R; Head marks the live chunk.
type BrandBar struct {
	X, Y, W, H, R float64
	Head          bool
}

// BrandLayout is the logo laid out for one size in pixels: the tile's square from X0 to X1
// with its radius, the hairline edge (0 below 32 px) and the bars, on the pixel grid.
type BrandLayout struct {
	X0, X1, Radius, Hairline float64
	Bars                     [5]BrandBar
}

// jsRound is JavaScript's Math.round: half up.
func jsRound(v float64) float64 { return math.Floor(v + 0.5) }

// brandFit is BrandMark's fit: the bars on the pixel grid at u pixels per master unit.
func brandFit(size int, u float64) [5]BrandBar {
	h := math.Max(1, jsRound(24*u))
	g := math.Max(1, jsRound(10*u))
	limit := jsRound(126*u) + 1
	for 4*h+3*g > limit {
		if g > 1 && (g-10*u >= h-24*u || h <= 2) {
			g--
		} else if h > 1 {
			h--
		} else {
			break
		}
	}
	w := math.Max(4, jsRound(116*u))
	short := math.Max(2, jsRound(80*u))
	head := math.Max(1, jsRound(32*u))
	mid := w - head - g
	s := float64(size)
	top := math.Floor((s - (4*h + 3*g)) / 2)
	left := math.Floor((s - w) / 2)
	r := 0.0
	if h >= 4 {
		r = h / 2
	}
	y := func(i float64) float64 { return top + i*(h+g) }
	return [5]BrandBar{
		{X: left, Y: y(0), W: short, H: h, R: r},
		{X: left, Y: y(1), W: w, H: h, R: r},
		{X: left, Y: y(2), W: mid, H: h},
		{X: left + mid + g, Y: y(2), W: w - mid - g, H: h, R: r, Head: true},
		{X: left, Y: y(3), W: short, H: h, R: r},
	}
}

// Brand is BrandMark's layout at size pixels: below 32 px a full-bleed tile with the mark
// 0.625 of the size wide and no hairline; from 32 px the inset tile with its hairline.
func Brand(size float64) BrandLayout {
	s := math.Max(1, jsRound(size))
	if s < 32 {
		return BrandLayout{X0: 0, X1: s, Radius: 3.5 / 16 * s, Bars: brandFit(int(s), 0.625*s/116)}
	}
	return BrandLayout{X0: 8.0 / 256 * s, X1: 248.0 / 256 * s, Radius: 52.0 / 256 * s,
		Hairline: math.Max(1, 1.5/256*s), Bars: brandFit(int(s), s/256)}
}

// path is the bar as SVG path data (BrandMark's barPath).
func (b BrandBar) path() string {
	n := func(v float64) string { return fmt.Sprint(v) }
	if b.R <= 0 {
		return fmt.Sprintf("M%s %sh%sv%sh%sz", n(b.X), n(b.Y), n(b.W), n(b.H), n(-b.W))
	}
	return fmt.Sprintf("M%s %sh%sa%s %s 0 0 1 0 %sh%sz", n(b.X), n(b.Y), n(b.W-b.R), n(b.R), n(b.R), n(b.H), n(-(b.W - b.R)))
}

// brandKey is b's path data, formatted once per bar and kept.
func (s *Set) brandKey(b BrandBar) string {
	if k, ok := s.brandKeys[b]; ok {
		return k
	}
	if s.brandKeys == nil {
		s.brandKeys = map[BrandBar]string{}
	}
	k := b.path()
	s.brandKeys[b] = k
	return k
}

// DrawBrand draws the logo of size pixels (BrandMark's size: 24 in the title bar) with its
// top-left corner at at, in the theme's --brand tokens. Like the SVG, the layout is on the
// grid of the size in DIPs, and the canvas scales it to the monitor.
func (s *Set) DrawBrand(c kit.Canvas, at kit.Point, size float64, t *tokens.Theme) {
	l := Brand(size)
	f := func(v float64) float32 { return float32(v) }
	side := l.X1 - l.X0
	tileRect := func(inset, radius float64) kit.RRect {
		return kit.RoundRect(kit.R(at.X+f(l.X0+inset), at.Y+f(l.X0+inset), at.X+f(l.X0+side-inset), at.Y+f(l.X0+side-inset)), f(math.Max(0, radius)))
	}
	if l.Hairline > 0 {
		c.FillRoundedRect(tileRect(0, l.Radius), t.BrandHairline)
		c.FillRoundedRect(tileRect(l.Hairline, l.Radius-l.Hairline), t.BrandTile)
	} else {
		c.FillRoundedRect(tileRect(0, l.Radius), t.BrandTile)
		if t.BrandRing.A > 0 { // the dark theme's 1 px inner ring
			c.StrokeRoundedRect(tileRect(0.5, l.Radius-0.5), kit.Stroke{Width: 1}, t.BrandRing)
		}
	}
	c.PushTransform(kit.Translation(at.X, at.Y))
	for _, b := range l.Bars {
		p := s.path(s.brandKey(b))
		if p == nil {
			continue
		}
		col := t.BrandBar
		if b.Head {
			col = t.BrandHead
		}
		c.FillPath(p, col)
	}
	c.Pop()
}
