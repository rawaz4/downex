package statetest

import (
	"context"
	"strconv"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format/formattest"
	"downex/ui/state"
)

// Byte units (testing.ts).
const (
	KiB = formattest.KiB
	MiB = formattest.MiB
)

// Queue is testing.ts makeQueue: the main queue (running) for id 1, else a stopped
// "Queue <id>", changed by edit.
func Queue(id int64, edit func(q *downexv1.Queue)) *downexv1.Queue {
	q := &downexv1.Queue{Id: id, Name: "Main queue", Running: true, IsMain: true, Schedule: &downexv1.Schedule{}}
	if id != state.MainQueueID {
		q.Name, q.Running, q.IsMain = "Queue "+strconv.FormatInt(id, 10), false, false
	}
	if edit != nil {
		edit(q)
	}
	return q
}

// Tools is testing.ts makeTools: both tools ready (Downex's own copies), changed by edit.
func Tools(edit func(t *downexv1.ToolsStatus)) *downexv1.ToolsStatus {
	checked := formattest.Millis(1_792_000_000_000)
	t := &downexv1.ToolsStatus{
		Enabled: true,
		Ready:   true,
		Tools: []*downexv1.ToolInfo{
			{Name: "yt-dlp", State: downexv1.ToolState_TOOL_STATE_READY, Version: "2026.10.10.233141", Path: `C:\Users\you\AppData\Local\Downex\tools\yt-dlp\2026.10.10.233141\yt-dlp.exe`, Managed: true, LatestVersion: "2026.10.10.233141", CheckedAt: checked, Verified: "openpgp+sha256"},
			{Name: "deno", State: downexv1.ToolState_TOOL_STATE_READY, Version: "2.5.4", Path: `C:\Users\you\AppData\Local\Downex\tools\deno\2.5.4\deno.exe`, Managed: true, LatestVersion: "2.5.4", CheckedAt: checked, Verified: "sha256+authenticode"},
		},
	}
	if edit != nil {
		edit(t)
	}
	return t
}

// Connection is testing.ts makeConnection: a worker receiving its first MiB.
func Connection(index int32, edit func(c *downexv1.ConnectionInfo)) *downexv1.ConnectionInfo {
	c := &downexv1.ConnectionInfo{Index: index, State: downexv1.ConnectionState_CONNECTION_STATE_RECEIVING, End: MiB - 1, Fragment: -1}
	if edit != nil {
		edit(c)
	}
	return c
}

// Ms is an epoch-milliseconds timestamp, also for 0 and below (formattest.Millis gives nil
// for those).
func Ms(ms int64) *timestamppb.Timestamp { return timestamppb.New(time.UnixMilli(ms)) }

// Updated is an updated event.
func Updated(d *downexv1.Download) *downexv1.Event {
	return &downexv1.Event{Kind: &downexv1.Event_Updated{Updated: d}}
}

// Snapshot is a snapshot event.
func Snapshot(d *downexv1.Download) *downexv1.Event {
	return &downexv1.Event{Kind: &downexv1.Event_Snapshot{Snapshot: d}}
}

// Removed is a removed event.
func Removed(id int64) *downexv1.Event {
	return &downexv1.Event{Kind: &downexv1.Event_Removed{Removed: id}}
}

// QueueUpdated is a queue_updated event.
func QueueUpdated(q *downexv1.Queue) *downexv1.Event {
	return &downexv1.Event{Kind: &downexv1.Event_QueueUpdated{QueueUpdated: q}}
}

// QueueRemoved is a queue_removed event.
func QueueRemoved(id int64) *downexv1.Event {
	return &downexv1.Event{Kind: &downexv1.Event_QueueRemoved{QueueRemoved: id}}
}

// PostAction is a post_action event; deadline in epoch ms (0: none).
func PostAction(queueID int64, action downexv1.PostAction, deadline int64, cancelled bool) *downexv1.Event {
	p := &downexv1.PostActionPending{QueueId: queueID, Action: action, Cancelled: cancelled}
	if deadline > 0 {
		p.Deadline = Ms(deadline)
	}
	return &downexv1.Event{Kind: &downexv1.Event_PostAction{PostAction: p}}
}

// ToolsEvent is a tools event.
func ToolsEvent(t *downexv1.ToolsStatus) *downexv1.Event {
	return &downexv1.Event{Kind: &downexv1.Event_Tools{Tools: t}}
}

// Stream is a host.WatchStream over an Events hub: a first batch, then every batch
// published, until its context ends, End or Close.
type Stream struct {
	mu     sync.Mutex
	cond   *sync.Cond
	queue  []*downexv1.WatchResponse
	msg    *downexv1.WatchResponse
	closed bool
	err    error
	cancel func()
}

// NewStream subscribes to events; first (when not empty) is the first batch.
func NewStream(ctx context.Context, events state.Events, first []*downexv1.Event) *Stream {
	s := &Stream{}
	s.cond = sync.NewCond(&s.mu)
	if len(first) > 0 {
		s.queue = append(s.queue, &downexv1.WatchResponse{Events: first})
	}
	unsub := events.Subscribe(func(b state.Batch) { s.push(&downexv1.WatchResponse{Events: b}) })
	stop := context.AfterFunc(ctx, func() { s.End(ctx.Err()) })
	s.cancel = func() { unsub(); stop() }
	return s
}

func (s *Stream) push(m *downexv1.WatchResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.queue = append(s.queue, m)
		s.cond.Broadcast()
	}
}

// End ends the stream with err (a daemon that went away).
func (s *Stream) End(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed, s.err = true, err
		s.cond.Broadcast()
	}
}

// Receive implements host.WatchStream.
func (s *Stream) Receive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.queue) == 0 && !s.closed {
		s.cond.Wait()
	}
	if len(s.queue) == 0 {
		return false
	}
	s.msg = s.queue[0]
	s.queue = s.queue[1:]
	return true
}

// Msg implements host.WatchStream.
func (s *Stream) Msg() *downexv1.WatchResponse { s.mu.Lock(); defer s.mu.Unlock(); return s.msg }

// Err implements host.WatchStream.
func (s *Stream) Err() error { s.mu.Lock(); defer s.mu.Unlock(); return s.err }

// Close implements host.WatchStream.
func (s *Stream) Close() error {
	s.cancel()
	s.End(nil)
	return nil
}
