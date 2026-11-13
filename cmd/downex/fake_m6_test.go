package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	downexv1 "downex/internal/rpc/downex/v1"
)

// fakeM6 is the M6 state of a fakeDaemon (docs/M6-SPEC.md): the QueueService, the
// secret store behind SetSecret/ClearSecret (has_password is derived from it, ids of
// new site logins are assigned like the daemon does), updates and post-queue actions.
// Every field is guarded by fakeDaemon.mu.
type fakeM6 struct {
	queues     []*downexv1.Queue
	nextQueue  int64
	queueCalls []proto.Message // every QueueService request, in order

	secrets     map[string]string // "kind/id" -> password
	secretCalls []proto.Message   // SetSecret / ClearSecret requests
	secretErr   error             // SetSecret fails with it
	nextLogin   int

	update      *downexv1.UpdateInfo
	updateErr   error
	installs    int
	cancelPosts []int64
	cancelErr   error
}

func (f *fakeDaemon) initM6() {
	f.queues = []*downexv1.Queue{{Id: 1, Name: "Main", Running: true, IsMain: true, DownloadIds: []int64{7}}}
	f.nextQueue = 2
	f.secrets = make(map[string]string)
	f.update = &downexv1.UpdateInfo{CurrentVersion: "0.3.3", LatestVersion: "0.3.3"}
}

// settingsOut returns a copy of the settings as the daemon reports them: has_password
// from the secret store. f.mu must be held.
func (f *fakeDaemon) settingsOut() *downexv1.Settings {
	s := proto.Clone(f.settings).(*downexv1.Settings)
	if p := s.GetProxy(); p != nil {
		_, p.HasPassword = f.secrets[secretProxy+"/"]
	}
	for _, l := range s.GetSiteLogins() {
		_, l.HasPassword = f.secrets[secretSiteLogin+"/"+l.GetId()]
	}
	return s
}

// assignLoginIDs gives new site logins (empty id) their stable id. f.mu must be held.
func (f *fakeDaemon) assignLoginIDs() {
	for _, l := range f.settings.GetSiteLogins() {
		if l.GetId() == "" {
			f.nextLogin++
			l.Id = fmt.Sprintf("L%d", f.nextLogin)
		}
	}
}

