package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"downex/internal/limiter"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/store"
)

// Queues (docs/M6-SPEC.md §1). Every download belongs to exactly one queue
// (entry.qid); the main queue (model.MainQueueID) receives downloads added without a
// queue and runs by default, as the single FIFO of earlier builds did. A queued
// download waits in Manager.waiting; it starts when its queue runs, the queue runs fewer
// than its max_concurrent (0 = max_concurrent_downloads) and fewer than
// max_concurrent_downloads run in all, in the queue's order. Running queues take turns
// while the global limit leaves room: one start each, by queue id, beginning after the
// queue that started a download last (round robin). The speed limits form a chain:
// global → queue → download (queue_limit.go); a running download that changes queues
// takes its new queue's limit at once.

// queueState is the daemon's view of one queue. Guarded by Manager.mu.
type queueState struct {
	q     model.Queue        // settings, Running and ScheduleMark; q.DownloadIDs is unused (see order)
	order []model.DownloadID // every download of the queue (owned or foreign), in queue order
	lim   *chainLink         // below Manager.global, parent of the queue's runs
	// completed counts the downloads of the queue that completed since it last started
	// running (or since the last finish): a queue only finishes after at least one.
	completed int
	post      *postPending // the post-queue action counting down, nil when none
	repeatAt  time.Time    // next repeat of the active schedule window (zero: none)
	repeatWin time.Time    // start of the window repeatAt belongs to
	dirty     bool         // memory is newer than the store (see persistQueueLocked)
}

func (q *queueState) id() model.QueueID { return q.q.ID }

func newQueueState(q model.Queue, global limiter.Limiter) *queueState {
	order := slices.Clone(q.DownloadIDs)
	q.DownloadIDs = nil
	return &queueState{q: q, order: order, lim: newChainLink(q.SpeedLimit, global)}
}

// loadQueues indexes the stored queues (recovery, before the downloads).
func (m *Manager) loadQueues(ctx context.Context) error {
	list, err := m.qstore.ListQueues(ctx)
	if err != nil {
		return fmt.Errorf("daemon: list queues: %w", err)
	}
	for _, q := range list {
		m.queues[q.ID] = newQueueState(q, m.global)
	}
	if m.queues[model.MainQueueID] == nil { // every store creates it; keep going without it stored
		m.log.Warn("the store has no main queue; using a default one")
		m.queues[model.MainQueueID] = newQueueState(model.Queue{ID: model.MainQueueID, Name: store.MainQueueName, Running: true}, m.global)
	}
	return nil
}

// queueLocked returns queue id (0 = the main queue), or nil.
func (m *Manager) queueLocked(id model.QueueID) *queueState {
	if id == 0 {
		id = model.MainQueueID
	}
	return m.queues[id]
}

// queueOfLocked returns the queue of e.
func (m *Manager) queueOfLocked(e *entry) *queueState {
	if q := m.queues[e.qid]; q != nil {
		return q
	}
	return m.queues[model.MainQueueID]
}

// sortedQueuesLocked returns the queues ordered by id.
func (m *Manager) sortedQueuesLocked() []*queueState {
	out := make([]*queueState, 0, len(m.queues))
	for _, q := range m.queues {
		out = append(out, q)
	}
	slices.SortFunc(out, func(a, b *queueState) int { return cmp.Compare(a.id(), b.id()) })
	return out
}

// joinQueueLocked makes e a member of queue qid (an unknown queue: the main queue) at
// the end of its order, unless it is already listed there.
func (m *Manager) joinQueueLocked(e *entry, qid model.QueueID) {
	q := m.queueLocked(qid)
	if q == nil {
		m.log.Warn("download belongs to an unknown queue; it joins the main queue", "download", int64(e.id), "queue", int64(qid))
		q = m.queues[model.MainQueueID]
	}
	if old := m.queues[e.qid]; old != nil && old != q {
		old.order = slices.DeleteFunc(old.order, func(id model.DownloadID) bool { return id == e.id })
		m.hub.publishQueue(old.id(), kindUpdated)
	}
	e.qid = q.id()
	if e.run != nil {
		e.run.lim.setParent(q.lim)
	}
	if !slices.Contains(q.order, e.id) {
		q.order = append(q.order, e.id)
		m.posDirty = true
		m.hub.publishQueue(q.id(), kindUpdated)
	}
	m.awaitingJoinedLocked(e, q)
}

// awaitingJoinedLocked cancels the post-queue countdown of q when e, which has just
// joined it, awaits confirmation: the queue has work again (queueBusyLocked), and the
// user, who has yet to decide, may be looking at the confirmation right now. (Queued
// work cancels it when it starts, or aborts it at the deadline.)
func (m *Manager) awaitingJoinedLocked(e *entry, q *queueState) {
	if q.post != nil && e.rec.State == model.StateAwaitingConfirmation {
		m.cancelPostLocked(q, "a download awaiting confirmation joined the queue")
	}
}

