//go:build darwin && cgo

package mac

// [popups]

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/mac/popuptest"
)

// runDialog runs f off the main thread while the main thread handles events, until f
// returns.
func runDialog(t *testing.T, f func()) {
	t.Helper()
	var done atomic.Bool
	go func() {
		defer done.Store(true)
		f()
	}()
	if !pumpUntil(10*time.Second, done.Load) {
		t.Fatal("the dialog did not end")
	}
}

func newTestDialogs(t *testing.T) (*Window, *Dialogs) {
	w := openTestWindow(t, &recRoot{ground: kit.RGBA(240, 240, 240, 1)}, kit.Size{W: 500, H: 400})
	var d *Dialogs
	mainDo(func() {
		var err error
		if d, err = NewDialogs(w); err != nil {
			t.Fatal(err)
		}
	})
	t.Cleanup(func() { mainDo(d.Close) })
	return w, d
}

func TestConfirmIsTodaysAlert(t *testing.T) {
	w, d := newTestDialogs(t)
	for _, c := range []struct {
		answer int
		yes    bool
	}{{1, true}, {2, false}} {
		popuptest.AnswerDialog(c.answer, 300*time.Millisecond)
		var yes bool
		var err error
		var posts atomic.Int32
		runDialog(t, func() {
			// The window behind the alert goes on taking posts.
			go func() {
				time.Sleep(100 * time.Millisecond)
				_ = w.Post(func() { posts.Add(1) })
			}()
			yes, err = d.Confirm("Open “setup.pkg”?", "It can change your Mac.")
		})
		if err != nil || yes != c.yes {
			t.Errorf("answer %d: yes %v err %v", c.answer, yes, err)
		}
		if posts.Load() != 1 {
			t.Error("a post to the window did not run while the alert was open")
		}
		cfg := popuptest.LastDialog()
		// Its texts, Yes then No with No on Return (no Escape), above other windows: it is set
		// to NSFloatingWindowLevel (3), which runModal raises to the modal panel level (8)
		// while it runs. The informational style cannot be read from its window.
		want := "alert texts=Open “setup.pkg”?|It can change your Mac. buttons=No[\\r],Yes[] level="
		if !popuptest.Active() && !strings.HasPrefix(cfg, want) {
			// An inactive application's alert (a test process macOS did not activate) shows
			// no key equivalents while it is inactive: AppKit's own, as for today's alert.
			t.Logf("the test process is not active; alert read as %s", cfg)
			want = strings.Replace(want, `No[\r]`, "No[]", 1)
		}
		if !strings.HasPrefix(cfg, want) || !(strings.HasSuffix(cfg, "=3") || strings.HasSuffix(cfg, "=8")) {
			t.Errorf("alert set up as\n%s\nwant\n%s", cfg, want)
		}
	}
}

