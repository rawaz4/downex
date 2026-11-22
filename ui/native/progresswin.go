//go:build windows || (darwin && cgo)

package main

import (
	"time"

	"downex/ui/kit"
	"downex/ui/views"
	"downex/ui/views/dialogs"
	"downex/ui/views/progress"
)

// openProgress opens the progress window of b's download (ProgressWindow.svelte,
// DESKTOP-SPEC §7 and §8) on a: the system's frame with the caption in the app's theme,
// today's 520 px wide page in a 300 px high window, fitted to its content by the window
// itself. What it holds is let go by b.later's functions once it has closed.
func openProgress(a *osApp, b *backend, o options) (*osWindow, error) {
	// Numbers, sizes and times in the user's locale (once for the process: userLocale).
	userLocale()
	pw, err := progress.New(progress.Options{
		ID: b.view.Progress, Started: b.view.Started, Daemon: b.daemon, Events: b.events, Platform: b.platform, Caps: b.caps,
		Theme: b.theme, Storage: b.storage, Call: b.call, Log: b.log,
		FixedTheme: o.fixed, SystemDark: o.systemDark, OpenPrompt: openPrompt,
	})
	if err != nil {
		return nil, err
	}
	b.later(pw.Dispose)
	// The frame the window will have (the window's FrameSize answers once it exists).
	initialFrame, minFrame := progressFrames()
	size := progress.InitialSize(initialFrame)
	if o.size != (kit.Size{}) {
		size = o.size
	}
	w, err := openWindow(a, pw.Root, windowSpec{
		Title: b.view.Window().Title, Size: size, MinSize: progress.MinSize(minFrame),
	}, o, b)
	if err != nil {
		return nil, err
	}
	firstFrame := b.since()
	if b.shown != nil {
		b.shown()
	}
	if b.attach != nil {
		b.attach(w, pw.Kit) // the pickers, the risky-file box, later starts, the theme followed
	}
	b.post = pw.Kit.Post
	if b.start != nil {
		b.start()
	}
	pw.Start()
	if o.report != "" {
		writeReport(o.report, w, firstFrame)
	}
	b.later(startStats(o.stats, pw.Kit))
	if o.exitAfter > 0 {
		time.AfterFunc(o.exitAfter, func() { _ = pw.Kit.Post(pw.Kit.Close) })
	}
	return w, nil
}

// openPrompt is the progress window's question before a program, or a name that hides its
// type, is opened: today's OpenDialog with the download's own name and kind.
func openPrompt(a *views.App, q progress.OpenQuestion) views.Modal {
	d := dialogs.NewOpenFile(a, dialogs.OpenFile{
		ID: q.ID, Name: q.Name, Executable: q.Executable, HiddenControls: q.HiddenControls, WithApp: q.WithApp,
		Disguise: q.Disguise,
	}, q.Confirm)
	return closedModal{Modal: d, closed: q.Closed}
}

// closedModal tells closed when its modal has left the window, however it went (Cancel,
// Escape, after its action, or dismissed).
type closedModal struct {
	views.Modal
	closed func()
}

// ShowModal is views.Modal's.
func (c closedModal) ShowModal(w *kit.Window, done func()) kit.Widget {
	return c.Modal.ShowModal(w, func() {
		if done != nil {
			done()
		}
		if c.closed != nil {
			c.closed()
		}
	})
}
