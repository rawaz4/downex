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

// path is a kit.Path: an immutable CoreGraphics path, which is device-independent, and its
// fill rule.
type path struct {
	res  *Resources
	p    C.CGPathRef
	rule kit.FillRule
}

// Release implements kit.Path.
func (p *path) Release() {
	if p == nil || p.p == 0 {
		return
	}
	C.CGPathRelease(p.p)
	p.p = 0
	p.res.live.paths--
}

// Bounds is the path's bounding box, without control points (tests and hit areas).
func (p *path) Bounds() kit.Rect {
	if p == nil || p.p == 0 {
		return kit.Rect{}
	}
	b := C.CGPathGetPathBoundingBox(p.p)
	if C.CGRectIsNull(b) {
		return kit.Rect{}
	}
	x, y := float32(b.origin.x), float32(b.origin.y)
	return kit.R(x, y, x+float32(b.size.width), y+float32(b.size.height))
}

// NewPath implements kit.Resources.
func (r *Resources) NewPath(d kit.PathData) (kit.Path, error) {
	if err := r.usable(); err != nil {
		return nil, err
	}
	for i, op := range d.Ops {
		n := 0
		switch op.Verb {
		case kit.PathMove, kit.PathLine:
			n = 1
		case kit.PathQuad:
			n = 2
		case kit.PathCubic:
			n = 3
		case kit.PathArc:
			n = 1
			if !finite(op.Radii.W) || !finite(op.Radii.H) || !finite(op.Rotation) {
				return nil, errInvalid("path op %d: arc radii or rotation not finite", i)
			}
		case kit.PathClose:
		default:
			return nil, errInvalid("path op %d: unknown verb %d", i, op.Verb)
		}
		for _, q := range op.P[:n] {
			if !finitePt(q) {
				return nil, errInvalid("path op %d: point %v is not finite", i, q)
			}
		}
	}
	return r.buildPath(d.Ops, d.Fill)
}

// NewSVGPath implements kit.Resources.
func (r *Resources) NewSVGPath(d string, rule kit.FillRule) (kit.Path, error) {
	if err := r.usable(); err != nil {
		return nil, err
	}
	ops, err := gfx.ParseSVGPath(d, nil)
	if err != nil {
		return nil, errWrap(err)
	}
	return r.buildPath(ops, rule)
}

func errWrap(err error) error { return &wrapped{err} }

type wrapped struct{ err error }

func (w *wrapped) Error() string { return "mac: " + w.err.Error() }
func (w *wrapped) Unwrap() error { return w.err }

func cg(v float32) C.CGFloat { return C.CGFloat(v) }

// buildPath writes ops into a new path with SVG's figure rules: a segment that follows no
// move starts a figure where the previous one ended (or at a closed figure's start). Fills
// close every figure; strokes draw open figures open.
func (r *Resources) buildPath(ops []kit.PathOp, rule kit.FillRule) (*path, error) {
	m := C.CGPathCreateMutable()
	if m == 0 {
		return nil, errInvalid("CGPathCreateMutable failed")
	}
	var cur, start kit.Point
	open := false
	begin := func(p kit.Point) {
		C.CGPathMoveToPoint(m, nil, cg(p.X), cg(p.Y))
		open, start = true, p
	}
	ensure := func() {
		if !open {
			begin(cur)
		}
	}
	for _, op := range ops {
		switch op.Verb {
		case kit.PathMove:
			begin(op.P[0])
			cur = op.P[0]
		case kit.PathLine:
			ensure()
			C.CGPathAddLineToPoint(m, nil, cg(op.P[0].X), cg(op.P[0].Y))
			cur = op.P[0]
		case kit.PathQuad:
			ensure()
			C.CGPathAddQuadCurveToPoint(m, nil, cg(op.P[0].X), cg(op.P[0].Y), cg(op.P[1].X), cg(op.P[1].Y))
			cur = op.P[1]
		case kit.PathCubic:
			ensure()
			C.CGPathAddCurveToPoint(m, nil, cg(op.P[0].X), cg(op.P[0].Y), cg(op.P[1].X), cg(op.P[1].Y),
				cg(op.P[2].X), cg(op.P[2].Y))
			cur = op.P[2]
		case kit.PathArc:
			ensure()
			r.arc = gfx.ArcToCubics(r.arc[:0], cur, op)
			for i := 0; i+2 < len(r.arc); i += 3 {
				a, b, e := r.arc[i], r.arc[i+1], r.arc[i+2]
				C.CGPathAddCurveToPoint(m, nil, cg(a.X), cg(a.Y), cg(b.X), cg(b.Y), cg(e.X), cg(e.Y))
			}
			cur = op.P[0]
		case kit.PathClose:
			if open {
				C.CGPathCloseSubpath(m)
				open = false
			}
			cur = start
		}
	}
	r.live.paths++
	return &path{res: r, p: C.CGPathRef(m), rule: rule}, nil
}

var _ kit.Path = (*path)(nil)
