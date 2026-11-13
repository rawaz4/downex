package shell

import (
	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
	"downex/ui/views"
)

// The title bar's metrics, as TitleBar.svelte and app.css write them (titlebar_test.go
// compares each with its source): the bar, the brand, the search field and its key cap, the
// icon buttons and the window buttons.
const (
	barPadStart     kit.DIP = 14   // .titlebar padding-inline: 14px 10px
	barPadEnd       kit.DIP = 10   // (0 when frameless: the window buttons reach the edge)
	brandGap        kit.DIP = 9    // .brand gap
	brandSize       kit.DIP = 24   // <BrandMark size={24} />
	searchMax       kit.DIP = 460  // .search inline-size: min(460px, 40vw)
	searchShare     kit.DIP = 0.40 //
	searchNarrowAt  kit.DIP = 1040 // @media (max-width: 1040px): min(360px, 36vw)
	searchNarrowMax kit.DIP = 360
	searchNarrowPct kit.DIP = 0.36
	fieldHeight     kit.DIP = 28 // .field block-size
	fieldPadStart   kit.DIP = 32 // .field padding-inline: 32px 62px
	fieldPadEnd     kit.DIP = 62
	glassInset      kit.DIP = 10 // .glass inset-inline-start
	glassSize       kit.DIP = 15 // <Icon name="search" size={15} />
	keyInset        kit.DIP = 3  // .palette-key inset-inline-end
	keyHeight       kit.DIP = 24 // .palette-key block-size, min-inline-size
	keyPadInline    kit.DIP = 2  // .palette-key padding-inline
	kbdHeight       kit.DIP = 19 // .palette-key kbd block-size
	kbdPadInline    kit.DIP = 6  // .palette-key kbd padding-inline
	kbdMinWidth     kit.DIP = 18 // kbd min-inline-size
	kbdRadius       kit.DIP = 5  // kbd border-radius
	endGap          kit.DIP = 2  // .end gap, .bar-btns gap
	barBtnW         kit.DIP = 32 // .bar-btn inline-size
	barBtnH         kit.DIP = 30 // .bar-btn block-size
	barIcon         kit.DIP = 17 // the theme and Settings icons' size
	winctlMargin    kit.DIP = 10 // .winctl margin-inline-start
	wcWidth         kit.DIP = 46 // .wc inline-size (block-size: 100%)
	wcIcon          kit.DIP = 20 // the window buttons' icons' size
)

// TitleBar is the main window's title bar (TitleBar.svelte, DESIGN-SYSTEM.md §4.2): the logo
// and the name, the list search with the command palette's key cap, the theme and Settings
// buttons and, on a window with a custom frame, the minimise, maximise or restore and close
// buttons. Its ground is the window's caption (drag, the system's double click), its window
// buttons are the frame's buttons (Snap Layouts opens over maximise), and the rest is the
// window's own.
type TitleBar struct {
	kit.Element
	// OnSearch hears the search field's text as it is typed; OnCommands the key cap (the
	// command palette); OnTheme the theme button, which cycles the choice (the owner calls
	// ThemeChanged once it has); OnSettings the gear.
	OnSearch   func(text string)
	OnCommands func()
	OnTheme    func()
	OnSettings func()

	theme     *views.Theme
	frameless bool
	maximized bool
	modal     bool
	dim       dimmer

	brand       *kit.Element
	mark        *brandMark
	word        *kit.Label
	search      *searchBox
	dimSearch   *dimmed
	field       *searchField
	glass       *iconBox
	key         *paletteKey
	endCol      *kit.Element
	end         *noDrag
	barBtns     *dimmed
	themeBtn    *iconButton
	settingsBtn *iconButton
	winctl      *kit.Element
	minBtn      *winButton
	maxBtn      *winButton
	closeBtn    *winButton
}

