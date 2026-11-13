package shell

import (
	"math"
	"strconv"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/queues"
	"downex/ui/core/text"
	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/views"
)

// ---- ConnectionBanner.svelte ------------------------------------------------------------------

// The connection banner's texts.
const (
	bannerDownTitle = "Downex can’t reach downexd, its download service"
	bannerLoadTitle = "Downex couldn’t load your downloads from downexd"
	bannerStale     = "The list shows the last known state, so speeds and times are not current. Adding, pausing and removing downloads is unavailable until it reconnects. "
	bannerLostAlert = "Lost connection to downexd. The list shows the last known state and actions are unavailable until it reconnects."
	bannerLoadAlert = "Downex couldn’t load your downloads from downexd."
	bannerBack      = "Reconnected to downexd."
)

// bannerEvery is how often the status is checked, in seconds (STATUS_INTERVAL_MS / 1000).
var bannerEvery = strconv.Itoa(int(math.Round(state.StatusInterval.Seconds())))

// ConnState is what the connection banner shows (ConnectionBanner.svelte's props).
type ConnState struct {
	Daemon *state.DaemonState
	// LoadError is why List() failed ("" when it did not).
	LoadError string
	// Checking: a status check is running.
	Checking bool
	// NextCheckAt is when the next automatic check runs (zero: not scheduled).
	NextCheckAt time.Time
}

// Down reports that the banner shows: downexd is unreachable or the list could not be read.
func (s ConnState) Down() bool { return s.daemonDown() || s.LoadError != "" }

func (s ConnState) daemonDown() bool { return s.Daemon != nil && !s.Daemon.OK }

// ConnectionBanner is the strip across the top of the work card while downexd is unreachable
// or the list could not be read (ConnectionBanner.svelte, DESIGN-SYSTEM.md §3.17): what
// happened, what still works, how to start downexd, the raw error behind "Technical details",
// the countdown to the next try and Retry now. It announces the outage once assertively and the
// recovery politely; the countdown is not a live region.
type ConnectionBanner struct {
	kit.Element
	st      ConnState
	now     func() time.Time
	onRetry func()

	icon      *widgets.Icon
	body      *kit.Element
	title     *views.Text
	para      *richText
	details   *disclosure
	actions   *kit.Element
	countdown *views.Text
	retry     *widgets.Button
	timer     *kit.Timer

	wasDown bool
	// Alert and Status are the live regions' texts (role=alert, role=status).
	Alert, Status string
}

// NewConnectionBanner makes the banner; now is the wall clock (App.Clock.Now).
func NewConnectionBanner(now func() time.Time, onRetry func()) *ConnectionBanner {
	b := &ConnectionBanner{now: now, onRetry: onRetry}
	b.Semantics = kit.Semantics{Role: kit.RoleRegion}
	b.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignStart, ColumnGap: kit.Px(10),
		Padding: kit.Edges{Top: kit.Px(10), Bottom: kit.Px(10), Start: kit.Px(20), End: kit.Px(14)},
		Border:  kit.Edges{Bottom: kit.Px(1)}, Hidden: true,
		Breakpoints: []kit.Breakpoint{{When: kit.Query{MaxWidth: kit.Rem(46)}, Apply: func(s *kit.Style) { s.Wrap = true }}},
	}
	b.icon = widgets.NewIcon(icons.Alert, 18, func(t *tokens.Theme) kit.Color { return t.Danger })
	b.icon.Style.Margin = kit.Edges{Top: kit.Px(1)}
	b.title = views.NewParagraph(bannerDownTitle, kit.Font{Family: tokens.FontUI, Size: tokens.TypeBody, Weight: 600, LineHeight: 20.0 / 13, Tabular: true, AutoOptical: true}, views.InkInk)
	b.title.Semantics.Role = kit.RoleHeading
	b.title.Semantics.Level = 2
	b.para = newRichText()
	b.details = newDisclosure("Technical details")
	b.body = &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, RowGap: kit.Px(3), Flex: kit.Flex1, MinWidth: kit.Px(0)}}
	b.body.Add(b.title, b.para, b.details)
	b.countdown = views.NewText("", uiFont(tokens.TypeSmall, 400), views.InkMuted, kit.TextOptions{Align: kit.TextAlignEnd})
	b.countdown.Style.MinWidth = kit.Rem(0.75 * 7.5) // min-inline-size: 7.5em of --type-small
	b.retry = widgets.NewButton("Retry now", "refresh", widgets.Secondary, widgets.SizeSm, func() {
		if !b.st.Checking && b.onRetry != nil {
			b.onRetry()
		}
	})
	b.retry.SetIconSize(14)
	b.actions = &kit.Element{Style: kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(10), Flex: kit.FlexNone,
		Breakpoints: []kit.Breakpoint{{When: kit.Query{MaxWidth: kit.Rem(46)}, Apply: func(s *kit.Style) {
			s.Width, s.Justify = kit.Pct(100), kit.JustifyEnd
		}}},
	}}
	b.actions.Add(b.countdown, b.retry)
	b.Add(b.icon, b.body, b.actions)
	return b
}

