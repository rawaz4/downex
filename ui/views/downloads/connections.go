package downloads

import (
	"math"
	"slices"
	"strconv"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/display"
	"downex/ui/core/format"
	"downex/ui/core/progress"
	"downex/ui/core/segments"
	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/state"
	"downex/ui/views"
)

// The connections column's texts (ConnectionBoxes.svelte).
const (
	connsTitle       = "Connections"
	connsReceived    = "Received"
	connsCounted     = "counted when the download starts"
	connsNotCurrent  = "Not current: downexd is unreachable"
	connsAllReceived = "All received"
	connsNotStarted  = "Not started"
	connsNotRunning  = "Not running"
	connsTogether    = "Together "
	legendReceived   = "Received"
	legendConnection = "Connection"
	legendRemaining  = "Remaining"
)

var (
	fontConnsHead = uiFont(tokens.TypeCell, 600, 1.4)
	fontConnsSub  = uiFont(tokens.TypeSmall, 400, 1.4)
	fontBoxSpeed  = uiFont(tokens.TypeMeta, 400, 1.2)
	fontMoreN     = uiFont(tokens.TypeSmall, 600, 1.4)
	fontFoot      = uiFont(tokens.TypeMeta, 400, 1.4)
	fontTogether  = uiFont(tokens.TypeSmall, 600, 1.4)
)

// connsView is what ConnectionBoxes shows for a download: the live boxes or the static
// overview, the heading's texts, the foot and the progress value.
type connsView struct {
	media     bool
	title     string
	sub       string
	live      bool
	summary   display.ConnSummary
	cells     []segments.SegCell
	idle      string
	fraction  float64
	value     int // -1: unknown
	twoRows   bool
	barName   string
	stateTone downexv1.DownloadState
	speed     float64
}

// connsOf works ConnectionBoxes' values out (stale: downexd is unreachable; maxConnections:
// its own limit, else the default).
func connsOf(d *downexv1.Download, stale bool, maxConnections int) connsView {
	v := connsView{media: d.GetKind() == downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA, stateTone: d.GetState(), speed: d.GetSpeedBps()}
	one, many := progress.PieceNames(d)
	v.live = display.ShowsConnections(d, stale)
	if v.live {
		v.summary = display.ConnectionBoxes(d.GetConnectionInfo(), display.BoxOptions{Media: v.media, Piece: one})
	} else {
		v.cells = segments.SegmentCells(d, 8)
	}
	v.fraction = format.ProgressFraction(d)
	v.title = connsTitle
	if v.media {
		v.title = many
	}
	if v.media {
		m := d.GetMedia()
		if m != nil && m.GetFragmentsTotal() > 0 {
			v.sub = format.Num(float64(m.GetFragmentsDone())) + " of " + format.Num(float64(m.GetFragmentsTotal()))
		} else {
			v.sub = connsCounted
		}
	} else {
		got := ""
		if d.GetReceived() > 0 || d.GetSize() >= 0 {
			got = display.ReceivedOf(float64(d.GetReceived()), float64(d.GetSize()), d.GetSizeApprox())
		}
		n := 0
		switch {
		case v.live:
			n = len(d.GetConnectionInfo())
		case d.GetState() == state.StateDownloading:
			n = int(d.GetConnections())
		}
		var parts []string
		switch {
		case n > 0:
			s := "s"
			if n == 1 {
				s = ""
			}
			parts = []string{strconv.Itoa(n) + " connection" + s, got}
		case d.GetState() == state.StateCompleted || d.GetMode() == downexv1.Mode_MODE_SINGLE || !(maxConnections > 0):
			parts = []string{got}
		default:
			parts = []string{"Up to " + strconv.Itoa(maxConnections), got}
		}
		for _, p := range parts {
			if p == "" {
				continue
			}
			if v.sub != "" {
				v.sub += " · "
			}
			v.sub += p
		}
	}
	switch {
	case stale:
		v.idle = connsNotCurrent
	case d.GetState() == state.StateCompleted:
		v.idle = connsAllReceived
	case d.GetState() == state.StateAwaiting:
		v.idle = connsNotStarted
	case d.GetState() == state.StateDownloading:
		v.idle = ""
	default:
		v.idle = connsNotRunning
	}
	v.value = -1
	if !math.IsNaN(v.fraction) && !math.IsInf(v.fraction, 0) {
		v.value = int(math.Floor(v.fraction * 100))
	}
	v.twoRows = v.live && len(v.summary.Boxes) > 8
	v.barName = connsReceived
	if v.media {
		v.barName = many + " downloaded"
	}
	return v
}

