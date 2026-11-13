package engine

import "sync"

// segment is one live byte range of a download. start and base are immutable; end,
// pos, eof and owner are guarded by mu so that a worker's reservation of bytes and
// the coordinator's shrinking of end (the compare-and-swap of docs/ENGINE-SPEC.md §4)
// are atomic with respect to each other.
type segment struct {
	start int64
	base  int64 // start + durable when this segment was created in this session

	mu    sync.Mutex
	end   int64 // inclusive; -1 = open-ended (single mode with unknown size)
	pos   int64 // next byte to reserve; bytes in [start, pos) are reserved
	eof   bool  // an open-ended segment whose response ended at pos
	owner *worker

	durable int64 // bytes from start that are flushed; owned by the coordinator
}

func newSegment(start, end, durable int64, owner *worker) *segment {
	return &segment{start: start, base: start + durable, end: end, pos: start + durable, durable: durable, owner: owner}
}

func (s *segment) doneLocked() bool { return s.eof || (s.end >= 0 && s.pos > s.end) }

// snapshot returns a consistent view of the mutable fields.
func (s *segment) snapshot() (end, pos int64, owner *worker, done bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.end, s.pos, s.owner, s.doneLocked()
}

func (s *segment) done() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.doneLocked()
}

// remaining is the number of bytes not yet reserved (0 for open-ended segments).
func (s *segment) remaining() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.end < 0 {
		return 0
	}
	return max(s.end-s.pos+1, 0)
}

// claim reserves up to n bytes at offset off for w and returns how many it reserved.
// It reserves nothing when w no longer owns the segment, when off is not the current
// position, or when the segment's (possibly just shrunk) end has been reached.
func (s *segment) claim(w *worker, off int64, n int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner != w || off != s.pos || s.doneLocked() {
		return 0
	}
	k := int64(n)
	if s.end >= 0 {
		k = min(k, s.end-s.pos+1)
	}
	if k <= 0 {
		return 0
	}
	s.pos += k
	return int(k)
}

// shrink moves end to newStart−1 and returns the old end. It refuses (the split is
// cancelled) when the write position already reached newStart.
func (s *segment) shrink(newStart int64) (oldEnd int64, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.end < 0 || newStart > s.end || s.pos >= newStart {
		return 0, false
	}
	oldEnd = s.end
	s.end = newStart - 1
	return oldEnd, true
}

// acquire makes w the owner of an unowned, unfinished segment.
func (s *segment) acquire(w *worker) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner != nil || s.doneLocked() {
		return false
	}
	s.owner = w
	return true
}

// release clears the owner if it is w.
func (s *segment) release(w *worker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner == w {
		s.owner = nil
	}
}

// takeover hands the segment to w when it starts at offset at and nothing of it has
// been reserved yet. It returns the previous owner, which must be cancelled.
func (s *segment) takeover(w *worker, at int64) (prev *worker, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.start != at || s.pos != at || s.doneLocked() {
		return nil, false
	}
	prev, s.owner = s.owner, w
	return prev, true
}

// detach takes the segment away from a stalled owner w. When w already reserved bytes,
// the segment is cut at its position and the unreserved tail [newStart, oldEnd] is
// returned for a new segment; otherwise the whole segment simply becomes unowned.
func (s *segment) detach(w *worker) (newStart, oldEnd int64, split bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner != w {
		return 0, 0, false
	}
	s.owner = nil
	if s.pos == s.start || s.end < 0 || s.pos > s.end {
		return 0, 0, false
	}
	newStart, oldEnd = s.pos, s.end
	s.end = s.pos - 1
	return newStart, oldEnd, true
}

// finishOpen marks an open-ended segment owned by w as ending just before off.
func (s *segment) finishOpen(w *worker, off int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner == w && s.end < 0 && off == s.pos {
		s.eof = true
	}
}
