package media

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// The fuzz targets below parse untrusted manifests and segments (docs/MEDIA-SPEC.md §3).
// Run one with: go test ./internal/media -run '^$' -fuzz FuzzParseHLS -fuzztime 30s

// addFixtures seeds f with every testdata file matching pattern.
func addFixtures(f *testing.F, pattern string) {
	f.Helper()
	files, err := filepath.Glob(filepath.Join("testdata", pattern))
	if err != nil || len(files) == 0 {
		f.Fatalf("no fixtures for %s: %v", pattern, err)
	}
	for _, name := range files {
		b, err := os.ReadFile(name)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
}

func FuzzParseHLS(f *testing.F) {
	addFixtures(f, "hls/*.m3u8")
	for _, s := range []string{
		"#EXTM3U\n#EXTINF:1,\n#EXT-X-BYTERANGE:10\na\n",
		"#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1,RESOLUTION=1x1,CODECS=\",\"\n\n",
		"#EXTM3U\n#EXT-X-DEFINE:NAME=\"a\",VALUE=\"{$a}\"\n#EXTINF:0,\n{$a}{$a}\n#EXT-X-ENDLIST",
		"#EXTM3U\r\n#EXT-X-MAP:URI=\"i\",BYTERANGE=\"5\"\r\n#EXT-X-GAP\r\n#EXTINF:.5\r\nx\r\n#EXT-X-ENDLIST",
		"#EXTM3U\n#EXT-X-KEY:METHOD=NONE,URI=\"\"\n#EXT-X-MEDIA-SEQUENCE:18446744073709551615\n#EXTINF:1,\na\n",
	} {
		f.Add([]byte(s))
	}
	bases := []*url.URL{mustURL(f, "https://example.com/a/b/index.m3u8?token=x"), nil}
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, base := range bases {
			p, err := ParseHLS(base, data)
			if err != nil {
				checkClassified(t, "ParseHLS", err)
				continue
			}
			if p.Master {
				if len(p.Variants) == 0 || len(p.Variants) > MaxVariants || len(p.Renditions) > MaxVariants || len(p.Segments) != 0 {
					t.Fatalf("master with %d variants, %d renditions, %d segments", len(p.Variants), len(p.Renditions), len(p.Segments))
				}
			} else if len(p.Segments) > MaxSegments || len(p.Variants) != 0 {
				t.Fatalf("media playlist with %d segments, %d variants", len(p.Segments), len(p.Variants))
			}
			for _, tr := range p.Options() {
				if len(tr.URL) > MaxURIBytes {
					t.Fatalf("track URL of %d bytes", len(tr.URL))
				}
			}
			DefaultPair(p.Options())
			plan, err := PlanHLS(p)
			if err != nil {
				checkClassified(t, "PlanHLS", err)
				continue
			}
			checkPlan(t, plan)
		}
	})
}

func FuzzParseDASH(f *testing.F) {
	addFixtures(f, "dash/*.mpd")
	for _, s := range []string{
		`<MPD mediaPresentationDuration="PT1S"><Period><AdaptationSet><SegmentTemplate media="$Number%03d$$$" duration="1" timescale="3"/><Representation id="r"/></AdaptationSet></Period></MPD>`,
		`<MPD><Period duration="PT2S"><AdaptationSet><SegmentTemplate media="$Time$"><SegmentTimeline><S t="5" d="1" r="-1"/><S t="9" d="2"/></SegmentTimeline></SegmentTemplate><Representation id="r"/></AdaptationSet></Period></MPD>`,
		`<MPD><Period duration="PT1S"><AdaptationSet><Representation id="r"><SegmentList duration="1"><SegmentURL media="a" mediaRange="0-1"/></SegmentList></Representation></AdaptationSet></Period></MPD>`,
		`<MPD><Period start="PT1S"/><Period start="PT0S"/></MPD>`,
	} {
		f.Add([]byte(s))
	}
	bases := []*url.URL{mustURL(f, "https://example.com/vod/manifest.mpd"), nil}
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, base := range bases {
			m, err := ParseDASH(base, data)
			if err != nil {
				checkClassified(t, "ParseDASH", err)
				continue
			}
			if len(m.Periods) == 0 || len(m.Periods) > MaxVariants {
				t.Fatalf("%d periods", len(m.Periods))
			}
			reps := 0
			for _, per := range m.Periods {
				for _, as := range per.AdaptationSets {
					reps += len(as.Representations)
				}
			}
			if reps > MaxVariants {
				t.Fatalf("%d representations", reps)
			}
			tracks := m.Options()
			DefaultPair(tracks)
			// Plan a few Representations; each plan is bounded by MaxSegments.
			for i, tr := range tracks {
				if i == 3 {
					break
				}
				plan, err := PlanDASH(m, tr.ID)
				if err != nil {
					checkClassified(t, "PlanDASH", err)
					continue
				}
				checkPlan(t, plan)
			}
		}
	})
}

func FuzzCheckInit(f *testing.F) {
	f.Add(initSegment("vide", 0, visualEntry("avc1", avcC())))
	f.Add(initSegment("soun", 0, audioEntry("mp4a", 1, protection("cenc"))))
	f.Add(initSegment("vide", 0, visualEntry("encv", protection("cbcs"))))
	f.Add(cat(ftyp(), box("moov"), u32(1), []byte("mdat"), make([]byte, 8)))
	f.Add(cat(ftyp(), box("moov", u32(0), []byte("trak"))))
	f.Add([]byte{0x47, 0x40, 0x00, 0x10})
	f.Fuzz(func(t *testing.T, data []byte) {
		if err := CheckInit(data); err != nil {
			checkClassified(t, "CheckInit", err)
		}
		Sniff(data)
	})
}
