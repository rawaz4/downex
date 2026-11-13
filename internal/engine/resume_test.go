package engine

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"downex/internal/model"
	"downex/internal/testserver"
)

// pauseMidway starts a download, cancels it once at least minReceived bytes arrived and
// returns Run's error. The server holds every response once it has sent twice
// minReceived bytes (the slack covers bytes a closed response leaves unread), so the
// download cannot complete before the cancel, however late a loaded machine reports
// its progress; the hold ends with the pause.
func pauseMidway(t *testing.T, h *harness, id model.DownloadID, minReceived int64) error {
	t.Helper()
	if size := h.srv.Options().Size; 2*minReceived >= size {
		t.Fatalf("pauseMidway after %d of %d bytes: the hold would not stop the download", minReceived, size)
	}
	h.srv.Hold(2 * minReceived)
	defer h.srv.Release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- h.eng.Run(ctx, id, nil, func(p Progress) {
			if p.Received >= minReceived {
				cancel()
			}
		})
	}()
	return awaitRun(t, done)
}

func resumeOpts() Options {
	o := fastOpts()
	o.FlushInterval = 50 * time.Millisecond
	return o
}

func TestPauseResume(t *testing.T) {
	t.Parallel()
	const size = 6 << 20
	h := newHarness(t, testserver.Options{Size: size, Seed: 31, ETag: `"v1"`, Throttle: 1 << 20, Filename: "movie.mkv"}, resumeOpts())
	d := h.add("/movie", "", 4)

	err := pauseMidway(t, h, d.ID, 1<<20)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run after cancel = %v, want context.Canceled", err)
	}
	paused := h.st.get(t, d.ID)
	if paused.State != model.StatePaused || paused.Mode != model.ModeRanged || paused.Size != size {
		t.Fatalf("paused record %+v", paused)
	}
	segs := h.st.segs(d.ID)
	if _, err := loadLayout(segs, size); err != nil {
		t.Fatalf("stored layout: %v", err)
	}
	var durable int64
	for _, s := range segs {
		durable += s.Durable
	}
	if durable == 0 || durable == size {
		t.Fatalf("durable bytes after pause = %d", durable)
	}
	// Every durable byte on disk must already be correct.
	part, err := os.ReadFile(partPath(paused.Dir, paused.Filename))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range segs {
		for i := s.Start; i < s.Start+s.Durable; i++ {
			if part[i] != testserver.Byte(31, i) {
				t.Fatalf("durable byte %d is wrong", i)
			}
		}
	}

	before := len(h.srv.Requests())
	if err := h.run(d.ID, nil, nil); err != nil {
		t.Fatalf("resume: %v", err)
	}
	got := h.verify(d.ID)
	if got.Filename != "movie.mkv" {
		t.Fatalf("filename %q", got.Filename)
	}
	for _, r := range h.srv.Requests()[before:] {
		if r.IfRange != `"v1"` || r.Range == "" || r.Range == "bytes=0-" {
			t.Fatalf("resumed request %+v lacks If-Range or a bounded range", r)
		}
	}
}

func TestResumeValidatorChanged(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]func(*testserver.Options){
		"etag changed": func(o *testserver.Options) { o.ETag = `"v2"`; o.Seed = 99 },
		"size changed": func(o *testserver.Options) { o.Size += 1000 },
		"416":          func(o *testserver.Options) { o.Always416 = true },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, testserver.Options{Size: 4 << 20, Seed: 41, ETag: `"v1"`, Throttle: 1 << 20}, resumeOpts())
			d := h.add("/f.zip", "", 4)
			if err := pauseMidway(t, h, d.ID, 512<<10); !errors.Is(err, context.Canceled) {
				t.Fatalf("pause: %v", err)
			}
			h.srv.Update(change)
			err := h.run(d.ID, nil, nil)
			if !errors.Is(err, ErrValidatorChanged) {
				t.Fatalf("resume = %v, want ErrValidatorChanged", err)
			}
			failed := h.st.get(t, d.ID)
			if failed.State != model.StateFailed || failed.Error == "" {
				t.Fatalf("record after validator change: %+v", failed)
			}
			// Partial progress is kept until the caller decides to restart.
			var durable int64
			for _, s := range h.st.segs(d.ID) {
				durable += s.Durable
			}
			if durable == 0 {
				t.Fatal("progress discarded on failure")
			}

			h.srv.Update(func(o *testserver.Options) { o.Always416 = false; o.Throttle = 0 })
			if err := h.eng.Restart(context.Background(), d.ID, RestartOptions{}); err != nil {
				t.Fatalf("Restart: %v", err)
			}
			if r := h.st.get(t, d.ID); r.Mode != model.ModeUnknown || r.Size != -1 || len(h.st.segs(d.ID)) != 0 {
				t.Fatalf("record after restart %+v", r)
			}
			if err := h.run(d.ID, nil, nil); err != nil {
				t.Fatalf("Run after restart: %v", err)
			}
			if got := h.verify(d.ID); got.Filename != "f.zip" {
				t.Fatalf("filename after restart %q", got.Filename)
			}
		})
	}
}

