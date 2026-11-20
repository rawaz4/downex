package progress

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format/formattest"
	"downex/ui/core/progress"
	"downex/ui/kit"
	"downex/ui/state"
	"downex/ui/state/statetest"
)

// fittedComplete is the height the complete view asks for: its content, the 39 px frame and
// the slack (jsdom has no layout and asks for PROGRESS_WINDOW.completeHeight instead).
func (r *rig) fittedComplete() kit.DIP {
	return kit.DIP(progress.FitWindowHeight(progress.ViewComplete, r.s().neededHeight(progress.ViewComplete), 39))
}

// fittedCollapsed is the height the collapsed view asks for (never below 300).
func (r *rig) fittedCollapsed() kit.DIP {
	return kit.DIP(progress.FitWindowHeight(progress.ViewCollapsed, r.s().neededHeight(progress.ViewCollapsed), 39))
}

func completed(edit func(d *downexv1.Download)) *downexv1.Download {
	return running(func(d *downexv1.Download) {
		d.State, d.Received, d.ConnectionInfo = state.StateCompleted, 100*MiB, nil
		if edit != nil {
			edit(d)
		}
	})
}

func showSettings(t *testing.T, d *downexv1.Download, edit func(s *downexv1.Settings)) *rig {
	s := settings()
	if edit != nil {
		edit(s)
	}
	return newRig(t, d, s, rigOptions{})
}

func offSettings(s *downexv1.Settings) { s.ShowCompleteDialog = false }

func (r *rig) completeShown() bool { return r.s().complete && visible(&r.s().done.Element) }

