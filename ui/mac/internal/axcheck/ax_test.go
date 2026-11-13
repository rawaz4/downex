//go:build darwin && cgo

package axcheck

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/mac"
	"downex/ui/views"
	"downex/ui/views/shell"
)

func init() { runtime.LockOSThread() }

var app *mac.App

// TestMain keeps the main goroutine on the main thread running AppKit's event loop, and
// runs the tests on another goroutine; a test does its AppKit work with onMain.
func TestMain(m *testing.M) {
	var err error
	app, err = mac.NewApp(mac.AppOptions{Regular: false})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// A small window keeps the event loop running between tests.
	keep, err := app.NewWindow(kit.NewWindow(&kit.Element{}, kit.WindowOptions{Title: "keep"}),
		mac.WindowOptions{Title: "keep", Size: kit.Size{W: 120, H: 60}, NoActivate: true})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := 0
	go func() {
		code = m.Run()
		_ = keep.Post(keep.Close)
	}()
	_ = app.Run()
	os.Exit(code)
}

// onMain runs f on the main thread and waits for it.
func onMain(t *testing.T, f func()) {
	t.Helper()
	done := make(chan any, 1)
	app.Post(func() {
		defer func() { done <- recover() }()
		f()
	})
	select {
	case v := <-done:
		if v == errFatal {
			t.FailNow()
		}
		if v != nil {
			t.Fatalf("panic on the main thread: %v", v)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the main thread did not answer")
	}
}

// errFatal ends a function onMain runs: t.Fatal there would end the main goroutine, which
// runs AppKit, so fatal reports the error and unwinds to onMain, which fails the test.
var errFatal = &struct{ string }{"fatal"}

func fatal(t *testing.T, args ...any) {
	t.Helper()
	t.Error(args...)
	panic(errFatal)
}

func fatalf(t *testing.T, format string, args ...any) {
	t.Helper()
	t.Errorf(format, args...)
	panic(errFatal)
}

// eventually retries cond on the main thread until it holds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ok := false
		onMain(t, func() { ok = cond() })
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// ---- the page: the Windows gate's (ui/win/uia_test.go) ---------------------------------

type rows struct{ n int }

func (m rows) Len() int                        { return m.n }
func (m rows) Key(i int) kit.ListKey           { return kit.ListKey(i + 1) }
func (m rows) Index(k kit.ListKey) (int, bool) { return int(k) - 1, k >= 1 && int(k) <= m.n }

var statuses = []string{"Completed", "Paused", "Failed", "Queued #3", "2.4 MB/s", "To confirm"}

func cellText(i, col int) string {
	switch col {
	case 0:
		return fmt.Sprintf("report-%05d.pdf", i)
	case 1:
		return fmt.Sprintf("%d.%d MB", 1+i%900, i%10)
	case 2:
		return fmt.Sprintf("%d%%", i%101)
	}
	return statuses[i%len(statuses)]
}

func rowName(i int) string { return cellText(i, 0) + ", " + cellText(i, 3) }

func (m rows) RowNode(i int, _ kit.ListKey) kit.SemanticNode {
	return kit.SemanticNode{Role: kit.RoleRow, Name: rowName(i)}
}
func (m rows) Cells() int { return 4 }
func (m rows) CellNode(i int, _ kit.ListKey, col int) kit.SemanticNode {
	return kit.SemanticNode{Role: kit.RoleCell, Name: cellText(i, col)}
}

var font = kit.Font{Family: tokens.FontUI, Size: tokens.TypeBody, Weight: 400, LineHeight: 1.4}

type row struct {
	kit.Element
	index int
	cells [4]*kit.Label
}

func newRow() kit.ListRow {
	r := &row{}
	r.Style = kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Padding: kit.EdgesInline(kit.Px(14))}
	r.Semantics.Role = kit.RoleRow
	for c := range r.cells {
		cell := &kit.Element{Style: kit.Style{Flex: kit.Flex1, MinWidth: kit.Px(0)}}
		cell.Semantics = kit.Semantics{Role: kit.RoleCell, Cell: kit.CellInfo{Col: c}}
		r.cells[c] = kit.NewLabel("", font, kit.TextOptions{Truncate: kit.TruncateEnd})
		cell.Add(r.cells[c])
		r.Add(cell)
	}
	return r
}

func (r *row) BindRow(index int, _ kit.ListKey) {
	r.index = index
	for c, l := range r.cells {
		l.SetText(cellText(index, c))
	}
	r.SemanticsChanged()
}

func (r *row) FillSemantics(n *kit.SemanticNode) { n.Name = rowName(r.index) }

