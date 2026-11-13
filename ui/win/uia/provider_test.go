//go:build windows

package uia

import (
	"math"
	"runtime"
	"slices"
	"syscall"
	"testing"
	"unsafe"

	"downex/ui/kit"
	"downex/ui/kit/kittest"
	"downex/ui/win/internal/com"
	"downex/ui/win/uia/internal/comobj"
)

// These tests call the providers' vtables directly, as UI Automation does, on a kit window
// driven by kittest: every pattern, the identity rules of QueryInterface, the errors UI
// Automation expects, the thunked doubles and the handle table. TestUIAutomationTree in
// ui/win reads a real window through the system's client.

// heapOut is an out-parameter on the heap: the methods called run Go code on this thread
// while the call that was given the pointer is in progress (see api.go).
//
//go:noinline
func heapOut[T any]() *T { return new(T) }

// unsafeAddr is p's address, for a vtable call's argument.
func unsafeAddr[T any](p *T) unsafe.Pointer { return unsafe.Pointer(p) }

// slot is the vtable slot of interface k's method name.
func slot(t *testing.T, k int, name string) int {
	t.Helper()
	for i, m := range ifaces[k].methods {
		if m.name == name {
			return 3 + i
		}
	}
	t.Fatalf("%s has no method %s", ifaces[k].name, name)
	return 0
}

// vcall calls slot of the interface pointer p.
func vcall(p uintptr, slot int, args ...uintptr) uintptr {
	fn := comobj.Word(comobj.Word(p) + uintptr(slot)*8)
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{p}, args...)...)
	return r
}

// release releases an interface pointer through its vtable.
func release(p uintptr) uint32 { return uint32(vcall(p, 2)) }

// qi asks p for interface iid.
func qi(p uintptr, iid *com.GUID) (uintptr, uintptr) {
	o := heapOut[uintptr]()
	hr := vcall(p, 0, uintptr(unsafeAddr(iid)), uintptr(unsafeAddr(o)))
	return *o, hr
}

// pattern is node p's pattern provider (0 when it has none).
func pattern(t *testing.T, p uintptr, id int32) uintptr {
	t.Helper()
	o := heapOut[uintptr]()
	if hr := vcall(p, slot(t, ifSimple, "GetPatternProvider"), uintptr(id), uintptr(unsafeAddr(o))); hr != hrOK {
		t.Fatalf("GetPatternProvider(%d) = %#x", id, hr)
	}
	return *o
}

// prop is property id of p.
func prop(t *testing.T, p uintptr, id int32) comobj.Variant {
	t.Helper()
	v := heapOut[comobj.Variant]()
	if hr := vcall(p, slot(t, ifSimple, "GetPropertyValue"), uintptr(id), uintptr(unsafeAddr(v))); hr != hrOK {
		t.Fatalf("GetPropertyValue(%d) = %#x", id, hr)
	}
	return *v
}

func propString(t *testing.T, p uintptr, id int32) string {
	t.Helper()
	v := prop(t, p, id)
	defer comobj.ClearVariant(&v)
	if v.VT == comobj.VT_EMPTY {
		return ""
	}
	if v.VT != comobj.VT_BSTR {
		t.Fatalf("property %d is VT %d, want a BSTR", id, v.VT)
	}
	return comobj.BSTRString(v.Pointer())
}

// ---- the window -----------------------------------------------------------------------------

var testFont = kit.Font{Family: "Segoe UI", Size: kit.Px(13), Weight: 400, LineHeight: 1.4}

// check is a check box the tests toggle.
type check struct {
	kit.Element
	on      bool
	toggles int
}

func (c *check) FillSemantics(n *kit.SemanticNode) {
	n.Toggle, n.Actions = kit.ToggleOff, n.Actions|kit.ActionToggle
	if c.on {
		n.Toggle = kit.ToggleOn
	}
}

func (c *check) PerformAction(r kit.ActionRequest) error {
	if r.Action != kit.ActionToggle {
		return kit.ErrUnsupported
	}
	c.on = !c.on
	c.toggles++
	return nil
}

// combo opens and closes.
type combo struct {
	kit.Element
	open bool
}

func (c *combo) FillSemantics(n *kit.SemanticNode) {
	n.Expand, n.Value = kit.ExpandClosed, "Default"
	if c.open {
		n.Expand = kit.ExpandOpen
	}
	n.Actions |= kit.ActionExpand | kit.ActionCollapse
}

func (c *combo) PerformAction(r kit.ActionRequest) error {
	switch r.Action {
	case kit.ActionExpand:
		c.open = true
	case kit.ActionCollapse:
		c.open = false
	default:
		return kit.ErrUnsupported
	}
	return nil
}

