//go:build windows

package win

import (
	"fmt"
	"testing"
	"time"
	"unicode/utf16"
	"unsafe"

	"downex/ui/kit"
	"downex/ui/win/internal/w32"
)

// escField is a field whose box clears it on Escape, as the title bar's search does.
type escField struct {
	kit.TextField
}

func (f *escField) HandleKey(e kit.KeyEvent) bool {
	if e.Action == kit.KeyDown && e.Key == kit.KeyEscape && f.Text() != "" {
		f.SetText("")
		if f.OnChange != nil {
			f.OnChange("")
		}
		return true
	}
	return false
}

type fieldRoot struct {
	win           *kit.Window
	page          *kit.Element
	field         *escField
	before, after *kit.Pressable
	changes       []string
}

var fieldStyle = kit.TextStyle{Family: "'Segoe UI Variable Text', 'Segoe UI', system-ui", Size: 13}

func newFieldRoot() *fieldRoot {
	r := &fieldRoot{}
	r.field = &escField{TextField: *kit.NewTextField(kit.TextFieldOptions{
		Style: fieldStyle, Color: kit.RGB(0x0f1613), Background: kit.RGB(0xffffff),
		Placeholder: "Search downloads", PlaceholderColor: kit.RGB(0xff0000), Name: "Search downloads",
	})}
	r.field.Style = kit.Style{Width: kit.Px(300), Height: kit.Px(28), Border: kit.EdgesAll(kit.Px(1)),
		Padding: kit.Edges{Start: kit.Px(32), End: kit.Px(62)}, Background: kit.RGB(0xffffff), BorderColor: kit.RGB(0x808b85)}
	r.field.OnChange = func(s string) { r.changes = append(r.changes, s) }
	r.before = kit.NewPressable(nil)
	r.before.Style = kit.Style{Width: kit.Px(30), Height: kit.Px(30)}
	r.after = kit.NewPressable(nil)
	r.after.Style = kit.Style{Width: kit.Px(30), Height: kit.Px(30)}
	r.page = &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(12),
		Padding: kit.EdgesAll(kit.Px(8)), Background: kit.RGB(0xeef2f0)}}
	r.page.Add(r.before, r.field, r.after)
	r.win = kit.NewWindow(r.page, kit.WindowOptions{Title: "Downex field test"})
	return r
}

var procIsWindow = user32.NewProc("IsWindow")

func isWindow(h w32.HWND) bool {
	r, _, _ := procIsWindow.Call(uintptr(h))
	return r != 0
}

