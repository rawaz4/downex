//go:build windows

package win

import (
	"syscall"

	"downex/ui/kit"
	"downex/ui/kit/hostcap"
	"downex/ui/win/internal/com"
	"downex/ui/win/internal/w32"

	"golang.org/x/sys/windows"
)

// hookSet is an App's thread hooks while popups are open or tooltips are on (hostcap's
// dismissal and the system's tooltips both need what happens anywhere in the App's windows):
// WH_MOUSE sees every mouse message the thread takes from its queue, before it is
// dispatched, so a press outside a popup dismisses it first (as a document's capture-phase
// pointerdown listener does) and then goes on to its window; WH_CALLWNDPROC sees what is
// sent to an owner window (losing the activation, moving, closing); WH_KEYBOARD sees keys (a
// key hides a tooltip). The hooks are the thread's own (SetWindowsHookEx with the thread's
// id): nothing outside the process sees them. They are made at the first user and removed
// with the last.
type hookSet struct {
	app                  *App
	refs                 int
	mouse, cwp, keyboard uintptr
}

var (
	hooksOf                              = map[*App]*hookSet{}
	hookProcOnce                         syscallOnce
	mouseProcPtr, cwpProcPtr, keyProcPtr uintptr
)

// syscallOnce makes the hook callbacks once (a callback is never freed).
type syscallOnce struct{ done bool }

func (o *syscallOnce) do(f func()) {
	if !o.done {
		o.done = true
		f()
	}
}

// hooks is the App's hook set.
func (a *App) hooks() *hookSet {
	h := hooksOf[a]
	if h == nil {
		h = &hookSet{app: a}
		hooksOf[a] = h
	}
	return h
}

func (h *hookSet) retain() {
	h.refs++
	if h.refs > 1 {
		return
	}
	hookProcOnce.do(func() {
		mouseProcPtr = syscall.NewCallback(mouseHook)
		cwpProcPtr = syscall.NewCallback(cwpHook)
		keyProcPtr = syscall.NewCallback(keyHook)
	})
	th := windows.GetCurrentThreadId()
	h.mouse = setWindowsHookEx(popWHMouse, mouseProcPtr, th)
	h.cwp = setWindowsHookEx(popWHCallWndProc, cwpProcPtr, th)
	h.keyboard = setWindowsHookEx(popWHKeyboard, keyProcPtr, th)
}

func (h *hookSet) release() {
	if h.refs == 0 {
		return
	}
	h.refs--
	if h.refs > 0 {
		return
	}
	for _, k := range []*uintptr{&h.mouse, &h.cwp, &h.keyboard} {
		if *k != 0 {
			unhookWindowsHookEx(*k)
			*k = 0
		}
	}
}

// appOfThread is the App that runs on the calling thread (the hooks run on it).
func appOfThread() *App {
	th := windows.GetCurrentThreadId()
	for a := range hooksOf {
		if a.thread == th && !a.closed {
			return a
		}
	}
	return nil
}

// ownerOf is the App's Window whose window (or a field in it) h is, or nil.
func (a *App) ownerOf(h w32.HWND) *Window {
	if w := a.windows[h]; w != nil {
		return w
	}
	if f := fieldByHWND(h); f != nil && f.w.app == a {
		return f.w
	}
	if top := w32.GetAncestor(h, 2); top != 0 { // GA_ROOT
		return a.windows[top]
	}
	return nil
}

// isPopup reports whether h is one of the App's popups.
func isPopup(h w32.HWND) *popupWindow {
	popupTable.Lock()
	defer popupTable.Unlock()
	return popupTable.byHWND[h]
}

func mouseHook(code, wp, lp uintptr) uintptr {
	if int32(code) == popHCAction {
		if a := appOfThread(); a != nil {
			a.onMouse(uint32(wp), (*popMouseHook)(com.Addr(lp)))
		}
	}
	return callNextHookEx(int32(code), wp, lp)
}

