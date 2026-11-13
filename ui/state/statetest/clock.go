// Package statetest holds the fixtures the ui/state tests and the views' tests share: a
// clock that tests advance, a scriptable daemon (testing.ts fakeApi), message builders
// (makeQueue, makeTools, makeConnection, makeTrack) and in-memory storages.
package statetest

import (
	"slices"
	"sync"
	"time"

	"downex/ui/state"
)

// Clock is a fake state.Clock: time moves only when Advance says (vi.useFakeTimers).
type Clock struct {
	mu     sync.Mutex
	now    time.Time
	seq    int
	timers []*timer
}

type timer struct {
	c   *Clock
	due time.Time
	seq int
	f   func()
}

// NewClock starts at ms milliseconds since the epoch.
func NewClock(ms int64) *Clock { return &Clock{now: time.UnixMilli(ms)} }

// Now implements state.Clock.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Set moves the clock to ms without running timers (a test that sets Date.now).
func (c *Clock) Set(ms int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = time.UnixMilli(ms)
}

// AfterFunc implements state.Clock.
func (c *Clock) AfterFunc(d time.Duration, f func()) state.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	t := &timer{c: c, due: c.now.Add(max(0, d)), seq: c.seq, f: f}
	c.timers = append(c.timers, t)
	return t
}

func (t *timer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	i := slices.Index(t.c.timers, t)
	if i < 0 {
		return false
	}
	t.c.timers = slices.Delete(t.c.timers, i, i+1)
	return true
}

// Pending is the number of timers set and not fired.
func (c *Clock) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}

// Advance moves the clock by d, running every timer that falls due, in order, on the
// calling goroutine (also those set by the timers it runs).
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var next *timer
		for _, t := range c.timers {
			if t.due.After(target) {
				continue
			}
			if next == nil || t.due.Before(next.due) || t.due.Equal(next.due) && t.seq < next.seq {
				next = t
			}
		}
		if next == nil {
			c.now = target
			c.mu.Unlock()
			return
		}
		c.timers = slices.DeleteFunc(c.timers, func(t *timer) bool { return t == next })
		if next.due.After(c.now) {
			c.now = next.due
		}
		c.mu.Unlock()
		next.f()
	}
}

// Frames is a state.Frames that runs nothing until Flush; Calls counts the frames asked
// for (requestAnimationFrame calls).
type Frames struct {
	mu      sync.Mutex
	Calls   int
	pending map[int]func()
	next    int
}

// Next implements state.Frames.
func (f *Frames) Next(fn func()) (cancel func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls++
	if f.pending == nil {
		f.pending = map[int]func(){}
	}
	id := f.next
	f.next++
	f.pending[id] = fn
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.pending, id)
	}
}

// Count is Calls, read safely.
func (f *Frames) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Calls
}

// Flush runs the frames asked for.
func (f *Frames) Flush() {
	f.mu.Lock()
	fns := make([]func(), 0, len(f.pending))
	for _, fn := range f.pending {
		fns = append(fns, fn)
	}
	clear(f.pending)
	f.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// Sync is a Go option that runs background work at once, on the caller's goroutine.
func Sync(f func()) { f() }
