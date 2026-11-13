//go:build windows

package glue

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"downex/ui/host"
)

// Stand-ins for today's window (Wails v2.17 on Windows), written from what its single-instance
// protocol does, not from its code: a start creates the named mutex "wails-app-<id>sim";
// when it already exists the start looks up the window of class "wails-app-<id>-sic" titled
// "wails-app-<id>-siw" with FindWindowW, sends it WM_COPYDATA (dwData 1542) holding its
// {"Args":[…],"WorkingDirectory":"…"} as NUL-terminated UTF-16 (cbData twice the units plus
// one), and exits; otherwise it makes that window, a message-only one, and reads every such
// payload up to its NUL. They let the tests check that the native window and today's hand
// over to each other in both directions.

var (
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procPeekMessageW     = user32.NewProc("PeekMessageW")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
)

const (
	wmCopyData    = 0x004A
	hwndMessage   = ^uintptr(2) // HWND_MESSAGE: (HWND)-3
	wailsDataKind = 1542
)

type copyData struct {
	kind uintptr
	size uint32
	ptr  uintptr
}

// secondInstanceData is the JSON a Wails v2.17 start hands over.
type secondInstanceData struct {
	Args             []string
	WorkingDirectory string
}

// wailsSend is a later start of today's window: it hands args over and exits 0, or reports
// why it would have opened a window of its own (4: no lock held, 5: no window found).
func wailsSend(lock string, args []string) int {
	n := HandoffNames(lock, host.ViewOf(args)) // the view's lock, as today's main chooses it
	m, err := windows.CreateMutex(nil, false, windows.StringToUTF16Ptr(n.Mutex))
	if m != 0 {
		defer windows.CloseHandle(m)
	}
	if !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return 4
	}
	h := call(procFindWindowW, utf16(n.Class), utf16(n.Window))
	if h == 0 {
		return 5
	}
	wd, _ := os.Getwd()
	if args == nil {
		args = []string{}
	}
	j, err := json.Marshal(secondInstanceData{Args: args, WorkingDirectory: wd})
	if err != nil {
		return 6
	}
	units, err := syscall.UTF16FromString(string(j)) // with its NUL
	if err != nil {
		return 6
	}
	cds := &copyData{kind: wailsDataKind, size: uint32(len(units)*2 + 1), ptr: uintptr(unsafe.Pointer(&units[0]))}
	call(procSendMessage, h, wmCopyData, 0, uintptr(unsafe.Pointer(cds)))
	return 0
}

// receiver is the stand-in window's state (one per process).
var receiver struct {
	got  *secondInstanceData
	proc uintptr
}

func receiverProc(h, msg, wp, lp uintptr) uintptr {
	if msg == wmCopyData {
		cds := (*copyData)(addr(lp))
		if cds.kind == wailsDataKind {
			s := windows.UTF16PtrToString((*uint16)(addr(cds.ptr)))
			var d secondInstanceData
			if json.Unmarshal([]byte(s), &d) == nil {
				receiver.got = &d
			}
		}
		return 0
	}
	r, _, _ := syscall.SyscallN(procDefWindowProcW.Addr(), h, msg, wp, lp)
	return r
}

// pump dispatches the thread's messages for d.
func pump(d time.Duration) { pumpWhile(d, func() bool { return true }) }

// pumpWhile dispatches the thread's messages while more is true, for at most d.
func pumpWhile(d time.Duration, more func() bool) {
	deadline := time.Now().Add(d)
	var m1 msg
	for more() && time.Now().Before(deadline) {
		for call(procPeekMessageW, uintptr(unsafe.Pointer(&m1)), 0, 0, 0, 1 /* PM_REMOVE */) != 0 {
			call(procTranslateMessage, uintptr(unsafe.Pointer(&m1)))
			call(procDispatchMessageW, uintptr(unsafe.Pointer(&m1)))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// addr is the memory at a, an address the system handed over (go vet's rule for uintptr).
func addr(a uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&a)) }

type wndClassEx struct {
	size, style                uint32
	proc                       uintptr
	clsExtra, wndExtra         int32
	instance, icon, cursor, bg uintptr
	menu, class                *uint16
	iconSm                     uintptr
}

type msg struct {
	hwnd           uintptr
	message        uint32
	wparam, lparam uintptr
	time           uint32
	pt             [2]int32
	private        uint32
}

// wailsReceive is today's running window of the view args ask for (its lock): it holds the
// lock, prints "ready", and prints the first hand-off it receives as JSON (exit 0). Without
// one it prints "none" after wait (exit 0), or exits 7 after 20 s when wait is 0.
func wailsReceive(lock string, args []string, wait time.Duration, out io.Writer) int {
	n := HandoffNames(lock, host.ViewOf(args))
	m, err := windows.CreateMutex(nil, false, windows.StringToUTF16Ptr(n.Mutex))
	if err != nil {
		return 4
	}
	defer windows.CloseHandle(m)
	receiver.proc = syscall.NewCallback(receiverProc)
	var inst windows.Handle
	_ = windows.GetModuleHandleEx(0, nil, &inst)
	wc := &wndClassEx{proc: receiver.proc, instance: uintptr(inst), class: windows.StringToUTF16Ptr(n.Class)}
	wc.size = uint32(unsafe.Sizeof(*wc))
	if call(procRegisterClassExW, uintptr(unsafe.Pointer(wc))) == 0 {
		return 5
	}
	h := call(procCreateWindowExW, 0, utf16(n.Class), utf16(n.Window), 0, 0, 0, 0, 0, hwndMessage, 0, uintptr(inst), 0)
	if h == 0 {
		return 6
	}
	defer call(procDestroyWindow, h)
	fmt.Fprintln(out, "ready")
	limit := wait
	if limit <= 0 {
		limit = 20 * time.Second
	}
	pumpWhile(limit, func() bool { return receiver.got == nil })
	if receiver.got == nil {
		if wait > 0 {
			fmt.Fprintln(out, "none")
			return 0
		}
		return 7
	}
	b, _ := json.Marshal(receiver.got)
	fmt.Fprintln(out, string(b))
	return 0
}
