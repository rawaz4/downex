package daemon

import (
	"testing"
	"time"
	_ "time/tzdata" // a fixed zone with daylight saving time on every system

	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
)

func newYork(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// at returns a local time in loc.
func at(loc *time.Location, y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, loc)
}

func utc(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
}

func minutes(h, m int) int { return h*60 + m }

// Local times that a clock change skips happen at the change; ones that occur twice, at
// their first occurrence (America/New_York, 2026: DST starts 8 March 02:00 EST, ends
// 1 November 02:00 EDT).
func TestLocalInstantAcrossDST(t *testing.T) {
	ny := newYork(t)
	for _, tc := range []struct {
		name   string
		date   civilDate
		minute int
		want   time.Time
	}{
		{"ordinary winter day", civilDate{2026, time.January, 15}, minutes(9, 0), utc(2026, 1, 15, 14, 0)},
		{"ordinary summer day", civilDate{2026, time.July, 15}, minutes(9, 0), utc(2026, 7, 15, 13, 0)},
		{"spring forward: 02:30 is skipped", civilDate{2026, time.March, 8}, minutes(2, 30), utc(2026, 3, 8, 7, 0)},
		{"spring forward: 02:00 is skipped", civilDate{2026, time.March, 8}, minutes(2, 0), utc(2026, 3, 8, 7, 0)},
		{"spring forward: 03:00 exists", civilDate{2026, time.March, 8}, minutes(3, 0), utc(2026, 3, 8, 7, 0)},
		{"spring forward: 01:59 exists", civilDate{2026, time.March, 8}, minutes(1, 59), utc(2026, 3, 8, 6, 59)},
		{"fall back: 01:30 twice, first", civilDate{2026, time.November, 1}, minutes(1, 30), utc(2026, 11, 1, 5, 30)},
		{"fall back: 01:00 twice, first", civilDate{2026, time.November, 1}, minutes(1, 0), utc(2026, 11, 1, 5, 0)},
		{"fall back: 02:00 once", civilDate{2026, time.November, 1}, minutes(2, 0), utc(2026, 11, 1, 7, 0)},
		{"fall back: 00:30 once", civilDate{2026, time.November, 1}, minutes(0, 30), utc(2026, 11, 1, 4, 30)},
	} {
		if got := localInstant(tc.date, tc.minute, ny); !got.Equal(tc.want) {
			t.Errorf("%s: localInstant(%s, %d) = %v, want %v", tc.name, tc.date, tc.minute, got.UTC(), tc.want)
		}
	}
	// In UTC every minute exists once.
	if got := localInstant(civilDate{2026, time.March, 8}, minutes(2, 30), time.UTC); !got.Equal(utc(2026, 3, 8, 2, 30)) {
		t.Errorf("UTC: %v", got)
	}
}

// eventString describes an event for the table below.
func eventString(ev schedEvent, ok bool) string {
	if !ok {
		return "none"
	}
	kind := "start"
	if ev.kind == evStop {
		kind = "stop"
	}
	return kind + "@" + ev.at.UTC().Format("2006-01-02T15:04Z")
}

