//go:build windows

package win

import (
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"downex/ui/kit"
	"downex/ui/win/internal/com"
	"downex/ui/win/internal/w32"

	"golang.org/x/sys/windows"
)

// App is the UI thread's part of the backend: COM in a single-threaded apartment, per-monitor
// DPI awareness, the window class, the process's Resources and the message loop. Make it
// with NewApp on a goroutine locked to its OS thread (runtime.LockOSThread), and make
// windows, run the loop and close it on that same thread.
type App struct {
	res    *Resources
	ownRes bool
	thread uint32
	inst   windows.Handle
	class  *uint16
	wc     *w32.WNDCLASSEXW
	arrow  windows.Handle

	windows map[w32.HWND]*Window
	msg     w32.MSG // the loop's message, on the heap (GetMessage runs window procedures)
	clock   *frameClock
	comInit bool
	closed  bool
	// msgWnd is the App's message-only window: the wake-ups and notices that are not for
	// one window go to it, not to the thread, because a modal loop (a window drag, a menu, a
	// message box) dispatches a thread message to no window and so drops it.
	msgWnd w32.HWND
	// gone is set when the App closes, for goroutines that post to msgWnd.
	gone   atomic.Bool
	iconID uint16 // the executable's icon resource; each window loads it at its DPI
	// readEnv reads a window's environment at a DPI (tests replace it); watch follows the
	// settings no window message announces (the text size).
	readEnv  func(dpi uint32) kit.Environment
	watch    *settingsWatch
	autoHide *autoHideCache

	cursors                [len(cursorIDs)]windows.Handle
	wheelLines, wheelChars uint32
	panicked               error // a panic in a window procedure, raised again by Run
	quit                   bool  // Quit was called: Run returns

	// The process host's hooks (AppOptions); faults and closedQ are what Run hands them
	// once the message that produced them has returned.
	onPanic      func(w *Window, err error)
	onMade       func(w *Window)
	onClosed     func(w *Window)
	onEndSession func()
	faults       []fault
	closedQ      []*Window
	ended        bool // WM_ENDSESSION was handed to onEndSession

	postMu sync.Mutex
	posts  []func() // Post's functions, until the message window runs them
}

// fault is a panic recovered in a procedure of window w (nil: of no window of the App).
type fault struct {
	w   *Window
	err error
}

// AppOptions are an App's choices.
type AppOptions struct {
	// Resources to draw with; nil makes the App its own (closed with it).
	Resources *Resources
	// ClassName is the window class's name; "" is DownexNativeWindow. Each kind of process
	// may have its own (the preview's differs from the product's).
	ClassName string
	// Icon is the resource id of the executable's icon for the windows' title bars, taskbar
	// buttons and Alt+Tab; 0 is the system's default icon.
	Icon uint16

	// The hooks of a process that hosts several windows (each optional, each called on the
	// UI thread by Run, outside every window procedure):
	//
	// OnPanic gets a panic recovered in a procedure of window w (nil when it was no window's)
	// instead of Run raising it again, so that one window's failure leaves the others
	// running; the window was put back in order (no call in progress, no frame open).
	OnPanic func(w *Window, err error)
	// OnWindowMade is told each window NewWindow makes, as soon as its HWND exists and
	// before its root is attached (called by NewWindow, not by Run): a host that opens a
	// window knows it even when the opening fails half way.
	OnWindowMade func(w *Window)
	// OnWindowClosed is told each window once it is destroyed (its root detached).
	OnWindowClosed func(w *Window)
	// OnEndSession is told, once, that the user's session ends (WM_ENDSESSION with TRUE to
	// any window): the process is ended as soon as the message returns, so it is called
	// inside it, and whatever has to be stored must be stored before it returns.
	OnEndSession func()
}

// The window procedure and the table that finds a window from its HWND: Win32 holds the
// procedure, never a Go pointer, and a window being made is found by its number, which
// CreateWindowEx hands to WM_NCCREATE.
var (
	wndProcOnce sync.Once
	wndProcPtr  uintptr
	windowTable struct {
		sync.Mutex
		byHWND map[w32.HWND]*Window
		making map[uintptr]*Window
		apps   map[w32.HWND]*App // by message window
		next   uintptr
	}
)

const className = "DownexNativeWindow"

