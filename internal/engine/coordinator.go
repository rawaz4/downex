package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync/atomic"
	"time"

	"downex/internal/model"
	"downex/internal/transport"
)

type evKind int

const (
	evIdle      evKind = iota + 1 // a worker finished or lost its segment and waits for an assignment
	evExit                        // a worker goroutine ended; always its last event
	evThrottled                   // a worker received 429, 503 or 421, or its connection was refused
)

// event is sent by workers to the coordinator.
type event struct {
	kind      evKind
	w         *worker
	err       error
	refused   bool  // evThrottled: refused or closed before a response, not a status
	hasStream bool  // evIdle: the worker's response continues past its segment
	streamOff int64 // evIdle: absolute offset of the response's next byte
}

// errNoWork is an internal invariant failure: nothing can be scheduled although the
// download is incomplete.
var errNoWork = errors.New("engine: no schedulable work for an incomplete download")

// coordinator is the per-download actor. Only its goroutine touches the segment list,
// the worker set and the download record while the download runs.
type coordinator struct {
	e    *Engine
	opts *Options
	log  *slog.Logger
	ctx  context.Context // Run's context; cancellation pauses the download
	sctx context.Context // store context that survives cancellation of ctx

	d         *model.Download
	job       *job
	host      string
	tailFirst bool
	lim       throttle

	file   partFile
	wr     *writer
	events chan event

	segs     []*segment // sorted by start; they tile [0, size−1]
	workers  map[*worker]struct{}
	live     int // workers that are neither retired nor cancelled
	nextID   int
	received atomic.Int64
	rows     []slotRow // connection slots of progress events; index = slot − 1
	ctl      *runCtl   // live changes (SetMaxConnections); nil outside Run

	ramp     ramp
	replaced int           // workers cancelled with a request out since the last fill (see fill)
	speed    speedMeter    // what ramp-up, splits and stealing decide on
	shown    shownMeter    // what progress events report (§9)
	rtt      time.Duration // smallest observed request latency

	stopping  bool
	succeeded bool
	failure   error
	// expiry is the first connection's link-expired error (§7.2 E3): the download fails
	// with it once the connections still receiving have ended, unless they completed it.
	expiry error

	onProgress func(Progress)
	head       []byte // Progress.Head waiting for the next event
	headSeen   bool   // this run's head was taken (from the writer or from disk)
	lastEmit   time.Time
	lastCommit time.Time
	dirty      bool // the segment layout (or the record) changed since the last commit
}

// slotRow is one connection slot of progress events. A slot is taken by a starting
// worker when its previous worker exited, retired or was cancelled.
type slotRow struct {
	w    *worker          // the slot's worker; nil once it exited
	last model.WorkerView // what the slot shows after its worker exited
	show bool             // the slot is still listed after its worker exited
}

func (e *Engine) newCoordinator(ctx, sctx context.Context, d *model.Download, file partFile, segs []*segment,
	lim throttle, onProgress func(Progress), contentType string) *coordinator {
	host := hostKey(d.Request.URL)
	// A shared per-host speed limiter (Settings.host_rules) bounds the combined rate of
	// every download to this host, in addition to the download/queue/global limiter.
	lim = combineThrottle(lim, e.hostThrottle(host))
	c := &coordinator{
		e: e, opts: &e.opts, ctx: ctx, sctx: sctx,
		log:        e.log.With("download", int64(d.ID)),
		d:          d,
		host:       host,
		tailFirst:  isTailFirst(d.Filename, contentType),
		lim:        lim,
		file:       file,
		events:     make(chan event, 64),
		segs:       segs,
		workers:    make(map[*worker]struct{}),
		onProgress: onProgress,
	}
	c.wr = newWriter(file, e.clock, e.opts.FlushInterval, e.opts.FlushBytes, e.bufs)
	c.wr.headNeed = headNeed(d.Size)
	if c.ctl = e.control(d.ID); c.ctl != nil {
		if n, ok := c.ctl.takeMaxConnections(); ok { // changed while probing
			d.MaxConnections = n
		}
	}
	c.job = &job{
		req:        requestFor(d.Request),
		ifRange:    ifRangeValidator(d.Validators),
		validators: d.Validators,
		size:       d.Size,
		single:     d.Mode == model.ModeSingle,
		caps:       e.requestCaps(),
		host:       host,
		opts:       &e.opts,
		clock:      e.clock,
		epoch:      e.clock.Now(),
		bufs:       e.bufs,
		lim:        lim,
		writes:     c.wr.in,
		events:     c.events,
		received:   &c.received,
		log:        c.log,
	}
	return c
}

