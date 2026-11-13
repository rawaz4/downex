//go:build windows

// Package com is the base of ui/win's hand-written COM bindings: the object header every
// interface pointer starts with, GUIDs, HRESULT errors, and the few ole32 calls the bindings
// need.
//
// # How a binding calls a method
//
// A binding type (d2d.RenderTarget, dwrite.TextLayout, ...) embeds Object, so a pointer to
// it is the interface pointer itself, pointing into memory the system owns. Each method is a
// small wrapper with a fixed number of arguments that reads its function from the vtable
// slot written in the wrapper (Method(17)) and calls it through syscall.SyscallN.
//
// Pointer arguments stay typed pointers up to the SyscallN call expression, where they are
// converted to uintptr. syscall.SyscallN keeps what they point to alive for the call, and as
// they are typed until then, a stack copy before the call adjusts them. Nothing escapes, so
// a call allocates nothing (tests check it with testing.AllocsPerRun). This is safe because
// no Direct2D, DirectWrite or WIC method used here calls back into Go: during the call the
// goroutine's stack cannot grow or move. Win32 calls that can run a window procedure (and
// so Go code) take pointers to heap memory instead; see package w32.
//
// Floats travel by value as their bits in a uintptr: on amd64 the Go runtime's asmstdcall
// copies the first four integer arguments into XMM0-XMM3 too, and later arguments go on the
// stack, where a FLOAT sits in the low half of its slot. 8-byte structs passed by value
// (D2D1_POINT_2F, DWRITE_TEXT_RANGE) travel packed in one uintptr, as the x64 convention
// passes them. Methods that return a FLOAT (in XMM0) cannot be called; ones that return a
// struct get it through the hidden pointer MSVC passes after this. windows/arm64 passes
// FLOATs in V0-V7, which syscall.SyscallN does not load, so ui/win refuses to run there
// (see Supported).
//
// Every slot number and GUID in the bindings is checked against the Windows SDK headers by
// the bindings' tests (package sdkheader).
package com

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// maxSlots bounds the vtable view; no interface used here has more methods.
const maxSlots = 256

// Object is the start of every COM object: the address of its table of methods. It is
// never made in Go; pointers to it come from the system.
type Object struct {
	vtbl *[maxSlots]uintptr
}

// Method is the address of the method in slot of o's vtable.
func (o *Object) Method(slot int) uintptr { return o.vtbl[slot] }

// QueryInterface asks o for the interface iid and stores it in out (an interface pointer's
// address). It is IUnknown's slot 0.
func (o *Object) QueryInterface(iid *GUID, out unsafe.Pointer) HRESULT {
	r, _, _ := syscall.SyscallN(o.Method(0), uintptr(unsafe.Pointer(o)), uintptr(unsafe.Pointer(iid)), uintptr(out))
	return HRESULT(r)
}

// AddRef adds a reference to o (IUnknown's slot 1).
func (o *Object) AddRef() uint32 {
	r, _, _ := syscall.SyscallN(o.Method(1), uintptr(unsafe.Pointer(o)))
	return uint32(r)
}

// Release drops a reference to o (IUnknown's slot 2).
func (o *Object) Release() uint32 {
	r, _, _ := syscall.SyscallN(o.Method(2), uintptr(unsafe.Pointer(o)))
	return uint32(r)
}

// Releaser is a binding type's pointer: anything that embeds Object.
type Releaser[T any] interface {
	*T
	Release() uint32
}

// Release releases *p when it is not nil and sets it to nil, so a release can be repeated.
func Release[T any, P Releaser[T]](p *P) {
	if *p != nil {
		(*p).Release()
		*p = nil
	}
}

// Query asks o for the interface iid as a *T, nil when o does not have it.
func Query[T any](o *Object, iid *GUID) (*T, HRESULT) {
	var out *T
	hr := o.QueryInterface(iid, unsafe.Pointer(&out))
	if hr.Failed() {
		return nil, hr
	}
	return out, hr
}

// GUID is a COM GUID (an IID or CLSID) in memory layout.
type GUID = windows.GUID

