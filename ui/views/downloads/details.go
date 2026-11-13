package downloads

import (
	"math"
	"slices"
	"strconv"
	"strings"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/display"
	"downex/ui/core/format"
	"downex/ui/core/media"
	"downex/ui/core/progress"
	"downex/ui/core/query"
	"downex/ui/core/text"
	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/views"
)

// The pane's texts (DetailsPanel.svelte).
const (
	txtReview        = "Review…"
	txtPause         = "Pause"
	txtResume        = "Resume"
	txtAgain         = "Download again…"
	txtShowFolder    = "Show in folder"
	txtCopyLink      = "Copy link"
	txtMore          = "More download actions"
	txtMoreTitle     = "More actions"
	txtShowDetails   = "Show details"
	txtHideDetails   = "Hide details"
	txtRegion        = "Download details"
	txtWaitsSite     = "This video from the browser waits until you choose its quality, folder and file name."
	txtWaitsStream   = "This stream from the browser waits until you choose its quality, folder and file name."
	txtWaitsFile     = "This download from the browser waits until you confirm its folder and file name."
	txtFailed        = "The download failed."
	txtErrorCode     = "Error code"
	txtAddLogin      = "Add site login…"
	txtRestart       = "Restart…"
	txtRetry         = "Retry"
	txtFinalURL      = "Final URL"
	txtSavedTo       = "Saved to"
	txtReceived      = "Received"
	txtSpeedLimit    = "Speed limit"
	txtLimitLabel    = "Download speed limit"
	txtUnlimited     = "Unlimited"
	txtQueue         = "Queue"
	txtAdded         = "Added"
	txtSite          = "Site"
	txtStream        = "Stream"
	txtUnknownFormat = "Unknown format"
	txtVideo         = "Video"
	txtAudio         = "Audio"
	txtOutput        = "Output"
	txtDecided       = "Decided when the stream is assembled"
	txtCompleted     = "Completed"
	txtNotStarted    = "Not started"
	txtMainQueue     = "Main queue"
	txtTimeUnknown   = "Time left unknown"
	txtAverage       = "Average speed"
	txtSpeed         = "Speed"
	txtNotCurrent    = "not current"
	txtLast60        = "last 60 s"
	txtPauseStrip    = "Pause"
	txtResumeStrip   = "Resume"
	txtRemoveStrip   = "Remove"
)

// The pane's heights (app.css --details-h, App.svelte's small-window rule) and its strip's.
const (
	detailsRem        = 14.125
	detailsCompactRem = 12.5
	detailsSmallRem   = 11
	stripHeight       = 44
)

var (
	fontTitle    = views.SectionFont(1.2)
	fontSubline  = uiFont(tokens.TypeSmall, 400, 1.4)
	fontStrip    = uiFont(tokens.TypeBody, 600, 1.4)
	fontBig      = kit.Font{Family: tokens.FontDisplay, Size: tokens.TypeFigure, Weight: 650, LineHeight: 1, LetterSpacing: -0.02, Tabular: true, AutoOptical: true}
	fontBigUnit  = uiFont(tokens.TypeBody, 500, 1)
	fontLeft     = uiFont(tokens.TypeBody, 600, 1.4)
	fontDone     = uiFont(tokens.TypeSmall, 400, 1.4)
	fontStats    = uiFont(tokens.TypeMeta, 400, 1.4)
	fontStatsB   = uiFont(tokens.TypeMeta, 600, 1.4)
	fontColHead  = uiFont(tokens.TypeCell, 600, 1.4)
	fontColSub   = uiFont(tokens.TypeSmall, 400, 1.4)
	fontFinished = uiFont(tokens.TypeSmall, 400, 1.4)
	fontFinishB  = uiFont(tokens.TypeSmall, 600, 1.4)
	fontErrMsg   = uiFont(tokens.TypeCell, 600, 1.4)
	fontErrHint  = uiFont(tokens.TypeSmall, 400, 1.4)
	fontCode     = kit.Font{Family: tokens.FontMono, Size: kit.Rem(0.6875), Weight: tokens.MonoWeight, LineHeight: 1.4, Tabular: true, AutoOptical: true}
)

// factFonts are dt's var(--type-small) and dd's var(--type-cell) on an 18 px line, whatever the
// text size (line-height: 18px), so their line height depends on the root size.
func factFonts(rem float64) (dt, dd, ddBold, mono kit.Font) {
	small := float32(0.75 * rem)
	cell := float32(0.78125 * rem)
	dt = uiFont(tokens.TypeSmall, 400, 18/small)
	dd = uiFont(tokens.TypeCell, 400, 18/cell)
	ddBold = uiFont(tokens.TypeCell, 600, 18/cell)
	mono = views.FontMono
	return
}

// paneMode is what the pane shows.
type paneMode uint8

const (
	paneNone paneMode = iota
	paneSingle
	paneStrip
)

// Details is DetailsPanel.svelte: nothing when nothing is selected, a 44 px strip for several
// downloads, and for one its header (tile, name, what it is, state, actions) over three
// columns: the facts, its connections and its speed. The user can collapse it to its header.
// It is the views.DetailsPane slot; the shell shows it only while something is selected.
type Details struct {
	kit.Element
	dl   *Downloads
	mode paneMode
	key  string
	// updaters write a download's changing texts into the widgets built for its structure.
	updaters []func(d *downexv1.Download)
	single   *single
	strip    *strip
	open     float32 // the opening animation's share (1: open)
	openAnim *kit.Tween
	height   kit.DIP
}

var _ views.DetailsPane = (*Details)(nil)

func newDetails(dl *Downloads) *Details {
	p := &Details{dl: dl, open: 1}
	p.Semantics.Role = kit.RoleRegion
	p.Style = kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, MinHeight: kit.Px(0), Container: true,
		Overflow: kit.OverflowHidden, Border: kit.Edges{Top: kit.Px(1)}}
	return p
}

// Mode reports what the pane shows: "", "single" or "strip".
func (p *Details) Mode() string {
	switch p.mode {
	case paneSingle:
		return "single"
	case paneStrip:
		return "strip"
	}
	return ""
}

// Paint is kit.Painter's: --surface-2 under a 1 px --line rule.
func (p *Details) Paint(pc *kit.PaintContext) {
	t := tok(&p.Element)
	sz := pc.Size()
	pc.FillRect(kit.R(0, 0, sz.W, sz.H), t.Surface2)
	pc.FillRect(kit.R(0, 0, sz.W, pc.Scale.SnapWidth(1)), t.Line)
}

// LaidOut is kit.LayoutFollower's: the pane's height follows the window's (11rem at 620 px or
// less).
func (p *Details) LaidOut() { p.applyHeight() }

