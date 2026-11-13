//go:build windows

package glue

import (
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/views"
	"downex/ui/win"

	"golang.org/x/sys/windows"
)

// These tests run a host in the test process itself, on the test's desktop, so that the race
// detector sees its goroutines and the test sees its windows.

// inprocHost starts a host under a test lock with args, its files in dir.
func inprocHost(t *testing.T, dir string, args ...string) (*Host, string) {
	t.Helper()
	onTestDesktop(t)
	lock := testLock()
	h, handed, err := Start(testConfig(lock, dir, args...))
	if err != nil || handed || h == nil {
		t.Fatalf("Start = %v, %v, %v; want a host", h, handed, err)
	}
	t.Cleanup(h.Close)
	return h, lock
}

// labelWindow opens a window showing text on a, not taking the focus.
func labelWindow(a *win.App, text string) (*win.Window, *kit.Window, error) {
	kw := kit.NewWindow(kit.NewLabel(text, views.FontBody, kit.TextOptions{}), kit.WindowOptions{Title: "Downex glue in-process test"})
	ww, err := a.NewWindow(kw, win.WindowOptions{
		Title: "Downex glue in-process test", Size: kit.Size{W: 320, H: 180}, Software: true, NoActivate: true,
	})
	return ww, kw, err
}

// OpenProgress is called from any goroutine (a window's session) while Run starts, runs and
// ends: the App it posts to is read race-free (the -race run checks it), it fails with
// ErrClosed once Run has returned, and the main lock with its hand-off window is let go before
// Done (a later start finds no host that would take a request and never show it).
func TestOpenProgressFromAnyGoroutine(t *testing.T) {
	h, lock := inprocHost(t, t.TempDir())
	stop, done := make(chan struct{}), make(chan struct{})
	var posted atomic.Int32
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if h.OpenProgress(9) == nil {
				posted.Add(1)
			}
			time.Sleep(time.Millisecond)
		}
	}()
	var lockFree, ranDone bool
	err := h.Run(RunOptions{
		App: win.AppOptions{ClassName: "DownexGlueTestInproc"},
		Open: func(a *win.App, w *Window) (*win.Window, error) {
			if w.View.IsProgress() {
				return nil, errors.New("no progress window in this test")
			}
			ww, kw, err := labelWindow(a, "main")
			if err != nil {
				return nil, err
			}
			time.AfterFunc(300*time.Millisecond, func() { _ = kw.Post(kw.Close) })
			return ww, nil
		},
		Done: func(*win.App) {
			ranDone = true
			in, running, err := win.ClaimInstance(win.InstanceNames(HandoffNames(lock, host.View{})), 0)
			lockFree = err == nil && !running.OK() && in.First()
			in.Close()
		},
	})
	time.Sleep(20 * time.Millisecond) // OpenProgress goes on after Run
	close(stop)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if !ranDone || !lockFree {
		t.Fatalf("Done ran %v; the main lock free there %v (want both)", ranDone, lockFree)
	}
	if posted.Load() == 0 {
		t.Fatal("no OpenProgress reached the running host")
	}
	if err := h.OpenProgress(9); !errors.Is(err, ErrClosed) {
		t.Fatalf("OpenProgress after Run: %v, want ErrClosed", err)
	}
}

