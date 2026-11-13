package widgets

import (
	"downex/ui/kit"
	"downex/ui/kit/tokens"
)

// Tone is a chip's or a pill's colour (app.css §3.7).
type Tone uint8

// The tones. A chip takes Plain, Live and Fail; a pill every tone but Live.
const (
	Plain Tone = iota
	Live       // a chip in Mint (.chip.live)
	Ok         // .pill.ok
	Info       // .pill.info
	Warn       // .pill.warn
	Fail       // .chip.fail, .pill.fail
	Done       // .pill.done
)

// Chip is app.css's .chip: a 22 px pill of 12 px text (--surface-2 with a --line border,
// --ink-2), Live in Mint (600) or Fail in --danger. A chip with OnPress is a button.chip: it
// toggles a filter (aria-pressed, --sel ground with a --sel-line border) and hovers. Its
// optional 7 px state dot and icon come before the text.
type Chip struct {
	control
	Tone    Tone
	Pressed bool
	button  bool
	dot     bool
	icon    *Icon
	label   *kit.Label
}

// NewChip makes a chip; onPress nil makes a static one (a span), else a toggle button.
func NewChip(text string, tone Tone, dot bool, icon string, onPress func()) *Chip {
	c := &Chip{Tone: tone, button: onPress != nil, dot: dot}
	if c.button {
		c.init(onPress)
	} else {
		c.Semantics.Role = kit.RoleText
	}
	c.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Flex: kit.FlexNone, ColumnGap: kit.Px(6),
		Height: kit.Px(22), Padding: kit.EdgesInline(kit.Px(8)), Border: kit.EdgesAll(kit.Px(1)), Radius: kit.CornersAll(tokens.RPill),
	}
	if dot {
		d := &kit.Element{Style: kit.Style{Width: kit.Px(7), Height: kit.Px(7), Flex: kit.FlexNone}}
		d.Semantics.Hidden = true
		c.Add(&chipDot{Element: *d, c: c})
	}
	if n, ok := iconOf(icon); ok {
		c.icon = NewIcon(n, 14, func(t *tokens.Theme) kit.Color { return t.Muted })
		c.Add(c.icon)
	}
	f := font(tokens.TypeSmall, 400)
	f.LineHeight = 1
	if tone == Live {
		f.Weight = 600
	}
	c.label = label(text, f)
	c.Add(c.label)
	return c
}

// chipDot is app.css's .dot: 7 px in the chip's text colour.
type chipDot struct {
	kit.Element
	c *Chip
}

// Paint is kit.Painter's.
func (d *chipDot) Paint(p *kit.PaintContext) {
	t := tokensOf(&d.Element)
	_, _, ink := d.c.colors(t, d.c.look.state())
	p.Canvas.FillEllipse(p.Scale.SnapRect(kit.RectAt(p.Origin, p.Size())), ink)
}

// SetText changes the chip's text.
func (c *Chip) SetText(s string) { c.label.SetText(s) }

// SetPressed shows the chip's filter on or off (aria-pressed).
func (c *Chip) SetPressed(on bool) {
	if on != c.Pressed {
		c.Pressed = on
		c.Invalidate()
		c.label.Invalidate()
		c.SemanticsChanged()
	}
}

func (c *Chip) colors(t *tokens.Theme, s uint8) (bg, border, ink kit.Color) {
	bg, border, ink = t.Surface2, t.Line, t.Ink2
	switch c.Tone {
	case Live:
		bg, border, ink = t.AccentSoft, kit.Color{}, t.AccentText
	case Fail:
		ink = t.Danger
	}
	if c.button && !c.Disabled && s&stHover != 0 {
		bg = t.Hover
		if c.Tone == Live {
			bg = t.SelHover
		}
	}
	if c.button && c.Pressed {
		bg, border, ink = t.Sel, t.SelLine, t.Ink
		switch c.Tone {
		case Live:
			border, ink = t.AccentLine, t.AccentText
		case Fail:
			bg, border, ink = t.DangerSoft, t.DangerLine, t.Danger
		}
	}
	if c.Disabled {
		ink = t.Faint
	}
	if t.IsForced() {
		border = t.Ink
	}
	return bg, border, ink
}

// Paint is kit.Painter's.
func (c *Chip) Paint(p *kit.PaintContext) {
	t := tokensOf(&c.Element)
	fb, fbo, fi := c.colors(t, c.look.fromState())
	tb, tbo, ti := c.colors(t, c.look.state())
	r := snapBox(p, p.Size().H/2)
	p.Canvas.FillRoundedRect(r, c.look.mix(fb, tb))
	strokeInside(p.Canvas, r, p.Scale.SnapWidth(1), c.look.mix(fbo, tbo))
	c.label.SetColor(c.look.mix(fi, ti))
}

