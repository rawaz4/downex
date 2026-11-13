package main

import (
	"context"

	"downex/internal/client"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/rpc/downex/v1/downexv1connect"
)

// The M6 calls of clientDaemon (docs/M6-SPEC.md): QueueService (client.Client.Queues), and the new DaemonService methods. Like every unary call they go
// out after the first contact and never start downexd later.

// queueRPC adapts a QueueService method to unary's client-based selector.
func queueRPC[Req, Res any](_ *clientDaemon, pick func(q downexv1connect.QueueServiceClient) rpc[Req, Res]) func(*client.Client) rpc[Req, Res] {
	return func(c *client.Client) rpc[Req, Res] { return pick(c.Queues) }
}

func (d *clientDaemon) ListQueues(ctx context.Context) (*downexv1.ListQueuesResponse, error) {
	return unary(ctx, d, &downexv1.ListQueuesRequest{}, queueRPC(d, func(q downexv1connect.QueueServiceClient) rpc[downexv1.ListQueuesRequest, downexv1.ListQueuesResponse] {
		return q.ListQueues
	}))
}

func (d *clientDaemon) CreateQueue(ctx context.Context, req *downexv1.CreateQueueRequest) (*downexv1.CreateQueueResponse, error) {
	return unary(ctx, d, req, queueRPC(d, func(q downexv1connect.QueueServiceClient) rpc[downexv1.CreateQueueRequest, downexv1.CreateQueueResponse] {
		return q.CreateQueue
	}))
}

func (d *clientDaemon) UpdateQueue(ctx context.Context, req *downexv1.UpdateQueueRequest) (*downexv1.UpdateQueueResponse, error) {
	return unary(ctx, d, req, queueRPC(d, func(q downexv1connect.QueueServiceClient) rpc[downexv1.UpdateQueueRequest, downexv1.UpdateQueueResponse] {
		return q.UpdateQueue
	}))
}

func (d *clientDaemon) DeleteQueue(ctx context.Context, req *downexv1.DeleteQueueRequest) (*downexv1.DeleteQueueResponse, error) {
	return unary(ctx, d, req, queueRPC(d, func(q downexv1connect.QueueServiceClient) rpc[downexv1.DeleteQueueRequest, downexv1.DeleteQueueResponse] {
		return q.DeleteQueue
	}))
}

func (d *clientDaemon) StartQueue(ctx context.Context, req *downexv1.StartQueueRequest) (*downexv1.StartQueueResponse, error) {
	return unary(ctx, d, req, queueRPC(d, func(q downexv1connect.QueueServiceClient) rpc[downexv1.StartQueueRequest, downexv1.StartQueueResponse] {
		return q.StartQueue
	}))
}

func (d *clientDaemon) StopQueue(ctx context.Context, req *downexv1.StopQueueRequest) (*downexv1.StopQueueResponse, error) {
	return unary(ctx, d, req, queueRPC(d, func(q downexv1connect.QueueServiceClient) rpc[downexv1.StopQueueRequest, downexv1.StopQueueResponse] {
		return q.StopQueue
	}))
}

func (d *clientDaemon) MoveDownloads(ctx context.Context, req *downexv1.MoveDownloadsRequest) (*downexv1.MoveDownloadsResponse, error) {
	return unary(ctx, d, req, queueRPC(d, func(q downexv1connect.QueueServiceClient) rpc[downexv1.MoveDownloadsRequest, downexv1.MoveDownloadsResponse] {
		return q.MoveDownloads
	}))
}

func (d *clientDaemon) SetSecret(ctx context.Context, req *downexv1.SetSecretRequest) (*downexv1.SetSecretResponse, error) {
	return unary(ctx, d, req, func(c *client.Client) rpc[downexv1.SetSecretRequest, downexv1.SetSecretResponse] {
		return c.Daemon.SetSecret
	})
}

func (d *clientDaemon) ClearSecret(ctx context.Context, req *downexv1.ClearSecretRequest) (*downexv1.ClearSecretResponse, error) {
	return unary(ctx, d, req, func(c *client.Client) rpc[downexv1.ClearSecretRequest, downexv1.ClearSecretResponse] {
		return c.Daemon.ClearSecret
	})
}

func (d *clientDaemon) CheckForUpdate(ctx context.Context) (*downexv1.CheckForUpdateResponse, error) {
	return unary(ctx, d, &downexv1.CheckForUpdateRequest{}, func(c *client.Client) rpc[downexv1.CheckForUpdateRequest, downexv1.CheckForUpdateResponse] {
		return c.Daemon.CheckForUpdate
	})
}

func (d *clientDaemon) InstallUpdate(ctx context.Context) (*downexv1.InstallUpdateResponse, error) {
	return unary(ctx, d, &downexv1.InstallUpdateRequest{}, func(c *client.Client) rpc[downexv1.InstallUpdateRequest, downexv1.InstallUpdateResponse] {
		return c.Daemon.InstallUpdate
	})
}

func (d *clientDaemon) CancelPostAction(ctx context.Context, req *downexv1.CancelPostActionRequest) (*downexv1.CancelPostActionResponse, error) {
	return unary(ctx, d, req, func(c *client.Client) rpc[downexv1.CancelPostActionRequest, downexv1.CancelPostActionResponse] {
		return c.Daemon.CancelPostAction
	})
}
