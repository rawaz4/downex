package queues

import (
	"downex/ui/core/queues"
	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/views"
)

// ScheduleEditor.svelte: a queue's schedule in the editor. A switch, then the start and stop
// times, every week (weekday chips) or once (a date) as a segmented control, the retry interval
// and, where the computer can be woken, waking it. Times and the date are the app's own 24-hour
// fields (widgets.DateTimeField). The rows show only while the schedule is on; what they hold
// is kept while it is off.

const (
	switchHint = "Off: the queue starts and stops when you say."
	timesHint  = "A stop time before the start means the next day."
	repeatHint = "While the queue runs on schedule, failed and unfinished downloads are tried again. Empty = off."
)

// qRow is .q-row: a row of the schedule's card (48 px or more, 9/14 px padding) with a 1 px
// --line-soft above it when another row comes before it (.q-row + .q-row).
type qRow struct {
	kit.Element
	top bool
}

func (r *qRow) setTop(on bool) {
	if on == r.top {
		return
	}
	r.top = on
	r.Style.Border = kit.Edges{}
	if on {
		r.Style.Border = kit.Edges{Top: kit.Px(1)}
	}
	r.Relayout()
	r.Invalidate()
}

// Paint is kit.Painter's.
func (r *qRow) Paint(p *kit.PaintContext) {
	if r.top {
		p.FillRect(kit.R(0, 0, p.Size().W, p.Scale.SnapWidth(1)), views.TokensOf(&r.Element).LineSoft)
	}
}

// qGridRow is a .q-row that is a grid (the times; the retry interval).
type qGridRow struct {
	grid
	top bool
}

func (r *qGridRow) setTop(on bool) {
	if on == r.top {
		return
	}
	r.top = on
	r.Style.Border = kit.Edges{}
	if on {
		r.Style.Border = kit.Edges{Top: kit.Px(1)}
	}
	r.Relayout()
	r.Invalidate()
}

// Paint is kit.Painter's.
func (r *qGridRow) Paint(p *kit.PaintContext) {
	if r.top {
		p.FillRect(kit.R(0, 0, p.Size().W, p.Scale.SnapWidth(1)), views.TokensOf(&r.Element).LineSoft)
	}
}

type topper interface {
	kit.Widget
	setTop(bool)
}

var rowPadding = kit.EdgesOf(kit.Px(9), kit.Px(14))

func newSwitchQRow(sw *widgets.SwitchRow) *qRow {
	r := &qRow{}
	r.Style = kit.Style{Display: kit.DisplayBlock, Padding: kit.EdgesOf(kit.Px(2), kit.Px(14)), MinHeight: kit.Px(48), MinWidth: kit.Px(0)}
	r.Add(sw)
	return r
}

// dayChip is .chip.day: a weekday toggle (aria-pressed), 28 px high and 2.75rem wide or more,
// 8 px padding, 7 px corners; pressed in Mint (--accent-soft, --accent-line, --accent-text
// 600); hovered --hover; 55% while disabled.
type dayChip struct {
	kit.Pressable
	bit     int32
	pressed bool
	label   *kit.Label
}

func newDayChip(day queues.Weekday, onPress func()) *dayChip {
	c := &dayChip{bit: day.Bit}
	c.OnPress = onPress
	c.Focus = kit.FocusTab
	c.Cursor = kit.CursorPointer
	c.Semantics.Role = kit.RoleButton
	c.Semantics.Name = day.Long // aria-label
	c.Tooltip = day.Long
	c.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Justify: kit.JustifyCenter, Flex: kit.FlexNone,
		MinWidth: kit.Rem(2.75), Height: kit.Px(28), Padding: kit.EdgesInline(kit.Px(8)), Border: kit.EdgesAll(kit.Px(1)),
		Radius: kit.CornersAll(kit.Px(7)),
	}
	c.label = kit.NewLabel(day.Short, fontDay, kit.TextOptions{})
	c.label.Semantics.Role = kit.RoleNone
	c.Add(c.label)
	return c
}

func (c *dayChip) setPressed(on bool) {
	if on == c.pressed {
		return
	}
	c.pressed = on
	w := float32(500)
	if on {
		w = 600
	}
	c.label.SetFont(withWeight(fontDay, w))
	c.Invalidate()
	c.SemanticsChanged()
}

