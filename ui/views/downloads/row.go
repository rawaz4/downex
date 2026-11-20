package downloads

import (
	"math"
	"strings"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/display"
	"downex/ui/core/format"
	"downex/ui/core/segments"
	"downex/ui/core/text"
	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/views"
)

// RowAction is a row's hover or inline action (DownloadRow.svelte's RowAction): the context
// menu and the shortcuts offer the same.
type RowAction string

// The row actions.
const (
	ActPause   RowAction = "pause"
	ActResume  RowAction = "resume"
	ActConfirm RowAction = "confirm"
	ActFolder  RowAction = "folder"
	ActRemove  RowAction = "remove"
	ActRestart RowAction = "restart"
	// ActRenew is a link that expired: New link… (docs/DESKTOP-SPEC.md §10).
	ActRenew RowAction = "renew"
)

// The columns in their order (display.Columns): a row has a cell for each.
const (
	colName = iota
	colSize
	colProgress
	colSpeed
	colEta
	colConnections
	colStatus
	colAdded
	numCols
)

var columnKeys = [numCols]string{"name", "size", "progress", "speed", "eta", "connections", "status", "added"}

// columnOf is a column key's position in columnKeys, or -1.
func columnOf(key string) int {
	for i, k := range columnKeys {
		if k == key {
			return i
		}
	}
	return -1
}

// row is DownloadRow.svelte: one download in the grid's columns. The list recycles it: BindRow
// shows another download, or the same one's new data.
type row struct {
	kit.Element
	t     *Table
	key   kit.ListKey
	index int
	d     *downexv1.Download
	// ghost: a row that was just removed, shown while it fades out (hidden from assistive
	// technology, takes no input).
	ghost   bool
	stale   bool
	compact bool
	state   downexv1.DownloadState

	cells [numCols]*cell
	// Name: the tile, then the name (with a stream's badge) over its site.
	tile    *tile
	names   *kit.Element
	line    *kit.Element
	name    *kit.Label
	tag     *views.Tag
	tagText string
	sub     *kit.Label
	// Size; Progress: the bar and the percentage, or Review ›.
	size   *kit.Label
	bar    *rowBar
	pct    *kit.Label
	review *review
	// Speed, Time left, Conn.
	speed, eta, conns *kit.Label
	// Status: the mark, the text ("Queued" and its position) and the inline action.
	mark   *mark
	status *kit.Label
	pos    *kit.Label
	inline *inlineAct
	// Added.
	added *kit.Label
	// The hover actions.
	acts *acts

	speedEase, etaEase ease
	// pushes are the canvas states Paint pushed, which PaintOver pops after the children.
	pushes int
	// slide moves a row that changed places from where it was (transform over --dur-5).
	slide      float32
	slideAnim  *kit.Tween
	live       bool
	check      display.FileCheckTone // the file check's tone (DESKTOP-SPEC §9.7)
	cell       display.ProgressCell
	pill       widgets.BarState
	statusTone int
	sizeDim    bool
	nameTitle  string
}

