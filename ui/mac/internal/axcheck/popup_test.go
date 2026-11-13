//go:build darwin && cgo

package axcheck

import (
	"strings"
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/mac"
	"downex/ui/mac/fieldtest"
	"downex/ui/views"
)

// popupPage is a window with a Select and a button a menu opens from.
type popupPage struct {
	win  *kit.Window
	sel  *widgets.Select
	more *kit.Element
}

const popupTitle = "Downex popups"

func newPopupPage() *popupPage {
	p := &popupPage{}
	system := tokens.System
	theme := &views.Theme{Fixed: &system}
	p.sel = widgets.NewSelect([]widgets.SelectOption{
		{Value: "video", Label: "Video"}, {Value: "music", Label: "Music"}, {Value: "docs", Label: "Documents"},
	}, "music", widgets.SelectOptions{Label: "Category"}, nil)
	p.sel.Style.Width = kit.Px(200)
	p.more = &kit.Element{Style: kit.Style{Width: kit.Px(80), Height: kit.Px(28)}}
	p.more.Semantics = kit.Semantics{Role: kit.RoleButton, Name: "More", Actions: kit.ActionInvoke}
	col := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, RowGap: kit.Px(12), Padding: kit.EdgesAll(kit.Px(20))}}
	col.Add(p.sel, p.more)
	scope := widgets.NewScope(theme, col)
	scope.Style = kit.Style{Display: kit.DisplayBlock, Width: kit.Pct(100), Height: kit.Pct(100)}
	p.win = kit.NewWindow(scope, kit.WindowOptions{Title: popupTitle, Palette: theme.Palette})
	return p
}

// axuiFocused is the application's focused element as the server answers VoiceOver.
func axuiFocused(t *testing.T) *axui {
	app := axuiApp()
	defer app.release()
	f, err := app.elem("AXFocusedUIElement")
	if err != 0 || f == nil {
		t.Errorf("AXFocusedUIElement: error %d", err)
		return nil
	}
	return f
}

// describe is an element's role, subrole and name as VoiceOver reads them.
func describe(e *axui) string {
	if e == nil {
		return "<nil>"
	}
	r := e.role()
	sr, _ := e.str("AXSubrole")
	ti, _ := e.str("AXTitle")
	de, _ := e.str("AXDescription")
	return strings.TrimSpace(r + "/" + sr + " " + ti + " | " + de)
}

// ancestors is e's AXParent chain up to its window (described), and whether it reached an
// AXWindow without an error.
func ancestors(e *axui) ([]string, bool) {
	var out []string
	cur := e
	for i := 0; i < 8; i++ {
		p, err := cur.elem("AXParent")
		if cur != e {
			cur.release()
		}
		if err != 0 || p == nil {
			return append(out, "error"), false
		}
		out = append(out, describe(p))
		if p.role() == "AXWindow" {
			p.release()
			return out, true
		}
		cur = p
	}
	return out, false
}

