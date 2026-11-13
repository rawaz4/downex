package engine

import (
	"fmt"
	"testing"
	"time"

	"downex/internal/testserver"
)

func TestStallReassignment(t *testing.T) {
	t.Parallel()
	for _, conns := range []int{1, 4} {
		t.Run(fmt.Sprintf("conns=%d", conns), func(t *testing.T) {
			t.Parallel()
			opts := fastOpts()
			opts.StallTimeout = 300 * time.Millisecond
			rec := &recorder{t: t}
			opts.openFile = rec.open
			const size = 3 << 20
			h := newHarness(t, testserver.Options{Size: size, Seed: 61, ETag: `"s"`, StallAfter: 64 << 10, StallCount: 1}, opts)
			d := h.add("/stall.bin", "", conns)
			if err := h.run(d.ID, nil, nil); err != nil {
				t.Fatalf("Run: %v", err)
			}
			h.verify(d.ID)
			checkStallCut(t, h.srv, opts.StallTimeout, conns == 1)
			if spans := rec.file().writtenSpans(); len(spans) != 1 || spans[0] != (span{0, size}) {
				t.Fatalf("written spans %v", spans)
			}
			if n := len(h.srv.Requests()); n < 2 {
				t.Fatalf("%d requests; the stalled range was not reassigned", n)
			}
		})
	}
}

func TestRetryAfterOnProbe(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testserver.Options{Size: 300_000, Seed: 71, FailFirst: 1, FailStatus: 429, RetryAfter: "1"}, fastOpts())
	d := h.add("/busy.bin", "", 4)
	start := time.Now()
	if err := h.run(d.ID, nil, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	h.verify(d.ID)
	if el := time.Since(start); el < 900*time.Millisecond {
		t.Fatalf("finished after %v; Retry-After: 1 was not honoured", el)
	}
	if r := h.srv.Requests(); r[0].Status != 429 || r[1].Range != "bytes=0-" || r[1].Status != 206 {
		t.Fatalf("requests %+v", r[:2])
	}
}

// TestThrottledSegmentLowersHostCap: the probe succeeds and one request of the first
// ramp-up step (7 sent at once) gets 503 with Retry-After. The server served the other
// 7 connections, so the per-host limit drops to the live connections less the refused
// one (7; fewer only when a loaded machine lets a takeover end a connection first), the
// refused connection stops at once (its range goes to the others), and no more than that
// run from then on. Responses are paced at 1 MiB/s, so the download outlasts several
// progress events.
func TestThrottledSegmentLowersHostCap(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testserver.Options{Size: 4 << 20, Seed: 72, ETag: `"t"`, Throttle: 1 << 20,
		FailSkip: 1, FailFirst: 1, FailStatus: 503, RetryAfter: "1"}, fastOpts())
	d := h.add("/t.bin", "", 8)
	var log progressLog
	if err := h.run(d.ID, nil, log.on); err != nil {
		t.Fatalf("Run: %v", err)
	}
	h.verify(d.ID)
	reqs := h.srv.Requests()
	if len(reqs) < 2 || reqs[1].Status != 503 {
		t.Fatalf("requests %+v", reqs)
	}
	lim := h.eng.hostLimit(hostKey(h.srv.URL))
	if lim < 4 || lim > 7 {
		t.Fatalf("per-host limit after 503 = %d, want 7 (the 8 connections less the refused one)", lim)
	}
	events, _ := log.snapshot()
	dropped := false
	for i, p := range events {
		dropped = dropped || (p.Connections > 0 && p.Connections <= lim)
		if dropped && p.Connections > lim {
			t.Fatalf("event %d: %d connections after the per-host limit dropped to %d", i, p.Connections, lim)
		}
	}
	if !dropped || len(events) < 3 {
		t.Fatalf("%d events, limit seen %v", len(events), dropped)
	}
}

// checkStallCut checks that the one stalled response was given up promptly: its client
// went away less than the 5 s default mid-transfer timeout after its last byte, so the
// configured stallTimeout (300 ms or less here) applied. That gap is timed by the
// server, apart from what the rest of the run (writing, syncing and renaming the file)
// takes on a loaded machine. With exact, the response was cancelled for its stall, which
// happens only after stallTimeout without bytes; with more than one connection, another
// worker may take its range over sooner.
func checkStallCut(t *testing.T, srv *testserver.Server, stallTimeout time.Duration, exact bool) {
	t.Helper()
	stalls := srv.Stalls()
	if len(stalls) != 1 {
		t.Fatalf("%d responses stalled, want 1: the stall was not exercised", len(stalls))
	}
	// The server notices a client that went away a moment after it did.
	waitFor(t, 10*time.Second, "the server to see the stalled client go", func() bool {
		stalls = srv.Stalls()
		return !stalls[0].End.IsZero()
	})
	gap := stalls[0].End.Sub(stalls[0].Start)
	if exact && gap < stallTimeout {
		t.Fatalf("the stalled response was given up %v after its last byte, before the %v stall timeout", gap, stallTimeout)
	}
	if mid := (Options{}).withDefaults().MidStallTimeout; gap >= mid {
		t.Fatalf("the stalled response was given up %v after its last byte, not by the %v stall timeout", gap, stallTimeout)
	}
}
