package queues

import (
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format"
)

// The cases of ui/frontend/src/lib/queues.test.ts and of the rules block of
// components/DateTimeField.test.ts.

var caps = Capabilities{Hibernate: true, WakeTimers: true}

// makeQueue is testing.ts makeQueue.
func makeQueue(id int64, edit ...func(*downexv1.Queue)) *downexv1.Queue {
	q := &downexv1.Queue{Id: id, Name: "Queue " + itoa(id), Running: id == MainQueueID, IsMain: id == MainQueueID, Schedule: &downexv1.Schedule{}}
	if id == MainQueueID {
		q.Name = "Main queue"
	}
	for _, e := range edit {
		e(q)
	}
	return q
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestScheduleTimes(t *testing.T) {
	for _, c := range []struct {
		m    int
		want string
	}{{0, "00:00"}, {1439, "23:59"}, {-5, "00:00"}} {
		if got := MinutesToTime(c.m); got != c.want {
			t.Errorf("MinutesToTime(%d) = %q", c.m, got)
		}
	}
	for _, c := range []struct {
		in   string
		want int
	}{{"23:30", 1410}, {"7:05", 425}} {
		if got, ok := TimeToMinutes(c.in); !ok || got != c.want {
			t.Errorf("TimeToMinutes(%q) = %d, %v", c.in, got, ok)
		}
	}
	for _, bad := range []string{"24:00", "12:60", "noon", "", "1230"} {
		if _, ok := TimeToMinutes(bad); ok {
			t.Errorf("TimeToMinutes(%q) read a time", bad)
		}
	}
}

func TestIsDate(t *testing.T) {
	for in, want := range map[string]bool{
		"2026-10-24": true,
		"2028-02-29": true,
		"2026-02-29": false,
		"2026-13-01": false,
		"24.10.2026": false,
		// the Date constructor reads a year below 100 as 19xx
		"0050-01-01": false,
	} {
		if got := IsDate(in); got != want {
			t.Errorf("IsDate(%q) = %v", in, got)
		}
	}
}

func TestDaysAndSummary(t *testing.T) {
	for _, c := range []struct {
		mask int32
		want string
	}{{EveryDay, "Every day"}, {Workdays, "Mon–Fri"}, {0b1000101, "Mon, Wed, Sun"}, {Weekend, "Sat–Sun"}} {
		if got := FormatDays(c.mask); got != c.want {
			t.Errorf("FormatDays(%b) = %q", c.mask, got)
		}
	}
	if got := ScheduleSummary(&downexv1.Schedule{}); !strings.HasPrefix(got, "Not scheduled") {
		t.Errorf("empty schedule = %q", got)
	}
	night := &downexv1.Schedule{Enabled: true, StartMinute: 1380, StopEnabled: true, StopMinute: 420, Weekdays: Workdays, RepeatMinutes: 30}
	if got := ScheduleSummary(night); got != "Mon–Fri at 23:00, stops at 07:00, retries every 30 min" {
		t.Errorf("night = %q", got)
	}
	once := &downexv1.Schedule{Enabled: true, StartMinute: 60, OnceDate: "2026-10-24"}
	if got := ScheduleSummary(once); got != "Once on 2026-10-24 at 01:00" {
		t.Errorf("once = %q", got)
	}
	if got := ScheduleSummary(&downexv1.Schedule{Enabled: true}); got != "Once on a date to choose at 00:00" {
		t.Errorf("once without a date = %q", got)
	}
}

func TestSecondsLeft(t *testing.T) {
	for _, c := range [][3]int64{{61_000, 1_000, 60}, {60_500, 1_000, 60}, {1_000, 5_000, 0}} {
		if got := SecondsLeft(c[0], c[1]); got != c[2] {
			t.Errorf("SecondsLeft(%d, %d) = %d", c[0], c[1], got)
		}
	}
}

func TestQueueOrder(t *testing.T) {
	order := []int64{10, 11, 12, 13}
	for _, c := range []struct {
		moved  []int64
		before int64
		want   int32
	}{{[]int64{13}, 10, 0}, {[]int64{10}, 13, 2}, {[]int64{10}, End, -1}, {[]int64{10}, 99, -1}} {
		if got := InsertionIndex(order, c.moved, c.before); got != c.want {
			t.Errorf("InsertionIndex(%v, %d) = %d, want %d", c.moved, c.before, got, c.want)
		}
	}
	for _, c := range []struct {
		moved  []int64
		before int64
		want   []int64
	}{
		{[]int64{10}, 13, []int64{11, 12, 10, 13}},
		{[]int64{12, 13}, 10, []int64{12, 13, 10, 11}},
		{[]int64{11}, End, []int64{10, 12, 13, 11}},
	} {
		if got := Reorder(order, c.moved, c.before); !slices.Equal(got, c.want) {
			t.Errorf("Reorder(%v, %d) = %v", c.moved, c.before, got)
		}
	}
}

func TestNeighbourTarget(t *testing.T) {
	// 12 is completed (not shown); 14 follows the shown rows.
	full := []int64{10, 11, 12, 13, 14}
	shown := []int64{10, 11, 13}
	for _, c := range []struct {
		order, shown []int64
		id           int64
		delta        int
		want         int64
		ok           bool
	}{
		{full, shown, 11, -1, 10, true},
		{full, shown, 10, -1, 0, false},
		// Down: just after 13, before 14.
		{full, shown, 11, 1, 14, true},
		{full, shown, 13, 1, 0, false},
		{[]int64{10, 11}, []int64{10, 11}, 10, 1, End, true},
		{full, shown, 99, 1, 0, false},
	} {
		if got, ok := NeighbourTarget(c.order, c.shown, c.id, c.delta); got != c.want || ok != c.ok {
			t.Errorf("NeighbourTarget(%d, %+d) = %d, %v; want %d, %v", c.id, c.delta, got, ok, c.want, c.ok)
		}
	}
}

func TestSortQueues(t *testing.T) {
	list := SortQueues([]*downexv1.Queue{makeQueue(3), makeQueue(1), makeQueue(2)})
	var ids []int64
	for _, q := range list {
		ids = append(ids, q.GetId())
	}
	if !slices.Equal(ids, []int64{1, 2, 3}) {
		t.Errorf("order = %v", ids)
	}
}

func TestPostActions(t *testing.T) {
	if got := PostActionChoices(Capabilities{Hibernate: true}, actionNone); !slices.Equal(got, PostActions) {
		t.Errorf("with hibernation = %v", got)
	}
	if got := PostActionChoices(Capabilities{}, actionNone); slices.Contains(got, actionHibernate) {
		t.Errorf("without hibernation = %v", got)
	}
	// A stored choice stays visible (to be changed).
	if got := PostActionChoices(Capabilities{}, actionHibernate); !slices.Contains(got, actionHibernate) {
		t.Errorf("stored hibernation = %v", got)
	}
	var short, long []string
	for _, a := range PostActionChoices(Capabilities{Hibernate: true}, actionNone) {
		short = append(short, PostActionShortLabel(a))
		long = append(long, PostActionLabel(a))
	}
	if !slices.Equal(short, []string{"Keep running", "Quit Downex", "Sleep", "Hibernate", "Shut down"}) {
		t.Errorf("short labels = %q", short)
	}
	if !slices.Equal(long, []string{"Keep running", "Quit Downex", "Put the computer to sleep", "Hibernate the computer", "Shut down the computer"}) {
		t.Errorf("labels = %q", long)
	}
	for a, want := range map[downexv1.PostAction]string{
		actionNone:      "When done: keep running",
		actionQuit:      "When done: Quit Downex",
		actionSleep:     "When done: sleep",
		actionHibernate: "When done: hibernate",
		actionShutdown:  "When done: shut down",
	} {
		if got := WhenDoneText(a); got != want {
			t.Errorf("WhenDoneText(%v) = %q", a, got)
		}
	}
	var power []downexv1.PostAction
	for _, a := range PostActions {
		if IsPowerAction(a) {
			power = append(power, a)
		}
	}
	if !slices.Equal(power, []downexv1.PostAction{actionSleep, actionHibernate, actionShutdown}) {
		t.Errorf("power actions = %v", power)
	}
	for a, want := range map[downexv1.PostAction]string{actionNone: "Finishing", actionQuit: "Quitting Downex", actionSleep: "Going to sleep", actionHibernate: "Hibernating", actionShutdown: "Shutting down"} {
		if got := PostActionProgress(a); got != want {
			t.Errorf("PostActionProgress(%v) = %q", a, got)
		}
	}
}

func TestQueueNote(t *testing.T) {
	if got := QueueNote(makeQueue(1, func(q *downexv1.Queue) { q.Running = true })); got != "running" {
		t.Errorf("running = %q", got)
	}
	if got := QueueNote(makeQueue(2)); got != "" {
		t.Errorf("stopped = %q", got)
	}
	night := makeQueue(2, func(q *downexv1.Queue) {
		q.Schedule = &downexv1.Schedule{Enabled: true, StartMinute: 60, Weekdays: EveryDay}
	})
	if got := QueueNote(night); got != "01:00" {
		t.Errorf("night = %q", got)
	}
	running := proto.CloneOf(night)
	running.Running = true
	if got := QueueNote(running); got != "running" {
		t.Errorf("running night = %q", got)
	}
	once := makeQueue(3, func(q *downexv1.Queue) {
		q.Schedule = &downexv1.Schedule{Enabled: true, StartMinute: 75, OnceDate: "2026-10-30"}
	})
	if got := QueueNote(once); got != "2026-10-30 01:15" {
		t.Errorf("once = %q", got)
	}
	// A switched-off schedule says nothing.
	off := proto.CloneOf(night)
	off.Schedule.Enabled = false
	if got := QueueNote(off); got != "" {
		t.Errorf("off = %q", got)
	}
}

func TestSpec(t *testing.T) {
	night := makeQueue(2, func(q *downexv1.Queue) {
		q.Name, q.MaxConcurrent, q.SpeedLimit, q.PostAction, q.DownloadIds, q.Running = "Night", 2, 1024, actionSleep, []int64{4, 5}, true
	})
	want := &downexv1.Queue{Id: 2, Name: "Night", MaxConcurrent: 2, SpeedLimit: 1024, Schedule: night.Schedule, PostAction: actionSleep}
	if got := Spec(night); !proto.Equal(got, want) {
		t.Errorf("Spec = %v", got)
	}
	changed := Spec(night)
	changed.PostAction = actionShutdown
	want.PostAction = actionShutdown
	if !proto.Equal(changed, want) || night.PostAction != actionSleep {
		t.Errorf("a changed spec = %v, queue %v", changed, night.PostAction)
	}
}

func nightQueue() *downexv1.Queue {
	return makeQueue(2, func(q *downexv1.Queue) {
		q.Name, q.MaxConcurrent, q.SpeedLimit, q.PostAction = "Night", 2, 2*1024*1024, actionSleep
		q.Schedule = &downexv1.Schedule{Enabled: true, StartMinute: 1380, StopEnabled: true, StopMinute: 420, Weekdays: Workdays, WakeComputer: true}
	})
}

func TestValidateFormRoundTrip(t *testing.T) {
	night := nightQueue()
	spec, errs, ok := ValidateForm(NewForm(night, time.Now()), nil, caps, night)
	want := &downexv1.Queue{Id: 2, Name: "Night", MaxConcurrent: 2, SpeedLimit: 2 * 1024 * 1024, PostAction: actionSleep, Schedule: night.Schedule}
	if !ok || !proto.Equal(spec, want) {
		t.Errorf("round trip = %v, %+v", spec, errs)
	}
}

func TestNewQueueForm(t *testing.T) {
	form := NewForm(nil, time.Date(2026, 10, 21, 12, 0, 0, 0, time.Local))
	if form.Name != "" || form.MaxConcurrent != "0" || form.ScheduleEnabled || form.Weekdays != EveryDay || form.OnceDate != "2026-10-21" || form.Mode != ModeDays {
		t.Errorf("new form = %+v", form)
	}
	if form.SpeedValue != "" || form.SpeedUnit != format.MBps {
		t.Errorf("new speed = %q %q", form.SpeedValue, form.SpeedUnit)
	}
	form.Name = "  Weekend "
	spec, errs, ok := ValidateForm(form, nil, caps, nil)
	if !ok || spec.GetName() != "Weekend" || !proto.Equal(spec.GetSchedule(), &downexv1.Schedule{}) {
		t.Errorf("new queue = %v, %+v", spec, errs)
	}
}

func TestValidateFormErrors(t *testing.T) {
	night := nightQueue()
	form := NewForm(night, time.Now())
	form.Name, form.MaxConcurrent, form.SpeedValue = "main QUEUE", "17", "fast"
	form.StartTime, form.StopTime, form.Weekdays, form.RepeatMinutes = "25:00", "x", 0, "-1"
	_, errs, ok := ValidateForm(form, []*downexv1.Queue{makeQueue(1)}, caps, night)
	want := FormErrors{
		Name:          "Another queue has this name",
		MaxConcurrent: "Simultaneous downloads must be from 0 to 16",
		Speed:         "Enter a number, or leave empty for no limit",
		StartTime:     "Enter a time such as 23:30",
		StopTime:      "Enter a time such as 07:00",
		Days:          "Choose at least one day",
		RepeatMinutes: "The interval must be a whole number from 0 to 1440",
	}
	if ok || errs != want {
		t.Errorf("errors = %+v", errs)
	}
	once := NewForm(night, time.Now())
	once.Mode, once.OnceDate = ModeOnce, "2026-02-30"
	if _, errs, ok := ValidateForm(once, nil, caps, night); ok || errs != (FormErrors{OnceDate: "Choose a date"}) {
		t.Errorf("once = %+v", errs)
	}
	empty := NewForm(night, time.Now())
	empty.Name = ""
	if _, errs, ok := ValidateForm(empty, nil, caps, nil); ok || errs.Name != "Enter a name" {
		t.Errorf("empty name = %+v", errs)
	}
	long := NewForm(night, time.Now())
	long.Name = strings.Repeat("x", 65)
	if _, errs, ok := ValidateForm(long, nil, caps, nil); ok || errs.Name != "Keep the name under 65 characters" {
		t.Errorf("long name = %+v", errs)
	}
	control := NewForm(night, time.Now())
	control.Name = "a\x07b"
	if _, errs, _ := ValidateForm(control, nil, caps, nil); errs.Name != "A name cannot contain control characters" {
		t.Errorf("control = %+v", errs)
	}
}

func TestHibernateAndWake(t *testing.T) {
	night := nightQueue()
	none := Capabilities{}
	form := NewForm(night, time.Now())
	form.PostAction = actionHibernate
	if _, errs, ok := ValidateForm(form, nil, none, nil); ok || errs != (FormErrors{PostAction: "This computer cannot hibernate"}) {
		t.Errorf("hibernate = %+v", errs)
	}
	wake := NewForm(night, time.Now())
	wake.WakeComputer = false
	if spec, _, ok := ValidateForm(wake, nil, none, night); !ok || !spec.GetSchedule().GetWakeComputer() {
		t.Errorf("wake kept = %v", spec)
	}
}

func TestOnceAndOff(t *testing.T) {
	night := nightQueue()
	form := NewForm(night, time.Now())
	form.Mode, form.OnceDate, form.StartTime, form.RepeatMinutes = ModeOnce, "2026-10-24", "01:15", "15"
	spec, errs, ok := ValidateForm(form, nil, caps, night)
	s := spec.GetSchedule()
	if !ok || !s.GetEnabled() || s.GetWeekdays() != 0 || s.GetOnceDate() != "2026-10-24" || s.GetStartMinute() != 75 || s.GetRepeatMinutes() != 15 {
		t.Errorf("once = %v, %+v", spec, errs)
	}
	// Invalid fields of a disabled schedule do not block saving; the stored schedule stays.
	off := NewForm(night, time.Now())
	off.ScheduleEnabled, off.StartTime = false, "junk"
	spec, errs, ok = ValidateForm(off, nil, caps, night)
	want := proto.CloneOf(night.Schedule)
	want.Enabled = false
	if !ok || !proto.Equal(spec.GetSchedule(), want) {
		t.Errorf("off = %v, %+v", spec, errs)
	}
}

func TestDateTimeFieldRules(t *testing.T) {
	for _, c := range []struct {
		kind FieldKind
		in   string
		want []int
	}{
		{TimeField, "07:05", []int{7, 5}},
		{TimeField, "7:05", []int{7, 5}},
		{TimeField, "0705", []int{7, 5}},
		{TimeField, "705", []int{7, 5}},
		{TimeField, " 7:05 ", []int{7, 5}},
		{TimeField, " 123 ", []int{1, 23}},
		{TimeField, "7:5", nil},
		{TimeField, "12345", nil},
		{TimeField, ":05", nil},
		{TimeField, "\uff17:05", nil}, // a fullwidth digit is not JavaScript's \d
		{TimeField, "24:00", nil},
		{TimeField, "12:60", nil},
		{TimeField, "12", nil},
		{DateField, "2026-02-29", nil},
		{DateField, "2028-02-29", []int{2028, 2, 29}},
		{DateField, "2026-2-3", []int{2026, 2, 3}},
		{DateField, "2026-1-5", []int{2026, 1, 5}},
		{DateField, "26-01-05", nil},
		{DateField, "2026-13-01", nil},
	} {
		if got := ParseParts(c.kind, c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseParts(%d, %q) = %v, want %v", c.kind, c.in, got, c.want)
		}
	}
	if got := FormatParts(TimeField, []int{7, 5}); got != "07:05" {
		t.Errorf("time = %q", got)
	}
	if got := FormatParts(DateField, []int{2026, 11, 2}); got != "2026-11-02" {
		t.Errorf("date = %q", got)
	}
	// String(n).padStart(4, '0') of a year stepped below 0: the sign is padded too.
	if got := FormatParts(DateField, []int{-5, 1, 1}); got != "00-5-01-01" {
		t.Errorf("date = %q", got)
	}
	for _, c := range []struct {
		kind  FieldKind
		caret int
		want  int
	}{{TimeField, 0, 0}, {TimeField, 4, 1}, {DateField, 6, 1}, {DateField, 9, 2}, {DateField, 4, 0}} {
		if got := PartAt(c.kind, c.caret); got != c.want {
			t.Errorf("PartAt(%d, %d) = %d", c.kind, c.caret, got)
		}
	}
	for _, c := range []struct {
		kind     FieldKind
		parts    []int
		part, by int
		want     []int
	}{
		{TimeField, []int{23, 59}, 0, 1, []int{0, 59}},
		{TimeField, []int{0, 0}, 1, -1, []int{0, 59}},
		{DateField, []int{2026, 10, 31}, 2, 1, []int{2026, 11, 1}},
		{DateField, []int{2026, 1, 31}, 1, 1, []int{2026, 2, 28}},
		{DateField, []int{2026, 12, 15}, 1, 1, []int{2027, 1, 15}},
		{DateField, []int{2028, 2, 29}, 0, 1, []int{2029, 2, 28}},
		{TimeField, []int{5, 30}, 1, -40, []int{5, 50}},
	} {
		if got := StepParts(c.kind, c.parts, c.part, c.by); !slices.Equal(got, c.want) {
			t.Errorf("StepParts(%v, %d, %d) = %v, want %v", c.parts, c.part, c.by, got, c.want)
		}
	}
	if s, e := PartRange(DateField, 1); s != 5 || e != 7 {
		t.Errorf("PartRange = %d, %d", s, e)
	}
}
