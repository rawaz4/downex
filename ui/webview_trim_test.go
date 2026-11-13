package main

import (
	"testing"
	"time"
)

func TestTrimPolicy(t *testing.T) {
	t0 := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	at := func(s float64) time.Time { return t0.Add(time.Duration(s * float64(time.Second))) }
	type sample struct {
		at          float64
		inUse       bool
		low, change bool
	}
	for _, tc := range []struct {
		name    string
		samples []sample
	}{
		{"in use stays normal", []sample{{0, true, false, false}, {30, true, false, false}, {200, true, false, false}}},
		{"out of use pulses low after trimAfter for trimHold", []sample{
			{0, false, false, false}, {9, false, false, false}, {10, false, true, true}, {12, false, true, false},
			{13, false, false, true}, {14, false, false, false}, {69, false, false, false},
		}},
		{"the pulse repeats every retrimEvery while out of use", []sample{
			{0, false, false, false}, {10, false, true, true}, {13, false, false, true},
			{70, false, true, true}, {73, false, false, true}, {130, false, true, true},
		}},
		{"back in use ends a pulse at once", []sample{
			{0, false, false, false}, {10, false, true, true}, {11, true, false, true}, {12, true, false, false},
		}},
		{"back in use restarts the wait", []sample{
			{0, false, false, false}, {10, false, true, true}, {13, false, false, true}, {20, true, false, false},
			{21, false, false, false}, {30, false, false, false}, {31, false, true, true},
		}},
		{"a short absence does not trim", []sample{
			{0, false, false, false}, {8, true, false, false}, {9, false, false, false}, {18, false, false, false}, {19, false, true, true},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var p trimPolicy
			for _, s := range tc.samples {
				low, change := p.step(at(s.at), s.inUse)
				if low != s.low || change != s.change {
					t.Fatalf("at %v s in use %v: (low %v, change %v), want (%v, %v)", s.at, s.inUse, low, change, s.low, s.change)
				}
			}
		})
	}
}
