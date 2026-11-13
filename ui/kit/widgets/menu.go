package widgets

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
)

// MenuItemKind is what a menu item is (ContextMenu.svelte's MenuItem.kind).
type MenuItemKind uint8

// The item kinds.
const (
	ItemPlain    MenuItemKind = iota // menuitem
	ItemRadio                        // menuitemradio: a Mint check in the gutter when Checked
	ItemCheckbox                     // menuitemcheckbox
)

// MenuItem is one item of a menu (ContextMenu.svelte's MenuItem).
type MenuItem struct {
	ID    string
	Label string
	// Icon is Icon.svelte's name for the item's icon, "" for none.
	Icon string
	// Disabled items stay in the menu (with Title as the reason) but the keyboard skips them
	// and a click does nothing.
	Disabled bool
	Danger   bool
	// Shortcut is a key cap at the end ("Ctrl+N").
	Shortcut string
	// Separated draws a separator above the item.
	Separated bool
	Kind      MenuItemKind
	Checked   bool
	// Heading is a caps heading above the item: it starts a group named by it.
	Heading string
	// Note is secondary text at the end in --faint ("Ctrl L", "now").
	Note  string
	Title string
}

// MenuAlign is the edge of its anchor a menu lines up with.
type MenuAlign uint8

// The alignments.
const (
	AlignToStart MenuAlign = iota
	AlignToEnd
)

// MenuDirection is where an anchored menu opens.
type MenuDirection uint8

// The directions.
const (
	OpenDown MenuDirection = iota
	OpenUp
)

// InitialFocus is which item has the focus when a menu opens.
type InitialFocus uint8

// The initial focus choices.
const (
	FocusFirst InitialFocus = iota
	FocusLast
	FocusChecked
)

// MenuAnchor opens a menu next to an element (ContextMenu.svelte's MenuAnchor): below or above
// it, lined up with its start or end edge, flipped when there is no room; a press on the
// element is its own (a trigger toggles its menu), not a press outside the menu.
type MenuAnchor struct {
	El        kit.Widget
	Align     MenuAlign
	Direction MenuDirection
}

// MenuOptions describe a menu.
type MenuOptions struct {
	Items []MenuItem
	// Label is the menu's accessible name.
	Label string
	// Footnote is small print under a separator (11.5 px --faint); it also describes the menu.
	Footnote string
	// At is the point a context menu opens at (owner window DIPs), unless Anchor is set.
	At     kit.Point
	Anchor *MenuAnchor
	// Initial is the item that has the focus at first.
	Initial InitialFocus
	// OnSelect hears a click on an enabled item (or Enter or Space on it). The menu stays open:
	// its owner closes it.
	OnSelect func(id string)
	// OnClose hears Escape, Tab, a press outside, the window losing the activation, moving or
	// resizing, or the wheel turning outside: the owner closes the menu (a nil OnClose closes
	// it itself).
	OnClose func()
}

// The menu's placement margins (ContextMenu.svelte's MARGIN and GAP, and the 4 px of a menu
// at a point).
const (
	menuMargin = 8
	menuGap    = 4
)

// PlaceMenuAt is where a context menu of size opens at p in a viewport of size view: at p,
// kept 4 px inside the viewport.
func PlaceMenuAt(p kit.Point, size, view kit.Size) kit.Point {
	return kit.Pt(max(4, min(p.X, view.W-size.W-4)), max(4, min(p.Y, view.H-size.H-4)))
}

// PlaceMenuAnchored is where a menu of size opens next to anchor (ContextMenu.svelte's
// placeAnchored): below it (or above, as dir asks), 4 px away, at its start or end edge (end
// is the left edge in a right-to-left layout), flipped when there is no room, kept 8 px
// inside the viewport.
func PlaceMenuAnchored(anchor kit.Rect, align MenuAlign, dir MenuDirection, rtl bool, size, view kit.Size) kit.Point {
	atEnd := align == AlignToEnd
	if rtl {
		atEnd = !atEnd
	}
	x := anchor.Min.X
	if atEnd {
		x = anchor.Max.X - size.W
	}
	left := clampf(x, menuMargin, max(menuMargin, view.W-size.W-menuMargin))
	below := anchor.Max.Y + menuGap
	above := anchor.Min.Y - menuGap - size.H
	fitsBelow := below+size.H <= view.H-menuMargin
	fitsAbove := above >= menuMargin
	up := !fitsBelow && fitsAbove
	if dir == OpenUp {
		up = fitsAbove || !fitsBelow
	}
	top := below
	if up {
		top = above
	}
	top = clampf(top, menuMargin, max(menuMargin, view.H-size.H-menuMargin))
	return kit.Pt(left, top)
}

