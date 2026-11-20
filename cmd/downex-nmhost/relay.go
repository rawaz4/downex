package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"connectrpc.com/connect"

	"downex/internal/client"
	downexv1 "downex/internal/rpc/downex/v1"
)

// Relay limits. The extension times its requests (hello 5 s, status 5 s, a capture's
// add 3 s on Chromium and 1.5 s on Firefox, sent as deadlineMs); every answer must come
// before the extension gives up.
const (
	// maxInFlight bounds the requests served at once; reading pauses while it is reached.
	maxInFlight = 16
	// maxVideoInFlight bounds the video pool's requests (probeVideo, addVideo,
	// probeStream) served at once. They hold their own slots, never one of maxInFlight,
	// so slow page resolutions and manifest probes never delay capture adds
	// (docs/SITE-MEDIA-SPEC.md §3.5, docs/VIDEO-DETECT-SPEC.md §3.5).
	maxVideoInFlight = 4
	// maxVideoWaiting bounds the video requests waiting for or holding a slot; more are
	// refused at once, so reading never pauses for them.
	maxVideoWaiting = 4 * maxVideoInFlight
	// maxFetchInFlight bounds the browser fetches' takes and puts served at once
	// (docs/SITE-MEDIA-SPEC.md §2.8): their own pool, so a take's long wait never holds
	// an add's or a video request's slot. More wait for a slot within their bound.
	maxFetchInFlight = 4
	// fetchPutTimeout bounds a put (it waits until the download has read its bytes);
	// fetchTakeMargin is added to a take's wait.
	fetchPutTimeout = 30 * time.Second
	fetchTakeMargin = 5 * time.Second
	// addTimeout bounds an add, including a daemon autostart (≤ 3 s); an add's
	// deadlineMs can only shorten it.
	addTimeout = 15 * time.Second
	// videoTimeout bounds a video request, its wait for a slot included; deadlineMs can
	// only shorten it. The daemon answers "try again" codes before it (its wait budget
	// ends 2 s before the deadline it is given, at most 25 s after it got the request).
	videoTimeout = 30 * time.Second
	// responseMargin is kept free before an add's deadlineMs for the answer to reach
	// the extension.
	responseMargin = 200 * time.Millisecond
	// minAddTime is the least time left before an add's deadline for which the relay
	// still sends DownloadService.Add; with less, the daemon could commit a download
	// the extension has already left to the browser.
	minAddTime = 150 * time.Millisecond
	// rescueTimeout bounds a rescue, a daemon autostart and the daemon's check of the
	// partial file with the server included; deadlineMs can only shorten it.
	rescueTimeout = 30 * time.Second
	// helloTimeout bounds a hello's wait for the daemon (its start and Status).
	helloTimeout = time.Second
	// queryTimeout bounds status.
	queryTimeout = 3 * time.Second
)

// errorCodeMeta is the Connect error metadata that carries the daemon's stable error
// code (internal/daemon ErrorCodeMeta), forwarded to the extension as "reason".
const errorCodeMeta = "Downex-Error-Code"

// backend is the daemon as the relay uses it.
type backend interface {
	// warm starts downexd when none runs, so that later adds find it running.
	warm(ctx context.Context) error
	// add forwards req to DownloadService.Add, starting downexd first when needed. It
	// does not send the Add when less than minAddTime is left before ctx's deadline.
	add(ctx context.Context, req *downexv1.AddRequest) (*downexv1.Download, error)
	// addMedia forwards req to DownloadService.AddMedia like add (autostart, deadline),
	// and returns the download and its fetch ticket ("" = none).
	addMedia(ctx context.Context, req *downexv1.AddMediaRequest) (*downexv1.Download, string, error)
	// probeMedia forwards req to DownloadService.ProbeMedia, starting downexd first when
	// needed. It has no side effects.
	probeMedia(ctx context.Context, req *downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error)
	// status returns the daemon's status, or nil when no daemon is running. It never
	// starts one.
	status(ctx context.Context) (*downexv1.StatusResponse, error)
	// rescue forwards req to DownloadService.Rescue, starting downexd first when needed
	// (a user's click), and returns the download and the bytes it kept.
	rescue(ctx context.Context, req *downexv1.RescueRequest) (*downexv1.Download, int64, error)
	// resume sends DownloadService.Resume with rescue_start for download id: the first
	// start of a rescue stored paused.
	resume(ctx context.Context, id int64) (*downexv1.Download, error)
	// takeFetch and putFetch forward a browser fetch's take and put
	// (docs/SITE-MEDIA-SPEC.md §2.8). Neither starts downexd.
	takeFetch(ctx context.Context, req *downexv1.TakeBrowserFetchRequest) (*downexv1.TakeBrowserFetchResponse, error)
	putFetch(ctx context.Context, req *downexv1.PutBrowserFetchRequest) (*downexv1.PutBrowserFetchResponse, error)
}

