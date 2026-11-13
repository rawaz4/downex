package widgets

import (
	"downex/ui/kit"
	"downex/ui/kit/tokens"
)

// Variant is a button's look (app.css §3.1: .btn and its modifiers).
type Variant uint8

// The button variants.
const (
	// Secondary is .btn: --raised with a --line-strong border and --shadow-sm, --ink-2 text.
	Secondary Variant = iota
	// Primary is .btn.primary: --accent with --accent-on text (600) and the primary lift.
	Primary
	// Ghost is .btn.ghost: no border, ground or shadow until hovered.
	Ghost
	// Danger is .btn.danger: --danger with --danger-on text (600).
	Danger
	// DangerText is .btn.danger-text: a ghost button in --danger.
	DangerText
	// Link is .btn.link: an action inside text (--accent-text, 600, underlined on hover).
	Link
)

// ButtonSize is a button's height (app.css .btn, .btn.sm, .btn.lg, and Dialog's footer).
type ButtonSize uint8

// The button sizes.
const (
	SizeMd   ButtonSize = iota // 30 px, 11 px padding, 7 px gap, --type-body
	SizeSm                     // 26 px, 9 px padding, 6 px gap, --type-cell
	SizeLg                     // 32 px, 13 px padding
	SizeFoot                   // 32 px with md padding: a dialog footer's (.foot .btn)
)

// The look states a control's colours depend on.
const (
	stRest  uint8 = 0
	stHover uint8 = 1 << 0
	stDown  uint8 = 1 << 1
)

// control is what buttons share: the kit's button behaviour, a look that follows hover and
// press with app.css's transitions, the pointer cursor, and an optional icon and label.
type control struct {
	kit.Pressable
	look transition
	// pressByKeyOnly marks a look whose :hover outranks :active (ghost, danger, danger-text:
	// their hover rules are more specific than .btn:active), so a pointer press shows the hover.
	pressByKeyOnly bool
	// onFocus hears the control getting and losing the focus (a notice holds its lifetime).
	onFocus func(bool)
}

func (c *control) init(onPress func()) {
	c.OnPress = onPress
	c.Focus = kit.FocusTab
	c.Semantics.Role = kit.RoleButton
	c.Cursor = kit.CursorPointer
}

// lookState is the state the control shows now.
func (c *control) lookState() uint8 {
	if !c.usable() {
		return stRest
	}
	s := stRest
	if c.Hovered() {
		s |= stHover
	}
	if c.Down() {
		s |= stDown
	}
	return s
}

func (c *control) usable() bool { return !c.Disabled }

func (c *control) update() {
	s := c.lookState()
	d := tokens.Dur2
	if s&stDown != 0 {
		d = tokens.Dur1 // .btn:active { transition-duration: var(--dur-1) }
	}
	c.look.moveTo(&c.Element, s, d)
}

// Attached is kit.Attacher's: the resting look at once.
func (c *control) Attached() { c.look.jump(&c.Element, c.lookState()) }

// HandlePointer is kit.PointerHandler's.
func (c *control) HandlePointer(in *kit.PointerInput) bool {
	used := c.Pressable.HandlePointer(in)
	c.update()
	return used
}

// HandleKey is kit.KeyHandler's (Space shows the press).
func (c *control) HandleKey(e kit.KeyEvent) bool {
	used := c.Pressable.HandleKey(e)
	c.update()
	return used
}

// HoverChanged is kit.HoverHandler's.
func (c *control) HoverChanged(h bool) {
	c.Pressable.HoverChanged(h)
	c.update()
}

// FocusChanged is kit.FocusHandler's.
func (c *control) FocusChanged(f bool) {
	c.Pressable.FocusChanged(f)
	c.update()
	if c.onFocus != nil {
		c.onFocus(f)
	}
}

// SetDisabled makes the control unavailable. focusable keeps it focusable (aria-disabled,
// which the design uses so that the focus is not lost when a button becomes unavailable);
// otherwise it is HTML's disabled.
func (c *control) SetDisabled(disabled, focusable bool) {
	if c.Disabled == disabled && c.DisabledFocusable == focusable {
		return
	}
	c.Disabled, c.DisabledFocusable = disabled, focusable
	c.Cursor = kit.CursorPointer
	if disabled {
		c.Cursor = kit.CursorDefault
	}
	if w := c.Window(); w != nil {
		w.UpdateCursor()
	}
	c.look.jump(&c.Element, c.lookState())
	c.SemanticsChanged()
}

