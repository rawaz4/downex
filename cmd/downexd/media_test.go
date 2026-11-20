package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"downex/internal/daemon"
	"downex/internal/media"
	"downex/internal/mediajob"
	"downex/internal/model"
	"downex/internal/remux"
)

// Checkpoints cross the adapter unchanged, without sharing their bitmaps.
func TestMediaCheckpointConversion(t *testing.T) {
	if jobCheckpoint(nil) != nil {
		t.Error("jobCheckpoint(nil) is not nil")
	}
	stored := &model.MediaCheckpoint{Tracks: []model.MediaTrackCheckpoint{
		{TrackID: "v1", Total: 10, Done: []byte{0xff, 0x01}, Key: "h1"},
		{TrackID: "a1", Total: 3, Done: []byte{}},
	}}
	jc := jobCheckpoint(stored)
	want := &mediajob.Checkpoint{Tracks: []mediajob.TrackCheckpoint{
		{TrackID: "v1", Total: 10, Done: []byte{0xff, 0x01}, Key: "h1"},
		{TrackID: "a1", Total: 3, Done: []byte{}},
	}}
	if !reflect.DeepEqual(jc, want) {
		t.Fatalf("jobCheckpoint = %+v, want %+v", jc, want)
	}
	jc.Tracks[0].Done[0] = 0
	if stored.Tracks[0].Done[0] != 0xff {
		t.Error("jobCheckpoint shares the stored bitmap")
	}
	if got := jobCheckpoint(&model.MediaCheckpoint{}); got == nil || got.Tracks != nil {
		t.Errorf("empty checkpoint = %+v", got)
	}

	job := mediajob.Checkpoint{Tracks: []mediajob.TrackCheckpoint{{TrackID: "v1", Total: 9, Done: []byte{0x0f, 0x00}, Key: "h2"}}}
	mc := modelCheckpoint(job)
	if !reflect.DeepEqual(mc, model.MediaCheckpoint{Tracks: []model.MediaTrackCheckpoint{{TrackID: "v1", Total: 9, Done: []byte{0x0f, 0x00}, Key: "h2"}}}) {
		t.Fatalf("modelCheckpoint = %+v", mc)
	}
	job.Tracks[0].Done[0] = 0xff
	if mc.Tracks[0].Done[0] != 0x0f {
		t.Error("modelCheckpoint shares the job's bitmap")
	}
}

// A site job's chunk prefixes, durable bytes and exact total (docs/SITE-MEDIA-SPEC.md
// §2.5) cross the adapter both ways and survive Clone, without sharing their maps; a
// checkpoint stored before 0.8.2 (no prefixes) converts to one without them.
func TestMediaCheckpointPartialConversion(t *testing.T) {
	job := mediajob.Checkpoint{
		Tracks: []mediajob.TrackCheckpoint{
			{TrackID: "137", Total: 30, Done: []byte{0x31, 0, 0, 0}, Key: "s1-137", ChunkBytes: 10 << 20,
				Partial: map[int]int64{1: 2 << 20, 2: 1536 << 10}},
			{TrackID: "140", Total: 8, Done: []byte{0x01}, Key: "s1-140", ChunkBytes: 2 << 20},
		},
		DurableBytes: 33_619_968 + 3_670_016,
		BytesTotal:   316_000_000,
	}
	mc := modelCheckpoint(job)
	want := model.MediaCheckpoint{
		Tracks: []model.MediaTrackCheckpoint{
			{TrackID: "137", Total: 30, Done: []byte{0x31, 0, 0, 0}, Key: "s1-137", ChunkBytes: 10 << 20,
				Partial: map[int]int64{1: 2 << 20, 2: 1536 << 10}},
			{TrackID: "140", Total: 8, Done: []byte{0x01}, Key: "s1-140", ChunkBytes: 2 << 20},
		},
		DurableBytes: 33_619_968 + 3_670_016,
		BytesTotal:   316_000_000,
	}
	if !reflect.DeepEqual(mc, want) {
		t.Fatalf("modelCheckpoint = %+v, want %+v", mc, want)
	}
	job.Tracks[0].Partial[1] = 7
	if mc.Tracks[0].Partial[1] != 2<<20 {
		t.Error("modelCheckpoint shares the job's prefixes")
	}

	clone := mc.Clone()
	if !reflect.DeepEqual(*clone, want) {
		t.Fatalf("Clone = %+v, want %+v", clone, want)
	}
	clone.Tracks[0].Partial[2] = 9
	if mc.Tracks[0].Partial[2] != 1536<<10 {
		t.Error("Clone shares the prefixes")
	}

	jc := jobCheckpoint(&mc)
	wantJob := &mediajob.Checkpoint{
		Tracks: []mediajob.TrackCheckpoint{
			{TrackID: "137", Total: 30, Done: []byte{0x31, 0, 0, 0}, Key: "s1-137", ChunkBytes: 10 << 20,
				Partial: map[int]int64{1: 2 << 20, 2: 1536 << 10}},
			{TrackID: "140", Total: 8, Done: []byte{0x01}, Key: "s1-140", ChunkBytes: 2 << 20},
		},
		DurableBytes: 33_619_968 + 3_670_016,
		BytesTotal:   316_000_000,
	}
	if !reflect.DeepEqual(jc, wantJob) {
		t.Fatalf("jobCheckpoint = %+v, want %+v", jc, wantJob)
	}
	jc.Tracks[0].Partial[1] = 5
	if mc.Tracks[0].Partial[1] != 2<<20 {
		t.Error("jobCheckpoint shares the stored prefixes")
	}

	// The JSON the store keeps: an old checkpoint has no prefixes and reads as before.
	b, err := json.Marshal(mc)
	if err != nil {
		t.Fatal(err)
	}
	var back model.MediaCheckpoint
	if err := json.Unmarshal(b, &back); err != nil || !reflect.DeepEqual(back, want) {
		t.Fatalf("JSON round trip = %+v, %v; want %+v (%s)", back, err, want, b)
	}
	var old model.MediaCheckpoint
	if err := json.Unmarshal([]byte(`{"tracks":[{"trackId":"137","total":3,"done":"Aw==","key":"s1-137","chunkBytes":1048576}]}`), &old); err != nil {
		t.Fatal(err)
	}
	if oj := jobCheckpoint(&old); oj.Tracks[0].Partial != nil || oj.DurableBytes != 0 || oj.BytesTotal != 0 ||
		!bytes.Equal(oj.Tracks[0].Done, []byte{3}) {
		t.Errorf("an old checkpoint converts to %+v", oj)
	}
}

