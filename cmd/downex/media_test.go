package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"downex/internal/daemon"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/sitemedia"
)

const testManifest = "https://cdn.test/show/master.m3u8?token=t"

// sampleProbe is the fake daemon's ProbeMedia answer: tracks in no particular order.
func sampleProbe() *downexv1.MediaInfo {
	return &downexv1.MediaInfo{
		ManifestUrl: testManifest, Format: "hls", DurationMs: 3_725_000, Container: "mp4", FragmentsTotal: 620,
		SelectedVideo: "v1", SelectedAudio: "a1",
		Tracks: []*downexv1.MediaTrack{
			{Id: "a0", Kind: "audio", Bandwidth: 64_000, Codecs: "mp4a.40.5", Language: "de", Name: "Deutsch"},
			{Id: "v0", Kind: "video", Bandwidth: 800_000, Width: 640, Height: 360, FrameRate: 25, Codecs: "avc1.4d401e"},
			{Id: "s0", Kind: "subtitles", Language: "en"},
			{Id: "v1", Kind: "video", Bandwidth: 5_000_000, Width: 1920, Height: 1080, FrameRate: 29.97, Codecs: "avc1.640028"},
			{Id: "a1", Kind: "audio", Bandwidth: 128_000, Codecs: "mp4a.40.2", Language: "en", Name: "English"},
		},
	}
}

func TestMediaList(t *testing.T) {
	f, h := newFakeDaemon(t)
	ctx := context.Background()
	r := h.run(ctx, "media", testManifest, "--list", "-H", "Cookie: s=1")
	if r.code != exitOK {
		t.Fatalf("media --list: %v", r)
	}
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if len(lines) != 8 || lines[0] != "Format: HLS  Duration: 1h02m  Saved as: .mp4  Fragments: 620" ||
		!strings.Contains(lines[1], "QUALITY") || !strings.Contains(lines[1], "PICTURE") || strings.Contains(lines[1], "NAME") ||
		!strings.HasPrefix(lines[2], "* v1") || !strings.Contains(lines[2], " 1080p (Full HD) ") || !strings.Contains(lines[2], " 1920x1080 29.97fps ") ||
		!strings.Contains(lines[2], "5.0 Mbit/s") || !strings.HasPrefix(lines[3], "  v0") || !strings.Contains(lines[3], " 360p ") ||
		!strings.HasPrefix(lines[4], "  a0") || !strings.Contains(lines[4], " Deutsch ") ||
		!strings.HasPrefix(lines[5], "* a1") || !strings.Contains(lines[5], "128 kbit/s") || !strings.HasPrefix(lines[6], "  s0") {
		t.Errorf("track list:\n%s", r.stdout)
	}
	rec := f.rec()
	f.do(func() {
		if len(f.mediaProbes) != 1 || f.mediaProbes[0].GetManifestUrl() != testManifest || len(f.mediaProbes[0].GetHeaders()) != 1 ||
			f.mediaProbes[0].GetHeaders()[0].GetValue() != "s=1" || len(f.mediaAdds) != 0 {
			t.Errorf("probes %v adds %v", f.mediaProbes, f.mediaAdds)
		}
	})
	if len(rec.adds) != 0 {
		t.Errorf("--list added a download")
	}

	r = h.run(ctx, "media", testManifest, "--list", "--json")
	var v mediaView
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil || v.Format != "hls" || v.Duration != 3725 || len(v.Tracks) != 5 ||
		v.Tracks[0].ID != "v1" || v.SelectedAudio != "a1" || v.FragmentsTotal != 620 {
		t.Errorf("media --list --json: %+v %v\n%s", v, err, r.stdout)
	}
}

func TestMediaAdd(t *testing.T) {
	f, h := newFakeDaemon(t)
	ctx := context.Background()
	dir := t.TempDir()
	r := h.run(ctx, "media", testManifest, "--video", "v0", "--audio", "a0", "-o", dir, "--name", "clip", "--paused",
		"-H", "Referer: https://site.test/")
	if r.code != exitOK || r.stdout != "7\n" ||
		!strings.Contains(r.stderr, "Download 7 paused: clip.mp4 (HLS: video v0 360p + audio a0 (de))") {
		t.Fatalf("media: %v", r)
	}
	var got *downexv1.AddMediaRequest
	f.do(func() { got = f.mediaAdds[0] })
	if got.GetManifestUrl() != testManifest || got.GetVideoTrack() != "v0" || got.GetAudioTrack() != "a0" || got.GetDir() != dir ||
		got.GetFilename() != "clip" || !got.GetStartPaused() || got.GetSource() != downexv1.AddSource_ADD_SOURCE_CLI ||
		len(got.GetHeaders()) != 1 || got.GetHeaders()[0].GetName() != "Referer" {
		t.Errorf("AddMediaRequest = %v", got)
	}
	// Defaults: no tracks, folder or name; -o is sent absolute.
	if r = h.run(ctx, "media", testManifest, "-o", "rel"); r.code != exitOK || !strings.Contains(r.stderr, "queued, position 1") {
		t.Fatalf("media: %v", r)
	}
	f.do(func() { got = f.mediaAdds[1] })
	if got.GetVideoTrack() != "" || got.GetAudioTrack() != "" || got.GetDir() != mustAbs(t, "rel") || got.GetStartPaused() {
		t.Errorf("AddMediaRequest = %v", got)
	}
}

func TestMediaArgumentErrors(t *testing.T) {
	f, h := newFakeDaemon(t)
	ctx := context.Background()
	for _, tc := range []struct {
		args []string
		out  string
	}{
		{[]string{"media"}, "expects exactly one manifest URL"},
		{[]string{"media", "a", "b"}, "expects exactly one manifest URL"},
		{[]string{"media", testManifest, "--list", "--video", "v0"}, "--list cannot be combined with --video"},
		{[]string{"media", testManifest, "--list", "--wait"}, "--list cannot be combined with --wait"},
		{[]string{"media", testManifest, "--json"}, "--json applies only with --list"},
		{[]string{"media", testManifest, "--paused", "--wait"}, "cannot be combined"},
		{[]string{"media", testManifest, "--name", ""}, "--name must not be empty"},
		{[]string{"media", testManifest, "-H", "bad"}, `want "Name: value"`},
	} {
		r := h.run(ctx, tc.args...)
		if r.code != exitError || !strings.Contains(r.stdout+r.stderr, tc.out) {
			t.Errorf("downex %q: %v\nwant exit 1 and %q", tc.args, r, tc.out)
		}
	}
	if dials, _ := h.recorded(); len(dials) != 0 {
		t.Errorf("bad arguments reached the daemon: %d dials", len(dials))
	}

	// Refusals of the daemon are explained.
	for code, want := range map[string]string{
		"encrypted": "encrypted or DRM-protected; Downex does not download protected content",
		"live":      "this is a live stream",
		"network":   "media: network: cannot fetch the manifest: connection refused",
	} {
		ce := connect.NewError(connect.CodeInvalidArgument, errors.New(code+": "+strings.TrimPrefix(want, "media: "+code+": ")))
		if code == "network" {
			ce = connect.NewError(connect.CodeFailedPrecondition, errors.New("network: cannot fetch the manifest: connection refused"))
		}
		ce.Meta().Set(errorCodeMeta, code)
		f.do(func() { f.mediaErr = ce })
		for _, args := range [][]string{{"media", testManifest}, {"media", testManifest, "--list"}} {
			r := h.run(ctx, args...)
			if r.code != exitError || !strings.Contains(r.stderr, want) || r.stdout != "" {
				t.Errorf("%s %q: %v", code, args, r)
			}
		}
	}
}

