package settings

import (
	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/views"
)

// The pages' shared layout classes (SettingsDialog.svelte's s-*), and the fonts of the
// Settings window's own rules.

func uiFont(size kit.Length, weight, lineHeight float32) kit.Font {
	return kit.Font{Family: tokens.FontUI, Size: size, Weight: weight, LineHeight: lineHeight, Tabular: true, AutoOptical: true}
}

func displayFont(size kit.Length, weight, lineHeight float32) kit.Font {
	return kit.Font{Family: tokens.FontDisplay, Size: size, Weight: weight, LineHeight: lineHeight, Tabular: true, AutoOptical: true}
}

var (
	// fontLead is .s-lead: var(--type-cell), line-height 1.45.
	fontLead = uiFont(tokens.TypeCell, 400, 1.45)
	// fontLabel is .s-label and a switch's label: var(--type-body) 500.
	fontLabel = uiFont(tokens.TypeBody, 500, 1.4)
	// fontHint is .s-hint: var(--type-small).
	fontHint = uiFont(tokens.TypeSmall, 400, 1.4)
	// fontCell is var(--type-cell) at body's line height.
	fontCell = uiFont(tokens.TypeCell, 400, 1.4)
	// fontBody is body's.
	fontBody = uiFont(tokens.TypeBody, 400, 1.4)
	// fontLeadTitle is 650 var(--type-lead) / 1.3 var(--font-display) (the editor's h4, a tool's
	// name, Advanced).
	fontLeadTitle = displayFont(tokens.TypeLead, 650, 1.3)
	// fontFoot is .foot-msg: var(--type-cell), line-height 1.35.
	fontFoot = uiFont(tokens.TypeCell, 400, 1.35)
	// fontCaps is the categories' and the tables' column heads: 600 var(--type-caps), +0.07em,
	// upper case.
	fontCaps = views.FontCaps
	// fontMono is .mono.
	fontMono = views.FontMono
)

// el is a plain element with a style.
func el(st kit.Style, children ...kit.Widget) *kit.Element {
	e := &kit.Element{Style: st}
	e.Add(children...)
	return e
}

// stack is a block whose children are gap apart (a one-column grid).
func stack(gap kit.Length, children ...kit.Widget) *kit.Element {
	return el(kit.Style{Display: kit.DisplayBlock, RowGap: gap, MinWidth: kit.Px(0)}, children...)
}

// para is a wrapping text.
func para(s string, f kit.Font, ink views.Ink) *views.Text { return views.NewParagraph(s, f, ink) }

// heading is a heading's text (h3: Level 3).
func heading(s string, f kit.Font, level int) *views.Text {
	t := views.NewParagraph(s, f, views.InkInk)
	t.Semantics.Role = kit.RoleHeading
	t.Semantics.Level = level
	return t
}

// sTitle is .s-title (an h3): on Windows 600 var(--type-body) in --ink, on the Mac 600 12.5 px
// in --muted (a group's heading).
func sTitle(s string) *views.Text {
	if curSkin == skinMac {
		t := heading(s, fontMacHead, 3)
		t.Ink = views.InkMuted
		return t
	}
	return heading(s, fontFluentHead, 3)
}

// sLead is .s-lead.
func sLead(s string) *views.Text { return para(s, fontLead, views.InkMuted) }

// sHint is .s-hint.
func sHint(s string) *views.Text { return para(s, fontHint, views.InkFaint) }

// sHead is .s-head: the title and its leads, 3 px apart.
func sHead(title string, leads ...kit.Widget) *kit.Element {
	h := stack(kit.Px(3), sTitle(title))
	h.Add(leads...)
	return h
}

// sSection is .s-section: its parts 12 px apart; it is a region named by its title.
func sSection(head kit.Widget, parts ...kit.Widget) *kit.Element {
	s := stack(kit.Px(8))
	if head != nil {
		s.Add(head)
	}
	s.Add(parts...)
	return s
}

// sCard is .s-card: --surface in a --line border with --r-lg corners; setRows draws
// .s-row + .s-row's rules.
//
// On Windows the card itself is not drawn: every row is its own card, 3 px apart (Windows 11's
// Settings). On the Mac it is a group on --surface-2 in a --line-soft border.
func sCard(rows ...kit.Widget) *surface {
	c := newSurface(kit.Style{Display: kit.DisplayBlock, MinWidth: kit.Px(0), Border: kit.EdgesAll(kit.Px(1))}, inkSurface2, inkLineSoft, 10)
	setRows(c, rows...)
	return c
}

// rowCards makes a box of rows draw each row as its own card (Windows): the box loses its
// ground and border and keeps its rows 3 px apart.
func rowCards(c *surface) {
	if curSkin != skinFluent || c.cards {
		return
	}
	c.cards = true
	c.ground, c.edge = nil, nil
	c.Style.Border = kit.Edges{}
	c.Style.RowGap = kit.Px(3)
	c.Relayout()
}

