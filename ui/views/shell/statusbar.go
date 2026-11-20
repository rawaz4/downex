package shell

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/display"
	"downex/ui/core/format"
	"downex/ui/core/queues"
	"downex/ui/core/text"
	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/views"
)

// mb is 1 MB = 1,000,000 B: limits are shown in decimal units (format.ts).
const mb = 1000 * 1000

// LimitPresets are the global speed limit's presets (bytes per second; 0 = unlimited),
// DESIGN-SYSTEM.md §3.14 (StatusBar.svelte's LIMIT_PRESETS).
var LimitPresets = []int64{0, 20 * mb, 10 * mb, 5 * mb, 1 * mb}

// StatusState is what the status bar shows (StatusBar.svelte's props).
type StatusState struct {
	// Daemon is the last status check (nil before the first); Demo the simulated daemon.
	Daemon *state.DaemonState
	Demo   bool
	Totals state.Totals
	// Shown is how many downloads the list shows, Selected how many are selected.
	Shown, Selected int
	// History is the total speed over the last minute.
	History []display.SpeedSample
	// Stale: downexd is unreachable; the menus are unavailable.
	Stale bool
	// Limit is Settings.globalSpeedLimit (0 = unlimited); LimitKnown false before the
	// settings are read.
	Limit      int64
	LimitKnown bool
	// PostAction is the main queue's post-queue action; PostActionKnown false while downexd
	// has no queues (the control is hidden). PostActionChoices are what this system offers.
	PostAction        downexv1.PostAction
	PostActionKnown   bool
	PostActionChoices []downexv1.PostAction
	// FreeBytes is free on the download folder's drive; FreeKnown false hides the item.
	FreeBytes int64
	FreeKnown bool
	// DriveLabel is "D:", "Network folder", or the folder's name.
	DriveLabel string
	// NeedBytes is what is left to download (active, queued and paused, known sizes).
	NeedBytes int64
	// Polite is Polite mode's status as downexd last reported it (nil: none yet, or an
	// older downexd); PoliteOn is the polite_mode setting (read with the limit).
	Polite   *downexv1.PoliteStatus
	PoliteOn bool
}

// StatusOptions are the status bar's actions; a nil OnLimit or OnPostAction leaves its menu
// out, a nil OnPolite the limit menu's "Polite mode" item.
type StatusOptions struct {
	OnLimit      func(views.LimitChoice)
	OnPostAction func(views.WhenDoneChoice)
	OnPolite     func(on bool)
}

// The status bar's metrics (StatusBar.svelte).
const (
	barGap      kit.DIP = 4  // .statusbar gap, .end gap
	barPad      kit.DIP = 10 // .statusbar padding-inline
	sbItemH     kit.DIP = 24 // .sb-item block-size
	sbItemPad   kit.DIP = 8  // .sb-item padding-inline
	sbItemGap   kit.DIP = 6  // .sb-item gap
	sbSepH      kit.DIP = 14 // .sep block-size
	sbSepMargin kit.DIP = 2  // .sep margin-inline
)

// The status bar's breakpoints: the counts leave at 1100 px and below, the sparkline at
// 960 px and below.
var (
	upTo1100 = kit.Query{MaxWidth: kit.Px(1100)}
	upTo960  = kit.Query{MaxWidth: kit.Px(960)}
)

// StatusBar is the status bar (StatusBar.svelte, DESIGN-SYSTEM.md §3.14): the connection, a
// mini chart of the total speed with the speed, what is active and queued, then at the end the
// global speed limit menu, "When done: keep running" (the main queue's post-queue action), the
// free space of the download folder's drive (a warning when it is too small for what is left
// to download) and the count. It implements views.StatusBar.
type StatusBar struct {
	kit.Element
	opts StatusOptions
	st   StatusState
	// updated: st is shown.
	updated bool

	conn      *sbItem
	connDot   *dot
	connText  *views.Text
	connTone  string
	speed     *sbItem
	spark     *Sparkline
	speedText *views.Text
	ticker    ticker
	countsSep *sep
	counts    *sbItem
	countsTxt *views.Text
	spacer    *kit.Element
	end       *kit.Element

	limit     *sbMenu
	limitMenu *widgets.MenuButton
	whenSep   *sep
	whenDone  *sbMenu
	whenMenu  *widgets.MenuButton
	freeSep   *sep
	free      *sbItem
	freeIcon  *widgets.Icon
	drive     *views.Text
	freeBytes *views.Text
	freeWord  *views.Text
	short     bool
	totalSep  *sep
	total     *sbItem
	totalTxt  *views.Text
}

