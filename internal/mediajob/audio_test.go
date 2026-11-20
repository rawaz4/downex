package mediajob

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"downex/internal/media"
)

// Audio pages (docs/SITE-MEDIA-SPEC.md §3.13): one audio track is joined (a fragmented
// AAC file) or copied (an MP4 without a container, an MP3), its head checked before any
// other chunk, and MP4 data with a video track is refused.

// hdlrTrak is a 'trak' with a 'tkhd' and a 'hdlr' of handler type h in its 'mdia'.
func hdlrTrak(h string) []byte {
	hdlr := append(make([]byte, 8), []byte(h)...)
	hdlr = append(hdlr, make([]byte, 13)...)
	return box("trak", box("tkhd", make([]byte, 84)), box("mdia", box("hdlr", hdlr)))
}

func TestRunRangedAudioRemux(t *testing.T) {
	_, a := fixturePair(t)
	s := newSiteServer(t)
	s.add("/a.m4a", a.data)
	opts := rangedOptions(t)
	opts.OutPath = filepath.Join(filepath.Dir(opts.OutPath), "track.m4a")
	sel := RangedSelection{Output: OutputRemux, Tracks: []media.RangedTrack{
		rangedTrack("hls_aac", media.KindAudio, "m4a", s.url("/a.m4a"), int64(len(a.data))),
	}}
	container, err := RunRanged(testContext(t), opts, sel, nil, nil, nil)
	if err != nil || container != "mp4" {
		t.Fatalf("RunRanged = %q, %v", container, err)
	}
	checkMP4(t, opts.OutPath, a.samples) // one sound track, the samples unchanged
}

func TestRunRangedAudioCopy(t *testing.T) {
	mvhd := box("mvhd", make([]byte, 100))
	id3 := slices.Concat([]byte("ID3\x04\x00\x00\x00\x00\x02\x01"), make([]byte, 257), []byte{0xff, 0xfb, 0x90, 0x64})
	frame := []byte{0xff, 0xfb, 0x90, 0x64}
	for _, tc := range []struct {
		name, ext string
		data      []byte
		want      error
		container string
		requests  int
	}{
		{"MP3 with an ID3 tag", "mp3", slices.Concat(id3, filler(1500<<10, 21)), nil, "mp3", 2},
		{"MP3 from its first frame", "mp3", slices.Concat(frame, filler(1500<<10, 22)), nil, "mp3", 2},
		{"a web page as an MP3", "mp3", slices.Concat([]byte("<!doctype html><title>x</title>"), filler(1500<<10, 23)), media.ErrUnsupported, "", 1},
		{"an ID3 header with a bad size", "mp3", slices.Concat([]byte("ID3\x04\x00\x00\x80\x00\x00\x00"), filler(1500<<10, 24)), media.ErrUnsupported, "", 1},
		{"progressive AAC", "m4a", slices.Concat(ftyp(), box("moov", mvhd, hdlrTrak("soun")), box("mdat", filler(1500<<10, 25))), nil, "mp4", 2},
		{"an M4A with a picture track", "m4a", slices.Concat(ftyp(), box("moov", mvhd, hdlrTrak("soun"), hdlrTrak("vide")), box("mdat", filler(1500<<10, 26))), media.ErrUnsupported, "", 1},
		{"a picture track in a moov at the end", "m4a", slices.Concat(ftyp(), box("mdat", filler(2200<<10, 27)), box("moov", mvhd, hdlrTrak("vide"))), media.ErrUnsupported, "", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSiteServer(t)
			s.add("/a", tc.data)
			opts := rangedOptions(t)
			opts.OutPath = filepath.Join(filepath.Dir(opts.OutPath), "track."+tc.ext)
			sel := RangedSelection{Output: OutputCopy, Tracks: []media.RangedTrack{
				rangedTrack("mp3-128", media.KindAudio, tc.ext, s.url("/a"), int64(len(tc.data))),
			}}
			container, err := RunRanged(testContext(t), opts, sel, nil, nil, nil)
			if n := len(s.requests("")); n != tc.requests {
				t.Errorf("%d requests, want %d", n, tc.requests)
			}
			if tc.want != nil {
				if !errors.Is(err, tc.want) {
					t.Fatalf("RunRanged = %v, want %v", err, tc.want)
				}
				if _, err := os.Stat(opts.OutPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("an output was written")
				}
				return
			}
			if err != nil || container != tc.container {
				t.Fatalf("RunRanged = %q, %v; want %q", container, err, tc.container)
			}
			if !bytes.Equal(readFile(t, opts.OutPath), tc.data) {
				t.Fatal("the copied output differs from the served file")
			}
			// A second call finds the recorded output (a restart after the rename).
			if again, err := RunRanged(testContext(t), opts, sel, nil, nil, nil); err != nil || again != tc.container {
				t.Fatalf("RunRanged again = %q, %v", again, err)
			}
		})
	}
}