// probeConn is the probe's open response, transport slot and request context, handed
// to the first worker so it keeps streaming from offset 0 (§2.2).
type probeConn struct {
	st     *stream
	f      transport.Fetcher
	ctx    context.Context
	cancel context.CancelFunc
	at     time.Time // when its headers arrived
}

// run drives the download to completion, pause (ctx cancelled) or failure. first, when
// non-nil, is the probe connection that becomes the first worker.
func (c *coordinator) run(first *probeConn) error {
	c.primeHead()
	go c.wr.run()
	ticker := c.e.clock.NewTicker(tickInterval)
	defer ticker.Stop()

	now := c.e.clock.Now()
	// Speeds count from the probe's headers: its body bytes that arrived while the part
	// file and the record were prepared are read on the first tick, and over the time
	// since run started they would look faster than they came (the worth-it guard of
	// ramp-up's first step decides on that speed).
	since := now
	if first != nil && !first.at.IsZero() && first.at.Before(now) {
		since = first.at
	}
	c.speed.reset(since, 0)
	c.shown.reset(since, 0)
	c.ramp = newRamp(c.opts.RampInterval)
	c.lastEmit, c.lastCommit = now, now
	if first != nil {
		c.nextID++
		w := &worker{id: c.nextID, job: c.job, ctx: first.ctx, cancel: first.cancel, assign: make(chan assignment, 1)}
		if len(c.segs) > 0 && c.segs[0].acquire(w) {
			c.start(w, c.segs[0], first.st, first.f)
			w.speed.reset(since, 0)
			w.shown.reset(since, 0)
		} else {
			first.st.close()
			first.f.Close()
			first.cancel()
		}
	}

	ctxDone := c.ctx.Done()
	var ctlWake <-chan struct{}
	if c.ctl != nil {
		ctlWake = c.ctl.wake
	}
	for {
		if !c.stopping {
			c.fill()
			if len(c.workers) == 0 {
				switch {
				case c.allDone():
					c.finish()
				case c.job.expired.Load():
					c.fail(c.expiryErr())
				case c.ctx.Err() == nil:
					c.fail(errNoWork)
				}
			}
		}
		if c.stopping && len(c.workers) == 0 {
			break
		}
		select {
		case ev := <-c.events:
			c.handle(ev)
		case res := <-c.wr.flushed:
			c.applyFlush(res)
			c.commit(model.StateDownloading)
		case err := <-c.wr.failed:
			c.fail(err)
		case h := <-c.wr.head:
			c.head, c.headSeen = h, true
		case <-ticker.C():
			c.tick(c.e.clock.Now())
		case <-ctxDone:
			ctxDone = nil
			c.stop()
		case <-ctlWake:
			if n, ok := c.ctl.takeMaxConnections(); ok {
				c.setMaxConnections(n)
			}
		}
	}
	return c.conclude()
}

func (c *coordinator) handle(ev event) {
	switch ev.kind {
	case evIdle:
		c.onIdle(ev)
	case evExit:
		c.onExit(ev)
	case evThrottled:
		c.onThrottled(ev)
	}
}

// onIdle assigns new work to a worker whose segment is finished or lost (§5): take
// over the adjacent segment its response already covers, else an unowned segment,
// else a new segment split off the largest one, else a forced split of the slowest
// (an idle owner's segment, else the end-game steal from a slow but receiving owner).
func (c *coordinator) onIdle(ev event) {
	w := ev.w
	if w.seg != nil {
		w.noteRange()
		w.seg.release(w)
		w.seg = nil
	}
	if w.cancelled || w.retired || c.stopping {
		c.retire(w)
		return
	}
	if c.allDone() {
		c.retire(w)
		c.finish()
		return
	}
	if c.job.expired.Load() { // no takeover, no split, no new request (§7.2 E3)
		c.retire(w)
		return
	}
	// A takeover never adds a connection: it replaces one that has received nothing.
	if ev.hasStream {
		if s := c.takeover(w, ev.streamOff); s != nil {
			w.seg = s
			w.assign <- assignment{seg: s, keepStream: true}
			return
		}
	}
	if c.live > c.allowed() {
		c.retire(w)
		return
	}
	if s := c.findWork(w, true, false); s != nil {
		w.seg = s
		w.assign <- assignment{seg: s}
		return
	}
	c.retire(w)
}

