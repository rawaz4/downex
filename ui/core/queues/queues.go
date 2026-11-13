// Package queues holds the texts and rules of queues, their schedules and post-queue actions
// (docs/M6-SPEC.md §1), ported from ui/frontend/src/lib/queues.ts and the module script of
// DateTimeField.svelte: labels, the queue editor's form and its validation, ordering helpers,
// and the parts of the 24-hour time and date fields. downexd stays the authority and validates
// again. Pure.
package queues

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	downexv1 "downex/internal/rpc/downex/v1"
)

// MainQueueID is the id of the main queue (M6-SPEC §1): it always exists and cannot be deleted.
const MainQueueID = 1

// Weekday is a day of a schedule's mask.
type Weekday struct {
	Bit   int32
	Short string
	Long  string
}

// Weekdays are the days of a schedule, Monday first: bit 0 = Monday … bit 6 = Sunday.
var Weekdays = []Weekday{
	{1 << 0, "Mon", "Monday"},
	{1 << 1, "Tue", "Tuesday"},
	{1 << 2, "Wed", "Wednesday"},
	{1 << 3, "Thu", "Thursday"},
	{1 << 4, "Fri", "Friday"},
	{1 << 5, "Sat", "Saturday"},
	{1 << 6, "Sun", "Sunday"},
}

// Masks of common sets of days.
const (
	EveryDay = 0x7f
	Workdays = 0x1f
	Weekend  = 0x60
)

const (
	actionNone      = downexv1.PostAction_POST_ACTION_UNSPECIFIED
	actionQuit      = downexv1.PostAction_POST_ACTION_QUIT_DOWNEX
	actionSleep     = downexv1.PostAction_POST_ACTION_SLEEP
	actionHibernate = downexv1.PostAction_POST_ACTION_HIBERNATE
	actionShutdown  = downexv1.PostAction_POST_ACTION_SHUTDOWN
)

// PostActions are the post-queue actions in the order the menus list them (UNSPECIFIED is
// nothing: "Keep running").
var PostActions = []downexv1.PostAction{actionNone, actionQuit, actionSleep, actionHibernate, actionShutdown}

func known(a downexv1.PostAction) downexv1.PostAction {
	if slices.Contains(PostActions, a) {
		return a
	}
	return actionNone
}

// PostActionLabel is what the queue editor offers for a finished queue.
func PostActionLabel(a downexv1.PostAction) string {
	switch known(a) {
	case actionQuit:
		return "Quit Downex"
	case actionSleep:
		return "Put the computer to sleep"
	case actionHibernate:
		return "Hibernate the computer"
	case actionShutdown:
		return "Shut down the computer"
	}
	return "Keep running"
}

// PostActionShortLabel is an action's short name, for menus where the question is already asked
// ("When the main queue is done": Keep running, Quit Downex, Sleep, …; DESIGN-SYSTEM.md §3.14).
func PostActionShortLabel(a downexv1.PostAction) string {
	switch known(a) {
	case actionQuit:
		return "Quit Downex"
	case actionSleep:
		return "Sleep"
	case actionHibernate:
		return "Hibernate"
	case actionShutdown:
		return "Shut down"
	}
	return "Keep running"
}

// WhenDoneText is the status bar's readable control: "When done: keep running", "When done:
// shut down".
func WhenDoneText(a downexv1.PostAction) string {
	short := PostActionShortLabel(a)
	if known(a) != actionQuit {
		short = strings.ToLower(short)
	}
	return "When done: " + short
}

// IsPowerAction reports an action that puts the computer to sleep, hibernates it or shuts it
// down (shown as a warning).
func IsPowerAction(a downexv1.PostAction) bool {
	return a == actionSleep || a == actionHibernate || a == actionShutdown
}

// PostActionProgress is the countdown's wording: "<verb> in 42 s".
func PostActionProgress(a downexv1.PostAction) string {
	switch known(a) {
	case actionQuit:
		return "Quitting Downex"
	case actionSleep:
		return "Going to sleep"
	case actionHibernate:
		return "Hibernating"
	case actionShutdown:
		return "Shutting down"
	}
	return "Finishing"
}