// NewApp starts the backend on the calling thread, which must be locked to its goroutine.
func NewApp(opts AppOptions) (*App, error) {
	if err := archErr(goarch); err != nil {
		return nil, err
	}
	if err := w32.Load(); err != nil {
		return nil, fmt.Errorf("win: %w: %w", err, kit.ErrUnsupported)
	}
	a := &App{thread: windows.GetCurrentThreadId(), windows: map[w32.HWND]*Window{}, readEnv: readEnvironment,
		onPanic: opts.OnPanic, onMade: opts.OnWindowMade, onClosed: opts.OnWindowClosed, onEndSession: opts.OnEndSession}
	// STA, as OLE drag and drop, the clipboard and UI Automation want. A thread already in
	// a multithreaded apartment keeps it.
	switch err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); err {
	case nil, windows.Errno(1): // S_OK, S_FALSE (initialised before): both are undone by Close
		a.comInit = true
	case windows.Errno(0x80010106): // RPC_E_CHANGED_MODE
	default:
		return nil, fmt.Errorf("win: CoInitializeEx: %w", err)
	}
	// The manifest sets per-monitor v2 in a product build; this covers tests and tools.
	w32.SetProcessDpiAwarenessContext(w32.DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2)
	a.res = opts.Resources
	if a.res == nil {
		r, err := NewResources()
		if err != nil {
			a.Close()
			return nil, err
		}
		a.res, a.ownRes = r, true
	}
	wndProcOnce.Do(func() {
		wndProcPtr = w32.NewCallback(wndProc)
		windowTable.byHWND = map[w32.HWND]*Window{}
		windowTable.making = map[uintptr]*Window{}
		windowTable.apps = map[w32.HWND]*App{}
	})
	a.inst = w32.GetModuleHandle()
	name := opts.ClassName
	if name == "" {
		name = className
	}
	var err error
	if a.class, err = windows.UTF16PtrFromString(name); err != nil {
		a.Close()
		return nil, errInvalid("class name %q", name)
	}
	a.arrow = w32.LoadCursor(w32.IDC_ARROW)
	a.iconID = opts.Icon
	// The class has no icons: each window loads its own at its monitor's DPI (icons.go).
	a.wc = &w32.WNDCLASSEXW{
		Style:     w32.CS_HREDRAW | w32.CS_VREDRAW,
		WndProc:   wndProcPtr,
		Instance:  a.inst,
		Cursor:    a.arrow,
		ClassName: a.class,
	}
	a.wc.Size = uint32(unsafe.Sizeof(*a.wc))
	if _, err := w32.RegisterClassEx(a.wc); err != nil && !errors.Is(err, windows.ERROR_CLASS_ALREADY_EXISTS) {
		a.Close()
		return nil, fmt.Errorf("win: %w", err)
	}
	h, err := w32.CreateWindowEx(0, a.class, nil, 0, 0, 0, 0, 0, w32.HWND_MESSAGE, a.inst, 0)
	if err != nil {
		a.Close()
		return nil, fmt.Errorf("win: message window: %w", err)
	}
	a.msgWnd = h
	windowTable.Lock()
	windowTable.apps[h] = a
	windowTable.Unlock()
	a.readSettings()
	a.clock = newFrameClock()
	a.autoHide = newAutoHideCache(readAutoHide, a.notifyAutoHide)
	a.watch = watchSettings(a.msgWnd)
	return a, nil
}

// post sends msg to the App's message window from any goroutine, unless the App is closed.
func (a *App) post(msg uint32) error {
	if a.gone.Load() {
		return fmt.Errorf("win: app closed: %w", kit.ErrClosed)
	}
	return w32.PostMessage(a.msgWnd, msg, 0, 0)
}

// msgProc is the message window's procedure.
func (a *App) msgProc(h w32.HWND, msg uint32, wp, lp uintptr) uintptr {
	switch msg {
	case msgWake:
		return 0 // Run looks at its state once the message is dispatched
	case msgSettings:
		a.settingsChanged()
		return 0
	case msgAutoHide:
		a.autoHideChanged()
		return 0
	case msgAppPost:
		a.runPosts()
		return 0
	}
	return w32.DefWindowProc(h, msg, wp, lp)
}

// Post runs f on the UI thread, from any goroutine, whether or not a window is open: in
// Run's loop, or a modal loop's. It fails once the App is closed (f then never runs).
func (a *App) Post(f func()) error {
	if f == nil {
		return errInvalid("Post of a nil function")
	}
	a.postMu.Lock()
	if a.gone.Load() {
		a.postMu.Unlock()
		return fmt.Errorf("win: app closed: %w", kit.ErrClosed)
	}
	wake := len(a.posts) == 0
	a.posts = append(a.posts, f)
	a.postMu.Unlock()
	if wake {
		if err := a.post(msgAppPost); err != nil {
			a.postMu.Lock()
			a.posts = nil
			a.postMu.Unlock()
			return err
		}
	}
	return nil
}

