package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"downex/internal/model"
	"downex/internal/transport"
)

// workerPhase says what a worker is doing; stall and slowness rules only apply while
// it waits on the network (phaseConnecting, phaseReceiving).
type workerPhase int32

const (
	phaseIdle       workerPhase = iota // waiting for an assignment
	phaseConnecting                    // waiting for response headers
	phaseReceiving                     // waiting for body bytes
	phaseThrottled                     // waiting on the speed limiter
	phaseWriting                       // handing bytes to the writer
	phaseBackoff                       // waiting before a retry
)

// job holds the per-download values workers share. It is immutable while workers run.
type job struct {
	req        model.Request  // what segment requests send (possibly pinned, see pin.go)
	fallback   *model.Request // the original request when req is pinned, else nil
	ifRange    string
	validators model.Validators
	size       int64 // -1 when unknown (single mode)
	single     bool
	chained    bool        // single mode with an unknown total: continue with "bytes=pos-" until 416
	resumed    bool        // the session continues stored progress: a 200 means the file changed (§7)
	caps       RequestCaps // per-host request length caps; nil = none
	host       string      // the download's host (hostKey of the original URL)
	opts       *Options
	clock      Clock
	bufs       *bufPool
	lim        throttle // nil = unlimited
	writes     chan<- writeReq
	events     chan<- event
	received   *atomic.Int64 // bytes reserved by all workers this session
	log        *slog.Logger
	// epoch is the clock time when the run started. Activity times are durations
	// since it (worker.touch), so idle times follow the monotonic clock: a system clock
	// set forward stalls no connection.
	epoch time.Time
}

// assignment is the coordinator's answer to an idle worker.
type assignment struct {
	seg        *segment // nil tells the worker to exit
	keepStream bool     // keep reading the current response into seg (takeover)
}

// worker streams one segment at a time over its own transport slot.
type worker struct {
	id     int
	job    *job
	fetch  transport.Fetcher
	ctx    context.Context
	cancel context.CancelFunc
	assign chan assignment // capacity 1; one answer per idle event

	phase        atomic.Int32
	lastActivity atomic.Int64 // time of the last progress since job.epoch, in ns
	received     atomic.Int64 // bytes reserved since the worker started
	rtt          atomic.Int64 // latency of the latest request until headers, in ns
	view         liveView     // what progress events report about this connection
	// midStream: the current response delivered body bytes, so the stall timeout is
	// the mid-stream one (§5, midStallTimeout). gap: the longest wait for that
	// response's bytes, its first ones counted from the headers, in ns. Both are
	// cleared by every request.
	midStream atomic.Bool
	gap       atomic.Int64

	// Owned by the coordinator goroutine.
	seg       *segment                        // current assignment
	retired   bool                            // no longer counted as a live connection
	cancelled bool                            // cancelled by the coordinator; its exit error is irrelevant
	slot      int                             // 1-based connection slot in progress events
	firstByte time.Time                       // clock time of the tick that first saw it receive bytes
	speed     speedMeter                      // what stealing decides on
	shown     shownMeter                      // what progress events report
	last      struct{ start, end, pos int64 } // the range it held last (see noteRange)
	hadRange  bool                            // last is set

	// Owned by the worker goroutine.
	st       *stream
	unpinned bool // the pinned target refused this worker; it uses job.fallback
	// replaced: the worker's next request replaces a connection the server may still
	// count: since its last request it was served a response (whole or not: a server
	// may count a finished response a moment longer, an abandoned one until it notices),
	// or it was started for a worker cancelled with a request out (coordinator.fill, set
	// before run). Cleared by that request.
	replaced bool
}

func (w *worker) setPhase(p workerPhase) { w.phase.Store(int32(p)) }
func (w *worker) getPhase() workerPhase  { return workerPhase(w.phase.Load()) }
func (w *worker) touch()                 { w.lastActivity.Store(int64(w.elapsed())) }

// elapsed is the clock time since the run started (job.epoch).
func (w *worker) elapsed() time.Duration { return w.job.clock.Now().Sub(w.job.epoch) }

// idle is how long w has made no progress at clock time now.
func (w *worker) idle(now time.Time) time.Duration {
	return now.Sub(w.job.epoch) - time.Duration(w.lastActivity.Load())
}

// arrived notes body bytes of the current response: the wait for them since the last
// activity (the headers, for its first bytes) may raise its gap; then it touches and
// marks the response mid-stream. The gap is stored before the touch and the flag after
// it, so the stall watchdog never sees the flag with an older activity or gap.
func (w *worker) arrived() {
	now := w.elapsed()
	if gap := now - time.Duration(w.lastActivity.Load()); gap > time.Duration(w.gap.Load()) {
		w.gap.Store(int64(gap))
	}
	w.lastActivity.Store(int64(now))
	w.midStream.Store(true)
}

// noteRange remembers w's current range for the row it leaves behind once it is done
// with it. Called by the coordinator while w.seg is set.
func (w *worker) noteRange() {
	end, pos, _, _ := w.seg.snapshot()
	w.last.start, w.last.end, w.last.pos, w.hadRange = w.seg.start, end, pos, true
}

