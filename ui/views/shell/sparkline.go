package shell

import (
	"math"

	"downex/ui/core/display"
	"downex/ui/core/format"
	"downex/ui/kit"
	"downex/ui/views"
)

// The mini sparkline's box (Sparkline.svelte's variant 'mini': 64 × 16, no axes, baseline or
// caption; a pixel of air above the peak, drawn down to its floor).
const (
	sparkW      kit.DIP = 64
	sparkH      kit.DIP = 16
	sparkTop            = 1.5
	sparkBottom         = 0.75
	sparkLine   kit.DIP = 1.5 // .line stroke-width
)

// SparkPoints are the chart's points for samples in a box w × h (Sparkline.svelte's paths):
// the samples of the last display.HistoryMS before the newest, x by time and y by speed
// against the peak, each rounded to 0.1 as the SVG path writes them; nil when there is
// nothing to draw (fewer than two samples, or no speed).
func SparkPoints(samples []display.SpeedSample, w, h, top, bottom float64, dst []kit.Point) []kit.Point {
	dst = dst[:0]
	if len(samples) < 2 {
		return dst
	}
	peak := 0.0
	for _, s := range samples {
		peak = math.Max(peak, s.V)
	}
	if peak <= 0 {
		return dst
	}
	from := samples[len(samples)-1].T - display.HistoryMS
	for _, s := range samples {
		if s.T < from {
			continue
		}
		x := (s.T - from) / display.HistoryMS * w
		y := h - bottom - (s.V/peak)*(h-top-bottom)
		dst = append(dst, kit.Pt(kit.DIP(round1(x)), kit.DIP(round1(y))))
	}
	if len(dst) < 2 {
		return dst[:0]
	}
	return dst
}

// round1 is toFixed(1)'s value.
func round1(v float64) float64 { return format.Round(v*10) / 10 }

// Sparkline is the status bar's mini chart of the total speed (Sparkline.svelte, variant
// 'mini'): a Mint line (--chart-line, 1.5 px, round joins and caps) over a Mint fill
// (--chart-fill), redrawn once per sample. It is an image named by Label.
type Sparkline struct {
	kit.Element
	samples []display.SpeedSample
	pts     []kit.Point
	line    kit.Path
	area    kit.Path
	res     kit.Resources
	built   bool
	data    kit.PathData
}

// NewSparkline makes a mini sparkline named label ("Total speed over the last minute").
func NewSparkline(label string) *Sparkline {
	s := &Sparkline{}
	s.Style = kit.Style{Width: kit.Px(sparkW), Height: kit.Px(sparkH), Flex: kit.FlexNone}
	s.Semantics = kit.Semantics{Role: kit.RoleImage, Name: label}
	s.PassThrough = true
	return s
}

// Set shows samples (the store's slice, kept as is: it is never changed in place).
func (s *Sparkline) Set(samples []display.SpeedSample) {
	if len(samples) == len(s.samples) && (len(samples) == 0 || samples[len(samples)-1] == s.samples[len(s.samples)-1] && samples[0] == s.samples[0]) {
		return
	}
	s.samples = samples
	s.release()
	s.Invalidate()
}

// Points are the chart's points (after the next paint), for tests.
func (s *Sparkline) Points() []kit.Point {
	return SparkPoints(s.samples, float64(sparkW), float64(sparkH), sparkTop, sparkBottom, nil)
}

func (s *Sparkline) release() {
	if s.line != nil {
		s.line.Release()
		s.line = nil
	}
	if s.area != nil {
		s.area.Release()
		s.area = nil
	}
	s.built = false
}

// Detached is kit.Detacher's: the paths go with the window.
func (s *Sparkline) Detached() {
	s.release()
	s.res = nil
}

// build makes the line and the area once per change of the samples.
func (s *Sparkline) build(res kit.Resources) {
	if s.built && res == s.res {
		return
	}
	s.release()
	s.res, s.built = res, true
	s.pts = SparkPoints(s.samples, float64(sparkW), float64(sparkH), sparkTop, sparkBottom, s.pts)
	if len(s.pts) < 2 {
		return
	}
	d := &s.data
	d.Ops = d.Ops[:0]
	d.Fill = kit.FillNonZero
	for i, p := range s.pts {
		if i == 0 {
			d.MoveTo(p)
		} else {
			d.LineTo(p)
		}
	}
	if line, err := res.NewPath(*d); err == nil {
		s.line = line
	}
	head, tail := s.pts[0], s.pts[len(s.pts)-1]
	d.LineTo(kit.Pt(tail.X, sparkH))
	d.LineTo(kit.Pt(head.X, sparkH))
	d.Close()
	if area, err := res.NewPath(*d); err == nil {
		s.area = area
	}
}

// Paint is kit.Painter's.
func (s *Sparkline) Paint(p *kit.PaintContext) {
	w := s.Window()
	if w == nil || w.Host() == nil {
		return
	}
	s.build(w.Host().Resources())
	if s.line == nil {
		return
	}
	t := views.TokensOf(&s.Element)
	c := p.Canvas
	c.PushTransform(kit.Translation(p.Origin.X, p.Origin.Y))
	if !t.IsForced() {
		c.FillPath(s.area, t.ChartFill)
	}
	line := t.ChartLine
	if t.IsForced() {
		line = t.Ink // CanvasText
	}
	c.StrokePath(s.line, kit.Stroke{Width: sparkLine, Cap: kit.CapRound, Join: kit.JoinRound}, line)
	c.Pop()
}
