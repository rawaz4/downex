//go:build darwin && cgo

package mac

// [fields] The window's native text fields: kit.NativeFields, kit.NativeFieldKinds,
// kit.FieldControl and kit.FieldReclaimer over AppKit's own fields (field_darwin.m).
//
// A single-line field is an NSTextField (an NSSecureTextField for a password) on the
// window's content view, whose cell has no insets: its view is the text area SetBounds
// gives, rounded to device pixels. The cell draws the ground, and the text or placeholder
// while the field is not edited, with the kit's own text engine at the baseline the kit
// draws the field's stand-in text on, so nothing moves when the kit covers the field; the
// field editor (the field's own TextKit 1 view, or the system's secure one) puts its line
// at that baseline too. A multi-line field is an NSTextView in an NSScrollView over the
// whole text area, with the kit's line boxes. Typing, input methods, the caret, selection,
// undo (one undo history per field), the clipboard and the context menu are AppKit's.
//
// Keys: while a field has the focus, an application-local event monitor hands each key to
// the root first (unless an input method composes with it) and a key the root uses never
// reaches the field; Enter, Escape and Tab that the root leaves do nothing in a single-line
// field. Command keys the root leaves go on to the menu (Edit: Cut, Copy, Paste, Select
// All, Undo, Redo); without a menu item for them the monitor does what the item would.

/*
#include <stdlib.h>
#include <CoreText/CoreText.h>
#include "field_darwin.h"
*/
import "C"

import (
	"fmt"
	"runtime/cgo"
	"strings"
	"unsafe"

	"downex/ui/kit"
)

// macField is one native field of a window.
type macField struct {
	w      *Window
	handle cgo.Handle
	st     unsafe.Pointer // the DXFState, +1
	kind   kit.FieldKind
	opts   kit.TextFieldOptions
	cb     kit.TextFieldCallbacks
	lineK  float32 // FieldOptions.LineHeight
	bounds kit.Rect
	dir    kit.Direction
	asc    float32 // the font's ascent and descent as the kit rounds them
	desc   float32
	placed kit.Rect // where place last put the view (device-pixel rounded), empty: hidden

	setting  bool
	released bool
}

// fldFields are each window's fields, so that they go with it.
var fldFields = map[*Window][]*macField{}

var (
	_ kit.NativeFields     = (*Window)(nil)
	_ kit.NativeFieldKinds = (*Window)(nil)
	_ kit.FieldControl     = (*macField)(nil)
	_ kit.FieldReclaimer   = (*macField)(nil)
)

// NewTextField implements kit.NativeFields.
func (w *Window) NewTextField(opts kit.TextFieldOptions, cb kit.TextFieldCallbacks) (kit.NativeField, error) {
	return w.NewField(opts, kit.FieldOptions{Kind: kit.FieldText}, cb)
}

// NewField implements kit.NativeFieldKinds.
func (w *Window) NewField(o kit.TextFieldOptions, k kit.FieldOptions, cb kit.TextFieldCallbacks) (kit.NativeField, error) {
	if w.closed {
		return nil, fmt.Errorf("mac: window closed: %w", kit.ErrClosed)
	}
	var kind C.int
	switch k.Kind {
	case kit.FieldText:
		kind = C.DXF_TEXT
	case kit.FieldPassword:
		kind = C.DXF_PASSWORD
	case kit.FieldMultiline:
		kind = C.DXF_MULTI
	default:
		return nil, fmt.Errorf("mac: field kind %d: %w", k.Kind, kit.ErrUnsupported)
	}
	f := &macField{w: w, kind: k.Kind, opts: o, cb: cb, lineK: k.LineHeight}
	f.handle = cgo.NewHandle(f)
	f.st = C.dxfNew(w.ns, C.uintptr_t(f.handle), kind)
	if f.st == nil {
		f.handle.Delete()
		return nil, fmt.Errorf("mac: cannot make a native field")
	}
	fldFields[w] = append(fldFields[w], f)
	if k.MaxLength > 0 {
		C.dxfSetMaxLength(f.st, C.long(k.MaxLength))
	}
	f.applyStyle()
	return f, nil
}

// fieldOf finds the field of a handle; nil once it is released.
func fieldOf(h C.uintptr_t) *macField {
	if h == 0 {
		return nil
	}
	f, _ := cgo.Handle(h).Value().(*macField)
	if f == nil || f.released || f.w.closed {
		return nil
	}
	return f
}

func (f *macField) dead() bool { return f.released || f.w.closed }

// style is the field's font: a multi-line field's line height is FieldOptions.LineHeight
// when its style has none (textarea's line-height: 1.55).
func (f *macField) style() kit.TextStyle {
	s := f.opts.Style
	if !(s.Size > 0) {
		s.Size = 13
	}
	if f.kind == kit.FieldMultiline && s.LineHeight == 0 && f.lineK > 0 {
		s.LineHeight = f.lineK
	}
	return s
}

