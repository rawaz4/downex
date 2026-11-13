//go:build !unix && !windows

package main

import "os"

// stopSignals are the signals that stop the daemon gracefully.
func stopSignals() []os.Signal { return []os.Signal{os.Interrupt} }

// urgentSignal reports signals after which the system terminates the process soon.
func urgentSignal(os.Signal) bool { return false }
