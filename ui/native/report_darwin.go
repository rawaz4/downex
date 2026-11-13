//go:build darwin && cgo

package main

import (
	"encoding/json"
	"os"
	"runtime"
	"syscall"
	"time"

	"downex/ui/kit"

	"golang.org/x/sys/unix"
)

// sinceStart is the time since the process was made (the kernel's start time of it).
func sinceStart() time.Duration {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", os.Getpid())
	if err != nil {
		return 0
	}
	tv := kp.Proc.P_starttime
	return time.Since(time.Unix(int64(tv.Sec), int64(tv.Usec)*1000))
}

// report is what -report writes, for the measuring scripts (Windows' shape: the window's
// number in the window server stands in for the HWND).
type report struct {
	PID          int     `json:"pid"`
	HWND         uint64  `json:"hwnd"`
	Renderer     string  `json:"renderer"`
	FirstFrameMS float64 `json:"first_frame_ms"`
	Version      string  `json:"version"`
}

func writeReport(path string, w *osWindow, first time.Duration) {
	b, _ := json.Marshal(report{PID: os.Getpid(), HWND: uint64(w.WindowNumber()), Renderer: w.Renderer(),
		FirstFrameMS: float64(first.Microseconds()) / 1000, Version: version})
	writeAtomic(path, b)
}

func writeAtomic(path string, b []byte) {
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, path)
	}
}

// sample is one line of -stats (Windows' shape; cycles are not measured on macOS).
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
				var ru syscall.Rusage
				_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
				cpu := float64(ru.Utime.Nano()+ru.Stime.Nano()) / 1e6
				var ms runtime.MemStats
				runtime.ReadMemStats(&ms)
				got <- sample{T: time.Since(start).Seconds(), Frames: st.Frames, Paints: st.Paints, Layouts: st.Layouts,
					FrameRequests: st.FrameRequests, Invalidations: st.Invalidations, PaintedArea: st.PaintedArea,
					CPUms: cpu, GoHeapMB: float64(ms.HeapAlloc) / (1 << 20), GoResidentMB: float64(ms.Sys-ms.HeapReleased) / (1 << 20),
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
