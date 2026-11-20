package sitemedia

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"downex/internal/media"
)

// Audio pages (docs/SITE-MEDIA-SPEC.md §3.10, §3.13): single-format answers, raw AAC,
// previews, the next usable group and the expiry of a stream's signed link.

// A single-format answer (no formats; the one format at the top level, as for a
// podcast episode) is read as its one format: an MP3 page offers it.
func TestParseSingleFormat(t *testing.T) {
	data := []byte(`{"id": "ep1", "extractor_key": "Generic", "title": "Episode 1", "duration": 1800,
		"format_id": "0", "protocol": "https", "url": "https://media.example.com/ep1.mp3", "ext": "mp3",
		"vcodec": "none", "acodec": "mp3", "abr": 128}`)
	for _, d := range [][]byte{data, []byte(strings.Replace(string(data), `"format_id"`, `"formats": [], "format_id"`, 1))} {
		info, err := Parse(d, testNow)
		if err != nil {
			t.Fatal(err)
		}
		if !info.AudioOnly || len(info.Formats) != 1 || info.Formats[0].ID != "0" || info.Formats[0].URL != "https://media.example.com/ep1.mp3" {
			t.Fatalf("info %+v", info)
		}
		off, err := Offers(info, "podcasts.example.com")
		if err != nil || off.Container != ContainerMP3 || off.DefaultAudio != "0" {
			t.Fatalf("Offers = %+v, %v", off, err)
		}
	}
	// A list of formats wins over the top level; no url, no format.
	info, err := Parse([]byte(`{"id": "x", "extractor_key": "Generic", "url": "https://media.example.com/top.mp3", "format_id": "top",
		"protocol": "https", "ext": "mp3", "vcodec": "none", "formats": [{"format_id": "a", "protocol": "https",
		"url": "https://media.example.com/a.mp3", "ext": "mp3", "vcodec": "none"}]}`), testNow)
	if err != nil || len(info.Formats) != 1 || info.Formats[0].ID != "a" {
		t.Fatalf("formats and a top-level url: %+v, %v", info, err)
	}
	if info, err := Parse([]byte(`{"id": "x", "extractor_key": "Generic", "ext": "mp3", "vcodec": "none"}`), testNow); err != nil ||
		len(info.Formats) != 0 || info.AudioOnly {
		t.Fatalf("no url: %+v, %v", info, err)
	}
}

// Raw AAC (ADTS) and previews are refused with their own reasons; a preview is used only
// when no whole track is offered.
func TestOffersAudioRawAndPreview(t *testing.T) {
	adts := `{"format_id": "a", "protocol": "https", "url": "https://cdn.example.com/a.aac", "ext": "aac", "vcodec": "none", "acodec": "aac"}`
	preview := func(id string) string {
		return `{"format_id": "` + id + `", "protocol": "https", "url": "https://cdn.example.com/` + id + `.mp3", "ext": "mp3", "vcodec": "none", "acodec": "mp3", "abr": 128}`
	}
	for name, c := range map[string]struct {
		data []byte
		text string
	}{
		"raw AAC":           {page(adts), "raw AAC stream"},
		"preview only":      {page(preview("http_mp3_preview")), "only a preview of this track is public"},
		"preview by note":   {page(strings.Replace(preview("m"), `"abr"`, `"format_note": "Preview", "abr"`, 1)), "only a preview"},
		"previews and opus": {page(preview("http_mp3_preview"), `{"format_id": "o", "protocol": "https", "url": "https://cdn.example.com/o", "ext": "opus", "vcodec": "none", "acodec": "opus"}`), "only a preview"},
	} {
		info, err := Parse(c.data, testNow)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if off, err := Offers(info, "example.com"); !errors.Is(err, media.ErrUnsupported) || off != nil || !strings.Contains(err.Error(), c.text) {
			t.Errorf("%s: Offers = %+v, %v; want %q", name, off, err, c.text)
		}
	}
	// The whole track wins over a preview of a higher rate; the word must stand alone.
	info, err := Parse(page(strings.Replace(preview("hq_preview"), "128", "320", 1), preview("http_mp3_128"), preview("previewless")), testNow)
	if err != nil {
		t.Fatal(err)
	}
	off, err := Offers(info, "example.com")
	if err != nil || len(off.Tracks) != 2 || off.DefaultAudio != "http_mp3_128" {
		t.Fatalf("Offers = %+v, %v", off, err)
	}
	if f := info.Format("previewless"); f.Preview {
		t.Error("previewless is marked a preview")
	}
}

