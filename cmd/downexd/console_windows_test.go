//go:build windows

package main

import "testing"

// chooseConsole (GO-1): only a sign-in style start (--background in a console of its
// own) hands over, and only a GUI build run from a terminal for output attaches to it.
// TestSignInStartHasNoConsole (internal/e2e) starts real builds as sign-in does.
func TestChooseConsole(t *testing.T) {
	bg := options{background: true}
	for _, c := range []struct {
		name         string
		o            options
		parsed       bool
		procs        int
		stdioMissing bool
		want         consoleAction
	}{
		{"sign-in start of a console build", bg, true, 1, false, consoleHandOver},
		{"absolute paths", options{background: true, db: `C:\data\downex.db`, logDir: `C:\logs`}, true, 1, false, consoleHandOver},
		{"relative --db", options{background: true, db: "downex.db"}, true, 1, false, consoleKeep},
		{"relative --log-dir", options{background: true, logDir: "logs"}, true, 1, false, consoleKeep},
		{"--background in a shared terminal", bg, true, 2, false, consoleKeep},
		{"--background without a console", bg, true, 0, true, consoleKeep},
		{"--background with invalid flags", bg, false, 1, false, consoleKeep},
		{"console build in its own console, no --background", options{}, true, 1, false, consoleKeep},
		{"GUI build in a terminal, --foreground", options{foreground: true}, true, 0, true, consoleAttach},
		{"GUI build in a terminal, --help or invalid flags", options{}, false, 0, true, consoleAttach},
		{"GUI build in a terminal, plain start", options{}, true, 0, true, consoleKeep},
		{"GUI build with pipes (installer probe)", options{}, false, 0, false, consoleKeep},
		{"detached (NUL handles), --foreground", options{foreground: true}, true, 0, false, consoleKeep},
		{"console build in a terminal, --foreground", options{foreground: true}, true, 2, false, consoleKeep},
	} {
		if got := chooseConsole(c.o, c.parsed, c.procs, c.stdioMissing); got != c.want {
			t.Errorf("%s: chooseConsole = %d, want %d", c.name, got, c.want)
		}
	}
}
