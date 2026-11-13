package daemon

import (
	"context"
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

// skewClock is a FakeClock whose wall clock can be set apart from its timers: a clock
// that is set, or a sleep that the timers did not count (as on macOS).
type skewClock struct {
	*engine.FakeClock
	offset atomic.Int64 // added to the fake time by Now
}

func (c *skewClock) Now() time.Time { return c.FakeClock.Now().Add(time.Duration(c.offset.Load())) }

// set makes Now return t (the timers keep their schedule).
func (c *skewClock) set(t time.Time) { c.offset.Store(int64(t.Sub(c.FakeClock.Now()))) }

// pendingPosts counts the post-queue countdowns in progress.
func (h *harness) pendingPosts() int {
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	return len(h.m.pendingPostsLocked())
}

func (h *harness) setPostAction(id int64, a downexv1.PostAction) {
	h.t.Helper()
	upd := protoQueueFor(h, id)
	upd.PostAction = a
	if _, err := h.dq.UpdateQueue(ctxT(h.t), connect.NewRequest(&downexv1.UpdateQueueRequest{Queue: upd})); err != nil {
		h.t.Fatalf("set the post action of queue %d: %v", id, err)
	}
}

// Pausing the remaining downloads never finishes a queue: "Pause all" to free the line
// must not start a shutdown. The queue finishes when its downloads complete.
func TestPauseAllDoesNotFinishAQueue(t *testing.T) {
	small := serve(t, testserver.Options{Size: 128 << 10, Seed: 74})
	const size, seed = 2 << 20, 75
	big := serve(t, testserver.Options{Size: size, Seed: seed})
	fc := engine.NewFakeClock(time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC))
	power := &fakePower{}
	h := newHarness(t, harnessOptions{clock: fc, power: power, settings: func(s *downexv1.Settings) { s.MaxConcurrentDownloads = 1 }})
	h.setPostAction(mainQ, downexv1.PostAction_POST_ACTION_SHUTDOWN)
	h.stopQueue(mainQ)
	d1 := h.add(&downexv1.AddRequest{Url: small.URL + "/1"}).GetId()
	d2 := h.add(&downexv1.AddRequest{Url: big.URL + "/2", SpeedLimit: 32 << 10}).GetId()
	d3 := h.add(&downexv1.AddRequest{Url: big.URL + "/3", SpeedLimit: 32 << 10}).GetId()
	h.startQueue(mainQ)
	h.waitCompleted(d1)
	h.waitReceived(d2, 1)
	if got := h.get(d3).GetState(); got != stQueued {
		t.Fatalf("d3 = %v", got)
	}

	n, err := h.m.PauseAll(ctxT(t))
	if err != nil || n != 2 {
		t.Fatalf("PauseAll = %d, %v", n, err)
	}
	if got := h.get(d2).GetState(); got != stPaused {
		t.Fatalf("d2 after PauseAll = %v", got)
	}
	if n := h.pendingPosts(); n != 0 {
		t.Fatalf("%d post-queue countdowns after PauseAll", n)
	}
	fc.Advance(2 * PostActionDelay)
	time.Sleep(100 * time.Millisecond)
	if n := power.shutdowns.Load(); n != 0 {
		t.Fatalf("PauseAll shut the computer down %d times", n)
	}

	// Resumed and completed, the downloads finish the queue.
	unlimited := int64(0)
	for _, id := range []int64{d2, d3} {
		h.resume(&downexv1.ResumeRequest{Id: id, SpeedLimit: &unlimited})
	}
	checkFile(t, h.waitCompleted(d2), seed, size)
	checkFile(t, h.waitCompleted(d3), seed, size)
	waitUntil(t, "the countdown", func() bool { return h.pendingPosts() == 1 })
	waitPending(t, fc, 1)
	fc.Advance(PostActionDelay)
	waitUntil(t, "the shutdown", func() bool { return power.shutdowns.Load() == 1 })
}

// finishQueues adds a download to each of the stopped queues, starts them and waits
// until their post-queue actions count down (the fake clock stands still: their deadlines
// are the same).
func finishQueues(t *testing.T, h *harness, fc *engine.FakeClock, url string, queues ...int64) {
	t.Helper()
	var ids []int64
	for _, q := range queues {
		ids = append(ids, h.add(&downexv1.AddRequest{Url: url, QueueId: q}).GetId())
		h.startQueue(q)
	}
	for _, id := range ids {
		h.waitCompleted(id)
	}
	waitUntil(t, "the countdowns", func() bool { return h.pendingPosts() == len(queues) })
	waitPending(t, fc, len(queues))
}

