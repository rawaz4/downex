package store

import (
	"bytes"
	"cmp"
	"fmt"
	"math"
	"slices"
	"time"

	"downex/internal/model"
)

// Timestamps are persisted as Unix nanoseconds, which cover roughly the years
// 1678–2262. Both implementations reject times outside that range so that the
// in-memory store behaves exactly like the SQLite one.
var (
	minStorableTime = time.Unix(0, math.MinInt64)
	maxStorableTime = time.Unix(0, math.MaxInt64)
)

// validateDownload rejects records that cannot be stored without loss.
func validateDownload(d *model.Download) error {
	if d == nil {
		return fmt.Errorf("%w: nil download", ErrInvalid)
	}
	for _, f := range []struct {
		name string
		t    time.Time
	}{
		{"createdAt", d.CreatedAt},
		{"updatedAt", d.UpdatedAt},
		{"completedAt", d.CompletedAt},
		{"startedAt", d.StartedAt},
	} {
		if err := checkStorableTime(f.name, f.t); err != nil {
			return err
		}
	}
	if d.Media != nil {
		for i, t := range d.Media.Tracks {
			// JSON, the media column's encoding, has no NaN or infinities.
			if math.IsNaN(t.FrameRate) || math.IsInf(t.FrameRate, 0) {
				return fmt.Errorf("%w: media track %d has a frame rate that is not a finite number", ErrInvalid, i+1)
			}
		}
	}
	return nil
}

// validateQueue rejects queues that cannot be stored without loss.
func validateQueue(q *model.Queue) error {
	if q == nil {
		return fmt.Errorf("%w: nil queue", ErrInvalid)
	}
	return checkStorableTime("scheduleMark", q.ScheduleMark)
}

// checkStorableTime reports whether t survives the Unix-nanosecond encoding.
// The zero time is always storable (it is persisted as "absent").
func checkStorableTime(field string, t time.Time) error {
	if t.IsZero() {
		return nil
	}
	if t.Before(minStorableTime) || t.After(maxStorableTime) {
		return fmt.Errorf("%w: %s %s is outside the storable range", ErrInvalid, field, t.Format(time.RFC3339))
	}
	return nil
}

// validateSegments rejects checkpoints that do not identify each segment uniquely.
func validateSegments(segs []model.Segment) error {
	seen := make(map[int]struct{}, len(segs))
	for _, s := range segs {
		if _, dup := seen[s.ID]; dup {
			return fmt.Errorf("%w: duplicate segment id %d in checkpoint", ErrInvalid, s.ID)
		}
		seen[s.ID] = struct{}{}
	}
	return nil
}

// canonicalTime returns t as the SQLite store reads it back: UTC, without a
// monotonic clock reading, and the zero time for any zero instant.
func canonicalTime(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	return t.UTC().Round(0)
}

// stampNew fills CreatedAt and UpdatedAt with now when they are zero.
func stampNew(d *model.Download, now time.Time) {
	if d.CreatedAt.IsZero() {
		d.CreatedAt = now
	}
	if d.UpdatedAt.IsZero() {
		d.UpdatedAt = now
	}
}

// cloneDownload returns a deep copy of d. Nil and empty headers, header values,
// bodies, media tracks and checkpoint bitmaps are preserved as they are.
func cloneDownload(d model.Download) model.Download {
	d.Request.Headers = d.Request.Headers.Clone()
	d.Request.Body = bytes.Clone(d.Request.Body)
	d.Media = d.Media.Clone()
	return d
}

// sortedSegments returns a copy of segs ordered by segment ID, the order in
// which both stores return a checkpoint. The result is never nil.
func sortedSegments(segs []model.Segment) []model.Segment {
	out := make([]model.Segment, len(segs))
	copy(out, segs)
	slices.SortFunc(out, func(a, b model.Segment) int { return cmp.Compare(a.ID, b.ID) })
	return out
}
