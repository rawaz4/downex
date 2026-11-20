package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"downex/internal/limiter"
	"downex/internal/model"
	"downex/internal/platform"
	"downex/internal/store"
	"downex/internal/transport"
)

// Engine runs downloads recorded in a store. It is safe for concurrent use; a given
// download runs at most once at a time.
type Engine struct {
	st    store.Store
	tf    transport.Factory
	opts  Options
	clock Clock
	log   *slog.Logger
	bufs  *bufPool

	mu      sync.Mutex
	running map[model.DownloadID]*runCtl
	hosts   map[string]hostPenalty // per-host limits lowered after 429/503/421 or refusals
}

type hostPenalty struct {
	limit int
	until time.Time
}

// New returns an Engine persisting to st and fetching through tf. Zero Options fields
// take the defaults of docs/ENGINE-SPEC.md.
func New(st store.Store, tf transport.Factory, opts Options) *Engine {
	opts = opts.withDefaults()
	return &Engine{
		st: st, tf: tf, opts: opts, clock: opts.Clock, log: opts.Logger,
		bufs:    newBufPool(),
		running: make(map[model.DownloadID]*runCtl),
		hosts:   make(map[string]hostPenalty),
	}
}

// Add records a new queued download of req into dir. When filename is empty the name
// is chosen when the download starts (Content-Disposition, else the URL). An existing
// file is never overwritten: a free "name (n).ext" variant is reserved instead.
// maxConn ≤ 0 selects the engine default; speedLimit is in bytes per second, 0 = unlimited.
func (e *Engine) Add(ctx context.Context, req model.Request, dir, filename string, maxConn int, speedLimit int64) (*model.Download, error) {
	u, err := url.Parse(req.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%w: unsupported URL %q", ErrInvalidRequest, req.URL)
	}
	if speedLimit < 0 {
		return nil, fmt.Errorf("%w: negative speed limit", ErrInvalidRequest)
	}
	if dir == "" {
		dir = "."
	}
	if dir, err = filepath.Abs(dir); err != nil {
		return nil, fmt.Errorf("%w: directory: %w", ErrInvalidRequest, err)
	}
	req.Headers = req.Headers.Clone()
	req.Body = bytes.Clone(req.Body)
	now := e.clock.Now()
	d := &model.Download{
		Request: req, Dir: dir, Size: -1, Mode: model.ModeUnknown, State: model.StateQueued,
		MaxConnections: min(max(maxConn, 0), e.opts.HardCap), SpeedLimit: speedLimit,
		CreatedAt: now, UpdatedAt: now,
	}
	if filename != "" {
		name, err := e.reserve(ctx, dir, transport.SanitizeFilename(filename), 0, false)
		if err != nil {
			return nil, err
		}
		d.Filename = name
	}
	if err := e.st.CreateDownload(ctx, d); err != nil {
		if d.Filename != "" {
			_ = os.Remove(partPath(dir, d.Filename))
		}
		return nil, fmt.Errorf("create download: %w", err)
	}
	return d, nil
}

// Run downloads id until it completes (nil), ctx is cancelled (the download is paused
// with its durable progress committed, and ctx's error is returned) or it fails (the
// record is marked failed, partial progress is kept, and the error is returned).
//
// lim throttles this download (nil = unlimited; if nil and the download has a
// SpeedLimit, a private limiter enforces it). onProgress, if non-nil, is called from
// Run's goroutine at most 4 times per second and once when the download stops; it
// must return quickly.
//
// ErrValidatorChanged and ErrCorruptCheckpoint mean the download cannot be resumed:
// call Restart, then Run again.
func (e *Engine) Run(ctx context.Context, id model.DownloadID, lim limiter.Limiter, onProgress func(Progress)) error {
	if err := e.acquire(id, true); err != nil {
		return err
	}
	defer e.releaseRun(id)
	d, err := e.st.GetDownload(ctx, id)
	if err != nil {
		return fmt.Errorf("load download %d: %w", id, err)
	}
	if d.State == model.StateCompleted {
		return nil
	}
	var thr throttle
	switch {
	case lim != nil:
		thr = lim
	case d.SpeedLimit > 0:
		thr = newBucket(e.clock, d.SpeedLimit)
	}
	sctx := context.WithoutCancel(ctx)
	for attempt := 1; ; attempt++ {
		err := e.runOnce(ctx, sctx, d, thr, onProgress)
		if err == nil || ctx.Err() != nil || d.Mode != model.ModeSingle || !isTransient(err) || attempt > e.opts.MaxRetries {
			return err
		}
		// Single mode cannot resume (§2.3): start over from byte 0.
		e.log.Warn("single-connection download failed; restarting from the beginning",
			"download", int64(id), "attempt", attempt, "err", err)
		if serr := sleep(ctx, e.clock, backoffDelay(&e.opts, attempt, retryAfterOf(err))); serr != nil {
			return e.settle(ctx, sctx, d, serr)
		}
	}
}