// slider is a range that can be set; with readOnly it is a progress bar.
type slider struct {
	kit.Element
	value    float64
	readOnly bool
	explode  bool // FillSemantics panics (a widget's bug)
}

func (s *slider) FillSemantics(n *kit.SemanticNode) {
	if s.explode {
		panic("slider: broken")
	}
	n.Range = kit.RangeValue{Valid: true, Min: 0, Max: 100, Value: s.value, Step: 1}
	if !s.readOnly {
		n.Actions |= kit.ActionSetRange
	}
}

func (s *slider) PerformAction(r kit.ActionRequest) error {
	if r.Action != kit.ActionSetRange {
		return kit.ErrUnsupported
	}
	s.value = r.Number
	return nil
}

type gridRows struct{ n int }

func (m gridRows) Len() int                        { return m.n }
func (m gridRows) Key(i int) kit.ListKey           { return kit.ListKey(i + 1) }
func (m gridRows) Index(k kit.ListKey) (int, bool) { return int(k) - 1, k >= 1 && int(k) <= m.n }
func (m gridRows) RowNode(i int, _ kit.ListKey) kit.SemanticNode {
	return kit.SemanticNode{Role: kit.RoleRow, Name: "row " + itoa(i)}
}
func (m gridRows) Cells() int { return 3 }
func (m gridRows) CellNode(i int, _ kit.ListKey, col int) kit.SemanticNode {
	return kit.SemanticNode{Role: kit.RoleCell, Name: "cell " + itoa(i) + "." + itoa(col)}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
}

type gridRow struct {
	kit.Element
	index int
	cells [3]*kit.Element
}

func (r *gridRow) BindRow(i int, _ kit.ListKey) { r.index = i; r.SemanticsChanged() }
func (r *gridRow) FillSemantics(n *kit.SemanticNode) {
	n.Name = "row " + itoa(r.index)
}

type gridSel struct {
	list *kit.List
	sel  kit.ListKey
}

func (s *gridSel) Selected(k kit.ListKey) bool { return k != 0 && k == s.sel }
func (s *gridSel) Cursor() (kit.ListKey, bool) { return s.sel, s.sel != 0 }
func (s *gridSel) SetCursor(kit.ListKey)       {}
func (s *gridSel) Click(k kit.ListKey, m kit.SelectMods) {
	if m.Toggle && s.sel == k {
		s.sel = 0
	} else {
		s.sel = k
	}
	s.list.SelectionChanged()
}
func (s *gridSel) ContextClick(k kit.ListKey)        { s.Click(k, kit.SelectMods{}) }
func (s *gridSel) Move(kit.ListMove, kit.SelectMods) {}
func (s *gridSel) ToggleCursor()                     {}
func (s *gridSel) AppendSelected(dst []kit.ListKey) []kit.ListKey {
	if s.sel != 0 {
		dst = append(dst, s.sel)
	}
	return dst
}

type fixture struct {
	win      *kit.Window
	loop     *kittest.Loop
	host     *fakeHost
	tree     *Tree
	button   *kit.Pressable
	pressed  int
	off      *kit.Pressable
	check    *check
	combo    *combo
	slider   *slider
	progress *slider
	field    *kit.TextField
	list     *kit.List
	sel      *gridSel
}

// fakeHost maps window DIPs to the screen at 125% from (100, 200).
type fakeHost struct {
	f      *fixture
	posted []kit.ActionRequest
}

func (h *fakeHost) HWND() uintptr                          { return 0 }
func (h *fakeHost) Semantics() kit.SemanticTree            { return h.f.win.Semantics() }
func (h *fakeHost) HostedWindow(*kit.SemanticNode) uintptr { return 0 }
func (h *fakeHost) HostedBounds(uintptr) (kit.Rect, bool)  { return kit.Rect{}, false }
func (h *fakeHost) CustomFrame() bool                      { return true }
func (h *fakeHost) ToScreen(r kit.Rect) Rect {
	return Rect{Left: 100 + 1.25*float64(r.Min.X), Top: 200 + 1.25*float64(r.Min.Y), Width: 1.25 * float64(r.Width()), Height: 1.25 * float64(r.Height())}
}
func (h *fakeHost) FromScreen(x, y float64) kit.Point {
	return kit.Pt(float32((x-100)/1.25), float32((y-200)/1.25))
}
func (h *fakeHost) Perform(r kit.ActionRequest) error {
	err := h.f.win.Semantics().Perform(r)
	h.f.loop.Step()
	return err
}
func (h *fakeHost) Post(r kit.ActionRequest) error { h.posted = append(h.posted, r); return nil }