// relay serves native-messaging requests from a browser.
type relay struct {
	be  backend
	log *slog.Logger
	bg  sync.WaitGroup // background work (daemon warm-ups), awaited by serve
}

// serve reads requests from in until it ends and writes responses to out. Requests
// are served concurrently, so responses may come out of order; each echoes its
// request's id. It returns nil when in ends at a message boundary (the browser closed
// the port), and an error for a message that cannot be answered (unframed, oversized,
// empty, or without a usable id) or a failed read: the browser then sees the host exit.
// Requests still in progress when serve returns are canceled.
func (r *relay) serve(ctx context.Context, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	fw := &frameWriter{w: out}
	sem := make(chan struct{}, maxInFlight)
	video := make(chan struct{}, maxVideoInFlight)
	videoWaiting := make(chan struct{}, maxVideoWaiting)
	fetches := make(chan struct{}, maxFetchInFlight)
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
		r.bg.Wait()
	}()
	for {
		msg, err := readFrame(in)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		received := time.Now()
		req, rerr := parseRequest(msg)
		if rerr != nil {
			if rerr.id == "" {
				return rerr
			}
			r.log.Warn("invalid request", "id", truncate(rerr.id, 64), "error", rerr.msg)
			r.reply(fw, failure(rerr.id, codeInvalidRequest, rerr.msg))
			continue
		}
		req.received = received
		if req.typ == typeFetchTake || req.typ == typeFetchPut {
			wg.Add(1)
			go func() {
				defer wg.Done()
				select {
				case fetches <- struct{}{}:
				case <-ctx.Done():
					return
				}
				defer func() { <-fetches }()
				r.reply(fw, r.handle(ctx, req))
			}()
			continue
		}
		if req.video() {
			select {
			case videoWaiting <- struct{}{}:
			default:
				r.log.Warn("video request refused: too many at once", "id", truncate(req.id, 64), "type", req.typ)
				r.reply(fw, failure(req.id, codeDaemonUnavailable, "too many video requests at once; try again"))
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-videoWaiting }()
				r.reply(fw, r.handleVideo(ctx, req, video))
			}()
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			r.reply(fw, r.handle(ctx, req))
		}()
	}
}

func (r *relay) reply(fw *frameWriter, resp response) {
	if err := fw.write(resp.encode()); err != nil {
		r.log.Warn("cannot send a response", "error", err)
	}
}

// handleVideo serves a video request once one of the video slots is free; the wait
// counts against the request's deadline.
func (r *relay) handleVideo(ctx context.Context, req *request, slots chan struct{}) response {
	select {
	case slots <- struct{}{}:
	default:
		wctx, cancel := context.WithDeadline(ctx, requestDeadline(req, videoTimeout))
		defer cancel()
		select {
		case slots <- struct{}{}:
		case <-wctx.Done():
			r.log.Warn("video request not served: no free slot before its deadline", "id", truncate(req.id, 64), "type", req.typ)
			return failure(req.id, codeDaemonUnavailable, "downexd did not answer in time")
		}
	}
	defer func() { <-slots }()
	return r.handle(ctx, req)
}