// Update shows st: the banner appears or leaves, and the live regions speak once at each
// change.
func (b *ConnectionBanner) Update(st ConnState) {
	b.st = st
	down := st.Down()
	daemonDown := st.daemonDown()
	if down && !b.wasDown {
		b.Status = ""
		b.Alert = bannerLoadAlert
		if daemonDown {
			b.Alert = bannerLostAlert
		}
		b.announce(b.Alert, kit.LiveAssertive)
	} else if !down && b.wasDown {
		b.Alert = ""
		b.Status = bannerBack
		b.announce(b.Status, kit.LivePolite)
	}
	b.wasDown = down
	b.SetHidden(!down)
	if !down {
		b.stopClock()
		return
	}
	title := bannerLoadTitle
	if daemonDown {
		title = bannerDownTitle
	}
	setText(b.title, title)
	b.Semantics.Name = title
	b.SemanticsChanged()
	lead := ""
	if daemonDown {
		lead = bannerStale
	}
	cell := uiFont(tokens.TypeCell, 400)
	code := views.FontMono
	code.Size = tokens.TypeSmall
	b.para.set([]Run{
		{Text: lead + "Downex tries again every " + bannerEvery + " seconds. If downexd isn’t running, start it with ", Font: cell, Ink: views.InkInk2},
		{Text: "downex daemon start", Font: code, Ink: views.InkInk2},
		{Text: ".", Font: cell, Ink: views.InkInk2},
	})
	errText := ""
	if daemonDown {
		errText = st.Daemon.Error
	} else {
		errText = st.LoadError
	}
	b.details.SetHidden(errText == "")
	b.details.setRaw(text.EscapeBidi(errText))
	b.retry.SetDisabled(st.Checking, true)
	b.tick()
	b.startClock()
}

func (b *ConnectionBanner) announce(s string, p kit.Politeness) {
	if w := b.Window(); w != nil && s != "" {
		w.Announce(s, p, b)
	}
}

// Seconds is the countdown's whole seconds to the next try (0: none scheduled or due).
func (b *ConnectionBanner) Seconds() int {
	if b.st.NextCheckAt.IsZero() {
		return 0
	}
	d := b.st.NextCheckAt.Sub(b.now())
	return max(0, int(math.Ceil(d.Seconds())))
}

// CountdownText is "Checking…", "Next try in 2 s" or "".
func (b *ConnectionBanner) CountdownText() string { return b.countdown.Text() }

func (b *ConnectionBanner) tick() {
	s := ""
	switch n := b.Seconds(); {
	case b.st.Checking:
		s = "Checking…"
	case n > 0:
		s = "Next try in " + strconv.Itoa(n) + " s"
	}
	setText(b.countdown, s)
}

// startClock runs a 1 Hz clock for the countdown while the banner is shown.
func (b *ConnectionBanner) startClock() {
	w := b.Window()
	if w == nil || b.timer != nil {
		return
	}
	var step func()
	step = func() {
		b.timer = nil
		if !b.st.Down() || b.Window() == nil {
			return
		}
		b.tick()
		b.timer = w.After(time.Second, step)
	}
	b.timer = w.After(time.Second, step)
}

