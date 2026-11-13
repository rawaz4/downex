//go:build windows

package win

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"downex/ui/win/internal/com"
	"downex/ui/win/internal/w32"
)

// The single-instance lock of a window process, as today's windows (Wails v2.17 on Windows)
// hold it, so that an old and a new downex-ui.exe hand over to each other during an
// upgrade: a named mutex says that a window of the instance is open, and a message-only
// window of a known class and title receives a later start's arguments by WM_COPYDATA. The
// names and the payload are the host's (host.HandoffNamesOf, host.EncodeHandoff,
// host.DecodeHandoff); this file does the Win32 part.

// InstanceNames are the kernel and window names of one instance's lock.
type InstanceNames struct {
	Mutex  string // the named mutex a running window holds
	Class  string // the class of its message-only window
	Window string // that window's title
}

// Instance is this process's part of an instance's lock: the mutex it holds and, once
// Listen was called, the message-only window that later starts hand their arguments to.
type Instance struct {
	names InstanceNames
	mutex windows.Handle
	first bool // this process made the mutex
	l     *listener
}

// Running is the window process that holds an instance's lock: a later start forwards its
// arguments to it and exits.
type Running struct{ hwnd w32.HWND }

// OK reports that a running window was found.
func (r Running) OK() bool { return r.hwnd != 0 }

// Alive reports that the running window's message-only window still exists: a window that
// has gone since it was found (its process closing) answers nothing a start can go by.
func (r Running) Alive() bool { return r.hwnd != 0 && windowExists(r.hwnd) }

// Window is the running window's message-only window.
func (r Running) Window() uintptr { return uintptr(r.hwnd) }

// lookFor is how often a start looks for the running window's message-only window.
const lookFor = 25 * time.Millisecond

// ClaimInstance takes the lock named n. It returns:
//   - an Instance, when this process is the first: it then calls Listen on its UI thread;
//   - a Running, when another process holds the lock and its message-only window was found
//     within wait: this process hands its arguments over (Running.Forward) and exits 0;
//   - an Instance, when another process holds the mutex but no window answers within wait
//     (a window that is closing, or one that holds the mutex but never listened): this
//     process opens a window of its own, which Listen makes reachable for later starts.
//
// Wails v2.17 looks once; wait bridges the moment between a first start's mutex and its
// window. An error other than an existing mutex comes with a usable Instance that holds
// nothing (the window opens without the lock, as Wails' does).
func ClaimInstance(n InstanceNames, wait time.Duration) (*Instance, Running, error) {
	if n.Mutex == "" || n.Class == "" || n.Window == "" {
		return nil, Running{}, errInvalid("instance names %+v", n)
	}
	name, err := windows.UTF16PtrFromString(n.Mutex)
	if err != nil {
		return nil, Running{}, errInvalid("mutex name %q", n.Mutex)
	}
	h, err := windows.CreateMutex(nil, false, name)
	switch {
	case err == nil:
		return &Instance{names: n, mutex: h, first: true}, Running{}, nil
	case errors.Is(err, windows.ERROR_ALREADY_EXISTS):
	default:
		if h != 0 {
			windows.CloseHandle(h)
		}
		return &Instance{names: n}, Running{}, fmt.Errorf("win: single-instance lock %q: %w", n.Mutex, err)
	}
	deadline := time.Now().Add(wait)
	for {
		if w := findWindow(n.Class, n.Window); w != 0 {
			windows.CloseHandle(h)
			return nil, Running{hwnd: w}, nil
		}
		if !time.Now().Before(deadline) {
			return &Instance{names: n, mutex: h}, Running{}, nil
		}
		time.Sleep(lookFor)
	}
}

// First reports that this process made the lock's mutex.
func (in *Instance) First() bool { return in != nil && in.first }

// Names are the lock's names.
func (in *Instance) Names() InstanceNames { return in.names }

// ListenOptions are what the message-only window does with what it receives.
type ListenOptions struct {
	// Kind is the WM_COPYDATA dwData of a hand-off (host.HandoffDataKind); AlsoKinds are
	// further dwData values taken the same way (a host's own, host.HostHandoffKind). Other
	// data is ignored (answered 0).
	Kind      uintptr
	AlsoKinds []uintptr
	// Answer says what WM_COPYDATA of an accepted kind answers and whether the payload is
	// taken (handed to OnData); nil answers 1 and takes it. A payload not taken is dropped.
	Answer func(kind uintptr) (result uintptr, take bool)
	// BusyAnswer is the answer to a payload refused because MaxPendingHandoffs wait already
	// (0 when unset).
	BusyAnswer uintptr
	// ClosedAnswer is the answer to an accepted kind that reaches the window while Close
	// destroys it (a host that is ending: host.HostClosing, so that its start tries again
	// rather than taking it for an older build's window); nothing is taken then.
	ClosedAnswer uintptr
	// Max bounds a payload in bytes (host.MaxHandoff); a longer one is ignored.
	Max int
	// OnData gets each payload (cbData bytes) on the UI thread, after the sender's
	// SendMessage has returned, so that it may bring a window to the front or enter a
	// modal loop.
	OnData func(payload []byte)
	// OnClose answers a WM_CLOSE sent to the message-only window: every window of the
	// process answers one (an installer closes a program by its windows). The window
	// itself stays, so that the lock keeps working; nil ignores it.
	OnClose func()
}

