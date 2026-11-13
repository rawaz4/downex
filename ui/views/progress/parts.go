package progress

import (
	"math"
	"sync"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/views"
)

// The fonts of ProgressWindow.svelte's rules. A rule with the font shorthand resets the
// body's tabular figures; the others inherit them.
var (
	// fontCell is .pw's text: var(--type-cell) at 400 / 1.4.
	fontCell = views.FontCell
	// fontCell500 and fontCell600 are the same at 500 (.toggle, .setting .label) and 600
	// (.state, .done-head h1).
	fontCell500 = withWeight(views.FontCell, 500)
	fontCell600 = withWeight(views.FontCell, 600)
	// fontSmall is var(--type-small) (.banner, .fail-hint, .hint, .error-text, the table).
	fontSmall    = views.FontHint
	fontSmall600 = withWeight(views.FontHint, 600)
	// fontName is .name: 650 var(--type-lead) / 1.3 var(--font-display), -0.005em.
	fontName = kit.Font{Family: tokens.FontDisplay, Size: tokens.TypeLead, Weight: 650, LineHeight: 1.3, LetterSpacing: -0.005, AutoOptical: true}
	// fontFigure is .figure: 650 var(--type-figure) / 1 var(--font-display), -0.02em.
	fontFigure = kit.Font{Family: tokens.FontDisplay, Size: tokens.TypeFigure, Weight: 650, LineHeight: 1, LetterSpacing: -0.02, AutoOptical: true}
	// fontSection is .message h1 and .done-name: 650 var(--type-section) / 1.3, -0.01em.
	fontSection = views.SectionFont(1.3)
)

func withWeight(f kit.Font, w float32) kit.Font {
	f.Weight = w
	return f
}

// iconSets are the tiles' and marks' icon sets, one per backend's resources (the widgets keep
// their own for the line icons).
var iconSets struct {
	sync.Mutex
	m map[kit.Resources]*icons.Set
}

func iconSet(w *kit.Window) *icons.Set {
	if w == nil || w.Host() == nil {
		return nil
	}
	res := w.Host().Resources()
	iconSets.Lock()
	defer iconSets.Unlock()
	if iconSets.m == nil {
		iconSets.m = map[kit.Resources]*icons.Set{}
	}
	s := iconSets.m[res]
	if s == nil {
		s = icons.NewSet(res)
		iconSets.m[res] = s
	}
	return s
}

// ReleaseIcons releases the paths this package made with res (call it before res closes).
func ReleaseIcons(res kit.Resources) {
	iconSets.Lock()
	defer iconSets.Unlock()
	if s := iconSets.m[res]; s != nil {
		s.Release()
		delete(iconSets.m, res)
	}
}

// snapPoint is p on the device's pixel grid (an <svg>'s box).
func snapPoint(p *kit.PaintContext, at kit.Point) kit.Point {
	return kit.Pt(p.Scale.Snap(at.X), p.Scale.Snap(at.Y))
}

// ---- the file tile (FileIcon.svelte) ---------------------------------------------------------

// tile is FileIcon: the file kind's tile at its size, decorative.
type tile struct {
	kit.Element
	kind icons.TileKind
	size kit.DIP
}

func newTile(size kit.DIP) *tile {
	t := &tile{size: size, kind: icons.TileOther}
	t.Style = kit.Style{Width: kit.Px(size), Height: kit.Px(size), Flex: kit.FlexNone}
	t.Semantics.Hidden = true
	return t
}

// tileOf is the tile of a file kind (FileIcon's TILE_KINDS; anything else is "other").
func tileOf(k categories.FileKind) icons.TileKind {
	switch k {
	case categories.Video:
		return icons.TileVideo
	case categories.Audio:
		return icons.TileAudio
	case categories.Archives:
		return icons.TileArchives
	case categories.Documents:
		return icons.TileDocuments
	case categories.Programs:
		return icons.TilePrograms
	}
	return icons.TileOther
}

