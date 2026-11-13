//go:build darwin && cgo

package progtest

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/mac"
)

// envRegular makes the tests' app a regular one (a Dock icon), as Downex.app is.
const envRegular = "PROGTEST_REGULAR"

// drive runs f off the main thread while the application's event loop runs (w open), until
// f returns (at most d).
func drive(t *testing.T, w *mac.Window, d time.Duration, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	run++
	this := run
	go func() {
		select {
		case <-done:
		case <-time.After(d):
		}
		app.Post(func() {
			if run == this {
				app.Quit()
			}
		})
	}()
	onMain(func() { _ = app.Run() })
	onMain(func() { run++ })
	select {
	case <-done:
	default:
		t.Fatalf("still running after %v", d)
	}
}

// waitModal waits up to d for a dialog in front whose state has prefix.
func waitModal(prefix string, d time.Duration) string {
	end := time.Now().Add(d)
	for {
		s := mac.ModalState()
		if strings.HasPrefix(s, prefix) || time.Now().After(end) {
			return s
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// inRegularApp runs the calling test in a regular app (a Dock icon, the menu bar, a key
// window: Downex.app's main window), a child process of this test binary, and reports
// true when it did (the caller returns); false in that child.
func inRegularApp(t *testing.T) bool {
	t.Helper()
	if os.Getenv(envRegular) != "" {
		return false
	}
	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v", "-test.timeout=60s")
	cmd.Env = append(os.Environ(), envRegular+"=1")
	out, err := cmd.CombinedOutput()
	if strings.Contains(string(out), "--- SKIP: "+t.Name()) {
		t.Skipf("in a regular app: %s", out)
	}
	if err != nil || !strings.Contains(string(out), "--- PASS: "+t.Name()) {
		t.Errorf("in a regular app: %v\n%s", err, out)
	}
	return true
}

// onLoop runs f on the main thread while the event loop runs, and waits.
func onLoop(f func()) {
	ch := make(chan struct{})
	app.Post(func() { f(); close(ch) })
	<-ch
}

// TestAboutIsTodays: Downex ▸ About Downex shows today's alert (ui/menu_darwin.go: the
// title and version, one OK button), and OK closes it.
func TestAboutIsTodays(t *testing.T) {
	if inRegularApp(t) {
		return
	}
	w, _ := open(t, mac.WindowOptions{Title: "t", Size: kit.Size{W: 300, H: 200}})
	var state string
	drive(t, w, 10*time.Second, func() {
		ran := make(chan bool, 1)
		go func() { ran <- mac.PerformMenuItem("Downex", "About Downex") }()
		state = waitModal("modal", 5*time.Second)
		if !mac.PressModalButton("OK") {
			t.Error("no OK button")
		}
		select {
		case ok := <-ran:
			if !ok {
				t.Error("no About item")
			}
		case <-time.After(5 * time.Second):
			t.Error("OK did not close the About alert")
		}
	})
	if !strings.Contains(state, "text=Downex|Version test\n\nA download manager.") || !strings.Contains(state, "buttons=OK[") {
		t.Errorf("About: %q", state)
	}
}

// TestHideHidesTheApp: ⌘H (Hide Downex) hides the application, and BringToFront (a later
// start) shows it again.
func TestHideHidesTheApp(t *testing.T) {
	if inRegularApp(t) {
		return
	}
	w, _ := open(t, mac.WindowOptions{Title: "t", Size: kit.Size{W: 300, H: 200}})
	var hidden, shown, took, active bool
	drive(t, w, 10*time.Second, func() {
		onLoop(func() { w.BringToFront(true) })
		time.Sleep(500 * time.Millisecond)
		active, _ = mac.AppState()
		onLoop(func() { took = mac.PerformMenuKey("h", 1<<20) })
		for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			if _, h := mac.AppState(); h {
				hidden = true
				break
			}
		}
		onLoop(func() { w.BringToFront(false) })
		for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			if _, h := mac.AppState(); !h {
				if _, v, _ := w.FrontState(); v {
					shown = true
					break
				}
			}
		}
	})
	if !hidden || !shown {
		t.Errorf("⌘H taken %v (app active %v): hidden %v, shown again %v", took, active, hidden, shown)
	}
}

// TestEditMenuReachesANativeField: the Edit menu's commands (Select All, Delete, Undo) go to
// the focused native text field, as Wails' Edit menu went to the page's fields. Cut, Copy
// and Paste are left out: they would use the user's pasteboard.
func TestEditMenuReachesANativeField(t *testing.T) {
	if inRegularApp(t) {
		return
	}
	w, _ := open(t, mac.WindowOptions{Title: "t", Size: kit.Size{W: 400, H: 200}})
	var f kit.NativeField
	onMain(func() {
		var err error
		f, err = w.NewField(kit.TextFieldOptions{Name: "Address"}, kit.FieldOptions{Kind: kit.FieldText}, kit.TextFieldCallbacks{})
		if err != nil {
			t.Fatal(err)
		}
		f.SetBounds(kit.R(10, 10, 310, 38))
		f.SetText("hello world")
	})
	defer onMain(f.Release)
	var afterSelect, afterDelete, afterUndo string
	var sel [2]int
	var took [3]bool
	var key bool
	drive(t, w, 15*time.Second, func() {
		onLoop(func() { w.BringToFront(true) })
		for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			if key, _, _ = w.FrontState(); key {
				break
			}
		}
		if !key {
			return // macOS did not activate the test's app: the menu has no key window to act on
		}
		get := func() (s string) {
			onLoop(func() { s = f.Text() })
			return s
		}
		onLoop(f.Focus)
		time.Sleep(200 * time.Millisecond)
		onLoop(func() { f.(kit.FieldControl).SetSelection(0, 0) })
		onLoop(func() { took[0] = mac.PerformMenuKey("a", 1<<20) })
		time.Sleep(100 * time.Millisecond)
		onLoop(func() { sel[0], sel[1] = f.(kit.FieldControl).Selection() })
		afterSelect = get()
		took[1] = mac.PerformMenuItem("Edit", "Delete")
		time.Sleep(100 * time.Millisecond)
		afterDelete = get()
		onLoop(func() { took[2] = mac.PerformMenuKey("z", 1<<20) })
		time.Sleep(100 * time.Millisecond)
		afterUndo = get()
	})
	if !key {
		t.Skip("macOS did not make the test's window key (another app is active and did not yield)")
	}
	if !took[0] || sel != [2]int{0, len("hello world")} || afterSelect != "hello world" {
		t.Errorf("⌘A (taken %v): selection %v, text %q", took[0], sel, afterSelect)
	}
	if !took[1] || afterDelete != "" {
		t.Errorf("Edit ▸ Delete (found %v): %q", took[1], afterDelete)
	}
	if !took[2] || afterUndo != "hello world" {
		t.Errorf("⌘Z (taken %v): %q", took[2], afterUndo)
	}
}
