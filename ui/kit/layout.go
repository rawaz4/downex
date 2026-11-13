package kit

import "math"

// LayoutContext is what a layout pass knows: the window's scale and environment, its
// text measurer and its direction. The window makes one per pass; widgets that lay out
// their content themselves (Layouter) use it to measure and place their children.
type LayoutContext struct {
	Scale Scale
	Env   Environment
	Text  TextMeasurer
	// RTL: the window's layout is mirrored (Window's Direction is DirRTL).
	RTL bool

	win  *Window
	pass uint32
	// full: the whole window is painted again after this pass, so moves need no
	// invalidation of their own.
	full bool
	// quiet: a measuring layout (for a baseline) whose moves are not painted.
	quiet int
	// containers are the size containers being measured or laid out now, with the content
	// width each has there: their descendants' breakpoints test it.
	containers []containerWidth
	// flexMem is the flex containers' items, kept from pass to pass (the window keeps its
	// context), so that a pass allocates nothing once it has grown.
	flexMem flexScratch
}

// flexScratch is the items of the flex containers being laid out, as a stack: a
// container's items sit above its ancestors' while its children are measured and placed,
// and go when it returns. Items live in fixed-size blocks, so an item's address stays put
// while the stack grows under nested containers.
type flexScratch struct {
	blocks [][]flexItem
	n      int         // items in use
	ptrs   []*flexItem // the containers' item lists, stacked
}

const flexBlock = 64

// mark is where the items of the container starting now begin.
func (f *flexScratch) mark() (items, ptrs int) { return f.n, len(f.ptrs) }

// item is a zeroed item on top of the stack.
func (f *flexScratch) item() *flexItem {
	b, i := f.n/flexBlock, f.n%flexBlock
	if b == len(f.blocks) {
		f.blocks = append(f.blocks, make([]flexItem, flexBlock))
	}
	f.n++
	it := &f.blocks[b][i]
	*it = flexItem{}
	return it
}

// release drops the items above a mark, and their references to elements and styles, which
// may leave the window.
func (f *flexScratch) release(items, ptrs int) {
	for k := items; k < f.n; k++ {
		f.blocks[k/flexBlock][k%flexBlock] = flexItem{}
	}
	f.n = items
	clear(f.ptrs[ptrs:])
	f.ptrs = f.ptrs[:ptrs]
}

type containerWidth struct {
	e *Element
	w DIP
}

func (ctx *LayoutContext) pushContainer(e *Element, w DIP) {
	ctx.containers = append(ctx.containers, containerWidth{e, max(w, 0)})
}

func (ctx *LayoutContext) popContainer() { ctx.containers = ctx.containers[:len(ctx.containers)-1] }

// Layouter lays out a widget's content itself instead of the block and flex rules: a
// label's text, a list's rows. Sizes are of the content box (inside the padding and
// border, which the kit adds).
type Layouter interface {
	// ContentWidths are the content's min-content and max-content widths.
	ContentWidths(ctx *LayoutContext) (min, max DIP)
	// ContentHeight is the content's height at width w, for an auto height.
	ContentHeight(ctx *LayoutContext, w DIP) DIP
	// LayoutContent lays out the content in box (the content box, in the element's own
	// coordinates): it places the children with ctx.Place and keeps what it draws.
	LayoutContent(ctx *LayoutContext, box Rect)
}

// Baseliner gives the first baseline of a Layouter's content after LayoutContent, from
// the content box's top; false when it has none.
type Baseliner interface {
	ContentBaseline() (DIP, bool)
}

var inf = DIP(math.Inf(1))

func clampDIP(v, lo, hi DIP) DIP { return max(lo, min(v, hi)) }

// style is e's style in effect for this pass: its Style with the breakpoints that match.
func (ctx *LayoutContext) style(e *Element) *Style {
	if len(e.Style.Breakpoints) == 0 {
		if e.used != &e.Style {
			e.used = &e.Style
		}
		e.stylePass = ctx.pass
		return e.used
	}
	qw := ctx.queryWidth(e)
	if e.used != nil && e.stylePass == ctx.pass && e.queried == qw {
		return e.used
	}
	if e.stylePass == ctx.pass {
		// The container is measured at another width in this pass: what e measured
		// under the other style does not hold.
		e.cache = measureCache{}
	}
	e.stylePass = ctx.pass
	e.styled = e.Style
	for _, b := range e.Style.Breakpoints {
		if b.Apply != nil && b.When.Matches(qw, ctx.Scale) {
			b.Apply(&e.styled)
		}
	}
	e.queried = qw
	e.used = &e.styled
	return e.used
}

// queryWidth is the width e's breakpoints test: the content width of its nearest
// container (the width it is being measured or laid out at, or its last one), else the
// window's width.
func (ctx *LayoutContext) queryWidth(e *Element) DIP {
	for p := e.parent; p != nil; p = p.parent {
		if !p.Style.Container {
			continue
		}
		for i := len(ctx.containers) - 1; i >= 0; i-- {
			if ctx.containers[i].e == p {
				return ctx.containers[i].w
			}
		}
		return max(p.bounds.Width()-p.insets.Left-p.insets.Right, 0)
	}
	return ctx.win.size.W
}