func (c *coordinator) onExit(ev event) {
	w := ev.w
	delete(c.workers, w)
	failed := ev.err != nil && !w.cancelled && !c.stopping && !isContextError(ev.err)
	// After an expiry a ranged download lets its live streams finish (§7.2 E3): the
	// expired connection and those that would send a request stop without failing it.
	expired := failed && !c.job.single && errors.Is(ev.err, ErrLinkExpired)
	if failed && errors.Is(ev.err, errLinkStopped) {
		failed = false
	}
	finished := w.retired && !w.cancelled // told to exit after finishing its range
	if !w.retired {
		w.retired = true
		c.live--
	}
	if w.seg != nil {
		w.noteRange()
		w.seg.release(w)
		w.seg = nil
	}
	if i := w.slot - 1; i >= 0 && i < len(c.rows) && c.rows[i].w == w {
		row := &c.rows[i]
		row.w = nil
		switch {
		case failed:
			row.last, row.show = w.finalView(model.WorkerError, shortReason(ev.err)), true
		case finished || c.succeeded:
			row.last, row.show = w.finalView(model.WorkerDone, ""), true
		default:
			row.show = false
		}
	}
	switch {
	case expired:
		if c.expiry == nil {
			c.expiry = fmt.Errorf("connection %d: %w", w.id, ev.err)
			c.log.Warn("link expired; no new requests, the connections still receiving finish their ranges",
				"live", c.live, "err", ev.err)
		}
	case failed:
		c.fail(fmt.Errorf("connection %d: %w", w.id, ev.err))
	}
}

// expiryErr is the error a download whose link expired fails with.
func (c *coordinator) expiryErr() error {
	if c.expiry != nil {
		return c.expiry
	}
	return fmt.Errorf("a connection found the link expired: %w", ErrLinkExpired)
}

// onThrottled lowers the per-host connection cap after 429/503/421, or after a refused
// connection while another connection of the download receives (a server at its
// connection limit), to the live connections less this one (§10). A wave of refusals,
// such as a ramp-up step beyond the server's limit, thus ends at the connections the
// server serves. The refused worker gives up its segment when more connections are live
// than allowed, and later ramp-up steps double from the lowered target.
func (c *coordinator) onThrottled(ev event) {
	w := ev.w
	if c.stopping {
		return
	}
	if ev.refused && !c.othersReceiving(w) {
		return // nothing else gets through either: a network failure, retried
	}
	// The connections the server serves besides w; w may already be stopped (a
	// takeover ended it before this event came), and its refusal still counts.
	others := c.live
	if !w.retired {
		others--
	}
	limit := max(1, others)
	if limit < c.perHostMax() {
		c.e.penalizeHost(c.host, limit)
		c.log.Warn("server asked to slow down; lowering per-host connections", "host", c.host, "limit", limit, "err", ev.err)
	}
	c.ramp.lower(c.perHostMax())
	if !w.retired && c.live > c.perHostMax() {
		if w.seg != nil {
			w.seg.release(w)
			w.seg = nil
		}
		c.cancelWorker(w)
	}
}

// othersReceiving reports whether a live worker other than w has received bytes.
func (c *coordinator) othersReceiving(w *worker) bool {
	for o := range c.workers {
		if o != w && !o.retired && o.received.Load() > 0 {
			return true
		}
	}
	return false
}

func (c *coordinator) tick(now time.Time) {
	received := c.received.Load()
	c.speed.sample(now, received)
	c.shown.sample(now, received, shownSilenceFor(c.capBps(), 1))
	perWorker := shownSilenceFor(c.capBps(), c.live)
	for w := range c.workers {
		if r := time.Duration(w.rtt.Load()); r > 0 && (c.rtt == 0 || r < c.rtt) {
			c.rtt = r
		}
		got := w.received.Load()
		if got > 0 && w.firstByte.IsZero() {
			w.firstByte = now
		}
		w.speed.sample(now, got)
		w.shown.sample(now, got, perWorker)
	}
	if !c.stopping {
		c.checkStalls(now)
		rem := c.remaining()
		// A step needs the speed measured: the meter starts with the first tick that saw
		// bytes, which a worker can receive between that sample and allReceiving.
		on := c.rampOn() && c.speed.primed && rampWorthwhile(rem, c.speed.rate, c.rtt)
		if c.ramp.step(now, connBudget(rem, c.perHostMax()), c.allReceiving(), on, c.capBps() > 0) {
			c.log.Debug("ramp-up", "target", c.ramp.target)
		}
		if c.dirty && now.Sub(c.lastCommit) >= c.opts.CheckpointInterval {
			c.commit(model.StateDownloading)
		}
	}
	if now.Sub(c.lastEmit) >= progressInterval {
		c.emit(now)
	}
}