// newRow makes a row of the table.
func newRow(t *Table, ghost bool) *row {
	r := &row{t: t, ghost: ghost, index: -1}
	r.Semantics.Role = kit.RoleRow
	if ghost {
		r.Semantics.Hidden = true
		r.PassThrough = true
	}
	r.Cursor = kit.CursorDefault
	for i := range r.cells {
		c := &cell{r: r, col: i}
		c.Semantics.Role = kit.RoleCell
		c.Style = kit.Style{
			Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, MinWidth: kit.Px(0),
			Padding: kit.EdgesInline(kit.Px(6)), Overflow: kit.OverflowClipInline,
		}
		r.cells[i] = c
		r.Add(c)
	}
	shrink := kit.Style{MinWidth: kit.Px(0), Flex: kit.FlexOf(0, 1, kit.Length{})}
	end := func(f kit.Font) *kit.Label {
		l := kit.NewLabel("", f, kit.TextOptions{Truncate: kit.TruncateEnd, Align: kit.TextAlignEnd})
		l.Style = kit.Style{MinWidth: kit.Px(0), Flex: kit.Flex1}
		return l
	}

	// Name
	nc := r.cells[colName]
	nc.Style.ColumnGap = kit.Px(11)
	r.tile = newTile(26)
	r.names = &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, RowGap: kit.Px(3), MinWidth: kit.Px(0)}}
	r.line = &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(7), MinWidth: kit.Px(0)}}
	// dir="auto": a right-to-left name keeps its start in view and reads ".mp4" (FileName).
	r.name = kit.NewFileName("", fontName, kit.TextOptions{Truncate: kit.TruncateEnd})
	r.name.Style = shrink
	r.tag = r.newTag("")
	r.line.Add(r.name, r.tag)
	r.sub = kit.NewLabel("", fontSub, kit.TextOptions{Truncate: kit.TruncateEnd, Direction: kit.DirAuto})
	r.sub.Style = kit.Style{MinWidth: kit.Px(0)}
	r.names.Add(r.line, r.sub)
	nc.Add(r.tile, r.names)

	// Size
	r.size = end(fontNum)
	r.cells[colSize].Add(r.size)

	// Progress
	pc := r.cells[colProgress]
	pc.Style.ColumnGap = kit.Px(10)
	r.bar = newRowBar(r)
	r.pct = kit.NewLabel("", fontPct, kit.TextOptions{Align: kit.TextAlignEnd})
	r.pct.Style = kit.Style{Flex: kit.FlexNone, MinWidth: kit.Rem(2.2)}
	r.review = newReview(r)
	pc.Add(r.bar, r.pct, r.review)

	// Speed, Time left, Conn.
	r.speed = end(fontNum)
	r.cells[colSpeed].Add(r.speed)
	r.eta = end(fontNum)
	r.cells[colEta].Add(r.eta)
	r.conns = end(fontNum)
	r.cells[colConnections].Add(r.conns)

	// Status
	sc := r.cells[colStatus]
	sc.Style.ColumnGap = kit.Px(8)
	r.mark = newMark(14)
	r.status = kit.NewLabel("", fontStatus, kit.TextOptions{Truncate: kit.TruncateEnd})
	r.status.Style = shrink
	r.pos = kit.NewLabel("", fontStatus, kit.TextOptions{})
	r.pos.Style = kit.Style{Flex: kit.FlexNone}
	r.inline = newInlineAct(r)
	// .text: "Queued" and its position read as one text
	txt := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignBaseline, MinWidth: kit.Px(0), Overflow: kit.OverflowClipInline}}
	txt.Add(r.status, r.pos)
	sc.Add(r.mark, txt, r.inline)

	// Added
	r.added = end(fontNum)
	r.cells[colAdded].Add(r.added)

	r.acts = newActs(r)
	r.Add(r.acts)
	return r
}

// BindRow is kit.ListRow's.
func (r *row) BindRow(index int, key kit.ListKey) {
	d, _ := r.t.store().Get(int64(key))
	same := key == r.key && r.d != nil
	if same && r.index >= 0 && index != r.index {
		r.startSlide(kit.DIP(r.index-index) * r.t.RowHeightNow())
	}
	r.index = index
	if !same {
		r.key = key
		r.speedEase.reset()
		r.etaEase.reset()
		r.mark.stopMorph()
		r.stopSlide()
		r.state = d.GetState()
	}
	r.show(d, same)
}

