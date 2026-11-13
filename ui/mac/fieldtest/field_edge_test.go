//go:build darwin && cgo

package fieldtest

// [fields] The fields' edge cases: text that is not valid UTF-8, selections inside a
// character, focus on a hidden field, the Edit keys of a password field without a menu,
// pointer moves heard once, the key ups of a composition and the root view's methods.

import (
	"strings"
	"testing"
	"unicode/utf8"

	"downex/ui/kit"
)

// movesAt counts the root's pointer moves at (x, y): the user's own mouse may move over the
// window during a test, elsewhere. A posted move can come back a fraction of a point off
// (AppKit's screen conversion of a window on a fractional origin).
func (r *root) movesAt(x, y float32) int {
	n := 0
	for _, p := range r.moveAt {
		if dx, dy := p.X-x, p.Y-y; dx*dx+dy*dy < 1 {
			n++
		}
	}
	return n
}

func TestFieldInvalidText(t *testing.T) {
	onMain(func() {
		r := &root{}
		w, d := open(t, r, kit.Size{W: 400, H: 200})
		if d == nil {
			return
		}
		defer w.Close()
		rec := &record{}
		nf, _ := w.NewTextField(opts(), rec.callbacks())
		nf.SetBounds(kit.R(10, 10, 300, 30))
		f := nf.(kit.FieldControl)
		// Each bad byte shows as U+FFFD, as Windows' field and a WebKit input show it.
		nf.SetText("bad\xffname.bin")
		if got := nf.Text(); got != "bad�name.bin" {
			t.Errorf("SetText of invalid UTF-8: %q", got)
		}
		f.SetSelection(0, 0)
		f.ReplaceSelection("\xfe") // unfocused
		if got := nf.Text(); got != "�bad�name.bin" {
			t.Errorf("ReplaceSelection of invalid UTF-8 (unfocused): %q", got)
		}
		nf.Focus()
		Pump(0.05)
		f.ReplaceSelection("\xc3")
		if got := nf.Text(); got != "��bad�name.bin" { // at the kept caret, after the first
			t.Errorf("ReplaceSelection of invalid UTF-8 (focused): %q", got)
		}
		o := opts()
		o.Placeholder, o.Name = "File \xffname", "\xfe"
		nf.SetOptions(o)
		nf.Blur()
		Pump(0.02)

		mf, err := w.NewField(opts(), kit.FieldOptions{Kind: kit.FieldMultiline}, kit.TextFieldCallbacks{})
		if err != nil {
			t.Errorf("multi-line: %v", err)
			return
		}
		mf.SetBounds(kit.R(10, 60, 300, 140))
		mf.SetText("a\xff\r\nb")
		if got := mf.Text(); got != "a�\nb" {
			t.Errorf("multi-line SetText of invalid UTF-8: %q", got)
		}
		mf.Release()
	})
}

// TestFieldSelectionInsideACharacter: a selection edge inside a surrogate pair goes where the
// field editor puts it, whether or not the field is edited, and an edit never splits the pair.
func TestFieldSelectionInsideACharacter(t *testing.T) {
	onMain(func() {
		r := &root{}
		w, d := open(t, r, kit.Size{W: 400, H: 100})
		if d == nil {
			return
		}
		defer w.Close()
		nf, _ := w.NewTextField(opts(), kit.TextFieldCallbacks{})
		nf.SetBounds(kit.R(10, 10, 300, 30))
		f := nf.(kit.FieldControl)
		const text = "a😀b" // the pair is UTF-16 units 1 and 2
		for _, c := range [][2]int{{2, 2}, {2, 3}, {0, 2}, {2, 4}, {1, 3}} {
			nf.SetText(text)
			nf.Focus()
			Pump(0.02)
			f.SetSelection(c[0], c[1])
			fa, fb := f.Selection()
			nf.Blur()
			Pump(0.02)
			nf.SetText(text)
			f.SetSelection(c[0], c[1])
			ua, ub := f.Selection()
			if ua != fa || ub != fb {
				t.Errorf("SetSelection(%d, %d): %d-%d unfocused, %d-%d in the field editor", c[0], c[1], ua, ub, fa, fb)
			}
			if ua == 2 || ub == 2 {
				t.Errorf("SetSelection(%d, %d): an edge inside the pair (%d-%d)", c[0], c[1], ua, ub)
			}
			f.ReplaceSelection("x")
			if got := nf.Text(); !utf8.ValidString(got) || !strings.Contains(got, "😀") && !(ua <= 1 && ub >= 3) {
				t.Errorf("SetSelection(%d, %d) then ReplaceSelection: %q", c[0], c[1], got)
			}
		}
	})
}

