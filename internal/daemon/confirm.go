package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	"downex/internal/engine"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/store"
	"downex/internal/validate"
)

// Confirm completes a download awaiting confirmation (docs/DESKTOP-SPEC.md §1). For a
// media download, video_track and audio_track (each "" = keep) replace the selected
// tracks (see selectMediaTracks; a site download's expected_size follows the pick); for
// other downloads they must be empty. dir and
// filename, when given, replace the proposed ones and are validated like Add's
// (docs/DAEMON-SPEC.md §4; a missing dir is created below an existing directory, and
// the proposed folder and the category folders are trusted, see checkConfirmedDir); the
// name is reserved (the first free "name (n).ext" variant in the folder); then the
// download is queued (start) or paused ("download later"). With queue_id (0 = keep its
// queue; one that does not exist is InvalidArgument) it moves to the end of that queue
// first and is queued either way (docs/M6-SPEC.md §1): "download later" into a queue
// waits until that queue runs. max_connections and speed_limit, when set, are stored as
// with Resume. A download that is not awaiting confirmation is FailedPrecondition; to
// cancel one, Remove it.
func (m *Manager) Confirm(ctx context.Context, r *downexv1.ConfirmRequest) (*downexv1.Download, error) {
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
		if v := r.GetSpeedLimit(); v >= 0 {
			speed = &v
		} else {
			return nil, errInvalid("speed_limit must not be negative")
		}
	}
	name, err := validate.Filename(r.GetFilename())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	video, audio := r.GetVideoTrack(), r.GetAudioTrack()
	for _, t := range []string{video, audio} {
		if err := validate.TrackID(t); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}
	m.mu.Lock()
	s := m.settings
	m.mu.Unlock()
	queue, err := m.queueForAdd(r.GetQueueId(), 0)
	if err != nil {
		return nil, err
	}
	var dir string
	if r.GetDir() != "" {
		if dir, err = absDir(r.GetDir()); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}

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
	if e.run != nil || e.rec.State != model.StateAwaitingConfirmation {
		st := e.rec.State
		m.mu.Unlock()
		return nil, errPrecondition("download %d is not awaiting confirmation (it is %s)", id, st)
	}
	rec := e.rec
	if maxConn != nil && *maxConn == 0 {
		v := m.settings.DefaultMaxConnections
		maxConn = &v
	}
	m.mu.Unlock()

	if rec.Kind == model.KindMedia {
		if err := selectMediaTracks(&rec, video, audio); err != nil {
			return nil, err
		}
		if isSite(rec.Media) {
			// Any offered MP4 quality: the container (and so the name's .mp4) stays, the
			// size follows the pick (docs/SITE-MEDIA-SPEC.md §3.3).
			rec.ExpectedSize = siteExpectedSize(rec.Media)
		}
	} else if video != "" || audio != "" {
		return nil, errInvalid("video_track and audio_track apply only to media downloads")
	}
	if dir != "" {
		if err := s.checkConfirmedDir(dir, rec.Dir); err != nil {
			return nil, err
		}
		if !samePath(dir, rec.Dir) {
			if c := s.categoryOfFolder(dir); c != "" {
				rec.Category = c
			}
		}
		rec.Dir = dir
	}
	if name != "" {
		rec.Filename = name
		if rec.Kind == model.KindMedia && rec.Media != nil {
			rec.Filename = mediaFileName(name, "", "", rec.Media.Container)
		}
	}
	if rec.Filename != "" {
		if rec.Filename, err = m.freeName(ctx, rec.Dir, rec.Filename, rec.ID); err != nil {
			return nil, err
		}
	}
	if maxConn != nil {
		rec.MaxConnections = *maxConn
	}
	if speed != nil {
		rec.SpeedLimit = *speed
	}
	rec.State, rec.UpdatedAt = model.StatePaused, time.Now()
	if r.GetStart() || queue != 0 {
		rec.State = model.StateQueued
	}
	lk, err := m.lockForChange(id)
	if err != nil {
		return nil, err
	}
	var target *queueState
	if queue != 0 {
		m.queueMu.Lock()
		err = m.qstore.MoveDownloads(context.WithoutCancel(ctx), []model.DownloadID{id}, queue, -1)
		if err == nil {
			m.mu.Lock()
			if target = m.queues[queue]; target != nil {
				m.moveLocked([]model.DownloadID{id}, target, -1)
			}
			m.mu.Unlock()
		}
		m.queueMu.Unlock()
		if err != nil {
			lk.Release()
			if errors.Is(err, store.ErrQueueNotFound) {
				return nil, errInvalid("queue_id %d: no such queue", queue)
			}
			return nil, errInternal("confirm download", err)
		}
	}
	err = m.st.UpdateDownload(context.WithoutCancel(ctx), &rec)
	lk.Release()
	if err != nil {
		return nil, errInternal("confirm download", err)
	}

	m.mu.Lock()
	e.rec = rec
	if rec.State == model.StateQueued && !m.closed {
		m.enqueueLocked(e.id)
	}
	m.scheduleLocked()
	pd := m.protoLocked(e)
	m.mu.Unlock()
	m.hub.publish(e.id, kindUpdated)
	m.log.Info("download confirmed", "download", int64(id), "dir", rec.Dir, "start", r.GetStart(), "queue", int64(queue))
	return pd, nil
}

