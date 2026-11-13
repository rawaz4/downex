//go:build windows

package win

import (
	"downex/ui/kit"
	"downex/ui/win/internal/d2d"
)

// canvas is the kit.Canvas of one device's frame. It is one value per device, reused by
// every frame, so drawing allocates nothing once its stack has grown.
type canvas struct {
	dev        *device
	stack      []push
	xf         kit.Affine // the current transform
	unbalanced int        // Pops without a Push in this frame
}

type pushKind uint8

const (
	pushNothing   pushKind = iota // a push with no effect (opacity 1)
	pushClip                      // an axis-aligned clip
	pushLayer                     // a layer: a rounded clip or an opacity
	pushTransform                 // a transform; prev is the one before it
)

type push struct {
	kind pushKind
	prev kit.Affine // a transform's previous one; a clip's transform when pushed
	clip kit.Rect   // a clip's rectangle, which BlurBackdrop pushes again
	aa   uint32     // a clip's antialias mode
}

func (c *canvas) reset() {
	c.stack = c.stack[:0]
	c.xf = kit.Affine{}
	c.unbalanced = 0
}

func (c *canvas) rt() *d2d.RenderTarget {
	if !c.dev.inFrame {
		return nil
	}
	return c.dev.rt
}

func (c *canvas) brush(col kit.Color) *d2d.Brush {
	c.dev.setColor(col)
	return &c.dev.brush.Brush
}

// FillRect implements kit.Canvas.
func (c *canvas) FillRect(r kit.Rect, col kit.Color) {
	rt := c.rt()
	if rt == nil || !finiteRect(r) || r.Empty() {
		return
	}
	rc := rectF(r)
	rt.FillRectangle(&rc, c.brush(col))
}

// StrokeRect implements kit.Canvas.
func (c *canvas) StrokeRect(r kit.Rect, s kit.Stroke, col kit.Color) {
	rt := c.rt()
	if rt == nil || !finiteRect(r) || !(s.Width > 0) || !finite(s.Width) {
		return
	}
	rc := rectF(r)
	rt.DrawRectangle(&rc, c.brush(col), s.Width, c.dev.res.strokeStyle(s, s.Width))
}

// FillRoundedRect implements kit.Canvas.
func (c *canvas) FillRoundedRect(r kit.RRect, col kit.Color) {
	rt := c.rt()
	if rt == nil || !finiteRRect(r) || r.Rect.Empty() {
		return
	}
	r = r.Normalized()
	if rad, ok := r.Uniform(); ok {
		if rad == 0 {
			rc := rectF(r.Rect)
			rt.FillRectangle(&rc, c.brush(col))
			return
		}
		rr := d2d.RoundedRect{Rect: rectF(r.Rect), RadiusX: rad, RadiusY: rad}
		rt.FillRoundedRectangle(&rr, c.brush(col))
		return
	}
	if g := c.dev.res.rrectGeometry(r.Rect.Size(), r.Radii); g != nil {
		c.moveTo(r.Rect.Min)
		rt.FillGeometry(g, c.brush(col))
		c.moveBack()
	}
}

// StrokeRoundedRect implements kit.Canvas.
func (c *canvas) StrokeRoundedRect(r kit.RRect, s kit.Stroke, col kit.Color) {
	rt := c.rt()
	if rt == nil || !finiteRRect(r) || !(s.Width > 0) || !finite(s.Width) {
		return
	}
	r = r.Normalized()
	st := c.dev.res.strokeStyle(s, s.Width)
	if rad, ok := r.Uniform(); ok {
		if rad == 0 {
			rc := rectF(r.Rect)
			rt.DrawRectangle(&rc, c.brush(col), s.Width, st)
			return
		}
		rr := d2d.RoundedRect{Rect: rectF(r.Rect), RadiusX: rad, RadiusY: rad}
		rt.DrawRoundedRectangle(&rr, c.brush(col), s.Width, st)
		return
	}
	if g := c.dev.res.rrectGeometry(r.Rect.Size(), r.Radii); g != nil {
		c.moveTo(r.Rect.Min)
		rt.DrawGeometry(g, c.brush(col), s.Width, st)
		c.moveBack()
	}
}

// moveTo moves the target's transform to origin for a geometry made at (0, 0); moveBack
// restores the canvas's own.
func (c *canvas) moveTo(origin kit.Point) {
	m := matrix(kit.Translation(origin.X, origin.Y).Then(c.xf))
	c.dev.rt.SetTransform(&m)
}

func (c *canvas) moveBack() {
	m := matrix(c.xf)
	c.dev.rt.SetTransform(&m)
}

func ellipse(r kit.Rect) d2d.Ellipse {
	return d2d.Ellipse{Point: d2d.PointF{X: (r.Min.X + r.Max.X) / 2, Y: (r.Min.Y + r.Max.Y) / 2},
		RadiusX: r.Width() / 2, RadiusY: r.Height() / 2}
}

// FillEllipse implements kit.Canvas.
func (c *canvas) FillEllipse(r kit.Rect, col kit.Color) {
	rt := c.rt()
	if rt == nil || !finiteRect(r) || r.Empty() {
		return
	}
	e := ellipse(r)
	rt.FillEllipse(&e, c.brush(col))
}

