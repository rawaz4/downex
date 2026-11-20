//go:build !windows

package polite

import "time"

// stopwatch starts timing with Go's monotonic clock and returns a function that reports
// the time elapsed since.
func stopwatch() func() time.Duration {
	start := time.Now()
	return func() time.Duration { return time.Since(start) }
}
