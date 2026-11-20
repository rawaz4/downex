package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"downex/internal/dlock"
	"downex/internal/engine"
	"downex/internal/filecheck"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/store"
	"downex/internal/update"
)

// Add validates r (docs/DAEMON-SPEC.md §4), records the download and returns at once.
//
// The category is chosen from the requested filename, the URL and the MIME hint
// (docs/DESKTOP-SPEC.md §2); without a dir the download goes into its category's folder
// when use_category_folders is set (created when the download starts), else into
// default_dir. A missing requested directory is created when its parent exists.
// max_connections 0 stores the current default_max_connections setting.
//
// A browser hand-off (source ADD_SOURCE_BROWSER) while confirm_browser_downloads is set
// is recorded as awaiting confirmation with a proposed name (start_paused does not
// apply) and nothing is created on disk; the confirmation UI is asked to show it. That
// needs a way to ask the user (confirmer.canAsk: the confirmation window, or a notice
// subscriber that shows notices, see ShowNotices); without one the hand-off is treated
// like any other add. A URL from the clipboard monitor (ADD_SOURCE_CLIPBOARD) is
// treated as a browser hand-off. Everything else is queued, or paused with
// start_paused; the scheduler starts it.
//
// The download joins queue queue_id (0 = the main queue; one that does not exist is
// InvalidArgument) at its end (docs/M6-SPEC.md §1): a queued download starts only while
// that queue runs.
func (m *Manager) Add(ctx context.Context, r *downexv1.AddRequest) (*downexv1.Download, error) {
	m.mu.Lock()
	closed, s := m.closed, m.settings
	m.mu.Unlock()
	if closed {
		return nil, errShuttingDown()
	}
	in, err := parseAdd(r, s.DefaultDir)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if in.pageURL, err = parsePageURL(r.GetPageUrl()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	queue, err := m.queueForAdd(r.GetQueueId(), model.MainQueueID)
	if err != nil {
		return nil, err
	}
	category := chooseCategory(s.Categories, in.filename, in.req.URL, in.mime)
	awaiting := handOff(in.source) && s.ConfirmBrowserDownloads
	unasked := awaiting && !m.confirm.canAsk() // nothing would ever show it: queue it
	awaiting = awaiting && !unasked
	fromSettings := false // in.dir is a configured category folder, created on demand
	if !in.dirGiven && s.UseCategoryFolders {
		if dir, ok := s.categoryFolder(category); ok {
			in.dir, fromSettings = dir, true
		}
	}
	switch {
	case fromSettings: // trusted: created with its parents when the download starts
		if err := checkTrustedDir(in.dir); err != nil {
			return nil, err
		}
	case awaiting: // nothing is created before the confirmation
		if err := checkCreatable(in.dir); err != nil {
			return nil, err
		}
	default:
		if err := ensureDir(in.dir); err != nil {
			return nil, err
		}
	}
	maxConn := in.maxConn
	if maxConn == 0 {
		maxConn = s.DefaultMaxConnections
	}
	var d *model.Download
	if awaiting {
		d, err = m.addAwaiting(ctx, in, category, maxConn, s.Categories, queue)
	} else {
		d, err = m.addQueued(ctx, in, category, maxConn, queue)
	}
	if err != nil {
		return nil, err
	}

	// A new link may continue a download whose link expired (renew.go): the add is held
	// while that is checked.
	pd := m.indexAdded(d, awaiting, m.renewCandidates(in, s))
	m.log.Info("download added", "download", int64(d.ID), "url", redactURL(d.Request.URL),
		"method", d.Request.Method, "headers", len(d.Request.Headers), "dir", d.Dir, "state", d.State.String(),
		"source", d.Source.String(), "category", d.Category, "queue", int64(d.QueueID))
	if unasked {
		m.log.Info("browser download queued without confirmation: nothing can ask the user (no confirmation "+
			"window next to downexd, and no notification-area icon shows notifications)", "download", int64(d.ID))
	}
	return pd, nil
}

// indexAdded indexes the download d just created by Add or AddMedia, queues it when it
// is queued, publishes it and, when it awaits confirmation, asks the confirmation UI to
// show it. With candidates (downloads whose link expired that its link may continue) it
// is held instead while that is checked (renew.go): runRenewCheck then does the rest.
// It returns d as rendered for clients.
func (m *Manager) indexAdded(d *model.Download, awaiting bool, candidates []model.DownloadID) *downexv1.Download {
	m.mu.Lock()
	e := m.entries[d.ID]
	if e == nil {
		e = newEntry(*d)
		m.entries[d.ID] = e
	} else { // a concurrent List adopted the new record before it was indexed here
		e.rec, e.foreign, e.received, e.errCode = *d, false, 0, ""
	}
	m.joinQueueLocked(e, d.QueueID)
	held := len(candidates) > 0 && !m.closed
	switch {
	case held:
		m.holdLocked(e, candidates, awaiting)
	case d.State == model.StateQueued && !m.closed:
		m.enqueueLocked(d.ID)
	}
	m.scheduleLocked()
	ask := awaiting && !m.closed && !held
	// A hand-off that starts at once shows its progress window (§7); one that waits in
	// the queue does not (it starts later).
	show := !awaiting && !held && handOff(d.Source) && e.run != nil && !m.closed &&
		m.settings.ShowProgressWindow && m.cfg.OpenProgress != nil
	if show {
		m.markWindowedLocked(e)
	}
	pd := m.protoLocked(e)
	// A browser add that opens neither shows nothing: never the main window (§7 "Above
	// the browser").
	if ask || show {
		m.wg.Add(1)
	}
	m.mu.Unlock()
	m.hub.publish(d.ID, kindUpdated)
	switch {
	case ask:
		go func() {
			defer m.wg.Done()
			m.confirm.request(pd)
		}()
	case show:
		go func() {
			defer m.wg.Done()
			if err := m.openProgressFor(int64(d.ID), d.Source); err != nil {
				m.log.Warn("cannot open the progress window", "download", int64(d.ID), "err", err)
			}
		}()
	}
	return pd
}

// addQueued records a download through the engine (which reserves a requested name)
// as queued, or paused with start_paused, with the request's hints, in queue queue.
func (m *Manager) addQueued(ctx context.Context, in addInput, category string, maxConn int, queue model.QueueID) (*model.Download, error) {
	d, err := m.eng.Add(ctx, in.req, in.dir, in.filename, maxConn, in.speedLimit)
	if err != nil {
		return nil, errAdd(err)
	}
	// The engine creates it in the main queue; the store moves it before the scheduler
	// knows it.
	if queue != model.MainQueueID {
		if err := m.qstore.MoveDownloads(context.WithoutCancel(ctx), []model.DownloadID{d.ID}, queue, -1); err != nil {
			m.log.Warn("cannot put the new download into its queue; it stays in the main queue", "download", int64(d.ID),
				"queue", int64(queue), "err", err)
		} else {
			d.QueueID = queue
		}
	}
	d.Source, d.ExpectedSize, d.MIME, d.Category = in.source, in.expectedSize, in.mime, category
	d.PageURL = in.pageURL
	// A requested name is the one the engine keeps; without one the engine notes the
	// controls of the name it chooses.
	d.NameControls = in.filename != "" && in.nameControls
	if in.startPaused {
		d.State = model.StatePaused
	}
	if d.State != model.StateQueued || d.Source != model.SourceUnknown || d.ExpectedSize != 0 || d.MIME != "" || d.Category != "" ||
		d.NameControls || d.PageURL != "" {
		d.UpdatedAt = time.Now()
		if err := m.st.UpdateDownload(context.WithoutCancel(ctx), d); err != nil {
			// The record exists as the engine created it: queued, without the hints.
			m.log.Warn("cannot store the new download's details; it is queued without them", "download", int64(d.ID), "err", err)
			d.State, d.Source, d.ExpectedSize, d.MIME, d.Category = model.StateQueued, model.SourceUnknown, 0, "", ""
			d.NameControls, d.PageURL = false, ""
		}
	}
	return d, nil
}

// addAwaiting records a browser hand-off awaiting confirmation, in one store write so
// that it is never seen queued. Its proposed name (see proposedName) is reserved in the
// store only: no folder or part file is created before the user confirms.
func (m *Manager) addAwaiting(ctx context.Context, in addInput, category string, maxConn int, cats []Category,
	queue model.QueueID) (*model.Download, error) {
	name := proposedName(in.filename, in.req.URL, cats)
	if name != "" {
		var err error
		if name, err = m.freeName(ctx, in.dir, name, 0); err != nil {
			return nil, err
		}
	}
	now := time.Now()
	d := &model.Download{
		Request: in.req, Dir: in.dir, Filename: name, Size: -1, Mode: model.ModeUnknown,
		State: model.StateAwaitingConfirmation, MaxConnections: min(max(maxConn, 0), MaxConnectionsLimit),
		SpeedLimit: in.speedLimit, CreatedAt: now, UpdatedAt: now,
		Source: in.source, ExpectedSize: in.expectedSize, MIME: in.mime, Category: category, QueueID: queue,
		NameControls: name != "" && in.nameControls, PageURL: in.pageURL,
	}
	if err := m.st.CreateDownload(ctx, d); err != nil {
		return nil, errAdd(fmt.Errorf("create download: %w", err))
	}
	return d, nil
}

// Get returns one download.
func (m *Manager) Get(ctx context.Context, id int64) (*downexv1.Download, error) {
	e, err := m.lookup(ctx, model.DownloadID(id))
	if err != nil {
		return nil, err
	}
	return m.current(e), nil
}

// List returns every download in the store ordered by id, with live progress for the
// running ones. Downloads created by another process since the daemon started are
// included as stored.
func (m *Manager) List(ctx context.Context) ([]*downexv1.Download, error) {
	recs, err := m.st.ListDownloads(ctx)
	if err != nil {
		return nil, errStore(ctx, "list downloads", err)
	}
	var maxID model.DownloadID
	var unowned []int
	m.mu.Lock()
	for i := range recs {
		maxID = max(maxID, recs[i].ID)
		if e := m.entries[recs[i].ID]; e == nil || e.foreign {
			unowned = append(unowned, i)
		}
	}
	m.mu.Unlock()
	received := make(map[model.DownloadID]int64, len(unowned))
	for _, i := range unowned {
		n, err := m.storedBytes(ctx, &recs[i])
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, errStore(ctx, "load download", err)
		}
		received[recs[i].ID] = n
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*downexv1.Download, 0, len(recs))
	seen := make(map[model.DownloadID]bool, len(recs))
	for i := range recs {
		seen[recs[i].ID] = true
		if e := m.adoptLocked(&recs[i], received[recs[i].ID]); e != nil {
			out = append(out, m.protoLocked(e))
		}
	}
	// Downloads added after the store was listed (ids only grow).
	var added []*entry
	for id, e := range m.entries {
		if !seen[id] && id > maxID && !e.foreign {
			added = append(added, e)
		}
	}
	slices.SortFunc(added, func(a, b *entry) int { return cmp.Compare(a.id, b.id) })
	for _, e := range added {
		out = append(out, m.protoLocked(e))
	}
	return out, nil
}

