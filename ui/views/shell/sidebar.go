package shell

import (
	"strconv"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/format"
	"downex/ui/core/query"
	"downex/ui/core/queues"
	"downex/ui/core/text"
	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/views"
)

// BrowserDownloads is what browser hand-offs do (Settings.confirmBrowserDownloads), or
// BrowserUnknown before the settings are read (Sidebar.svelte's BrowserDownloads).
type BrowserDownloads uint8

// The hand-off modes.
const (
	BrowserUnknown BrowserDownloads = iota
	BrowserAsk
	BrowserStart
)

// HandoffText is the sidebar footer's line for a mode (Sidebar.svelte's handoff).
func HandoffText(b BrowserDownloads) string {
	switch b {
	case BrowserAsk:
		return "Browser downloads: ask first"
	case BrowserStart:
		return "Browser downloads: start at once"
	}
	return "Browser downloads"
}

// handoffHint is the footer's second line.
const handoffHint = "Change in Settings › Integration"

// SidebarState is what the sidebar shows (Sidebar.svelte's props).
type SidebarState struct {
	// Index is the configured categories (Settings.categories).
	Index    *categories.Index
	Category categories.Filter
	Status   categories.Status
	Counts   query.Counts
	// Queues are the queues, main first; none: the section is not shown.
	Queues []*downexv1.Queue
	// Queue is the queue filter (0: every queue).
	Queue int64
	// QueueCounts are each queue's downloads that are not completed.
	QueueCounts map[int64]int
	Browser     BrowserDownloads
}

// SidebarOptions are the sidebar's actions; OnIntegration nil leaves the footer out.
type SidebarOptions struct {
	OnCategory     func(categories.Filter)
	OnStatus       func(categories.Status)
	OnQueue        func(id int64)
	OnManageQueues func()
	OnIntegration  func()
}

// The sidebar's metrics (Sidebar.svelte).
const (
	scrollPadTop     kit.DIP = 4  // .scroll padding-block: 4px 10px
	scrollPadBottom  kit.DIP = 10 //
	scrollPadInline  kit.DIP = 10 // .scroll padding-inline (8 px in the rail)
	railPadInline    kit.DIP = 8
	groupPadTop      kit.DIP = 16 // .group padding-block: 16px 6px (12 px for the first)
	groupPadTopFirst kit.DIP = 12
	groupPadBottom   kit.DIP = 6
	groupPadInline   kit.DIP = 9
	listGap          kit.DIP = 1 // ul gap
	railListSpace    kit.DIP = 6 // the rail's ul:not(:last-child) padding and margin below
	railFirstList    kit.DIP = 8 // the rail's first ul margin-block-start
	manageBelow      kit.DIP = 2 // .manage margin-block-end
	footPad          kit.DIP = 8 // .foot padding: 8px 10px 10px (8 px in the rail)
	footPadInline    kit.DIP = 10
	footPadBottom    kit.DIP = 10
	handoffPadBlock  kit.DIP = 7 // .handoff padding: 7px 9px (9px 0 in the rail)
	handoffPadInline kit.DIP = 9
	handoffGap       kit.DIP = 9 // .handoff gap
	handoffDotTop    kit.DIP = 6 // .handoff .dot margin-block-start
)

// Sidebar is the filters (Sidebar.svelte, DESIGN-SYSTEM.md §4.4): Library (All downloads, the
// configured categories with their file tiles, Other), Queues (running or the next scheduled
// start, and what is left to do) and Status, on the window's chrome, and a footer that says
// what browser hand-offs do. In a window 721–999 px wide it is a 56 px rail of glyphs. Update
// shows a state; the sidebar keeps its items (and their focus) across updates.
type Sidebar struct {
	kit.Element
	opts      SidebarOptions
	st        SidebarState
	rail      bool
	railAsked bool

	pane    *widgets.ScrollPane
	content *kit.Element

	libHead   *groupHeading
	libList   *navList
	cats      []*navItem
	catKeys   []categories.Filter
	qHead     *kit.Element
	qHeading  *groupHeading
	manage    *widgets.IconButton
	qList     *navList
	qItems    []*navItem
	qIDs      []int64
	stHead    *groupHeading
	stList    *navList
	statuses  []*navItem
	foot      *sidebarFoot
	handoff   *handoffButton
	firstList *navList
}

