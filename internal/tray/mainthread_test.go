package tray

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

// fakeKit plays AppKit for an appLoop: run blocks until stop (or, after
// replyTerminate, until the fake exit "ends the process").
type fakeKit struct {
	main     bool
	startErr error
	// onReply emulates AppKit after replyTerminate: it calls willTerminate.
	onReply func()
	// stops ends run; stop and the fake exit put a token in it.
	stops     chan struct{}
	stopWorks bool

	mu    sync.Mutex
	calls []string
}

func newFakeKit() *fakeKit {
	return &fakeKit{main: true, stops: make(chan struct{}, 4), stopWorks: true}
}

func (k *fakeKit) record(s string) { k.mu.Lock(); k.calls = append(k.calls, s); k.mu.Unlock() }

func (k *fakeKit) took() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return slices.Clone(k.calls)
}

func (k *fakeKit) mainThread() bool { return k.main }

func (k *fakeKit) start() error { k.record("start"); return k.startErr }

func (k *fakeKit) run() { k.record("run"); <-k.stops }

func (k *fakeKit) stop() {
	k.record("stop")
	if k.stopWorks {
		k.stops <- struct{}{}
	}
}

func (k *fakeKit) replyTerminate() {
	k.record("reply")
	if k.onReply != nil {
		go k.onReply()
	}
}

// fakeExit records exit statuses; like os.Exit it ends the event loop.
type fakeExit struct {
	kit   *fakeKit
	mu    sync.Mutex
	codes []int
}

func (e *fakeExit) exit(code int) {
	e.mu.Lock()
	e.codes = append(e.codes, code)
	e.mu.Unlock()
	select {
	case e.kit.stops <- struct{}{}:
	default:
	}
}

func (e *fakeExit) took() []int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.codes)
}

func newTestLoop(k *fakeKit) (*appLoop, *fakeExit) {
	e := &fakeExit{kit: k}
	a := newAppLoop(k, e.exit)
	a.grace = time.Hour // only the fallback test lets the timer fire
	return a, e
}

// runLoop runs a.run(f) on a goroutine (the "main thread") and returns its result.
func runLoop(t *testing.T, a *appLoop, f func() int) int {
	t.Helper()
	got := make(chan int, 1)
	go func() { got <- a.run(f) }()
	select {
	case code := <-got:
		return code
	case <-time.After(20 * time.Second):
		t.Fatal("RunMain did not return")
		return -1
	}
}

func TestAppLoopWithoutTray(t *testing.T) {
	k := newFakeKit()
	a, e := newTestLoop(k)
	if code := runLoop(t, a, func() int { return 7 }); code != 7 {
		t.Fatalf("RunMain = %d, want 7", code)
	}
	if calls := k.took(); len(calls) != 0 {
		t.Errorf("AppKit was touched without a tray: %q", calls)
	}
	if err := a.ensure(); !errors.Is(err, errAppEnded) {
		t.Errorf("ensure after the program returned: %v", err)
	}
	if codes := e.took(); len(codes) != 0 {
		t.Errorf("exit called: %v", codes)
	}
}

func TestAppLoopOffTheMainThread(t *testing.T) {
	k := newFakeKit()
	k.main = false
	a, _ := newTestLoop(k)
	var err error
	if code := runLoop(t, a, func() int { err = a.ensure(); return 4 }); code != 4 {
		t.Fatalf("RunMain = %d", code)
	}
	if !errors.Is(err, errMainNotServed) {
		t.Errorf("ensure off the main thread: %v", err)
	}
	if calls := k.took(); len(calls) != 0 {
		t.Errorf("AppKit calls: %q", calls)
	}
	// Without RunMain at all.
	b, _ := newTestLoop(newFakeKit())
	if err := b.ensure(); !errors.Is(err, errMainNotServed) {
		t.Errorf("ensure without RunMain: %v", err)
	}
}

func TestAppLoopStartsAppKitOnce(t *testing.T) {
	k := newFakeKit()
	a, e := newTestLoop(k)
	code := runLoop(t, a, func() int {
		var wg sync.WaitGroup
		errs := make([]error, 8)
		for i := range errs {
			wg.Go(func() { errs[i] = a.ensure() })
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Errorf("ensure %d: %v", i, err)
			}
		}
		if err := a.ensure(); err != nil {
			t.Errorf("ensure once running: %v", err)
		}
		return 3
	})
	if code != 3 {
		t.Fatalf("RunMain = %d, want 3", code)
	}
	// The event loop may start after the program already asked it to stop.
	if calls := slices.Sorted(slices.Values(k.took())); !slices.Equal(calls, []string{"run", "start", "stop"}) {
		t.Errorf("AppKit calls %q, want start, run, stop", calls)
	}
	if codes := e.took(); len(codes) != 0 {
		t.Errorf("exit called although the main thread came back: %v", codes)
	}
}

