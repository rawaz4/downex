package daemon

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	"downex/internal/engine"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/store"
	"downex/internal/testserver"
)

// ---- helpers -----------------------------------------------------------------------

// fakePower records power actions; nothing powers off in tests.
type fakePower struct {
	sleeps, hibernates, shutdowns atomic.Int32
	canHibernate                  bool
}

func (p *fakePower) Sleep() error       { p.sleeps.Add(1); return nil }
func (p *fakePower) Hibernate() error   { p.hibernates.Add(1); return nil }
func (p *fakePower) Shutdown() error    { p.shutdowns.Add(1); return nil }
func (p *fakePower) CanHibernate() bool { return p.canHibernate }

// fakeWake records the times a wake timer was armed for (zero: cancelled).
type fakeWake struct {
	mu    sync.Mutex
	times []time.Time
}

func (w *fakeWake) WakeAt(t time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.times = append(w.times, t)
	return nil
}

func (w *fakeWake) Cancel() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.times = append(w.times, time.Time{})
	return nil
}

func (w *fakeWake) last() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.times) == 0 {
		return time.Time{}
	}
	return w.times[len(w.times)-1]
}

func (h *harness) createQueue(q *downexv1.Queue) *downexv1.Queue {
	h.t.Helper()
	res, err := h.dq.CreateQueue(ctxT(h.t), connect.NewRequest(&downexv1.CreateQueueRequest{Queue: q}))
	if err != nil {
		h.t.Fatalf("create queue %q: %v", q.GetName(), err)
	}
	return res.Msg.GetQueue()
}

func (h *harness) startQueue(id int64) *downexv1.Queue {
	h.t.Helper()
	res, err := h.dq.StartQueue(ctxT(h.t), connect.NewRequest(&downexv1.StartQueueRequest{Id: id}))
	if err != nil {
		h.t.Fatalf("start queue %d: %v", id, err)
	}
	return res.Msg.GetQueue()
}

func (h *harness) stopQueue(id int64) *downexv1.Queue {
	h.t.Helper()
	res, err := h.dq.StopQueue(ctxT(h.t), connect.NewRequest(&downexv1.StopQueueRequest{Id: id}))
	if err != nil {
		h.t.Fatalf("stop queue %d: %v", id, err)
	}
	return res.Msg.GetQueue()
}

func (h *harness) move(queue int64, position int32, ids ...int64) *downexv1.Queue {
	h.t.Helper()
	res, err := h.dq.MoveDownloads(ctxT(h.t), connect.NewRequest(&downexv1.MoveDownloadsRequest{Ids: ids, QueueId: queue, Position: position}))
	if err != nil {
		h.t.Fatalf("move %v to queue %d: %v", ids, queue, err)
	}
	return res.Msg.GetQueue()
}

func (h *harness) queue(id int64) *downexv1.Queue {
	h.t.Helper()
	res, err := h.dq.ListQueues(ctxT(h.t), connect.NewRequest(&downexv1.ListQueuesRequest{}))
	if err != nil {
		h.t.Fatal(err)
	}
	for _, q := range res.Msg.GetQueues() {
		if q.GetId() == id {
			return q
		}
	}
	h.t.Fatalf("queue %d is not listed", id)
	return nil
}

