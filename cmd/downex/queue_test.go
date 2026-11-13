package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	downexv1 "downex/internal/rpc/downex/v1"
)

func TestQueueCreateUpdateRemove(t *testing.T) {
	f, h := newFakeDaemon(t)
	ctx := context.Background()

	r := h.run(ctx, "queue", "ls")
	if r.code != exitOK || !strings.Contains(r.stdout, "Main (main)") || !strings.Contains(r.stdout, "running") {
		t.Fatalf("queue ls: %v", r)
	}
	if r2 := h.run(ctx, "queue"); r2.stdout != r.stdout {
		t.Errorf("queue without a subcommand is not ls: %v", r2)
	}

	r = h.run(ctx, "queue", "create", "Night", "--max", "2", "--limit", "1M", "--post", "shutdown")
	if r.code != exitOK || r.stdout != "2\n" || !strings.Contains(r.stderr, "Created queue 2 (Night): stopped, up to 2 at once") ||
		!strings.Contains(r.stderr, "Start it with: downex queue start 2") {
		t.Fatalf("queue create: %v", r)
	}
	creates := queueCallsOf[*downexv1.CreateQueueRequest](f.m6())
	if len(creates) != 1 {
		t.Fatalf("%d CreateQueue calls", len(creates))
	}
	if q := creates[0].GetQueue(); q.GetName() != "Night" || q.GetMaxConcurrent() != 2 || q.GetSpeedLimit() != 1_000_000 ||
		q.GetPostAction() != downexv1.PostAction_POST_ACTION_SHUTDOWN {
		t.Errorf("CreateQueue %v", q)
	}

	r = h.run(ctx, "queue", "ls", "--json")
	var views []queueView
	if err := json.Unmarshal([]byte(r.stdout), &views); err != nil || len(views) != 2 || views[1].Name != "Night" ||
		views[1].MaxConcurrent != 2 || views[1].PostAction != "shutdown" || views[1].Running || !views[0].Main ||
		!slices.Equal(views[0].DownloadIDs, []int64{7}) || views[1].DownloadIDs == nil {
		t.Fatalf("queue ls --json: %+v %v\n%s", views, err, r.stdout)
	}

	// A schedule set before is kept by update, which replaces the queue as a whole.
	f.do(func() {
		q, _ := f.queueByID(2)
		q.Schedule = &downexv1.Schedule{Enabled: true, StartMinute: 120, Weekdays: allWeekdays}
	})
	r = h.run(ctx, "queue", "update", "night", "--name", "Nightly", "--post", "none")
	if r.code != exitOK || !strings.Contains(r.stdout, "Updated queue 2 (Nightly)") {
		t.Fatalf("queue update: %v", r)
	}
	ups := queueCallsOf[*downexv1.UpdateQueueRequest](f.m6())
	if q := ups[0].GetQueue(); q.GetId() != 2 || q.GetName() != "Nightly" || q.GetMaxConcurrent() != 2 ||
		q.GetSpeedLimit() != 1_000_000 || q.GetPostAction() != downexv1.PostAction_POST_ACTION_UNSPECIFIED ||
		q.GetSchedule().GetStartMinute() != 120 || !q.GetSchedule().GetEnabled() {
		t.Errorf("UpdateQueue %v", q)
	}

	if r = h.run(ctx, "queue", "rm", "main"); r.code != exitError || !strings.Contains(r.stderr, "main queue, which cannot be deleted") {
		t.Errorf("queue rm main: %v", r)
	}
	f.do(func() { q, _ := f.queueByID(2); q.DownloadIds = []int64{3, 4} })
	r = h.run(ctx, "queue", "rm", "Nightly")
	if r.code != exitOK || !strings.Contains(r.stdout, "Removed queue 2 (Nightly); its 2 downloads moved to the main queue.") {
		t.Fatalf("queue rm: %v", r)
	}
	if dels := queueCallsOf[*downexv1.DeleteQueueRequest](f.m6()); len(dels) != 1 || dels[0].GetId() != 2 {
		t.Errorf("DeleteQueue calls %v", dels)
	}

	for _, tc := range []struct {
		args []string
		out  string
	}{
		{[]string{"queue", "create"}, "create expects exactly one queue name"},
		{[]string{"queue", "create", "  "}, "must not be empty"},
		{[]string{"queue", "create", strings.Repeat("q", 65)}, "at most 64 characters"},
		{[]string{"queue", "create", "x\x1b[2J"}, "control characters"},
		{[]string{"queue", "create", "x", "--max", "17"}, "--max must be between 1 and 16"},
		{[]string{"queue", "create", "x", "--limit", "fast"}, "invalid rate"},
		{[]string{"queue", "create", "x", "--post", "reboot"}, "want none, quit, sleep, hibernate or shutdown"},
		{[]string{"queue", "update", "1"}, "update needs --name, --max, --limit or --post"},
		{[]string{"queue", "update", "1", "--name", ""}, "must not be empty"},
		{[]string{"queue", "frob"}, `unknown subcommand "frob"`},
		{[]string{"queue", "rm"}, "expects exactly one queue"},
	} {
		if r := h.run(ctx, tc.args...); r.code != exitError || !strings.Contains(r.stderr, tc.out) {
			t.Errorf("downex %q: %v\nwant %q", tc.args, r, tc.out)
		}
	}
	if n := len(queueCallsOf[*downexv1.ListQueuesRequest](f.m6())); n != 6 {
		t.Errorf("%d ListQueues calls, want 6: invalid arguments reached the daemon", n)
	}
	if r := h.run(ctx, "queue", "rm", "nope"); r.code != exitError || !strings.Contains(r.stderr, `no queue "nope" (see "downex queue ls")`) {
		t.Errorf("unknown queue: %v", r)
	}
	f.do(func() {
		f.queues = append(f.queues, &downexv1.Queue{Id: 5, Name: "Dup"}, &downexv1.Queue{Id: 6, Name: "dup"})
	})
	if r := h.run(ctx, "queue", "start", "DUP"); r.code != exitError || !strings.Contains(r.stderr, "ambiguous") {
		t.Errorf("ambiguous queue name: %v", r)
	}
	if r := h.run(ctx, "queue", "start", "dup"); r.code != exitOK || !strings.Contains(r.stdout, "Started queue 6") {
		t.Errorf("exact queue name: %v", r)
	}
	// A daemon refusal is reported with its message.
	f.do(func() { f.queues = append(f.queues, &downexv1.Queue{Id: 9, Name: "Night"}) })
	if r := h.run(ctx, "queue", "create", "Night"); r.code != exitError || !strings.Contains(r.stderr, `queue create: a queue named "Night" exists`) {
		t.Errorf("duplicate queue: %v", r)
	}
}

