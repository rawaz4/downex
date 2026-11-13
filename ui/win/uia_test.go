//go:build windows

package win

import (
	"fmt"
	"math"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/views"
	"downex/ui/views/shell"
	"downex/ui/win/internal/w32"
	"downex/ui/win/uia"
	"downex/ui/win/uia/uiaclient"
)

// The UI Automation gate: a window like the native preview's (the title bar of
// ui/views/shell over a grid of 10,000 downloads and a status bar) is read through the
// system's UI Automation client, IUIAutomation from UIAutomationCore, on another thread of
// this process, as Narrator and Accessibility Insights read it.

// uiaRows is the grid's data: the download list's name, size, progress and status columns.
type uiaRows struct{ n int }

func (m uiaRows) Len() int                        { return m.n }
func (m uiaRows) Key(i int) kit.ListKey           { return kit.ListKey(i + 1) }
func (m uiaRows) Index(k kit.ListKey) (int, bool) { return int(k) - 1, k >= 1 && int(k) <= m.n }

var uiaStatuses = []string{"Completed", "Paused", "Failed", "Queued #3", "2.4 MB/s", "To confirm"}

// uiaCell is the text of column col of row i.
func uiaCell(i, col int) string {
	switch col {
	case 0:
		return fmt.Sprintf("report-%05d.pdf", i)
	case 1:
		return fmt.Sprintf("%d.%d MB", 1+i%900, i%10)
	case 2:
		return fmt.Sprintf("%d%%", i%101)
	}
	return uiaStatuses[i%len(uiaStatuses)]
}

// uiaRowName is a row's accessible name: the download's name and status (DownloadRow).
func uiaRowName(i int) string { return uiaCell(i, 0) + ", " + uiaCell(i, 3) }

// RowNode, Cells and CellNode are kit.ListSemantics': the rows not laid out.
func (m uiaRows) RowNode(i int, _ kit.ListKey) kit.SemanticNode {
	return kit.SemanticNode{Role: kit.RoleRow, Name: uiaRowName(i)}
}
func (m uiaRows) Cells() int { return 4 }
func (m uiaRows) CellNode(i int, _ kit.ListKey, col int) kit.SemanticNode {
	return kit.SemanticNode{Role: kit.RoleCell, Name: uiaCell(i, col)}
}

var uiaFont = kit.Font{Family: tokens.FontUI, Size: tokens.TypeBody, Weight: 400, LineHeight: 1.4}

// uiaRow is one laid-out row: four cells, each a label.
type uiaRow struct {
	kit.Element
	index int
	cells [4]*kit.Label
}

func newUIARow() kit.ListRow {
	r := &uiaRow{}
	r.Style = kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Padding: kit.EdgesInline(kit.Px(14))}
	r.Semantics.Role = kit.RoleRow
	for c := range r.cells {
		cell := &kit.Element{Style: kit.Style{Flex: kit.Flex1, MinWidth: kit.Px(0)}}
		cell.Semantics = kit.Semantics{Role: kit.RoleCell, Cell: kit.CellInfo{Col: c}}
		r.cells[c] = kit.NewLabel("", uiaFont, kit.TextOptions{Truncate: kit.TruncateEnd})
		cell.Add(r.cells[c])
		r.Add(cell)
	}
	return r
}

// BindRow is kit.ListRow's.
func (r *uiaRow) BindRow(index int, _ kit.ListKey) {
	r.index = index
	for c, l := range r.cells {
		l.SetText(uiaCell(index, c))
	}
	r.SemanticsChanged()
}

// FillSemantics is kit.SemanticFiller's: the row's name is the download's name and status.
func (r *uiaRow) FillSemantics(n *kit.SemanticNode) { n.Name = uiaRowName(r.index) }

// uiaSelection is a single selection.
type uiaSelection struct {
	list        *kit.List
	sel, cursor kit.ListKey
}

