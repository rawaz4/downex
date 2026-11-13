package testserver

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"
)

func get(t *testing.T, s *Server, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, s.URL+"/f.bin", nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, body
}

func TestContentGenerator(t *testing.T) {
	p := make([]byte, 1000)
	Fill(5, p, 3)
	for i := range p {
		if p[i] != Byte(5, int64(i)+3) {
			t.Fatalf("Fill and Byte differ at %d", i)
		}
	}
	if bytes.Equal(Bytes(1, 0, 64), Bytes(2, 0, 64)) {
		t.Fatal("seed does not change content")
	}
	if bytes.Equal(Bytes(1, 0, 64), Bytes(1, 64, 64)) {
		t.Fatal("content repeats")
	}
}

func TestRanges(t *testing.T) {
	lm := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	s := New(Options{Size: 10_000, Seed: 9, ETag: `"e1"`, LastModified: lm, Filename: "r é.bin"})
	defer s.Close()

	resp, body := get(t, s, nil)
	if resp.StatusCode != 200 || !bytes.Equal(body, Bytes(9, 0, 10_000)) || resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("full GET: %d, %d bytes", resp.StatusCode, len(body))
	}
	if cd := resp.Header.Get("Content-Disposition"); cd == "" {
		t.Fatal("no Content-Disposition")
	}
	for _, tc := range []struct {
		rng, ifRange string
		status       int
		start, n     int64
		cr           string
	}{
		{"bytes=100-199", "", 206, 100, 100, "bytes 100-199/10000"},
		{"bytes=9990-", "", 206, 9990, 10, "bytes 9990-9999/10000"},
		{"bytes=-5", "", 206, 9995, 5, "bytes 9995-9999/10000"},
		{"bytes=9000-20000", "", 206, 9000, 1000, "bytes 9000-9999/10000"},
		{"bytes=10000-", "", 416, 0, 0, "bytes */10000"},
		{"bytes=5-1", "", 200, 0, 10_000, ""},
		{"bytes=0-1,5-6", "", 200, 0, 10_000, ""},
		{"bytes=100-199", `"e1"`, 206, 100, 100, "bytes 100-199/10000"},
		{"bytes=100-199", `"other"`, 200, 0, 10_000, ""},
		{"bytes=100-199", `W/"e1"`, 200, 0, 10_000, ""},
		{"bytes=100-199", lm.Format(http.TimeFormat), 206, 100, 100, "bytes 100-199/10000"},
		{"bytes=100-199", lm.Add(time.Second).Format(http.TimeFormat), 200, 0, 10_000, ""},
	} {
		h := map[string]string{"Range": tc.rng}
		if tc.ifRange != "" {
			h["If-Range"] = tc.ifRange
		}
		resp, body := get(t, s, h)
		if resp.StatusCode != tc.status || resp.Header.Get("Content-Range") != tc.cr {
			t.Errorf("%s if-range %q: %d %q", tc.rng, tc.ifRange, resp.StatusCode, resp.Header.Get("Content-Range"))
			continue
		}
		if tc.status != 416 && !bytes.Equal(body, Bytes(9, tc.start, tc.n)) {
			t.Errorf("%s: wrong body", tc.rng)
		}
	}
	s.SetETag(`"e2"`)
	if resp, _ := get(t, s, map[string]string{"Range": "bytes=0-9", "If-Range": `"e1"`}); resp.StatusCode != 200 {
		t.Fatalf("changed ETag still matched: %d", resp.StatusCode)
	}
	if n := len(s.Requests()); n != 14 {
		t.Fatalf("recorded %d requests", n)
	}
}

func TestFaults(t *testing.T) {
	s := New(Options{Size: 1000, FailFirst: 2, FailStatus: 429, RetryAfter: "3"})
	defer s.Close()
	for i := range 2 {
		resp, _ := get(t, s, nil)
		if resp.StatusCode != 429 || resp.Header.Get("Retry-After") != "3" {
			t.Fatalf("request %d: %d", i, resp.StatusCode)
		}
	}
	if resp, _ := get(t, s, map[string]string{"Range": "bytes=0-"}); resp.StatusCode != 206 {
		t.Fatalf("after failures: %d", resp.StatusCode)
	}
	s.Update(func(o *Options) { o.NoRanges = true })
	if resp, _ := get(t, s, map[string]string{"Range": "bytes=5-"}); resp.StatusCode != 200 || resp.Header.Get("Accept-Ranges") != "" {
		t.Fatalf("NoRanges: %d", resp.StatusCode)
	}
	s.Update(func(o *Options) { o.NoRanges, o.AcceptRangesNone = false, true })
	if resp, _ := get(t, s, map[string]string{"Range": "bytes=5-"}); resp.StatusCode != 200 || resp.Header.Get("Accept-Ranges") != "none" {
		t.Fatalf("AcceptRangesNone: %d", resp.StatusCode)
	}
	s.Update(func(o *Options) { o.AcceptRangesNone, o.Always416 = false, true })
	if resp, _ := get(t, s, map[string]string{"Range": "bytes=5-"}); resp.StatusCode != 416 {
		t.Fatalf("Always416: %d", resp.StatusCode)
	}
}

