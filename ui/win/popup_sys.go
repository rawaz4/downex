//go:build windows

package win

import (
	"syscall"
	"unsafe"

	"downex/ui/win/internal/w32"

	"golang.org/x/sys/windows"
)

// The Win32 calls the popup windows, their hooks, the tooltips and the field kinds need beyond
// package w32's, bound the same way (typed wrappers of a fixed number of arguments around
// syscall.SyscallN). The re-entrancy rule of package w32 holds: UpdateLayeredWindow and
// SetWindowsHookEx's hooks can run window procedures, so the pointers they take point to heap
// memory that stays referenced.
var (
	pUser32 = windows.NewLazySystemDLL("user32.dll")
	pGdi32  = windows.NewLazySystemDLL("gdi32.dll")
	pImm32  = windows.NewLazySystemDLL("imm32.dll")

	popProcSetWindowsHookExW   = pUser32.NewProc("SetWindowsHookExW")
	popProcUnhookWindowsHookEx = pUser32.NewProc("UnhookWindowsHookEx")
	popProcCallNextHookEx      = pUser32.NewProc("CallNextHookEx")
	popProcUpdateLayeredWindow = pUser32.NewProc("UpdateLayeredWindow")
	popProcSetTimer            = pUser32.NewProc("SetTimer")
	popProcKillTimer           = pUser32.NewProc("KillTimer")
	popProcGetForegroundWindow = pUser32.NewProc("GetForegroundWindow")
	popProcGetWindowThreadPID  = pUser32.NewProc("GetWindowThreadProcessId")
	popProcGetCursorInfo       = pUser32.NewProc("GetSystemMetrics")
	popProcCreateDIBSection    = pGdi32.NewProc("CreateDIBSection")
	popProcPCreateCompatibleDC = pGdi32.NewProc("CreateCompatibleDC")
	popProcPDeleteDC           = pGdi32.NewProc("DeleteDC")
	popProcPSelectObject       = pGdi32.NewProc("SelectObject")
	popProcImmAssociateContext = pImm32.NewProc("ImmAssociateContext")
	popProcImmGetCompStringW   = pImm32.NewProc("ImmGetCompositionStringW")
	popProcImmSetOpenStatus    = pImm32.NewProc("ImmSetOpenStatus")
	popProcImmGetOpenStatus    = pImm32.NewProc("ImmGetOpenStatus")
	popProcImmCreateContext    = pImm32.NewProc("ImmCreateContext")
	popProcImmDestroyContext   = pImm32.NewProc("ImmDestroyContext")
)