// A countdown whose deadline passed long ago (the computer slept, the timers fired on
// waking) performs nothing and is reported as cancelled; of countdowns due together only
// one power action runs.
func TestOverdueCountdownsDoNotPowerOff(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 64 << 10, Seed: 76})
	fc := engine.NewFakeClock(time.Date(2026, 10, 21, 22, 0, 0, 0, time.UTC))
	power := &fakePower{}
	h := newHarness(t, harnessOptions{clock: fc, power: power})
	notices, cancel := h.m.Subscribe()
	defer cancel()
	a := h.createQueue(&downexv1.Queue{Name: "A", PostAction: downexv1.PostAction_POST_ACTION_SLEEP}).GetId()
	b := h.createQueue(&downexv1.Queue{Name: "B", PostAction: downexv1.PostAction_POST_ACTION_SLEEP}).GetId()

	finishQueues(t, h, fc, ts.URL+"/1", a, b)
	fc.Advance(8 * time.Hour) // asleep over both deadlines; the timers fire on waking
	h.m.Reevaluate()
	waitUntil(t, "the overdue countdowns to end", func() bool { return h.pendingPosts() == 0 })
	time.Sleep(100 * time.Millisecond)
	if n := power.sleeps.Load(); n != 0 {
		t.Fatalf("overdue countdowns slept %d times", n)
	}
	cancelled := 0
	for len(notices) > 0 {
		if n := <-notices; n.Kind == NoticePostAction && n.PostAction.GetCancelled() {
			cancelled++
		}
	}
	if cancelled != 2 {
		t.Fatalf("%d countdowns reported as cancelled, want 2", cancelled)
	}

	// Due together: one sleep; the other countdown is cancelled by it.
	finishQueues(t, h, fc, ts.URL+"/2", a, b)
	fc.Advance(PostActionDelay)
	waitUntil(t, "the sleep", func() bool { return power.sleeps.Load() >= 1 })
	time.Sleep(200 * time.Millisecond)
	if n := power.sleeps.Load(); n != 1 || h.pendingPosts() != 0 {
		t.Fatalf("%d sleeps, %d countdowns left, want 1 and 0", n, h.pendingPosts())
	}
}

// When the computer wakes with the countdown's deadline long past while its timer has not
// fired (timers that do not count the time asleep), Reevaluate on resume cancels it.
func TestResumeCancelsAnOverdueCountdown(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 64 << 10, Seed: 77})
	fc := engine.NewFakeClock(time.Date(2026, 10, 21, 22, 0, 0, 0, time.UTC))
	clk := &skewClock{FakeClock: fc}
	power := &fakePower{}
	h := newHarness(t, harnessOptions{clock: clk, power: power})
	h.setPostAction(mainQ, downexv1.PostAction_POST_ACTION_SLEEP)
	h.waitCompleted(h.add(&downexv1.AddRequest{Url: ts.URL + "/s"}).GetId())
	waitPending(t, fc, 1)

	clk.set(clk.Now().Add(8 * time.Hour)) // the wall clock moved on while asleep
	h.m.Reevaluate()
	waitPending(t, fc, 0) // the countdown's timer stopped: it was cancelled
	if n := h.pendingPosts(); n != 0 {
		t.Fatalf("%d countdowns after the resume", n)
	}
	fc.Advance(2 * PostActionDelay)
	time.Sleep(100 * time.Millisecond)
	if n := power.sleeps.Load(); n != 0 {
		t.Fatalf("slept %d times after waking", n)
	}
}

