package media

import (
	"strings"
	"testing"
)

// TestHLSTrackKeys: a track's Key stays with its attributes when the master playlist
// lists its tracks in another order or with other URIs (session tokens), differs between
// tracks, also of identical attributes, and is "" where the ID is stable already.
func TestHLSTrackKeys(t *testing.T) {
	lines := []string{
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="English",LANGUAGE="en",DEFAULT=YES,URI="en/index.m3u8?s=1"`,
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="Deutsch",LANGUAGE="de",URI="de/index.m3u8?s=1"`,
		"#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360,CODECS=\"avc1.4d401e,mp4a.40.2\",AUDIO=\"aud\"\nlow/index.m3u8?s=1",
		"#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1280x720,CODECS=\"avc1.4d401f,mp4a.40.2\",AUDIO=\"aud\"\nhigh/index.m3u8?s=1",
		// A redundant stream of the 720p variant (RFC 8216 §6.2.3): identical attributes.
		"#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1280x720,CODECS=\"avc1.4d401f,mp4a.40.2\",AUDIO=\"aud\"\nbackup/high/index.m3u8?s=1",
	}
	parse := func(order []int, session string) map[string]string {
		t.Helper()
		var b strings.Builder
		b.WriteString("#EXTM3U\n")
		for _, i := range order {
			b.WriteString(strings.ReplaceAll(lines[i], "?s=1", "?s="+session) + "\n")
		}
		p, err := ParseHLS(mustURL(t, "https://m.example/s/"+session+"/master.m3u8"), []byte(b.String()))
		if err != nil {
			t.Fatal(err)
		}
		byURL := map[string]string{} // URL without the session → key
		seen := map[string]bool{}
		for _, tr := range p.Options() {
			if !strings.HasPrefix(tr.Key, "h") || len(tr.Key) != 25 {
				t.Fatalf("track %s: key %q", tr.ID, tr.Key)
			}
			if seen[tr.Key] {
				t.Fatalf("track %s: key %q is not unique", tr.ID, tr.Key)
			}
			seen[tr.Key] = true
			u := strings.ReplaceAll(strings.ReplaceAll(tr.URL, "/s/"+session+"/", "/s/X/"), "?s="+session, "")
			byURL[u] = tr.Key
		}
		return byURL
	}
	a := parse([]int{0, 1, 2, 3, 4}, "one")
	b := parse([]int{1, 0, 3, 2, 4}, "two") // renditions and the two 720p variants swapped
	for u, k := range a {
		if strings.Contains(u, "high/") {
			continue // the redundant streams may trade keys: they are the same rendition
		}
		if b[u] != k {
			t.Errorf("%s: key %q, then %q after reordering", u, k, b[u])
		}
	}
	if len(b) != len(a) {
		t.Fatalf("keys %v, then %v", a, b)
	}

	media, err := ParseHLS(mustURL(t, hlsBase), fixture(t, "hls/media_vod.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	if tr := media.Options(); tr[0].Key != "" {
		t.Errorf("media playlist track key = %q", tr[0].Key)
	}
	for _, tr := range parseFixtureMPD(t, "dash/template_number.mpd").Options() {
		if tr.Key != "" {
			t.Errorf("DASH track %s: key %q", tr.ID, tr.Key)
		}
	}
}