// A joined audio download refuses a fragmented MP4 whose 'moov' has a video track
// before the second chunk is requested.
func TestRunRangedAudioRefusesVideo(t *testing.T) {
	v, _ := fixturePair(t)
	s := newSiteServer(t)
	s.add("/v.mp4", v.data)
	opts := rangedOptions(t)
	sel := RangedSelection{Output: OutputRemux, Tracks: []media.RangedTrack{
		rangedTrack("a", media.KindAudio, "mp4", s.url("/v.mp4"), int64(len(v.data))),
	}}
	if _, err := RunRanged(testContext(t), opts, sel, nil, nil, nil); !errors.Is(err, media.ErrUnsupported) {
		t.Fatalf("RunRanged = %v, want a refusal", err)
	}
	if n := len(s.requests("")); n != 1 {
		t.Fatalf("%d requests, want the first chunk only", n)
	}
}

func TestCheckMP3Head(t *testing.T) {
	for _, tc := range []struct {
		head []byte
		ok   bool
	}{
		{[]byte("ID3\x03\x00\x00\x00\x00\x10\x7f"), true},
		{[]byte("ID3\x02\x00\x40\x00\x00\x00\x00"), true},
		{[]byte("ID3\x05\x00\x00\x00\x00\x00\x00"), false}, // no such version
		{[]byte("ID3\x04\xff\x00\x00\x00\x00\x00"), false}, // revision 0xff
		{[]byte("ID3\x04\x00\x00\x00\x80\x00\x00"), false}, // not syncsafe
		{[]byte{0xff, 0xfb, 0x90, 0x64}, true},             // MPEG-1 layer III, 128 kbit/s, 44.1 kHz
		{[]byte{0xff, 0xf3, 0x48, 0xc4}, true},             // MPEG-2 layer III
		{[]byte{0xff, 0xeb, 0x90, 0x64}, false},            // reserved version
		{[]byte{0xff, 0xf9, 0x90, 0x64}, false},            // reserved layer
		{[]byte{0xff, 0xfb, 0xf0, 0x64}, false},            // bit rate index 15
		{[]byte{0xff, 0xfb, 0x9c, 0x64}, false},            // sampling rate index 3
		{[]byte{0xff, 0xf1, 0x50, 0x80}, false},            // ADTS (AAC), not MP3
		{[]byte("\x00\x00\x00\x20ftypM4A "), false},
		{nil, false},
	} {
		if err := checkMP3Head(tc.head); (err == nil) != tc.ok {
			t.Errorf("checkMP3Head(% x) = %v, want ok %v", tc.head, err, tc.ok)
		}
	}
}

// Run with AudioOnly (an audio page's stream): the media playlist's one track (which has
// no kind) is joined as audio; an initialization segment with a video track, a part and
// a video-only selection are refused before any media is fetched.
func TestRunAudioOnly(t *testing.T) {
	s := newServer(t)
	init, segs := addFMP4Playlist(s, "/a/", 3, 7)
	asm := &fakeAssembler{}
	opts := testOptions(t, asm)
	opts.AudioOnly, opts.MP4Only = true, true
	container, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/a/index.m3u8"), VideoTrack: "v0"}, nil, nil, nil)
	if err != nil || container != "mp4" {
		t.Fatalf("Run = %q, %v", container, err)
	}
	if got := readFile(t, opts.OutPath); !bytes.Equal(got, slices.Concat(init, segs)) {
		t.Fatalf("output: %d bytes", len(got))
	}
	if in := asm.inputs; len(in) != 1 || in[0].Kind != "audio" || in[0].Container != "fmp4" || len(in[0].Fragments) != 3 {
		t.Fatalf("inputs = %+v", in)
	}

	// An initialization segment with a picture track.
	s.add("/v/init.mp4", append(ftyp(), box("moov", box("mvhd", make([]byte, 100)), hdlrTrak("vide"))...), "video/mp4")
	s.add("/v/s0.m4s", mediaSegment(9, 400), "video/iso.segment")
	s.add("/v/index.m3u8", []byte(mediaPlaylist("init.mp4", []string{"s0.m4s"})), hlsType)
	opts = testOptions(t, asm)
	opts.AudioOnly = true
	if _, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/v/index.m3u8")}, nil, nil, nil); !errors.Is(err, media.ErrUnsupported) {
		t.Fatalf("Run of a picture track = %v, want a refusal", err)
	}
	if n := s.hitCount("/v/s0.m4s"); n != 0 {
		t.Fatalf("%d media requests after the refused init", n)
	}

	// A part of an audio download.
	opts = testOptions(t, asm)
	opts.AudioOnly = true
	sel := Selection{ManifestURL: s.url("/a/index.m3u8"), Clip: media.Clip{Start: time.Second}}
	if _, err := Run(testContext(t), opts, sel, nil, nil, nil); !errors.Is(err, ErrInvalidSelection) {
		t.Fatalf("Run of a part = %v", err)
	}
}
