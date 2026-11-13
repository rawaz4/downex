//go:build windows

package win

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/win/uia"
	"downex/ui/win/uia/uiaclient"
)

// popupUIACycles is how many times the gate opens, walks and closes a popup.
const popupUIACycles = 200

// popupUIAPage is a window with a menu button (radio items, a check box item and a plain
// one) and a Select, as the speed-limit menu and the settings' selects are.
type popupUIAPage struct {
	ww      *widgetWindow
	trigger *widgets.Button
	mb      *widgets.MenuButton
	sel     *widgets.Select
}

func openPopupUIA(t *testing.T) *popupUIAPage {
	t.Helper()
	trigger := widgets.NewButton("Unlimited", "gauge", widgets.Secondary, widgets.SizeMd, nil)
	mb := widgets.NewMenuButton(trigger, "Global speed limit", []widgets.MenuItem{
		{ID: "0", Label: "Unlimited", Kind: widgets.ItemRadio, Checked: true, Heading: "Global speed limit"},
		{ID: "20", Label: "20 MiB/s", Kind: widgets.ItemRadio},
		{ID: "10", Label: "10 MiB/s", Kind: widgets.ItemRadio},
		{ID: "notify", Label: "Notify when done", Kind: widgets.ItemCheckbox, Checked: true, Separated: true},
		{ID: "custom", Label: "Custom…", Separated: true},
	}, nil)
	sel := widgets.NewSelect([]widgets.SelectOption{
		{Value: "none", Label: "Unlimited"}, {Value: "20", Label: "20 MiB/s"}, {Value: "10", Label: "10 MiB/s", Detail: "half"},
	}, "none", widgets.SelectOptions{Label: "Speed limit", Size: widgets.SelectSm}, nil)
	sel.Style.Width = kit.Px(200)
	ww := openWidgets(t, "DownexPopupUIATest", false, kit.Size{W: 520, H: 460}, mb, sel)
	t.Cleanup(uiaSweep)
	return &popupUIAPage{ww: ww, trigger: trigger, mb: mb, sel: sel}
}

// uiaInt reads an I4 property of e.
func uiaInt(e *uiaclient.Element, id int32) (int32, error) {
	v, err := e.GetCurrentPropertyValue(id)
	if err != nil {
		return 0, err
	}
	return v.I4(), nil
}

// uiaBool reads a VT_BOOL property of e.
func uiaBool(e *uiaclient.Element, id int32) (bool, error) {
	v, err := e.GetCurrentPropertyValue(id)
	if err != nil {
		return false, err
	}
	return v.Bool(), nil
}

// uiaItem is what the gate reads of a menu item or an option.
type uiaItem struct {
	name    string
	ct      int32
	toggle  int32 // -1: no Toggle pattern
	focused bool
	sel     bool
}

// readItems reads every element of control type ct under e, in order.
func readItems(c *uiaclient.Automation5, e *uiaclient.Element, ct int32) ([]uiaItem, error) {
	cond, err := c.IntCondition(uia.UIA_ControlTypePropertyId, ct)
	if err != nil {
		return nil, err
	}
	defer cond.Release()
	all, err := e.FindAll(uiaclient.TreeScope_Descendants, cond)
	if err != nil {
		return nil, err
	}
	defer all.Release()
	n, err := all.Len()
	if err != nil {
		return nil, err
	}
	out := make([]uiaItem, 0, n)
	for i := 0; i < n; i++ {
		el, err := all.GetElement(i)
		if err != nil {
			return nil, err
		}
		it := uiaItem{ct: ct, toggle: -1}
		it.name, _ = el.Name()
		it.focused, _ = uiaBool(el, uia.UIA_HasKeyboardFocusPropertyId)
		if tp, err := el.GetCurrentPattern(uia.UIA_TogglePatternId); err == nil && tp != nil {
			tp.Release()
			it.toggle, _ = uiaInt(el, uia.UIA_ToggleToggleStatePropertyId)
		}
		if ct == uia.UIA_ListItemControlTypeId {
			it.sel, _ = uiaBool(el, uia.UIA_SelectionItemIsSelectedPropertyId)
		}
		el.Release()
		out = append(out, it)
	}
	return out, nil
}

func focusedName(items []uiaItem) string {
	var names []string
	for _, it := range items {
		if it.focused {
			names = append(names, it.name)
		}
	}
	return strings.Join(names, "|")
}