// Capabilities are what this system can do after a queue (the host's StartupView).
type Capabilities struct {
	// Hibernate: the system can hibernate (false on macOS).
	Hibernate bool
	// WakeTimers: the computer can be woken for a schedule's start (Windows).
	WakeTimers bool
}

// PostActionChoices are the post-queue actions this system can do: hibernation only where the
// host says so (not on macOS). A queue that already has an action keeps it on offer.
func PostActionChoices(caps Capabilities, current downexv1.PostAction) []downexv1.PostAction {
	var out []downexv1.PostAction
	for _, a := range PostActions {
		if a != actionHibernate || caps.Hibernate || current == actionHibernate {
			out = append(out, a)
		}
	}
	return out
}

func isMain(q *downexv1.Queue) bool { return q.GetIsMain() || q.GetId() == MainQueueID }

// SortQueues lists the main queue first, then the others in the order they were created.
func SortQueues(list []*downexv1.Queue) []*downexv1.Queue {
	out := slices.Clone(list)
	slices.SortStableFunc(out, func(a, b *downexv1.Queue) int {
		if am, bm := isMain(a), isMain(b); am != bm {
			if am {
				return -1
			}
			return 1
		}
		switch {
		case a.GetId() < b.GetId():
			return -1
		case a.GetId() > b.GetId():
			return 1
		}
		return 0
	})
	return out
}

func pad2(n int) string {
	s := strconv.Itoa(n)
	if len(s) < 2 {
		return "0" + s
	}
	return s
}

// MinutesToTime is minutes after midnight as "HH:MM"; anything outside the day reads 00:00.
func MinutesToTime(minutes int) string {
	if minutes < 0 || minutes >= 1440 {
		minutes = 0
	}
	return pad2(minutes/60) + ":" + pad2(minutes%60)
}

// TimeToMinutes reads "HH:MM" (24 h) as minutes after midnight; ok is false when it is not a
// time of day.
func TimeToMinutes(text string) (minutes int, ok bool) {
	h, m, found := strings.Cut(jsTrim(text), ":")
	if !found || len(h) < 1 || len(h) > 2 || len(m) != 2 || !digits(h) || !digits(m) {
		return 0, false
	}
	hours, _ := strconv.Atoi(h)
	mins, _ := strconv.Atoi(m)
	if hours > 23 || mins > 59 {
		return 0, false
	}
	return hours*60 + mins, true
}

// IsDate reports a local calendar date "YYYY-MM-DD" that exists. As today's window reads it
// with the Date constructor, a year below 100 is not one.
func IsDate(text string) bool {
	if len(text) != 10 || text[4] != '-' || text[7] != '-' || !digits(text[:4]) || !digits(text[5:7]) || !digits(text[8:]) {
		return false
	}
	y, _ := strconv.Atoi(text[:4])
	mo, _ := strconv.Atoi(text[5:7])
	d, _ := strconv.Atoi(text[8:])
	if y < 100 {
		return false
	}
	t := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC)
	return t.Year() == y && int(t.Month()) == mo && t.Day() == d
}

// FormatDays names the days of a weekday mask: "Mon–Fri", "Every day", "Mon, Wed, Sat".
func FormatDays(mask int32) string {
	days := mask & EveryDay
	switch days {
	case EveryDay:
		return "Every day"
	case Workdays:
		return "Mon–Fri"
	case Weekend:
		return "Sat–Sun"
	}
	var names []string
	for _, d := range Weekdays {
		if days&d.Bit != 0 {
			names = append(names, d.Short)
		}
	}
	return strings.Join(names, ", ")
}

