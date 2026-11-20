package daemon

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"downex/internal/media"
	downexv1 "downex/internal/rpc/downex/v1"
)

// Audio pages (docs/SITE-MEDIA-SPEC.md §3.13): a page without video is recorded as its
// audio (container m4a or mp3, no video), named with its extension, filed under Audio,
// run with the audio track only and never as a part.
func TestSiteAddAudioPage(t *testing.T) {
	fs := newFakeSite(2)
	fs.set(func(f *fakeSite) {
		f.probe = SiteProbe{MediaProbe: MediaProbe{Format: "http", Container: "mp3", DefaultAudio: "mp3-128", Duration: 348 * time.Second,
			Tracks: []media.Track{{ID: "mp3-128", Kind: media.KindAudio, Bandwidth: 128_000, Codecs: "mp3", Key: "s1-mp3", Size: 5_573_712,
				SizeApprox: true, Ext: "mp3"}}},
			Site: "Bandcamp", Title: "Artist - Song"}
		f.job.container = "mp3"
	})
	h := newSiteHarness(t, harnessOptions{}, siteHarnessOptions{site: fs, tools: newFakeTools()})
	d := h.addMedia(&downexv1.AddMediaRequest{Origin: originSite, PageUrl: testPage, Source: sourceBrowser})
	mi := d.GetMedia()
	if d.GetFilename() != "Artist - Song.mp3" || d.GetCategory() != "Audio" || d.GetExpectedSize() != 5_573_712 ||
		mi.GetContainer() != "mp3" || mi.GetSelectedVideo() != "" || mi.GetSelectedAudio() != "mp3-128" {
		t.Fatalf("added = %v", d)
	}
	// A video or a part of an audio page is refused.
	if _, err := h.confirmReq(&downexv1.ConfirmRequest{Id: d.GetId(), VideoTrack: "mp3-128"}); mediaCode(t, err) != CodeMediaInvalid {
		t.Errorf("confirm with a video: %v", err)
	}
	if _, err := h.confirmReq(&downexv1.ConfirmRequest{Id: d.GetId(), Clip: &downexv1.MediaClip{StartMs: 1_000}}); mediaCode(t, err) != CodeClipInvalid {
		t.Errorf("confirm with a part: %v", err)
	}
	if _, err := h.addMediaReq(&downexv1.AddMediaRequest{Origin: originSite, PageUrl: testPage, Clip: &downexv1.MediaClip{StartMs: 1_000}}); mediaCode(t, err) != CodeClipInvalid {
		t.Errorf("add with a part: %v", err)
	}
	c, err := h.confirmReq(&downexv1.ConfirmRequest{Id: d.GetId(), Start: true, AudioTrack: "mp3-128"})
	if err != nil {
		t.Fatal(err)
	}
	if c.GetFilename() != "Artist - Song.mp3" || c.GetMedia().GetContainer() != "mp3" {
		t.Fatalf("confirmed = %v", c)
	}
	done := h.waitCompleted(d.GetId())
	if done.GetMedia().GetContainer() != "mp3" || filepath.Ext(done.GetFilename()) != ".mp3" {
		t.Fatalf("completed = %v", done)
	}
	_, runs := fs.calls()
	if len(runs) != 1 || runs[0].sel.VideoTrack != "" || runs[0].sel.AudioTrack != "mp3-128" || runs[0].sel.AudioKey != "s1-mp3" ||
		filepath.Ext(runs[0].opts.OutPath) != ".mp3" {
		t.Fatalf("runs = %+v", runs)
	}
}

// A run that returns another container than the audio page's recorded one fails, as an
// MP4 download does.
func TestSiteAudioRunOtherContainer(t *testing.T) {
	fs := newFakeSite(1)
	fs.set(func(f *fakeSite) {
		f.probe = SiteProbe{MediaProbe: MediaProbe{Format: "hls", Container: "m4a", DefaultAudio: "v0",
			Tracks: []media.Track{{ID: "v0", Kind: media.KindAudio}}}, Site: "SoundCloud", Title: "Track"}
		f.job.container = "mp4"
	})
	h := newSiteHarness(t, harnessOptions{}, siteHarnessOptions{site: fs, tools: newFakeTools()})
	d := h.addMedia(&downexv1.AddMediaRequest{Origin: originSite, PageUrl: testPage, Source: downexv1.AddSource_ADD_SOURCE_CLI})
	if d.GetFilename() != "Track.m4a" || d.GetMedia().GetContainer() != "m4a" {
		t.Fatalf("added = %v", d)
	}
	if f := h.waitState(d.GetId(), downexv1.DownloadState_DOWNLOAD_STATE_FAILED); !strings.Contains(f.GetError(), "mp4") {
		t.Fatalf("failed = %v", f)
	}
}

// An audio answer that names a video, or lists a video track, is not recorded.
func TestMediaFromSiteProbeAudio(t *testing.T) {
	audio := media.Track{ID: "a", Kind: media.KindAudio, Ext: "m4a"}
	video := media.Track{ID: "v", Kind: media.KindVideo, Ext: "mp4"}
	for name, c := range map[string]struct {
		p  SiteProbe
		ok bool
	}{
		"audio":          {SiteProbe{MediaProbe: MediaProbe{Format: "http", Container: "m4a", DefaultAudio: "a", Tracks: []media.Track{audio}}}, true},
		"named video":    {SiteProbe{MediaProbe: MediaProbe{Format: "http", Container: "m4a", DefaultVideo: "a", DefaultAudio: "a", Tracks: []media.Track{audio}}}, false},
		"a video track":  {SiteProbe{MediaProbe: MediaProbe{Format: "http", Container: "m4a", DefaultAudio: "a", Tracks: []media.Track{video, audio}}}, false},
		"no audio":       {SiteProbe{MediaProbe: MediaProbe{Format: "http", Container: "mp3", Tracks: []media.Track{audio}}}, false},
		"other type":     {SiteProbe{MediaProbe: MediaProbe{Format: "http", Container: "ogg", DefaultAudio: "a", Tracks: []media.Track{audio}}}, false},
		"mp4 with audio": {SiteProbe{MediaProbe: MediaProbe{Format: "http", Container: "mp4", DefaultAudio: "a", Tracks: []media.Track{audio}}}, false},
	} {
		med, err := mediaFromSiteProbe(&c.p, testPage)
		if (err == nil) != c.ok {
			t.Errorf("%s: %+v, %v", name, med, err)
		}
	}
}