// MustGUID parses s, a uuid such as "06152247-6f50-465a-9245-118bfd3b6007". It panics on
// a malformed string: GUIDs are constants of the bindings.
func MustGUID(s string) GUID {
	g, err := ParseGUID(s)
	if err != nil {
		panic(err)
	}
	return g
}

// ParseGUID parses a uuid string without braces.
func ParseGUID(s string) (GUID, error) {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return GUID{}, fmt.Errorf("com: malformed GUID %q", s)
	}
	hex := func(part string, bits int) (uint64, error) { return strconv.ParseUint(part, 16, bits) }
	d1, err1 := hex(s[0:8], 32)
	d2, err2 := hex(s[9:13], 16)
	d3, err3 := hex(s[14:18], 16)
	if err := errors.Join(err1, err2, err3); err != nil {
		return GUID{}, fmt.Errorf("com: malformed GUID %q: %w", s, err)
	}
	g := GUID{Data1: uint32(d1), Data2: uint16(d2), Data3: uint16(d3)}
	rest := s[19:23] + s[24:]
	for i := range g.Data4 {
		b, err := hex(rest[2*i:2*i+2], 8)
		if err != nil {
			return GUID{}, fmt.Errorf("com: malformed GUID %q: %w", s, err)
		}
		g.Data4[i] = byte(b)
	}
	return g, nil
}

// GUIDString formats g as ParseGUID reads it, lower case.
func GUIDString(g GUID) string {
	return fmt.Sprintf("%08x-%04x-%04x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		g.Data1, g.Data2, g.Data3, g.Data4[0], g.Data4[1], g.Data4[2], g.Data4[3],
		g.Data4[4], g.Data4[5], g.Data4[6], g.Data4[7])
}

// HRESULT is a COM status code.
type HRESULT uint32

// Some HRESULTs the bindings test for.
const (
	S_OK                    HRESULT = 0
	S_FALSE                 HRESULT = 1
	E_NOTIMPL               HRESULT = 0x80004001
	E_NOINTERFACE           HRESULT = 0x80004002
	E_POINTER               HRESULT = 0x80004003
	E_FAIL                  HRESULT = 0x80004005
	E_UNEXPECTED            HRESULT = 0x8000FFFF
	E_OUTOFMEMORY           HRESULT = 0x8007000E
	E_INVALIDARG            HRESULT = 0x80070057
	E_NOT_SUFFICIENT_BUFFER HRESULT = 0x8007007A
	RPC_E_CHANGED_MODE      HRESULT = 0x80010106
	CO_E_NOTINITIALIZED     HRESULT = 0x800401F0

	D2DERR_WRONG_STATE        HRESULT = 0x88990001
	D2DERR_NOT_INITIALIZED    HRESULT = 0x88990002
	D2DERR_UNSUPPORTED_OP     HRESULT = 0x88990003
	D2DERR_RECREATE_TARGET    HRESULT = 0x8899000C
	D2DERR_BAD_NUMBER         HRESULT = 0x88990011
	D2DERR_DISPLAY_STATE      HRESULT = 0x88990006
	D2DERR_PUSH_POP_UNBALANCE HRESULT = 0x88990016
	D2DERR_WRONG_FACTORY      HRESULT = 0x88990012

	DWRITE_E_NOFONT HRESULT = 0x88985002

	WINCODEC_ERR_COMPONENTNOTFOUND    HRESULT = 0x88982F50
	WINCODEC_ERR_BADIMAGE             HRESULT = 0x88982F60
	WINCODEC_ERR_BADHEADER            HRESULT = 0x88982F61
	WINCODEC_ERR_UNKNOWNIMAGEFORMAT   HRESULT = 0x88982F07
	WINCODEC_ERR_STREAMREAD           HRESULT = 0x88982F72
	WINCODEC_ERR_BADSTREAMDATA        HRESULT = 0x88982F84
	WINCODEC_ERR_FRAMEMISSING         HRESULT = 0x88982F62
	WINCODEC_ERR_UNSUPPORTEDOPERATION HRESULT = 0x88982F81
)

