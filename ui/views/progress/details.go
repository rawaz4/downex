package progress

import (
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format"
	"downex/ui/core/progress"
	"downex/ui/core/segments"
	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/views"
	"math"
	"slices"
)

// ---- the segment map (SegmentMap.svelte at 18 px, with markers) ------------------------------

// segmentMap is SegmentMap: the byte-range map of the download in a 1 px --line box with
// --r-xs corners, each segment's received part from its start offset and, with markers, a tick
// with a notch at each start position. Its node is an image named by MapLabel.
type segmentMap struct {
	kit.Element
	d       *downexv1.Download
	height  kit.DIP
	markers bool
	shapes  []segments.Shape
	// next is set's scratch: the shapes of a new download at the last paint's width, which
	// set compares with what was drawn (a report that changes nothing repaints nothing).
	next   []segments.Shape
	paintW float64
	notch  kit.Path
	res    kit.Resources
	label  string
}

func newSegmentMap(height kit.DIP, markers bool) *segmentMap {
	m := &segmentMap{height: height, markers: markers}
	// a flex item of the details (flex: 0 1 auto) that clips: in a short window it gives up
	// room, as today's .map does, and shows the canvas's top part
	m.Style = kit.Style{Width: kit.Pct(100), MinWidth: kit.Px(0), Height: kit.Px(height + 2), Border: kit.EdgesAll(kit.Px(1)), Overflow: kit.OverflowHidden}
	m.Semantics.Role = kit.RoleImage
	return m
}

func (m *segmentMap) set(d *downexv1.Download) {
	m.d = d
	if l := segments.MapLabel(d); l != m.label {
		m.label = l
		m.Semantics.Name = l
		m.SemanticsChanged()
	}
	if m.paintW > 0 && d != nil {
		m.next = segments.AppendMapShapes(m.next[:0], d, m.paintW, float64(m.height), m.markers)
		if slices.Equal(m.next, m.shapes) {
			return
		}
	}
	m.Invalidate()
}

func (m *segmentMap) Detached() {
	if m.notch != nil {
		m.notch.Release()
		m.notch, m.res = nil, nil
	}
}

// ensureNotch makes the markers' notch, a triangle from (0, 0) to (8, 0) and (4, 5).
func (m *segmentMap) ensureNotch(res kit.Resources) bool {
	if m.notch != nil && m.res == res {
		return true
	}
	m.Detached()
	var d kit.PathData
	d.MoveTo(kit.Pt(0, 0))
	d.LineTo(kit.Pt(8, 0))
	d.LineTo(kit.Pt(4, 5))
	d.Close()
	p, err := res.NewPath(d)
	if err != nil {
		return false
	}
	m.notch, m.res = p, res
	return true
}

func (m *segmentMap) paintOf(t *tokens.Theme, p segments.Paint) kit.Color {
	switch p {
	case segments.PaintDone:
		return t.SegFill
	case segments.PaintFront:
		return t.SegEdge
	case segments.PaintGap:
		return t.Surface
	case segments.PaintMarker:
		return t.SegEdge // app.css defines no --seg-marker: the front's colour
	}
	return t.SegTrack
}

