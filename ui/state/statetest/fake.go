package statetest

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format/formattest"
	"downex/ui/host"
	"downex/ui/state"
)

// Refused is the message of every call to a stopped daemon (testing.ts REFUSED without its
// code).
const Refused = "dial tcp 127.0.0.1:47820: connect: connection refused"

// Err is a Connect error with code and message.
func Err(code connect.Code, format string, args ...any) error {
	return connect.NewError(code, fmt.Errorf(format, args...))
}

// Daemon is a scriptable host.Daemon over an in-memory list (testing.ts fakeApi): Down
// makes every call fail like a stopped daemon. It is safe for concurrent use.
type Daemon struct {
	// Events is where Emit publishes (hand it to the stores).
	Events state.Hub

	mu       sync.Mutex
	down     bool
	settings *downexv1.Settings
	items    map[int64]*downexv1.Download
	order    []int64
	calls    []string
	args     map[string][]any
	nextID   int64
	queues   map[int64]*downexv1.Queue
	nextQ    int64
	tools    *downexv1.ToolsStatus
	gone     map[int64]bool
	status   *downexv1.StatusResponse
	install  *downexv1.UpdateInfo // InstallUpdate's answer (SetInstallUpdate)

	// Before runs after a call is recorded and before it is answered; an error it returns
	// is the call's answer (a test's replacement of one api method). It may block.
	Before func(name string, args ...any) error
}

// NewDaemon is fakeApi(initial): its settings are the defaults with /home/you/Downloads,
// the main queue holds the initial downloads.
func NewDaemon(initial ...*downexv1.Download) *Daemon {
	s := state.DefaultSettings()
	s.DefaultDir = "/home/you/Downloads"
	d := &Daemon{
		settings: s,
		items:    map[int64]*downexv1.Download{},
		args:     map[string][]any{},
		queues:   map[int64]*downexv1.Queue{},
		nextQ:    2,
		gone:     map[int64]bool{},
		status: &downexv1.StatusResponse{
			Version: "1.0.0-test", Pid: 1, StartedAt: formattest.Millis(1),
			Endpoint: "127.0.0.1:47820", DataDir: "/home/you/.config/Downex", LogDir: "/home/you/.local/state/Downex",
			ToolsDir: "/home/you/.local/share/Downex/tools",
		},
	}
	var ids []int64
	for _, x := range initial {
		d.items[x.GetId()] = x
		d.order = append(d.order, x.GetId())
		ids = append(ids, x.GetId())
		d.nextID = max(d.nextID, x.GetId())
	}
	d.nextID++
	d.queues[state.MainQueueID] = Queue(state.MainQueueID, func(q *downexv1.Queue) { q.DownloadIds = ids })
	return d
}

// SetDown makes every call fail (true) or answer (false).
func (d *Daemon) SetDown(down bool) { d.mu.Lock(); d.down = down; d.mu.Unlock() }

// Calls are the calls made, by name, in order.
func (d *Daemon) Calls() []string { d.mu.Lock(); defer d.mu.Unlock(); return slices.Clone(d.calls) }

// Count is how often name was called.
func (d *Daemon) Count(name string) int {
	n := 0
	for _, c := range d.Calls() {
		if c == name {
			n++
		}
	}
	return n
}

// ResetCalls forgets the calls made.
func (d *Daemon) ResetCalls() { d.mu.Lock(); d.calls = nil; d.mu.Unlock() }

// Args are the arguments of the last call of name.
func (d *Daemon) Args(name string) []any { d.mu.Lock(); defer d.mu.Unlock(); return d.args[name] }

// Item is a download as the fake holds it.
func (d *Daemon) Item(id int64) *downexv1.Download {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.items[id]
}

// DeleteItem forgets a download (the daemon no longer knows it).
func (d *Daemon) DeleteItem(id int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.items, id)
	d.order = slices.DeleteFunc(d.order, func(x int64) bool { return x == id })
}

// SetQueue stores a queue.
func (d *Daemon) SetQueue(q *downexv1.Queue) { d.mu.Lock(); d.queues[q.GetId()] = q; d.mu.Unlock() }

