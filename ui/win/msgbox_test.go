//go:build windows

package win

import (
	"errors"
	"strings"
	"testing"
	"time"

	"downex/ui/kit"
)

type confirmAnswer struct {
	ok  bool
	err error
}

// The risky-file question is today's message box: the title and the text as given (the
// host escaped the names in them), Yes and No with No the default, system modal (topmost),
// owned by the shown window; Yes and No answer true and false.
func TestConfirmIsTodaysYesNoBox(t *testing.T) {
	desk := onTestDesktop(t)
	a, w := openTest(t, &testRoot{}, WindowOptions{Software: true})
	d, err := NewDialogs(w)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	const title = "Open this program?"
	// A name with characters a dialog could misread: the box shows it exactly.
	const text = "“setup&run (1).exe” can run programs and change this computer.\n\nOpen it only if you trust where it came from."
	for _, c := range []struct {
		name   string
		button int
		want   bool
	}{{"Yes opens", idYes, true}, {"No declines", 7, false}} {
		func() { // on the test's thread, the UI thread (a subtest would run on another)
			t.Logf("case %q", c.name)
			res := make(chan confirmAnswer, 1)
			go func() {
				ok, err := d.Confirm(title, text)
				res <- confirmAnswer{ok, err}
			}()
			var (
				owner, defID    uintptr
				shown, topmost  bool
				body            string
				haveYes, haveNo bool
			)
			drv := onDesktop(t, desk, func() {
				dlg := waitWindow("#32770", title, 10*time.Second)
				if dlg == 0 {
					return
				}
				shown = true
				owner = ownerOf(dlg)
				topmost = exStyleOf(dlg)&0x8 != 0 // WS_EX_TOPMOST: MB_SYSTEMMODAL
				defID = sendMsg(dlg, 0x0400 /* DM_GETDEFID */, 0, 0) & 0xFFFF
				body = windowText(dlgItem(dlg, 0xFFFF))
				haveYes, haveNo = dlgItem(dlg, idYes) != 0, dlgItem(dlg, 7) != 0
				postMsg(dlg, 0x0111 /* WM_COMMAND */, uintptr(c.button), 0)
			})
			var got confirmAnswer
			pump(t, a, w, "the answer", 15*time.Second, func() bool {
				select {
				case got = <-res:
					return true
				default:
					return false
				}
			})
			<-drv
			if !shown {
				t.Fatal("no message box titled " + title)
			}
			if got.err != nil || got.ok != c.want {
				t.Fatalf("Confirm = %v, %v; want %v", got.ok, got.err, c.want)
			}
			if owner != uintptr(w.hwnd) {
				t.Errorf("owner %#x, want the window %#x", owner, w.hwnd)
			}
			if !topmost {
				t.Error("not system modal (topmost)")
			}
			if defID != 7 {
				t.Errorf("default button %d, want No (7)", defID)
			}
			if !haveYes || !haveNo {
				t.Errorf("buttons Yes %v No %v", haveYes, haveNo)
			}
			if body != text {
				t.Errorf("text %q, want %q", body, text)
			}
		}()
	}
}

// While the question is open the window goes on: what is posted to it runs (live
// numbers), as the WebView2 page did behind today's box.
func TestConfirmKeepsTheWindowLive(t *testing.T) {
	desk := onTestDesktop(t)
	a, w := openTest(t, &testRoot{}, WindowOptions{Software: true})
	d, err := NewDialogs(w)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	res := make(chan confirmAnswer, 1)
	go func() {
		ok, err := d.Confirm("Live", "text")
		res <- confirmAnswer{ok, err}
	}()
	ran := make(chan struct{}, 1)
	drv := onDesktop(t, desk, func() {
		dlg := waitWindow("#32770", "Live", 10*time.Second)
		if dlg == 0 {
			return
		}
		// The dialog is open: a function posted to the window now runs before it closes.
		if err := w.Post(func() { ran <- struct{}{} }); err != nil {
			t.Error(err)
		}
		select {
		case <-ran:
		case <-time.After(5 * time.Second):
			t.Error("a posted function did not run while the box was open")
		}
		postMsg(dlg, 0x0111, 7, 0)
	})
	pump(t, a, w, "the answer", 15*time.Second, func() bool {
		select {
		case <-res:
			return true
		default:
			return false
		}
	})
	<-drv
}

// Questions wait their turn; Close fails the ones that never showed, and a closed
// Dialogs answers kit.ErrClosed.
func TestDialogsCloseFailsWaitingCalls(t *testing.T) {
	_ = onTestDesktop(t)
	_, w := openTest(t, &testRoot{}, WindowOptions{Software: true})
	d, err := NewDialogs(w)
	if err != nil {
		t.Fatal(err)
	}
	res := make(chan confirmAnswer, 1)
	go func() {
		ok, err := d.Confirm("Never shown", "text")
		res <- confirmAnswer{ok, err}
	}()
	// The loop is not run: the call waits in the queue until Close.
	time.Sleep(100 * time.Millisecond)
	d.Close()
	select {
	case got := <-res:
		if got.ok || !errors.Is(got.err, kit.ErrClosed) {
			t.Fatalf("Confirm after Close = %v, %v", got.ok, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a waiting call did not end at Close")
	}
	if _, err := d.Confirm("x", "y"); !errors.Is(err, kit.ErrClosed) {
		t.Fatalf("Confirm on closed Dialogs: %v", err)
	}
	if _, err := d.Confirm("bad\x00title", "y"); err == nil || !strings.Contains(err.Error(), "title") {
		t.Fatalf("a NUL in the title: %v", err)
	}
}
