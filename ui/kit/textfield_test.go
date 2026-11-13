package kit_test

import (
	"strings"
	"testing"

	"downex/ui/kit"
	"downex/ui/kit/kittest"
)

// A native field follows its box: it fills the content box in window coordinates, hides
// with it and goes with the widget.
func TestTextFieldPlacement(t *testing.T) {
	f := kit.NewTextField(kit.TextFieldOptions{Placeholder: "Search downloads", Name: "Search downloads"})
	f.Style = kit.Style{
		Width: kit.Px(440), Height: kit.Px(28),
		Border:  kit.EdgesAll(kit.Px(1)),
		Padding: kit.Edges{Start: kit.Px(32), End: kit.Px(62)},
	}
	page := box(kit.Style{Padding: kit.EdgesOf(kit.Px(8), kit.Px(337))}, f)
	w, loop := open(t, page, kit.Size{W: 1100, H: 720}, env125)
	if len(loop.Host.Fields) != 1 {
		t.Fatalf("%d native fields made, want 1", len(loop.Host.Fields))
	}
	nf := loop.Host.Fields[0]
	if want := kit.R(337+1+32, 8+1, 337+440-1-62, 8+28-1); nf.Bounds != want {
		t.Errorf("field placed at %v, want the content box %v", nf.Bounds, want)
	}
	if nf.Opts.Placeholder != "Search downloads" {
		t.Errorf("options %+v", nf.Opts)
	}
	moves := nf.SetBoundsCalls
	loop.Step()
	if nf.SetBoundsCalls != moves {
		t.Error("an idle frame moved the field again")
	}
	f.SetHidden(true)
	loop.Step()
	if nf.Shown() {
		t.Errorf("a hidden field is still placed at %v", nf.Bounds)
	}
	f.SetHidden(false)
	loop.Step()
	if !nf.Shown() {
		t.Error("the field did not come back")
	}
	page.Remove(f)
	loop.Step()
	if !nf.Released {
		t.Error("a field taken out of the window was not released")
	}
	_ = w
}

// Typing reaches OnChange; SetText does not, as setting an input's value fires no input
// event. A click into the field gives the widget the kit's focus; Tab into it selects its
// text, and Tab out of it gives the system's focus back to the window.
func TestTextFieldTextAndFocus(t *testing.T) {
	before := kit.NewPressable(nil)
	before.Style = kit.Style{Width: kit.Px(30), Height: kit.Px(30)}
	f := kit.NewTextField(kit.TextFieldOptions{})
	f.Style = kit.Style{Width: kit.Px(200), Height: kit.Px(28)}
	after := kit.NewPressable(nil)
	after.Style = kit.Style{Width: kit.Px(30), Height: kit.Px(30)}
	var changes []string
	f.OnChange = func(s string) { changes = append(changes, s) }
	page := box(kit.Style{Display: kit.DisplayFlex}, before, f, after)
	w, loop := open(t, page, kit.Size{W: 400, H: 100}, env100)
	nf := loop.Host.Fields[0]

	nf.Type("ubuntu")
	if f.Text() != "ubuntu" || len(changes) != 1 || changes[0] != "ubuntu" {
		t.Errorf("after typing: text %q, changes %q", f.Text(), changes)
	}
	f.SetText("")
	if nf.Value != "" || len(changes) != 1 {
		t.Errorf("SetText: field %q, changes %q", nf.Value, changes)
	}

	nf.Click()
	if w.Focused() != f.Base() {
		t.Fatalf("a click into the field left the focus on %v", w.Focused())
	}
	if nf.Selected {
		t.Error("a click selected the text")
	}

	w.Focus(before)
	if nf.HasFocus {
		t.Error("the field kept the system's focus after the kit's moved away")
	}
	loop.Send(kit.KeyEvent{Action: kit.KeyDown, Key: kit.KeyTab})
	if w.Focused() != f.Base() || !nf.HasFocus || !nf.Selected {
		t.Errorf("Tab into the field: focused %v, native focus %v, selected %v", w.Focused(), nf.HasFocus, nf.Selected)
	}
	// The keys of a focused native field reach the root first: Tab moves the focus on.
	if !loop.Send(kit.KeyEvent{Action: kit.KeyDown, Key: kit.KeyTab}) {
		t.Error("Tab in the field was not used by the window")
	}
	if w.Focused() != after.Base() || nf.HasFocus {
		t.Errorf("Tab out of the field: focused %v, native focus %v", w.Focused(), nf.HasFocus)
	}

	// The window loses the focus with the field focused and gets it back (Alt+Tab).
	nf.Click()
	nf.LoseFocus()
	loop.Send(kit.FocusEvent{In: false})
	loop.Send(kit.FocusEvent{In: true})
	if !nf.HasFocus {
		t.Error("the field did not take the system's focus back with the window")
	}
}