// checkConfirmedDir validates dir (absolute and clean), the folder a confirmation chose
// for a download proposed into the folder proposed. The folders the daemon itself would
// choose are trusted like the settings they come from, exactly as Add trusts a category
// folder: the proposed folder (accepting it unchanged is no different from sending no
// dir) and the folder of every configured category, which the confirmation dialog's
// category selector sends. They may lie off the local drives below a redirected
// default_dir and are created with their parents when the download starts; only one
// that exists as something other than a directory is refused. Any other folder is a
// client's choice and is validated like Add's dir (docs/DAEMON-SPEC.md §4): on a local
// drive unless it is default_dir, and created now when its parent exists.
func (s *Settings) checkConfirmedDir(dir, proposed string) error {
	if samePath(dir, proposed) || s.categoryOfFolder(dir) != "" {
		return checkTrustedDir(dir)
	}
	if _, err := checkDir(dir, s.DefaultDir); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return ensureDir(dir)
}

// PauseAll pauses every queued and running download of this daemon (as Pause does) and
// returns how many it paused. Queued ones are paused first, so that no slot freed by a
// stopping download starts one of them. Downloads that complete, fail or are removed
// meanwhile are skipped; the first other failure is returned with the count so far.
func (m *Manager) PauseAll(ctx context.Context) (int, error) {
	if m.isClosed() {
		return 0, errShuttingDown()
	}
	m.mu.Lock()
	queued := make([]model.DownloadID, 0, len(m.waiting))
	for id := range m.waiting {
		queued = append(queued, id)
	}
	slices.Sort(queued)
	var running []model.DownloadID
	for id, e := range m.entries {
		if e.run != nil && e.run.intent != intentPause && e.run.intent != intentRemove {
			running = append(running, id)
		}
	}
	m.mu.Unlock()
	slices.Sort(running)

	var (
		mu       sync.Mutex
		n        int
		firstErr error
	)
	pause := func(id model.DownloadID) {
		d, err := m.Pause(ctx, int64(id))
		mu.Lock()
		defer mu.Unlock()
		switch {
		case err == nil:
			if d.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_PAUSED {
				n++
			}
		case skippable(err):
		case firstErr == nil:
			firstErr = err
		}
	}
	for _, id := range queued {
		pause(id)
	}
	var wg sync.WaitGroup
	for _, id := range running {
		wg.Go(func() { pause(id) })
	}
	wg.Wait()
	if n > 0 {
		m.log.Info("paused all downloads", "paused", n)
	}
	return n, firstErr
}

// ResumeAll queues every paused download (as Resume does, without restart) and returns
// how many it queued. Failed downloads and downloads awaiting confirmation are left as
// they are. Downloads that another process holds or that are removed meanwhile are
// skipped; the first other failure is returned with the count so far.
func (m *Manager) ResumeAll(ctx context.Context) (int, error) {
	if m.isClosed() {
		return 0, errShuttingDown()
	}
	all, err := m.List(ctx) // also adopts paused downloads of other processes
	if err != nil {
		return 0, err
	}
	n := 0
	var firstErr error
	for _, d := range all {
		if d.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_PAUSED {
			continue
		}
		got, err := m.Resume(ctx, &downexv1.ResumeRequest{Id: d.GetId()})
		switch {
		case err == nil:
			if got.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED {
				n++
			}
		case skippable(err):
		case firstErr == nil:
			firstErr = err
		}
		if ctx.Err() != nil {
			break
		}
	}
	if n > 0 {
		m.log.Info("resumed all downloads", "resumed", n)
	}
	return n, firstErr
}

// skippable reports the failures of PauseAll and ResumeAll that concern one download
// only: it is gone, or not in a state the operation applies to.
func skippable(err error) bool {
	switch connect.CodeOf(err) {
	case connect.CodeNotFound, connect.CodeFailedPrecondition:
		return true
	}
	return false
}

// ---- names -------------------------------------------------------------------------

// maxNameVariants bounds the "name (n).ext" candidates freeName tries.
const maxNameVariants = 10_000

