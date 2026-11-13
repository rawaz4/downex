//go:build darwin && cgo

package mac

// [ax] The window's side of NSAccessibility: a window's bridge (package ui/mac/ax) is made
// the first time assistive technology asks the content view for its children, its hit
// test or its focused element, so a window nobody reads keeps none, and the kit keeps no
// lasting row ids for it. The bridge's events follow the root's NotifySemantics, and it is
// closed when the window closes, before its root's last call (Detach).
//
// The hooks other parts of the backend call (all on the main thread):
//
// [popups] A popup window (a menu, a Select list) is an axHost of its own: it implements
// axSemantics (its root's tree, nil once closed), axPerform/axPost (calls into its root,
// as Window's), axView (its panel's content view), axGone (true once it is closing), and,
// when its root is drawn shifted in its content view, axOrigin (axShifted). Then:
//   - axRegister(p) when its panel is made (cheap: nothing is read until a client asks);
//   - axNotifyHost(p, e) from its NotifySemantics;
//   - axPopupOpened(owner, p) once it is shown, axPopupClosed(p) when it is hidden: while
//     it is open the owner's focused element is the popup's active item (a non-activating
//     panel never becomes key, so VoiceOver would not follow its focus by itself);
//   - axCloseHost(p) when it closes, before its root's Detach (every element is destroyed;
//     the popup is never registered again, since axGone is then true);
//   - its content view, which is not a DXView, forwards its accessibilityChildren,
//     accessibilityHitTest: and accessibilityFocusedUIElement to dxaxViewChildren,
//     dxaxViewHitTest and dxaxViewFocused (ax_view.h), answering super's when they return
//     nil.
//
// [fields] Native text fields need no registration: the bridge finds the NSTextField, or
// the NSScrollView of an NSTextView (a multi-line field, shown as its text view, AXTextArea,
// as WebKit shows a textarea), placed in a text-field node's box, and shows it in the
// node's place, labelled with the node's name. The fields part calls:
//   - axFieldsChanged(w) when fields were added, removed, shown, hidden or moved (one
//     layout change is posted once the current work is done, however many calls);
//   - nothing for a field's focus or text: AppKit posts those for its own views.
//
// [window] window.go calls axNotify from NotifySemantics and axClosed from closedByAppKit.

/*
#include <stdlib.h>
#include "ax_darwin.h"
*/
import "C"

import (
	"unsafe"

	"downex/ui/kit"
	"downex/ui/mac/ax"
)

// axHost is a window (or popup) whose content view shows a semantic tree.
type axHost interface {
	// axSemantics is the root's tree while the window is open, else nil.
	axSemantics() kit.SemanticTree
	// axPerform does r as a call into the root: now, or after the call in progress
	// (reporting nil).
	axPerform(r kit.ActionRequest) error
	// axPost does r as a call into the root once the current work is done.
	axPost(r kit.ActionRequest) error
	// axView is the content view (an NSView, unretained).
	axView() unsafe.Pointer
	// axGone reports a host that is closing or closed: it is never registered again.
	axGone() bool
}

// axShifted is a host whose root's (0, 0) is not the content view's (0, 0): axOrigin is
// where it is, in the content view's points (y down).
type axShifted interface {
	axOrigin() kit.Point
}

// axWin is the accessibility of one host: its key, and its bridge once a client asked.
type axWin struct {
	key   uintptr
	host  axHost
	view  unsafe.Pointer
	b     *ax.Bridge
	popup *axWin // the popup this host opened, while it is open
	owner *axWin // for a popup: its owner
}

// origin is where the host's root's (0, 0) is in its content view.
func (a *axWin) origin() kit.Point {
	if s, ok := a.host.(axShifted); ok {
		return s.axOrigin()
	}
	return kit.Point{}
}

// Hosts registered and not yet closed. A closed host is in neither map (axCloseHost), so
// nothing here keeps it alive; axGone keeps it from being registered again.
var (
	axWins   = map[uintptr]*axWin{}
	axByHost = map[axHost]*axWin{}
	axNext   uintptr
)

// axRegister ties h's content view to a key, so that the view's accessibility finds h; the
// bridge itself is made at the first request. It returns nil for a host that is closing.
func axRegister(h axHost) *axWin {
	if a := axByHost[h]; a != nil {
		return a
	}
	if h.axGone() {
		return nil
	}
	v := h.axView()
	if v == nil {
		return nil
	}
	axNext++
	a := &axWin{key: axNext, host: h, view: v}
	axWins[a.key] = a
	axByHost[h] = a
	C.dxaxSetViewKey(v, C.uintptr_t(a.key))
	return a
}