func TestFindQueue(t *testing.T) {
	qs := []*downexv1.Queue{{Id: 1, Name: "Main queue", IsMain: true}, {Id: 2, Name: "3"}, {Id: 3, Name: "Night"}}
	for _, tc := range []struct {
		arg  string
		want int64
	}{
		{"1", 1}, {"3", 3}, {"Night", 3}, {"night", 3}, {" 2 ", 2}, {"main", 1}, {"MAIN QUEUE", 1},
	} {
		q, err := findQueue(qs, tc.arg)
		if err != nil || q.GetId() != tc.want {
			t.Errorf("findQueue(%q) = %v, %v; want queue %d", tc.arg, q, err, tc.want)
		}
	}
	for _, arg := range []string{"", "4", "Day"} {
		if q, err := findQueue(qs, arg); err == nil {
			t.Errorf("findQueue(%q) = %v, want an error", arg, q)
		}
	}
}

func TestQueueStartStopMove(t *testing.T) {
	f, h := newFakeDaemon(t)
	ctx := context.Background()
	f.do(func() {
		for _, id := range []int64{3, 4, 5, 7} {
			f.downloads[id] = &downexv1.Download{Id: id, State: downexv1.DownloadState_DOWNLOAD_STATE_QUEUED}
		}
		f.queues = append(f.queues, &downexv1.Queue{Id: 2, Name: "Night", DownloadIds: []int64{5}})
	})

	if r := h.run(ctx, "queue", "start", "Night"); r.code != exitOK || r.stdout != "Started queue 2 (Night) with 1 download.\n" {
		t.Errorf("queue start: %v", r)
	}
	if q := f.queue(2); !q.GetRunning() {
		t.Error("queue not started")
	}
	if r := h.run(ctx, "queue", "stop", "2"); r.code != exitOK || !strings.Contains(r.stdout, "Stopped queue 2 (Night); its running downloads are paused.") {
		t.Errorf("queue stop: %v", r)
	}
	if r := h.run(ctx, "queue", "stop", "1"); r.code != exitOK || !strings.Contains(r.stdout, "Run it again with: downex queue start 1") {
		t.Errorf("queue stop main: %v", r)
	}

	r := h.run(ctx, "queue", "move", "3", "4", "3", "--to", "night", "--pos", "1")
	if r.code != exitOK || r.stdout != "Moved downloads 3, 4 to queue 2 (Night) at position 1.\n" {
		t.Fatalf("queue move: %v", r)
	}
	moves := queueCallsOf[*downexv1.MoveDownloadsRequest](f.m6())
	if m := moves[0]; !slices.Equal(m.GetIds(), []int64{3, 4}) || m.GetQueueId() != 2 || m.GetPosition() != 0 {
		t.Errorf("MoveDownloads %v", m)
	}
	if q := f.queue(2); !slices.Equal(q.GetDownloadIds(), []int64{3, 4, 5}) {
		t.Errorf("queue order %v", q.GetDownloadIds())
	}
	// Without --pos at the end (-1: position 0 would be the front).
	if r := h.run(ctx, "queue", "move", "7", "--to", "Night"); r.code != exitOK || r.stdout != "Moved download 7 to queue 2 (Night) at the end.\n" {
		t.Errorf("queue move to the end: %v", r)
	}
	moves = queueCallsOf[*downexv1.MoveDownloadsRequest](f.m6())
	if m := moves[1]; m.GetPosition() != -1 {
		t.Errorf("MoveDownloads without --pos %v", m)
	}
	if r := h.run(ctx, "queue", "move", "99", "--to", "Night"); r.code != exitError || !strings.Contains(r.stderr, "download 99 not found") {
		t.Errorf("move of an unknown download: %v", r)
	}

	for _, tc := range []struct {
		args []string
		out  string
	}{
		{[]string{"queue", "move", "--to", "2"}, "move expects one or more download ids"},
		{[]string{"queue", "move", "3"}, "move needs --to <queue>"},
		{[]string{"queue", "move", "3", "--to", "2", "--pos", "0"}, "--pos must be 1 or more"},
		{[]string{"queue", "move", "x", "--to", "2"}, `invalid download id "x"`},
		{[]string{"queue", "start"}, "expects exactly one queue"},
	} {
		if r := h.run(ctx, tc.args...); r.code != exitError || !strings.Contains(r.stderr, tc.out) {
			t.Errorf("downex %q: %v\nwant %q", tc.args, r, tc.out)
		}
	}
}

