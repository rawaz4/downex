package settings

import (
	"math"

	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/views"
)

// The layouts the Settings pages use that kit's block and flex boxes do not have: CSS grid
// tracks (the pages' grid-template-columns and HTML's table columns), balanced CSS columns
// (Integration), and inline text in several fonts (Integration's steps, an update's head).

// trackKind is how a grid track takes its width.
type trackKind uint8

const (
	// tFixed is a length (20px, 6.5rem).
	tFixed trackKind = iota
	// tFr is minmax(min, <fr>fr): a share of the room left after the other tracks.
	tFr
	// tAuto is auto (max-content, at least min-content). With grow it is an HTML table's
	// auto column: the room left over is shared in proportion to the columns' max-content
	// widths, as the table's auto layout does.
	tAuto
)

// track is one column of a grid.
type track struct {
	kind trackKind
	size kit.Length // tFixed: the width; tFr: the minimum
	fr   float32
	grow bool
}

func fixed(l kit.Length) track              { return track{kind: tFixed, size: l} }
func frac(min kit.Length, fr float32) track { return track{kind: tFr, size: min, fr: fr} }
func auto() track                           { return track{kind: tAuto} }
func tableAuto() track                      { return track{kind: tAuto, grow: true} }
func tracks(ts ...track) []track            { return ts }

// repeatFr is repeat(n, minmax(0, 1fr)).
func repeatFr(n int) []track {
	out := make([]track, n)
	for i := range out {
		out[i] = frac(kit.Px(0), 1)
	}
	return out
}

// place is where a grid item goes: its column (from 0; -1 = the next free one), its span (-1 =
// every column: grid-column 1 / -1), its row (from 0; -1 = auto) and row span, and its own
// alignment (justify-self, align-self; AlignAuto = the grid's).
type place struct {
	col, span, row, rowSpan int
	justify, align          kit.Align
}

func autoPlace() place { return place{col: -1, span: 1, row: -1, rowSpan: 1} }

// gridItem is a placed child.
type gridItem struct {
	e                    *kit.Element
	p                    place
	row, col, span, rows int
}

// grid is a CSS grid with auto rows: items in DOM order, auto-placed row by row or put where
// their place says.
type grid struct {
	kit.Element
	look
	cols           []track
	colGap, rowGap kit.Length
	// alignItems is align-items (AlignAuto = stretch); justifyItems justify-items.
	alignItems, justifyItems kit.Align
	places                   map[*kit.Element]place
	// table is an HTML table's auto layout (table-layout: auto) for a grid of tFixed columns
	// (a cell's specified width) and auto ones (tFr or tAuto): with less room than the fixed
	// columns' widths and the auto columns' min-content widths, the auto columns keep their
	// min-content widths and the fixed ones give up their room above theirs, in proportion to
	// it (CSS Tables 3 §3.9.3, the min-content and min-content-specified guesses).
	table bool
	// respond restyles the grid for the pane's width before each layout (its container
	// queries: @container settings-pane).
	respond func()

	// scratch, kept from layout to layout
	items  []gridItem
	widths []kit.DIP
	mins   []kit.DIP
	maxs   []kit.DIP
	rowsH  []kit.DIP
}

func newGrid(cols []track, colGap, rowGap kit.Length, children ...kit.Widget) *grid {
	g := &grid{cols: cols, colGap: colGap, rowGap: rowGap}
	g.Style.MinWidth = kit.Px(0)
	g.Add(children...)
	return g
}

// addAt adds w at p.
func (g *grid) addAt(w kit.Widget, p place) {
	g.setPlace(w, p)
	g.Add(w)
}

func (g *grid) setPlace(w kit.Widget, p place) {
	if g.places == nil {
		g.places = map[*kit.Element]place{}
	}
	g.places[w.Base()] = p
}

func (g *grid) placeOf(e *kit.Element) place {
	if p, ok := g.places[e]; ok {
		return p
	}
	return autoPlace()
}

