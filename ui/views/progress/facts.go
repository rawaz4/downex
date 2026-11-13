package progress

import (
	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/views"
)

// facts is the window's description lists as CSS grids: .rows (the Status tab: max-content
// minmax(0, 1fr) max-content minmax(0, 1fr), 12 px apart, rows 5 px apart) and .facts (the
// complete view: max-content minmax(0, 1fr), rows 7 px apart). Each fact is a dt (--muted, on
// one line) and its dd; a wide dd (dd.wide) runs from the second column to the end, and its dt
// starts a row of its own (dt:has(+ dd.wide) { grid-column: 1 }). Items sit in the middle of
// their row (align-items: center). The live regions (.sr-only role=status) are children too,
// taking no room, as position: absolute takes them out of the flow.
type facts struct {
	kit.Element
	cols   int
	rowGap kit.DIP
	items  []fact
	live   []kit.Widget
	shown  []fact // the facts shown, in order (reused)

	// the last layout
	colW  [4]kit.DIP
	rowsH []kit.DIP
}

// fact is one dt and its dd.
type fact struct {
	dt   *views.Text
	dd   kit.Widget
	wide bool
	show bool
}

const factsColumnGap = 12 // var(--sp-6)

func newFacts(cols int, rowGap kit.DIP) *facts {
	f := &facts{cols: cols, rowGap: rowGap}
	f.Style = kit.Style{MinWidth: kit.Px(0)}
	f.Semantics.Role = kit.RoleList
	return f
}

// add appends a fact (shown) and returns its index.
func (f *facts) add(term string, dd kit.Widget, wide bool) int {
	dt := views.NewText(term, fontCell, views.InkMuted, kit.TextOptions{})
	dt.Semantics.Role = kit.RoleTerm
	f.items = append(f.items, fact{dt: dt, dd: dd, wide: wide, show: true})
	f.Add(dt, dd)
	return len(f.items) - 1
}

// addLive adds a live region that takes no room.
func (f *facts) addLive(w kit.Widget) {
	f.live = append(f.live, w)
	f.Add(w)
}

// setShown shows or hides fact i.
func (f *facts) setShown(i int, show bool) {
	it := &f.items[i]
	if it.show == show {
		return
	}
	it.show = show
	it.dt.SetHidden(!show)
	it.dd.Base().SetHidden(!show)
	f.Relayout()
}

// setTerm changes fact i's label.
func (f *facts) setTerm(i int, term string) { f.items[i].dt.SetText(term) }

// cell is where an item goes: its row, its first column and how many it spans.
type cell struct{ row, col, span int }

// place works out where the shown facts go (CSS grid auto-placement, sparse), calling fn for
// each dt and dd, and returns the number of rows.
func (f *facts) place(fn func(w kit.Widget, c cell)) int {
	row, col, rows := 0, 0, 0
	for i := range f.items {
		it := &f.items[i]
		if !it.show {
			continue
		}
		if it.wide {
			if col != 0 {
				row++
			}
			fn(it.dt, cell{row, 0, 1})
			fn(it.dd, cell{row, 1, f.cols - 1})
			rows = row + 1
			row, col = row+1, 0
			continue
		}
		if col+2 > f.cols {
			row, col = row+1, 0
		}
		fn(it.dt, cell{row, col, 1})
		fn(it.dd, cell{row, col + 1, 1})
		rows = row + 1
		col += 2
		if col >= f.cols {
			row, col = row+1, 0
		}
	}
	return rows
}

