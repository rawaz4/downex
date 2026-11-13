package kit_test

import (
	"testing"

	"downex/ui/kit"
)

// button is a focusable box Tab reaches.
func button(name string) *kit.Pressable {
	b := kit.NewPressable(nil)
	b.Style = kit.Style{Width: kit.Px(80), Height: kit.Px(30)}
	b.Semantics.Name = name
	return b
}

func tab(t *testing.T, w *kit.Window, from *kit.Pressable, shift bool) *kit.Element {
	t.Helper()
	if from != nil {
		w.Focus(from)
	}
	var mods kit.Modifiers
	if shift {
		mods = kit.ModShift
	}
	w.Event(kit.KeyEvent{Action: kit.KeyDown, Key: kit.KeyTab, Mods: mods})
	return w.Focused()
}

// TestTabOrder checks Tab and Shift+Tab: tree order, wrapping, and what Tab skips.
func TestTabOrder(t *testing.T) {
	a, b, c, d, e := button("a"), button("b"), button("c"), button("d"), button("e")
	c.Disabled = true                                   // HTML disabled: skipped
	d.Disabled, d.DisabledFocusable = true, true        // aria-disabled: still reached
	hidden := box(kit.Style{Hidden: true}, button("x")) // display: none
	inert := box(kit.Style{}, button("y"))
	inert.Inert = true
	click := button("click only")
	click.Focus = kit.FocusClick
	page := box(kit.Style{Display: kit.DisplayFlex, Wrap: true}, a, hidden, b, c, inert, click, d, e)
	w, _ := open(t, page, kit.Size{W: 800, H: 400}, env100)

	got := []string{}
	for _, el := range w.TabOrder() {
		got = append(got, el.Semantics.Name)
	}
	if want := "a b d e"; join(got) != want {
		t.Fatalf("tab order %q, want %q", join(got), want)
	}
	if f := tab(t, w, nil, false); f != &a.Element {
		t.Errorf("Tab from nothing focuses %v, want a", name(f))
	}
	if f := tab(t, w, b, false); f != &d.Element {
		t.Errorf("Tab from b focuses %v, want d (c is disabled, the inert box is skipped)", name(f))
	}
	if f := tab(t, w, e, false); f != &a.Element {
		t.Errorf("Tab from the last wraps to %v", name(f))
	}
	if f := tab(t, w, a, true); f != &e.Element {
		t.Errorf("Shift+Tab from the first wraps to %v", name(f))
	}
	// From an element Tab does not reach (focused by a click), Tab goes on in tree order.
	w.Focus(click)
	if f := tab(t, w, nil, false); f != &d.Element {
		t.Errorf("Tab from the click-only element focuses %v, want d", name(f))
	}
	w.Focus(click)
	if f := tab(t, w, nil, true); f != &b.Element {
		t.Errorf("Shift+Tab from the click-only element focuses %v, want b", name(f))
	}
	if w.Focus(c) {
		t.Error("a disabled element took the focus")
	}
}

func join(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += " "
		}
		out += x
	}
	return out
}

func name(e *kit.Element) string {
	if e == nil {
		return "nothing"
	}
	return e.Semantics.Name
}

