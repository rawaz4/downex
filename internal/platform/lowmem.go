package platform

import (
	"context"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"time"
)

// Memory settings of Downex's long-running processes (downexd, downex-ui). They run all
// day beside the user's other programs, so they trade a little CPU for memory.
//
// What an idle downexd holds (measured 2026-11-12 on a 32-thread Windows 11 laptop, a
// private daemon with an empty database, before and after the idle trims below): working
// set 26.7 MB (private working set 8.3 MB) at start, 29.5-33 MB (8.4-11.9 MB) after a
// 256 MiB download; with the trims 0.5-2.5 MB (0.3-1.1 MB) from 20 s after start and
// within two minutes of a download's end. Commit (Task Manager's "Commit size", Private
// Bytes) stays 53-57 MB either way: 32 MiB of it is a zero-filled buffer of Go's crypto
// (crypto/internal/fips140/drbg.memory) that Windows charges to commit but that is normally
// not read in (ui/native/glue/imagetail_windows.go). The live Go heap is about 2 MB, so the
// settings below matter during downloads more than at rest.
const (
	// lowMemMaxProcs bounds the Go scheduler's processors: one per CPU thread (32 here)
	// keeps a span cache and a thread for each, and a download needs a few.
	lowMemMaxProcs = 4
	// lowMemGCPercent collects once the heap grew by half of what is live (Go's
	// default lets it double).
	lowMemGCPercent = 50
)

// TuneMemory applies the memory settings above to the Go runtime, each only when the
// environment does not choose it (GOMAXPROCS, GOGC).
func TuneMemory() {
	if os.Getenv("GOMAXPROCS") == "" {
		runtime.GOMAXPROCS(min(runtime.NumCPU(), lowMemMaxProcs))
	}
	if os.Getenv("GOGC") == "" {
		debug.SetGCPercent(lowMemGCPercent)
	}
}

// IdleStartupTrimAfter is when downexd gives back, once, what start-up touched
// (ReleaseMemoryAndTrimWhileIdle): the pages of start-up code, the database's opening and
// the DLLs it loaded, which an idle daemon does not touch again.
const IdleStartupTrimAfter = 20 * time.Second

// releaseMinAlloc is how much the heap must have allocated since the last release for an
// idle tick to release again. A forced collection touches the whole heap and the
// collector's code, which pulls about 6 MB back into an emptied working set (measured on
// an idle downexd): with less allocated than this, there is no freed memory worth that.
const releaseMinAlloc = 1 << 20

// ReleaseMemoryWhileIdle returns the memory the process no longer uses to the system while
// busy reports false, until ctx ends. Go keeps freed memory for reuse and hands it back only
// slowly; after a download that is many megabytes the process holds while it waits for the
// next one.
//
// Every interval it asks busy. An idle tick releases Go's freed memory (a forced
// collection) only when the heap allocated releaseMinAlloc since the last release, so a
// process with nothing to do is left alone. The tick on which the process has just become
// idle (the one before was busy) also empties the working set (TrimWorkingSet). A busy
// process is never touched: a download keeps every page it uses.
func ReleaseMemoryWhileIdle(ctx context.Context, interval time.Duration, busy func() bool) {
	ReleaseMemoryAndTrimWhileIdle(ctx, interval, 0, busy)
}

// ReleaseMemoryAndTrimWhileIdle is ReleaseMemoryWhileIdle plus one look startupTrim after
// the start (none when it is not positive) that releases and empties the working set if the
// process is idle then; if it is busy, its becoming idle trims instead. For a process that
// has no start-up trim of its own (downexd; the windows trim themselves, glue).
func ReleaseMemoryAndTrimWhileIdle(ctx context.Context, interval, startupTrim time.Duration, busy func() bool) {
	t := time.NewTicker(interval)
	defer t.Stop()
	var startup <-chan time.Time
	if startupTrim > 0 {
		st := time.NewTimer(startupTrim)
		defer st.Stop()
		startup = st.C
	}
	r := idleRelease{
		busy:      busy,
		allocated: heapAllocCounter(),
		release:   debug.FreeOSMemory,
		// A failed trim only leaves the pages where they are; nothing is lost.
		trim: func() { _ = TrimWorkingSet() },
	}
	r.run(ctx, t.C, startup)
}

// idleRelease is ReleaseMemoryWhileIdle's decision, apart from its clock and the runtime so
// that tests drive it tick by tick.
type idleRelease struct {
	busy      func() bool
	allocated func() uint64 // bytes the heap allocated since the process started
	release   func()        // gives Go's freed memory back
	trim      func()        // empties the working set

	wasBusy  bool   // the previous tick found the process busy
	released uint64 // allocated() at the last release
}

// run handles ticks and the one startup tick (nil: none) until ctx ends.
func (r *idleRelease) run(ctx context.Context, ticks, startup <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-startup:
			startup = nil
			r.tick(true)
		case <-ticks:
			r.tick(false)
		}
	}
}

// tick is one look at the process; startup marks the start-up trim's tick.
func (r *idleRelease) tick(startup bool) {
	if r.busy() {
		r.wasBusy = true
		return
	}
	settled := startup || r.wasBusy
	r.wasBusy = false
	now := r.allocated()
	if now-r.released < releaseMinAlloc {
		return
	}
	r.released = now
	r.release() // what Go freed goes first, so the trim takes the rest
	if settled {
		r.trim()
	}
}

// heapAllocCounter returns a reader of the bytes the heap has allocated since the process
// started, for one goroutine. The runtime metric is cumulative, so it is read without
// stopping the program (unlike runtime.ReadMemStats, its fallback for a runtime without it).
func heapAllocCounter() func() uint64 {
	sample := []metrics.Sample{{Name: "/gc/heap/allocs:bytes"}}
	return func() uint64 {
		metrics.Read(sample)
		if sample[0].Value.Kind() == metrics.KindUint64 {
			return sample[0].Value.Uint64()
		}
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		return ms.TotalAlloc
	}
}
