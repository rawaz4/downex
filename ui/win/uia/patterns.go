//go:build windows

package uia

import (
	"math"

	"downex/ui/kit"
	"downex/ui/win/internal/com"
	"downex/ui/win/uia/internal/comobj"

	"golang.org/x/sys/windows"
)

// The control patterns, each over the node's SemanticNode: what the node says it is and
// can do (kit's States, Actions, Toggle, Expand, Range, Grid and Cell), and the actions
// asked of it through Host.Perform.

// maxDepth bounds the walks up and down the tree a pattern makes (a cell's grid, a grid's
// headers): the kit's grids are a few levels deep.
const maxDepth = 64

// writeInt writes an int out-parameter.
func writeInt(p uintptr, v int32) uintptr {
	if p == 0 {
		return hrPointer
	}
	*(*int32)(com.Addr(p)) = v
	return hrOK
}

// writeBool writes a BOOL out-parameter.
func writeBool(p uintptr, b bool) uintptr {
	var v int32
	if b {
		v = 1
	}
	return writeInt(p, v)
}

// writeDouble writes a double out-parameter.
func writeDouble(p uintptr, v float64) uintptr {
	if p == 0 {
		return hrPointer
	}
	*(*float64)(com.Addr(p)) = v
	return hrOK
}

// withNode runs f with e's node now, or fails for a node that is gone.
func withNode(this uintptr, f func(e *element, n *kit.SemanticNode) uintptr) uintptr {
	return call(this, func(e *element) uintptr {
		n, ok := e.node()
		if !ok {
			return hrNotAvailable
		}
		return f(e, &n)
	})
}

// ancestor is the nearest ancestor of n whose role is, or 0.
func (t *Tree) ancestor(n *kit.SemanticNode, is func(kit.Role) bool) kit.NodeID {
	st := t.tree()
	for id, i := n.Parent, 0; id != 0 && i < maxDepth; i++ {
		p, ok := st.Node(id)
		if !ok {
			return 0
		}
		if is(p.Role) {
			return id
		}
		id = p.Parent
	}
	return 0
}

// findRole appends the nodes of role within depth levels under from (not looking inside a
// match).
func (t *Tree) findRole(dst []kit.NodeID, from kit.NodeID, role kit.Role, depth int) []kit.NodeID {
	st := t.tree()
	if st == nil || depth <= 0 {
		return dst
	}
	for _, id := range st.AppendChildren(nil, from) {
		n, ok := st.Node(id)
		if !ok {
			continue
		}
		if n.Role == role {
			dst = append(dst, id)
			continue
		}
		dst = t.findRole(dst, id, role, depth-1)
	}
	return dst
}

// providers is a SAFEARRAY of the Simple providers of ids, written to ret.
func (t *Tree) providers(ret uintptr, ids []kit.NodeID) uintptr {
	if ret == 0 {
		return hrPointer
	}
	comobj.SetWord(ret, 0)
	t.objs = t.objs[:0]
	for _, id := range ids {
		if p := t.iface(id, ifSimple); p != 0 {
			t.objs = append(t.objs, p)
		}
	}
	psa, err := comobj.NewUnknownArray(t.objs)
	for _, p := range t.objs {
		releaseIface(p)
	}
	if err != nil {
		return hrOf(err)
	}
	comobj.SetWord(ret, psa)
	return hrOK
}

// ---- Grid and Table ------------------------------------------------------------------------

func gridGetItem(this, row, col, ret uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr {
		if ret == 0 {
			return hrPointer
		}
		comobj.SetWord(ret, 0)
		r, c := int32(uint32(row)), int32(uint32(col))
		if r < 0 || c < 0 || int(r) >= n.Grid.Rows || (n.Grid.Cols > 0 && int(c) >= n.Grid.Cols) {
			return hrInvalidArg
		}
		id := e.tree.tree().Cell(e.id, int(r), int(c))
		comobj.SetWord(ret, e.tree.iface(id, ifSimple))
		return hrOK
	})
}

func gridRowCount(this, ret uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr { return writeInt(ret, int32(n.Grid.Rows)) })
}

func gridColumnCount(this, ret uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr { return writeInt(ret, int32(n.Grid.Cols)) })
}