// tsPackets is n MPEG-TS packets of filler (null packets), whose first byte is the sync
// byte: what the job needs to identify a transport stream, not anything the remuxer
// could convert (the tests use fake assemblers).
func tsPackets(n int) []byte {
	return bytes.Repeat(append([]byte{0x47, 0x1f, 0xff, 0x10}, make([]byte, 184)...), n)
}

// tsPlaylist serves a VOD media playlist of n TS segments under dir and returns the
// segments joined.
func tsPlaylist(s *fileServer, dir string, n int) []byte {
	var pl strings.Builder
	pl.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXT-X-PLAYLIST-TYPE:VOD\n")
	var all []byte
	for i := range n {
		seg := tsPackets(2 + i)
		s.add(fmt.Sprintf("%sseg%d.ts", dir, i), seg)
		fmt.Fprintf(&pl, "#EXTINF:4.0,\nseg%d.ts\n", i)
		all = append(all, seg...)
	}
	pl.WriteString("#EXT-X-ENDLIST\n")
	s.add(dir+"index.m3u8", []byte(pl.String()))
	return all
}

// tsMaster serves a master playlist under dir: two 1080p MPEG-TS variants, the better
// one with AC-3 (not converted) and the other with AAC, and a 480p variant without
// CODECS.
func tsMaster(s *fileServer, dir string) {
	s.add(dir+"master.m3u8", []byte(`#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=6000000,RESOLUTION=1920x1080,CODECS="avc1.640028,ac-3"
ac3/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080,CODECS="avc1.640028,mp4a.40.2"
aac/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=1000000,RESOLUTION=854x480
sd/index.m3u8
`))
	tsPlaylist(s, dir+"ac3/", 2)
	tsPlaylist(s, dir+"aac/", 3)
	tsPlaylist(s, dir+"sd/", 2)
}

