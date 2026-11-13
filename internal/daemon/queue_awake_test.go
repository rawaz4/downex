package daemon

import (
	"sync/atomic"
	"testing"
	"time"

	"downex/internal/engine"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
)

// fakeAwake records whether the computer would be kept awake; the system is never
// asked.
type fakeAwake struct {
	held  atomic.Bool
	holds atomic.Int32
}

func (a *fakeAwake) Hold() error {
	if !a.held.Swap(true) {
		a.holds.Add(1)
	}
	return nil
}

func (a *fakeAwake) Release() error { a.held.Store(false); return nil }

// The computer is kept from idling into sleep while a queue with a post-queue action or
// a schedule has work, and while a countdown is pending; other downloads leave the
// system's sleep alone, and Shutdown releases it.
func TestKeepAwakeWhileAQueueThatMattersHasWork(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 4 << 20, Seed: 82})
	fc := engine.NewFakeClock(time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC))
	power, awake := &fakePower{}, &fakeAwake{}
	h := newHarness(t, harnessOptions{clock: fc, location: time.UTC, power: power, awake: awake})
	held := func(want bool, what string) {
		t.Helper()
		waitUntil(t, what, func() bool { return awake.held.Load() == want })
	}
	stays := func(want bool, what string) {
		t.Helper()
		time.Sleep(100 * time.Millisecond)
		if awake.held.Load() != want {
			t.Fatal(what)
		}
	}

	plain := h.add(&downexv1.AddRequest{Url: ts.URL + "/plain", SpeedLimit: 64 << 10}).GetId()
	h.waitReceived(plain, 1)
	stays(false, "kept awake for a download of a queue without post-queue action or schedule")
	h.pause(plain)

	night := h.createQueue(&downexv1.Queue{Name: "Night", PostAction: downexv1.PostAction_POST_ACTION_SHUTDOWN}).GetId()
	h.startQueue(night)
	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/night", QueueId: night, SpeedLimit: 64 << 10}).GetId()
	held(true, "the computer to be kept awake for the queue's download")
	h.setSpeed(d, 0)
	h.waitCompleted(d)
	waitUntil(t, "the countdown", func() bool { return h.pendingPosts() == 1 })
	stays(true, "released while the post-queue action counts down")
	if err := h.m.CancelPostAction(ctxT(t), night); err != nil {
		t.Fatal(err)
	}
	held(false, "the release after the cancellation")

	// A scheduled queue: not while it is stopped, then while it runs its download.
	sched := h.createQueue(&downexv1.Queue{Name: "Noon", Schedule: &downexv1.Schedule{
		Enabled: true, StartMinute: 12*60 + 1, Weekdays: model.AllWeekdays,
	}}).GetId()
	h.add(&downexv1.AddRequest{Url: ts.URL + "/sched", QueueId: sched, SpeedLimit: 64 << 10})
	stays(false, "kept awake for a stopped queue's waiting download")
	waitPending(t, fc, 1)
	fc.Advance(time.Minute)
	held(true, "the computer to be kept awake for the scheduled queue")

	if err := h.m.Shutdown(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	if awake.held.Load() {
		t.Fatal("still kept awake after Shutdown")
	}
	if n := power.shutdowns.Load(); n != 0 {
		t.Fatalf("%d shutdowns", n)
	}
}
