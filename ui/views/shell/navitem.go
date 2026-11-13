package shell

import (
	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/views"
)

// navItem is the sidebar's .item: a 29 px button (34 px in the rail) with a glyph, a label, an
// optional note and a count, on a transparent ground that eases to --hover (--ink text) when
// hovered; the active one is Mint (--sel with a 1 px --sel-line ring, --ink text in 600, the
// count in --ink-2 600), --sel-hover when hovered. Its focus ring is inset 2 px.
type navItem struct {
	// glyphLook is a queue item's glyph and running state as last painted.
	glyphLook [2]int
	kit.Pressable
	glyph   *kit.Element
	label   *kit.Label
	note    *kit.Label
	count   *countBadge
	active  bool
	rail    bool
	bg, ink fade
	// mark is the theme bg and ink were set in.
	mark themeMark
}

// The item's metrics (Sidebar.svelte).
const (
	itemHeight     kit.DIP = 29 // .item block-size
	itemRailHeight kit.DIP = 34 // the rail's .item block-size
	itemPad        kit.DIP = 9  // .item padding-inline
	itemGap        kit.DIP = 10 // .item gap
	itemRadius     kit.DIP = 7  // .item border-radius
	glyphWidth     kit.DIP = 18 // .glyph inline-size
)

func newNavItem(glyph kit.Widget, label string, onPress func()) *navItem {
	it := &navItem{}
	it.OnPress = onPress
	it.Focus = kit.FocusTab
	it.Semantics.Role = kit.RoleButton
	it.Semantics.Toggle = kit.ToggleOff
	it.Cursor = kit.CursorPointer
	it.Ring = kit.Ring{Offset: kit.Px(-2)}
	it.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(itemGap),
		Width: kit.Pct(100), Height: kit.Px(itemHeight), Padding: kit.EdgesInline(kit.Px(itemPad)),
		Radius: kit.CornersAll(kit.Px(itemRadius)),
	}
	it.glyph = &kit.Element{Style: kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Justify: kit.JustifyCenter,
		Width: kit.Px(glyphWidth), Flex: kit.FlexNone,
	}}
	it.glyph.PassThrough = true
	if glyph != nil {
		it.glyph.Add(glyph)
	}
	it.label = kit.NewLabel(label, fontBody, kit.TextOptions{Truncate: kit.TruncateEnd, Direction: kit.DirAuto})
	it.label.Style = kit.Style{Flex: kit.Flex1, MinWidth: kit.Px(0)}
	it.label.Semantics.Role = kit.RoleNone
	it.label.PassThrough = true
	it.count = newCountBadge()
	it.Add(it.glyph, it.label, it.count)
	for _, f := range []*fade{&it.bg, &it.ink} {
		f.dur = tokens.Dur2
		f.onChange = it.Invalidate
	}
	it.applyFonts()
	return it
}

// addNote puts the queue's note (.qnote) between the label and the count.
func (it *navItem) addNote() {
	it.note = kit.NewLabel("", uiFont(tokens.TypeMeta, 400), kit.TextOptions{Truncate: kit.TruncateEnd})
	it.note.Style = kit.Style{Flex: kit.FlexNone, MinWidth: kit.Px(0)} // max-inline-size: 7em (LayoutContent)
	it.note.Semantics.Hidden = true                                    // aria-hidden: the state is in the name
	it.note.PassThrough = true
	it.Remove(it.count)
	it.Add(it.note, it.count)
}

func (it *navItem) theme() *tokens.Theme { return views.TokensOf(&it.Element) }

// colors are the item's ground and text in a state.
func (it *navItem) colors(t *tokens.Theme) (bg, ink kit.Color) {
	hover := it.Hovered()
	switch {
	case it.active && hover:
		return t.SelHover, t.Ink
	case it.active:
		return t.Sel, t.Ink
	case hover:
		return t.Hover, t.Ink
	}
	return kit.Color{}, t.Ink2
}

func (it *navItem) update() {
	t := it.theme()
	it.mark.changed(t)
	bg, ink := it.colors(t)
	w := it.Window()
	it.bg.moveTo(w, bg)
	it.ink.moveTo(w, ink)
}

// resetColors shows the state's colours at once (a theme change, a new active item).
func (it *navItem) resetColors() {
	t := it.theme()
	it.mark.changed(t)
	bg, ink := it.colors(t)
	it.bg.jump(bg)
	it.ink.jump(ink)
}

// Attached is kit.Attacher's.
func (it *navItem) Attached() { it.resetColors() }

