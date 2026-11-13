//go:build darwin && cgo

package mac

/*
#include "draw.h"
*/
import "C"

import (
	"downex/ui/internal/gfx"
	"downex/ui/kit"
)

// canvas is the kit.Canvas of one target's frame, over its CGContext in window DIPs (y
// down; the frame's base transform maps them to device pixels). It is one value per target,
// reused by every frame, so drawing allocates nothing once its stack has grown. Every push
// but a no-op saves the graphics state, and Pop restores it, so clips, layers and
// transforms close in order.
type canvas struct {
	t          *bitmapTarget
	stack      []push
	xf         kit.Affine // the current transform
	hidden     int        // pushed transforms that draw nothing (not finite, or flat)
	unbalanced int        // Pops without a Push in this frame
}

type pushKind uint8

const (
	pushNothing   pushKind = iota // a push with no effect (opacity 1)
	pushClip                      // a rectangle clip
	pushRClip                     // a rounded clip (a layer, for BlurBackdrop)
	pushLayer                     // an opacity layer
	pushTransform                 // a transform; prev is the one before it
)

type push struct {
	kind pushKind
	prev kit.Affine // a transform's previous one; a clip's transform when pushed
	clip kit.Rect   // a clip's rectangle, which BlurBackdrop intersects
	hide bool       // a transform that draws nothing
}

func (c *canvas) reset() {
	c.stack = c.stack[:0]
	c.xf = kit.Affine{}
	c.hidden = 0
	c.unbalanced = 0
}

// ctx is the context to draw on, 0 outside a frame or under a transform that shows nothing.
func (c *canvas) ctx() C.CGContextRef {
	if !c.t.inFrame || c.hidden > 0 {
		return 0
	}
	return c.t.ctx
}

func paint(col kit.Color) C.dx_paint {
	return C.dx_paint{r: cg(col.R), g: cg(col.G), b: cg(col.B), a: cg(col.A)}
}

// strokePaint is col with s's width, caps, join and dash (a dash only when Off > 0 and
// On >= 0, as SVG draws a dasharray).
func strokePaint(col kit.Color, s kit.Stroke) C.dx_paint {
	p := paint(col)
	p.width = cg(s.Width)
	p.cap = C.int(s.Cap)
	p.join = C.int(s.Join)
	if s.Dash.Off > 0 && s.Dash.On >= 0 && finite(s.Dash.On) && finite(s.Dash.Off) {
		p.on, p.off = cg(s.Dash.On), cg(s.Dash.Off)
	}
	if s.Cap > kit.CapSquare {
		p.cap = 0
	}
	if s.Join > kit.JoinBevel {
		p.join = 0
	}
	return p
}

func strokeOK(s kit.Stroke) bool { return s.Width > 0 && finite(s.Width) }

// FillRect implements kit.Canvas.
func (c *canvas) FillRect(r kit.Rect, col kit.Color) {
	ctx := c.ctx()
	if ctx == 0 || !finiteRect(r) || r.Empty() || !finiteColor(col) {
		return
	}
	C.dx_fill_rect(ctx, crect(r), paint(col))
}

// StrokeRect implements kit.Canvas.
func (c *canvas) StrokeRect(r kit.Rect, s kit.Stroke, col kit.Color) {
	ctx := c.ctx()
	if ctx == 0 || !finiteRect(r) || !strokeOK(s) || !finiteColor(col) {
		return
	}
	C.dx_stroke_rect(ctx, crectAny(r), strokePaint(col, s))
}

// crectAny is r for an outline: a flat rectangle still strokes as a line.
func crectAny(r kit.Rect) C.dx_rect {
	return C.dx_rect{x0: cg(min(r.Min.X, r.Max.X)), y0: cg(min(r.Min.Y, r.Max.Y)),
		x1: cg(max(r.Min.X, r.Max.X)), y1: cg(max(r.Min.Y, r.Max.Y))}
}

// FillRoundedRect implements kit.Canvas.
func (c *canvas) FillRoundedRect(r kit.RRect, col kit.Color) {
	ctx := c.ctx()
	if ctx == 0 || !finiteRRect(r) || r.Rect.Empty() || !finiteColor(col) {
		return
	}
	r = r.Normalized()
	if rad, ok := r.Uniform(); ok && rad == 0 {
		C.dx_fill_rect(ctx, crect(r.Rect), paint(col))
		return
	}
	C.dx_fill_rrect(ctx, crect(r.Rect), cradii(r.Radii), paint(col))
}