// DeleteQueueItem forgets a queue (removed while the stream was down).
func (d *Daemon) DeleteQueueItem(id int64) { d.mu.Lock(); delete(d.queues, id); d.mu.Unlock() }

// SetTools sets what GetTools answers (nil: a downexd without site media, unimplemented).
func (d *Daemon) SetTools(t *downexv1.ToolsStatus) { d.mu.Lock(); d.tools = t; d.mu.Unlock() }

// SetGone marks a download's file as gone (keep both then keeps the name).
func (d *Daemon) SetGone(id int64) { d.mu.Lock(); d.gone[id] = true; d.mu.Unlock() }

// Settings are the stored settings.
func (d *Daemon) Settings() *downexv1.Settings { d.mu.Lock(); defer d.mu.Unlock(); return d.settings }

// SetSettings replaces them.
func (d *Daemon) SetSettings(s *downexv1.Settings) { d.mu.Lock(); d.settings = s; d.mu.Unlock() }

// Emit publishes a batch to the subscribers of Events.
func (d *Daemon) Emit(events ...*downexv1.Event) { d.Events.Publish(events) }

func (d *Daemon) gate(name string, args ...any) error {
	d.mu.Lock()
	d.calls = append(d.calls, name)
	d.args[name] = args
	before := d.Before
	down := d.down
	d.mu.Unlock()
	if before != nil {
		if err := before(name, args...); err != nil {
			return err
		}
	}
	if down {
		return connect.NewError(connect.CodeUnavailable, errors.New(Refused))
	}
	return nil
}

func notFound(id int64) error { return Err(connect.CodeNotFound, "download %d not found", id) }

// updateLocked replaces a download with edit applied and updated_at moved on by 1 ms.
func (d *Daemon) updateLocked(id int64, edit func(*downexv1.Download)) (*downexv1.Download, error) {
	cur := d.items[id]
	if cur == nil {
		return nil, Err(connect.CodeNotFound, "no such download")
	}
	next := proto.CloneOf(cur)
	edit(next)
	next.UpdatedAt = timestamppb.New(cur.GetUpdatedAt().AsTime().Add(1e6))
	d.items[id] = next
	return next, nil
}

func (d *Daemon) update(id int64, edit func(*downexv1.Download)) (*downexv1.Download, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.updateLocked(id, edit)
}

// ---- DownloadService -----------------------------------------------------------------

func (d *Daemon) List(context.Context) (*downexv1.ListResponse, error) {
	if err := d.gate("list"); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	out := &downexv1.ListResponse{}
	for _, id := range d.order {
		if x := d.items[id]; x != nil {
			out.Downloads = append(out.Downloads, x)
		}
	}
	return out, nil
}

func (d *Daemon) Get(_ context.Context, id int64) (*downexv1.Download, error) {
	if err := d.gate("get", id); err != nil {
		return nil, err
	}
	if x := d.Item(id); x != nil {
		return x, nil
	}
	return nil, notFound(id)
}

func (d *Daemon) Add(_ context.Context, req *downexv1.AddRequest) (*downexv1.AddResponse, error) {
	if err := d.gate("add", req); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	id := d.nextID
	d.nextID++
	x := formattest.Download(id, func(x *downexv1.Download) {
		x.Url = req.GetUrl()
		x.Filename = req.GetFilename()
		if req.GetStartPaused() {
			x.State = state.StatePaused
		}
		x.QueueId = state.MainQueueID
		if req.GetQueueId() != 0 {
			x.QueueId = req.GetQueueId()
		}
	})
	d.items[id] = x
	d.order = append(d.order, id)
	return &downexv1.AddResponse{Download: x}, nil
}

func (d *Daemon) Pause(_ context.Context, req *downexv1.PauseRequest) (*downexv1.PauseResponse, error) {
	if err := d.gate("pause", req.GetId()); err != nil {
		return nil, err
	}
	x, err := d.update(req.GetId(), func(x *downexv1.Download) { x.State, x.SpeedBps = state.StatePaused, 0 })
	if err != nil {
		return nil, err
	}
	return &downexv1.PauseResponse{Download: x}, nil
}