// arrange places the visible children on the grid's cells.
func (g *grid) arrange() int {
	if g.respond != nil {
		g.respond()
	}
	n := len(g.cols)
	g.items = g.items[:0]
	r, c := 0, 0
	rows := 0
	for _, e := range g.Children() {
		if e.Style.Hidden {
			continue
		}
		p := g.placeOf(e)
		span := p.span
		if span < 0 || span > n {
			span = n
		}
		var row, col int
		switch {
		case p.row >= 0 && p.col >= 0:
			row, col = p.row, p.col
		case p.col >= 0:
			if p.col < c {
				r++
			}
			row, col = r, p.col
			r, c = row, col+span
		default:
			if c+span > n {
				r, c = r+1, 0
			}
			row, col = r, c
			c += span
		}
		if c >= n && p.row < 0 {
			r, c = r+1, 0
		}
		rs := max(p.rowSpan, 1)
		rows = max(rows, row+rs)
		g.items = append(g.items, gridItem{e: e, p: p, row: row, col: col, span: span, rows: rs})
	}
	return rows
}

func (g *grid) gaps(s kit.Scale) (cg, rg kit.DIP) {
	cg, _ = g.colGap.Resolve(s, 0)
	rg, _ = g.rowGap.Resolve(s, 0)
	return cg, rg
}

// columnWidths fills g.widths for a content width w.
func (g *grid) columnWidths(ctx *kit.LayoutContext, w kit.DIP) {
	n := len(g.cols)
	g.widths = resize(g.widths, n)
	g.mins = resize(g.mins, n)
	g.maxs = resize(g.maxs, n)
	for i := range n {
		g.mins[i], g.maxs[i] = 0, 0
	}
	for _, it := range g.items {
		if it.span != 1 {
			continue
		}
		lo, hi := ctx.IntrinsicWidths(it.e.Widget())
		g.mins[it.col] = max(g.mins[it.col], lo)
		g.maxs[it.col] = max(g.maxs[it.col], hi)
	}
	cg, _ := g.gaps(ctx.Scale)
	avail := w - cg*kit.DIP(max(n-1, 0))
	used, frs := kit.DIP(0), float32(0)
	var growMax, growMin kit.DIP
	for i, t := range g.cols {
		switch t.kind {
		case tFixed:
			v, _ := t.size.Resolve(ctx.Scale, w)
			g.widths[i] = v
			used += v
		case tAuto:
			g.widths[i] = max(g.maxs[i], g.mins[i])
			used += g.widths[i]
			if t.grow {
				growMax += g.widths[i]
				growMin += g.mins[i]
			}
		case tFr:
			frs += t.fr
		}
	}
	left := avail - used
	if g.table && g.squeezeTable(ctx, w, avail) {
		return
	}
	if growMax > 0 {
		// An HTML table's auto columns: the room left is shared in proportion to their
		// max-content widths; with too little, each is cut towards its min-content width.
		for _, t := range g.cols {
			if t.kind == tFr {
				left = 0
			}
		}
		switch {
		case left >= 0:
			for i, t := range g.cols {
				if t.kind == tAuto && t.grow {
					g.widths[i] += left * g.widths[i] / growMax
				}
			}
		case growMax > growMin:
			k := max(0, (growMax+left-growMin)/(growMax-growMin))
			for i, t := range g.cols {
				if t.kind == tAuto && t.grow {
					g.widths[i] = g.mins[i] + (g.widths[i]-g.mins[i])*k
				}
			}
		}
		return
	}
	if frs <= 0 {
		return
	}
	// minmax(min, fr): a track whose share is below its minimum keeps the minimum and the
	// others share the rest (CSS Grid §12.7.1).
	frozen := make([]bool, 0, 8)
	frozen = append(frozen, make([]bool, n)...)
	for range n {
		share := max(left, 0) / kit.DIP(frs)
		again := false
		for i, t := range g.cols {
			if t.kind != tFr || frozen[i] {
				continue
			}
			lo, _ := t.size.Resolve(ctx.Scale, w)
			if share*kit.DIP(t.fr) < lo {
				frozen[i] = true
				g.widths[i] = lo
				left -= lo
				frs -= t.fr
				again = true
			}
		}
		if !again || frs <= 0 {
			break
		}
	}
	share := max(left, 0) / kit.DIP(max(frs, 1e-6))
	for i, t := range g.cols {
		if t.kind == tFr && !frozen[i] {
			g.widths[i] = share * kit.DIP(t.fr)
		}
	}
}

