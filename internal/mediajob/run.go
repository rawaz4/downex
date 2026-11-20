package mediajob

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"downex/internal/media"
	"downex/internal/model"
	"downex/internal/remux"
	"downex/internal/transport"
)

// Timing of the job's reports.
const (
	progressInterval = 250 * time.Millisecond // progress events at most 4 Hz
	speedTau         = 2 * time.Second        // time constant of the speed average
)

// maxInitBytes bounds the initialization data read into memory for CheckInit. Real
// initialization segments are kilobytes; CheckInit accepts a prefix.
const maxInitBytes = 16 << 20

// Run downloads the selected tracks of sel and assembles them into opts.OutPath,
// returning the output container: "mp4" (transport streams are converted by
// internal/remux), or "ts" for one transport stream joined byte for byte under
// Options.LegacyTS.
//
// It plans each track, fetches every initialization segment first and refuses
// protected ones (an error wrapping media.ErrEncrypted; nothing is assembled). With
// them it fetches the first media fragment of each transport stream (and of each HLS
// track whose container the playlist leaves open, which that fragment identifies:
// neither MPEG-TS nor fragmented MP4 is media.ErrUnsupported) and checks its start with
// Options.CheckTS: a scrambled, unconvertible or unparsable stream is refused before any
// other media fragment is requested (not under LegacyTS). Then it fetches the media
// fragments with min(pending fragments, MaxConnections) parallel
// requests, each fragment into PartDir/<track>/<index>.frag via a temporary file that
// is synced and renamed, so a fragment marked done is never truncated. Every phase
// shares the job's transport slots (at most MaxConnections; the first one fetched the
// manifest), so their connections stay warm where a phase fetches from the hosts they
// are to (warmPool); a response that delivers no byte for mediaStallTimeout is closed
// and retried (copier). onCheckpoint
// (may be nil) receives the durable fragments at most every CheckpointInterval and
// when Run stops; an error from it stops the job. onProgress (may be nil) receives
// progress at most 4 times per second and once when Run stops (when it stops early, a
// paused or failed job, also once before its last checkpoint, without the bytes of the
// requests it gave up). Both are called from the goroutine that called Run.
//
// resume is the last checkpoint of an earlier Run of the same selection (nil for a
// fresh start): fragments it records are skipped when their files are still intact,
// and resumed tracks are matched by their keys (TrackCheckpoint.Key) where they have
// them. Cancelling ctx pauses the job: Run saves the checkpoint and returns ctx.Err().
//
// After a successful assembly PartDir stays, with a record of the output, until the
// caller removes it: a Run that finds the recorded output at OutPath (or its part file,
// which it then renames) returns its container at once, without fetching anything.
func Run(ctx context.Context, opts Options, sel Selection, resume *Checkpoint, onProgress func(Progress), onCheckpoint func(Checkpoint) error) (string, error) {
	o, err := opts.withDefaults()
	if err != nil {
		return "", err
	}
	if container, clip, done, err := finishRecorded(ctx, o); err != nil || done {
		if done && onProgress != nil {
			n := checkpointTotals(resume)
			size := int64(0)
			if fi, serr := os.Stat(o.OutPath); serr == nil {
				size = fi.Size()
			}
			onProgress(Progress{FragmentsTotal: n, FragmentsDone: n, BytesDone: size, BytesTotal: size, Clip: clip})
		}
		return container, err
	}
	if !sel.Clip.IsZero() && o.LegacyTS {
		return "", fmt.Errorf("mediajob: %w: a part cannot be saved from a download stored as .ts", media.ErrUnsupported)
	}
	if !sel.Clip.IsZero() && o.AudioOnly {
		return "", errAudioPart
	}
	r := &runner{
		o:            o,
		hp:           newHeaderPolicy(sel.ManifestURL, sel.Headers),
		rt:           retrier{clock: o.Clock, max: o.MaxRetries, base: o.RetryBaseDelay, maxDelay: o.RetryMaxDelay},
		rep:          reporter{onProgress: onProgress},
		onCheckpoint: onCheckpoint,
	}
	r.gate = newHostGate(&r.o)
	defer r.closePool()
	// The manifest's slot becomes the first fetch slot: its connection stays warm when
	// the fragments are on the manifest's host.
	r.pool = []*warmSlot{newWarmSlot(o.Factory.NewSlot())}
	res, err := resolve(ctx, r.pool[0], r.rt, sel, o.LegacyTS, o.AudioOnly)
	if err != nil {
		return "", err
	}
	if o.AudioOnly {
		if err := audioOnly(res); err != nil {
			return "", err
		}
	}
	if err := o.checkMP4(res.info.Container); err != nil {
		return "", err
	}
	leadIn := 0
	if resume != nil {
		leadIn = resume.LeadIn
	}
	r.clip = newClipRun(res, sel.Clip, leadIn)
	for {
		if r.clip != nil {
			wins, err := r.clip.window(res)
			if err != nil {
				return "", err
			}
			r.wins = wins
		}
		if err := r.prepare(res, resume); err != nil {
			return "", err
		}
		now := o.Clock.Now()
		r.speed.reset(now, 0)
		r.lastCP = now
		if r.clip != nil && r.dirty {
			// A window that changed (a lead-in) is stored before anything is fetched
			// for it (docs/MEDIA-SPEC.md §8 C12).
			if err := r.checkpoint(true); err != nil {
				return "", err
			}
		}
		r.emit(true)
		container, err := r.run(ctx)
		if r.clip != nil && errors.Is(err, remux.ErrNoKeyframe) && r.clip.extendable() {
			// No keyframe at or before the part's start in the first fragment: one more
			// fragment back (C5).
			r.clip.leadIn++
			resume = r.snapshot()
			r.reset()
			continue
		}
		r.speed.rate = 0
		r.emit(true)
		return container, err
	}
}

