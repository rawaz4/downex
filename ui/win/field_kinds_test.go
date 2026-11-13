//go:build windows

package win

import (
	"testing"
	"time"
	"unicode/utf16"
	"unsafe"

	"downex/ui/core/queues"
	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/win/internal/w32"
)

var (
	tpGetKeyboardLayoutList = user32.NewProc("GetKeyboardLayoutList")
	tpEMGetPasswordChar     = uint32(0x00D2)
)

const (
	esPassword = 0x0020
	esMultiLn  = 0x0004

	wmIMEStartComposition = 0x010D
	wmIMEEndComposition   = 0x010E
	wmIMEComposition      = 0x010F
	wmIMEChar             = 0x0286
	wmIMENotify           = 0x0282
	wmIMESetContext       = 0x0281
)

// fieldWidgets is a window of every native field kind the widgets have.
type fieldWidgets struct {
	*widgetWindow
	text     *widgets.Input
	password *widgets.Input
	number   *widgets.NumberField
	clock    *widgets.DateTimeField
	area     *widgets.TextArea
	changes  map[string][]string
	applied  []string
}

func openFields(t *testing.T) *fieldWidgets {
	t.Helper()
	fw := &fieldWidgets{changes: map[string][]string{}}
	rec := func(k string) func(string) { return func(s string) { fw.changes[k] = append(fw.changes[k], s) } }
	fw.text = widgets.NewInput("", widgets.InputOptions{Name: "Name", Placeholder: "Night"}, rec("text"))
	fw.password = widgets.NewInput("", widgets.InputOptions{Name: "Proxy password", Password: true, Size: widgets.FieldSm, Placeholder: "None"}, rec("password"))
	fw.number = widgets.NewNumberField("", widgets.InputOptions{Name: "Speed limit", Size: widgets.FieldSm}, func(s string) { fw.applied = append(fw.applied, s) })
	fw.clock = widgets.NewDateTimeField(queues.TimeField, "23:30", "Start at", rec("clock"))
	fw.area = widgets.NewTextArea("", 4, widgets.InputOptions{Name: "URLs", Placeholder: "https://example.com/file.zip"}, rec("area"))
	fw.widgetWindow = openWidgets(t, "DownexFieldKindsTest", false, kit.Size{W: 520, H: 440}, fw.text, fw.password, fw.number, fw.clock, fw.area)
	return fw
}

// edit is the EDIT of a widget's native field.
func (fw *fieldWidgets) edit(t *testing.T, nf kit.NativeField) w32.HWND {
	t.Helper()
	switch f := nf.(type) {
	case *textField:
		return f.hwnd
	case *areaField:
		return f.hwnd
	}
	t.Fatalf("native field %T", nf)
	return 0
}