func TestProgressWindowCompletionAndFailure(t *testing.T) {
	t.Run("turns into the completion view; \"Don’t show this dialog again\" writes every setting back", func(t *testing.T) {
		r := showSettings(t, running(nil), func(s *downexv1.Settings) { s.NotifyOnComplete, s.LaunchAtLogin = false, true })
		want(t, "state", r.stateLine(), "Receiving data…")
		r.update(7, func(d *downexv1.Download) { d.State, d.Received, d.ConnectionInfo = state.StateCompleted, 100*MiB, nil })
		if !r.completeShown() {
			t.Fatal("no complete view")
		}
		region := textOf(&r.s().done.Element)
		for _, w := range []string{"distro-installer.iso", "/home/you/Downloads/Archives", "105 MB"} {
			if !strings.Contains(region, w) {
				t.Fatal(w, region)
			}
		}
		want(t, "title", r.api.title, "100% distro-installer.iso")
		if r.api.closed != 0 {
			t.Fatal("closed")
		}
		saved := settings()
		saved.NotifyOnComplete, saved.LaunchAtLogin = false, true
		r.click(r.s().done.dontShow.Box)
		wantSaved := proto.CloneOf(saved)
		wantSaved.ShowCompleteDialog = false
		if a := r.api.lastArgs("updateSettings"); len(a) != 1 || !proto.Equal(a[0].(*downexv1.Settings), wantSaved) {
			t.Fatal(a)
		}
		// Read first, then written back whole.
		if c := r.api.Calls(); !slices.Equal(c[len(c)-2:], []string{"getSettings", "updateSettings"}) {
			t.Fatal(c)
		}
		// The view stays although the setting is now off.
		if !r.completeShown() {
			t.Fatal("no complete view")
		}
		r.click(r.s().done.dontShow.Box)
		wantSaved.ShowCompleteDialog = true
		if a := r.api.lastArgs("updateSettings"); len(a) != 1 || !proto.Equal(a[0].(*downexv1.Settings), wantSaved) {
			t.Fatal(a)
		}
		r.click(r.s().done.open)
		if r.api.closed != 1 || !slices.Contains(r.api.Calls(), "openFile") {
			t.Fatal(r.api.closed, r.api.Calls())
		}
	})

	t.Run("closes by itself on completion when the summary is off", func(t *testing.T) {
		r := showSettings(t, running(nil), offSettings)
		r.update(7, func(d *downexv1.Download) { d.State, d.Received = state.StateCompleted, 100*MiB })
		if r.api.closed != 1 {
			t.Fatal(r.api.closed)
		}
	})

	t.Run("reads the settings again when its download completes", func(t *testing.T) {
		// Turned off after the window opened (in Settings, or in another progress window).
		r := show(t, running(nil))
		s := settings()
		s.ShowCompleteDialog = false
		r.api.setSettings(s)
		r.update(7, func(d *downexv1.Download) { d.State, d.Received, d.ConnectionInfo = state.StateCompleted, 100*MiB, nil })
		if r.api.closed != 1 || r.s().complete {
			t.Fatal(r.api.closed, r.s().complete)
		}
	})

	t.Run("shows the summary that was turned on after the window opened", func(t *testing.T) {
		r := showSettings(t, running(nil), offSettings)
		r.api.setSettings(settings())
		r.update(7, func(d *downexv1.Download) { d.State, d.Received, d.ConnectionInfo = state.StateCompleted, 100*MiB, nil })
		if !r.completeShown() || r.api.closed != 0 {
			t.Fatal(r.completeShown(), r.api.closed)
		}
	})

	t.Run("decides with the settings it has when they cannot be read at completion", func(t *testing.T) {
		r := showSettings(t, running(nil), offSettings)
		r.api.getSettings = func() (*downexv1.Settings, error) { return nil, errors.New(refused) }
		r.update(7, func(d *downexv1.Download) { d.State, d.Received, d.ConnectionInfo = state.StateCompleted, 100*MiB, nil })
		if r.api.closed != 1 {
			t.Fatal(r.api.closed)
		}
	})

	t.Run("closes on completion when the settings could only be read late", func(t *testing.T) {
		// downexd was still starting when the window opened: the first read failed.
		failures := 1
		r := newRig(t, running(nil), func() *downexv1.Settings { s := settings(); offSettings(s); return s }(), rigOptions{setup: func(a *fakeAPI) {
			a.getSettings = func() (*downexv1.Settings, error) {
				if failures > 0 {
					failures--
					return nil, errors.New(refused)
				}
				a.mu.Lock()
				defer a.mu.Unlock()
				return proto.CloneOf(a.settings), nil
			}
		}})
		want(t, "state", r.stateLine(), "Receiving data…")
		r.update(7, func(d *downexv1.Download) { d.State, d.Received, d.ConnectionInfo = state.StateCompleted, 100*MiB, nil })
		if r.api.closed != 1 || r.s().complete {
			t.Fatal(r.api.closed)
		}
	})

	t.Run("shows the summary of a download that was complete when it opened", func(t *testing.T) {
		// "downex-ui --progress <id>" from downexd when a marked download completed (§8).
		r := show(t, completed(nil))
		if !r.completeShown() {
			t.Fatal("no complete view")
		}
		if visible(&r.s().tabs.Element) {
			t.Fatal("tabs")
		}
		r.click(r.s().done.folder)
		if r.api.closed != 1 || !slices.Contains(r.api.Calls(), "openFolder") {
			t.Fatal(r.api.closed, r.api.Calls())
		}
	})

	t.Run("closes when its download was complete before it opened and the summary is off", func(t *testing.T) {
		// Opened as the download started (a browser hand-off, Start download), and a small
		// file was done before this window's first read: nothing pops up (§7, §8).
		r := showSettings(t, completed(nil), offSettings)
		if r.api.closed != 1 || r.s().complete {
			t.Fatal(r.api.closed)
		}
	})

	t.Run("reads the settings again when a download complete at its opening is restarted and completes", func(t *testing.T) {
		r := show(t, completed(nil))
		if !r.completeShown() {
			t.Fatal("no complete view")
		}
		r.update(7, func(d *downexv1.Download) { d.State, d.Received = state.StateDownloading, 0 })
		want(t, "state", r.stateLine(), "Receiving data…")
		s := settings()
		s.ShowCompleteDialog = false
		r.api.setSettings(s)
		r.update(7, func(d *downexv1.Download) { d.State, d.Received, d.ConnectionInfo = state.StateCompleted, 100*MiB, nil })
		if r.api.closed != 1 || r.s().complete {
			t.Fatal(r.api.closed)
		}
	})

	t.Run("shows the summary of a download complete when it opened if the settings cannot be read", func(t *testing.T) {
		r := newRig(t, completed(nil), nil, rigOptions{setup: func(a *fakeAPI) {
			a.getSettings = func() (*downexv1.Settings, error) { return nil, errors.New(refused) }
		}})
		if !r.completeShown() || r.api.closed != 0 {
			t.Fatal(r.completeShown(), r.api.closed)
		}
	})

	t.Run("asks before it opens a program", func(t *testing.T) {
		r := show(t, completed(func(d *downexv1.Download) { d.Filename = "setup-x64.exe" }))
		r.click(r.s().done.open)
		if len(r.prompts) != 1 || !r.prompts[0].Executable || r.prompts[0].WithApp || r.prompts[0].Name != "setup-x64.exe" {
			t.Fatal(r.prompts)
		}
		if slices.Contains(r.api.Calls(), "openFile") {
			t.Fatal("opened")
		}
		r.modals[0].confirm()
		r.settle()
		if !slices.Contains(r.api.Calls(), "openFile") {
			t.Fatal(r.api.Calls())
		}
	})

	t.Run("stays open on failure with Retry and Close", func(t *testing.T) {
		r := show(t, running(nil))
		r.update(7, func(d *downexv1.Download) {
			d.State, d.Error, d.ErrorCode, d.SpeedBps = state.StateFailed, "Connection reset by the server", "network", 0
		})
		want(t, "state", r.stateLine(), "Failed: Connection reset by the server")
		if !visible(&r.s().statusV.hint.Element) || !strings.Contains(r.s().statusV.hint.Text.Text(), "Resuming keeps the data received so far") {
			t.Fatal(r.s().statusV.hint.Text.Text())
		}
		if r.api.closed != 0 || !visible(&r.s().actions.close.Element) {
			t.Fatal("closed or no Close")
		}
		r.click(r.s().actions.retry)
		if a := r.api.lastArgs("resume"); len(a) != 2 || a[1] != (resumeOpts{}) {
			t.Fatal(a)
		}
		want(t, "state", r.stateLine(), "Waiting in queue")
	})

	t.Run("starts over after the file changed, and offers no retry for a refused stream", func(t *testing.T) {
		r := show(t, running(func(d *downexv1.Download) {
			d.State, d.Error, d.ErrorCode = state.StateFailed, "The file on the server changed", "validator_changed"
		}))
		want(t, "button", r.s().actions.restart.Label().Text(), "Start over")
		r.click(r.s().actions.restart)
		if a := r.api.lastArgs("resume"); len(a) != 2 || a[1] != (resumeOpts{Restart: true}) {
			t.Fatal(a)
		}
		r.update(7, func(d *downexv1.Download) {
			d.State, d.Error, d.ErrorCode = state.StateFailed, "The stream is encrypted", "encrypted"
		})
		want(t, "state", r.stateLine(), "Failed: The stream is encrypted")
		if visible(&r.s().actions.retry.Element) || visible(&r.s().actions.restart.Element) {
			t.Fatal("a retry")
		}
		r.click(r.s().actions.close)
		if r.api.closed != 1 {
			t.Fatal(r.api.closed)
		}
	})
}

