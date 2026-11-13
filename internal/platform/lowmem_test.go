package platform

import (
	"context"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

func TestTuneMemoryKeepsTheEnvironmentsChoice(t *testing.T) {
	procs, gc := runtime.GOMAXPROCS(0), debug.SetGCPercent(100)
	t.Cleanup(func() {
		runtime.GOMAXPROCS(procs)
		debug.SetGCPercent(gc)
	})

	t.Setenv("GOMAXPROCS", "")
	t.Setenv("GOGC", "")
	TuneMemory()
	if got, want := runtime.GOMAXPROCS(0), min(runtime.NumCPU(), lowMemMaxProcs); got != want {
		t.Errorf("GOMAXPROCS = %d, want %d", got, want)
	}
	if got := debug.SetGCPercent(100); got != lowMemGCPercent {
		t.Errorf("GC percent = %d, want %d", got, lowMemGCPercent)
	}

	runtime.GOMAXPROCS(7)
	t.Setenv("GOMAXPROCS", "7")
	t.Setenv("GOGC", "80")
	debug.SetGCPercent(80)
	TuneMemory()
	if got := runtime.GOMAXPROCS(0); got != 7 {
		t.Errorf("GOMAXPROCS = %d, want the environment's 7", got)
	}
	if got := debug.SetGCPercent(80); got != 80 {
		t.Errorf("GC percent = %d, want the environment's 80", got)
	}
}

// fakeIdle is a process for idleRelease: busy answers, an allocation counter and the
// releases and trims made, in order.
type fakeIdle struct {
	busy  bool
	alloc uint64
	log   []string
}

func (f *fakeIdle) release() *idleRelease {
	return &idleRelease{
		busy:      func() bool { return f.busy },
		allocated: func() uint64 { return f.alloc },
		release:   func() { f.log = append(f.log, "release") },
		trim:      func() { f.log = append(f.log, "trim") },
	}
}

// took returns what was done since the last call.
func (f *fakeIdle) took() string {
	s := strings.Join(f.log, "+")
	f.log = nil
	return s
}

// An idle process is released only after it allocated a mebibyte, and trimmed only on the
// tick it became idle (or the start-up tick); a busy one is never touched.
func TestIdleReleaseTicks(t *testing.T) {
	f := &fakeIdle{alloc: 8 << 20} // start-up's allocations
	r := f.release()
	steps := []struct {
		name    string
		startup bool
		busy    bool
		alloc   uint64 // allocated since the step before
		want    string
	}{
		{name: "first idle tick after start", alloc: 0, want: "release"},
		{name: "idle, nothing allocated", want: ""},
		{name: "idle, a little allocated", alloc: releaseMinAlloc - 1, want: ""},
		{name: "idle, the little adds up", alloc: 1, want: "release"},
		{name: "a download runs", busy: true, alloc: 64 << 20, want: ""},
		{name: "still running", busy: true, alloc: 64 << 20, want: ""},
		{name: "just idle", want: "release+trim"},
		{name: "idle again, nothing allocated", want: ""},
		{name: "a client's calls while idle", alloc: 2 << 20, want: "release"},
		{name: "a tiny download", busy: true, alloc: 4096, want: ""},
		{name: "just idle, nothing worth it", want: ""},
		{name: "idle after it: no late trim", alloc: 2 << 20, want: "release"},
	}
	for _, s := range steps {
		f.busy = s.busy
		f.alloc += s.alloc
		r.tick(s.startup)
		if got := f.took(); got != s.want {
			t.Errorf("%s: did %q, want %q", s.name, got, s.want)
		}
	}
}

// The start-up tick trims an idle process; a busy one is left to its becoming idle.
func TestIdleReleaseStartupTrim(t *testing.T) {
	f := &fakeIdle{alloc: 8 << 20}
	r := f.release()
	r.tick(true)
	if got := f.took(); got != "release+trim" {
		t.Errorf("idle at the start-up tick: did %q, want release+trim", got)
	}

	f = &fakeIdle{alloc: 8 << 20, busy: true}
	r = f.release()
	r.tick(true)
	if got := f.took(); got != "" {
		t.Errorf("busy at the start-up tick: did %q, want nothing", got)
	}
	f.busy = false
	r.tick(false)
	if got := f.took(); got != "release+trim" {
		t.Errorf("idle after a busy start-up tick: did %q, want release+trim", got)
	}
}

// run handles the start-up tick once and every tick in order, and returns when its context
// ends; the ticks are channels, so no clock is involved.
func TestIdleReleaseRun(t *testing.T) {
	f := &fakeIdle{alloc: 8 << 20}
	var answers []bool // busy's answers in order, for the steps below
	r := f.release()
	r.busy = func() bool {
		b := answers[0]
		answers = answers[1:]
		if !b {
			f.alloc += 2 << 20 // a client's calls between ticks
		}
		return b
	}
	ticks, startup := make(chan time.Time), make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	answers = []bool{false, true, false, false}
	go func() { r.run(ctx, ticks, startup); close(done) }()

	startup <- time.Time{} // idle: release+trim
	ticks <- time.Time{}   // busy
	ticks <- time.Time{}   // just idle: release+trim
	ticks <- time.Time{}   // idle: release
	select {
	case startup <- time.Time{}:
		t.Fatal("a second start-up tick was taken")
	default:
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after its context ended")
	}
	if got, want := f.took(), "release+trim+release+trim+release"; got != want {
		t.Fatalf("did %q, want %q", got, want)
	}
}

// The real loop ends with its context, and the runtime's allocation counter grows.
func TestReleaseMemoryWhileIdleStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		ReleaseMemoryAndTrimWhileIdle(ctx, time.Hour, time.Hour, func() bool { return false })
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ReleaseMemoryAndTrimWhileIdle did not return after its context ended")
	}

	allocated := heapAllocCounter()
	before := allocated()
	sink = make([]byte, 2<<20)
	if after := allocated(); after-before < 2<<20 {
		t.Fatalf("allocated %d bytes after a 2 MiB allocation, want at least that", after-before)
	}
	sink = nil
}

// sink keeps TestReleaseMemoryWhileIdleStops's allocation on the heap.
var sink []byte

// TrimWorkingSet succeeds on this process (on Windows the real call; elsewhere nothing).
func TestTrimWorkingSet(t *testing.T) {
	if err := TrimWorkingSet(); err != nil {
		t.Fatalf("TrimWorkingSet: %v", err)
	}
}
