package mediajob

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"downex/internal/media"
	"downex/internal/model"
	"downex/internal/platform"
	"downex/internal/remux"
	"downex/internal/transport"
)

// Output modes of RunRanged.
const (
	// OutputRemux joins the tracks (a video and an audio track, or one muxed track,
	// each a fragmented MP4 file) into a progressive MP4 with internal/remux.
	OutputRemux = "remux"
	// OutputCopy publishes one MP4 track as it is: its file is renamed into place. The
	// track is muxed, or the video of a page without sound (docs/SITE-MEDIA-SPEC.md
	// §3.11, amendment 2026-11-18).
	OutputCopy = "copy"
)

// RangedSelection names the formats of a resolved web page that RunRanged downloads
// (docs/SITE-MEDIA-SPEC.md §3.4).
type RangedSelection struct {
	// Tracks are the chosen formats: the video or muxed track first, then the audio
	// track; 1 or 2. Their URLs and headers are secrets of the run: errors never repeat
	// them and nothing stores them.
	Tracks []media.RangedTrack
	// Output is OutputRemux or OutputCopy (one muxed or video-only MP4 track).
	Output string
	// Clip is the part of the video to save (docs/SITE-MEDIA-SPEC.md §2.7,
	// docs/MEDIA-SPEC.md §8); the zero Clip saves all of it. Its times are on the
	// resources' media timeline.
	Clip media.Clip
	// NotBefore is the earliest time of the first request (yt-dlp's available_at); zero
	// for none. RunRanged waits for it before it reports any progress, and refuses one
	// more than 10 minutes ahead.
	NotBefore time.Time
	// Refresh resolves the page again and returns the tracks with fresh URL, Headers,
	// Expires and Network, matched by Key (an error wrapping ErrInvalidSelection when a
	// Key is gone). RunRanged calls it when a request is answered 403 or 410, and ahead
	// of a known expiry; nil makes 403 and 410 final.
	Refresh func(ctx context.Context) ([]media.RangedTrack, error)
}

// Timing and bounds of RunRanged (docs/SITE-MEDIA-SPEC.md §2.4–§2.6, §9).
const (
	maxNotBefore    = 10 * time.Minute // a later NotBefore is refused
	syncInterval    = time.Second      // written chunks are synced at least this often
	syncBytes       = 64 << 20         // ... or once this many bytes wait for a sync
	refreshGap      = 60 * time.Second // at most one address renewal per minute
	maxRefreshes    = 5                // and five per run
	refreshAhead    = 2 * time.Minute  // renew when a known expiry is this close
	freeSpaceMargin = 64 << 20         // free space kept beyond what a job needs
	maxMoovBytes    = 64 << 20         // the 'moov' box a copied file may have
	maxTopBoxes     = 1 << 16          // top-level boxes walked to find it
	dataName        = "data"           // a track's file in its directory
)

// partialSyncInterval: the prefixes of chunks still arriving are synced at least this
// often (the engine's flush interval, docs/ENGINE-SPEC.md), and with every sync of their
// track's file (docs/SITE-MEDIA-SPEC.md §2.5).
const partialSyncInterval = 8 * time.Second

// detailRenewing is a connection's Detail while the page is resolved again.
const detailRenewing = "renewing the address"

// Windows error numbers of a full volume, which have no portable syscall constant.
const (
	winErrorHandleDiskFull = syscall.Errno(39)
	winErrorDiskFull       = syscall.Errno(112)
)

var (
	// errWholeBody reports a server that answered a later chunk's range with the whole
	// resource, or the first chunk's after other chunks started; unlike other range
	// mismatches it is retried (§2.4).
	errWholeBody = fmt.Errorf("%w: the server sent the whole resource for one of its parts", ErrRangeMismatch)
	// errRefreshLimit reports an address that expired once more after the last renewal
	// a run may make.
	errRefreshLimit = errors.New("mediajob: the address expired again after the last renewal")
)