func TestFieldFocusWhileHidden(t *testing.T) {
	onMain(func() {
		r := &root{}
		w, d := open(t, r, kit.Size{W: 400, H: 200})
		if d == nil {
			return
		}
		defer w.Close()
		// A multi-line field never placed takes the focus out of sight.
		rec := &record{}
		mf, _ := w.NewField(opts(), kit.FieldOptions{Kind: kit.FieldMultiline}, rec.callbacks())
		mf.Focus()
		Pump(0.05)
		if views := d.FieldViews(); strings.Contains(views, "shows=1") {
			t.Errorf("Focus showed a field the kit never placed: %s", views)
		}
		if len(rec.focus) != 1 || !rec.focus[0] {
			t.Errorf("Focused %v", rec.focus)
		}
		mf.Release()
		// A field the kit hid (an empty rectangle) stays hidden when focused, and shows at its
		// place once the kit places it again.
		nf, _ := w.NewTextField(opts(), kit.TextFieldCallbacks{})
		place := kit.R(10, 120, 250, 140)
		nf.SetBounds(place)
		nf.SetBounds(kit.Rect{})
		nf.Focus()
		Pump(0.05)
		if views := d.FieldViews(); strings.Contains(views, "shows=1") {
			t.Errorf("Focus showed a hidden field: %s", views)
		}
		if !strings.Contains(d.Responder(), "Editor") {
			t.Errorf("the hidden field did not take the focus: %s", d.Responder())
		}
		nf.SetBounds(place)
		Pump(0.02)
		if views := d.FieldViews(); !strings.Contains(views, "shows=1 frame={{10, 120}, {240, 20}}") {
			t.Errorf("placed again: %s", views)
		}
	})
}

// TestFieldPasswordEditWithoutMenu: without an Edit menu the keys do what the menu's items
// would, validated: a password field is never cut or copied, but takes a paste.
func TestFieldPasswordEditWithoutMenu(t *testing.T) {
	onMain(func() {
		NoMenu()
		defer EditMenu()
		r := &root{}
		w, d := open(t, r, kit.Size{W: 300, H: 100})
		if d == nil {
			return
		}
		defer w.Close()
		nf, _ := w.NewField(opts(), kit.FieldOptions{Kind: kit.FieldPassword}, kit.TextFieldCallbacks{})
		nf.SetBounds(kit.R(10, 10, 250, 30))
		nf.Focus()
		Pump(0.05)
		d.Type("pw")
		d.Press(0x00, "a", "a", 8)
		SetBoardString("keep")
		count := BoardCount()
		d.Press(0x07, "x", "x", 8)
		if got := nf.Text(); got != "pw" {
			t.Errorf("Command+X cut a password: %q", got)
		}
		d.Press(0x08, "c", "c", 8)
		if BoardCount() != count || BoardString() != "keep" {
			t.Errorf("Command+C or X wrote the pasteboard: %q", BoardString())
		}
		d.Press(0x00, "a", "a", 8)
		d.Press(0x09, "v", "v", 8)
		if got := nf.Text(); got != "keep" {
			t.Errorf("Command+V into a password: %q", got)
		}
	})
}

