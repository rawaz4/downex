package mediajob

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"downex/internal/media"
	"downex/internal/remux"
	"downex/internal/testmedia"
)

// Transport streams (docs/VIDEO-DETECT-SPEC.md §3.3, §4.8): ProbeMP4 offers the HLS
// variants of MPEG-TS segments that the remuxer converts, Run checks the start of each
// transport stream before it fetches anything else, and every TS selection is saved as
// an MP4 (a record of before 2026-10-23 keeps its byte-for-byte .ts with LegacyTS).

// addSegments serves the named segments (TS filler packets) under dir and returns them
// joined.
func addSegments(s *server, dir string, names []string, seed uint64) []byte {
	var all []byte
	for i, name := range names {
		data := tsSegment(seed*1000+uint64(i), 3+i%4)
		s.add(dir+name, data, "video/mp2t")
		all = append(all, data...)
	}
	return all
}

// addNamedPlaylist serves a media playlist of n segments named seg<i><ext> under dir
// and returns the segments joined.
func addNamedPlaylist(s *server, dir, ext string, n int, seed uint64) []byte {
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("seg%d%s", i, ext)
	}
	all := addSegments(s, dir, names, seed)
	s.add(dir+"index.m3u8", []byte(mediaPlaylist("", names)), hlsType)
	return all
}

// segHits returns the hits of seg<i><ext> under dir for i < n.
func segHits(s *server, dir, ext string, n int) []int {
	hits := make([]int, n)
	for i := range hits {
		hits[i] = s.hitCount(fmt.Sprintf("%sseg%d%s", dir, i, ext))
	}
	return hits
}

// masterVariants offers the transport-stream variants of TestProbeMP4TransportStreams.
const masterVariants = `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=4000000,RESOLUTION=1920x1080,CODECS="avc1.640028,ac-3"
ac3/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1280x720,CODECS="avc1.64001f,mp4a.40.2"
hi/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=1000000,RESOLUTION=854x480
nocodecs/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=600000,RESOLUTION=640x360,CODECS="avc1.4d401e,mp4a.40.2"
bin/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=300000,RESOLUTION=426x240,CODECS="avc1.42c015,mp4a.40.2"
webm/index.m3u8
`

