//go:build windows

// Package uiaclient is a small UI Automation client (IUIAutomation from UIAutomationCore,
// UIAutomationClient.h) for tests: it reads windows' trees as Narrator or Accessibility
// Insights would, through the system's client, in this process or another. Nothing in the
// product imports it. Its vtable slots and interface ids are checked against the Windows
// SDK header like the other bindings of ui/win (package sdkheader).
//
// A client lives on one goroutine locked to a thread in the multithreaded apartment (Init),
// never on a UI thread whose windows it reads: UI Automation brings provider calls to their
// UI thread, which must stay free to pump its messages. The event handlers run on UI
// Automation's own threads and hand what they see to channels.
package uiaclient

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	"downex/ui/win/internal/com"
	"downex/ui/win/uia/internal/comobj"

	"golang.org/x/sys/windows"
)

// The interfaces' ids (UIAutomationClient.h).
var (
	IID_IUIAutomation                             = com.MustGUID("30cbe57d-d9d0-452a-ab13-7ac5ac4825ee")
	IID_IUIAutomation5                            = com.MustGUID("25f700c8-d816-4057-a9dc-3cbdee77e256")
	IID_IUIAutomationElement                      = com.MustGUID("d22108aa-8ac5-49a5-837b-37bbb3d7591e")
	IID_IUIAutomationGridPattern                  = com.MustGUID("414c3cdc-856b-4f5b-8538-3131c6302550")
	IID_IUIAutomationInvokePattern                = com.MustGUID("fb377fbe-8ea6-46d5-9c73-6499642d3059")
	IID_IUIAutomationSelectionItemPattern         = com.MustGUID("a8efa66a-0fda-421a-9194-38021f3578ea")
	IID_IUIAutomationValuePattern                 = com.MustGUID("a94cd8b1-0844-4cd6-9d2d-640537ab39e9")
	IID_IUIAutomationRangeValuePattern            = com.MustGUID("59213f4f-7346-49e5-b120-80555987a148")
	IID_IUIAutomationTogglePattern                = com.MustGUID("94cf8058-9b8d-4ab9-8bfd-4cd0a33c8c70")
	IID_IUIAutomationNotificationEventHandler     = com.MustGUID("c7cb2637-e6c2-4d0c-85de-4948c02175c7")
	IID_IUIAutomationFocusChangedEventHandler     = com.MustGUID("c270f6b5-5c69-4290-9745-7a7f97169468")
	IID_IUIAutomationStructureChangedEventHandler = com.MustGUID("e81d1b4e-11c5-42f8-9754-e7036c79f054")
	IID_IUIAutomationEventHandler                 = com.MustGUID("146c3c17-f12e-4e22-8c27-f894b9b79c69")
)

// clsidCUIAutomation8 is CLSID_CUIAutomation8, the class of IUIAutomation (the header
// declares it with DECLSPEC_UUID, which client_test.go checks; sdkheader reads only
// DEFINE_GUID's CLSIDs).
var clsidCUIAutomation8 = mustParse("e22ad333-b25f-460c-83d0-0581107395c9")

func mustParse(s string) com.GUID {
	g, err := com.ParseGUID(s)
	if err != nil {
		panic(err)
	}
	return g
}

// TreeScope values (UIAutomationClient.h).
const (
	TreeScope_Element     = 0x1
	TreeScope_Children    = 0x2
	TreeScope_Descendants = 0x4
	TreeScope_Subtree     = TreeScope_Element | TreeScope_Children | TreeScope_Descendants
)

// heap returns a new zero T on the heap: the out-parameters of calls whose callee may run
// Go code on this thread before it returns (package w32's rule).
//
//go:noinline
func heap[T any]() *T { return new(T) }

// Init puts the calling thread, which must be locked to its goroutine, in the
// multithreaded apartment.
//
// The thread stays in the multithreaded apartment: call Init on a thread locked to its
// goroutine for good (runtime.LockOSThread, never unlocked), so that it ends with the
// goroutine instead of going back to Go's pool as an MTA thread, where a window's UI thread
// that later lands on it could not be a single-threaded apartment.
func Init() error {
	switch err := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED); {
	case err == nil, errors.Is(err, windows.Errno(1)): // S_OK, S_FALSE
		return nil
	default:
		return fmt.Errorf("uiaclient: CoInitializeEx: %w", err)
	}
}

// Automation is IUIAutomation.
type Automation struct{ com.Object }

