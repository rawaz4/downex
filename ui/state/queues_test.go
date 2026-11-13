package state_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/state"
	"downex/ui/state/statetest"
)

const (
	actNone     = downexv1.PostAction_POST_ACTION_UNSPECIFIED
	actQuit     = downexv1.PostAction_POST_ACTION_QUIT_DOWNEX
	actSleep    = downexv1.PostAction_POST_ACTION_SLEEP
	actShutdown = downexv1.PostAction_POST_ACTION_SHUTDOWN
)

func setupQueues(t *testing.T, edit ...func(*state.QueuesOptions)) (*statetest.Daemon, *state.QueuesStore, *statetest.Clock) {
	t.Helper()
	f := statetest.NewDaemon(dl(1, nil), dl(2, nil), dl(3, nil))
	f.SetQueue(statetest.Queue(2, func(q *downexv1.Queue) { q.Name = "Night" }))
	c := statetest.NewClock(1_800_000_000_000)
	o := state.QueuesOptions{Daemon: f, Events: &f.Events, Clock: c, Go: statetest.Sync}
	for _, e := range edit {
		e(&o)
	}
	s := state.NewQueues(o)
	t.Cleanup(s.Dispose)
	s.Start()
	return f, s, c
}

func names(qs []*downexv1.Queue) []string {
	var out []string
	for _, q := range qs {
		out = append(out, q.GetName())
	}
	return out
}

func spec(name string) *downexv1.Queue {
	return &downexv1.Queue{Name: name, MaxConcurrent: 1, Schedule: &downexv1.Schedule{}}
}

func TestQueuesReadsAndFollowsEvents(t *testing.T) {
	f, s, _ := setupQueues(t)
	f.SetQueue(statetest.Queue(5, func(q *downexv1.Queue) { q.Name = "Later" }))
	if !s.Load() || !slices.Equal(names(s.List()), []string{"Main queue", "Night", "Later"}) || !s.Loaded() {
		t.Fatal(names(s.List()))
	}
	f.Emit(statetest.QueueUpdated(statetest.Queue(2, func(q *downexv1.Queue) { q.Name, q.Running = "Nightly", true })), statetest.QueueRemoved(5))
	if q, _ := s.Get(2); q.GetName() != "Nightly" || !q.GetRunning() {
		t.Fatal(q)
	}
	if _, ok := s.Get(5); ok {
		t.Fatal("removed queue kept")
	}
	if ids := s.IDs(); len(ids) != 2 || !ids[1] || !ids[2] {
		t.Fatal(ids)
	}
	if s.NameOf(9) != "Queue 9" {
		t.Fatal(s.NameOf(9))
	}
}

func TestQueuesKeepsWhatEventsSaidWhileAReadWasInFlight(t *testing.T) {
	f, s, _ := setupQueues(t)
	gate := make(chan struct{})
	entered := make(chan struct{})
	f.Before = func(name string, _ ...any) error {
		if name == "listQueues" {
			close(entered)
			<-gate
		}
		return nil
	}
	done := make(chan bool)
	go func() { done <- s.Load() }()
	<-entered
	// Meanwhile Night started and a new queue appeared.
	f.Emit(statetest.QueueUpdated(statetest.Queue(2, func(q *downexv1.Queue) { q.Name, q.Running = "Night", true })))
	f.Emit(statetest.QueueUpdated(statetest.Queue(7, func(q *downexv1.Queue) { q.Name = "New" })))
	close(gate)
	<-done
	if q, _ := s.Get(2); !q.GetRunning() {
		t.Fatal(q)
	}
	if q, _ := s.Get(7); q.GetName() != "New" {
		t.Fatal(q)
	}
}

func TestQueuesReadsAgainAfterAReSubscription(t *testing.T) {
	f, s, _ := setupQueues(t)
	s.Load()
	f.Emit(statetest.Snapshot(dl(1, nil)))
	if f.Count("listQueues") != 1 {
		t.Fatal(f.Count("listQueues"))
	}
	f.DeleteQueueItem(2) // removed while the stream was down
	f.Emit(statetest.Snapshot(dl(1, nil)))
	if f.Count("listQueues") != 2 {
		t.Fatal(f.Count("listQueues"))
	}
	if _, ok := s.Get(2); ok {
		t.Fatal("removed queue kept")
	}
}