// Menu is an open menu (ContextMenu.svelte, DESIGN-SYSTEM.md §3.3) in a popup window: a
// --raised box (5 px padding, a --line border, 11 px corners, --shadow-lg) of 30 px items
// (a 16 px gutter for an icon or a check, the label, a note and a key cap), separators,
// caps headings and a footnote, 220 to 360 px wide. Arrows, Home and End move between the
// enabled items (wrapping), typing a letter jumps to the next item that starts with it, Esc
// and Tab close it. The owner's window keeps the keyboard and hands the menu its keys.
type Menu struct {
	o      MenuOptions
	owner  *kit.Window
	pop    *popup
	box    *menuBox
	proxy  *keyProxy
	isOpen bool
}

// OpenMenu opens a menu for owner (a widget in the window it opens over).
func OpenMenu(owner kit.Widget, o MenuOptions) (*Menu, error) {
	ow := owner.Base().Window()
	if ow == nil || ow.Host() == nil {
		return nil, ErrNoPopups
	}
	if _, ok := ow.Host().(kit.Popups); !ok {
		return nil, ErrNoPopups
	}
	m := &Menu{o: o, owner: ow}
	theme := ThemeOf(owner.Base())
	view := ow.Size()
	m.box = newMenuBox(m, view)
	size := measure(ow, theme, m.box)
	var at kit.Point
	if o.Anchor != nil && o.Anchor.El != nil {
		at = PlaceMenuAnchored(o.Anchor.El.Base().WindowBounds(), o.Anchor.Align, o.Anchor.Direction, isRTL(ow), size, view)
	} else {
		at = PlaceMenuAt(o.At, size, view)
	}
	pop, err := openPopup(owner, theme, m.box, kit.RectAt(at, size), o.Label, m.dismissed)
	if err != nil {
		return nil, err
	}
	m.pop, m.isOpen = pop, true
	m.proxy = newKeyProxy(pop.deliver)
	m.proxy.show(ow)
	m.box.focusInitial(o.Initial, ow.FocusVisible())
	return m, nil
}

// isRTL reports whether w lays out right to left.
func isRTL(w *kit.Window) bool { return w.Direction() == kit.DirRTL }

// Open reports whether the menu is still open.
func (m *Menu) Open() bool { return m != nil && m.isOpen }

// Root is the menu's popup window (for tests and tools).
func (m *Menu) Root() *kit.Window {
	if m == nil || m.pop == nil {
		return nil
	}
	return m.pop.win
}

// Box is the menu's box (RoleMenu) in its popup window.
func (m *Menu) Box() *kit.Element { return &m.box.Element }

// Bounds is where the menu's box is, in the owner's window DIPs.
func (m *Menu) Bounds() kit.Rect { return m.pop.box }

// Close closes the menu without OnClose. The owner gives the focus back where it belongs.
func (m *Menu) Close() {
	if !m.Open() {
		return
	}
	m.isOpen = false
	m.pop.close()
	m.proxy.hide(m.owner)
}

// Active is the label of the item that has the focus, or "".
func (m *Menu) Active() string {
	if m.box == nil {
		return ""
	}
	if it := m.box.focused(); it != nil {
		return it.item.Label
	}
	return ""
}

// dismissed is the host's OnDismiss: a press on the anchor is the anchor's.
func (m *Menu) dismissed(d kit.Dismiss) {
	if !m.Open() {
		return
	}
	if d.Reason == kit.DismissPress && m.o.Anchor != nil && m.o.Anchor.El != nil {
		if a := m.o.Anchor.El.Base(); a.Window() == m.owner && a.WindowBounds().Contains(d.Pos) {
			return
		}
	}
	m.closeAsked()
}

// closeAsked tells the owner (in its own call) that the menu should close.
func (m *Menu) closeAsked() {
	post(m.owner, func() {
		if !m.Open() {
			return
		}
		if m.o.OnClose != nil {
			m.o.OnClose()
			return
		}
		m.Close()
	})
}

