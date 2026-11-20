package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"downex/internal/engine"
	"downex/internal/media"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/sitemedia"
	"downex/internal/store"
)

const testManifest = "https://cdn.example.test/shows/big-buck-bunny/hls/master.m3u8?token=secret"

func (h *harness) addMedia(req *downexv1.AddMediaRequest) *downexv1.Download {
	h.t.Helper()
	res, err := h.dl.AddMedia(ctxT(h.t), connect.NewRequest(req))
	if err != nil {
		h.t.Fatalf("add media: %v", err)
	}
	return res.Msg.GetDownload()
}

func (h *harness) confirmReq(req *downexv1.ConfirmRequest) (*downexv1.Download, error) {
	res, err := h.dl.Confirm(ctxT(h.t), connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return res.Msg.GetDownload(), nil
}

// mediaCode returns the stable code of a refused media call: its ErrorCodeMeta, which
// must match the message's prefix.
func mediaCode(t *testing.T, err error) string {
	t.Helper()
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("not a Connect error: %v", err)
	}
	code := ce.Meta().Get(ErrorCodeMeta)
	if !strings.HasPrefix(ce.Message(), code+": ") {
		t.Errorf("message %q does not start with the code %q", ce.Message(), code)
	}
	return code
}

func TestAddMediaQueuesAndCompletes(t *testing.T) {
	fm := newFakeMedia(5)
	h := newHarness(t, harnessOptions{media: fm})
	d := h.addMedia(&downexv1.AddMediaRequest{
		ManifestUrl: testManifest, Source: downexv1.AddSource_ADD_SOURCE_CLI, Title: "Big Buck\nBunny",
		Headers: []*downexv1.Header{{Name: "cookie", Value: "s=1"}, {Name: "Referer", Value: "https://site.test/watch"}},
	})
	if d.GetKind() != downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA || d.GetUrl() != testManifest || d.GetCategory() != "Video" ||
		d.GetFilename() != "Big Buck Bunny.mp4" || d.GetDir() != filepath.Join(h.dir, "Video") || d.GetSize() != -1 ||
		d.GetSource() != downexv1.AddSource_ADD_SOURCE_CLI || d.GetMaxConnections() != DefaultMaxConnections {
		t.Fatalf("added = %v", d)
	}
	mi := d.GetMedia()
	if mi.GetFormat() != "hls" || mi.GetManifestUrl() != testManifest || len(mi.GetTracks()) != 6 || mi.GetSelectedVideo() != "v1" ||
		mi.GetSelectedAudio() != "a1" || mi.GetDurationMs() != 90_000 || mi.GetContainer() != "mp4" ||
		mi.GetFragmentsTotal() != 5 || mi.GetTitle() != "Big Buck Bunny" {
		t.Fatalf("media = %v", mi)
	}
	if tr := mi.GetTracks()[1]; tr.GetId() != "v1" || tr.GetKind() != "video" || tr.GetWidth() != 1920 || tr.GetHeight() != 1080 ||
		tr.GetFrameRate() != 29.97 || tr.GetBandwidth() != 5_000_000 || tr.GetCodecs() != "avc1.640028" {
		t.Errorf("track = %v", tr)
	}

	done := h.waitCompleted(d.GetId())
	out := filepath.Join(h.dir, "Video", "Big Buck Bunny.mp4")
	if got, err := os.ReadFile(out); err != nil || !bytes.Equal(got, wantOutput(5)) {
		t.Fatalf("output %q, %v", got, err)
	}
	if done.GetSize() != int64(len(wantOutput(5))) || done.GetReceived() != done.GetSize() ||
		done.GetMedia().GetFragmentsDone() != 5 || done.GetMedia().GetFragmentsTotal() != 5 || done.GetCompletedAt() == nil {
		t.Errorf("completed = %v", done)
	}
	for _, p := range []string{MediaPartsDir(filepath.Dir(out), filepath.Base(out)), out + ".dxpart"} {
		if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s left behind: %v", p, err)
		}
	}
	probes, runs := fm.calls()
	if len(probes) != 1 || probes[0].ManifestURL != testManifest || probes[0].Headers.Get("Cookie") != "s=1" ||
		probes[0].VideoTrack != "" || probes[0].AudioTrack != "" {
		t.Errorf("probes = %+v", probes)
	}
	if len(runs) != 1 {
		t.Fatalf("%d runs, want 1", len(runs))
	}
	r := runs[0]
	if r.sel.ManifestURL != testManifest || r.sel.VideoTrack != "v1" || r.sel.AudioTrack != "a1" ||
		r.sel.Headers.Get("Referer") != "https://site.test/watch" || r.resume != nil || r.opts.MaxConnections != DefaultMaxConnections ||
		r.opts.OutPath != out || r.opts.PartDir != out+".dxparts" {
		t.Errorf("run = %+v", r)
	}
	rec := storedState(t, h.st, d.GetId())
	if rec.Kind != model.KindMedia || rec.State != model.StateCompleted || rec.Media == nil || rec.Media.Checkpoint != nil ||
		rec.Media.FragmentsDone != 5 || rec.Media.Container != "mp4" || rec.Request.Method != "GET" {
		t.Errorf("stored = %+v media %+v", rec, rec.Media)
	}
	// Removing a completed media download with its file deletes the output.
	if _, err := h.dl.Remove(ctxT(t), connect.NewRequest(&downexv1.RemoveRequest{Id: d.GetId(), DeleteFile: true})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(out); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("output kept: %v", err)
	}
}

