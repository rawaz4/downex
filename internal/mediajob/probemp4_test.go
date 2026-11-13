package mediajob

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"downex/internal/media"
)

// masterMixed offers a 720p fMP4 variant, a 360p MPEG-TS variant and a 1080p fMP4
// variant, with an fMP4 and an MPEG-TS audio rendition.
const masterMixed = `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="English",LANGUAGE="en",DEFAULT=YES,URI="audio/index.m3u8"
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="TS audio",LANGUAGE="de",URI="tsaudio/index.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=1500000,RESOLUTION=1280x720,CODECS="avc1.64001f",AUDIO="aud"
v720/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=600000,RESOLUTION=640x360,CODECS="avc1.4d401e",AUDIO="aud"
v360/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=4000000,RESOLUTION=1920x1080,CODECS="avc1.640028",AUDIO="aud"
v1080/index.m3u8
`

func addMixedHLS(s *server) {
	s.add("/x/master.m3u8", []byte(masterMixed), hlsType)
	addFMP4Playlist(s, "/x/v720/", 3, 1)
	addTSPlaylist(s, "/x/v360/", 3, 2)
	addFMP4Playlist(s, "/x/v1080/", 3, 3)
	addFMP4Playlist(s, "/x/audio/", 3, 4)
	addTSPlaylist(s, "/x/tsaudio/", 3, 5)
}

func trackIDs(ts []media.Track) []string {
	ids := make([]string, 0, len(ts))
	for _, t := range ts {
		ids = append(ids, t.ID)
	}
	return ids
}

// byHeight orders video and muxed tracks by height, highest first, then audio tracks.
func byHeight(ts []media.Track) []media.Track {
	ts = slices.Clone(ts)
	slices.SortStableFunc(ts, func(a, b media.Track) int {
		if (a.Kind == media.KindAudio) != (b.Kind == media.KindAudio) {
			if a.Kind == media.KindAudio {
				return 1
			}
			return -1
		}
		return cmp.Compare(b.Height, a.Height)
	})
	return ts
}

// ProbeMP4 lists the tracks of an HLS master playlist that are saved as an MP4
// (fragmented MP4 and MPEG-TS alike), fetches each media playlist once, takes the first
// video of the offer as the default and refuses a track that is not listed.
func TestProbeMP4HLS(t *testing.T) {
	s := newServer(t)
	addMixedHLS(s)
	sel := Selection{ManifestURL: s.url("/x/master.m3u8")}

	info, err := ProbeMP4(testContext(t), testFactory(t), sel, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := trackIDs(info.Tracks); !slices.Equal(got, []string{"v0", "v1", "v2", "r0", "r1"}) {
		t.Fatalf("tracks = %v, want all of v0, v1, v2, r0, r1", got)
	}
	if info.Format != "hls" || info.Container != "mp4" || info.DefaultVideo != "v0" || info.DefaultAudio != "r0" ||
		info.FragmentsTotal != 8 {
		t.Fatalf("info = %+v", info)
	}
	for _, p := range []string{"/x/v720/index.m3u8", "/x/v360/index.m3u8", "/x/v1080/index.m3u8", "/x/audio/index.m3u8", "/x/tsaudio/index.m3u8"} {
		if n := s.hitCount(p); n != 1 {
			t.Errorf("%s fetched %d times, want once", p, n)
		}
	}

	info, err = ProbeMP4(testContext(t), testFactory(t), sel, byHeight)
	if err != nil {
		t.Fatal(err)
	}
	if got := trackIDs(info.Tracks); !slices.Equal(got, []string{"v2", "v0", "v1", "r0", "r1"}) || info.DefaultVideo != "v2" ||
		info.DefaultAudio != "r0" {
		t.Fatalf("ordered offer: tracks %v, default %q + %q", got, info.DefaultVideo, info.DefaultAudio)
	}

	sel.VideoTrack = "v0"
	if info, err := ProbeMP4(testContext(t), testFactory(t), sel, byHeight); err != nil || info.DefaultVideo != "v0" {
		t.Fatalf("named track: %+v, %v", info, err)
	}
	// A track the offer leaves out is not found.
	dropTS := func(ts []media.Track) []media.Track {
		return slices.DeleteFunc(ts, func(t media.Track) bool { return t.ID == "v1" })
	}
	named := Selection{ManifestURL: sel.ManifestURL, VideoTrack: "v1"}
	if _, err := ProbeMP4(testContext(t), testFactory(t), named, dropTS); !errors.Is(err, ErrInvalidSelection) ||
		!errors.Is(err, media.ErrNotFound) {
		t.Errorf("track left out by the offer: err = %v", err)
	}
}

// A presentation without an MP4 video (here MPEG-TS with AC-3, or audio only) is
// refused as unsupported (no MP4 video); one whose every video is encrypted keeps that
// reason.
func TestProbeMP4Refusals(t *testing.T) {
	s := newServer(t)
	s.add("/m/master.m3u8", []byte(strings.ReplaceAll(masterTS, "mp4a.40.2", "ac-3")), hlsType)
	addTSPlaylist(s, "/m/low/", 3, 1)
	addTSPlaylist(s, "/m/high/", 5, 2)
	s.add("/key/master.m3u8", []byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000,RESOLUTION=640x360,CODECS=\"avc1.4d401e\"\nv.m3u8\n"), hlsType)
	s.add("/key/v.m3u8", []byte("#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXT-X-MAP:URI=\"init.mp4\"\n"+
		"#EXT-X-KEY:METHOD=AES-128,URI=\"k\"\n#EXTINF:4,\na.m4s\n#EXT-X-ENDLIST\n"), hlsType)
	s.add("/a/master.m3u8", []byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000,CODECS=\"mp4a.40.2\"\naudio/index.m3u8\n"), hlsType)
	addFMP4Playlist(s, "/a/audio/", 2, 3)

	for _, tc := range []struct {
		path, text string
		want       error
	}{
		{"/m/master.m3u8", "no MP4 video", media.ErrUnsupported},
		{"/key/master.m3u8", "", media.ErrEncrypted},
		{"/a/master.m3u8", "no MP4 video", media.ErrUnsupported},
	} {
		_, err := ProbeMP4(testContext(t), testFactory(t), Selection{ManifestURL: s.url(tc.path)}, nil)
		if !errors.Is(err, tc.want) || !strings.Contains(fmt.Sprint(err), tc.text) {
			t.Errorf("%s: err = %v, want %v (%q)", tc.path, err, tc.want, tc.text)
		}
		if err != nil && strings.Contains(err.Error(), s.ts.URL) {
			t.Errorf("%s: the error repeats the URL: %v", tc.path, err)
		}
	}
	if s.hitCount("/m/high/seg0.ts") != 0 || s.hitCount("/a/audio/s0.m4s") != 0 {
		t.Error("ProbeMP4 fetched media data")
	}
}