// selected tells the owner (in its own call) that an item was chosen.
func (m *Menu) selected(id string) {
	post(m.owner, func() {
		if m.Open() && m.o.OnSelect != nil {
			m.o.OnSelect(id)
		}
	})
}

// post runs f as a call into w: from a popup's call, the owner's root is not in one.
func post(w *kit.Window, f func()) {
	if w == nil {
		return // a preview: nothing to tell
	}
	if err := w.Post(f); err != nil {
		return // the window is closed: nothing to tell
	}
}

// activeChanged is called when the item that has the focus changes. The owner's key proxy
// only takes the menu's keys: it stays out of the semantic tree (RoleNone, no name), since
// the menu's popup window shows the menu and its active item to UI Automation itself (its
// own provider, ui/win popup_uia.go). Before, the proxy also stood for the active item, so
// a client met each focused item twice, the second time as a 0 × 0 element of the owner.
func (m *Menu) activeChanged(*menuItem) {}

// menuBox is the menu's box in its popup window.
type menuBox struct {
	kit.Element
	m     *Menu
	items []*menuItem
}

func newMenuBox(m *Menu, view kit.Size) *menuBox {
	b := &menuBox{m: m}
	b.Semantics.Role = kit.RoleMenu
	b.Semantics.Name = m.o.Label
	b.Semantics.Description = m.o.Footnote
	b.Focus = kit.FocusClick
	b.Ring = kit.Ring{None: true}
	b.Style = kit.Style{
		Display: kit.DisplayBlock, Padding: kit.EdgesAll(kit.Px(5)), Border: kit.EdgesAll(kit.Px(1)),
		Radius: kit.CornersAll(kit.Px(11)), MinWidth: kit.Px(220), MaxWidth: kit.Px(min(360, view.W-16)),
	}
	body := &kit.Element{Style: kit.Style{Display: kit.DisplayBlock}}
	var group *kit.Element
	for i, it := range m.o.Items {
		starts := i == 0 || it.Heading != "" || it.Separated
		if starts {
			if it.Separated && i > 0 {
				body.Add(newSeparator())
			}
			group = body
			if it.Heading != "" {
				g := &kit.Element{Style: kit.Style{Display: kit.DisplayBlock}}
				g.Semantics.Role = kit.RoleGroup
				g.Semantics.Name = it.Heading
				h := kit.NewLabel(strings.ToUpper(it.Heading), fontCaps, kit.TextOptions{Truncate: kit.TruncateEnd})
				h.Semantics.Role = kit.RoleNone
				h.Style.Padding = kit.Edges{Top: kit.Px(9), End: kit.Px(9), Bottom: kit.Px(7), Start: kit.Px(9)}
				g.Add(&menuHeading{Label: h})
				body.Add(g)
				group = g
			}
		}
		mi := newMenuItem(b, it)
		b.items = append(b.items, mi)
		group.Add(mi)
	}
	if m.o.Footnote != "" {
		body.Add(newSeparator())
		body.Add(newFootnote(m.o.Footnote))
	}
	pane := NewScrollPane(body)
	pane.Style.MaxHeight = kit.Px(view.H - 16 - 12)
	b.Add(pane)
	return b
}

// Paint is kit.Painter's: --shadow-lg, --raised and the --line border (CanvasText in forced
// colours).
func (b *menuBox) Paint(p *kit.PaintContext) {
	t := tokensOf(&b.Element)
	r := snapBox(p, 11)
	drawShadows(p.Canvas, r, t.ShadowLg)
	p.Canvas.FillRoundedRect(r, t.Raised)
	border := t.Line
	if t.IsForced() {
		border = t.Ink
	}
	strokeInside(p.Canvas, r, p.Scale.SnapWidth(1), border)
}

// enabled are the items the keyboard moves between.
func (b *menuBox) enabled() []*menuItem {
	out := make([]*menuItem, 0, len(b.items))
	for _, it := range b.items {
		if !it.item.Disabled {
			out = append(out, it)
		}
	}
	return out
}

func (b *menuBox) focused() *menuItem {
	w := b.Window()
	if w == nil {
		return nil
	}
	for _, it := range b.items {
		if w.Focused() == &it.Element {
			return it
		}
	}
	return nil
}

