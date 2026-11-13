//go:build windows

package main

import (
	"os"
	"syscall"
)

// stopSignals are the signals that stop the daemon gracefully. Go delivers Ctrl+C and
// Ctrl+Break as os.Interrupt, and closing the console window (and, for a process
// without user32.dll, logoff and shutdown) as SIGTERM.
func stopSignals() []os.Signal { return []os.Signal{os.Interrupt, syscall.SIGTERM} }

// urgentSignal reports signals after which the system terminates the process within
// seconds: Windows kills a process about 5 s after its console was closed.
func urgentSignal(s os.Signal) bool { return s == syscall.SIGTERM }
