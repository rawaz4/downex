//go:build windows

package win

import (
	"errors"
	"fmt"
	"math"
	"runtime/debug"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	"downex/ui/kit"
	"downex/ui/win/internal/com"
	"downex/ui/win/internal/w32"

	"golang.org/x/sys/windows"
)

// Window is one top-level window hosting a kit.Root: it implements kit.Host for it, draws it
// with Direct2D when the system asks (WM_PAINT: uncovered, resized) and at the display-paced
// frames the root asks for (RequestFrame, Invalidate), and turns the window's messages into
// kit events. It has the system's frame, or a custom one (WindowOptions.CustomFrame, frame.go)
// whose title bar the root draws and describes with Root.FrameHit. It hosts native text
// fields for the root (kit.NativeFields, field.go).
type Window struct {
	app     *App
	root    kit.Root
	hwnd    w32.HWND
	style   uint32 // the window's styles, which its frame's size follows
	exStyle uint32
	icons   windowIcons

	target   *hwndTarget
	dpi      uint32
	env      kit.Environment
	size     kit.Size // client size in DIPs; the restored one while minimised
	px       w32.Point
	state    kit.WindowState
	minSize  kit.Size
	maxSize  kit.Size // the largest client size the user can resize to; a side of 0 has none
	resizing bool     // the window's own Resize runs, which the largest size does not hold back
	cursor   kit.Cursor
	colors   kit.FrameColors
	active   bool
	bgBrush  windows.Handle
	bgColor  kit.Color
	painted  bool // the first frame is shown
	title    *uint16
	textIn   kit.TextInput
	attached bool // Attach was called: events go to the root

	// The never-nest rule (kit's package doc): calls into the root run at depth 1, and what
	// the window gets meanwhile waits in queue until the call has returned.
	depth   int
	queue   []queued
	closing bool // Close was called: destroy once the call in progress returns
	closed  bool // destroyed; the root got Detach
	uiaDone bool // WM_DESTROY disconnected UI Automation: no provider is made again
	// closeAsked: the WM_CLOSE being sent now is the window's own (CloseNow, or the
	// system's SC_CLOSE: Alt+F4, the system menu, the taskbar, which DefWindowProc turns
	// into a WM_CLOSE before it returns); it lasts only while that message is sent. A
	// posted own close (Command(WindowClose)) carries ownCloseMark instead. Any other
	// WM_CLOSE is foreign (an installer's CloseMainWindow), which onForeignClose may take.
	closeAsked     bool
	onForeignClose func() bool

	pending     kit.Rect // area to paint at the next frame (DIPs)
	frameWanted bool
	inFrame     bool // Root.Frame is running: what it invalidates is painted next
	painting    bool // a frame is being made
	retrying    bool // a repaint after a failed target is scheduled

	tracking bool // WM_MOUSELEAVE is asked for: the pointer is over the client area
	// The custom frame (frame.go).
	custom     bool
	forceDPI   uint32 // the DPI the content is drawn at whatever the monitor's (0: the monitor's)
	ncTracking bool   // WM_NCMOUSELEAVE is asked for: the pointer is over the caption or a button
	inside     bool   // the root was told the pointer is over the window (PointerEnter)
	// overWindow reports whether the pointer is over a part of the window the root sees
	// (its client area, the caption and its buttons, a hosted field); tests replace it.
	overWindow func() bool
	fields     []*textField // hosted native fields (field.go)
	captured   bool         // CapturePointer is in effect
	releasing  bool         // our own ReleaseCapture, not something else taking the capture
	buttons    kit.Buttons
	click      struct {
		button kit.Buttons
		time   int32
		pt     w32.Point
		n      int
	}
	keyUsed   bool   // the root used the key message being dispatched
	surrogate uint16 // a high surrogate waiting for its low half (WM_CHAR)

	postMu     sync.Mutex
	posts      []func() // Post's functions, until msgPost moves them to queue
	spare      []func() // the other buffer of posts (the UI thread's)
	postWoken  bool     // a msgPost is on its way
	postClosed bool

	// Heap memory for the Win32 calls that run window procedures (see package w32), and for
	// DWM attributes, whose changes can reach the window procedure too.
	ps        w32.PAINTSTRUCT
	placement w32.WINDOWPLACEMENT
	tme       w32.TRACKMOUSEEVENT
	dwmValue  uint32
	margins   w32.MARGINS

	// hostErr is the last failure of a Host method that reports none (Resize), for
	// diagnostics and tests.
	hostErr error

	// Counts for tests.
	frames, paints int
}

// WindowOptions are a window's choices; the root sets its title and sizes in Attach too.
type WindowOptions struct {
	Title string
	// Size is the client size in DIPs; zero is 800 × 600.
	Size kit.Size
	// MinSize is the smallest client size in DIPs that the user can resize to.
	MinSize kit.Size
	// At places the window's top-left corner at this screen point (physical pixels) when
	// Placed is set; otherwise the window is centred on the primary monitor's work area.
	At     w32.Point
	Placed bool
	// Software draws with Direct2D's software renderer instead of the GPU.
	Software bool
	// NoActivate shows the window without taking the focus, and keeps clicks from activating
	// it (tests and measurements: WS_EX_NOACTIVATE).
	NoActivate bool
	// CustomFrame gives the window no system title bar: the root's content fills the client
	// area from the window's top edge and draws its own title bar, which Root.FrameHit
	// describes (caption, minimise, maximise and close: dragging, the system's double click,
	// Snap Layouts on Windows 11). The system keeps the resize borders, the shadow, the
	// rounded corners and the system menu, and a maximised window's client area is its
	// monitor's work area.
	CustomFrame bool
	// ForceDPI draws the content at this DPI whatever the monitor's (a 100% screenshot on a
	// 125% monitor); the window then ignores DPI changes. 0 follows the monitor.
	ForceDPI uint32
	// OnForeignClose is asked about a WM_CLOSE that neither the window's own close
	// (kit.WindowClose: its title bar's button) nor the system's (Alt+F4, the system menu,
	// the taskbar's Close) asked for: another program's, such as an installer's
	// CloseMainWindow. Returning true takes it (the window does not close on its own); nil,
	// or false, closes the window as any WM_CLOSE does.
	OnForeignClose func() bool
	// Style is the window's Win32 style; 0 is windowStyle (a top-level window with the
	// system's frame). A popup (menus, lists, tooltips) is WS_POPUP. ExStyle adds extended
	// styles to what the other options set.
	Style, ExStyle uint32
}

