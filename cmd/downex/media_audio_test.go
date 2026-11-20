package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
)

// audioSiteProbe is an audio page's answer (docs/SITE-MEDIA-SPEC.md §3.13).
func audioSiteProbe() *downexv1.MediaInfo {
	return &downexv1.MediaInfo{
		Origin: downexv1.MediaOrigin_MEDIA_ORIGIN_SITE, PageUrl: testPage, Site: "Bandcamp", Title: "Artist - Song",
		Format: "http", Container: "mp3", DurationMs: 348_000, SelectedAudio: "mp3-128",
		Tracks: []*downexv1.MediaTrack{
			{Id: "mp3-128", Kind: "audio", Bandwidth: 128_000, Codecs: "mp3", Size: 5_573_712, SizeApprox: true, Ext: "mp3"},
		},
	}
}

// TestMediaSiteAudioPage: "--list" of an audio page lists its audio and says what it is
// saved as; an add names it as audio.
func TestMediaSiteAudioPage(t *testing.T) {
	f, h := newFakeDaemon(t)
	f.do(func() { f.probe = audioSiteProbe() })
	r := h.run(context.Background(), "media", "--site", testPage, "--list")
	if r.code != exitOK {
		t.Fatalf("media --site --list: %v", r)
	}
	for _, want := range []string{"Bandcamp audio: Artist - Song", "Saved as: .mp3", "* mp3-128", "MP3",
		"Audio only: this page has no video; saved as .mp3.", "choose another with --audio ID."} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("listing lacks %q:\n%s", want, r.stdout)
		}
	}
	if strings.Contains(r.stdout, "no sound") || strings.Contains(r.stdout, "--video") {
		t.Errorf("listing:\n%s", r.stdout)
	}
	if got := describeSelection(audioSiteProbe()); got != "Bandcamp audio: audio mp3-128" {
		t.Errorf("describeSelection = %q", got)
	}
}

// TestMediaSiteAudioTexts: an audio page's items are tracks, its refusals say why (a raw AAC
// stream, a preview, a playlist of tracks), and "info" names a saved audio page as one
// (docs/SITE-MEDIA-SPEC.md §3.12, §3.13).
func TestMediaSiteAudioTexts(t *testing.T) {
	f, h := newFakeDaemon(t)
	f.do(func() {
		p := audioSiteProbe()
		p.EntryId, p.ItemsLeftOut = "t1", 3
		p.Items = []*downexv1.MediaItem{{Id: "t1", Title: "Turning"}, {Id: "t2", Title: "Other"}}
		f.probe = p
	})
	r := h.run(context.Background(), "media", "--site", testPage, "--list")
	for _, want := range []string{"Tracks on this page: 2 (3 other items not listed: links or audio Downex cannot save)",
		"* = the track listed above; choose another with --item ID."} {
		if !strings.Contains(r.stdout, want+"\n") {
			t.Errorf("listing lacks %q:\n%s", want, r.stdout)
		}
	}
	for reason, want := range map[string]string{
		"media: unsupported operation: the audio is a raw AAC stream, which Downex cannot save without converting it": "media: the audio is a raw AAC stream, which Downex cannot save without converting it",
		"media: unsupported operation: only a preview of this track is public, not the whole track":                   "media: only a preview of this track is public, not the whole track",
		"media: unsupported operation: the page is a playlist; open a single track of it":                             "media: the page is a playlist; open a single track of it",
		"media: unsupported operation: no MP4 video on this page":                                                     "media: no MP4 video on this page (or a playlist: open a single video of it)",
	} {
		f.do(func() { f.mediaErr = siteRefusal("media_unsupported", connect.CodeInvalidArgument, reason) })
		r := h.run(context.Background(), "media", "--site", testPage, "--list")
		if r.code != exitError || !strings.Contains(r.stderr, want) {
			t.Errorf("%q: %v", reason, r)
		}
	}

	db := filepath.Join(t.TempDir(), "downex.db")
	st, _, err := openStore(db)
	if err != nil {
		t.Fatal(err)
	}
	d := &model.Download{Request: model.Request{URL: testPage}, Dir: t.TempDir(), Filename: "Song.m4a", Size: -1,
		State: model.StatePaused, Kind: model.KindMedia, Media: &model.Media{Origin: "site", Site: "SoundCloud", Format: "hls",
			Container: "m4a", SelectedAudio: "v0", Tracks: []model.MediaTrack{{ID: "v0", Kind: "audio", Ext: "m4a"}}}}
	if err := st.CreateDownload(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if r := runWith(context.Background(), offlineSystem(), "info", "1", "--db", db); !strings.Contains(r.stdout, "Kind: ") ||
		!strings.Contains(r.stdout, "audio page (SoundCloud)") {
		t.Errorf("info:\n%s", r.stdout)
	}
}