// squeezeTable sets a table's column widths when avail is below its min-content-specified
// guess (the fixed columns at their specified widths, the others at min-content), and reports
// whether it did. Below the min-content guess every column keeps its min-content width and the
// table overflows.
func (g *grid) squeezeTable(ctx *kit.LayoutContext, w, avail kit.DIP) bool {
	var minGuess, fixedGuess, spare kit.DIP
	for i, t := range g.cols {
		lo := g.mins[i]
		if t.kind == tFr {
			m, _ := t.size.Resolve(ctx.Scale, w)
			lo = max(lo, m)
		}
		minGuess += lo
		if t.kind == tFixed {
			v, _ := t.size.Resolve(ctx.Scale, w)
			hi := max(v, lo)
			fixedGuess += hi
			spare += hi - lo
		} else {
			fixedGuess += lo
		}
	}
	if avail >= fixedGuess {
		return false
	}
	k := kit.DIP(0)
	if spare > 0 && avail > minGuess {
		k = (avail - minGuess) / spare
	}
	for i, t := range g.cols {
		lo := g.mins[i]
		if t.kind == tFr {
			m, _ := t.size.Resolve(ctx.Scale, w)
			lo = max(lo, m)
		}
		g.widths[i] = lo
		if t.kind == tFixed {
			v, _ := t.size.Resolve(ctx.Scale, w)
			g.widths[i] = lo + (max(v, lo)-lo)*k
		}
	}
	return true
}

func resize(s []kit.DIP, n int) []kit.DIP {
	if cap(s) < n {
		return make([]kit.DIP, n)
	}
	return s[:n]
}

// cellX is the start of column col and the width of span columns from it.
func (g *grid) cell(col, span int, cg kit.DIP) (x, w kit.DIP) {
	for i := range col {
		x += g.widths[i] + cg
	}
	for i := col; i < col+span && i < len(g.widths); i++ {
		w += g.widths[i]
		if i > col {
			w += cg
		}
	}
	return x, w
}

// itemWidth is an item's width in its cell (stretched, or its max-content width).
func (g *grid) itemWidth(ctx *kit.LayoutContext, it gridItem, cw kit.DIP) kit.DIP {
	j := it.p.justify
	if j == kit.AlignAuto {
		j = g.justifyItems
	}
	if j == kit.AlignAuto || j == kit.AlignStretch {
		return cw
	}
	_, hi := ctx.IntrinsicWidths(it.e.Widget())
	return min(hi, cw)
}

// rowHeights fills g.rowsH.
func (g *grid) rowHeights(ctx *kit.LayoutContext, rows int, cg, rg kit.DIP) {
	g.rowsH = resize(g.rowsH, rows)
	for i := range g.rowsH {
		g.rowsH[i] = 0
	}
	for _, it := range g.items {
		if it.rows != 1 {
			continue
		}
		_, cw := g.cell(it.col, it.span, cg)
		h := ctx.HeightFor(it.e.Widget(), g.itemWidth(ctx, it, cw), cw)
		g.rowsH[it.row] = max(g.rowsH[it.row], h)
	}
	for _, it := range g.items {
		if it.rows == 1 {
			continue
		}
		_, cw := g.cell(it.col, it.span, cg)
		h := ctx.HeightFor(it.e.Widget(), g.itemWidth(ctx, it, cw), cw)
		have := rg * kit.DIP(it.rows-1)
		for r := it.row; r < it.row+it.rows; r++ {
			have += g.rowsH[r]
		}
		if h > have {
			g.rowsH[it.row+it.rows-1] += h - have
		}
	}
}