// FillSemantics is kit.SemanticFiller's: aria-pressed.
func (c *dayChip) FillSemantics(n *kit.SemanticNode) {
	c.Pressable.FillSemantics(n)
	n.Toggle = kit.ToggleOff
	if c.pressed {
		n.Toggle = kit.ToggleOn
	}
}

// Paint is kit.Painter's.
func (c *dayChip) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&c.Element)
	bg, border, ink := t.Surface2, t.Line, t.Ink2
	if !c.Disabled && c.Hovered() {
		bg = t.Hover
	}
	if c.pressed {
		bg, border, ink = t.AccentSoft, t.AccentLine, t.AccentText
	}
	if t.IsForced() {
		border = t.Ink
	}
	if c.Disabled {
		bg, border, ink = bg.WithAlpha(bg.A*0.55), border.WithAlpha(border.A*0.55), ink.WithAlpha(ink.A*0.55)
	}
	r := views.SnapBox(p, 7)
	p.Canvas.FillRoundedRect(r, bg) // --accent-soft is translucent: the ground is under the border too
	w := p.Scale.SnapWidth(1)
	p.Canvas.StrokeRoundedRect(r.Inset(w/2), kit.Stroke{Width: w}, border)
	c.label.SetColor(ink)
}

// suffixed is .suffixed: the retry field with "min" over its end (10 px in, --faint 12 px).
type suffixed struct {
	kit.Element
	input  *widgets.Input
	suffix *views.Text
}

func newSuffixed(in *widgets.Input) *suffixed {
	s := &suffixed{input: in}
	s.Style = kit.Style{Flex: kit.FlexNone}
	s.suffix = views.NewText("min", fontSmall, views.InkFaint, kit.TextOptions{})
	s.suffix.Semantics.Hidden = true
	s.suffix.PassThrough = true
	s.Add(in, s.suffix)
	return s
}

// ContentWidths is kit.Layouter's.
func (s *suffixed) ContentWidths(ctx *kit.LayoutContext) (lo, hi kit.DIP) {
	w, _ := s.input.Style.Width.Resolve(ctx.Scale, -1)
	return w, w
}

// ContentHeight is kit.Layouter's.
func (s *suffixed) ContentHeight(ctx *kit.LayoutContext, w kit.DIP) kit.DIP {
	return ctx.HeightFor(s.input, w, w)
}

// LayoutContent is kit.Layouter's: the suffix centred at the end (inset-inline-end 10px).
func (s *suffixed) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	ctx.Place(s.input, box, box.Width())
	_, sw := ctx.IntrinsicWidths(s.suffix)
	sh := ctx.HeightFor(s.suffix, sw, box.Width())
	x := box.Max.X - 10 - sw
	if ctx.RTL {
		x = box.Min.X + 10
	}
	y := box.Min.Y + (box.Height()-sh)/2
	ctx.Place(s.suffix, kit.R(x, y, x+sw, y+sh), box.Width())
}

// scheduleSection is section.schedule (a size container of its own: in a wide editor it is a
// column beside the settings, so its rows follow its own width).
type scheduleSection struct {
	kit.Element
	d    *Dialog
	card *card

	swRow *qRow
	sw    *widgets.SwitchRow
	hint  *kit.Element // the switch's hint ("Off: …"), shown only while it is off

	times     *qGridRow
	start     *widgets.DateTimeField
	stopCheck *widgets.Checkbox
	stop      *widgets.DateTimeField
	startErr  *views.Text
	stopErr   *views.Text
	timesHint *views.Text

	when    *qRow
	modeBox *kit.Element
	mode    *widgets.SegmentedControl
	days    *kit.Element
	chips   []*dayChip
	once    *widgets.DateTimeField
	daysErr *views.Text
	onceErr *views.Text

	repeatRow *qGridRow
	repeat    *widgets.Input
	repeatErr *views.Text

	wakeRow *qRow
	wake    *widgets.SwitchRow

	disabled bool
}