// TestFocusVisible checks that the ring shows after keyboard use only, 2 px outside the
// box, in the focus colour.
func TestFocusVisible(t *testing.T) {
	a, b := button("a"), button("b")
	page := box(kit.Style{Display: kit.DisplayFlex, Padding: kit.EdgesAll(kit.Px(20)), ColumnGap: kit.Px(20)}, a, b)
	w, loop := open(t, page, kit.Size{W: 400, H: 100}, env100)
	ring := func() []kit.Rect {
		loop.Canvas.Reset()
		w.InvalidateRect(kit.R(0, 0, 400, 100))
		loop.Step()
		var out []kit.Rect
		for _, op := range loop.Canvas.Ops {
			if op.Kind == "StrokeRoundedRect" && op.Color == testPalette.Focus {
				out = append(out, op.Rect)
			}
		}
		return out
	}
	loop.Send(kit.KeyEvent{Action: kit.KeyDown, Key: kit.KeyTab})
	if !w.RingShown() || w.Focused() != &a.Element {
		t.Fatal("Tab did not focus a with a visible ring")
	}
	// The outline's stroke: 2 px wide, 2 px outside the 80 x 30 box at (20, 20).
	if got := ring(); len(got) != 1 || got[0] != kit.R(17, 17, 103, 53) {
		t.Errorf("ring strokes %v, want one centred on (17,17)-(103,53)", got)
	}
	loop.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: kit.Pt(130, 30), Button: kit.ButtonLeft, Buttons: kit.ButtonLeft, Clicks: 1})
	loop.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: kit.Pt(130, 30), Button: kit.ButtonLeft})
	if w.Focused() != &b.Element || w.RingShown() {
		t.Error("a click focuses b without a ring")
	}
	if got := ring(); len(got) != 0 {
		t.Errorf("rings after a click: %v", got)
	}
	loop.Send(kit.FocusEvent{In: false})
	loop.Send(kit.KeyEvent{Action: kit.KeyDown, Key: kit.KeyArrowRight})
	if got := ring(); len(got) != 0 {
		t.Error("a window without the keyboard focus shows a ring")
	}
	loop.Send(kit.FocusEvent{In: true})
	if got := ring(); len(got) != 1 {
		t.Errorf("rings after the window came back: %v", got)
	}
}

// trapFixture is focus-trap.test.ts's setup: an opener, a grid and a dialog with Cancel
// and OK, the dialog in a layer above.
type trapFixture struct {
	w                    *kit.Window
	opener, grid, cancel *kit.Pressable
	ok                   *kit.Pressable
	dialog               *kit.Element
}

func newTrapFixture(t *testing.T) *trapFixture {
	f := &trapFixture{opener: button("Remove"), grid: button("grid"), cancel: button("Cancel"), ok: button("OK")}
	page := box(kit.Style{Display: kit.DisplayFlex}, f.opener, f.grid)
	f.dialog = box(kit.Style{Display: kit.DisplayFlex, AlignSelf: kit.AlignStart}, f.cancel, f.ok)
	f.dialog.Semantics.Role = kit.RoleDialog
	f.w, _ = open(t, page, kit.Size{W: 800, H: 400}, env100)
	f.w.PushLayer(box(kit.Style{}, f.dialog))
	f.w.Focus(f.opener)
	return f
}

// inCall runs f as one call into the window (a Post), as the host's loop would.
func inCall(w *kit.Window, f func()) {
	_ = w.Post(f)
	if h, ok := w.Host().(interface{ RunPosted() int }); ok {
		h.RunPosted()
	}
}