// Automation5 is IUIAutomation5 (notification events).
type Automation5 struct{ Automation }

// New makes the system's UI Automation client (CUIAutomation8) as IUIAutomation5, which
// Windows 10 1709 and later have.
func New() (*Automation5, error) {
	if err := comobj.Load(); err != nil {
		return nil, err
	}
	a, err := com.CreateInstance[Automation5]("CUIAutomation8", &clsidCUIAutomation8, &IID_IUIAutomation5)
	if err != nil {
		return nil, err
	}
	return a, nil
}

// ElementFromHandle is the element of a window.
func (a *Automation) ElementFromHandle(hwnd uintptr) (*Element, error) {
	out := heap[*Element]()
	r, _, _ := syscall.SyscallN(a.Method(6), uintptr(unsafe.Pointer(a)), hwnd, uintptr(unsafe.Pointer(out)))
	if err := com.Check("IUIAutomation::ElementFromHandle", com.HRESULT(r)); err != nil {
		return nil, err
	}
	return *out, nil
}

// elementFromPoint386 is ElementFromPoint on 386, where the POINT passed by value is two
// words, x first (the SDK checker counts the x64 form in the method).
func elementFromPoint386(a *Automation, x, y int32, out **Element) (*Element, error) {
	r, _, _ := syscall.SyscallN(a.Method(7), uintptr(unsafe.Pointer(a)), uintptr(uint32(x)), uintptr(uint32(y)), uintptr(unsafe.Pointer(out)))
	if err := com.Check("IUIAutomation::ElementFromPoint", com.HRESULT(r)); err != nil {
		return nil, err
	}
	return *out, nil
}

// ElementFromPoint is the element at a screen point in physical pixels (a POINT passed by
// value: 8 bytes, one word on a 64-bit target, two on 386).
func (a *Automation) ElementFromPoint(x, y int32) (*Element, error) {
	out := heap[*Element]()
	if !com.Wide {
		return elementFromPoint386(a, x, y, out)
	}
	r, _, _ := syscall.SyscallN(a.Method(7), uintptr(unsafe.Pointer(a)), com.Pack2U(uint32(x), uint32(y)), uintptr(unsafe.Pointer(out)))
	if err := com.Check("IUIAutomation::ElementFromPoint", com.HRESULT(r)); err != nil {
		return nil, err
	}
	return *out, nil
}

// CreateTrueCondition is the condition every element meets.
func (a *Automation) CreateTrueCondition() (*Condition, error) {
	out := heap[*Condition]()
	r, _, _ := syscall.SyscallN(a.Method(21), uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(out)))
	if err := com.Check("IUIAutomation::CreateTrueCondition", com.HRESULT(r)); err != nil {
		return nil, err
	}
	return *out, nil
}

// CreatePropertyCondition is the condition that property id equals v (a VARIANT passed by
// value: a pointer to a copy, on the x64 convention).
func (a *Automation) CreatePropertyCondition(id int32, v *comobj.Variant) (*Condition, error) {
	out := heap[*Condition]()
	val := heap[comobj.Variant]()
	*val = *v
	r, _, _ := syscall.SyscallN(a.Method(23), uintptr(unsafe.Pointer(a)), uintptr(id), uintptr(unsafe.Pointer(val)),
		uintptr(unsafe.Pointer(out)))
	if err := com.Check("IUIAutomation::CreatePropertyCondition", com.HRESULT(r)); err != nil {
		return nil, err
	}
	return *out, nil
}

// StringCondition is the condition that property id is the text s.
func (a *Automation) StringCondition(id int32, s string) (*Condition, error) {
	var v comobj.Variant
	v.SetPointer(comobj.VT_BSTR, comobj.NewBSTR(heap[[]uint16](), s))
	defer comobj.ClearVariant(&v)
	return a.CreatePropertyCondition(id, &v)
}

// IntCondition is the condition that property id is the number n (a control type).
func (a *Automation) IntCondition(id, n int32) (*Condition, error) {
	var v comobj.Variant
	v.SetI4(n)
	return a.CreatePropertyCondition(id, &v)
}

// get_ControlViewWalker is the walker of the control view.
func (a *Automation) get_ControlViewWalker() (*TreeWalker, error) {
	out := heap[*TreeWalker]()
	r, _, _ := syscall.SyscallN(a.Method(14), uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(out)))
	if err := com.Check("IUIAutomation::get_ControlViewWalker", com.HRESULT(r)); err != nil {
		return nil, err
	}
	return *out, nil
}