// Paint draws the border, then the canvas clipped to the padding box.
func (m *segmentMap) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&m.Element)
	outer := views.SnapBox(p, 4)
	border := t.Line
	if t.IsForced() {
		border = t.Ink
	}
	bw := p.Scale.SnapWidth(1)
	inner := kit.RoundRect(outer.Rect.Inset(kit.UniformInsets(bw)), 4-bw)
	defer p.Canvas.StrokeRoundedRect(outer.Inset(bw/2), kit.Stroke{Width: bw}, border)
	if m.d == nil {
		return
	}
	r := inner.Rect
	// the canvas is height tall whatever the box (the box clips it)
	w, h := float64(r.Width()), float64(m.height)
	m.shapes = segments.AppendMapShapes(m.shapes[:0], m.d, w, h, m.markers)
	m.paintW = w
	rtl := m.Window() != nil && m.Window().Direction() == kit.DirRTL
	p.Canvas.PushRoundedClip(inner)
	for i := range m.shapes {
		s := &m.shapes[i]
		col := m.paintOf(t, s.Paint)
		x := s.X
		if rtl {
			x = w - s.X - s.W
		}
		switch s.Kind {
		case segments.ShapeRect:
			if s.W > 0 && s.H > 0 {
				p.Canvas.FillRect(kit.R(r.Min.X+kit.DIP(x), r.Min.Y+kit.DIP(s.Y), r.Min.X+kit.DIP(x+s.W), r.Min.Y+kit.DIP(s.Y+s.H)), col)
			}
		case segments.ShapeStroke:
			x0, x1 := s.X, s.X+s.W
			if rtl {
				x0, x1 = w-x0, w-x1
			}
			p.Canvas.DrawLine(kit.Pt(r.Min.X+kit.DIP(x0), r.Min.Y+kit.DIP(s.Y)), kit.Pt(r.Min.X+kit.DIP(x1), r.Min.Y+kit.DIP(s.Y+s.H)),
				kit.Stroke{Width: segments.HatchStroke}, col)
		case segments.ShapeNotch:
			if w := m.Window(); w != nil && w.Host() != nil && m.ensureNotch(w.Host().Resources()) {
				nx := s.X
				if rtl {
					nx = float64(r.Width()) - s.X - 8
				}
				p.Canvas.PushTransform(kit.Translation(r.Min.X+kit.DIP(nx), r.Min.Y+kit.DIP(s.Y)))
				p.Canvas.FillPath(m.notch, col)
				p.Canvas.Pop()
			}
		}
	}
	p.Canvas.Pop()
}

// ---- the connections (table.conns-table in div.table-wrap) ----------------------------------

const (
	cellPad   = 10 // padding-inline: var(--sp-5)
	rowHeight = 26 // th, td block-size
)

// connTable is the table of connections: N., Downloaded, Speed (numbers at the end, as narrow
// as they are) and Info (the rest of the width, wrapping), a sticky 26 px header on
// --surface-2 with its --line edge, 26 px rows split by --line-soft, in var(--type-small). A
// row's colour follows its connection's state; with none, one row says why.
type connTable struct {
	kit.Element
	pane   *widgets.ScrollPane
	wrap   *tableWrap
	head   *tableRow
	rows   []*tableRow
	shown  int
	empty  *views.Text
	emptyR *kit.Element
	colW   [3]kit.DIP
	offset kit.DIP
}

// tableRow is one row: its four cells' texts and the Info cell's mark.
type tableRow struct {
	kit.Element
	header  bool
	tone    string // the connection's state name (conn-<state>)
	cells   [4]*views.Text
	mark    *stateMark
	info    *kit.Element
	hovered bool
}

func newTableRow(header bool) *tableRow {
	r := &tableRow{header: header}
	r.Semantics.Role = kit.RoleRow
	font := fontSmall
	if header {
		font = fontSmall600
	}
	for i := range r.cells {
		ink := views.InkInk2
		if header {
			ink = views.InkMuted
		}
		o := kit.TextOptions{}
		if i < 3 {
			o.Align = kit.TextAlignEnd
		} else if !header {
			o.Wrap = kit.TextWrapWords
		}
		r.cells[i] = views.NewText("", font, ink, o)
		if header {
			r.cells[i].Semantics.Role = kit.RoleColumnHeader
		} else {
			r.cells[i].Semantics.Role = kit.RoleCell
		}
	}
	if header {
		r.Add(r.cells[0], r.cells[1], r.cells[2], r.cells[3])
		return r
	}
	r.mark = newStateMark()
	r.info = box(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: tokens.Sp3, MinWidth: kit.Px(0)}, r.mark, r.cells[3])
	r.info.Semantics.Role = kit.RoleCell
	r.cells[3].Semantics.Role = kit.RoleNone
	r.cells[3].Style.MinWidth = kit.Px(0)
	r.cells[3].Style.Flex = kit.FlexOf(0, 1, kit.Length{})
	r.Add(r.cells[0], r.cells[1], r.cells[2], r.info)
	return r
}

