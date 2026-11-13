package downloads

import (
	"math"
	"slices"

	"downex/ui/core/display"
	"downex/ui/core/format"
	"downex/ui/kit"
	"downex/ui/kit/tokens"
)

// The chart's texts (SpeedChart.svelte).
const (
	chartNone      = "No speed in the last minute"
	chartNoSamples = "Speed over the last minute: no samples yet"
)

// chartLabel is the chart image's name with samples: "Speed over the last minute: now …,
// average …, peak …".
func chartLabel(now, avg, peak float64) string {
	return "Speed over the last minute: now " + format.Speed(now) + ", average " + format.Speed(avg) + ", peak " + format.Speed(peak)
}

var fontAxis = uiFont(kit.Rem(0.6875), 400, 1)
var fontChartNone = uiFont(tokens.TypeMeta, 400, 1.4)

// chartTop is the plot's top line (TOP).
const chartTop = 5

// speedChart is SpeedChart.svelte: the last minute's speed as a Mint line over a Mint fill,
// dashed grid lines at the top and the middle of a y-axis at the end (0, half and the peak
// rounded up to 1/2/5), and a dot on the newest sample. It is an image named by its values.
type speedChart struct {
	kit.Element
	samples []display.SpeedSample
	stats   display.SpeedStats
	axisFor display.SpeedAxis
	height  kit.DIP
	ready   bool // set has run once
	plot    *chartPlot
	axis    *chartAxis
}

func newSpeedChart(height kit.DIP) *speedChart {
	c := &speedChart{height: height}
	c.Style = kit.Style{Display: kit.DisplayFlex, ColumnGap: kit.Px(8), Height: kit.Px(height), Flex: kit.FlexNone}
	c.plot = &chartPlot{c: c}
	c.plot.Semantics.Role = kit.RoleImage
	c.plot.Style = kit.Style{Flex: kit.Flex1, MinWidth: kit.Px(0), Overflow: kit.OverflowHidden}
	c.plot.none = kit.NewLabel(chartNone, fontChartNone, kit.TextOptions{})
	c.plot.none.Semantics.Hidden = true
	c.plot.Add(c.plot.none)
	c.axis = &chartAxis{c: c}
	c.axis.Semantics.Hidden = true
	c.axis.Style = kit.Style{Flex: kit.FlexNone}
	for i := range c.axis.ticks {
		c.axis.ticks[i] = kit.NewLabel("", fontAxis, kit.TextOptions{})
		c.axis.Add(c.axis.ticks[i])
	}
	c.Add(c.plot, c.axis)
	c.set(nil, height)
	return c
}

// set shows samples in a plot height px high.
func (c *speedChart) set(samples []display.SpeedSample, height kit.DIP) {
	if c.ready && height == c.height && slices.Equal(samples, c.samples) {
		return // the same samples draw the same chart: no layout, no paint
	}
	c.ready = true
	c.samples = append(c.samples[:0], samples...) // a copy: the store's history may change in place
	c.stats = display.SpeedStatsOf(c.samples)
	c.axisFor = display.SpeedAxisFor(c.stats.Peak)
	if height != c.height {
		c.height = height
		c.Style.Height = kit.Px(height)
		c.Relayout()
	}
	for i, l := range c.axis.ticks {
		l.SetText(c.axisFor.Labels[i])
	}
	has := c.hasPaths()
	c.plot.none.SetHidden(has)
	if has {
		latest := samples[len(samples)-1]
		c.plot.Semantics.Name = chartLabel(latest.V, c.stats.Avg, c.stats.Peak)
	} else {
		c.plot.Semantics.Name = chartNoSamples
	}
	c.plot.SemanticsChanged()
	c.plot.dirty = true
	c.plot.RelayoutContent()
	c.plot.Invalidate()
	c.axis.RelayoutContent()
}

func (c *speedChart) hasPaths() bool { return len(c.samples) > 0 && len(c.stats.Shown) >= 2 }

// yOf is a speed's height in the plot.
func (c *speedChart) yOf(v float64) float64 {
	bottom := float64(c.height) - 1
	return bottom - (math.Min(v, c.axisFor.Max)/c.axisFor.Max)*(bottom-chartTop)
}

// chartPlot is the plot: the grid lines, the area, the line and the end dot.
type chartPlot struct {
	kit.Element
	c     *speedChart
	none  *kit.Label
	area  kit.Path
	line  kit.Path
	end   kit.Point
	width kit.DIP
	dirty bool
	data  kit.PathData
}

// toFixed1 is a coordinate as the SVG path writes it (toFixed(1)).
func toFixed1(v float64) kit.DIP {
	return kit.DIP(math.Round(v*10) / 10)
}

func (p *chartPlot) release() {
	if p.area != nil {
		p.area.Release()
		p.area = nil
	}
	if p.line != nil {
		p.line.Release()
		p.line = nil
	}
}

// Detached is kit.Detacher's.
func (p *chartPlot) Detached() { p.release(); p.dirty = true }

// ContentWidths is kit.Layouter's.
func (p *chartPlot) ContentWidths(*kit.LayoutContext) (min, max kit.DIP) { return 0, 0 }

// ContentHeight is kit.Layouter's.
func (p *chartPlot) ContentHeight(*kit.LayoutContext, kit.DIP) kit.DIP { return 0 }