// leaveQueueLocked removes download id from its queue's order.
func (m *Manager) leaveQueueLocked(e *entry) {
	if q := m.queues[e.qid]; q != nil {
		if i := slices.Index(q.order, e.id); i >= 0 {
			q.order = slices.Delete(q.order, i, i+1)
			m.posDirty = true
			m.hub.publishQueue(q.id(), kindUpdated)
		}
	}
}

func (m *Manager) enqueueLocked(id model.DownloadID) {
	if _, ok := m.waiting[id]; !ok {
		m.waiting[id] = struct{}{}
		m.posDirty = true
		m.kickAwake()
	}
}

func (m *Manager) dequeueLocked(id model.DownloadID) bool {
	_, ok := m.waiting[id]
	if ok {
		delete(m.waiting, id)
		m.posDirty = true
		m.kickAwake()
	}
	return ok
}

// parkLocked takes e out of the waiting set while an operation (Pause, Resume,
// SetDownloadOptions, Remove) writes its record, so that the scheduler cannot start it
// meanwhile, and reports whether it was waiting. Until the operation ends (unparkLocked,
// or restore when it failed) its queue still counts it as work (queueBusyLocked): the
// queue's last run completing meanwhile must not finish, and so stop, the queue that the
// download may go back into.
func (m *Manager) parkLocked(e *entry) bool {
	if !m.dequeueLocked(e.id) {
		return false
	}
	e.parked = true
	return true
}

// unparkLocked ends parkLocked: the operation has stored the record and queued the
// download again or left it out of the waiting set for good.
func (m *Manager) unparkLocked(e *entry) { e.parked = false }

// queuePositionLocked returns the 1-based position of a waiting download among the
// waiting downloads of its queue, in queue order (0 when it is not waiting).
func (m *Manager) queuePositionLocked(id model.DownloadID) int {
	if _, ok := m.waiting[id]; !ok {
		return 0
	}
	if m.posDirty || m.positions == nil {
		m.positions = make(map[model.DownloadID]int, len(m.waiting))
		for _, q := range m.queues {
			n := 0
			for _, d := range q.order {
				if _, ok := m.waiting[d]; ok {
					n++
					m.positions[d] = n
				}
			}
		}
		m.posDirty = false
	}
	return m.positions[id]
}

// capLocked returns how many downloads queue q may run at once.
func (m *Manager) capLocked(q *queueState) int {
	if q.q.MaxConcurrent > 0 {
		return q.q.MaxConcurrent
	}
	return m.settings.MaxConcurrentDownloads
}

// runsByQueueLocked counts the runs of each queue (including runs that are stopping).
func (m *Manager) runsByQueueLocked() map[model.QueueID]int {
	n := make(map[model.QueueID]int, len(m.queues))
	for _, e := range m.active {
		n[m.queueOfLocked(e).id()]++
	}
	return n
}

// scheduleLocked starts waiting downloads (see the admission rule above). Downloads
// another process holds are skipped and retried later.
func (m *Manager) scheduleLocked() {
	m.kickAwake() // called after every change of the runs and the queues' settings
	if m.closed {
		return
	}
	now := time.Now()
	var retryAt time.Time
	defer func() { m.armRetryLocked(retryAt) }()
	if m.running >= m.settings.MaxConcurrentDownloads || len(m.waiting) == 0 {
		return
	}
	type cursor struct {
		q *queueState
		i int // next index into q.order
	}
	runs := m.runsByQueueLocked()
	var turn []*cursor
	for _, q := range m.sortedQueuesLocked() {
		if q.q.Running {
			turn = append(turn, &cursor{q: q})
		}
	}
	// Round robin: the queue after the one that started a download last goes first.
	if i := slices.IndexFunc(turn, func(c *cursor) bool { return c.q.id() > m.lastTurn }); i > 0 {
		turn = slices.Concat(turn[i:], turn[:i])
	}
	for len(turn) > 0 && m.running < m.settings.MaxConcurrentDownloads {
		next := turn[:0]
		for _, c := range turn {
			if m.running >= m.settings.MaxConcurrentDownloads {
				next = append(next, c)
				continue
			}
			if runs[c.q.id()] >= m.capLocked(c.q) {
				continue
			}
			started := false
			for !started && c.i < len(c.q.order) {
				id := c.q.order[c.i]
				c.i++
				if _, ok := m.waiting[id]; !ok {
					continue
				}
				e := m.entries[id]
				switch {
				case e == nil:
					m.dequeueLocked(id)
				case now.Before(e.deferUntil):
					retryAt = earliest(retryAt, e.deferUntil)
				case m.startLocked(e, now):
					runs[c.q.id()]++
					m.lastTurn, started = c.q.id(), true
				default:
					retryAt = earliest(retryAt, e.deferUntil)
				}
			}
			if started {
				next = append(next, c)
			}
		}
		turn = next
	}
}