// count is the number of boxes.
func (v *connsView) count() int {
	if v.live {
		return len(v.summary.Boxes)
	}
	return len(v.cells)
}

// connections is ConnectionBoxes.svelte: one box per connection of a running download, each
// showing how far it is through its current range and its own speed, adding up to "Together";
// without live connections the file's static segment overview.
type connections struct {
	kit.Element
	dl          *Downloads
	head        *kit.Element
	title       *views.Text
	sub         *views.Text
	boxes       *connBoxes
	foot        *kit.Element
	left        *richLine
	legend      *kit.Element
	swatch      [3]*swatch
	legendItems [3]*kit.Element
	view        connsView
}

func newConnections(dl *Downloads) *connections {
	c := &connections{dl: dl}
	c.Style = kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, RowGap: kit.Px(9), MinWidth: kit.Px(0)}
	c.head = &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignBaseline, ColumnGap: kit.Px(8), MinWidth: kit.Px(0)}}
	c.head.Semantics.Role = kit.RoleHeading
	c.head.Semantics.Level = 3
	c.title = views.NewText("", fontConnsHead, views.InkInk2, kit.TextOptions{})
	c.title.Style.Flex = kit.FlexNone
	c.sub = views.NewLine("", fontConnsSub, views.InkFaint)
	c.head.Add(c.title, c.sub)
	c.boxes = &connBoxes{c: c}
	c.boxes.Semantics.Role = kit.RoleProgressBar
	c.foot = &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Wrap: true, AlignItems: kit.AlignBaseline,
		Justify: kit.JustifySpaceBetween, RowGap: kit.Px(4), ColumnGap: kit.Px(12), MinWidth: kit.Px(0)}}
	c.left = newRichLine()
	c.legend = &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, ColumnGap: kit.Px(10), AlignItems: kit.AlignBaseline}}
	c.legend.Semantics.Hidden = true
	for i, name := range []string{legendReceived, legendConnection, legendRemaining} {
		it := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(5)}}
		c.swatch[i] = &swatch{c: c, kind: i}
		c.swatch[i].Style = kit.Style{Width: kit.Px(9), Height: kit.Px(9), Flex: kit.FlexNone}
		it.Add(c.swatch[i], views.NewText(name, fontFoot, views.InkFaint, kit.TextOptions{}))
		c.legendItems[i] = it
		c.legend.Add(it)
	}
	c.foot.Add(c.left, c.legend)
	c.Add(c.head, c.boxes, c.foot)
	return c
}

// set shows a download's connections.
func (c *connections) set(d *downexv1.Download, stale bool, maxConnections int, compact bool) {
	v := connsOf(d, stale, maxConnections)
	if c.boxes.v != nil && c.boxes.compact == compact && sameConns(&c.view, &v) {
		return // nothing shown changed: no layout, no paint (a status check, a report of another download)
	}
	c.view = v
	c.title.SetText(v.title)
	c.sub.SetText(v.sub)
	c.head.Semantics.Name = v.title + " " + v.sub
	c.boxes.set(&c.view, compact)
	c.boxes.Semantics.Name = v.barName
	c.boxes.Semantics.Range = kit.RangeValue{Min: 0, Max: 100}
	if v.value >= 0 {
		c.boxes.Semantics.Range.Valid = true
		c.boxes.Semantics.Range.Value = float64(v.value)
		c.boxes.Semantics.Value = format.Percent(v.fraction)
	} else {
		c.boxes.Semantics.Value = "Unknown"
	}
	c.boxes.SemanticsChanged()
	switch {
	case v.live:
		runs := []run{{connsTogether, fontFoot, views.InkFaint}, {format.Speed(v.summary.Together), fontTogether, views.InkInk}}
		if v.summary.Unit != "" {
			runs = append(runs, run{" · each in " + v.summary.Unit, fontFoot, views.InkFaint})
		}
		c.left.set(runs...)
	case v.idle != "":
		c.left.set(run{v.idle, fontFoot, views.InkFaint})
	default:
		c.left.set(run{connsTogether, fontFoot, views.InkFaint}, run{format.Speed(v.speed), fontTogether, views.InkInk})
	}
	c.legend.SetHidden(v.twoRows)
	c.legendItems[1].SetHidden(!v.live)
	for _, s := range c.swatch {
		s.Invalidate()
	}
}