// windowStyle is a window's default style.
const windowStyle = w32.WS_OVERLAPPEDWINDOW | w32.WS_CLIPCHILDREN

// queued is a call into the root that waits for the call in progress: an event, a native
// field's callback or a Post function.
type queued struct {
	e kit.Event
	f func()
}

// NewWindow opens a window for root. root.Attach runs before the window is shown, which
// happens with its first frame.
func (a *App) NewWindow(root kit.Root, opts WindowOptions) (*Window, error) {
	if err := a.onThread(); err != nil {
		return nil, err
	}
	if root == nil {
		return nil, errInvalid("NewWindow without a root")
	}
	w := &Window{app: a, root: root, minSize: opts.MinSize, active: true, custom: opts.CustomFrame, forceDPI: opts.ForceDPI,
		style: opts.Style, exStyle: opts.ExStyle, onForeignClose: opts.OnForeignClose}
	if w.style == 0 {
		w.style = windowStyle
	}
	if opts.NoActivate {
		w.exStyle |= w32.WS_EX_NOACTIVATE
	}
	w.overWindow = w.pointerOverWindow
	if opts.Size.W <= 0 || opts.Size.H <= 0 || !finite(opts.Size.W) || !finite(opts.Size.H) {
		opts.Size = kit.Size{W: 800, H: 600}
	}
	// The window is made at its place, size and DPI, and with its render target and
	// environment ready: Windows sends a popup WM_SIZE (and every window WM_GETMINMAXINFO)
	// before CreateWindowEx returns. The target makes its Direct2D target at the first frame.
	mon := w32.MonitorFromPoint(w32.Point{}, w32.MONITOR_DEFAULTTOPRIMARY)
	if opts.Placed {
		mon = w32.MonitorFromPoint(opts.At, w32.MONITOR_DEFAULTTONEAREST)
	}
	monDPI := w32.GetDpiForMonitor(mon)
	if monDPI == 0 {
		monDPI = 96
	}
	w.dpi = monDPI
	if w.forceDPI > 0 {
		w.dpi = w.forceDPI
	}
	w.env = a.environment(w.dpi)
	w.target = newHWNDTarget(w, opts.Software)
	rc := w.initialRect(opts, mon, monDPI)

	w.title, _ = windows.UTF16PtrFromString(opts.Title)
	windowTable.Lock()
	windowTable.next++
	id := windowTable.next
	windowTable.making[id] = w
	windowTable.Unlock()
	hwnd, err := w32.CreateWindowEx(w.exStyle, a.class, w.title, w.style,
		rc.Left, rc.Top, rc.Right-rc.Left, rc.Bottom-rc.Top, 0, a.inst, id)
	windowTable.Lock()
	delete(windowTable.making, id)
	windowTable.Unlock()
	if err != nil {
		w.target.close()
		return nil, fmt.Errorf("win: %w", err)
	}
	w.hwnd = hwnd
	a.windows[hwnd] = w
	if a.onMade != nil {
		a.onMade(w) // before its root is attached: a failure from here on can close it
	}
	if w.custom {
		w.extendFrame()
	}
	// The system may still have put it on a monitor of another DPI: size and place it there.
	if d := w32.GetDpiForWindow(hwnd); d != 0 && d != monDPI {
		if w.forceDPI == 0 {
			w.setDPI(d)
			w.env = a.environment(d)
		}
		rc = w.initialRect(opts, w32.MonitorFromWindow(hwnd, w32.MONITOR_DEFAULTTONEAREST), d)
		w32.SetWindowPos(hwnd, rc.Left, rc.Top, rc.Right-rc.Left, rc.Bottom-rc.Top, w32.SWP_NOZORDER|w32.SWP_NOACTIVATE)
	}
	w.applyIcons()
	if w.custom { // what a maximised window's frame will need, read ahead (autohide.go)
		m := w32.MonitorFromWindow(hwnd, w32.MONITOR_DEFAULTTONEAREST)
		if mi, ok := w32.GetMonitorInfo(m); ok {
			a.autoHide.edges(m, mi.Monitor)
		}
	}
	w.readClient()

	w.attached = true
	w.depth++
	root.Attach(w)
	w.depth--

	// Shown cloaked until the first frame is drawn, so it never flashes empty.
	w.setDWM(w32.DWMWA_CLOAK, 1)
	show := int32(w32.SW_SHOW)
	if opts.NoActivate {
		show = w32.SW_SHOWNOACTIVATE
	}
	w32.ShowWindow(hwnd, show)
	w32.UpdateWindow(hwnd)
	w.setDWM(w32.DWMWA_CLOAK, 0)
	w.drain()
	if w.closed {
		return nil, fmt.Errorf("win: the window was closed while it opened: %w", kit.ErrClosed)
	}
	return w, nil
}

