//go:build windows

package uia

import (
	"fmt"
	"runtime/debug"
	"sync/atomic"
	"syscall"

	"downex/ui/win/internal/com"
	"downex/ui/win/uia/internal/comobj"

	"golang.org/x/sys/windows"
)

// A provider object has one interface pointer for each interface below, in this order (see
// comobj): IRawElementProviderSimple first, so that the object's IUnknown is its Simple
// pointer. QueryInterface hands out only the interfaces the node has (element.mask).
const (
	ifSimple = iota
	ifFragment
	ifRoot
	ifGrid
	ifTable
	ifGridItem
	ifTableItem
	ifSelection
	ifSelectionItem
	ifInvoke
	ifValue
	ifRangeValue
	ifToggle
	ifExpandCollapse
	ifScrollItem
	ifHwndOverride
	numIfaces
)

// The interfaces' ids (UIAutomationCore.h; consts_test.go checks them against the header).
var (
	IID_IRawElementProviderSimple       = com.MustGUID("d6dd68d1-86fd-4332-8666-9abedea2d24c")
	IID_IRawElementProviderFragment     = com.MustGUID("f7063da8-8359-439c-9297-bbc5299a7d87")
	IID_IRawElementProviderFragmentRoot = com.MustGUID("620ce2a5-ab8f-40a9-86cb-de3c75599b58")
	IID_IGridProvider                   = com.MustGUID("b17d6187-0907-464b-a168-0ef17a1572b1")
	IID_ITableProvider                  = com.MustGUID("9c860395-97b3-490a-b52a-858cc22af166")
	IID_IGridItemProvider               = com.MustGUID("d02541f1-fb81-4d64-ae32-f520f8a6dbd1")
	IID_ITableItemProvider              = com.MustGUID("b9734fa6-771f-4d78-9c90-2517999349cd")
	IID_ISelectionProvider              = com.MustGUID("fb8b03af-3bdf-48d4-bd36-1a65793be168")
	IID_ISelectionItemProvider          = com.MustGUID("2acad808-b2d4-452d-a407-91ff1ad167b2")
	IID_IInvokeProvider                 = com.MustGUID("54fcb24b-e18e-47a2-b4d3-eccbe77599a2")
	IID_IValueProvider                  = com.MustGUID("c7935180-6fb3-4201-b174-7df73adbf64a")
	IID_IRangeValueProvider             = com.MustGUID("36dc7aef-33e6-4691-afe1-2be7274b3d33")
	IID_IToggleProvider                 = com.MustGUID("56d00bd0-c4f4-433c-a836-1a52a57e0892")
	IID_IExpandCollapseProvider         = com.MustGUID("d847d3a5-cab0-4a98-8c32-ecb45c59ad24")
	IID_IScrollItemProvider             = com.MustGUID("2360c714-4bf1-4b26-ba65-9b21316127eb")
	IID_IRawElementProviderHwndOverride = com.MustGUID("1d5df27c-8947-4425-b8d9-79787bb460b8")
	IID_IUnknown                        = com.MustGUID("00000000-0000-0000-c000-000000000046")
)

// method is one vtable entry after IUnknown's: its name in the header, and the Go function
// that implements it (each argument a machine word, this first), or an assembly thunk that
// passes its doubles to fn as bits.
type method struct {
	name  string
	fn    any
	thunk bool
}

// iface is one interface of the provider objects, as the header declares it.
type iface struct {
	name    string
	methods []method
}

// iids are the interfaces' ids, by interface index.
var iids = [numIfaces]*com.GUID{
	ifSimple: &IID_IRawElementProviderSimple, ifFragment: &IID_IRawElementProviderFragment,
	ifRoot: &IID_IRawElementProviderFragmentRoot, ifGrid: &IID_IGridProvider, ifTable: &IID_ITableProvider,
	ifGridItem: &IID_IGridItemProvider, ifTableItem: &IID_ITableItemProvider, ifSelection: &IID_ISelectionProvider,
	ifSelectionItem: &IID_ISelectionItemProvider, ifInvoke: &IID_IInvokeProvider, ifValue: &IID_IValueProvider,
	ifRangeValue: &IID_IRangeValueProvider, ifToggle: &IID_IToggleProvider,
	ifExpandCollapse: &IID_IExpandCollapseProvider, ifScrollItem: &IID_IScrollItemProvider,
	ifHwndOverride: &IID_IRawElementProviderHwndOverride,
}