// boxInsets are e's padding and border, and its border alone, with percentages of cbw.
func (ctx *LayoutContext) boxInsets(st *Style, cbw DIP) (pb, border Insets) {
	border = st.Border.resolve(ctx.Scale, cbw, ctx.RTL)
	pad := st.Padding.resolve(ctx.Scale, cbw, ctx.RTL)
	pb = Insets{pad.Top + border.Top, pad.Right + border.Right, pad.Bottom + border.Bottom, pad.Left + border.Left}
	return pb, border
}

func horizontal(in Insets) DIP { return in.Left + in.Right }
func vertical(in Insets) DIP   { return in.Top + in.Bottom }

// widthLimits are e's min-width and max-width in DIPs (0 and +Inf when not set, auto
// included: callers apply the automatic minimum themselves), floored at its padding and
// border.
func (ctx *LayoutContext) widthLimits(st *Style, cbw, pbw DIP) (lo, hi DIP) {
	lo = st.MinWidth.or(ctx.Scale, cbw, 0)
	hi = st.MaxWidth.or(ctx.Scale, cbw, inf)
	lo = max(lo, pbw)
	return lo, max(hi, lo)
}

func (ctx *LayoutContext) heightLimits(st *Style, cbh, pbh DIP) (lo, hi DIP) {
	lo = st.MinHeight.or(ctx.Scale, cbh, 0)
	hi = st.MaxHeight.or(ctx.Scale, cbh, inf)
	lo = max(lo, pbh)
	return lo, max(hi, lo)
}

// intrinsicWidths are e's min-content and max-content border-box widths.
func (ctx *LayoutContext) intrinsicWidths(e *Element) (lo, hi DIP) {
	if e.cache.widths {
		return e.cache.minW, e.cache.maxW
	}
	ctx.win.stats.Measures++
	st := ctx.style(e)
	pb, _ := ctx.boxInsets(st, -1)
	pbw := horizontal(pb)
	if w, ok := st.Width.Resolve(ctx.Scale, -1); ok {
		lo, hi = w, w
	} else {
		cmin, cmax := ctx.contentWidths(e, st)
		lo, hi = cmin+pbw, cmax+pbw
	}
	mn, mx := ctx.widthLimits(st, -1, pbw)
	lo, hi = clampDIP(lo, mn, mx), clampDIP(hi, mn, mx)
	e.cache.minW, e.cache.maxW, e.cache.widths = lo, hi, true
	return lo, hi
}

// contentMinWidth is e's min-content border-box width as if its width were auto, clamped
// by its max-width: the content size suggestion of a row's flex item (CSS Flexbox §4.5).
func (ctx *LayoutContext) contentMinWidth(e *Element) DIP {
	if e.cache.cmin {
		return e.cache.cminW
	}
	st := ctx.style(e)
	pb, _ := ctx.boxInsets(st, -1)
	cmin, _ := ctx.contentWidths(e, st)
	v := cmin + horizontal(pb)
	if mx, ok := st.MaxWidth.Resolve(ctx.Scale, -1); ok {
		v = min(v, max(mx, horizontal(pb)))
	}
	e.cache.cminW, e.cache.cmin = v, true
	return v
}

// contentHeightAt is e's border-box height at border-box width w as if its height were
// auto, clamped by its max-height: a column's flex item's content size suggestion.
func (ctx *LayoutContext) contentHeightAt(e *Element, w, cbw DIP) DIP {
	if e.cache.cheight && e.cache.cforW == w {
		return e.cache.ch
	}
	st := ctx.style(e)
	pb, _ := ctx.boxInsets(st, cbw)
	v := ctx.contentHeight(e, st, max(w-horizontal(pb), 0)) + vertical(pb)
	if mx, ok := st.MaxHeight.Resolve(ctx.Scale, -1); ok {
		v = min(v, max(mx, vertical(pb)))
	}
	e.cache.cforW, e.cache.ch, e.cache.cheight = w, v, true
	return v
}

// contentWidths are the min-content and max-content widths of e's content box.
func (ctx *LayoutContext) contentWidths(e *Element, st *Style) (lo, hi DIP) {
	if l, ok := e.self.(Layouter); ok {
		return l.ContentWidths(ctx)
	}
	if st.Container {
		return 0, 0
	}
	if st.Display == DisplayFlex && st.Axis == AxisRow {
		gap := st.ColumnGap.or(ctx.Scale, -1, 0)
		n := 0
		for _, c := range e.kids {
			if !ctx.inFlow(c) {
				continue
			}
			cmin, cmax := ctx.contribution(c)
			hi += cmax
			if st.Wrap {
				lo = max(lo, cmin)
			} else {
				lo += cmin
			}
			n++
		}
		if n > 1 {
			hi += gap * DIP(n-1)
			if !st.Wrap {
				lo += gap * DIP(n-1)
			}
		}
		return lo, hi
	}
	for _, c := range e.kids {
		if !ctx.inFlow(c) {
			continue
		}
		cmin, cmax := ctx.contribution(c)
		lo, hi = max(lo, cmin), max(hi, cmax)
	}
	return lo, hi
}

