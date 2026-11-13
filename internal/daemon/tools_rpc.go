package daemon

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/sitemedia"
)

// The tools RPCs of site media (docs/SITE-MEDIA-SPEC.md §3.3): GetTools reports the
// yt-dlp and Deno copies the daemon would run, InstallTools starts or joins their
// install. Both need Config.Tools; without it they answer Unimplemented with the code
// tools_unavailable, like the M5 RPCs of a daemon without Config.Media.

// errNoTools is the error of the tools RPCs while no tools manager is configured.
var errNoTools = fmt.Errorf("%w: this Downex daemon does not download tools", sitemedia.ErrToolsUnavailable)

// GetTools returns the status of the tools that resolve video pages.
func (s daemonService) GetTools(context.Context, *connect.Request[downexv1.GetToolsRequest]) (*connect.Response[downexv1.GetToolsResponse], error) {
	st, err := s.m.ToolsStatus()
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&downexv1.GetToolsResponse{Status: st}), nil
}

// InstallTools starts or joins the install of the tools and returns when it ends or at
// the caller's deadline (the install goes on).
func (s daemonService) InstallTools(ctx context.Context, req *connect.Request[downexv1.InstallToolsRequest]) (*connect.Response[downexv1.InstallToolsResponse], error) {
	st, err := s.m.InstallTools(ctx, req.Msg.GetUpdate())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&downexv1.InstallToolsResponse{Status: st}), nil
}

// ToolsStatus describes the tools (Config.Tools' status). It answers even while site
// media is off: the tools are then disabled.
func (m *Manager) ToolsStatus() (*downexv1.ToolsStatus, error) {
	if m.isClosed() {
		return nil, errShuttingDown()
	}
	if m.cfg.Tools == nil {
		return nil, mediaError(CodeToolsUnavailable, connect.CodeUnimplemented, errNoTools)
	}
	return protoToolsStatus(m.cfg.Tools.Status()), nil
}

// InstallTools starts or joins the install of the tools (with update, after looking for
// newer releases) and returns their status when it ends. It returns
// siteDeadlineMargin before the caller's deadline with the status then, while the install
// goes on (Watch's tools events follow it). Site media off is site_media_off; a failed
// install tools_unavailable (FailedPrecondition, the message names the cause).
func (m *Manager) InstallTools(ctx context.Context, update bool) (*downexv1.ToolsStatus, error) {
	if m.isClosed() {
		return nil, errShuttingDown()
	}
	t := m.cfg.Tools
	if t == nil {
		return nil, mediaError(CodeToolsUnavailable, connect.CodeUnimplemented, errNoTools)
	}
	if !m.Settings().Media.SiteMedia {
		return nil, mediaError(CodeSiteMediaOff, connect.CodeFailedPrecondition, sitemedia.ErrSiteMediaOff)
	}
	ictx := ctx
	if d, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		ictx, cancel = context.WithDeadline(ctx, d.Add(-siteDeadlineMargin))
		defer cancel()
	}
	snap, err := t.Install(ictx, update)
	switch {
	case err == nil:
		return protoToolsStatus(snap), nil
	case ctx.Err() != nil && isContextError(err):
		return nil, ctx.Err()
	case errors.Is(err, sitemedia.ErrToolsInstalling), isContextError(err) && !hasSiteSentinel(err):
		// The caller's time is up; the install goes on.
		return protoToolsStatus(t.Status()), nil
	case errors.Is(err, sitemedia.ErrSiteMediaOff):
		return nil, mediaError(CodeSiteMediaOff, connect.CodeFailedPrecondition, err)
	case !errors.Is(err, sitemedia.ErrToolsUnavailable):
		err = fmt.Errorf("%w: %w", sitemedia.ErrToolsUnavailable, err)
	}
	return nil, mediaError(CodeToolsUnavailable, connect.CodeFailedPrecondition, err)
}

// toolsEvent is a Watch event with the tools status, or nil without Config.Tools.
func (m *Manager) toolsEvent() *downexv1.Event {
	if m.cfg.Tools == nil {
		return nil
	}
	return &downexv1.Event{Kind: &downexv1.Event_Tools{Tools: protoToolsStatus(m.cfg.Tools.Status())}}
}

// protoToolsStatus renders a tools status; error texts are cut to one line of
// maxToolError bytes.
func protoToolsStatus(s ToolsSnapshot) *downexv1.ToolsStatus {
	out := &downexv1.ToolsStatus{Enabled: s.Enabled, Ready: s.Ready}
	if len(s.Tools) > 0 {
		out.Tools = make([]*downexv1.ToolInfo, len(s.Tools))
	}
	for i, t := range s.Tools {
		out.Tools[i] = &downexv1.ToolInfo{
			Name: t.Name, State: protoToolState(t.State), Version: t.Version, Path: t.Path, Managed: t.Managed,
			LatestVersion: t.LatestVersion, CheckedAt: protoTime(t.CheckedAt), BytesDone: max(t.BytesDone, 0),
			BytesTotal: max(t.BytesTotal, -1), Error: shortLine(t.Error, maxToolError), ErrorCode: t.ErrorCode,
			Verified: t.Verified,
		}
	}
	return out
}

// protoToolState maps a tool state to its API value.
func protoToolState(s ToolState) downexv1.ToolState {
	switch s {
	case ToolStateMissing:
		return downexv1.ToolState_TOOL_STATE_MISSING
	case ToolStateInstalling:
		return downexv1.ToolState_TOOL_STATE_INSTALLING
	case ToolStateReady:
		return downexv1.ToolState_TOOL_STATE_READY
	case ToolStateFailed:
		return downexv1.ToolState_TOOL_STATE_FAILED
	case ToolStateDisabled:
		return downexv1.ToolState_TOOL_STATE_DISABLED
	}
	return downexv1.ToolState_TOOL_STATE_UNSPECIFIED
}
