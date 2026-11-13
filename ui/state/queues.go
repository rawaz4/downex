package state

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/queues"
	"downex/ui/host"
)

// Countdown lengths of the post-queue actions (stores/queues.svelte.ts).
const (
	// PostActionGrace is how long a countdown stays shown after its deadline ("… now"),
	// while the action runs.
	PostActionGrace = 10 * time.Second
	// PostActionDelay is the countdown of an action whose event names no deadline.
	PostActionDelay = 60 * time.Second
)

// Queue is a downex.v1.Queue as the stores hold it.
type Queue = downexv1.Queue

// Pending is a post-queue action counting down (downex.v1.PostActionPending).
type Pending = downexv1.PostActionPending

// QueuesOptions are what a QueuesStore works with.
type QueuesOptions struct {
	Daemon host.Daemon
	Events Events
	Clock  Clock
	Call   CallContext
	// Go runs the reads after a re-subscription in the background (nil: a goroutine).
	Go func(func())
}

// QueuesStore is the queues as the window knows them (stores/queues.svelte.ts, M6-SPEC
// §1): read with ListQueues, kept up to date by the queue events of Watch, and read again
// after every re-subscription's snapshot, since queues removed meanwhile send no event.
// Post-queue countdowns are kept by queue: a subscription's snapshot lists all of them, a
// cancellation ends one, and one whose deadline passed ends by itself shortly after
// (downexd sends nothing when it performs the action).
type QueuesStore struct {
	Changes Signal

	daemon host.Daemon
	events Events
	clock  Clock
	call   CallContext
	goFn   func(func())

	mu         sync.Mutex
	items      map[int64]*Queue
	loading    chan struct{}
	loadOK     bool
	again      bool
	touched    map[int64]*Queue // nil value: removed; nil map: no read in flight
	ends       map[int64]time.Time
	expiry     Timer
	unsub      func()
	started    bool
	disposed   bool
	subscribed bool

	loaded    bool
	supported bool
	loadError string
	pending   map[int64]*Pending

	listCache []*Queue
	listValid bool
}

// NewQueues makes the store; Start subscribes, Load reads.
func NewQueues(o QueuesOptions) *QueuesStore {
	q := &QueuesStore{daemon: o.Daemon, events: o.Events, clock: clockOr(o.Clock), call: callOr(o.Call), goFn: o.Go,
		items: map[int64]*Queue{}, ends: map[int64]time.Time{}, pending: map[int64]*Pending{}, supported: true}
	if q.goFn == nil {
		q.goFn = func(f func()) { go f() }
	}
	return q
}

// List is the queues, the main queue first.
func (q *QueuesStore) List() []*Queue {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.listValid {
		all := make([]*Queue, 0, len(q.items))
		for _, x := range q.items {
			all = append(all, x)
		}
		slices.SortFunc(all, func(a, b *Queue) int { return int(a.GetId() - b.GetId()) })
		q.listCache, q.listValid = queues.SortQueues(all), true
	}
	return q.listCache
}

// IDs is the set of the queues' ids.
func (q *QueuesStore) IDs() map[int64]bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make(map[int64]bool, len(q.items))
	for id := range q.items {
		out[id] = true
	}
	return out
}

// Get is one queue.
func (q *QueuesStore) Get(id int64) (*Queue, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	x, ok := q.items[id]
	return x, ok
}

// NameOf is a queue's name; "Queue <id>" for one this window does not know.
func (q *QueuesStore) NameOf(id int64) string {
	if x, ok := q.Get(id); ok {
		return x.GetName()
	}
	return fmt.Sprintf("Queue %d", id)
}

// Loaded reports that the queues were read since downexd last became reachable.
func (q *QueuesStore) Loaded() bool { q.mu.Lock(); defer q.mu.Unlock(); return q.loaded }

// Supported reports that downexd has QueueService (an older one answers unimplemented: no
// queues are shown).
func (q *QueuesStore) Supported() bool { q.mu.Lock(); defer q.mu.Unlock(); return q.supported }

// LoadError is why the last read failed ("" when it did not).
func (q *QueuesStore) LoadError() string { q.mu.Lock(); defer q.mu.Unlock(); return q.loadError }

// Pending is the countdowns by queue id (cancelled ones are dropped).
func (q *QueuesStore) Pending() map[int64]*Pending {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.pending
}

