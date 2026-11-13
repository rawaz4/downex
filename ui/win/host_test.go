//go:build windows

package win

import (
	"slices"
	"syscall"
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/win/internal/w32"
)

// openHostApp makes an App with the host's hooks on the test's locked thread.
func openHostApp(t *testing.T, o AppOptions) *App {
	t.Helper()
	runtimeLock(t)
	a, err := NewApp(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}

// hostWindow opens a window of root on a, without taking the focus.
func hostWindow(t *testing.T, a *App, root *testRoot, o WindowOptions) *Window {
	t.Helper()
	root.t = t
	mon := w32.MonitorFromPoint(w32.Point{}, w32.MONITOR_DEFAULTTOPRIMARY)
	mi, _ := w32.GetMonitorInfo(mon)
	o.At, o.Placed = w32.Point{X: mi.Work.Left + 40, Y: mi.Work.Top + 40}, true
	o.Size, o.NoActivate, o.Software = kit.Size{W: 240, H: 160}, true, true
	w, err := a.NewWindow(root, o)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// runUntil runs a's loop; every 10 ms step runs on the UI thread until it reports done, and
// the test fails if Run has not returned after timeout.
func runUntil(t *testing.T, a *App, timeout time.Duration, step func() bool) {
	t.Helper()
	stop := make(chan struct{})
	defer close(stop)
	var timedOut, done bool // the UI thread's
	go func() {
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		deadline := time.After(timeout)
		for {
			select {
			case <-stop:
				return
			case <-deadline:
				_ = a.Post(func() {
					timedOut = true
					for _, w := range a.windows {
						w.destroy()
					}
				})
				return
			case <-tick.C:
				_ = a.Post(func() { done = done || step() })
			}
		}
	}()
	if err := a.Run(); err != nil {
		t.Fatal(err)
	}
	if timedOut {
		t.Fatalf("the loop still ran after %v", timeout)
	}
}

// A panic in one window's procedure goes to OnPanic with that window, and Run goes on with
// the others (a host closes the failed one); OnWindowClosed hears each window once it is
// destroyed; App.Post runs on the UI thread from any goroutine; WM_ENDSESSION reaches
// OnEndSession once; CloseNow closes a window through its root's CloseEvent before it
// returns. Run returns with the last window.
func TestHostHooks(t *testing.T) {
	var panics, closed []*Window
	ends := 0
	a := openHostApp(t, AppOptions{
		OnPanic: func(w *Window, err error) {
			panics = append(panics, w)
			if w != nil {
				w.Close()
			}
		},
		OnWindowClosed: func(w *Window) { closed = append(closed, w) },
		OnEndSession:   func() { ends++ },
	})
	r1, r2 := &testRoot{}, &testRoot{}
	w1, w2 := hostWindow(t, a, r1, WindowOptions{}), hostWindow(t, a, r2, WindowOptions{})
	r1.onEvent = func(e kit.Event) bool {
		if _, ok := e.(kit.TextEvent); ok {
			panic("one window's bug")
		}
		return false
	}
	w32.PostMessage(w1.hwnd, w32.WM_CHAR, 'z', 0)
	posted := make(chan bool, 1)
	go func() { _ = a.Post(func() { posted <- true }) }()
	var ran bool
	runUntil(t, a, 10*time.Second, func() bool {
		select {
		case <-posted:
			ran = true
		default:
		}
		if !ran || len(closed) == 0 {
			return false
		}
		send(w2, w32.WM_ENDSESSION, 0, 0) // the session goes on: nothing
		send(w2, w32.WM_ENDSESSION, 1, 0)
		send(w2, w32.WM_ENDSESSION, 1, 0)
		w2.CloseNow()
		if !w2.closed {
			t.Error("CloseNow returned with the window open")
		}
		return true
	})
	if !slices.Equal(panics, []*Window{w1}) || !slices.Equal(closed, []*Window{w1, w2}) || ends != 1 {
		t.Fatalf("panics %v closed %v ends %d; want [w1], [w1 w2], 1", panics, closed, ends)
	}
	if !slices.ContainsFunc(r2.events, func(e kit.Event) bool { _, ok := e.(kit.CloseEvent); return ok }) {
		t.Fatal("CloseNow did not ask the root (CloseEvent)")
	}
	if slices.ContainsFunc(r1.events, func(e kit.Event) bool { _, ok := e.(kit.CloseEvent); return ok }) {
		t.Fatal("the failed window was asked to close (its state is not to be stored)")
	}
	if err := a.Post(func() {}); err != nil {
		t.Fatalf("Post with no window open: %v", err)
	}
	a.Close()
	if err := a.Post(func() {}); err == nil {
		t.Fatal("Post after Close")
	}
}

// A WM_CLOSE that the window's own close (kit.WindowClose) or the system's (SC_CLOSE:
// Alt+F4, the system menu, the taskbar) asked for closes it; any other (another program's,
// such as an installer's CloseMainWindow) goes to OnForeignClose first, which may take it.
func TestForeignCloseIsToldFromTheUsersClose(t *testing.T) {
	a := openHostApp(t, AppOptions{})
	var foreign int
	take := true
	opts := WindowOptions{OnForeignClose: func() bool { foreign++; return take }}
	root := &testRoot{}
	w := hostWindow(t, a, root, opts)
	closes := func() int {
		return len(slices.DeleteFunc(slices.Clone(root.events), func(e kit.Event) bool { _, ok := e.(kit.CloseEvent); return !ok }))
	}
	root.onEvent = func(e kit.Event) bool { _, ok := e.(kit.CloseEvent); return ok } // the root keeps the window open
	send(w, w32.WM_CLOSE, 0, 0)
	if foreign != 1 || closes() != 0 {
		t.Fatalf("a foreign WM_CLOSE: told %d, root asked %d; want 1, 0", foreign, closes())
	}
	send(w, w32.WM_SYSCOMMAND, w32.SC_CLOSE, 0) // DefWindowProc sends the WM_CLOSE
	if foreign != 1 || closes() != 1 {
		t.Fatalf("SC_CLOSE: told %d, root asked %d; want 1, 1", foreign, closes())
	}
	w.Command(kit.WindowClose)
	pump(t, a, w, "the posted close", 5*time.Second, func() bool { return closes() == 2 })
	if foreign != 1 {
		t.Fatalf("the window's own close was told as foreign")
	}
	send(w, w32.WM_CLOSE, 0, 0) // foreign again: the flag of the user's close does not stay
	if foreign != 2 {
		t.Fatalf("a second foreign WM_CLOSE told %d times", foreign)
	}
	take = false // not taken: the window closes as any WM_CLOSE closes it
	root.onEvent = nil
	send(w, w32.WM_CLOSE, 0, 0)
	if foreign != 3 || !w.closed {
		t.Fatalf("a foreign WM_CLOSE not taken: told %d, closed %v", foreign, w.closed)
	}
}

// The listener's answers: an accepted kind answers what Answer says (taking the payload or
// not), AlsoKinds are accepted too, a payload beyond MaxPendingHandoffs gets BusyAnswer,
// Send returns the answer, and Drain hands the waiting payloads over at once.
func TestListenerAnswers(t *testing.T) {
	a, w := openTest(t, &testRoot{}, WindowOptions{Software: true})
	_ = a
	n := testNames()
	in, _, err := ClaimInstance(n, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	var got []string
	open := true
	if err := in.Listen(ListenOptions{Kind: 1542, AlsoKinds: []uintptr{1543}, Max: 64, BusyAnswer: 9,
		Answer: func(kind uintptr) (uintptr, bool) {
			if kind == 1543 && !open {
				return 7, false
			}
			return 100 + kind, true
		},
		OnData: func(b []byte) { got = append(got, string(b)) },
	}); err != nil {
		t.Fatal(err)
	}
	_, running, _ := ClaimInstance(n, time.Second)
	answers := make(chan uintptr, 4)
	sendAll := func(kind uintptr, payloads ...string) {
		done := make(chan struct{})
		go func() { // from another thread, as another process sends
			defer close(done)
			for _, p := range payloads {
				r, err := running.Send(kind, []byte(p), 5*time.Second)
				if err != nil {
					t.Error(err)
				}
				answers <- r
			}
		}()
		pump(t, a, w, "the sends", 10*time.Second, func() bool {
			select {
			case <-done:
				return true
			default:
				return false
			}
		})
	}
	sendAll(1543, "host")
	sendAll(1542, "today")
	sendAll(77, "other")
	if r := []uintptr{<-answers, <-answers, <-answers}; !slices.Equal(r, []uintptr{1643, 1642, 0}) {
		t.Fatalf("answers %v", r)
	}
	in.Drain()
	if !slices.Equal(got, []string{"host", "today"}) {
		t.Fatalf("drained %q", got)
	}
	open = false
	sendAll(1543, "refused")
	if r := <-answers; r != 7 {
		t.Fatalf("a refused kind answered %d", r)
	}
	// MaxPendingHandoffs taken and not handed on: the next one is BusyAnswer.
	got = nil
	for i := 0; i < MaxPendingHandoffs; i++ {
		in.l.pending = append(in.l.pending, []byte("x"))
	}
	sendAll(1542, "busy")
	if r := <-answers; r != 9 {
		t.Fatalf("a payload past the pending ones answered %d", r)
	}
	in.Drain()
	if len(got) != MaxPendingHandoffs {
		t.Fatalf("drained %d", len(got))
	}
}

// A window closed by a message another thread sends (handled inside GetMessage, which then
// waits for a posted message) is told to OnWindowClosed at once, not at the next message.
func TestASentCloseIsToldAtOnce(t *testing.T) {
	var closed []*Window
	var a *App
	a = openHostApp(t, AppOptions{OnWindowClosed: func(w *Window) {
		closed = append(closed, w)
		a.Quit()
	}})
	w1, w2 := hostWindow(t, a, &testRoot{}, WindowOptions{}), hostWindow(t, a, &testRoot{}, WindowOptions{})
	pump(t, a, w2, "the first frames", 5*time.Second, func() bool { return w1.painted && w2.painted })
	h := w1.hwnd
	go func() {
		time.Sleep(50 * time.Millisecond)
		syscall.SyscallN(procSendMessage.Addr(), uintptr(h), w32.WM_SYSCOMMAND, w32.SC_CLOSE, 0)
	}()
	late := time.AfterFunc(5*time.Second, func() { _ = a.Post(a.Quit) })
	defer late.Stop()
	start := time.Now()
	if err := a.Run(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(closed, []*Window{w1}) || time.Since(start) > 2*time.Second {
		t.Fatalf("closed %v after %v; want w1 at once", closed, time.Since(start))
	}
	w2.destroy()
}

// The window's own close posted (kit.WindowClose) and a foreign WM_CLOSE that arrives before
// it are told apart: the foreign one goes to OnForeignClose, the posted one closes the window
// as its own; and a system close (SC_CLOSE) leaves nothing behind for a later foreign one.
func TestAForeignCloseBeforeTheOwnOne(t *testing.T) {
	a := openHostApp(t, AppOptions{})
	var foreign int
	root := &testRoot{}
	w := hostWindow(t, a, root, WindowOptions{OnForeignClose: func() bool { foreign++; return true }})
	closes := func() int {
		return len(slices.DeleteFunc(slices.Clone(root.events), func(e kit.Event) bool { _, ok := e.(kit.CloseEvent); return !ok }))
	}
	root.onEvent = func(e kit.Event) bool { _, ok := e.(kit.CloseEvent); return ok } // the root keeps the window open
	// Posted: not handled yet when the foreign one comes.
	w.Command(kit.WindowClose)
	send(w, w32.WM_CLOSE, 0, 0)
	if foreign != 1 || closes() != 0 {
		t.Fatalf("a foreign WM_CLOSE ahead of the own one: told %d, root asked %d; want 1, 0", foreign, closes())
	}
	pump(t, a, w, "the posted own close", 5*time.Second, func() bool { return closes() == 1 })
	if foreign != 1 {
		t.Fatalf("the own close was told as foreign")
	}
	// The root's answer to SC_CLOSE's WM_CLOSE kept the window open: the next WM_CLOSE is foreign.
	send(w, w32.WM_SYSCOMMAND, w32.SC_CLOSE, 0)
	send(w, w32.WM_CLOSE, 0, 0)
	if foreign != 2 || closes() != 2 {
		t.Fatalf("after SC_CLOSE: told %d, root asked %d; want 2, 2", foreign, closes())
	}
	w.destroy()
}

// CloseNow asks the root (CloseEvent) and destroys the window before it returns, even when
// the root keeps it open; inside a call into the root it does neither and reports false.
func TestCloseNowClosesOrSaysItCannot(t *testing.T) {
	a := openHostApp(t, AppOptions{})
	r1, r2 := &testRoot{}, &testRoot{}
	keep := func(e kit.Event) bool { _, ok := e.(kit.CloseEvent); return ok }
	r1.onEvent, r2.onEvent = keep, keep
	w1, w2 := hostWindow(t, a, r1, WindowOptions{}), hostWindow(t, a, r2, WindowOptions{})
	var inCall bool
	w2.call(func() { inCall = w2.CloseNow() })
	if inCall || w2.closed || !isWindow(w2.hwnd) {
		t.Fatalf("CloseNow inside a call: %v, closed %v; want false and the window open", inCall, w2.closed)
	}
	if !w1.CloseNow() || !w1.closed || isWindow(w1.hwnd) {
		t.Fatal("CloseNow left a window whose root keeps it open")
	}
	if !slices.ContainsFunc(r1.events, func(e kit.Event) bool { _, ok := e.(kit.CloseEvent); return ok }) {
		t.Fatal("CloseNow did not ask the root first")
	}
	if !w1.CloseNow() {
		t.Fatal("CloseNow of a closed window")
	}
	w2.destroy()
}

// A window whose opening failed half way (a panic in its root's Attach, which left the call
// counted) is told to OnWindowMade before the root is attached, and Discard destroys it.
func TestAHalfMadeWindowIsKnownAndDiscarded(t *testing.T) {
	var made []*Window
	a := openHostApp(t, AppOptions{OnWindowMade: func(w *Window) { made = append(made, w) }})
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("no panic from the root's Attach")
			}
		}()
		_, _ = a.NewWindow(panicRoot{&testRoot{t: t}}, WindowOptions{Size: kit.Size{W: 200, H: 120}, NoActivate: true, Software: true})
	}()
	if len(made) != 1 || made[0].closed || !isWindow(made[0].hwnd) {
		t.Fatalf("made %v", made)
	}
	w := made[0]
	h := w.hwnd
	w.Discard()
	if !w.closed || isWindow(h) || len(a.windows) != 0 {
		t.Fatalf("Discard: closed %v, window %v, %d windows left", w.closed, isWindow(h), len(a.windows))
	}
}

// panicRoot panics in Attach.
type panicRoot struct{ *testRoot }

func (r panicRoot) Attach(kit.Host) { panic("a view's bug while the window opens") }

// A listener that Close is destroying answers ClosedAnswer and takes nothing; once it is
// gone the running window is no longer Alive and a send to it fails.
func TestAClosingListenerSaysSo(t *testing.T) {
	a, w := openTest(t, &testRoot{}, WindowOptions{Software: true})
	n := testNames()
	in, _, err := ClaimInstance(n, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	var got int
	if err := in.Listen(ListenOptions{Kind: 1543, Max: 64, ClosedAnswer: 5,
		Answer: func(uintptr) (uintptr, bool) { return 3, true },
		OnData: func([]byte) { got++ },
	}); err != nil {
		t.Fatal(err)
	}
	_, running, _ := ClaimInstance(n, time.Second)
	if !running.OK() || !running.Alive() {
		t.Fatal("the listener is not found alive")
	}
	sendOne := func() (uintptr, error) {
		type res struct {
			r   uintptr
			err error
		}
		ch := make(chan res, 1)
		go func() {
			r, err := running.Send(1543, []byte("x"), 2*time.Second)
			ch <- res{r, err}
		}()
		var out res
		pump(t, a, w, "the send", 10*time.Second, func() bool {
			select {
			case out = <-ch:
				return true
			default:
				return false
			}
		})
		return out.r, out.err
	}
	if r, err := sendOne(); r != 3 || err != nil {
		t.Fatalf("an open listener answered %d, %v", r, err)
	}
	in.Drain()
	in.l.closing = true // as while Close destroys it
	if r, err := sendOne(); r != 5 || err != nil {
		t.Fatalf("a closing listener answered %d, %v; want ClosedAnswer", r, err)
	}
	in.l.closing = false
	in.Drain()
	if got != 1 {
		t.Fatalf("%d payloads taken; want only the open listener's", got)
	}
	in.Close()
	if running.Alive() {
		t.Fatal("a closed listener is Alive")
	}
	if _, err := running.Send(1543, []byte("x"), time.Second); err == nil {
		t.Fatal("a send to a closed listener did not fail")
	}
}
