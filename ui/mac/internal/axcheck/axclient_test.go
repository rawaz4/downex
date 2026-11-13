//go:build darwin && cgo

package axcheck

import (
	"strings"
	"testing"
	"unsafe"

	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/mac"
	"downex/ui/views"
)

// The real AXUIElement client (axclient.go) reads this process on the main thread, where
// the accessibility server's requests are served (the in-process client of ui/mac calls
// the same methods, but not through the server's request, so it never saw what follows).

// TestSystemClientWithFields: the server answers a window's AXChildren, again and again,
// while native fields are shown (a single-line field in its node's box, the title bar's
// search field, a multi-line field elsewhere). Before the fix, decorating a field inside
// the request (reading a cell's unset label back) made the server fail the whole request
// with kAXErrorAttributeUnsupported: VoiceOver saw an empty window.
func TestSystemClientWithFields(t *testing.T) {
	t.Logf("AXIsProcessTrusted = %v (not needed for this process's own windows)", axuiTrusted())
	w, _ := open(t)
	var field, area unsafe.Pointer
	onMain(t, func() {
		win := w.AXWindow()
		node := find(win, 4, func(e *mac.AXElement) bool { return e.String("AXIdentifier") == "rename" })
		if node == nil {
			fatalf(t, "no rename node:\n%s", mac.AXDump(win, 4))
		}
		fr, _ := node.Attribute("AXFrame").(kit.Rect)
		field = addField(win.Object(), fr)
		area = addTextArea(win.Object(), kit.R(fr.Min.X, fr.Min.Y-120, fr.Min.X+200, fr.Min.Y-60))
	})
	defer onMain(t, func() { removeField(field); removeField(area) })
	refused := mac.AXStats().Refused
	onMain(t, func() {
		for i := 0; i < 5; i++ {
			win, err := axuiWindow("Downex")
			if err != nil {
				fatal(t, err)
			}
			kids, e := win.list("AXChildren")
			n, ce := win.count("AXChildren")
			if e != 0 || len(kids) == 0 || ce != 0 || n != len(kids) {
				t.Errorf("read %d: AXChildren error %d (%d children), count %d error %d", i, e, len(kids), n, ce)
			}
			releaseAll(kids)
			labels := map[string]bool{}
			var walk func(e *axui, d int)
			walk = func(e *axui, d int) {
				ks, err := e.list("AXChildren")
				if err != 0 && err != -25212 { // -25212: no value (a title bar button)
					t.Errorf("read %d: %s's AXChildren: error %d", i, e.role(), err)
				}
				for _, k := range ks {
					if r := k.role(); r == "AXTextField" {
						l, _ := k.str("AXDescription")
						labels[l] = true
						if p, err := k.elem("AXParent"); err != 0 || !p.same(e) {
							t.Errorf("read %d: field %q: parent error %d, same %v", i, l, err, p.same(e))
						} else {
							p.release()
						}
					}
					if d > 0 {
						walk(k, d-1)
					}
				}
				releaseAll(ks)
			}
			walk(win, 6)
			if !labels["Rename"] || !labels["Search downloads by name or URL"] {
				t.Errorf("read %d: fields reached %v:\n%s", i, labels, axuiDump(win, 6))
			}
			win.release()
		}
	})
	if r := mac.AXStats().Refused; r != refused {
		t.Errorf("%d requests refused (not on the main thread)", r-refused)
	}
}

// dialogPage is a dialog group holding a multi-line field's box (a textarea in a dialog).
func dialogPage() *kit.Window {
	notes := &kit.Element{Style: kit.Style{Width: kit.Px(220), Height: kit.Px(64)}}
	notes.Semantics = kit.Semantics{Role: kit.RoleTextField, Name: "Notes", Description: "Shown in the list",
		AutomationID: "notes", States: kit.NodeRequired}
	ok := &kit.Element{Style: kit.Style{Width: kit.Px(60), Height: kit.Px(24)}}
	ok.Semantics = kit.Semantics{Role: kit.RoleButton, Name: "Save", Actions: kit.ActionInvoke}
	dlg := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, Padding: kit.EdgesAll(kit.Px(20))}}
	dlg.Semantics = kit.Semantics{Role: kit.RoleDialog, Name: "Edit notes"}
	dlg.Add(notes, ok)
	pg := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn}}
	pg.Add(dlg)
	return kit.NewWindow(pg, kit.WindowOptions{Title: "Downex dialog"})
}