// focusAt focuses the enabled item at index (wrapping; the box itself when there is none).
func (b *menuBox) focusAt(index int) {
	w := b.Window()
	if w == nil {
		return
	}
	list := b.enabled()
	if len(list) == 0 {
		w.Focus(b)
		return
	}
	i := ((index % len(list)) + len(list)) % len(list)
	w.Focus(list[i])
	b.revealFocused()
}

func (b *menuBox) revealFocused() {
	if it := b.focused(); it != nil {
		if pane, ok := b.Children()[0].Widget().(*ScrollPane); ok {
			pane.Show(&it.Element)
		}
	}
}

func (b *menuBox) focusInitial(f InitialFocus, visible bool) {
	if w := b.Window(); w != nil {
		w.SetFocusVisible(visible)
	}
	switch f {
	case FocusLast:
		b.focusAt(-1)
	case FocusChecked:
		at := 0
		for i, it := range b.enabled() {
			if it.item.Kind != ItemPlain && it.item.Checked {
				at = i
				break
			}
		}
		b.focusAt(at)
	default:
		b.focusAt(0)
	}
}

func (b *menuBox) current() int {
	f := b.focused()
	for i, it := range b.enabled() {
		if it == f {
			return i
		}
	}
	return -1
}

// HandleKey is kit.KeyHandler's: the menu's keyboard model.
func (b *menuBox) HandleKey(e kit.KeyEvent) bool {
	if e.Action != kit.KeyDown {
		return false
	}
	cur := b.current()
	switch e.Key {
	case kit.KeyArrowDown:
		b.focusAt(cur + 1)
	case kit.KeyArrowUp:
		if cur < 0 {
			b.focusAt(-1)
		} else {
			b.focusAt(cur - 1)
		}
	case kit.KeyHome:
		b.focusAt(0)
	case kit.KeyEnd:
		b.focusAt(-1)
	case kit.KeyEscape, kit.KeyTab:
		b.m.closeAsked()
	default:
		return false
	}
	return true
}

// HandleText is kit.TextHandler's: type-ahead, the next item (after the focused one,
// wrapping) whose label starts with the letter typed.
func (b *menuBox) HandleText(e kit.TextEvent) bool {
	if utf8.RuneCountInString(e.Text) != 1 {
		return false
	}
	r, _ := utf8.DecodeRuneInString(e.Text)
	if unicode.IsSpace(r) || unicode.IsControl(r) {
		return false
	}
	k := strings.ToLower(e.Text)
	list := b.enabled()
	start := b.current() + 1
	for n := range len(list) {
		it := list[(start+n)%len(list)]
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(it.item.Label)), k) {
			if w := b.Window(); w != nil {
				w.Focus(it)
				b.revealFocused()
			}
			break
		}
	}
	return true
}

// menuItem is one item: a button with tabindex -1.
type menuItem struct {
	kit.Element
	b     *menuBox
	item  MenuItem
	glyph *Icon
	label *kit.Label
	note  *kit.Label
	kbd   *Kbd
	look  transition
	down  bool
	space bool
}

