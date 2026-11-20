package daemon

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"downex/internal/browserfetch"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/store"
	"downex/internal/testmedia"
)

const (
	originPlayed = downexv1.MediaOrigin_MEDIA_ORIGIN_PLAYED
	playedPage   = "https://www.social.example/feed"
)

// fakePlayed is a PlayedMedia that records its runs and downloads like fakeMedia.
type fakePlayed struct {
	job  *fakeMedia
	mu   sync.Mutex
	runs []PlayedSelection
	opts []MediaRunOptions
}

func (f *fakePlayed) Run(ctx context.Context, opts MediaRunOptions, sel PlayedSelection, resume *model.MediaCheckpoint,
	onProgress func(MediaProgress), onCheckpoint func(model.MediaCheckpoint) error) (string, error) {
	f.mu.Lock()
	f.runs = append(f.runs, sel)
	f.opts = append(f.opts, opts)
	f.mu.Unlock()
	return f.job.Run(ctx, opts, MediaSelection{}, resume, onProgress, onCheckpoint)
}

func (f *fakePlayed) calls() ([]PlayedSelection, []MediaRunOptions) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]PlayedSelection(nil), f.runs...), append([]MediaRunOptions(nil), f.opts...)
}

// playedHead is the first 64 KiB of a muxed 3 s MP4.
func playedHead(t *testing.T) []byte {
	t.Helper()
	v := testmedia.VideoConfig{Frames: 90}
	a := testmedia.AudioConfig{Frames: 140}
	data, err := testmedia.ProgressiveMP4(v, testmedia.Video(v), a, testmedia.AAC(a))
	if err != nil {
		t.Fatal(err)
	}
	return data[:min(len(data), 64<<10)]
}

func playedSource(t *testing.T) *downexv1.PlayedSource {
	head := playedHead(t)
	return &downexv1.PlayedSource{
		DurationMs: 3000, VideoWidth: 640, VideoHeight: 360, HasAudio: 1,
		Resources: []*downexv1.PlayedResource{
			{Id: "r1", Url: "https://media.example/v/other.mp4", Size: 9_000_000, Kind: "video", Head: []byte("<html>")},
			{Id: "r2", Url: "https://media.example/v/this.mp4?sig=1", Size: 2_000_000, Kind: "video", Head: head, Browser: true},
		},
	}
}

func TestPlayedProbeAddRun(t *testing.T) {
	fp := &fakePlayed{job: newFakeMedia(1)}
	h := newSiteHarness(t, harnessOptions{}, siteHarnessOptions{played: fp})

	info, err := h.dl.ProbeMedia(ctxT(t), connect.NewRequest(&downexv1.ProbeMediaRequest{Origin: originPlayed, PageUrl: playedPage,
		Played: playedSource(t)}))
	if err != nil {
		t.Fatal(err)
	}
	m := info.Msg.GetMedia()
	if m.GetOrigin() != originPlayed || m.GetSelectedVideo() != "r2" || m.GetSelectedAudio() != "" || len(m.GetTracks()) != 1 ||
		m.GetTracks()[0].GetKind() != "muxed" || m.GetSite() != "social.example" || m.GetDurationMs() != 3000 || !m.GetBrowserFetch() {
		t.Fatalf("probe: %+v", m)
	}

	res, err := h.dl.AddMedia(ctxT(t), connect.NewRequest(&downexv1.AddMediaRequest{Origin: originPlayed, PageUrl: playedPage,
		Played: playedSource(t), Source: sourceBrowser, Title: "someone - a caption"}))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Msg.GetDownload()
	if !browserfetch.ValidTicket(res.Msg.GetFetchTicket()) {
		t.Fatalf("no ticket: %q", res.Msg.GetFetchTicket())
	}
	if d.GetFilename() != "someone - a caption.mp4" || d.GetMedia().GetOrigin() != originPlayed || d.GetUrl() != playedPage {
		t.Fatalf("download: %s %v %s", d.GetFilename(), d.GetMedia().GetOrigin(), d.GetUrl())
	}
	if d.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION {
		if _, err := h.confirmReq(&downexv1.ConfirmRequest{Id: d.GetId(), Start: true}); err != nil {
			t.Fatal(err)
		}
	}
	h.waitCompleted(d.GetId())
	runs, opts := fp.calls()
	if len(runs) != 1 || len(runs[0].Tracks) != 1 || runs[0].Tracks[0].URL != "https://media.example/v/this.mp4?sig=1" ||
		runs[0].Output != "copy" || !runs[0].BrowserFetch || opts[0].Browser == nil {
		t.Fatalf("runs %+v", runs)
	}
	if h.m.browser.Has(model.DownloadID(d.GetId())) {
		t.Error("the ticket outlived the completed download")
	}
	rec, err := h.m.st.GetDownload(ctxT(t), model.DownloadID(d.GetId()))
	if err != nil || len(rec.Media.Played) != 1 || rec.Media.Played[0].URL == "" {
		t.Fatalf("stored %+v %v", rec.Media, err)
	}
}