// freeName returns the first variant of name ("name", "name (1).ext", ...) in dir that
// no other unfinished download claims and that exists neither as a file nor as a part
// file (nor as a media download's part directory). Storing it in self's record reserves it against other downloads (the engine's
// reservation skips names that records claim); the engine creates the part file when
// the download starts, choosing another variant only if one appeared meanwhile.
func (m *Manager) freeName(ctx context.Context, dir, name string, self model.DownloadID) (string, error) {
	all, err := m.st.ListDownloads(ctx)
	if err != nil {
		return "", errStore(ctx, "reserve name", err)
	}
	claimed := make(map[string]bool)
	for _, d := range all {
		if d.ID == self || d.Filename == "" || d.State == model.StateCompleted || nameKey(d.Dir) != nameKey(dir) {
			continue
		}
		claimed[nameKey(d.Filename)] = true
	}
	for i := range maxNameVariants {
		cand := numbered(name, i)
		if claimed[nameKey(cand)] {
			continue
		}
		taken, err := anyExists(filepath.Join(dir, cand), engine.PartPath(dir, cand), MediaPartsDir(dir, cand))
		if err != nil {
			return "", errPrecondition("check %s: %v", filepath.Join(dir, cand), err)
		}
		if !taken {
			return cand, nil
		}
	}
	return "", errPrecondition("no free file name for %q in %s", name, dir)
}

// numbered returns name for n = 0, else "base (n).ext" (as the engine numbers names).
func numbered(name string, n int) string {
	if n == 0 {
		return name
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	if base == "" {
		base, ext = name, ""
	}
	return fmt.Sprintf("%s (%d)%s", base, n, ext)
}

// nameKey normalises a path for comparing reservations: cleaned, and case-folded where
// file names are case-insensitive by default (as the engine compares them).
func nameKey(p string) string {
	p = filepath.Clean(p)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		p = strings.ToLower(p)
	}
	return p
}

// anyExists reports whether any of paths exists (as anything, links included).
func anyExists(paths ...string) (bool, error) {
	for _, p := range paths {
		_, err := os.Lstat(p)
		switch {
		case err == nil:
			return true, nil
		case !errors.Is(err, fs.ErrNotExist):
			return false, err
		}
	}
	return false, nil
}

// proposedName is the file name proposed for a browser hand-off awaiting confirmation:
// the requested one, else the URL's last path segment when its extension belongs to a
// category (a name such as "download.php" is left to the server's answer), else ""
// (the engine names the file from the server's answer when the download starts).
func proposedName(requested, rawURL string, cats []Category) string {
	if requested != "" {
		return requested
	}
	seg := urlFileName(rawURL)
	if categoryForExtension(cats, fileExtension(seg)) == nil {
		return ""
	}
	name, err := validate.Filename(seg)
	if err != nil {
		return ""
	}
	return name
}

// ---- confirmation UI ---------------------------------------------------------------

// confirmLaunchInterval spaces the launches of the confirmation UI: a burst of browser
// hand-offs ("download all links") starts it once, and the running UI shows every
// awaiting download in turn.
const confirmLaunchInterval = 2 * time.Second

// confirmer asks the desktop UI to show awaiting downloads (Config.OpenConfirmation)
// and reports each one as a NoticeAwaiting.
type confirmer struct {
	m        *Manager
	launch   func(id int64) error // nil: no UI
	interval time.Duration        // between launches (confirmLaunchInterval)

	mu      sync.Mutex
	next    time.Time            // earliest time of the next launch
	pending []*downexv1.Download // waiting for the trailing launch
	timer   *time.Timer
	closed  bool
}

// canAsk reports whether the user can be asked to confirm a browser hand-off now: the
// confirmation UI can be opened, or a subscriber (the tray with its icon shown) shows the
// NoticeAwaiting. Without either nothing would ever show an awaiting download.
func (c *confirmer) canAsk() bool {
	return c.launch != nil || c.m.noticesShown()
}

// request asks for d's confirmation: at once, or with the next trailing launch when
// the UI was started less than confirmLaunchInterval ago.
func (c *confirmer) request(d *downexv1.Download) {
	if c.launch == nil {
		c.m.notify(Notice{Kind: NoticeAwaiting, Download: d})
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	now := time.Now()
	if c.timer == nil && !now.Before(c.next) {
		c.next = now.Add(c.interval)
		c.mu.Unlock()
		c.launchFor([]*downexv1.Download{d})
		return
	}
	c.pending = append(c.pending, d)
	if c.timer == nil {
		c.timer = time.AfterFunc(time.Until(c.next), c.flush)
	}
	c.mu.Unlock()
}

func (c *confirmer) flush() {
	c.mu.Lock()
	ds := c.pending
	c.pending, c.timer = nil, nil
	if c.closed || len(ds) == 0 {
		c.mu.Unlock()
		return
	}
	c.next = time.Now().Add(c.interval)
	c.mu.Unlock()
	c.launchFor(ds)
}

// launchFor starts the UI for the oldest of ds (it shows every awaiting download,
// oldest first) and reports them.
func (c *confirmer) launchFor(ds []*downexv1.Download) {
	oldest := slices.MinFunc(ds, func(a, b *downexv1.Download) int { return cmp.Compare(a.GetId(), b.GetId()) })
	err := c.launch(oldest.GetId())
	if err != nil {
		c.m.log.Warn("cannot open the confirmation window", "download", oldest.GetId(), "err", err)
	}
	for _, d := range ds {
		c.m.notify(Notice{Kind: NoticeAwaiting, Download: d, UILaunched: err == nil})
	}
}

// close drops pending launches (Shutdown).
func (c *confirmer) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	c.pending = nil
}
