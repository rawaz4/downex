//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// hideInput turns off the echo of the console f for a password prompt. term is false
// (and nothing changes) when f is not a console, e.g. a pipe, a file or the null device.
func hideInput(f *os.File) (restore func(), term bool, err error) {
	h := windows.Handle(f.Fd())
	var mode uint32
	if windows.GetConsoleMode(h, &mode) != nil {
		return nil, false, nil
	}
	hidden := mode&^windows.ENABLE_ECHO_INPUT | windows.ENABLE_LINE_INPUT | windows.ENABLE_PROCESSED_INPUT
	if err := windows.SetConsoleMode(h, hidden); err != nil {
		return nil, true, err
	}
	return func() { _ = windows.SetConsoleMode(h, mode) }, true, nil
}
