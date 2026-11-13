//go:build windows

package win

import (
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"downex/ui/kit"
	"downex/ui/win/internal/com"
	"downex/ui/win/internal/w32"
)

// The window as an OLE drop target (OleIdl.h IDropTarget): a link dragged from a browser,
// text, or files from Explorer. What the drag carries is read as today's WebView2 page saw
// it (DropData); a DropHandler decides what a drop does, as App.svelte's dragover and drop
// handlers do; a hosted text field under the pointer can take the text, as a browser's text
// field takes a drop the page leaves to it.

// DropAction is what a drop at a point does.
type DropAction uint8

const (
	// DropRefuse: nothing happens (DROPEFFECT_NONE; the pointer shows the no-drop cursor).
	DropRefuse DropAction = iota
	// DropCopy: the handler takes the drop (DROPEFFECT_COPY).
	DropCopy
	// DropToField: the text field under the pointer takes the text (DropEvent.Field), as a
	// browser's text field does (DROPEFFECT_COPY).
	DropToField
)

// DropEvent is a drag at a point of the window.
type DropEvent struct {
	// At is the point in the window's client area, in DIPs.
	At kit.Point
	// Data is what the drag carries; the handler must not keep it past the call.
	Data *DropData
	// Field reports an editable hosted text field under the point (one that can take the
	// text: DropToField).
	Field bool
}

// DropHandler decides what drags over a window do. Its methods run on the UI thread during
// the drag, outside the window's calls into its root, as an input message's handler does:
// they answer at once and never enter a modal loop (Drop may open a dialog; the source waits
// for its answer, so it only starts longer work).
type DropHandler interface {
	// DragOver answers what a drop at e would do (it is asked at every move).
	DragOver(e DropEvent) DropAction
	// Drop is the drop at e: it answers what it did.
	Drop(e DropEvent) DropAction
	// DragLeave says that the drag left the window or was cancelled.
	DragLeave()
}

const (
	dropEffectNone = 0
	dropEffectCopy = 1
)

// dropTarget is one window's IDropTarget: a COM object in memory outside the Go heap (OLE
// keeps its pointer), found by its address.
type dropTarget struct {
	w       *Window
	h       DropHandler
	obj     uintptr // the COM object: its vtable pointer, then the reference count
	data    DropData
	entered bool
	fe      formatEtc
	med     stgMedium
	// shortcut is the drag's Internet shortcut, read at DragEnter (dataReader.shortcut).
	shortcut shortcutRead
}

var (
	iidIUnknown    = com.MustGUID("00000000-0000-0000-c000-000000000046")
	iidIDropTarget = com.MustGUID("00000122-0000-0000-c000-000000000046")

	dropVtblOnce sync.Once
	dropVtbl     uintptr
	dropVtblErr  error
	dropTargets  struct {
		sync.Mutex
		by    map[uintptr]*dropTarget // by COM object
		byWin map[*Window]*dropTarget
	}
)

// IDropTarget's slots after IUnknown's.
const (
	slotDTDragEnter = 3
	slotDTDragOver  = 4
	slotDTDragLeave = 5
	slotDTDrop      = 6
	dropMethods     = 7
)

const (
	sOK          = 0
	eNoInterface = 0x80004002
	ePointer     = 0x80004003
	eUnexpected  = 0x8000FFFF
)

func makeDropVtbl() {
	dropTargets.by = map[uintptr]*dropTarget{}
	dropTargets.byWin = map[*Window]*dropTarget{}
	methods := [dropMethods]uintptr{
		syscall.NewCallback(dropQueryInterface),
		syscall.NewCallback(dropAddRef),
		syscall.NewCallback(dropRelease),
	}
	// POINTL travels by value: in one register on 64-bit systems, as two words on 386.
	if unsafe.Sizeof(uintptr(0)) == 8 {
		methods[slotDTDragEnter] = syscall.NewCallback(dropDragEnter64)
		methods[slotDTDragOver] = syscall.NewCallback(dropDragOver64)
		methods[slotDTDrop] = syscall.NewCallback(dropDrop64)
	} else {
		methods[slotDTDragEnter] = syscall.NewCallback(dropDragEnter32)
		methods[slotDTDragOver] = syscall.NewCallback(dropDragOver32)
		methods[slotDTDrop] = syscall.NewCallback(dropDrop32)
	}
	methods[slotDTDragLeave] = syscall.NewCallback(dropDragLeave)
	p, err := windows.LocalAlloc(0x0040, uint32(len(methods))*uint32(unsafe.Sizeof(uintptr(0))))
	if p == 0 {
		dropVtblErr = fmt.Errorf("win: drop target methods: %w", err)
		return
	}
	copy(unsafe.Slice((*uintptr)(com.Addr(p)), len(methods)), methods[:])
	dropVtbl = p
}