// finalView is the row of a worker that finished or failed: its last range, no speed.
func (w *worker) finalView(s model.WorkerState, detail string) model.WorkerView {
	v := model.WorkerView{Slot: w.slot, State: s, Detail: detail, Start: -1, End: -1, Fragment: -1}
	if w.hadRange {
		v.Start, v.End, v.Received = w.last.start, w.last.end, w.last.pos-w.last.start
	}
	return v
}

// run serves assignments until told to exit, cancelled, or failed. Its last action is
// an evExit event, sent after all of its writes have been queued.
func (w *worker) run(seg *segment) {
	var exitErr error
	defer func() {
		w.closeStream()
		w.fetch.Close()
		w.cancel()
		w.job.events <- event{kind: evExit, w: w, err: exitErr}
	}()
	for seg != nil {
		if err := w.serve(seg); err != nil {
			exitErr = err
			return
		}
		ev := event{kind: evIdle, w: w}
		if w.st != nil && w.st.more() {
			ev.hasStream, ev.streamOff = true, w.st.off
		}
		w.setPhase(phaseIdle)
		w.view.set(model.WorkerIdle, "")
		w.job.events <- ev
		select {
		case a := <-w.assign:
			if !a.keepStream {
				if a.seg != nil {
					// The worker owns a.seg from here on: keep it visible to stall
					// detection while the old response is released.
					w.setPhase(phaseConnecting)
					w.view.set(model.WorkerConnecting, "")
					w.touch()
				}
				w.closeStream()
			} else if a.seg != nil {
				w.view.set(model.WorkerReceiving, "") // the open response continues
			}
			seg = a.seg
		case <-w.ctx.Done():
			return
		}
	}
}

// serve downloads seg until it is complete or no longer owned by w. Transient failures
// are retried with backoff, re-requesting from the current position; it returns an
// error only when the download cannot continue on this connection.
func (w *worker) serve(seg *segment) error {
	failures := 0
	var lastErr error
	for {
		if err := w.ctx.Err(); err != nil {
			return err
		}
		end, pos, owner, done := seg.snapshot()
		if owner != w || done {
			return nil
		}
		if w.st == nil || w.st.off != pos || !w.st.more() {
			w.closeStream()
			if w.job.single && !w.job.chained { // no ranges: the probe response was the only stream
				if lastErr == nil {
					lastErr = fmt.Errorf("response ended at byte %d: %w", pos, io.ErrUnexpectedEOF)
				}
				return lastErr
			}
			if failures > 0 {
				if failures > w.job.opts.MaxRetries {
					return fmt.Errorf("giving up after %d attempts: %w", failures, lastErr)
				}
				w.setPhase(phaseBackoff)
				delay := backoffDelay(w.job.opts, failures, retryAfterOf(lastErr))
				w.view.retry(shortReason(lastErr), w.job.clock.Now().Add(delay))
				if err := sleep(w.ctx, w.job.clock, delay); err != nil {
					return err
				}
			}
			replacing := w.replaced // only the first request: a retry may be one too many
			w.replaced = false
			st, err := w.open(pos, end)
			if errors.Is(err, errEndOfFile) {
				seg.finishOpen(w, pos)
				return nil
			}
			if err != nil {
				if w.ctx.Err() != nil {
					return w.ctx.Err()
				}
				if !isTransient(err) {
					return err
				}
				failures, lastErr = failures+1, err
				w.job.log.Debug("request failed, will retry", "worker", w.id, "attempt", failures, "err", err)
				if isMisdirected(err) {
					// RFC 9110 §15.5.20: retry over a different connection.
					w.fetch.Close()
				}
				// A request that replaces a connection the download just closed adds
				// no connection, so its refusal is the server still counting the old
				// one (§10): it is retried, not taken as pushback; a refused retry is.
				if throttled := isThrottle(err); !replacing && (throttled || isRefused(err)) {
					w.job.events <- event{kind: evThrottled, w: w, err: err, refused: !throttled}
				}
				continue
			}
			w.st = st
		}
		progressed, err := w.pump(seg)
		if progressed {
			failures = 0
		}
		if err == nil {
			continue
		}
		if w.ctx.Err() != nil {
			return w.ctx.Err()
		}
		if !isTransient(err) {
			return err
		}
		failures, lastErr = failures+1, err
		w.job.log.Debug("stream failed, will retry", "worker", w.id, "attempt", failures, "err", err)
		w.closeStream()
	}
}

// errEndOfFile reports that a chained single-mode download reached the end of the
// resource: the server answered "bytes=pos-" with 416.
var errEndOfFile = errors.New("engine: end of resource")

