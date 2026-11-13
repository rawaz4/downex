//go:build darwin && cgo

package tray

import (
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// AppKit runs on the main thread: the test binary hands it over like downexd does
// (cmd/downexd/mainthread_darwin.go).
func init() { runtime.LockOSThread() }

func TestMain(m *testing.M) { os.Exit(RunMain(m.Run)) }

// TestMenuBarSmoke really adds a menu-bar icon of this process, checks its menu, its
// status line and a menu choice through AppKit's target/action, hides and shows it,
// and removes it. It posts no notification: a test binary is not an app bundle.
func TestMenuBarSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("creates a real menu-bar icon")
	}
	if !Supported() {
		t.Skip("no window-server (GUI) session")
	}
	got := make(chan Command, 4)
	tr, err := New(Options{Tooltip: "Downex — 0 active, 0 B/s", OnCommand: func(c Command) { got <- c }})
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	be := tr.mb.be.(*cocoaStatus)
	describe := func() []string {
		t.Helper()
		return strings.Split(onMain(t, tr.mb, func(*machine) string { return be.describe() }), "\n")
	}

	if d := onMain(t, tr.mb, func(*machine) string { return be.describe() }); d != "" {
		t.Fatalf("a status item exists before SetVisible: %q", d)
	}
	tr.SetVisible(true)
	want := []string{"visible", "Downex — 0 active, 0 B/s", "(disabled) 0 active · 0 B/s", "-",
		"Open Downex", "Pause all", "Resume all", "-", "Settings…", "Quit Downex"}
	if d := describe(); strings.Join(d, "\n") != strings.Join(want, "\n") {
		t.Fatalf("status item %q, want %q", d, want)
	}

	tr.SetTooltip("Downex — 2 active, 1.5 MB/s")
	if d := describe(); len(d) < 3 || d[1] != "Downex — 2 active, 1.5 MB/s" || d[2] != "(disabled) 2 active · 1.5 MB/s" {
		t.Fatalf("after SetTooltip: %q", d)
	}

	// "Pause all" through the menu's action: menu item 3, after the status line, a
	// separator and "Open Downex".
	onMain(t, tr.mb, func(*machine) bool { be.perform(3); return true })
	select {
	case c := <-got:
		if c != CommandPauseAll {
			t.Fatalf("the menu sent %v, want pause-all", c)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the menu choice sent no command")
	}

	tr.SetVisible(false)
	if d := describe(); d[0] != "hidden" {
		t.Fatalf("after SetVisible(false): %q", d)
	}
	tr.SetVisible(true)
	if d := describe(); d[0] != "visible" {
		t.Fatalf("after SetVisible(true): %q", d)
	}

	released := releasedHandles.Load()
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if be.status != nil { // written by the final drain, which Close waited for
		t.Fatal("the status item survived Close")
	}
	deadline := time.Now().Add(10 * time.Second)
	for releasedHandles.Load() == released {
		if time.Now().After(deadline) {
			t.Fatal("the handle was not released after Close")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Calls after Close are ignored.
	tr.SetVisible(true)
	tr.Notify(Balloon{Title: "ignored"})
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestNilTrayDarwin: a nil *Tray ignores every call.
func TestNilTrayDarwin(t *testing.T) {
	var tr *Tray
	tr.SetVisible(true)
	tr.SetTooltip("x")
	tr.Notify(Balloon{})
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
}
