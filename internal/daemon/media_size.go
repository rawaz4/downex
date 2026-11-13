package daemon

import (
	"downex/internal/dlview"
	"downex/internal/model"
)

// liveMediaSize is Download.size and size_approx of media download d (not completed)
// while it runs and its job has reported progress p. Before the first event (queued,
// probing, connecting: docs/SITE-MEDIA-SPEC.md §3.3) and while it is stopped, the stored
// rules of dlview.Size apply alone. In order:
//   - the job's byte total (MediaProgress.BytesTotal; an estimate when BytesTotalApprox);
//   - the stored size (dlview.Size: a checkpoint's total, a web page's probed size, a
//     stream's extrapolated fragments);
//   - the probe's size of the selected tracks (dlview.ProbedSize), which a stream shows
//     once its job runs, even before its first fragment is durable.
func liveMediaSize(d *model.Download, p *MediaProgress) (int64, bool) {
	if p.BytesTotal > 0 {
		return p.BytesTotal, p.BytesTotalApprox
	}
	if n, approx := dlview.Size(d); n >= 0 {
		return n, approx
	}
	return dlview.ProbedSize(d.Media)
}