// RunRanged downloads the formats of sel into opts.OutPath and returns the container
// written: "mp4", or "mp3" for a copied MP3 (an audio page's, docs/SITE-MEDIA-SPEC.md
// §3.13).
//
// Each track is planned with media.PlanRanged (a track without a known size is asked
// for "bytes=0-0" first; a server that ignores ranges makes it one whole request) and
// fetched into PartDir/<kind>-<id>/data, preallocated and never truncated on resume,
// with min(pending chunks, MaxConnections, the hosts' connection caps) parallel
// requests of at most the host's request cap. The first chunk of every track of
// several chunks is fetched and checked first: its complete top-level boxes must hold a
// 'moov' that media.CheckInit accepts (protection: media.ErrEncrypted) and, for
// OutputRemux, an 'ftyp' and an 'mvex' (else media.ErrUnsupported) before any other
// chunk is requested. The other chunks may start as soon as the received prefix of
// every first chunk passes that check; a refusal is decided only on the complete chunk,
// whose check runs in any case.
// A chunk counts as durable only after a sync that began after its last byte was
// written, and so does the prefix a chunk still arriving wrote (docs/SITE-MEDIA-SPEC.md
// §2.5): every retry continues after it, and so does a resume. onCheckpoint (may be nil)
// receives the durable chunks and prefixes per track (with their Key and ChunkBytes),
// their bytes (DurableBytes) and the tracks' size (BytesTotal) at most every
// CheckpointInterval and when RunRanged stops.
// onProgress (may be nil) receives progress, with BytesTotal when every size is known,
// at most 4 times per second and once when RunRanged stops, the first time once the
// NotBefore wait and the size probes are over. Both are called from the goroutine that
// called RunRanged.
//
// resume is the checkpoint of an earlier run of the same selection (nil for a fresh
// start). A track continues from it only when its Key (or, without one, its ID), its
// chunk size and part count match and its file still has the planned size; otherwise
// that track starts over and the other keeps its chunks. Cancelling ctx pauses the job:
// RunRanged syncs and saves the checkpoint, with the prefixes of the chunks it stopped,
// and returns ctx.Err(). A 403 or 410 renews the addresses through sel.Refresh and
// continues the chunk; the same chunk refused
// again after a renewal, or a failed renewal, fails the run with its progress kept.
// After a 429 or 503 the host gets half its connections for the rest of the run.
//
// The output is published as Run publishes it: a record in PartDir, then a rename to
// OutPath that never replaces a file; a later call that finds the record completes at
// once. OutputCopy renames the track's file instead of assembling, so no copy is made.
func RunRanged(ctx context.Context, opts Options, sel RangedSelection, resume *Checkpoint,
	onProgress func(Progress), onCheckpoint func(Checkpoint) error) (string, error) {
	o, err := opts.withDefaults()
	if err != nil {
		return "", err
	}
	if err := checkRangedSelection(sel, o.AudioOnly); err != nil {
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
	r := newRangedRunner(o, sel, onProgress, onCheckpoint)
	defer r.close()
	r.resume = resume
	if err := r.waitNotBefore(ctx); err != nil {
		return "", err
	}
	if err := r.planTracks(ctx); err != nil {
		return "", err
	}
	if err := r.prepare(ctx, resume); err != nil {
		return "", err
	}
	now := o.Clock.Now()
	r.speed.reset(now, 0)
	r.lastCP, r.lastSync, r.lastPartial = now, now, now
	r.emit(true)
	container, err := r.run(ctx)
	r.speed.rate = 0
	r.emit(true)
	return container, err
}

// checkRangedSelection refuses selections RunRanged cannot turn into one MP4 file.
// One audio track is an audio page's download (docs/SITE-MEDIA-SPEC.md §3.13): an MP4
// (m4a or mp4) joined or copied, or an MP3 copied; never a part.
func checkRangedSelection(sel RangedSelection, audioOnly bool) error {
	ts := sel.Tracks
	audio := len(ts) == 1 && ts[0].Kind == media.KindAudio
	switch {
	case len(ts) == 0 || len(ts) > 2:
		return fmt.Errorf("%w: %d tracks; a video needs a video (or muxed) track and at most one audio track", ErrInvalidSelection, len(ts))
	case len(ts) == 2 && (ts[0].Kind != media.KindVideo || ts[1].Kind != media.KindAudio):
		return fmt.Errorf("%w: two tracks must be a video track, then an audio track", ErrInvalidSelection)
	case len(ts) == 1 && ts[0].Kind != media.KindVideo && ts[0].Kind != media.KindMuxed && !audio:
		return fmt.Errorf("%w: a single track must carry video or be the audio of a page without video", ErrInvalidSelection)
	case (audio || audioOnly) && !sel.Clip.IsZero():
		return errAudioPart
	}
	if audio && sel.Output == OutputCopy {
		if e := ts[0].Ext; e != "mp4" && e != "m4a" && e != containerMP3 {
			return fmt.Errorf("mediajob: track %q: %w: only an MP4 or MP3 audio file is saved as it is (file type %q)", ts[0].ID, media.ErrUnsupported, e)
		}
		return nil
	}
	switch sel.Output {
	case OutputRemux:
		for _, t := range ts {
			if t.Ext != "mp4" && t.Ext != "m4a" {
				return fmt.Errorf("mediajob: track %q: %w: only MP4 formats can be joined (file type %q)", t.ID, media.ErrUnsupported, t.Ext)
			}
		}
	case OutputCopy:
		if len(ts) != 1 {
			return fmt.Errorf("%w: a copied download is one track", ErrInvalidSelection)
		}
		if ts[0].Ext != "mp4" {
			return fmt.Errorf("mediajob: track %q: %w: only an MP4 file is saved as it is (file type %q)", ts[0].ID, media.ErrUnsupported, ts[0].Ext)
		}
	default:
		return fmt.Errorf("%w: unknown output %q", ErrInvalidSelection, sel.Output)
	}
	return nil
}

// source is where a track's bytes come from now: its URL and request headers (secrets
// of the run), the address family of direct connections and the URL's known expiry.
// Refresh replaces it; gen counts the renewals.
type source struct {
	url     string
	headers http.Header
	network string
	expires time.Time
	host    string
	gen     uint64
}

func newSource(t media.RangedTrack, gen uint64) source {
	return source{url: t.URL, headers: t.Headers.Clone(), network: t.Network, expires: t.Expires, host: hostOf(t.URL), gen: gen}
}

// rtrack is a chosen track of a RunRanged job and its chunks.
type rtrack struct {
	id, key    string
	kind       media.TrackKind
	ext        string
	unranged   bool  // one whole-resource request (yt-dlp's no_resume, or ranges ignored)
	chunkBytes int64 // RangedTrack.ChunkBytes, or ChunkSize's when the caller left it 0
	size       int64 // bytes; -1 while unknown

	plan  *media.Plan
	chunk int64 // the checkpoint's ChunkBytes: chunkBytes for a ranged plan, else 0
	dir   string
	path  string
	file  *os.File
	base  int // index of the track's first chunk in the job (Progress.Workers)

	done     []byte // bitmap of durable chunks (the checkpoint)
	got      []byte // bitmap of completely written chunks, a superset of done
	ndone    int
	ngot     int
	gotBytes int64
	checked  bool // the first chunk's boxes passed the checks

	// wrote counts per chunk of a ranged plan the bytes written from the chunk's start
	// (its worker adds them; the main goroutine reads them): a retry, a renewal or a
	// resume continues after them. part is the durable prefix of each chunk without its
	// bit (main goroutine): what wrote was when a sync that completed began
	// (docs/SITE-MEDIA-SPEC.md §2.5). Whole-resource requests use neither.
	wrote []atomic.Int64
	part  []int64

	// open: the track lets the job's other chunks start (§2.4): it has one chunk, or the
	// init check of its first chunk passed, early on a received prefix or on the
	// complete chunk.
	open bool
	// headCheck: the first chunk is being fetched; its init check runs once it is
	// complete.
	headCheck bool
	// head counts the attempts at the first chunk (from bit headGenShift) and the bytes
	// the current one wrote from offset 0 (below), for the early check. Only the worker
	// fetching that chunk writes it.
	head atomic.Int64
	// headMode is headUndecided, headSplit (an early check let other chunks start: the
	// first chunk can no longer become the whole track) or headWhole (a 200 to the first
	// chunk claimed the whole track: no early check opens it).
	headMode atomic.Int32
	walk     earlyWalk // the early check's progress (main goroutine)

	// A part (ranged_clip.go): clip is the track's clip mode; a span of the resource
	// starts at base (the file holds resource byte base+i at offset i; size is then the
	// span's length, fullSize the resource's); headLen is the length of its head file,
	// moov a progressive file's 'moov', spanStart/spanEnd the times of the subsegments.
	clip               int
	spanBase           int64
	fullSize           int64
	headLen            int64
	moov               []byte
	spanStart, spanEnd time.Duration

	src source // guarded by rangedRunner.srcMu
}

// The early init check (docs/SITE-MEDIA-SPEC.md §2.4).
const (
	earlyStep    = 64 << 10 // the first chunk's worker reports at most once per this many bytes
	headGenShift = 40       // rtrack.head: the attempt above, the bytes below
	headBytes    = 1<<headGenShift - 1
)

// rtrack.headMode values.
const (
	headUndecided int32 = iota
	headSplit
	headWhole
)

// earlyWalk is how far the early check read the received prefix of a track's first
// chunk in attempt gen: the top-level boxes before off (boxes of them, an 'ftyp' among
// them when ftyp is set). done: no early decision is possible in this attempt, or one
// was made; the check of the complete chunk decides.
type earlyWalk struct {
	gen   int64
	off   int64
	boxes int
	ftyp  bool
	done  bool
}

// newHeadAttempt starts a new count of the first chunk's prefix at the bytes the chunk
// kept (wrote[0]): the attempt continues after them, and the early check walks them
// again. Called by the chunk's worker only.
func (s *rtrack) newHeadAttempt() {
	s.head.Store((s.head.Load()>>headGenShift+1)<<headGenShift | s.wrote[0].Load())
}

// claimWhole reports whether a 200 to the first chunk may become the whole track: no
// early check let other chunks start, and none can now. Called by the chunk's worker.
func (s *rtrack) claimWhole() bool {
	return s.headMode.CompareAndSwap(headUndecided, headWhole) || s.headMode.Load() == headWhole
}

func isBit(b []byte, i int) bool { return b[i>>3]&(1<<(i&7)) != 0 }
func setBit(b []byte, i int)     { b[i>>3] |= 1 << (i & 7) }

// whole reports a plan of one request for the whole resource.
func (s *rtrack) whole() bool {
	return len(s.plan.Fragments) == 1 && s.plan.Fragments[0].ByteRange == nil
}

// fragLen is the length of chunk i, -1 for a whole resource of unknown size.
func (s *rtrack) fragLen(i int) int64 {
	if br := s.plan.Fragments[i].ByteRange; br != nil {
		return br[1] - br[0] + 1
	}
	return s.size
}

// resetBits forgets every chunk of the plan: bits and prefixes.
func (s *rtrack) resetBits() {
	n := len(s.plan.Fragments)
	s.done, s.got = make([]byte, (n+7)/8), make([]byte, (n+7)/8)
	s.ndone, s.ngot, s.gotBytes = 0, 0, 0
	s.wrote, s.part = make([]atomic.Int64, n), make([]int64, n)
}

// prefixes reports whether chunks of s can keep a prefix: a ranged plan.
func (s *rtrack) prefixes() bool { return !s.whole() }

// chunkRef is chunk i of track s.
type chunkRef struct {
	s *rtrack
	i int
}

// rslot is a connection slot: its transport (for one address family), its copier and
// its view. Only the main goroutine assigns it work; while busy, only the worker
// goroutine running its chunk touches f, fnet, cp, wrote, extra and head.
type rslot struct {
	cv    *connView
	f     transport.Fetcher
	fnet  string
	cp    *copier
	wrote *atomic.Int64 // the chunk prefix the current 206 extends (rtrack.wrote); nil for a whole resource
	extra int64         // bytes of the current attempt's whole-resource copy (they restart from 0)
	head  *rtrack       // the track whose first chunk the current response writes from byte 0
	busy  bool          // main goroutine
}

// fetcher returns the slot's transport for the address family network.
func (sl *rslot) fetcher(tf transport.Factory, network string) transport.Fetcher {
	if sl.f == nil || sl.fnet != network {
		if sl.f != nil {
			sl.f.Close()
		}
		sl.f, sl.fnet = transport.WithNetwork(tf, network).NewSlot(), network
	}
	return sl.f
}

// chunkResult is a worker's report on one chunk. What a failed chunk wrote stays in its
// prefix (rtrack.wrote).
type chunkResult struct {
	ref   chunkRef
	slot  *rslot
	host  string // the host it was counted against
	size  int64  // bytes of the chunk (of the track when whole)
	extra int64  // bytes a whole-resource copy added to rangedRunner.written
	whole bool   // the server sent the whole track for chunk 0
	err   error
}

// rangedRunner is one RunRanged. Its fields are owned by the goroutine that called
// RunRanged, except the atomics, the sources (srcMu) and the renewal state (refMu).
type rangedRunner struct {
	o            Options
	sel          RangedSelection
	rt           retrier
	rep          reporter
	onCheckpoint func(Checkpoint) error
	resume       *Checkpoint // the checkpoint RunRanged resumes from (nil: none)
	clipDone     *model.MediaClip

	// audio: the download of a page without video (Options.AudioOnly, or one audio
	// track): MP4 data with a video track is refused.
	audio        bool
	tracks       []*rtrack
	total        int        // chunks of all tracks
	pending      []chunkRef // written chunks waiting for a sync
	pendingBytes int64
	lastSync     time.Time
	lastPartial  time.Time // the last sync that recorded the prefixes of every track
	dirty        bool      // the checkpoint changed since it was last saved
	lastCP       time.Time
	speed        speedMeter
	slots        []*connView // the views of the current phase
	pool         []*rslot
	active       map[string]int // busy slots per host
	penalty      map[string]int // a host's lowered connection cap after 429/503
	throttled    chan string    // hosts that answered 429/503
	headReady    chan struct{}  // a first chunk received more bytes (the early check)

	srcMu sync.Mutex
	gen   uint64 // address renewals so far

	refMu      sync.Mutex
	refCount   int
	refLast    time.Time
	refRunning chan struct{} // closed when the running renewal ends
	refFailed  error         // a failed renewal: the run fails

	bg       sync.WaitGroup // renewals ahead of expiry
	bgCtx    context.Context
	bgCancel context.CancelFunc

	// written counts the bytes written into chunks without their got bit (their
	// prefixes, rtrack.wrote) and those of whole-resource copies in flight: with the
	// complete chunks, Progress.BytesDone. A failed range keeps its bytes, a failed
	// whole-resource copy takes them back.
	written  atomic.Int64
	received atomic.Int64 // every payload byte received, for the speed average
	requests atomic.Int32 // requests in flight
}

func newRangedRunner(o Options, sel RangedSelection, onProgress func(Progress), onCheckpoint func(Checkpoint) error) *rangedRunner {
	r := &rangedRunner{
		o:            o,
		sel:          sel,
		rt:           retrier{clock: o.Clock, max: o.MaxRetries, base: o.RetryBaseDelay, maxDelay: o.RetryMaxDelay},
		rep:          reporter{onProgress: onProgress},
		onCheckpoint: onCheckpoint,
		active:       make(map[string]int),
		penalty:      make(map[string]int),
		throttled:    make(chan string, 64),
		headReady:    make(chan struct{}, 1),
	}
	r.bgCtx, r.bgCancel = context.WithCancel(context.Background())
	r.audio = o.AudioOnly || len(sel.Tracks) == 1 && sel.Tracks[0].Kind == media.KindAudio
	for _, t := range sel.Tracks {
		s := &rtrack{id: t.ID, key: t.Key, kind: t.Kind, ext: t.Ext, unranged: t.Unranged, chunkBytes: t.ChunkBytes, size: -1, src: newSource(t, 0)}
		if t.Size > 0 {
			s.size = t.Size
		}
		if s.chunkBytes == 0 {
			s.chunkBytes = ChunkSize(o.HostRules, t.URL, 0)
		}
		r.tracks = append(r.tracks, s)
	}
	return r
}

// close stops a renewal still running and releases the files and connections.
func (r *rangedRunner) close() {
	r.bgCancel()
	r.bg.Wait()
	for _, s := range r.tracks {
		if s.file != nil {
			_ = s.file.Close()
			s.file = nil
		}
	}
	for _, sl := range r.pool {
		if sl.f != nil {
			sl.f.Close()
		}
	}
}

func (r *rangedRunner) source(s *rtrack) source {
	r.srcMu.Lock()
	defer r.srcMu.Unlock()
	return s.src
}

func (r *rangedRunner) generation() uint64 {
	r.srcMu.Lock()
	defer r.srcMu.Unlock()
	return r.gen
}

// waitNotBefore waits until sel.NotBefore.
func (r *rangedRunner) waitNotBefore(ctx context.Context) error {
	if r.sel.NotBefore.IsZero() {
		return ctx.Err()
	}
	wait := r.sel.NotBefore.Sub(r.o.Clock.Now())
	if wait > maxNotBefore {
		return fmt.Errorf("%w: the formats become available only in %s", ErrInvalidSelection, wait.Round(time.Minute))
	}
	return sleep(ctx, r.o.Clock, wait)
}

// planTracks finds the size of tracks that have none and plans every track.
func (r *rangedRunner) planTracks(ctx context.Context) error {
	for _, s := range r.tracks {
		if s.size < 0 && !s.unranged {
			if err := r.probeSize(ctx, s); err != nil {
				return err
			}
		}
		if !r.sel.Clip.IsZero() {
			if s.size > 0 && !s.unranged {
				var prev *TrackCheckpoint
				if r.resume != nil {
					prev = resumedTrack(r.resume.Tracks, &track{id: s.id, key: s.key})
				}
				if err := r.clipHead(ctx, s, prev); err != nil {
					return err
				}
			} else {
				s.clip = clipWhole
			}
		}
		if err := r.replan(s); err != nil {
			return err
		}
	}
	return nil
}

// replan plans s from its current size, chunk size and unranged flag.
func (r *rangedRunner) replan(s *rtrack) error {
	src := r.source(s)
	plan, err := media.PlanRanged(media.RangedTrack{
		ID: s.id, Key: s.key, Kind: s.kind, Ext: s.ext, URL: src.url, Size: max(s.size, 0),
		ChunkBytes: s.chunkBytes, Network: src.network, Unranged: s.unranged,
	})
	if err != nil {
		return fmt.Errorf("mediajob: track %q: %w", s.id, err)
	}
	s.plan = plan
	s.chunk = 0
	if !s.whole() {
		s.chunk = s.chunkBytes
	}
	return nil
}

// probeSize asks for "bytes=0-0" of a track of unknown size: Content-Range gives the
// total; a server that ignores the range makes the track one whole request.
func (r *rangedRunner) probeSize(ctx context.Context, s *rtrack) error {
	err := r.retry(ctx, s, nil, func(src source) error {
		f := transport.WithNetwork(r.o.Factory, src.network).NewSlot()
		defer f.Close()
		resp, err := f.Fetch(ctx, transport.RangeRequest{Request: model.Request{URL: src.url, Headers: src.headers}, Start: 0, End: 0})
		if err != nil {
			return redactURL(err)
		}
		defer resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusPartialContent:
			cr := resp.ContentRange
			switch {
			case cr == nil || cr.Start != 0:
				return fmt.Errorf("%w: the answer to bytes 0-0 has no usable Content-Range", ErrRangeMismatch)
			case cr.Total > 0:
				s.size = cr.Total
			default:
				s.unranged = true // a total of "*": ranges cannot be planned
			}
			return nil
		case http.StatusOK:
			s.unranged = true
			if resp.ContentLength > 0 {
				s.size = resp.ContentLength
			}
			return nil
		}
		return &StatusError{Code: resp.StatusCode, RetryAfter: resp.RetryAfter}
	})
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		return fmt.Errorf("mediajob: track %q: finding its size: %w", s.id, err)
	}
	return nil
}