// HoverChanged: a body row shows --hover under the pointer.
func (r *tableRow) HoverChanged(h bool) {
	if !r.header {
		r.hovered = h
		r.Invalidate()
	}
}

// Paint: the header's --surface-2 with its --line edge; a body row's hover ground and its
// --line-soft rule (none under the last row).
func (r *tableRow) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&r.Element)
	b := p.Scale.SnapRect(kit.RectAt(p.Origin, p.Size()))
	one := p.Scale.SnapWidth(1)
	if r.header {
		// sticky at the top of the box, under its rounded corners
		in := wrapInner - one
		p.Canvas.FillRoundedRect(kit.RRect{Rect: b, Radii: kit.Radii{TopLeft: in, TopRight: in}}, t.Surface2)
		p.Canvas.FillRect(kit.R(b.Min.X, b.Max.Y-one, b.Max.X, b.Max.Y), t.Line)
		return
	}
	if r.hovered {
		// inside the box's rounded corners (the box clips its rows to a plain rectangle, which
		// keeps their text in ClearType)
		if w := r.wrap(); w != nil {
			box := w.WindowBounds().Add(p.Origin.Sub(r.WindowBounds().Min))
			in := p.Scale.SnapRect(box).Inset(kit.UniformInsets(one))
			p.Canvas.PushRoundedClip(kit.RoundRect(in, wrapInner-one))
			p.Canvas.FillRect(b, t.Hover)
			p.Canvas.Pop()
		} else {
			p.Canvas.FillRect(b, t.Hover)
		}
	}
	if tbl, ok := r.Parent().Widget().(*connTable); ok && tbl.rows[tbl.shown-1] != r {
		p.Canvas.FillRect(kit.R(b.Min.X, b.Max.Y-one, b.Max.X, b.Max.Y), t.LineSoft)
	}
}

// wrapInner is the table box's corner radius (--r-md).
const wrapInner = 8

// wrap is the box the row's table scrolls in.
func (r *tableRow) wrap() *tableWrap {
	if tbl, ok := r.Parent().Widget().(*connTable); ok && tbl.wrap != nil {
		return tbl.wrap
	}
	return nil
}

// setTone colours the row as its connection's state does.
func (r *tableRow) setTone(state string) {
	if state == r.tone {
		return
	}
	r.tone = state
	info := views.InkInk2
	switch state {
	case "receiving":
		info = views.InkInk
	case "retrying":
		info = views.InkWarn
	case "error":
		info = views.InkDanger
	}
	others := views.InkInk2
	if state == "done" || state == "idle" {
		info, others = views.InkFaint, views.InkFaint
	}
	for i := 0; i < 3; i++ {
		r.cells[i].Ink = others
	}
	r.cells[3].Ink = info
	r.mark.set(state)
	r.Invalidate()
}

func newConnTable() *connTable {
	t := &connTable{}
	t.Semantics.Role = kit.RoleTable
	t.head = newTableRow(true)
	for i, s := range [4]string{"N.", "Downloaded", "Speed", "Info"} {
		t.head.cells[i].SetText(s)
	}
	t.head.cells[3].SetOptions(kit.TextOptions{})
	t.empty = views.NewText("", fontSmall, views.InkMuted, kit.TextOptions{Align: kit.TextAlignCenter, Wrap: kit.TextWrapWords})
	t.empty.Semantics.Role = kit.RoleCell
	t.emptyR = box(kit.Style{Padding: kit.EdgesOf(tokens.Sp6, kit.Px(cellPad))}, t.empty)
	t.emptyR.Semantics.Role = kit.RoleRow
	t.Add(t.emptyR, t.head)
	return t
}

