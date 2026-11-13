//go:build windows

package uia

import (
	"errors"
	"math"

	"downex/ui/kit"
	"downex/ui/win/internal/com"
	"downex/ui/win/uia/internal/comobj"
)

// hrOf is the HRESULT UI Automation expects for a kit error.
func hrOf(err error) uintptr {
	switch {
	case err == nil:
		return hrOK
	case errors.Is(err, kit.ErrNoNode), errors.Is(err, kit.ErrClosed):
		return hrNotAvailable
	case errors.Is(err, kit.ErrNotEnabled):
		return hrNotEnabled
	case errors.Is(err, kit.ErrUnsupported), errors.Is(err, kit.ErrInvalid):
		return hrInvalidOperation
	}
	if hr := com.Code(err); hr.Failed() {
		return uintptr(hr)
	}
	return hrFail
}

// perform asks the node for an action, as a click or a key would, now.
func (e *element) perform(a kit.Actions, text string, number float64) uintptr {
	return hrOf(e.tree.host.Perform(kit.ActionRequest{Node: e.id, Action: a, Text: text, Number: number}))
}

// ---- IRawElementProviderFragment --------------------------------------------------------

func fragmentNavigate(this, dir, ret uintptr) uintptr {
	return call(this, func(e *element) uintptr {
		if ret == 0 {
			return hrPointer
		}
		comobj.SetWord(ret, 0)
		n, ok := e.node()
		if !ok {
			return hrNotAvailable
		}
		t := e.tree
		st := t.tree()
		var to kit.NodeID
		switch int32(uint32(dir)) {
		case NavigateDirection_Parent:
			if !e.root {
				to = n.Parent
			}
		case NavigateDirection_FirstChild, NavigateDirection_LastChild:
			t.kids = st.AppendChildren(t.kids[:0], e.id)
			if len(t.kids) > 0 {
				to = t.kids[0]
				if int32(uint32(dir)) == NavigateDirection_LastChild {
					to = t.kids[len(t.kids)-1]
				}
			}
		case NavigateDirection_NextSibling, NavigateDirection_PreviousSibling:
			if e.root || n.Parent == 0 {
				break
			}
			t.kids = st.AppendChildren(t.kids[:0], n.Parent)
			for i, id := range t.kids {
				if id != e.id {
					continue
				}
				j := i + 1
				if int32(uint32(dir)) == NavigateDirection_PreviousSibling {
					j = i - 1
				}
				if j >= 0 && j < len(t.kids) {
					to = t.kids[j]
				}
				break
			}
		default:
			return hrInvalidArg
		}
		comobj.SetWord(ret, t.iface(to, ifFragment))
		return hrOK
	})
}

func fragmentGetRuntimeID(this, ret uintptr) uintptr {
	return call(this, func(e *element) uintptr {
		if ret == 0 {
			return hrPointer
		}
		comobj.SetWord(ret, 0)
		if e.root {
			return hrOK // the window's runtime id
		}
		if _, ok := e.node(); !ok {
			return hrNotAvailable
		}
		psa, err := comobj.NewI4Array(e.tree.runtimeID(e.id))
		if err != nil {
			return hrOf(err)
		}
		comobj.SetWord(ret, psa)
		return hrOK
	})
}

// runtimeID is a node's runtime id after the window's: UiaAppendRuntimeId, then the
// node's id (unique and never reused while the window lives) in two halves. It is the
// tree's scratch slice.
func (t *Tree) runtimeID(id kit.NodeID) []int32 {
	t.rid = append(t.rid[:0], UiaAppendRuntimeId, int32(uint32(id)), int32(uint32(id>>32)))
	return t.rid
}

func fragmentBoundingRectangle(this, ret uintptr) uintptr {
	return call(this, func(e *element) uintptr {
		if ret == 0 {
			return hrPointer
		}
		r := (*Rect)(com.Addr(ret))
		*r = Rect{}
		n, ok := e.node()
		if !ok {
			return hrNotAvailable
		}
		if !n.Bounds.Empty() {
			*r = e.tree.host.ToScreen(n.Bounds)
		}
		return hrOK
	})
}

func fragmentEmbeddedRoots(this, ret uintptr) uintptr {
	return call(this, func(e *element) uintptr { return out(ret, 0) })
}

