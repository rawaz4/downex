package main

import (
	"context"
	"encoding/json"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
)

// The site media bindings (docs/SITE-MEDIA-SPEC.md §5.4): the state of yt-dlp and Deno,
// which downexd downloads on first use, and their install or update (Settings → Video
// sites). Video pages themselves go through AddMedia and ProbeMedia with origin
// MEDIA_ORIGIN_SITE ({pageUrl, origin, headers?}, no manifestUrl), forwarded as they are:
// downexd validates them and answers within its wait budget (≤ 25 s, inside callTimeout),
// with tools_installing or site_resolving when the page should be asked for again.

// toolsInstallTimeout bounds InstallTools: downexd answers when the install ends, or with
// the progress so far at this deadline while the install goes on.
const toolsInstallTimeout = 15 * time.Minute

// GetTools returns DaemonService.GetTools: {status: {tools: [{name, state, version, path,
// managed, latestVersion, checkedAt, bytesDone, bytesTotal, error, errorCode, verified}],
// enabled, ready}}. A daemon without site media rejects with unimplemented.
func (a *App) GetTools() (json.RawMessage, error) {
	ctx, cancel := a.callContext()
	defer cancel()
	return reply(a.daemon.GetTools(ctx))
}

// InstallTools forwards an InstallToolsRequest ({update?}: true also looks for newer
// releases) and returns the InstallToolsResponse ({status}) when the install ends, or at
// toolsInstallTimeout with the progress so far. A failed install rejects with
// failed_precondition and the daemon's "tools_unavailable: …" text; the progress itself
// arrives as tools events meanwhile.
func (a *App) InstallTools(req json.RawMessage) (json.RawMessage, error) {
	in := &downexv1.InstallToolsRequest{}
	if err := decode(req, in); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(a.ctx, toolsInstallTimeout)
	defer cancel()
	a.log.Info("tools installation requested", "update", in.GetUpdate())
	return reply(a.daemon.InstallTools(ctx, in))
}