// Pause stops a running download, committing its progress, or keeps a queued one
// from starting. Pausing a paused download is a no-op; a completed or failed one, or
// one awaiting confirmation (see Confirm), is FailedPrecondition. It returns once the
// download has stopped.
func (m *Manager) Pause(ctx context.Context, id int64) (*downexv1.Download, error) {
	m.endCheck(model.DownloadID(id), false)
	e, err := m.acquire(ctx, model.DownloadID(id))
	if err != nil {
		return nil, err
	}
	defer releaseOp(e)

	m.mu.Lock()
	if err := m.settledLocked(ctx, e); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if r := e.run; r != nil {
		r.setIntent(intentPause)
		r.cancel()
		m.mu.Unlock()
		if err := waitDone(ctx, r.done); err != nil {
			return nil, err
		}
		return m.current(e), nil
	}
	switch st := e.rec.State; st {
	case model.StatePaused, model.StateCanceled:
		pd := m.protoLocked(e)
		m.mu.Unlock()
		return pd, nil
	case model.StateCompleted, model.StateFailed:
		m.mu.Unlock()
		return nil, errPrecondition("download %d is %s", id, st)
	case model.StateAwaitingConfirmation:
		m.mu.Unlock()
		return nil, errAwaiting(e.id)
	}
	wasQueued := m.parkLocked(e)
	rec := e.rec
	m.mu.Unlock()

	rec.State, rec.UpdatedAt = model.StatePaused, time.Now()
	if err := m.st.UpdateDownload(context.WithoutCancel(ctx), &rec); err != nil {
		m.restore(e, wasQueued)
		return nil, errInternal("pause download", err)
	}
	m.mu.Lock()
	e.rec = rec
	m.unparkLocked(e) // paused: no longer work of its queue, which this never finishes
	pd := m.protoLocked(e)
	m.mu.Unlock()
	m.hub.publish(e.id, kindUpdated)
	return pd, nil
}