func TestAddMediaOptions(t *testing.T) {
	fm := newFakeMedia(2)
	h := newHarness(t, harnessOptions{media: fm})
	dir := t.TempDir()
	// Requested tracks, folder, name (its extension replaced) and start_paused.
	d := h.addMedia(&downexv1.AddMediaRequest{
		ManifestUrl: testManifest, Dir: dir, Filename: "clip.mkv", VideoTrack: "v0", AudioTrack: "a0", StartPaused: true,
		PageUrl: "https://site.test/watch?v=1",
	})
	if d.GetState() != stPaused || d.GetDir() != dir || d.GetFilename() != "clip.mp4" ||
		d.GetMedia().GetSelectedVideo() != "v0" || d.GetMedia().GetSelectedAudio() != "a0" {
		t.Fatalf("added = %v", d)
	}
	if rec := storedState(t, h.st, d.GetId()); rec.Media.PageURL != "https://site.test/watch?v=1" || rec.Media.Title != "" {
		t.Errorf("stored media = %+v", rec.Media)
	}
	probes, runs := fm.calls()
	if probes[0].VideoTrack != "v0" || probes[0].AudioTrack != "a0" || len(runs) != 0 {
		t.Errorf("probes %+v runs %+v", probes, runs)
	}
	// The name is reserved: a second add of the same stream gets a variant.
	d2 := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest, Dir: dir, Filename: "clip", StartPaused: true})
	if d2.GetFilename() != "clip (1).mp4" {
		t.Errorf("second name = %q", d2.GetFilename())
	}
	// The manifest names it; every stream is a video saved as ".mp4".
	d3 := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: "https://cdn.test/radio/show-42/index.m3u8", StartPaused: true})
	if d3.GetCategory() != "Video" || d3.GetFilename() != "show-42.mp4" || d3.GetDir() != filepath.Join(h.dir, "Video") ||
		d3.GetMedia().GetContainer() != "mp4" {
		t.Errorf("named by the manifest = %v", d3)
	}
	// Without category folders the default directory is used.
	h2 := newHarness(t, harnessOptions{media: newFakeMedia(1), settings: func(s *downexv1.Settings) { s.UseCategoryFolders = false }})
	if d := h2.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest, StartPaused: true}); d.GetDir() != h2.dir ||
		d.GetFilename() != "big-buck-bunny.mp4" {
		t.Errorf("no category folders = %v", d)
	}
}

