package queues

import (
	"strconv"
	"time"
	"unicode/utf8"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/validate"
)

// DaysMode is whether a schedule runs on days of the week or once on a date.
type DaysMode string

const (
	ModeDays DaysMode = "days"
	ModeOnce DaysMode = "once"
)

// Form is the queue editor's form.
type Form struct {
	Name string
	// MaxConcurrent as typed; 0 = the global "simultaneous downloads" setting.
	MaxConcurrent string
	SpeedValue    string
	SpeedUnit     RateUnit
	// SpeedLimitBytes is the limit as loaded (bytes per second): kept exactly while its text is
	// unchanged.
	SpeedLimitBytes int64
	PostAction      downexv1.PostAction
	ScheduleEnabled bool
	StartTime       string
	StopEnabled     bool
	StopTime        string
	Mode            DaysMode
	Weekdays        int32
	OnceDate        string
	// RepeatMinutes as typed; 0 or empty = off.
	RepeatMinutes string
	WakeComputer  bool
}

// FormErrors are the queue editor's messages by field ("" = fine).
type FormErrors struct {
	Name, MaxConcurrent, Speed, PostAction, StartTime, StopTime, Days, OnceDate, RepeatMinutes string
}

// Empty reports no error.
func (e FormErrors) Empty() bool { return e == FormErrors{} }

func dateOf(t time.Time) string {
	y, m, d := t.Date()
	year := strconv.Itoa(y)
	return year + "-" + pad2(int(m)) + "-" + pad2(d)
}

// NewForm is the editor's form for a queue, or for a new one (q nil); now (local time) gives a
// one-time schedule's proposed date.
func NewForm(q *downexv1.Queue, now time.Time) Form {
	s := q.GetSchedule()
	value, unit := splitRate(q.GetSpeedLimit())
	mode := ModeDays
	if s.GetEnabled() && s.GetWeekdays() == 0 {
		mode = ModeOnce
	}
	weekdays := s.GetWeekdays()
	if weekdays == 0 {
		// A new schedule starts with every day.
		weekdays = EveryDay
	}
	once := s.GetOnceDate()
	if once == "" {
		once = dateOf(now)
	}
	repeat := ""
	if s.GetRepeatMinutes() > 0 {
		repeat = strconv.Itoa(int(s.GetRepeatMinutes()))
	}
	return Form{
		Name:            q.GetName(),
		MaxConcurrent:   strconv.Itoa(int(q.GetMaxConcurrent())),
		SpeedValue:      value,
		SpeedUnit:       unit,
		SpeedLimitBytes: q.GetSpeedLimit(),
		PostAction:      q.GetPostAction(),
		ScheduleEnabled: s.GetEnabled(),
		StartTime:       MinutesToTime(int(s.GetStartMinute())),
		StopEnabled:     s.GetStopEnabled(),
		StopTime:        MinutesToTime(int(s.GetStopMinute())),
		Mode:            mode,
		Weekdays:        weekdays,
		OnceDate:        once,
		RepeatMinutes:   repeat,
		WakeComputer:    s.GetWakeComputer(),
	}
}

// ValidateForm checks the queue editor: a unique name (ignoring case) of at most 64
// characters, 0–16 simultaneous downloads, a speed limit, an action this system can do, and a
// schedule with valid times and at least one day (or a date). A disabled schedule keeps what was
// entered, unchecked, so that switching it on again restores it. others are the other queues
// (for the name); base the queue being edited (nil for a new one), whose stored schedule values
// are kept for the fields the editor leaves alone. The spec is a Queue message with the fields
// CreateQueue and UpdateQueue take (and base's id).
func ValidateForm(form Form, others []*downexv1.Queue, caps Capabilities, base *downexv1.Queue) (*downexv1.Queue, FormErrors, bool) {
	var errs FormErrors
	name := jsTrim(form.Name)
	switch {
	case name == "":
		errs.Name = "Enter a name"
	case utf8.RuneCountInString(name) > validate.MaxQueueNameLength:
		errs.Name = "Keep the name under " + strconv.Itoa(validate.MaxQueueNameLength+1) + " characters"
	case hasControl(name):
		errs.Name = "A name cannot contain control characters"
	default:
		lower := jsLower(name)
		for _, q := range others {
			if jsLower(jsTrim(q.GetName())) == lower {
				errs.Name = "Another queue has this name"
				break
			}
		}
	}
	errs.MaxConcurrent = validate.CheckIntRange(form.MaxConcurrent, 0, validate.MaxQueueConcurrent, "Simultaneous downloads")
	speed, speedOK := parseRate(form.SpeedValue, form.SpeedUnit, form.SpeedLimitBytes)
	if !speedOK {
		errs.Speed = "Enter a number, or leave empty for no limit"
	}
	if form.PostAction == actionHibernate && !caps.Hibernate {
		errs.PostAction = "This computer cannot hibernate"
	}

	start, startOK := TimeToMinutes(form.StartTime)
	stop, stopOK := TimeToMinutes(form.StopTime)
	repeatText := jsTrim(form.RepeatMinutes)
	repeatError := ""
	if repeatText != "" {
		repeatError = validate.CheckIntRange(repeatText, 0, validate.MaxRepeatMinutes, "The interval")
	}
	if form.ScheduleEnabled {
		if !startOK {
			errs.StartTime = "Enter a time such as 23:30"
		}
		if form.StopEnabled && !stopOK {
			errs.StopTime = "Enter a time such as 07:00"
		}
		if form.Mode == ModeDays && form.Weekdays&EveryDay == 0 {
			errs.Days = "Choose at least one day"
		}
		if form.Mode == ModeOnce && !IsDate(form.OnceDate) {
			errs.OnceDate = "Choose a date"
		}
		errs.RepeatMinutes = repeatError
	}
	if !errs.Empty() {
		return nil, errs, false
	}

	kept := base.GetSchedule()
	var schedule *downexv1.Schedule
	if form.ScheduleEnabled {
		schedule = &downexv1.Schedule{
			Enabled:     true,
			StartMinute: int32(start),
			StopEnabled: form.StopEnabled,
			StopMinute:  kept.GetStopMinute(),
			Weekdays:    form.Weekdays & EveryDay,
			OnceDate:    kept.GetOnceDate(),
			// Only where the computer can be woken; elsewhere what was stored stays.
			WakeComputer: kept.GetWakeComputer(),
		}
		if stopOK {
			schedule.StopMinute = int32(stop)
		}
		if form.Mode == ModeOnce {
			schedule.Weekdays, schedule.OnceDate = 0, form.OnceDate
		}
		if caps.WakeTimers {
			schedule.WakeComputer = form.WakeComputer
		}
		if repeatText != "" {
			n, _ := strconv.Atoi(repeatText)
			schedule.RepeatMinutes = int32(n)
		}
	} else {
		schedule = &downexv1.Schedule{
			StartMinute:   kept.GetStartMinute(),
			StopEnabled:   kept.GetStopEnabled(),
			StopMinute:    kept.GetStopMinute(),
			Weekdays:      kept.GetWeekdays(),
			OnceDate:      kept.GetOnceDate(),
			WakeComputer:  kept.GetWakeComputer(),
			RepeatMinutes: kept.GetRepeatMinutes(),
		}
	}
	maxConcurrent, _ := strconv.Atoi(jsTrim(form.MaxConcurrent))
	return &downexv1.Queue{
		Id:            base.GetId(),
		Name:          name,
		MaxConcurrent: int32(maxConcurrent),
		SpeedLimit:    speed,
		Schedule:      schedule,
		PostAction:    form.PostAction,
	}, errs, true
}

func hasControl(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}
