//go:build unix

package e2e

import (
	"errors"
	"syscall"
)

// processAlive reports whether process pid still exists (signal 0 probes it).
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
