package shell

import (
	"downex/ui/core/categories"
	"downex/ui/core/format"
	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/views"
)

// HeaderChips are the page header's summary chips: how many downloads are downloading, queued
// or failed (Toolbar.svelte's HeaderChips).
type HeaderChips struct {
	Downloading, Queued, Failed int
}

// MoreState is what the More menu shows (Toolbar.svelte's MoreState).
type MoreState struct {
	Compact bool
	// Details: the details pane is shown (not collapsed to its header).
	Details bool
	// Queues: Manage queues… is offered (downexd has queues).
	Queues bool
}

// HeaderState is what the page header shows.
type HeaderState struct {
	views.Abilities
	// Title is "All downloads", or the names of the filters shown (views.PageTitle).
	Title string
	// Count is how many downloads the list shows.
	Count int
	Chips HeaderChips
	// Status is the status filter in effect (a chip is pressed when it is its own).
	Status categories.Status
	More   MoreState
}

// HeaderOptions are the page header's actions (Toolbar.svelte's callbacks).
type HeaderOptions struct {
	OnAdd, OnAddVideo               func()
	OnResume, OnPause, OnRemove     func()
	OnPauseAll, OnResumeAll         func()
	OnChip                          func(categories.Status)
	OnDensity                       func(compact bool)
	OnToggleDetails, OnManageQueues func()
	OnSelectAll                     func()
}

// The header's metrics (Toolbar.svelte).
const (
	headPadStart  kit.DIP = 20 // .toolbar padding-inline: 20px 12px
	headPadEnd    kit.DIP = 12
	chipsGap      kit.DIP = 6 // .chips gap
	chipGap       kit.DIP = 5 // .chips .chip gap
	actionsGap    kit.DIP = 2 // .actions gap
	moreStart     kit.DIP = 2 // .actions .more margin-inline: 2px 8px
	moreEnd       kit.DIP = 8
	vsepMargin    kit.DIP = 6 // .actions .vsep margin-inline
	ghostPad      kit.DIP = 9 // .btn.ghost padding-inline
	actionIcon    kit.DIP = 15
	iconOnlyWidth kit.DIP = 30 // .bulk, .sel inline-size below their breakpoints
)

// The header's breakpoints: the bulk pair is icon-only below 1366 px, the selection's
// buttons below 1180 px, and the chips leave below 960 px (Toolbar.svelte's media queries).
var (
	belowBulk  = kit.Query{MaxWidth: kit.Px(1365.98)}
	belowSel   = kit.Query{MaxWidth: kit.Px(1179.98)}
	belowChips = kit.Query{MaxWidth: kit.Px(959.98)}
)

// Header is the page header in the work card (Toolbar.svelte, DESIGN-SYSTEM.md §4.3): the
// page title with its count and the summary chips, then the selection's Resume, Pause and
// Remove with labels, the bulk pair (icon only below 1366 px), the More menu (density, details
// pane, queues, select all) and the Add split button. It never wraps.
type Header struct {
	kit.Element
	opts HeaderOptions
	st   HeaderState
	// updated: st is shown.
	updated bool

	heading *kit.Element
	title   *views.Text
	count   *views.Text
	chips   *kit.Element
	chip    [3]*widgets.Chip
	words   [3]*views.Text

	actions               *kit.Element
	resume, pause, remove *widgets.Button
	resumeAll, pauseAll   *widgets.Button
	moreBtn               *widgets.IconButton
	more                  *widgets.MenuButton
	add                   *widgets.SplitButton
}

// chipFilters are the chips' filters and words, in order (Toolbar.svelte's CHIPS).
var chipFilters = [3]struct {
	filter categories.Status
	word   string
	tone   widgets.Tone
}{
	{categories.Active, "downloading", widgets.Live},
	{categories.Queued, "queued", widgets.Plain},
	{categories.Failed, "failed", widgets.Fail},
}

