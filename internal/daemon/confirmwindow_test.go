package daemon

import (
	"slices"
	"testing"
	"time"

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

// TestConfirmedStartShowsItsOutcomeInItsWindow: a browser download confirmed with Start by a
// window that opens its progress window (progress_window) is marked at once, so its completion
// or failure always shows in that window, brought above the browser, and the tray stays quiet;
// a confirmation without the flag (the CLI) keeps the tray notification and opens nothing.
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
			got, err := h.confirmReq(&downexv1.ConfirmRequest{Id: d.GetId(), Start: true, ProgressWindow: c.window})
			if err != nil {
				t.Fatal(err)
			}
			if got.GetCompletionDialog() != c.window {
				t.Errorf("answer marked %v, want %v", got.GetCompletionDialog(), c.window)
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
			waitUntil(t, "the outcome window", func() bool { return len(raised.list()) > 1 })
			time.Sleep(150 * time.Millisecond) // a duplicate would be on its way by now
			if got := raised.list(); !slices.Equal(got, []shownAdd{{d.GetId(), AddConfirm}, {d.GetId(), AddProgress}}) {
				t.Fatalf("raised %v, want the progress window of %d above the browser", got, d.GetId())
			}
			if got := windows.launched(); len(got) != 0 {
				t.Fatalf("plain progress windows %v", got)
			}
		})
	}
}
