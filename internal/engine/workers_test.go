package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"downex/internal/model"
	"downex/internal/testserver"
)

// progressLog records progress events with the time each arrived.
type progressLog struct {
	mu     sync.Mutex
	events []Progress
	at     []time.Time
}

func (l *progressLog) on(p Progress) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, p)
	l.at = append(l.at, time.Now())
}

func (l *progressLog) len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.events)
}

func (l *progressLog) snapshot() ([]Progress, []time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Progress(nil), l.events...), append([]time.Time(nil), l.at...)
}

func (l *progressLog) last() (Progress, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.events) == 0 {
		return Progress{}, false
	}
	return l.events[len(l.events)-1], true
}

// slot returns the latest view of a connection slot.
func (l *progressLog) slot(n int) (model.WorkerView, bool) {
	p, ok := l.last()
	if !ok {
		return model.WorkerView{}, false
	}
	return findSlot(p, n)
}

func findSlot(p Progress, n int) (model.WorkerView, bool) {
	for _, v := range p.Workers {
		if v.Slot == n {
			return v, true
		}
	}
	return model.WorkerView{}, false
}

// active counts the listed connections that are neither done nor failed.
func active(p Progress) int {
	n := 0
	for _, v := range p.Workers {
		if v.State != model.WorkerDone && v.State != model.WorkerError {
			n++
		}
	}
	return n
}

// checkViews checks invariants every event must keep: slots ordered and unique,
// fragment -1, no URL in a detail, RetryAt only while retrying.
func checkViews(t *testing.T, events []Progress, host string) {
	t.Helper()
	for i, p := range events {
		prev := 0
		for _, v := range p.Workers {
			if v.Slot <= prev {
				t.Fatalf("event %d: slots out of order: %+v", i, p.Workers)
			}
			prev = v.Slot
			if v.Fragment != -1 {
				t.Fatalf("event %d: fragment %d for a file download", i, v.Fragment)
			}
			if strings.Contains(v.Detail, "http") || strings.Contains(v.Detail, host) {
				t.Fatalf("event %d: detail %q carries a URL", i, v.Detail)
			}
			if (v.State == model.WorkerRetrying) != !v.RetryAt.IsZero() {
				t.Fatalf("event %d: state %v with RetryAt %v", i, v.State, v.RetryAt)
			}
		}
	}
}