// Resume queues a paused, failed or canceled download; the scheduler starts it. One
// awaiting confirmation is FailedPrecondition (see Confirm).
// With restart its saved progress and validators are discarded first (after the
// remote file changed); a running download is stopped, restarted and queued. A
// running download that a Pause is still stopping is queued again once it stopped;
// one that a Remove is stopping is FailedPrecondition.
// max_connections and speed_limit, when set, are stored; for a running download they
// apply as with SetDownloadOptions (the speed limit at once) and are stored when the run
// stops. A completed download is resumed only with restart: it is downloaded again, and
// existing_file says what happens to its file (docs/ENGINE-SPEC.md §7.1; without restart
// FailedPrecondition). existing_file without restart is InvalidArgument; for a download
// that is not completed it is ignored.
func (m *Manager) Resume(ctx context.Context, r *downexv1.ResumeRequest) (*downexv1.Download, error) {
	id := model.DownloadID(r.GetId())
	if r.GetExistingFile() != downexv1.ExistingFile_EXISTING_FILE_UNSPECIFIED && !r.GetRestart() {
		return nil, errInvalid("existing_file is only for a restart")
	}
	if r.GetRescueStart() && r.GetRestart() {
		return nil, errInvalid("rescue_start cannot be combined with restart")
	}
	switch r.GetExistingFile() {
	case downexv1.ExistingFile_EXISTING_FILE_UNSPECIFIED, downexv1.ExistingFile_EXISTING_FILE_KEEP_BOTH,
		downexv1.ExistingFile_EXISTING_FILE_REPLACE:
	default:
		return nil, errInvalid("existing_file %d is not known", r.GetExistingFile())
	}
	var maxConn *int
	if r.MaxConnections != nil {
		n, err := checkConnections(r.GetMaxConnections())
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		maxConn = &n
	}
	var speed *int64
	if r.SpeedLimit != nil {
		v := r.GetSpeedLimit()
		if v < 0 {
			return nil, errInvalid("speed_limit must not be negative")
		}
		speed = &v
	}
	m.endCheck(id, false)
	e, err := m.acquire(ctx, id)
	if err != nil {
		return nil, err
	}
	defer releaseOp(e)

	m.mu.Lock()
	if err := m.settledLocked(ctx, e); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if m.closed {
		m.mu.Unlock()
		return nil, errShuttingDown()
	}
	// The first start of a rescue stored paused for the browser relay (rescue.go): only
	// such a rescue, once, within RescueStartWindow (docs/DAEMON-SPEC.md §3 "Rescue").
	rescueStart := r.GetRescueStart()
	if rescueStart {
		waited := !e.rescueWait.IsZero() && m.now().Sub(e.rescueWait) <= RescueStartWindow
		if !waited || e.run != nil || e.rec.State != model.StatePaused {
			m.mu.Unlock()
			return nil, errPrecondition("download %d is not a rescue waiting for its first start", id)
		}
	}
	e.rescueWait = time.Time{}
	if maxConn != nil && *maxConn == 0 {
		v := m.settings.DefaultMaxConnections
		maxConn = &v
	}
	if run := e.run; run != nil {
		if speed != nil {
			run.lim.SetLimit(*speed)
			e.pendSpeed = speed
		}
		if maxConn != nil {
			e.pendMax = maxConn
		}
		live := maxConn != nil && e.rec.Kind == model.KindFile && !r.GetRestart()
		switch {
		case run.intent == intentRemove:
			// A Remove whose call ended before the run stopped is still pending; it is
			// final.
			m.mu.Unlock()
			return nil, errPrecondition("download %d is being removed", id)
		case r.GetRestart():
		case run.intent == intentPause:
			// A Pause whose call ended (canceled, timed out) before the run stopped is
			// still pending: replace it, so that the run is queued again once it has
			// stopped, instead of reporting success and leaving the download paused.
			run.setIntent(intentResume)
			fallthrough
		default:
			pd := m.protoLocked(e)
			m.mu.Unlock()
			if live {
				m.applyConns(e.id, *maxConn)
			}
			m.hub.publish(e.id, kindUpdated)
			return pd, nil
		}
		run.setIntent(intentRestart)
		run.cancel()
		m.mu.Unlock()
		if err := waitDone(ctx, run.done); err != nil {
			return nil, err
		}
		return m.current(e), nil
	}
	switch e.rec.State {
	case model.StateCompleted:
		if !r.GetRestart() {
			m.mu.Unlock()
			return nil, errPrecondition("download %d is already completed", id)
		}
	case model.StateAwaitingConfirmation:
		m.mu.Unlock()
		return nil, errAwaiting(id)
	}
	wasQueued := m.parkLocked(e)
	rec, received := e.rec, e.received
	m.mu.Unlock()

	sctx := context.WithoutCancel(ctx)
	if r.GetRestart() {
		lk, err := m.lockForChange(id)
		if err != nil {
			m.restore(e, wasQueued)
			return nil, err
		}
		opts := engine.RestartOptions{
			Completed: rec.State == model.StateCompleted,
			Replace:   r.GetExistingFile() == downexv1.ExistingFile_EXISTING_FILE_REPLACE,
		}
		err = m.restartDownload(sctx, id, opts)
		lk.Release()
		if err != nil {
			m.restore(e, wasQueued)
			if errors.Is(err, engine.ErrInvalidRequest) || errors.Is(err, engine.ErrAlreadyRunning) {
				return nil, connect.NewError(connect.CodeFailedPrecondition, err)
			}
			return nil, errInternal("restart download", err)
		}
		fresh, err := m.st.GetDownload(sctx, id)
		if err != nil {
			m.restore(e, wasQueued)
			return nil, errInternal("restart download", err)
		}
		rec, received = *fresh, 0
	}
	dirty := rec.State != model.StateQueued || rec.Error != ""
	rec.State, rec.Error = model.StateQueued, ""
	if maxConn != nil && rec.MaxConnections != *maxConn {
		rec.MaxConnections, dirty = *maxConn, true
	}
	if speed != nil && rec.SpeedLimit != *speed {
		rec.SpeedLimit, dirty = *speed, true
	}
	if dirty {
		rec.UpdatedAt = time.Now()
		if err := m.st.UpdateDownload(sctx, &rec); err != nil {
			m.restore(e, wasQueued)
			return nil, errInternal("resume download", err)
		}
	}
	m.mu.Lock()
	e.rec, e.received, e.errCode = rec, received, ""
	if !m.closed {
		m.enqueueLocked(e.id)
	}
	m.unparkLocked(e)
	m.scheduleLocked()
	pd := m.protoLocked(e)
	// A browser rescue's first start shows its progress window when it starts at once,
	// as a browser hand-off does (docs/DESKTOP-SPEC.md §7); one that waits in its queue
	// does not.
	show := rescueStart && e.run != nil && !m.closed && m.settings.ShowProgressWindow && m.cfg.OpenProgress != nil
	if show {
		m.wg.Add(1)
	}
	m.mu.Unlock()
	m.hub.publish(e.id, kindUpdated)
	if show {
		go func() {
			defer m.wg.Done()
			if err := m.openProgressFor(int64(e.id), model.SourceBrowser); err != nil {
				m.log.Warn("cannot open the progress window", "download", int64(e.id), "err", err)
			}
		}()
	}
	return pd, nil
}