// A native field fills its box's content area, takes and gives text without echoing
// SetText, hands the root its keys first, follows the kit's focus, draws its placeholder
// with DirectWrite while it is empty, follows a DPI change and goes with its widget.
func TestNativeField(t *testing.T) {
	r := newFieldRoot()
	root := r.win
	runtimeLock(t)
	a, err := NewApp(AppOptions{ClassName: "DownexFieldTest"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	mon := w32.MonitorFromPoint(w32.Point{}, w32.MONITOR_DEFAULTTOPRIMARY)
	mi, _ := w32.GetMonitorInfo(mon)
	w, err := a.NewWindow(root, WindowOptions{Size: kit.Size{W: 480, H: 120}, NoActivate: true, Placed: true,
		At: w32.Point{X: mi.Work.Left + 60, Y: mi.Work.Top + 60}})
	if err != nil {
		t.Fatal(err)
	}
	w.overWindow = func() bool { return false }
	pump(t, a, w, "the first frame", 2*time.Second, func() bool { return w.painted })
	if len(w.fields) != 1 {
		t.Fatalf("%d native fields", len(w.fields))
	}
	f := w.fields[0]
	if !isWindow(f.hwnd) || !f.shown {
		t.Fatal("the field's EDIT is not shown")
	}
	if f.fontKey.family != "Segoe UI Variable Text" {
		t.Logf("the field's GDI font is %q (Segoe UI Variable Text not installed?)", f.fontKey.family)
	}

	// Placed on the content box: x from the box's left edge, border and padding in; its line
	// centred in the box as a browser centres an input's.
	ppd := w.ppd()
	box := r.field.WindowBounds()
	content := r.field.ContentRect().Add(box.Min)
	er := w32.GetWindowRect(f.hwnd)
	o := w32.ClientToScreen(w.hwnd, w32.Point{})
	left := er.Left - o.X
	if want := int32(content.Min.X*ppd + 0.5); left != want {
		t.Errorf("EDIT at x %d px, want the content box's %d", left, want)
	}
	if top, bottom := float32(er.Top-o.Y)/ppd, float32(er.Bottom-o.Y)/ppd; top < box.Min.Y || bottom > box.Max.Y {
		t.Errorf("EDIT from %.1f to %.1f DIPs, outside its box %v", top, bottom, box)
	}

	// SetText is not the user's change; typing is. The user types into the field with the
	// focus, where a value set goes in with the caret at its end (unfocused, the caret is at
	// the start, so that the value shows from its start: TestNativeFieldShowsAnUnfocusedValueFromItsStart).
	root.Focus(r.field)
	r.field.SetText("ubuntu.iso")
	if got := f.Text(); got != "ubuntu.iso" || len(r.changes) != 0 {
		t.Fatalf("SetText: field %q, changes %q", got, r.changes)
	}
	w32.SendMessage(f.hwnd, w32.WM_CHAR, 'x', 1)
	pumpFor(t, a, w, 50*time.Millisecond, func() bool { return len(r.changes) > 0 })
	if fmt.Sprint(r.changes) != "[ubuntu.isox]" || r.field.Text() != "ubuntu.isox" {
		t.Errorf("typing: changes %q, widget text %q", r.changes, r.field.Text())
	}
	// The characters of keys the root leaves to the field: Ctrl+Backspace (DEL) deletes the
	// word before the caret, as a browser's input does; Enter and Escape type nothing.
	r.field.SetText("annual report")
	r.changes = nil
	for _, c := range []uintptr{'\r', 0x1B, '\n'} {
		if w32.SendMessage(f.hwnd, w32.WM_CHAR, c, 1) != 0 {
			t.Errorf("WM_CHAR %#x answered by the EDIT", c)
		}
	}
	w32.SendMessage(f.hwnd, w32.WM_CHAR, 0x7F, 1)
	pumpFor(t, a, w, 50*time.Millisecond, func() bool { return len(r.changes) > 0 })
	if got := f.Text(); got != "annual " || fmt.Sprint(r.changes) != "[annual ]" {
		t.Errorf("Ctrl+Backspace after %q: field %q, changes %q; want %q", "annual report", got, r.changes, "annual ")
	}
	w32.SendMessage(f.hwnd, w32.WM_CHAR, 0x7F, 1)
	if got := f.Text(); got != "" {
		t.Errorf("a second Ctrl+Backspace left %q", got)
	}
	r.changes = nil
	intl := "会议记录 📦 تقرير"
	r.field.SetText(intl)
	if got := f.Text(); got != intl {
		t.Errorf("international text came back as %q", got)
	}

	// The kit's focus moves the system's.
	root.Focus(r.field)
	if w32.GetFocus() != f.hwnd {
		t.Fatal("focusing the widget did not focus the EDIT")
	}
	// Escape goes to the root first, which clears the field; the EDIT never sees it.
	r.changes = nil
	postKey(f.hwnd, uintptr(kit.KeyEscape))
	pumpFor(t, a, w, 300*time.Millisecond, func() bool { return len(r.changes) > 0 })
	if fmt.Sprint(r.changes) != "[]" || f.Text() != "" {
		t.Errorf("Escape: changes %q, field %q", r.changes, f.Text())
	}
	// Tab moves the kit's focus on, and the system's focus back to the window.
	postKey(f.hwnd, uintptr(kit.KeyTab))
	pumpFor(t, a, w, 300*time.Millisecond, func() bool { return root.Focused() == r.after.Base() })
	if root.Focused() != r.after.Base() || w32.GetFocus() == f.hwnd {
		t.Errorf("Tab out of the field: kit focus %v, system focus on the EDIT %v", root.Focused(), w32.GetFocus() == f.hwnd)
	}

	// The placeholder: drawn with DirectWrite while the field is empty (red here), gone once
	// it has text.
	red := func() int {
		w32.InvalidateRect(f.hwnd, nil, true)
		w32.UpdateWindow(f.hwnd)
		w32.DwmFlush() // the capture reads what the compositor shows
		w32.DwmFlush()
		img, err := capture(w)
		if err != nil {
			t.Skipf("cannot capture the window: %v", err)
		}
		n := 0
		rc := w32.GetWindowRect(f.hwnd)
		for y := rc.Top - o.Y; y < rc.Bottom-o.Y; y++ {
			for x := rc.Left - o.X; x < rc.Right-o.X; x++ {
				c := img.RGBAAt(int(x), int(y))
				if c.R > 200 && c.G < 70 && c.B < 70 { // the placeholder's red; ClearType fringes of dark text are never this pure
					n++
				}
			}
		}
		return n
	}
	if n := red(); n < 20 {
		t.Errorf("the empty field shows %d placeholder pixels", n)
	}
	r.field.SetText("kernel.tar")
	if n := red(); n != 0 {
		t.Errorf("a field with text still shows %d placeholder pixels", n)
	}

	// A DPI change makes the font again at the new size.
	h0 := f.tm.Height
	w.setDPI(96)
	if f.tm.Height == h0 && ppd != 1 {
		t.Errorf("the font kept its height %d after a DPI change from %.0f to 96", h0, ppd*96)
	}
	w.setDPI(uint32(ppd * 96))
	if f.tm.Height != h0 {
		t.Errorf("font height %d after going back, was %d", f.tm.Height, h0)
	}

	// The field goes with its widget.
	edit := f.hwnd
	r.page.Remove(r.field)
	pumpFor(t, a, w, 100*time.Millisecond, func() bool { return !isWindow(edit) })
	if isWindow(edit) || len(w.fields) != 0 || fieldByHWND(edit) != nil {
		t.Errorf("after the widget left: EDIT alive %v, fields %d", isWindow(edit), len(w.fields))
	}
}

// postKey posts a key's down and up to a window, as the system would.
func postKey(h w32.HWND, vk uintptr) {
	scan, _, _ := user32.NewProc("MapVirtualKeyW").Call(vk, 0)
	w32.PostMessage(h, w32.WM_KEYDOWN, vk, 1|scan<<16)
	w32.PostMessage(h, w32.WM_KEYUP, vk, 1|scan<<16|3<<30)
}

// Ctrl+Backspace's word: spaces, then a word or a run of punctuation, as a browser deletes.
func TestWordStartBefore(t *testing.T) {
	for _, c := range []struct {
		text string
		end  int // in UTF-16 units; -1: the end
		want string
	}{
		{"annual report", -1, "annual "},
		{"annual report  ", -1, "annual "},
		{"kernel-6.11.tar", -1, "kernel-6.11."},
		{"kernel-6.11.", -1, "kernel-6.11"},
		{"a.b ...", -1, "a.b "},
		{"report", 3, "ort"},
		{"会议记录 📦", -1, "会议记录 "},
		{"", -1, ""},
		{"   ", -1, ""},
	} {
		u := utf16.Encode([]rune(c.text))
		end := c.end
		if end < 0 {
			end = len(u)
		}
		i := wordStartBefore(u, end)
		got := string(utf16.Decode(append(append([]uint16{}, u[:i]...), u[end:]...)))
		if got != c.want {
			t.Errorf("%q at %d: %q left, want %q", c.text, end, got, c.want)
		}
	}
}

// A value set while the field has no focus shows from its start, as a browser's unfocused
// input does, also when it was set before the EDIT had its size (a prefilled Save to folder
// or file name); while the user edits, a value set goes in with the caret at its end.
func TestNativeFieldShowsAnUnfocusedValueFromItsStart(t *testing.T) {
	const long = `C:\Users\someone\AppData\Local\Temp\downloads\2026\november\budget-plan-final-revised.xlsx`
	r := newFieldRoot()
	r.field.SetText(long) // before the window: the EDIT is made, still 0 px wide
	runtimeLock(t)
	a, err := NewApp(AppOptions{ClassName: "DownexFieldStartTest"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	mon := w32.MonitorFromPoint(w32.Point{}, w32.MONITOR_DEFAULTTOPRIMARY)
	mi, _ := w32.GetMonitorInfo(mon)
	w, err := a.NewWindow(r.win, WindowOptions{Size: kit.Size{W: 480, H: 120}, NoActivate: true, Placed: true,
		At: w32.Point{X: mi.Work.Left + 60, Y: mi.Work.Top + 60}})
	if err != nil {
		t.Fatal(err)
	}
	w.overWindow = func() bool { return false }
	pump(t, a, w, "the first frame", 2*time.Second, func() bool { return w.painted })
	if len(w.fields) != 1 {
		t.Fatalf("%d native fields", len(w.fields))
	}
	f := w.fields[0]
	// The character at the EDIT's left edge (EM_CHARFROMPOS at x 2, y 4: its low word).
	first := func() uintptr { return w32.SendMessage(f.hwnd, w32.EM_CHARFROMPOS, 0, 2|4<<16) & 0xffff }
	if got := f.Text(); got != long {
		t.Fatalf("the field holds %q", got)
	}
	if n := first(); n != 0 {
		t.Errorf("set before its first layout: the field shows from character %d, want 0", n)
	}
	r.field.SetText(long + " (2)") // set again, laid out and without the focus
	if n := first(); n != 0 {
		t.Errorf("set without the focus: the field shows from character %d, want 0", n)
	}
	var sel [2]uint32
	w32.SendMessage(f.hwnd, w32.EM_GETSEL, uintptr(unsafe.Pointer(&sel[0])), uintptr(unsafe.Pointer(&sel[1])))
	if sel != [2]uint32{0, 0} {
		t.Errorf("selection %v, want the caret at the start", sel)
	}
	r.win.Focus(r.field)
	if w32.GetFocus() != f.hwnd {
		t.Fatal("focusing the widget did not focus the EDIT")
	}
	r.field.SetText(long)
	w32.SendMessage(f.hwnd, w32.EM_GETSEL, uintptr(unsafe.Pointer(&sel[0])), uintptr(unsafe.Pointer(&sel[1])))
	if n := uint32(len(utf16.Encode([]rune(long)))); sel != [2]uint32{n, n} || first() == 0 {
		t.Errorf("set while editing: selection %v and first visible character %d, want the caret at the end (%d) in view", sel, first(), n)
	}
}

// A field aligned to the end (CSS text-align: end: Settings' limits, the Queues' retry
// minutes) shows its text at the right edge of a left-to-right window and at the left edge
// of a right-to-left one; start is the other side, and a change of options moves it.
func TestNativeFieldAlignment(t *testing.T) {
	r := newFieldRoot()
	runtimeLock(t)
	a, err := NewApp(AppOptions{ClassName: "DownexFieldAlignTest"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	mon := w32.MonitorFromPoint(w32.Point{}, w32.MONITOR_DEFAULTTOPRIMARY)
	mi, _ := w32.GetMonitorInfo(mon)
	w, err := a.NewWindow(r.win, WindowOptions{Size: kit.Size{W: 480, H: 120}, NoActivate: true, Placed: true,
		At: w32.Point{X: mi.Work.Left + 60, Y: mi.Work.Top + 60}})
	if err != nil {
		t.Fatal(err)
	}
	w.overWindow = func() bool { return false }
	pump(t, a, w, "the first frame", 2*time.Second, func() bool { return w.painted })
	f := w.fields[0]
	r.field.SetText("10")
	width := w32.GetClientRect(f.hwnd).Right
	// x of the first character's left edge in the EDIT (EM_POSFROMCHAR: its low word).
	at := func() int32 { return int32(int16(w32.SendMessage(f.hwnd, w32.EM_POSFROMCHAR, 0, 0) & 0xffff)) }
	opts := f.opts
	for _, c := range []struct {
		name  string
		align kit.TextAlign
		dir   kit.Direction
		right bool
	}{
		{"start", kit.TextAlignStart, kit.DirLTR, false},
		{"end", kit.TextAlignEnd, kit.DirLTR, true},
		{"end, right to left", kit.TextAlignEnd, kit.DirRTL, false},
		{"start, right to left", kit.TextAlignStart, kit.DirRTL, true},
		{"start again", kit.TextAlignStart, kit.DirLTR, false},
	} {
		o := opts
		o.Align, o.Direction = c.align, c.dir
		f.SetOptions(o)
		x := at()
		if right := x > width/2; right != c.right {
			t.Errorf("%s: the text starts at x %d of %d px; want it at the right edge %v", c.name, x, width, c.right)
		}
	}
}
