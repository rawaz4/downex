package main

import (
	"bytes"
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	downexv1 "downex/internal/rpc/downex/v1"
)

const (
	stQueued      = downexv1.DownloadState_DOWNLOAD_STATE_QUEUED
	stProbing     = downexv1.DownloadState_DOWNLOAD_STATE_PROBING
	stDownloading = downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING
	stPaused      = downexv1.DownloadState_DOWNLOAD_STATE_PAUSED
	stCompleted   = downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED
	stFailed      = downexv1.DownloadState_DOWNLOAD_STATE_FAILED
)

// active returns download id while it downloads size bytes, received of them so far.
func active(id, size, received int64) *downexv1.Download {
	return &downexv1.Download{Id: id, Url: "https://x.test/f" + idArg(id), Filename: "f" + idArg(id) + ".bin",
		Dir: "/dl", Size: size, Received: received, State: stDownloading, Mode: downexv1.Mode_MODE_RANGED,
		SpeedBps: 1 << 20, Connections: 4}
}

func TestTableRow(t *testing.T) {
	d := &downexv1.Download{Id: 3, Filename: "big.iso", Size: 100 << 20, Received: 25 << 20,
		State: stDownloading, SpeedBps: 5 << 20, Connections: 8}
	if got, want := tableRow(4, d), "3     downloading     25.0%      5.2 MB/s      8     15s  big.iso"; got != want {
		t.Errorf("row\n%q, want\n%q", got, want)
	}
	if got, want := tableHeader(4), "ID    STATE        PROGRESS         SPEED  CONNS     ETA  NAME"; got != want {
		t.Errorf("header\n%q, want\n%q", got, want)
	}
	for _, tc := range []struct {
		d    *downexv1.Download
		want []string // the row's fields
	}{
		{&downexv1.Download{Id: 4, Url: "https://x.test/next", Size: -1, State: stQueued, QueuePosition: 2},
			[]string{"4", "queued", "#2", "-", "-", "-", "-", "https://x.test/next"}},
		{&downexv1.Download{Id: 5, Filename: "s.bin", Size: -1, Received: 3 << 10, State: stDownloading, SpeedBps: 100, Connections: 1},
			[]string{"5", "downloading", "3.1", "KB", "100", "B/s", "1", "--", "s.bin"}},
		{&downexv1.Download{Id: 6, Filename: "g.bin", Size: 10, Received: 4, State: stFailed, Error: "HTTP 404"},
			[]string{"6", "failed", "40.0%", "-", "-", "-", "g.bin:", "HTTP", "404"}},
		{&downexv1.Download{Id: 7, Filename: "e.bin", Size: 0, State: stCompleted},
			[]string{"7", "completed", "100.0%", "-", "-", "-", "e.bin"}},
		{&downexv1.Download{Id: 123456, Filename: "x", Size: 1, State: downexv1.DownloadState(42)},
			[]string{"123456", "state(42)", "0.0%", "-", "-", "-", "x"}},
	} {
		if got := strings.Fields(tableRow(6, tc.d)); !slices.Equal(got, tc.want) {
			t.Errorf("row fields %q, want %q", got, tc.want)
		}
	}
}

func TestTableViewLog(t *testing.T) {
	var buf bytes.Buffer
	v := newTableView(&buf, nil)
	v.apply([]*downexv1.Event{snapshot(active(1, 100, 10)), snapshot(active(2, 100, 50))})
	v.render()
	first := buf.String()
	if lines := strings.Split(strings.TrimSuffix(first, "\n"), "\n"); len(lines) != 3 ||
		!strings.HasPrefix(lines[0], "ID") || !strings.Contains(lines[1], "10.0%") || !strings.Contains(lines[2], "50.0%") {
		t.Fatalf("first render:\n%s", first)
	}

	buf.Reset()
	v.render()
	if buf.Len() != 0 {
		t.Errorf("render without changes printed %q", buf.String())
	}

	v.apply([]*downexv1.Event{updated(active(1, 100, 60)), removed(2), removed(99)})
	v.render()
	if got := buf.String(); strings.Count(got, "\n") != 2 || !strings.Contains(got, "2     removed\n") ||
		!strings.Contains(got, "60.0%") || strings.Contains(got, "ID") || strings.Contains(got, "\x1b") {
		t.Errorf("second render printed:\n%q", got)
	}
}

