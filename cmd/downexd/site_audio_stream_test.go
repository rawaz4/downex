package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"downex/internal/daemon"
	"downex/internal/media"
	"downex/internal/model"
	"downex/internal/sitemedia"
)

// Audio pages' streams (docs/SITE-MEDIA-SPEC.md §3.13): an audio-only MPD and an HLS
// master of audio-only variants are saved as their one sound track; a stream that
// cannot be saved gives way to the page's next usable audio; a stream's link that the
// server refuses (it expired) makes the page be read again (§2.6).

// audioMPD serves the test audio track as an audio-only MPD at dir/m.mpd, its
// Representation "aac" with segment templates; it returns the MPD's URL.
func audioMPD(t *testing.T, srv *fileServer, dir, mime, codecs string) string {
	t.Helper()
	_, audio := testPair(t)
	srv.add(dir+"/aac-init", audio.init)
	for i, f := range audio.frags {
		srv.add(fmt.Sprintf("%s/aac-%d", dir, i+1), f)
	}
	// Each fragment is 50 samples of 1024 at 48 kHz: 1.0667 s.
	segDur := 50 * 1024 * 1000 / 48000
	total := float64(segDur*len(audio.frags)) / 1000
	srv.add(dir+"/m.mpd", fmt.Appendf(nil, `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT%.3fS" minBufferTime="PT2S">
 <Period>
  <AdaptationSet mimeType="%s" contentType="audio">
   <SegmentTemplate initialization="$RepresentationID$-init" media="$RepresentationID$-$Number$" duration="%d" timescale="1000" startNumber="1"/>
   <Representation id="aac" codecs="%s" bandwidth="160000"/>
  </AdaptationSet>
 </Period>
</MPD>`, total, mime, segDur, codecs))
	return srv.url(dir + "/m.mpd")
}

// audioHLS serves the test audio track as a media playlist at dir/playlist.m3u8 and a
// master listing it as its one audio-only variant at dir/master.m3u8; it returns the
// master's URL.
func audioHLS(t *testing.T, srv *fileServer, dir string) string {
	t.Helper()
	_, audio := testPair(t)
	var pl strings.Builder
	pl.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:2\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-MAP:URI=\"init.mp4\"\n")
	for i, f := range audio.frags {
		srv.add(fmt.Sprintf("%s/seg%d.m4s", dir, i), f)
		fmt.Fprintf(&pl, "#EXTINF:1.067,\nseg%d.m4s\n", i)
	}
	pl.WriteString("#EXT-X-ENDLIST\n")
	srv.add(dir+"/init.mp4", audio.init)
	srv.add(dir+"/playlist.m3u8", []byte(pl.String()))
	srv.add(dir+"/master.m3u8", []byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=130000,CODECS=\"mp4a.40.2\"\nplaylist.m3u8\n"))
	return srv.url(dir + "/master.m3u8")
}

// probeAndRunAudio probes an audio page of formats, checks it offers one m4a audio
// track of format, runs it as the clients record it and checks the saved file.
func probeAndRunAudio(t *testing.T, srv *fileServer, format string, formats ...format) {
	t.Helper()
	_, audio := testPair(t)
	s := newSiteMedia(&fakeResolver{pages: []string{audioPage(t, formats...)}})
	pageURL := srv.url("/artist/track")
	p, err := s.Probe(siteContext(t), siteFactory(t), daemon.SiteSelection{PageURL: pageURL})
	if err != nil {
		t.Fatal(err)
	}
	if p.Format != format || p.Container != "m4a" || p.DefaultVideo != "" || len(p.Tracks) != 1 ||
		p.Tracks[0].Kind != media.KindAudio || p.DefaultAudio != p.Tracks[0].ID {
		t.Fatalf("probe = %+v", p)
	}
	opts := runOptions(t, daemon.MediaRunOptions{})
	sel := daemon.SiteSelection{PageURL: pageURL, Format: format, AudioTrack: p.DefaultAudio, AudioKey: p.Tracks[0].Key}
	container, err := s.Run(siteContext(t), opts, sel, nil, nil, func(model.MediaCheckpoint) error { return nil })
	if err != nil || container != "m4a" {
		t.Fatalf("Run = %q, %v", container, err)
	}
	checkOutput(t, opts.OutPath, audio)
}

// An audio-only MPD (streamed AAC over DASH) is one audio track.
func TestSiteAudioDASH(t *testing.T) {
	srv := newFileServer(t)
	m := audioMPD(t, srv, "/d", "audio/mp4", "mp4a.40.2")
	probeAndRunAudio(t, srv, "dash", format{"format_id": "dash-aac", "protocol": "http_dash_segments", "url": m,
		"manifest_url": m, "ext": "m4a", "vcodec": "none", "acodec": "mp4a.40.2", "abr": 160})
}

// An HLS master whose one variant is audio only, given as the format's own address (no
// media playlist of its own), is that variant's audio track.
func TestSiteAudioHLSMaster(t *testing.T) {
	srv := newFileServer(t)
	m := audioHLS(t, srv, "/h")
	probeAndRunAudio(t, srv, "hls", format{"format_id": "hls-130", "protocol": "m3u8_native", "url": m, "manifest_url": m,
		"ext": "m4a", "vcodec": "none", "acodec": "mp4a.40.2", "abr": 130})
}

