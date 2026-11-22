// Package confirmwin is the standalone Confirm download window (docs/DESKTOP-SPEC.md §1): a
// download the user just started in the browser, with confirm_browser_downloads on, is
// confirmed in a compact window of its own, as a download manager's dialog is, and the main
// window is not shown. The window holds the Confirm download dialog of ui/views/add filling it
// (add.ConfirmOptions.Window), one download at a time, oldest first; it fits itself to each
// dialog (once the user resized it, it keeps the user's size for that dialog), never grows
// past what the dialog can use, and closes once nothing is left to confirm, or when the user
// closes it.
package confirmwin

import (
	"context"
	"math"
	"slices"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/confirm"
	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/views"
	"downex/ui/views/add"
)

// Title is the window's title.
const Title = "Confirm download"

// The window opens host.ConfirmWidth × host.ConfirmHeight (the dialog of a file) and fits the
// dialog shown; the user cannot make it smaller than host.ConfirmMinWidth × ConfirmMinHeight,
// nor wider than maxWidthRatio times the dialog's width or taller than its content.
const maxWidthRatio = 1.25

// CaptionFrame is the window's frame in theme t: a system caption as the progress windows'
// (tokens' CaptionFrame), on --raised, the ground of the dialog that fills the window, so that
// the caption and the dialog are one surface.
func CaptionFrame(t *tokens.Theme) kit.FrameColors {
	f := t.CaptionFrame()
	f.Background, f.Caption = t.Raised, t.Raised
	return f
}

// The host's window can do more than kit.Host's Resize (ui/win's window): resize about its
// centre, and keep the user from making it larger than a size. Hosts without them (the mac
// host, tests) resize keeping the top-left corner and have no largest size.
type (
	centredResizer interface{ ResizeCentered(kit.Size) }
	maxSizer       interface{ SetMaxSize(kit.Size) }
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
	// Resize asks for the window's client size in DIPs (nil: the kit window's host's
	// ResizeCentered when it has one and the user has not resized the window, else its Resize).
	Resize func(kit.Size)
	// MaxSize sets the largest client size the user can resize the window to, in DIPs, a side
	// of 0 having none (nil: the kit window's host's SetMaxSize, when it has one).
	MaxSize func(kit.Size)
	// Closing is told, once, that the window is about to close, before Close: from then on the
	// host gives a later start's "--confirm <id>" to a new Confirm download window, not to this
	// one, which would drop it (nil: nobody).
	Closing func()
	// Reopen takes the ids of a request that came after the window began to close (a start
	// the host handed to this window just before): the host opens a new Confirm download
	// window for them. It runs on the UI thread. Nil: they are dropped (the downloads still
	// await confirmation, and the main window lists them).
	Reopen func(ids []int64)
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
	// fitted is the dialog the window was last fitted to and natural the height its content
	// asked for then; asked is the size the window was asked for, from the size it had then.
	fitted        *add.Confirm
	natural       kit.DIP
	asked, from   kit.Size
	userSized     bool     // the user resized the window since the dialog was fitted: it is not fitted again
	everUserSized bool     // since the window opened: fits keep its top-left corner, not its centre
	maxSize       kit.Size // the largest size set (0 × 0: none)
	unwatch       []func()
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
// thread. Once the window is closing, the ids go to Options.Reopen instead (reopen): a request
// is never dropped for having come while the window closed.
func (s *Screen) Request(ids []int64) {
	if s.closed {
		s.reopen(ids)
		return
	}
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

// reopen hands the ids of a request that came after the window began to close to
// Options.Reopen, once each: those of downloads still awaiting confirmation, and those not
// listed yet (a browser add whose event has not come). A download the list shows confirmed,
// put off or cancelled meanwhile is not asked for again.
func (s *Screen) reopen(ids []int64) {
	if s.o.Reopen == nil {
		return
	}
	var left []int64
	for _, id := range ids {
		if id <= 0 || slices.Contains(left, id) {
			continue
		}
		if m := s.app.Main; m != nil && m.Downloads.Ready() && !m.Downloads.Stale() {
			if d, ok := m.Downloads.Get(id); ok && !views.IsAwaiting(d.GetState()) {
				continue
			}
		}
		left = append(left, id)
	}
	if len(left) > 0 {
		s.o.Reopen(left)
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
			// downexd opens the progress window of a Start itself, above the browser
			// (opening): this window opens it only when downexd says it does not.
			got, opening, err := s.app.Main.Downloads.Confirm(ctx, req)
			if err == nil && req.GetStart() && !opening {
				s.app.UI(func() { s.progress = got })
			}
			return err
		},
		OnClose: func() { s.dialogClosed(c, id) },
	})
	s.current, s.currentID = c, id
	s.app.Dialogs.Show(c)
	// The first layout measures the dialog at the width the window takes for it too: the
	// window fits it in one resize (LaidOut).
	c.MeasureAt(c.PreferredWidth(s.app.Window.Environment().Scale))
	if s.o.OnShow != nil {
		s.o.OnShow(id)
	}
	s.Relayout()
}

