//go:build windows

package win

import (
	"fmt"
	"math"
	"runtime/debug"
	"strings"
	"sync"
	"unicode"
	"unicode/utf16"
	"unsafe"

	"downex/ui/kit"
	"downex/ui/win/internal/com"
	"downex/ui/win/internal/d2d"
	"downex/ui/win/internal/w32"

	"golang.org/x/sys/windows"
)

// textField is a native text field hosted in a window (kit.NativeField): an EDIT control of
// Common Controls 6 (the executable's manifest asks for it), a child window that does its own
// typing, input methods, caret, selection, undo, clipboard, context menu and accessibility.
// It is subclassed for three things the kit needs: its keys go to the root first (and a key
// the root uses never reaches it), its pointer moves reach the root (the box's hover), and
// while it is empty its placeholder is drawn with DirectWrite, in the window's text engine,
// over the field (EDIT's own cue banner draws with GDI in the theme's grey, which no message
// changes). Its text is drawn by the EDIT with a GDI font of the style's family, size and
// weight.
type textField struct {
	w    *Window
	hwnd w32.HWND
	old  uintptr // the EDIT's own window procedure

	opts   kit.TextFieldOptions
	cb     kit.TextFieldCallbacks
	bounds kit.Rect // the text area the root gave, window DIPs
	shown  bool
	placed w32.Rect // where the EDIT is, client pixels (while shown)

	font     windows.Handle
	fontKey  fontKey
	tm       w32.TEXTMETRICW
	brush    windows.Handle
	brushRef uint32

	// The placeholder: a layout of the window's resources and a Direct2D target that draws
	// into the EDIT's device context.
	placeholder kit.TextLayout
	dc          *d2d.DCRenderTarget
	dcBrush     *d2d.SolidColorBrush

	// Heap memory for the calls that run window procedures (package w32's rule).
	face  *uint16
	text  []uint16
	set   *uint16
	tme   w32.TRACKMOUSEEVENT
	setRc d2d.RECT
	sel   [2]uint32 // EM_GETSEL's start and end

	setting bool // SetText is running: EN_CHANGE is not the user's
	// changed runs after every change of the text, the user's or SetText's (a multi-line
	// field fits its scroll bar to it); nil for a one-line field.
	changed func()
	multi   bool // an ES_MULTILINE EDIT (field_kinds.go's areaField)
	// dir is the direction the field shows its text in: the options' Direction, or with
	// AutoDirection its text's, which applyAlign follows at every change.
	dir      kit.Direction
	empty    bool // the text was empty at the last change
	tracking bool // WM_MOUSELEAVE is asked for
	released bool
}

type fontKey struct {
	family string
	px     int32
	weight int32
}

// The fields' subclass procedure and the table that finds a field from its HWND.
var (
	fieldProcOnce sync.Once
	fieldProcPtr  uintptr
	fieldTable    struct {
		sync.Mutex
		byHWND map[w32.HWND]*textField
	}
	editClass = func() *uint16 { p, _ := windows.UTF16PtrFromString("Edit"); return p }()
	noText    = func() *uint16 { p, _ := windows.UTF16PtrFromString(""); return p }()
)

func fieldByHWND(h w32.HWND) *textField {
	if h == 0 {
		return nil
	}
	fieldTable.Lock()
	defer fieldTable.Unlock()
	return fieldTable.byHWND[h]
}

// fieldOf is w's field with HWND h, or nil.
func (w *Window) fieldOf(h w32.HWND) *textField {
	for _, f := range w.fields {
		if f.hwnd == h {
			return f
		}
	}
	return nil
}

// NewTextField implements kit.NativeFields.
func (w *Window) NewTextField(opts kit.TextFieldOptions, cb kit.TextFieldCallbacks) (kit.NativeField, error) {
	if w.closed {
		return nil, fmt.Errorf("win: window closed: %w", kit.ErrClosed)
	}
	fieldProcOnce.Do(func() {
		fieldProcPtr = w32.NewCallback(fieldProc)
		fieldTable.byHWND = map[w32.HWND]*textField{}
	})
	f := &textField{w: w, opts: opts, cb: cb, empty: true, text: make([]uint16, 256)}
	h, err := w32.CreateWindowEx(0, editClass, noText, w32.WS_CHILD|w32.ES_AUTOHSCROLL, 0, 0, 0, 0, w.hwnd, w.app.inst, 0)
	if err != nil {
		return nil, fmt.Errorf("win: native field: %w", err)
	}
	f.hwnd = h
	fieldTable.Lock()
	fieldTable.byHWND[h] = f
	fieldTable.Unlock()
	f.old = w32.SetWindowLongPtr(h, w32.GWLP_WNDPROC, fieldProcPtr)
	w.fields = append(w.fields, f)
	f.applyFont()
	f.applyAlign()
	f.applyColors()
	return f, nil
}

