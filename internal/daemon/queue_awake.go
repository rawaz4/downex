package daemon

import "downex/internal/model"

// KeepAwake keeps the computer from sleeping while idle as long as it is held
// (Config.KeepAwake; downexd passes the platform's power request for the user's own
// daemon only, tests a fake). Idle sleep would otherwise stop a scheduled or
// post-action queue in the middle of its work, so that its post-queue action never
// came, and a computer that a wake timer woke for a scheduled start goes back to sleep
// after a couple of minutes unless something asks it to stay awake. An explicit sleep or
// shutdown (Power) still happens while it is held. Hold and Release are called from one
// goroutine and must not block for long; holding twice is holding once.
type KeepAwake interface {
	Hold() error
	Release() error
}

// kickAwake makes the keep-awake goroutine look again whether the computer must stay
// awake (see awakeWantedLocked); it never blocks. The callers change what that depends
// on: the waiting set, the runs, the queues and their countdowns.
func (m *Manager) kickAwake() {
	select {
	case m.awakeKick <- struct{}{}:
	default:
	}
}

// awakeWantedLocked reports whether the computer must stay awake: a post-queue countdown
// is pending, or a queue whose work matters after the user left (one with a post-queue
// action or an enabled schedule) has downloads running, or waiting while it runs. Other
// downloads leave the system's sleep alone, as before queues existed.
func (m *Manager) awakeWantedLocked() bool {
	if m.closed {
		return false
	}
	for _, q := range m.queues {
		if q.post != nil {
			return true
		}
		if q.q.PostAction == model.PostActionNone && !q.q.Schedule.Enabled {
			continue
		}
		for _, id := range q.order {
			if e := m.entries[id]; e != nil && e.run != nil {
				return true
			}
			if _, ok := m.waiting[id]; ok && q.q.Running {
				return true
			}
		}
	}
	return false
}

// runAwake holds Config.KeepAwake while awakeWantedLocked and releases it otherwise,
// re-evaluating at every kickAwake, until Shutdown (which releases it). A failed Hold or
// Release is logged and tried again at the next change.
func (m *Manager) runAwake() {
	defer m.wg.Done()
	held := false
	apply := func(want bool) {
		if want == held {
			return
		}
		var err error
		if want {
			err = m.cfg.KeepAwake.Hold()
		} else {
			err = m.cfg.KeepAwake.Release()
		}
		if err != nil {
			m.log.Warn("cannot change whether the computer stays awake for the queues", "awake", want, "err", err)
			return
		}
		held = want
		m.log.Info("keeping the computer awake for the queues", "awake", want)
	}
	for {
		select {
		case <-m.awakeKick:
		case <-m.schedDone:
			apply(false)
			return
		}
		m.mu.Lock()
		want := m.awakeWantedLocked()
		m.mu.Unlock()
		apply(want)
	}
}