// contribution is c's outer (margin-box) min-content and max-content width contribution.
func (ctx *LayoutContext) contribution(c *Element) (lo, hi DIP) {
	st := ctx.style(c)
	lo, hi = ctx.intrinsicWidths(c)
	if st.Flex.Grow == 0 && st.Flex.Shrink() == 0 {
		if b, ok := st.Flex.Basis.Resolve(ctx.Scale, -1); ok {
			lo, hi = b, b
		}
	}
	m := st.Margin.resolve(ctx.Scale, -1, ctx.RTL)
	return lo + horizontal(m), hi + horizontal(m)
}

// inFlow reports whether c takes part in its parent's layout.
func (ctx *LayoutContext) inFlow(c *Element) bool {
	return c.flags&isParked == 0 && !ctx.style(c).Hidden
}

// heightFor is e's border-box height at border-box width w, for an auto height, inside a
// containing block cbw wide.
func (ctx *LayoutContext) heightFor(e *Element, w, cbw DIP) DIP {
	if e.cache.height && e.cache.forW == w {
		return e.cache.h
	}
	ctx.win.stats.Measures++
	st := ctx.style(e)
	pb, _ := ctx.boxInsets(st, cbw)
	pbh := vertical(pb)
	var h DIP
	if v, ok := st.Height.Resolve(ctx.Scale, -1); ok {
		h = v
	} else {
		cw := max(w-horizontal(pb), 0)
		if st.Container {
			ctx.pushContainer(e, cw)
		}
		h = ctx.contentHeight(e, st, cw) + pbh
		if st.Container {
			ctx.popContainer()
		}
	}
	lo, hi := ctx.heightLimits(st, -1, pbh)
	h = clampDIP(h, lo, hi)
	e.cache.forW, e.cache.h, e.cache.height = w, h, true
	return h
}

// contentHeight is the height of e's content at content width cw.
func (ctx *LayoutContext) contentHeight(e *Element, st *Style, cw DIP) DIP {
	if l, ok := e.self.(Layouter); ok {
		return l.ContentHeight(ctx, cw)
	}
	if st.Display == DisplayFlex {
		r := ctx.flex(e, st, Size{cw, -1}, false)
		return r.Height
	}
	return ctx.block(e, st, Size{cw, -1}, false).H
}

// Place lays out child c in the rectangle r of its parent's coordinates: it sets c's
// border box and lays out c's content when its size changed or its content did. A
// Layouter calls it for each of its children; cbw is the containing block's width, which
// percentages resolve against (the parent's content width).
func (ctx *LayoutContext) Place(c Widget, r Rect, cbw DIP) {
	ctx.place(c.Base(), r, cbw)
}

func (ctx *LayoutContext) place(e *Element, r Rect, cbw DIP) {
	old := e.bounds
	resized := old.Size() != r.Size()
	moved := old.Min != r.Min
	paint := (resized || moved) && !ctx.full && ctx.quiet == 0
	if paint && e.flags&everLaidOut != 0 {
		ctx.win.invalidateElement(e)
	}
	e.bounds = r
	switch {
	case resized || e.flags&(needsLayout|everLaidOut) != everLaidOut:
		ctx.layoutContent(e, cbw)
	case e.flags&childNeedsLayout != 0:
		ctx.flushChildren(e)
	}
	if paint {
		ctx.win.invalidateElement(e)
	}
}

// Move puts c at p in its parent's coordinates without laying it out again; for a
// Layouter that moves children whose size did not change (a list's rows when it scrolls).
func (ctx *LayoutContext) Move(c Widget, p Point) {
	e := c.Base()
	if e.bounds.Min == p {
		return
	}
	paint := !ctx.full && ctx.quiet == 0
	if paint {
		ctx.win.invalidateElement(e)
	}
	e.bounds = RectAt(p, e.bounds.Size())
	if paint {
		ctx.win.invalidateElement(e)
	}
}

// IntrinsicWidths are c's min-content and max-content border-box widths.
func (ctx *LayoutContext) IntrinsicWidths(c Widget) (min, max DIP) {
	return ctx.intrinsicWidths(c.Base())
}

// HeightFor is c's border-box height at border-box width w (for an auto height).
func (ctx *LayoutContext) HeightFor(c Widget, w, cbw DIP) DIP {
	return ctx.heightFor(c.Base(), w, cbw)
}