func newFixture(t *testing.T) *fixture {
	t.Helper()
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)
	f := &fixture{}
	f.button = kit.NewPressable(func() { f.pressed++ })
	f.button.Semantics.Name = "Start"
	f.button.Style = kit.Style{Width: kit.Px(80), Height: kit.Px(30)}
	f.off = kit.NewPressable(nil)
	f.off.Semantics.Name = "Remove"
	f.off.Disabled = true
	f.off.Style = kit.Style{Width: kit.Px(80), Height: kit.Px(30)}
	f.check = &check{}
	f.check.Semantics = kit.Semantics{Role: kit.RoleCheckBox, Name: "Every day"}
	f.check.Style = kit.Style{Width: kit.Px(20), Height: kit.Px(20)}
	f.combo = &combo{}
	f.combo.Semantics = kit.Semantics{Role: kit.RoleComboBox, Name: "Category"}
	f.combo.Style = kit.Style{Width: kit.Px(120), Height: kit.Px(30)}
	f.slider = &slider{value: 40}
	f.slider.Semantics = kit.Semantics{Role: kit.RoleSlider, Name: "Limit"}
	f.slider.Style = kit.Style{Width: kit.Px(120), Height: kit.Px(20)}
	f.progress = &slider{value: 45, readOnly: true}
	f.progress.Semantics = kit.Semantics{Role: kit.RoleProgressBar, Name: "Progress", Value: "45%"}
	f.progress.Style = kit.Style{Width: kit.Px(120), Height: kit.Px(6)}
	f.field = kit.NewTextField(kit.TextFieldOptions{Name: "Address"})
	f.field.Style = kit.Style{Width: kit.Px(200), Height: kit.Px(28)}
	bar := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, ColumnGap: kit.Px(8), Height: kit.Px(40)}}
	bar.Semantics = kit.Semantics{Role: kit.RoleToolbar, Name: "Actions"}
	bar.Add(f.button, f.off, f.check, f.combo, f.slider, f.progress, f.field)

	rows := gridRows{n: 1000}
	f.sel = &gridSel{}
	f.list = kit.NewList(rows, func() kit.ListRow {
		r := &gridRow{}
		r.Style = kit.Style{Display: kit.DisplayFlex}
		r.Semantics.Role = kit.RoleRow
		for c := range r.cells {
			r.cells[c] = &kit.Element{Style: kit.Style{Flex: kit.Flex1}}
			r.cells[c].Semantics = kit.Semantics{Role: kit.RoleCell, Cell: kit.CellInfo{Col: c}, Name: "cell"}
			r.Add(r.cells[c])
		}
		return r
	}, func(kit.Scale) kit.DIP { return 30 })
	f.list.Selection, f.list.RowSemantics, f.sel.list = f.sel, rows, f.list
	f.list.Semantics = kit.Semantics{Role: kit.RoleGrid, Name: "Downloads"}
	f.list.Style = kit.Style{Flex: kit.Flex1, MinHeight: kit.Px(0)}
	head := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Height: kit.Px(24)}}
	head.Semantics.Role = kit.RoleRow
	for c, name := range []string{"Name", "Size", "Status"} {
		h := &kit.Element{Style: kit.Style{Flex: kit.Flex1}}
		h.Semantics = kit.Semantics{Role: kit.RoleColumnHeader, Name: name, Cell: kit.CellInfo{Col: c}, Sort: kit.SortNone}
		if c == 0 {
			h.Semantics.Sort = kit.SortAscending
		}
		head.Add(h)
	}
	f.list.SetHeader(head)

	page := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, Padding: kit.EdgesAll(kit.Px(8))}}
	page.Add(bar, f.list)
	clock := kittest.NewClock()
	f.win = kit.NewWindow(page, kit.WindowOptions{Title: "Downex", Now: clock.Now, Scheduler: clock,
		Palette: func(kit.Environment) kit.Palette { return kit.Palette{} }})
	f.loop = kittest.NewLoop(f.win, clock, kit.Size{W: 900, H: 600}, kit.Environment{Scale: kit.Scale{PixelsPerDIP: 1.25, Text: 1}})
	f.loop.Step()
	f.host = &fakeHost{f: f}
	var err error
	if f.tree, err = NewTree(f.host); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		f.tree.closed = true // no UiaDisconnectProvider for providers UI Automation never saw
		if n := Live(); n != 0 {
			t.Errorf("%d providers alive at the end: %v", n, f.tree.Held())
		}
	})
	return f
}

// get is the Simple interface of node id's provider, with a reference.
func (f *fixture) get(t *testing.T, id kit.NodeID) uintptr {
	t.Helper()
	p := f.tree.iface(id, ifSimple)
	if p == 0 {
		t.Fatalf("no provider for node %d", id)
	}
	return p
}