func newScheduleSection(d *Dialog) *scheduleSection {
	s := &scheduleSection{d: d}
	s.Semantics.Role = kit.RoleRegion
	s.Semantics.Name = "Schedule"
	s.Style = kit.Style{Display: kit.DisplayBlock, RowGap: tokens.Sp4, MinWidth: kit.Px(0), Container: true}

	s.sw = widgets.NewSwitchRow("Start and stop this queue on a schedule", switchHint, false, func(on bool) {
		d.form.ScheduleEnabled = on
		s.showRows()
		s.applyDisabled()
		d.formChanged()
		d.ed.Relayout()
	})
	if kids := s.sw.Children(); len(kids) > 0 && len(kids[0].Children()) > 1 {
		s.hint = kids[0].Children()[1]
	}
	s.swRow = newSwitchQRow(s.sw)

	// the times: Start at | Stop at | the hint
	s.start = widgets.NewDateTimeField(queues.TimeField, "", "Start at", func(v string) {
		d.form.StartTime = v
		s.stop.Fallback = v
		d.formChanged()
	})
	s.startErr = views.NewErrorText("")
	s.startErr.SetHidden(true)
	startLabel := views.NewText("Start at", fontLabel, views.InkInk, kit.TextOptions{})
	startField := box(kit.Style{Display: kit.DisplayBlock, RowGap: tokens.Sp3, MinWidth: kit.Px(0)}, startLabel, s.start, s.startErr)

	s.stopCheck = widgets.NewCheckbox("Stop at", false, func(on bool) {
		d.form.StopEnabled = on
		s.applyDisabled()
		d.formChanged()
	})
	s.stopCheck.Box.Style.Width, s.stopCheck.Box.Style.Height = kit.Px(14), kit.Px(14)
	s.stopCheck.Style.ColumnGap = tokens.Sp3
	f := fontLabel
	f.LineHeight = 1.2
	s.stopCheck.Label.SetFont(f)
	s.stop = widgets.NewDateTimeField(queues.TimeField, "", "Stop time", func(v string) {
		d.form.StopTime = v
		d.formChanged()
	})
	s.stopErr = views.NewErrorText("")
	s.stopErr.SetHidden(true)
	stopField := box(kit.Style{Display: kit.DisplayBlock, RowGap: tokens.Sp3, MinWidth: kit.Px(0)}, s.stopCheck, s.stop, s.stopErr)

	s.timesHint = views.NewHint(timesHint)
	s.timesHint.Style.AlignSelf = kit.AlignEnd
	s.timesHint.Style.Padding = kit.Edges{Bottom: kit.Px(7)}
	s.timesHint.Style.Breakpoints = []kit.Breakpoint{{When: kit.Query{MaxWidth: kit.Rem(36)}, Apply: func(st *kit.Style) {
		st.Padding = kit.Edges{}
		st.AlignSelf = kit.AlignStart
	}}}
	s.times = &qGridRow{}
	s.times.Style = kit.Style{Padding: rowPadding, MinHeight: kit.Px(48), MinWidth: kit.Px(0)}
	s.times.colGap, s.times.rowGap, s.times.align = kit.Px(16), kit.Px(6), kit.AlignStart
	s.times.cols = func(w kit.DIP, sc kit.Scale) []track {
		container := w + 30 // the schedule's width: the row's padding and the card's border
		switch {
		case w < 0:
		case container <= sc.Rem(22):
			return []track{fr1}
		case container <= sc.Rem(36):
			return []track{fr1, fr1}
		}
		return []track{{max: kit.Rem(10)}, {max: kit.Rem(10)}, fr1}
	}
	s.times.full = func(c *kit.Element) bool {
		return c == &s.timesHint.Element && s.times.narrowTimes()
	}
	s.times.Add(startField, stopField, s.timesHint)

	// every week or once
	s.mode = s.newMode()
	s.modeBox = box(kit.Style{Display: kit.DisplayBlock, Flex: kit.FlexNone}, s.mode)
	s.days = box(kit.Style{Display: kit.DisplayFlex, Wrap: true, ColumnGap: tokens.Sp2, RowGap: tokens.Sp2, Flex: kit.FlexOf(0, 1, kit.Length{}), MinWidth: kit.Px(0)})
	s.days.Semantics.Role = kit.RoleGroup
	s.days.Semantics.Name = "Weekdays"
	for _, day := range queues.Weekdays {
		bit := day.Bit
		c := newDayChip(day, func() {
			d.form.Weekdays ^= bit
			s.loadDays()
			d.formChanged()
		})
		s.chips = append(s.chips, c)
		s.days.Add(c)
	}
	s.once = widgets.NewDateTimeField(queues.DateField, "", "Date", func(v string) {
		d.form.OnceDate = v
		d.formChanged()
	})
	s.daysErr = views.NewErrorText("")
	s.daysErr.Style.Flex = kit.FlexOf(0, 1, kit.Pct(100))
	s.daysErr.SetHidden(true)
	s.onceErr = views.NewErrorText("")
	s.onceErr.Style.Flex = kit.FlexOf(0, 1, kit.Pct(100))
	s.onceErr.SetHidden(true)
	s.when = &qRow{}
	s.when.Style = kit.Style{Display: kit.DisplayFlex, Wrap: true, AlignItems: kit.AlignCenter, RowGap: tokens.Sp5, ColumnGap: kit.Px(14),
		MinHeight: kit.Px(48), Padding: rowPadding, MinWidth: kit.Px(0)}
	s.when.Add(s.modeBox, s.days, s.once, s.daysErr, s.onceErr)

	// retry every … minutes
	s.repeat = widgets.NewInput("", widgets.InputOptions{Name: "Retry unfinished downloads every … minutes", Description: repeatHint, Size: widgets.FieldSm, Placeholder: "Off", Align: kit.TextAlignEnd, Number: true}, func(v string) {
		d.form.RepeatMinutes = v
		d.formChanged()
	})
	// .repeat is text-align: end with padding-inline-end 36px (ScheduleEditor.svelte). It is an
	// <input type="number">, so the spin button's room is kept before that padding too (the
	// value and "Off" end 52 CSS px from the border box's end in the 0.9.2 references).
	s.repeat.Style.Width = kit.Rem(7)
	s.repeat.Style.Padding = kit.Edges{Start: kit.Px(11), End: kit.Px(36 + widgets.NumberSpinRoom)}
	repeatText := box(kit.Style{Display: kit.DisplayBlock, RowGap: tokens.Sp1, MinWidth: kit.Px(0)},
		views.NewParagraph("Retry unfinished downloads every … minutes", fontQLabel, views.InkInk), qHint(repeatHint))
	s.repeatErr = views.NewErrorText("")
	s.repeatErr.SetHidden(true)
	s.repeatRow = &qGridRow{}
	s.repeatRow.Style = kit.Style{Padding: rowPadding, MinHeight: kit.Px(48), MinWidth: kit.Px(0)}
	s.repeatRow.cols = fixedCols(fr1, track{auto: true})
	s.repeatRow.colGap, s.repeatRow.rowGap, s.repeatRow.align = kit.Px(20), kit.Px(6), kit.AlignCenter
	s.repeatRow.full = func(c *kit.Element) bool { return c == &s.repeatErr.Element }
	s.repeatRow.Add(repeatText, newSuffixed(s.repeat), s.repeatErr)

	// wake the computer (Windows)
	s.wake = widgets.NewSwitchRow("Wake the computer from sleep to start", "While Downex runs in the background.", false, func(on bool) {
		d.form.WakeComputer = on
		d.formChanged()
	})
	s.wakeRow = newSwitchQRow(s.wake)
	s.wakeRow.SetHidden(!d.caps.WakeTimers)

	s.card = newCard(kit.Style{Display: kit.DisplayBlock}, s.swRow, s.times, s.when, s.repeatRow, s.wakeRow)
	s.Add(title("Schedule"), s.card)
	return s
}