// TestWorkerViewsLifecycle follows the second connection of a two-connection download
// through connecting (its dial is held), requesting (the response head is held),
// receiving (a throttled body) and done.
func TestWorkerViewsLifecycle(t *testing.T) {
	t.Parallel()
	const size = 2 << 20
	srv := testserver.New(testserver.Options{Size: size, Seed: 91, ETag: `"w"`, Throttle: 512 << 10})
	t.Cleanup(srv.Close)
	headGate, dialGate := make(chan struct{}), make(chan struct{})
	var openHead, openDial sync.Once
	releaseHeads := func() { openHead.Do(func() { close(headGate) }) }
	releaseDials := func() { openDial.Do(func() { close(dialGate) }) }
	t.Cleanup(releaseHeads)
	t.Cleanup(releaseDials)

	var reqs atomic.Int32
	inner := srv.Config.Handler
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reqs.Add(1) > 1 { // every request after the probe waits for the gate
			select {
			case <-headGate:
			case <-r.Context().Done():
				return
			}
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(front.Close)

	var dials atomic.Int32
	fac := &httpFactory{newTransport: func() *http.Transport {
		var d net.Dialer
		return &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if dials.Add(1) > 1 { // every dial after the probe's waits for the gate
				select {
				case <-dialGate:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return d.DialContext(ctx, network, addr)
		}}
	}}
	st := newMemStore()
	eng := New(st, fac, fastOpts())
	d, err := eng.Add(context.Background(), model.Request{URL: front.URL + "/views.bin"}, t.TempDir(), "", 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := runContext(t)
	defer cancel()
	var log progressLog
	done := make(chan error, 1)
	go func() { done <- eng.Run(ctx, d.ID, nil, log.on) }()

	waitState := func(want model.WorkerState, extra func(model.WorkerView) bool) {
		t.Helper()
		waitFor(t, 10*time.Second, fmt.Sprintf("slot 2 in state %d", want), func() bool {
			v, ok := log.slot(2)
			return ok && v.State == want && (extra == nil || extra(v))
		})
	}
	waitState(model.WorkerConnecting, nil)
	if v, _ := log.slot(1); v.State != model.WorkerReceiving || v.Received <= 0 || v.Start != 0 {
		t.Fatalf("slot 1 while slot 2 connects: %+v", v)
	}
	releaseDials()
	waitState(model.WorkerRequesting, nil)
	releaseHeads()
	waitState(model.WorkerReceiving, func(v model.WorkerView) bool { return v.Received > 0 && v.SpeedBps > 0 })
	if v, _ := log.slot(2); v.Start <= 0 || v.End < v.Start || v.End >= size {
		t.Fatalf("slot 2 range: %+v", v)
	}
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}

	events, _ := log.snapshot()
	checkViews(t, events, strings.TrimPrefix(front.URL, "http://"))
	final := events[len(events)-1]
	if len(final.Workers) < 2 {
		t.Fatalf("final workers: %+v", final.Workers)
	}
	for _, v := range final.Workers {
		if v.State != model.WorkerDone || v.SpeedBps != 0 || v.Start < 0 || v.Received != v.End-v.Start+1 {
			t.Fatalf("final view %+v, want done with its whole last range", v)
		}
	}
	// Slot 2 went through the states in order.
	first := map[model.WorkerState]int{}
	for i, p := range events {
		if v, ok := findSlot(p, 2); ok {
			if _, seen := first[v.State]; !seen {
				first[v.State] = i
			}
		}
	}
	order := []model.WorkerState{model.WorkerConnecting, model.WorkerRequesting, model.WorkerReceiving, model.WorkerDone}
	for i := 1; i < len(order); i++ {
		a, okA := first[order[i-1]]
		b, okB := first[order[i]]
		if !okA || !okB || a >= b {
			t.Fatalf("slot 2 states not in order %v: first seen %v", order, first)
		}
	}
	got := st.get(t, d.ID)
	if got.State != model.StateCompleted {
		t.Fatalf("state %v (%s)", got.State, got.Error)
	}
}

// TestWorkerViewRetryAfter checks that a connection waiting out Retry-After reports
// retrying with the reason and the time of the next attempt.
func TestWorkerViewRetryAfter(t *testing.T) {
	t.Parallel()
	opts := fastOpts()
	opts.StallTimeout = 300 * time.Millisecond
	// The probe's response stalls; its replacement is answered 503 with Retry-After: 1.
	h := newHarness(t, testserver.Options{Size: 1 << 20, Seed: 92, ETag: `"r"`, StallAfter: 64 << 10, StallCount: 1,
		FailSkip: 1, FailFirst: 1, FailStatus: http.StatusServiceUnavailable, RetryAfter: "1"}, opts)
	d := h.add("/retry.bin", "", 1)
	var log progressLog
	if err := h.run(d.ID, nil, log.on); err != nil {
		t.Fatalf("Run: %v", err)
	}
	h.verify(d.ID)
	events, at := log.snapshot()
	checkViews(t, events, strings.TrimPrefix(h.srv.URL, "http://"))
	var retryAt time.Time
	seen := 0
	for i, p := range events {
		for _, v := range p.Workers {
			if v.State != model.WorkerRetrying {
				continue
			}
			seen++
			if v.Detail != "HTTP 503, retrying in 1 s" {
				t.Fatalf("retry detail %q", v.Detail)
			}
			if wait := v.RetryAt.Sub(at[i]); wait <= 0 || wait > 1100*time.Millisecond {
				t.Fatalf("RetryAt %v is %v after the event", v.RetryAt, wait)
			}
			if !retryAt.IsZero() && !v.RetryAt.Equal(retryAt) {
				t.Fatalf("RetryAt changed from %v to %v during one wait", retryAt, v.RetryAt)
			}
			retryAt = v.RetryAt
			if v.Slot != 1 || active(p) != 1 {
				t.Fatalf("retrying view %+v in %+v", v, p.Workers)
			}
		}
	}
	if seen < 2 {
		t.Fatalf("%d events showed the retry wait, want ≥ 2 (a 1 s wait at 4 Hz)", seen)
	}
}

// TestSetMaxConnectionsLower lowers a running download from 8 connections to 2:
// the extra connections stop at once, their ranges are finished by the others, and
// every byte is written exactly once. The server holds after half the file until the
// limit is lowered, so the download cannot end first (at 8 × 4 MiB/s it takes 0.5 s).
func TestSetMaxConnectionsLower(t *testing.T) {
	t.Parallel()
	opts := fastOpts()
	rec := &recorder{t: t}
	opts.openFile = rec.open
	const size = 16 << 20
	h := newHarness(t, testserver.Options{Size: size, Seed: 93, ETag: `"l"`, Throttle: 4 << 20}, opts)
	d := h.add("/lower.bin", "", 8)
	h.srv.Hold(size / 2)
	defer h.srv.Release()
	var log progressLog
	done := make(chan error, 1)
	go func() { done <- h.run(d.ID, nil, log.on) }()
	waitFor(t, 30*time.Second, "8 connections", func() bool {
		p, ok := log.last()
		return ok && p.Connections == 8 && active(p) == 8
	})
	mark := log.len()
	if err := h.eng.SetMaxConnections(d.ID, 2); err != nil {
		t.Fatalf("SetMaxConnections: %v", err)
	}
	h.srv.Release()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := h.verify(d.ID)
	if got.MaxConnections != 2 {
		t.Fatalf("stored max connections %d, want 2", got.MaxConnections)
	}
	if spans := rec.file().writtenSpans(); len(spans) != 1 || spans[0] != (span{0, size}) {
		t.Fatalf("written spans %v", spans)
	}
	events, _ := log.snapshot()
	checkViews(t, events, strings.TrimPrefix(h.srv.URL, "http://"))
	applied := -1
	for i := mark; i < len(events)-1; i++ { // the last event follows the end of the run
		if p := events[i]; p.Connections <= 2 && active(p) <= 2 {
			applied = i
			break
		}
	}
	if applied < 0 {
		t.Fatalf("the lower limit never applied while the download ran (%d events after the change)", len(events)-mark)
	}
	for i := applied; i < len(events); i++ {
		if p := events[i]; p.Connections > 2 || active(p) > 2 {
			t.Fatalf("event %d after the change: %d connections, %d active views", i, p.Connections, active(p))
		}
	}
	if n := events[applied].Workers; len(n) == 0 || n[0].Slot != 1 {
		t.Fatalf("workers after lowering: %+v (the lowest slots stay)", n)
	}
}

// TestSetMaxConnectionsRaise raises a one-connection download to 4: ramp-up adds
// connections up to the new limit and never beyond.
func TestSetMaxConnectionsRaise(t *testing.T) {
	t.Parallel()
	const size = 8 << 20
	h := newHarness(t, testserver.Options{Size: size, Seed: 94, ETag: `"u"`, Throttle: 2 << 20}, fastOpts())
	d := h.add("/raise.bin", "", 1)
	var log progressLog
	done := make(chan error, 1)
	go func() { done <- h.run(d.ID, nil, log.on) }()
	waitFor(t, 10*time.Second, "three events", func() bool { return log.len() >= 3 })
	mark := log.len()
	if err := h.eng.SetMaxConnections(d.ID, 4); err != nil {
		t.Fatalf("SetMaxConnections: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := h.verify(d.ID); got.MaxConnections != 4 {
		t.Fatalf("stored max connections %d, want 4", got.MaxConnections)
	}
	events, _ := log.snapshot()
	checkViews(t, events, strings.TrimPrefix(h.srv.URL, "http://"))
	peak := 0
	for i, p := range events {
		if i < mark && p.Connections > 1 {
			t.Fatalf("event %d before the change: %d connections", i, p.Connections)
		}
		if p.Connections > 4 || active(p) > 4 {
			t.Fatalf("event %d: %d connections, %d active views", i, p.Connections, active(p))
		}
		peak = max(peak, p.Connections)
	}
	if peak != 4 {
		t.Fatalf("peak connections %d after raising the limit to 4", peak)
	}
}

func TestSetMaxConnectionsNotRunning(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testserver.Options{Size: 1000, Seed: 95}, fastOpts())
	d := h.add("/idle.bin", "", 2)
	if err := h.eng.SetMaxConnections(d.ID, 4); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("SetMaxConnections on a queued download: %v", err)
	}
	if err := h.eng.SetMaxConnections(d.ID+100, 4); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("SetMaxConnections on an unknown download: %v", err)
	}
	if got := h.st.get(t, d.ID); got.MaxConnections != 2 {
		t.Fatalf("stored max connections changed to %d", got.MaxConnections)
	}
}

