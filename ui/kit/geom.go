package kit

import "math"

// DIP is a length in device-independent pixels: 1/96 inch, which is the CSS px. Layout,
// drawing and input all work in DIPs; only a backend deals in device pixels (see Scale).
type DIP = float32

// Point is a position in DIPs.
type Point struct {
	X, Y DIP
}

// Pt is the point (x, y).
func Pt(x, y DIP) Point { return Point{x, y} }

// Add is p moved by q.
func (p Point) Add(q Point) Point { return Point{p.X + q.X, p.Y + q.Y} }

// Sub is p moved back by q.
func (p Point) Sub(q Point) Point { return Point{p.X - q.X, p.Y - q.Y} }

// Size is a width and a height in DIPs.
type Size struct {
	W, H DIP
}

// Rect is the axis-aligned rectangle from Min, its top-left corner (inside it), to Max,
// its bottom-right corner (just outside it). A Rect whose Max is not to the right of and
// below its Min is empty, and every empty Rect means the same: no area.
type Rect struct {
	Min, Max Point
}

// R is the rectangle from (x0, y0) to (x1, y1).
func R(x0, y0, x1, y1 DIP) Rect { return Rect{Point{x0, y0}, Point{x1, y1}} }

// RectAt is the rectangle of size s whose top-left corner is p.
func RectAt(p Point, s Size) Rect { return Rect{p, Point{p.X + s.W, p.Y + s.H}} }

// Width is Max.X - Min.X (zero or less for some empty rectangles).
func (r Rect) Width() DIP { return r.Max.X - r.Min.X }

// Height is Max.Y - Min.Y (zero or less for some empty rectangles).
func (r Rect) Height() DIP { return r.Max.Y - r.Min.Y }

// Size is the rectangle's width and height.
func (r Rect) Size() Size { return Size{r.Width(), r.Height()} }

// Empty reports whether r holds no area (also when a coordinate is NaN).
func (r Rect) Empty() bool { return !(r.Max.X > r.Min.X && r.Max.Y > r.Min.Y) }

// Contains reports whether p is inside r: a point on the top or left edge is, one on the
// bottom or right edge is not.
func (r Rect) Contains(p Point) bool {
	return p.X >= r.Min.X && p.X < r.Max.X && p.Y >= r.Min.Y && p.Y < r.Max.Y
}

// Intersect is the area r and s share, or Rect{} when they share none.
func (r Rect) Intersect(s Rect) Rect {
	out := Rect{
		Point{max(r.Min.X, s.Min.X), max(r.Min.Y, s.Min.Y)},
		Point{min(r.Max.X, s.Max.X), min(r.Max.Y, s.Max.Y)},
	}
	if out.Empty() {
		return Rect{}
	}
	return out
}

// Union is the smallest rectangle that holds r and s; an empty one adds nothing.
func (r Rect) Union(s Rect) Rect {
	switch {
	case r.Empty() && s.Empty():
		return Rect{}
	case r.Empty():
		return s
	case s.Empty():
		return r
	}
	return Rect{
		Point{min(r.Min.X, s.Min.X), min(r.Min.Y, s.Min.Y)},
		Point{max(r.Max.X, s.Max.X), max(r.Max.Y, s.Max.Y)},
	}
}

// Add is r moved by p.
func (r Rect) Add(p Point) Rect { return Rect{r.Min.Add(p), r.Max.Add(p)} }

// Inset is r with in taken off its sides; negative insets grow it.
func (r Rect) Inset(in Insets) Rect {
	return Rect{
		Point{r.Min.X + in.Left, r.Min.Y + in.Top},
		Point{r.Max.X - in.Right, r.Max.Y - in.Bottom},
	}
}

// Center is the point in the middle of r.
func (r Rect) Center() Point { return Point{(r.Min.X + r.Max.X) / 2, (r.Min.Y + r.Max.Y) / 2} }

// Insets are distances in from the sides of a rectangle: padding, border widths, a
// frame's margins. The sides are physical; the kit maps CSS's logical inline-start and
// inline-end onto Left and Right for the layout's direction.
type Insets struct {
	Top, Right, Bottom, Left DIP
}

// UniformInsets is d on every side.
func UniformInsets(d DIP) Insets { return Insets{d, d, d, d} }

// Radii are the radii of the four corners of a rounded rectangle. The corners are
// physical; the kit maps CSS's logical ones (border-start-end-radius and the others) for
// the layout's direction.
type Radii struct {
	TopLeft, TopRight, BottomRight, BottomLeft DIP
}

// UniformRadii is r at every corner.
func UniformRadii(r DIP) Radii { return Radii{r, r, r, r} }

// RRect is a rectangle with rounded corners, each a quarter circle of its radius: CSS
// border-radius with one length per corner. Canvas methods make the radii fit first
// (see Normalized), so a pill may ask for 999.
type RRect struct {
	Rect  Rect
	Radii Radii
}

// RoundRect is r with radius at every corner.
func RoundRect(r Rect, radius DIP) RRect { return RRect{r, UniformRadii(radius)} }