// show displays d (nil: nothing); again is the same download's new data.
func (r *row) show(d *downexv1.Download, again bool) {
	t := r.t
	prev := r.state
	r.d = d
	r.stale = t.stale
	r.compact = t.compact
	if d == nil {
		return
	}
	now := t.nowMS()
	compact := r.compact
	r.live = d.GetState() == state.StateDownloading && !r.stale
	acting := t.acting() && !r.stale && !r.ghost

	// Name
	name := categories.DisplayName(d)
	sub := display.RowSubline(d)
	r.tile.set(tileKind(string(categories.KindOf(d))), map[bool]kit.DIP{false: 26, true: 16}[compact])
	r.cells[colName].Style.ColumnGap = kit.Px(map[bool]float32{false: 11, true: 8}[compact])
	r.name.SetText(name)
	if compact {
		r.name.SetFont(fontNameCompact)
	} else {
		r.name.SetFont(fontName)
	}
	r.nameTitle = name
	if compact && sub != "" {
		r.nameTitle = name + "\n" + sub
	}
	r.name.Tooltip = r.nameTitle
	media := d.GetKind() == downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA
	if media {
		if b := display.MediaBadge(d); b != r.tagText {
			old := r.tag
			r.tag = r.newTag(b)
			r.line.Remove(old)
			r.line.Add(r.tag)
		}
	}
	r.tag.SetHidden(!media)
	r.sub.SetText(sub)
	r.sub.SetHidden(compact || sub == "")

	// Size
	if d.GetSize() < 0 && display.SizeHint(d) > 0 {
		r.size.SetText("~" + format.Bytes(float64(display.SizeHint(d))))
		r.size.Tooltip = "The size the browser announced"
		r.sizeDim = true
	} else {
		r.size.SetText(format.Size(d))
		r.size.Tooltip = ""
		r.sizeDim = d.GetState() == state.StateCompleted
	}

	// Progress
	r.cell = display.ProgressCellOf(d)
	showBar := r.cell == display.CellBar || r.cell == display.CellProbe
	r.bar.SetHidden(!showBar)
	r.pct.SetHidden(!showBar)
	r.review.SetHidden(!(r.cell == display.CellReview && acting))
	if compact {
		r.pct.SetFont(fontPctCompact)
		r.bar.setSize(widgets.SegCompact)
		r.review.setCompact(true)
	} else {
		r.pct.SetFont(fontPct)
		r.bar.setSize(widgets.SegRow)
		r.review.setCompact(false)
	}
	switch r.cell {
	case display.CellBar:
		c := segments.RowCell(d)
		r.bar.set(widgets.SegCell{Fraction: c.Fraction, Live: c.Live, Indeterminate: c.Indeterminate}, barState(d.GetState()), false)
		r.pct.SetText(format.Percent(format.ProgressFraction(d)))
	case display.CellProbe:
		// PROBING: an indeterminate bar, the hatch drifting while it connects, in the fill tone.
		r.bar.set(widgets.SegCell{Indeterminate: true, Live: true}, widgets.BarProbing, true)
		r.pct.SetText("")
	}

	// Speed, Time left, Conn. (live values only while it runs and downexd answers)
	target := math.NaN()
	etaTarget := math.NaN()
	if r.live {
		target = d.GetSpeedBps()
		etaTarget = format.EtaSeconds(d)
	}
	r.speedEase.to(r, target, again)
	r.etaEase.to(r, etaTarget, again)
	r.showLive()
	if r.live {
		r.speed.SetFont(fontStrong)
		r.conns.SetText(format.Num(float64(d.GetConnections())))
	} else {
		r.speed.SetFont(fontNum)
		r.conns.SetText("")
	}

	// Status
	status := display.StatusText(d)
	r.check = display.ToneOfFileCheck(d)
	markKind := icons.MarkOf(stateName(d.GetState()))
	switch r.check { // the alert mark: the file is not what its name says (DESKTOP-SPEC §9.7)
	case display.FileCheckFail:
		markKind = icons.MarkFailed
	case display.FileCheckWarn:
		markKind = icons.MarkCheckWarn
	}
	r.mark.set(markKind, map[bool]kit.DIP{false: 14, true: 12}[compact])
	r.cells[colStatus].Style.ColumnGap = kit.Px(map[bool]float32{false: 8, true: 7}[compact])
	if s, p, ok := queuedParts(status); ok {
		r.status.SetText(s)
		r.pos.SetText(" " + p)
		r.pos.SetHidden(false)
	} else {
		r.status.SetText(status)
		r.pos.SetHidden(true)
	}
	switch e := d.GetError(); {
	case r.check != display.FileCheckNone && e != "":
		r.cells[colStatus].Tooltip = d.GetFileCheck().GetSummary() + "\n" + text.EscapeBidi(e)
	case r.check != display.FileCheckNone:
		r.cells[colStatus].Tooltip = d.GetFileCheck().GetSummary()
	case e != "":
		r.cells[colStatus].Tooltip = text.EscapeBidi(e)
	default:
		r.cells[colStatus].Tooltip = status
	}
	bold := false
	switch d.GetState() {
	case state.StateDownloading, state.StatePaused, state.StateFailed, state.StateAwaiting:
		bold = true
	}
	if bold {
		r.status.SetFont(fontStatusBold)
	} else {
		r.status.SetFont(fontStatus)
	}
	inline, ok := display.InlineAction(d)
	r.inline.SetHidden(!(ok && acting))
	if ok {
		r.inline.set(inline)
	}
	if again && prev != state.StateCompleted && d.GetState() == state.StateCompleted {
		r.mark.startMorph()
	}
	r.state = d.GetState()

	// Added
	if ms := tsMillis(d.GetCreatedAt()); ms > 0 {
		r.added.SetText(format.Date(ms, now))
	} else {
		r.added.SetText(format.Dash)
	}

	// Hover actions
	r.acts.set(d.GetState(), acting, compact)

	r.Semantics.Name = name + " " + status
	r.Semantics.Description = display.RowDescription(d, now)
	r.SemanticsChanged()
	r.Invalidate()
}