// ProbeMP4 leaves out WebM Representations and SegmentBase ones of an MPD.
func TestProbeMP4DASH(t *testing.T) {
	s := newServer(t)
	addDASH(s, false)
	webm := `<AdaptationSet mimeType="video/webm" contentType="video">
      <SegmentTemplate timescale="1000" duration="2000" initialization="webm/init.webm" media="webm/$Number$.webm" startNumber="1"/>
      <Representation id="vp9-1080" bandwidth="3000000" width="1920" height="1080" codecs="vp09.00.40.08"/>
    </AdaptationSet>
    <AdaptationSet mimeType="video/mp4" contentType="video">
      <Representation id="base" bandwidth="1" width="2560" height="1440" codecs="avc1.640032">
        <BaseURL>v.mp4</BaseURL><SegmentBase indexRange="800-1000"><Initialization range="0-799"/></SegmentBase>
      </Representation>
    </AdaptationSet>
    <AdaptationSet mimeType="video/mp4" contentType="video">`
	s.add("/d/mixed.mpd", []byte(strings.Replace(dashMPD, `<AdaptationSet mimeType="video/mp4" contentType="video">`, webm, 1)),
		"application/dash+xml")
	info, err := ProbeMP4(testContext(t), testFactory(t), Selection{ManifestURL: s.url("/d/mixed.mpd")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := trackIDs(info.Tracks); !slices.Equal(got, []string{"v720", "v360", "a128"}) || info.DefaultVideo != "v720" ||
		info.DefaultAudio != "a128" || info.Container != "mp4" || info.FragmentsTotal != 10 {
		t.Fatalf("info = %+v (tracks %v)", info, got)
	}
}

// With MP4Only, Run saves an MPEG-TS selection (it is converted) and refuses what would
// not be an MP4: a track whose data turns out to be WebM before its media fragments,
// and an assembly of another container before it is published.
func TestRunMP4Only(t *testing.T) {
	s := newServer(t)
	s.add("/m/master.m3u8", []byte(masterTS), hlsType)
	addTSPlaylist(s, "/m/low/", 3, 1)
	want := addTSPlaylist(s, "/m/high/", 5, 2)
	asm := &fakeAssembler{}
	opts := testOptions(t, asm)
	opts.MP4Only = true
	c, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/m/master.m3u8")}, nil, nil, nil)
	if err != nil || c != "mp4" || asm.inputs[0].Container != "ts" {
		t.Fatalf("MPEG-TS selection: %q, %v (inputs %+v)", c, err, asm.inputs)
	}
	if got := readFile(t, opts.OutPath); !bytes.Equal(got, want) {
		t.Fatal("output differs")
	}

	// An MPD that names no type, whose initialization data is WebM.
	s.add("/u/manifest.mpd", []byte(dashUntyped), "application/dash+xml")
	s.add("/u/v/init.bin", append([]byte{0x1a, 0x45, 0xdf, 0xa3}, make([]byte, 60)...), "application/octet-stream")
	s.add("/u/v/1.bin", tsSegment(2, 3), "application/octet-stream")
	for _, mp4Only := range []bool{true, false} {
		opts = testOptions(t, asm)
		opts.MP4Only = mp4Only
		_, err = Run(testContext(t), opts, Selection{ManifestURL: s.url("/u/manifest.mpd")}, nil, nil, nil)
		if !errors.Is(err, media.ErrUnsupported) || s.hitCount("/u/v/1.bin") != 0 {
			t.Fatalf("sniffed WebM (MP4Only %v): err = %v, segment hits %d", mp4Only, err, s.hitCount("/u/v/1.bin"))
		}
	}

	// An assembler that answers another container: refused with MP4Only, nothing
	// published; without it, the container is the caller's to judge.
	other := &fakeAssembler{container: "ts"}
	n := asm.callCount()
	opts = testOptions(t, other)
	opts.MP4Only = true
	if _, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/m/master.m3u8")}, nil, nil, nil); !errors.Is(err, media.ErrUnsupported) {
		t.Fatalf("assembled as ts with MP4Only: err = %v", err)
	}
	for _, p := range []string{opts.OutPath, opts.OutPath + ".dxpart"} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s exists after the refusal: %v", p, err)
		}
	}
	opts = testOptions(t, other)
	if c, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/m/master.m3u8")}, nil, nil, nil); err != nil || c != "ts" {
		t.Fatalf("assembled as ts without MP4Only: %q, %v", c, err)
	}
	if asm.callCount() != n {
		t.Error("the WebM track was assembled")
	}
}
