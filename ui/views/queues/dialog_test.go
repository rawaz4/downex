package queues

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format/formattest"
	"downex/ui/kit"
	"downex/ui/kit/hostcap/hostcaptest"
	"downex/ui/kit/widgets"
	"downex/ui/state/statetest"
	"downex/ui/views"
	"downex/ui/views/viewstest"
)

// QueueDialog.test.ts, case by case (its describe blocks "QueueDialog", "QueueDialog: the
// restyled editor (DESIGN-SYSTEM.md §4.10)" and "QueueDialog: sizing (DESIGN-SYSTEM.md §3.9)").
// The file's MoveToQueueDialog cases are the Move to queue dialog's (views/dialogs). The
// DateTimeField.test.ts cases are ui/core/queues' TestDateTimeFieldRules and kit/widgets'
// TestDateTimeFieldKeyboard (the field's owners); here the field is used inside the schedule.

const (
	stQueued    = downexv1.DownloadState_DOWNLOAD_STATE_QUEUED
	stCompleted = downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED
	stPaused    = downexv1.DownloadState_DOWNLOAD_STATE_PAUSED
)

func download(id int64, name string, st downexv1.DownloadState) *downexv1.Download {
	return formattest.Download(id, func(d *downexv1.Download) { d.Filename, d.State = name, st })
}

type rig struct {
	t    *testing.T
	h    *viewstest.Harness
	d    *Dialog
	fake *statetest.Daemon
}

type setupOptions struct {
	// night edits the second queue ("Night", when done: sleep) before it is made.
	night   func(q *downexv1.Queue)
	main    func(q *downexv1.Queue)
	noCaps  bool
	initial int64
	width   int
	height  int
}

// setup is the test file's setup(): queues 1 (10, 11, 12 completed, 13 paused) and 2 Night.
func setup(t *testing.T, o setupOptions) *rig {
	t.Helper()
	fake := statetest.NewDaemon(
		download(10, "first.iso", stQueued),
		download(11, "second.iso", stQueued),
		download(12, "done.iso", stCompleted),
		download(13, "third.iso", stPaused),
	)
	night := &downexv1.Queue{Name: "Night", PostAction: downexv1.PostAction_POST_ACTION_SLEEP, Schedule: &downexv1.Schedule{}}
	if o.night != nil {
		o.night(night)
	}
	if _, err := fake.CreateQueue(context.Background(), &downexv1.CreateQueueRequest{Queue: night}); err != nil {
		t.Fatal(err)
	}
	if o.main != nil {
		m := statetest.Queue(1, func(q *downexv1.Queue) { q.DownloadIds = []int64{10, 11, 12, 13} })
		o.main(m)
		fake.SetQueue(m)
	}
	fake.ResetCalls()
	w, h := o.width, o.height
	if w == 0 {
		w, h = 1920, 1080
	}
	hr := viewstest.New(t, viewstest.Options{Width: w, Height: h, Daemon: fake})
	hr.App.Caps.Hibernate, hr.App.Caps.WakeTimers = !o.noCaps, !o.noCaps
	d := New(hr.App, views.QueuesRequest{Queue: o.initial})
	hr.App.Dialogs.Show(d)
	hr.Settle()
	return &rig{t: t, h: hr, d: d, fake: fake}
}

// click scrolls e into view (as a pointer user would) and clicks it.
func (r *rig) click(w kit.Widget) {
	r.t.Helper()
	e := w.Base()
	if e.Style.Hidden || e.Window() == nil {
		r.t.Fatalf("click: %T is not shown", w)
	}
	r.h.Window.ScrollIntoView(e)
	r.h.Settle()
	r.h.Click(e)
}

// typeIn replaces a field's text, as fireEvent.input does.
func (r *rig) typeIn(in *widgets.Input, s string) {
	r.t.Helper()
	f, ok := in.Native().(*hostcaptest.Field)
	if !ok {
		r.t.Fatalf("no native field for %q", in.Semantics.Name)
	}
	f.Replace(s)
	r.h.Settle()
}

func (r *rig) key(k kit.Key) { r.h.Key(k, 0) }