func (p *Details) fullHeight() kit.DIP {
	w := p.Window()
	rem := kit.DIP(16)
	small := false
	if w != nil {
		rem = w.Environment().Scale.RemBase()
		small = w.Size().H <= 620
	}
	switch {
	case p.mode == paneStrip || (p.mode == paneSingle && p.dl.app.Details != nil && p.dl.app.Details.Collapsed()):
		return stripHeight
	case small:
		return detailsSmallRem * rem
	case p.dl.app.Compact():
		return detailsCompactRem * rem
	}
	return detailsRem * rem
}

func (p *Details) applyHeight() {
	h := p.fullHeight() * p.open
	if p.mode == paneNone {
		h = 0
	}
	if h != p.height {
		p.height = h
		p.Style.Height = kit.Px(h)
		p.Relayout()
	}
}

// refresh follows the selection and the shown download.
func (p *Details) refresh() {
	s := p.dl.store
	sel := s.Selected()
	d, ok := s.Detail()
	mode := paneNone
	switch {
	case len(sel) > 1:
		mode = paneStrip
	case ok && len(sel) <= 1:
		mode = paneSingle
	}
	was := p.mode
	if mode != was {
		p.mode = mode
		p.key = ""
		p.RemoveAll()
		p.single, p.strip, p.updaters = nil, nil, nil
		if mode == paneSingle && !p.collapsed() {
			p.playOpen()
		}
	}
	switch mode {
	case paneStrip:
		if p.strip == nil {
			p.strip = newStrip(p)
			p.Add(p.strip)
		}
		p.strip.set(sel, s.Stale())
	case paneSingle:
		key := p.structureKey(d)
		if key != p.key {
			p.key = key
			p.RemoveAll()
			p.updaters = nil
			p.single = p.buildSingle(d)
			p.Add(p.single)
		}
		for _, u := range p.updaters {
			u(d)
		}
	}
	p.Semantics.Name = ""
	p.applyHeight()
}

func (p *Details) collapsed() bool { return p.dl.app.Details != nil && p.dl.app.Details.Collapsed() }

// playOpen grows the pane from nothing over --dur-4 (animation: open).
func (p *Details) playOpen() {
	w := p.dl.app.Window
	if w == nil || !w.MotionOK() || w.Host() == nil {
		p.open = 1
		return
	}
	if p.openAnim != nil {
		w.StopAnimation(p.openAnim)
	}
	p.open = 0
	p.openAnim = &kit.Tween{From: 0, To: 1, Duration: tokens.Dur4, Ease: kit.EaseOut, Set: func(v float32) {
		p.open = v
		p.applyHeight()
	}}
	w.Animate(p.openAnim)
}

// ---- the single download ----------------------------------------------------------------------

// facts of a download that decide the pane's structure (which rows, notes and buttons exist).
func (p *Details) structureKey(d *downexv1.Download) string {
	var b strings.Builder
	app := p.dl.app
	w := app.Window
	rem := 16.0
	if w != nil {
		rem = float64(w.Environment().Scale.RemBase())
	}
	m := d.GetMedia()
	isMedia := d.GetKind() == downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA && m != nil
	v, a := "", ""
	if isMedia {
		v, a = media.ChosenTracks(m)
	}
	flags := []bool{
		p.collapsed(), app.Compact(), p.dl.store.Stale(), isMedia, isMedia && media.IsSite(m),
		d.GetError() != "", d.GetFinalUrl() != "" && d.GetFinalUrl() != d.GetUrl(), v != "", a != "",
		d.GetCompletedAt() != nil && tsMillis(d.GetCompletedAt()) > 0, p.queueText(d) != "", p.canLimit(d),
		bigOf(d, p.dl.store.Stale()) != nil, p.live(d), d.GetErrorCode() != "",
	}
	b.WriteString(strconv.FormatInt(d.GetId(), 10))
	b.WriteString(":")
	b.WriteString(stateName(d.GetState()))
	b.WriteString(":")
	b.WriteString(d.GetErrorCode())
	b.WriteString(":")
	b.WriteString(strconv.FormatFloat(rem, 'f', -1, 64))
	for _, f := range flags {
		if f {
			b.WriteByte('1')
		} else {
			b.WriteByte('0')
		}
	}
	return b.String()
}

func (p *Details) live(d *downexv1.Download) bool {
	return d.GetState() == state.StateDownloading && !p.dl.store.Stale()
}

func (p *Details) canLimit(d *downexv1.Download) bool {
	return d.GetState() != state.StateCompleted && p.dl.app.Act.SetDownloadLimit != nil
}

// queueText is "Main queue · position 2": the queue's name (when there are queues to choose
// from, else only the main queue is named) and the place in it.
func (p *Details) queueText(d *downexv1.Download) string {
	app := p.dl.app
	name := ""
	if len(app.QueueChoices()) > 0 && app.Main.Queues != nil {
		name = text.EscapeBidi(app.Main.Queues.NameOf(query.QueueOf(d)))
	} else if query.QueueOf(d) == state.MainQueueID {
		name = txtMainQueue
	}
	if name == "" {
		return ""
	}
	out := text.EscapeBidi(name)
	if d.GetState() == state.StateQueued && d.GetQueuePosition() > 0 {
		out += " · position " + format.Num(float64(d.GetQueuePosition()))
	}
	return out
}

// big is the speed figure: the live speed, or a finished download's average.
type bigFigure struct{ n, unit string }

func bigOf(d *downexv1.Download, stale bool) *bigFigure {
	switch {
	case d.GetState() == state.StateDownloading && !stale:
		n, u := display.SpeedParts(d.GetSpeedBps())
		return &bigFigure{n, u}
	case d.GetState() == state.StateCompleted && d.GetActiveMs() > 0 && d.GetSize() > 0:
		n, u := display.SpeedParts(float64(d.GetSize()) / float64(d.GetActiveMs()) * 1000)
		return &bigFigure{n, u}
	}
	return nil
}

// single is the one download's section: its header and its body.
type single struct {
	kit.Element
	p *Details
}

// on adds an updater.
func (p *Details) on(f func(d *downexv1.Download)) { p.updaters = append(p.updaters, f) }

// guard runs f unless downexd is unreachable (the buttons are aria-disabled then).
func (p *Details) guard(f func()) func() {
	return func() {
		if !p.dl.store.Stale() {
			f()
		}
	}
}