func TestMediaFollow(t *testing.T) {
	f, h := newFakeDaemon(t)
	dir := t.TempDir()
	running := func(done int32) *downexv1.Download {
		mi := sampleProbe()
		mi.FragmentsDone = done
		return &downexv1.Download{Id: 7, Url: testManifest, Dir: dir, Filename: "stream.mp4", Size: -1, Received: int64(done) << 20,
			State: downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING, SpeedBps: 2 << 20, Connections: 4,
			Kind: downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA, Media: mi}
	}
	completed := running(620)
	completed.State, completed.Size, completed.Received = downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED, 650<<20, 650<<20
	f.do(func() {
		f.watch = func(ctx context.Context, _ *downexv1.WatchRequest, send func(...*downexv1.Event) error) error {
			if err := send(snapshot(running(155))); err != nil {
				return err
			}
			time.Sleep(10 * time.Millisecond)
			if err := send(updated(completed)); err != nil {
				return err
			}
			<-ctx.Done()
			return nil
		}
	})
	r := h.run(context.Background(), "media", testManifest, "--wait")
	if r.code != exitOK || !strings.Contains(r.stderr, "Saved "+filepath.Join(dir, "stream.mp4")+" (681.6 MB)") {
		t.Fatalf("media --wait: %v", r)
	}
	if line := followLine(running(155)); line != " 25.0%  155/620 fragments  162.5 MB  2.1 MB/s  4 conn" {
		t.Errorf("progress line = %q", line)
	}
	if row := tableRow(4, running(155)); !strings.Contains(row, "25.0%") || !strings.Contains(row, "stream.mp4") {
		t.Errorf("table row = %q", row)
	}

	// A failure of a media download explains its code and keeps no "--restart" advice.
	failed := running(10)
	failed.State, failed.Error, failed.ErrorCode = downexv1.DownloadState_DOWNLOAD_STATE_FAILED, "init segment: encrypted", "encrypted"
	f.do(func() {
		f.watch = func(ctx context.Context, _ *downexv1.WatchRequest, send func(...*downexv1.Event) error) error {
			_ = send(snapshot(failed))
			<-ctx.Done()
			return nil
		}
	})
	r = h.run(context.Background(), "media", testManifest, "--wait")
	if r.code != exitError || !strings.Contains(r.stderr, "Downex does not download protected content") || strings.Contains(r.stderr, "Try again") {
		t.Errorf("failed media --wait: %v", r)
	}
	failed.ErrorCode = "network"
	r = h.run(context.Background(), "media", testManifest, "--wait")
	if r.code != exitError || !strings.Contains(r.stderr, "Its progress (10/620 fragments) is kept.") ||
		!strings.Contains(r.stderr, "Try again with: downex resume 7") {
		t.Errorf("failed media --wait (network): %v", r)
	}

	// A web page's direct video is measured as ls, info, pause and its progress line
	// measure it: by its bytes of an exact size, else by parts, never in fragments; a
	// site's HLS/DASH stream (the manifest fallback) counts fragments like any stream.
	siteFailed := func(format string, size int64, approx bool) *downexv1.Download {
		mi := sampleSiteProbe()
		mi.Format, mi.FragmentsTotal, mi.FragmentsDone = format, 37, 4
		return &downexv1.Download{Id: 7, Url: testPage, Dir: dir, Filename: "A video title.mp4", Size: size, SizeApprox: approx,
			Received: 33_619_968, State: downexv1.DownloadState_DOWNLOAD_STATE_FAILED, Error: "network error",
			ErrorCode: "network", Kind: downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA, Media: mi}
	}
	for _, tc := range []struct {
		name string
		d    *downexv1.Download
		want string
	}{
		{"exact size", siteFailed("http", 316_000_000, false), "Its progress (33.6 MB of 316.0 MB (10.6%) saved) is kept."},
		{"estimated size", siteFailed("http", 330_000_000, true), "Its progress (4/37 parts) is kept."},
		{"unknown size", siteFailed("http", -1, false), "Its progress (4/37 parts) is kept."},
		{"site stream", siteFailed("hls", 330_000_000, true), "Its progress (4/37 fragments) is kept."},
	} {
		f.do(func() { f.probe, f.watch = sampleSiteProbe(), waitScript([]*downexv1.Event{snapshot(tc.d)}) })
		r := h.run(context.Background(), "media", "--site", testPage, "--wait")
		if r.code != exitError || !strings.Contains(r.stderr, "download 7 failed: network error\n"+tc.want+"\n") ||
			!strings.Contains(r.stderr, "Try again with: downex resume 7") {
			t.Errorf("failed site video --wait (%s): %v", tc.name, r)
		}
		if tc.d.GetMedia().GetFormat() == "http" && strings.Contains(r.stderr, "fragments") {
			t.Errorf("a web page's direct video is reported in fragments (%s): %q", tc.name, r.stderr)
		}
	}

	// A video server that refuses Downex even with renewed addresses (403, often bot
	// protection) is explained; trying again is advised only when progress is kept.
	refused := siteFailed("http", 316_000_000, false)
	refused.Error, refused.ErrorCode = "yt-dlp could not read this page: "+sitemedia.ReasonForbidden, "site_error"
	for _, received := range []int64{0, 33_619_968} {
		refused.Received = received
		if received == 0 {
			refused.Media.FragmentsDone = 0
		}
		f.do(func() { f.probe, f.watch = sampleSiteProbe(), waitScript([]*downexv1.Event{snapshot(refused)}) })
		r := h.run(context.Background(), "media", "--site", testPage, "--wait")
		retry := strings.Contains(r.stderr, "Try again with: downex resume 7")
		if r.code != exitError || !strings.Contains(r.stderr, "The site's video server refused Downex") ||
			strings.Contains(r.stderr, "unexpected HTTP status") || retry != (received > 0) {
			t.Errorf("refused site video --wait (received %d): %v", received, r)
		}
	}
}