// A DASH AAC stream of a higher rate than the page's MP3 is offered and saved.
func TestSiteAudioDASHBeatsMP3(t *testing.T) {
	srv := newFileServer(t)
	m := audioMPD(t, srv, "/d", "audio/mp4", "mp4a.40.2")
	srv.add("/a.mp3", testMP3())
	probeAndRunAudio(t, srv, "dash",
		format{"format_id": "mp3-128", "protocol": "https", "url": srv.url("/a.mp3"), "ext": "mp3", "vcodec": "none",
			"acodec": "mp3", "abr": 128},
		format{"format_id": "dash-aac", "protocol": "http_dash_segments", "url": m, "manifest_url": m, "ext": "m4a",
			"vcodec": "none", "acodec": "mp4a.40.2", "abr": 160})
}

func testMP3() []byte {
	return append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), bytes.Repeat([]byte{0xff, 0xfb, 0x90, 0x64}, 300_000)...)
}

// A stream that turns out to hold nothing Downex saves (yt-dlp named AAC, the MPD has
// WebM) gives way to the page's MP3, which the run then downloads as recorded; without
// another usable audio the refusal is the audio one, not "no MP4 video".
func TestSiteAudioStreamFallback(t *testing.T) {
	srv := newFileServer(t)
	m := audioMPD(t, srv, "/d", "audio/webm", "opus")
	mp3 := testMP3()
	srv.add("/a.mp3", mp3)
	stream := format{"format_id": "dash-aac", "protocol": "http_dash_segments", "url": m, "manifest_url": m, "ext": "m4a",
		"vcodec": "none", "acodec": "mp4a.40.2", "abr": 160}
	page := audioPage(t, format{"format_id": "mp3-128", "protocol": "https", "url": srv.url("/a.mp3"), "ext": "mp3",
		"vcodec": "none", "acodec": "mp3", "abr": 128}, stream)
	s := newSiteMedia(&fakeResolver{pages: []string{page}})
	pageURL := srv.url("/artist/track")
	p, err := s.Probe(siteContext(t), siteFactory(t), daemon.SiteSelection{PageURL: pageURL})
	if err != nil {
		t.Fatal(err)
	}
	if p.Format != "http" || p.Container != "mp3" || p.DefaultAudio != "mp3-128" || len(p.Tracks) != 1 {
		t.Fatalf("probe = %+v", p)
	}
	opts := runOptions(t, daemon.MediaRunOptions{})
	sel := daemon.SiteSelection{PageURL: pageURL, Format: "http", AudioTrack: "mp3-128", AudioKey: keyOf(t, page, "mp3-128")}
	if container, err := s.Run(siteContext(t), opts, sel, nil, nil, nil); err != nil || container != "mp3" {
		t.Fatalf("Run = %q, %v", container, err)
	}
	if got, err := os.ReadFile(opts.OutPath); err != nil || !bytes.Equal(got, mp3) {
		t.Fatalf("output: %d bytes, %v", len(got), err)
	}

	s = newSiteMedia(&fakeResolver{pages: []string{audioPage(t, stream)}})
	_, err = s.Probe(siteContext(t), siteFactory(t), daemon.SiteSelection{PageURL: pageURL})
	if !errors.Is(err, media.ErrUnsupported) || !strings.Contains(err.Error(), "no AAC or MP3 audio") {
		t.Fatalf("Probe of the stream alone = %v", err)
	}
}

// signedURL is u with a signed link's Policy whose end is at.
func signedURL(u string, at time.Time) string {
	policy := fmt.Sprintf(`{"Statement":[{"Resource":"*","Condition":{"DateLessThan":{"AWS:EpochTime":%d}}}]}`, at.Unix())
	enc := strings.NewReplacer("+", "-", "=", "_", "/", "~").Replace(base64.StdEncoding.EncodeToString([]byte(policy)))
	return u + "?Policy=" + enc + "&Signature=sig"
}

