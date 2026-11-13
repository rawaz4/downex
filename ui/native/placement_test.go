//go:build windows

package main

import (
	"math"
	"runtime"
	"testing"
	"time"

	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/win"
)

var procGetForegroundWindow = user32.NewProc("GetForegroundWindow")

// The main window's minimum client size (App.svelte: 900 × 600).
const minWidth, minHeight = host.MainMinWidth, host.MainMinHeight

func foreground() uintptr {
	h, _, _ := procGetForegroundWindow.Call()
	return h
}

// step is one stage of a scripted run of real windows: it does its action once, then the
// loop waits until its condition holds.
type step struct {
	name string
	do   func()
	done func() bool
}

// script runs the steps on the App's loop, checking every 10 ms, and ends the loop when they
// are done or after timeout. A last step named "closed" closes the window: the loop then ends
// by itself, as the window is destroyed.
func script(t *testing.T, a *win.App, kw *kit.Window, timeout time.Duration, steps []step) {
	t.Helper()
	i, started := 0, false
	var failed string
	stop := make(chan struct{})
	go func() {
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		deadline := time.Now().Add(timeout)
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			late := time.Now().After(deadline)
			if kw.Post(func() {
				for i < len(steps) {
					s := steps[i]
					if !started {
						started = true
						if s.do != nil {
							s.do()
						}
					}
					if s.done != nil && !s.done() {
						if late {
							failed = s.name
							a.Quit()
						}
						return
					}
					i, started = i+1, false
				}
				a.Quit()
			}) != nil {
				return
			}
		}
	}()
	if err := a.Run(); err != nil {
		t.Fatal(err)
	}
	close(stop)
	if i == len(steps)-1 && started && steps[i].name == "closed" {
		i++
	}
	if failed != "" {
		t.Fatalf("timed out at %q", failed)
	}
	if i < len(steps) {
		t.Fatalf("stopped at step %d of %d", i, len(steps))
	}
}

func near(a, b kit.Size) bool {
	return math.Abs(float64(a.W-b.W)) <= 0.5 && math.Abs(float64(a.H-b.H)) <= 0.5
}