// listener is an Instance's message-only window.
type listener struct {
	hwnd    w32.HWND
	class   *uint16
	opts    ListenOptions
	pending [][]byte
	thread  uint32
	closing bool // Close is destroying the window (the UI thread's)
}

// MaxPendingHandoffs is how many payloads the listener keeps while its thread has not handed
// them on yet; a payload beyond them is refused (WM_COPYDATA answers 0), so that any process
// of the desktop cannot make it hold more than this many times ListenOptions.Max.
const MaxPendingHandoffs = 16

// msgHandoff makes the listener hand what it received to OnData.
const msgHandoff = w32.WM_APP + 0x31

var (
	listenerOnce sync.Once
	listenerProc uintptr
	listeners    struct {
		sync.Mutex
		by map[w32.HWND]*listener
	}
)

// Listen makes the message-only window that later starts hand their arguments to. It is
// for the thread that runs the message loop (the UI thread), which receives them.
func (in *Instance) Listen(o ListenOptions) error {
	if in == nil {
		return errInvalid("Listen of a nil Instance")
	}
	if in.l != nil {
		return errInvalid("Listen called twice")
	}
	if o.Max <= 0 {
		return errInvalid("ListenOptions.Max %d", o.Max)
	}
	listenerOnce.Do(func() {
		listenerProc = w32.NewCallback(listenerWndProc)
		listeners.by = map[w32.HWND]*listener{}
	})
	class, err := windows.UTF16PtrFromString(in.names.Class)
	if err != nil {
		return errInvalid("class name %q", in.names.Class)
	}
	title, err := windows.UTF16PtrFromString(in.names.Window)
	if err != nil {
		return errInvalid("window name %q", in.names.Window)
	}
	inst := w32.GetModuleHandle()
	wc := &w32.WNDCLASSEXW{WndProc: listenerProc, Instance: inst, ClassName: class}
	wc.Size = uint32(unsafe.Sizeof(*wc))
	if _, err := w32.RegisterClassEx(wc); err != nil {
		return fmt.Errorf("win: hand-off window class: %w", err)
	}
	h, err := w32.CreateWindowEx(0, class, title, 0, 0, 0, 0, 0, w32.HWND_MESSAGE, inst, 0)
	if err != nil {
		w32.UnregisterClass(class, inst)
		return fmt.Errorf("win: hand-off window: %w", err)
	}
	l := &listener{hwnd: h, class: class, opts: o, thread: windows.GetCurrentThreadId()}
	listeners.Lock()
	listeners.by[h] = l
	listeners.Unlock()
	in.l = l
	return nil
}

// Listening reports that the message-only window exists.
func (in *Instance) Listening() bool { return in != nil && in.l != nil }

// Close destroys the message-only window (on the thread that made it) and lets the lock go.
func (in *Instance) Close() {
	if in == nil {
		return
	}
	if l := in.l; l != nil {
		in.l = nil
		// Still in the table while it is destroyed: a hand-off that reaches it meanwhile is
		// answered ClosedAnswer (DefWindowProc's 0 is an older build's answer), never taken.
		l.closing = true
		l.pending = nil
		w32.DestroyWindow(l.hwnd)
		listeners.Lock()
		delete(listeners.by, l.hwnd)
		listeners.Unlock()
		w32.UnregisterClass(l.class, w32.GetModuleHandle())
	}
	if in.mutex != 0 {
		windows.CloseHandle(in.mutex)
		in.mutex = 0
	}
}

// accepts reports a dwData the listener takes.
func (l *listener) accepts(kind uintptr) bool {
	return kind == l.opts.Kind || slices.Contains(l.opts.AlsoKinds, kind)
}

// deliver hands the payloads taken so far to OnData, in order.
func (l *listener) deliver() {
	take := l.pending
	l.pending = nil
	for _, b := range take {
		if l.opts.OnData != nil {
			l.opts.OnData(b)
		}
	}
}

// Drain hands OnData, now, every payload the message-only window has taken and not handed
// on yet (a host that is about to end looks at what came meanwhile). For the UI thread.
func (in *Instance) Drain() {
	if in != nil && in.l != nil {
		in.l.deliver()
	}
}