func cwpHook(code, wp, lp uintptr) uintptr {
	if int32(code) == popHCAction {
		if a := appOfThread(); a != nil {
			a.onSent((*popCWP)(com.Addr(lp)))
		}
	}
	return callNextHookEx(int32(code), wp, lp)
}

func keyHook(code, wp, lp uintptr) uintptr {
	if int32(code) == popHCAction && lp&(1<<31) == 0 { // a key going down
		if a := appOfThread(); a != nil {
			a.tips().hideAll()
		}
	}
	return callNextHookEx(int32(code), wp, lp)
}

// onMouse is the mouse hook: presses and the wheel outside popups dismiss them; moves drive the
// tooltips.
func (a *App) onMouse(msg uint32, m *popMouseHook) {
	switch msg {
	case popWMLButtonDown, popWMRButtonDown, popWMMButtonDown, popWMXButtonDown,
		popWMNCLButtonDown, popWMNCRButtonDown, popWMNCMButtonDown, popWMNCXButtonDown:
		a.tips().hideAll()
		a.dismissOutside(m, hostcap.DismissPress)
	case popWMMouseWheel, popWMMouseHWheel:
		a.tips().hideAll()
		a.dismissOutside(m, hostcap.DismissWheel)
	case popWMMouseMove:
		a.tips().moved(m)
	}
}

// dismissOutside tells every open popup that is not under the event's point about it.
func (a *App) dismissOutside(m *popMouseHook, reason hostcap.DismissReason) {
	var hit *popupWindow
	if p := isPopup(m.Hwnd); p != nil && p.hitContains(m.Pt) {
		hit = p
	}
	for owner, list := range popupsOf {
		if owner.app != a {
			continue
		}
		for _, p := range append([]*popupWindow(nil), list...) {
			if p == hit || p.closed {
				continue
			}
			d := hostcap.Dismiss{Reason: reason}
			if reason == hostcap.DismissPress {
				c := w32.ScreenToClient(owner.hwnd, m.Pt)
				ppd := owner.ppd()
				d.Pos = kit.Pt(float32(c.X)/ppd, float32(c.Y)/ppd)
				o := a.ownerOf(m.Hwnd)
				d.Inside = o == owner && m.HitTest == popHTClient
			}
			if reason == hostcap.DismissWheel && hit != nil {
				continue // the wheel over a popup scrolls it
			}
			p.dismiss(d)
		}
	}
}

// onSent is the call-window-procedure hook: an owner losing the activation, moving or
// closing dismisses its popups (and its tooltip).
func (a *App) onSent(c *popCWP) {
	w := a.windows[c.Hwnd]
	if w == nil {
		return
	}
	var reason hostcap.DismissReason
	switch c.Message {
	case popWMActivate:
		if c.WParam&0xFFFF == popWAInactive {
			reason = hostcap.DismissDeactivate
		}
	case popWMActivateApp:
		if c.WParam == 0 {
			reason = hostcap.DismissDeactivate
		}
	case popWMWindowPosChanged:
		if wp := (*popWindowPos)(com.Addr(c.LParam)); wp.Flags&(popSWPNoMove|popSWPNoSize) != popSWPNoMove|popSWPNoSize {
			reason = hostcap.DismissMove
		}
	case popWMDestroy:
		reason = hostcap.DismissClosed
	}
	if reason == 0 {
		return
	}
	a.tips().hide(w)
	for _, p := range append([]*popupWindow(nil), popupsOf[w]...) {
		p.dismiss(hostcap.Dismiss{Reason: reason})
		if reason == hostcap.DismissClosed {
			p.Close()
		}
	}
	if reason == hostcap.DismissClosed {
		// The window goes: its tooltip state goes with it (it holds the *Window), and so
		// does its hold on the thread's hooks, which come off with the last holder. A window
		// with tooltips holds the hooks, so this hook always sees its WM_DESTROY.
		w.SetTooltipSource(nil)
	}
	if reason == hostcap.DismissDeactivate {
		a.areasLostActivation(w)
	}
}