// NextAction is the countdown that ends first (nil: none).
func (q *QueuesStore) NextAction() *Pending {
	q.mu.Lock()
	defer q.mu.Unlock()
	var first *Pending
	for _, p := range q.pending {
		if first == nil || tsNanos(p.GetDeadline()) < tsNanos(first.GetDeadline()) ||
			tsNanos(p.GetDeadline()) == tsNanos(first.GetDeadline()) && p.GetQueueId() < first.GetQueueId() {
			first = p
		}
	}
	return first
}

// Start subscribes to the queue events.
func (q *QueuesStore) Start() {
	q.mu.Lock()
	if q.started || q.disposed {
		q.mu.Unlock()
		return
	}
	q.started = true
	q.mu.Unlock()
	if q.events != nil {
		unsub := q.events.Subscribe(q.Apply)
		q.mu.Lock()
		q.unsub = unsub
		q.mu.Unlock()
	}
}

// Dispose unsubscribes and stops the countdowns' timer.
func (q *QueuesStore) Dispose() {
	q.mu.Lock()
	q.disposed = true
	unsub := q.unsub
	q.unsub = nil
	if q.expiry != nil {
		q.expiry.Stop()
		q.expiry = nil
	}
	q.mu.Unlock()
	if unsub != nil {
		unsub()
	}
}

// Load reads every queue (concurrent calls share one read; one that arrives meanwhile
// asks for another read afterwards) and reports whether that worked.
func (q *QueuesStore) Load() bool {
	q.mu.Lock()
	if q.loading != nil {
		q.again = true // a snapshot arrived meanwhile: read once more afterwards
		ch := q.loading
		q.mu.Unlock()
		<-ch
		q.mu.Lock()
		defer q.mu.Unlock()
		return q.loadOK
	}
	ch := make(chan struct{})
	q.loading = ch
	q.touched = map[int64]*Queue{}
	q.mu.Unlock()

	resp, err := callWith(q.call, q.daemon.ListQueues)

	q.mu.Lock()
	ok := true
	switch {
	case err == nil:
		next := map[int64]*Queue{}
		for _, x := range resp.GetQueues() {
			next[x.GetId()] = x
		}
		for id, x := range q.touched {
			if x != nil {
				next[id] = x
			} else {
				delete(next, id)
			}
		}
		q.items = next
		q.listValid = false
		q.dropPendingOfMissingLocked()
		q.supported, q.loaded, q.loadError = true, true, ""
	case codeOf(err) == connect.CodeUnimplemented:
		// downexd without queues: nothing to show, nothing to read again until it restarts
		q.items = map[int64]*Queue{}
		q.listValid = false
		q.supported, q.loaded, q.loadError = false, true, ""
	default:
		q.loaded = false
		q.loadError = Message(err)
		ok = false
	}
	q.touched = nil
	q.loadOK = ok
	q.loading = nil
	again := q.again
	q.again = false
	q.mu.Unlock()
	close(ch)
	q.Changes.Notify()
	if again {
		q.goFn(func() { q.Load() })
	}
	return ok
}

// Invalidate: downexd did not answer a status check, so the queues are read again when it
// does. The countdowns stay (the window hides them meanwhile): the event stream may still
// be up, and nothing would send them again; a new subscription's snapshot replaces them.
func (q *QueuesStore) Invalidate() {
	q.mu.Lock()
	q.loaded = false
	q.mu.Unlock()
	q.Changes.Notify()
}

// Apply takes one batch of events.
func (q *QueuesStore) Apply(batch Batch) {
	q.mu.Lock()
	var items map[int64]*Queue
	var pending map[int64]*Pending
	snapshot := false
	copyItems := func() {
		if items == nil {
			items = make(map[int64]*Queue, len(q.items))
			for k, v := range q.items {
				items[k] = v
			}
		}
	}
	copyPending := func() {
		if pending == nil {
			pending = make(map[int64]*Pending, len(q.pending))
			for k, v := range q.pending {
				pending[k] = v
			}
		}
	}
	for _, ev := range batch {
		switch k := ev.GetKind().(type) {
		case *downexv1.Event_Snapshot:
			// A subscription's first batch: its post_action events are every countdown.
			if !snapshot {
				pending = map[int64]*Pending{}
			}
			snapshot = true
		case *downexv1.Event_QueueUpdated:
			copyItems()
			items[k.QueueUpdated.GetId()] = k.QueueUpdated
			if q.touched != nil {
				q.touched[k.QueueUpdated.GetId()] = k.QueueUpdated
			}
		case *downexv1.Event_QueueRemoved:
			copyItems()
			delete(items, k.QueueRemoved)
			if q.touched != nil {
				q.touched[k.QueueRemoved] = nil
			}
			copyPending()
			delete(pending, k.QueueRemoved)
		case *downexv1.Event_PostAction:
			copyPending()
			p := k.PostAction
			if p.GetCancelled() || p.GetAction() == downexv1.PostAction_POST_ACTION_UNSPECIFIED {
				delete(pending, p.GetQueueId())
			} else {
				pending[p.GetQueueId()] = p
			}
		}
	}
	if items != nil {
		q.items = items
		q.listValid = false
	}
	if pending != nil {
		q.setPendingLocked(pending)
	}
	reload := false
	if snapshot {
		// A new subscription (not the first): queues removed while it was down sent no event.
		reload = q.subscribed && q.supported && q.loaded
		q.subscribed = true
	}
	q.mu.Unlock()
	q.Changes.Notify()
	if reload {
		q.goFn(func() { q.Load() })
	}
}

