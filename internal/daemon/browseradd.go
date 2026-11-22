package daemon

import "downex/internal/model"

// AddWindow is the window that a download the user just started in the browser shows
// (Config.ShowBrowserAdd).
type AddWindow int

const (
	// AddProgress is its progress window: the outcome of a run that a progress window
	// shows (docs/DESKTOP-SPEC.md §8), brought above the browser.
	AddProgress AddWindow = iota + 1
	// AddConfirm is the standalone Confirm download window of the hand-off
	// (confirm_browser_downloads on; docs/DESKTOP-SPEC.md §1): the main window is not shown.
	AddConfirm
	// AddProgressStarted is its progress window as the download starts (show_progress_window
	// on): a Start of its confirmation, a hand-off that starts at once, a rescue's or a
	// renewal's first start. The window shows the progress for a moment even when the
	// download completes at once, before its completion (docs/DESKTOP-SPEC.md §7; downexd
	// adds "--started").
	AddProgressStarted
)

// nextWindow says that the next run of a stopped download shows in a progress window
// (entry.next): the run is marked as it starts (startLocked).
type nextWindow uint8

const (
	nextNone nextWindow = iota
	// nextShown: a window of the user's is open on the download (SetDownloadOptions with
	// completion_dialog, or Resume with progress_window, while it is stopped or queued).
	nextShown
	// nextLaunched: downexd opened the window itself for a Start that waits in its queue
	// (Confirm, docs/DESKTOP-SPEC.md §7); a failed launch takes the mark back
	// (unmarkWindowed).
	nextLaunched
)

// markWindowedLocked records that a progress window shows e's running download: the
// download complete dialog (completion_dialog) is due when the run ends, whatever the
// window's own mark does, and the run's outcome shows in that window (the run is
// windowed, docs/DESKTOP-SPEC.md §8). shown: the window is the user's own (it marked the
// run itself), so the mark stays whatever happens; otherwise downexd opens it, and a
// failed launch takes the mark back (unmarkWindowed). m.mu is held; nothing happens
// without a run.
func (m *Manager) markWindowedLocked(e *entry, shown bool) {
	r := e.run
	if r == nil {
		return
	}
	r.windowed = true
	r.shown = r.shown || shown
	if !e.rec.CompletionDialog {
		on := true
		e.pendDialog = &on
		r.marked = e.pendDialog
	}
}

// showProgressLocked marks e's run for the progress window that downexd opens itself and
// returns the launch that opens it (openProgressFor with started set, from source src),
// which the caller runs on a goroutine of its own once m.mu is released: m.wg is added
// for it. A launch that fails takes the mark back (unmarkWindowed). m.mu is held; e.run
// is set.
func (m *Manager) showProgressLocked(e *entry, src model.Source) func() {
	m.markWindowedLocked(e, false)
	return m.launchLocked(e.id, src, e.run)
}

// launchLocked returns the launch of download id's progress window as it starts (see
// showProgressLocked): for run r, or, with r nil, for the next run (entry.next is
// nextLaunched). m.mu is held: m.wg is added for the launch, which the caller always runs.
func (m *Manager) launchLocked(id model.DownloadID, src model.Source, r *run) func() {
	m.wg.Add(1)
	return func() {
		defer m.wg.Done()
		if err := m.openProgressFor(int64(id), src, true); err != nil {
			m.log.Warn("cannot open the progress window", "download", int64(id), "err", err)
			m.unmarkWindowed(id, r)
		}
	}
}

// unmarkWindowed takes back the mark of a progress window that downexd could not open
// (launchLocked): run r is no longer windowed, and the completion_dialog it set is not
// stored, so its outcome goes to the tray as a notification instead of a lone download
// complete dialog (docs/DESKTOP-SPEC.md §8). With r nil it is the mark of the next run
// (nextLaunched), or of the run that it marked meanwhile. A run that a window of the
// user's marked too (shown), or one that has ended, keeps its mark.
func (m *Manager) unmarkWindowed(id model.DownloadID, r *run) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entries[id]
	if e == nil {
		return
	}
	if r == nil {
		if e.next == nextLaunched {
			e.next = nextNone
			return
		}
		if r = e.run; r == nil {
			return
		}
	}
	if e.run != r || r.shown || r.finalizing {
		return
	}
	r.windowed = false
	if r.marked != nil && e.pendDialog == r.marked {
		e.pendDialog = nil
	}
	r.marked = nil
}

// openProgressFor opens the progress window of download id from source src: above the
// browser for a browser add (Config.ShowBrowserAdd: AddProgressStarted as it starts,
// AddProgress for a run's outcome), else as any other (OpenProgress). It is called
// outside m.mu.
func (m *Manager) openProgressFor(id int64, src model.Source, started bool) error {
	if src == model.SourceBrowser && m.cfg.ShowBrowserAdd != nil {
		w := AddProgress
		if started {
			w = AddProgressStarted
		}
		return m.cfg.ShowBrowserAdd(id, w)
	}
	return m.cfg.OpenProgress(id)
}