// handle serves one valid request.
func (r *relay) handle(ctx context.Context, req *request) response {
	start := time.Now()
	attrs := []any{"id", truncate(req.id, 64), "type", req.typ}
	resp := r.dispatch(ctx, req)
	attrs = append(attrs, "ms", time.Since(start).Milliseconds())
	switch req.typ {
	case typeAdd:
		attrs = append(attrs, "url", redactURL(req.info.url), "method", req.info.method,
			"headers", req.info.headers, "body_bytes", req.info.body, "mime", req.info.mime, "size", req.info.size,
			"start_paused", req.add.GetStartPaused(), "deadline_ms", req.info.deadline.Milliseconds(),
			"page_host", req.info.pageHost)
	case typeAddMedia:
		attrs = append(attrs, "manifest", redactURL(req.info.url), "headers", req.info.headers,
			"title_chars", req.info.title, "video_track", truncate(req.media.GetVideoTrack(), 64),
			"audio_track", truncate(req.media.GetAudioTrack(), 64), "deadline_ms", req.info.deadline.Milliseconds())
	case typeProbeStream:
		attrs = append(attrs, "manifest", redactURL(req.info.url), "headers", req.info.headers,
			"deadline_ms", req.info.deadline.Milliseconds())
	case typeProbeVideo:
		attrs = append(attrs, "page", redactURL(req.info.url), "headers", req.info.headers,
			"prefetch", req.probe.GetPrefetch(), "deadline_ms", req.info.deadline.Milliseconds())
	case typeAddVideo:
		attrs = append(attrs, "page", redactURL(req.info.url), "headers", req.info.headers,
			"title_chars", req.info.title, "video_track", truncate(req.media.GetVideoTrack(), 64),
			"audio_track", truncate(req.media.GetAudioTrack(), 64), "deadline_ms", req.info.deadline.Milliseconds())
	case typeProbePlayed, typeAddPlayed:
		attrs = append(attrs, "page_host", playedHost(req.info.url), "files", req.info.files, "hosts", req.info.hosts,
			"head_bytes", req.info.headBytes, "title_chars", req.info.title, "deadline_ms", req.info.deadline.Milliseconds())
	case typeRescue:
		attrs = append(attrs, "url", redactURL(req.info.url), "headers", req.info.headers, "target", req.info.target,
			"bytes_received", req.info.received, "total_bytes", req.info.size, "start_paused", req.rescue.GetStartPaused(),
			"deadline_ms", req.info.deadline.Milliseconds(), "page_host", req.info.pageHost)
		if kept, ok := resp["keptBytes"]; ok {
			attrs = append(attrs, "kept_bytes", kept)
		}
	case typeResume:
		attrs = append(attrs, "resume", req.resume)
	case typeFetchTake:
		attrs = append(attrs, "tickets", len(req.take.GetTickets()), "wait_ms", req.take.GetWaitMs())
		if f, ok := resp["fetch"].(map[string]any); ok {
			attrs = append(attrs, "ticket", ticketPrefix(f["ticket"]), "fetch", f["fetchId"], "start", f["start"], "end", f["end"])
		}
	case typeFetchPut:
		attrs = append(attrs, "ticket", ticketPrefix(req.put.GetTicket()), "fetch", req.put.GetFetchId(), "seq", req.put.GetSeq(),
			"bytes", len(req.put.GetData()), "end", req.put.GetEnd(), "status", req.put.GetHead().GetStatus(), "error", truncate(req.put.GetError(), 64))
	case typeHello:
		attrs = append(attrs, "browser", req.hello.browser, "ext_version", req.hello.extVersion)
	}
	if e, ok := resp["error"].(map[string]string); ok {
		attrs = append(attrs, "code", e["code"], "error", e["message"])
		if reason := e["reason"]; reason != "" {
			attrs = append(attrs, "reason", reason)
		}
		r.log.Warn("request failed", attrs...)
	} else if req.typ == typeFetchPut || (req.typ == typeFetchTake && resp["fetch"] == nil) {
		// Every piece of a video and every empty wait: not worth an info line each.
		r.log.Debug("request served", attrs...)
	} else {
		if id, ok := resp["downloadId"]; ok {
			attrs = append(attrs, "download", id)
		}
		r.log.Info("request served", attrs...)
	}
	return resp
}

// allowForeground hands the browser's right to the foreground on (handForeground); tests
// replace it.
var allowForeground = handForeground