func (t *tile) set(k icons.TileKind) {
	if k != t.kind {
		t.kind = k
		t.Invalidate()
	}
}

func (t *tile) Paint(p *kit.PaintContext) {
	if s := iconSet(t.Window()); s != nil {
		s.DrawTile(p.Canvas, t.kind, snapPoint(p, p.Origin), t.size, views.TokensOf(&t.Element))
	}
}

// ---- the state marks ---------------------------------------------------------------------

// markKind is what a state mark shows: the main window's StatusMark for the shared states, or
// one of the transient connection glyphs.
type markKind uint8

const (
	markStatus     markKind = iota // StatusMark of status
	markConnecting                 // connecting, requesting, probing: a turning arc
	markRetrying                   // retrying: the circular arrow
)

// stateMark is the stateMark snippet at 12 px.
type stateMark struct {
	kit.Element
	kind   markKind
	status icons.MarkKind
	deg    float32
	spin   *spinner
	paths  struct {
		res          kit.Resources
		arc, retry   kit.Path
		failedBefore bool
	}
}

func newStateMark() *stateMark {
	m := &stateMark{}
	m.Style = kit.Style{Width: kit.Px(markSize), Height: kit.Px(markSize), Flex: kit.FlexNone}
	m.Semantics.Hidden = true
	return m
}

// markOf is the mark of a download state or a connection's state name ("receiving", "error",
// "done", "idle", "unspecified": markOfAny).
func markOf(name string) (markKind, icons.MarkKind) {
	switch name {
	case "probing", "connecting", "requesting":
		return markConnecting, 0
	case "retrying":
		return markRetrying, 0
	case "receiving":
		return markStatus, icons.MarkDownloading
	case "error":
		return markStatus, icons.MarkFailed
	case "done":
		return markStatus, icons.MarkCompleted
	case "idle", "unspecified":
		return markStatus, icons.MarkCanceled
	}
	return markStatus, icons.MarkOf(name)
}

func (m *stateMark) set(name string) {
	k, st := markOf(name)
	if k == m.kind && st == m.status {
		return
	}
	m.kind, m.status = k, st
	m.syncSpin()
	m.Invalidate()
}

// syncSpin turns the arc (1 s a turn, linear) while it shows, unless motion is reduced.
func (m *stateMark) syncSpin() {
	w := m.Window()
	on := w != nil && m.kind == markConnecting && w.MotionOK()
	switch {
	case on && m.spin == nil:
		m.spin = &spinner{m: m}
		w.Animate(m.spin)
	case !on && m.spin != nil:
		m.spin.dead = true
		m.spin = nil
		m.deg = 0
	}
}

func (m *stateMark) Attached() { m.Window().Defer(m.syncSpin) }

func (m *stateMark) Detached() {
	if m.spin != nil {
		m.spin.dead = true
		m.spin = nil
	}
	if m.paths.arc != nil {
		m.paths.arc.Release()
	}
	if m.paths.retry != nil {
		m.paths.retry.Release()
	}
	m.paths.arc, m.paths.retry, m.paths.res = nil, nil, nil
}

type spinner struct {
	m     *stateMark
	start time.Time
	dead  bool
}

func (s *spinner) Step(now time.Time) bool {
	if s.dead {
		return false
	}
	if s.start.IsZero() {
		s.start = now
	}
	f := math.Mod(float64(now.Sub(s.start))/float64(time.Second), 1)
	s.m.deg = float32(f * 360)
	s.m.Invalidate()
	return true
}

func (s *spinner) Finish() { s.dead = true }

// The transient glyphs' paths (ProgressWindow.svelte's svg.mark, on a 12 × 12 grid).
const (
	arcPath   = "M6 1.75a4.25 4.25 0 1 1-4.25 4.25"
	retryPath = "M9.9 6.1A3.9 3.9 0 1 1 8.7 3.2M9 1.5v2.25H6.75"
)

