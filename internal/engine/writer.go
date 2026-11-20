package engine

import (
	"slices"
	"sync"
	"time"
)

// bufPool recycles read slices between workers and the writer.
type bufPool struct{ p sync.Pool }

func newBufPool() *bufPool {
	return &bufPool{p: sync.Pool{New: func() any {
		b := make([]byte, sliceUncapped)
		return &b
	}}}
}

func (b *bufPool) get() *[]byte  { return b.p.Get().(*[]byte) }
func (b *bufPool) put(p *[]byte) { b.p.Put(p) }

// writeReq hands reserved bytes of a segment to the writer.
type writeReq struct {
	seg *segment
	off int64
	buf []byte  // the data, a prefix of *ref
	ref *[]byte // pooled buffer, returned after the write
}

// flushResult reports, after a successful flush, the end of each segment's contiguous
// written prefix. Every byte in [seg.base, frontier) is on stable storage.
type flushResult struct {
	frontiers map[*segment]int64
	err       error
}

// writer is the single goroutine that writes the part file (docs/ENGINE-SPEC.md §6).
// It drains in until the channel is closed, even after a write error, so workers
// never block forever on a send.
type writer struct {
	file     partFile
	clock    Clock
	interval time.Duration
	limit    int64
	bufs     *bufPool

	in      chan writeReq
	flushed chan flushResult // periodic flushes, read by the coordinator
	failed  chan error       // the first write or flush error
	done    chan flushResult // the final flush after in is closed

	front map[*segment]*frontier

	// Progress.Head (docs/ENGINE-SPEC.md §9): once every byte of [0, headNeed) is
	// written, the writer reads them back and hands them over on head (once per run).
	// headNeed 0 disables it; headSent is owned by the writer goroutine once it runs.
	headNeed  int64
	headFront frontier
	headSent  bool
	head      chan []byte
}

func newWriter(f partFile, clock Clock, interval time.Duration, limit int64, bufs *bufPool) *writer {
	return &writer{
		file: f, clock: clock, interval: interval, limit: limit, bufs: bufs,
		in:      make(chan writeReq, writeQueueLen),
		flushed: make(chan flushResult, 1),
		failed:  make(chan error, 1),
		done:    make(chan flushResult, 1),
		front:   make(map[*segment]*frontier),
		head:    make(chan []byte, 1),
	}
}

// noteHead counts the written span [off, off+n) towards the head and sends the head
// once all of it is written. Called by the writer goroutine only.
func (w *writer) noteHead(off, n int64) {
	if w.headSent || off >= w.headNeed {
		return
	}
	w.headFront.add(off, min(n, w.headNeed-off))
	if w.headFront.next < w.headNeed {
		return
	}
	w.headSent = true
	buf := make([]byte, w.headNeed)
	if _, err := w.file.ReadAt(buf, 0); err != nil {
		return // advisory: no head this run
	}
	select {
	case w.head <- buf:
	default:
	}
}

func (w *writer) run() {
	timer := w.clock.NewTimer(w.interval)
	defer timer.Stop()
	var unflushed int64
	var werr error
	flush := func() {
		res := w.flush()
		unflushed = 0
		if res.err != nil {
			werr = res.err
			w.failed <- werr
			return
		}
		w.flushed <- res
	}
	for {
		select {
		case req, ok := <-w.in:
			if !ok {
				if werr != nil {
					w.done <- flushResult{err: werr}
				} else {
					w.done <- w.flush()
				}
				return
			}
			if werr == nil {
				if _, err := w.file.WriteAt(req.buf, req.off); err != nil {
					werr = diskError("write part file", err)
					w.failed <- werr
				} else {
					w.track(req.seg, req.off, int64(len(req.buf)))
					w.noteHead(req.off, int64(len(req.buf)))
					unflushed += int64(len(req.buf))
				}
			}
			w.bufs.put(req.ref)
			if werr == nil && unflushed >= w.limit {
				flush()
				timer.Reset(w.interval)
			}
		case <-timer.C():
			if werr == nil && unflushed > 0 {
				flush()
			}
			timer.Reset(w.interval)
		}
	}
}

func (w *writer) track(s *segment, off, n int64) {
	f := w.front[s]
	if f == nil {
		f = &frontier{next: s.base}
		w.front[s] = f
	}
	f.add(off, n)
}

// flush syncs the file and snapshots the frontiers. The writer is the only goroutine
// that writes, so the snapshot exactly matches what the sync made durable.
func (w *writer) flush() flushResult {
	if err := w.file.Sync(); err != nil {
		return flushResult{err: diskError("flush part file", err)}
	}
	m := make(map[*segment]int64, len(w.front))
	for s, f := range w.front {
		m[s] = f.next
	}
	return flushResult{frontiers: m}
}

// span is a half-open byte interval [lo, hi).
type span struct{ lo, hi int64 }

// frontier tracks the contiguous written prefix of one segment. Writes of a segment
// normally arrive in order; out-of-order spans are held until the gap is filled.
type frontier struct {
	next    int64  // every byte in [base, next) is written
	pending []span // written spans beyond next, sorted, disjoint and non-adjacent
}

func (f *frontier) add(off, n int64) {
	lo, hi := off, off+n
	if n <= 0 || hi <= f.next {
		return
	}
	if lo <= f.next {
		f.next = hi
	} else {
		i, _ := slices.BinarySearchFunc(f.pending, lo, func(s span, lo int64) int {
			switch {
			case s.lo < lo:
				return -1
			case s.lo > lo:
				return 1
			}
			return 0
		})
		f.pending = slices.Insert(f.pending, i, span{lo, hi})
		merged := f.pending[:0]
		for _, s := range f.pending {
			if k := len(merged); k > 0 && s.lo <= merged[k-1].hi {
				merged[k-1].hi = max(merged[k-1].hi, s.hi)
				continue
			}
			merged = append(merged, s)
		}
		f.pending = merged
	}
	for len(f.pending) > 0 && f.pending[0].lo <= f.next {
		f.next = max(f.next, f.pending[0].hi)
		f.pending = f.pending[1:]
	}
}
