package media

import (
	"strings"
	"testing"
)

// Error messages never repeat URIs, which may carry credentials.
func TestErrorsDoNotRepeatURIs(t *testing.T) {
	const secret = "s3cr3t-t0ken"
	hls := []string{
		"#EXTM3U\n#EXTINF:1,\nhttp://[::1]:x/" + secret + "\n",
		"#EXTM3U\n#EXTINF:1,\nftp://h/" + secret + "\n",
		"#EXTM3U\n#EXT-X-MAP:URI=\"http://[" + secret + "\"\n",
		"#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"https://k/" + secret + "\"\n",
		"#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nhttp://h/%zz" + secret + "\n",
		"#EXTM3U\n#EXTINF:1,\n#EXT-X-BYTERANGE:5\nhttps://h/" + secret + "\n",
		"#EXTM3U\n#EXTINF:1,\n" + strings.Repeat("a", MaxURIBytes) + secret + "\n",
	}
	for _, in := range hls {
		_, err := ParseHLS(mustURL(t, "https://h/i.m3u8?"+secret), []byte(in))
		if err == nil {
			t.Fatalf("no error for %.60q", in)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error repeats a URI: %v", err)
		}
	}
	dash := []string{
		`<MPD><BaseURL>http://[` + secret + `</BaseURL><Period/></MPD>`,
		`<MPD><BaseURL>file:///` + secret + `</BaseURL><Period/></MPD>`,
		`<MPD xmlns:xlink="http://www.w3.org/1999/xlink"><Period xlink:href="https://h/` + secret + `"/></MPD>`,
		`<MPD><Period duration="PT1S"><AdaptationSet><SegmentTemplate media="` + secret + `$Bogus$" duration="1"/><Representation id="r"/></AdaptationSet></Period></MPD>`,
		`<MPD><Period duration="PT1S"><AdaptationSet><SegmentTemplate media="ftp://h/` + secret + `/$Number$" duration="1"/><Representation id="r"/></AdaptationSet></Period></MPD>`,
	}
	for _, in := range dash {
		m, err := ParseDASH(mustURL(t, "https://h/m.mpd?"+secret), []byte(in))
		if err == nil {
			_, err = PlanDASH(m, "r")
		}
		if err == nil {
			t.Fatalf("no error for %.60q", in)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error repeats a URI: %v", err)
		}
	}
}
