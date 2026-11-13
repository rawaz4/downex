package daemon

import (
	"context"
	"time"

	"connectrpc.com/connect"

	"downex/internal/engine"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
)

// The schedule engine (docs/M6-SPEC.md §1) is one goroutine that sleeps until the next
// schedule event of any queue (a start, a stop or a repeat), then applies every event
// that is due: for each queue the latest event after its ScheduleMark (the latest one
// applied, persisted) wins, so starts missed while the computer was off or asleep run
// when the daemon sees them, if their window is still open, and a missed stop stops the
// queue. It re-evaluates when a queue changes, when Reevaluate is called (the system
// resumed from sleep, the clock or time zone changed), and at least every
// scheduleRecheck while a schedule is enabled, which also catches clock changes and
// sleep on systems whose timers do not count the time asleep. A mark ahead of the clock
// means the clock was set back over applied events (scheduleRollback): the state is
// derived again from the latest event before the corrected time. Times come from
// Config.Clock in Config.Location (each evaluation reads time.Local when it is nil).

// scheduleRecheck bounds the scheduler's sleep while a schedule is enabled (also one
// without a next event in sight).
const scheduleRecheck = 15 * time.Minute

// scheduleRollback is how far a queue's ScheduleMark may lie ahead of the clock before
// the clock counts as set back over the events applied up to the mark (the clock was
// ahead when they were applied: set by hand, or a skewed hardware clock at boot). The
// queue's state is then derived again from the latest event at or before the corrected
// time, and the events after it happen again. Smaller differences are corrections of a
// clock that was barely ahead and change nothing.
const scheduleRollback = time.Minute

// location returns the zone that schedules are read in.
func (m *Manager) location() *time.Location {
	if m.cfg.Location != nil {
		return m.cfg.Location
	}
	return time.Local
}

// Reevaluate makes the scheduler apply due schedule events now, and reads a system or
// PAC proxy configuration again (the network may have changed). downexd calls it when
// the system resumed from sleep or its clock or time zone changed; it never blocks.
func (m *Manager) Reevaluate() {
	m.reevaluateSchedules()
	if mode := m.Settings().Network.Proxy.Mode; mode == ProxyModeSystem || mode == ProxyModePAC {
		m.kickNetwork()
	}
}

// reevaluateSchedules makes the scheduler apply due schedule events now (a queue
// changed); it never blocks.
func (m *Manager) reevaluateSchedules() {
	select {
	case m.schedKick <- struct{}{}:
	default:
	}
}

// runScheduler is the schedule engine's goroutine; it ends at Shutdown.
func (m *Manager) runScheduler() {
	defer m.wg.Done()
	for {
		next := m.evaluateSchedules()
		var (
			t engine.Timer
			c <-chan time.Time
		)
		if !next.IsZero() {
			d := min(max(next.Sub(m.clock.Now()), 0), scheduleRecheck)
			t = m.clock.NewTimer(d)
			c = t.C()
		}
		select {
		case <-c:
		case <-m.schedKick:
		case <-m.schedDone:
			if t != nil {
				t.Stop()
			}
			return
		}
		if t != nil {
			t.Stop()
		}
	}
}