// get_RawViewWalker is the walker of the raw view.
func (a *Automation) get_RawViewWalker() (*TreeWalker, error) {
	out := heap[*TreeWalker]()
	r, _, _ := syscall.SyscallN(a.Method(16), uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(out)))
	if err := com.Check("IUIAutomation::get_RawViewWalker", com.HRESULT(r)); err != nil {
		return nil, err
	}
	return *out, nil
}

// ControlViewWalker and RawViewWalker are the walkers of the two views.
func (a *Automation) ControlViewWalker() (*TreeWalker, error) { return a.get_ControlViewWalker() }
func (a *Automation) RawViewWalker() (*TreeWalker, error)     { return a.get_RawViewWalker() }

// AddAutomationEventHandler subscribes h to event id raised under e (a live region's
// change, an invoke).
func (a *Automation) AddAutomationEventHandler(id int32, e *Element, scope int32, h *Handler) error {
	r, _, _ := syscall.SyscallN(a.Method(32), uintptr(unsafe.Pointer(a)), uintptr(id), uintptr(unsafe.Pointer(e)), uintptr(scope), 0, h.iface())
	return com.Check("IUIAutomation::AddAutomationEventHandler", com.HRESULT(r))
}

// AddFocusChangedEventHandler subscribes h to every focus change on the desktop.
func (a *Automation) AddFocusChangedEventHandler(h *Handler) error {
	r, _, _ := syscall.SyscallN(a.Method(39), uintptr(unsafe.Pointer(a)), 0, h.iface())
	return com.Check("IUIAutomation::AddFocusChangedEventHandler", com.HRESULT(r))
}

// AddStructureChangedEventHandler subscribes h to structure changes under e.
func (a *Automation) AddStructureChangedEventHandler(e *Element, scope int32, h *Handler) error {
	r, _, _ := syscall.SyscallN(a.Method(37), uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(e)), uintptr(scope), 0, h.iface())
	return com.Check("IUIAutomation::AddStructureChangedEventHandler", com.HRESULT(r))
}

// RemoveAllEventHandlers unsubscribes every handler of this client.
func (a *Automation) RemoveAllEventHandlers() error {
	r, _, _ := syscall.SyscallN(a.Method(41), uintptr(unsafe.Pointer(a)))
	return com.Check("IUIAutomation::RemoveAllEventHandlers", com.HRESULT(r))
}

// AddNotificationEventHandler subscribes h to the notifications raised under e.
func (a *Automation5) AddNotificationEventHandler(e *Element, scope int32, h *Handler) error {
	r, _, _ := syscall.SyscallN(a.Method(68), uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(e)), uintptr(scope), 0, h.iface())
	return com.Check("IUIAutomation5::AddNotificationEventHandler", com.HRESULT(r))
}

// Condition is IUIAutomationCondition.
type Condition struct{ com.Object }

// Element is IUIAutomationElement.
type Element struct{ com.Object }

// FindFirst is the first element in scope that meets c, nil for none.
func (e *Element) FindFirst(scope int32, c *Condition) (*Element, error) {
	out := heap[*Element]()
	r, _, _ := syscall.SyscallN(e.Method(5), uintptr(unsafe.Pointer(e)), uintptr(scope), uintptr(unsafe.Pointer(c)),
		uintptr(unsafe.Pointer(out)))
	if err := com.Check("IUIAutomationElement::FindFirst", com.HRESULT(r)); err != nil {
		return nil, err
	}
	return *out, nil
}

// FindAll is every element in scope that meets c.
func (e *Element) FindAll(scope int32, c *Condition) (*ElementArray, error) {
	out := heap[*ElementArray]()
	r, _, _ := syscall.SyscallN(e.Method(6), uintptr(unsafe.Pointer(e)), uintptr(scope), uintptr(unsafe.Pointer(c)),
		uintptr(unsafe.Pointer(out)))
	if err := com.Check("IUIAutomationElement::FindAll", com.HRESULT(r)); err != nil {
		return nil, err
	}
	return *out, nil
}

