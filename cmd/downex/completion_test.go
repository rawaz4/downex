package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
)

// lineOf returns the first line of out that starts with prefix ("" when none).
func lineOf(out, prefix string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	return ""
}

// TestInfoShowsTimeTaken: info prints when a download first started and how long its
// runs took (pauses excluded); a running daemon's live total includes the current run.
func TestInfoShowsTimeTaken(t *testing.T) {
	db := filepath.Join(t.TempDir(), "downex.db")
	st, _, err := openStore(db)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	for _, d := range []*model.Download{
		{Request: model.Request{URL: "https://example.test/done.iso"}, Dir: dir, Filename: "done.iso", Size: 10,
			Mode: model.ModeRanged, State: model.StateCompleted, CompletedAt: started.Add(2 * time.Hour),
			StartedAt: started, ActiveMS: 65_000, CompletionDialog: true},
		{Request: model.Request{URL: "https://example.test/run.iso"}, Dir: dir, Filename: "run.iso", Size: 10,
			Mode: model.ModeRanged, State: model.StateDownloading, StartedAt: started, ActiveMS: 4000},
		{Request: model.Request{URL: "https://example.test/new.iso"}, Dir: dir, Size: -1, State: model.StatePaused},
	} {
		if err := st.CreateDownload(context.Background(), d); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	f, h := newFakeDaemon(t)
	ctx := context.Background()
	f.do(func() {
		f.downloads[2] = &downexv1.Download{Id: 2, Size: 10, State: downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING,
			ActiveMs: 9500, StartedAt: timestamppb.New(started), CompletionDialog: true}
	})

	r := h.run(ctx, "info", "1", "--db", db)
	if r.code != exitOK {
		t.Fatalf("info 1: %v", r)
	}
	if l := lineOf(r.stdout, "Started:"); !strings.Contains(l, formatTime(started)) {
		t.Errorf("started line %q in\n%s", l, r.stdout)
	}
	if l := lineOf(r.stdout, "Time taken:"); !strings.HasSuffix(l, " 1m05s") {
		t.Errorf("time taken line %q in\n%s", l, r.stdout)
	}
	if lineOf(r.stdout, "Complete dialog:") == "" {
		t.Errorf("no complete dialog line in\n%s", r.stdout)
	}
	r = h.run(ctx, "info", "1", "--json", "--db", db)
	var v infoView
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil || v.ActiveMS != 65_000 || v.StartedAt == nil ||
		!v.StartedAt.Equal(started) || !v.CompletionDialog {
		t.Fatalf("info --json: %+v %v\n%s", v, err, r.stdout)
	}

	// Running in the daemon: its live total, marked during the run.
	r = h.run(ctx, "info", "2", "--db", db)
	if l := lineOf(r.stdout, "Time taken:"); r.code != exitOK || !strings.HasSuffix(l, " 9.5s so far") ||
		lineOf(r.stdout, "Complete dialog:") == "" {
		t.Errorf("running: time taken line %q in\n%s", l, r.stdout)
	}
	// Without the daemon: the stored total.
	h.setUp(false)
	if r = h.run(ctx, "info", "2", "--db", db); !strings.HasSuffix(lineOf(r.stdout, "Time taken:"), " 4.0s so far") ||
		lineOf(r.stdout, "Complete dialog:") != "" {
		t.Errorf("stored: %v", r)
	}
	// Never started: neither line.
	if r = h.run(ctx, "info", "3", "--db", db); r.code != exitOK || lineOf(r.stdout, "Started:") != "" ||
		lineOf(r.stdout, "Time taken:") != "" {
		t.Errorf("never started: %v", r)
	}
}

// TestInfoShowsAverageSpeed: right after the time taken, info prints the average speed of
// the whole download (its bytes over the time its runs took, pauses excluded); while
// downexd runs it, from the daemon's live bytes and running total.
func TestInfoShowsAverageSpeed(t *testing.T) {
	db := filepath.Join(t.TempDir(), "downex.db")
	st, _, err := openStore(db)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	for _, d := range []*model.Download{
		// 600 MiB in a minute of running (an hour with its pauses)
		{Request: model.Request{URL: "https://example.test/done.iso"}, Dir: dir, Filename: "done.iso", Size: 600 << 20,
			Mode: model.ModeRanged, State: model.StateCompleted, CompletedAt: started.Add(time.Hour),
			StartedAt: started, ActiveMS: 60_000},
		{Request: model.Request{URL: "https://example.test/run.iso"}, Dir: dir, Filename: "run.iso", Size: 100 << 20,
			Mode: model.ModeRanged, State: model.StateDownloading, StartedAt: started, ActiveMS: 4000},
		{Request: model.Request{URL: "https://example.test/new.iso"}, Dir: dir, Size: -1, State: model.StatePaused},
	} {
		if err := st.CreateDownload(context.Background(), d); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	f, h := newFakeDaemon(t)
	ctx := context.Background()
	f.do(func() {
		f.downloads[2] = &downexv1.Download{Id: 2, Size: 100 << 20, Received: 50 << 20,
			State: downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING, ActiveMs: 10_000, StartedAt: timestamppb.New(started)}
	})

	r := h.run(ctx, "info", "1", "--db", db)
	lines := strings.Split(r.stdout, "\n")
	i := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "Time taken:") })
	if r.code != exitOK || i < 0 || i+1 == len(lines) || !strings.HasPrefix(lines[i+1], "Average speed:") ||
		!strings.HasSuffix(lines[i+1], " 10.5 MB/s") {
		t.Errorf("completed: no average speed after the time taken in\n%s", r.stdout)
	}
	// Running in the daemon: its live bytes over its live total.
	if r = h.run(ctx, "info", "2", "--db", db); !strings.HasSuffix(lineOf(r.stdout, "Average speed:"), " 5.2 MB/s so far") {
		t.Errorf("running: %v", r)
	}
	// Never started: no average.
	if r = h.run(ctx, "info", "3", "--db", db); r.code != exitOK || lineOf(r.stdout, "Average speed:") != "" {
		t.Errorf("never started: %v", r)
	}
}
