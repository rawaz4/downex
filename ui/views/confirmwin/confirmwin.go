// Package confirmwin is the standalone Confirm download window (docs/DESKTOP-SPEC.md §1): a
// download the user just started in the browser, with confirm_browser_downloads on, is
// confirmed in a compact window of its own, as a download manager's dialog is, and the main
// window is not shown. The window holds the Confirm download dialog of ui/views/add filling it
// (add.ConfirmOptions.Window), one download at a time, oldest first; it fits its height to the
// dialog and closes once nothing is left to confirm, or when the user closes it.
package confirmwin

import (
	"context"
	"math"
	"slices"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/confirm"
	"downex/ui/kit"
	"downex/ui/views"
	"downex/ui/views/add"
)

// Title is the window's title.
const Title = "Confirm download"

// The window's sizes in DIPs: it opens Width × Height (the dialog of a file, before it is
// measured) and fits the dialog shown; the user cannot make it smaller than MinWidth × MinHeight.
const (
	Width     = 704 // the dialog's 44rem
	Height    = 420
	MinWidth  = 480
	MinHeight = 300
)

// Options are the window's parts beside its App (whose Main must be set: the downloads, the
// queues and the settings the dialog uses).
type Options struct {
	// IDs are the downloads asked for ("--confirm <id>"); later starts add theirs (Request).
	IDs []int64
	// Close closes the window (nil: the kit window's Close).
	Close func()
	// OnShow is told each download whose confirmation the window shows (the host keeps the
	// main window from showing it too); nil: nobody.
	OnShow func(id int64)
	// Resize asks for the window's client size in DIPs (nil: the kit window's host's Resize).
	Resize func(kit.Size)
}

// Screen is the window's content: the dialog of the download being confirmed.
type Screen struct {
	kit.Element
	app *views.App
	o   Options

	// requested are the ids asked for; floor the oldest of them: the browser's downloads
	// awaiting confirmation from it on came in the same burst and are shown in turn (downexd
	// asks for the oldest of a burst only).
	requested map[int64]bool
	floor     int64
	// handled are the downloads whose dialog this window showed and that closed.
	handled map[int64]bool

	current   *add.Confirm
	currentID int64
	// progress is a download Start download started: its progress window is asked for once
	// the dialog has gone, before the window moves on or closes (show_progress_window on and
	// it starts now).
	progress *downexv1.Download
	loading  bool
	closed   bool
	disposed bool
	// fitted is the dialog the window was last fitted to, and the height it was given.
	fitted       *add.Confirm
	fittedHeight kit.DIP
	unwatch      []func()
}

// New makes the window's content in app (shown with app's window). Nothing is shown until the
// list of downloads is read (app.Main.Downloads).
func New(app *views.App, o Options) *Screen {
	s := &Screen{app: app, o: o, requested: map[int64]bool{}, handled: map[int64]bool{}}
	s.Style = kit.Style{Display: kit.DisplayBlock, Width: kit.Pct(100), Height: kit.Pct(100)}
	s.Semantics.Role = kit.RoleNone
	s.Request(o.IDs)
	if app.Main != nil {
		s.unwatch = append(s.unwatch, app.Watch(&app.Main.Downloads.Changes, s.sync))
	}
	return s
}

// Request asks for the confirmation of ids too (a later start's "--confirm <id>"), on the UI
// thread.
func (s *Screen) Request(ids []int64) {
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		s.requested[id] = true
		delete(s.handled, id)
		if s.floor == 0 || id < s.floor {
			s.floor = id
		}
	}
	if len(ids) > 0 {
		s.sync()
	}
}

// Current is the dialog shown (nil: none yet).
func (s *Screen) Current() *add.Confirm { return s.current }

// Closed reports a window that asked to close.
func (s *Screen) Closed() bool { return s.closed }

// queue is the downloads to confirm here, oldest first: those asked for, and the browser's
// that came with them (from the oldest asked for on), less those already handled.
func (s *Screen) queue() []*downexv1.Download {
	var out []*downexv1.Download
	for _, d := range confirm.AwaitingInOrder(s.app.Main.Downloads.All()) {
		id := d.GetId()
		if s.handled[id] {
			continue
		}
		if s.requested[id] || (s.floor > 0 && id >= s.floor && d.GetSource() == downexv1.AddSource_ADD_SOURCE_BROWSER) {
			out = append(out, d)
		}
	}
	return out
}

// sync follows the list: a dialog whose download was confirmed or removed elsewhere goes; the
// next download shows; nothing left closes the window.
func (s *Screen) sync() {
	if s.closed || s.disposed || s.app.Main == nil {
		return
	}
	ds := s.app.Main.Downloads
	if !ds.Ready() || ds.Stale() {
		return
	}
	if s.current != nil {
		if d, ok := ds.Get(s.currentID); (ok && views.IsAwaiting(d.GetState())) || s.current.Busy() {
			return // waiting, or its own Start, Later or Cancel is on its way (dialogClosed follows)
		}
		// Confirmed or removed elsewhere (the main window, another device): it goes quietly.
		c := s.current
		s.current = nil
		s.handled[s.currentID] = true
		if s.app.Dialogs.Dialog() == views.Modal(c) {
			c.DismissModal()
		}
	}
	s.next()
}