func newMenuItem(b *menuBox, it MenuItem) *menuItem {
	mi := &menuItem{b: b, item: it}
	mi.Focus = kit.FocusClick
	mi.Ring = kit.Ring{Offset: kit.Px(-2)}
	mi.Cursor = kit.CursorPointer
	mi.Tooltip = it.Title
	switch it.Kind {
	case ItemRadio:
		mi.Semantics.Role = kit.RoleMenuItemRadio
	case ItemCheckbox:
		mi.Semantics.Role = kit.RoleMenuItemCheckBox
	default:
		mi.Semantics.Role = kit.RoleMenuItem
	}
	if it.Disabled {
		mi.Disabled, mi.DisabledFocusable = true, true
		mi.Cursor = kit.CursorDefault
	}
	mi.Semantics.Shortcut = kit.KeyLabel(it.Shortcut)
	mi.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(9), Width: kit.Pct(100),
		MinHeight: kit.Px(30), Padding: kit.EdgesInline(kit.Px(9)), Radius: kit.CornersAll(kit.Px(7)),
	}
	checkable := it.Kind != ItemPlain
	glyph := box(kit.Style{Width: kit.Px(16), Flex: kit.FlexNone, Display: kit.DisplayFlex, Justify: kit.JustifyCenter, AlignItems: kit.AlignCenter})
	glyph.Semantics.Hidden = true
	switch {
	case checkable && it.Checked:
		mi.glyph = NewIcon(icons.Check, 14, func(t *tokens.Theme) kit.Color { return t.Accent })
		glyph.Add(mi.glyph)
	case !checkable && it.Icon != "":
		ic, _ := iconOf(it.Icon)
		mi.glyph = NewIcon(ic, 16, func(t *tokens.Theme) kit.Color {
			if it.Danger && !it.Disabled {
				return t.Danger // .item.danger .glyph { color: inherit }
			}
			return t.Muted
		})
		glyph.Add(mi.glyph)
	}
	if mi.glyph != nil && it.Disabled {
		mi.glyph.Opacity = 0.7
	}
	mi.Add(glyph)
	f := FontBody
	if checkable && it.Checked {
		f.Weight = 600
	}
	mi.label = label(it.Label, f)
	mi.label.Style.Flex = kit.Flex1
	mi.Add(mi.label)
	if it.Note != "" {
		mi.note = label(it.Note, font(tokens.TypeSmall, 400))
		mi.note.Style.Flex = kit.FlexNone
		mi.note.Style.Margin = kit.Edges{Start: kit.Px(12)}
		mi.Add(mi.note)
	}
	if it.Shortcut != "" {
		mi.kbd = NewKbd(it.Shortcut)
		mi.kbd.Semantics.Hidden = false
		mi.kbd.Style.Margin = kit.Edges{Start: kit.Px(12)}
		mi.Add(mi.kbd)
	}
	return mi
}

func (mi *menuItem) lit() bool { return (mi.Hovered() && !mi.item.Disabled) || mi.Focused() }

// Paint is kit.Painter's: --sel with a --sel-line edge while hovered or focused (Highlight in
// forced colours), and the texts' colours.
func (mi *menuItem) Paint(p *kit.PaintContext) {
	t := tokensOf(&mi.Element)
	r := snapBox(p, 7)
	lit := mi.lit()
	if lit {
		if t.IsForced() {
			p.Canvas.FillRoundedRect(r, t.Focus)
		} else {
			p.Canvas.FillRoundedRect(r, t.Sel)
			strokeInside(p.Canvas, r, p.Scale.SnapWidth(1), t.SelLine)
		}
	}
	ink := t.Ink2
	switch {
	case mi.item.Disabled:
		ink = t.Faint
	case mi.item.Danger:
		ink = t.Danger
	case lit || (mi.item.Kind != ItemPlain && mi.item.Checked):
		ink = t.Ink
	}
	if lit && t.IsForced() {
		ink = t.AccentOn // HighlightText
	}
	mi.label.SetColor(ink)
	if mi.note != nil {
		c := t.Faint
		if mi.Focused() {
			c = t.Muted
		}
		mi.note.SetColor(c)
	}
}

func (mi *menuItem) choose() {
	if mi.item.Disabled {
		return
	}
	mi.b.m.selected(mi.item.ID)
}

// HoverChanged is kit.HoverHandler's.
func (mi *menuItem) HoverChanged(bool) { mi.Invalidate() }

// FocusChanged is kit.FocusHandler's: the owner's proxy follows.
func (mi *menuItem) FocusChanged(f bool) {
	mi.space = false
	mi.Invalidate()
	if f {
		mi.b.m.activeChanged(mi)
	}
}

// HandlePointer is kit.PointerHandler's: a click chooses the item.
func (mi *menuItem) HandlePointer(in *kit.PointerInput) bool {
	inside := kit.RectAt(kit.Point{}, mi.Size()).Contains(in.Local)
	switch in.Action {
	case kit.PointerDown:
		if in.Button != kit.ButtonLeft {
			return false
		}
		mi.down = true
		mi.Window().Capture(&mi.Element)
		return true
	case kit.PointerUp:
		if !mi.down || in.Button != kit.ButtonLeft {
			return false
		}
		mi.down = false
		if inside {
			mi.choose()
		}
		return true
	case kit.PointerCancel:
		mi.down = false
	}
	return false
}