// TestFieldKinds checks each kind's EDIT: a password field with the system's mark and no input
// method, a multi-line field over the whole text area, the caret and selection a DateTimeField
// steps through, and Enter's line break in the text area.
func TestFieldKinds(t *testing.T) {
	fw := openFields(t)
	a, w := fw.a, fw.w
	pw := fw.edit(t, fw.password.Native())
	if st := windowLong(pw, -16); st&esPassword == 0 { // GWL_STYLE
		t.Errorf("the password EDIT's style %#x lacks ES_PASSWORD", st)
	}
	if c := w32.SendMessage(pw, tpEMGetPasswordChar, 0, 0); c != passwordMark {
		t.Errorf("password mark %#x, want U+25CF", c)
	}
	if imc := w32.ImmGetContext(pw); imc != 0 {
		w32.ImmReleaseContext(pw, imc)
		t.Error("the password field has an input context: input methods are on in it")
	}
	ar := fw.edit(t, fw.area.Native())
	if st := windowLong(ar, -16); st&esMultiLn == 0 {
		t.Errorf("the text area's style %#x lacks ES_MULTILINE", st)
	}
	content := fw.area.ContentRect().Add(fw.area.WindowBounds().Min)
	rc := w32.GetWindowRect(ar)
	o := w32.ClientToScreen(w.hwnd, w32.Point{})
	ppd := w.ppd()
	if got, want := float32(rc.Bottom-rc.Top)/ppd, content.Height(); got < want-1 || got > want+1 {
		t.Errorf("the text area's EDIT is %.1f DIPs high, its content box %.1f", got, want)
	}
	if got, want := float32(rc.Top-o.Y)/ppd, content.Min.Y; got < want-1 || got > want+1 {
		t.Errorf("the text area's EDIT is at %.1f, its content box at %.1f", got, want)
	}

	// The text area shows a vertical scroll bar only while its lines overflow it (a
	// textarea's overflow: auto): none with one line, one past its 4 rows, none again once
	// the lines fit (before, WS_VSCROLL was always on: a bar beside one URL).
	vscroll := func() bool { return windowLong(ar, -16)&0x00200000 != 0 } // WS_VSCROLL
	if vscroll() {
		t.Error("the empty text area shows a scroll bar")
	}
	fw.area.SetText("https://example.com/one.zip")
	if vscroll() {
		t.Error("the text area shows a scroll bar with one line")
	}
	long := "https://example.com/1.zip\r\nhttps://example.com/2.zip\r\nhttps://example.com/3.zip\r\nhttps://example.com/4.zip\r\nhttps://example.com/5.zip\r\nhttps://example.com/6.zip"
	fw.area.SetText(long)
	if !vscroll() {
		t.Error("six lines in four rows: no scroll bar")
	}
	fw.area.SetText("")
	if vscroll() {
		t.Error("the scroll bar stayed once the text fits")
	}

	// Links joined with LF (a paste, a drop, the failed ones listed again: a textarea's value)
	// show one per line: the EDIT breaks lines only at CR LF, and ran them together on one
	// line. The value reads back with LF; a lone CR is a line break too, as in a textarea.
	lines := func() int { return int(w32.SendMessage(ar, w32.EM_GETLINECOUNT, 0, 0)) }
	for _, c := range []struct{ set, want string }{
		{"https://example.com/a.zip\nhttps://example.com/b.zip\nhttps://example.com/c.zip", "https://example.com/a.zip\nhttps://example.com/b.zip\nhttps://example.com/c.zip"},
		{"https://example.com/a.zip\r\nhttps://example.com/b.zip\rhttps://example.com/c.zip", "https://example.com/a.zip\nhttps://example.com/b.zip\nhttps://example.com/c.zip"},
	} {
		fw.area.SetText(c.set)
		if n := lines(); n != 3 {
			t.Errorf("SetText(%q): the EDIT shows %d lines, want 3", c.set, n)
		}
		if got := fw.area.Native().Text(); got != c.want {
			t.Errorf("SetText(%q): the field reads back %q, want %q", c.set, got, c.want)
		}
	}
	fw.area.SetText("")
	// A single-line field takes no line breaks, as an input's value has none.
	was := fw.clock.Native().Text()
	fw.clock.Native().SetText("00:\n15")
	if got := fw.clock.Native().Text(); got != "00:15" {
		t.Errorf("a single-line field kept a line break: %q", got)
	}
	fw.clock.Native().SetText(was)

	// the caret: Up at the hour steps it and selects it, through the real EDIT
	ck := fw.edit(t, fw.clock.Native())
	w.call(func() { fw.kw.Focus(fw.clock) })
	w32.SendMessage(ck, w32.EM_SETSEL, 1, 1)
	postKey(ck, uintptr(kit.KeyArrowUp))
	pump(t, a, w, "the step", time.Second, func() bool { return fw.clock.Text() == "00:30" })
	if s, e := fw.clock.Native().(*textField).Selection(); s != 0 || e != 2 {
		t.Errorf("selection %d-%d after the step, want the hour 0-2", s, e)
	}
	if got := fw.clock.Native().Text(); got != "00:30" {
		t.Errorf("the EDIT shows %q", got)
	}

	// Enter in the text area breaks the line (one undoable edit), through the root; the value
	// has it as LF, as a textarea's value does
	w.call(func() { fw.kw.Focus(fw.area) })
	w32.SendMessage(ar, w32.WM_CHAR, 'a', 1)
	postKey(ar, uintptr(kit.KeyEnter))
	if !pumpFor(t, a, w, time.Second, func() bool { return fw.area.Text() == "a\n" }) {
		t.Fatalf("the line break: area %q, EDIT %q, kit focus %T, system focus on the EDIT %v", fw.area.Text(), fw.area.Native().Text(), fw.kw.Focused().Widget(), w32.GetFocus() == ar)
	}
	w32.SendMessage(ar, w32.WM_CHAR, 'b', 1)
	pump(t, a, w, "the second line", time.Second, func() bool { return fw.area.Text() == "a\nb" })

	// the number field applies 600 ms after typing
	nb := fw.edit(t, fw.number.Native())
	w.call(func() { fw.kw.Focus(fw.number) })
	w32.SendMessage(nb, w32.WM_CHAR, '5', 1)
	start := time.Now()
	pump(t, a, w, "the apply", 2*time.Second, func() bool { return len(fw.applied) == 1 })
	if d := time.Since(start); d < 550*time.Millisecond {
		t.Errorf("applied after %v, want about 600 ms", d)
	}
}