// showLive writes the eased speed and time left.
func (r *row) showLive() {
	if r.live {
		r.speed.SetText(format.Speed(r.speedEase.current))
		r.eta.SetText(format.Eta(r.etaEase.current))
	} else {
		r.speed.SetText("")
		r.eta.SetText("")
	}
}

// queuedParts splits "Queued #2" into "Queued" and "#2" (the position in --faint).
func queuedParts(status string) (s, pos string, ok bool) {
	rest, found := strings.CutPrefix(status, "Queued #")
	if !found || rest == "" {
		return "", "", false
	}
	for _, c := range rest {
		if c < '0' || c > '9' {
			return "", "", false
		}
	}
	return "Queued", "#" + rest, true
}

// stateName is a state as the window names it ("awaiting_confirmation").
func stateName(s downexv1.DownloadState) string {
	switch s {
	case state.StateQueued:
		return "queued"
	case state.StateProbing:
		return "probing"
	case state.StateDownloading:
		return "downloading"
	case state.StatePaused:
		return "paused"
	case state.StateCompleted:
		return "completed"
	case state.StateFailed:
		return "failed"
	case state.StateCanceled:
		return "canceled"
	case state.StateAwaiting:
		return "awaiting_confirmation"
	}
	return "unspecified"
}

// barState is SegmentBar's tone for a state.
func barState(s downexv1.DownloadState) widgets.BarState {
	switch s {
	case state.StateDownloading:
		return widgets.BarDownloading
	case state.StateProbing:
		return widgets.BarProbing
	case state.StatePaused:
		return widgets.BarPaused
	case state.StateFailed:
		return widgets.BarFailed
	case state.StateCompleted:
		return widgets.BarCompleted
	case state.StateCanceled:
		return widgets.BarCanceled
	}
	return widgets.BarQueued
}

func tsMillis(ts interface {
	GetSeconds() int64
	GetNanos() int32
}) float64 {
	if ts == nil || (ts.GetSeconds() == 0 && ts.GetNanos() == 0) {
		return 0
	}
	return float64(ts.GetSeconds())*1000 + float64(ts.GetNanos())/1e6
}

// WidgetTheme is widgets.ThemeSource's: a stale list's rows are drawn through grayscale(1).
func (r *row) WidgetTheme() widgets.Theme {
	if r.stale && !r.ghost {
		return r.t.grayTheme()
	}
	return r.t.baseTheme()
}

// tagInk is the media badge's icon colour, the tag's text colour.
func (r *row) tagInk(t *tokens.Theme) kit.Color {
	ink := t.CVideo
	if t.Dark && !t.IsForced() {
		ink = kit.Mix(ink, 0.55, t.Ink)
	}
	return ink
}

// ---- layout ---------------------------------------------------------------------------------

// ContentWidths is kit.Layouter's: a row never sizes the list.
func (r *row) ContentWidths(*kit.LayoutContext) (min, max kit.DIP) { return 0, 0 }

// ContentHeight is kit.Layouter's.
func (r *row) ContentHeight(*kit.LayoutContext, kit.DIP) kit.DIP { return 0 }