// HandleKey is kit.KeyHandler's: Enter chooses as it goes down, Space as it comes up (a
// button's keys).
func (mi *menuItem) HandleKey(e kit.KeyEvent) bool {
	if e.Mods&(kit.ModCtrl|kit.ModAlt|kit.ModMeta) != 0 {
		return false
	}
	switch {
	case e.Key == kit.KeyEnter && e.Action == kit.KeyDown:
		mi.choose()
		return true
	case e.Key == kit.KeySpace && e.Action == kit.KeyDown:
		mi.space = true
		return true
	case e.Key == kit.KeySpace && e.Action == kit.KeyUp && mi.space:
		mi.space = false
		mi.choose()
		return true
	}
	return false
}

// FillSemantics is kit.SemanticFiller's.
func (mi *menuItem) FillSemantics(n *kit.SemanticNode) {
	if mi.item.Kind != ItemPlain {
		n.Toggle = kit.ToggleOff
		if mi.item.Checked {
			n.Toggle = kit.ToggleOn
		}
	}
	n.Actions |= kit.ActionInvoke
}

// PerformAction is kit.ActionHandler's.
func (mi *menuItem) PerformAction(r kit.ActionRequest) error {
	if r.Action != kit.ActionInvoke {
		return kit.ErrUnsupported
	}
	if mi.item.Disabled {
		return kit.ErrNotEnabled
	}
	mi.choose()
	return nil
}

// menuHeading is a group's caps heading (9 9 7 px padding, --type-caps 600, +0.07em, --faint).
type menuHeading struct {
	*kit.Label
}

// Paint is kit.Painter's.
func (h *menuHeading) Paint(p *kit.PaintContext) {
	h.Label.SetColor(tokensOf(&h.Label.Element).Faint)
	h.Label.Paint(p)
}

// separator is a menu's 1 px --line separator (5 px 4 px margins).
type separator struct{ kit.Element }

func newSeparator() *separator {
	s := &separator{}
	s.Semantics.Role = kit.RoleSeparator
	s.Style = kit.Style{Height: kit.Px(1), Margin: kit.Edges{Top: kit.Px(5), Bottom: kit.Px(5), Start: kit.Px(4), End: kit.Px(4)}}
	return s
}

// Paint is kit.Painter's.
func (s *separator) Paint(p *kit.PaintContext) {
	p.FillRect(kit.RectAt(kit.Point{}, p.Size()), tokensOf(&s.Element).Line)
}

// footnote is a menu's small print: 11.5 px --faint, line-height 1.4, at most 30ch wide,
// wrapping (4 9 6 px padding).
type footnote struct {
	kit.Element
	label *kit.Label
	ch    kit.DIP
	chOf  kit.TextStyle
	zero  kit.TextLayout
}

func newFootnote(text string) *footnote {
	f := &footnote{}
	f.Semantics.Role = kit.RoleText
	f.Style = kit.Style{Padding: kit.Edges{Top: kit.Px(4), End: kit.Px(9), Bottom: kit.Px(6), Start: kit.Px(9)}, AlignSelf: kit.AlignStart}
	f.label = kit.NewLabel(text, font(tokens.TypeMeta, 400), kit.TextOptions{Wrap: kit.TextWrapWords})
	f.label.Semantics.Role = kit.RoleNone
	f.Add(f.label)
	return f
}

// limit is 30ch: thirty widths of the font's "0".
func (f *footnote) limit(ctx *kit.LayoutContext) kit.DIP {
	st := f.label.Font().Style(ctx.Scale)
	if f.zero == nil || st != f.chOf {
		if f.zero != nil {
			f.zero.Release()
			f.zero = nil
		}
		if ctx.Text == nil {
			return 0
		}
		z, err := ctx.Text.LayoutText("0", st, kit.TextOptions{})
		if err != nil {
			return 0
		}
		f.zero, f.chOf = z, st
		f.ch = z.Metrics().Advance
		if f.ch <= 0 {
			f.ch = z.Metrics().Width
		}
	}
	return 30 * f.ch
}

// ContentWidths is kit.Layouter's: the text's, at most 30ch.
func (f *footnote) ContentWidths(ctx *kit.LayoutContext) (min, max kit.DIP) {
	lo, hi := ctx.IntrinsicWidths(f.label)
	l := f.limit(ctx) - 18 // max-inline-size is the border box's: less the padding
	if l > 0 {
		lo, hi = kit.DIP(minf(float32(lo), float32(l))), kit.DIP(minf(float32(hi), float32(l)))
	}
	return lo, hi
}