func (q *QueuesStore) putLocked(x *Queue) *Queue {
	next := make(map[int64]*Queue, len(q.items)+1)
	for k, v := range q.items {
		next[k] = v
	}
	next[x.GetId()] = x
	q.items = next
	q.listValid = false
	return x
}

func (q *QueuesStore) put(x *Queue) *Queue {
	q.mu.Lock()
	q.putLocked(x)
	q.mu.Unlock()
	q.Changes.Notify()
	return x
}

func (q *QueuesStore) dropPendingOfMissingLocked() {
	missing := false
	for id := range q.pending {
		if _, ok := q.items[id]; !ok {
			missing = true
		}
	}
	if !missing {
		return
	}
	next := map[int64]*Pending{}
	for id, p := range q.pending {
		if _, ok := q.items[id]; ok {
			next[id] = p
		}
	}
	q.setPendingLocked(next)
}

func (q *QueuesStore) dropPendingLocked(queueID int64) {
	if _, ok := q.pending[queueID]; !ok {
		return
	}
	next := map[int64]*Pending{}
	for id, p := range q.pending {
		if id != queueID {
			next[id] = p
		}
	}
	q.setPendingLocked(next)
}

// setPendingLocked sets the countdowns: each ends by itself a little after its deadline
// (a new one that names none, PostActionDelay after it arrived), and those already over
// are left out.
func (q *QueuesStore) setPendingLocked(next map[int64]*Pending) {
	now := q.clock.Now()
	ends := map[int64]time.Time{}
	for id, p := range next {
		end, known := q.ends[id]
		if q.pending[id] != p {
			known = false
		}
		if !known {
			if d := p.GetDeadline(); tsNanos(d) > 0 {
				end = d.AsTime()
			} else {
				end = now.Add(PostActionDelay)
			}
			end = end.Add(PostActionGrace)
		}
		if !end.After(now) {
			delete(next, id)
		} else {
			ends[id] = end
		}
	}
	q.ends = ends
	q.pending = next
	if q.expiry != nil {
		q.expiry.Stop()
		q.expiry = nil
	}
	if len(ends) == 0 || q.disposed {
		return
	}
	first := time.Time{}
	for _, e := range ends {
		if first.IsZero() || e.Before(first) {
			first = e
		}
	}
	wait := min(first.Sub(now), time.Duration(math.MaxInt32)*time.Millisecond)
	q.expiry = q.clock.AfterFunc(wait, func() {
		q.mu.Lock()
		cur := make(map[int64]*Pending, len(q.pending))
		for k, v := range q.pending {
			cur[k] = v
		}
		q.expiry = nil
		q.setPendingLocked(cur)
		q.mu.Unlock()
		q.Changes.Notify()
	})
}

// ---- actions (the error is the daemon's) -----------------------------------------------

// Create makes a queue from spec (its id, running, is_main and download_ids are ignored).
func (q *QueuesStore) Create(ctx context.Context, spec *Queue) (*Queue, error) {
	resp, err := q.daemon.CreateQueue(ctx, &downexv1.CreateQueueRequest{Queue: spec})
	if err != nil {
		return nil, err
	}
	return q.put(resp.GetQueue()), nil
}