// applyAlign gives the EDIT the options' alignment (ES_LEFT, ES_CENTER or ES_RIGHT), which a
// live single-line EDIT takes through its style, in the field's direction (dirFor), and that
// direction's reading order (WS_EX_RTLREADING): a right-to-left name reads right to left, its
// ".mp4" on the left, as an input's text with dir=auto does.
func (f *textField) applyAlign() {
	f.dir = f.opts.ResolveDirection(f.Text())
	var want uintptr
	switch {
	case f.opts.Align == kit.TextAlignCenter:
		want = w32.ES_CENTER
	case f.opts.AlignsRightAt(f.dir):
		want = w32.ES_RIGHT
	}
	var wantEx uintptr
	if f.dir == kit.DirRTL {
		wantEx = wsExRTLReading
	}
	st := w32.GetWindowLongPtr(f.hwnd, w32.GWL_STYLE)
	ex := w32.GetWindowLongPtr(f.hwnd, gwlExStyle)
	if st&(w32.ES_CENTER|w32.ES_RIGHT) == want && ex&wsExRTLReading == wantEx {
		return
	}
	if st&(w32.ES_CENTER|w32.ES_RIGHT) != want {
		w32.SetWindowLongPtr(f.hwnd, w32.GWL_STYLE, st&^(w32.ES_CENTER|w32.ES_RIGHT)|want)
	}
	if ex&wsExRTLReading != wantEx {
		w32.SetWindowLongPtr(f.hwnd, gwlExStyle, ex&^wsExRTLReading|wantEx)
	}
	w32.InvalidateRect(f.hwnd, nil, true)
}

// GWL_EXSTYLE and WS_EX_RTLREADING (winuser.h).
const (
	gwlExStyle     int32 = -20
	wsExRTLReading       = 0x00002000
)

// fieldProc is every field's procedure: the field's own subclass, then the EDIT's.
func fieldProc(h w32.HWND, msg uint32, wp, lp uintptr) uintptr {
	f := fieldByHWND(h)
	if f == nil {
		return w32.DefWindowProc(h, msg, wp, lp)
	}
	return f.proc(msg, wp, lp)
}

func (f *textField) proc(msg uint32, wp, lp uintptr) (ret uintptr) {
	w := f.w
	defer func() {
		if v := recover(); v != nil {
			w.app.failIn(w, v, debug.Stack())
			w.recovered()
			ret = 0
		}
	}()
	switch msg {
	case w32.WM_KEYDOWN, w32.WM_SYSKEYDOWN, w32.WM_KEYUP, w32.WM_SYSKEYUP:
		if wp == w32.VK_PROCESSKEY || w.closed || !w.attached {
			break // the input method has it
		}
		e := kit.KeyEvent{Action: kit.KeyDown, Key: keyOf(wp), Mods: modsOf(0), Repeat: lp&(1<<30) != 0}
		if msg == w32.WM_KEYUP || msg == w32.WM_SYSKEYUP {
			e = kit.KeyEvent{Action: kit.KeyUp, Key: keyOf(wp), Mods: modsOf(0)}
		}
		if w.depth > 0 {
			w.event(e) // a modal loop: queued, and the field acts on it now
			break
		}
		if w.event(e) {
			w.keyUsed = true
			return 0 // the root used it: the field never sees it
		}
	case w32.WM_MOUSEMOVE:
		if !f.tracking {
			f.tme = w32.TRACKMOUSEEVENT{Flags: w32.TME_LEAVE, Track: f.hwnd}
			f.tme.Size = uint32(unsafe.Sizeof(f.tme))
			w32.TrackMouseEvent(&f.tme)
			f.tracking = true
		}
		ppd := w.ppd()
		px := f.pixelRect()
		pos := kit.Pt((float32(int16(lp))+float32(px.Left))/ppd, (float32(int16(lp>>16))+float32(px.Top))/ppd)
		w.pointerEnter(pos)
		w.event(kit.PointerEvent{Action: kit.PointerMove, Pos: pos, Buttons: buttonsOf(wp), Mods: modsOf(wp)})
	case w32.WM_MOUSELEAVE:
		f.tracking = false
		w.pointerMaybeLeft()
	case w32.WM_CHAR:
		// The characters of keys the root did not use (dispatch translates only those).
		switch wp {
		case 0x7F: // Ctrl+Backspace, which TranslateMessage makes DEL: a browser deletes a word
			f.deleteWordBack()
			return 0
		case '\r', '\n', 0x1B: // Enter, Ctrl+Enter and Escape: a single-line EDIT would beep
			return 0
		}
	case w32.WM_PAINT:
		r := w32.CallWindowProc(f.old, f.hwnd, msg, wp, lp)
		if wp != 0 {
			f.paintPlaceholder(windows.Handle(wp)) // a device context given: the placeholder goes there too
		} else {
			f.paintPlaceholder(0)
		}
		return r
	case wmPrintClient:
		// The EDIT draws itself into another device context (PrintWindow without the
		// compositor, AnimateWindow): the placeholder is part of its picture there too, or
		// the empty field prints as an empty box.
		r := w32.CallWindowProc(f.old, f.hwnd, msg, wp, lp)
		if wp != 0 && lp&prfClient != 0 {
			f.paintPlaceholder(windows.Handle(wp))
		}
		return r
	case w32.WM_NCDESTROY:
		r := w32.CallWindowProc(f.old, f.hwnd, msg, wp, lp)
		f.forget()
		return r
	}
	return w32.CallWindowProc(f.old, f.hwnd, msg, wp, lp)
}