// SetDownloadOptions changes a download's speed limit (bytes/s, 0 = unlimited),
// connection limit (1 to MaxConnectionsLimit; 0 stores the current
// default_max_connections) and completion_dialog mark, each only when set, validated
// like Add's (docs/DESKTOP-SPEC.md §7, §8). A stopped download stores them for its next
// run (a queued one leaves the queue while they are written, so that it starts with
// them). For a running download the speed limit applies at once (its limiter), the
// connection limit as soon as the engine can (fewer connections stop the extra workers
// at a safe point; more let the ramp-up add workers; a media download takes it on its
// next start), and all are stored when the run stops, since only the run writes its
// record meanwhile; clients see the new values at once, and the completion or failure
// of that run already follows the new mark. Limits for a completed download are
// FailedPrecondition (its completion_dialog alone may still be set), as is any change
// to one that a Remove is stopping.
func (m *Manager) SetDownloadOptions(ctx context.Context, r *downexv1.SetDownloadOptionsRequest) (*downexv1.Download, error) {
	id := model.DownloadID(r.GetId())
	var maxConn *int
	if r.MaxConnections != nil {
		n, err := checkConnections(r.GetMaxConnections())
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		maxConn = &n
	}
	var speed *int64
	if r.SpeedLimit != nil {
		v := r.GetSpeedLimit()
		if v < 0 {
			return nil, errInvalid("speed_limit must not be negative")
		}
		speed = &v
	}
	var dialog *bool
	if r.CompletionDialog != nil {
		v := r.GetCompletionDialog()
		dialog = &v
	}
	m.endCheck(id, false)
	e, err := m.acquire(ctx, id)
	if err != nil {
		return nil, err
	}
	defer releaseOp(e)

	m.mu.Lock()
	if err := m.settledLocked(ctx, e); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if m.closed {
		m.mu.Unlock()
		return nil, errShuttingDown()
	}
	if maxConn != nil && *maxConn == 0 {
		v := m.settings.DefaultMaxConnections
		maxConn = &v
	}
	if run := e.run; run != nil {
		if run.intent == intentRemove {
			m.mu.Unlock()
			return nil, errPrecondition("download %d is being removed", id)
		}
		if speed != nil {
			run.lim.SetLimit(*speed)
			e.pendSpeed = speed
		}
		live := maxConn != nil && e.rec.Kind == model.KindFile
		if maxConn != nil {
			e.pendMax = maxConn
		}
		if dialog != nil {
			e.pendDialog = dialog
			if *dialog { // its progress window is open: the run's outcome shows there
				run.windowed = true
			}
		}
		pd := m.protoLocked(e)
		m.mu.Unlock()
		if live {
			m.applyConns(e.id, *maxConn)
		}
		m.hub.publish(e.id, kindUpdated)
		m.log.Info("download options changed", "download", int64(id), "running", true,
			"speed_limit", pd.GetSpeedLimit(), "max_connections", pd.GetMaxConnections(),
			"completion_dialog", pd.GetCompletionDialog())
		return pd, nil
	}
	if e.rec.State == model.StateCompleted && (maxConn != nil || speed != nil) {
		m.mu.Unlock()
		return nil, errPrecondition("download %d is already completed", id)
	}
	rec := e.rec
	dirty := false
	if maxConn != nil && rec.MaxConnections != *maxConn {
		rec.MaxConnections, dirty = *maxConn, true
	}
	if speed != nil && rec.SpeedLimit != *speed {
		rec.SpeedLimit, dirty = *speed, true
	}
	if dialog != nil && rec.CompletionDialog != *dialog {
		rec.CompletionDialog, dirty = *dialog, true
	}
	if !dirty {
		pd := m.protoLocked(e)
		m.mu.Unlock()
		return pd, nil
	}
	// Out of the queue while the record is written, as with Pause and Resume: otherwise
	// the scheduler could start it meanwhile with the old options, and its run would
	// then write its own copy over the new ones. Its queue keeps counting it as work.
	wasQueued := m.parkLocked(e)
	m.mu.Unlock()

	rec.UpdatedAt = time.Now()
	if err := m.st.UpdateDownload(context.WithoutCancel(ctx), &rec); err != nil {
		m.restore(e, wasQueued)
		return nil, errInternal("change download options", err)
	}
	m.mu.Lock()
	e.rec = rec
	if wasQueued && m.entries[e.id] == e && e.run == nil && !m.closed {
		m.enqueueLocked(e.id)
		m.scheduleLocked()
	}
	m.unparkLocked(e)
	pd := m.protoLocked(e)
	m.mu.Unlock()
	m.hub.publish(e.id, kindUpdated)
	m.log.Info("download options changed", "download", int64(id), "running", false,
		"speed_limit", rec.SpeedLimit, "max_connections", rec.MaxConnections, "completion_dialog", rec.CompletionDialog)
	return pd, nil
}

