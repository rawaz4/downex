package progress

import (
	"context"
	"math"
	"regexp"
	"slices"
	"strconv"
	"time"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/display"
	"downex/ui/core/format"
	"downex/ui/core/media"
	"downex/ui/core/progress"
	"downex/ui/core/segments"
	"downex/ui/core/text"
	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/views"
)

// The window's texts (ProgressWindow.svelte).
const (
	offlineText    = "downexd is not reachable. This is the last known state; Downex keeps trying."
	goneTitle      = "This download no longer exists"
	goneText       = "It was removed from Downex."
	unreadTitle    = "Could not read this download"
	loadingText    = "Loading…"
	detailsTitle   = "Start positions and progress by connection"
	cancelTitle    = "Pauses the download and closes this window"
	countedText    = "Counted when it starts"
	mergingText    = "Merging the video and its sound into one MP4 file; no connections."
	assemblingText = "Assembling the file; no connections."
	noConnsYet     = "No connections yet."
	noConnsStopped = "No connections while the download is not running."
)

type copiedWhat uint8

const (
	copiedNone copiedWhat = iota
	copiedAddress
	copiedPath
)

const (
	tabStatus = "status"
	tabLimits = "limits"
)

// Screen is the window's content (div.pw): a column 10 px apart in 12 × 16 px padding on
// --surface, in var(--type-cell). Its methods belong to the UI thread.
type Screen struct {
	kit.Element
	pw    *Window
	app   *views.App
	store *state.ProgressStore

	// the component's state
	tab           string
	expanded      bool
	complete      bool
	busy          bool
	actionError   string
	copied        copiedWhat
	copiedTimer   *kit.Timer
	openPrompt    string // "", "open" or "with"
	dontShowAgain bool
	prefError     string
	closing       bool
	lastTitle     string
	sized         bool
	deciding      bool
	decidedOnce   bool
	destroyed     bool
	lim           limits
	fit           fitState
	// the retry countdown: the clock it reads, when each slot's notice was first seen
	now       time.Time
	retrySeen map[int32]retryNotice
	countdown *kit.Timer
	// what the effects last saw
	seen seenState

	// the views
	banner    *note
	message   *messageView
	done      *completeView
	head      *header
	bar       *barBox
	tabsBox   *kit.Element
	tabs      *widgets.Tabs
	limitsDot *kit.Element
	panel     *widgets.ScrollPane
	panelBody *kit.Element
	statusV   *statusView
	limitsV   *limitsView
	actErr    *views.Text
	actions   *actionsRow
	details   *detailsView
	kids      []kit.Widget
}

type retryNotice struct {
	detail string
	at     time.Time
}

// seenState is what the effects that follow a value last saw.
type seenState struct {
	actionError, prefError string
	dontShow, offline      bool
	tab                    string
	failed                 bool
	init                   bool
}

func newScreen(pw *Window) *Screen {
	s := &Screen{pw: pw, app: pw.App, store: pw.Store, tab: tabStatus, retrySeen: map[int32]retryNotice{}}
	s.lim.unit = format.KBps
	if pw.o.Storage != nil {
		s.expanded = state.ProgressDetails(pw.o.Storage)
	}
	s.Style = kit.Style{
		Display: kit.DisplayFlex, Axis: kit.AxisColumn, RowGap: kit.Px(10), Width: kit.Pct(100), Height: kit.Pct(100),
		Padding: kit.EdgesOf(kit.Px(12), tokens.Sp7), Overflow: kit.OverflowHidden,
	}
	s.build()
	s.render()
	return s
}

// Paint is the page's --surface ground.
func (s *Screen) Paint(p *kit.PaintContext) {
	p.Canvas.FillRect(p.Scale.SnapRect(kit.RectAt(p.Origin, p.Size())), views.TokensOf(&s.Element).Surface)
}

func escapeBidi(s string) string { return text.EscapeBidi(s) }

// ---- building -----------------------------------------------------------------------------

func (s *Screen) build() {
	// the offline banner: p.banner.note.warn role=status
	icon := widgets.NewIcon(icons.Alert, 16, func(t *tokens.Theme) kit.Color { return t.Warn })
	bt := views.NewText(offlineText, fontSmall, views.InkWarn, kit.TextOptions{Wrap: kit.TextWrapWords})
	bt.Style.MinWidth = kit.Px(0)
	bt.Style.Flex = kit.FlexOf(0, 1, kit.Length{})
	s.banner = newNote(views.NoteWarn, "")
	s.banner.Add(icon, bt)
	s.banner.Style.Display, s.banner.Style.AlignItems, s.banner.Style.ColumnGap = kit.DisplayFlex, kit.AlignCenter, tokens.Sp4
	s.banner.Style.Padding = kit.EdgesOf(kit.Px(5), kit.Px(11))
	s.banner.Style.Flex = kit.FlexNone
	s.banner.Semantics.Role, s.banner.Semantics.Live, s.banner.Semantics.Name = kit.RoleStatus, kit.LivePolite, offlineText

	s.message = newMessageView(s)
	s.done = newCompleteView(s)
	s.head = newHeader()
	s.bar = newBarBox()

	s.statusV = newStatusView(s)
	s.limitsV = newLimitsView(s)
	s.panelBody = box(kit.Style{Display: kit.DisplayBlock, MinWidth: kit.Px(0)}, s.statusV)
	s.panel = widgets.NewScrollPane(s.panelBody)
	s.panel.Style.Flex = kit.FlexOf(1, 1, kit.Length{})
	s.tabs = widgets.NewTabs([]widgets.TabOption{
		{Value: tabStatus, Label: "Status"},
		{Value: tabLimits, Label: "Speed limit", Dot: true},
	}, tabStatus, widgets.TabsOptions{AriaLabel: "Download progress", Panel: s.panel, IDPrefix: "pw", Small: true, Flush: true}, s.onTab)
	if t := s.tabs.TabWidget(tabLimits); t != nil {
		if kids := t.Base().Children(); len(kids) > 0 {
			s.limitsDot = kids[len(kids)-1]
		}
	}
	s.tabsBox = box(kit.Style{Flex: kit.FlexNone, MinWidth: kit.Px(0)}, s.tabs)
	s.actErr = views.NewText("", fontSmall, views.InkDanger, kit.TextOptions{Wrap: kit.TextWrapWords})
	s.actErr.Style.Flex = kit.FlexNone
	s.actErr.Semantics.Role, s.actErr.Semantics.Live = kit.RoleAlert, kit.LiveAssertive
	s.actions = newActionsRow(s)
	s.details = newDetailsView()
}