func TestQueuesShowsNoneForADaemonWithoutThem(t *testing.T) {
	f, s, _ := setupQueues(t)
	f.Before = func(name string, _ ...any) error {
		if name == "listQueues" {
			return statetest.Err(connect.CodeUnimplemented, "unknown procedure")
		}
		return nil
	}
	if !s.Load() || s.Supported() || !s.Loaded() || len(s.List()) != 0 {
		t.Fatal(s.Supported(), s.Loaded(), s.List())
	}
	of, os, _ := setupQueues(t)
	of.SetDown(true)
	if os.Load() || os.Loaded() || !strings.Contains(os.LoadError(), "connection refused") {
		t.Fatal(os.LoadError())
	}
}

func TestQueuesKeepsCountdownsByQueue(t *testing.T) {
	f, s, c := setupQueues(t)
	s.Load()
	now := c.Now().UnixMilli()
	f.Emit(statetest.PostAction(2, actShutdown, now+60_000, false), statetest.PostAction(1, actSleep, now+30_000, false))
	if p := s.NextAction(); p.GetQueueId() != 1 || p.GetAction() != actSleep {
		t.Fatal(p)
	}
	f.Emit(statetest.PostAction(1, actSleep, now+30_000, true))
	if p := s.NextAction(); p.GetQueueId() != 2 {
		t.Fatal(p)
	}
	if ok, err := s.CancelPostAction(ctx, 2); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if a := f.Args("cancelPostAction"); len(a) != 1 || a[0] != int64(2) {
		t.Fatal(a)
	}
	if s.NextAction() != nil {
		t.Fatal(s.NextAction())
	}
	// A countdown of a deleted queue goes with it.
	f.Emit(statetest.PostAction(2, actQuit, c.Now().UnixMilli()+60_000, false))
	f.Emit(statetest.QueueRemoved(2))
	if len(s.Pending()) != 0 {
		t.Fatal(s.Pending())
	}
}

func TestQueuesKeepsCountdownsWhileAStatusCheckFails(t *testing.T) {
	f, s, c := setupQueues(t)
	s.Load()
	f.Emit(statetest.PostAction(1, actShutdown, c.Now().UnixMilli()+60_000, false))
	s.Invalidate()
	if s.Loaded() {
		t.Fatal("still loaded")
	}
	if p := s.NextAction(); p.GetQueueId() != 1 || p.GetAction() != actShutdown {
		t.Fatal(p)
	}
}

func TestQueuesTakesTheCountdownsOfANewSubscription(t *testing.T) {
	f, s, c := setupQueues(t)
	s.Load()
	soon := c.Now().UnixMilli() + 60_000
	f.Emit(statetest.Snapshot(dl(1, nil)))
	f.Emit(statetest.PostAction(1, actSleep, soon, false), statetest.PostAction(2, actShutdown, soon, false))
	if len(s.Pending()) != 2 {
		t.Fatal(s.Pending())
	}
	// The stream was down meanwhile: Main's countdown was cancelled elsewhere, Night's runs on.
	f.Emit(statetest.Snapshot(dl(1, nil)), statetest.QueueUpdated(statetest.Queue(2, func(q *downexv1.Queue) { q.Name = "Night" })), statetest.PostAction(2, actShutdown, soon, false))
	if p := s.Pending(); len(p) != 1 || p[2] == nil {
		t.Fatal(p)
	}
}

func TestQueuesDropsACountdownShortlyAfterItsDeadline(t *testing.T) {
	f, s, c := setupQueues(t)
	s.Load()
	now := c.Now().UnixMilli()
	f.Emit(statetest.PostAction(2, actSleep, now+60_000, false), statetest.PostAction(1, actShutdown, now+90_000, false))
	c.Advance(60 * time.Second)
	if p := s.NextAction(); p.GetQueueId() != 2 { // "Going to sleep now…" for a moment
		t.Fatal(p)
	}
	c.Advance(state.PostActionGrace)
	if p := s.NextAction(); p.GetQueueId() != 1 {
		t.Fatal(p)
	}
	c.Advance(30*time.Second + state.PostActionGrace)
	if s.NextAction() != nil {
		t.Fatal(s.NextAction())
	}
	// One without a deadline gets the usual 60 s.
	f.Emit(statetest.PostAction(1, actQuit, 0, false))
	c.Advance(60 * time.Second)
	if s.NextAction() == nil {
		t.Fatal("ended too soon")
	}
	c.Advance(state.PostActionGrace)
	if s.NextAction() != nil {
		t.Fatal(s.NextAction())
	}
}

