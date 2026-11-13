package engine

import (
	"math"
	"testing"
	"time"
)

func TestConnBudget(t *testing.T) {
	for _, tc := range []struct {
		remaining int64
		perHost   int
		want      int
	}{
		{0, 8, 1}, {10_239, 8, 1}, {10_240, 8, 1}, {20_479, 8, 1}, {20_480, 8, 2},
		{81_920, 8, 8}, {1 << 30, 8, 8}, {1 << 30, 32, 32}, {327_680, 32, 32}, {327_679, 32, 31},
		{1 << 30, 0, 1}, {1 << 30, 1, 1},
	} {
		if got := connBudget(tc.remaining, tc.perHost); got != tc.want {
			t.Errorf("connBudget(%d, %d) = %d, want %d", tc.remaining, tc.perHost, got, tc.want)
		}
	}
}

// TestRampOpensBudget checks §3: the first step opens the whole budget at once, as soon
// as its gates allow it; a later step (a grown budget) waits for the ramp interval.
func TestRampOpensBudget(t *testing.T) {
	clk := NewFakeClock(time.Unix(1_000, 0))
	r := newRamp(time.Second)
	if r.target != 1 {
		t.Fatalf("initial target %d", r.target)
	}
	if !r.step(clk.Now(), 32, true, true, false) || r.target != 32 {
		t.Fatalf("first step: target %d, want the budget 32", r.target)
	}
	if r.step(clk.Now(), 32, true, true, false) {
		t.Fatal("stepped again at the budget")
	}
	clk.Advance(999 * time.Millisecond)
	if r.step(clk.Now(), 40, true, true, false) || r.target != 32 {
		t.Fatalf("grew to %d within the interval", r.target)
	}
	clk.Advance(time.Millisecond)
	if !r.step(clk.Now(), 40, true, true, false) || r.target != 40 {
		t.Fatalf("target %d after the interval, want 40", r.target)
	}
}

// TestRampDoublesUnderCap checks §3: while a speed cap is active a step only doubles
// the target (1→2→4…), at most once per interval, so the 85% rule can stop it.
func TestRampDoublesUnderCap(t *testing.T) {
	t0 := time.Unix(0, 0)
	r := newRamp(time.Second)
	for i, want := range []int{2, 2, 4, 4, 8, 8} {
		at := t0.Add(time.Duration(i) * 600 * time.Millisecond)
		r.step(at, 8, true, true, true)
		if r.target != want {
			t.Fatalf("step %d at %v: target %d, want %d", i, at.Sub(t0), r.target, want)
		}
	}
}

// TestRampLowerDoubles checks §3/§10: after the server pushed back (lower), the target
// grows back by doubling, gated like any later step, instead of jumping to the budget.
func TestRampLowerDoubles(t *testing.T) {
	t0 := time.Unix(0, 0)
	r := newRamp(time.Second)
	r.step(t0, 32, true, true, false) // the first step: 1 → 32
	r.lower(4)
	if r.target != 4 {
		t.Fatalf("target %d after lower(4)", r.target)
	}
	if r.step(t0.Add(5*time.Second), 32, false, true, false) || r.target != 4 {
		t.Fatalf("grew to %d although a worker has received nothing", r.target)
	}
	for _, st := range []struct {
		at   time.Duration
		want int
	}{{5 * time.Second, 8}, {5500 * time.Millisecond, 8}, {6 * time.Second, 16}, {7 * time.Second, 32}, {8 * time.Second, 32}} {
		r.step(t0.Add(st.at), 32, true, true, false)
		if r.target != st.want {
			t.Fatalf("at %v: target %d, want %d", st.at, r.target, st.want)
		}
	}
	r.lower(64) // never raises the target
	if r.target != 32 {
		t.Fatalf("lower(64) changed the target to %d", r.target)
	}
}

func TestRampGates(t *testing.T) {
	t0 := time.Unix(0, 0)
	r := newRamp(time.Second)
	if r.step(t0.Add(5*time.Second), 8, false, true, false) || r.target != 1 {
		t.Fatal("stepped although a worker has received nothing")
	}
	if r.step(t0.Add(5*time.Second), 8, true, false, false) || r.target != 1 {
		t.Fatal("stepped although ramp-up is disabled")
	}
	if !r.step(t0.Add(5*time.Second), 8, true, true, false) || r.target != 8 {
		t.Fatalf("target %d, want 8", r.target)
	}
	// A shrunken budget does not lower the target (allowed() takes the minimum).
	if r.step(t0.Add(10*time.Second), 3, true, true, false) || r.target != 8 {
		t.Fatalf("target %d", r.target)
	}
}