// Remove pauses a running download, deletes its record and, with deleteFile, its
// file: the completed file, or the .dxpart of an unfinished download. A media
// download's part directory (<name>.dxparts, Downex's own fragments, which nothing can
// resume without the record) goes in either case. The files are deleted first, so a
// failure keeps the record and Remove can be retried; without deleteFile a part
// directory that cannot be deleted is only logged.
func (m *Manager) Remove(ctx context.Context, id int64, deleteFile bool) error {
	m.endCheck(model.DownloadID(id), true)
	e, err := m.acquire(ctx, model.DownloadID(id))
	if err != nil {
		return err
	}
	defer releaseOp(e)
	return m.removeAcquired(ctx, e, deleteFile)
}

// removeAcquired is Remove of e, whose operation slot the caller holds.
func (m *Manager) removeAcquired(ctx context.Context, e *entry, deleteFile bool) error {
	id := int64(e.id)
	m.mu.Lock()
	if err := m.settledLocked(ctx, e); err != nil {
		m.mu.Unlock()
		return err
	}
	if m.closed {
		m.mu.Unlock()
		return errShuttingDown()
	}
	if r := e.run; r != nil {
		// The run applies the removal intent: it is left paused and never rescheduled.
		r.setIntent(intentRemove)
		r.cancel()
		m.mu.Unlock()
		if err := waitDone(ctx, r.done); err != nil {
			return err
		}
		m.mu.Lock()
		if m.entries[e.id] != e { // its record vanished while it stopped
			m.mu.Unlock()
			return nil
		}
	}
	wasQueued := m.parkLocked(e)
	rec := e.rec
	m.mu.Unlock()

	lk, err := m.lockForChange(e.id)
	if err != nil {
		m.restore(e, wasQueued)
		return err
	}
	if path := filePath(&rec); deleteFile && path != "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			lk.Release()
			m.restore(e, wasQueued)
			return errPrecondition("delete %s (download %d was kept): %v", path, id, err)
		}
	}
	if rec.Kind == model.KindMedia && rec.Filename != "" {
		// The fragments of an unfinished media download (or the leftovers of a
		// completed one whose cleanup failed): without the record nothing resumes them.
		parts := MediaPartsDir(rec.Dir, rec.Filename)
		if err := os.RemoveAll(parts); err != nil {
			if deleteFile {
				lk.Release()
				m.restore(e, wasQueued)
				return errPrecondition("delete %s (download %d was kept): %v", parts, id, err)
			}
			m.log.Warn("cannot delete the part directory of a removed media download", "download", id, "err", err)
		}
	}
	if err := m.st.DeleteDownload(context.WithoutCancel(ctx), e.id); err != nil && !errors.Is(err, store.ErrNotFound) {
		lk.Release()
		m.restore(e, wasQueued)
		return errInternal("remove download", err)
	}
	lk.Discard()
	// A site download's sign-in lives for its runs only (docs/SITE-MEDIA-SPEC.md §6.3).
	m.siteCookies.set(e.id, siteSignIn{})
	m.browser.Forget(e.id) // and its fetch ticket (docs/SITE-MEDIA-SPEC.md §2.8)
	m.mu.Lock()
	m.unparkLocked(e)
	m.forgetLocked(e.id) // its queue's work shrinks; a removal never finishes the queue
	m.dropOffersLocked(e.id)
	m.mu.Unlock()
	m.hub.publish(e.id, kindRemoved)
	m.log.Info("download removed", "download", id, "file_deleted", deleteFile)
	return nil
}

