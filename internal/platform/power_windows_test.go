package platform

import (
	"testing"
	"time"
)

// The privilege lookup and the wake timer are safe to exercise: a wake timer armed far
// in the future and cancelled at once never wakes anything.
func TestShutdownPrivilegeLookup(t *testing.T) {
	if _, err := shutdownPrivilege(); err != nil {
		t.Fatal(err)
	}
}

// A power request only keeps the computer from idling into sleep while it is held; one
// held for a moment changes nothing.
func TestAwakeRequestHoldRelease(t *testing.T) {
	a, err := NewAwakeRequest("Downex test: power request")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for range 2 {
		if err := a.Hold(); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := a.Release(); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Hold(); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil { // releases it
		t.Fatal(err)
	}
	if err := a.Hold(); err == nil {
		t.Fatal("Hold after Close succeeded")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWakeTimerArmCancel(t *testing.T) {
	w, err := NewWakeTimer()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := w.WakeAt(time.Now().Add(365 * 24 * time.Hour)); err != nil {
		t.Logf("WakeAt: %v (wake timers may be unsupported here)", err)
	}
	if err := w.Cancel(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.WakeAt(time.Now().Add(time.Hour)); err == nil {
		t.Fatal("WakeAt after Close succeeded")
	}
}
