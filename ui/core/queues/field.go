package queues

import (
	"strconv"
	"strings"
	"time"
)

// The 24-hour time (hh:mm) and date (yyyy-mm-dd) fields of the schedule editor
// (DESIGN-SYSTEM.md §3.5): text fields in the app's own format, never a 12-hour picker or a
// calendar. Up and Down step the part at the caret (hour or minute; year, month or day), PageUp
// and PageDown by 10.

// FieldKind is a time or a date field.
type FieldKind uint8

const (
	TimeField FieldKind = iota
	DateField
)

// ParseParts reads a field's value into its parts: a time's [hours, minutes], a date's [year,
// month, day]; nil when it does not parse ("7:05" and "0705" are times, "2026-2-3" a date).
func ParseParts(kind FieldKind, value string) []int {
	v := jsTrim(value)
	if kind == TimeField {
		var h, m string
		if before, after, found := strings.Cut(v, ":"); found {
			h, m = before, after
		} else if len(v) >= 3 {
			h, m = v[:len(v)-2], v[len(v)-2:]
		}
		if len(h) < 1 || len(h) > 2 || len(m) != 2 || !digits(h) || !digits(m) {
			return nil
		}
		hours, _ := strconv.Atoi(h)
		mins, _ := strconv.Atoi(m)
		if hours > 23 || mins > 59 {
			return nil
		}
		return []int{hours, mins}
	}
	parts := strings.Split(v, "-")
	if len(parts) != 3 || len(parts[0]) != 4 || len(parts[1]) < 1 || len(parts[1]) > 2 || len(parts[2]) < 1 || len(parts[2]) > 2 ||
		!digits(parts[0]) || !digits(parts[1]) || !digits(parts[2]) {
		return nil
	}
	y, _ := strconv.Atoi(parts[0])
	mo, _ := strconv.Atoi(parts[1])
	d, _ := strconv.Atoi(parts[2])
	if mo < 1 || mo > 12 || d < 1 || d > daysIn(y, mo) {
		return nil
	}
	return []int{y, mo, d}
}

func part(parts []int, i, missing int) int {
	if i < len(parts) {
		return parts[i]
	}
	return missing
}

func padStart(n, width int) string {
	s := strconv.Itoa(n)
	if len(s) < width {
		s = strings.Repeat("0", width-len(s)) + s
	}
	return s
}

// FormatParts writes parts out in full: "07:05", "2026-11-02".
func FormatParts(kind FieldKind, parts []int) string {
	if kind == TimeField {
		return padStart(part(parts, 0, 0), 2) + ":" + padStart(part(parts, 1, 0), 2)
	}
	return padStart(part(parts, 0, 0), 4) + "-" + padStart(part(parts, 1, 1), 2) + "-" + padStart(part(parts, 2, 1), 2)
}

// jsUTC is new Date(Date.UTC(year, month0, day)): out-of-range months and days carry over,
// and a year from 0 to 99 is read as 1900 + year, as JavaScript reads it.
func jsUTC(year, month0, day int) time.Time {
	if year >= 0 && year <= 99 {
		year += 1900
	}
	return time.Date(year, time.Month(month0+1), day, 0, 0, 0, 0, time.UTC)
}

// daysIn is the number of days of month (1–12) of year, as today's window counts them.
func daysIn(year, month int) int { return jsUTC(year, month, 0).Day() }

// PartAt is the part the caret is in: time hh|mm, date yyyy|mm|dd.
func PartAt(kind FieldKind, caret int) int {
	if kind == TimeField {
		if caret <= 2 {
			return 0
		}
		return 1
	}
	switch {
	case caret <= 4:
		return 0
	case caret <= 7:
		return 1
	}
	return 2
}

// PartRange is where a part sits in the formatted value: [start, end).
func PartRange(kind FieldKind, p int) (start, end int) {
	if kind == TimeField {
		if p == 0 {
			return 0, 2
		}
		return 3, 5
	}
	switch p {
	case 0:
		return 0, 4
	case 1:
		return 5, 7
	}
	return 8, 10
}

// StepParts steps one part by by: hours and minutes wrap within the day (24-hour); a date
// moves by whole years, months or days on the calendar (the 31st of a month steps to the last
// day of a shorter one).
func StepParts(kind FieldKind, parts []int, p, by int) []int {
	if kind == TimeField {
		h, m := part(parts, 0, 0), part(parts, 1, 0)
		if p == 0 {
			return []int{((h+by)%24 + 24) % 24, m}
		}
		return []int{h, ((m+by)%60 + 60) % 60}
	}
	y, mo, d := part(parts, 0, 2000), part(parts, 1, 1), part(parts, 2, 1)
	if p == 2 {
		t := jsUTC(y, mo-1, d+by)
		return []int{t.Year(), int(t.Month()), t.Day()}
	}
	step := by * 12
	if p == 1 {
		step = by
	}
	months := y*12 + (mo - 1) + step
	ny := floorDiv(months, 12)
	nm := months%12 + 1
	return []int{ny, nm, min(d, daysIn(ny, nm))}
}

func floorDiv(a, b int) int {
	q := a / b
	if (a%b != 0) && (a < 0) != (b < 0) {
		q--
	}
	return q
}
