package widgets

import (
	"math"
	"time"

	"downex/ui/core/format"
	"downex/ui/kit"
	"downex/ui/kit/tokens"
)

// BarState is the download state a bar's colour follows (api.ts's DownloadState).
type BarState uint8

// The states.
const (
	BarDownloading BarState = iota
	BarProbing
	BarQueued
	BarPaused
	BarFailed
	BarCompleted
	BarCanceled
)

// ProgressBar is ProgressBar.svelte (DESIGN-SYSTEM.md §3.10): a single progress bar (the tools
// toast, the Add dialog's setup, a progress window without segments), 8 px high (6 px Small)
// on --seg-track, its fill in the state's colour with a 2 px --seg-edge leading edge while it
// downloads, and the percentage beside it (12.5 px 600, tabular; --warn when paused, --danger
// when failed). The fill and the percentage ease to each new report (360 ms, the streaming rule
// of lib/motion); an unknown size (NaN) while downloading draws a drifting hatch. Decorative
// bars (table rows) hide the bar from assistive technology and expose only the percentage.
type ProgressBar struct {
	kit.Element
	// Label names the bar ("Progress").
	Label string
	// Decorative hides the bar itself from assistive technology.
	Decorative bool
	// Percent formats the percentage (format.Percent when nil).
	Percent  func(float64) string
	fraction float64
	state    BarState
	eased    kit.Eased
	bar      *progressTrack
	pct      *kit.Label
	small    bool
	hatch    *hatchAnim
}

// NewProgressBar makes a bar; small is the 6 px row bar (sm).
func NewProgressBar(label string, small bool) *ProgressBar {
	b := &ProgressBar{Label: label, small: small, fraction: math.NaN()}
	b.Style = kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: tokens.Sp5, Width: kit.Pct(100)}
	b.bar = &progressTrack{b: b}
	h := kit.DIP(8)
	if small {
		h = 6
	}
	b.bar.Style = kit.Style{Flex: kit.Flex1, Height: kit.Px(h), Radius: kit.CornersAll(tokens.RPill), Overflow: kit.OverflowHidden}
	b.bar.Semantics.Role = kit.RoleProgressBar
	f := font(tokens.TypeCell, 600)
	b.pct = kit.NewLabel("", f, kit.TextOptions{Align: kit.TextAlignEnd, Truncate: kit.TruncateNone})
	b.pct.Style.Width = kit.Rem(3.2 * 0.78125) // 3.2em of 12.5 px text
	b.pct.Style.Flex = kit.FlexNone
	b.pct.Semantics.Role = kit.RoleText
	b.eased.OnChange = b.shownChanged
	b.eased.Glide = true // a download's progress moves smoothly between its reports
	b.Add(b.bar, b.pct)
	return b
}

// Set shows a report: fraction from 0 to 1, or NaN when the size is unknown.
func (b *ProgressBar) Set(fraction float64, state BarState) {
	if state != b.state {
		b.state = state
		b.Invalidate()
		b.bar.Invalidate()
	}
	if fraction != b.fraction && !(math.IsNaN(fraction) && math.IsNaN(b.fraction)) {
		b.fraction = fraction
		w := b.Window()
		if math.IsNaN(fraction) {
			b.eased.Reset(float32(math.NaN()))
		} else {
			b.eased.SetTarget(w, float32(fraction))
		}
		b.bar.SemanticsChanged()
	}
	b.shownChanged()
	b.syncHatch()
}

func (b *ProgressBar) indeterminate() bool {
	return math.IsNaN(b.fraction) || math.IsInf(b.fraction, 0)
}

func (b *ProgressBar) shownChanged() {
	p := b.Percent
	if p == nil {
		p = format.Percent
	}
	text := ""
	if !b.indeterminate() {
		if e := b.easedValue(); !math.IsNaN(e) && !math.IsInf(e, 0) {
			text = p(e)
		}
	}
	b.pct.SetText(text)
	b.bar.Invalidate()
}

// shown is how much of the track the fill covers.
func (b *ProgressBar) shown() float64 {
	if b.indeterminate() {
		if b.state == BarDownloading {
			return 1
		}
		return 0
	}
	e := b.easedValue()
	if math.IsNaN(e) || math.IsInf(e, 0) {
		e = b.fraction
	}
	return math.Min(1, math.Max(0, e))
}

// easedValue is the eased fraction, the reported one exactly once the tween is there (the
// tween runs in float32: 0.45 would floor to 44%).
func (b *ProgressBar) easedValue() float64 {
	v := b.eased.Value()
	if v == float32(b.fraction) {
		return b.fraction
	}
	return float64(v)
}