// choose picks an option of a Select from the keyboard: open it, go to the option, Enter.
func (r *rig) choose(s *widgets.Select, value string) {
	r.t.Helper()
	i := slices.IndexFunc(s.Options, func(o widgets.SelectOption) bool { return o.Value == value })
	if i < 0 {
		r.t.Fatalf("no option %q in %v", value, optionValues(s))
	}
	r.h.Window.Focus(s)
	r.key(kit.KeyArrowDown) // opens on the chosen option
	r.key(kit.KeyHome)
	for range i {
		r.key(kit.KeyArrowDown)
	}
	r.key(kit.KeyEnter)
	if s.Value() != value {
		r.t.Fatalf("chose %q, the select holds %q", value, s.Value())
	}
}

func optionValues(s *widgets.Select) []string {
	var out []string
	for _, o := range s.Options {
		out = append(out, o.Value)
	}
	return out
}

func optionLabels(s *widgets.Select) []string {
	var out []string
	for _, o := range s.Options {
		out = append(out, o.Label)
	}
	return out
}

func (r *rig) chip(long string) *dayChip {
	for _, c := range r.d.sched.chips {
		if c.Semantics.Name == long {
			return c
		}
	}
	r.t.Fatalf("no weekday %q", long)
	return nil
}

// orderRows are the order's rows, in order.
func (r *rig) orderRows() []*orderRow {
	var out []*orderRow
	for _, c := range r.d.order.list.Children() {
		if row, ok := c.Widget().(*orderRow); ok {
			out = append(out, row)
		}
	}
	return out
}

func (r *rig) orderNames() []string {
	var out []string
	for _, row := range r.orderRows() {
		out = append(out, row.name.Text())
	}
	return out
}

// button is the arrow named name ("Move second.iso up").
func (r *rig) arrow(name string) *widgets.IconButton {
	for _, row := range r.orderRows() {
		for _, b := range []*widgets.IconButton{row.up, row.down} {
			if b.Semantics.Name == name {
				return b
			}
		}
	}
	r.t.Fatalf("no button %q", name)
	return nil
}

func (r *rig) heading() string { return r.d.head.h3.Text() }

func (r *rig) moveArgs() (ids []int64, queue int64, position int32) {
	a := r.fake.Args("moveDownloads")
	if len(a) != 3 {
		r.t.Fatalf("moveDownloads args %v", a)
	}
	return a[0].([]int64), a[1].(int64), a[2].(int32)
}

func (r *rig) wantMove(ids []int64, queue int64, position int32) {
	r.t.Helper()
	gi, gq, gp := r.moveArgs()
	if !slices.Equal(gi, ids) || gq != queue || gp != position {
		r.t.Fatalf("MoveDownloads(%v, %d, %d), want (%v, %d, %d)", gi, gq, gp, ids, queue, position)
	}
}

func (r *rig) focused() *kit.Element { return r.h.Window.Focused() }

// drag presses a row, moves to y in another one (or the list's end) and releases; marked is
// whether a drop place was marked just before the release.
func (r *rig) drag(from *orderRow, to *kit.Element, yShare float32) (marked bool) {
	r.t.Helper()
	at := from.name.WindowBounds().Center()
	b := to.WindowBounds()
	over := kit.Pt(b.Min.X+40, b.Min.Y+b.Height()*yShare)
	l := r.h.Loop
	l.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: at})
	l.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: at, Button: kit.ButtonLeft, Buttons: kit.ButtonLeft, Clicks: 1})
	l.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: kit.Pt(at.X, at.Y+(over.Y-at.Y)/2), Buttons: kit.ButtonLeft})
	l.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: over, Buttons: kit.ButtonLeft})
	r.h.Settle()
	marked = r.d.order.list.dropAt != noDrop
	l.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: over, Button: kit.ButtonLeft})
	r.h.Settle()
	return marked
}

