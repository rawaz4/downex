package queues

import (
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format"
	"downex/ui/core/queues"
	"downex/ui/core/text"
	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/views"
)

// The editor's head (the queue's name, its state and Start/Stop, the schedule's summary) and
// its settings, and how the dialog shows its state in its widgets.

// headSection is section.head: the heading line (10 px gaps) over the summary (4 px apart).
type headSection struct {
	kit.Element
	h3      *views.Text
	pill    *widgets.Pill
	pillDot *dot
	run     *widgets.Button
	clock   *widgets.Icon
	summary *views.Text
	state   string // what the run button shows now
}

func newHeadSection(onRun func()) *headSection {
	h := &headSection{}
	h.Semantics.Role = kit.RoleRegion
	h.Style = kit.Style{Display: kit.DisplayBlock, RowGap: tokens.Sp2, MinWidth: kit.Px(0)}
	h.h3 = views.NewText("", views.FontPage, views.InkInk, kit.TextOptions{Wrap: kit.TextWrapAnywhere})
	h.h3.Semantics.Role = kit.RoleHeading
	h.h3.Semantics.Level = 3
	h.h3.Style.MinWidth = kit.Px(0)
	h.pill = widgets.NewPill("Stopped", widgets.Done)
	h.pillDot = newDot(views.InkAccent)
	h.pill.Insert(0, h.pillDot)
	h.run = widgets.NewButton("Start", "resume", widgets.Secondary, widgets.SizeSm, onRun)
	h.run.SetIconSize(14)
	h.run.Style.Flex = kit.FlexNone
	h.run.Style.Margin = kit.Edges{Start: kit.Auto}
	line := box(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: tokens.Sp5, MinWidth: kit.Px(0)}, h.h3, h.pill, h.run)
	h.clock = widgets.NewIcon(icons.Clock, 13, func(t *tokens.Theme) kit.Color { return t.Faint })
	h.summary = qHint("")
	h.summary.Style.MinWidth = kit.Px(0)
	h.summary.Style.Flex = kit.FlexOf(0, 1, kit.Length{})
	sum := box(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: tokens.Sp3, MinWidth: kit.Px(0)}, h.clock, h.summary)
	h.Add(line, sum)
	return h
}

func (h *headSection) set(q *downexv1.Queue, busy busyKind, locked bool) {
	if q == nil {
		h.h3.SetText("New queue")
		h.Semantics.Name = "New queue"
		h.pill.SetHidden(true)
		h.run.SetHidden(true)
		h.clock.SetHidden(true)
		h.summary.SetText("A new queue starts stopped; add downloads to it from the list or when you add them.")
		return
	}
	name := text.EscapeBidi(q.GetName())
	h.h3.SetText(name)
	h.Semantics.Name = name
	h.pill.SetHidden(false)
	h.run.SetHidden(false)
	h.clock.SetHidden(false)
	running := q.GetRunning()
	h.pillDot.SetHidden(!running)
	if running {
		h.pill.SetText("Running")
		h.pill.SetTone(widgets.Ok)
	} else {
		h.pill.SetText("Stopped")
		h.pill.SetTone(widgets.Done)
	}
	label := "Start"
	switch {
	case busy == busyRun && running:
		label = "Stopping…"
	case busy == busyRun:
		label = "Starting…"
	case running:
		label = "Stop"
	}
	if label != h.state {
		h.state = label
		h.run.SetText(label)
		ic := icons.Resume
		if running {
			ic = icons.Pause
		}
		h.run.IconWidget().SetName(ic)
	}
	h.run.SetDisabled(locked, false)
	s := queues.ScheduleSummary(q.GetSchedule())
	if q.GetIsMain() {
		s += " · The main queue cannot be deleted."
	}
	h.summary.SetText(s)
}

// field is .fields .field: a label (nowrap) over its control, with its error under it, 6 px
// apart.
type field struct {
	kit.Element
	err *views.Text
}

func newField(label kit.Widget, control kit.Widget) *field {
	f := &field{}
	f.Style = kit.Style{Display: kit.DisplayBlock, RowGap: tokens.Sp3, MinWidth: kit.Px(0)}
	f.err = views.NewErrorText("")
	f.err.SetHidden(true)
	f.Add(label, control, f.err)
	return f
}

// fieldLabel is .fields label: 600 var(--type-cell) --ink, on one line.
func fieldLabel(s string) *views.Text {
	return views.NewText(s, fontLabel, views.InkInk, kit.TextOptions{})
}