func (r *relay) dispatch(ctx context.Context, req *request) response {
	switch req.typ {
	case typeAdd, typeAddMedia, typeAddVideo, typeAddPlayed, typeRescue:
		// The user just started a download in the browser: Downex's window for it may
		// come to the front over the browser.
		if !allowForeground() {
			r.log.Debug("the foreground could not be handed on")
		}
	}
	switch req.typ {
	case typePing:
		return success(req.id, "pong", nil)
	case typeHello:
		// A hello never fails because of the daemon, and it is answered within
		// helloTimeout, long before the extension gives up on it and drops the
		// connection. It starts downexd in the background, so that the first capture
		// does not pay for a cold start; daemonVersion is "" until the daemon answers.
		hctx, cancel := context.WithTimeout(ctx, helloTimeout)
		defer cancel()
		select {
		case <-r.warm(ctx):
		case <-hctx.Done():
		}
		v := ""
		st, err := r.be.status(hctx)
		if err != nil {
			r.log.Info("daemon status unavailable during hello", "error", err)
		} else if st != nil {
			v = st.GetVersion()
		}
		return success(req.id, "welcome", map[string]any{"protocol": protocolVersion, "daemonVersion": v, "features": features})
	case typeStatus:
		qctx, cancel := context.WithTimeout(ctx, queryTimeout)
		defer cancel()
		st, err := r.be.status(qctx)
		if err != nil {
			return failed(req.id, err)
		}
		return success(req.id, "status", statusFields(st))
	case typeAdd:
		actx, cancel := context.WithDeadline(ctx, requestDeadline(req, addTimeout))
		defer cancel()
		d, err := r.be.add(actx, req.add)
		if err != nil {
			return failed(req.id, err)
		}
		return added(req.id, d)
	case typeAddMedia:
		// The daemon fetches and parses the manifest before it answers; a stream it
		// cannot download is `rejected` with a message that starts with its code
		// ("encrypted: ...", "live: ...").
		actx, cancel := context.WithDeadline(ctx, requestDeadline(req, addTimeout))
		defer cancel()
		d, _, err := r.be.addMedia(actx, req.media)
		if err != nil {
			return failed(req.id, err)
		}
		return added(req.id, d)
	case typeProbeVideo, typeProbeStream, typeProbePlayed:
		// probeVideo: the daemon resolves the page with yt-dlp (downloading it first on
		// first use) within its wait budget; past it, it answers tools_installing or
		// site_resolving, forwarded as reason, and the extension asks again.
		// probeStream: the daemon fetches the manifest and its media playlists and lists
		// the tracks it can save as MP4; refusals carry their code as reason.
		vctx, cancel := context.WithDeadline(ctx, requestDeadline(req, videoTimeout))
		defer cancel()
		info, err := r.be.probeMedia(vctx, req.probe)
		if err != nil {
			return failed(req.id, err)
		}
		fields, err := videoInfo(info)
		if err != nil {
			return failure(req.id, codeInternal, err.Error())
		}
		if resp := success(req.id, "videoInfo", fields); resp.fits(maxVideoResponseBytes) {
			return resp
		}
		// Chapters are dropped first: the qualities are what the answer is for
		// (docs/SITE-MEDIA-SPEC.md §3.5).
		fields["chapters"] = []map[string]any{}
		if resp := success(req.id, "videoInfo", fields); resp.fits(maxVideoResponseBytes) {
			return resp
		}
		// Then the items' thumbnails: the items themselves are needed to choose one.
		withoutThumbnails(fields)
		if resp := success(req.id, "videoInfo", fields); resp.fits(maxVideoResponseBytes) {
			return resp
		}
		return failure(req.id, codeInternal, fmt.Sprintf("the video info is larger than %d bytes", maxVideoResponseBytes))
	case typeRescue:
		// downexd proves the partial file with a few range requests before it moves it;
		// refusals carry their code as reason (rescue_changed, rescue_not_found, ...).
		rctx, cancel := context.WithDeadline(ctx, requestDeadline(req, rescueTimeout))
		defer cancel()
		d, kept, err := r.be.rescue(rctx, req.rescue)
		if err != nil {
			return failed(req.id, err)
		}
		return rescued(req.id, d, kept)
	case typeResume:
		rctx, cancel := context.WithDeadline(ctx, requestDeadline(req, addTimeout))
		defer cancel()
		d, err := r.be.resume(rctx, req.resume)
		if err != nil {
			return failed(req.id, err)
		}
		return resumed(req.id, d)
	case typeAddVideo, typeAddPlayed:
		vctx, cancel := context.WithDeadline(ctx, requestDeadline(req, videoTimeout))
		defer cancel()
		d, ticket, err := r.be.addMedia(vctx, req.media)
		if err != nil {
			return failed(req.id, err)
		}
		return addedVideo(req.id, d, ticket)
	case typeFetchTake:
		tctx, cancel := context.WithTimeout(ctx, time.Duration(req.take.GetWaitMs())*time.Millisecond+fetchTakeMargin)
		defer cancel()
		resp, err := r.be.takeFetch(tctx, req.take)
		if err != nil {
			return failed(req.id, err)
		}
		return fetchTask(req.id, resp)
	case typeFetchPut:
		pctx, cancel := context.WithTimeout(ctx, fetchPutTimeout)
		defer cancel()
		resp, err := r.be.putFetch(pctx, req.put)
		if err != nil {
			return failed(req.id, err)
		}
		return success(req.id, "fetchAck", map[string]any{"stop": resp.GetStop()})
	}
	return failure(req.id, codeInternal, "unhandled request type")
}

