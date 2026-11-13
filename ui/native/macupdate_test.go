//go:build windows || (darwin && cgo)

package main

import (
	"slices"
	"testing"

	"downex/internal/client"
)

// The macOS installation's layouts (client.Layout as FindLayout gives them).
var (
	macInstalled = client.Layout{
		Exe: "/Applications/Downex.app/Contents/MacOS/Downex", Bundle: "/Applications/Downex.app",
		Dir: "/Applications/Downex.app/Contents/Helpers", Window: "/Applications/Downex.app/Contents/MacOS/Downex",
	}
	macTranslocated = client.Layout{
		Exe:    "/private/var/folders/ab/T/AppTranslocation/1234/d/Downex.app/Contents/MacOS/Downex",
		Bundle: "/private/var/folders/ab/T/AppTranslocation/1234/d/Downex.app",
	}
	devBuild = client.Layout{Exe: "/home/dev/downex/bin/downex-ui", Dir: "/home/dev/downex/bin"}
)

// TestRegistersBrowsers is the registration trigger: the macOS main window inside Downex.app
// registers at every start (the relay leaves right manifests alone and rewrites missing ones
// and those naming another path); never a translocated copy, a development build or Windows.
func TestRegistersBrowsers(t *testing.T) {
	for _, c := range []struct {
		name string
		goos string
		l    client.Layout
		want bool
	}{
		{"macOS, Downex.app", "darwin", macInstalled, true},
		{"macOS, translocated copy", "darwin", macTranslocated, false},
		{"macOS, development build", "darwin", devBuild, false},
		{"Windows (Setup registers)", "windows", client.Layout{Exe: `C:\Downex\downex-ui.exe`, Dir: `C:\Downex`}, false},
	} {
		if got := registersBrowsers(c.goos, c.l); got != c.want {
			t.Errorf("%s: registersBrowsers = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestReopenCommand: after an update the window opens the bundle again once this process has
// exited, with the bundle and pid as arguments (nothing in the script); not outside a bundle.
func TestReopenCommand(t *testing.T) {
	exe, args, ok := reopenCommand(macInstalled, 4242)
	if !ok || exe != "/bin/sh" || !slices.Equal(args, []string{"-c", reopenScript, "downex-reopen", "4242", "/Applications/Downex.app"}) {
		t.Errorf("reopenCommand = %s %q %v", exe, args, ok)
	}
	for name, l := range map[string]client.Layout{"development build": devBuild, "translocated": macTranslocated} {
		if _, _, ok := reopenCommand(l, 1); ok {
			t.Errorf("%s: a reopen helper", name)
		}
	}
}
