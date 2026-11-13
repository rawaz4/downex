//go:build windows

package uia

import (
	"fmt"
	"sync"
	"sync/atomic"

	"downex/ui/kit"
	"downex/ui/win/uia/internal/comobj"

	"golang.org/x/sys/windows"
)

// Host is the window a Tree serves, as the providers see it. Its methods are called on the
// UI thread.
type Host interface {
	// HWND is the window's handle.
	HWND() uintptr
	// Semantics is the window's semantic tree now, or nil while its root keeps none.
	Semantics() kit.SemanticTree
	// ToScreen maps a rectangle in window DIPs to the screen, in physical pixels.
	ToScreen(r kit.Rect) Rect
	// FromScreen maps a screen point in physical pixels to window DIPs.
	FromScreen(x, y float64) kit.Point
	// Perform does r as a call into the window's root (the kit's never-nest rule): now, or,
	// while another call into the root is in progress (a modal loop the root entered), after
	// it has returned, reporting nil.
	Perform(r kit.ActionRequest) error
	// Post does r as a call into the root once the client's call has returned: an Invoke
	// must not keep its caller waiting (a button may open a dialog).
	Post(r kit.ActionRequest) error
	// HostedWindow is the native child window that n stands for (a hosted text field), or 0:
	// the window's own element is placed at n (IRawElementProviderHwndOverride).
	HostedWindow(n *kit.SemanticNode) uintptr
	// HostedBounds is where a native child window of the window is (window DIPs), or false
	// when hwnd is none of them.
	HostedBounds(hwnd uintptr) (kit.Rect, bool)
	// CustomFrame reports a window whose title bar the root draws: the system's own title
	// bar element (the non-client area's) would show it twice.
	CustomFrame() bool
}

// Rect is a UiaRect: a screen rectangle in physical pixels.
type Rect struct{ Left, Top, Width, Height float64 }

// Stats count what a tree did, for tests and diagnostics.
type Stats struct {
	Calls   int // provider methods served on the UI thread
	Events  int // events raised
	Created int // provider objects made
}

// Tree is the UI Automation provider of one window: the fragment root it hands out for
// WM_GETOBJECT, and the providers of the window's semantic nodes, which it makes when a
// client asks for them and which live while a client holds them. It belongs to the UI
// thread: make it there, and call GetObject, Notify and Close there.
type Tree struct {
	host   Host
	hwnd   uintptr
	thread uint32
	closed bool

	// elems are the providers alive now, by node, and overrides the ones that stand in for
	// a hosted child window (IRawElementProviderHwndOverride); objects.mu guards them (a
	// client may release one on another thread).
	elems     map[kit.NodeID]*element
	overrides map[kit.NodeID]*element

	// Scratch memory for the calls, on the heap (UIAutomationCore may call back into the
	// providers while a call that was given it runs).
	text    []uint16
	kids    []kit.NodeID
	kids2   []kit.NodeID
	objs    []uintptr
	rid     []int32
	oldV    *comobj.Variant
	newV    *comobj.Variant
	hostOut *uintptr

	stats Stats
}

// element is the Go side of one provider object: which node of which tree it stands for.
type element struct {
	tree *Tree
	id   kit.NodeID
	obj  uintptr
	h    comobj.Handle
	root bool
	// override: the provider stands in for a hosted child window's own (see
	// rootOverrideForHwnd), so that the window shows where its node is.
	override bool
	// mask is the interfaces the object answers QueryInterface for (bit per interface
	// index), from the node as last read; objects.mu guards it.
	mask uint32
	// last is the node as the client last saw it, for the property events of a change.
	last kit.SemanticNode
}

// objects is every provider object of the process: the handle table the objects' handles
// name, and their class.
var objects struct {
	mu    sync.Mutex
	table comobj.Table[*element]
	class *comobj.Class
	// refused counts calls refused because they came on another thread than their tree's.
	refused atomic.Int64
	// panics counts panics recovered in provider methods.
	panics atomic.Int64
	err    error
	once   sync.Once
}

// Live is the number of provider objects alive in the process: those some client (or UI
// Automation itself) holds. It returns to 0 once every reference is released.
func Live() int {
	objects.mu.Lock()
	defer objects.mu.Unlock()
	return objects.table.Len()
}

// Refused is the number of provider calls refused since the process started because they
// came on a thread other than their window's UI thread (ProviderOptions_UseComThreading
// should bring every call to it).
func Refused() int64 { return objects.refused.Load() }

// Panics is the number of panics recovered in provider methods since the process started.
func Panics() int64 { return objects.panics.Load() }

// NewTree makes the provider of a window. It fails on a system without UI Automation's
// functions, or on a build without the x64 thunks (the native window needs x64).
func NewTree(h Host) (*Tree, error) {
	if !thunksSupported {
		return nil, fmt.Errorf("uia: UI Automation providers need the x64 (amd64) build: %w", kit.ErrUnsupported)
	}
	objects.once.Do(func() { objects.err = initClass() })
	if objects.err != nil {
		return nil, objects.err
	}
	return &Tree{
		host: h, hwnd: h.HWND(), thread: windows.GetCurrentThreadId(),
		elems: map[kit.NodeID]*element{}, overrides: map[kit.NodeID]*element{},
		oldV: new(comobj.Variant), newV: new(comobj.Variant), hostOut: new(uintptr),
		rid: make([]int32, 0, 4),
	}, nil
}

// Stats is what the tree did so far.
func (t *Tree) Stats() Stats { return t.stats }