// requestDeadline is when a request must be done: timeout after it was received or,
// with deadlineMs, responseMargin before the extension stops waiting for it.
func requestDeadline(req *request, timeout time.Duration) time.Time {
	budget := timeout
	if d := req.info.deadline; d > 0 {
		budget = min(budget, d-responseMargin)
	}
	from := req.received
	if from.IsZero() {
		from = time.Now()
	}
	return from.Add(budget)
}

// warm starts downexd in the background (bounded by addTimeout and by ctx, the
// relay's lifetime) and returns a channel closed when that is done.
func (r *relay) warm(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	r.bg.Add(1)
	go func() {
		defer r.bg.Done()
		defer close(done)
		wctx, cancel := context.WithTimeout(ctx, addTimeout)
		defer cancel()
		if err := r.be.warm(wctx); err != nil {
			r.log.Info("downexd could not be started ahead of an add", "error", err)
		}
	}()
	return done
}

// unavailableError is a daemon that cannot be reached or started.
type unavailableError struct{ err error }

func (e *unavailableError) Error() string { return e.err.Error() }
func (e *unavailableError) Unwrap() error { return e.err }

// failed is the failure response of a backend error, with the daemon's error code as
// reason when it sent one.
func failed(id string, err error) response {
	code, msg, reason := classify(err)
	return failure(id, code, msg).withReason(reason)
}

// classify maps a backend error to a response code and message, and the daemon's
// stable error code (errorCodeMeta) when the error carries one.
func classify(err error) (code, msg, reason string) {
	var ue *unavailableError
	if errors.As(err, &ue) {
		return codeDaemonUnavailable, err.Error(), ""
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		msg = ce.Message()
		if msg == "" {
			msg = ce.Error()
		}
		reason = ce.Meta().Get(errorCodeMeta)
	} else {
		msg = err.Error()
	}
	switch connect.CodeOf(err) {
	case connect.CodeInvalidArgument, connect.CodeFailedPrecondition, connect.CodeNotFound,
		connect.CodeAlreadyExists, connect.CodePermissionDenied, connect.CodeResourceExhausted,
		connect.CodeOutOfRange, connect.CodeAborted:
		return codeRejected, msg, reason
	case connect.CodeUnavailable, connect.CodeDeadlineExceeded, connect.CodeCanceled:
		return codeDaemonUnavailable, "downexd is not reachable: " + msg, reason
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return codeDaemonUnavailable, "downexd did not answer in time", ""
	}
	return codeInternal, msg, reason
}

// daemonBackend reaches downexd through client.Env: on the per-user endpoint, started
// (once, even for concurrent adds) when an add finds nothing listening.
type daemonBackend struct {
	env client.Env

	mu       sync.Mutex // guards c and starting
	c        *client.Client
	starting chan struct{} // holds a token while an autostart runs (serialises them)
}