func (m *stateMark) ensurePaths(res kit.Resources) bool {
	if m.paths.res == res && m.paths.arc != nil {
		return true
	}
	m.Detached()
	arc, err := res.NewSVGPath(arcPath, kit.FillNonZero)
	if err != nil {
		return false
	}
	retry, err := res.NewSVGPath(retryPath, kit.FillNonZero)
	if err != nil {
		arc.Release()
		return false
	}
	m.paths.res, m.paths.arc, m.paths.retry = res, arc, retry
	m.syncSpin()
	return true
}

// markStroke is .mark: 1.5 px, round caps and joins.
var markStroke = kit.Stroke{Width: 1.5, Cap: kit.CapRound, Join: kit.JoinRound}

func (m *stateMark) Paint(p *kit.PaintContext) {
	w := m.Window()
	if w == nil || w.Host() == nil {
		return
	}
	t := views.TokensOf(&m.Element)
	at := snapPoint(p, p.Origin)
	if m.kind == markStatus {
		if s := iconSet(w); s != nil {
			s.DrawMark(p.Canvas, m.status, at, 12, t)
		}
		return
	}
	if !m.ensurePaths(w.Host().Resources()) {
		return
	}
	if m.kind == markRetrying {
		p.Canvas.PushTransform(kit.Translation(at.X, at.Y))
		p.Canvas.StrokePath(m.paths.retry, markStroke, t.Warn)
		p.Canvas.Pop()
		return
	}
	// The arc turns about the glyph's centre (transform-origin 6px 6px).
	p.Canvas.PushTransform(kit.Translation(-6, -6).Then(kit.Rotation(m.deg)).Then(kit.Translation(at.X+6, at.Y+6)))
	p.Canvas.StrokePath(m.paths.arc, markStroke, t.Muted)
	p.Canvas.Pop()
}

// ---- small pieces --------------------------------------------------------------------------

// box is a plain element of a style.
func box(st kit.Style, children ...kit.Widget) *kit.Element {
	e := &kit.Element{Style: st}
	e.Add(children...)
	return e
}

// spacer is .spacer: flex 1.
func spacer() *kit.Element { return box(kit.Style{Flex: kit.Flex1}) }

// copyButton is .icon-btn.sm.copy: a 22 px icon button with the copy icon at 14 px, the
// check for a moment after it copied.
type copyButton struct {
	*widgets.IconButton
	icon *widgets.Icon
}

func newCopyButton(name string, onPress func()) *copyButton {
	b := &copyButton{IconButton: widgets.NewIconButton("copy", 14, name, true, onPress)}
	b.Tooltip = name
	b.Style.Width, b.Style.Height = kit.Px(22), kit.Px(22)
	b.Style.Margin = kit.Edges{Top: kit.Px(-3), Bottom: kit.Px(-3)}
	for _, c := range b.Children() {
		if ic, ok := c.Widget().(*widgets.Icon); ok {
			b.icon = ic
		}
	}
	return b
}

func (b *copyButton) setCopied(on bool) {
	if b.icon == nil {
		return
	}
	n := icons.Copy
	if on {
		n = icons.Check
	}
	b.icon.SetName(n)
}

// address is the address snippet: the address in mono --ink-2, its last 24 characters always
// in view (the head ends in an ellipsis), and its copy button.
type address struct {
	kit.Element
	head, tail *views.Text
	url        string
}

func newAddress() *address {
	a := &address{}
	a.Style = kit.Style{Display: kit.DisplayFlex, MinWidth: kit.Px(0)}
	a.head = views.NewLine("", views.FontMono, views.InkInk2)
	a.head.SetOptions(kit.TextOptions{Truncate: kit.TruncateEnd, Direction: kit.DirLTR})
	a.head.Style.Overflow = kit.OverflowClipInline
	a.tail = views.NewText("", views.FontMono, views.InkInk2, kit.TextOptions{Direction: kit.DirLTR})
	a.tail.Style.Flex = kit.FlexNone
	a.head.Semantics.Role, a.tail.Semantics.Role = kit.RoleNone, kit.RoleNone
	a.Semantics.Role = kit.RoleText
	a.Add(a.head, a.tail)
	return a
}