func tableRowHeaders(this, ret uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr {
		return e.tree.providers(ret, e.tree.findRole(nil, e.id, kit.RoleRowHeader, 3))
	})
}

func tableColumnHeaders(this, ret uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr {
		return e.tree.providers(ret, e.tree.findRole(nil, e.id, kit.RoleColumnHeader, 3))
	})
}

func tableRowOrColumnMajor(this, ret uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr {
		return writeInt(ret, RowOrColumnMajor_RowMajor)
	})
}

// ---- GridItem and TableItem ----------------------------------------------------------------

func gridItemRow(this, ret uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr { return writeInt(ret, int32(n.Cell.Row)) })
}

func gridItemColumn(this, ret uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr { return writeInt(ret, int32(n.Cell.Col)) })
}

func gridItemRowSpan(this, ret uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr {
		return writeInt(ret, int32(max(n.Cell.RowSpan, 1)))
	})
}

func gridItemColumnSpan(this, ret uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr {
		return writeInt(ret, int32(max(n.Cell.ColSpan, 1)))
	})
}

func gridItemContainingGrid(this, ret uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr {
		if ret == 0 {
			return hrPointer
		}
		return out(ret, e.tree.iface(e.tree.ancestor(n, isGrid), ifSimple))
	})
}

func tableItemRowHeaders(this, ret uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr {
		var ids []kit.NodeID
		if n.Role != kit.RoleRowHeader && n.Parent != 0 {
			st := e.tree.tree()
			for _, id := range st.AppendChildren(nil, n.Parent) {
				if c, ok := st.Node(id); ok && c.Role == kit.RoleRowHeader {
					ids = append(ids, id)
				}
			}
		}
		return e.tree.providers(ret, ids)
	})
}

func tableItemColumnHeaders(this, ret uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr {
		var ids []kit.NodeID
		if n.Role != kit.RoleColumnHeader {
			if g := e.tree.ancestor(n, isGrid); g != 0 {
				st := e.tree.tree()
				for _, id := range e.tree.findRole(nil, g, kit.RoleColumnHeader, 3) {
					if h, ok := st.Node(id); ok && h.Cell.Col == n.Cell.Col {
						ids = append(ids, id)
					}
				}
			}
		}
		return e.tree.providers(ret, ids)
	})
}

// ---- Selection and SelectionItem -----------------------------------------------------------

func selectionGet(this, ret uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr {
		t := e.tree
		t.kids2 = t.tree().AppendSelection(t.kids2[:0], e.id)
		return t.providers(ret, t.kids2)
	})
}

func selectionCanSelectMultiple(this, ret uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr {
		return writeBool(ret, n.States&kit.NodeMultiSelectable != 0)
	})
}

// selectionIsRequired: a tab list always has its selected tab; the kit says nothing of the
// others, which allow an empty selection.
func selectionIsRequired(this, ret uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr {
		return writeBool(ret, n.Role == kit.RoleTabList)
	})
}

func selectionItemSelect(this uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr { return e.perform(kit.ActionSelect, "", 0) })
}

func selectionItemAdd(this uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr {
		return e.perform(kit.ActionAddToSelection, "", 0)
	})
}

func selectionItemRemove(this uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr {
		return e.perform(kit.ActionRemoveFromSelection, "", 0)
	})
}

func selectionItemIsSelected(this, ret uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr { return writeBool(ret, selected(n)) })
}

// selected reports a selected item (a radio button checked).
func selected(n *kit.SemanticNode) bool {
	return n.States&kit.NodeSelected != 0 || (n.Role == kit.RoleRadioButton && n.Toggle == kit.ToggleOn)
}

func selectionItemContainer(this, ret uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr {
		if ret == 0 {
			return hrPointer
		}
		return out(ret, e.tree.iface(e.tree.ancestor(n, isSelectionContainer), ifSimple))
	})
}

// ---- Invoke --------------------------------------------------------------------------------

// invokeInvoke checks that the node can be invoked now and invokes it once the client's
// call has returned: IInvokeProvider::Invoke must not block (a button may open a dialog).
func invokeInvoke(this uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr {
		switch {
		case n.States&kit.NodeDisabled != 0:
			return hrNotEnabled
		case n.Actions&kit.ActionInvoke == 0:
			return hrInvalidOperation
		}
		return hrOf(e.tree.host.Post(kit.ActionRequest{Node: e.id, Action: kit.ActionInvoke}))
	})
}