func (p *Details) buildSingle(d *downexv1.Download) *single {
	app := p.dl.app
	act := app.Act
	collapsed := p.collapsed()
	compact := app.Compact()
	stale := p.dl.store.Stale()
	rem := 16.0
	if w := app.Window; w != nil {
		rem = float64(w.Environment().Scale.RemBase())
	}
	id := d.GetId()
	cur := func() *downexv1.Download {
		if x, ok := p.dl.store.Get(id); ok {
			return x
		}
		return d
	}
	m := d.GetMedia()
	isMedia := d.GetKind() == downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA && m != nil
	site := isMedia && media.IsSite(m)

	s := &single{p: p}
	s.Style = kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, Flex: kit.Flex1, MinHeight: kit.Px(0)}

	// ---- header ----
	head := &kit.Element{Style: kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(12), Flex: kit.FlexNone, MinWidth: kit.Px(0),
		Padding: kit.Edges{Top: kit.Px(14), Start: kit.Px(20), End: kit.Px(12)},
	}}
	if compact {
		head.Style.Padding.Top = kit.Px(10)
	}
	if collapsed {
		head.Style.Padding.Top = kit.Px(0)
		head.Style.Height = kit.Px(43)
	}
	tileSize := kit.DIP(32)
	if collapsed {
		tileSize = 24
	}
	tl := newTile(tileSize)
	tl.Style.AlignSelf = kit.AlignStart
	if collapsed {
		tl.Style.AlignSelf = kit.AlignCenter
	}
	titles := &kit.Element{Style: kit.Style{Flex: kit.Flex1, MinWidth: kit.Px(0)}}
	titleLine := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(8), MinWidth: kit.Px(0)}}
	lines := 2
	if collapsed {
		lines = 1
	}
	dir := kit.DirLTR
	if p.dl.grid.table.rtl() {
		dir = kit.DirRTL
	}
	title := kit.NewFileName("", fontTitle, kit.TextOptions{Wrap: kit.TextWrapAnywhere, MaxLines: lines, Truncate: kit.TruncateEnd, Direction: dir})
	title.Style = kit.Style{MinWidth: kit.Px(0)}
	title.Semantics.Role = kit.RoleHeading
	title.Semantics.Level = 2
	titleLine.Add(title)
	var tag *views.Tag
	if isMedia {
		tag = views.NewTag(text.EscapeBidi(display.MediaBadge(d)), nil)
		tag.Insert(0, widgets.NewIcon(icons.Stream, 11, (&row{}).tagInk))
		titleLine.Add(tag)
	}
	titles.Add(titleLine)
	var sub *views.Text
	if !collapsed {
		sub = views.NewText("", fontSubline, views.InkMuted, kit.TextOptions{Truncate: kit.TruncateEnd, Direction: kit.DirAuto})
		sub.Style.MinWidth = kit.Px(0)
		sub.Style.Margin = kit.Edges{Top: kit.Px(3)}
		titles.Add(sub)
	}
	actions := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(4), Flex: kit.FlexNone}}
	pill := widgets.NewPill("", widgets.Done)
	pill.Style.Margin = kit.Edges{End: kit.Px(6)}
	pmark := newMark(12)
	pill.Insert(0, pmark)
	actions.Add(pill)
	var main *widgets.Button
	switch {
	case d.GetState() == state.StateAwaiting && app.Open.Confirm != nil:
		main = widgets.NewButton(txtReview, "", widgets.Primary, widgets.SizeSm, p.guard(func() {
			app.Open.Confirm(views.ConfirmRequest{ID: id, Requested: true})
		}))
	case state.IsActive(d.GetState()) && act.Pause != nil:
		main = widgets.NewButton(txtPause, "pause", widgets.Secondary, widgets.SizeSm, p.guard(func() { act.Pause([]int64{id}) }))
	case d.GetState() == state.StatePaused || d.GetState() == state.StateCanceled:
		main = widgets.NewButton(txtResume, "resume", widgets.Secondary, widgets.SizeSm, p.guard(func() {
			if act.Resume != nil {
				act.Resume([]int64{id})
			}
		}))
	case d.GetState() == state.StateCompleted:
		// Downloads it again (ENGINE-SPEC §7.1); the dialog asks what happens to the file.
		main = widgets.NewButton(txtAgain, "restart", widgets.Secondary, widgets.SizeSm, p.guard(func() { p.dl.restart(cur()) }))
	}
	if main != nil {
		if main.IconWidget() != nil {
			main.SetIconSize(14)
		}
		main.SetDisabled(stale, true)
		actions.Add(main)
	}
	wideOnly := func(label, icon string, run func()) *widgets.Button {
		b := widgets.NewButton(label, icon, widgets.Ghost, widgets.SizeSm, run)
		b.SetIconSize(15)
		b.Semantics.Name = label
		b.Tooltip = label
		lbl := b.Label()
		b.Style.Breakpoints = append(b.Style.Breakpoints, kit.Breakpoint{When: kit.Query{MaxWidth: kit.Px(820)}, Apply: func(s *kit.Style) {
			s.Padding.Start, s.Padding.End = kit.Px(6), kit.Px(6)
		}})
		lbl.Style.Breakpoints = append(lbl.Style.Breakpoints, kit.Breakpoint{When: kit.Query{MaxWidth: kit.Px(820)}, Apply: func(s *kit.Style) { s.Hidden = true }})
		return b
	}
	actions.Add(wideOnly(txtShowFolder, "folder", func() { p.dl.openFolder(id) }))
	actions.Add(wideOnly(txtCopyLink, "copy", func() { p.dl.copyURL(id) }))
	if act.OpenMenu != nil {
		var more *widgets.IconButton
		more = widgets.NewIconButton("more", 16, txtMore, true, func() {
			r := more.WindowBounds()
			act.OpenMenu(kit.Pt(r.Min.X, r.Max.Y+4))
		})
		more.Tooltip = txtMoreTitle
		more.Semantics.States |= kit.NodeHasPopup
		actions.Add(more)
	}
	if app.Details != nil {
		actions.Add(newPaneSep())
		label, icon := txtHideDetails, "chevron-down"
		if collapsed {
			label, icon = txtShowDetails, "chevron-up"
		}
		toggle := widgets.NewIconButton(icon, 16, label, true, func() {
			if act.ToggleDetails != nil {
				act.ToggleDetails()
			} else {
				app.Details.Toggle()
			}
		})
		toggle.Tooltip = label
		toggle.Semantics.Expand = kit.ExpandClosed
		if !collapsed {
			toggle.Semantics.Expand = kit.ExpandOpen
		}
		actions.Add(toggle)
	}
	head.Add(tl, titles, actions)
	s.Add(head)

	p.on(func(d *downexv1.Download) {
		tl.set(tileKind(string(categories.KindOf(d))), tileSize)
		title.SetText(categories.DisplayName(d))
		if sub != nil {
			line := display.RowSubline(d)
			if mm := d.GetMedia(); d.GetKind() == downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA && mm != nil {
				if q := media.CodecSummary(mm, nil); q != "" {
					if line != "" {
						line += " · "
					}
					line += q
				}
			}
			sub.SetText(line)
			sub.SetHidden(line == "")
		}
		pl := display.StatePill(d)
		pill.SetText(pl.Text)
		pill.SetTone(pillTone(pl.Tone))
		pmark.set(icons.MarkOf(stateName(d.GetState())), 12)
	})

	if collapsed {
		return s
	}

	// ---- body ----
	body := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, RowGap: kit.Px(10),
		Padding: kit.EdgesBlock(kit.Px(12)), MinHeight: kit.Px(0)}}
	if compact {
		body.Style.RowGap = kit.Px(8)
		body.Style.Padding = kit.EdgesBlock(kit.Px(8))
	}
	pane := widgets.NewScrollPane(body)
	pane.Style.Flex = kit.Flex1
	pane.Focusable = true
	pane.Semantics.Name = txtRegion
	fade := &fadeOver{pane: pane}
	fade.Style = kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, Flex: kit.Flex1, MinHeight: kit.Px(0)}
	fade.Add(pane)
	s.Add(fade)

	noteInset := kit.Edges{Start: kit.Px(20), End: kit.Px(16)}
	if d.GetState() == state.StateAwaiting {
		msg := txtWaitsFile
		switch {
		case site:
			msg = txtWaitsSite
		case isMedia:
			msg = txtWaitsStream
		}
		n := views.NewNote(views.NoteInfo, msg)
		n.Style.Margin = noteInset
		n.Style.Padding.Top, n.Style.Padding.Bottom = kit.Px(6), kit.Px(6)
		body.Add(n)
	}
	if d.GetState() == state.StateFailed || d.GetError() != "" {
		body.Add(p.errorNote(d, site, stale, noteInset, cur))
	}

	cols := newColumnsBox()
	body.Add(cols)

	// facts
	dtFont, ddFont, ddBold, monoFont := factFonts(rem)
	facts := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, RowGap: kit.Px(6),
		Padding: kit.EdgesInline(kit.Px(20)), MinWidth: kit.Px(0)}}
	if compact {
		facts.Style.RowGap = kit.Px(4)
	}
	facts.Style.Breakpoints = []kit.Breakpoint{{When: kit.Query{MaxWidth: kit.Px(820)}, Apply: func(s *kit.Style) {
		s.Padding.End = kit.Px(14)
	}}}
	facts.Semantics.Role = kit.RoleList
	addFact := func(term string, value kit.Widget, with bool) *kit.Element {
		r := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, ColumnGap: kit.Px(12), MinWidth: kit.Px(0)}}
		dt := views.NewText(term, dtFont, views.InkFaint, kit.TextOptions{})
		dt.Semantics.Role = kit.RoleTerm
		dt.Style = kit.Style{Width: kit.Px(76), Flex: kit.FlexNone}
		dd := &kit.Element{Style: kit.Style{Flex: kit.Flex1, MinWidth: kit.Px(0), Overflow: kit.OverflowClipInline}}
		if with {
			dd.Style.Display = kit.DisplayFlex
			dd.Style.AlignItems = kit.AlignCenter
			dd.Style.ColumnGap = kit.Px(6)
		}
		dd.Semantics.Role = kit.RoleDefinition
		dd.Add(value)
		r.Add(dt, dd)
		facts.Add(r)
		return dd
	}
	ddText := func() *views.Text {
		t := views.NewText("", ddFont, views.InkInk2, kit.TextOptions{Truncate: kit.TruncateEnd})
		t.Style.MinWidth = kit.Px(0)
		return t
	}
	mono := func() *views.Text {
		t := views.NewText("", monoFont, views.InkInk2, kit.TextOptions{Truncate: kit.TruncateEnd, Direction: kit.DirAuto, Antialias: kit.AntialiasGrayscale})
		t.Style.MinWidth = kit.Px(0)
		t.Style.Flex = kit.FlexOf(0, 1, kit.Length{})
		return t
	}
	addr := mono()
	ddAddr := addFact(progress.AddressLabel(d), addr, true)
	ddAddr.Add(newMini(icons.Copy, txtCopyLink, func() { p.dl.copyURL(id) }))
	p.on(func(d *downexv1.Download) {
		u := text.EscapeBidi(d.GetUrl())
		addr.SetText(u)
		addr.Tooltip = u
	})
	if d.GetFinalUrl() != "" && d.GetFinalUrl() != d.GetUrl() {
		fin := mono()
		addFact(txtFinalURL, fin, true)
		p.on(func(d *downexv1.Download) {
			u := text.EscapeBidi(d.GetFinalUrl())
			fin.SetText(u)
			fin.Tooltip = u
		})
	}
	saved := mono()
	ddSaved := addFact(txtSavedTo, saved, true)
	ddSaved.Add(newMini(icons.Folder, txtShowFolder, func() { p.dl.openFolder(id) }))
	p.on(func(d *downexv1.Download) {
		path := display.JoinPath(d.GetDir(), d.GetFilename())
		saved.SetText(path)
		saved.Tooltip = path
	})
	recv := newRichLine()
	addFact(txtReceived, recv, false)
	p.on(func(d *downexv1.Download) {
		if d.GetState() == state.StateAwaiting {
			s := txtNotStarted
			if e := expectedText(d); e != "" {
				s += " (" + e + ")"
			}
			recv.set(run{s, ddFont, views.InkInk2})
			return
		}
		runs := []run{{format.Bytes(float64(d.GetReceived())), ddBold, views.InkInk}}
		rest := ""
		mm := d.GetKind() == downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA && d.GetMedia() != nil
		if d.GetSize() >= 0 {
			rest = " of " + format.Size(d)
		} else if !mm {
			rest = " of unknown size"
		}
		if pct := format.Percent(format.ProgressFraction(d)); pct != "" {
			rest += " · " + pct
		}
		if rest != "" {
			runs = append(runs, run{rest, ddFont, views.InkInk2})
		}
		recv.set(runs...)
	})
	if p.canLimit(d) {
		limits := display.LimitOptions(float64(d.GetSpeedLimit()))
		opts := make([]widgets.SelectOption, len(limits))
		for i, o := range limits {
			opts[i] = widgets.SelectOption{Value: limitValue(o.Value), Label: o.Label}
		}
		var sel *widgets.Select
		sel = widgets.NewSelect(opts, limitValue(float64(d.GetSpeedLimit())), widgets.SelectOptions{
			AriaLabel: txtLimitLabel, Size: widgets.SelectInline, Disabled: stale,
		}, func(v string) {
			x := cur()
			if v == limitValue(display.CustomLimit) {
				sel.SetValue(limitValue(float64(x.GetSpeedLimit())))
				p.dl.openProgress(x.GetId())
				return
			}
			bps, err := strconv.ParseInt(v, 10, 64)
			if err == nil {
				act.SetDownloadLimit(x.GetId(), bps)
			}
		})
		sel.Style.Margin = kit.Edges{Top: kit.Px(-4), Bottom: kit.Px(-4), Start: kit.Px(-7)}
		addFact(txtSpeedLimit, sel, true)
		p.on(func(d *downexv1.Download) {
			limits := display.LimitOptions(float64(d.GetSpeedLimit()))
			opts := make([]widgets.SelectOption, len(limits))
			for i, o := range limits {
				opts[i] = widgets.SelectOption{Value: limitValue(o.Value), Label: o.Label}
			}
			// SetOptions closes an open list and repaints: only when the choices changed (a
			// status check reports the same download again every 3 s).
			if !slices.Equal(opts, sel.Options) {
				sel.SetOptions(opts)
			}
			sel.SetValue(limitValue(float64(d.GetSpeedLimit())))
		})
	} else {
		lim := ddText()
		addFact(txtSpeedLimit, lim, true)
		p.on(func(d *downexv1.Download) {
			if d.GetSpeedLimit() > 0 {
				lim.SetText(format.Speed(float64(d.GetSpeedLimit())))
			} else {
				lim.SetText(txtUnlimited)
			}
		})
	}
	if p.queueText(d) != "" {
		q := ddText()
		addFact(txtQueue, q, false)
		p.on(func(d *downexv1.Download) { q.SetText(p.queueText(d)) })
	}
	added := ddText()
	addFact(txtAdded, added, false)
	p.on(func(d *downexv1.Download) { added.SetText(display.AddedText(d, p.dl.grid.table.nowMS())) })
	if isMedia {
		kind := ddText()
		label := txtStream
		if site {
			label = txtSite
		}
		addFact(label, kind, false)
		p.on(func(d *downexv1.Download) {
			mm := d.GetMedia()
			s := ""
			if media.IsSite(mm) {
				s = text.EscapeBidi(media.SiteVideoLabel(mm))
			} else if s = media.FormatLabel(mm.GetFormat()); s == "" {
				s = txtUnknownFormat
			}
			if mm.GetDurationMs() > 0 {
				s += " · " + format.Duration(float64(mm.GetDurationMs()))
			}
			kind.SetText(s)
		})
		v, a := media.ChosenTracks(m)
		if v != "" {
			vt := ddText()
			addFact(txtVideo, vt, false)
			p.on(func(d *downexv1.Download) {
				v, _ := media.ChosenTracks(d.GetMedia())
				vt.SetText(text.EscapeBidi(v))
			})
		}
		if a != "" {
			at := ddText()
			addFact(txtAudio, at, false)
			p.on(func(d *downexv1.Download) {
				_, a := media.ChosenTracks(d.GetMedia())
				at.SetText(text.EscapeBidi(a))
			})
		}
		out := ddText()
		addFact(txtOutput, out, false)
		p.on(func(d *downexv1.Download) {
			c := media.ContainerLabel(d.GetMedia().GetContainer())
			if c == "" {
				c = txtDecided
			}
			out.SetText(c)
		})
	}
	if tsMillis(d.GetCompletedAt()) > 0 {
		done := ddText()
		addFact(txtCompleted, done, false)
		p.on(func(d *downexv1.Download) { done.SetText(format.DateTime(tsMillis(d.GetCompletedAt()))) })
	}

	// connections
	conns := newConnections(p.dl)
	connCol := newPaneCol()
	connCol.Add(conns)
	p.on(func(d *downexv1.Download) {
		mc := int(d.GetMaxConnections())
		if mc == 0 {
			if st, _ := app.Settings(); st != nil {
				mc = int(st.GetDefaultMaxConnections())
			}
		}
		conns.set(d, p.dl.store.Stale(), mc, app.Compact())
	})

	// speed
	speedCol := newPaneCol()
	speedCol.Style.Display = kit.DisplayFlex
	speedCol.Style.Axis = kit.AxisColumn
	speedCol.Style.RowGap = kit.Px(8)
	p.buildSpeed(&speedCol.Element, d, compact)

	cols.facts, cols.conns, cols.speed = facts, connCol, speedCol
	cols.Add(facts, connCol, speedCol)
	return s
}

