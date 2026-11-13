//go:build !windows && !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package main

import (
	"errors"
	"os"
)

// hideInput cannot hide terminal input where no terminal support is implemented: a
// terminal is refused (the password would be echoed), anything else is read as is.
func hideInput(f *os.File) (restore func(), term bool, err error) {
	if isTerminal(f) {
		return nil, true, errors.New("not supported on this system; pipe the password on standard input")
	}
	return nil, false, nil
}
