//go:build darwin && cgo

package axcheck

import (
	"runtime"
	"testing"
	"time"
	"weak"

	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/mac"
)

// gone collects garbage until p is cleared, or reports false after a while.
func gone(p weak.Pointer[mac.Window]) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		if p.Value() == nil {
			return true
		}
		time.Sleep(50 * time.Millisecond) // the main loop runs the posts the close left
	}
	return false
}

// TestClosedWindowsCollected: a closed window is garbage once nobody else holds it, read by
// a client or not, with a native field placed in it, or after a popup of it was read and
// closed; and so is every window after it (the round-2 finding: a weak pointer to a closed
// mac.Window never cleared; App.progCalls kept the last window called into in its backing
// array, so every closed window but the first stayed reachable).
func TestClosedWindowsCollected(t *testing.T) {
	for _, c := range []struct {
		name string
		read func(t *testing.T, w *mac.Window)
	}{
		{"unread", nil},
		{"read", func(t *testing.T, w *mac.Window) {
			win := w.AXWindow()
			mac.AXDump(win, 6)
			win.Release()
		}},
		{"read with a field", func(t *testing.T, w *mac.Window) {
			win := w.AXWindow()
			defer win.Release()
			node := find(win, 4, func(e *mac.AXElement) bool { return e.String("AXIdentifier") == "notes" })
			if node == nil {
				fatalf(t, "no notes node:\n%s", mac.AXDump(win, 4))
			}
			fr, _ := node.Attribute("AXFrame").(kit.Rect)
			addTextArea(win.Object(), fr)
			mac.AXDump(win, 6)
		}},
		{"unread again", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			var wp weak.Pointer[mac.Window]
			onMain(t, func() {
				p := newParityPage()
				w, err := app.NewWindow(p.win, mac.WindowOptions{Title: "collected", Size: kit.Size{W: 300, H: 200}, NoActivate: true})
				if err != nil {
					fatal(t, err)
				}
				wp = weak.Make(w)
				if c.read != nil {
					c.read(t, w)
				}
				w.Close()
			})
			if !gone(wp) {
				t.Error("the closed window is still reachable")
			}
		})
	}
	t.Run("read with a popup", func(t *testing.T) {
		var wp weak.Pointer[mac.Window]
		p := newPopupPage()
		onMain(t, func() {
			w, err := app.NewWindow(p.win, mac.WindowOptions{Title: popupTitle, Size: kit.Size{W: 420, H: 320}, NoActivate: true})
			if err != nil {
				fatal(t, err)
			}
			wp = weak.Make(w)
			win := w.AXWindow()
			mac.AXDump(win, 4)
			win.Release()
			p.sel.Open(widgets.FocusFirst)
		})
		eventually(t, "the list to open", func() bool { return p.sel.IsOpen() && p.sel.List() != nil })
		onMain(t, func() {
			if f := axuiFocused(t); f != nil {
				f.release()
			}
			wp.Value().Close() // closes the list with it
		})
		p = nil
		if !gone(wp) {
			t.Error("the closed window is still reachable")
		}
	})
}