var _ views.StatusBar = (*StatusBar)(nil)

// NewStatusBar makes the status bar.
func NewStatusBar(o StatusOptions) *StatusBar {
	b := &StatusBar{opts: o}
	b.Semantics = kit.Semantics{Role: kit.RoleGroup, Name: "", AutomationID: "statusbar"}
	b.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(barGap), Height: tokens.StatusbarH,
		Padding: kit.EdgesInline(kit.Px(barPad)), Overflow: kit.OverflowHidden, MinWidth: kit.Px(0),
	}
	small := uiFont(tokens.TypeSmall, 400)

	b.connDot = newDot(func(t *tokens.Theme) kit.Color {
		switch b.connTone {
		case "ok":
			return t.Accent
		case "pending", "demo":
			return t.Warn
		case "down":
			return t.Danger
		}
		return t.Faint
	})
	b.connText = views.NewLine("Connecting…", small, func(t *tokens.Theme) kit.Color {
		if b.connTone == "down" {
			return t.Danger
		}
		return t.Muted
	})
	b.conn = newSBItem(sbItemGap, b.connDot, b.connText)

	b.spark = NewSparkline("Total speed over the last minute")
	b.spark.Style.Breakpoints = []kit.Breakpoint{{When: upTo960, Apply: func(s *kit.Style) { s.Hidden = true }}}
	arrow := widgets.NewIcon(icons.ArrowDown, 13, func(t *tokens.Theme) kit.Color { return t.Accent })
	b.speedText = views.NewText(format.Speed(0), uiFont(tokens.TypeSmall, 600), views.InkInk, kit.TextOptions{})
	b.speedText.Style.Flex = kit.FlexNone
	// .speed's gap: 7px loses to `.statusbar :global(.sb-item)`'s 6px (Svelte's scoping makes
	// that rule the more specific one), so the items are 6 px apart as measured.
	b.speed = newSBItem(sbItemGap, b.spark, arrow, b.speedText)
	b.speed.Tooltip = "Total download speed"
	b.ticker.init(b.speedText)

	b.countsSep = newSep()
	b.countsSep.Style.Breakpoints = []kit.Breakpoint{{When: upTo1100, Apply: func(s *kit.Style) { s.Hidden = true }}}
	b.countsTxt = views.NewText("", small, views.InkMuted, kit.TextOptions{})
	b.counts = newSBItem(sbItemGap, b.countsTxt)
	b.counts.Style.Breakpoints = []kit.Breakpoint{{When: upTo1100, Apply: func(s *kit.Style) { s.Hidden = true }}}

	b.spacer = &kit.Element{Style: kit.Style{Flex: kit.FlexAuto, MinWidth: kit.Px(0)}}
	b.end = &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(barGap), MinWidth: kit.Px(0), Flex: kit.FlexOf(0, 1, kit.Length{})}}

	if o.OnLimit != nil {
		b.limit = newSBMenu("gauge", "Unlimited")
		b.limitMenu = widgets.NewMenuButton(b.limit, "Global speed limit", nil, b.onLimit)
		b.limitMenu.Align, b.limitMenu.Direction = widgets.AlignToEnd, widgets.OpenUp
		b.limitMenu.Footnote = "Per-download and queue limits still apply."
		b.limit.menu = b.limitMenu
		b.end.Add(b.limitMenu)
	}
	if o.OnPostAction != nil {
		b.whenSep = newSep()
		b.whenDone = newSBMenu("power", queues.WhenDoneText(downexv1.PostAction_POST_ACTION_UNSPECIFIED))
		b.whenMenu = widgets.NewMenuButton(b.whenDone, "When the main queue is done", nil, b.onWhenDone)
		b.whenMenu.Align, b.whenMenu.Direction = widgets.AlignToEnd, widgets.OpenUp
		b.whenDone.menu = b.whenMenu
		b.end.Add(b.whenSep, b.whenMenu)
	}
	b.freeSep = newSep()
	b.freeIcon = widgets.NewIcon(icons.Drive, 14, func(t *tokens.Theme) kit.Color { return b.freeInk(t) })
	b.drive = views.NewText("", small, func(t *tokens.Theme) kit.Color {
		if b.short {
			return t.Warn
		}
		return t.Ink2
	}, kit.TextOptions{Direction: kit.DirAuto})
	b.drive.Style.Flex = kit.FlexNone
	b.freeBytes = views.NewText("", uiFont(tokens.TypeSmall, 600), func(t *tokens.Theme) kit.Color {
		if b.short {
			return t.Warn
		}
		return t.Ink2
	}, kit.TextOptions{})
	b.freeWord = views.NewText(" free", small, func(t *tokens.Theme) kit.Color { return b.freeInk(t) }, kit.TextOptions{})
	freeText := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignBaseline, Flex: kit.FlexNone}}
	freeText.PassThrough = true
	freeText.Add(b.freeBytes, b.freeWord)
	b.free = newSBItem(sbItemGap, b.freeIcon, b.drive, freeText)
	b.totalSep = newSep()
	b.totalTxt = views.NewText("", small, views.InkMuted, kit.TextOptions{})
	b.total = newSBItem(sbItemGap, b.totalTxt)
	b.end.Add(b.freeSep, b.free, b.totalSep, b.total)

	b.Add(b.conn, newSep(), b.speed, b.countsSep, b.counts, b.spacer, b.end)
	b.Update(StatusState{})
	return b
}