func TestAddMediaRefusals(t *testing.T) {
	fm := newFakeMedia(1)
	h := newHarness(t, harnessOptions{media: fm})
	ctx := ctxT(t)
	add := func(req *downexv1.AddMediaRequest) error {
		_, err := h.dl.AddMedia(ctx, connect.NewRequest(req))
		return err
	}
	// Invalid input is refused before anything is fetched.
	for name, req := range map[string]*downexv1.AddMediaRequest{
		"no URL":         {},
		"ftp URL":        {ManifestUrl: "ftp://x.test/a.m3u8"},
		"header CRLF":    {ManifestUrl: testManifest, Headers: []*downexv1.Header{{Name: "X", Value: "a\r\nb"}}},
		"forbidden hdr":  {ManifestUrl: testManifest, Headers: []*downexv1.Header{{Name: "Range", Value: "bytes=0-"}}},
		"relative dir":   {ManifestUrl: testManifest, Dir: "rel"},
		"filename slash": {ManifestUrl: testManifest, Filename: "a/b"},
		"long title":     {ManifestUrl: testManifest, Title: strings.Repeat("t", 513)},
		"bad page URL":   {ManifestUrl: testManifest, PageUrl: "javascript:alert(1)"},
		"bad track id":   {ManifestUrl: testManifest, VideoTrack: "v\n0"},
	} {
		if err := add(req); connectCode(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if probes, _ := fm.calls(); len(probes) != 0 {
		t.Fatalf("invalid requests were probed: %+v", probes)
	}

	netErr := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	for _, tc := range []struct {
		name  string
		err   error
		probe func(p *MediaProbe)
		code  connect.Code
		want  string
	}{
		{"encrypted", fmt.Errorf("parse playlist: %w", media.ErrEncrypted), nil, connect.CodeInvalidArgument, CodeEncrypted},
		{"live", fmt.Errorf("playlist: %w", media.ErrLive), nil, connect.CodeInvalidArgument, CodeLive},
		{"malformed", fmt.Errorf("x: %w", media.ErrMalformed), nil, connect.CodeInvalidArgument, CodeMediaInvalid},
		{"unknown track", fmt.Errorf("x: %w", media.ErrNotFound), nil, connect.CodeInvalidArgument, CodeMediaInvalid},
		{"unsupported", fmt.Errorf("x: %w", media.ErrUnsupported), nil, connect.CodeInvalidArgument, CodeMediaUnsupported},
		{"network", fmt.Errorf("fetch manifest: %w", netErr), nil, connect.CodeFailedPrecondition, CodeNetwork},
		{"http status", &engine.StatusError{Code: 403}, nil, connect.CodeFailedPrecondition, CodeHTTPStatus},
		{"webm", nil, func(p *MediaProbe) { p.Container = "webm" }, connect.CodeInvalidArgument, CodeMediaUnsupported},
		{"ts", nil, func(p *MediaProbe) { p.Container = "ts" }, connect.CodeInvalidArgument, CodeMediaUnsupported},
		{"audio only", nil, func(p *MediaProbe) { p.DefaultVideo = "" }, connect.CodeInvalidArgument, CodeMediaUnsupported},
		{"no tracks", nil, func(p *MediaProbe) { p.DefaultVideo, p.DefaultAudio = "", "" }, connect.CodeInvalidArgument, CodeMediaInvalid},
		{"audio as video", nil, func(p *MediaProbe) { p.DefaultVideo = "a0" }, connect.CodeInvalidArgument, CodeMediaInvalid},
		{"muxed plus audio", nil, func(p *MediaProbe) { p.DefaultVideo = "m0" }, connect.CodeInvalidArgument, CodeMediaInvalid},
	} {
		fm.set(func(f *fakeMedia) {
			f.probeErr = tc.err
			f.probe = newFakeMedia(1).probe
			if tc.probe != nil {
				tc.probe(&f.probe)
			}
		})
		err := add(&downexv1.AddMediaRequest{ManifestUrl: testManifest, Source: downexv1.AddSource_ADD_SOURCE_BROWSER})
		if connectCode(err) != tc.code || mediaCode(t, err) != tc.want {
			t.Errorf("%s: %v (code %v), want %v %s", tc.name, err, connectCode(err), tc.code, tc.want)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: the error repeats the manifest URL: %v", tc.name, err)
		}
	}
	// ProbeMedia refuses the same way.
	fm.set(func(f *fakeMedia) { f.probeErr = fmt.Errorf("x: %w", media.ErrEncrypted) })
	_, err := h.dl.ProbeMedia(ctx, connect.NewRequest(&downexv1.ProbeMediaRequest{ManifestUrl: testManifest}))
	if connectCode(err) != connect.CodeInvalidArgument || mediaCode(t, err) != CodeEncrypted {
		t.Errorf("probe encrypted: %v", err)
	}
	if list := h.list(); len(list) != 0 {
		t.Fatalf("refused adds stored downloads: %v", list)
	}

	// Without media jobs the RPCs are unimplemented.
	h2 := newHarness(t, harnessOptions{})
	if _, err := h2.dl.AddMedia(ctx, connect.NewRequest(&downexv1.AddMediaRequest{ManifestUrl: testManifest})); connectCode(err) != connect.CodeUnimplemented {
		t.Errorf("AddMedia without media jobs: %v", err)
	}
	if _, err := h2.dl.ProbeMedia(ctx, connect.NewRequest(&downexv1.ProbeMediaRequest{ManifestUrl: testManifest})); connectCode(err) != connect.CodeUnimplemented {
		t.Errorf("ProbeMedia without media jobs: %v", err)
	}
}

func TestProbeMedia(t *testing.T) {
	fm := newFakeMedia(7)
	h := newHarness(t, harnessOptions{media: fm})
	res, err := h.dl.ProbeMedia(ctxT(t), connect.NewRequest(&downexv1.ProbeMediaRequest{
		ManifestUrl: testManifest, Headers: []*downexv1.Header{{Name: "User-Agent", Value: "UA/1"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	mi := res.Msg.GetMedia()
	if mi.GetManifestUrl() != testManifest || mi.GetFormat() != "hls" || len(mi.GetTracks()) != 6 || mi.GetSelectedVideo() != "v1" ||
		mi.GetSelectedAudio() != "a1" || mi.GetDurationMs() != 90_000 || mi.GetFragmentsTotal() != 7 || mi.GetContainer() != "mp4" {
		t.Fatalf("probe = %v", mi)
	}
	if probes, runs := fm.calls(); len(probes) != 1 || probes[0].Headers.Get("User-Agent") != "UA/1" || len(runs) != 0 {
		t.Errorf("probes %+v runs %+v", probes, runs)
	}
	if list := h.list(); len(list) != 0 {
		t.Fatalf("ProbeMedia added downloads: %v", list)
	}
}

func TestMediaBrowserConfirmation(t *testing.T) {
	fm := newFakeMedia(3)
	h := newHarness(t, harnessOptions{media: fm})
	d := h.addMedia(&downexv1.AddMediaRequest{
		ManifestUrl: testManifest, Source: downexv1.AddSource_ADD_SOURCE_BROWSER, Title: "Episode 1", StartPaused: true,
	})
	videoDir := filepath.Join(h.dir, "Video")
	if d.GetState() != stAwaiting || d.GetFilename() != "Episode 1.mp4" || d.GetDir() != videoDir {
		t.Fatalf("browser media add = %v", d)
	}
	if _, err := os.Lstat(videoDir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the category folder was created before the confirmation: %v", err)
	}
	ctx := ctxT(t)
	if _, err := h.dl.Pause(ctx, connect.NewRequest(&downexv1.PauseRequest{Id: d.GetId()})); connectCode(err) != connect.CodeFailedPrecondition {
		t.Errorf("pause while awaiting: %v", err)
	}
	for name, req := range map[string]*downexv1.ConfirmRequest{
		"unknown video":  {Id: d.GetId(), VideoTrack: "v9", Start: true},
		"audio as video": {Id: d.GetId(), VideoTrack: "a0", Start: true},
		"video as audio": {Id: d.GetId(), AudioTrack: "v0", Start: true},
		"muxed + audio":  {Id: d.GetId(), VideoTrack: "m0", AudioTrack: "a0", Start: true},
		"subtitles":      {Id: d.GetId(), AudioTrack: "s0", Start: true},
		"bad id":         {Id: d.GetId(), VideoTrack: strings.Repeat("x", 2000), Start: true},
	} {
		if _, err := h.confirmReq(req); connectCode(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if got := h.get(d.GetId()); got.GetState() != stAwaiting || got.GetMedia().GetSelectedVideo() != "v1" {
		t.Fatalf("refused confirmations changed the download: %v", got)
	}
	// A muxed track drops the separate audio; the new name gets the extension.
	c, err := h.confirmReq(&downexv1.ConfirmRequest{Id: d.GetId(), VideoTrack: "m0", Filename: "Folge 1", Start: false})
	if err != nil {
		t.Fatal(err)
	}
	if c.GetState() != stPaused || c.GetFilename() != "Folge 1.mp4" || c.GetMedia().GetSelectedVideo() != "m0" ||
		c.GetMedia().GetSelectedAudio() != "" || c.GetMedia().GetFragmentsTotal() != 0 {
		t.Fatalf("confirmed = %v", c)
	}
	if _, runs := fm.calls(); len(runs) != 0 {
		t.Fatalf("a download confirmed for later ran: %+v", runs)
	}

	// Another one: a video track gets the best audio when none is chosen; then it runs.
	d2 := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest, Source: downexv1.AddSource_ADD_SOURCE_BROWSER})
	fm.set(func(f *fakeMedia) { f.probe.DefaultAudio = "" }) // (only affects later probes)
	c2, err := h.confirmReq(&downexv1.ConfirmRequest{Id: d2.GetId(), VideoTrack: "v0", AudioTrack: "a0", Start: true})
	if err != nil {
		t.Fatal(err)
	}
	if c2.GetMedia().GetSelectedVideo() != "v0" || c2.GetMedia().GetSelectedAudio() != "a0" {
		t.Fatalf("confirmed = %v", c2)
	}
	h.waitCompleted(d2.GetId())
	if _, runs := fm.calls(); len(runs) != 1 || runs[0].sel.VideoTrack != "v0" || runs[0].sel.AudioTrack != "a0" {
		t.Fatalf("runs = %+v", runs)
	}
	sel := &model.Download{Media: &model.Media{Tracks: []model.MediaTrack{{ID: "v", Kind: "video"}, {ID: "a", Kind: "audio", Bandwidth: 1},
		{ID: "b", Kind: "audio", Bandwidth: 9}}, SelectedVideo: "v", SelectedAudio: ""}}
	if err := selectMediaTracks(sel, "v", ""); err != nil || sel.Media.SelectedAudio != "b" {
		t.Errorf("best audio: %v %+v", err, sel.Media)
	}
	// A site download's tracks come best first (the original language before a higher bit
	// rate, docs/SITE-MEDIA-SPEC.md §3.11): a video-only pick gets the first audio, as
	// the pickers and sitemedia.Select pair it.
	site := &model.Download{Media: &model.Media{Origin: sitemedia.OriginSite, Tracks: []model.MediaTrack{
		{ID: "m18", Kind: "muxed"}, {ID: "v", Kind: "video"}, {ID: "en", Kind: "audio", Bandwidth: 96_000},
		{ID: "de", Kind: "audio", Bandwidth: 160_000}}, SelectedVideo: "m18"}}
	if err := selectMediaTracks(site, "v", ""); err != nil || site.Media.SelectedAudio != "en" {
		t.Errorf("site audio: %v %+v", err, site.Media)
	}

	// Track ids apply to media downloads only.
	f := h.add(&downexv1.AddRequest{Url: "https://files.test/a.zip", Source: downexv1.AddSource_ADD_SOURCE_BROWSER})
	if f.GetKind() != downexv1.DownloadKind_DOWNLOAD_KIND_FILE || f.GetMedia() != nil {
		t.Errorf("file download = %v", f)
	}
	if _, err := h.confirmReq(&downexv1.ConfirmRequest{Id: f.GetId(), VideoTrack: "v0"}); connectCode(err) != connect.CodeInvalidArgument {
		t.Errorf("confirm a file with a track: %v", err)
	}
}

// TestMediaRunsFindTracksByKey: the keys of the probed tracks are stored with them and
// passed with the selection, the default one or the one confirmed, so that the job
// finds the chosen tracks wherever the manifest lists them when it runs.
func TestMediaRunsFindTracksByKey(t *testing.T) {
	fm := newFakeMedia(2)
	fm.set(func(f *fakeMedia) {
		for i := range f.probe.Tracks {
			f.probe.Tracks[i].Key = "k-" + f.probe.Tracks[i].ID
		}
	})
	h := newHarness(t, harnessOptions{media: fm})
	d := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest, Source: downexv1.AddSource_ADD_SOURCE_CLI})
	h.waitCompleted(d.GetId())
	if rec := storedState(t, h.st, d.GetId()); rec.Media.Track("v0") == nil || rec.Media.Track("v0").Key != "k-v0" {
		t.Fatalf("stored tracks = %+v", rec.Media.Tracks)
	}
	d2 := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest, Source: downexv1.AddSource_ADD_SOURCE_BROWSER})
	if _, err := h.confirmReq(&downexv1.ConfirmRequest{Id: d2.GetId(), VideoTrack: "v0", AudioTrack: "a0", Start: true}); err != nil {
		t.Fatal(err)
	}
	h.waitCompleted(d2.GetId())
	_, runs := fm.calls()
	if len(runs) != 2 {
		t.Fatalf("%d runs, want 2", len(runs))
	}
	for i, want := range []MediaSelection{
		{VideoTrack: "v1", AudioTrack: "a1", VideoKey: "k-v1", AudioKey: "k-a1"},
		{VideoTrack: "v0", AudioTrack: "a0", VideoKey: "k-v0", AudioKey: "k-a0"},
	} {
		got := runs[i].sel
		if got.VideoTrack != want.VideoTrack || got.AudioTrack != want.AudioTrack || got.VideoKey != want.VideoKey || got.AudioKey != want.AudioKey {
			t.Errorf("run %d selection = %+v, want %+v", i, got, want)
		}
	}
}

func TestMediaPauseResumeRestartRemove(t *testing.T) {
	fm := newFakeMedia(10)
	fm.gate = make(chan struct{})
	h := newHarness(t, harnessOptions{media: fm})
	dir := t.TempDir()
	d := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest, Dir: dir, Filename: "movie"})
	out := filepath.Join(dir, "movie.mp4")
	parts := MediaPartsDir(dir, "movie.mp4")

	fm.feed(t, 3)
	live := h.waitFor(d.GetId(), "3 fragments", func(d *downexv1.Download) bool { return d.GetMedia().GetFragmentsDone() >= 3 })
	// The fake reports no byte total: the size is the stored fragments' bytes over the rest
	// (3 of 10), an estimate.
	if live.GetState() != stDownloading || live.GetSize() != int64(10*len(fragData(0))) || !live.GetSizeApprox() ||
		live.GetConnections() != 2 || live.GetSpeedBps() != 1000 ||
		live.GetReceived() != int64(3*len(fragData(0))) || live.GetMedia().GetFragmentsTotal() != 10 {
		t.Errorf("live = %v", live)
	}
	if a := h.m.Activity(); a.Active != 1 || a.SpeedBps != 1000 {
		t.Errorf("activity = %+v", a)
	}
	p := h.pause(d.GetId())
	// Stopped: the bytes of the durable fragments over the rest (3 of 10).
	if p.GetState() != stPaused || p.GetReceived() != int64(3*len(fragData(0))) || p.GetMedia().GetFragmentsDone() != 3 ||
		p.GetSize() != int64(10*len(fragData(0))) || !p.GetSizeApprox() {
		t.Fatalf("paused = %v", p)
	}
	rec := storedState(t, h.st, d.GetId())
	if rec.State != model.StatePaused || rec.Media.Checkpoint == nil || rec.Media.Checkpoint.Tracks[0].Done[0] != 0b111 {
		t.Fatalf("stored after pause = %+v %+v", rec, rec.Media)
	}
	if entries, err := os.ReadDir(parts); err != nil || len(entries) != 3 {
		t.Fatalf("part dir = %v, %v", entries, err)
	}

	// Resume continues from the checkpoint.
	h.resume(&downexv1.ResumeRequest{Id: d.GetId()})
	fm.feed(t, 7)
	h.waitCompleted(d.GetId())
	if got, err := os.ReadFile(out); err != nil || !bytes.Equal(got, wantOutput(10)) {
		t.Fatalf("output %q, %v", got, err)
	}
	_, runs := fm.calls()
	if len(runs) != 2 || runs[1].resume == nil || runs[1].resume.Tracks[0].Done[0] != 0b111 {
		t.Fatalf("runs = %+v", runs)
	}
	// Only a restart downloads a completed media download again (again_test.go).
	if _, err := h.dl.Resume(ctxT(t), connect.NewRequest(&downexv1.ResumeRequest{Id: d.GetId()})); connectCode(err) != connect.CodeFailedPrecondition {
		t.Errorf("resume a completed media download: %v", err)
	}

	// Restart discards the fragments; Remove with delete_file deletes the part directory.
	d2 := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest, Dir: dir, Filename: "second"})
	parts2 := MediaPartsDir(dir, "second.mp4")
	fm.feed(t, 2)
	h.waitFor(d2.GetId(), "2 fragments", func(d *downexv1.Download) bool { return d.GetMedia().GetFragmentsDone() >= 2 })
	h.pause(d2.GetId())
	r := h.resume(&downexv1.ResumeRequest{Id: d2.GetId(), Restart: true})
	if r.GetReceived() != 0 || r.GetMedia().GetFragmentsDone() != 0 {
		t.Errorf("restarted = %v", r)
	}
	fm.feed(t, 1)
	h.waitFor(d2.GetId(), "1 fragment", func(d *downexv1.Download) bool {
		return d.GetState() == stDownloading && d.GetMedia().GetFragmentsDone() == 1
	})
	if _, runs = fm.calls(); runs[len(runs)-1].resume != nil {
		t.Errorf("the restarted run resumed: %+v", runs[len(runs)-1].resume)
	}
	h.pause(d2.GetId())
	if entries, err := os.ReadDir(parts2); err != nil || len(entries) != 1 {
		t.Fatalf("part dir after restart = %v, %v", entries, err)
	}
	if _, err := h.dl.Remove(ctxT(t), connect.NewRequest(&downexv1.RemoveRequest{Id: d2.GetId(), DeleteFile: true})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(parts2); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("part dir kept after remove: %v", err)
	}

	// Remove without delete_file deletes the fragments too: nothing resumes them without
	// the record (amendment 2026-11-19); a file of the user's beside them stays.
	d3 := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest, Dir: dir, Filename: "third"})
	fm.feed(t, 1)
	h.waitFor(d3.GetId(), "1 fragment", func(d *downexv1.Download) bool { return d.GetMedia().GetFragmentsDone() >= 1 })
	mine := filepath.Join(dir, "third.mp4.notes")
	if err := os.WriteFile(mine, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.dl.Remove(ctxT(t), connect.NewRequest(&downexv1.RemoveRequest{Id: d3.GetId()})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(MediaPartsDir(dir, "third.mp4")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("part dir kept after remove without delete_file: %v", err)
	}
	if _, err := os.Stat(mine); err != nil {
		t.Errorf("a user file went with the remove: %v", err)
	}
}

func TestMediaFailureCodes(t *testing.T) {
	st := store.NewMemory()
	fm := newFakeMedia(4)
	h := newHarness(t, harnessOptions{media: fm, st: st})
	codes := make(map[int64]string)
	for _, tc := range []struct {
		err  error
		code string
	}{
		{fmt.Errorf("init segment: %w", media.ErrEncrypted), CodeEncrypted},
		{fmt.Errorf("assemble: %w", ErrMediaUnsupported), CodeMediaUnsupported},
		{fmt.Errorf("remux: %w", errors.ErrUnsupported), CodeMediaUnsupported},
		{fmt.Errorf("fragment 3: %w", &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset by peer")}), CodeNetwork},
	} {
		fm.set(func(f *fakeMedia) { f.runErr, f.failAfter = tc.err, 2 })
		d := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest, Dir: h.dir, Filename: tc.code})
		codes[d.GetId()] = tc.code
		got := h.waitState(d.GetId(), stFailed)
		if got.GetErrorCode() != tc.code || got.GetError() == "" || got.GetMedia().GetFragmentsDone() != 2 ||
			got.GetReceived() != int64(2*len(fragData(0))) {
			t.Errorf("%s: failed = %v", tc.code, got)
		}
	}
	// The codes survive a restart of the daemon (they are derived from the message).
	ctx := ctxT(t)
	m2, err := New(ctx, Config{Store: st, Transport: h.m.cfg.Transport, DownloadsDir: func() (string, error) { return h.dir, nil },
		Logger: testLogger(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m2.Shutdown(context.Background()) }()
	all, err := m2.List(ctx)
	if err != nil || len(all) != 4 {
		t.Fatalf("list = %v, %v", all, err)
	}
	for _, d := range all {
		if want := codes[d.GetId()]; d.GetErrorCode() != want {
			t.Errorf("after restart: %s has code %q, want %q (%s)", d.GetFilename(), d.GetErrorCode(), want, d.GetError())
		}
	}
}

// A media download that fails with nothing resumable leaves no part directory; one
// that fails midway keeps it for a resume (amendment 2026-11-19).
func TestMediaFailedPartsDir(t *testing.T) {
	fm := newFakeMedia(4)
	h := newHarness(t, harnessOptions{media: fm})
	fm.set(func(f *fakeMedia) { f.runErr, f.failAfter = errors.New("boom"), 0 })
	d := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest, Dir: h.dir, Filename: "none"})
	h.waitState(d.GetId(), stFailed)
	if _, err := os.Lstat(MediaPartsDir(h.dir, "none.mp4")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("part dir of a failure with nothing resumable: %v", err)
	}
	fm.set(func(f *fakeMedia) { f.runErr, f.failAfter = errors.New("boom"), 2 })
	d = h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest, Dir: h.dir, Filename: "half"})
	h.waitState(d.GetId(), stFailed)
	if _, err := os.Stat(MediaPartsDir(h.dir, "half.mp4")); err != nil {
		t.Errorf("part dir of a resumable failure: %v", err)
	}
}