func TestLsShowsMedia(t *testing.T) {
	f, h := newFakeDaemon(t)
	mi := sampleProbe()
	mi.FragmentsDone = 155
	f.do(func() {
		f.downloads[9] = &downexv1.Download{Id: 9, Url: testManifest, Dir: "/v", Filename: "show.mp4", Size: -1,
			State: downexv1.DownloadState_DOWNLOAD_STATE_PAUSED, Kind: downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA, Media: mi}
		f.downloads[10] = &downexv1.Download{Id: 10, Url: "https://x.test/f.zip", Dir: "/v", Filename: "f.zip", Size: 10,
			State: downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED, Received: 10}
	})
	ctx := context.Background()
	r := h.run(ctx, "ls")
	if r.code != exitOK || !strings.Contains(r.stdout, "25.0%") || !strings.Contains(r.stdout, "show.mp4") {
		t.Errorf("ls:\n%v", r)
	}
	r = h.run(ctx, "ls", "--json")
	var views []downloadView
	if err := json.Unmarshal([]byte(r.stdout), &views); err != nil || len(views) != 2 {
		t.Fatalf("ls --json: %v %v", err, r)
	}
	if v := views[0]; v.Kind != "media" || v.Media == nil || v.Media.FragmentsDone != 155 || v.Media.SelectedVideo != "v1" ||
		len(v.Media.Tracks) != 5 || v.Media.ManifestURL != testManifest {
		t.Errorf("media entry: %+v %+v", v, v.Media)
	}
	if v := views[1]; v.Kind != "file" || v.Media != nil {
		t.Errorf("file entry: %+v", v)
	}
}

// TestMediaLocalCommands covers media records in the database: info shows the stream,
// resume --local refuses (only downexd runs them), rm --local --delete-file deletes the
// part directory too.
func TestMediaLocalCommands(t *testing.T) {
	db := filepath.Join(t.TempDir(), "downex.db")
	dir := t.TempDir()
	st, _, err := openStore(db)
	if err != nil {
		t.Fatal(err)
	}
	d := &model.Download{Request: model.Request{URL: testManifest}, Dir: dir, Filename: "show.mp4", Size: -1,
		State: model.StatePaused, Kind: model.KindMedia, Media: &model.Media{
			Format: "dash", Title: "The Show", Duration: 90 * time.Second, Container: "mp4", SelectedVideo: "720p",
			SelectedAudio: "aac", FragmentsTotal: 40, FragmentsDone: 10, BytesDone: 3 << 20,
			Tracks:     []model.MediaTrack{{ID: "720p", Kind: "video", Width: 1280, Height: 720}, {ID: "aac", Kind: "audio"}},
			Checkpoint: &model.MediaCheckpoint{Tracks: []model.MediaTrackCheckpoint{{TrackID: "720p", Total: 20, Done: []byte{0xff, 0x03}}}},
		}}
	if err := st.CreateDownload(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	parts := daemon.MediaPartsDir(dir, "show.mp4")
	if parts != filepath.Join(dir, "show.mp4"+mediaPartsExt) {
		t.Fatalf("part directory %q does not match the daemon's %q", filepath.Join(dir, "show.mp4"+mediaPartsExt), parts)
	}
	if err := os.MkdirAll(parts, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parts, "0.frag"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	sys := offlineSystem()
	ctx := context.Background()

	r := runWith(ctx, sys, "info", "1", "--db", db)
	for _, want := range []string{"Kind:", "media (DASH stream)", "The Show", "video 720p + audio aac", "1m30s", "10/40 fragments",
		"Stream tracks (2):", "1280x720"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("info lacks %q:\n%s", want, r.stdout)
		}
	}
	if strings.Contains(r.stdout, "Segments") || strings.Contains(r.stdout, "Mode:") {
		t.Errorf("info shows file fields for a stream:\n%s", r.stdout)
	}
	r = runWith(ctx, sys, "info", "1", "--json", "--db", db)
	var v infoView
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil || v.Kind != "media" || v.Media == nil || v.Media.Duration != 90 ||
		v.Downloaded != 3<<20 || v.Path != filepath.Join(dir, "show.mp4.dxpart") {
		t.Errorf("info --json: %+v %v\n%s", v, err, r.stdout)
	}
	r = runWith(ctx, sys, "ls", "--db", db)
	if !strings.Contains(r.stdout, "25.0%") {
		t.Errorf("ls from the database:\n%s", r.stdout)
	}

	r = runWith(ctx, sys, "resume", "1", "--local", "--db", db)
	if r.code != exitError || !strings.Contains(r.stderr, "streaming-media download, which only downexd runs") {
		t.Errorf("resume --local of a stream: %v", r)
	}
	r = runWith(ctx, sys, "rm", "1", "--local", "--delete-file", "--db", db)
	if r.code != exitOK {
		t.Fatalf("rm --local: %v", r)
	}
	if _, err := os.Stat(parts); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("rm --local --delete-file kept the part directory: %v", err)
	}
}

func TestMediaConstantsMatchDaemon(t *testing.T) {
	if errorCodeMeta != daemon.ErrorCodeMeta {
		t.Errorf("errorCodeMeta = %q, daemon uses %q", errorCodeMeta, daemon.ErrorCodeMeta)
	}
	views := []trackView{{ID: "a", Kind: "audio", Language: "en", Bandwidth: 1}, {ID: "s", Kind: "subtitles"},
		{ID: "v", Kind: "video", Width: 1, Height: 1}, {ID: "b", Kind: "audio", Language: "de"},
		{ID: "m", Kind: "muxed", Width: 2, Height: 2}, {ID: "c", Kind: "audio", Language: "en", Bandwidth: 9}}
	sortTracks(views)
	var ids []string
	for _, v := range views {
		ids = append(ids, v.ID)
	}
	if want := []string{"m", "v", "b", "c", "a", "s"}; !slices.Equal(ids, want) {
		t.Errorf("sorted tracks %q, want %q", ids, want)
	}
}