// set shows the connections; text gives each row's Info.
func (t *connTable) set(conns []*downexv1.ConnectionInfo, info func(c *downexv1.ConnectionInfo) string, emptyText string) {
	for len(t.rows) < len(conns) {
		r := newTableRow(false)
		t.rows = append(t.rows, r)
	}
	if len(conns) != t.shown {
		// rows in order, then the header on top (it paints last)
		kids := make([]kit.Widget, 0, len(conns)+2)
		for i := range conns {
			kids = append(kids, t.rows[i])
		}
		kids = append(kids, t.emptyR, t.head)
		t.SetChildren(kids...)
		t.shown = len(conns)
		t.Relayout()
	}
	t.emptyR.SetHidden(len(conns) > 0)
	t.empty.SetText(emptyText)
	for i, c := range conns {
		r := t.rows[i]
		r.cells[0].SetText(itoa(int64(c.GetIndex())))
		r.cells[1].SetText(progress.ConnectionReceived(c))
		speed := format.Dash
		if c.GetState() == downexv1.ConnectionState_CONNECTION_STATE_RECEIVING && c.GetSpeedBps() > 0 {
			speed = format.Speed(float64(c.GetSpeedBps()))
		}
		r.cells[2].SetText(speed)
		r.cells[3].SetText(escapeBidi(info(c)))
		r.setTone(connStateName(c.GetState()))
	}
}

// scrolled keeps the header at the top of the pane (position: sticky).
func (t *connTable) scrolled(offset kit.DIP) {
	if offset != t.offset {
		t.offset = offset
		t.RelayoutContent()
	}
}

// columns are the number columns' widths: as wide as their widest cell (width: 1%).
func (t *connTable) columns(ctx *kit.LayoutContext) [3]kit.DIP {
	var w [3]kit.DIP
	for i := 0; i < 3; i++ {
		_, m := ctx.IntrinsicWidths(t.head.cells[i])
		w[i] = m
		for _, r := range t.rows[:t.shown] {
			if _, m := ctx.IntrinsicWidths(r.cells[i]); m > w[i] {
				w[i] = m
			}
		}
		w[i] += 2 * cellPad
	}
	return w
}

func (t *connTable) ContentWidths(ctx *kit.LayoutContext) (min, max kit.DIP) {
	w := t.columns(ctx)
	min = w[0] + w[1] + w[2] + 2*cellPad
	_, m := ctx.IntrinsicWidths(t.head.cells[3])
	return min, min + m
}

func (t *connTable) rowH(ctx *kit.LayoutContext, r *tableRow, infoW kit.DIP) kit.DIP {
	h := ctx.HeightFor(r.info, infoW, infoW)
	return kit.DIP(fmax(rowHeight, float32(h)))
}

func (t *connTable) ContentHeight(ctx *kit.LayoutContext, w kit.DIP) kit.DIP {
	cw := t.columns(ctx)
	infoW := w - cw[0] - cw[1] - cw[2] - 2*cellPad
	h := kit.DIP(rowHeight)
	if t.shown == 0 {
		return h + ctx.HeightFor(t.emptyR, w, w)
	}
	for _, r := range t.rows[:t.shown] {
		h += t.rowH(ctx, r, infoW)
	}
	return h
}

func (t *connTable) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	t.colW = t.columns(ctx)
	w := box.Width()
	infoW := kit.DIP(fmax(0, float32(w-t.colW[0]-t.colW[1]-t.colW[2]-2*cellPad)))
	rtl := ctx.RTL
	placeRow := func(r *tableRow, y, h kit.DIP) {
		ctx.Place(r, kit.R(box.Min.X, y, box.Max.X, y+h), w)
	}
	y := box.Min.Y + rowHeight
	if t.shown == 0 {
		h := ctx.HeightFor(t.emptyR, w, w)
		ctx.Place(t.emptyR, kit.R(box.Min.X, y, box.Max.X, y+h), w)
	}
	// border-collapse: the 1 px rule between two rows is centred on their edge, half in each
	// row's cell, so a row's box here runs from half a pixel below its edge (the first row from
	// its edge) to half a pixel below its bottom edge (the last row to its bottom edge), with the
	// rule as its last pixel; its cells centre in the box less the rule.
	for i, r := range t.rows[:t.shown] {
		h := t.rowH(ctx, r, infoW)
		top, bottom := y, y+h
		if i > 0 {
			top += rule / 2
		}
		if i < t.shown-1 {
			bottom += rule / 2
		}
		placeRow(r, top, bottom-top)
		t.layoutCells(ctx, r, t.cellHeight(r, bottom-top), infoW, rtl)
		y += h
	}
	placeRow(t.head, box.Min.Y+t.offset, rowHeight)
	t.layoutCells(ctx, t.head, rowHeight, infoW, rtl)
}