func TestMediaWithoutJobsFails(t *testing.T) {
	st := store.NewMemory()
	dir := t.TempDir()
	// A media download stored by a daemon that had media jobs.
	rec := &model.Download{Request: model.Request{URL: testManifest}, Dir: dir, Filename: "x.mp4", Size: -1,
		State: model.StateQueued, Kind: model.KindMedia, Media: &model.Media{Format: "hls", SelectedVideo: "v0", Container: "mp4"}}
	if err := st.CreateDownload(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, harnessOptions{st: st, dir: dir})
	got := h.waitState(int64(rec.ID), stFailed)
	if got.GetErrorCode() != CodeMediaUnsupported || got.GetKind() != downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA {
		t.Errorf("failed = %v", got)
	}
}

func TestMediaConcurrencyAndRecovery(t *testing.T) {
	st := store.NewMemory()
	fm := newFakeMedia(3)
	fm.gate = make(chan struct{})
	dir := t.TempDir()
	h := newHarness(t, harnessOptions{media: fm, st: st, dir: dir, settings: func(s *downexv1.Settings) {
		s.MaxConcurrentDownloads = 1
	}})
	a := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest, Dir: dir, Filename: "a"})
	b := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest, Dir: dir, Filename: "b"})
	fm.feed(t, 1)
	h.waitFor(a.GetId(), "a fragment", func(d *downexv1.Download) bool { return d.GetMedia().GetFragmentsDone() == 1 })
	if got := h.get(b.GetId()); got.GetState() != stQueued || got.GetQueuePosition() != 1 {
		t.Fatalf("second download = %v, want queued behind the first", got)
	}
	fm.feed(t, 2)
	h.waitCompleted(a.GetId())
	fm.feed(t, 1)
	h.waitFor(b.GetId(), "b fragment", func(d *downexv1.Download) bool { return d.GetMedia().GetFragmentsDone() == 1 })
	fm.set(func(f *fakeMedia) {
		if f.maxActive != 1 {
			t.Errorf("%d media runs at once, want 1", f.maxActive)
		}
	})

	// Shutdown pauses b, commits its checkpoint and stores it queued; a new daemon
	// resumes it from there.
	if err := h.m.Shutdown(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	if rec := storedState(t, st, b.GetId()); rec.State != model.StateQueued || rec.Media.Checkpoint == nil {
		t.Fatalf("after shutdown: %+v", rec)
	}
	fm2 := newFakeMedia(3)
	h2 := newHarness(t, harnessOptions{media: fm2, st: st, dir: dir})
	h2.waitCompleted(b.GetId())
	if got, err := os.ReadFile(filepath.Join(dir, "b.mp4")); err != nil || !bytes.Equal(got, wantOutput(3)) {
		t.Fatalf("output %q, %v", got, err)
	}
	if _, runs := fm2.calls(); len(runs) != 1 || runs[0].resume == nil || runs[0].resume.Tracks[0].Done[0] != 0b1 {
		t.Fatalf("recovered runs = %+v", runs)
	}

	// A download left "downloading" by a crash is requeued and resumed too.
	crashed := &model.Download{Request: model.Request{URL: testManifest}, Dir: dir, Filename: "c.mp4", Size: -1,
		State: model.StateDownloading, Kind: model.KindMedia, Media: &model.Media{Format: "hls", SelectedVideo: "v1",
			Container: "mp4", FragmentsTotal: 3, FragmentsDone: 1, BytesDone: 5,
			Checkpoint: &model.MediaCheckpoint{Tracks: []model.MediaTrackCheckpoint{{TrackID: "v1", Total: 3, Done: []byte{0b10}}}}}}
	if err := st.CreateDownload(context.Background(), crashed); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(MediaPartsDir(dir, "c.mp4"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(MediaPartsDir(dir, "c.mp4"), "1.frag"), fragData(1), 0o644); err != nil {
		t.Fatal(err)
	}
	h3 := newHarness(t, harnessOptions{media: newFakeMedia(3), st: st, dir: dir})
	h3.waitCompleted(int64(crashed.ID))
	if got, err := os.ReadFile(filepath.Join(dir, "c.mp4")); err != nil || !bytes.Equal(got, wantOutput(3)) {
		t.Fatalf("output %q, %v", got, err)
	}
}

func TestMediaNameTakenAtStart(t *testing.T) {
	fm := newFakeMedia(1)
	h := newHarness(t, harnessOptions{media: fm})
	dir := t.TempDir()
	d := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest, Dir: dir, Filename: "x", StartPaused: true})
	if err := os.WriteFile(filepath.Join(dir, "x.mp4"), []byte("someone else's"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.resume(&downexv1.ResumeRequest{Id: d.GetId()})
	done := h.waitCompleted(d.GetId())
	if done.GetFilename() != "x (1).mp4" {
		t.Errorf("filename = %q", done.GetFilename())
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "x.mp4")); string(b) != "someone else's" {
		t.Errorf("existing file overwritten: %q", b)
	}
}

