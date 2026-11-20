package format

import (
	"math"

	downexv1 "downex/internal/rpc/downex/v1"
)

// Size is a download's size for display: "2.1 GB", "~2.1 GB" for downexd's estimate (a
// stream that is not completed), "—" when unknown.
func Size(d *downexv1.Download) string {
	if !(d.GetSize() >= 0) {
		return Dash
	}
	if d.GetSizeApprox() {
		return "~" + Bytes(float64(d.GetSize()))
	}
	return Bytes(float64(d.GetSize()))
}

// isMedia reports a media download (DOWNLOAD_KIND_MEDIA: a stream or a web page's video).
func isMedia(d *downexv1.Download) bool {
	return d.GetKind() == downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA
}

// fragmentFraction is the share of a media download's fragments done, 0..1; NaN when it has
// no fragment count, and for a web page's video saved from its direct formats (format
// "http"): its parts are chunks of uneven sizes (a 10 MiB video chunk, a 2 MiB audio one), so
// its bytes measure it against Download.size like a file, indeterminate while the size is
// unknown (SITE-MEDIA-SPEC §2.5, §5.4). A stream's estimated size never measures its progress:
// the fragments do.
func fragmentFraction(d *downexv1.Download) float64 {
	m := d.GetMedia()
	if m == nil || !(m.GetFragmentsTotal() > 0) {
		return math.NaN()
	}
	if isMedia(d) && (m.GetOrigin() == downexv1.MediaOrigin_MEDIA_ORIGIN_SITE || m.GetOrigin() == downexv1.MediaOrigin_MEDIA_ORIGIN_PLAYED) && m.GetFormat() == "http" {
		return math.NaN()
	}
	return math.Min(1, math.Max(0, float64(m.GetFragmentsDone())/float64(m.GetFragmentsTotal())))
}

// EtaSeconds is the remaining time of a running download in seconds; NaN when it cannot be
// computed. A stream's comes from downexd's estimate of its size while that is ahead of the
// bytes received, else from the bytes its finished fragments took.
func EtaSeconds(d *downexv1.Download) float64 {
	speed := d.GetSpeedBps()
	if d.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING || !(speed > 0) {
		return math.NaN()
	}
	size, received := float64(d.GetSize()), float64(d.GetReceived())
	if d.GetSizeApprox() && size > received {
		return (size - received) / speed
	}
	fragments := fragmentFraction(d)
	if !math.IsNaN(fragments) {
		if !(fragments > 0) || !(received > 0) {
			return math.NaN()
		}
		return math.Max(0, received/fragments-received) / speed
	}
	if size < 0 {
		return math.NaN()
	}
	return math.Max(0, size-received) / speed
}

// ProgressFraction is a download's progress 0..1, or NaN when its size is unknown
// (indeterminate). Media downloads count fragments, except a web page's video from its direct
// formats, always measured by its bytes (indeterminate while its size is unknown).
func ProgressFraction(d *downexv1.Download) float64 {
	if d.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED {
		return 1
	}
	if fragments := fragmentFraction(d); !math.IsNaN(fragments) {
		return fragments
	}
	size := float64(d.GetSize())
	if size <= 0 {
		if size == 0 {
			return 0
		}
		return math.NaN()
	}
	return math.Min(1, math.Max(0, float64(d.GetReceived())/size))
}