func targetOf(this uintptr) *dropTarget {
	dropTargets.Lock()
	defer dropTargets.Unlock()
	return dropTargets.by[this]
}

func refsOf(obj uintptr) *int32 { return (*int32)(com.Addr(obj + unsafe.Sizeof(uintptr(0)))) }

func dropQueryInterface(this, riid, ppv uintptr) uintptr {
	if ppv == 0 {
		return ePointer
	}
	out := (*uintptr)(com.Addr(ppv))
	*out = 0
	if riid == 0 {
		return eNoInterface
	}
	iid := *(*com.GUID)(com.Addr(riid))
	if iid != iidIUnknown && iid != iidIDropTarget {
		return eNoInterface
	}
	atomic.AddInt32(refsOf(this), 1)
	*out = this
	return sOK
}

func dropAddRef(this uintptr) uintptr { return uintptr(atomic.AddInt32(refsOf(this), 1)) }

func dropRelease(this uintptr) uintptr {
	n := atomic.AddInt32(refsOf(this), -1)
	if n == 0 {
		dropTargets.Lock()
		delete(dropTargets.by, this)
		dropTargets.Unlock()
		windows.LocalFree(windows.Handle(this))
	}
	return uintptr(max(n, 0))
}

func dropDragEnter64(this, data, keys, pt, effect uintptr) uintptr {
	return dragEnter(this, data, int32(uint32(pt)), int32(uint32(uint64(pt)>>32)), effect)
}
func dropDragEnter32(this, data, keys, x, y, effect uintptr) uintptr {
	return dragEnter(this, data, int32(x), int32(y), effect)
}
func dropDragOver64(this, keys, pt, effect uintptr) uintptr {
	return dragOver(this, int32(uint32(pt)), int32(uint32(uint64(pt)>>32)), effect)
}
func dropDragOver32(this, keys, x, y, effect uintptr) uintptr {
	return dragOver(this, int32(x), int32(y), effect)
}
func dropDrop64(this, data, keys, pt, effect uintptr) uintptr {
	return drop(this, data, int32(uint32(pt)), int32(uint32(uint64(pt)>>32)), effect)
}
func dropDrop32(this, data, keys, x, y, effect uintptr) uintptr {
	return drop(this, data, int32(x), int32(y), effect)
}

// guard keeps a panic in the handler from unwinding through OLE: the drag is refused, and
// the panic is reported as a window procedure's is (App.fail: Run returns it).
func guard(this, effect uintptr, ret *uintptr) {
	if v := recover(); v != nil {
		if effect != 0 {
			*(*uint32)(com.Addr(effect)) = dropEffectNone
		}
		*ret = sOK
		if t := targetOf(this); t != nil && t.w != nil && t.w.app != nil {
			t.w.recovered() // the handler's call into the window did not end
			t.w.app.failIn(t.w, v, debug.Stack())
		}
	}
}

func dragEnter(this, data uintptr, x, y int32, effect uintptr) (ret uintptr) {
	defer guard(this, effect, &ret)
	t := targetOf(this)
	if t == nil || effect == 0 {
		return eUnexpected
	}
	t.shortcut = shortcutRead{} // a new drag
	t.data = t.read(data)
	t.entered = true
	return t.answer(x, y, effect, false)
}

func dragOver(this uintptr, x, y int32, effect uintptr) (ret uintptr) {
	defer guard(this, effect, &ret)
	t := targetOf(this)
	if t == nil || effect == 0 {
		return eUnexpected
	}
	if !t.entered {
		*(*uint32)(com.Addr(effect)) = dropEffectNone
		return sOK
	}
	return t.answer(x, y, effect, false)
}

