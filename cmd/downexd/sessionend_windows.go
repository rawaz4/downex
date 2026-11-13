//go:build windows

package main

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// At logoff, restart and shutdown Windows asks every top-level window whether the
// session may end (WM_QUERYENDSESSION) and then announces the end (WM_ENDSESSION); the
// process may be terminated as soon as WM_ENDSESSION returns. A process that has loaded
// user32.dll (as downexd has, through the shell's known-folder lookup) no longer gets the
// console's logoff and shutdown events, and a detached daemon has no console at all, so
// a hidden top-level window is the only notice it gets.
const (
	wmDestroy         = 0x0002
	wmClose           = 0x0010
	wmQueryEndSession = 0x0011
	wmEndSession      = 0x0016
	wmTimeChange      = 0x001E
	wmPowerBroadcast  = 0x0218
	// WM_POWERBROADCAST events after the system resumed from sleep or hibernation.
	pbtResumeSuspend   = 0x0007 // PBT_APMRESUMESUSPEND (the user is present)
	pbtResumeAutomatic = 0x0012 // PBT_APMRESUMEAUTOMATIC (any resume, also by a wake timer)
	// sessionWindowClass prefixes the window class name, which lets tests find the
	// window of a daemon process.
	sessionWindowClass = "DownexdSessionWatcher"
	// endSessionWait bounds how long WM_ENDSESSION waits for the daemon to stop; the
	// daemon's own budget for an ending session is shorter.
	endSessionWait = 30 * time.Second
)

var (
	user32                         = windows.NewLazySystemDLL("user32.dll")
	procRegisterClassExW           = user32.NewProc("RegisterClassExW")
	procUnregisterClassW           = user32.NewProc("UnregisterClassW")
	procCreateWindowExW            = user32.NewProc("CreateWindowExW")
	procDestroyWindow              = user32.NewProc("DestroyWindow")
	procDefWindowProcW             = user32.NewProc("DefWindowProcW")
	procGetMessageW                = user32.NewProc("GetMessageW")
	procTranslateMessage           = user32.NewProc("TranslateMessage")
	procDispatchMessageW           = user32.NewProc("DispatchMessageW")
	procPostMessageW               = user32.NewProc("PostMessageW")
	procPostQuitMessage            = user32.NewProc("PostQuitMessage")
	procShutdownBlockReasonCreate  = user32.NewProc("ShutdownBlockReasonCreate")
	procShutdownBlockReasonDestroy = user32.NewProc("ShutdownBlockReasonDestroy")
)

// wndClassEx mirrors WNDCLASSEXW.
type wndClassEx struct {
	size       uint32
	style      uint32
	wndProc    uintptr
	clsExtra   int32
	wndExtra   int32
	instance   windows.Handle
	icon       windows.Handle
	cursor     windows.Handle
	background windows.Handle
	menuName   *uint16
	className  *uint16
	iconSm     windows.Handle
}

// msg mirrors MSG.
type msg struct {
	hwnd    windows.HWND
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      struct{ x, y int32 }
	private uint32
}

// sessionWatcher owns the hidden window that receives the end-of-session messages. The
// same window hears when the system resumed from sleep and when the clock or time zone
// changed (broadcast to top-level windows); setOnResume reports those (the queue
// scheduler re-evaluates its schedules, docs/M6-SPEC.md §1).
type sessionWatcher struct {
	onResume atomic.Pointer[func()]
	onEnd    func() // called once when the session starts ending
	endOnce  sync.Once
	stopped  chan struct{} // closed by finished: the daemon has stopped
	stopOnce sync.Once
	hwnd     windows.HWND
	loopDone chan struct{} // closed when the window's thread has ended
}

// watchSessionEnd creates a hidden top-level window on a thread of its own. When the
// user session ends it calls onEnd (at WM_QUERYENDSESSION, so that pausing starts as
// early as possible, and allows the end), registers a shutdown block reason, and keeps
// WM_ENDSESSION from returning until finished is called (or endSessionWait passed), so
// that Windows does not terminate the daemon before its downloads are paused and
// committed. close destroys the window.
func watchSessionEnd(onEnd func()) (*sessionWatcher, error) {
	w := &sessionWatcher{onEnd: onEnd, stopped: make(chan struct{}), loopDone: make(chan struct{})}
	ready := make(chan error, 1)
	go w.loop(ready)
	if err := <-ready; err != nil {
		return nil, err
	}
	return w, nil
}