// bridge is a's bridge, made now when a client asks first; nil while the root keeps no
// tree.
func (a *axWin) bridge() *ax.Bridge {
	if a.b == nil {
		if a.host.axSemantics() == nil {
			return nil
		}
		a.b = ax.New(axBridgeHost{a}, axSystem{a})
	}
	return a.b
}

// axNotifyHost hands the bridge of h a semantic event: nothing while no client has read h.
func axNotifyHost(h axHost, e kit.SemanticEvent) {
	a := axByHost[h]
	if a == nil || a.b == nil {
		return
	}
	C.dxaxBump()
	a.b.Notify(e)
}

// axCloseHost closes h's accessibility: every element of it is destroyed, and none is
// made again. Call it while h and its root are still whole.
func axCloseHost(h axHost) {
	a := axByHost[h]
	if a == nil {
		return
	}
	delete(axByHost, h)
	delete(axWins, a.key)
	C.dxaxSetViewKey(a.view, 0)
	if a.owner != nil && a.owner.popup == a {
		a.owner.popup = nil
	}
	if a.popup != nil {
		a.popup.owner = nil
		a.popup = nil
	}
	if a.b != nil {
		a.b.Close()
	}
	C.dxaxBump()
}

// axPopupOpened tells the owner's accessibility that popup opened: while it is open the
// owner's focused element is the popup's (a non-activating panel never becomes key, so
// VoiceOver would not follow its focus by itself). When a client reads the owner, the
// popup's focused item is announced as the focus at once.
func axPopupOpened(owner, popup axHost) {
	o := axRegister(owner)
	p := axRegister(popup)
	if o == nil || p == nil {
		return
	}
	o.popup, p.owner = p, o
	if o.b == nil || current == nil {
		return // nobody reads the owner, or the application is going
	}
	current.Post(func() {
		if o.popup != p || p.bridge() == nil {
			return
		}
		st := p.host.axSemantics()
		if st == nil {
			return
		}
		if id := st.Focused(); id != 0 && id != st.Root() {
			p.b.Notify(kit.SemanticEvent{Kind: kit.SemanticFocusChanged, Node: id})
		}
	})
}

// axPopupClosed ends axPopupOpened: VoiceOver's focus returns to the owner's element.
func axPopupClosed(popup axHost) {
	p := axByHost[popup]
	if p == nil || p.owner == nil {
		return
	}
	o := p.owner
	o.popup, p.owner = nil, nil
	if o.b != nil {
		if st := o.host.axSemantics(); st != nil {
			o.b.Notify(kit.SemanticEvent{Kind: kit.SemanticFocusChanged, Node: st.Focused()})
		}
	}
}

// axFieldsChanged tells VoiceOver that h's native fields were added, removed, shown,
// hidden or moved, so that it reads the content again: one layout change, posted once the
// current work is done.
func axFieldsChanged(h axHost) {
	if a := axByHost[h]; a != nil && a.b != nil {
		C.dxaxBump()
		a.b.Relayout()
	}
}

// ---- the Window as an axHost ----------------------------------------------------------

func (w *Window) axSemantics() kit.SemanticTree {
	if w.closed {
		return nil
	}
	return w.root.Semantics()
}

func (w *Window) axPerform(r kit.ActionRequest) error {
	st := w.axSemantics()
	if st == nil {
		return kit.ErrClosed
	}
	ran := false
	var err error
	w.call(func() {
		ran = true
		err = st.Perform(r)
	})
	C.dxaxBump()
	if !ran {
		return nil // queued behind the call in progress (a modal loop the root entered)
	}
	return err
}

func (w *Window) axPost(r kit.ActionRequest) error {
	return w.Post(func() {
		if st := w.axSemantics(); st != nil {
			_ = st.Perform(r) // the client was told it was done; a node gone since is no error to report
			C.dxaxBump()
		}
	})
}

