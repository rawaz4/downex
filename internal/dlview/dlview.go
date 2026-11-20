// Package dlview holds the rules by which a stored download is shown: its size (exact,
// an estimate or unknown) and the bytes it has on disk. downexd's Download (Get, List,
// Watch) shows a stopped download by them, and layers a running job's progress on top
// (internal/daemon); the CLI's local views (info, and ls without downexd) show every
// stored download by them, so both print the same record alike
// (docs/SITE-MEDIA-SPEC.md §3.3, §3.7). The rules read only the stored model.Download.
package dlview

import (
	"math"
	"net/url"

	"downex/internal/media"
	"downex/internal/model"
	"downex/internal/played"
	"downex/internal/sitemedia"
)

// Size is the size of stored download d and whether it is an estimate (Download.size and
// size_approx; -1 = unknown): a completed download's file size, a file download's size,
// and for a media download not completed (docs/MEDIA-SPEC.md §7,
// docs/SITE-MEDIA-SPEC.md §3.3), in order:
//   - the exact total a site job stored with its checkpoint (it measured the sizes);
//   - unknown while nothing was received (queued, or stopped before its first fragment);
//   - for a web page's direct formats (docs/SITE-MEDIA-SPEC.md §2.5), whose chunks are
//     of uneven sizes, the probe's size (ProbedSize; exact when every selected track's
//     is), never an extrapolation;
//   - for a stream, its durable fragments' bytes as of the last checkpoint extrapolated
//     over the rest of the fragments (an estimate; every fragment: their bytes, still an
//     estimate, since the output's size differs);
//   - else unknown.
func Size(d *model.Download) (size int64, approx bool) {
	if d.Kind != model.KindMedia || d.State == model.StateCompleted {
		return d.Size, false
	}
	med := d.Media
	if med == nil {
		return -1, false
	}
	if cp := med.Checkpoint; cp != nil && cp.BytesTotal > 0 {
		return cp.BytesTotal, false
	}
	if received := med.BytesDone > 0 || med.FragmentsDone > 0 || med.Checkpoint != nil; !received {
		return -1, false
	}
	if rangedSite(med) {
		return ProbedSize(med)
	}
	if med.BytesDone > 0 && med.FragmentsDone > 0 && med.FragmentsTotal > 0 {
		if med.FragmentsDone >= med.FragmentsTotal {
			return med.BytesDone, true // every fragment: the output's size differs
		}
		if n := float64(med.BytesDone) * float64(med.FragmentsTotal) / float64(med.FragmentsDone); n < math.MaxInt64/2 {
			return int64(math.Round(n)), true
		}
	}
	return -1, false
}

// NeedsSegments reports whether Received reads d's segments: a file download that is
// not completed.
func NeedsSegments(d *model.Download) bool {
	return d.Kind != model.KindMedia && d.State != model.StateCompleted
}

// Received is the bytes of stored download d known to be on disk (Download.received
// while it is stopped): a completed download's size, a media download's stored bytes
// done (mediaReceived), a file download's durable bytes of its segments segs (read only
// when NeedsSegments).
func Received(d *model.Download, segs []model.Segment) int64 {
	switch {
	case d.State == model.StateCompleted:
		return max(d.Size, 0)
	case d.Kind == model.KindMedia:
		if d.Media == nil {
			return 0
		}
		return mediaReceived(d.Media)
	}
	var n int64
	for _, s := range segs {
		n += s.Durable
	}
	return n
}

// rangedSite reports a web page's direct formats (site media of format "http", and the
// files of a played video, docs/SITE-MEDIA-SPEC.md §2.9), which mediajob.RunRanged
// downloads in chunks.
func rangedSite(med *model.Media) bool {
	return (med.Origin == sitemedia.OriginSite || med.Origin == played.OriginPlayed) && med.Format == sitemedia.FormatHTTP
}

// ProbedSize is the probe's size of med's selected tracks, -1 when it is not known:
// their sizes when every one has one (an estimate when any is), else their bit rates ×
// the duration (an estimate), as the desktop's quality picker computes it
// (ui/frontend lib/media.ts pickSize). downexd also shows it for a stream whose job has
// reported progress but no size yet.
func ProbedSize(med *model.Media) (int64, bool) {
	if med == nil {
		return -1, false
	}
	var chosen []*model.MediaTrack
	for _, id := range []string{med.SelectedVideo, med.SelectedAudio} {
		if t := med.Track(id); id != "" && t != nil {
			chosen = append(chosen, t)
		}
	}
	if len(chosen) == 0 {
		return -1, false
	}
	var size int64
	approx, sized := false, true
	for _, t := range chosen {
		sized = sized && t.Size > 0
		size += t.Size
		approx = approx || t.SizeApprox
	}
	if sized {
		return clipSize(med, size, approx)
	}
	if med.Duration <= 0 {
		return -1, false
	}
	var bits int64
	for _, t := range chosen {
		if t.Bandwidth <= 0 && t.Kind != string(media.KindAudio) {
			return -1, false // a video's bit rate is what the estimate stands on
		}
		bits += max(t.Bandwidth, 0)
	}
	if bits <= 0 {
		return -1, false
	}
	n := float64(bits) / 8 * med.Duration.Seconds()
	if n < 1 || n >= math.MaxInt64/2 {
		return -1, false
	}
	return clipSize(med, int64(math.Round(n)), true)
}