// storedQueue reads queue id straight from the store.
func storedQueue(t *testing.T, st store.Store, id int64) model.Queue {
	t.Helper()
	list, err := st.(store.QueueStore).ListQueues(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range list {
		if int64(q.ID) == id {
			return q
		}
	}
	t.Fatalf("queue %d is not stored", id)
	return model.Queue{}
}

func ids64(ids []model.DownloadID) []int64 {
	out := make([]int64, len(ids))
	for i, id := range ids {
		out[i] = int64(id)
	}
	return out
}

// runningSet returns the ids of the running downloads, ascending.
func (h *harness) runningSet() []int64 {
	h.t.Helper()
	var out []int64
	for _, d := range h.list() {
		if isRunning(d) {
			out = append(out, d.GetId())
		}
	}
	return out
}

func (h *harness) waitRunning(want ...int64) {
	h.t.Helper()
	slices.Sort(want)
	var got []int64
	deadline := time.Now().Add(waitTimeout)
	for {
		if got = h.runningSet(); slices.Equal(got, want) {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("running downloads = %v, want %v", got, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

const mainQ = int64(model.MainQueueID)

// ---- tests -------------------------------------------------------------------------

// Downloads of a store from before queues all belong to the main queue, in id order,
// and the main queue runs.
func TestMainQueueHoldsExistingDownloads(t *testing.T) {
	st := store.NewMemory()
	for i := range 3 {
		d := &model.Download{Request: model.Request{URL: fmt.Sprintf("http://127.0.0.1:9/%d", i)}, Dir: t.TempDir(),
			Size: -1, State: model.StatePaused}
		if err := st.CreateDownload(context.Background(), d); err != nil {
			t.Fatal(err)
		}
	}
	h := newHarness(t, harnessOptions{st: st})
	q := h.queue(mainQ)
	if !q.GetIsMain() || !q.GetRunning() || q.GetName() != store.MainQueueName || !slices.Equal(q.GetDownloadIds(), []int64{1, 2, 3}) {
		t.Fatalf("main queue = %v", q)
	}
	for _, d := range h.list() {
		if d.GetQueueId() != mainQ {
			t.Fatalf("download %d is in queue %d", d.GetId(), d.GetQueueId())
		}
	}
	if _, err := h.dq.DeleteQueue(ctxT(t), connect.NewRequest(&downexv1.DeleteQueueRequest{Id: mainQ})); connectCode(err) != connect.CodeFailedPrecondition {
		t.Fatalf("delete of the main queue: %v", err)
	}
}

// Admission: running queues take turns (round robin) within the global limit, each
// within its own max_concurrent, in queue order; a stopped queue starts nothing.
func TestQueueAdmissionAcrossQueues(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 8 << 20, Seed: 61})
	h := newHarness(t, harnessOptions{settings: func(s *downexv1.Settings) { s.MaxConcurrentDownloads = 1 }})
	a := h.createQueue(&downexv1.Queue{Name: "A", MaxConcurrent: 1})
	b := h.createQueue(&downexv1.Queue{Name: "B"})
	if a.GetRunning() || b.GetRunning() || a.GetIsMain() {
		t.Fatalf("new queues = %v / %v", a, b)
	}
	h.startQueue(a.GetId())
	add := func(queue int64, name string) int64 {
		return h.add(&downexv1.AddRequest{Url: ts.URL + "/" + name, SpeedLimit: 32 << 10, QueueId: queue}).GetId()
	}
	m1, m2, m3, m4 := add(0, "m1"), add(mainQ, "m2"), add(0, "m3"), add(0, "m4")
	a1, a2, a3 := add(a.GetId(), "a1"), add(a.GetId(), "a2"), add(a.GetId(), "a3")
	b1 := add(b.GetId(), "b1")
	h.waitRunning(m1)
	if d := h.get(a1); d.GetQueueId() != a.GetId() || d.GetState() != stQueued {
		t.Fatalf("a1 = %v", d)
	}

	// Two more slots: queue A (after the main queue, which started last) gets one, the
	// main queue the other.
	s := protoSettings(h.m.Settings())
	s.MaxConcurrentDownloads = 3
	if _, err := h.dm.UpdateSettings(ctxT(t), connect.NewRequest(&downexv1.UpdateSettingsRequest{Settings: s})); err != nil {
		t.Fatal(err)
	}
	h.waitRunning(m1, m2, a1)
	positions := map[int64]int32{m3: 1, m4: 2, a2: 1, a3: 2, b1: 1}
	for _, d := range h.list() {
		if want, ok := positions[d.GetId()]; ok && (d.GetState() != stQueued || d.GetQueuePosition() != want) {
			t.Errorf("download %d: %v at position %d, want queued at %d", d.GetId(), d.GetState(), d.GetQueuePosition(), want)
		}
	}
	// A slot freed in A goes to A's next download (A's limit is 1, the main queue had
	// the last turn).
	h.pause(a1)
	h.waitRunning(m1, m2, a2)
	// A started queue waits for a free slot, then has its turn.
	h.startQueue(b.GetId())
	time.Sleep(100 * time.Millisecond)
	h.waitRunning(m1, m2, a2)
	h.pause(m1)
	h.waitRunning(m2, a2, b1)
	if st := h.status(); st.GetActive() != 3 || st.GetQueued() != 3 { // m3, m4, a3
		t.Fatalf("status = %v", st)
	}
}

// StopQueue stops the queue's running downloads (progress committed); they wait in the
// queue and StartQueue continues them. Moving a running download into a stopped queue
// stops it too. Running state is persisted.
func TestStopStartAndMoveRunningDownload(t *testing.T) {
	const size, seed = 2 << 20, 62
	ts := serve(t, testserver.Options{Size: size, Seed: seed, ETag: `"q"`})
	h := newHarness(t, harnessOptions{})
	q := h.createQueue(&downexv1.Queue{Name: "Later"})
	h.startQueue(q.GetId())
	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/stop", SpeedLimit: 256 << 10, QueueId: q.GetId()})
	h.waitReceived(d.GetId(), 64<<10)

	stopped := h.stopQueue(q.GetId())
	if stopped.GetRunning() {
		t.Fatalf("stopped queue = %v", stopped)
	}
	got := h.get(d.GetId())
	if got.GetState() != stQueued || got.GetQueuePosition() != 1 || got.GetReceived() == 0 {
		t.Fatalf("download of the stopped queue = %v at %d with %d bytes", got.GetState(), got.GetQueuePosition(), got.GetReceived())
	}
	waitUntil(t, "the stopped queue to be stored", func() bool { return !storedQueue(t, h.st, q.GetId()).Running })
	time.Sleep(200 * time.Millisecond)
	if got := h.get(d.GetId()); got.GetState() != stQueued {
		t.Fatalf("download of a stopped queue started: %v", got.GetState())
	}

	// A running download of the main queue moved into the stopped queue stops there.
	other := h.add(&downexv1.AddRequest{Url: ts.URL + "/other", SpeedLimit: 256 << 10})
	h.waitReceived(other.GetId(), 1)
	moved := h.move(q.GetId(), 0, other.GetId())
	if !slices.Equal(moved.GetDownloadIds(), []int64{other.GetId(), d.GetId()}) {
		t.Fatalf("queue after the move = %v", moved.GetDownloadIds())
	}
	o := h.waitState(other.GetId(), stQueued)
	if o.GetQueueId() != q.GetId() || o.GetQueuePosition() != 1 {
		t.Fatalf("moved download = %v", o)
	}

	h.startQueue(q.GetId())
	waitUntil(t, "the started queue to be stored", func() bool { return storedQueue(t, h.st, q.GetId()).Running })
	unlimited := int64(0)
	for _, id := range []int64{d.GetId(), other.GetId()} {
		if _, err := h.dl.SetDownloadOptions(ctxT(t), connect.NewRequest(&downexv1.SetDownloadOptionsRequest{Id: id, SpeedLimit: &unlimited})); err != nil {
			t.Fatal(err)
		}
	}
	checkFile(t, h.waitCompleted(d.GetId()), seed, size)
	checkFile(t, h.waitCompleted(other.GetId()), seed, size)
}

// MoveDownloads reorders a queue and moves downloads between queues; the order is
// stored and survives a restart.
func TestMoveDownloadsAndRestart(t *testing.T) {
	st := store.NewMemory()
	dir := t.TempDir()
	h := newHarness(t, harnessOptions{st: st, dir: dir})
	h.stopQueue(mainQ)
	var ids []int64
	for i := range 4 {
		ids = append(ids, h.add(&downexv1.AddRequest{Url: fmt.Sprintf("http://127.0.0.1:9/d%d", i)}).GetId())
	}
	d1, d2, d3, d4 := ids[0], ids[1], ids[2], ids[3]
	if q := h.move(0, 0, d4); !slices.Equal(q.GetDownloadIds(), []int64{d4, d1, d2, d3}) {
		t.Fatalf("main after moving d4 first = %v", q.GetDownloadIds())
	}
	if p := h.get(d4).GetQueuePosition(); p != 1 {
		t.Fatalf("d4 position = %d", p)
	}
	later := h.createQueue(&downexv1.Queue{Name: "Later"})
	if q := h.move(later.GetId(), -1, d2, d1, d2); !slices.Equal(q.GetDownloadIds(), []int64{d2, d1}) {
		t.Fatalf("later = %v", q.GetDownloadIds())
	}
	if q := h.move(later.GetId(), 1, d3); !slices.Equal(q.GetDownloadIds(), []int64{d2, d3, d1}) {
		t.Fatalf("later after inserting d3 = %v", q.GetDownloadIds())
	}
	if q := h.queue(mainQ); !slices.Equal(q.GetDownloadIds(), []int64{d4}) {
		t.Fatalf("main = %v", q.GetDownloadIds())
	}
	if d := h.get(d1); d.GetQueueId() != later.GetId() || d.GetQueuePosition() != 3 {
		t.Fatalf("d1 = queue %d position %d", d.GetQueueId(), d.GetQueuePosition())
	}
	if got := ids64(storedQueue(t, st, later.GetId()).DownloadIDs); !slices.Equal(got, []int64{d2, d3, d1}) {
		t.Fatalf("stored order = %v", got)
	}
	for _, tc := range []struct {
		req  *downexv1.MoveDownloadsRequest
		code connect.Code
	}{
		{&downexv1.MoveDownloadsRequest{QueueId: mainQ}, connect.CodeInvalidArgument},
		{&downexv1.MoveDownloadsRequest{Ids: []int64{d1}, QueueId: mainQ, Position: -2}, connect.CodeInvalidArgument},
		{&downexv1.MoveDownloadsRequest{Ids: []int64{d1, 999}, QueueId: mainQ}, connect.CodeNotFound},
		{&downexv1.MoveDownloadsRequest{Ids: []int64{d1}, QueueId: 999}, connect.CodeNotFound},
	} {
		if _, err := h.dq.MoveDownloads(ctxT(t), connect.NewRequest(tc.req)); connectCode(err) != tc.code {
			t.Errorf("MoveDownloads(%v) = %v, want %v", tc.req, err, tc.code)
		}
	}
	if q := h.queue(later.GetId()); !slices.Equal(q.GetDownloadIds(), []int64{d2, d3, d1}) {
		t.Fatalf("a refused move changed the order: %v", q.GetDownloadIds())
	}

	// A restart reads the same queues, orders and running states.
	if err := h.m.Shutdown(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	h2 := newHarness(t, harnessOptions{st: st, dir: dir})
	if q := h2.queue(later.GetId()); q.GetName() != "Later" || q.GetRunning() || !slices.Equal(q.GetDownloadIds(), []int64{d2, d3, d1}) {
		t.Fatalf("later after a restart = %v", q)
	}
	if q := h2.queue(mainQ); q.GetRunning() || !slices.Equal(q.GetDownloadIds(), []int64{d4}) {
		t.Fatalf("main after a restart = %v", q)
	}
	if d := h2.get(d3); d.GetQueueId() != later.GetId() || d.GetState() != stQueued || d.GetQueuePosition() != 2 {
		t.Fatalf("d3 after a restart = %v", d)
	}
}

// DeleteQueue moves the queue's downloads to the end of the main queue, which runs
// them; Watch reports the removal.
func TestDeleteQueueMovesDownloadsToMain(t *testing.T) {
	const size, seed = 256 << 10, 63
	ts := serve(t, testserver.Options{Size: size, Seed: seed})
	h := newHarness(t, harnessOptions{})
	paused := h.add(&downexv1.AddRequest{Url: ts.URL + "/p", StartPaused: true}).GetId()
	q := h.createQueue(&downexv1.Queue{Name: "Doomed"})
	q1 := h.add(&downexv1.AddRequest{Url: ts.URL + "/q1", QueueId: q.GetId()}).GetId()
	q2 := h.add(&downexv1.AddRequest{Url: ts.URL + "/q2", QueueId: q.GetId()}).GetId()

	ctx := ctxT(t)
	stream, err := h.dl.Watch(ctx, connect.NewRequest(&downexv1.WatchRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if !stream.Receive() {
		t.Fatal(stream.Err())
	}
	var queues []int64
	for _, ev := range stream.Msg().GetEvents() {
		if u := ev.GetQueueUpdated(); u != nil {
			queues = append(queues, u.GetId())
		}
	}
	if !slices.Equal(queues, []int64{mainQ, q.GetId()}) {
		t.Fatalf("snapshot queues = %v", queues)
	}

	if _, err := h.dq.DeleteQueue(ctx, connect.NewRequest(&downexv1.DeleteQueueRequest{Id: 999})); connectCode(err) != connect.CodeNotFound {
		t.Fatalf("delete of an unknown queue: %v", err)
	}
	if _, err := h.dq.DeleteQueue(ctx, connect.NewRequest(&downexv1.DeleteQueueRequest{Id: q.GetId()})); err != nil {
		t.Fatal(err)
	}
	if m := h.queue(mainQ); !slices.Equal(m.GetDownloadIds(), []int64{paused, q1, q2}) {
		t.Fatalf("main after the delete = %v", m.GetDownloadIds())
	}
	checkFile(t, h.waitCompleted(q1), seed, size)
	checkFile(t, h.waitCompleted(q2), seed, size)
	if d := h.get(q2); d.GetQueueId() != mainQ {
		t.Fatalf("q2 is in queue %d", d.GetQueueId())
	}
	removed := false
	for !removed && stream.Receive() {
		for _, ev := range stream.Msg().GetEvents() {
			if ev.GetQueueRemoved() == q.GetId() {
				removed = true
			}
		}
	}
	if !removed {
		t.Fatalf("no queue_removed event: %v", stream.Err())
	}
	if got := storedState(t, h.st, q1); got.QueueID != model.MainQueueID {
		t.Fatalf("stored queue of q1 = %d", got.QueueID)
	}
}

// Add and Confirm put downloads into a queue; "download later" into a queue waits there
// as queued until the queue runs.
func TestAddAndConfirmIntoQueue(t *testing.T) {
	const size, seed = 256 << 10, 64
	ts := serve(t, testserver.Options{Size: size, Seed: seed})
	h := newHarness(t, harnessOptions{})
	q := h.createQueue(&downexv1.Queue{Name: "Night"})

	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/night.bin", QueueId: q.GetId()})
	if d.GetQueueId() != q.GetId() || d.GetState() != stQueued || d.GetQueuePosition() != 1 {
		t.Fatalf("added = %v", d)
	}
	if _, err := h.dl.Add(ctxT(t), connect.NewRequest(&downexv1.AddRequest{Url: ts.URL + "/x", QueueId: 999})); connectCode(err) != connect.CodeInvalidArgument {
		t.Fatalf("add into an unknown queue: %v", err)
	}
	if got := storedState(t, h.st, d.GetId()); got.QueueID != model.QueueID(q.GetId()) {
		t.Fatalf("stored queue = %d", got.QueueID)
	}

	// A browser hand-off confirmed with "download later" into the queue.
	aw := h.add(&downexv1.AddRequest{Url: ts.URL + "/later.bin", Source: downexv1.AddSource_ADD_SOURCE_BROWSER})
	if aw.GetState() != stAwaiting || aw.GetQueueId() != mainQ {
		t.Fatalf("hand-off = %v", aw)
	}
	res, err := h.dl.Confirm(ctxT(t), connect.NewRequest(&downexv1.ConfirmRequest{Id: aw.GetId(), QueueId: q.GetId()}))
	if err != nil {
		t.Fatal(err)
	}
	if c := res.Msg.GetDownload(); c.GetState() != stQueued || c.GetQueueId() != q.GetId() || c.GetQueuePosition() != 2 {
		t.Fatalf("confirmed into the queue = %v", c)
	}
	// Without a queue, "download later" still pauses (in the main queue).
	aw2 := h.add(&downexv1.AddRequest{Url: ts.URL + "/paused.bin", Source: downexv1.AddSource_ADD_SOURCE_CLIPBOARD})
	if aw2.GetState() != stAwaiting || aw2.GetSource() != downexv1.AddSource_ADD_SOURCE_CLIPBOARD {
		t.Fatalf("clipboard hand-off = %v", aw2)
	}
	res, err = h.dl.Confirm(ctxT(t), connect.NewRequest(&downexv1.ConfirmRequest{Id: aw2.GetId()}))
	if err != nil {
		t.Fatal(err)
	}
	if c := res.Msg.GetDownload(); c.GetState() != stPaused || c.GetQueueId() != mainQ {
		t.Fatalf("confirmed for later = %v", c)
	}
	time.Sleep(200 * time.Millisecond)
	if got := h.get(d.GetId()); got.GetState() != stQueued {
		t.Fatalf("download of a stopped queue started: %v", got.GetState())
	}
	h.startQueue(q.GetId())
	checkFile(t, h.waitCompleted(d.GetId()), seed, size)
	checkFile(t, h.waitCompleted(aw.GetId()), seed, size)
}

func TestQueueValidation(t *testing.T) {
	power := &fakePower{}
	h := newHarness(t, harnessOptions{power: power})
	h.createQueue(&downexv1.Queue{Name: "Night"})
	long := string(slices.Repeat([]rune{'q'}, MaxQueueNameRunes+1))
	for _, tc := range []struct {
		q    *downexv1.Queue
		code connect.Code
	}{
		{&downexv1.Queue{Name: "  "}, connect.CodeInvalidArgument},
		{&downexv1.Queue{Name: long}, connect.CodeInvalidArgument},
		{&downexv1.Queue{Name: "a\nb"}, connect.CodeInvalidArgument},
		{&downexv1.Queue{Name: "NIGHT"}, connect.CodeAlreadyExists},
		{&downexv1.Queue{Name: "x", MaxConcurrent: MaxConcurrentDownloadsLimit + 1}, connect.CodeInvalidArgument},
		{&downexv1.Queue{Name: "x", SpeedLimit: -1}, connect.CodeInvalidArgument},
		{&downexv1.Queue{Name: "x", Schedule: &downexv1.Schedule{Enabled: true}}, connect.CodeInvalidArgument},
		{&downexv1.Queue{Name: "x", PostAction: downexv1.PostAction_POST_ACTION_HIBERNATE}, connect.CodeInvalidArgument},
		{&downexv1.Queue{Name: "x", PostAction: downexv1.PostAction_POST_ACTION_QUIT_DOWNEX}, connect.CodeFailedPrecondition},
		{&downexv1.Queue{Name: "x", PostAction: 42}, connect.CodeInvalidArgument},
	} {
		if _, err := h.dq.CreateQueue(ctxT(t), connect.NewRequest(&downexv1.CreateQueueRequest{Queue: tc.q})); connectCode(err) != tc.code {
			t.Errorf("CreateQueue(%v) = %v, want %v", tc.q, err, tc.code)
		}
	}
	// Sleep is available with a power backend; without one it is not.
	ok := h.createQueue(&downexv1.Queue{Name: "Sleepy", PostAction: downexv1.PostAction_POST_ACTION_SLEEP, Running: true,
		IsMain: true, DownloadIds: []int64{7}})
	if ok.GetRunning() || ok.GetIsMain() || len(ok.GetDownloadIds()) != 0 {
		t.Fatalf("read-only fields were taken: %v", ok)
	}
	h2 := newHarness(t, harnessOptions{})
	if _, err := h2.dq.CreateQueue(ctxT(t), connect.NewRequest(&downexv1.CreateQueueRequest{
		Queue: &downexv1.Queue{Name: "x", PostAction: downexv1.PostAction_POST_ACTION_SHUTDOWN}})); connectCode(err) != connect.CodeFailedPrecondition {
		t.Fatalf("shutdown without a power backend: %v", err)
	}
	// The main queue can be renamed and limited; an unknown queue is NotFound.
	res, err := h.dq.UpdateQueue(ctxT(t), connect.NewRequest(&downexv1.UpdateQueueRequest{
		Queue: &downexv1.Queue{Id: mainQ, Name: "Everything", MaxConcurrent: 2, SpeedLimit: 1 << 20}}))
	if err != nil {
		t.Fatal(err)
	}
	if m := res.Msg.GetQueue(); m.GetName() != "Everything" || !m.GetIsMain() || !m.GetRunning() || m.GetMaxConcurrent() != 2 {
		t.Fatalf("updated main = %v", m)
	}
	if _, err := h.dq.UpdateQueue(ctxT(t), connect.NewRequest(&downexv1.UpdateQueueRequest{
		Queue: &downexv1.Queue{Id: 999, Name: "y"}})); connectCode(err) != connect.CodeNotFound {
		t.Fatalf("update of an unknown queue: %v", err)
	}
	if got := storedQueue(t, h.st, mainQ); got.Name != "Everything" || got.SpeedLimit != 1<<20 {
		t.Fatalf("stored main = %+v", got)
	}
}

// A queue's speed limit is shared by its downloads (global → queue → download) and
// changes live.
func TestQueueSpeedLimitIsShared(t *testing.T) {
	const size, seed = 4 << 20, 65
	ts := serve(t, testserver.Options{Size: size, Seed: seed})
	h := newHarness(t, harnessOptions{})
	q := h.createQueue(&downexv1.Queue{Name: "Slow", SpeedLimit: 64 << 10})
	h.startQueue(q.GetId())
	a := h.add(&downexv1.AddRequest{Url: ts.URL + "/a", QueueId: q.GetId()}).GetId()
	b := h.add(&downexv1.AddRequest{Url: ts.URL + "/b", QueueId: q.GetId()}).GetId()
	free := h.add(&downexv1.AddRequest{Url: ts.URL + "/free"}).GetId()
	checkFile(t, h.waitCompleted(free), seed, size) // unlimited in the main queue
	time.Sleep(time.Second)
	total := h.get(a).GetReceived() + h.get(b).GetReceived()
	if total > 1<<20 {
		t.Fatalf("the downloads of a queue limited to 64 KiB/s received %d bytes", total)
	}
	upd := protoQueueFor(h, q.GetId())
	upd.SpeedLimit = 0
	if _, err := h.dq.UpdateQueue(ctxT(t), connect.NewRequest(&downexv1.UpdateQueueRequest{Queue: upd})); err != nil {
		t.Fatal(err)
	}
	checkFile(t, h.waitCompleted(a), seed, size)
	checkFile(t, h.waitCompleted(b), seed, size)
}

// protoQueueFor returns queue id as UpdateQueue takes it.
func protoQueueFor(h *harness, id int64) *downexv1.Queue {
	q := h.queue(id)
	return &downexv1.Queue{Id: q.GetId(), Name: q.GetName(), MaxConcurrent: q.GetMaxConcurrent(), SpeedLimit: q.GetSpeedLimit(),
		Schedule: q.GetSchedule(), PostAction: q.GetPostAction()}
}

// postEvents collects the post-queue action events of an in-process Watch.
type postEvents struct {
	mu  sync.Mutex
	evs []*downexv1.PostActionPending
}

func (p *postEvents) all() []*downexv1.PostActionPending {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.evs)
}

func watchPosts(t *testing.T, h *harness) *postEvents {
	p := &postEvents{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = h.m.Watch(ctx, nil, func(r *downexv1.WatchResponse) error {
			p.mu.Lock()
			defer p.mu.Unlock()
			for _, ev := range r.GetEvents() {
				if pa := ev.GetPostAction(); pa != nil {
					p.evs = append(p.evs, pa)
				}
			}
			return nil
		})
	}()
	t.Cleanup(func() { cancel(); <-done })
	return p
}

// waitPending waits until the fake clock has n armed timers.
func waitPending(t *testing.T, fc *engine.FakeClock, n int) {
	t.Helper()
	waitUntil(t, fmt.Sprintf("%d armed timers", n), func() bool { return fc.Pending() == n })
}

// A finished queue's post-queue action counts down 60 s and then runs exactly once; the
// queue (not the main one) stops.
func TestPostActionRunsOnceAfterCountdown(t *testing.T) {
	const size, seed = 128 << 10, 66
	ts := serve(t, testserver.Options{Size: size, Seed: seed})
	fc := engine.NewFakeClock(time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC))
	power := &fakePower{}
	h := newHarness(t, harnessOptions{clock: fc, power: power})
	posts := watchPosts(t, h)
	notices, cancel := h.m.Subscribe()
	defer cancel()

	q := h.createQueue(&downexv1.Queue{Name: "Then sleep", PostAction: downexv1.PostAction_POST_ACTION_SLEEP})
	h.startQueue(q.GetId())
	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/s", QueueId: q.GetId()})
	checkFile(t, h.waitCompleted(d.GetId()), seed, size)

	var pending *downexv1.PostActionPending
	deadline := time.After(waitTimeout)
	for pending == nil {
		select {
		case n := <-notices:
			if n.Kind == NoticePostAction {
				pending = n.PostAction
			}
		case <-deadline:
			t.Fatal("no post-action notice")
		}
	}
	want := fc.Now().Add(PostActionDelay)
	if pending.GetQueueId() != q.GetId() || pending.GetAction() != downexv1.PostAction_POST_ACTION_SLEEP ||
		pending.GetCancelled() || !pending.GetDeadline().AsTime().Equal(want) {
		t.Fatalf("pending = %v, want deadline %v", pending, want)
	}
	waitUntil(t, "the Watch post_action event", func() bool { return len(posts.all()) == 1 })
	if h.queue(q.GetId()).GetRunning() {
		t.Fatal("the finished queue still runs")
	}
	waitPending(t, fc, 1) // the countdown

	fc.Advance(PostActionDelay - time.Second)
	time.Sleep(100 * time.Millisecond)
	if n := power.sleeps.Load(); n != 0 {
		t.Fatalf("slept %d times before the deadline", n)
	}
	fc.Advance(time.Second)
	waitUntil(t, "the sleep", func() bool { return power.sleeps.Load() == 1 })
	fc.Advance(10 * time.Minute)
	time.Sleep(100 * time.Millisecond)
	if n := power.sleeps.Load(); n != 1 || power.shutdowns.Load() != 0 || power.hibernates.Load() != 0 {
		t.Fatalf("power actions: %d sleeps, %d shutdowns, %d hibernations", n, power.shutdowns.Load(), power.hibernates.Load())
	}
	if err := h.m.CancelPostAction(ctxT(t), q.GetId()); connectCode(err) != connect.CodeFailedPrecondition {
		t.Fatalf("cancel after the action: %v", err)
	}
}

// CancelPostAction stops the countdown: the action never runs. The main queue keeps
// running after it finished.
func TestCancelPostActionPreventsIt(t *testing.T) {
	const size, seed = 128 << 10, 67
	ts := serve(t, testserver.Options{Size: size, Seed: seed})
	fc := engine.NewFakeClock(time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC))
	power := &fakePower{}
	h := newHarness(t, harnessOptions{clock: fc, power: power})
	posts := watchPosts(t, h)
	upd := protoQueueFor(h, mainQ)
	upd.PostAction = downexv1.PostAction_POST_ACTION_SHUTDOWN
	if _, err := h.dq.UpdateQueue(ctxT(t), connect.NewRequest(&downexv1.UpdateQueueRequest{Queue: upd})); err != nil {
		t.Fatal(err)
	}
	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/c"})
	checkFile(t, h.waitCompleted(d.GetId()), seed, size)
	waitUntil(t, "the countdown", func() bool { return len(posts.all()) == 1 })
	waitPending(t, fc, 1)
	if _, err := h.dm.CancelPostAction(ctxT(t), connect.NewRequest(&downexv1.CancelPostActionRequest{QueueId: mainQ})); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the cancellation event", func() bool {
		evs := posts.all()
		return len(evs) == 2 && evs[1].GetCancelled() && evs[1].GetQueueId() == mainQ
	})
	waitPending(t, fc, 0)
	fc.Advance(2 * PostActionDelay)
	time.Sleep(100 * time.Millisecond)
	if n := power.shutdowns.Load(); n != 0 {
		t.Fatalf("shut down %d times after the cancellation", n)
	}
	if !h.queue(mainQ).GetRunning() {
		t.Fatal("the main queue stopped after finishing")
	}
	if _, err := h.dm.CancelPostAction(ctxT(t), connect.NewRequest(&downexv1.CancelPostActionRequest{})); connectCode(err) != connect.CodeFailedPrecondition {
		t.Fatalf("cancel with nothing pending: %v", err)
	}
	if _, err := h.dm.CancelPostAction(ctxT(t), connect.NewRequest(&downexv1.CancelPostActionRequest{QueueId: 999})); connectCode(err) != connect.CodeNotFound {
		t.Fatalf("cancel for an unknown queue: %v", err)
	}
}

// A countdown is aborted when its queue gets work again; quitting runs Config.Quit once.
func TestPostActionAbortedByNewWorkAndQuit(t *testing.T) {
	const size, seed = 128 << 10, 68
	ts := serve(t, testserver.Options{Size: size, Seed: seed})
	fc := engine.NewFakeClock(time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC))
	var quits atomic.Int32
	h := newHarness(t, harnessOptions{clock: fc, quit: func() { quits.Add(1) }})
	posts := watchPosts(t, h)
	upd := protoQueueFor(h, mainQ)
	upd.PostAction = downexv1.PostAction_POST_ACTION_QUIT_DOWNEX
	if _, err := h.dq.UpdateQueue(ctxT(t), connect.NewRequest(&downexv1.UpdateQueueRequest{Queue: upd})); err != nil {
		t.Fatal(err)
	}
	first := h.add(&downexv1.AddRequest{Url: ts.URL + "/1"})
	checkFile(t, h.waitCompleted(first.GetId()), seed, size)
	waitUntil(t, "the countdown", func() bool { return len(posts.all()) == 1 })
	// Slow, so that it is still running (the queue busy) when the batch is sent: Watch
	// coalesces the events of a queue to the latest.
	second := h.add(&downexv1.AddRequest{Url: ts.URL + "/2", SpeedLimit: 16 << 10})
	waitUntil(t, "the abort", func() bool {
		evs := posts.all()
		return len(evs) >= 2 && evs[1].GetCancelled()
	})
	fc.Advance(2 * PostActionDelay)
	time.Sleep(100 * time.Millisecond)
	if quits.Load() != 0 {
		t.Fatal("quit although the queue had work again")
	}
	// The second download finishing starts a new countdown, which quits.
	unlimited := int64(0)
	if _, err := h.dl.SetDownloadOptions(ctxT(t), connect.NewRequest(&downexv1.SetDownloadOptionsRequest{Id: second.GetId(), SpeedLimit: &unlimited})); err != nil {
		t.Fatal(err)
	}
	checkFile(t, h.waitCompleted(second.GetId()), seed, size)
	waitUntil(t, "the second countdown", func() bool {
		evs := posts.all()
		return len(evs) == 3 && !evs[2].GetCancelled()
	})
	waitPending(t, fc, 1)
	fc.Advance(PostActionDelay)
	waitUntil(t, "quit", func() bool { return quits.Load() == 1 })
	time.Sleep(100 * time.Millisecond)
	if quits.Load() != 1 {
		t.Fatalf("quit %d times", quits.Load())
	}
}

