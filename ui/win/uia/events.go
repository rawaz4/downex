//go:build windows

package uia

import (
	"strings"

	"downex/ui/kit"
	"downex/ui/win/uia/internal/comobj"
)

// activityID groups the window's announcements for assistive technology (it is not read
// out): UiaRaiseNotificationEvent requires one.
const activityID = "Downex.Announcement"

// Notify raises the UI Automation events of a change the kit reports (kit.Host's
// NotifySemantics): the focus moving, children added or removed, a node's properties
// changing (and a live region's content), and announcements. Nothing is raised while no
// client listens; the tree still notes what changed, so a later change is compared with
// what clients last saw.
func (t *Tree) Notify(ev kit.SemanticEvent) {
	if t.closed {
		return
	}
	st := t.tree()
	if st == nil {
		return
	}
	listening := clientsAreListening()
	switch ev.Kind {
	case kit.SemanticNodeChanged:
		t.nodeChanged(st, ev.Node, listening)
	case kit.SemanticFocusChanged:
		if listening && ev.Node != 0 && ev.Node != st.Root() {
			t.raise(ev.Node, func(p uintptr) { raiseAutomationEvent(p, UIA_AutomationFocusChangedEventId) })
		}
	case kit.SemanticChildrenChanged:
		if listening {
			root := ev.Node == st.Root()
			t.raise(ev.Node, func(p uintptr) {
				var rid []int32
				if !root {
					rid = t.runtimeID(ev.Node)
				}
				raiseStructureChanged(p, StructureChangeType_ChildrenInvalidated, rid)
			})
		}
	case kit.SemanticAnnounce:
		if !listening || ev.Text == "" {
			return
		}
		about := ev.Node
		if _, ok := st.Node(about); about == 0 || !ok {
			about = st.Root()
		}
		processing := int32(NotificationProcessing_All)
		if ev.Politeness == kit.LiveAssertive {
			processing = NotificationProcessing_ImportantAll
		}
		t.raise(about, func(p uintptr) {
			text := comobj.NewBSTR(&t.text, ev.Text)
			activity := comobj.NewBSTR(&t.text, activityID)
			raiseNotification(p, NotificationKind_Other, processing, text, activity)
			comobj.FreeBSTR(text)
			comobj.FreeBSTR(activity)
		})
	}
}

// raise calls f with the Simple provider of node id, made for the call when no client
// holds one.
func (t *Tree) raise(id kit.NodeID, f func(p uintptr)) {
	e, err := t.acquire(id)
	if err != nil {
		return
	}
	t.stats.Events++
	f(objects.class.Iface(e.obj, ifSimple))
	t.release(e)
}

// nodeChanged compares node id with what clients last saw of it, raises a property event
// for each change, and has a live region's change read out.
func (t *Tree) nodeChanged(st kit.SemanticTree, id kit.NodeID, listening bool) {
	n, ok := st.Node(id)
	if !ok {
		return
	}
	objects.mu.Lock()
	e := t.elems[id]
	if e != nil {
		objects.class.AddRef(e.obj)
	}
	objects.mu.Unlock()
	changed := true
	if e != nil {
		old := e.last
		e.last = n
		m := patternsOf(&n, e.root)
		objects.mu.Lock()
		e.mask = m
		objects.mu.Unlock()
		if listening {
			t.raiseChanges(e, &old, &n)
		}
		changed = old.Name != n.Name || old.Value != n.Value || old.Description != n.Description
		t.release(e)
	}
	if !listening || !changed {
		return
	}
	// A change inside a live region is the region's to announce (aria-live).
	if live := t.liveRegion(st, &n, id); live != 0 {
		t.raise(live, func(p uintptr) { raiseAutomationEvent(p, UIA_LiveRegionChangedEventId) })
	}
}