// TestLimit: requests beyond Limit at once are refused with 503 (or, with LimitAbort,
// a connection closed unanswered) and are not counted as served.
func TestLimit(t *testing.T) {
	for _, abort := range []bool{false, true} {
		s := New(Options{Size: 100_000, Throttle: 200_000, Limit: 1, LimitAbort: abort, RetryAfter: "2"})
		held, err := s.Client().Get(s.URL) // 0.5 s at 200 kB/s: holds the one place
		if err != nil {
			t.Fatal(err)
		}
		resp, err := s.Client().Get(s.URL)
		switch {
		case abort && err == nil:
			resp.Body.Close()
			t.Errorf("abort: status %d, want the connection closed", resp.StatusCode)
		case !abort && (err != nil || resp.StatusCode != 503 || resp.Header.Get("Retry-After") != "2"):
			t.Errorf("503: %v %v", resp, err)
		case !abort:
			resp.Body.Close()
		}
		_, _ = io.Copy(io.Discard, held.Body)
		held.Body.Close()
		if resp, _ := get(t, s, nil); resp.StatusCode != 200 {
			t.Errorf("abort %v: after the limit freed: %d", abort, resp.StatusCode)
		}
		want := 503
		if abort {
			want = 0
		}
		if r := s.Requests(); len(r) != 3 || r[1].Status != want || s.MaxConcurrent() != 1 {
			t.Errorf("abort %v: requests %+v, max concurrent %d", abort, r, s.MaxConcurrent())
		}
		s.Close()
	}
}

func TestEmptyResource(t *testing.T) {
	s := New(Options{Size: 0})
	defer s.Close()
	if resp, _ := get(t, s, map[string]string{"Range": "bytes=0-"}); resp.StatusCode != 416 || resp.Header.Get("Content-Range") != "bytes */0" {
		t.Fatalf("empty range: %d %q", resp.StatusCode, resp.Header.Get("Content-Range"))
	}
}

func TestStallAndThrottle(t *testing.T) {
	s := New(Options{Size: 100_000, StallAfter: 1000, StallCount: 1, Throttle: 1 << 20})
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	resp, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err == nil || len(body) != 1000 {
		t.Fatalf("stalled response: %d bytes, err %v", len(body), err)
	}
	start := time.Now()
	_, body = get(t, s, nil) // only the first response stalls
	if len(body) != 100_000 {
		t.Fatalf("second response: %d bytes", len(body))
	}
	if el := time.Since(start); el < 60*time.Millisecond {
		t.Fatalf("100 kB at 1 MiB/s took %v", el)
	}
	// The stall ends when the server notices that the first client gave up (its 300 ms
	// deadline).
	st := s.Stalls()
	for deadline := time.Now().Add(10 * time.Second); len(st) == 1 && st[0].End.IsZero() && time.Now().Before(deadline); st = s.Stalls() {
		time.Sleep(time.Millisecond)
	}
	if len(st) != 1 || st[0].Start.IsZero() || st[0].End.Before(st[0].Start) {
		t.Fatalf("stalls %+v", st)
	}
}

// TestHold: a Hold lets its budget through over all responses together and then holds
// them all until Release, after which every response completes with its content.
func TestHold(t *testing.T) {
	s := New(Options{Size: 100_000, Seed: 9, Throttle: 1 << 20})
	defer s.Close()
	s.Hold(30_000)
	type result struct {
		body []byte
		err  error
	}
	results := make(chan result, 2)
	for _, rng := range []string{"bytes=0-", "bytes=50000-"} {
		go func() {
			req, _ := http.NewRequest(http.MethodGet, s.URL, nil)
			req.Header.Set("Range", rng)
			resp, err := s.Client().Do(req)
			if err != nil {
				results <- result{err: err}
				return
			}
			defer resp.Body.Close()
			b, err := io.ReadAll(resp.Body)
			results <- result{b, err}
		}()
	}
	for deadline := time.Now().Add(10 * time.Second); s.BytesSent() < 30_000; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d bytes sent, want the 30000 the hold lets through", s.BytesSent())
		}
	}
	select {
	case r := <-results:
		t.Fatalf("a response ended during the hold: %d bytes, %v", len(r.body), r.err)
	case <-time.After(50 * time.Millisecond):
	}
	if got := s.BytesSent(); got != 30_000 {
		t.Fatalf("%d bytes sent during the hold, want 30000", got)
	}
	s.Release()
	for range 2 {
		r := <-results
		if r.err != nil || (len(r.body) != 100_000 && len(r.body) != 50_000) {
			t.Fatalf("after Release: %d bytes, %v", len(r.body), r.err)
		}
		if !bytes.Equal(r.body, Bytes(9, 100_000-int64(len(r.body)), int64(len(r.body)))) {
			t.Fatal("content after Release differs")
		}
	}
}

func TestHTTP2(t *testing.T) {
	s := New(Options{Size: 5000, Seed: 1, HTTP2: true})
	defer s.Close()
	c := &http.Client{Transport: s.NewTransport()}
	req, _ := http.NewRequest(http.MethodGet, s.URL, nil)
	req.Header.Set("Range", "bytes=10-19")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.ProtoMajor != 2 || resp.StatusCode != 206 || !bytes.Equal(body, Bytes(1, 10, 10)) {
		t.Fatalf("proto %s status %d", resp.Proto, resp.StatusCode)
	}
}

func TestCounters(t *testing.T) {
	s := New(Options{Size: 50_000, Seed: 3, Throttle: 500_000})
	defer s.Close()
	done := make(chan struct{})
	for range 3 {
		go func() {
			defer func() { done <- struct{}{} }()
			resp, err := s.Client().Get(s.URL)
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}()
	}
	for range 3 {
		<-done
	}
	if got := s.BytesSent(); got != 150_000 {
		t.Errorf("BytesSent = %d, want 150000", got)
	}
	if got := s.MaxConcurrent(); got < 2 || got > 3 {
		t.Errorf("MaxConcurrent = %d, want 2..3 for three overlapping throttled requests", got)
	}
	_, _ = get(t, s, map[string]string{"Range": "bytes=0-9"})
	if got := s.BytesSent(); got != 150_010 {
		t.Errorf("BytesSent after a 10-byte range = %d", got)
	}
}