// NewTitleBar makes the title bar of a window: frameless when the window has a custom
// frame, whose buttons the bar then draws. Its colours come from theme, its icons from the
// window's resources.
func NewTitleBar(theme *views.Theme, frameless bool) *TitleBar {
	tb := &TitleBar{theme: theme, frameless: frameless}
	tb.dim.init(tb)
	end := kit.Px(barPadEnd)
	if frameless {
		end = kit.Px(0)
	}
	tb.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: tokens.Sp6,
		Height: tokens.TitlebarH, Padding: kit.Edges{Start: kit.Px(barPadStart), End: end},
	}
	if frameless {
		tb.Frame = kit.FrameCaption // the drag region and the system's double click
	}
	tb.Semantics = kit.Semantics{Role: kit.RoleTitleBar, AutomationID: "titlebar"}

	// The brand: the logo and the name.
	tb.mark = &brandMark{theme: theme}
	tb.mark.Style = kit.Style{Width: kit.Px(brandSize), Height: kit.Px(brandSize), Flex: kit.FlexNone}
	tb.word = kit.NewLabel("Downex", fontWord, kit.TextOptions{})
	tb.word.Semantics.Role = kit.RoleNone // the window's title names it
	tb.brand = &kit.Element{Style: kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(brandGap),
		Flex: kit.Flex1, MinWidth: kit.Px(0),
	}}
	tb.brand.Add(tb.mark, tb.word)

	// The search: the field, its magnifier and the palette's key cap over it.
	tb.field = newSearchField(tb)
	tb.glass = newIconBox(icons.Search, glassSize, func(p *kit.PaintContext) kit.Color { return theme.Tokens(p.Env).Faint })
	tb.key = newPaletteKey(tb)
	tb.search = &searchBox{field: tb.field, glass: tb.glass, key: tb.key}
	tb.search.Style = kit.Style{Flex: kit.FlexNone}
	tb.search.Add(tb.field, tb.glass, tb.key)
	tb.dimSearch = &dimmed{dim: &tb.dim}
	tb.dimSearch.Style = kit.Style{Display: kit.DisplayFlex, Flex: kit.FlexNone}
	tb.dimSearch.Add(tb.search)

	// The end: the theme and Settings buttons, and the window buttons.
	tb.themeBtn = newIconButton(tb, icons.System, func() {
		if tb.OnTheme != nil {
			tb.OnTheme()
		}
	})
	tb.settingsBtn = newIconButton(tb, icons.Gear, func() {
		if tb.OnSettings != nil {
			tb.OnSettings()
		}
	})
	tb.settingsBtn.Semantics.Name = "Settings"
	tb.settingsBtn.Semantics.Shortcut = kit.KeyLabel("Ctrl+,")
	tb.settingsBtn.Tooltip = kit.KeyLabel("Settings (Ctrl+,)")
	tb.barBtns = &dimmed{dim: &tb.dim}
	tb.barBtns.Style = kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(endGap)}
	tb.barBtns.Add(tb.themeBtn, tb.settingsBtn)
	tb.end = &noDrag{}
	tb.end.Style = kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(endGap), Height: kit.Pct(100)}
	tb.end.Add(tb.barBtns)
	if frameless {
		tb.minBtn = newWinButton(tb, icons.WinMinimise, "Minimise", kit.FrameMinimize, false, func() { tb.command(kit.WindowMinimize) })
		tb.maxBtn = newWinButton(tb, icons.WinMaximise, "Maximise", kit.FrameMaximize, false, func() {
			if tb.maximized {
				tb.command(kit.WindowRestore)
			} else {
				tb.command(kit.WindowMaximize)
			}
		})
		tb.closeBtn = newWinButton(tb, icons.WinClose, "Close", kit.FrameClose, true, func() { tb.command(kit.WindowClose) })
		tb.winctl = &kit.Element{Style: kit.Style{
			Display: kit.DisplayFlex, AlignSelf: kit.AlignStretch, Margin: kit.Edges{Start: kit.Px(winctlMargin)},
		}}
		tb.winctl.Exempt = kit.ExemptTab // reachable with Tab from an open dialog
		tb.winctl.Semantics = kit.Semantics{Role: kit.RoleGroup, Name: "Window"}
		tb.winctl.Add(tb.minBtn, tb.maxBtn, tb.closeBtn)
		tb.end.Add(tb.winctl)
	}
	tb.endCol = &kit.Element{Style: kit.Style{
		Display: kit.DisplayFlex, Justify: kit.JustifyEnd, AlignSelf: kit.AlignStretch,
		Flex: kit.Flex1, MinWidth: kit.Px(0),
	}}
	tb.endCol.Add(tb.end)

	tb.Add(tb.brand, tb.dimSearch, tb.endCol)
	tb.applyChoice()
	return tb
}

