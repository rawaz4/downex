package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/kit/frontend"
	"downex/ui/kit/icons"
	"downex/ui/kit/kittest"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/views"
)

var env100 = kit.Environment{Scale: kit.Scale{PixelsPerDIP: 1, Text: 1}}

type barApp struct {
	tb     *TitleBar
	win    *kit.Window
	loop   *kittest.Loop
	theme  *views.Theme
	choice tokens.Choice
	search []string
	calls  []string
}

func openBar(t *testing.T, size kit.Size, frameless bool) *barApp {
	t.Helper()
	b := &barApp{choice: tokens.ChoiceLight}
	b.theme = &views.Theme{Fixed: &b.choice}
	b.tb = NewTitleBar(b.theme, frameless)
	b.tb.OnSearch = func(s string) { b.search = append(b.search, s) }
	b.tb.OnCommands = func() { b.calls = append(b.calls, "commands") }
	b.tb.OnSettings = func() { b.calls = append(b.calls, "settings") }
	b.tb.OnTheme = func() {
		b.calls = append(b.calls, "theme")
		b.choice = b.choice.Next()
		b.tb.ThemeChanged()
	}
	body := &kit.Element{Style: kit.Style{Flex: kit.Flex1}}
	page := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn}}
	page.Add(b.tb, body)
	scope := widgets.NewScope(b.theme, page)
	clock := kittest.NewClock()
	b.win = kit.NewWindow(scope, kit.WindowOptions{
		Title: "Downex", MinSize: kit.Size{W: 900, H: 600}, Now: clock.Now, Scheduler: clock,
		Palette: b.theme.Palette, FrameColors: b.theme.MainFrame,
		OnWindowState: func(s kit.WindowState) { b.tb.SetMaximized(s.Maximized) },
	})
	b.loop = kittest.NewLoop(b.win, clock, size, env100)
	b.loop.Step()
	return b
}

func near(a, b float32) bool { return a-b < 0.02 && b-a < 0.02 }

// The bar's layout is TitleBar.svelte's grid: the search min(460px, 40vw) wide (36vw and
// 360 px at 1040 px and less) in the middle column, the brand at the start, the buttons at
// the end; the spike measured the search at x 337 and 440 wide in a 1100 px window.
func TestTitleBarLayout(t *testing.T) {
	for _, c := range []struct {
		w, x, width float32
	}{
		{1100, 337, 440}, {1200, 14 + (1200-14-24-460)/2 + 12, 460}, {900, 14 + (900-14-24-324)/2 + 12, 324},
		{1040, 14 + (1040-14-24-360)/2 + 12, 360}, {1920, 14 + (1920-14-24-460)/2 + 12, 460},
	} {
		b := openBar(t, kit.Size{W: c.w, H: 720}, true)
		r := b.tb.search.WindowBounds()
		if !near(r.Min.X, c.x) || !near(r.Width(), c.width) || !near(r.Min.Y, 8) || !near(r.Height(), 28) {
			t.Errorf("%v px wide: search at %v, want x %.1f, width %.1f, y 8, height 28", c.w, r, c.x, c.width)
		}
		if h := b.tb.WindowBounds().Height(); h != 44 {
			t.Errorf("bar height %v", h)
		}
		// The window buttons reach the window's edge, 46 px each, the bar's height.
		cl := b.tb.closeBtn.WindowBounds()
		if !near(cl.Max.X, c.w) || !near(cl.Width(), 46) || !near(cl.Height(), 44) || cl.Min.Y != 0 {
			t.Errorf("%v px wide: close at %v", c.w, cl)
		}
		mn := b.tb.minBtn.WindowBounds()
		th := b.tb.themeBtn.WindowBounds()
		st := b.tb.settingsBtn.WindowBounds()
		if !near(st.Max.X+2+10, mn.Min.X) || !near(th.Max.X+2, st.Min.X) || !near(th.Width(), 32) || !near(th.Height(), 30) || !near(th.Min.Y, 7) {
			t.Errorf("%v px wide: theme %v, settings %v, minimise %v", c.w, th, st, mn)
		}
		// The brand: the logo at 14, 10; the name 9 px after it.
		if m := b.tb.mark.WindowBounds(); m != kit.R(14, 10, 38, 34) {
			t.Errorf("logo at %v", m)
		}
		if w := b.tb.word.WindowBounds(); !near(w.Min.X, 14+24+9) {
			t.Errorf("name at %v", w)
		}
		// The search's parts: the native field fills the content box (32 px in, 62 px from
		// the end, inside the 1 px border), the magnifier 10 px in, the key cap 3 px from the end.
		nf := b.loop.Host.Fields[0]
		if want := kit.R(r.Min.X+1+32, r.Min.Y+1, r.Max.X-1-62, r.Max.Y-1); nf.Bounds != want {
			t.Errorf("native field at %v, want %v", nf.Bounds, want)
		}
		if g := b.tb.glass.WindowBounds(); !near(g.Min.X, r.Min.X+10) || !near(g.Min.Y, r.Min.Y+6.5) || g.Width() != 15 {
			t.Errorf("magnifier at %v", g)
		}
		if k := b.tb.key.WindowBounds(); !near(k.Max.X, r.Max.X-3) || !near(k.Min.Y, r.Min.Y+2) || k.Height() != 24 {
			t.Errorf("key cap at %v", k)
		}
		if k := b.tb.key.kbd.WindowBounds(); k.Height() != 19 || !near(k.Min.Y, r.Min.Y+2+2.5) {
			t.Errorf("kbd at %v", k)
		}
	}
}

