package queues

import (
	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/views"
)

// The layout of QueueDialog.svelte: the shell (the list column and the editor, one row that
// fills the dialog's height, so that the list runs from the header to the footer and only the
// editor scrolls), and the editor's grid.

// shell is .queues-shell under a 1 px --line: grid-template-columns 14.5rem minmax(0, 1fr); a
// narrow dialog (40rem or less) puts the list in a row above the editor.
type shell struct {
	kit.Element
	d      *Dialog
	list   *queueList
	pane   *widgets.ScrollPane
	ed     *editor
	narrow bool
}

const (
	listWidth = 14.5 // rem
	wideAt    = 64.5 // rem: @container dialog (min-width: 64.5rem)
	narrowAt  = 40   // rem: @container dialog (max-width: 40rem)
)

func newShell(d *Dialog, list *queueList, ed *editor) *shell {
	s := &shell{d: d, list: list, ed: ed}
	s.Style = kit.Style{Border: kit.Edges{Top: kit.Px(1)}, MinHeight: kit.Px(0)}
	s.pane = widgets.NewScrollPane(ed)
	ed.pane = s.pane
	s.Add(list, s.pane)
	return s
}

// Attached is kit.Attacher's: the dialog follows the stores while it is shown.
func (s *shell) Attached() { s.d.attach() }

// Detached is kit.Detacher's.
func (s *shell) Detached() { s.d.detach() }

// ContentWidths is kit.Layouter's: the dialog's width is its own (72rem).
func (s *shell) ContentWidths(*kit.LayoutContext) (lo, hi kit.DIP) { return 0, 0 }

// modes sets the dialog's container queries for a shell (= the dialog's content box) w wide.
func (s *shell) modes(ctx *kit.LayoutContext, w kit.DIP) {
	s.narrow = w <= ctx.Scale.Rem(narrowAt)
	s.list.setNarrow(s.narrow)
	s.ed.wide = w >= ctx.Scale.Rem(wideAt)
}

// natural is the shell's content height at width w as its content asks for it: the list's
// and the editor's, beside each other (or the list's row above the editor).
func (s *shell) natural(ctx *kit.LayoutContext, w kit.DIP) kit.DIP {
	s.modes(ctx, w)
	if s.narrow {
		return ctx.HeightFor(s.list, w, w) + s.ed.natural(ctx, w)
	}
	lw := ctx.Scale.Rem(listWidth)
	return max(ctx.HeightFor(s.list, lw, w), s.ed.natural(ctx, max(w-lw, 0)))
}

// ContentHeight is kit.Layouter's: the content's height within the room the dialog has, so
// that the dialog's body (one row, minmax(0, 1fr), overflow: hidden) never scrolls: the
// dialog is at least 48rem high (at most the room), and grows with the editor up to the room.
func (s *shell) ContentHeight(ctx *kit.LayoutContext, w kit.DIP) kit.DIP {
	h := s.natural(ctx, w)
	lo, hi, ok := s.bodyRange(ctx, w)
	if !ok {
		return h
	}
	border := kit.DIP(1) // the shell's border-block-start
	return min(max(h+border, lo), hi) - border
}

// bodyRange is the least and the most height the dialog leaves its body (the shell's border
// box): Dialog.svelte's min-block-size min(48rem, room) and max-block-size room, less the
// header, the footer and the dialog's 1 px borders.
func (s *shell) bodyRange(ctx *kit.LayoutContext, w kit.DIP) (lo, hi kit.DIP, ok bool) {
	win := s.Window()
	if win == nil {
		return 0, 0, false
	}
	card := s.d.Card()
	fixed := kit.DIP(2) // the card's borders
	for _, c := range card.Children() {
		if c.Contains(&s.Element) || c.Style.Hidden {
			continue
		}
		fixed += ctx.HeightFor(c, w, w)
	}
	size := win.Size()
	top := s.d.app.ModalTop()
	gap := kit.DIP(16)
	if size.H <= 640 {
		gap = 8
	}
	room := max(size.H-top-2*gap, 0)
	least := min(ctx.Scale.Rem(48), room)
	return max(least-fixed, 0), max(room-fixed, 0), true
}

