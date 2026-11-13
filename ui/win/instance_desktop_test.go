//go:build windows

package win

import (
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Tests that show windows that activate (system dialogs, a window brought to the front) run
// on a desktop of their own (Desktop): nothing reaches the user's screen or keyboard, and
// the tests drive the dialogs with messages from a thread on that desktop.

var desktopSeq atomic.Int32

// onTestDesktop puts the test's thread, locked for the rest of the test, on a new desktop
// and returns its name. It must run before the thread makes any window (before openTest).
func onTestDesktop(t *testing.T) string {
	t.Helper()
	runtime.LockOSThread()
	name := fmt.Sprintf("DownexWinTest-%d-%d", os.Getpid(), desktopSeq.Add(1))
	d, err := NewDesktop(name)
	if err != nil {
		runtime.UnlockOSThread()
		t.Fatal(err)
	}
	restore, err := UseDesktop(name)
	if err != nil {
		d.Close()
		runtime.UnlockOSThread()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// The thread is never unlocked: Go ends it with the test's goroutine. A thread that
		// had windows on the test desktop keeps state of it even when it moves back (measured:
		// after TestPickFolder's or TestPickFile's file dialog, owned by a window of this
		// thread, a window that a later test made on the reused thread answered UI
		// Automation's WM_GETOBJECT, yet the client saw only its default HWND frame, and
		// TestPopupUIAutomation failed). A thread that cannot move back at all (COM's and the
		// shell's hidden windows) would also put a later test's windows on the hidden desktop.
		if err := restore(); err != nil {
			t.Logf("the thread stays on the test desktop until it ends: %v", err)
		}
		d.Close()
	})
	return name
}

// onDesktop runs f on a thread of its own on the desktop called name (the driver of a
// dialog that the test's thread shows). The thread ends with f.
func onDesktop(t *testing.T, name string, f func()) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.LockOSThread() // never unlocked: the thread ends with the goroutine
		restore, err := UseDesktop(name)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = restore() }() // the thread ends with the goroutine either way
		f()
	}()
	return done
}

var (
	testUser32           = windows.NewLazySystemDLL("user32.dll")
	procTestGetDlgItem   = testUser32.NewProc("GetDlgItem")
	procTestGetWindow    = testUser32.NewProc("GetWindow")
	procTestGetWindowLng = testUser32.NewProc("GetWindowLongW")
	procTestFindWindowEx = testUser32.NewProc("FindWindowExW")
	procTestSendMessage  = testUser32.NewProc("SendMessageW")
	procTestPostMessage  = testUser32.NewProc("PostMessageW")
)

func dlgItem(dlg uintptr, id int) uintptr {
	r, _, _ := syscall.SyscallN(procTestGetDlgItem.Addr(), dlg, uintptr(id))
	return r
}

func ownerOf(h uintptr) uintptr {
	r, _, _ := syscall.SyscallN(procTestGetWindow.Addr(), h, 4 /* GW_OWNER */)
	return r
}

func exStyleOf(h uintptr) uint32 {
	r, _, _ := syscall.SyscallN(procTestGetWindowLng.Addr(), h, uintptr(^uintptr(19)) /* GWL_EXSTYLE -20 */)
	return uint32(r)
}

func findChild(parent, after uintptr, class string) uintptr {
	c, _ := windows.UTF16PtrFromString(class)
	r, _, _ := syscall.SyscallN(procTestFindWindowEx.Addr(), parent, after, uintptr(unsafe.Pointer(c)), 0)
	return r
}

func sendMsg(h uintptr, msg uint32, wp, lp uintptr) uintptr {
	r, _, _ := syscall.SyscallN(procTestSendMessage.Addr(), h, uintptr(msg), wp, lp)
	return r
}

func postMsg(h uintptr, msg uint32, wp, lp uintptr) {
	syscall.SyscallN(procTestPostMessage.Addr(), h, uintptr(msg), wp, lp)
}

// windowText is a window's text (WM_GETTEXT, which reaches another thread's window).
func windowText(h uintptr) string {
	buf := make([]uint16, 4096)
	n := sendMsg(h, 0x000D /* WM_GETTEXT */, uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0])))
	return windows.UTF16ToString(buf[:n])
}

// waitWindow waits up to timeout for a top-level window of class titled title on the
// calling thread's desktop.
func waitWindow(class, title string, timeout time.Duration) uintptr {
	deadline := time.Now().Add(timeout)
	c, _ := windows.UTF16PtrFromString(class)
	tt, _ := windows.UTF16PtrFromString(title)
	for {
		r, _, _ := syscall.SyscallN(procTestFindWindowEx.Addr(), 0, 0, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(tt)))
		if r != 0 || !time.Now().Before(deadline) {
			return r
		}
		time.Sleep(20 * time.Millisecond)
	}
}
