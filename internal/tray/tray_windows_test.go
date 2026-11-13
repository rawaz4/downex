//go:build windows

package tray

import (
	"testing"
	"time"
	"unsafe"
)

// onThread runs f on the tray's window thread and waits for it.
func onThread[T any](t *testing.T, tr *Tray, f func(*machine) T) T {
	t.Helper()
	ch := make(chan T, 1)
	tr.call(func(m *machine) { ch <- f(m) })
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatal("the window thread does not answer")
		panic("unreachable")
	}
}

// iconExists asks the shell about the tray's icon: NIM_MODIFY succeeds only for an
// existing icon.
func iconExists(tr *Tray) bool {
	nid := notifyIconData{hwnd: tr.hwnd, id: iconID}
	nid.size = uint32(unsafe.Sizeof(nid))
	return shellNotify(nimModify, &nid) == nil
}

func TestNotifyIconDataLayout(t *testing.T) {
	// sizeof(NOTIFYICONDATAW) in the Windows SDK.
	want := uintptr(976)
	if unsafe.Sizeof(uintptr(0)) == 4 {
		want = 956
	}
	if got := unsafe.Sizeof(notifyIconData{}); got != want {
		t.Fatalf("sizeof(notifyIconData) = %d, want %d", got, want)
	}
}

// TestTraySmoke really adds, changes, re-creates and deletes a notification-area icon
// of this process (its window handle makes it unique). It shows no notification.
func TestTraySmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("creates a real notification-area icon")
	}
	got := make(chan Command, 4)
	tr, err := New(Options{Tooltip: "Downex tray test", OnCommand: func(c Command) { got <- c }})
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	if iconExists(tr) {
		t.Fatal("the icon exists before SetVisible")
	}
	tr.SetVisible(true)
	if added := onThread(t, tr, func(m *machine) bool { return m.added }); !added {
		_ = tr.Close()
		t.Skip("the notification area is not available (no Explorer taskbar in this session)")
	}
	if !iconExists(tr) {
		t.Fatal("the shell does not know the added icon")
	}
	size := onThread(t, tr, func(*machine) int { return tr.w.iconSize })
	if size < 16 || tr.w.icon == 0 {
		t.Fatalf("icon %v of %d px", tr.w.icon, size)
	}

	tr.SetTooltip("Downex tray test — 1 active, 0 B/s")
	if tip := onThread(t, tr, func(m *machine) string { return m.tip }); tip != "Downex tray test — 1 active, 0 B/s" {
		t.Fatalf("tooltip = %q", tip)
	}

	// Explorer's TaskbarCreated broadcast re-creates the icon.
	if tr.taskbarCreated == 0 {
		t.Fatal("TaskbarCreated is not registered")
	}
	_, _, _ = procPostMessageW.Call(uintptr(tr.hwnd), uintptr(tr.taskbarCreated), 0, 0)
	if added := onThread(t, tr, func(m *machine) bool { return m.added }); !added || !iconExists(tr) {
		t.Fatal("the icon is gone after TaskbarCreated")
	}

	// A double-click, as the shell reports it, sends CommandOpen.
	_, _, _ = procPostMessageW.Call(uintptr(tr.hwnd), wmCallback, 0, uintptr(evLButtonDblClk|iconID<<16))
	select {
	case c := <-got:
		if c != CommandOpen {
			t.Fatalf("double-click sent %v", c)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("double-click sent no command")
	}

	tr.SetVisible(false)
	if added := onThread(t, tr, func(m *machine) bool { return m.added }); added || iconExists(tr) {
		t.Fatal("the icon is still there after SetVisible(false)")
	}
	tr.SetVisible(true)
	onThread(t, tr, func(m *machine) bool { return m.added })
	if !iconExists(tr) {
		t.Fatal("the icon did not come back")
	}

	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if iconExists(tr) {
		t.Fatal("the icon survived Close")
	}
	select {
	case <-tr.loopDone:
	default:
		t.Fatal("the window thread still runs after Close")
	}
	// Calls after Close are ignored.
	tr.SetVisible(true)
	tr.Notify(Balloon{Title: "ignored"})
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestTrayCloseWhileMenuOpen: Close ends an open context menu (TrackPopupMenuEx runs
// a modal loop on the window thread) instead of waiting for the user.
func TestTrayCloseWhileMenuOpen(t *testing.T) {
	if testing.Short() {
		t.Skip("creates a real notification-area icon and opens its menu")
	}
	got := make(chan Command, 4)
	tr, err := New(Options{Tooltip: "Downex tray test", OnCommand: func(c Command) { got <- c }})
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	tr.SetVisible(true)
	if added := onThread(t, tr, func(m *machine) bool { return m.added }); !added {
		t.Skip("the notification area is not available (no Explorer taskbar in this session)")
	}
	_, _, _ = procPostMessageW.Call(uintptr(tr.hwnd), wmCallback, 0, uintptr(evContextMenu|iconID<<16))
	deadline := time.Now().Add(5 * time.Second)
	for !tr.w.inMenu.Load() {
		if time.Now().After(deadline) {
			t.Skip("the context menu did not open")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Requests made while the menu is open wait for it.
	tr.SetTooltip("changed while the menu is open")
	start := time.Now()
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("Close took %v with the menu open", d)
	}
	if iconExists(tr) {
		t.Fatal("the icon survived Close")
	}
	select {
	case c := <-got:
		t.Fatalf("the closed menu sent %v", c)
	default:
	}
}

func TestNilTray(t *testing.T) {
	var tr *Tray
	tr.SetVisible(true)
	tr.SetTooltip("x")
	tr.Notify(Balloon{})
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if !Supported() {
		t.Fatal("Supported() is false on Windows")
	}
}