// labelRow is .fields .label-row: the label and its hint, the hint under the label when the
// column is narrow (flex-wrap: wrap; row-gap 0).
func labelRow(label, hint string) *kit.Element { return views.NewLabelRow(label, views.NewHint(hint)) }

func setError(t *views.Text, s string) {
	t.SetText(s)
	t.SetHidden(s == "")
}

// settingsSection is section.settings: the Settings title and .q-card.fields (two columns,
// 14/16 px gaps, 14 px padding; one column in a narrow editor).
type settingsSection struct {
	kit.Element
	grid                        *cardGrid
	name, concurrent, speed     *widgets.Input
	unit, after                 *widgets.Select
	fName, fConc, fSpeed, fWhen *field
}

// postActionNames are the actions' values in the Select (api.ts's PostAction names).
var postActionNames = map[downexv1.PostAction]string{
	downexv1.PostAction_POST_ACTION_UNSPECIFIED: "none",
	downexv1.PostAction_POST_ACTION_QUIT_DOWNEX: "quit",
	downexv1.PostAction_POST_ACTION_SLEEP:       "sleep",
	downexv1.PostAction_POST_ACTION_HIBERNATE:   "hibernate",
	downexv1.PostAction_POST_ACTION_SHUTDOWN:    "shutdown",
}

func postActionOf(name string) downexv1.PostAction {
	for a, n := range postActionNames {
		if n == name {
			return a
		}
	}
	return downexv1.PostAction_POST_ACTION_UNSPECIFIED
}

func unitOptions() []widgets.SelectOption {
	var out []widgets.SelectOption
	for _, u := range format.RateUnits() {
		out = append(out, widgets.SelectOption{Value: string(u), Label: string(u)})
	}
	return out
}

func newSettingsSection(d *Dialog) *settingsSection {
	s := &settingsSection{}
	s.Style = kit.Style{Display: kit.DisplayBlock, RowGap: tokens.Sp4, MinWidth: kit.Px(0)}
	s.Semantics.Role = kit.RoleRegion
	s.Semantics.Name = "Settings"

	s.name = widgets.NewInput("", widgets.InputOptions{Name: "Name", Size: widgets.FieldSm, AutoDirection: true}, func(v string) {
		d.form.Name = v
		d.formChanged()
	})
	s.name.Autofocus = true // data-autofocus
	s.fName = newField(fieldLabel("Name"), s.name)

	s.concurrent = widgets.NewInput("", widgets.InputOptions{Name: "Simultaneous downloads", Description: "0 = the global setting", Size: widgets.FieldSm, Number: true}, func(v string) {
		d.form.MaxConcurrent = v
		d.formChanged()
	})
	s.fConc = newField(labelRow("Simultaneous downloads", "0 = the global setting"), s.concurrent)

	s.speed = widgets.NewInput("", widgets.InputOptions{Name: "Speed limit of the queue", Size: widgets.FieldSm, Placeholder: "No limit"}, func(v string) {
		d.form.SpeedValue = v
		d.formChanged()
	})
	s.speed.Style.Flex = kit.FlexAuto
	s.speed.Style.Width = kit.Length{}
	s.unit = widgets.NewSelect(unitOptions(), "", widgets.SelectOptions{AriaLabel: "Queue speed limit unit", Size: widgets.SelectSm}, func(v string) {
		d.form.SpeedUnit = format.RateUnit(v)
		d.formChanged()
	})
	unit := box(kit.Style{Display: kit.DisplayBlock, Flex: kit.FlexOf(0, 0, kit.Rem(6))}, s.unit)
	rate := box(kit.Style{Display: kit.DisplayFlex, ColumnGap: tokens.Sp3, MinWidth: kit.Px(0)}, s.speed, unit)
	s.fSpeed = newField(fieldLabel("Speed limit of the queue"), rate)

	s.after = widgets.NewSelect(nil, "", widgets.SelectOptions{Label: "When the queue is done", Size: widgets.SelectSm, Icon: "power"}, func(v string) {
		d.form.PostAction = postActionOf(v)
		d.formChanged()
	})
	s.after.Semantics.Description = "Can be cancelled for 60 s"
	s.fWhen = newField(labelRow("When the queue is done", "Can be cancelled for 60 s"), s.after)

	g := &cardGrid{}
	g.cols = func(_ kit.DIP, sc kit.Scale) []track {
		if d.ed != nil && d.ed.cw > 0 && d.ed.cw <= sc.Rem(30) {
			return []track{fr1}
		}
		return []track{fr1, fr1}
	}
	g.colGap, g.rowGap, g.align = kit.Px(16), kit.Px(14), kit.AlignStart
	g.Style = kit.Style{Padding: kit.EdgesAll(kit.Px(14)), Border: kit.EdgesAll(kit.Px(1)), MinWidth: kit.Px(0)}
	g.Add(s.fName, s.fConc, s.fSpeed, s.fWhen)
	s.grid = g
	s.Add(title("Settings"), g)
	return s
}