// Update replaces a queue's settings with spec's.
func (q *QueuesStore) Update(ctx context.Context, id int64, spec *Queue) (*Queue, error) {
	s := cloneQueue(spec)
	s.Id = id
	resp, err := q.daemon.UpdateQueue(ctx, &downexv1.UpdateQueueRequest{Queue: s})
	if err != nil {
		return nil, err
	}
	return q.put(resp.GetQueue()), nil
}

// SetPostAction changes only what a queue does when it is done (the status bar's when-done
// menu): the rest of its settings are sent as this window knows them. A queue this window
// does not know is refused with not_found, as UpdateQueue would.
func (q *QueuesStore) SetPostAction(ctx context.Context, id int64, action downexv1.PostAction) (*Queue, error) {
	x, ok := q.Get(id)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("queue %d not found", id))
	}
	spec := queues.Spec(x)
	spec.PostAction = action
	return q.Update(ctx, id, spec)
}

// Remove deletes a queue; its downloads join the main queue (the daemon's events say where).
func (q *QueuesStore) Remove(ctx context.Context, id int64) error {
	if _, err := q.daemon.DeleteQueue(ctx, &downexv1.DeleteQueueRequest{Id: id}); err != nil {
		return err
	}
	q.mu.Lock()
	next := map[int64]*Queue{}
	for k, v := range q.items {
		next[k] = v
	}
	gone := next[id]
	delete(next, id)
	var main *Queue
	for _, x := range next {
		if x.GetIsMain() {
			main = x
		}
	}
	if gone != nil && main != nil {
		m := cloneQueue(main)
		m.DownloadIds = append(slices.Clone(main.GetDownloadIds()), gone.GetDownloadIds()...)
		next[m.GetId()] = m
	}
	q.items = next
	q.listValid = false
	q.dropPendingOfMissingLocked()
	q.mu.Unlock()
	q.Changes.Notify()
	return nil
}

// StartQueue starts a queue.
func (q *QueuesStore) StartQueue(ctx context.Context, id int64) (*Queue, error) {
	resp, err := q.daemon.StartQueue(ctx, &downexv1.StartQueueRequest{Id: id})
	if err != nil {
		return nil, err
	}
	return q.put(resp.GetQueue()), nil
}

// StopQueue stops a queue.
func (q *QueuesStore) StopQueue(ctx context.Context, id int64) (*Queue, error) {
	resp, err := q.daemon.StopQueue(ctx, &downexv1.StopQueueRequest{Id: id})
	if err != nil {
		return nil, err
	}
	return q.put(resp.GetQueue()), nil
}

// Move moves downloads into a queue at position (-1: the end); they leave their old queues.
func (q *QueuesStore) Move(ctx context.Context, ids []int64, queueID int64, position int32) (*Queue, error) {
	resp, err := q.daemon.MoveDownloads(ctx, &downexv1.MoveDownloadsRequest{Ids: ids, QueueId: queueID, Position: position})
	if err != nil {
		return nil, err
	}
	target := resp.GetQueue()
	moving := map[int64]bool{}
	for _, id := range ids {
		moving[id] = true
	}
	q.mu.Lock()
	next := map[int64]*Queue{}
	for id, x := range q.items {
		if id == target.GetId() {
			next[id] = target
			continue
		}
		c := cloneQueue(x)
		c.DownloadIds = slices.DeleteFunc(slices.Clone(x.GetDownloadIds()), func(d int64) bool { return moving[d] })
		next[id] = c
	}
	next[target.GetId()] = target
	q.items = next
	q.listValid = false
	q.mu.Unlock()
	q.Changes.Notify()
	return target, nil
}

// CancelPostAction cancels a countdown; it disappears at once (the daemon confirms with an
// event). It answers false when downexd had none for the queue any more (the action ran,
// or the countdown or the queue ended elsewhere): it disappears too. Other failures are
// returned and keep it, since it may still run.
func (q *QueuesStore) CancelPostAction(ctx context.Context, queueID int64) (bool, error) {
	cancelled := true
	if _, err := q.daemon.CancelPostAction(ctx, &downexv1.CancelPostActionRequest{QueueId: queueID}); err != nil {
		switch codeOf(err) {
		case connect.CodeFailedPrecondition, connect.CodeNotFound:
			cancelled = false
		default:
			return false, err
		}
	}
	q.mu.Lock()
	q.dropPendingLocked(queueID)
	q.mu.Unlock()
	q.Changes.Notify()
	return cancelled, nil
}

func cloneQueue(x *Queue) *Queue {
	if x == nil {
		return &Queue{}
	}
	return proto.CloneOf(x)
}
