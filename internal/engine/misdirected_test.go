package engine

import (
	"context"
	"slices"
	"strconv"
	"sync"
	"testing"

	"downex/internal/model"
	"downex/internal/testserver"
	"downex/internal/transport"
)

// eventFactory wraps a factory and logs, per slot, each response status and each Close.
type eventFactory struct {
	inner transport.Factory
	mu    sync.Mutex
	slots [][]string
}

func (f *eventFactory) NewSlot() transport.Fetcher {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.slots = append(f.slots, nil)
	return &eventFetcher{f: f, i: len(f.slots) - 1, inner: f.inner.NewSlot()}
}

func (f *eventFactory) logs() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]string, len(f.slots))
	for i, s := range f.slots {
		out[i] = slices.Clone(s)
	}
	return out
}

type eventFetcher struct {
	f     *eventFactory
	i     int
	inner transport.Fetcher
}

func (e *eventFetcher) log(s string) {
	e.f.mu.Lock()
	defer e.f.mu.Unlock()
	e.f.slots[e.i] = append(e.f.slots[e.i], s)
}

func (e *eventFetcher) Fetch(ctx context.Context, rr transport.RangeRequest) (*transport.Response, error) {
	resp, err := e.inner.Fetch(ctx, rr)
	if err != nil {
		e.log("error")
		return nil, err
	}
	e.log(strconv.Itoa(resp.StatusCode))
	return resp, nil
}

func (e *eventFetcher) Close() {
	e.log("close")
	e.inner.Close()
}

// TestMisdirectedRetriesOnNewConnection: RFC 9110 §15.5.20 says a 421 may be retried
// over a different connection, so the slot's pooled connection is closed first.
func TestMisdirectedRetriesOnNewConnection(t *testing.T) {
	t.Parallel()
	for name, skip := range map[string]int{"probe": 0, "segment": 1} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, testserver.Options{Size: 2 << 20, Seed: 86, ETag: `"m"`, Throttle: 1 << 20,
				FailSkip: skip, FailFirst: 1, FailStatus: 421}, fastOpts())
			ef := &eventFactory{inner: h.fac}
			h.eng = New(h.st, ef, fastOpts())
			d, err := h.eng.Add(context.Background(), model.Request{URL: h.srv.URL + "/m.bin"}, h.dir, "", 4, 0)
			if err != nil {
				t.Fatal(err)
			}
			if err := h.run(d.ID, nil, nil); err != nil {
				t.Fatalf("Run: %v", err)
			}
			h.verify(d.ID)
			found := false
			for _, log := range ef.logs() {
				i := slices.Index(log, "421")
				if i < 0 {
					continue
				}
				found = true
				if i+1 >= len(log) || log[i+1] != "close" {
					t.Fatalf("slot events %v: the 421 was retried over the same connection", log)
				}
			}
			if !found {
				t.Fatal("no 421 was served")
			}
		})
	}
}
