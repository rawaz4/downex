package e2e

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
)

// The download complete dialog (docs/DESKTOP-SPEC.md §8) with real executables: downexd
// starts "downex-ui --progress <id>" (the stand-in window next to it) once when a download
// whose progress window was opened completes or fails, never for other downloads or with
// show_complete_dialog off; it reports the time the download was active, without its
// pauses, and keeps the mark and the times across a restart.

// markCompletionDialog marks download id as a progress window does when it opens:
// SetDownloadOptions{completion_dialog: true}.
func (h *harness) markCompletionDialog(id int64) *downexv1.Download {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), rpcCallTimeout)
	defer cancel()
	res, err := h.dl.SetDownloadOptions(ctx, connect.NewRequest(&downexv1.SetDownloadOptionsRequest{
		Id: id, CompletionDialog: proto.Bool(true)}))
	if err != nil {
		h.t.Fatalf("mark download %d for the complete dialog: %v", id, err)
	}
	if !res.Msg.GetDownload().GetCompletionDialog() {
		h.t.Fatalf("SetDownloadOptions answered an unmarked download: %v", res.Msg.GetDownload())
	}
	return res.Msg.GetDownload()
}

// waitUICalls waits until the stand-in window has been started at least n times and
// returns every recorded start.
func (d desktopInstall) waitUICalls(t *testing.T, n int) []uiCall {
	t.Helper()
	deadline := time.Now().Add(readyTimeout)
	for {
		calls := d.uiCalls(t)
		if len(calls) >= n {
			return calls
		}
		if time.Now().After(deadline) {
			t.Fatalf("the window was started %d times within %v, want %d: %+v", len(calls), readyTimeout, n, calls)
		}
		time.Sleep(pollInterval)
	}
}

// progressCalls is the argument lists of "downex-ui --progress <id>" for ids.
func progressCalls(ids ...int64) [][]string {
	var want [][]string
	for _, id := range ids {
		want = append(want, []string{"--progress", idArg(id)})
	}
	return want
}

func callArgs(calls []uiCall) [][]string {
	var got [][]string
	for _, c := range calls {
		got = append(got, c.Args)
	}
	return got
}

func equalCalls(a, b [][]string) bool {
	return slices.EqualFunc(a, b, func(x, y []string) bool { return slices.Equal(x, y) })
}

// slack is the resolution allowed between the test's clock and the daemon's when they
// are compared: Windows clocks may tick every 15.6 ms, and up to four readings on each
// side go into one comparison.
const slack = 100 * time.Millisecond

