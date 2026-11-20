package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"connectrpc.com/connect"

	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/store"
)

// Queue limits (docs/M6-SPEC.md §1).
const (
	// MaxQueueNameRunes bounds a queue name.
	MaxQueueNameRunes = 64
	// MaxQueues bounds the number of queues.
	MaxQueues = 64
	// MaxRepeatMinutes bounds Schedule.repeat_minutes (one day).
	MaxRepeatMinutes = 24 * 60
	// MaxMoveDownloads bounds the downloads of one MoveDownloads call.
	MaxMoveDownloads = 10_000
)

// queueSettings validates the client-settable fields of p (name, max_concurrent,
// speed_limit, schedule, post_action); self is the queue being updated (0 for a new
// one), whose own name does not count as taken. The caller holds queueMu.
func (m *Manager) queueSettings(p *downexv1.Queue, self model.QueueID) (model.Queue, error) {
	var q model.Queue
	if p == nil {
		return q, errInvalid("queue is required")
	}
	name := strings.TrimSpace(p.GetName())
	switch {
	case name == "":
		return q, errInvalid("queue name is required")
	case !utf8.ValidString(name):
		return q, errInvalid("queue name is not valid UTF-8")
	case utf8.RuneCountInString(name) > MaxQueueNameRunes:
		return q, errInvalid("queue name is longer than %d characters", MaxQueueNameRunes)
	case strings.ContainsFunc(name, unicode.IsControl):
		return q, errInvalid("queue name contains control characters")
	}
	if n := p.GetMaxConcurrent(); n < 0 || n > MaxConcurrentDownloadsLimit {
		return q, errInvalid("max_concurrent must be between 0 and %d", MaxConcurrentDownloadsLimit)
	}
	if p.GetSpeedLimit() < 0 {
		return q, errInvalid("speed_limit must not be negative")
	}
	s, err := scheduleFromProto(p.GetSchedule())
	if err != nil {
		return q, err
	}
	action, ok := postActionFromProto(p.GetPostAction())
	if !ok {
		return q, errInvalid("unknown post_action %d", p.GetPostAction())
	}
	if err := m.checkPostAction(action); err != nil {
		return q, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, other := range m.queues {
		if other.id() != self && strings.EqualFold(other.q.Name, name) {
			return q, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("a queue named %q exists", other.q.Name))
		}
	}
	return model.Queue{
		Name: name, MaxConcurrent: int(p.GetMaxConcurrent()), SpeedLimit: p.GetSpeedLimit(), Schedule: s, PostAction: action,
	}, nil
}

// scheduleFromProto validates a schedule (nil: none).
func scheduleFromProto(p *downexv1.Schedule) (model.Schedule, error) {
	s := model.Schedule{
		Enabled: p.GetEnabled(), StartMinute: int(p.GetStartMinute()), StopEnabled: p.GetStopEnabled(),
		StopMinute: int(p.GetStopMinute()), Weekdays: int(p.GetWeekdays()), OnceDate: strings.TrimSpace(p.GetOnceDate()),
		WakeComputer: p.GetWakeComputer(), RepeatMinutes: int(p.GetRepeatMinutes()),
	}
	const day = 24 * 60
	switch {
	case s.StartMinute < 0 || s.StartMinute >= day:
		return s, errInvalid("schedule.start_minute must be between 0 and %d", day-1)
	case s.StopMinute < 0 || s.StopMinute >= day:
		return s, errInvalid("schedule.stop_minute must be between 0 and %d", day-1)
	case s.Weekdays < 0 || s.Weekdays > model.AllWeekdays:
		return s, errInvalid("schedule.weekdays must be a set of the bits 0 (Monday) to 6 (Sunday)")
	case s.RepeatMinutes < 0 || s.RepeatMinutes > MaxRepeatMinutes:
		return s, errInvalid("schedule.repeat_minutes must be between 0 and %d", MaxRepeatMinutes)
	}
	if s.OnceDate != "" {
		if _, ok := parseDate(s.OnceDate); !ok {
			return s, errInvalid("schedule.once_date must be a date YYYY-MM-DD")
		}
	}
	if s.Enabled && s.Weekdays == 0 && s.OnceDate == "" {
		return s, errInvalid("schedule needs weekdays or a once_date")
	}
	return s, nil
}

