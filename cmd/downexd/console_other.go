//go:build !windows

package main

import (
	"io"
	"os"
)

// setupConsole returns the standard output and error: only Windows gives programs
// console windows of their own (console_windows.go).
func setupConsole([]string) (stdout, stderr io.Writer, handedOver bool) {
	return os.Stdout, os.Stderr, false
}