func (b *ConnectionBanner) stopClock() {
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
}

// Attached is kit.Attacher's: the clock starts if the banner shows.
func (b *ConnectionBanner) Attached() {
	if b.st.Down() {
		b.startClock()
	}
}

// Detached is kit.Detacher's.
func (b *ConnectionBanner) Detached() { b.stopClock() }

// Paint is kit.Painter's: --danger-soft over a --danger-line rule.
func (b *ConnectionBanner) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&b.Element)
	r := kit.RectAt(p.Origin, p.Size())
	p.Canvas.FillRect(r, t.DangerSoft)
	w := p.Scale.SnapWidth(1)
	line := t.DangerLine
	if t.IsForced() {
		line = t.Ink
	}
	y := p.Scale.Snap(r.Max.Y) - w
	p.Canvas.FillRect(kit.R(r.Min.X, y, r.Max.X, y+w), line)
}

// The banner's parts, for tests.
func (b *ConnectionBanner) TitleText() string      { return b.title.Text() }
func (b *ConnectionBanner) Paragraph() string      { return b.para.Text() }
func (b *ConnectionBanner) Raw() string            { return b.details.raw.Text() }
func (b *ConnectionBanner) Retry() *widgets.Button { return b.retry }
func (b *ConnectionBanner) Details() *kit.Element  { return b.details.Base() }

// disclosure is <details><summary>: a summary (--muted, --type-small) that shows and hides the
// raw text under it (.raw: mono --type-small on --surface, a --line border, --r-sm corners,
// 4 px 8 px in, 4 px below the summary, wrapping anywhere).
type disclosure struct {
	kit.Element
	summary *summaryButton
	raw     *views.Text
	rawBox  *rawBox
	open    bool
}

func newDisclosure(label string) *disclosure {
	d := &disclosure{}
	d.Style = kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, AlignItems: kit.AlignStart}
	d.summary = newSummaryButton(label, func() { d.toggle() })
	mono := views.FontMono
	mono.Size = tokens.TypeSmall
	d.raw = views.NewText("", mono, views.InkInk, kit.TextOptions{Wrap: kit.TextWrapAnywhere, Direction: kit.DirAuto})
	d.rawBox = &rawBox{}
	d.rawBox.Style = kit.Style{
		Display: kit.DisplayFlex, Width: kit.Pct(100), Margin: kit.Edges{Top: tokens.Sp2},
		Padding: kit.Edges{Top: tokens.Sp2, Bottom: tokens.Sp2, Start: tokens.Sp4, End: tokens.Sp4},
		Border:  kit.EdgesAll(kit.Px(1)), Hidden: true,
	}
	d.rawBox.Add(d.raw)
	d.Add(d.summary, d.rawBox)
	return d
}

func (d *disclosure) setRaw(s string) { setText(d.raw, s) }

func (d *disclosure) toggle() {
	d.open = !d.open
	d.rawBox.SetHidden(!d.open)
	d.summary.Semantics.Expand = kit.ExpandClosed
	if d.open {
		d.summary.Semantics.Expand = kit.ExpandOpen
	}
	d.summary.setOpen(d.open)
	d.summary.SemanticsChanged()
}

// Open reports whether the raw text shows.
func (d *disclosure) Open() bool { return d.open }

type rawBox struct{ kit.Element }

// Paint is kit.Painter's.
func (r *rawBox) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&r.Element)
	box := views.SnapBox(p, 6)
	p.Canvas.FillRoundedRect(box, t.Surface)
	w := p.Scale.SnapWidth(1)
	p.Canvas.StrokeRoundedRect(box.Inset(w/2), kit.Stroke{Width: w}, t.Line)
}

// summaryButton is <summary>: the disclosure marker (Chromium's ::marker, "▸ " closed and
// "▾ " open) and the label, in --muted --type-small.
type summaryButton struct {
	kit.Pressable
	label *views.Text
	text  string
}