// GetCurrentPropertyValue is property id now; the caller clears the variant.
func (e *Element) GetCurrentPropertyValue(id int32) (comobj.Variant, error) {
	out := heap[comobj.Variant]()
	r, _, _ := syscall.SyscallN(e.Method(10), uintptr(unsafe.Pointer(e)), uintptr(id), uintptr(unsafe.Pointer(out)))
	if err := com.Check("IUIAutomationElement::GetCurrentPropertyValue", com.HRESULT(r)); err != nil {
		return comobj.Variant{}, err
	}
	return *out, nil
}

// GetCurrentPattern is the element's pattern object, 0 when it has none.
func (e *Element) GetCurrentPattern(pattern int32) (*com.Object, error) {
	out := heap[*com.Object]()
	r, _, _ := syscall.SyscallN(e.Method(16), uintptr(unsafe.Pointer(e)), uintptr(pattern), uintptr(unsafe.Pointer(out)))
	if err := com.Check("IUIAutomationElement::GetCurrentPattern", com.HRESULT(r)); err != nil {
		return nil, err
	}
	return *out, nil
}

// get_CurrentName is the element's name.
func (e *Element) get_CurrentName() (string, error) {
	out := heap[uintptr]()
	r, _, _ := syscall.SyscallN(e.Method(23), uintptr(unsafe.Pointer(e)), uintptr(unsafe.Pointer(out)))
	if err := com.Check("IUIAutomationElement::get_CurrentName", com.HRESULT(r)); err != nil {
		return "", err
	}
	s := comobj.BSTRString(*out)
	comobj.FreeBSTR(*out)
	return s, nil
}

// get_CurrentControlType is the element's control type id.
func (e *Element) get_CurrentControlType() (int32, error) {
	out := heap[int32]()
	r, _, _ := syscall.SyscallN(e.Method(21), uintptr(unsafe.Pointer(e)), uintptr(unsafe.Pointer(out)))
	return *out, com.Check("IUIAutomationElement::get_CurrentControlType", com.HRESULT(r))
}

// get_CurrentAutomationId is the element's automation id.
func (e *Element) get_CurrentAutomationId() (string, error) {
	out := heap[uintptr]()
	r, _, _ := syscall.SyscallN(e.Method(29), uintptr(unsafe.Pointer(e)), uintptr(unsafe.Pointer(out)))
	if err := com.Check("IUIAutomationElement::get_CurrentAutomationId", com.HRESULT(r)); err != nil {
		return "", err
	}
	s := comobj.BSTRString(*out)
	comobj.FreeBSTR(*out)
	return s, nil
}

// GetRuntimeId is the element's runtime id.
func (e *Element) GetRuntimeId() ([]int32, error) {
	out := heap[uintptr]()
	r, _, _ := syscall.SyscallN(e.Method(4), uintptr(unsafe.Pointer(e)), uintptr(unsafe.Pointer(out)))
	if err := com.Check("IUIAutomationElement::GetRuntimeId", com.HRESULT(r)); err != nil {
		return nil, err
	}
	defer comobj.DestroyArray(*out)
	return comobj.I4Values(*out), nil
}

// RECT is a Win32 RECT.
type RECT struct{ Left, Top, Right, Bottom int32 }

// get_CurrentBoundingRectangle is the element's screen rectangle.
func (e *Element) get_CurrentBoundingRectangle() (RECT, error) {
	out := heap[RECT]()
	r, _, _ := syscall.SyscallN(e.Method(43), uintptr(unsafe.Pointer(e)), uintptr(unsafe.Pointer(out)))
	return *out, com.Check("IUIAutomationElement::get_CurrentBoundingRectangle", com.HRESULT(r))
}

// Name, ControlType, AutomationID and Bounds are the element's properties now.
func (e *Element) Name() (string, error)         { return e.get_CurrentName() }
func (e *Element) ControlType() (int32, error)   { return e.get_CurrentControlType() }
func (e *Element) AutomationID() (string, error) { return e.get_CurrentAutomationId() }
func (e *Element) Bounds() (RECT, error)         { return e.get_CurrentBoundingRectangle() }

// Pattern is the element's pattern object as *T (of interface iid), nil when the element
// does not have it.
func Pattern[T any](e *Element, pattern int32, iid *com.GUID) (*T, error) {
	o, err := e.GetCurrentPattern(pattern)
	if err != nil || o == nil {
		return nil, err
	}
	defer o.Release()
	p, hr := com.Query[T](o, iid)
	if hr.Failed() {
		return nil, com.Check("QueryInterface(pattern)", hr)
	}
	return p, nil
}

// ElementArray is IUIAutomationElementArray.
type ElementArray struct{ com.Object }