func TestNextEventTable(t *testing.T) {
	ny := newYork(t)
	mon := model.WeekdayBit(time.Monday)
	daily := model.Schedule{Enabled: true, StartMinute: minutes(9, 0), StopEnabled: true, StopMinute: minutes(17, 0), Weekdays: model.AllWeekdays}
	night := model.Schedule{Enabled: true, StartMinute: minutes(23, 0), StopEnabled: true, StopMinute: minutes(2, 0), Weekdays: mon}
	once := model.Schedule{Enabled: true, StartMinute: minutes(10, 0), OnceDate: "2026-10-25"}
	dst := model.Schedule{Enabled: true, StartMinute: minutes(2, 30), StopEnabled: true, StopMinute: minutes(3, 30), Weekdays: model.AllWeekdays}
	for _, tc := range []struct {
		name string
		s    model.Schedule
		from time.Time
		want string
	}{
		// 2026-10-19 is a Monday; New York is UTC-4 until 1 November.
		{"daily: before the start", daily, at(ny, 2026, 10, 19, 8, 0), "start@2026-10-19T13:00Z"},
		{"daily: exactly at the start (strictly after)", daily, at(ny, 2026, 10, 19, 9, 0), "stop@2026-10-19T21:00Z"},
		{"daily: inside", daily, at(ny, 2026, 10, 19, 10, 0), "stop@2026-10-19T21:00Z"},
		{"daily: after the stop", daily, at(ny, 2026, 10, 19, 18, 0), "start@2026-10-20T13:00Z"},
		{"daily: across the fall-back day the offset changes", daily, at(ny, 2026, 10, 31, 18, 0), "start@2026-11-01T14:00Z"},
		{"cross midnight: Monday evening", night, at(ny, 2026, 10, 19, 22, 0), "start@2026-10-20T03:00Z"},
		{"cross midnight: inside after midnight", night, at(ny, 2026, 10, 20, 1, 0), "stop@2026-10-20T06:00Z"},
		{"cross midnight: Tuesday evening waits a week", night, at(ny, 2026, 10, 20, 22, 0), "start@2026-10-27T03:00Z"},
		{"once: the day before", once, at(ny, 2026, 10, 24, 12, 0), "start@2026-10-25T14:00Z"},
		{"once: afterwards never again", once, at(ny, 2026, 10, 25, 11, 0), "none"},
		{"once: three weeks ahead", once, at(ny, 2026, 10, 4, 12, 0), "start@2026-10-25T14:00Z"},
		{"once: a year ahead, in the other offset", once, at(ny, 2025, 12, 1, 12, 0), "start@2026-10-25T14:00Z"},
		{"DST: skipped start runs at the change", dst, at(ny, 2026, 3, 7, 12, 0), "start@2026-03-08T07:00Z"},
		{"DST: then the stop in the new offset", dst, at(ny, 2026, 3, 8, 3, 0), "stop@2026-03-08T07:30Z"},
		{"DST: next day back to 02:30", dst, at(ny, 2026, 3, 8, 4, 0), "start@2026-03-09T06:30Z"},
		{"disabled", model.Schedule{StartMinute: 1, Weekdays: model.AllWeekdays}, at(ny, 2026, 10, 19, 0, 0), "none"},
	} {
		if got := eventString(nextEvent(tc.s, tc.from, ny)); got != tc.want {
			t.Errorf("%s: nextEvent = %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestLatestEventAndWindows(t *testing.T) {
	ny := newYork(t)
	daily := model.Schedule{Enabled: true, StartMinute: minutes(9, 0), StopEnabled: true, StopMinute: minutes(17, 0), Weekdays: model.AllWeekdays}
	open := model.Schedule{Enabled: true, StartMinute: minutes(9, 0), Weekdays: model.AllWeekdays}
	allDay := model.Schedule{Enabled: true, StartMinute: minutes(6, 0), StopEnabled: true, StopMinute: minutes(6, 0), Weekdays: model.AllWeekdays}
	for _, tc := range []struct {
		name      string
		s         model.Schedule
		mark, now time.Time
		want      string
		inside    bool // now lies inside the latest event's window
	}{
		{"nothing since the mark", daily, at(ny, 2026, 10, 19, 9, 0), at(ny, 2026, 10, 19, 12, 0), "none", false},
		{"start missed while asleep, window open", daily, at(ny, 2026, 10, 18, 17, 0), at(ny, 2026, 10, 19, 12, 0), "start@2026-10-19T13:00Z", true},
		{"whole window missed: its stop wins", daily, at(ny, 2026, 10, 18, 17, 0), at(ny, 2026, 10, 19, 18, 0), "stop@2026-10-19T21:00Z", false},
		{"days missed: only the latest counts", daily, at(ny, 2026, 10, 10, 17, 0), at(ny, 2026, 10, 19, 10, 0), "start@2026-10-19T13:00Z", true},
		{"open window: a missed start still applies", open, at(ny, 2026, 10, 18, 9, 0), at(ny, 2026, 10, 19, 23, 0), "start@2026-10-19T13:00Z", true},
		{"adjacent windows: the next start wins over the stop", allDay, at(ny, 2026, 10, 18, 7, 0), at(ny, 2026, 10, 19, 6, 0), "start@2026-10-19T10:00Z", true},
		{"mark in the future (clock set back)", daily, at(ny, 2026, 10, 20, 9, 0), at(ny, 2026, 10, 19, 12, 0), "none", false},
	} {
		ev, ok := latestEvent(tc.s, tc.mark, tc.now, ny)
		if got := eventString(ev, ok); got != tc.want {
			t.Errorf("%s: latestEvent = %s, want %s", tc.name, got, tc.want)
			continue
		}
		if ok && ev.win.contains(tc.now) != tc.inside {
			t.Errorf("%s: window %v–%v contains %v = %v", tc.name, ev.win.start, ev.win.end, tc.now, !tc.inside)
		}
	}

	// The window of a schedule crossing midnight is active after midnight, on the next day.
	night := model.Schedule{Enabled: true, StartMinute: minutes(23, 0), StopEnabled: true, StopMinute: minutes(2, 0),
		Weekdays: model.WeekdayBit(time.Monday)}
	if w, ok := activeWindow(night, at(ny, 2026, 10, 20, 1, 0), ny); !ok || !w.start.Equal(at(ny, 2026, 10, 19, 23, 0)) {
		t.Errorf("active window Tuesday 01:00 = %v %v", w, ok)
	}
	if _, ok := activeWindow(night, at(ny, 2026, 10, 20, 23, 30), ny); ok {
		t.Error("Tuesday 23:30 is inside a window that only Monday opens")
	}
	// A window across the fall-back night lasts an hour more.
	fall := model.Schedule{Enabled: true, StartMinute: minutes(0, 0), StopEnabled: true, StopMinute: minutes(4, 0), OnceDate: "2026-11-01"}
	if w := windowOn(fall, civilDate{2026, time.November, 1}, ny); w.end.Sub(w.start) != 5*time.Hour {
		t.Errorf("fall-back window lasts %v, want 5h", w.end.Sub(w.start))
	}

	// Repeats count from the window's start and end with it.
	w := window{start: utc(2026, 10, 19, 13, 0), end: utc(2026, 10, 19, 14, 0)}
	for _, tc := range []struct {
		now  time.Time
		want time.Time
	}{
		{utc(2026, 10, 19, 13, 0), utc(2026, 10, 19, 13, 20)},
		{utc(2026, 10, 19, 13, 25), utc(2026, 10, 19, 13, 40)},
		{utc(2026, 10, 19, 13, 40), time.Time{}}, // 14:00 is the end
	} {
		if got := nextRepeat(w, tc.now, 20*time.Minute); !got.Equal(tc.want) {
			t.Errorf("nextRepeat(%v) = %v, want %v", tc.now, got, tc.want)
		}
	}
	if st, ok := nextStart(daily, at(ny, 2026, 10, 19, 10, 0), ny); !ok || !st.Equal(at(ny, 2026, 10, 20, 9, 0)) {
		t.Errorf("nextStart = %v %v", st, ok)
	}
	later := model.Schedule{Enabled: true, StartMinute: minutes(9, 0), OnceDate: "2026-11-10"}
	if st, ok := nextStart(later, at(ny, 2026, 10, 19, 10, 0), ny); !ok || !st.Equal(at(ny, 2026, 11, 10, 9, 0)) {
		t.Errorf("nextStart of a once date three weeks ahead = %v %v", st, ok)
	}
	if _, ok := nextStart(later, at(ny, 2026, 11, 10, 9, 0), ny); ok {
		t.Error("a once schedule starts again after its date")
	}
}

func TestScheduleValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    *model.Schedule
		ok   bool
	}{
		{"none", nil, true},
		{"weekdays", &model.Schedule{Enabled: true, StartMinute: 1439, StopEnabled: true, StopMinute: 0, Weekdays: model.AllWeekdays}, true},
		{"once", &model.Schedule{Enabled: true, OnceDate: "2026-12-31"}, true},
		{"enabled without days", &model.Schedule{Enabled: true}, false},
		{"bad date", &model.Schedule{Enabled: true, OnceDate: "2026-02-30"}, false},
		{"start out of range", &model.Schedule{StartMinute: 1440}, false},
		{"stop negative", &model.Schedule{StopMinute: -1}, false},
		{"weekday bit 7", &model.Schedule{Weekdays: 128}, false},
		{"repeat too long", &model.Schedule{RepeatMinutes: MaxRepeatMinutes + 1}, false},
	} {
		var p *downexv1.Schedule
		if tc.s != nil {
			p = protoQueue(model.Queue{Schedule: *tc.s}, nil).GetSchedule()
		}
		if _, err := scheduleFromProto(p); (err == nil) != tc.ok {
			t.Errorf("%s: err = %v", tc.name, err)
		}
	}
}