// The native field's callbacks are calls into the window, as events are: what OnChange
// defers runs once it has returned, and the semantic events of a change (the field's value)
// reach the host as the callback returns, not with the next input event.
func TestTextFieldCallbacksAreCalls(t *testing.T) {
	f := kit.NewTextField(kit.TextFieldOptions{Name: "Search downloads"})
	f.Style = kit.Style{Width: kit.Px(200), Height: kit.Px(28)}
	page := box(kit.Style{}, f)
	w, loop := open(t, page, kit.Size{W: 400, H: 100}, env100)
	nf := loop.Host.Fields[0]
	tree := w.Semantics()
	if _, ok := tree.Node(f.NodeID()); !ok {
		t.Fatal("the field has no node")
	}
	var order []string
	f.OnChange = func(s string) {
		w.Defer(func() { order = append(order, "deferred") })
		order = append(order, "change "+s)
	}
	loop.Host.Semantics = nil
	nf.Type("iso")
	if got := strings.Join(order, ", "); got != "change iso, deferred" {
		t.Errorf("order %q, want the deferred function after OnChange", got)
	}
	var changed bool
	for _, e := range loop.Host.Semantics {
		if e.Kind == kit.SemanticNodeChanged && e.Node == f.NodeID() {
			changed = true
		}
	}
	if !changed {
		t.Errorf("no NodeChanged for the field's new value as the callback returned: %+v", loop.Host.Semantics)
	}

	order = nil
	f.OnChange = nil
	w.Blur()
	nf.Click()
	if w.Focused() != f.Base() {
		t.Errorf("a click into the field left the focus on %v", w.Focused())
	}
}

// AlignsRight: end is the right edge of a left-to-right field and the left of a
// right-to-left one; start the other way; center neither.
func TestTextFieldOptionsAlignsRight(t *testing.T) {
	for _, c := range []struct {
		align kit.TextAlign
		dir   kit.Direction
		want  bool
	}{
		{kit.TextAlignStart, kit.DirLTR, false},
		{kit.TextAlignEnd, kit.DirLTR, true},
		{kit.TextAlignStart, kit.DirRTL, true},
		{kit.TextAlignEnd, kit.DirRTL, false},
		{kit.TextAlignCenter, kit.DirLTR, false},
		{kit.TextAlignCenter, kit.DirRTL, false},
	} {
		if got := (kit.TextFieldOptions{Align: c.align, Direction: c.dir}).AlignsRight(); got != c.want {
			t.Errorf("align %d, direction %d: AlignsRight %v, want %v", c.align, c.dir, got, c.want)
		}
	}
}

// standInField is a field whose owner paints the kit's stand-in for its text (as the title
// bar's search and widgets.Input do).
type standInField struct{ kit.TextField }

func (f *standInField) Paint(p *kit.PaintContext) { f.PaintStandIn(p) }

// standInTexts are the texts the stand-in drew in the frames since ops was cut.
func standInTexts(ops []kittest.Op) []string {
	var s []string
	for _, op := range ops {
		if op.Kind == "DrawText" {
			s = append(s, op.Text)
		}
	}
	return s
}