// layoutContent lays out e's content for its current border box.
func (ctx *LayoutContext) layoutContent(e *Element, cbw DIP) {
	ctx.win.stats.BoxLayouts++
	e.flags |= inProgress
	defer func() { e.flags &^= inProgress }()
	st := ctx.style(e)
	if st.Container && e.flags&everLaidOut != 0 {
		ctx.containerResized(e)
	}
	pb, border := ctx.boxInsets(st, cbw)
	e.insets, e.border = pb, border
	size := e.bounds.Size()
	box := RectAt(Point{}, size).Inset(pb)
	inner := Size{max(box.Width(), 0), max(box.Height(), 0)}
	if st.Container {
		ctx.pushContainer(e, inner.W)
		defer ctx.popContainer()
	}
	e.hasBase = false
	switch l, ok := e.self.(Layouter); {
	case ok:
		l.LayoutContent(ctx, Rect{box.Min, box.Min.Add(Point{inner.W, inner.H})})
		if b, ok := e.self.(Baseliner); ok {
			if v, ok := b.ContentBaseline(); ok {
				e.base, e.hasBase = v+pb.Top, true
			}
		}
	case st.Display == DisplayFlex:
		r := ctx.flex(e, st, inner, true)
		if r.hasBase {
			e.base, e.hasBase = r.base+pb.Top, true
		}
	default:
		r := ctx.block(e, st, inner, true)
		if r.hasBase {
			e.base, e.hasBase = r.base+pb.Top, true
		}
	}
	e.flags = e.flags&^(needsLayout|childNeedsLayout) | everLaidOut
}

// relayout runs e's layout again at its current size (a boundary whose content changed).
func (ctx *LayoutContext) relayout(e *Element) {
	cbw := DIP(-1)
	if p := e.parent; p != nil {
		cbw = max(p.bounds.Width()-horizontal(p.insets), 0)
	}
	ctx.layoutContent(e, cbw)
}

// flushChildren lays out the descendants of e that asked for it, below e's boundaries.
func (ctx *LayoutContext) flushChildren(e *Element) {
	e.flags &^= childNeedsLayout
	e.flags |= inProgress
	defer func() { e.flags &^= inProgress }()
	for _, c := range e.kids {
		switch {
		case c.flags&needsLayout != 0:
			ctx.relayout(c)
		case c.flags&childNeedsLayout != 0:
			ctx.flushChildren(c)
		}
	}
}

// containerResized lays out everything inside the container e again for its new width,
// so that the breakpoints inside it test that width.
func (ctx *LayoutContext) containerResized(e *Element) {
	var walk func(*Element)
	walk = func(p *Element) {
		for _, c := range p.kids {
			c.flags |= needsLayout
			c.cache = measureCache{}
			c.stylePass = 0
			walk(c)
		}
	}
	walk(e)
}

// baselineOf is e's first baseline from its border box's top once laid out at size, or
// false when it has none.
func (ctx *LayoutContext) baselineOf(e *Element, size Size, cbw DIP) (DIP, bool) {
	if b, ok := e.self.(baselineMeasurer); ok {
		return b.baselineAt(ctx, size.W)
	}
	if e.flags&everLaidOut == 0 || e.bounds.Size() != size || e.flags&needsLayout != 0 {
		ctx.quiet++
		ctx.place(e, RectAt(e.bounds.Min, size), cbw)
		ctx.quiet--
	}
	return e.base, e.hasBase
}

// baselineMeasurer answers its first baseline at a border-box width without a layout
// (a label: its first line's baseline does not depend on its height).
type baselineMeasurer interface {
	baselineAt(ctx *LayoutContext, w DIP) (DIP, bool)
}

// ---- block flow -------------------------------------------------------------------------

type blockResult struct {
	H       DIP
	base    DIP
	hasBase bool
}

// block lays out e's children as a block (see DisplayBlock) in a content box inner wide
// (inner.H < 0 when the height is not known), placing them when final.
func (ctx *LayoutContext) block(e *Element, st *Style, inner Size, final bool) blockResult {
	gap := st.RowGap.or(ctx.Scale, inner.H, 0)
	var res blockResult
	y := DIP(0)
	n := 0
	for _, c := range e.kids {
		if !ctx.inFlow(c) {
			continue
		}
		cst := ctx.style(c)
		if n > 0 {
			y += gap
		}
		n++
		m := cst.Margin.resolve(ctx.Scale, inner.W, ctx.RTL)
		w, x := ctx.blockWidth(c, cst, inner.W, m)
		y += m.Top
		var h DIP
		if v, ok := cst.Height.Resolve(ctx.Scale, inner.H); ok && inner.H >= 0 {
			pb, _ := ctx.boxInsets(cst, inner.W)
			lo, hi := ctx.heightLimits(cst, inner.H, vertical(pb))
			h = clampDIP(v, lo, hi)
		} else {
			h = ctx.heightFor(c, w, inner.W)
		}
		if final {
			ctx.place(c, R(x, y, x+w, y+h).Add(e.ContentRect().Min), inner.W)
			if !res.hasBase && c.hasBase {
				res.base, res.hasBase = y+c.base, true
			}
		}
		y += h + m.Bottom
	}
	res.H = y
	return res
}