// TestFieldMovesOnce: a move AppKit delivers both to the root view (its tracking area) and
// to the focused field's text view (the first responder) reaches the root once.
func TestFieldMovesOnce(t *testing.T) {
	onMain(func() {
		r := &root{}
		w, d := open(t, r, kit.Size{W: 300, H: 100})
		if d == nil {
			return
		}
		defer w.Close()
		d.MoveTwice(40, 60) // the root is the first responder
		if n := r.movesAt(40, 60); n != 1 {
			t.Errorf("the root heard %d moves, want 1", n)
		}
		nf, _ := w.NewTextField(opts(), kit.TextFieldCallbacks{})
		nf.SetBounds(kit.R(10, 10, 250, 30))
		nf.Focus()
		Pump(0.05)
		d.MoveTwice(60, 20)
		d.MoveTwice(200, 80)
		if n, m := r.movesAt(60, 20), r.movesAt(200, 80); n != 1 || m != 1 {
			t.Errorf("with a field focused the root heard %d and %d moves, want 1 each", n, m)
		}
	})
}

// TestFieldCompositionKeys: while an input method composes, its key downs never reach the
// root and the key ups do, in a field and in the root's own text input (as WebKit sends a
// page keyup after a composing keydown it holds back).
func TestFieldCompositionKeys(t *testing.T) {
	onMain(func() {
		r := &root{}
		w, d := open(t, r, kit.Size{W: 300, H: 100})
		if d == nil {
			return
		}
		defer w.Close()
		rec := &record{}
		nf, _ := w.NewField(opts(), kit.FieldOptions{Kind: kit.FieldText, MaxLength: 3}, rec.callbacks())
		nf.SetBounds(kit.R(10, 10, 250, 30))
		nf.Focus()
		Pump(0.05)
		d.Type("ab")
		n := len(r.keys)
		if !d.SetMarked("かんじ") {
			t.Errorf("no composition in the field")
		}
		d.Key(false, 0x00, "a", "a", 0)
		if got := r.keys[n:]; len(got) != 1 || got[0].Action != kit.KeyUp {
			t.Errorf("a key up during a composition: the root saw %v", got)
		}
		// The max length holds for the committed text, as WebKit cuts a composition's commit.
		d.Insert("漢字")
		if got := nf.Text(); got != "ab漢" {
			t.Errorf("max length 3 after the commit: %q", got)
		}
		if c := rec.changed; len(c) == 0 || c[len(c)-1] != "ab漢" {
			t.Errorf("Changed %q", c)
		}
		if a, b := nf.(kit.FieldControl).Selection(); a != 3 || b != 3 {
			t.Errorf("caret after the cut commit %d-%d", a, b)
		}
		nf.Release()

		// The root's own text input.
		w.SetTextInput(kit.TextInput{Active: true, Caret: kit.R(10, 10, 11, 26)})
		n = len(r.keys)
		if !d.SetMarked("か") {
			t.Errorf("no composition in the root (first responder %s)", d.Responder())
		}
		d.Key(true, 0x33, "\x7f", "\x7f", 0) // Backspace: the input method's
		d.Key(false, 0x33, "\x7f", "\x7f", 0)
		if got := r.keys[n:]; len(got) != 1 || got[0].Action != kit.KeyUp {
			t.Errorf("Backspace during the root's composition: the root saw %v", got)
		}
		d.Insert("漢")
		if got := strings.Join(r.texts, ","); got != "漢" {
			t.Errorf("the root's text %q", got)
		}
		w.SetTextInput(kit.TextInput{})
	})
}

// TestDXViewSelectorsOnce: the fields' DXView category (the root view's text input) and the
// other parts' DXView methods never implement one selector twice, which would leave the
// runtime to pick one.
func TestDXViewSelectorsOnce(t *testing.T) {
	onMain(func() {
		r := &root{}
		w, _ := open(t, r, kit.Size{W: 100, H: 60})
		if w != nil {
			defer w.Close()
		}
		if dup := DuplicateSelectors("DXView"); dup != "" {
			t.Errorf("DXView implements %s more than once", dup)
		}
	})
}