// ifaces lists every method in the header's order; vtable_test.go compares names, order
// and argument counts with UIAutomationCore.h.
var ifaces = [numIfaces]iface{
	ifSimple: {"IRawElementProviderSimple", []method{
		{"get_ProviderOptions", simpleProviderOptions, false},
		{"GetPatternProvider", simpleGetPatternProvider, false},
		{"GetPropertyValue", simpleGetPropertyValue, false},
		{"get_HostRawElementProvider", simpleHostRawElementProvider, false},
	}},
	ifFragment: {"IRawElementProviderFragment", []method{
		{"Navigate", fragmentNavigate, false},
		{"GetRuntimeId", fragmentGetRuntimeID, false},
		{"get_BoundingRectangle", fragmentBoundingRectangle, false},
		{"GetEmbeddedFragmentRoots", fragmentEmbeddedRoots, false},
		{"SetFocus", fragmentSetFocus, false},
		{"get_FragmentRoot", fragmentRoot, false},
	}},
	ifRoot: {"IRawElementProviderFragmentRoot", []method{
		{"ElementProviderFromPoint", rootFromPoint, true},
		{"GetFocus", rootGetFocus, false},
	}},
	ifGrid: {"IGridProvider", []method{
		{"GetItem", gridGetItem, false},
		{"get_RowCount", gridRowCount, false},
		{"get_ColumnCount", gridColumnCount, false},
	}},
	ifTable: {"ITableProvider", []method{
		{"GetRowHeaders", tableRowHeaders, false},
		{"GetColumnHeaders", tableColumnHeaders, false},
		{"get_RowOrColumnMajor", tableRowOrColumnMajor, false},
	}},
	ifGridItem: {"IGridItemProvider", []method{
		{"get_Row", gridItemRow, false},
		{"get_Column", gridItemColumn, false},
		{"get_RowSpan", gridItemRowSpan, false},
		{"get_ColumnSpan", gridItemColumnSpan, false},
		{"get_ContainingGrid", gridItemContainingGrid, false},
	}},
	ifTableItem: {"ITableItemProvider", []method{
		{"GetRowHeaderItems", tableItemRowHeaders, false},
		{"GetColumnHeaderItems", tableItemColumnHeaders, false},
	}},
	ifSelection: {"ISelectionProvider", []method{
		{"GetSelection", selectionGet, false},
		{"get_CanSelectMultiple", selectionCanSelectMultiple, false},
		{"get_IsSelectionRequired", selectionIsRequired, false},
	}},
	ifSelectionItem: {"ISelectionItemProvider", []method{
		{"Select", selectionItemSelect, false},
		{"AddToSelection", selectionItemAdd, false},
		{"RemoveFromSelection", selectionItemRemove, false},
		{"get_IsSelected", selectionItemIsSelected, false},
		{"get_SelectionContainer", selectionItemContainer, false},
	}},
	ifInvoke: {"IInvokeProvider", []method{
		{"Invoke", invokeInvoke, false},
	}},
	ifValue: {"IValueProvider", []method{
		{"SetValue", valueSet, false},
		{"get_Value", valueGet, false},
		{"get_IsReadOnly", valueIsReadOnly, false},
	}},
	ifRangeValue: {"IRangeValueProvider", []method{
		{"SetValue", rangeSet, true},
		{"get_Value", rangeValue, false},
		{"get_IsReadOnly", rangeIsReadOnly, false},
		{"get_Maximum", rangeMaximum, false},
		{"get_Minimum", rangeMinimum, false},
		{"get_LargeChange", rangeLargeChange, false},
		{"get_SmallChange", rangeSmallChange, false},
	}},
	ifToggle: {"IToggleProvider", []method{
		{"Toggle", toggleToggle, false},
		{"get_ToggleState", toggleState, false},
	}},
	ifExpandCollapse: {"IExpandCollapseProvider", []method{
		{"Expand", expandExpand, false},
		{"Collapse", expandCollapse, false},
		{"get_ExpandCollapseState", expandState, false},
	}},
	ifScrollItem: {"IScrollItemProvider", []method{
		{"ScrollIntoView", scrollItemScrollIntoView, false},
	}},
	ifHwndOverride: {"IRawElementProviderHwndOverride", []method{
		{"GetOverrideProviderForHwnd", rootOverrideForHwnd, false},
	}},
}

// interfaceOf is the interface index of an interface id, or -1.
func interfaceOf(iid com.GUID) int {
	for k, p := range iids {
		if *p == iid {
			return k
		}
	}
	return -1
}

// patternIface is the interface index of a control pattern, or -1.
func patternIface(pattern int32) int {
	switch pattern {
	case UIA_GridPatternId:
		return ifGrid
	case UIA_TablePatternId:
		return ifTable
	case UIA_GridItemPatternId:
		return ifGridItem
	case UIA_TableItemPatternId:
		return ifTableItem
	case UIA_SelectionPatternId:
		return ifSelection
	case UIA_SelectionItemPatternId:
		return ifSelectionItem
	case UIA_InvokePatternId:
		return ifInvoke
	case UIA_ValuePatternId:
		return ifValue
	case UIA_RangeValuePatternId:
		return ifRangeValue
	case UIA_TogglePatternId:
		return ifToggle
	case UIA_ExpandCollapsePatternId:
		return ifExpandCollapse
	case UIA_ScrollItemPatternId:
		return ifScrollItem
	}
	return -1
}

