package kit_test

import (
	"errors"
	"testing"

	"downex/ui/kit"
)

// TestSemanticTree checks the nodes a small toolbar makes: layout boxes are not exposed,
// a button is named by its text and hides its children, a label is text.
func TestSemanticTree(t *testing.T) {
	pressed := 0
	pause := kit.NewPressable(func() { pressed++ })
	pause.Style = kit.Style{Display: kit.DisplayFlex, Padding: kit.EdgesInline(kit.Px(11)), Height: kit.Px(30)}
	pauseText := kit.NewLabel("Pause", font13, kit.TextOptions{})
	pause.Add(pauseText)
	pause.Semantics.Shortcut = "Space"
	off := kit.NewPressable(nil)
	off.Style = kit.Style{Width: kit.Px(30), Height: kit.Px(30)}
	off.Semantics.Name = "Remove"
	off.Disabled, off.DisabledFocusable = true, true
	hint := kit.NewLabel("28 downloads", font13, kit.TextOptions{})
	deco := kit.NewLabel("decoration", font13, kit.TextOptions{})
	deco.Semantics.Hidden = true
	toolbar := box(kit.Style{Display: kit.DisplayFlex, ColumnGap: kit.Px(8)}, pause, off, hint, deco)
	toolbar.Semantics.Role = kit.RoleToolbar
	toolbar.Semantics.Name = "Actions"
	page := box(kit.Style{Padding: kit.EdgesAll(kit.Px(10))}, toolbar) // RoleNone: looked through
	w, loop := open(t, page, kit.Size{W: 600, H: 200}, env100)
	tree := w.Semantics()

	root, ok := tree.Node(tree.Root())
	if !ok || root.Role != kit.RoleWindow || root.Name != "Downex" || root.Parent != 0 {
		t.Fatalf("root %+v", root)
	}
	kids := tree.AppendChildren(nil, tree.Root())
	if len(kids) != 1 || kids[0] != toolbar.NodeID() {
		t.Fatalf("root children %v, want the toolbar alone (the page box is not exposed)", kids)
	}
	tb := tree.AppendChildren(nil, toolbar.NodeID())
	if len(tb) != 3 {
		t.Fatalf("toolbar children %v, want Pause, Remove and the text (the decoration is hidden)", tb)
	}
	n, _ := tree.Node(tb[0])
	if n.Role != kit.RoleButton || n.Name != "Pause" || n.Parent != toolbar.NodeID() || n.Shortcut != "Space" ||
		n.Actions&kit.ActionInvoke == 0 || n.Actions&kit.ActionFocus == 0 || n.States&kit.NodeFocusable == 0 {
		t.Errorf("Pause node %+v", n)
	}
	if got := tree.AppendChildren(nil, tb[0]); len(got) != 0 {
		t.Errorf("a button's children are presentational, got %v", got)
	}
	if n, _ := tree.Node(tb[2]); n.Role != kit.RoleText || n.Name != "28 downloads" {
		t.Errorf("text node %+v", n)
	}
	if n, _ := tree.Node(tb[1]); n.States&kit.NodeDisabled == 0 || n.States&kit.NodeFocusable == 0 {
		t.Errorf("aria-disabled button %+v: disabled and still focusable", n)
	}
	if b := pause.WindowBounds(); n.Bounds == (kit.Rect{}) || b.Empty() {
		t.Error("nodes have bounds")
	}
	if got := tree.HitTest(pauseText.WindowBounds().Center()); got != pause.NodeID() {
		t.Errorf("HitTest on the button's text = %d, want the button %d", got, pause.NodeID())
	}

	// Actions.
	if err := tree.Perform(kit.ActionRequest{Node: pause.NodeID(), Action: kit.ActionInvoke}); err != nil || pressed != 1 {
		t.Errorf("Invoke: %v, %d presses", err, pressed)
	}
	for _, c := range []struct {
		r    kit.ActionRequest
		want error
	}{
		{kit.ActionRequest{Node: pause.NodeID(), Action: kit.ActionToggle}, kit.ErrUnsupported},
		{kit.ActionRequest{Node: off.NodeID(), Action: kit.ActionInvoke}, kit.ErrNotEnabled},
		{kit.ActionRequest{Node: 999_999, Action: kit.ActionInvoke}, kit.ErrNoNode},
		{kit.ActionRequest{Node: page.NodeID(), Action: kit.ActionInvoke}, kit.ErrNoNode},
	} {
		if err := tree.Perform(c.r); !errors.Is(err, c.want) {
			t.Errorf("Perform(%+v) = %v, want %v", c.r, err, c.want)
		}
	}
	if err := tree.Perform(kit.ActionRequest{Node: pause.NodeID(), Action: kit.ActionFocus}); err != nil || w.Focused() != &pause.Element {
		t.Errorf("Focus action: %v, focused %v", err, name(w.Focused()))
	}
	if !w.RingShown() {
		t.Error("focus from assistive technology shows the ring")
	}

	// Changes reach the host as events: focus, a name from content, children.
	h := loop.Host
	h.Semantics = nil
	w.Focus(off)
	pauseText.SetText("Resume")
	toolbar.Add(kit.NewLabel("new", font13, kit.TextOptions{}))
	loop.Step()
	has := func(kind kit.SemanticEventKind, id kit.NodeID) bool {
		for _, e := range h.Semantics {
			if e.Kind == kind && e.Node == id {
				return true
			}
		}
		return false
	}
	if !has(kit.SemanticFocusChanged, off.NodeID()) {
		t.Errorf("no FocusChanged for Remove in %+v", h.Semantics)
	}
	if !has(kit.SemanticNodeChanged, pause.NodeID()) {
		t.Errorf("the button's name changed with its text, but no NodeChanged: %+v", h.Semantics)
	}
	if !has(kit.SemanticChildrenChanged, toolbar.NodeID()) {
		t.Errorf("no ChildrenChanged for the toolbar: %+v", h.Semantics)
	}
	if n, _ := tree.Node(pause.NodeID()); n.Name != "Resume" {
		t.Errorf("button name %q after the change", n.Name)
	}
	w.Announce("Downloading, 42%", kit.LivePolite, nil)
	if last := h.Semantics[len(h.Semantics)-1]; last.Kind != kit.SemanticAnnounce || last.Text != "Downloading, 42%" {
		t.Errorf("Announce sent %+v", last)
	}
}