// ---- the views of the progress -------------------------------------------------------------

// header is header.head: the tile, the name over the state, the figure.
type header struct {
	kit.Element
	tile   *tile
	name   *views.Text
	state  *kit.Element
	mark   *stateMark
	status *views.Text
	figure *views.Text
	// inked is the state the status and the figure were coloured for (their ink is a
	// function, which a repaint follows only when asked).
	inked    downexv1.DownloadState
	inkedSet bool
}

func newHeader() *header {
	h := &header{}
	h.Style = kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: tokens.Sp6, MinWidth: kit.Px(0),
		MinHeight: kit.Px(44), Flex: kit.FlexNone}
	h.tile = newTile(32)
	h.name = &views.Text{Label: kit.NewFileName("", fontName, kit.TextOptions{Truncate: kit.TruncateEnd}), Ink: views.InkInk}
	h.name.Style.MinWidth = kit.Px(0)
	h.name.Style.Overflow = kit.OverflowClipInline
	h.mark = newStateMark()
	h.status = views.NewText("", fontCell600, views.InkMuted, kit.TextOptions{Truncate: kit.TruncateEnd})
	h.status.Style.MinWidth = kit.Px(0)
	h.status.Style.Flex = kit.FlexOf(0, 1, kit.Length{})
	h.state = box(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(6), MinWidth: kit.Px(0)}, h.mark, h.status)
	titles := box(kit.Style{Display: kit.DisplayBlock, RowGap: kit.Px(3), Flex: kit.FlexOf(1, 1, kit.Length{}), MinWidth: kit.Px(0)}, h.name, h.state)
	h.figure = views.NewText("", fontFigure, views.InkInk, kit.TextOptions{})
	h.figure.Style.Flex = kit.FlexNone
	h.Add(h.tile, titles, h.figure)
	return h
}

func (h *header) set(d *downexv1.Download) {
	h.tile.set(tileOf(categories.KindOf(d)))
	name := categories.DisplayName(d)
	h.name.SetText(name)
	h.name.Tooltip = name
	st := d.GetState()
	h.mark.set(stateName(st))
	status := escapeBidi(progress.Status(d))
	h.status.SetText(status)
	h.state.Tooltip = status
	h.status.Ink = toneInk(st)
	if st == downexv1.DownloadState_DOWNLOAD_STATE_FAILED {
		// a failure's reason on at most two lines
		h.status.SetOptions(kit.TextOptions{Wrap: kit.TextWrapAnywhere, MaxLines: 2, Truncate: kit.TruncateEnd})
	} else {
		h.status.SetOptions(kit.TextOptions{Truncate: kit.TruncateEnd})
	}
	pct := progress.HeaderPercent(d)
	h.figure.SetText(pct)
	h.figure.SetHidden(pct == "")
	switch st {
	case downexv1.DownloadState_DOWNLOAD_STATE_PAUSED:
		h.figure.Ink = views.InkWarn
	case downexv1.DownloadState_DOWNLOAD_STATE_FAILED:
		h.figure.Ink = views.InkDanger
	default:
		h.figure.Ink = views.InkInk
	}
	if !h.inkedSet || st != h.inked {
		h.inked, h.inkedSet = st, true
		h.status.Invalidate()
		h.figure.Invalidate()
	}
}

// barBox is div.bar role=progressbar around the lg SegmentBar.
type barBox struct {
	kit.Element
	seg   *segBar
	value string
}

func newBarBox() *barBox {
	b := &barBox{seg: newSegBar()}
	b.Style = kit.Style{Display: kit.DisplayFlex, Flex: kit.FlexNone}
	b.Semantics.Role = kit.RoleProgressBar
	b.Semantics.Name = "Download progress"
	b.Add(b.seg)
	return b
}

func (b *barBox) set(d *downexv1.Download) {
	b.seg.setDownload(d)
	f := format.ProgressFraction(d)
	if finite(f) {
		b.Semantics.Range = kit.RangeValue{Valid: true, Min: 0, Max: 100, Value: math.Floor(math.Min(1, math.Max(0, f)) * 100)}
		b.Semantics.Value = format.Percent(f)
	} else {
		b.Semantics.Range = kit.RangeValue{Min: 0, Max: 100}
		b.Semantics.Value = "Size unknown"
	}
	if v := b.Semantics.Value; v != b.value {
		b.value = v
		b.SemanticsChanged()
	}
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// segBar is SegmentBar lg: the cells barCells gives for its width.
type segBar struct {
	*widgets.SegmentBar
	d     *downexv1.Download
	width kit.DIP
	cells []segments.SegCell
	// shown is what the bar holds (SegmentBar keeps the slice it is given); next is the
	// cells of a new report, given to the bar only when they differ.
	shown, next []widgets.SegCell
	state       widgets.BarState
	set         bool
}

func newSegBar() *segBar { return &segBar{SegmentBar: widgets.NewSegmentBar(widgets.SegLg)} }

func (b *segBar) setDownload(d *downexv1.Download) {
	b.d = d
	b.refresh()
}

func (b *segBar) refresh() {
	if b.d == nil {
		return
	}
	b.cells = segments.AppendBarCells(b.cells[:0], b.d, float64(b.width), segments.MaxCells)
	b.next = b.next[:0]
	for _, c := range b.cells {
		b.next = append(b.next, widgets.SegCell{Fraction: c.Fraction, Live: c.Live, Indeterminate: c.Indeterminate})
	}
	st := barState(b.d.GetState())
	if b.set && st == b.state && slices.Equal(b.next, b.shown) {
		return
	}
	b.set, b.state = true, st
	b.shown, b.next = b.next, b.shown
	b.Set(b.shown, st)
}

// LayoutContent is the bar's, then the cells for a new width.
func (b *segBar) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	b.SegmentBar.LayoutContent(ctx, box)
	if w := box.Width(); w != b.width {
		b.width = w
		b.refresh()
	}
}

