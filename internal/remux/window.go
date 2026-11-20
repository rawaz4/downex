package remux

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// Parts of a video (clips, docs/MEDIA-SPEC.md §8 C6–C8). A Window cuts the assembled
// tracks to [Start, End) without re-encoding: video decoding starts at the sync sample
// at or before Start (its presentation time is the keyframe time K), audio is kept from
// K too, so that a player that ignores edit lists stays in sync, and an edit list
// (ISO/IEC 14496-12 §8.6.6) on every track makes the visible start exactly
// max(Start, K) and the length End' − max(Start, K). Video keeps the samples whose
// decode time is before End (no kept frame references a dropped one), audio those whose
// presentation time is.

// WindowOrigin says what the times of a Window are measured from.
type WindowOrigin int

// Origins.
const (
	// OriginFirst: from the start of the first input (its tracks' earliest time on the
	// output timeline: the first decode time of fragmented MP4, the earliest
	// presentation time of a transport stream). Used when the first input begins with
	// the first kept fragment of a stream whose plan time is known (docs/MEDIA-SPEC.md
	// §8 C3); a later input (a separate audio track) may start before it.
	OriginFirst WindowOrigin = iota
	// OriginMedia: on the inputs' own media timeline (fragmented MP4 decode times, or
	// a progressive file's presentation after its edit list): the presentation that
	// starts at media time 0. Transport stream inputs have no such timeline and use
	// OriginFirst.
	OriginMedia
)

// Window is a part of the assembled presentation.
type Window struct {
	Start, End time.Duration // End 0 = to the end
	Origin     WindowOrigin
	// Late allows a start after Start when the video input has no sync sample at or
	// before it (C6); otherwise that is ErrNoKeyframe, so that the caller can fetch
	// earlier media first (C5).
	Late bool
}

// IsZero reports the whole presentation.
func (w Window) IsZero() bool { return w.Start <= 0 && w.End <= 0 }

// Result describes an assembled file.
type Result struct {
	// Container is "mp4".
	Container string
	// Keyframe is the presentation time, in the window's origin, of the first decoded
	// video frame (the clip's start for audio only); 0 without a window.
	Keyframe time.Duration
	// Exact: the visible start is the window's Start (false: Keyframe is after it).
	Exact bool
}

// ErrNoKeyframe reports a video input without a sync sample at or before the start of
// a Window that does not allow a Late start.
var ErrNoKeyframe = errors.New("remux: no keyframe at or before the start of the part")

// errNoPicture is the kind of a transport stream video input without a decodable
// picture (ErrUnsupported); in a window that may still start earlier it is ErrNoKeyframe.
var errNoPicture = fmt.Errorf("%w", ErrUnsupported)

// errClipTooLong refuses a part whose samples exceed the in-memory tables (C15).
var errClipTooLong = errf(ErrUnsupported, "the part is too long to cut")

// firstInputOffset is the offset of an OriginFirst window for applyWindow: minus the
// output time at which the first input's tracks start.
func (m *muxer) firstInputOffset() time.Duration {
	start := uint64(math.MaxUint64)
	for _, t := range m.tracks {
		if t.input == 0 {
			start = min(start, t.empty)
		}
	}
	if start == math.MaxUint64 {
		return 0
	}
	return -movieDuration(start)
}

// windowTicks converts d to ticks of timescale ts, clamped at 0.
func windowTicks(d time.Duration, ts uint32) int64 {
	if d <= 0 {
		return 0
	}
	return int64(scaleTime(uint64(d), ts, 1_000_000_000))
}

// movieDuration converts v units of the output movie timescale to a duration.
func movieDuration(v uint64) time.Duration {
	return time.Duration(min(v, uint64(math.MaxInt64/int64(time.Second/movieTimescale)))) * (time.Second / movieTimescale)
}

func ticksDuration(v int64, ts uint32) time.Duration {
	if v <= 0 {
		return 0
	}
	return time.Duration(scaleTime(uint64(v), 1_000_000_000, ts))
}