// TestSystemClientTextAreaInGroup: a multi-line field inside a dialog is the dialog's
// child for the server too (its AXParent is the dialog, not the window), labelled once,
// with its help and identifier, and its scroll view never shows.
func TestSystemClientTextAreaInGroup(t *testing.T) {
	var w *mac.Window
	var area unsafe.Pointer
	onMain(t, func() {
		var err error
		w, err = app.NewWindow(dialogPage(), mac.WindowOptions{Title: "Downex dialog", Size: kit.Size{W: 420, H: 260}, NoActivate: true})
		if err != nil {
			fatal(t, err)
		}
		win := w.AXWindow()
		node := find(win, 4, func(e *mac.AXElement) bool { return e.String("AXIdentifier") == "notes" })
		if node == nil {
			fatalf(t, "no notes node:\n%s", mac.AXDump(win, 4))
		}
		fr, _ := node.Attribute("AXFrame").(kit.Rect)
		area = addTextArea(win.Object(), fr)
	})
	defer onMain(t, func() { removeField(area); w.Close() })
	onMain(t, func() {
		for i := 0; i < 3; i++ {
			win, err := axuiWindow("Downex dialog")
			if err != nil {
				fatal(t, err)
			}
			kids, e := win.list("AXChildren")
			var dlg *axui
			for _, k := range kids {
				if d, _ := k.str("AXDescription"); k.role() == "AXGroup" && d == "Edit notes" {
					dlg = k
				}
			}
			if e != 0 || dlg == nil {
				fatalf(t, "read %d: window children error %d, dialog %v:\n%s", i, e, dlg != nil, axuiDump(win, 4))
			}
			inner, e := dlg.list("AXChildren")
			var ta *axui
			for _, k := range inner {
				switch k.role() {
				case "AXTextArea":
					ta = k
				case "AXScrollArea":
					t.Errorf("read %d: the scroll view shows", i)
				}
			}
			if e != 0 || ta == nil {
				fatalf(t, "read %d: dialog children error %d, no text area:\n%s", i, e, axuiDump(win, 4))
			}
			if p, err := ta.elem("AXParent"); err != 0 || !p.same(dlg) {
				r := ""
				if p != nil {
					r = p.role()
				}
				t.Errorf("read %d: the text area's parent is %q (error %d), not the dialog", i, r, err)
			}
			for name, want := range map[string]string{"AXDescription": "Notes", "AXHelp": "Shown in the list", "AXIdentifier": "notes"} {
				if got, err := ta.str(name); got != want {
					t.Errorf("read %d: %s %q (error %d), want %q", i, name, got, err, want)
				}
			}
			// The in-process client sees the same parent.
			if i == 0 {
				iw := w.AXWindow()
				el := find(iw, 4, func(e *mac.AXElement) bool { return e.String("AXRole") == "AXTextArea" })
				if el == nil || el.Element("AXParent").String("AXDescription") != "Edit notes" {
					t.Errorf("in-process: the text area's parent is not the dialog:\n%s", mac.AXDump(iw, 4))
				}
			}
			releaseAll(inner)
			releaseAll(kids)
			win.release()
		}
	})
}