// checkStalls cancels workers idle past the stall timeout and reassigns the unreserved
// rest of their segments (§5). A worker whose response already delivered body bytes is
// stalled after midStallTimeout; this needs no free worker, so a connection that goes
// silent while every other one is busy is replaced too.
func (c *coordinator) checkStalls(now time.Time) {
	for w := range c.workers {
		if w.retired || w.cancelled {
			continue
		}
		p := w.getPhase()
		if p != phaseConnecting && p != phaseReceiving {
			continue
		}
		timeout := c.opts.StallTimeout
		// The gap before the flag: open clears the flag first, so a cleared gap is
		// never paired with the previous response's flag.
		gap := time.Duration(w.gap.Load())
		if p == phaseReceiving && !c.job.single && w.midStream.Load() {
			timeout = midStallTimeout(c.opts, c.rtt, gap)
		}
		idle := w.idle(now)
		if idle < timeout {
			continue
		}
		if c.job.single {
			c.fail(fmt.Errorf("no data for %s: %w", idle.Round(time.Millisecond), ErrStalled))
			return
		}
		if s := w.seg; s != nil {
			if newStart, oldEnd, split := s.detach(w); split {
				c.insertAfter(s, newSegment(newStart, oldEnd, 0, nil))
			}
			w.seg = nil
		}
		c.cancelWorker(w)
		c.replaced++
		c.log.Warn("connection stalled; reassigning its range", "worker", w.id, "idle", idle, "timeout", timeout)
	}
}

// midStallTimeout is the stall timeout of a connection whose current response already
// delivered body bytes (§5): MidStallTimeout, at least 4·RTT once a request latency is
// known and twice gap, the longest wait for that response's bytes (a slow but steady
// TLS connection delivers whole 16 KiB records, far apart below a few KB/s), never more
// than StallTimeout.
func midStallTimeout(o *Options, rtt, gap time.Duration) time.Duration {
	return min(o.StallTimeout, max(o.MidStallTimeout, 4*rtt, 2*gap))
}

// fill starts workers until the ramp-up target and the connection budget are reached.
// The segments of all new workers are planned before any of them starts, so that each
// requests exactly the range it keeps (§3): a segment split again after its request
// was sent would end its response early. The first new workers after a takeover or a
// stall replace the connections those cancelled (worker.replaced, §10).
func (c *coordinator) fill() {
	replaced := c.replaced
	c.replaced = 0
	if c.stopping || c.ctx.Err() != nil || c.job.expired.Load() {
		return
	}
	n := c.allowed() - c.live
	if n <= 0 {
		return
	}
	type launch struct {
		w *worker
		s *segment
	}
	plan := make([]launch, 0, n)
	for range n {
		w := c.newWorker()
		s := c.findWork(w, false, n > 1)
		if s == nil {
			w.cancel()
			break
		}
		plan = append(plan, launch{w, s})
	}
	for i, l := range plan {
		l.w.replaced = i < replaced
		c.start(l.w, l.s, nil, nil)
	}
}

// findWork returns a segment now owned by w: an unowned one first, else a split.
// Only free workers (those that finished a segment) may force-split a slow segment.
// several says w is one of several workers started at once (a ramp-up step).
func (c *coordinator) findWork(w *worker, free, several bool) *segment {
	for _, s := range c.segs {
		if s.acquire(w) {
			return s
		}
	}
	if c.job.single {
		return nil
	}
	if s := c.split(w, false, several); s != nil {
		return s
	}
	if free {
		return c.split(w, true, false)
	}
	return nil
}

