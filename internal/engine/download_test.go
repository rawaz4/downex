package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"downex/internal/model"
	"downex/internal/testserver"
)

// fastOpts makes ramp-up quick so that many connections are exercised in tests.
func fastOpts() Options {
	return Options{RampInterval: time.Millisecond, RetryBaseDelay: 10 * time.Millisecond, RetryMaxDelay: 50 * time.Millisecond}
}

func TestDownloadMatrix(t *testing.T) {
	sizes := []int64{0, 1, 11_679, 23_363, 1 << 20, 50 << 20}
	conns := []int{1, 8, 32}
	for _, size := range sizes {
		for _, c := range conns {
			t.Run(fmt.Sprintf("size=%d/conns=%d", size, c), func(t *testing.T) {
				t.Parallel()
				so := testserver.Options{Size: size, Seed: uint64(size) + 7, ETag: `"m1"`}
				if size == 1<<20 {
					// Slow enough for splits, takeovers and ramp-up to happen: one
					// connection would need about 2 s, well past the ramp-up's worth-it
					// guard (500 ms, ENGINE-SPEC §3).
					so.Throttle = 512 << 10
				}
				h := newHarness(t, so, fastOpts())
				d := h.add("/file.bin", "", c)
				onProgress, peak := maxConns()
				if err := h.run(d.ID, nil, onProgress); err != nil {
					t.Fatalf("Run: %v", err)
				}
				got := h.verify(d.ID)
				if got.Filename != "file.bin" || got.Mode != model.ModeRanged {
					t.Fatalf("filename %q mode %v", got.Filename, got.Mode)
				}
				if peak() > c {
					t.Fatalf("peak connections %d exceed limit %d", peak(), c)
				}
				segs := h.st.segs(d.ID)
				if size > 0 && (len(segs) != 1 || !segs[0].Done() || segs[0].End != size-1) {
					t.Fatalf("final segments %+v", segs)
				}
				if size == 1<<20 && c > 1 {
					// The case exists for the multi-connection paths: the ramp-up's worth-it
					// guard must not keep it on one connection.
					if n := len(h.srv.Requests()); n < 2 || h.fac.slots.Load() < 2 {
						t.Fatalf("%d requests on %d slots, want ramp-up to at least 2 connections", n, h.fac.slots.Load())
					}
				}
			})
		}
	}
}

// TestManyConnections drives a throttled download to the 32-connection cap and checks
// that no byte is written twice and that checkpoints only claim synced bytes.
func TestManyConnections(t *testing.T) {
	t.Parallel()
	const size = 8 << 20
	opts := fastOpts()
	opts.FlushInterval = 50 * time.Millisecond
	opts.CheckpointInterval = 20 * time.Millisecond
	rec := &recorder{t: t}
	opts.openFile = rec.open
	h := newHarness(t, testserver.Options{Size: size, Seed: 3, Throttle: 1 << 20, ETag: `"x"`}, opts)
	h.st.onSave = func(d model.Download, segs []model.Segment) {
		f := rec.file()
		if f == nil {
			return
		}
		for _, s := range segs {
			if !f.syncedCovers(s.Start, s.Start+s.Durable) {
				t.Errorf("checkpoint claims [%d,%d) durable before it was synced", s.Start, s.Start+s.Durable)
			}
		}
	}
	d := h.add("/many.bin", "", 32)
	onProgress, peak := maxConns()
	if err := h.run(d.ID, nil, onProgress); err != nil {
		t.Fatalf("Run: %v", err)
	}
	h.verify(d.ID)
	if peak() < 16 {
		t.Fatalf("peak connections %d, want ramp-up towards 32", peak())
	}
	if p := peak(); p > 32 {
		t.Fatalf("peak %d above hard cap", p)
	}
	if spans := rec.file().writtenSpans(); len(spans) != 1 || spans[0] != (span{0, size}) {
		t.Fatalf("written spans %v", spans)
	}
	if n := h.fac.slots.Load(); n < 16 {
		t.Fatalf("only %d transport slots used", n)
	}
}

func TestSingleModeNoRanges(t *testing.T) {
	t.Parallel()
	for name, so := range map[string]testserver.Options{
		"200 only":           {Size: 3 << 20, Seed: 11, NoRanges: true},
		"accept-ranges none": {Size: 3 << 20, Seed: 12, AcceptRangesNone: true},
		"empty 200":          {Size: 0, NoRanges: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, so, fastOpts())
			d := h.add("/single.dat", "", 8)
			onProgress, peak := maxConns()
			if err := h.run(d.ID, nil, onProgress); err != nil {
				t.Fatalf("Run: %v", err)
			}
			got := h.verify(d.ID)
			if got.Mode != model.ModeSingle {
				t.Fatalf("mode %v", got.Mode)
			}
			if peak() > 1 || h.fac.slots.Load() != 1 {
				t.Fatalf("single mode used %d connections, %d slots", peak(), h.fac.slots.Load())
			}
			if n := len(h.srv.Requests()); n != 1 {
				t.Fatalf("%d requests in single mode", n)
			}
		})
	}
}