type selection struct {
	list        *kit.List
	sel, cursor kit.ListKey
}

func (s *selection) Selected(k kit.ListKey) bool { return k != 0 && k == s.sel }
func (s *selection) Cursor() (kit.ListKey, bool) { return s.cursor, s.cursor != 0 }
func (s *selection) SetCursor(k kit.ListKey)     { s.cursor = k }
func (s *selection) Click(k kit.ListKey, _ kit.SelectMods) {
	old := s.sel
	s.sel, s.cursor = k, k
	if old != 0 {
		s.list.RowChanged(old)
	}
	s.list.RowChanged(k)
	s.list.SelectionChanged()
}
func (s *selection) ContextClick(k kit.ListKey)        { s.Click(k, kit.SelectMods{}) }
func (s *selection) Move(kit.ListMove, kit.SelectMods) {}
func (s *selection) ToggleCursor()                     {}
func (s *selection) AppendSelected(dst []kit.ListKey) []kit.ListKey {
	if s.sel != 0 {
		dst = append(dst, s.sel)
	}
	return dst
}

type slider struct {
	kit.Element
	value float64
}

func (s *slider) FillSemantics(n *kit.SemanticNode) {
	n.Range = kit.RangeValue{Valid: true, Min: 0, Max: 100, Value: s.value, Step: 1}
	n.Value = fmt.Sprintf("%g KB/s", s.value)
	n.Actions |= kit.ActionSetRange
}

func (s *slider) PerformAction(r kit.ActionRequest) error {
	if r.Action != kit.ActionSetRange {
		return kit.ErrUnsupported
	}
	s.value = r.Number
	s.SemanticsChanged()
	return nil
}

type page struct {
	win      *kit.Window
	tb       *shell.TitleBar
	list     *kit.List
	status   *kit.Label
	limit    *slider
	name     *kit.Element // a text field's box, where a native field is placed
	settings atomic.Int32
}

func newPage() *page {
	p := &page{}
	system := tokens.System
	theme := &views.Theme{Fixed: &system}
	p.tb = shell.NewTitleBar(theme, false)
	p.tb.OnSettings = func() { p.settings.Add(1) }
	rs := rows{n: 10000}
	sel := &selection{}
	p.list = kit.NewList(rs, newRow, func(kit.Scale) kit.DIP { return 44 })
	p.list.Selection, p.list.RowSemantics = sel, rs
	sel.list = p.list
	p.list.Semantics = kit.Semantics{Role: kit.RoleGrid, Name: "Downloads", States: kit.NodeMultiSelectable}
	p.list.Style = kit.Style{Flex: kit.Flex1, MinHeight: kit.Px(0)}
	head := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Height: kit.Px(32), Padding: kit.EdgesInline(kit.Px(14))}}
	head.Semantics.Role = kit.RoleRow
	for c, name := range []string{"Name", "Size", "Progress", "Status"} {
		l := kit.NewLabel(name, font, kit.TextOptions{})
		l.Semantics.Role = kit.RoleColumnHeader
		l.Semantics.Cell = kit.CellInfo{Col: c}
		l.Style = kit.Style{Flex: kit.Flex1}
		head.Add(l)
	}
	p.list.SetHeader(head)
	bar := &kit.Element{Style: kit.Style{Flex: kit.Flex1, Padding: kit.EdgesInline(kit.Px(18))}}
	bar.Semantics.Role = kit.RoleStatus
	p.status = kit.NewLabel("10,000 downloads · 0 downloading", font, kit.TextOptions{})
	bar.Add(p.status)
	p.limit = &slider{value: 40}
	p.limit.Semantics = kit.Semantics{Role: kit.RoleSlider, Name: "Limit"}
	p.limit.Style = kit.Style{Width: kit.Px(120), Height: kit.Px(20)}
	p.name = &kit.Element{Style: kit.Style{Width: kit.Px(160), Height: kit.Px(24)}}
	p.name.Semantics = kit.Semantics{Role: kit.RoleTextField, Name: "Rename", Description: "The file's new name",
		AutomationID: "rename"}
	foot := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Height: kit.Px(28)}}
	foot.Add(bar, p.name, p.limit)
	pg := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn}}
	pg.Add(p.tb, p.list, foot)
	p.win = kit.NewWindow(pg, kit.WindowOptions{Title: "Downex", Palette: theme.Palette, FrameColors: theme.MainFrame})
	return p
}