// applyStyle gives the views the font, colours, placeholder, name, alignment and line box.
func (f *macField) applyStyle() {
	var font C.CTFontRef
	var spacing float32
	var fm kit.FontMetrics
	if e, err := f.w.app.res.engine(); err == nil {
		if s, err := normStyle(f.style()); err == nil {
			if tf, err := e.format(s); err == nil {
				font, spacing, fm = tf.font, tf.spacing, tf.metrics
			}
		}
	}
	f.asc, f.desc = fm.Ascent, fm.Descent
	lh, lb := fm.Ascent+fm.Descent, fm.Ascent // one line: the font's ascent and descent
	if f.kind == kit.FieldMultiline {
		lh, lb = fm.LineHeight, fm.Baseline
	}
	f.dir = f.opts.ResolveDirection(f.Text())
	col := func(c kit.Color) [4]C.double {
		return [4]C.double{C.double(c.R), C.double(c.G), C.double(c.B), C.double(c.A)}
	}
	fg, bg, ph := col(f.opts.Color), col(f.opts.Background), col(f.opts.PlaceholderColor)
	cp := C.CString(fldUTF8(f.opts.Placeholder))
	cn := C.CString(fldUTF8(f.opts.Name))
	C.dxfSetStyle(f.st, font, C.double(spacing), &fg[0], &bg[0], &ph[0], cp, cn,
		C.int(fldAlign(f.opts, f.dir)), C.bool(f.dir == kit.DirRTL), C.double(lh), C.double(lb))
	C.free(unsafe.Pointer(cp))
	C.free(unsafe.Pointer(cn))
	f.place()
}

// applyAlign follows dir=auto: the text's first strong character turns the field.
func (f *macField) applyAlign() {
	dir := f.opts.ResolveDirection(f.Text())
	if dir == f.dir {
		return
	}
	f.dir = dir
	C.dxfSetAlign(f.st, C.int(fldAlign(f.opts, dir)), C.bool(dir == kit.DirRTL))
}

// place puts the view on the text area, or hides it, at the window's device pixels; the
// window's accessibility hears when a field shows, hides or moves.
func (f *macField) place() {
	if f.dead() {
		return
	}
	var r kit.Rect // empty: hidden
	if fldShown(f.bounds) {
		r = fldRound(f.bounds, f.w.ppd)
		base := fldBaseline(f.bounds, f.asc, f.desc, f.w.ppd) - r.Min.Y
		C.dxfSetFrame(f.st, C.double(r.Min.X), C.double(r.Min.Y), C.double(r.Width()), C.double(r.Height()), C.double(base), false)
	} else {
		C.dxfSetFrame(f.st, 0, 0, 0, 0, 0, true)
	}
	if r != f.placed {
		f.placed = r
		axFieldsChanged(f.w) // [ax] VoiceOver reads the content again
	}
}

// nativeFrame is where the field's view is (content points, y down) and whether it is
// hidden: for the tests.
func (f *macField) nativeFrame() (r kit.Rect, hidden bool) {
	if f.dead() {
		return kit.Rect{}, true
	}
	var x, y, w, h C.double
	hidden = bool(C.dxfFrame(f.st, &x, &y, &w, &h))
	return kit.R(float32(x), float32(y), float32(x+w), float32(y+h)), hidden
}

// ---- kit.NativeField ----

// SetBounds implements kit.NativeField.
func (f *macField) SetBounds(r kit.Rect) {
	if f.dead() {
		return
	}
	f.bounds = r
	f.place()
}

// SetOptions implements kit.NativeField.
func (f *macField) SetOptions(o kit.TextFieldOptions) {
	if f.dead() || o == f.opts {
		return
	}
	f.opts = o
	f.applyStyle()
}

// Text implements kit.NativeField.
func (f *macField) Text() string {
	if f.dead() {
		return ""
	}
	p := C.dxfText(f.st)
	s := C.GoString(p)
	C.free(unsafe.Pointer(p))
	if f.kind == kit.FieldMultiline && strings.IndexByte(s, '\r') >= 0 {
		s = fldValue(s, true)
	}
	return s
}

// SetText implements kit.NativeField: no Changed, and the field's undo starts again.
func (f *macField) SetText(s string) {
	if f.dead() {
		return
	}
	cs := C.CString(fldValue(s, f.kind == kit.FieldMultiline))
	f.setting = true
	C.dxfSetText(f.st, cs)
	f.setting = false
	C.free(unsafe.Pointer(cs))
	if f.opts.AutoDirection {
		f.applyAlign()
	}
}

// Focus implements kit.NativeField.
func (f *macField) Focus() {
	if !f.dead() {
		C.dxfFocus(f.st)
	}
}

// Blur implements kit.NativeField: the window's content view takes the focus back.
func (f *macField) Blur() {
	if !f.dead() {
		C.dxfBlur(f.st)
	}
}

// SelectAll implements kit.NativeField.
func (f *macField) SelectAll() {
	if !f.dead() {
		C.dxfSelectAll(f.st)
	}
}