// StrokeRoundedRect implements kit.Canvas.
func (c *canvas) StrokeRoundedRect(r kit.RRect, s kit.Stroke, col kit.Color) {
	ctx := c.ctx()
	if ctx == 0 || !finiteRRect(r) || !strokeOK(s) || !finiteColor(col) {
		return
	}
	r = r.Normalized()
	if rad, ok := r.Uniform(); ok && rad == 0 {
		C.dx_stroke_rect(ctx, crectAny(r.Rect), strokePaint(col, s))
		return
	}
	C.dx_stroke_rrect(ctx, crect(r.Rect), cradii(r.Radii), strokePaint(col, s))
}

// FillEllipse implements kit.Canvas.
func (c *canvas) FillEllipse(r kit.Rect, col kit.Color) {
	ctx := c.ctx()
	if ctx == 0 || !finiteRect(r) || r.Empty() || !finiteColor(col) {
		return
	}
	C.dx_fill_ellipse(ctx, crect(r), paint(col))
}

// StrokeEllipse implements kit.Canvas.
func (c *canvas) StrokeEllipse(r kit.Rect, s kit.Stroke, col kit.Color) {
	ctx := c.ctx()
	if ctx == 0 || !finiteRect(r) || !strokeOK(s) || !finiteColor(col) {
		return
	}
	C.dx_stroke_ellipse(ctx, crectAny(r), strokePaint(col, s))
}

// DrawLine implements kit.Canvas.
func (c *canvas) DrawLine(a, b kit.Point, s kit.Stroke, col kit.Color) {
	ctx := c.ctx()
	if ctx == 0 || !finitePt(a) || !finitePt(b) || !strokeOK(s) || !finiteColor(col) {
		return
	}
	C.dx_line(ctx, cg(a.X), cg(a.Y), cg(b.X), cg(b.Y), strokePaint(col, s))
}

func (c *canvas) path(p kit.Path) *path {
	pp, ok := p.(*path)
	if !ok || pp == nil || pp.p == 0 || pp.res != c.t.res {
		return nil
	}
	return pp
}

// FillPath implements kit.Canvas.
func (c *canvas) FillPath(p kit.Path, col kit.Color) {
	ctx := c.ctx()
	pp := c.path(p)
	if ctx == 0 || pp == nil || !finiteColor(col) {
		return
	}
	C.dx_fill_path(ctx, pp.p, cbool(pp.rule == kit.FillEvenOdd), paint(col))
}

// StrokePath implements kit.Canvas.
func (c *canvas) StrokePath(p kit.Path, s kit.Stroke, col kit.Color) {
	ctx := c.ctx()
	pp := c.path(p)
	if ctx == 0 || pp == nil || !strokeOK(s) || !finiteColor(col) {
		return
	}
	C.dx_stroke_path(ctx, pp.p, strokePaint(col, s))
}

// FillGradient implements kit.Canvas.
func (c *canvas) FillGradient(r kit.Rect, g kit.LinearGradient) {
	ctx := c.ctx()
	if ctx == 0 || !finiteRect(r) || r.Empty() || len(g.Stops) == 0 || !finitePt(g.Start) || !finitePt(g.End) {
		return
	}
	for _, s := range g.Stops {
		if !finite(s.Offset) || !finiteColor(s.Color) {
			return
		}
	}
	if len(g.Stops) == 1 || g.Start == g.End {
		// One colour, or no gradient line to spread the colours along: CSS paints the last.
		C.dx_fill_rect(ctx, crect(r), paint(g.Stops[len(g.Stops)-1].Color))
		return
	}
	c.t.drawGradient(r, g)
}

// DrawShadow implements kit.Canvas.
func (c *canvas) DrawShadow(r kit.RRect, s kit.Shadow) {
	ctx := c.ctx()
	if ctx == 0 || !finiteRRect(r) || r.Rect.Empty() || !(s.Color.A > 0) || !finiteColor(s.Color) ||
		!finitePt(s.Offset) || !finite(s.Blur) || !finite(s.Spread) || s.Blur < 0 {
		return
	}
	c.t.drawShadow(r.Normalized(), s)
}

// DrawText implements kit.Canvas. Text is antialiased in grayscale without font smoothing
// whatever the layout asks (app.css: -webkit-font-smoothing: antialiased; macOS has no
// subpixel antialiasing).
func (c *canvas) DrawText(l kit.TextLayout, origin kit.Point, col kit.Color) {
	ctx := c.ctx()
	tl, ok := l.(*textLayout)
	if ctx == 0 || !ok || tl == nil || !finitePt(origin) || !finiteColor(col) {
		return
	}
	tl.draw(ctx, origin, col)
}