// NewSidebar makes the sidebar.
func NewSidebar(o SidebarOptions) *Sidebar {
	s := &Sidebar{opts: o}
	s.Semantics = kit.Semantics{Role: kit.RoleNavigation, Name: "Filters"}
	s.Style = kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, MinHeight: kit.Px(0), Height: kit.Pct(100)}

	s.content = &kit.Element{Style: kit.Style{
		Display: kit.DisplayFlex, Axis: kit.AxisColumn,
		Padding: kit.Edges{Top: kit.Px(scrollPadTop), Bottom: kit.Px(scrollPadBottom), Start: kit.Px(scrollPadInline), End: kit.Px(scrollPadInline)},
	}}
	s.libHead = newGroupHeading("Library", true)
	s.libList = newNavList("Library")
	s.qHeading = newGroupHeading("Queues", false)
	s.qHead = &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignEnd, Justify: kit.JustifySpaceBetween}}
	s.qHead.Add(s.qHeading)
	if o.OnManageQueues != nil {
		s.manage = widgets.NewIconButton("settings", 14, "Manage queues", true, o.OnManageQueues)
		s.manage.Tooltip = "Manage queues…"
		s.manage.Style.Margin = kit.Edges{Bottom: kit.Px(manageBelow)}
		// .manage { color: var(--faint) }, --ink when hovered (.icon-btn:hover)
		if ic, ok := s.manage.Children()[0].Widget().(*widgets.Icon); ok {
			m := s.manage
			ic.SetColor(func(t *tokens.Theme) kit.Color {
				if m.Hovered() || m.Down() {
					return t.Ink
				}
				return t.Faint
			})
		}
		s.qHead.Add(s.manage)
	}
	s.qList = newNavList("Queues")
	s.stHead = newGroupHeading("Status", false)
	s.stList = newNavList("Status")
	for _, st := range categories.StatusFilters() {
		st := st
		label := categories.StatusLabel(st)
		it := newNavItem(newMark(markOfFilter(st), 14), label, func() {
			if s.opts.OnStatus == nil {
				return
			}
			if s.st.Status == st {
				s.opts.OnStatus(categories.AnyStatus)
			} else {
				s.opts.OnStatus(st)
			}
		})
		it.Tooltip = label
		s.statuses = append(s.statuses, it)
		s.stList.Add(it)
	}
	s.content.Add(s.libHead, s.libList, s.qHead, s.qList, s.stHead, s.stList)
	s.pane = widgets.NewScrollPane(s.content)
	s.pane.Style.Flex = kit.FlexAuto
	s.pane.Style.MinHeight = kit.Px(0)
	s.Add(s.pane)
	if o.OnIntegration != nil {
		s.handoff = newHandoffButton(o.OnIntegration)
		s.foot = &sidebarFoot{}
		s.foot.Style = kit.Style{
			Display: kit.DisplayFlex, Axis: kit.AxisColumn, Flex: kit.FlexNone, Border: kit.Edges{Top: kit.Px(1)},
			Padding: kit.Edges{Top: kit.Px(footPad), Start: kit.Px(footPadInline), End: kit.Px(footPadInline), Bottom: kit.Px(footPadBottom)},
		}
		s.foot.Add(s.handoff)
		s.Add(s.foot)
	}
	s.Update(SidebarState{Index: categories.NewIndex(nil), Category: categories.All, Status: categories.AnyStatus})
	return s
}

// markOfFilter is StatusMark's markOfFilter: Active shows the downloading mark.
func markOfFilter(f categories.Status) icons.MarkKind {
	switch f {
	case categories.Active:
		return icons.MarkDownloading
	case categories.Awaiting:
		return icons.MarkAwaiting
	case categories.Paused:
		return icons.MarkPaused
	case categories.Completed:
		return icons.MarkCompleted
	case categories.Failed:
		return icons.MarkFailed
	}
	return icons.MarkQueued
}

// tileOf is FileIcon's tile of a file kind.
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

// State is what the sidebar shows.
func (s *Sidebar) State() SidebarState { return s.st }

// Rail reports the 56 px rail (a window 721–999 px wide).
func (s *Sidebar) Rail() bool { return s.rail }

// Update shows st: the categories (rebuilt when the configured ones changed), the queues
// (the section only while there are any), the counts, the active filters and the footer.
func (s *Sidebar) Update(st SidebarState) {
	if st.Index == nil {
		st.Index = categories.NewIndex(nil)
	}
	s.st = st
	s.updateCategories()
	s.updateQueues()
	for i, f := range categories.StatusFilters() {
		it := s.statuses[i]
		n := st.Counts.Statuses[f]
		it.setActive(st.Status == f)
		s.setCount(it, categories.StatusLabel(f), "", n)
	}
	if s.handoff != nil {
		s.handoff.set(st.Browser)
	}
}