// Release implements kit.NativeField.
func (f *macField) Release() {
	if f.released {
		return
	}
	if !f.w.closed {
		f.Blur()
	}
	f.forget()
}

func (f *macField) forget() {
	if f.released {
		return
	}
	f.released = true
	C.dxfRelease(f.st)
	f.st = nil
	f.handle.Delete()
	list := fldFields[f.w]
	for i, x := range list {
		if x == f {
			fldFields[f.w] = append(list[:i:i], list[i+1:]...)
			break
		}
	}
	if len(fldFields[f.w]) == 0 {
		delete(fldFields, f.w)
	}
	if !f.placed.Empty() && !f.w.closed {
		axFieldsChanged(f.w) // [ax] a shown field went
	}
}

// ---- kit.FieldControl, kit.FieldReclaimer ----

// Selection implements kit.FieldControl (UTF-16 offsets).
func (f *macField) Selection() (start, end int) {
	if f.dead() {
		return 0, 0
	}
	var a, b C.long
	C.dxfSelection(f.st, &a, &b)
	return int(a), int(b)
}

// SetSelection implements kit.FieldControl.
func (f *macField) SetSelection(start, end int) {
	if !f.dead() {
		C.dxfSetSelection(f.st, C.long(start), C.long(end))
	}
}

// ReplaceSelection implements kit.FieldControl: one undoable edit, as typing makes; the
// Changed callback follows (queued: it runs after the root's call that made the edit).
func (f *macField) ReplaceSelection(s string) {
	if f.dead() {
		return
	}
	cs := C.CString(fldValue(s, f.kind == kit.FieldMultiline))
	C.dxfReplaceSelection(f.st, cs)
	C.free(unsafe.Pointer(cs))
}

// SetMaxLength implements kit.FieldControl.
func (f *macField) SetMaxLength(n int) {
	if !f.dead() {
		C.dxfSetMaxLength(f.st, C.long(max(n, 0)))
	}
}

// SetPassword implements kit.FieldControl: an NSSecureTextField in place of the field
// (the system's mark, secure input: no input methods, no copying), or a plain one again.
func (f *macField) SetPassword(on bool) {
	if f.dead() || f.kind == kit.FieldMultiline {
		return
	}
	if on {
		f.kind = kit.FieldPassword
	} else {
		f.kind = kit.FieldText
	}
	C.dxfSetPassword(f.st, C.bool(on))
}

// Reclaim implements kit.FieldReclaimer: a multi-line field that had the focus when its
// window resigned key takes it back once the window is key again.
func (f *macField) Reclaim() {
	if !f.dead() && f.kind == kit.FieldMultiline {
		C.dxfReclaim(f.st)
	}
}

// ---- the window's side ----

// fldRescale places the window's fields again on its device pixels: its backing scale
// changed (another screen), and the kit calls SetBounds only when a DIP rectangle changes.
func (w *Window) fldRescale() {
	for _, f := range fldFields[w] {
		f.place()
	}
}

// fldWindowClosing releases the window's fields: it is closing.
func (w *Window) fldWindowClosing() {
	for _, f := range append([]*macField(nil), fldFields[w]...) {
		f.forget()
	}
	delete(fldFields, w)
	C.dxfWindowClosing(w.ns)
}

// fldSetTextInput is kit.Host.SetTextInput: the input methods compose for the root's own
// text input while it is active, at its caret.
func (w *Window) fldSetTextInput(t kit.TextInput) {
	if w.closed {
		return
	}
	c := t.Caret
	C.dxfSetRootTextInput(w.ns, C.bool(t.Active), C.double(c.Min.X), C.double(c.Min.Y), C.double(c.Width()), C.double(c.Height()))
}

//export goFldChanged
func goFldChanged(h C.uintptr_t) {
	f := fieldOf(h)
	if f == nil {
		return
	}
	if f.opts.AutoDirection {
		f.applyAlign()
	}
	if f.setting || f.cb.Changed == nil {
		return
	}
	text := f.Text()
	cb := f.cb.Changed
	f.w.call(func() { cb(text) })
}

//export goFldFocus
func goFldFocus(h C.uintptr_t, in bool) {
	f := fieldOf(h)
	if f == nil || f.cb.Focused == nil {
		return
	}
	cb := f.cb.Focused
	f.w.call(func() { cb(in) })
}

//export goFldKey
func goFldKey(h C.uintptr_t, down bool, code C.ushort, ch C.ushort, mods C.int, repeat bool) bool {
	f := fieldOf(h)
	if f == nil {
		return false
	}
	// Inside a call into the root (a modal loop), the key is queued for the root and the
	// field acts on it now: w.event reports a queued event as not used.
	return goWindowKey(C.uintptr_t(f.w.handle), down, code, ch, mods, repeat)
}

//export goFldFlags
func goFldFlags(h C.uintptr_t, code C.ushort, mods C.int) {
	if f := fieldOf(h); f != nil {
		goWindowFlags(C.uintptr_t(f.w.handle), code, mods)
	}
}