// RestartOptions say what Restart does with a completed download (docs/ENGINE-SPEC.md
// §7.1). The zero value refuses one.
type RestartOptions struct {
	// Completed allows restarting a completed download: it is downloaded again from its
	// address. Without it a completed download is ErrInvalidRequest.
	Completed bool
	// Replace (a completed download whose file is a regular file): the new copy keeps the
	// name and replaces the file once it is complete (model.Download.ReplaceFile); until
	// then, and after a failure, the file stays as it is. Otherwise the file stays and the
	// new copy gets a free variant of the name ("name (1).ext"), reserved at once. A file
	// that is gone is downloaded again under its own name either way.
	Replace bool
}

// Restart discards a download's progress and remote validators so that the next Run
// probes and downloads it from scratch. Use it after ErrValidatorChanged or
// ErrCorruptCheckpoint. The reserved filename is kept, and so is the choice of an
// earlier restart of the completed download to replace its file. A completed download
// is restarted only with opts.Completed (see RestartOptions).
func (e *Engine) Restart(ctx context.Context, id model.DownloadID, opts RestartOptions) error {
	if err := e.acquire(id, false); err != nil {
		return err
	}
	defer e.releaseRun(id)
	d, err := e.st.GetDownload(ctx, id)
	if err != nil {
		return fmt.Errorf("load download %d: %w", id, err)
	}
	if d.Kind != model.KindFile {
		// Media downloads are restarted by their runner (the daemon's restartMedia).
		return fmt.Errorf("%w: download %d is not a file download", ErrInvalidRequest, id)
	}
	reserved := false // a part file was made for a completed download
	if d.State == model.StateCompleted {
		if !opts.Completed {
			return fmt.Errorf("%w: download %d is already completed", ErrInvalidRequest, id)
		}
		if err := e.againName(ctx, d, opts.Replace); err != nil {
			return err
		}
		reserved = !d.ReplaceFile && d.Filename != ""
	}
	// The reset record is saved before the part file is emptied: a crash in between
	// leaves a fresh download (whose next Run empties the part file itself), never
	// saved progress that the emptied part file no longer holds (ErrCorruptCheckpoint).
	resetRemote(d)
	// A restart is a new copy: its time and average start over too (the daemon does the
	// same for downloads it restarts; this covers "downex resume --restart --local").
	d.StartedAt, d.ActiveMS = time.Time{}, 0
	d.State = model.StateQueued
	d.UpdatedAt = e.clock.Now()
	if err := e.st.SaveCheckpoint(ctx, d, nil); err != nil {
		if reserved { // the record stays completed: nothing may keep the name it reserved
			_ = os.Remove(partPath(d.Dir, d.Filename))
		}
		return fmt.Errorf("save download: %w", err)
	}
	if d.Filename != "" {
		if err := os.Truncate(partPath(d.Dir, d.Filename), 0); err != nil && !errors.Is(err, fs.ErrNotExist) {
			// Only disk space is at stake: the next Run starts the part file over.
			e.log.Warn("cannot empty the part file of a restarted download", "download", int64(id), "err", err)
		}
	}
	return nil
}

// againName picks the name under which completed download d is downloaded again: its own
// with replace while its file is plausibly its own (OwnFile; marked ReplaceFile, the part
// file is made when the run opens it), else a free one reserved now (its own when the
// file is gone, a variant when it is there).
func (e *Engine) againName(ctx context.Context, d *model.Download, replace bool) error {
	d.ReplaceFile = false
	if d.Filename == "" {
		return nil
	}
	if replace {
		ok, err := OwnFile(ctx, e.st, d)
		if err != nil {
			return diskError("check the file", err)
		}
		if ok {
			d.ReplaceFile = true
			return nil
		}
	}
	name, err := e.reserve(ctx, d.Dir, d.Filename, d.ID, false)
	if err != nil {
		return err
	}
	if name != d.Filename {
		e.log.Info("completed download is downloaded again under another name", "download", int64(d.ID), "name", name)
	}
	d.Filename = name
	return nil
}

