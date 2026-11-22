//go:build darwin && cgo

package main

import (
	"testing"

	"downex/ui/host"
)

// TestFirstMouseOnlyOverTheBrowser: the standalone Confirm download window (a Dialog) and the
// progress windows, which open over the browser the user is in, take the first click; the
// main window keeps the Mac's rule.
func TestFirstMouseOnlyOverTheBrowser(t *testing.T) {
	for _, c := range []struct {
		name string
		ws   windowSpec
		v    host.View
		want bool
	}{
		{"main window", windowSpec{Title: "Downex"}, host.View{}, false},
		{"main window maximised", windowSpec{Maximized: true}, host.View{}, false},
		{"confirm window", windowSpec{Dialog: true}, host.View{Confirm: 4}, true},
		{"progress window", windowSpec{}, host.View{Progress: 9}, true},
	} {
		if got := acceptsFirstMouse(c.ws, c.v); got != c.want {
			t.Errorf("%s: acceptsFirstMouse %v, want %v", c.name, got, c.want)
		}
	}
}