// FillSemantics is kit.SemanticFiller's: a toggle button's state.
func (c *Chip) FillSemantics(n *kit.SemanticNode) {
	if !c.button {
		return
	}
	c.Pressable.FillSemantics(n)
	n.Toggle = kit.ToggleOff
	if c.Pressed {
		n.Toggle = kit.ToggleOn
	}
}

// Pill is app.css's .pill: a 20 px pill of 11 px 600 text in its tone (a queue's state, a
// tool's, an update).
type Pill struct {
	kit.Element
	Tone  Tone
	label *kit.Label
}

// NewPill makes a pill.
func NewPill(text string, tone Tone) *Pill {
	p := &Pill{Tone: tone}
	p.Semantics.Role = kit.RoleText
	p.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Flex: kit.FlexNone, ColumnGap: kit.Px(5),
		Height: kit.Px(20), Padding: kit.EdgesInline(kit.Px(7)), Radius: kit.CornersAll(tokens.RPill),
	}
	f := font(kit.Rem(0.6875), 600)
	f.LineHeight = 1
	p.label = label(text, f)
	p.Add(p.label)
	return p
}

// SetText changes the pill's text.
func (p *Pill) SetText(s string) { p.label.SetText(s) }

// SetTone changes the pill's tone.
func (p *Pill) SetTone(t Tone) {
	if t != p.Tone {
		p.Tone = t
		p.Invalidate()
	}
}

func (p *Pill) colors(t *tokens.Theme) (bg, ink kit.Color) {
	switch p.Tone {
	case Ok:
		return t.AccentSoft, t.AccentText
	case Info:
		return t.InfoSoft, t.Info
	case Warn:
		return t.WarnSoft, t.Warn
	case Fail:
		return t.DangerSoft, t.Danger
	}
	return t.Chrome, t.Muted
}

// Paint is kit.Painter's.
func (p *Pill) Paint(c *kit.PaintContext) {
	t := tokensOf(&p.Element)
	bg, ink := p.colors(t)
	r := snapBox(c, c.Size().H/2)
	c.Canvas.FillRoundedRect(r, bg)
	if t.IsForced() {
		strokeInside(c.Canvas, r, c.Scale.SnapWidth(1), t.Ink)
	}
	p.label.SetColor(ink)
}

// Kbd is app.css's kbd: a key cap (18 px, a 1 px --line-strong border with a 2 px bottom,
// 5 px corners, --surface, 11 px 500 --muted) showing a shortcut.
type Kbd struct {
	kit.Element
	label *kit.Label
}

// NewKbd makes a key cap.
func NewKbd(text string) *Kbd {
	k := &Kbd{}
	k.Semantics.Hidden = true // the shortcut is the item's aria-keyshortcuts
	k.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Justify: kit.JustifyCenter, Flex: kit.FlexNone,
		Height: kit.Px(18), MinWidth: kit.Px(18), Padding: kit.EdgesInline(kit.Px(5)),
		Border: kit.Edges{Top: kit.Px(1), End: kit.Px(1), Bottom: kit.Px(2), Start: kit.Px(1)},
	}
	k.label = label(kit.KeyLabel(text), kit.Font{Family: tokens.FontUI, Size: kit.Rem(0.6875), Weight: 500, LineHeight: 1, AutoOptical: true})
	k.Add(k.label)
	return k
}

// Paint is kit.Painter's: the cap's border (2 px at the bottom) and ground.
func (k *Kbd) Paint(p *kit.PaintContext) {
	t := tokensOf(&k.Element)
	box := p.Scale.SnapRect(kit.RectAt(p.Origin, p.Size()))
	s := p.Scale
	top, bottom := s.SnapWidth(1), s.SnapWidth(2)
	p.Canvas.FillRoundedRect(kit.RoundRect(box, 5), t.LineStrong)
	inner := kit.R(box.Min.X+top, box.Min.Y+top, box.Max.X-top, box.Max.Y-bottom)
	p.Canvas.FillRoundedRect(kit.RRect{Rect: inner, Radii: kit.Radii{TopLeft: 5 - top, TopRight: 5 - top, BottomRight: 5 - bottom, BottomLeft: 5 - bottom}}, t.Surface)
	k.label.SetColor(t.Muted)
}

// HandlePointer is kit.PointerHandler's: only a chip that is a button takes the pointer.
func (c *Chip) HandlePointer(in *kit.PointerInput) bool {
	if !c.button {
		return false
	}
	return c.control.HandlePointer(in)
}