func (b *ProgressBar) syncHatch() {
	w := b.Window()
	on := w != nil && b.indeterminate() && b.state == BarDownloading && w.MotionOK()
	switch {
	case on && b.hatch == nil:
		b.hatch = &hatchAnim{el: &b.bar.Element}
		w.Animate(b.hatch)
	case !on && b.hatch != nil:
		b.hatch.dead = true
		b.hatch = nil
	}
}

// Attached is kit.Attacher's.
func (b *ProgressBar) Attached() { b.Window().Defer(b.syncHatch) }

// Detached is kit.Detacher's.
func (b *ProgressBar) Detached() {
	if b.hatch != nil {
		b.hatch.dead = true
		b.hatch = nil
	}
}

// Paint is kit.Painter's: the percentage's colour.
func (b *ProgressBar) Paint(p *kit.PaintContext) {
	t := tokensOf(&b.Element)
	c := t.Ink
	switch b.state {
	case BarPaused:
		c = t.Warn
	case BarFailed:
		c = t.Danger
	}
	b.pct.SetColor(c)
}

// toneColor is the fill of a state (§3.12): live and done Mint, paused amber, failed red,
// waiting grey.
func toneColor(t *tokens.Theme, s BarState) kit.Color {
	switch s {
	case BarPaused:
		return t.SegPaused
	case BarFailed:
		return t.SegFailed
	case BarQueued, BarCanceled:
		return t.SegIdle
	}
	return t.SegFill
}

// progressTrack is the bar itself.
type progressTrack struct {
	kit.Element
	b     *ProgressBar
	rtl   bool
	stops [4]kit.GradientStop
}

// ContentWidths, ContentHeight and LayoutContent make the track a Layouter that learns the
// direction.
func (t *progressTrack) ContentWidths(*kit.LayoutContext) (min, max kit.DIP) { return 0, 0 }
func (t *progressTrack) ContentHeight(*kit.LayoutContext, kit.DIP) kit.DIP   { return 0 }
func (t *progressTrack) LayoutContent(ctx *kit.LayoutContext, _ kit.Rect)    { t.rtl = ctx.RTL }

// Paint is kit.Painter's: the track, the fill slid in from the start edge (it keeps the
// track's rounded leading edge), the live edge, or the hatch.
func (t *progressTrack) Paint(p *kit.PaintContext) {
	th := tokensOf(&t.Element)
	b := t.b
	r := snapBox(p, p.Size().H/2)
	track, fill := th.SegTrack, toneColor(th, b.state)
	if th.IsForced() {
		track, fill = th.Surface, th.Focus
	}
	p.Canvas.FillRoundedRect(r, track)
	if th.IsForced() {
		strokeInside(p.Canvas, r, p.Scale.SnapWidth(1), th.Ink)
	}
	p.Canvas.PushRoundedClip(r)
	defer p.Canvas.Pop()
	if b.indeterminate() && b.state == BarDownloading {
		var phase float32
		if b.hatch != nil {
			phase = b.hatch.phase(t.Window())
		}
		drawHatch(p.Canvas, r.Rect, fill, track, phase, &t.stops)
		return
	}
	f := float32(b.shown())
	if f <= 0 {
		return
	}
	w := r.Rect.Width()
	fr := kit.RRect{Rect: r.Rect.Add(kit.Pt((f-1)*w, 0)), Radii: r.Radii}
	if t.rtl {
		fr = kit.RRect{Rect: r.Rect.Add(kit.Pt((1-f)*w, 0)), Radii: r.Radii}
	}
	p.Canvas.FillRoundedRect(fr, fill)
	if b.state == BarDownloading && !th.IsForced() {
		e := p.Scale.SnapWidth(2) // inset -2px 0 0 var(--seg-edge)
		x := fr.Rect.Max.X - e
		if t.rtl {
			x = fr.Rect.Min.X
		}
		p.Canvas.PushRoundedClip(fr)
		p.Canvas.FillRect(kit.R(x, fr.Rect.Min.Y, x+e, fr.Rect.Max.Y), th.SegEdge)
		p.Canvas.Pop()
	}
}