func (f *fixture) root(t *testing.T) uintptr { return f.get(t, f.win.Semantics().Root()) }

func TestProviderIdentityAndNavigation(t *testing.T) {
	f := newFixture(t)
	root := f.root(t)
	defer release(root)

	opts := heapOut[int32]()
	if hr := vcall(root, slot(t, ifSimple, "get_ProviderOptions"), uintptr(unsafeAddr(opts))); hr != hrOK ||
		*opts != ProviderOptions_ServerSideProvider|ProviderOptions_UseComThreading|ProviderOptions_RefuseNonClientSupport {
		t.Errorf("root options %#x, %#x", *opts, hr)
	}
	// QueryInterface: IUnknown is the Simple pointer; the root has FragmentRoot, a button
	// has not; a button has Invoke and no Toggle.
	if p, hr := qi(root, &IID_IUnknown); hr != hrOK || p != root {
		t.Errorf("QI(IUnknown) = %#x, %#x, want the Simple pointer %#x", p, hr, root)
	} else {
		release(p)
	}
	frag, hr := qi(root, &IID_IRawElementProviderFragmentRoot)
	if hr != hrOK || frag == 0 {
		t.Fatalf("root QI(FragmentRoot) %#x", hr)
	}
	if back, _ := qi(frag, &IID_IUnknown); back != root {
		t.Errorf("FragmentRoot's IUnknown %#x, want %#x", back, root)
	} else {
		release(back)
	}
	release(frag)
	btn := f.get(t, f.button.NodeID())
	defer release(btn)
	if p, hr := qi(btn, &IID_IRawElementProviderFragmentRoot); hr != hrNoInterface || p != 0 {
		t.Errorf("a button answered QI(FragmentRoot): %#x", hr)
	}
	if p, hr := qi(btn, &IID_IToggleProvider); hr != hrNoInterface || p != 0 {
		t.Errorf("a button answered QI(Toggle): %#x", hr)
	}
	if p := pattern(t, btn, UIA_InvokePatternId); p == 0 {
		t.Error("a button has no Invoke pattern")
	} else {
		release(p)
	}

	// Properties.
	if v := prop(t, root, UIA_ControlTypePropertyId); v.VT != comobj.VT_EMPTY {
		t.Errorf("the root's control type is VT %d, want the window's", v.VT)
	}
	if v := prop(t, btn, UIA_ControlTypePropertyId); v.VT != comobj.VT_I4 || v.I4() != UIA_ButtonControlTypeId {
		t.Errorf("button control type %+v", v)
	}
	if s := propString(t, btn, UIA_NamePropertyId); s != "Start" {
		t.Errorf("button name %q", s)
	}
	if s := propString(t, root, UIA_NamePropertyId); s != "Downex" {
		t.Errorf("root name %q", s)
	}

	// Navigation: the root's children, each one's parent, siblings both ways.
	navigate := func(p uintptr, dir int32) uintptr {
		o := heapOut[uintptr]()
		if hr := vcall(p, slot(t, ifFragment, "Navigate"), uintptr(dir), uintptr(unsafeAddr(o))); hr != hrOK {
			t.Fatalf("Navigate(%d) = %#x", dir, hr)
		}
		return *o
	}
	rootFrag := f.tree.iface(f.win.Semantics().Root(), ifFragment)
	defer release(rootFrag)
	if p := navigate(rootFrag, NavigateDirection_Parent); p != 0 {
		t.Error("the root has a parent in the fragment")
	}
	bar := navigate(rootFrag, NavigateDirection_FirstChild)
	if bar == 0 {
		t.Fatal("no first child")
	}
	var names []string
	for c := navigate(bar, NavigateDirection_FirstChild); c != 0; {
		simple, _ := qi(c, &IID_IRawElementProviderSimple)
		names = append(names, propString(t, simple, UIA_NamePropertyId))
		if p := navigate(c, NavigateDirection_Parent); p != bar {
			t.Errorf("a child's parent %#x, want the toolbar %#x", p, bar)
			release(p)
		} else {
			release(p)
		}
		release(simple)
		next := navigate(c, NavigateDirection_NextSibling)
		release(c)
		c = next
	}
	if want := []string{"Start", "Remove", "Every day", "Category", "Limit", "Progress", "Address"}; !slices.Equal(names, want) {
		t.Errorf("toolbar children %q, want %q", names, want)
	}
	last := navigate(bar, NavigateDirection_LastChild)
	prev := navigate(last, NavigateDirection_PreviousSibling)
	ps, _ := qi(prev, &IID_IRawElementProviderSimple)
	if s := propString(t, ps, UIA_NamePropertyId); s != "Progress" {
		t.Errorf("the last child's previous sibling is %q", s)
	}
	for _, p := range []uintptr{ps, prev, last, bar} {
		release(p)
	}

	// Runtime ids: none for the root (the window's), UiaAppendRuntimeId then the node's id.
	rid := heapOut[uintptr]()
	if hr := vcall(rootFrag, slot(t, ifFragment, "GetRuntimeId"), uintptr(unsafeAddr(rid))); hr != hrOK || *rid != 0 {
		t.Errorf("root runtime id %#x, %#x", *rid, hr)
	}
	bf, _ := qi(btn, &IID_IRawElementProviderFragment)
	defer release(bf)
	if hr := vcall(bf, slot(t, ifFragment, "GetRuntimeId"), uintptr(unsafeAddr(rid))); hr != hrOK {
		t.Fatal(hr)
	}
	got := comobj.I4Values(*rid)
	comobj.DestroyArray(*rid)
	if id := f.button.NodeID(); !slices.Equal(got, []int32{UiaAppendRuntimeId, int32(uint32(id)), int32(uint32(id >> 32))}) {
		t.Errorf("button runtime id %v", got)
	}

	// Bounds and hit testing through the screen mapping, the doubles through the thunk.
	r := heapOut[Rect]()
	if hr := vcall(bf, slot(t, ifFragment, "get_BoundingRectangle"), uintptr(unsafeAddr(r))); hr != hrOK {
		t.Fatal(hr)
	}
	if want := f.host.ToScreen(f.button.WindowBounds()); *r != want {
		t.Errorf("button rectangle %+v, want %+v", *r, want)
	}
	cx, cy := r.Left+r.Width/2, r.Top+r.Height/2
	fr, _ := qi(root, &IID_IRawElementProviderFragmentRoot)
	defer release(fr)
	hit := heapOut[uintptr]()
	if hr := vcall(fr, slot(t, ifRoot, "ElementProviderFromPoint"), uintptr(math.Float64bits(cx)), uintptr(math.Float64bits(cy)),
		uintptr(unsafeAddr(hit))); hr != hrOK || *hit != bf {
		t.Errorf("ElementProviderFromPoint(%v, %v) = %#x (%#x), want the button %#x", cx, cy, *hit, hr, bf)
	}
	if *hit != 0 {
		release(*hit)
	}
	if hr := vcall(fr, slot(t, ifRoot, "ElementProviderFromPoint"), uintptr(math.Float64bits(math.NaN())), 0,
		uintptr(unsafeAddr(hit))); hr != hrInvalidArg {
		t.Errorf("ElementProviderFromPoint(NaN) = %#x", hr)
	}

	// Focus: nothing is focused, then SetFocus moves the kit's focus and GetFocus finds it.
	foc := heapOut[uintptr]()
	if hr := vcall(fr, slot(t, ifRoot, "GetFocus"), uintptr(unsafeAddr(foc))); hr != hrOK || *foc != 0 {
		t.Errorf("GetFocus = %#x, %#x before any focus", *foc, hr)
	}
	if hr := vcall(bf, slot(t, ifFragment, "SetFocus")); hr != hrOK || !f.button.Focused() {
		t.Errorf("SetFocus %#x, focused %v", hr, f.button.Focused())
	}
	if hr := vcall(fr, slot(t, ifRoot, "GetFocus"), uintptr(unsafeAddr(foc))); hr != hrOK || *foc != bf {
		t.Errorf("GetFocus = %#x, want the button %#x", *foc, bf)
	}
	if *foc != 0 {
		release(*foc)
	}
	if v := prop(t, btn, UIA_HasKeyboardFocusPropertyId); v.VT != comobj.VT_BOOL || !v.Bool() {
		t.Errorf("HasKeyboardFocus %+v", v)
	}
}