// get_Length is the number of elements.
func (a *ElementArray) get_Length() (int, error) {
	out := heap[int32]()
	r, _, _ := syscall.SyscallN(a.Method(3), uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(out)))
	return int(*out), com.Check("IUIAutomationElementArray::get_Length", com.HRESULT(r))
}

// GetElement is element i, with a reference the caller releases.
func (a *ElementArray) GetElement(i int) (*Element, error) {
	out := heap[*Element]()
	r, _, _ := syscall.SyscallN(a.Method(4), uintptr(unsafe.Pointer(a)), uintptr(int32(i)), uintptr(unsafe.Pointer(out)))
	if err := com.Check("IUIAutomationElementArray::GetElement", com.HRESULT(r)); err != nil {
		return nil, err
	}
	return *out, nil
}

// Len is the number of elements.
func (a *ElementArray) Len() (int, error) { return a.get_Length() }

// TreeWalker is IUIAutomationTreeWalker.
type TreeWalker struct{ com.Object }

// GetParentElement is e's parent, nil for none.
func (w *TreeWalker) GetParentElement(e *Element) (*Element, error) {
	out := heap[*Element]()
	r, _, _ := syscall.SyscallN(w.Method(3), uintptr(unsafe.Pointer(w)), uintptr(unsafe.Pointer(e)), uintptr(unsafe.Pointer(out)))
	if err := com.Check("IUIAutomationTreeWalker::GetParentElement", com.HRESULT(r)); err != nil {
		return nil, err
	}
	return *out, nil
}

// GetFirstChildElement is e's first child, nil for none.
func (w *TreeWalker) GetFirstChildElement(e *Element) (*Element, error) {
	out := heap[*Element]()
	r, _, _ := syscall.SyscallN(w.Method(4), uintptr(unsafe.Pointer(w)), uintptr(unsafe.Pointer(e)), uintptr(unsafe.Pointer(out)))
	if err := com.Check("IUIAutomationTreeWalker::GetFirstChildElement", com.HRESULT(r)); err != nil {
		return nil, err
	}
	return *out, nil
}

// GetNextSiblingElement is e's next sibling, nil for none.
func (w *TreeWalker) GetNextSiblingElement(e *Element) (*Element, error) {
	out := heap[*Element]()
	r, _, _ := syscall.SyscallN(w.Method(6), uintptr(unsafe.Pointer(w)), uintptr(unsafe.Pointer(e)), uintptr(unsafe.Pointer(out)))
	if err := com.Check("IUIAutomationTreeWalker::GetNextSiblingElement", com.HRESULT(r)); err != nil {
		return nil, err
	}
	return *out, nil
}

// GridPattern is IUIAutomationGridPattern.
type GridPattern struct{ com.Object }

// GetItem is the cell at row and column.
func (g *GridPattern) GetItem(row, col int) (*Element, error) {
	out := heap[*Element]()
	r, _, _ := syscall.SyscallN(g.Method(3), uintptr(unsafe.Pointer(g)), uintptr(int32(row)), uintptr(int32(col)), uintptr(unsafe.Pointer(out)))
	if err := com.Check("IUIAutomationGridPattern::GetItem", com.HRESULT(r)); err != nil {
		return nil, err
	}
	return *out, nil
}

// get_CurrentRowCount is the number of rows.
func (g *GridPattern) get_CurrentRowCount() (int, error) {
	out := heap[int32]()
	r, _, _ := syscall.SyscallN(g.Method(4), uintptr(unsafe.Pointer(g)), uintptr(unsafe.Pointer(out)))
	return int(*out), com.Check("IUIAutomationGridPattern::get_CurrentRowCount", com.HRESULT(r))
}

// get_CurrentColumnCount is the number of columns.
func (g *GridPattern) get_CurrentColumnCount() (int, error) {
	out := heap[int32]()
	r, _, _ := syscall.SyscallN(g.Method(5), uintptr(unsafe.Pointer(g)), uintptr(unsafe.Pointer(out)))
	return int(*out), com.Check("IUIAutomationGridPattern::get_CurrentColumnCount", com.HRESULT(r))
}

// RowCount and ColumnCount are the grid's size.
func (g *GridPattern) RowCount() (int, error)    { return g.get_CurrentRowCount() }
func (g *GridPattern) ColumnCount() (int, error) { return g.get_CurrentColumnCount() }

