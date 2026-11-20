//go:build windows

package polite

import (
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Go's monotonic clock on Windows advances with the system timer tick, which can be as
// coarse as 15.6 ms: too coarse for delays judged against a 30 ms target. Probes are timed
// with the performance counter instead (100 ns or finer).
var (
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	procQPC  = kernel32.NewProc("QueryPerformanceCounter")
	procQPF  = kernel32.NewProc("QueryPerformanceFrequency")
	// qpcFreq is the counter's frequency in counts per second (0: unavailable), read at
	// the first probe.
	qpcFreq = sync.OnceValue(queryFrequency)
)

func queryFrequency() int64 {
	var f int64
	if procQPF.Find() != nil || procQPC.Find() != nil {
		return 0
	}
	if r, _, _ := procQPF.Call(uintptr(unsafe.Pointer(&f))); r == 0 {
		return 0
	}
	return f
}

// stopwatch starts timing and returns a function that reports the time elapsed since.
func stopwatch() func() time.Duration {
	freq := qpcFreq()
	if freq <= 0 {
		start := time.Now()
		return func() time.Duration { return time.Since(start) }
	}
	var start int64
	procQPC.Call(uintptr(unsafe.Pointer(&start)))
	return func() time.Duration {
		var now int64
		procQPC.Call(uintptr(unsafe.Pointer(&now)))
		ticks := now - start
		return time.Duration(ticks/freq)*time.Second + time.Duration(ticks%freq)*time.Second/time.Duration(freq)
	}
}
