package daemon

import (
	"context"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/rpc/downex/v1/downexv1connect"
)

// NewQueueService returns the QueueService implementation backed by m
// (docs/M6-SPEC.md §1).
func NewQueueService(m *Manager) downexv1connect.QueueServiceHandler {
	return queueService{m: m}
}

type queueService struct{ m *Manager }

var _ downexv1connect.QueueServiceHandler = queueService{}

func (s queueService) ListQueues(context.Context, *connect.Request[downexv1.ListQueuesRequest]) (*connect.Response[downexv1.ListQueuesResponse], error) {
	return connect.NewResponse(&downexv1.ListQueuesResponse{Queues: s.m.ListQueues()}), nil
}

func (s queueService) CreateQueue(ctx context.Context, req *connect.Request[downexv1.CreateQueueRequest]) (*connect.Response[downexv1.CreateQueueResponse], error) {
	q, err := s.m.CreateQueue(ctx, req.Msg.GetQueue())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&downexv1.CreateQueueResponse{Queue: q}), nil
}

func (s queueService) UpdateQueue(ctx context.Context, req *connect.Request[downexv1.UpdateQueueRequest]) (*connect.Response[downexv1.UpdateQueueResponse], error) {
	if req.Msg.GetQueue() == nil {
		return nil, errInvalid("queue is required")
	}
	q, err := s.m.UpdateQueue(ctx, req.Msg.GetQueue())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&downexv1.UpdateQueueResponse{Queue: q}), nil
}

func (s queueService) DeleteQueue(ctx context.Context, req *connect.Request[downexv1.DeleteQueueRequest]) (*connect.Response[downexv1.DeleteQueueResponse], error) {
	if err := s.m.DeleteQueue(ctx, req.Msg.GetId()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&downexv1.DeleteQueueResponse{}), nil
}

func (s queueService) StartQueue(ctx context.Context, req *connect.Request[downexv1.StartQueueRequest]) (*connect.Response[downexv1.StartQueueResponse], error) {
	q, err := s.m.StartQueue(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&downexv1.StartQueueResponse{Queue: q}), nil
}

func (s queueService) StopQueue(ctx context.Context, req *connect.Request[downexv1.StopQueueRequest]) (*connect.Response[downexv1.StopQueueResponse], error) {
	q, err := s.m.StopQueue(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&downexv1.StopQueueResponse{Queue: q}), nil
}

func (s queueService) MoveDownloads(ctx context.Context, req *connect.Request[downexv1.MoveDownloadsRequest]) (*connect.Response[downexv1.MoveDownloadsResponse], error) {
	q, err := s.m.MoveDownloads(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&downexv1.MoveDownloadsResponse{Queue: q}), nil
}

// CancelPostAction cancels a post-queue action's countdown (queue_id 0: every one).
func (s daemonService) CancelPostAction(ctx context.Context, req *connect.Request[downexv1.CancelPostActionRequest]) (*connect.Response[downexv1.CancelPostActionResponse], error) {
	if err := s.m.CancelPostAction(ctx, req.Msg.GetQueueId()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&downexv1.CancelPostActionResponse{}), nil
}
