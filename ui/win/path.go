//go:build windows

package win

import (
	"fmt"

	"downex/ui/internal/gfx"
	"downex/ui/kit"
	"downex/ui/win/internal/com"
	"downex/ui/win/internal/d2d"
)

// path is a kit.Path: a Direct2D path geometry, which is device-independent. The backend
// keeps paths of its own (rounded rectangles, shadow rings), which are not public.
type path struct {
	res    *Resources
	geo    *d2d.PathGeometry
	public bool // made by NewPath or NewSVGPath, counted in res.live
}

// Release implements kit.Path.
func (p *path) Release() {
	if p == nil || p.geo == nil {
		return
	}
	com.Release(&p.geo)
	if p.public {
		p.res.live.paths--
	}
}

// Bounds is the path's bounding box (tests and hit areas); a line's has no height or width.
func (p *path) Bounds() kit.Rect {
	if p.geo == nil {
		return kit.Rect{}
	}
	b, hr := p.geo.GetBounds(nil)
	if hr.Failed() || b.Left > b.Right {
		return kit.Rect{}
	}
	return kit.R(b.Left, b.Top, b.Right, b.Bottom)
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
	return r.publicPath(r.buildPath(d.Ops, d.Fill))
}

func (r *Resources) publicPath(p *path, err error) (kit.Path, error) {
	if err != nil {
		return nil, err
	}
	p.public = true
	r.live.paths++
	return p, nil
}

// NewSVGPath implements kit.Resources.
func (r *Resources) NewSVGPath(d string, rule kit.FillRule) (kit.Path, error) {
	if err := r.usable(); err != nil {
		return nil, err
	}
	ops, err := parseSVGPath(d, nil)
	if err != nil {
		return nil, err
	}
	return r.publicPath(r.buildPath(ops, rule))
}

// buildPath writes ops into a new path geometry. Every figure is fillable (fills close
// them); strokes draw open figures open.
func (r *Resources) buildPath(ops []kit.PathOp, rule kit.FillRule) (*path, error) {
	geo, hr := r.d2d.CreatePathGeometry()
	if hr.Failed() {
		return nil, fmt.Errorf("win: %w", com.Check("ID2D1Factory::CreatePathGeometry", hr))
	}
	sink, hr := geo.Open()
	if hr.Failed() {
		geo.Release()
		return nil, fmt.Errorf("win: %w", com.Check("ID2D1PathGeometry::Open", hr))
	}
	mode := uint32(d2d.FILL_MODE_WINDING)
	if rule == kit.FillEvenOdd {
		mode = d2d.FILL_MODE_ALTERNATE
	}
	sink.SetFillMode(mode)
	var cur, start kit.Point
	open := false
	begin := func(p kit.Point) {
		if open {
			sink.EndFigure(d2d.FIGURE_END_OPEN)
		}
		sink.BeginFigure(p.X, p.Y, d2d.FIGURE_BEGIN_FILLED)
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
			sink.AddLine(op.P[0].X, op.P[0].Y)
			cur = op.P[0]
		case kit.PathQuad:
			ensure()
			q := d2d.QuadraticBezierSegment{P1: pf(op.P[0]), P2: pf(op.P[1])}
			sink.AddQuadraticBezier(&q)
			cur = op.P[1]
		case kit.PathCubic:
			ensure()
			b := d2d.BezierSegment{P1: pf(op.P[0]), P2: pf(op.P[1]), P3: pf(op.P[2])}
			sink.AddBezier(&b)
			cur = op.P[2]
		case kit.PathArc:
			ensure()
			addArc(sink, cur, op)
			cur = op.P[0]
		case kit.PathClose:
			if open {
				sink.EndFigure(d2d.FIGURE_END_CLOSED)
				open = false
			}
			cur = start
		}
	}
	if open {
		sink.EndFigure(d2d.FIGURE_END_OPEN)
	}
	hr = sink.Close()
	sink.Release()
	if hr.Failed() {
		geo.Release()
		return nil, fmt.Errorf("win: path: %w", com.Check("ID2D1GeometrySink::Close", hr))
	}
	return &path{res: r, geo: geo}, nil
}

func pf(p kit.Point) d2d.PointF { return d2d.PointF{X: p.X, Y: p.Y} }

// addArc adds an SVG arc from cur, handling the out-of-range parameters as SVG's
// implementation notes (F.6.2, F.6.6) say: equal end points draw nothing, a zero radius
// draws a straight line, and radii too small to reach the end point are scaled up.
func addArc(sink *d2d.GeometrySink, cur kit.Point, op kit.PathOp) {
	end := op.P[0]
	kind, rx, ry := gfx.ArcRadii(cur, op)
	switch kind {
	case gfx.ArcNone:
		return
	case gfx.ArcLine:
		sink.AddLine(end.X, end.Y)
		return
	}
	a := d2d.ArcSegment{Point: pf(end), RotationAngle: op.Rotation}
	a.Size.W, a.Size.H = float32(rx), float32(ry)
	if op.Sweep {
		a.SweepDirection = d2d.SWEEP_DIRECTION_CLOCKWISE
	}
	if op.Large {
		a.ArcSize = d2d.ARC_SIZE_LARGE
	}
	sink.AddArc(&a)
}

var _ kit.Path = (*path)(nil)
