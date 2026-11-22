package daemon

import (
	"errors"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
)

// outcomeNotice waits for the completed or failed notice of download id.
func outcomeNotice(t *testing.T, ch <-chan Notice, kind NoticeKind, id int64) Notice {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case n := <-ch:
			if n.Kind == kind && n.Download.GetId() == id {
				return n
			}
		case <-deadline:
			t.Fatalf("no notice %v for download %d", kind, id)
		}
	}
}

// confirmAnswer confirms with req and returns the whole answer.
func (h *harness) confirmAnswer(req *downexv1.ConfirmRequest) *downexv1.ConfirmResponse {
	h.t.Helper()
	res, err := h.dl.Confirm(ctxT(h.t), connect.NewRequest(req))
	if err != nil {
		h.t.Fatalf("confirm: %v", err)
	}
	return res.Msg
}

// TestConfirmedStartShowsItsOutcomeInItsWindow: a browser download confirmed with Start and
// progress_window has its progress window opened by downexd itself, above the browser and
// as it starts, and says so (progress_window_opening); the run is marked at once, so its
// completion or failure always shows in that window, brought above the browser, and the
// tray stays quiet. A confirmation without the flag (the CLI) keeps the tray notification
// and opens nothing.
func TestConfirmedStartShowsItsOutcomeInItsWindow(t *testing.T) {
	browser := downexv1.AddSource_ADD_SOURCE_BROWSER
	cases := []struct {
		name   string
		opts   testserver.Options
		window bool
		kind   NoticeKind
		state  downexv1.DownloadState
	}{
		{"completes", testserver.Options{Size: 4 << 10, Seed: 151}, true, NoticeCompleted, stCompleted},
		{"fails", testserver.Options{Size: 1, FailFirst: 1000, FailStatus: 404}, true, NoticeFailed, stFailed},
		{"cli confirm", testserver.Options{Size: 4 << 10, Seed: 152}, false, NoticeCompleted, stCompleted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ts := serve(t, c.opts)
			windows, confirms, raised := &launcher{}, &launcher{}, &addWindows{}
			h := newHarness(t, harnessOptions{openProgress: windows.open, openConfirmation: confirms.open, showBrowserAdd: raised.show})
			notices, stop := h.m.Subscribe()
			defer stop()
			d := h.add(&downexv1.AddRequest{Url: ts.URL + "/file.bin", Source: browser})
			if d.GetState() != stAwaiting {
				t.Fatalf("state = %v", d.GetState())
			}
			ans := h.confirmAnswer(&downexv1.ConfirmRequest{Id: d.GetId(), Start: true, ProgressWindow: c.window})
			if got := ans.GetDownload(); got.GetCompletionDialog() != c.window {
				t.Errorf("answer marked %v, want %v", got.GetCompletionDialog(), c.window)
			}
			if ans.GetProgressWindowOpening() != c.window {
				t.Errorf("progress_window_opening = %v, want %v", ans.GetProgressWindowOpening(), c.window)
			}
			h.waitState(d.GetId(), c.state)
			n := outcomeNotice(t, notices, c.kind, d.GetId())
			if n.InWindow != c.window {
				t.Errorf("notice in window = %v, want %v", n.InWindow, c.window)
			}
			if !c.window {
				time.Sleep(200 * time.Millisecond)
				if !slices.Equal(raised.list(), []shownAdd{{d.GetId(), AddConfirm}}) || len(windows.launched()) != 0 {
					t.Fatalf("windows opened for a CLI confirmation: raised %v, plain %v", raised.list(), windows.launched())
				}
				return
			}
			waitUntil(t, "the outcome window", func() bool { return len(raised.list()) > 2 })
			time.Sleep(150 * time.Millisecond) // a duplicate would be on its way by now
			want := []shownAdd{{d.GetId(), AddConfirm}, {d.GetId(), AddProgressStarted}, {d.GetId(), AddProgress}}
			if got := raised.list(); !slices.Equal(got, want) {
				t.Fatalf("raised %v, want %v: the progress window as it starts, then the outcome in it", got, want)
			}
			if got := windows.launched(); len(got) != 0 {
				t.Fatalf("plain progress windows %v", got)
			}
		})
	}
}

// TestConfirmedStartWhoseWindowCannotOpen: when downexd cannot open the progress window
// of a confirmed Start, the run's mark is taken back: its outcome is a tray notification
// (not in a window), no lone download complete dialog opens, and the mark is not stored.
func TestConfirmedStartWhoseWindowCannotOpen(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 256 << 10, Seed: 153})
	windows := &launcher{}
	raised := &addWindows{fail: map[AddWindow]error{AddProgressStarted: errors.New("no window")}}
	h := newHarness(t, harnessOptions{openProgress: windows.open, openConfirmation: (&launcher{}).open, showBrowserAdd: raised.show})
	notices, stop := h.m.Subscribe()
	defer stop()
	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/file.bin", Source: downexv1.AddSource_ADD_SOURCE_BROWSER})
	// Slow, so that the launch fails while it runs.
	ans := h.confirmAnswer(&downexv1.ConfirmRequest{Id: d.GetId(), Start: true, ProgressWindow: true, SpeedLimit: ptr(int64(32 << 10))})
	if !ans.GetProgressWindowOpening() || !ans.GetDownload().GetCompletionDialog() {
		t.Fatalf("answer = %v", ans)
	}
	h.waitFor(d.GetId(), "the mark taken back", func(x *downexv1.Download) bool { return !x.GetCompletionDialog() })
	if _, err := h.setOptions(&downexv1.SetDownloadOptionsRequest{Id: d.GetId(), SpeedLimit: ptr(int64(0))}); err != nil {
		t.Fatal(err)
	}
	h.waitCompleted(d.GetId())
	if n := outcomeNotice(t, notices, NoticeCompleted, d.GetId()); n.InWindow {
		t.Error("the outcome was left to a window that did not open")
	}
	time.Sleep(200 * time.Millisecond)
	progress := slices.DeleteFunc(raised.list(), func(a shownAdd) bool { return a.w == AddConfirm })
	if !slices.Equal(progress, []shownAdd{{d.GetId(), AddProgressStarted}}) || len(windows.launched()) != 0 {
		t.Fatalf("windows after the failed launch: raised %v, plain %v", raised.list(), windows.launched())
	}
	if storedState(t, h.st, d.GetId()).CompletionDialog {
		t.Fatal("the mark of a window that never opened was stored")
	}
}