// What the frame is: the caption over the brand and the bar's empty ground, the window's
// own over the search, the buttons and the .end box with its gaps, the frame's buttons
// over the three window buttons.
func TestTitleBarFrameHit(t *testing.T) {
	b := openBar(t, kit.Size{W: 1100, H: 720}, true)
	r := b.tb.search.WindowBounds()
	th := b.tb.themeBtn.WindowBounds()
	end := b.tb.end.WindowBounds()
	for _, c := range []struct {
		name string
		p    kit.Point
		want kit.FramePart
	}{
		{"logo", kit.Pt(20, 20), kit.FrameCaption},
		{"name", kit.Pt(60, 22), kit.FrameCaption},
		{"ground left of the search", kit.Pt(r.Min.X-5, 22), kit.FrameCaption},
		{"ground above the search", kit.Pt(r.Min.X+100, 3), kit.FrameCaption},
		{"search", kit.Pt(r.Min.X+100, 22), kit.FrameClient},
		{"magnifier", kit.Pt(r.Min.X+15, 22), kit.FrameClient},
		{"key cap", kit.Pt(r.Max.X-10, 22), kit.FrameClient},
		{"ground right of the search", kit.Pt(r.Max.X+20, 22), kit.FrameCaption},
		{"theme button", kit.Pt(th.Min.X+5, 22), kit.FrameClient},
		{"gap between the buttons", kit.Pt(th.Max.X+1, 22), kit.FrameClient},
		{"above the theme button (.end)", kit.Pt(th.Min.X+5, 2), kit.FrameClient},
		{"left of .end", kit.Pt(end.Min.X-3, 22), kit.FrameCaption},
		{"minimise", kit.Pt(1100-46*3+10, 22), kit.FrameMinimize},
		{"maximise", kit.Pt(1100-46*2+10, 40), kit.FrameMaximize},
		{"close", kit.Pt(1099, 0), kit.FrameClose},
		{"below the bar", kit.Pt(500, 300), kit.FrameClient},
	} {
		if got := b.win.FrameHit(c.p); got != c.want {
			t.Errorf("%s at %v: %v, want %v", c.name, c.p, got, c.want)
		}
	}
}