// split applies planSplit, shrinking the victim with a compare-and-swap of its end.
// When the victim's position already passed the split point the plan is recomputed.
func (c *coordinator) split(w *worker, force, several bool) *segment {
	for range 3 {
		views, idx := c.splitViews(w, force)
		vi, newStart, ok := planSplit(views, c.speed.rate, len(c.segs) == 1, c.tailFirst, several, force)
		if !ok {
			return nil
		}
		victim := c.segs[idx[vi]]
		oldEnd, ok := victim.shrink(newStart)
		if !ok {
			continue
		}
		s := newSegment(newStart, oldEnd, 0, w)
		c.insertAfter(victim, s)
		steal := views[vi].Idle == 0 && views[vi].ETA > 0
		c.log.Debug("split", "start", newStart, "end", oldEnd, "forced", force, "steal", steal, "segments", len(c.segs))
		return s
	}
	return nil
}

// splitViews snapshots the splittable segments for w. For forced splits it marks the
// segments whose worker is slow (§5): receiving and idle ≥ max(2 s, 4·RTT) (Idle), or
// receiving but slow enough for the end game to wait on it (ETA, see slowOwnerETA),
// the latter only when w is not slow itself (canSteal).
func (c *coordinator) splitViews(w *worker, force bool) ([]segView, []int) {
	now := c.e.clock.Now()
	threshold := max(slowIdleMin, 4*c.rtt)
	var below float64
	if force {
		below = stealBelow(c.capBps(), c.measuredRate(), c.live)
		if !canSteal(w.speed.primed, w.speed.rate, below) {
			below = 0
		}
	}
	views := make([]segView, 0, len(c.segs))
	idx := make([]int, 0, len(c.segs))
	for i, s := range c.segs {
		end, pos, owner, done := s.snapshot()
		if done || end < 0 || pos > end {
			continue
		}
		v := segView{Start: s.start, End: end, Pos: pos}
		if force && owner != nil && owner.getPhase() == phaseReceiving {
			if idle := owner.idle(now); idle >= threshold {
				v.Idle = idle
			} else if below > 0 {
				var age time.Duration
				if !owner.firstByte.IsZero() {
					age = now.Sub(owner.firstByte)
				}
				v.ETA = slowOwnerETA(end-pos+1, age, owner.speed.primed, owner.speed.rate, below)
			}
		}
		views = append(views, v)
		idx = append(idx, i)
	}
	return views, idx
}

// stealBelow is the speed under which a receiving owner counts as slow for end-game
// stealing (§5): a quarter of the average live connection's speed. It is 0, which
// turns stealing off, while a speed cap is active (slow connections are then the
// limiter's doing) or while the download's speed is unknown.
func stealBelow(capBps int64, rateBps float64, live int) float64 {
	if capBps > 0 || rateBps <= 0 || live <= 0 {
		return 0
	}
	return rateBps / float64(live) / slowSpeedRatio
}

// canSteal reports whether a free worker may steal in the end game (§5): its own speed
// is measured (primed) and at least belowBps, the speed under which an owner is slow.
// It is then always faster than the owner it steals from. A slower stealer would hold
// the stolen half longer than its owner, and once the faster workers' steals shrank
// that half below 46,720 bytes nothing could take it back.
func canSteal(primed bool, stealerBps, belowBps float64) bool {
	return belowBps > 0 && primed && stealerBps >= belowBps
}

// maxETA bounds slowOwnerETA so that the conversion to a Duration cannot overflow.
const maxETA = 1e6 // seconds

// slowOwnerETA is how long an owner needs for its rem remaining bytes at its own speed
// ownerBps when it is slow but alive (§5): it received its first byte at least
// slowMinAge ago (age), its speed is measured (primed, > 0) and below belowBps, and
// that time exceeds slowMinETA. Otherwise it is 0.
func slowOwnerETA(rem int64, age time.Duration, primed bool, ownerBps, belowBps float64) time.Duration {
	if age < slowMinAge || !primed || ownerBps <= 0 || ownerBps >= belowBps {
		return 0
	}
	secs := float64(rem) / ownerBps
	if secs <= slowMinETA.Seconds() {
		return 0
	}
	return time.Duration(min(secs, maxETA) * float64(time.Second))
}