// FillSemantics is kit.SemanticFiller's: the progress bar's value, or none for a decorative one.
func (t *progressTrack) FillSemantics(n *kit.SemanticNode) {
	b := t.b
	if b.Decorative {
		n.Role = kit.RoleNone
		return
	}
	n.Name = b.Label
	if b.indeterminate() {
		n.Value = "Size unknown"
		return
	}
	pct := math.Floor(b.fraction*1000) / 10
	n.Range = kit.RangeValue{Valid: true, Min: 0, Max: 100, Value: math.Floor(pct)}
	n.Value = format.Percent(b.fraction)
}

// hatchAnim drifts an unknown-size hatch: 11.32 px every 1.2 s (still under reduced motion).
type hatchAnim struct {
	el    *kit.Element
	start time.Time
	now   time.Time
	dead  bool
}

// Step is kit.Animation's.
func (a *hatchAnim) Step(now time.Time) bool {
	if a.dead {
		return false
	}
	if a.start.IsZero() {
		a.start = now
	}
	a.now = now
	a.el.Invalidate()
	return true
}

// Finish is kit.Animation's.
func (a *hatchAnim) Finish() { a.dead = true }

func (a *hatchAnim) phase(*kit.Window) float32 {
	if a.start.IsZero() {
		return 0
	}
	const period = 1200 * time.Millisecond
	return float32(a.now.Sub(a.start)%period) / float32(period)
}

// hatchTile is the hatch's tile: background-size 11.32 px (8√2: one 8 px stripe pair along the
// 45° line).
const hatchTile = 11.32

// drawHatch fills r with repeating-linear-gradient(-45deg, a 0 4px, b 4px 8px), shifted by
// phase of a tile.
// stops is the widget's own storage for the hatch's stops, so a frame allocates nothing.
func drawHatch(c kit.Canvas, r kit.Rect, a, b kit.Color, phase float32, stops *[4]kit.GradientStop) {
	c.FillRect(r, b)
	dx := phase * hatchTile
	// the -45deg gradient line points to the top left; 8 px along it is one stripe pair
	s := kit.Pt(r.Min.X+dx, r.Max.Y)
	const k = 0.70710678
	e := kit.Pt(s.X-8*k, s.Y-8*k)
	*stops = [4]kit.GradientStop{{Offset: 0, Color: a}, {Offset: 0.5, Color: a}, {Offset: 0.5, Color: b}, {Offset: 1, Color: b}}
	c.FillGradient(r, kit.LinearGradient{Start: s, End: e, Repeat: true, Stops: stops[:]})
}

// SegSize is a SegmentBar's size (SegmentBar.svelte).
type SegSize uint8

// The sizes.
const (
	SegRow     SegSize = iota // 6 px, 3 px ends
	SegCompact                // 4 px, 2 px ends
	SegMini                   // 4 px × 90 px (the palette)
	SegLg                     // 8 px (the progress window's connection map)
)

// SegCell is one cell of a SegmentBar (segments.ts's cells).
type SegCell struct {
	// Fraction is the cell's share done, 0 to 1.
	Fraction float64
	// Live marks a cell a connection is writing (its 2 px live edge).
	Live bool
	// Indeterminate: the size is unknown (a live one draws the hatch).
	Indeterminate bool
}

// SegmentBar is SegmentBar.svelte (DESIGN-SYSTEM.md §3.13): the progress bar of rows and of the
// progress window. Rows draw one cell; the progress window draws the connection map (the cells
// segments.ts's barCells gives for its width) with 2 px gaps that show the ground. A fill runs
// from the inline start in the state's colour, at least 2 px once started, with a static 2 px
// live edge where a connection writes; an unknown size while bytes arrive is a drifting hatch.
// It is decorative: the percentage beside it is the value.
type SegmentBar struct {
	kit.Element
	Size SegSize
	// Cells gives the cells for the bar's width (the progress window's); without it the bar
	// draws Fixed.
	Cells func(width kit.DIP) []SegCell
	Fixed []SegCell
	state BarState
	// Tone overrides the state's colour when set.
	Tone      *BarState
	cells     []SegCell
	rtl       bool
	hatch     *hatchAnim
	lastWidth kit.DIP
	stops     [4]kit.GradientStop

	// The glide (adopt): while a download streams, each cell's fill moves at an even pace from
	// what was drawn to the new report over the time since the previous report, so the bar
	// advances smoothly rather than in steps.
	glide      *segGlide
	glideFrom  []SegCell
	glideTo    []SegCell
	glideStart time.Time
	glideDur   time.Duration
	lastReport time.Time
	drawn      []SegCell // scratch for Paint
}

// segGlide redraws a SegmentBar while its glide runs.
type segGlide struct {
	s    *SegmentBar
	dead bool
}

