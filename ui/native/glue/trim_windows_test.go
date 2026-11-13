package glue

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// The start-up trim runs once, after its delay, and not when the process closes first; the
// real call (SetProcessWorkingSetSizeEx with both sizes -1) succeeds on this process.
func TestStartupTrim(t *testing.T) {
	var n atomic.Int32
	old := trimWorkingSet
	t.Cleanup(func() { trimWorkingSet = old })
	trimWorkingSet = func() error { n.Add(1); return nil }
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	done := make(chan struct{})
	go func() { startupTrim(context.Background(), 30*time.Millisecond, log); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the trim did not run")
	}
	if n.Load() != 1 {
		t.Fatalf("%d trims, want 1", n.Load())
	}

	ctx, cancel := context.WithCancel(context.Background())
	done = make(chan struct{})
	go func() { startupTrim(ctx, time.Hour, log); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a closed process's trim did not end")
	}
	if n.Load() != 1 {
		t.Fatalf("%d trims after a close, want still 1", n.Load())
	}

	if err := old(); err != nil {
		t.Fatalf("SetProcessWorkingSetSizeEx: %v", err)
	}
}

// The first frame's trim runs once, FirstFrameTrimAfter after the first frame, and never in a
// process without the memory trims (tests, Config.NoMemoryTrim).
func TestFirstFrameTrim(t *testing.T) {
	var n atomic.Int32
	old := trimWorkingSet
	t.Cleanup(func() { trimWorkingSet = old })
	trimWorkingSet = func() error { n.Add(1); return nil }
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	(&Window{h: &Host{}}).FirstFrame() // no trims
	var none *Window
	none.FirstFrame()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &Window{h: &Host{trimCtx: ctx, trimLog: log}}
	start := time.Now()
	p.FirstFrame()
	p.FirstFrame() // a later frame: no second trim
	for n.Load() == 0 && time.Since(start) < 5*time.Second {
		time.Sleep(5 * time.Millisecond)
	}
	if took := time.Since(start); n.Load() == 0 || took < FirstFrameTrimAfter {
		t.Fatalf("%d trims after %v, want one after %v", n.Load(), took, FirstFrameTrimAfter)
	}
	time.Sleep(2 * FirstFrameTrimAfter)
	if n.Load() != 1 {
		t.Fatalf("%d trims, want 1", n.Load())
	}
}