func TestTitleBarActions(t *testing.T) {
	b := openBar(t, kit.Size{W: 1100, H: 720}, true)
	click := func(e *kit.Element) {
		p := e.WindowBounds().Center()
		b.loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: p})
		b.loop.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: p, Button: kit.ButtonLeft, Buttons: kit.ButtonLeft, Clicks: 1})
		b.loop.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: p, Button: kit.ButtonLeft})
	}
	click(b.tb.minBtn.Base())
	click(b.tb.maxBtn.Base())
	b.loop.Send(kit.WindowStateEvent{State: kit.WindowState{Maximized: true}})
	if !b.tb.Maximized() || b.tb.maxBtn.icon.name != icons.WinRestore || b.tb.maxBtn.Semantics.Name != "Restore" {
		t.Error("the middle button does not show Restore while maximised")
	}
	click(b.tb.maxBtn.Base())
	click(b.tb.closeBtn.Base())
	if got := fmt.Sprint(b.loop.Host.Commands); got != fmt.Sprint([]kit.WindowCommand{kit.WindowMinimize, kit.WindowMaximize, kit.WindowRestore, kit.WindowClose}) {
		t.Errorf("window commands %v", got)
	}
	click(b.tb.settingsBtn.Base())
	click(b.tb.key.Base())
	click(b.tb.themeBtn.Base())
	if fmt.Sprint(b.calls) != "[settings commands theme]" {
		t.Errorf("calls %v", b.calls)
	}
	if b.choice != tokens.ChoiceDark || b.tb.themeBtn.icon.name != icons.Moon || !strings.HasPrefix(b.tb.themeBtn.Semantics.Name, "Dark theme. Switch to system theme") {
		t.Errorf("after the theme button: choice %v, icon %v, name %q", b.choice, b.tb.themeBtn.icon.name, b.tb.themeBtn.Semantics.Name)
	}

	// The search: typing reaches OnSearch, Escape clears it, Ctrl+F focuses and selects.
	nf := b.loop.Host.Fields[0]
	nf.Click()
	nf.Type("ubuntu")
	b.loop.Send(kit.KeyEvent{Action: kit.KeyDown, Key: kit.KeyEscape})
	if fmt.Sprint(b.search) != "[ubuntu ]" || nf.Value != "" {
		t.Errorf("search %q, field %q", b.search, nf.Value)
	}
	b.win.Focus(b.tb.themeBtn)
	b.tb.FocusSearch()
	if b.win.Focused() != b.tb.field.Base() || !nf.HasFocus || !nf.Selected {
		t.Error("FocusSearch did not focus and select the search")
	}
	if nf.Opts.Placeholder != "Search downloads" || nf.Opts.Name != "Search downloads by name or URL" || nf.Opts.Background != searchGroundDark {
		t.Errorf("field options %+v", nf.Opts)
	}
}

// The hover colours ease in over --dur-2 and the window buttons' colours are TitleBar's.
func TestTitleBarHoverColours(t *testing.T) {
	b := openBar(t, kit.Size{W: 1100, H: 720}, true)
	cl := b.tb.closeBtn
	if cl.bg.cur != (kit.Color{}) || cl.ink.cur != tokens.Light.Ink2 {
		t.Fatalf("close at rest: %v %v", cl.bg.cur, cl.ink.cur)
	}
	b.loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: cl.WindowBounds().Center()})
	b.loop.Run(tokens.Dur2 / 2)
	if a := cl.bg.cur.A; a <= 0 || a >= 1 {
		t.Errorf("half way through the hover the close button's ground has alpha %v", a)
	}
	b.loop.Run(tokens.Dur2)
	if cl.bg.cur != tokens.Light.WinctlClose || cl.ink.cur != closeGlyph {
		t.Errorf("hovered close: %v %v", cl.bg.cur, cl.ink.cur)
	}
	mx := b.tb.maxBtn
	b.loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: mx.WindowBounds().Center()})
	b.loop.Run(2 * tokens.Dur2)
	if mx.bg.cur != tokens.Light.Press || mx.ink.cur != tokens.Light.Ink {
		t.Errorf("hovered maximise: %v %v", mx.bg.cur, mx.ink.cur)
	}
	b.loop.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: mx.WindowBounds().Center(), Button: kit.ButtonLeft, Buttons: kit.ButtonLeft, Clicks: 1})
	b.loop.Run(2 * tokens.Dur2)
	if mx.bg.cur != tokens.Light.Line {
		t.Errorf("pressed maximise: %v", mx.bg.cur)
	}
	// Idle again: no frames.
	b.loop.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: kit.Pt(500, 400), Button: kit.ButtonLeft})
	b.loop.Run(time.Second)
	frames := b.loop.Frames
	b.loop.Run(5 * time.Second)
	if b.loop.Frames != frames {
		t.Errorf("%d frames while idle", b.loop.Frames-frames)
	}
}

