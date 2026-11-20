package mediajob

import (
	"fmt"
	"testing"

	"downex/internal/media"
)

// fmp4Level is a fragmented MP4 media playlist of n segments (never fetched by a probe).
func fmp4Level(n int) string {
	pl := "#EXTM3U\n#EXT-X-VERSION:6\n#EXT-X-TARGETDURATION:4\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-MAP:URI=\"init.mp4\"\n"
	for i := range n {
		pl += fmt.Sprintf("#EXTINF:4.000,\nseg%d.m4s\n", i)
	}
	return pl + "#EXT-X-ENDLIST\n"
}

// A Pinterest-like master: two picture-only levels and their sound in one audio rendition.
const pinMaster = `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="Audio",DEFAULT=YES,URI="abc_audio.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360,CODECS="avc1.4d401e,mp4a.40.2",AUDIO="aud"
abc_360w.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1280x720,CODECS="avc1.4d401f,mp4a.40.2",AUDIO="aud"
abc_720w.m3u8
`

// Only a level without sound was recorded: the probe and the run use its master, so the
// default has sound (MEDIA-SPEC §7, amendment 2026-11-19).
func TestBareLevelUsesItsMaster(t *testing.T) {
	s := newServer(t)
	s.add("/v/abc.m3u8", []byte(pinMaster), hlsType)
	for _, p := range []string{"/v/abc_360w.m3u8", "/v/abc_720w.m3u8", "/v/abc_audio.m3u8"} {
		s.add(p, []byte(fmp4Level(3)), hlsType)
	}
	sel := Selection{ManifestURL: s.url("/v/abc_720w.m3u8")}
	info, err := ProbeMP4(testContext(t), testFactory(t), sel, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Tracks) != 3 || info.DefaultAudio == "" || info.DefaultVideo == "" {
		t.Fatalf("probe of the level: %+v", info)
	}
	f := testFactory(t).NewSlot()
	defer f.Close()
	res, err := resolve(testContext(t), f, retrier{clock: RealClock(), max: 1}, sel, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.tracks) != 2 || res.tracks[1].kind != media.KindAudio {
		t.Fatalf("run of the level: %+v", res.tracks)
	}
}

// A level whose sibling is no master of it (or is missing) stays itself.
func TestBareLevelWithoutMaster(t *testing.T) {
	s := newServer(t)
	s.add("/w/abc.m3u8", []byte(`#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="Audio",URI="abc_audio.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=800000,AUDIO="aud"
other_360w.m3u8
`), hlsType)
	s.add("/w/abc_720w.m3u8", []byte(fmp4Level(3)), hlsType)
	s.add("/x/abc_720w.m3u8", []byte(fmp4Level(3)), hlsType)
	for _, u := range []string{s.url("/w/abc_720w.m3u8"), s.url("/x/abc_720w.m3u8")} {
		info, err := ProbeMP4(testContext(t), testFactory(t), Selection{ManifestURL: u}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(info.Tracks) != 1 || info.DefaultAudio != "" {
			t.Errorf("%s: %+v", u, info)
		}
	}
	if n := s.hitCount("/x/abc.m3u8"); n != 1 {
		t.Errorf("the missing master was asked %d times, want once", n)
	}
}