// blockWidth is a block child's border-box width and its x in a content box avail wide.
func (ctx *LayoutContext) blockWidth(c *Element, st *Style, avail DIP, m Insets) (w, x DIP) {
	pb, _ := ctx.boxInsets(st, avail)
	lo, hi := ctx.widthLimits(st, avail, horizontal(pb))
	room := max(avail-horizontal(m), 0)
	_, rightAuto, _, leftAuto := st.Margin.autoSides(ctx.RTL)
	if v, ok := st.Width.Resolve(ctx.Scale, avail); ok {
		w = clampDIP(v, lo, hi)
	} else {
		switch st.AlignSelf {
		case AlignStart, AlignEnd, AlignCenter:
			cmin, cmax := ctx.intrinsicWidths(c)
			w = clampDIP(min(max(cmin, room), cmax), lo, hi)
		default:
			w = clampDIP(room, lo, hi)
		}
	}
	free := room - w
	x = m.Left
	switch {
	case leftAuto && rightAuto:
		x += max(free, 0) / 2
	case leftAuto:
		x += max(free, 0)
	case rightAuto:
	default:
		start, end := AlignStart, AlignEnd
		if ctx.RTL {
			start, end = end, start
		}
		switch st.AlignSelf {
		case end:
			x += free
		case AlignCenter:
			x += free / 2
		case start:
		default:
			if ctx.RTL {
				x += free // an over-constrained block keeps its inline-start edge
			}
		}
	}
	return w, x
}

// ---- flexbox (CSS Flexible Box Layout Module Level 1, §9) ---------------------------------

type flexItem struct {
	e      *Element
	st     *Style
	margin Insets
	autoM  [4]bool // top, right, bottom, left
	pb     Insets
	align  Align

	base, hypo, minMain, maxMain DIP
	target                       DIP
	frozen                       bool
	main, cross                  DIP
	crossLo, crossHi             DIP
	crossFixed                   bool
	asc                          DIP // baseline from the margin box's cross start
	baseItem                     bool
	pos, cpos                    DIP
}

type flexLine struct {
	items      []*flexItem
	cross, pos DIP
	asc        DIP  // the baseline-aligned items' baseline from the line's cross start
	hasBase    bool // some item is aligned by its baseline
}

type flexResult struct {
	Height  DIP // the content height (row: cross size; column: main size)
	base    DIP
	hasBase bool
}

func (it *flexItem) marginMain(row bool) (a, b DIP) {
	if row {
		return it.margin.Left, it.margin.Right
	}
	return it.margin.Top, it.margin.Bottom
}

func (it *flexItem) marginCross(row bool) (a, b DIP) {
	if row {
		return it.margin.Top, it.margin.Bottom
	}
	return it.margin.Left, it.margin.Right
}

func (it *flexItem) autoCross(row bool) (a, b bool) {
	if row {
		return it.autoM[0], it.autoM[2]
	}
	return it.autoM[3], it.autoM[1]
}

func (it *flexItem) autoMain(row bool) (a, b bool) {
	if row {
		return it.autoM[3], it.autoM[1]
	}
	return it.autoM[0], it.autoM[2]
}

func (it *flexItem) outerMain(row bool, v DIP) DIP {
	a, b := it.marginMain(row)
	return v + a + b
}