func dropDragLeave(this uintptr) (ret uintptr) {
	defer guard(this, 0, &ret)
	t := targetOf(this)
	if t == nil {
		return eUnexpected
	}
	t.leave()
	return sOK
}

func drop(this, data uintptr, x, y int32, effect uintptr) (ret uintptr) {
	defer guard(this, effect, &ret)
	t := targetOf(this)
	if t == nil || effect == 0 {
		return eUnexpected
	}
	t.data = t.read(data) // what is dropped, read again
	ret = t.answer(x, y, effect, true)
	t.entered = false
	t.data, t.shortcut = DropData{}, shortcutRead{}
	return ret
}

func (t *dropTarget) leave() {
	was := t.entered
	t.entered = false
	t.data, t.shortcut = DropData{}, shortcutRead{}
	if was && !t.w.closed {
		t.w.dropCall(func() { t.h.DragLeave() })
	}
}

// read reads the data object at data (an IDataObject pointer).
func (t *dropTarget) read(data uintptr) DropData {
	if data == 0 {
		return DropData{}
	}
	return dataReader{obj: (*com.Object)(com.Addr(data)), fe: &t.fe, med: &t.med, shortcut: &t.shortcut}.read()
}

// answer asks the handler about the screen point (x, y) and writes the effect: a drop when
// dropping.
func (t *dropTarget) answer(x, y int32, effect uintptr, dropping bool) uintptr {
	p := (*uint32)(com.Addr(effect))
	allowed := *p
	w := t.w
	if w.closed || w.hwnd == 0 {
		*p = dropEffectNone
		return sOK
	}
	client := w32.ScreenToClient(w.hwnd, w32.Point{X: x, Y: y})
	field := w.dropField(client)
	ppd := w.ppd()
	e := DropEvent{At: kit.Pt(float32(client.X)/ppd, float32(client.Y)/ppd), Data: &t.data, Field: field != nil}
	act := DropRefuse // also when the window is inside a call into its root (below)
	w.dropCall(func() {
		if dropping {
			act = t.h.Drop(e)
		} else {
			act = t.h.DragOver(e)
		}
	})
	if act == DropToField && (field == nil || fieldText(&t.data) == "") {
		act = DropRefuse
	}
	if act != DropRefuse && allowed&dropEffectCopy == 0 {
		act = DropRefuse // the source does not let its data be copied
	}
	if dropping && act == DropToField {
		field.insertAt(client, fieldText(&t.data))
	}
	if act == DropRefuse {
		*p = dropEffectNone
	} else {
		*p = dropEffectCopy
	}
	return sOK
}

// dropCall runs a drop handler's f as a call into the window, as its other callbacks run
// (Window.call), but only when no call is in progress: OLE's drag loop can deliver a drag
// while the UI thread pumps inside a root call (a modal loop), and a drop then is refused
// rather than run re-entrantly or queued past the drag that asked.
func (w *Window) dropCall(f func()) {
	if w.closed || !w.attached || w.depth > 0 {
		return
	}
	w.depth++
	f()
	w.depth--
	w.drain()
}

// fieldText is the text a text field takes from a drop: the plain text, else the URL.
func fieldText(d *DropData) string {
	if d.Text != "" {
		return d.Text
	}
	return d.URIList
}

// dropField is the editable hosted field under the client point p, or nil.
func (w *Window) dropField(p w32.Point) *textField {
	c := childAt(w.hwnd, p)
	if c == 0 || c == w.hwnd {
		return nil
	}
	f := w.fieldOf(c)
	if f == nil || windowStyleOf(f.hwnd)&esReadOnly != 0 {
		return nil
	}
	return f
}