// runPosts runs Post's functions, in order; a panic in one is a fault of no window.
func (a *App) runPosts() {
	a.postMu.Lock()
	take := a.posts
	a.posts = nil
	a.postMu.Unlock()
	for _, f := range take {
		a.runSafely(f)
	}
}

func (a *App) runSafely(f func()) {
	defer func() {
		if v := recover(); v != nil {
			a.failIn(nil, v, debug.Stack())
		}
	}()
	f()
}

// settle hands Run's hooks what the last message produced: the windows closed, then the
// faults (or raises a fault again when there is no OnPanic).
func (a *App) settle() {
	for len(a.closedQ) > 0 || len(a.faults) > 0 {
		if len(a.closedQ) > 0 {
			w := a.closedQ[0]
			a.closedQ = a.closedQ[1:]
			if a.onClosed != nil {
				a.onClosed(w)
			}
			continue
		}
		f := a.faults[0]
		a.faults = a.faults[1:]
		a.onPanic(f.w, f.err)
	}
	if a.panicked != nil {
		panic(a.panicked)
	}
}

// environment is a window's environment at dpi.
func (a *App) environment(dpi uint32) kit.Environment { return a.readEnv(dpi) }

// settingsChanged reads every window's environment again (a setting changed that no window
// message announced). Each window hears it through its own procedure, which keeps a panic
// in its root from unwinding through Win32.
func (a *App) settingsChanged() {
	a.readSettings()
	for _, w := range a.windows {
		w.proc(msgEnvChanged, 0, 0)
	}
}

// Resources is the App's drawing resources, which its windows share.
func (a *App) Resources() *Resources { return a.res }

func (a *App) onThread() error {
	if a.closed {
		return fmt.Errorf("win: app closed: %w", kit.ErrClosed)
	}
	if id := windows.GetCurrentThreadId(); id != a.thread {
		return fmt.Errorf("win: called on thread %d, not the UI thread %d (lock the goroutine to its thread): %w", id, a.thread, kit.ErrInvalid)
	}
	return nil
}

// Run runs the message loop until every window is closed (it returns at once when there is
// none) or Quit is called. It ends with its own wake-up message, not WM_QUIT, so it leaves
// nothing behind on the thread for a later loop.
func (a *App) Run() error {
	if err := a.onThread(); err != nil {
		return err
	}
	a.quit = false
	a.settle()
	for len(a.windows) > 0 && !a.quit {
		r := w32.GetMessage(&a.msg)
		if r == 0 {
			w32.PostQuitMessage(int32(a.msg.WParam)) // someone else's quit: leave it for the outer loop
			return nil
		}
		if r < 0 {
			return fmt.Errorf("win: GetMessage failed")
		}
		a.dispatch(&a.msg)
		a.settle()
	}
	return nil
}

// wake makes Run look at its state again. It fails only when the thread's message queue is
// full, and then Run has messages to wake up for anyway.
func (a *App) wake() { a.post(msgWake) }

// dispatch runs one message. A key the root uses produces no character: the key message is
// dispatched before TranslateMessage would make one.
func (a *App) dispatch(m *w32.MSG) {
	switch m.Message {
	case w32.WM_KEYDOWN, w32.WM_SYSKEYDOWN:
		w := a.windows[m.Hwnd]
		if w == nil {
			if f := fieldByHWND(m.Hwnd); f != nil && f.w.app == a {
				w = f.w // a hosted field: the root sees the key first, then the field
			}
		}
		if w != nil {
			w.keyUsed = false
			w32.DispatchMessage(m)
			if !w.keyUsed || m.WParam == w32.VK_PROCESSKEY {
				w32.TranslateMessage(m)
			}
			return
		}
	}
	w32.TranslateMessage(m)
	w32.DispatchMessage(m)
}

// Quit ends Run, leaving the windows open. It is for the UI thread.
func (a *App) Quit() {
	a.quit = true
	a.wake()
}