func (f *fakeDaemon) queueByID(id int64) (*downexv1.Queue, error) {
	for _, q := range f.queues {
		if q.GetId() == id {
			return q, nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("queue %d not found", id))
}

func cloneQueue(q *downexv1.Queue) *downexv1.Queue { return proto.Clone(q).(*downexv1.Queue) }

func (f *fakeDaemon) ListQueues(_ context.Context, req *connect.Request[downexv1.ListQueuesRequest]) (*connect.Response[downexv1.ListQueuesResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queueCalls = append(f.queueCalls, req.Msg)
	resp := &downexv1.ListQueuesResponse{}
	for _, q := range f.queues {
		resp.Queues = append(resp.Queues, cloneQueue(q))
	}
	slices.SortFunc(resp.Queues, func(x, y *downexv1.Queue) int { return cmp.Compare(x.Id, y.Id) })
	return connect.NewResponse(resp), nil
}

func (f *fakeDaemon) CreateQueue(_ context.Context, req *connect.Request[downexv1.CreateQueueRequest]) (*connect.Response[downexv1.CreateQueueResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queueCalls = append(f.queueCalls, req.Msg)
	for _, q := range f.queues {
		if q.GetName() == req.Msg.GetQueue().GetName() {
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("a queue named %q exists", q.GetName()))
		}
	}
	q := cloneQueue(req.Msg.GetQueue())
	q.Id, q.Running, q.IsMain, q.DownloadIds = f.nextQueue, false, false, nil
	f.nextQueue++
	f.queues = append(f.queues, q)
	return connect.NewResponse(&downexv1.CreateQueueResponse{Queue: cloneQueue(q)}), nil
}

func (f *fakeDaemon) UpdateQueue(_ context.Context, req *connect.Request[downexv1.UpdateQueueRequest]) (*connect.Response[downexv1.UpdateQueueResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queueCalls = append(f.queueCalls, req.Msg)
	in := req.Msg.GetQueue()
	q, err := f.queueByID(in.GetId())
	if err != nil {
		return nil, err
	}
	q.Name, q.MaxConcurrent, q.SpeedLimit, q.PostAction = in.GetName(), in.GetMaxConcurrent(), in.GetSpeedLimit(), in.GetPostAction()
	q.Schedule = nil
	if in.GetSchedule() != nil {
		q.Schedule = proto.Clone(in.GetSchedule()).(*downexv1.Schedule)
	}
	return connect.NewResponse(&downexv1.UpdateQueueResponse{Queue: cloneQueue(q)}), nil
}

func (f *fakeDaemon) DeleteQueue(_ context.Context, req *connect.Request[downexv1.DeleteQueueRequest]) (*connect.Response[downexv1.DeleteQueueResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queueCalls = append(f.queueCalls, req.Msg)
	q, err := f.queueByID(req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if q.GetIsMain() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the main queue cannot be deleted"))
	}
	f.queues = slices.DeleteFunc(f.queues, func(x *downexv1.Queue) bool { return x == q })
	f.queues[0].DownloadIds = append(f.queues[0].DownloadIds, q.GetDownloadIds()...)
	return connect.NewResponse(&downexv1.DeleteQueueResponse{}), nil
}

func (f *fakeDaemon) StartQueue(_ context.Context, req *connect.Request[downexv1.StartQueueRequest]) (*connect.Response[downexv1.StartQueueResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queueCalls = append(f.queueCalls, req.Msg)
	q, err := f.queueByID(req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	q.Running = true
	return connect.NewResponse(&downexv1.StartQueueResponse{Queue: cloneQueue(q)}), nil
}

func (f *fakeDaemon) StopQueue(_ context.Context, req *connect.Request[downexv1.StopQueueRequest]) (*connect.Response[downexv1.StopQueueResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queueCalls = append(f.queueCalls, req.Msg)
	q, err := f.queueByID(req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	q.Running = false
	return connect.NewResponse(&downexv1.StopQueueResponse{Queue: cloneQueue(q)}), nil
}

func (f *fakeDaemon) MoveDownloads(_ context.Context, req *connect.Request[downexv1.MoveDownloadsRequest]) (*connect.Response[downexv1.MoveDownloadsResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queueCalls = append(f.queueCalls, req.Msg)
	target, err := f.queueByID(req.Msg.GetQueueId())
	if err != nil {
		return nil, err
	}
	for _, id := range req.Msg.GetIds() {
		if _, ok := f.downloads[id]; !ok {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("download %d not found", id))
		}
	}
	for _, q := range f.queues {
		q.DownloadIds = slices.DeleteFunc(q.DownloadIds, func(id int64) bool { return slices.Contains(req.Msg.GetIds(), id) })
	}
	at := int(req.Msg.GetPosition())
	if at < 0 || at > len(target.DownloadIds) {
		at = len(target.DownloadIds)
	}
	target.DownloadIds = slices.Insert(target.DownloadIds, at, req.Msg.GetIds()...)
	return connect.NewResponse(&downexv1.MoveDownloadsResponse{Queue: cloneQueue(target)}), nil
}

func (f *fakeDaemon) SetSecret(_ context.Context, req *connect.Request[downexv1.SetSecretRequest]) (*connect.Response[downexv1.SetSecretResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.secretCalls = append(f.secretCalls, req.Msg)
	if f.secretErr != nil {
		return nil, f.secretErr
	}
	f.secrets[req.Msg.GetKind()+"/"+req.Msg.GetId()] = req.Msg.GetPassword()
	return connect.NewResponse(&downexv1.SetSecretResponse{}), nil
}

func (f *fakeDaemon) ClearSecret(_ context.Context, req *connect.Request[downexv1.ClearSecretRequest]) (*connect.Response[downexv1.ClearSecretResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.secretCalls = append(f.secretCalls, req.Msg)
	key := req.Msg.GetKind() + "/" + req.Msg.GetId()
	if _, ok := f.secrets[key]; !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such secret"))
	}
	delete(f.secrets, key)
	return connect.NewResponse(&downexv1.ClearSecretResponse{}), nil
}

func (f *fakeDaemon) CheckForUpdate(context.Context, *connect.Request[downexv1.CheckForUpdateRequest]) (*connect.Response[downexv1.CheckForUpdateResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	return connect.NewResponse(&downexv1.CheckForUpdateResponse{Info: proto.Clone(f.update).(*downexv1.UpdateInfo)}), nil
}

func (f *fakeDaemon) InstallUpdate(context.Context, *connect.Request[downexv1.InstallUpdateRequest]) (*connect.Response[downexv1.InstallUpdateResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installs++
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	return connect.NewResponse(&downexv1.InstallUpdateResponse{Info: proto.Clone(f.update).(*downexv1.UpdateInfo)}), nil
}

func (f *fakeDaemon) CancelPostAction(_ context.Context, req *connect.Request[downexv1.CancelPostActionRequest]) (*connect.Response[downexv1.CancelPostActionResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelPosts = append(f.cancelPosts, req.Msg.GetQueueId())
	if f.cancelErr != nil {
		return nil, f.cancelErr
	}
	return connect.NewResponse(&downexv1.CancelPostActionResponse{}), nil
}

// m6Record is a copy of the M6 requests a fakeDaemon received.
type m6Record struct {
	queueCalls  []proto.Message
	secretCalls []proto.Message
	installs    int
	cancelPosts []int64
}

func (f *fakeDaemon) m6() m6Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	return m6Record{queueCalls: slices.Clone(f.queueCalls), secretCalls: slices.Clone(f.secretCalls),
		installs: f.installs, cancelPosts: slices.Clone(f.cancelPosts)}
}

// queueCallsOf returns the queue requests of type T.
func queueCallsOf[T proto.Message](r m6Record) []T {
	var out []T
	for _, m := range r.queueCalls {
		if v, ok := m.(T); ok {
			out = append(out, v)
		}
	}
	return out
}

// queue returns a copy of the fake's queue id.
func (f *fakeDaemon) queue(id int64) *downexv1.Queue {
	f.mu.Lock()
	defer f.mu.Unlock()
	q, err := f.queueByID(id)
	if err != nil {
		return nil
	}
	return cloneQueue(q)
}

// currentSettings returns a copy of the fake's settings as the daemon reports them.
func (f *fakeDaemon) currentSettings() *downexv1.Settings {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.settingsOut()
}