// A download awaiting confirmation is work of its queue: the queue does not finish while
// the confirmation is open, and one that arrives during a countdown cancels it.
func TestAwaitingConfirmationKeepsTheQueueBusy(t *testing.T) {
	const size, seed = 64 << 10, 78
	ts := serve(t, testserver.Options{Size: size, Seed: seed})
	fc := engine.NewFakeClock(time.Date(2026, 10, 21, 22, 0, 0, 0, time.UTC))
	power := &fakePower{}
	h := newHarness(t, harnessOptions{clock: fc, power: power})
	h.setPostAction(mainQ, downexv1.PostAction_POST_ACTION_SLEEP)

	aw := h.add(&downexv1.AddRequest{Url: ts.URL + "/aw", Source: downexv1.AddSource_ADD_SOURCE_BROWSER})
	if aw.GetState() != stAwaiting {
		t.Fatalf("hand-off = %v", aw.GetState())
	}
	h.waitCompleted(h.add(&downexv1.AddRequest{Url: ts.URL + "/d"}).GetId())
	if n := h.pendingPosts(); n != 0 {
		t.Fatalf("%d countdowns while a download awaits confirmation", n)
	}
	fc.Advance(2 * PostActionDelay)
	time.Sleep(100 * time.Millisecond)
	if n := power.sleeps.Load(); n != 0 {
		t.Fatalf("slept %d times with a confirmation open", n)
	}

	// Confirmed and completed, it finishes the queue.
	if _, err := h.dl.Confirm(ctxT(t), connect.NewRequest(&downexv1.ConfirmRequest{Id: aw.GetId(), Start: true})); err != nil {
		t.Fatal(err)
	}
	checkFile(t, h.waitCompleted(aw.GetId()), seed, size)
	waitUntil(t, "the countdown", func() bool { return h.pendingPosts() == 1 })
	// A hand-off arriving during the countdown cancels it.
	if got := h.add(&downexv1.AddRequest{Url: ts.URL + "/aw2", Source: downexv1.AddSource_ADD_SOURCE_BROWSER}); got.GetState() != stAwaiting {
		t.Fatalf("second hand-off = %v", got.GetState())
	}
	if n := h.pendingPosts(); n != 0 {
		t.Fatalf("%d countdowns after a hand-off arrived", n)
	}
	fc.Advance(2 * PostActionDelay)
	time.Sleep(100 * time.Millisecond)
	if n := power.sleeps.Load(); n != 0 {
		t.Fatalf("slept %d times with a confirmation open", n)
	}
}

// holdStore holds the next UpdateDownload of one download until released.
type holdStore struct {
	*store.Memory
	mu      sync.Mutex
	id      model.DownloadID
	entered chan struct{}
	release chan struct{}
}

func (s *holdStore) hold(id int64) (entered, release chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.id, s.entered, s.release = model.DownloadID(id), make(chan struct{}), make(chan struct{})
	return s.entered, s.release
}

func (s *holdStore) UpdateDownload(ctx context.Context, d *model.Download) error {
	s.mu.Lock()
	held := s.id != 0 && d.ID == s.id
	entered, release := s.entered, s.release
	if held {
		s.id = 0
	}
	s.mu.Unlock()
	if held {
		close(entered)
		<-release
	}
	return s.Memory.UpdateDownload(ctx, d)
}

// A queued download that an operation has out of the waiting set while it writes the
// record is still work of its queue: the queue's last run completing meanwhile neither
// finishes nor stops the queue, and the download runs once it is back.
func TestQueuedOptionsChangeDuringTheLastCompletion(t *testing.T) {
	const size, seed = 512 << 10, 79
	ts := serve(t, testserver.Options{Size: size, Seed: seed})
	fc := engine.NewFakeClock(time.Date(2026, 10, 21, 22, 0, 0, 0, time.UTC))
	power := &fakePower{}
	st := &holdStore{Memory: store.NewMemory()}
	h := newHarness(t, harnessOptions{st: st, clock: fc, power: power})
	night := h.createQueue(&downexv1.Queue{Name: "Night", MaxConcurrent: 1, PostAction: downexv1.PostAction_POST_ACTION_SLEEP})
	h.startQueue(night.GetId())
	r := h.add(&downexv1.AddRequest{Url: ts.URL + "/r", QueueId: night.GetId(), SpeedLimit: 32 << 10}).GetId()
	x := h.add(&downexv1.AddRequest{Url: ts.URL + "/x", QueueId: night.GetId()}).GetId()
	h.waitReceived(r, 1)

	entered, release := st.hold(x)
	done := make(chan error, 1)
	go func() {
		limit := int64(8 << 20)
		_, err := h.dl.SetDownloadOptions(ctxT(t), connect.NewRequest(&downexv1.SetDownloadOptionsRequest{Id: x, SpeedLimit: &limit}))
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(waitTimeout):
		t.Fatal("SetDownloadOptions did not write the record")
	}
	h.setSpeed(r, 0)
	checkFile(t, h.waitCompleted(r), seed, size)
	busy := h.pendingPosts() == 0 && h.queue(night.GetId()).GetRunning()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !busy {
		t.Fatal("the queue finished while its next download was being changed")
	}
	checkFile(t, h.waitCompleted(x), seed, size)
	waitUntil(t, "the countdown", func() bool { return h.pendingPosts() == 1 })
}