// build makes the dialog's widgets.
func (d *Dialog) build() {
	d.qlist = newQueueList(d.newQueue)
	d.ed = newEditor(d)
	d.head = newHeadSection(d.toggleRunning)
	d.settings = newSettingsSection(d)
	d.sched = newScheduleSection(d)
	d.order = newOrderSection(d)
	d.confirm = views.NewNote(views.NoteFail, "Delete")
	d.confirm.Text.Ink = views.InkDanger
	d.confirm.Semantics.Role = kit.RoleAlert
	d.confirm.SetHidden(true)
	d.ed.set(d.head, d.settings, d.sched, d.order, d.confirm)
	d.shell = newShell(d, d.qlist, d.ed)
	d.Body.Add(d.shell)

	d.delBtn = widgets.NewButton("Delete queue…", "remove", widgets.DangerText, widgets.SizeFoot, d.remove)
	d.keepBtn = widgets.NewButton("Keep it", "", widgets.Secondary, widgets.SizeFoot, d.keep)
	d.delGroup = box(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: tokens.Sp3}, d.delBtn, d.keepBtn)
	d.attention = newFootMsg()
	d.failMsg = newFootMsg()
	start := box(kit.Style{Display: kit.DisplayBlock, MinWidth: kit.Px(0)}, d.delGroup, d.attention, d.failMsg)
	start.Semantics.Role = kit.RoleNone
	d.closeBtn = widgets.NewButton("Close", "", widgets.Secondary, widgets.SizeMd, d.Close)
	d.saveBtn = widgets.NewButton("Create queue", "", widgets.Primary, widgets.SizeMd, d.submit)
	d.saveBtn.Semantics.States |= kit.NodeDefault
	d.SetFooter(start, d.closeBtn, d.saveBtn)
}

// loadForm shows the form in the fields (after a queue was chosen or saved).
func (d *Dialog) loadForm() {
	f := d.form
	s := d.settings
	s.name.SetText(f.Name)
	s.concurrent.SetText(f.MaxConcurrent)
	s.speed.SetText(f.SpeedValue)
	s.unit.SetValue(string(f.SpeedUnit))
	var current downexv1.PostAction
	if q := d.queue(); q != nil {
		current = q.GetPostAction()
	}
	var choices []widgets.SelectOption
	for _, a := range queues.PostActionChoices(d.caps, current) {
		choices = append(choices, widgets.SelectOption{Value: postActionNames[a], Label: queues.PostActionLabel(a)})
	}
	s.after.SetOptions(choices)
	s.after.SetValue(postActionNames[f.PostAction])
	d.sched.load(f)
	d.validate()
}

// refresh shows the dialog's state: the list, the head, what is locked, the order, the
// errors, the inline confirmation and the footer.
func (d *Dialog) refresh() {
	q := d.queue()
	locked := d.locked()
	d.refreshList()
	d.head.set(q, d.busy, locked)
	stale := d.stale
	s := d.settings
	s.name.SetDisabled(stale)
	s.concurrent.SetDisabled(stale)
	s.speed.SetDisabled(stale)
	s.unit.SetDisabled(stale)
	s.after.SetDisabled(stale)
	d.sched.setDisabled(stale)
	d.order.refresh()
	d.validate()
	d.refreshErrors()
	if d.confirmDelete && q != nil {
		d.confirm.Text.SetText("Delete " + text.EscapeBidi(q.GetName()) + "? Its downloads move to the end of the main queue.")
		d.confirm.Semantics.Name = d.confirm.Text.Text()
		d.confirm.SetHidden(false)
	} else {
		d.confirm.SetHidden(true)
	}
	d.refreshFooter()
	d.SetBusy(d.busy != notBusy)
}