func newSummaryButton(label string, onPress func()) *summaryButton {
	s := &summaryButton{text: label}
	s.OnPress = onPress
	s.Focus = kit.FocusTab
	s.Semantics = kit.Semantics{Role: kit.RoleButton, Name: label, Expand: kit.ExpandClosed}
	s.Cursor = kit.CursorPointer
	s.Style = kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Flex: kit.FlexNone}
	s.label = views.NewText("▸ "+label, uiFont(tokens.TypeSmall, 400), views.InkMuted, kit.TextOptions{})
	s.label.Semantics.Role = kit.RoleNone
	s.label.PassThrough = true
	s.Add(s.label)
	return s
}

// setOpen shows the marker of the state.
func (s *summaryButton) setOpen(open bool) {
	m := "▸ "
	if open {
		m = "▾ "
	}
	setText(s.label, m+s.text)
}

// ---- PostActionBanner.svelte ------------------------------------------------------------------

// PostActionBanner is the 40 px strip at the very top of the window while a queue's post-queue
// action counts down (PostActionBanner.svelte, DESIGN-SYSTEM.md §3.17): "Shutting down in 42
// s", the queue that finished, and Cancel (or Escape on it). It is announced once when it
// starts, takes the focus when it appears (also from an open dialog, whose trap lets it keep
// it: kit.ExemptKeep) and stays usable above a dialog's scrim (views.ZBanner).
type PostActionBanner struct {
	kit.Element
	pending   *downexv1.PostActionPending
	queueName string
	now       func() time.Time
	onCancel  func(done func(err error))

	icon    *widgets.Icon
	title   *views.Text
	detail  *views.Text
	failure *views.Text
	cancel  *widgets.Button
	timer   *kit.Timer
	busy    bool
	// Announced is the alert spoken once when the countdown starts.
	Announced string
}

// NewPostActionBanner makes the banner of a countdown; onCancel asks downexd to cancel it and
// calls done with the daemon's error (nil when it worked). now is the wall clock.
func NewPostActionBanner(p *downexv1.PostActionPending, queueName string, now func() time.Time, onCancel func(done func(err error))) *PostActionBanner {
	b := &PostActionBanner{pending: p, queueName: queueName, now: now, onCancel: onCancel}
	b.Semantics = kit.Semantics{Role: kit.RoleRegion}
	b.Exempt = kit.ExemptKeep
	b.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(10), Flex: kit.FlexNone,
		MinHeight: kit.Px(40), Padding: kit.Edges{Top: kit.Px(4), Bottom: kit.Px(4), Start: kit.Px(14), End: kit.Px(10)},
		Border: kit.Edges{Bottom: kit.Px(1)}, Width: kit.Pct(100),
	}
	b.icon = widgets.NewIcon(icons.Power, 16, func(t *tokens.Theme) kit.Color { return t.Warn })
	b.title = views.NewText("", uiFont(tokens.TypeBody, 600), views.InkWarn, kit.TextOptions{})
	b.title.Style.Flex = kit.FlexNone
	b.title.Semantics.Role = kit.RoleHeading
	b.title.Semantics.Level = 2
	b.title.Semantics.Live = kit.LiveOff
	b.detail = views.NewLine(text.Isolate(queueName)+" finished its downloads.", uiFont(tokens.TypeCell, 400), views.InkInk2)
	b.detail.Style.Flex = kit.Flex1
	b.failure = views.NewParagraph("", views.FontHint, views.InkDanger)
	b.failure.Style.Flex = kit.FlexNone
	b.failure.Style.MaxWidth = kit.Pct(40)
	b.failure.Label.SetOptions(kit.TextOptions{Wrap: kit.TextWrapAnywhere})
	b.failure.SetHidden(true)
	b.cancel = widgets.NewButton("Cancel", "", widgets.Secondary, widgets.SizeSm, b.doCancel)
	b.cancel.Semantics.Shortcut = "Escape"
	b.Add(b.icon, b.title, b.detail, b.failure, b.cancel)
	left := int64(60)
	if DeadlineMs(p) > 0 {
		left = queues.SecondsLeft(DeadlineMs(p), b.now().UnixMilli())
	}
	b.Announced = queues.PostActionProgress(p.GetAction()) + " in about " + strconv.FormatInt(left, 10) + " seconds, because the queue " + text.EscapeBidi(queueName) + " finished. Cancel (or Escape) to stop it."
	b.tick()
	return b
}