// initialRect is the window's first rectangle on monitor mon, whose DPI (the frame's) is
// dpi: the client size of opts with the frame around it, at opts.At or centred on the
// monitor's work area, and fitted into the work area, as Resize fits it.
func (w *Window) initialRect(opts WindowOptions, mon windows.Handle, dpi uint32) w32.Rect {
	ww, wh := w.outerSize(w.pixels(opts.Size.W), w.pixels(opts.Size.H), dpi)
	x, y := opts.At.X, opts.At.Y
	if mi, ok := w32.GetMonitorInfo(mon); ok {
		work := mi.Work
		ww, wh = min(ww, work.Right-work.Left), min(wh, work.Bottom-work.Top)
		if !opts.Placed {
			x = work.Left + (work.Right-work.Left-ww)/2
			y = work.Top + (work.Bottom-work.Top-wh)/2
		}
		x = max(min(x, work.Right-ww), work.Left)
		y = max(min(y, work.Bottom-wh), work.Top)
	}
	return w32.Rect{Left: x, Top: y, Right: x + ww, Bottom: y + wh}
}

// HWND is the window's handle (for the custom frame and tools).
func (w *Window) HWND() w32.HWND { return w.hwnd }

// Renderer names what draws the window: "hardware" (Direct2D on the GPU), "software" (asked
// for, the GPU target could not be made, or the GPU kept losing the device), or "" before
// the first frame and while a lost target is made again.
func (w *Window) Renderer() string { return w.target.Renderer() }

func (w *Window) ppd() float32 { return float32(w.dpi) / 96 }

func (w *Window) pixels(d kit.DIP) int32 { return int32(math.Round(float64(d * w.ppd()))) }

// outerSize is the window size that has a client area of cw × ch pixels with the window's
// frame at dpi, the DPI of the monitor the frame is on (sysDPI: the content's DPI may be
// forced). A custom frame keeps the system's side and bottom borders and has no top.
func (w *Window) outerSize(cw, ch int32, dpi uint32) (int32, int32) {
	rc := w32.Rect{Right: cw, Bottom: ch}
	w32.AdjustWindowRectExForDpi(&rc, w.style, w.exStyle, dpi)
	if w.custom {
		return rc.Right - rc.Left, rc.Bottom
	}
	return rc.Right - rc.Left, rc.Bottom - rc.Top
}

// readClient updates the size from the client rectangle; it reports a change in DIPs.
func (w *Window) readClient() bool {
	rc := w32.GetClientRect(w.hwnd)
	px := w32.Point{X: rc.Right - rc.Left, Y: rc.Bottom - rc.Top}
	if px.X <= 0 && px.Y <= 0 && w32.IsIconic(w.hwnd) {
		return false // minimised: the restored size stands
	}
	w.px = px
	s := kit.Size{W: float32(px.X) / w.ppd(), H: float32(px.Y) / w.ppd()}
	if s == w.size {
		return false
	}
	w.size = s
	return true
}

func (w *Window) setDPI(dpi uint32) {
	w.dpi = dpi
	w.target.dev.setPPD(w.ppd())
	for _, f := range w.fields {
		f.dpiChanged() // the fields' fonts and places are in pixels
	}
}

// ---- calls into the root, one at a time ----

// event gives the root e now, or queues it while a call into the root is in progress.
func (w *Window) event(e kit.Event) bool {
	if w.closed || !w.attached {
		return false
	}
	if w.depth > 0 {
		w.queue = append(w.queue, queued{e: e})
		return false
	}
	used := w.callEvent(e)
	w.drain()
	return used
}

// call runs f as a call into the root (a native field's callback): now, or after the call
// in progress.
func (w *Window) call(f func()) {
	if w.closed || !w.attached || f == nil {
		return
	}
	if w.depth > 0 {
		w.queue = append(w.queue, queued{f: f})
		return
	}
	w.depth++
	f()
	w.depth--
	w.drain()
}

func (w *Window) callEvent(e kit.Event) bool {
	w.depth++
	used := w.root.Event(e)
	w.depth--
	return used
}

// drain delivers what waited for the call in progress, and closes the window when Close
// was called.
func (w *Window) drain() {
	for w.depth == 0 && !w.closed {
		if len(w.queue) > 0 {
			q := w.queue[0]
			copy(w.queue, w.queue[1:])
			w.queue[len(w.queue)-1] = queued{}
			w.queue = w.queue[:len(w.queue)-1]
			if q.f != nil {
				w.depth++
				q.f()
				w.depth--
				continue
			}
			used := w.callEvent(q.e)
			if _, ok := q.e.(kit.CloseEvent); ok && !used {
				w.destroy()
				return
			}
			continue
		}
		if w.closing {
			w.destroy()
		}
		return
	}
}

// destroy closes the window now: Detach is the root's last call.
func (w *Window) destroy() {
	if w.closed || w.hwnd == 0 {
		return
	}
	w32.DestroyWindow(w.hwnd) // WM_DESTROY detaches
}

func (w *Window) detach() {
	if w.closed {
		return
	}
	w.closed = true
	w.postMu.Lock()
	w.postClosed = true
	w.posts = nil
	w.postMu.Unlock()
	if w.captured {
		w.releasing = true
		w32.ReleaseCapture()
	}
	w.depth++
	w.root.Detach()
	w.depth--
	w.queue = nil
	for len(w.fields) > 0 {
		w.fields[len(w.fields)-1].Release() // a field the root kept goes with the window
	}
	w.target.close()
	if w.bgBrush != 0 {
		w32.DeleteObject(w.bgBrush)
		w.bgBrush = 0
	}
	w.app.clock.cancel(w.hwnd)
	delete(w.app.windows, w.hwnd)
	// Run tells OnWindowClosed once the message returns; a message another thread sent is
	// handled inside GetMessage, which then waits for a posted one: the wake-up is that.
	w.app.closedQ = append(w.app.closedQ, w)
	w.app.wake()
	windowTable.Lock()
	delete(windowTable.byHWND, w.hwnd)
	windowTable.Unlock()
}

// ---- frames ----

