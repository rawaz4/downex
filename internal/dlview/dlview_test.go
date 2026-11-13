package dlview

import (
	"encoding/json"
	"testing"
	"time"

	"downex/internal/model"
)

const testManifest = "https://cdn.test/show/master.m3u8"

// testMedia is an HLS stream of 40 fragments: video v1 (4 Mbit/s) and audio a1 chosen,
// 100 s long, no track sizes.
func testMedia() *model.Media {
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

func mediaDownload(state model.State, med *model.Media) *model.Download {
	return &model.Download{ID: 1, Request: model.Request{URL: testManifest}, Size: -1, State: state,
		Kind: model.KindMedia, Media: med}
}

// TestSize: a stored media download's size is its file's once completed; until then the
// total a site job stored with its checkpoint, else (once something was received) a web
// page's probed size, never extrapolated, or a stream's durable fragments extrapolated
// over the rest; size_approx marks every estimate. Before anything was received it is
// unknown. (The running job's precedence is downexd's: internal/daemon
// TestMediaDownloadSize.)
func TestSize(t *testing.T) {
	check := func(name string, d *model.Download, size int64, approx bool) {
		t.Helper()
		if n, a := Size(d); n != size || a != approx {
			t.Errorf("%s: size %d (approx %v), want %d (approx %v)", name, n, a, size, approx)
		}
	}

	check("queued", mediaDownload(model.StateQueued, testMedia()), -1, false)
	paused := testMedia()
	paused.FragmentsDone, paused.BytesDone = 10, 12_500_000
	check("paused midway", mediaDownload(model.StatePaused, paused), 50_000_000, true)
	check("failed midway", mediaDownload(model.StateFailed, paused), 50_000_000, true)
	check("paused before any fragment", mediaDownload(model.StatePaused, testMedia()), -1, false)
	every := testMedia()
	every.FragmentsDone, every.BytesDone = 40, 49_000_000
	check("paused with every fragment", mediaDownload(model.StatePaused, every), 49_000_000, true)

	done := mediaDownload(model.StateCompleted, paused)
	done.Size = 49_000_000
	check("completed", done, 49_000_000, false)
	check("no media description", mediaDownload(model.StatePaused, nil), -1, false)
	file := &model.Download{ID: 2, Size: 1000, State: model.StatePaused, Kind: model.KindFile}
	check("file", file, 1000, false)

	// A web page's direct formats (docs/SITE-MEDIA-SPEC.md §2.5): chunks are uneven (a
	// 10 MiB video chunk, a 2 MiB audio one), so their bytes are never extrapolated; once
	// it has started, the exact total stays, paused or failed.
	exact := func() *model.Media {
		m := testMedia()
		m.Origin, m.Format = "site", "http"
		m.Tracks[1].Size, m.Tracks[2].Size = 300_000_000, 16_000_000
		m.FragmentsTotal = 37
		return m
	}
	const exactTotal = 300_000_000 + 16_000_000
	check("site queued", mediaDownload(model.StateQueued, exact()), -1, false)
	sitePaused := exact()
	sitePaused.FragmentsDone, sitePaused.BytesDone = 4, 52_000_000
	check("site paused", mediaDownload(model.StatePaused, sitePaused), exactTotal, false)
	check("site failed", mediaDownload(model.StateFailed, sitePaused), exactTotal, false)
	estimated := exact()
	estimated.Tracks[1].SizeApprox = true
	estimated.FragmentsDone, estimated.BytesDone = 4, 52_000_000
	check("site paused, estimated sizes", mediaDownload(model.StatePaused, estimated), exactTotal, true)
	// The job measured the sizes (formats without one): its checkpoint keeps the total.
	measured := testMedia()
	measured.Origin, measured.Format = "site", "http"
	measured.FragmentsDone, measured.BytesDone = 4, 52_000_000
	measured.Checkpoint = &model.MediaCheckpoint{DurableBytes: 52_000_000, BytesTotal: 316_000_000,
		Tracks: []model.MediaTrackCheckpoint{{TrackID: "v1", Total: 37, Done: []byte{0x0f, 0, 0, 0, 0}}}}
	check("site paused, measured total", mediaDownload(model.StatePaused, measured), 316_000_000, false)
	unsized := testMedia()
	unsized.Origin, unsized.Format = "site", "http"
	unsized.Tracks[1].Bandwidth, unsized.Tracks[2].Bandwidth = 0, 0
	unsized.FragmentsDone, unsized.BytesDone = 4, 52_000_000
	check("site paused, no size at all", mediaDownload(model.StatePaused, unsized), -1, false)
	// A site's stream (HLS/DASH through yt-dlp) is a stream: its fragments are extrapolated.
	siteStream := testMedia()
	siteStream.Origin, siteStream.Format = "site", "hls"
	siteStream.FragmentsDone, siteStream.BytesDone = 10, 12_500_000
	check("site stream paused", mediaDownload(model.StatePaused, siteStream), 50_000_000, true)
	// Exact yt-dlp sizes do not make a stream's size exact: the remuxed MP4 differs from
	// the tracks' sum, so the size keeps its kind across a pause.
	siteStreamSized := testMedia()
	siteStreamSized.Origin, siteStreamSized.Format = "site", "hls"
	siteStreamSized.Tracks[1].Size, siteStreamSized.Tracks[2].Size = 50_000_000, 1_600_000
	siteStreamSized.FragmentsDone, siteStreamSized.BytesDone = 10, 12_500_000
	check("site stream with sizes, paused", mediaDownload(model.StatePaused, siteStreamSized), 50_000_000, true)
}

// TestProbedSize: the probe's size of the selected tracks is their sizes when every one
// has one (an estimate when any is), else their bit rates × the duration (an estimate);
// unknown without a video's bit rate or a duration.
func TestProbedSize(t *testing.T) {
	check := func(name string, med *model.Media, size int64, approx bool) {
		t.Helper()
		if n, a := ProbedSize(med); n != size || a != approx {
			t.Errorf("%s: size %d (approx %v), want %d (approx %v)", name, n, a, size, approx)
		}
	}
	check("bit rates", testMedia(), (4_000_000+128_000)*100/8, true)
	sized := testMedia()
	sized.Tracks[1].Size, sized.Tracks[1].SizeApprox = 40_000_000, true
	sized.Tracks[2].Size = 1_600_000
	check("probed sizes", sized, 41_600_000, true)
	sized.Tracks[1].SizeApprox = false
	check("exact probed sizes", sized, 41_600_000, false)
	muxed := testMedia()
	muxed.SelectedVideo, muxed.SelectedAudio = "m0", ""
	check("no bit rate", muxed, -1, false)
	noLength := testMedia()
	noLength.Duration = 0
	check("no duration", noLength, -1, false)
	none := testMedia()
	none.SelectedVideo, none.SelectedAudio = "", ""
	check("nothing selected", none, -1, false)
	check("no media description", nil, -1, false)
}

// A web page's direct formats paused under 0.8.0/0.8.1, in the shape those versions stored
// them (made-up sizes and keys that keep the chunk counts: 29 video chunks of 10 MiB, 8
// audio chunks of 2,162,688 B): the checkpoint has no DurableBytes, and BytesDone counted
// chunks still arriving. A resumed RunRanged starts from the checkpoint's done chunks only
// (video chunks 0, 4, 5 of 10 MiB, audio chunk 0 of 2,162,688 B), so the stopped view
// shows those bytes, never more, and the progress does not drop when it resumes
// (docs/SITE-MEDIA-SPEC.md §2.5).
func TestOldSiteCheckpointShowsItsDurableChunks(t *testing.T) {
	const cpJSON = `{"tracks":[{"trackId":"401","total":29,"done":"MQAAAA==","key":"s1-v","chunkBytes":10485760},{"trackId":"140","total":8,"done":"AQ==","key":"s1-a","chunkBytes":2162688}]}`
	const durable = 3*10_485_760 + 2_162_688
	rec := func() *model.Download {
		var cp model.MediaCheckpoint
		if err := json.Unmarshal([]byte(cpJSON), &cp); err != nil {
			t.Fatal(err)
		}
		return &model.Download{ID: 1, State: model.StatePaused, Kind: model.KindMedia, Size: -1,
			Media: &model.Media{
				Origin: "site", Format: "http", SelectedVideo: "401", SelectedAudio: "140",
				Tracks: []model.MediaTrack{
					{ID: "401", Kind: "video", Size: 300_000_000, Key: "s1-v"},
					{ID: "140", Kind: "audio", Size: 16_000_000, Key: "s1-a"},
				},
				FragmentsTotal: 37, FragmentsDone: 4, BytesDone: 52_000_000, Checkpoint: &cp,
			}}
	}

	d := rec()
	if n := Received(d, nil); n != durable {
		t.Errorf("paused old checkpoint: received %d, want the %d bytes a resume starts from", n, durable)
	}
	if n, approx := Size(d); n != 300_000_000+16_000_000 || approx {
		t.Errorf("paused old checkpoint: size %d (approx %v), want the exact total", n, approx)
	}

	// The last chunk is shorter than ChunkBytes: it counts its own length.
	last := rec()
	last.Media.Checkpoint.Tracks[1].Done = []byte{0x81} // audio chunks 0 and 7
	if n, want := Received(last, nil), int64(3*10_485_760+2_162_688+(16_000_000-7*2_162_688)); n != want {
		t.Errorf("old checkpoint with a short last chunk: received %d, want %d", n, want)
	}

	// An estimated track size: the chunks bound BytesDone, never raise it.
	approx := rec()
	approx.Media.Tracks[0].SizeApprox = true
	if n := Received(approx, nil); n != durable {
		t.Errorf("old checkpoint, estimated size: received %d, want %d", n, durable)
	}
	approx.Media.BytesDone = 1000
	if n := Received(approx, nil); n != 1000 {
		t.Errorf("old checkpoint, estimated size, few bytes done: received %d, want 1000", n)
	}

	// A 0.8.2 checkpoint is taken as written: BytesDone is its DurableBytes.
	cur := rec()
	cur.Media.Checkpoint.DurableBytes, cur.Media.Checkpoint.BytesTotal = 40_000_000, 316_000_000
	cur.Media.Checkpoint.Tracks[0].Partial = map[int]int64{1: 4_000_000}
	cur.Media.BytesDone = 40_000_000
	if n := Received(cur, nil); n != 40_000_000 {
		t.Errorf("0.8.2 checkpoint: received %d, want its 40000000 durable bytes", n)
	}

	// A stream's old checkpoint keeps BytesDone (its percent counts fragments).
	stream := rec()
	stream.Media.Format = "hls"
	if n := Received(stream, nil); n != 52_000_000 {
		t.Errorf("old stream checkpoint: received %d, want BytesDone", n)
	}
}

// TestReceived: a completed download has its size on disk; a file download its
// segments' durable bytes, which are read only for it (NeedsSegments).
func TestReceived(t *testing.T) {
	segs := []model.Segment{{ID: 0, Start: 0, End: 499, Durable: 200}, {ID: 1, Start: 500, End: 999, Durable: 100}}
	file := &model.Download{Size: 1000, State: model.StatePaused, Kind: model.KindFile}
	if n := Received(file, segs); n != 300 || !NeedsSegments(file) {
		t.Errorf("paused file: received %d (segments read: %v), want 300 from its segments", n, NeedsSegments(file))
	}
	doneFile := &model.Download{Size: 1000, State: model.StateCompleted, Kind: model.KindFile}
	if n := Received(doneFile, segs); n != 1000 || NeedsSegments(doneFile) {
		t.Errorf("completed file: received %d (segments read: %v), want its size", n, NeedsSegments(doneFile))
	}
	unsized := &model.Download{Size: -1, State: model.StateCompleted, Kind: model.KindFile}
	if n := Received(unsized, nil); n != 0 {
		t.Errorf("completed, size unknown: received %d, want 0", n)
	}
	med := mediaDownload(model.StatePaused, testMedia())
	med.Media.BytesDone = 12_500_000
	if n := Received(med, segs); n != 12_500_000 || NeedsSegments(med) {
		t.Errorf("paused stream: received %d (segments read: %v), want its bytes done", n, NeedsSegments(med))
	}
	if n := Received(mediaDownload(model.StatePaused, nil), nil); n != 0 {
		t.Errorf("media without a description: received %d, want 0", n)
	}
	doneMedia := mediaDownload(model.StateCompleted, testMedia())
	doneMedia.Size = 49_000_000
	if n := Received(doneMedia, nil); n != 49_000_000 {
		t.Errorf("completed stream: received %d, want its file's size", n)
	}
}

// TestAverageBps: the bytes over the running time, rounded; an unknown time or byte count
// gives 0 (docs/DESKTOP-SPEC.md §8, the daemon's avg_bps and downex info).
func TestAverageBps(t *testing.T) {
	for _, c := range []struct{ bytes, ms, want int64 }{
		{256 << 10, 4000, 64 << 10},
		{10, 3, 3333},
		{2, 3, 667},
		{0, 1000, 0},
		{1 << 20, 0, 0},
		{1 << 20, -5, 0},
		{-1, 1000, 0},
	} {
		if got := AverageBps(c.bytes, c.ms); got != c.want {
			t.Errorf("AverageBps(%d, %d) = %d, want %d", c.bytes, c.ms, got, c.want)
		}
	}
}