// StrokeEllipse implements kit.Canvas.
func (c *canvas) StrokeEllipse(r kit.Rect, s kit.Stroke, col kit.Color) {
	rt := c.rt()
	if rt == nil || !finiteRect(r) || !(s.Width > 0) || !finite(s.Width) {
		return
	}
	e := ellipse(r)
	rt.DrawEllipse(&e, c.brush(col), s.Width, c.dev.res.strokeStyle(s, s.Width))
}

// DrawLine implements kit.Canvas.
func (c *canvas) DrawLine(a, b kit.Point, s kit.Stroke, col kit.Color) {
	rt := c.rt()
	if rt == nil || !finitePt(a) || !finitePt(b) || !(s.Width > 0) || !finite(s.Width) {
		return
	}
	rt.DrawLine(a.X, a.Y, b.X, b.Y, c.brush(col), s.Width, c.dev.res.strokeStyle(s, s.Width))
}

func (c *canvas) path(p kit.Path) *d2d.Geometry {
	pp, ok := p.(*path)
	if !ok || pp == nil || pp.geo == nil || pp.res != c.dev.res {
		return nil
	}
	return &pp.geo.Geometry
}

// FillPath implements kit.Canvas.
func (c *canvas) FillPath(p kit.Path, col kit.Color) {
	rt := c.rt()
	g := c.path(p)
	if rt == nil || g == nil {
		return
	}
	rt.FillGeometry(g, c.brush(col))
}

// StrokePath implements kit.Canvas.
func (c *canvas) StrokePath(p kit.Path, s kit.Stroke, col kit.Color) {
	rt := c.rt()
	g := c.path(p)
	if rt == nil || g == nil || !(s.Width > 0) || !finite(s.Width) {
		return
	}
	rt.DrawGeometry(g, c.brush(col), s.Width, c.dev.res.strokeStyle(s, s.Width))
}

// FillGradient implements kit.Canvas.
func (c *canvas) FillGradient(r kit.Rect, g kit.LinearGradient) {
	rt := c.rt()
	if rt == nil || !finiteRect(r) || r.Empty() || len(g.Stops) == 0 || !finitePt(g.Start) || !finitePt(g.End) {
		return
	}
	for _, s := range g.Stops {
		if !finite(s.Offset) {
			return
		}
	}
	rc := rectF(r)
	if len(g.Stops) == 1 || g.Start == g.End {
		// One colour, or no gradient line to spread the colours along: CSS paints the last.
		rt.FillRectangle(&rc, c.brush(g.Stops[len(g.Stops)-1].Color))
		return
	}
	b := c.dev.gradientBrush(g)
	if b == nil {
		return
	}
	rt.FillRectangle(&rc, &b.Brush)
}

// DrawShadow implements kit.Canvas.
func (c *canvas) DrawShadow(r kit.RRect, s kit.Shadow) {
	rt := c.rt()
	if rt == nil || !finiteRRect(r) || r.Rect.Empty() || s.Color.A <= 0 ||
		!finitePt(s.Offset) || !finite(s.Blur) || !finite(s.Spread) || s.Blur < 0 {
		return
	}
	c.dev.drawShadow(r.Normalized(), s)
}

// DrawText implements kit.Canvas.
func (c *canvas) DrawText(l kit.TextLayout, origin kit.Point, col kit.Color) {
	rt := c.rt()
	tl, ok := l.(*textLayout)
	if rt == nil || !ok || tl == nil || tl.layout == nil || tl.res != c.dev.res || !finitePt(origin) {
		return
	}
	mode := uint32(d2d.TEXT_ANTIALIAS_MODE_DEFAULT)
	if tl.aa == kit.AntialiasGrayscale {
		mode = d2d.TEXT_ANTIALIAS_MODE_GRAYSCALE
	}
	if mode != c.dev.textAA {
		rt.SetTextAntialiasMode(mode)
		c.dev.textAA = mode
	}
	rt.DrawTextLayout(origin.X, origin.Y, &tl.layout.Object, c.brush(col), d2d.DRAW_TEXT_OPTIONS_ENABLE_COLOR_FONT)
}

// DrawImage implements kit.Canvas.
func (c *canvas) DrawImage(img kit.Image, src, dst kit.Rect) {
	rt := c.rt()
	im, ok := img.(*image)
	if rt == nil || !ok || im == nil || im.res != c.dev.res || !finiteRect(src) || !finiteRect(dst) || dst.Empty() {
		return
	}
	b := im.bitmap(c.dev)
	if b == nil {
		return
	}
	if src.Empty() {
		src = kit.R(0, 0, float32(im.w), float32(im.h))
	}
	s, d := rectF(src), rectF(dst)
	rt.DrawBitmap(b, &d, 1, d2d.BITMAP_INTERPOLATION_MODE_LINEAR, &s)
}