// deleteWordBack deletes the selection or, with none, the word before the caret (with the
// spaces after it), as Ctrl+Backspace does in a browser's input. It is one undoable edit,
// and the EDIT tells the root through EN_CHANGE.
func (f *textField) deleteWordBack() {
	w32.SendMessage(f.hwnd, w32.EM_GETSEL, uintptr(unsafe.Pointer(&f.sel[0])), uintptr(unsafe.Pointer(&f.sel[1])))
	start, end := int(f.sel[0]), int(f.sel[1])
	if start == end {
		n := w32.GetWindowTextLength(f.hwnd)
		if n+1 > len(f.text) {
			f.text = make([]uint16, n+64)
		}
		n = w32.GetWindowText(f.hwnd, f.text)
		end = min(end, n)
		start = wordStartBefore(f.text[:n], end)
		if start == end {
			return
		}
		w32.SendMessage(f.hwnd, w32.EM_SETSEL, uintptr(start), uintptr(end))
	}
	w32.SendMessage(f.hwnd, w32.EM_REPLACESEL, 1, uintptr(unsafe.Pointer(noText)))
}

// wordStartBefore is where the word before position end of t (UTF-16) starts: back over
// white space, then over a run of word characters (letters, digits, marks, the underscore,
// and characters outside the BMP), or else over a run of other characters (punctuation).
func wordStartBefore(t []uint16, end int) int {
	space := func(c uint16) bool { return unicode.IsSpace(rune(c)) }
	word := func(c uint16) bool {
		r := rune(c)
		return utf16.IsSurrogate(r) || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r)
	}
	i := end
	for i > 0 && space(t[i-1]) {
		i--
	}
	if i > 0 && word(t[i-1]) {
		for i > 0 && word(t[i-1]) {
			i--
		}
		return i
	}
	for i > 0 && !space(t[i-1]) && !word(t[i-1]) {
		i--
	}
	return i
}

// command is a notification the EDIT sent its parent (WM_COMMAND).
func (f *textField) command(code uint16) {
	switch code {
	case w32.EN_CHANGE:
		if f.changed != nil {
			f.changed()
		}
		text := f.Text()
		if f.opts.AutoDirection && f.opts.ResolveDirection(text) != f.dir {
			f.applyAlign() // dir=auto: the typed text's first strong letter turns the field
		}
		if empty := text == ""; empty != f.empty {
			f.empty = empty
			// The EDIT draws a typed character in place; the placeholder goes, or comes
			// back, with a repaint of the whole field.
			w32.InvalidateRect(f.hwnd, nil, true)
		}
		if f.setting || f.cb.Changed == nil {
			return
		}
		f.w.call(func() { f.cb.Changed(text) })
	case w32.EN_SETFOCUS, w32.EN_KILLFOCUS:
		if f.cb.Focused == nil {
			return
		}
		in := code == w32.EN_SETFOCUS
		f.w.call(func() { f.cb.Focused(in) })
	}
}