func TestQueueSchedule(t *testing.T) {
	f, h := newFakeDaemon(t)
	ctx := context.Background()
	f.do(func() {
		f.queues = append(f.queues, &downexv1.Queue{Id: 2, Name: "Night", MaxConcurrent: 3,
			PostAction: downexv1.PostAction_POST_ACTION_SLEEP})
	})
	schedule := func() *downexv1.Schedule { return f.queue(2).GetSchedule() }

	if r := h.run(ctx, "queue", "schedule", "Night"); r.code != exitOK || r.stdout != "Queue 2 (Night): schedule off.\n" {
		t.Errorf("show schedule: %v", r)
	}
	if r := h.run(ctx, "queue", "schedule", "Night", "--stop", "06:00"); r.code != exitError || !strings.Contains(r.stderr, "give --start HH:MM") {
		t.Errorf("stop without a start: %v", r)
	}

	r := h.run(ctx, "queue", "schedule", "Night", "--start", "22:30", "--stop", "06:00", "--days", "mon-fri", "--repeat", "30", "--wake")
	if r.code != exitOK || r.stdout != "Queue 2 (Night): schedule weekdays 22:30-06:00, repeat every 30 min, wakes the computer.\n" {
		t.Fatalf("set schedule: %v", r)
	}
	want := &downexv1.Schedule{Enabled: true, StartMinute: 22*60 + 30, StopEnabled: true, StopMinute: 360, Weekdays: 0x1f,
		RepeatMinutes: 30, WakeComputer: true}
	if got := schedule(); !proto.Equal(got, want) {
		t.Errorf("schedule %v, want %v", got, want)
	}
	if q := f.queue(2); q.GetMaxConcurrent() != 3 || q.GetPostAction() != downexv1.PostAction_POST_ACTION_SLEEP || q.GetName() != "Night" {
		t.Errorf("schedule changed other fields: %v", q)
	}

	// Flags not given keep their values; --once replaces the days.
	if r = h.run(ctx, "queue", "schedule", "2", "--once", "2026-12-24", "--wake=false"); r.code != exitOK ||
		!strings.Contains(r.stdout, "schedule once 2026-12-24 22:30-06:00, repeat every 30 min.") {
		t.Errorf("once: %v", r)
	}
	if s := schedule(); s.GetWeekdays() != 0 || s.GetOnceDate() != "2026-12-24" || s.GetWakeComputer() || s.GetStartMinute() != 1350 {
		t.Errorf("once schedule %v", s)
	}
	if r = h.run(ctx, "queue", "schedule", "2", "--days", "sat,sun", "--stop", "none", "--repeat", "0"); r.code != exitOK ||
		!strings.Contains(r.stdout, "schedule weekend 22:30.") {
		t.Errorf("weekend: %v", r)
	}
	if s := schedule(); s.GetOnceDate() != "" || s.GetWeekdays() != 0x60 || s.GetStopEnabled() {
		t.Errorf("weekend schedule %v", s)
	}
	if r = h.run(ctx, "queue", "schedule", "2", "--off", "--json"); r.code != exitOK {
		t.Fatalf("off: %v", r)
	}
	var v queueView
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil || v.Schedule == nil || v.Schedule.Enabled || v.Schedule.Start != "22:30" ||
		!slices.Equal(v.Schedule.Days, []string{"sat", "sun"}) {
		t.Errorf("off --json: %+v %v\n%s", v, err, r.stdout)
	}
	// --off=false turns it on again with the stored times.
	if r = h.run(ctx, "queue", "schedule", "2", "--off=false"); r.code != exitOK || !strings.Contains(r.stdout, "schedule weekend 22:30.") {
		t.Errorf("on again: %v", r)
	}
	// A new schedule without --days runs daily.
	f.do(func() { q, _ := f.queueByID(2); q.Schedule = nil })
	if r = h.run(ctx, "queue", "schedule", "2", "--start", "1:05"); r.code != exitOK || !strings.Contains(r.stdout, "schedule daily 01:05.") {
		t.Errorf("daily: %v", r)
	}

	n := len(queueCallsOf[*downexv1.UpdateQueueRequest](f.m6()))
	for _, tc := range []struct {
		args []string
		out  string
	}{
		{[]string{"--start", "24:00"}, "want a time HH:MM"},
		{[]string{"--start", "7"}, "want a time HH:MM"},
		{[]string{"--start", "07:5"}, "want a time HH:MM"},
		{[]string{"--start", "01:00", "--stop", "25:00"}, "--stop: want a time"},
		{[]string{"--start", "01:00", "--days", "mon,funday"}, `unknown day "funday"`},
		{[]string{"--start", "01:00", "--days", ","}, "no days given"},
		{[]string{"--start", "01:00", "--days", "mon", "--once", "2026-01-01"}, "cannot be combined"},
		{[]string{"--start", "01:00", "--once", "2026-13-01"}, "want a date YYYY-MM-DD"},
		{[]string{"--start", "01:00", "--repeat", "2000"}, "--repeat must be between 0 and 1440"},
		{[]string{"--off", "--start", "01:00"}, "--off cannot be combined"},
		{[]string{"--start", "06:00", "--stop", "06:00"}, "stop time must differ"},
	} {
		args := append([]string{"queue", "schedule", "2"}, tc.args...)
		if r := h.run(ctx, args...); r.code != exitError || !strings.Contains(r.stderr, tc.out) {
			t.Errorf("downex %q: %v\nwant %q", args, r, tc.out)
		}
	}
	if got := len(queueCallsOf[*downexv1.UpdateQueueRequest](f.m6())); got != n {
		t.Errorf("invalid schedules were sent: %d UpdateQueue calls, want %d", got, n)
	}
}