func (s *Sidebar) setCount(it *navItem, label, state string, n int) {
	c := format.Count(float64(n))
	it.count.set(c)
	name := label
	if state != "" {
		name += " " + state
	}
	name += " " + c
	if it.Semantics.Name != name {
		it.Semantics.Name = name
		it.SemanticsChanged()
	}
}

func (s *Sidebar) updateCategories() {
	type cat struct {
		key   categories.Filter
		label string
		kind  categories.FileKind
		all   bool
	}
	entries := s.st.Index.Entries()
	list := make([]cat, 0, len(entries)+2)
	list = append(list, cat{key: categories.All, label: categories.AllLabel, all: true})
	for _, e := range entries {
		list = append(list, cat{key: e.Key, label: text.EscapeBidi(e.Name), kind: e.Kind})
	}
	list = append(list, cat{key: categories.OtherFilter, label: categories.OtherLabel, kind: categories.Other})
	same := len(list) == len(s.catKeys)
	for i := 0; same && i < len(list); i++ {
		same = list[i].key == s.catKeys[i] && s.cats[i].label.Text() == list[i].label
	}
	if !same {
		s.libList.RemoveAll()
		s.cats, s.catKeys = s.cats[:0], s.catKeys[:0]
		for _, c := range list {
			key := c.key
			var glyph kit.Widget
			if c.all {
				glyph = widgets.NewIcon(icons.Tray, 16, func(t *tokens.Theme) kit.Color { return t.Accent })
			} else {
				glyph = newTile(tileOf(c.kind), 16)
			}
			it := newNavItem(glyph, c.label, func() {
				if s.opts.OnCategory != nil {
					s.opts.OnCategory(key)
				}
			})
			it.Tooltip = c.label
			it.setRail(s.rail)
			s.cats = append(s.cats, it)
			s.catKeys = append(s.catKeys, key)
			s.libList.Add(it)
		}
	}
	for i, it := range s.cats {
		it.setActive(s.st.Category == s.catKeys[i])
		s.setCount(it, it.label.Text(), "", s.st.Counts.Categories[s.catKeys[i]])
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Sidebar) updateQueues() {
	qs := s.st.Queues
	show := len(qs) > 0
	s.qHead.SetHidden(!show)
	s.qList.SetHidden(!show)
	same := len(qs) == len(s.qIDs)
	for i := 0; same && i < len(qs); i++ {
		same = qs[i].GetId() == s.qIDs[i]
	}
	if !same {
		s.qList.RemoveAll()
		s.qItems, s.qIDs = s.qItems[:0], s.qIDs[:0]
		for _, q := range qs {
			id := q.GetId()
			var it *navItem
			glyph := widgets.NewIcon(icons.Queue, 16, nil)
			it = newNavItem(glyph, "", func() {
				if s.opts.OnQueue == nil {
					return
				}
				if s.st.Queue == id {
					s.opts.OnQueue(0)
				} else {
					s.opts.OnQueue(id)
				}
			})
			item := it
			glyph.SetColor(func(t *tokens.Theme) kit.Color {
				// .queue-glyph: --muted; a running queue's and the active one's --accent
				if item.active || s.running(id) {
					return t.Accent
				}
				return t.Muted
			})
			it.addNote()
			it.setRail(s.rail)
			s.qItems = append(s.qItems, it)
			s.qIDs = append(s.qIDs, id)
			s.qList.Add(it)
		}
	}
	for i, q := range qs {
		it := s.qItems[i]
		name := text.EscapeBidi(q.GetName())
		if it.label.Text() != name {
			it.label.SetText(name)
		}
		note := queues.QueueNote(q)
		if it.note.Text() != note {
			it.note.SetText(note)
			it.Relayout()
		}
		ic := it.glyph.Children()[0].Widget().(*widgets.Icon)
		glyph := icons.Queue
		if !q.GetRunning() && q.GetSchedule().GetEnabled() {
			glyph = icons.Clock
		}
		// the glyph's colour follows the queue's running state (SetColor's func): repaint it
		// when that or the glyph changes
		if look := [2]int{int(glyph), boolInt(q.GetRunning())}; look != it.glyphLook {
			it.glyphLook = look
			ic.SetName(glyph)
			ic.Invalidate()
		}
		n := s.st.QueueCounts[q.GetId()]
		state, word := "Stopped", "stopped"
		if q.GetRunning() {
			state, word = "Running", "running"
		}
		it.Tooltip = name + ": " + word + ", " + strconv.Itoa(n) + " to do"
		it.setActive(s.st.Queue == q.GetId())
		s.setCount(it, name, state, n)
	}
}

func (s *Sidebar) running(id int64) bool {
	for _, q := range s.st.Queues {
		if q.GetId() == id {
			return q.GetRunning()
		}
	}
	return false
}

// setRail switches the rail on or off.
func (s *Sidebar) setRail(rail bool) {
	if rail == s.rail {
		return
	}
	s.rail = rail
	pad := scrollPadInline
	if rail {
		pad = railPadInline
	}
	s.content.Style.Padding.Start, s.content.Style.Padding.End = kit.Px(pad), kit.Px(pad)
	for _, h := range []*groupHeading{s.libHead, s.qHeading, s.stHead} {
		h.SetHidden(rail)
	}
	if rail {
		s.qHead.Style.Justify = kit.JustifyCenter
	} else {
		s.qHead.Style.Justify = kit.JustifySpaceBetween
	}
	s.libList.first = rail
	for _, l := range []*navList{s.libList, s.qList} {
		l.rule = rail
		l.applyStyle()
	}
	s.stList.rule = false
	s.stList.applyStyle()
	for _, group := range [][]*navItem{s.cats, s.qItems, s.statuses} {
		for _, it := range group {
			it.setRail(rail)
		}
	}
	if s.foot != nil {
		fp := kit.Edges{Top: kit.Px(footPad), Start: kit.Px(footPadInline), End: kit.Px(footPadInline), Bottom: kit.Px(footPadBottom)}
		if rail {
			fp = kit.EdgesAll(kit.Px(footPad))
		}
		s.foot.Style.Padding = fp
		s.handoff.setRail(rail)
	}
	s.content.Relayout()
	s.Relayout()
}

// ContentWidths is kit.Layouter's (the sidebar fills its column).
func (s *Sidebar) ContentWidths(ctx *kit.LayoutContext) (lo, hi kit.DIP) { return 0, 0 }

// ContentHeight is kit.Layouter's.
func (s *Sidebar) ContentHeight(ctx *kit.LayoutContext, w kit.DIP) kit.DIP { return 0 }

// LayoutContent is kit.Layouter's: the rail follows the window's width (@media (min-width:
// 721px) and (max-width: 999px)), then the scroll pane takes the room the footer leaves.
func (s *Sidebar) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	if w := s.Window(); w != nil {
		vw := w.Size().W
		if rail := vw >= 721 && vw <= 999; rail != s.rail && !s.railAsked {
			// The rail's styles change many boxes: they apply before the window's second pass.
			s.railAsked = true
			w.AfterLayout(func() {
				s.railAsked = false
				if w := s.Window(); w != nil {
					vw := w.Size().W
					s.setRail(vw >= 721 && vw <= 999)
				}
			})
		}
	}
	cw := box.Width()
	h := box.Height()
	var fh kit.DIP
	if s.foot != nil {
		fh = ctx.HeightFor(s.foot, cw, cw)
		ctx.Place(s.foot, kit.RectAt(kit.Pt(box.Min.X, box.Max.Y-fh), kit.Size{W: cw, H: fh}), cw)
	}
	ctx.Place(s.pane, kit.RectAt(box.Min, kit.Size{W: cw, H: max(0, h-fh)}), cw)
}

