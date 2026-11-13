package shell

import (
	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
)

// These are the title bar's controls. Each eases its colours as app.css's transitions do
// (background-color and color over --dur-2, ease), paints from its state (hover, press,
// focus) and its theme, and keeps the kit's button behaviour (kit.Pressable).

// pressable is the part the buttons share: their colours follow their state.
type pressable struct {
	kit.Pressable
	tb       *TitleBar
	bg, ink  fade
	colorsOf func(t *tokens.Theme, hover, down bool) (bg, ink kit.Color)
}

func (b *pressable) init(tb *TitleBar, onPress func(), colorsOf func(t *tokens.Theme, hover, down bool) (bg, ink kit.Color)) {
	b.tb, b.colorsOf = tb, colorsOf
	b.OnPress = onPress
	b.Focus = kit.FocusTab
	b.Semantics.Role = kit.RoleButton
	for _, f := range []*fade{&b.bg, &b.ink} {
		f.dur = tokens.Dur2
		f.onChange = b.Invalidate
	}
}

func (b *pressable) theme() *tokens.Theme {
	if w := b.Window(); w != nil {
		return b.tb.theme.Tokens(w.Environment())
	}
	return b.tb.theme.Tokens(kit.Environment{})
}

// update moves the colours towards the state's.
func (b *pressable) update() {
	bg, ink := b.colorsOf(b.theme(), b.Hovered(), b.Down())
	w := b.Window()
	b.bg.moveTo(w, bg)
	b.ink.moveTo(w, ink)
}

// resetColors shows the state's colours at once (a theme change).
func (b *pressable) resetColors() {
	bg, ink := b.colorsOf(b.theme(), b.Hovered(), b.Down())
	b.bg.jump(bg)
	b.ink.jump(ink)
}

// Attached is kit.Attacher's: the colours of the resting state.
func (b *pressable) Attached() { b.resetColors() }

// HandlePointer is kit.PointerHandler's: the kit's button, then the colours.
func (b *pressable) HandlePointer(in *kit.PointerInput) bool {
	used := b.Pressable.HandlePointer(in)
	b.update()
	return used
}

// HandleKey is kit.KeyHandler's (Space shows the press).
func (b *pressable) HandleKey(e kit.KeyEvent) bool {
	used := b.Pressable.HandleKey(e)
	b.update()
	return used
}

// HoverChanged is kit.HoverHandler's.
func (b *pressable) HoverChanged(h bool) {
	b.Pressable.HoverChanged(h)
	b.update()
}

// FocusChanged is kit.FocusHandler's.
func (b *pressable) FocusChanged(f bool) {
	b.Pressable.FocusChanged(f)
	b.update()
}

// iconButton is app.css's .icon-btn as the title bar sizes it (.bar-btn): 32 × 30 with a
// 1 px transparent border and --r-md corners, a 17 px icon in --muted; hover --hover and
// --ink, pressed --press.
type iconButton struct {
	pressable
	icon *iconBox
}

func newIconButton(tb *TitleBar, name icons.Name, onPress func()) *iconButton {
	b := &iconButton{}
	b.init(tb, onPress, func(t *tokens.Theme, hover, down bool) (kit.Color, kit.Color) {
		switch {
		case down:
			return t.Press, t.Ink // :active, on the hovered button
		case hover:
			return t.Hover, t.Ink
		}
		return kit.Color{}, t.Muted
	})
	b.Style = kit.Style{
		Width: kit.Px(barBtnW), Height: kit.Px(barBtnH), Border: kit.EdgesAll(kit.Px(1)), Radius: kit.CornersAll(tokens.RMd),
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Justify: kit.JustifyCenter, Flex: kit.FlexNone,
	}
	b.Cursor = kit.CursorPointer
	b.icon = newIconBox(name, barIcon, func(*kit.PaintContext) kit.Color { return b.ink.cur })
	b.Add(b.icon)
	return b
}

// Paint is kit.Painter's.
func (b *iconButton) Paint(p *kit.PaintContext) {
	p.FillRoundedRect(kit.RoundRect(kit.RectAt(kit.Point{}, p.Size()), 8), b.bg.cur)
}

// winButton is TitleBar's .wc: 46 px wide, the bar's height, a 20 px icon in --ink-2; hover
// --press and --ink, pressed --line; the close button --winctl-close with a white glyph
// (Highlight and HighlightText in forced colours). Its focus ring is inset.
type winButton struct {
	pressable
	icon *iconBox
}