func TestProviderPatterns(t *testing.T) {
	f := newFixture(t)
	invoke := func(p uintptr, k int, name string, args ...uintptr) uintptr {
		return vcall(p, slot(t, k, name), args...)
	}

	// Invoke is posted (it must not block); a disabled button refuses.
	btn := f.get(t, f.button.NodeID())
	ip := pattern(t, btn, UIA_InvokePatternId)
	if hr := invoke(ip, ifInvoke, "Invoke"); hr != hrOK || len(f.host.posted) != 1 || f.host.posted[0].Action != kit.ActionInvoke {
		t.Errorf("Invoke %#x, posted %v", hr, f.host.posted)
	}
	release(ip)
	release(btn)
	off := f.get(t, f.off.NodeID())
	ip = pattern(t, off, UIA_InvokePatternId)
	if hr := invoke(ip, ifInvoke, "Invoke"); hr != hrNotEnabled {
		t.Errorf("Invoke of a disabled button %#x, want UIA_E_ELEMENTNOTENABLED", hr)
	}
	if v := prop(t, off, UIA_IsEnabledPropertyId); v.VT != comobj.VT_BOOL || v.Bool() {
		t.Errorf("IsEnabled of a disabled button %+v", v)
	}
	release(ip)
	release(off)

	// Toggle.
	ck := f.get(t, f.check.NodeID())
	tp := pattern(t, ck, UIA_TogglePatternId)
	state := heapOut[int32]()
	invoke(tp, ifToggle, "get_ToggleState", uintptr(unsafeAddr(state)))
	if *state != ToggleState_Off {
		t.Errorf("toggle state %d", *state)
	}
	if hr := invoke(tp, ifToggle, "Toggle"); hr != hrOK || f.check.toggles != 1 {
		t.Errorf("Toggle %#x, toggles %d", hr, f.check.toggles)
	}
	invoke(tp, ifToggle, "get_ToggleState", uintptr(unsafeAddr(state)))
	if *state != ToggleState_On {
		t.Errorf("toggle state after Toggle %d", *state)
	}
	if p := pattern(t, ck, UIA_InvokePatternId); p != 0 {
		t.Error("a check box has the Invoke pattern")
		release(p)
	}
	release(tp)
	release(ck)

	// ExpandCollapse.
	cb := f.get(t, f.combo.NodeID())
	ep := pattern(t, cb, UIA_ExpandCollapsePatternId)
	invoke(ep, ifExpandCollapse, "get_ExpandCollapseState", uintptr(unsafeAddr(state)))
	if *state != ExpandCollapseState_Collapsed {
		t.Errorf("expand state %d", *state)
	}
	if hr := invoke(ep, ifExpandCollapse, "Expand"); hr != hrOK || !f.combo.open {
		t.Errorf("Expand %#x", hr)
	}
	invoke(ep, ifExpandCollapse, "get_ExpandCollapseState", uintptr(unsafeAddr(state)))
	if *state != ExpandCollapseState_Expanded {
		t.Errorf("expand state after Expand %d", *state)
	}
	if hr := invoke(ep, ifExpandCollapse, "Collapse"); hr != hrOK || f.combo.open {
		t.Errorf("Collapse %#x", hr)
	}
	release(ep)
	release(cb)

	// RangeValue: the double through the thunk, the range checked, a progress bar read-only.
	sl := f.get(t, f.slider.NodeID())
	rp := pattern(t, sl, UIA_RangeValuePatternId)
	if hr := invoke(rp, ifRangeValue, "SetValue", uintptr(math.Float64bits(75.5))); hr != hrOK || f.slider.value != 75.5 {
		t.Errorf("SetValue(75.5) %#x, value %v", hr, f.slider.value)
	}
	if hr := invoke(rp, ifRangeValue, "SetValue", uintptr(math.Float64bits(150))); hr != hrInvalidArg {
		t.Errorf("SetValue(150) %#x, want E_INVALIDARG", hr)
	}
	d := heapOut[float64]()
	for _, c := range []struct {
		name string
		want float64
	}{{"get_Value", 75.5}, {"get_Maximum", 100}, {"get_Minimum", 0}, {"get_SmallChange", 1}, {"get_LargeChange", 1}} {
		if hr := invoke(rp, ifRangeValue, c.name, uintptr(unsafeAddr(d))); hr != hrOK || *d != c.want {
			t.Errorf("%s = %v (%#x), want %v", c.name, *d, hr, c.want)
		}
	}
	release(rp)
	release(sl)
	pb := f.get(t, f.progress.NodeID())
	rp = pattern(t, pb, UIA_RangeValuePatternId)
	ro := heapOut[int32]()
	if invoke(rp, ifRangeValue, "get_IsReadOnly", uintptr(unsafeAddr(ro))); *ro != 1 {
		t.Error("a progress bar's range is not read-only")
	}
	if hr := invoke(rp, ifRangeValue, "SetValue", uintptr(math.Float64bits(10))); hr != hrInvalidOperation {
		t.Errorf("SetValue on a progress bar %#x", hr)
	}
	vp := pattern(t, pb, UIA_ValuePatternId)
	b := heapOut[uintptr]()
	if invoke(vp, ifValue, "get_Value", uintptr(unsafeAddr(b))); comobj.BSTRString(*b) != "45%" {
		t.Errorf("progress value text %q", comobj.BSTRString(*b))
	}
	comobj.FreeBSTR(*b)
	if v := prop(t, pb, UIA_ControlTypePropertyId); v.I4() != UIA_ProgressBarControlTypeId {
		t.Errorf("progress control type %d", v.I4())
	}
	release(vp)
	release(rp)
	release(pb)

	// Value: a text field's text set as typing would.
	fd := f.get(t, f.field.NodeID())
	vp = pattern(t, fd, UIA_ValuePatternId)
	text := []uint16{'h', 't', 't', 'p', ':', '/', '/', 'x', 0}
	if hr := invoke(vp, ifValue, "SetValue", uintptr(unsafeAddr(&text[0]))); hr != hrOK || f.field.Text() != "http://x" {
		t.Errorf("SetValue %#x, text %q", hr, f.field.Text())
	}
	if invoke(vp, ifValue, "get_Value", uintptr(unsafeAddr(b))); comobj.BSTRString(*b) != "http://x" {
		t.Errorf("get_Value %q", comobj.BSTRString(*b))
	}
	comobj.FreeBSTR(*b)
	if invoke(vp, ifValue, "get_IsReadOnly", uintptr(unsafeAddr(ro))); *ro != 0 {
		t.Error("the field is read-only")
	}
	if s := propString(t, fd, UIA_NamePropertyId); s != "Address" {
		t.Errorf("field name %q", s)
	}
	release(vp)
	release(fd)
}