// TestConfirmedStartThatWaitsInItsQueue: a confirmed Start that waits for a free slot has its
// progress window opened at once (it shows the download queued); its run is windowed as it
// starts, so its completion shows in that window and not in the tray (docs/DESKTOP-SPEC.md
// §7).
func TestConfirmedStartThatWaitsInItsQueue(t *testing.T) {
	busy := serve(t, testserver.Options{Size: 8 << 20, Seed: 154, Throttle: 64 << 10})
	small := serve(t, testserver.Options{Size: 4 << 10, Seed: 155})
	raised := &addWindows{}
	h := newHarness(t, harnessOptions{openProgress: (&launcher{}).open, openConfirmation: (&launcher{}).open,
		showBrowserAdd: raised.show, settings: func(s *downexv1.Settings) { s.MaxConcurrentDownloads = 1 }})
	notices, stop := h.m.Subscribe()
	defer stop()
	blocker := h.add(&downexv1.AddRequest{Url: busy.URL + "/big.bin", Source: downexv1.AddSource_ADD_SOURCE_UI})
	h.waitState(blocker.GetId(), stDownloading)
	d := h.add(&downexv1.AddRequest{Url: small.URL + "/file.bin", Source: downexv1.AddSource_ADD_SOURCE_BROWSER})
	ans := h.confirmAnswer(&downexv1.ConfirmRequest{Id: d.GetId(), Start: true, ProgressWindow: true})
	if !ans.GetProgressWindowOpening() || ans.GetDownload().GetState() != stQueued {
		t.Fatalf("answer = %v", ans)
	}
	waitUntil(t, "the progress window", func() bool {
		return slices.Contains(raised.list(), shownAdd{d.GetId(), AddProgressStarted})
	})
	if got := h.get(d.GetId()); got.GetState() != stQueued {
		t.Fatalf("state with its window open = %v", got.GetState())
	}
	h.pause(blocker.GetId())
	h.waitCompleted(d.GetId())
	if n := outcomeNotice(t, notices, NoticeCompleted, d.GetId()); !n.InWindow {
		t.Error("the queued Start's outcome went to the tray")
	}
	waitUntil(t, "the outcome window", func() bool { return slices.Contains(raised.list(), shownAdd{d.GetId(), AddProgress}) })
}

// TestCompletionDialogOnlyForWindowedRuns: the download complete dialog follows the run, not
// the stored mark (docs/DESKTOP-SPEC.md §8): a download whose earlier run had a window gets
// the tray notification only when it runs again without one (the queue, the scheduler, the
// CLI), and a window open on it before it starts makes that run windowed.
func TestCompletionDialogOnlyForWindowedRuns(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 4 << 10, Seed: 156})
	windows := &launcher{}
	h := newHarness(t, harnessOptions{openProgress: windows.open})
	notices, stop := h.m.Subscribe()
	defer stop()
	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/a.bin", Source: downexv1.AddSource_ADD_SOURCE_UI, StartPaused: true})
	// Its window opens while it is paused: the next run is windowed, whoever starts it.
	if _, err := h.setOptions(&downexv1.SetDownloadOptionsRequest{Id: d.GetId(), CompletionDialog: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	h.resume(&downexv1.ResumeRequest{Id: d.GetId()})
	h.waitCompleted(d.GetId())
	if n := outcomeNotice(t, notices, NoticeCompleted, d.GetId()); !n.InWindow {
		t.Error("the windowed run's outcome went to the tray")
	}
	waitLaunches(t, windows, d.GetId())

	// Downloaded again without a window: still marked, but the tray tells the outcome.
	h.resume(&downexv1.ResumeRequest{Id: d.GetId(), Restart: true, ExistingFile: downexv1.ExistingFile_EXISTING_FILE_REPLACE})
	if n := outcomeNotice(t, notices, NoticeCompleted, d.GetId()); n.InWindow {
		t.Error("a run without a window left its outcome to a window")
	}
	if !h.get(d.GetId()).GetCompletionDialog() {
		t.Error("the stored mark was lost")
	}
	waitLaunches(t, windows, d.GetId())

	// The caller shows the window of a restart (progress_window): that run is windowed.
	h.resume(&downexv1.ResumeRequest{Id: d.GetId(), Restart: true, ExistingFile: downexv1.ExistingFile_EXISTING_FILE_REPLACE,
		ProgressWindow: true})
	if n := outcomeNotice(t, notices, NoticeCompleted, d.GetId()); !n.InWindow {
		t.Error("the outcome of a run whose window the caller shows went to the tray")
	}
	waitLaunches(t, windows, d.GetId(), d.GetId())
}