// flex lays out e's children as a flex container with a content box of size inner
// (a negative side is not known yet: the height when measuring a row, the height of a
// column), placing them when final.
func (ctx *LayoutContext) flex(e *Element, st *Style, inner Size, final bool) flexResult {
	s := ctx.Scale
	row := st.Axis == AxisRow
	mainGap := st.ColumnGap.or(s, inner.W, 0)
	crossGap := st.RowGap.or(s, inner.H, 0)
	availMain, availCross := inner.W, inner.H
	if !row {
		mainGap, crossGap = st.RowGap.or(s, inner.H, 0), st.ColumnGap.or(s, inner.W, 0)
		availMain, availCross = inner.H, inner.W
	}
	cbw := inner.W

	mem := &ctx.flexMem
	markItems, markPtrs := mem.mark()
	defer mem.release(markItems, markPtrs)
	for _, c := range e.kids {
		if !ctx.inFlow(c) {
			continue
		}
		it := mem.item()
		it.e, it.st = c, ctx.style(c)
		ist := it.st
		it.margin = ist.Margin.resolve(s, cbw, ctx.RTL)
		it.autoM[0], it.autoM[1], it.autoM[2], it.autoM[3] = ist.Margin.autoSides(ctx.RTL)
		it.pb, _ = ctx.boxInsets(ist, cbw)
		it.align = ist.AlignSelf
		if it.align == AlignAuto {
			it.align = st.AlignItems
			if it.align == AlignAuto {
				it.align = AlignStretch
			}
		}
		ctx.flexBase(it, row, availMain, availCross, cbw)
		// What flexBase measured released its own items: this one goes on top.
		mem.ptrs = append(mem.ptrs, it)
	}
	// Nested containers stack their lists above this one, and may move the stack: items
	// keeps this one's.
	items := mem.ptrs[markPtrs:len(mem.ptrs):len(mem.ptrs)]

	// Collect the items into lines (§9.3). A single line stays on the stack: nothing is
	// appended to its slice.
	var lines []*flexLine
	var one [1]*flexLine
	var single flexLine
	if !st.Wrap || !row || availMain < 0 {
		single.items = items
		one[0] = &single
		lines = one[:]
	} else {
		var wrapped []*flexLine
		var cur *flexLine
		used := DIP(0)
		for _, it := range items {
			out := it.outerMain(row, it.hypo)
			if cur == nil || (len(cur.items) > 0 && used+mainGap+out > availMain) {
				cur = &flexLine{}
				wrapped = append(wrapped, cur)
				used = out
			} else if len(cur.items) > 0 {
				used += mainGap + out
			} else {
				used = out
			}
			cur.items = append(cur.items, it)
		}
		if len(wrapped) == 0 {
			wrapped = []*flexLine{{}}
		}
		lines = wrapped
	}

	// Resolve the flexible lengths (§9.7).
	for _, ln := range lines {
		if availMain >= 0 {
			resolveFlexible(ln.items, row, availMain, mainGap)
		} else {
			for _, it := range ln.items {
				it.main = it.hypo
			}
		}
	}

	// Cross sizes (§9.4): the hypothetical cross size of each item, then the lines'.
	for _, ln := range lines {
		maxAsc, maxDesc := DIP(0), DIP(0)
		for _, it := range ln.items {
			if row {
				ctx.rowItemCross(it, availCross, cbw)
			}
			ca, cb := it.marginCross(row)
			outer := it.cross + ca + cb
			aa, ab := it.autoCross(row)
			if row && it.align == AlignBaseline && !aa && !ab {
				b, ok := ctx.baselineOf(it.e, Size{it.main, it.cross}, cbw)
				if !ok {
					b = it.cross // synthesized from the border box's bottom edge
				}
				it.asc, it.baseItem = ca+b, true
				maxAsc, maxDesc = max(maxAsc, it.asc), max(maxDesc, outer-it.asc)
				ln.hasBase = true
			}
			ln.cross = max(ln.cross, outer)
		}
		if ln.hasBase {
			ln.asc = maxAsc
			ln.cross = max(ln.cross, maxAsc+maxDesc)
		}
	}
	// A single-line container's line is as tall as the container (§9.4 step 8); a
	// multi-line one (flex-wrap, even with one line) shares out its room by align-content.
	multi := st.Wrap && row
	if !multi && availCross >= 0 {
		lines[0].cross = availCross
	}

	// The container's cross size and align-content (§9.4 step 15, §9.6 step 16).
	total := DIP(0)
	for i, ln := range lines {
		if i > 0 {
			total += crossGap
		}
		total += ln.cross
	}
	crossSize := availCross
	if crossSize < 0 {
		crossSize = total
	}
	offset, between := DIP(0), DIP(0)
	if multi {
		free := crossSize - total
		switch st.AlignContent {
		case AlignAuto, AlignStretch:
			if free > 0 {
				for _, ln := range lines {
					ln.cross += free / DIP(len(lines))
				}
			}
		case AlignEnd:
			offset = free
		case AlignCenter:
			offset = free / 2
		case AlignSpaceBetween:
			if free > 0 && len(lines) > 1 {
				between = free / DIP(len(lines)-1)
			}
		case AlignSpaceAround:
			if free > 0 {
				between = free / DIP(len(lines))
				offset = between / 2
			} else {
				offset = free / 2
			}
		case AlignSpaceEvenly:
			if free > 0 {
				between = free / DIP(len(lines)+1)
				offset = between
			} else {
				offset = free / 2
			}
		}
	}
	pos := offset
	for _, ln := range lines {
		ln.pos = pos
		pos += ln.cross + crossGap + between
	}

	res := flexResult{Height: crossSize}
	if !row {
		used := DIP(0)
		for i, it := range lines[0].items {
			if i > 0 {
				used += mainGap
			}
			used += it.outerMain(row, it.main)
		}
		res.Height = used
		if availMain >= 0 {
			res.Height = availMain
		}
	}
	if !final {
		if row && lines[0].hasBase {
			res.base, res.hasBase = lines[0].pos+lines[0].asc, true
		}
		return res
	}

	// Stretch (§9.4 step 11), then the main-axis (§9.5) and cross-axis (§9.6) alignment.
	for _, ln := range lines {
		for _, it := range ln.items {
			aa, ab := it.autoCross(row)
			if it.align == AlignStretch && !it.crossFixed && !aa && !ab {
				ca, cb := it.marginCross(row)
				it.cross = clampDIP(ln.cross-ca-cb, it.crossLo, it.crossHi)
			}
		}
		mainUsed := DIP(0)
		autos := 0
		for i, it := range ln.items {
			if i > 0 {
				mainUsed += mainGap
			}
			mainUsed += it.outerMain(row, it.main)
			a, b := it.autoMain(row)
			if a {
				autos++
			}
			if b {
				autos++
			}
		}
		free := DIP(0)
		if availMain >= 0 {
			free = availMain - mainUsed
		}
		autoShare := DIP(0)
		if free > 0 && autos > 0 {
			autoShare = free / DIP(autos)
			free = 0
		}
		start, gapExtra := justify(st.Justify, free, len(ln.items))
		p := start
		for _, it := range ln.items {
			ma, mb := it.marginMain(row)
			a, b := it.autoMain(row)
			if a {
				ma += autoShare
			}
			if b {
				mb += autoShare
			}
			p += ma
			it.pos = p
			p += it.main + mb + mainGap + gapExtra
		}
		for _, it := range ln.items {
			ca, cb := it.marginCross(row)
			aa, ab := it.autoCross(row)
			free := ln.cross - (it.cross + ca + cb)
			c := ca
			switch {
			case aa && ab:
				c += max(free, 0) / 2
			case aa:
				c += max(free, 0)
			case ab:
			case it.baseItem:
				c = ln.asc - it.asc + ca
			case it.align == AlignEnd:
				c += free
			case it.align == AlignCenter:
				c += free / 2
			}
			it.cpos = ln.pos + c
		}
	}

	// Place the items, mirrored where the main axis runs the other way.
	content := e.ContentRect().Min
	mirror := (row && ctx.RTL != st.Reverse) || (!row && st.Reverse)
	crossMirror := !row && ctx.RTL
	for _, ln := range lines {
		for _, it := range ln.items {
			var r Rect
			if row {
				x := it.pos
				if mirror {
					x = availMain - it.pos - it.main
				}
				r = R(x, it.cpos, x+it.main, it.cpos+it.cross)
			} else {
				y := it.pos
				if mirror {
					y = res.Height - it.pos - it.main
				}
				x := it.cpos
				if crossMirror {
					x = crossSize - it.cpos - it.cross
				}
				r = R(x, y, x+it.cross, y+it.main)
			}
			ctx.place(it.e, r.Add(content), cbw)
		}
	}

	// The container's first baseline (§8.5): its first line's baseline-aligned items',
	// else its first item's, synthesized from that item's border box when it has none.
	if ln := lines[0]; len(ln.items) > 0 {
		if row && ln.hasBase {
			res.base, res.hasBase = ln.pos+ln.asc, true
		} else if first := ln.items[0]; first.e.hasBase {
			res.base, res.hasBase = first.e.bounds.Min.Y-content.Y+first.e.base, true
		} else {
			res.base, res.hasBase = first.e.bounds.Max.Y-content.Y, true
		}
	}
	return res
}