func TestResumeMissingPartFile(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testserver.Options{Size: 2 << 20, Seed: 51, ETag: `"e"`, Throttle: 1 << 20}, resumeOpts())
	d := h.add("/gone.bin", "", 2)
	if err := pauseMidway(t, h, d.ID, 256<<10); !errors.Is(err, context.Canceled) {
		t.Fatalf("pause: %v", err)
	}
	r := h.st.get(t, d.ID)
	if err := os.Remove(partPath(r.Dir, r.Filename)); err != nil {
		t.Fatal(err)
	}
	h.srv.Update(func(o *testserver.Options) { o.Throttle = 0 })
	if err := h.run(d.ID, nil, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	h.verify(d.ID)
}

func TestResumeTruncatedPartFile(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testserver.Options{Size: 2 << 20, Seed: 52, ETag: `"e"`, Throttle: 1 << 20}, resumeOpts())
	d := h.add("/t.bin", "", 2)
	if err := pauseMidway(t, h, d.ID, 256<<10); !errors.Is(err, context.Canceled) {
		t.Fatalf("pause: %v", err)
	}
	r := h.st.get(t, d.ID)
	if err := os.Truncate(partPath(r.Dir, r.Filename), 1000); err != nil {
		t.Fatal(err)
	}
	if err := h.run(d.ID, nil, nil); !errors.Is(err, ErrCorruptCheckpoint) {
		t.Fatalf("Run = %v, want ErrCorruptCheckpoint", err)
	}
}

func TestConcurrentRunRejected(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testserver.Options{Size: 1 << 20, Seed: 1, Throttle: 256 << 10}, fastOpts())
	d := h.add("/c.bin", "", 2)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.eng.Run(ctx, d.ID, nil, nil) }()
	waitFor(t, 5*time.Second, "first run to start", func() bool { return len(h.srv.Requests()) > 0 })
	if err := h.eng.Run(context.Background(), d.ID, nil, nil); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Run = %v", err)
	}
	if err := h.eng.Restart(context.Background(), d.ID, RestartOptions{}); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("Restart while running = %v", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("first Run = %v", err)
	}
}

// TestInterruptedRestartStaysResumable (L8): Restart stores the reset record before it
// empties the part file. When it is interrupted in between (here the save fails; a
// crash or kill is the same), the download is still consistent and resumes, instead of
// keeping progress that the emptied part file no longer holds (ErrCorruptCheckpoint,
// from which only another restart recovers).
func TestInterruptedRestartStaysResumable(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testserver.Options{Size: 4 << 20, Seed: 47, ETag: `"r"`, Throttle: 1 << 20}, resumeOpts())
	d := h.add("/restart.bin", "", 4)
	if err := pauseMidway(t, h, d.ID, 512<<10); !errors.Is(err, context.Canceled) {
		t.Fatalf("pause: %v", err)
	}
	h.st.mu.Lock()
	h.st.saveErr = errors.New("injected: disk I/O error")
	h.st.mu.Unlock()
	if err := h.eng.Restart(context.Background(), d.ID, RestartOptions{}); err == nil {
		t.Fatal("Restart succeeded although its record could not be saved")
	}
	h.st.mu.Lock()
	h.st.saveErr = nil
	h.st.mu.Unlock()
	h.srv.Update(func(o *testserver.Options) { o.Throttle = 0 })
	if err := h.run(d.ID, nil, nil); err != nil {
		t.Fatalf("Run after an interrupted Restart: %v", err)
	}
	h.verify(d.ID)
}