// ctlColor answers WM_CTLCOLOREDIT: the text's colour and the field's ground.
func (f *textField) ctlColor(dc windows.Handle) uintptr {
	w32.SetTextColor(dc, colorRef(f.opts.Color))
	w32.SetBkColor(dc, f.brushRef)
	return uintptr(f.brush)
}

// applyFont makes the GDI font of the style at the window's DPI and gives it to the EDIT,
// which then needs its margins again (WM_SETFONT resets them).
func (f *textField) applyFont() {
	st := f.opts.Style
	size := st.Size
	if !(size > 0) {
		size = 13
	}
	weight := int32(st.Weight)
	if weight <= 0 {
		weight = w32.FW_NORMAL
	}
	k := fontKey{family: f.w.app.res.resolveFamily(st.Family), px: int32(math.Round(float64(size * f.w.ppd()))), weight: weight}
	if k == f.fontKey && f.font != 0 {
		return
	}
	face, err := windows.UTF16PtrFromString(k.family)
	if err != nil {
		return
	}
	font := w32.CreateFont(k.px, k.weight, face)
	if font == 0 {
		return
	}
	old := f.font
	f.face, f.font, f.fontKey = face, font, k
	f.tm, _ = w32.FontMetrics(font)
	w32.SendMessage(f.hwnd, w32.WM_SETFONT, uintptr(font), 1)
	w32.SendMessage(f.hwnd, w32.EM_SETMARGINS, w32.EC_LEFTMARGIN|w32.EC_RIGHTMARGIN, 0)
	if old != 0 {
		w32.DeleteObject(old)
	}
}

// applyColors makes the ground's brush and repaints the field.
func (f *textField) applyColors() {
	ref := colorRef(f.opts.Background)
	if f.brush == 0 || ref != f.brushRef {
		if f.brush != 0 {
			w32.DeleteObject(f.brush)
		}
		f.brush, f.brushRef = w32.CreateSolidBrush(ref), ref
	}
	w32.InvalidateRect(f.hwnd, nil, true)
}

// pixelRect is the EDIT's place in the window's client area, in pixels: the text area's
// left and right edges, and one line of the font whose baseline is where a browser puts a
// text field's (its line centred in the area).
func (f *textField) pixelRect() w32.Rect {
	ppd := f.w.ppd()
	r := f.bounds
	x0 := int32(math.Round(float64(r.Min.X * ppd)))
	x1 := int32(math.Round(float64(r.Max.X * ppd)))
	baseline := r.Min.Y + r.Height()/2 // without metrics: the middle
	if fm, err := f.w.app.res.FontMetrics(f.opts.Style); err == nil {
		baseline = r.Min.Y + (r.Height()-fm.Ascent-fm.Descent)/2 + fm.Ascent
	}
	top := int32(math.Round(float64(baseline*ppd))) - f.tm.Ascent
	return w32.Rect{Left: x0, Top: top, Right: max(x1, x0), Bottom: top + f.tm.Height}
}

// place moves the EDIT onto its text area, or hides it.
func (f *textField) place() {
	if f.released {
		return
	}
	if f.bounds.Empty() || !finiteRect(f.bounds) {
		if f.shown {
			w32.ShowWindow(f.hwnd, w32.SW_HIDE)
			f.shown = false
		}
		return
	}
	rc := f.pixelRect()
	if f.shown && rc == f.placed {
		return
	}
	flags := uint32(w32.SWP_NOZORDER | w32.SWP_NOACTIVATE)
	if !f.shown {
		flags |= swpShowWindow
		f.shown = true
	}
	f.placed = rc
	w32.SetWindowPos(f.hwnd, rc.Left, rc.Top, rc.Right-rc.Left, rc.Bottom-rc.Top, flags)
	// The move erases the EDIT at its new place at once, but its text and placeholder would
	// wait for WM_PAINT, after the compositor has shown the blank field (a live resize's
	// flicker): paint it now.
	w32.UpdateWindow(f.hwnd)
}

// swpShowWindow is SWP_SHOWWINDOW.
const swpShowWindow = 0x0040

// dpiChanged follows a new DPI: the font is in pixels, and so is the EDIT's place.
func (f *textField) dpiChanged() {
	if f.released {
		return
	}
	f.applyFont()
	f.releaseTarget() // its DPI is the old one
	f.place()
}

// WM_PRINTCLIENT and its PRF_ flags (winuser.h).
const (
	wmPrintClient = 0x0318
	prfClient     = 0x0004
	prfErase      = 0x0008
)