func TestTableViewTerminalRedraw(t *testing.T) {
	var buf bytes.Buffer
	width, height := 40, 6
	v := newTableView(&buf, func() (int, int) { return width, height })
	v.apply([]*downexv1.Event{snapshot(active(1, 100, 10)), snapshot(active(2, 100, 20)), snapshot(active(3, 100, 30))})
	v.render()
	frame := buf.String()
	if strings.Contains(frame, "\x1b[5A") || strings.Count(frame, "\x1b[2K") != 5 || !strings.HasSuffix(frame, "\x1b[J") {
		t.Fatalf("first frame: %q", frame)
	}
	for _, l := range strings.Split(strings.TrimSuffix(frame, "\x1b[J"), "\n") {
		if n := utf8.RuneCountInString(strings.TrimPrefix(l, "\x1b[2K")); n >= width {
			t.Errorf("line of %d columns on a %d-column terminal would wrap: %q", n, width, l)
		}
	}
	if !strings.Contains(frame, "3 downloads, 3 active, 0 queued, 3.1 M~\n") {
		t.Errorf("summary missing from %q", frame)
	}

	// The next frame overwrites this one in place.
	buf.Reset()
	v.apply([]*downexv1.Event{updated(active(2, 100, 90))})
	v.render()
	if frame = buf.String(); !strings.HasPrefix(frame, "\r\x1b[5A\x1b[2K") || !strings.Contains(frame, "90.0%") {
		t.Errorf("redraw: %q", frame)
	}

	// More downloads than fit: the frame keeps to the terminal's height.
	for id := int64(4); id <= 12; id++ {
		v.apply([]*downexv1.Event{snapshot(active(id, 100, 1))})
	}
	width, height = 200, 6
	buf.Reset()
	v.render()
	frame = buf.String()
	if n := strings.Count(frame, "\n"); n != height-1 || !strings.Contains(frame, "... and 10 more") {
		t.Errorf("%d lines for a %d-line terminal: %q", n, height, frame)
	}
}

func TestWatchCommandLog(t *testing.T) {
	f, h := newFakeDaemon(t)
	f.do(func() {
		f.watch = func(_ context.Context, _ *downexv1.WatchRequest, send func(...*downexv1.Event) error) error {
			_ = send(snapshot(active(1, 1000, 0)), snapshot(active(2, 1000, 500)))
			for i := int64(1); i <= 50; i++ {
				d := active(1, 1000, i*20)
				if i == 50 {
					d.State = stCompleted
				}
				_ = send(updated(d))
			}
			_ = send(removed(2))
			return nil // the daemon ends the stream
		}
	})
	r := h.run(context.Background(), "watch")
	if r.code != exitError || !strings.Contains(r.stderr, "downexd closed the event stream") {
		t.Fatalf("watch: %v", r)
	}
	lines := strings.Split(strings.TrimSuffix(r.stdout, "\n"), "\n")
	if strings.Count(r.stdout, "STATE") != 1 || !strings.HasPrefix(lines[0], "ID") {
		t.Errorf("header not printed exactly once:\n%s", r.stdout)
	}
	var rows1 []string
	for _, l := range lines {
		if strings.HasPrefix(l, "1 ") {
			rows1 = append(rows1, l)
		}
	}
	// 51 batches in a burst are coalesced: the first one is drawn at once, the rest at
	// most every 250 ms, and the final state when the stream ends.
	if len(rows1) == 0 || len(rows1) > 3 || !strings.Contains(rows1[len(rows1)-1], "completed") ||
		!strings.Contains(rows1[len(rows1)-1], "100.0%") {
		t.Errorf("rows of download 1: %q", rows1)
	}
	if !strings.Contains(r.stdout, "2     removed\n") {
		t.Errorf("removal not shown:\n%s", r.stdout)
	}
	if w := f.rec().watches; len(w) != 1 || len(w[0].GetIds()) != 0 {
		t.Errorf("watch requests %v, want one for all downloads", w)
	}
}

