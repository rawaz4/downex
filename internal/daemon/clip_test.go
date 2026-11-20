package daemon

import (
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"downex/internal/media"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
)

// A part of a stream (docs/MEDIA-SPEC.md §8): AddMedia checks it against the duration,
// stores it, names the file by its times, passes it to the job and completes it with
// what the job reports (C20); Download again keeps the part and forgets that report.
func TestAddMediaClip(t *testing.T) {
	fm := newFakeMedia(3) // 90 s
	fm.clip = &model.MediaClip{KeyframeMs: 77_400, FetchStartMs: 76_000, FetchEndMs: 88_000}
	h := newHarness(t, harnessOptions{media: fm})
	d := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest, Title: "Talk",
		Clip: &downexv1.MediaClip{StartMs: 80_000, EndMs: 85_000, Chapter: "Results\n"}})
	if d.GetFilename() != "Talk (01m20s-01m25s).mp4" {
		t.Errorf("name %q", d.GetFilename())
	}
	if c := d.GetMedia().GetClip(); c.GetStartMs() != 80_000 || c.GetEndMs() != 85_000 || c.GetChapter() != "Results" || c.GetKeyframeMs() != 0 {
		t.Errorf("clip %v", c)
	}
	done := h.waitCompleted(d.GetId())
	if c := done.GetMedia().GetClip(); c.GetKeyframeMs() != 77_400 || c.GetFetchStartMs() != 76_000 || c.GetFetchEndMs() != 88_000 ||
		c.GetStartMs() != 80_000 || c.GetChapter() != "Results" {
		t.Errorf("completed clip %v", c)
	}
	_, runs := fm.calls()
	if len(runs) != 1 || runs[0].sel.Clip != (media.Clip{Start: 80 * time.Second, End: 85 * time.Second}) {
		t.Fatalf("runs %+v", runs)
	}
	// Download again: the part stays, the last run's report goes.
	if _, err := h.dl.Resume(ctxT(t), connect.NewRequest(&downexv1.ResumeRequest{Id: d.GetId(), Restart: true})); err != nil {
		t.Fatal(err)
	}
	rec := storedState(t, h.st, d.GetId())
	if rec.Media.Clip == nil || rec.Media.Clip.StartMs != 80_000 {
		t.Errorf("restart lost the part: %+v", rec.Media.Clip)
	}
	h.waitCompleted(d.GetId())
	if _, runs := fm.calls(); len(runs) != 2 || runs[1].sel.Clip.Start != 80*time.Second {
		t.Errorf("the restarted run had no part: %+v", runs)
	}

	// A part to the end, and one that covers the whole video: no part.
	d2 := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest, Title: "Talk", StartPaused: true,
		Clip: &downexv1.MediaClip{StartMs: 70_000, EndMs: 90_500}})
	if d2.GetFilename() != "Talk (01m10s-end).mp4" || d2.GetMedia().GetClip().GetEndMs() != 0 {
		t.Errorf("open end: %q %v", d2.GetFilename(), d2.GetMedia().GetClip())
	}
	d3 := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest, Title: "Whole", StartPaused: true,
		Clip: &downexv1.MediaClip{StartMs: 0, EndMs: 90_000}})
	if d3.GetFilename() != "Whole.mp4" || d3.GetMedia().GetClip() != nil {
		t.Errorf("whole video: %q %v", d3.GetFilename(), d3.GetMedia().GetClip())
	}
	// A name the client typed is used as typed.
	d4 := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest, Filename: "mine", StartPaused: true,
		Clip: &downexv1.MediaClip{StartMs: 1_000, EndMs: 5_000}})
	if d4.GetFilename() != "mine.mp4" {
		t.Errorf("typed name: %q", d4.GetFilename())
	}
}

// Refused parts are InvalidArgument with code clip_invalid and the reasons of C2.
func TestAddMediaClipRefusals(t *testing.T) {
	fm := newFakeMedia(3) // 90 s = 1:30
	h := newHarness(t, harnessOptions{media: fm})
	for _, c := range []struct {
		clip   *downexv1.MediaClip
		reason string
	}{
		{&downexv1.MediaClip{StartMs: 5_000, EndMs: 5_000}, "the part must start before it ends"},
		{&downexv1.MediaClip{StartMs: 5_000, EndMs: 5_500}, "the part must be at least 1 second long"},
		{&downexv1.MediaClip{StartMs: -1}, "times must be between 0 and 168 hours"},
		{&downexv1.MediaClip{StartMs: 5_000, EndMs: 92_000}, "the part ends after the video (1:30)"},
		{&downexv1.MediaClip{StartMs: 95_000}, "the part must start before it ends"},
	} {
		_, err := h.dl.AddMedia(ctxT(t), connect.NewRequest(&downexv1.AddMediaRequest{ManifestUrl: testManifest, Clip: c.clip}))
		if connectCode(err) != connect.CodeInvalidArgument || mediaCode(t, err) != CodeClipInvalid || !strings.HasSuffix(err.Error(), c.reason) {
			t.Errorf("%v: %v, want %q", c.clip, err, c.reason)
		}
	}
	if n := len(h.list()); n != 0 {
		t.Errorf("%d downloads added by refused parts", n)
	}
}