func (d *Daemon) Resume(_ context.Context, req *downexv1.ResumeRequest) (*downexv1.ResumeResponse, error) {
	if err := d.gate("resume", req.GetId(), req); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	cur := d.items[req.GetId()]
	if req.GetExistingFile() != 0 && !req.GetRestart() {
		return nil, Err(connect.CodeInvalidArgument, "existing_file is only for a restart")
	}
	if cur.GetState() == state.StateCompleted {
		// As downexd: a completed download is downloaded again only with restart; keep both
		// saves the new copy under a free name unless the file is gone.
		if !req.GetRestart() {
			return nil, Err(connect.CodeFailedPrecondition, "download %d is already completed", req.GetId())
		}
		keep := req.GetExistingFile() != state.Replace && !d.gone[req.GetId()]
		delete(d.gone, req.GetId())
		x, err := d.updateLocked(req.GetId(), func(x *downexv1.Download) {
			x.State, x.Received, x.CompletedAt = state.StateQueued, 0, nil
			if keep {
				x.Filename = state.NumberedName(x.GetFilename(), 1)
			}
		})
		if err != nil {
			return nil, err
		}
		return &downexv1.ResumeResponse{Download: x}, nil
	}
	x, err := d.updateLocked(req.GetId(), func(x *downexv1.Download) { x.State = state.StateQueued })
	if err != nil {
		return nil, err
	}
	return &downexv1.ResumeResponse{Download: x}, nil
}

func (d *Daemon) Remove(_ context.Context, req *downexv1.RemoveRequest) (*downexv1.RemoveResponse, error) {
	if err := d.gate("remove", req.GetId(), req.GetDeleteFile()); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.items[req.GetId()] == nil {
		return nil, Err(connect.CodeNotFound, "no such download")
	}
	delete(d.items, req.GetId())
	d.order = slices.DeleteFunc(d.order, func(x int64) bool { return x == req.GetId() })
	return &downexv1.RemoveResponse{}, nil
}

func (d *Daemon) Confirm(_ context.Context, req *downexv1.ConfirmRequest) (*downexv1.ConfirmResponse, error) {
	if err := d.gate("confirm", req.GetId(), req); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	cur := d.items[req.GetId()]
	if cur == nil {
		return nil, Err(connect.CodeNotFound, "no such download")
	}
	if cur.GetState() != state.StateAwaiting {
		return nil, Err(connect.CodeFailedPrecondition, "download %d is not awaiting confirmation", req.GetId())
	}
	if req.GetQueueId() != 0 {
		if _, err := d.moveLocked([]int64{req.GetId()}, req.GetQueueId(), -1); err != nil {
			return nil, err
		}
	}
	x, err := d.updateLocked(req.GetId(), func(x *downexv1.Download) {
		x.State = state.StatePaused
		if req.GetStart() || req.GetQueueId() != 0 {
			x.State = state.StateQueued
		}
		if req.GetDir() != "" {
			x.Dir = req.GetDir()
		}
		if req.GetFilename() != "" {
			x.Filename = req.GetFilename()
		}
	})
	if err != nil {
		return nil, err
	}
	return &downexv1.ConfirmResponse{Download: x}, nil
}

var drm = regexp.MustCompile(`drm`)