// takeover gives w the segment starting at the offset its response continues from,
// provided nothing of it was received yet; that segment's owner is cancelled (§5).
func (c *coordinator) takeover(w *worker, off int64) *segment {
	i, found := slices.BinarySearchFunc(c.segs, off, func(s *segment, off int64) int {
		switch {
		case s.start < off:
			return -1
		case s.start > off:
			return 1
		}
		return 0
	})
	if !found {
		return nil
	}
	s := c.segs[i]
	prev, ok := s.takeover(w, off)
	if !ok {
		return nil
	}
	if prev != nil && prev != w {
		if prev.seg == s {
			prev.seg = nil
		}
		if prev.getPhase() != phaseBackoff { // its request is out: the server may count it
			c.replaced++
		}
		c.cancelWorker(prev)
		c.log.Debug("takeover", "worker", w.id, "from", prev.id, "start", off)
	}
	return s
}

func (c *coordinator) insertAfter(victim, s *segment) {
	i := slices.Index(c.segs, victim)
	c.segs = slices.Insert(c.segs, i+1, s)
	c.dirty = true
}

func (c *coordinator) newWorker() *worker {
	c.nextID++
	ctx, cancel := context.WithCancel(c.ctx)
	return &worker{id: c.nextID, job: c.job, ctx: ctx, cancel: cancel, assign: make(chan assignment, 1)}
}

// start launches w on s. st and f are reused from the probe for the first worker.
func (c *coordinator) start(w *worker, s *segment, st *stream, f transport.Fetcher) {
	if f == nil {
		f = c.e.tf.NewSlot()
	}
	w.fetch, w.st, w.seg = f, st, s
	w.touch()
	if st != nil {
		w.setPhase(phaseReceiving)
		w.view.set(model.WorkerRequesting, "") // the probe's response: its body comes next
	} else {
		w.setPhase(phaseConnecting)
		w.view.set(model.WorkerConnecting, "")
	}
	now := c.e.clock.Now()
	w.speed.reset(now, 0)
	w.shown.reset(now, 0)
	c.takeSlot(w)
	c.workers[w] = struct{}{}
	c.live++
	go w.run(s)
}

// takeSlot gives w the lowest slot whose worker exited, finished or was stopped.
func (c *coordinator) takeSlot(w *worker) {
	i := slices.IndexFunc(c.rows, func(r slotRow) bool { return r.w == nil || r.w.retired })
	if i < 0 {
		i = len(c.rows)
		c.rows = append(c.rows, slotRow{})
	}
	c.rows[i] = slotRow{w: w}
	w.slot = i + 1
}

// retire answers an idle worker with "exit".
func (c *coordinator) retire(w *worker) {
	if !w.retired {
		w.retired = true
		c.live--
	}
	w.assign <- assignment{}
}

func (c *coordinator) cancelWorker(w *worker) {
	if !w.retired {
		w.retired = true
		c.live--
	}
	w.cancelled = true
	w.cancel()
}

func (c *coordinator) stop() {
	c.stopping = true
	for w := range c.workers {
		c.cancelWorker(w)
	}
}

func (c *coordinator) fail(err error) {
	if c.failure == nil {
		c.failure = err
	}
	c.stop()
}

func (c *coordinator) finish() {
	c.succeeded = true
	c.stop()
}

func (c *coordinator) allDone() bool {
	for _, s := range c.segs {
		if !s.done() {
			return false
		}
	}
	return true
}

// allowed is the number of connections that may be live now.
func (c *coordinator) allowed() int {
	if c.job.single {
		return 1
	}
	return min(c.ramp.target, c.budget())
}

func (c *coordinator) budget() int {
	return connBudget(c.remaining(), c.perHostMax())
}

// remaining is the number of bytes no worker has reserved yet.
func (c *coordinator) remaining() int64 {
	var rem int64
	for _, s := range c.segs {
		rem += s.remaining()
	}
	return rem
}

// measuredRate is the download's speed, or 0 while it has not been sampled yet.
func (c *coordinator) measuredRate() float64 {
	if !c.speed.primed {
		return 0
	}
	return c.speed.rate
}

func (c *coordinator) perHostMax() int {
	m := c.d.MaxConnections
	if m <= 0 {
		m = c.opts.DefaultMaxConnections
	}
	m = min(m, c.opts.HardCap)
	if hc := c.e.hostLimit(c.host); hc > 0 {
		m = min(m, hc)
	}
	if hr := c.e.hostRuleMax(c.host); hr > 0 {
		m = min(m, hr)
	}
	return max(m, 1)
}

func (c *coordinator) allReceiving() bool {
	for w := range c.workers {
		if !w.retired && w.received.Load() == 0 {
			return false
		}
	}
	return true
}

