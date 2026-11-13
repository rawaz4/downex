package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"downex/internal/model"
	"downex/internal/transport"
)

func TestFakeClock(t *testing.T) {
	clk := NewFakeClock(time.Unix(0, 0))
	a := clk.NewTimer(2 * time.Second)
	b := clk.NewTimer(time.Second)
	tk := clk.NewTicker(500 * time.Millisecond)
	if clk.Pending() != 3 {
		t.Fatalf("pending %d", clk.Pending())
	}
	clk.Advance(time.Second)
	select {
	case at := <-b.C():
		if !at.Equal(time.Unix(1, 0)) {
			t.Fatalf("b fired at %v", at)
		}
	default:
		t.Fatal("b did not fire")
	}
	select {
	case <-a.C():
		t.Fatal("a fired early")
	default:
	}
	<-tk.C() // one tick buffered, the other dropped
	select {
	case <-tk.C():
		t.Fatal("ticker should drop ticks for a slow reader")
	default:
	}
	if a.Stop() != true || a.Stop() != false {
		t.Fatal("Stop result")
	}
	if a.Reset(time.Second) {
		t.Fatal("Reset of stopped timer reported active")
	}
	clk.Advance(time.Second)
	<-a.C()
	tk.Stop()
	if clk.Pending() != 0 {
		t.Fatalf("pending %d after stop", clk.Pending())
	}
}

// TestOptionDefaults pins the timing and size defaults of docs/ENGINE-SPEC.md §3, §5
// and §6 (MidStallTimeout: §5's stall of a connection whose response delivered bytes).
func TestOptionDefaults(t *testing.T) {
	o := Options{}.withDefaults()
	for _, c := range []struct {
		name      string
		got, want time.Duration
	}{
		{"RampInterval", o.RampInterval, 250 * time.Millisecond},
		{"FlushInterval", o.FlushInterval, 8 * time.Second},
		{"CheckpointInterval", o.CheckpointInterval, 2500 * time.Millisecond},
		{"StallTimeout", o.StallTimeout, 20 * time.Second},
		{"MidStallTimeout", o.MidStallTimeout, 5 * time.Second},
	} {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	// 256 MiB, not 64 MiB: each flush is a synchronous sync in the one writer goroutine,
	// and 64 MiB measurably slowed disk-bound downloads (perf-2026-10 after.md, S3/S9).
	if o.FlushBytes != 256<<20 {
		t.Errorf("FlushBytes = %d, want 256 MiB", o.FlushBytes)
	}
	if o.DefaultMaxConnections != 32 || o.HardCap != 32 {
		t.Errorf("connections %d / hard cap %d, want 32 / 32", o.DefaultMaxConnections, o.HardCap)
	}
}

func TestSleepCancel(t *testing.T) {
	clk := NewFakeClock(time.Unix(0, 0))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- sleep(ctx, clk, time.Hour) }()
	waitFor(t, time.Second, "timer", func() bool { return clk.Pending() == 1 })
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("sleep = %v", err)
	}
	go func() { done <- sleep(context.Background(), clk, time.Second) }()
	waitFor(t, time.Second, "timer", func() bool { return clk.Pending() == 1 })
	clk.Advance(time.Second)
	if err := <-done; err != nil {
		t.Fatalf("sleep = %v", err)
	}
}

func TestFrontier(t *testing.T) {
	f := frontier{next: 100}
	f.add(100, 10)
	f.add(130, 10) // gap 110-130
	f.add(150, 5)
	if f.next != 110 || len(f.pending) != 2 {
		t.Fatalf("next %d pending %v", f.next, f.pending)
	}
	f.add(140, 10) // joins 130-140 and 150-155
	if len(f.pending) != 1 || f.pending[0] != (span{130, 155}) {
		t.Fatalf("pending %v", f.pending)
	}
	f.add(110, 20)
	if f.next != 155 || len(f.pending) != 0 {
		t.Fatalf("next %d pending %v", f.next, f.pending)
	}
	f.add(120, 10) // already covered
	f.add(150, 0)
	if f.next != 155 {
		t.Fatalf("next %d", f.next)
	}
}