func TestSingleModeRestartsAfterFailure(t *testing.T) {
	t.Parallel()
	// The first response stalls; single mode cannot resume, so it starts over.
	opts := fastOpts()
	opts.StallTimeout = 200 * time.Millisecond
	h := newHarness(t, testserver.Options{Size: 1 << 20, Seed: 5, NoRanges: true, StallAfter: 100 << 10, StallCount: 1}, opts)
	d := h.add("/s.bin", "", 4)
	if err := h.run(d.ID, nil, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	checkStallCut(t, h.srv, opts.StallTimeout, true)
	h.verify(d.ID)
	if n := len(h.srv.Requests()); n != 2 {
		t.Fatalf("%d requests, want 2", n)
	}
}

func TestHTTP2(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testserver.Options{Size: 4 << 20, Seed: 21, HTTP2: true, Throttle: 2 << 20,
		LastModified: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}, fastOpts())
	d := h.add("/h2.bin", "", 8)
	// Held after half the file until a second request came: at 2 MiB/s a loaded machine
	// could otherwise let the probe finish before a ramp-up step.
	h.srv.Hold(2 << 20)
	defer h.srv.Release()
	done := make(chan error, 1)
	go func() { done <- h.run(d.ID, nil, nil) }()
	waitFor(t, 30*time.Second, "a second request", func() bool { return len(h.srv.Requests()) >= 2 })
	h.srv.Release()
	if err := awaitRun(t, done); err != nil {
		t.Fatalf("Run: %v", err)
	}
	h.verify(d.ID)
	reqs := h.srv.Requests()
	for _, r := range reqs {
		if r.Proto != "HTTP/2.0" {
			t.Fatalf("request over %s", r.Proto)
		}
	}
	if len(reqs) < 2 {
		t.Fatalf("only %d requests; expected segmented download", len(reqs))
	}
	// Without a strong ETag, later requests carry Last-Modified in If-Range.
	for _, r := range reqs[1:] {
		if r.IfRange != "Fri, 02 Jan 2026 03:04:05 GMT" {
			t.Fatalf("If-Range %q", r.IfRange)
		}
	}
}

func TestFilenameSelection(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testserver.Options{Size: 50_000, Seed: 1, Filename: "Report Q3.pdf"}, fastOpts())
	if err := os.WriteFile(filepath.Join(h.dir, "Report Q3.pdf"), []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := h.add("/download?id=7", "", 4)
	if err := h.run(d.ID, nil, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := h.verify(d.ID)
	if got.Filename != "Report Q3 (1).pdf" {
		t.Fatalf("filename %q", got.Filename)
	}
	if b, _ := os.ReadFile(filepath.Join(h.dir, "Report Q3.pdf")); string(b) != "existing" {
		t.Fatal("existing file was overwritten")
	}

	// An explicit filename is reserved at Add time.
	d2, err := h.eng.Add(context.Background(), model.Request{URL: h.srv.URL + "/x"}, h.dir, "Report Q3.pdf", 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if d2.Filename != "Report Q3 (2).pdf" {
		t.Fatalf("explicit name %q", d2.Filename)
	}
	if err := h.run(d2.ID, nil, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	h.verify(d2.ID)

	// Running a completed download is a no-op.
	if err := h.run(d2.ID, nil, nil); err != nil {
		t.Fatalf("second Run: %v", err)
	}
}

func TestAddValidation(t *testing.T) {
	e := New(newMemStore(), &httpFactory{}, Options{})
	for _, u := range []string{"ftp://x/y", "not a url", "http://", ""} {
		if _, err := e.Add(context.Background(), model.Request{URL: u}, t.TempDir(), "", 0, 0); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("Add(%q) = %v", u, err)
		}
	}
	d, err := e.Add(context.Background(), model.Request{URL: "https://example.test/a"}, t.TempDir(), "", 99, 0)
	if err != nil || d.MaxConnections != HardMaxConnections || d.State != model.StateQueued || d.Size != -1 {
		t.Fatalf("Add = %+v, %v", d, err)
	}
}

func TestSpeedCapUsesSmallSlices(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testserver.Options{Size: 2 << 20, Seed: 9}, fastOpts())
	d := h.add("/capped.bin", "", 4)
	lim := &countingLimiter{}
	lim.SetLimit(1 << 40) // a cap that is never reached keeps the test fast
	if err := h.run(d.ID, lim, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	h.verify(d.ID)
	calls, maxN, total := lim.stats()
	if total != 2<<20 || maxN > sliceCapped || calls < (2<<20)/sliceCapped {
		t.Fatalf("limiter saw %d calls, max %d, total %d", calls, maxN, total)
	}
}

func TestSpeedLimitWithoutLimiter(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testserver.Options{Size: 600 << 10, Seed: 4}, fastOpts())
	d, err := h.eng.Add(context.Background(), model.Request{URL: h.srv.URL + "/slow.bin"}, h.dir, "", 4, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := h.run(d.ID, nil, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	h.verify(d.ID)
	// 600 KiB at 1 MiB/s with a 256 KiB burst needs at least ~300 ms.
	if el := time.Since(start); el < 250*time.Millisecond {
		t.Fatalf("finished in %v; the speed limit was not applied", el)
	}
}
