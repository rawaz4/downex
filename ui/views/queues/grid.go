package queues

import "downex/ui/kit"

// The kit lays out blocks and flex boxes; the queue dialog's CSS also uses a few grids
// (.fields, .q-row, .q-row.times). grid is the part of CSS grid they use: explicit columns,
// auto rows, items placed in order (one cell each, or a whole row: grid-column: 1 / -1),
// column and row gaps, align-items, and auto rows stretched into a box taller than its
// content (align-content: normal, with a min-block-size).

// track is one column: minmax(0, Nfr) when fr > 0; auto (its items' max-content) when auto;
// else minmax(0, max): it grows up to max before the fr columns take the rest.
type track struct {
	fr   float32
	auto bool
	max  kit.Length
}

// fr1 is minmax(0, 1fr).
var fr1 = track{fr: 1}

type grid struct {
	kit.Element
	// cols is the template at a content width (a container query changes it).
	cols           func(w kit.DIP, s kit.Scale) []track
	colGap, rowGap kit.Length
	// align is align-items (start, center or stretch).
	align kit.Align
	// full reports an item that spans every column.
	full func(c *kit.Element) bool

	// scratch, kept from pass to pass
	widths []kit.DIP
	xs     []kit.DIP
	rows   []kit.DIP
	cells  []gridCell
}

type gridCell struct {
	e        *kit.Element
	row, col int
	span     bool
	w, h     kit.DIP
}

func newGrid(st kit.Style, cols func(w kit.DIP, s kit.Scale) []track, colGap, rowGap kit.Length, align kit.Align, children ...kit.Widget) *grid {
	g := &grid{cols: cols, colGap: colGap, rowGap: rowGap, align: align}
	g.Style = st
	g.Add(children...)
	return g
}

func fixedCols(t ...track) func(kit.DIP, kit.Scale) []track {
	return func(kit.DIP, kit.Scale) []track { return t }
}

func (g *grid) gaps(s kit.Scale) (col, row kit.DIP) {
	col, _ = g.colGap.Resolve(s, -1)
	row, _ = g.rowGap.Resolve(s, -1)
	return col, row
}

func (g *grid) inFlow(c *kit.Element) bool { return !c.Style.Hidden }

func (g *grid) spans(c *kit.Element) bool { return g.full != nil && g.full(c) }

// itemWidth is an item's border-box width in a cell (justify-self: stretch, unless it has a
// width of its own).
func itemWidth(ctx *kit.LayoutContext, c *kit.Element, cell kit.DIP) kit.DIP {
	w := cell
	if v, ok := c.Style.Width.Resolve(ctx.Scale, cell); ok {
		w = v
	}
	if v, ok := c.Style.MaxWidth.Resolve(ctx.Scale, cell); ok {
		w = min(w, v)
	}
	if v, ok := c.Style.MinWidth.Resolve(ctx.Scale, cell); ok {
		w = max(w, v)
	}
	return w
}

// columns works out the column widths and starts at content width w.
func (g *grid) columns(ctx *kit.LayoutContext, w kit.DIP) {
	cols := g.cols(w, ctx.Scale)
	n := len(cols)
	colGap, _ := g.gaps(ctx.Scale)
	g.widths = g.widths[:0]
	g.xs = g.xs[:0]
	for range cols {
		g.widths = append(g.widths, 0)
		g.xs = append(g.xs, 0)
	}
	if n == 0 {
		return
	}
	free := w - colGap*kit.DIP(n-1)
	// auto columns: their items' max-content widths
	if hasAuto(cols) {
		col := 0
		for _, c := range g.Children() {
			if !g.inFlow(c) {
				continue
			}
			if g.spans(c) {
				col = 0
				continue
			}
			if cols[col].auto {
				_, hi := ctx.IntrinsicWidths(c)
				g.widths[col] = max(g.widths[col], hi)
			}
			col = (col + 1) % n
		}
		for i, t := range cols {
			if t.auto {
				g.widths[i] = min(g.widths[i], max(free, 0))
				free -= g.widths[i]
			}
		}
	}
	// minmax(0, max) columns grow to their limits
	var fixed int
	for _, t := range cols {
		if !t.auto && t.fr == 0 {
			fixed++
		}
	}
	for i, t := range cols {
		if t.auto || t.fr > 0 {
			continue
		}
		limit, _ := t.max.Resolve(ctx.Scale, w)
		share := max(free, 0) / kit.DIP(fixed)
		g.widths[i] = min(limit, share)
		free -= g.widths[i]
		fixed--
	}
	var frs float32
	for _, t := range cols {
		frs += t.fr
	}
	for i, t := range cols {
		if t.fr > 0 {
			g.widths[i] = max(free, 0) * kit.DIP(t.fr/frs)
		}
	}
	x := kit.DIP(0)
	for i := range cols {
		g.xs[i] = x
		x += g.widths[i] + colGap
	}
}

