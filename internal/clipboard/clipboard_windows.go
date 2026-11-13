//go:build windows

package clipboard

import (
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procRegisterClassExW              = user32.NewProc("RegisterClassExW")
	procCreateWindowExW               = user32.NewProc("CreateWindowExW")
	procDestroyWindow                 = user32.NewProc("DestroyWindow")
	procDefWindowProcW                = user32.NewProc("DefWindowProcW")
	procGetMessageW                   = user32.NewProc("GetMessageW")
	procDispatchMessageW              = user32.NewProc("DispatchMessageW")
	procPostMessageW                  = user32.NewProc("PostMessageW")
	procPostQuitMessage               = user32.NewProc("PostQuitMessage")
	procAddClipboardFormatListener    = user32.NewProc("AddClipboardFormatListener")
	procRemoveClipboardFormatListener = user32.NewProc("RemoveClipboardFormatListener")
	procOpenClipboard                 = user32.NewProc("OpenClipboard")
	procCloseClipboard                = user32.NewProc("CloseClipboard")
	procGetClipboardData              = user32.NewProc("GetClipboardData")
	procIsClipboardFormatAvailable    = user32.NewProc("IsClipboardFormatAvailable")
	procRegisterClipboardFormatW      = user32.NewProc("RegisterClipboardFormatW")
	procGetClipboardSequenceNumber    = user32.NewProc("GetClipboardSequenceNumber")
	procGlobalLock                    = kernel32.NewProc("GlobalLock")
	procGlobalUnlock                  = kernel32.NewProc("GlobalUnlock")
	procGlobalSize                    = kernel32.NewProc("GlobalSize")
)

// Win32 constants (winuser.h).
const (
	wmDestroy         = 0x0002
	wmClipboardUpdate = 0x031D
	wmApp             = 0x8000
	wmStop            = wmApp + 1 // Close: unregister and destroy the window

	cfUnicodeText = 13

	// hwndMessage is HWND_MESSAGE ((HWND)-3): the parent of message-only windows.
	hwndMessage = ^uintptr(2)

	windowClass = "DownexClipboardWindow"

	// openAttempts and openRetry: another program may still hold the clipboard open
	// right after it changed it.
	openAttempts = 5
	openRetry    = 20 * time.Millisecond
	// closeTimeout bounds how long Close waits for the window thread.
	closeTimeout = 5 * time.Second
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

// Window class, registered formats and procedure, shared by every monitor of the
// process.
var (
	classOnce sync.Once
	classErr  error
	className *uint16
	instance  windows.Handle

	// Registered clipboard formats (0 when registration failed).
	cfExclude, cfViewerIgnore, cfHistory uintptr

	watchersMu sync.Mutex
	watchers   = map[windows.HWND]*watcher{}
)

func registerClass() error {
	classOnce.Do(func() {
		if err := windows.GetModuleHandleEx(0, nil, &instance); err != nil {
			classErr = fmt.Errorf("clipboard: module handle: %w", err)
			return
		}
		name, err := windows.UTF16PtrFromString(windowClass)
		if err != nil {
			classErr = err
			return
		}
		wc := wndClassEx{wndProc: windows.NewCallback(wndProc), instance: instance, className: name}
		wc.size = uint32(unsafe.Sizeof(wc))
		if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
			classErr = fmt.Errorf("clipboard: register window class: %w", err)
			return
		}
		className = name
		cfExclude = registerFormat(FormatExcludeFromMonitoring)
		cfViewerIgnore = registerFormat(FormatViewerIgnore)
		cfHistory = registerFormat(FormatCanIncludeInHistory)
	})
	return classErr
}

func registerFormat(name string) uintptr {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0
	}
	r, _, _ := procRegisterClipboardFormatW.Call(uintptr(unsafe.Pointer(p)))
	return r
}

// watcher is the window-thread side of a Monitor.
type watcher struct {
	hwnd     windows.HWND
	offer    func(string)
	log      *slog.Logger
	lastSeq  uintptr
	loopDone chan struct{}
}

// startPlatform creates the message-only window on a thread of its own and
// registers it as a clipboard format listener.
func startPlatform(offer func(string), log *slog.Logger) (func() error, error) {
	w := &watcher{offer: offer, log: log, loopDone: make(chan struct{})}
	ready := make(chan error, 1)
	go w.loop(ready)
	if err := <-ready; err != nil {
		<-w.loopDone
		return nil, err
	}
	var once sync.Once
	var stopErr error
	return func() error {
		once.Do(func() {
			if r, _, err := procPostMessageW.Call(uintptr(w.hwnd), wmStop, 0, 0); r == 0 {
				stopErr = fmt.Errorf("clipboard: stop the window thread: %w", err)
				return
			}
			select {
			case <-w.loopDone:
			case <-time.After(closeTimeout):
				stopErr = errors.New("clipboard: the window thread did not end in time")
			}
		})
		return stopErr
	}, nil
}