func resetRemote(d *model.Download) {
	d.Mode, d.Size, d.Validators, d.FinalURL, d.Error = model.ModeUnknown, -1, model.Validators{}, "", ""
	d.CompletedAt, d.ServedType, d.RemoteName = time.Time{}, "", ""
	// Bytes adopted from a partial file (§7.4) are gone once the download starts over.
	d.RescuedBytes = 0
}

func (e *Engine) runOnce(ctx, sctx context.Context, d *model.Download, lim throttle, onProgress func(Progress)) error {
	switch {
	case d.Mode == model.ModeRanged:
		return e.resume(ctx, sctx, d, lim, onProgress)
	case d.Mode == model.ModeSingle && e.promotedBeforeCrash(ctx, d):
		return e.markCompleted(sctx, d)
	}
	return e.fresh(ctx, sctx, d, lim, onProgress)
}

// fresh probes the resource (§2), picks the mode, name and size, prepares the part
// file and streams. Single-mode downloads always come here: they cannot resume.
func (e *Engine) fresh(ctx, sctx context.Context, d *model.Download, lim throttle, onProgress func(Progress)) error {
	resetRemote(d)
	d.State = model.StateProbing
	d.UpdatedAt = e.clock.Now()
	if err := e.st.SaveCheckpoint(sctx, d, nil); err != nil {
		return fmt.Errorf("save download: %w", err)
	}
	// The probe's response becomes the first worker's stream, so it is fetched with
	// that worker's own context: cancelling the worker (stall) must abort its reads.
	pctx, pcancel := context.WithCancel(ctx)
	f := e.tf.NewSlot()
	resp, err := e.probe(pctx, f, requestFor(d.Request), isHTMLFile(d))
	if err != nil {
		pcancel()
		f.Close()
		return e.settle(ctx, sctx, d, err)
	}
	headersAt := e.clock.Now()
	abort := func(err error) error {
		closeBody(resp)
		pcancel()
		f.Close()
		return e.settle(ctx, sctx, d, err)
	}
	pr, err := classifyProbe(resp)
	if err != nil {
		return abort(err)
	}
	d.ServedType = servedType(resp.ContentType)
	// The server's own name for the file, before any variant: a new link for this
	// download is matched against it (§7.3).
	remote, remoteControls := chooseName(resp, d.Request.URL)
	d.RemoteName = remote
	if d.Filename == "" {
		chosen, controls := remote, remoteControls
		name, err := e.reserve(ctx, d.Dir, chosen, d.ID, false)
		if err != nil {
			return abort(err)
		}
		d.Filename = name
		d.NameControls = d.NameControls || controls
	}
	file, err := e.openPart(ctx, d)
	if err != nil {
		return abort(err)
	}
	if err := preallocate(file, pr.size); err != nil {
		_ = file.Close()
		return abort(err)
	}
	d.Mode, d.Size, d.Validators, d.FinalURL = pr.mode, pr.size, resp.Validators, resp.FinalURL
	if pr.mode == model.ModeSingle {
		e.log.Warn("server does not support byte ranges; using one connection without resume",
			"download", int64(d.ID), "url", d.Request.URL)
	}
	var segs []*segment
	var stored []model.Segment
	if !pr.empty {
		end := int64(-1)
		if pr.size >= 0 {
			end = pr.size - 1
		}
		segs = []*segment{newSegment(0, end, 0, nil)}
		stored = []model.Segment{{Start: 0, End: end}}
	}
	d.State = model.StateDownloading
	d.UpdatedAt = e.clock.Now()
	if err := e.st.SaveCheckpoint(sctx, d, stored); err != nil {
		_ = file.Close()
		return abort(fmt.Errorf("save checkpoint: %w", err))
	}
	c := e.newCoordinator(ctx, sctx, d, file, segs, lim, onProgress, resp.ContentType)
	c.job.chained = pr.chained
	c.job.pinTo(resp.FinalURL)
	if pr.empty {
		closeBody(resp)
		pcancel()
		f.Close()
		return c.run(nil)
	}
	return c.run(&probeConn{st: newStream(resp.Body, 0, pr.streamEnd), f: f, ctx: pctx, cancel: pcancel, at: headersAt})
}

