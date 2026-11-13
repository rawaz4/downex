package engine

import (
	"cmp"
	"fmt"
	"slices"

	"downex/internal/model"
)

// compactSegments drops empty segments, merges each fully durable segment into its
// adjacent successor (the merged durable prefix stays contiguous) and renumbers IDs.
func compactSegments(in []model.Segment) []model.Segment {
	out := make([]model.Segment, 0, len(in))
	for _, s := range in {
		if s.End >= 0 && s.End < s.Start {
			continue
		}
		if k := len(out); k > 0 {
			if p := out[k-1]; p.Done() && s.Start == p.End+1 {
				out[k-1] = model.Segment{Start: p.Start, End: s.End, Durable: p.End - p.Start + 1 + s.Durable}
				continue
			}
		}
		out = append(out, s)
	}
	for i := range out {
		out[i].ID = i
	}
	return out
}

// loadLayout sorts persisted segments and checks that they tile [0, size−1] with
// durable offsets inside their ranges (docs/ENGINE-SPEC.md §1).
func loadLayout(segs []model.Segment, size int64) ([]model.Segment, error) {
	segs = slices.Clone(segs)
	slices.SortFunc(segs, func(a, b model.Segment) int { return cmp.Compare(a.Start, b.Start) })
	if size < 0 {
		return nil, fmt.Errorf("%w: ranged download without a size", ErrCorruptCheckpoint)
	}
	var next int64
	for _, s := range segs {
		if s.Start != next || s.End < s.Start || s.End >= size || s.Durable < 0 || s.Durable > s.End-s.Start+1 {
			return nil, fmt.Errorf("%w: segment %d-%d (durable %d) in a file of %d bytes",
				ErrCorruptCheckpoint, s.Start, s.End, s.Durable, size)
		}
		next = s.End + 1
	}
	if next != size {
		return nil, fmt.Errorf("%w: segments cover %d of %d bytes", ErrCorruptCheckpoint, next, size)
	}
	return segs, nil
}