// ProbeMP4 lists the MPEG-TS variants it can convert: named by .ts segments or by
// nothing (no EXT-X-MAP), with convertible CODECS or none. AC-3 in TS and WebM are not
// listed; nothing of the segments is fetched; the output is an MP4.
func TestProbeMP4TransportStreams(t *testing.T) {
	s := newServer(t)
	s.add("/v/master.m3u8", []byte(masterVariants), hlsType)
	addNamedPlaylist(s, "/v/ac3/", ".ts", 3, 1)
	addNamedPlaylist(s, "/v/hi/", ".ts", 4, 2)
	addNamedPlaylist(s, "/v/nocodecs/", ".ts", 3, 3)
	addNamedPlaylist(s, "/v/bin/", ".bin", 3, 4)
	addNamedPlaylist(s, "/v/webm/", ".webm", 3, 5)

	info, err := ProbeMP4(testContext(t), testFactory(t), Selection{ManifestURL: s.url("/v/master.m3u8")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := trackIDs(info.Tracks); !slices.Equal(got, []string{"v1", "v2", "v3"}) {
		t.Fatalf("tracks = %v, want the convertible transport streams v1, v2, v3", got)
	}
	if info.Format != "hls" || info.Container != "mp4" || info.DefaultVideo != "v1" || info.DefaultAudio != "" ||
		info.FragmentsTotal != 4 || info.Duration != 16*time.Second {
		t.Fatalf("info = %+v", info)
	}
	if info.Tracks[0].Codecs != "avc1.64001f,mp4a.40.2" || info.Tracks[0].Kind != media.KindMuxed || info.Tracks[1].Codecs != "" {
		t.Errorf("listed tracks = %+v", info.Tracks)
	}
	for _, dir := range []string{"ac3", "hi", "nocodecs", "bin", "webm"} {
		if n := s.hitCount("/v/" + dir + "/index.m3u8"); n != 1 {
			t.Errorf("%s/index.m3u8 fetched %d times, want once", dir, n)
		}
	}
	for _, dir := range []string{"/v/ac3/", "/v/hi/", "/v/nocodecs/"} {
		if h := segHits(s, dir, ".ts", 3); slices.Max(h) != 0 {
			t.Errorf("segments of %s fetched at probe time: %v", dir, h)
		}
	}

	// A named variant of each kind: listed ones resolve, the others are not found.
	for id, want := range map[string]error{"v3": nil, "v2": nil, "v0": media.ErrNotFound, "v4": media.ErrNotFound} {
		info, err := ProbeMP4(testContext(t), testFactory(t), Selection{ManifestURL: s.url("/v/master.m3u8"), VideoTrack: id}, nil)
		switch {
		case want == nil && (err != nil || info.DefaultVideo != id):
			t.Errorf("%s: %+v, %v", id, info, err)
		case want != nil && (!errors.Is(err, want) || !errors.Is(err, ErrInvalidSelection)):
			t.Errorf("%s: err = %v, want %v", id, err, want)
		}
	}

	// A master whose only variant carries AC-3 in TS has no MP4 video.
	s.add("/v/ac3only.m3u8", []byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=4000000,RESOLUTION=1920x1080,CODECS=\"avc1.640028,ac-3\"\nac3/index.m3u8\n"), hlsType)
	if _, err := ProbeMP4(testContext(t), testFactory(t), Selection{ManifestURL: s.url("/v/ac3only.m3u8")}, nil); !errors.Is(err, media.ErrUnsupported) ||
		!strings.Contains(err.Error(), "no MP4 video in this stream") {
		t.Errorf("AC-3 only: err = %v", err)
	}
}

// A TS variant whose CODECS also name its subtitle format (wvtt), or spell the AAC
// object type with a leading zero, is listed: subtitles are not a converted track.
func TestProbeMP4TransportStreamWithSubtitleCodec(t *testing.T) {
	s := newServer(t)
	s.add("/y/master.m3u8", []byte(`#EXTM3U
#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="English",LANGUAGE="en",URI="subs/index.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=600000,RESOLUTION=640x360,CODECS="avc1.4d401e,mp4a.40.02,wvtt",SUBTITLES="subs"
v360/index.m3u8
`), hlsType)
	addTSPlaylist(s, "/y/v360/", 3, 2)
	info, err := ProbeMP4(testContext(t), testFactory(t), Selection{ManifestURL: s.url("/y/master.m3u8")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := trackIDs(info.Tracks); !slices.Equal(got, []string{"v0"}) || info.Container != "mp4" {
		t.Fatalf("tracks = %v, container %q", got, info.Container)
	}
}

// A media playlist of WebVTT segments (a player's subtitle playlist, detected like any
// other .m3u8) is not a transport stream: no MP4 video, and nothing of it is fetched.
func TestProbeMP4SubtitlePlaylistRefused(t *testing.T) {
	s := newServer(t)
	for _, ext := range []string{".vtt", ".webvtt"} {
		dir := "/subs" + strings.TrimPrefix(ext, ".") + "/"
		addNamedPlaylist(s, dir, ext, 2, 6)
		_, err := ProbeMP4(testContext(t), testFactory(t), Selection{ManifestURL: s.url(dir + "index.m3u8")}, nil)
		if !errors.Is(err, media.ErrUnsupported) || !strings.Contains(err.Error(), "no MP4 video in this stream") {
			t.Errorf("%s: err = %v, want no MP4 video", ext, err)
		}
		if s.hitCount(dir+"seg0"+ext) != 0 {
			t.Errorf("%s: a segment was fetched", ext)
		}
	}
}

// bestPerHeight is an offer like sitemedia.ManifestTracks: one video per height (the
// highest bandwidth), highest first, then the audio tracks. It records what it offered.
type bestPerHeight struct {
	mu      sync.Mutex
	offered [][]string
}

func (b *bestPerHeight) offer(ts []media.Track) []media.Track {
	ts = byHeight(ts)
	var out []media.Track
	seen := make(map[int]bool)
	for _, t := range ts {
		if t.Kind == media.KindAudio {
			out = append(out, t)
			continue
		}
		if !seen[t.Height] {
			seen[t.Height] = true
			best := t
			for _, o := range ts {
				if o.Kind != media.KindAudio && o.Height == t.Height && o.Bandwidth > best.Bandwidth {
					best = o
				}
			}
			out = append(out, best)
		}
	}
	b.mu.Lock()
	b.offered = append(b.offered, trackIDs(out))
	b.mu.Unlock()
	return out
}

// When the best variant of a quality cannot be saved (AC-3 in TS), ProbeMP4 asks the
// offer once more without it, so that the quality falls back to its next variant; only
// the newly offered variants are planned, and there are never more than two rounds.
func TestProbeMP4SecondOfferRound(t *testing.T) {
	s := newServer(t)
	s.add("/r/master.m3u8", []byte(`#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=3000000,RESOLUTION=1280x720,CODECS="avc1.64001f,ac-3"
ac3/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1280x720,CODECS="avc1.64001f,mp4a.40.2"
aac/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360,CODECS="avc1.4d401e,mp4a.40.2"
low/index.m3u8
`), hlsType)
	for i, dir := range []string{"ac3", "aac", "low"} {
		addNamedPlaylist(s, "/r/"+dir+"/", ".ts", 2, uint64(i))
	}
	var off bestPerHeight
	info, err := ProbeMP4(testContext(t), testFactory(t), Selection{ManifestURL: s.url("/r/master.m3u8")}, off.offer)
	if err != nil {
		t.Fatal(err)
	}
	if got := trackIDs(info.Tracks); !slices.Equal(got, []string{"v1", "v2"}) || info.DefaultVideo != "v1" {
		t.Fatalf("tracks %v, default %q; want the AAC 720p variant v1 and v2", got, info.DefaultVideo)
	}
	if want := [][]string{{"v0", "v2"}, {"v1", "v2"}}; !slices.EqualFunc(off.offered, want, slices.Equal) {
		t.Errorf("offers %v, want %v", off.offered, want)
	}
	for _, dir := range []string{"ac3", "aac", "low"} {
		if n := s.hitCount("/r/" + dir + "/index.m3u8"); n != 1 {
			t.Errorf("%s/index.m3u8 fetched %d times, want once", dir, n)
		}
	}

	// Two refused variants of one quality: the second round offers the other, which is
	// refused too, and no third round follows.
	s.add("/r/two.m3u8", []byte(`#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=3000000,RESOLUTION=1280x720,CODECS="avc1.64001f,ac-3"
ac3/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2500000,RESOLUTION=1280x720,CODECS="avc1.64001f,ec-3"
ec3/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1280x720,CODECS="avc1.64001f,mp4a.40.2"
aac/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360,CODECS="avc1.4d401e,mp4a.40.2"
low/index.m3u8
`), hlsType)
	addNamedPlaylist(s, "/r/ec3/", ".ts", 2, 9)
	off = bestPerHeight{}
	info, err = ProbeMP4(testContext(t), testFactory(t), Selection{ManifestURL: s.url("/r/two.m3u8")}, off.offer)
	if err != nil {
		t.Fatal(err)
	}
	if got := trackIDs(info.Tracks); !slices.Equal(got, []string{"v3"}) || len(off.offered) != 2 {
		t.Fatalf("tracks %v after offers %v; want only v3 after two rounds", got, off.offered)
	}
}

// Pairing (docs/VIDEO-DETECT-SPEC.md §3.3): a TS video goes with TS audio and an fMP4
// video with fMP4 audio. A video-only track is listed when the presentation has audio
// it can be paired with, or no audio at all.
func TestProbeMP4Pairing(t *testing.T) {
	s := newServer(t)
	addMixedHLS(s) // fMP4 v0 720p, TS v1 360p, fMP4 v2 1080p; fMP4 r0, TS r1
	sel := Selection{ManifestURL: s.url("/x/master.m3u8")}
	probe := func(sel Selection) (*Info, error) {
		return ProbeMP4(testContext(t), testFactory(t), sel, nil)
	}
	info, err := probe(sel)
	if err != nil {
		t.Fatal(err)
	}
	if got := trackIDs(info.Tracks); !slices.Equal(got, []string{"v0", "v1", "v2", "r0", "r1"}) ||
		info.DefaultVideo != "v0" || info.DefaultAudio != "r0" {
		t.Fatalf("tracks %v, default %q + %q", got, info.DefaultVideo, info.DefaultAudio)
	}
	for _, c := range []struct {
		video, audio         string
		wantVideo, wantAudio string
		wantErr              error
	}{
		{video: "v1", wantVideo: "v1", wantAudio: "r1"}, // TS video: the TS audio
		{video: "v2", wantVideo: "v2", wantAudio: "r0"}, // fMP4 video: the fMP4 audio
		{audio: "r1", wantVideo: "v1", wantAudio: "r1"}, // TS audio: the first TS video
		{video: "v1", audio: "r0", wantErr: ErrInvalidSelection},
		{video: "v0", audio: "r1", wantErr: ErrInvalidSelection},
	} {
		named := sel
		named.VideoTrack, named.AudioTrack = c.video, c.audio
		info, err := probe(named)
		switch {
		case c.wantErr != nil:
			if !errors.Is(err, c.wantErr) {
				t.Errorf("%s + %s: err = %v, want %v", c.video, c.audio, err, c.wantErr)
			}
		case err != nil || info.DefaultVideo != c.wantVideo || info.DefaultAudio != c.wantAudio || info.Container != "mp4":
			t.Errorf("%s + %s: %+v, %v; want %s + %s", c.video, c.audio, info, err, c.wantVideo, c.wantAudio)
		}
	}

	// A TS video whose presentation has fMP4 audio only is not listed.
	s.add("/p/master.m3u8", []byte(`#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="English",LANGUAGE="en",DEFAULT=YES,URI="audio/index.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=1500000,RESOLUTION=1280x720,CODECS="avc1.64001f,mp4a.40.2",AUDIO="aud"
fmp4/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=600000,RESOLUTION=640x360,CODECS="avc1.4d401e,mp4a.40.2",AUDIO="aud"
ts/index.m3u8
`), hlsType)
	addFMP4Playlist(s, "/p/fmp4/", 2, 1)
	addNamedPlaylist(s, "/p/ts/", ".ts", 2, 2)
	addFMP4Playlist(s, "/p/audio/", 2, 3)
	if info, err := probe(Selection{ManifestURL: s.url("/p/master.m3u8")}); err != nil ||
		!slices.Equal(trackIDs(info.Tracks), []string{"v0", "r0"}) {
		t.Errorf("TS video without TS audio: %+v, %v", info, err)
	}

	// Its only audio packed (raw AAC segments, not a transport stream): no MP4 video.
	s.add("/k/master.m3u8", []byte(`#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="English",LANGUAGE="en",DEFAULT=YES,URI="packed/index.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=600000,RESOLUTION=640x360,CODECS="avc1.4d401e,mp4a.40.2",AUDIO="aud"
video/index.m3u8
`), hlsType)
	addNamedPlaylist(s, "/k/video/", ".ts", 2, 4)
	addNamedPlaylist(s, "/k/packed/", ".aac", 2, 5)
	if _, err := probe(Selection{ManifestURL: s.url("/k/master.m3u8")}); !errors.Is(err, media.ErrUnsupported) {
		t.Errorf("packed audio only: err = %v, want no MP4 video", err)
	}
	// With a TS rendition beside the packed one, the TS pair is listed.
	s.add("/k/both.m3u8", []byte(`#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="English",LANGUAGE="en",DEFAULT=YES,URI="packed/index.m3u8"
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="Deutsch",LANGUAGE="de",URI="tsaudio/index.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=600000,RESOLUTION=640x360,CODECS="avc1.4d401e,mp4a.40.2",AUDIO="aud"
video/index.m3u8
`), hlsType)
	addNamedPlaylist(s, "/k/tsaudio/", ".ts", 2, 6)
	if info, err := probe(Selection{ManifestURL: s.url("/k/both.m3u8")}); err != nil ||
		!slices.Equal(trackIDs(info.Tracks), []string{"v0", "r1"}) || info.DefaultAudio != "r1" {
		t.Errorf("packed and TS audio: %+v, %v", info, err)
	}

	// A silent video: video only, and no audio in the presentation.
	s.add("/q/master.m3u8", []byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=600000,RESOLUTION=640x360,CODECS=\"avc1.4d401e\"\nv/index.m3u8\n"), hlsType)
	addNamedPlaylist(s, "/q/v/", ".ts", 2, 7)
	info, err = probe(Selection{ManifestURL: s.url("/q/master.m3u8")})
	if err != nil || !slices.Equal(trackIDs(info.Tracks), []string{"v0"}) || info.Tracks[0].Kind != media.KindVideo ||
		info.DefaultVideo != "v0" || info.DefaultAudio != "" {
		t.Errorf("silent video: %+v, %v", info, err)
	}
}

// masterTSAudio offers a TS video-only variant with a separate TS audio rendition.
const masterTSAudio = `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="Deutsch",LANGUAGE="de",DEFAULT=YES,URI="audio/index.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=1500000,RESOLUTION=1280x720,CODECS="avc1.64001f,mp4a.40.2",AUDIO="aud"
video/index.m3u8
`

// A TS video with a separate TS audio rendition is one MP4 of two inputs, each with
// its discontinuities (Breaks) and language.
func TestRunTSInputs(t *testing.T) {
	s := newServer(t)
	s.add("/i/master.m3u8", []byte(masterTSAudio), hlsType)
	names := []string{"map1.ts", "s0.ts", "s1.ts", "s2.ts", "map2.ts", "s3.ts", "s4.ts", "s5.ts"}
	addSegments(s, "/i/video/", names, 1)
	s.add("/i/video/index.m3u8", []byte(`#EXTM3U
#EXT-X-VERSION:7
#EXT-X-TARGETDURATION:4
#EXT-X-PLAYLIST-TYPE:VOD
#EXT-X-MAP:URI="map1.ts"
#EXTINF:4.0,
s0.ts
#EXTINF:4.0,
s1.ts
#EXT-X-DISCONTINUITY
#EXTINF:4.0,
s2.ts
#EXT-X-DISCONTINUITY
#EXT-X-MAP:URI="map2.ts"
#EXTINF:4.0,
s3.ts
#EXT-X-GAP
#EXTINF:4.0,
s4.ts
#EXTINF:4.0,
s5.ts
#EXT-X-ENDLIST
`), hlsType)
	addNamedPlaylist(s, "/i/audio/", ".ts", 3, 2)

	info, err := ProbeMP4(testContext(t), testFactory(t), Selection{ManifestURL: s.url("/i/master.m3u8")}, nil)
	if err != nil || info.DefaultVideo != "v0" || info.DefaultAudio != "r0" || info.Container != "mp4" {
		t.Fatalf("probe = %+v, %v", info, err)
	}
	asm := &fakeAssembler{}
	opts := testOptions(t, asm)
	var check tsChecker
	opts.CheckTS = check.check
	c, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/i/master.m3u8")}, nil, nil, nil)
	if err != nil || c != "mp4" {
		t.Fatalf("Run = %q, %v", c, err)
	}
	if len(asm.inputs) != 2 {
		t.Fatalf("inputs = %+v", asm.inputs)
	}
	v, a := asm.inputs[0], asm.inputs[1]
	if v.Kind != remux.KindVideo || v.Container != "ts" || v.Init != "" || len(v.Fragments) != 7 ||
		!slices.Equal(v.Breaks, []int{3, 4, 6}) || v.Language != "" {
		t.Errorf("video input = %+v", v)
	}
	if a.Kind != remux.KindAudio || a.Container != "ts" || len(a.Fragments) != 3 || a.Breaks != nil || a.Language != "de" {
		t.Errorf("audio input = %+v", a)
	}
	if s.hitCount("/i/video/s4.ts") != 0 {
		t.Error("a gap segment was fetched")
	}
	// Each transport stream was checked once, from its map and first segment.
	prefixes, kinds := check.calls()
	if len(prefixes) != 2 || !slices.Equal(kinds, []remux.Kind{remux.KindVideo, remux.KindAudio}) {
		t.Fatalf("CheckTS calls: %d, kinds %v", len(prefixes), kinds)
	}
	vmap, _ := os.ReadFile(v.Fragments[0])
	vfirst, _ := os.ReadFile(v.Fragments[1])
	afirst, _ := os.ReadFile(a.Fragments[0])
	if !bytes.Equal(prefixes[0], append(vmap, vfirst...)) || !bytes.Equal(prefixes[1], afirst) {
		t.Errorf("CheckTS prefixes of %d and %d bytes", len(prefixes[0]), len(prefixes[1]))
	}
}

// A refusing CheckTS stops the job after the first segment of each transport stream:
// nothing else is fetched and nothing is assembled. The error keeps its kind
// (encrypted, media_unsupported, media_invalid in the daemon).
func TestRunTSEarlyCheckRefuses(t *testing.T) {
	for _, want := range []error{media.ErrEncrypted, remux.ErrUnsupported, remux.ErrMalformed} {
		t.Run(want.Error(), func(t *testing.T) {
			s := newServer(t)
			first := addNamedPlaylist(s, "/e/", ".ts", 5, 3)[:len(tsSegment(3000, 3))]
			asm := &fakeAssembler{}
			opts := testOptions(t, asm)
			check := tsChecker{err: fmt.Errorf("pid 256: %w", want)}
			opts.CheckTS = check.check
			_, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/e/index.m3u8")}, nil, nil, nil)
			if !errors.Is(err, want) || strings.Contains(err.Error(), s.ts.URL) {
				t.Fatalf("err = %v, want %v", err, want)
			}
			if h := segHits(s, "/e/", ".ts", 5); !slices.Equal(h, []int{1, 0, 0, 0, 0}) {
				t.Errorf("segment hits %v, want only the first", h)
			}
			if prefixes, kinds := check.calls(); len(prefixes) != 1 || !bytes.Equal(prefixes[0], first) ||
				kinds[0] != remux.KindMuxed {
				t.Errorf("CheckTS was given %d prefixes, kinds %v", len(prefixes), kinds)
			}
			if asm.callCount() != 0 {
				t.Error("assembled after a refusal")
			}
			if _, err := os.Stat(opts.OutPath); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("an output exists: %v", err)
			}
		})
	}

	// Two transport streams: both first segments are fetched before anything else, and
	// a refused audio stops the video too.
	s := newServer(t)
	s.add("/i/master.m3u8", []byte(masterTSAudio), hlsType)
	addNamedPlaylist(s, "/i/video/", ".ts", 4, 1)
	addNamedPlaylist(s, "/i/audio/", ".ts", 4, 2)
	opts := testOptions(t, &fakeAssembler{})
	check := tsChecker{err: fmt.Errorf("no AAC: %w", remux.ErrUnsupported), kind: remux.KindAudio}
	opts.CheckTS = check.check
	if _, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/i/master.m3u8")}, nil, nil, nil); !errors.Is(err, remux.ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
	if v, a := segHits(s, "/i/video/", ".ts", 4), segHits(s, "/i/audio/", ".ts", 4); !slices.Equal(v, []int{1, 0, 0, 0}) ||
		!slices.Equal(a, []int{1, 0, 0, 0}) {
		t.Errorf("segment hits: video %v, audio %v", v, a)
	}
}

// CheckTS gets the EXT-X-MAP section followed by the first segment, at most
// remux.MaxCheckTSBytes of them.
func TestRunTSEarlyCheckPrefix(t *testing.T) {
	s := newServer(t)
	tables := tsSegment(1, 2)
	big := tsSegment(2, remux.MaxCheckTSBytes/188+10)
	s.add("/b/map.ts", tables, "video/mp2t")
	s.add("/b/seg0.ts", big, "video/mp2t")
	s.add("/b/seg1.ts", tsSegment(3, 3), "video/mp2t")
	s.add("/b/index.m3u8", []byte(mediaPlaylist("map.ts", []string{"seg0.ts", "seg1.ts"})), hlsType)
	asm := &fakeAssembler{}
	opts := testOptions(t, asm)
	var check tsChecker
	opts.CheckTS = check.check
	if c, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/b/index.m3u8")}, nil, nil, nil); err != nil || c != "mp4" {
		t.Fatalf("Run = %q, %v", c, err)
	}
	prefixes, _ := check.calls()
	want := append(slices.Clone(tables), big...)[:remux.MaxCheckTSBytes]
	if len(prefixes) != 1 || !bytes.Equal(prefixes[0], want) {
		t.Fatalf("CheckTS prefixes: %d, first %d bytes; want %d", len(prefixes), len(prefixes[0]), len(want))
	}
	if in := asm.inputs[0]; in.Container != "ts" || in.Init != "" || len(in.Fragments) != 3 || in.Breaks != nil {
		t.Errorf("input = %+v", in)
	}
}