// legacyRecord stores a media download as a daemon before 2026-10-23 stored an MPEG-TS
// stream: container "ts", a ".ts" name, in state. With resumable, its checkpoint has
// fragment 0 of 3 durable, and that fragment is in its part folder.
func legacyRecord(t *testing.T, st store.Store, dir, name string, state model.State, resumable bool) *model.Download {
	t.Helper()
	rec := &model.Download{Request: model.Request{URL: testManifest}, Dir: dir, Filename: name, Size: -1, State: state,
		Kind: model.KindMedia, Category: "Video", Media: &model.Media{Format: "hls", SelectedVideo: "v1", Container: "ts",
			FragmentsTotal: 3, Tracks: []model.MediaTrack{{ID: "v1", Kind: "muxed"}}}}
	if resumable {
		rec.Media.FragmentsDone, rec.Media.BytesDone = 1, int64(len(fragData(0)))
		rec.Media.Checkpoint = &model.MediaCheckpoint{Tracks: []model.MediaTrackCheckpoint{{TrackID: "v1", Total: 3, Done: []byte{0b1}}}}
		parts := MediaPartsDir(dir, name)
		if err := os.MkdirAll(parts, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(parts, "0.frag"), fragData(0), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CreateDownload(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

// A download stored as MPEG-TS before 2026-10-23 that has a checkpoint resumes as one:
// LegacyTS, its ".ts" name and part folder, container "ts". One without a checkpoint
// (never started, or restarted) has nothing on disk yet and becomes an MP4 download
// under a ".mp4" name that is free (docs/VIDEO-DETECT-SPEC.md §3.3).
func TestMediaLegacyTS(t *testing.T) {
	st := store.NewMemory()
	dir := t.TempDir()
	resumed := legacyRecord(t, st, dir, "show.ts", model.StateQueued, true)
	fresh := legacyRecord(t, st, dir, "old.ts", model.StatePaused, false)
	paused := legacyRecord(t, st, dir, "later.ts", model.StatePaused, true)
	if err := os.WriteFile(filepath.Join(dir, "old.mp4"), []byte("someone else's"), 0o644); err != nil {
		t.Fatal(err)
	}
	fm := newFakeMedia(3) // "ts" for a LegacyTS run, else "mp4", as the real job
	h := newHarness(t, harnessOptions{media: fm, st: st, dir: dir})
	done := h.waitCompleted(int64(resumed.ID))
	if done.GetFilename() != "show.ts" || done.GetMedia().GetContainer() != "ts" {
		t.Fatalf("resumed legacy download = %v", done)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "show.ts")); err != nil || !bytes.Equal(got, wantOutput(3)) {
		t.Fatalf("legacy output %q, %v", got, err)
	}
	if _, err := os.Lstat(MediaPartsDir(dir, "show.ts")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("legacy part folder kept: %v", err)
	}

	h.resume(&downexv1.ResumeRequest{Id: int64(fresh.ID)})
	done = h.waitCompleted(int64(fresh.ID))
	if done.GetFilename() != "old (1).mp4" || done.GetMedia().GetContainer() != "mp4" {
		t.Fatalf("legacy download without a checkpoint = %v", done)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "old (1).mp4")); err != nil || !bytes.Equal(got, wantOutput(3)) {
		t.Fatalf("converted output %q, %v", got, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "old.mp4")); string(b) != "someone else's" {
		t.Errorf("an existing file was overwritten: %q", b)
	}

	// A restart discards the checkpoint: the record runs as an MP4 download.
	h.resume(&downexv1.ResumeRequest{Id: int64(paused.ID), Restart: true})
	done = h.waitCompleted(int64(paused.ID))
	if done.GetFilename() != "later.mp4" || done.GetMedia().GetContainer() != "mp4" {
		t.Fatalf("restarted legacy download = %v", done)
	}

	_, runs := fm.calls()
	if len(runs) != 3 {
		t.Fatalf("%d runs, want 3", len(runs))
	}
	for i, want := range []struct {
		legacy bool
		out    string
		resume bool
	}{{true, "show.ts", true}, {false, "old (1).mp4", false}, {false, "later.mp4", false}} {
		r := runs[i]
		if r.opts.LegacyTS != want.legacy || r.opts.OutPath != filepath.Join(dir, want.out) ||
			r.opts.PartDir != MediaPartsDir(dir, want.out) || (r.resume != nil) != want.resume {
			t.Errorf("run %d: legacy %v, out %s, parts %s, resume %v; want %+v", i, r.opts.LegacyTS, r.opts.OutPath,
				r.opts.PartDir, r.resume != nil, want)
		}
	}
	for _, id := range []model.DownloadID{fresh.ID, paused.ID} {
		if rec := storedState(t, st, int64(id)); rec.Media.Container != "mp4" || filepath.Ext(rec.Filename) != ".mp4" {
			t.Errorf("stored %s: container %q", rec.Filename, rec.Media.Container)
		}
	}
}

// A run that is not a legacy one must produce an MP4; anything else fails with
// media_unsupported and leaves neither the file nor its part folder. A legacy run must
// produce what it was planned as, a ".ts".
func TestMediaRunMustBeMP4(t *testing.T) {
	st := store.NewMemory()
	dir := t.TempDir()
	fm := newFakeMedia(2)
	fm.set(func(f *fakeMedia) { f.container = "ts" })
	h := newHarness(t, harnessOptions{media: fm, st: st, dir: dir})
	d := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest, Dir: dir, Filename: "clip"})
	got := h.waitState(d.GetId(), stFailed)
	if got.GetErrorCode() != CodeMediaUnsupported || got.GetFilename() != "clip.mp4" {
		t.Fatalf("failed = %v", got)
	}
	for _, p := range []string{filepath.Join(dir, "clip.mp4"), MediaPartsDir(dir, "clip.mp4")} {
		if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s left behind: %v", p, err)
		}
	}

	st2, dir2 := store.NewMemory(), t.TempDir()
	legacy := legacyRecord(t, st2, dir2, "show.ts", model.StatePaused, true)
	fm2 := newFakeMedia(3)
	fm2.set(func(f *fakeMedia) { f.container = "mp4" })
	h2 := newHarness(t, harnessOptions{media: fm2, st: st2, dir: dir2})
	h2.resume(&downexv1.ResumeRequest{Id: int64(legacy.ID)})
	if got := h2.waitState(int64(legacy.ID), stFailed); got.GetErrorCode() != CodeMediaUnsupported {
		t.Fatalf("legacy run saved as mp4 = %v", got)
	}
	if _, err := os.Lstat(filepath.Join(dir2, "show.ts")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("an MP4 was left under the .ts name: %v", err)
	}
}

