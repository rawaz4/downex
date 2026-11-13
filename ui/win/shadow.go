//go:build windows

package win

import (
	"unsafe"

	"downex/ui/internal/gfx"
	"downex/ui/kit"
	"downex/ui/win/internal/com"
	"downex/ui/win/internal/d2d"
)

// CSS box-shadow (kit.Shadow). A sharp shadow (Blur 0: focus rings, hairline insets) is a
// geometry, the shadow's shape less the box, filled exactly. A blurred one is an alpha mask
// computed once per size on the CPU (a Gaussian, as three box blurs for wide ones, the
// approximation SVG's feGaussianBlur describes), with the box cut out of an outer shadow so
// that it never shows through a translucent fill. Masks of large boxes are computed for a
// box shrunk to where the shadow stops changing, and drawn in nine stretched pieces.

func (d *device) drawShadow(r kit.RRect, s kit.Shadow) {
	if s.Blur == 0 {
		g := d.res.shadowRing(r.Rect.Size(), r.Radii, s)
		if g == nil {
			return
		}
		c := &d.canvas
		c.moveTo(r.Rect.Min)
		d.rt.FillGeometry(g, c.brush(s.Color))
		c.moveBack()
		return
	}
	k := shadowKeyOf(r, s, d.ppd)
	m, ok := d.shadows[k]
	if !ok {
		if m = d.makeShadowMask(k); m == nil {
			// A mask that cannot be made is cached empty, so it is not tried every frame,
			// and ages out as the others do.
			m = &shadowMask{}
		}
		d.shadows[k] = m
	}
	m.used = d.frame
	if m.bmp == nil {
		return
	}
	// The mask is in device pixels from the box's top-left corner.
	ox, oy := r.Rect.Min.X+float32(m.x0)/d.ppd, r.Rect.Min.Y+float32(m.y0)/d.ppd
	cols := [3][2]int{{0, m.w}}
	nc := 1
	if m.dx > 0 {
		cols = [3][2]int{{0, m.cx}, {m.cx, m.cx + 1}, {m.cx + 1, m.w}}
		nc = 3
	}
	rows := [3][2]int{{0, m.h}}
	nr := 1
	if m.dy > 0 {
		rows = [3][2]int{{0, m.cy}, {m.cy, m.cy + 1}, {m.cy + 1, m.h}}
		nr = 3
	}
	d.rt.SetAntialiasMode(d2d.ANTIALIAS_MODE_ALIASED) // FillOpacityMask needs it
	b := d.canvas.brush(s.Color)
	for i := 0; i < nr; i++ {
		y0, y1 := rows[i][0], rows[i][1]
		dy0, dy1 := y0, y1
		if nr == 3 {
			if i >= 1 {
				dy1 += m.dy
			}
			if i == 2 {
				dy0 += m.dy
			}
		}
		for j := 0; j < nc; j++ {
			x0, x1 := cols[j][0], cols[j][1]
			if x1 <= x0 || y1 <= y0 {
				continue
			}
			dx0, dx1 := x0, x1
			if nc == 3 {
				if j >= 1 {
					dx1 += m.dx
				}
				if j == 2 {
					dx0 += m.dx
				}
			}
			src := d2d.RectF{Left: float32(x0), Top: float32(y0), Right: float32(x1), Bottom: float32(y1)}
			dst := d2d.RectF{Left: ox + float32(dx0)/d.ppd, Top: oy + float32(dy0)/d.ppd, Right: ox + float32(dx1)/d.ppd, Bottom: oy + float32(dy1)/d.ppd}
			d.rt.FillOpacityMask(m.bmp, b, &dst, &src)
		}
	}
	d.rt.SetAntialiasMode(d2d.ANTIALIAS_MODE_PER_PRIMITIVE)
}

// shadowKey is a blurred shadow in device pixels (the shared gfx.ShadowKey).
type shadowKey = gfx.ShadowKey

func shadowKeyOf(r kit.RRect, s kit.Shadow, ppd float32) shadowKey { return gfx.ShadowKeyOf(r, s, ppd) }

// shadowMask is a blurred shadow's alpha on a device: w × h pixels whose (0, 0) is (x0, y0)
// from the box's top-left corner. When dx > 0 the column cx stands for dx + 1 columns of
// the real box (the mask was made for a box dx narrower), and the same for rows.
type shadowMask struct {
	bmp            *d2d.Bitmap
	x0, y0, w, h   int
	cx, dx, cy, dy int
	used           uint64
}

func (m *shadowMask) release() {
	if m != nil {
		com.Release(&m.bmp)
	}
}

// shadowAlpha computes the mask of k (gfx.ShadowAlpha): its placement and its alpha bytes,
// row by row.
func shadowAlpha(k shadowKey) (*shadowMask, []byte) {
	l, pix := gfx.ShadowAlpha(k)
	if l == nil {
		return nil, pix
	}
	return &shadowMask{x0: l.X0, y0: l.Y0, w: l.W, h: l.H, cx: l.CX, dx: l.DX, cy: l.CY, dy: l.DY}, pix
}

// shadowShape is gfx.ShadowShape.
func shadowShape(size kit.Size, radii kit.Radii, s kit.Shadow) kit.RRect {
	return gfx.ShadowShape(size, radii, s)
}