// LayoutContent is kit.Layouter's.
func (s *shell) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	w := box.Width()
	s.modes(ctx, w)
	if s.narrow {
		lh := ctx.HeightFor(s.list, w, w)
		ctx.Place(s.list, kit.R(box.Min.X, box.Min.Y, box.Max.X, box.Min.Y+lh), w)
		s.placeEditor(ctx, kit.R(box.Min.X, box.Min.Y+lh, box.Max.X, box.Max.Y))
		return
	}
	lw := min(ctx.Scale.Rem(listWidth), w)
	lx, ex := box.Min.X, box.Min.X+lw
	if ctx.RTL {
		lx, ex = box.Max.X-lw, box.Min.X
	}
	ctx.Place(s.list, kit.R(lx, box.Min.Y, lx+lw, box.Max.Y), w)
	s.placeEditor(ctx, kit.R(ex, box.Min.Y, ex+w-lw, box.Max.Y))
}

func (s *shell) placeEditor(ctx *kit.LayoutContext, r kit.Rect) {
	if r.Width() != s.ed.paneW || r.Height() != s.ed.paneH {
		s.ed.paneW, s.ed.paneH = r.Width(), r.Height()
		s.ed.Relayout() // its 1fr row follows the pane's height
	}
	ctx.Place(s.pane, r, r.Width())
}

// Paint is kit.Painter's: the 1 px --line above the shell.
func (s *shell) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&s.Element)
	p.FillRect(kit.R(0, 0, p.Size().W, p.Scale.SnapWidth(1)), t.Line)
}

// editor is .editor, the one part that scrolls (the order list scrolls with it): a grid with
// 20 px gaps, 18/24 px padding above and below, 24/18 px at the sides, and a scroll bar gutter
// that is always there (scrollbar-gutter: stable). A wide dialog (64.5rem or more) puts the
// settings and the order on the start side and the schedule beside them (grid-template-rows
// auto auto minmax(0, 1fr) auto; without a schedule the order takes the other column from the
// top). It is the queue-editor container: a narrow editor (30rem or less) puts the settings in
// one column.
type editor struct {
	kit.Element
	d                                        *Dialog
	pane                                     *widgets.ScrollPane
	head, settings, schedule, order, confirm kit.Widget

	wide         bool
	paneW, paneH kit.DIP
	// cw is the content width the editor was last measured or laid out at (the queue-editor
	// container's width).
	cw kit.DIP
}

const (
	edPadTop, edPadBottom, edPadStart, edPadEnd = 18, 24, 24, 18
	edGap                                       = 20
	edColGap                                    = 24
)

func newEditor(d *Dialog) *editor {
	e := &editor{d: d}
	return e
}

func (e *editor) set(head, settings, schedule, order, confirm kit.Widget) {
	e.head, e.settings, e.schedule, e.order, e.confirm = head, settings, schedule, order, confirm
	e.SetChildren(head, settings, schedule, order, confirm)
}

// inner is the content width at the width the pane gives (it reserves the gutter itself only
// while it overflows).
func (e *editor) inner(w kit.DIP) kit.DIP {
	t := e.pane.Scrollbar().Metrics.Thickness
	gutter := max(e.paneW-w, 0)
	extra := max(t-gutter, 0)
	if e.paneW == 0 {
		extra = t
	}
	return max(w-edPadStart-edPadEnd-extra, 0)
}

func shown(w kit.Widget) bool { return w != nil && !w.Base().Style.Hidden }

// scheduled reports whether the schedule shows its times (QueueDialog's :has(.schedule .times)).
func (e *editor) scheduled() bool { return e.d.form.ScheduleEnabled }

// wideRows are the wide grid's rows at content width w: their heights, and the items' heights.
type wideRows struct {
	r1, r2, r3, r4      kit.DIP
	hh, hs, hc, ho, hf  kit.DIP
	col                 kit.DIP
	orderTall, hasOrder bool
}