// Held describes the providers alive now (a client holds them), for diagnostics: each
// one's node, its references and whether it is the root or a child window's override.
func (t *Tree) Held() []string {
	objects.mu.Lock()
	defer objects.mu.Unlock()
	var out []string
	for _, m := range []map[kit.NodeID]*element{t.elems, t.overrides} {
		for id, e := range m {
			out = append(out, fmt.Sprintf("node %d (%v, root %v, override %v): %d references",
				id, e.last.Role, e.root, e.override, objects.class.Refs(e.obj)))
		}
	}
	return out
}

// tree is the window's semantic tree, or nil.
func (t *Tree) tree() kit.SemanticTree {
	if t.closed {
		return nil
	}
	return t.host.Semantics()
}

// GetObject answers WM_GETOBJECT. For UiaRootObjectId it hands UI Automation the fragment
// root's provider and reports true; for anything else (MSAA's object ids) it reports false,
// and the window answers as DefWindowProc does.
func (t *Tree) GetObject(wp, lp uintptr) (uintptr, bool) {
	if int32(uint32(lp)) != UiaRootObjectId || t.closed {
		return 0, false
	}
	st := t.tree()
	if st == nil {
		return 0, false
	}
	e, err := t.acquire(st.Root())
	if err != nil {
		return 0, false
	}
	r := returnRawElementProvider(t.hwnd, wp, lp, objects.class.Iface(e.obj, ifSimple))
	t.release(e)
	return r, true
}

// Close disconnects every provider of the window (the window is being destroyed): UI
// Automation drops its references, clients' elements report that they are gone, and
// every later call fails with UIA_E_ELEMENTNOTAVAILABLE. It is idempotent.
func (t *Tree) Close() {
	if t.closed {
		return
	}
	t.closed = true
	objects.mu.Lock()
	objs := make([]uintptr, 0, len(t.elems)+len(t.overrides))
	for _, m := range []map[kit.NodeID]*element{t.elems, t.overrides} {
		for _, e := range m {
			objects.class.AddRef(e.obj) // kept alive across the calls below
			objs = append(objs, e.obj)
		}
	}
	objects.mu.Unlock()
	for _, o := range objs {
		disconnectProvider(objects.class.Iface(o, ifSimple))
	}
	for _, o := range objs {
		releaseObject(o)
	}
	returnRawElementProvider(t.hwnd, 0, 0, 0)
}

// acquire is the provider object of node id with a new reference, made when none is
// alive; it reads the node, which must exist.
func (t *Tree) acquire(id kit.NodeID) (*element, error) { return t.acquireAs(id, false) }

// acquireOverride is the override provider of node id (see element.override).
func (t *Tree) acquireOverride(id kit.NodeID) (*element, error) { return t.acquireAs(id, true) }

func (t *Tree) acquireAs(id kit.NodeID, override bool) (*element, error) {
	st := t.tree()
	if st == nil {
		return nil, fmt.Errorf("uia: node %d: %w", id, kit.ErrNoNode)
	}
	n, ok := st.Node(id)
	if !ok {
		return nil, fmt.Errorf("uia: node %d: %w", id, kit.ErrNoNode)
	}
	live := t.elems
	if override {
		live = t.overrides
	}
	objects.mu.Lock()
	defer objects.mu.Unlock()
	if e := live[id]; e != nil {
		objects.class.AddRef(e.obj)
		e.mask = patternsOf(&n, e.root)
		return e, nil
	}
	e := &element{tree: t, id: id, root: id == st.Root(), override: override, last: n}
	e.mask = patternsOf(&n, e.root)
	e.h = objects.table.Add(e)
	obj, err := objects.class.New(e.h)
	if err != nil {
		objects.table.Remove(e.h)
		return nil, err
	}
	e.obj = obj
	live[id] = e
	t.stats.Created++
	return e, nil
}

// release drops the reference acquire took.
func (t *Tree) release(e *element) { releaseObject(e.obj) }

// releaseObject drops one reference to an object; the last one frees it.
func releaseObject(obj uintptr) uint32 {
	objects.mu.Lock()
	defer objects.mu.Unlock()
	n := objects.class.Release(obj)
	if n > 0 {
		return uint32(n)
	}
	if n < 0 {
		// Released once too often: leave the memory alone rather than free it twice.
		return 0
	}
	h := objects.class.Handle(obj)
	if e, ok := objects.table.Get(h); ok {
		objects.table.Remove(h)
		live := e.tree.elems
		if e.override {
			live = e.tree.overrides
		}
		if live[e.id] == e {
			delete(live, e.id)
		}
	}
	objects.class.Free(obj)
	return 0
}

// releaseIface drops the reference an interface pointer of a provider carries.
func releaseIface(p uintptr) {
	if obj, _, ok := objects.class.Object(p); ok {
		releaseObject(obj)
	}
}

// iface is interface k of node id's provider, with a reference for the caller (0 when the
// node is gone).
func (t *Tree) iface(id kit.NodeID, k int) uintptr {
	if id == 0 {
		return 0
	}
	e, err := t.acquire(id)
	if err != nil {
		return 0
	}
	return objects.class.Iface(e.obj, k)
}

// node is e's node now; it refreshes the interfaces the object answers for.
func (e *element) node() (kit.SemanticNode, bool) {
	st := e.tree.tree()
	if st == nil {
		return kit.SemanticNode{}, false
	}
	n, ok := st.Node(e.id)
	if !ok {
		return n, false
	}
	m := patternsOf(&n, e.root)
	if m != e.mask {
		objects.mu.Lock()
		e.mask = m
		objects.mu.Unlock()
	}
	return n, true
}
