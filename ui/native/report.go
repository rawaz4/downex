//go:build windows

package main

import (
	"encoding/json"
	"os"
	"runtime"
	"time"
	"unsafe"

	"downex/ui/kit"
	"downex/ui/win"

	"golang.org/x/sys/windows"
)

// sinceStart is the time since the process was made.
func sinceStart() time.Duration {
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(windows.CurrentProcess(), &created, &exited, &kernel, &user); err != nil {
		return 0
	}
	return time.Since(time.Unix(0, created.Nanoseconds()))
}

// report is what -report writes, for the measuring scripts.
type report struct {
	PID          int     `json:"pid"`
	HWND         uint64  `json:"hwnd"`
	Renderer     string  `json:"renderer"`
	FirstFrameMS float64 `json:"first_frame_ms"`
	Version      string  `json:"version"`
}

func writeReport(path string, w *win.Window, first time.Duration) {
	b, _ := json.Marshal(report{PID: os.Getpid(), HWND: uint64(w.HWND()), Renderer: w.Renderer(),
		FirstFrameMS: float64(first.Microseconds()) / 1000, Version: version})
	writeAtomic(path, b)
}

func writeAtomic(path string, b []byte) {
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, path)
	}
}

// ticks is a FILETIME duration in 100 ns units.
func ticks(f windows.Filetime) uint64 { return uint64(f.HighDateTime)<<32 | uint64(f.LowDateTime) }

// sample is one line of -stats: the window's counters (kit.Stats) and the process's memory
// and CPU at that moment.
type sample struct {
	T             float64 `json:"t"`
	Frames        int     `json:"frames"`
	Paints        int     `json:"paints"`
	Layouts       int     `json:"layouts"`
	FrameRequests int     `json:"frame_requests"`
	Invalidations int     `json:"invalidations"`
	PaintedArea   float64 `json:"painted_area"`
	Cycles        uint64  `json:"cycles"`
	CPUms         float64 `json:"cpu_ms"`
	GoHeapMB      float64 `json:"go_heap_mb"`
	GoResidentMB  float64 `json:"go_resident_mb"`
	Goroutines    int     `json:"goroutines"`
}

var procQueryProcessCycleTime = windows.NewLazySystemDLL("kernel32.dll").NewProc("QueryProcessCycleTime")

// startStats writes a sample of kw to path once a second (nothing when path is ""), until the
// returned stop runs or the window closes. Each sample is read on the UI thread.
func startStats(path string, kw *kit.Window) (stop func()) {
	if path == "" {
		return func() {}
	}
	out, err := os.Create(path)
	if err != nil {
		return func() {}
	}
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer out.Close()
		enc := json.NewEncoder(out)
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
			}
			got := make(chan sample, 1)
			if kw.Post(func() {
				st := kw.Stats()
				var cycles uint64
				procQueryProcessCycleTime.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&cycles)))
				var c, e, k, u windows.Filetime
				_ = windows.GetProcessTimes(windows.CurrentProcess(), &c, &e, &k, &u)
				var ms runtime.MemStats
				runtime.ReadMemStats(&ms)
				got <- sample{T: time.Since(start).Seconds(), Frames: st.Frames, Paints: st.Paints, Layouts: st.Layouts,
					FrameRequests: st.FrameRequests, Invalidations: st.Invalidations, PaintedArea: st.PaintedArea,
					Cycles: cycles, CPUms: float64(ticks(k)+ticks(u)) / 1e4,
					GoHeapMB: float64(ms.HeapAlloc) / (1 << 20), GoResidentMB: float64(ms.Sys-ms.HeapReleased) / (1 << 20),
					Goroutines: runtime.NumGoroutine()}
			}) != nil {
				return
			}
			select {
			case s := <-got:
				_ = enc.Encode(s)
			case <-done:
				return
			}
		}
	}()
	return func() { close(done) }
}