func TestPlayedRequestValidation(t *testing.T) {
	h := newSiteHarness(t, harnessOptions{}, siteHarnessOptions{played: &fakePlayed{job: newFakeMedia(1)}})
	probe := func(edit func(r *downexv1.ProbeMediaRequest)) error {
		r := &downexv1.ProbeMediaRequest{Origin: originPlayed, PageUrl: playedPage, Played: playedSource(t)}
		edit(r)
		_, err := h.dl.ProbeMedia(ctxT(t), connect.NewRequest(r))
		return err
	}
	for name, edit := range map[string]func(r *downexv1.ProbeMediaRequest){
		"youtube page": func(r *downexv1.ProbeMediaRequest) { r.PageUrl = "https://www.youtube.com/watch?v=abcdefghijk" },
		"only youtube files": func(r *downexv1.ProbeMediaRequest) {
			for _, f := range r.Played.Resources {
				f.Url = "https://r1.googlevideo.com/videoplayback?id=" + f.Id
			}
		},
		"cookie":         func(r *downexv1.ProbeMediaRequest) { r.Headers = []*downexv1.Header{{Name: "Cookie", Value: "a=b"}} },
		"manifest":       func(r *downexv1.ProbeMediaRequest) { r.ManifestUrl = "https://media.example/a.m3u8" },
		"no source":      func(r *downexv1.ProbeMediaRequest) { r.Played = nil },
		"prefetch":       func(r *downexv1.ProbeMediaRequest) { r.Prefetch = true },
		"huge head":      func(r *downexv1.ProbeMediaRequest) { r.Played.Resources[1].Head = make([]byte, 300<<10) },
		"no page":        func(r *downexv1.ProbeMediaRequest) { r.PageUrl = "" },
		"duplicate id":   func(r *downexv1.ProbeMediaRequest) { r.Played.Resources[0].Id = "r2" },
		"negative ms":    func(r *downexv1.ProbeMediaRequest) { r.Played.DurationMs = -1 },
		"other's ms":     func(r *downexv1.ProbeMediaRequest) { r.Played.OtherDurationsMs = []int64{0} },
		"many others":    func(r *downexv1.ProbeMediaRequest) { r.Played.OtherDurationsMs = make([]int64, 9) },
		"other origin's": func(r *downexv1.ProbeMediaRequest) { r.Origin = originSite },
	} {
		err := probe(edit)
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A file on a refused host is left out, never the source refused for it: here the
	// clicked video's file is the one left out, so nothing of it was played yet.
	for _, u := range []string{"https://r1.googlevideo.com/videoplayback", "http://10.0.0.2/a.mp4"} {
		err := probe(func(r *downexv1.ProbeMediaRequest) { r.Played.Resources[1].Url = u })
		var ce *connect.Error
		if !errors.As(err, &ce) || ce.Meta().Get(ErrorCodeMeta) != CodeNotFoundYet {
			t.Fatalf("%s: got %v", u, err)
		}
	}
	// Nothing of this video was played yet: not_found_yet, which the extension waits on.
	err := probe(func(r *downexv1.ProbeMediaRequest) { r.Played.DurationMs = 60_000 })
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Meta().Get(ErrorCodeMeta) != CodeNotFoundYet {
		t.Fatalf("got %v", err)
	}
	// A part of a played video is refused (whole files only).
	_, err = h.dl.AddMedia(ctxT(t), connect.NewRequest(&downexv1.AddMediaRequest{Origin: originPlayed, PageUrl: playedPage,
		Played: playedSource(t), Source: sourceBrowser, Clip: &downexv1.MediaClip{StartMs: 0, EndMs: 1000}}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "part") {
		t.Fatalf("clip: %v", err)
	}
}

func TestPlayedRecordResumeAfterRestart(t *testing.T) {
	st := store.NewMemory()
	fp := &fakePlayed{job: newFakeMedia(1)}
	h := newSiteHarness(t, harnessOptions{st: st}, siteHarnessOptions{played: fp})
	res, err := h.dl.AddMedia(ctxT(t), connect.NewRequest(&downexv1.AddMediaRequest{Origin: originPlayed, PageUrl: playedPage,
		Played: playedSource(t), Source: downexv1.AddSource_ADD_SOURCE_CLI, StartPaused: true}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.GetDownload().GetId()
	if res.Msg.GetFetchTicket() != "" {
		t.Fatal("a CLI add has no browser to fetch for it")
	}
	sctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	if err := h.m.Shutdown(sctx); err != nil {
		t.Fatal(err)
	}

	fp2 := &fakePlayed{job: newFakeMedia(1)}
	h2 := newSiteHarness(t, harnessOptions{st: st}, siteHarnessOptions{played: fp2})
	if _, err := h2.dl.Resume(ctxT(t), connect.NewRequest(&downexv1.ResumeRequest{Id: id})); err != nil {
		t.Fatal(err)
	}
	h2.waitCompleted(id)
	runs, opts := fp2.calls()
	if len(runs) != 1 || runs[0].Tracks[0].URL != "https://media.example/v/this.mp4?sig=1" || opts[0].Browser != nil {
		t.Fatalf("runs %+v", runs)
	}
}