// t0 is the dialog tests' T0 (2026-10-20 08:00 UTC).
var t0 = time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC).UnixMilli()

// done is the dialog tests' done(): completed in 30 minutes, 754.25 s of it running.
func done(edit func(d *downexv1.Download)) *downexv1.Download {
	return running(func(d *downexv1.Download) {
		d.State, d.Received, d.SpeedBps, d.Connections = state.StateCompleted, 100*MiB, 0, 0
		d.Segments, d.ConnectionInfo = nil, nil
		d.CompletionDialog = true
		d.StartedAt, d.CompletedAt = formattest.Millis(t0), formattest.Millis(t0+30*60_000)
		d.ActiveMs = 754_250
		if edit != nil {
			edit(d)
		}
	})
}

func TestProgressWindowCompleteDialog(t *testing.T) {
	sent := func(r *rig) int { return r.api.count("setDownloadOptions") }

	t.Run("marks its download for the dialog once, when it first knows it", func(t *testing.T) {
		r := show(t, running(nil))
		if !r.api.item(7).GetCompletionDialog() {
			t.Fatal("not marked")
		}
		wantArgs(t, r, "{completionDialog: true}")
		r.update(7, func(d *downexv1.Download) { d.Received = 50 * MiB })
		r.update(7, func(d *downexv1.Download) { d.State, d.SpeedBps = state.StatePaused, 0 })
		r.loop.Send(kit.FocusEvent{In: true})
		r.settle()
		if sent(r) != 1 {
			t.Fatal(sent(r))
		}
	})

	t.Run("sends no mark for a download that is complete or marked already", func(t *testing.T) {
		r := show(t, done(func(d *downexv1.Download) { d.CompletionDialog = false }))
		if !r.completeShown() || slices.Contains(r.api.Calls(), "setDownloadOptions") {
			t.Fatal(r.api.Calls())
		}
	})

	t.Run("sends no mark for a download marked by an earlier window", func(t *testing.T) {
		r := show(t, running(func(d *downexv1.Download) { d.CompletionDialog = true }))
		if sent(r) != 0 {
			t.Fatal(sent(r))
		}
	})

	t.Run("marks again after downexd could not be reached, but not after a refusal", func(t *testing.T) {
		attempts := 0
		r := newRig(t, running(nil), nil, rigOptions{setup: func(a *fakeAPI) {
			a.setOptions = func(req *downexv1.SetDownloadOptionsRequest) (*downexv1.Download, error) {
				if req.CompletionDialog == nil || !req.GetCompletionDialog() {
					resp, err := a.applyOptions(req)
					return resp.GetDownload(), err
				}
				attempts++
				if attempts == 1 {
					return nil, errors.New(refused)
				}
				resp, err := a.applyOptions(req)
				return resp.GetDownload(), err
			}
		}})
		if attempts != 1 || r.api.item(7).GetCompletionDialog() {
			t.Fatal(attempts, r.api.item(7).GetCompletionDialog())
		}
		r.update(7, func(d *downexv1.Download) { d.Received = 30 * MiB })
		if !r.api.item(7).GetCompletionDialog() || attempts != 2 {
			t.Fatal(attempts)
		}

		refusals := 0
		newRig(t, running(func(d *downexv1.Download) { d.Id = 9 }), nil, rigOptions{setup: func(a *fakeAPI) {
			a.setOptions = func(*downexv1.SetDownloadOptionsRequest) (*downexv1.Download, error) {
				refusals++
				return nil, errors.New("failed_precondition: download 9 is being removed")
			}
		}})
		if refusals != 1 {
			t.Fatal(refusals)
		}
	})

	t.Run("shows the address, the saved file, its size and the time taken, with copy buttons", func(t *testing.T) {
		var copied []string
		r := newRig(t, done(nil), nil, rigOptions{setup: func(a *fakeAPI) {
			a.copyText = func(text string) error { copied = append(copied, text); return nil }
		}})
		if !strings.Contains(textOf(&r.s().done.Element), "distro-installer.iso") {
			t.Fatal("no name")
		}
		want(t, "Address", r.mustRow("Address"), longURL)
		want(t, "Saved to", r.mustRow("Saved to"), "/home/you/Downloads/Archives/distro-installer.iso")
		want(t, "File size", r.mustRow("File size"), "105 MB")
		// 100 MiB in 754.25 s of running
		want(t, "Time taken", r.mustRow("Time taken"), "12m 34s · average 139 KB/s")
		// A file has no stream format or quality.
		if _, ok := r.row("Format"); ok {
			t.Fatal("Format")
		}
		if _, ok := r.row("Quality"); ok {
			t.Fatal("Quality")
		}
		r.click(r.s().done.copyA)
		r.click(r.s().done.copyP)
		if !slices.Equal(copied, []string{longURL, "/home/you/Downloads/Archives/distro-installer.iso"}) {
			t.Fatal(copied)
		}
		want(t, "status", r.s().done.live.Semantics.Name, "Path copied")
		// The window is sized to the dialog.
		if s := r.lastSize(); s != (kit.Size{W: 520, H: r.fittedComplete()}) {
			t.Fatal(s, r.fittedComplete())
		}
		var labels []string
		for _, b := range []kit.Widget{r.s().done.open, r.s().done.openWith, r.s().done.folder, r.s().done.close} {
			if visible(b.Base()) {
				labels = append(labels, textOf(b.Base()))
			}
		}
		if !slices.Equal(labels, []string{"Open", "Open with…", "Open folder", "Close"}) {
			t.Fatal(labels)
		}
	})

	t.Run("counts from the first start without the running time, and shows a stream’s format and quality", func(t *testing.T) {
		r := show(t, formattest.MediaDownload(7, func(d *downexv1.Download) {
			d.State, d.Size, d.Received = state.StateCompleted, 300*MiB, 300*MiB
			d.Media = formattest.MediaInfo(func(m *downexv1.MediaInfo) { m.FragmentsDone = 200 })
			d.StartedAt, d.CompletedAt, d.ActiveMs = formattest.Millis(t0), formattest.Millis(t0+3723_000), 0
		}))
		want(t, "Manifest", r.mustRow("Manifest"), "https://media.example.org/talk/master.m3u8")
		want(t, "Saved to", r.mustRow("Saved to"), "/home/you/Downloads/Keynote.mp4")
		want(t, "Time taken", r.mustRow("Time taken"), "1h 02m 03s")
		want(t, "Format", r.mustRow("Format"), "HLS stream, saved as MP4")
		want(t, "Quality", r.mustRow("Quality"), "1080p · H.264 · AAC 128 kbit/s")
	})

	t.Run("shows the time taken with the average speed of the whole download, pauses excluded", func(t *testing.T) {
		r := show(t, done(func(d *downexv1.Download) { d.Size, d.Received, d.ActiveMs = 1368*MiB, 1368*MiB, 180_000 }))
		want(t, "Time taken", r.mustRow("Time taken"), "3m 00s · average 8.0 MB/s")
	})

	t.Run("says the time taken is unknown when downexd did not record it", func(t *testing.T) {
		r := show(t, done(func(d *downexv1.Download) { d.ActiveMs, d.StartedAt = 0, nil }))
		want(t, "Time taken", r.mustRow("Time taken"), "Unknown")
	})

	t.Run("opens with an app the user picks; closing that choice keeps the dialog", func(t *testing.T) {
		r := show(t, done(nil))
		choose := r.s().done.openWith
		r.api.openWith = func() error { return errors.New("canceled: no app was chosen") }
		r.click(choose)
		if r.api.closed != 0 || visible(&r.s().done.note.Element) {
			t.Fatal(r.api.closed, r.s().actionError)
		}
		// A system without the dialog says so.
		r.api.openWith = func() error {
			return errors.New("unimplemented: choosing an app to open a file with is not available on this system yet")
		}
		r.click(choose)
		if !visible(&r.s().done.note.Element) || !strings.Contains(r.s().done.note.Text.Text(), "not available on this system yet") || r.api.closed != 0 {
			t.Fatal(r.s().done.note.Text.Text())
		}
		r.api.openWith = nil
		r.click(choose)
		if r.api.closed != 1 {
			t.Fatal(r.api.closed)
		}
		if a := r.api.lastArgs("openWith"); len(a) != 1 || a[0] != int64(7) || slices.Contains(r.api.Calls(), "openFile") {
			t.Fatal(a, r.api.Calls())
		}
	})

	t.Run("does not offer Open with… where the host has no such dialog (macOS)", func(t *testing.T) {
		r := newRig(t, done(nil), nil, rigOptions{noOpenWith: true})
		if visible(&r.s().done.openWith.Element) || !visible(&r.s().done.open.Element) || !visible(&r.s().done.folder.Element) || !visible(&r.s().done.close.Element) {
			t.Fatal("buttons")
		}
		r.click(r.s().done.folder)
		if !slices.Contains(r.api.Calls(), "openFolder") || slices.Contains(r.api.Calls(), "openWith") {
			t.Fatal(r.api.Calls())
		}
	})

	t.Run("asks before a program is opened with another app", func(t *testing.T) {
		r := show(t, done(func(d *downexv1.Download) { d.Filename = "setup-x64.exe" }))
		r.click(r.s().done.openWith)
		if len(r.prompts) != 1 || !r.prompts[0].WithApp || !r.prompts[0].Executable {
			t.Fatal(r.prompts)
		}
		// Esc closes the question (the dialog's own), not the window.
		r.key(kit.KeyEscape, 0)
		r.modals[0].finish()
		r.settle()
		if r.api.closed != 0 || slices.Contains(r.api.Calls(), "openWith") || r.s().openPrompt != "" {
			t.Fatal(r.api.closed, r.api.Calls())
		}
		r.click(r.s().done.openWith)
		if len(r.modals) != 2 {
			t.Fatal(len(r.modals))
		}
		r.modals[1].confirm()
		r.settle()
		if !slices.Contains(r.api.Calls(), "openWith") || slices.Contains(r.api.Calls(), "openFile") {
			t.Fatal(r.api.Calls())
		}
	})

	t.Run("opens the file on Enter, with the focus on Open", func(t *testing.T) {
		r := show(t, done(nil))
		if f := r.pw.Kit.Focused(); f != r.s().done.open.Base() {
			t.Fatalf("focus on %v", f)
		}
		// Enter on another button is that button's.
		r.focus(r.s().done.folder)
		r.key(kit.KeyEnter, 0)
		if slices.Contains(r.api.Calls(), "openFile") || !slices.Contains(r.api.Calls(), "openFolder") {
			t.Fatal(r.api.Calls())
		}
		r2 := show(t, done(nil))
		r2.blur()
		r2.key(kit.KeyEnter, 0)
		if !slices.Contains(r2.api.Calls(), "openFile") || r2.api.closed != 1 {
			t.Fatal(r2.api.Calls(), r2.api.closed)
		}
	})

	t.Run("closes on Esc, which the progress view leaves alone", func(t *testing.T) {
		r := show(t, running(nil))
		r.key(kit.KeyEscape, 0)
		if r.api.closed != 0 {
			t.Fatal("closed")
		}
		r.update(7, func(d *downexv1.Download) { d.State, d.Received, d.ConnectionInfo = state.StateCompleted, 100*MiB, nil })
		if !r.completeShown() {
			t.Fatal("no complete view")
		}
		r.key(kit.KeyEscape, 0)
		if r.api.closed != 1 || slices.Contains(r.api.Calls(), "openFile") {
			t.Fatal(r.api.closed, r.api.Calls())
		}
	})

	t.Run("keeps the box as it was when the choice cannot be saved", func(t *testing.T) {
		r := show(t, done(nil))
		r.api.updateSettings = func(*downexv1.Settings) error { return errors.New("unavailable: downexd is not running") }
		box := r.s().done.dontShow
		r.click(box.Box)
		if !strings.Contains(r.s().done.prefError.Text(), "Could not save this choice: downexd is not running") || !visible(&r.s().done.prefError.Element) {
			t.Fatal(r.s().done.prefError.Text())
		}
		if box.Checked() {
			t.Fatal("checked")
		}
	})

	t.Run("returns to the progress, at its size, when the download starts again", func(t *testing.T) {
		r := show(t, done(nil))
		if s := r.lastSize(); s != (kit.Size{W: 520, H: r.fittedComplete()}) {
			t.Fatal(s)
		}
		r.update(7, func(d *downexv1.Download) { d.State, d.Received = state.StateQueued, 0 })
		want(t, "state", r.stateLine(), "Waiting in queue")
		// at the collapsed view's size: its facts' height, never below progress.CollapsedHeight
		if s := r.lastSize(); s != (kit.Size{W: 520, H: r.fittedCollapsed()}) || s.H < progress.CollapsedHeight {
			t.Fatal(s, r.fittedCollapsed())
		}
	})
}