// The usable groups follow each other by best rate, ties in the group order.
func TestOffersAudioNext(t *testing.T) {
	info, err := Parse(page(
		`{"format_id": "mp3", "protocol": "https", "url": "https://cdn.example.com/a.mp3", "ext": "mp3", "vcodec": "none", "acodec": "mp3", "abr": 128}`,
		`{"format_id": "dash", "protocol": "http_dash_segments", "url": "https://cdn.example.com/m.mpd", "ext": "m4a", "vcodec": "none", "acodec": "mp4a.40.2", "abr": 160}`,
		`{"format_id": "aac", "protocol": "https", "url": "https://cdn.example.com/a.m4a", "ext": "m4a", "vcodec": "none", "acodec": "mp4a.40.2", "abr": 128}`,
	), testNow)
	if err != nil {
		t.Fatal(err)
	}
	off, err := Offers(info, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for o := off; o != nil; o = o.Next() {
		got = append(got, o.Format+"/"+o.Container)
	}
	if strings.Join(got, " ") != "dash/m4a http/m4a http/mp3" {
		t.Errorf("groups %v", got)
	}
	_, video := offerFixture(t, "youtube.json", "youtube.com")
	if video.Next() != nil {
		t.Error("a video page's offer has a next")
	}
}

func policyURL(u string, ends ...int64) string {
	var st []string
	for _, e := range ends {
		st = append(st, fmt.Sprintf(`{"Resource":"*","Condition":{"DateLessThan":{"AWS:EpochTime":%d}}}`, e))
	}
	p := base64.StdEncoding.EncodeToString([]byte(`{"Statement":[` + strings.Join(st, ",") + `]}`))
	return u + "Policy=" + strings.NewReplacer("+", "-", "=", "_", "/", "~").Replace(p) + "&Signature=s"
}

func TestSignedExpiry(t *testing.T) {
	now := testNow.Unix()
	at := func(d int64) time.Time { return time.Unix(now+d, 0).UTC() }
	for _, c := range []struct {
		url  string
		want time.Time
	}{
		{"https://cdn.example.com/p.m3u8", time.Time{}},
		{fmt.Sprintf("https://cdn.example.com/p.m3u8?Expires=%d", now+600), at(600)},
		{policyURL(fmt.Sprintf("https://cdn.example.com/p.m3u8?expires=%d&", now+7200), now+320), at(320)},
		{policyURL(fmt.Sprintf("https://cdn.example.com/p.m3u8?Expires=%d&", now+7200), now+320), at(320)},
		{policyURL(fmt.Sprintf("https://cdn.example.com/p.m3u8?Expires=%d&", now+100), now+320), at(100)},
		{policyURL("https://cdn.example.com/p.m3u8?", now+900, now+300), at(300)},
		{policyURL("https://cdn.example.com/p.m3u8?", now-10), time.Time{}},                  // already past
		{policyURL("https://cdn.example.com/p.m3u8?", now+30*24*3600), time.Time{}},          // beyond 7 days
		{"https://cdn.example.com/p.m3u8?Policy=%%%&Expires=abc", time.Time{}},               // unreadable
		{"https://cdn.example.com/p.m3u8?Policy=" + strings.Repeat("A", 8<<10), time.Time{}}, // too long
	} {
		if got := signedExpiry(c.url, testNow); !got.Equal(c.want) {
			t.Errorf("signedExpiry(%.80s) = %v, want %v", c.url, got, c.want)
		}
	}
	// A stream offer carries the expiry of its playlist; a direct format's own rules stay.
	info, err := Parse(page(`{"format_id": "hls", "protocol": "m3u8_native", "url": "`+policyURL("https://cdn.example.com/p.m3u8?", now+320)+
		`", "ext": "m4a", "vcodec": "none", "acodec": "mp4a.40.2"}`), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if off, err := Offers(info, "example.com"); err != nil || !off.Expires.Equal(at(320)) {
		t.Errorf("Offers = %+v, %v", off, err)
	}
}

func TestAudioSite(t *testing.T) {
	for host, want := range map[string]bool{
		"soundcloud.com": true, "m.soundcloud.com": true, "www.mixcloud.com": true, "artist.bandcamp.com": true,
		"BANDCAMP.COM:443": true, "bandcamp.com.": true, "notbandcamp.com": false, "soundcloud.com.example.com": false,
		"youtube.com": false, "": false,
	} {
		if AudioSite(host) != want {
			t.Errorf("AudioSite(%q) = %v", host, !want)
		}
	}
	if err := PlaylistError(media.ErrUnsupported, "artist.bandcamp.com"); !errors.Is(err, ErrPlaylist) ||
		err.Error() != "media: unsupported operation: the page is a playlist; open a single track of it" {
		t.Errorf("PlaylistError = %v", err)
	}
}