func (w *Window) axGone() bool {
	if w.closed {
		return true
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return !w.postOK // closedByAppKit is closing it
}

func (w *Window) axView() unsafe.Pointer {
	if w.ns == nil {
		return nil
	}
	return C.dxaxContentView(w.ns)
}

// axNotify is the window's NotifySemantics.
func axNotify(w *Window, e kit.SemanticEvent) { axNotifyHost(w, e) }

// axClosed is called when the window closes, before its root's Detach.
func axClosed(w *Window) { axCloseHost(w) }

// popWindow is s without its last window, the slot it leaves cleared. App.progCalls is
// pushed and popped around every call into a root: a pop by reslicing alone left the last
// window called into in the backing array, which kept a closed Window (its root, views,
// state and bridge) reachable until another call overwrote the slot.
func popWindow(s []*Window) []*Window {
	s[len(s)-1] = nil
	return s[:len(s)-1]
}

// ---- the bridge's host and system -----------------------------------------------------

type axBridgeHost struct{ a *axWin }

func (h axBridgeHost) Semantics() kit.SemanticTree       { return h.a.host.axSemantics() }
func (h axBridgeHost) Perform(r kit.ActionRequest) error { return h.a.host.axPerform(r) }
func (h axBridgeHost) Post(r kit.ActionRequest) error    { return h.a.host.axPost(r) }
func (h axBridgeHost) HostedField(n *kit.SemanticNode) ax.Ref {
	if n.Role != kit.RoleTextField || n.Bounds.Empty() {
		return 0
	}
	const slack = 0.5 // DIPs of rounding between the box and the field placed in it
	b := n.Bounds.Add(h.a.origin())
	return ax.Ref(C.dxaxFieldIn(h.a.view, C.double(b.Min.X), C.double(b.Min.Y), C.double(b.Max.X), C.double(b.Max.Y), slack))
}

type axSystem struct{ a *axWin }

func (s axSystem) Container() ax.Ref { return ax.Ref(s.a.view) }
func (s axSystem) NewElement(id kit.NodeID) ax.Ref {
	return ax.Ref(C.dxaxElementNew(C.uintptr_t(s.a.key), C.uint64_t(id), s.a.view))
}
func (s axSystem) Destroy(r ax.Ref)                 { C.dxaxElementDestroy(axPtr(r)) }
func (s axSystem) Post(r ax.Ref, n ax.Notification) { C.dxaxPost(axPtr(r), C.int(n)) }
func (s axSystem) Announce(text string, p ax.Priority) {
	cs := C.CString(text)
	C.dxaxAnnounce(s.a.view, cs, C.int(p))
	C.free(unsafe.Pointer(cs))
}
func (s axSystem) DecorateField(field, parent ax.Ref, n *kit.SemanticNode) {
	label, help, ident := axCString(n.Name), axCString(n.Description), axCString(n.AutomationID)
	C.dxaxFieldDecorate(axPtr(field), axPtr(parent), s.a.view, label, help, ident,
		C.bool(n.States&kit.NodeRequired != 0))
	axFree(label, help, ident)
}
func (s axSystem) Later(f func()) bool {
	if current == nil {
		return false
	}
	current.Post(f)
	return true
}

// axPtr is r as the Objective-C object it is (a Ref holds a pointer kept alive by the
// bridge's own reference, or by the view hierarchy).
func axPtr(r ax.Ref) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&r)) }

func axCString(s string) *C.char {
	if s == "" {
		return nil
	}
	return C.CString(s)
}

func axFree(ps ...*C.char) {
	for _, p := range ps {
		if p != nil {
			C.free(unsafe.Pointer(p))
		}
	}
}

// ---- what the Objective-C side asks ---------------------------------------------------

// axBridgeOf is the bridge of key, made at this first request; nil when there is none.
func axBridgeOf(key C.uintptr_t) *ax.Bridge {
	a := axWins[uintptr(key)]
	if a == nil {
		return nil
	}
	return a.bridge()
}

// axRefs is refs as a malloc'd array for Objective-C (NULL when empty).
func axRefs(refs []ax.Ref, n *C.int) unsafe.Pointer {
	*n = C.int(len(refs))
	if len(refs) == 0 {
		return nil
	}
	p := C.malloc(C.size_t(len(refs)) * C.size_t(unsafe.Sizeof(uintptr(0))))
	s := unsafe.Slice((*uintptr)(p), len(refs))
	for i, r := range refs {
		s[i] = uintptr(r)
	}
	return p
}

//export goAXViewKey
func goAXViewKey(handle C.uintptr_t) C.uintptr_t {
	w := windowOf(handle)
	if w == nil || w.closed {
		return 0
	}
	a := axRegister(w)
	if a == nil {
		return 0
	}
	return C.uintptr_t(a.key)
}

