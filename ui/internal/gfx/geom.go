package gfx

import (
	"math"

	"downex/ui/kit"
)

// RoundOut is r rounded out to whole device pixels at ppd pixels per DIP, in DIPs: what a
// frame's dirty area becomes (a 1e-3 pixel tolerance keeps float error from adding a row).
func RoundOut(r kit.Rect, ppd float32) kit.Rect {
	f := func(v float32, up bool) float32 {
		p := float64(v * ppd)
		if up {
			p = math.Ceil(p - 1e-3)
		} else {
			p = math.Floor(p + 1e-3)
		}
		return float32(p) / ppd
	}
	return kit.R(f(r.Min.X, false), f(r.Min.Y, false), f(r.Max.X, true), f(r.Max.Y, true))
}

// XFBounds is the bounds of r through m.
func XFBounds(m kit.Affine, r kit.Rect) kit.Rect {
	a, b := m.Apply(r.Min), m.Apply(r.Max)
	c, e := m.Apply(kit.Pt(r.Max.X, r.Min.Y)), m.Apply(kit.Pt(r.Min.X, r.Max.Y))
	return kit.R(min(a.X, b.X, c.X, e.X), min(a.Y, b.Y, c.Y, e.Y), max(a.X, b.X, c.X, e.X), max(a.Y, b.Y, c.Y, e.Y))
}

// ArcKind is what an SVG arc from one point to another draws once its out-of-range
// parameters are handled (SVG 1.1 implementation notes F.6.2).
type ArcKind uint8

// The arc kinds.
const (
	ArcNone    ArcKind = iota // equal end points: nothing
	ArcLine                   // a zero radius: a straight line to the end point
	ArcEllipse                // an elliptical arc with the radii ArcRadii returns
)

// ArcRadii is how the SVG arc op from cur is drawn: nothing, a line, or an arc on radii
// that are made absolute and, when too small to reach the end point, scaled up by
// sqrt(lambda) (F.6.6) with a margin of 1e-6 so that rounding never leaves them short.
func ArcRadii(cur kit.Point, op kit.PathOp) (kind ArcKind, rx, ry float64) {
	end := op.P[0]
	if end == cur {
		return ArcNone, 0, 0
	}
	rx, ry = math.Abs(float64(op.Radii.W)), math.Abs(float64(op.Radii.H))
	if rx == 0 || ry == 0 {
		return ArcLine, 0, 0
	}
	phi := float64(op.Rotation) * math.Pi / 180
	sin, cos := math.Sincos(phi)
	dx, dy := float64(cur.X-end.X)/2, float64(cur.Y-end.Y)/2
	x1, y1 := cos*dx+sin*dy, -sin*dx+cos*dy
	if lambda := x1*x1/(rx*rx) + y1*y1/(ry*ry); lambda > 1 {
		s := math.Sqrt(lambda) * (1 + 1e-6)
		rx, ry = rx*s, ry*s
	}
	return ArcEllipse, rx, ry
}

// ArcToCubics appends to dst the cubic Béziers (three points each: two control points and
// the end point) of the SVG arc op from cur, converted from SVG's end-point form to its
// centre form (F.6.5) and split into pieces of at most 90°. A line (zero radius) is one
// straight cubic; equal end points append nothing.
func ArcToCubics(dst []kit.Point, cur kit.Point, op kit.PathOp) []kit.Point {
	kind, rx, ry := ArcRadii(cur, op)
	end := op.P[0]
	switch kind {
	case ArcNone:
		return dst
	case ArcLine:
		return append(dst, cur, end, end)
	}
	phi := float64(op.Rotation) * math.Pi / 180
	sin, cos := math.Sincos(phi)
	x1p, y1p := float64(cur.X), float64(cur.Y)
	x2p, y2p := float64(end.X), float64(end.Y)
	dx, dy := (x1p-x2p)/2, (y1p-y2p)/2
	xp, yp := cos*dx+sin*dy, -sin*dx+cos*dy
	rx2, ry2 := rx*rx, ry*ry
	num := rx2*ry2 - rx2*yp*yp - ry2*xp*xp
	den := rx2*yp*yp + ry2*xp*xp
	co := 0.0 // radii scaled up to just reach: the centre is the chord's middle
	r0x, r0y := math.Abs(float64(op.Radii.W)), math.Abs(float64(op.Radii.H))
	if scaled := xp*xp/(r0x*r0x)+yp*yp/(r0y*r0y) >= 1; !scaled && den > 0 && num > 0 {
		co = math.Sqrt(num / den)
	}
	if op.Large == op.Sweep {
		co = -co
	}
	cxp, cyp := co*rx*yp/ry, -co*ry*xp/rx
	cx := cos*cxp - sin*cyp + (x1p+x2p)/2
	cy := sin*cxp + cos*cyp + (y1p+y2p)/2
	angle := func(ux, uy, vx, vy float64) float64 {
		a := math.Atan2(ux*vy-uy*vx, ux*vx+uy*vy)
		return a
	}
	ux, uy := (xp-cxp)/rx, (yp-cyp)/ry
	vx, vy := (-xp-cxp)/rx, (-yp-cyp)/ry
	theta := angle(1, 0, ux, uy)
	delta := angle(ux, uy, vx, vy)
	if !op.Sweep && delta > 0 {
		delta -= 2 * math.Pi
	} else if op.Sweep && delta < 0 {
		delta += 2 * math.Pi
	}
	n := int(math.Ceil(math.Abs(delta)/(math.Pi/2) - 1e-9))
	if n < 1 {
		n = 1
	}
	step := delta / float64(n)
	k := 4.0 / 3 * math.Tan(step/4)
	pt := func(t float64) (x, y, tx, ty float64) {
		st, ct := math.Sincos(t)
		ex, ey := rx*ct, ry*st
		dex, dey := -rx*st, ry*ct
		return cos*ex - sin*ey + cx, sin*ex + cos*ey + cy, cos*dex - sin*dey, sin*dex + cos*dey
	}
	t := theta
	px, py, ptx, pty := pt(t)
	for i := 0; i < n; i++ {
		t2 := theta + step*float64(i+1)
		qx, qy, qtx, qty := pt(t2)
		c1 := kit.Pt(float32(px+k*ptx), float32(py+k*pty))
		c2 := kit.Pt(float32(qx-k*qtx), float32(qy-k*qty))
		q := kit.Pt(float32(qx), float32(qy))
		if i == n-1 {
			q = end // land exactly on the end point
		}
		dst = append(dst, c1, c2, q)
		px, py, ptx, pty = qx, qy, qtx, qty
	}
	return dst
}
