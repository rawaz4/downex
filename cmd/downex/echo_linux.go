//go:build linux

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// hideInput turns off the echo of the terminal f for a password prompt. term is false
// (and nothing changes) when f is not a terminal, e.g. a pipe or a file.
func hideInput(f *os.File) (restore func(), term bool, err error) {
	return hideTermiosEcho(int(f.Fd()), unix.TCGETS, unix.TCSETS)
}
