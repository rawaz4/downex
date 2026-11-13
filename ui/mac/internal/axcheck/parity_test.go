//go:build darwin && cgo

package axcheck

import (
	"slices"
	"testing"
	"unsafe"

	"downex/ui/kit"
	"downex/ui/mac"
)

// parityPage has the widgets whose exposure the review compared with today's WebKit
// window (a WebKit1 probe of the same ARIA on this Mac): checkable menu items, a status
// region without a name, a listbox option, a group, and a multi-line field's box.
type parityPage struct {
	win   *kit.Window
	notes *kit.Element
}

const parityTitle = "Downex parity"

func newParityPage() *parityPage {
	p := &parityPage{}
	box := func(s kit.Semantics, kids ...kit.Widget) *kit.Element {
		e := &kit.Element{Style: kit.Style{Height: kit.Px(24), Display: kit.DisplayFlex}}
		e.Semantics = s
		e.Add(kids...)
		return e
	}
	menu := box(kit.Semantics{Role: kit.RoleMenu, Name: "Sort"},
		box(kit.Semantics{Role: kit.RoleMenuItemCheckBox, Name: "Name", Toggle: kit.ToggleOn}),
		box(kit.Semantics{Role: kit.RoleMenuItemRadio, Name: "Dark", Toggle: kit.ToggleOff}),
		box(kit.Semantics{Role: kit.RoleMenuItem, Name: "Open", Shortcut: "Meta+K"}))
	status := box(kit.Semantics{Role: kit.RoleStatus}, kit.NewLabel("3 downloads", font, kit.TextOptions{}))
	list := box(kit.Semantics{Role: kit.RoleListBox, Name: "Quality"},
		box(kit.Semantics{Role: kit.RoleOption, Name: "1080p", PosInSet: 1, SetSize: 2, States: kit.NodeSelected}))
	group := box(kit.Semantics{Role: kit.RoleGroup, Name: "Limits"},
		box(kit.Semantics{Role: kit.RoleButton, Name: "Apply", Actions: kit.ActionInvoke}))
	p.notes = &kit.Element{Style: kit.Style{Width: kit.Px(200), Height: kit.Px(60)}}
	p.notes.Semantics = kit.Semantics{Role: kit.RoleTextField, Name: "Notes", AutomationID: "notes"}
	pg := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn}}
	pg.Add(menu, status, list, group, p.notes)
	p.win = kit.NewWindow(pg, kit.WindowOptions{Title: parityTitle})
	return p
}

// openParity opens the page, with a multi-line field placed in the Notes box when withArea.
func openParity(t *testing.T, withArea bool) (*mac.Window, *parityPage, unsafe.Pointer) {
	t.Helper()
	p := newParityPage()
	var w *mac.Window
	var area unsafe.Pointer
	onMain(t, func() {
		var err error
		w, err = app.NewWindow(p.win, mac.WindowOptions{Title: parityTitle, Size: kit.Size{W: 500, H: 400}, NoActivate: true})
		if err != nil {
			fatal(t, err)
		}
		win := w.AXWindow()
		if !withArea {
			return
		}
		node := find(win, 4, func(e *mac.AXElement) bool { return e.String("AXIdentifier") == "notes" })
		if node == nil {
			fatalf(t, "no notes node:\n%s", mac.AXDump(win, 4))
		}
		fr, _ := node.Attribute("AXFrame").(kit.Rect)
		area = addTextArea(win.Object(), fr)
	})
	t.Cleanup(func() { onMain(t, func() { w.Close() }) })
	return w, p, area
}

// TestTextAreaAppearsOnce: a multi-line field shows as its text view alone (AXTextArea,
// as WebKit's textarea), in its node's place, labelled once; its scroll view never shows.
func TestTextAreaAppearsOnce(t *testing.T) {
	w, _, area := openParity(t, true)
	onMain(t, func() {
		win := w.AXWindow()
		var areas, scrolls, nodes int
		var walk func(e *mac.AXElement, d int)
		walk = func(e *mac.AXElement, d int) {
			for _, c := range e.Children() {
				switch {
				case c.String("AXRole") == "AXTextArea":
					areas++
					if c.Object() != documentOf(area) {
						t.Errorf("the text area is %s, not the field's text view", c.Class())
					}
					if c.String("AXDescription") != "Notes" {
						t.Errorf("text area label %q", c.String("AXDescription"))
					}
					if !c.Element("AXParent").Same(e) {
						t.Errorf("the text area's parent is %s %s, not the element that lists it", c.Element("AXParent").Class(), c.Element("AXParent").String("AXRole"))
					}
				case c.String("AXRole") == "AXScrollArea":
					scrolls++
				case c.String("AXIdentifier") == "notes":
					nodes++
				}
				if d > 0 {
					walk(c, d-1)
				}
			}
		}
		walk(win, 6)
		if areas != 1 || scrolls != 0 || nodes != 0 {
			t.Errorf("text area %d times, scroll areas %d, its node %d times:\n%s", areas, scrolls, nodes, mac.AXDump(win, 6))
		}
		removeField(area)
	})
}