func TestPickers(t *testing.T) {
	_, d := newTestDialogs(t)
	dir := t.TempDir()
	real, _ := filepath.EvalSymlinks(dir)

	popuptest.AnswerDialog(2, 500*time.Millisecond)
	var got string
	var err error
	runDialog(t, func() { got, err = d.PickFolder(context.Background(), "Choose a folder", dir) })
	if got != "" || err != nil {
		t.Errorf("cancelled folder picker: %q %v", got, err)
	}
	cfg := popuptest.LastDialog()
	if !strings.HasPrefix(cfg, "picker files=0 dirs=1 create=1 multiple=0 other=0 hidden=0 aliases=0 packages=0 dir=") ||
		!strings.Contains(cfg, "sheet=1") {
		t.Errorf("folder picker set up as %s", cfg)
	}

	popuptest.AnswerDialog(1, 500*time.Millisecond)
	runDialog(t, func() { got, err = d.PickFolder(context.Background(), "Choose a folder", dir) })
	// Ended with OK by the test (no selection made in the panel): the chosen folder, if the
	// panel reports one, else "".
	t.Logf("OK in %s chose %q", dir, got)
	if err != nil || (got != "" && got != dir && got != real) {
		t.Errorf("folder picker OK: %q %v, want %q", got, err, dir)
	}

	popuptest.AnswerDialog(2, 500*time.Millisecond)
	runDialog(t, func() { got, err = d.PickFile(context.Background(), "Choose a file", "") })
	cfg = popuptest.LastDialog()
	if got != "" || err != nil || !strings.HasPrefix(cfg, "picker files=1 dirs=0 create=0 multiple=0 other=1 hidden=0 aliases=0 packages=0 dir=") {
		t.Errorf("file picker: %q %v, set up as %s", got, err, cfg)
	}

	// Two at once: one after the other.
	popuptest.AnswerDialog(2, 300*time.Millisecond)
	popuptest.AnswerDialog(2, 600*time.Millisecond)
	var n atomic.Int32
	runDialog(t, func() {
		ch := make(chan struct{}, 2)
		for range 2 {
			go func() {
				if _, err := d.PickFolder(context.Background(), "", ""); err == nil {
					n.Add(1)
				}
				ch <- struct{}{}
			}()
		}
		<-ch
		<-ch
	})
	if n.Load() != 2 {
		t.Errorf("%d of 2 queued pickers answered", n.Load())
	}

	// A context that ends before the dialog's turn; Close fails waiting calls.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	mainDo(func() { d.running = true }) // a dialog is open: the call waits its turn
	if _, err := d.PickFile(ctx, "", ""); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled wait: %v", err)
	}
	errc := make(chan error, 1)
	go func() { _, err := d.Confirm("t", "m"); errc <- err }()
	time.Sleep(100 * time.Millisecond)
	mainDo(d.Close)
	if err := <-errc; !errors.Is(err, kit.ErrClosed) {
		t.Errorf("a waiting call after Close: %v", err)
	}
	if _, err := d.Confirm("t", "m"); !errors.Is(err, kit.ErrClosed) {
		t.Errorf("a call after Close: %v", err)
	}
}

func TestOpenExternalOnlyHTTPS(t *testing.T) {
	for _, u := range []string{"http://example.com", "file:///etc/hosts", "https:///x", "javascript:alert(1)", ""} {
		if err := OpenExternal(context.Background(), u); !errors.Is(err, ErrNotHTTPS) {
			t.Errorf("%q: %v", u, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := OpenExternal(ctx, "https://example.com/licence"); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}

// A picker shown as a sheet ends as cancelled when its window closes, or its Dialogs close:
// AppKit would never answer it, and every later dialog would wait for it.
func TestPickerSheetEndsWithItsOwner(t *testing.T) {
	// "command" is the menu's Quit or Close Window (kit.WindowClose): AppKit ignores
	// performClose: on a window with a sheet, so the window would stay.
	for _, how := range []string{"window", "dialogs", "command"} {
		w, d := newTestDialogs(t)
		popuptest.AnswerDialog(0, 300*time.Millisecond) // records it, leaves it open
		got, gotErr := "x", error(nil)
		var done atomic.Bool
		go func() {
			defer done.Store(true)
			got, gotErr = d.PickFolder(context.Background(), "Choose a folder", t.TempDir())
		}()
		if !pumpUntil(5*time.Second, func() bool { return popuptest.SheetOn(w.ns) }) {
			t.Fatalf("%s: no sheet on the window", how)
		}
		pump(200 * time.Millisecond)
		if !strings.Contains(popuptest.LastDialog(), "sheet=1") {
			t.Errorf("%s: the picker is not a sheet: %s", how, popuptest.LastDialog())
		}
		mainDo(func() {
			switch how {
			case "window":
				w.Close()
			case "command":
				w.Command(kit.WindowClose)
			default:
				d.Close()
			}
		})
		if !pumpUntil(5*time.Second, done.Load) {
			t.Fatalf("%s: the picker's caller still waits after its %s closed", how, how)
		}
		if got != "" || gotErr != nil {
			t.Errorf("%s: %q %v, want a cancel", how, got, gotErr)
		}
		mainDo(func() {
			if d.running || d.sheet != 0 {
				t.Errorf("%s: running %v sheet %d after the end", how, d.running, d.sheet)
			}
		})
		if how == "command" && !pumpUntil(5*time.Second, func() bool { return w.closed }) {
			t.Errorf("command: the window did not close")
		}
		if how != "dialogs" {
			// The next dialog is not stuck behind it (it opens app-modal: no window).
			popuptest.AnswerDialog(2, 300*time.Millisecond)
			var yes bool
			var err error
			runDialog(t, func() { yes, err = d.Confirm("t", "m") })
			if yes || err != nil {
				t.Errorf("the next dialog: %v %v", yes, err)
			}
		}
	}
}