func TestQueueDialog(t *testing.T) {
	t.Run("creates a queue with a schedule and an action", func(t *testing.T) {
		r := setup(t, setupOptions{})
		s, sc := r.d.settings, r.d.sched
		r.click(r.d.qlist.newBtn)
		if r.focused() != &s.name.Element {
			t.Fatalf("focus on %v, want the name", r.focused())
		}
		r.typeIn(s.name, "Overnight")
		r.typeIn(s.concurrent, "2")
		r.typeIn(s.speed, "512")
		r.choose(s.unit, "KB/s")
		r.choose(s.after, "shutdown")
		r.click(sc.sw)
		r.typeIn(&sc.start.Input, "23:30")
		r.click(sc.stopCheck)
		r.typeIn(&sc.stop.Input, "06:45")
		// Every day is proposed: keep Monday to Friday.
		r.click(r.chip("Saturday"))
		r.click(r.chip("Sunday"))
		if r.chip("Sunday").pressed {
			t.Fatal("Sunday is still pressed")
		}
		r.typeIn(sc.repeat, "30")
		r.click(sc.wake)
		r.click(r.d.saveBtn)
		if r.fake.Count("createQueue") != 1 {
			t.Fatalf("calls %v", r.fake.Calls())
		}
		spec := r.fake.Args("createQueue")[0].(*downexv1.Queue)
		want := &downexv1.Queue{Name: "Overnight", MaxConcurrent: 2, SpeedLimit: 512 * 1000, PostAction: downexv1.PostAction_POST_ACTION_SHUTDOWN,
			Schedule: &downexv1.Schedule{Enabled: true, StartMinute: 1410, StopEnabled: true, StopMinute: 405, Weekdays: 0x1f, OnceDate: "", WakeComputer: true, RepeatMinutes: 30}}
		if spec.String() != want.String() {
			t.Fatalf("CreateQueue(%v), want %v", spec, want)
		}
		// The new queue is selected and can be started.
		if r.heading() != "Overnight" || r.d.head.run.Label().Text() != "Start" || r.d.head.run.Style.Hidden {
			t.Fatalf("heading %q, run %q", r.heading(), r.d.head.run.Label().Text())
		}
	})

	t.Run("shows what needs attention and saves nothing", func(t *testing.T) {
		r := setup(t, setupOptions{initial: 2})
		sc := r.d.sched
		r.typeIn(r.d.settings.name, "main queue")
		r.click(sc.sw)
		r.click(segment(r.d, "Once"))
		r.typeIn(&sc.once.Input, "")
		r.click(r.d.saveBtn)
		if got := r.d.settings.fName.err; got.Style.Hidden || got.Text() != "Another queue has this name" {
			t.Fatalf("name error %q", got.Text())
		}
		if sc.onceErr.Style.Hidden || sc.onceErr.Text() != "Choose a date" {
			t.Fatalf("date error %q", sc.onceErr.Text())
		}
		if r.focused() != &r.d.settings.name.Element {
			t.Fatalf("focus %v, want the name", r.focused())
		}
		if r.fake.Count("updateQueue") != 0 {
			t.Fatal("saved")
		}
		if r.d.attention.Style.Hidden || r.d.attention.text.Text() != "Some fields need attention." {
			t.Fatal("no footer message")
		}
	})

	t.Run("offers only what the system can do", func(t *testing.T) {
		r := setup(t, setupOptions{noCaps: true, initial: 2})
		got := optionLabels(r.d.settings.after)
		want := []string{"Keep running", "Quit Downex", "Put the computer to sleep", "Shut down the computer"}
		if !slices.Equal(got, want) {
			t.Fatalf("options %q", got)
		}
		r.click(r.d.sched.sw)
		if r.d.sched.times.Style.Hidden {
			t.Fatal("no Start at")
		}
		if !r.d.sched.wakeRow.Style.Hidden {
			t.Fatal("Wake the computer is offered")
		}
	})

	t.Run("starts and stops a queue, and deletes it after asking", func(t *testing.T) {
		r := setup(t, setupOptions{initial: 2})
		if r.d.head.pill.Style.Hidden || textOf(&r.d.head.pill.Element) != "Stopped" {
			t.Fatalf("pill %q", textOf(&r.d.head.pill.Element))
		}
		r.click(r.d.head.run)
		if a := r.fake.Args("startQueue"); len(a) != 1 || a[0].(int64) != 2 {
			t.Fatalf("startQueue %v", a)
		}
		if r.d.head.run.Label().Text() != "Stop" {
			t.Fatalf("run %q", r.d.head.run.Label().Text())
		}
		r.click(r.d.head.run)
		if a := r.fake.Args("stopQueue"); len(a) != 1 || a[0].(int64) != 2 {
			t.Fatalf("stopQueue %v", a)
		}

		if r.d.delBtn.Label().Text() != "Delete queue…" {
			t.Fatalf("delete %q", r.d.delBtn.Label().Text())
		}
		r.click(r.d.delBtn)
		if r.d.confirm.Style.Hidden || r.d.confirm.Semantics.Role != kit.RoleAlert ||
			!strings.Contains(r.d.confirm.Text.Text(), "Its downloads move to the end of the main queue") {
			t.Fatalf("confirmation %q", r.d.confirm.Text.Text())
		}
		if r.fake.Count("deleteQueue") != 0 {
			t.Fatal("deleted without asking")
		}
		if r.d.delBtn.Label().Text() != "Delete queue now" || r.d.keepBtn.Style.Hidden {
			t.Fatal("no confirmation buttons")
		}
		r.click(r.d.delBtn)
		if a := r.fake.Args("deleteQueue"); len(a) != 1 || a[0].(int64) != 2 {
			t.Fatalf("deleteQueue %v", a)
		}
		// Back on the main queue, which cannot be deleted.
		if r.heading() != "Main queue" {
			t.Fatalf("heading %q", r.heading())
		}
		if !r.d.delGroup.Style.Hidden {
			t.Fatal("the main queue offers Delete")
		}
	})

	t.Run("reorders the queue with the arrows and by dragging", func(t *testing.T) {
		r := setup(t, setupOptions{initial: 1})
		// Completed downloads are not part of the order shown.
		if got := r.orderNames(); !slices.Equal(got, []string{"first.iso", "second.iso", "third.iso"}) {
			t.Fatalf("order %q", got)
		}
		r.click(r.arrow("Move second.iso up"))
		r.wantMove([]int64{11}, 1, 0)
		if got := r.orderNames(); got[0] != "second.iso" {
			t.Fatalf("order %q", got)
		}
		// At the top its Up arrow is off: the keyboard stays on the row, on its other arrow.
		if r.focused() != &r.arrow("Move second.iso down").Element {
			t.Fatalf("focus %v", r.focused())
		}

		// Down from the first row: after first.iso, which is followed by the completed one.
		r.click(r.arrow("Move second.iso down"))
		r.wantMove([]int64{11}, 1, 1)

		// Drag third.iso onto the top half of the first row.
		rows := r.orderRows()
		r.drag(rows[2], &rows[0].Element, 0.1)
		r.wantMove([]int64{13}, 1, 0)
	})

	t.Run("drops a row below another by where the pointer is, and onto itself or its own place as nothing", func(t *testing.T) {
		r := setup(t, setupOptions{initial: 1})
		rows := r.orderRows()
		// third.iso onto the lower half of its own row, and of the row above it: it stays.
		if r.drag(rows[2], &rows[2].Element, 0.9) || r.drag(rows[2], &rows[1].Element, 0.9) {
			t.Fatal("a place was marked where the row stays")
		}
		// first.iso onto the upper half of its own row and of the next one.
		if r.drag(rows[0], &rows[0].Element, 0.1) || r.drag(rows[0], &rows[1].Element, 0.1) {
			t.Fatal("a place was marked where the row stays")
		}
		if n := r.fake.Count("moveDownloads"); n != 0 {
			t.Fatalf("%d moves", n)
		}
		// first.iso onto the lower half of second.iso: after it (and the completed one), before third.iso.
		if !r.drag(rows[0], &rows[1].Element, 0.9) {
			t.Fatal("no place marked")
		}
		r.wantMove([]int64{10}, 1, 2)
		if n := r.fake.Count("moveDownloads"); n != 1 {
			t.Fatalf("%d moves", n)
		}
	})

	t.Run("keeps the edited fields when the queue changes elsewhere, and reports refusals", func(t *testing.T) {
		r := setup(t, setupOptions{initial: 2})
		r.typeIn(r.d.settings.name, "Nightly")
		once := true
		r.fake.Before = func(name string, _ ...any) error {
			if name == "updateQueue" && once {
				once = false
				return errors.New(`already_exists: a queue named "Nightly" exists`)
			}
			return nil
		}
		r.click(r.d.saveBtn)
		if r.d.failMsg.Style.Hidden || !strings.Contains(r.d.failMsg.text.Text(), `a queue named "Nightly" exists`) {
			t.Fatalf("refusal %q", r.d.failMsg.text.Text())
		}
		if r.d.failMsg.Semantics.Role != kit.RoleAlert {
			t.Fatal("the refusal is not an alert")
		}
		// The queue changes elsewhere: the edits stay.
		q, _ := r.h.Main.Queues.Get(2)
		q = statetest.Queue(2, func(x *downexv1.Queue) { x.Name, x.Running = q.GetName(), true })
		r.fake.Emit(statetest.QueueUpdated(q))
		r.h.Settle()
		if got := r.d.settings.name.Text(); got != "Nightly" {
			t.Fatalf("name %q", got)
		}
	})

	t.Run("lists the schedule of a queue", func(t *testing.T) {
		r := setup(t, setupOptions{initial: 2, night: func(q *downexv1.Queue) {
			q.Schedule = &downexv1.Schedule{Enabled: true, StartMinute: 60, Weekdays: 0x7f}
		}})
		if got := r.d.head.summary.Text(); !strings.Contains(got, "Every day at 01:00") {
			t.Fatalf("summary %q", got)
		}
	})
}

