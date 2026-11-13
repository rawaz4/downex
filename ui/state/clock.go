package state

import (
	"context"
	"sync"
	"time"

	"downex/ui/host"
)

// Clock is the time the stores read and the timers they set (the status checks, the
// countdowns' ends, the toasts' lifetimes). SystemClock is the real one; tests advance a
// fake one (statetest.Clock).
type Clock interface {
	Now() time.Time
	// AfterFunc calls f on a goroutine of its own once d has passed.
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is a timer AfterFunc set; Stop reports whether it stopped one that had not fired.
type Timer interface {
	Stop() bool
}

// SystemClock is the wall clock and time.AfterFunc.
var SystemClock Clock = systemClock{}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

func clockOr(c Clock) Clock {
	if c == nil {
		return SystemClock
	}
	return c
}

// millis is t as the window's numbers count time: milliseconds since the epoch.
func millis(t time.Time) float64 { return float64(t.UnixMilli()) }

// CallContext gives a store's own calls (status checks, reads after an event) their
// context: host.Session.CallContext in the window. DefaultCallContext bounds each call by
// host.CallTimeout.
type CallContext func() (context.Context, context.CancelFunc)

// DefaultCallContext is a context of host.CallTimeout.
func DefaultCallContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), host.CallTimeout)
}

func callOr(c CallContext) CallContext {
	if c == nil {
		return DefaultCallContext
	}
	return c
}

// Frames schedules work for the next frame (today's requestAnimationFrame). A store
// coalesces the answers of a bulk action into at most one change per frame.
type Frames interface {
	// Next calls f once before the next frame; cancel drops it if it has not run.
	Next(f func()) (cancel func())
}

// FrameInterval is the frame of TimerFrames (today's setTimeout fallback of 16 ms).
const FrameInterval = 16 * time.Millisecond

// TimerFrames is a Frames on a clock: f runs FrameInterval later.
type TimerFrames struct{ Clock Clock }

// Next implements Frames.
func (f TimerFrames) Next(fn func()) (cancel func()) {
	t := clockOr(f.Clock).AfterFunc(FrameInterval, fn)
	return func() { t.Stop() }
}

// Signal tells subscribers that something changed. Notify calls them on the notifying
// goroutine, outside every store lock; a subscriber posts a repaint and returns.
type Signal struct {
	mu   sync.Mutex
	next int
	subs map[int]func()
}

// Subscribe adds f; cancel removes it.
func (s *Signal) Subscribe(f func()) (cancel func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subs == nil {
		s.subs = map[int]func(){}
	}
	id := s.next
	s.next++
	s.subs[id] = f
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.subs, id)
	}
}

// Notify calls every subscriber.
func (s *Signal) Notify() {
	s.mu.Lock()
	subs := make([]func(), 0, len(s.subs))
	for _, f := range s.subs {
		subs = append(subs, f)
	}
	s.mu.Unlock()
	for _, f := range subs {
		f()
	}
}