// TestFieldIME composes Japanese and Chinese text in every field kind that takes it, as an
// input method does: the field has an input context (ImmAssociateContext gives it one of its
// own, opened), and WM_IME_STARTCOMPOSITION, WM_IME_COMPOSITION, WM_IME_CHAR and
// WM_IME_ENDCOMPOSITION reach the EDIT through the field's subclass, which inserts the composed
// characters; the root sees no key of the composition (VK_PROCESSKEY is the input method's)
// and hears the change. The password field keeps input methods off, as a browser's does.
func TestFieldIME(t *testing.T) {
	fw := openFields(t)
	a, w := fw.a, fw.w
	if n, _, _ := tpGetKeyboardLayoutList.Call(0, 0); n > 0 {
		layouts := make([]uintptr, n)
		tpGetKeyboardLayoutList.Call(n, uintptr(unsafe.Pointer(&layouts[0])))
		var ja, zh bool
		for _, l := range layouts {
			switch l & 0xFFFF {
			case 0x0411:
				ja = true
			case 0x0804, 0x0404:
				zh = true
			}
		}
		t.Logf("installed keyboard layouts: %d (Japanese %v, Chinese %v); the composition below needs none", n, ja, zh)
	}
	cases := []struct {
		name   string
		widget kit.Widget
		nf     func() kit.NativeField
		text   func() string
	}{
		{"text", fw.text, fw.text.Native, fw.text.Text},
		{"number", fw.number, fw.number.Native, fw.number.Text},
		{"date and time", fw.clock, fw.clock.Native, fw.clock.Text},
		{"multi-line", fw.area, fw.area.Native, fw.area.Text},
	}
	for _, c := range cases {
		for _, compose := range []string{"日本語", "中文"} {
			h := fw.edit(t, c.nf())
			w.call(func() { fw.kw.Focus(c.widget) })
			if w32.GetFocus() != h {
				t.Fatalf("%s: the EDIT does not have the focus", c.name)
			}
			c.nf().SetText("")
			imc := immCreateContext()
			if imc == 0 {
				t.Fatal("ImmCreateContext failed")
			}
			prev := immAssociateContext(h, imc)
			immSetOpenStatus(imc, true)
			if !immGetOpenStatus(imc) {
				t.Errorf("%s: the input context does not open", c.name)
			}
			keysBefore := 0
			w32.SendMessage(h, wmIMESetContext, 1, 0xC000000F)
			w32.SendMessage(h, wmIMEStartComposition, 0, 0)
			for _, u := range utf16.Encode([]rune(compose)) {
				w32.SendMessage(h, w32.WM_KEYDOWN, w32.VK_PROCESSKEY, 1)
				w32.SendMessage(h, wmIMEComposition, uintptr(u), popGCSResultStr)
				w32.SendMessage(h, wmIMEChar, uintptr(u), 1)
			}
			w32.SendMessage(h, wmIMEEndComposition, 0, 0)
			pump(t, a, w, c.name+" composition", time.Second, func() bool { return c.text() == compose })
			if got := c.nf().Text(); got != compose {
				t.Errorf("%s: the EDIT holds %q after composing %q", c.name, got, compose)
			}
			_ = keysBefore
			immAssociateContext(h, prev)
			immDestroyContext(imc)
		}
	}
	// the password field: no input context, so no composition
	pw := fw.edit(t, fw.password.Native())
	if imc := w32.ImmGetContext(pw); imc != 0 {
		w32.ImmReleaseContext(pw, imc)
		t.Error("the password field has an input context")
	}
}
