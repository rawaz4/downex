package add

import (
	"testing"

	"downex/ui/core/format/formattest"
	"downex/ui/kit"
	"downex/ui/views/viewstest"
)

// TestVideoCardAddress checks the card's page address against today's <input class="address
// mono" readonly>: a read-only text field named "Page" with the address as its value, the
// whole address as its title, an ellipsis only while it is not focused, Tab selecting it all,
// the selection keys, Ctrl+C copying the selection, a press placing the caret, and Enter
// submitting the form (implicit submission, which readonly keeps).
func TestVideoCardAddress(t *testing.T) {
	r := setupConfirm(t, confirmSetup{d: siteConfirm(nil)})
	h, page := r.h, r.c.Video.Address
	value := formattest.Page

	t.Run("is a read-only text field named Page with the address as its value", func(t *testing.T) {
		n, ok := h.Window.Semantics().Node(page.NodeID())
		if !ok {
			t.Fatal("no semantic node")
		}
		eq(t, "role", n.Role, kit.RoleTextField)
		eq(t, "name", n.Name, "Page")
		eq(t, "value", n.Value, value)
		if n.States&kit.NodeReadOnly == 0 {
			t.Error("not read-only")
		}
		eq(t, "tooltip", page.TooltipText(), value)
		eq(t, "cursor", page.Cursor, kit.CursorText)
		eq(t, "ellipsis in the middle while unfocused (the video id stays)", page.Options().Truncate, kit.TruncateMiddle)
		eq(t, "dir", page.Options().Direction, kit.DirLTR)
	})

	t.Run("Tab selects the whole address, and Ctrl+C copies it", func(t *testing.T) {
		h.Window.SetFocusVisible(true)
		h.Window.Focus(page)
		h.Settle()
		lo, hi := page.Selection()
		eq(t, "selection", [2]int{lo, hi}, [2]int{0, len(value)})
		eq(t, "whole line while focused", page.Options().Truncate, kit.TruncateNone)
		h.Host.Clipboard = ""
		if !h.Key(kit.KeyC, kit.ModCtrl) {
			t.Error("Ctrl+C not taken")
		}
		eq(t, "clipboard", h.Host.Clipboard, value)
	})

	t.Run("moves and extends the selection with the keyboard", func(t *testing.T) {
		h.Window.Focus(page)
		h.Key(kit.KeyHome, 0)
		lo, hi := page.Selection()
		eq(t, "Home", [2]int{lo, hi}, [2]int{0, 0})
		h.Key(kit.KeyArrowRight, kit.ModCtrl|kit.ModShift)
		eq(t, "Ctrl+Shift+Right", page.SelectedText(), "https")
		h.Key(kit.KeyArrowRight, kit.ModShift)
		eq(t, "Shift+Right", page.SelectedText(), "https:")
		h.Key(kit.KeyArrowLeft, 0)
		lo, hi = page.Selection()
		eq(t, "Left collapses to the start", [2]int{lo, hi}, [2]int{0, 0})
		h.Key(kit.KeyEnd, kit.ModShift)
		eq(t, "Shift+End", page.SelectedText(), value)
		h.Key(kit.KeyHome, 0)
		h.Key(kit.KeyA, kit.ModCtrl)
		eq(t, "Ctrl+A", page.SelectedText(), value)
		h.Host.Clipboard = "kept"
		h.Key(kit.KeyArrowRight, 0)
		h.Key(kit.KeyInsert, kit.ModCtrl)
		eq(t, "Ctrl+Insert with nothing selected", h.Host.Clipboard, "kept")
	})

	t.Run("a press puts the caret without selecting, and the focus leaving hides the selection", func(t *testing.T) {
		h.Window.Blur()
		h.Settle()
		eq(t, "ellipsis again", page.Options().Truncate, kit.TruncateMiddle)
		h.Click(page.Base())
		if !page.Focused() {
			t.Fatal("not focused by a press")
		}
		lo, hi := page.Selection()
		if lo != hi {
			t.Errorf("a press selected %d..%d", lo, hi)
		}
		h.Window.Blur()
		h.Settle()
		lo, hi = page.Selection()
		eq(t, "no selection once blurred", [2]int{lo, hi}, [2]int{0, 0})
	})

	t.Run("Enter in it submits the form", func(t *testing.T) {
		h.Window.Focus(page)
		h.Settle()
		if !h.Key(kit.KeyEnter, 0) {
			t.Fatal("Enter not taken")
		}
		r.waitConfirm(t)
	})
}

func TestAddressWords(t *testing.T) {
	const s = "https://www.youtube.com/watch?v=Xb7"
	for _, c := range []struct {
		name   string
		at     int
		lo, hi int
	}{
		{"a word", 13, 12, 19},   // youtube
		{"a separator", 5, 5, 6}, // ':'
		{"the end", len(s), 32, 35},
	} {
		t.Run(c.name, func(t *testing.T) {
			lo, hi := wordAt(s, c.at)
			eq(t, "word", [2]int{lo, hi}, [2]int{c.lo, c.hi})
		})
	}
	eq(t, "next word", nextWord(s, 0), 5)
	eq(t, "next word from a separator", nextWord(s, 5), 11)
	eq(t, "previous word", prevWord(s, 15), 12)
	eq(t, "previous rune of a multibyte one", prevRune("a⟨", len("a⟨")), 1)
}

// TestVideoCardAddressSteady repaints the address focused with a selection, scrolled, and
// under the pointer (with DirectWrite layouts): a steady repaint allocates nothing.
func TestVideoCardAddressSteady(t *testing.T) {
	h := viewstest.New(t, viewstest.Options{Real: true, Fixture: "confirm-video-6-longurl"})
	c := confirmState(t, h, "confirm-video-6-longurl")
	page := c.Video.Address
	h.Window.SetFocusVisible(true)
	h.Window.Focus(page)
	h.Settle()
	h.Key(kit.KeyEnd, kit.ModShift) // extends nothing: Tab selected it all; End then moves
	h.Key(kit.KeyArrowLeft, kit.ModShift|kit.ModCtrl)
	if page.SelectedText() == "" {
		t.Fatal("nothing selected")
	}
	if page.scroll <= 0 {
		t.Errorf("a long address focused at its end is not scrolled (%v)", page.scroll)
	}
	h.Loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: page.WindowBounds().Center()})
	h.Settle()
	if !page.Hovered() {
		t.Error("not hovered")
	}
	full := kit.RectAt(kit.Point{}, h.Window.Size())
	var cv nopCanvas
	h.Window.Paint(cv, full)
	if n := allocs(50, func() { h.Window.Paint(cv, full) }); n != 0 {
		t.Errorf("a repaint allocates %.1f times", n)
	}
}