// Button is app.css's .btn: an optional icon and a label in one of the variants and sizes.
type Button struct {
	control
	Variant Variant
	Size    ButtonSize
	// Pressed shows the button as toggled on (aria-pressed: the More menu's trigger while open
	// uses Expanded instead).
	icon  *Icon
	label *kit.Label
	shape buttonShape
}

// buttonShape overrides a button's corners for a split button's halves: their own radii, no
// lift of their own (the pair has one), and the 1 px --split-line at the inline start.
type buttonShape struct {
	custom     bool
	radii      kit.Radii
	noLift     bool
	splitStart bool
}

// NewButton makes a button with an icon named as Icon.svelte names it ("" for none).
func NewButton(text, icon string, v Variant, size ButtonSize, onPress func()) *Button {
	b := &Button{Variant: v, Size: size}
	b.init(onPress)
	b.pressByKeyOnly = v == Ghost || v == Danger || v == DangerText
	if n, ok := iconOf(icon); ok {
		b.icon = NewIcon(n, 16, b.iconColor)
		b.Add(b.icon)
	}
	b.label = label(text, b.font())
	b.label.Style.Flex = kit.FlexOf(0, 1, kit.Length{})
	if text != "" {
		b.Add(b.label)
	}
	b.applyStyle()
	return b
}

// Label is the button's label.
func (b *Button) Label() *kit.Label { return b.label }

// SetText changes the label.
func (b *Button) SetText(s string) {
	if s != "" && b.label.Parent() == nil {
		b.Add(b.label)
	}
	b.label.SetText(s)
	b.label.SetHidden(s == "")
}

// IconWidget is the button's icon, or nil.
func (b *Button) IconWidget() *Icon { return b.icon }

func (b *Button) font() kit.Font {
	f := FontButton
	if b.Size == SizeSm {
		f.Size = tokens.TypeCell
	}
	if b.Variant == Primary || b.Variant == Danger || b.Variant == Link {
		f.Weight = 600
	}
	return f
}

func (b *Button) applyStyle() {
	h, pad, gap := kit.DIP(30), kit.DIP(11), kit.DIP(7)
	switch b.Size {
	case SizeSm:
		h, pad, gap = 26, 9, 6
	case SizeLg:
		h, pad = 32, 13
	case SizeFoot:
		h = 32
	}
	st := kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Justify: kit.JustifyCenter, Flex: kit.FlexNone,
		Height: kit.Px(h), MinHeight: kit.Px(h), Padding: kit.EdgesInline(kit.Px(pad)), ColumnGap: kit.Px(gap),
		Border: kit.EdgesAll(kit.Px(1)), Radius: kit.CornersAll(tokens.RMd),
	}
	if b.Variant == Link {
		st.Height, st.MinHeight, st.Padding, st.Border = kit.Length{}, kit.Px(0), kit.Edges{}, kit.Edges{}
		st.Radius = kit.CornersAll(tokens.RXs)
	}
	b.Style = st
	b.label.SetFont(b.font())
}

// SetVariant changes the look.
func (b *Button) SetVariant(v Variant) {
	if v == b.Variant {
		return
	}
	b.Variant = v
	b.pressByKeyOnly = v == Ghost || v == Danger || v == DangerText
	b.applyStyle()
	b.Relayout()
	b.Invalidate()
}