func TestDownloadCompleteDialog(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	const size, seed, throttle = 1 << 20, 601, 256 << 10
	srv := newServer(t, testserver.Options{Size: size, Seed: seed, ETag: `"cd1"`, Throttle: throttle})
	inst := h.newDesktopInstall()
	d := h.startDaemonFrom(inst.downexd, inst.env)
	h.waitReady(d)
	if s := h.getSettings(); !s.GetShowCompleteDialog() {
		t.Fatal("show_complete_dialog is off by default")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*rpcCallTimeout)
	defer cancel()

	// A download whose progress window was opened while it ran, paused for a while and
	// resumed: one window on its completion, and the time taken without the pause.
	t0 := time.Now()
	id := h.add(srv.URL+"/finished.bin", "-o", h.out, "-c", "1")
	first := h.waitReceived(id, 32<<10)
	seen := time.Now()
	if first.GetCompletionDialog() {
		t.Fatalf("a CLI add is marked for the complete dialog: %v", first)
	}
	if got := h.markCompletionDialog(id); !running(got.GetState()) {
		t.Fatalf("the download stopped before it was marked: %v", got)
	}
	if !h.get(id).GetCompletionDialog() {
		t.Fatal("the running download does not show its mark")
	}
	if _, err := h.dl.Pause(ctx, connect.NewRequest(&downexv1.PauseRequest{Id: id})); err != nil {
		t.Fatalf("pause: %v", err)
	}
	paused := time.Now()
	stored := h.get(id)
	if stored.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_PAUSED || !stored.GetCompletionDialog() ||
		stored.GetActiveMs() <= 0 || stored.GetStartedAt() == nil {
		t.Fatalf("after the pause: %v", stored)
	}
	if v := h.completionInfo(id); !v.CompletionDialog || v.ActiveMS != stored.GetActiveMs() || v.StartedAt == nil {
		t.Fatalf("stored after the pause (info --json): %+v; the daemon reports %v", v, stored)
	}
	time.Sleep(2 * time.Second)
	if again := h.get(id); again.GetActiveMs() != stored.GetActiveMs() {
		t.Fatalf("the active time grew while paused: %d -> %d ms", stored.GetActiveMs(), again.GetActiveMs())
	}
	// Resumed by its window (progress_window, as the window's own Resume sends): the dialog
	// is the run's, and a run without a window would get the tray notification only.
	resumed := time.Now()
	if _, err := h.dl.Resume(ctx, connect.NewRequest(&downexv1.ResumeRequest{Id: id, ProgressWindow: true})); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if calls := inst.uiCalls(t); len(calls) != 0 {
		t.Fatalf("a window was started before the download ended: %+v", calls)
	}
	done := h.waitCompleted(id)
	t1 := time.Now()
	checkFile(t, filepath.Join(h.out, "finished.bin"), seed, size)
	calls := inst.waitUICalls(t, 1)
	if !equalCalls(callArgs(calls), progressCalls(id)) || calls[0].Endpoint != h.endpoint {
		t.Fatalf("window starts %+v, want [--progress %d] on %q", calls, id, h.endpoint)
	}

	// Active time: the two runs lie within [t0, paused] and [resumed, t1], so the 2 s
	// pause is outside it; the throttled transfer of the whole file is inside it.
	active := time.Duration(done.GetActiveMs()) * time.Millisecond
	upper := paused.Sub(t0) + t1.Sub(resumed) + slack
	lower := time.Duration(float64(size) / throttle * 0.8 * float64(time.Second))
	t.Logf("active %v; wall %v, of which paused %v; bounds [%v, %v]", active, t1.Sub(t0), resumed.Sub(paused), lower, upper)
	if active < lower || active > upper {
		t.Errorf("active time %v, want within [%v, %v] (the transfer, without the pause)", active, lower, upper)
	}
	started := done.GetStartedAt().AsTime()
	if done.GetStartedAt() == nil || started.Before(t0.Add(-slack)) || started.After(seen.Add(slack)) ||
		!started.Equal(stored.GetStartedAt().AsTime()) {
		t.Errorf("started_at %v, want the first start in [%v, %v] (after the pause: %v)", started, t0, seen,
			stored.GetStartedAt().AsTime())
	}
	if !done.GetCompletionDialog() {
		t.Errorf("the completed download lost its mark: %v", done)
	}

	// An unmarked download completes without a window.
	plain := h.add(srv.URL+"/plain.bin", "-o", h.out)
	srv.Update(func(o *testserver.Options) { o.Throttle = 0 })
	if dl := h.waitCompleted(plain); dl.GetCompletionDialog() {
		t.Fatalf("unmarked download = %v", dl)
	}

	// With show_complete_dialog off a marked download completes without a window.
	h.cli("settings", "--complete-dialog", "off").ok(t)
	quiet := h.add(srv.URL+"/quiet.bin", "-o", h.out, "--paused")
	h.markCompletionDialog(quiet)
	if v := h.completionInfo(quiet); !v.CompletionDialog || v.State != "paused" {
		t.Fatalf("the paused download's mark is not stored at once: %+v", v)
	}
	h.cli("resume", idArg(quiet)).ok(t)
	h.waitCompleted(quiet)

	// A marked download that fails gets the window too (with the error, Retry and
	// Close); it is also the positive control for the two downloads above.
	h.cli("settings", "--complete-dialog", "on").ok(t)
	bad := newServer(t, testserver.Options{Size: size, Seed: seed + 1, FailFirst: 1 << 20, FailStatus: 404})
	failed := h.add(bad.URL+"/missing.bin", "-o", h.out, "--paused")
	h.markCompletionDialog(failed)
	h.cli("resume", idArg(failed)).ok(t)
	if dl := h.waitState(failed, downexv1.DownloadState_DOWNLOAD_STATE_FAILED); !dl.GetCompletionDialog() {
		t.Fatalf("failed download = %v", dl)
	}
	inst.waitUICalls(t, 2)
	time.Sleep(time.Second) // a late extra start would show up now
	if got, want := callArgs(inst.uiCalls(t)), progressCalls(id, failed); !equalCalls(got, want) {
		t.Fatalf("window starts %q, want %q (unmarked %d, setting off %d)", got, want, plain, quiet)
	}

	// The mark and the times survive a restart, which opens no window.
	h.stop(d)
	d = h.startDaemonFrom(inst.downexd, inst.env)
	h.waitReady(d)
	again := h.get(id)
	if !again.GetCompletionDialog() || again.GetActiveMs() != done.GetActiveMs() ||
		!again.GetStartedAt().AsTime().Equal(started) || again.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED {
		t.Fatalf("after a restart: %v, want the mark, %d ms and started_at %v", again, done.GetActiveMs(), started)
	}
	if !h.get(failed).GetCompletionDialog() {
		t.Errorf("the failed download lost its mark across the restart")
	}
	time.Sleep(time.Second)
	if calls := inst.uiCalls(t); len(calls) != 2 {
		t.Errorf("window starts after the restart: %+v", calls)
	}
	h.stop(d)

	// "downex info --json" reads the same from the database.
	v := h.completionInfo(id)
	if !v.CompletionDialog || v.ActiveMS != done.GetActiveMs() || v.StartedAt == nil || !v.StartedAt.Equal(started) {
		t.Errorf("info --json without the daemon: %+v, want the mark, %d ms and started_at %v", v, done.GetActiveMs(), started)
	}
}

// completionJSON is the part of "downex info --json" about the complete dialog.
type completionJSON struct {
	State            string     `json:"state"`
	StartedAt        *time.Time `json:"startedAt"`
	ActiveMS         int64      `json:"activeMs"`
	CompletionDialog bool       `json:"completionDialog"`
}

func (h *harness) completionInfo(id int64) completionJSON {
	h.t.Helper()
	r := h.cli("info", idArg(id), "--json").ok(h.t)
	var v completionJSON
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil {
		h.t.Fatalf("info --json: %v: %v", err, r)
	}
	return v
}