// expectedText is a download's size before it starts: a stream's (estimated) size, or the size
// the requester announced.
func expectedText(d *downexv1.Download) string {
	if m := d.GetMedia(); d.GetKind() == downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA && m != nil {
		return media.PickSizeText(media.PickSizeOf(m, media.TrackPick{Video: m.GetSelectedVideo(), Audio: m.GetSelectedAudio()}))
	}
	if h := display.SizeHint(d); h > 0 {
		return "about " + format.Bytes(float64(h))
	}
	return ""
}

func limitValue(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func pillTone(t display.PillTone) widgets.Tone {
	switch t {
	case display.ToneOK:
		return widgets.Ok
	case display.ToneInfo:
		return widgets.Info
	case display.ToneWarn:
		return widgets.Warn
	case display.ToneFail:
		return widgets.Fail
	}
	return widgets.Done
}

// errorNote is the failure's note: the alert, the message and its hint, the code, and what can
// help (a site login, Restart…, Retry).
func (p *Details) errorNote(d *downexv1.Download, site, stale bool, inset kit.Edges, cur func() *downexv1.Download) *views.Note {
	app := p.dl.app
	n := views.NewNote(views.NoteFail, "")
	n.Style.Display = kit.DisplayFlex
	n.Style.AlignItems = kit.AlignCenter
	n.Style.ColumnGap = kit.Px(10)
	n.Style.Margin = inset
	n.Style.Padding.Top, n.Style.Padding.Bottom = kit.Px(5), kit.Px(5)
	n.Add(widgets.NewIcon(icons.Alert, 16, func(t *tokens.Theme) kit.Color { return t.Danger }))
	body := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Wrap: true, AlignItems: kit.AlignBaseline, ColumnGap: kit.Px(8), Flex: kit.Flex1, MinWidth: kit.Px(0)}}
	msg := views.NewText("", fontErrMsg, views.InkDanger, kit.TextOptions{Wrap: kit.TextWrapAnywhere})
	msg.Style.MinWidth = kit.Px(0)
	body.Add(msg)
	hint := views.NewText("", fontErrHint, views.InkInk2, kit.TextOptions{Wrap: kit.TextWrapWords})
	hint.Style.MinWidth = kit.Px(0)
	body.Add(hint)
	n.Add(body)
	if code := d.GetErrorCode(); code != "" {
		c := &codeTag{}
		c.Tooltip = txtErrorCode
		c.Style = kit.Style{Flex: kit.FlexNone, Padding: kit.EdgesOf(kit.Px(1), kit.Px(5)), Border: kit.EdgesAll(kit.Px(1))}
		c.label = views.NewText(code, fontCode, views.InkDanger, kit.TextOptions{})
		c.Add(c.label)
		n.Add(c)
	}
	id := d.GetId()
	if d.GetErrorCode() == "auth_required" && app.Act.AddSiteLogin != nil && !site {
		b := widgets.NewButton(txtAddLogin, "add", widgets.Secondary, widgets.SizeSm, p.guard(func() { app.Act.AddSiteLogin(id) }))
		b.SetIconSize(14)
		b.SetDisabled(stale, true)
		n.Add(b)
	}
	switch {
	case d.GetErrorCode() == "validator_changed":
		b := widgets.NewButton(txtRestart, "restart", widgets.Secondary, widgets.SizeSm, p.guard(func() { p.dl.restart(cur()) }))
		b.SetIconSize(14)
		b.SetDisabled(stale, true)
		n.Add(b)
	case d.GetState() == state.StateFailed && !media.CannotRetry(d.GetErrorCode()):
		b := widgets.NewButton(txtRetry, "resume", widgets.Secondary, widgets.SizeSm, p.guard(func() {
			if app.Act.Resume != nil {
				app.Act.Resume([]int64{id})
			}
		}))
		b.SetIconSize(14)
		b.SetDisabled(stale, true)
		n.Add(b)
	}
	p.on(func(d *downexv1.Download) {
		if e := d.GetError(); e != "" {
			msg.SetText(text.EscapeBidi(e))
		} else {
			msg.SetText(txtFailed)
		}
		h := ""
		if d.GetKind() == downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA && media.IsSite(d.GetMedia()) {
			h = media.SiteErrorHint(d.GetErrorCode())
		}
		if h == "" {
			h = display.ErrorHint(d.GetErrorCode())
		}
		hint.SetText(h)
		hint.SetHidden(h == "")
	})
	return n
}