// A panic in RunOptions.Open, after it made its win.Window and before Attach (or inside the
// window's making, in its root's Attach), closes that half-made window and lets go of its
// part; the host goes on with its other windows, and Run returns with the last one.
func TestAPanicWhileOpeningClosesThatWindow(t *testing.T) {
	for _, c := range []struct {
		name string
		open func(a *win.App) *win.Window
	}{
		{"after NewWindow", func(a *win.App) *win.Window {
			if _, _, err := labelWindow(a, "half made"); err != nil {
				panic(err)
			}
			panic("a view's bug before Attach")
		}},
		{"inside NewWindow", func(a *win.App) *win.Window {
			kw := kit.NewWindow(kit.NewLabel("half made", views.FontBody, kit.TextOptions{}), kit.WindowOptions{})
			_, _ = a.NewWindow(attachPanics{kw}, win.WindowOptions{Size: kit.Size{W: 320, H: 180}, Software: true, NoActivate: true})
			return nil
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, _ := inprocHost(t, t.TempDir())
			var made *win.Window
			var checked, ended bool
			var mainKW *kit.Window
			err := h.Run(RunOptions{
				App: win.AppOptions{ClassName: "DownexGlueTestInproc"},
				Open: func(a *win.App, w *Window) (*win.Window, error) {
					if w.View.IsProgress() {
						w.Defer(func() { ended = true })
						defer func() { made = w.win }() // the window the host knows of, as the panic leaves Open
						c.open(a)
						return nil, errors.New("not reached")
					}
					ww, kw, err := labelWindow(a, "main")
					if err != nil {
						return nil, err
					}
					mainKW = kw
					_ = a.Post(func() {
						h.Request([]string{"--progress", "6"})
						checked = true
						if made == nil || made == ww {
							t.Error("the progress window was never made")
						} else if !made.Closed() {
							t.Error("the half-made window stayed open")
							made.Discard()
						}
						if h.progress[6] != nil || len(h.windows) != 1 {
							t.Errorf("the failed window's part stayed: %d windows", len(h.windows))
						}
						_ = mainKW.Post(mainKW.Close)
					})
					return ww, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if !checked || !ended {
				t.Fatalf("checked %v, the failed window's defers ran %v", checked, ended)
			}
		})
	}
}

// attachPanics is a kit window whose Attach panics (a view's bug while its window is made).
type attachPanics struct{ *kit.Window }

func (attachPanics) Attach(kit.Host) { panic("a view's bug while the window is made") }

// At the end of the session (WM_ENDSESSION) a window inside a call into its views (a dialog
// is open: the call has not returned) is not closed under the call, and what it holds is not
// let go while its HWND is there; its prefs are written. The other windows close at once and
// let go of what they hold before the message returns.
func TestTheSessionEndsWithADialogOpen(t *testing.T) {
	dir := t.TempDir()
	h, _ := inprocHost(t, dir)
	var mainWin, progWin *win.Window
	var mainEnded, progEnded, checked bool
	var mainPrefs interface {
		Set(key, value string) error
		Path() string
	}
	err := h.Run(RunOptions{
		App: win.AppOptions{ClassName: "DownexGlueTestInproc"},
		Open: func(a *win.App, w *Window) (*win.Window, error) {
			pf, err := w.OpenPrefs()
			if err != nil {
				return nil, err
			}
			w.Defer(pf.Close)
			if w.View.IsProgress() {
				w.Defer(func() { progEnded = true })
				ww, _, err := labelWindow(a, "progress")
				progWin = ww
				return ww, err
			}
			w.Defer(func() { mainEnded = true })
			mainPrefs = pf
			ww, kw, err := labelWindow(a, "main")
			if err != nil {
				return nil, err
			}
			mainWin = ww
			_ = a.Post(func() {
				h.Request([]string{"--progress", "3"})
				// A call into the main window's views, as a dialog's modal loop is one.
				_ = kw.Post(func() {
					checked = true
					if err := mainPrefs.Set("glue-test", "kept"); err != nil {
						t.Error(err)
					}
					call(procSendMessage, uintptr(mainWin.HWND()), wmEndSession, 1, 0)
					if progWin == nil || !progWin.Closed() || !progEnded {
						t.Errorf("the progress window: closed %v, let go %v; want both", progWin != nil && progWin.Closed(), progEnded)
					}
					if mainWin.Closed() || mainEnded {
						t.Errorf("the window in a call: closed %v, let go %v; want neither", mainWin.Closed(), mainEnded)
					}
					b, err := os.ReadFile(mainPrefs.Path())
					if err != nil || !strings.Contains(string(b), `"glue-test"`) {
						t.Errorf("the prefs at the end of the session: %q, %v", b, err)
					}
					kw.Close()
				})
			})
			return ww, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !checked || !mainEnded {
		t.Fatalf("checked %v; the main window let go once closed %v", checked, mainEnded)
	}
}

// What a start makes of the running window's answer to its host hand-off: a host's answers,
// an older build's (0 or 1, or no answer in time) only from a window that is still there; a
// window that has gone meanwhile (a host's hand-off window being destroyed answers 0) is
// looked for again, never taken for an older build's.
func TestTheStartReadsTheAnswer(t *testing.T) {
	failed := errors.New("the window did not take the hand-off")
	for _, c := range []struct {
		answer uintptr
		err    error
		alive  bool
		want   int
	}{
		{host.HostAnswer, nil, true, replyTaken},
		{host.HostAnswer, nil, false, replyTaken},
		{host.HostClosing, nil, true, replyRetry},
		{host.HostBusy, nil, true, replyRetry},
		{0, failed, true, replyRetry},
		{0, nil, true, replyOlder},
		{1, nil, true, replyOlder},
		{0, windows.ERROR_TIMEOUT, true, replyOlder},
		{0, nil, false, replyGone},
		{1, nil, false, replyGone},
		{0, failed, false, replyGone},
		{host.HostClosing, nil, false, replyGone},
	} {
		if got := hostReply(c.answer, c.err, c.alive); got != c.want {
			t.Errorf("hostReply(%#x, %v, alive %v) = %d, want %d", c.answer, c.err, c.alive, got, c.want)
		}
	}
}