// The constants these calls take (WinUser.h, WinGDI.h, CommCtrl.h, imm.h).
const (
	popWHMouse       = 7 // WH_MOUSE
	popWHCallWndProc = 4 // WH_CALLWNDPROC
	popWHKeyboard    = 2 // WH_KEYBOARD
	popHCAction      = 0 // HC_ACTION
	popWSExLayered   = 0x00080000
	popWSExToolWin   = 0x00000080
	popULWAlpha      = 0x00000002
	popACSrcOver     = 0x00
	popACSrcAlpha    = 0x01
	popHTTransparent = ^uintptr(0) // HTTRANSPARENT, -1
	popHTClient      = 1
	popMANoActivate  = 3
	popSWShowNA      = 8
	popSWHide        = 0
	popWAInactive    = 0
	popSMCYCursor    = 14 // SM_CYCURSOR

	popWMMouseActivate    = 0x0021
	popWMActivate         = 0x0006
	popWMActivateApp      = 0x001C
	popWMWindowPosChanged = 0x0047
	popWMDestroy          = 0x0002
	popWMNCDestroy        = 0x0082
	popWMTimer            = 0x0113
	popWMNCHitTest        = 0x0084
	popWMSetCursor        = 0x0020
	popWMMouseMove        = 0x0200
	popWMLButtonDown      = 0x0201
	popWMLButtonUp        = 0x0202
	popWMRButtonDown      = 0x0204
	popWMRButtonUp        = 0x0205
	popWMMButtonDown      = 0x0207
	popWMMButtonUp        = 0x0208
	popWMMouseWheel       = 0x020A
	popWMXButtonDown      = 0x020B
	popWMXButtonUp        = 0x020C
	popWMMouseHWheel      = 0x020E
	popWMNCLButtonDown    = 0x00A1
	popWMNCRButtonDown    = 0x00A4
	popWMNCMButtonDown    = 0x00A7
	popWMNCXButtonDown    = 0x00AB
	popWMMouseLeave       = 0x02A3
	popWMCaptureChanged   = 0x0215
	popWMKeyDown          = 0x0100
	popWMSysKeyDown       = 0x0104

	popSWPNoMove = 0x0002
	popSWPNoSize = 0x0001

	popDIBRGBColors = 0
	popBIRGB        = 0

	// tooltips
	popTTSAlwaysTip      = 0x01
	popTTSNoPrefix       = 0x02
	popTTFTrack          = 0x0020
	popTTFAbsolute       = 0x0080
	popTTFIDIsHWND       = 0x0001
	popTTMAddToolW       = 0x0400 + 50
	popTTMTrackActivate  = 0x0400 + 17
	popTTMTrackPosition  = 0x0400 + 18
	popTTMSetMaxTipWidth = 0x0400 + 24
	popTTMUpdateTipTextW = 0x0400 + 57
	popTTMDelToolW       = 0x0400 + 51

	// edit controls
	popEMSetPasswordChar = 0x00CC
	popEMLimitText       = 0x00C5
	popESMultiline       = 0x0004
	popESAutoVScroll     = 0x0040
	popESWantReturn      = 0x1000
	popWSVScroll         = 0x00200000
	popWSChild           = 0x40000000
	popWSVisible         = 0x10000000
	popWSClipChildren    = 0x02000000

	// IME
	popGCSCompStr   = 0x0008
	popGCSResultStr = 0x0800
)

// popBlend is BLENDFUNCTION.
type popBlend struct {
	Op, Flags, SourceConstantAlpha, AlphaFormat byte
}

// popSize is SIZE.
type popSize struct{ CX, CY int32 }

// popBitmapInfo is BITMAPINFO with its BITMAPINFOHEADER (no colour table: 32 bpp BI_RGB).
type popBitmapInfo struct {
	Size          uint32
	Width, Height int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
	Colors        [1]uint32
}

// popMouseHook is MOUSEHOOKSTRUCT.
type popMouseHook struct {
	Pt        w32.Point
	Hwnd      w32.HWND
	HitTest   uint32
	ExtraInfo uintptr
}

// popCWP is CWPSTRUCT.
type popCWP struct {
	LParam, WParam uintptr
	Message        uint32
	Hwnd           w32.HWND
}

// popWindowPos is WINDOWPOS.
type popWindowPos struct {
	Hwnd, After  w32.HWND
	X, Y, CX, CY int32
	Flags        uint32
}

// popToolInfo is TOOLINFOW (comctl32 v6).
type popToolInfo struct {
	Size     uint32
	Flags    uint32
	Hwnd     w32.HWND
	ID       uintptr
	Rect     w32.Rect
	Inst     windows.Handle
	Text     *uint16
	LParam   uintptr
	Reserved uintptr
}

func setWindowsHookEx(id int32, proc uintptr, thread uint32) uintptr {
	r, _, _ := syscall.SyscallN(popProcSetWindowsHookExW.Addr(), uintptr(id), proc, 0, uintptr(thread))
	return r
}

func unhookWindowsHookEx(h uintptr) {
	syscall.SyscallN(popProcUnhookWindowsHookEx.Addr(), h)
}

func callNextHookEx(code int32, wp, lp uintptr) uintptr {
	r, _, _ := syscall.SyscallN(popProcCallNextHookEx.Addr(), 0, uintptr(code), wp, lp)
	return r
}

