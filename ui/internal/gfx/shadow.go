package gfx

import (
	"math"

	"downex/ui/kit"
)

// CSS box-shadow (kit.Shadow) as an alpha mask: a blurred shadow's alpha is computed once
// per size on the CPU (a Gaussian, as three box blurs for wide ones, the approximation
// SVG's feGaussianBlur describes), with the box cut out of an outer shadow so that it
// never shows through a translucent fill. Masks of large boxes are computed for a box
// shrunk to where the shadow stops changing, and drawn in nine stretched pieces.

// GrownRadius is a corner radius r grown by d > 0 as CSS grows a spread shadow's radii,
// keeping small corners sharper (Backgrounds 3, "spread distance"); a d < 0 shrinks it,
// flooring at 0.
func GrownRadius(r, d float32) float32 {
	if d < 0 {
		return max(r+d, 0)
	}
	if r <= 0 {
		return 0
	}
	if r < d {
		q := r/d - 1
		d *= 1 + q*q*q
	}
	return r + d
}

func SpreadRRect(b kit.RRect, d float32) kit.RRect {
	q := b.Radii
	return kit.RRect{
		Rect: b.Rect.Inset(kit.UniformInsets(-d)),
		Radii: kit.Radii{TopLeft: GrownRadius(q.TopLeft, d), TopRight: GrownRadius(q.TopRight, d),
			BottomRight: GrownRadius(q.BottomRight, d), BottomLeft: GrownRadius(q.BottomLeft, d)},
	}
}

// ShadowShape is, in coordinates of the box's top-left corner, the shape an outer shadow
// casts or the inner edge of an inset one.
func ShadowShape(size kit.Size, radii kit.Radii, s kit.Shadow) kit.RRect {
	b := kit.RRect{Rect: kit.R(0, 0, size.W, size.H), Radii: radii}
	d := s.Spread
	if s.Inset {
		d = -d
	}
	sh := SpreadRRect(b, d)
	sh.Rect = sh.Rect.Add(s.Offset)
	return sh.Normalized()
}

// ShadowKey is a blurred shadow in device pixels: the box's size and radii, the shadow's
// offset, spread and blur.
type ShadowKey struct {
	W, H                 int32
	TL, TR, BR, BL       float32
	OX, OY, Spread, Blur float32
	Inset                bool
}

func ShadowKeyOf(r kit.RRect, s kit.Shadow, ppd float32) ShadowKey {
	q := r.Radii
	return ShadowKey{
		W: int32(math.Round(float64(r.Rect.Width() * ppd))), H: int32(math.Round(float64(r.Rect.Height() * ppd))),
		TL: q.TopLeft * ppd, TR: q.TopRight * ppd, BR: q.BottomRight * ppd, BL: q.BottomLeft * ppd,
		OX: s.Offset.X * ppd, OY: s.Offset.Y * ppd, Spread: s.Spread * ppd, Blur: s.Blur * ppd, Inset: s.Inset,
	}
}

// MaskLayout places a blurred shadow's alpha: W × H pixels whose (0, 0) is (X0, Y0) from
// the box's top-left corner. When DX > 0 the column CX stands for DX + 1 columns of the
// real box (the mask was made for a box DX narrower), and the same for rows.
type MaskLayout struct {
	X0, Y0, W, H   int
	CX, DX, CY, DY int
}