// codeTag is the error code: Cascadia Mono in a 1 px --danger-line border, --r-xs corners.
type codeTag struct {
	kit.Element
	label *views.Text
}

// Paint is kit.Painter's.
func (c *codeTag) Paint(p *kit.PaintContext) {
	t := tok(&c.Element)
	r := kit.RoundRect(p.Scale.SnapRect(kit.RectAt(p.Origin, p.Size())), 4)
	w := p.Scale.SnapWidth(1)
	p.Canvas.StrokeRoundedRect(r.Inset(w/2), kit.Stroke{Width: w}, t.DangerLine)
}

// buildSpeed is the speed column: the big figure and when it ends, or its heading; then what a
// stopped download did last, or the chart and its numbers.
func (p *Details) buildSpeed(col *kit.Element, d *downexv1.Download, compact bool) {
	stale := p.dl.store.Stale()
	id := d.GetId()
	big := bigOf(d, stale)
	live := p.live(d)
	if big != nil {
		figures := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignEnd, Justify: kit.JustifySpaceBetween, ColumnGap: kit.Px(12)}}
		bigLine := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignBaseline, ColumnGap: kit.Px(4), Flex: kit.FlexNone}}
		n := views.NewText("", fontBig, views.InkInk, kit.TextOptions{})
		u := views.NewText("", fontBigUnit, views.InkMuted, kit.TextOptions{})
		bigLine.Add(n, u)
		when := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, AlignItems: kit.AlignEnd, RowGap: kit.Px(3), MinWidth: kit.Px(0)}}
		left := views.NewText("", fontLeft, views.InkInk, kit.TextOptions{Align: kit.TextAlignEnd})
		done := views.NewText("", fontDone, views.InkMuted, kit.TextOptions{Align: kit.TextAlignEnd})
		when.Add(left, done)
		figures.Add(bigLine, when)
		col.Add(figures)
		p.on(func(d *downexv1.Download) {
			if b := bigOf(d, p.dl.store.Stale()); b != nil {
				n.SetText(b.n)
				u.SetText(b.unit)
			}
			if live {
				eta := format.EtaSeconds(d)
				if !math.IsNaN(eta) && !math.IsInf(eta, 0) {
					left.SetText(format.Eta(eta) + " left")
					done.SetText("Done ≈ " + format.Clock(p.dl.grid.table.nowMS()+eta*1000))
					done.SetHidden(false)
				} else {
					left.SetText(txtTimeUnknown)
					done.SetHidden(true)
				}
			} else {
				left.SetText("Took " + progress.TimeTakenText(d))
				done.SetText(txtAverage)
			}
		})
	} else {
		head := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignBaseline, ColumnGap: kit.Px(8)}}
		head.Semantics.Role = kit.RoleHeading
		head.Semantics.Level = 3
		h := views.NewText(txtSpeed, fontColHead, views.InkInk2, kit.TextOptions{})
		subText := txtLast60
		if stale && d.GetState() == state.StateDownloading {
			subText = txtNotCurrent
		}
		sub := views.NewText(subText, fontColSub, views.InkFaint, kit.TextOptions{})
		head.Add(h, sub)
		head.Semantics.Name = txtSpeed + " " + subText
		col.Add(head)
	}
	finished := newRichLine()
	finished.Style.Padding = kit.Edges{Top: kit.Px(10)}
	finBox := &ruleTop{}
	finBox.Style = kit.Style{Padding: kit.Edges{Top: kit.Px(10)}, Border: kit.Edges{Top: kit.Px(1)}}
	finished.Style.Padding = kit.Edges{}
	finBox.Add(finished)
	chartH := kit.DIP(72)
	switch {
	case compact:
		chartH = 42
	case big != nil:
		chartH = 60
	}
	chart := newSpeedChart(chartH)
	stats := newRichLine()
	col.Add(finBox, chart, stats)
	p.on(func(d *downexv1.Download) {
		hist := p.dl.store.HistoryOf(id)
		st := display.SpeedStatsOf(hist)
		lim := "none"
		if d.GetSpeedLimit() > 0 {
			lim = format.Speed(float64(d.GetSpeedLimit()))
		}
		stopped, has := stoppedText(d, p.live(d), st.Peak, p.dl.grid.table.nowMS())
		finBox.SetHidden(!has)
		chart.SetHidden(has)
		if has {
			runs := []run{{stopped.lead, fontFinished, views.InkMuted}}
			if stopped.bold != "" {
				runs = append(runs, run{stopped.bold, fontFinishB, views.InkInk2})
			}
			if stopped.tail != "" {
				runs = append(runs, run{stopped.tail, fontFinished, views.InkMuted})
			}
			finished.set(runs...)
			stats.SetHidden(d.GetState() == state.StateCompleted)
			stats.set(run{"Limit ", fontStats, views.InkFaint}, run{lim, fontStatsB, views.InkInk2})
			return
		}
		chart.set(hist, chartH)
		stats.SetHidden(false)
		if st.Peak > 0 {
			avg, peak := format.Speed(st.Avg), format.Speed(st.Peak)
			unit := func(s string) string { return s[strings.LastIndexByte(s, ' ')+1:] }
			if unit(avg) == unit(peak) {
				avg = avg[:max(strings.LastIndexByte(avg, ' '), 0)]
			}
			stats.set(run{"Avg ", fontStats, views.InkFaint}, run{avg, fontStatsB, views.InkInk2},
				run{" · Peak ", fontStats, views.InkFaint}, run{peak, fontStatsB, views.InkInk2},
				run{" · Limit ", fontStats, views.InkFaint}, run{lim, fontStatsB, views.InkInk2})
		} else {
			stats.set(run{"Limit ", fontStats, views.InkFaint}, run{lim, fontStatsB, views.InkInk2})
		}
	})
}

