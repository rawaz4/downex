package mediajob

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"downex/internal/model"
)

// liveLog records progress events for a test that inspects them while Run runs.
type liveLog struct {
	mu     sync.Mutex
	events []Progress
	at     []time.Time
}

func (l *liveLog) on(p Progress) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, p)
	l.at = append(l.at, time.Now())
}

func (l *liveLog) snapshot() ([]Progress, []time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Progress(nil), l.events...), append([]time.Time(nil), l.at...)
}

// fragment returns the latest view of the worker on fragment i.
func (l *liveLog) fragment(i int) (model.WorkerView, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.events) == 0 {
		return model.WorkerView{}, false
	}
	for _, v := range l.events[len(l.events)-1].Workers {
		if v.Fragment == i {
			return v, true
		}
	}
	return model.WorkerView{}, false
}

// checkWorkerViews checks invariants of every event: at most max workers in slot
// order, fragments within the job, no URL in a detail, RetryAt only while retrying.
func checkWorkerViews(t *testing.T, events []Progress, maxWorkers int, host string) {
	t.Helper()
	for i, p := range events {
		if len(p.Workers) > maxWorkers {
			t.Fatalf("event %d: %d workers", i, len(p.Workers))
		}
		for k, v := range p.Workers {
			if v.Slot != k+1 {
				t.Fatalf("event %d: slots %+v", i, p.Workers)
			}
			if v.Fragment < -1 || v.Fragment >= p.FragmentsTotal {
				t.Fatalf("event %d: fragment %d of %d", i, v.Fragment, p.FragmentsTotal)
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

// TestWorkerViews holds one fragment's response head, then the second half of its
// body, and checks that its worker reports requesting, then receiving with the bytes
// of the attempt, and finally done.
func TestWorkerViews(t *testing.T) {
	const n, hold = 4, 1
	segs := make([][]byte, n)
	names := make([]string, n)
	var all []byte
	for i := range n {
		segs[i] = tsSegment(uint64(500+i), 40)
		names[i] = fmt.Sprintf("seg%d.ts", i)
		all = append(all, segs[i]...)
	}
	playlist := mediaPlaylist("", names)
	half := len(segs[hold]) / 2
	head, body := make(chan struct{}), make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v/index.m3u8" {
			w.Header().Set("Content-Type", hlsType)
			_, _ = io.WriteString(w, playlist)
			return
		}
		var i int
		if _, err := fmt.Sscanf(r.URL.Path, "/v/seg%d.ts", &i); err != nil || i < 0 || i >= n {
			http.NotFound(w, r)
			return
		}
		wait := func(gate chan struct{}) bool {
			if i != hold {
				return true
			}
			select {
			case <-gate:
				return true
			case <-r.Context().Done():
				return false
			}
		}
		if !wait(head) {
			return
		}
		data := segs[i]
		w.Header().Set("Content-Type", "video/mp2t")
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data[:len(data)/2])
		w.(http.Flusher).Flush()
		if !wait(body) {
			return
		}
		_, _ = w.Write(data[len(data)/2:])
	}))
	t.Cleanup(ts.Close)
	var openHead, openBody sync.Once
	releaseHead := func() { openHead.Do(func() { close(head) }) }
	releaseBody := func() { openBody.Do(func() { close(body) }) }
	t.Cleanup(releaseHead) // before ts.Close
	t.Cleanup(releaseBody)

	asm := &fakeAssembler{}
	opts := testOptions(t, asm)
	opts.MaxConnections = 2
	var log liveLog
	done := make(chan error, 1)
	go func() {
		_, err := Run(testContext(t), opts, Selection{ManifestURL: ts.URL + "/v/index.m3u8"}, nil, log.on, nil)
		done <- err
	}()
	waitView := func(what string, cond func(model.WorkerView) bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			if v, ok := log.fragment(hold); ok && cond(v) {
				return
			}
			select {
			case err := <-done:
				t.Fatalf("Run returned (%v) before the worker was %s", err, what)
			default:
			}
			if time.Now().After(deadline) {
				v, _ := log.fragment(hold)
				t.Fatalf("timed out waiting for the worker to be %s; last view %+v", what, v)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitView("requesting", func(v model.WorkerView) bool { return v.State == model.WorkerRequesting })
	releaseHead()
	waitView("receiving", func(v model.WorkerView) bool {
		return v.State == model.WorkerReceiving && v.Received == int64(half) && v.Start == -1 && v.End == -1
	})
	releaseBody()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, opts.OutPath); !bytes.Equal(got, all) {
		t.Fatal("output differs")
	}
	events, _ := log.snapshot()
	checkWorkerViews(t, events, 2, strings.TrimPrefix(ts.URL, "http://"))
	final := events[len(events)-1]
	if len(final.Workers) != 2 {
		t.Fatalf("final workers %+v", final.Workers)
	}
	for _, v := range final.Workers {
		if v.State != model.WorkerDone || v.SpeedBps != 0 || v.Fragment < 0 {
			t.Fatalf("final view %+v", v)
		}
	}
	seen := map[int]bool{}
	for _, p := range events {
		for _, v := range p.Workers {
			seen[v.Fragment] = true
		}
	}
	if !seen[hold] || len(seen) < 2 {
		t.Fatalf("fragments seen in worker views: %v", seen)
	}
}