func TestMediaFileName(t *testing.T) {
	long := strings.Repeat("ü", 300)
	for _, c := range []struct {
		requested, title, url, container string
		want                             string
	}{
		{"clip", "", testManifest, "mp4", "clip.mp4"},
		{"clip.mp4", "", testManifest, "mp4", "clip.mp4"},
		{"clip.MKV", "", testManifest, "ts", "clip.ts"},
		{"report.v2", "", testManifest, "mp4", "report.v2.mp4"},
		{"", "A/B: C?", testManifest, "mp4", "A_B_ C_.mp4"},
		{"", "", testManifest, "mp4", "big-buck-bunny.mp4"},
		{"", "", "https://x.test/video/12345/index.m3u8", "ts", "12345.ts"},
		{"", "", "https://x.test/manifest.mpd", "mp4", "manifest.mp4"},
		{"", "", "https://x.test/", "mp4", "video.mp4"},
		{"", "", "https://x.test/My%20Show/playlist.m3u8", "mp4", "My Show.mp4"},
		{"", "", "https://x.test/a/b/c/d/index.m3u8", "mp4", "d.mp4"},
		{".mp4", "", testManifest, "mp4", "mp4.mp4"},
		{"CON", "", testManifest, "mp4", "_CON.mp4"},
	} {
		if got := mediaFileName(c.requested, c.title, c.url, c.container); got != c.want {
			t.Errorf("mediaFileName(%q, %q, %q, %q) = %q, want %q", c.requested, c.title, c.url, c.container, got, c.want)
		}
	}
	if got := mediaFileName(long, "", testManifest, "mp4"); len(got) > 240-12+len(".mp4") || !strings.HasSuffix(got, ".mp4") {
		t.Errorf("long name = %q (%d bytes)", got, len(got))
	}
}