// Paint is kit.Painter's: the bar on the chrome.
func (tb *TitleBar) Paint(p *kit.PaintContext) {
	p.FillRect(kit.RectAt(kit.Point{}, p.Size()), tb.theme.Tokens(p.Env).Chrome)
}

// command asks the window for what a window button does.
func (tb *TitleBar) command(c kit.WindowCommand) {
	if w := tb.Window(); w != nil && w.Host() != nil {
		w.Host().Command(c)
	}
}

// SetMaximized follows the window: the middle button restores a maximised window.
func (tb *TitleBar) SetMaximized(m bool) {
	if m == tb.maximized || tb.maxBtn == nil {
		tb.maximized = m
		return
	}
	tb.maximized = m
	name, label := icons.WinMaximise, "Maximise"
	if m {
		name, label = icons.WinRestore, "Restore"
	}
	tb.maxBtn.icon.name = name
	tb.maxBtn.Semantics.Name = label
	tb.maxBtn.Tooltip = label
	tb.maxBtn.SemanticsChanged()
	tb.maxBtn.Invalidate()
}

// Maximized reports whether the bar shows the restore button.
func (tb *TitleBar) Maximized() bool { return tb.maximized }

// ThemeChanged repaints the bar in the theme's colours and shows the theme choice on its
// button; call it after the choice or the environment changed.
func (tb *TitleBar) ThemeChanged() {
	tb.applyChoice()
	tb.field.applyOptions()
	for _, f := range []interface{ resetColors() }{tb.themeBtn, tb.settingsBtn, tb.key, tb.field} {
		f.resetColors()
	}
	if tb.minBtn != nil {
		tb.minBtn.resetColors()
		tb.maxBtn.resetColors()
		tb.closeBtn.resetColors()
	}
	tb.Invalidate()
}

func (tb *TitleBar) applyChoice() {
	labels := map[tokens.Choice]string{tokens.System: "System theme", tokens.ChoiceLight: "Light theme", tokens.ChoiceDark: "Dark theme"}
	glyphs := map[tokens.Choice]icons.Name{tokens.System: icons.System, tokens.ChoiceLight: icons.Sun, tokens.ChoiceDark: icons.Moon}
	c := tb.theme.Choice()
	tb.themeBtn.icon.name = glyphs[c]
	tb.themeBtn.Semantics.Name = labels[c] + ". Switch to " + lower(labels[c.Next()])
	tb.themeBtn.Tooltip = labels[c] + " (click to change)"
	tb.themeBtn.SemanticsChanged()
	tb.themeBtn.Invalidate()
}

func lower(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	if b[0] >= 'A' && b[0] <= 'Z' {
		b[0] += 'a' - 'A'
	}
	return string(b)
}

// Search is the search field.
func (tb *TitleBar) Search() *kit.TextField { return &tb.field.TextField }

// FocusSearch moves the focus to the search and selects its text (Ctrl+F).
func (tb *TitleBar) FocusSearch() {
	if w := tb.Window(); w != nil && w.Focus(tb.field) {
		tb.field.SelectAll()
	}
}

// ---- the parts ----------------------------------------------------------------------------

// noDrag is a part of the bar that is the window's own, gaps included (TitleBar.svelte:
// --wails-draggable: no-drag on .end): it takes the pointer, so it is not the caption.
type noDrag struct {
	kit.Element
}

// HandlePointer is kit.PointerHandler's: it takes no action itself.
func (*noDrag) HandlePointer(*kit.PointerInput) bool { return false }

// brandMark draws the logo (BrandMark.svelte) at its size, snapped to device pixels.
type brandMark struct {
	kit.Element
	theme *views.Theme
}

// Paint is kit.Painter's.
func (b *brandMark) Paint(p *kit.PaintContext) {
	set := setOf(&b.Element)
	if set == nil {
		return
	}
	at := kit.Pt(p.Scale.Snap(p.Origin.X), p.Scale.Snap(p.Origin.Y))
	set.DrawBrand(p.Canvas, at, float64(brandSize), b.theme.Tokens(p.Env))
}

// searchBox lays out the search (TitleBar's .search): min(460px, 40vw) wide, or
// min(360px, 36vw) at 1040 px and less, as tall as the field, the magnifier and the key cap
// placed over the field.
type searchBox struct {
	kit.Element
	field *searchField
	glass *iconBox
	key   *paletteKey
}