func open(t *testing.T) (*mac.Window, *page) {
	t.Helper()
	var w *mac.Window
	p := newPage()
	onMain(t, func() {
		var err error
		w, err = app.NewWindow(p.win, mac.WindowOptions{Title: "Downex", Size: kit.Size{W: 900, H: 600}, NoActivate: true})
		if err != nil {
			t.Error(err)
		}
	})
	if w == nil {
		t.FailNow()
	}
	t.Cleanup(func() {
		onMain(t, func() { w.Close() })
	})
	return w, p
}

// find is the first element under e (depth-first, at most depth levels) that ok accepts.
func find(e *mac.AXElement, depth int, ok func(*mac.AXElement) bool) *mac.AXElement {
	if ok(e) {
		return e
	}
	if depth == 0 {
		return nil
	}
	for _, c := range e.Children() {
		if f := find(c, depth-1, ok); f != nil {
			return f
		}
	}
	return nil
}

func role(r, sub string) func(*mac.AXElement) bool {
	return func(e *mac.AXElement) bool {
		return e.String("AXRole") == r && (sub == "" || e.String("AXSubrole") == sub)
	}
}

func named(r, label string) func(*mac.AXElement) bool {
	return func(e *mac.AXElement) bool { return e.String("AXRole") == r && e.String("AXDescription") == label }
}

func center(r kit.Rect) kit.Point { return r.Center() }

// isField reports a native text field's element (an NSTextField's cell).
func isField(e *mac.AXElement) bool { return strings.HasPrefix(e.Class(), "NSTextField") }