// frame makes one frame: Frame, then Paint of what is invalid (dirty, the pending area and
// what Frame invalidated). It also runs while a modal loop the root entered is open (the one
// exception to the never-nest rule), but never inside itself.
func (w *Window) frame(dirty kit.Rect) {
	if w.closed || w.painting || w.state.Minimized || !w.attached {
		if !w.closed {
			w.pending = w.pending.Union(dirty)
		}
		return
	}
	w.painting = true
	w.frames++
	w.inFrame = true
	w.depth++
	w.root.Frame(time.Now())
	w.depth--
	w.inFrame = false
	dirty = dirty.Union(w.pending)
	w.pending = kit.Rect{}
	bounds := kit.RectAt(kit.Point{}, w.size)
	if w.target.retryHardware(time.Now()) {
		dirty = bounds // a new target: nothing of the old one's content is kept
	}
	dirty = roundOut(dirty, w.ppd()).Intersect(roundOut(bounds, w.ppd()))
	if !dirty.Empty() {
		c, err := w.target.BeginFrame(dirty)
		if err == nil {
			w.paints++
			w.depth++
			w.root.Paint(c, dirty)
			w.depth--
			err = w.target.EndFrame()
			if err == nil {
				w.painted = true
			}
		}
		if err != nil {
			w.target.lastErr = err
			switch {
			case w.target.dev.rt != nil:
				// Drawn and shown; only unbalanced pushes were reported.
			case errors.Is(err, kit.ErrDeviceLost):
				// The next frame repaints everything on a new device, now or, when even the
				// software renderer keeps losing it, after a pause (hwndTarget.lost).
				if w.target.lost(time.Now()) {
					w.retryTarget()
				} else {
					w32.InvalidateRect(w.hwnd, nil, false)
				}
			default:
				// No target could be made: try again in a while, not in a loop of paints.
				w.retryTarget()
			}
		}
	}
	w.painting = false
	w.drain()
}

// retryTarget repaints the whole window after a pause, once.
func (w *Window) retryTarget() {
	if w.retrying {
		return
	}
	w.retrying = true
	time.AfterFunc(time.Second, func() {
		w.Post(func() {
			w.retrying = false
			w32.InvalidateRect(w.hwnd, nil, false)
		})
	})
}

func (w *Window) onPaint() {
	w32.BeginPaint(w.hwnd, &w.ps)
	r := w.ps.Paint
	w32.EndPaint(w.hwnd, &w.ps)
	ppd := w.ppd()
	w.frame(kit.R(float32(r.Left)/ppd, float32(r.Top)/ppd, float32(r.Right)/ppd, float32(r.Bottom)/ppd))
}

// ---- the window procedure ----

