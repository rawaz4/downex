package media

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

const hlsBase = "https://media.example.com/show/ep1/index.m3u8"

func TestParseHLSMaster(t *testing.T) {
	p, err := ParseHLS(mustURL(t, "https://media.example.com/show/master.m3u8"), fixture(t, "hls/master.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Master || p.Version != 6 || !p.IndependentSegments {
		t.Fatalf("master=%v version=%d independent=%v", p.Master, p.Version, p.IndependentSegments)
	}
	wantVariants := []Variant{
		{URI: "https://media.example.com/show/video/360p.m3u8", Bandwidth: 1280000, AverageBandwidth: 1000000,
			Codecs: "avc1.4d401f,mp4a.40.2", Width: 640, Height: 360, FrameRate: 29.97,
			Audio: "aud-lo", Subtitles: "subs", ClosedCaptions: "cc"},
		{URI: "https://media.example.com/show/video/1080p.m3u8", Bandwidth: 5000000, Codecs: "avc1.640028,ec-3",
			Width: 1920, Height: 1080, FrameRate: 60, Audio: "aud-hi", Subtitles: "subs", ClosedCaptions: "NONE"},
		{URI: "https://cdn.example.net/video/720p.m3u8", Bandwidth: 2560000, Codecs: "avc1.4d401f,mp4a.40.2",
			Width: 1280, Height: 720, Audio: "aud-lo"},
	}
	if len(p.Variants) != len(wantVariants) {
		t.Fatalf("%d variants, want %d (I-frame variants are not offered)", len(p.Variants), len(wantVariants))
	}
	for i, w := range wantVariants {
		if p.Variants[i] != w {
			t.Errorf("variant %d = %+v\nwant %+v", i, p.Variants[i], w)
		}
	}
	if len(p.Renditions) != 5 {
		t.Fatalf("%d renditions, want 5", len(p.Renditions))
	}
	en := p.Renditions[2]
	if en.Type != "AUDIO" || en.GroupID != "aud-hi" || en.Name != "English, 5.1" || en.Language != "en" ||
		!en.Default || !en.Autoselect || en.Channels != "6" || en.URI != "https://media.example.com/show/audio/en/hi.m3u8" {
		t.Errorf("rendition 2 = %+v", en)
	}
	if cc := p.Renditions[4]; cc.Type != "CLOSED-CAPTIONS" || cc.URI != "" || cc.InstreamID != "CC1" {
		t.Errorf("closed captions = %+v", cc)
	}
	if _, err := PlanHLS(p); !errors.Is(err, ErrNotFound) {
		t.Errorf("PlanHLS(master) error = %v, want ErrNotFound", err)
	}
}

func TestParseHLSMediaVOD(t *testing.T) {
	data := fixture(t, "hls/media_vod.m3u8")
	for _, crlf := range []bool{false, true} {
		in := data
		if crlf {
			in = []byte(strings.ReplaceAll(string(data), "\n", "\r\n"))
		}
		p, err := ParseHLS(mustURL(t, hlsBase), in)
		if err != nil {
			t.Fatal(err)
		}
		if p.Master || p.Live() || p.TargetDuration != 10*time.Second || p.MediaSequence != 7 || p.PlaylistType != "VOD" || !p.EndList {
			t.Fatalf("playlist header = %+v", p)
		}
		if len(p.Segments) != 3 || p.Segments[1].Title != "second segment" || p.Segments[2].Sequence != 9 {
			t.Fatalf("segments = %+v", p.Segments)
		}
		plan, err := PlanHLS(p)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Container != ContainerTS || plan.Duration != 21021*time.Millisecond {
			t.Errorf("container %q duration %v", plan.Container, plan.Duration)
		}
		comparePlan(t, plan, []wantFragment{
			{url: "https://media.example.com/show/ep1/seg-7.ts", durMS: 9009},
			{url: "https://media.example.com/show/ep1/seg-8.ts", durMS: 9009},
			{url: "https://media.example.com/abs/seg-9.ts", durMS: 3003},
		})
	}
}

func TestHLSImplicitByteRanges(t *testing.T) {
	p, err := ParseHLS(mustURL(t, hlsBase), fixture(t, "hls/byterange.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanHLS(p)
	if err != nil {
		t.Fatal(err)
	}
	u := "https://media.example.com/show/ep1/all.ts"
	comparePlan(t, plan, []wantFragment{
		{url: u, rng: rng(0, 999), durMS: 6000},
		{url: u, rng: rng(1000, 2999), durMS: 6000},
		{url: u, rng: rng(3000, 4499), durMS: 6000},
		{url: u, rng: rng(10000, 10499), durMS: 4500},
		{url: u, rng: rng(10500, 10749), durMS: 4500},
	})
}

func TestHLSByteRangeErrors(t *testing.T) {
	for name, body := range map[string]string{
		"implicit first":         "#EXTINF:1,\n#EXT-X-BYTERANGE:100\na.ts\n",
		"implicit other URI":     "#EXTINF:1,\n#EXT-X-BYTERANGE:100@0\na.ts\n#EXTINF:1,\n#EXT-X-BYTERANGE:100\nb.ts\n",
		"implicit after whole":   "#EXTINF:1,\n#EXT-X-BYTERANGE:100@0\na.ts\n#EXTINF:1,\na.ts\n#EXTINF:1,\n#EXT-X-BYTERANGE:100\na.ts\n",
		"zero length":            "#EXTINF:1,\n#EXT-X-BYTERANGE:0@0\na.ts\n",
		"offset overflow":        "#EXTINF:1,\n#EXT-X-BYTERANGE:2@9223372036854775807\na.ts\n",
		"not a number":           "#EXTINF:1,\n#EXT-X-BYTERANGE:12a@0\na.ts\n",
		"map range not a number": "#EXT-X-MAP:URI=\"i.mp4\",BYTERANGE=\"x@0\"\n#EXTINF:1,\na.m4s\n",
	} {
		_, err := ParseHLS(mustURL(t, hlsBase), []byte("#EXTM3U\n"+body+"#EXT-X-ENDLIST\n"))
		if !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: error = %v, want ErrMalformed", name, err)
		}
	}
	// The implicit offset continues the same resource even across a map change.
	p, err := ParseHLS(nil, []byte("#EXTM3U\n#EXTINF:1,\n#EXT-X-BYTERANGE:10@5\na.ts\n#EXT-X-DISCONTINUITY\n#EXTINF:1,\n#EXT-X-BYTERANGE:10\na.ts\n#EXT-X-ENDLIST\n"))
	if err != nil {
		t.Fatal(err)
	}
	if r := p.Segments[1].ByteRange; r == nil || *r != [2]int64{15, 24} {
		t.Fatalf("second range = %v", r)
	}
}

func TestHLSMapsDiscontinuitiesAndGaps(t *testing.T) {
	p, err := ParseHLS(mustURL(t, "https://h.example/v/x.m3u8"), fixture(t, "hls/fmp4.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Segments[0].Map != p.Segments[1].Map {
		t.Error("segments under one EXT-X-MAP do not share it")
	}
	plan, err := PlanHLS(p)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Container != ContainerFMP4 {
		t.Errorf("container = %q", plan.Container)
	}
	b := "https://h.example/v/"
	comparePlan(t, plan, []wantFragment{
		{url: b + "init-a.mp4", init: true},
		{url: b + "a1.m4s", durMS: 4000},
		{url: b + "a2.m4s", durMS: 4000},
		{url: b + "main.mp4", rng: rng(0, 719), init: true, disc: true},
		{url: b + "main.mp4", rng: rng(720, 50719), durMS: 4000},
		{url: b + "main.mp4", rng: rng(50720, 90719), disc: true, durMS: 2500},
	})
}

func TestHLSRelativeURIResolution(t *testing.T) {
	cases := []struct{ base, ref, want string }{
		{"https://a.example/x/y/index.m3u8", "seg.ts", "https://a.example/x/y/seg.ts"},
		{"https://a.example/x/y/index.m3u8?tok=1", "seg.ts", "https://a.example/x/y/seg.ts"},
		{"https://a.example/x/y/index.m3u8", "../z/seg.ts", "https://a.example/x/z/seg.ts"},
		{"https://a.example/x/y/index.m3u8", "/root.ts", "https://a.example/root.ts"},
		{"https://a.example/x/y/index.m3u8", "//cdn.example/s.ts", "https://cdn.example/s.ts"},
		{"https://a.example/x/y/index.m3u8", "http://plain.example/s.ts?q=a%20b", "http://plain.example/s.ts?q=a%20b"},
		{"https://a.example/x/y/", "seg.ts?x=1#frag", "https://a.example/x/y/seg.ts?x=1#frag"},
	}
	for _, c := range cases {
		body := "#EXTM3U\n#EXTINF:1,\n" + c.ref + "\n#EXT-X-ENDLIST\n"
		p, err := ParseHLS(mustURL(t, c.base), []byte(body))
		if err != nil {
			t.Fatalf("%s + %s: %v", c.base, c.ref, err)
		}
		if got := p.Segments[0].URI; got != c.want {
			t.Errorf("%s + %s = %s, want %s", c.base, c.ref, got, c.want)
		}
	}
	// Without a base, references stay relative.
	p, err := ParseHLS(nil, []byte("#EXTM3U\n#EXTINF:1,\nsub/seg.ts\n#EXT-X-ENDLIST\n"))
	if err != nil || p.Segments[0].URI != "sub/seg.ts" {
		t.Fatalf("no base: %v %+v", err, p)
	}
	// Other schemes are refused, except the base's own.
	for _, ref := range []string{"file:///etc/passwd", "data:video/mp2t;base64,AAAA", "javascript:alert(1)", "ftp://h/x.ts"} {
		_, err := ParseHLS(mustURL(t, "https://a.example/i.m3u8"), []byte("#EXTM3U\n#EXTINF:1,\n"+ref+"\n#EXT-X-ENDLIST\n"))
		if !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: error = %v, want ErrUnsupported", ref, err)
		}
	}
	if _, err := ParseHLS(mustURL(t, "file:///media/i.m3u8"), []byte("#EXTM3U\n#EXTINF:1,\nseg.ts\n#EXT-X-ENDLIST\n")); err != nil {
		t.Errorf("file base with relative segment: %v", err)
	}
}

func TestHLSEncryptionRefused(t *testing.T) {
	for _, name := range []string{"hls/aes128.m3u8", "hls/sample_aes.m3u8", "hls/session_key.m3u8"} {
		_, err := ParseHLS(mustURL(t, hlsBase), fixture(t, name))
		if !errors.Is(err, ErrEncrypted) {
			t.Errorf("%s: error = %v, want ErrEncrypted", name, err)
		}
	}
	for _, method := range []string{"AES-128", "SAMPLE-AES", "SAMPLE-AES-CTR", "SAMPLE-AES-CENC", `"AES-128"`, "none", "ISO-23001-7"} {
		for _, tag := range []string{"#EXT-X-KEY", "#EXT-X-SESSION-KEY"} {
			body := fmt.Sprintf("#EXTM3U\n%s:METHOD=%s,URI=\"k\"\n#EXTINF:1,\na.ts\n#EXT-X-ENDLIST\n", tag, method)
			if _, err := ParseHLS(nil, []byte(body)); !errors.Is(err, ErrEncrypted) {
				t.Errorf("%s METHOD=%s: error = %v, want ErrEncrypted", tag, method, err)
			}
		}
	}
	// A key tag without METHOD cannot be shown to be clear.
	if _, err := ParseHLS(nil, []byte("#EXTM3U\n#EXT-X-KEY:URI=\"k\"\n#EXTINF:1,\na.ts\n")); !errors.Is(err, ErrEncrypted) {
		t.Errorf("KEY without METHOD: %v", err)
	}
	// Encryption is refused even in a playlist that is otherwise malformed later.
	if _, err := ParseHLS(nil, []byte("#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"k\"\nnot-a-segment\n")); !errors.Is(err, ErrEncrypted) {
		t.Errorf("KEY before malformed line: %v", err)
	}
	// METHOD=NONE is clear.
	p, err := ParseHLS(nil, []byte("#EXTM3U\n#EXT-X-KEY:METHOD=NONE\n#EXTINF:1,\na.ts\n#EXT-X-ENDLIST\n"))
	if err != nil || len(p.Segments) != 1 {
		t.Fatalf("METHOD=NONE: %v", err)
	}
}

func TestHLSLiveAndUnsupported(t *testing.T) {
	live := []byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:100\n#EXTINF:2,\na.ts\n#EXTINF:2,\nb.ts\n")
	p, err := ParseHLS(nil, live)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Live() {
		t.Fatal("playlist without EXT-X-ENDLIST is not live")
	}
	if _, err := PlanHLS(p); !errors.Is(err, ErrLive) {
		t.Errorf("PlanHLS(live) = %v, want ErrLive", err)
	}
	event := []byte("#EXTM3U\n#EXT-X-PLAYLIST-TYPE:EVENT\n#EXTINF:2,\na.ts\n")
	if p, err := ParseHLS(nil, event); err != nil || !p.Live() {
		t.Errorf("open EVENT playlist: live=%v err=%v", p != nil && p.Live(), err)
	}
	vod := []byte("#EXTM3U\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXTINF:2,\na.ts\n")
	if p, err := ParseHLS(nil, vod); err != nil || p.Live() {
		t.Errorf("VOD playlist without ENDLIST: err=%v", err)
	}
	iframes := []byte("#EXTM3U\n#EXT-X-I-FRAMES-ONLY\n#EXTINF:2,\n#EXT-X-BYTERANGE:100@0\na.ts\n#EXT-X-ENDLIST\n")
	if p, err := ParseHLS(nil, iframes); err != nil {
		t.Error(err)
	} else if _, err := PlanHLS(p); !errors.Is(err, ErrUnsupported) {
		t.Errorf("PlanHLS(I-frames) = %v, want ErrUnsupported", err)
	}
	skip := []byte("#EXTM3U\n#EXT-X-SKIP:SKIPPED-SEGMENTS=10\n#EXTINF:2,\na.ts\n")
	if _, err := ParseHLS(nil, skip); !errors.Is(err, ErrUnsupported) {
		t.Errorf("EXT-X-SKIP: %v, want ErrUnsupported", err)
	}
	if _, err := ParseHLS(nil, []byte("#EXTM3U\n#EXT-X-DEFINE:IMPORT=\"x\"\n")); !errors.Is(err, ErrUnsupported) {
		t.Errorf("EXT-X-DEFINE IMPORT: %v, want ErrUnsupported", err)
	}
	gapsOnly := []byte("#EXTM3U\n#EXT-X-GAP\n#EXTINF:2,\na.ts\n#EXT-X-ENDLIST\n")
	if p, err := ParseHLS(nil, gapsOnly); err != nil {
		t.Error(err)
	} else if _, err := PlanHLS(p); !errors.Is(err, ErrNotFound) {
		t.Errorf("PlanHLS(only gaps) = %v, want ErrNotFound", err)
	}
	if _, err := PlanHLS(nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("PlanHLS(nil) = %v", err)
	}
}

func TestHLSVariables(t *testing.T) {
	p, err := ParseHLS(mustURL(t, "https://a.example/p/index.m3u8?token=s3cr%2Ft&x=1"), fixture(t, "hls/variables.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := p.Segments[0].URI, "https://edge.example.com/s1.m4s?t=s3cr/t"; got != want {
		t.Errorf("segment URI = %s, want %s", got, want)
	}
	if got, want := p.Segments[0].Map.URI, "https://edge.example.com/init.mp4?t=s3cr/t"; got != want {
		t.Errorf("map URI = %s, want %s", got, want)
	}
	for name, body := range map[string]string{
		"undefined":       "#EXT-X-DEFINE:NAME=\"a\",VALUE=\"1\"\n#EXTINF:1,\n{$b}.ts\n",
		"duplicate":       "#EXT-X-DEFINE:NAME=\"a\",VALUE=\"1\"\n#EXT-X-DEFINE:NAME=\"a\",VALUE=\"2\"\n",
		"bad name":        "#EXT-X-DEFINE:NAME=\"a b\",VALUE=\"1\"\n",
		"no value":        "#EXT-X-DEFINE:NAME=\"a\"\n",
		"missing query":   "#EXT-X-DEFINE:QUERYPARAM=\"nope\"\n",
		"nothing defined": "#EXT-X-DEFINE:VALUE=\"1\"\n",
	} {
		if _, err := ParseHLS(mustURL(t, "https://a.example/i.m3u8?token=1"), []byte("#EXTM3U\n"+body)); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: %v, want ErrMalformed", name, err)
		}
	}
	// Expansion is bounded before the result is built.
	long := strings.Repeat("x", MaxURIBytes/2)
	body := "#EXTM3U\n#EXT-X-DEFINE:NAME=\"v\",VALUE=\"" + long + "\"\n#EXTINF:1,\n{$v}{$v}{$v}\n"
	if _, err := ParseHLS(nil, []byte(body)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversized substitution: %v, want ErrTooLarge", err)
	}
	// Without EXT-X-DEFINE, "{$" is ordinary text.
	if p, err := ParseHLS(nil, []byte("#EXTM3U\n#EXTINF:1,\na{$b}.ts\n#EXT-X-ENDLIST\n")); err != nil || p.Segments[0].URI != "a%7B$b%7D.ts" {
		t.Errorf("literal braces: %v %q", err, p.Segments[0].URI)
	}
}

func TestHLSMalformed(t *testing.T) {
	for name, body := range map[string]string{
		"empty":                "",
		"no header":            "#EXTINF:1,\na.ts\n",
		"header not first":     "\n#EXTM3U\n",
		"uri without extinf":   "#EXTM3U\na.ts\n",
		"dangling extinf":      "#EXTM3U\n#EXTINF:1,\n",
		"dangling stream-inf":  "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\n",
		"double stream-inf":    "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\n#EXT-X-STREAM-INF:BANDWIDTH=2\nv.m3u8\n",
		"negative extinf":      "#EXTM3U\n#EXTINF:-1,\na.ts\n",
		"exponent extinf":      "#EXTM3U\n#EXTINF:1e3,\na.ts\n",
		"huge extinf":          "#EXTM3U\n#EXTINF:99999999999,\na.ts\n",
		"mixed":                "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nv.m3u8\n#EXTINF:1,\na.ts\n",
		"master uri alone":     "#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"a\",NAME=\"x\"\nv.m3u8\n",
		"no variants":          "#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"a\",NAME=\"x\"\n",
		"bad bandwidth":        "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=-5\nv.m3u8\n",
		"bad resolution":       "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1,RESOLUTION=12x\nv.m3u8\n",
		"duplicate attribute":  "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1,BANDWIDTH=2\nv.m3u8\n",
		"unterminated quote":   "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1,CODECS=\"avc1\nv.m3u8\n",
		"media without group":  "#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,NAME=\"x\"\n#EXT-X-STREAM-INF:BANDWIDTH=1\nv.m3u8\n",
		"map without uri":      "#EXTM3U\n#EXT-X-MAP:BYTERANGE=\"1@0\"\n",
		"two versions":         "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-VERSION:4\n",
		"bad playlist type":    "#EXTM3U\n#EXT-X-PLAYLIST-TYPE:LIVE\n",
		"bad target duration":  "#EXTM3U\n#EXT-X-TARGETDURATION:4.5\n",
		"bad media sequence":   "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:x\n",
		"sequence overflow":    "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:18446744073709551615\n#EXTINF:1,\na.ts\n#EXTINF:1,\nb.ts\n",
		"invalid uri":          "#EXTM3U\n#EXTINF:1,\nhttp://[::1\n",
		"lowercase attribute":  "#EXTM3U\n#EXT-X-STREAM-INF:bandwidth=1\nv.m3u8\n",
		"attribute no equals":  "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH\nv.m3u8\n",
		"attribute no comma":   "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1 CODECS=\"x\"\nv.m3u8\n",
		"key attribute broken": "#EXTM3U\n#EXT-X-KEY:METHOD\n",
	} {
		_, err := ParseHLS(mustURL(t, hlsBase), []byte(body))
		if !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: error = %v, want ErrMalformed", name, err)
		}
	}
}

func TestHLSLimits(t *testing.T) {
	if _, err := ParseHLS(nil, make([]byte, MaxManifestBytes+1)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversized playlist: %v", err)
	}
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	for i := 0; i <= MaxSegments; i++ {
		b.WriteString("#EXTINF:1,\na\n")
	}
	if _, err := ParseHLS(nil, []byte(b.String())); !errors.Is(err, ErrTooLarge) {
		t.Errorf("too many segments: %v", err)
	}
	b.Reset()
	b.WriteString("#EXTM3U\n")
	for i := 0; i <= MaxVariants; i++ {
		b.WriteString("#EXT-X-STREAM-INF:BANDWIDTH=1\nv\n")
	}
	if _, err := ParseHLS(nil, []byte(b.String())); !errors.Is(err, ErrTooLarge) {
		t.Errorf("too many variants: %v", err)
	}
	if _, err := ParseHLS(nil, []byte("#EXTM3U\n#EXTINF:1,\n"+strings.Repeat("a", MaxURIBytes+1)+"\n")); !errors.Is(err, ErrTooLarge) {
		t.Errorf("long URI: %v", err)
	}
	if _, err := ParseHLS(mustURL(t, "https://h/"+strings.Repeat("p", MaxURIBytes)), []byte("#EXTM3U\n")); !errors.Is(err, ErrTooLarge) {
		t.Errorf("long base: %v", err)
	}
	// A long base times many distinct segments exceeds the aggregate URL budget.
	base := mustURL(t, "https://h.example/"+strings.Repeat("p", MaxURIBytes-100)+"/i.m3u8")
	b.Reset()
	b.WriteString("#EXTM3U\n")
	for i := 0; i < MaxURLBytesTotal/(MaxURIBytes-100)+10; i++ {
		fmt.Fprintf(&b, "#EXTINF:1,\n%d\n", i)
	}
	if _, err := ParseHLS(base, []byte(b.String())); !errors.Is(err, ErrTooLarge) {
		t.Errorf("aggregate URL budget: %v", err)
	}
	var many strings.Builder
	for i := 0; i <= maxAttrs; i++ {
		fmt.Fprintf(&many, "X-%d=1,", i)
	}
	if _, err := ParseHLS(nil, []byte("#EXTM3U\n#EXT-X-STREAM-INF:"+many.String()+"BANDWIDTH=1\nv\n")); !errors.Is(err, ErrTooLarge) {
		t.Errorf("too many attributes: %v", err)
	}
}

func TestHLSBOMAndComments(t *testing.T) {
	body := "\xEF\xBB\xBF#EXTM3U\n# a comment\n#EXT-X-UNKNOWN-TAG:FOO=1\n#EXTINF:2.5\n  a.ts  \n#EXT-X-ENDLIST\n"
	p, err := ParseHLS(nil, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Segments) != 1 || p.Segments[0].URI != "a.ts" || p.Segments[0].Duration != 2500*time.Millisecond {
		t.Fatalf("segments = %+v", p.Segments)
	}
}

// Lines end with LF or CRLF only (RFC 8216 §4.1). A bare CR, which some readers also
// take as a line end, is refused rather than hiding what follows it; so is a key tag
// anywhere but at the start of its own line, which a reader that splits lines
// differently or searches for tags would apply.
func TestHLSHiddenKeyTags(t *testing.T) {
	media := "#EXTINF:10,\na.ts\n#EXT-X-ENDLIST\n"
	for name, c := range map[string]struct {
		body string
		want error
	}{
		"after CR in unknown tag": {"#EXT-X-TARGETDURATION:10\n#EXT-X-FOO:1\r#EXT-X-KEY:METHOD=AES-128,URI=\"k\"\n" + media, ErrEncrypted},
		"after CR in comment":     {"# comment\r#EXT-X-KEY:METHOD=AES-128,URI=\"k\"\n" + media, ErrEncrypted},
		"session key after CR": {"#EXT-X-SESSION-KEY:METHOD=NONE\r#EXT-X-SESSION-KEY:METHOD=SAMPLE-AES,URI=\"k\"\n" +
			"#EXT-X-STREAM-INF:BANDWIDTH=1\nv.m3u8\n", ErrEncrypted},
		"after line separator": {"# note #EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"k\"\n" + media, ErrEncrypted},
		"in a comment":         {"# see #EXT-X-KEY:METHOD=AES-128,URI=\"k\"\n" + media, ErrEncrypted},
		"in a URI":             {"#EXTINF:10,\na.ts#EXT-X-KEY:METHOD=AES-128\n#EXT-X-ENDLIST\n", ErrEncrypted},
		"in the header":        {"#EXT-X-KEY:METHOD=SAMPLE-AES\n" + media, ErrEncrypted},
		"in a quoted string":   {"#EXT-X-KEY:METHOD=NONE,KEYFORMAT=\"#EXT-X-KEY:METHOD=AES-128\"\n" + media, ErrMalformed},
		"embedded clear key":   {"# #EXT-X-KEY:METHOD=NONE\n" + media, ErrMalformed},
		"longer tag name":      {"#EXT-X-KEYS:METHOD=AES-128\n" + media, ErrMalformed},
		"bare CR":              {"#EXTINF:10,\ra.ts\n#EXT-X-ENDLIST\n", ErrMalformed},
		"CR line ends":         {"#EXTINF:10,\ra.ts\r#EXT-X-ENDLIST\r", ErrMalformed},
		"CR before CRLF":       {"#EXTINF:10,\r\r\na.ts\r\n#EXT-X-ENDLIST\r\n", ErrMalformed},
		"CR in a URI":          {"#EXTINF:10,\na\rb.ts\n#EXT-X-ENDLIST\n", ErrMalformed},
	} {
		body := "#EXTM3U\n" + c.body
		if name == "in the header" {
			body = "#EXTM3U " + c.body
		}
		if _, err := ParseHLS(nil, []byte(body)); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	if _, err := ParseHLS(nil, []byte("#EXTM3U\r#EXTINF:10,\ra.ts\r#EXT-X-ENDLIST\r")); !errors.Is(err, ErrMalformed) {
		t.Errorf("CR-only playlist: %v, want ErrMalformed", err)
	}
	// CRLF still ends lines, a lone CR at the end of the input included, and the key
	// tag of its own line is examined as before.
	p, err := ParseHLS(nil, []byte("#EXTM3U\r\n#EXT-X-KEY:METHOD=NONE\r\n#EXTINF:10,\r\na.ts\r\n#EXT-X-ENDLIST\r"))
	if err != nil || len(p.Segments) != 1 || p.Segments[0].URI != "a.ts" || !p.EndList {
		t.Fatalf("CRLF playlist: %v %+v", err, p)
	}
}