// narrowTimes reports whether the times row is two columns or one (the hint under them).
func (g *qGridRow) narrowTimes() bool { return len(g.widths) < 3 }

func (s *scheduleSection) newMode() *widgets.SegmentedControl {
	d := s.d
	return widgets.NewSegmentedControl([]widgets.SegmentOption{{Value: string(queues.ModeDays), Label: "Every week"}, {Value: string(queues.ModeOnce), Label: "Once"}},
		string(d.form.Mode), widgets.SegmentedOptions{Legend: "Repeat", Small: true, Disabled: s.disabled}, func(v string) {
			d.form.Mode = queues.DaysMode(v)
			s.showMode()
			d.formChanged()
		})
}

// load shows the form's schedule.
func (s *scheduleSection) load(f queues.Form) {
	s.sw.Switch.SetOn(f.ScheduleEnabled)
	s.start.SetText(f.StartTime)
	s.stopCheck.SetChecked(f.StopEnabled)
	s.stop.SetText(f.StopTime)
	s.stop.Fallback = f.StartTime
	s.mode.SetValue(string(f.Mode))
	s.loadDays()
	s.once.SetText(f.OnceDate)
	s.repeat.SetText(f.RepeatMinutes)
	s.wake.Switch.SetOn(f.WakeComputer)
	s.showRows()
	s.applyDisabled()
}