// LayoutContent is kit.Layouter's: each shown column's cell in its track, centred in the row's
// height less its 1 px rule, and the hover actions 10 px from its end.
func (r *row) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	lay := &r.t.layout
	full := box.Width()
	inner := box.Height() - 1 // the bottom rule
	rtl := ctx.RTL
	var placed [numCols]bool
	for i, c := range lay.Shown {
		ci := columnOf(c.Key)
		if ci < 0 {
			continue
		}
		placed[ci] = true
		cl := r.cells[ci]
		cl.index = i
		cl.SetHidden(false)
		w := kit.DIP(lay.W[i])
		x := kit.DIP(display.RowPadStart+lay.X[i]) - r.t.scrollX
		if rtl {
			x = full - x - w
		}
		h := ctx.HeightFor(cl, w, w)
		y := (inner - h) / 2
		ctx.Place(cl, kit.R(box.Min.X+x, box.Min.Y+y, box.Min.X+x+w, box.Min.Y+y+h), full)
	}
	for i, c := range r.cells {
		if !placed[i] {
			c.SetHidden(true)
		}
	}
	if !r.acts.Style.Hidden {
		w, _ := ctx.IntrinsicWidths(r.acts)
		h := ctx.HeightFor(r.acts, w, w)
		total := r.t.rowsWidth(full)
		x := total - 10 - w - r.t.scrollX
		if rtl {
			x = full - total + 10 + r.t.scrollX
		}
		y := (inner - h) / 2
		ctx.Place(r.acts, kit.R(box.Min.X+x, box.Min.Y+y, box.Min.X+x+w, box.Min.Y+y+h), full)
	}
}

// ---- painting -------------------------------------------------------------------------------

// look is the row's ground, rule and selection (the arrival tint mixed in).
func (r *row) Paint(p *kit.PaintContext) {
	t := tok(&r.Element)
	st := r.t.rowState(r)
	sz := p.Size()
	bg := kit.Color{}
	switch {
	case st.Selected && st.Hovered:
		bg = t.SelHover
	case st.Selected:
		bg = t.Sel
	case st.Hovered:
		bg = t.Hover
	}
	// the arrival (900 ms, ease-out): from 6 px up, fading in, on --accent-soft until 35%,
	// then the ground it has; a ghost fades out 3 px down over 220 ms (ease-in)
	alpha, dy, tint := r.t.motionOf(r)
	if tint > 0 {
		bg = kit.Mix(t.AccentSoft, tint, bg)
	}
	pushes := 0
	if dy != 0 {
		p.Canvas.PushTransform(kit.Translation(0, dy))
		pushes++
	}
	if alpha < 1 {
		p.Canvas.PushOpacity(alpha)
		pushes++
	}
	r.pushes = pushes
	if t.IsForced() && st.Selected {
		bg = t.Focus // Highlight
	}
	p.FillRect(kit.R(0, 0, sz.W, sz.H), bg)
	one := p.Scale.SnapWidth(1)
	if !st.Selected {
		p.FillRect(kit.R(0, sz.H-one, sz.W, sz.H), t.LineSoft)
	} else {
		// inset 0 0 0 1px --sel-line (inside the padding box: above the transparent rule)
		pb := p.Scale.SnapRect(kit.R(p.Origin.X, p.Origin.Y, p.Origin.X+sz.W, p.Origin.Y+sz.H-1))
		p.Canvas.StrokeRect(pb.Inset(kit.UniformInsets(one/2)), kit.Stroke{Width: one}, t.SelLine)
		// the 3 px Mint bar at the start, 7 px in from the top and bottom (4 in Compact)
		in := kit.DIP(7)
		if r.compact {
			in = 4
		}
		x0, x1 := kit.DIP(0), kit.DIP(3)
		rad := kit.Radii{TopRight: 3, BottomRight: 3}
		if r.t.rtl() {
			x0, x1 = sz.W-3, sz.W
			rad = kit.Radii{TopLeft: 3, BottomLeft: 3}
		}
		bar := kit.RRect{Rect: p.Scale.SnapRect(kit.R(p.Origin.X+x0, p.Origin.Y+in, p.Origin.X+x1, p.Origin.Y+sz.H-1-in)), Radii: rad}
		p.Canvas.FillRoundedRect(bar.Normalized(), t.Accent)
	}
	r.paintColors(t, st)
}

