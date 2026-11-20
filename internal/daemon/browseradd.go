package daemon

import "downex/internal/model"

// AddWindow is the window that a download the user just started in the browser shows
// (Config.ShowBrowserAdd).
type AddWindow int

const (
	// AddProgress is its progress window (show_progress_window on, started at once).
	AddProgress AddWindow = iota + 1
	// AddConfirm is the standalone Confirm download window of the hand-off
	// (confirm_browser_downloads on; docs/DESKTOP-SPEC.md §1): the main window is not shown.
	AddConfirm
)

// openProgressFor opens the progress window of download id from source src: above the
// browser for a browser add (Config.ShowBrowserAdd), else as any other (OpenProgress).
// It is called outside m.mu.
// markWindowedLocked records that a progress window shows e's running download: the download
// complete dialog (completion_dialog) is due when the run ends, whatever the window's own mark
// does, and the run's outcome shows in that window. m.mu is held; e.run is set.
func (m *Manager) markWindowedLocked(e *entry) {
	if e.run == nil {
		return
	}
	e.run.windowed = true
	if !e.rec.CompletionDialog {
		on := true
		e.pendDialog = &on
	}
}

func (m *Manager) openProgressFor(id int64, src model.Source) error {
	if src == model.SourceBrowser && m.cfg.ShowBrowserAdd != nil {
		return m.cfg.ShowBrowserAdd(id, AddProgress)
	}
	return m.cfg.OpenProgress(id)
}