// The values the title bar mirrors are the ones TitleBar.svelte and app.css write: this
// fails when a component changes and the native bar does not.
func TestTitleBarMatchesComponent(t *testing.T) {
	src, err := frontend.Dir()
	if err != nil {
		t.Fatal(err)
	}
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		return strings.ReplaceAll(string(b), "\r\n", "\n")
	}
	bar := read("lib/components/TitleBar.svelte")
	css, _, err := frontend.SvelteStyle(bar)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := frontend.ParseCSS(css)
	if err != nil {
		t.Fatal(err)
	}
	app, err := frontend.StripComments(read("app.css"))
	if err != nil {
		t.Fatal(err)
	}
	appRules, err := frontend.ParseCSS(app)
	if err != nil {
		t.Fatal(err)
	}
	get := func(rs []frontend.Rule, media, sel, prop string) string {
		v, found := "", false
		for _, r := range rs {
			if r.Media == media && r.HasSelector(sel) {
				if x, ok := r.Get(prop); ok {
					v, found = x, true
				}
			}
		}
		if !found {
			return "<none>"
		}
		return v
	}
	for _, c := range []struct {
		app         bool
		media       string
		sel, prop   string
		want, mirro string
	}{
		{false, "", ".titlebar", "gap", "var(--sp-6)", "tokens.Sp6"},
		{false, "", ".titlebar", "block-size", "var(--titlebar-h)", "tokens.TitlebarH"},
		{false, "", ".titlebar", "padding-inline", "14px 10px", "barPadStart, barPadEnd"},
		{false, "", ".titlebar.frameless", "padding-inline-end", "0", "frameless end padding"},
		{false, "", ".titlebar", "background", "var(--chrome)", "TitleBar.Paint"},
		{false, "", ".brand", "gap", "9px", "brandGap"},
		{false, "", ".word", "font", "650 0.9375rem / 1 var(--font-display)", "fontWord"},
		{false, "", ".word", "letter-spacing", "-0.015em", "fontWord"},
		{false, "", ".search", "inline-size", "min(460px, 40vw)", "searchMax, searchShare"},
		{false, "(max-width: 1040px)", ".search", "inline-size", "min(360px, 36vw)", "searchNarrow*"},
		{false, "", ".search", "color", "var(--faint)", "the magnifier's colour"},
		{false, "", ".glass", "inset-inline-start", "10px", "glassInset"},
		{false, "", ".field", "block-size", "28px", "fieldHeight"},
		{false, "", ".field", "padding-inline", "32px 62px", "fieldPadStart, fieldPadEnd"},
		{false, "", ".field", "border", "1px solid var(--line-field)", "searchField.borderColor"},
		{false, "", ".field", "border-radius", "var(--r-md)", "searchField.Paint (8)"},
		{false, "", ".field", "background", "var(--search-ground, var(--surface))", "searchField.ground"},
		{false, "", ".field", "box-shadow", "var(--shadow-sm)", "searchField.Paint"},
		{false, "", ".field", "font-size", "var(--type-body)", "fontBody"},
		{false, "", ".field::placeholder", "color", "var(--faint)", "PlaceholderColor"},
		{false, "", ".field:hover", "border-color", "var(--ink-2)", "searchField.borderColor"},
		{false, "", ".field:focus", "border-color", "var(--focus)", "searchField.borderColor"},
		{false, "", ".field:focus", "box-shadow", "0 0 0 1px var(--focus), 0 0 0 4px var(--accent-soft)", "searchField.Paint"},
		{false, "", ".palette-key", "inset-inline-end", "3px", "keyInset"},
		{false, "", ".palette-key", "block-size", "24px", "keyHeight"},
		{false, "", ".palette-key", "min-inline-size", "24px", "keyHeight"},
		{false, "", ".palette-key", "padding-inline", "2px", "keyPadInline"},
		{false, "", ".palette-key", "border-radius", "var(--r-sm)", "tokens.RSm"},
		{false, "", ".palette-key kbd", "block-size", "19px", "kbdHeight"},
		{false, "", ".palette-key kbd", "padding-inline", "6px", "kbdPadInline"},
		{false, "", ".palette-key kbd", "font-size", "0.6875rem", "fontKbd"},
		{false, "", ".palette-key:hover kbd", "color", "var(--ink)", "paletteKey colours"},
		{false, "", ".palette-key:hover kbd", "border-color", "var(--line-field)", "paletteKey colours"},
		{false, "", ".palette-key:focus-visible", "outline-offset", "1px", "paletteKey.Ring"},
		{false, "", ".end", "gap", "2px", "endGap"},
		{false, "", ".bar-btns", "gap", "2px", "endGap"},
		{false, "", ".bar-btn", "inline-size", "32px", "barBtnW"},
		{false, "", ".bar-btn", "block-size", "30px", "barBtnH"},
		{false, "", ".winctl", "margin-inline-start", "10px", "winctlMargin"},
		{false, "", ".wc", "inline-size", "46px", "wcWidth"},
		{false, "", ".wc", "color", "var(--ink-2)", "winButton colours"},
		{false, "", ".wc:hover", "background", "var(--press)", "winButton colours"},
		{false, "", ".wc:hover", "color", "var(--ink)", "winButton colours"},
		{false, "", ".wc:active", "background", "var(--line)", "winButton colours"},
		{false, "", ".wc.close:hover", "background", "var(--winctl-close)", "winButton colours"},
		{false, "", ".wc.close:hover", "color", "#fff", "closeGlyph"},
		{false, "", ".wc:focus-visible", "outline-offset", "-2px", "winButton.Ring"},
		{false, "", ":global(:root[data-theme='dark']) .titlebar", "--search-ground", "#141a17", "searchGroundDark"},
		{false, "(forced-colors: active)", ".field:focus", "outline", "2px solid Highlight", "searchField.Paint"},
		{false, "(forced-colors: active)", ".wc.close:hover", "background", "Highlight", "winButton colours"},
		{true, "", ".icon-btn", "border", "1px solid transparent", "iconButton"},
		{true, "", ".icon-btn", "border-radius", "var(--r-md)", "iconButton"},
		{true, "", ".icon-btn", "color", "var(--muted)", "iconButton colours"},
		{true, "", ".icon-btn:hover:not(:disabled, [aria-disabled='true'])", "background", "var(--hover)", "iconButton colours"},
		{true, "", ".icon-btn:hover:not(:disabled, [aria-disabled='true'])", "color", "var(--ink)", "iconButton colours"},
		{true, "", ".icon-btn:active:not(:disabled, [aria-disabled='true'])", "background", "var(--press)", "iconButton colours"},
		{true, "", "kbd", "min-inline-size", "18px", "kbdMinWidth"},
		{true, "", "kbd", "border", "1px solid var(--line-strong)", "paletteKey colours"},
		{true, "", "kbd", "border-block-end-width", "2px", "kbd.Paint"},
		{true, "", "kbd", "border-radius", "5px", "kbdRadius"},
		{true, "", "kbd", "background", "var(--surface)", "kbd.Paint"},
		{true, "", "kbd", "color", "var(--muted)", "paletteKey colours"},
		{true, "", "kbd", "font", "500 0.6875rem/1 var(--font-ui)", "fontKbd"},
	} {
		rs := rules
		if c.app {
			rs = appRules
		}
		if got := get(rs, c.media, c.sel, c.prop); got != c.want {
			t.Errorf("%s { %s } is %q, the native bar mirrors %q (%s)", c.sel, c.prop, got, c.want, c.mirro)
		}
	}
	// The markup's sizes, texts and names.
	for _, s := range []string{
		`<BrandMark size={24} />`, `<span class="word">Downex</span>`, `<Icon name="search" size={15} />`,
		`placeholder="Search downloads"`, `aria-label="Search downloads by name or URL"`, `<kbd>Ctrl K</kbd>`,
		`<Icon name={THEME_ICON[theme.choice]} size={17} />`, `<Icon name="gear" size={17} />`,
		`<Icon name="win-minimise" size={20} />`, `<Icon name="win-close" size={20} />`,
		`system: 'System theme',`, `const THEME_ICON = { system: 'system', light: 'sun', dark: 'moon' } as const;`,
		`aria-label="Commands"`, `title="Commands (Ctrl+K)"`, `title="Settings (Ctrl+,)"`,
	} {
		if !strings.Contains(bar, s) {
			t.Errorf("TitleBar.svelte no longer has %s", s)
		}
	}
}