// ShadowAlpha computes the mask of k: its placement and its alpha bytes, row by row.
func ShadowAlpha(k ShadowKey) (*MaskLayout, []byte) {
	sigma := float64(k.Blur) / 2
	e := int(math.Ceil(3 * sigma))
	radii := kit.Radii{TopLeft: k.TL, TopRight: k.TR, BottomRight: k.BR, BottomLeft: k.BL}
	s := kit.Shadow{Offset: kit.Pt(k.OX, k.OY), Spread: k.Spread, Inset: k.Inset}
	W, H := float32(k.W), float32(k.H)
	if W <= 0 || H <= 0 {
		return nil, nil
	}
	// Where the mask is not constant along x and y: near the box's corners, and within the
	// blur of the shape's edges and corners.
	geom := func(w, h float32) (box, shape kit.RRect, region kit.Rect) {
		box = kit.RRect{Rect: kit.R(0, 0, w, h), Radii: radii}.Normalized()
		shape = ShadowShape(kit.Size{W: w, H: h}, radii, s)
		if k.Inset {
			region = box.Rect
		} else {
			region = shape.Rect.Inset(kit.UniformInsets(float32(-e)))
		}
		return
	}
	box, shape, region := geom(W, H)
	if shape.Rect.Empty() && !k.Inset {
		return nil, nil
	}
	maxR := func(q kit.Radii) float32 { return max(q.TopLeft, q.TopRight, q.BottomRight, q.BottomLeft) }
	ef := float32(e)
	fx0 := max(shape.Rect.Min.X+maxR(shape.Radii)+ef, maxR(box.Radii)) + 1
	fx1 := min(shape.Rect.Max.X-maxR(shape.Radii)-ef, W-maxR(box.Radii)) - 1
	fy0 := max(shape.Rect.Min.Y+maxR(shape.Radii)+ef, maxR(box.Radii)) + 1
	fy1 := min(shape.Rect.Max.Y-maxR(shape.Radii)-ef, H-maxR(box.Radii)) - 1
	X0, Y0 := int(math.Floor(float64(region.Min.X))), int(math.Floor(float64(region.Min.Y)))
	cut := func(f0, f1 float32, o int) (c, delta int) {
		// columns i whose centres o+i+0.5 lie in [f0, f1]
		i0 := int(math.Ceil(float64(f0 - 0.5 - float32(o))))
		i1 := int(math.Floor(float64(f1 - 0.5 - float32(o))))
		if i1-i0+1 <= 3 {
			return 0, 0
		}
		return i0 + 1, i1 - i0 + 1 - 3
	}
	cx, dx := cut(fx0, fx1, X0)
	cy, dy := cut(fy0, fy1, Y0)
	if dx > 0 || dy > 0 {
		box, shape, region = geom(W-float32(dx), H-float32(dy))
	}
	mw := int(math.Ceil(float64(region.Max.X))) - X0
	mh := int(math.Ceil(float64(region.Max.Y))) - Y0
	if mw <= 0 || mh <= 0 || mw > 8192 || mh > 8192 {
		return nil, nil
	}
	// An inset shadow's blur reads the source e pixels beyond the box, where it is 1; an
	// outer shadow's source is 0 beyond its region.
	pad := 0
	if k.Inset {
		pad = e
	}
	pw, ph := mw+2*pad, mh+2*pad
	src := make([]float32, pw*ph)
	for j := 0; j < ph; j++ {
		y := float32(Y0-pad+j) + 0.5
		for i := 0; i < pw; i++ {
			x := float32(X0-pad+i) + 0.5
			c := Coverage(shape, x, y)
			if k.Inset {
				c = 1 - c
			}
			src[j*pw+i] = c
		}
	}
	if sigma > 0 {
		tmp := make([]float32, max(pw, 2*ph))
		blurRows(src, tmp, pw, ph, sigma)
		blurCols(src, tmp, pw, ph, sigma)
	}
	out := make([]byte, mw*mh)
	for j := 0; j < mh; j++ {
		y := float32(Y0+j) + 0.5
		for i := 0; i < mw; i++ {
			x := float32(X0+i) + 0.5
			v := src[(j+pad)*pw+i+pad]
			b := Coverage(box, x, y)
			if k.Inset {
				v *= b
			} else {
				v *= 1 - b
			}
			out[j*mw+i] = byte(min(max(v, 0), 1)*255 + 0.5)
		}
	}
	return &MaskLayout{X0: X0, Y0: Y0, W: mw, H: mh, CX: cx, DX: dx, CY: cy, DY: dy}, out
}