// pushes counts the pushes Paint made, which PaintOver pops after the children.
func (r *row) PaintOver(p *kit.PaintContext) {
	st := r.t.rowState(r)
	if st.Cursor && st.FocusVisible && !r.ghost {
		p.FocusRing(kit.RRect{Rect: kit.RectAt(kit.Point{}, p.Size())}, kit.Ring{Offset: kit.Px(-2)})
	}
	for ; r.pushes > 0; r.pushes-- {
		p.Canvas.Pop()
	}
}

// paintColors gives the texts their colours for the state and the theme.
func (r *row) paintColors(t *tokens.Theme, st kit.RowState) {
	faint := t.Faint
	if st.Selected && st.Hovered {
		faint = t.Muted // --faint is under 4.5:1 on the hovered selection
	}
	r.name.SetColor(t.Ink)
	r.sub.SetColor(faint)
	if r.sizeDim {
		r.size.SetColor(t.Faint)
	} else {
		r.size.SetColor(t.Ink2)
	}
	pct := t.Ink
	switch r.state {
	case state.StatePaused:
		pct = t.Warn
	case state.StateFailed:
		pct = t.Danger
	case state.StateQueued:
		pct = t.Muted
	}
	r.pct.SetColor(pct)
	if r.live {
		r.speed.SetColor(t.Ink)
	} else {
		r.speed.SetColor(t.Ink2)
	}
	r.eta.SetColor(t.Ink2)
	r.conns.SetColor(t.Ink2)
	sc := t.Ink2
	switch r.state {
	case state.StateDownloading, state.StateProbing:
		sc = t.AccentText
	case state.StatePaused:
		sc = t.Warn
	case state.StateFailed:
		sc = t.Danger
	case state.StateAwaiting:
		sc = t.Info
	case state.StateCompleted, state.StateCanceled:
		sc = faint
	}
	if r.state == state.StateCompleted { // "Check file" (DESKTOP-SPEC §9.7)
		switch r.check {
		case display.FileCheckFail:
			sc = t.Danger
		case display.FileCheckWarn:
			sc = t.Warn
		}
	}
	r.status.SetColor(sc)
	r.pos.SetColor(faint)
	r.added.SetColor(faint)
	if t.IsForced() && st.Selected {
		on := t.Chrome // HighlightText
		for _, l := range []*kit.Label{r.name, r.sub, r.size, r.pct, r.speed, r.eta, r.conns, r.status, r.pos, r.added} {
			l.SetColor(on)
		}
	}
}

// HoverChanged is kit.HoverHandler's: the hover actions show (and take the pointer) while the
// pointer is over the row.
func (r *row) HoverChanged(h bool) { r.acts.hover(h) }

// FillSemantics is kit.SemanticFiller's: aria-rowindex, aria-selected.
func (r *row) FillSemantics(n *kit.SemanticNode) {
	if r.Semantics.Name != "" {
		n.Name = r.Semantics.Name
	}
}

// cell is one grid cell of a row: role gridcell, clipped at its sides only (the names' marks
// reaching past their line box stay whole), with the keyboard's cell ring.
type cell struct {
	kit.Element
	r     *row
	col   int
	index int
}

// FillSemantics is kit.SemanticFiller's: aria-colindex and aria-selected.
func (c *cell) FillSemantics(n *kit.SemanticNode) {
	n.Cell.Col = c.index
	if c.r.d != nil && c.r.t.store().Selection().Has(c.r.d.GetId()) {
		n.States |= kit.NodeSelected
	}
}

// PaintOver is kit.OverPainter's: the active cell's 2 px ring, 3 px inside, --r-xs corners.
func (c *cell) PaintOver(p *kit.PaintContext) {
	r := c.r
	st := r.t.rowState(r)
	if !st.Cursor || !st.FocusVisible || r.ghost || r.t.inHeader || r.t.colIndex() != c.index {
		return
	}
	p.FocusRing(kit.RRect{Rect: kit.RectAt(kit.Point{}, p.Size()), Radii: kit.UniformRadii(4)}, kit.Ring{Offset: kit.Px(-3)})
}

