package daemon

import (
	"time"

	"downex/internal/model"
)

// Queue schedules (docs/M6-SPEC.md §1) are computed here, as pure functions of a
// schedule, instants and a location. A schedule opens a window on each of its start
// days (or once, on its once date) at its start minute, local time, and closes it at
// the stop minute when stopping is enabled (on the next day when the stop minute is at
// or before the start minute); without a stop the window stays open. Every local time
// is found with time.Date in the location each time, so changes of the UTC offset
// (daylight saving time) are followed: a start or stop minute that a clock change
// skips happens at the change, one that occurs twice happens at its first occurrence.

// scheduleLookback bounds how far back the latest events of a schedule are searched:
// a week covers every weekday and one more day for windows that cross midnight.
const scheduleLookback = 8

// window is one opening of a schedule: [start, end), open-ended when end is zero.
type window struct {
	start, end time.Time
}

// contains reports whether t lies inside w.
func (w window) contains(t time.Time) bool {
	return !t.Before(w.start) && (w.end.IsZero() || t.Before(w.end))
}

// schedEventKind is what a schedule event does.
type schedEventKind uint8

const (
	evStart schedEventKind = iota + 1 // the queue starts
	evStop                            // the queue stops
)

// schedEvent is a start or stop of a schedule window.
type schedEvent struct {
	at   time.Time
	kind schedEventKind
	win  window
}

// before orders events by time; at the same instant, the events of an earlier window
// come first (its stop before the next window's start) and a window's start comes before
// its own stop.
func (a schedEvent) before(b schedEvent) bool {
	switch {
	case !a.at.Equal(b.at):
		return a.at.Before(b.at)
	case !a.win.start.Equal(b.win.start):
		return a.win.start.Before(b.win.start)
	}
	return a.kind == evStart && b.kind == evStop
}

// civilDate is a calendar date without a time zone.
type civilDate struct {
	year  int
	month time.Month
	day   int
}

// dateIn returns t's local date in loc.
func dateIn(t time.Time, loc *time.Location) civilDate {
	y, m, d := t.In(loc).Date()
	return civilDate{y, m, d}
}

// parseDate parses "YYYY-MM-DD".
func parseDate(s string) (civilDate, bool) {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return civilDate{}, false
	}
	y, m, d := t.Date()
	return civilDate{y, m, d}, true
}

// noon returns the date at noon UTC, for date arithmetic.
func (c civilDate) noon() time.Time { return time.Date(c.year, c.month, c.day, 12, 0, 0, 0, time.UTC) }

func (c civilDate) addDays(n int) civilDate {
	y, m, d := c.noon().AddDate(0, 0, n).Date()
	return civilDate{y, m, d}
}

func (c civilDate) weekday() time.Weekday { return c.noon().Weekday() }

func (c civilDate) String() string { return c.noon().Format(time.DateOnly) }

// daysBetween returns the number of days from a to b.
func daysBetween(a, b civilDate) int {
	return int(b.noon().Sub(a.noon()).Hours() / 24)
}

// localInstant returns the first instant at which the wall clock of loc shows minute
// (minutes after midnight) on date c: the instant itself on an ordinary day, the
// change of the offset when the clock skips over that minute, and its first occurrence
// when the clock is set back over it.
func localInstant(c civilDate, minute int, loc *time.Location) time.Time {
	h, mi := minute/60, minute%60
	t := time.Date(c.year, c.month, c.day, h, mi, 0, 0, loc)
	want := time.Date(c.year, c.month, c.day, h, mi, 0, 0, time.UTC)
	ly, lm, ld := t.Date()
	got := time.Date(ly, lm, ld, t.Hour(), t.Minute(), 0, 0, time.UTC)
	if !got.Equal(want) {
		// The minute does not exist in loc: the clock jumps over it at a zone change,
		// and the event happens at that change.
		start, end := t.ZoneBounds()
		switch {
		case got.After(want) && !start.IsZero():
			return start
		case got.Before(want) && !end.IsZero():
			return end
		}
		return t
	}
	// The minute may exist twice (the clock was set back): take the earlier one, in the
	// zone that was in effect before t's.
	if start, _ := t.ZoneBounds(); !start.IsZero() {
		_, offPrev := start.Add(-time.Nanosecond).Zone()
		_, off := t.Zone()
		if earlier := t.Add(time.Duration(off-offPrev) * time.Second); earlier.Before(start) && earlier.Before(t) {
			ey, em, ed := earlier.Date()
			if time.Date(ey, em, ed, earlier.Hour(), earlier.Minute(), 0, 0, time.UTC).Equal(want) {
				return earlier
			}
		}
	}
	return t
}

// startsOn reports whether s opens a window on date c.
func startsOn(s model.Schedule, c civilDate) bool {
	if s.Weekdays == 0 {
		once, ok := parseDate(s.OnceDate)
		return ok && once == c
	}
	return s.Weekdays&model.WeekdayBit(c.weekday()) != 0
}