// resume reloads the persisted layout; every segment restarts at start + durable and
// every request carries If-Range (§7).
func (e *Engine) resume(ctx, sctx context.Context, d *model.Download, lim throttle, onProgress func(Progress)) error {
	stored, err := e.st.LoadSegments(ctx, d.ID)
	if err != nil {
		return e.settle(ctx, sctx, d, fmt.Errorf("load segments: %w", err))
	}
	layout, err := loadLayout(stored, d.Size)
	if err != nil {
		return e.settle(ctx, sctx, d, err)
	}
	file, err := e.opts.openFile(partPath(d.Dir, d.Filename), os.O_RDWR, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return e.recoverMissingPart(ctx, sctx, d, layout, lim, onProgress)
	}
	if err != nil {
		return e.settle(ctx, sctx, d, diskError("open part file", err))
	}
	if fi, err := file.Stat(); err != nil || fi.Size() != d.Size {
		_ = file.Close()
		if err == nil {
			err = fmt.Errorf("%w: part file has %d bytes, expected %d", ErrCorruptCheckpoint, fi.Size(), d.Size)
		}
		return e.settle(ctx, sctx, d, err)
	}
	segs := make([]*segment, len(layout))
	for i, s := range layout {
		segs[i] = newSegment(s.Start, s.End, s.Durable, nil)
	}
	d.State, d.Error = model.StateDownloading, ""
	d.UpdatedAt = e.clock.Now()
	if err := e.st.UpdateDownload(sctx, d); err != nil {
		_ = file.Close()
		return fmt.Errorf("save download: %w", err)
	}
	c := e.newCoordinator(ctx, sctx, d, file, segs, lim, onProgress, "")
	c.job.resumed = true
	return c.run(nil)
}

// recoverMissingPart handles a ranged download whose part file is gone: either the
// process stopped between the final rename and its commit, or the file was deleted.
func (e *Engine) recoverMissingPart(ctx, sctx context.Context, d *model.Download, layout []model.Segment,
	lim throttle, onProgress func(Progress)) error {
	if layoutDone(layout) && finalMatches(d) {
		return e.markCompleted(sctx, d)
	}
	e.log.Warn("part file is missing; downloading again from the beginning", "download", int64(d.ID))
	return e.fresh(ctx, sctx, d, lim, onProgress)
}

// promotedBeforeCrash reports whether d's part file is missing because it was already
// renamed to its final name, the process stopping before the commit that marks the
// download completed: every stored segment is durable and the final file has d.Size
// bytes. (The final name is committed before the rename; see coordinator.complete.)
func (e *Engine) promotedBeforeCrash(ctx context.Context, d *model.Download) bool {
	if d.Filename == "" || d.Size < 0 {
		return false
	}
	if _, err := os.Lstat(partPath(d.Dir, d.Filename)); !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	stored, err := e.st.LoadSegments(ctx, d.ID)
	if err != nil {
		return false
	}
	layout, err := loadLayout(stored, d.Size)
	return err == nil && layoutDone(layout) && finalMatches(d)
}

func layoutDone(layout []model.Segment) bool {
	for _, s := range layout {
		if !s.Done() {
			return false
		}
	}
	return true
}

// finalMatches reports whether d's final file exists as a regular file of d.Size bytes.
func finalMatches(d *model.Download) bool {
	fi, err := os.Lstat(filepath.Join(d.Dir, d.Filename))
	return err == nil && fi.Mode().IsRegular() && fi.Size() == d.Size
}

func (e *Engine) markCompleted(sctx context.Context, d *model.Download) error {
	d.State, d.Error, d.ReplaceFile = model.StateCompleted, "", false
	d.CompletedAt = e.clock.Now()
	d.UpdatedAt = d.CompletedAt
	if err := e.st.UpdateDownload(sctx, d); err != nil {
		return fmt.Errorf("save download: %w", err)
	}
	return nil
}

// openPart opens d's part file for writing. A part file that has gone missing is
// never recreated blindly under the stored name, which another download may have
// reserved meanwhile: the name is reserved again (O_EXCL, which may pick a variant),
// so that the file belongs to this download alone. A download that replaces its file
// (ReplaceFile) keeps its name while that file is a regular one; under a variant it
// replaces nothing.
func (e *Engine) openPart(ctx context.Context, d *model.Download) (partFile, error) {
	for attempt := 0; ; attempt++ {
		f, err := e.opts.openFile(partPath(d.Dir, d.Filename), os.O_RDWR, 0)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, fs.ErrNotExist) || attempt > 0 {
			return nil, diskError("open part file", err)
		}
		name, err := e.reserve(ctx, d.Dir, d.Filename, d.ID, d.ReplaceFile)
		if err != nil {
			return nil, err
		}
		if name != d.Filename {
			e.log.Warn("reserved name is taken; using another", "download", int64(d.ID), "name", name)
			d.ReplaceFile = false
		}
		d.Filename = name
	}
}