// Close closes every window (without a CloseEvent), then releases what the App holds.
func (a *App) Close() {
	if a.closed {
		return
	}
	for _, w := range a.windows {
		w.destroy()
	}
	a.gone.Store(true)
	if a.clock != nil {
		a.clock.stop()
	}
	if a.watch != nil {
		a.watch.stop() // before the window it posts to goes
	}
	if a.msgWnd != 0 {
		windowTable.Lock()
		delete(windowTable.apps, a.msgWnd)
		windowTable.Unlock()
		w32.DestroyWindow(a.msgWnd)
		a.msgWnd = 0
	}
	if a.class != nil {
		w32.UnregisterClass(a.class, a.inst)
	}
	if a.ownRes && a.res != nil {
		a.res.Close()
	}
	if a.comInit {
		windows.CoUninitialize()
	}
	a.closed = true
}

// wndProc is every window's procedure: it finds the window by HWND (or, while it is made,
// by the number CreateWindowEx passed) and hands the message to it; an App's message window
// gets its messages too.
func wndProc(h w32.HWND, msg uint32, wp, lp uintptr) uintptr {
	windowTable.Lock()
	w := windowTable.byHWND[h]
	if w == nil && msg == w32.WM_NCCREATE {
		cs := (*w32.CREATESTRUCTW)(com.Addr(lp))
		if w = windowTable.making[cs.CreateParams]; w != nil {
			w.hwnd = h
			windowTable.byHWND[h] = w
		}
	}
	var a *App
	if w == nil {
		a = windowTable.apps[h]
	}
	windowTable.Unlock()
	switch {
	case w != nil:
		return w.proc(msg, wp, lp)
	case a != nil:
		return a.msgProc(h, msg, wp, lp)
	}
	return w32.DefWindowProc(h, msg, wp, lp)
}

// frameClock wakes windows that asked for a frame at the compositor's next frame
// (DwmFlush, on a goroutine of its own), so that animations run at the display's rate and
// nothing runs when no window asks.
type frameClock struct {
	mu    sync.Mutex
	want  map[w32.HWND]bool
	wake  chan struct{}
	quit  chan struct{}
	ticks int
}

const (
	msgFrame      = w32.WM_APP + 1 // to a window: the frame clock's tick
	msgPost       = w32.WM_APP + 2 // to a window: Host.Post's wake-up
	msgWake       = w32.WM_APP + 3 // to the message window: Run looks at its state
	msgSettings   = w32.WM_APP + 4 // to the message window: a watched setting changed
	msgAutoHide   = w32.WM_APP + 5 // to the message window: a monitor's auto-hide taskbars changed
	msgEnvChanged = w32.WM_APP + 6 // called on each window's procedure: read the environment again
	msgAppPost    = w32.WM_APP + 7 // to the message window: run App.Post's functions
)

func newFrameClock() *frameClock {
	c := &frameClock{want: map[w32.HWND]bool{}, wake: make(chan struct{}, 1), quit: make(chan struct{})}
	go c.run()
	return c
}

func (c *frameClock) request(h w32.HWND) {
	c.mu.Lock()
	c.want[h] = true
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// cancel forgets h's request: a destroyed window's HWND may be reused by another window.
func (c *frameClock) cancel(h w32.HWND) {
	c.mu.Lock()
	delete(c.want, h)
	c.mu.Unlock()
}

func (c *frameClock) stop() { close(c.quit) }

func (c *frameClock) run() {
	var due []w32.HWND
	last := time.Now()
	for {
		select {
		case <-c.quit:
			return
		case <-c.wake:
		}
		for {
			// The compositor's next frame; a flush that returns at once (nothing composed)
			// still waits out a 60 Hz frame.
			if w32.DwmFlush() != nil || time.Since(last) < 4*time.Millisecond {
				time.Sleep(time.Until(last.Add(16 * time.Millisecond)))
			}
			last = time.Now()
			c.mu.Lock()
			due = due[:0]
			for h := range c.want {
				due = append(due, h)
				delete(c.want, h)
			}
			c.ticks++
			c.mu.Unlock()
			if len(due) == 0 {
				break
			}
			for _, h := range due {
				if err := w32.PostMessage(h, msgFrame, 0, 0); err != nil && !errors.Is(err, windows.ERROR_INVALID_WINDOW_HANDLE) {
					// The thread's queue is full: try again at the next tick, or the window
					// would wait for this frame (frameWanted) for ever.
					c.mu.Lock()
					c.want[h] = true
					c.mu.Unlock()
				}
			}
			select {
			case <-c.quit:
				return
			case <-c.wake:
			default:
				// No new request yet: the next one wakes the loop again.
			}
		}
	}
}
