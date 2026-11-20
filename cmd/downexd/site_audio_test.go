package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"downex/internal/daemon"
	"downex/internal/media"
	"downex/internal/model"
)

// Audio pages (docs/SITE-MEDIA-SPEC.md §3.13): a page without video is probed as its
// audio tracks (container m4a or mp3, no video) and run as its one audio file.

// audioPage is yt-dlp's -J output for an audio page with formats.
func audioPage(t *testing.T, formats ...format) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"id": "track0001", "extractor_key": "Bandcamp", "title": "Test track",
		"duration": 16.5, "formats": formats})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSiteAudioMP3(t *testing.T) {
	srv := newFileServer(t)
	mp3 := append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), bytes.Repeat([]byte{0xff, 0xfb, 0x90, 0x64}, 300_000)...)
	srv.add("/a.mp3", mp3)
	page := audioPage(t, format{"format_id": "mp3-128", "protocol": "https", "url": srv.url("/a.mp3"), "ext": "mp3",
		"vcodec": "none", "acodec": "mp3", "abr": 128, "filesize_approx": len(mp3)})
	res := &fakeResolver{pages: []string{page}}
	s := newSiteMedia(res)
	pageURL := srv.url("/track/test")
	p, err := s.Probe(siteContext(t), siteFactory(t), daemon.SiteSelection{PageURL: pageURL})
	if err != nil {
		t.Fatal(err)
	}
	if p.Format != "http" || p.Container != "mp3" || p.DefaultVideo != "" || p.DefaultAudio != "mp3-128" || len(p.Tracks) != 1 ||
		p.Tracks[0].Kind != media.KindAudio || p.Site != "Bandcamp" {
		t.Fatalf("probe = %+v", p)
	}
	opts := runOptions(t, daemon.MediaRunOptions{})
	sel := daemon.SiteSelection{PageURL: pageURL, Format: "http", AudioTrack: "mp3-128", AudioKey: keyOf(t, page, "mp3-128")}
	container, err := s.Run(siteContext(t), opts, sel, nil, nil, nil)
	if err != nil || container != "mp3" {
		t.Fatalf("Run = %q, %v", container, err)
	}
	if got, err := os.ReadFile(opts.OutPath); err != nil || !bytes.Equal(got, mp3) {
		t.Fatalf("output: %d bytes, %v", len(got), err)
	}
	// A part of an audio download is refused before anything is fetched.
	before := len(srv.requests())
	opts = runOptions(t, daemon.MediaRunOptions{})
	sel.Clip = media.Clip{Start: 1e9}
	if _, err := s.Run(siteContext(t), opts, sel, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "videos only") {
		t.Fatalf("Run of a part = %v", err)
	}
	if len(srv.requests()) != before {
		t.Fatal("a refused part fetched the file")
	}
}

// A fragmented AAC file is joined into an MP4 with its one sound track: the site run
// reports m4a.
func TestSiteAudioRemux(t *testing.T) {
	srv := newFileServer(t)
	_, audio := testPair(t)
	srv.add("/a.m4a", audio.file())
	page := audioPage(t, audioFormat("140", srv.url("/a.m4a"), len(audio.file())))
	s := newSiteMedia(&fakeResolver{pages: []string{page}})
	opts := runOptions(t, daemon.MediaRunOptions{})
	sel := daemon.SiteSelection{PageURL: srv.url("/track/test"), Format: "http", AudioTrack: "140", AudioKey: keyOf(t, page, "140")}
	container, err := s.Run(siteContext(t), opts, sel, nil, nil, nil)
	if err != nil || container != "m4a" {
		t.Fatalf("Run = %q, %v", container, err)
	}
	checkOutput(t, opts.OutPath, audio)
}

// An audio page's AAC stream (a media playlist, as SoundCloud serves it) is probed as
// one audio track, container m4a, and run with the track named as recorded (audio).
func TestSiteAudioStream(t *testing.T) {
	srv := newFileServer(t)
	_, audio := testPair(t)
	var pl strings.Builder
	pl.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:1\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-MAP:URI=\"init.mp4\"\n")
	for i, f := range audio.frags {
		srv.add(fmt.Sprintf("/aac/seg%d.m4s", i), f)
		fmt.Fprintf(&pl, "#EXTINF:0.533,\nseg%d.m4s\n", i)
	}
	pl.WriteString("#EXT-X-ENDLIST\n")
	srv.add("/aac/init.mp4", audio.init)
	srv.add("/aac/playlist.m3u8", []byte(pl.String()))
	page := audioPage(t, format{"format_id": "hls_aac_96k", "protocol": "m3u8_native", "url": srv.url("/aac/playlist.m3u8"),
		"ext": "m4a", "container": "m4a_dash", "vcodec": "none", "acodec": "mp4a.40.2", "abr": 96},
		format{"format_id": "hls_opus_64k", "protocol": "m3u8_native", "url": srv.url("/opus/playlist.m3u8"),
			"ext": "opus", "vcodec": "none", "acodec": "opus", "abr": 64})
	s := newSiteMedia(&fakeResolver{pages: []string{page}})
	pageURL := srv.url("/artist/track")
	p, err := s.Probe(siteContext(t), siteFactory(t), daemon.SiteSelection{PageURL: pageURL})
	if err != nil {
		t.Fatal(err)
	}
	if p.Format != "hls" || p.Container != "m4a" || p.DefaultVideo != "" || len(p.Tracks) != 1 || p.Tracks[0].Kind != media.KindAudio ||
		p.DefaultAudio != p.Tracks[0].ID {
		t.Fatalf("probe = %+v", p)
	}
	opts := runOptions(t, daemon.MediaRunOptions{})
	sel := daemon.SiteSelection{PageURL: pageURL, Format: "hls", AudioTrack: p.DefaultAudio, AudioKey: p.Tracks[0].Key}
	var last daemon.MediaProgress
	container, err := s.Run(siteContext(t), opts, sel, nil, func(mp daemon.MediaProgress) { last = mp },
		func(model.MediaCheckpoint) error { return nil })
	if err != nil || container != "m4a" {
		t.Fatalf("Run = %q, %v", container, err)
	}
	checkOutput(t, opts.OutPath, audio)
	if last.FragmentsTotal != len(audio.frags)+1 {
		t.Errorf("progress %+v", last)
	}
	if slices.ContainsFunc(srv.requests(), func(r fileReq) bool { return strings.HasPrefix(r.path, "/opus/") }) {
		t.Error("the Opus stream was fetched")
	}
}
