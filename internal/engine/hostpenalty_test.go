package engine

import (
	"testing"
	"time"
)

// HostPenalized reports an active §10 penalty by host name, with or without the port of
// the download's URL, and not once it expired.
func TestHostPenalized(t *testing.T) {
	clock := NewFakeClock(time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC))
	e := New(nil, nil, Options{Clock: clock, HostPenaltyTTL: time.Minute})
	e.penalizeHost("cdn.example.com", 2)
	e.penalizeHost("files.example.org:8443", 1)
	e.penalizeHost("[2001:db8::1]:443", 1)
	for _, h := range []string{"cdn.example.com", "CDN.example.com", "files.example.org", "2001:db8::1"} {
		if !e.HostPenalized(h) {
			t.Errorf("%s is not penalized", h)
		}
	}
	if e.HostPenalized("example.com") {
		t.Error("an unpenalized host is")
	}
	clock.Advance(time.Minute + time.Second)
	if e.HostPenalized("cdn.example.com") {
		t.Error("an expired penalty still counts")
	}
}
