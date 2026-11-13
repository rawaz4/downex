package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"

	"downex/internal/limiter"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
)

// engineLimit returns the limit that the engine reads from the limiter of download id's
// current run (the cap it sizes its reads and its connection ramp-up by).
func (h *harness) engineLimit(id int64) int64 {
	h.t.Helper()
	lim, ok := h.runLimit(id)
	if !ok {
		h.t.Fatalf("download %d is not running", id)
	}
	return lim
}

func (h *harness) setSpeed(id, bytesPerSecond int64) {
	h.t.Helper()
	if _, err := h.dl.SetDownloadOptions(ctxT(h.t), connect.NewRequest(&downexv1.SetDownloadOptionsRequest{
		Id: id, SpeedLimit: &bytesPerSecond})); err != nil {
		h.t.Fatalf("set the speed limit of %d: %v", id, err)
	}
}

// A chain link reports the tightest limit above and including it, follows a new parent
// at once, waits on every level and honours cancellation.
func TestChainLink(t *testing.T) {
	global := limiter.New(0)
	slow := newChainLink(64<<10, global)
	free := newChainLink(0, global)
	run := newChainLink(0, slow)
	if got := run.Limit(); got != 64<<10 {
		t.Fatalf("below a 64 KiB/s queue: %d", got)
	}
	global.SetLimit(32 << 10)
	if got := run.Limit(); got != 32<<10 {
		t.Fatalf("below a 32 KiB/s global limit: %d", got)
	}
	run.SetLimit(16 << 10)
	if got := run.Limit(); got != 16<<10 {
		t.Fatalf("own 16 KiB/s: %d", got)
	}
	run.SetLimit(0)
	global.SetLimit(0)
	run.setParent(free)
	if got := run.Limit(); got != 0 {
		t.Fatalf("re-pointed below an unlimited chain: %d", got)
	}
	start := time.Now()
	if err := run.WaitN(context.Background(), 64<<20); err != nil || time.Since(start) > time.Second {
		t.Fatalf("unlimited wait: %v after %v", err, time.Since(start))
	}
	// Back below the 64 KiB/s queue (whose bucket starts empty), 96 KiB take about 1.5 s;
	// a cancelled wait returns at once.
	run.setParent(slow)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start = time.Now()
	if err := run.WaitN(ctx, 96<<10); !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("cancelled wait: %v after %v", err, time.Since(start))
	}
	if got := tightest(0, 0); got != 0 {
		t.Fatalf("tightest(0, 0) = %d", got)
	}
}

// The engine sees the tightest limit of the chain global → queue → download, not only
// the download's own: a download capped by its queue or by the global limit reads in
// capped slices and stops ramping up connections like one capped by its own limit.
func TestRunLimiterReportsTheChainsLimit(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 16 << 20, Seed: 71})
	h := newHarness(t, harnessOptions{settings: func(s *downexv1.Settings) { s.GlobalSpeedLimit = 48 << 10 }})
	slow := h.createQueue(&downexv1.Queue{Name: "Slow", SpeedLimit: 32 << 10})
	h.startQueue(slow.GetId())
	inQueue := h.add(&downexv1.AddRequest{Url: ts.URL + "/q", QueueId: slow.GetId()}).GetId()
	inMain := h.add(&downexv1.AddRequest{Url: ts.URL + "/m"}).GetId()
	h.waitReceived(inQueue, 1)
	h.waitReceived(inMain, 1)

	if got := h.engineLimit(inQueue); got != 32<<10 {
		t.Errorf("download in a queue limited to 32 KiB/s (global 48 KiB/s): the engine sees %d B/s", got)
	}
	if got := h.engineLimit(inMain); got != 48<<10 {
		t.Errorf("download in the unlimited main queue (global 48 KiB/s): the engine sees %d B/s", got)
	}
	// Its own limit, when tighter, wins; changes of every level apply at once.
	h.setSpeed(inMain, 16<<10)
	if got := h.engineLimit(inMain); got != 16<<10 {
		t.Errorf("own limit 16 KiB/s: the engine sees %d B/s", got)
	}
	s := protoSettings(h.m.Settings())
	s.GlobalSpeedLimit = 8 << 10
	if _, err := h.dm.UpdateSettings(ctxT(t), connect.NewRequest(&downexv1.UpdateSettingsRequest{Settings: s})); err != nil {
		t.Fatal(err)
	}
	if got := h.engineLimit(inQueue); got != 8<<10 {
		t.Errorf("global lowered to 8 KiB/s: the engine sees %d B/s for the queue's download", got)
	}
	upd := protoQueueFor(h, slow.GetId())
	upd.SpeedLimit = 4 << 10
	if _, err := h.dq.UpdateQueue(ctxT(t), connect.NewRequest(&downexv1.UpdateQueueRequest{Queue: upd})); err != nil {
		t.Fatal(err)
	}
	if got := h.engineLimit(inQueue); got != 4<<10 {
		t.Errorf("queue lowered to 4 KiB/s: the engine sees %d B/s", got)
	}
}

// A running download that changes queues takes its new queue's limit at once: deleting
// a limited queue frees its running downloads in the main queue, and moving one into a
// limited queue caps it there.
func TestMovedRunFollowsItsNewQueuesLimit(t *testing.T) {
	const size, seed = 2 << 20, 72
	ts := serve(t, testserver.Options{Size: size, Seed: seed})
	big := serve(t, testserver.Options{Size: 32 << 20, Seed: 73})
	h := newHarness(t, harnessOptions{})
	slow := h.createQueue(&downexv1.Queue{Name: "Slow", SpeedLimit: 16 << 10})
	h.startQueue(slow.GetId())
	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/d", QueueId: slow.GetId()}).GetId()
	h.waitReceived(d, 1)

	if _, err := h.dq.DeleteQueue(ctxT(t), connect.NewRequest(&downexv1.DeleteQueueRequest{Id: slow.GetId()})); err != nil {
		t.Fatal(err)
	}
	if got := h.get(d); got.GetQueueId() != mainQ || !isRunning(got) {
		t.Fatalf("download of the deleted queue = %v in queue %d", got.GetState(), got.GetQueueId())
	}
	if got := h.engineLimit(d); got != 0 {
		t.Errorf("in the unlimited main queue the engine still sees %d B/s", got)
	}
	// 2 MiB at the deleted queue's 16 KiB/s would take more than two minutes.
	checkFile(t, h.waitCompleted(d), seed, size)

	capped := h.createQueue(&downexv1.Queue{Name: "Capped", SpeedLimit: 24 << 10})
	h.startQueue(capped.GetId())
	e := h.add(&downexv1.AddRequest{Url: big.URL + "/e", SpeedLimit: 256 << 10}).GetId()
	h.waitReceived(e, 64<<10)
	h.move(capped.GetId(), -1, e)
	if got := h.engineLimit(e); got != 24<<10 {
		t.Errorf("moved into a queue limited to 24 KiB/s: the engine sees %d B/s", got)
	}
	from := h.get(e).GetReceived()
	time.Sleep(1500 * time.Millisecond)
	// 1.5 s at 24 KiB/s plus a slice in flight and a bucket's depth; at its own 256 KiB/s
	// it would have received 384 KiB.
	if got := h.get(e).GetReceived() - from; got > 192<<10 {
		t.Errorf("moved into a queue limited to 24 KiB/s, it received %d bytes in 1.5 s", got)
	}
}