// reset forgets the tracks of a prepared run, for preparing a new window.
func (r *runner) reset() {
	r.tracks, r.total, r.doneN, r.durable, r.dirty = nil, 0, 0, 0, false
	r.est = estimator{}
	r.slots = nil
}

// trackState is a selected track and its durable fragments.
type trackState struct {
	*track
	dir   string // PartDir/<track>
	base  int    // index of the track's first fragment in the job (Progress.Workers)
	done  []byte // bitmap of durable fragments
	ndone int
	dirty bool // fragments renamed into dir since the last checkpoint
	// win is the track's window in a clip run (nil otherwise): its fragments are a
	// window of the full plan, their files named by their full-plan index.
	win *clipWindow
}

func (s *trackState) isDone(i int) bool { return s.done[i>>3]&(1<<(i&7)) != 0 }

func (s *trackState) setDone(i int) {
	s.done[i>>3] |= 1 << (i & 7)
	s.ndone++
	s.dirty = true
}

// fragPath is the file of fragment i: named by its index in the full plan, which a
// window of the plan keeps (docs/MEDIA-SPEC.md §8 C11).
func (s *trackState) fragPath(i int) string {
	return filepath.Join(s.dir, strconv.Itoa(s.plan.Fragments[i].FullIndex)+".frag")
}

// cpTotal and cpBit map the track's fragments to its checkpoint: a window's bitmap
// counts full-plan indexes from its First (C11), a whole plan's its own indexes.
func (s *trackState) cpTotal() int {
	if s.win != nil {
		return s.win.span
	}
	return len(s.plan.Fragments)
}

func (s *trackState) cpBit(i int) int {
	if s.win != nil {
		return s.plan.Fragments[i].FullIndex - s.win.First
	}
	return i
}

func (s *trackState) cpFirst() int {
	if s.win != nil {
		return s.win.First
	}
	return 0
}

// cpDone returns the checkpoint bitmap of the durable fragments.
func (s *trackState) cpDone() []byte {
	if s.win == nil {
		return bytes.Clone(s.done)
	}
	out := make([]byte, (s.win.span+7)/8)
	for i := range s.plan.Fragments {
		if s.isDone(i) {
			b := s.cpBit(i)
			out[b>>3] |= 1 << (b & 7)
		}
	}
	return out
}

// firstMedia is the index of the track's first media fragment.
func (s *trackState) firstMedia() int {
	for i := range s.plan.Fragments {
		if !s.plan.Fragments[i].Init {
			return i
		}
	}
	return -1
}

// intact reports whether the file of fragment i is still what a completed download
// left: a regular, non-empty file of the requested range's length.
func (s *trackState) intact(i int) (int64, bool) {
	fi, err := os.Stat(s.fragPath(i))
	if err != nil || !fi.Mode().IsRegular() || fi.Size() <= 0 {
		return 0, false
	}
	if br := s.plan.Fragments[i].ByteRange; br != nil && fi.Size() != br[1]-br[0]+1 {
		return 0, false
	}
	return fi.Size(), true
}

// input describes the track to internal/remux. A transport stream is all its
// fragments in order (an EXT-X-MAP of a transport stream holds tables that precede
// the segments), with a break before each fragment that follows a discontinuity and
// before every map entry after the first; an fMP4 track is its initialization segment
// plus its media fragments (checkInits ensured that every further initialization
// segment is identical).
func (s *trackState) input() remux.Input {
	in := remux.Input{Kind: remux.Kind(string(s.kind)), Container: string(s.container), Language: s.language}
	maps := 0
	for i := range s.plan.Fragments {
		f := &s.plan.Fragments[i]
		if s.container == media.ContainerFMP4 && f.Init {
			if in.Init == "" {
				in.Init = s.fragPath(i)
			}
			continue
		}
		if s.container == media.ContainerTS && len(in.Fragments) > 0 && (f.Discontinuity || f.Init && maps > 0) {
			in.Breaks = append(in.Breaks, len(in.Fragments))
		}
		if f.Init {
			maps++
		}
		in.Fragments = append(in.Fragments, s.fragPath(i))
	}
	return in
}