func (c *coordinator) rampOn() bool {
	return rampEnabled(c.job.single, c.capBps(), c.speed.peak)
}

// capBps is the active speed cap in bytes per second, 0 when unlimited.
func (c *coordinator) capBps() int64 {
	if c.lim == nil {
		return 0
	}
	return c.lim.Limit()
}

// applyFlush records the durable prefix of every segment the writer flushed.
func (c *coordinator) applyFlush(res flushResult) {
	for s, next := range res.frontiers {
		if d := next - s.start; d > s.durable {
			s.durable = d
		}
	}
}

// commit stores the record and every segment's durable offset in one transaction.
func (c *coordinator) commit(state model.State) error {
	now := c.e.clock.Now()
	c.d.State = state
	c.d.UpdatedAt = now
	c.lastCommit = now
	if err := c.e.st.SaveCheckpoint(c.sctx, c.d, c.persistable()); err != nil {
		c.log.Warn("checkpoint failed", "err", err)
		return fmt.Errorf("save checkpoint: %w", err)
	}
	c.dirty = false
	return nil
}

// persistable converts the live layout to stored segments. Durable offsets come only
// from flush results, so the store never claims unflushed bytes.
func (c *coordinator) persistable() []model.Segment {
	out := make([]model.Segment, 0, len(c.segs))
	for _, s := range c.segs {
		end, pos, _, done := s.snapshot()
		if end < 0 && done { // open-ended segment whose response ended at pos
			end = pos - 1
		}
		out = append(out, model.Segment{Start: s.start, End: end, Durable: s.durable})
	}
	return compactSegments(out)
}

func (c *coordinator) emit(now time.Time) {
	c.lastEmit = now
	if c.onProgress != nil {
		p := c.progress()
		p.Head, c.head = c.head, nil
		c.onProgress(p)
	}
}

// primeHead prepares Progress.Head before the writer starts: the bytes resumed from
// disk count towards it, and when they already cover it the head is read from the part
// file for the first event (docs/ENGINE-SPEC.md §9).
func (c *coordinator) primeHead() {
	need := c.wr.headNeed
	if need <= 0 {
		return
	}
	for _, s := range c.segs {
		if s.start < need && s.durable > 0 {
			c.wr.headFront.add(s.start, min(s.durable, need-s.start))
		}
	}
	if c.wr.headFront.next >= need {
		c.wr.headSent = true
		c.readHead(need)
	}
}

// readHead reads the part file's first n bytes as the run's Progress.Head.
func (c *coordinator) readHead(n int64) {
	c.headSeen = true
	if n <= 0 {
		return
	}
	buf := make([]byte, n)
	if _, err := c.file.ReadAt(buf, 0); err != nil {
		c.log.Debug("head not read", "err", err)
		return
	}
	c.head = buf
}

func (c *coordinator) progress() Progress {
	p := Progress{
		ID: c.d.ID, Mode: c.d.Mode, Size: c.d.Size,
		SpeedBps: c.shownRate(c.shown.rate), Connections: c.live,
		Segments: make([]SegmentView, 0, len(c.segs)),
	}
	for _, s := range c.segs {
		end, pos, _, _ := s.snapshot()
		p.Received += pos - s.start
		p.Segments = append(p.Segments, SegmentView{Start: s.start, End: end, Received: pos - s.start})
	}
	if len(c.rows) > 0 {
		now := c.e.clock.Now()
		p.Workers = make([]model.WorkerView, 0, len(c.rows))
		for i := range c.rows {
			if v, ok := c.slotView(&c.rows[i], now); ok {
				p.Workers = append(p.Workers, v)
			}
		}
	}
	return p
}

// shownRate is rate as progress events report it: 0 once the download stops (paused,
// failed or complete), at once rather than decaying (§9).
func (c *coordinator) shownRate(rate float64) float64 {
	if c.stopping {
		return 0
	}
	return rate
}

