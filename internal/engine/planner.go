package engine

import (
	"mime"
	"path"
	"strings"
	"time"
)

// Split-rule constants, classic profile (docs/ENGINE-SPEC.md §4).
const (
	minChunkBase     = 11_680  // minimum chunk at normal speeds
	minChunkFast     = 116_800 // minimum chunk once the download is faster than fastThresholdBps
	fastThresholdBps = 116_800 // bytes per second
	splitFloor       = 23_363  // a victim with less remaining is never split unless forced
	tailFirstChunk   = 23_360  // tail-first: newStart = end − tailFirstChunk ...
	tailFirstSmall   = 11_680  // ... or end − tailFirstSmall when remaining < tailFirstLimit
	tailFirstLimit   = 46_721
	earlySplitPad    = 11_680 // early first split: start + earlySplitFactor·(received + earlySplitPad)
	earlySplitFactor = 10
)

// segView is a planner's snapshot of one segment. Ranges are inclusive.
type segView struct {
	Start int64
	End   int64 // inclusive; negative (open-ended) segments are never split
	Pos   int64 // next byte to fetch: Start + bytes received
	// Idle is how long the owning worker has been idle; it is non-zero only for
	// segments whose worker counts as slow, which makes them forced-split candidates.
	Idle time.Duration
	// ETA is how long the owning worker needs for the rest at its own speed; it is
	// non-zero only for an owner that is slow but still receiving (§5), which makes
	// the segment a forced-split candidate when no owner is idle.
	ETA time.Duration
}

// End-game stealing from slow but still-receiving owners (docs/ENGINE-SPEC.md §5).
const (
	slowSpeedRatio    = 4                // slow: below a quarter of the average live connection
	slowMinETA        = time.Second      // ... with more than 1 s left at its own speed
	slowMinAge        = time.Second      // ... after receiving for at least 1 s
	stealMinRemaining = 4 * minChunkBase // 46,720: the new half gets at least 23,360 bytes
)

// remaining is seg.end − (seg.start + seg.received) + 1.
func (s segView) remaining() int64 {
	if s.End < 0 {
		return 0
	}
	return max(s.End-s.Pos+1, 0)
}

// minChunk is 11,680 bytes, or 116,800 once the download is faster than 116,800 B/s.
func minChunk(speedBps float64) int64 {
	if speedBps > fastThresholdBps {
		return minChunkFast
	}
	return minChunkBase
}

// planSplit picks the segment to split and where the new segment starts
// (docs/ENGINE-SPEC.md §4, §5). It is pure; the caller applies the result with a
// compare-and-swap of the victim's end and retries when the victim moved on.
//
//   - Unforced: the victim is the segment with the largest remaining (lowest index on
//     ties). It must have at least 2·minChunk+3 and at least 23,363 bytes remaining.
//   - Forced: the victim is the segment of the slowest idle owner (largest Idle), both
//     floors ignored; without one, the segment of the slow but receiving owner that
//     needs longest (largest ETA), provided at least 46,720 bytes remain.
//   - newStart = victim.end − remaining/2, except for the first split of a download:
//     tail-first types take the last 23,361 bytes (11,681 when remaining < 46,721);
//     other types use min(mid, start + 10·(received + 11,680)) — unless the split is
//     one of several made at once (several: a ramp-up step opening more than one
//     connection), which keeps the midpoint so that the probe keeps an even share.
//
// ok is false when nothing may be split. The victim always keeps at least one byte.
func planSplit(segs []segView, speedBps float64, firstSplit, tailFirst, several, force bool) (victimIdx int, newStart int64, ok bool) {
	victimIdx = -1
	behind := -1 // forced: the slow but receiving owner's segment with the largest ETA
	for i, s := range segs {
		rem := s.remaining()
		if rem < 2 {
			continue
		}
		switch {
		case !force:
			if victimIdx < 0 || rem > segs[victimIdx].remaining() {
				victimIdx = i
			}
		case s.Idle > 0:
			if victimIdx < 0 || s.Idle > segs[victimIdx].Idle {
				victimIdx = i
			}
		case s.ETA > 0 && rem >= stealMinRemaining:
			if behind < 0 || s.ETA > segs[behind].ETA {
				behind = i
			}
		}
	}
	if victimIdx < 0 {
		victimIdx = behind
	}
	if victimIdx < 0 {
		return -1, 0, false
	}
	v := segs[victimIdx]
	rem := v.remaining()
	if !force && (rem < 2*minChunk(speedBps)+3 || rem < splitFloor) {
		return -1, 0, false
	}
	mid := v.End - rem/2
	newStart = mid
	if firstSplit && !force {
		if tailFirst {
			if rem < tailFirstLimit {
				newStart = v.End - tailFirstSmall
			} else {
				newStart = v.End - tailFirstChunk
			}
		} else if !several {
			newStart = min(mid, v.Start+earlySplitFactor*(v.Pos-v.Start+earlySplitPad))
		}
	}
	if newStart <= v.Pos || newStart > v.End {
		return -1, 0, false
	}
	return victimIdx, newStart, true
}

// isTailFirst reports archive and container types whose index lives at the end of the
// file (central directories, moov atoms), judged by file extension or MIME type.
func isTailFirst(filename, contentType string) bool {
	switch strings.ToLower(path.Ext(filename)) {
	case ".zip", ".7z", ".rar", ".apk", ".jar", ".mp4", ".mov", ".m4a", ".mkv":
		return true
	}
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	switch mt {
	case "application/zip", "application/x-zip-compressed", "application/x-zip",
		"application/x-7z-compressed",
		"application/vnd.rar", "application/x-rar-compressed", "application/x-rar",
		"application/vnd.android.package-archive",
		"application/java-archive", "application/x-java-archive",
		"video/mp4", "video/quicktime", "audio/mp4", "audio/x-m4a", "audio/m4a",
		"video/x-matroska", "video/matroska", "audio/x-matroska":
		return true
	}
	return false
}
