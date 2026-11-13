//go:build windows

package comobj

import (
	"syscall"
	"unicode/utf16"
	"unsafe"

	"downex/ui/win/internal/com"

	"golang.org/x/sys/windows"
)

// The variant types used here (wtypes.h, VARENUM).
const (
	VT_EMPTY   = 0
	VT_I4      = 3
	VT_R8      = 5
	VT_BSTR    = 8
	VT_BOOL    = 11
	VT_UNKNOWN = 13
	VT_ARRAY   = 0x2000
)

// Variant is a VARIANT (oaidl.h): the type, three reserved words and a 16-byte union, of
// which the values used here take the first 8 bytes.
type Variant struct {
	VT  uint16
	_   [3]uint16
	Val uint64
	_   uint64
}

// SetI4 makes v a VT_I4.
func (v *Variant) SetI4(n int32) { *v = Variant{VT: VT_I4, Val: uint64(uint32(n))} }

// SetBool makes v a VT_BOOL (VARIANT_TRUE is -1).
func (v *Variant) SetBool(b bool) {
	*v = Variant{VT: VT_BOOL}
	if b {
		v.Val = 0xFFFF
	}
}

// SetR8 makes v a VT_R8.
func (v *Variant) SetR8(f float64) {
	*v = Variant{VT: VT_R8}
	*(*float64)(unsafe.Pointer(&v.Val)) = f
}

// SetPointer makes v a variant of type vt that holds a pointer: a BSTR, an IUnknown or a
// SAFEARRAY. The variant owns what p points to.
func (v *Variant) SetPointer(vt uint16, p uintptr) { *v = Variant{VT: vt, Val: uint64(p)} }

// I4 is a VT_I4's value.
func (v *Variant) I4() int32 { return int32(uint32(v.Val)) }

// Bool is a VT_BOOL's value.
func (v *Variant) Bool() bool { return uint16(v.Val) != 0 }

// R8 is a VT_R8's value.
func (v *Variant) R8() float64 { return *(*float64)(unsafe.Pointer(&v.Val)) }

// Pointer is the pointer a BSTR, IUnknown or SAFEARRAY variant holds.
func (v *Variant) Pointer() uintptr { return uintptr(v.Val) }

var (
	modOleAut32               = windows.NewLazySystemDLL("oleaut32.dll")
	procSysAllocStringLen     = modOleAut32.NewProc("SysAllocStringLen")
	procSysFreeString         = modOleAut32.NewProc("SysFreeString")
	procSysStringLen          = modOleAut32.NewProc("SysStringLen")
	procSafeArrayCreateVector = modOleAut32.NewProc("SafeArrayCreateVector")
	procSafeArrayPutElement   = modOleAut32.NewProc("SafeArrayPutElement")
	procSafeArrayGetElement   = modOleAut32.NewProc("SafeArrayGetElement")
	procSafeArrayGetLBound    = modOleAut32.NewProc("SafeArrayGetLBound")
	procSafeArrayGetUBound    = modOleAut32.NewProc("SafeArrayGetUBound")
	procSafeArrayAccessData   = modOleAut32.NewProc("SafeArrayAccessData")
	procSafeArrayUnaccessData = modOleAut32.NewProc("SafeArrayUnaccessData")
	procSafeArrayDestroy      = modOleAut32.NewProc("SafeArrayDestroy")
	procVariantClear          = modOleAut32.NewProc("VariantClear")
)

// Load finds the OLE Automation functions, so that a missing one fails up front.
func Load() error {
	for _, p := range []*windows.LazyProc{procSysAllocStringLen, procSysFreeString, procSysStringLen,
		procSafeArrayCreateVector, procSafeArrayPutElement, procSafeArrayGetElement, procSafeArrayGetLBound,
		procSafeArrayGetUBound, procSafeArrayAccessData, procSafeArrayUnaccessData, procSafeArrayDestroy, procVariantClear} {
		if err := p.Find(); err != nil {
			return err
		}
	}
	return nil
}

// AppendUTF16 appends s as UTF-16 to dst. Invalid UTF-8 becomes U+FFFD, as utf16.Encode
// does.
func AppendUTF16(dst []uint16, s string) []uint16 {
	for _, r := range s {
		dst = utf16.AppendRune(dst, r)
	}
	return dst
}

// NewBSTR is s as a BSTR the caller owns (0 when the system is out of memory). buf is
// scratch space for the UTF-16 text, kept by the caller between calls.
func NewBSTR(buf *[]uint16, s string) uintptr {
	b := AppendUTF16((*buf)[:0], s)
	*buf = b
	var p *uint16
	if len(b) > 0 {
		p = &b[0]
	}
	r, _, _ := syscall.SyscallN(procSysAllocStringLen.Addr(), uintptr(unsafe.Pointer(p)), uintptr(len(b)))
	return r
}

