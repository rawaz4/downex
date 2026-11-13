package daemon

import (
	"testing"
	"time"

	"downex/internal/engine"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
)

// A once schedule weeks ahead keeps the scheduler's timer armed and the wake timer set
// for its start, and starts the queue on its date without any outside re-evaluation.
// Afterwards (an enabled schedule without further events) the scheduler keeps checking.
func TestOnceScheduleWeeksAhead(t *testing.T) {
	const size, seed = 64 << 10, 80
	ts := serve(t, testserver.Options{Size: size, Seed: seed})
	fc := engine.NewFakeClock(time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC))
	wake := &fakeWake{}
	h := newHarness(t, harnessOptions{clock: fc, location: time.UTC, wake: wake})
	q := h.createQueue(&downexv1.Queue{Name: "Later", Schedule: &downexv1.Schedule{
		Enabled: true, StartMinute: 9 * 60, OnceDate: "2026-11-10", WakeComputer: true,
	}})
	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/once", QueueId: q.GetId()}).GetId()
	waitPending(t, fc, 1)
	start := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	waitUntil(t, "the wake timer", func() bool { return wake.last().Equal(start.Add(-wakeLead)) })

	fc.Advance(start.Sub(fc.Now()) + 30*time.Second)
	waitUntil(t, "the scheduled start", func() bool { return h.queue(q.GetId()).GetRunning() })
	checkFile(t, h.waitCompleted(d), seed, size)
	waitPending(t, fc, 1)
}

// A schedule mark ahead of the clock (the clock was a day ahead and is set back) does not
// suppress the schedule: the state is derived again from the latest event at or before
// the corrected time, and the events after it happen again.
func TestClockSetBackReappliesTheSchedule(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 8 << 20, Seed: 81})
	fc := engine.NewFakeClock(time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)) // a day ahead
	clk := &skewClock{FakeClock: fc}
	h := newHarness(t, harnessOptions{clock: clk, location: time.UTC})
	q := h.createQueue(&downexv1.Queue{Name: "Day", Schedule: &downexv1.Schedule{
		Enabled: true, StartMinute: 9 * 60, StopEnabled: true, StopMinute: 17 * 60, Weekdays: model.AllWeekdays,
	}})
	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/day", SpeedLimit: 16 << 10, QueueId: q.GetId()}).GetId()
	running := func(want bool, what string) {
		t.Helper()
		waitUntil(t, what, func() bool { return h.queue(q.GetId()).GetRunning() == want })
		waitPending(t, fc, 1)
	}
	waitPending(t, fc, 1)
	fc.Advance(time.Hour) // 09:00 on the 22nd
	running(true, "the start on the wrong day")
	fc.Advance(8 * time.Hour) // 17:00
	running(false, "the stop on the wrong day")

	clk.set(time.Date(2026, 10, 21, 9, 5, 0, 0, time.UTC))
	h.m.Reevaluate()
	running(true, "the open window of the corrected day")
	h.waitReceived(d, 1)
	waitUntil(t, "the corrected mark to be stored", func() bool {
		return storedQueue(t, h.st, q.GetId()).ScheduleMark.Equal(time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC))
	})
	fc.Advance(7*time.Hour + 55*time.Minute) // 17:00 on the 21st
	running(false, "the stop of the corrected day")
	fc.Advance(16 * time.Hour) // 09:00 on the 22nd, again
	running(true, "the next day's start")
}
