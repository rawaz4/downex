package downloads

import (
	"strings"

	"downex/ui/core/display"
	"downex/ui/core/query"
	"downex/ui/kit"
)

// header is DownloadTable's .header row: a column header for each shown column (a sort button
// with its label and arrow) and the resize handles of the fixed-width ones, on --surface-2
// between two 1 px --line rules, as wide as the rows.
type header struct {
	kit.Element
	t     *Table
	ths   []*th
	grips []*resizer
}

func newHeader(t *Table) *header {
	h := &header{t: t}
	h.Semantics.Role = kit.RoleRow
	h.Style = kit.Style{Border: kit.EdgesBlock(kit.Px(1))}
	for _, c := range display.Columns() {
		x := newTH(t, c)
		h.ths = append(h.ths, x)
		h.Add(x)
	}
	// The handles come after every header, so that they take the pointer over the next
	// column's 4 px as their z-index does.
	for _, x := range h.ths {
		if x.col.Width > 0 {
			g := &resizer{t: t, col: x.col}
			g.Semantics.Hidden = true
			g.Cursor = kit.CursorColResize
			g.Tooltip = "Drag to resize; double-click to reset"
			x.grip = g
			h.grips = append(h.grips, g)
			h.Add(g)
		}
	}
	return h
}

// ContentWidths is kit.Layouter's: the header never sizes the list.
func (h *header) ContentWidths(*kit.LayoutContext) (min, max kit.DIP) { return 0, 0 }

// ContentHeight is kit.Layouter's: --header-h less the two rules.
func (h *header) ContentHeight(ctx *kit.LayoutContext, _ kit.DIP) kit.DIP {
	return kit.DIP(headerHeight(h.t.remFor(ctx.Scale))) - 2
}

// LayoutContent is kit.Layouter's: the column layout for the rows' width, then each header
// and handle in its track (mirrored right to left).
func (h *header) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	h.t.fitColumns(float64(box.Width()), h.t.remFor(ctx.Scale), float64(ctx.Scale.RemBase()))
	lay := &h.t.layout
	shown := map[string]int{}
	for i, c := range lay.Shown {
		shown[c.Key] = i
	}
	rtl := ctx.RTL
	full := box.Max.X - box.Min.X
	sx := h.t.scrollX
	pos := func(x, w float64) (kit.DIP, kit.DIP) {
		x0 := kit.DIP(display.RowPadStart+x) - sx
		if rtl {
			x0 = full - x0 - kit.DIP(w)
		}
		return box.Min.X + x0, kit.DIP(w)
	}
	for _, x := range h.ths {
		i, ok := shown[x.col.Key]
		x.index = i
		x.SetHidden(!ok)
		if !ok {
			continue
		}
		x0, w := pos(lay.X[i], lay.W[i])
		ctx.Place(x, kit.R(x0, box.Min.Y, x0+w, box.Max.Y), box.Width())
	}
	// the handles: 8 px across each fixed column's end edge, inside the last one
	for _, g := range h.grips {
		i, ok := shown[g.col.Key]
		g.SetHidden(!ok)
		if !ok {
			continue
		}
		x0, w := pos(lay.X[i], lay.W[i])
		end := x0 + w // the end edge's x, left to right
		if rtl {
			end = x0
		}
		gx := end - 4
		if i == len(lay.Shown)-1 {
			gx = end - 8
			if rtl {
				gx = end
			}
		}
		ctx.Place(g, kit.R(gx, box.Min.Y-1, gx+8, box.Max.Y+1), box.Width())
	}
}

// Paint is kit.Painter's: --surface-2 between --line rules.
func (h *header) Paint(p *kit.PaintContext) {
	t := tok(&h.Element)
	sz := p.Size()
	p.FillRect(kit.R(0, 0, sz.W, sz.H), t.Surface2)
	w := p.Scale.SnapWidth(1)
	p.FillRect(kit.R(0, 0, sz.W, w), t.Line)
	p.FillRect(kit.R(0, sz.H-w, sz.W, sz.H), t.Line)
}

// HoverChanged is kit.HoverHandler's: the handles' bars show while the pointer is over the
// header (.header:hover .resizer::after).
func (h *header) HoverChanged(bool) {
	for _, g := range h.grips {
		g.Invalidate()
	}
}