//export goAXRootChildren
func goAXRootChildren(key C.uintptr_t, n *C.int) unsafe.Pointer {
	*n = -1
	b := axBridgeOf(key)
	if b == nil {
		return nil
	}
	return axRefs(b.RootChildren(nil), n)
}

//export goAXList
func goAXList(key C.uintptr_t, id C.uint64_t, kind C.int, n *C.int) unsafe.Pointer {
	*n = 0
	b := axBridgeOf(key)
	if b == nil {
		return nil
	}
	node := kit.NodeID(id)
	var refs []ax.Ref
	switch kind {
	case C.DXAX_CHILDREN:
		refs = b.Children(nil, node)
	case C.DXAX_ROWS:
		refs = b.Rows(nil, node, false)
	case C.DXAX_VISIBLE_ROWS:
		refs = b.Rows(nil, node, true)
	case C.DXAX_SELECTION:
		refs = b.Selection(nil, node)
	case C.DXAX_COLUMN_HEADERS:
		refs = b.ColumnHeaders(nil, node)
	}
	return axRefs(refs, n)
}

//export goAXInfo
func goAXInfo(key C.uintptr_t, id C.uint64_t, out *C.dxaxInfo) C.bool {
	b := axBridgeOf(key)
	if b == nil {
		return false
	}
	in, ok := b.Info(kit.NodeID(id))
	if !ok {
		return false
	}
	bounds := in.Bounds
	if !bounds.Empty() {
		bounds = bounds.Add(axWins[uintptr(key)].origin())
	}
	*out = C.dxaxInfo{
		role: axCString(in.Role), subrole: axCString(in.Subrole), roleDesc: axCString(in.RoleDescription),
		title: axCString(in.Title), label: axCString(in.Label), help: axCString(in.Help), ident: axCString(in.Identifier),
		text: axCString(in.Text), valueDesc: axCString(in.ValueDescription), shortcut: axCString(in.Shortcut),
		mark:      axCString(in.Mark),
		valueKind: C.int(in.Value), number: C.double(in.Number),
		min: C.double(in.Min), max: C.double(in.Max),
		orientation: C.int(in.Orientation), sort: C.int(in.Sort),
		rows: C.int(in.Rows), cols: C.int(in.Cols), index: C.int(in.Index),
		rowLoc: C.int(in.RowLoc), rowLen: C.int(in.RowLen), colLoc: C.int(in.ColLoc), colLen: C.int(in.ColLen),
		posInSet: C.int(in.PosInSet), setSize: C.int(in.SetSize), level: C.int(in.Level),
		actions: C.int(in.Actions), settable: C.int(in.Settable),
		x: C.double(bounds.Min.X), y: C.double(bounds.Min.Y),
		w: C.double(bounds.Width()), h: C.double(bounds.Height()),
	}
	if in.HasRange {
		out.hasRange = 1
	}
	f := 0
	for _, b := range []struct {
		on  bool
		bit int
	}{
		{in.Enabled, C.DXAX_F_ENABLED}, {in.Focused, C.DXAX_F_FOCUSED}, {in.Selected, C.DXAX_F_SELECTED},
		{in.Expandable, C.DXAX_F_EXPANDABLE}, {in.Expanded, C.DXAX_F_EXPANDED}, {in.Required, C.DXAX_F_REQUIRED},
		{in.Protected, C.DXAX_F_PROTECTED}, {in.Invalid, C.DXAX_F_INVALID}, {in.Table, C.DXAX_F_TABLE},
		{in.Row, C.DXAX_F_ROW}, {in.Cell, C.DXAX_F_CELL}, {in.Busy, C.DXAX_F_BUSY}, {in.Modal, C.DXAX_F_MODAL},
		{in.Multiselect, C.DXAX_F_MULTI}, {in.Control, C.DXAX_F_CONTROL}, {in.Checkable, C.DXAX_F_CHECKABLE},
	} {
		if b.on {
			f |= b.bit
		}
	}
	out.flags = C.int(f)
	return true
}

//export goAXParent
func goAXParent(key C.uintptr_t, id C.uint64_t) unsafe.Pointer {
	if b := axBridgeOf(key); b != nil {
		return axPtr(b.Parent(kit.NodeID(id)))
	}
	return nil
}

//export goAXCell
func goAXCell(key C.uintptr_t, id C.uint64_t, col, row C.int) unsafe.Pointer {
	if b := axBridgeOf(key); b != nil {
		return axPtr(b.Cell(kit.NodeID(id), int(col), int(row)))
	}
	return nil
}