// An HLS track whose container the playlist leaves open is identified from its first
// segment before anything else is fetched: data that is neither MPEG-TS nor fMP4 (raw
// AAC, as packed audio is) is refused; a transport stream is checked, also when a
// resumed job finds that segment on disk.
func TestRunSniffsUnknownContainer(t *testing.T) {
	s := newServer(t)
	adts := bytes.Repeat([]byte{0xff, 0xf1, 0x50, 0x80, 0x02, 0x1f, 0xfc, 1, 2, 3, 4, 5, 6, 7, 8, 9}, 16)
	var names []string
	for i := range 4 {
		names = append(names, fmt.Sprintf("seg%d.bin", i))
		s.add(fmt.Sprintf("/a/seg%d.bin", i), adts, "application/octet-stream")
	}
	s.add("/a/index.m3u8", []byte(mediaPlaylist("", names)), hlsType)
	asm := &fakeAssembler{}
	opts := testOptions(t, asm)
	var check tsChecker
	opts.CheckTS = check.check
	_, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/a/index.m3u8")}, nil, nil, nil)
	if !errors.Is(err, media.ErrUnsupported) || !strings.Contains(err.Error(), "neither MPEG-TS nor fragmented MP4") {
		t.Fatalf("raw AAC: err = %v", err)
	}
	if h := segHits(s, "/a/", ".bin", 4); !slices.Equal(h, []int{1, 0, 0, 0}) {
		t.Errorf("segment hits %v, want only the first", h)
	}
	if p, _ := check.calls(); len(p) != 0 || asm.callCount() != 0 {
		t.Errorf("CheckTS called %d times, assembled %d times", len(p), asm.callCount())
	}

	// Transport stream data: checked, then the job pauses at segment 2 and resumes.
	want := addNamedPlaylist(s, "/t/", ".bin", 4, 7)
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()
	var once sync.Once
	s.setHook(func(r *http.Request) {
		if r.URL.Path == "/t/seg2.bin" {
			once.Do(func() {
				cancel()
				select {
				case <-r.Context().Done():
				case <-time.After(10 * time.Second):
				}
			})
		}
	})
	opts = testOptions(t, asm)
	opts.MaxConnections = 1
	check = tsChecker{}
	opts.CheckTS = check.check
	var rec recorder
	if _, err := Run(ctx, opts, Selection{ManifestURL: s.url("/t/index.m3u8")}, nil, nil, rec.onCheckpoint); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	s.setHook(nil)
	if c, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/t/index.m3u8")}, rec.last(), nil, nil); err != nil || c != "mp4" {
		t.Fatalf("resumed Run = %q, %v", c, err)
	}
	if got := readFile(t, opts.OutPath); !bytes.Equal(got, want) || asm.inputs[0].Container != "ts" {
		t.Fatalf("output: %d bytes, want %d; inputs %+v", len(got), len(want), asm.inputs)
	}
	first := readFile(t, asm.inputs[0].Fragments[0])
	if prefixes, _ := check.calls(); len(prefixes) != 2 || !bytes.Equal(prefixes[0], first) || !bytes.Equal(prefixes[1], first) {
		t.Errorf("CheckTS calls: %d", len(prefixes))
	}
	if h := segHits(s, "/t/", ".bin", 4); h[0] != 1 || h[1] != 1 {
		t.Errorf("segment hits %v: the resumed job fetched done segments again", h)
	}
}