func (w *Window) proc(msg uint32, wp, lp uintptr) (ret uintptr) {
	defer func() {
		if v := recover(); v != nil {
			w.app.failIn(w, v, debug.Stack())
			w.recovered()
			ret = 0
		}
	}()
	if w.custom {
		if r, ok := w.frameProc(msg, wp, lp); ok {
			return r
		}
	}
	switch msg {
	case w32.WM_PAINT:
		w.onPaint()
		return 0
	case w32.WM_ERASEBKGND:
		if !w.painted && w.bgBrush != 0 {
			rc := w32.GetClientRect(w.hwnd)
			w32.FillRect(windows.Handle(wp), &rc, w.bgBrush)
		}
		return 1
	case msgFrame:
		if w.frameWanted && !w.state.Minimized {
			w.frameWanted = false
			if rc, ok := w32.GetUpdateRect(w.hwnd); ok {
				ppd := w.ppd()
				w.pending = w.pending.Union(kit.R(float32(rc.Left)/ppd, float32(rc.Top)/ppd, float32(rc.Right)/ppd, float32(rc.Bottom)/ppd))
				w32.ValidateRect(w.hwnd, nil)
			}
			w.frame(kit.Rect{})
		}
		return 0
	case msgPost:
		w.runPosts()
		return 0
	case msgEnvChanged:
		w.envChanged()
		return 0
	case w32.WM_SIZE:
		w.onSize(wp)
		return 0
	case w32.WM_GETMINMAXINFO:
		mmi := (*w32.MINMAXINFO)(com.Addr(lp))
		if w.minSize.W > 0 || w.minSize.H > 0 {
			ow, oh := w.outerSize(w.pixels(w.minSize.W), w.pixels(w.minSize.H), w.sysDPI())
			if w.minSize.W > 0 {
				mmi.MinTrackSize.X = ow
			}
			if w.minSize.H > 0 {
				mmi.MinTrackSize.Y = oh
			}
		}
		// The largest size holds the user's resizing back, not the window's own Resize
		// (DefWindowProc clamps SetWindowPos to it too), and never below the least.
		if (w.maxSize.W > 0 || w.maxSize.H > 0) && !w.resizing {
			ow, oh := w.outerSize(w.pixels(w.maxSize.W), w.pixels(w.maxSize.H), w.sysDPI())
			if w.maxSize.W > 0 {
				mmi.MaxTrackSize.X = max(ow, mmi.MinTrackSize.X)
			}
			if w.maxSize.H > 0 {
				mmi.MaxTrackSize.Y = max(oh, mmi.MinTrackSize.Y)
			}
		}
		return 0
	case w32.WM_GETDPISCALEDSIZE:
		if w.forceDPI > 0 {
			return 0
		}
		// Keep the client's size in DIPs exact on the new monitor.
		dpi := uint32(wp)
		sz := (*w32.Point)(com.Addr(lp))
		k := float64(dpi) / 96
		sz.X, sz.Y = w.outerSize(int32(math.Round(float64(w.size.W)*k)), int32(math.Round(float64(w.size.H)*k)), dpi)
		return 1
	case w32.WM_DPICHANGED:
		w.applyIcons() // the frame's icons follow the monitor, whatever the content's DPI
		if w.forceDPI > 0 {
			return 0 // drawn at its own DPI wherever it is
		}
		w.setDPI(uint32(wp & 0xFFFF))
		rc := (*w32.Rect)(com.Addr(lp))
		w32.SetWindowPos(w.hwnd, rc.Left, rc.Top, rc.Right-rc.Left, rc.Bottom-rc.Top, w32.SWP_NOZORDER|w32.SWP_NOACTIVATE)
		w.envChanged()
		w32.InvalidateRect(w.hwnd, nil, false)
		return 0
	case w32.WM_SETTINGCHANGE, w32.WM_THEMECHANGED, w32.WM_SYSCOLORCHANGE:
		w.app.readSettings()
		w.envChanged()
		if msg == w32.WM_SETTINGCHANGE && w.custom {
			w.app.autoHide.refresh() // the taskbar's auto-hide may have changed
		}
	case w32.WM_COMMAND:
		if f := w.fieldOf(w32.HWND(lp)); f != nil {
			f.command(uint16(wp >> 16))
			return 0
		}
	case w32.WM_CTLCOLOREDIT:
		if f := w.fieldOf(w32.HWND(lp)); f != nil {
			return f.ctlColor(windows.Handle(wp))
		}
	case wmGetObject: // UI Automation's root object (uia_window.go); MSAA's go to DefWindowProc
		if r, ok := w.uiaGetObject(wp, lp); ok {
			return r
		}
	case w32.WM_NCACTIVATE:
		w.active = wp != 0
		w.applyFrameColors()
	case w32.WM_SETCURSOR:
		if w32.HWND(wp) == w.hwnd && lp&0xFFFF == w32.HTCLIENT {
			w32.SetCursor(w.app.cursor(w.cursor))
			return 1
		}
	case w32.WM_SETFOCUS:
		w.event(kit.FocusEvent{In: true})
		w.applyTextInput()
		return 0
	case w32.WM_KILLFOCUS:
		if to := w32.HWND(wp); to == 0 || !w32.IsChild(w.hwnd, to) {
			w.event(kit.FocusEvent{In: false})
		}
		return 0
	case w32.WM_SYSCOMMAND:
		if wp&0xFFF0 == w32.SC_CLOSE {
			// Alt+F4, the system menu, the taskbar: the WM_CLOSE DefWindowProc sends now is the user's.
			var r uintptr
			w.ownClose(func() { r = w32.DefWindowProc(w.hwnd, msg, wp, lp) })
			return r
		}
	case w32.WM_ENDSESSION:
		if wp != 0 && w.app.onEndSession != nil && !w.app.ended {
			w.app.ended = true
			w.app.onEndSession()
		}
		return 0
	case w32.WM_CLOSE:
		own := w.closeAsked || wp == ownCloseMark
		w.closeAsked = false // a WM_CLOSE the root's answer sends (a modal loop's) is not this one
		if !own && w.onForeignClose != nil && w.onForeignClose() {
			return 0
		}
		if !w.attached {
			w.destroy()
			return 0
		}
		if w.depth > 0 {
			w.queue = append(w.queue, queued{e: kit.CloseEvent{}})
			return 0
		}
		if !w.callEvent(kit.CloseEvent{}) {
			w.destroy()
			return 0
		}
		w.drain()
		return 0
	case w32.WM_DESTROY:
		// UI Automation's elements of the window go first: every provider is disconnected
		// and the root handed back, as the UIA docs prescribe for a window being destroyed.
		w.uiaDestroyed()
		w.dropDestroyed() // RevokeDragDrop while the window exists (drop.go)
		w.detach()
		if len(w.app.windows) == 0 {
			w.app.wake()
		}
		return 0
	case w32.WM_NCDESTROY:
		h := w.hwnd
		windowTable.Lock()
		delete(windowTable.byHWND, h)
		windowTable.Unlock()
		r := w32.DefWindowProc(h, msg, wp, lp)
		w.hwnd = 0
		w.releaseIcons() // the window no longer shows them
		return r
	}
	if r, ok := w.input(msg, wp, lp); ok {
		return r
	}
	return w32.DefWindowProc(w.hwnd, msg, wp, lp)
}

func (w *Window) onSize(wp uintptr) {
	state := kit.WindowState{Maximized: wp == w32.SIZE_MAXIMIZED, Minimized: wp == w32.SIZE_MINIMIZED}
	old := w.px
	changed := w.readClient()
	if !state.Minimized {
		w.target.resize(w.px)
		w.fillGrown(old)
	}
	if changed {
		w.event(kit.SizeEvent{Size: w.size})
	}
	if state != w.state {
		w.state = state
		w.event(kit.WindowStateEvent{State: state})
		if !state.Minimized && w.frameWanted {
			w.app.clock.request(w.hwnd) // a frame asked for while minimised
		}
	}
	// A new size is painted, and the compositor has the frame, before the resize returns
	// (hwndTarget.waitComposed): left to WM_PAINT, or not waited for, the compositor shows
	// the window at its new size with the previous frame first. Not inside a call into the
	// root (never nest), and not before the first paint (WM_PAINT has it). The window is
	// validated first: this paint covers it, and a paint that fails invalidates it again.
	if changed && !state.Minimized && w.depth == 0 && w.painted {
		w32.ValidateRect(w.hwnd, nil)
		made := w.target.dev.frame
		w.frame(kit.RectAt(kit.Point{}, w.size))
		if w.target.dev.frame != made {
			w.target.waitComposed()
		}
	}
}