// paintPlaceholder draws the placeholder over the empty field with DirectWrite, after the
// EDIT has painted itself: the field's ground, then the text where the EDIT puts its own.
// It draws into the EDIT's own device context, or into into (a WM_PRINTCLIENT's) when it is
// not 0.
func (f *textField) paintPlaceholder(into windows.Handle) {
	if f.opts.Placeholder == "" || w32.GetWindowTextLength(f.hwnd) > 0 || f.w.closed {
		return
	}
	res := f.w.app.res
	if f.placeholder == nil {
		l, err := res.LayoutText(f.opts.Placeholder, f.opts.Style, kit.TextOptions{})
		if err != nil {
			return
		}
		f.placeholder = l
	}
	tl, ok := f.placeholder.(*textLayout)
	if !ok || tl.layout == nil {
		return
	}
	if f.dc == nil {
		p := d2d.RenderTargetProperties{
			Type:        d2d.RENDER_TARGET_TYPE_SOFTWARE,
			PixelFormat: d2d.PixelFormat{Format: d2d.DXGI_FORMAT_B8G8R8A8_UNORM, AlphaMode: d2d.ALPHA_MODE_IGNORE},
			DpiX:        96 * f.w.ppd(), DpiY: 96 * f.w.ppd(),
		}
		rt, hr := res.d2d.CreateDCRenderTarget(&p)
		if hr.Failed() {
			return
		}
		black := d2d.ColorF{A: 1}
		b, hr := rt.CreateSolidColorBrush(&black)
		if hr.Failed() {
			rt.Release()
			return
		}
		f.dc, f.dcBrush = rt, b
	}
	rc := w32.GetClientRect(f.hwnd)
	if rc.Right <= 0 || rc.Bottom <= 0 {
		return
	}
	hdc := into
	if hdc == 0 {
		if hdc = w32.GetDC(f.hwnd); hdc == 0 {
			return
		}
		defer w32.ReleaseDC(f.hwnd, hdc)
		w32.HideCaret(f.hwnd)
		defer w32.ShowCaret(f.hwnd)
	}
	f.setRc = d2d.RECT{Right: rc.Right, Bottom: rc.Bottom}
	if f.dc.BindDC(hdc, &f.setRc).Failed() {
		return
	}
	bg := f.opts.Background
	bg.A = 1
	f.dc.BeginDraw()
	f.dc.SetTransform(&d2d.Identity)
	f.dc.Clear((*d2d.ColorF)(unsafe.Pointer(&bg)))
	col := f.opts.PlaceholderColor
	f.dcBrush.SetColor((*d2d.ColorF)(unsafe.Pointer(&col)))
	ppd := f.w.ppd()
	m := tl.Metrics()
	// The GDI text's baseline is the font's ascent below the EDIT's top; so is this one's,
	// and it sits where the EDIT puts its text (its alignment: CSS puts an input's
	// placeholder where its text goes).
	y := float32(f.tm.Ascent)/ppd - m.Baseline
	var x float32
	switch room := float32(rc.Right)/ppd - m.Width; {
	case room <= 0:
	case f.opts.Align == kit.TextAlignCenter:
		x = room / 2
	case f.opts.AlignsRightAt(f.dir):
		x = room
	}
	f.dc.DrawTextLayout(x, y, &tl.layout.Object, &f.dcBrush.Brush, d2d.DRAW_TEXT_OPTIONS_ENABLE_COLOR_FONT)
	if hr := f.dc.EndDraw(); hr == com.D2DERR_RECREATE_TARGET {
		f.releaseTarget()
	}
}

func (f *textField) releaseTarget() {
	com.Release(&f.dcBrush)
	com.Release(&f.dc)
}

// ---- kit.NativeField ----

// SetBounds implements kit.NativeField.
func (f *textField) SetBounds(r kit.Rect) {
	if f.released {
		return
	}
	f.bounds = r
	f.place()
}

// SetOptions implements kit.NativeField.
func (f *textField) SetOptions(o kit.TextFieldOptions) {
	if f.released || o == f.opts {
		return
	}
	style := o.Style != f.opts.Style
	if style || o.Placeholder != f.opts.Placeholder || o.Align != f.opts.Align || o.Direction != f.opts.Direction {
		if f.placeholder != nil {
			f.placeholder.Release()
			f.placeholder = nil
		}
	}
	f.opts = o
	if style {
		f.applyFont()
		f.place()
	}
	f.applyAlign()
	f.applyColors()
}