func TestCompactSegments(t *testing.T) {
	in := []model.Segment{
		{Start: 0, End: 99, Durable: 100},
		{Start: 100, End: 199, Durable: 100},
		{Start: 200, End: 299, Durable: 50},
		{Start: 300, End: 299}, // empty
		{Start: 300, End: 399, Durable: 0},
		{Start: 400, End: 499, Durable: 100},
	}
	got := compactSegments(in)
	want := []model.Segment{
		{ID: 0, Start: 0, End: 299, Durable: 250},
		{ID: 1, Start: 300, End: 399, Durable: 0},
		{ID: 2, Start: 400, End: 499, Durable: 100},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestLoadLayout(t *testing.T) {
	ok := []model.Segment{{Start: 100, End: 199, Durable: 3}, {Start: 0, End: 99, Durable: 100}}
	got, err := loadLayout(ok, 200)
	if err != nil || got[0].Start != 0 || got[1].Start != 100 {
		t.Fatalf("loadLayout = %v, %v", got, err)
	}
	if _, err := loadLayout(nil, 0); err != nil {
		t.Fatalf("empty: %v", err)
	}
	for name, segs := range map[string][]model.Segment{
		"gap":           {{Start: 0, End: 98}, {Start: 100, End: 199}},
		"overlap":       {{Start: 0, End: 100}, {Start: 100, End: 199}},
		"short":         {{Start: 0, End: 150}},
		"past end":      {{Start: 0, End: 200}},
		"durable > len": {{Start: 0, End: 199, Durable: 201}},
		"negative":      {{Start: 0, End: 199, Durable: -1}},
		"missing":       nil,
	} {
		if _, err := loadLayout(segs, 200); !errors.Is(err, ErrCorruptCheckpoint) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := loadLayout(nil, -1); !errors.Is(err, ErrCorruptCheckpoint) {
		t.Errorf("unknown size: %v", err)
	}
}

func TestSegmentClaimShrinkRace(t *testing.T) {
	// A worker reserving bytes while the coordinator repeatedly splits must never
	// reserve past the end it observes, and splits must never take reserved bytes.
	for range 200 {
		w := &worker{}
		s := newSegment(0, 99_999, 0, w)
		var wg sync.WaitGroup
		var reserved int64
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				k := s.claim(w, reserved, 997)
				if k == 0 {
					return
				}
				reserved += int64(k)
			}
		}()
		var cuts []int64
		for ns := int64(90_000); ns > 0; ns -= 7_919 {
			if _, ok := s.shrink(ns); ok {
				cuts = append(cuts, ns)
			}
		}
		wg.Wait()
		end, pos, _, done := s.snapshot()
		if !done || pos != end+1 || reserved != pos {
			t.Fatalf("end %d pos %d reserved %d done %v", end, pos, reserved, done)
		}
		if len(cuts) > 0 && end != cuts[len(cuts)-1]-1 {
			t.Fatalf("end %d, last cut %d", end, cuts[len(cuts)-1])
		}
	}
}

func TestSegmentOwnership(t *testing.T) {
	a, b := &worker{id: 1}, &worker{id: 2}
	s := newSegment(1000, 1999, 0, nil)
	if !s.acquire(a) || s.acquire(b) {
		t.Fatal("acquire")
	}
	if s.claim(b, 1000, 10) != 0 {
		t.Fatal("non-owner reserved bytes")
	}
	prev, ok := s.takeover(b, 1000)
	if !ok || prev != a {
		t.Fatalf("takeover = %v, %v", prev, ok)
	}
	if s.claim(a, 1000, 10) != 0 || s.claim(b, 1000, 10) != 10 {
		t.Fatal("claims after takeover")
	}
	if _, ok := s.takeover(a, 1000); ok {
		t.Fatal("takeover of a started segment")
	}
	ns, oldEnd, split := s.detach(b)
	if !split || ns != 1010 || oldEnd != 1999 {
		t.Fatalf("detach = %d %d %v", ns, oldEnd, split)
	}
	if !s.done() {
		t.Fatal("detached segment should be fully reserved")
	}
	s2 := newSegment(0, 9, 0, a)
	if _, _, split := s2.detach(a); split || s2.owner != nil {
		t.Fatal("detach of an unstarted segment should only release it")
	}
	s3 := newSegment(0, -1, 0, a)
	s3.claim(a, 0, 5)
	s3.finishOpen(a, 5)
	if !s3.done() {
		t.Fatal("open segment not finished")
	}
	resumed := newSegment(100, 199, 40, nil)
	if resumed.base != 140 || resumed.pos != 140 || resumed.remaining() != 60 {
		t.Fatalf("resumed segment %+v", resumed)
	}
}

func TestStreamPushBack(t *testing.T) {
	body := io.NopCloser(strings.NewReader("abcdefghij"))
	st := newStream(body, 100, 109)
	buf := make([]byte, 4)
	n, fromLeft, err := st.read(buf)
	if n != 4 || fromLeft || err != nil || st.off != 104 {
		t.Fatalf("read %d %v %v off %d", n, fromLeft, err, st.off)
	}
	st.unread(buf[2:4])
	if st.off != 102 || !st.more() {
		t.Fatalf("off %d", st.off)
	}
	n, fromLeft, _ = st.read(make([]byte, 8))
	if n != 2 || !fromLeft || st.off != 104 {
		t.Fatalf("leftover read %d %v off %d", n, fromLeft, st.off)
	}
	rest, _ := io.ReadAll(readerFunc(func(p []byte) (int, error) { n, _, err := st.read(p); return n, err }))
	if string(rest) != "efghij" || st.more() {
		t.Fatalf("rest %q", rest)
	}
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

func TestClassifyProbe(t *testing.T) {
	cr := func(s, e, tot int64) *transport.ContentRange {
		return &transport.ContentRange{Start: s, End: e, Total: tot}
	}
	for _, tc := range []struct {
		name string
		resp transport.Response
		want probeResult
		err  error
	}{
		{"ranged", transport.Response{StatusCode: 206, ContentRange: cr(0, 999, 1000)},
			probeResult{mode: model.ModeRanged, size: 1000, streamEnd: 999}, nil},
		{"partial first response", transport.Response{StatusCode: 206, ContentRange: cr(0, 99, 1000)},
			probeResult{mode: model.ModeRanged, size: 1000, streamEnd: 99}, nil},
		{"200 is single", transport.Response{StatusCode: 200, ContentLength: 1000},
			probeResult{mode: model.ModeSingle, size: 1000, streamEnd: 999}, nil},
		{"200 unknown length", transport.Response{StatusCode: 200, ContentLength: -1},
			probeResult{mode: model.ModeSingle, size: -1, streamEnd: -1}, nil},
		{"200 empty", transport.Response{StatusCode: 200, ContentLength: 0},
			probeResult{mode: model.ModeSingle, size: 0, streamEnd: -1, empty: true}, nil},
		{"accept-ranges none", transport.Response{StatusCode: 206, ContentRange: cr(0, 999, 1000), AcceptRangesNone: true},
			probeResult{mode: model.ModeSingle, size: 1000, streamEnd: 999}, nil},
		{"accept-ranges none, partial", transport.Response{StatusCode: 206, ContentRange: cr(0, 999, 5000), AcceptRangesNone: true},
			probeResult{mode: model.ModeRanged, size: 5000, streamEnd: 999}, nil},
		{"unknown total", transport.Response{StatusCode: 206, ContentRange: cr(0, 999, -1)},
			probeResult{mode: model.ModeSingle, size: -1, streamEnd: 999, chained: true}, nil},
		{"206 without content-range", transport.Response{StatusCode: 206, ContentLength: 1000}, probeResult{}, ErrRangeIgnored},
		{"wrong start", transport.Response{StatusCode: 206, ContentRange: cr(5, 999, 1000)}, probeResult{}, ErrRangeIgnored},
		{"416 empty", transport.Response{StatusCode: 416, ContentRange: cr(-1, -1, 0)},
			probeResult{mode: model.ModeRanged, size: 0, streamEnd: -1, empty: true}, nil},
	} {
		got, err := classifyProbe(&tc.resp)
		if !errors.Is(err, tc.err) || (tc.err == nil && got != tc.want) {
			t.Errorf("%s: got %+v, %v; want %+v, %v", tc.name, got, err, tc.want, tc.err)
		}
	}
	if _, err := classifyProbe(&transport.Response{StatusCode: 416}); err == nil {
		t.Error("416 without total accepted")
	}
}

func TestCheckRangeResponse(t *testing.T) {
	strong := model.Validators{ETag: `"a"`, StrongETag: true}
	cr := func(s, e, tot int64) *transport.ContentRange {
		return &transport.ContentRange{Start: s, End: e, Total: tot}
	}
	for _, tc := range []struct {
		name string
		resp transport.Response
		ifr  bool
		want error
	}{
		{"ok", transport.Response{StatusCode: 206, ContentRange: cr(100, 199, 1000)}, true, nil},
		{"200 after If-Range", transport.Response{StatusCode: 200}, true, ErrValidatorChanged},
		{"200 without validator", transport.Response{StatusCode: 200}, false, ErrRangeIgnored},
		{"416", transport.Response{StatusCode: 416}, true, ErrValidatorChanged},
		{"size changed", transport.Response{StatusCode: 206, ContentRange: cr(100, 199, 2000)}, true, ErrValidatorChanged},
		{"wrong start", transport.Response{StatusCode: 206, ContentRange: cr(0, 199, 1000)}, true, ErrRangeIgnored},
		{"no content-range", transport.Response{StatusCode: 206}, true, ErrRangeIgnored},
		{"etag changed", transport.Response{StatusCode: 206, ContentRange: cr(100, 199, 1000),
			Validators: model.Validators{ETag: `"b"`, StrongETag: true}}, true, ErrValidatorChanged},
		{"etag changed to weak", transport.Response{StatusCode: 206, ContentRange: cr(100, 199, 1000),
			Validators: model.Validators{ETag: `W/"b"`}}, false, ErrValidatorChanged},
		{"same opaque tag, now weak", transport.Response{StatusCode: 206, ContentRange: cr(100, 199, 1000),
			Validators: model.Validators{ETag: `W/"a"`}}, false, nil},
		{"last-modified changed", transport.Response{StatusCode: 206, ContentRange: cr(100, 199, 1000),
			Validators: model.Validators{LastModified: "Thu, 05 Mar 2026 05:06:07 GMT"}}, false, ErrValidatorChanged},
		{"last-modified equal in another form", transport.Response{StatusCode: 206, ContentRange: cr(100, 199, 1000),
			Validators: model.Validators{LastModified: "Wednesday, 04-Mar-26 05:06:07 GMT"}}, false, nil},
	} {
		recorded := strong
		recorded.LastModified = "Wed, 04 Mar 2026 05:06:07 GMT"
		err := checkRangeResponse(&tc.resp, 100, 1000, tc.ifr, recorded)
		if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	var se *StatusError
	err := checkRangeResponse(&transport.Response{StatusCode: 429, RetryAfter: time.Second}, 0, 10, true, strong)
	if !errors.As(err, &se) || se.RetryAfter != time.Second || !isTransient(err) || !isThrottle(err) {
		t.Fatalf("429: %v", err)
	}
}

func TestErrorClassification(t *testing.T) {
	transient := []error{io.ErrUnexpectedEOF, errors.New("connection reset"), &StatusError{Code: 503},
		&StatusError{Code: 500}, fmt.Errorf("x: %w", &StatusError{Code: 429}), ErrStalled}
	for _, err := range transient {
		if !isTransient(err) {
			t.Errorf("%v should be transient", err)
		}
	}
	permanent := []error{context.Canceled, &StatusError{Code: 404}, &StatusError{Code: 403}, ErrValidatorChanged,
		ErrRangeIgnored, fmt.Errorf("w: %w", ErrDiskFull), transport.ErrTLS, ErrTLS, transport.ErrRedirects,
		fmt.Errorf("fetch: %w", transport.ErrInsecureRedirect),
		fmt.Errorf("fetch: %w", transport.ErrInvalidRequest), ErrInvalidRequest, nil}
	for _, err := range permanent {
		if isTransient(err) {
			t.Errorf("%v should not be transient", err)
		}
	}
	if !isDiskFull(&os.PathError{Op: "write", Path: "x", Err: syscall.ENOSPC}) {
		t.Error("ENOSPC not mapped")
	}
	if err := diskError("write", &os.PathError{Op: "write", Path: "x", Err: syscall.ENOSPC}); !errors.Is(err, ErrDiskFull) {
		t.Errorf("diskError = %v", err)
	}
	if isDiskFull(errors.New("other")) {
		t.Error("false positive")
	}
	o := Options{}.withDefaults()
	if d := backoffDelay(&o, 1, 0); d < 400*time.Millisecond || d > 600*time.Millisecond {
		t.Errorf("first backoff %v", d)
	}
	if d := backoffDelay(&o, 30, 0); d > 36*time.Second {
		t.Errorf("capped backoff %v", d)
	}
	if d := backoffDelay(&o, 3, 7*time.Second); d != 7*time.Second {
		t.Errorf("Retry-After not honoured: %v", d)
	}
	if !strings.Contains((&StatusError{Code: http.StatusTooManyRequests}).Error(), "429") {
		t.Error("status text")
	}
}

func TestNamesAndPromote(t *testing.T) {
	for _, tc := range []struct {
		name string
		n    int
		want string
	}{
		{"a.zip", 0, "a.zip"}, {"a.zip", 1, "a (1).zip"}, {"a.tar.gz", 2, "a.tar (2).gz"},
		{"noext", 3, "noext (3)"}, {".profile", 1, ".profile (1)"},
	} {
		if got := numbered(tc.name, tc.n); got != tc.want {
			t.Errorf("numbered(%q, %d) = %q, want %q", tc.name, tc.n, got, tc.want)
		}
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.bin"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := New(newMemStore(), nil, Options{})
	ctx := context.Background()
	n1, err := e.reserve(ctx, dir, "f.bin", 0, false)
	if err != nil || n1 != "f (1).bin" {
		t.Fatalf("reserve = %q, %v", n1, err)
	}
	n2, err := e.reserve(ctx, dir, "f.bin", 0, false) // f (1).bin.dxpart is reserved now
	if err != nil || n2 != "f (2).bin" {
		t.Fatalf("second reserve = %q, %v", n2, err)
	}
	// The reserved final name gets taken before completion: promote picks another.
	if err := os.WriteFile(filepath.Join(dir, n1), []byte("intruder"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(partPath(dir, n1), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	var variants []string
	final, err := e.promote(ctx, dir, n1, 0, false, func(v string) error { variants = append(variants, v); return nil })
	if err != nil || final != "f (1) (1).bin" || !slices.Equal(variants, []string{final}) {
		t.Fatalf("promote = %q, %v (variants announced %q)", final, err, variants)
	}
	for name, want := range map[string]string{"f.bin": "keep", n1: "intruder", final: "data"} {
		if got, _ := os.ReadFile(filepath.Join(dir, name)); string(got) != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestBucket(t *testing.T) {
	clk := NewFakeClock(time.Unix(0, 0))
	b := newBucket(clk, 64<<10) // burst 32 KiB
	if b.Limit() != 64<<10 {
		t.Fatal("limit")
	}
	if d := b.reserve(32 << 10); d != 0 {
		t.Fatalf("burst should pass, wait %v", d)
	}
	if d := b.reserve(32 << 10); d != 500*time.Millisecond {
		t.Fatalf("wait %v, want 500ms", d)
	}
	done := make(chan error)
	go func() { done <- b.WaitN(context.Background(), 1) }()
	waitFor(t, time.Second, "limiter wait", func() bool { return clk.Pending() == 1 })
	clk.Advance(time.Second)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRequestFor(t *testing.T) {
	h := http.Header{}
	h.Set("Range", "bytes=5-")
	h.Set("If-None-Match", `"x"`)
	h.Set("Cookie", "a=b")
	h.Set("Accept-Encoding", "gzip")
	r := requestFor(model.Request{URL: "http://x/", Headers: h})
	if r.Method != http.MethodGet || r.Headers.Get("Range") != "" || r.Headers.Get("If-None-Match") != "" ||
		r.Headers.Get("Cookie") != "a=b" || r.Headers.Get("Accept-Encoding") != "identity" {
		t.Fatalf("headers %v method %q", r.Headers, r.Method)
	}
	if h.Get("Range") == "" {
		t.Fatal("caller's headers were modified")
	}
}
