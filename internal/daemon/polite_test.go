package daemon

import (
	"context"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"downex/internal/model"
	"downex/internal/polite"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
)

// scriptedProber answers every probe after rtt (0: no reply, after the timeout).
type scriptedProber struct{ rtt atomic.Int64 }

func (p *scriptedProber) set(d time.Duration) { p.rtt.Store(int64(d)) }

func (p *scriptedProber) answer(ctx context.Context) (time.Duration, error) {
	d := time.Duration(p.rtt.Load())
	wait := d
	if d == 0 {
		wait = polite.ProbeTimeout
	}
	select {
	case <-time.After(wait):
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	if d == 0 {
		return 0, polite.ErrNoReply
	}
	return d, nil
}

func (p *scriptedProber) ProbeICMP(ctx context.Context, _ netip.Addr) (time.Duration, error) {
	return p.answer(ctx)
}

func (p *scriptedProber) ProbeTCP(ctx context.Context, _ netip.AddrPort) (time.Duration, error) {
	return p.answer(ctx)
}

func (p *scriptedProber) Close() error { return nil }

// politeWatch collects the polite events of a Watch without ids.
type politeWatch struct {
	mu     sync.Mutex
	events []*downexv1.PoliteStatus
	first  []*downexv1.Event // the first batch
	other  int               // polite events of a Watch of one download
}

func (w *politeWatch) last() *downexv1.PoliteStatus {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.events) == 0 {
		return nil
	}
	return w.events[len(w.events)-1]
}