// th is one column header: role columnheader with its sort order, holding the sort button.
type th struct {
	kit.Element
	t     *Table
	col   display.Column
	index int
	btn   *sortButton
	grip  *resizer
}

func newTH(t *Table, c display.Column) *th {
	x := &th{t: t, col: c}
	x.Semantics.Role = kit.RoleColumnHeader
	x.Style = kit.Style{Display: kit.DisplayFlex, MinWidth: kit.Px(0)}
	x.btn = newSortButton(t, c)
	x.Add(x.btn)
	return x
}

// FillSemantics is kit.SemanticFiller's: aria-colindex and aria-sort.
func (x *th) FillSemantics(n *kit.SemanticNode) {
	n.Cell.Col = x.index
	if n.Name == "" {
		n.Name = x.col.Label
		if x.col.Title != "" {
			n.Name = x.col.Title
		}
	}
	s := x.t.store().Sort()
	if s.Key == query.SortKey(x.col.Key) {
		if s.Dir == query.Asc {
			n.Sort = kit.SortAscending
		} else {
			n.Sort = kit.SortDescending
		}
	}
}

// PaintOver is kit.OverPainter's: the keyboard's header cell (.grid:focus-visible .th.current:
// a 2 px --focus outline 2 px inside, --r-xs corners).
func (x *th) PaintOver(p *kit.PaintContext) {
	t := x.t
	if !t.inHeader || t.colIndex() != x.index || !t.ringShown() {
		return
	}
	p.FocusRing(kit.RRect{Rect: kit.RectAt(kit.Point{}, p.Size()), Radii: kit.UniformRadii(4)}, kit.Ring{Offset: kit.Px(-2)})
}

// sortButton is the header's button: the label and the sort arrow, which leads in the
// numeric columns so that the label ends flush with their figures.
type sortButton struct {
	kit.Pressable
	t     *Table
	col   display.Column
	label *kit.Label
	arrow *sortArrow
}

func newSortButton(t *Table, c display.Column) *sortButton {
	b := &sortButton{t: t, col: c}
	b.OnPress = func() {
		t.store().SortBy(query.SortKey(c.Key))
		t.FocusList()
	}
	b.Focus = kit.FocusNone // tabindex -1: the grid keeps the keyboard
	b.Semantics.Role = kit.RoleButton
	if c.Title != "" {
		b.Semantics.Name = c.Title // aria-label
	}
	name := c.Label
	if c.Title != "" {
		name = c.Title
	}
	b.Tooltip = "Sort by " + strings.ToLower(name)
	b.Cursor = kit.CursorPointer
	b.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(4), Width: kit.Pct(100),
		Padding: kit.EdgesInline(kit.Px(6)), Overflow: kit.OverflowHidden, MinWidth: kit.Px(0),
	}
	if c.End {
		b.Style.Reverse = true
	}
	b.label = kit.NewLabel(c.Label, fontHead, kit.TextOptions{})
	b.label.Style = kit.Style{Flex: kit.FlexNone}
	b.arrow = &sortArrow{b: b}
	b.arrow.Semantics.Hidden = true
	b.arrow.PassThrough = true
	b.arrow.Style = kit.Style{Width: kit.Px(10), Height: kit.Px(10), Flex: kit.FlexNone}
	b.Add(b.label, b.arrow)
	return b
}

// HoverChanged is kit.HoverHandler's.
func (b *sortButton) HoverChanged(h bool) {
	b.Pressable.HoverChanged(h)
	b.arrow.Invalidate()
}

func (b *sortButton) sorted() (on, desc bool) {
	s := b.t.store().Sort()
	if s.Key == query.SortKey(b.col.Key) {
		return true, s.Dir == query.Desc
	}
	return false, b.col.End
}

// Paint is kit.Painter's: the label's colour (--faint, --ink under the pointer, 600 --ink-2
// while sorted).
func (b *sortButton) Paint(p *kit.PaintContext) {
	t := tok(&b.Element)
	on, _ := b.sorted()
	f, col := fontHead, t.Faint
	switch {
	case on:
		f, col = fontHeadSorted, t.Ink2
	case b.Hovered():
		col = t.Ink
	}
	b.label.SetFont(f)
	b.label.SetColor(col)
}

// sortArrow is the 10 px arrow: shown on the sorted column (--ink-2), at half strength over a
// header the pointer is on; it points down for a descending order (and for an unsorted
// numeric column, which sorts descending first).
type sortArrow struct {
	kit.Element
	b    *sortButton
	path kit.Path
	res  kit.Resources
}