// ContentWidths is kit.Layouter's.
func (g *grid) ContentWidths(ctx *kit.LayoutContext) (lo, hi kit.DIP) {
	g.arrange()
	n := len(g.cols)
	mins := make([]kit.DIP, n)
	maxs := make([]kit.DIP, n)
	for _, it := range g.items {
		if it.span != 1 {
			continue
		}
		a, b := ctx.IntrinsicWidths(it.e.Widget())
		mins[it.col] = max(mins[it.col], a)
		maxs[it.col] = max(maxs[it.col], b)
	}
	cg, _ := g.gaps(ctx.Scale)
	for i, t := range g.cols {
		switch t.kind {
		case tFixed:
			v, _ := t.size.Resolve(ctx.Scale, 0)
			if g.table {
				// a table's fixed column shrinks to its min-content width
				lo, hi = lo+mins[i], hi+max(v, mins[i])
				continue
			}
			lo, hi = lo+v, hi+v
		case tFr:
			m, _ := t.size.Resolve(ctx.Scale, 0)
			lo, hi = lo+m, hi+max(maxs[i], m)
		default:
			lo, hi = lo+mins[i], hi+maxs[i]
		}
	}
	gaps := cg * kit.DIP(max(n-1, 0))
	return lo + gaps, hi + gaps
}

// ContentHeight is kit.Layouter's.
func (g *grid) ContentHeight(ctx *kit.LayoutContext, w kit.DIP) kit.DIP {
	rows := g.arrange()
	g.columnWidths(ctx, w)
	cg, rg := g.gaps(ctx.Scale)
	g.rowHeights(ctx, rows, cg, rg)
	return g.sumRows(rg)
}

func (g *grid) sumRows(rg kit.DIP) kit.DIP {
	h := kit.DIP(0)
	for i, v := range g.rowsH {
		if i > 0 {
			h += rg
		}
		h += v
	}
	return h
}

// Paint is kit.Painter's.
func (g *grid) Paint(p *kit.PaintContext) { g.look.paint(&g.Element, p) }

// LayoutContent is kit.Layouter's.
func (g *grid) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	rows := g.arrange()
	w := box.Width()
	g.columnWidths(ctx, w)
	cg, rg := g.gaps(ctx.Scale)
	g.rowHeights(ctx, rows, cg, rg)
	// align-content: normal stretches the auto rows into a taller box (a 52 px row)
	if sum := g.sumRows(rg); rows > 0 && box.Height() > sum+0.01 {
		extra := (box.Height() - sum) / kit.DIP(rows)
		for i := range g.rowsH {
			g.rowsH[i] += extra
		}
	}
	for _, it := range g.items {
		x, cw := g.cell(it.col, it.span, cg)
		y := kit.DIP(0)
		for r := range it.row {
			y += g.rowsH[r] + rg
		}
		rh := rg * kit.DIP(it.rows-1)
		for r := it.row; r < it.row+it.rows; r++ {
			rh += g.rowsH[r]
		}
		iw := g.itemWidth(ctx, it, cw)
		j := it.p.justify
		if j == kit.AlignAuto {
			j = g.justifyItems
		}
		switch j {
		case kit.AlignEnd:
			x += cw - iw
		case kit.AlignCenter:
			x += (cw - iw) / 2
		}
		a := it.p.align
		if a == kit.AlignAuto {
			a = g.alignItems
		}
		ih := rh
		if a != kit.AlignAuto && a != kit.AlignStretch {
			ih = min(ctx.HeightFor(it.e.Widget(), iw, cw), rh)
			switch a {
			case kit.AlignEnd:
				y += rh - ih
			case kit.AlignCenter:
				y += (rh - ih) / 2
			}
		}
		if ctx.RTL {
			x = w - x - iw
		}
		ctx.Place(it.e.Widget(), kit.RectAt(kit.Pt(box.Min.X+x, box.Min.Y+y), kit.Size{W: iw, H: ih}), cw)
	}
}

// columns is CSS multi-column layout with balancing (Integration's `columns: 2 24rem;
// column-gap: 28px`): its children, which never break (break-inside: avoid), go down the first
// column, then the second, split where the taller column is the shortest; each has its 24 px
// bottom margin, truncated at the end of a column.
type columns struct {
	kit.Element
	count   int        // the most columns (2)
	minW    kit.Length // their least width (24rem)
	gap     kit.Length // column-gap (28px)
	itemGap kit.Length // the children's bottom margins (24px)

	heights []kit.DIP
	// [mac webkit] each child's column and top under WebKit's balancing (layout_webkit.go)
	wkCols []int
	wkTops []kit.DIP
}