// earliest returns the earlier of a and b, ignoring zero times.
func earliest(a, b time.Time) time.Time {
	switch {
	case a.IsZero():
		return b
	case b.IsZero() || a.Before(b):
		return a
	}
	return b
}

// rebalanceLocked requeues runs while a queue runs more than its limit (the ones
// latest in its order first), then while more run in all than max_concurrent_downloads
// allows (the highest ids first). Single-connection downloads are left running: they
// cannot resume, so stopping one would discard its progress.
func (m *Manager) rebalanceLocked() {
	byQueue := make(map[*queueState][]*entry)
	for _, e := range m.active {
		if e.run.intent == intentNone {
			q := m.queueOfLocked(e)
			byQueue[q] = append(byQueue[q], e)
		}
	}
	var rest []*entry
	for q, list := range byQueue {
		pos := make(map[model.DownloadID]int, len(q.order))
		for i, id := range q.order {
			pos[id] = i
		}
		slices.SortFunc(list, func(a, b *entry) int { return cmp.Compare(pos[b.id], pos[a.id]) })
		excess := len(list) - m.capLocked(q)
		for _, e := range list {
			if excess > 0 && m.requeueRunLocked(e) {
				excess--
				continue
			}
			rest = append(rest, e)
		}
	}
	excess := len(rest) - m.settings.MaxConcurrentDownloads
	slices.SortFunc(rest, func(a, b *entry) int { return cmp.Compare(b.id, a.id) })
	for _, e := range rest {
		if excess <= 0 {
			break
		}
		if m.requeueRunLocked(e) {
			excess--
		}
	}
}

// requeueRunLocked stops e's run so that it waits in its queue again, unless it is a
// single-connection download (see rebalanceLocked). It reports whether it did.
func (m *Manager) requeueRunLocked(e *entry) bool {
	mode := e.rec.Mode
	if p := e.run.progress.Load(); p != nil {
		mode = p.Mode
	}
	if mode == model.ModeSingle {
		return false
	}
	e.run.setIntent(intentRequeue)
	e.run.cancel()
	return true
}

// queueBusyLocked reports whether queue q has work left: downloads that wait, run, await
// confirmation (the user has yet to decide, possibly in the dialog open right now) or are
// parked by an operation that may queue them again (parkLocked).
func (m *Manager) queueBusyLocked(q *queueState) bool {
	for _, id := range q.order {
		if _, ok := m.waiting[id]; ok {
			return true
		}
		if e := m.entries[id]; e != nil && (e.run != nil || e.parked || e.rec.State == model.StateAwaitingConfirmation) {
			return true
		}
	}
	return false
}

// checkFinishedLocked handles the end of a queue's work (docs/M6-SPEC.md §1): a
// running queue that has no work left (queueBusyLocked) after at least one of its
// downloads completed has finished. Its completion count starts again; a queue other than
// the main one stops (it runs again when started or scheduled; one whose schedule
// repeats inside an open window keeps running); a post-queue action starts its
// countdown. It is called only when a run of q ended its download by completing or
// failing: the user pausing, removing or moving the remaining downloads (PauseAll to
// free the line, for one) stops the queue's work but never finishes it, so it never
// starts a shutdown.
func (m *Manager) checkFinishedLocked(q *queueState) {
	if q == nil || m.closed || !q.q.Running || q.completed == 0 || q.post != nil || m.queueBusyLocked(q) {
		return
	}
	q.completed = 0
	m.log.Info("queue finished", "queue", int64(q.id()), "name", q.q.Name, "post_action", q.q.PostAction.String())
	if !q.q.IsMain() && !m.repeatingLocked(q) {
		q.q.Running = false
		m.persistQueueLocked(q)
		m.hub.publishQueue(q.id(), kindUpdated)
	}
	if a := q.q.PostAction; a != model.PostActionNone && a <= model.PostActionLast {
		m.startPostLocked(q)
	}
}

// repeatingLocked reports whether q's schedule repeats inside a window open now.
func (m *Manager) repeatingLocked(q *queueState) bool {
	s := q.q.Schedule
	if !s.Enabled || s.RepeatMinutes <= 0 {
		return false
	}
	_, ok := activeWindow(s, m.clock.Now(), m.location())
	return ok
}

