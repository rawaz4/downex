package app

import (
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/state"
	"downex/ui/views"
)

// The hooks the lanes' dialogs call back with where App.svelte hands them more than the store
// call the lane makes by itself (addDownloads, addMediaDownload, confirmDownload: the
// progress window of a download that starts now, DESKTOP-SPEC §7). The others (the Remove,
// Restart, Open and Move dialogs' confirm, cancelDownload, Settings' onsaved) are the lanes'
// own defaults. The hooks take a context and run off the UI thread.

// SetScreens sets the lanes' constructors (a composition that needs the Main for their
// callbacks sets them after New); nil fields are the lanes'.
func (m *Main) SetScreens(s Screens) { m.screens = m.withLanes(s) }

// AddDownloads adds downloads; one that starts at once gets its progress window when that is
// on (a list of addresses opens none: one per download would bury the screen).
func (m *Main) AddDownloads(ctx contextT, reqs []*downexv1.AddRequest) []state.AddOutcome {
	out := m.App.Main.Downloads.Add(ctx, reqs)
	if len(out) == 1 && out[0].OK() {
		d := out[0].Download
		m.App.UI(func() { m.progressIfStarts(d) })
	}
	return out
}

// AddMedia adds a stream or a web page's video; it gets its progress window when that is on
// and it starts now.
func (m *Main) AddMedia(ctx contextT, req *downexv1.AddMediaRequest) (*downexv1.Download, error) {
	d, err := m.App.Main.Downloads.AddMedia(ctx, req)
	if err == nil {
		m.App.UI(func() { m.progressIfStarts(d) })
	}
	return d, err
}

// ConfirmDownload is the confirmation dialog's Start download / Download later: a download
// started gets its progress window when that is on and it starts now. downexd opens that
// window itself when it says so (progress_window_opening, asked for with
// ConfirmRequest.progress_window); this window opens it only for a downexd that does not.
func (m *Main) ConfirmDownload(ctx contextT, req *downexv1.ConfirmRequest) error {
	d, opening, err := m.App.Main.Downloads.Confirm(ctx, req)
	if err == nil && req.GetStart() && !opening {
		m.App.UI(func() { m.progressIfStarts(d) })
	}
	return err
}

// progressIfStarts opens a new download's progress window when that is on and downexd starts
// it now (DESKTOP-SPEC §7).
func (m *Main) progressIfStarts(d *downexv1.Download) {
	s, _ := m.settings()
	if d != nil && s.GetShowProgressWindow() && views.StartsNow(d) {
		m.openProgress(d.GetId())
	}
}