// LayoutContent is kit.Layouter's: the message centred, and the paths for the width.
func (p *chartPlot) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	if !p.none.Style.Hidden {
		w, _ := ctx.IntrinsicWidths(p.none)
		h := ctx.HeightFor(p.none, w, w)
		x := box.Min.X + (box.Width()-w)/2
		y := box.Min.Y + (box.Height()-h)/2
		ctx.Place(p.none, kit.R(x, y, x+w, y+h), box.Width())
	}
	if box.Width() != p.width {
		p.width = box.Width()
		p.dirty = true
	}
}

// build makes the line and the area for the plot's width.
func (p *chartPlot) build() {
	p.dirty = false
	p.release()
	c := p.c
	if !c.hasPaths() {
		return
	}
	w := p.Window()
	if w == nil || w.Host() == nil {
		return
	}
	res := w.Host().Resources()
	W := float64(p.width)
	if W <= 0 {
		W = 220 // a first paint measures nothing: a nominal width
	}
	latest := c.samples[len(c.samples)-1]
	from := latest.T - display.HistoryMS
	right := W - 4 // keep the end dot inside the plot
	pts := make([]kit.Point, len(c.stats.Shown))
	for i, s := range c.stats.Shown {
		pts[i] = kit.Pt(toFixed1(((s.T-from)/display.HistoryMS)*right), toFixed1(c.yOf(s.V)))
	}
	bottom := kit.DIP(float64(c.height) - 1)
	p.data.Reset()
	for i, pt := range pts {
		if i == 0 {
			p.data.MoveTo(pt)
		} else {
			p.data.LineTo(pt)
		}
	}
	p.line, _ = res.NewPath(p.data)
	tail, head := pts[len(pts)-1], pts[0]
	p.data.LineTo(kit.Pt(tail.X, bottom))
	p.data.LineTo(kit.Pt(head.X, bottom))
	p.data.Close()
	p.area, _ = res.NewPath(p.data)
	p.end = tail
}

// Paint is kit.Painter's.
func (p *chartPlot) Paint(pc *kit.PaintContext) {
	c := p.c
	t := tok(&p.Element)
	if p.dirty {
		p.build()
	}
	W := pc.Size().W
	o := pc.Origin
	grid := kit.Stroke{Width: 1, Dash: kit.Dash{On: 3, Off: 3}}
	mid := kit.DIP(c.yOf(c.axisFor.Max / 2))
	bottom := c.height - 1
	pc.Canvas.DrawLine(kit.Pt(o.X, o.Y+chartTop), kit.Pt(o.X+W, o.Y+chartTop), grid, t.ChartGrid)
	pc.Canvas.DrawLine(kit.Pt(o.X, o.Y+mid), kit.Pt(o.X+W, o.Y+mid), grid, t.ChartGrid)
	pc.Canvas.DrawLine(kit.Pt(o.X, o.Y+bottom), kit.Pt(o.X+W, o.Y+bottom), kit.Stroke{Width: 1}, t.ChartGrid)
	if p.line == nil {
		return
	}
	line := t.ChartLine
	if t.IsForced() {
		line = t.Ink
	}
	pc.Canvas.PushTransform(kit.Translation(o.X, o.Y))
	if !t.IsForced() {
		pc.Canvas.FillPath(p.area, t.ChartFill)
	}
	pc.Canvas.StrokePath(p.line, kit.Stroke{Width: 1.6, Cap: kit.CapRound, Join: kit.JoinRound}, line)
	dot := kit.R(p.end.X-3, p.end.Y-3, p.end.X+3, p.end.Y+3)
	pc.Canvas.FillEllipse(dot, t.Surface)
	pc.Canvas.StrokeEllipse(dot, kit.Stroke{Width: 1.6}, line)
	pc.Canvas.Pop()
}

// chartAxis is the y-axis: as wide as its widest label, each tick right-aligned and centred
// on its line; without a line only "0" shows.
type chartAxis struct {
	kit.Element
	c     *speedChart
	ticks [3]*kit.Label
}

// ContentWidths is kit.Layouter's: the widest of the top two labels (the sizers).
func (a *chartAxis) ContentWidths(ctx *kit.LayoutContext) (min, max kit.DIP) {
	w0, _ := ctx.IntrinsicWidths(a.ticks[0])
	w1, _ := ctx.IntrinsicWidths(a.ticks[1])
	w := kit.DIP(math.Max(float64(w0), float64(w1)))
	return w, w
}

// ContentHeight is kit.Layouter's.
func (a *chartAxis) ContentHeight(*kit.LayoutContext, kit.DIP) kit.DIP { return a.c.height }

// LayoutContent is kit.Layouter's.
func (a *chartAxis) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	c := a.c
	ys := [3]float64{chartTop, c.yOf(c.axisFor.Max / 2), float64(c.height) - 1}
	blank := !c.hasPaths()
	for i, l := range a.ticks {
		l.SetHidden(blank && i < 2)
		if blank && i < 2 {
			continue
		}
		w, _ := ctx.IntrinsicWidths(l)
		h := ctx.HeightFor(l, w, w)
		x := box.Max.X - w
		if ctx.RTL {
			x = box.Min.X
		}
		y := box.Min.Y + kit.DIP(ys[i]) - h/2
		ctx.Place(l, kit.R(x, y, x+w, y+h), box.Width())
	}
}

// Paint is kit.Painter's: the labels in --faint.
func (a *chartAxis) Paint(*kit.PaintContext) {
	t := tok(&a.Element)
	for _, l := range a.ticks {
		l.SetColor(t.Faint)
	}
	a.c.plot.none.SetColor(t.Faint)
}