// next shows the oldest download left, with the settings read first; or closes the window.
func (s *Screen) next() {
	q := s.queue()
	if len(q) == 0 {
		if len(s.requested) > 0 {
			s.finish()
		}
		return // nothing asked for yet: the list was read before the first Request
	}
	if _, loaded := s.app.Settings(); !loaded {
		if s.loading {
			return
		}
		s.loading = true
		s.app.Go(func() {
			s.app.Main.LoadSettings()
			s.app.UI(func() {
				s.loading = false
				if _, ok := s.app.Settings(); ok {
					s.sync()
				}
			})
		})
		return
	}
	s.show(q[0], len(q))
}

func (s *Screen) show(d *downexv1.Download, waiting int) {
	settings, _ := s.app.Settings()
	id := d.GetId()
	var c *add.Confirm
	c = add.NewConfirm(s.app, d, add.ConfirmOptions{
		Settings: settings,
		Waiting:  waiting,
		Window:   true,
		Confirm: func(ctx context.Context, req *downexv1.ConfirmRequest) error {
			got, err := s.app.Main.Downloads.Confirm(ctx, req)
			if err == nil && req.GetStart() {
				s.app.UI(func() { s.progress = got })
			}
			return err
		},
		OnClose: func() { s.dialogClosed(c, id) },
	})
	s.current, s.currentID = c, id
	s.app.Dialogs.Show(c)
	if s.o.OnShow != nil {
		s.o.OnShow(id)
	}
	s.Relayout()
}

// dialogClosed: a download confirmed, put off or cancelled shows the next one (the progress
// window of one started is asked for first, so that it opens before this window closes);
// Escape closes the window.
func (s *Screen) dialogClosed(c *add.Confirm, id int64) {
	if s.current != c {
		return
	}
	s.current = nil
	s.handled[id] = true
	started := s.progress
	s.progress = nil
	if started != nil {
		// Asked for before this window closes: when it is the process's last window, the
		// host's request (posted to the UI thread, which runs posts in order) opens the
		// progress window before the close, so the process does not end first.
		settings, _ := s.app.Settings()
		if settings.GetShowProgressWindow() && views.StartsNow(started) {
			sid := started.GetId()
			if err := s.app.Platform.OpenProgressWindow(sid); err != nil {
				s.app.Log.Warn("cannot open the progress window", "download", sid, "error", err)
			}
		}
	}
	if !c.Completed() {
		s.finish()
	} else {
		s.sync()
		if s.current == nil && !s.closed {
			s.finish() // nothing shown: the list is not ready
		}
	}
}

// finish closes the window once.
func (s *Screen) finish() {
	if s.closed {
		return
	}
	s.closed = true
	if s.o.Close != nil {
		s.o.Close()
		return
	}
	s.app.Window.Close()
}

// LaidOut is kit.LayoutFollower's: the window fits the dialog shown, its width when another
// dialog shows, its height whenever what the dialog needs changes.
func (s *Screen) LaidOut() {
	c := s.current
	if c == nil || s.disposed {
		return
	}
	w := s.Window()
	if w == nil {
		return
	}
	size := w.Size()
	natural := c.NaturalHeight()
	if natural <= 0 {
		return
	}
	want := size
	if c != s.fitted {
		if pw := c.PreferredWidth(w.Environment().Scale); pw > 0 {
			want.W = pw
		}
	}
	if c == s.fitted && math.Abs(float64(natural-s.fittedHeight)) < 0.5 {
		return
	}
	want.H = kit.DIP(math.Ceil(float64(natural)))
	s.fitted, s.fittedHeight = c, natural
	if math.Abs(float64(want.W-size.W)) < 0.5 && math.Abs(float64(want.H-size.H)) < 0.5 {
		return
	}
	s.resize(want)
}

func (s *Screen) resize(sz kit.Size) {
	if s.o.Resize != nil {
		s.o.Resize(sz)
		return
	}
	if h := s.app.Window.Host(); h != nil {
		h.Resize(sz)
	}
}

// Dispose stops following the list and takes the dialog away.
func (s *Screen) Dispose() {
	if s.disposed {
		return
	}
	s.disposed = true
	for _, f := range s.unwatch {
		f()
	}
	s.unwatch = nil
	s.app.Dialogs.DismissAll()
}

// Handled reports the downloads whose dialog showed here and closed (tests).
func (s *Screen) Handled() []int64 {
	var ids []int64
	for id := range s.handled {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}
