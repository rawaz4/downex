package main

import (
	"testing"

	"downex/internal/client"
)

// TestOpensInBackground: inside Downex.app only the progress app, which downexd opens by
// itself, is opened in the background; the window answers the user, and outside a
// bundle nothing goes through Launch Services.
func TestOpensInBackground(t *testing.T) {
	const helpers = "/Applications/Downex.app/Contents/Helpers"
	l := client.LayoutOf("darwin", helpers+"/downexd")
	for exe, want := range map[string]bool{
		l.Progress:             true,
		l.Window:               false,
		l.Agent:                false,
		helpers + "/downex-ui": false,
	} {
		if got := opensInBackground(l, exe); got != want {
			t.Errorf("opensInBackground(%s) = %v, want %v", exe, got, want)
		}
	}
	dev := client.LayoutOf("darwin", "/Users/me/downex/downexd")
	if opensInBackground(dev, dev.Progress) {
		t.Errorf("outside a bundle: %s opened in the background", dev.Progress)
	}
}