// FreeBSTR frees a BSTR (0 is allowed).
func FreeBSTR(b uintptr) {
	if b != 0 {
		syscall.SyscallN(procSysFreeString.Addr(), b)
	}
}

// BSTRString is the text of a BSTR, "" for 0.
func BSTRString(b uintptr) string {
	if b == 0 {
		return ""
	}
	n, _, _ := syscall.SyscallN(procSysStringLen.Addr(), b)
	if n == 0 {
		return ""
	}
	return string(utf16.Decode(unsafe.Slice((*uint16)(com.Addr(b)), int(n))))
}

// ClearVariant frees what v holds (VariantClear) and makes it VT_EMPTY.
func ClearVariant(v *Variant) {
	syscall.SyscallN(procVariantClear.Addr(), uintptr(unsafe.Pointer(v)))
}

// NewI4Array is a one-dimensional SAFEARRAY of VT_I4 holding vals, which the caller owns.
func NewI4Array(vals []int32) (uintptr, error) {
	psa, _, _ := syscall.SyscallN(procSafeArrayCreateVector.Addr(), VT_I4, 0, uintptr(len(vals)))
	if psa == 0 {
		return 0, &com.Error{Op: "SafeArrayCreateVector(VT_I4)", HRESULT: com.E_OUTOFMEMORY}
	}
	if len(vals) == 0 {
		return psa, nil
	}
	var data uintptr
	r, _, _ := syscall.SyscallN(procSafeArrayAccessData.Addr(), psa, uintptr(unsafe.Pointer(&data)))
	if err := com.Check("SafeArrayAccessData", com.HRESULT(r)); err != nil {
		DestroyArray(psa)
		return 0, err
	}
	copy(unsafe.Slice((*int32)(com.Addr(data)), len(vals)), vals)
	syscall.SyscallN(procSafeArrayUnaccessData.Addr(), psa)
	return psa, nil
}

// NewUnknownArray is a one-dimensional SAFEARRAY of VT_UNKNOWN holding the interface
// pointers objs, which the caller owns; the array takes a reference to each.
func NewUnknownArray(objs []uintptr) (uintptr, error) {
	psa, _, _ := syscall.SyscallN(procSafeArrayCreateVector.Addr(), VT_UNKNOWN, 0, uintptr(len(objs)))
	if psa == 0 {
		return 0, &com.Error{Op: "SafeArrayCreateVector(VT_UNKNOWN)", HRESULT: com.E_OUTOFMEMORY}
	}
	for i, o := range objs {
		idx := int32(i)
		r, _, _ := syscall.SyscallN(procSafeArrayPutElement.Addr(), psa, uintptr(unsafe.Pointer(&idx)), o)
		if err := com.Check("SafeArrayPutElement", com.HRESULT(r)); err != nil {
			DestroyArray(psa)
			return 0, err
		}
	}
	return psa, nil
}

// ArrayLen is the number of elements of a one-dimensional SAFEARRAY.
func ArrayLen(psa uintptr) int {
	if psa == 0 {
		return 0
	}
	var lo, hi int32
	syscall.SyscallN(procSafeArrayGetLBound.Addr(), psa, 1, uintptr(unsafe.Pointer(&lo)))
	syscall.SyscallN(procSafeArrayGetUBound.Addr(), psa, 1, uintptr(unsafe.Pointer(&hi)))
	return int(hi - lo + 1)
}

// I4Values are the elements of a one-dimensional SAFEARRAY of VT_I4.
func I4Values(psa uintptr) []int32 {
	n := ArrayLen(psa)
	out := make([]int32, 0, n)
	for i := 0; i < n; i++ {
		idx, v := int32(i), int32(0)
		syscall.SyscallN(procSafeArrayGetElement.Addr(), psa, uintptr(unsafe.Pointer(&idx)), uintptr(unsafe.Pointer(&v)))
		out = append(out, v)
	}
	return out
}

// UnknownValues are the elements of a one-dimensional SAFEARRAY of VT_UNKNOWN, each with a
// reference the caller releases.
func UnknownValues(psa uintptr) []uintptr {
	n := ArrayLen(psa)
	out := make([]uintptr, 0, n)
	for i := 0; i < n; i++ {
		idx, v := int32(i), uintptr(0)
		syscall.SyscallN(procSafeArrayGetElement.Addr(), psa, uintptr(unsafe.Pointer(&idx)), uintptr(unsafe.Pointer(&v)))
		out = append(out, v)
	}
	return out
}

// DestroyArray frees a SAFEARRAY and what it holds (0 is allowed).
func DestroyArray(psa uintptr) {
	if psa != 0 {
		syscall.SyscallN(procSafeArrayDestroy.Addr(), psa)
	}
}