func (b *StatusBar) freeInk(t *tokens.Theme) kit.Color {
	if b.short {
		return t.Warn
	}
	return t.Muted
}

// Paint is kit.Painter's: the chrome.
func (b *StatusBar) Paint(p *kit.PaintContext) {
	p.Canvas.FillRect(kit.RectAt(p.Origin, p.Size()), views.TokensOf(&b.Element).Chrome)
}

// Connection is the connection item's tone, text and tooltip (StatusBar.svelte's connection).
func Connection(d *state.DaemonState, demo bool) (tone, label, title string) {
	switch {
	case demo:
		return "demo", "Demo mode", "Demo mode: a simulated downexd"
	case d == nil:
		return "pending", "Connecting…", "Connecting to downexd…"
	case !d.OK:
		return "down", "downexd unavailable", "downexd unavailable: " + text.Isolate(d.Error)
	}
	return "ok", "Connected", strings.TrimSpace("Connected to downexd " + d.Status.GetVersion())
}

// LimitLabel is a limit as the menu names it: a preset reads "20 MB/s" (as in the details
// pane's menu), another limit keeps its decimals (format.Speed).
func LimitLabel(v int64) string {
	if slices.Contains(LimitPresets, v) {
		return strconv.FormatInt(v/mb, 10) + " MB/s"
	}
	return format.Speed(float64(v))
}

// LimitText is the limit control's text: "Limit 10 MB/s" or "Unlimited".
func LimitText(limit int64, known bool) string {
	if known && limit > 0 {
		return "Limit " + LimitLabel(limit)
	}
	return "Unlimited"
}

// politeOf is Polite mode as the bar shows it: whether it is on (the setting once read, else
// what downexd reports) and its state (UNSPECIFIED while it is off or not reported yet).
func politeOf(st StatusState) (bool, downexv1.PoliteState, int64) {
	s := st.Polite.GetState()
	on := s != downexv1.PoliteState_POLITE_STATE_UNSPECIFIED && s != downexv1.PoliteState_POLITE_STATE_OFF
	if st.LimitKnown {
		on = st.PoliteOn
	}
	if !on || s == downexv1.PoliteState_POLITE_STATE_OFF {
		return on, downexv1.PoliteState_POLITE_STATE_UNSPECIFIED, 0
	}
	return on, s, st.Polite.GetRateLimit()
}

// PoliteLimitText is the limit control's text with Polite mode (docs/POLITE-SPEC.md §8):
// "Polite 2.4 MB/s" while it slows downloads under the global limit, "Polite: measuring…"
// while it measures the line, else LimitText with " · Polite" while it is on.
func PoliteLimitText(limit int64, known, on bool, state downexv1.PoliteState, rate int64) string {
	if on {
		switch state {
		case downexv1.PoliteState_POLITE_STATE_SLOWED:
			if rate > 0 && (!known || limit <= 0 || rate < limit) {
				return "Polite " + format.Speed(float64(rate))
			}
		case downexv1.PoliteState_POLITE_STATE_MEASURING:
			return "Polite: measuring…"
		}
		return LimitText(limit, known) + " · Polite"
	}
	return LimitText(limit, known)
}