// coverage is how much of the pixel centred at (x, y) the rounded rectangle r covers, from
// its signed distance.
func Coverage(r kit.RRect, x, y float32) float32 {
	if r.Rect.Empty() {
		return 0
	}
	cx, cy := (r.Rect.Min.X+r.Rect.Max.X)/2, (r.Rect.Min.Y+r.Rect.Max.Y)/2
	hw, hh := r.Rect.Width()/2, r.Rect.Height()/2
	var rad float32
	switch {
	case x < cx && y < cy:
		rad = r.Radii.TopLeft
	case x >= cx && y < cy:
		rad = r.Radii.TopRight
	case x >= cx:
		rad = r.Radii.BottomRight
	default:
		rad = r.Radii.BottomLeft
	}
	qx := float32(math.Abs(float64(x-cx))) - (hw - rad)
	qy := float32(math.Abs(float64(y-cy))) - (hh - rad)
	ox, oy := max(qx, 0), max(qy, 0)
	sd := float32(math.Sqrt(float64(ox*ox+oy*oy))) + min(max(qx, qy), 0) - rad
	return min(max(0.5-sd, 0), 1)
}

// blurRows blurs each row of the w × h image v by a Gaussian of standard deviation sigma
// (pixels), using tmp of the same size.
func blurRows(v, tmp []float32, w, h int, sigma float64) {
	for j := 0; j < h; j++ {
		blur1D(v[j*w:(j+1)*w], tmp[:w], 1, sigma)
	}
}

func blurCols(v, tmp []float32, w, h int, sigma float64) {
	col, buf := tmp[:h], tmp[h:2*h]
	for i := 0; i < w; i++ {
		for j := 0; j < h; j++ {
			col[j] = v[j*w+i]
		}
		blur1D(col, buf, 1, sigma)
		for j := 0; j < h; j++ {
			v[j*w+i] = col[j]
		}
	}
}

// blur1D blurs line in place (tmp is scratch of its length): an exact Gaussian kernel for a
// narrow blur, else three box blurs of the sizes SVG's feGaussianBlur gives (an odd size d
// three times centred; an even d twice, off-centre either way, then d+1 centred).
func blur1D(line, tmp []float32, _ int, sigma float64) {
	n := len(line)
	if n == 0 {
		return
	}
	if sigma < 2 {
		rad := int(math.Ceil(3 * sigma))
		var kern [16]float64
		ks := kern[:2*rad+1]
		var sum float64
		for i := range ks {
			x := float64(i - rad)
			ks[i] = math.Exp(-x * x / (2 * sigma * sigma))
			sum += ks[i]
		}
		copy(tmp, line)
		for i := 0; i < n; i++ {
			var acc float64
			for t, kv := range ks {
				if j := i + t - rad; j >= 0 && j < n {
					acc += kv * float64(tmp[j])
				}
			}
			line[i] = float32(acc / sum)
		}
		return
	}
	d := int(math.Floor(sigma*3*math.Sqrt(2*math.Pi)/4 + 0.5))
	if d%2 == 1 {
		for range 3 {
			boxBlur(line, tmp, (d-1)/2, (d-1)/2)
		}
		return
	}
	boxBlur(line, tmp, d/2, d/2-1)
	boxBlur(line, tmp, d/2-1, d/2)
	boxBlur(line, tmp, d/2, d/2)
}

// boxBlur replaces each value by the mean of the lo values before it, itself and the hi
// after it (zero outside the line).
func boxBlur(line, tmp []float32, lo, hi int) {
	n := len(line)
	copy(tmp, line)
	size := float32(lo + hi + 1)
	var sum float32
	for j := 0; j <= hi && j < n; j++ {
		sum += tmp[j]
	}
	for i := 0; i < n; i++ {
		line[i] = sum / size
		if j := i + hi + 1; j < n {
			sum += tmp[j]
		}
		if j := i - lo; j >= 0 {
			sum -= tmp[j]
		}
	}
}