// wndProc dispatches the messages of every clipboard window to its watcher.
func wndProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	watchersMu.Lock()
	w := watchers[windows.HWND(hwnd)]
	watchersMu.Unlock()
	if w != nil {
		switch message {
		case wmClipboardUpdate:
			w.changed()
			return 0
		case wmStop:
			_, _, _ = procRemoveClipboardFormatListener.Call(hwnd)
			_, _, _ = procDestroyWindow.Call(hwnd)
			return 0
		case wmDestroy:
			_, _, _ = procPostQuitMessage.Call(0)
			return 0
		}
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
	return r
}

// loop creates the window and pumps its messages until it is destroyed.
func (w *watcher) loop(ready chan<- error) {
	defer close(w.loopDone)
	// The window belongs to this thread, which must pump its messages; the thread is
	// never unlocked, so it ends with the goroutine.
	runtime.LockOSThread()
	if err := registerClass(); err != nil {
		ready <- err
		return
	}
	hwnd, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(className)), 0, 0, 0, 0, 0, hwndMessage, 0, uintptr(instance), 0)
	if hwnd == 0 {
		ready <- fmt.Errorf("clipboard: create window: %w", err)
		return
	}
	w.hwnd = windows.HWND(hwnd)
	watchersMu.Lock()
	watchers[w.hwnd] = w
	watchersMu.Unlock()
	defer func() {
		watchersMu.Lock()
		delete(watchers, w.hwnd)
		watchersMu.Unlock()
	}()
	w.lastSeq, _, _ = procGetClipboardSequenceNumber.Call()
	if r, _, err := procAddClipboardFormatListener.Call(hwnd); r == 0 {
		_, _, _ = procDestroyWindow.Call(hwnd)
		ready <- fmt.Errorf("clipboard: add clipboard format listener: %w", err)
		w.pump() // ends with the WM_QUIT of WM_DESTROY
		return
	}
	ready <- nil
	w.pump()
}

func (w *watcher) pump() {
	var m msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 { // WM_QUIT or an error
			return
		}
		_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// changed reads the clipboard after WM_CLIPBOARDUPDATE and offers its text unless the
// content is excluded from monitoring.
func (w *watcher) changed() {
	// Each change is reported once; the sequence number also skips updates without a
	// new content (a program re-announcing the same data).
	if seq, _, _ := procGetClipboardSequenceNumber.Call(); seq != 0 {
		if seq == w.lastSeq {
			return
		}
		w.lastSeq = seq
	}
	text, ok, err := w.read()
	if err != nil {
		w.log.Debug("clipboard: cannot read the clipboard", "err", err)
		return
	}
	if ok {
		w.offer(text)
	}
}

// read returns the clipboard's text; ok is false when there is none, it is too long
// or the content is excluded from monitoring.
func (w *watcher) read() (text string, ok bool, err error) {
	opened := false
	for i := 0; i < openAttempts; i++ {
		r, _, e := procOpenClipboard.Call(uintptr(w.hwnd))
		if r != 0 {
			opened = true
			break
		}
		err = e
		time.Sleep(openRetry)
	}
	if !opened {
		return "", false, fmt.Errorf("open clipboard: %w", err)
	}
	defer procCloseClipboard.Call()

	available := func(format uintptr) bool {
		if format == 0 {
			return false
		}
		r, _, _ := procIsClipboardFormatAvailable.Call(format)
		return r != 0
	}
	historyPresent := available(cfHistory)
	var history *uint32
	if historyPresent {
		history = readDWORD(cfHistory)
	}
	if windowsExcluded(available(cfExclude), available(cfViewerIgnore), historyPresent, history) {
		return "", false, nil
	}
	if !available(cfUnicodeText) {
		return "", false, nil
	}
	h, _, e := procGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", false, fmt.Errorf("get clipboard text: %w", e)
	}
	return lockedText(h)
}

// readDWORD reads a clipboard format holding a DWORD (nil when unreadable). The
// clipboard is open.
func readDWORD(format uintptr) *uint32 {
	h, _, _ := procGetClipboardData.Call(format)
	if h == 0 {
		return nil
	}
	if size, _, _ := procGlobalSize.Call(h); size < 4 {
		return nil
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return nil
	}
	defer procGlobalUnlock.Call(h)
	v := *(*uint32)(pointer(p))
	return &v
}

// lockedText reads the NUL-terminated UTF-16 text of the global memory h; texts
// longer than MaxTextBytes code units are skipped (ok false) without being copied.
func lockedText(h uintptr) (string, bool, error) {
	size, _, _ := procGlobalSize.Call(h)
	units := int(size / 2)
	if units == 0 {
		return "", false, nil
	}
	p, _, e := procGlobalLock.Call(h)
	if p == 0 {
		return "", false, fmt.Errorf("lock clipboard text: %w", e)
	}
	defer procGlobalUnlock.Call(h)
	data := unsafe.Slice((*uint16)(pointer(p)), units)
	n := 0
	for n < len(data) && data[n] != 0 {
		n++
	}
	if n == 0 || n > MaxTextBytes {
		return "", false, nil
	}
	return windows.UTF16ToString(data[:n]), true, nil
}

// pointer converts an address returned by a Win32 call (memory outside the Go heap)
// to a pointer.
func pointer(addr uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&addr))
}