// PoliteLine is the limit control's tooltip line for Polite mode's state while it is on
// ("" while it is off). UNSPECIFIED is a state not reported yet.
func PoliteLine(on bool, state downexv1.PoliteState, rate int64) string {
	if !on {
		return ""
	}
	switch state {
	case downexv1.PoliteState_POLITE_STATE_SLOWED:
		return "Polite: slowed to keep the line smooth. Downloads are held to " + format.Speed(float64(rate)) + " while the line is busy."
	case downexv1.PoliteState_POLITE_STATE_MEASURING:
		return "Polite: measuring the line…"
	case downexv1.PoliteState_POLITE_STATE_NO_SIGNAL:
		return "Polite mode can't measure this connection, so downloads run at full speed."
	case downexv1.PoliteState_POLITE_STATE_UNAVAILABLE:
		return "Polite mode isn't available with this downexd."
	}
	return "Polite mode is on: Downex slows down if its downloads make the line laggy."
}

// PoliteItem is the limit menu's "Polite mode" checkbox.
func PoliteItem(on bool) widgets.MenuItem {
	return widgets.MenuItem{ID: "polite", Label: "Polite mode", Kind: widgets.ItemCheckbox, Checked: on, Separated: true,
		Title: "Slow down when downloads make calls, games or streaming lag"}
}

// LimitItems are the limit menu's items: the presets, the limit set elsewhere (Settings, the
// CLI) checked with its "custom" note, and Custom….
func LimitItems(limit int64) []widgets.MenuItem {
	items := make([]widgets.MenuItem, 0, len(LimitPresets)+2)
	for i, v := range LimitPresets {
		label := LimitLabel(v)
		if v == 0 {
			label = "Unlimited"
		}
		it := widgets.MenuItem{ID: "limit-" + strconv.FormatInt(v, 10), Label: label, Kind: widgets.ItemRadio, Checked: limit == v}
		if i == 0 {
			it.Heading = "Global speed limit"
		}
		items = append(items, it)
	}
	if limit > 0 && !slices.Contains(LimitPresets, limit) {
		items = append(items, widgets.MenuItem{ID: "limit-" + strconv.FormatInt(limit, 10), Label: format.Speed(float64(limit)), Kind: widgets.ItemRadio, Checked: true, Note: "custom"})
	}
	return append(items, widgets.MenuItem{ID: "custom", Label: "Custom…", Icon: "gauge", Separated: true})
}

// postActionIDs are the actions' names in the menu's ids (api.ts's PostAction).
var postActionIDs = map[downexv1.PostAction]string{
	downexv1.PostAction_POST_ACTION_UNSPECIFIED: "none",
	downexv1.PostAction_POST_ACTION_QUIT_DOWNEX: "quit_downex",
	downexv1.PostAction_POST_ACTION_SLEEP:       "sleep",
	downexv1.PostAction_POST_ACTION_HIBERNATE:   "hibernate",
	downexv1.PostAction_POST_ACTION_SHUTDOWN:    "shutdown",
}

// WhenDoneItems are the when-done menu's items: this system's actions, the current one
// checked, and Other queues….
func WhenDoneItems(current downexv1.PostAction, choices []downexv1.PostAction) []widgets.MenuItem {
	items := make([]widgets.MenuItem, 0, len(choices)+1)
	for i, a := range choices {
		it := widgets.MenuItem{ID: "post-" + postActionIDs[a], Label: queues.PostActionShortLabel(a), Kind: widgets.ItemRadio, Checked: current == a}
		if i == 0 {
			it.Heading = "When the main queue is done"
		}
		items = append(items, it)
	}
	return append(items, widgets.MenuItem{ID: "other", Label: "Other queues…", Icon: "queue", Separated: true})
}

// FreeTitle is the free-space item's tooltip, and whether the drive is short.
func FreeTitle(free, need int64) (title string, short bool) {
	if need > free {
		return "Not enough free space for the active and queued downloads (about " + format.Bytes(float64(need-free)) + " more needed)", true
	}
	return format.Bytes(float64(free)) + " free on the drive of the download folder", false
}

// TotalText is the count at the end: "28 items", "5 of 28 items · 1 selected".
func TotalText(shown, count, selected int) string {
	s := format.Count(float64(shown))
	if shown != count {
		s += " of " + format.Count(float64(count))
	}
	s += " items"
	if selected > 0 {
		s += " · " + format.Count(float64(selected)) + " selected"
	}
	return s
}

// CountsText is "3 active · 7 queued".
func CountsText(t state.Totals) string {
	return format.Count(float64(t.Active)) + " active · " + format.Count(float64(t.Queued)) + " queued"
}