// set shows url: urlTail = max(0, url.length - 24) in UTF-16 units.
func (a *address) set(url string) {
	if url == a.url {
		return
	}
	a.url = url
	cut := utf16Cut(url, 24)
	a.head.SetText(escapeBidi(url[:cut]))
	a.tail.SetText(escapeBidi(url[cut:]))
	a.Tooltip = escapeBidi(url)
	a.Semantics.Name = escapeBidi(url)
	a.SemanticsChanged()
}

// utf16Cut is the byte offset in s where its last n UTF-16 code units begin (JavaScript's
// s.slice(0, max(0, s.length - n))), moved to a whole character.
func utf16Cut(s string, n int) int {
	units := 0
	for _, r := range s {
		units++
		if r >= 0x10000 {
			units++
		}
	}
	keep := units - n
	if keep <= 0 {
		return 0
	}
	u := 0
	for i, r := range s {
		if u >= keep {
			return i
		}
		u++
		if r >= 0x10000 {
			u++
		}
	}
	return len(s)
}

// tone is a state line's colour (toneOf): live Mint, paused amber, failed red, awaiting blue,
// else quiet.
func toneInk(s downexv1.DownloadState) views.Ink {
	switch s {
	case downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING, downexv1.DownloadState_DOWNLOAD_STATE_PROBING,
		downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED:
		return views.InkAccent
	case downexv1.DownloadState_DOWNLOAD_STATE_PAUSED:
		return views.InkWarn
	case downexv1.DownloadState_DOWNLOAD_STATE_FAILED:
		return views.InkDanger
	case downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION:
		return views.InkInfo
	}
	return views.InkMuted
}

// stateName is a download state's api.ts name ("downloading", "awaiting_confirmation").
func stateName(s downexv1.DownloadState) string {
	switch s {
	case downexv1.DownloadState_DOWNLOAD_STATE_QUEUED:
		return "queued"
	case downexv1.DownloadState_DOWNLOAD_STATE_PROBING:
		return "probing"
	case downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING:
		return "downloading"
	case downexv1.DownloadState_DOWNLOAD_STATE_PAUSED:
		return "paused"
	case downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED:
		return "completed"
	case downexv1.DownloadState_DOWNLOAD_STATE_FAILED:
		return "failed"
	case downexv1.DownloadState_DOWNLOAD_STATE_CANCELED:
		return "canceled"
	case downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION:
		return "awaiting_confirmation"
	}
	return "unspecified"
}

// connStateName is a connection state's api.ts name.
func connStateName(s downexv1.ConnectionState) string {
	switch s {
	case downexv1.ConnectionState_CONNECTION_STATE_CONNECTING:
		return "connecting"
	case downexv1.ConnectionState_CONNECTION_STATE_REQUESTING:
		return "requesting"
	case downexv1.ConnectionState_CONNECTION_STATE_RECEIVING:
		return "receiving"
	case downexv1.ConnectionState_CONNECTION_STATE_RETRYING:
		return "retrying"
	case downexv1.ConnectionState_CONNECTION_STATE_DONE:
		return "done"
	case downexv1.ConnectionState_CONNECTION_STATE_ERROR:
		return "error"
	case downexv1.ConnectionState_CONNECTION_STATE_IDLE:
		return "idle"
	}
	return "unspecified"
}

// barState is the state a bar's colour follows.
func barState(s downexv1.DownloadState) widgets.BarState {
	switch s {
	case downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING:
		return widgets.BarDownloading
	case downexv1.DownloadState_DOWNLOAD_STATE_PROBING:
		return widgets.BarProbing
	case downexv1.DownloadState_DOWNLOAD_STATE_PAUSED:
		return widgets.BarPaused
	case downexv1.DownloadState_DOWNLOAD_STATE_FAILED:
		return widgets.BarFailed
	case downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED:
		return widgets.BarCompleted
	case downexv1.DownloadState_DOWNLOAD_STATE_CANCELED:
		return widgets.BarCanceled
	}
	return widgets.BarQueued
}