// ---- the eased live values --------------------------------------------------------------------

// ease is motion.svelte.ts's ticking: a number that eases to each new value over 360 ms
// (cubic-out) instead of jumping. Unknown values are shown at once, and so is everything when
// motion is off, and a value that changes again within 720 ms of its last change (a running
// download's progress, reported 4 times a second): easing those would keep a frame loop going
// for as long as the download runs.
type ease struct {
	current, from, target float64
	changed               time.Time
	anim                  *easeAnim
	set                   bool
}

const tickMS = 360 * time.Millisecond

func (e *ease) reset() {
	if e.anim != nil {
		e.anim.dead = true
		e.anim = nil
	}
	*e = ease{}
}

// to eases the value to target; a value shown for the first time is shown at once.
func (e *ease) to(r *row, target float64, again bool) {
	if !e.set || !again {
		e.current, e.target, e.set = target, target, true
		return
	}
	if target == e.target || (math.IsNaN(target) && math.IsNaN(e.target)) {
		return
	}
	w := r.Window()
	var now time.Time
	if w != nil {
		now = w.Now()
	}
	from := e.current
	if e.anim != nil {
		e.anim.dead = true
		e.anim = nil
	}
	streaming := !e.changed.IsZero() && now.Sub(e.changed) < 2*tickMS
	if target != from {
		e.changed = now
	}
	e.target = target
	if streaming || w == nil || !w.MotionOK() || math.IsNaN(target) || math.IsInf(target, 0) ||
		math.IsNaN(from) || math.IsInf(from, 0) || from == target {
		e.current = target
		return
	}
	e.from = from
	e.anim = &easeAnim{e: e, r: r, start: now}
	w.Animate(e.anim)
}

type easeAnim struct {
	e     *ease
	r     *row
	start time.Time
	dead  bool
}

func (a *easeAnim) Step(now time.Time) bool {
	if a.dead {
		return false
	}
	share := float64(now.Sub(a.start)) / float64(tickMS)
	if share >= 1 {
		a.Finish()
		return false
	}
	e := a.e
	e.current = e.from + (e.target-e.from)*float64(kit.CubicOut(float32(share)))
	a.r.showLive()
	return true
}

func (a *easeAnim) Finish() {
	if a.dead {
		return
	}
	a.dead = true
	a.e.current = a.e.target
	if a.e.anim == a {
		a.e.anim = nil
	}
	a.r.showLive()
}

// newTag is the stream's badge (.tag.media): the stream icon and the badge's text, hidden from
// assistive technology (the row's description says it: "HLS stream").
func (r *row) newTag(s string) *views.Tag {
	t := views.NewTag(s, nil)
	t.Tooltip = "Streaming media"
	t.Semantics.Hidden = true
	t.Insert(0, widgets.NewIcon(icons.Stream, 11, r.tagInk))
	r.tagText = s
	return t
}

// startSlide moves the row from offset (DIPs from its new place) to its place over 320 ms,
// ease-out, as the rows' transform transition slides a re-sorted row.
func (r *row) startSlide(offset kit.DIP) {
	w := r.Window()
	r.stopSlide()
	if w == nil || !w.MotionOK() || offset == 0 {
		return
	}
	r.slide = offset
	r.slideAnim = &kit.Tween{From: offset, To: 0, Duration: tokens.Dur5, Ease: kit.EaseOut, Set: func(v float32) {
		r.invalidateMoved(max(abs32(r.slide), abs32(v)))
		r.slide = v
	}}
	w.Animate(r.slideAnim)
	r.invalidateMoved(abs32(offset))
}

func (r *row) stopSlide() {
	if r.slideAnim != nil {
		if w := r.Window(); w != nil {
			w.StopAnimation(r.slideAnim)
		}
		r.slideAnim = nil
	}
	r.slide = 0
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// invalidateMoved repaints the row and reach DIPs above and below it, where a moved row draws.
func (r *row) invalidateMoved(reach kit.DIP) {
	w := r.Window()
	if w == nil {
		return
	}
	b := r.WindowBounds()
	b.Min.Y -= reach + 1
	b.Max.Y += reach + 1
	w.InvalidateRect(b)
}