// TestTitleBar is TitleBar.test.ts. The system frame's part (a double click on the caption
// toggles maximise, the resize borders) is ui/win's: the bar gives HTCAPTION where it drags and
// the system handles its double click once (frame.go's partAt); ui/win's resizeHit gives
// resize borders to a restored window only.
func TestTitleBar(t *testing.T) {
	click := func(b *barApp, e *kit.Element, clicks int) {
		p := e.WindowBounds().Center()
		b.loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: p})
		b.loop.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: p, Button: kit.ButtonLeft, Buttons: kit.ButtonLeft, Clicks: clicks})
		b.loop.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: p, Button: kit.ButtonLeft})
	}
	t.Run("shows the logo, the list search, the palette key, theme and Settings", func(t *testing.T) {
		b := openBar(t, kit.Size{W: 1100, H: 720}, false)
		b.tb.SetSearchText("iso")
		b.loop.Step()
		nf := b.loop.Host.Fields[0]
		if b.tb.word.Text() != "Downex" || nf.Value != "iso" || nf.Opts.Name != "Search downloads by name or URL" || b.tb.field.Semantics.Shortcut != "Ctrl+F" {
			t.Errorf("word %q, search %q %q %q", b.tb.word.Text(), nf.Value, nf.Opts.Name, b.tb.field.Semantics.Shortcut)
		}
		nf.Click()
		nf.Type("s")
		if b.search[len(b.search)-1] != "isos" {
			t.Errorf("search %q", b.search)
		}
		// Escape clears a search (and only then is it taken)
		if !b.loop.Send(kit.KeyEvent{Action: kit.KeyDown, Key: kit.KeyEscape}) || b.search[len(b.search)-1] != "" {
			t.Errorf("Escape: search %q", b.search)
		}
		k := b.tb.key
		if k.Semantics.Name != "Commands" || k.Semantics.Shortcut != "Ctrl+K" || k.Tooltip != "Commands (Ctrl+K)" || k.kbd.text.Text() != "Ctrl K" {
			t.Errorf("palette key %+v %q %q", k.Semantics, k.Tooltip, k.kbd.text.Text())
		}
		click(b, k.Base(), 1)
		click(b, b.tb.settingsBtn.Base(), 1)
		if fmt.Sprint(b.calls) != "[commands settings]" || !strings.Contains(b.tb.themeBtn.Semantics.Name, "theme. Switch to") {
			t.Errorf("calls %v, theme %q", b.calls, b.tb.themeBtn.Semantics.Name)
		}
	})
	t.Run("turns Wails’ resize edges off while maximised, and back on when restored", func(t *testing.T) {
		b := openBar(t, kit.Size{W: 1100, H: 720}, true)
		b.loop.Send(kit.WindowStateEvent{State: kit.WindowState{Maximized: true}})
		if !b.tb.Maximized() || b.tb.maxBtn.Semantics.Name != "Restore" {
			t.Fatal("not shown maximised")
		}
		// The bar's top edge is caption, never a resize edge of its own: ui/win gives resize
		// borders only while restored.
		if got := b.win.FrameHit(kit.Pt(300, 0)); got != kit.FrameCaption {
			t.Errorf("top edge %v", got)
		}
		click(b, b.tb.maxBtn.Base(), 1)
		b.loop.Send(kit.WindowStateEvent{State: kit.WindowState{}})
		if b.tb.Maximized() || b.tb.maxBtn.Semantics.Name != "Maximise" || fmt.Sprint(b.loop.Host.Commands) != fmt.Sprint([]kit.WindowCommand{kit.WindowRestore}) {
			t.Errorf("after Restore: %v %q", b.loop.Host.Commands, b.tb.maxBtn.Semantics.Name)
		}
	})
	t.Run("has no window controls and no drag region without a custom title bar", func(t *testing.T) {
		b := openBar(t, kit.Size{W: 1100, H: 720}, false)
		if b.tb.minBtn != nil || b.tb.maxBtn != nil || b.tb.closeBtn != nil || b.tb.winctl != nil {
			t.Error("window controls without a custom title bar")
		}
		for _, p := range []kit.Point{kit.Pt(20, 20), kit.Pt(60, 22), kit.Pt(1090, 22)} {
			if got := b.win.FrameHit(p); got != kit.FrameClient {
				t.Errorf("%v: %v", p, got)
			}
		}
	})
	t.Run("draws the window controls and the drag region of a frameless window", func(t *testing.T) {
		b := openBar(t, kit.Size{W: 1100, H: 720}, true)
		// the whole bar drags the window; its controls do not
		r := b.tb.search.WindowBounds()
		st := b.tb.settingsBtn.WindowBounds()
		if b.win.FrameHit(kit.Pt(60, 22)) != kit.FrameCaption || b.win.FrameHit(r.Center()) != kit.FrameClient || b.win.FrameHit(st.Center()) != kit.FrameClient {
			t.Error("drag region")
		}
		var names []string
		for _, c := range b.tb.winctl.Children() {
			names = append(names, c.Semantics.Name)
		}
		if b.tb.winctl.Semantics.Name != "Window" || fmt.Sprint(names) != "[Minimise Maximise Close]" {
			t.Errorf("group %q %v", b.tb.winctl.Semantics.Name, names)
		}
		click(b, b.tb.minBtn.Base(), 1)
		// Maximise turns into Restore (its glyph follows the window), and back
		click(b, b.tb.maxBtn.Base(), 1)
		b.loop.Send(kit.WindowStateEvent{State: kit.WindowState{Maximized: true}})
		if b.tb.maxBtn.Semantics.Name != "Restore" || b.tb.maxBtn.icon.name != icons.WinRestore {
			t.Error("no Restore while maximised")
		}
		click(b, b.tb.maxBtn.Base(), 1)
		b.loop.Send(kit.WindowStateEvent{State: kit.WindowState{}})
		if b.tb.maxBtn.Semantics.Name != "Maximise" || b.tb.maxBtn.icon.name != icons.WinMaximise {
			t.Error("no Maximise once restored")
		}
		click(b, b.tb.closeBtn.Base(), 1)
		if got := fmt.Sprint(b.loop.Host.Commands); got != fmt.Sprint([]kit.WindowCommand{kit.WindowMinimize, kit.WindowMaximize, kit.WindowRestore, kit.WindowClose}) {
			t.Errorf("commands %v", got)
		}
	})
	t.Run("follows a maximise by the system (snap, Win+Up) when the window resizes", func(t *testing.T) {
		b := openBar(t, kit.Size{W: 1100, H: 720}, true)
		if b.tb.maxBtn.Semantics.Name != "Maximise" {
			t.Fatal("not Maximise at first")
		}
		b.loop.Send(kit.WindowStateEvent{State: kit.WindowState{Maximized: true}})
		if b.tb.maxBtn.Semantics.Name != "Restore" {
			t.Error("the bar did not follow the system's maximise")
		}
	})
	t.Run("maximises on a double-click on empty bar space, once", func(t *testing.T) {
		// The caption's double click is the system's (HTCAPTION): it toggles once, and the bar
		// sends no command of its own that could toggle it back.
		b := openBar(t, kit.Size{W: 1100, H: 720}, true)
		if b.win.FrameHit(b.tb.word.WindowBounds().Center()) != kit.FrameCaption {
			t.Fatal("the brand is not caption")
		}
		click(b, b.tb.brand, 1)
		click(b, b.tb.brand, 2)
		// not on its controls: the search and Settings are the window's own
		click(b, b.tb.settingsBtn.Base(), 2)
		if b.win.FrameHit(b.tb.search.WindowBounds().Center()) != kit.FrameClient {
			t.Error("the search drags")
		}
		for _, c := range b.loop.Host.Commands {
			if c == kit.WindowMaximize || c == kit.WindowRestore {
				t.Errorf("the bar toggled maximise itself: %v", b.loop.Host.Commands)
			}
		}
	})
	t.Run("ignores double-clicks without a custom title bar", func(t *testing.T) {
		b := openBar(t, kit.Size{W: 1100, H: 720}, false)
		click(b, b.tb.brand, 2)
		if len(b.loop.Host.Commands) != 0 || b.win.FrameHit(kit.Pt(60, 22)) != kit.FrameClient {
			t.Errorf("commands %v", b.loop.Host.Commands)
		}
	})
}