// The schedule starts and stops a queue at its local times, applies a start missed
// while the computer slept when its window is still open, a missed stop, and arms the
// wake timer shortly before each start.
func TestScheduleStartsStopsAndCatchesUp(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 8 << 20, Seed: 69})
	fc := engine.NewFakeClock(time.Date(2026, 10, 21, 8, 59, 0, 0, time.UTC))
	wake := &fakeWake{}
	h := newHarness(t, harnessOptions{clock: fc, location: time.UTC, wake: wake})
	q := h.createQueue(&downexv1.Queue{Name: "Morning", Schedule: &downexv1.Schedule{
		Enabled: true, StartMinute: 9 * 60, StopEnabled: true, StopMinute: 9*60 + 30, Weekdays: model.AllWeekdays, WakeComputer: true,
	}})
	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/sched", SpeedLimit: 32 << 10, QueueId: q.GetId()}).GetId()
	waitPending(t, fc, 1)
	if want := time.Date(2026, 10, 21, 8, 59, 0, 0, time.UTC); !wake.last().Equal(want) { // a minute early
		t.Fatalf("wake timer = %v, want %v", wake.last(), want)
	}
	if h.get(d).GetState() != stQueued {
		t.Fatal("download started before its queue's schedule")
	}

	step := func(by time.Duration, what string, running bool) {
		t.Helper()
		fc.Advance(by)
		waitUntil(t, what, func() bool { return h.queue(q.GetId()).GetRunning() == running })
		waitPending(t, fc, 1)
	}
	step(time.Minute, "the scheduled start", true) // 09:00
	h.waitReceived(d, 1)
	if want := time.Date(2026, 10, 22, 8, 59, 0, 0, time.UTC); !wake.last().Equal(want) {
		t.Fatalf("wake timer after the start = %v, want %v", wake.last(), want)
	}
	step(30*time.Minute, "the scheduled stop", false) // 09:30
	h.waitState(d, stQueued)
	// Asleep over the next start; awake at 09:10 the next day, inside the window.
	step(23*time.Hour+40*time.Minute, "the missed start", true)
	h.waitReceived(d, 1)
	// Asleep over the stop.
	step(24*time.Hour+50*time.Minute, "the missed stop", false) // day 3, 10:00
	h.waitState(d, stQueued)
	waitUntil(t, "the schedule mark to be stored", func() bool {
		return storedQueue(t, h.st, q.GetId()).ScheduleMark.Equal(time.Date(2026, 10, 23, 9, 30, 0, 0, time.UTC))
	})
	// Asleep over two whole windows: nothing starts.
	fc.Advance(48 * time.Hour)
	waitPending(t, fc, 1)
	h.m.Reevaluate()
	time.Sleep(100 * time.Millisecond)
	if h.queue(q.GetId()).GetRunning() {
		t.Fatal("a queue started for windows that had closed")
	}
	// A disabled schedule disarms the wake timer.
	upd := protoQueueFor(h, q.GetId())
	upd.Schedule = nil
	if _, err := h.dq.UpdateQueue(ctxT(t), connect.NewRequest(&downexv1.UpdateQueueRequest{Queue: upd})); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the wake timer to be cancelled", func() bool { return wake.last().IsZero() })
}