// ---- Value -----------------------------------------------------------------------------------

func valueReadOnly(n *kit.SemanticNode) bool {
	return n.States&(kit.NodeReadOnly|kit.NodeDisabled) != 0 || n.Actions&kit.ActionSetValue == 0
}

func valueSet(this, text uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr {
		switch {
		case text == 0:
			return hrInvalidArg
		case n.States&kit.NodeDisabled != 0:
			return hrNotEnabled
		case valueReadOnly(n):
			return hrInvalidOperation
		}
		return e.perform(kit.ActionSetValue, windows.UTF16PtrToString((*uint16)(com.Addr(text))), 0)
	})
}

func valueGet(this, ret uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr {
		if ret == 0 {
			return hrPointer
		}
		b := comobj.NewBSTR(&e.tree.text, n.Value)
		if b == 0 {
			comobj.SetWord(ret, 0)
			return hrOutOfMemory
		}
		comobj.SetWord(ret, b)
		return hrOK
	})
}

func valueIsReadOnly(this, ret uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr { return writeBool(ret, valueReadOnly(n)) })
}

// ---- RangeValue ------------------------------------------------------------------------------

// rangeSet is IRangeValueProvider::SetValue; its double comes as bits (thunk_amd64.s).
func rangeSet(this, bits uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr {
		v := math.Float64frombits(uint64(bits))
		switch {
		case !n.Range.Valid:
			return hrInvalidOperation
		case n.States&kit.NodeDisabled != 0:
			return hrNotEnabled
		case n.States&kit.NodeReadOnly != 0 || n.Actions&kit.ActionSetRange == 0:
			return hrInvalidOperation
		case math.IsNaN(v) || v < n.Range.Min || v > n.Range.Max:
			return hrInvalidArg
		}
		return e.perform(kit.ActionSetRange, "", v)
	})
}

func rangeValue(this, ret uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr { return writeDouble(ret, n.Range.Value) })
}

func rangeIsReadOnly(this, ret uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr {
		return writeBool(ret, n.States&(kit.NodeReadOnly|kit.NodeDisabled) != 0 || n.Actions&kit.ActionSetRange == 0)
	})
}

func rangeMaximum(this, ret uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr { return writeDouble(ret, n.Range.Max) })
}

func rangeMinimum(this, ret uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr { return writeDouble(ret, n.Range.Min) })
}

// rangeLargeChange and rangeSmallChange are the node's one step (the kit has no larger one).
func rangeLargeChange(this, ret uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr { return writeDouble(ret, n.Range.Step) })
}

func rangeSmallChange(this, ret uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr { return writeDouble(ret, n.Range.Step) })
}

// ---- Toggle ----------------------------------------------------------------------------------

// toggleToggle toggles the node; a toggle button that the kit only presses (a Pressable
// with a toggle state) is pressed.
func toggleToggle(this uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr {
		switch {
		case n.Actions&kit.ActionToggle != 0:
			return e.perform(kit.ActionToggle, "", 0)
		case n.Actions&kit.ActionInvoke != 0:
			return e.perform(kit.ActionInvoke, "", 0)
		}
		return hrInvalidOperation
	})
}

func toggleState(this, ret uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr { return writeInt(ret, toggleOf(n)) })
}

// ---- ExpandCollapse --------------------------------------------------------------------------

func expandExpand(this uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr { return e.perform(kit.ActionExpand, "", 0) })
}

func expandCollapse(this uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr { return e.perform(kit.ActionCollapse, "", 0) })
}

func expandState(this, ret uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr { return writeInt(ret, expandStateOf(n)) })
}

func expandStateOf(n *kit.SemanticNode) int32 {
	switch n.Expand {
	case kit.ExpandOpen:
		return ExpandCollapseState_Expanded
	case kit.ExpandClosed:
		return ExpandCollapseState_Collapsed
	}
	return ExpandCollapseState_LeafNode
}

// ---- ScrollItem ------------------------------------------------------------------------------

func scrollItemScrollIntoView(this uintptr) uintptr {
	return withNode(this, func(e *element, n *kit.SemanticNode) uintptr {
		return e.perform(kit.ActionScrollIntoView, "", 0)
	})
}