func TestWorkerViewRetry(t *testing.T) {
	s := newServer(t)
	want := addTSPlaylist(s, "/r/", 3, 21)
	s.failNext("/r/seg1.ts", 1, http.StatusServiceUnavailable, "1")
	opts := testOptions(t, &fakeAssembler{})
	opts.MaxConnections = 1
	var log liveLog
	if _, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/r/index.m3u8")}, nil, log.on, nil); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, opts.OutPath); !bytes.Equal(got, want) {
		t.Fatal("output differs")
	}
	events, at := log.snapshot()
	checkWorkerViews(t, events, 1, strings.TrimPrefix(s.ts.URL, "http://"))
	retrying := 0
	for i, p := range events {
		for _, v := range p.Workers {
			if v.State != model.WorkerRetrying {
				continue
			}
			retrying++
			if v.Fragment != 1 || v.Detail != "HTTP 503, retrying in 1 s" {
				t.Fatalf("retrying view %+v", v)
			}
			// at is taken in the callback, after the view: an event of the tick that
			// coincides with the end of the wait may see RetryAt a moment before at.
			if wait := v.RetryAt.Sub(at[i]); wait <= -50*time.Millisecond || wait > 1100*time.Millisecond {
				t.Fatalf("RetryAt %v after the event", wait)
			}
		}
	}
	if retrying < 2 {
		t.Fatalf("%d events showed the retry wait, want ≥ 2 (a 1 s wait at 4 Hz)", retrying)
	}
	final := events[len(events)-1].Workers
	if len(final) != 1 || final[0].State != model.WorkerDone || final[0].Fragment != 2 {
		t.Fatalf("final workers %+v", final)
	}
}

func TestWorkerViewError(t *testing.T) {
	s := newServer(t)
	addTSPlaylist(s, "/e/", 4, 22)
	s.failNext("/e/seg2.ts", 100, http.StatusForbidden, "")
	opts := testOptions(t, &fakeAssembler{})
	opts.MaxConnections = 1
	var log liveLog
	if _, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/e/index.m3u8")}, nil, log.on, nil); err == nil {
		t.Fatal("Run succeeded despite a permanent failure")
	}
	events, _ := log.snapshot()
	checkWorkerViews(t, events, 1, strings.TrimPrefix(s.ts.URL, "http://"))
	final := events[len(events)-1].Workers
	if len(final) != 1 || final[0].State != model.WorkerError || final[0].Detail != "HTTP 403" || final[0].Fragment != 2 {
		t.Fatalf("final workers %+v", final)
	}
}

func TestConnViewIgnoresStaleTrace(t *testing.T) {
	v := newConnView(1)
	v.assign(3, 10, 19)
	_, g1 := v.trace(context.Background())
	v.step(g1, model.WorkerRequesting, "")
	v.receiving()
	v.step(g1, model.WorkerConnecting, detailOpening) // a request after the body started
	v.end(g1)
	if got := v.view(time.Now()); got.State != model.WorkerReceiving || got.Fragment != 3 || got.Start != 10 || got.End != 19 {
		t.Fatalf("view %+v", got)
	}
	_, g2 := v.trace(context.Background())
	v.step(g1, model.WorkerRequesting, "")
	if got := v.view(time.Now()); got.State != model.WorkerConnecting {
		t.Fatalf("stale callback moved the view: %+v", got)
	}
	v.end(g2)
	v.step(g2, model.WorkerRequesting, "")
	if got := v.view(time.Now()); got.State != model.WorkerConnecting {
		t.Fatalf("callback after the attempt moved the view: %+v", got)
	}
}