// TestLiveViewIgnoresStaleTrace checks that trace callbacks of an earlier request, or
// arriving after the response, never move the state.
func TestLiveViewIgnoresStaleTrace(t *testing.T) {
	var v liveView
	_, g1 := v.trace(context.Background())
	v.step(g1, model.WorkerRequesting, "")
	if s, _, _ := v.snapshot(); s != model.WorkerRequesting {
		t.Fatalf("state %v after WroteRequest", s)
	}
	v.end(g1)
	v.receiving()
	v.step(g1, model.WorkerConnecting, detailOpening) // a late dial of the first request
	if s, d, _ := v.snapshot(); s != model.WorkerReceiving || d != "" {
		t.Fatalf("late callback changed the view: %v %q", s, d)
	}
	_, g2 := v.trace(context.Background())
	v.step(g1, model.WorkerRequesting, "") // the first request's callback during the second
	if s, _, _ := v.snapshot(); s != model.WorkerConnecting {
		t.Fatalf("stale callback changed the view: %v", s)
	}
	v.step(g2, model.WorkerConnecting, detailSecure)
	if s, d, _ := v.snapshot(); s != model.WorkerConnecting || d != detailSecure {
		t.Fatalf("view %v %q", s, d)
	}
	at := time.Unix(100, 0)
	v.retry("HTTP 429", at)
	if s, d, r := v.snapshot(); s != model.WorkerRetrying || d != "HTTP 429" || !r.Equal(at) {
		t.Fatalf("retry view %v %q %v", s, d, r)
	}
	v.step(g2, model.WorkerRequesting, "")
	if s, _, _ := v.snapshot(); s != model.WorkerRetrying {
		t.Fatalf("callback after the request returned changed the view: %v", s)
	}
}