func TestProviderGrid(t *testing.T) {
	f := newFixture(t)
	invoke := func(p uintptr, k int, name string, args ...uintptr) uintptr {
		return vcall(p, slot(t, k, name), args...)
	}
	g := f.get(t, f.list.NodeID())
	defer release(g)
	if v := prop(t, g, UIA_ControlTypePropertyId); v.I4() != UIA_DataGridControlTypeId {
		t.Errorf("grid control type %d", v.I4())
	}
	gp := pattern(t, g, UIA_GridPatternId)
	defer release(gp)
	n := heapOut[int32]()
	if invoke(gp, ifGrid, "get_RowCount", uintptr(unsafeAddr(n))); *n != 1001 {
		t.Errorf("rows %d, want 1001 (the header's included)", *n)
	}
	if invoke(gp, ifGrid, "get_ColumnCount", uintptr(unsafeAddr(n))); *n != 3 {
		t.Errorf("columns %d", *n)
	}
	cell := heapOut[uintptr]()
	if hr := invoke(gp, ifGrid, "GetItem", 701, 2, uintptr(unsafeAddr(cell))); hr != hrOK || *cell == 0 {
		t.Fatalf("GetItem(701, 2) %#x", hr)
	}
	if s := propString(t, *cell, UIA_NamePropertyId); s != "cell 700.2" {
		t.Errorf("virtual cell %q", s)
	}
	gi := pattern(t, *cell, UIA_GridItemPatternId)
	if invoke(gi, ifGridItem, "get_Row", uintptr(unsafeAddr(n))); *n != 701 {
		t.Errorf("cell row %d", *n)
	}
	if invoke(gi, ifGridItem, "get_Column", uintptr(unsafeAddr(n))); *n != 2 {
		t.Errorf("cell column %d", *n)
	}
	if invoke(gi, ifGridItem, "get_RowSpan", uintptr(unsafeAddr(n))); *n != 1 {
		t.Errorf("cell row span %d", *n)
	}
	cg := heapOut[uintptr]()
	if invoke(gi, ifGridItem, "get_ContainingGrid", uintptr(unsafeAddr(cg))); *cg != g {
		t.Errorf("containing grid %#x, want %#x", *cg, g)
	}
	if *cg != 0 {
		release(*cg)
	}
	ti := pattern(t, *cell, UIA_TableItemPatternId)
	arr := heapOut[uintptr]()
	invoke(ti, ifTableItem, "GetColumnHeaderItems", uintptr(unsafeAddr(arr)))
	heads := comobj.UnknownValues(*arr)
	comobj.DestroyArray(*arr)
	if len(heads) != 1 || propString(t, heads[0], UIA_NamePropertyId) != "Status" {
		t.Errorf("the cell's column header: %d of them", len(heads))
	}
	for _, h := range heads {
		release(h)
	}
	release(ti)
	release(gi)
	release(*cell)
	if hr := invoke(gp, ifGrid, "GetItem", 1001, 0, uintptr(unsafeAddr(cell))); hr != hrInvalidArg {
		t.Errorf("GetItem out of range %#x", hr)
	}

	// Table: the column headers, one sorted.
	tp := pattern(t, g, UIA_TablePatternId)
	invoke(tp, ifTable, "GetColumnHeaders", uintptr(unsafeAddr(arr)))
	heads = comobj.UnknownValues(*arr)
	comobj.DestroyArray(*arr)
	var names []string
	for _, h := range heads {
		names = append(names, propString(t, h, UIA_NamePropertyId))
	}
	if !slices.Equal(names, []string{"Name", "Size", "Status"}) {
		t.Errorf("column headers %q", names)
	}
	if s := propString(t, heads[0], UIA_AriaPropertiesPropertyId); s != "sort=ascending" {
		t.Errorf("sorted header's AriaProperties %q", s)
	}
	if v := prop(t, heads[0], UIA_ControlTypePropertyId); v.I4() != UIA_HeaderItemControlTypeId {
		t.Errorf("header control type %d", v.I4())
	}
	for _, h := range heads {
		release(h)
	}
	release(tp)

	// Selection: a row selected through SelectionItem is the grid's selection.
	rowID := f.win.Semantics().AppendChildren(nil, f.list.NodeID())[1] // [0] is the header row
	row := f.get(t, rowID)
	defer release(row)
	if s := propString(t, row, UIA_NamePropertyId); s != "row 0" {
		t.Errorf("first row %q", s)
	}
	si := pattern(t, row, UIA_SelectionItemPatternId)
	defer release(si)
	if hr := invoke(si, ifSelectionItem, "Select"); hr != hrOK {
		t.Fatalf("Select %#x", hr)
	}
	on := heapOut[int32]()
	if invoke(si, ifSelectionItem, "get_IsSelected", uintptr(unsafeAddr(on))); *on != 1 {
		t.Error("the row is not selected")
	}
	sp := pattern(t, g, UIA_SelectionPatternId)
	defer release(sp)
	invoke(sp, ifSelection, "GetSelection", uintptr(unsafeAddr(arr)))
	sel := comobj.UnknownValues(*arr)
	comobj.DestroyArray(*arr)
	if len(sel) != 1 || sel[0] != row {
		t.Errorf("selection %v, want the row %#x", sel, row)
	}
	for _, s := range sel {
		release(s)
	}
	cont := heapOut[uintptr]()
	if invoke(si, ifSelectionItem, "get_SelectionContainer", uintptr(unsafeAddr(cont))); *cont != g {
		t.Errorf("selection container %#x, want the grid %#x", *cont, g)
	}
	if *cont != 0 {
		release(*cont)
	}
	if hr := invoke(si, ifSelectionItem, "RemoveFromSelection"); hr != hrOK || f.sel.sel != 0 {
		t.Errorf("RemoveFromSelection %#x, selection %d", hr, f.sel.sel)
	}
}