func newColumns(children ...kit.Widget) *columns {
	c := &columns{count: 2, minW: kit.Rem(24), gap: kit.Px(28), itemGap: kit.Px(24)}
	c.Style.MinWidth = kit.Px(0)
	c.Add(children...)
	return c
}

func (c *columns) visible() []*kit.Element {
	var out []*kit.Element
	for _, e := range c.Children() {
		if !e.Style.Hidden {
			out = append(out, e)
		}
	}
	return out
}

// metrics are the number of columns and their width for content width w (CSS Multi-column
// §3.4's pseudo-algorithm).
func (c *columns) metrics(s kit.Scale, w kit.DIP) (n int, cw, gap kit.DIP) {
	mw, _ := c.minW.Resolve(s, w)
	gap, _ = c.gap.Resolve(s, w)
	n = int(math.Floor(float64((w + gap) / (mw + gap))))
	n = max(1, min(c.count, n))
	cw = max(0, (w-gap*kit.DIP(n-1))/kit.DIP(n))
	return n, cw, gap
}

// split is how many children the first column takes (with two columns), and the height.
func (c *columns) split(ctx *kit.LayoutContext, items []*kit.Element, n int, cw kit.DIP) (first int, h kit.DIP) {
	mg, _ := c.itemGap.Resolve(ctx.Scale, 0)
	c.heights = resize(c.heights, len(items))
	for i, e := range items {
		c.heights[i] = ctx.HeightFor(e.Widget(), cw, cw)
	}
	col := func(from, to int) kit.DIP {
		var s kit.DIP
		for i := from; i < to; i++ {
			if i > from {
				s += mg
			}
			s += c.heights[i]
		}
		return s
	}
	if n < 2 || len(items) < 2 {
		return len(items), col(0, len(items))
	}
	best, bestH := len(items), col(0, len(items))
	for k := 1; k < len(items); k++ {
		if hh := max(col(0, k), col(k, len(items))); hh < bestH {
			best, bestH = k, hh
		}
	}
	return best, bestH
}

// ContentWidths is kit.Layouter's.
func (c *columns) ContentWidths(ctx *kit.LayoutContext) (lo, hi kit.DIP) {
	for _, e := range c.visible() {
		a, b := ctx.IntrinsicWidths(e.Widget())
		lo, hi = max(lo, a), max(hi, b)
	}
	return lo, hi
}

// ContentHeight is kit.Layouter's: the balanced height and the last child's margin.
func (c *columns) ContentHeight(ctx *kit.LayoutContext, w kit.DIP) kit.DIP {
	items := c.visible()
	n, cw, _ := c.metrics(ctx.Scale, w)
	_, h := c.split(ctx, items, n, cw)
	mg, _ := c.itemGap.Resolve(ctx.Scale, 0)
	if ctx.Env.WebKit && n > 1 { // [mac webkit] the page is the column height high
		var wh kit.DIP
		c.wkCols, c.wkTops, wh = webkitColumns(c.heights, mg, n, c.wkCols, c.wkTops)
		return wh
	}
	return h + mg
}

// LayoutContent is kit.Layouter's.
func (c *columns) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	items := c.visible()
	w := box.Width()
	n, cw, gap := c.metrics(ctx.Scale, w)
	first, _ := c.split(ctx, items, n, cw)
	mg, _ := c.itemGap.Resolve(ctx.Scale, 0)
	if ctx.Env.WebKit && n > 1 { // [mac webkit] WebKit's balancing
		c.wkCols, c.wkTops, _ = webkitColumns(c.heights, mg, n, c.wkCols, c.wkTops)
		for i, e := range items {
			px := kit.DIP(c.wkCols[i]) * (cw + gap)
			if ctx.RTL {
				px = w - px - cw
			}
			ctx.Place(e.Widget(), kit.RectAt(kit.Pt(box.Min.X+px, box.Min.Y+c.wkTops[i]), kit.Size{W: cw, H: c.heights[i]}), cw)
		}
		return
	}
	y := kit.DIP(0)
	x := kit.DIP(0)
	for i, e := range items {
		if i == first && n > 1 {
			x, y = cw+gap, 0
		}
		px := x
		if ctx.RTL {
			px = w - x - cw
		}
		ctx.Place(e.Widget(), kit.RectAt(kit.Pt(box.Min.X+px, box.Min.Y+y), kit.Size{W: cw, H: c.heights[i]}), cw)
		y += c.heights[i] + mg
	}
}