// colors are the button's ground, border and text in a state.
func (b *Button) colors(t *tokens.Theme, s uint8) (bg, border, ink kit.Color) {
	hover, down := s&stHover != 0, s&stDown != 0
	usable := !b.Disabled
	switch b.Variant {
	case Primary:
		bg, ink = t.Accent, t.AccentOn
		switch {
		case usable && down:
			bg = t.AccentPress
		case usable && hover:
			bg = t.AccentHover
		}
	case Ghost:
		ink = t.Ink2
		switch {
		case usable && hover:
			bg = t.Hover // .btn.ghost:hover outranks .btn:active
		case usable && down:
			bg = t.Press
		}
	case Danger:
		bg, ink = t.Danger, t.DangerOn
		switch {
		case usable && hover:
			bg = kit.Mix(t.Danger, 0.88, t.Ink)
		case usable && down:
			bg = t.Press
		}
	case DangerText:
		ink = t.Danger
		switch {
		case usable && hover:
			bg = t.DangerSoft
		case usable && down:
			bg = t.Press
		}
	case Link:
		ink = t.AccentText
	default:
		bg, border, ink = t.Raised, t.LineStrong, t.Ink2
		switch {
		case usable && down:
			bg = t.Press
		case usable && hover:
			bg = t.Hover
		}
	}
	if b.Disabled && b.Variant != Primary && b.Variant != Danger {
		ink = t.Faint
	}
	if t.IsForced() && b.Variant != Link {
		border = t.Ink // forced colours: border 1px solid ButtonText
	}
	return bg, border, ink
}

func (b *Button) iconColor(t *tokens.Theme) kit.Color {
	_, _, ink := b.colors(t, b.look.state())
	if b.Variant == Secondary || b.Variant == Ghost {
		if b.Disabled {
			return t.Faint
		}
		return t.Muted // .btn .icon { color: var(--muted) }
	}
	return ink
}

// Paint is kit.Painter's: shadow, ground and border, the label's colour, and the opacity of
// a disabled primary or danger button (pushed here, popped in PaintOver).
func (b *Button) Paint(p *kit.PaintContext) {
	t := tokensOf(&b.Element)
	fb, fbo, fi := b.colors(t, b.look.fromState())
	tb, tbo, ti := b.colors(t, b.look.state())
	bg, border, ink := b.look.mix(fb, tb), b.look.mix(fbo, tbo), b.look.mix(fi, ti)
	if b.fades() {
		p.Canvas.PushOpacity(0.55)
	}
	r := snapBox(p, 8)
	switch {
	case b.shape.custom:
		r.Radii = b.shape.radii
		r = r.Normalized()
	case b.Variant == Link:
		r = snapBox(p, 4)
	}
	switch {
	case b.shape.noLift:
	case b.Variant == Secondary:
		drawShadows(p.Canvas, r, t.ShadowSm)
	case b.Variant == Primary:
		drawShadows(p.Canvas, r, t.PrimaryLift)
	}
	if bg.A > 0 {
		p.Canvas.FillRoundedRect(r, bg)
	}
	if b.Variant == Primary && !b.shape.noLift && !t.IsForced() {
		drawInsetShadows(p.Canvas, r.Inset(p.Scale.SnapWidth(1)), t.PrimaryLift)
	}
	if b.shape.splitStart {
		w := p.Scale.SnapWidth(1)
		x := r.Rect.Min.X
		p.Canvas.FillRect(kit.R(x, r.Rect.Min.Y, x+w, r.Rect.Max.Y), t.SplitLine)
	} else {
		strokeInside(p.Canvas, r, p.Scale.SnapWidth(1), border)
	}
	b.label.SetColor(ink)
	if b.icon != nil && b.Disabled && (b.Variant == Secondary || b.Variant == Ghost) {
		b.icon.Opacity = 0.7
	} else if b.icon != nil {
		b.icon.Opacity = 0
	}
}

// fades reports whether the button is drawn at 55% (a disabled primary or danger button).
func (b *Button) fades() bool {
	return b.Disabled && (b.Variant == Primary || b.Variant == Danger)
}

// PaintOver is kit.OverPainter's: the link's underline on hover, and the end of the opacity.
func (b *Button) PaintOver(p *kit.PaintContext) {
	if b.Variant == Link && b.Hovered() && !b.Disabled && b.label.TextLayout() != nil {
		t := tokensOf(&b.Element)
		m := b.label.TextLayout().Metrics()
		o := b.label.Bounds().Min.Add(b.label.TextOrigin())
		y := p.Scale.Snap(o.Y + m.Baseline + 2) // text-underline-offset: 2px
		w := p.Scale.SnapWidth(1)
		p.FillRect(kit.R(o.X, y, o.X+m.Width, y+w), t.AccentText)
	}
	if b.fades() {
		p.Canvas.Pop()
	}
}