// samplePres returns the presentation times (track ticks, on the movie timeline) and
// decode times (same timeline) of the samples of t.
func (t *track) samplePres() (pres, dec []int64) {
	ts := t.src.timescale
	empty := int64(scaleTime(t.empty, ts, movieTimescale))
	n := len(t.sizes)
	pres, dec = make([]int64, n), make([]int64, n)
	var d int64
	for i := range n {
		dec[i] = empty + d - t.mediaTime
		pres[i] = dec[i] + int64(t.ctos[i])
		d += int64(t.durs[i])
	}
	return pres, dec
}

// isSync reports whether sample i (0-based) of t is a sync sample.
func (t *track) isSync(i int, syncs map[uint32]bool) bool {
	if !t.nonSync {
		return true
	}
	return syncs[uint32(i)+1]
}

// applyWindow cuts the progressive tables of every track of m to w (C6–C8): offset is
// the time of output presentation 0 in w's origin. The tracks' edit lists then show
// [max(Start, K), End'). It returns the keyframe time in w's origin.
func (m *muxer) applyWindow(w Window, offset time.Duration) (Result, error) {
	res := Result{Container: ContainerMP4, Exact: true}
	if !m.progressive {
		return res, errClipTooLong
	}
	start := w.Start - offset
	end := time.Duration(0)
	if w.End > 0 {
		end = max(w.End-offset, 1)
	}
	type cut struct{ lo, hi int }
	cuts := make([]cut, len(m.tracks))
	pres := make([][]int64, len(m.tracks))
	decs := make([][]int64, len(m.tracks))
	for i, t := range m.tracks {
		pres[i], decs[i] = t.samplePres()
	}
	vt := -1
	for i, t := range m.tracks {
		if t.src.handler == "vide" {
			vt = i
			break
		}
	}
	k := start // the keyframe time on the output timeline
	if vt >= 0 {
		t := m.tracks[vt]
		ts := t.src.timescale
		syncs := make(map[uint32]bool, len(t.syncs))
		for _, s := range t.syncs {
			syncs[s] = true
		}
		st := windowTicks(start, ts)
		a, after := -1, -1
		for i := range pres[vt] {
			if !t.isSync(i, syncs) {
				continue
			}
			p := pres[vt][i]
			if p <= st && (a < 0 || p >= pres[vt][a]) {
				a = i
			}
			if p > st && (after < 0 || p < pres[vt][after]) {
				after = i
			}
		}
		if a < 0 {
			if !w.Late || after < 0 {
				if after < 0 && w.Late {
					return res, errf(ErrUnsupported, "the video has no keyframe")
				}
				return res, ErrNoKeyframe
			}
			a = after
			res.Exact = false
		}
		k = ticksDuration(pres[vt][a], ts)
		if end > 0 && k >= end {
			// A late start at or after the part's end would show nothing.
			return res, errf(ErrUnsupported, "the video has no keyframe before the end of the part")
		}
		hi := len(pres[vt])
		if end > 0 {
			et := windowTicks(end, ts)
			for j := a + 1; j < len(decs[vt]); j++ {
				if decs[vt][j] >= et {
					hi = j
					break
				}
			}
		}
		cuts[vt] = cut{a, max(hi, a+1)}
	}
	for i, t := range m.tracks {
		if i == vt {
			continue
		}
		ts := t.src.timescale
		kt := windowTicks(k, ts)
		n := len(pres[i])
		lo := n - 1
		for j := range n {
			if pres[i][j]+int64(t.durs[j]) > kt {
				lo = j
				break
			}
		}
		hi := n
		if end > 0 {
			et := windowTicks(end, ts)
			for j := lo + 1; j < n; j++ {
				if pres[i][j] >= et {
					hi = j
					break
				}
			}
		}
		cuts[i] = cut{lo, max(hi, lo+1)}
	}
	var kept uint64
	for _, c := range cuts {
		kept += uint64(c.hi - c.lo)
	}
	if kept > maxProgressiveSamples {
		return res, errClipTooLong
	}
	visible := max(start, k)
	for i, t := range m.tracks {
		if err := t.cut(cuts[i].lo, cuts[i].hi, pres[i], visible, end); err != nil {
			return res, err
		}
	}
	res.Keyframe = k + offset
	return res, nil
}