// Paint is kit.Painter's: the chrome.
func (s *Sidebar) Paint(p *kit.PaintContext) {
	p.Canvas.FillRect(kit.RectAt(p.Origin, p.Size()), views.TokensOf(&s.Element).Chrome)
}

// Items are the sidebar's buttons in order (categories, queues, statuses), for tests and the
// references' boxes.
func (s *Sidebar) Items() (cats, queueItems, statuses []*kit.Element) {
	for _, it := range s.cats {
		cats = append(cats, it.Base())
	}
	for _, it := range s.qItems {
		queueItems = append(queueItems, it.Base())
	}
	for _, it := range s.statuses {
		statuses = append(statuses, it.Base())
	}
	return
}

// Manage is the Manage queues button (nil without OnManageQueues).
func (s *Sidebar) Manage() *widgets.IconButton { return s.manage }

// Handoff is the footer's button (nil without OnIntegration).
func (s *Sidebar) Handoff() *kit.Element {
	if s.handoff == nil {
		return nil
	}
	return s.handoff.Base()
}

// Pane is the scrolling part.
func (s *Sidebar) Pane() *widgets.ScrollPane { return s.pane }

// ---- the parts ----------------------------------------------------------------------------

// groupHeading is .group: a caps heading (600 var(--type-caps), +0.07em, upper case, --faint)
// with padding 16px 9px 6px (12 px above the first).
type groupHeading struct {
	*views.Text
}