type stopped struct{ lead, bold, tail string }

// stoppedText is what a download that is not running did last (it has no speed in the last
// minute), instead of an empty chart: finished, paused at how much, stopped when, queued where,
// not started.
func stoppedText(d *downexv1.Download, live bool, peak, now float64) (stopped, bool) {
	if live || peak > 0 {
		return stopped{}, false
	}
	switch d.GetState() {
	case state.StateCompleted:
		if ms := tsMillis(d.GetCompletedAt()); ms > 0 {
			return stopped{"Finished ", display.DayTimeText(ms, now), ""}, true
		}
		return stopped{"Finished", "", ""}, true
	case state.StatePaused:
		tail := ""
		if d.GetSize() >= 0 {
			tail = " of " + format.Size(d)
		}
		return stopped{"Paused · ", format.Bytes(float64(d.GetReceived())), tail}, true
	case state.StateFailed, state.StateCanceled:
		if ms := tsMillis(d.GetUpdatedAt()); ms > 0 {
			return stopped{"Stopped ", display.DayTimeText(ms, now), ""}, true
		}
		return stopped{"Stopped", "", ""}, true
	case state.StateQueued:
		b := ""
		if d.GetQueuePosition() > 0 {
			b = " #" + format.Num(float64(d.GetQueuePosition()))
		}
		return stopped{"Queued", b, ""}, true
	case state.StateAwaiting:
		return stopped{"Not started", "", ""}, true
	}
	return stopped{}, false
}