// IconButton is app.css's .icon-btn: a square button with one icon in --muted (--ink on
// hover), 30 px with 8 px corners or 26 px with 6 px (sm). It names itself by its aria-label
// and shows it as its tooltip when asked.
type IconButton struct {
	control
	Small bool
	// faint rests in --faint (a toast's dismiss button), hovered --ink.
	faint bool
	// Pressed and Expanded are aria-pressed and aria-expanded: they show the --press ground.
	Pressed, Expanded bool
	icon              *Icon
}

// NewIconButton makes an icon button named name (its aria-label) with the icon (Icon.svelte's
// name) at size px.
func NewIconButton(icon string, size kit.DIP, name string, small bool, onPress func()) *IconButton {
	b := &IconButton{Small: small}
	b.init(onPress)
	b.Semantics.Name = name
	side, rad := kit.DIP(30), tokens.RMd
	if small {
		side, rad = 26, tokens.RSm
	}
	b.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Justify: kit.JustifyCenter, Flex: kit.FlexNone,
		Width: kit.Px(side), Height: kit.Px(side), Border: kit.EdgesAll(kit.Px(1)), Radius: kit.CornersAll(rad),
	}
	n, _ := iconOf(icon)
	b.icon = NewIcon(n, size, b.iconColor)
	b.Add(b.icon)
	return b
}

// SetPressed shows the button toggled on or off (aria-pressed).
func (b *IconButton) SetPressed(on bool) {
	if on != b.Pressed {
		b.Pressed = on
		b.Semantics.Toggle = kit.ToggleOff
		if on {
			b.Semantics.Toggle = kit.ToggleOn
		}
		b.Invalidate()
		b.SemanticsChanged()
	}
}

// SetExpanded shows that the button's menu is open (aria-expanded).
func (b *IconButton) SetExpanded(open bool) {
	if open != b.Expanded {
		b.Expanded = open
		b.Invalidate()
	}
}

func (b *IconButton) colors(t *tokens.Theme, s uint8) (bg, ink kit.Color) {
	ink = t.Muted
	if b.faint {
		ink = t.Faint
	}
	if b.Disabled {
		return kit.Color{}, t.Faint
	}
	switch {
	case s&stDown != 0:
		bg, ink = t.Press, t.Ink
	case s&stHover != 0:
		bg, ink = t.Hover, t.Ink
	}
	if b.Pressed || b.Expanded {
		bg, ink = t.Press, t.Ink
	}
	return bg, ink
}

func (b *IconButton) iconColor(t *tokens.Theme) kit.Color {
	_, fi := b.colors(t, b.look.fromState())
	_, ti := b.colors(t, b.look.state())
	return b.look.mix(fi, ti)
}

// Paint is kit.Painter's.
func (b *IconButton) Paint(p *kit.PaintContext) {
	t := tokensOf(&b.Element)
	fb, _ := b.colors(t, b.look.fromState())
	tb, _ := b.colors(t, b.look.state())
	rad := kit.DIP(8)
	if b.Small {
		rad = 6
	}
	if bg := b.look.mix(fb, tb); bg.A > 0 {
		p.Canvas.FillRoundedRect(snapBox(p, rad), bg)
	}
	b.icon.Opacity = 0
	if b.Disabled {
		b.icon.Opacity = 0.7
	}
}

// FillSemantics is kit.SemanticFiller's: the menu it opens.
func (b *IconButton) FillSemantics(n *kit.SemanticNode) {
	b.Pressable.FillSemantics(n)
	if b.Expanded {
		n.Expand = kit.ExpandOpen
	}
}

// SetIconSize sets the icon's size in px (16 by default; the page header's are 15).
func (b *Button) SetIconSize(size kit.DIP) {
	if b.icon == nil {
		return
	}
	b.icon.size = size
	b.icon.Style.Width, b.icon.Style.Height = kit.Px(size), kit.Px(size)
	b.icon.Relayout()
}
