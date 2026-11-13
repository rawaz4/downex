//go:build darwin && cgo

package mac

import (
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/mac/popuptest"
)

// The Edit menu's Copy and Select All, chosen with the mouse, are answered by the content
// view, and reach only a focused element that takes edit commands (EditCommand: the
// read-only Page field, tested in ui/views/add): WebKit runs them as editing commands and
// fires no keydown, so a root (a list) hears no ⌘C or ⌘A from the menu.
func TestEditMenuCopyAndSelectAllReachTheFocus(t *testing.T) {
	root := &recRoot{}
	var got []kit.KeyEvent
	root.onKey = func(e kit.KeyEvent) bool {
		got = append(got, e)
		return true
	}
	w := openTestWindow(t, root, kit.Size{W: 300, H: 200})
	var okCopy, okAll bool
	mainDo(func() {
		okCopy = popuptest.Perform(w.ns, "copy:")
		okAll = popuptest.Perform(w.ns, "selectAll:")
	})
	pump(100 * time.Millisecond)
	if !okCopy || !okAll {
		t.Fatalf("copy: %v, selectAll: %v: the content view does not answer them", okCopy, okAll)
	}
	mainDo(func() {
		if len(got) != 0 {
			t.Errorf("keys heard from the menu: %+v, want none (WebKit fires no keydown)", got)
		}
	})
}