// Text implements kit.NativeField.
func (f *textField) Text() string {
	if f.released {
		return ""
	}
	n := w32.GetWindowTextLength(f.hwnd)
	if n+1 > len(f.text) {
		f.text = make([]uint16, n+64)
	}
	n = w32.GetWindowText(f.hwnd, f.text)
	return f.value(string(utf16.Decode(f.text[:n])))
}

// editText is s, a value, as the EDIT holds it: in a multi-line EDIT its line breaks (LF,
// CR LF or a lone CR, which a textarea's value takes alike) are the CR LF the EDIT breaks
// lines at, since it shows a bare LF as nothing and runs the lines together; a one-line
// field drops them, as an input's value does. NULs, which would end the text, go.
func (f *textField) editText(s string) string {
	if strings.IndexByte(s, 0) >= 0 {
		s = stripNUL(s)
	}
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	if !f.multi {
		return strings.NewReplacer("\r", "", "\n", "").Replace(s)
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

// value is the EDIT's text as the field's value: a multi-line EDIT's CR LF line breaks are
// LF, as a textarea's value has them.
func (f *textField) value(s string) string {
	if f.multi {
		return strings.ReplaceAll(s, "\r\n", "\n")
	}
	return s
}

// SetText implements kit.NativeField: the value goes in with editText's line breaks.
func (f *textField) SetText(s string) {
	if f.released {
		return
	}
	p, err := windows.UTF16PtrFromString(f.editText(s))
	if err != nil {
		return
	}
	f.set = p
	f.setting = true
	w32.SetWindowText(f.hwnd, f.set)
	f.setting = false
	if f.opts.AutoDirection {
		f.applyAlign() // dir=auto: the new value's direction
	}
	if w32.GetFocus() == f.hwnd {
		// While the user edits (a field that clears itself, a suggestion taken), the caret
		// goes to the end, as a browser's does when a script sets an input's value.
		n := uintptr(w32.GetWindowTextLength(f.hwnd))
		w32.SendMessage(f.hwnd, w32.EM_SETSEL, n, n)
		return
	}
	// A field without the focus shows its value from the start, as a browser's input does
	// (a prefilled file name or folder). The caret at the end would scroll an
	// ES_AUTOHSCROLL EDIT to its end, still 0 px wide before its first layout, and it keeps
	// that scroll once sized: "ıdget-plan.xlsx" for budget-plan.xlsx.
	w32.SendMessage(f.hwnd, w32.EM_SETSEL, 0, 0)
	w32.SendMessage(f.hwnd, w32.EM_SCROLLCARET, 0, 0)
}

// Focus implements kit.NativeField.
func (f *textField) Focus() {
	if !f.released && w32.GetFocus() != f.hwnd {
		w32.SetFocus(f.hwnd)
	}
}

// Blur implements kit.NativeField.
func (f *textField) Blur() {
	if !f.released && w32.GetFocus() == f.hwnd {
		w32.SetFocus(f.w.hwnd)
	}
}

// SelectAll implements kit.NativeField.
func (f *textField) SelectAll() {
	if !f.released {
		w32.SendMessage(f.hwnd, w32.EM_SETSEL, 0, ^uintptr(0))
	}
}

// Release implements kit.NativeField: it destroys the EDIT and frees what the field holds.
func (f *textField) Release() {
	if f.released {
		return
	}
	f.Blur()
	h := f.hwnd
	w32.DestroyWindow(h) // WM_NCDESTROY forgets it
	f.forget()
}

// forget frees the field once its window is gone (destroyed by Release, or with its
// parent).
func (f *textField) forget() {
	if f.released {
		return
	}
	f.released = true
	w32.SetWindowLongPtr(f.hwnd, w32.GWLP_WNDPROC, f.old)
	fieldTable.Lock()
	delete(fieldTable.byHWND, f.hwnd)
	fieldTable.Unlock()
	for i, x := range f.w.fields {
		if x == f {
			f.w.fields = append(f.w.fields[:i], f.w.fields[i+1:]...)
			break
		}
	}
	if f.placeholder != nil {
		f.placeholder.Release()
		f.placeholder = nil
	}
	f.releaseTarget()
	if f.font != 0 {
		w32.DeleteObject(f.font)
		f.font = 0
	}
	if f.brush != 0 {
		w32.DeleteObject(f.brush)
		f.brush = 0
	}
}

var _ kit.NativeField = (*textField)(nil)