// slotView is what a slot shows at time now (see Progress.Workers); ok is false when
// the slot is not listed.
func (c *coordinator) slotView(r *slotRow, now time.Time) (model.WorkerView, bool) {
	w := r.w
	switch {
	case w == nil:
		return r.last, r.show
	case w.retired && (!w.cancelled || c.succeeded):
		return w.finalView(model.WorkerDone, ""), true
	case w.retired: // stopped: no longer a connection
		return model.WorkerView{}, false
	}
	v := model.WorkerView{Slot: w.slot, Start: -1, End: -1, SpeedBps: c.shownRate(w.shown.rate), Fragment: -1}
	if s := w.seg; s != nil {
		end, pos, _, _ := s.snapshot()
		v.Start, v.End, v.Received = s.start, end, pos-s.start
	}
	v.State, v.Detail, v.RetryAt = w.view.snapshot()
	if v.State == model.WorkerRetrying {
		v.Detail = retryDetail(v.Detail, v.RetryAt, now)
	}
	return v, true
}

// conclude waits for the writer's final flush, then completes, pauses or fails the
// download, committing whatever is durable.
func (c *coordinator) conclude() error {
	close(c.wr.in)
	var final flushResult
	for waiting := true; waiting; {
		select {
		case res := <-c.wr.flushed:
			c.applyFlush(res)
		case final = <-c.wr.done:
			waiting = false
		}
	}
	select { // a head the writer sent after the loop's last receive
	case h := <-c.wr.head:
		c.head, c.headSeen = h, true
	default:
	}
	if final.err != nil {
		c.succeeded = false
		if c.failure == nil {
			c.failure = final.err
		}
	} else {
		c.applyFlush(final)
	}

	switch {
	case c.failure != nil:
		return c.abandon(model.StateFailed, c.failure)
	case c.succeeded:
		return c.complete()
	default:
		err := c.ctx.Err()
		if err == nil {
			err = context.Canceled
		}
		return c.abandon(model.StatePaused, err)
	}
}

// abandon closes the part file and commits partial progress in the given state. It
// returns cause itself unless closing or committing failed too.
func (c *coordinator) abandon(state model.State, cause error) error {
	closeErr := c.file.Close()
	if closeErr != nil {
		closeErr = diskError("close part file", closeErr)
	}
	if state == model.StateFailed {
		c.d.Error = cause.Error()
	}
	commitErr := c.commit(state)
	c.emit(c.e.clock.Now())
	if closeErr == nil && commitErr == nil {
		return cause
	}
	return errors.Join(cause, closeErr, commitErr)
}

// complete verifies that every byte is durable, commits that, renames the part file
// to its final name without overwriting anything, and marks the download completed.
func (c *coordinator) complete() error {
	for _, s := range c.segs {
		end, pos, _, _ := s.snapshot()
		if end < 0 {
			end = pos - 1
		}
		if s.durable != end-s.start+1 {
			return c.abandon(model.StateFailed, fmt.Errorf("engine: bytes %d-%d not durable at completion", s.start+s.durable, end))
		}
	}
	if c.d.Size < 0 { // single mode with unknown size: the response's end defined it
		var size int64
		for _, s := range c.segs {
			_, pos, _, _ := s.snapshot()
			size = max(size, pos)
		}
		c.d.Size = size
	}
	if !c.headSeen && !c.wr.headSent { // shorter than the head: the whole file is it
		c.readHead(headNeed(c.d.Size))
	}
	if err := c.commit(model.StateDownloading); err != nil {
		return c.abandon(model.StateFailed, err)
	}
	if err := c.file.Close(); err != nil {
		c.d.State = model.StateFailed
		c.d.Error = err.Error()
		return errors.Join(diskError("close part file", err), c.commit(model.StateFailed))
	}
	// A variant name is committed before the rename to it, so that recovery after a
	// crash between the rename and the final commit looks for the file that was really
	// written, never for another file under the reserved name.
	// A download that replaces its file (ReplaceFile) renames over it only under its own
	// name; a variant replaces nothing.
	reserved, replace := c.d.Filename, c.d.ReplaceFile
	name, err := c.e.promote(c.sctx, c.d.Dir, reserved, c.d.ID, replace, func(variant string) error {
		c.d.Filename, c.d.ReplaceFile = variant, false
		return c.commit(model.StateDownloading)
	})
	if err != nil {
		c.d.Filename, c.d.ReplaceFile = reserved, replace
		c.d.Error = err.Error()
		return errors.Join(err, c.commit(model.StateFailed))
	}
	c.d.Filename, c.d.ReplaceFile = name, false
	c.d.Error = ""
	c.d.CompletedAt = c.e.clock.Now()
	if err := c.commit(model.StateCompleted); err != nil {
		return err
	}
	c.emit(c.e.clock.Now())
	return nil
}
