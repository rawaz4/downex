//go:build windows

package win

import (
	"fmt"
	"math"
	"unsafe"

	"downex/ui/kit"
	"downex/ui/kit/hostcap"
	"downex/ui/win/internal/w32"

	"golang.org/x/sys/windows"
)

// The native fields' kinds (hostcap.NativeFieldKinds) and their caret and selection
// (hostcap.FieldControl): the EDIT of field.go, made a password field with the system's mark
// (EM_SETPASSWORDCHAR, which sets ES_PASSWORD; input methods off in it, as a browser's
// password input has them), or a multi-line EDIT (ES_MULTILINE) that wraps at its width and
// scrolls vertically. Every kind keeps field.go's subclass: the root sees each key first, the
// pointer's moves reach it, and the placeholder is drawn with DirectWrite.

var (
	_ hostcap.NativeFieldKinds = (*Window)(nil)
	_ hostcap.FieldControl     = (*textField)(nil)
	_ hostcap.FieldControl     = (*areaField)(nil)
	_ hostcap.FieldReclaimer   = (*areaField)(nil)
)

// passwordMark is the system's password character in a Common Controls 6 EDIT: U+25CF.
const passwordMark = 0x25CF

// NewField implements hostcap.NativeFieldKinds.
func (w *Window) NewField(o kit.TextFieldOptions, k hostcap.FieldOptions, cb kit.TextFieldCallbacks) (kit.NativeField, error) {
	switch k.Kind {
	case hostcap.FieldMultiline:
		a, err := w.newAreaField(o, cb)
		if err != nil {
			return nil, err
		}
		if k.MaxLength > 0 {
			a.SetMaxLength(k.MaxLength)
		}
		return a, nil
	case hostcap.FieldText, hostcap.FieldPassword:
		nf, err := w.NewTextField(o, cb)
		if err != nil {
			return nil, err
		}
		f := nf.(*textField)
		if k.MaxLength > 0 {
			f.SetMaxLength(k.MaxLength)
		}
		if k.Kind == hostcap.FieldPassword {
			f.SetPassword(true)
		}
		return f, nil
	}
	return nil, fmt.Errorf("win: field kind %d: %w", k.Kind, kit.ErrUnsupported)
}

// Selection implements hostcap.FieldControl (EM_GETSEL: UTF-16 offsets).
func (f *textField) Selection() (start, end int) {
	if f.released {
		return 0, 0
	}
	w32.SendMessage(f.hwnd, w32.EM_GETSEL, uintptr(unsafe.Pointer(&f.sel[0])), uintptr(unsafe.Pointer(&f.sel[1])))
	return int(f.sel[0]), int(f.sel[1])
}

// SetSelection implements hostcap.FieldControl.
func (f *textField) SetSelection(start, end int) {
	if f.released {
		return
	}
	n := w32.GetWindowTextLength(f.hwnd)
	start, end = min(max(start, 0), n), min(max(end, 0), n)
	w32.SendMessage(f.hwnd, w32.EM_SETSEL, uintptr(start), uintptr(end))
}

// ReplaceSelection implements hostcap.FieldControl: one undoable edit, as typing makes (its
// line breaks as editText gives them); the EDIT tells the root through EN_CHANGE.
func (f *textField) ReplaceSelection(s string) {
	if f.released {
		return
	}
	p, err := windows.UTF16PtrFromString(f.editText(s))
	if err != nil {
		return
	}
	f.set = p
	w32.SendMessage(f.hwnd, w32.EM_REPLACESEL, 1, uintptr(unsafe.Pointer(f.set)))
}

// SetMaxLength implements hostcap.FieldControl (EM_LIMITTEXT; 0 is the EDIT's own limit).
func (f *textField) SetMaxLength(n int) {
	if f.released {
		return
	}
	w32.SendMessage(f.hwnd, popEMLimitText, uintptr(max(n, 0)), 0)
}

// SetPassword implements hostcap.FieldControl.
func (f *textField) SetPassword(on bool) {
	if f.released {
		return
	}
	if on {
		w32.SendMessage(f.hwnd, popEMSetPasswordChar, passwordMark, 0)
		w32.ImmAssociateContextEx(f.hwnd, 0) // no input context: input methods are off
	} else {
		w32.SendMessage(f.hwnd, popEMSetPasswordChar, 0, 0)
		w32.ImmAssociateContextEx(f.hwnd, w32.IACE_DEFAULT)
	}
	w32.InvalidateRect(f.hwnd, nil, true)
}

// areaField is a multi-line field: field.go's field around an ES_MULTILINE EDIT, placed over
// the whole text area (not on one centred line).
type areaField struct {
	*textField
	hadFocus bool // it had the focus when its window lost the activation
	bar      bool // its vertical scroll bar is shown
}

var areasOf = map[*Window][]*areaField{}