func (d *Dialog) refreshList() {
	l := d.qlist
	if len(l.rows) != len(d.all) || !sameIDs(l.rows, d.all) {
		l.rows = l.rows[:0]
		for _, q := range d.all {
			id := q.GetId()
			l.rows = append(l.rows, newQueueRow(id, func() { d.selectQueue(id) }))
		}
		ws := make([]kit.Widget, 0, len(l.rows)+1)
		for _, r := range l.rows {
			ws = append(ws, listItem(r))
		}
		if d.selected == 0 {
			if l.draft == nil {
				l.draft = newQueueRow(0, nil)
			}
			ws = append(ws, listItem(l.draft))
		}
		l.ul.SetChildren(ws...)
		if l.narrow {
			l.narrow = false
			l.setNarrow(true)
		}
	}
	for i, q := range d.all {
		l.rows[i].set(q, d.pendingCount(q), d.selected == q.GetId())
	}
	switch {
	case d.selected == 0 && (l.draft == nil || l.draft.Parent() == nil):
		if l.draft == nil {
			l.draft = newQueueRow(0, nil)
		}
		l.ul.Add(listItem(l.draft))
	case d.selected != 0 && l.draft != nil && l.draft.Parent() != nil:
		l.ul.Remove(l.draft.Parent().Widget())
	}
	if l.draft != nil {
		l.draft.set(nil, 0, true)
	}
	l.newBtn.SetDisabled(d.locked(), false)
}

// listItem is the row's li.
func listItem(r *queueRow) *kit.Element {
	li := box(kit.Style{Display: kit.DisplayBlock, MinWidth: kit.Px(0)}, r)
	li.Semantics.Role = kit.RoleListItem
	return li
}

func sameIDs(rows []*queueRow, qs []*downexv1.Queue) bool {
	for i, q := range qs {
		if rows[i].id != q.GetId() {
			return false
		}
	}
	return true
}

// refreshErrors shows the messages of the fields that need attention (once a save was tried).
func (d *Dialog) refreshErrors() {
	e := d.errs
	s := d.settings
	setError(s.fName.err, e.Name)
	s.name.SetInvalid(e.Name != "")
	s.name.Semantics.Description = e.Name
	setError(s.fConc.err, e.MaxConcurrent)
	s.concurrent.SetInvalid(e.MaxConcurrent != "")
	s.concurrent.Semantics.Description = firstOf(e.MaxConcurrent, "0 = the global setting")
	setError(s.fSpeed.err, e.Speed)
	s.speed.SetInvalid(e.Speed != "")
	s.speed.Semantics.Description = e.Speed
	setError(s.fWhen.err, e.PostAction)
	s.after.SetInvalid(e.PostAction != "")
	s.after.Semantics.Description = firstOf(e.PostAction, "Can be cancelled for 60 s")
	d.sched.showErrors(e, d.form)
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// firstInvalid is the first field that needs attention, in the page's order.
func (d *Dialog) firstInvalid() kit.Widget {
	e := d.errs
	s := d.settings
	switch {
	case e.Name != "":
		return s.name
	case e.MaxConcurrent != "":
		return s.concurrent
	case e.Speed != "":
		return s.speed
	case e.PostAction != "":
		return s.after
	}
	return d.sched.firstInvalid(e, d.form)
}

// refreshFooter shows Delete (with its confirmation), the messages and the buttons' states.
func (d *Dialog) refreshFooter() {
	q := d.queue()
	locked := d.locked()
	d.delGroup.SetHidden(q == nil || q.GetIsMain())
	label := "Delete queue…"
	switch {
	case d.busy == busyDelete:
		label = "Deleting…"
	case d.confirmDelete:
		label = "Delete queue now"
	}
	d.delBtn.SetText(label)
	d.delBtn.SetDisabled(locked, false)
	d.keepBtn.SetHidden(!d.confirmDelete)
	d.keepBtn.SetDisabled(d.busy != notBusy, false)
	attention := ""
	if d.attempted && !d.valid {
		attention = "Some fields need attention."
	}
	d.attention.set(attention)
	d.failMsg.set(text.EscapeBidi(d.failure))
	d.closeBtn.SetDisabled(d.busy != notBusy, false)
	save := "Create queue"
	switch {
	case d.busy == busySave:
		save = "Saving…"
	case q != nil:
		save = "Save queue"
	}
	d.saveBtn.SetText(save)
	d.saveBtn.SetDisabled(locked, false)
}