func (e *editor) wideMeasure(ctx *kit.LayoutContext, w kit.DIP) wideRows {
	var m wideRows
	m.col = max((w-edColGap)/2, 0)
	m.hh = ctx.HeightFor(e.head, w, w)
	m.hs = ctx.HeightFor(e.settings, m.col, w)
	m.hc = ctx.HeightFor(e.schedule, m.col, w)
	m.hasOrder = shown(e.order)
	if m.hasOrder {
		m.ho = ctx.HeightFor(e.order, m.col, w)
	}
	if shown(e.confirm) {
		m.hf = ctx.HeightFor(e.confirm, w, w)
	}
	m.orderTall = !e.scheduled() // the order takes the other column from the top
	m.r1, m.r2, m.r4 = m.hh, m.hs, m.hf
	if m.orderTall {
		m.r3 = m.hc
		if m.hasOrder {
			m.r3 = max(m.r3, m.ho-m.r2-edGap)
		}
	} else {
		m.r3 = max(m.hc-m.r2-edGap, 0)
		if m.hasOrder {
			m.r3 = max(m.r3, m.ho)
		}
	}
	return m
}

// natural is the editor's height at width w (the whole pane's) as its content asks for it.
func (e *editor) natural(ctx *kit.LayoutContext, w kit.DIP) kit.DIP {
	return e.naturalIn(ctx, max(w-edPadStart-edPadEnd-e.pane.Scrollbar().Metrics.Thickness, 0))
}

// naturalIn is that height at content width in.
func (e *editor) naturalIn(ctx *kit.LayoutContext, in kit.DIP) kit.DIP {
	e.cw = in
	if e.wide {
		m := e.wideMeasure(ctx, in)
		return edPadTop + m.r1 + m.r2 + m.r3 + m.r4 + 3*edGap + edPadBottom
	}
	h := kit.DIP(edPadTop + edPadBottom)
	n := 0
	for _, c := range e.Children() {
		if c.Style.Hidden {
			continue
		}
		if n > 0 {
			h += edGap
		}
		h += ctx.HeightFor(c, in, in)
		n++
	}
	return h
}

// ContentWidths is kit.Layouter's.
func (e *editor) ContentWidths(*kit.LayoutContext) (lo, hi kit.DIP) { return 0, 0 }

// ContentHeight is kit.Layouter's: what the pane scrolls. In the wide grid the 1fr row takes
// what the pane's height leaves, and what does not fit runs out of it (Chromium's scrollable
// overflow: the items' ends, the pane's height at least).
func (e *editor) ContentHeight(ctx *kit.LayoutContext, w kit.DIP) kit.DIP {
	in := e.inner(w)
	if !e.wide || e.paneH <= 0 {
		return e.naturalIn(ctx, in)
	}
	e.cw = in
	m := e.wideMeasure(ctx, in)
	_, bottom := e.wideRowsIn(m, e.paneH)
	return max(e.paneH, bottom)
}

// wideRowsIn fixes the 1fr row for a pane of height h, and returns the rows' tops and the
// items' lowest end.
func (e *editor) wideRowsIn(m wideRows, h kit.DIP) (tops [4]kit.DIP, bottom kit.DIP) {
	r3 := max(h-edPadTop-edPadBottom-m.r1-m.r2-m.r4-3*edGap, 0)
	tops[0] = edPadTop
	tops[1] = tops[0] + m.r1 + edGap
	tops[2] = tops[1] + m.r2 + edGap
	tops[3] = tops[2] + r3 + edGap
	bottom = max(tops[0]+m.hh, tops[1]+m.hs)
	if m.orderTall {
		bottom = max(bottom, tops[2]+m.hc)
		if m.hasOrder {
			bottom = max(bottom, tops[1]+m.ho)
		}
	} else {
		bottom = max(bottom, tops[1]+m.hc)
		if m.hasOrder {
			bottom = max(bottom, tops[2]+m.ho)
		}
	}
	if m.hf > 0 {
		bottom = max(bottom, tops[3]+m.hf)
	}
	return tops, bottom
}