func TestConfirmSendsTracks(t *testing.T) {
	f, h := newFakeDaemon(t)
	f.do(func() {
		f.downloads[5] = &downexv1.Download{Id: 5, Url: testManifest, Dir: "/v", Filename: "show.mp4", Size: -1,
			State: downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION, Kind: downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA,
			Media: sampleProbe()}
	})
	r := h.run(context.Background(), "confirm", "5", "--video", "v0", "--audio", "a0", "--later")
	if r.code != exitOK {
		t.Fatalf("confirm: %v", r)
	}
	if c := f.rec().confirms; len(c) != 1 || c[0].GetVideoTrack() != "v0" || c[0].GetAudioTrack() != "a0" || c[0].GetStart() {
		t.Errorf("confirms = %v", c)
	}
}

const testPage = "https://www.video.test/watch?v=abcdefghijk"

// sampleSiteProbe is the fake daemon's answer for a web page of a wide film: one MP4
// video per quality class in the daemon's order (best first), named by the class, then
// the audio tracks; the last video has no name (an older daemon's record), so the CLI
// names it from its picture.
func sampleSiteProbe() *downexv1.MediaInfo {
	return &downexv1.MediaInfo{
		Origin: downexv1.MediaOrigin_MEDIA_ORIGIN_SITE, PageUrl: testPage, Site: "YouTube", Title: "A video title",
		Format: "http", Container: "mp4", DurationMs: 212_000, SelectedVideo: "137", SelectedAudio: "140",
		Tracks: []*downexv1.MediaTrack{
			{Id: "137", Kind: "video", Bandwidth: 4_000_000, Width: 1920, Height: 818, FrameRate: 24, Codecs: "avc1.640028",
				Name: "1080p (Full HD)", Size: 412_000_000, Ext: "mp4"},
			{Id: "399", Kind: "video", Bandwidth: 2_500_000, Width: 1280, Height: 544, FrameRate: 24, Codecs: "av01.0.08M.08",
				Name: "720p (HD)", Size: 300_000_000, SizeApprox: true, Ext: "mp4"},
			{Id: "699", Kind: "video", Bandwidth: 5_000_000, Width: 854, Height: 364, FrameRate: 60, Codecs: "av01.0.09M.10",
				Name: "480p 60fps HDR", Ext: "mp4"},
			{Id: "22", Kind: "muxed", Bandwidth: 1_500_000, Width: 640, Height: 272, Codecs: "avc1.64001F, mp4a.40.2",
				Size: 90_000_000, Ext: "mp4"},
			{Id: "140", Kind: "audio", Bandwidth: 130_000, Codecs: "mp4a.40.2", Language: "en", Name: "original",
				Size: 4_000_000, Ext: "m4a"},
		},
	}
}

// siteRefusal is a refusal of the daemon with a stable code (message prefix and metadata).
func siteRefusal(code string, cc connect.Code, msg string) error {
	ce := connect.NewError(cc, errors.New(code+": "+msg))
	ce.Meta().Set(errorCodeMeta, code)
	return ce
}

// TestMediaSiteList: "media --site --list" retries while yt-dlp installs and the page
// resolves (one status line each), then lists the daemon's tracks (one video per quality)
// in its order with their quality names (from the picture when the daemon gave none),
// pictures, codecs, sizes ("~" for estimates), file types and languages.
func TestMediaSiteList(t *testing.T) {
	f, h := newFakeDaemon(t)
	f.do(func() {
		f.probe = sampleSiteProbe()
		f.mediaRetry = []error{
			siteRefusal("tools_installing", connect.CodeFailedPrecondition, "downloading yt-dlp and Deno (12%)"),
			siteRefusal("tools_installing", connect.CodeFailedPrecondition, "downloading yt-dlp and Deno (34%)"),
			siteRefusal("site_resolving", connect.CodeFailedPrecondition, "still getting the video info"),
		}
	})
	ctx := context.Background()
	r := h.run(ctx, "media", "--site", testPage, "--list", "-H", "Cookie: SID=1", "-H", "referer: https://www.video.test/")
	if r.code != exitOK {
		t.Fatalf("media --site --list: %v", r)
	}
	lines := strings.Split(strings.TrimRight(r.stdout, "\n"), "\n")
	if len(lines) != 9 || lines[0] != "YouTube video: A video title" || lines[1] != "Duration: 3m32s  Saved as: .mp4" ||
		!strings.HasPrefix(lines[2], "  ID") || !strings.Contains(lines[2], "SIZE") || !strings.Contains(lines[2], "EXT") ||
		!strings.Contains(lines[2], "QUALITY") || !strings.Contains(lines[2], "PICTURE") {
		t.Fatalf("listing:\n%s", r.stdout)
	}
	for i, want := range [][]string{
		{"* 137", "video", "1080p (Full HD)", "1920x818 24fps", "H.264", "412.0 MB", "mp4", "-"},
		{"  399", "720p (HD)", "1280x544 24fps", "AV1", "~300.0 MB"},
		{"  699", "480p 60fps HDR", "854x364 60fps", "AV1", "~132.5 MB"}, // no size: 5 Mbit/s × 212 s
		{"  22", "muxed", "360p", "640x272", "H.264, AAC", "90.0 MB"},    // no name: from its picture
		{"* 140", "audio", "130 kbit/s original", "AAC", "4.0 MB", "m4a", "en"},
	} {
		for _, w := range want {
			if !strings.Contains(lines[3+i], w) {
				t.Errorf("row %d lacks %q: %q", i, w, lines[3+i])
			}
		}
	}
	if !strings.Contains(lines[8], "--video ID") {
		t.Errorf("footer = %q", lines[8])
	}
	if strings.Count(r.stderr, "Downloading yt-dlp and Deno…") != 1 || strings.Count(r.stderr, "Getting video info…") != 1 {
		t.Errorf("status lines:\n%s", r.stderr)
	}
	f.do(func() {
		if len(f.mediaProbes) != 4 {
			t.Fatalf("%d probes, want 4 (3 retries)", len(f.mediaProbes))
		}
		p := f.mediaProbes[3]
		if p.GetOrigin() != downexv1.MediaOrigin_MEDIA_ORIGIN_SITE || p.GetPageUrl() != testPage || p.GetManifestUrl() != "" ||
			len(p.GetHeaders()) != 2 || p.GetHeaders()[0].GetName() != "Cookie" || p.GetHeaders()[1].GetName() != "Referer" {
			t.Errorf("probe = %v", p)
		}
	})

	r = h.run(ctx, "media", "--site", testPage, "--list", "--json")
	var v mediaView
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil || v.Origin != "site" || v.PageURL != testPage || v.Site != "YouTube" ||
		v.ManifestURL != "" || len(v.Tracks) != 5 || v.Tracks[2].ID != "699" || v.Tracks[1].Size != 300_000_000 ||
		!v.Tracks[1].SizeApprox || v.Tracks[4].Ext != "m4a" {
		t.Errorf("--json: %+v %v\n%s", v, err, r.stdout)
	}
	if !strings.Contains(r.stdout, `"sizeApprox": true`) || !strings.Contains(r.stdout, `"pageUrl"`) {
		t.Errorf("--json fields:\n%s", r.stdout)
	}
}