// ContentHeight is kit.Layouter's.
func (f *footnote) ContentHeight(ctx *kit.LayoutContext, w kit.DIP) kit.DIP {
	return ctx.HeightFor(f.label, w, w)
}

// LayoutContent is kit.Layouter's.
func (f *footnote) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	if l := f.limit(ctx) - 18; l > 0 && box.Width() > l {
		box.Max.X = box.Min.X + l
	}
	ctx.Place(f.label, box, box.Width())
}

// Paint is kit.Painter's.
func (f *footnote) Paint(p *kit.PaintContext) { f.label.SetColor(tokensOf(&f.Element).Faint) }

// Detached is kit.Detacher's.
func (f *footnote) Detached() {
	if f.zero != nil {
		f.zero.Release()
		f.zero = nil
	}
}

// MenuButton is MenuButton.svelte: a button of any look (Button, IconButton) with an anchored
// menu, aria-haspopup="menu" and aria-expanded. A click, Enter, Space or ArrowDown opens it on
// its first item, ArrowUp on its last; closing it gives the focus back to the trigger; choosing
// an item closes it, then tells OnSelect. An unavailable one keeps the focus and does not open.
type MenuButton struct {
	kit.Element
	Trigger   kit.Widget
	Items     []MenuItem
	Label     string
	Footnote  string
	Align     MenuAlign
	Direction MenuDirection
	OnSelect  func(id string)
	OnToggle  func(open bool)
	menu      *Menu
}

// menuTrigger is a control a MenuButton drives.
type menuTrigger interface {
	kit.Widget
	ctl() *control
}

func (c *control) ctl() *control { return c }

// NewMenuButton makes a menu button around trigger (a *Button or *IconButton).
func NewMenuButton(trigger kit.Widget, label string, items []MenuItem, onSelect func(string)) *MenuButton {
	mb := &MenuButton{Trigger: trigger, Label: label, Items: items, OnSelect: onSelect}
	mb.Style = kit.Style{Display: kit.DisplayFlex, Flex: kit.FlexNone}
	if t, ok := trigger.(menuTrigger); ok {
		c := t.ctl()
		c.OnPress = func() {
			if mb.IsOpen() {
				mb.CloseMenu(false)
			} else {
				mb.OpenMenu(FocusFirst)
			}
		}
		c.Semantics.States |= kit.NodeHasPopup
		c.Semantics.Expand = kit.ExpandClosed
	}
	mb.Add(trigger)
	return mb
}

// IsOpen reports whether the menu is open.
func (mb *MenuButton) IsOpen() bool { return mb.menu.Open() }

// Menu is the open menu, or nil.
func (mb *MenuButton) Menu() *Menu {
	if mb.IsOpen() {
		return mb.menu
	}
	return nil
}

func (mb *MenuButton) disabled() bool { return mb.Trigger.Base().Disabled }

// SetDisabled makes the trigger unavailable but focusable (aria-disabled).
func (mb *MenuButton) SetDisabled(d bool) {
	if t, ok := mb.Trigger.(menuTrigger); ok {
		t.ctl().SetDisabled(d, true)
	}
	if d {
		mb.CloseMenu(false)
	}
}

// OpenMenu opens the menu with the focus on its first, last or checked item.
func (mb *MenuButton) OpenMenu(f InitialFocus) {
	if mb.disabled() || mb.IsOpen() {
		return
	}
	m, err := OpenMenu(mb.Trigger, MenuOptions{
		Items: mb.Items, Label: mb.Label, Footnote: mb.Footnote, Initial: f,
		Anchor:   &MenuAnchor{El: mb.Trigger, Align: mb.Align, Direction: mb.Direction},
		OnSelect: func(id string) { mb.CloseMenu(true); mb.selected(id) },
		OnClose:  func() { mb.CloseMenu(true) },
	})
	if err != nil {
		return
	}
	mb.menu = m
	mb.expanded(true)
	if mb.OnToggle != nil {
		mb.OnToggle(true)
	}
}

func (mb *MenuButton) selected(id string) {
	if mb.OnSelect != nil {
		mb.OnSelect(id)
	}
}

// CloseMenu closes the menu; the focus goes back to the trigger unless restore is false.
func (mb *MenuButton) CloseMenu(restore bool) {
	if !mb.IsOpen() {
		return
	}
	mb.menu.Close()
	mb.expanded(false)
	if mb.OnToggle != nil {
		mb.OnToggle(false)
	}
	if restore {
		if w := mb.Window(); w != nil {
			w.Focus(mb.Trigger)
		}
	}
}