func (d *Daemon) AddMedia(_ context.Context, req *downexv1.AddMediaRequest) (*downexv1.AddMediaResponse, error) {
	if err := d.gate("addMedia", req); err != nil {
		return nil, err
	}
	if drm.MatchString(req.GetManifestUrl()) {
		return nil, Err(connect.CodeInvalidArgument, "encrypted: the stream is DRM-protected")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	id := d.nextID
	d.nextID++
	x := formattest.MediaDownload(id, func(x *downexv1.Download) {
		x.Url = req.GetManifestUrl()
		x.Media.ManifestUrl = req.GetManifestUrl()
	})
	d.items[id] = x
	d.order = append(d.order, id)
	return &downexv1.AddMediaResponse{Download: x}, nil
}

func (d *Daemon) ProbeMedia(_ context.Context, req *downexv1.ProbeMediaRequest) (*downexv1.ProbeMediaResponse, error) {
	if err := d.gate("probeMedia", req); err != nil {
		return nil, err
	}
	switch u := req.GetManifestUrl(); {
	case strings.Contains(u, "drm"):
		return nil, Err(connect.CodeInvalidArgument, "encrypted: the stream is DRM-protected")
	case strings.Contains(u, "live"):
		return nil, Err(connect.CodeInvalidArgument, "live: the presentation is live")
	}
	return &downexv1.ProbeMediaResponse{Media: formattest.MediaInfo(func(m *downexv1.MediaInfo) { m.ManifestUrl = req.GetManifestUrl() })}, nil
}

func (d *Daemon) SetDownloadOptions(_ context.Context, req *downexv1.SetDownloadOptionsRequest) (*downexv1.SetDownloadOptionsResponse, error) {
	if err := d.gate("setDownloadOptions", req.GetId(), req); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if req.CompletionDialog != nil && d.items[req.GetId()].GetState() == state.StateCompleted {
		// As downexd: a completed download takes no options.
		return nil, Err(connect.CodeFailedPrecondition, "download %d is already completed", req.GetId())
	}
	x, err := d.updateLocked(req.GetId(), func(x *downexv1.Download) {
		if req.SpeedLimit != nil {
			x.SpeedLimit = req.GetSpeedLimit()
		}
		if req.MaxConnections != nil {
			x.MaxConnections = req.GetMaxConnections()
		}
		if req.CompletionDialog != nil {
			x.CompletionDialog = req.GetCompletionDialog()
		}
	})
	if err != nil {
		return nil, err
	}
	return &downexv1.SetDownloadOptionsResponse{Download: x}, nil
}

func (d *Daemon) PauseAll(context.Context) (*downexv1.PauseAllResponse, error) {
	if err := d.gate("pauseAll"); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var n int32
	for _, id := range d.order {
		if s := d.items[id].GetState(); s == state.StateDownloading || s == state.StateQueued || s == state.StateProbing {
			_, _ = d.updateLocked(id, func(x *downexv1.Download) { x.State, x.SpeedBps = state.StatePaused, 0 })
			n++
		}
	}
	return &downexv1.PauseAllResponse{Paused: n}, nil
}

func (d *Daemon) ResumeAll(context.Context) (*downexv1.ResumeAllResponse, error) {
	if err := d.gate("resumeAll"); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var n int32
	for _, id := range d.order {
		if d.items[id].GetState() == state.StatePaused {
			_, _ = d.updateLocked(id, func(x *downexv1.Download) { x.State = state.StateQueued })
			n++
		}
	}
	return &downexv1.ResumeAllResponse{Resumed: n}, nil
}

func (d *Daemon) GetSettings(context.Context) (*downexv1.GetSettingsResponse, error) {
	if err := d.gate("getSettings"); err != nil {
		return nil, err
	}
	return &downexv1.GetSettingsResponse{Settings: d.Settings()}, nil
}

func (d *Daemon) UpdateSettings(_ context.Context, req *downexv1.UpdateSettingsRequest) (*downexv1.UpdateSettingsResponse, error) {
	if err := d.gate("updateSettings", req.GetSettings()); err != nil {
		return nil, err
	}
	s := proto.CloneOf(req.GetSettings())
	// As downexd: new site logins get an id.
	n := 1
	for _, l := range s.GetSiteLogins() {
		if l.GetId() == "" {
			l.Id = fmt.Sprintf("login-%d", n)
			n++
		}
	}
	d.SetSettings(s)
	return &downexv1.UpdateSettingsResponse{Settings: s}, nil
}

func (d *Daemon) Status(context.Context) (*downexv1.StatusResponse, error) {
	if err := d.gate("status"); err != nil {
		return nil, err
	}
	return d.status, nil
}

// Watch answers with a stream of the batches Emit publishes, after a snapshot of the
// downloads ids (all when none).
func (d *Daemon) Watch(ctx context.Context, ids []int64) (host.WatchStream, error) {
	if err := d.gate("watch", ids); err != nil {
		return nil, err
	}
	list, _ := d.List(ctx)
	var first []*downexv1.Event
	for _, x := range list.GetDownloads() {
		if len(ids) == 0 || slices.Contains(ids, x.GetId()) {
			first = append(first, &downexv1.Event{Kind: &downexv1.Event_Snapshot{Snapshot: x}})
		}
	}
	return NewStream(ctx, &d.Events, first), nil
}

// ---- QueueService and the rest -------------------------------------------------------

func (d *Daemon) queueLocked(id int64) (*downexv1.Queue, error) {
	q := d.queues[id]
	if q == nil {
		return nil, Err(connect.CodeNotFound, "queue %d not found", id)
	}
	return q, nil
}

func (d *Daemon) moveLocked(ids []int64, queueID int64, position int32) (*downexv1.Queue, error) {
	target, err := d.queueLocked(queueID)
	if err != nil {
		return nil, err
	}
	for qid, q := range d.queues {
		c := proto.CloneOf(q)
		c.DownloadIds = slices.DeleteFunc(slices.Clone(q.GetDownloadIds()), func(x int64) bool { return slices.Contains(ids, x) })
		d.queues[qid] = c
	}
	target = d.queues[queueID]
	order := slices.Clone(target.GetDownloadIds())
	at := int(position)
	if position < 0 || at > len(order) {
		at = len(order)
	}
	order = slices.Insert(order, at, ids...)
	for _, id := range ids {
		if d.items[id] != nil {
			_, _ = d.updateLocked(id, func(x *downexv1.Download) { x.QueueId = queueID })
		}
	}
	c := proto.CloneOf(target)
	c.DownloadIds = order
	d.queues[queueID] = c
	return c, nil
}

func (d *Daemon) ListQueues(context.Context) (*downexv1.ListQueuesResponse, error) {
	if err := d.gate("listQueues"); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	out := &downexv1.ListQueuesResponse{}
	ids := make([]int64, 0, len(d.queues))
	for id := range d.queues {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		out.Queues = append(out.Queues, d.queues[id])
	}
	return out, nil
}

func (d *Daemon) CreateQueue(_ context.Context, req *downexv1.CreateQueueRequest) (*downexv1.CreateQueueResponse, error) {
	if err := d.gate("createQueue", req.GetQueue()); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	q := proto.CloneOf(req.GetQueue())
	q.Id = d.nextQ
	d.nextQ++
	q.Running, q.IsMain, q.DownloadIds = false, false, nil
	if q.Schedule == nil {
		q.Schedule = &downexv1.Schedule{}
	}
	d.queues[q.Id] = q
	return &downexv1.CreateQueueResponse{Queue: q}, nil
}

func (d *Daemon) UpdateQueue(_ context.Context, req *downexv1.UpdateQueueRequest) (*downexv1.UpdateQueueResponse, error) {
	if err := d.gate("updateQueue", req.GetQueue().GetId(), req.GetQueue()); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	cur, err := d.queueLocked(req.GetQueue().GetId())
	if err != nil {
		return nil, err
	}
	spec := req.GetQueue()
	q := proto.CloneOf(cur)
	q.Name, q.MaxConcurrent, q.SpeedLimit, q.Schedule, q.PostAction = spec.GetName(), spec.GetMaxConcurrent(), spec.GetSpeedLimit(), spec.GetSchedule(), spec.GetPostAction()
	d.queues[q.Id] = q
	return &downexv1.UpdateQueueResponse{Queue: q}, nil
}

func (d *Daemon) DeleteQueue(_ context.Context, req *downexv1.DeleteQueueRequest) (*downexv1.DeleteQueueResponse, error) {
	if err := d.gate("deleteQueue", req.GetId()); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	q, err := d.queueLocked(req.GetId())
	if err != nil {
		return nil, err
	}
	if q.GetIsMain() {
		return nil, Err(connect.CodeFailedPrecondition, "the main queue cannot be deleted")
	}
	delete(d.queues, req.GetId())
	return &downexv1.DeleteQueueResponse{}, nil
}

func (d *Daemon) setRunning(name string, id int64, running bool) (*downexv1.Queue, error) {
	if err := d.gate(name, id); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	q, err := d.queueLocked(id)
	if err != nil {
		return nil, err
	}
	c := proto.CloneOf(q)
	c.Running = running
	d.queues[id] = c
	return c, nil
}

func (d *Daemon) StartQueue(_ context.Context, req *downexv1.StartQueueRequest) (*downexv1.StartQueueResponse, error) {
	q, err := d.setRunning("startQueue", req.GetId(), true)
	if err != nil {
		return nil, err
	}
	return &downexv1.StartQueueResponse{Queue: q}, nil
}

func (d *Daemon) StopQueue(_ context.Context, req *downexv1.StopQueueRequest) (*downexv1.StopQueueResponse, error) {
	q, err := d.setRunning("stopQueue", req.GetId(), false)
	if err != nil {
		return nil, err
	}
	return &downexv1.StopQueueResponse{Queue: q}, nil
}

func (d *Daemon) MoveDownloads(_ context.Context, req *downexv1.MoveDownloadsRequest) (*downexv1.MoveDownloadsResponse, error) {
	if err := d.gate("moveDownloads", req.GetIds(), req.GetQueueId(), req.GetPosition()); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	q, err := d.moveLocked(req.GetIds(), req.GetQueueId(), req.GetPosition())
	if err != nil {
		return nil, err
	}
	return &downexv1.MoveDownloadsResponse{Queue: q}, nil
}

func (d *Daemon) SetSecret(_ context.Context, req *downexv1.SetSecretRequest) (*downexv1.SetSecretResponse, error) {
	if err := d.gate("setSecret", req.GetKind(), req.GetId(), "<password>"); err != nil {
		return nil, err
	}
	return &downexv1.SetSecretResponse{}, nil
}

func (d *Daemon) ClearSecret(_ context.Context, req *downexv1.ClearSecretRequest) (*downexv1.ClearSecretResponse, error) {
	if err := d.gate("clearSecret", req.GetKind(), req.GetId()); err != nil {
		return nil, err
	}
	return &downexv1.ClearSecretResponse{}, nil
}

func (d *Daemon) CheckForUpdate(context.Context) (*downexv1.CheckForUpdateResponse, error) {
	if err := d.gate("checkForUpdate"); err != nil {
		return nil, err
	}
	return nil, Err(connect.CodeFailedPrecondition, "updates are off: no update feed is set")
}

// SetInstallUpdate makes InstallUpdate answer that it installs info (nil: that no update is
// available, the default).
func (d *Daemon) SetInstallUpdate(info *downexv1.UpdateInfo) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.install = info
}