// cut keeps samples [lo, hi) of t's progressive tables and sets its edit to show the
// presentation from visible to end (0 = the end of its kept samples), both on the
// output timeline; pres are the samples' presentation times (samplePres).
func (t *track) cut(lo, hi int, pres []int64, visible, end time.Duration) error {
	ts := t.src.timescale
	var d0 uint64 // old decode time of sample lo, from the track's first
	for i := range lo {
		d0 += uint64(t.durs[i])
	}
	// The chunks of the kept samples.
	var chunks []chunk
	var payload int64
	s0 := 0
	for _, c := range t.chunks {
		c0, c1 := s0, s0+int(c.n)
		s0 = c1
		if c1 <= lo || c0 >= hi {
			continue
		}
		nc := c
		nc.first = c.first + uint64(max(lo-c0, 0))
		for j := c0; j < max(lo, c0); j++ {
			nc.off += int64(t.sizes[j])
		}
		nc.n, nc.size = 0, 0
		for j := max(lo, c0); j < min(hi, c1); j++ {
			nc.n++
			nc.size += int64(t.sizes[j])
		}
		chunks = append(chunks, nc)
		payload += nc.size
	}
	var syncs []uint32
	for _, s := range t.syncs {
		if int(s) > lo && int(s) <= hi {
			syncs = append(syncs, s-uint32(lo))
		}
	}
	t.lo += uint64(lo)
	t.sizes = append([]uint32(nil), t.sizes[lo:hi]...)
	t.durs = append([]uint32(nil), t.durs[lo:hi]...)
	t.ctos = append([]int32(nil), t.ctos[lo:hi]...)
	keptPres := pres[lo:hi]
	t.syncs = syncs
	t.nonSync = t.nonSync && len(syncs) < len(t.sizes)
	t.chunks, t.payload = chunks, payload
	var at uint64
	j := 0
	for i := range t.chunks {
		c := &t.chunks[i]
		c.time = at
		for range c.n {
			at += uint64(t.durs[j])
			j++
		}
	}
	t.next = at
	t.nSamples = uint64(len(t.sizes))
	t.minCTO, t.anyCTO = 0, false
	for _, c := range t.ctos {
		t.minCTO = min(t.minCTO, c)
		t.anyCTO = t.anyCTO || c != 0
	}
	t.shift = int64(max(-t.minCTO, 0))

	pmin := int64(math.MaxInt64)
	for _, p := range keptPres {
		pmin = min(pmin, p)
	}
	oldEmpty := int64(scaleTime(t.empty, ts, movieTimescale))
	// The kept samples end where the last one's decode time ends, on the presentation
	// timeline (as an uncut track's edit does, finish).
	pend := oldEmpty + int64(d0+at) - t.mediaTime
	vt := windowTicks(visible, ts)
	from := max(vt, pmin)
	stop := pend
	if end > 0 {
		stop = min(stop, windowTicks(end, ts))
	}
	stop = max(stop, from)
	t.empty = 0
	if pmin > vt {
		t.empty = scaleTime(uint64(pmin-vt), movieTimescale, ts)
	}
	// The media time of from in the cut track: its composition time on the old track's
	// timeline, less the decode time of the first kept sample.
	mt := from - oldEmpty + t.mediaTime - int64(d0)
	if mt < -t.shift {
		mt = -t.shift
	}
	t.mediaTime = mt
	t.presDur = scaleTime(uint64(stop-from), movieTimescale, ts)
	t.tkhdDur = satAdd(t.empty, t.presDur)
	t.offset = scaleTime(t.empty, ts, movieTimescale)
	t.edit = true
	if len(t.sizes) == 0 {
		return errf(ErrMalformed, "track %d has no samples in the part", t.outID)
	}
	return nil
}