func newGroupHeading(s string, first bool) *groupHeading {
	h := &groupHeading{Text: views.NewCaps(s)}
	top := groupPadTop
	if first {
		top = groupPadTopFirst
	}
	h.Style.Padding = kit.Edges{Top: kit.Px(top), Bottom: kit.Px(groupPadBottom), Start: kit.Px(groupPadInline), End: kit.Px(groupPadInline)}
	h.Semantics.Role = kit.RoleHeading
	h.Semantics.Level = 2
	return h
}

// navList is a <ul> of items: a column 1 px apart; in the rail every list but the last ends
// with 6 px, a 1 px --line rule and 6 px more, and the first starts 8 px down.
type navList struct {
	kit.Element
	rule, first bool
}

func newNavList(name string) *navList {
	l := &navList{}
	l.Semantics = kit.Semantics{Role: kit.RoleList, Name: name}
	l.applyStyle()
	return l
}

func (l *navList) applyStyle() {
	st := kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, RowGap: kit.Px(listGap), Hidden: l.Style.Hidden}
	if l.rule {
		st.Padding.Bottom = kit.Px(railListSpace)
		st.Margin.Bottom = kit.Px(railListSpace)
		st.Border.Bottom = kit.Px(1)
	}
	if l.first {
		st.Margin.Top = kit.Px(railFirstList)
	}
	l.Style = st
	l.Relayout()
}

// Paint is kit.Painter's: the rail's rule.
func (l *navList) Paint(p *kit.PaintContext) {
	if !l.rule {
		return
	}
	sz := p.Size()
	w := p.Scale.SnapWidth(1)
	y := p.Scale.Snap(p.Origin.Y + sz.H - w)
	p.Canvas.FillRect(kit.R(p.Origin.X, y, p.Origin.X+sz.W, y+w), views.TokensOf(&l.Element).Line)
}

// sidebarFoot is .foot: the footer above a 1 px --line.
type sidebarFoot struct{ kit.Element }

// Paint is kit.Painter's.
func (f *sidebarFoot) Paint(p *kit.PaintContext) {
	w := p.Scale.SnapWidth(1)
	y := p.Scale.Snap(p.Origin.Y)
	p.Canvas.FillRect(kit.R(p.Origin.X, y, p.Origin.X+p.Size().W, y+w), views.TokensOf(&f.Element).Line)
}

// handoffButton is .handoff: the dot (--seg-idle; --accent when the mode is known) and two
// lines ("Browser downloads: ask first", "Change in Settings › Integration"), on a ground that
// eases to --hover; in the rail the dot alone, centred.
type handoffButton struct {
	kit.Pressable
	dot   *dot
	text  *kit.Element
	title *kit.Label
	hint  *kit.Label
	known bool
	rail  bool
	bg    fade
	// mark is the theme bg was set in.
	mark themeMark
}

func newHandoffButton(onPress func()) *handoffButton {
	b := &handoffButton{}
	b.OnPress = onPress
	b.Focus = kit.FocusTab
	b.Semantics.Role = kit.RoleButton
	b.Cursor = kit.CursorPointer
	b.Ring = kit.Ring{Offset: kit.Px(-2)}
	b.Tooltip = "Settings › Integration"
	b.dot = newDot(func(t *tokens.Theme) kit.Color {
		if b.known {
			return t.Accent
		}
		return t.SegIdle
	})
	b.dot.Style.Margin = kit.Edges{Top: kit.Px(handoffDotTop)}
	b.title = kit.NewLabel(HandoffText(BrowserUnknown), uiFont(tokens.TypeCell, 600), kit.TextOptions{Wrap: kit.TextWrapWords})
	b.hint = kit.NewLabel(handoffHint, uiFont(tokens.TypeMeta, 400), kit.TextOptions{Wrap: kit.TextWrapWords})
	for _, l := range []*kit.Label{b.title, b.hint} {
		l.Semantics.Role = kit.RoleNone
		l.PassThrough = true
	}
	b.text = &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, RowGap: kit.Px(1), MinWidth: kit.Px(0), Flex: kit.FlexOf(0, 1, kit.Length{})}}
	b.text.PassThrough = true
	b.text.Add(b.title, b.hint)
	b.Add(b.dot, b.text)
	b.applyStyle()
	b.bg.dur = tokens.Dur2
	b.bg.onChange = b.Invalidate
	b.Semantics.Name = HandoffText(BrowserUnknown) + " " + handoffHint
	return b
}

