package main

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	downexv1 "downex/internal/rpc/downex/v1"
)

// The M6 calls of fakeDaemon: queues kept in f.queues (the main queue is created on
// first use), secrets only recorded, and f.updateInfo as the update check's answer.

func (f *fakeDaemon) queueList() []*downexv1.Queue {
	if len(f.queues) == 0 {
		f.queues = []*downexv1.Queue{{Id: 1, Name: "Main queue", Running: true, IsMain: true}}
		f.nextQueue = 2
	}
	return f.queues
}

func (f *fakeDaemon) queue(id int64) (*downexv1.Queue, error) {
	for _, q := range f.queueList() {
		if q.GetId() == id {
			return q, nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("queue %d not found", id))
}

func (f *fakeDaemon) ListQueues(context.Context) (*downexv1.ListQueuesResponse, error) {
	if err := f.record(&downexv1.ListQueuesRequest{}); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	resp := &downexv1.ListQueuesResponse{}
	for _, q := range f.queueList() {
		resp.Queues = append(resp.Queues, proto.CloneOf(q))
	}
	return resp, nil
}

func (f *fakeDaemon) CreateQueue(_ context.Context, req *downexv1.CreateQueueRequest) (*downexv1.CreateQueueResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	in := req.GetQueue()
	if in.GetName() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name: must not be empty"))
	}
	f.queueList()
	q := &downexv1.Queue{
		Id: f.nextQueue, Name: in.GetName(), MaxConcurrent: in.GetMaxConcurrent(), SpeedLimit: in.GetSpeedLimit(),
		Schedule: proto.CloneOf(in.GetSchedule()), PostAction: in.GetPostAction(),
	}
	f.nextQueue++
	f.queues = append(f.queues, q)
	return &downexv1.CreateQueueResponse{Queue: proto.CloneOf(q)}, nil
}

func (f *fakeDaemon) UpdateQueue(_ context.Context, req *downexv1.UpdateQueueRequest) (*downexv1.UpdateQueueResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	in := req.GetQueue()
	q, err := f.queue(in.GetId())
	if err != nil {
		return nil, err
	}
	q.Name, q.MaxConcurrent, q.SpeedLimit = in.GetName(), in.GetMaxConcurrent(), in.GetSpeedLimit()
	q.Schedule, q.PostAction = proto.CloneOf(in.GetSchedule()), in.GetPostAction()
	return &downexv1.UpdateQueueResponse{Queue: proto.CloneOf(q)}, nil
}

func (f *fakeDaemon) DeleteQueue(_ context.Context, req *downexv1.DeleteQueueRequest) (*downexv1.DeleteQueueResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	q, err := f.queue(req.GetId())
	if err != nil {
		return nil, err
	}
	if q.GetIsMain() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the main queue cannot be deleted"))
	}
	f.queues = slices.DeleteFunc(f.queues, func(x *downexv1.Queue) bool { return x == q })
	main := f.queues[0]
	main.DownloadIds = append(main.DownloadIds, q.GetDownloadIds()...)
	return &downexv1.DeleteQueueResponse{}, nil
}

func (f *fakeDaemon) setRunning(id int64, running bool) (*downexv1.Queue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q, err := f.queue(id)
	if err != nil {
		return nil, err
	}
	q.Running = running
	return proto.CloneOf(q), nil
}

func (f *fakeDaemon) StartQueue(_ context.Context, req *downexv1.StartQueueRequest) (*downexv1.StartQueueResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	q, err := f.setRunning(req.GetId(), true)
	if err != nil {
		return nil, err
	}
	return &downexv1.StartQueueResponse{Queue: q}, nil
}

func (f *fakeDaemon) StopQueue(_ context.Context, req *downexv1.StopQueueRequest) (*downexv1.StopQueueResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	q, err := f.setRunning(req.GetId(), false)
	if err != nil {
		return nil, err
	}
	return &downexv1.StopQueueResponse{Queue: q}, nil
}

func (f *fakeDaemon) MoveDownloads(_ context.Context, req *downexv1.MoveDownloadsRequest) (*downexv1.MoveDownloadsResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	target, err := f.queue(req.GetQueueId())
	if err != nil {
		return nil, err
	}
	for _, q := range f.queues {
		q.DownloadIds = slices.DeleteFunc(q.DownloadIds, func(id int64) bool { return slices.Contains(req.GetIds(), id) })
	}
	pos := int(req.GetPosition())
	if pos < 0 || pos > len(target.DownloadIds) {
		pos = len(target.DownloadIds)
	}
	target.DownloadIds = slices.Insert(target.DownloadIds, pos, req.GetIds()...)
	return &downexv1.MoveDownloadsResponse{Queue: proto.CloneOf(target)}, nil
}

func (f *fakeDaemon) SetSecret(_ context.Context, req *downexv1.SetSecretRequest) (*downexv1.SetSecretResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	if req.GetKind() != "proxy" && req.GetKind() != "site_login" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("kind: must be proxy or site_login"))
	}
	return &downexv1.SetSecretResponse{}, nil
}

func (f *fakeDaemon) ClearSecret(_ context.Context, req *downexv1.ClearSecretRequest) (*downexv1.ClearSecretResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	return &downexv1.ClearSecretResponse{}, nil
}

func (f *fakeDaemon) CheckForUpdate(context.Context) (*downexv1.CheckForUpdateResponse, error) {
	if err := f.record(&downexv1.CheckForUpdateRequest{}); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updateInfo == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("updates are off: no update feed is set"))
	}
	return &downexv1.CheckForUpdateResponse{Info: proto.CloneOf(f.updateInfo)}, nil
}

func (f *fakeDaemon) InstallUpdate(context.Context) (*downexv1.InstallUpdateResponse, error) {
	if err := f.record(&downexv1.InstallUpdateRequest{}); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.updateInfo.GetAvailable() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("no update is available"))
	}
	return &downexv1.InstallUpdateResponse{Info: proto.CloneOf(f.updateInfo)}, nil
}

func (f *fakeDaemon) CancelPostAction(_ context.Context, req *downexv1.CancelPostActionRequest) (*downexv1.CancelPostActionResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	return &downexv1.CancelPostActionResponse{}, nil
}
