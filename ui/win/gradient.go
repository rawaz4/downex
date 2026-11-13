//go:build windows

package win

import (
	"slices"

	"downex/ui/kit"
	"downex/ui/win/internal/d2d"
)

// gradient is one LinearGradient's stops made for a device: the stop collection and a brush
// over it, whose end points each draw sets. The stops are kept to find it again.
type gradient struct {
	stops  []kit.GradientStop
	repeat bool
	lo, hi float32 // the stops' offsets, which the brush spreads over 0..1
	coll   *d2d.GradientStopCollection
	brush  *d2d.LinearGradientBrush
	used   uint64
}

func (g *gradient) release() {
	if g.brush != nil {
		g.brush.Release()
		g.brush = nil
	}
	if g.coll != nil {
		g.coll.Release()
		g.coll = nil
	}
}

// gradientBrush is the cached brush of g with its line set; nil when it cannot be made.
// Stops out of order are put in order as CSS does (each at least the one before), and the
// gradient line is stretched to span the stops, so offsets outside 0..1 work.
func (d *device) gradientBrush(g kit.LinearGradient) *d2d.LinearGradientBrush {
	var e *gradient
	for _, c := range d.gradients {
		if c.repeat == g.Repeat && slices.Equal(c.stops, g.Stops) {
			e = c
			break
		}
	}
	if e == nil {
		e = d.makeGradient(g)
		if e == nil {
			return nil
		}
	}
	e.used = d.frame
	dx, dy := g.End.X-g.Start.X, g.End.Y-g.Start.Y
	e.brush.SetStartPoint(g.Start.X+dx*e.lo, g.Start.Y+dy*e.lo)
	e.brush.SetEndPoint(g.Start.X+dx*e.hi, g.Start.Y+dy*e.hi)
	return e.brush
}

func (d *device) makeGradient(g kit.LinearGradient) *gradient {
	e := &gradient{stops: slices.Clone(g.Stops), repeat: g.Repeat}
	stops := make([]d2d.GradientStop, len(g.Stops))
	prev := g.Stops[0].Offset
	for i, s := range g.Stops {
		o := max(s.Offset, prev)
		prev = o
		stops[i] = d2d.GradientStop{Position: o, Color: d2d.ColorF{R: s.Color.R, G: s.Color.G, B: s.Color.B, A: s.Color.A}}
	}
	e.lo, e.hi = stops[0].Position, stops[len(stops)-1].Position
	if e.hi <= e.lo {
		e.hi = e.lo + 1e-6
	}
	for i := range stops {
		stops[i].Position = (stops[i].Position - e.lo) / (e.hi - e.lo)
	}
	extend := uint32(d2d.EXTEND_MODE_CLAMP)
	if g.Repeat {
		extend = d2d.EXTEND_MODE_WRAP
	}
	// CSS interpolates in premultiplied sRGB, which Direct2D 1.1 offers; 1.0 interpolates
	// straight alpha.
	if d.dc != nil {
		e.coll, _ = d.dc.CreateGradientStopCollection(&stops[0], uint32(len(stops)), extend, d2d.COLOR_INTERPOLATION_MODE_PREMULTIPLIED)
	}
	if e.coll == nil {
		e.coll, _ = d.rt.CreateGradientStopCollection(&stops[0], uint32(len(stops)), extend)
	}
	if e.coll == nil {
		return nil
	}
	p := d2d.LinearGradientBrushProperties{}
	b, hr := d.rt.CreateLinearGradientBrush(&p, e.coll)
	if hr.Failed() {
		e.release()
		return nil
	}
	e.brush = b
	d.gradients = append(d.gradients, e)
	return e
}