// PushClip implements kit.Canvas.
func (c *canvas) PushClip(r kit.Rect) {
	rt := c.rt()
	if rt == nil {
		return
	}
	if !finiteRect(r) {
		r = kit.Rect{}
	}
	rc := rectF(r)
	rt.PushAxisAlignedClip(&rc, d2d.ANTIALIAS_MODE_PER_PRIMITIVE)
	c.stack = append(c.stack, push{kind: pushClip, prev: c.xf, clip: r, aa: d2d.ANTIALIAS_MODE_PER_PRIMITIVE})
}

// PushRoundedClip implements kit.Canvas.
func (c *canvas) PushRoundedClip(r kit.RRect) {
	rt := c.rt()
	if rt == nil {
		return
	}
	if !finiteRRect(r) || r.Rect.Empty() {
		rc := d2d.RectF{}
		rt.PushAxisAlignedClip(&rc, d2d.ANTIALIAS_MODE_ALIASED)
		c.stack = append(c.stack, push{kind: pushClip, prev: c.xf, aa: d2d.ANTIALIAS_MODE_ALIASED})
		return
	}
	r = r.Normalized()
	if rad, ok := r.Uniform(); ok && rad == 0 {
		rc := rectF(r.Rect)
		rt.PushAxisAlignedClip(&rc, d2d.ANTIALIAS_MODE_PER_PRIMITIVE)
		c.stack = append(c.stack, push{kind: pushClip, prev: c.xf, clip: r.Rect, aa: d2d.ANTIALIAS_MODE_PER_PRIMITIVE})
		return
	}
	g := c.dev.res.rrectGeometry(r.Rect.Size(), r.Radii)
	if g == nil {
		c.stack = append(c.stack, push{kind: pushNothing})
		return
	}
	p := d2d.LayerParameters{
		ContentBounds:     rectF(r.Rect),
		GeometricMask:     g,
		MaskAntialiasMode: d2d.ANTIALIAS_MODE_PER_PRIMITIVE,
		MaskTransform:     d2d.Matrix3x2F{M11: 1, M22: 1, M31: r.Rect.Min.X, M32: r.Rect.Min.Y},
		Opacity:           1,
	}
	if c.dev.dc != nil && c.dev.clearTypeLayers {
		// A layer has an alpha channel of its own, and Direct2D draws text on one in
		// grayscale, even with D2D1_LAYER_OPTIONS_INITIALIZE_FOR_CLEARTYPE (measured: the
		// list card's rows, headers and sizes were grayscale while the title bar had
		// ClearType). On a window's target, which has no alpha, a layer that starts from the
		// pixels under it and ignores alpha keeps ClearType, as a page's rounded overflow
		// clip does (TestClearTypeInsideRoundedClip). Offscreen targets keep the plain layer.
		p.LayerOptions = d2d.LAYER_OPTIONS1_INITIALIZE_FROM_BACKGROUND | d2d.LAYER_OPTIONS1_IGNORE_ALPHA
		c.dev.dc.PushLayer(&p, nil)
	} else {
		rt.PushLayer(&p, nil)
	}
	c.stack = append(c.stack, push{kind: pushLayer})
}

// PushOpacity implements kit.Canvas.
func (c *canvas) PushOpacity(alpha float32) {
	rt := c.rt()
	if rt == nil {
		return
	}
	if alpha >= 1 {
		c.stack = append(c.stack, push{kind: pushNothing})
		return
	}
	if !(alpha > 0) {
		alpha = 0
	}
	p := d2d.LayerParameters{ContentBounds: d2d.InfiniteRect, MaskTransform: d2d.Identity, Opacity: alpha}
	rt.PushLayer(&p, nil)
	c.stack = append(c.stack, push{kind: pushLayer})
}

// PushTransform implements kit.Canvas.
func (c *canvas) PushTransform(m kit.Affine) {
	rt := c.rt()
	if rt == nil {
		return
	}
	c.stack = append(c.stack, push{kind: pushTransform, prev: c.xf})
	a, b, cc, d, e, f := m.Elems()
	if !finite(a) || !finite(b) || !finite(cc) || !finite(d) || !finite(e) || !finite(f) {
		m = kit.Scaling(0, 0) // draws nothing until popped
	}
	c.xf = m.Then(c.xf)
	mm := matrix(c.xf)
	rt.SetTransform(&mm)
}

// Pop implements kit.Canvas.
func (c *canvas) Pop() {
	rt := c.rt()
	if rt == nil {
		return
	}
	n := len(c.stack)
	if n == 0 {
		c.unbalanced++
		return
	}
	p := c.stack[n-1]
	c.stack = c.stack[:n-1]
	switch p.kind {
	case pushClip:
		rt.PopAxisAlignedClip()
	case pushLayer:
		rt.PopLayer()
	case pushTransform:
		c.xf = p.prev
		mm := matrix(c.xf)
		rt.SetTransform(&mm)
	}
}

func finiteRRect(r kit.RRect) bool {
	q := r.Radii
	return finiteRect(r.Rect) && finite(q.TopLeft) && finite(q.TopRight) && finite(q.BottomRight) && finite(q.BottomLeft)
}

var _ kit.Canvas = (*canvas)(nil)