// insertAt puts text into the field at the character under the window's client point p, as
// a browser's text field takes a drop, with the line breaks the field's editText gives (none
// in a single-line field, CR LF in a multi-line one). The field tells its root of the change
// as typing does.
func (f *textField) insertAt(p w32.Point, text string) {
	u, err := windows.UTF16PtrFromString(f.editText(text))
	if err != nil {
		return
	}
	scr := w32.ClientToScreen(f.w.hwnd, p)
	at := w32.ScreenToClient(f.hwnd, scr)
	pos := w32.SendMessage(f.hwnd, emCharFromPos, 0, uintptr(uint32(uint16(int16(at.X)))|uint32(uint16(int16(at.Y)))<<16))
	idx := uintptr(uint16(pos))
	if pos == ^uintptr(0) || uint32(pos) == 0xFFFFFFFF {
		idx = uintptr(w32.GetWindowTextLength(f.hwnd))
	}
	w32.SendMessage(f.hwnd, emSetSel, idx, idx)
	w32.SendMessage(f.hwnd, emReplaceSel, 1, uintptr(unsafe.Pointer(u)))
}

// EnableDrop makes the window a drop target, with h deciding what drops do (a later call
// replaces h). It is for the UI thread; the window stops being one when it is destroyed or
// DisableDrop is called.
func (w *Window) EnableDrop(h DropHandler) error {
	if h == nil {
		return errInvalid("EnableDrop without a handler")
	}
	if err := w.app.onThread(); err != nil {
		return err
	}
	if w.closed || w.hwnd == 0 {
		return fmt.Errorf("win: window closed: %w", kit.ErrClosed)
	}
	dropVtblOnce.Do(makeDropVtbl)
	if dropVtblErr != nil {
		return dropVtblErr
	}
	dropTargets.Lock()
	if t := dropTargets.byWin[w]; t != nil {
		t.h = h
		dropTargets.Unlock()
		return nil
	}
	dropTargets.Unlock()
	if r, _, _ := syscall.SyscallN(procOleInitialize.Addr(), 0); com.HRESULT(r).Failed() {
		return com.Check("OleInitialize", com.HRESULT(r))
	}
	obj, err := windows.LocalAlloc(0x0040, uint32(unsafe.Sizeof(uintptr(0)))+8)
	if obj == 0 {
		syscall.SyscallN(procOleUninitialize.Addr())
		return fmt.Errorf("win: drop target: %w", err)
	}
	*(*uintptr)(com.Addr(obj)) = dropVtbl
	*refsOf(obj) = 1 // ours, given back by DisableDrop
	t := &dropTarget{w: w, h: h, obj: obj}
	dropTargets.Lock()
	dropTargets.by[obj] = t
	dropTargets.byWin[w] = t
	dropTargets.Unlock()
	if r, _, _ := syscall.SyscallN(procRegisterDragDrop.Addr(), uintptr(w.hwnd), obj); com.HRESULT(r).Failed() {
		w.forgetDrop(t)
		return com.Check("RegisterDragDrop", com.HRESULT(r))
	}
	return nil
}

// DisableDrop stops the window being a drop target. It is for the UI thread.
func (w *Window) DisableDrop() {
	dropTargets.Lock()
	t := dropTargets.byWin[w]
	dropTargets.Unlock()
	if t == nil {
		return
	}
	if w.hwnd != 0 {
		syscall.SyscallN(procRevokeDragDrop.Addr(), uintptr(w.hwnd))
	}
	w.forgetDrop(t)
}

// forgetDrop drops this side's reference of the target and OLE's initialisation for it.
func (w *Window) forgetDrop(t *dropTarget) {
	dropTargets.Lock()
	delete(dropTargets.byWin, w)
	dropTargets.Unlock()
	t.entered = false
	dropRelease(t.obj)
	syscall.SyscallN(procOleUninitialize.Addr())
}

// dropDestroyed is WM_DESTROY's: the window stops being a drop target before it goes, as
// RevokeDragDrop must be called while the window still exists.
func (w *Window) dropDestroyed() {
	dropTargets.Lock()
	t := dropTargets.byWin[w] // nil map before the first EnableDrop: no target
	dropTargets.Unlock()
	if t != nil {
		w.DisableDrop()
	}
}

// dropObject is the window's IDropTarget pointer (tests call its methods).
func (w *Window) dropObject() uintptr {
	dropTargets.Lock()
	defer dropTargets.Unlock()
	if t := dropTargets.byWin[w]; t != nil {
		return t.obj
	}
	return 0
}