// Update shows st.
func (b *StatusBar) Update(st StatusState) {
	if b.updated && b.same(st) {
		return // a status check repeats what the bar shows
	}
	b.st, b.updated = st, true
	tone, txt, title := Connection(st.Daemon, st.Demo)
	if tone != b.connTone {
		b.connTone = tone
		b.connDot.Invalidate()
		b.connText.Invalidate()
	}
	setText(b.connText, txt)
	b.conn.Tooltip = title
	b.spark.Set(st.History)
	b.ticker.to(b.Window(), st.Totals.Speed)
	b.speed.Semantics.Name = "Total speed " + format.Speed(b.ticker.target)
	setText(b.countsTxt, CountsText(st.Totals))

	if b.limit != nil {
		on, ps, rate := politeOf(st)
		if b.opts.OnPolite == nil {
			on = false
		}
		control := PoliteLimitText(st.Limit, st.LimitKnown, on, ps, rate)
		line := PoliteLine(on, ps, rate)
		b.limit.setText(control)
		b.limit.Semantics.Name = "Global speed limit: " + control
		if line != "" {
			b.limit.Semantics.Name += ". " + line
		}
		b.limit.warn = (st.LimitKnown && st.Limit > 0) || (on && (ps == downexv1.PoliteState_POLITE_STATE_SLOWED ||
			ps == downexv1.PoliteState_POLITE_STATE_MEASURING))
		b.limitMenu.SetDisabled(st.Stale || !st.LimitKnown)
		// One line: the Windows tooltip control shows a single line (it has no maximum width).
		b.limit.Tooltip = "Global speed limit"
		if line != "" {
			b.limit.Tooltip += ". " + line
		}
		if st.Stale {
			b.limit.Tooltip = "Unavailable while downexd is unreachable"
		}
		var lim int64
		if st.LimitKnown {
			lim = st.Limit
		}
		b.limitMenu.Items = LimitItems(lim)
		if b.opts.OnPolite != nil {
			b.limitMenu.Items = append(b.limitMenu.Items, PoliteItem(st.LimitKnown && st.PoliteOn))
		}
		b.limit.Invalidate()
	}
	if b.whenDone != nil {
		show := st.PostActionKnown
		b.whenSep.SetHidden(!show)
		b.whenMenu.SetHidden(!show)
		if !show {
			b.whenMenu.CloseMenu(false)
		}
		wd := queues.WhenDoneText(st.PostAction)
		b.whenDone.setText(wd)
		b.whenDone.Semantics.Name = wd
		b.whenDone.warn = queues.IsPowerAction(st.PostAction)
		b.whenMenu.SetDisabled(st.Stale)
		b.whenDone.Tooltip = "What happens when the main queue is done"
		if st.Stale {
			b.whenDone.Tooltip = "Unavailable while downexd is unreachable"
		}
		choices := st.PostActionChoices
		if choices == nil {
			choices = []downexv1.PostAction{downexv1.PostAction_POST_ACTION_UNSPECIFIED, downexv1.PostAction_POST_ACTION_QUIT_DOWNEX, downexv1.PostAction_POST_ACTION_SLEEP, downexv1.PostAction_POST_ACTION_SHUTDOWN}
		}
		b.whenMenu.Items = WhenDoneItems(st.PostAction, choices)
		b.whenDone.Invalidate()
	}

	b.freeSep.SetHidden(!st.FreeKnown)
	b.free.SetHidden(!st.FreeKnown)
	if st.FreeKnown {
		title, short := FreeTitle(st.FreeBytes, st.NeedBytes)
		b.short = short
		b.free.Tooltip = title
		name := format.Bytes(float64(st.FreeBytes)) + " free"
		if st.DriveLabel != "" {
			name = st.DriveLabel + " " + name
		}
		if short {
			name += ". " + title
			b.freeIcon.SetName(icons.Alert)
		} else {
			b.freeIcon.SetName(icons.Drive)
		}
		b.free.Semantics.Name = name
		setText(b.drive, st.DriveLabel)
		b.drive.SetHidden(st.DriveLabel == "")
		setText(b.freeBytes, format.Bytes(float64(st.FreeBytes)))
		b.free.Invalidate()
		b.freeBytes.Invalidate()
		b.freeWord.Invalidate()
		b.drive.Invalidate()
	}
	setText(b.totalTxt, TotalText(st.Shown, st.Totals.Count, st.Selected))
	b.SemanticsChanged()
}