// statusView is the Status tabpanel: a failure's hint, then the facts.
type statusView struct {
	kit.Element
	hint                                  *note
	facts                                 *facts
	addr                                  *address
	copyA                                 *copyButton
	live                                  *kit.Element
	size, downloaded, rate, avg, left, at *views.Text
	pieces, resumable, quality, limit     *views.Text
	addrI, rateI, leftI, piecesI, qualI   int
	limitI                                int
	// rateInk is the rate's colour drawn (0 none yet, 1 live in --accent, 2 --ink).
	rateInk int8
}

func liveRate(live bool) int8 {
	if live {
		return 1
	}
	return 2
}

func newStatusView(s *Screen) *statusView {
	v := &statusView{}
	v.Semantics.Role = kit.RoleTabPanel
	v.Focus = kit.FocusClick // tabindex="-1"
	v.hint = newNote(views.NoteFail, " ")
	v.hint.Style.Margin = kit.Edges{Bottom: tokens.Sp4}
	v.hint.Style.Padding = kit.EdgesOf(kit.Px(5), kit.Px(11))
	v.hint.Text.SetFont(fontSmall)
	v.facts = newFacts(4, 5)
	v.addr = newAddress()
	v.copyA = newCopyButton("Copy address", func() { s.copy(false) })
	v.addrI = v.facts.add("Address", box(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: tokens.Sp2, MinWidth: kit.Px(0)},
		v.addr, v.copyA), true)
	line := func(ink views.Ink, f kit.Font) *views.Text {
		t := views.NewLine("", f, ink)
		t.Semantics.Role = kit.RoleDefinition
		return t
	}
	v.size, v.downloaded = line(views.InkInk, fontCell), line(views.InkInk, fontCell)
	v.facts.add("File size", v.size, false)
	v.facts.add("Downloaded", v.downloaded, false)
	v.rate, v.avg = line(views.InkInk, fontCell), line(views.InkMuted, fontCell)
	v.rate.Style.Flex = kit.FlexOf(0, 1, kit.Length{})
	v.avg.Style.Flex = kit.FlexOf(0, 1000, kit.Length{})
	rateRow := box(kit.Style{Display: kit.DisplayFlex, MinWidth: kit.Px(0), Overflow: kit.OverflowHidden}, v.rate, v.avg)
	v.rateI = v.facts.add("Transfer rate", rateRow, false)
	v.left, v.at = line(views.InkInk, fontCell), line(views.InkMuted, fontCell)
	v.left.Style.Flex = kit.FlexOf(0, 1, kit.Length{})
	v.at.Style.Flex = kit.FlexOf(0, 1000, kit.Length{})
	leftRow := box(kit.Style{Display: kit.DisplayFlex, MinWidth: kit.Px(0), Overflow: kit.OverflowHidden}, v.left, v.at)
	v.leftI = v.facts.add("Time left", leftRow, false)
	v.pieces = line(views.InkInk, fontCell)
	v.piecesI = v.facts.add("Fragments", v.pieces, false)
	v.resumable = line(views.InkInk, fontCell)
	v.facts.add("Resumable", v.resumable, false)
	v.quality = line(views.InkInk, fontCell)
	v.qualI = v.facts.add("Quality", v.quality, true)
	v.limit = line(views.InkAccent, fontCell)
	v.limitI = v.facts.add("Speed limit", v.limit, false)
	v.live = liveRegion()
	v.facts.addLive(v.live)
	v.Style = kit.Style{Display: kit.DisplayBlock, MinWidth: kit.Px(0)}
	v.Add(v.hint, v.facts)
	return v
}