func (d *device) makeShadowMask(k shadowKey) *shadowMask {
	m, pix := shadowAlpha(k)
	if m == nil || len(pix) == 0 {
		return nil
	}
	p := d2d.BitmapProperties{PixelFormat: d2d.PixelFormat{Format: d2d.DXGI_FORMAT_A8_UNORM, AlphaMode: d2d.ALPHA_MODE_PREMULTIPLIED}, DpiX: 96, DpiY: 96}
	b, hr := d.rt.CreateBitmap(uint32(m.w), uint32(m.h), unsafe.Pointer(&pix[0]), uint32(m.w), &p)
	if hr.Failed() {
		// A target without A8 bitmaps: the mask's alpha in BGRA works the same.
		bgra := make([]byte, 4*len(pix))
		for i, a := range pix {
			bgra[4*i], bgra[4*i+1], bgra[4*i+2], bgra[4*i+3] = a, a, a, a
		}
		p.PixelFormat.Format = d2d.DXGI_FORMAT_B8G8R8A8_UNORM
		if b, hr = d.rt.CreateBitmap(uint32(m.w), uint32(m.h), unsafe.Pointer(&bgra[0]), uint32(4*m.w), &p); hr.Failed() {
			return nil
		}
	}
	m.bmp = b
	return m
}

// shadowRing is the geometry of a sharp shadow (Blur 0) of a box of size and radii at
// (0, 0): the spread shape less the box for an outer shadow, the box less the inner edge
// for an inset one. Geometries are device-independent and kept by shape.
func (r *Resources) shadowRing(size kit.Size, radii kit.Radii, s kit.Shadow) *d2d.Geometry {
	k := ringKey{size, radii, s.Offset, s.Spread, s.Inset}
	if g, ok := r.rings[k]; ok {
		return g.geometry()
	}
	box := kit.RRect{Rect: kit.R(0, 0, size.W, size.H), Radii: radii}.Normalized()
	shape := shadowShape(size, radii, s)
	outer, inner := shape, box
	if s.Inset {
		outer, inner = box, shape
	}
	var ring *path
	if !outer.Rect.Empty() {
		a, err1 := r.rrectPath(outer)
		if err1 == nil {
			ring = a
			if !inner.Rect.Empty() {
				if b, err2 := r.rrectPath(inner); err2 == nil {
					ring = r.combine(a, b, d2d.COMBINE_MODE_EXCLUDE)
					a.Release()
					b.Release()
				}
			}
		}
	}
	if len(r.rings) >= 128 {
		for kk, g := range r.rings {
			g.Release()
			delete(r.rings, kk)
		}
	}
	r.rings[k] = ring
	return ring.geometry()
}

type ringKey struct {
	size   kit.Size
	radii  kit.Radii
	offset kit.Point
	spread float32
	inset  bool
}

func (p *path) geometry() *d2d.Geometry {
	if p == nil || p.geo == nil {
		return nil
	}
	return &p.geo.Geometry
}

// combine is a mode b, as a new path (nil when Direct2D fails).
func (r *Resources) combine(a, b *path, mode uint32) *path {
	geo, hr := r.d2d.CreatePathGeometry()
	if hr.Failed() {
		return nil
	}
	sink, hr := geo.Open()
	if hr.Failed() {
		geo.Release()
		return nil
	}
	hr = a.geo.CombineWithGeometry(&b.geo.Geometry, mode, nil, 0.01, &sink.SimplifiedGeometrySink)
	if hr2 := sink.Close(); !hr.Failed() {
		hr = hr2
	}
	sink.Release()
	if hr.Failed() {
		geo.Release()
		return nil
	}
	return &path{res: r, geo: geo}
}

// rrectPath is the path of r: straight sides joined by quarter circles.
func (r *Resources) rrectPath(rr kit.RRect) (*path, error) {
	x0, y0, x1, y1 := rr.Rect.Min.X, rr.Rect.Min.Y, rr.Rect.Max.X, rr.Rect.Max.Y
	q := rr.Radii
	var d kit.PathData
	d.MoveTo(kit.Pt(x0+q.TopLeft, y0))
	d.LineTo(kit.Pt(x1-q.TopRight, y0))
	if q.TopRight > 0 {
		d.ArcTo(kit.Size{W: q.TopRight, H: q.TopRight}, 0, false, true, kit.Pt(x1, y0+q.TopRight))
	}
	d.LineTo(kit.Pt(x1, y1-q.BottomRight))
	if q.BottomRight > 0 {
		d.ArcTo(kit.Size{W: q.BottomRight, H: q.BottomRight}, 0, false, true, kit.Pt(x1-q.BottomRight, y1))
	}
	d.LineTo(kit.Pt(x0+q.BottomLeft, y1))
	if q.BottomLeft > 0 {
		d.ArcTo(kit.Size{W: q.BottomLeft, H: q.BottomLeft}, 0, false, true, kit.Pt(x0, y1-q.BottomLeft))
	}
	d.LineTo(kit.Pt(x0, y0+q.TopLeft))
	if q.TopLeft > 0 {
		d.ArcTo(kit.Size{W: q.TopLeft, H: q.TopLeft}, 0, false, true, kit.Pt(x0+q.TopLeft, y0))
	}
	d.Close()
	return r.buildPath(d.Ops, kit.FillNonZero)
}

// rrectGeometry is the cached geometry of a rounded rectangle of size and radii (already
// normalised) at (0, 0), for corners that differ.
func (r *Resources) rrectGeometry(size kit.Size, radii kit.Radii) *d2d.Geometry {
	k := rrectKey{size, radii}
	if p, ok := r.rrects[k]; ok {
		return p.geometry()
	}
	p, err := r.rrectPath(kit.RRect{Rect: kit.R(0, 0, size.W, size.H), Radii: radii})
	if err != nil {
		p = nil
	}
	if len(r.rrects) >= 128 {
		for kk, old := range r.rrects {
			old.Release()
			delete(r.rrects, kk)
		}
	}
	r.rrects[k] = p
	return p.geometry()
}

type rrectKey struct {
	size  kit.Size
	radii kit.Radii
}