func (s *scheduleSection) loadDays() {
	for _, c := range s.chips {
		c.setPressed(s.d.form.Weekdays&c.bit != 0)
	}
}

// showRows shows the rows while the schedule is on (the switch's hint while it is off).
func (s *scheduleSection) showRows() {
	on := s.d.form.ScheduleEnabled
	if s.hint != nil {
		s.hint.SetHidden(on)
	}
	desc := switchHint
	if on {
		desc = ""
	}
	if s.sw.Switch.Semantics.Description != desc {
		s.sw.Switch.Semantics.Description = desc
		s.sw.Switch.SemanticsChanged()
	}
	s.times.SetHidden(!on)
	s.when.SetHidden(!on)
	s.repeatRow.SetHidden(!on)
	s.wakeRow.SetHidden(!on || !s.d.caps.WakeTimers)
	s.showMode()
	s.borders()
}

func (s *scheduleSection) showMode() {
	days := s.d.form.Mode == queues.ModeDays
	s.days.SetHidden(!days)
	s.once.SetHidden(days)
	s.showErrors(s.d.errs, s.d.form)
}

// borders puts the --line-soft between the rows shown.
func (s *scheduleSection) borders() {
	first := true
	for _, r := range []topper{s.swRow, s.times, s.when, s.repeatRow, s.wakeRow} {
		if r.Base().Style.Hidden {
			continue
		}
		r.setTop(!first)
		first = false
	}
}

func (s *scheduleSection) setDisabled(d bool) {
	if d == s.disabled {
		return
	}
	s.disabled = d
	// The segmented control takes its disabled state when it is made.
	mode := s.newMode()
	s.modeBox.SetChildren(mode)
	s.mode = mode
	s.applyDisabled()
}

// applyDisabled: off = disabled or the schedule off; the stop time needs its check box.
func (s *scheduleSection) applyDisabled() {
	off := s.disabled || !s.d.form.ScheduleEnabled
	s.sw.SetDisabled(s.disabled)
	s.start.SetDisabled(off)
	s.stopCheck.SetDisabled(off)
	s.stop.SetDisabled(off || !s.d.form.StopEnabled)
	for _, c := range s.chips {
		if c.Disabled != off {
			c.Disabled = off
			c.Cursor = kit.CursorPointer
			if off {
				c.Cursor = kit.CursorDefault
			}
			c.Invalidate()
			c.SemanticsChanged()
		}
	}
	s.once.SetDisabled(off)
	s.repeat.SetDisabled(off)
	s.wake.SetDisabled(off)
}

// showErrors shows the schedule's messages (each in its row).
func (s *scheduleSection) showErrors(e queues.FormErrors, f queues.Form) {
	setError(s.startErr, e.StartTime)
	s.start.SetInvalid(e.StartTime != "")
	s.start.SetDescription(e.StartTime)
	setError(s.stopErr, e.StopTime)
	s.stop.SetInvalid(e.StopTime != "")
	stopDesc := timesHint
	if e.StopTime != "" {
		stopDesc += " " + e.StopTime
	}
	s.stop.SetDescription(stopDesc)
	days := ""
	if f.Mode == queues.ModeDays {
		days = e.Days
	}
	setError(s.daysErr, days)
	s.days.Semantics.Description = days
	once := ""
	if f.Mode == queues.ModeOnce {
		once = e.OnceDate
	}
	setError(s.onceErr, once)
	s.once.SetInvalid(once != "")
	s.once.SetDescription(once)
	setError(s.repeatErr, e.RepeatMinutes)
	s.repeat.SetInvalid(e.RepeatMinutes != "")
	repeatDesc := repeatHint
	if e.RepeatMinutes != "" {
		repeatDesc += " " + e.RepeatMinutes
	}
	s.repeat.Semantics.Description = repeatDesc
}

// firstInvalid is the schedule's first field that needs attention.
func (s *scheduleSection) firstInvalid(e queues.FormErrors, f queues.Form) kit.Widget {
	switch {
	case e.StartTime != "":
		return s.start
	case e.StopTime != "":
		return s.stop
	case e.OnceDate != "" && f.Mode == queues.ModeOnce:
		return s.once
	case e.RepeatMinutes != "":
		return s.repeat
	}
	return nil
}