// setRows puts rows into a card, each after the first with its 1 px --line-soft rule (the
// rows' top border).
func setRows(c *surface, rows ...kit.Widget) {
	c.RemoveAll()
	first := true
	for _, r := range rows {
		if r == nil {
			continue
		}
		if l, ok := r.(looker); ok && l.lookOf().rule && curSkin == skinFluent {
			rowCards(c)
			lk := l.lookOf()
			lk.card, lk.ground, lk.edge, lk.radius = true, inkSurface2, inkLineSoft, 8
			r.Base().Style.Border = kit.EdgesAll(kit.Px(1))
		} else if l, ok := r.(looker); ok && l.lookOf().rule {
			lk := l.lookOf()
			lk.ruled = !first && !r.Base().Style.Hidden
			r.Base().Style.Border.Top = kit.Length{}
			if lk.ruled {
				r.Base().Style.Border.Top = kit.Px(1)
			}
		}
		if !r.Base().Style.Hidden {
			first = false
		}
		c.Add(r)
	}
}

// reRule redraws a card's rules after a row was shown or hidden.
func reRule(c *surface) {
	kids := c.Children()
	rows := make([]kit.Widget, len(kids))
	for i, k := range kids {
		rows[i] = k.Widget()
	}
	first := true
	for _, r := range rows {
		if l, ok := r.(looker); ok && l.lookOf().rule && !l.lookOf().card {
			lk := l.lookOf()
			was := lk.ruled
			lk.ruled = !first && !r.Base().Style.Hidden
			if was != lk.ruled {
				r.Base().Style.Border.Top = kit.Length{}
				if lk.ruled {
					r.Base().Style.Border.Top = kit.Px(1)
				}
				r.Base().Relayout()
			}
		}
		if !r.Base().Style.Hidden {
			first = false
		}
	}
}

// sRow is .s-row: a grid of minmax(0, 1fr) auto, centred, 6 × 20 px apart, at least 52 px
// high with 9 × 14 px of padding.
func sRow(children ...kit.Widget) *grid {
	g := newGrid(tracks(frac(kit.Px(0), 1), auto()), kit.Px(20), kit.Px(6), children...)
	g.alignItems = kit.AlignCenter
	g.Style.MinHeight, g.Style.Padding = rowBox(kit.Px(9))
	g.edge, g.rule = inkLineSoft, true
	return g
}

// rowBox is a row's least height and padding (block is its top and bottom padding today):
// Windows' rows are 62 px high with 18 px at the sides, the Mac's 44 px with 14 px.
func rowBox(block kit.Length) (kit.Length, kit.Edges) {
	if curSkin == skinMac {
		return kit.Px(44), kit.EdgesOf(block, kit.Px(14))
	}
	return kit.Px(62), kit.EdgesOf(block, kit.Px(18))
}

// sStack is .s-row.stack: one column, 10 px apart, 12 × 14 px of padding.
func sStack(children ...kit.Widget) *surface {
	mh, pad := rowBox(kit.Px(12))
	r := newSurface(kit.Style{Display: kit.DisplayBlock, RowGap: kit.Px(10), MinWidth: kit.Px(0), MinHeight: mh,
		Padding: pad}, nil, inkLineSoft, 0, children...)
	r.rule = true
	return r
}

// sSwitchRow is .s-row.s-switch: a block with 2 × 14 px of padding.
func sSwitchRow(children ...kit.Widget) *surface {
	mh, pad := rowBox(kit.Px(2))
	// a column that centres the switch's row in the least height
	r := newSurface(kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, Justify: kit.JustifyCenter, MinWidth: kit.Px(0), MinHeight: mh,
		Padding: pad}, nil, inkLineSoft, 0, children...)
	r.rule = true
	return r
}

// sText is .s-text: a label over its hint, 2 px apart.
func sText(label string, hint *views.Text) *kit.Element {
	t := stack(kit.Px(2), para(label, fontLabel, views.InkInk))
	if hint != nil {
		t.Add(hint)
	}
	return t
}

// sControl is .s-control: the controls at the end, 8 px apart.
func sControl(children ...kit.Widget) *kit.Element {
	return el(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Justify: kit.JustifyEnd, ColumnGap: kit.Px(8), MinWidth: kit.Px(0)}, children...)
}