// flexBase works out an item's flex base size, its hypothetical main size and its
// min and max main sizes (§9.2 steps 3 and 4, §4.5 for the automatic minimum).
func (ctx *LayoutContext) flexBase(it *flexItem, row bool, availMain, availCross, cbw DIP) {
	s := ctx.Scale
	ist := it.st
	var mainProp, minProp, maxProp Length
	var pbMain DIP
	if row {
		mainProp, minProp, maxProp = ist.Width, ist.MinWidth, ist.MaxWidth
		pbMain = horizontal(it.pb)
	} else {
		mainProp, minProp, maxProp = ist.Height, ist.MinHeight, ist.MaxHeight
		pbMain = vertical(it.pb)
		ctx.columnItemWidth(it, availCross)
	}
	content := func() DIP {
		if row {
			_, hi := ctx.intrinsicWidths(it.e)
			return hi
		}
		return ctx.heightFor(it.e, it.cross, cbw)
	}
	// flex-basis: a definite basis, else (auto) the main size property when definite, else
	// the content size (also for a percentage of an indefinite container).
	basis := ist.Flex.Basis
	base, ok := basis.Resolve(s, availMain)
	if !ok && basis.IsAuto() {
		base, ok = mainProp.Resolve(s, availMain)
	}
	if !ok {
		base = content()
	}
	base = max(base, pbMain)

	// min-width/min-height auto: the automatic minimum (§4.5), the smaller of the content
	// size suggestion (the content's min-content size, whatever the item's own size) and
	// the specified size suggestion (its own size, when definite); none when it is a
	// scroll container (OverflowHidden; an inline clip is none, as CSS's overflow: clip).
	minMain, ok := minProp.Resolve(s, availMain)
	if !ok {
		minMain = 0
		if ist.Overflow != OverflowHidden {
			var c DIP
			if row {
				c = ctx.contentMinWidth(it.e)
			} else {
				c = ctx.contentHeightAt(it.e, it.cross, cbw)
			}
			if v, ok := mainProp.Resolve(s, availMain); ok {
				c = min(c, v)
			}
			if v, ok := maxProp.Resolve(s, availMain); ok {
				c = min(c, v)
			}
			minMain = c
		}
	}
	minMain = max(minMain, pbMain)
	maxMain := maxProp.or(s, availMain, inf)
	maxMain = max(maxMain, minMain)
	it.base, it.minMain, it.maxMain = base, minMain, maxMain
	it.hypo = clampDIP(base, minMain, maxMain)
}