func hasAuto(cols []track) bool {
	for _, t := range cols {
		if t.auto {
			return true
		}
	}
	return false
}

// measure places the items in cells and works out the rows' heights at content width w.
func (g *grid) measure(ctx *kit.LayoutContext, w kit.DIP) (height kit.DIP) {
	g.columns(ctx, w)
	n := len(g.widths)
	_, rowGap := g.gaps(ctx.Scale)
	g.cells = g.cells[:0]
	g.rows = g.rows[:0]
	row, col := 0, 0
	for _, c := range g.Children() {
		if !g.inFlow(c) || n == 0 {
			continue
		}
		cell := gridCell{e: c}
		if g.spans(c) {
			if col > 0 {
				row, col = row+1, 0
			}
			cell.row, cell.span = row, true
			cell.w = itemWidth(ctx, c, w)
			row++
		} else {
			cell.row, cell.col = row, col
			cell.w = itemWidth(ctx, c, g.widths[col])
			col++
			if col == n {
				row, col = row+1, 0
			}
		}
		cbw := w
		if !cell.span {
			cbw = g.widths[cell.col]
		}
		cell.h = ctx.HeightFor(c, cell.w, cbw)
		for len(g.rows) <= cell.row {
			g.rows = append(g.rows, 0)
		}
		g.rows[cell.row] = max(g.rows[cell.row], cell.h)
		g.cells = append(g.cells, cell)
	}
	for i, h := range g.rows {
		height += h
		if i > 0 {
			height += rowGap
		}
	}
	return height
}

// ContentWidths is kit.Layouter's: minmax(0, …) columns have no minimum; the maximum is the
// columns' items side by side.
func (g *grid) ContentWidths(ctx *kit.LayoutContext) (lo, hi kit.DIP) {
	cols := g.cols(-1, ctx.Scale)
	n := len(cols)
	if n == 0 {
		return 0, 0
	}
	colGap, _ := g.gaps(ctx.Scale)
	widest := make([]kit.DIP, n)
	col := 0
	for _, c := range g.Children() {
		if !g.inFlow(c) {
			continue
		}
		cmin, cmax := ctx.IntrinsicWidths(c)
		if g.spans(c) {
			hi = max(hi, cmax)
			if cols[0].auto {
				lo = max(lo, cmin)
			}
			col = 0
			continue
		}
		widest[col] = max(widest[col], cmax)
		if cols[col].auto {
			lo = max(lo, cmin)
		}
		col = (col + 1) % n
	}
	sum := colGap * kit.DIP(n-1)
	for _, v := range widest {
		sum += v
	}
	return lo, max(hi, sum)
}

// ContentHeight is kit.Layouter's.
func (g *grid) ContentHeight(ctx *kit.LayoutContext, w kit.DIP) kit.DIP { return g.measure(ctx, w) }

// LayoutContent is kit.Layouter's: the rows stretched into the box, each item in its cell.
func (g *grid) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	h := g.measure(ctx, box.Width())
	if extra := box.Height() - h; extra > 0 && len(g.rows) > 0 {
		each := extra / kit.DIP(len(g.rows))
		for i := range g.rows {
			g.rows[i] += each
		}
	}
	_, rowGap := g.gaps(ctx.Scale)
	for _, cell := range g.cells {
		y := box.Min.Y
		for i := 0; i < cell.row; i++ {
			y += g.rows[i] + rowGap
		}
		rh := g.rows[cell.row]
		x, cw := box.Min.X, box.Width()
		if !cell.span {
			x, cw = box.Min.X+g.xs[cell.col], g.widths[cell.col]
		}
		ih := cell.h
		align := g.align
		if a := cell.e.Style.AlignSelf; a != kit.AlignAuto {
			align = a
		}
		switch align {
		case kit.AlignCenter:
			y += (rh - ih) / 2
		case kit.AlignEnd:
			y += rh - ih
		case kit.AlignStretch:
			if !cell.e.Style.Height.IsSet() {
				ih = rh
			}
		}
		iw := cell.w
		if ctx.RTL {
			x = box.Min.X + box.Max.X - (x + cw)
			x += cw - iw
		}
		ctx.Place(cell.e, kit.R(x, y, x+iw, y+ih), cw)
	}
}