// sAfter is .s-after: what follows a card (its buttons and notes), wrapping, 8 × 12 px apart.
func sAfter(children ...kit.Widget) *kit.Element {
	// a wrapped line's item shrinks to the line (flex-shrink), as kit's flex lines do not
	for _, c := range children {
		if _, ok := c.(*views.Text); ok {
			c.Base().Style.MaxWidth = kit.Pct(100)
		}
	}
	return el(kit.Style{Display: kit.DisplayFlex, Wrap: true, AlignItems: kit.AlignCenter, RowGap: kit.Px(8), ColumnGap: kit.Px(12), MinWidth: kit.Px(0)}, children...)
}

// switchRow is a switch in an .s-row.s-switch.
func switchRow(label, hint string, on bool, onChange func(bool)) (*surface, *widgets.SwitchRow) {
	sw := widgets.NewSwitchRow(label, hint, on, onChange)
	sw.Switch.SetStyle(curSkin.switchStyle())
	return sSwitchRow(sw), sw
}

// icon is a decorative icon in an ink.
func icon(name string, size kit.DIP, ink views.Ink) *widgets.Icon {
	n, _ := icons.ByName(name)
	return widgets.NewIcon(n, size, ink)
}

// button makes a button; small is .btn.sm.
func button(label, iconName string, v widgets.Variant, small bool, onPress func()) *widgets.Button {
	size := widgets.SizeMd
	if small {
		size = widgets.SizeSm
	}
	b := widgets.NewButton(label, iconName, v, size, onPress)
	if small && iconName != "" {
		b.SetIconSize(14)
	}
	return b
}

// iconButton is .icon-btn.sm with a tooltip.
func iconButton(iconName string, name, tooltip string, onPress func()) *widgets.IconButton {
	b := widgets.NewIconButton(iconName, 16, name, true, onPress)
	b.Tooltip = tooltip
	return b
}

// input makes an .input.sm (mono: .input.mono).
func input(value string, o widgets.InputOptions, onChange func(string)) *widgets.Input {
	o.Size = widgets.FieldSm
	return widgets.NewInput(value, o, onChange)
}

// errorText is an .error-text that shows while it has a message.
type errorText struct{ *views.Text }

// TextWidget is the text shown (tests read it).
func (e *errorText) TextWidget() *views.Text { return e.Text }

func newErrorText() *errorText {
	e := &errorText{views.NewErrorText("")}
	e.Style.Hidden = true
	return e
}

// set shows s ("" hides it).
func (e *errorText) set(s string) {
	if e.Text.Text() != s {
		e.SetText(s)
	}
	e.SetHidden(s == "")
}

// overlay lays an input out with an icon or a suffix over it, as `position: absolute` does
// (.s-field-icon's folder at inset-inline-start 10px; .suffixed's "MB" at inset-inline-end 10px):
// the input fills the box; the overlay is centred on it at its side.
type overlay struct {
	kit.Element
	field *widgets.Input
	over  kit.Widget
	end   bool
	inset kit.DIP
}

func newOverlay(field *widgets.Input, over kit.Widget, end bool) *overlay {
	o := &overlay{field: field, over: over, end: end, inset: 10}
	o.Style.MinWidth = kit.Px(0)
	over.Base().PassThrough = true
	o.Add(field, over)
	return o
}

// ContentWidths is kit.Layouter's.
func (o *overlay) ContentWidths(ctx *kit.LayoutContext) (lo, hi kit.DIP) {
	return ctx.IntrinsicWidths(o.field)
}

// ContentHeight is kit.Layouter's.
func (o *overlay) ContentHeight(ctx *kit.LayoutContext, w kit.DIP) kit.DIP {
	return ctx.HeightFor(o.field, w, w)
}

// LayoutContent is kit.Layouter's.
func (o *overlay) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	ctx.Place(o.field, box, box.Width())
	_, ow := ctx.IntrinsicWidths(o.over)
	oh := ctx.HeightFor(o.over, ow, ow)
	x := box.Min.X + o.inset
	if o.end != ctx.RTL {
		x = box.Max.X - o.inset - ow
	}
	y := box.Min.Y + (box.Height()-oh)/2
	ctx.Place(o.over, kit.RectAt(kit.Pt(x, y), kit.Size{W: ow, H: oh}), ow)
}

// checkSmall is `.check.small`: a check box whose label is var(--type-cell) in --ink-2.
type checkSmall struct{ *widgets.Checkbox }

func newCheckSmall(label string, checked bool, onChange func(bool)) *checkSmall {
	c := &checkSmall{widgets.NewCheckbox(label, checked, onChange)}
	c.Label.SetFont(fontCell)
	return c
}

// Paint is kit.Painter's: the label in --ink-2 (--faint while disabled).
func (c *checkSmall) Paint(p *kit.PaintContext) {
	t := views.TokensOf(c.Base())
	col := t.Ink2
	if c.Box.Disabled {
		col = t.Faint
	}
	c.Label.SetColor(col)
}