// fillGrown fills the part of the client area a resize added with the frame's background
// colour, by GDI, which reaches the window's surface at once. The compositor can still show
// a frame before the GPU has drawn the new size; that frame then has the old content and
// the background colour where the window grew, not black (the compositor's fill).
func (w *Window) fillGrown(old w32.Point) {
	if w.bgBrush == 0 || !w.painted || (w.px.X <= old.X && w.px.Y <= old.Y) {
		return
	}
	dc := w32.GetDC(w.hwnd)
	if dc == 0 {
		return
	}
	if w.px.X > old.X {
		w32.FillRect(dc, &w32.Rect{Left: old.X, Right: w.px.X, Bottom: w.px.Y}, w.bgBrush)
	}
	if w.px.Y > old.Y {
		w32.FillRect(dc, &w32.Rect{Top: old.Y, Right: w.px.X, Bottom: w.px.Y}, w.bgBrush)
	}
	w32.ReleaseDC(w.hwnd, dc)
}

func (w *Window) envChanged() {
	env := w.app.environment(w.dpi)
	if env != w.env {
		w.env = env
		w.event(kit.EnvironmentEvent{Env: env})
	}
}

// recovered puts the window back in order after a call into the root panicked: no call is
// in progress any more, and a frame left open is ended.
func (w *Window) recovered() {
	w.depth, w.inFrame, w.painting = 0, false, false
	w.queue = w.queue[:0]
	if w.target != nil && w.target.dev.inFrame {
		w.target.dev.canvas.stack = w.target.dev.canvas.stack[:0]
		w.target.dev.end()
	}
}

// fail records a panic in a window procedure: Win32 cannot unwind it, so the window
// procedure returns and Run panics with it on the Go side.
func (a *App) fail(v any, stack []byte) { a.failIn(nil, v, stack) }

// failIn records a panic in a procedure of window w (nil: of no window): Win32 cannot unwind
// it, so the procedure returns, and Run hands it to AppOptions.OnPanic, or panics with it on
// the Go side when there is none.
func (a *App) failIn(w *Window, v any, stack []byte) {
	err := fmt.Errorf("win: panic in a window procedure: %v\n%s", v, stack)
	switch {
	case a.onPanic != nil:
		a.faults = append(a.faults, fault{w: w, err: err})
	case a.panicked == nil:
		a.panicked = err
	}
	a.wake()
}

// ---- kit.Host ----

// Environment implements kit.Host.
func (w *Window) Environment() kit.Environment { return w.env }

// Size implements kit.Host.
func (w *Window) Size() kit.Size { return w.size }

// WindowState implements kit.Host.
func (w *Window) WindowState() kit.WindowState { return w.state }

// Resources implements kit.Host.
func (w *Window) Resources() kit.Resources { return w.app.res }

// Invalidate implements kit.Host. The area is painted at the display's next refresh (the
// frame clock's), so invalidations faster than the display coalesce into one frame.
func (w *Window) Invalidate(r kit.Rect) {
	if w.closed || r.Empty() || !finiteRect(r) {
		return
	}
	w.pending = w.pending.Union(r)
	if w.inFrame {
		return // painted in the frame in progress
	}
	w.RequestFrame()
}

// RequestFrame implements kit.Host.
func (w *Window) RequestFrame() {
	if w.closed || w.frameWanted {
		return
	}
	w.frameWanted = true
	if !w.state.Minimized {
		w.app.clock.request(w.hwnd)
	}
}

// SetCursor implements kit.Host.
func (w *Window) SetCursor(c kit.Cursor) {
	if w.closed || c == w.cursor {
		return
	}
	w.cursor = c
	if w.tracking || w.captured {
		w32.SetCursor(w.app.cursor(c))
	}
}

// CapturePointer implements kit.Host.
func (w *Window) CapturePointer() {
	if w.closed || w.captured {
		return
	}
	w.captured = true
	w32.SetCapture(w.hwnd)
}

// ReleasePointer implements kit.Host.
func (w *Window) ReleasePointer() {
	if w.closed || !w.captured {
		return
	}
	w.releasing = true
	w32.ReleaseCapture()
	w.releasing = false
	w.captured = false
}

// SetTextInput implements kit.Host.
func (w *Window) SetTextInput(t kit.TextInput) {
	if w.closed {
		return
	}
	w.textIn = t
	w.applyTextInput()
}

// applyTextInput gives the window an input context only while it takes text, and puts the
// input method's windows at the caret.
func (w *Window) applyTextInput() {
	if !w.textIn.Active {
		w32.ImmAssociateContextEx(w.hwnd, 0)
		return
	}
	w32.ImmAssociateContextEx(w.hwnd, w32.IACE_DEFAULT)
	imc := w32.ImmGetContext(w.hwnd)
	if imc == 0 {
		return
	}
	defer w32.ImmReleaseContext(w.hwnd, imc)
	ppd := w.ppd()
	c := w.textIn.Caret
	at := w32.Point{X: int32(c.Min.X * ppd), Y: int32(c.Min.Y * ppd)}
	area := w32.Rect{Left: at.X, Top: at.Y, Right: int32(math.Ceil(float64(c.Max.X * ppd))), Bottom: int32(math.Ceil(float64(c.Max.Y * ppd)))}
	cf := w32.COMPOSITIONFORM{Style: w32.CFS_POINT | w32.CFS_FORCE_POSITION, CurrentPos: at}
	w32.ImmSetCompositionWindow(imc, &cf)
	cand := w32.CANDIDATEFORM{Style: w32.CFS_EXCLUDE, CurrentPos: at, Area: area}
	w32.ImmSetCandidateWindow(imc, &cand)
}

// SetTitle implements kit.Host.
func (w *Window) SetTitle(title string) {
	if w.closed {
		return
	}
	p, err := windows.UTF16PtrFromString(title)
	if err != nil {
		p, _ = windows.UTF16PtrFromString(stripNUL(title))
	}
	w.title = p
	w32.SetWindowText(w.hwnd, p)
}

func stripNUL(s string) string {
	b := []rune(s)
	out := b[:0]
	for _, c := range b {
		if c != 0 {
			out = append(out, c)
		}
	}
	return string(out)
}

