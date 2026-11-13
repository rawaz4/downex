//go:build windows

package uia

import (
	"fmt"
	"syscall"
	"unsafe"

	"downex/ui/win/internal/com"
	"downex/ui/win/uia/internal/comobj"

	"golang.org/x/sys/windows"
)

// The functions of UIAutomationCore.dll this package calls (UIAutomationCoreApi.h). Each of
// them may call back into the providers on this thread before it returns, so the memory
// their pointer arguments point to is on the heap, never on a goroutine's stack (the same
// rule as package w32's for calls that run window procedures).
var (
	modUIA                                     = windows.NewLazySystemDLL("uiautomationcore.dll")
	procUiaReturnRawElementProvider            = modUIA.NewProc("UiaReturnRawElementProvider")
	procUiaHostProviderFromHwnd                = modUIA.NewProc("UiaHostProviderFromHwnd")
	procUiaRaiseAutomationEvent                = modUIA.NewProc("UiaRaiseAutomationEvent")
	procUiaRaiseAutomationPropertyChangedEvent = modUIA.NewProc("UiaRaiseAutomationPropertyChangedEvent")
	procUiaRaiseStructureChangedEvent          = modUIA.NewProc("UiaRaiseStructureChangedEvent")
	procUiaRaiseNotificationEvent              = modUIA.NewProc("UiaRaiseNotificationEvent")
	procUiaClientsAreListening                 = modUIA.NewProc("UiaClientsAreListening")
	procUiaDisconnectProvider                  = modUIA.NewProc("UiaDisconnectProvider")
)

// load finds every function, so that a system without one (UiaRaiseNotificationEvent needs
// Windows 10 1709) fails when the tree is made, not in the middle of a client's call.
func load() error {
	for _, p := range []*windows.LazyProc{procUiaReturnRawElementProvider, procUiaHostProviderFromHwnd,
		procUiaRaiseAutomationEvent, procUiaRaiseAutomationPropertyChangedEvent, procUiaRaiseStructureChangedEvent,
		procUiaRaiseNotificationEvent, procUiaClientsAreListening, procUiaDisconnectProvider} {
		if err := p.Find(); err != nil {
			return fmt.Errorf("uia: %w", err)
		}
	}
	return comobj.Load()
}

// returnRawElementProvider answers WM_GETOBJECT with the provider el (0: the window's
// providers are gone, which a closing window says with wParam and lParam 0).
func returnRawElementProvider(hwnd, wp, lp, el uintptr) uintptr {
	r, _, _ := syscall.SyscallN(procUiaReturnRawElementProvider.Addr(), hwnd, wp, lp, el)
	return r
}

// hostProviderFromHwnd is the system's provider for a window (its HWND properties), with a
// reference the caller owns; out is heap memory.
func hostProviderFromHwnd(hwnd uintptr, out *uintptr) (uintptr, com.HRESULT) {
	*out = 0
	r, _, _ := syscall.SyscallN(procUiaHostProviderFromHwnd.Addr(), hwnd, uintptr(unsafe.Pointer(out)))
	return *out, com.HRESULT(r)
}

func raiseAutomationEvent(p uintptr, id int32) com.HRESULT {
	r, _, _ := syscall.SyscallN(procUiaRaiseAutomationEvent.Addr(), p, uintptr(id))
	return com.HRESULT(r)
}

// raisePropertyChanged passes the two VARIANTs by value, which the x64 convention does with
// pointers to copies the callee may change: old and new are heap memory the caller clears.
func raisePropertyChanged(p uintptr, id int32, old, new *comobj.Variant) com.HRESULT {
	r, _, _ := syscall.SyscallN(procUiaRaiseAutomationPropertyChangedEvent.Addr(), p, uintptr(id),
		uintptr(unsafe.Pointer(old)), uintptr(unsafe.Pointer(new)))
	return com.HRESULT(r)
}

// raiseStructureChanged: rid is heap memory (or empty).
func raiseStructureChanged(p uintptr, kind int32, rid []int32) com.HRESULT {
	var at *int32
	if len(rid) > 0 {
		at = &rid[0]
	}
	r, _, _ := syscall.SyscallN(procUiaRaiseStructureChangedEvent.Addr(), p, uintptr(kind), uintptr(unsafe.Pointer(at)), uintptr(len(rid)))
	return com.HRESULT(r)
}

func raiseNotification(p uintptr, kind, processing int32, text, activity uintptr) com.HRESULT {
	r, _, _ := syscall.SyscallN(procUiaRaiseNotificationEvent.Addr(), p, uintptr(kind), uintptr(processing), text, activity)
	return com.HRESULT(r)
}

// clientsAreListening reports whether any UI Automation client listens for events (on this
// desktop): while none does, raising them is skipped.
func clientsAreListening() bool {
	r, _, _ := syscall.SyscallN(procUiaClientsAreListening.Addr())
	return r != 0
}

func disconnectProvider(p uintptr) com.HRESULT {
	r, _, _ := syscall.SyscallN(procUiaDisconnectProvider.Addr(), p)
	return com.HRESULT(r)
}
