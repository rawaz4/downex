package downloads

import (
	"math"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/display"
	"downex/ui/core/text"
	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/views"
)

// Grid is DownloadTable.svelte's .wrap: the table, the rows that just left fading out where
// they were, and the empty list's message over the rows' area. It is the views.DownloadGrid
// slot.
type Grid struct {
	kit.Element
	dl     *Downloads
	table  *Table
	ghosts *ghostLayer
	empty  *emptyState
	hbar   *hscroll
}

var _ views.DownloadGrid = (*Grid)(nil)

func newGrid(dl *Downloads) *Grid {
	g := &Grid{dl: dl}
	g.Style = kit.Style{MinWidth: kit.Px(0), MinHeight: kit.Px(0)}
	g.table = newTable(dl)
	g.ghosts = newGhostLayer(g)
	g.empty = newEmptyState(dl)
	g.hbar = newHScroll(g.table)
	g.Add(g.table, g.ghosts, g.empty, g.hbar)
	return g
}

// Table is the grid's table.
func (g *Grid) Table() *Table { return g.table }

// FocusList is views.DownloadGrid's: the grid takes the focus at its cursor row.
func (g *Grid) FocusList() { g.table.FocusList() }

// FocusTarget is views.DownloadGrid's: the element the focus falls back to.
func (g *Grid) FocusTarget() kit.Widget { return g.table }

// ContentWidths is kit.Layouter's: the grid takes the room it is given.
func (g *Grid) ContentWidths(*kit.LayoutContext) (min, max kit.DIP) { return 0, 0 }

// ContentHeight is kit.Layouter's.
func (g *Grid) ContentHeight(*kit.LayoutContext, kit.DIP) kit.DIP { return 0 }

// LayoutContent is kit.Layouter's: the table fills the box; the ghosts lie over its rows'
// area and the empty message under the header (inset: var(--header-h) 0 0 0).
func (g *Grid) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	t := g.table
	rem, css := t.remFor(ctx.Scale), float64(ctx.Scale.RemBase())
	head := kit.DIP(headerHeight(rem))
	rowH := kit.DIP(display.RowHeightFor(rem, t.compact))
	sm := kit.ScrollMetricsFor(ctx.Env.ScrollBars) // [mac scroll] FluentScroll on Windows
	thick := ctx.Scale.Snap(sm.Thickness)
	tall := head + kit.DIP(t.model.Len())*rowH
	vbar := tall > box.Height()
	w := box.Width()
	if vbar {
		w -= thick
	}
	need := func(w kit.DIP) kit.DIP {
		lay := t.cols.Fit(float64(w), rem, css)
		return kit.DIP(math.Max(lay.MinInline, rowPad+lay.Content))
	}
	hbar := need(w) > w+0.5
	if hbar && !vbar && tall > box.Height()-thick {
		vbar = true
		w -= thick
		hbar = need(w) > w+0.5
	}
	t.viewW = w
	list := box
	if hbar {
		list.Max.Y -= thick
		t.scrollX = min(max(t.scrollX, 0), need(w)-w)
	} else {
		t.scrollX = 0
	}
	ctx.Place(t, list, box.Width())
	g.hbar.SetHidden(!hbar)
	if hbar {
		x0, x1 := box.Min.X, box.Max.X
		if vbar {
			if ctx.RTL {
				x0 += thick
			} else {
				x1 -= thick
			}
		}
		hy := list.Max.Y
		if sm.Overlay { // [mac scroll] an overlay bar lies over the rows' bottom edge
			hy -= ctx.Scale.Snap(sm.BarWidth())
		}
		ctx.Place(g.hbar, kit.R(x0, hy, x1, box.Max.Y), box.Width())
	}
	rows := kit.R(box.Min.X, box.Min.Y+head, box.Max.X, list.Max.Y)
	ctx.Place(g.ghosts, rows, box.Width())
	if !g.empty.Style.Hidden {
		ctx.Place(g.empty, rows, box.Width())
	}
}

func (g *Grid) relayoutGhosts() {
	if len(g.ghosts.rows) > 0 {
		g.ghosts.RelayoutContent()
	}
}

// ---- ghosts ----------------------------------------------------------------------------------

// ghostLayer holds the rows that were just removed while they fade out (220 ms ease-in, 3 px
// down, gone after 240 ms), each where it was; it takes no input and is hidden from assistive
// technology.
type ghostLayer struct {
	kit.Element
	g    *Grid
	rows []*ghost
	anim *ghostAnim
}

type ghost struct {
	r     *row
	index int
	start time.Time
}

const (
	leaveFor  = 220 * time.Millisecond
	ghostLife = 240 * time.Millisecond
)

func newGhostLayer(g *Grid) *ghostLayer {
	l := &ghostLayer{g: g}
	l.PassThrough = true
	l.Semantics.Hidden = true
	l.Style = kit.Style{Overflow: kit.OverflowHidden}
	return l
}

// add shows the rows of removed downloads at their indexes.
func (l *ghostLayer) add(ds []*downexv1.Download, indexes []int) {
	w := l.Window()
	if w == nil || !w.MotionOK() {
		return
	}
	now := w.Now()
	for i, d := range ds {
		r := newRow(l.g.table, true)
		r.key = kit.ListKey(d.GetId())
		r.index = indexes[i]
		r.show(d, false)
		l.rows = append(l.rows, &ghost{r: r, index: indexes[i], start: now})
		l.Add(r)
	}
	if l.anim == nil {
		l.anim = &ghostAnim{l: l}
		w.Animate(l.anim)
	}
	l.RelayoutContent()
}