// Uniform reports the radius when every corner has the same one.
func (r RRect) Uniform() (DIP, bool) {
	q := r.Radii
	same := q.TopLeft == q.TopRight && q.TopLeft == q.BottomRight && q.TopLeft == q.BottomLeft
	return q.TopLeft, same
}

// Normalized is r with radii that fit, as CSS Backgrounds 3 §5.5 makes them: a negative
// radius becomes 0, and when the two radii along a side add up to more than the side is
// long, every radius is scaled by the one factor that makes them all fit.
func (r RRect) Normalized() RRect {
	q := r.Radii
	q.TopLeft, q.TopRight = max(q.TopLeft, 0), max(q.TopRight, 0)
	q.BottomRight, q.BottomLeft = max(q.BottomRight, 0), max(q.BottomLeft, 0)
	w, h := max(r.Rect.Width(), 0), max(r.Rect.Height(), 0)
	f := DIP(1)
	fit := func(side, a, b DIP) {
		if s := a + b; s > side {
			f = min(f, side/s)
		}
	}
	fit(w, q.TopLeft, q.TopRight)
	fit(w, q.BottomLeft, q.BottomRight)
	fit(h, q.TopLeft, q.BottomLeft)
	fit(h, q.TopRight, q.BottomRight)
	if f < 1 {
		q = Radii{q.TopLeft * f, q.TopRight * f, q.BottomRight * f, q.BottomLeft * f}
	}
	return RRect{r.Rect, q}
}

// Inset is r with d taken off every side and off every radius that is not 0, no radius
// going below 0. Inset(w/2) is the centre line of a border w wide inside the box (what
// StrokeRoundedRect strokes); a negative d gives an outline -d outside it, as a focus
// ring's. Sharp corners stay sharp, as in CSS.
func (r RRect) Inset(d DIP) RRect {
	in := func(v DIP) DIP {
		if v <= 0 {
			return 0
		}
		return max(v-d, 0)
	}
	q := r.Radii
	q = Radii{in(q.TopLeft), in(q.TopRight), in(q.BottomRight), in(q.BottomLeft)}
	return RRect{r.Rect.Inset(UniformInsets(d)), q}
}

// Affine is a 2-D affine transform: it maps the point (x, y) to (a·x + c·y + e,
// b·x + d·y + f), the matrix SVG writes matrix(a b c d e f) and CSS transforms produce.
// Its layout differs from the matrix so that the zero value is the identity: build one
// with the functions below and read it with Elems.
type Affine struct {
	a1, b, c, d1, e, f float32 // a and d are kept less 1
}

// AffineOf is the transform SVG writes matrix(a b c d e f).
func AffineOf(a, b, c, d, e, f float32) Affine { return Affine{a - 1, b, c, d - 1, e, f} }

// Elems are m's a, b, c, d, e and f (see Affine): Direct2D's D2D1_MATRIX_3X2_F holds them
// in this order as _11, _12, _21, _22, _31 and _32.
func (m Affine) Elems() (a, b, c, d, e, f float32) {
	return m.a1 + 1, m.b, m.c, m.d1 + 1, m.e, m.f
}

// Translation moves by (dx, dy).
func Translation(dx, dy DIP) Affine { return Affine{e: dx, f: dy} }

// Scaling scales by sx and sy about the origin.
func Scaling(sx, sy float32) Affine { return Affine{a1: sx - 1, d1: sy - 1} }

// Rotation turns by deg degrees about the origin, clockwise on the screen (y points
// down) for a positive angle, as CSS rotate() does.
func Rotation(deg float32) Affine {
	s, c := math.Sincos(float64(deg) * math.Pi / 180)
	return AffineOf(float32(c), float32(s), float32(-s), float32(c), 0, 0)
}

// Then is m followed by n: the transform that applies m first, then n.
func (m Affine) Then(n Affine) Affine {
	a1, b1, c1, d1, e1, f1 := m.Elems()
	a2, b2, c2, d2, e2, f2 := n.Elems()
	return AffineOf(
		a2*a1+c2*b1, b2*a1+d2*b1,
		a2*c1+c2*d1, b2*c1+d2*d1,
		a2*e1+c2*f1+e2, b2*e1+d2*f1+f2,
	)
}

// Apply is p transformed by m.
func (m Affine) Apply(p Point) Point {
	a, b, c, d, e, f := m.Elems()
	return Point{a*p.X + c*p.Y + e, b*p.X + d*p.Y + f}
}

// Invert is the transform that undoes m (to map a window point into a transformed
// widget), or false when m cannot be undone (it squashes the plane onto a line).
func (m Affine) Invert() (Affine, bool) {
	ma, mb, mc, md, me, mf := m.Elems()
	a, b, c, d := float64(ma), float64(mb), float64(mc), float64(md)
	e, f := float64(me), float64(mf)
	det := a*d - b*c
	if det == 0 || math.IsNaN(det) || math.IsInf(det, 0) {
		return Affine{}, false
	}
	inv := func(v float64) float32 { return float32(v / det) }
	return AffineOf(inv(d), inv(-b), inv(-c), inv(a), inv(c*f-d*e), inv(b*e-a*f)), true
}