func TestCheckpointCounts(t *testing.T) {
	cp := &model.MediaCheckpoint{Tracks: []model.MediaTrackCheckpoint{
		{TrackID: "v", Total: 10, Done: []byte{0xff, 0xff, 0xff}}, // bits beyond Total ignored
		{TrackID: "a", Total: 3, Done: []byte{0b101}},
		{TrackID: "x", Total: 0, Done: nil},
	}}
	if total, done := checkpointCounts(cp); total != 13 || done != 12 {
		t.Errorf("checkpointCounts = %d, %d; want 13, 12", total, done)
	}
}

// TestMediaErrorCodeMapping covers the media codes of mediaErrorCode and
// codeFromMessage, and that ErrorCode (file downloads) keeps its own.
func TestMediaErrorCodeMapping(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("a: %w", media.ErrEncrypted), CodeEncrypted},
		{fmt.Errorf("a: %w", media.ErrLive), CodeLive},
		{fmt.Errorf("a: %w", media.ErrTooLarge), CodeMediaInvalid},
		{fmt.Errorf("a: %w", errNoMediaJobs), CodeMediaUnsupported},
		{&engine.StatusError{Code: 404}, CodeHTTPStatus},
		{errors.New("boom"), CodeInternal},
	} {
		if got := mediaErrorCode(c.err); got != c.want {
			t.Errorf("mediaErrorCode(%v) = %q, want %q", c.err, got, c.want)
		}
		if got := codeFromMessage(c.err.Error()); got != c.want {
			t.Errorf("codeFromMessage(%q) = %q, want %q", c.err.Error(), got, c.want)
		}
	}
	// The job's own errors, known by their text.
	for msg, want := range map[string]string{
		"mediajob: unexpected HTTP status 403 Forbidden":                         CodeHTTPStatus,
		"fragment 7: mediajob: response ended early":                             CodeNetwork,
		"mediajob: server answered with a different byte range":                  CodeHTTPStatus,
		"fragment 3: mediajob: response has a content coding: gzip":              CodeHTTPStatus,
		"mediajob: response has a content coding: media: unsupported operation":  CodeMediaUnsupported,
		"mediajob: invalid track selection: audio with a muxed track":            CodeMediaInvalid,
		"mediajob: not an HLS playlist or DASH MPD":                              CodeMediaInvalid,
		"remux: unsupported operation: TS video with separate TS audio":          CodeMediaUnsupported,
		"mediajob: invalid track selection: media: track not found: no track v9": CodeMediaInvalid,
		"track video-137: mediajob: disk full: 80 MB free, 412 MB needed":        CodeDiskFull,
	} {
		if got := mediaErrorCode(errors.New(msg)); got != want {
			t.Errorf("mediaErrorCode(%q) = %q, want %q", msg, got, want)
		}
		if got := recordErrorCode(&model.Download{Kind: model.KindMedia, Error: msg}); got != want {
			t.Errorf("recordErrorCode(media, %q) = %q, want %q", msg, got, want)
		}
	}
	if got := recordErrorCode(&model.Download{Error: "remux: unsupported operation"}); got != CodeInternal {
		t.Errorf("file download with an unsupported operation = %q", got)
	}
	if got := runErrorCode(model.KindFile, fmt.Errorf("x: %w", errors.ErrUnsupported)); got != CodeInternal {
		t.Errorf("file download with ErrUnsupported = %q", got)
	}
	if got := mediaErrorCode(nil); got != "" {
		t.Errorf("nil = %q", got)
	}
}