func (s *uiaSelection) Selected(k kit.ListKey) bool { return k != 0 && k == s.sel }
func (s *uiaSelection) Cursor() (kit.ListKey, bool) { return s.cursor, s.cursor != 0 }
func (s *uiaSelection) SetCursor(k kit.ListKey)     { s.cursor = k }
func (s *uiaSelection) Click(k kit.ListKey, _ kit.SelectMods) {
	old := s.sel
	s.sel, s.cursor = k, k
	if old != 0 {
		s.list.RowChanged(old)
	}
	s.list.RowChanged(k)
	s.list.SelectionChanged()
}
func (s *uiaSelection) ContextClick(k kit.ListKey)        { s.Click(k, kit.SelectMods{}) }
func (s *uiaSelection) Move(kit.ListMove, kit.SelectMods) {}
func (s *uiaSelection) ToggleCursor()                     {}
func (s *uiaSelection) AppendSelected(dst []kit.ListKey) []kit.ListKey {
	if s.sel != 0 {
		dst = append(dst, s.sel)
	}
	return dst
}

// uiaPage is the window's content and what the test reads of it.
type uiaPage struct {
	win      *kit.Window
	tb       *shell.TitleBar
	list     *kit.List
	status   *kit.Label
	limit    *uiaSlider
	settings atomic.Int32 // presses of the Settings button
}

// uiaSlider is a range a client sets (RangeValue's SetValue takes a double, which COM's
// stub passes in XMM1: the thunk's real caller).
type uiaSlider struct {
	kit.Element
	value float64
}

func (s *uiaSlider) FillSemantics(n *kit.SemanticNode) {
	n.Range = kit.RangeValue{Valid: true, Min: 0, Max: 100, Value: s.value, Step: 1}
	n.Actions |= kit.ActionSetRange
}

func (s *uiaSlider) PerformAction(r kit.ActionRequest) error {
	if r.Action != kit.ActionSetRange {
		return kit.ErrUnsupported
	}
	s.value = r.Number
	s.SemanticsChanged()
	return nil
}

func newUIAPage() *uiaPage {
	p := &uiaPage{}
	system := tokens.System
	theme := &views.Theme{Fixed: &system}
	p.tb = shell.NewTitleBar(theme, true)
	p.tb.OnSettings = func() { p.settings.Add(1) }
	rows := uiaRows{n: 10000}
	sel := &uiaSelection{}
	p.list = kit.NewList(rows, newUIARow, func(kit.Scale) kit.DIP { return 44 })
	p.list.Selection, p.list.RowSemantics = sel, rows
	sel.list = p.list
	p.list.Semantics = kit.Semantics{Role: kit.RoleGrid, Name: "Downloads", States: kit.NodeMultiSelectable}
	p.list.Style = kit.Style{Flex: kit.Flex1, MinHeight: kit.Px(0)}
	head := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Height: kit.Px(32), Padding: kit.EdgesInline(kit.Px(14))}}
	head.Semantics.Role = kit.RoleRow
	for c, name := range []string{"Name", "Size", "Progress", "Status"} {
		l := kit.NewLabel(name, uiaFont, kit.TextOptions{})
		l.Semantics.Role = kit.RoleColumnHeader
		l.Semantics.Cell = kit.CellInfo{Col: c}
		l.Style = kit.Style{Flex: kit.Flex1}
		head.Add(l)
	}
	p.list.SetHeader(head)
	// The status bar: a live region (role=status) holding its text, and a slider.
	bar := &kit.Element{Style: kit.Style{Flex: kit.Flex1, Padding: kit.EdgesInline(kit.Px(18))}}
	bar.Semantics.Role = kit.RoleStatus
	p.status = kit.NewLabel("10,000 downloads · 0 downloading", uiaFont, kit.TextOptions{})
	bar.Add(p.status)
	p.limit = &uiaSlider{value: 40}
	p.limit.Semantics = kit.Semantics{Role: kit.RoleSlider, Name: "Limit"}
	p.limit.Style = kit.Style{Width: kit.Px(120), Height: kit.Px(20)}
	foot := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Height: kit.Px(28)}}
	foot.Add(bar, p.limit)
	page := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn}}
	page.Add(p.tb, p.list, foot)
	p.win = kit.NewWindow(page, kit.WindowOptions{Title: "Downex", Palette: theme.Palette, FrameColors: theme.MainFrame})
	return p
}

