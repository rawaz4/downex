package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"time"

	"connectrpc.com/connect"

	"downex/internal/dlock"
	"downex/internal/engine"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/store"
	"downex/internal/transport"
	"downex/internal/validate"
)

// Expired links (docs/DAEMON-SPEC.md §3 "New links", docs/ENGINE-SPEC.md §7.2-7.3).
const (
	// RenewCheckBudget bounds the automatic check of a new add against the downloads whose
	// link expired: the add is held (not started, not asked about) at most this long.
	RenewCheckBudget = 15 * time.Second
	// MaxRenewCandidates is how many downloads one new add is checked against.
	MaxRenewCandidates = 3
	// renewApplyTimeout bounds storing an automatic renewal.
	renewApplyTimeout = 10 * time.Second
)

// renewCheck is the hold of a new add whose link is being checked against downloads
// whose link expired (A4). Its fields are set when it starts; ended and quiet are
// guarded by Manager.mu.
type renewCheck struct {
	candidates []model.DownloadID
	req        model.Request // the new add's request
	awaiting   bool          // the add was recorded awaiting confirmation
	ctx        context.Context
	cancel     context.CancelFunc

	ended bool // a user operation ended it (A6): the result is discarded
	quiet bool // ... and the add is released without asking or opening anything
}

// renewOffer is the offer of a new add awaiting confirmation to continue a stopped
// download instead (A8). In memory only.
type renewOffer struct {
	old         model.DownloadID
	nameDiffers bool
}

// renewWin is a proven candidate: its operation slot and its lock are held.
type renewWin struct {
	e     *entry
	lk    *dlock.Lock
	proof engine.LinkProof
}

func (w *renewWin) release() {
	w.lk.Release()
	releaseOp(w.e)
}

// ---- RenewLink -----------------------------------------------------------------------

