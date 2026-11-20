package add

import (
	"context"
	"strings"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format/formattest"
)

// Audio pages (docs/SITE-MEDIA-SPEC.md §3.13, docs/DESKTOP-SPEC.md §1): Add → Video and
// Confirm download list the audio, propose a name with its extension and send the audio
// track alone, without a Part section.

// audioInfo is an audio page's answer: two AAC tracks, saved as .m4a (or one MP3).
func audioInfo(page string, mp3 bool) *downexv1.MediaInfo {
	return formattest.SiteMediaInfo(func(m *downexv1.MediaInfo) {
		m.PageUrl = page
		m.Site = "SoundCloud"
		m.Title = "A track"
		m.Container = "m4a"
		m.SelectedVideo, m.SelectedAudio = "", "hls_aac_160"
		m.Chapters = nil
		m.Tracks = []*downexv1.MediaTrack{
			{Id: "hls_aac_160", Kind: "audio", Bandwidth: 160_000, Codecs: "mp4a.40.2", Size: 3_000_000, SizeApprox: true, Ext: "m4a"},
			{Id: "hls_aac_96", Kind: "audio", Bandwidth: 96_000, Codecs: "mp4a.40.2", Size: 1_700_000, SizeApprox: true, Ext: "m4a"},
		}
		if mp3 {
			m.Site, m.Container, m.SelectedAudio = "Bandcamp", "mp3", "mp3-128"
			m.Tracks = []*downexv1.MediaTrack{{Id: "mp3-128", Kind: "audio", Bandwidth: 128_000, Codecs: "mp3", Size: 5_573_712, SizeApprox: true, Ext: "mp3"}}
		}
	})
}

func TestAddDialogAudioPage(t *testing.T) {
	const page = "https://soundcloud.example/artist/track"
	probe := func(_ context.Context, req *downexv1.ProbeMediaRequest) (*downexv1.MediaInfo, error) {
		return audioInfo(req.GetPageUrl(), false), nil
	}
	r := setupAdd(t, rigOptions{probe: probe, submitMedia: siteAdd})
	r.enterPage(t, page)
	r.primary()
	r.settle(t, "audio", func() bool { return r.a.Info() != nil })
	if r.a.Clip != nil {
		t.Error("a Part section for audio")
	}
	if r.a.Picker.QualitySection() != nil {
		t.Error("quality cards for audio")
	}
	text := dialogTextOf(r.a.qualityArea)
	if !strings.Contains(text, "This page has no video: its audio is saved as .m4a.") {
		t.Errorf("qualities' column: %q", text)
	}
	audio := r.audio()
	if audio == nil || audio.Mode() != "select" || audio.Select.Value() != "hls_aac_160" {
		t.Fatalf("audio part %+v", audio)
	}
	eq(t, "name", r.a.streamFileName(), "A track.m4a")
	eq(t, "button", r.a.SubmitLabel(), "Add audio")
	if card := dialogTextOf(&r.a.Video.Element); !strings.Contains(card, "M4A") {
		t.Errorf("video card: %q", card)
	}
	choose(t, r.h, audio.Select, "hls_aac_96")
	r.primary()
	r.settle(t, "added", func() bool { return r.media.n() > 0 })
	got := r.media.last()
	eq(t, "video", got.GetVideoTrack(), "")
	eq(t, "audio", got.GetAudioTrack(), "hls_aac_96")
	if got.GetClip() != nil {
		t.Errorf("a part was sent: %v", got.GetClip())
	}
}

func TestConfirmDialogAudioPage(t *testing.T) {
	d := siteConfirm(func(d *downexv1.Download) {
		d.Media = audioInfo(formattest.Page, true)
		d.Filename = "Artist - Song.mp3"
		d.Category = "Audio"
	})
	r := setupConfirm(t, confirmSetup{d: d})
	if r.c.Clip != nil || r.c.qualities {
		t.Errorf("part %v, qualities column %v", r.c.Clip != nil, r.c.qualities)
	}
	if text := dialogTextOf(r.c.fields); !strings.Contains(text, "This page has no video: its audio is saved as .mp3.") {
		t.Errorf("fields: %q", text)
	}
	if a := r.audioSelect(); a == nil || a.Mode() != "static" || !strings.Contains(a.StaticText(), "MP3") {
		t.Fatalf("audio part %+v", a)
	}
	if hint := r.c.nameHint.Text(); !strings.HasSuffix(hint, " · saved as MP3") {
		t.Errorf("name hint %q", hint)
	}
	if desc := r.c.description(); !strings.HasPrefix(desc, "Audio from the browser is waiting.") {
		t.Errorf("description %q", desc)
	}
	typeIn(t, r.h, r.c.Name, "song")
	r.start()
	r.waitConfirm(t)
	got := r.confirmed()
	eq(t, "video", got.GetVideoTrack(), "")
	eq(t, "filename", got.GetFilename(), "song.mp3")
}