// TestPopupTreeAsVoiceOverReads opens a Select's list and a menu over a window and reads
// them through the accessibility server as VoiceOver does: the application's focused
// element is the popup's active item while it is open, it follows the arrows, its parent
// chain reaches a window, the popup's panel lists its items, and the owner's focus comes
// back when it closes.
func TestPopupTreeAsVoiceOverReads(t *testing.T) {
	p := newPopupPage()
	var w *mac.Window
	onMain(t, func() {
		var err error
		w, err = app.NewWindow(p.win, mac.WindowOptions{Title: popupTitle, Size: kit.Size{W: 420, H: 320}})
		if err != nil {
			fatal(t, err)
		}
	})
	defer onMain(t, func() { w.Close() })
	var key bool
	onMain(t, func() { key = fieldtest.Find(popupTitle).Activate() })
	t.Logf("window key: %v", key)
	onMain(t, func() { p.win.Focus(p.sel) })
	// A client reads the window first (the bridge is made at the first request).
	onMain(t, func() {
		win, err := axuiWindow(popupTitle)
		if err != nil {
			fatal(t, err)
		}
		t.Logf("window:\n%s", axuiDump(win, 4))
		win.release()
		if f := axuiFocused(t); f != nil {
			t.Logf("focused before: %s", describe(f))
			f.release()
		}
	})

	labelOf := map[string]string{"video": "Video", "music": "Music", "docs": "Documents"}
	var hosts int
	onMain(t, func() { hosts = mac.AXRegistered() })

	// ---- the Select's list ----
	onMain(t, func() { p.sel.Open(widgets.FocusFirst) })
	eventually(t, "the list to open", func() bool { return p.sel.IsOpen() && p.sel.List() != nil })
	time.Sleep(2 * 100 * time.Millisecond)
	onMain(t, func() {
		f := axuiFocused(t)
		if f == nil {
			return
		}
		defer f.release()
		d := describe(f)
		t.Logf("focused in the list: %s", d)
		if !strings.HasPrefix(d, "AXStaticText") || !strings.Contains(d, labelOf[p.sel.Active()]) {
			t.Errorf("the focused element is %s, want the active option %q", d, labelOf[p.sel.Active()])
		}
		if sel, _ := f.str("AXSelected"); sel != "" {
			t.Logf("AXSelected %q", sel)
		}
		chain, ok := ancestors(f)
		t.Logf("its ancestors: %q", chain)
		if !ok || !strings.HasPrefix(chain[0], "AXList") {
			t.Errorf("the option's ancestors %q: want an AXList up to a window", chain)
		}
		w2, err := f.elem("AXWindow")
		if err != 0 || w2 == nil {
			t.Errorf("the option's AXWindow: error %d", err)
		} else {
			kids, e := w2.list("AXChildren")
			t.Logf("its window %s, children error %d:\n%s", describe(w2), e, axuiDump(w2, 4))
			if e != 0 || len(kids) == 0 {
				t.Errorf("the option's window lists no children (error %d)", e)
			}
			releaseAll(kids)
			w2.release()
		}
	})
	first := ""
	onMain(t, func() { first = p.sel.Active() })
	onMain(t, func() { p.sel.HandleKey(kit.KeyEvent{Action: kit.KeyDown, Key: kit.KeyArrowDown}) })
	time.Sleep(1 * 100 * time.Millisecond)
	onMain(t, func() {
		f := axuiFocused(t)
		if f == nil {
			return
		}
		defer f.release()
		d := describe(f)
		t.Logf("after ArrowDown: %s (active %q, was %q)", d, p.sel.Active(), first)
		if p.sel.Active() == first || !strings.Contains(d, labelOf[p.sel.Active()]) {
			t.Errorf("the focused element %s does not follow the active option %q", d, p.sel.Active())
		}
	})
	onMain(t, func() { p.sel.Close() })
	eventually(t, "the list to close", func() bool { return !p.sel.IsOpen() })
	time.Sleep(1 * 100 * time.Millisecond)
	onMain(t, func() {
		if f := axuiFocused(t); f != nil {
			d := describe(f)
			t.Logf("focused after the list closed: %s", d)
			if !strings.HasPrefix(d, "AXPopUpButton") && !strings.HasPrefix(d, "AXComboBox") {
				t.Errorf("the focus did not come back to the Select: %s", d)
			}
			f.release()
		}
	})

	// ---- a menu ----
	var m *widgets.Menu
	onMain(t, func() {
		var err error
		m, err = widgets.OpenMenu(p.more, widgets.MenuOptions{Label: "Actions", Items: []widgets.MenuItem{
			{ID: "open", Label: "Open"}, {ID: "copy", Label: "Copy address", Shortcut: "Meta+C"},
			{ID: "wrap", Label: "Wrap names", Kind: widgets.ItemCheckbox, Checked: true},
		}, Anchor: &widgets.MenuAnchor{El: p.more}, Initial: widgets.FocusFirst})
		if err != nil {
			fatal(t, err)
		}
	})
	defer onMain(t, func() { m.Close() })
	time.Sleep(2 * 100 * time.Millisecond)
	onMain(t, func() {
		f := axuiFocused(t)
		if f == nil {
			return
		}
		defer f.release()
		d := describe(f)
		t.Logf("focused in the menu: %s", d)
		if !strings.HasPrefix(d, "AXMenuItem") || !strings.Contains(d, "Open") {
			t.Errorf("the focused element is %s, want the menu item Open", d)
		}
		chain, ok := ancestors(f)
		t.Logf("its ancestors: %q", chain)
		if !ok || !strings.HasPrefix(chain[0], "AXMenu/") {
			t.Errorf("the item's ancestors %q: want an AXMenu up to a window", chain)
		}
		menu, _ := f.elem("AXParent")
		if menu != nil {
			items, e := menu.list("AXChildren")
			var names []string
			for _, it := range items {
				names = append(names, describe(it))
			}
			releaseAll(items)
			menu.release()
			t.Logf("the menu's items: %q", names)
			if e != 0 || len(names) != 3 {
				t.Errorf("the menu lists %d items (error %d), want 3", len(names), e)
			}
		}
	})
	onMain(t, func() { m.Close() })
	time.Sleep(1 * 100 * time.Millisecond)
	onMain(t, func() {
		if n := mac.AXRegistered(); n != hosts {
			t.Errorf("%d accessibility hosts after the popups closed, %d before they opened", n, hosts)
		}
	})
}