func TestQueueDialogRestyledEditor(t *testing.T) {
	t.Run("lists the queues like the sidebar: the chosen one pressed, with a note and what waits in each", func(t *testing.T) {
		r := setup(t, setupOptions{initial: 2, night: func(q *downexv1.Queue) {
			q.Schedule = &downexv1.Schedule{Enabled: true, StartMinute: 60, Weekdays: 0x7f}
		}})
		l := r.d.qlist
		if l.Semantics.Role != kit.RoleNavigation || l.Semantics.Name != "Queues" || len(l.rows) != 2 {
			t.Fatalf("list %v %q %d", l.Semantics.Role, l.Semantics.Name, len(l.rows))
		}
		main, night := l.rows[0], l.rows[1]
		if main.active || !night.active {
			t.Fatal("pressed states")
		}
		// The main queue runs; three of its downloads wait (the completed one is not counted).
		if main.Semantics.Name != "Main queue running 3" {
			t.Fatalf("main %q", main.Semantics.Name)
		}
		// Night is stopped, and its schedule starts it at 01:00.
		if night.Semantics.Name != "Night 01:00 stopped 0" {
			t.Fatalf("night %q", night.Semantics.Name)
		}
		var n kit.SemanticNode
		night.FillSemantics(&n)
		if n.Toggle != kit.ToggleOn {
			t.Fatal("Night is not aria-pressed")
		}
		if r.heading() != "Night" {
			t.Fatalf("heading %q", r.heading())
		}
	})

	t.Run("chooses the speed unit and the action when done from the keyboard (custom selects)", func(t *testing.T) {
		r := setup(t, setupOptions{initial: 2})
		after := r.d.settings.after
		if after.Semantics.Role != kit.RoleComboBox {
			t.Fatal("not a combobox")
		}
		// Night's stored action is shown.
		if after.Value() != "sleep" {
			t.Fatalf("value %q", after.Value())
		}
		r.h.Window.Focus(after)
		r.key(kit.KeyArrowDown)
		if !after.IsOpen() {
			t.Fatal("Down did not open the list")
		}
		r.key(kit.KeyEnd)
		r.key(kit.KeyEnter)
		if after.IsOpen() || after.Value() != "shutdown" || !strings.Contains(textOf(&after.Element), "Shut down the computer") {
			t.Fatalf("open %v, value %q, shows %q", after.IsOpen(), after.Value(), textOf(&after.Element))
		}

		unit := r.d.settings.unit
		r.h.Window.Focus(unit)
		// Type-ahead chooses without opening.
		r.h.Loop.Send(kit.TextEvent{Text: "k"})
		r.h.Settle()
		if unit.Value() != "KB/s" || unit.IsOpen() {
			t.Fatalf("unit %q open %v", unit.Value(), unit.IsOpen())
		}
		r.typeIn(r.d.settings.speed, "300")
		r.click(r.d.saveBtn)
		spec := r.fake.Args("updateQueue")[1].(*downexv1.Queue)
		if spec.GetPostAction() != downexv1.PostAction_POST_ACTION_SHUTDOWN || spec.GetSpeedLimit() != 300*1000 {
			t.Fatalf("UpdateQueue %v", spec)
		}
	})

	t.Run("closes only the list of an open select on Escape, not the dialog", func(t *testing.T) {
		r := setup(t, setupOptions{initial: 2})
		after := r.d.settings.after
		r.h.Window.Focus(after)
		r.key(kit.KeyEnter)
		if !after.IsOpen() {
			t.Fatal("Enter did not open the list")
		}
		r.key(kit.KeyEscape)
		if after.IsOpen() || after.Value() != "sleep" {
			t.Fatalf("open %v value %q", after.IsOpen(), after.Value())
		}
		if !r.d.Shown() || r.d.Leaving() || r.h.App.Dialogs.Kind() != views.ModalQueues {
			t.Fatal("Escape closed the dialog")
		}
	})

	t.Run("switches between days of the week and one date, with weekday chips that are pressed or not", func(t *testing.T) {
		r := setup(t, setupOptions{initial: 2})
		sc := r.d.sched
		// Off: only the switch, which says what that means.
		if !sc.times.Style.Hidden {
			t.Fatal("Start at shows while the schedule is off")
		}
		if sc.sw.Switch.Semantics.Description == "" {
			t.Fatal("the switch is not described")
		}
		r.click(sc.sw)
		if sc.mode.Track().Semantics.Name != "Repeat" || sc.mode.Value != "days" {
			t.Fatalf("repeat %q %q", sc.mode.Track().Semantics.Name, sc.mode.Value)
		}
		if sc.days.Semantics.Name != "Weekdays" || sc.days.Semantics.Role != kit.RoleGroup {
			t.Fatal("no Weekdays group")
		}
		var names []string
		for _, c := range sc.chips {
			names = append(names, c.Semantics.Name)
			if !c.pressed {
				t.Fatalf("%s is not pressed", c.Semantics.Name)
			}
		}
		if !slices.Equal(names, []string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}) {
			t.Fatalf("chips %q", names)
		}
		// No day: nothing to save.
		for _, c := range sc.chips {
			r.click(c)
		}
		for _, c := range sc.chips {
			if c.pressed {
				t.Fatalf("%s is still pressed", c.Semantics.Name)
			}
		}
		r.click(r.d.saveBtn)
		if sc.daysErr.Style.Hidden || sc.daysErr.Text() != "Choose at least one day" {
			t.Fatalf("days error %q", sc.daysErr.Text())
		}
		if r.fake.Count("updateQueue") != 0 {
			t.Fatal("saved")
		}
		r.click(r.chip("Wednesday"))

		// Once: a date instead of the chips.
		r.click(segment(r.d, "Once"))
		if !sc.days.Style.Hidden || sc.once.Style.Hidden {
			t.Fatal("Once shows the chips")
		}
		r.typeIn(&sc.once.Input, "2026-11-02")
		r.click(segment(r.d, "Every week"))
		if !r.chip("Wednesday").pressed {
			t.Fatal("Wednesday was lost")
		}
		r.click(r.d.saveBtn)
		spec := r.fake.Args("updateQueue")[1].(*downexv1.Queue)
		if !spec.GetSchedule().GetEnabled() || spec.GetSchedule().GetWeekdays() != 1<<2 {
			t.Fatalf("schedule %v", spec.GetSchedule())
		}
	})

	t.Run("lists the order with each download's state", func(t *testing.T) {
		r := setup(t, setupOptions{initial: 1})
		var states []string
		for _, row := range r.orderRows() {
			states = append(states, row.text.Text())
			if row.tile.Style.Hidden {
				t.Fatal("a row has no file tile")
			}
			// a name cut in a narrow column is whole in its tooltip
			if row.name.Tooltip != row.name.Text() {
				t.Fatalf("tooltip %q of %q", row.name.Tooltip, row.name.Text())
			}
		}
		if !slices.Equal(states, []string{"Queued", "Queued", "Paused"}) {
			t.Fatalf("states %q", states)
		}
	})
}

func TestQueueDialogSizing(t *testing.T) {
	t.Run("is 72rem wide and at least 48rem high, growing to the room the window has (scripts/measure-dialogs.mjs checks its one scroll region)", func(t *testing.T) {
		// 72rem: a wide editor puts the schedule beside the settings and the order (§4.10)
		r := setup(t, setupOptions{})
		card := r.d.Card().WindowBounds()
		if card.Width() != 72*16 {
			t.Fatalf("width %v", card.Width())
		}
		// never lower than 48rem (switching queues does not shrink it)
		if card.Height() < 48*16 {
			t.Fatalf("height %v", card.Height())
		}
		r.click(r.d.qlist.rows[1])
		if h := r.d.Card().WindowBounds().Height(); h != card.Height() {
			t.Fatalf("switching queues changed the height %v → %v", card.Height(), h)
		}
		// at most the window's room (and the window less 32 px wide)
		small := setup(t, setupOptions{width: 900, height: 600})
		c := small.d.Card().WindowBounds()
		room := kit.DIP(600) - views.TitleBarHeight - 2*8
		if c.Width() != 900-32 || c.Height() != room {
			t.Fatalf("900×600: %v × %v, want 868 × %v", c.Width(), c.Height(), room)
		}
	})
}