// start returns the first n bytes of the track's initialization data before its first
// media fragment (a transport stream's map section, if any) followed by that fragment,
// each from its first packet (readTSPrefix).
func (s *trackState) start(n int) ([]byte, error) {
	var b []byte
	for i := 0; i <= s.firstMedia() && len(b) < n; i++ {
		data, err := readTSPrefix(s.fragPath(i), n-len(b))
		if err != nil {
			return nil, err
		}
		b = append(b, data...)
	}
	return b, nil
}

// runner is one Run. Its fields are owned by the goroutine that called Run, except
// the atomics, which the workers update, and gate.
type runner struct {
	o            Options
	hp           headerPolicy
	rt           retrier
	rep          reporter
	onCheckpoint func(Checkpoint) error
	gate         *hostGate // per-host connection caps (Options.HostRules)

	tracks  []*trackState
	total   int   // fragments of all tracks
	doneN   int   // durable fragments
	durable int64 // bytes of durable fragments
	dirty   bool  // the checkpoint changed since it was last saved
	lastCP  time.Time
	speed   speedMeter
	slots   []*connView // the fetch workers of the current phase
	est     estimator   // Progress.BytesTotal (estimate.go)
	// pool holds the transport slots of the whole job: pool[0] fetched the manifest,
	// and fetch worker k of every phase uses pool[k], so connections stay warm across
	// the phases (warmPool). Each slot is used by one worker at a time; there are at
	// most MaxConnections. Closed after the last phase and when Run returns.
	pool []*warmSlot

	// clip is the part a Run saves (nil: all of it), wins the tracks' windows, and
	// clipDone the assembled part (Progress.Clip).
	clip     *clipRun
	wins     []*clipWindow
	clipDone *model.MediaClip

	inflight atomic.Int64 // bytes of attempts in progress
	received atomic.Int64 // every payload byte received, for the speed average
	active   atomic.Int32 // requests in flight
}

// reporter passes progress events on, leaving out repeats of the last one unless
// forced. It is owned by the goroutine that called Run (or RunRanged).
type reporter struct {
	onProgress func(Progress) // nil: no events
	last       Progress       // last emitted
	emitted    bool
	scratch    []model.WorkerView // reused to compare a new event with the last one
}

// emit reports p with the views of slots at time now.
func (rp *reporter) emit(p Progress, slots []*connView, now time.Time, force bool) {
	if rp.onProgress == nil {
		return
	}
	rp.scratch = rp.scratch[:0]
	for _, cv := range slots {
		rp.scratch = append(rp.scratch, cv.view(now))
	}
	if !force && rp.emitted && sameTotals(p, rp.last) && slices.Equal(rp.scratch, rp.last.Workers) {
		return
	}
	if len(rp.scratch) > 0 {
		p.Workers = slices.Clone(rp.scratch) // the receiver may keep it
	}
	rp.last, rp.emitted = p, true
	rp.onProgress(p)
}

// prepare creates the part directories and takes over the fragments of resume that
// are still intact.
func (r *runner) prepare(res *resolved, resume *Checkpoint) error {
	var prev []TrackCheckpoint
	if resume != nil {
		prev = resume.Tracks
	}
	taken := 0 // checkpoints of prev whose fragments are taken over
	for k, t := range res.tracks {
		n := len(t.plan.Fragments)
		s := &trackState{track: t, dir: filepath.Join(r.o.PartDir, dirName(t.kind, t.id)), base: r.total, done: make([]byte, (n+7)/8)}
		if k < len(r.wins) {
			s.win = r.wins[k]
		}
		cp := resumedTrack(prev, t)
		if cp != nil && cp.TrackID != t.id && !r.adoptDir(s, cp.TrackID) {
			cp = nil // its fragments cannot be moved: the track starts over
		}
		if cp == nil {
			// Nothing in the directory is recorded durable; it may hold fragments of
			// another track that had this ID.
			_ = os.RemoveAll(s.dir)
		}
		if err := os.MkdirAll(s.dir, 0o700); err != nil {
			return fmt.Errorf("mediajob: creating the part directory: %w", err)
		}
		if cp != nil {
			taken++
			if s.reusable(cp) {
				for i := range n {
					b := s.cpBit(i) + s.cpFirst() - cp.First
					if b < 0 || b >= cp.Total || cp.Done[b>>3]&(1<<(b&7)) == 0 {
						if cp.First != s.cpFirst() {
							r.dirty = true // the window moved: the next checkpoint records it
						}
						continue
					}
					if size, ok := s.intact(i); ok {
						s.setDone(i)
						r.durable += size
						r.est.add(s, i, size)
					} else {
						r.dirty = true // the next checkpoint drops it
					}
				}
			} else {
				r.dirty = true // the plan changed: the track starts over
			}
			s.dirty = false
		}
		r.total += n
		r.doneN += s.ndone
		r.tracks = append(r.tracks, s)
	}
	if taken < len(prev) {
		r.dirty = true // the next checkpoint drops the tracks that were not taken over
	}
	return nil
}