// Window placement on real windows (the main window's frame, at this PC's DPI): GetWindowPlacement
// gives the normal size while the window is normal, maximised, minimised from maximised and
// restored; a window made with NewMaximizedWindow is maximised from its first frame, restores to
// its size, and a size larger than the work area is fitted into it. The windows never take
// the focus.
func TestWindowPlacement(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	a, err := win.NewApp(win.AppOptions{ClassName: "DownexNativePlacementTest"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	fg := foreground()
	open := func(opts win.WindowOptions, maximized bool) (*kit.Window, *win.Window) {
		kw := kit.NewWindow(&kit.Element{}, kit.WindowOptions{Title: "placement test"})
		opts.Title, opts.MinSize, opts.CustomFrame, opts.NoActivate = "placement test", kit.Size{W: minWidth, H: minHeight}, true, true
		newWindow := a.NewWindow
		if maximized {
			newWindow = a.NewMaximizedWindow
		}
		w, err := newWindow(kw, opts)
		if err != nil {
			t.Fatal(err)
		}
		return kw, w
	}
	placement := func(w *win.Window) win.Placement {
		p, err := w.Placement()
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	check := func(w *win.Window, what string, want win.Placement) func() bool {
		return func() bool {
			p := placement(w)
			if p.Maximized != want.Maximized || p.Minimized != want.Minimized {
				return false // not there yet
			}
			if !near(p.Normal, want.Normal) {
				t.Errorf("%s: normal size %v, want %v", what, p.Normal, want.Normal)
			}
			return true
		}
	}

	// A normal window, maximised, minimised from maximised, and restored twice.
	size := kit.Size{W: 1000, H: 650}
	kw, w := open(win.WindowOptions{Size: size}, false)
	var maxSize kit.Size
	script(t, a, kw, 5*time.Second, []step{
		{name: "normal", done: check(w, "normal", win.Placement{Normal: size})},
		{name: "maximised", do: func() { w.Command(kit.WindowMaximize) }, done: check(w, "maximised", win.Placement{Normal: size, Maximized: true})},
		{name: "maximised size", done: func() bool {
			maxSize = w.Size()
			return maxSize.W > size.W && maxSize.H > size.H
		}},
		{name: "minimised from maximised", do: func() { w.Command(kit.WindowMinimize) },
			done: check(w, "minimised", win.Placement{Normal: size, Maximized: true, Minimized: true})},
		{name: "restored to maximised", do: func() { w.Command(kit.WindowRestore) }, done: check(w, "restored", win.Placement{Normal: size, Maximized: true})},
		{name: "restored", do: func() { w.Command(kit.WindowRestore) }, done: check(w, "normal again", win.Placement{Normal: size})},
		{name: "normal size", done: func() bool { return near(w.Size(), size) }},
		{name: "minimised", do: func() { w.Command(kit.WindowMinimize) }, done: check(w, "minimised", win.Placement{Normal: size, Minimized: true})},
		{name: "closed", do: func() { w.Command(kit.WindowClose) }, done: func() bool { return false }},
	})
	if _, err := w.Placement(); err == nil {
		t.Error("a closed window has a placement")
	}

	// A window that opens maximised: maximised before its first frame, its client area the
	// work area (the size the maximised window above had), restoring to its size.
	size = kit.Size{W: 1100, H: 700}
	var states []kit.WindowState
	kw = kit.NewWindow(&kit.Element{}, kit.WindowOptions{Title: "placement test", OnWindowState: func(s kit.WindowState) { states = append(states, s) }})
	w, err = a.NewMaximizedWindow(kw, win.WindowOptions{Title: "placement test", Size: size, MinSize: kit.Size{W: minWidth, H: minHeight},
		CustomFrame: true, NoActivate: true})
	if err != nil {
		t.Fatal(err)
	}
	if !w.WindowState().Maximized || !kw.WindowState().Maximized {
		t.Errorf("NewMaximizedWindow: the window opened %+v (the root sees %+v)", w.WindowState(), kw.WindowState())
	}
	if !near(w.Size(), maxSize) {
		t.Errorf("NewMaximizedWindow: client %v, a maximised window's is %v", w.Size(), maxSize)
	}
	if err := w.HostErr(); err != nil {
		t.Error(err)
	}
	if p := placement(w); !p.Maximized || !near(p.Normal, size) {
		t.Errorf("NewMaximizedWindow: placement %+v, want maximised restoring to %v", p, size)
	}
	script(t, a, kw, 5*time.Second, []step{
		{name: "restored", do: func() { w.Command(kit.WindowRestore) }, done: check(w, "restored", win.Placement{Normal: size})},
		{name: "restored size", done: func() bool { return near(w.Size(), size) }},
		{name: "closed", do: func() { w.Command(kit.WindowClose) }, done: func() bool { return false }},
	})
	t.Logf("maximised client %v; the opened-maximised window's states %+v", maxSize, states)

	// A stored size larger than the work area is fitted into it, maximised or not.
	for _, maxed := range []bool{false, true} {
		kw, w := open(win.WindowOptions{Size: kit.Size{W: 16000, H: 16000}}, maxed)
		p := placement(w)
		if p.Normal.W > maxSize.W+0.5 || p.Normal.H > maxSize.H+0.5 || p.Normal.W < minWidth {
			t.Errorf("maximised %v: a 16000 × 16000 window restores to %v, the work area is %v", maxed, p.Normal, maxSize)
		}
		script(t, a, kw, 5*time.Second, []step{{name: "closed", do: func() { w.Command(kit.WindowClose) }, done: func() bool { return false }}})
		t.Logf("maximised %v: a 16000 × 16000 window restores to %v", maxed, p.Normal)
	}
	if got := foreground(); got != fg {
		t.Errorf("the foreground window changed from %#x to %#x", fg, got)
	}
}