func TestQueuesTreatsACountdownDownexdNoLongerHasAsGone(t *testing.T) {
	f, s, c := setupQueues(t)
	s.Load()
	soon := c.Now().UnixMilli() + 60_000
	f.Emit(statetest.PostAction(1, actSleep, soon, false), statetest.PostAction(2, actShutdown, soon, false))
	var refuse func(id int64) error
	f.Before = func(name string, args ...any) error {
		if name == "cancelPostAction" && refuse != nil {
			return refuse(args[0].(int64))
		}
		return nil
	}
	refuse = func(id int64) error {
		if id == 1 {
			return statetest.Err(connect.CodeFailedPrecondition, "queue 1 has no post-queue action pending")
		}
		return statetest.Err(connect.CodeNotFound, "queue 2 not found")
	}
	if ok, err := s.CancelPostAction(ctx, 1); ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, err := s.CancelPostAction(ctx, 2); ok || err != nil {
		t.Fatal(ok, err)
	}
	if len(s.Pending()) != 0 {
		t.Fatal(s.Pending())
	}
	// Other failures keep it: it may still run.
	f.Emit(statetest.PostAction(1, actSleep, soon, false))
	refuse = func(int64) error { return statetest.Err(connect.CodeUnavailable, "connection refused") }
	if _, err := s.CancelPostAction(ctx, 1); err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatal(err)
	}
	if s.Pending()[1] == nil {
		t.Fatal("dropped")
	}
	refuse = nil
	if ok, err := s.CancelPostAction(ctx, 1); !ok || err != nil || len(s.Pending()) != 0 {
		t.Fatal(ok, err)
	}
}

func TestQueuesCreatesChangesStartsStopsMovesAndDeletes(t *testing.T) {
	f, s, _ := setupQueues(t)
	s.Load()
	created, err := s.Create(ctx, spec("Weekend"))
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetId()
	if q, _ := s.Get(id); q.GetName() != "Weekend" {
		t.Fatal(q)
	}
	_, _ = s.Update(ctx, id, spec("Weekends"))
	if q, _ := s.Get(id); q.GetName() != "Weekends" {
		t.Fatal(q)
	}
	if q, _ := s.StartQueue(ctx, id); !q.GetRunning() {
		t.Fatal(q)
	}
	if q, _ := s.Get(id); !q.GetRunning() {
		t.Fatal(q)
	}
	_, _ = s.StopQueue(ctx, id)
	if q, _ := s.Get(id); q.GetRunning() {
		t.Fatal(q)
	}
	_, _ = s.Move(ctx, []int64{1, 3}, id, -1)
	if q, _ := s.Get(id); !slices.Equal(q.GetDownloadIds(), []int64{1, 3}) {
		t.Fatal(q.GetDownloadIds())
	}
	if q, _ := s.Get(1); !slices.Equal(q.GetDownloadIds(), []int64{2}) { // they left the main queue
		t.Fatal(q.GetDownloadIds())
	}
	if a := f.Args("moveDownloads"); !slices.Equal(a[0].([]int64), []int64{1, 3}) || a[1] != id || a[2] != int32(-1) {
		t.Fatal(a)
	}
	if err := s.Remove(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get(id); ok {
		t.Fatal("still there")
	}
	if q, _ := s.Get(1); !slices.Equal(q.GetDownloadIds(), []int64{2, 1, 3}) {
		t.Fatal(q.GetDownloadIds())
	}
	if err := s.Remove(ctx, 1); err == nil || !strings.Contains(err.Error(), "main queue") {
		t.Fatal(err)
	}
}

func TestQueuesChangesOnlyWhatAQueueDoesWhenDone(t *testing.T) {
	f, s, _ := setupQueues(t)
	f.SetQueue(statetest.Queue(2, func(q *downexv1.Queue) {
		q.Name, q.MaxConcurrent, q.SpeedLimit, q.PostAction, q.DownloadIds = "Night", 2, 4096, actNone, []int64{3}
	}))
	s.Load()
	q, err := s.SetPostAction(ctx, 2, actShutdown)
	if err != nil {
		t.Fatal(err)
	}
	a := f.Args("updateQueue")
	sent := a[1].(*downexv1.Queue)
	if a[0] != int64(2) || sent.GetName() != "Night" || sent.GetMaxConcurrent() != 2 || sent.GetSpeedLimit() != 4096 || sent.GetPostAction() != actShutdown ||
		sent.GetSchedule().GetEnabled() || len(sent.GetDownloadIds()) != 0 {
		t.Fatal(a)
	}
	if q.GetPostAction() != actShutdown {
		t.Fatal(q)
	}
	if g, _ := s.Get(2); g.GetPostAction() != actShutdown || !slices.Equal(g.GetDownloadIds(), []int64{3}) {
		t.Fatal(g)
	}
	// A queue this window does not know is refused as downexd would.
	_, err = s.SetPostAction(ctx, 9, actSleep)
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodeNotFound {
		t.Fatal(err)
	}
}