// TestMediaSiteListNoSound: a page whose videos are listed without any audio track (a
// silent page) says so in the CODECS column, and its footer offers no audio to choose.
func TestMediaSiteListNoSound(t *testing.T) {
	f, h := newFakeDaemon(t)
	f.do(func() {
		p := sampleSiteProbe()
		p.Tracks = slices.DeleteFunc(p.Tracks, func(t *downexv1.MediaTrack) bool { return t.GetKind() != "video" })
		p.SelectedAudio = ""
		f.probe = p
	})
	r := h.run(context.Background(), "media", "--site", testPage, "--list")
	if r.code != exitOK {
		t.Fatalf("media --site --list: %v", r)
	}
	lines := strings.Split(strings.TrimRight(r.stdout, "\n"), "\n")
	if len(lines) != 7 || !strings.Contains(lines[3], "H.264 (no sound)") || !strings.Contains(lines[4], "AV1 (no sound)") {
		t.Errorf("listing:\n%s", r.stdout)
	}
	if foot := lines[len(lines)-1]; foot != "* = downloaded by default; choose another quality with --video ID." {
		t.Errorf("footer %q", foot)
	}
}

// TestMediaSiteAdd: "media --site" adds a page with the chosen quality.
func TestMediaSiteAdd(t *testing.T) {
	f, h := newFakeDaemon(t)
	f.do(func() {
		f.probe = sampleSiteProbe()
		f.mediaRetry = []error{siteRefusal("site_resolving", connect.CodeFailedPrecondition, "still getting the video info")}
	})
	dir := t.TempDir()
	r := h.run(context.Background(), "media", "--site", testPage, "--video", "399", "-o", dir, "--name", "talk", "--paused")
	if r.code != exitOK || r.stdout != "7\n" ||
		!strings.Contains(r.stderr, "Download 7 paused: talk.mp4 (YouTube video: 720p (HD) AV1 + audio 140 (en))") {
		t.Fatalf("media --site: %v", r)
	}
	f.do(func() {
		if len(f.mediaAdds) != 2 {
			t.Fatalf("%d adds, want 2 (one retry)", len(f.mediaAdds))
		}
		a := f.mediaAdds[1]
		if a.GetOrigin() != downexv1.MediaOrigin_MEDIA_ORIGIN_SITE || a.GetPageUrl() != testPage || a.GetManifestUrl() != "" ||
			a.GetVideoTrack() != "399" || a.GetDir() != dir || a.GetFilename() != "talk" || !a.GetStartPaused() ||
			a.GetSource() != downexv1.AddSource_ADD_SOURCE_CLI {
			t.Errorf("AddMediaRequest = %v", a)
		}
	})
}

// TestMediaSiteRefusals: -H takes only Cookie and Referer; the daemon's refusals of a page
// are explained (docs/SITE-MEDIA-SPEC.md §5.5); the retries end after retryFor.
func TestMediaSiteRefusals(t *testing.T) {
	f, h := newFakeDaemon(t)
	ctx := context.Background()
	r := h.run(ctx, "media", "--site", testPage, "-H", "User-Agent: x")
	if r.code != exitError || !strings.Contains(r.stderr, "-H User-Agent: with --site only the Cookie and Referer headers") {
		t.Errorf("User-Agent: %v", r)
	}
	if r = h.run(ctx, "media", "--site"); r.code != exitError || !strings.Contains(r.stderr, "expects exactly one page URL") {
		t.Errorf("no page: %v", r)
	}
	if dials, _ := h.recorded(); len(dials) != 0 {
		t.Errorf("bad arguments reached the daemon: %d dials", len(dials))
	}
	for _, c := range []struct {
		err  error
		want string
	}{
		{siteRefusal("site_unsupported", connect.CodeInvalidArgument, "no video found on this page: Unsupported URL"),
			"media: Downex found no video on this page"},
		{siteRefusal("site_unavailable", connect.CodeFailedPrecondition, "this video is not available: Private video"),
			"media: this video is not available (private, removed, blocked in your country, or the site asks to try again later): Private video"},
		{siteRefusal("auth_required", connect.CodeFailedPrecondition, "the site asks to sign in: Sign in to confirm your age"),
			"the site wants you to sign in"},
		{siteRefusal("site_error", connect.CodeFailedPrecondition, "yt-dlp could not read this page: HTTP Error 500"),
			"media: yt-dlp could not read this page: HTTP Error 500"},
		{siteRefusal("tools_unavailable", connect.CodeFailedPrecondition, "yt-dlp and Deno are not set up: yt-dlp failed its signature check"),
			"media: Downex could not set up yt-dlp/Deno (see \"downex tools\"): yt-dlp failed its signature check"},
		{siteRefusal("site_media_off", connect.CodeFailedPrecondition, "video downloads from web pages are turned off (Settings → Video sites)"),
			"downex settings --site-media on"},
		{siteRefusal("media_unsupported", connect.CodeInvalidArgument, "media: this stream cannot be saved yet: no MP4 video on this page"),
			"media: no MP4 video on this page"},
		{siteRefusal("encrypted", connect.CodeInvalidArgument, "media: content is encrypted or DRM-protected"),
			"Downex does not download protected content"},
	} {
		f.do(func() { f.mediaErr = c.err })
		for _, args := range [][]string{{"media", "--site", testPage}, {"media", "--site", testPage, "--list"}} {
			r := h.run(ctx, args...)
			if r.code != exitError || !strings.Contains(r.stderr, c.want) || r.stdout != "" {
				t.Errorf("%v %q: %v\nwant %q", c.err, args, r, c.want)
			}
		}
	}

	// A page that keeps resolving: downex gives up after retryFor.
	h.retryFor = 50 * time.Millisecond
	f.do(func() {
		f.mediaErr = siteRefusal("tools_installing", connect.CodeFailedPrecondition, "downloading yt-dlp and Deno (1%)")
	})
	r = h.run(ctx, "media", "--site", testPage, "--list")
	if r.code != exitError || !strings.Contains(r.stderr, "yt-dlp and Deno are still being downloaded; try again") {
		t.Errorf("gave up: %v", r)
	}
	// A manifest is never retried.
	f.do(func() { f.mediaProbes = nil })
	if r = h.run(ctx, "media", testManifest, "--list"); r.code != exitError {
		t.Errorf("manifest: %v", r)
	}
	f.do(func() {
		if len(f.mediaProbes) != 1 {
			t.Errorf("a manifest probe was tried %d times", len(f.mediaProbes))
		}
	})
}

