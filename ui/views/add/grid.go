package add

import "downex/ui/kit"

// grid is the CSS grids these dialogs use (display: grid with minmax(0, Nfr) columns): the
// kit lays out block and flex boxes, so a grid is a Layouter. Columns share the width after
// the gaps in proportion to their weights, never below 0 (minmax(0, …)); rows are as high as
// their items (auto), an item spanning a flexible row (minmax(0, 1fr)) giving its height to
// that row, as CSS sizes a flexible track with an indefinite height. Items are placed where
// place says, else row-major in order; they stretch across their columns and, with
// AlignStart, keep their own height at the top of their rows (align-items: start), else
// stretch over them (align-items: normal).
type grid struct {
	kit.Element
	// cols gives the column weights for the grid's content width (a container query reads it).
	cols func(w kit.DIP, s kit.Scale) []float32
	// gaps gives the row and column gaps for the width.
	gaps func(w kit.DIP, s kit.Scale) (row, col kit.DIP)
	// place gives an item's area for the width, ok false for auto placement.
	place func(c *kit.Element, w kit.DIP, s kit.Scale) (a area, ok bool)
	// flexRow is the 1-based row that is minmax(0, 1fr) (0: none).
	flexRow func(w kit.DIP, s kit.Scale) int
	start   bool // align-items: start
	// onArrange hears the content width before the items are measured (a dialog's form tells
	// its container width to the grids inside it).
	onArrange func(w kit.DIP)
	// onLayout runs after each layout of the grid's content.
	onLayout func()

	// scratch, kept from layout to layout
	items   []gridItem
	rows    []kit.DIP
	weights []float32
}

// area is a grid item's place: 1-based column and row, and how many it spans (0 = 1; a
// column span of -1 runs to the last column, grid-column: 1 / -1).
type area struct {
	col, colSpan, row, rowSpan int
}

type gridItem struct {
	e       *kit.Element
	a       area
	x, w, h kit.DIP
	mt      kit.DIP // the top margin (in h)
}

func newGrid(cols func(kit.DIP, kit.Scale) []float32, rowGap, colGap kit.DIP) *grid {
	g := &grid{cols: cols}
	g.gaps = func(kit.DIP, kit.Scale) (kit.DIP, kit.DIP) { return rowGap, colGap }
	g.Style.MinWidth = kit.Px(0)
	return g
}

// fixedCols is a column list that does not follow the width.
func fixedCols(ws ...float32) func(kit.DIP, kit.Scale) []float32 {
	return func(kit.DIP, kit.Scale) []float32 { return ws }
}

// arrange places the items for content width w and measures their heights; it reports the
// column positions in g.items and the rows in g.rows.
func (g *grid) arrange(ctx *kit.LayoutContext, w kit.DIP) {
	if g.onArrange != nil {
		g.onArrange(w)
	}
	s := ctx.Scale
	cols := g.cols(w, s)
	n := len(cols)
	if n == 0 {
		n, cols = 1, []float32{1}
	}
	rowGap, colGap := g.gaps(w, s)
	var total float32
	for _, c := range cols {
		total += c
	}
	room := max(w-colGap*kit.DIP(n-1), 0)
	g.weights = append(g.weights[:0], cols...)
	colX := func(i int) kit.DIP { // the start of column i (0-based)
		var x kit.DIP
		for k := 0; k < i; k++ {
			x += room*kit.DIP(cols[k]/total) + colGap
		}
		return x
	}
	colW := func(i, span int) kit.DIP {
		var v kit.DIP
		for k := i; k < i+span && k < n; k++ {
			v += room * kit.DIP(cols[k]/total)
		}
		return v + colGap*kit.DIP(span-1)
	}

	g.items = g.items[:0]
	// auto placement: row-major after the explicit items' cells
	type cell struct{ r, c int }
	used := map[cell]bool{}
	var autos []int
	maxRow := 0
	for _, c := range g.Children() {
		if c.Style.Hidden {
			continue
		}
		it := gridItem{e: c}
		if g.place != nil {
			a, ok := g.place(c, w, s)
			if !ok {
				a.row, a.rowSpan, a.col = 0, 0, 0 // auto placement, with its column span
			}
			it.a = a
		}
		g.items = append(g.items, it)
	}
	for i := range g.items {
		a := &g.items[i].a
		if a.row == 0 {
			autos = append(autos, i)
			continue
		}
		norm(a, n)
		for r := a.row; r < a.row+a.rowSpan; r++ {
			for c := a.col; c < a.col+a.colSpan; c++ {
				used[cell{r, c}] = true
			}
		}
		maxRow = max(maxRow, a.row+a.rowSpan-1)
	}
	r, c := 1, 1
	for _, i := range autos {
		a := &g.items[i].a
		span := a.colSpan
		if span <= 0 || span > n {
			span = n
		}
		if a.colSpan == 0 {
			span = 1
		}
		for {
			if c+span-1 > n {
				r, c = r+1, 1
				continue
			}
			free := true
			for k := c; k < c+span; k++ {
				if used[cell{r, k}] {
					free = false
				}
			}
			if free {
				break
			}
			c++
		}
		a.row, a.rowSpan, a.col, a.colSpan = r, 1, c, span
		for k := c; k < c+span; k++ {
			used[cell{r, k}] = true
		}
		maxRow = max(maxRow, r)
		c += span
	}

	flex := 0
	if g.flexRow != nil {
		flex = g.flexRow(w, s)
	}
	g.rows = g.rows[:0]
	for k := 0; k < maxRow; k++ {
		g.rows = append(g.rows, 0)
	}
	for i := range g.items {
		it := &g.items[i]
		it.x = colX(it.a.col - 1)
		it.w = colW(it.a.col-1, it.a.colSpan)
		it.h = ctx.HeightFor(it.e.Widget(), it.w, w)
		it.mt, _ = it.e.Style.Margin.Top.Resolve(ctx.Scale, w)
		mb, _ := it.e.Style.Margin.Bottom.Resolve(ctx.Scale, w)
		it.h += it.mt + mb // the margin box fills the area
		if it.a.rowSpan == 1 {
			g.rows[it.a.row-1] = max(g.rows[it.a.row-1], it.h)
		}
	}
	// spanning items: what the rows they cross lack goes to the flexible row among them, else
	// to the last
	for i := range g.items {
		it := &g.items[i]
		if it.a.rowSpan == 1 {
			continue
		}
		first, last := it.a.row-1, it.a.row+it.a.rowSpan-2
		var sum kit.DIP
		for k := first; k <= last; k++ {
			sum += g.rows[k]
		}
		sum += rowGap * kit.DIP(it.a.rowSpan-1)
		if it.h > sum {
			to := last
			if flex-1 >= first && flex-1 <= last {
				to = flex - 1
			}
			g.rows[to] += it.h - sum
		}
	}
	_ = rowGap
}