// TestSystemClientGridColumns: the grid's columns as WebKit gives a role=grid table:
// AXColumns lists one AXColumn per column (also in AXVisibleColumns, and after the rows in
// AXChildren), each with its AXIndex, its header cell as AXHeader, the table as its
// parent, and as its children the cell at its index of every row laid out, header first.
func TestSystemClientGridColumns(t *testing.T) {
	w, _ := open(t)
	onMain(t, func() {
		win, err := axuiWindow("Downex")
		if err != nil {
			fatal(t, err)
		}
		defer win.release()
		var table *axui
		var walk func(e *axui, d int)
		walk = func(e *axui, d int) {
			ks, _ := e.list("AXChildren")
			for _, k := range ks {
				if de, _ := k.str("AXDescription"); table == nil && k.role() == "AXTable" && de == "Downloads" {
					table = k
					continue
				}
				if d > 0 {
					walk(k, d-1)
				}
				k.release()
			}
		}
		walk(win, 3)
		if table == nil {
			fatalf(t, "no Downloads table:\n%s", axuiDump(win, 3))
		}
		defer table.release()
		cols, cerr := table.list("AXColumns")
		defer releaseAll(cols)
		if n, _ := table.num("AXColumnCount"); cerr != 0 || len(cols) != 4 || n != 4 {
			fatalf(t, "AXColumns: %d (error %d), AXColumnCount %v", len(cols), cerr, n)
		}
		vis, _ := table.list("AXVisibleColumns")
		if len(vis) != 4 {
			t.Errorf("AXVisibleColumns: %d", len(vis))
		}
		releaseAll(vis)
		rows, _ := table.list("AXRows")
		defer releaseAll(rows)
		kids, _ := table.list("AXChildren")
		defer releaseAll(kids)
		if len(kids) != len(rows)+4 || !kids[len(kids)-1].same(cols[3]) || kids[len(rows)].role() != "AXColumn" {
			t.Errorf("the table's children: %d for %d rows and 4 columns, columns last %v", len(kids), len(rows), len(kids) > 0 && kids[len(kids)-1].same(cols[3]))
		}
		for i, c := range cols {
			if r := c.role(); r != "AXColumn" {
				t.Errorf("column %d role %q", i, r)
			}
			if rd, _ := c.str("AXRoleDescription"); rd != "column" {
				t.Errorf("column %d role description %q", i, rd)
			}
			if x, err := c.num("AXIndex"); err != 0 || int(x) != i {
				t.Errorf("column %d AXIndex %v (error %d)", i, x, err)
			}
			if p, _ := c.elem("AXParent"); !p.same(table) {
				t.Errorf("column %d: its parent is not the table", i)
			} else {
				p.release()
			}
			h, err := c.elem("AXHeader")
			want := []string{"Name", "Size", "Progress", "Status"}[i]
			if err != 0 || h == nil {
				t.Errorf("column %d AXHeader: error %d", i, err)
			} else {
				if d := describe(h); !strings.Contains(d, want) {
					t.Errorf("column %d header %s, want %q", i, d, want)
				}
				h.release()
			}
			cells, err := c.list("AXChildren")
			if err != 0 || len(cells) != len(rows) {
				t.Errorf("column %d: %d cells (error %d) for %d rows", i, len(cells), err, len(rows))
			} else {
				if d := describe(cells[0]); !strings.Contains(d, want) {
					t.Errorf("column %d's first cell %s, want its header %q", i, d, want)
				}
				if d := describe(cells[1]); !strings.Contains(d, cellText(0, i)) {
					t.Errorf("column %d's second cell %s, want %q", i, d, cellText(0, i))
				}
			}
			releaseAll(cells)
		}
	})
	_ = w
}

// TestSystemClientFieldset: a SegmentedControl (SegmentedControl.svelte's <fieldset> of
// radios) is what WebKit gives VoiceOver for it: an AXGroup/AXFieldset ("group") named by
// its label, with its options as radio buttons in it.
func TestSystemClientFieldset(t *testing.T) {
	system := tokens.System
	theme := &views.Theme{Fixed: &system}
	seg := widgets.NewSegmentedControl([]widgets.SegmentOption{{Value: "light", Label: "Light"}, {Value: "dark", Label: "Dark"}},
		"light", widgets.SegmentedOptions{AriaLabel: "Theme"}, nil)
	scope := widgets.NewScope(theme, seg)
	scope.Style = kit.Style{Display: kit.DisplayBlock, Width: kit.Pct(100), Height: kit.Pct(100), Padding: kit.EdgesAll(kit.Px(20))}
	kw := kit.NewWindow(scope, kit.WindowOptions{Title: "Downex fieldset", Palette: theme.Palette})
	var w *mac.Window
	onMain(t, func() {
		var err error
		w, err = app.NewWindow(kw, mac.WindowOptions{Title: "Downex fieldset", Size: kit.Size{W: 360, H: 160}, NoActivate: true})
		if err != nil {
			fatal(t, err)
		}
	})
	defer onMain(t, func() { w.Close() })
	onMain(t, func() {
		win, err := axuiWindow("Downex fieldset")
		if err != nil {
			fatal(t, err)
		}
		defer win.release()
		kids, _ := win.list("AXChildren")
		defer releaseAll(kids)
		var fs *axui
		for _, k := range kids {
			if sr, _ := k.str("AXSubrole"); k.role() == "AXGroup" && sr == "AXFieldset" {
				fs = k
			}
		}
		if fs == nil {
			fatalf(t, "no AXGroup/AXFieldset:\n%s", axuiDump(win, 3))
		}
		if d, _ := fs.str("AXDescription"); d != "Theme" {
			t.Errorf("fieldset description %q", d)
		}
		if rd, _ := fs.str("AXRoleDescription"); rd != "group" {
			t.Errorf("fieldset role description %q", rd)
		}
		radios, _ := fs.list("AXChildren")
		var roles []string
		for _, r := range radios {
			roles = append(roles, describe(r))
		}
		releaseAll(radios)
		if len(roles) != 2 || !strings.HasPrefix(roles[0], "AXRadioButton") || !strings.Contains(roles[1], "Dark") {
			t.Errorf("the fieldset's children %q", roles)
		}
	})
}