// ScheduleSummary is one line about when a queue runs.
func ScheduleSummary(s *downexv1.Schedule) string {
	if !s.GetEnabled() {
		return "Not scheduled: starts and stops when you say"
	}
	var when string
	if s.GetWeekdays() == 0 {
		date := s.GetOnceDate()
		if date == "" {
			date = "a date to choose"
		}
		when = "Once on " + date
	} else {
		when = FormatDays(s.GetWeekdays())
	}
	out := when + " at " + MinutesToTime(int(s.GetStartMinute()))
	if s.GetStopEnabled() {
		out += ", stops at " + MinutesToTime(int(s.GetStopMinute()))
	}
	if s.GetRepeatMinutes() > 0 {
		out += ", retries every " + strconv.Itoa(int(s.GetRepeatMinutes())) + " min"
	}
	return out
}

// QueueNote is the short note beside a queue's name in a list (the sidebar, the queue manager):
// "running", or the time its schedule starts it next ("01:00"; a one-time start also names its
// date), or "" for a stopped queue without a schedule.
func QueueNote(q *downexv1.Queue) string {
	if q.GetRunning() {
		return "running"
	}
	s := q.GetSchedule()
	if !s.GetEnabled() {
		return ""
	}
	at := MinutesToTime(int(s.GetStartMinute()))
	if s.GetWeekdays() == 0 && s.GetOnceDate() != "" {
		return s.GetOnceDate() + " " + at
	}
	return at
}

// Spec is a queue's settings as CreateQueue and UpdateQueue take them (UpdateQueue replaces
// name, max_concurrent, speed_limit, schedule and post_action): a new message with the id and
// those fields of q, for the caller to change (the when-done menu sets PostAction).
func Spec(q *downexv1.Queue) *downexv1.Queue {
	return &downexv1.Queue{
		Id:            q.GetId(),
		Name:          q.GetName(),
		MaxConcurrent: q.GetMaxConcurrent(),
		SpeedLimit:    q.GetSpeedLimit(),
		Schedule:      proto.CloneOf(q.GetSchedule()),
		PostAction:    q.GetPostAction(),
	}
}

// SecondsLeft is the whole seconds until deadline, never negative (times in epoch ms).
func SecondsLeft(deadline, now int64) int64 {
	d := deadline - now
	if d <= 0 {
		return 0
	}
	return (d + 999) / 1000
}

// End stands for "after the last download" where a download id is expected (ids start at 1).
const End int64 = 0

func without(order, moved []int64) []int64 {
	var out []int64
	for _, id := range order {
		if !slices.Contains(moved, id) {
			out = append(out, id)
		}
	}
	return out
}

// InsertionIndex is the position MoveDownloads takes to put moved just before before in a queue
// whose downloads are order (before End = at the end): its index among the queue's other
// downloads, or -1 for the end.
func InsertionIndex(order, moved []int64, before int64) int32 {
	if before == End {
		return -1
	}
	return int32(slices.Index(without(order, moved), before))
}

// Reorder is the order after moving moved before before (End = to the end), as downexd will
// have it.
func Reorder(order, moved []int64, before int64) []int64 {
	others := without(order, moved)
	at := len(others)
	if i := InsertionIndex(order, moved, before); i >= 0 {
		at = int(i)
	}
	out := make([]int64, 0, len(others)+len(moved))
	out = append(out, others[:at]...)
	out = append(out, moved...)
	return append(out, others[at:]...)
}

// NeighbourTarget is where moving id one place up (delta -1) or down (+1) among shown (the rows
// the editor lists, in the order of the queue's downloads order, which may hold more) puts it:
// the download it then goes before, or End; ok is false when it cannot move.
func NeighbourTarget(order, shown []int64, id int64, delta int) (before int64, ok bool) {
	i := slices.Index(shown, id)
	if i < 0 {
		return 0, false
	}
	if delta < 0 {
		if i == 0 {
			return 0, false
		}
		return shown[i-1], true
	}
	if i+1 >= len(shown) {
		return 0, false
	}
	next := shown[i+1]
	// Just after the next row: before whatever follows it in the queue.
	others := without(order, []int64{id})
	if j := slices.Index(others, next) + 1; j < len(others) {
		return others[j], true
	}
	return End, true
}

func digits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}