// Inside its window, a repeating schedule queues unfinished downloads of the queue again
// every repeat_minutes.
func TestScheduleRepeatQueuesUnfinished(t *testing.T) {
	const size, seed = 128 << 10, 70
	ts := serve(t, testserver.Options{Size: size, Seed: seed})
	fc := engine.NewFakeClock(time.Date(2026, 10, 21, 8, 59, 0, 0, time.UTC))
	h := newHarness(t, harnessOptions{clock: fc, location: time.UTC})
	q := h.createQueue(&downexv1.Queue{Name: "Sync", Schedule: &downexv1.Schedule{
		Enabled: true, StartMinute: 9 * 60, Weekdays: model.AllWeekdays, RepeatMinutes: 10,
	}})
	waitPending(t, fc, 1)
	fc.Advance(time.Minute) // 09:00
	waitUntil(t, "the scheduled start", func() bool { return h.queue(q.GetId()).GetRunning() })
	waitPending(t, fc, 1)
	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/r", QueueId: q.GetId(), StartPaused: true}).GetId()
	fc.Advance(9 * time.Minute)
	waitPending(t, fc, 1)
	if h.get(d).GetState() != stPaused {
		t.Fatal("repeated before its interval")
	}
	fc.Advance(time.Minute) // 09:10
	checkFile(t, h.waitCompleted(d), seed, size)
	// The queue keeps running inside its open window (it repeats).
	time.Sleep(100 * time.Millisecond)
	if !h.queue(q.GetId()).GetRunning() {
		t.Fatal("a repeating queue stopped after finishing")
	}
}