// Status reports the daemon's identity and queue counters, its folders (only absolute
// ones: the data folder is that of Config.SettingsPath) and whether this build can
// install updates (a usable update signing key: UpdatesConfig.PublicKey, else
// update.PublicKey).
func (m *Manager) Status() *downexv1.StatusResponse {
	m.mu.Lock()
	active, queued := m.running, len(m.waiting)
	m.mu.Unlock()
	dataDir := ""
	if m.cfg.SettingsPath != "" {
		dataDir = filepath.Dir(m.cfg.SettingsPath)
	}
	return &downexv1.StatusResponse{
		Version:          m.cfg.Version,
		Pid:              int32(os.Getpid()),
		StartedAt:        timestamppb.New(m.started),
		Active:           int32(active),
		Queued:           int32(queued),
		Endpoint:         m.cfg.Endpoint,
		DataDir:          absoluteOnly(dataDir),
		LogDir:           absoluteOnly(m.cfg.LogDir),
		ToolsDir:         absoluteOnly(m.cfg.ToolsDir),
		UpdatesSupported: m.updatesSupported(),
		Polite:           m.politeProto(),
	}
}

// Active reports how many downloads are running now (Status's Active) and how many runs
// have started since the Manager started, without building a status: downexd asks it every
// minute to know whether it is idle or worked since it last asked, a download shorter than
// that included (platform.ReleaseMemoryAndTrimWhileIdle).
func (m *Manager) Active() (running int, started uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running, m.runs
}

// updatesSupported reports whether this daemon can verify updates: the key of its update
// component (StartUpdates), else the one built in (update.PublicKey), parses.
func (m *Manager) updatesSupported() bool {
	updateServices.mu.Lock()
	u := updateServices.m[m]
	updateServices.mu.Unlock()
	key := update.PublicKey
	if u != nil && u.cfg.PublicKey != "" {
		key = u.cfg.PublicKey
	}
	_, err := update.ParsePublicKey(key)
	return err == nil
}

// absoluteOnly returns dir when it is an absolute path, else "" (not known).
func absoluteOnly(dir string) string {
	if dir == "" || !filepath.IsAbs(dir) {
		return ""
	}
	return filepath.Clean(dir)
}

// Settings returns (a copy of) the effective settings.
func (m *Manager) Settings() Settings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings.Clone()
}