func TestParseWeekdays(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int32
		text string
	}{
		{"mon", 1, "mon"}, {"Monday,WED", 0b101, "mon,wed"}, {"mon-fri", 0x1f, "weekdays"},
		{"fri-mon", 0b1110001, "mon,fri,sat,sun"}, {"weekend", 0x60, "weekend"}, {"daily", 0x7f, "daily"},
		{"sun-sat", 0x7f, "daily"}, {"tue-tue", 0b10, "tue"}, {" thu , sat ", 0b101000, "thu,sat"},
	} {
		got, err := parseWeekdays(tc.in)
		if err != nil || got != tc.want || describeDays(got) != tc.text {
			t.Errorf("parseWeekdays(%q) = %07b (%s), %v; want %07b (%s)", tc.in, got, describeDays(got), err, tc.want, tc.text)
		}
	}
	for _, in := range []string{"", "mo", "mon-", "-fri", "xyz", "mon-xyz"} {
		if got, err := parseWeekdays(in); err == nil {
			t.Errorf("parseWeekdays(%q) = %07b, want an error", in, got)
		}
	}
}

func TestQueueFlagOfAddMediaConfirm(t *testing.T) {
	f, h := newFakeDaemon(t)
	ctx := context.Background()
	f.do(func() {
		f.queues = append(f.queues, &downexv1.Queue{Id: 2, Name: "Night"})
		f.downloads[5] = &downexv1.Download{Id: 5, State: downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION, Dir: "/d", Filename: "f"}
	})

	r := h.run(ctx, "add", "https://example.test/a", "--queue", "night")
	if r.code != exitOK || r.stdout != "7\n" || !strings.Contains(r.stderr, "Queue 2 (Night) is not running: the download starts when it runs (downex queue start 2)") {
		t.Fatalf("add --queue: %v", r)
	}
	if a := f.rec().adds[0]; a.GetQueueId() != 2 {
		t.Errorf("AddRequest.queue_id = %d", a.GetQueueId())
	}
	lists := len(queueCallsOf[*downexv1.ListQueuesRequest](f.m6()))
	if r = h.run(ctx, "add", "https://example.test/b"); r.code != exitOK || strings.Contains(r.stderr, "not running") {
		t.Errorf("add: %v", r)
	}
	if a := f.rec().adds[1]; a.GetQueueId() != 0 || len(queueCallsOf[*downexv1.ListQueuesRequest](f.m6())) != lists {
		t.Errorf("add without --queue: queue_id %d, queues listed", a.GetQueueId())
	}
	// Paused downloads do not wait for the queue: no hint.
	if r = h.run(ctx, "add", "https://example.test/c", "--queue", "2", "--paused"); r.code != exitOK || strings.Contains(r.stderr, "not running") {
		t.Errorf("add --paused --queue: %v", r)
	}
	if r = h.run(ctx, "add", "https://example.test/d", "--queue", "Day"); r.code != exitError || !strings.Contains(r.stderr, `no queue "Day"`) {
		t.Errorf("add --queue unknown: %v", r)
	}
	if n := len(f.rec().adds); n != 3 {
		t.Errorf("%d adds, want 3 (none for an unknown queue)", n)
	}

	if r = h.run(ctx, "media", "https://example.test/m.m3u8", "--queue", "Night"); r.code != exitOK {
		t.Fatalf("media --queue: %v", r)
	}
	f.do(func() {
		if got := f.mediaAdds[0].GetQueueId(); got != 2 {
			t.Errorf("AddMediaRequest.queue_id = %d", got)
		}
	})
	if r = h.run(ctx, "media", "https://example.test/m.m3u8", "--list", "--queue", "2"); r.code != exitError || !strings.Contains(r.stderr, "--list cannot be combined with --queue") {
		t.Errorf("media --list --queue: %v", r)
	}

	if r = h.run(ctx, "confirm", "5", "--queue", "Night", "--later"); r.code != exitOK {
		t.Fatalf("confirm --queue: %v", r)
	}
	if c := f.rec().confirms[0]; c.GetQueueId() != 2 || c.GetStart() {
		t.Errorf("ConfirmRequest %v", c)
	}

	// get runs without downexd: --queue is refused with the command that does it.
	r = runCLI(ctx, "get", "https://example.test/g", "--queue", "Night")
	if r.code != exitError || !strings.Contains(r.stderr, "downex add --queue Night --wait <url>") {
		t.Errorf("get --queue: %v", r)
	}
}