// columns sizes the columns for a width: the max-content ones (the terms' columns) to their
// widest term, the minmax(0, 1fr) ones sharing what is left.
func (f *facts) columns(ctx *kit.LayoutContext, w kit.DIP) [4]kit.DIP {
	var cw [4]kit.DIP
	f.place(func(it kit.Widget, c cell) {
		if c.span != 1 || c.col%2 != 0 {
			return
		}
		_, max := ctx.IntrinsicWidths(it)
		cw[c.col] = kit.DIP(fmax(float32(cw[c.col]), float32(max)))
	})
	gaps := kit.DIP(f.cols-1) * factsColumnGap
	free := w - gaps
	for c := 0; c < f.cols; c += 2 {
		free -= cw[c]
	}
	share := kit.DIP(fmax(0, float32(free)/float32(f.cols/2)))
	for c := 1; c < f.cols; c += 2 {
		cw[c] = share
	}
	return cw
}

func fmax(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

func (f *facts) span(cw [4]kit.DIP, c cell) (x, w kit.DIP) {
	for i := 0; i < c.col; i++ {
		x += cw[i] + factsColumnGap
	}
	for i := c.col; i < c.col+c.span; i++ {
		w += cw[i]
	}
	w += kit.DIP(c.span-1) * factsColumnGap
	return x, w
}

// rowHeights are the rows' heights at width w.
func (f *facts) rowHeights(ctx *kit.LayoutContext, cw [4]kit.DIP) []kit.DIP {
	f.rowsH = f.rowsH[:0]
	f.place(func(it kit.Widget, c cell) {
		for len(f.rowsH) <= c.row {
			f.rowsH = append(f.rowsH, 0)
		}
		_, w := f.span(cw, c)
		h := ctx.HeightFor(it, w, w)
		if h > f.rowsH[c.row] {
			f.rowsH[c.row] = h
		}
	})
	return f.rowsH
}

// ContentWidths is kit.Layouter's: the terms' columns and the gaps (the others may be 0).
func (f *facts) ContentWidths(ctx *kit.LayoutContext) (min, max kit.DIP) {
	cw := f.columns(ctx, 0)
	for c := 0; c < f.cols; c += 2 {
		min += cw[c]
	}
	min += kit.DIP(f.cols-1) * factsColumnGap
	max = min
	f.place(func(it kit.Widget, c cell) {
		if c.col%2 == 1 {
			_, m := ctx.IntrinsicWidths(it)
			max += m / kit.DIP(f.cols/2)
		}
	})
	return min, max
}

// ContentHeight is kit.Layouter's.
func (f *facts) ContentHeight(ctx *kit.LayoutContext, w kit.DIP) kit.DIP {
	rows := f.rowHeights(ctx, f.columns(ctx, w))
	var h kit.DIP
	for i, r := range rows {
		if i > 0 {
			h += f.rowGap
		}
		h += r
	}
	return h
}

// LayoutContent is kit.Layouter's.
func (f *facts) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	f.colW = f.columns(ctx, box.Width())
	rows := f.rowHeights(ctx, f.colW)
	tops := make([]kit.DIP, len(rows))
	y := box.Min.Y
	for i, r := range rows {
		tops[i] = y
		y += r + f.rowGap
	}
	rtl := ctx.RTL
	f.place(func(it kit.Widget, c cell) {
		x, w := f.span(f.colW, c)
		h := ctx.HeightFor(it, w, w)
		top := tops[c.row] + (rows[c.row]-h)/2
		left := box.Min.X + x
		if rtl {
			left = box.Max.X - x - w
		}
		ctx.Place(it, kit.R(left, top, left+w, top+h), w)
	})
	for _, l := range f.live {
		ctx.Place(l, kit.RectAt(box.Min, kit.Size{}), 0)
	}
}

// liveRegion is an .sr-only role=status element: no room, nothing drawn, its text read out.
func liveRegion() *kit.Element {
	e := &kit.Element{}
	e.Semantics.Role = kit.RoleStatus
	e.Semantics.Live = kit.LivePolite
	return e
}

// setLive changes a live region's text.
func setLive(e *kit.Element, s string) {
	if e.Semantics.Name != s {
		e.Semantics.Name = s
		e.SemanticsChanged()
	}
}

var _ = tokens.Sp6