// evaluateSchedules applies the due schedule events of every queue (and ends overdue
// post-queue countdowns), arms the wake timer and returns when the scheduler should look
// again: the next event, at the latest scheduleRecheck from now while a schedule is
// enabled (zero: no schedule is enabled). Schedules follow the wall clock.
func (m *Manager) evaluateSchedules() time.Time {
	now, loc := m.clock.Now().Round(0), m.location()
	var (
		next   time.Time
		repeat []model.DownloadID
	)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return time.Time{}
	}
	m.cancelOverduePostsLocked(now)
	enabled := false
	for _, q := range m.sortedQueuesLocked() {
		s := q.q.Schedule
		if !s.Enabled {
			q.repeatAt, q.repeatWin = time.Time{}, time.Time{}
			continue
		}
		enabled = true
		mark := q.q.ScheduleMark
		if mark.Round(0).Sub(now) > scheduleRollback {
			// The clock was set back over the events applied: derive the state again.
			m.log.Info("the clock was set back before the queue's last schedule event; applying its schedule again",
				"queue", int64(q.id()), "mark", mark, "now", now)
			mark, q.repeatAt, q.repeatWin = time.Time{}, time.Time{}, time.Time{}
			q.q.ScheduleMark = now
			m.persistQueueLocked(q)
		}
		if ev, ok := latestEvent(s, mark, now, loc); ok {
			q.q.ScheduleMark = ev.at
			m.persistQueueLocked(q)
			switch {
			case ev.kind == evStart && ev.win.contains(now):
				m.startQueueLocked(q, "schedule")
			case ev.kind == evStart:
				m.log.Info("scheduled start missed: its window has closed", "queue", int64(q.id()), "start", ev.at)
			case ev.kind == evStop:
				m.stopQueueLocked(q, "schedule")
			}
		}
		if s.RepeatMinutes > 0 {
			every := time.Duration(s.RepeatMinutes) * time.Minute
			w, inside := activeWindow(s, now, loc)
			switch {
			case !inside || !q.q.Running:
				q.repeatAt, q.repeatWin = time.Time{}, time.Time{}
			case q.repeatAt.IsZero() || !q.repeatWin.Equal(w.start):
				q.repeatAt, q.repeatWin = nextRepeat(w, now, every), w.start
			case !now.Before(q.repeatAt):
				repeat = append(repeat, m.unfinishedLocked(q)...)
				q.repeatAt = nextRepeat(w, now, every)
			}
			next = earliest(next, q.repeatAt)
		}
		if ev, ok := nextEvent(s, now, loc); ok {
			next = earliest(next, ev.at)
		}
	}
	if enabled {
		next = earliest(next, now.Add(scheduleRecheck))
	}
	wake := m.nextWakeLocked(now, loc)
	m.scheduleLocked()
	m.mu.Unlock()

	m.setWake(wake)
	if len(repeat) > 0 {
		m.requeueUnfinished(repeat)
	}
	return next
}

// unfinishedLocked returns the downloads of q that a repeat queues again: failed,
// paused or canceled ones that are not running.
func (m *Manager) unfinishedLocked(q *queueState) []model.DownloadID {
	var out []model.DownloadID
	for _, id := range q.order {
		e := m.entries[id]
		if e == nil || e.run != nil || e.foreign {
			continue
		}
		switch e.rec.State {
		case model.StateFailed, model.StatePaused, model.StateCanceled:
			out = append(out, id)
		}
	}
	return out
}

// requeueUnfinished queues the downloads ids again (as Resume does).
func (m *Manager) requeueUnfinished(ids []model.DownloadID) {
	ctx, cancel := context.WithTimeout(context.Background(), finalizeTimeout)
	defer cancel()
	n := 0
	for _, id := range ids {
		_, err := m.Resume(ctx, &downexv1.ResumeRequest{Id: int64(id)})
		switch {
		case err == nil:
			n++
		case skippable(err), connect.CodeOf(err) == connect.CodeUnavailable:
		default:
			m.log.Warn("scheduled repeat cannot queue a download again", "download", int64(id), "err", err)
		}
	}
	m.log.Info("scheduled repeat queued unfinished downloads again", "downloads", n)
}

// nextWakeLocked returns the first scheduled start, after now, of a queue whose
// schedule wakes the computer (zero: none).
func (m *Manager) nextWakeLocked(now time.Time, loc *time.Location) time.Time {
	var t time.Time
	for _, q := range m.queues {
		if s := q.q.Schedule; s.Enabled && s.WakeComputer {
			if st, ok := nextStart(s, now, loc); ok {
				t = earliest(t, st)
			}
		}
	}
	return t
}

// setWake arms Config.Wake for start (wakeLead early), or cancels it for zero.
func (m *Manager) setWake(start time.Time) {
	if m.cfg.Wake == nil {
		return
	}
	m.wakeMu.Lock()
	defer m.wakeMu.Unlock()
	if start.Equal(m.wakeSet) {
		return
	}
	m.wakeSet = start
	if start.IsZero() {
		if err := m.cfg.Wake.Cancel(); err != nil {
			m.log.Warn("cannot cancel the wake timer", "err", err)
		}
		return
	}
	at := start.Add(-wakeLead)
	if err := m.cfg.Wake.WakeAt(at); err != nil {
		m.log.Warn("cannot arm the wake timer for a scheduled start", "start", start, "err", err)
		return
	}
	m.log.Info("wake timer armed for a scheduled start", "start", start, "wake", at)
}