func TestAppLoopStartFailure(t *testing.T) {
	k := newFakeKit()
	k.startErr = ErrNotSupported
	a, _ := newTestLoop(k)
	var first, second error
	code := runLoop(t, a, func() int {
		first = a.ensure()
		second = a.ensure()
		return 2
	})
	if code != 2 {
		t.Fatalf("RunMain = %d", code)
	}
	if !errors.Is(first, ErrNotSupported) || !errors.Is(second, ErrNotSupported) {
		t.Errorf("ensure errors %v, %v; want ErrNotSupported twice", first, second)
	}
	if calls := k.took(); !slices.Equal(calls, []string{"start"}) {
		t.Errorf("AppKit calls %q, want one start and no event loop", calls)
	}
}

// The system asks the app to quit while the daemon runs: the termination waits, the
// daemon is asked to stop (CommandQuit), and the process then ends with its status.
func TestAppLoopTerminationWaitsForTheProgram(t *testing.T) {
	k := newFakeKit()
	a, e := newTestLoop(k)
	k.onReply = a.willTerminate // AppKit, once the termination may proceed
	quit := make(chan struct{})
	var commands []Command
	var mu sync.Mutex
	code := runLoop(t, a, func() int {
		if err := a.ensure(); err != nil {
			t.Error(err)
			return 1
		}
		remove := a.setHandler(func(c Command) {
			mu.Lock()
			commands = append(commands, c)
			mu.Unlock()
			if c == CommandQuit {
				close(quit)
			}
		})
		defer remove()
		if now := a.shouldTerminate(); now {
			t.Error("shouldTerminate let AppKit terminate while the daemon runs")
		}
		<-quit // the graceful shutdown
		return 5
	})
	if code != 5 {
		t.Fatalf("RunMain = %d, want 5", code)
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(e.took()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if codes := e.took(); !slices.Equal(codes, []int{5}) {
		t.Errorf("exit statuses %v, want [5] (from willTerminate)", codes)
	}
	if calls := slices.Sorted(slices.Values(k.took())); !slices.Equal(calls, []string{"reply", "run", "start"}) {
		t.Errorf("AppKit calls %q, want start, run, reply", calls)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(commands, []Command{CommandQuit}) {
		t.Errorf("commands %v, want [quit]", commands)
	}
	if !a.shouldTerminate() {
		t.Error("shouldTerminate after the program returned postpones")
	}
}

func TestAppLoopExitsWhenTheMainThreadDoesNotReturn(t *testing.T) {
	k := newFakeKit()
	k.stopWorks = false // the event loop ignores stop
	a, e := newTestLoop(k)
	a.grace = 30 * time.Millisecond
	code := runLoop(t, a, func() int {
		if err := a.ensure(); err != nil {
			t.Error(err)
		}
		return 6
	})
	// The fake exit ended the event loop, so RunMain returned too.
	if codes := e.took(); !slices.Equal(codes, []int{6}) {
		t.Errorf("exit statuses %v, want [6]", codes)
	}
	if code != 6 {
		t.Errorf("RunMain = %d", code)
	}
}

func TestAppLoopRoutesToTheLatestHandler(t *testing.T) {
	a, _ := newTestLoop(newFakeKit())
	var got []string
	removeA := a.setHandler(func(c Command) { got = append(got, "a "+c.String()) })
	a.route(CommandOpen)
	removeB := a.setHandler(func(c Command) { got = append(got, "b "+c.String()) })
	a.route(CommandOpen)
	removeA() // replaced already: b stays
	a.route(CommandOpen)
	removeB()
	a.route(CommandOpen)
	if want := []string{"a open", "b open", "b open"}; !slices.Equal(got, want) {
		t.Errorf("routed %q, want %q", got, want)
	}

	// A click before any handler (the one that launched the app) reaches the next
	// handler, once; a quit request without a handler is not kept.
	got = nil
	a.route(CommandOpen)
	a.route(CommandOpen)
	a.route(CommandQuit)
	removeC := a.setHandler(func(c Command) { got = append(got, "c "+c.String()) })
	removeC()
	a.setHandler(func(c Command) { got = append(got, "d "+c.String()) })()
	if want := []string{"c open"}; !slices.Equal(got, want) {
		t.Errorf("early clicks routed %q, want %q", got, want)
	}
}