func TestRetryDetail(t *testing.T) {
	now := time.Unix(1000, 0)
	for _, c := range []struct {
		reason string
		left   time.Duration
		want   string
	}{
		{"HTTP 503", 4 * time.Second, "HTTP 503, retrying in 4 s"},
		{"HTTP 503", 3100 * time.Millisecond, "HTTP 503, retrying in 4 s"},
		{"connection reset", 10 * time.Millisecond, "connection reset, retrying in 1 s"},
		{"HTTP 429", -time.Second, "HTTP 429, retrying in 1 s"},
		{"HTTP 429", 99 * time.Second, "HTTP 429, retrying in 99 s"},
		{"HTTP 429", 150 * time.Second, "HTTP 429, retrying in 3 min"},
		{"", 2 * time.Second, "retrying in 2 s"},
	} {
		if got := retryDetail(c.reason, now.Add(c.left), now); got != c.want {
			t.Errorf("retryDetail(%q, %v) = %q, want %q", c.reason, c.left, got, c.want)
		}
	}
}

func TestShortReasonHasNoURL(t *testing.T) {
	secret := "https://user:pass@example.test/f.bin?token=abc"
	for _, c := range []struct {
		err  error
		want string
	}{
		{&StatusError{Code: 503}, "HTTP 503"},
		{&url.Error{Op: "Get", URL: secret, Err: io.ErrUnexpectedEOF}, "connection closed early"},
		{&url.Error{Op: "Get", URL: secret, Err: &net.DNSError{Err: "no such host", Name: "example.test"}}, "host not found"},
		{&url.Error{Op: "Get", URL: secret, Err: errors.New("boom " + secret)}, "network error"},
		{&url.Error{Op: "Get", URL: secret, Err: context.DeadlineExceeded}, "timed out"},
		{ErrValidatorChanged, "file changed on the server"},
		{ErrDiskFull, "disk full"},
	} {
		got := shortReason(c.err)
		if got != c.want {
			t.Errorf("shortReason(%v) = %q, want %q", c.err, got, c.want)
		}
		if strings.Contains(got, "example") || strings.Contains(got, "token") {
			t.Errorf("shortReason leaks the URL: %q", got)
		}
	}
}