func updateLayeredWindow(h w32.HWND, dst *w32.Point, size *popSize, src windows.Handle, srcPt *w32.Point, blend *popBlend) bool {
	r, _, _ := syscall.SyscallN(popProcUpdateLayeredWindow.Addr(), uintptr(h), 0, uintptr(unsafe.Pointer(dst)), uintptr(unsafe.Pointer(size)),
		uintptr(src), uintptr(unsafe.Pointer(srcPt)), 0, uintptr(unsafe.Pointer(blend)), popULWAlpha)
	return r != 0
}

func setTimer(h w32.HWND, id uintptr, ms uint32) {
	syscall.SyscallN(popProcSetTimer.Addr(), uintptr(h), id, uintptr(ms), 0)
}

func killTimer(h w32.HWND, id uintptr) {
	syscall.SyscallN(popProcKillTimer.Addr(), uintptr(h), id)
}

func getForegroundWindow() w32.HWND {
	r, _, _ := syscall.SyscallN(popProcGetForegroundWindow.Addr())
	return w32.HWND(r)
}

func windowThread(h w32.HWND) uint32 {
	r, _, _ := syscall.SyscallN(popProcGetWindowThreadPID.Addr(), uintptr(h), 0)
	return uint32(r)
}

func getSystemMetrics(i int32) int32 {
	r, _, _ := syscall.SyscallN(popProcGetCursorInfo.Addr(), uintptr(i))
	return int32(r)
}

func createCompatibleDC() windows.Handle {
	r, _, _ := syscall.SyscallN(popProcPCreateCompatibleDC.Addr(), 0)
	return windows.Handle(r)
}

func deleteDC(dc windows.Handle) {
	syscall.SyscallN(popProcPDeleteDC.Addr(), uintptr(dc))
}

func selectObject(dc, obj windows.Handle) windows.Handle {
	r, _, _ := syscall.SyscallN(popProcPSelectObject.Addr(), uintptr(dc), uintptr(obj))
	return windows.Handle(r)
}

func createDIBSection(dc windows.Handle, bi *popBitmapInfo, bits *unsafe.Pointer) windows.Handle {
	r, _, _ := syscall.SyscallN(popProcCreateDIBSection.Addr(), uintptr(dc), uintptr(unsafe.Pointer(bi)), popDIBRGBColors,
		uintptr(unsafe.Pointer(bits)), 0, 0)
	return windows.Handle(r)
}

// immAssociateContext gives h the input context imc (0: none, so input methods are off in it)
// and returns the one it had.
func immAssociateContext(h w32.HWND, imc uintptr) uintptr {
	r, _, _ := syscall.SyscallN(popProcImmAssociateContext.Addr(), uintptr(h), imc)
	return r
}

// immCompositionString is the input method's composition (GCS_COMPSTR) or result
// (GCS_RESULTSTR) string of imc.
func immCompositionString(imc uintptr, which uint32) string {
	n, _, _ := syscall.SyscallN(popProcImmGetCompStringW.Addr(), imc, uintptr(which), 0, 0)
	if int32(n) <= 0 {
		return ""
	}
	buf := make([]uint16, int32(n)/2)
	syscall.SyscallN(popProcImmGetCompStringW.Addr(), imc, uintptr(which), uintptr(unsafe.Pointer(&buf[0])), n)
	return windows.UTF16ToString(buf)
}

func immSetOpenStatus(imc uintptr, open bool) bool {
	v := uintptr(0)
	if open {
		v = 1
	}
	r, _, _ := syscall.SyscallN(popProcImmSetOpenStatus.Addr(), imc, v)
	return r != 0
}

func immGetOpenStatus(imc uintptr) bool {
	r, _, _ := syscall.SyscallN(popProcImmGetOpenStatus.Addr(), imc)
	return r != 0
}

func immCreateContext() uintptr {
	r, _, _ := syscall.SyscallN(popProcImmCreateContext.Addr())
	return r
}

func immDestroyContext(imc uintptr) {
	syscall.SyscallN(popProcImmDestroyContext.Addr(), imc)
}