// initClass makes the vtables (once per process): IUnknown's three methods are shared by
// every interface, and each method a callback of its own.
func initClass() error {
	if err := load(); err != nil {
		return err
	}
	qi := syscall.NewCallback(unkQueryInterface)
	addRef := syscall.NewCallback(unkAddRef)
	release := syscall.NewCallback(unkRelease)
	fromPoint, setRange := thunkPCs()
	vtbls := make([]uintptr, numIfaces)
	for k := range ifaces {
		entries := []uintptr{qi, addRef, release}
		for _, m := range ifaces[k].methods {
			cb := syscall.NewCallback(m.fn)
			if m.thunk {
				switch k {
				case ifRoot:
					fromPointTarget, cb = cb, fromPoint
				case ifRangeValue:
					setRangeTarget, cb = cb, setRange
				default:
					return fmt.Errorf("uia: %s.%s: no thunk for it", ifaces[k].name, m.name)
				}
			}
			entries = append(entries, cb)
		}
		vt, err := comobj.Vtable(entries)
		if err != nil {
			return err
		}
		vtbls[k] = vt
	}
	objects.class = comobj.NewClass(vtbls)
	return nil
}

// ---- IUnknown ---------------------------------------------------------------------------

// The HRESULTs the methods return.
const (
	hrOK               = uintptr(com.S_OK)
	hrNoInterface      = uintptr(com.E_NOINTERFACE)
	hrPointer          = uintptr(com.E_POINTER)
	hrFail             = uintptr(com.E_FAIL)
	hrInvalidArg       = uintptr(com.E_INVALIDARG)
	hrOutOfMemory      = uintptr(com.E_OUTOFMEMORY)
	hrNotAvailable     = uintptr(UIA_E_ELEMENTNOTAVAILABLE)
	hrNotEnabled       = uintptr(UIA_E_ELEMENTNOTENABLED)
	hrInvalidOperation = uintptr(UIA_E_INVALIDOPERATION)
	hrWrongThread      = uintptr(0x8001010E) // RPC_E_WRONG_THREAD
	hrDisconnected     = uintptr(0x80010108) // RPC_E_DISCONNECTED
)

func unkQueryInterface(this, riid, ppv uintptr) (hr uintptr) {
	defer recovered(&hr)
	if ppv == 0 {
		return hrPointer
	}
	comobj.SetWord(ppv, 0)
	if riid == 0 {
		return hrInvalidArg
	}
	obj, _, ok := objects.class.Object(this)
	if !ok {
		return hrNoInterface
	}
	iid := comobj.GUIDAt(riid)
	k := ifSimple
	if iid != IID_IUnknown {
		if k = interfaceOf(iid); k < 0 {
			return hrNoInterface
		}
	}
	objects.mu.Lock()
	defer objects.mu.Unlock()
	e, ok := objects.table.Get(objects.class.Handle(obj))
	if !ok {
		return hrDisconnected
	}
	if e.mask&(1<<k) == 0 {
		return hrNoInterface
	}
	objects.class.AddRef(obj)
	comobj.SetWord(ppv, objects.class.Iface(obj, k))
	return hrOK
}

func unkAddRef(this uintptr) (n uintptr) {
	defer recovered(&n)
	obj, _, ok := objects.class.Object(this)
	if !ok {
		return 0
	}
	objects.mu.Lock()
	defer objects.mu.Unlock()
	return uintptr(objects.class.AddRef(obj))
}

func unkRelease(this uintptr) (n uintptr) {
	defer recovered(&n)
	obj, _, ok := objects.class.Object(this)
	if !ok {
		return 0
	}
	return uintptr(releaseObject(obj))
}

// recovered turns a panic in a method into E_FAIL: a panic must not unwind into the
// system's frames that called the method.
func recovered(hr *uintptr) {
	if v := recover(); v != nil {
		objects.panics.Add(1)
		lastPanic.Store(fmt.Sprintf("uia: panic in a provider method: %v\n%s", v, debug.Stack()))
		*hr = hrFail
	}
}

// lastPanic is the last panic recovered, for diagnostics and tests.
var lastPanic atomic.Value

// LastPanic is the last panic recovered in a provider method, or "".
func LastPanic() string {
	s, _ := lastPanic.Load().(string)
	return s
}

// call runs f for the element an interface pointer belongs to, on its tree's UI thread:
// a call on another thread, for a freed object or a closed window fails as UI Automation
// expects, and a panic becomes E_FAIL.
func call(this uintptr, f func(e *element) uintptr) (hr uintptr) {
	defer recovered(&hr)
	obj, _, ok := objects.class.Object(this)
	if !ok {
		return hrInvalidArg
	}
	objects.mu.Lock()
	e, ok := objects.table.Get(objects.class.Handle(obj))
	objects.mu.Unlock()
	if !ok {
		return hrNotAvailable
	}
	if windows.GetCurrentThreadId() != e.tree.thread {
		objects.refused.Add(1)
		return hrWrongThread
	}
	if e.tree.closed {
		return hrNotAvailable
	}
	e.tree.stats.Calls++
	return f(e)
}

// out writes a result word through an out pointer (a caller that makes a reference for it
// checks the pointer first, so that a null one leaks nothing).
func out(p, v uintptr) uintptr {
	if p == 0 {
		return hrPointer
	}
	comobj.SetWord(p, v)
	return hrOK
}
