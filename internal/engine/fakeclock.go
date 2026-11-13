package engine

import (
	"sync"
	"time"
)

// FakeClock is a manually advanced Clock for deterministic tests. Timers and tickers
// fire only from Advance. It is safe for concurrent use.
type FakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer // armed timers and tickers
}

// NewFakeClock returns a FakeClock whose current time is start.
func NewFakeClock(start time.Time) *FakeClock { return &FakeClock{now: start} }

// Now returns the fake current time.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// NewTimer returns a timer that fires once the clock has advanced by d.
func (c *FakeClock) NewTimer(d time.Duration) Timer { return c.arm(d, 0) }

// NewTicker returns a ticker with period d (non-positive periods become 1 ns).
func (c *FakeClock) NewTicker(d time.Duration) Ticker {
	if d <= 0 {
		d = time.Nanosecond
	}
	return fakeTicker{c.arm(d, d)}
}

type fakeTicker struct{ t *fakeTimer }

func (k fakeTicker) C() <-chan time.Time { return k.t.ch }
func (k fakeTicker) Stop()               { k.t.Stop() }

// Pending reports how many timers and tickers are armed. Tests use it to wait
// until goroutines under test have reached a timed wait.
func (c *FakeClock) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}

// Advance moves the clock forward by d, firing due timers in deadline order.
// Like real tickers, a ticker whose previous tick is still unread drops ticks.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	end := c.now.Add(d)
	for {
		var next *fakeTimer
		for _, t := range c.timers {
			if !t.when.After(end) && (next == nil || t.when.Before(next.when)) {
				next = t
			}
		}
		if next == nil {
			break
		}
		if next.when.After(c.now) {
			c.now = next.when
		}
		select {
		case next.ch <- c.now:
		default:
		}
		if next.period > 0 {
			next.when = next.when.Add(next.period)
		} else {
			c.disarmLocked(next)
		}
	}
	c.now = end
}

func (c *FakeClock) arm(d, period time.Duration) *fakeTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{clock: c, ch: make(chan time.Time, 1), period: period}
	c.resetLocked(t, d)
	return t
}

// resetLocked arms t to fire d after now and reports whether it was armed before.
func (c *FakeClock) resetLocked(t *fakeTimer, d time.Duration) bool {
	was := c.disarmLocked(t)
	select { // Go 1.23+ semantics: no stale tick survives Stop/Reset.
	case <-t.ch:
	default:
	}
	t.when = c.now.Add(d)
	if d <= 0 && t.period == 0 {
		t.ch <- c.now
		return was
	}
	c.timers = append(c.timers, t)
	return was
}

func (c *FakeClock) disarmLocked(t *fakeTimer) bool {
	for i, x := range c.timers {
		if x == t {
			c.timers = append(c.timers[:i], c.timers[i+1:]...)
			return true
		}
	}
	return false
}

type fakeTimer struct {
	clock  *FakeClock
	ch     chan time.Time
	when   time.Time
	period time.Duration // 0 for one-shot timers
}

func (t *fakeTimer) C() <-chan time.Time { return t.ch }

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	was := t.clock.disarmLocked(t)
	select {
	case <-t.ch:
	default:
	}
	return was
}

func (t *fakeTimer) Reset(d time.Duration) bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	return t.clock.resetLocked(t, d)
}