// AddMedia puts a streaming download into the requested queue, where it waits until the
// queue runs; an unknown queue is refused before anything is fetched.
func TestAddMediaIntoQueue(t *testing.T) {
	fm := newFakeMedia(3)
	h := newHarness(t, harnessOptions{media: fm})
	q := h.createQueue(&downexv1.Queue{Name: "Videos"})
	d := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest, Source: downexv1.AddSource_ADD_SOURCE_CLI, QueueId: q.GetId()})
	if d.GetQueueId() != q.GetId() || d.GetState() != stQueued {
		t.Fatalf("media download = %v", d)
	}
	if got := storedState(t, h.st, d.GetId()); got.QueueID != model.QueueID(q.GetId()) {
		t.Fatalf("stored queue = %d", got.QueueID)
	}
	if _, err := h.dl.AddMedia(ctxT(t), connect.NewRequest(&downexv1.AddMediaRequest{ManifestUrl: testManifest, QueueId: 999})); connectCode(err) != connect.CodeInvalidArgument {
		t.Fatalf("media into an unknown queue: %v", err)
	}
	if probes, _ := fm.calls(); len(probes) != 1 {
		t.Fatalf("%d probes, want 1 (none for the refused add)", len(probes))
	}
	time.Sleep(100 * time.Millisecond)
	if _, runs := fm.calls(); len(runs) != 0 {
		t.Fatal("a media download of a stopped queue ran")
	}
	h.startQueue(q.GetId())
	h.waitCompleted(d.GetId())
}