// layoutCells places a row's cells in the columns (after Place laid the row out).
func (t *connTable) layoutCells(ctx *kit.LayoutContext, r *tableRow, h, infoW kit.DIP, rtl bool) {
	x := kit.DIP(0)
	w := r.Size().W
	put := func(c kit.Widget, cx, cw kit.DIP) {
		ch := ctx.HeightFor(c, cw, cw)
		left := cx
		if rtl {
			left = w - cx - cw
		}
		top := (h - ch) / 2
		ctx.Place(c, kit.R(left, top, left+cw, top+ch), cw)
	}
	for i := 0; i < 3; i++ {
		put(r.cells[i], x+cellPad, t.colW[i]-2*cellPad)
		x += t.colW[i]
	}
	if r.header {
		put(r.cells[3], x+cellPad, infoW)
		return
	}
	// span.info is an inline-flex box on the cell's line: its baseline is its first item's,
	// the 12 px mark's bottom edge (an <svg> has no baseline of its own), which sits on the
	// line's baseline (the cell's strut), so the line box is taller than the flex box and the
	// cell centres that line box, not the flex box.
	ch := ctx.HeightFor(r.info, infoW, infoW)
	top := (h - ch) / 2
	if fm, err := ctx.Text.FontMetrics(fontSmall.Style(ctx.Scale)); err == nil {
		// the strut's baseline as the page's engine places it: the font's ascent and descent
		// in whole device pixels, the leading split evenly around them
		px := func(d kit.DIP) kit.DIP {
			return ctx.Scale.FromPixels(float32(math.Round(float64(ctx.Scale.Pixels(d)))))
		}
		asc, desc := px(fm.Ascent), px(fm.Descent)
		base := (fm.LineHeight-(asc+desc))/2 + asc
		shift := base - (ch+markSize)/2 // the flex box's top below the strut's line top
		lineTop := kit.DIP(min(0, float32(shift)))
		lineBottom := kit.DIP(max(float32(fm.LineHeight), float32(shift+ch)))
		top = (h-(lineBottom-lineTop))/2 + shift - lineTop
	}
	left := x + cellPad
	if rtl {
		left = w - left - infoW
	}
	ctx.Place(r.info, kit.R(left, top, left+infoW, top+ch), infoW)
}

// markSize is the stateMark snippet's size in the table (12 px).
const markSize = 12

// Rows lay out their cells themselves (connTable places them).
func (r *tableRow) ContentWidths(*kit.LayoutContext) (min, max kit.DIP) { return 0, 0 }
func (r *tableRow) ContentHeight(*kit.LayoutContext, kit.DIP) kit.DIP   { return rowHeight }
func (r *tableRow) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	if tbl, ok := r.Parent().Widget().(*connTable); ok {
		cw := tbl.colW
		infoW := kit.DIP(fmax(0, float32(box.Width()-cw[0]-cw[1]-cw[2]-2*cellPad)))
		tbl.layoutCells(ctx, r, tbl.cellHeight(r, box.Height()), infoW, ctx.RTL)
	}
}

// rule is the width of the rule under a body row (border-block-end: 1px).
const rule = 1

// cellHeight is the height a body row's cells centre in: its box less the rule under it (the
// last row has none). The header's is its box.
func (t *connTable) cellHeight(r *tableRow, boxH kit.DIP) kit.DIP {
	if r.header || t.shown == 0 || t.rows[t.shown-1] == r {
		return boxH
	}
	return boxH - rule
}

// itoa is strconv's, for the N. column.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