func (v *statusView) set(s *Screen, d *downexv1.Download) {
	failed := d.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_FAILED
	hint := s.failHint(d)
	v.hint.SetHidden(!(failed && hint != ""))
	if hint != "" {
		v.hint.Text.SetText(hint)
	}
	v.facts.setTerm(v.addrI, progress.AddressLabel(d))
	v.addr.set(d.GetUrl())
	v.copyA.setCopied(s.copied == copiedAddress)
	live := ""
	if s.copied == copiedAddress {
		live = "Address copied"
	}
	setLive(v.live, live)
	v.size.SetText(progress.SizeText(d))
	v.downloaded.SetText(progress.DownloadedText(d))
	v.facts.setShown(v.rateI, !failed)
	v.facts.setShown(v.leftI, !failed)
	rate, avg := progress.RateText(d), progress.AverageRateText(d)
	v.rate.SetText(rate)
	live2 := d.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING && d.GetSpeedBps() > 0
	if live2 {
		v.rate.Ink = views.InkAccent
		v.rate.Label.SetFont(fontCell600)
	} else {
		v.rate.Ink = views.InkInk
		v.rate.Label.SetFont(fontCell)
	}
	if r := liveRate(live2); r != v.rateInk {
		v.rateInk = r
		v.rate.Invalidate()
	}
	if avg != "" {
		v.avg.SetText(" · avg " + avg)
		v.rate.Parent().Tooltip = rate + " · avg " + avg
	} else {
		v.avg.SetText("")
		v.rate.Parent().Tooltip = ""
	}
	v.avg.SetHidden(avg == "")
	left, at := progress.TimeLeftText(d), progress.DoneAtText(d, float64(s.app.Clock.Now().UnixMilli()))
	v.left.SetText(left)
	if at != "" {
		v.at.SetText(" · Done ≈ " + at)
		v.left.Parent().Tooltip = left + " · Done ≈ " + at
	} else {
		v.at.SetText("")
		v.left.Parent().Tooltip = ""
	}
	v.at.SetHidden(at == "")
	media := d.GetKind() == downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA
	v.facts.setShown(v.piecesI, media)
	if media {
		_, many := progress.PieceNames(d)
		v.facts.setTerm(v.piecesI, many)
		if f := progress.FragmentsText(d); f != "" {
			v.pieces.SetText(f)
		} else {
			v.pieces.SetText(countedText)
		}
	}
	v.resumable.SetText(progress.ResumeSupport(d))
	q := progress.QualityText(d)
	v.facts.setShown(v.qualI, q != "")
	v.quality.SetText(escapeBidi(q))
	v.quality.Tooltip = escapeBidi(progress.QualityDetail(d))
	v.facts.setShown(v.limitI, d.GetSpeedLimit() > 0)
	if d.GetSpeedLimit() > 0 {
		v.limit.SetText(format.Speed(float64(d.GetSpeedLimit())) + " for this download")
	}
}

// actionsRow is div.actions: Show/Hide details, then Pause/Resume and Cancel/Close, or for a
// failure Retry/Start over and Close.
type actionsRow struct {
	kit.Element
	toggle                *ghostToggle
	pause, resume         *widgets.Button
	retry, restart, close *widgets.Button
	cancel                *widgets.Button
}

func newActionsRow(s *Screen) *actionsRow {
	a := &actionsRow{}
	a.Style = kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: tokens.Sp4, Flex: kit.FlexNone}
	a.toggle = newGhostToggle(s.toggleDetails)
	a.retry = widgets.NewButton("Retry", "resume", widgets.Primary, widgets.SizeMd, s.retry)
	a.restart = widgets.NewButton("Start over", "restart", widgets.Primary, widgets.SizeMd, s.retry)
	a.close = widgets.NewButton("Close", "", widgets.Secondary, widgets.SizeMd, s.closeWindow)
	a.pause = widgets.NewButton("Pause", "pause", widgets.Secondary, widgets.SizeMd, s.togglePause)
	a.resume = widgets.NewButton("Resume", "resume", widgets.Secondary, widgets.SizeMd, s.togglePause)
	a.cancel = widgets.NewButton("Cancel", "", widgets.Secondary, widgets.SizeMd, s.cancel)
	a.Add(a.toggle, spacer(), a.retry, a.restart, a.close, a.pause, a.resume, a.cancel)
	return a
}

// show shows b or hides it; a hidden button's focus goes to its stand-in (the same <button>
// today, whose label changes).
func showButton(w *kit.Window, b *widgets.Button, on bool, standIn *widgets.Button) {
	if !on && b.Focused() && standIn != nil && w != nil {
		standIn.SetHidden(false)
		w.Focus(standIn)
	}
	b.SetHidden(!on)
}

// detailsView is section.details: the caps title, the segment map (a stream's fragments bar),
// and the connections in their bordered scroll region.
type detailsView struct {
	kit.Element
	title *views.Text
	smap  *segmentMap
	frags *widgets.ProgressBar
	fragL string
	wrap  *tableWrap
	table *connTable
}

func newDetailsView() *detailsView {
	v := &detailsView{}
	v.Semantics.Role = kit.RoleRegion
	v.Semantics.Name = detailsTitle
	v.Style = kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, RowGap: tokens.Sp5, Flex: kit.FlexOf(1, 1, kit.Length{}),
		MinHeight: kit.Px(0), Padding: kit.Edges{Top: tokens.Sp6}, Border: kit.Edges{Top: kit.Px(1)}}
	v.title = views.NewCaps(detailsTitle)
	v.title.Semantics.Role, v.title.Semantics.Level = kit.RoleHeading, 2
	v.title.Style.Flex = kit.FlexNone
	v.smap = newSegmentMap(18, true)
	v.frags = widgets.NewProgressBar("Fragments downloaded", false)
	v.frags.Style.Flex = kit.FlexNone
	v.table = newConnTable()
	v.wrap = newTableWrap(v.table)
	v.Add(v.title, v.smap, v.frags, v.wrap)
	return v
}

// Paint is the section's top rule.
func (v *detailsView) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&v.Element)
	b := p.Scale.SnapRect(kit.RectAt(p.Origin, p.Size()))
	p.Canvas.FillRect(kit.R(b.Min.X, b.Min.Y, b.Max.X, b.Min.Y+p.Scale.SnapWidth(1)), t.Line)
}

// tableWrap is div.table-wrap: a 1 px --line box with --r-md corners on --surface that scrolls
// the table (its header stays at the top).
type tableWrap struct {
	kit.Element
	pane *widgets.ScrollPane
}

func newTableWrap(t *connTable) *tableWrap {
	w := &tableWrap{}
	w.pane = widgets.NewScrollPane(t)
	w.pane.Style.Flex = kit.FlexOf(1, 1, kit.Length{})
	w.pane.OnScroll = t.scrolled
	t.pane, t.wrap = w.pane, w
	// The box clips the table to a plain rectangle: a rounded clip is a layer, where the text
	// loses ClearType (the page's table text has it). Its corners are painted round instead:
	// the box itself (Paint), the sticky header's top corners and a hovered row's ground.
	w.Style = kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, Flex: kit.FlexOf(1, 1, kit.Length{}), MinHeight: kit.Px(0),
		Border: kit.EdgesAll(kit.Px(1)), Overflow: kit.OverflowHidden}
	w.Add(w.pane)
	return w
}

