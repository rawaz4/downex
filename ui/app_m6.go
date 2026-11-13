package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
)

// The M6 bindings (docs/M6-SPEC.md): queues and their scheduler, the post-queue action,
// secrets of the network settings, updates, and the file picker of the antivirus scanner.
// Arguments and results are protojson like every other binding.

const (
	// checkTimeout bounds CheckForUpdate: downexd fetches the manifest (≤ 1 MiB) and its
	// signature from the network.
	checkTimeout = 60 * time.Second
	// installTimeout bounds InstallUpdate: downexd downloads the whole installer first.
	installTimeout = 15 * time.Minute
)

// ListQueues returns QueueService.ListQueues: {queues: [...]}, the main queue first.
func (a *App) ListQueues() (json.RawMessage, error) {
	ctx, cancel := a.callContext()
	defer cancel()
	return reply(a.daemon.ListQueues(ctx))
}

// CreateQueue forwards a CreateQueueRequest ({queue: {name, maxConcurrent, speedLimit,
// schedule, postAction}}) and returns {queue: {...}} with the id the daemon gave it.
func (a *App) CreateQueue(req json.RawMessage) (json.RawMessage, error) {
	in := &downexv1.CreateQueueRequest{}
	if err := decode(req, in); err != nil {
		return nil, err
	}
	ctx, cancel := a.callContext()
	defer cancel()
	return reply(a.daemon.CreateQueue(ctx, in))
}

// UpdateQueue forwards an UpdateQueueRequest ({queue: {...}}: name, maxConcurrent,
// speedLimit, schedule and postAction are replaced; the order is changed with
// MoveDownloads) and returns {queue: {...}}.
func (a *App) UpdateQueue(req json.RawMessage) (json.RawMessage, error) {
	in := &downexv1.UpdateQueueRequest{}
	if err := decode(req, in); err != nil {
		return nil, err
	}
	ctx, cancel := a.callContext()
	defer cancel()
	return reply(a.daemon.UpdateQueue(ctx, in))
}

// DeleteQueue forwards a DeleteQueueRequest ({id}); its downloads move to the main queue.
func (a *App) DeleteQueue(req json.RawMessage) (json.RawMessage, error) {
	in := &downexv1.DeleteQueueRequest{}
	if err := decode(req, in); err != nil {
		return nil, err
	}
	ctx, cancel := a.callContext()
	defer cancel()
	return reply(a.daemon.DeleteQueue(ctx, in))
}

// StartQueue forwards a StartQueueRequest ({id}) and returns {queue: {...}}.
func (a *App) StartQueue(req json.RawMessage) (json.RawMessage, error) {
	in := &downexv1.StartQueueRequest{}
	if err := decode(req, in); err != nil {
		return nil, err
	}
	ctx, cancel := a.callContext()
	defer cancel()
	return reply(a.daemon.StartQueue(ctx, in))
}

// StopQueue forwards a StopQueueRequest ({id}; its running downloads are paused) and
// returns {queue: {...}}.
func (a *App) StopQueue(req json.RawMessage) (json.RawMessage, error) {
	in := &downexv1.StopQueueRequest{}
	if err := decode(req, in); err != nil {
		return nil, err
	}
	ctx, cancel := a.callContext()
	defer cancel()
	return reply(a.daemon.StopQueue(ctx, in))
}

// MoveDownloads forwards a MoveDownloadsRequest ({ids, queueId, position}: position is
// the 0-based insertion index in the target queue, -1 the end) and returns {queue: {...}},
// the target queue with its new order.
func (a *App) MoveDownloads(req json.RawMessage) (json.RawMessage, error) {
	in := &downexv1.MoveDownloadsRequest{}
	if err := decode(req, in); err != nil {
		return nil, err
	}
	ctx, cancel := a.callContext()
	defer cancel()
	return reply(a.daemon.MoveDownloads(ctx, in))
}

// CancelPostAction forwards a CancelPostActionRequest ({queueId}): the countdown of that
// queue's post-queue action stops, and nothing is done.
func (a *App) CancelPostAction(req json.RawMessage) (json.RawMessage, error) {
	in := &downexv1.CancelPostActionRequest{}
	if err := decode(req, in); err != nil {
		return nil, err
	}
	ctx, cancel := a.callContext()
	defer cancel()
	return reply(a.daemon.CancelPostAction(ctx, in))
}

// SetSecret forwards a SetSecretRequest ({kind: "proxy" | "site_login", id, password}):
// downexd keeps the password in the system's credential store. The request is never
// logged, and a malformed one is refused without repeating any of it.
func (a *App) SetSecret(req json.RawMessage) (json.RawMessage, error) {
	in := &downexv1.SetSecretRequest{}
	if err := decodeSecret(req, in); err != nil {
		return nil, err
	}
	ctx, cancel := a.callContext()
	defer cancel()
	return reply(a.daemon.SetSecret(ctx, in))
}

// ClearSecret forwards a ClearSecretRequest ({kind, id}): the stored password is removed.
func (a *App) ClearSecret(req json.RawMessage) (json.RawMessage, error) {
	in := &downexv1.ClearSecretRequest{}
	if err := decode(req, in); err != nil {
		return nil, err
	}
	ctx, cancel := a.callContext()
	defer cancel()
	return reply(a.daemon.ClearSecret(ctx, in))
}

// CheckForUpdate returns DaemonService.CheckForUpdate: {info: {currentVersion,
// latestVersion, available, notes, publishedAt, checkedAt}} for the feed in the saved
// settings.
func (a *App) CheckForUpdate() (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(a.ctx, checkTimeout)
	defer cancel()
	return reply(a.daemon.CheckForUpdate(ctx))
}

// InstallUpdate returns DaemonService.InstallUpdate: downexd downloads and verifies the
// update, starts its installer and exits for it ({info: {...}}). The page asks the user
// first.
func (a *App) InstallUpdate() (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(a.ctx, installTimeout)
	defer cancel()
	a.log.Info("update installation requested")
	return reply(a.daemon.InstallUpdate(ctx))
}

// pickFileRequest is the argument of PickFile.
type pickFileRequest struct {
	Title      string `json:"title"`
	InitialDir string `json:"initialDir"`
}

// PickFile shows the native file picker ({title?, initialDir?}) and returns the chosen
// absolute file, or "" when the user cancels (Settings → Integration: the scanner).
func (a *App) PickFile(req json.RawMessage) (string, error) {
	var in pickFileRequest
	if !isEmptyJSON(req) {
		dec := json.NewDecoder(bytes.NewReader(req))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			return "", apiError(connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("malformed request: %w", err)))
		}
	}
	h := a.runtimeHost()
	if h == nil {
		return "", apiError(errNoWindow())
	}
	title := in.Title
	if title == "" || len(title) > 200 {
		title = "Choose a file"
	}
	path, err := h.PickFile(title, existingDir(in.InitialDir))
	if err != nil {
		return "", apiError(connect.NewError(connect.CodeInternal, fmt.Errorf("file picker: %w", err)))
	}
	return path, nil
}

// decodeSecret is decode for a request that carries a password: the error never quotes
// the input (protojson's messages may repeat the offending value).
func decodeSecret(raw json.RawMessage, m *downexv1.SetSecretRequest) error {
	if isEmptyJSON(raw) {
		return nil
	}
	if err := unmarshalOptions.Unmarshal(raw, m); err != nil {
		m.Reset()
		return apiError(connect.NewError(connect.CodeInvalidArgument, errors.New("malformed request")))
	}
	return nil
}