// prepare opens the track files, taking over the chunks of resume that are still
// valid, and allocates the files of tracks that start over. Before it recreates the file
// of a resumed track or removes the directory of a track no longer chosen, it stores a
// checkpoint without their chunks (an error fails the run with nothing changed): a crash
// in between must never leave old bits over a new, zero-filled file.
func (r *rangedRunner) prepare(ctx context.Context, resume *Checkpoint) error {
	var prev []TrackCheckpoint
	if resume != nil {
		prev = resume.Tracks
	}
	if err := os.MkdirAll(r.o.PartDir, 0o700); err != nil {
		return diskErr("creating the part directory", err)
	}
	dirs := make(map[string]bool)
	for _, s := range r.tracks {
		s.dir = filepath.Join(r.o.PartDir, dirName(s.kind, s.id))
		s.path = filepath.Join(s.dir, dataName)
		dirs[s.dir] = true
	}
	taken := make([]bool, len(prev))
	var fresh []*rtrack
	for _, s := range r.tracks {
		s.base = r.total
		r.total += len(s.plan.Fragments)
		s.resetBits()
		cp := resumedTrack(prev, &track{id: s.id, key: s.key})
		for i := range prev {
			if &prev[i] == cp {
				taken[i] = true
			}
		}
		if cp != nil {
			ok, err := r.adopt(ctx, s, cp)
			if err != nil {
				return err
			}
			if ok {
				continue
			}
			r.dirty = true // the checkpoint below drops its chunks
		}
		fresh = append(fresh, s)
	}
	var stale []string
	for i, cp := range prev {
		if taken[i] {
			continue
		}
		r.dirty = true
		for _, k := range [...]media.TrackKind{media.KindVideo, media.KindAudio, media.KindMuxed} {
			if d := filepath.Join(r.o.PartDir, dirName(k, cp.TrackID)); !dirs[d] {
				stale = append(stale, d)
			}
		}
	}
	if err := r.checkpoint(true); err != nil {
		return err
	}
	// Directories of checkpointed tracks that are no longer chosen only take space.
	for _, d := range stale {
		_ = os.RemoveAll(d)
	}
	if err := r.checkSpace(); err != nil {
		return err
	}
	for _, s := range fresh {
		if err := r.create(s); err != nil {
			return err
		}
	}
	return nil
}

// adopt takes over the durable chunks and chunk prefixes cp records for s when its plan
// and file still match them, and reports whether it did. A file that is missing or does not match
// makes the track start over; one that is there but cannot be opened (after the retries
// of a file another process briefly holds) is an error, so that its chunks are kept for
// a later run.
func (r *rangedRunner) adopt(ctx context.Context, s *rtrack, cp *TrackCheckpoint) (bool, error) {
	n := len(s.plan.Fragments)
	if cp.Total != n || len(cp.Done) != (n+7)/8 || cp.ChunkBytes != s.chunk || cp.Base != s.spanBase || cp.Head != s.headLen || cp.First != 0 {
		return false, nil
	}
	fi, err := os.Stat(s.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, diskErr("reading a track file", err)
	case !fi.Mode().IsRegular():
		return false, nil
	}
	switch {
	case s.size >= 0 && fi.Size() != s.size:
		return false, nil
	case s.size < 0:
		// A whole resource of unknown size is kept only when it was finished.
		if n != 1 || !isBit(cp.Done, 0) || fi.Size() <= 0 {
			return false, nil
		}
	}
	var f *os.File
	err = withSharingRetry(ctx, r.o.Clock, func() error {
		var oerr error
		f, oerr = os.OpenFile(s.path, os.O_RDWR, 0)
		return oerr
	})
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil && ctx.Err() != nil:
		return false, ctx.Err()
	case err != nil:
		return false, diskErr("opening a track file", err)
	}
	if s.size < 0 {
		s.size = fi.Size()
	}
	s.file = f
	for i := range n {
		if isBit(cp.Done, i) {
			setBit(s.done, i)
			setBit(s.got, i)
			s.ndone++
			s.ngot++
			s.gotBytes += s.fragLen(i)
		}
	}
	// The durable prefixes of the other chunks; an entry that does not fit the plan is
	// ignored (that chunk is fetched in full).
	if s.prefixes() {
		for i, p := range cp.Partial {
			if i < 0 || i >= n || isBit(s.done, i) || p <= 0 || p >= s.fragLen(i) {
				continue
			}
			s.part[i] = p
			s.wrote[i].Store(p)
			r.written.Add(p)
		}
	}
	return true, nil
}

// create starts s over: an empty directory with a new file of the track's size.
func (r *rangedRunner) create(s *rtrack) error {
	_ = os.RemoveAll(s.dir)
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return diskErr("creating the part directory", err)
	}
	f, err := os.OpenFile(s.path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return diskErr("creating a track file", err)
	}
	s.file = f
	if s.size > 0 {
		if err := platform.Preallocate(f, s.size); err != nil {
			return diskErr("allocating a track file", err)
		}
	}
	return nil
}