// HoverChanged is kit.HoverHandler's.
func (it *navItem) HoverChanged(h bool) {
	it.Pressable.HoverChanged(h)
	it.update()
}

// setActive marks the active filter (aria-pressed).
func (it *navItem) setActive(a bool) {
	if a == it.active {
		return
	}
	it.active = a
	it.Semantics.Toggle = kit.ToggleOff
	if a {
		it.Semantics.Toggle = kit.ToggleOn
	}
	it.applyFonts()
	it.update()
	it.count.active = a
	it.count.Invalidate()
	it.SemanticsChanged()
	it.Invalidate()
}

// applyFonts: the active item's label and count are 600.
func (it *navItem) applyFonts() {
	f := fontBody
	c := uiFont(tokens.TypeSmall, 400)
	if it.active {
		f.Weight, c.Weight = 600, 600
	}
	if it.rail {
		c = uiFont(kit.Rem(0.625), c.Weight)
		c.LineHeight = 0
	}
	it.label.SetFont(f)
	it.count.label.SetFont(c)
}

// setRail lays the item out for the 56 px rail (721–999 px): the glyph centred, the label and
// note hidden (the name stays), the count a small badge at the top end corner.
func (it *navItem) setRail(rail bool) {
	if rail == it.rail {
		return
	}
	it.rail = rail
	h := itemHeight
	pad := itemPad
	j := kit.JustifyStart
	if rail {
		h, pad, j = itemRailHeight, 0, kit.JustifyCenter
	}
	it.Style.Height = kit.Px(h)
	it.Style.Padding = kit.EdgesInline(kit.Px(pad))
	it.Style.Justify = j
	it.label.SetHidden(rail)
	if it.note != nil {
		it.note.SetHidden(rail)
	}
	it.count.setRail(rail)
	it.applyFonts()
	it.Relayout()
}

// ContentWidths is kit.Layouter's: the glyph, the note and the count with their gaps (the
// label shrinks to nothing), or the glyph alone in the rail.
func (it *navItem) ContentWidths(ctx *kit.LayoutContext) (lo, hi kit.DIP) {
	if it.rail {
		return glyphWidth, glyphWidth
	}
	_, lw := ctx.IntrinsicWidths(it.label)
	fixed := glyphWidth + itemGap + it.countWidth(ctx)
	if it.note != nil && it.note.Text() != "" {
		fixed += it.noteWidth(ctx) + itemGap
	}
	return fixed + itemGap, fixed + itemGap + lw
}

// ContentHeight is kit.Layouter's (the item's height is fixed).
func (it *navItem) ContentHeight(*kit.LayoutContext, kit.DIP) kit.DIP { return itemHeight }

// countWidth is the count's width: its text, at least 1.5em of its own font (min-inline-size).
func (it *navItem) countWidth(ctx *kit.LayoutContext) kit.DIP {
	_, w := ctx.IntrinsicWidths(it.count.label)
	return max(w, 1.5*it.count.label.Font().Style(ctx.Scale).Size)
}

func (it *navItem) noteWidth(ctx *kit.LayoutContext) kit.DIP {
	_, w := ctx.IntrinsicWidths(it.note)
	em := it.note.Font().Style(ctx.Scale).Size
	return min(w, 7*em)
}