// norm fills an explicit area's spans for n columns.
func norm(a *area, n int) {
	if a.col <= 0 {
		a.col = 1
	}
	if a.rowSpan <= 0 {
		a.rowSpan = 1
	}
	switch {
	case a.colSpan < 0:
		a.colSpan = n - a.col + 1
	case a.colSpan == 0:
		a.colSpan = 1
	}
	if a.col > n {
		a.col = n
	}
	if a.col+a.colSpan-1 > n {
		a.colSpan = n - a.col + 1
	}
}

func (g *grid) height(ctx *kit.LayoutContext, w kit.DIP) kit.DIP {
	g.arrange(ctx, w)
	rowGap, _ := g.gaps(w, ctx.Scale)
	var h kit.DIP
	for i, r := range g.rows {
		if i > 0 {
			h += rowGap
		}
		h += r
	}
	return h
}

// ContentWidths is kit.Layouter's: minmax(0, …) columns have no minimum but the gaps; the
// widest item in every column at most.
func (g *grid) ContentWidths(ctx *kit.LayoutContext) (lo, hi kit.DIP) {
	cols := g.cols(-1, ctx.Scale)
	n := max(len(cols), 1)
	_, colGap := g.gaps(-1, ctx.Scale)
	var widest kit.DIP
	for _, c := range g.Children() {
		if c.Style.Hidden {
			continue
		}
		_, mx := ctx.IntrinsicWidths(c.Widget())
		widest = max(widest, mx)
	}
	gaps := colGap * kit.DIP(n-1)
	return gaps, widest*kit.DIP(n) + gaps
}

// ContentHeight is kit.Layouter's.
func (g *grid) ContentHeight(ctx *kit.LayoutContext, w kit.DIP) kit.DIP { return g.height(ctx, w) }

// LayoutContent is kit.Layouter's.
func (g *grid) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	w := box.Width()
	g.arrange(ctx, w)
	rowGap, _ := g.gaps(w, ctx.Scale)
	tops := make([]kit.DIP, len(g.rows)+1)
	y := box.Min.Y
	for i, r := range g.rows {
		tops[i] = y
		y += r + rowGap
	}
	tops[len(g.rows)] = y
	for _, it := range g.items {
		top := tops[it.a.row-1]
		h := it.h
		if !g.start {
			end := tops[it.a.row-1+it.a.rowSpan] - rowGap
			h = max(h, end-top)
		}
		mb := it.h - it.mt - ctx.HeightFor(it.e.Widget(), it.w, w)
		top += it.mt
		h -= it.mt + mb
		x := box.Min.X + it.x
		if ctx.RTL {
			x = box.Max.X - it.x - it.w
		}
		ctx.Place(it.e.Widget(), kit.R(x, top, x+it.w, top+h), w)
	}
	if g.onLayout != nil {
		g.onLayout()
	}
}