func (mb *MenuButton) expanded(open bool) {
	if ib, ok := mb.Trigger.(*IconButton); ok {
		ib.SetExpanded(open)
	}
	if t, ok := mb.Trigger.(menuTrigger); ok {
		c := t.ctl()
		c.Semantics.Expand = kit.ExpandClosed
		if open {
			c.Semantics.Expand = kit.ExpandOpen
		}
		c.SemanticsChanged()
	}
}

// HandleKey is kit.KeyHandler's: the trigger's arrows (Enter and Space press it).
func (mb *MenuButton) HandleKey(e kit.KeyEvent) bool {
	if e.Action != kit.KeyDown || e.Mods&(kit.ModCtrl|kit.ModAlt|kit.ModMeta) != 0 || !mb.Trigger.Base().Focused() {
		return false
	}
	switch e.Key {
	case kit.KeyArrowDown:
		mb.OpenMenu(FocusFirst)
	case kit.KeyArrowUp:
		mb.OpenMenu(FocusLast)
	default:
		return false
	}
	return true
}

// Detached is kit.Detacher's: the menu goes with its button.
func (mb *MenuButton) Detached() {
	if mb.IsOpen() {
		mb.menu.Close()
	}
}

// SplitButton is SplitButton.svelte (DESIGN-SYSTEM.md §3.2): the primary action and a 26 px
// chevron that opens a menu of other ways to do it ("Add downloads…", "Add video or stream…"),
// the halves split by a 1 px --split-line, the menu below and lined up with the end.
type SplitButton struct {
	kit.Element
	Main *Button
	More *MenuButton
}

// NewSplitButton makes a split button. moreLabel names the chevron ("More ways to add") and
// menuLabel its menu ("Add").
func NewSplitButton(text, icon string, onClick func(), items []MenuItem, onSelect func(string), moreLabel, menuLabel string) *SplitButton {
	s := &SplitButton{}
	s.Main = NewButton(text, icon, Primary, SizeMd, onClick)
	s.Main.Style.Padding = kit.Edges{Start: kit.Px(11), End: kit.Px(12)}
	s.Main.shape = buttonShape{custom: true, radii: kit.Radii{TopLeft: 8, BottomLeft: 8}, noLift: true}
	more := NewButton("", "chevron-down", Primary, SizeMd, nil)
	more.icon.size = 14
	more.icon.Style.Width, more.icon.Style.Height = kit.Px(14), kit.Px(14)
	more.Style.Width = kit.Px(26)
	more.Style.Padding = kit.Edges{}
	more.Style.Border = kit.Edges{Start: kit.Px(1)}
	more.shape = buttonShape{custom: true, radii: kit.Radii{TopRight: 8, BottomRight: 8}, noLift: true, splitStart: true}
	more.Semantics.Name = moreLabel
	more.Tooltip = moreLabel
	s.More = NewMenuButton(more, menuLabel, items, onSelect)
	s.More.Align = AlignToEnd
	s.Style = kit.Style{Display: kit.DisplayFlex, Flex: kit.FlexNone, Radius: kit.CornersAll(tokens.RMd)}
	s.Add(s.Main, s.More)
	return s
}

// SetDisabled makes both halves unavailable but focusable.
func (s *SplitButton) SetDisabled(d bool) {
	s.Main.SetDisabled(d, true)
	s.More.SetDisabled(d)
	s.Invalidate()
}

// OpenMenu opens the chevron's menu (from a shortcut or the palette).
func (s *SplitButton) OpenMenu() { s.More.OpenMenu(FocusFirst) }

// Paint is kit.Painter's: the primary lift around both halves.
func (s *SplitButton) Paint(p *kit.PaintContext) {
	t := tokensOf(&s.Element)
	drawShadows(p.Canvas, snapBox(p, 8), t.PrimaryLift)
}

// MenuPreview is a menu's box drawn in place, as its popup shows it, for previews and
// screenshots: it opens no window and its items choose nothing. view is the viewport its
// widths are limited by.
func MenuPreview(o MenuOptions, view kit.Size) kit.Widget {
	m := &Menu{o: o}
	return newMenuBox(m, view)
}