// Step is kit.Animation's.
func (g *segGlide) Step(now time.Time) bool {
	if g.dead {
		return false
	}
	g.s.Invalidate()
	if !now.Before(g.s.glideStart.Add(g.s.glideDur)) {
		g.s.glide = nil
		return false
	}
	return true
}

// Finish is kit.Animation's: the glide ends at its target.
func (g *segGlide) Finish() {
	if !g.dead {
		g.dead = true
		if g.s.glide == g {
			g.s.glide = nil
		}
		g.s.Invalidate()
	}
}

// adopt makes next the cells to draw, gliding to them when they continue a streaming download
// (same cells, nothing going back, something gone forward, the last report recent enough).
func (s *SegmentBar) adopt(next []SegCell) {
	w := s.Window()
	var now time.Time
	if w != nil {
		now = w.Now()
	}
	since := now.Sub(s.lastReport)
	glide := w != nil && w.MotionOK() && !s.lastReport.IsZero() && since > 0 && since < 2*time.Second &&
		s.state == BarDownloading && len(next) > 0 && len(next) == len(s.cells)
	var from []SegCell
	if glide {
		// A fresh buffer: cellsAt reads glideFrom while it fills this one.
		from = s.cellsAt(now, make([]SegCell, 0, len(next)))
		forward := false
		for i, c := range next {
			p := from[i]
			if c.Indeterminate || p.Indeterminate || c.Fraction < p.Fraction-1e-9 {
				glide = false
				break
			}
			if c.Fraction > p.Fraction {
				forward = true
			}
		}
		glide = glide && forward
	}
	if !now.IsZero() {
		s.lastReport = now
	}
	if !glide {
		s.stopGlide()
		s.cells = next
		return
	}
	s.glideFrom = from
	s.glideTo = append(s.glideTo[:0], next...)
	s.glideStart, s.glideDur = now, kit.GlideDuration(since)
	s.cells = next
	if s.glide == nil {
		s.glide = &segGlide{s: s}
		w.Animate(s.glide)
	}
}

func (s *SegmentBar) stopGlide() {
	if s.glide != nil {
		s.glide.dead = true
		s.glide = nil
	}
}

// cellsAt appends to buf the cells as drawn at now: the glide's position while it runs, else
// the cells themselves.
func (s *SegmentBar) cellsAt(now time.Time, buf []SegCell) []SegCell {
	buf = append(buf, s.cells...)
	if s.glide == nil || s.glideDur <= 0 || len(s.glideFrom) != len(buf) || len(s.glideTo) != len(buf) {
		return buf
	}
	t := float64(now.Sub(s.glideStart)) / float64(s.glideDur)
	if t >= 1 || t < 0 {
		return buf
	}
	for i := range buf {
		f, to := s.glideFrom[i].Fraction, s.glideTo[i].Fraction
		buf[i].Fraction = f + (to-f)*t
	}
	return buf
}

// NewSegmentBar makes a bar of a size.
func NewSegmentBar(size SegSize) *SegmentBar {
	s := &SegmentBar{Size: size}
	s.Semantics.Hidden = true
	h := kit.DIP(6)
	switch size {
	case SegCompact, SegMini:
		h = 4
	case SegLg:
		h = 8
	}
	s.Style = kit.Style{Flex: kit.FlexAuto, MinWidth: kit.Px(0), Height: kit.Px(h)}
	if size == SegMini {
		s.Style.Flex, s.Style.Width = kit.FlexNone, kit.Px(90)
	}
	return s
}

// Set shows the cells and the state.
func (s *SegmentBar) Set(cells []SegCell, state BarState) {
	s.Fixed, s.state = cells, state
	if s.Cells == nil {
		s.adopt(cells)
	}
	s.Invalidate()
	s.syncHatch()
}

// Shown are the cells drawn in the last layout.
func (s *SegmentBar) Shown() []SegCell { return s.cells }

// Drawn are the cells as drawn now: while a glide runs, the fills on their way to Shown.
func (s *SegmentBar) Drawn() []SegCell {
	w := s.Window()
	if w == nil {
		return s.cells
	}
	return s.cellsAt(w.Now(), nil)
}

func (s *SegmentBar) radius() kit.DIP {
	if s.Size == SegCompact || s.Size == SegMini {
		return 2
	}
	return 3
}

// ContentWidths is kit.Layouter's.
func (s *SegmentBar) ContentWidths(*kit.LayoutContext) (min, max kit.DIP) { return 0, 0 }