// ruleTop is .finished's box: a 1 px --line-soft rule over it.
type ruleTop struct{ kit.Element }

// Paint is kit.Painter's.
func (r *ruleTop) Paint(p *kit.PaintContext) {
	t := tok(&r.Element)
	p.FillRect(kit.R(0, 0, p.Size().W, p.Scale.SnapWidth(1)), t.LineSoft)
}

// paneCol is a .col: 20 px either side (14 under 820 px), a 1 px --line rule at its start.
type paneCol struct {
	kit.Element
	rule bool
}

func newPaneCol() *paneCol {
	c := &paneCol{rule: true}
	c.Style = kit.Style{MinWidth: kit.Px(0), Padding: kit.EdgesInline(kit.Px(20)), Border: kit.Edges{Start: kit.Px(1)}}
	c.Style.Breakpoints = []kit.Breakpoint{{When: kit.Query{MaxWidth: kit.Px(820)}, Apply: func(s *kit.Style) {
		s.Padding = kit.EdgesInline(kit.Px(14))
	}}}
	return c
}

// Paint is kit.Painter's.
func (c *paneCol) Paint(p *kit.PaintContext) {
	if !c.rule {
		return
	}
	t := tok(&c.Element)
	w := p.Scale.SnapWidth(1)
	x := kit.DIP(0)
	if c.Window() != nil && c.Window().Direction() == kit.DirRTL {
		x = p.Size().W - w
	}
	p.FillRect(kit.R(x, 0, x+w, p.Size().H), t.Line)
}

// columnsBox is .columns: minmax(0, 1.32fr) minmax(0, 1fr) minmax(0, 0.92fr); under 640 px the
// facts and the speed side by side (1.2fr 1fr) and the connections under them, 14 px apart.
type columnsBox struct {
	kit.Element
	facts *kit.Element
	conns *paneCol
	speed *paneCol
}

func newColumnsBox() *columnsBox {
	c := &columnsBox{}
	c.Style = kit.Style{Flex: kit.Flex1, MinHeight: kit.Px(0)}
	return c
}

func (c *columnsBox) narrow(w kit.DIP) bool {
	// the container is the pane: its width is this box's (the body has no inline padding)
	return w <= 640
}

// ContentWidths is kit.Layouter's.
func (c *columnsBox) ContentWidths(*kit.LayoutContext) (min, max kit.DIP) { return 0, 0 }

// ContentHeight is kit.Layouter's.
func (c *columnsBox) ContentHeight(ctx *kit.LayoutContext, w kit.DIP) kit.DIP {
	if c.facts == nil {
		return 0
	}
	if c.narrow(w) {
		fw := w * 1.2 / 2.2
		top := max(ctx.HeightFor(c.facts, fw, w), ctx.HeightFor(c.speed, w-fw, w))
		return top + 14 + ctx.HeightFor(c.conns, w, w)
	}
	ws := c.widths(w)
	return max(ctx.HeightFor(c.facts, ws[0], w), ctx.HeightFor(c.conns, ws[1], w), ctx.HeightFor(c.speed, ws[2], w))
}

func (c *columnsBox) widths(w kit.DIP) [3]kit.DIP {
	const total = 1.32 + 1 + 0.92
	return [3]kit.DIP{w * 1.32 / total, w * 1 / total, w * 0.92 / total}
}

// LayoutContent is kit.Layouter's.
func (c *columnsBox) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	if c.facts == nil {
		return
	}
	w := box.Width()
	place := func(e kit.Widget, x, y, cw kit.DIP, h kit.DIP) {
		x0 := box.Min.X + x
		if ctx.RTL {
			x0 = box.Max.X - x - cw
		}
		ctx.Place(e, kit.R(x0, box.Min.Y+y, x0+cw, box.Min.Y+y+h), w)
	}
	if c.narrow(w) {
		fw := w * 1.2 / 2.2
		top := max(ctx.HeightFor(c.facts, fw, w), ctx.HeightFor(c.speed, w-fw, w))
		place(c.facts, 0, 0, fw, top)
		place(c.speed, fw, 0, w-fw, top)
		c.conns.rule = false
		place(c.conns, 0, top+14, w, ctx.HeightFor(c.conns, w, w))
		return
	}
	c.conns.rule = true
	ws := c.widths(w)
	h := box.Height()
	place(c.facts, 0, 0, ws[0], h)
	place(c.conns, ws[0], 0, ws[1], h)
	place(c.speed, ws[0]+ws[1], 0, ws[2], h)
}

