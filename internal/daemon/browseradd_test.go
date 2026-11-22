package daemon

import (
	"slices"
	"sync"
	"testing"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
)

// addWindows records Config.ShowBrowserAdd's calls; a window kind in fail is refused with
// its error (a launch that fails).
type addWindows struct {
	mu    sync.Mutex
	shown []shownAdd
	fail  map[AddWindow]error
}

type shownAdd struct {
	id int64
	w  AddWindow
}

func (a *addWindows) show(id int64, w AddWindow) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.shown = append(a.shown, shownAdd{id, w})
	return a.fail[w]
}

func (a *addWindows) list() []shownAdd {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.shown)
}

// TestBrowserAddShowsItsWindowAboveTheBrowser: a download the user starts in the browser
// shows its window through ShowBrowserAdd (downexd adds "--raise"): its progress window as
// it starts ("--started"), or its standalone Confirm download window; when neither opens,
// nothing shows (never the main window). A clipboard add keeps the plain hooks and never
// comes above the app in use. The §8 complete dialog of a run whose window was open on the
// stopped download comes above the browser too: the outcome shows in that window.
func TestBrowserAddShowsItsWindowAboveTheBrowser(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 4 << 20, Seed: 141})
	slow := int64(32 << 10)
	browser, clip := downexv1.AddSource_ADD_SOURCE_BROWSER, downexv1.AddSource_ADD_SOURCE_CLIPBOARD

	t.Run("progress", func(t *testing.T) {
		windows, raised := &launcher{}, &addWindows{}
		h := newHarness(t, harnessOptions{openProgress: windows.open, showBrowserAdd: raised.show,
			settings: func(s *downexv1.Settings) { s.ConfirmBrowserDownloads = false; s.MaxConcurrentDownloads = 4 }})
		d := h.add(&downexv1.AddRequest{Url: ts.URL + "/b.bin", Source: browser, SpeedLimit: slow})
		c := h.add(&downexv1.AddRequest{Url: ts.URL + "/c.bin", Source: clip, SpeedLimit: slow})
		waitUntil(t, "both windows", func() bool { return len(raised.list()) > 0 && len(windows.launched()) > 0 })
		if got := raised.list(); !slices.Equal(got, []shownAdd{{d.GetId(), AddProgressStarted}}) {
			t.Fatalf("raised %v, want the browser add's progress window as it starts", got)
		}
		if got := windows.launched(); !slices.Equal(got, []int64{c.GetId()}) {
			t.Fatalf("plain progress windows %v, want the clipboard add's", got)
		}
	})

	t.Run("nothing when no progress window", func(t *testing.T) {
		windows, raised := &launcher{}, &addWindows{}
		h := newHarness(t, harnessOptions{openProgress: windows.open, showBrowserAdd: raised.show,
			settings: func(s *downexv1.Settings) {
				s.ConfirmBrowserDownloads, s.ShowProgressWindow, s.MaxConcurrentDownloads = false, false, 4
			}})
		h.add(&downexv1.AddRequest{Url: ts.URL + "/clip.bin", Source: clip, SpeedLimit: slow})
		d := h.add(&downexv1.AddRequest{Url: ts.URL + "/one.bin", Source: browser, SpeedLimit: slow})
		h.add(&downexv1.AddRequest{Url: ts.URL + "/two.bin", Source: browser, SpeedLimit: slow}) // same burst
		waitUntil(t, "the first one running", func() bool { return h.get(d.GetId()).GetState() == downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING })
		time.Sleep(300 * time.Millisecond)
		if got := raised.list(); len(got) != 0 {
			t.Fatalf("raised %v, want no window (never the main window)", got)
		}
		if got := windows.launched(); len(got) != 0 {
			t.Fatalf("progress windows %v with the setting off", got)
		}
	})

	t.Run("confirmation", func(t *testing.T) {
		confirms, raised := &launcher{}, &addWindows{}
		h := newHarness(t, harnessOptions{openConfirmation: confirms.open, showBrowserAdd: raised.show})
		d := h.add(&downexv1.AddRequest{Url: ts.URL + "/ask.bin", Source: browser})
		waitUntil(t, "the confirmation", func() bool { return len(raised.list()) > 0 })
		if got := raised.list(); !slices.Equal(got, []shownAdd{{d.GetId(), AddConfirm}}) {
			t.Fatalf("raised %v, want the confirmation of %d", got, d.GetId())
		}
		if got := confirms.launched(); len(got) != 0 {
			t.Fatalf("plain confirmations %v", got)
		}
	})

	t.Run("complete dialog", func(t *testing.T) {
		small := serve(t, testserver.Options{Size: 4 << 10, Seed: 142})
		windows, raised := &launcher{}, &addWindows{}
		h := newHarness(t, harnessOptions{openProgress: windows.open, showBrowserAdd: raised.show,
			settings: func(s *downexv1.Settings) { s.ConfirmBrowserDownloads = false }})
		d := h.add(&downexv1.AddRequest{Url: small.URL + "/a.bin", Source: browser, StartPaused: true})
		if _, err := h.setOptions(&downexv1.SetDownloadOptionsRequest{Id: d.GetId(), CompletionDialog: ptr(true)}); err != nil {
			t.Fatal(err)
		}
		h.resume(&downexv1.ResumeRequest{Id: d.GetId()})
		h.waitCompleted(d.GetId())
		waitUntil(t, "the download complete dialog", func() bool { return len(raised.list()) > 0 })
		time.Sleep(150 * time.Millisecond) // a duplicate would be on its way by now
		if got := raised.list(); !slices.Equal(got, []shownAdd{{d.GetId(), AddProgress}}) {
			t.Fatalf("raised %v, want the outcome in the progress window of %d", got, d.GetId())
		}
		if got := windows.launched(); len(got) != 0 {
			t.Fatalf("plain windows %v for a browser download's outcome", got)
		}
	})

	t.Run("clipboard complete dialog", func(t *testing.T) {
		small := serve(t, testserver.Options{Size: 4 << 10, Seed: 143})
		windows, raised := &launcher{}, &addWindows{}
		h := newHarness(t, harnessOptions{openProgress: windows.open, showBrowserAdd: raised.show,
			settings: func(s *downexv1.Settings) { s.ConfirmBrowserDownloads = false }})
		d := h.add(&downexv1.AddRequest{Url: small.URL + "/c.bin", Source: clip, StartPaused: true})
		if _, err := h.setOptions(&downexv1.SetDownloadOptionsRequest{Id: d.GetId(), CompletionDialog: ptr(true)}); err != nil {
			t.Fatal(err)
		}
		h.resume(&downexv1.ResumeRequest{Id: d.GetId()})
		h.waitCompleted(d.GetId())
		waitLaunches(t, windows, d.GetId())
		if got := raised.list(); len(got) != 0 {
			t.Fatalf("the complete dialog came above the app in use: %v", got)
		}
	})
}
