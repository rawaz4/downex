package sitemedia

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"downex/internal/media"
	"downex/internal/mediajob"
)

// The fuzz targets below read untrusted yt-dlp output (docs/SITE-MEDIA-SPEC.md §6.1).
// Run one with: go test ./internal/sitemedia -run '^$' -fuzz '^FuzzParse$' -fuzztime 30s

// FuzzParse checks that Parse never panics, classifies every error, and that what it
// keeps, what Offers lists and what Select picks stay within the limits of §3.10 and
// §3.11.
func FuzzParse(f *testing.F) {
	files, err := filepath.Glob(filepath.Join("testdata", "*.json"))
	if err != nil || len(files) == 0 {
		f.Fatalf("no fixtures: %v", err)
	}
	for _, name := range files {
		b, err := os.ReadFile(name)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	for _, s := range []string{
		`{"id":"x","extractor_key":"G","formats":[{"format_id":"a","protocol":"https","url":"http://127.0.0.1/v","ext":"mp4","vcodec":"avc1","acodec":"mp4a","cookies":"a=b; Domain=127.0.0.1"}]}`,
		`{"id":"x","extractor_key":"G","formats":[{"format_id":"v","protocol":"https","url":"https://h/v?expire=1792630000&ip=::1&lmt=1","ext":"mp4","container":"mp4_dash","vcodec":"av01","acodec":"none","fps":NaN},{"format_id":"a","protocol":"http","url":"https://h/a","ext":"m4a","container":"m4a_dash","vcodec":"none","acodec":"ec-3","http_headers":{"X-A":"1","Cookie":"c"}}]}`,
		`{"id":"x","extractor_key":"G","formats":[{"format_id":"m","protocol":"http_dash_segments","manifest_url":"https://h/m.mpd"}]}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		info, err := Parse(data, testNow)
		if err != nil {
			checkClassified(t, err)
			return
		}
		if info.ID == "" || len(info.Formats) > MaxFormats || !utf8.ValidString(info.Title) || info.Title == "" {
			t.Fatalf("info id %q, %d formats, title %q", info.ID, len(info.Formats), info.Title)
		}
		ids := make(map[string]bool)
		for i := range info.Formats {
			fm := &info.Formats[i]
			if ids[fm.ID] {
				t.Fatalf("format id %q twice", fm.ID)
			}
			ids[fm.ID] = true
			u := fm.URL
			if !fm.direct() {
				u = fm.ManifestURL
			}
			if p, err := url.Parse(u); err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Hostname() == "" || p.User != nil {
				t.Fatalf("format %q has the URL %q", fm.ID, u)
			}
			if !strings.HasPrefix(fm.Key, keyPrefix) || fm.Kind == "" || fm.Size < 0 || fm.ApproxSize < 0 {
				t.Fatalf("format %+v", *fm)
			}
			for name := range fm.Headers {
				if name != "Cookie" && !strings.HasPrefix(name, "X-") && !slices.Contains(allowedHeaders, name) {
					t.Fatalf("format %q keeps the header %q", fm.ID, name)
				}
			}
		}
		for _, host := range []string{"example.com", "127.0.0.1", "h"} {
			off, err := Offers(info, host)
			if err != nil {
				checkClassified(t, err)
				continue
			}
			checkOffer(t, info, off)
		}
	})
}

// checkOffer checks an offer and the picks it allows.
func checkOffer(t *testing.T, info *Info, off *Offer) {
	t.Helper()
	if off.Container != ContainerMP4 {
		t.Fatalf("offer container %q", off.Container)
	}
	if off.Format != FormatHTTP {
		if (off.Format != FormatHLS && off.Format != FormatDASH) || off.ManifestURL == "" || len(off.Tracks) != 0 {
			t.Fatalf("manifest offer %+v", off)
		}
		return
	}
	if len(off.Tracks) == 0 || len(off.Tracks) > MaxOffered || off.DefaultVideo != off.Tracks[0].ID {
		t.Fatalf("offer with %d tracks, default %q", len(off.Tracks), off.DefaultVideo)
	}
	for _, tr := range off.Tracks {
		if tr.Ext != "mp4" && tr.Ext != "m4a" {
			t.Fatalf("offered track %s has ext %q", tr.ID, tr.Ext)
		}
		video := tr.Kind != media.KindAudio
		pick, err := Select(info, off, tr.ID, "", tr.Key, "")
		if !video {
			pick, err = Select(info, off, "", tr.ID, "", tr.Key)
		}
		if err != nil {
			if !video && errors.Is(err, media.ErrMalformed) { // the default video is muxed
				continue
			}
			t.Fatalf("Select(%s): %v", tr.ID, err)
		}
		if pick.Container != ContainerMP4 || len(pick.Tracks) == 0 || len(pick.Tracks) > 2 {
			t.Fatalf("pick %+v", pick)
		}
		for _, rt := range pick.Tracks {
			rt.ChunkBytes = mediajob.ChunkSize(nil, rt.URL, info.Format(rt.ID).ChunkSize)
			if _, err := media.PlanRanged(rt); err != nil {
				t.Fatalf("PlanRanged(%s): %v", rt.ID, err)
			}
		}
	}
	// One video per quality class (videos without a picture, class 0, have none); the
	// videos left out still select by key and are then listed once, in place of an
	// offered video of their class (class 0: the last offered video).
	classes := make(map[int]bool)
	for _, tr := range off.Tracks {
		if tr.Kind == media.KindAudio {
			continue
		}
		if c, ok := off.class[tr.ID]; !ok || (c > 0 && classes[c]) {
			t.Fatalf("offered video %s of class %d (known %v) is not the only one of its class", tr.ID, c, ok)
		}
		classes[off.class[tr.ID]] = true
	}
	for _, tr := range off.more {
		pick, err := Select(info, off, "", "", tr.Key, "")
		if err != nil || pick.Tracks[0].ID != tr.ID {
			t.Fatalf("Select(left-out %s) = %+v, %v", tr.ID, pick, err)
		}
		listed := off.Listed(pick)
		at := slices.IndexFunc(listed, func(l media.Track) bool { return l.ID == tr.ID })
		if len(listed) != len(off.Tracks) || at < 0 || listed[at].Kind == media.KindAudio ||
			(off.class[tr.ID] > 0 && off.class[off.Tracks[at].ID] != off.class[tr.ID]) ||
			(off.class[tr.ID] == 0 && (at+1 < len(listed) && listed[at+1].Kind != media.KindAudio)) {
			t.Fatalf("Listed(%s) = %d tracks, at %d", tr.ID, len(listed), at)
		}
	}
}

// checkClassified fails unless err wraps one of the errors Parse and Offers return.
func checkClassified(t *testing.T, err error) {
	t.Helper()
	for _, kind := range []error{media.ErrMalformed, media.ErrTooLarge, media.ErrUnsupported, media.ErrLive,
		media.ErrEncrypted, ErrSiteUnsupported, ErrAuthRequired} {
		if errors.Is(err, kind) {
			if strings.Contains(err.Error(), "://") {
				t.Fatalf("error repeats a URL: %v", err)
			}
			return
		}
	}
	t.Fatalf("unclassified error %v", err)
}

// FuzzCookieHeader checks that a Cookie value built from a cookies field is a valid
// header value within its limits.
func FuzzCookieHeader(f *testing.F) {
	f.Add("a=b; Domain=.example.com; Path=/; Secure; Expires=1800000000; c=d; Domain=example.com", "https://cdn.example.com/v")
	f.Add(`x="q;r"; Domain=EXAMPLE.com.; Path=/a`, "http://example.com/a/b")
	f.Add("; ; =; Domain=; a=1; Domain=1.2.3.4", "https://1.2.3.4/")
	f.Fuzz(func(t *testing.T, cookies, target string) {
		if len(cookies) > maxCookiesFieldBytes {
			return
		}
		h := cookieHeader(cookies, target, testNow)
		if len(h) > maxCookieBytes || strings.Count(h, "; ") >= maxCookies || !printableASCII(h) {
			t.Fatalf("cookie header %q", h)
		}
	})
}