// expandState reads the ExpandCollapse state of the owner's element named name with control
// type ct.
func expandState(c *uiaclient.Automation5, owner *uiaclient.Element, ct int32, name string) (int32, error) {
	nc, err := c.StringCondition(uia.UIA_NamePropertyId, name)
	if err != nil {
		return 0, err
	}
	defer nc.Release()
	all, err := owner.FindAll(uiaclient.TreeScope_Descendants, nc)
	if err != nil {
		return 0, err
	}
	defer all.Release()
	n, _ := all.Len()
	for i := 0; i < n; i++ {
		el, err := all.GetElement(i)
		if err != nil {
			return 0, err
		}
		got, _ := el.ControlType()
		if got != ct {
			el.Release()
			continue
		}
		st, err := uiaInt(el, uia.UIA_ExpandCollapseExpandCollapseStatePropertyId)
		el.Release()
		return st, err
	}
	return 0, fmt.Errorf("no element %q of control type %d in the owner", name, ct)
}

// openOn runs open on the UI thread and returns the popup it opened, with its HWND.
func (pg *popupUIAPage) openOn(open func()) (*popupWindow, uintptr, error) {
	w := pg.ww.w
	var p *popupWindow
	err := onUI(w, func() { // a posted function runs as a call into the root
		open()
		if ps := popupsOf[w]; len(ps) == 1 {
			p = ps[0]
		}
	})
	if err != nil {
		return nil, 0, err
	}
	if p == nil {
		return nil, 0, fmt.Errorf("no popup opened")
	}
	return p, uintptr(p.hwnd), nil
}