// RenewLink gives a stopped file download a new link (docs/DAEMON-SPEC.md §3, R1-R4):
// the link of url, or that of from_download, a new download awaiting confirmation,
// which is removed once the renewal succeeded. The engine proves first that the new
// link serves the same file (engine.ProveLink); the download then continues from
// where it stopped, queued (start) or paused. Nothing changes when the proof fails:
// the refusal is FailedPrecondition with a "<code>: " message prefix and the code as
// ErrorCodeMeta (renew_*, link_expired, auth_required, http_status, network, tls).
// progress_window with start: downexd opens the download's progress window itself when it
// starts now, as Confirm does (progress_window_opening in the answer).
func (m *Manager) RenewLink(ctx context.Context, r *downexv1.RenewLinkRequest) (*downexv1.RenewLinkResponse, error) {
	id := model.DownloadID(r.GetId())
	page, err := parsePageURL(r.GetPageUrl())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	var (
		req     model.Request
		from    model.DownloadID
		fromSrc model.Source
	)
	switch link := r.GetLink().(type) {
	case *downexv1.RenewLinkRequest_Url:
		if err := validate.URL(link.Url); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		headers, err := validate.Headers(r.GetHeaders())
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		req = model.Request{URL: link.Url, Method: http.MethodGet, Headers: headers}
	case *downexv1.RenewLinkRequest_FromDownload:
		from = model.DownloadID(link.FromDownload)
		switch {
		case from == id:
			return nil, errInvalid("from_download must be another download")
		case len(r.GetHeaders()) > 0:
			return nil, errInvalid("headers apply only to a new url")
		}
	default:
		return nil, errInvalid("url or from_download is required")
	}

	// A user operation ends the automatic check of either download (A6). The new add's
	// slot is taken before the stopped download's, as the check takes them.
	m.endCheck(id, false)
	var src *entry
	if from != 0 {
		m.endCheck(from, true)
		if src, err = m.acquire(ctx, from); err != nil {
			return nil, err
		}
		defer releaseOp(src)
		m.mu.Lock()
		ok := src.run == nil && src.rec.State == model.StateAwaitingConfirmation && src.rec.Kind == model.KindFile
		fromReq, fromPage := src.rec.Request, src.rec.PageURL
		fromSrc = src.rec.Source
		m.mu.Unlock()
		if !ok {
			return nil, errPrecondition("download %d is not a new file download awaiting confirmation", from)
		}
		req = model.Request{URL: fromReq.URL, Method: http.MethodGet, Headers: fromReq.Headers.Clone()}
		if page == "" {
			page = fromPage
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
	stored := e.rec
	switch {
	case stored.Kind != model.KindFile:
		m.mu.Unlock()
		return nil, errInvalid("download %d is a stream or web page video: it cannot take a new link", id)
	case e.run != nil:
		m.mu.Unlock()
		return nil, errPrecondition("download %d is running; pause it first", id)
	case stored.State == model.StateCompleted:
		m.mu.Unlock()
		return nil, errPrecondition("download %d is already completed", id)
	case stored.State == model.StateAwaitingConfirmation:
		m.mu.Unlock()
		return nil, errAwaiting(id)
	}
	wasQueued := m.parkLocked(e)
	m.mu.Unlock()

	if from == 0 && len(req.Headers) == 0 {
		req.Headers = engine.RenewHeaders(stored.Request, req.URL)
	}
	lk, err := m.lockForChange(id)
	if err != nil {
		m.restore(e, wasQueued)
		return nil, err
	}
	proof, err := m.eng.ProveLink(ctx, id, req, engine.ProveOptions{RestartIfUnproven: r.GetRestartIfUnproven()})
	if err == nil && proof.NameDiffers && !r.GetAllowNameChange() {
		err = &engine.NameError{ID: id, New: proof.RemoteName, Old: stored.Filename}
	}
	if err == nil {
		proof.PageURL = page
		err = m.eng.Renew(context.WithoutCancel(ctx), id, proof, r.GetStart())
	}
	lk.Release()
	if err != nil {
		m.restore(e, wasQueued)
		m.log.Info("new link refused", "download", int64(id), "code", renewErrorCode(err), "err", err)
		return nil, renewRefusal(ctx, id, err)
	}

	sctx := context.WithoutCancel(ctx)
	// The progress window of a renewal that starts now (the Confirm download dialog's
	// "Continue that download"): downexd opens it, from the source of the add whose link it
	// took (a browser add's comes above the browser).
	source := stored.Source
	if src != nil {
		source = fromSrc
	}
	pd, launch := m.afterRenewal(sctx, e, r.GetStart(), r.GetStart() && r.GetProgressWindow(), source)
	m.hub.publish(e.id, kindUpdated)
	if launch != nil {
		go launch()
	}
	if src != nil {
		if err := m.removeAcquired(sctx, src, true); err != nil {
			m.log.Warn("cannot remove the download whose link continued another", "download", int64(from), "err", err)
		}
	}
	m.log.Info("download renewed", "download", int64(id), "host", hostOf(req.URL), "restarted", proof.Fresh,
		"compared_bytes", proof.Compared, "from_download", int64(from), "opening", launch != nil)
	return &downexv1.RenewLinkResponse{Download: pd, Restarted: proof.Fresh, NameDiffers: proof.NameDiffers,
		ProgressWindowOpening: launch != nil}, nil
}

// afterRenewal re-reads e's record after engine.Renew stored it, queues it when it was
// started (queue) and returns it as clients see it. Offers to continue e are dropped.
// window: its progress window opens when it starts now (show_progress_window on and a
// window to open): the run is marked, and launch, non-nil then, opens the window from
// source src; the caller runs it on a goroutine of its own (showProgressLocked).
func (m *Manager) afterRenewal(ctx context.Context, e *entry, queue, window bool, src model.Source) (pd *downexv1.Download, launch func()) {
	rec, err := m.st.GetDownload(ctx, e.id)
	var received int64
	if err == nil {
		received, err = m.storedBytes(ctx, rec)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		// The engine stored it; the next operation reads it again (freshen).
		m.log.Warn("cannot read the renewed download", "download", int64(e.id), "err", err)
		e.stale = true
		e.rec.Error, e.errCode = "", ""
		e.rec.State = model.StatePaused
		if queue {
			e.rec.State = model.StateQueued
		}
	} else {
		e.rec, e.received, e.errCode, e.stale = *rec, received, "", false
	}
	if queue && e.rec.State == model.StateQueued && !m.closed {
		m.enqueueLocked(e.id)
	}
	m.unparkLocked(e)
	m.dropOffersLocked(e.id)
	m.scheduleLocked()
	if window && e.run != nil && !m.closed && m.settings.ShowProgressWindow && m.cfg.OpenProgress != nil {
		launch = m.showProgressLocked(e, src)
	}
	return m.protoLocked(e), launch
}

// hostOf is rawURL's host for logs (never its path or query).
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Host
}

// parsePageURL validates a page address of an add or a renewal: "" (unknown), or an
// http or https URL without user information of at most 32 KiB.
func parsePageURL(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if len(raw) > store.MaxPageURLBytes {
		return "", fmt.Errorf("page_url is longer than %d bytes", store.MaxPageURLBytes)
	}
	if err := validate.URL(raw); err != nil {
		return "", fmt.Errorf("page_url: %w", err)
	}
	if u, _ := url.Parse(raw); u.User != nil {
		return "", errors.New("page_url must not carry a user name or password")
	}
	return raw, nil
}

// renewErrorCode is the code of a refused renewal (docs/DAEMON-SPEC.md §6).
func renewErrorCode(err error) string {
	for _, c := range []sentinelCode{
		{engine.ErrLinkMismatch, CodeRenewMismatch},
		{engine.ErrLinkSize, CodeRenewSize},
		{engine.ErrLinkChanged, CodeRenewChanged},
		{engine.ErrLinkNoRanges, CodeRenewNoRanges},
		{engine.ErrLinkUnproven, CodeRenewUnproven},
	} {
		if errors.Is(err, c.err) {
			return c.code
		}
	}
	var name *engine.NameError
	if errors.As(err, &name) {
		return CodeRenewName
	}
	return ErrorCode(err)
}

// renewRefusal maps a failed proof or renewal of download id to its RPC error.
func renewRefusal(ctx context.Context, id model.DownloadID, err error) error {
	switch {
	case ctx.Err() != nil && isContextError(err):
		return ctx.Err()
	case isContextError(err):
		return connect.NewError(connect.CodeDeadlineExceeded, fmt.Errorf("checking the new link took too long: %w", err))
	case errors.Is(err, engine.ErrAlreadyRunning):
		return errPrecondition("download %d is running; pause it first", id)
	case errors.Is(err, engine.ErrLinkStale):
		return errPrecondition("download %d changed during the check; try again", id)
	case errors.Is(err, engine.ErrInvalidRequest), errors.Is(err, transport.ErrInvalidRequest):
		return mediaError(CodeInvalidRequest, connect.CodeInvalidArgument, err)
	}
	code := renewErrorCode(err)
	if code == CodeInternal {
		return errInternal("renew download", err)
	}
	return mediaError(code, connect.CodeFailedPrecondition, err)
}

// ---- automatic matching ------------------------------------------------------------

// renewCandidates returns the downloads whose link expired that a new add in may
// continue (A1-A3), newest first, at most MaxRenewCandidates: same name (the add's
// name hint, else its URL's last segment, without a " (n)" variant, against the
// download's remote name or its file name without variant) and, when the add has a size
// hint, the same size. Only browser, clipboard and Add dialog adds of a plain GET are
// matched, and only while renew_links is not off.
func (m *Manager) renewCandidates(in addInput, s Settings) []model.DownloadID {
	if s.Desktop.RenewMode() == RenewLinksOff || len(in.req.Body) > 0 || in.req.Method != http.MethodGet {
		return nil
	}
	switch in.source {
	case model.SourceBrowser, model.SourceClipboard, model.SourceUI:
	default:
		return nil
	}
	raw := in.filename
	if raw == "" {
		raw = transport.FilenameFromURL(in.req.URL)
	}
	if raw == "" {
		return nil
	}
	key := engine.NameKey(engine.StripVariant(raw))
	type cand struct {
		id      model.DownloadID
		updated time.Time
	}
	var found []cand
	m.mu.Lock()
	for id, e := range m.entries {
		d := &e.rec
		if e.foreign || e.stale || e.run != nil || e.check != nil || len(e.op) > 0 || d.Kind != model.KindFile ||
			d.State != model.StateFailed || e.errCode != CodeLinkExpired || d.Mode != model.ModeRanged || e.received <= 0 {
			continue
		}
		if in.expectedSize > 0 && in.expectedSize != d.Size {
			continue
		}
		if key != engine.NameKey(d.RemoteName) && key != engine.NameKey(engine.StripVariant(d.Filename)) {
			continue
		}
		found = append(found, cand{id, d.UpdatedAt})
	}
	m.mu.Unlock()
	slices.SortFunc(found, func(a, b cand) int {
		if c := b.updated.Compare(a.updated); c != 0 {
			return c
		}
		return cmp.Compare(b.id, a.id)
	})
	out := make([]model.DownloadID, 0, min(len(found), MaxRenewCandidates))
	for _, c := range found[:min(len(found), MaxRenewCandidates)] {
		out = append(out, c.id)
	}
	return out
}

// holdLocked holds new add e while its link is checked against candidates (A4): it
// is not queued, asked about or shown until runRenewCheck ends the check; its queue
// counts it as work meanwhile.
func (m *Manager) holdLocked(e *entry, candidates []model.DownloadID, awaiting bool) {
	c := &renewCheck{candidates: candidates, req: e.rec.Request, awaiting: awaiting}
	c.ctx, c.cancel = context.WithTimeout(m.base, RenewCheckBudget)
	e.check = c
	e.parked = true
	m.wg.Add(1)
	go m.runRenewCheck(e, c)
}

// endCheck ends the automatic check of download id, if any, for a user operation (A6):
// the check's result is discarded and the add is released as it would have been
// without one (quiet: without asking for its confirmation; Remove and Confirm). The
// caller then waits for the operation slot, which the check frees promptly.
func (m *Manager) endCheck(id model.DownloadID, quiet bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.entries[id]; e != nil && e.check != nil && !e.check.ended {
		e.check.ended, e.check.quiet = true, quiet
		e.check.cancel()
	}
}

// runRenewCheck checks new add e against the candidates of c (A5) and applies the
// outcome: continue the matching download (auto), offer it (ask), or release the add
// as if there had been no check (A9).
func (m *Manager) runRenewCheck(e *entry, c *renewCheck) {
	defer m.wg.Done()
	defer c.cancel()
	select {
	case e.op <- struct{}{}:
	case <-m.schedDone: // shutting down: the record stays as stored (A10)
		return
	}
	defer releaseOp(e)

	m.mu.Lock()
	mode := m.settings.Desktop.RenewMode()
	m.mu.Unlock()
	win := m.proveCandidates(e, c, mode == RenewLinksAsk)

	m.mu.Lock()
	ended := c.ended || m.closed || m.entries[e.id] != e || e.check != c
	mode = m.settings.Desktop.RenewMode()
	m.mu.Unlock()
	if win != nil && !ended {
		switch {
		case mode == RenewLinksAuto && !win.proof.NameDiffers:
			if m.applyRenewal(e, c, win) {
				return
			}
		case mode == RenewLinksAsk && m.confirm.canAsk():
			if m.offerRenewal(e, c, win) {
				return
			}
		}
	}
	if win != nil {
		win.release()
	}
	m.releaseCheck(e, c)
}

// proveCandidates proves the new add's link against each candidate in turn (A5),
// holding the candidate's operation slot and lock; busy candidates are skipped. The
// first proven match wins (with a different name only when nameOK). Nothing else
// matches: errors, mismatches, Fresh, the budget running out.
func (m *Manager) proveCandidates(e *entry, c *renewCheck, nameOK bool) *renewWin {
	for _, id := range c.candidates {
		if c.ctx.Err() != nil {
			return nil
		}
		ce, lk, ok := m.takeCandidate(id)
		if !ok {
			continue
		}
		p, err := m.eng.ProveLink(c.ctx, id, c.req, engine.ProveOptions{})
		if err == nil && !p.Fresh && (nameOK || !p.NameDiffers) {
			m.log.Info("new download serves the same file as a download whose link expired", "download", int64(e.id),
				"continues", int64(id), "compared_bytes", p.Compared, "name_differs", p.NameDiffers)
			return &renewWin{e: ce, lk: lk, proof: p}
		}
		m.log.Info("new download does not continue a download whose link expired", "download", int64(e.id),
			"candidate", int64(id), "fresh", p.Fresh, "name_differs", p.NameDiffers, "code", renewErrorCode(err))
		lk.Release()
		releaseOp(ce)
	}
	return nil
}

// takeCandidate takes candidate id's operation slot without waiting and its lock, when it
// still waits for a new link (A2).
func (m *Manager) takeCandidate(id model.DownloadID) (*entry, *dlock.Lock, bool) {
	waits := func(ce *entry) bool {
		return m.entries[id] == ce && !ce.foreign && !ce.stale && ce.run == nil &&
			ce.rec.State == model.StateFailed && ce.errCode == CodeLinkExpired
	}
	m.mu.Lock()
	ce := m.entries[id]
	ok := ce != nil && waits(ce)
	m.mu.Unlock()
	if !ok {
		return nil, nil, false
	}
	select {
	case ce.op <- struct{}{}:
	default:
		return nil, nil, false // an operation acts on it: skipped
	}
	m.mu.Lock()
	ok = waits(ce)
	m.mu.Unlock()
	if !ok {
		releaseOp(ce)
		return nil, nil, false
	}
	lk, err := m.lockForChange(id)
	if err != nil {
		releaseOp(ce)
		return nil, nil, false
	}
	return ce, lk, true
}

// applyRenewal continues the proven download with the new add's link and removes the
// add (A7). It reports false, changing nothing, when the renewal could not be stored.
func (m *Manager) applyRenewal(e *entry, c *renewCheck, win *renewWin) bool {
	ctx, cancel := context.WithTimeout(context.Background(), renewApplyTimeout)
	defer cancel()
	old := win.e
	m.mu.Lock()
	p := win.proof
	p.PageURL = e.rec.PageURL
	src := e.rec.Source
	m.mu.Unlock()
	if err := m.eng.Renew(ctx, old.id, p, true); err != nil {
		m.log.Warn("cannot continue the download with the new link", "download", int64(old.id), "err", err)
		return false
	}
	win.lk.Release() // before the scheduler may start it
	win.lk = nil
	// Its progress window opens when the add's would have (a hand-off that starts now).
	_, launch := m.afterRenewal(ctx, old, true, !c.awaiting && handOff(src), src)
	m.mu.Lock()
	pdOld := m.protoLocked(old)
	m.notify(Notice{Kind: NoticeLinkRenewed, Download: pdOld})
	e.check, e.parked = nil, false
	m.mu.Unlock()
	releaseOp(old)
	m.hub.publish(old.id, kindUpdated)
	if err := m.removeAcquired(ctx, e, true); err != nil {
		m.log.Warn("cannot remove the new download whose link continued another", "download", int64(e.id), "err", err)
	}
	m.log.Info("download renewed", "download", int64(old.id), "host", hostOf(c.req.URL), "restarted", false,
		"compared_bytes", p.Compared, "from_download", int64(e.id))
	if launch != nil {
		go launch()
	}
	return true
}

// offerRenewal makes new add e wait for confirmation with an offer to continue the
// proven download instead (A8). A queued or paused add is stored as awaiting
// confirmation first; its reserved, empty part file goes (its record keeps the name).
// It reports false, changing nothing, when that could not be stored.
func (m *Manager) offerRenewal(e *entry, c *renewCheck, win *renewWin) bool {
	m.mu.Lock()
	rec := e.rec
	m.mu.Unlock()
	if rec.State != model.StateAwaitingConfirmation {
		if rec.State != model.StateQueued && rec.State != model.StatePaused {
			return false
		}
		part := filePath(&rec)
		rec.State, rec.UpdatedAt = model.StateAwaitingConfirmation, time.Now()
		lk, err := m.lockForChange(e.id)
		if err != nil {
			return false
		}
		err = m.st.UpdateDownload(context.Background(), &rec)
		lk.Release()
		if err != nil {
			m.log.Warn("cannot store the new download as awaiting confirmation", "download", int64(e.id), "err", err)
			return false
		}
		if fi, err := os.Stat(part); err == nil && fi.Mode().IsRegular() && fi.Size() == 0 {
			_ = os.Remove(part)
		}
	}
	m.mu.Lock()
	e.rec = rec
	e.offer = &renewOffer{old: win.e.id, nameDiffers: win.proof.NameDiffers}
	e.check, e.parked = nil, false
	pd := m.protoLocked(e)
	m.wg.Add(1)
	m.mu.Unlock()
	win.release()
	m.hub.publish(e.id, kindUpdated)
	go func() {
		defer m.wg.Done()
		m.confirm.request(pd)
	}()
	m.log.Info("new download offers to continue a download whose link expired", "download", int64(e.id),
		"continues", int64(win.e.id), "name_differs", win.proof.NameDiffers)
	return true
}

// releaseCheck ends c, the check of new add e, without a renewal (A9): the add is
// queued, asked about or shown as Add would have done (a quiet end asks nothing; one
// that a user operation ended opens no progress window and leaves the scheduling to it).
func (m *Manager) releaseCheck(e *entry, c *renewCheck) {
	m.mu.Lock()
	if e.check == c {
		e.check, e.parked = nil, false
	}
	current := m.entries[e.id] == e && !m.closed
	ask := false
	var launch func()
	src := e.rec.Source
	// A queued add always goes back into the queue and the scheduler runs (a quiet end
	// too: the operation that ended the check may refuse it or change nothing, and then
	// nothing else would start it; one that pauses or removes it stops what started);
	// only asking and showing depend on how the check ended.
	if current && e.run == nil {
		switch e.rec.State {
		case model.StateQueued:
			if _, waiting := m.waiting[e.id]; !waiting {
				m.enqueueLocked(e.id)
				m.scheduleLocked()
				if !c.ended && !c.awaiting && handOff(e.rec.Source) && e.run != nil &&
					m.settings.ShowProgressWindow && m.cfg.OpenProgress != nil {
					launch = m.showProgressLocked(e, src)
				}
			}
		case model.StateAwaitingConfirmation:
			ask = !c.quiet
		}
	}
	var pd *downexv1.Download
	if current {
		pd = m.protoLocked(e)
	}
	if ask {
		m.wg.Add(1)
	}
	m.mu.Unlock()
	if !current {
		return // nothing to ask or show (a launch is made only for the entry indexed now)
	}
	m.hub.publish(e.id, kindUpdated)
	if launch != nil {
		go launch()
	}
	if ask {
		go func() {
			defer m.wg.Done()
			m.confirm.request(pd)
		}()
	}
}

// dropOffersLocked drops the offers to continue download id (it was renewed, removed or
// completed) and publishes the downloads that showed one.
func (m *Manager) dropOffersLocked(id model.DownloadID) {
	for oid, o := range m.entries {
		if o.offer != nil && o.offer.old == id {
			o.offer = nil
			m.hub.publish(oid, kindUpdated)
		}
	}
}

// renewOfferLocked renders e's offer while the download it names still waits.
func (m *Manager) renewOfferLocked(e *entry) *downexv1.RenewOffer {
	o := e.offer
	if o == nil {
		return nil
	}
	old := m.entries[o.old]
	if old == nil || old.run != nil || old.rec.State == model.StateCompleted {
		return nil
	}
	return &downexv1.RenewOffer{
		DownloadId: int64(old.id), Filename: old.rec.Filename, Size: old.rec.Size, Received: old.received,
		NameDiffers: o.nameDiffers,
	}
}