// AddItems are the Add split button's menu (Toolbar.svelte's addItems).
var AddItems = []widgets.MenuItem{
	{ID: "files", Label: "Add downloads…", Icon: "add", Shortcut: "Ctrl+N"},
	{ID: "stream", Label: "Add video or stream…", Icon: "stream"},
}

// NewHeader makes the page header.
func NewHeader(o HeaderOptions) *Header {
	h := &Header{opts: o}
	h.Semantics = kit.Semantics{Role: kit.RoleGroup, AutomationID: "toolbar"}
	h.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: tokens.Sp6, Height: tokens.PageheadH,
		Flex: kit.FlexNone, Padding: kit.Edges{Start: kit.Px(headPadStart), End: kit.Px(headPadEnd)},
		MinWidth: kit.Px(0), Overflow: kit.OverflowHidden,
	}

	h.title = views.NewLine(categories.AllLabel, views.FontPage, views.InkInk)
	h.title.Label.SetOptions(kit.TextOptions{Truncate: kit.TruncateEnd, Direction: kit.DirAuto})
	h.title.Semantics.Role = kit.RoleHeading
	h.title.Semantics.Level = 1
	h.count = views.NewText("0", uiFont(tokens.TypeBody, 500), views.InkFaint, kit.TextOptions{})
	h.count.Style.Flex = kit.FlexNone
	h.chips = &kit.Element{Style: kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(chipsGap), Flex: kit.FlexNone,
		Margin: kit.Edges{Start: tokens.Sp4}, AlignSelf: kit.AlignCenter,
		Breakpoints: []kit.Breakpoint{{When: belowChips, Apply: func(s *kit.Style) { s.Hidden = true }}},
	}}
	for i, c := range chipFilters {
		f := c.filter
		ch := widgets.NewChip("", c.tone, c.tone == widgets.Live, "", func() {
			if h.opts.OnChip != nil {
				h.opts.OnChip(f)
			}
		})
		ch.Style.ColumnGap = kit.Px(chipGap)
		ch.SetHidden(true)
		// <span class="tnum">3</span> downloading: the count and the word are two flex items
		// of the chip, 5 px apart; the word is in the chip's text colour.
		tone, pressed := c.tone, func() bool { return ch.Pressed }
		var font kit.Font
		for _, k := range ch.Children() {
			if l, ok := k.Widget().(*kit.Label); ok {
				font = l.Font()
			}
		}
		word := views.NewText(c.word, font, func(t *tokens.Theme) kit.Color {
			switch {
			case tone == widgets.Live:
				return t.AccentText
			case tone == widgets.Fail:
				return t.Danger
			case pressed():
				return t.Ink
			}
			return t.Ink2
		}, kit.TextOptions{})
		word.PassThrough = true
		word.Semantics.Role = kit.RoleNone
		ch.Add(word)
		h.words[i] = word
		h.chip[i] = ch
		h.chips.Add(ch)
	}
	h.heading = &kit.Element{Style: kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignBaseline, ColumnGap: tokens.Sp4, Flex: kit.FlexAuto, MinWidth: kit.Px(0),
	}}
	h.heading.Add(&clippedText{h.title}, h.count, h.chips)

	h.resume = h.action("Resume", "resume", "Resume (Space)", belowSel, func() { h.when(h.st.CanResume, h.opts.OnResume) })
	h.pause = h.action("Pause", "pause", "Pause (Space)", belowSel, func() { h.when(h.st.CanPause, h.opts.OnPause) })
	h.remove = h.action("Remove", "remove", kit.KeyLabel("Remove (Del)"), belowSel, func() { h.when(h.st.CanRemove, h.opts.OnRemove) })
	h.remove.Semantics.Shortcut = "Delete"
	sep := views.NewVSep()
	sep.Base().Style.Margin = kit.EdgesInline(kit.Px(vsepMargin))
	h.resumeAll = h.action("Resume all", "resume-all", "Resume all paused downloads", belowBulk, func() { h.when(h.st.CanResumeAll, h.opts.OnResumeAll) })
	h.pauseAll = h.action("Pause all", "pause-all", "Pause all active downloads", belowBulk, func() { h.when(h.st.CanPauseAll, h.opts.OnPauseAll) })

	h.moreBtn = widgets.NewIconButton("more", 18, "More actions", false, nil)
	h.moreBtn.Tooltip = "More actions"
	h.more = widgets.NewMenuButton(h.moreBtn, "More actions", h.moreItems(), h.onMore)
	h.more.Align = widgets.AlignToEnd
	h.more.Style.Margin = kit.Edges{Start: kit.Px(moreStart), End: kit.Px(moreEnd)}

	h.add = widgets.NewSplitButton("Add", "add", func() { h.call(h.opts.OnAdd) }, AddItems, func(id string) {
		if id == "stream" && h.opts.OnAddVideo != nil {
			h.opts.OnAddVideo()
		} else {
			h.call(h.opts.OnAdd)
		}
	}, "More ways to add", "Add")
	h.add.Main.Semantics.Shortcut = kit.KeyLabel("Ctrl+N")
	// The chevron is a MenuButton of class "btn primary more", so Toolbar's
	// `.actions :global(.more) { margin-inline: 2px 8px }` reaches it too: 2 px from the main
	// half, 8 px inside the split's end (which its lift covers).
	h.add.More.Style.Margin = kit.Edges{Start: kit.Px(moreStart), End: kit.Px(moreEnd)}

	h.actions = &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(actionsGap), Flex: kit.FlexNone}}
	h.actions.Add(h.resume, h.pause, h.remove, sep, h.resumeAll, h.pauseAll, h.more, h.add)
	h.Add(h.heading, h.actions)
	h.Update(HeaderState{Title: categories.AllLabel, Status: categories.AnyStatus, Abilities: views.Abilities{CanAdd: true}, More: MoreState{Details: true}})
	return h
}