func (w *tableWrap) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&w.Element)
	r := views.SnapBox(p, wrapInner)
	p.Canvas.FillRoundedRect(r, t.Line)
	bw := p.Scale.SnapWidth(1)
	p.Canvas.FillRoundedRect(kit.RoundRect(r.Rect.Inset(kit.UniformInsets(bw)), wrapInner-bw), t.Surface)
}

// messageView is div.message: a download that does not exist, one that could not be read,
// or the first read in progress.
type messageView struct {
	kit.Element
	title   *views.Text
	text    *views.Text
	again   *widgets.Button
	loading *views.Text
	actions *kit.Element
}

func newMessageView(s *Screen) *messageView {
	v := &messageView{}
	v.Style = kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, Justify: kit.JustifyCenter, AlignItems: kit.AlignStart,
		RowGap: tokens.Sp5, Flex: kit.Flex1, MinHeight: kit.Px(0)}
	v.title = views.NewText("", fontSection, views.InkInk, kit.TextOptions{Wrap: kit.TextWrapWords})
	v.title.Semantics.Role, v.title.Semantics.Level = kit.RoleHeading, 1
	v.text = views.NewText("", fontCell, views.InkInk, kit.TextOptions{Wrap: kit.TextWrapWords})
	v.again = widgets.NewButton("Try again", "", widgets.Secondary, widgets.SizeMd, func() { s.app.Go(s.store.Refresh) })
	v.loading = views.NewText(loadingText, fontCell, views.InkMuted, kit.TextOptions{})
	v.loading.Semantics.Role, v.loading.Semantics.Live = kit.RoleStatus, kit.LivePolite
	closeB := widgets.NewButton("Close", "", widgets.Primary, widgets.SizeMd, s.closeWindow)
	v.actions = box(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: tokens.Sp4, Flex: kit.FlexNone}, closeB)
	v.Add(v.title, v.text, v.again, v.loading, v.actions)
	return v
}

func (v *messageView) set(missing bool, loadError string) {
	switch {
	case missing:
		v.title.SetText(goneTitle)
		v.text.SetText(goneText)
	case loadError != "":
		v.title.SetText(unreadTitle)
		v.text.SetText(escapeBidi(loadError))
	}
	msg := missing || loadError != ""
	v.title.SetHidden(!msg)
	v.text.SetHidden(!msg)
	v.again.SetHidden(missing || loadError == "")
	v.loading.SetHidden(msg)
	v.actions.SetHidden(!msg)
}

// ---- the reactive part (the script's $derived and $effect) ----------------------------------

func (s *Screen) d() *downexv1.Download { return s.store.Download() }

func (s *Screen) offline() bool { return !s.store.Connected() }

func (s *Screen) media(d *downexv1.Download) bool {
	return d.GetKind() == downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA
}

// failHint is the hint under a failure: a web page's own, else the shared one.
func (s *Screen) failHint(d *downexv1.Download) string {
	if media.IsSiteDownload(d) {
		if h := media.SiteErrorHint(d.GetErrorCode()); h != "" {
			return h
		}
	}
	return display.ErrorHint(d.GetErrorCode())
}

func (s *Screen) canPause(d *downexv1.Download) bool { return d != nil && state.IsActive(d.GetState()) }

func (s *Screen) canResume(d *downexv1.Download) bool {
	return d != nil && state.IsResumable(d.GetState()) &&
		!(d.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_FAILED && (progress.IsRefused(d) || views.NeedsRestart(d)))
}

// sync is what follows a change of the store: the effects, then the view.
func (s *Screen) sync() {
	if s.destroyed {
		return
	}
	d := s.d()
	ready := s.store.Ready()

	// The title bar: "<percent>% <file name>".
	title := "Downex"
	if d != nil {
		title = progress.Title(d)
	}
	if title != s.lastTitle {
		if h := s.pw.Kit.Host(); h != nil {
			s.lastTitle = title
			h.SetTitle(title)
		}
	}

	// The window opens collapsed; it shows the details at once when they were shown last time
	// and there is a running download to show them for. Collapsed, it fits its facts.
	if !s.sized && ready && d != nil {
		s.sized = true
		if d.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED {
			if s.expanded {
				s.resize(true)
			} else {
				s.queueFit(false)
			}
		}
	}

	// A download removed elsewhere takes its window along.
	if s.store.Removed() {
		s.closeWindow()
	}

	s.completion(d, ready)
	s.followLimits(d)
	s.syncCountdown(d)
	s.render()
}

// completion: the summary view (Settings.showCompleteDialog), or the window closes; the
// summary also shows when the settings are unknown. The setting may have changed since the
// window opened, so it is read again first; the last one read decides when that fails. A
// download that was complete when the window opened decides with the settings read with it.
func (s *Screen) completion(d *downexv1.Download, ready bool) {
	if d == nil || !ready {
		return
	}
	if d.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED {
		// Restarted (or reset elsewhere): back to its progress, at the size for it.
		if s.complete {
			s.complete = false
			s.resize(s.expanded)
		}
		return
	}
	if s.complete || s.deciding {
		return
	}
	first := !s.decidedOnce
	s.decidedOnce = true
	if fs, ok := s.store.FirstState(); first && ok && fs == downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED {
		if st := s.store.Settings(); st != nil && !st.GetShowCompleteDialog() {
			s.closeWindow()
		} else {
			s.showComplete()
		}
		return
	}
	s.deciding = true
	s.app.Go(func() {
		settings := s.store.LoadSettings()
		s.app.UI(func() {
			s.deciding = false
			// It may have been restarted (or removed) while the settings were read.
			cur := s.d()
			if s.destroyed || s.complete || cur.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED {
				return
			}
			if settings != nil && !settings.GetShowCompleteDialog() {
				s.closeWindow()
			} else {
				s.showComplete()
				s.render()
			}
		})
	})
}