func newWinButton(tb *TitleBar, name icons.Name, label string, part kit.FramePart, closing bool, onPress func()) *winButton {
	b := &winButton{}
	b.init(tb, onPress, func(t *tokens.Theme, hover, down bool) (kit.Color, kit.Color) {
		switch {
		case closing && (hover || down) && t.IsForced():
			return t.Focus, t.AccentOn // Highlight, HighlightText
		case closing && (hover || down):
			return t.WinctlClose, closeGlyph
		case down:
			return t.Line, t.Ink
		case hover:
			return t.Press, t.Ink
		}
		return kit.Color{}, t.Ink2
	})
	b.Style = kit.Style{
		Width: kit.Px(wcWidth), AlignSelf: kit.AlignStretch, Flex: kit.FlexNone,
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Justify: kit.JustifyCenter,
	}
	b.Frame = part
	b.Ring = kit.Ring{Offset: kit.Px(-2)}
	b.Semantics.Name = label
	b.Tooltip = label
	b.icon = newIconBox(name, wcIcon, func(*kit.PaintContext) kit.Color { return b.ink.cur })
	b.Add(b.icon)
	return b
}

// closeGlyph is the close button's glyph on --winctl-close (TitleBar.svelte: color #fff).
var closeGlyph = kit.RGB(0xffffff)

// Paint is kit.Painter's.
func (b *winButton) Paint(p *kit.PaintContext) {
	p.FillRect(kit.RectAt(kit.Point{}, p.Size()), b.bg.cur)
}

// paletteKey is TitleBar's .palette-key: a 24 px target with app.css's kbd "Ctrl K" in it
// (19 px, a 1 px --line-strong border with a 2 px bottom, --surface, --muted text; hover
// --ink text and a --line-field border). It opens the command palette.
type paletteKey struct {
	pressable
	kbd    *kbd
	border fade
}

type kbd struct {
	kit.Element
	key  *paletteKey
	text *kit.Label
}

func newPaletteKey(tb *TitleBar) *paletteKey {
	k := &paletteKey{}
	k.init(tb, func() {
		if tb.OnCommands != nil {
			tb.OnCommands()
		}
	}, func(t *tokens.Theme, hover, down bool) (kit.Color, kit.Color) {
		if hover {
			return t.LineField, t.Ink // the kbd's border and text
		}
		return t.LineStrong, t.Muted
	})
	k.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Justify: kit.JustifyCenter,
		Height: kit.Px(keyHeight), MinWidth: kit.Px(keyHeight), Padding: kit.EdgesInline(kit.Px(keyPadInline)),
		Radius: kit.CornersAll(tokens.RSm),
	}
	k.Cursor = kit.CursorPointer
	k.Ring = kit.Ring{Offset: kit.Px(1)}
	k.Semantics.Name = "Commands"
	k.Semantics.Shortcut = kit.KeyLabel("Ctrl+K")
	k.Tooltip = kit.KeyLabel("Commands (Ctrl+K)")
	k.kbd = &kbd{key: k}
	k.kbd.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Justify: kit.JustifyCenter,
		Height: kit.Px(kbdHeight), MinWidth: kit.Px(kbdMinWidth), Padding: kit.EdgesInline(kit.Px(kbdPadInline)),
		Border: kit.Edges{Top: kit.Px(1), End: kit.Px(1), Bottom: kit.Px(2), Start: kit.Px(1)},
	}
	k.kbd.text = kit.NewLabel(kit.KeyLabel("Ctrl K"), fontKbd, kit.TextOptions{})
	k.kbd.text.Semantics.Role = kit.RoleNone
	k.kbd.Add(k.kbd.text)
	k.Add(k.kbd)
	k.ink.onChange = func() { k.kbd.text.SetColor(k.ink.cur) } // the label repaints itself
	return k
}

// Paint is kit.Painter's: the key cap's border and ground (a rounded box whose bottom
// border is twice as thick), in the faded colours.
func (k *kbd) Paint(p *kit.PaintContext) {
	t := k.key.theme()
	box := kit.RectAt(kit.Point{}, p.Size())
	s := p.Scale
	top, bottom := s.SnapWidth(1), s.SnapWidth(2)
	p.FillRoundedRect(kit.RoundRect(box, kbdRadius), k.key.bg.cur) // the border colour
	inner := kit.R(box.Min.X+top, box.Min.Y+top, box.Max.X-top, box.Max.Y-bottom)
	p.FillRoundedRect(kit.RRect{Rect: inner, Radii: kit.Radii{
		TopLeft: kbdRadius - top, TopRight: kbdRadius - top, BottomRight: kbdRadius - bottom, BottomLeft: kbdRadius - bottom,
	}}, t.Surface)
}

// searchField is TitleBar's .field: the native field in a 28 px box with an 8 px radius, a
// 1 px --line-field border (--ink-2 on hover, --focus with a 1 px --focus ring and a 4 px
// --accent-soft spread while focused), the search ground (--surface, #141a17 in the dark
// theme) and --shadow-sm. Escape clears a search that has text.
type searchField struct {
	kit.TextField
	tb     *TitleBar
	border fade
}

// searchGroundDark is the search field's ground in the dark theme (TitleBar.svelte's
// --search-ground; the light theme takes --surface).
var searchGroundDark = kit.RGB(0x141a17)