func (l *ghostLayer) ghostOf(r *row) (alpha, dy, tint float32, ok bool) {
	for _, gh := range l.rows {
		if gh.r == r {
			w := l.Window()
			if w == nil {
				return 0, 0, 0, true
			}
			share := min(float32(w.Now().Sub(gh.start))/float32(leaveFor), 1)
			e := kit.EaseIn(share)
			return 1 - e, 3 * e, 0, true
		}
	}
	return 1, 0, 0, false
}

// ContentWidths is kit.Layouter's.
func (l *ghostLayer) ContentWidths(*kit.LayoutContext) (min, max kit.DIP) { return 0, 0 }

// ContentHeight is kit.Layouter's.
func (l *ghostLayer) ContentHeight(*kit.LayoutContext, kit.DIP) kit.DIP { return 0 }

// LayoutContent is kit.Layouter's: each ghost where its row was, as the list scrolls.
func (l *ghostLayer) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	t := l.g.table
	rh := t.RowHeightNow()
	w := box.Width()
	if t.Scrollbar() != nil && !t.Scrollbar().Style.Hidden {
		w -= ctx.Scale.Snap(t.Scrollbar().Metrics.Thickness)
	}
	x := box.Min.X
	if ctx.RTL && w < box.Width() {
		x = box.Max.X - w
	}
	for _, gh := range l.rows {
		y := box.Min.Y + kit.DIP(gh.index)*rh - t.ScrollOffset()
		ctx.Place(gh.r, kit.R(x, y, x+w, y+rh), w)
	}
}

type ghostAnim struct {
	l    *ghostLayer
	dead bool
}

func (a *ghostAnim) Step(now time.Time) bool {
	l := a.l
	if a.dead {
		return false
	}
	kept := l.rows[:0]
	for _, gh := range l.rows {
		if now.Sub(gh.start) >= ghostLife {
			l.Remove(gh.r)
			continue
		}
		gh.r.Invalidate()
		kept = append(kept, gh)
	}
	l.rows = kept
	if len(kept) == 0 {
		l.anim = nil
		return false
	}
	return true
}

func (a *ghostAnim) Finish() {
	l := a.l
	for _, gh := range l.rows {
		l.Remove(gh.r)
	}
	l.rows = nil
	l.anim = nil
	a.dead = true
}

// ---- the empty list ------------------------------------------------------------------------------

// EmptyKind is what the empty list says (ListEmpty.svelte's EmptyKind).
type EmptyKind uint8

// The empty kinds.
const (
	EmptyLoading EmptyKind = iota
	EmptyUnreachable
	EmptyNone
	EmptyFiltered
)

// emptyState is ListEmpty.svelte over the rows' area while the list has no rows: while the
// first read runs, while downexd cannot be reached, with no downloads at all (how to add one,
// and where browser downloads come from), or with filters that match nothing.
type emptyState struct {
	kit.Element
	dl    *Downloads
	kind  EmptyKind
	title string
	stale bool
	box   *kit.Element
	built bool
	add   *buttonRef
}

type buttonRef struct{ set func(disabled bool) }

func newEmptyState(dl *Downloads) *emptyState {
	e := &emptyState{dl: dl, kind: 255}
	e.PassThrough = true
	e.Style = kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, Justify: kit.JustifyCenter, Hidden: true, Overflow: kit.OverflowHidden}
	return e
}

// update shows the message for the store now (nothing while there are rows).
func (e *emptyState) update(total int, ready bool, loadError string, filtered bool, title string, stale bool) {
	if total > 0 {
		e.SetHidden(true)
		return
	}
	kind := EmptyNone
	switch {
	case !ready && loadError != "":
		kind = EmptyUnreachable
	case !ready:
		kind = EmptyLoading
	case filtered:
		kind = EmptyFiltered
	}
	if kind != e.kind || title != e.title || !e.built {
		e.kind, e.title = kind, title
		e.build()
	}
	if stale != e.stale || e.add == nil {
		e.stale = stale
		if e.add != nil {
			e.add.set(stale)
		}
	}
	e.SetHidden(false)
}

func (e *emptyState) build() {
	e.RemoveAll()
	e.built = true
	e.add = nil
	e.box = buildEmpty(e.dl, e.kind, e.title, e.stale, func(r *buttonRef) { e.add = r })
	e.Add(e.box)
	e.Relayout()
}

// filterTitle is what the filters that match nothing are: the search, else the one filter in
// use (DownloadTable's filterTitle).
func filterTitle(search string, category categories.Filter, ix *categories.Index, status categories.Status, queue int64) string {
	query := text.TrimJS(search)
	if query != "" {
		return "No downloads match “" + text.Isolate(query) + "”"
	}
	var names []string
	switch {
	case category == categories.OtherFilter:
		names = append(names, categories.OtherLabel)
	case category != categories.All:
		name := "this category"
		if ix != nil {
			for _, en := range ix.Entries() {
				if en.Key == category {
					name = text.Isolate(en.Name)
					break
				}
			}
		}
		names = append(names, name)
	}
	if status != categories.AnyStatus {
		names = append(names, categories.StatusLabel(status))
	}
	if queue != 0 {
		names = append(names, "this queue")
	}
	if len(names) == 1 {
		return "Nothing in " + names[0]
	}
	return "No downloads match the current filters"
}

var _ = tokens.Light
