package daemon

import (
	"testing"
	"time"

	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
)

// TestMediaDownloadSize: while a media download runs, its Download.size is the job's
// byte total (an estimate when the job says so), else the stored size (dlview.Size,
// whose rules for stopped records internal/dlview TestSize covers), else, once the job
// reports progress, the probe's size of the selected tracks (their sizes, else their bit
// rate × the duration); size_approx marks every estimate. Before the job's first event
// (probing, connecting) the stored size applies alone, so a size without durable
// fragments is unknown. A web page's direct formats keep their exact total (the probe's,
// or the one the job stored with its checkpoint) whether running or not.
func TestMediaDownloadSize(t *testing.T) {
	media := func() *model.Media {
		return &model.Media{
			Format: "hls", Container: "mp4", Duration: 100 * time.Second, SelectedVideo: "v1", SelectedAudio: "a1",
			Tracks: []model.MediaTrack{
				{ID: "v0", Kind: "video", Bandwidth: 800_000},
				{ID: "v1", Kind: "video", Bandwidth: 4_000_000},
				{ID: "a1", Kind: "audio", Bandwidth: 128_000},
				{ID: "m0", Kind: "muxed"},
			},
			FragmentsTotal: 40,
		}
	}
	view := func(state model.State, med *model.Media, running bool, p *MediaProgress) downloadView {
		return downloadView{
			rec: model.Download{ID: 1, Request: model.Request{URL: testManifest}, Size: -1, State: state,
				Kind: model.KindMedia, Media: med},
			running: running, mprogress: p,
		}
	}
	check := func(name string, v downloadView, size int64, approx bool) {
		t.Helper()
		if p := v.toProto(); p.GetSize() != size || p.GetSizeApprox() != approx {
			t.Errorf("%s: size %d (approx %v), want %d (approx %v)", name, p.GetSize(), p.GetSizeApprox(), size, approx)
		}
	}
	bitRate := int64((4_000_000 + 128_000) * 100 / 8)

	check("started, no event yet", view(model.StateProbing, media(), true, nil), -1, false)
	check("no total from the job", view(model.StateDownloading, media(), true, &MediaProgress{FragmentsTotal: 40}), bitRate, true)
	check("the job's estimate", view(model.StateDownloading, media(), true,
		&MediaProgress{FragmentsTotal: 40, FragmentsDone: 4, BytesDone: 5_000_000, BytesTotal: 51_000_000, BytesTotalApprox: true}),
		51_000_000, true)
	check("the job's exact total", view(model.StateDownloading, media(), true,
		&MediaProgress{FragmentsTotal: 40, BytesTotal: 52_000_000}), 52_000_000, false)

	sized := media()
	sized.Tracks[1].Size, sized.Tracks[1].SizeApprox = 40_000_000, true
	sized.Tracks[2].Size = 1_600_000
	check("probed sizes", view(model.StateDownloading, sized, true, &MediaProgress{}), 41_600_000, true)
	sized.Tracks[1].SizeApprox = false
	check("exact probed sizes", view(model.StateDownloading, sized, true, &MediaProgress{}), 41_600_000, false)
	muxed := media()
	muxed.SelectedVideo, muxed.SelectedAudio = "m0", ""
	check("no bit rate", view(model.StateDownloading, muxed, true, &MediaProgress{}), -1, false)
	noLength := media()
	noLength.Duration = 0
	check("no duration", view(model.StateDownloading, noLength, true, &MediaProgress{}), -1, false)

	paused := media()
	paused.FragmentsDone, paused.BytesDone = 10, 12_500_000
	check("resumed, no event yet", view(model.StateDownloading, paused, true, nil), 50_000_000, true)
	check("resumed, fragments durable, no total from the job", view(model.StateDownloading, paused, true,
		&MediaProgress{FragmentsTotal: 40, FragmentsDone: 10, BytesDone: 12_500_000}), 50_000_000, true)

	site := media()
	site.Origin, site.Format = "site", "http"
	check("site total", view(model.StateDownloading, site, true, &MediaProgress{BytesTotal: 60_000_000}), 60_000_000, false)

	// A web page's direct formats (docs/SITE-MEDIA-SPEC.md §2.5): chunks are uneven, so
	// their bytes are never extrapolated; once it has started, the exact total stays.
	exact := func() *model.Media {
		m := media()
		m.Origin, m.Format = "site", "http"
		m.Tracks[1].Size, m.Tracks[2].Size = 300_000_000, 16_000_000
		m.FragmentsTotal = 37
		return m
	}
	const exactTotal = 300_000_000 + 16_000_000
	check("site resolving", view(model.StateProbing, exact(), true, nil), -1, false)
	check("site running", view(model.StateDownloading, exact(), true, &MediaProgress{}), exactTotal, false)
	sitePaused := exact()
	sitePaused.FragmentsDone, sitePaused.BytesDone = 4, 52_000_000
	check("site resumed, no event yet", view(model.StateProbing, sitePaused, true, nil), exactTotal, false)
	estimated := exact()
	estimated.Tracks[1].SizeApprox = true
	estimated.FragmentsDone, estimated.BytesDone = 4, 52_000_000
	check("site resumed, estimated sizes", view(model.StateProbing, estimated, true, nil), exactTotal, true)
	// Exact yt-dlp sizes do not make a site stream's size exact: the remuxed MP4 differs
	// from the tracks' sum, and Run's estimate is approximate while it runs, so the size
	// keeps its kind across a pause (the paused case: internal/dlview TestSize).
	siteStreamSized := media()
	siteStreamSized.Origin, siteStreamSized.Format = "site", "hls"
	siteStreamSized.Tracks[1].Size, siteStreamSized.Tracks[2].Size = 50_000_000, 1_600_000
	siteStreamSized.FragmentsDone, siteStreamSized.BytesDone = 10, 12_500_000
	check("site stream with sizes, running", view(model.StateDownloading, siteStreamSized, true,
		&MediaProgress{FragmentsTotal: 40, FragmentsDone: 10, BytesDone: 12_500_000, BytesTotal: 51_000_000, BytesTotalApprox: true}),
		51_000_000, true)
	check("site stream with sizes, resumed, no event yet", view(model.StateProbing, siteStreamSized, true, nil), 50_000_000, true)

	live := view(model.StateDownloading, media(), true,
		&MediaProgress{FragmentsTotal: 40, FragmentsDone: 4, BytesDone: 5_000_000, BytesTotal: 51_000_000, BytesTotalApprox: true})
	if p := live.toProto(); p.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING || p.GetReceived() != 5_000_000 ||
		p.GetMedia().GetFragmentsDone() != 4 {
		t.Errorf("live = %v", p)
	}
}