// settle records the outcome of a run that ended before streaming started.
func (e *Engine) settle(ctx, sctx context.Context, d *model.Download, cause error) error {
	if ctx.Err() != nil && isContextError(cause) {
		d.State = model.StatePaused
	} else {
		d.State, d.Error = model.StateFailed, cause.Error()
	}
	d.UpdatedAt = e.clock.Now()
	if err := e.st.UpdateDownload(sctx, d); err != nil {
		return errors.Join(cause, fmt.Errorf("save download: %w", err))
	}
	return cause
}

// preallocate empties the part file of a fresh download and, when the size is known,
// sizes it to size bytes: sparse where the file system allows it (platform.Preallocate),
// so that segments can be written at any offset without zero-filling the gaps. Part
// files other than *os.File (test doubles) are sized with Truncate.
func preallocate(f partFile, size int64) error {
	if err := f.Truncate(0); err != nil {
		return diskError("truncate part file", err)
	}
	if size > 0 {
		var err error
		if osf, ok := f.(*os.File); ok {
			err = platform.Preallocate(osf, size)
		} else {
			err = f.Truncate(size)
		}
		if err != nil {
			return diskError("preallocate part file", err)
		}
	}
	return nil
}

// servedType is a Content-Type's media type as the record stores it (ServedType): lower
// case, without parameters.
func servedType(ct string) string {
	ct, _, _ = strings.Cut(ct, ";")
	return strings.ToLower(strings.TrimSpace(ct))
}

// chooseName is the Content-Disposition filename, else the last segment of the final
// URL, sanitised for Windows and Linux (§2.5). controls reports that the name before
// sanitising held hidden text-direction characters (docs/DESKTOP-SPEC.md §9.5 N1).
func chooseName(resp *transport.Response, requestURL string) (name string, controls bool) {
	if resp.Filename != "" {
		return transport.SanitizeFilename(resp.Filename), resp.FilenameControls
	}
	u := resp.FinalURL
	if u == "" {
		u = requestURL
	}
	return transport.FilenameFromURL(u), transport.HiddenControls(transport.RawFilenameFromURL(u))
}

// requestFor prepares the caller's request for range fetches: range-related and
// conditional headers are owned by the engine, and content coding must be identity
// so that byte offsets address the file itself.
func requestFor(r model.Request) model.Request {
	h := r.Headers.Clone()
	if h == nil {
		h = http.Header{}
	}
	for _, k := range []string{"Range", "If-Range", "If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since"} {
		h.Del(k)
	}
	h.Set("Accept-Encoding", "identity")
	r.Headers = h
	if r.Method == "" {
		r.Method = http.MethodGet
	}
	return r
}

func hostKey(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}

// acquire marks id as held by Run (runs) or Restart.
func (e *Engine) acquire(id model.DownloadID, runs bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, busy := e.running[id]; busy {
		return fmt.Errorf("%w: %d", ErrAlreadyRunning, id)
	}
	e.running[id] = newRunCtl(runs)
	return nil
}

func (e *Engine) releaseRun(id model.DownloadID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.running, id)
}

// penalizeHost lowers the connection limit for host for Options.HostPenaltyTTL.
func (e *Engine) penalizeHost(host string, limit int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.hosts[host] = hostPenalty{limit: max(limit, 1), until: e.clock.Now().Add(e.opts.HostPenaltyTTL)}
}

// HostPenalized reports whether a download host named hostname (lower case, without a
// port) has an active connection penalty (§10: a cap lowered after 429/503/421 or
// refused connections). Polite mode sends no TCP probes to its address then, which the
// server would count as connections (docs/POLITE-SPEC.md rule 4).
func (e *Engine) HostPenalized(hostname string) bool {
	now := e.clock.Now()
	e.mu.Lock()
	defer e.mu.Unlock()
	for host, p := range e.hosts {
		if now.After(p.until) {
			continue
		}
		name := host
		if h, _, err := net.SplitHostPort(host); err == nil {
			name = h
		}
		if strings.EqualFold(strings.Trim(name, "[]"), hostname) {
			return true
		}
	}
	return false
}

// hostLimit returns the current lowered limit for host, or 0 when there is none.
func (e *Engine) hostLimit(host string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	p, ok := e.hosts[host]
	if !ok {
		return 0
	}
	if e.clock.Now().After(p.until) {
		delete(e.hosts, host)
		return 0
	}
	return p.limit
}