// ghostToggle is .btn.ghost.details-toggle: padding-inline 6 px 9 px, --muted, with the
// chevron that turns over (rotate, var(--dur-3) var(--ease-out)) while the details show.
// The text is its own label in --muted (--faint disabled): the button's label, which the
// button colours in every paint, stays hidden, holding the same text.
type ghostToggle struct {
	*widgets.Button
	chev  *chevron
	label *kit.Label
}

func newGhostToggle(onPress func()) *ghostToggle {
	const text = "Show details"
	g := &ghostToggle{Button: widgets.NewButton(text, "", widgets.Ghost, widgets.SizeMd, onPress)}
	g.Style.Padding = kit.Edges{Start: kit.Px(6), End: kit.Px(9)}
	g.chev = newChevron()
	g.Insert(0, g.chev)
	own := g.Button.Label()
	own.SetHidden(true)
	g.label = kit.NewLabel(text, widgets.FontButton, kit.TextOptions{Truncate: kit.TruncateEnd})
	g.label.Semantics.Role = kit.RoleNone
	g.label.Style = own.Style
	g.label.Style.Hidden = false
	g.Add(g.label)
	return g
}

// SetText changes the text (the hidden label's too, which names the button).
func (g *ghostToggle) SetText(s string) {
	g.Button.Label().SetText(s)
	g.label.SetText(s)
}

// Paint is the button's, with the text in --muted (--faint while disabled).
func (g *ghostToggle) Paint(p *kit.PaintContext) {
	g.Button.Paint(p)
	t := views.TokensOf(&g.Element)
	ink := t.Muted
	if g.Disabled {
		ink = t.Faint
	}
	g.label.SetColor(ink)
}

// chevron is the toggle's chevron-down icon at 14 px, turned 180° while up.
type chevron struct {
	kit.Element
	up    bool
	deg   float32
	tween *kit.Tween
}

func newChevron() *chevron {
	c := &chevron{}
	c.Style = kit.Style{Width: kit.Px(14), Height: kit.Px(14), Flex: kit.FlexNone}
	c.Semantics.Hidden = true
	c.PassThrough = true
	return c
}

func (c *chevron) setUp(up bool) {
	if up == c.up {
		return
	}
	c.up = up
	to := float32(0)
	if up {
		to = 180
	}
	w := c.Window()
	if c.tween != nil && w != nil {
		w.StopAnimation(c.tween)
	}
	if w == nil || !w.MotionOK() {
		c.deg = to
		c.Invalidate()
		return
	}
	c.tween = &kit.Tween{From: c.deg, To: to, Duration: tokens.Dur3, Ease: tokens.EaseOut.Easing(), Set: func(v float32) {
		c.deg = v
		c.Invalidate()
	}}
	w.Animate(c.tween)
}

func (c *chevron) Paint(p *kit.PaintContext) {
	w := c.Window()
	if w == nil || w.Host() == nil {
		return
	}
	s := iconSet(w)
	if s == nil {
		return
	}
	at := snapPoint(p, p.Origin)
	col := views.TokensOf(&c.Element).Muted
	if c.deg == 0 {
		s.DrawIcon(p.Canvas, icons.ChevronDown, at, 14, col)
		return
	}
	p.Canvas.PushTransform(kit.Translation(-7, -7).Then(kit.Rotation(c.deg)).Then(kit.Translation(at.X+7, at.Y+7)))
	s.DrawIcon(p.Canvas, icons.ChevronDown, kit.Point{}, 14, col)
	p.Canvas.Pop()
}

// labelledSwitch is label.toggle: a switch and its text (10 px apart, --ink 500), the whole
// label toggling it as a click on a <label> does.
type labelledSwitch struct {
	kit.Element
	sw       *widgets.Switch
	text     *views.Text
	onChange func(bool)
	pressed  bool
}

