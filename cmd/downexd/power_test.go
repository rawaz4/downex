package main

import (
	"log/slog"
	"testing"
	"time"
)

type stubWake struct{ closed bool }

func (*stubWake) WakeAt(time.Time) error { return nil }
func (*stubWake) Cancel() error          { return nil }
func (w *stubWake) Close() error         { w.closed = true; return nil }

type stubAwake struct{ closed bool }

func (*stubAwake) Hold() error    { return nil }
func (*stubAwake) Release() error { return nil }
func (a *stubAwake) Close() error { a.closed = true; return nil }

// Only the user's own daemon gets the power actions, the wake timer and the keep-awake
// request: a daemon of tests (--db, --endpoint, $DOWNEX_ENDPOINT) never powers anything
// off or keeps it on. Nothing here calls a power action.
func TestPowerHooksOnlyForOwnDaemon(t *testing.T) {
	stub, awake := &stubWake{}, &stubAwake{}
	origWake, origAwake := newWakeTimer, newAwakeRequest
	newWakeTimer = func() (wakeTimer, error) { return stub, nil }
	newAwakeRequest = func() (awakeRequest, error) { return awake, nil }
	t.Cleanup(func() { newWakeTimer, newAwakeRequest = origWake, origAwake })
	log := slog.New(slog.DiscardHandler)

	t.Setenv("DOWNEX_ENDPOINT", "")
	for _, o := range []options{{db: "x.db"}, {endpoint: "pipe"}} {
		if p := powerHooks(o, log); p.power != nil || p.wake != nil || p.awake != nil {
			t.Fatalf("private daemon %+v got power hooks", o)
		}
	}
	t.Setenv("DOWNEX_ENDPOINT", "custom")
	if p := powerHooks(options{}, log); p.power != nil || p.wake != nil || p.awake != nil {
		t.Fatal("daemon on $DOWNEX_ENDPOINT got power hooks")
	}
	t.Setenv("DOWNEX_ENDPOINT", "")
	p := powerHooks(options{}, log)
	if _, ok := p.power.(systemPower); !ok || p.wake != stub || p.awake != awake {
		t.Fatalf("own daemon: power %T, wake %v, awake %v", p.power, p.wake, p.awake)
	}
	p.close()
	if !stub.closed || !awake.closed {
		t.Fatalf("closed: wake timer %v, keep-awake request %v", stub.closed, awake.closed)
	}
}