// TestSemanticsOffscreenAndInert checks NodeOffscreen for what a clipping box cuts and
// that inert content is not in the tree.
func TestSemanticsOffscreenAndInert(t *testing.T) {
	inView, cut := button("in view"), button("cut")
	strip := box(kit.Style{Display: kit.DisplayFlex, Width: kit.Px(70), Overflow: kit.OverflowHidden}, inView, cut)
	cut.Style.Flex = kit.FlexNone
	inView.Style.Flex = kit.FlexNone
	behind := button("behind")
	inert := box(kit.Style{}, behind)
	inert.Inert = true
	w, _ := open(t, box(kit.Style{}, strip, inert), kit.Size{W: 400, H: 200}, env100)
	tree := w.Semantics()
	if n, _ := tree.Node(inView.NodeID()); n.States&kit.NodeOffscreen != 0 {
		t.Error("the visible button is offscreen")
	}
	if n, _ := tree.Node(cut.NodeID()); n.States&kit.NodeOffscreen == 0 {
		t.Errorf("the clipped button is not offscreen: %v", n.Bounds)
	}
	if _, ok := tree.Node(behind.NodeID()); ok {
		t.Error("an inert button is in the tree")
	}
	if w.Focus(behind) {
		t.Error("an inert button took the focus")
	}
}

// TestPressableKeys checks HTML button activation: Enter on key down, Space on key up.
func TestPressableKeys(t *testing.T) {
	n := 0
	b := kit.NewPressable(func() { n++ })
	b.Style = kit.Style{Width: kit.Px(50), Height: kit.Px(30)}
	w, loop := open(t, box(kit.Style{}, b), kit.Size{W: 200, H: 100}, env100)
	w.Focus(b)
	loop.Send(kit.KeyEvent{Action: kit.KeyDown, Key: kit.KeyEnter})
	if n != 1 {
		t.Errorf("Enter: %d presses", n)
	}
	loop.Send(kit.KeyEvent{Action: kit.KeyDown, Key: kit.KeySpace})
	if n != 1 || !b.Down() {
		t.Errorf("Space down: %d presses, down %v", n, b.Down())
	}
	loop.Send(kit.KeyEvent{Action: kit.KeyUp, Key: kit.KeySpace})
	if n != 2 || b.Down() {
		t.Errorf("Space up: %d presses, down %v", n, b.Down())
	}
	b.Disabled = true
	loop.Send(kit.KeyEvent{Action: kit.KeyDown, Key: kit.KeyEnter})
	if n != 2 {
		t.Error("a disabled button pressed")
	}
}

// TestNoReaderNoEvents checks that a window nobody reads with assistive technology sends
// no semantic events, and that once the tree is read, changes are reported.
func TestNoReaderNoEvents(t *testing.T) {
	f := newGrid(t, 1000, kit.Size{W: 800, H: 600})
	f.w.Focus(f.list)
	for k := kit.ListKey(1); k <= 10; k++ {
		f.m.pct[k] = 50
		f.list.RowChanged(k)
	}
	f.loop.Settle(10)
	if n := f.loop.Host.SemanticCount; n != 0 {
		t.Errorf("%d semantic events with no reader", n)
	}
	tree := f.w.Semantics()
	tree.Root()
	f.list.RowChanged(3)
	f.key(kit.KeyEvent{Key: kit.KeyArrowDown})
	if n := f.loop.Host.SemanticCount; n == 0 {
		t.Error("no semantic events once the tree is read")
	}
	if got := tree.Focused(); got != f.list.RowWidget(2).Base().NodeID() {
		t.Errorf("focused node %d, want the cursor's row", got)
	}
}