// sameConns reports whether a and b show the same: every value connections.set draws or names.
func sameConns(a, b *connsView) bool {
	return a.media == b.media && a.title == b.title && a.sub == b.sub && a.live == b.live &&
		a.idle == b.idle && a.fraction == b.fraction && a.value == b.value && a.twoRows == b.twoRows &&
		a.barName == b.barName && a.stateTone == b.stateTone && a.speed == b.speed &&
		a.summary.Together == b.summary.Together && a.summary.Unit == b.summary.Unit &&
		slices.Equal(a.summary.Boxes, b.summary.Boxes) && slices.Equal(a.cells, b.cells)
}

// swatch is a legend's 9 px square: Received in the fill (the state's tone for the static
// cells), Connection in the live edge's colour, Remaining in the track's.
type swatch struct {
	kit.Element
	c    *connections
	kind int
}

// Paint is kit.Painter's.
func (s *swatch) Paint(p *kit.PaintContext) {
	t := tok(&s.Element)
	col := t.SegTrack
	switch s.kind {
	case 0:
		col = t.SegFill
		if !s.c.view.live {
			col = stateFill(t, s.c.view.stateTone)
		}
	case 1:
		col = t.SegEdge
	}
	p.Canvas.FillRoundedRect(kit.RoundRect(p.Scale.SnapRect(kit.RectAt(p.Origin, p.Size())), 2), col)
}

// stateFill is a static cell's fill in its state's tone (.fill.tone-<state>).
func stateFill(t *tokens.Theme, s downexv1.DownloadState) kit.Color {
	switch s {
	case state.StatePaused:
		return t.SegPaused
	case state.StateFailed:
		return t.SegFailed
	case state.StateQueued, state.StateCanceled:
		return t.SegIdle
	}
	return t.SegFill
}

// connBoxes is the boxes' grid (repeat(per row, 1fr), 6 × 4 px gaps): each slot a 26 px box
// (18 px for two rows in Compact) with the speed under it for live connections.
type connBoxes struct {
	kit.Element
	c       *connections
	v       *connsView
	slots   []*connSlot
	compact bool
	hatch   *hatchDrift
}

func (b *connBoxes) set(v *connsView, compact bool) {
	b.v = v
	b.compact = compact
	n := v.count()
	for len(b.slots) < n {
		s := newConnSlot(b)
		b.slots = append(b.slots, s)
		b.Add(s)
	}
	for i, s := range b.slots {
		s.SetHidden(i >= n)
		if i < n {
			s.bind(i)
		}
	}
	b.syncHatch()
	b.RelayoutContent()
	b.Relayout()
	b.Invalidate()
}

func (b *connBoxes) perRow() int { return max(1, min(8, b.v.count())) }

func (b *connBoxes) boxHeight() kit.DIP {
	if b.compact && b.v.twoRows {
		return 18
	}
	return 26
}

// ContentWidths is kit.Layouter's.
func (b *connBoxes) ContentWidths(*kit.LayoutContext) (min, max kit.DIP) { return 0, 0 }

// ContentHeight is kit.Layouter's: the rows of slots.
func (b *connBoxes) ContentHeight(ctx *kit.LayoutContext, w kit.DIP) kit.DIP {
	if b.v == nil {
		return 0
	}
	n := b.v.count()
	per := b.perRow()
	rows := (n + per - 1) / per
	if rows == 0 {
		return 0
	}
	cw := (w - 4*kit.DIP(per-1)) / kit.DIP(per)
	h := kit.DIP(0)
	for i := 0; i < n && i < per; i++ {
		h = max(h, ctx.HeightFor(b.slots[i], cw, w))
	}
	return h*kit.DIP(rows) + 6*kit.DIP(rows-1)
}

// LayoutContent is kit.Layouter's.
func (b *connBoxes) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	if b.v == nil {
		return
	}
	n := b.v.count()
	per := b.perRow()
	w := box.Width()
	cw := (w - 4*kit.DIP(per-1)) / kit.DIP(per)
	rowH := kit.DIP(0)
	for i := 0; i < n && i < per; i++ {
		rowH = max(rowH, ctx.HeightFor(b.slots[i], cw, w))
	}
	for i := range n {
		col, r := i%per, i/per
		x := kit.DIP(col) * (cw + 4)
		if ctx.RTL {
			x = w - x - cw
		}
		y := kit.DIP(r) * (rowH + 6)
		ctx.Place(b.slots[i], kit.R(box.Min.X+x, box.Min.Y+y, box.Min.X+x+cw, box.Min.Y+y+rowH), w)
	}
}