func TestWindowTree(t *testing.T) {
	w, p := open(t)
	onMain(t, func() {
		if w.AXElements() != 0 {
			t.Error("elements before any client asked")
		}
		win := w.AXWindow()
		if r := win.String("AXRole"); r != "AXWindow" {
			fatalf(t, "window role %q", r)
		}
		if win.String("AXTitle") != "Downex" {
			t.Errorf("window title %q", win.String("AXTitle"))
		}
		kids := win.Children()
		dump := mac.AXDump(win, 3)
		t.Logf("tree:\n%s", dump)
		var ours int
		for _, k := range kids {
			if k.Class() == "DXAXElement" || isField(k) {
				ours++
			}
		}
		if ours != 4 { // title bar, grid, status, the rename field's node... see below
			t.Logf("window children %d of ours", ours)
		}
		tb := find(win, 2, role("AXGroup", "AXLandmarkBanner"))
		if tb == nil {
			fatal(t, "no title bar (banner)")
		}
		if d := tb.String("AXRoleDescription"); d != "banner" {
			t.Errorf("title bar role description %q", d)
		}
		gear := find(tb, 4, named("AXButton", "Settings"))
		if gear == nil {
			fatalf(t, "no Settings button:\n%s", mac.AXDump(tb, 4))
		}
		// Identity: the same object on every read.
		again := find(win, 2, role("AXGroup", "AXLandmarkBanner"))
		if !again.Same(tb) {
			t.Error("the title bar's element changed between reads")
		}
		if !gear.Element("AXParent").Same(find(tb, 4, func(e *mac.AXElement) bool {
			for _, c := range e.Children() {
				if c.Same(gear) {
					return true
				}
			}
			return false
		})) {
			t.Error("the button's parent")
		}
		if !tb.Element("AXParent").Same(win) {
			t.Error("the title bar's parent is not the window")
		}
		if !gear.Element("AXWindow").Same(win) || !gear.Element("AXTopLevelUIElement").Same(win) {
			t.Error("AXWindow/AXTopLevelUIElement")
		}
		// Frames: in the window, and the hit test finds the button at its centre.
		wf, _ := win.Attribute("AXFrame").(kit.Rect)
		gf, _ := gear.Attribute("AXFrame").(kit.Rect)
		if gf.Empty() || !wf.Contains(gf.Min) || !wf.Contains(gf.Max) {
			t.Errorf("button frame %v outside the window %v", gf, wf)
		}
		// AXFrame is y-up; the hit test takes the same coordinates.
		if h := win.HitTest(center(gf)); h == nil || !h.Same(gear) {
			got := "nil"
			if h != nil {
				got = h.String("AXRole") + " " + h.String("AXDescription")
			}
			t.Errorf("hit test at the button: %s", got)
		}
		if !strings.Contains(strings.Join(gear.ActionNames(), ","), "AXPress") {
			t.Errorf("button actions %v", gear.ActionNames())
		}
		gear.Perform("AXPress")
	})
	eventually(t, "the press", func() bool { return p.settings.Load() == 1 })

	onMain(t, func() {
		win := w.AXWindow()
		grid := find(win, 3, role("AXTable", ""))
		if grid == nil {
			fatal(t, "no table")
		}
		if grid.String("AXDescription") != "Downloads" {
			t.Errorf("grid name %q", grid.String("AXDescription"))
		}
		if n, _ := grid.Number("AXRowCount"); n != 10001 {
			t.Errorf("row count %v", n)
		}
		if n, _ := grid.Number("AXColumnCount"); n != 4 {
			t.Errorf("column count %v", n)
		}
		rowsNow := grid.Elements("AXRows")
		if len(rowsNow) < 5 || rowsNow[1].String("AXRole") != "AXRow" {
			fatalf(t, "rows %d", len(rowsNow))
		}
		if !strings.HasPrefix(rowsNow[1].String("AXDescription"), "report-") {
			t.Errorf("row name %q", rowsNow[1].String("AXDescription"))
		}
		if len(grid.Elements("AXVisibleRows")) == 0 {
			t.Error("no visible rows")
		}
		if h := grid.Elements("AXColumnHeaderUIElements"); len(h) != 4 {
			t.Errorf("headers %d", len(h))
		}
		// A row far below, not laid out, through the cell API: made on demand, stable.
		cell, _ := grid.Attribute("AXCellForColumnAndRow", 0, 5000).(*mac.AXElement)
		if cell == nil {
			fatalf(t, "no cell; parameterized %v", grid.ParameterizedAttributeNames())
		}
		if cell.String("AXRole") != "AXCell" || !strings.HasPrefix(cell.String("AXDescription"), "report-0") {
			t.Errorf("cell %s %q", cell.String("AXRole"), cell.String("AXDescription"))
		}
		again, _ := grid.Attribute("AXCellForColumnAndRow", 0, 5000).(*mac.AXElement)
		if !again.Same(cell) {
			t.Error("cell identity")
		}
		if r, ok := cell.Attribute("AXRowIndexRange").(mac.AXRange); !ok || r.Length != 1 {
			t.Errorf("cell row range %v", cell.Attribute("AXRowIndexRange"))
		}
		// Select a row through the table.
		grid.Set("AXSelectedRows", []*mac.AXElement{rowsNow[2]})
	})
	eventually(t, "the selection", func() bool {
		grid := find(w.AXWindow(), 3, role("AXTable", ""))
		sel := grid.Elements("AXSelectedRows")
		return len(sel) == 1
	})

	// The slider: value, range, increment.
	onMain(t, func() {
		sl := find(w.AXWindow(), 3, role("AXSlider", ""))
		if sl == nil {
			fatal(t, "no slider")
		}
		if v, _ := sl.Number("AXValue"); v != 40 {
			t.Errorf("slider value %v", v)
		}
		if mx, _ := sl.Number("AXMaxValue"); mx != 100 {
			t.Errorf("slider max %v", mx)
		}
		if sl.String("AXValueDescription") != "40 KB/s" {
			t.Errorf("value description %q", sl.String("AXValueDescription"))
		}
		if !sl.Settable("AXValue") {
			t.Error("slider value not settable")
		}
		sl.Perform("AXIncrement")
		if v, _ := sl.Number("AXValue"); v != 41 {
			t.Errorf("after increment %v", v)
		}
		sl.Set("AXValue", 75.0)
		if v, _ := sl.Number("AXValue"); v != 75 {
			t.Errorf("after set %v", v)
		}
		sl.Set("AXValue", 175.0) // refused: out of range
		if v, _ := sl.Number("AXValue"); v != 75 {
			t.Errorf("out of range set took %v", v)
		}
	})

	// The status bar: a live region; its change is announced politely, once.
	var before mac.AXCounters
	onMain(t, func() {
		before = mac.AXStats()
		p.status.SetText("10,000 downloads · 1 downloading")
	})
	eventually(t, "the announcement", func() bool {
		s := mac.AXStats()
		return s.Announcements > before.Announcements && s.LastAnnouncement == "10,000 downloads · 1 downloading"
	})
	onMain(t, func() {
		if s := mac.AXStats(); s.LastPriority != 1 {
			t.Errorf("status priority %d", s.LastPriority)
		}
		// Announce from the kit (the grid's I key).
		p.win.Announce("45% of report.pdf", kit.LiveAssertive, nil)
		if s := mac.AXStats(); s.LastAnnouncement != "45% of report.pdf" || s.LastPriority != 2 {
			t.Errorf("announce: %+v", s)
		}
	})

	// Focus: setting AXFocused on the button moves the kit's focus; the window's focused
	// element follows, and the change is posted.
	onMain(t, func() {
		before = mac.AXStats()
		win := w.AXWindow()
		gear := find(win, 5, named("AXButton", "Settings"))
		if !gear.Settable("AXFocused") {
			fatal(t, "AXFocused not settable")
		}
		gear.Set("AXFocused", true)
		if f, _ := gear.Number("AXFocused"); f != 1 {
			t.Error("button not focused")
		}
		if fe := win.Element("AXFocusedUIElement"); fe == nil || !fe.Same(gear) {
			t.Errorf("window's focused element %v", fe)
		}
		if mac.AXStats().Posted["AXFocusedUIElementChanged"] <= before.Posted["AXFocusedUIElementChanged"] {
			t.Error("no focus notification")
		}
	})

	// Walking the whole tree again and again keeps the same elements.
	onMain(t, func() {
		win := w.AXWindow()
		mac.AXDump(win, 8)
		n := w.AXElements()
		for i := 0; i < 50; i++ {
			mac.AXDump(win, 8)
		}
		if m := w.AXElements(); m != n {
			t.Errorf("elements %d after one walk, %d after 50", n, m)
		}
		t.Logf("%d elements", n)
	})

	// Off the main thread: refused, no crash.
	var gear *mac.AXElement
	onMain(t, func() { gear = find(w.AXWindow(), 5, named("AXButton", "Settings")) })
	before = mac.AXStats()
	_ = gear.Attribute("AXDescription")
	if mac.AXStats().Refused <= before.Refused {
		t.Error("an off-thread request was not refused")
	}

	// Closing destroys every element; a kept element answers nothing.
	var kept int
	onMain(t, func() {
		before = mac.AXStats()
		kept = w.AXElements()
		w.Close()
	})
	eventually(t, "the close", func() bool {
		return mac.AXStats().Posted["AXUIElementDestroyed"]-before.Posted["AXUIElementDestroyed"] >= kept
	})
	onMain(t, func() {
		if r := gear.String("AXRole"); r != "" {
			t.Errorf("a destroyed element still answers %q", r)
		}
		if gear.Children() != nil && len(gear.Children()) != 0 {
			t.Error("a destroyed element has children")
		}
	})
}