// dashUntyped is an MPD that names no type: its Representation is assumed fragmented
// MP4 until its data says otherwise.
const dashUntyped = `<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT12S">
<Period><AdaptationSet contentType="video">
<SegmentTemplate timescale="1000" duration="4000" initialization="v/init.bin" media="v/$Number$.bin" startNumber="1"/>
<Representation id="v" bandwidth="1000" width="640" height="360" codecs="avc1.4d401e"/></AdaptationSet></Period></MPD>`

// A DASH track found to be a transport stream by its initialization data is checked
// from that data and its first segment, fetched next, before any other segment.
func TestRunDASHTransportStreamChecked(t *testing.T) {
	s := newServer(t)
	s.add("/u/manifest.mpd", []byte(dashUntyped), "application/dash+xml")
	s.add("/u/v/init.bin", tsSegment(1, 2), "application/octet-stream")
	for i := 1; i <= 3; i++ {
		s.add(fmt.Sprintf("/u/v/%d.bin", i), tsSegment(uint64(1+i), 3), "application/octet-stream")
	}
	asm := &fakeAssembler{}
	opts := testOptions(t, asm)
	check := tsChecker{err: fmt.Errorf("scrambled: %w", media.ErrEncrypted)}
	opts.CheckTS = check.check
	_, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/u/manifest.mpd")}, nil, nil, nil)
	if !errors.Is(err, media.ErrEncrypted) || s.hitCount("/u/v/1.bin") != 1 || s.hitCount("/u/v/2.bin") != 0 || s.hitCount("/u/v/3.bin") != 0 {
		t.Fatalf("err = %v; segment hits %d %d %d", err, s.hitCount("/u/v/1.bin"), s.hitCount("/u/v/2.bin"), s.hitCount("/u/v/3.bin"))
	}
	prefixes, _ := check.calls()
	if want := append(tsSegment(1, 2), tsSegment(2, 3)...); len(prefixes) != 1 || !bytes.Equal(prefixes[0], want) {
		t.Errorf("CheckTS prefixes: %d", len(prefixes))
	}

	// Accepted, it is saved as an MP4 of one TS input (the tables first).
	opts = testOptions(t, asm)
	c, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/u/manifest.mpd")}, nil, nil, nil)
	if err != nil || c != "mp4" {
		t.Fatalf("Run = %q, %v", c, err)
	}
	if in := asm.inputs[0]; in.Container != "ts" || in.Init != "" || len(in.Fragments) != 4 {
		t.Errorf("input = %+v", in)
	}
}