// ContentHeight is kit.Layouter's.
func (s *SegmentBar) ContentHeight(*kit.LayoutContext, kit.DIP) kit.DIP { return 0 }

// LayoutContent is kit.Layouter's: the cells for the width.
func (s *SegmentBar) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	s.rtl = ctx.RTL
	if s.Cells != nil && (box.Width() != s.lastWidth || s.cells == nil) {
		s.lastWidth = box.Width()
		s.stopGlide() // a new width gives other cells
		s.cells = s.Cells(box.Width())
		s.syncHatch()
	}
}

func (s *SegmentBar) hatching() bool {
	for _, c := range s.cells {
		if c.Indeterminate && c.Live {
			return true
		}
	}
	return false
}

func (s *SegmentBar) syncHatch() {
	w := s.Window()
	on := w != nil && s.hatching() && w.MotionOK()
	switch {
	case on && s.hatch == nil:
		s.hatch = &hatchAnim{el: &s.Element}
		w.Animate(s.hatch)
	case !on && s.hatch != nil:
		s.hatch.dead = true
		s.hatch = nil
	}
}

// Attached is kit.Attacher's.
func (s *SegmentBar) Attached() { s.Window().Defer(s.syncHatch) }

// Detached is kit.Detacher's.
func (s *SegmentBar) Detached() {
	s.stopGlide()
	if s.hatch != nil {
		s.hatch.dead = true
		s.hatch = nil
	}
}

// Paint is kit.Painter's.
func (s *SegmentBar) Paint(p *kit.PaintContext) {
	n := len(s.cells)
	if n == 0 {
		return
	}
	th := tokensOf(&s.Element)
	st := s.state
	if s.Tone != nil {
		st = *s.Tone
	}
	tone := toneColor(th, st)
	if st == BarProbing || st == BarCompleted || st == BarDownloading {
		tone = th.SegFill
	}
	track := th.SegTrack
	if th.IsForced() {
		track, tone = th.Surface, th.Focus
	}
	sz := p.Size()
	gap := kit.DIP(2)
	cw := (sz.W - gap*kit.DIP(n-1)) / kit.DIP(n)
	rad := s.radius()
	var phase float32
	if s.hatch != nil {
		phase = s.hatch.phase(s.Window())
	}
	cells := s.cells
	if s.glide != nil {
		if w := s.Window(); w != nil {
			s.drawn = s.cellsAt(w.Now(), s.drawn[:0])
			cells = s.drawn
		}
	}
	for i, c := range cells {
		j := i
		if s.rtl {
			j = n - 1 - i
		}
		x := kit.DIP(j) * (cw + gap)
		cell := p.Scale.SnapRect(kit.R(p.Origin.X+x, p.Origin.Y, p.Origin.X+x+max(cw, 2), p.Origin.Y+sz.H))
		var radii kit.Radii
		start, end := i == 0, i == n-1
		if s.rtl {
			start, end = end, start
		}
		if start {
			radii.TopLeft, radii.BottomLeft = rad, rad
		}
		if end {
			radii.TopRight, radii.BottomRight = rad, rad
		}
		rr := kit.RRect{Rect: cell, Radii: radii}.Normalized()
		p.Canvas.FillRoundedRect(rr, track)
		if th.IsForced() {
			strokeInside(p.Canvas, rr, p.Scale.SnapWidth(1), th.Ink)
		}
		p.Canvas.PushRoundedClip(rr)
		switch {
		case c.Indeterminate && c.Live:
			drawHatch(p.Canvas, cell, tone, track, phase, &s.stops)
		case !c.Indeterminate && c.Fraction > 0:
			w := cell.Width()
			fw := max(float32(c.Fraction)*w, 2) // a started cell shows at least 2 px
			fr := kit.R(cell.Min.X, cell.Min.Y, cell.Min.X+fw, cell.Max.Y)
			if s.rtl {
				fr = kit.R(cell.Max.X-fw, cell.Min.Y, cell.Max.X, cell.Max.Y)
			}
			p.Canvas.FillRect(fr, tone)
			if c.Live && !th.IsForced() {
				e := p.Scale.SnapWidth(2)
				ex := fr.Max.X - e
				if s.rtl {
					ex = fr.Min.X
				}
				p.Canvas.FillRect(kit.R(ex, fr.Min.Y, ex+e, fr.Max.Y), th.SegEdge)
			}
		}
		p.Canvas.Pop()
	}
}