// windowOn returns the window s opens on date c.
func windowOn(s model.Schedule, c civilDate, loc *time.Location) window {
	w := window{start: localInstant(c, s.StartMinute, loc)}
	if s.StopEnabled {
		end := c
		if s.StopMinute <= s.StartMinute {
			end = c.addDays(1)
		}
		w.end = localInstant(end, s.StopMinute, loc)
		if w.end.Before(w.start) { // offset changes between the two minutes
			w.end = w.start
		}
	}
	return w
}

// windowsFrom returns the windows s opens on the dates from through to (inclusive), in
// order.
func windowsFrom(s model.Schedule, from, to civilDate, loc *time.Location) []window {
	var out []window
	if s.Weekdays == 0 {
		if once, ok := parseDate(s.OnceDate); ok && daysBetween(from, once) >= 0 && daysBetween(once, to) >= 0 {
			out = append(out, windowOn(s, once, loc))
		}
		return out
	}
	for c, n := from, daysBetween(from, to); n >= 0; c, n = c.addDays(1), n-1 {
		if startsOn(s, c) {
			out = append(out, windowOn(s, c, loc))
		}
	}
	return out
}

// windowEvents returns the start and stop events of w.
func windowEvents(s model.Schedule, w window) []schedEvent {
	evs := []schedEvent{{at: w.start, kind: evStart, win: w}}
	if s.StopEnabled {
		evs = append(evs, schedEvent{at: w.end, kind: evStop, win: w})
	}
	return evs
}

// lookahead returns the last date whose windows nextEvent and nextStart consider from
// date d: a week and a day (every weekday, and a window crossing midnight), or a once
// date further ahead, however far.
func lookahead(s model.Schedule, d civilDate) civilDate {
	to := d.addDays(scheduleLookback)
	if s.Weekdays == 0 {
		if once, ok := parseDate(s.OnceDate); ok && daysBetween(to, once) > 0 {
			return once
		}
	}
	return to
}

// nextEvent returns the first event of s after t.
func nextEvent(s model.Schedule, t time.Time, loc *time.Location) (schedEvent, bool) {
	if !s.Enabled {
		return schedEvent{}, false
	}
	d := dateIn(t, loc)
	var best schedEvent
	found := false
	for _, w := range windowsFrom(s, d.addDays(-1), lookahead(s, d), loc) {
		for _, ev := range windowEvents(s, w) {
			if ev.at.After(t) && (!found || ev.before(best)) {
				best, found = ev, true
			}
		}
	}
	return best, found
}

// latestEvent returns the last event of s after mark and at or before now (zero mark:
// any in the lookback). Earlier events of that span are superseded by it.
func latestEvent(s model.Schedule, mark, now time.Time, loc *time.Location) (schedEvent, bool) {
	if !s.Enabled || (!mark.IsZero() && !mark.Before(now)) {
		return schedEvent{}, false
	}
	to := dateIn(now, loc)
	from := to.addDays(-scheduleLookback)
	if !mark.IsZero() {
		if m := dateIn(mark, loc).addDays(-1); daysBetween(from, m) > 0 {
			from = m
		}
	}
	var best schedEvent
	found := false
	for _, w := range windowsFrom(s, from, to, loc) {
		for _, ev := range windowEvents(s, w) {
			if ev.at.After(now) || (!mark.IsZero() && !ev.at.After(mark)) {
				continue
			}
			if !found || best.before(ev) {
				best, found = ev, true
			}
		}
	}
	return best, found
}

// activeWindow returns the window of s that contains now (the latest one when windows
// overlap).
func activeWindow(s model.Schedule, now time.Time, loc *time.Location) (window, bool) {
	if !s.Enabled {
		return window{}, false
	}
	to := dateIn(now, loc)
	ws := windowsFrom(s, to.addDays(-scheduleLookback), to, loc)
	for i := len(ws) - 1; i >= 0; i-- {
		if ws[i].contains(now) {
			return ws[i], true
		}
	}
	return window{}, false
}

// nextRepeat returns the first repeat of window w after now, every interval from its
// start, or zero when the window ends first.
func nextRepeat(w window, now time.Time, interval time.Duration) time.Time {
	if interval <= 0 {
		return time.Time{}
	}
	k := now.Sub(w.start)/interval + 1
	t := w.start.Add(k * interval)
	if !w.end.IsZero() && !t.Before(w.end) {
		return time.Time{}
	}
	return t
}

// nextStart returns the first start of s after t (for waking the computer).
func nextStart(s model.Schedule, t time.Time, loc *time.Location) (time.Time, bool) {
	if !s.Enabled {
		return time.Time{}, false
	}
	d := dateIn(t, loc)
	for _, w := range windowsFrom(s, d.addDays(-1), lookahead(s, d), loc) {
		if w.start.After(t) {
			return w.start, true
		}
	}
	return time.Time{}, false
}