func (s *Screen) showComplete() {
	s.complete = true
	s.openPrompt = ""
	// The details belong to the progress; the window is sized to the dialog.
	s.expanded = false
	s.render()
	s.done.badge.play()
	// Enter opens the file: the primary button has the focus.
	s.pw.Kit.AfterLayout(func() {
		if s.complete && s.openPrompt == "" {
			s.pw.Kit.SetFocusVisible(true)
			s.pw.Kit.Focus(s.done.open)
		}
	})
}

func (s *Screen) closeWindow() {
	if s.closing {
		return
	}
	s.closing = true
	s.pw.close()
}

func (s *Screen) windowFocused() {
	if s.store.Connected() {
		s.app.Go(func() { s.store.LoadSettings() })
	}
}

// windowKey: Enter opens the file and Esc closes the window (DESKTOP-SPEC §8); buttons keep
// their own Enter (they use it, so it never gets here).
func (s *Screen) windowKey(e kit.KeyEvent) bool {
	if !s.complete || s.openPrompt != "" || e.Action != kit.KeyDown {
		return false
	}
	if e.Key == kit.KeyEscape {
		s.closeWindow()
		return true
	}
	if e.Key != kit.KeyEnter || e.Mods&(kit.ModAlt|kit.ModCtrl|kit.ModMeta|kit.ModShift) != 0 {
		return false
	}
	if f := s.pw.Kit.Focused(); f != nil {
		switch f.Widget().(type) {
		case *widgets.Button, *widgets.IconButton, *copyButton, *widgets.Select, *field:
			return false
		}
		if _, ok := f.Widget().(*kit.TextField); ok {
			return false
		}
	}
	s.open()
	return true
}

func (s *Screen) environment(kit.Environment) {
	if s == nil {
		return
	}
	// The display's scaling may have changed: the frame and the rounding change, so it fits
	// again.
	s.fit.frameKnown = false
	s.queueFit(false)
}

func (s *Screen) dispose() {
	s.destroyed = true
	s.lim.speedTimer.Stop()
	s.lim.connTimer.Stop()
	s.copiedTimer.Stop()
	s.countdown.Stop()
}

// ---- actions -------------------------------------------------------------------------------

// act runs one action off the UI thread, one at a time (busy), showing its error; then hears
// whether it worked (false at once when another one runs).
func (s *Screen) act(fn func(ctx context.Context) error, then func(ok bool)) {
	if s.busy {
		if then != nil {
			then(false)
		}
		return
	}
	s.busy = true
	s.actionError = ""
	s.render()
	s.app.Go(func() {
		ctx, cancel := s.app.CallContext()
		err := fn(ctx)
		cancel()
		s.app.UI(func() {
			if err != nil {
				s.actionError = state.Message(err)
			}
			s.busy = false
			if then != nil {
				then(err == nil)
			}
			s.render()
		})
	})
}

func (s *Screen) togglePause() {
	d := s.d()
	if s.offline() || d == nil {
		return
	}
	switch {
	case s.canPause(d):
		s.act(s.store.Pause, nil)
	case s.canResume(d):
		s.act(func(ctx context.Context) error { return s.store.Resume(ctx, false) }, nil)
	}
}

// cancel pauses the download (it stays in the list and can be resumed) and closes.
func (s *Screen) cancel() {
	if d := s.d(); d != nil && state.IsActive(d.GetState()) {
		if s.offline() {
			return
		}
		s.act(s.store.Pause, func(ok bool) {
			if ok {
				s.closeWindow()
			}
		})
		return
	}
	s.closeWindow()
}

func (s *Screen) retry() {
	d := s.d()
	if s.offline() || d == nil {
		return
	}
	restart := views.NeedsRestart(d)
	s.act(func(ctx context.Context) error { return s.store.Resume(ctx, restart) }, nil)
}

// copy copies the address, or the full path of the saved file (as it is, not as shown).
func (s *Screen) copy(path bool) {
	d := s.d()
	if d == nil {
		return
	}
	text, what := d.GetUrl(), copiedAddress
	if path {
		text, what = display.RawJoinPath(d.GetDir(), categories.RawName(d)), copiedPath
	}
	plat := s.app.Platform
	s.app.Go(func() {
		ctx, cancel := s.app.CallContext()
		err := plat.CopyText(ctx, text)
		cancel()
		s.app.UI(func() {
			if err != nil {
				s.actionError = "Could not copy: " + state.Message(err)
			} else {
				s.copied = what
				s.copiedTimer.Stop()
				s.copiedTimer = s.pw.Kit.After(2*time.Second, func() {
					s.copied = copiedNone
					s.render()
				})
			}
			s.render()
		})
	})
}

func (s *Screen) toggleDetails() {
	s.expanded = !s.expanded
	if s.pw.o.Storage != nil {
		state.SaveProgressDetails(s.pw.o.Storage, s.expanded) // unavailable: the choice lasts for this window
	}
	s.render()
	s.resize(s.expanded)
}

func (s *Screen) onTab(v string) {
	s.tab = v
	s.render()
}

// needsPrompt: a program, or a name that hides its type, is confirmed before anything opens it.
func needsPrompt(d *downexv1.Download) bool {
	raw := categories.RawName(d)
	return categories.IsExecutableName(raw) || text.HasBidiControls(raw)
}