// clippedText is a text that clips its ink to its own box (.title's overflow: hidden): a
// title cut to less than its ellipsis shows only what fits inside, and never reaches the
// count beside it.
type clippedText struct{ *views.Text }

// Paint is kit.Painter's.
func (c *clippedText) Paint(p *kit.PaintContext) {
	p.Canvas.PushClip(kit.RectAt(p.Origin, p.Size()))
	c.Text.Paint(p)
	p.Canvas.Pop()
}

// action is one of the header's ghost buttons: a 15 px icon and a label that hides below its
// breakpoint (the button 30 px wide and its name kept).
func (h *Header) action(label, icon, tip string, below kit.Query, press func()) *widgets.Button {
	b := widgets.NewButton(label, icon, widgets.Ghost, widgets.SizeMd, press)
	b.SetIconSize(actionIcon)
	b.Style.Padding = kit.EdgesInline(kit.Px(ghostPad))
	b.Tooltip = tip
	b.Semantics.Name = label
	b.Style.Breakpoints = []kit.Breakpoint{{When: below, Apply: func(s *kit.Style) {
		s.Width, s.Padding = kit.Px(iconOnlyWidth), kit.Edges{}
	}}}
	b.Label().Style.Breakpoints = []kit.Breakpoint{{When: below, Apply: func(s *kit.Style) { s.Hidden = true }}}
	return b
}

// when runs f when the action is available (aria-disabled buttons keep the focus but do
// nothing).
func (h *Header) when(enabled bool, f func()) {
	if enabled && f != nil {
		f()
	}
}

func (h *Header) call(f func()) {
	if f != nil {
		f()
	}
}

