package app_test

import (
	"testing"
	"time"

	"downex/ui/views/app"
	"downex/ui/views/viewstest"
)

func confirmingMain(t *testing.T) (*viewstest.Harness, *app.Main) {
	t.Helper()
	h := viewstest.New(t, viewstest.Options{Fixture: "confirm-file"})
	m, err := app.New(h.App, app.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Dispose)
	return h, m
}

// While the standalone Confirm download window is open, the main window opens no
// confirmation by itself (docs/DESKTOP-SPEC.md §1); once it has closed, the queue goes on.
func TestTheMainWindowLeavesConfirmationsToTheConfirmWindow(t *testing.T) {
	h, m := confirmingMain(t)
	open := true
	m.SetConfirmElsewhere(func() bool { return open })
	h.Show(m.Root())
	h.Settle()
	h.Advance(1500 * time.Millisecond)
	if h.App.Dialogs.DialogOpen() {
		t.Fatal("the main window opened a confirmation while the confirm window is open")
	}
	open = false
	m.ConfirmElsewhereChanged()
	h.Settle()
	h.Advance(1500 * time.Millisecond)
	if _, ok := m.Confirming(); !ok {
		t.Fatal("the confirm queue did not go on once the confirm window closed")
	}
}

// A confirmation the main window shows goes when the confirm window shows that download, and
// does not come back by itself.
func TestConfirmShownElsewhereTakesTheMainWindowsDialogAway(t *testing.T) {
	h, m := confirmingMain(t)
	h.Show(m.Root())
	h.Settle()
	h.Advance(1500 * time.Millisecond)
	id, ok := m.Confirming()
	if !ok {
		t.Fatal("no confirmation in the main window")
	}
	m.ConfirmShownElsewhere(id)
	h.Settle()
	h.Advance(1500 * time.Millisecond)
	if _, ok := m.Confirming(); ok {
		t.Fatal("the main window still shows the confirmation")
	}
	if !m.Dismissed(id) {
		t.Fatal("the download's dialog would open again by itself")
	}
}