// surface is a box with a ground, a border and corners from the theme, read when it paints
// (.s-card, .s-row's rules, the editor, the tools' cards): kit's Style colours are fixed.
type surface struct {
	kit.Element
	look
}

// look is a box's ground, border and corners, read from the theme when it paints.
type look struct {
	ground, edge views.Ink
	radius       kit.DIP
	// rule draws the top border only (.s-row + .s-row: border-block-start), in edge, while
	// ruled.
	rule, ruled bool
	// headRule draws the bottom border only (a table head's), in edge.
	headRule bool
	// card: a row drawn as its own card (Windows), its ground and border all round.
	card bool
	// cards: a box whose rows are each their own card (it draws nothing itself).
	cards bool
}

func (l *look) lookOf() *look { return l }

// looker is a box with a look (setRows draws the rules between rows).
type looker interface{ lookOf() *look }

func newSurface(st kit.Style, ground, edge views.Ink, radius kit.DIP, children ...kit.Widget) *surface {
	s := &surface{look: look{ground: ground, edge: edge, radius: radius}}
	s.Style = st
	s.Add(children...)
	return s
}

// Paint is kit.Painter's.
func (s *surface) Paint(p *kit.PaintContext) { s.look.paint(&s.Element, p) }

func (l *look) paint(e *kit.Element, p *kit.PaintContext) {
	if l.ground == nil && l.edge == nil {
		return
	}
	t := views.TokensOf(e)
	sz := p.Size()
	if l.ground != nil {
		p.FillRoundedRect(kit.RoundRect(kit.RectAt(kit.Point{}, sz), l.radius), l.ground(t))
	}
	if l.edge == nil {
		return
	}
	c := l.edge(t)
	if l.headRule {
		w := p.Scale.SnapWidth(1)
		p.FillRect(kit.R(0, sz.H-w, sz.W, sz.H), c)
		return
	}
	if l.rule && !l.card {
		if l.ruled {
			p.FillRect(kit.R(0, 0, sz.W, p.Scale.SnapWidth(1)), c)
		}
		return
	}
	if t.IsForced() {
		c = t.Ink
	}
	p.Border(kit.RoundRect(kit.RectAt(kit.Point{}, sz), l.radius), 1, c)
}

// The inks the pages use beyond views'.
var (
	inkLine       views.Ink = func(t *tokens.Theme) kit.Color { return t.Line }
	inkLineSoft   views.Ink = func(t *tokens.Theme) kit.Color { return t.LineSoft }
	inkLineStrong views.Ink = func(t *tokens.Theme) kit.Color { return t.LineStrong }
	inkSurface    views.Ink = func(t *tokens.Theme) kit.Color { return t.Surface }
	inkSurface2   views.Ink = func(t *tokens.Theme) kit.Color { return t.Surface2 }
	inkAccentSoft views.Ink = func(t *tokens.Theme) kit.Color { return t.AccentSoft }
	inkAccentLine views.Ink = func(t *tokens.Theme) kit.Color { return t.AccentLine }
	inkAccentFill views.Ink = func(t *tokens.Theme) kit.Color { return t.Accent }
	inkWarnSoft   views.Ink = func(t *tokens.Theme) kit.Color { return t.WarnSoft }
	inkWarnLine   views.Ink = func(t *tokens.Theme) kit.Color { return t.WarnLine }
	inkDangerSoft views.Ink = func(t *tokens.Theme) kit.Color { return t.DangerSoft }
	inkRaised     views.Ink = func(t *tokens.Theme) kit.Color { return t.Raised }
)