func newSearchField(tb *TitleBar) *searchField {
	f := &searchField{tb: tb}
	f.TextField = *kit.NewTextField(kit.TextFieldOptions{})
	f.Style = kit.Style{
		Height: kit.Px(fieldHeight), Border: kit.EdgesAll(kit.Px(1)),
		Padding: kit.Edges{Start: kit.Px(fieldPadStart), End: kit.Px(fieldPadEnd)},
	}
	f.Ring = kit.Ring{None: true} // .field:focus { outline: none }: the border and ring show it
	f.Semantics.Shortcut = kit.KeyLabel("Ctrl+F")
	f.Semantics.AutomationID = "titlebar.search"
	f.border.dur = tokens.Dur2
	f.border.onChange = f.Invalidate
	f.OnChange = func(s string) {
		if tb.OnSearch != nil {
			tb.OnSearch(s)
		}
	}
	return f
}

func (f *searchField) theme() *tokens.Theme {
	if w := f.Window(); w != nil {
		return f.tb.theme.Tokens(w.Environment())
	}
	return f.tb.theme.Tokens(kit.Environment{})
}

func (f *searchField) ground(t *tokens.Theme) kit.Color {
	if t.Dark && !t.IsForced() {
		return searchGroundDark
	}
	return t.Surface
}

// applyOptions gives the native field the theme's colours and the font at the window's text
// size.
func (f *searchField) applyOptions() {
	t := f.theme()
	var s kit.Scale
	if w := f.Window(); w != nil {
		s = w.Environment().Scale
	}
	f.SetOptions(kit.TextFieldOptions{
		Style: fontBody.Style(s), Color: t.Ink, Background: f.ground(t),
		Placeholder: "Search downloads", PlaceholderColor: t.Faint, Name: "Search downloads by name or URL",
	})
}

func (f *searchField) borderColor(t *tokens.Theme) kit.Color {
	switch {
	case f.Focused():
		return t.Focus
	case f.Hovered():
		return t.Ink2
	}
	return t.LineField
}

// Attached is kit.Attacher's: the native field, in the theme's colours.
func (f *searchField) Attached() {
	f.TextField.Attached()
	f.applyOptions()
	f.border.jump(f.borderColor(f.theme()))
}

func (f *searchField) update() { f.border.moveTo(f.Window(), f.borderColor(f.theme())) }

func (f *searchField) resetColors() { f.border.jump(f.borderColor(f.theme())) }

// HoverChanged is kit.HoverHandler's.
func (f *searchField) HoverChanged(h bool) {
	f.TextField.HoverChanged(h)
	f.update()
}

// FocusChanged is kit.FocusHandler's.
func (f *searchField) FocusChanged(focused bool) {
	f.TextField.FocusChanged(focused)
	f.update()
}

// HandleKey is kit.KeyHandler's: Escape clears a search that has text (and is the field's
// otherwise).
func (f *searchField) HandleKey(e kit.KeyEvent) bool {
	if e.Action == kit.KeyDown && e.Key == kit.KeyEscape && e.Mods == 0 && f.Text() != "" {
		f.SetText("")
		if f.OnChange != nil {
			f.OnChange("")
		}
		return true
	}
	return false
}

// Paint is kit.Painter's: shadow, ground, border, and the focus look.
func (f *searchField) Paint(p *kit.PaintContext) {
	t := f.theme()
	s := p.Scale
	box := kit.RRect{Rect: s.SnapRect(kit.RectAt(p.Origin, p.Size())), Radii: kit.UniformRadii(8)}
	focused := f.Focused()
	switch {
	case focused && !t.IsForced():
		// box-shadow: 0 0 0 1px var(--focus), 0 0 0 4px var(--accent-soft): CSS paints the
		// first on top, so the spread of 4 goes first.
		p.Canvas.DrawShadow(box, kit.Shadow{Spread: 4, Color: t.AccentSoft})
		p.Canvas.DrawShadow(box, kit.Shadow{Spread: 1, Color: t.Focus})
	default:
		for i := len(t.ShadowSm) - 1; i >= 0; i-- {
			p.Canvas.DrawShadow(box, t.ShadowSm[i])
		}
	}
	p.Canvas.FillRoundedRect(box, f.ground(t))
	w := s.SnapWidth(1)
	p.Canvas.StrokeRoundedRect(box.Inset(w/2), kit.Stroke{Width: w}, f.border.cur)
	if focused && t.IsForced() {
		// .field:focus { outline: 2px solid Highlight; outline-offset: 1px }
		o := s.SnapWidth(2)
		p.Canvas.StrokeRoundedRect(box.Inset(-1-o/2), kit.Stroke{Width: o}, t.Focus)
	}
	f.PaintStandIn(p) // the text, while a modal fades the search (TitleBar.SetModal)
}