func (b *connBoxes) syncHatch() {
	w := b.Window()
	on := false
	if b.v != nil && !b.v.live {
		for _, c := range b.v.cells {
			if c.Indeterminate && c.Live {
				on = true
			}
		}
	}
	on = on && w != nil && w.MotionOK()
	switch {
	case on && b.hatch == nil:
		b.hatch = &hatchDrift{b: b, start: w.Now()}
		w.Animate(b.hatch)
	case !on && b.hatch != nil:
		b.hatch.dead = true
		b.hatch = nil
	}
}

// Attached is kit.Attacher's.
func (b *connBoxes) Attached() { b.Window().Defer(b.syncHatch) }

// Detached is kit.Detacher's.
func (b *connBoxes) Detached() {
	if b.hatch != nil {
		b.hatch.dead = true
		b.hatch = nil
	}
}

// hatchDrift is the hatch's drift: 11.32 px a 1.2 s, linear, for ever.
type hatchDrift struct {
	b     *connBoxes
	start time.Time
	now   time.Time
	dead  bool
}

func (h *hatchDrift) Step(now time.Time) bool {
	if h.dead {
		return false
	}
	h.now = now
	for _, s := range h.b.slots {
		if !s.Style.Hidden {
			s.box.Invalidate()
		}
	}
	return true
}

func (h *hatchDrift) Finish() { h.dead = true }

func (h *hatchDrift) phase() float32 {
	if h == nil || h.now.IsZero() {
		return 0
	}
	const period = 1200 * time.Millisecond
	return float32(h.now.Sub(h.start)%period) / float32(period)
}

// connSlot is a box with its speed under it (3 px apart), its tooltip the connection's state.
type connSlot struct {
	kit.Element
	b     *connBoxes
	i     int
	box   *connBox
	more  *kit.Label
	speed *kit.Label
}

func newConnSlot(b *connBoxes) *connSlot {
	s := &connSlot{b: b}
	s.Style = kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, RowGap: kit.Px(3), MinWidth: kit.Px(0)}
	s.box = &connBox{s: s}
	s.box.Style = kit.Style{Height: kit.Px(26), Flex: kit.FlexNone}
	s.box.Semantics.Hidden = true
	s.more = kit.NewLabel("", fontMoreN, kit.TextOptions{})
	s.box.Add(s.more)
	s.speed = kit.NewLabel("", fontBoxSpeed, kit.TextOptions{Align: kit.TextAlignCenter})
	s.speed.Style = kit.Style{Margin: kit.EdgesInline(kit.Px(-2)), Overflow: kit.OverflowClipInline, MinWidth: kit.Px(0)}
	s.speed.Semantics.Hidden = true
	s.Add(s.box, s.speed)
	return s
}

func (s *connSlot) bind(i int) {
	s.i = i
	v := s.b.v
	h := kit.Px(float32(s.b.boxHeight()))
	if s.box.Style.Height != h {
		s.box.Style.Height = h
		s.box.Relayout()
	}
	if v.live {
		bx := v.summary.Boxes[i]
		s.Tooltip = bx.Title
		s.speed.SetHidden(false)
		s.speed.SetText(bx.Label)
		s.more.SetHidden(bx.More == 0)
		if bx.More > 0 {
			s.more.SetText("+" + strconv.Itoa(bx.More))
		}
	} else {
		s.Tooltip = ""
		s.speed.SetHidden(true)
		s.more.SetHidden(true)
	}
	s.box.Invalidate()
}

// Paint is kit.Painter's: the speed's colour (--faint when idle).
func (s *connSlot) Paint(*kit.PaintContext) {
	t := tok(&s.Element)
	v := s.b.v
	col := t.Ink2
	if v != nil && v.live && s.i < len(v.summary.Boxes) && v.summary.Boxes[s.i].Speed <= 0 {
		col = t.Faint
	}
	s.speed.SetColor(col)
	s.more.SetColor(t.Ink2)
}

// connBox is one box: --seg-track with the fill from the inline start, a 2 px live edge, the
// retry and error tones, the "+N" box, or the drifting hatch.
type connBox struct {
	kit.Element
	s     *connSlot
	stops [4]kit.GradientStop
}

// LayoutContent centres "+N".
func (b *connBox) ContentWidths(*kit.LayoutContext) (min, max kit.DIP) { return 0, 0 }

// ContentHeight is kit.Layouter's.
func (b *connBox) ContentHeight(*kit.LayoutContext, kit.DIP) kit.DIP { return 0 }