// mediaReceived is the bytes done of a media download as stored: its BytesDone, except
// for a web page's direct formats (rangedSite) with a checkpoint written before 0.8.2
// (no DurableBytes, BytesTotal or chunk prefixes), whose BytesDone also counted chunks
// still arriving. For those, it is the bytes of the checkpoint's done chunks, which a
// resumed RunRanged starts from (it keeps no prefix of an old checkpoint), so the
// stopped view never shows more than the resume continues from; with an estimated size
// the chunks only bound BytesDone. Streams keep BytesDone: their percent counts
// fragments.
func mediaReceived(med *model.Media) int64 {
	done := max(med.BytesDone, 0)
	cp := med.Checkpoint
	if cp == nil || !rangedSite(med) || cp.DurableBytes > 0 || cp.BytesTotal > 0 {
		return done
	}
	var n int64
	exact := true
	for _, tc := range cp.Tracks {
		if len(tc.Partial) > 0 {
			return done // written by 0.8.2: its prefixes are counted in BytesDone
		}
		size := int64(-1)
		if t := med.Track(tc.TrackID); t != nil && t.Size > 0 {
			size = t.Size
			exact = exact && !t.SizeApprox
		} else {
			exact = false
		}
		for i := range min(tc.Total, len(tc.Done)*8) {
			if tc.Done[i/8]&(1<<(i%8)) == 0 {
				continue
			}
			switch {
			case tc.ChunkBytes > 0 && size > 0:
				n += max(min(tc.ChunkBytes, size-int64(i)*tc.ChunkBytes), 0)
			case tc.ChunkBytes > 0:
				n += tc.ChunkBytes // at most one chunk
			case size > 0:
				n += size // a whole track, finished
			default:
				return done // a finished whole track of unknown size: BytesDone is all there is
			}
		}
	}
	if exact {
		return n
	}
	return min(done, n)
}

// AverageBps is the average speed of bytes received over ms of running time (active_ms,
// pauses excluded) in bytes per second, rounded (docs/DESKTOP-SPEC.md §8); 0 when the time
// or the bytes are not known. downexd's "download completed" record and downex info use it.
func AverageBps(bytes, ms int64) int64 {
	if bytes < 0 || ms <= 0 {
		return 0
	}
	return int64(math.Round(float64(bytes) * 1000 / float64(ms)))
}

// maxPageURLBytes bounds a page address (the store's limit, docs/ENGINE-SPEC.md §7.2).
const maxPageURLBytes = 32 << 10

// PageURL is the page stored file download d came from (Download.page_url;
// docs/DESKTOP-SPEC.md §10): the one its add named, else its Referer when that is an
// http or https address naming more than an origin (a referrer cut to its origin names
// no page); "" when unknown, and always for a media download (its page is media's).
func PageURL(d *model.Download) string {
	if d.Kind == model.KindMedia {
		return ""
	}
	if d.PageURL != "" {
		return d.PageURL
	}
	ref := d.Request.Headers.Get("Referer")
	if ref == "" || len(ref) > maxPageURLBytes {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return ""
	}
	if (u.Path == "" || u.Path == "/") && u.RawQuery == "" {
		return ""
	}
	return ref
}

// clipPad is what a part's estimate adds to its length: the fetch starts at a keyframe
// or fragment before the part (docs/MEDIA-SPEC.md §8 B4).
const clipPad = 5_000 // ms

// ClipFraction is the share of med's video that its part fetches (docs/MEDIA-SPEC.md §8
// B4): (To − From + 5 s) / duration, the end rounded up to a whole second, at most 1; 1
// without a part or without a duration.
func ClipFraction(med *model.Media) float64 {
	if med == nil || med.Clip == nil || med.Duration <= 0 {
		return 1
	}
	dur := med.Duration.Milliseconds()
	end := med.Clip.EndMs
	if end <= 0 || end > dur {
		end = dur
	}
	span := end - med.Clip.StartMs + clipPad
	span = (span + 999) / 1000 * 1000
	if span <= 0 {
		return 1
	}
	return min(float64(span)/float64(dur), 1)
}

// clipSize scales a whole video's size to its part: an estimate whenever a part is cut.
func clipSize(med *model.Media, size int64, approx bool) (int64, bool) {
	f := ClipFraction(med)
	if f >= 1 || size <= 0 {
		return size, approx
	}
	return max(int64(math.Round(float64(size)*f)), 1), true
}