// LegacyTS (a record stored as ".ts" before 2026-10-23, resumed) joins its one transport
// stream byte for byte with ConcatTS: no CheckTS, no conversion, whatever MP4Only says.
// A TS video with separate TS audio cannot be joined that way.
func TestRunLegacyTS(t *testing.T) {
	s := newServer(t)
	want := addTSPlaylist(s, "/l/", 4, 5)
	for _, mp4Only := range []bool{false, true} {
		asm := &fakeAssembler{}
		opts := testOptions(t, asm)
		var check tsChecker
		var concat fakeConcat
		opts.CheckTS, opts.ConcatTS, opts.LegacyTS, opts.MP4Only = check.check, concat.concat, true, mp4Only
		c, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/l/index.m3u8")}, nil, nil, nil)
		if err != nil || c != "ts" {
			t.Fatalf("MP4Only %v: Run = %q, %v", mp4Only, c, err)
		}
		if got := readFile(t, opts.OutPath); !bytes.Equal(got, want) {
			t.Fatalf("output: %d bytes, want %d", len(got), len(want))
		}
		n, inputs := concat.recorded()
		if n != 1 || inputs[0].Container != "ts" || inputs[0].Kind != remux.KindMuxed || len(inputs[0].Fragments) != 4 {
			t.Errorf("ConcatTS calls %d, inputs %+v", n, inputs)
		}
		if p, _ := check.calls(); len(p) != 0 || asm.callCount() != 0 {
			t.Errorf("CheckTS called %d times, Assemble %d times", len(p), asm.callCount())
		}
	}

	s.add("/i/master.m3u8", []byte(masterTSAudio), hlsType)
	addNamedPlaylist(s, "/i/video/", ".ts", 2, 1)
	addNamedPlaylist(s, "/i/audio/", ".ts", 2, 2)
	opts := testOptions(t, &fakeAssembler{})
	opts.LegacyTS = true
	if _, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/i/master.m3u8")}, nil, nil, nil); !errors.Is(err, media.ErrUnsupported) {
		t.Errorf("legacy TS video + TS audio: err = %v", err)
	}
	if s.hitCount("/i/video/seg0.ts")+s.hitCount("/i/audio/seg0.ts") != 0 {
		t.Error("segments were fetched for a combination that cannot be joined")
	}
}