// TestSiteMediaViews: ls --json and info --json show a web page's origin, page and track
// sizes; the progress line of a running page download counts bytes of its known size.
func TestSiteMediaViews(t *testing.T) {
	f, h := newFakeDaemon(t)
	running := &downexv1.Download{Id: 9, Url: testPage, Dir: "/v", Filename: "A video title.mp4", Size: 416_000_000,
		Received: 104_000_000, State: downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING, SpeedBps: 4 << 20, Connections: 4,
		Kind: downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA, Media: sampleSiteProbe()}
	f.do(func() { f.downloads[9] = clone(running) })
	r := h.run(context.Background(), "ls", "--json")
	var views []downloadView
	if err := json.Unmarshal([]byte(r.stdout), &views); err != nil || len(views) != 1 {
		t.Fatalf("ls --json: %v %v", err, r)
	}
	if m := views[0].Media; m == nil || m.Origin != "site" || m.PageURL != testPage || m.Site != "YouTube" || m.ManifestURL != "" ||
		m.Tracks[0].Size != 412_000_000 || m.Tracks[0].Ext != "mp4" {
		t.Errorf("media = %+v", views[0].Media)
	}
	if line := mediaProgressLine(running); line != " 25.0%  104.0 MB/416.0 MB  4.2 MB/s  4 conn" {
		t.Errorf("progress line = %q", line)
	}
	// Before the sizes are known, a web page's direct formats count parts.
	unsized := clone(running)
	unsized.Size, unsized.Media.Format, unsized.Media.FragmentsDone, unsized.Media.FragmentsTotal = -1, "http", 3, 40
	if line := mediaProgressLine(unsized); !strings.HasPrefix(line, "  7.5%  3/40 parts  ") {
		t.Errorf("progress line without a size = %q", line)
	}

	stored := storedMediaView(&model.Download{Request: model.Request{URL: testPage}, Media: &model.Media{
		Origin: "site", Site: "YouTube", Format: "http", Container: "mp4", SelectedVideo: "137",
		Tracks: []model.MediaTrack{{ID: "137", Kind: "video", Size: 412_000_000, SizeApprox: true, Ext: "mp4"},
			{ID: "18", Kind: "muxed", Width: 640, Height: 360}},
	}})
	if stored.Origin != "site" || stored.PageURL != testPage || stored.ManifestURL != "" || stored.Tracks[0].ID != "137" ||
		!stored.Tracks[0].SizeApprox || stored.Tracks[0].Ext != "mp4" {
		t.Errorf("stored view = %+v", stored)
	}
	if m := storedMediaView(&model.Download{Request: model.Request{URL: testManifest}, Media: &model.Media{Format: "hls"}}); m.Origin != "manifest" ||
		m.ManifestURL != testManifest {
		t.Errorf("manifest view = %+v", m)
	}
	for in, want := range map[string]string{
		"avc1.640028": "H.264", "avc3.4d": "H.264", "av01.0.08M.08": "AV1", "hvc1.1.6.L93": "HEVC", "hev1": "HEVC",
		"vp09.00.40.08": "VP9", "vp9": "VP9", "mp4a.40.2": "AAC", "opus": "Opus", "ac-3": "AC-3", "ec-3": "E-AC-3",
		"avc1.64001F, mp4a.40.2": "H.264, AAC", "theora": "theora", "": "",
	} {
		if got := codecNames(in); got != want {
			t.Errorf("codecNames(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSiteInfo: "info" of a web page's video names the site and counts parts.
func TestSiteInfo(t *testing.T) {
	db := filepath.Join(t.TempDir(), "downex.db")
	st, _, err := openStore(db)
	if err != nil {
		t.Fatal(err)
	}
	d := &model.Download{Request: model.Request{URL: testPage}, Dir: t.TempDir(), Filename: "A video.mp4", Size: -1,
		State: model.StatePaused, Kind: model.KindMedia, Media: &model.Media{
			Origin: "site", Site: "YouTube", Format: "http", Container: "mp4", SelectedVideo: "137", SelectedAudio: "140",
			FragmentsTotal: 46, FragmentsDone: 12,
			Tracks: []model.MediaTrack{{ID: "137", Kind: "video", Width: 1920, Height: 1080, Size: 412_000_000, Ext: "mp4"},
				{ID: "140", Kind: "audio", Size: 4_000_000, Ext: "m4a"}},
		}}
	if err := st.CreateDownload(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	r := runWith(context.Background(), offlineSystem(), "info", "1", "--db", db)
	for _, want := range []string{"video page (YouTube)", "12/46 parts", "video 137 + audio 140"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("info lacks %q:\n%s", want, r.stdout)
		}
	}
	if strings.Contains(r.stdout, "stream)") || strings.Contains(r.stdout, "fragments") {
		t.Errorf("info describes a web page's video as a stream:\n%s", r.stdout)
	}
}

// TestSiteRetryTiming: the next try of a page starts retryEvery after the start of the
// previous one, so that a call that used up its time is followed at once, not after one
// more full wait.
func TestSiteRetryTiming(t *testing.T) {
	var stdout, stderr syncBuffer
	a := newApp(&stdout, &stderr, offlineSystem())
	a.retryEvery, a.retryFor = 200*time.Millisecond, time.Minute
	var starts []time.Time
	err := a.siteRetry(context.Background(), func(context.Context) error {
		starts = append(starts, time.Now())
		if len(starts) == 4 {
			return nil
		}
		time.Sleep(200 * time.Millisecond) // the call takes as long as the retry interval
		return siteRefusal(codeSiteResolving, connect.CodeFailedPrecondition, "getting the video info takes long")
	})
	if err != nil || len(starts) != 4 {
		t.Fatalf("siteRetry = %v after %d tries", err, len(starts))
	}
	if took := starts[3].Sub(starts[0]); took > 900*time.Millisecond {
		t.Errorf("3 retries took %v, want about 600 ms (each try starts retryEvery after the last one began)", took)
	}
}

// The help names .mp4 as the only output of a stream: MPEG-TS is converted
// (docs/VIDEO-DETECT-SPEC.md §3.9).
func TestMediaHelpMP4(t *testing.T) {
	_, h := newFakeDaemon(t)
	r := h.run(context.Background(), "media", "-h")
	out := r.stdout + r.stderr
	if r.code != exitOK || !strings.Contains(out, "one .mp4 file") || !strings.Contains(out, "(.mp4 is added)") ||
		strings.Contains(out, ".ts") {
		t.Errorf("help (exit %d):\n%s", r.code, out)
	}
}

// --list shows the MP4 qualities the daemon offers, MPEG-TS variants with their codecs,
// saved as .mp4; a stream without an MP4 video is explained.
func TestMediaListTransportStreams(t *testing.T) {
	f, h := newFakeDaemon(t)
	ctx := context.Background()
	f.do(func() {
		f.probe = &downexv1.MediaInfo{
			ManifestUrl: testManifest, Format: "hls", DurationMs: 60_000, Container: "mp4", FragmentsTotal: 15, SelectedVideo: "v1",
			Tracks: []*downexv1.MediaTrack{
				{Id: "v1", Kind: "muxed", Bandwidth: 5_000_000, Width: 1920, Height: 1080, Codecs: "avc1.640028,mp4a.40.2"},
				{Id: "v2", Kind: "muxed", Bandwidth: 1_000_000, Width: 854, Height: 480},
			},
		}
	})
	r := h.run(ctx, "media", testManifest, "--list")
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if r.code != exitOK || len(lines) != 5 || lines[0] != "Format: HLS  Duration: 1m00s  Saved as: .mp4  Fragments: 15" ||
		!strings.HasPrefix(lines[2], "* v1") || !strings.Contains(lines[2], "avc1.640028,mp4a.40.2") ||
		!strings.HasPrefix(lines[3], "  v2") {
		t.Errorf("track list:\n%s", r.stdout)
	}

	ce := connect.NewError(connect.CodeInvalidArgument,
		errors.New("media_unsupported: mediajob: unsupported operation: no MP4 video in this stream"))
	ce.Meta().Set(errorCodeMeta, "media_unsupported")
	f.do(func() { f.mediaErr = ce })
	for _, args := range [][]string{{"media", testManifest}, {"media", testManifest, "--list"}} {
		r := h.run(ctx, args...)
		if r.code != exitError || !strings.Contains(r.stderr, "media: no MP4 video in this stream") || r.stdout != "" {
			t.Errorf("%q: %v", args, r)
		}
	}
}

// --list shows a track's size, "~" for the daemon's estimate: a bare media playlist the
// daemon measured (no BANDWIDTH) lists its picture, name and estimated size; a track
// with a BANDWIDTH and no size, its bandwidth × the duration ("~", as the desktop and the
// extension estimate it); a size wins over the bandwidth.
func TestMediaListSizes(t *testing.T) {
	f, h := newFakeDaemon(t)
	ctx := context.Background()
	f.do(func() {
		f.probe = &downexv1.MediaInfo{
			ManifestUrl: testManifest, Format: "hls", DurationMs: 3_907_000, Container: "mp4", FragmentsTotal: 977, SelectedVideo: "v0",
			Tracks: []*downexv1.MediaTrack{
				{Id: "v0", Kind: "muxed", Width: 1920, Height: 1080, FrameRate: 25, Codecs: "avc1.640028,mp4a.40.2", Name: "1080p",
					Size: 2_254_857_830, SizeApprox: true},
				{Id: "v1", Kind: "muxed", Bandwidth: 1_000_000, Width: 854, Height: 480, Size: 52_428_800},
				{Id: "v2", Kind: "muxed", Bandwidth: 500_000, Width: 640, Height: 360},
			},
		}
	})
	r := h.run(ctx, "media", testManifest, "--list")
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if r.code != exitOK || len(lines) != 6 || !strings.Contains(lines[1], "SIZE") ||
		!strings.HasPrefix(lines[2], "* v0") || !strings.Contains(lines[2], "1920x1080 25fps") || !strings.Contains(lines[2], "~2.3 GB") ||
		!strings.Contains(lines[2], "1080p") || !strings.Contains(lines[3], " 52.4 MB ") || strings.Contains(lines[3], "~") ||
		!strings.Contains(lines[4], "500 kbit/s  ~244.2 MB  ") { // 500 kbit/s × 3907 s = 244,187,500 bytes
		t.Errorf("track list:\n%s", r.stdout)
	}
	// The JSON keeps what the daemon said: no size, the bandwidth and the duration.
	r = h.run(ctx, "media", testManifest, "--list", "--json")
	var v mediaView
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil || v.Tracks[0].Size != 2_254_857_830 || !v.Tracks[0].SizeApprox ||
		v.Tracks[2].Size != 0 || v.Tracks[2].SizeApprox || v.Tracks[2].Bandwidth != 500_000 {
		t.Errorf("media --list --json: %+v %v", v, err)
	}
	// Without a duration nothing is estimated.
	f.do(func() { f.probe.DurationMs = 0 })
	r = h.run(ctx, "media", testManifest, "--list")
	if lines = strings.Split(strings.TrimSpace(r.stdout), "\n"); r.code != exitOK || len(lines) != 6 ||
		!strings.Contains(lines[4], "500 kbit/s  -  ") || !strings.Contains(lines[2], "~2.3 GB") {
		t.Errorf("track list without a duration:\n%s", r.stdout)
	}
}

// A record of an older daemon (`downex info`, a list) shows the names the desktop and
// the extension show for it (media.TrackName): its raw heights ("1634p", "480p" of a
// 1138x480 picture), "1080p60" and "1920x1080" are named again from the picture, a
// standard name is kept.
func TestOldRecordNames(t *testing.T) {
	v := &mediaView{Origin: originManifest, Format: "hls", Tracks: []trackView{
		{ID: "v0", Kind: "video", Width: 3840, Height: 1634, FrameRate: 24, Name: "1634p"},
		{ID: "v1", Kind: "video", Width: 1138, Height: 480, FrameRate: 24, Name: "480p"},
		{ID: "v2", Kind: "muxed", Width: 1920, Height: 1080, FrameRate: 60, Name: "1080p60"},
		{ID: "v3", Kind: "video", Width: 1280, Height: 720, FrameRate: 30, Name: "1280x720"},
		{ID: "v4", Kind: "video", Width: 3840, Height: 2160, FrameRate: 60, Name: "2160p60 HDR"},
		{ID: "v5", Kind: "video", Width: 854, Height: 364, FrameRate: 24, Name: "480p"},
		{ID: "v6", Kind: "video", Name: "Main"},
	}}
	want := []string{"4K (2160p)", "720p (HD)", "1080p (Full HD) 60fps", "720p (HD)", "4K (2160p) 60fps HDR", "480p", "Main"}
	for _, origin := range []string{originManifest, originSite} {
		v.Origin = origin
		var b strings.Builder
		if err := writeTracks(&b, v); err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(b.String(), "\n")
		at := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "QUALITY") })
		if at < 0 || len(lines) < at+1+len(want) {
			t.Fatalf("%s list:\n%s", origin, b.String())
		}
		for i, name := range want {
			if l := lines[at+1+i]; !strings.Contains(l, "  "+name+"  ") {
				t.Errorf("%s: %s, want %q", origin, l, name)
			}
		}
	}
}

// TestFormatBandwidth: track bitrates step at 1000 and never show 1000 of a unit.
func TestFormatBandwidth(t *testing.T) {
	for bps, want := range map[int64]string{-1: "-", 0: "-", 999: "999 bit/s", 1000: "1 kbit/s",
		128_000: "128 kbit/s", 999_499: "999 kbit/s", 999_500: "1.0 Mbit/s", 999_999: "1.0 Mbit/s",
		1_000_000: "1.0 Mbit/s", 5_000_000: "5.0 Mbit/s", 999_949_999: "999.9 Mbit/s", 999_950_000: "1.0 Gbit/s",
		1_500_000_000: "1.5 Gbit/s", 2e12: "2.0 Tbit/s", math.MaxInt64: "9.2 Ebit/s"} {
		if got := formatBandwidth(bps); got != want {
			t.Errorf("formatBandwidth(%d) = %q, want %q", bps, got, want)
		}
	}
}

// TestMediaSiteItems: a page with several videos lists them after the qualities, the one
// described marked; --item names one for the probe and the add (docs/SITE-MEDIA-SPEC.md
// §3.7, §3.12), only with --site.
func TestMediaSiteItems(t *testing.T) {
	f, h := newFakeDaemon(t)
	f.do(func() {
		p := sampleSiteProbe()
		p.EntryId, p.ItemsLeftOut = "itemB", 2
		p.Items = []*downexv1.MediaItem{{Id: "itemA", Title: "Video by example", DurationMs: 12_000},
			{Id: "itemB", Title: "Second", DurationMs: 75_500}, {Id: "itemC"}}
		f.probe = p
	})
	r := h.run(context.Background(), "media", "--site", testPage, "--list", "--item", "itemB")
	if r.code != exitOK {
		t.Fatalf("media --site --list --item: %v", r)
	}
	for _, want := range []string{
		"Videos on this page: 3 (2 other items not listed: photos, links or videos Downex cannot save)",
		"  1  itemA  Video by example  0:12", "* 2  itemB  Second            1:15.5", "  3  itemC  -                 -",
		"* = the video listed above; choose another with --item ID.",
	} {
		if !strings.Contains(r.stdout, want+"\n") {
			t.Errorf("listing lacks %q:\n%s", want, r.stdout)
		}
	}
	r = h.run(context.Background(), "media", "--site", testPage, "--list", "--json")
	if r.code != exitOK || !strings.Contains(r.stdout, `"entryId": "itemB"`) || !strings.Contains(r.stdout, `"itemsLeftOut": 2`) ||
		!strings.Contains(r.stdout, `"durationSeconds": 75.5`) {
		t.Errorf("--json: %v", r)
	}
	r = h.run(context.Background(), "media", "--site", testPage, "--item", "itemC", "-o", t.TempDir())
	if r.code != exitOK {
		t.Fatalf("media --site --item: %v", r)
	}
	f.do(func() {
		if f.mediaProbes[0].GetEntryId() != "itemB" || f.mediaAdds[len(f.mediaAdds)-1].GetEntryId() != "itemC" {
			t.Errorf("probe %v, add %v", f.mediaProbes[0], f.mediaAdds[len(f.mediaAdds)-1])
		}
	})
	for _, args := range [][]string{
		{"media", testManifest, "--item", "x"},
		{"media", "--site", testPage, "--item", ""},
		{"media", "--site", testPage, "--item", "a\x01b"},
	} {
		if r := h.run(context.Background(), args...); r.code != exitError || !strings.Contains(r.stderr, "--item") {
			t.Errorf("%q: %v", args, r)
		}
	}
}

// --cookie-domain (docs/SITE-MEDIA-SPEC.md §6.3) sends the -H Cookie's domain with the
// probe and the add; it needs --site, a Cookie and a domain that holds the page.
func TestMediaSiteCookieDomain(t *testing.T) {
	f, h := newFakeDaemon(t)
	f.do(func() { f.probe = sampleSiteProbe() })
	const host = "video.test" // testPage's site
	r := h.run(context.Background(), "media", "--site", testPage, "-H", "Cookie: sid=e2e", "--cookie-domain", host, "-o", t.TempDir())
	if r.code != exitOK {
		t.Fatalf("media --site --cookie-domain: %v", r)
	}
	r = h.run(context.Background(), "media", "--site", testPage, "-H", "Cookie: sid=e2e", "--cookie-domain", host, "--list")
	if r.code != exitOK {
		t.Fatalf("media --site --list --cookie-domain: %v", r)
	}
	f.do(func() {
		if add := f.mediaAdds[len(f.mediaAdds)-1]; add.GetCookieDomain() != host {
			t.Errorf("add %v", add)
		}
		if p := f.mediaProbes[len(f.mediaProbes)-1]; p.GetCookieDomain() != host {
			t.Errorf("probe %v", p)
		}
	})
	for _, args := range [][]string{
		{"media", testManifest, "-H", "Cookie: sid=e2e", "--cookie-domain", host},
		{"media", "--site", testPage, "--cookie-domain", host},
		{"media", "--site", testPage, "-H", "Cookie: sid=e2e", "--cookie-domain", ""},
		{"media", "--site", testPage, "-H", "Cookie: sid=e2e", "--cookie-domain", "Example.COM"},
		{"media", "--site", testPage, "-H", "Cookie: sid=e2e", "--cookie-domain", "other.example"},
		{"media", "--site", testPage, "-H", "Cookie: sid=e2e", "--cookie-domain", "com"},
	} {
		if r := h.run(context.Background(), args...); r.code != exitError || !strings.Contains(r.stderr, "--cookie-domain") ||
			strings.Contains(r.stderr, "sid=e2e") {
			t.Errorf("%q: %v", args, r)
		}
	}
}