// A stream's playlist that its server refuses (403: the signed link expired) makes the
// probe and the run read the page once more past the cache and use its fresh link; a
// refusal that stays says the link was refused, not "yt-dlp" nor "no MP4 video".
func TestSiteAudioStreamLinkRefused(t *testing.T) {
	srv := newFileServer(t)
	m := audioHLS(t, srv, "/h")
	srv.deny["/old/playlist.m3u8"] = http.StatusForbidden
	stream := func(u string) string {
		return audioPage(t, format{"format_id": "hls_aac_96k", "protocol": "m3u8_native", "url": u, "ext": "m4a",
			"vcodec": "none", "acodec": "mp4a.40.2", "abr": 96})
	}
	playlist := strings.TrimSuffix(m, "master.m3u8") + "playlist.m3u8"
	old, fresh := stream(srv.url("/old/playlist.m3u8")), stream(playlist)
	pageURL := srv.url("/artist/track")

	res := &fakeResolver{pages: []string{old, fresh}}
	s := newSiteMedia(res)
	p, err := s.Probe(siteContext(t), siteFactory(t), daemon.SiteSelection{PageURL: pageURL})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.freshness(); !slices.Equal(got, []bool{false, true}) {
		t.Errorf("probe resolutions fresh = %v", got)
	}

	res = &fakeResolver{pages: []string{old, fresh}}
	s = newSiteMedia(res)
	s.retryDelay = time.Hour // a refused link is read again at once
	opts := runOptions(t, daemon.MediaRunOptions{})
	sel := daemon.SiteSelection{PageURL: pageURL, Format: "hls", AudioTrack: p.DefaultAudio, AudioKey: p.Tracks[0].Key}
	container, err := s.Run(siteContext(t), opts, sel, nil, nil, func(model.MediaCheckpoint) error { return nil })
	if err != nil || container != "m4a" {
		t.Fatalf("Run = %q, %v", container, err)
	}
	if got := res.freshness(); !slices.Equal(got, []bool{false, true}) {
		t.Errorf("run resolutions fresh = %v", got)
	}

	s = newSiteMedia(&fakeResolver{pages: []string{old}})
	_, err = s.Probe(siteContext(t), siteFactory(t), daemon.SiteSelection{PageURL: pageURL})
	if !errors.Is(err, sitemedia.ErrSiteError) || !strings.Contains(err.Error(), "refused the link") ||
		!strings.Contains(err.Error(), "403") {
		t.Fatalf("Probe with a link refused again = %v", err)
	}
	s = newSiteMedia(&fakeResolver{pages: []string{old}})
	_, err = s.Run(siteContext(t), runOptions(t, daemon.MediaRunOptions{}), sel, nil, nil, nil)
	if !errors.Is(err, sitemedia.ErrSiteError) || !strings.Contains(err.Error(), "refused the link") {
		t.Fatalf("Run with a link refused again = %v", err)
	}
}

// A cached stream whose signed link expires soon is read again past the cache: by a
// probe when it ends within a minute, by a run within siteMinValid.
func TestSiteAudioStreamLinkExpiry(t *testing.T) {
	srv := newFileServer(t)
	m := audioHLS(t, srv, "/h")
	playlist := strings.TrimSuffix(m, "master.m3u8") + "playlist.m3u8"
	page := func(expires time.Time) string {
		return audioPage(t, format{"format_id": "hls_aac_96k", "protocol": "m3u8_native", "url": signedURL(playlist, expires),
			"ext": "m4a", "vcodec": "none", "acodec": "mp4a.40.2", "abr": 96})
	}
	pageURL := srv.url("/artist/track")
	for name, c := range map[string]struct {
		in        time.Duration
		probe     []bool
		runFresh  []bool
		runPassed bool
	}{
		"ends in 30 s":  {30 * time.Second, []bool{false, true}, []bool{false, true}, true},
		"ends in 5 min": {5 * time.Minute, []bool{false}, []bool{false, true}, true},
		"ends in 2 h":   {2 * time.Hour, []bool{false}, []bool{false}, true},
	} {
		res := &fakeResolver{pages: []string{page(time.Now().Add(c.in))}}
		s := newSiteMedia(res)
		p, err := s.Probe(siteContext(t), siteFactory(t), daemon.SiteSelection{PageURL: pageURL})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := res.freshness(); !slices.Equal(got, c.probe) {
			t.Errorf("%s: probe resolutions fresh = %v, want %v", name, got, c.probe)
		}
		res = &fakeResolver{pages: []string{page(time.Now().Add(c.in))}}
		s = newSiteMedia(res)
		sel := daemon.SiteSelection{PageURL: pageURL, Format: "hls", AudioTrack: p.DefaultAudio, AudioKey: p.Tracks[0].Key}
		if _, err := s.Run(siteContext(t), runOptions(t, daemon.MediaRunOptions{}), sel, nil, nil, nil); (err == nil) != c.runPassed {
			t.Fatalf("%s: Run = %v", name, err)
		}
		if got := res.freshness(); !slices.Equal(got, c.runFresh) {
			t.Errorf("%s: run resolutions fresh = %v, want %v", name, got, c.runFresh)
		}
	}
}

// A playlist refused on an audio site says to open a single track of it.
func TestSiteAudioPlaylistText(t *testing.T) {
	list := `{"_type": "playlist", "id": "set1", "extractor_key": "Bandcamp", "title": "Album",
		"entries": [{"_type": "url", "url": "https://artist.example.com/track/a"}]}`
	for pageURL, want := range map[string]string{
		"https://artist.bandcamp.com/album/blue": "open a single track of it",
		"https://soundcloud.com/artist/sets/one": "open a single track of it",
		"https://video.example.com/playlist?x=1": "open a single video of it",
	} {
		s := newSiteMedia(&fakeResolver{pages: []string{list}})
		_, err := s.Probe(siteContext(t), siteFactory(t), daemon.SiteSelection{PageURL: pageURL})
		if !errors.Is(err, media.ErrUnsupported) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: Probe = %v, want %q", pageURL, err, want)
		}
	}
}
