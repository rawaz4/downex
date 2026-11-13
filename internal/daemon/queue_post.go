package daemon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	"downex/internal/engine"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
)

// PostActionDelay is the countdown between a queue finishing and its post-queue action
// (docs/M6-SPEC.md §1); CancelPostAction stops it meanwhile.
const PostActionDelay = 60 * time.Second

// postActionLateness bounds how far past its deadline, on the wall clock, a countdown may
// end and still perform its action. A countdown found later than that had its deadline
// pass while the computer slept (a timer that counts the time asleep fires on waking, one
// that does not continues after it) or while its clock was put forward: it is cancelled
// instead, so that a computer that has just been woken does not go back to sleep or shut
// down at once. The bound covers timer latency and small clock corrections.
const postActionLateness = 15 * time.Second

// Power performs the power actions after a queue finished. downexd passes the
// platform's (platform.Sleep, Hibernate, Shutdown, CanHibernate) for the user's own
// daemon only; tests pass a fake, so that nothing ever powers off.
type Power interface {
	Sleep() error
	Hibernate() error
	Shutdown() error
	// CanHibernate reports whether Hibernate can work on this system (validation
	// refuses the action otherwise).
	CanHibernate() bool
}

// WakeTimer wakes the computer from sleep for a scheduled start (docs/M6-SPEC.md §1
// wake_computer; downexd passes platform.WakeTimer on Windows). The scheduler arms it
// for the first start of a queue whose schedule asks for it, shortly before that start,
// and cancels it when there is none.
type WakeTimer interface {
	WakeAt(t time.Time) error
	Cancel() error
}

// wakeLead is how long before a scheduled start the computer is woken, so that the
// network is back when the queue starts.
const wakeLead = time.Minute

// postPending is a post-queue action counting down. Guarded by Manager.mu.
type postPending struct {
	queue    model.QueueID
	action   model.PostAction
	deadline time.Time     // wall clock (no monotonic reading)
	cancel   chan struct{} // closed when the countdown is cancelled
}

// overdue reports whether now lies more than postActionLateness past p's deadline on the
// wall clock (see postActionLateness).
func (p *postPending) overdue(now time.Time) bool {
	return now.Round(0).Sub(p.deadline) > postActionLateness
}

func (p *postPending) proto(cancelled bool) *downexv1.PostActionPending {
	return &downexv1.PostActionPending{
		QueueId: int64(p.queue), Action: protoPostAction(p.action), Deadline: protoTime(p.deadline), Cancelled: cancelled,
	}
}

// startPostLocked starts the countdown of q's post-queue action: Watch subscribers get
// a PostActionPending event and notice subscribers a NoticePostAction (the tray shows
// it); the action runs at the deadline unless it is cancelled or q has work again.
func (m *Manager) startPostLocked(q *queueState) {
	if m.closed {
		return
	}
	p := &postPending{
		queue: q.id(), action: q.q.PostAction, deadline: m.clock.Now().Round(0).Add(PostActionDelay), cancel: make(chan struct{}),
	}
	q.post = p
	t := m.clock.NewTimer(PostActionDelay)
	m.wg.Add(1)
	go m.runPost(p, t)
	m.kickAwake()
	ev := p.proto(false)
	m.hub.publishPost(ev)
	m.notify(Notice{Kind: NoticePostAction, PostAction: ev})
	m.log.Info("post-queue action pending", "queue", int64(q.id()), "action", p.action.String(), "deadline", p.deadline)
}

// cancelPostLocked cancels q's pending post-queue action, if any, and reports it as
// cancelled.
func (m *Manager) cancelPostLocked(q *queueState, why string) bool {
	p := q.post
	if p == nil {
		return false
	}
	q.post = nil
	close(p.cancel)
	m.reportPostEndLocked(p, "post-queue action cancelled", why)
	return true
}

// reportPostEndLocked reports the countdown p, which ended without performing its action,
// as cancelled (Watch post_action, NoticePostAction) and logs msg with why.
func (m *Manager) reportPostEndLocked(p *postPending, msg, why string) {
	m.kickAwake()
	ev := p.proto(true)
	m.hub.publishPost(ev)
	m.notify(Notice{Kind: NoticePostAction, PostAction: ev})
	m.log.Info(msg, "queue", int64(p.queue), "action", p.action.String(), "reason", why)
}

// cancelOverduePostsLocked cancels every countdown that is overdue at now (see
// postActionLateness): Reevaluate after the system resumed or its clock changed, before
// a timer that did not count the time asleep ends it.
func (m *Manager) cancelOverduePostsLocked(now time.Time) {
	for _, q := range m.sortedQueuesLocked() {
		if q.post != nil && q.post.overdue(now) {
			m.cancelPostLocked(q, "its deadline passed while the computer slept or its clock changed")
		}
	}
}