func TestCancelPost(t *testing.T) {
	f, h := newFakeDaemon(t)
	ctx := context.Background()
	f.do(func() { f.queues = append(f.queues, &downexv1.Queue{Id: 2, Name: "Night"}) })

	if r := h.run(ctx, "cancel-post", "Night"); r.code != exitOK || r.stdout != "Cancelled the post-queue action of queue 2 (Night).\n" {
		t.Errorf("cancel-post: %v", r)
	}
	f.do(func() { f.cancelErr = connect.NewError(connect.CodeFailedPrecondition, errors.New("nothing pending")) })
	if r := h.run(ctx, "cancel-post", "main"); r.code != exitError ||
		!strings.Contains(r.stderr, "no post-queue action of queue 1 (Main) is counting down (nothing pending)") {
		t.Errorf("cancel-post without a countdown: %v", r)
	}
	if got := f.m6().cancelPosts; !slices.Equal(got, []int64{2, 1}) {
		t.Errorf("CancelPostAction calls %v", got)
	}
	if r := h.run(ctx, "cancel-post"); r.code != exitError || !strings.Contains(r.stderr, "expects exactly one queue") {
		t.Errorf("cancel-post without a queue: %v", r)
	}
}

func TestLsShowsQueueAndClipboardSource(t *testing.T) {
	f, h := newFakeDaemon(t)
	f.do(func() {
		f.downloads[3] = &downexv1.Download{Id: 3, Url: "https://example.test/c", QueueId: 2,
			Source: downexv1.AddSource_ADD_SOURCE_CLIPBOARD, State: downexv1.DownloadState_DOWNLOAD_STATE_QUEUED}
	})
	r := h.run(context.Background(), "ls", "--json")
	var views []downloadView
	if err := json.Unmarshal([]byte(r.stdout), &views); err != nil || len(views) != 1 || views[0].QueueID != 2 || views[0].Source != "clipboard" {
		t.Errorf("ls --json: %+v %v\n%s", views, err, r.stdout)
	}
}