// moreItems are the More menu's items (Toolbar.svelte's moreItems).
func (h *Header) moreItems() []widgets.MenuItem {
	m := h.st.More
	queues := m.Queues && h.opts.OnManageQueues != nil
	items := []widgets.MenuItem{
		{ID: "density-comfortable", Label: "Comfortable", Kind: widgets.ItemRadio, Checked: !m.Compact, Heading: "List density", Note: "2 lines"},
		{ID: "density-compact", Label: "Compact", Kind: widgets.ItemRadio, Checked: m.Compact, Note: "1 line"},
		{ID: "details", Label: "Details pane", Kind: widgets.ItemCheckbox, Checked: m.Details, Separated: true},
	}
	if queues {
		items = append(items, widgets.MenuItem{ID: "queues", Label: "Manage queues…", Icon: "queue"})
	}
	return append(items, widgets.MenuItem{ID: "select-all", Label: "Select all", Shortcut: "Ctrl+A", Separated: !queues})
}

func (h *Header) onMore(id string) {
	o := h.opts
	switch id {
	case "density-comfortable":
		if o.OnDensity != nil {
			o.OnDensity(false)
		}
	case "density-compact":
		if o.OnDensity != nil {
			o.OnDensity(true)
		}
	case "details":
		h.call(o.OnToggleDetails)
	case "queues":
		h.call(o.OnManageQueues)
	case "select-all":
		h.call(o.OnSelectAll)
	}
}

// Update shows st.
func (h *Header) Update(st HeaderState) {
	if h.updated && st == h.st {
		return // a status check repeats what the header shows
	}
	h.st, h.updated = st, true
	if h.title.Text() != st.Title {
		h.title.SetText(st.Title)
	}
	if c := format.Count(float64(st.Count)); h.count.Text() != c {
		h.count.SetText(c)
	}
	counts := [3]int{st.Chips.Downloading, st.Chips.Queued, st.Chips.Failed}
	for i, c := range chipFilters {
		ch := h.chip[i]
		n := counts[i]
		ch.SetHidden(n <= 0)
		text := format.Count(float64(n)) + " " + c.word
		if ch.Semantics.Name != text {
			ch.SetText(format.Count(float64(n)))
			ch.Semantics.Name = text
			ch.SemanticsChanged()
		}
		pressed := st.Status == c.filter
		ch.SetPressed(pressed)
		tip := "Show only the " + c.word + " downloads"
		if pressed {
			tip = "Show every status"
		}
		ch.Tooltip = tip
	}
	h.resume.SetDisabled(!st.CanResume, true)
	h.pause.SetDisabled(!st.CanPause, true)
	h.remove.SetDisabled(!st.CanRemove, true)
	h.resumeAll.SetDisabled(!st.CanResumeAll, true)
	h.pauseAll.SetDisabled(!st.CanPauseAll, true)
	h.add.SetDisabled(!st.CanAdd)
	if st.CanAdd {
		h.add.Main.Tooltip = kit.KeyLabel("Add downloads (Ctrl+N)")
	} else {
		h.add.Main.Tooltip = "Unavailable while downexd is unreachable"
	}
	h.more.Items = h.moreItems()
}

// The header's parts, for the shell, the composition and tests.
func (h *Header) Title() *views.Text                     { return h.title }
func (h *Header) CountText() *views.Text                 { return h.count }
func (h *Header) Chip(f categories.Status) *widgets.Chip { return h.chipOf(f) }
func (h *Header) Resume() *widgets.Button                { return h.resume }
func (h *Header) Pause() *widgets.Button                 { return h.pause }
func (h *Header) RemoveButton() *widgets.Button          { return h.remove }
func (h *Header) ResumeAll() *widgets.Button             { return h.resumeAll }
func (h *Header) PauseAll() *widgets.Button              { return h.pauseAll }
func (h *Header) More() *widgets.MenuButton              { return h.more }
func (h *Header) AddButton() *widgets.SplitButton        { return h.add }
func (h *Header) Heading() *kit.Element                  { return h.heading }
func (h *Header) Chips() *kit.Element                    { return h.chips }
func (h *Header) Actions() *kit.Element                  { return h.actions }

func (h *Header) chipOf(f categories.Status) *widgets.Chip {
	for i, c := range chipFilters {
		if c.filter == f {
			return h.chip[i]
		}
	}
	return nil
}