// TestWebKitParity: what WebKit gives VoiceOver for the same ARIA in today's window (a
// WebKit1 probe on this Mac), through the in-process client; WebKit's extra attributes
// through accessibilityAttributeNames/accessibilityAttributeValue: themselves, the route by
// which the system's server reads them (a standalone AppKit program on this Mac, read with
// AXUIElement, saw an element's protocol attributes and these overrides together).
func TestWebKitParity(t *testing.T) {
	w, _, _ := openParity(t, false)
	onMain(t, func() {
		win := w.AXWindow()
		defer win.Release()
		byTitle := func(role, title string) *mac.AXElement {
			e := find(win, 6, func(e *mac.AXElement) bool { return e.String("AXRole") == role && e.String("AXTitle") == title })
			if e == nil {
				fatalf(t, "no %s %q:\n%s", role, title, mac.AXDump(win, 6))
			}
			return e
		}
		hasName := func(e *mac.AXElement, n string) bool { return slices.Contains(e.LegacyAttributeNames(), n) }

		// Checkable menu items: AXValue 1/0 and the mark while checked.
		on := byTitle("AXMenuItem", "Name")
		if v, _ := on.Number("AXValue"); v != 1 {
			t.Errorf("checked item value %v", on.Attribute("AXValue"))
		}
		if v := on.LegacyAttribute("AXMenuItemMarkChar"); v != "\u2713" || !hasName(on, "AXMenuItemMarkChar") {
			t.Errorf("checked item mark %v; names %v", v, on.LegacyAttributeNames())
		}
		if s := on.String("AXDescription"); s != "Name" {
			t.Errorf("menu item description %q", s)
		}
		off := byTitle("AXMenuItem", "Dark")
		if v, ok := off.Number("AXValue"); !ok || v != 0 {
			t.Errorf("unchecked item value %v", off.Attribute("AXValue"))
		}
		if v := off.LegacyAttribute("AXMenuItemMarkChar"); v != nil {
			t.Errorf("unchecked item mark %v", v)
		}
		plain := byTitle("AXMenuItem", "Open")
		if v := plain.LegacyAttribute("AXKeyShortcutsValue"); v != "Meta+K" {
			t.Errorf("shortcut %v", v)
		}
		if v := plain.Attribute("AXValue"); v != nil {
			t.Errorf("a plain item has a value %v", v)
		}
		if v := plain.LegacyAttribute("AXElementBusy"); v != 0.0 {
			t.Errorf("AXElementBusy %v", v)
		}
		// An attribute that is not an extra goes on to AppKit's own answer.
		if v := plain.LegacyAttribute("AXNoSuchAttribute"); v != nil {
			t.Errorf("unknown attribute %v", v)
		}

		// The status region: unnamed, its text in its child.
		st := find(win, 6, role("AXGroup", "AXApplicationStatus"))
		if st == nil {
			fatal(t, "no status region")
		}
		if st.String("AXDescription") != "" || st.String("AXTitle") != "" {
			t.Errorf("status named %q / %q", st.String("AXTitle"), st.String("AXDescription"))
		}
		if kids := st.Children(); len(kids) != 1 || kids[0].String("AXRole") != "AXStaticText" ||
			kids[0].String("AXValue") != "3 downloads" {
			t.Errorf("status children:\n%s", mac.AXDump(st, 2))
		}

		// The option: its name as its title, an empty value, its place in the set.
		opt := byTitle("AXStaticText", "1080p")
		if v := opt.Attribute("AXValue"); v != "" {
			t.Errorf("option value %v", v)
		}
		if v := opt.LegacyAttribute("AXARIAPosInSet"); v != 1.0 {
			t.Errorf("option position %v", v)
		}
		if v := opt.LegacyAttribute("AXARIASetSize"); v != 2.0 {
			t.Errorf("option set size %v", v)
		}
		if v, _ := opt.Number("AXSelected"); v != 1 {
			t.Error("option not selected")
		}

		// role=group; a button's AXInvalid.
		if g := byTitle("AXGroup", "Limits"); g.String("AXSubrole") != "AXApplicationGroup" {
			t.Errorf("group subrole %q", g.String("AXSubrole"))
		}
		if v := byTitle("AXButton", "Apply").LegacyAttribute("AXInvalid"); v != "false" {
			t.Errorf("button AXInvalid %v", v)
		}
	})
}

// TestClosedWindowNotKept: once a window that a client read has closed, accessibility
// keeps nothing of it (the review's leak: a set of closed hosts kept every Window), and a
// late request about it registers nothing again.
func TestClosedWindowNotKept(t *testing.T) {
	var before int
	onMain(t, func() {
		before = mac.AXRegistered()
		p := newParityPage()
		w, err := app.NewWindow(p.win, mac.WindowOptions{Title: "closing", Size: kit.Size{W: 300, H: 200}, NoActivate: true})
		if err != nil {
			fatal(t, err)
		}
		win := w.AXWindow()
		mac.AXDump(win, 6) // a client reads it: its bridge is made
		if w.AXElements() == 0 || mac.AXRegistered() != before+1 {
			fatalf(t, "not registered: %d elements, %d hosts", w.AXElements(), mac.AXRegistered())
		}
		w.Close()
		if n := mac.AXRegistered(); n != before {
			t.Errorf("%d hosts registered after the close, %d before", n, before)
		}
		// A client still holding the window asks again: nothing is registered.
		_ = win.Children()
		_ = win.Element("AXFocusedUIElement")
		win.Release()
		if n := mac.AXRegistered(); n != before {
			t.Errorf("a late request registered the closed window again (%d hosts)", n)
		}
	})
}

// TestAnnouncementOnItsWindow: an announcement is posted about the window whose content
// announced, not about the key window.
func TestAnnouncementOnItsWindow(t *testing.T) {
	w, p, _ := openParity(t, false)
	onMain(t, func() {
		mac.AXDump(w.AXWindow(), 2)
		p.win.Announce("45% of report.pdf", kit.LivePolite, nil)
		s := mac.AXStats()
		if s.LastAnnouncement != "45% of report.pdf" {
			fatalf(t, "announcement %+v", s)
		}
		win := w.AXWindow()
		defer win.Release()
		if s.LastTarget != win.Object() {
			t.Error("the announcement is not about its own window")
		}
	})
}