// checkSpace refuses a job whose volume lacks room for the rest of its tracks, the
// assembled output and freeSpaceMargin (docs/SITE-MEDIA-SPEC.md §9).
func (r *rangedRunner) checkSpace() error {
	if r.o.FreeSpace == nil {
		return nil
	}
	need := int64(freeSpaceMargin)
	for _, s := range r.tracks {
		if s.size > 0 {
			need += s.size - s.gotBytes
			// The output is written beside the tracks when they are joined or a part is cut
			// from them (docs/MEDIA-SPEC.md §8 C18); a whole copied file is moved instead.
			if r.sel.Output == OutputRemux || !r.sel.Clip.IsZero() {
				need += s.size
			}
		}
	}
	free, err := r.o.FreeSpace(r.o.PartDir)
	if err != nil || free >= need {
		return nil //nolint:nilerr // free space unknown: the writes report a full volume
	}
	return fmt.Errorf("%w: %d MB free, %d MB needed", ErrDiskFull, free/1_000_000, (need+999_999)/1_000_000)
}

// run fetches the chunks of every track, checks the first chunk of each and assembles
// the output. Until the init check of every track of several chunks has passed, only
// those tracks' first chunks are requested (docs/SITE-MEDIA-SPEC.md §2.4); a check may
// pass early, on the received prefix of a first chunk (a resumed one's durable prefix
// first), and runs on the complete chunk in any case. A track resumed with its first
// chunk durable is checked from its file before anything is requested.
func (r *rangedRunner) run(ctx context.Context) (string, error) {
	for _, s := range r.tracks {
		switch {
		case s.clip == clipFrag || s.clip == clipProg:
			// A span of the resource: its head was checked (clipHead).
			s.open, s.checked = true, true
		case isBit(s.got, 0):
			if err := r.checkInit(s, false); err != nil {
				return "", err
			}
			s.open = true
		case len(s.plan.Fragments) > 1:
			s.headCheck = true
			// The durable prefix of a resumed first chunk is its received prefix: it may
			// pass the early check before anything is requested.
			s.head.Store(s.wrote[0].Load())
		default:
			s.open = true
		}
	}
	r.earlyChecks()
	if err := r.fetchChunks(ctx); err != nil {
		return "", err
	}
	return r.assemble(ctx)
}

// phase is one fetchChunks: per track the chunks with a prefix (taken first, so that
// what they kept is completed), the position from which nextChunk looks for the next
// other chunk and the chunks taken, and the slots that fetch them.
type phase struct {
	first   [][]int
	next    []int
	taken   [][]byte
	slots   []*rslot
	busy    int
	results chan chunkResult
}

// fetchChunks fetches every chunk not written yet. This goroutine assigns them to slots
// (nextChunk: the video or muxed track's first, within each host's connection cap,
// behind the init gate), records completions, runs the init checks, syncs, checkpoints
// and reports progress. The first failure stops the other workers. A cancelled ctx
// returns ctx.Err() after the sync and the checkpoint, which keep the prefixes of the
// chunks that were still arriving.
func (r *rangedRunner) fetchChunks(parent context.Context) error {
	total := 0
	hosts := make(map[string]bool)
	for _, s := range r.tracks {
		if left := len(s.plan.Fragments) - s.ngot; left > 0 {
			total += left
			hosts[r.source(s).host] = true
		}
	}
	if err := parent.Err(); err != nil || total == 0 {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	// Slots dial only when they are first used.
	n := min(total, r.o.hostBudget(hosts, r.o.MaxConnections))
	for len(r.pool) < n {
		r.pool = append(r.pool, r.newSlot())
	}
	now := r.o.Clock.Now()
	ph := &phase{first: make([][]int, len(r.tracks)), next: make([]int, len(r.tracks)), taken: make([][]byte, len(r.tracks)),
		slots: r.pool[:n], results: make(chan chunkResult, n)}
	for k, s := range r.tracks {
		ph.taken[k] = make([]byte, len(s.done))
		for i := range s.wrote {
			if !isBit(s.got, i) && s.wrote[i].Load() > 0 {
				ph.first[k] = append(ph.first[k], i)
			}
		}
	}
	r.slots = make([]*connView, n)
	for k, sl := range ph.slots {
		sl.cv = newConnView(k + 1)
		sl.cv.speed.reset(now, 0)
		sl.busy = false
		r.slots[k] = sl.cv
	}

	var failed error
	fail := func(err error) {
		if failed == nil {
			failed = err
			cancel()
		}
	}
	tick := r.o.Clock.NewTimer(progressInterval)
	defer func() { tick.Stop() }()
	for {
		if failed == nil && ctx.Err() == nil {
			r.dispatch(ctx, ph)
		}
		if ph.busy == 0 {
			break
		}
		select {
		case res := <-ph.results:
			ph.busy--
			res.slot.busy = false
			r.active[res.host]--
			switch {
			case res.err != nil:
				fail(res.err)
			default:
				if err := r.complete(res); err != nil {
					fail(err)
				} else if err := r.headDone(res.ref); err != nil {
					fail(err)
				} else if r.pendingBytes >= syncBytes {
					if err := r.syncDurable(false); err != nil {
						fail(err)
					}
				}
				if err := r.checkpoint(false); err != nil {
					fail(err)
				}
			}
		case <-r.headReady:
			if failed == nil {
				r.earlyChecks()
			}
		case host := <-r.throttled:
			r.halve(host)
		case <-tick.C():
			now := r.o.Clock.Now()
			r.speed.sample(now, r.received.Load())
			for _, cv := range r.slots {
				cv.speed.sample(now, cv.total.Load())
			}
			r.emit(false)
			partials := now.Sub(r.lastPartial) >= partialSyncInterval
			if partials || len(r.pending) > 0 && now.Sub(r.lastSync) >= syncInterval {
				if err := r.syncDurable(partials); err != nil {
					fail(err)
				}
			}
			if err := r.checkpoint(false); err != nil {
				fail(err)
			}
			r.renewAhead(now)
			tick = r.o.Clock.NewTimer(progressInterval)
		}
	}
	// Every worker has stopped: the prefixes of the chunks they left are final.
	if err := r.syncDurable(true); err != nil && failed == nil {
		failed = err
	}
	if err := r.checkpoint(true); err != nil && failed == nil {
		failed = err
	}
	if err := parent.Err(); err != nil {
		return err
	}
	if failed == nil {
		for _, sl := range ph.slots {
			sl.cv.set(model.WorkerDone, "")
		}
	}
	return failed
}

// headDone runs the init check of a track's first chunk once it is complete (as a
// range, or as the whole track): a failure fails the run, a pass opens the track. An
// early pass on a prefix (earlyChecks) does not replace it.
func (r *rangedRunner) headDone(ref chunkRef) error {
	s := ref.s
	if ref.i != 0 || !s.headCheck {
		return nil
	}
	s.headCheck = false
	if err := r.checkInit(s, false); err != nil {
		return err
	}
	s.open = true
	return nil
}

// earlyChecks runs the early init check on the received prefix of every first chunk
// that is still arriving and has not opened its track. A pass opens the track, unless
// a 200 to that chunk claimed the whole track first.
func (r *rangedRunner) earlyChecks() {
	for _, s := range r.tracks {
		if !s.open && s.headCheck && r.earlyPass(s) && s.headMode.CompareAndSwap(headUndecided, headSplit) {
			s.open = true
		}
	}
}

// earlyPass reports whether the received prefix of the first chunk of s passes the init
// check (docs/SITE-MEDIA-SPEC.md §2.4): to be copied, its first top-level box is an
// 'ftyp' and a complete 'moov' passes media.CheckInit; to be joined, an 'ftyp' comes
// before a complete 'moov' that passes media.CheckInit and holds an 'mvex'. Any other
// prefix is undecided, never refused: the check of the complete chunk decides then. It
// reads box headers only, from where the last call stopped, until the 'moov' is
// complete, and only bytes the current attempt wrote (a new attempt is walked again).
func (r *rangedRunner) earlyPass(s *rtrack) bool {
	w := &s.walk
	v := s.head.Load()
	if gen := v >> headGenShift; gen != w.gen {
		*w = earlyWalk{gen: gen}
	}
	pass := r.walkPrefix(s, w, v&headBytes)
	if s.head.Load()>>headGenShift != w.gen {
		// Another attempt started while the prefix was read: what was read may be its
		// bytes. Walk again at the next report.
		*w = earlyWalk{gen: -1}
		return false
	}
	return pass
}

// walkPrefix continues the early check's walk w over the first limit bytes of the first
// chunk of s and reports a pass.
func (r *rangedRunner) walkPrefix(s *rtrack, w *earlyWalk, limit int64) bool {
	reach := min(s.fragLen(0), maxInitBytes) // what the check of the complete chunk reads
	limit = min(limit, reach)
	if r.mp3(s) {
		if limit < mp3HeadBytes {
			return false
		}
		w.done = true
		var head [mp3HeadBytes]byte
		_, err := s.file.ReadAt(head[:], 0)
		return err == nil && checkMP3Head(head[:]) == nil
	}
	copied := r.sel.Output == OutputCopy
	var hdr [16]byte
	for !w.done {
		if w.off >= reach || w.boxes >= maxTopBoxes {
			w.done = true // no complete 'moov' within what the full check reads
			break
		}
		k := min(int64(len(hdr)), limit-w.off)
		if k < 8 {
			break // the next header has not arrived yet
		}
		if _, err := s.file.ReadAt(hdr[:k], w.off); err != nil {
			w.done = true
			break
		}
		b, ok, err := parseBoxHeader(hdr[:k], w.off, s.size)
		switch {
		case err != nil:
			w.done = true
			continue
		case !ok && limit < reach:
			return false // a 64-bit size that has not arrived yet
		case !ok, copied && w.boxes == 0 && b.typ != "ftyp":
			w.done = true
			continue
		case b.typ != "moov":
			w.ftyp = w.ftyp || b.typ == "ftyp"
			w.boxes++
			w.off = b.end()
			continue
		case b.end() > reach || !copied && !w.ftyp:
			w.done = true
			continue
		case b.end() > limit:
			return false // the 'moov' is still arriving
		}
		w.done = true // decided: a pass, or the check of the complete chunk decides
		buf := make([]byte, b.end())
		if _, err := s.file.ReadAt(buf, 0); err != nil || media.CheckInit(buf) != nil {
			return false
		}
		if r.audio && checkNoVideo(s.id, buf[b.off:]) != nil {
			return false // refused by the check of the complete chunk
		}
		return copied || hasChildBox(buf[b.off+b.hdr:], "mvex")
	}
	return false
}

func (r *rangedRunner) newSlot() *rslot {
	sl := &rslot{}
	sl.cp = newCopier(nil, func(n int) {
		if w := sl.wrote; w != nil {
			w.Add(int64(n))
		} else {
			sl.extra += int64(n)
		}
		r.written.Add(int64(n))
		r.received.Add(int64(n))
		sl.cv.received.Add(int64(n))
		sl.cv.total.Add(int64(n))
		sl.cv.receiving()
		if s := sl.head; s != nil {
			// The bytes are written: they count toward the first chunk's prefix. The
			// main goroutine is told at most once per earlyStep.
			v := s.head.Add(int64(n)) & headBytes
			if v/earlyStep != (v-int64(n))/earlyStep {
				select {
				case r.headReady <- struct{}{}:
				default:
				}
			}
		}
	})
	sl.cp.clock = r.o.Clock // the stall watchdog runs on the job's clock
	return sl
}

// dispatch gives idle slots the next chunks whose hosts have a connection left.
func (r *rangedRunner) dispatch(ctx context.Context, ph *phase) {
	for _, sl := range ph.slots {
		if sl.busy {
			continue
		}
		ref, host, ok := r.nextChunk(ph)
		if !ok {
			return
		}
		sl.busy = true
		ph.busy++
		r.active[host]++
		go r.work(ctx, sl, ref, ref.s.base+ref.i, host, ph.results)
	}
}

// nextChunk takes the next chunk not written yet whose host has a connection left, the
// video or muxed track's before the audio track's, and of a track its chunks with a
// prefix first. Until every track is open, it takes only the first chunks of the tracks
// that are not (the init gate, §2.4).
func (r *rangedRunner) nextChunk(ph *phase) (chunkRef, string, bool) {
	open := true
	for _, s := range r.tracks {
		open = open && s.open
	}
	for k, s := range r.tracks {
		i, ok := ph.candidate(k, s, open)
		if !ok {
			continue
		}
		host := r.source(s).host
		if r.active[host] >= r.hostCap(host) {
			continue
		}
		setBit(ph.taken[k], i)
		return chunkRef{s, i}, host, true
	}
	return chunkRef{}, "", false
}

// candidate is the chunk of track k (s) that nextChunk would take next.
func (ph *phase) candidate(k int, s *rtrack, open bool) (int, bool) {
	n := len(s.plan.Fragments) // a track that became whole has one chunk now
	free := func(i int) bool { return i < n && !isBit(s.got, i) && !isBit(ph.taken[k], i) }
	if !open {
		return 0, !s.open && free(0)
	}
	for len(ph.first[k]) > 0 {
		if i := ph.first[k][0]; free(i) {
			return i, true
		}
		ph.first[k] = ph.first[k][1:]
	}
	for ph.next[k] < n && !free(ph.next[k]) {
		ph.next[k]++
	}
	return ph.next[k], ph.next[k] < n
}

// hostCap is the number of connections host may have: MaxConnections, the host rule's
// cap and the cap lowered after 429/503, whichever is smallest.
func (r *rangedRunner) hostCap(host string) int {
	c := r.o.MaxConnections
	if h := r.o.hostConnections(host); h > 0 {
		c = min(c, h)
	}
	if p, ok := r.penalty[host]; ok {
		c = min(c, p)
	}
	return max(c, 1)
}

// halve lowers host's connections to half of those it has (at least 1) for the rest
// of the run, after it answered 429 or 503.
func (r *rangedRunner) halve(host string) {
	r.penalty[host] = max(1, min(r.hostCap(host), max(r.active[host], 1))/2)
}

// reportThrottle tells the main goroutine that host asked to slow down.
func (r *rangedRunner) reportThrottle(host string) {
	select {
	case r.throttled <- host:
	default: // enough reports are pending: the cap is halved already
	}
}

// work fetches one chunk on slot sl (a goroutine per chunk) and reports the result.
func (r *rangedRunner) work(ctx context.Context, sl *rslot, ref chunkRef, frag int, host string, results chan<- chunkResult) {
	start, end := int64(-1), int64(-1)
	if br := ref.s.plan.Fragments[ref.i].ByteRange; br != nil {
		start, end = br[0], br[1]
	}
	sl.cv.assign(frag, start, end)
	res := chunkResult{ref: ref, slot: sl, host: host}
	res.whole, res.size, res.err = r.fetchChunk(ctx, sl, ref)
	res.extra = sl.extra
	switch {
	case res.err == nil:
		sl.cv.set(model.WorkerIdle, "")
	case ctx.Err() == nil:
		sl.cv.set(model.WorkerError, shortReason(res.err))
	}
	results <- res
}

// fetchChunk downloads one chunk with retries and address renewals. Each attempt at a
// range continues after the bytes the chunk kept (rtrack.wrote); a whole resource
// starts over.
func (r *rangedRunner) fetchChunk(ctx context.Context, sl *rslot, ref chunkRef) (whole bool, n int64, err error) {
	s, i := ref.s, ref.i
	err = r.retry(ctx, s, sl.cv, func(src source) error {
		sl.extra = 0
		if i == 0 {
			s.newHeadAttempt()
		}
		f := sl.fetcher(r.o.Factory, src.network)
		sl.cp.lim = r.o.hostThrottle(src.host)
		tctx, gen := sl.cv.trace(ctx)
		var aerr error
		whole, n, aerr = r.attempt(tctx, f, sl, s, i, src)
		sl.cv.end(gen)
		if aerr != nil {
			r.written.Add(-sl.extra)
			sl.extra = 0
			if errors.Is(aerr, errStalled) {
				// As in Run: the retry dials anew rather than reuse an idle connection
				// that may be as dead as the stalled one.
				f.Close()
			}
		}
		return aerr
	})
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return false, 0, cerr
		}
		if isDiskFull(err) {
			err = fmt.Errorf("%w: %w", ErrDiskFull, err)
		}
		return false, 0, fmt.Errorf("mediajob: track %q part %d: %w", s.id, i+1, err)
	}
	return whole, n, nil
}

