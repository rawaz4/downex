//go:build windows

package win

import (
	"sync"

	"downex/ui/win/internal/w32"

	"golang.org/x/sys/windows"
)

// autoHideCache knows, per monitor, on which edges an auto-hide taskbar waits, for the
// custom frame's maximised client area (frame.go). Asking the taskbar is a synchronous call
// into Explorer (SHAppBarMessage), which blocks while Explorer hangs or restarts and lets
// sent messages re-enter the window procedure; so WM_NCCALCSIZE only reads this cache, and
// the taskbar is asked on a goroutine of its own. A monitor not known yet has no inset until
// its answer comes; an answer that changes what is known makes the App frame its maximised
// windows again (notify).
type autoHideCache struct {
	read   func(mon w32.Rect) [4]bool // asks the taskbar (readAutoHide; tests replace it)
	notify func()                     // called on the reading goroutine after a change

	mu      sync.Mutex
	known   map[windows.Handle]autoHideEntry
	reading map[windows.Handle]bool
}

type autoHideEntry struct {
	mon   w32.Rect // the monitor's rectangle when it was read: a new one is read again
	edges [4]bool  // in ABE_ order
}

func newAutoHideCache(read func(w32.Rect) [4]bool, notify func()) *autoHideCache {
	return &autoHideCache{read: read, notify: notify, known: map[windows.Handle]autoHideEntry{}, reading: map[windows.Handle]bool{}}
}

// edges is what is known of monitor m (whose rectangle is rc); ok is false while it is
// being read.
func (c *autoHideCache) edges(m windows.Handle, rc w32.Rect) (edges [4]bool, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.known[m]; ok && e.mon == rc {
		return e.edges, true
	}
	c.startLocked(m, rc)
	return [4]bool{}, false
}

// refresh reads every known monitor again (the taskbar's settings may have changed).
func (c *autoHideCache) refresh() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for m, e := range c.known {
		c.startLocked(m, e.mon)
	}
}

func (c *autoHideCache) startLocked(m windows.Handle, rc w32.Rect) {
	if c.reading[m] {
		return
	}
	c.reading[m] = true
	go func() {
		edges := c.read(rc)
		c.mu.Lock()
		old, had := c.known[m]
		c.known[m] = autoHideEntry{mon: rc, edges: edges}
		delete(c.reading, m)
		c.mu.Unlock()
		// A monitor seen for the first time changes the frame only if it has a bar.
		if (had && old.edges != edges) || (!had && edges != [4]bool{}) {
			c.notify()
		}
	}()
}

// readAutoHide asks the taskbar on which edges of the monitor whose rectangle is mon an
// auto-hide bar waits. It runs on a goroutine of its own, with its own heap memory for the
// calls.
func readAutoHide(mon w32.Rect) [4]bool {
	var edges [4]bool
	d := new(w32.APPBARDATA)
	if w32.SHAppBarMessage(w32.ABM_GETSTATE, d)&w32.ABS_AUTOHIDE == 0 {
		return edges
	}
	for edge := uint32(w32.ABE_LEFT); edge <= w32.ABE_BOTTOM; edge++ {
		*d = w32.APPBARDATA{Edge: edge, Rc: mon}
		edges[edge] = w32.SHAppBarMessage(w32.ABM_GETAUTOHIDEBAREX, d) != 0
	}
	return edges
}

// notifyAutoHide tells the UI thread that what is known of the auto-hide bars changed.
func (a *App) notifyAutoHide() { a.post(msgAutoHide) }

// autoHideChanged frames the maximised custom-framed windows again with what is known now.
func (a *App) autoHideChanged() {
	for _, w := range a.windows {
		if w.custom && w32.IsZoomed(w.hwnd) {
			w32.SetWindowPos(w.hwnd, 0, 0, 0, 0, w32.SWP_NOMOVE|w32.SWP_NOSIZE|w32.SWP_NOZORDER|w32.SWP_NOACTIVATE|w32.SWP_FRAMECHANGED)
		}
	}
}