func TestNativeFieldAppearsOnce(t *testing.T) {
	w, _ := open(t)
	var field unsafeField
	onMain(t, func() {
		win := w.AXWindow()
		node := find(win, 4, func(e *mac.AXElement) bool { return e.String("AXIdentifier") == "rename" })
		if node == nil {
			fatalf(t, "no rename node:\n%s", mac.AXDump(win, 4))
		}
		if node.String("AXRole") != "AXTextField" || node.String("AXDescription") != "Rename" {
			t.Errorf("node %s %q", node.String("AXRole"), node.String("AXDescription"))
		}
		fr, _ := node.Attribute("AXFrame").(kit.Rect)
		field = unsafeField{addField(win.Object(), fr)}
	})
	onMain(t, func() {
		win := w.AXWindow()
		dump := mac.AXDump(win, 6)
		var fields, nodes int
		var walk func(e *mac.AXElement, d int)
		var parentOK bool
		walk = func(e *mac.AXElement, d int) {
			for _, c := range e.Children() {
				if isField(c) {
					fields++
					if c.String("AXDescription") != "Rename" || c.String("AXHelp") != "The file's new name" {
						t.Errorf("field label %q help %q", c.String("AXDescription"), c.String("AXHelp"))
					}
					parentOK = c.Element("AXParent").Same(e)
				} else if c.String("AXIdentifier") == "rename" {
					nodes++
				}
				if d > 0 {
					walk(c, d-1)
				}
			}
		}
		walk(win, 6)
		if fields != 1 || nodes != 0 {
			t.Errorf("field shows %d times, its node %d times:\n%s", fields, nodes, dump)
		}
		if !parentOK {
			t.Error("the field's parent is not the element that lists it")
		}
		// The hit test at the field reaches the field.
		f := find(win, 6, func(e *mac.AXElement) bool { return isField(e) })
		fr, _ := f.Attribute("AXFrame").(kit.Rect)
		if h := win.HitTest(fr.Center()); h == nil || !(h.Same(f) || strings.Contains(h.Class(), "Text")) {
			t.Errorf("hit test at the field: %v", h)
		}
		// Hidden: the node stands alone again.
		hideField(field.p, true)
		if find(win, 6, func(e *mac.AXElement) bool { return isField(e) }) != nil {
			t.Error("a hidden field shows")
		}
		if find(win, 6, func(e *mac.AXElement) bool { return e.String("AXIdentifier") == "rename" }) == nil {
			t.Error("the hidden field's node is missing")
		}
		removeField(field.p)
	})
}