func (b *handoffButton) applyStyle() {
	pb, pi, j := handoffPadBlock, handoffPadInline, kit.JustifyStart
	if b.rail {
		pb, pi, j = 9, 0, kit.JustifyCenter
	}
	b.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignStart, Justify: j, ColumnGap: kit.Px(handoffGap), Width: kit.Pct(100),
		Padding: kit.Edges{Top: kit.Px(pb), Bottom: kit.Px(pb), Start: kit.Px(pi), End: kit.Px(pi)},
		Radius:  kit.CornersAll(kit.Px(itemRadius)),
	}
	top := handoffDotTop
	if b.rail {
		top = 0
	}
	b.dot.Style.Margin = kit.Edges{Top: kit.Px(top)}
	b.text.SetHidden(b.rail)
	b.Relayout()
}

func (b *handoffButton) setRail(rail bool) {
	if rail != b.rail {
		b.rail = rail
		b.applyStyle()
	}
}

func (b *handoffButton) set(m BrowserDownloads) {
	known := m != BrowserUnknown
	t := HandoffText(m)
	if b.title.Text() != t {
		b.title.SetText(t)
		b.Semantics.Name = t + " " + handoffHint
		b.SemanticsChanged()
	}
	if known != b.known {
		b.known = known
		b.dot.Invalidate()
	}
}

// Title is the footer's first line.
func (b *handoffButton) Title() string { return b.title.Text() }

// Attached is kit.Attacher's.
func (b *handoffButton) Attached() { b.resetColors() }

// resetColors shows the state's ground at once (a theme change).
func (b *handoffButton) resetColors() {
	b.mark.changed(views.TokensOf(&b.Element))
	b.bg.jump(b.ground())
}

func (b *handoffButton) ground() kit.Color {
	if b.Hovered() {
		return views.TokensOf(&b.Element).Hover
	}
	return kit.Color{}
}

// HoverChanged is kit.HoverHandler's.
func (b *handoffButton) HoverChanged(h bool) {
	b.Pressable.HoverChanged(h)
	b.bg.moveTo(b.Window(), b.ground())
}

// Paint is kit.Painter's.
func (b *handoffButton) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&b.Element)
	if b.mark.changed(t) {
		b.bg.snap(b.ground())
	}
	if b.bg.cur.A > 0 {
		p.Canvas.FillRoundedRect(views.SnapBox(p, itemRadius), b.bg.cur)
	}
	b.title.SetColor(t.Ink)
	b.hint.SetColor(t.Faint)
}

// Parts are the sidebar's elements with the references' selectors.
func (s *Sidebar) Parts() map[string]Part {
	m := map[string]Part{
		"scroll": {"div.scroll", 0, s.pane.Base()}, "library heading": {"h2.group", 0, s.libHead.Base()},
	}
	n := 1
	if !s.qHead.Style.Hidden {
		m["queues heading"] = Part{"h2.group", 1, s.qHeading.Base()}
		m["group head"] = Part{"div.group-head", 0, s.qHead}
		if s.manage != nil {
			m["manage"] = Part{"button.icon-btn.sm.manage", 0, s.manage.Base()}
		}
		n = 2
	}
	m["status heading"] = Part{"h2.group", n, s.stHead.Base()}
	if s.foot != nil {
		m["foot"] = Part{"div.foot", 0, s.foot.Base()}
		m["handoff"] = Part{"button.handoff", 0, s.handoff.Base()}
	}
	return m
}

// ThemeChanged shows every item in the theme's colours at once; call it after the choice or
// the environment changed (the items also notice it when they paint).
func (s *Sidebar) ThemeChanged() {
	for _, group := range [][]*navItem{s.cats, s.qItems, s.statuses} {
		for _, it := range group {
			it.resetColors()
		}
	}
	if s.handoff != nil {
		s.handoff.resetColors()
	}
	s.Invalidate()
}