// conn returns the client of the endpoint, resolving it once.
func (b *daemonBackend) conn() (*client.Client, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.c == nil {
		ep, err := b.env.Endpoint()
		if err != nil {
			return nil, &unavailableError{fmt.Errorf("locate the downexd endpoint: %w", err)}
		}
		b.c = b.env.Client(ep)
	}
	return b.c, nil
}

// lockStart waits until no other request starts the daemon, or until ctx is done.
func (b *daemonBackend) lockStart(ctx context.Context) (unlock func(), err error) {
	b.mu.Lock()
	if b.starting == nil {
		b.starting = make(chan struct{}, 1)
	}
	sem := b.starting
	b.mu.Unlock()
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, nil
	case <-ctx.Done():
		return nil, &unavailableError{fmt.Errorf("downexd is starting and did not answer in time: %w", ctx.Err())}
	}
}

// errTooLate drops an add whose deadline is too close to send it.
var errTooLate = errors.New("downexd was not ready before the request's deadline; the download was not added")

// timeLeft reports whether at least d is left before ctx's deadline.
func timeLeft(ctx context.Context, d time.Duration) bool {
	dl, ok := ctx.Deadline()
	return !ok || time.Until(dl) >= d
}

// connect returns the client when a daemon runs (or, with autostart, once one was
// started), and nil without autostart when none runs.
func (b *daemonBackend) connect(ctx context.Context, autostart bool) (*client.Client, error) {
	c, err := b.conn()
	if err != nil {
		return nil, err
	}
	up, err := b.env.Running(ctx, c.Endpoint)
	switch {
	case err != nil:
		return nil, &unavailableError{err}
	case up:
		return c, nil
	case !autostart:
		return nil, nil
	}
	unlock, err := b.lockStart(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	// Another request may have started it meanwhile.
	if up, err = b.env.Running(ctx, c.Endpoint); err != nil {
		return nil, &unavailableError{err}
	} else if up {
		return c, nil
	}
	if _, err := b.env.Start(ctx, c); err != nil {
		return nil, &unavailableError{err}
	}
	return c, nil
}

func (b *daemonBackend) warm(ctx context.Context) error {
	_, err := b.connect(ctx, true)
	return err
}

func (b *daemonBackend) add(ctx context.Context, req *downexv1.AddRequest) (*downexv1.Download, error) {
	if !timeLeft(ctx, minAddTime) {
		return nil, &unavailableError{errTooLate}
	}
	c, err := b.connect(ctx, true)
	if err != nil {
		return nil, err
	}
	// The extension leaves a download to the browser once the deadline passes: an Add
	// that may complete after it would download the file twice.
	if !timeLeft(ctx, minAddTime) {
		return nil, &unavailableError{errTooLate}
	}
	resp, err := c.Downloads.Add(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return resp.Msg.GetDownload(), nil
}

func (b *daemonBackend) addMedia(ctx context.Context, req *downexv1.AddMediaRequest) (*downexv1.Download, string, error) {
	if !timeLeft(ctx, minAddTime) {
		return nil, "", &unavailableError{errTooLate}
	}
	c, err := b.connect(ctx, true)
	if err != nil {
		return nil, "", err
	}
	if !timeLeft(ctx, minAddTime) {
		return nil, "", &unavailableError{errTooLate}
	}
	resp, err := c.Downloads.AddMedia(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, "", err
	}
	return resp.Msg.GetDownload(), resp.Msg.GetFetchTicket(), nil
}

// errNoDaemonFetch answers a browser fetch while downexd does not run: its tickets ended
// with it, and a fetch never starts it.
var errNoDaemonFetch = errors.New("downexd is not running; a browser fetch never starts it")

func (b *daemonBackend) takeFetch(ctx context.Context, req *downexv1.TakeBrowserFetchRequest) (*downexv1.TakeBrowserFetchResponse, error) {
	c, err := b.connect(ctx, false)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, &unavailableError{errNoDaemonFetch}
	}
	resp, err := c.Downloads.TakeBrowserFetch(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (b *daemonBackend) putFetch(ctx context.Context, req *downexv1.PutBrowserFetchRequest) (*downexv1.PutBrowserFetchResponse, error) {
	c, err := b.connect(ctx, false)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, &unavailableError{errNoDaemonFetch}
	}
	resp, err := c.Downloads.PutBrowserFetch(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// ticketPrefix is what the log keeps of a fetch ticket: its first 8 characters.
func ticketPrefix(t any) string {
	s, _ := t.(string)
	return truncate(s, 8)
}

// errNotRunning answers a look-ahead while downexd does not run: only a user's request
// starts it (the user may have quit Downex).
var errNotRunning = errors.New("downexd is not running; a look-ahead never starts it")

func (b *daemonBackend) probeMedia(ctx context.Context, req *downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) {
	c, err := b.connect(ctx, !req.GetPrefetch())
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, &unavailableError{errNotRunning}
	}
	resp, err := c.Downloads.ProbeMedia(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return resp.Msg.GetMedia(), nil
}

func (b *daemonBackend) rescue(ctx context.Context, req *downexv1.RescueRequest) (*downexv1.Download, int64, error) {
	c, err := b.connect(ctx, true)
	if err != nil {
		return nil, 0, err
	}
	resp, err := c.Downloads.Rescue(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, 0, err
	}
	return resp.Msg.GetDownload(), resp.Msg.GetRescuedBytes(), nil
}

func (b *daemonBackend) resume(ctx context.Context, id int64) (*downexv1.Download, error) {
	c, err := b.connect(ctx, true)
	if err != nil {
		return nil, err
	}
	resp, err := c.Downloads.Resume(ctx, connect.NewRequest(&downexv1.ResumeRequest{Id: id, RescueStart: true}))
	if err != nil {
		return nil, err
	}
	return resp.Msg.GetDownload(), nil
}

func (b *daemonBackend) status(ctx context.Context) (*downexv1.StatusResponse, error) {
	c, err := b.connect(ctx, false)
	if err != nil || c == nil {
		return nil, err
	}
	resp, err := c.Daemon.Status(ctx, connect.NewRequest(&downexv1.StatusRequest{}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// runRelay serves the browser on stdin/stdout until it closes the port.
func runRelay(ctx context.Context, stdin io.Reader, stdout io.Writer, from string) int {
	logger, closeLog := openLog()
	defer closeLog()
	logger.Info("relay started", "version", version, "caller", from)
	r := &relay{be: &daemonBackend{env: client.Default()}, log: logger}
	if err := r.serve(ctx, stdin, stdout); err != nil {
		logger.Error("relay stopped", "error", err)
		return exitError
	}
	logger.Info("relay stopped: the browser closed the connection")
	return exitOK
}

// statusFields is the status answer for st (nil: no daemon runs; docs/BROWSER-SPEC.md
// §2): running, active and queued, and Polite mode's state ("off", "idle", "watching",
// "slowed", "measuring", "no_signal" or "unavailable"; left out for a daemon without
// Polite mode) with politeRate, its cap in bytes per second, while it slows downloads
// (docs/POLITE-SPEC.md §8).
func statusFields(st *downexv1.StatusResponse) map[string]any {
	f := map[string]any{"running": st != nil, "active": st.GetActive(), "queued": st.GetQueued()}
	p := st.GetPolite()
	name := politeStateName(p.GetState())
	if name == "" {
		return f
	}
	f["polite"] = name
	if p.GetState() == downexv1.PoliteState_POLITE_STATE_SLOWED {
		f["politeRate"] = p.GetRateLimit()
	}
	return f
}

// politeStateName is the relay's name of a Polite mode state ("" for an older daemon's).
func politeStateName(s downexv1.PoliteState) string {
	switch s {
	case downexv1.PoliteState_POLITE_STATE_OFF:
		return "off"
	case downexv1.PoliteState_POLITE_STATE_IDLE:
		return "idle"
	case downexv1.PoliteState_POLITE_STATE_WATCHING:
		return "watching"
	case downexv1.PoliteState_POLITE_STATE_SLOWED:
		return "slowed"
	case downexv1.PoliteState_POLITE_STATE_MEASURING:
		return "measuring"
	case downexv1.PoliteState_POLITE_STATE_NO_SIGNAL:
		return "no_signal"
	case downexv1.PoliteState_POLITE_STATE_UNAVAILABLE:
		return "unavailable"
	}
	return ""
}