// A MANIFEST probe lists only what is saved as an MP4, best first
// (sitemedia.ManifestTracks): MPEG-TS variants with convertible codecs or none, the AAC
// 1080p variant in place of the AC-3 one above it; WebM Representations and audio-only
// manifests are not (docs/VIDEO-DETECT-SPEC.md §3.3).
func TestMediaJobsProbeMP4Only(t *testing.T) {
	srv := newFileServer(t)
	tsMaster(srv, "/t/")
	p, err := mediaJobs{}.Probe(siteContext(t), siteFactory(t), daemon.MediaSelection{ManifestURL: srv.url("/t/master.m3u8")})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, tr := range p.Tracks {
		ids = append(ids, tr.ID)
	}
	if !slices.Equal(ids, []string{"v1", "v2"}) || p.DefaultVideo != "v1" || p.DefaultAudio != "" || p.Container != "mp4" ||
		p.Format != "hls" || p.FragmentsTotal != 3 {
		t.Fatalf("probe = %+v (tracks %v)", p, ids)
	}
	if p.Tracks[0].Codecs != "avc1.640028,mp4a.40.2" {
		t.Errorf("codecs = %q", p.Tracks[0].Codecs)
	}
	for _, r := range srv.requests() {
		if strings.HasSuffix(r.path, ".ts") {
			t.Errorf("the probe fetched a segment: %s", r.path)
		}
	}

	srv.add("/a/master.m3u8", []byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=128000,CODECS=\"mp4a.40.2\"\nindex.m3u8\n"))
	tsPlaylist(srv, "/a/", 2)
	if _, err := (mediaJobs{}).Probe(siteContext(t), siteFactory(t), daemon.MediaSelection{ManifestURL: srv.url("/a/master.m3u8")}); !errors.Is(err, media.ErrUnsupported) ||
		!strings.Contains(err.Error(), "no MP4 video") {
		t.Errorf("audio only: %v", err)
	}

	const tmpl = `<SegmentTemplate initialization="$RepresentationID$-init" media="$RepresentationID$-$Number$" duration="2" timescale="1" startNumber="1"/>`
	srv.add("/d/m.mpd", []byte(`<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT4S" minBufferTime="PT2S">
 <Period>
  <AdaptationSet mimeType="video/webm" contentType="video">`+tmpl+`
   <Representation id="vp9-1080" codecs="vp09.00.40.08" width="1920" height="1080" bandwidth="4000000"/>
  </AdaptationSet>
  <AdaptationSet mimeType="video/mp4" contentType="video">`+tmpl+`
   <Representation id="avc-720" codecs="avc1.4d401f" width="1280" height="720" bandwidth="2000000"/>
  </AdaptationSet>
  <AdaptationSet mimeType="audio/mp4" contentType="audio" lang="en">`+tmpl+`
   <Representation id="aac" codecs="mp4a.40.2" bandwidth="128000"/>
  </AdaptationSet>
 </Period>
</MPD>`))
	p, err = mediaJobs{}.Probe(siteContext(t), siteFactory(t), daemon.MediaSelection{ManifestURL: srv.url("/d/m.mpd")})
	if err != nil || len(p.Tracks) != 2 || p.Tracks[0].ID != "avc-720" || p.Tracks[1].ID != "aac" || p.DefaultVideo != "avc-720" ||
		p.DefaultAudio != "aac" || p.Container != "mp4" {
		t.Errorf("DASH with WebM = %+v, %v", p, err)
	}
}

// A variant the one-per-class rule leaves out of the list can still be chosen by its ID
// (`downex media --video v1`, an add from a stored pick): the probe of that selection
// lists it in place of its class's variant (sitemedia.ManifestTracksFor). Variants
// without a RESOLUTION have no class: every one is listed and can be chosen.
func TestMediaJobsProbeLeftOutVariant(t *testing.T) {
	srv := newFileServer(t)
	srv.add("/l/master.m3u8", []byte(`#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080,CODECS="avc1.640028,mp4a.40.2"
avc/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=6000000,RESOLUTION=1920x1080,CODECS="hvc1.1.6.L120.90,mp4a.40.2"
hevc/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=1500000,RESOLUTION=1280x720,CODECS="avc1.64001f,mp4a.40.2"
hd/index.m3u8
`))
	for _, dir := range []string{"avc", "hevc", "hd"} {
		tsPlaylist(srv, "/l/"+dir+"/", 2)
	}
	ids := func(p *daemon.MediaProbe) []string {
		var out []string
		for _, tr := range p.Tracks {
			out = append(out, tr.ID+" "+tr.Name)
		}
		return out
	}
	sel := daemon.MediaSelection{ManifestURL: srv.url("/l/master.m3u8")}
	p, err := mediaJobs{}.Probe(siteContext(t), siteFactory(t), sel)
	if err != nil || !slices.Equal(ids(p), []string{"v0 1080p (Full HD)", "v2 720p (HD)"}) || p.DefaultVideo != "v0" {
		t.Fatalf("probe = %v, %v", p, err)
	}
	sel.VideoTrack = "v1"
	p, err = mediaJobs{}.Probe(siteContext(t), siteFactory(t), sel)
	if err != nil || !slices.Equal(ids(p), []string{"v1 1080p (Full HD)", "v2 720p (HD)"}) || p.DefaultVideo != "v1" {
		t.Fatalf("probe of the left-out v1 = %+v, %v", p, err)
	}

	srv.add("/n/master.m3u8", []byte(`#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=800000,CODECS="avc1.4d401e,mp4a.40.2"
lo/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2000000,CODECS="avc1.4d401f,mp4a.40.2"
mid/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=5000000,CODECS="avc1.640028,mp4a.40.2"
hi/index.m3u8
`))
	for _, dir := range []string{"lo", "mid", "hi"} {
		tsPlaylist(srv, "/n/"+dir+"/", 2)
	}
	sel = daemon.MediaSelection{ManifestURL: srv.url("/n/master.m3u8")}
	if p, err = (mediaJobs{}).Probe(siteContext(t), siteFactory(t), sel); err != nil ||
		!slices.Equal(ids(p), []string{"v2 ", "v1 ", "v0 "}) || p.DefaultVideo != "v2" {
		t.Fatalf("no RESOLUTION: probe = %+v, %v", p, err)
	}
	sel.VideoTrack = "v0"
	if p, err = (mediaJobs{}).Probe(siteContext(t), siteFactory(t), sel); err != nil || p.DefaultVideo != "v0" {
		t.Fatalf("no RESOLUTION, v0: probe = %+v, %v", p, err)
	}
}

