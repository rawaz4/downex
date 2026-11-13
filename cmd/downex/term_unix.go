//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// enableVT is a no-op: Unix terminals interpret escape sequences natively.
func enableVT(*os.File) (restore func(), err error) { return func() {}, nil }

// termSize returns the size of the terminal f, in cells.
func termSize(f *os.File) (width, height int, ok bool) {
	ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 || ws.Row == 0 {
		return 0, 0, false
	}
	return int(ws.Col), int(ws.Row), true
}