const arrowPath = "M5 8.5v-7M2.2 4.3 5 1.5l2.8 2.8"

// Paint is kit.Painter's.
func (a *sortArrow) Paint(p *kit.PaintContext) {
	on, desc := a.b.sorted()
	alpha := float32(0)
	switch {
	case on:
		alpha = 1
	case a.b.Hovered():
		alpha = 0.5
	}
	if alpha == 0 {
		return
	}
	w := a.Window()
	if w == nil || w.Host() == nil {
		return
	}
	if res := w.Host().Resources(); a.path == nil || a.res != res {
		if a.path != nil {
			a.path.Release()
		}
		a.path, _ = res.NewSVGPath(arrowPath, kit.FillNonZero)
		a.res = res
	}
	if a.path == nil {
		return
	}
	t := tok(&a.Element)
	col := t.Ink
	if on {
		col = t.Ink2
	}
	col = col.WithAlpha(col.A * alpha)
	at := kit.Pt(p.Scale.Snap(p.Origin.X), p.Scale.Snap(p.Origin.Y))
	m := kit.Translation(at.X, at.Y)
	if desc {
		m = kit.Translation(-5, -5).Then(kit.Rotation(180)).Then(kit.Translation(5+at.X, 5+at.Y))
	}
	p.Canvas.PushTransform(m)
	p.Canvas.StrokePath(a.path, kit.Stroke{Width: 1.4, Cap: kit.CapRound, Join: kit.JoinRound}, col)
	p.Canvas.Pop()
}

// Detached is kit.Detacher's.
func (a *sortArrow) Detached() {
	if a.path != nil {
		a.path.Release()
		a.path, a.res = nil, nil
	}
}

// resizer is a fixed column's 8 px handle across its end edge: a drag resizes the column
// (clamped to its minimum and 40 rem, mirrored right to left) and a double click resets it.
// Its 2 px bar shows while the pointer is over the header or the handle is held.
type resizer struct {
	kit.Element
	t        *Table
	col      display.Column
	dragging bool
	startX   kit.DIP
	start    float64
}

// HandlePointer is kit.PointerHandler's.
func (g *resizer) HandlePointer(in *kit.PointerInput) bool {
	t := g.t
	rem := t.layout.Rem
	switch in.Action {
	case kit.PointerDown:
		if in.Button != kit.ButtonLeft {
			return false
		}
		if in.Clicks >= 2 {
			g.dragging = false
			t.resetWidth(g.col)
			return true
		}
		g.dragging = true
		g.startX = in.Pos.X
		g.start = t.cols.WidthOf(g.col, rem)
		if w := g.Window(); w != nil {
			w.Capture(&g.Element)
		}
		g.Invalidate()
		return true
	case kit.PointerMove:
		if !g.dragging {
			return false
		}
		d := float64(in.Pos.X - g.startX)
		if t.rtl() {
			d = -d
		}
		if t.cols.SetWidth(g.col, g.start+d, rem) {
			t.columnsChanged()
		}
		return true
	case kit.PointerUp:
		if !g.dragging {
			return false
		}
		g.dragging = false
		if w := g.Window(); w != nil {
			w.ReleaseCapture()
		}
		t.cols.Save()
		g.Invalidate()
		return true
	case kit.PointerCancel:
		if g.dragging {
			g.dragging = false
			t.cols.Save()
			g.Invalidate()
		}
	}
	return false
}

// Paint is kit.Painter's: .resizer::after, a 2 px --line-strong bar over the middle half,
// 3 px in from the handle's start.
func (g *resizer) Paint(p *kit.PaintContext) {
	if !g.dragging && !g.t.header.Hovered() {
		return
	}
	t := tok(&g.Element)
	sz := p.Size()
	h := sz.H - 2 // the header's box, without the handle's 1 px either side
	x := kit.DIP(3)
	if g.t.rtl() {
		x = sz.W - 3 - 2
	}
	top := 1 + h*0.25
	r := kit.RoundRect(p.Scale.SnapRect(kit.R(p.Origin.X+x, p.Origin.Y+top, p.Origin.X+x+2, p.Origin.Y+top+h*0.5)), 1)
	p.Canvas.FillRoundedRect(r, t.LineStrong)
}