// While the kit stands in for the native field (SetStandIn: the title bar's search under a
// modal, faded), the native field hides and the kit draws the placeholder, then the text, in
// their colours, inside the content box; a covered field is drawn the same way under its layer.
// Back, the native field shows again and the kit draws nothing, and no layout is left alive.
func TestTextFieldStandIn(t *testing.T) {
	f := &standInField{}
	f.TextField = *kit.NewTextField(kit.TextFieldOptions{Placeholder: "Search downloads", Name: "Search downloads",
		Color: kit.RGB(0x101010), PlaceholderColor: kit.RGB(0x909090), Style: kit.TextStyle{Family: "Segoe UI", Size: 13, Weight: 400}})
	f.Style = kit.Style{Width: kit.Px(300), Height: kit.Px(28), Border: kit.EdgesAll(kit.Px(1)), Padding: kit.EdgesInline(kit.Px(30))}
	page := box(kit.Style{Padding: kit.EdgesAll(kit.Px(10))}, f)
	_, loop := open(t, page, kit.Size{W: 400, H: 100}, env100)
	nf := loop.Host.Fields[0]
	if !nf.Shown() || len(standInTexts(loop.Canvas.Ops)) != 0 {
		t.Fatalf("before: shown %v, drawn %q", nf.Shown(), standInTexts(loop.Canvas.Ops))
	}

	cut := len(loop.Canvas.Ops)
	f.SetStandIn(true)
	loop.Step()
	if nf.Shown() {
		t.Error("the native field still shows while the kit stands in for it")
	}
	var at kit.Point
	var ink kit.Color
	for _, op := range loop.Canvas.Ops[cut:] {
		if op.Kind == "DrawText" {
			at, ink = op.At, op.Color
		}
	}
	if got := standInTexts(loop.Canvas.Ops[cut:]); len(got) != 1 || got[0] != "Search downloads" || ink != kit.RGB(0x909090) {
		t.Errorf("stood in for, empty: drew %q in %v, want the placeholder in its colour", got, ink)
	}
	if cb := f.WindowBounds(); at.X != cb.Min.X+1+30 {
		t.Errorf("the placeholder starts at x %v, want the content box's start %v", at.X, cb.Min.X+1+30)
	}

	cut = len(loop.Canvas.Ops)
	f.SetText("ubuntu")
	loop.Step()
	if got := standInTexts(loop.Canvas.Ops[cut:]); len(got) != 1 || got[0] != "ubuntu" {
		t.Errorf("stood in for, with text: drew %q", got)
	}

	cut = len(loop.Canvas.Ops)
	f.SetStandIn(false)
	loop.Step()
	if !nf.Shown() || nf.Value != "ubuntu" {
		t.Errorf("back: shown %v, value %q", nf.Shown(), nf.Value)
	}
	if got := standInTexts(loop.Canvas.Ops[cut:]); len(got) != 0 {
		t.Errorf("back: the kit still drew %q", got)
	}

	cut = len(loop.Canvas.Ops)
	f.SetCovered(true)
	loop.Step()
	if got := standInTexts(loop.Canvas.Ops[cut:]); nf.Shown() || len(got) != 1 || got[0] != "ubuntu" {
		t.Errorf("covered: shown %v, drew %q", nf.Shown(), got)
	}
	f.SetCovered(false)
	loop.Step()
	page.Remove(f)
	loop.Step()
	if live := loop.Host.Res.Live; live != 0 {
		t.Errorf("%d text layouts alive after the field went", live)
	}
}

// A field scrolled partly out of its clipping box is drawn by the kit, clipped with the page:
// the host's field cannot be clipped, and centring a whole line on the part that shows drew
// it past the box's edge (Settings: a field at the pane's bottom over the dialog's footer).
// While it is typed in, the host's field shows on the part in view; a field wholly in view
// is the host's.
func TestTextFieldPartlyScrolledOut(t *testing.T) {
	f := &standInField{}
	f.TextField = *kit.NewTextField(kit.TextFieldOptions{Placeholder: "The scanner's program", Name: "Scanner program",
		Color: kit.RGB(0x101010), PlaceholderColor: kit.RGB(0x909090), Style: kit.TextStyle{Family: "Segoe UI", Size: 13, Weight: 400}})
	f.Style = kit.Style{Width: kit.Px(300), Height: kit.Px(28), Border: kit.EdgesAll(kit.Px(1)), Padding: kit.EdgesInline(kit.Px(10))}
	pane := box(kit.Style{Height: kit.Px(30), Overflow: kit.OverflowHidden, Padding: kit.Edges{Top: kit.Px(20)}}, f)
	page := box(kit.Style{}, pane)
	w, loop := open(t, page, kit.Size{W: 400, H: 100}, env100)
	nf := loop.Host.Fields[0]
	if nf.Shown() {
		t.Errorf("a field 10 of 28 px in view shows the host's field at %v", nf.Bounds)
	}
	if got := standInTexts(loop.Canvas.Ops); len(got) != 1 || got[0] != "The scanner's program" {
		t.Errorf("the kit drew %q, want the placeholder", got)
	}

	if !w.Focus(f) {
		t.Fatal("the field took no focus")
	}
	loop.Step()
	if !nf.Shown() {
		t.Error("typed in, the host's field does not show")
	}
	w.Blur()
	loop.Step()
	if nf.Shown() {
		t.Error("left, the host's field still shows past the box's edge")
	}

	pane.Style.Height = kit.Px(60)
	pane.Relayout()
	cut := len(loop.Canvas.Ops)
	loop.Step()
	if !nf.Shown() || len(standInTexts(loop.Canvas.Ops[cut:])) != 0 {
		t.Errorf("wholly in view: shown %v, the kit drew %q", nf.Shown(), standInTexts(loop.Canvas.Ops[cut:]))
	}
}