// DeadlineMs is a countdown's deadline in epoch ms (0: not known), as api.ts reads it.
func DeadlineMs(p *downexv1.PostActionPending) int64 {
	d := p.GetDeadline()
	if d == nil || (d.GetSeconds() == 0 && d.GetNanos() == 0) {
		return 0
	}
	return d.AsTime().UnixMilli()
}

// Key is the countdown's identity (App.svelte's {#key queueId:deadline}): a new key is a new
// banner.
func (b *PostActionBanner) Key() string {
	return strconv.FormatInt(b.pending.GetQueueId(), 10) + ":" + strconv.FormatInt(DeadlineMs(b.pending), 10)
}

// Text is the countdown: "Shutting down in 42 s", "… now…", "… soon".
func (b *PostActionBanner) Text() string { return b.title.Text() }

// Detail is the second line, without its isolates.
func (b *PostActionBanner) Detail() string {
	return text.EscapeBidi(b.queueName) + " finished its downloads."
}

// Failure is the daemon's error after a failed cancel ("" for none).
func (b *PostActionBanner) Failure() string { return b.failure.Text() }

// CancelButton is the Cancel button.
func (b *PostActionBanner) CancelButton() *widgets.Button { return b.cancel }

func (b *PostActionBanner) tick() {
	verb := queues.PostActionProgress(b.pending.GetAction())
	s := verb + " soon"
	if DeadlineMs(b.pending) > 0 {
		left := queues.SecondsLeft(DeadlineMs(b.pending), b.now().UnixMilli())
		if left > 0 {
			s = verb + " in " + strconv.FormatInt(left, 10) + " s"
		} else {
			s = verb + " now…"
		}
	}
	setText(b.title, s)
	if b.Semantics.Name != s {
		b.Semantics.Name = s
		b.SemanticsChanged()
	}
}

// Attached is kit.Attacher's: the 250 ms clock, the announcement and, when asked, the focus.
func (b *PostActionBanner) Attached() {
	w := b.Window()
	var step func()
	step = func() {
		b.timer = nil
		if b.Window() == nil {
			return
		}
		b.tick()
		b.timer = w.After(250*time.Millisecond, step)
	}
	b.timer = w.After(250*time.Millisecond, step)
	w.Announce(b.Announced, kit.LiveAssertive, b)
}

// Detached is kit.Detacher's.
func (b *PostActionBanner) Detached() {
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
}

// Focus gives the Cancel button the focus (the banner takes it when it appears, unless the
// context menu is open).
func (b *PostActionBanner) Focus() {
	if w := b.Window(); w != nil {
		w.Focus(b.cancel)
	}
}

func (b *PostActionBanner) doCancel() {
	if b.busy || b.onCancel == nil {
		return
	}
	b.busy = true
	b.failure.SetHidden(true)
	setText(b.failure, "")
	b.cancel.SetText("Cancelling…")
	b.cancel.SetDisabled(true, true)
	b.onCancel(func(err error) {
		b.busy = false
		b.cancel.SetText("Cancel")
		b.cancel.SetDisabled(false, true)
		if err != nil {
			setText(b.failure, text.Isolate(views.ErrorMessage(err)))
			b.failure.SetHidden(false)
		}
	})
}

// HandleKey is kit.KeyHandler's: Escape on the countdown cancels it (not an open dialog behind
// it).
func (b *PostActionBanner) HandleKey(e kit.KeyEvent) bool {
	if e.Action != kit.KeyDown || e.Key != kit.KeyEscape {
		return false
	}
	b.doCancel()
	return true
}

// Paint is kit.Painter's: --warn-soft over a --warn-line rule.
func (b *PostActionBanner) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&b.Element)
	r := kit.RectAt(p.Origin, p.Size())
	p.Canvas.FillRect(r, t.WarnSoft)
	w := p.Scale.SnapWidth(1)
	line := t.WarnLine
	if t.IsForced() {
		line = t.Ink
	}
	y := p.Scale.Snap(r.Max.Y) - w
	p.Canvas.FillRect(kit.R(r.Min.X, y, r.Max.X, y+w), line)
}