// CreateQueue adds a stopped queue with the settings of p (its id, running, is_main and
// download_ids are ignored). Its schedule applies from now on: a start that lies in the
// past is not caught up.
func (m *Manager) CreateQueue(ctx context.Context, p *downexv1.Queue) (*downexv1.Queue, error) {
	m.queueMu.Lock()
	defer m.queueMu.Unlock()
	if m.isClosed() {
		return nil, errShuttingDown()
	}
	q, err := m.queueSettings(p, 0)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	n := len(m.queues)
	m.mu.Unlock()
	if n >= MaxQueues {
		return nil, errPrecondition("there are already %d queues", n)
	}
	q.ScheduleMark = m.clock.Now()
	if err := m.qstore.CreateQueue(context.WithoutCancel(ctx), &q); err != nil {
		return nil, errInternal("create queue", err)
	}
	m.mu.Lock()
	qs := newQueueState(q, m.queueParent())
	m.queues[q.ID] = qs
	m.hub.publishQueue(q.ID, kindUpdated)
	pq := m.protoQueueLocked(qs)
	m.mu.Unlock()
	m.reevaluateSchedules()
	m.log.Info("queue created", "queue", int64(q.ID), "name", q.Name)
	return pq, nil
}

// UpdateQueue replaces the name, max_concurrent, speed_limit, schedule and post_action
// of queue p.id and applies them at once: the speed limit changes live, a lower
// max_concurrent requeues the runs latest in its order (see rebalanceLocked), a changed
// schedule applies from now on, and a changed post_action cancels a pending countdown.
func (m *Manager) UpdateQueue(ctx context.Context, p *downexv1.Queue) (*downexv1.Queue, error) {
	m.queueMu.Lock()
	defer m.queueMu.Unlock()
	if m.isClosed() {
		return nil, errShuttingDown()
	}
	id := model.QueueID(p.GetId())
	m.mu.Lock()
	cur := m.queues[id]
	m.mu.Unlock()
	if cur == nil {
		return nil, errQueueNotFound(id)
	}
	next, err := m.queueSettings(p, id)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	upd := cur.q.Clone()
	m.mu.Unlock()
	scheduleChanged := upd.Schedule != next.Schedule
	upd.Name, upd.MaxConcurrent, upd.SpeedLimit = next.Name, next.MaxConcurrent, next.SpeedLimit
	upd.Schedule, upd.PostAction = next.Schedule, next.PostAction
	if scheduleChanged {
		upd.ScheduleMark = m.clock.Now()
	}
	if err := m.qstore.UpdateQueue(context.WithoutCancel(ctx), &upd); err != nil {
		if errors.Is(err, store.ErrQueueNotFound) {
			return nil, errQueueNotFound(id)
		}
		return nil, errInternal("update queue", err)
	}
	m.mu.Lock()
	q := m.queues[id]
	if q == nil { // deleted meanwhile (DeleteQueue holds queueMu too: cannot happen)
		m.mu.Unlock()
		return nil, errQueueNotFound(id)
	}
	if q.q.PostAction != upd.PostAction {
		m.cancelPostLocked(q, "the post-queue action changed")
	}
	q.q.Name, q.q.MaxConcurrent, q.q.SpeedLimit = upd.Name, upd.MaxConcurrent, upd.SpeedLimit
	q.q.Schedule, q.q.PostAction = upd.Schedule, upd.PostAction
	if scheduleChanged {
		q.q.ScheduleMark = upd.ScheduleMark
		q.repeatAt, q.repeatWin = time.Time{}, time.Time{} // recomputed by the scheduler
	}
	q.lim.SetLimit(upd.SpeedLimit)
	m.rebalanceLocked()
	m.scheduleLocked()
	m.hub.publishQueue(id, kindUpdated)
	pq := m.protoQueueLocked(q)
	m.mu.Unlock()
	m.reevaluateSchedules()
	m.log.Info("queue updated", "queue", int64(id), "name", upd.Name, "max_concurrent", upd.MaxConcurrent,
		"speed_limit", upd.SpeedLimit, "schedule", upd.Schedule.Enabled, "post_action", upd.PostAction.String())
	return pq, nil
}