// site is the video page tests' site().
func site(edit func(d *downexv1.Download)) *downexv1.Download {
	return formattest.SiteDownload(7, func(d *downexv1.Download) {
		d.State, d.Dir, d.Size, d.Received, d.SpeedBps = state.StateDownloading, "/home/you/Downloads/Video", 460*MiB, 115*MiB, 4*MiB
		d.Media = formattest.SiteMediaInfo(func(m *downexv1.MediaInfo) { m.FragmentsTotal, m.FragmentsDone = 46, 11 })
		d.ConnectionInfo = []*downexv1.ConnectionInfo{
			conn(1, func(c *downexv1.ConnectionInfo) { c.Fragment, c.Received = 11, 2*MiB }),
			conn(2, func(c *downexv1.ConnectionInfo) {
				c.Fragment, c.State = 12, downexv1.ConnectionState_CONNECTION_STATE_CONNECTING
			}),
		}
		if edit != nil {
			edit(d)
		}
	})
}

func TestProgressWindowVideoPages(t *testing.T) {
	t.Run("says \"Getting video info…\" while yt-dlp reads the page, then shows the page, parts and size", func(t *testing.T) {
		r := show(t, site(func(d *downexv1.Download) {
			d.State, d.Size, d.Received, d.ConnectionInfo = state.StateProbing, -1, 0, nil
		}))
		want(t, "state", r.stateLine(), "Getting video info…")
		want(t, "Page", r.mustRow("Page"), formattest.Page)
		if _, ok := r.row("Manifest"); ok {
			t.Fatal("Manifest")
		}
		next := site(nil)
		r.update(7, func(d *downexv1.Download) {
			upd := d.UpdatedAt
			proto.Reset(d)
			proto.Merge(d, next)
			d.UpdatedAt = upd
		})
		want(t, "state", r.stateLine(), "Receiving data…")
		want(t, "File size", r.mustRow("File size"), "482 MB")
		want(t, "Parts", r.mustRow("Parts"), "11 / 46")
		want(t, "Quality", r.mustRow("Quality"), "1080p · H.264 · AAC 129 kbit/s")
		want(t, "title", r.api.title, "25% A talk.mp4")
		r.click(r.s().actions.toggle)
		for _, w := range []string{"Part 12 · Receiving data…", "Part 13 · Connecting…"} {
			if !slices.Contains(r.infos(), w) {
				t.Fatal(r.infos())
			}
		}
		if r.s().details.frags.Label != "Parts downloaded" || !visible(&r.s().details.frags.Element) {
			t.Fatal(r.s().details.frags.Label)
		}
	})

	t.Run("says \"Merging…\" while the video and its sound become one MP4", func(t *testing.T) {
		r := show(t, site(func(d *downexv1.Download) {
			d.Received, d.SpeedBps, d.ConnectionInfo = 460*MiB, 0, nil
			d.Media = formattest.SiteMediaInfo(func(m *downexv1.MediaInfo) { m.FragmentsTotal, m.FragmentsDone = 46, 46 })
		}))
		want(t, "state", r.stateLine(), "Merging…")
		want(t, "rate", r.mustRow("Transfer rate"), "—")
		r.click(r.s().actions.toggle)
		if !slices.Contains(r.tableRows(), "Merging the video and its sound into one MP4 file; no connections.") {
			t.Fatal(r.tableRows())
		}
	})

	t.Run("ends with the site, quality and MP4 in the completion view", func(t *testing.T) {
		r := show(t, site(nil))
		r.update(7, func(d *downexv1.Download) {
			d.State, d.Received, d.ConnectionInfo = state.StateCompleted, 460*MiB, nil
			d.Media = formattest.SiteMediaInfo(func(m *downexv1.MediaInfo) { m.FragmentsTotal, m.FragmentsDone = 46, 46 })
		})
		if !r.completeShown() {
			t.Fatal("no complete view")
		}
		want(t, "Page", r.mustRow("Page"), formattest.Page)
		// The format and the quality as the human codec text, on rows of their own.
		want(t, "Format", r.mustRow("Format"), "YouTube video, saved as MP4")
		want(t, "Quality", r.mustRow("Quality"), "1080p · H.264 · AAC 129 kbit/s")
	})

	t.Run("shows the part of the video with its keyframe in the completion view", func(t *testing.T) {
		r := show(t, site(nil))
		r.update(7, func(d *downexv1.Download) {
			d.State, d.Received, d.ConnectionInfo = state.StateCompleted, 460*MiB, nil
			d.Media = formattest.SiteMediaInfo(func(m *downexv1.MediaInfo) {
				m.FragmentsTotal, m.FragmentsDone = 46, 46
				m.Clip = &downexv1.MediaClip{StartMs: 80_000, EndMs: 165_000, KeyframeMs: 77_400, FetchEndMs: 168_000}
			})
		})
		if !r.completeShown() {
			t.Fatal("no complete view")
		}
		want(t, "Part", r.mustRow("Part"), "1:20 – 2:45")
	})

	t.Run("explains a page that failed and offers no retry for a gone quality", func(t *testing.T) {
		r := show(t, site(func(d *downexv1.Download) {
			d.State, d.ErrorCode, d.Error, d.ConnectionInfo = state.StateFailed, "media_invalid", "the chosen quality is gone", nil
		}))
		if !strings.HasPrefix(r.stateLine(), "Failed:") {
			t.Fatal(r.stateLine())
		}
		want(t, "hint", r.s().statusV.hint.Text.Text(), "The chosen quality is no longer offered. Add the video again.")
		if visible(&r.s().actions.retry.Element) {
			t.Fatal("Retry")
		}
	})
}

var _ = statetest.Sync