// jobFakes stand in for internal/remux in the adapter's runs: the assembler joins the
// fragments byte by byte and answers container ("mp4" when ""), the TS check records its
// calls, the legacy join joins byte by byte.
type jobFakes struct {
	mu        sync.Mutex
	container string
	assembled []remux.Input
	checked   []remux.Kind
	concats   int
}

func (f *jobFakes) jobs() mediaJobs {
	join := func(paths []string, out string) error {
		var b bytes.Buffer
		for _, p := range paths {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			b.Write(data)
		}
		return os.WriteFile(out, b.Bytes(), 0o600)
	}
	return mediaJobs{
		assemble: func(_ context.Context, inputs []remux.Input, out string, _ remux.Window) (remux.Result, error) {
			f.mu.Lock()
			f.assembled = append(f.assembled, inputs...)
			container := f.container
			f.mu.Unlock()
			if container == "" {
				container = "mp4"
			}
			var paths []string
			for _, in := range inputs {
				if in.Init != "" {
					paths = append(paths, in.Init)
				}
				paths = append(paths, in.Fragments...)
			}
			return remux.Result{Container: container, Exact: true}, join(paths, out)
		},
		checkTS: func(_ []byte, k remux.Kind) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.checked = append(f.checked, k)
			return nil
		},
		concatTS: func(_ context.Context, in remux.Input, out string) error {
			f.mu.Lock()
			f.concats++
			f.mu.Unlock()
			return join(in.Fragments, out)
		},
	}
}

// The adapter runs every download as an MP4 (MP4Only; transport streams are converted,
// their start checked first) and a legacy one (LegacyTS) as the byte-for-byte .ts join.
func TestMediaJobsRun(t *testing.T) {
	srv := newFileServer(t)
	tsMaster(srv, "/t/")
	sel := daemon.MediaSelection{ManifestURL: srv.url("/t/master.m3u8")}
	p, err := mediaJobs{}.Probe(siteContext(t), siteFactory(t), sel)
	if err != nil {
		t.Fatal(err)
	}
	sel.VideoTrack = p.DefaultVideo
	for _, tr := range p.Tracks {
		if tr.ID == p.DefaultVideo {
			sel.VideoKey = tr.Key
		}
	}
	want := tsPlaylist(srv, "/t/aac/", 3)

	var f jobFakes
	opts := runOptions(t, daemon.MediaRunOptions{})
	c, err := f.jobs().Run(siteContext(t), opts, sel, nil, nil, nil)
	if err != nil || c != "mp4" {
		t.Fatalf("Run = %q, %v", c, err)
	}
	if got, err := os.ReadFile(opts.OutPath); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("output: %d bytes, %v", len(got), err)
	}
	if len(f.assembled) != 1 || f.assembled[0].Container != "ts" || !slices.Equal(f.checked, []remux.Kind{remux.KindMuxed}) || f.concats != 0 {
		t.Errorf("assembled %+v, checked %v, legacy joins %d", f.assembled, f.checked, f.concats)
	}

	// An assembly that is not an MP4 is refused before it is published.
	f = jobFakes{container: "ts"}
	opts = runOptions(t, daemon.MediaRunOptions{})
	if _, err := f.jobs().Run(siteContext(t), opts, sel, nil, nil, nil); !errors.Is(err, media.ErrUnsupported) {
		t.Errorf("assembled as ts: %v", err)
	}
	if _, err := os.Stat(opts.OutPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("an output was published: %v", err)
	}

	// A legacy download: joined byte for byte, no check.
	f = jobFakes{}
	opts = runOptions(t, daemon.MediaRunOptions{LegacyTS: true})
	if c, err := f.jobs().Run(siteContext(t), opts, sel, nil, nil, nil); err != nil || c != "ts" {
		t.Fatalf("legacy Run = %q, %v", c, err)
	}
	if got, _ := os.ReadFile(opts.OutPath); !bytes.Equal(got, want) || f.concats != 1 || len(f.assembled) != 0 || len(f.checked) != 0 {
		t.Errorf("legacy run: %d bytes, joins %d, assembled %d, checked %d", len(got), f.concats, len(f.assembled), len(f.checked))
	}
}