// same reports a state that shows as the current one: the same connection (a status check
// makes a new DaemonState every time), totals, counts, history, menus and free space.
func (b *StatusBar) same(st StatusState) bool {
	o := &b.st
	t1, x1, d1 := Connection(o.Daemon, o.Demo)
	t2, x2, d2 := Connection(st.Daemon, st.Demo)
	return t1 == t2 && x1 == x2 && d1 == d2 && o.Totals == st.Totals && o.Shown == st.Shown &&
		o.Selected == st.Selected && slices.Equal(o.History, st.History) && o.Stale == st.Stale &&
		o.Limit == st.Limit && o.LimitKnown == st.LimitKnown && o.PostAction == st.PostAction &&
		o.PostActionKnown == st.PostActionKnown && slices.Equal(o.PostActionChoices, st.PostActionChoices) &&
		o.FreeBytes == st.FreeBytes && o.FreeKnown == st.FreeKnown && o.DriveLabel == st.DriveLabel &&
		o.NeedBytes == st.NeedBytes && o.PoliteOn == st.PoliteOn && o.Polite.GetState() == st.Polite.GetState() &&
		o.Polite.GetRateLimit() == st.Polite.GetRateLimit()
}

// State is what the bar shows.
func (b *StatusBar) State() StatusState { return b.st }

// OpenLimitMenu opens the limit menu on its checked item (the palette's "Set global speed
// limit…"); views.StatusBar's.
func (b *StatusBar) OpenLimitMenu() {
	if b.limitMenu != nil {
		b.limitMenu.OpenMenu(widgets.FocusChecked)
	}
}

// OpenWhenDoneMenu opens the when-done menu on its checked item (the palette's "When done…");
// views.StatusBar's. It does nothing while downexd has no queues.
func (b *StatusBar) OpenWhenDoneMenu() {
	if b.whenMenu != nil && b.st.PostActionKnown {
		b.whenMenu.OpenMenu(widgets.FocusChecked)
	}
}

// OpenWhenDone is OpenWhenDoneMenu, reporting whether the menu exists to open.
func (b *StatusBar) OpenWhenDone() bool {
	if b.whenMenu == nil || !b.st.PostActionKnown {
		return false
	}
	b.whenMenu.OpenMenu(widgets.FocusChecked)
	return true
}

func (b *StatusBar) onLimit(id string) {
	if id == "polite" {
		if b.opts.OnPolite != nil {
			b.opts.OnPolite(!b.st.PoliteOn)
		}
		return
	}
	if b.opts.OnLimit == nil {
		return
	}
	if id == "custom" {
		b.opts.OnLimit(views.LimitChoice{Custom: true})
		return
	}
	v, err := strconv.ParseInt(strings.TrimPrefix(id, "limit-"), 10, 64)
	if err != nil {
		return
	}
	b.opts.OnLimit(views.LimitChoice{BytesPerSecond: v})
}

func (b *StatusBar) onWhenDone(id string) {
	if b.opts.OnPostAction == nil {
		return
	}
	if id == "other" {
		b.opts.OnPostAction(views.WhenDoneChoice{Other: true})
		return
	}
	name := strings.TrimPrefix(id, "post-")
	for a, n := range postActionIDs {
		if n == name {
			b.opts.OnPostAction(views.WhenDoneChoice{Action: a})
			return
		}
	}
}

// The bar's parts, for the shell and tests.
func (b *StatusBar) LimitMenu() *widgets.MenuButton    { return b.limitMenu }
func (b *StatusBar) WhenDoneMenu() *widgets.MenuButton { return b.whenMenu }
func (b *StatusBar) Free() *kit.Element                { return b.free.Base() }
func (b *StatusBar) Short() bool                       { return b.short }

// LimitWarns reports that the limit control is in the warning ink (a limit or Polite mode
// holds the downloads back).
func (b *StatusBar) LimitWarns() bool { return b.limit != nil && b.limit.warn }

func (b *StatusBar) Sparkline() *Sparkline { return b.spark }
func (b *StatusBar) SpeedText() string     { return b.speedText.Text() }
func (b *StatusBar) Parts() map[string]*kit.Element {
	m := map[string]*kit.Element{
		"conn": b.conn.Base(), "speed": b.speed.Base(), "counts": b.counts.Base(), "end": b.end,
		"free": b.free.Base(), "total": b.total.Base(), "speedValue": b.speedText.Base(), "spark": b.spark.Base(),
		"connText": b.connText.Base(), "drive": b.drive.Base(), "freeBytes": b.freeBytes.Base(),
	}
	if b.limit != nil {
		m["limit"] = b.limit.Base()
	}
	if b.whenDone != nil {
		m["whendone"] = b.whenDone.Base()
	}
	return m
}