func watchPolite(t *testing.T, h *harness, ids []int64) *politeWatch {
	t.Helper()
	w := &politeWatch{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	go func() {
		defer close(done)
		_ = h.m.Watch(ctx, ids, func(r *downexv1.WatchResponse) error {
			w.mu.Lock()
			defer w.mu.Unlock()
			if w.first == nil {
				w.first = r.GetEvents()
				if w.first == nil {
					w.first = []*downexv1.Event{}
				}
			}
			for _, ev := range r.GetEvents() {
				if p := ev.GetPolite(); p != nil {
					if ids != nil {
						w.other++
					}
					w.events = append(w.events, p)
				}
			}
			return nil
		})
	}()
	return w
}

func (w *politeWatch) waitState(t *testing.T, want downexv1.PoliteState) *downexv1.PoliteStatus {
	t.Helper()
	var got *downexv1.PoliteStatus
	waitUntil(t, "Polite mode "+want.String(), func() bool {
		got = w.last()
		return got != nil && got.GetState() == want
	})
	return got
}

func runLimit(t *testing.T, h *harness, id int64) int64 {
	t.Helper()
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	e := h.m.entries[model.DownloadID(id)]
	if e == nil || e.run == nil {
		t.Fatalf("download %d is not running", id)
	}
	return e.run.lim.Limit()
}

func setPolite(t *testing.T, h *harness, on bool, change func(*downexv1.Settings)) {
	t.Helper()
	s := protoSettings(h.m.Settings())
	s.PoliteMode = on
	if change != nil {
		change(s)
	}
	if _, err := h.m.UpdateSettings(ctxT(t), s); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
}

// Polite mode end to end in the daemon (docs/POLITE-SPEC.md §4-§7): switched on, it
// watches a running download, slows it when the line lags (the cap reaches the run's
// limiter chain), stays under a tighter user limit, and switching off removes the cap at
// once.
func TestPoliteSlowsDownloads(t *testing.T) {
	prober := &scriptedProber{}
	prober.set(20 * time.Millisecond)
	h := newHarness(t, harnessOptions{politeProber: prober})
	ts := serve(t, testserver.Options{Size: 256 << 20, Throttle: 2 << 20})
	all := watchPolite(t, h, nil)
	waitUntil(t, "the first batch", func() bool { all.mu.Lock(); defer all.mu.Unlock(); return all.first != nil })
	all.mu.Lock()
	if n := len(all.first); n == 0 || all.first[n-1].GetPolite().GetState() != downexv1.PoliteState_POLITE_STATE_OFF {
		t.Fatalf("the first batch does not end with Polite mode's status: %v", all.first)
	}
	all.mu.Unlock()

	setPolite(t, h, true, nil)
	all.waitState(t, downexv1.PoliteState_POLITE_STATE_IDLE)
	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/file", MaxConnections: 4})
	one := watchPolite(t, h, []int64{d.GetId()})
	all.waitState(t, downexv1.PoliteState_POLITE_STATE_WATCHING)
	h.waitReceived(d.GetId(), 1)
	if got := runLimit(t, h, d.GetId()); got != 0 {
		t.Fatalf("limit %d while watching", got)
	}

	// The line lags: SLOWED, and the run's chain carries the cap.
	time.Sleep(time.Second) // a few quiet samples first
	// Status shows the line delay as soon as it is known, although only state changes are
	// published while watching (rule 11).
	waitUntil(t, "a known line delay in Status while watching", func() bool {
		s := h.status().GetPolite()
		return s.GetState() == downexv1.PoliteState_POLITE_STATE_WATCHING && s.GetDelayMs() >= 0 && s.GetBaseMs() > 0
	})
	prober.set(150 * time.Millisecond)
	st := all.waitState(t, downexv1.PoliteState_POLITE_STATE_SLOWED)
	if st.GetRateLimit() < polite.Floor {
		t.Fatalf("SLOWED with rate %d", st.GetRateLimit())
	}
	if got := runLimit(t, h, d.GetId()); got <= 0 {
		t.Fatalf("the run's limit is %d while SLOWED", got)
	}
	if s := h.status().GetPolite(); s.GetState() != downexv1.PoliteState_POLITE_STATE_SLOWED || s.GetRateLimit() <= 0 {
		t.Fatalf("Status().polite = %v", s)
	}
	if !h.m.Activity().PoliteSlowed {
		t.Fatal("Activity().PoliteSlowed is false while SLOWED")
	}

	// A user limit tighter than the cap is the one in effect.
	setPolite(t, h, true, func(s *downexv1.Settings) { s.GlobalSpeedLimit = polite.Floor / 2 })
	if got := runLimit(t, h, d.GetId()); got != polite.Floor/2 {
		t.Fatalf("limit %d with a global limit of %d", got, polite.Floor/2)
	}

	// Off: no cap at once, and an OFF event.
	setPolite(t, h, false, func(s *downexv1.Settings) { s.GlobalSpeedLimit = 0 })
	if got := runLimit(t, h, d.GetId()); got != 0 {
		t.Fatalf("limit %d right after switching off", got)
	}
	all.waitState(t, downexv1.PoliteState_POLITE_STATE_OFF)
	if h.m.Activity().PoliteSlowed {
		t.Fatal("Activity().PoliteSlowed after switching off")
	}
	one.mu.Lock()
	defer one.mu.Unlock()
	if one.other != 0 {
		t.Fatalf("a Watch of one download got %d polite events", one.other)
	}
}

// Without a prober the setting is kept and the status says UNAVAILABLE while on.
func TestPoliteUnavailable(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	all := watchPolite(t, h, nil)
	if s := h.status().GetPolite(); s.GetState() != downexv1.PoliteState_POLITE_STATE_OFF {
		t.Fatalf("status %v", s)
	}
	setPolite(t, h, true, nil)
	all.waitState(t, downexv1.PoliteState_POLITE_STATE_UNAVAILABLE)
	if s := h.status().GetPolite(); s.GetState() != downexv1.PoliteState_POLITE_STATE_UNAVAILABLE {
		t.Fatalf("status %v", s)
	}
	if !h.m.Settings().Polite.Enabled {
		t.Fatal("the setting was not kept")
	}
	setPolite(t, h, false, nil)
	all.waitState(t, downexv1.PoliteState_POLITE_STATE_OFF)
}

// The polite level sits between the global limiter and every queue, also a queue created
// later, and a run's link meters its bytes.
func TestPoliteLevelInTheChain(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	h.m.polite.link.SetLimit(3 << 20)
	h.m.mu.Lock()
	main := h.m.queues[model.MainQueueID]
	h.m.mu.Unlock()
	if got := main.lim.Limit(); got != 3<<20 {
		t.Fatalf("the main queue's chain limit %d, want the polite level's", got)
	}
	q, err := h.m.CreateQueue(ctxT(t), &downexv1.Queue{Name: "Later", MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	h.m.mu.Lock()
	later := h.m.queues[model.QueueID(q.GetId())]
	h.m.mu.Unlock()
	if got := later.lim.Limit(); got != 3<<20 {
		t.Fatalf("a new queue's chain limit %d", got)
	}
	h.m.global.SetLimit(1 << 20)
	if got := later.lim.Limit(); got != 1<<20 {
		t.Fatalf("with a tighter global limit: %d", got)
	}
	h.m.polite.link.SetLimit(0)
	h.m.global.SetLimit(0)

	ts := serve(t, testserver.Options{Size: 1 << 20})
	before := h.m.polite.rx.Load()
	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/file"})
	h.waitCompleted(d.GetId())
	if got := h.m.polite.rx.Load() - before; got < 1<<20 {
		t.Fatalf("the meter counted %d bytes of a 1 MiB download", got)
	}
}