func TestM6Help(t *testing.T) {
	_, h := newFakeDaemon(t)
	ctx := context.Background()
	for _, tc := range []struct {
		args []string
		out  string
	}{
		{[]string{"help"}, "downex queue ls|create|update|rm|start|stop|move|schedule"},
		{[]string{"help", "queue"}, "downex queue schedule <queue>"},
		{[]string{"queue", "-h"}, "Run 'downex queue <subcommand> -h'"},
		{[]string{"queue", "create", "-h"}, "Usage: downex queue create <name>"},
		{[]string{"queue", "schedule", "-h"}, "-wake"},
		{[]string{"help", "proxy"}, "downex proxy set pac <url>"},
		{[]string{"proxy", "set", "-h"}, "-socks"},
		{[]string{"help", "login"}, "downex login add <url-pattern> --user name"},
		{[]string{"help", "update"}, "downex update install"},
		{[]string{"help", "cancel-post"}, "Usage: downex cancel-post <queue>"},
		{[]string{"help", "settings"}, "-av-command"},
		{[]string{"help", "add"}, "-queue"},
	} {
		r := h.run(ctx, tc.args...)
		if r.code != exitOK || !strings.Contains(r.stdout+r.stderr, tc.out) {
			t.Errorf("downex %q: %v\nwant exit 0 and %q", tc.args, r, tc.out)
		}
	}
	for _, args := range [][]string{{"update"}, {"update", "frob"}, {"proxy", "frob"}, {"login", "frob"}} {
		if r := h.run(ctx, args...); r.code != exitError || !strings.Contains(r.stderr, "Run 'downex help "+args[0]+"'") {
			t.Errorf("downex %q: %v", args, r)
		}
	}
}