// reusable reports whether the fragments cp records can be taken over by s: the same
// plan (its fragment count; for a window the same full-plan span), or a window that only
// begins earlier than cp's (a lead-in, docs/MEDIA-SPEC.md §8 C11/C12).
func (s *trackState) reusable(cp *TrackCheckpoint) bool {
	if cp.Total < 0 || len(cp.Done) != (cp.Total+7)/8 {
		return false
	}
	if s.win == nil {
		return cp.First == 0 && cp.Total == len(s.plan.Fragments)
	}
	return cp.First >= s.win.First && cp.First+cp.Total == s.win.First+s.win.span
}

// resumedTrack returns the checkpoint of t in prev, or nil: the one with t's key when it
// has a key, else, for checkpoints without one (DASH, or written before keys existed),
// the one with t's ID. A checkpoint of another key is never t's, whatever its ID.
func resumedTrack(prev []TrackCheckpoint, t *track) *TrackCheckpoint {
	for i := range prev {
		switch cp := &prev[i]; {
		case cp.Key != "":
			if cp.Key == t.key {
				return cp
			}
		case cp.TrackID == t.id:
			return cp
		}
	}
	return nil
}

// adoptDir moves the fragments of a resumed track from the directory of the ID it had
// when they were fetched to s.dir, and reports whether they are there now. Each kind
// of track occurs once per job, so the target holds nothing of another track.
func (r *runner) adoptDir(s *trackState, oldID string) bool {
	old := filepath.Join(r.o.PartDir, dirName(s.kind, oldID))
	if old == s.dir {
		return true
	}
	if fi, err := os.Stat(old); err != nil || !fi.IsDir() {
		return false
	}
	if err := os.RemoveAll(s.dir); err != nil {
		return false
	}
	return os.Rename(old, s.dir) == nil
}

// dirName names a track's directory: its kind plus its ID when that is a short portable
// file name, else a digest of the ID (DASH @id values are arbitrary strings).
func dirName(kind media.TrackKind, id string) string {
	ok := id != "" && len(id) <= 64
	for i := 0; ok && i < len(id); i++ {
		c := id[i]
		ok = c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
	}
	if !ok {
		sum := sha256.Sum256([]byte(id))
		id = "h" + hex.EncodeToString(sum[:8])
	}
	return string(kind) + "-" + id
}

func (r *runner) run(ctx context.Context) (string, error) {
	// Initialization segments first, with the first media fragment of each transport
	// stream: protected or unconvertible content is refused before any other media
	// fragment is requested.
	if err := r.fetchAll(ctx, r.pending(true)); err != nil {
		return "", err
	}
	if err := r.checkInits(); err != nil {
		return "", err
	}
	if err := r.checkStarts(ctx); err != nil {
		return "", err
	}
	if err := r.fetchAll(ctx, r.pending(false)); err != nil {
		return "", err
	}
	r.closePool() // nothing is fetched any more
	return r.assemble(ctx)
}

type fragRef struct {
	s *trackState
	i int
}

// pending lists the fragments still to fetch, the tracks interleaved so that they
// progress together: with early, the initialization data and the first media fragment
// of every track whose start is checked (startChecked); else the media fragments.
func (r *runner) pending(early bool) []fragRef {
	lists := make([][]fragRef, len(r.tracks))
	n := 0
	for k, s := range r.tracks {
		first := -1
		if early && r.startChecked(s) {
			first = s.firstMedia()
		}
		for i := range s.plan.Fragments {
			init := s.plan.Fragments[i].Init
			if !s.isDone(i) && (early && (init || i == first) || !early && !init) {
				lists[k] = append(lists[k], fragRef{s, i})
				n++
			}
		}
	}
	out := make([]fragRef, 0, n)
	for j := 0; len(out) < n; j++ {
		for _, l := range lists {
			if j < len(l) {
				out = append(out, l[j])
			}
		}
	}
	return out
}

type fragResult struct {
	ref  fragRef
	size int64
	err  error
}