// Run refuses a transport stream that ProbeMP4 would not list (CODECS the remuxer does
// not convert, packed audio) before it fetches any segment, as a selection named by key
// may still reach it; a legacy job joins what it joined before.
func TestRunRefusesUnconvertibleTS(t *testing.T) {
	s := newServer(t)
	s.add("/v/master.m3u8", []byte(masterVariants), hlsType)
	addNamedPlaylist(s, "/v/ac3/", ".ts", 3, 1)
	asm := &fakeAssembler{}
	opts := testOptions(t, asm)
	sel := Selection{ManifestURL: s.url("/v/master.m3u8"), VideoTrack: "v0", VideoKey: keyOf(t, masterVariants, "v0")}
	if _, err := Run(testContext(t), opts, sel, nil, nil, nil); !errors.Is(err, media.ErrUnsupported) {
		t.Fatalf("AC-3 in TS: err = %v", err)
	}
	if h := segHits(s, "/v/ac3/", ".ts", 3); slices.Max(h) != 0 {
		t.Errorf("segments fetched: %v", h)
	}

	s.add("/k/master.m3u8", []byte(`#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="English",LANGUAGE="en",DEFAULT=YES,URI="packed/index.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=600000,RESOLUTION=640x360,CODECS="avc1.4d401e,mp4a.40.2",AUDIO="aud"
video/index.m3u8
`), hlsType)
	addNamedPlaylist(s, "/k/video/", ".ts", 2, 4)
	addNamedPlaylist(s, "/k/packed/", ".aac", 2, 5)
	if _, err := Run(testContext(t), testOptions(t, asm), Selection{ManifestURL: s.url("/k/master.m3u8")}, nil, nil, nil); !errors.Is(err, media.ErrUnsupported) {
		t.Fatalf("packed audio: err = %v", err)
	}
	if s.hitCount("/k/video/seg0.ts")+s.hitCount("/k/packed/seg0.aac") != 0 {
		t.Error("segments of packed audio were fetched")
	}

	opts = testOptions(t, asm)
	var concat fakeConcat
	opts.LegacyTS, opts.ConcatTS = true, concat.concat
	if c, err := Run(testContext(t), opts, sel, nil, nil, nil); err != nil || c != "ts" {
		t.Fatalf("legacy AC-3 in TS: %q, %v", c, err)
	}
}

