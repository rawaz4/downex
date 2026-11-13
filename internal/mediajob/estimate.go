package mediajob

import (
	"math"
	"time"
)

// estimator derives Progress.BytesTotal of a Run from the bytes of its durable fragments
// (docs/VIDEO-DETECT-SPEC.md, bug C of 0.7.0: a stream's size is shown while it
// downloads). Per track the total is the bytes of its durable fragments plus, for each
// fragment still missing, the length of its byte range when it has one, else the
// track's bytes per second of media so far times the fragment's duration (the track's
// average media fragment when durations are unknown; the average initialization segment
// for an initialization segment). It is unknown (0) while a track that needs an estimate
// has no durable media fragment yet. Even a total that needed no estimate (every
// missing fragment has a byte range, or none is missing) is the fragments' bytes, not
// the size of the file, which Assemble writes anew (an MP4; a legacy .ts leaves out
// what precedes each fragment's first packet): it is always BytesTotalApprox.
//
// It is owned by the goroutine that called Run, like the runner's other fields.
type estimator struct {
	tracks map[*trackState]*trackBytes
	// The last result, valid while the job's durable fragment count is doneN.
	cached bool
	doneN  int
	total  int64
}

// trackBytes sums the durable fragments of one track.
type trackBytes struct {
	initBytes  int64
	initN      int
	mediaBytes int64
	mediaN     int
	// timedBytes and timed cover the durable media fragments with a duration.
	timedBytes int64
	timed      time.Duration
}

// add records that fragment i of s is durable with size bytes.
func (e *estimator) add(s *trackState, i int, size int64) {
	if e.tracks == nil {
		e.tracks = make(map[*trackState]*trackBytes)
	}
	b := e.tracks[s]
	if b == nil {
		b = &trackBytes{}
		e.tracks[s] = b
	}
	e.cached = false
	f := &s.plan.Fragments[i]
	switch {
	case f.Init:
		b.initBytes += size
		b.initN++
	default:
		b.mediaBytes += size
		b.mediaN++
		if f.Duration > 0 {
			b.timedBytes += size
			b.timed += f.Duration
		}
	}
}

// estimate returns BytesTotal and BytesTotalApprox for tracks, of which doneN fragments
// are durable.
func (e *estimator) estimate(tracks []*trackState, doneN int) (int64, bool) {
	if !e.cached || e.doneN != doneN {
		var total float64
		for _, s := range tracks {
			n, ok := e.track(s)
			if !ok {
				total = 0
				break
			}
			total += n
		}
		e.cached, e.doneN, e.total = true, doneN, 0
		if total > 0 && total < math.MaxInt64/2 {
			e.total = int64(math.Round(total))
		}
	}
	return e.total, e.total > 0
}

// track estimates the bytes of s; ok is false while that cannot be estimated yet.
func (e *estimator) track(s *trackState) (bytes float64, ok bool) {
	b := e.tracks[s]
	if b == nil {
		b = &trackBytes{}
	}
	bytes = float64(b.initBytes + b.mediaBytes)
	for i := range s.plan.Fragments {
		if s.isDone(i) {
			continue
		}
		f := &s.plan.Fragments[i]
		switch {
		case f.ByteRange != nil:
			bytes += float64(f.ByteRange[1] - f.ByteRange[0] + 1)
		case f.Init:
			if b.initN > 0 {
				bytes += float64(b.initBytes) / float64(b.initN)
			}
		case f.Duration > 0 && b.timed > 0:
			bytes += float64(b.timedBytes) * (f.Duration.Seconds() / b.timed.Seconds())
		case b.mediaN > 0:
			bytes += float64(b.mediaBytes) / float64(b.mediaN)
		default:
			return 0, false // no durable media fragment to estimate from
		}
	}
	return bytes, true
}