// InvokePattern is IUIAutomationInvokePattern.
type InvokePattern struct{ com.Object }

// Invoke presses the element.
func (p *InvokePattern) Invoke() error {
	r, _, _ := syscall.SyscallN(p.Method(3), uintptr(unsafe.Pointer(p)))
	return com.Check("IUIAutomationInvokePattern::Invoke", com.HRESULT(r))
}

// SelectionItemPattern is IUIAutomationSelectionItemPattern.
type SelectionItemPattern struct{ com.Object }

// Select selects only this item.
func (p *SelectionItemPattern) Select() error {
	r, _, _ := syscall.SyscallN(p.Method(3), uintptr(unsafe.Pointer(p)))
	return com.Check("IUIAutomationSelectionItemPattern::Select", com.HRESULT(r))
}

// get_CurrentIsSelected reports whether the item is selected.
func (p *SelectionItemPattern) get_CurrentIsSelected() (bool, error) {
	out := heap[int32]()
	r, _, _ := syscall.SyscallN(p.Method(6), uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(out)))
	return *out != 0, com.Check("IUIAutomationSelectionItemPattern::get_CurrentIsSelected", com.HRESULT(r))
}

// IsSelected reports whether the item is selected.
func (p *SelectionItemPattern) IsSelected() (bool, error) { return p.get_CurrentIsSelected() }

// ValuePattern is IUIAutomationValuePattern.
type ValuePattern struct{ com.Object }

// SetValue sets the value.
func (p *ValuePattern) SetValue(s string) error {
	b := comobj.NewBSTR(heap[[]uint16](), s)
	defer comobj.FreeBSTR(b)
	r, _, _ := syscall.SyscallN(p.Method(3), uintptr(unsafe.Pointer(p)), b)
	return com.Check("IUIAutomationValuePattern::SetValue", com.HRESULT(r))
}

// get_CurrentValue is the value.
func (p *ValuePattern) get_CurrentValue() (string, error) {
	out := heap[uintptr]()
	r, _, _ := syscall.SyscallN(p.Method(4), uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(out)))
	if err := com.Check("IUIAutomationValuePattern::get_CurrentValue", com.HRESULT(r)); err != nil {
		return "", err
	}
	s := comobj.BSTRString(*out)
	comobj.FreeBSTR(*out)
	return s, nil
}

// Value is the value.
func (p *ValuePattern) Value() (string, error) { return p.get_CurrentValue() }

// RangeValuePattern is IUIAutomationRangeValuePattern.
type RangeValuePattern struct{ com.Object }

// SetValue sets the number (a double: its bits travel in the integer argument, which the
// runtime's system call also loads into XMM1).
func (p *RangeValuePattern) SetValue(v float64) error {
	r, _, _ := syscall.SyscallN(p.Method(3), uintptr(unsafe.Pointer(p)), uintptr(float64bits(v)))
	return com.Check("IUIAutomationRangeValuePattern::SetValue", com.HRESULT(r))
}

// get_CurrentValue is the number.
func (p *RangeValuePattern) get_CurrentValue() (float64, error) {
	out := heap[float64]()
	r, _, _ := syscall.SyscallN(p.Method(4), uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(out)))
	return *out, com.Check("IUIAutomationRangeValuePattern::get_CurrentValue", com.HRESULT(r))
}

// Value is the number.
func (p *RangeValuePattern) Value() (float64, error) { return p.get_CurrentValue() }

// TogglePattern is IUIAutomationTogglePattern.
type TogglePattern struct{ com.Object }

// Toggle toggles the element.
func (p *TogglePattern) Toggle() error {
	r, _, _ := syscall.SyscallN(p.Method(3), uintptr(unsafe.Pointer(p)))
	return com.Check("IUIAutomationTogglePattern::Toggle", com.HRESULT(r))
}

// get_CurrentToggleState is the toggle state (0 off, 1 on, 2 indeterminate).
func (p *TogglePattern) get_CurrentToggleState() (int32, error) {
	out := heap[int32]()
	r, _, _ := syscall.SyscallN(p.Method(4), uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(out)))
	return *out, com.Check("IUIAutomationTogglePattern::get_CurrentToggleState", com.HRESULT(r))
}

// State is the toggle state.
func (p *TogglePattern) State() (int32, error) { return p.get_CurrentToggleState() }

func float64bits(v float64) uint64 { return *(*uint64)(unsafe.Pointer(&v)) }