// liveRegion is the live region node n (of id) is in, itself included, or 0.
func (t *Tree) liveRegion(st kit.SemanticTree, n *kit.SemanticNode, id kit.NodeID) kit.NodeID {
	for i := 0; i < maxDepth; i++ {
		if liveSetting(n) != Off {
			return id
		}
		if id = n.Parent; id == 0 {
			return 0
		}
		p, ok := st.Node(id)
		if !ok {
			return 0
		}
		n = &p
	}
	return 0
}

// liveName is what a live region without a name of its own reads out: the text inside it
// (a status bar's or a toast's), as the region's change is announced by its name.
func (t *Tree) liveName(st kit.SemanticTree, id kit.NodeID) string {
	var b strings.Builder
	var walk func(id kit.NodeID, depth int)
	walk = func(id kit.NodeID, depth int) {
		if depth > 4 {
			return
		}
		for _, c := range st.AppendChildren(nil, id) {
			n, ok := st.Node(c)
			if !ok {
				continue
			}
			if n.Name != "" {
				if b.Len() > 0 {
					b.WriteByte(' ')
				}
				b.WriteString(n.Name)
				continue
			}
			walk(c, depth+1)
		}
	}
	walk(id, 0)
	return b.String()
}

// raiseChanges raises the property events between what a client saw (old) and n.
func (t *Tree) raiseChanges(e *element, old, n *kit.SemanticNode) {
	p := objects.class.Iface(e.obj, ifSimple)
	o, v := t.oldV, t.newV
	send := func(id int32) {
		raisePropertyChanged(p, id, o, v)
		t.stats.Events++
		comobj.ClearVariant(o)
		comobj.ClearVariant(v)
	}
	text := func(id int32, a, b string) {
		if a == b {
			return
		}
		o.SetPointer(comobj.VT_BSTR, comobj.NewBSTR(&t.text, a))
		v.SetPointer(comobj.VT_BSTR, comobj.NewBSTR(&t.text, b))
		send(id)
	}
	flag := func(id int32, a, b bool) {
		if a == b {
			return
		}
		o.SetBool(a)
		v.SetBool(b)
		send(id)
	}
	num := func(id int32, a, b int32) {
		if a == b {
			return
		}
		o.SetI4(a)
		v.SetI4(b)
		send(id)
	}
	text(UIA_NamePropertyId, old.Name, n.Name)
	text(UIA_FullDescriptionPropertyId, old.Description, n.Description)
	if e.mask&(1<<ifValue) != 0 {
		text(UIA_ValueValuePropertyId, old.Value, n.Value)
	}
	flag(UIA_IsEnabledPropertyId, old.States&kit.NodeDisabled == 0, n.States&kit.NodeDisabled == 0)
	flag(UIA_IsOffscreenPropertyId, old.States&kit.NodeOffscreen != 0, n.States&kit.NodeOffscreen != 0)
	if n.Toggle != kit.ToggleNone || old.Toggle != kit.ToggleNone {
		num(UIA_ToggleToggleStatePropertyId, toggleOf(old), toggleOf(n))
	}
	if n.Expand != kit.ExpandNone || old.Expand != kit.ExpandNone {
		num(UIA_ExpandCollapseExpandCollapseStatePropertyId, expandStateOf(old), expandStateOf(n))
	}
	if n.Range.Valid && old.Range.Valid && old.Range.Value != n.Range.Value {
		o.SetR8(old.Range.Value)
		v.SetR8(n.Range.Value)
		send(UIA_RangeValueValuePropertyId)
	}
	if isGrid(n.Role) {
		num(UIA_GridRowCountPropertyId, int32(old.Grid.Rows), int32(n.Grid.Rows))
	}
	if was, is := selected(old), selected(n); was != is {
		flag(UIA_SelectionItemIsSelectedPropertyId, was, is)
		if is {
			raiseAutomationEvent(p, UIA_SelectionItem_ElementSelectedEventId)
			t.stats.Events++
		}
	}
}

func toggleOf(n *kit.SemanticNode) int32 {
	switch n.Toggle {
	case kit.ToggleOn:
		return ToggleState_On
	case kit.ToggleMixed:
		return ToggleState_Indeterminate
	}
	return ToggleState_Off
}
