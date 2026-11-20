package played

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/aac"
	"github.com/Eyevinn/mp4ff/mp4"

	"downex/internal/media"
	"downex/internal/testmedia"
)

func encode(t testing.TB, boxes ...interface{ Encode(w io.Writer) error }) []byte {
	t.Helper()
	var buf bytes.Buffer
	for _, b := range boxes {
		if err := b.Encode(&buf); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}

// fragHead builds the head of a fragmented file: its init segment (with a 'mehd' of
// mehd ms when > 0) and, with sidx > 0, a top-level 'sidx' of that many ms in 2 references.
func fragHead(t testing.TB, video bool, mehd, sidx uint64) []byte {
	t.Helper()
	init := mp4.CreateEmptyInit()
	init.Moov.Mvhd.Timescale = 1000
	if video {
		sps := testmedia.H264SPS(0, 1280, 720)
		pps := testmedia.H264PPS(0, 0)
		if err := init.AddEmptyTrack(90000, "video", "und").SetAVCDescriptor("avc1", [][]byte{sps}, [][]byte{pps}, true); err != nil {
			t.Fatal(err)
		}
	} else if err := init.AddEmptyTrack(48000, "audio", "und").SetAACDescriptor(aac.AAClc, 48000); err != nil {
		t.Fatal(err)
	}
	if mehd > 0 {
		init.Moov.Mvex.AddChild(&mp4.MehdBox{FragmentDuration: int64(mehd)})
	}
	out := encode(t, init)
	if sidx > 0 {
		s := &mp4.SidxBox{Version: 0, ReferenceID: 1, Timescale: 1000}
		s.SidxRefs = []mp4.SidxRef{
			{ReferencedSize: 1000, SubSegmentDuration: uint32(sidx / 2), StartsWithSAP: 1, SAPType: 1},
			{ReferencedSize: 1000, SubSegmentDuration: uint32(sidx - sidx/2), StartsWithSAP: 1, SAPType: 1},
		}
		out = append(out, encode(t, s)...)
	}
	return out
}

func progressive(t testing.TB, frames int) []byte {
	t.Helper()
	v := testmedia.VideoConfig{Frames: frames}
	a := testmedia.AudioConfig{Frames: frames * 3 / 2}
	data, err := testmedia.ProgressiveMP4(v, testmedia.Video(v), a, testmedia.AAC(a))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDescribe(t *testing.T) {
	t.Run("progressive", func(t *testing.T) {
		d, err := Describe(progressive(t, 90))
		if err != nil {
			t.Fatal(err)
		}
		if !d.Video || !d.Audio || d.Fragmented || !d.Complete || d.Width != 640 || d.Height != 360 || d.VideoCodec != "avc1" || d.AudioCodec != "mp4a" {
			t.Fatalf("%+v", d)
		}
		if d.Duration < 2900*time.Millisecond || d.Duration > 3100*time.Millisecond {
			t.Fatalf("duration %v, want about 3 s", d.Duration)
		}
	})
	t.Run("progressive cut inside moov", func(t *testing.T) {
		full := progressive(t, 90)
		d, err := Describe(full[:200])
		if err != nil {
			t.Fatal(err)
		}
		if d.Complete || d.Duration < 2900*time.Millisecond {
			t.Fatalf("%+v", d)
		}
		if k := fileKind(Resource{Kind: "video"}, d); k != media.KindMuxed {
			t.Fatalf("kind %q", k)
		}
	})
	t.Run("fragmented with mehd", func(t *testing.T) {
		d, err := Describe(fragHead(t, true, 18994, 0))
		if err != nil {
			t.Fatal(err)
		}
		if !d.Video || d.Audio || !d.Fragmented || !d.Complete || d.Duration != 18994*time.Millisecond || d.Width != 1280 || d.Height != 720 {
			t.Fatalf("%+v", d)
		}
		if fileKind(Resource{}, d) != media.KindVideo {
			t.Fatal("not a video file")
		}
	})
	t.Run("fragmented with sidx only", func(t *testing.T) {
		d, err := Describe(fragHead(t, false, 0, 46320))
		if err != nil {
			t.Fatal(err)
		}
		if !d.Audio || d.Video || d.Duration != 46320*time.Millisecond || d.AudioCodec != "mp4a" {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("fragmented without duration", func(t *testing.T) {
		d, err := Describe(fragHead(t, true, 0, 0))
		if err != nil || d.Duration != 0 {
			t.Fatalf("%+v %v", d, err)
		}
	})
	t.Run("encrypted", func(t *testing.T) {
		head := fragHead(t, true, 5000, 0)
		head = bytes.Replace(head, []byte("avc1"), []byte("encv"), 1)
		d, err := Describe(head)
		if err != nil || !d.Encrypted {
			t.Fatalf("%+v %v", d, err)
		}
	})
	t.Run("moov after mdat", func(t *testing.T) {
		head := append([]byte{0, 0, 0, 16, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 0, 0}, 0, 0, 0x10, 0, 'm', 'd', 'a', 't')
		head = append(head, make([]byte, 64)...)
		d, err := Describe(head)
		if err != nil || d.Video || d.Duration != 0 {
			t.Fatalf("%+v %v", d, err)
		}
	})
	t.Run("not an MP4", func(t *testing.T) {
		for _, head := range [][]byte{nil, []byte("<!doctype html><html><body>"), {0x47, 0x40, 0, 0x10, 0, 0, 0, 0, 0}} {
			if _, err := Describe(head); !errors.Is(err, ErrNotMP4) {
				t.Fatalf("%q: %v", head, err)
			}
		}
	})
}

// res is a resource with the first 64 KiB of data as its head, as the extension reads it.
func res(id, url string, data []byte) Resource {
	head := data[:min(len(data), 64<<10)]
	return Resource{ID: id, URL: url, Head: head, Size: int64(len(data)) * 1000}
}

func TestMatch(t *testing.T) {
	// The clicked reel (19.0 s, its 720p and 480p video files and its sound), the next reel
	// prefetched (46.3 s), and an ad (6 s).
	src := Source{
		Element: Element{Duration: 18994 * time.Millisecond, Width: 720, Height: 1280, HasAudio: 1},
		Resources: []Resource{
			res("r1", "https://cdn.example/v/next-video.mp4", fragHead(t, true, 46320, 0)),
			res("r2", "https://cdn.example/v/next-audio.mp4", fragHead(t, false, 46320, 0)),
			res("r3", "https://cdn.example/v/this-720.mp4", fragHead(t, true, 19000, 0)),
			res("r4", "https://cdn.example/v/this-audio.mp4", fragHead(t, false, 0, 19020)),
			res("r5", "https://cdn.example/v/ad.mp4", progressive(t, 180)),
			res("r6", "https://cdn.example/v/page.html", []byte("<html></html>")),
		},
	}
	src.Resources[2].Size = 3_000_000
	o, err := Probe(src)
	if err != nil {
		t.Fatal(err)
	}
	if o.DefaultVideo != "r3" || o.DefaultAudio != "r4" || len(o.Tracks) != 2 || o.Silent {
		t.Fatalf("%+v", o)
	}
	if v := o.Files["r3"]; Output(v.Kind, v.Fragmented, o.DefaultAudio != "") != OutputRemux {
		t.Fatal("a video and its sound are joined")
	}
	if o.Tracks[0].Name == "" || o.Tracks[0].Bandwidth == 0 || o.Tracks[1].Ext != "m4a" {
		t.Fatalf("%+v", o.Tracks)
	}

	// The tolerance: 1.5 % (at least 0.3 s) for video.
	for _, c := range []struct {
		el, file time.Duration
		ok       bool
	}{
		{10 * time.Second, 10300 * time.Millisecond, true},
		{10 * time.Second, 10400 * time.Millisecond, false},
		{100 * time.Second, 101400 * time.Millisecond, true},
		{100 * time.Second, 101600 * time.Millisecond, false},
	} {
		s := Source{Element: Element{Duration: c.el}, Resources: []Resource{res("r1", "https://cdn.example/a.mp4", fragHead(t, true, uint64(c.file.Milliseconds()), 0))}}
		_, err := Match(s)
		if (err == nil) != c.ok {
			t.Fatalf("element %v file %v: %v", c.el, c.file, err)
		}
	}

	// No file says the element's length: the newest video of unknown length.
	s := Source{Element: Element{Duration: 30 * time.Second, HasAudio: 1}, Resources: []Resource{
		res("r1", "https://cdn.example/n.mp4", fragHead(t, true, 0, 0)),
		res("r2", "https://cdn.example/o.mp4", fragHead(t, true, 0, 0)),
		res("r3", "https://cdn.example/a.mp4", fragHead(t, false, 0, 0)),
	}}
	o, err = Probe(s)
	if err != nil || o.DefaultVideo != "r1" || o.DefaultAudio != "r3" {
		t.Fatalf("%+v %v", o, err)
	}

	// Nothing of this video yet: the panel keeps waiting.
	s = Source{Element: Element{Duration: 30 * time.Second}, Resources: []Resource{res("r1", "https://cdn.example/n.mp4", fragHead(t, true, 5000, 0))}}
	if _, err := Probe(s); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}

	// A muxed whole file is copied; the video alone of a silent reel is copied too.
	s = Source{Element: Element{Duration: 3 * time.Second}, Resources: []Resource{res("r1", "https://cdn.example/m.mp4", progressive(t, 90))}}
	o, err = Probe(s)
	if err != nil || o.DefaultVideo != "r1" || o.DefaultAudio != "" || o.Tracks[0].Kind != media.KindMuxed {
		t.Fatalf("%+v %v", o, err)
	}
	if f := o.Files["r1"]; Output(f.Kind, f.Fragmented, false) != OutputCopy {
		t.Fatal("a muxed file is copied")
	}
	s = Source{Element: Element{Duration: 5 * time.Second, HasAudio: -1}, Resources: []Resource{res("r1", "https://cdn.example/s.mp4", fragHead(t, true, 5000, 0))}}
	o, err = Probe(s)
	if err != nil || !o.Silent || o.DefaultAudio != "" {
		t.Fatalf("%+v %v", o, err)
	}

	// A protected file of this length refuses the source.
	enc := bytes.Replace(fragHead(t, true, 5000, 0), []byte("avc1"), []byte("encv"), 1)
	s = Source{Element: Element{Duration: 5 * time.Second}, Resources: []Resource{res("r1", "https://cdn.example/e.mp4", enc)}}
	if _, err := Probe(s); !errors.Is(err, media.ErrEncrypted) {
		t.Fatalf("got %v", err)
	}
}

func TestOfferOnePerHeight(t *testing.T) {
	mk := func(id string, h int, codec string, size int64, kind media.TrackKind) File {
		return File{Resource: Resource{ID: id, Size: size}, Desc: Desc{Video: true, Height: h, Width: h * 16 / 9, VideoCodec: codec, Duration: 10 * time.Second}, Kind: kind}
	}
	files := []File{
		mk("r1", 720, "hvc1", 900, media.KindVideo),
		mk("r2", 720, "avc1", 1000, media.KindVideo),
		mk("r3", 1080, "av01", 2000, media.KindVideo),
		mk("r4", 480, "avc1", 500, media.KindVideo),
		mk("r5", 720, "vp09", 800, media.KindVideo),
		{Resource: Resource{ID: "r6", Size: 100}, Desc: Desc{Audio: true, AudioCodec: "mp4a", Duration: 10 * time.Second}, Kind: media.KindAudio},
		{Resource: Resource{ID: "r7", Size: 200}, Desc: Desc{Audio: true, AudioCodec: "mp4a", Duration: 10 * time.Second}, Kind: media.KindAudio},
	}
	o, err := MakeOffer(Matched{Videos: files[:5], Audios: files[5:]}, Element{Duration: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, tr := range o.Tracks {
		ids = append(ids, tr.ID)
	}
	if got := strings.Join(ids, ","); got != "r3,r2,r4,r7,r6" || o.DefaultVideo != "r3" || o.DefaultAudio != "r7" {
		t.Fatalf("tracks %s, default %s+%s", got, o.DefaultVideo, o.DefaultAudio)
	}
}

func TestCheck(t *testing.T) {
	ok := Source{Element: Element{Duration: time.Second}, Resources: []Resource{res("r1", "https://cdn.example/a.mp4", []byte("x"))}}
	if err := Check(ok); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(s *Source){
		"no files":        func(s *Source) { s.Resources = nil },
		"bad id":          func(s *Source) { s.Resources[0].ID = "x1" },
		"youtube":         func(s *Source) { s.Resources[0].URL = "https://rr1---sn-abc.googlevideo.com/videoplayback" },
		"youtube page":    func(s *Source) { s.Resources[0].URL = "https://www.youtube.com/watch?v=x" },
		"loopback":        func(s *Source) { s.Resources[0].URL = "http://127.0.0.1:8080/a.mp4" },
		"private":         func(s *Source) { s.Resources[0].URL = "http://192.168.1.2/a.mp4" },
		"link-local v6":   func(s *Source) { s.Resources[0].URL = "http://[fe80::1]/a.mp4" },
		"localhost":       func(s *Source) { s.Resources[0].URL = "http://localhost/a.mp4" },
		"user info":       func(s *Source) { s.Resources[0].URL = "https://u:p@cdn.example/a.mp4" },
		"fragment":        func(s *Source) { s.Resources[0].URL = "https://cdn.example/a.mp4#t=1" },
		"ftp":             func(s *Source) { s.Resources[0].URL = "ftp://cdn.example/a.mp4" },
		"long":            func(s *Source) { s.Resources[0].URL = "https://cdn.example/" + strings.Repeat("a", MaxURLBytes) },
		"empty head":      func(s *Source) { s.Resources[0].Head = nil },
		"big head":        func(s *Source) { s.Resources[0].Head = make([]byte, MaxHeadBytes+1) },
		"negative size":   func(s *Source) { s.Resources[0].Size = -1 },
		"duration":        func(s *Source) { s.Element.Duration = 25 * time.Hour },
		"picture":         func(s *Source) { s.Element.Width = 20000 },
		"audio flag":      func(s *Source) { s.Element.HasAudio = 2 },
		"kind":            func(s *Source) { s.Resources[0].Kind = "muxed" },
		"duplicate id":    func(s *Source) { s.Resources = append(s.Resources, s.Resources[0]) },
		"too many heads":  func(s *Source) { s.Resources = manyHeads() },
		"too many files":  func(s *Source) { s.Resources = manyFiles(MaxResources + 1) },
		"control in addr": func(s *Source) { s.Resources[0].URL = "https://cdn.example/a\n.mp4" },
	}
	for name, edit := range bad {
		s := Source{Element: ok.Element, Resources: []Resource{ok.Resources[0]}}
		edit(&s)
		if err := Check(s); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func manyFiles(n int) []Resource {
	var out []Resource
	for i := range n {
		out = append(out, Resource{ID: "r" + itoa(i+1), URL: "https://cdn.example/a.mp4", Head: []byte("x")})
	}
	return out
}

func manyHeads() []Resource {
	out := manyFiles(5)
	for i := range out {
		out[i].Head = make([]byte, MaxHeadBytes)
	}
	return out
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

func TestSiteName(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.tiktok.com/foryou":             "TikTok",
		"https://m.facebook.com/reel/1":             "Facebook",
		"https://www.instagram.com/reels/abc/":      "Instagram",
		"https://video.example.org/watch":           "video.example.org",
		"https://www.example.com/":                  "example.com",
		"::":                                        "",
		"https://vkvideo.ru/video_ext.php?oid=1&id": "VK",
	} {
		if got := SiteName(in); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}

func FuzzDescribe(f *testing.F) {
	f.Add(progressive(f, 30))
	f.Add(fragHead(f, true, 1000, 2000))
	f.Add(fragHead(f, false, 0, 3000))
	f.Add([]byte("\x00\x00\x00\x08ftyp"))
	f.Fuzz(func(t *testing.T, head []byte) {
		d, err := Describe(head)
		if err == nil && (d.Duration < 0 || d.Duration > MaxDuration) {
			t.Fatalf("duration %v", d.Duration)
		}
	})
}

// A stream's initialization segment says the whole duration but is no video: never matched.
func TestMatchSkipsInitSegments(t *testing.T) {
	init := fragHead(t, true, 63533, 0)
	src := Source{Element: Element{Duration: 63533 * time.Millisecond}, Resources: []Resource{
		{ID: "r1", URL: "https://cdn.example/v/init.mp4", Head: init, Size: int64(len(init))},
		{ID: "r2", URL: "https://cdn.example/v/small.mp4", Head: init, Size: 2000},
	}}
	if _, err := Probe(src); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

// A progressive file whose head ends inside a 'moov' that lists its sound first is still the
// video when the server said so.
func TestCutMoovSoundFirst(t *testing.T) {
	d := Desc{Audio: true, AudioCodec: "mp4a", Duration: 487 * time.Second}
	if k := fileKind(Resource{Kind: "video"}, d); k != media.KindMuxed {
		t.Fatalf("kind %q", k)
	}
	if k := fileKind(Resource{Kind: "audio"}, d); k != media.KindAudio {
		t.Fatalf("kind %q", k)
	}
	if k := fileKind(Resource{Kind: "video"}, Desc{Audio: true, Complete: true}); k != media.KindAudio {
		t.Fatalf("a whole 'moov' with sound only is audio, got %q", k)
	}
}

// fragHeadWH is fragHead with a video picture of w x h and a 'mehd' of ms.
func fragHeadWH(t *testing.T, video bool, w, h int, ms uint64) []byte {
	t.Helper()
	init := mp4.CreateEmptyInit()
	init.Moov.Mvhd.Timescale = 1000
	if video {
		sps := testmedia.H264SPS(0, w, h)
		pps := testmedia.H264PPS(0, 0)
		if err := init.AddEmptyTrack(90000, "video", "und").SetAVCDescriptor("avc1", [][]byte{sps}, [][]byte{pps}, true); err != nil {
			t.Fatal(err)
		}
	} else if err := init.AddEmptyTrack(48000, "audio", "und").SetAACDescriptor(aac.AAClc, 48000); err != nil {
		t.Fatal(err)
	}
	init.Moov.Mvex.AddChild(&mp4.MehdBox{FragmentDuration: int64(ms)})
	return encode(t, init)
}

func sized(id, url string, head []byte, size int64) Resource {
	return Resource{ID: id, URL: url, Head: head, Size: size}
}

// A feed: the next item, preloaded after the clicked one (so newer), is within the length
// tolerance; the clicked item's own files are picked, never the neighbour's.
func TestMatchFeedNeighbour(t *testing.T) {
	t.Run("closest length", func(t *testing.T) {
		// Measured 2026-11-18 (For You): the clicked item 5.92 s, its neighbour 6.1 s and larger.
		next := res("r1", "https://cdn.example/v/next.mp4", progressive(t, 183))
		this := res("r2", "https://cdn.example/v/this.mp4", progressive(t, 178))
		next.Size, this.Size = 492_445, 400_000
		o, err := Probe(Source{Element: Element{Duration: 5920 * time.Millisecond, HasAudio: 1}, Resources: []Resource{next, this}})
		if err != nil {
			t.Fatal(err)
		}
		if o.DefaultVideo != "r2" || len(o.Tracks) != 1 {
			t.Fatalf("default %s, %d tracks: the neighbour is offered", o.DefaultVideo, len(o.Tracks))
		}
	})
	t.Run("picture and sound", func(t *testing.T) {
		src := Source{
			Element: Element{Duration: 20 * time.Second, Width: 720, Height: 1280, HasAudio: 1},
			Resources: []Resource{
				sized("r1", "https://cdn.example/b-video.mp4", fragHeadWH(t, true, 1080, 1920, 20250), 6_000_000),
				sized("r2", "https://cdn.example/b-audio.mp4", fragHeadWH(t, false, 0, 0, 20250), 400_000),
				sized("r3", "https://cdn.example/a-video.mp4", fragHeadWH(t, true, 720, 1280, 20000), 3_000_000),
				sized("r4", "https://cdn.example/a-audio.mp4", fragHeadWH(t, false, 0, 0, 20000), 300_000),
			},
		}
		o, err := Probe(src)
		if err != nil {
			t.Fatal(err)
		}
		if o.DefaultVideo != "r3" || o.DefaultAudio != "r4" || len(o.Tracks) != 2 {
			t.Fatalf("default %s+%s, %d tracks", o.DefaultVideo, o.DefaultAudio, len(o.Tracks))
		}
	})
	t.Run("foreign sound", func(t *testing.T) {
		// The other item's sound is within the audio tolerance of the element and larger.
		src := Source{
			Element: Element{Duration: 30 * time.Second, Width: 720, Height: 1280, HasAudio: 1},
			Resources: []Resource{
				sized("r1", "https://cdn.example/b-video.mp4", fragHeadWH(t, true, 720, 1280, 30500), 5_000_000),
				sized("r2", "https://cdn.example/b-audio.mp4", fragHeadWH(t, false, 0, 0, 30500), 500_000),
				sized("r3", "https://cdn.example/a-video.mp4", fragHeadWH(t, true, 720, 1280, 30000), 4_000_000),
				sized("r4", "https://cdn.example/a-audio.mp4", fragHeadWH(t, false, 0, 0, 30000), 480_000),
			},
		}
		o, err := Probe(src)
		if err != nil {
			t.Fatal(err)
		}
		if o.DefaultVideo != "r3" || o.DefaultAudio != "r4" {
			t.Fatalf("default %s+%s", o.DefaultVideo, o.DefaultAudio)
		}
		if _, ok := o.Files["r2"]; ok {
			t.Fatal("the other item's sound is listed")
		}
	})
	t.Run("capped length", func(t *testing.T) {
		// Two items of exactly 15 s: the element's own picture decides, its sound is the one
		// fetched in its turn, and the other item is never listed with it.
		src := Source{
			Element: Element{Duration: 15 * time.Second, Width: 576, Height: 1024, HasAudio: 1},
			Resources: []Resource{
				sized("r1", "https://cdn.example/other-video.mp4", fragHeadWH(t, true, 720, 1280, 15000), 3_000_000),
				sized("r2", "https://cdn.example/this-video.mp4", fragHeadWH(t, true, 576, 1024, 15000), 2_000_000),
				sized("r3", "https://cdn.example/other-audio.mp4", fragHeadWH(t, false, 0, 0, 15000), 250_000),
				sized("r4", "https://cdn.example/this-audio.mp4", fragHeadWH(t, false, 0, 0, 15000), 240_000),
			},
		}
		o, err := Probe(src)
		if err != nil {
			t.Fatal(err)
		}
		if o.DefaultVideo != "r2" || o.DefaultAudio != "r4" || len(o.Tracks) != 2 {
			t.Fatalf("default %s+%s, %d tracks", o.DefaultVideo, o.DefaultAudio, len(o.Tracks))
		}
	})
	t.Run("qualities of one video", func(t *testing.T) {
		// Its 1080p and 720p files (the element shows 720p) and one sound: both listed, the
		// element's own picture is the default.
		src := Source{
			Element: Element{Duration: 20 * time.Second, Width: 720, Height: 1280, HasAudio: 1},
			Resources: []Resource{
				sized("r1", "https://cdn.example/a-1080.mp4", fragHeadWH(t, true, 1080, 1920, 20030), 6_000_000),
				sized("r2", "https://cdn.example/a-720.mp4", fragHeadWH(t, true, 720, 1280, 20000), 3_000_000),
				sized("r3", "https://cdn.example/a-audio.mp4", fragHeadWH(t, false, 0, 0, 20010), 300_000),
			},
		}
		o, err := Probe(src)
		if err != nil {
			t.Fatal(err)
		}
		if o.DefaultVideo != "r2" || o.DefaultAudio != "r3" || len(o.Tracks) != 3 || o.Tracks[0].ID != "r1" {
			t.Fatalf("default %s+%s, tracks %+v", o.DefaultVideo, o.DefaultAudio, o.Tracks)
		}
	})
}

// A <video> that has not loaded (or is live) has no duration: nothing is "this video" yet.
func TestMatchUnloadedElement(t *testing.T) {
	src := Source{Resources: []Resource{
		sized("r1", "https://cdn.example/previous-video.mp4", fragHeadWH(t, true, 720, 1280, 12000), 2_000_000),
		sized("r2", "https://cdn.example/previous-audio.mp4", fragHeadWH(t, false, 0, 0, 12000), 200_000),
	}}
	if _, err := Probe(src); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

// A stream's media segment ('moof' or 'styp' first, no 'moov') is never a whole video
// (measured 2026-11-18: a Bluesky HLS segment of 254 KB).
func TestMatchSkipsMediaSegments(t *testing.T) {
	seg := make([]byte, 64<<10)
	binary.BigEndian.PutUint32(seg[0:], 24)
	copy(seg[4:], "moof")
	binary.BigEndian.PutUint32(seg[8:], 16)
	copy(seg[12:], "mfhd")
	binary.BigEndian.PutUint32(seg[20:], 2)
	binary.BigEndian.PutUint32(seg[24:], 254066-24)
	copy(seg[28:], "mdat")
	for _, lead := range []string{"moof", "styp"} {
		head := bytes.Clone(seg)
		copy(head[4:], lead)
		src := Source{Element: Element{Duration: 42500 * time.Millisecond, HasAudio: 1},
			Resources: []Resource{{ID: "r1", URL: "https://cdn.example/seg2.m4s", Size: 254066, Kind: "video", Head: head}}}
		if o, err := Probe(src); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s: got %v %+v", lead, err, o)
		}
	}
}

// A file on a refused host (an embed's YouTube file in the same tab) is left out; the
// clicked video's own files are still offered.
func TestMatchLeavesRefusedHostsOut(t *testing.T) {
	src := Source{
		Element: Element{Duration: 18994 * time.Millisecond, Width: 720, Height: 1280, HasAudio: 1},
		Resources: []Resource{
			res("r1", "https://cdn.example/v/this-720.mp4", fragHead(t, true, 19000, 0)),
			res("r2", "https://cdn.example/v/this-audio.mp4", fragHead(t, false, 0, 19020)),
			res("r3", "https://rr1---sn-abc.googlevideo.com/videoplayback?itag=137", fragHead(t, true, 19000, 0)),
			res("r4", "http://192.168.1.10/video.mp4", fragHead(t, true, 19000, 0)),
		},
	}
	o, err := Probe(src)
	if err != nil {
		t.Fatal(err)
	}
	if o.DefaultVideo != "r1" || o.DefaultAudio != "r2" || len(o.Files) != 2 {
		t.Fatalf("%+v", o.Files)
	}
}

// The frame's other elements claim their own files (measured 2026-11-18, For You: the
// clicked element said 5.92 s and its file 5.77 s; the next element 6.24 s and its file,
// preloaded after it, 6.10 s: both files are within the tolerance and about as near).
func TestMatchOtherElementsClaimTheirFiles(t *testing.T) {
	files := []Resource{
		sized("r1", "https://cdn.example/v/next.mp4", fragHeadWH(t, true, 720, 1280, 6100), 492_445),
		sized("r2", "https://cdn.example/v/this.mp4", fragHeadWH(t, true, 720, 1280, 5767), 393_551),
	}
	el := Element{Duration: 5920 * time.Millisecond, Width: 720, Height: 1280, HasAudio: 1}
	el.Others = []time.Duration{6243 * time.Millisecond}
	o, err := Probe(Source{Element: el, Resources: files})
	if err != nil {
		t.Fatal(err)
	}
	if o.DefaultVideo != "r2" || len(o.Tracks) != 1 {
		t.Fatalf("default %s, %d tracks", o.DefaultVideo, len(o.Tracks))
	}
	// A copy of the clicked element (a blurred backdrop of the same video) claims nothing.
	el.Others = []time.Duration{5920 * time.Millisecond}
	if _, err := Probe(Source{Element: el, Resources: files[1:]}); err != nil {
		t.Fatal(err)
	}
	// Limits.
	el.Others = make([]time.Duration, MaxOthers+1)
	for i := range el.Others {
		el.Others[i] = time.Second
	}
	if err := Check(Source{Element: el, Resources: files}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v", err)
	}
	el.Others = []time.Duration{0}
	if err := Check(Source{Element: el, Resources: files}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v", err)
	}
}

// Review 2026-11-18: sounds of one length are paired in fetch order, also when they differ by
// a frame or so (two items' sounds of 15.03 s and 15 s).
func TestMatchSoundTieInFetchOrder(t *testing.T) {
	src := Source{
		Element: Element{Duration: 15 * time.Second, Width: 576, Height: 1024, HasAudio: 1},
		Resources: []Resource{
			sized("r1", "https://cdn.example/other-video.mp4", fragHeadWH(t, true, 720, 1280, 15000), 3_000_000),
			sized("r2", "https://cdn.example/this-video.mp4", fragHeadWH(t, true, 576, 1024, 15000), 2_000_000),
			sized("r3", "https://cdn.example/other-audio.mp4", fragHeadWH(t, false, 0, 0, 15030), 250_000),
			sized("r4", "https://cdn.example/this-audio.mp4", fragHeadWH(t, false, 0, 0, 15000), 240_000),
		},
	}
	o, err := Probe(src)
	if err != nil {
		t.Fatal(err)
	}
	if o.DefaultVideo != "r2" || o.DefaultAudio != "r4" || len(o.Tracks) != 2 {
		t.Fatalf("default %s+%s, %d tracks: the other item's sound is joined", o.DefaultVideo, o.DefaultAudio, len(o.Tracks))
	}
}

// Review 2026-11-18: one item whose adaptive player fetched two qualities of its picture and
// two of its sound keeps every quality, with the better sound as the default.
func TestMatchSoundQualitiesOfOneItem(t *testing.T) {
	src := Source{
		Element: Element{Duration: 20 * time.Second, Width: 720, Height: 1280, HasAudio: 1},
		Resources: []Resource{
			sized("r1", "https://cdn.example/a-1080.mp4", fragHeadWH(t, true, 1080, 1920, 20030), 6_000_000),
			sized("r2", "https://cdn.example/a-720.mp4", fragHeadWH(t, true, 720, 1280, 20000), 3_000_000),
			sized("r3", "https://cdn.example/a-audio-128k.mp4", fragHeadWH(t, false, 0, 0, 20010), 320_000),
			sized("r4", "https://cdn.example/a-audio-64k.mp4", fragHeadWH(t, false, 0, 0, 20010), 160_000),
		},
	}
	o, err := Probe(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := o.Files["r1"]; !ok || o.DefaultVideo != "r2" || o.DefaultAudio != "r3" || len(o.Tracks) != 4 {
		t.Fatalf("tracks %d, default %s+%s", len(o.Tracks), o.DefaultVideo, o.DefaultAudio)
	}
	// Another element of that length on the page: two items of one length after all.
	src.Element.Others = []time.Duration{20 * time.Second}
	if o, err = Probe(src); err != nil {
		t.Fatal(err)
	}
	if len(o.Tracks) != 2 {
		t.Fatalf("with a second element of that length: %d tracks", len(o.Tracks))
	}
}

// Review 2026-11-18: players' elements say a length about 0.15 s longer than their files',
// so a neighbour's element can lie between the clicked file and the clicked element. Files of
// one picture are paired with the players in order of length.
func TestMatchNeighboursPairedInOrder(t *testing.T) {
	for _, tc := range []struct {
		name  string
		other time.Duration
		files []Resource
	}{
		// The neighbour's element is 80 ms longer than the clicked one.
		{"longer neighbour", 6000 * time.Millisecond, []Resource{
			sized("r1", "https://cdn.example/v/next.mp4", fragHeadWH(t, true, 720, 1280, 5850), 492_445),
			sized("r2", "https://cdn.example/v/this.mp4", fragHeadWH(t, true, 720, 1280, 5770), 393_551),
		}},
		// The neighbour's element is 120 ms shorter: its file is the shorter one.
		{"shorter neighbour", 5800 * time.Millisecond, []Resource{
			sized("r1", "https://cdn.example/v/next.mp4", fragHeadWH(t, true, 720, 1280, 5650), 492_445),
			sized("r2", "https://cdn.example/v/this.mp4", fragHeadWH(t, true, 720, 1280, 5770), 393_551),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			el := Element{Duration: 5920 * time.Millisecond, Width: 720, Height: 1280, HasAudio: 1, Others: []time.Duration{tc.other}}
			o, err := Probe(Source{Element: el, Resources: tc.files})
			if err != nil {
				t.Fatal(err)
			}
			if o.DefaultVideo != "r2" || len(o.Tracks) != 1 {
				t.Fatalf("default %s, %d tracks", o.DefaultVideo, len(o.Tracks))
			}
		})
	}
}

// Review 2026-11-18: a file another player claims is never listed as a quality of the
// clicked video.
func TestMatchClaimedFileNotAQuality(t *testing.T) {
	el := Element{Duration: 6240 * time.Millisecond, Width: 720, Height: 1280, HasAudio: 1,
		Others: []time.Duration{6000 * time.Millisecond}}
	files := []Resource{
		sized("r1", "https://cdn.example/v/next-576.mp4", fragHeadWH(t, true, 576, 1024, 6110), 300_000),
		sized("r2", "https://cdn.example/v/this-720.mp4", fragHeadWH(t, true, 720, 1280, 6200), 493_000),
	}
	o, err := Probe(Source{Element: el, Resources: files})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := o.Files["r1"]; ok || o.DefaultVideo != "r2" {
		t.Fatalf("the neighbour's file is offered: default %s, %+v", o.DefaultVideo, o.Tracks)
	}
}