func (d *Daemon) InstallUpdate(context.Context) (*downexv1.InstallUpdateResponse, error) {
	if err := d.gate("installUpdate"); err != nil {
		return nil, err
	}
	d.mu.Lock()
	info := d.install
	d.mu.Unlock()
	if info != nil {
		return &downexv1.InstallUpdateResponse{Info: info}, nil
	}
	return nil, Err(connect.CodeFailedPrecondition, "no update is available")
}

func (d *Daemon) CancelPostAction(_ context.Context, req *downexv1.CancelPostActionRequest) (*downexv1.CancelPostActionResponse, error) {
	if err := d.gate("cancelPostAction", req.GetQueueId()); err != nil {
		return nil, err
	}
	return &downexv1.CancelPostActionResponse{}, nil
}

func (d *Daemon) GetTools(context.Context) (*downexv1.GetToolsResponse, error) {
	if err := d.gate("getTools"); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.tools == nil {
		return nil, Err(connect.CodeUnimplemented, "downex.v1.DaemonService.GetTools is not implemented")
	}
	return &downexv1.GetToolsResponse{Status: d.tools}, nil
}

func (d *Daemon) InstallTools(_ context.Context, req *downexv1.InstallToolsRequest) (*downexv1.InstallToolsResponse, error) {
	if err := d.gate("installTools", req.GetUpdate()); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.tools == nil {
		return nil, Err(connect.CodeUnimplemented, "downex.v1.DaemonService.InstallTools is not implemented")
	}
	enabled := d.tools.GetEnabled()
	d.tools = Tools(func(t *downexv1.ToolsStatus) { t.Enabled = enabled })
	return &downexv1.InstallToolsResponse{Status: d.tools}, nil
}

var _ host.Daemon = (*Daemon)(nil)