func (s *Screen) open() {
	d := s.d()
	if d == nil || s.busy {
		return
	}
	if needsPrompt(d) {
		s.ask("open")
		return
	}
	s.openFile()
}

func (s *Screen) openFile() {
	id := s.store.ID()
	s.act(func(ctx context.Context) error { return s.app.Platform.OpenFile(ctx, id) }, func(ok bool) {
		if ok {
			s.closeWindow()
		}
	})
}

// chooseApp is Open with…: the user picks the app (the system's dialog); the same checks first.
func (s *Screen) chooseApp() {
	d := s.d()
	if d == nil || s.busy || !s.pw.o.Caps.OpenWith {
		return
	}
	if needsPrompt(d) {
		s.ask("with")
		return
	}
	s.openWithApp()
}

func (s *Screen) openWithApp() {
	if s.busy {
		return
	}
	s.busy = true
	s.actionError = ""
	s.render()
	id := s.store.ID()
	s.app.Go(func() {
		ctx, cancel := s.app.CallContext()
		err := s.app.Platform.OpenWith(ctx, id)
		cancel()
		s.app.UI(func() {
			s.busy = false
			if err == nil {
				s.closeWindow()
			} else if e := state.ErrorOf(err); e.Code != connect.CodeCanceled {
				// Closing the system's dialog without a choice leaves this one as it was.
				s.actionError = e.Message
			}
			s.render()
		})
	})
}

func (s *Screen) openFolder() {
	id := s.store.ID()
	s.act(func(ctx context.Context) error { return s.app.Platform.OpenFolder(ctx, id) }, func(ok bool) {
		if ok {
			s.closeWindow()
		}
	})
}

// ask shows the question before a program is opened (OpenDialog).
func (s *Screen) ask(mode string) {
	d := s.d()
	prompt := s.pw.o.OpenPrompt
	if prompt == nil || d == nil {
		s.pw.o.Log.Warn("progress: no open prompt; a program is not opened without asking", "id", s.store.ID())
		return
	}
	s.openPrompt = mode
	raw := categories.RawName(d)
	q := OpenQuestion{
		ID: d.GetId(), Name: categories.DisplayName(d), Executable: categories.IsExecutableName(raw),
		HiddenControls: text.HasBidiControls(raw), WithApp: mode == "with",
		Confirm: func() {
			if mode == "with" {
				s.openWithApp()
			} else {
				s.openFile()
			}
		},
		Closed: func() {
			s.openPrompt = ""
			s.render()
		},
	}
	if m := prompt(s.app, q); m != nil {
		s.app.Dialogs.Show(m)
	} else {
		s.openPrompt = ""
	}
}

func (s *Screen) onDontShowAgain(checked bool) {
	s.dontShowAgain = checked
	s.prefError = ""
	s.render()
	s.app.Go(func() {
		ctx, cancel := s.app.CallContext()
		err := s.store.SetShowCompleteDialog(ctx, !checked)
		cancel()
		s.app.UI(func() {
			if err != nil {
				s.dontShowAgain = !checked
				s.prefError = "Could not save this choice: " + state.Message(err)
			}
			s.render()
		})
	})
}

// ---- the retry countdown --------------------------------------------------------------------

// retryWait is /^(.+?),\s*retrying in (\d+(?:\.\d+)?)\s*s$/i.
var retryWait = regexp.MustCompile(`(?i)^([^\n\r\x{2028}\x{2029}]+?),[\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]*retrying in (\d+(?:\.\d+)?)[\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]*s$`)

// syncCountdown ticks the clock every second while a shown connection waits to retry; it
// starts again with every report.
func (s *Screen) syncCountdown(d *downexv1.Download) {
	s.countdown.Stop()
	s.countdown = nil
	if !s.expanded || s.complete || d == nil {
		return
	}
	waiting := false
	for _, c := range d.GetConnectionInfo() {
		if c.GetState() == downexv1.ConnectionState_CONNECTION_STATE_RETRYING && retryWait.MatchString(c.GetDetail()) {
			waiting = true
			break
		}
	}
	if !waiting {
		return
	}
	s.now = s.app.Clock.Now()
	s.tick()
}

func (s *Screen) tick() {
	s.countdown = s.pw.Kit.After(time.Second, func() {
		s.now = s.app.Clock.Now()
		s.renderDetails(s.d())
		s.tick()
	})
}

// connectionLine is a connection's Info; a wait in seconds counts down between the reports.
func (s *Screen) connectionLine(c *downexv1.ConnectionInfo, isMedia bool, piece string) string {
	var m []string
	if c.GetState() == downexv1.ConnectionState_CONNECTION_STATE_RETRYING {
		m = retryWait.FindStringSubmatch(c.GetDetail())
	}
	if m == nil {
		delete(s.retrySeen, c.GetIndex())
		return progress.ConnectionText(c, isMedia, piece)
	}
	seen, ok := s.retrySeen[c.GetIndex()]
	if !ok || seen.detail != c.GetDetail() {
		seen = retryNotice{detail: c.GetDetail(), at: s.app.Clock.Now()}
		s.retrySeen[c.GetIndex()] = seen
	}
	wait, _ := strconv.ParseFloat(m[2], 64)
	elapsed := math.Max(0, float64(s.now.Sub(seen.at))/float64(time.Millisecond))
	left := math.Max(0, math.Ceil(wait-elapsed/1000))
	t := "Retrying in " + strconv.FormatFloat(left, 'f', -1, 64) + " s (" + m[1] + ")"
	if isMedia && c.GetFragment() >= 0 {
		return piece + " " + strconv.Itoa(int(c.GetFragment())+1) + " · " + t
	}
	return t
}

// ---- rendering ------------------------------------------------------------------------------