// The real remux.ConcatTS is the default of a legacy job: the output is the segments
// byte for byte.
func TestRemuxTransportStream(t *testing.T) {
	s := newServer(t)
	want := addTSPlaylist(s, "/x/", 4, 13)
	opts := testOptions(t, nil)
	opts.LegacyTS = true
	container, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/x/index.m3u8")}, nil, nil, nil)
	if err != nil || container != "ts" {
		t.Fatalf("Run = %q, %v", container, err)
	}
	if got := readFile(t, opts.OutPath); !bytes.Equal(got, want) {
		t.Fatalf("output: %d bytes, want %d", len(got), len(want))
	}
}

func TestOutputContainer(t *testing.T) {
	const (
		ts   = media.ContainerTS
		fmp4 = media.ContainerFMP4
		webm = media.ContainerWebM
	)
	for _, c := range []struct {
		cs     []media.Container
		legacy bool
		want   string // "" = unsupported
	}{
		{[]media.Container{fmp4}, false, "mp4"},
		{[]media.Container{fmp4, fmp4}, false, "mp4"},
		{[]media.Container{ts}, false, "mp4"},
		{[]media.Container{ts, ts}, false, "mp4"},
		{[]media.Container{ts}, true, "ts"},
		{[]media.Container{ts, ts}, true, ""},
		{[]media.Container{fmp4}, true, "mp4"},
		{[]media.Container{ts, fmp4}, false, ""},
		{[]media.Container{webm}, false, ""},
		{[]media.Container{fmp4, webm}, false, ""},
		{nil, false, ""},
		{[]media.Container{ts, ts, ts}, false, ""},
	} {
		got, err := outputContainer(c.cs, c.legacy)
		if got != c.want || (c.want == "") != errors.Is(err, media.ErrUnsupported) {
			t.Errorf("outputContainer(%v, legacy %v) = %q, %v; want %q", c.cs, c.legacy, got, err, cmp.Or(c.want, "unsupported"))
		}
	}
}

