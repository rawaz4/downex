package state_test

import (
	"slices"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/state"
	"downex/ui/state/statetest"
)

// The ordered races of reportOrder: downexd changes queue_position without moving
// updated_at (MoveDownloads renumbers the queue it changes, internal/store MoveDownloads),
// so a report with the same updated_at as a newer one must not replace it. The window of
// TestReordersReachDownexd once showed queue positions [1 1 3] while downexd had [2 1 3].

// gate holds the calls named name at the fake's gate: reached receives once per held call,
// and each call waits for one release.
type gate struct {
	reached chan struct{}
	release chan struct{}
}

func holdCalls(f *statetest.Daemon, name string) *gate {
	g := &gate{reached: make(chan struct{}, 16), release: make(chan struct{}, 16)}
	f.Before = func(n string, _ ...any) error {
		if n == name {
			g.reached <- struct{}{}
			<-g.release
		}
		return nil
	}
	return g
}

// queued is a download waiting at position pos, last updated at ms.
func queued(id int64, pos int32, ms int64) *downexv1.Download {
	return dl(id, func(d *downexv1.Download) {
		d.State = state.StateQueued
		d.QueuePosition = pos
		d.UpdatedAt = statetest.Ms(ms)
	})
}

func positions(s *state.DownloadsStore, ids ...int64) []int32 {
	var out []int32
	for _, id := range ids {
		d, ok := s.Get(id)
		if !ok {
			out = append(out, -1)
			continue
		}
		out = append(out, d.GetQueuePosition())
	}
	return out
}

func TestReloadSentBeforeAnEventKeepsTheEvent(t *testing.T) {
	f, s := fake(t, queued(1, 1, 100), queued(2, 2, 100), queued(3, 3, 100))
	g := holdCalls(f, "list")
	var wg sync.WaitGroup
	wg.Go(func() { s.Reload(ctx) }) // after a reconnect: List is sent before the move
	<-g.reached
	// The move (2 to the front) reaches the window while List is out: downexd's List
	// answer was read before it and carries the same updated_at.
	f.Emit(statetest.Updated(queued(2, 1, 100)))
	f.Emit(statetest.Updated(queued(1, 2, 100)))
	// A download added and one removed meanwhile.
	f.Emit(statetest.Updated(queued(4, 4, 100)), statetest.Removed(3))
	g.release <- struct{}{}
	wg.Wait()
	if got := positions(s, 1, 2, 3, 4); !slices.Equal(got, []int32{2, 1, -1, 4}) {
		t.Fatalf("positions %v after a list sent before the move, want [2 1 -1 4]", got)
	}
	// A list sent after the events is the newer report.
	f.Before = nil
	f.DeleteItem(4)
	s.Reload(ctx)
	if got := positions(s, 1, 2, 3, 4); !slices.Equal(got, []int32{1, 2, 3, -1}) {
		t.Fatalf("positions %v after a list sent later, want downexd's [1 2 3 -1]", got)
	}
	// A newer updated_at still wins whatever the order.
	f.Emit(statetest.Updated(queued(1, 7, 50)))
	if got := positions(s, 1); got[0] != 1 {
		t.Fatalf("an older updated_at won: %v", got)
	}
}

func TestRereadSentBeforeAnEventKeepsTheEvent(t *testing.T) {
	f, s := fake(t, queued(1, 1, 100), queued(2, 2, 100))
	g := holdCalls(f, "get")
	var wg sync.WaitGroup
	wg.Go(func() { s.Reread(ctx, []int64{1}) }) // downexd answers position 1
	<-g.reached
	f.Emit(statetest.Updated(queued(1, 2, 100))) // a later move, learned first
	g.release <- struct{}{}
	wg.Wait()
	if got := positions(s, 1); got[0] != 2 {
		t.Fatalf("a read sent before the event replaced it: position %d, want 2", got[0])
	}
	// A read sent after the event is newer: it wins with the same updated_at.
	f.Before = nil
	s.Reread(ctx, []int64{1})
	if got := positions(s, 1); got[0] != 1 {
		t.Fatalf("a read sent after the event lost: position %d, want 1", got[0])
	}
}

func TestLateSnapshotKeepsANewerRead(t *testing.T) {
	// downexd took the stream's snapshot (position 1) before the move; the window read the
	// moved download again (position 2) before the snapshot arrived.
	f, s := fake(t, queued(1, 2, 100))           // downexd after the move
	f.Emit(statetest.Updated(queued(1, 1, 100))) // the previous stream's last batch
	s.Reread(ctx, []int64{1})
	f.Emit(statetest.Snapshot(queued(1, 1, 100)))
	if got := positions(s, 1); got[0] != 2 {
		t.Fatalf("a late snapshot replaced a newer read: position %d, want 2", got[0])
	}
	// A snapshot after a stream's events (a reconnect) replaces what they reported.
	f.Emit(statetest.Updated(queued(1, 5, 100)))
	f.Emit(statetest.Snapshot(queued(1, 3, 100)))
	if got := positions(s, 1); got[0] != 3 {
		t.Fatalf("a reconnect's snapshot lost: position %d, want 3", got[0])
	}
	// Events after the snapshot are newer.
	f.Emit(statetest.Updated(queued(1, 4, 100)))
	if got := positions(s, 1); got[0] != 4 {
		t.Fatalf("an event after the snapshot lost: position %d, want 4", got[0])
	}
}

func TestAnswerSentBeforeARemovalDoesNotBringItBack(t *testing.T) {
	f, s := fake(t, queued(1, 1, 100))
	g := holdCalls(f, "pause")
	var wg sync.WaitGroup
	wg.Go(func() { s.Pause(ctx, []int64{1}) })
	<-g.reached
	f.Emit(statetest.Removed(1))
	g.release <- struct{}{}
	wg.Wait()
	if _, ok := s.Get(1); ok {
		t.Fatal("the answer of a pause sent before the removal brought the download back")
	}
}

func TestProgressReadSentBeforeAnEventKeepsTheEvent(t *testing.T) {
	f := statetest.NewDaemon(queued(4, 1, 100))
	p, _ := progressStore(t, f, 4)
	p.Start(0)
	g := holdCalls(f, "get")
	var wg sync.WaitGroup
	wg.Go(p.Refresh)
	<-g.reached
	moved := proto.CloneOf(f.Item(4)) // as the start's completion mark left it
	moved.QueuePosition = 2
	f.Emit(statetest.Updated(moved))
	g.release <- struct{}{}
	wg.Wait()
	if got := p.Download().GetQueuePosition(); got != 2 {
		t.Fatalf("a read sent before the event replaced it: position %d, want 2", got)
	}
}
