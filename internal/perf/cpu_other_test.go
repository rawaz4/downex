//go:build perf && !windows

package perf

import (
	"syscall"
	"time"
)

// cpuTime returns the CPU time (user + system) this process has used.
func cpuTime() time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
