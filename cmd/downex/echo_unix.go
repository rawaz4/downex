//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package main

import "golang.org/x/sys/unix"

// hideTermiosEcho clears ECHO on terminal fd (keeping line editing and Ctrl+C) with the
// platform's get/set requests, and returns the function that restores the old mode.
func hideTermiosEcho(fd int, get, set uint) (restore func(), term bool, err error) {
	old, err := unix.IoctlGetTermios(fd, get)
	if err != nil {
		return nil, false, nil // not a terminal
	}
	t := *old
	t.Lflag &^= unix.ECHO
	t.Lflag |= unix.ICANON | unix.ISIG
	t.Iflag |= unix.ICRNL
	if err := unix.IoctlSetTermios(fd, set, &t); err != nil {
		return nil, true, err
	}
	return func() { _ = unix.IoctlSetTermios(fd, set, old) }, true, nil
}