// fetchAll fetches refs with min(len(refs), MaxConnections) workers, worker k with the
// job's transport slot pool[k], while this goroutine records completions, checkpoints
// and reports progress. It returns once every worker has stopped. The first failure
// stops the other workers. A cancelled ctx returns ctx.Err() after the checkpoint is
// saved.
func (r *runner) fetchAll(parent context.Context, refs []fragRef) error {
	if err := parent.Err(); err != nil || len(refs) == 0 {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	hosts := make(map[string]bool)
	for _, ref := range refs {
		hosts[hostOf(ref.s.plan.Fragments[ref.i].URL)] = true
	}
	n := min(len(refs), r.o.MaxConnections)
	if r.o.HostRules != nil {
		// No more workers than the hosts' connection caps let fetch at once.
		n = r.o.hostBudget(hosts, n)
	}
	r.warmPool(n, hosts)
	jobs := make(chan fragRef)
	results := make(chan fragResult, n)
	var wg sync.WaitGroup
	wg.Add(n)
	now := r.o.Clock.Now()
	r.slots = make([]*connView, n)
	for k := range n {
		cv := newConnView(k + 1)
		cv.speed.reset(now, 0)
		r.slots[k] = cv
		go r.worker(ctx, r.pool[k], jobs, results, &wg, cv)
	}
	go func() {
		defer close(jobs)
		for _, ref := range refs {
			select {
			case jobs <- ref:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	var failed error
	fail := func(err error) {
		if failed == nil {
			failed = err
			cancel()
		}
	}
	tick := r.o.Clock.NewTimer(progressInterval)
	defer func() { tick.Stop() }()
	for open := true; open; {
		select {
		case res, ok := <-results:
			switch {
			case !ok:
				open = false
			case res.err != nil:
				fail(res.err)
			default:
				if err := r.complete(res); err != nil {
					fail(err)
				} else if err := r.checkpoint(false); err != nil {
					fail(err)
				}
			}
		case <-tick.C():
			now := r.o.Clock.Now()
			r.speed.sample(now, r.received.Load())
			for _, cv := range r.slots {
				cv.speed.sample(now, cv.total.Load())
			}
			r.emit(false)
			if err := r.checkpoint(false); err != nil {
				fail(err)
			}
			tick = r.o.Clock.NewTimer(progressInterval)
		}
	}
	if failed != nil || parent.Err() != nil {
		// The bytes of the requests given up are taken back now: an event before the
		// last checkpoint lets a caller that stores the bytes done with it (the daemon)
		// store those of the durable fragments.
		r.emit(false)
	}
	if err := r.checkpoint(true); err != nil && failed == nil {
		failed = err
	}
	if err := parent.Err(); err != nil {
		return err
	}
	return failed
}

// complete records a durable fragment.
func (r *runner) complete(res fragResult) error {
	s, i := res.ref.s, res.ref.i
	s.setDone(i)
	r.doneN++
	r.inflight.Add(-res.size)
	r.durable += res.size
	r.est.add(s, i, res.size)
	r.dirty = true
	if s.container == media.ContainerUnknown && i == s.firstMedia() {
		return r.sniff(s)
	}
	return nil
}

// worker fetches the fragments it takes from jobs over slot f, which no other worker
// uses meanwhile.
func (r *runner) worker(ctx context.Context, f transport.Fetcher, jobs <-chan fragRef, results chan<- fragResult, wg *sync.WaitGroup, cv *connView) {
	defer wg.Done()
	var attemptBytes int64
	cp := newCopier(nil, func(n int) {
		attemptBytes += int64(n)
		r.inflight.Add(int64(n))
		r.received.Add(int64(n))
		cv.received.Add(int64(n))
		cv.total.Add(int64(n))
		cv.receiving()
	})
	cp.clock = r.o.Clock
	for ref := range jobs {
		if ctx.Err() != nil {
			continue // drain: the job is stopping
		}
		fr := &ref.s.plan.Fragments[ref.i]
		start, end := int64(-1), int64(-1)
		if br := fr.ByteRange; br != nil {
			start, end = br[0], br[1]
		}
		cv.assign(ref.s.base+ref.i, start, end)
		host := ""
		if r.o.HostRules != nil {
			host = hostOf(fr.URL)
		}
		release, err := r.gate.acquire(ctx, host)
		if err != nil {
			results <- fragResult{ref: ref, err: err} // ctx is done
			continue
		}
		cp.lim = r.o.hostThrottle(host)
		size, err := r.fetchFragment(ctx, f, cp, ref, host, &attemptBytes, cv)
		release()
		if err != nil && ctx.Err() == nil {
			// The job fails with this fragment: take no further work, so that the view
			// keeps the failure (the feeder stops once the failure cancels ctx).
			cv.set(model.WorkerError, shortReason(err))
			results <- fragResult{ref: ref, size: size, err: err}
			return
		}
		cv.set(model.WorkerIdle, "")
		results <- fragResult{ref: ref, size: size, err: err}
	}
	if ctx.Err() == nil {
		cv.set(model.WorkerDone, "")
	}
}

// fetchFragment downloads one fragment from host with retries into its .frag file.
func (r *runner) fetchFragment(ctx context.Context, f transport.Fetcher, cp *copier, ref fragRef, host string, attemptBytes *int64, cv *connView) (int64, error) {
	s, i := ref.s, ref.i
	fr := &s.plan.Fragments[i]
	final := s.fragPath(i)
	tmp := final + ".tmp"
	spec := fetchSpec{url: fr.URL, headers: r.hp.forURL(fr.URL), rng: fr.ByteRange, maxReq: r.o.hostRequestCap(host)}
	var size int64
	err := r.rt.doNotify(ctx, func() error {
		*attemptBytes = 0
		n, err := r.attempt(ctx, f, cp, spec, tmp, cv)
		if err != nil {
			r.inflight.Add(-*attemptBytes)
			if errors.Is(err, errStalled) {
				// The retry dials anew rather than reuse an idle connection that may be
				// as dead as the stalled one.
				f.Close()
			}
			return err
		}
		size = n
		return nil
	}, func(err error, wait time.Duration) {
		cv.retry(shortReason(err), r.o.Clock.Now().Add(wait))
	})
	if err == nil {
		if rerr := renameRetry(ctx, r.o.Clock, tmp, final); rerr != nil {
			r.inflight.Add(-size)
			err = &localError{rerr}
		}
	}
	if err != nil {
		_ = os.Remove(tmp)
		if cerr := ctx.Err(); cerr != nil {
			return 0, cerr
		}
		return 0, fmt.Errorf("mediajob: track %q fragment %d: %w", s.id, i, err)
	}
	return size, nil
}

// attempt fetches spec once into tmp and syncs it.
func (r *runner) attempt(ctx context.Context, f transport.Fetcher, cp *copier, spec fetchSpec, tmp string, cv *connView) (int64, error) {
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, &localError{err}
	}
	tctx, gen := cv.trace(ctx)
	_, n, err := fetchInto(tctx, f, spec, file, cp, func(d int) { r.active.Add(int32(d)) })
	cv.end(gen)
	if err == nil && n == 0 {
		err = fmt.Errorf("%w: the fragment is empty", ErrTruncated)
	}
	if err == nil {
		if serr := file.Sync(); serr != nil {
			err = &localError{serr}
		}
	}
	if cerr := file.Close(); cerr != nil && err == nil {
		err = &localError{cerr}
	}
	return n, err
}

// warmPool readies the slots pool[0..n) for a phase that fetches from hosts. A slot
// keeps its idle connections only when the phase uses it and every host it may hold
// one to is among hosts; every other slot drops them first. So no slot holds a
// connection that the phase cannot use, and the job never has more connections open
// than the phase's slots (≤ MaxConnections, and within the hosts' caps) have to its
// hosts.
func (r *runner) warmPool(n int, hosts map[string]bool) {
	for len(r.pool) < n {
		r.pool = append(r.pool, newWarmSlot(r.o.Factory.NewSlot()))
	}
	for k, s := range r.pool {
		if k >= n || !s.within(hosts) {
			s.Close()
		}
	}
}

// closePool releases the idle connections of the job's transport slots.
func (r *runner) closePool() {
	for _, s := range r.pool {
		s.Close()
	}
	r.pool = nil
}

// warmSlot is one of a Run's transport slots. It notes the hosts it may hold an idle
// connection to: those of the requests sent over it, and of their final URLs after
// redirects, since it last dropped its idle connections. Like the slot, it is used by
// one goroutine at a time.
type warmSlot struct {
	transport.Fetcher
	hosts map[string]bool
}

func newWarmSlot(f transport.Fetcher) *warmSlot {
	return &warmSlot{Fetcher: f, hosts: make(map[string]bool)}
}

func (s *warmSlot) Fetch(ctx context.Context, req transport.RangeRequest) (*transport.Response, error) {
	s.hosts[hostOf(req.URL)] = true
	resp, err := s.Fetcher.Fetch(ctx, req)
	if resp != nil && resp.FinalURL != "" {
		s.hosts[hostOf(resp.FinalURL)] = true
	}
	return resp, err
}

// Close drops the slot's idle connections; it may be used again afterwards.
func (s *warmSlot) Close() {
	s.Fetcher.Close()
	clear(s.hosts)
}

// within reports whether every host the slot may hold an idle connection to is in
// hosts.
func (s *warmSlot) within(hosts map[string]bool) bool {
	for h := range s.hosts {
		if !hosts[h] {
			return false
		}
	}
	return true
}

// checkInits checks every initialization segment of the fMP4 tracks with
// media.CheckInit, identifies the container of tracks the manifest left open, and
// refuses tracks whose initialization data changes midway.
func (r *runner) checkInits() error {
	for _, s := range r.tracks {
		var first []byte
		for i := range s.plan.Fragments {
			if !s.plan.Fragments[i].Init {
				continue
			}
			data, err := readPrefix(s.fragPath(i), maxInitBytes)
			if err != nil {
				return fmt.Errorf("mediajob: track %q: reading the initialization segment: %w", s.id, err)
			}
			if s.container == media.ContainerUnknown {
				if err := r.setContainer(s, sniffContainer(data)); err != nil {
					return err
				}
			}
			if s.container != media.ContainerFMP4 {
				continue // the tables of a transport stream: nothing to check
			}
			if err := media.CheckInit(data); err != nil {
				return fmt.Errorf("mediajob: track %q: initialization segment: %w", s.id, err)
			}
			if r.o.AudioOnly {
				if err := checkNoVideo(s.id, data); err != nil {
					return err
				}
			}
			switch {
			case first == nil:
				first = data
			case !bytes.Equal(first, data):
				return fmt.Errorf("mediajob: track %q: %w: the initialization segment changes within the track", s.id, media.ErrUnsupported)
			}
		}
	}
	return nil
}

// sniff identifies the container of a track the manifest left open from its first
// media fragment (sniffContainer: a transport stream may follow an image).
func (r *runner) sniff(s *trackState) error {
	i := s.firstMedia()
	if i < 0 {
		return fmt.Errorf("mediajob: track %q: %w", s.id, media.ErrNotFound)
	}
	c, err := sniffFile(s.fragPath(i))
	if err != nil {
		return fmt.Errorf("mediajob: track %q: reading a fragment: %w", s.id, err)
	}
	return r.setContainer(s, c)
}

// startChecked reports whether Run checks the start of s with CheckTS before any other
// media fragment is requested (docs/VIDEO-DETECT-SPEC.md §4.8): a transport stream, or
// an HLS track whose container the playlist leaves open (its first media fragment
// identifies it); never under LegacyTS.
func (r *runner) startChecked(s *trackState) bool {
	return !r.o.LegacyTS && (s.container == media.ContainerTS || s.hls && s.container == media.ContainerUnknown)
}

// checkStarts checks the start of every transport stream with Options.CheckTS: its map
// section (if any) followed by its first media fragment, at most remux.MaxCheckTSBytes.
// A track that its initialization data showed to be a transport stream (DASH) fetches
// that fragment now, before any other; one whose container is still open (a resumed job
// found that fragment on disk) is identified from it first. Nothing of this runs under
// LegacyTS.
func (r *runner) checkStarts(ctx context.Context) error {
	if r.o.LegacyTS {
		return nil
	}
	var late []fragRef
	for _, s := range r.tracks {
		if i := s.firstMedia(); s.container == media.ContainerTS && i >= 0 && !s.isDone(i) {
			late = append(late, fragRef{s, i})
		}
	}
	if err := r.fetchAll(ctx, late); err != nil {
		return err
	}
	for _, s := range r.tracks {
		if i := s.firstMedia(); s.container == media.ContainerUnknown && s.hls && i >= 0 && s.isDone(i) {
			if err := r.sniff(s); err != nil {
				return err
			}
		}
		if s.container != media.ContainerTS {
			continue
		}
		prefix, err := s.start(remux.MaxCheckTSBytes)
		if err != nil {
			return fmt.Errorf("mediajob: track %q: reading the start of the stream: %w", s.id, err)
		}
		if err := r.o.CheckTS(prefix, remux.Kind(string(s.kind))); err != nil {
			return fmt.Errorf("mediajob: track %q: %w", s.id, err)
		}
	}
	return nil
}

// setContainer records the sniffed container of s and checks that the tracks can still
// be assembled.
func (r *runner) setContainer(s *trackState, c media.Container) error {
	switch {
	case c != media.ContainerTS && c != media.ContainerFMP4:
		return fmt.Errorf("mediajob: track %q: %w: the data is neither MPEG-TS nor fragmented MP4", s.id, media.ErrUnsupported)
	case c == media.ContainerFMP4 && !planHasInit(s.plan):
		return fmt.Errorf("mediajob: track %q: %w: fragmented MP4 without an initialization segment", s.id, media.ErrUnsupported)
	}
	s.container = c
	cs := make([]media.Container, len(r.tracks))
	for k, t := range r.tracks {
		cs[k] = assumedContainer(t.track)
	}
	out, err := outputContainer(cs, r.o.LegacyTS)
	if err != nil {
		return err
	}
	return r.o.checkMP4(out)
}

// checkMP4 refuses an output container other than MP4 when MP4Only is set (and LegacyTS
// is not).
func (o Options) checkMP4(container string) error {
	if o.MP4Only && !o.LegacyTS && container != "mp4" {
		return fmt.Errorf("mediajob: %w: no MP4 video (the selection would be saved as %q)", media.ErrUnsupported, container)
	}
	return nil
}

// assemble joins the tracks into OutPath+".dxpart" (with Options.Assemble, or under
// LegacyTS one transport stream byte for byte with Options.ConcatTS) and renames it to
// OutPath; the part directory stays for the caller.
func (r *runner) assemble(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	cs := make([]media.Container, len(r.tracks))
	for k, s := range r.tracks {
		if s.ndone != len(s.plan.Fragments) {
			return "", fmt.Errorf("mediajob: track %q: %d of %d fragments missing", s.id, len(s.plan.Fragments)-s.ndone, len(s.plan.Fragments))
		}
		if s.container == media.ContainerUnknown {
			if err := r.sniff(s); err != nil {
				return "", err
			}
		}
		cs[k] = s.container
	}
	out, err := outputContainer(cs, r.o.LegacyTS)
	if err == nil {
		err = r.o.checkMP4(out)
	}
	if err != nil {
		return "", err
	}
	inputs := make([]remux.Input, 0, len(r.tracks))
	for _, s := range r.tracks {
		inputs = append(inputs, s.input())
	}
	part := r.o.OutPath + ".dxpart"
	if err := os.Remove(part); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("mediajob: removing a stale part file: %w", err)
	}
	var container string
	var clip *model.MediaClip
	if r.o.LegacyTS && out == remux.ContainerTS {
		// A download stored as ".ts" before 2026-10-23 keeps the join it began with.
		container, err = remux.ContainerTS, r.o.ConcatTS(ctx, inputs[0], part)
	} else {
		var w remux.Window
		if r.clip != nil {
			w = r.clip.remuxWindow()
		}
		var res remux.Result
		res, err = r.o.Assemble(ctx, inputs, part, w)
		container = res.Container
		if err == nil && r.clip != nil {
			clip = r.clip.result(res, r.tracks[0].plan.Duration)
		}
	}
	if err == nil {
		err = r.o.checkMP4(container)
	}
	if err != nil {
		_ = os.Remove(part)
		if cerr := ctx.Err(); cerr != nil {
			return "", cerr
		}
		return "", diskErr("assembling", err)
	}
	if err := publish(ctx, r.o, part, container, clip); err != nil {
		_ = os.Remove(part)
		return "", err
	}
	r.clipDone = clip
	return container, nil
}

// publish records the assembled part file and renames it to OutPath (placeOutput: never
// replacing anything but, with ReplaceExisting, a regular file). The record is written
// before the rename, so that a job stopped after it is known complete. PartDir stays:
// the caller removes it once it has stored the completion. On failure part is left in
// place for the caller to remove or keep.
func publish(ctx context.Context, o Options, part, container string, clip *model.MediaClip) error {
	if err := recordOutput(ctx, o, part, container, clip); err != nil {
		return diskErr("recording the output", err)
	}
	if err := placeOutput(ctx, o, part); err != nil {
		forgetOutput(o)
		return diskErr("renaming the output", err)
	}
	syncDir(filepath.Dir(o.OutPath))
	return nil
}

// checkpoint passes the durable fragments to onCheckpoint when they changed and, unless
// force is set, CheckpointInterval has passed since the last one. The fragment files
// were synced before they were renamed into place; the renames are made durable here.
func (r *runner) checkpoint(force bool) error {
	if !r.dirty {
		return nil
	}
	now := r.o.Clock.Now()
	if !force && now.Sub(r.lastCP) < r.o.CheckpointInterval {
		return nil
	}
	for _, s := range r.tracks {
		if s.dirty {
			syncDir(s.dir)
			s.dirty = false
		}
	}
	if r.onCheckpoint != nil {
		if err := r.onCheckpoint(*r.snapshot()); err != nil {
			return fmt.Errorf("mediajob: saving the checkpoint: %w", err)
		}
	}
	r.dirty = false
	r.lastCP = now
	return nil
}

// snapshot returns the checkpoint of the durable fragments. DurableBytes: what the
// caller stores as the bytes done, never the bytes of requests in flight (they are taken
// back when a request is given up).
func (r *runner) snapshot() *Checkpoint {
	cp := &Checkpoint{Tracks: make([]TrackCheckpoint, len(r.tracks)), DurableBytes: r.durable}
	if r.clip != nil {
		cp.LeadIn = r.clip.leadIn
	}
	for k, s := range r.tracks {
		cp.Tracks[k] = TrackCheckpoint{TrackID: s.id, Total: s.cpTotal(), Done: s.cpDone(), Key: s.key, First: s.cpFirst()}
	}
	return cp
}

func (r *runner) emit(force bool) {
	p := Progress{
		FragmentsTotal: r.total,
		FragmentsDone:  r.doneN,
		BytesDone:      r.durable + r.inflight.Load(),
		SpeedBps:       r.speed.rate,
		Connections:    int(r.active.Load()),
		Clip:           r.clipDone,
	}
	p.BytesTotal, p.BytesTotalApprox = r.est.estimate(r.tracks, r.doneN)
	r.rep.emit(p, r.slots, r.o.Clock.Now(), force)
}

// sameTotals compares everything of two events but their workers.
func sameTotals(a, b Progress) bool {
	return a.FragmentsTotal == b.FragmentsTotal && a.FragmentsDone == b.FragmentsDone &&
		a.BytesDone == b.BytesDone && a.BytesTotal == b.BytesTotal && a.BytesTotalApprox == b.BytesTotalApprox &&
		a.SpeedBps == b.SpeedBps && a.Connections == b.Connections
}

// speedMeter is an exponentially weighted moving average of the receive rate.
type speedMeter struct {
	last      time.Time
	lastBytes int64
	rate      float64 // bytes per second
	primed    bool
}

func (m *speedMeter) reset(now time.Time, total int64) {
	*m = speedMeter{last: now, lastBytes: total}
}

func (m *speedMeter) sample(now time.Time, total int64) {
	dt := now.Sub(m.last)
	if dt <= 0 {
		return
	}
	inst := float64(total-m.lastBytes) / dt.Seconds()
	if m.primed {
		m.rate += (1 - math.Exp(-dt.Seconds()/speedTau.Seconds())) * (inst - m.rate)
	} else {
		m.rate, m.primed = inst, true
	}
	m.last, m.lastBytes = now, total
}

// readPrefix reads at most n bytes from the start of a file.
func readPrefix(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(n)))
	if err != nil {
		return nil, err
	}
	return data, nil
}