func (w *Window) newAreaField(o kit.TextFieldOptions, cb kit.TextFieldCallbacks) (*areaField, error) {
	if w.closed {
		return nil, fmt.Errorf("win: window closed: %w", kit.ErrClosed)
	}
	fieldProcOnce.Do(func() {
		fieldProcPtr = w32.NewCallback(fieldProc)
		fieldTable.byHWND = map[w32.HWND]*textField{}
	})
	f := &textField{w: w, opts: o, cb: cb, empty: true, multi: true, text: make([]uint16, 1024)}
	// No WS_VSCROLL: the bar shows only while the text overflows, as a textarea's
	// (overflow: auto; fitBar).
	h, err := w32.CreateWindowEx(0, editClass, noText, popWSChild|popESMultiline|popESAutoVScroll|popESWantReturn,
		0, 0, 0, 0, w.hwnd, w.app.inst, 0)
	if err != nil {
		return nil, fmt.Errorf("win: native multi-line field: %w", err)
	}
	f.hwnd = h
	fieldTable.Lock()
	fieldTable.byHWND[h] = f
	fieldTable.Unlock()
	f.old = w32.SetWindowLongPtr(h, w32.GWLP_WNDPROC, fieldProcPtr)
	w.fields = append(w.fields, f)
	f.applyFont()
	f.applyColors()
	a := &areaField{textField: f}
	f.changed = a.fitBar
	areasOf[w] = append(areasOf[w], a)
	return a, nil
}

// fitBar shows the vertical scroll bar while the lines overflow the field and hides it once
// they fit, as a textarea's overflow: auto does. The lines are counted at the width the text
// has now: with the bar, the narrower one.
func (a *areaField) fitBar() {
	f := a.textField
	if f.released || f.tm.Height <= 0 {
		return
	}
	var r w32.Rect
	w32.SendMessage(f.hwnd, w32.EM_GETRECT, 0, uintptr(unsafe.Pointer(&r)))
	visible := (r.Bottom - r.Top) / f.tm.Height
	if r.Bottom <= r.Top {
		visible = 0
	}
	lines := int32(w32.SendMessage(f.hwnd, w32.EM_GETLINECOUNT, 0, 0))
	need := lines > max(visible, 1)
	if need == a.bar {
		return
	}
	// Stable either way: shown, the bar narrows the text, which then wraps into as many lines
	// or more (still too many); hidden, the wider text wraps into as few or fewer.
	a.bar = need
	w32.ShowScrollBar(f.hwnd, w32.SB_VERT, need)
}

// SetText implements kit.NativeField (field.go's, which gives the EDIT the value's line
// breaks as CR LF; a multi-line EDIT sends no EN_CHANGE for WM_SETTEXT, so the bar is fitted
// here). Text, field.go's too, gives them back as LF.
func (a *areaField) SetText(s string) {
	a.textField.SetText(s)
	a.fitBar()
}

// SetBounds implements kit.NativeField: the EDIT fills the whole text area.
func (a *areaField) SetBounds(r kit.Rect) {
	f := a.textField
	if f.released {
		return
	}
	f.bounds = r
	a.placeArea()
}

func (a *areaField) placeArea() {
	f := a.textField
	r := f.bounds
	if r.Empty() || !finiteRect(r) {
		if f.shown {
			w32.ShowWindow(f.hwnd, w32.SW_HIDE)
			f.shown = false
		}
		return
	}
	ppd := f.w.ppd()
	rc := w32.Rect{
		Left: int32(math.Round(float64(r.Min.X * ppd))), Top: int32(math.Round(float64(r.Min.Y * ppd))),
		Right: int32(math.Round(float64(r.Max.X * ppd))), Bottom: int32(math.Round(float64(r.Max.Y * ppd))),
	}
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
	a.fitBar() // a new size wraps the lines again
	w32.UpdateWindow(f.hwnd)
}

// SetOptions implements kit.NativeField (field.go's, then the whole area again).
func (a *areaField) SetOptions(o kit.TextFieldOptions) {
	a.textField.SetOptions(o)
	a.textField.placed = w32.Rect{}
	a.placeArea()
}

// Release implements kit.NativeField.
func (a *areaField) Release() {
	list := areasOf[a.w]
	for i, x := range list {
		if x == a {
			areasOf[a.w] = append(list[:i:i], list[i+1:]...)
			break
		}
	}
	if len(areasOf[a.w]) == 0 {
		delete(areasOf, a.w)
	}
	a.textField.Release()
}

// Reclaim implements hostcap.FieldReclaimer: the window is active again and the field had the
// focus when it lost the activation.
func (a *areaField) Reclaim() {
	f := a.textField
	if f.released || !a.hadFocus {
		return
	}
	if getForegroundWindow() != f.w.hwnd {
		return
	}
	a.hadFocus = false
	if w32.GetFocus() != f.hwnd {
		w32.SetFocus(f.hwnd)
	}
}

// areasLostActivation marks w's multi-line field that has the focus as the one to take it back
// (the hook sees WM_ACTIVATE before the focus moves).
func (a *App) areasLostActivation(w *Window) {
	focus := w32.GetFocus()
	for _, f := range areasOf[w] {
		f.hadFocus = f.hwnd == focus
	}
}