// openUIA opens the page's window on the test's locked thread.
func openUIA(t *testing.T) (*App, *Window, *uiaPage) {
	t.Helper()
	runtimeLock(t)
	a, err := NewApp(AppOptions{ClassName: "DownexUIATest"})
	if err != nil {
		t.Fatal(err)
	}
	p := newUIAPage()
	t.Cleanup(func() {
		uiaSweep()
		shell.ReleaseIcons(a.Resources())
		a.Close()
	})
	mi, _ := w32.GetMonitorInfo(w32.MonitorFromPoint(w32.Point{}, w32.MONITOR_DEFAULTTOPRIMARY))
	w, err := a.NewWindow(p.win, WindowOptions{Title: "Downex", Size: kit.Size{W: 900, H: 600}, CustomFrame: true,
		NoActivate: true, Placed: true, At: w32.Point{X: mi.Work.Left + 40, Y: mi.Work.Top + 40}})
	if err != nil {
		t.Fatal(err)
	}
	w.overWindow = func() bool { return false } // the real pointer is the owner's, elsewhere
	pump(t, a, w, "the first frame", 5*time.Second, func() bool { return w.painted })
	return a, w, p
}

// clientRun runs f on a thread of its own in the multithreaded apartment, as a UI
// Automation client, while the UI thread pumps its messages; it fails the test with what f
// returns.
func clientRun(t *testing.T, a *App, w *Window, timeout time.Duration, f func(c *uiaclient.Automation5) error) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		// Never unlocked: the thread joins the multithreaded apartment (uiaclient.Init) and
		// ends with the goroutine. Unlocked, it went back to Go's pool still in the MTA, and a
		// later test's UI thread that landed on it stayed there too (NewApp keeps a thread's
		// apartment): UI Automation then called its providers from RPC threads, which refuse
		// such calls, and the client saw only the window's frame (TestUIAutomationTree
		// failing after TestHostedFieldsAppearOnce or another client test in a full run).
		runtime.LockOSThread()
		if err := uiaclient.Init(); err != nil {
			done <- err
			return
		}
		c, err := uiaclient.New()
		if err != nil {
			done <- err
			return
		}
		err = f(c)
		c.Release()
		done <- err
	}()
	var err error
	finished := false
	if !pumpFor(t, a, w, timeout, func() bool {
		select {
		case err = <-done:
			finished = true
		default:
		}
		return finished
	}) {
		t.Fatalf("the UI Automation client did not finish in %v", timeout)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// onUI runs f on the UI thread (through the window's Post) and waits for it, from the
// client's thread.
func onUI(w *Window, f func()) error {
	ran := make(chan struct{})
	if err := w.Post(func() { f(); close(ran) }); err != nil {
		return err
	}
	select {
	case <-ran:
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("the UI thread did not run a posted function")
	}
}

// setTopmost puts h above every window that is not topmost itself (HWND_TOPMOST, -1), or
// back among them (HWND_NOTOPMOST, -2), without moving, sizing or activating it (winuser.h
// SetWindowPos with SWP_NOMOVE | SWP_NOSIZE | SWP_NOACTIVATE).
func setTopmost(h w32.HWND, on bool) {
	after := ^uintptr(1) // HWND_NOTOPMOST
	if on {
		after = ^uintptr(0) // HWND_TOPMOST
	}
	_, _, _ = user32.NewProc("SetWindowPos").Call(uintptr(h), after, 0, 0, 0, 0, w32.SWP_NOMOVE|w32.SWP_NOSIZE|w32.SWP_NOACTIVATE)
}

// onKit runs f as a call into the kit window (kit.Window.Post: its events are flushed to the
// host when f returns) and waits for it, from the client's thread.
func onKit(kw *kit.Window, f func()) error {
	ran := make(chan struct{})
	if err := kw.Post(func() { f(); close(ran) }); err != nil {
		return err
	}
	select {
	case <-ran:
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("the UI thread did not run a posted function")
	}
}

// find is the first element under e (scope) whose property id is the string s.
func find(c *uiaclient.Automation5, e *uiaclient.Element, scope int32, id int32, s string) (*uiaclient.Element, error) {
	cond, err := c.StringCondition(id, s)
	if err != nil {
		return nil, err
	}
	defer cond.Release()
	found, err := e.FindFirst(scope, cond)
	if err == nil && found == nil {
		err = fmt.Errorf("no element with property %d = %q", id, s)
	}
	return found, err
}

// findType is the first element under e whose control type is ct.
func findType(c *uiaclient.Automation5, e *uiaclient.Element, scope int32, ct int32) (*uiaclient.Element, error) {
	cond, err := c.IntCondition(uia.UIA_ControlTypePropertyId, ct)
	if err != nil {
		return nil, err
	}
	defer cond.Release()
	found, err := e.FindFirst(scope, cond)
	if err == nil && found == nil {
		err = fmt.Errorf("no element of control type %d", ct)
	}
	return found, err
}

// countAll is the number of elements under e (scope) of control type ct, 0 for any.
func countAll(c *uiaclient.Automation5, e *uiaclient.Element, scope int32, ct int32) (int, error) {
	var cond *uiaclient.Condition
	var err error
	if ct == 0 {
		cond, err = c.CreateTrueCondition()
	} else {
		cond, err = c.IntCondition(uia.UIA_ControlTypePropertyId, ct)
	}
	if err != nil {
		return 0, err
	}
	defer cond.Release()
	all, err := e.FindAll(scope, cond)
	if err != nil {
		return 0, err
	}
	defer all.Release()
	return all.Len()
}

// walk visits every element under e with the walker and returns how many it saw; it
// releases each one.
func walk(wk *uiaclient.TreeWalker, e *uiaclient.Element, depth int) (int, error) {
	if depth > 32 {
		return 0, fmt.Errorf("the tree is more than 32 levels deep")
	}
	n := 0
	child, err := wk.GetFirstChildElement(e)
	for err == nil && child != nil {
		n++
		var m int
		if m, err = walk(wk, child, depth+1); err != nil {
			child.Release()
			return n, err
		}
		n += m
		next, nerr := wk.GetNextSiblingElement(child)
		child.Release()
		child, err = next, nerr
	}
	return n, err
}

// TestUIAutomationTree is the step-9 gate: the title-bar buttons by name and role, the list
// as a grid with its row and column counts, a row's name plus status, a virtual row's cell,
// Invoke on a button, an announcement, focus and structure events, the native search field
// shown once, and no provider left alive after 1,000 walks of the whole tree.
func TestUIAutomationTree(t *testing.T) {
	a, w, p := openUIA(t)
	hwnd := uintptr(w.hwnd)
	settingsName := "Settings"

	// 1,000 walks of the whole tree, each from a fresh element of the window, releasing
	// everything: the providers' handle table must return to empty. They come first: once a
	// client has read the tree, the kit's changes raise real events (a selection, an
	// Invoke, a SetValue below), and UI Automation's core keeps the senders of events until
	// the window's providers are disconnected (checked at the end).
	var elements []int
	start := time.Now()
	clientRun(t, a, w, 8*time.Minute, func(c *uiaclient.Automation5) error {
		wk, err := c.RawViewWalker()
		if err != nil {
			return err
		}
		defer wk.Release()
		for i := 0; i < 1000; i++ {
			root, err := c.ElementFromHandle(hwnd)
			if err != nil {
				return err
			}
			n, err := walk(wk, root, 0)
			root.Release()
			if err != nil {
				return fmt.Errorf("walk %d: %w", i, err)
			}
			elements = append(elements, n)
		}
		return nil
	})
	took := time.Since(start)
	runtime.GC()
	settled := pumpFor(t, a, w, 10*time.Second, func() bool { return uia.Live() == 0 })
	t.Logf("1,000 walks of %d to %d elements in %v (%.0f µs an element); providers alive after: %d; calls refused off the UI thread: %d; panics: %d",
		minOf(elements), maxOf(elements), took.Round(time.Millisecond), float64(took.Microseconds())/math.Max(1, float64(sum(elements))),
		uia.Live(), uia.Refused(), uia.Panics())
	if !settled {
		t.Errorf("%d providers still alive after the client released everything: %v", uia.Live(), uiaTrees[w].Held())
	}
	if minOf(elements) < 100 || minOf(elements) != maxOf(elements) {
		t.Errorf("walks saw %d to %d elements: the tree should be the same each time, title bar and rows", minOf(elements), maxOf(elements))
	}
	if n := uia.Refused(); n != 0 {
		t.Errorf("%d provider calls arrived off the UI thread", n)
	}
	if n := uia.Panics(); n != 0 {
		t.Errorf("%d panics in provider methods: %s", n, uia.LastPanic())
	}
	if n := uiaclient.LiveHandlers(); n != 0 {
		t.Errorf("%d event handlers still alive", n)
	}

	clientRun(t, a, w, 3*time.Minute, func(c *uiaclient.Automation5) error {
		root, err := c.ElementFromHandle(hwnd)
		if err != nil {
			return err
		}
		defer root.Release()
		if name, _ := root.Name(); name != "Downex" {
			return fmt.Errorf("the window's name is %q, want Downex", name)
		}
		if ct, _ := root.ControlType(); ct != uia.UIA_WindowControlTypeId {
			return fmt.Errorf("the window's control type is %d, want Window", ct)
		}

		// The title bar and its buttons, by name and role.
		bar, err := find(c, root, uiaclient.TreeScope_Descendants, uia.UIA_AutomationIdPropertyId, "titlebar")
		if err != nil {
			return fmt.Errorf("title bar: %w", err)
		}
		defer bar.Release()
		if ct, _ := bar.ControlType(); ct != uia.UIA_TitleBarControlTypeId {
			return fmt.Errorf("the title bar's control type is %d, want TitleBar", ct)
		}
		for _, name := range []string{"Minimise", "Maximise", "Close", settingsName, "Commands"} {
			b, err := find(c, bar, uiaclient.TreeScope_Descendants, uia.UIA_NamePropertyId, name)
			if err != nil {
				return fmt.Errorf("title-bar button %q: %w", name, err)
			}
			ct, _ := b.ControlType()
			b.Release()
			if ct != uia.UIA_ButtonControlTypeId {
				return fmt.Errorf("%q has control type %d, want Button", name, ct)
			}
		}
		// The native search field shows once: the node claims its EDIT window.
		if n, err := countAll(c, root, uiaclient.TreeScope_Descendants, uia.UIA_EditControlTypeId); err != nil || n != 1 {
			return fmt.Errorf("%d edit fields under the window (%v), want the search alone", n, err)
		}

		// The list: a grid of every row (the header's included) and four columns.
		grid, err := findType(c, root, uiaclient.TreeScope_Descendants, uia.UIA_DataGridControlTypeId)
		if err != nil {
			return err
		}
		defer grid.Release()
		if name, _ := grid.Name(); name != "Downloads" {
			return fmt.Errorf("the grid's name is %q", name)
		}
		gp, err := uiaclient.Pattern[uiaclient.GridPattern](grid, uia.UIA_GridPatternId, &uiaclient.IID_IUIAutomationGridPattern)
		if err != nil || gp == nil {
			return fmt.Errorf("the grid has no Grid pattern: %v", err)
		}
		defer gp.Release()
		rows, _ := gp.RowCount()
		cols, _ := gp.ColumnCount()
		if rows != 10001 || cols != 4 {
			return fmt.Errorf("the grid is %d × %d, want 10001 × 4", rows, cols)
		}
		// A laid-out row is named by the download's name and status.
		row, err := find(c, grid, uiaclient.TreeScope_Children, uia.UIA_NamePropertyId, uiaRowName(0))
		if err != nil {
			return fmt.Errorf("row 0: %w", err)
		}
		if ct, _ := row.ControlType(); ct != uia.UIA_DataItemControlTypeId {
			return fmt.Errorf("a row's control type is %d, want DataItem", ct)
		}
		sp, err := uiaclient.Pattern[uiaclient.SelectionItemPattern](row, uia.UIA_SelectionItemPatternId, &uiaclient.IID_IUIAutomationSelectionItemPattern)
		row.Release()
		if err != nil || sp == nil {
			return fmt.Errorf("a row has no SelectionItem pattern: %v", err)
		}
		if err := sp.Select(); err != nil {
			return fmt.Errorf("Select a row: %w", err)
		}
		on, _ := sp.IsSelected()
		sp.Release()
		if !on {
			return fmt.Errorf("the row selected is not selected")
		}
		// A cell of a row far out of view comes from the grid's ListSemantics.
		cell, err := gp.GetItem(5001, 1) // the header is row 0
		if err != nil || cell == nil {
			return fmt.Errorf("GetItem(5001, 1): %v", err)
		}
		name, _ := cell.Name()
		cell.Release()
		if want := uiaCell(5000, 1); name != want {
			return fmt.Errorf("GetItem(5001, 1) is %q, want %q", name, want)
		}

		// Invoke on a button.
		settings, err := find(c, bar, uiaclient.TreeScope_Descendants, uia.UIA_NamePropertyId, settingsName)
		if err != nil {
			return err
		}
		ip, err := uiaclient.Pattern[uiaclient.InvokePattern](settings, uia.UIA_InvokePatternId, &uiaclient.IID_IUIAutomationInvokePattern)
		settings.Release()
		if err != nil || ip == nil {
			return fmt.Errorf("Settings has no Invoke pattern: %v", err)
		}
		err = ip.Invoke()
		ip.Release()
		if err != nil {
			return err
		}
		for deadline := time.Now().Add(5 * time.Second); p.settings.Load() == 0; {
			if time.Now().After(deadline) {
				return fmt.Errorf("Invoke did not press Settings")
			}
			time.Sleep(5 * time.Millisecond)
		}

		// The doubles of ElementProviderFromPoint and RangeValue's SetValue, from the
		// system's own callers (UI Automation's core and COM's stub pass them in XMM
		// registers only: thunk_amd64.s).
		settings, err = find(c, bar, uiaclient.TreeScope_Descendants, uia.UIA_NamePropertyId, settingsName)
		if err != nil {
			return err
		}
		r, err := settings.Bounds()
		settings.Release()
		if err != nil || r.Right <= r.Left {
			return fmt.Errorf("Settings' rectangle %+v: %v", r, err)
		}
		// ElementFromPoint reads the screen: a point of the button that no other window
		// (another test's, the owner's) covers. The test's window goes above the others'
		// for it, without taking the activation (and back afterwards).
		if err := onUI(w, func() { setTopmost(w.hwnd, true) }); err != nil {
			return err
		}
		defer func() { _ = onUI(w, func() { setTopmost(w.hwnd, false) }) }()
		var pt *w32.Point
		for _, c := range []w32.Point{{X: (r.Left + r.Right) / 2, Y: (r.Top + r.Bottom) / 2}, {X: r.Left + 3, Y: r.Top + 3},
			{X: r.Right - 4, Y: r.Top + 3}, {X: r.Left + 3, Y: r.Bottom - 4}, {X: r.Right - 4, Y: r.Bottom - 4}} {
			if w32.GetAncestor(w32.WindowFromPoint(c), w32.GA_ROOT) == w.hwnd {
				pt = &c
				break
			}
		}
		if pt == nil {
			fromPointUnverified.Store(true)
		} else {
			at, err := c.ElementFromPoint(pt.X, pt.Y)
			if err != nil || at == nil {
				return fmt.Errorf("ElementFromPoint: %v", err)
			}
			name, _ = at.Name()
			at.Release()
			if name != settingsName {
				return fmt.Errorf("ElementFromPoint at %+v in Settings found %q", *pt, name)
			}
		}
		limit, err := find(c, root, uiaclient.TreeScope_Descendants, uia.UIA_NamePropertyId, "Limit")
		if err != nil {
			return err
		}
		rp, err := uiaclient.Pattern[uiaclient.RangeValuePattern](limit, uia.UIA_RangeValuePatternId, &uiaclient.IID_IUIAutomationRangeValuePattern)
		limit.Release()
		if err != nil || rp == nil {
			return fmt.Errorf("the slider has no RangeValue pattern: %v", err)
		}
		defer rp.Release()
		if err := rp.SetValue(75.5); err != nil {
			return fmt.Errorf("RangeValue.SetValue(75.5): %w", err)
		}
		if v, err := rp.Value(); err != nil || v != 75.5 {
			return fmt.Errorf("the slider's value is %v (%v) after SetValue(75.5)", v, err)
		}
		if err := rp.SetValue(150); err == nil {
			return fmt.Errorf("SetValue(150) beyond the maximum was taken")
		}
		return nil
	})
	if fromPointUnverified.Load() {
		// Not a skip: the rest of the gate still runs. A gate run (DOWNEX_WIN_REQUIRE_ALL)
		// fails instead.
		const why = "every point of the Settings button was under another window: ElementFromPoint (the fragment root's doubles from UI Automation's core) was not exercised"
		if os.Getenv("DOWNEX_WIN_REQUIRE_ALL") != "" {
			t.Error(why)
		} else {
			t.Log("UNVERIFIED: " + why)
		}
	}
	if p.limit.value != 75.5 {
		t.Errorf("the slider's widget has %v, want 75.5", p.limit.value)
	}
	if n := p.settings.Load(); n != 1 {
		t.Errorf("Settings pressed %d times, want 1", n)
	}

	// Events: an announcement, the focus, a structure change and a live region's change.
	clientRun(t, a, w, time.Minute, func(c *uiaclient.Automation5) error {
		root, err := c.ElementFromHandle(hwnd)
		if err != nil {
			return err
		}
		defer root.Release()
		notes, err := uiaclient.NewNotificationHandler(8)
		if err != nil {
			return err
		}
		defer notes.Release()
		focus, err := uiaclient.NewFocusHandler(64)
		if err != nil {
			return err
		}
		defer focus.Release()
		structure, err := uiaclient.NewStructureHandler(8)
		if err != nil {
			return err
		}
		defer structure.Release()
		live, err := uiaclient.NewAutomationHandler(8)
		if err != nil {
			return err
		}
		defer live.Release()
		// The handlers go on every return, an early failure's too (a handler left alive fails
		// the next test's count); removing them again after the last wait is harmless.
		defer func() { _ = c.RemoveAllEventHandlers() }()
		if err := c.AddNotificationEventHandler(root, uiaclient.TreeScope_Subtree, notes); err != nil {
			return err
		}
		if err := c.AddFocusChangedEventHandler(focus); err != nil {
			return err
		}
		if err := c.AddStructureChangedEventHandler(root, uiaclient.TreeScope_Subtree, structure); err != nil {
			return err
		}
		if err := c.AddAutomationEventHandler(uia.UIA_LiveRegionChangedEventId, root, uiaclient.TreeScope_Subtree, live); err != nil {
			return err
		}
		const said = "report-00007.pdf, 45% downloaded"
		const status = "10,000 downloads · 1 downloading"
		var before, after int
		if err := onUI(w, func() { before = uiaTrees[w].Stats().Events }); err != nil {
			return err
		}
		// The kit's own events, as a view makes them in a call into the window: they reach
		// UI Automation through the window's NotifySemantics.
		if err := onKit(p.win, func() {
			p.win.Announce(said, kit.LivePolite, p.list)
			if btn := findButton(p.tb, settingsName); btn == nil || !p.win.Focus(btn) {
				panic("the Settings button takes no focus")
			}
			p.list.ScrollTo(44 * 200) // other rows are realised: the grid's children change
			// The status bar's text changes: the kit reports the text's node, inside the
			// live region.
			p.status.SetText(status)
		}); err != nil {
			return err
		}
		if err := waitEvent(live, "live region changed", func(e uiaclient.Event) bool {
			return e.EventID == uia.UIA_LiveRegionChangedEventId && e.Name == status && e.ControlType == uia.UIA_GroupControlTypeId
		}); err != nil {
			return err
		}
		if err := waitEvent(notes, "announcement", func(e uiaclient.Event) bool {
			return e.Text == said && e.Activity == "Downex.Announcement" && e.Processing == uia.NotificationProcessing_All &&
				e.Kind == uia.NotificationKind_Other && e.Name == "Downloads"
		}); err != nil {
			return err
		}
		if err := waitEvent(focus, "focus changed", func(e uiaclient.Event) bool {
			return e.Name == settingsName && e.ControlType == uia.UIA_ButtonControlTypeId
		}); err != nil {
			return err
		}
		if err := waitEvent(structure, "structure changed", func(e uiaclient.Event) bool {
			return e.Name == "Downloads" && e.Change == uia.StructureChangeType_ChildrenInvalidated
		}); err != nil {
			return err
		}
		if err := onUI(w, func() { after = uiaTrees[w].Stats().Events }); err != nil {
			return err
		}
		if got := after - before; got < 4 {
			return fmt.Errorf("the kit's changes raised %d UI Automation events, want 4 or more", got)
		}
		return c.RemoveAllEventHandlers()
	})
	if st := uiaTrees[w].Stats(); st.Calls == 0 || st.Events < 4 {
		t.Errorf("tree stats %+v", st)
	} else {
		t.Logf("tree stats %+v", st)
	}
	// UI Automation keeps the events' senders for a while; it holds nothing of the
	// window once the window's providers are disconnected.
	t.Logf("providers alive after the events: %d (%v)", uia.Live(), uiaTrees[w].Held())

	// Invoke on the title bar's Close closes the window, as a click does.
	clientRun(t, a, w, time.Minute, func(c *uiaclient.Automation5) error {
		root, err := c.ElementFromHandle(hwnd)
		if err != nil {
			return err
		}
		defer root.Release()
		b, err := find(c, root, uiaclient.TreeScope_Descendants, uia.UIA_NamePropertyId, "Close")
		if err != nil {
			return err
		}
		ip, err := uiaclient.Pattern[uiaclient.InvokePattern](b, uia.UIA_InvokePatternId, &uiaclient.IID_IUIAutomationInvokePattern)
		b.Release()
		if err != nil || ip == nil {
			return fmt.Errorf("Close has no Invoke pattern: %v", err)
		}
		defer ip.Release()
		return ip.Invoke()
	})
	pump(t, a, w, "the window to close", 5*time.Second, func() bool { return w.closed })
	// The window's WM_DESTROY disconnected every provider (uiaDestroyed).
	if _, ok := uiaTrees[w]; ok || !w.uiaDone {
		t.Error("the destroyed window's providers are still connected")
	}
	if !pumpFor(t, a, w, 10*time.Second, func() bool { return uia.Live() == 0 }) {
		t.Errorf("%d providers alive after the window's providers were disconnected", uia.Live())
	}
}

// fromPointUnverified: the ElementFromPoint check found its window covered.
var fromPointUnverified atomic.Bool

// findButton is the title bar's widget whose accessible name is name.
func findButton(root kit.Widget, name string) kit.Widget {
	var found kit.Widget
	var visit func(e *kit.Element)
	visit = func(e *kit.Element) {
		for _, c := range e.Children() {
			if found != nil {
				return
			}
			if c.Semantics.Name == name {
				found = c.Widget()
				return
			}
			visit(c)
		}
	}
	visit(root.Base())
	return found
}

// waitEvent waits for an event that ok accepts.
func waitEvent(h *uiaclient.Handler, what string, ok func(uiaclient.Event) bool) error {
	deadline := time.After(10 * time.Second)
	var seen []string
	for {
		select {
		case e := <-h.C:
			if ok(e) {
				return nil
			}
			seen = append(seen, fmt.Sprintf("%+v", e))
		case <-deadline:
			return fmt.Errorf("no %s event; saw %s", what, strings.Join(seen, "; "))
		}
	}
}

func minOf(v []int) int {
	m := math.MaxInt
	for _, x := range v {
		m = min(m, x)
	}
	return m
}

func maxOf(v []int) int {
	m := 0
	for _, x := range v {
		m = max(m, x)
	}
	return m
}

func sum(v []int) int {
	s := 0
	for _, x := range v {
		s += x
	}
	return s
}
