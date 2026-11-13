//go:build windows

package win

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"downex/ui/win/internal/w32"
)

// The Win32, OLE and shell calls of the process glue (instance, hand-off, message box,
// pickers, drop target, desktop isolation), bound as package w32 binds its own: typed
// wrappers of a fixed number of arguments around syscall.SyscallN. Pointers they take to
// memory a window procedure may run during the call point to heap memory (package w32's
// rule), never to a local.
var (
	glueUser32  = windows.NewLazySystemDLL("user32.dll")
	glueOle32   = windows.NewLazySystemDLL("ole32.dll")
	glueShell32 = windows.NewLazySystemDLL("shell32.dll")

	procFindWindowExW          = glueUser32.NewProc("FindWindowExW")
	procFindWindowW            = glueUser32.NewProc("FindWindowW")
	procIsWindowGlue           = glueUser32.NewProc("IsWindow")
	procSendMessageTimeoutW    = glueUser32.NewProc("SendMessageTimeoutW")
	procSetForegroundWindow    = glueUser32.NewProc("SetForegroundWindow")
	procAllowSetForegroundWnd  = glueUser32.NewProc("AllowSetForegroundWindow")
	procChildWindowFromPointEx = glueUser32.NewProc("ChildWindowFromPointEx")
	procGetWindowLongPtrW      = glueUser32.NewProc("GetWindowLongPtrW")
	procGetWindowLongW         = glueUser32.NewProc("GetWindowLongW")
	procCreateDesktopW         = glueUser32.NewProc("CreateDesktopW")
	procOpenDesktopW           = glueUser32.NewProc("OpenDesktopW")
	procCloseDesktop           = glueUser32.NewProc("CloseDesktop")
	procSetThreadDesktop       = glueUser32.NewProc("SetThreadDesktop")
	procGetThreadDesktop       = glueUser32.NewProc("GetThreadDesktop")

	procOleInitialize             = glueOle32.NewProc("OleInitialize")
	procOleUninitialize           = glueOle32.NewProc("OleUninitialize")
	procRegisterDragDrop          = glueOle32.NewProc("RegisterDragDrop")
	procRevokeDragDrop            = glueOle32.NewProc("RevokeDragDrop")
	procReleaseStgMedium          = glueOle32.NewProc("ReleaseStgMedium")
	procSHCreateItemFromParsingNm = glueShell32.NewProc("SHCreateItemFromParsingName")
	procDragQueryFileW            = glueShell32.NewProc("DragQueryFileW")
	procRegisterClipboardFormatW  = glueUser32.NewProc("RegisterClipboardFormatW")
)

// Messages and flags of the glue (WinUser.h, OleIdl.h).
const (
	wmCopyData = 0x004A

	smtoBlock          = 0x0001
	smtoAbortIfHung    = 0x0002
	smtoErrorOnExit    = 0x0020
	asfwAny            = ^uintptr(0) // ASFW_ANY: (DWORD)-1
	cwpSkipInvisible   = 0x0001
	cwpSkipDisabled    = 0x0002
	cwpSkipTransparent = 0x0004
	gwlStyle           = -16
	esMultiline        = 0x0004
	esReadOnly         = 0x0800

	emSetSel      = 0x00B1
	emReplaceSel  = 0x00C2
	emCharFromPos = 0x00D7

	mbYesNo       = 0x00000004
	mbDefButton2  = 0x00000100
	mbSystemModal = 0x00001000
	idYes         = 6
)

func utf16Ptr(s string) *uint16 {
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return nil
	}
	return p
}

// findWindow finds the window of class and title among the message-only windows, then
// among the top-level ones (FindWindow, which Wails v2.17's sender uses, reaches both).
func findWindow(class, title string) w32.HWND {
	c, t := utf16Ptr(class), utf16Ptr(title)
	if c == nil || t == nil {
		return 0
	}
	h, _, _ := syscall.SyscallN(procFindWindowExW.Addr(), uintptr(w32.HWND_MESSAGE), 0, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(t)))
	if h == 0 {
		h, _, _ = syscall.SyscallN(procFindWindowW.Addr(), uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(t)))
	}
	return w32.HWND(h)
}

// windowExists reports that h is a window (not destroyed).
func windowExists(h w32.HWND) bool {
	r, _, _ := syscall.SyscallN(procIsWindowGlue.Addr(), uintptr(h))
	return r != 0
}

// sendMessageTimeout sends msg and waits at most ms milliseconds for the answer; it reports
// false when the window did not answer (hung, gone, or too slow). The answer is written to
// result when it is not 0 (lpdwResult).
func sendMessageTimeout(h w32.HWND, msg uint32, wp, lp uintptr, flags, ms uint32, result uintptr) (bool, error) {
	r, _, e := syscall.SyscallN(procSendMessageTimeoutW.Addr(), uintptr(h), uintptr(msg), wp, lp, uintptr(flags), uintptr(ms), result)
	if r == 0 {
		if e == 0 {
			return false, windows.ERROR_TIMEOUT
		}
		return false, e
	}
	return true, nil
}

func setForegroundWindow(h w32.HWND) bool {
	r, _, _ := syscall.SyscallN(procSetForegroundWindow.Addr(), uintptr(h))
	return r != 0
}

// AllowForegroundToAny lets any process bring its window to the front (AllowSetForegroundWindow
// with ASFW_ANY): a second start does it before it hands its arguments over, so that the
// running window may come to the front.
func AllowForegroundToAny() bool {
	r, _, _ := syscall.SyscallN(procAllowSetForegroundWnd.Addr(), asfwAny)
	return r != 0
}

func windowStyleOf(h w32.HWND) uintptr {
	p := procGetWindowLongPtrW
	if unsafe.Sizeof(uintptr(0)) == 4 {
		p = procGetWindowLongW // 32-bit user32 has no GetWindowLongPtrW export
	}
	gwl := int32(gwlStyle)
	r, _, _ := syscall.SyscallN(p.Addr(), uintptr(h), uintptr(gwl))
	return r
}

// childAt is the visible, enabled child window of parent under the client point p (physical
// pixels), or parent itself.
func childAt(parent w32.HWND, p w32.Point) w32.HWND {
	var r uintptr
	flags := uintptr(cwpSkipInvisible | cwpSkipDisabled | cwpSkipTransparent)
	if unsafe.Sizeof(uintptr(0)) == 8 {
		r, _, _ = syscall.SyscallN(procChildWindowFromPointEx.Addr(), uintptr(parent),
			uintptr(uint64(uint32(p.X))|uint64(uint32(p.Y))<<32), flags)
	} else {
		r, _, _ = syscall.SyscallN(procChildWindowFromPointEx.Addr(), uintptr(parent), uintptr(p.X), uintptr(p.Y), flags)
	}
	return w32.HWND(r)
}

func registerClipboardFormat(name string) uint16 {
	p := utf16Ptr(name)
	if p == nil {
		return 0
	}
	r, _, _ := syscall.SyscallN(procRegisterClipboardFormatW.Addr(), uintptr(unsafe.Pointer(p)))
	return uint16(r)
}