// UpdateSettings replaces the settings (zero max_concurrent_downloads and
// default_max_connections select their defaults, an empty default_dir the Downloads
// folder; categories are validated, see normalizeCategories), persists them and applies
// them at once: the global speed limit changes live; a lower max_concurrent_downloads
// requeues running downloads (see rebalanceLocked; queues without a limit of their own
// follow it), a higher one starts more; a changed launch_at_login is applied with Config.LaunchAtLogin
// before anything is saved (its failure fails the update). default_max_connections,
// categories and the folder policy apply to downloads added later. Subscribers get a
// NoticeSettings when anything changed.
func (m *Manager) UpdateSettings(ctx context.Context, p *downexv1.Settings) (Settings, error) {
	if p == nil {
		return Settings{}, errInvalid("settings are required")
	}
	m.settingsMu.Lock()
	defer m.settingsMu.Unlock()
	if m.isClosed() {
		return Settings{}, errShuttingDown()
	}
	next, err := settingsFromProto(p).normalize(m.cfg.DownloadsDir)
	if err != nil {
		return Settings{}, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := ctx.Err(); err != nil {
		return Settings{}, err
	}
	m.mu.Lock()
	prev := m.settings
	m.mu.Unlock()
	login := next.LaunchAtLogin != prev.LaunchAtLogin && m.cfg.LaunchAtLogin != nil
	if login {
		if err := m.cfg.LaunchAtLogin(next.LaunchAtLogin); err != nil {
			if errors.Is(err, errors.ErrUnsupported) {
				return Settings{}, errPrecondition("launch_at_login is not supported on this system")
			}
			return Settings{}, errInternal("apply launch_at_login", err)
		}
	}
	if m.cfg.SettingsPath != "" {
		if err := saveSettings(m.cfg.SettingsPath, next); err != nil {
			if login {
				if rerr := m.cfg.LaunchAtLogin(prev.LaunchAtLogin); rerr != nil {
					m.log.Warn("cannot restore the launch-at-login entry", "err", rerr)
				}
			}
			return Settings{}, errInternal("save settings", err)
		}
	}
	m.mu.Lock()
	m.settings = next
	m.global.SetLimit(next.GlobalSpeedLimit)
	m.rebalanceLocked()
	m.scheduleLocked()
	if prev.Desktop.CheckFiles != next.Desktop.CheckFiles {
		// Every download's file_check appears or goes (docs/DESKTOP-SPEC.md §9.9): Watch
		// sends each one again, so no window keeps an outdated warning.
		for id := range m.entries {
			m.hub.publish(id, kindUpdated)
		}
	}
	m.mu.Unlock()
	if next.Polite != prev.Polite {
		m.configurePolite(prev.Polite, next.Polite) // off removes the cap at once (POLITE-SPEC rule 15)
	}
	if !(Settings{Network: prev.Network}).Equal(Settings{Network: next.Network}) {
		// The proxy, credentials and host rules apply to requests made from now on.
		m.networkChanged(prev.Network, next.Network)
	}
	if next.DefaultMaxConnections != prev.DefaultMaxConnections {
		m.followDefaultConns(ctx, prev.DefaultMaxConnections, next.DefaultMaxConnections)
	}
	if !prev.Equal(next) {
		m.log.Info("settings updated", "max_concurrent_downloads", next.MaxConcurrentDownloads,
			"global_speed_limit", next.GlobalSpeedLimit, "default_dir", next.DefaultDir,
			"default_max_connections", next.DefaultMaxConnections, "categories", len(next.Categories),
			"use_category_folders", next.UseCategoryFolders, "confirm_browser_downloads", next.ConfirmBrowserDownloads,
			"launch_at_login", next.LaunchAtLogin, "show_tray_icon", next.ShowTrayIcon,
			"show_progress_window", next.ShowProgressWindow, "show_complete_dialog", next.ShowCompleteDialog,
			"proxy_mode", int(next.Network.Proxy.Mode), "host_rules", len(next.Network.HostRules),
			"site_logins", len(next.Network.SiteLogins), "clipboard_monitor", next.Desktop.ClipboardMonitor,
			"check_files", next.Desktop.CheckFiles, "renew_links", next.Desktop.RenewMode(),
			"mark_downloaded_files", next.Desktop.MarkDownloadedFiles, "av_scan_enabled", next.Desktop.AVScanEnabled,
			"update_feed", next.Update.FeedURL != "", "auto_check_updates", next.Update.AutoCheck,
			"polite", next.Polite.Enabled, "polite_target_ms", next.Polite.TargetMs)
		m.notify(Notice{Kind: NoticeSettings, Settings: next.Clone()})
	}
	return next.Clone(), nil
}

// followDefaultConns gives every unfinished download of this daemon whose connection
// limit is the previous default_max_connections (old) the new one (n), through
// SetDownloadOptions, so that downloads that use the default follow it like new ones;
// a download given another limit keeps it. Failures (one being removed meanwhile) are
// only logged: the settings are already saved.
func (m *Manager) followDefaultConns(ctx context.Context, old, n int) {
	m.mu.Lock()
	var ids []model.DownloadID
	for id, e := range m.entries {
		cur := e.rec.MaxConnections
		if e.pendMax != nil {
			cur = *e.pendMax
		}
		if !e.foreign && e.rec.State != model.StateCompleted && cur == old {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	slices.Sort(ids)
	v := int32(n)
	for _, id := range ids {
		_, err := m.SetDownloadOptions(ctx, &downexv1.SetDownloadOptionsRequest{Id: int64(id), MaxConnections: &v})
		if err != nil {
			m.log.Info("connection limit not changed with the default", "download", int64(id), "err", err)
		}
	}
	if len(ids) > 0 {
		m.log.Info("downloads follow the new default connection limit", "downloads", len(ids), "max_connections", n)
	}
}

// ---- helpers -----------------------------------------------------------------------

// lookup returns the entry of id. Downloads the daemon does not own (created or held
// by another process) are read from the store each time and indexed as foreign.
func (m *Manager) lookup(ctx context.Context, id model.DownloadID) (*entry, error) {
	m.mu.Lock()
	e := m.entries[id]
	owned := e != nil && !e.foreign
	m.mu.Unlock()
	if owned {
		return e, nil
	}
	if id <= 0 {
		return nil, errNotFound(id)
	}
	rec, err := m.st.GetDownload(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		m.mu.Lock()
		if cur := m.entries[id]; cur != nil && cur.foreign {
			m.forgetLocked(id)
		}
		m.mu.Unlock()
		return nil, errNotFound(id)
	}
	if err != nil {
		return nil, errStore(ctx, "read download", err)
	}
	received, err := m.storedBytes(ctx, rec)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, errStore(ctx, "read download", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.adoptLocked(rec, received); e != nil {
		return e, nil
	}
	return nil, errNotFound(id)
}

// adoptLocked indexes rec, read from the store, as a foreign entry, or refreshes an
// existing foreign entry. Entries the daemon owns are returned unchanged. It returns
// nil for a download removed meanwhile (rec is then stale).
func (m *Manager) adoptLocked(rec *model.Download, received int64) *entry {
	if _, gone := m.removed[rec.ID]; gone {
		return nil
	}
	if _, busy := m.rescuing[rec.ID]; busy { // being adopted by Rescue (rescue.go)
		return nil
	}
	e := m.entries[rec.ID]
	switch {
	case e == nil:
		e = newEntry(*rec)
		e.foreign, e.received = true, received
		m.entries[rec.ID] = e
		m.joinQueueLocked(e, rec.QueueID)
	case e.foreign:
		e.rec, e.received, e.errCode = *rec, received, ""
		if rec.State == model.StateFailed {
			e.errCode = recordErrorCode(rec)
		}
	}
	return e
}

// acquire looks id up and takes its operation slot (released with releaseOp). A
// foreign download is adopted first: refused while another process holds it, and a
// stale probing/downloading state (that process died) is stored as paused.
func (m *Manager) acquire(ctx context.Context, id model.DownloadID) (*entry, error) {
	e, err := m.lookup(ctx, id)
	if err != nil {
		return nil, err
	}
	select {
	case e.op <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	m.mu.Lock()
	current, foreign := m.entries[id] == e, e.foreign
	m.mu.Unlock()
	if !current {
		releaseOp(e)
		return nil, errNotFound(id)
	}
	if foreign {
		if err := m.takeOver(ctx, e); err != nil {
			releaseOp(e)
			return nil, err
		}
	}
	if err := m.freshen(ctx, e); err != nil {
		releaseOp(e)
		return nil, err
	}
	return e, nil
}

// freshen re-reads the record of a stopped entry marked stale (its run ended while
// the store could not be read), so that no operation writes the outdated copy over
// what the engine committed (mode, size, validators, reserved name) or misses the
// file to delete. It fails with CodeUnavailable while the store still cannot be read.
// The caller holds e's operation slot.
func (m *Manager) freshen(ctx context.Context, e *entry) error {
	m.mu.Lock()
	need := e.stale && e.run == nil
	m.mu.Unlock()
	if !need {
		return nil
	}
	rec, err := m.st.GetDownload(ctx, e.id)
	if errors.Is(err, store.ErrNotFound) {
		m.mu.Lock()
		m.forgetLocked(e.id)
		m.mu.Unlock()
		return errNotFound(e.id)
	}
	var received int64
	if err == nil {
		received, err = m.storedBytes(ctx, rec)
	}
	if err != nil {
		if ctx.Err() != nil && isContextError(err) {
			return err
		}
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("read download %d: %w", e.id, err))
	}
	if activeState(rec.State) {
		// Not running here and not queued: the run that stopped was not recorded.
		rec.State = model.StatePaused
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if e.run == nil {
		e.rec, e.received, e.stale = *rec, received, false
		e.errCode = ""
		if rec.State == model.StateFailed {
			e.errCode = recordErrorCode(rec)
		}
	}
	return nil
}

func releaseOp(e *entry) { <-e.op }

// takeOver makes the daemon the owner of a foreign download.
func (m *Manager) takeOver(ctx context.Context, e *entry) error {
	lk, err := m.lockForChange(e.id)
	if err != nil {
		return err
	}
	defer lk.Release()
	rec, err := m.st.GetDownload(ctx, e.id)
	if errors.Is(err, store.ErrNotFound) {
		m.mu.Lock()
		m.forgetLocked(e.id)
		m.mu.Unlock()
		return errNotFound(e.id)
	}
	if err != nil {
		return errStore(ctx, "read download", err)
	}
	received, err := m.storedBytes(ctx, rec)
	if err != nil {
		return errStore(ctx, "read download", err)
	}
	if rec.State == model.StateProbing || rec.State == model.StateDownloading {
		rec.State, rec.UpdatedAt = model.StatePaused, time.Now()
		if err := m.st.UpdateDownload(ctx, rec); err != nil {
			return errStore(ctx, "adopt download", err)
		}
	}
	m.mu.Lock()
	e.rec, e.received, e.foreign = *rec, received, false
	e.errCode = ""
	if rec.State == model.StateFailed {
		e.errCode = recordErrorCode(rec)
	}
	m.mu.Unlock()
	return nil
}

// lockForChange takes download id's cross-process lock for a change of its record or
// files (nil without Config.LockDir). A busy lock is FailedPrecondition.
func (m *Manager) lockForChange(id model.DownloadID) (*dlock.Lock, error) {
	if m.cfg.LockDir == "" {
		return nil, nil
	}
	lk, err := dlock.Acquire(m.cfg.LockDir, id)
	switch {
	case errors.Is(err, dlock.ErrBusy):
		return nil, errPrecondition("download %d is in use by another downex process", id)
	case err != nil:
		return nil, errInternal("lock download", err)
	}
	return lk, nil
}

// forgetLocked drops a download whose record is gone. Ids are never reused, so the id
// is remembered to keep a concurrent List or lookup from indexing a stale copy again.
func (m *Manager) forgetLocked(id model.DownloadID) {
	if e := m.entries[id]; e != nil {
		m.leaveQueueLocked(e)
	}
	delete(m.entries, id)
	m.dequeueLocked(id)
	m.removed[id] = struct{}{}
}

// restore puts a download that an operation took out of the queue (parkLocked) back
// after the operation failed.
func (m *Manager) restore(e *entry, wasQueued bool) {
	if !wasQueued {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.unparkLocked(e)
	if m.entries[e.id] == e && e.run == nil && !m.closed {
		m.enqueueLocked(e.id)
		m.scheduleLocked()
	}
}

// current renders e.
func (m *Manager) current(e *entry) *downexv1.Download {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.protoLocked(e)
}

func (m *Manager) protoLocked(e *entry) *downexv1.Download {
	v := downloadView{
		rec: e.rec, received: e.received, errCode: e.errCode, queue: m.queueOfLocked(e).id(),
		running: e.run != nil, queuePosition: m.queuePositionLocked(e.id),
	}
	if m.settings.Desktop.CheckFiles { // docs/DESKTOP-SPEC.md §9.9: nothing while off
		v.check = filecheck.Shown(&e.rec, e.early)
	}
	v.renewCheck, v.renewOffer = e.check != nil, m.renewOfferLocked(e)
	if r := e.run; r != nil {
		v.progress = r.progress.Load()
		v.mprogress = r.mprogress.Load()
		// The running total: the stored time of the earlier runs plus this one's so far
		// (up to its stop while it is finalizing).
		end := r.stopped
		if end.IsZero() {
			end = m.now()
		}
		v.rec.ActiveMS += runMillis(r.started, end)
		if v.rec.StartedAt.IsZero() {
			v.rec.StartedAt = r.started
		}
	}
	// Options changed during the run are stored when it stops; clients see them now.
	if e.pendMax != nil {
		v.rec.MaxConnections = *e.pendMax
	}
	if e.pendSpeed != nil {
		v.rec.SpeedLimit = *e.pendSpeed
	}
	if e.pendDialog != nil {
		v.rec.CompletionDialog = *e.pendDialog
	}
	return v.toProto()
}

// settledLocked waits (releasing m.mu meanwhile) while e's run is applying its
// outcome. Afterwards e has no run, or a run whose intent may still be set: an intent
// set under m.mu before the run starts finalizing is always applied.
func (m *Manager) settledLocked(ctx context.Context, e *entry) error {
	for e.run != nil && e.run.finalizing {
		done := e.run.done
		m.mu.Unlock()
		err := waitDone(ctx, done)
		m.mu.Lock()
		if err != nil {
			return err
		}
	}
	return nil
}

// waitDone waits for done or the end of ctx.
func waitDone(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// errStore maps a store failure: the caller's cancellation as is, else Internal.
func errStore(ctx context.Context, op string, err error) error {
	if ctx.Err() != nil && isContextError(err) {
		return err
	}
	return errInternal(op, err)
}

// filePath is a download's file on disk: the final file once completed, else the
// part file, or "" before a name was chosen or while awaiting confirmation (its name is
// only proposed: no file exists yet).
func filePath(d *model.Download) string {
	switch {
	case d.Filename == "", d.State == model.StateAwaitingConfirmation:
		return ""
	case d.State == model.StateCompleted:
		return filepath.Join(d.Dir, d.Filename)
	}
	return engine.PartPath(d.Dir, d.Filename)
}

// redactURL returns rawURL without user information, query and fragment, which may
// carry credentials, for logs.
func redactURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "(unparsable URL)"
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}).String()
}