var names = map[HRESULT]string{
	S_FALSE:                           "S_FALSE",
	E_NOTIMPL:                         "E_NOTIMPL",
	E_NOINTERFACE:                     "E_NOINTERFACE",
	E_POINTER:                         "E_POINTER",
	E_FAIL:                            "E_FAIL",
	E_UNEXPECTED:                      "E_UNEXPECTED",
	E_OUTOFMEMORY:                     "E_OUTOFMEMORY",
	E_INVALIDARG:                      "E_INVALIDARG",
	E_NOT_SUFFICIENT_BUFFER:           "E_NOT_SUFFICIENT_BUFFER",
	RPC_E_CHANGED_MODE:                "RPC_E_CHANGED_MODE",
	CO_E_NOTINITIALIZED:               "CO_E_NOTINITIALIZED",
	D2DERR_WRONG_STATE:                "D2DERR_WRONG_STATE",
	D2DERR_NOT_INITIALIZED:            "D2DERR_NOT_INITIALIZED",
	D2DERR_UNSUPPORTED_OP:             "D2DERR_UNSUPPORTED_OPERATION",
	D2DERR_RECREATE_TARGET:            "D2DERR_RECREATE_TARGET",
	D2DERR_BAD_NUMBER:                 "D2DERR_BAD_NUMBER",
	D2DERR_DISPLAY_STATE:              "D2DERR_DISPLAY_STATE_INVALID",
	D2DERR_PUSH_POP_UNBALANCE:         "D2DERR_PUSH_POP_UNBALANCED",
	D2DERR_WRONG_FACTORY:              "D2DERR_WRONG_FACTORY",
	DWRITE_E_NOFONT:                   "DWRITE_E_NOFONT",
	WINCODEC_ERR_COMPONENTNOTFOUND:    "WINCODEC_ERR_COMPONENTNOTFOUND",
	WINCODEC_ERR_BADIMAGE:             "WINCODEC_ERR_BADIMAGE",
	WINCODEC_ERR_BADHEADER:            "WINCODEC_ERR_BADHEADER",
	WINCODEC_ERR_UNKNOWNIMAGEFORMAT:   "WINCODEC_ERR_UNKNOWNIMAGEFORMAT",
	WINCODEC_ERR_STREAMREAD:           "WINCODEC_ERR_STREAMREAD",
	WINCODEC_ERR_BADSTREAMDATA:        "WINCODEC_ERR_BADSTREAMDATA",
	WINCODEC_ERR_FRAMEMISSING:         "WINCODEC_ERR_FRAMEMISSING",
	WINCODEC_ERR_UNSUPPORTEDOPERATION: "WINCODEC_ERR_UNSUPPORTEDOPERATION",
}

// Failed reports a failure code (severity bit set).
func (hr HRESULT) Failed() bool { return int32(hr) < 0 }

// String is the code in hex, with its name when it is one of the known ones.
func (hr HRESULT) String() string {
	if n, ok := names[hr]; ok {
		return fmt.Sprintf("HRESULT %#08x %s", uint32(hr), n)
	}
	return fmt.Sprintf("HRESULT %#08x", uint32(hr))
}

// Error is a failed call: what was called and the HRESULT it returned.
type Error struct {
	// Op names the call, "ID2D1RenderTarget::EndDraw" or "CoCreateInstance(WICImagingFactory)".
	Op      string
	HRESULT HRESULT
}

func (e *Error) Error() string { return e.Op + ": " + e.HRESULT.String() }

// Check is nil for a success code and an *Error naming op otherwise.
func Check(op string, hr HRESULT) error {
	if !hr.Failed() {
		return nil
	}
	return &Error{Op: op, HRESULT: hr}
}

// Code is the HRESULT of err's *Error, or 0 when it has none.
func Code(err error) HRESULT {
	var e *Error
	if errors.As(err, &e) {
		return e.HRESULT
	}
	return 0
}