// open requests [pos, end] with If-Range and validates the response. Under a host's
// request cap (RequestCaps) it asks only for the first cap bytes of that range: the
// stream then ends early and serve continues with a new request from there. A pinned
// target that refuses the request (an expired signature, a removed mirror) is given up
// for the original URL once.
func (w *worker) open(pos, end int64) (*stream, error) {
	// Before the phase: a new request waits for its first byte. The flag before the gap
	// (see checkStalls).
	w.midStream.Store(false)
	w.gap.Store(0)
	w.setPhase(phaseConnecting)
	w.touch()
	req := w.job.req
	if w.unpinned {
		req = *w.job.fallback
	}
	reqEnd := cappedEnd(pos, end, requestCap(w.job.caps, w.job.host, req.URL))
	t0 := w.job.clock.Now()
	tctx, gen := w.view.trace(w.ctx)
	resp, err := w.fetch.Fetch(tctx, transport.RangeRequest{
		Request: req, Start: pos, End: reqEnd, IfRange: w.job.ifRange,
	})
	if err == nil {
		w.view.step(gen, model.WorkerRequesting, "") // a response means the request was sent
	}
	w.view.end(gen)
	if err != nil {
		return nil, &noResponseError{fmt.Errorf("fetch bytes %d-%d: %w", pos, end, err)}
	}
	w.rtt.Store(int64(max(w.job.clock.Now().Sub(t0), 1)))
	if resp.Body == nil {
		resp.Body = http.NoBody
	}
	if w.job.fallback != nil && !w.unpinned && pinRefused(resp.StatusCode) {
		closeBody(resp)
		w.unpinned = true
		w.job.log.Debug("redirect target refused a segment; using the original URL", "worker", w.id, "status", resp.StatusCode)
		return w.open(pos, end)
	}
	if w.job.chained && resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		closeBody(resp)
		if cr := resp.ContentRange; cr != nil && cr.Total >= 0 && cr.Total != pos {
			return nil, fmt.Errorf("server reports %d bytes after %d were received: %w", cr.Total, pos, ErrValidatorChanged)
		}
		return nil, errEndOfFile
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusProxyAuthRequired {
		closeBody(resp)
		return nil, fmt.Errorf("bytes %d-%d: HTTP %d: %w", pos, end, resp.StatusCode, ErrAuthRequired)
	}
	changedOn200 := w.job.ifRange != "" || w.job.resumed
	if err := checkRangeResponse(resp, pos, w.job.size, changedOn200, w.job.validators); err != nil {
		closeBody(resp)
		return nil, fmt.Errorf("bytes %d-%d: %w", pos, end, err)
	}
	w.touch()
	w.setPhase(phaseReceiving)
	return newStream(resp.Body, pos, resp.ContentRange.End), nil
}

// pump moves bytes from the stream into seg until the segment's current end is
// reached, ownership is lost, the response ends, or an error occurs. Each slice is
// charged to the limiter, then reserved under the segment lock, then queued for the
// writer, so the worker stops exactly at an end that shrank concurrently.
func (w *worker) pump(seg *segment) (progressed bool, err error) {
	st := w.st
	for {
		if err := w.ctx.Err(); err != nil {
			return progressed, err
		}
		if _, _, owner, done := seg.snapshot(); owner != w || done {
			return progressed, nil // keep the rest of the response for a takeover
		}
		ref := w.job.bufs.get()
		buf := (*ref)[:w.sliceSize()]
		w.setPhase(phaseReceiving)
		off := st.off
		n, fromLeft, rerr := st.read(buf)
		if n > 0 {
			w.view.receiving()
			w.arrived()
			if !fromLeft && w.job.lim != nil {
				w.setPhase(phaseThrottled)
				if err := w.job.lim.WaitN(w.ctx, n); err != nil {
					st.unread(buf[:n])
					w.job.bufs.put(ref)
					return progressed, err
				}
				w.touch()
			}
			k := seg.claim(w, off, n)
			if k < n {
				st.unread(buf[k:n])
			}
			if k > 0 {
				w.setPhase(phaseWriting)
				w.job.writes <- writeReq{seg: seg, off: off, buf: buf[:k], ref: ref}
				w.received.Add(int64(k))
				w.job.received.Add(int64(k))
				progressed = true
				w.touch()
				w.setPhase(phaseReceiving)
			} else {
				w.job.bufs.put(ref)
			}
			if k < n {
				return progressed, nil
			}
		} else {
			w.job.bufs.put(ref)
		}
		switch {
		case rerr == nil:
		case errors.Is(rerr, io.EOF):
			if st.end >= 0 && st.off <= st.end {
				return progressed, fmt.Errorf("response ended at byte %d of %d: %w", st.off, st.end, io.ErrUnexpectedEOF)
			}
			if w.job.chained {
				return progressed, nil // serve asks for the bytes after this response
			}
			// For an open-ended segment (single mode, unknown size) the end of the
			// response is the end of the file; bounded segments are unaffected.
			seg.finishOpen(w, st.off)
			return progressed, nil
		default:
			return progressed, fmt.Errorf("read body: %w", rerr)
		}
	}
}

func (w *worker) sliceSize() int {
	if w.job.lim != nil && w.job.lim.Limit() > 0 {
		return sliceCapped
	}
	return sliceUncapped
}

func (w *worker) closeStream() {
	if w.st != nil {
		w.replaced = true // the next request follows a served one (§10)
		w.st.close()
		w.st = nil
	}
}