// TestTrapRestoresFocus ports focus-trap.test.ts's "returning focus on close" cases.
func TestTrapRestoresFocus(t *testing.T) {
	t.Run("to the opener when it can still take it", func(t *testing.T) {
		f := newTrapFixture(t)
		trap := f.w.Trap(f.dialog, kit.TrapOptions{Fallback: func() kit.Widget { return f.grid }})
		if f.w.Focused() != &f.cancel.Element {
			t.Fatalf("the trap focused %v, want its first control", name(f.w.Focused()))
		}
		inCall(f.w, trap.Release)
		if f.w.Focused() != &f.opener.Element {
			t.Errorf("focus went to %v, want the opener", name(f.w.Focused()))
		}
	})
	for _, c := range []struct {
		what  string
		spoil func(f *trapFixture)
	}{
		{"disabled", func(f *trapFixture) { f.opener.Disabled = true }},
		{"inert", func(f *trapFixture) { f.opener.Inert = true }},
		{"hidden", func(f *trapFixture) { f.opener.SetHidden(true) }},
		{"gone", func(f *trapFixture) { f.opener.Parent().Remove(f.opener) }},
		{"hidden from assistive technology", func(f *trapFixture) { f.opener.Parent().Semantics.Hidden = true }},
	} {
		t.Run("falls back when the opener is "+c.what, func(t *testing.T) {
			f := newTrapFixture(t)
			trap := f.w.Trap(f.dialog, kit.TrapOptions{Fallback: func() kit.Widget { return f.grid }})
			c.spoil(f)
			inCall(f.w, trap.Release)
			if f.w.Focused() != &f.grid.Element {
				t.Errorf("focus went to %v, want the fallback", name(f.w.Focused()))
			}
		})
	}
	t.Run("decides after the call, not when the dialog starts closing", func(t *testing.T) {
		f := newTrapFixture(t)
		trap := f.w.Trap(f.dialog, kit.TrapOptions{Fallback: func() kit.Widget { return f.grid }})
		inCall(f.w, func() {
			trap.Release()
			f.opener.Disabled = true // the selection emptied in the same update
		})
		if f.w.Focused() != &f.grid.Element {
			t.Errorf("focus went to %v, want the fallback", name(f.w.Focused()))
		}
	})
	t.Run("autofocus", func(t *testing.T) {
		f := newTrapFixture(t)
		f.ok.Autofocus = true
		f.w.Trap(f.dialog, kit.TrapOptions{})
		if f.w.Focused() != &f.ok.Element {
			t.Errorf("focus went to %v, want the Autofocus control", name(f.w.Focused()))
		}
	})
	t.Run("escape", func(t *testing.T) {
		f := newTrapFixture(t)
		closed := 0
		f.w.Trap(f.dialog, kit.TrapOptions{OnEscape: func() { closed++ }})
		if !f.w.Event(kit.KeyEvent{Action: kit.KeyDown, Key: kit.KeyEscape}) || closed != 1 {
			t.Errorf("Escape: used and closed %d times", closed)
		}
	})
}

// TestTrapExemptRegions ports the exempt-region cases of focus-trap.test.ts.
func TestTrapExemptRegions(t *testing.T) {
	t.Run("an exempt region keeps the focus and joins the Tab cycle", func(t *testing.T) {
		stop, outside, cancel, ok := button("Stop it"), button("outside"), button("Cancel"), button("OK")
		banner := box(kit.Style{}, stop)
		banner.Exempt = kit.ExemptKeep
		page := box(kit.Style{Display: kit.DisplayFlex}, outside, banner)
		dialog := box(kit.Style{Display: kit.DisplayFlex}, cancel, ok)
		w, _ := open(t, page, kit.Size{W: 800, H: 400}, env100)
		w.PushLayer(box(kit.Style{}, dialog))
		trap := w.Trap(dialog, kit.TrapOptions{})
		if w.Focused() != &cancel.Element {
			t.Fatalf("trap focused %v", name(w.Focused()))
		}
		w.Focus(stop)
		if w.Focused() != &stop.Element {
			t.Error("the exempt region did not keep the focus")
		}
		w.Focus(outside)
		if w.Focused() != &cancel.Element {
			t.Errorf("focus outside went to %v, want back into the dialog", name(w.Focused()))
		}
		if f := tab(t, w, ok, false); f != &stop.Element {
			t.Errorf("Tab from OK: %v, want Stop it", name(f))
		}
		if f := tab(t, w, stop, false); f != &cancel.Element {
			t.Errorf("Tab from Stop it: %v, want Cancel", name(f))
		}
		if f := tab(t, w, cancel, true); f != &stop.Element {
			t.Errorf("Shift+Tab from Cancel: %v, want Stop it", name(f))
		}
		if f := tab(t, w, stop, true); f != &ok.Element {
			t.Errorf("Shift+Tab from Stop it: %v, want OK", name(f))
		}
		trap.Release()
	})
	t.Run("a modal that opened by itself leaves the focus in an exempt region", func(t *testing.T) {
		stop, ok := button("Stop it"), button("OK")
		banner := box(kit.Style{}, stop)
		banner.Exempt = kit.ExemptKeep
		dialog := box(kit.Style{}, ok)
		dialog.Focus = kit.FocusClick
		w, _ := open(t, box(kit.Style{}, banner), kit.Size{W: 800, H: 400}, env100)
		w.PushLayer(box(kit.Style{}, dialog))
		w.Focus(stop)
		self := w.Trap(dialog, kit.TrapOptions{FocusSelf: true})
		if w.Focused() != &stop.Element {
			t.Errorf("focus %v, want it left on Stop it", name(w.Focused()))
		}
		self.Release()
		w.Trap(dialog, kit.TrapOptions{})
		if w.Focused() != &ok.Element {
			t.Errorf("a modal the user opened takes the focus: %v", name(w.Focused()))
		}
	})
	t.Run("a Tab-only region joins the cycle without keeping the focus", func(t *testing.T) {
		minimise, ok := button("Minimise"), button("OK")
		controls := box(kit.Style{}, minimise)
		controls.Exempt = kit.ExemptTab
		dialog := box(kit.Style{}, ok)
		dialog.Focus = kit.FocusClick
		w, _ := open(t, box(kit.Style{}, controls), kit.Size{W: 800, H: 400}, env100)
		w.PushLayer(box(kit.Style{}, dialog))
		w.Focus(minimise)
		w.Trap(dialog, kit.TrapOptions{FocusSelf: true})
		if w.Focused() != dialog {
			t.Errorf("focus %v, want the dialog itself", name(w.Focused()))
		}
		if f := tab(t, w, ok, false); f != &minimise.Element {
			t.Errorf("Tab from OK: %v, want the window controls", name(f))
		}
	})
}