// Confirm sets, changes or removes the part (C1: set with 0/0 = the whole video), checks
// it, and the proposed name follows it; a file download refuses one.
func TestConfirmClip(t *testing.T) {
	fm := newFakeMedia(3)
	h := newHarness(t, harnessOptions{media: fm})
	d := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest, Source: downexv1.AddSource_ADD_SOURCE_BROWSER, Title: "Show",
		Clip: &downexv1.MediaClip{StartMs: 10_000, EndMs: 20_000}})
	if d.GetState() != stAwaiting || d.GetFilename() != "Show (00m10s-00m20s).mp4" {
		t.Fatalf("added %v", d)
	}
	if _, err := h.confirmReq(&downexv1.ConfirmRequest{Id: d.GetId(), Clip: &downexv1.MediaClip{StartMs: 10_000, EndMs: 100_000}}); mediaCode(t, err) != CodeClipInvalid {
		t.Errorf("past the end: %v", err)
	}
	c, err := h.confirmReq(&downexv1.ConfirmRequest{Id: d.GetId(), Clip: &downexv1.MediaClip{StartMs: 0, EndMs: 0}})
	if err != nil {
		t.Fatal(err)
	}
	if c.GetFilename() != "Show.mp4" || c.GetMedia().GetClip() != nil {
		t.Errorf("whole video: %q %v", c.GetFilename(), c.GetMedia().GetClip())
	}

	d2 := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest, Source: downexv1.AddSource_ADD_SOURCE_BROWSER, Title: "Show 2"})
	c2, err := h.confirmReq(&downexv1.ConfirmRequest{Id: d2.GetId(), Clip: &downexv1.MediaClip{StartMs: 61_000, Chapter: "End"}})
	if err != nil {
		t.Fatal(err)
	}
	if c2.GetFilename() != "Show 2 (01m01s-end).mp4" || c2.GetMedia().GetClip().GetChapter() != "End" {
		t.Errorf("confirmed %q %v", c2.GetFilename(), c2.GetMedia().GetClip())
	}
	// Without a clip the part is kept.
	d3 := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest, Source: downexv1.AddSource_ADD_SOURCE_BROWSER, Title: "Show 3",
		Clip: &downexv1.MediaClip{StartMs: 1_000, EndMs: 9_000}})
	c3, err := h.confirmReq(&downexv1.ConfirmRequest{Id: d3.GetId(), Filename: "typed"})
	if err != nil {
		t.Fatal(err)
	}
	if c3.GetFilename() != "typed.mp4" || c3.GetMedia().GetClip().GetEndMs() != 9_000 {
		t.Errorf("kept: %q %v", c3.GetFilename(), c3.GetMedia().GetClip())
	}

	// A file download refuses a part.
	f := h.add(&downexv1.AddRequest{Url: "https://files.example.test/a.zip", Source: downexv1.AddSource_ADD_SOURCE_BROWSER})
	if f.GetState() == stAwaiting {
		if _, err := h.confirmReq(&downexv1.ConfirmRequest{Id: f.GetId(), Clip: &downexv1.MediaClip{StartMs: 1_000}}); mediaCode(t, err) != CodeClipInvalid {
			t.Errorf("file download: %v", err)
		}
	}
}

// Chapters of a site video are checked (C14) and rendered.
func TestMediaChapters(t *testing.T) {
	med := &model.Media{Origin: "site", Format: "http", Duration: 100 * time.Second,
		Chapters: checkChapters([]model.Chapter{{StartMs: 50_000, EndMs: 100_000, Title: "B"}, {StartMs: 0, EndMs: 50_000, Title: "A\u0007"},
			{StartMs: 40_000, EndMs: 60_000}, {StartMs: 90_000, EndMs: 120_000}}, 100*time.Second)}
	pm := protoMedia("https://site.example.test/watch", med)
	if len(pm.GetChapters()) != 2 || pm.GetChapters()[0].GetTitle() != "A" || pm.GetChapters()[1].GetStartMs() != 50_000 {
		t.Errorf("chapters %v", pm.GetChapters())
	}
	if pm.GetClip() != nil {
		t.Error("a clip without a part")
	}
}

// The name of a part fits the file-name limit, its suffix intact.
func TestClipFileNameLimit(t *testing.T) {
	long := strings.Repeat("ü", 200) + ".mp4"
	got := clipFileName(long, media.Clip{Start: time.Hour, End: 2 * time.Hour})
	if !strings.HasSuffix(got, " (1h00m00s-2h00m00s).mp4") || len(got) > 255-12 {
		t.Errorf("%q (%d bytes)", got, len(got))
	}
	if got := clipFileName("a (00m01s-00m02s).mp4", media.Clip{Start: 3 * time.Second, End: 4 * time.Second}); got != "a (00m03s-00m04s).mp4" {
		t.Errorf("replaced suffix: %q", got)
	}
}