//export goAXColumnCount
func goAXColumnCount(key C.uintptr_t, id C.uint64_t) C.int {
	if b := axBridgeOf(key); b != nil {
		return C.int(b.Columns(kit.NodeID(id)))
	}
	return 0
}

//export goAXColumnCells
func goAXColumnCells(key C.uintptr_t, id C.uint64_t, col C.int, n *C.int) unsafe.Pointer {
	*n = 0
	b := axBridgeOf(key)
	if b == nil {
		return nil
	}
	return axRefs(b.ColumnCells(nil, kit.NodeID(id), int(col)), n)
}

//export goAXColumnHeader
func goAXColumnHeader(key C.uintptr_t, id C.uint64_t, col C.int) unsafe.Pointer {
	if b := axBridgeOf(key); b != nil {
		return axPtr(b.ColumnHeader(kit.NodeID(id), int(col)))
	}
	return nil
}

//export goAXHitTest
func goAXHitTest(key C.uintptr_t, x, y C.double) unsafe.Pointer {
	if b := axBridgeOf(key); b != nil {
		o := axWins[uintptr(key)].origin()
		return axPtr(b.HitTest(kit.Pt(float32(x)-o.X, float32(y)-o.Y)))
	}
	return nil
}

//export goAXFocused
func goAXFocused(key C.uintptr_t) unsafe.Pointer {
	a := axWins[uintptr(key)]
	if a == nil {
		return nil
	}
	// While a popup is open its active item has the focus (its panel never becomes key).
	if p := a.popup; p != nil {
		if pb := p.bridge(); pb != nil {
			if r := pb.Focused(); r != 0 {
				return axPtr(r)
			}
		}
	}
	if b := a.bridge(); b != nil {
		return axPtr(b.Focused())
	}
	return nil
}

//export goAXAction
func goAXAction(key C.uintptr_t, id C.uint64_t, act C.int) C.bool {
	b := axBridgeOf(key)
	if b == nil {
		return false
	}
	node := kit.NodeID(id)
	var err error
	switch act {
	case C.DXAX_PRESS:
		err = b.Press(node)
	case C.DXAX_INCREMENT:
		err = b.Step(node, true)
	case C.DXAX_DECREMENT:
		err = b.Step(node, false)
	case C.DXAX_SHOWMENU:
		err = b.ShowMenu(node)
	case C.DXAX_SCROLL:
		err = b.ScrollToVisible(node)
	default:
		return false
	}
	C.dxaxBump()
	return err == nil
}

//export goAXSetBool
func goAXSetBool(key C.uintptr_t, id C.uint64_t, what C.int, on C.bool) C.bool {
	b := axBridgeOf(key)
	if b == nil {
		return false
	}
	node := kit.NodeID(id)
	var err error
	switch what {
	case C.DXAX_SET_FOCUSED:
		if !on {
			return true // nothing to do: the focus moves elsewhere by itself
		}
		err = b.SetFocused(node)
	case C.DXAX_SET_SELECTED:
		err = b.SetSelected(node, bool(on))
	case C.DXAX_SET_EXPANDED:
		err = b.SetExpanded(node, bool(on))
	default:
		return false
	}
	C.dxaxBump()
	return err == nil
}

//export goAXSetText
func goAXSetText(key C.uintptr_t, id C.uint64_t, s *C.char) C.bool {
	b := axBridgeOf(key)
	if b == nil || s == nil {
		return false
	}
	err := b.SetText(kit.NodeID(id), C.GoString(s))
	C.dxaxBump()
	return err == nil
}

//export goAXSetNumber
func goAXSetNumber(key C.uintptr_t, id C.uint64_t, v C.double) C.bool {
	b := axBridgeOf(key)
	if b == nil {
		return false
	}
	err := b.SetNumber(kit.NodeID(id), float64(v))
	C.dxaxBump()
	return err == nil
}

//export goAXSetSelection
func goAXSetSelection(key C.uintptr_t, id C.uint64_t, ids *C.uint64_t, n C.int) C.bool {
	b := axBridgeOf(key)
	if b == nil {
		return false
	}
	var nodes []kit.NodeID
	if n > 0 {
		for _, v := range unsafe.Slice(ids, int(n)) {
			nodes = append(nodes, kit.NodeID(v))
		}
	}
	err := b.SetSelection(kit.NodeID(id), nodes)
	C.dxaxBump()
	return err == nil
}
