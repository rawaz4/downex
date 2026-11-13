package add

import (
	"testing"

	"downex/ui/core/format/formattest"
	"downex/ui/kit"
)

// [mac webkit] TestVideoCardAddressMacKeys: in today's Mac window the Page field is WebKit's
// read-only <input>, which takes macOS's keys: ⌘A selects all, ⌘C copies, ⌥←/⌥→ move by
// word, ⌘←/⌘→ go to the line's ends, Control+A/Control+E too (Emacs keys); Ctrl+C is not
// copy there.
func TestVideoCardAddressMacKeys(t *testing.T) {
	r := setupConfirm(t, confirmSetup{d: siteConfirm(nil)})
	h, page := r.h, r.c.Video.Address
	value := formattest.Page
	env := h.Window.Environment()
	env.WebKit = true
	h.Loop.Send(kit.EnvironmentEvent{Env: env})
	h.Settle()

	h.Window.Focus(page)
	h.Settle()
	h.Key(kit.KeyArrowLeft, kit.ModMeta)
	lo, hi := page.Selection()
	eq(t, "⌘←", [2]int{lo, hi}, [2]int{0, 0})
	h.Key(kit.KeyArrowRight, kit.ModAlt|kit.ModShift)
	eq(t, "⌥⇧→", page.SelectedText(), "https")
	h.Key(kit.KeyArrowRight, kit.ModMeta|kit.ModShift)
	eq(t, "⌘⇧→", page.SelectedText(), value)
	h.Host.Clipboard = ""
	h.Key(kit.KeyC, kit.ModCtrl)
	eq(t, "Ctrl+C copies nothing on a Mac", h.Host.Clipboard, "")
	h.Key(kit.KeyArrowRight, 0)
	h.Key(kit.KeyA, kit.ModCtrl)
	lo, hi = page.Selection()
	eq(t, "Control+A goes to the start", [2]int{lo, hi}, [2]int{0, 0})
	h.Key(kit.KeyE, kit.ModCtrl)
	lo, hi = page.Selection()
	eq(t, "Control+E goes to the end", [2]int{lo, hi}, [2]int{len(value), len(value)})
	h.Key(kit.KeyArrowLeft, kit.ModAlt)
	if lo, _ := page.Selection(); lo >= len(value) {
		t.Errorf("⌥← stayed at %d", lo)
	}
	if !h.Key(kit.KeyA, kit.ModMeta) {
		t.Error("⌘A not taken")
	}
	eq(t, "⌘A", page.SelectedText(), value)
	if !h.Key(kit.KeyC, kit.ModMeta) {
		t.Error("⌘C not taken")
	}
	eq(t, "⌘C", h.Host.Clipboard, value)
}

// [mac webkit] The Edit menu's Copy and Select All chosen with the mouse act on the focused
// Page field (WebKit's editing commands on a read-only <input>); a widget without
// EditCommand never hears them (the macOS host checks for it).
func TestVideoCardAddressEditCommands(t *testing.T) {
	r := setupConfirm(t, confirmSetup{d: siteConfirm(nil)})
	h, page := r.h, r.c.Video.Address
	value := formattest.Page
	env := h.Window.Environment()
	env.WebKit = true
	h.Loop.Send(kit.EnvironmentEvent{Env: env})
	h.Settle()
	h.Window.Focus(page)
	h.Settle()
	page.Select(0, 0)
	ec, ok := h.Window.Focused().Widget().(interface{ EditCommand(string) bool })
	if !ok {
		t.Fatal("the focused Page field takes no edit commands")
	}
	if !ec.EditCommand("selectAll") {
		t.Error("selectAll not taken")
	}
	eq(t, "Select All", page.SelectedText(), value)
	h.Host.Clipboard = ""
	if !ec.EditCommand("copy") {
		t.Error("copy not taken")
	}
	eq(t, "Copy", h.Host.Clipboard, value)
	if ec.EditCommand("paste") {
		t.Error("paste taken by a read-only field")
	}
}