// Texts are the bar's visible texts in order, as its textContent reads them (tests).
func (b *StatusBar) Texts() []string {
	var out []string
	add := func(e *kit.Element, s string) {
		if !e.Style.Hidden && s != "" {
			out = append(out, s)
		}
	}
	add(b.conn.Base(), b.connText.Text())
	add(b.speed.Base(), "Total speed "+b.speedText.Text())
	add(b.counts.Base(), b.countsTxt.Text())
	if b.limit != nil {
		add(b.limitMenu.Base(), b.limit.Semantics.Name)
	}
	if b.whenDone != nil {
		add(b.whenMenu.Base(), b.whenDone.Semantics.Name)
	}
	add(b.free.Base(), b.free.Semantics.Name)
	add(b.total.Base(), b.totalTxt.Text())
	return out
}

func setText(t *views.Text, s string) {
	if t.Text() != s {
		t.SetText(s)
	}
}

// ---- the parts ----------------------------------------------------------------------------

// sbItem is .sb-item: an inline row 24 px high, 8 px in, 6 px apart, --r-sm corners.
type sbItem struct{ kit.Element }

func newSBItem(gap kit.DIP, children ...kit.Widget) *sbItem {
	it := &sbItem{}
	it.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(gap), Height: kit.Px(sbItemH),
		Padding: kit.EdgesInline(kit.Px(sbItemPad)), Radius: kit.CornersAll(tokens.RSm), MinWidth: kit.Px(0), Flex: kit.FlexOf(0, 1, kit.Length{}),
	}
	it.Semantics.Role = kit.RoleText
	it.Add(children...)
	return it
}

// sep is .sep: 1 × 14 px of --line-strong, 2 px apart.
type sep struct{ kit.Element }

func newSep() *sep {
	s := &sep{}
	s.Style = kit.Style{Width: kit.Px(1), Height: kit.Px(sbSepH), Margin: kit.EdgesInline(kit.Px(sbSepMargin)), Flex: kit.FlexNone}
	s.Semantics.Hidden = true
	return s
}

// Paint is kit.Painter's.
func (s *sep) Paint(p *kit.PaintContext) {
	r := p.Scale.SnapRect(kit.RectAt(p.Origin, p.Size()))
	if r.Width() < p.Scale.SnapWidth(1) {
		r.Max.X = r.Min.X + p.Scale.SnapWidth(1)
	}
	p.Canvas.FillRect(r, views.TokensOf(&s.Element).LineStrong)
}

// sbMenu is .sb-menu: the trigger of a status-bar menu, a quiet text button (an icon, the
// text and a 12 px chevron in --faint) in --muted on a transparent ground; hovered or open
// --hover and --ink; unavailable --faint; a warning (a limit in effect, a power action) in
// --warn. It is a widgets.Button underneath, so a widgets.MenuButton drives it.
type sbMenu struct {
	*widgets.Button
	chevron *widgets.Icon
	menu    *widgets.MenuButton
	warn    bool
	bg, ink fade
	// mark is the theme bg and ink were set in.
	mark themeMark
}

func newSBMenu(icon, txt string) *sbMenu {
	m := &sbMenu{Button: widgets.NewButton(txt, icon, widgets.Ghost, widgets.SizeSm, nil)}
	m.SetIconSize(14)
	m.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Flex: kit.FlexNone, ColumnGap: kit.Px(sbItemGap),
		Height: kit.Px(sbItemH), Padding: kit.EdgesInline(kit.Px(sbItemPad)), Radius: kit.CornersAll(tokens.RSm), MinWidth: kit.Px(0),
	}
	m.Label().SetFont(uiFont(tokens.TypeSmall, 400))
	if ic := m.IconWidget(); ic != nil {
		ic.SetColor(func(*tokens.Theme) kit.Color { return m.ink.cur })
	}
	m.chevron = widgets.NewIcon(icons.ChevronDown, 12, func(t *tokens.Theme) kit.Color { return t.Faint })
	m.Add(m.chevron)
	m.Cursor = kit.CursorPointer
	for _, f := range []*fade{&m.bg, &m.ink} {
		f.dur = tokens.Dur2
		f.onChange = m.Invalidate
	}
	return m
}