// paneSep is the header's .vsep: 1 × 18 px of --line, 4 px either side.
type paneSep struct{ kit.Element }

func newPaneSep() *paneSep {
	s := &paneSep{}
	s.Semantics.Hidden = true
	s.Style = kit.Style{Width: kit.Px(1), Height: kit.Px(18), Flex: kit.FlexNone, Margin: kit.EdgesInline(kit.Px(4))}
	return s
}

// Paint is kit.Painter's.
func (s *paneSep) Paint(p *kit.PaintContext) {
	p.FillRect(kit.R(0, 0, p.Size().W, p.Size().H), tok(&s.Element).Line)
}

// mini is a fact's 22 px button (copy, show in folder): a mouse convenience, out of the Tab
// order and hidden from assistive technology (the header has the same).
type mini struct {
	kit.Pressable
	icon *widgets.Icon
}

func newMini(icon icons.Name, title string, run func()) *mini {
	m := &mini{}
	m.OnPress = run
	m.Focus = kit.FocusNone
	m.Semantics.Hidden = true
	m.Tooltip = title
	m.Cursor = kit.CursorPointer
	m.Style = kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Justify: kit.JustifyCenter,
		Width: kit.Px(22), Height: kit.Px(22), Flex: kit.FlexNone}
	m.icon = widgets.NewIcon(icon, 13, func(t *tokens.Theme) kit.Color {
		if m.Hovered() {
			return t.Ink
		}
		return t.Faint
	})
	m.Add(m.icon)
	return m
}

// Paint is kit.Painter's: --hover under the pointer, --r-xs corners.
func (m *mini) Paint(p *kit.PaintContext) {
	if m.Hovered() {
		p.Canvas.FillRoundedRect(kit.RoundRect(p.Scale.SnapRect(kit.RectAt(p.Origin, p.Size())), 4), tok(&m.Element).Hover)
	}
}

// HoverChanged is kit.HoverHandler's.
func (m *mini) HoverChanged(h bool) {
	m.Pressable.HoverChanged(h)
	m.icon.Invalidate()
}

// fadeOver holds the body's scroll pane and fades its last 22 px out while more is below
// (.content.more's mask: the pane's ground shows through).
type fadeOver struct {
	kit.Element
	pane  *widgets.ScrollPane
	stops [2]kit.GradientStop
}

// PaintOver is kit.OverPainter's.
func (f *fadeOver) PaintOver(p *kit.PaintContext) {
	off, content, view := f.pane.ScrollRange()
	if content-off-view <= 1 {
		return
	}
	t := tok(&f.Element)
	sz := p.Size()
	r := kit.R(p.Origin.X, p.Origin.Y+sz.H-22, p.Origin.X+sz.W, p.Origin.Y+sz.H)
	f.stops = [2]kit.GradientStop{{Offset: 0, Color: t.Surface2.WithAlpha(0)}, {Offset: 1, Color: t.Surface2}}
	p.Canvas.FillGradient(r, kit.LinearGradient{Start: kit.Pt(r.Min.X, r.Min.Y), End: kit.Pt(r.Min.X, r.Max.Y), Stops: f.stops[:]})
}

// ---- the multi-select strip -------------------------------------------------------------------

// strip is the pane for several downloads: their count and size, and Pause, Resume and Remove
// for the selection.
type strip struct {
	kit.Element
	p                          *Details
	title                      *views.Text
	pause                      *widgets.Button
	resume                     *widgets.Button
	remove                     *widgets.Button
	canPause, canResume, stale bool
}

func newStrip(p *Details) *strip {
	s := &strip{p: p}
	app := p.dl.app
	s.Style = kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(12),
		Padding: kit.Edges{Start: kit.Px(20), End: kit.Px(12)}, Flex: kit.Flex1}
	s.title = views.NewLine("", fontStrip, views.InkInk2)
	s.title.Style.Flex = kit.Flex1
	s.title.Semantics.Role = kit.RoleHeading
	s.title.Semantics.Level = 2
	s.Add(s.title)
	acts := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(4), Flex: kit.FlexNone}}
	s.pause = widgets.NewButton(txtPauseStrip, "pause", widgets.Ghost, widgets.SizeSm, func() {
		if s.canPause && app.Act.Pause != nil {
			app.Act.Pause(nil)
		}
	})
	s.resume = widgets.NewButton(txtResumeStrip, "resume", widgets.Ghost, widgets.SizeSm, func() {
		if s.canResume && app.Act.Resume != nil {
			app.Act.Resume(nil)
		}
	})
	s.remove = widgets.NewButton(txtRemoveStrip, "remove", widgets.DangerText, widgets.SizeSm, func() {
		if !s.stale && app.Open.Remove != nil {
			app.Open.Remove(views.RemoveRequest{IDs: views.SelectedIDs(p.dl.store.Selected())})
		}
	})
	for _, b := range []*widgets.Button{s.pause, s.resume, s.remove} {
		b.SetIconSize(14)
		acts.Add(b)
	}
	s.Add(acts)
	return s
}

func (s *strip) set(sel []*downexv1.Download, stale bool) {
	s.title.SetText(display.SelectionSummary(sel))
	s.stale = stale
	s.canPause, s.canResume = false, false
	for _, d := range sel {
		if state.IsActive(d.GetState()) {
			s.canPause = true
		}
		if state.IsResumable(d.GetState()) {
			s.canResume = true
		}
	}
	s.canPause = s.canPause && !stale
	s.canResume = s.canResume && !stale
	s.pause.SetDisabled(!s.canPause, true)
	s.resume.SetDisabled(!s.canResume, true)
	s.remove.SetDisabled(stale, true)
}

// ---- what the pane asks of the composition -------------------------------------------------

func (dl *Downloads) openFolder(id int64) {
	if f := dl.app.Act.OpenFolder; f != nil {
		f(id)
	}
}

func (dl *Downloads) copyURL(id int64) {
	if f := dl.app.Act.CopyURLs; f != nil {
		f([]int64{id})
	}
}

// openProgress is "Custom…": the download's progress window (its Speed limit tab), unavailable
// while stale.
func (dl *Downloads) openProgress(id int64) {
	if dl.app.Stale() {
		dl.app.Unavailable()
		return
	}
	if f := dl.app.Act.OpenProgress; f != nil {
		f(id)
	}
}