// DrawImage implements kit.Canvas.
func (c *canvas) DrawImage(img kit.Image, src, dst kit.Rect) {
	ctx := c.ctx()
	im, ok := img.(*image)
	if ctx == 0 || !ok || im == nil || im.img == 0 || im.res != c.t.res || !finiteRect(src) || !finiteRect(dst) || dst.Empty() {
		return
	}
	if src.Empty() {
		src = kit.R(0, 0, float32(im.w), float32(im.h))
	}
	// The whole image placed so that src lands on dst, drawn through a clip to dst.
	sx, sy := dst.Width()/src.Width(), dst.Height()/src.Height()
	x0, y0 := dst.Min.X-src.Min.X*sx, dst.Min.Y-src.Min.Y*sy
	full := kit.R(x0, y0, x0+float32(im.w)*sx, y0+float32(im.h)*sy)
	if !finiteRect(full) || full.Empty() {
		return
	}
	C.dx_draw_image(ctx, im.img, crect(full), crect(dst))
}

// PushClip implements kit.Canvas.
func (c *canvas) PushClip(r kit.Rect) {
	if !c.t.inFrame {
		return
	}
	if !finiteRect(r) {
		r = kit.Rect{}
	}
	c.pushClipRect(r)
	c.stack = append(c.stack, push{kind: pushClip, prev: c.xf, clip: r})
}

// pushClipRect clips to r, or to its bounds on the screen when the transform turns it.
func (c *canvas) pushClipRect(r kit.Rect) {
	a, b, cc, d, _, _ := c.xf.Elems()
	if (b == 0 && cc == 0) || (a == 0 && d == 0) || c.hidden > 0 {
		C.dx_push_clip(c.t.ctx, crect(r))
		return
	}
	C.dx_push_clip_screen(c.t.ctx, crect(gfx.XFBounds(c.xf, r)), C.int(c.t.h), C.CGFloat(c.t.ppd))
}

// PushRoundedClip implements kit.Canvas.
func (c *canvas) PushRoundedClip(r kit.RRect) {
	if !c.t.inFrame {
		return
	}
	if !finiteRRect(r) || r.Rect.Empty() {
		C.dx_push_clip(c.t.ctx, crect(kit.Rect{}))
		c.stack = append(c.stack, push{kind: pushClip, prev: c.xf})
		return
	}
	r = r.Normalized()
	if rad, ok := r.Uniform(); ok && rad == 0 {
		c.pushClipRect(r.Rect)
		c.stack = append(c.stack, push{kind: pushClip, prev: c.xf, clip: r.Rect})
		return
	}
	C.dx_push_rclip(c.t.ctx, crect(r.Rect), cradii(r.Radii))
	c.stack = append(c.stack, push{kind: pushRClip, prev: c.xf, clip: r.Rect})
}

// PushOpacity implements kit.Canvas: a transparency layer composited once at alpha.
func (c *canvas) PushOpacity(alpha float32) {
	if !c.t.inFrame {
		return
	}
	if alpha >= 1 {
		c.stack = append(c.stack, push{kind: pushNothing})
		return
	}
	if !(alpha > 0) {
		alpha = 0
	}
	C.dx_push_layer(c.t.ctx, C.CGFloat(alpha))
	c.stack = append(c.stack, push{kind: pushLayer})
}

// PushTransform implements kit.Canvas.
func (c *canvas) PushTransform(m kit.Affine) {
	if !c.t.inFrame {
		return
	}
	p := push{kind: pushTransform, prev: c.xf}
	a, b, cc, d, e, f := m.Elems()
	if !finite(a) || !finite(b) || !finite(cc) || !finite(d) || !finite(e) || !finite(f) {
		m = kit.Scaling(0, 0) // draws nothing until popped
	}
	c.xf = m.Then(c.xf)
	if xa, xb, xc, xd, _, _ := c.xf.Elems(); xa*xd-xb*xc == 0 {
		// A flat transform shows nothing; CoreGraphics is not given it.
		p.hide = true
		c.hidden++
		C.dx_push_transform(c.t.ctx, caffine(kit.Affine{}))
	} else {
		C.dx_push_transform(c.t.ctx, caffine(m))
	}
	c.stack = append(c.stack, p)
}

// Pop implements kit.Canvas.
func (c *canvas) Pop() {
	if !c.t.inFrame {
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
	case pushNothing:
		return
	case pushLayer:
		C.dx_pop(c.t.ctx, 1)
	case pushTransform:
		c.xf = p.prev
		if p.hide {
			c.hidden--
		}
		C.dx_pop(c.t.ctx, 0)
	default:
		C.dx_pop(c.t.ctx, 0)
	}
}

var (
	_ kit.Canvas         = (*canvas)(nil)
	_ kit.BackdropFilter = (*canvas)(nil)
)