// retry runs attempt with the track's current source until it succeeds, fails
// permanently, fails MaxRetries times in a row or ctx is done (ctx.Err()). A 403 or 410
// renews the addresses (not counted as a failure); the same attempt refused again
// after a renewal fails (ErrRefusedAgain). A 429 or 503 halves the host's connections. cv (may be nil)
// shows the waits.
func (r *rangedRunner) retry(ctx context.Context, s *rtrack, cv *connView, attempt func(src source) error) error {
	expired, expiredGen := false, uint64(0)
	for failures := 0; ; {
		src := r.source(s)
		err := attempt(src)
		if err == nil {
			return nil
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		var se *StatusError
		isStatus := errors.As(err, &se)
		if isStatus && (se.Code == http.StatusForbidden || se.Code == http.StatusGone) && r.sel.Refresh != nil {
			if expired && src.gen > expiredGen {
				return fmt.Errorf("%w: %w", ErrRefusedAgain, err)
			}
			expired, expiredGen = true, src.gen
			if cv != nil {
				cv.set(model.WorkerConnecting, detailRenewing)
			}
			if rerr := r.refresh(ctx, src.gen); rerr != nil {
				if errors.Is(rerr, errRefreshLimit) {
					return fmt.Errorf("%w: %w", rerr, err)
				}
				return rerr
			}
			continue
		}
		if !errors.Is(err, errWholeBody) && !transient(err) {
			return err
		}
		failures++
		if failures >= r.o.MaxRetries {
			return fmt.Errorf("giving up after %d attempts: %w", failures, err)
		}
		if isStatus && (se.Code == http.StatusTooManyRequests || se.Code == http.StatusServiceUnavailable) {
			r.reportThrottle(src.host)
		}
		wait := r.rt.delay(failures, retryAfterOf(err))
		if cv != nil {
			cv.retry(shortReason(err), r.o.Clock.Now().Add(wait))
		}
		if serr := sleep(ctx, r.o.Clock, wait); serr != nil {
			return serr
		}
	}
}

// attempt fetches chunk i of s once from src: the rest of its byte range after the bytes
// it kept, in requests of at most the host's request cap, written at its offset; or the
// whole resource.
func (r *rangedRunner) attempt(ctx context.Context, f transport.Fetcher, sl *rslot, s *rtrack, i int, src source) (whole bool, n int64, err error) {
	br := s.plan.Fragments[i].ByteRange
	if br == nil {
		n, err := r.fetchWhole(ctx, f, sl, s, src)
		return false, n, err
	}
	start, end := br[0], br[1]
	limit := r.o.hostRequestCap(src.host)
	for {
		pos := start + s.wrote[i].Load()
		if pos > end {
			break
		}
		reqEnd := cappedEnd(pos, end, limit)
		r.requests.Add(1)
		resp, err := f.Fetch(ctx, transport.RangeRequest{
			Request: model.Request{URL: src.url, Headers: src.headers},
			Start:   pos + s.spanBase,
			End:     reqEnd + s.spanBase,
		})
		if err != nil {
			r.requests.Add(-1)
			return false, 0, redactURL(err)
		}
		full, err := r.readChunk(ctx, resp, sl, s, i, pos, reqEnd)
		_ = resp.Body.Close()
		r.requests.Add(-1)
		switch {
		case err != nil:
			return false, 0, err
		case full:
			return len(s.plan.Fragments) > 1, s.size, nil
		}
	}
	return false, end - start + 1, nil
}

// readChunk writes the part of resp that the request for [pos, reqEnd] of chunk i asked
// for at its offset, extending the chunk's prefix (rtrack.wrote). A 206 may end early
// (the caller asks for the rest). A 200 to a request of chunk 0 from byte 0 whose length
// is the track's size is the whole track, written at once (full); a 200 to a resumed
// chunk 0 drops its prefix so that the retry asks from byte 0, unless an early check has
// started other chunks (the track can no longer become whole).
func (r *rangedRunner) readChunk(ctx context.Context, resp *transport.Response, sl *rslot, s *rtrack, i int, pos, reqEnd int64) (full bool, err error) {
	// A span of the resource (ranged_clip.go) asks for its bytes at their offset in the
	// resource, base more than in the file.
	base, resSize := s.spanBase, s.size
	if s.fullSize > 0 {
		resSize = s.fullSize
	}
	switch resp.StatusCode {
	case http.StatusPartialContent:
		cr := resp.ContentRange
		switch {
		case cr == nil:
			return false, fmt.Errorf("%w: 206 without a usable Content-Range", ErrRangeMismatch)
		case cr.Start != pos+base || cr.End < cr.Start || cr.End > reqEnd+base:
			return false, fmt.Errorf("%w: asked for bytes %d-%d, got %d-%d", ErrRangeMismatch, pos+base, reqEnd+base, cr.Start, cr.End)
		case cr.Total >= 0 && resSize >= 0 && cr.Total != resSize:
			return false, fmt.Errorf("%w: the resource has %d bytes, not the %d declared", ErrRangeMismatch, cr.Total, resSize)
		}
		length := cr.End - cr.Start + 1
		if resp.ContentLength >= 0 && resp.ContentLength != length {
			return false, fmt.Errorf("%w: Content-Length %d disagrees with Content-Range", ErrRangeMismatch, resp.ContentLength)
		}
		if i == 0 && len(s.plan.Fragments) > 1 && s.head.Load()&headBytes == pos {
			sl.head = s // the bytes from byte 0 count toward the early check
		}
		sl.wrote = &s.wrote[i]
		_, err := sl.cp.copy(ctx, io.NewOffsetWriter(s.file, pos), resp.Body, length, true)
		sl.head, sl.wrote = nil, nil
		return false, err
	case http.StatusOK:
		switch {
		case i != 0 || s.fullSize > 0:
			return false, errWholeBody
		case pos != 0:
			if len(s.plan.Fragments) == 1 || s.headMode.Load() != headSplit {
				r.written.Add(-s.wrote[0].Swap(0))
			}
			return false, errWholeBody
		case s.size <= 0 || resp.ContentLength != s.size:
			return false, fmt.Errorf("%w: the whole resource has %d bytes, not the %d declared", ErrRangeMismatch, resp.ContentLength, s.size)
		case len(s.plan.Fragments) > 1 && !s.claimWhole():
			return false, errWholeBody // an early check started other chunks already
		}
		_, err := sl.cp.copy(ctx, io.NewOffsetWriter(s.file, 0), resp.Body, s.size, true)
		return err == nil, err
	case http.StatusRequestedRangeNotSatisfiable:
		return false, fmt.Errorf("%w: the resource is shorter than the %d bytes declared", ErrRangeMismatch, s.size)
	}
	return false, &StatusError{Code: resp.StatusCode, RetryAfter: resp.RetryAfter}
}

// fetchWhole fetches the whole resource of an unranged track from byte 0 (an attempt
// that is interrupted starts over).
func (r *rangedRunner) fetchWhole(ctx context.Context, f transport.Fetcher, sl *rslot, s *rtrack, src source) (int64, error) {
	spec := fetchSpec{url: src.url, headers: src.headers}
	if s.size > 0 {
		spec.rng, spec.total = &[2]int64{0, s.size - 1}, s.size
	} else if err := s.file.Truncate(0); err != nil {
		return 0, &localError{err}
	}
	_, n, err := fetchInto(ctx, f, spec, io.NewOffsetWriter(s.file, 0), sl.cp, func(d int) { r.requests.Add(int32(d)) })
	if err == nil && n == 0 {
		err = fmt.Errorf("%w: the resource is empty", ErrTruncated)
	}
	return n, err
}

// refresh renews the addresses unless they were renewed since generation seen: at most
// once per refreshGap and maxRefreshes times per run, one renewal at a time (the
// others wait for it). A failed renewal fails every later call.
func (r *rangedRunner) refresh(ctx context.Context, seen uint64) error {
	for {
		if r.generation() > seen {
			return nil
		}
		r.refMu.Lock()
		if err := r.refFailed; err != nil {
			r.refMu.Unlock()
			return err
		}
		if ch := r.refRunning; ch != nil {
			r.refMu.Unlock()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if r.refCount >= maxRefreshes {
			r.refMu.Unlock()
			return errRefreshLimit
		}
		if r.refCount > 0 {
			if wait := r.refLast.Add(refreshGap).Sub(r.o.Clock.Now()); wait > 0 {
				r.refMu.Unlock()
				if err := sleep(ctx, r.o.Clock, wait); err != nil {
					return err
				}
				continue
			}
		}
		ch := make(chan struct{})
		r.refRunning = ch
		r.refCount++
		r.refMu.Unlock()

		err := r.renew(ctx)
		r.refMu.Lock()
		r.refRunning = nil
		r.refLast = r.o.Clock.Now()
		if err != nil && ctx.Err() == nil {
			r.refFailed = err
		}
		r.refMu.Unlock()
		close(ch)
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		return err
	}
}

// renewAhead starts a renewal in the background when the known expiry of a track with
// chunks left is less than refreshAhead away and a renewal is allowed now. Its failure
// is not fatal: a later 403 renews again or fails the run.
func (r *rangedRunner) renewAhead(now time.Time) {
	if r.sel.Refresh == nil {
		return
	}
	soon := false
	r.srcMu.Lock()
	for _, s := range r.tracks {
		exp := s.src.expires
		if s.ngot < len(s.plan.Fragments) && !exp.IsZero() && exp.Sub(now) < refreshAhead {
			soon = true
		}
	}
	r.srcMu.Unlock()
	if !soon {
		return
	}
	r.refMu.Lock()
	if r.refRunning != nil || r.refFailed != nil || r.refCount >= maxRefreshes ||
		(r.refCount > 0 && now.Sub(r.refLast) < refreshGap) {
		r.refMu.Unlock()
		return
	}
	ch := make(chan struct{})
	r.refRunning = ch
	r.refCount++
	r.refMu.Unlock()
	r.bg.Add(1)
	go func() {
		defer r.bg.Done()
		_ = r.renew(r.bgCtx)
		r.refMu.Lock()
		r.refRunning = nil
		r.refLast = r.o.Clock.Now()
		r.refMu.Unlock()
		close(ch)
	}()
}

// renew calls sel.Refresh and replaces the sources of every track by Key.
func (r *rangedRunner) renew(ctx context.Context) error {
	fresh, err := r.sel.Refresh(ctx)
	if err != nil {
		return fmt.Errorf("mediajob: renewing the addresses: %w", err)
	}
	srcs := make([]source, len(r.tracks))
	r.srcMu.Lock()
	gen := r.gen + 1
	r.srcMu.Unlock()
	for k, s := range r.tracks {
		t := findRanged(fresh, s.id, s.key)
		if t == nil {
			return fmt.Errorf("%w: %w: the site no longer offers the chosen %s track (%q)", ErrInvalidSelection, media.ErrNotFound, s.kind, s.id)
		}
		// Only the address is checked: one whole request, whatever the track's size.
		if _, err := media.PlanRanged(media.RangedTrack{Kind: s.kind, Ext: s.ext, URL: t.URL, Network: t.Network, Unranged: true}); err != nil {
			return fmt.Errorf("mediajob: the renewed address of track %q: %w", s.id, err)
		}
		srcs[k] = newSource(*t, gen)
	}
	r.srcMu.Lock()
	defer r.srcMu.Unlock()
	if r.gen+1 != gen {
		return nil // renewed meanwhile (cannot happen: one renewal at a time)
	}
	r.gen = gen
	for k, s := range r.tracks {
		s.src = srcs[k]
	}
	return nil
}

// findRanged returns the track of ts with key (by ID when key is ""), or nil.
func findRanged(ts []media.RangedTrack, id, key string) *media.RangedTrack {
	for i := range ts {
		if key != "" && ts[i].Key == key || key == "" && ts[i].Key == "" && ts[i].ID == id {
			return &ts[i]
		}
	}
	return nil
}

// complete records a written chunk; its bit is set by the next sync. Its bytes move from
// written to the complete chunks.
func (r *rangedRunner) complete(res chunkResult) error {
	s, i := res.ref.s, res.ref.i
	r.written.Add(-res.extra)
	if res.whole {
		return r.becomeWhole(s, res.size)
	}
	if isBit(s.got, i) {
		return nil
	}
	r.written.Add(-s.wrote[i].Load())
	setBit(s.got, i)
	s.ngot++
	s.gotBytes += res.size
	if s.size < 0 {
		s.size = res.size // a whole resource of unknown size
	}
	r.pending = append(r.pending, res.ref)
	r.pendingBytes += res.size
	return nil
}

// becomeWhole turns s into one whole-resource request after the server sent all of it
// for chunk 0: the track restarts from byte 0 when interrupted, as servers that ignore
// ranges require.
func (r *rangedRunner) becomeWhole(s *rtrack, size int64) error {
	// No other chunk of s is in flight (it was not open): what they kept is dropped.
	for i := range s.wrote {
		r.written.Add(-s.wrote[i].Load())
	}
	s.unranged = true
	if err := r.replan(s); err != nil {
		return err
	}
	s.resetBits()
	setBit(s.got, 0)
	s.ngot, s.gotBytes = 1, size
	keep := r.pending[:0]
	r.pendingBytes = 0
	for _, ref := range r.pending {
		if ref.s != s {
			keep = append(keep, ref)
			r.pendingBytes += ref.s.fragLen(ref.i)
		}
	}
	r.pending = append(keep, chunkRef{s, 0})
	r.pendingBytes += size
	r.total = 0
	for _, t := range r.tracks {
		t.base = r.total
		r.total += len(t.plan.Fragments)
	}
	r.dirty = true
	return nil
}

// syncDurable makes written bytes durable (docs/SITE-MEDIA-SPEC.md §2.5). For each
// track it syncs, it first notes how far every unfinished chunk was written, then syncs
// the file, then sets the bits of the chunks written completely before the sync and
// records the noted prefixes of the others. It syncs the tracks with chunks waiting for
// their bits and, when partials is set, those whose prefixes grew since they were last
// recorded. A failed sync records nothing.
func (r *rangedRunner) syncDurable(partials bool) error {
	now := r.o.Clock.Now()
	r.lastSync = now
	if partials {
		r.lastPartial = now
	}
	type noted struct {
		s     *rtrack
		wrote []int64
	}
	var sync []noted
	for _, s := range r.tracks {
		waiting := slices.ContainsFunc(r.pending, func(ref chunkRef) bool { return ref.s == s })
		if !waiting && !(partials && r.grew(s)) {
			continue
		}
		var w []int64
		if s.prefixes() {
			w = make([]int64, len(s.wrote))
			for i := range s.wrote {
				w[i] = s.wrote[i].Load() // before the sync begins
			}
		}
		sync = append(sync, noted{s, w})
	}
	for _, n := range sync {
		if err := r.syncFile(n.s.file); err != nil {
			return diskErr("syncing a track file", err)
		}
	}
	for _, ref := range r.pending {
		if !isBit(ref.s.done, ref.i) {
			setBit(ref.s.done, ref.i)
			ref.s.ndone++
			ref.s.part[ref.i] = 0
			r.dirty = true
		}
	}
	r.pending = r.pending[:0]
	r.pendingBytes = 0
	for _, n := range sync {
		for i, w := range n.wrote {
			// A chunk written completely waits for its result: it gets its bit.
			if isBit(n.s.got, i) || w >= n.s.fragLen(i) || w == n.s.part[i] {
				continue
			}
			n.s.part[i] = w
			r.dirty = true
		}
	}
	return nil
}

// grew reports whether a chunk of s without its bit was written further (or, after a
// 200 dropped its prefix, less far) than its recorded prefix.
func (r *rangedRunner) grew(s *rtrack) bool {
	if !s.prefixes() {
		return false
	}
	for i := range s.wrote {
		if w := s.wrote[i].Load(); !isBit(s.got, i) && w != s.part[i] && w < s.fragLen(i) {
			return true
		}
	}
	return false
}

// syncFile syncs a track file (Options.syncFile in tests).
func (r *rangedRunner) syncFile(f *os.File) error {
	if r.o.syncFile != nil {
		return r.o.syncFile(f)
	}
	return f.Sync()
}

func containsTrack(ts []*rtrack, s *rtrack) bool {
	for _, t := range ts {
		if t == s {
			return true
		}
	}
	return false
}

// checkpoint passes the durable chunks and chunk prefixes to onCheckpoint when they
// changed and, unless force is set, CheckpointInterval has passed since the last one.
func (r *rangedRunner) checkpoint(force bool) error {
	if !r.dirty {
		return nil
	}
	now := r.o.Clock.Now()
	if !force && now.Sub(r.lastCP) < r.o.CheckpointInterval {
		return nil
	}
	if r.onCheckpoint != nil {
		cp := Checkpoint{Tracks: make([]TrackCheckpoint, len(r.tracks)), BytesTotal: r.knownSize()}
		for k, s := range r.tracks {
			tc := TrackCheckpoint{TrackID: s.id, Total: len(s.plan.Fragments), Done: bytes.Clone(s.done), Key: s.key, ChunkBytes: s.chunk,
				Base: s.spanBase, Head: s.headLen}
			for i := range s.plan.Fragments {
				if isBit(s.done, i) {
					cp.DurableBytes += max(s.fragLen(i), 0)
				} else if p := s.part[i]; p > 0 {
					if tc.Partial == nil {
						tc.Partial = make(map[int]int64)
					}
					tc.Partial[i] = p
					cp.DurableBytes += p
				}
			}
			cp.Tracks[k] = tc
		}
		if err := r.onCheckpoint(cp); err != nil {
			return fmt.Errorf("mediajob: saving the checkpoint: %w", err)
		}
	}
	r.dirty = false
	r.lastCP = now
	return nil
}

// emit reports progress: the complete chunks and the bytes written (BytesDone never
// decreases, but when a whole-resource copy fails or a 200 makes a track start over),
// with the size of the tracks once every one is known.
func (r *rangedRunner) emit(force bool) {
	p := Progress{FragmentsTotal: r.total, SpeedBps: r.speed.rate, Connections: int(r.requests.Load()), BytesTotal: r.knownSize(), Clip: r.clipDone}
	var got int64
	for _, s := range r.tracks {
		p.FragmentsDone += s.ngot
		got += s.gotBytes
	}
	p.BytesDone = got + r.written.Load()
	r.rep.emit(p, r.slots, r.o.Clock.Now(), force)
}

// knownSize is the size of the tracks when every one is known, else 0.
func (r *rangedRunner) knownSize() int64 {
	var size int64
	for _, s := range r.tracks {
		if s.size <= 0 {
			return 0
		}
		size += s.size
	}
	return size
}

// assemble checks the tracks that are not checked yet, then joins them into
// OutPath+".dxpart" (or, for OutputCopy, moves the track's file there) and publishes it.
func (r *rangedRunner) assemble(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	for _, s := range r.tracks {
		if n := len(s.plan.Fragments); s.ndone != n {
			return "", fmt.Errorf("mediajob: track %q: %d of %d parts missing", s.id, n-s.ndone, n)
		}
		if !s.checked {
			if err := r.checkInit(s, true); err != nil {
				return "", err
			}
		}
	}
	clip := !r.sel.Clip.IsZero()
	if r.sel.Output == OutputCopy && !clip && !r.mp3(r.tracks[0]) {
		if err := checkTiling(r.tracks[0]); err != nil {
			return "", err
		}
	}
	if clip && r.sel.Output == OutputCopy && r.tracks[0].moov == nil && r.tracks[0].clip != clipFrag {
		moov, err := readTrackMoov(r.tracks[0])
		if err != nil {
			return "", err
		}
		r.tracks[0].moov = moov
	}
	for _, s := range r.tracks {
		err := s.file.Close()
		s.file = nil
		if err != nil {
			return "", diskErr("closing a track file", err)
		}
	}
	part := r.o.OutPath + ".dxpart"
	if err := os.Remove(part); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("mediajob: removing a stale part file: %w", err)
	}
	if clip {
		info, err := r.assembleClip(ctx, part)
		if err != nil {
			_ = os.Remove(part)
			if cerr := ctx.Err(); cerr != nil {
				return "", cerr
			}
			return "", diskErr("cutting the part", err)
		}
		if err := publish(ctx, r.o, part, remux.ContainerMP4, info); err != nil {
			_ = os.Remove(part)
			return "", err
		}
		r.clipDone = info
		return remux.ContainerMP4, nil
	}
	if r.sel.Output == OutputCopy {
		container := "mp4"
		if r.mp3(r.tracks[0]) {
			container = containerMP3
		}
		if err := r.publishCopy(ctx, r.tracks[0], part, container); err != nil {
			return "", err
		}
		return container, nil
	}
	inputs := make([]remux.Input, 0, len(r.tracks))
	for _, s := range r.tracks {
		inputs = append(inputs, remux.Input{Kind: remux.Kind(string(s.kind)), Container: remux.ContainerFMP4, Fragments: []string{s.path}})
	}
	res, err := r.o.Assemble(ctx, inputs, part, remux.Window{})
	container := res.Container
	if err == nil && container != remux.ContainerMP4 {
		err = fmt.Errorf("%w: the assembler wrote %q, not MP4", media.ErrUnsupported, container)
	}
	if err != nil {
		_ = os.Remove(part)
		if cerr := ctx.Err(); cerr != nil {
			return "", cerr
		}
		return "", diskErr("assembling", err)
	}
	if err := publish(ctx, r.o, part, container, nil); err != nil {
		_ = os.Remove(part)
		return "", err
	}
	return container, nil
}

// publishCopy publishes the file of the muxed track s as the output: it records the
// file (renames keep its size and time), renames it to part, then to OutPath
// (placeOutput: never replacing anything but, with ReplaceExisting, a regular file). A
// failed final rename puts the file back.
func (r *rangedRunner) publishCopy(ctx context.Context, s *rtrack, part, container string) error {
	if err := recordOutput(ctx, r.o, s.path, container, nil); err != nil {
		return diskErr("recording the output", err)
	}
	if err := renameRetry(ctx, r.o.Clock, s.path, part); err != nil {
		forgetOutput(r.o)
		return diskErr("moving the output", err)
	}
	if err := placeOutput(ctx, r.o, part); err != nil {
		forgetOutput(r.o)
		_ = os.Rename(part, s.path)
		return diskErr("renaming the output", err)
	}
	syncDir(filepath.Dir(r.o.OutPath))
	return nil
}

// mp3 reports a track copied as an MP3 (an audio page's, docs/SITE-MEDIA-SPEC.md §3.13):
// its head is checked as an MP3's, never as an MP4's.
func (r *rangedRunner) mp3(s *rtrack) bool {
	return r.sel.Output == OutputCopy && s.kind == media.KindAudio && s.ext == containerMP3
}

// checkInit checks the boxes at the start of track s (its first chunk, at most
// maxInitBytes): a complete 'moov' that media.CheckInit accepts and, to be joined, an
// 'ftyp' and an 'mvex' in the 'moov'; to be copied, an 'ftyp' first. A copied file
// whose 'moov' lies beyond the first chunk is checked once it is complete (final).
func (r *rangedRunner) checkInit(s *rtrack, final bool) error {
	n := s.fragLen(0)
	if n < 0 {
		fi, err := s.file.Stat()
		if err != nil {
			return diskErr("reading a track file", err)
		}
		n = fi.Size()
	}
	buf := make([]byte, min(n, maxInitBytes))
	if _, err := s.file.ReadAt(buf, 0); err != nil && !errors.Is(err, io.EOF) {
		return diskErr("reading a track file", err)
	}
	if r.mp3(s) {
		if err := checkMP3Head(buf[:min(len(buf), mp3HeadBytes)]); err != nil {
			return fmt.Errorf("mediajob: track %q: %w", s.id, err)
		}
		s.checked = true
		return nil
	}
	boxes, partial, err := topBoxes(buf, s.size)
	if err != nil {
		return fmt.Errorf("mediajob: track %q: %w", s.id, err)
	}
	var moov *isoBox
	ftyp := false
	for k := range boxes {
		switch boxes[k].typ {
		case "ftyp":
			ftyp = true
		case "moov":
			if moov == nil {
				moov = &boxes[k]
			}
		}
	}
	if r.sel.Output == OutputCopy {
		if len(boxes) == 0 && (partial == nil || partial.typ != "ftyp") || len(boxes) > 0 && boxes[0].typ != "ftyp" {
			return fmt.Errorf("mediajob: track %q: %w: the file is not an MP4 file", s.id, media.ErrUnsupported)
		}
		switch {
		case moov != nil:
			if err := media.CheckInit(buf[:moov.end()]); err != nil {
				return fmt.Errorf("mediajob: track %q: %w", s.id, err)
			}
			if r.audio {
				if err := checkNoVideo(s.id, buf[moov.off:moov.end()]); err != nil {
					return err
				}
			}
		case !final:
			return nil // the 'moov' comes later: checked on the finished file
		default:
			if err := checkMoovAtEnd(s, r.audio); err != nil {
				return err
			}
		}
		s.checked = true
		return nil
	}
	if moov == nil {
		if partial != nil && partial.typ == "moov" {
			return fmt.Errorf("mediajob: track %q: %w: its 'moov' box does not fit in the first part", s.id, media.ErrUnsupported)
		}
		return fmt.Errorf("mediajob: track %q: %w: no 'moov' box at its start (not a fragmented MP4)", s.id, media.ErrUnsupported)
	}
	if err := media.CheckInit(buf[:moov.end()]); err != nil {
		return fmt.Errorf("mediajob: track %q: %w", s.id, err)
	}
	if r.audio {
		if err := checkNoVideo(s.id, buf[moov.off:moov.end()]); err != nil {
			return err
		}
	}
	switch {
	case !ftyp:
		return fmt.Errorf("mediajob: track %q: %w: no 'ftyp' box before the first media", s.id, media.ErrUnsupported)
	case !hasChildBox(buf[moov.off+moov.hdr:moov.end()], "mvex"):
		return fmt.Errorf("mediajob: track %q: %w: a progressive (non-fragmented) MP4 cannot be joined", s.id, media.ErrUnsupported)
	}
	s.checked = true
	return nil
}

// checkMoovAtEnd finds the 'moov' box of the finished file of s among its top-level
// boxes and checks it with media.CheckInit, and with audio that it has no video track.
func checkMoovAtEnd(s *rtrack, audio bool) error {
	var hdr [16]byte
	for off, count := int64(0), 0; off < s.size; count++ {
		if count >= maxTopBoxes {
			return fmt.Errorf("mediajob: track %q: %w: more than %d top-level boxes", s.id, media.ErrUnsupported, maxTopBoxes)
		}
		k, err := s.file.ReadAt(hdr[:min(int64(len(hdr)), s.size-off)], off)
		if err != nil && !errors.Is(err, io.EOF) {
			return diskErr("reading a track file", err)
		}
		b, ok, err := parseBoxHeader(hdr[:k], off, s.size)
		if err != nil || !ok || b.end() > s.size {
			return fmt.Errorf("mediajob: track %q: %w: a top-level box runs past the end of the file", s.id, media.ErrMalformed)
		}
		if b.typ == "moov" {
			if b.size > maxMoovBytes {
				return fmt.Errorf("mediajob: track %q: %w: its 'moov' box is larger than %d bytes", s.id, media.ErrUnsupported, maxMoovBytes)
			}
			moov := make([]byte, b.size)
			if _, err := s.file.ReadAt(moov, b.off); err != nil {
				return diskErr("reading a track file", err)
			}
			if err := media.CheckInit(moov); err != nil {
				return fmt.Errorf("mediajob: track %q: %w", s.id, err)
			}
			if audio {
				return checkNoVideo(s.id, moov)
			}
			return nil
		}
		off = b.end()
	}
	return fmt.Errorf("mediajob: track %q: %w: the file has no 'moov' box", s.id, media.ErrUnsupported)
}

// checkTiling walks the top-level boxes of the finished file of the copied track s and
// requires them to fill it exactly: a box that runs past the end means the file is cut
// (a site that declared a size smaller than the resource), media.ErrMalformed.
func checkTiling(s *rtrack) error {
	var hdr [16]byte
	off := int64(0)
	for count := 0; off < s.size; count++ {
		if count >= maxTopBoxes {
			return fmt.Errorf("mediajob: track %q: %w: more than %d top-level boxes", s.id, media.ErrUnsupported, maxTopBoxes)
		}
		k, err := s.file.ReadAt(hdr[:min(int64(len(hdr)), s.size-off)], off)
		if err != nil && !errors.Is(err, io.EOF) {
			return diskErr("reading a track file", err)
		}
		b, ok, err := parseBoxHeader(hdr[:k], off, s.size)
		if err != nil || !ok || b.end() > s.size {
			return fmt.Errorf("mediajob: track %q: %w: a top-level box runs past the end of the file (the file is cut)", s.id, media.ErrMalformed)
		}
		off = b.end()
	}
	return nil
}

// isoBox is a top-level ISO-BMFF box (ISO/IEC 14496-12 §4.2): its type, offset, header
// length and total size.
type isoBox struct {
	typ       string
	off       int64
	hdr, size int64
}

func (b isoBox) end() int64 { return b.off + b.size }

// parseBoxHeader reads the box header at the start of p, the box at offset off of a
// file of fileSize bytes (-1 unknown). ok is false when p is too short for the header,
// or the box extends to the end of a file of unknown size.
func parseBoxHeader(p []byte, off, fileSize int64) (isoBox, bool, error) {
	if len(p) < 8 {
		return isoBox{}, false, nil
	}
	b := isoBox{typ: string(p[4:8]), off: off, hdr: 8, size: int64(binary.BigEndian.Uint32(p))}
	switch b.size {
	case 1:
		if len(p) < 16 {
			return isoBox{}, false, nil
		}
		large := binary.BigEndian.Uint64(p[8:16])
		if large > media.MaxRangedSize {
			return isoBox{}, false, fmt.Errorf("%w: box %q of %d bytes", media.ErrMalformed, b.typ, large)
		}
		b.hdr, b.size = 16, int64(large)
	case 0: // to the end of the file
		if fileSize < 0 {
			return b, false, nil
		}
		b.size = fileSize - off
	}
	if b.size < b.hdr {
		return isoBox{}, false, fmt.Errorf("%w: box %q has a size of %d bytes", media.ErrMalformed, b.typ, b.size)
	}
	return b, true, nil
}

// topBoxes lists the complete top-level boxes at the start of data, a prefix of a file
// of fileSize bytes (-1 unknown), and returns the first box that does not fit in data
// as partial (nil when even its header is cut).
func topBoxes(data []byte, fileSize int64) (boxes []isoBox, partial *isoBox, err error) {
	for off := int64(0); off < int64(len(data)); {
		if len(boxes) >= maxTopBoxes {
			return nil, nil, fmt.Errorf("%w: more than %d top-level boxes", media.ErrMalformed, maxTopBoxes)
		}
		b, ok, err := parseBoxHeader(data[off:], off, fileSize)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			if len(data)-int(off) >= 8 {
				return boxes, &b, nil
			}
			return boxes, nil, nil
		}
		if b.end() > int64(len(data)) {
			return boxes, &b, nil
		}
		boxes = append(boxes, b)
		off = b.end()
	}
	return boxes, nil, nil
}

// hasChildBox reports whether the box payload p holds a child box of type typ.
func hasChildBox(p []byte, typ string) bool {
	for off := int64(0); off+8 <= int64(len(p)); {
		b, ok, err := parseBoxHeader(p[off:], off, int64(len(p)))
		if err != nil || !ok || b.end() > int64(len(p)) {
			return false
		}
		if b.typ == typ {
			return true
		}
		off = b.end()
	}
	return false
}

// diskErr wraps a file error, tagging it with ErrDiskFull when the volume is full.
func diskErr(op string, err error) error {
	if isDiskFull(err) {
		return fmt.Errorf("mediajob: %s: %w: %w", op, ErrDiskFull, err)
	}
	return fmt.Errorf("mediajob: %s: %w", op, err)
}

// isDiskFull maps ENOSPC and ERROR_DISK_FULL / ERROR_HANDLE_DISK_FULL.
func isDiskFull(err error) bool {
	if errors.Is(err, syscall.ENOSPC) {
		return true
	}
	var errno syscall.Errno
	return runtime.GOOS == "windows" && errors.As(err, &errno) &&
		(errno == winErrorDiskFull || errno == winErrorHandleDiskFull)
}
