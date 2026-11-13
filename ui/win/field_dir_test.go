//go:build windows

package win

import (
	"testing"
	"time"
	"unicode/utf16"

	"downex/ui/kit"
	"downex/ui/win/internal/w32"
)

// A field with AutoDirection is HTML's dir="auto" input (the Confirm dialog's File name, a
// queue's and a category's name): a Kurdish or Arabic name sits at the right edge and reads
// right to left, its ".mp4" on the left, and the field turns as its text changes, by SetText
// and by typing. A field without it stays left to right whatever its text.
func TestNativeFieldAutoDirection(t *testing.T) {
	runtimeLock(t)
	a, err := NewApp(AppOptions{ClassName: "DownexFieldDirTest"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	mk := func(auto bool) *kit.TextField {
		f := kit.NewTextField(kit.TextFieldOptions{
			Style: fieldStyle, Color: kit.RGB(0x0f1613), Background: kit.RGB(0xffffff),
			Placeholder: "Named by the server", PlaceholderColor: kit.RGB(0xff0000), Name: "File name", AutoDirection: auto,
		})
		f.Style = kit.Style{Width: kit.Px(420), Height: kit.Px(36), Border: kit.EdgesAll(kit.Px(1)), Padding: kit.EdgesInline(kit.Px(11))}
		return f
	}
	auto, plain := mk(true), mk(false)
	page := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, RowGap: kit.Px(8), Padding: kit.EdgesAll(kit.Px(8))}}
	page.Add(auto, plain)
	root := kit.NewWindow(page, kit.WindowOptions{Title: "Downex field direction test"})
	mon := w32.MonitorFromPoint(w32.Point{}, w32.MONITOR_DEFAULTTOPRIMARY)
	mi, _ := w32.GetMonitorInfo(mon)
	w, err := a.NewWindow(root, WindowOptions{Size: kit.Size{W: 480, H: 120}, NoActivate: true, Placed: true,
		At: w32.Point{X: mi.Work.Left + 60, Y: mi.Work.Top + 60}})
	if err != nil {
		t.Fatal(err)
	}
	w.overWindow = func() bool { return false }
	pump(t, a, w, "the first frame", 2*time.Second, func() bool { return w.painted })
	edit := func(f *kit.TextField) *textField { return f.Native().(*textField) }

	// x of the UTF-16 unit i in the EDIT (EM_POSFROMCHAR: its leading edge, client pixels)
	posOf := func(f *textField, i int) int32 {
		return int32(int16(w32.SendMessage(f.hwnd, w32.EM_POSFROMCHAR, uintptr(i), 0)))
	}
	rtl := func(f *textField) bool { return w32.GetWindowLongPtr(f.hwnd, gwlExStyle)&wsExRTLReading != 0 }
	right := func(f *textField) bool { return w32.GetWindowLongPtr(f.hwnd, w32.GWL_STYLE)&w32.ES_RIGHT != 0 }

	const kurdish = "سلێمانی تا بەغداد بۆ بینینی The Odyssey و IMAX.mp4"
	n := len(utf16.Encode([]rune(kurdish)))
	check := func(what string, f *textField, wantRTL bool) {
		t.Helper()
		if rtl(f) != wantRTL || right(f) != wantRTL {
			t.Errorf("%s: right-to-left reading %v, right-aligned %v; want %v", what, rtl(f), right(f), wantRTL)
		}
		first, last := posOf(f, 0), posOf(f, n-1) // the first Arabic letter, the "4" of ".mp4"
		if wantRTL && !(last < first) {
			t.Errorf("%s: the name's first letter at x %d and its \"4\" at %d: the extension is not on the left", what, first, last)
		}
		if !wantRTL && !(last > first) {
			t.Errorf("%s: the name's first letter at x %d and its \"4\" at %d: the extension is not on the right", what, first, last)
		}
		if wantRTL {
			rc := w32.GetClientRect(f.hwnd)
			if first < rc.Right-40 {
				t.Errorf("%s: a right-to-left name starts at x %d of %d, not at the right edge", what, first, rc.Right)
			}
		}
	}
	auto.SetText(kurdish)
	plain.SetText(kurdish)
	check("dir=auto, Kurdish", edit(auto), true)
	check("no dir, Kurdish", edit(plain), false)

	// an English name turns it back; an empty field is left to right
	auto.SetText("The Odyssey IMAX.mp4")
	if rtl(edit(auto)) || right(edit(auto)) {
		t.Error("dir=auto with an English name stays right to left")
	}
	auto.SetText("")
	if rtl(edit(auto)) || right(edit(auto)) {
		t.Error("an empty dir=auto field is right to left")
	}
	// typing an Arabic letter turns it, as the input event re-resolves dir=auto
	w32.SendMessage(edit(auto).hwnd, w32.WM_CHAR, 'ب', 1)
	pumpFor(t, a, w, 100*time.Millisecond, func() bool { return rtl(edit(auto)) })
	if !rtl(edit(auto)) || !right(edit(auto)) {
		t.Errorf("typing an Arabic letter into an empty dir=auto field: right-to-left %v, right-aligned %v", rtl(edit(auto)), right(edit(auto)))
	}
}
