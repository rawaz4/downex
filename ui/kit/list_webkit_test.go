package kit_test

import (
	"testing"

	"downex/ui/kit"
)

// TestListMenuOnPressInWebKit: today's Mac window is WebKit, which fires contextmenu on the
// press (a right press, or a left press with Control: macOS's secondary click), so the row
// menu shows while the button is still down and the release does nothing more (no toggle).
// Chromium's rule (the menu on release, Ctrl+click toggles) stays where WebKit is off.
func TestListMenuOnPressInWebKit(t *testing.T) {
	env := env100
	env.WebKit = true
	f := newGridEnv(t, 100, kit.Size{W: 800, H: 600}, env)
	rowY := func(i int) kit.DIP { return 30 + kit.DIP(i)*46 + 20 }
	down := func(i int, b kit.Buttons, mods kit.Modifiers) {
		f.loop.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: kit.Pt(200, rowY(i)), Button: b, Buttons: b, Mods: mods, Clicks: 1})
		f.loop.Settle(10)
	}
	up := func(i int, b kit.Buttons, mods kit.Modifiers) {
		f.loop.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: kit.Pt(200, rowY(i)), Button: b, Mods: mods})
		f.loop.Settle(10)
	}
	down(3, kit.ButtonRight, 0)
	if len(f.menus) != 1 || f.menus[0] != f.m.Key(3) || !f.sel.Selected(f.m.Key(3)) {
		t.Fatalf("right press: menus %v, selection %v", f.menus, f.sel.ids)
	}
	up(3, kit.ButtonRight, 0)
	if len(f.menus) != 1 {
		t.Errorf("the release opened the menu again: %v", f.menus)
	}
	// Control-click: the menu on the press, and no Ctrl toggle on the release.
	down(5, kit.ButtonLeft, kit.ModCtrl)
	if len(f.menus) != 2 || f.menus[1] != f.m.Key(5) {
		t.Fatalf("control press: menus %v", f.menus)
	}
	up(5, kit.ButtonLeft, kit.ModCtrl)
	if len(f.sel.ids) != 1 || !f.sel.Selected(f.m.Key(5)) {
		t.Errorf("control click: selection %v, want only row 5", f.sel.ids)
	}
	// Command+click still toggles.
	down(7, kit.ButtonLeft, kit.ModMeta)
	up(7, kit.ButtonLeft, kit.ModMeta)
	if len(f.menus) != 2 || len(f.sel.ids) != 2 {
		t.Errorf("command click: menus %v, selection %v", f.menus, f.sel.ids)
	}

	// Without WebKit: nothing on the press, the menu on the release, Ctrl+click toggles.
	g := newGrid(t, 100, kit.Size{W: 800, H: 600})
	g.loop.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: kit.Pt(200, rowY(3)), Button: kit.ButtonRight, Buttons: kit.ButtonRight, Clicks: 1})
	if len(g.menus) != 0 {
		t.Errorf("Chromium rule: the press opened the menu")
	}
}