// SetMinSize implements kit.Host.
func (w *Window) SetMinSize(s kit.Size) {
	if w.closed || !finite(s.W) || !finite(s.H) {
		return
	}
	w.minSize = kit.Size{W: max(s.W, 0), H: max(s.H, 0)}
}

// SetMaxSize sets the largest client size, in DIPs, that the user can resize the window to
// (it follows DPI changes, and is never below the smallest); a side of 0 has no maximum. The
// window's own Resize and ResizeCentered are not held back by it.
func (w *Window) SetMaxSize(s kit.Size) {
	if w.closed || !finite(s.W) || !finite(s.H) {
		return
	}
	w.maxSize = kit.Size{W: max(s.W, 0), H: max(s.H, 0)}
}

// Resize implements kit.Host.
func (w *Window) Resize(s kit.Size) { w.resize(s, false) }

// ResizeCentered is Resize keeping the window's centre where it is instead of its top-left
// corner (a window that fits its content: it grows or shrinks about where the user sees it),
// fitted into its monitor's work area as Resize fits it.
func (w *Window) ResizeCentered(s kit.Size) { w.resize(s, true) }

func (w *Window) resize(s kit.Size, centred bool) {
	if w.closed || !(s.W > 0) || !(s.H > 0) || !finite(s.W) || !finite(s.H) {
		return
	}
	ww, wh := w.outerSize(w.pixels(s.W), w.pixels(s.H), w.sysDPI())
	if w.state.Maximized || w.state.Minimized {
		// The restored size changes; the window stays as it is, and is not activated.
		err := w32.GetWindowPlacement(w.hwnd, &w.placement)
		if err == nil {
			n := &w.placement.NormalPosition
			n.Left, n.Top = resizedOrigin(*n, ww, wh, centred)
			n.Right, n.Bottom = n.Left+ww, n.Top+wh
			w.placement.ShowCmd = w32.SW_SHOWNA
			err = w32.SetWindowPlacement(w.hwnd, &w.placement)
		}
		if err != nil {
			w.hostErr = fmt.Errorf("win: Resize of a maximised or minimised window: %w", err)
		}
		return
	}
	wr := w32.GetWindowRect(w.hwnd)
	x, y := resizedOrigin(wr, ww, wh, centred)
	if mi, ok := w32.GetMonitorInfo(w32.MonitorFromWindow(w.hwnd, w32.MONITOR_DEFAULTTONEAREST)); ok {
		work := mi.Work
		ww, wh = min(ww, work.Right-work.Left), min(wh, work.Bottom-work.Top)
		x, y = resizedOrigin(wr, ww, wh, centred)
		x = max(min(x, work.Right-ww), work.Left)
		y = max(min(y, work.Bottom-wh), work.Top)
	}
	w.resizing = true
	w32.SetWindowPos(w.hwnd, x, y, ww, wh, w32.SWP_NOZORDER|w32.SWP_NOACTIVATE)
	w.resizing = false
}

// resizedOrigin is the top-left corner of r resized to ww × wh: r's own, or (centred) the one
// that keeps r's centre.
func resizedOrigin(r w32.Rect, ww, wh int32, centred bool) (x, y int32) {
	if !centred {
		return r.Left, r.Top
	}
	return r.Left + (r.Right-r.Left-ww)/2, r.Top + (r.Bottom-r.Top-wh)/2
}

// SetFrameColors implements kit.Host.
func (w *Window) SetFrameColors(c kit.FrameColors) {
	if w.closed {
		return
	}
	w.colors = c
	if c.Background != w.bgColor {
		if w.bgBrush != 0 {
			w32.DeleteObject(w.bgBrush)
			w.bgBrush = 0
		}
		w.bgColor = c.Background
		if c.Background.A > 0 {
			w.bgBrush = w32.CreateSolidBrush(colorRef(c.Background))
		}
	}
	w.applyFrameColors()
}

func (w *Window) applyFrameColors() {
	c := w.colors
	dark := uint32(0)
	if c.Dark {
		dark = 1
	}
	w.setDWM(w32.DWMWA_USE_IMMERSIVE_DARK_MODE, dark)
	ref := func(col kit.Color) uint32 {
		if col.A > 0 {
			return colorRef(col)
		}
		return w32.DWMWA_COLOR_DEFAULT
	}
	text, border := c.Text, c.Border
	if !w.active {
		text, border = c.InactiveText, c.InactiveBorder
	}
	w.setDWM(w32.DWMWA_CAPTION_COLOR, ref(c.Caption))
	w.setDWM(w32.DWMWA_TEXT_COLOR, ref(text))
	w.setDWM(w32.DWMWA_BORDER_COLOR, ref(border))
}

// setDWM sets a 4-byte DWM attribute of the window (DWM's colour attributes need Windows 11
// and fail harmlessly before it).
func (w *Window) setDWM(attr, v uint32) {
	w.dwmValue = v
	w32.DwmSetWindowAttribute(w.hwnd, attr, unsafe.Pointer(&w.dwmValue), 4)
}

// colorRef is c over black as a Win32 COLORREF (0x00BBGGRR).
func colorRef(c kit.Color) uint32 {
	b := func(v float32) uint32 { return uint32(min(max(v*c.A, 0), 1)*255 + 0.5) }
	return b(c.R) | b(c.G)<<8 | b(c.B)<<16
}

// Command implements kit.Host.
func (w *Window) Command(c kit.WindowCommand) {
	if w.closed {
		return
	}
	switch c {
	case kit.WindowMinimize:
		w32.PostMessage(w.hwnd, w32.WM_SYSCOMMAND, w32.SC_MINIMIZE, 0)
	case kit.WindowMaximize:
		w32.PostMessage(w.hwnd, w32.WM_SYSCOMMAND, w32.SC_MAXIMIZE, 0)
	case kit.WindowRestore:
		w32.PostMessage(w.hwnd, w32.WM_SYSCOMMAND, w32.SC_RESTORE, 0)
	case kit.WindowClose:
		w32.PostMessage(w.hwnd, w32.WM_CLOSE, ownCloseMark, 0) // the window's own close (its title bar's button)
	}
}