func (m *sbMenu) setText(s string) {
	if m.Label().Text() != s {
		m.SetText(s)
	}
}

func (m *sbMenu) colors(t *tokens.Theme) (bg, ink kit.Color) {
	open := m.menu != nil && m.menu.IsOpen()
	switch {
	case m.Disabled:
		return kit.Color{}, t.Faint
	case m.warn && (m.Hovered() || open):
		return t.Hover, t.Warn
	case m.warn:
		return kit.Color{}, t.Warn
	case m.Hovered() || open:
		return t.Hover, t.Ink
	}
	return kit.Color{}, t.Muted
}

// Paint is kit.Painter's: the ground and the text's colour (not the widgets button's look).
func (m *sbMenu) Paint(p *kit.PaintContext) {
	t := views.TokensOf(m.Base())
	bg, ink := m.colors(t)
	if fresh := m.mark.changed(t); m.bg.set && !fresh {
		w := m.Window()
		m.bg.moveTo(w, bg)
		m.ink.moveTo(w, ink)
	} else {
		// the first paint, or the theme changed: the colours at once
		m.bg.snap(bg)
		m.ink.snap(ink)
	}
	if m.bg.cur.A > 0 {
		p.Canvas.FillRoundedRect(views.SnapBox(p, 6), m.bg.cur)
	}
	m.Label().SetColor(m.ink.cur)
}

// resetColors shows the state's colours at once (a theme change).
func (m *sbMenu) resetColors() {
	t := views.TokensOf(m.Base())
	m.mark.changed(t)
	bg, ink := m.colors(t)
	m.bg.jump(bg)
	m.ink.jump(ink)
}

// PaintOver is kit.OverPainter's (the widgets button's link underline and fade are not this
// look's).
func (m *sbMenu) PaintOver(*kit.PaintContext) {}

// ticker is StatusBar's ticking speed (motion.svelte.ts's ticking): a value that eases to
// each new speed over 360 ms (cubic out), shown at once when it changes again within 720 ms
// of its last change (a running download's speed), when it is not finite, or without motion.
type ticker struct {
	label   *views.Text
	cur     float64
	target  float64
	from    float64
	changed time.Time
	anim    *tickAnim
}

const tickMs = 360 * time.Millisecond

type tickAnim struct {
	t     *ticker
	start time.Time
	dead  bool
}

func (t *ticker) init(l *views.Text) { t.label = l }

func (t *ticker) show(v float64) {
	t.cur = v
	setText(t.label, format.Speed(v))
}

// to moves the value to target.
func (t *ticker) to(w *kit.Window, target float64) {
	if target == t.target && t.label.Text() != "" {
		return
	}
	if t.anim != nil {
		t.anim.dead = true
		t.anim = nil
	}
	from := t.cur
	t.target = target
	var now time.Time
	if w != nil {
		now = w.Now()
	}
	streaming := !t.changed.IsZero() && now.Sub(t.changed) < 2*tickMs
	if target != from {
		t.changed = now
	}
	if w == nil || streaming || !w.MotionOK() || math.IsInf(target, 0) || math.IsNaN(target) || math.IsInf(from, 0) || math.IsNaN(from) || from == target {
		t.show(target)
		return
	}
	t.from = from
	t.anim = &tickAnim{t: t}
	w.Animate(t.anim)
}

// Step is kit.Animation's.
func (a *tickAnim) Step(now time.Time) bool {
	if a.dead {
		return false
	}
	if a.start.IsZero() {
		a.start = now
	}
	f := float64(now.Sub(a.start)) / float64(tickMs)
	if f >= 1 {
		a.t.show(a.t.target)
		return false
	}
	e := 1 - math.Pow(1-f, 3)
	a.t.show(a.t.from + (a.t.target-a.t.from)*e)
	return true
}

// Finish is kit.Animation's.
func (a *tickAnim) Finish() {
	if !a.dead {
		a.t.show(a.t.target)
	}
}

// ThemeChanged shows the menus in the theme's colours at once; call it after the choice or
// the environment changed (they also notice it when they paint).
func (b *StatusBar) ThemeChanged() {
	for _, m := range []*sbMenu{b.limit, b.whenDone} {
		if m != nil {
			m.resetColors()
		}
	}
	b.Invalidate()
}