// LayoutContent is kit.Layouter's: .item's flex row (glyph, label, note, count, 10 px apart,
// centred), or in the rail the glyph centred and the count at the top end corner.
func (it *navItem) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	cw := box.Width()
	centerY := func(h kit.DIP) kit.DIP { return box.Min.Y + (box.Height()-h)/2 }
	gh := ctx.HeightFor(it.glyph, glyphWidth, cw)
	if it.rail {
		ctx.Place(it.glyph, kit.RectAt(kit.Pt(box.Min.X+(cw-glyphWidth)/2, centerY(gh)), kit.Size{W: glyphWidth, H: gh}), cw)
		_, lw := ctx.IntrinsicWidths(it.count.label)
		bw := max(badgeMinW, lw+2*badgePad)
		ctx.Place(it.count, kit.RectAt(kit.Pt(box.Max.X-badgeInset-bw, box.Min.Y+badgeInset), kit.Size{W: bw, H: badgeHeight}), cw)
		lh := ctx.HeightFor(it.count.label, bw-2*badgePad, bw)
		ctx.Place(it.count.label, kit.RectAt(kit.Pt(badgePad, (badgeHeight-lh)/2), kit.Size{W: bw - 2*badgePad, H: lh}), bw)
		return
	}
	x := box.Min.X
	ctx.Place(it.glyph, kit.RectAt(kit.Pt(x, centerY(gh)), kit.Size{W: glyphWidth, H: gh}), cw)
	x += glyphWidth + itemGap
	cntW := it.countWidth(ctx)
	// end is where the label stops: a gap before the count, and before the note with its own
	// gap to the count when there is one (label 10 px note 10 px count, as the flex row).
	end := box.Max.X - cntW - itemGap
	var nw kit.DIP
	if it.note != nil && it.note.Text() != "" {
		nw = it.noteWidth(ctx)
		end -= nw + itemGap
	}
	lw := max(0, end-x)
	lh := ctx.HeightFor(it.label, lw, cw)
	ctx.Place(it.label, kit.RectAt(kit.Pt(x, centerY(lh)), kit.Size{W: lw, H: lh}), cw)
	if it.note != nil {
		nh := ctx.HeightFor(it.note, nw, cw)
		ctx.Place(it.note, kit.RectAt(kit.Pt(end+itemGap, centerY(nh)), kit.Size{W: nw, H: nh}), cw)
	}
	ch := ctx.HeightFor(it.count.label, cntW, cntW)
	ctx.Place(it.count, kit.RectAt(kit.Pt(box.Max.X-cntW, centerY(ch)), kit.Size{W: cntW, H: ch}), cw)
	ctx.Place(it.count.label, kit.RectAt(kit.Point{}, kit.Size{W: cntW, H: ch}), cntW)
}

// Paint is kit.Painter's: the ground, the active ring, and the texts' colours.
func (it *navItem) Paint(p *kit.PaintContext) {
	t := it.theme()
	if it.mark.changed(t) {
		// the theme changed since the colours were set: the new theme's, at once
		bg, ink := it.colors(t)
		it.bg.snap(bg)
		it.ink.snap(ink)
	}
	r := views.SnapBox(p, itemRadius)
	if it.bg.cur.A > 0 {
		p.Canvas.FillRoundedRect(r, it.bg.cur)
	}
	if it.active {
		// box-shadow: inset 0 0 0 1px var(--sel-line)
		w := p.Scale.SnapWidth(1)
		p.Canvas.StrokeRoundedRect(r.Inset(w/2), kit.Stroke{Width: w}, t.SelLine)
	}
	it.label.SetColor(it.ink.cur)
	if it.note != nil {
		it.note.SetColor(t.Faint)
	}
}

// countBadge is .count: the item's count, at its end (min 1.5em, end-aligned, --type-small in
// --faint; the active item's in --ink-2 600); in the rail a 14 px pill at the top end corner
// on --chrome (--sel for the active one).
type countBadge struct {
	kit.Element
	label  *kit.Label
	active bool
	rail   bool
}

func newCountBadge() *countBadge {
	c := &countBadge{}
	c.label = kit.NewLabel("0", uiFont(tokens.TypeSmall, 400), kit.TextOptions{Align: kit.TextAlignEnd})
	c.label.Semantics.Role = kit.RoleNone
	c.label.Style = kit.Style{Flex: kit.Flex1}
	c.label.PassThrough = true
	c.PassThrough = true
	c.Style = kit.Style{Display: kit.DisplayFlex, Flex: kit.FlexNone}
	c.Add(c.label)
	return c
}

func (c *countBadge) setRail(rail bool) {
	c.rail = rail
	if rail {
		c.label.SetOptions(kit.TextOptions{Align: kit.TextAlignCenter})
	} else {
		c.label.SetOptions(kit.TextOptions{Align: kit.TextAlignEnd})
	}
	c.Relayout()
}

// The rail's count badge (Sidebar.svelte's narrow .count).
const (
	badgeInset  kit.DIP = 1  // inset-block-start, inset-inline-end
	badgeMinW   kit.DIP = 15 // min-inline-size
	badgeHeight kit.DIP = 14 // block-size
	badgePad    kit.DIP = 3  // padding-inline
)

// Paint is kit.Painter's.
func (c *countBadge) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&c.Element)
	if c.rail {
		bg := t.Chrome
		if c.active {
			bg = t.Sel
		}
		r := p.Size()
		p.Canvas.FillRoundedRect(views.SnapBox(p, min(r.H, r.W)/2), bg)
	}
	col := t.Faint
	if c.active {
		col = t.Ink2
	}
	c.label.SetColor(col)
}

func (c *countBadge) set(s string) {
	if c.label.Text() != s {
		c.label.SetText(s)
	}
}