// ownCloseMark is the wParam of the WM_CLOSE the window posts for its own close
// (Command(WindowClose)): WM_CLOSE has no parameters, so another program's has 0 there.
const ownCloseMark = 0x44584F43 // "DXOC"

// ownClose runs send, which sends the window its own WM_CLOSE (closeAsked lasts that long).
func (w *Window) ownClose(send func()) {
	w.closeAsked = true
	defer func() { w.closeAsked = false }()
	send()
}

// CloseNow closes the window before it returns, for the end of the user's session (when
// nothing posted runs any more): as its own close does, the root's CloseEvent first (which
// may store what it shows), and then the window is destroyed whatever the root answered.
// Inside a call into the root (a modal loop: the session ends while one of its dialogs is
// open) the root cannot be asked, nor the window destroyed under the call: CloseNow leaves
// it open. It reports whether the window is closed. It is for the UI thread.
func (w *Window) CloseNow() bool {
	if w.closed || w.hwnd == 0 {
		return true
	}
	if w.depth > 0 {
		return false
	}
	w.ownClose(func() { w32.SendMessage(w.hwnd, w32.WM_CLOSE, 0, 0) })
	if !w.closed && w.depth == 0 {
		w.destroy() // the root kept it open: the session ends all the same
	}
	return w.closed
}

// Discard destroys the window at once, without asking its root, whatever was in progress:
// for a window whose opening failed (a panic unwound the calls into its root, which left
// them counted). It is for the UI thread.
func (w *Window) Discard() {
	if w.closed || w.hwnd == 0 {
		return
	}
	w.recovered() // no call is in progress any more: none can be under this one
	w.closing = false
	w.destroy()
}

// Closed reports that the window has been destroyed.
func (w *Window) Closed() bool { return w.closed }

// Close implements kit.Host.
func (w *Window) Close() {
	if w.closed {
		return
	}
	if w.depth > 0 {
		w.closing = true
		return
	}
	w.destroy()
}

// Post implements kit.Host.
func (w *Window) Post(f func()) error {
	if f == nil {
		return errInvalid("Post of a nil function")
	}
	w.postMu.Lock()
	if w.postClosed {
		w.postMu.Unlock()
		return fmt.Errorf("win: window closed: %w", kit.ErrClosed)
	}
	at := len(w.posts)
	w.posts = append(w.posts, f)
	wake := !w.postWoken
	w.postWoken = true
	h := w.hwnd
	w.postMu.Unlock()
	if !wake {
		return nil
	}
	if err := w32.PostMessage(h, msgPost, 0, 0); err != nil {
		// No wake-up is on its way (the thread's queue is full): f is taken back, so that
		// the error means it will not run, and the next Post tries to wake the window
		// again (the functions posted meanwhile run then).
		w.postMu.Lock()
		if at < len(w.posts) {
			w.posts = append(w.posts[:at], w.posts[at+1:]...)
		}
		w.postWoken = false
		w.postMu.Unlock()
		return fmt.Errorf("win: %w", err)
	}
	return nil
}

// runPosts moves the posted functions into the queue of calls into the root, which runs
// them one at a time, never inside another call: a posted function that enters a modal loop
// (a message box, a menu, a file dialog) lets that loop dispatch msgPost, and what was posted
// meanwhile then waits until the function has returned.
func (w *Window) runPosts() {
	w.postMu.Lock()
	take := w.posts
	w.posts, w.spare = w.spare[:0], nil
	w.postWoken = false
	w.postMu.Unlock()
	if !w.closed {
		for _, f := range take {
			w.queue = append(w.queue, queued{f: f})
		}
	}
	clear(take)
	w.postMu.Lock()
	w.spare = take[:0]
	w.postMu.Unlock()
	w.drain()
}

// NotifySemantics implements kit.Host: the kit's focus, structure, property, live-region and
// announcement events go to UI Automation's clients (uia_window.go; nothing while no client
// has read the window).
func (w *Window) NotifySemantics(e kit.SemanticEvent) { w.uiaNotify(e) }

var (
	_ kit.RendererInfo = (*Window)(nil)
	_ kit.NativeFields = (*Window)(nil)
)

// ---- helpers for the App ----

var cursorIDs = [...]uintptr{
	kit.CursorDefault:    w32.IDC_ARROW,
	kit.CursorPointer:    w32.IDC_HAND,
	kit.CursorText:       w32.IDC_IBEAM,
	kit.CursorColResize:  w32.IDC_SIZEWE,
	kit.CursorRowResize:  w32.IDC_SIZENS,
	kit.CursorGrab:       w32.IDC_HAND,
	kit.CursorGrabbing:   w32.IDC_SIZEALL,
	kit.CursorMove:       w32.IDC_SIZEALL,
	kit.CursorNotAllowed: w32.IDC_NO,
	kit.CursorWait:       w32.IDC_WAIT,
	kit.CursorProgress:   w32.IDC_APPSTARTING,
}

// cursor is the system cursor of c (the arrow for an unknown one).
func (a *App) cursor(c kit.Cursor) windows.Handle {
	if int(c) >= len(cursorIDs) {
		c = kit.CursorDefault
	}
	if a.cursors[c] == 0 {
		a.cursors[c] = w32.LoadCursor(cursorIDs[c])
	}
	return a.cursors[c]
}

var _ kit.Host = (*Window)(nil)

// utf16Decode joins a surrogate pair from WM_CHAR.
func utf16Decode(hi, lo uint16) rune { return utf16.DecodeRune(rune(hi), rune(lo)) }