// loop creates the window and pumps its messages until it is destroyed.
func (w *sessionWatcher) loop(ready chan<- error) {
	defer close(w.loopDone)
	// The window belongs to this thread: it must pump the messages. The thread is
	// never unlocked, so it ends with the goroutine.
	runtime.LockOSThread()
	var instance windows.Handle
	if err := windows.GetModuleHandleEx(0, nil, &instance); err != nil {
		ready <- fmt.Errorf("session watcher: module handle: %w", err)
		return
	}
	// One class per watcher, so that each window gets its own procedure (a test process
	// may create several).
	class, err := windows.UTF16PtrFromString(fmt.Sprintf("%s-%p", sessionWindowClass, w))
	if err != nil {
		ready <- err
		return
	}
	wc := wndClassEx{
		wndProc:   windows.NewCallback(w.wndProc),
		instance:  instance,
		className: class,
	}
	wc.size = uint32(unsafe.Sizeof(wc))
	if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		ready <- fmt.Errorf("session watcher: register window class: %w", err)
		return
	}
	defer procUnregisterClassW.Call(uintptr(unsafe.Pointer(class)), uintptr(instance)) //nolint:errcheck // best effort
	hwnd, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(class)),
		0, 0, 0, 0, 0, 0, 0, uintptr(instance), 0)
	if hwnd == 0 {
		ready <- fmt.Errorf("session watcher: create window: %w", err)
		return
	}
	w.hwnd = windows.HWND(hwnd)
	ready <- nil
	var m msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 { // WM_QUIT or an error
			return
		}
		_, _, _ = procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// wndProc handles the window's messages on its thread.
func (w *sessionWatcher) wndProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case wmQueryEndSession:
		w.ending(hwnd)
		return 1 // the session may end
	case wmEndSession:
		if wParam != 0 { // the session ends now
			w.ending(hwnd)
			select {
			case <-w.stopped:
			case <-time.After(endSessionWait):
			}
		}
		return 0
	case wmPowerBroadcast:
		if wParam == pbtResumeAutomatic || wParam == pbtResumeSuspend {
			w.resumed()
		}
		return 1
	case wmTimeChange:
		w.resumed()
		return 0
	case wmClose:
		_, _, _ = procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		_, _, _ = procShutdownBlockReasonDestroy.Call(hwnd)
		_, _, _ = procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
	return r
}

// setOnResume sets the function called (on the window's thread; it must not block)
// after the system resumed from sleep or the clock changed. It is safe on nil.
func (w *sessionWatcher) setOnResume(f func()) {
	if w != nil {
		w.onResume.Store(&f)
	}
}

func (w *sessionWatcher) resumed() {
	if f := w.onResume.Load(); f != nil {
		(*f)()
	}
}

// ending reports the end of the session once and tells Windows why the daemon delays it.
func (w *sessionWatcher) ending(hwnd uintptr) {
	w.endOnce.Do(func() {
		if reason, err := windows.UTF16PtrFromString("Downex is saving download progress"); err == nil {
			_, _, _ = procShutdownBlockReasonCreate.Call(hwnd, uintptr(unsafe.Pointer(reason)))
		}
		w.onEnd()
	})
}

// finished reports that the daemon has stopped, releasing a pending WM_ENDSESSION. It
// is safe on nil.
func (w *sessionWatcher) finished() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() { close(w.stopped) })
}

// close destroys the window and ends its thread. It is safe on nil.
func (w *sessionWatcher) close() {
	if w == nil {
		return
	}
	w.finished()
	if r, _, _ := procPostMessageW.Call(uintptr(w.hwnd), wmClose, 0, 0); r != 0 {
		<-w.loopDone
	}
}