func newLabelledSwitch(text string, onChange func(bool)) *labelledSwitch {
	l := &labelledSwitch{onChange: onChange}
	l.sw = widgets.NewSwitch(false, onChange)
	l.sw.Semantics.Name = text
	l.text = views.NewText(text, fontCell500, views.InkInk, kit.TextOptions{Wrap: kit.TextWrapWords})
	l.text.Semantics.Role = kit.RoleNone
	// an inline-flex label: it shrinks beside the limit's fields, its text wrapping. (The
	// minimum is 0 rather than the longest word: ui/win reports a wrapping text's min-content
	// as its whole width; the fields beside it never leave less than a word's room.)
	l.text.Style.MinWidth = kit.Px(0)
	l.Style = kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: tokens.Sp5, MinWidth: kit.Px(0)}
	l.Cursor = kit.CursorPointer
	l.Add(l.sw, l.text)
	return l
}

func (l *labelledSwitch) setDisabled(d bool) {
	if d == l.sw.Disabled {
		return
	}
	l.sw.Disabled = d
	if d && l.sw.Focused() {
		if w := l.Window(); w != nil {
			w.Blur()
		}
	}
	l.sw.Invalidate()
	l.sw.SemanticsChanged()
}

// HandlePointer: a press on the text (released inside) toggles the switch and focuses it.
func (l *labelledSwitch) HandlePointer(in *kit.PointerInput) bool {
	if in.Target == &l.sw.Element {
		return false
	}
	inside := kit.RectAt(kit.Point{}, l.Size()).Contains(in.Local)
	switch in.Action {
	case kit.PointerDown:
		if in.Button != kit.ButtonLeft || l.sw.Disabled {
			return false
		}
		l.pressed = true
		l.Window().Capture(&l.Element)
		return true
	case kit.PointerUp:
		if !l.pressed || in.Button != kit.ButtonLeft {
			return false
		}
		l.pressed = false
		if inside && !l.sw.Disabled {
			l.Window().Focus(l.sw)
			l.sw.SetOn(!l.sw.On)
			if l.onChange != nil {
				l.onChange(l.sw.On)
			}
		}
		return true
	case kit.PointerCancel:
		l.pressed = false
	}
	return false
}

// field is a widgets.Input that hears it lost the focus (onblur).
type field struct {
	*widgets.Input
	onBlur func()
}

func newField(o widgets.InputOptions, onChange func(string), onEnter func(), onBlur func()) *field {
	f := &field{Input: widgets.NewInput("", o, onChange), onBlur: onBlur}
	f.OnEnter = func() bool {
		onEnter()
		return true
	}
	return f
}

// FocusChanged is the input's, then onblur.
func (f *field) FocusChanged(focused bool) {
	f.Input.FocusChanged(focused)
	if !focused && f.onBlur != nil {
		f.onBlur()
	}
}

// note is app.css's .note as CSS paints it: the ground under the whole border box, then the
// 1 px border over it (views.Note paints the ground inside the border over a full border-colour
// box, which shows through the dark theme's translucent grounds).
type note struct {
	*views.Note
}

func newNote(tone views.NoteTone, s string) *note { return &note{Note: views.NewNote(tone, s)} }

func noteColors(t *tokens.Theme, tone views.NoteTone) (ground, border kit.Color) {
	switch tone {
	case views.NoteInfo:
		return t.InfoSoft, t.InfoLine
	case views.NoteWarn:
		return t.WarnSoft, t.WarnLine
	case views.NoteFail:
		return t.DangerSoft, t.DangerLine
	}
	return t.Surface2, t.Line
}

// Paint is kit.Painter's.
func (n *note) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&n.Element)
	ground, border := noteColors(t, n.Tone)
	if t.IsForced() {
		border = t.Ink
	}
	r := views.SnapBox(p, 8)
	p.Canvas.FillRoundedRect(r, ground)
	w := p.Scale.SnapWidth(1)
	p.Canvas.StrokeRoundedRect(r.Inset(w/2), kit.Stroke{Width: w}, border)
}