// runPost waits for p's deadline and performs its action, unless it was cancelled, the
// manager shut down, the queue has work again or the deadline passed long ago (see
// postActionLateness); those end it as cancelled. A power action ends every other
// countdown, as cancelled: they would act on a computer that is about to sleep, hibernate
// or shut down, or, when it wakes, has just been woken, so one resume never brings a
// second power action.
func (m *Manager) runPost(p *postPending, t engine.Timer) {
	defer m.wg.Done()
	select {
	case <-t.C():
	case <-p.cancel:
		t.Stop()
		return
	case <-m.schedDone:
		t.Stop()
		return
	}
	m.mu.Lock()
	q := m.queues[p.queue]
	if q == nil || q.post != p || m.closed {
		m.mu.Unlock()
		return
	}
	q.post = nil
	switch {
	case p.overdue(m.clock.Now()):
		m.reportPostEndLocked(p, "post-queue action cancelled", "its deadline passed while the computer slept or its clock changed")
		m.mu.Unlock()
		return
	case m.queueBusyLocked(q):
		m.reportPostEndLocked(p, "post-queue action aborted", "the queue has downloads again")
		m.mu.Unlock()
		return
	}
	if p.action != model.PostActionQuit { // quitting ends the other countdowns anyway
		for _, other := range m.sortedQueuesLocked() {
			if other.post != nil {
				m.cancelPostLocked(other, fmt.Sprintf("queue %d performs its post-queue action %s", p.queue, p.action))
			}
		}
	}
	m.kickAwake()
	m.mu.Unlock()
	m.log.Info("performing post-queue action", "queue", int64(p.queue), "action", p.action.String())
	if err := m.performPost(p.action); err != nil {
		m.log.Warn("post-queue action failed", "queue", int64(p.queue), "action", p.action.String(), "err", err)
		ev := p.proto(true)
		m.hub.publishPost(ev)
		m.notify(Notice{Kind: NoticePostAction, PostAction: ev})
	}
}

// performPost runs a post-queue action. Quit asks the process to shut down gracefully
// (Config.Quit, which must not block); the power actions go to Config.Power.
func (m *Manager) performPost(a model.PostAction) error {
	switch a {
	case model.PostActionQuit:
		if m.cfg.Quit == nil {
			return errors.New("this daemon cannot quit on its own")
		}
		m.cfg.Quit()
		return nil
	case model.PostActionSleep, model.PostActionHibernate, model.PostActionShutdown:
		if m.cfg.Power == nil {
			return errors.New("power actions are not available to this daemon")
		}
		switch a {
		case model.PostActionSleep:
			return m.cfg.Power.Sleep()
		case model.PostActionHibernate:
			return m.cfg.Power.Hibernate()
		}
		return m.cfg.Power.Shutdown()
	}
	return fmt.Errorf("unknown post-queue action %d", a)
}

// checkPostAction refuses a post-queue action this daemon cannot perform: quitting
// without Config.Quit, power actions without Config.Power, hibernation where the system
// has none (macOS).
func (m *Manager) checkPostAction(a model.PostAction) error {
	switch a {
	case model.PostActionNone:
		return nil
	case model.PostActionQuit:
		if m.cfg.Quit == nil {
			return errPrecondition("post_action %s is not available: this daemon cannot quit on its own", a)
		}
		return nil
	case model.PostActionSleep, model.PostActionShutdown:
		if m.cfg.Power == nil {
			return errPrecondition("post_action %s is not available to this daemon", a)
		}
		return nil
	case model.PostActionHibernate:
		if m.cfg.Power == nil || !m.cfg.Power.CanHibernate() {
			return errInvalid("post_action hibernate is not supported on this system")
		}
		return nil
	}
	return errInvalid("unknown post_action %d", a)
}

// CancelPostAction cancels the countdown of queue queueID's post-queue action (0: of
// every queue). Nothing pending is FailedPrecondition; an unknown queue NotFound.
func (m *Manager) CancelPostAction(_ context.Context, queueID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if queueID < 0 {
		return errInvalid("queue_id must not be negative")
	}
	if queueID == 0 {
		n := 0
		for _, q := range m.queues {
			if m.cancelPostLocked(q, "cancelled by the user") {
				n++
			}
		}
		if n == 0 {
			return errPrecondition("no post-queue action is pending")
		}
		return nil
	}
	q := m.queues[model.QueueID(queueID)]
	if q == nil {
		return errQueueNotFound(model.QueueID(queueID))
	}
	if !m.cancelPostLocked(q, "cancelled by the user") {
		return errPrecondition("queue %d has no post-queue action pending", queueID)
	}
	return nil
}

// pendingPostsLocked returns the countdowns in progress, by queue id.
func (m *Manager) pendingPostsLocked() []*downexv1.PostActionPending {
	var out []*downexv1.PostActionPending
	for _, q := range m.sortedQueuesLocked() {
		if q.post != nil {
			out = append(out, q.post.proto(false))
		}
	}
	return out
}

func errQueueNotFound(id model.QueueID) error {
	return connect.NewError(connect.CodeNotFound, fmt.Errorf("queue %d not found", id))
}