// DeleteQueue removes queue id; its downloads move, in their order, to the end of the
// main queue (running ones keep running while the main queue runs, else they wait
// there). The main queue cannot be deleted.
func (m *Manager) DeleteQueue(ctx context.Context, id int64) error {
	m.queueMu.Lock()
	defer m.queueMu.Unlock()
	if m.isClosed() {
		return errShuttingDown()
	}
	qid := model.QueueID(id)
	if qid == model.MainQueueID {
		return errPrecondition("the main queue cannot be deleted")
	}
	m.mu.Lock()
	known := m.queues[qid] != nil
	m.mu.Unlock()
	if !known {
		return errQueueNotFound(qid)
	}
	if err := m.qstore.DeleteQueue(context.WithoutCancel(ctx), qid); err != nil {
		if errors.Is(err, store.ErrQueueNotFound) {
			return errQueueNotFound(qid)
		}
		return errInternal("delete queue", err)
	}
	m.mu.Lock()
	q, main := m.queues[qid], m.queues[model.MainQueueID]
	m.cancelPostLocked(q, "the queue was deleted")
	moved := q.order
	q.order = nil
	delete(m.queues, qid)
	m.hub.publishQueue(qid, kindRemoved)
	if len(moved) > 0 {
		m.moveLocked(moved, main, -1)
	}
	m.mu.Unlock()
	m.reevaluateSchedules()
	m.log.Info("queue deleted", "queue", id, "downloads", len(moved))
	return nil
}

// StartQueue makes queue id run: its waiting downloads start as the limits allow.
func (m *Manager) StartQueue(_ context.Context, id int64) (*downexv1.Queue, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errShuttingDown()
	}
	q := m.queues[model.QueueID(id)]
	if q == nil {
		return nil, errQueueNotFound(model.QueueID(id))
	}
	m.startQueueLocked(q, "user")
	m.scheduleLocked()
	return m.protoQueueLocked(q), nil
}

// StopQueue stops queue id: its running downloads stop (progress committed) and wait in
// the queue again, a pending post-queue action is cancelled. It returns once they
// stopped (or ctx ended).
func (m *Manager) StopQueue(ctx context.Context, id int64) (*downexv1.Queue, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errShuttingDown()
	}
	q := m.queues[model.QueueID(id)]
	if q == nil {
		m.mu.Unlock()
		return nil, errQueueNotFound(model.QueueID(id))
	}
	stopping := m.stopQueueLocked(q, "user")
	m.mu.Unlock()
	for _, r := range stopping {
		if err := waitDone(ctx, r.done); err != nil {
			return nil, err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.protoQueueLocked(q), nil
}

// MoveDownloads moves the downloads ids (in that order, repeats ignored) into queue
// queueID (0 = the main queue) at position, the 0-based index among the downloads that
// queue keeps (-1 or past the end: appended). A running download whose new queue is not
// running stops and waits there. It returns the target queue.
func (m *Manager) MoveDownloads(ctx context.Context, r *downexv1.MoveDownloadsRequest) (*downexv1.Queue, error) {
	raw := r.GetIds()
	switch {
	case len(raw) == 0:
		return nil, errInvalid("ids are required")
	case len(raw) > MaxMoveDownloads:
		return nil, errInvalid("at most %d downloads can be moved at once", MaxMoveDownloads)
	case r.GetPosition() < -1:
		return nil, errInvalid("position must be -1 (the end) or a 0-based index")
	case r.GetQueueId() < 0:
		return nil, errInvalid("queue_id must not be negative")
	}
	ids := make([]model.DownloadID, len(raw))
	for i, id := range raw {
		ids[i] = model.DownloadID(id)
	}
	ids = model.UniqueDownloadIDs(ids)
	for _, id := range ids {
		if _, err := m.lookup(ctx, id); err != nil {
			return nil, err
		}
	}
	m.queueMu.Lock()
	defer m.queueMu.Unlock()
	if m.isClosed() {
		return nil, errShuttingDown()
	}
	m.mu.Lock()
	to := m.queueLocked(model.QueueID(r.GetQueueId()))
	m.mu.Unlock()
	if to == nil {
		return nil, errQueueNotFound(model.QueueID(r.GetQueueId()))
	}
	if err := m.qstore.MoveDownloads(context.WithoutCancel(ctx), ids, to.id(), int(r.GetPosition())); err != nil {
		switch {
		case errors.Is(err, store.ErrQueueNotFound):
			return nil, errQueueNotFound(to.id())
		case errors.Is(err, store.ErrNotFound):
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, errInternal("move downloads", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.moveLocked(ids, to, int(r.GetPosition()))
	return m.protoQueueLocked(to), nil
}

// queueForAdd resolves the queue_id of Add, AddMedia and Confirm (0 = def).
func (m *Manager) queueForAdd(id int64, def model.QueueID) (model.QueueID, error) {
	if id < 0 {
		return 0, errInvalid("queue_id must not be negative")
	}
	if id == 0 {
		return def, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.queues[model.QueueID(id)] == nil {
		return 0, errInvalid("queue_id %d: no such queue", id)
	}
	return model.QueueID(id), nil
}