// startQueueLocked starts queue q (StartQueue or its schedule): its waiting downloads
// may start, and its completion count starts again.
func (m *Manager) startQueueLocked(q *queueState, why string) {
	if q.q.Running {
		return
	}
	q.q.Running, q.completed = true, 0
	m.persistQueueLocked(q)
	m.hub.publishQueue(q.id(), kindUpdated)
	m.log.Info("queue started", "queue", int64(q.id()), "name", q.q.Name, "by", why)
}

// stopQueueLocked stops queue q (StopQueue or its schedule): a pending post-queue
// action is cancelled and its running downloads stop and wait in the queue again
// (progress committed), so that starting the queue continues them. It returns the runs
// that were told to stop.
func (m *Manager) stopQueueLocked(q *queueState, why string) []*run {
	var stopping []*run
	for _, e := range m.active {
		if m.queueOfLocked(e) == q && e.run.intent != intentRemove {
			e.run.setIntent(intentRequeue)
			e.run.cancel()
			stopping = append(stopping, e.run)
		}
	}
	m.cancelPostLocked(q, "the queue stopped")
	if q.q.Running {
		q.q.Running = false
		m.persistQueueLocked(q)
		m.hub.publishQueue(q.id(), kindUpdated)
		m.log.Info("queue stopped", "queue", int64(q.id()), "name", q.q.Name, "by", why, "stopping", len(stopping))
	}
	return stopping
}

// moveLocked moves the downloads ids (existing entries, without repeats) into queue to
// at position (see model.PlaceDownloads) after the store did. A running download whose
// new queue is not running stops and waits there (see requeueRunLocked); one that keeps
// running is limited by its new queue's speed limit at once (its old queue's no longer
// applies, also when that queue was deleted). Moving work out of a queue never finishes
// it (see checkFinishedLocked).
func (m *Manager) moveLocked(ids []model.DownloadID, to *queueState, position int) {
	from := make(map[*queueState]bool)
	ids = slices.DeleteFunc(slices.Clone(ids), func(id model.DownloadID) bool { return m.entries[id] == nil })
	for _, id := range ids {
		e := m.entries[id]
		if old := m.queues[e.qid]; old != nil && old != to {
			old.order = slices.DeleteFunc(old.order, func(d model.DownloadID) bool { return d == id })
			from[old] = true
		}
		e.qid = to.id()
		if e.run != nil {
			e.run.lim.setParent(to.lim)
			if !to.q.Running && e.run.intent == intentNone {
				m.requeueRunLocked(e)
			}
		}
		m.awaitingJoinedLocked(e, to)
		m.hub.publish(id, kindUpdated)
	}
	to.order = model.PlaceDownloads(to.order, ids, position)
	m.posDirty = true
	m.hub.publishQueue(to.id(), kindUpdated)
	for q := range from {
		m.hub.publishQueue(q.id(), kindUpdated)
	}
	m.scheduleLocked()
}

// persistQueueLocked records that q changed in memory; a saver goroutine writes every
// changed queue, the latest state last (queueMu orders it with the queue RPCs). Nothing
// is written once the manager is closed.
func (m *Manager) persistQueueLocked(q *queueState) {
	q.dirty = true
	m.kickAwake() // it may have started or stopped
	if m.saving || m.closed {
		return
	}
	m.saving = true
	m.wg.Add(1)
	go m.saveQueues()
}

func (m *Manager) saveQueues() {
	defer m.wg.Done()
	m.queueMu.Lock()
	defer m.queueMu.Unlock()
	for {
		m.mu.Lock()
		var batch []model.Queue
		for _, q := range m.queues {
			if q.dirty {
				q.dirty = false
				batch = append(batch, q.q.Clone())
			}
		}
		if len(batch) == 0 {
			m.saving = false
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()
		for i := range batch {
			ctx, cancel := context.WithTimeout(context.Background(), finalizeTimeout)
			err := m.qstore.UpdateQueue(ctx, &batch[i])
			cancel()
			if err != nil && !errors.Is(err, store.ErrQueueNotFound) {
				m.log.Warn("cannot save queue", "queue", int64(batch[i].ID), "err", err)
			}
		}
	}
}

// protoQueueLocked renders q.
func (m *Manager) protoQueueLocked(q *queueState) *downexv1.Queue {
	ids := make([]int64, len(q.order))
	for i, id := range q.order {
		ids[i] = int64(id)
	}
	return protoQueue(q.q, ids)
}

// ListQueues returns every queue ordered by id, with its downloads in queue order.
func (m *Manager) ListQueues() []*downexv1.Queue {
	m.mu.Lock()
	defer m.mu.Unlock()
	qs := m.sortedQueuesLocked()
	out := make([]*downexv1.Queue, len(qs))
	for i, q := range qs {
		out[i] = m.protoQueueLocked(q)
	}
	return out
}
