//go:build darwin && cgo

package mac

/*
#include "draw.h"
*/
import "C"

import (
	"math"
	"slices"

	"downex/ui/kit"
)

// Linear gradients as CSS draws them, with CGGradient: the stops put in order (each at least
// the one before), the gradient line stretched to span them so offsets outside 0..1 work,
// the end colours carried on beyond the ends; a repeating gradient's stops laid out again
// over every period the rectangle covers, as WebKit's CoreGraphics renderer does, since
// CGGradient cannot repeat. CSS interpolates in premultiplied sRGB, CGGradient in straight
// sRGB: the two agree between stops of one alpha, a stop of alpha 0 takes its neighbour's
// colour (exact), and other pairs are split into short pieces (close).

// gradStop is a stop ready for CoreGraphics: a location from 0 to 1 and a straight colour.
type gradStop struct {
	loc float64
	c   kit.Color
}

// premulSteps is how many pieces a stop pair of different colours and alphas is split into.
const premulSteps = 8

// maxPeriods bounds the periods a repeating gradient is laid out over.
const maxPeriods = 4096

// lerpPremul is the colour t of the way from a to b, interpolated with premultiplied alpha.
func lerpPremul(a, b kit.Color, t float32) kit.Color {
	al := a.A + (b.A-a.A)*t
	if al <= 0 {
		return kit.Color{}
	}
	f := func(x, y float32) float32 { return (x*a.A + (y*b.A-x*a.A)*t) / al }
	return kit.Color{R: f(a.R, b.R), G: f(a.G, b.G), B: f(a.B, b.B), A: al}
}

// appendPremul appends the stops that draw from a at la to b at lb with straight
// interpolation as premultiplied interpolation draws it.
func appendPremul(dst []gradStop, a, b kit.Color, la, lb float64) []gradStop {
	sameRGB := a.R == b.R && a.G == b.G && a.B == b.B
	switch {
	case a.A == b.A || sameRGB || la == lb:
	case a.A <= 0:
		a = b.WithAlpha(0)
	case b.A <= 0:
		b = a.WithAlpha(0)
	default:
		dst = appendStop(dst, gradStop{la, a})
		for i := 1; i < premulSteps; i++ {
			t := float32(i) / premulSteps
			dst = append(dst, gradStop{la + (lb-la)*float64(t), lerpPremul(a, b, t)})
		}
		return append(dst, gradStop{lb, b})
	}
	dst = appendStop(dst, gradStop{la, a})
	return append(dst, gradStop{lb, b})
}

// appendStop appends s unless it repeats the last stop.
func appendStop(dst []gradStop, s gradStop) []gradStop {
	if n := len(dst); n > 0 && dst[n-1] == s {
		return dst
	}
	return append(dst, s)
}

// layoutStops lays out stops (already in order, at offsets u from 0 to 1 of one period)
// over n periods: locations (k + u) / n.
func layoutStops(dst []gradStop, stops []kit.GradientStop, lo, hi float32, n int) []gradStop {
	span := float64(hi - lo)
	u := func(o float32) float64 {
		if span <= 0 {
			return 0
		}
		return math.Min(math.Max(float64(o-lo)/span, 0), 1)
	}
	for k := range n {
		prev := stops[0].Offset
		for i := 0; i+1 < len(stops); i++ {
			oa := max(stops[i].Offset, prev)
			ob := max(stops[i+1].Offset, oa)
			prev = ob
			la := (float64(k) + u(oa)) / float64(n)
			lb := (float64(k) + u(ob)) / float64(n)
			dst = appendPremul(dst, stops[i].Color, stops[i+1].Color, la, lb)
		}
	}
	return dst
}

// gradient is a CGGradient of one LinearGradient's stops laid out over n periods (1 when it
// does not repeat). The stops are kept to find it again.
type gradient struct {
	stops  []kit.GradientStop
	repeat bool
	n      int
	g      C.CGGradientRef
	used   uint64
}

func (g *gradient) release() {
	if g.g != 0 {
		C.CGGradientRelease(g.g)
		g.g = 0
	}
}

// stopRange is the stops' first and last offsets in order (the last raised to the first),
// with a line of some length between them.
func stopRange(stops []kit.GradientStop) (lo, hi float32) {
	lo = stops[0].Offset
	hi = lo
	for _, s := range stops {
		hi = max(hi, s.Offset)
	}
	if hi <= lo {
		hi = lo + 1e-6
	}
	return lo, hi
}

