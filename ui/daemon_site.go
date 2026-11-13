package main

import (
	"context"

	"downex/internal/client"
	downexv1 "downex/internal/rpc/downex/v1"
)

// The site media calls of clientDaemon (docs/SITE-MEDIA-SPEC.md §3.1): the tools RPCs of
// DaemonService. Like every unary call they go out after the first contact and never
// start downexd later. AddMedia and ProbeMedia carry web pages (origin SITE) unchanged.

func (d *clientDaemon) GetTools(ctx context.Context) (*downexv1.GetToolsResponse, error) {
	return unary(ctx, d, &downexv1.GetToolsRequest{}, func(c *client.Client) rpc[downexv1.GetToolsRequest, downexv1.GetToolsResponse] {
		return c.Daemon.GetTools
	})
}

func (d *clientDaemon) InstallTools(ctx context.Context, req *downexv1.InstallToolsRequest) (*downexv1.InstallToolsResponse, error) {
	return unary(ctx, d, req, func(c *client.Client) rpc[downexv1.InstallToolsRequest, downexv1.InstallToolsResponse] {
		return c.Daemon.InstallTools
	})
}
