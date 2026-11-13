//go:build windows

// Package comobj is the base of the COM objects that ui/win/uia implements in Go: memory
// outside the Go heap for the objects and their method tables, the table of handles
// through which an object finds its Go state, and the OLE Automation values (BSTR,
// VARIANT, SAFEARRAY) that UI Automation passes.
//
// # Objects hold handles, never Go pointers
//
// COM keeps the interface pointers it is given for as long as its callers hold them, on
// other threads and behind other processes' proxies, where the Go collector cannot see
// them. So an object is a small block of LocalAlloc memory, laid out as a C++ object with
// several interfaces is: one vtable pointer per interface (an interface pointer is the
// address of its slot), then the reference count and a Handle. The Go side of the object
// lives in a Table and is found by that Handle. When the count reaches zero the owner
// frees the block and removes the handle, and from then on the handle finds nothing.
//
// # Methods
//
// A vtable is an array of callback addresses (syscall.NewCallback, or an assembly thunk)
// in memory of its own, made once per process. A method receives the interface pointer
// it was called through; Class.Object finds the object and the interface from it by the
// vtable the slot points to.
package comobj

import (
	"fmt"
	"sync/atomic"
	"unsafe"

	"downex/ui/win/internal/com"

	"golang.org/x/sys/windows"
)

const ptrSize = unsafe.Sizeof(uintptr(0))

// LMEM_FIXED | LMEM_ZEROINIT.
const lptr = 0x0040

// Alloc returns n zeroed bytes of LocalAlloc memory, which the Go collector neither moves
// nor frees.
func Alloc(n uintptr) (uintptr, error) {
	p, err := windows.LocalAlloc(lptr, uint32(n))
	if p == 0 {
		return 0, fmt.Errorf("comobj: LocalAlloc(%d): %w", n, err)
	}
	return p, nil
}

// FreeMem frees memory from Alloc.
func FreeMem(p uintptr) {
	if p != 0 {
		windows.LocalFree(windows.Handle(p))
	}
}

// Word is the machine word at address p (memory outside the Go heap).
func Word(p uintptr) uintptr { return *(*uintptr)(com.Addr(p)) }

// SetWord writes the machine word at address p.
func SetWord(p, v uintptr) { *(*uintptr)(com.Addr(p)) = v }

// Vtable writes the method addresses of one interface, IUnknown's three first, to memory
// of their own and returns its address. Vtables live as long as the process.
func Vtable(methods []uintptr) (uintptr, error) {
	p, err := Alloc(uintptr(len(methods)) * ptrSize)
	if err != nil {
		return 0, err
	}
	for i, m := range methods {
		SetWord(p+uintptr(i)*ptrSize, m)
	}
	return p, nil
}

// Class is the layout of one kind of object: its interfaces' vtables, in the order of the
// object's interface pointers. A vtable of 0 is an interface the class does not have.
type Class struct {
	vtbls []uintptr
}

// NewClass makes a class of objects with these interfaces.
func NewClass(vtbls []uintptr) *Class { return &Class{vtbls: vtbls} }

// Interfaces is the number of interface pointers an object of c has.
func (c *Class) Interfaces() int { return len(c.vtbls) }

func (c *Class) refsAt(obj uintptr) *int32 {
	return (*int32)(com.Addr(obj + uintptr(len(c.vtbls))*ptrSize))
}

func (c *Class) handleAt(obj uintptr) *Handle {
	return (*Handle)(com.Addr(obj + uintptr(len(c.vtbls))*ptrSize + 8))
}

// New makes an object of c with one reference and handle h.
func (c *Class) New(h Handle) (uintptr, error) {
	obj, err := Alloc(uintptr(len(c.vtbls))*ptrSize + 16)
	if err != nil {
		return 0, err
	}
	for i, v := range c.vtbls {
		SetWord(obj+uintptr(i)*ptrSize, v)
	}
	*c.refsAt(obj) = 1
	*c.handleAt(obj) = h
	return obj, nil
}

// Free frees an object of c. Its handle is cleared first, so a caller that keeps a
// pointer it no longer owns finds no object rather than another one's state.
func (c *Class) Free(obj uintptr) {
	if obj == 0 {
		return
	}
	*c.handleAt(obj) = 0
	FreeMem(obj)
}

// Iface is the interface pointer of the object's interface i.
func (c *Class) Iface(obj uintptr, i int) uintptr { return obj + uintptr(i)*ptrSize }

// Object is the object and the interface index of an interface pointer of class c, found
// by the vtable its slot points to; ok is false for a pointer that is not one of c's.
func (c *Class) Object(this uintptr) (obj uintptr, iface int, ok bool) {
	if this == 0 {
		return 0, -1, false
	}
	vt := Word(this)
	for i, v := range c.vtbls {
		if v != 0 && v == vt {
			return this - uintptr(i)*ptrSize, i, true
		}
	}
	return 0, -1, false
}

// Handle is the object's handle (0 once it is freed).
func (c *Class) Handle(obj uintptr) Handle { return *c.handleAt(obj) }

// AddRef adds a reference and returns the new count.
func (c *Class) AddRef(obj uintptr) int32 { return atomic.AddInt32(c.refsAt(obj), 1) }

// Release drops a reference and returns the new count; at 0 the caller frees the object.
func (c *Class) Release(obj uintptr) int32 { return atomic.AddInt32(c.refsAt(obj), -1) }

// Refs is the reference count now.
func (c *Class) Refs(obj uintptr) int32 { return atomic.LoadInt32(c.refsAt(obj)) }

// IID_IUnknown is IUnknown's interface id.
var IID_IUnknown = com.MustGUID("00000000-0000-0000-c000-000000000046")

// GUIDAt is the GUID a REFIID argument points to.
func GUIDAt(p uintptr) com.GUID { return *(*com.GUID)(com.Addr(p)) }
