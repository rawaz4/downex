//go:build unix

package main

import (
	"os"
	"syscall"
)

// stopSignals are the signals that stop the daemon gracefully: Ctrl+C, SIGTERM (kill,
// systemd, logout) and SIGHUP (its terminal was closed), whose default action would end
// the process without pausing its downloads.
func stopSignals() []os.Signal { return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP} }

// urgentSignal reports signals after which the system terminates the process within
// seconds; none of these is (systemd, for one, waits 90 s after SIGTERM).
func urgentSignal(os.Signal) bool { return false }