// columnItemWidth sets a column item's cross size: its own width, the container's width
// when it stretches, else its fit-content width.
func (ctx *LayoutContext) columnItemWidth(it *flexItem, avail DIP) {
	ist := it.st
	pbw := horizontal(it.pb)
	lo, hi := ctx.widthLimits(ist, avail, pbw)
	it.crossLo, it.crossHi = lo, hi
	room := max(avail-it.margin.Left-it.margin.Right, 0)
	if v, ok := ist.Width.Resolve(ctx.Scale, avail); ok {
		it.cross, it.crossFixed = clampDIP(v, lo, hi), true
		return
	}
	aa, ab := it.autoCross(false)
	if it.align == AlignStretch && !aa && !ab && avail >= 0 {
		it.cross = clampDIP(room, lo, hi)
		return
	}
	cmin, cmax := ctx.intrinsicWidths(it.e)
	if avail < 0 {
		it.cross = clampDIP(cmax, lo, hi)
		return
	}
	it.cross = clampDIP(min(max(cmin, room), cmax), lo, hi)
}

// rowItemCross sets a row item's hypothetical cross size (its height) for its used width.
func (ctx *LayoutContext) rowItemCross(it *flexItem, availCross, cbw DIP) {
	ist := it.st
	pbh := vertical(it.pb)
	lo, hi := ctx.heightLimits(ist, availCross, pbh)
	it.crossLo, it.crossHi = lo, hi
	if v, ok := ist.Height.Resolve(ctx.Scale, availCross); ok {
		it.cross, it.crossFixed = clampDIP(v, lo, hi), true
		return
	}
	it.cross = clampDIP(ctx.heightFor(it.e, it.main, cbw), lo, hi)
}

// resolveFlexible is §9.7: the items' main sizes in a line avail long.
func resolveFlexible(items []*flexItem, row bool, avail, gap DIP) {
	if len(items) == 0 {
		return
	}
	gaps := gap * DIP(len(items)-1)
	sumHypo := gaps
	for _, it := range items {
		sumHypo += it.outerMain(row, it.hypo)
	}
	growing := sumHypo < avail
	for _, it := range items {
		f := it.st.Flex.Shrink()
		if growing {
			f = it.st.Flex.Grow
		}
		it.frozen = f == 0 || (growing && it.base > it.hypo) || (!growing && it.base < it.hypo)
		if it.frozen {
			it.target = it.hypo
		} else {
			it.target = it.base
		}
	}
	freeSpace := func() DIP {
		used := gaps
		for _, it := range items {
			if it.frozen {
				used += it.outerMain(row, it.target)
			} else {
				used += it.outerMain(row, it.base)
			}
		}
		return avail - used
	}
	initial := freeSpace()
	for range len(items) + 1 {
		var sumFactor, sumScaled float32
		open := 0
		for _, it := range items {
			if it.frozen {
				continue
			}
			open++
			if growing {
				sumFactor += it.st.Flex.Grow
			} else {
				sumFactor += it.st.Flex.Shrink()
				sumScaled += it.st.Flex.Shrink() * float32(max(it.base-pbMainOf(it, row), 0))
			}
		}
		if open == 0 {
			break
		}
		free := freeSpace()
		if sumFactor < 1 {
			if v := initial * sumFactor; abs32(v) < abs32(free) {
				free = v
			}
		}
		for _, it := range items {
			if it.frozen {
				continue
			}
			switch {
			case growing && sumFactor > 0:
				it.target = it.base + free*it.st.Flex.Grow/sumFactor
			case !growing && sumScaled > 0:
				scaled := it.st.Flex.Shrink() * float32(max(it.base-pbMainOf(it, row), 0))
				it.target = it.base - abs32(free)*scaled/sumScaled
			default:
				it.target = it.base
			}
		}
		var total DIP
		viol := make([]DIP, len(items))
		for i, it := range items {
			if it.frozen {
				continue
			}
			c := clampDIP(it.target, it.minMain, it.maxMain)
			viol[i] = c - it.target
			total += viol[i]
			it.target = c
		}
		for i, it := range items {
			if it.frozen {
				continue
			}
			switch {
			case total == 0:
				it.frozen = true
			case total > 0 && viol[i] > 0:
				it.frozen = true
			case total < 0 && viol[i] < 0:
				it.frozen = true
			}
		}
		if total == 0 {
			break
		}
	}
	for _, it := range items {
		it.main = it.target
	}
}

func pbMainOf(it *flexItem, row bool) DIP {
	if row {
		return horizontal(it.pb)
	}
	return vertical(it.pb)
}

func abs32(v DIP) DIP {
	if v < 0 {
		return -v
	}
	return v
}

// justify is where justify-content puts a line's first item and the extra space between
// items, for free space left on the line (negative when the items overflow it).
func justify(j Justify, free DIP, n int) (start, between DIP) {
	switch j {
	case JustifyEnd:
		return free, 0
	case JustifyCenter:
		return free / 2, 0
	case JustifySpaceBetween:
		if free > 0 && n > 1 {
			return 0, free / DIP(n-1)
		}
		return 0, 0
	case JustifySpaceAround:
		if free > 0 && n > 0 {
			return free / DIP(n) / 2, free / DIP(n)
		}
		return free / 2, 0
	case JustifySpaceEvenly:
		if free > 0 {
			return free / DIP(n+1), free / DIP(n+1)
		}
		return free / 2, 0
	}
	return 0, 0
}