func TestWatchCommandRedrawsAtMost4Hz(t *testing.T) {
	f, h := newFakeDaemon(t)
	h.termSize = func() (int, int) { return 120, 40 }
	f.do(func() {
		f.watch = func(ctx context.Context, _ *downexv1.WatchRequest, send func(...*downexv1.Event) error) error {
			if err := send(snapshot(active(1, 1<<30, 0)), snapshot(active(2, 1<<30, 0))); err != nil {
				return nil
			}
			for i := int64(1); ; i++ {
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(5 * time.Millisecond):
				}
				if err := send(updated(active(1+i%2, 1<<30, i<<20))); err != nil {
					return nil
				}
			}
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run := h.start(ctx, "watch", "1", "2")
	waitFor(t, "the first frame", func() bool { return strings.Contains(run.stdout.String(), "f2.bin") })
	time.Sleep(1200 * time.Millisecond)
	cancel() // Ctrl+C
	if code := run.wait(t); code != exitInterrupted {
		t.Fatalf("watch after Ctrl+C: exit %d\n%s", code, run.stderr)
	}

	// Each frame is one write; consecutive frames are at least 250 ms apart (the
	// last one, drawn on Ctrl+C, may come sooner).
	writes := run.stdout.writeTimes()
	if len(writes) < 4 || len(writes) > 8 {
		t.Errorf("%d frames in about 1.2 s, want about 5", len(writes))
	}
	for i := 1; i < len(writes)-1; i++ {
		if gap := writes[i].Sub(writes[i-1]); gap < 240*time.Millisecond {
			t.Errorf("frames %d and %d only %v apart", i-1, i, gap)
		}
	}
	out := run.stdout.String()
	if strings.Count(out, "\r\x1b[") < len(writes)-1 || !strings.Contains(out, "2 downloads, 2 active") {
		t.Errorf("frames are not redrawn in place: %q", out[:min(len(out), 400)])
	}
	if w := f.rec().watches; len(w) != 1 || !slices.Equal(w[0].GetIds(), []int64{1, 2}) {
		t.Errorf("watch requests %v, want ids [1 2]", w)
	}
}

func TestWatchArguments(t *testing.T) {
	_, h := newFakeDaemon(t)
	for _, tc := range []struct {
		args []string
		out  string
	}{
		{[]string{"watch", "1", "x"}, `invalid download id "x"`},
		{[]string{"watch", "-3"}, "flag provided but not defined"},
		{[]string{"watch", "--", "-3"}, `invalid download id "-3"`},
	} {
		if r := h.run(context.Background(), tc.args...); r.code != exitError || !strings.Contains(r.stderr, tc.out) {
			t.Errorf("downex %q: %v\nwant %q", tc.args, r, tc.out)
		}
	}
	// A daemon without Watch support is reported, not mistaken for an empty table.
	if r := h.run(context.Background(), "watch"); r.code != exitError || !strings.Contains(r.stderr, "watch: no watch in this test") {
		t.Errorf("watch error: %v", r)
	}
}

// waitScript serves Watch with batches, then waits for the client to go away.
func waitScript(batches ...[]*downexv1.Event) func(context.Context, *downexv1.WatchRequest, func(...*downexv1.Event) error) error {
	return func(ctx context.Context, _ *downexv1.WatchRequest, send func(...*downexv1.Event) error) error {
		for _, b := range batches {
			if err := send(b...); err != nil {
				return nil
			}
		}
		<-ctx.Done() // the client stops by itself
		return nil
	}
}

func TestAddWaitFollowsUntilDone(t *testing.T) {
	f, h := newFakeDaemon(t)
	dir := t.TempDir()
	base := &downexv1.Download{Id: 7, Url: "https://example.test/f.bin", Dir: dir, Size: 1 << 20, Mode: downexv1.Mode_MODE_RANGED}
	queued := clone(base)
	queued.State, queued.QueuePosition = stQueued, 1
	running := clone(base)
	running.State, running.Filename, running.Received, running.SpeedBps, running.Connections = stDownloading, "f.bin", 512<<10, 1<<20, 4
	done := clone(running)
	done.State, done.Received = stCompleted, 1<<20
	f.do(func() {
		f.watch = waitScript([]*downexv1.Event{snapshot(queued)}, []*downexv1.Event{updated(running)},
			[]*downexv1.Event{snapshot(active(8, 1, 0)), updated(done)})
	})

	r := h.run(context.Background(), "add", "https://example.test/f.bin", "--wait")
	final := filepath.Join(dir, "f.bin")
	if r.code != exitOK || r.stdout != "7\n" || !strings.Contains(r.stderr, "Saving to "+final+"\n") ||
		!strings.Contains(r.stderr, "Saved "+final+" (1.0 MB)") {
		t.Fatalf("add --wait: %v", r)
	}
	if w := f.rec().watches; len(w) != 1 || !slices.Equal(w[0].GetIds(), []int64{7}) {
		t.Errorf("watch requests %v, want one for download 7", w)
	}
}

func TestAddWaitOutcomes(t *testing.T) {
	failed := active(7, 100, 40)
	failed.State, failed.Error, failed.ErrorCode, failed.SpeedBps = stFailed, "HTTP 503 after 8 retries", "http_status", 0
	changed := active(7, 100, 40)
	changed.State, changed.Error, changed.ErrorCode = stFailed, "the file on the server changed", "validator_changed"
	paused := active(7, 100, 40)
	paused.State = stPaused
	expired := active(7, 100, 40)
	expired.State, expired.ErrorCode, expired.SpeedBps = stFailed, "link_expired", 0
	expired.Error = "connection 1: bytes 40-99: HTTP 403 Forbidden: engine: link expired"
	expired.PageUrl = "https://pages.example/files"

	for _, tc := range []struct {
		name   string
		events [][]*downexv1.Event
		end    bool // the daemon ends the stream after the events
		want   []string
	}{
		{"failed", [][]*downexv1.Event{{snapshot(active(7, 100, 10))}, {updated(failed)}}, false,
			[]string{"downex: error: download 7 failed: HTTP 503 after 8 retries", "Its progress (40 B of 100 B (40.0%) saved) is kept.",
				"Try again with: downex resume 7"}},
		{"changed", [][]*downexv1.Event{{updated(changed)}}, false,
			[]string{"download 7 failed: the file on the server changed", "downex resume 7 --restart"}},
		{"paused", [][]*downexv1.Event{{snapshot(paused)}}, false, []string{"Download 7 was paused.", "Resume with: downex resume 7"}},
		{"link expired", [][]*downexv1.Event{{updated(expired)}}, false, []string{
			"downex: error: download 7 failed: connection 1: bytes 40-99: HTTP 403 Forbidden: engine: link expired\n" +
				"The link stopped working (it expired, was revoked or leads to a web page). Its progress (40 B of 100 B) is kept.\n" +
				"Give it a new link with:\n  downex renew 7 <new-url>",
			"The page it came from: https://pages.example/files\n"}},
		{"removed", [][]*downexv1.Event{{snapshot(active(7, 100, 1))}, {removed(7)}}, false, []string{"download 7 was removed"}},
		{"stream end", [][]*downexv1.Event{{snapshot(active(7, 100, 1))}}, true, []string{"downexd closed the event stream"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, h := newFakeDaemon(t)
			script := waitScript(tc.events...)
			if tc.end {
				script = func(_ context.Context, _ *downexv1.WatchRequest, send func(...*downexv1.Event) error) error {
					for _, b := range tc.events {
						_ = send(b...)
					}
					return nil
				}
			}
			f.do(func() { f.watch = script })
			r := h.run(context.Background(), "add", "https://example.test/f.bin", "--wait")
			if r.code != exitError || r.stdout != "7\n" {
				t.Fatalf("add --wait: %v", r)
			}
			for _, w := range tc.want {
				if !strings.Contains(r.stderr, w) {
					t.Errorf("stderr lacks %q:\n%s", w, r.stderr)
				}
			}
		})
	}
}

func TestAddWaitInterrupted(t *testing.T) {
	f, h := newFakeDaemon(t)
	sent := make(chan struct{})
	f.do(func() {
		f.watch = func(ctx context.Context, _ *downexv1.WatchRequest, send func(...*downexv1.Event) error) error {
			_ = send(snapshot(active(7, 100, 10)))
			close(sent)
			<-ctx.Done()
			return nil
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run := h.start(ctx, "add", "https://example.test/f.bin", "--wait", "--endpoint", "ep-9")
	<-sent
	cancel() // Ctrl+C stops following; the download goes on
	if code := run.wait(t); code != exitInterrupted {
		t.Fatalf("exit %d\n%s", code, run.stderr)
	}
	if err := run.stderr.String(); !strings.Contains(err, "download 7 continues in the background") ||
		!strings.Contains(err, "Watch it with: downex watch 7 --endpoint ep-9") {
		t.Errorf("stderr:\n%s", err)
	}
	if r := f.rec(); len(r.pauses) != 0 || len(r.removes) != 0 {
		t.Errorf("Ctrl+C changed the download: %+v", r)
	}
}

func TestResumeWait(t *testing.T) {
	f, h := newFakeDaemon(t)
	dir := t.TempDir()
	d := &downexv1.Download{Id: 3, Dir: dir, Filename: "r.bin", Size: 2048, Received: 1024, State: stPaused}
	done := clone(d)
	done.State, done.Received = stCompleted, 2048
	f.do(func() {
		f.downloads[3] = d
		f.watch = waitScript([]*downexv1.Event{snapshot(done)})
	})
	r := h.run(context.Background(), "resume", "3", "--wait")
	if r.code != exitOK || !strings.Contains(r.stdout, "Resumed download 3") || !strings.Contains(r.stderr, "Saved "+filepath.Join(dir, "r.bin")) {
		t.Errorf("resume --wait: %v", r)
	}

	// A completed download is not followed.
	f.do(func() { f.downloads[3] = clone(done) })
	if r = h.run(context.Background(), "resume", "3", "--wait"); r.code != exitOK || !strings.Contains(r.stdout, "already complete") {
		t.Errorf("resume of a completed download: %v", r)
	}
	if n := len(f.rec().watches); n != 1 {
		t.Errorf("%d watches, want 1", n)
	}
}

func TestFollowLine(t *testing.T) {
	for _, tc := range []struct {
		d    *downexv1.Download
		want string
	}{
		{&downexv1.Download{State: stQueued, QueuePosition: 2}, "Queued (position 2)"},
		{&downexv1.Download{State: stQueued}, "Queued"},
		{&downexv1.Download{State: stProbing}, "Connecting..."},
		{&downexv1.Download{State: stDownloading, Size: 100 << 20, Received: 25 << 20, SpeedBps: 5 << 20, Connections: 8},
			" 25.0%  26.2 MB / 104.9 MB  5.2 MB/s  8 conn  ETA 15s"},
		{&downexv1.Download{State: stPaused}, "paused"},
	} {
		if got := followLine(tc.d); got != tc.want {
			t.Errorf("followLine(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

// A stream's estimated size (size_approx) is shown as an estimate ("~2.2 GB", an ETA
// "~8m28s") and never measures its progress: its fragments do, as before downexd sent an
// estimate, in the watch table, the followed line and ls.
func TestApproxStreamSize(t *testing.T) {
	d := &downexv1.Download{Id: 7, Filename: "v.mp4", Size: 2_250_000_000, SizeApprox: true, Received: 120_000_000,
		State: stDownloading, SpeedBps: 4 << 20, Connections: 4, Kind: downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA,
		Media: &downexv1.MediaInfo{Format: "hls", FragmentsDone: 100, FragmentsTotal: 2176}}
	if got, want := strings.Fields(tableRow(4, d)), []string{"7", "downloading", "4.6%", "4.2", "MB/s", "4", "~8m28s", "v.mp4"}; !slices.Equal(got, want) {
		t.Errorf("row fields %q, want %q", got, want)
	}
	if got, want := followLine(d), "  4.6%  100/2176 fragments  120.0 MB of ~2.2 GB  4.2 MB/s  4 conn  ETA ~8m28s"; got != want {
		t.Errorf("followLine = %q, want %q", got, want)
	}
	// More received than estimated: no ETA, still the fragments' percentage.
	behind := clone(d)
	behind.Received = 2_300_000_000
	if got, want := strings.Fields(tableRow(4, behind)), []string{"7", "downloading", "4.6%", "4.2", "MB/s", "4", "--", "v.mp4"}; !slices.Equal(got, want) {
		t.Errorf("row fields %q, want %q", got, want)
	}
	if got, want := followLine(behind), "  4.6%  100/2176 fragments  2.3 GB of ~2.2 GB  4.2 MB/s  4 conn"; got != want {
		t.Errorf("followLine = %q, want %q", got, want)
	}
	paused := clone(d)
	paused.State, paused.SpeedBps, paused.Connections = stPaused, 0, 0
	if got := strings.Fields(tableRow(4, paused)); !slices.Equal(got, []string{"7", "paused", "4.6%", "-", "-", "-", "v.mp4"}) {
		t.Errorf("paused row fields %q", got)
	}
	var buf bytes.Buffer
	if err := writeList(&buf, []downloadView{rpcView(d)}); err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(buf.String()), "\n"); len(lines) != 2 ||
		!slices.Equal(strings.Fields(lines[1]), []string{"7", "downloading", "4.6%", "~2.2", "GB", "v.mp4"}) {
		t.Errorf("ls:\n%s", buf.String())
	}
	if v := rpcView(d); !v.SizeApprox || v.Size != d.Size {
		t.Errorf("view = %+v", v)
	}
	if got, want := describeSaved(rpcSize(paused), paused.GetReceived()), "120.0 MB saved"; got != want {
		t.Errorf("saved = %q, want %q", got, want)
	}
}