// dialogClosed: a download confirmed, put off or cancelled shows the next one (the progress
// window of one started that downexd does not open itself is asked for first, above the
// browser, so that it opens before this window closes); Escape closes the window.
func (s *Screen) dialogClosed(c *add.Confirm, id int64) {
	if s.current != c {
		return
	}
	s.current = nil
	s.handled[id] = true
	started := s.progress
	s.progress = nil
	if started != nil {
		// Asked for before this window closes: when it is the process's last window, a
		// host's request posted to the UI thread (which runs posts in order) opens the
		// progress window before the close, so the process does not end first; a start of
		// "--progress <id> --raise" hands over to whichever window process runs then.
		settings, _ := s.app.Settings()
		if settings.GetShowProgressWindow() && views.StartsNow(started) {
			sid := started.GetId()
			open := s.app.Platform.OpenProgressWindow
			if started.GetSource() == downexv1.AddSource_ADD_SOURCE_BROWSER {
				open = s.app.Platform.OpenProgressWindowRaised // over the browser, as downexd's would
			}
			if err := open(sid); err != nil {
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

// finish closes the window once, telling Options.Closing first: a start that comes while the
// close is on its way then opens a new window rather than reaching this one.
func (s *Screen) finish() {
	if s.closed {
		return
	}
	s.closed = true
	if s.o.Closing != nil {
		s.o.Closing()
	}
	if s.o.Close != nil {
		s.o.Close()
		return
	}
	s.app.Window.Close()
}

// Paint is kit.Painter's: the dialog's --raised ground, under the dialog and before it shows
// (the list and the settings are read first), so that the window never shows another colour.
func (s *Screen) Paint(p *kit.PaintContext) {
	p.Canvas.FillRect(p.Scale.SnapRect(kit.RectAt(p.Origin, p.Size())), views.TokensOf(&s.Element).Raised)
}

// LaidOut is kit.LayoutFollower's: the window fits the dialog shown, in one resize, when
// another dialog shows (its width, and its height at that width), and its height when what
// the dialog needs changes at the width the window was given (a note, another video). Once
// the user resized the window, it keeps the user's size until the next dialog. The window
// never grows past maxWidthRatio of the dialog's width nor past what its content needs at the
// window's width (the footer stays put and the body scrolls in a shorter window).
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
	pw := c.PreferredWidth(w.Environment().Scale)
	if pw <= 0 {
		pw = size.W
	}
	if c != s.fitted {
		h, ok := c.NaturalHeightAt(pw)
		if !ok {
			c.MeasureAt(pw) // the next layout measures it
			return
		}
		s.fitted, s.userSized = c, false
		s.fit(kit.Size{W: pw, H: ceil(h)}, h, pw)
		return
	}
	natural := c.NaturalHeight()
	if natural <= 0 {
		return
	}
	switch {
	case s.userSized:
	case near(size.W, s.asked.W):
		if math.Abs(float64(natural-s.natural)) >= 0.5 {
			s.fit(kit.Size{W: s.asked.W, H: ceil(natural)}, natural, pw)
			return
		}
	case near(size.W, s.from.W):
		return // the size asked for has not come yet
	default:
		// The window is not as wide as it was asked to be: the user resized it.
		s.userSized, s.everUserSized = true, true
	}
	s.limit(pw, natural)
}

// fit asks for size want, whose content asks for height natural, for a dialog pw wide.
func (s *Screen) fit(want kit.Size, natural, pw kit.DIP) {
	size := s.Window().Size()
	// Kept before the resize: its size may come back (and lay out) before Resize returns.
	s.natural, s.asked, s.from = natural, want, size
	if !near(want.W, size.W) || math.Abs(float64(want.H-size.H)) >= 0.5 {
		// The largest size holds a growing window back (a system clamps its own resizes to
		// it as well): set again once the resize is asked for.
		s.setMaxSize(kit.Size{})
		s.resize(want)
	}
	s.limit(pw, natural)
}

// limit keeps the user from making the window wider than maxWidthRatio of the dialog's width
// pw or taller than its content's height natural, never under the least size.
func (s *Screen) limit(pw, natural kit.DIP) {
	s.setMaxSize(kit.Size{
		W: max(ceil(pw*maxWidthRatio), host.ConfirmMinWidth),
		H: max(ceil(natural), host.ConfirmMinHeight),
	})
}

func (s *Screen) setMaxSize(sz kit.Size) {
	if sz == s.maxSize {
		return
	}
	s.maxSize = sz
	if s.o.MaxSize != nil {
		s.o.MaxSize(sz)
		return
	}
	if m, ok := s.app.Window.Host().(maxSizer); ok {
		m.SetMaxSize(sz)
	}
}

func (s *Screen) resize(sz kit.Size) {
	if s.o.Resize != nil {
		s.o.Resize(sz)
		return
	}
	h := s.app.Window.Host()
	if h == nil {
		return
	}
	if r, ok := h.(centredResizer); ok && !s.everUserSized {
		r.ResizeCentered(sz)
		return
	}
	h.Resize(sz)
}

// near reports two widths the same but for the rounding to device pixels.
func near(a, b kit.DIP) bool { return math.Abs(float64(a-b)) <= 1 }

func ceil(v kit.DIP) kit.DIP { return kit.DIP(math.Ceil(float64(v))) }

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