// closeAndCheck closes the popups on the UI thread and checks that the popup's provider was
// disconnected and that, once UI Automation has let go, none of its providers is alive.
func (pg *popupUIAPage) closeAndCheck(p *popupWindow, close func()) error {
	w := pg.ww.w
	var tree *uia.Tree
	var made bool
	if err := onUI(w, func() { tree, made = p.uia, p.uia != nil; close() }); err != nil {
		return err
	}
	var state string
	if err := onUI(w, func() {
		state = fmt.Sprintf("popups %d, closed %v, provider %v, done %v", len(popupsOf[w]), p.closed, p.uia != nil, p.uiaDone)
	}); err != nil {
		return err
	}
	if !made {
		return fmt.Errorf("the popup made no provider for the client")
	}
	if state != "popups 0, closed true, provider false, done true" {
		return fmt.Errorf("after close: %s", state)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		var held []string
		if err := onUI(w, func() { held = tree.Held() }); err != nil {
			return err
		}
		if len(held) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%d providers of the closed popup alive: %v", len(held), held)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestPopupUIAutomation is the gate for the popups' UI Automation (stage 1 re-check: menus
// and Select lists were invisible to it). Through the system's IUIAutomation client it opens
// the menu and the Select's list, walks them (Menu, MenuItems with their radio and check box
// Toggle states, List and ListItems with SelectionItem), reads the active item and option
// (the keyboard focus in the popup's tree, and a focus-changed event as it moves), reads the
// trigger's ExpandCollapse state in the owner's tree while open and after close, and checks
// that the popup's providers are disconnected and none is alive after close, and again over
// 200 open, walk and close cycles.
func TestPopupUIAutomation(t *testing.T) {
	pg := openPopupUIA(t)
	a, w, kw := pg.ww.a, pg.ww.w, pg.ww.kw
	hwnd := uintptr(w.hwnd)
	var cycles int
	var walked []int
	start := time.Now()
	clientRun(t, a, w, 8*time.Minute, func(c *uiaclient.Automation5) error {
		owner, err := c.ElementFromHandle(hwnd)
		if err != nil {
			return err
		}
		defer owner.Release()
		focus, err := uiaclient.NewFocusHandler(64)
		if err != nil {
			return err
		}
		defer focus.Release()
		if err := c.AddFocusChangedEventHandler(focus); err != nil {
			return err
		}
		// On every return, an early failure's too: UI Automation holds the handler until it is
		// removed, and a handler left alive fails the next test's count. Removing them twice
		// (once below, before the cycles) is harmless.
		defer func() { _ = c.RemoveAllEventHandlers() }()
		drain := func() {
			for len(focus.C) > 0 {
				<-focus.C
			}
		}

		// ---- the menu ----
		if st, err := expandState(c, owner, uia.UIA_ButtonControlTypeId, "Unlimited"); err != nil || st != uia.ExpandCollapseState_Collapsed {
			return fmt.Errorf("the closed menu's trigger: ExpandCollapse %d (%v), want Collapsed", st, err)
		}
		p, ph, err := pg.openOn(func() { kw.Focus(pg.trigger); pg.mb.OpenMenu(widgets.FocusFirst) })
		if err != nil {
			return err
		}
		root, err := c.ElementFromHandle(ph)
		if err != nil {
			return fmt.Errorf("the menu's popup window: %w", err)
		}
		menu, err := findType(c, root, uiaclient.TreeScope_Descendants, uia.UIA_MenuControlTypeId)
		if err != nil {
			root.Release()
			return fmt.Errorf("the menu: %w", err)
		}
		if name, _ := menu.Name(); name != "Global speed limit" {
			return fmt.Errorf("the menu's name is %q", name)
		}
		items, err := readItems(c, menu, uia.UIA_MenuItemControlTypeId)
		menu.Release()
		if err != nil {
			return err
		}
		want := []uiaItem{
			{name: "Unlimited", toggle: uia.ToggleState_On, focused: true},
			{name: "20 MiB/s", toggle: uia.ToggleState_Off},
			{name: "10 MiB/s", toggle: uia.ToggleState_Off},
			{name: "Notify when done", toggle: uia.ToggleState_On},
			{name: "Custom…", toggle: -1},
		}
		if len(items) != len(want) {
			return fmt.Errorf("menu items %+v, want %d", items, len(want))
		}
		for i, it := range items {
			if it.name != want[i].name || it.toggle != want[i].toggle || it.focused != want[i].focused {
				return fmt.Errorf("menu item %d is %+v, want %+v", i, it, want[i])
			}
		}
		t.Logf("menu: %+v", items)
		// Where the desktop's tree puts the owned popup (observed, not required: UI Automation
		// decides whether an owned top-level window shows under its owner).
		if n, err := countAll(c, owner, uiaclient.TreeScope_Descendants, uia.UIA_MenuControlTypeId); err == nil {
			t.Logf("menus under the owner window's element: %d", n)
		}
		if st, err := expandState(c, owner, uia.UIA_ButtonControlTypeId, "Unlimited"); err != nil || st != uia.ExpandCollapseState_Expanded {
			return fmt.Errorf("the open menu's trigger: ExpandCollapse %d (%v), want Expanded", st, err)
		}
		// the active item moves: a focus-changed event from the popup, and the keyboard focus
		drain()
		postKey(w.hwnd, uintptr(kit.KeyArrowDown))
		if err := waitEvent(focus, "focus changed to the next menu item", func(e uiaclient.Event) bool {
			return e.Name == "20 MiB/s" && e.ControlType == uia.UIA_MenuItemControlTypeId
		}); err != nil {
			return err
		}
		mi, err := find(c, root, uiaclient.TreeScope_Descendants, uia.UIA_NamePropertyId, "20 MiB/s")
		if err != nil {
			return err
		}
		f, _ := uiaBool(mi, uia.UIA_HasKeyboardFocusPropertyId)
		mi.Release()
		root.Release()
		if !f {
			return fmt.Errorf("the active menu item has no keyboard focus")
		}
		if err := pg.closeAndCheck(p, func() { pg.mb.CloseMenu(true) }); err != nil {
			return fmt.Errorf("the menu: %w", err)
		}
		if st, err := expandState(c, owner, uia.UIA_ButtonControlTypeId, "Unlimited"); err != nil || st != uia.ExpandCollapseState_Collapsed {
			return fmt.Errorf("the closed menu's trigger: ExpandCollapse %d (%v), want Collapsed", st, err)
		}

		// ---- the Select's list ----
		if st, err := expandState(c, owner, uia.UIA_ComboBoxControlTypeId, "Speed limit"); err != nil || st != uia.ExpandCollapseState_Collapsed {
			return fmt.Errorf("the closed Select: ExpandCollapse %d (%v), want Collapsed", st, err)
		}
		p, ph, err = pg.openOn(func() { kw.Focus(pg.sel); pg.sel.Open(widgets.FocusChecked) })
		if err != nil {
			return err
		}
		root, err = c.ElementFromHandle(ph)
		if err != nil {
			return fmt.Errorf("the list's popup window: %w", err)
		}
		list, err := findType(c, root, uiaclient.TreeScope_Descendants, uia.UIA_ListControlTypeId)
		if err != nil {
			root.Release()
			return fmt.Errorf("the list: %w", err)
		}
		if name, _ := list.Name(); name != "Speed limit" {
			return fmt.Errorf("the list's name is %q", name)
		}
		if sp, err := list.GetCurrentPattern(uia.UIA_SelectionPatternId); err != nil || sp == nil {
			return fmt.Errorf("the list has no Selection pattern (%v)", err)
		} else {
			sp.Release()
		}
		opts, err := readItems(c, list, uia.UIA_ListItemControlTypeId)
		list.Release()
		if err != nil {
			return err
		}
		t.Logf("list: %+v", opts)
		if len(opts) != 3 || !strings.HasPrefix(opts[0].name, "Unlimited") || !opts[0].sel || opts[1].sel || opts[2].sel {
			return fmt.Errorf("options %+v, want Unlimited (selected), 20 MiB/s, 10 MiB/s", opts)
		}
		if got := focusedName(opts); got != opts[0].name {
			return fmt.Errorf("the active option read %q, want %q", got, opts[0].name)
		}
		if st, err := expandState(c, owner, uia.UIA_ComboBoxControlTypeId, "Speed limit"); err != nil || st != uia.ExpandCollapseState_Expanded {
			return fmt.Errorf("the open Select: ExpandCollapse %d (%v), want Expanded", st, err)
		}
		drain()
		postKey(w.hwnd, uintptr(kit.KeyArrowDown))
		if err := waitEvent(focus, "focus changed to the next option", func(e uiaclient.Event) bool {
			return strings.HasPrefix(e.Name, "20 MiB/s") && e.ControlType == uia.UIA_ListItemControlTypeId
		}); err != nil {
			return err
		}
		var active string
		if err := onUI(w, func() { active = pg.sel.Active() }); err != nil {
			return err
		}
		opts, err = readItems(c, root, uia.UIA_ListItemControlTypeId)
		root.Release()
		if err != nil {
			return err
		}
		if got := focusedName(opts); !strings.HasPrefix(got, "20 MiB/s") || active != "20" {
			return fmt.Errorf("after ArrowDown the active option reads %q (Select.Active %q)", got, active)
		}
		if err := pg.closeAndCheck(p, func() { pg.sel.Close() }); err != nil {
			return fmt.Errorf("the Select's list: %w", err)
		}
		if st, err := expandState(c, owner, uia.UIA_ComboBoxControlTypeId, "Speed limit"); err != nil || st != uia.ExpandCollapseState_Collapsed {
			return fmt.Errorf("the closed Select: ExpandCollapse %d (%v), want Collapsed", st, err)
		}
		if err := c.RemoveAllEventHandlers(); err != nil {
			return err
		}

		// ---- open, walk and close, over and over ----
		wk, err := c.RawViewWalker()
		if err != nil {
			return err
		}
		defer wk.Release()
		for i := 0; i < popupUIACycles; i++ {
			open, shut := func() { kw.Focus(pg.trigger); pg.mb.OpenMenu(widgets.FocusFirst) }, func() { pg.mb.CloseMenu(true) }
			if i%2 == 1 {
				open, shut = func() { kw.Focus(pg.sel); pg.sel.Open(widgets.FocusChecked) }, func() { pg.sel.Close() }
			}
			p, ph, err := pg.openOn(open)
			if err != nil {
				return fmt.Errorf("cycle %d: %w", i, err)
			}
			root, err := c.ElementFromHandle(ph)
			if err != nil {
				return fmt.Errorf("cycle %d: %w", i, err)
			}
			n, err := walk(wk, root, 0)
			root.Release()
			if err != nil {
				return fmt.Errorf("cycle %d: walk: %w", i, err)
			}
			walked = append(walked, n)
			if err := pg.closeAndCheck(p, shut); err != nil {
				return fmt.Errorf("cycle %d: %w", i, err)
			}
			cycles++
		}
		return nil
	})
	took := time.Since(start)
	runtime.GC()
	t.Logf("%d open, walk and close cycles in %v; elements walked per popup %d to %d; calls refused %d, panics %d",
		cycles, took.Round(time.Millisecond), minOf(walked), maxOf(walked), uia.Refused(), uia.Panics())
	if cycles != popupUIACycles {
		t.Fatalf("%d cycles of %d", cycles, popupUIACycles)
	}
	if minOf(walked) < 4 {
		t.Errorf("a popup walk saw %d elements", minOf(walked))
	}
	if n := uia.Refused(); n != 0 {
		t.Errorf("%d provider calls arrived off the UI thread", n)
	}
	if n := uia.Panics(); n != 0 {
		t.Errorf("%d panics in provider methods: %s", n, uia.LastPanic())
	}
	if len(popupsOf[w]) != 0 {
		t.Errorf("%d popups left open", len(popupsOf[w]))
	}
	// The owner goes too: no provider of the process is left.
	w.Close()
	settled := pumpFor(t, a, w, 10*time.Second, func() bool { runtime.GC(); return w.closed && uia.Live() == 0 })
	t.Logf("providers alive after the owner closed: %d; event handlers alive: %d", uia.Live(), uiaclient.LiveHandlers())
	if !settled {
		t.Errorf("%d providers alive after every popup and the owner closed", uia.Live())
	}
	if n := uiaclient.LiveHandlers(); n != 0 {
		t.Errorf("%d event handlers still alive", n)
	}
}