func (t *bitmapTarget) drawGradient(r kit.Rect, g kit.LinearGradient) {
	lo, hi := stopRange(g.Stops)
	dx, dy := g.End.X-g.Start.X, g.End.Y-g.Start.Y
	// the line from the first stop to the last
	p0 := kit.Pt(g.Start.X+dx*lo, g.Start.Y+dy*lo)
	vx, vy := dx*(hi-lo), dy*(hi-lo)
	if !g.Repeat {
		e := t.gradient(g, lo, hi, 1)
		if e == nil {
			return
		}
		e.used = t.frame
		C.dx_draw_gradient(t.ctx, e.g, crect(r), cg(p0.X), cg(p0.Y), cg(p0.X+vx), cg(p0.Y+vy))
		return
	}
	// The periods the rectangle's corners project onto, from k0 for n periods.
	l2 := float64(vx*vx + vy*vy)
	if !(l2 > 0) {
		return
	}
	proj := func(x, y float32) float64 { return (float64(x-p0.X)*float64(vx) + float64(y-p0.Y)*float64(vy)) / l2 }
	s0, s1, s2, s3 := proj(r.Min.X, r.Min.Y), proj(r.Max.X, r.Min.Y), proj(r.Min.X, r.Max.Y), proj(r.Max.X, r.Max.Y)
	a, b := min(s0, s1, s2, s3), max(s0, s1, s2, s3)
	k0 := math.Floor(a)
	// [render] Over more periods than are laid out, a period is far below a pixel: the
	// rectangle takes the gradient's average colour, as its pixels would (before, only the
	// first maxPeriods periods were painted and the rest left empty).
	if !(b-k0 <= maxPeriods) {
		C.dx_fill_rect(t.ctx, crect(r), paint(averageColor(g.Stops, lo, hi)))
		return
	} // [/render]
	n := min(int(math.Ceil(b-k0)), maxPeriods)
	// CoreGraphics samples a gradient into a table of about a thousand colours, which over
	// a long bar moves a hard edge by up to a pixel. The periods are drawn in chunks of m
	// periods no longer than chunkPixels, each a CGGradient of m periods (one, cached, for
	// every chunk): the table then places edges within a few hundredths of a pixel.
	a0, b0, c0, d0, _, _ := t.canvas.xf.Elems()
	scale := math.Sqrt(math.Abs(float64(a0*d0-b0*c0))) * float64(t.ppd)
	period := math.Sqrt(l2) * scale // device pixels
	m := 1
	if period > 0 {
		m = max(1, int(chunkPixels/period))
	}
	m = min(m, n)
	e := t.gradient(g, lo, hi, m)
	if e == nil {
		return
	}
	e.used = t.frame
	C.dx_draw_repeating(t.ctx, e.g, crect(r), cg(p0.X), cg(p0.Y), cg(vx), cg(vy), C.CGFloat(k0), C.int(n), C.int(m))
}

// chunkPixels is the longest stretch of a repeating gradient one CGGradient draws.
const chunkPixels = 64

// gradient is the cached CGGradient of g laid out over n periods, made on first use.
func (t *bitmapTarget) gradient(g kit.LinearGradient, lo, hi float32, n int) *gradient {
	for _, c := range t.gradients {
		if c.n == n && c.repeat == g.Repeat && slices.Equal(c.stops, g.Stops) {
			return c
		}
	}
	stops := layoutStops(nil, g.Stops, lo, hi, n)
	if len(stops) < 2 {
		return nil
	}
	comps := make([]C.CGFloat, 4*len(stops))
	locs := make([]C.CGFloat, len(stops))
	for i, s := range stops {
		comps[4*i], comps[4*i+1], comps[4*i+2], comps[4*i+3] = cg(s.c.R), cg(s.c.G), cg(s.c.B), cg(s.c.A)
		locs[i] = C.CGFloat(min(max(s.loc, 0), 1))
	}
	cgg := C.dx_gradient_new(&comps[0], &locs[0], C.int(len(stops)))
	if cgg == 0 {
		return nil
	}
	e := &gradient{stops: slices.Clone(g.Stops), repeat: g.Repeat, n: n, g: cgg}
	t.gradients = append(t.gradients, e)
	return e
}

// [render] averageColor is the mean colour of one period of the stops (in order, from lo
// to hi), averaged with premultiplied alpha as CSS interpolates.
func averageColor(stops []kit.GradientStop, lo, hi float32) kit.Color {
	span := float64(hi - lo)
	if len(stops) == 1 || !(span > 0) {
		return stops[len(stops)-1].Color
	}
	var r, g, b, a, total float64
	prev := stops[0].Offset
	for i := 0; i+1 < len(stops); i++ {
		oa := max(stops[i].Offset, prev)
		ob := max(stops[i+1].Offset, oa)
		prev = ob
		w := float64(ob-oa) / span
		ca, cb := stops[i].Color, stops[i+1].Color
		r += w * float64(ca.R*ca.A+cb.R*cb.A) / 2
		g += w * float64(ca.G*ca.A+cb.G*cb.A) / 2
		b += w * float64(ca.B*ca.A+cb.B*cb.A) / 2
		a += w * float64(ca.A+cb.A) / 2
		total += w
	}
	if !(total > 0) {
		return stops[len(stops)-1].Color
	}
	r, g, b, a = r/total, g/total, b/total, a/total
	if a <= 0 {
		return kit.Color{}
	}
	return kit.Color{R: float32(r / a), G: float32(g / a), B: float32(b / a), A: float32(a)}
}

// [/render]