// TestRampWorthwhile checks the worth-it guard of §3: no doubling when the download
// would finish within max(500 ms, 4·RTT) at its current speed.
func TestRampWorthwhile(t *testing.T) {
	if rampMinETA != 500*time.Millisecond {
		t.Fatalf("rampMinETA = %v, want 500 ms", rampMinETA)
	}
	const mib = 1 << 20
	for _, tc := range []struct {
		name string
		rem  int64
		rate float64
		rtt  time.Duration
		want bool
	}{
		{"unknown speed", 1, 0, 0, true},
		{"negative speed is unknown", 1, -1, time.Second, true},
		{"below 500 ms", 4 * mib, 10 * mib, 0, false},
		{"exactly 500 ms", 5 * mib, 10 * mib, 0, false},
		{"just above 500 ms", 5*mib + 1, 10 * mib, 0, true},
		{"short RTT does not lower the floor", 4 * mib, 10 * mib, 100 * time.Millisecond, false},
		{"4·RTT wins over 500 ms", 10 * mib, 10 * mib, 300 * time.Millisecond, false},
		{"above 4·RTT", 13 * mib, 10 * mib, 300 * time.Millisecond, true},
		{"nothing left", 0, 10 * mib, 0, false},
		{"slow download", 1 << 30, 1000, 0, true},
	} {
		if got := rampWorthwhile(tc.rem, tc.rate, tc.rtt); got != tc.want {
			t.Errorf("%s: rampWorthwhile(%d, %v, %v) = %v, want %v", tc.name, tc.rem, tc.rate, tc.rtt, got, tc.want)
		}
	}
}

func TestRampEnabled(t *testing.T) {
	for _, tc := range []struct {
		single bool
		cap    int64
		peak   float64
		want   bool
	}{
		{true, 0, 0, false}, {false, 0, 1e9, true}, {false, 1000, 849.9, true},
		{false, 1000, 850, false}, {false, 1000, 2000, false},
	} {
		if got := rampEnabled(tc.single, tc.cap, tc.peak); got != tc.want {
			t.Errorf("rampEnabled(%v, %d, %v) = %v, want %v", tc.single, tc.cap, tc.peak, got, tc.want)
		}
	}
}

// TestSpeedMeterWaitsForBytes checks that the decision speed starts with the first
// sample that saw bytes: ticks without bytes before it (a connect, a server's first-byte
// delay) are not averaged in, so the worth-it guard of ramp-up sees the real speed.
func TestSpeedMeterWaitsForBytes(t *testing.T) {
	clk := NewFakeClock(time.Unix(0, 0))
	var m speedMeter
	m.reset(clk.Now(), 0)
	for range 3 {
		clk.Advance(tickInterval)
		m.sample(clk.Now(), 0)
		if m.primed {
			t.Fatalf("primed at %v without bytes (rate %v)", clk.Now().Sub(time.Unix(0, 0)), m.rate)
		}
	}
	clk.Advance(tickInterval)
	m.sample(clk.Now(), 1_000_000) // 1 MB in the 100 ms after the last empty tick
	if !m.primed || math.Abs(m.rate-10e6) > 1 {
		t.Fatalf("first speed %v (primed %v), want 10 MB/s", m.rate, m.primed)
	}
}

func TestSpeedEWMA(t *testing.T) {
	clk := NewFakeClock(time.Unix(0, 0))
	var m speedMeter
	m.reset(clk.Now(), 0)
	var total int64
	for range 100 { // 1 MB/s for 10 s, sampled every 100 ms
		clk.Advance(tickInterval)
		total += 100_000
		m.sample(clk.Now(), total)
	}
	if math.Abs(m.rate-1e6) > 1 {
		t.Fatalf("steady rate %v, want 1e6", m.rate)
	}
	// After the transfer stops the average decays with time constant 1 s.
	for range 10 {
		clk.Advance(tickInterval)
		m.sample(clk.Now(), total)
	}
	if want := 1e6 * math.Exp(-1); math.Abs(m.rate-want) > 1 {
		t.Fatalf("decayed rate %v, want %v", m.rate, want)
	}
	if m.peak < 1e6-1 {
		t.Fatalf("peak %v", m.peak)
	}
	m.sample(clk.Now(), total) // zero interval is ignored
}
