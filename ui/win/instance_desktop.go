//go:build windows

package win

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Desktop is a desktop of this process's window station, apart from the one the user sees.
// Windows on it never reach the user's screen, focus or input, and a modal dialog shown on
// it cannot take the keyboard from the user: tests and measurements open their windows and
// system dialogs there and drive them with messages.
type Desktop struct {
	name string
	h    uintptr
}

const genericAll = 0x10000000

// NewDesktop makes (or opens) the desktop called name; Close lets it go once its last thread
// and window are gone.
func NewDesktop(name string) (*Desktop, error) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil || name == "" {
		return nil, errInvalid("desktop name %q", name)
	}
	h, _, e := syscall.SyscallN(procCreateDesktopW.Addr(), uintptr(unsafe.Pointer(p)), 0, 0, 0, genericAll, 0)
	if h == 0 {
		return nil, fmt.Errorf("win: CreateDesktop %q: %w", name, e)
	}
	return &Desktop{name: name, h: h}, nil
}

// Name is the desktop's name.
func (d *Desktop) Name() string { return d.name }

// Close closes this handle of the desktop.
func (d *Desktop) Close() {
	if d != nil && d.h != 0 {
		syscall.SyscallN(procCloseDesktop.Addr(), d.h)
		d.h = 0
	}
}

// UseDesktop moves the calling thread, which must be locked to its goroutine and have no
// window or hook yet, to the desktop called name (made by NewDesktop, in this process or
// another). FindWindow and SendMessage then reach the windows of that desktop only. restore
// moves the thread back and closes the handle; it is for the same thread, once its windows
// and hooks are gone. It fails while the thread still has one (COM's, OLE's and the shell's
// hidden windows stay for the thread's life): such a thread stays on the desktop, so its
// goroutine must keep it locked and let it end.
func UseDesktop(name string) (restore func() error, err error) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil || name == "" {
		return nil, errInvalid("desktop name %q", name)
	}
	old, _, _ := syscall.SyscallN(procGetThreadDesktop.Addr(), uintptr(windows.GetCurrentThreadId()))
	h, _, e := syscall.SyscallN(procOpenDesktopW.Addr(), uintptr(unsafe.Pointer(p)), 0, 0, genericAll)
	if h == 0 {
		return nil, fmt.Errorf("win: OpenDesktop %q: %w", name, e)
	}
	if r, _, e := syscall.SyscallN(procSetThreadDesktop.Addr(), h); r == 0 {
		syscall.SyscallN(procCloseDesktop.Addr(), h)
		return nil, fmt.Errorf("win: SetThreadDesktop %q (the thread has windows already?): %w", name, e)
	}
	return func() error {
		var err error
		if old != 0 {
			if r, _, e := syscall.SyscallN(procSetThreadDesktop.Addr(), old); r == 0 {
				err = fmt.Errorf("win: SetThreadDesktop back from %q (the thread has windows or hooks there): %w", name, e)
			}
		}
		syscall.SyscallN(procCloseDesktop.Addr(), h)
		return err
	}, nil
}