// LayoutContent is kit.Layouter's.
func (b *connBox) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	m := b.s.more
	if m.Style.Hidden {
		return
	}
	w, _ := ctx.IntrinsicWidths(m)
	h := ctx.HeightFor(m, w, w)
	x := box.Min.X + (box.Width()-w)/2
	y := box.Min.Y + (box.Height()-h)/2
	ctx.Place(m, kit.R(x, y, x+w, y+h), box.Width())
}

// Paint is kit.Painter's.
func (b *connBox) Paint(p *kit.PaintContext) {
	s := b.s
	v := s.b.v
	if v == nil {
		return
	}
	t := tok(&b.Element)
	r := kit.RoundRect(p.Scale.SnapRect(kit.RectAt(p.Origin, p.Size())), 4)
	one := p.Scale.SnapWidth(1)
	ring := func(c kit.Color) { p.Canvas.StrokeRoundedRect(r.Inset(one/2), kit.Stroke{Width: one}, c) }
	rtl := p.Env.Scale.PixelsPerDIP > 0 && s.b.c.dl.grid.table.rtl()
	fill := func(f float64, col kit.Color, edge bool) {
		if f <= 0 {
			return
		}
		w := r.Rect.Width() * kit.DIP(math.Min(f, 1))
		fr := kit.R(r.Rect.Min.X, r.Rect.Min.Y, r.Rect.Min.X+w, r.Rect.Max.Y)
		if rtl {
			fr = kit.R(r.Rect.Max.X-w, r.Rect.Min.Y, r.Rect.Max.X, r.Rect.Max.Y)
		}
		p.Canvas.PushRoundedClip(r)
		p.Canvas.FillRect(fr, col)
		if edge {
			e := p.Scale.SnapWidth(2)
			ex := fr.Max.X - e
			if rtl {
				ex = fr.Min.X
			}
			p.Canvas.FillRect(kit.R(ex, fr.Min.Y, ex+e, fr.Max.Y), t.SegEdge)
		}
		p.Canvas.Pop()
	}
	forced := t.IsForced()
	if forced {
		p.Canvas.FillRoundedRect(r, t.Surface)
		ring(t.Ink)
	}
	if v.live {
		bx := v.summary.Boxes[s.i]
		switch {
		case bx.More > 0:
			p.Canvas.FillRoundedRect(r, t.Surface)
			ring(t.LineStrong)
		case bx.Tone == display.BoxRetry:
			if !forced {
				p.Canvas.FillRoundedRect(r, t.WarnSoft)
			}
			fill(bx.Fraction, pick(forced, t.Focus, t.SegPaused), false)
			if !forced {
				ring(t.SegPaused)
			}
		case bx.Tone == display.BoxError:
			if !forced {
				p.Canvas.FillRoundedRect(r, t.DangerSoft)
			}
			fill(bx.Fraction, pick(forced, t.Focus, t.SegFailed), false)
			if !forced {
				ring(t.SegFailed)
			}
		default:
			if !forced {
				p.Canvas.FillRoundedRect(r, t.SegTrack)
			}
			fill(bx.Fraction, pick(forced, t.Focus, t.SegFill), bx.Tone == display.BoxLive && !forced)
		}
		return
	}
	c := v.cells[s.i]
	if !forced {
		p.Canvas.FillRoundedRect(r, t.SegTrack)
	}
	switch {
	case c.Indeterminate && c.Live:
		// repeating-linear-gradient(-45deg, --seg-fill 0 4px, --seg-track 4px 8px), drifting
		p.Canvas.PushRoundedClip(r)
		dx := s.b.hatch.phase() * 11.32
		start := kit.Pt(r.Rect.Min.X+dx, r.Rect.Max.Y)
		const k = 0.70710678
		end := kit.Pt(start.X-8*k, start.Y-8*k)
		b.stops = [4]kit.GradientStop{{Offset: 0, Color: t.SegFill}, {Offset: 0.5, Color: t.SegFill}, {Offset: 0.5, Color: t.SegTrack}, {Offset: 1, Color: t.SegTrack}}
		p.Canvas.FillGradient(r.Rect, kit.LinearGradient{Start: start, End: end, Repeat: true, Stops: b.stops[:]})
		p.Canvas.Pop()
	case !c.Indeterminate:
		fill(c.Fraction, pick(forced, t.Focus, stateFill(t, v.stateTone)), c.Live && !forced)
	}
}

func pick(cond bool, a, b kit.Color) kit.Color {
	if cond {
		return a
	}
	return b
}
