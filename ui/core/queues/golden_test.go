package queues_test

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format"
	. "downex/ui/core/format/formattest"
	"downex/ui/core/queues"
)

// TestGoldens replays every queues.ts case of the 0.9.2 formatter goldens (the inputs of the
// window's vitest tables, with Blink's output) in each locale and time zone; queueForm's local
// dates come from the zone, as the goldens set the engine's zone to it.
func TestGoldens(t *testing.T) {
	g, path, ok, err := LoadGoldens()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Skipf("no goldens at %s (set DOWNEX_GOLDENS)", path)
	}
	RunGoldens(t, g, "queues.", queuesCalls(g.FixedNow))
}

// The api.ts Queue and Schedule as the goldens hold them.
type wireSchedule struct {
	Enabled       bool   `json:"enabled"`
	StartMinute   int32  `json:"startMinute"`
	StopEnabled   bool   `json:"stopEnabled"`
	StopMinute    int32  `json:"stopMinute"`
	Weekdays      int32  `json:"weekdays"`
	OnceDate      string `json:"onceDate"`
	WakeComputer  bool   `json:"wakeComputer"`
	RepeatMinutes int32  `json:"repeatMinutes"`
}

type wireQueue struct {
	ID            int64         `json:"id"`
	Name          string        `json:"name"`
	Running       bool          `json:"running"`
	MaxConcurrent int32         `json:"maxConcurrent"`
	SpeedLimit    int64         `json:"speedLimit"`
	Schedule      *wireSchedule `json:"schedule"`
	PostAction    string        `json:"postAction"`
	IsMain        bool          `json:"isMain"`
	DownloadIDs   []int64       `json:"downloadIds"`
}

func postAction(name string) downexv1.PostAction {
	return downexv1.PostAction(downexv1.PostAction_value["POST_ACTION_"+strings.ToUpper(name)])
}

func postActionName(a downexv1.PostAction) string {
	if a == downexv1.PostAction_POST_ACTION_UNSPECIFIED {
		return "none"
	}
	return strings.ToLower(strings.TrimPrefix(a.String(), "POST_ACTION_"))
}

func (w *wireSchedule) proto() *downexv1.Schedule {
	if w == nil {
		return nil
	}
	return &downexv1.Schedule{Enabled: w.Enabled, StartMinute: w.StartMinute, StopEnabled: w.StopEnabled, StopMinute: w.StopMinute,
		Weekdays: w.Weekdays, OnceDate: w.OnceDate, WakeComputer: w.WakeComputer, RepeatMinutes: w.RepeatMinutes}
}

func (w *wireQueue) proto() *downexv1.Queue {
	return &downexv1.Queue{Id: w.ID, Name: w.Name, Running: w.Running, MaxConcurrent: w.MaxConcurrent, SpeedLimit: w.SpeedLimit,
		Schedule: w.Schedule.proto(), PostAction: postAction(w.PostAction), IsMain: w.IsMain, DownloadIds: w.DownloadIDs}
}

func queueArg(raw json.RawMessage) (*downexv1.Queue, error) {
	if string(raw) == "null" || IsUndefined(raw) {
		return nil, nil
	}
	var w wireQueue
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	return w.proto(), nil
}

func queuesArg(raw json.RawMessage) ([]*downexv1.Queue, error) {
	var ws []wireQueue
	if err := json.Unmarshal(raw, &ws); err != nil {
		return nil, err
	}
	out := make([]*downexv1.Queue, len(ws))
	for i := range ws {
		out[i] = ws[i].proto()
	}
	return out, nil
}

func scheduleJSON(s *downexv1.Schedule) map[string]any {
	return map[string]any{"enabled": s.GetEnabled(), "startMinute": s.GetStartMinute(), "stopEnabled": s.GetStopEnabled(),
		"stopMinute": s.GetStopMinute(), "weekdays": s.GetWeekdays(), "onceDate": s.GetOnceDate(),
		"wakeComputer": s.GetWakeComputer(), "repeatMinutes": s.GetRepeatMinutes()}
}

// specJSON is queues.ts's QueueSpec: what CreateQueue and UpdateQueue take.
func specJSON(q *downexv1.Queue) map[string]any {
	return map[string]any{"name": q.GetName(), "maxConcurrent": q.GetMaxConcurrent(), "speedLimit": q.GetSpeedLimit(),
		"schedule": scheduleJSON(q.GetSchedule()), "postAction": postActionName(q.GetPostAction())}
}

func queueJSON(q *downexv1.Queue) map[string]any {
	m := specJSON(q)
	ids := q.GetDownloadIds()
	if ids == nil {
		ids = []int64{}
	}
	m["id"], m["running"], m["isMain"], m["downloadIds"] = q.GetId(), q.GetRunning(), q.GetIsMain(), ids
	return m
}