// copyDataStruct is COPYDATASTRUCT.
type copyDataStruct struct {
	data uintptr // dwData
	size uint32  // cbData
	ptr  uintptr // lpData
}

func listenerWndProc(h w32.HWND, msg uint32, wp, lp uintptr) (ret uintptr) {
	listeners.Lock()
	l := listeners.by[h]
	listeners.Unlock()
	if l == nil {
		return w32.DefWindowProc(h, msg, wp, lp)
	}
	defer func() {
		// A panic in a callback cannot unwind through Win32: the message counts as handled.
		if v := recover(); v != nil {
			ret = 0
		}
	}()
	switch msg {
	case wmCopyData:
		cds := (*copyDataStruct)(com.Addr(lp))
		if cds == nil || !l.accepts(cds.data) || int64(cds.size) > int64(l.opts.Max) {
			return 0
		}
		if l.closing {
			return l.opts.ClosedAnswer
		}
		result, take := uintptr(1), true
		if l.opts.Answer != nil {
			result, take = l.opts.Answer(cds.data)
		}
		if !take {
			return result
		}
		if len(l.pending) >= MaxPendingHandoffs {
			return l.opts.BusyAnswer // not taken: the sender reports it (a flood while the UI thread is busy)
		}
		b := make([]byte, cds.size)
		if cds.size > 0 && cds.ptr != 0 {
			copy(b, unsafe.Slice((*byte)(com.Addr(cds.ptr)), cds.size))
		}
		l.pending = append(l.pending, b)
		w32.PostMessage(h, msgHandoff, 0, 0)
		return result
	case msgHandoff:
		if !l.closing {
			l.deliver()
		}
		return 0
	case w32.WM_CLOSE:
		if l.opts.OnClose != nil {
			l.opts.OnClose()
		}
		return 0
	}
	return w32.DefWindowProc(h, msg, wp, lp)
}

// Forward hands payload to the running window as WM_COPYDATA with dwData kind, and waits at
// most timeout for it to take it (Wails v2.17's sender waits for ever: a hung window would
// keep a second start, the browser's or the tray's, waiting).
func (r Running) Forward(kind uintptr, payload []byte, timeout time.Duration) error {
	_, err := r.Send(kind, payload, timeout)
	return err
}

// Send is Forward that also returns what the running window's WM_COPYDATA answered (a
// host's answer tells it from today's windows, which answer 0 or 1).
func (r Running) Send(kind uintptr, payload []byte, timeout time.Duration) (uintptr, error) {
	if r.hwnd == 0 {
		return 0, errInvalid("Forward without a running window")
	}
	// The structure, the answer and the bytes in one block outside the Go heap: the
	// receiver may be a window of this thread, whose procedure runs Go code during the call.
	head := unsafe.Sizeof(copyDataStruct{})
	slot := unsafe.Sizeof(uintptr(0))
	block, err := windows.LocalAlloc(0x0040, uint32(head+slot)+uint32(len(payload))+1) // LPTR
	if block == 0 {
		return 0, fmt.Errorf("win: hand-off memory: %w", err)
	}
	defer windows.LocalFree(windows.Handle(block))
	data := block + head + slot
	if len(payload) > 0 {
		copy(unsafe.Slice((*byte)(com.Addr(data)), len(payload)), payload)
	}
	cds := (*copyDataStruct)(com.Addr(block))
	cds.data, cds.size, cds.ptr = kind, uint32(len(payload)), data
	ms := uint32(min(max(timeout, time.Millisecond), time.Hour) / time.Millisecond)
	if _, err := sendMessageTimeout(r.hwnd, wmCopyData, 0, block, smtoAbortIfHung|smtoBlock|smtoErrorOnExit, ms, block+head); err != nil {
		return 0, fmt.Errorf("win: the running window did not take the hand-off: %w", err)
	}
	return *(*uintptr)(com.Addr(block + head)), nil
}

// BringToFront brings the window to the front, as a second start of today's window does:
// a minimised window is restored (to the size and state it had), a hidden one shown, and
// the window asked to take the foreground. It reports whether Windows made it the
// foreground window (a process that is not allowed to, because the user is using another
// window and the starting process did not hand its right over, only flashes its taskbar
// button). It is for the UI thread.
func (w *Window) BringToFront() bool {
	if w.closed || w.hwnd == 0 {
		return false
	}
	if w32.IsIconic(w.hwnd) {
		w32.ShowWindow(w.hwnd, w32.SW_RESTORE)
	} else {
		w32.ShowWindow(w.hwnd, w32.SW_SHOW)
	}
	return setForegroundWindow(w.hwnd)
}