// TestCanRestoreFocus ports "canRestoreFocus checks ancestors too".
func TestCanRestoreFocus(t *testing.T) {
	b := button("b")
	wrap := box(kit.Style{}, b)
	w, _ := open(t, box(kit.Style{}, wrap), kit.Size{W: 200, H: 200}, env100)
	if !w.CanRestoreFocus(&b.Element) {
		t.Fatal("a shown button cannot take the focus back")
	}
	wrap.Semantics.Hidden = true
	if w.CanRestoreFocus(&b.Element) {
		t.Error("inside aria-hidden")
	}
	wrap.Semantics.Hidden = false
	wrap.SetHidden(true)
	if w.CanRestoreFocus(&b.Element) {
		t.Error("inside display: none")
	}
	if w.CanRestoreFocus(nil) {
		t.Error("nil")
	}
}

// TestHiddenFocusFallsBack checks HTML's focus fixup: hiding the focused element leaves
// the focus on nothing.
func TestHiddenFocusFallsBack(t *testing.T) {
	a := button("a")
	w, _ := open(t, box(kit.Style{}, a), kit.Size{W: 200, H: 200}, env100)
	w.Focus(a)
	a.SetHidden(true)
	if w.Focused() != nil {
		t.Errorf("focus stayed on a hidden element: %v", name(w.Focused()))
	}
}

// A focused element that becomes disabled loses the focus to the window's body at the next
// frame, as HTML's focus fixup does (a button disabled while busy); one that stays focusable
// while unavailable (aria-disabled: DisabledFocusable) keeps it.
func TestDisabledFocusedElementLosesTheFocus(t *testing.T) {
	a, b := button("a"), button("b")
	page := box(kit.Style{Display: kit.DisplayFlex}, a, b)
	w, l := open(t, page, kit.Size{W: 400, H: 100}, env100)
	w.Focus(a)
	a.Disabled = true
	a.Invalidate()
	l.Settle(10)
	if f := w.Focused(); f != nil {
		t.Errorf("a disabled element kept the focus: %v", name(f))
	}
	w.Focus(b)
	b.Disabled, b.DisabledFocusable = true, true
	b.Invalidate()
	l.Settle(10)
	if f := w.Focused(); f != &b.Element {
		t.Errorf("an aria-disabled element lost the focus to %v", name(f))
	}
}