// A muxed TS whose video carries no IDR picture is refused by the real remuxer
// (media_unsupported) rather than saved as an audio-only MP4, and no output is left.
func TestRunTSWithoutDecodableVideoFails(t *testing.T) {
	s := newServer(t)
	aus := testmedia.Video(testmedia.VideoConfig{Frames: 48, LeadingNonSync: 48, Seed: 21})
	frames := testmedia.AAC(testmedia.AudioConfig{Frames: 70, Seed: 22})
	m := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: 0x100, Type: testmedia.TypeH264}, {PID: 0x101, Type: testmedia.TypeAAC}}}
	segs := m.Segments(testmedia.Interleave(testmedia.VideoPES(0x100, aus), testmedia.AudioPES(0x101, frames, 5)),
		func(i int, _ testmedia.PES) bool { return i%40 == 0 })
	var names []string
	for i, seg := range segs {
		name := fmt.Sprintf("s%d.ts", i)
		s.add("/n/"+name, seg, "video/mp2t")
		names = append(names, name)
	}
	s.add("/n/index.m3u8", []byte(mediaPlaylist("", names)), hlsType)
	opts := testOptions(t, nil)
	opts.CheckTS = remux.CheckTS
	opts.MP4Only = true
	c, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/n/index.m3u8")}, nil, nil, nil)
	if !errors.Is(err, errors.ErrUnsupported) || !strings.Contains(err.Error(), "no decodable video") {
		t.Fatalf("Run = %q, %v; want an unsupported refusal", c, err)
	}
	if _, err := os.Stat(opts.OutPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output left behind: %v", err)
	}
}