func fragmentSetFocus(this uintptr) uintptr {
	return call(this, func(e *element) uintptr {
		if _, ok := e.node(); !ok {
			return hrNotAvailable
		}
		return e.perform(kit.ActionFocus, "", 0)
	})
}

func fragmentRoot(this, ret uintptr) uintptr {
	return call(this, func(e *element) uintptr {
		if ret == 0 {
			return hrPointer
		}
		comobj.SetWord(ret, 0)
		st := e.tree.tree()
		if st == nil {
			return hrNotAvailable
		}
		comobj.SetWord(ret, e.tree.iface(st.Root(), ifRoot))
		return hrOK
	})
}

// ---- IRawElementProviderFragmentRoot ----------------------------------------------------

// rootFromPoint is ElementProviderFromPoint; its doubles come as bits (thunk_amd64.s).
func rootFromPoint(this, xbits, ybits, ret uintptr) uintptr {
	return call(this, func(e *element) uintptr {
		if ret == 0 {
			return hrPointer
		}
		comobj.SetWord(ret, 0)
		st := e.tree.tree()
		if st == nil {
			return hrNotAvailable
		}
		x, y := math.Float64frombits(uint64(xbits)), math.Float64frombits(uint64(ybits))
		if math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) {
			return hrInvalidArg
		}
		id := st.HitTest(e.tree.host.FromScreen(x, y))
		comobj.SetWord(ret, e.tree.iface(id, ifFragment))
		return hrOK
	})
}

func rootGetFocus(this, ret uintptr) uintptr {
	return call(this, func(e *element) uintptr {
		if ret == 0 {
			return hrPointer
		}
		comobj.SetWord(ret, 0)
		st := e.tree.tree()
		if st == nil {
			return hrNotAvailable
		}
		// The focus on the window itself, or on nothing, is no element of the fragment.
		if id := st.Focused(); id != 0 && id != st.Root() {
			comobj.SetWord(ret, e.tree.iface(id, ifFragment))
		}
		return hrOK
	})
}

// ---- IRawElementProviderHwndOverride ----------------------------------------------------

// hostingNode is the text-field node that hosts the child window hwnd, whose box's centre is
// at: the node hit there, else the one found by walking down the nodes whose boxes hold the
// point. A layer above the field (a dialog's backdrop over the title bar's search) takes the
// hit, and without the walk the field's window showed twice: at its node, and again as a
// bare nameless element under the root (the Queues dialog's three).
func hostingNode(st kit.SemanticTree, h Host, at kit.Point, hwnd uintptr) kit.NodeID {
	hosts := func(id kit.NodeID) bool {
		n, ok := st.Node(id)
		return ok && n.Role == kit.RoleTextField && h.HostedWindow(&n) == hwnd
	}
	if id := st.HitTest(at); hosts(id) {
		return id
	}
	var kids []kit.NodeID
	var walk func(id kit.NodeID, depth int) kit.NodeID
	walk = func(id kit.NodeID, depth int) kit.NodeID {
		if depth > 64 {
			return 0
		}
		if hosts(id) {
			return id
		}
		start := len(kids)
		kids = st.AppendChildren(kids, id)
		for i := start; i < len(kids); i++ {
			c := kids[i]
			if n, ok := st.Node(c); ok && !n.Bounds.Empty() && !n.Bounds.Contains(at) {
				continue
			}
			if found := walk(c, depth+1); found != 0 {
				return found
			}
		}
		kids = kids[:start]
		return 0
	}
	return walk(st.Root(), 0)
}

// rootOverrideForHwnd places a hosted child window (a native text field) where its node
// is: UI Automation would otherwise add the window as a child of the root, after the
// fragment's own children, besides showing it at its node. The node is the one at the
// centre of the window's box.
func rootOverrideForHwnd(this, hwnd, ret uintptr) uintptr {
	return call(this, func(e *element) uintptr {
		if ret == 0 {
			return hrPointer
		}
		comobj.SetWord(ret, 0)
		b, ok := e.tree.host.HostedBounds(hwnd)
		st := e.tree.tree()
		if !ok || st == nil {
			return hrOK
		}
		id := hostingNode(st, e.tree.host, b.Center(), hwnd)
		if id == 0 {
			return hrOK
		}
		o, err := e.tree.acquireOverride(id)
		if err != nil {
			return hrOK
		}
		comobj.SetWord(ret, objects.class.Iface(o.obj, ifSimple))
		return hrOK
	})
}