// render shows the state: which views are in the page and what they say; and asks for the
// fits the script's effects ask for.
func (s *Screen) render() {
	if s.destroyed {
		return
	}
	d := s.d()
	off := s.offline()
	s.kids = s.kids[:0]
	if off {
		s.kids = append(s.kids, s.banner)
	}
	switch {
	case d == nil:
		s.message.set(s.store.Missing(), s.store.LoadError())
		s.kids = append(s.kids, s.message)
	case s.complete:
		s.done.set(s, d)
		s.kids = append(s.kids, s.done)
	default:
		s.renderProgress(d)
	}
	s.SetChildren(s.kids...)
	s.effects(d, off)
}

func (s *Screen) renderProgress(d *downexv1.Download) {
	failed := d.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_FAILED
	s.head.set(d)
	s.bar.set(d)
	if s.limitsDot != nil {
		s.limitsDot.SetHidden(!(d.GetSpeedLimit() > 0))
	}
	s.tabs.SetValue(s.tab)
	if s.tab == tabStatus {
		s.statusV.set(s, d)
		s.panelBody.SetChildren(s.statusV)
	} else {
		s.renderLimits(false)
		s.panelBody.SetChildren(s.limitsV)
	}
	flex := kit.FlexOf(1, 1, kit.Length{})
	if s.expanded {
		flex = kit.FlexNone
	}
	if s.panel.Style.Flex != flex {
		s.panel.Style.Flex = flex
		s.panel.Relayout()
	}
	s.kids = append(s.kids, s.head, s.bar, s.tabsBox, s.panel)
	if s.actionError != "" {
		s.actErr.SetText(escapeBidi(s.actionError))
		s.kids = append(s.kids, s.actErr)
	}
	s.renderActions(d, failed)
	s.kids = append(s.kids, s.actions)
	if s.expanded {
		s.renderDetails(d)
		s.kids = append(s.kids, s.details)
	}
}

func (s *Screen) renderActions(d *downexv1.Download, failed bool) {
	a := s.actions
	w := s.pw.Kit
	off := s.offline()
	if s.expanded {
		a.toggle.SetText("Hide details")
	} else {
		a.toggle.SetText("Show details")
	}
	a.toggle.Semantics.Expand = kit.ExpandClosed
	if s.expanded {
		a.toggle.Semantics.Expand = kit.ExpandOpen
	}
	a.toggle.Semantics.Controls = s.details
	a.toggle.SemanticsChanged()
	a.toggle.chev.setUp(s.expanded)
	canPause, canResume := s.canPause(d), s.canResume(d)
	refused, restart := progress.IsRefused(d), views.NeedsRestart(d)
	showButton(w, a.retry, failed && !refused && !restart, a.restart)
	showButton(w, a.restart, failed && !refused && restart, a.retry)
	showButton(w, a.close, failed, a.cancel)
	showButton(w, a.pause, !failed && canPause, a.resume)
	showButton(w, a.resume, !failed && !canPause, a.pause)
	showButton(w, a.cancel, !failed, a.close)
	for _, b := range []*widgets.Button{a.retry, a.restart} {
		b.SetDisabled(s.busy, false)
		b.DisabledFocusable = false
		if off {
			b.Semantics.States |= kit.NodeDisabled
		} else {
			b.Semantics.States &^= kit.NodeDisabled
		}
	}
	pr := s.busy || (!canPause && !canResume) || progress.IsAssembling(d)
	for _, b := range []*widgets.Button{a.pause, a.resume} {
		b.SetDisabled(pr, false)
		if off {
			b.Semantics.States |= kit.NodeDisabled
		} else {
			b.Semantics.States &^= kit.NodeDisabled
		}
	}
	a.cancel.SetDisabled(s.busy, false)
	if canPause {
		a.cancel.SetText("Cancel")
		a.cancel.Tooltip = cancelTitle
	} else {
		a.cancel.SetText("Close")
		a.cancel.Tooltip = ""
	}
}

func (s *Screen) renderDetails(d *downexv1.Download) {
	if d == nil || !s.expanded || s.complete {
		return
	}
	v := s.details
	isMedia := s.media(d)
	one, many := progress.PieceNames(d)
	v.smap.SetHidden(isMedia)
	v.frags.SetHidden(!isMedia)
	if isMedia {
		label := many + " downloaded"
		if label != v.fragL {
			v.fragL = label
			v.frags.Label = label
			v.frags.SemanticsChanged()
		}
		v.frags.Set(format.ProgressFraction(d), barState(d.GetState()))
	} else {
		v.smap.set(d)
	}
	empty := noConnsStopped
	if d.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING {
		switch {
		case progress.IsAssembling(d) && media.IsSiteDownload(d):
			empty = mergingText
		case progress.IsAssembling(d):
			empty = assemblingText
		default:
			empty = noConnsYet
		}
	}
	v.table.set(d.GetConnectionInfo(), func(c *downexv1.ConnectionInfo) string { return s.connectionLine(c, isMedia, one) }, empty)
}

// effects asks for the fits the script's effects ask for, when what they follow changed.
func (s *Screen) effects(d *downexv1.Download, off bool) {
	failed := d.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_FAILED
	n := seenState{actionError: s.actionError, prefError: s.prefError, dontShow: s.dontShowAgain, offline: off, tab: s.tab, failed: failed, init: true}
	p := s.seen
	s.seen = n
	if !p.init {
		return
	}
	// The completion view: when a message or the banner changes its height.
	if s.complete && (n.actionError != p.actionError || n.prefError != p.prefError || n.dontShow != p.dontShow || n.offline != p.offline) {
		s.queueFit(false)
	}
	// The collapsed progress view: another tab, a message or the banner.
	if !s.complete && !s.expanded && s.sized && (n.tab != p.tab || n.actionError != p.actionError || n.offline != p.offline || n.failed != p.failed) {
		s.queueFit(false)
	}
}

var _ = widgets.FontBody