func formJSON(f queues.Form) map[string]any {
	return map[string]any{"name": f.Name, "maxConcurrent": f.MaxConcurrent, "speedValue": f.SpeedValue, "speedUnit": string(f.SpeedUnit),
		"speedLimitBytes": f.SpeedLimitBytes, "postAction": postActionName(f.PostAction), "scheduleEnabled": f.ScheduleEnabled,
		"startTime": f.StartTime, "stopEnabled": f.StopEnabled, "stopTime": f.StopTime, "mode": string(f.Mode),
		"weekdays": f.Weekdays, "onceDate": f.OnceDate, "repeatMinutes": f.RepeatMinutes, "wakeComputer": f.WakeComputer}
}

func formArg(raw json.RawMessage) (queues.Form, error) {
	var w struct {
		Name            string `json:"name"`
		MaxConcurrent   string `json:"maxConcurrent"`
		SpeedValue      string `json:"speedValue"`
		SpeedUnit       string `json:"speedUnit"`
		SpeedLimitBytes int64  `json:"speedLimitBytes"`
		PostAction      string `json:"postAction"`
		ScheduleEnabled bool   `json:"scheduleEnabled"`
		StartTime       string `json:"startTime"`
		StopEnabled     bool   `json:"stopEnabled"`
		StopTime        string `json:"stopTime"`
		Mode            string `json:"mode"`
		Weekdays        int32  `json:"weekdays"`
		OnceDate        string `json:"onceDate"`
		RepeatMinutes   string `json:"repeatMinutes"`
		WakeComputer    bool   `json:"wakeComputer"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return queues.Form{}, err
	}
	return queues.Form{Name: w.Name, MaxConcurrent: w.MaxConcurrent, SpeedValue: w.SpeedValue, SpeedUnit: format.RateUnit(w.SpeedUnit),
		SpeedLimitBytes: w.SpeedLimitBytes, PostAction: postAction(w.PostAction), ScheduleEnabled: w.ScheduleEnabled,
		StartTime: w.StartTime, StopEnabled: w.StopEnabled, StopTime: w.StopTime, Mode: queues.DaysMode(w.Mode),
		Weekdays: w.Weekdays, OnceDate: w.OnceDate, RepeatMinutes: w.RepeatMinutes, WakeComputer: w.WakeComputer}, nil
}

// errorsJSON is queues.ts's QueueFormErrors: only the fields with a message.
func errorsJSON(e queues.FormErrors) map[string]any {
	m := map[string]any{}
	for k, v := range map[string]string{"name": e.Name, "maxConcurrent": e.MaxConcurrent, "speed": e.Speed, "postAction": e.PostAction,
		"startTime": e.StartTime, "stopTime": e.StopTime, "days": e.Days, "onceDate": e.OnceDate, "repeatMinutes": e.RepeatMinutes} {
		if v != "" {
			m[k] = v
		}
	}
	return m
}

func capsArg(raw json.RawMessage) (queues.Capabilities, error) {
	var c struct {
		Hibernate  bool `json:"hibernate"`
		WakeTimers bool `json:"wakeTimers"`
	}
	err := json.Unmarshal(raw, &c)
	return queues.Capabilities{Hibernate: c.Hibernate, WakeTimers: c.WakeTimers}, err
}

func ids(raw json.RawMessage) ([]int64, error) {
	var out []int64
	err := json.Unmarshal(raw, &out)
	return out, err
}

// before is an insertion target: a download id, or null for the end.
func before(raw json.RawMessage) (int64, error) {
	if string(raw) == "null" {
		return queues.End, nil
	}
	n, err := Num(raw)
	return int64(n), err
}

func num(raw json.RawMessage) (int64, error) {
	n, err := Num(raw)
	return int64(n), err
}

func str(raw json.RawMessage) (string, error) { return Str(raw) }

func queuesCalls(fixedNow float64) map[string]Call {
	return map[string]Call{
		"queues.formatDays": func(a []json.RawMessage) (any, error) {
			n, err := num(a[0])
			return queues.FormatDays(int32(n)), err
		},
		"queues.insertionIndex": func(a []json.RawMessage) (any, error) {
			order, err := ids(a[0])
			if err != nil {
				return nil, err
			}
			moved, err := ids(a[1])
			if err != nil {
				return nil, err
			}
			b, err := before(a[2])
			return queues.InsertionIndex(order, moved, b), err
		},
		"queues.isDate": func(a []json.RawMessage) (any, error) {
			s, err := str(a[0])
			return queues.IsDate(s), err
		},
		"queues.isPowerAction": func(a []json.RawMessage) (any, error) {
			s, err := str(a[0])
			return queues.IsPowerAction(postAction(s)), err
		},
		"queues.minutesToTime": func(a []json.RawMessage) (any, error) {
			n, err := Num(a[0])
			if err != nil {
				return nil, err
			}
			// Minutes are whole (Schedule's int32 fields): a fraction or NaN only exists in
			// TypeScript.
			if n != math.Trunc(n) || math.Abs(n) > math.MaxInt32 {
				return nil, ErrUnrepresentable
			}
			return queues.MinutesToTime(int(n)), nil
		},
		"queues.neighbourTarget": func(a []json.RawMessage) (any, error) {
			order, err := ids(a[0])
			if err != nil {
				return nil, err
			}
			shown, err := ids(a[1])
			if err != nil {
				return nil, err
			}
			id, err := num(a[2])
			if err != nil {
				return nil, err
			}
			delta, err := num(a[3])
			if err != nil {
				return nil, err
			}
			b, ok := queues.NeighbourTarget(order, shown, id, int(delta))
			switch {
			case !ok:
				return Undefined, nil
			case b == queues.End:
				return nil, nil
			}
			return b, nil
		},
		"queues.postActionChoices": func(a []json.RawMessage) (any, error) {
			caps, err := capsArg(a[0])
			if err != nil {
				return nil, err
			}
			current := downexv1.PostAction_POST_ACTION_UNSPECIFIED
			if len(a) > 1 && !IsUndefined(a[1]) {
				s, err := str(a[1])
				if err != nil {
					return nil, err
				}
				current = postAction(s)
			}
			var out []string
			for _, p := range queues.PostActionChoices(caps, current) {
				out = append(out, postActionName(p))
			}
			return out, nil
		},
		"queues.queueForm": func(a []json.RawMessage) (any, error) {
			q, err := queueArg(a[0])
			if err != nil {
				return nil, err
			}
			now := fixedNow
			if len(a) > 1 && !IsUndefined(a[1]) {
				if now, err = Num(a[1]); err != nil {
					return nil, err
				}
			}
			loc, err := time.LoadLocation(format.Locale().TimeZone())
			if err != nil {
				return nil, err
			}
			return formJSON(queues.NewForm(q, time.UnixMilli(int64(now)).In(loc))), nil
		},
		"queues.queueNote": func(a []json.RawMessage) (any, error) {
			q, err := queueArg(a[0])
			return queues.QueueNote(q), err
		},
		"queues.queueSpec": func(a []json.RawMessage) (any, error) {
			q, err := queueArg(a[0])
			if err != nil {
				return nil, err
			}
			spec := queues.Spec(q)
			// queueSpec(q, patch) spreads the patch over the spec; the Go caller sets the field.
			if len(a) > 1 && !IsUndefined(a[1]) {
				var patch struct {
					PostAction *string `json:"postAction"`
				}
				if err := json.Unmarshal(a[1], &patch); err != nil {
					return nil, err
				}
				if patch.PostAction != nil {
					spec.PostAction = postAction(*patch.PostAction)
				}
			}
			return specJSON(spec), nil
		},
		"queues.reorder": func(a []json.RawMessage) (any, error) {
			order, err := ids(a[0])
			if err != nil {
				return nil, err
			}
			moved, err := ids(a[1])
			if err != nil {
				return nil, err
			}
			b, err := before(a[2])
			return queues.Reorder(order, moved, b), err
		},
		"queues.scheduleSummary": func(a []json.RawMessage) (any, error) {
			var w wireSchedule
			if err := json.Unmarshal(a[0], &w); err != nil {
				return nil, err
			}
			return queues.ScheduleSummary(w.proto()), nil
		},
		"queues.secondsLeft": func(a []json.RawMessage) (any, error) {
			deadline, err := num(a[0])
			if err != nil {
				return nil, err
			}
			now, err := num(a[1])
			return queues.SecondsLeft(deadline, now), err
		},
		"queues.sortQueues": func(a []json.RawMessage) (any, error) {
			list, err := queuesArg(a[0])
			if err != nil {
				return nil, err
			}
			var out []any
			for _, q := range queues.SortQueues(list) {
				out = append(out, queueJSON(q))
			}
			return out, nil
		},
		"queues.timeToMinutes": func(a []json.RawMessage) (any, error) {
			s, err := str(a[0])
			if err != nil {
				return nil, err
			}
			m, ok := queues.TimeToMinutes(s)
			if !ok {
				return nil, nil // null: not a time
			}
			return m, nil
		},
		"queues.validateQueueForm": func(a []json.RawMessage) (any, error) {
			form, err := formArg(a[0])
			if err != nil {
				return nil, err
			}
			others, err := queuesArg(a[1])
			if err != nil {
				return nil, err
			}
			caps, err := capsArg(a[2])
			if err != nil {
				return nil, err
			}
			var base *downexv1.Queue
			if len(a) > 3 {
				if base, err = queueArg(a[3]); err != nil {
					return nil, err
				}
			}
			spec, errs, ok := queues.ValidateForm(form, others, caps, base)
			if !ok {
				return map[string]any{"ok": false, "errors": errorsJSON(errs)}, nil
			}
			return map[string]any{"ok": true, "spec": specJSON(spec)}, nil
		},
		"queues.whenDoneText": func(a []json.RawMessage) (any, error) {
			s, err := str(a[0])
			return queues.WhenDoneText(postAction(s)), err
		},
	}
}