func (s *searchBox) width() kit.DIP {
	var vw kit.DIP
	if w := s.Window(); w != nil {
		vw = w.Size().W
	}
	if vw <= searchNarrowAt {
		return min(searchNarrowMax, searchNarrowPct*vw)
	}
	return min(searchMax, searchShare*vw)
}

// ContentWidths is kit.Layouter's: the width follows the window's.
func (s *searchBox) ContentWidths(*kit.LayoutContext) (min, max kit.DIP) {
	w := s.width()
	return w, w
}

// ContentHeight is kit.Layouter's.
func (s *searchBox) ContentHeight(*kit.LayoutContext, kit.DIP) kit.DIP { return fieldHeight }

// LayoutContent is kit.Layouter's.
func (s *searchBox) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	w := box.Width()
	ctx.Place(s.field, kit.RectAt(box.Min, kit.Size{W: w, H: fieldHeight}), w)
	ctx.Place(s.glass, kit.RectAt(kit.Pt(box.Min.X+glassInset, box.Min.Y+(fieldHeight-glassSize)/2), kit.Size{W: glassSize, H: glassSize}), w)
	_, kw := ctx.IntrinsicWidths(s.key)
	kw = max(kw, keyHeight)
	ctx.Place(s.key, kit.RectAt(kit.Pt(box.Max.X-keyInset-kw, box.Min.Y+(fieldHeight-keyHeight)/2), kit.Size{W: kw, H: keyHeight}), w)
}

// SetModal shows that a modal dialog or the command palette is open (TitleBar's modal): the
// search and the bar's buttons step back to 0.45 opacity over --dur-2 (the dialog host makes
// them inert); the drag region and the window buttons stay as they are.
func (tb *TitleBar) SetModal(m bool) {
	if m == tb.modal {
		return
	}
	tb.modal = m
	target := float32(1)
	if m {
		target = modalOpacity
	}
	tb.dim.moveTo(tb.Window(), target)
}

// Modal reports what SetModal last set.
func (tb *TitleBar) Modal() bool { return tb.modal }

// Inert are the parts of the bar that are inert while a modal is open (the search and the
// theme and Settings buttons; not the window buttons).
func (tb *TitleBar) Inert() []*kit.Element {
	return []*kit.Element{tb.dimSearch.Base(), tb.barBtns.Base()}
}

// SetSearchText shows the list's search in the field (another part of the window cleared it),
// without telling OnSearch.
func (tb *TitleBar) SetSearchText(s string) {
	if tb.field.Text() != s {
		tb.field.SetText(s)
	}
}

// LaidOut is kit.LayoutFollower's: maximising, restoring and snapping resize the window, so
// the middle button follows the window's state after each layout (TitleBar.svelte reads it at
// every resize).
func (tb *TitleBar) LaidOut() {
	if w := tb.Window(); w != nil && tb.frameless {
		tb.SetMaximized(w.WindowState().Maximized)
	}
}

// Parts are the bar's elements with the references' selectors.
// Logo is the brand mark (BrandMark at 24 px), drawn by kit/icons' DrawBrand.
func (tb *TitleBar) Logo() *kit.Element { return tb.mark.Base() }

func (tb *TitleBar) Parts() map[string]Part {
	m := map[string]Part{
		"brand": {"div.brand", 0, tb.brand}, "word": {"span.word", 0, tb.word.Base()}, "search": {"div.search", 0, tb.search.Base()},
		"palette key": {"button.palette-key", 0, tb.key.Base()}, "kbd": {"kbd.", 0, tb.key.kbd.Base()},
		"end": {"div.end", 0, tb.end.Base()}, "bar buttons": {"div.bar-btns", 0, tb.barBtns.Base()},
		"theme": {"button.icon-btn.bar-btn", 0, tb.themeBtn.Base()}, "settings": {"button.icon-btn.bar-btn", 1, tb.settingsBtn.Base()},
	}
	if tb.winctl != nil {
		m["winctl"] = Part{"div.winctl", 0, tb.winctl}
		m["minimise"] = Part{"button.wc", 0, tb.minBtn.Base()}
		m["maximise"] = Part{"button.wc", 1, tb.maxBtn.Base()}
		m["close"] = Part{"button.wc", 2, tb.closeBtn.Base()}
	}
	return m
}