// Addr is the address a, which a system call returned, as a pointer: memory the system
// owns (an object, a locked HGLOBAL), never Go memory. It reads a's bits as a pointer
// rather than converting the integer, which unsafe.Pointer's rules reserve for Go memory.
func Addr(a uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&a)) }

// F is the bits of v, a FLOAT argument passed by value.
func F(v float32) uintptr { return uintptr(math.Float32bits(v)) }

// Wide reports a 64-bit target (amd64, arm64), where a structure of two 32-bit fields passed
// by value (a POINT, a D2D1_SIZE_U) is one argument word, the first field in the low half. On
// 386 it is two words, the first field first: a caller passes the fields as two arguments
// there (see w32.MonitorFromPoint).
const Wide = unsafe.Sizeof(uintptr(0)) == 8

// Pack2F packs two FLOATs (a D2D1_POINT_2F or D2D1_SIZE_F) passed by value: the one-word
// form of a 64-bit target (Wide).
func Pack2F(x, y float32) uintptr { return Pack2U(math.Float32bits(x), math.Float32bits(y)) }

// Pack2U packs two UINT32s (a DWRITE_TEXT_RANGE or D2D1_SIZE_U) passed by value: the
// one-word form of a 64-bit target (Wide). It is computed in uint64, so it compiles for 386
// too, where it would drop b: callers that run there pass a and b as two words.
//
// Only the calls that also run on 386 split their pairs (MonitorFromPoint, WindowFromPoint,
// CreateBitmap, the UI Automation client's ElementFromPoint). The Direct2D drawing calls
// (SetStartPoint, AddLine and the other pairs of floats) and DirectWrite's text ranges keep
// the one-word form: they never run on 386, since the native window refuses every target but
// amd64 when it starts (Supported, called by win.NewApp), and it is vetted, not run, there.
func Pack2U(a, b uint32) uintptr { return uintptr(uint64(a) | uint64(b)<<32) }

// Bool is a Win32 BOOL.
func Bool(b bool) uintptr {
	if b {
		return 1
	}
	return 0
}

// Supported is nil when goarch can call these bindings, and otherwise says why not. Only
// amd64 passes FLOAT arguments the way the bindings rely on (see the package doc).
func Supported(goarch string) error {
	if goarch == "amd64" {
		return nil
	}
	return fmt.Errorf("Direct2D and DirectWrite calls need the x64 (amd64) build: Go's windows/%s system calls cannot pass the FLOAT arguments they take by value", goarch)
}

var (
	modOle32             = windows.NewLazySystemDLL("ole32.dll")
	procCoCreateInstance = modOle32.NewProc("CoCreateInstance")
	procCoIncrementMTA   = modOle32.NewProc("CoIncrementMTAUsage")
)

// CLSCTX_INPROC_SERVER is the class context of in-process servers such as WIC.
const CLSCTX_INPROC_SERVER = 0x1

// CreateInstance makes an object of class clsid and returns its interface iid as a *T.
func CreateInstance[T any](op string, clsid, iid *GUID) (*T, error) {
	if err := procCoCreateInstance.Find(); err != nil {
		return nil, err
	}
	var out *T
	r, _, _ := syscall.SyscallN(procCoCreateInstance.Addr(), uintptr(unsafe.Pointer(clsid)), 0, CLSCTX_INPROC_SERVER,
		uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out)))
	if err := Check("CoCreateInstance("+op+")", HRESULT(r)); err != nil {
		return nil, err
	}
	return out, nil
}

// KeepMTA makes the process's multithreaded apartment exist for the rest of its life
// (CoIncrementMTAUsage), so that threads that never initialised COM, which Go may run any
// goroutine on, can create and use free-threaded objects such as WIC's. A thread that
// initialised its own apartment (the UI thread's STA) keeps it.
func KeepMTA() error {
	if err := procCoIncrementMTA.Find(); err != nil {
		return err
	}
	var cookie uintptr
	r, _, _ := syscall.SyscallN(procCoIncrementMTA.Addr(), uintptr(unsafe.Pointer(&cookie)))
	return Check("CoIncrementMTAUsage", HRESULT(r))
}