// LayoutContent is kit.Layouter's.
func (e *editor) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	in := e.inner(box.Width())
	e.cw = in
	x0 := box.Min.X + edPadStart
	if ctx.RTL {
		x0 = box.Max.X - edPadStart - in
	}
	place := func(c kit.Widget, x, y, w kit.DIP) {
		h := ctx.HeightFor(c, w, in)
		if ctx.RTL { // mirrored inside the content box
			x = x0 + (in - (x - x0) - w)
		}
		ctx.Place(c, kit.R(x, y, x+w, y+h), in)
	}
	if !e.wide {
		y := box.Min.Y + edPadTop
		n := 0
		for _, c := range e.Children() {
			if c.Style.Hidden {
				continue
			}
			if n > 0 {
				y += edGap
			}
			place(c, x0, y, in)
			y += c.Bounds().Height()
			n++
		}
		return
	}
	m := e.wideMeasure(ctx, in)
	h := e.paneH
	if h <= 0 {
		h = box.Height()
	}
	tops, _ := e.wideRowsIn(m, h)
	y := box.Min.Y
	c1, c2 := x0, x0+m.col+edColGap
	place(e.head, c1, y+tops[0], in)
	place(e.settings, c1, y+tops[1], m.col)
	if m.orderTall {
		place(e.schedule, c1, y+tops[2], m.col)
		if m.hasOrder {
			place(e.order, c2, y+tops[1], m.col)
		}
	} else {
		place(e.schedule, c2, y+tops[1], m.col)
		if m.hasOrder {
			place(e.order, c1, y+tops[2], m.col)
		}
	}
	if shown(e.confirm) {
		place(e.confirm, c1, y+tops[3], in)
	}
}

// card is .q-card: a block on --surface with a 1 px --line border and --r-lg corners.
type card struct{ kit.Element }

func newCard(st kit.Style, children ...kit.Widget) *card {
	c := &card{}
	c.Style = st
	c.Style.Border = kit.EdgesAll(kit.Px(1))
	c.Style.MinWidth = kit.Px(0)
	c.Add(children...)
	return c
}

// Paint is kit.Painter's.
func (c *card) Paint(p *kit.PaintContext) { paintCard(p, &c.Element) }

func paintCard(p *kit.PaintContext, e *kit.Element) {
	t := views.TokensOf(e)
	r := views.SnapBox(p, 10)
	border := t.Line
	if t.IsForced() {
		border = t.Ink
	}
	p.Canvas.FillRoundedRect(r, border)
	w := p.Scale.SnapWidth(1)
	p.Canvas.FillRoundedRect(kit.RoundRect(r.Rect.Inset(kit.UniformInsets(w)), 10-w), t.Surface)
}

// cardGrid is a grid that is also a .q-card (.q-card.fields).
type cardGrid struct{ grid }

// Paint is kit.Painter's.
func (c *cardGrid) Paint(p *kit.PaintContext) { paintCard(p, &c.Element) }

// section is .q-section: a grid with 8 px gaps (a title, then its card).
func section(name string, children ...kit.Widget) *kit.Element {
	s := box(kit.Style{Display: kit.DisplayBlock, RowGap: tokens.Sp4, MinWidth: kit.Px(0)}, children...)
	s.Semantics.Role = kit.RoleRegion
	s.Semantics.Name = name
	return s
}

// title is .q-title: 650 var(--type-lead)/1.3 var(--font-display), --ink (an h4).
func title(s string) *views.Text {
	t := views.NewParagraph(s, fontTitle, views.InkInk)
	t.Semantics.Role = kit.RoleHeading
	t.Semantics.Level = 4
	return t
}

// qHint is .q-hint: var(--type-small) --faint.
func qHint(s string) *views.Text { return views.NewParagraph(s, fontSmall, views.InkFaint) }

// footMsg is .foot-msg: an alert icon and a message in --danger (var(--type-cell)), 4 px below
// what comes before it (role alert).
type footMsg struct {
	kit.Element
	text *views.Text
}

func newFootMsg() *footMsg {
	m := &footMsg{}
	m.Semantics.Role = kit.RoleAlert
	m.Style = kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignStart, ColumnGap: kit.Px(7), Margin: kit.Edges{Top: kit.Px(4)}, MinWidth: kit.Px(0)}
	ic := widgets.NewIcon(icons.Alert, 14, func(t *tokens.Theme) kit.Color { return t.Danger })
	ic.Style.Margin = kit.Edges{Top: kit.Px(1)}
	m.text = views.NewParagraph("", fontCell, views.InkDanger)
	m.text.Semantics.Role = kit.RoleNone
	m.text.Style.MinWidth = kit.Px(0)
	m.Add(ic, m.text)
	return m
}

func (m *footMsg) set(s string) {
	m.text.SetText(s)
	if m.Semantics.Name != s {
		m.Semantics.Name = s
		m.SemanticsChanged()
	}
	m.SetHidden(s == "")
}