// A provider outlives what it stands for: its node's removal, the window's closing, a
// call on the wrong thread and a widget's panic all fail as UI Automation expects, and
// the last Release frees it.
func TestProviderFailures(t *testing.T) {
	f := newFixture(t)
	sl := f.get(t, f.slider.NodeID())
	name := slot(t, ifSimple, "GetPropertyValue")
	v := heapOut[comobj.Variant]()

	// Another thread: refused, never read.
	before := Refused()
	done := make(chan uintptr)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		ov := heapOut[comobj.Variant]()
		hr := vcall(sl, name, UIA_NamePropertyId, uintptr(unsafeAddr(ov)))
		runtime.KeepAlive(ov)
		done <- hr
	}()
	if hr := <-done; hr != hrWrongThread || Refused() != before+1 {
		t.Errorf("a call from another thread = %#x (refused %d)", hr, Refused()-before)
	}

	// A panic in a widget's FillSemantics becomes E_FAIL.
	f.slider.explode = true
	if hr := vcall(sl, name, UIA_NamePropertyId, uintptr(unsafeAddr(v))); hr != hrFail || Panics() == 0 {
		t.Errorf("a panicking widget = %#x", hr)
	}
	f.slider.explode = false

	// The node removed: not available.
	f.slider.Base().Parent().Remove(f.slider)
	f.loop.Step()
	if hr := vcall(sl, name, UIA_NamePropertyId, uintptr(unsafeAddr(v))); hr != hrNotAvailable {
		t.Errorf("a removed node's property = %#x, want UIA_E_ELEMENTNOTAVAILABLE", hr)
	}

	// The window closed: every call fails, and the last Release frees the object.
	btn := f.get(t, f.button.NodeID())
	f.tree.closed = true
	if hr := vcall(btn, name, UIA_NamePropertyId, uintptr(unsafeAddr(v))); hr != hrNotAvailable {
		t.Errorf("a closed window's property = %#x", hr)
	}
	if n := Live(); n != 2 {
		t.Errorf("%d providers alive, want the two held", n)
	}
	if n := release(btn); n != 0 {
		t.Errorf("Release left %d references", n)
	}
	release(sl)
	if n := Live(); n != 0 {
		t.Errorf("%d providers alive after the last Release", n)
	}
}