// TestMediaInfoOrigin: a manifest's media keeps the manifest origin (the zero value every
// client before site media knows), reports its page hint as page_url, keeps the sizes the
// probe found for its tracks and never carries the site-only file type.
func TestMediaInfoOrigin(t *testing.T) {
	fm := newFakeMedia(1)
	fm.probe.Tracks[1].Size, fm.probe.Tracks[1].SizeApprox, fm.probe.Tracks[1].Ext = 5000, true, "mp4" // the type is ignored
	fm.probe.Tracks[2].SizeApprox = true                                                               // no size: no estimate
	h := newHarness(t, harnessOptions{media: fm})
	d := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest, PageUrl: "https://site.test/watch", StartPaused: true})
	mi := d.GetMedia()
	if mi.GetOrigin() != downexv1.MediaOrigin_MEDIA_ORIGIN_UNSPECIFIED || mi.GetManifestUrl() != testManifest ||
		mi.GetPageUrl() != "https://site.test/watch" || mi.GetSite() != "" || d.GetExpectedSize() != 0 {
		t.Fatalf("media = %v", mi)
	}
	if tr := mi.GetTracks()[1]; tr.GetSize() != 5000 || tr.GetExt() != "" || !tr.GetSizeApprox() {
		t.Errorf("track = %v", tr)
	}
	if tr := mi.GetTracks()[2]; tr.GetSize() != 0 || tr.GetSizeApprox() {
		t.Errorf("track without a size = %v", tr)
	}
	if rec := storedState(t, h.st, d.GetId()); rec.Media.Origin != "" || rec.Media.Tracks[1].Size != 5000 ||
		!rec.Media.Tracks[1].SizeApprox || rec.Media.Tracks[1].Ext != "" {
		t.Errorf("stored = %+v", rec.Media)
	}
	// An explicit MANIFEST origin is the same.
	d = h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest, Origin: downexv1.MediaOrigin_MEDIA_ORIGIN_MANIFEST,
		StartPaused: true})
	if d.GetMedia().GetOrigin() != downexv1.MediaOrigin_MEDIA_ORIGIN_UNSPECIFIED || d.GetUrl() != testManifest {
		t.Errorf("manifest origin = %v", d)
	}
}
