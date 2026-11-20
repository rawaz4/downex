package media

import (
	"strings"
	"testing"
	"time"

	imedia "downex/internal/media"
	downexv1 "downex/internal/rpc/downex/v1"
)

// The section's validation texts (B3), for a video of 12:34.
func TestClipFormParse(t *testing.T) {
	const dur = 754_000
	for _, c := range []struct {
		form ClipForm
		clip imedia.Clip
		errs ClipErrors
	}{
		{ClipForm{}, imedia.Clip{}, ClipErrors{}},
		{ClipForm{On: true}, imedia.Clip{}, ClipErrors{}},
		{ClipForm{On: true, From: "1:20", To: "2:45"}, imedia.Clip{Start: 80 * time.Second, End: 165 * time.Second}, ClipErrors{}},
		{ClipForm{On: true, From: " 1:20 "}, imedia.Clip{Start: 80 * time.Second}, ClipErrors{}},
		{ClipForm{On: true, To: "12:34"}, imedia.Clip{}, ClipErrors{}}, // the whole video
		{ClipForm{On: true, From: "1:20", To: "12:35"}, imedia.Clip{Start: 80 * time.Second}, ClipErrors{}},
		{ClipForm{On: true, From: "1:2"}, imedia.Clip{}, ClipErrors{From: ClipBadTime}},
		{ClipForm{On: true, To: "soon"}, imedia.Clip{}, ClipErrors{To: ClipBadTime}},
		{ClipForm{On: true, From: "2:45", To: "1:20"}, imedia.Clip{}, ClipErrors{Range: ClipOrder}},
		{ClipForm{On: true, From: "1:20", To: "1:20.5"}, imedia.Clip{}, ClipErrors{Range: ClipShort}},
		{ClipForm{On: true, From: "1:20", To: "13:00"}, imedia.Clip{}, ClipErrors{Range: "To is after the end of the video (12:34)."}},
		{ClipForm{On: true, From: "12:34"}, imedia.Clip{}, ClipErrors{Range: ClipOrder}},
	} {
		clip, errs := c.form.Parse(dur)
		if clip != c.clip || errs != c.errs {
			t.Errorf("%+v: %+v %+v, want %+v %+v", c.form, clip, errs, c.clip, c.errs)
		}
	}
	// Without a duration, a late To cannot be judged.
	if clip, errs := (ClipForm{On: true, From: "1:00", To: "99:00"}).Parse(0); errs.Any() || clip.End != 99*time.Minute {
		t.Errorf("unknown duration: %+v %+v", clip, errs)
	}
}

func TestChapterOptionsAndFacts(t *testing.T) {
	info := &downexv1.MediaInfo{DurationMs: 754_000, Chapters: []*downexv1.Chapter{
		{StartMs: 0, EndMs: 80_000, Title: "Intro"}, {StartMs: 80_000, EndMs: 754_000}}}
	opts := ChapterOptions(info)
	if len(opts) != 2 || opts[0].Label != "1. Intro (0:00–1:20)" || opts[1].Label != "2. Chapter 2 (1:20–12:34)" ||
		opts[1].From != "1:20" || opts[1].To != "12:34" {
		t.Errorf("options %+v", opts)
	}
	if DurationText(info) != "12:34" {
		t.Error("duration text")
	}
	info.Clip = &downexv1.MediaClip{StartMs: 80_000, EndMs: 165_000}
	if PartFact(info) != "1:20 – 2:45" || PartText(info) != "Part: 1:20 – 2:45" || KeyframeTip(info) != "" {
		t.Errorf("pending: %q %q %q", PartFact(info), PartText(info), KeyframeTip(info))
	}
	info.Clip.KeyframeMs, info.Clip.FetchEndMs = 77_400, 168_000
	if PartText(info) != "Part: 1:20 – 2:45 (keyframe 1:17.4)" ||
		KeyframeTip(info) != "Decoding starts at the keyframe at 1:17.4; players that ignore edit lists start there." {
		t.Errorf("complete: %q %q", PartText(info), KeyframeTip(info))
	}
	info.Clip.KeyframeMs = 83_000
	if PartFact(info) != "1:23 – 2:45 (first keyframe after 1:20)" || KeyframeTip(info) != "" {
		t.Errorf("late: %q", PartFact(info))
	}
	info.Clip.FetchedWhole = true
	if FetchedWholeText(info) != ClipWholeLine {
		t.Error("fetched whole")
	}
	if ClipSummary(imedia.Clip{Start: 80 * time.Second, End: 165 * time.Second}, 754_000) != "part 1:20 – 2:45" {
		t.Error("summary")
	}
}

// B4: the part's size is the size × (To − From + 5 s) ÷ duration, an estimate.
func TestClipSize(t *testing.T) {
	got := ClipSize(PickSize{Bytes: 754_000_000, Kind: SizeExact}, imedia.Clip{Start: 80 * time.Second, End: 165 * time.Second}, 754_000)
	if got.Bytes != 90_000_000 || got.Kind != SizeApprox {
		t.Errorf("%+v", got)
	}
	if got := ClipSize(PickSize{Bytes: 5, Kind: SizeExact}, imedia.Clip{}, 754_000); got.Kind != SizeExact {
		t.Errorf("no part: %+v", got)
	}
}

// The length a part shows (the card's chip, the details pane): the part's of the whole's, read
// with the browser panel's "selected"; the whole's without a part, or for one that is not.
func TestLengthText(t *testing.T) {
	part := imedia.Clip{Start: 618 * time.Second, End: 1126 * time.Second} // 10:18 – 18:46
	for _, c := range []struct {
		clip        imedia.Clip
		dur         int64
		shown, name string
	}{
		{imedia.Clip{}, 1_914_000, "31:54", "31:54"},
		{part, 1_914_000, "8:28 of 31:54", "8:28 selected of 31:54"},
		{imedia.Clip{Start: 618 * time.Second}, 1_914_000, "21:36 of 31:54", "21:36 selected of 31:54"}, // to the end
		{part, 0, "8:28 selected", "8:28 selected"},
		// The part's span keeps tenths like the browser panel's "N selected" and From/To
		// (FormatClipTime); the whole's length stays in whole seconds.
		{imedia.Clip{Start: 618500 * time.Millisecond, End: 1126 * time.Second}, 1_914_000, "8:27.5 of 31:54", "8:27.5 selected of 31:54"},
		{imedia.Clip{Start: 618 * time.Second}, 1_914_381, "21:36.3 of 31:54", "21:36.3 selected of 31:54"},
		{imedia.Clip{Start: 10 * time.Second, End: 11400 * time.Millisecond}, 1_914_000, "0:01.4 of 31:54", "0:01.4 selected of 31:54"},
		{imedia.Clip{Start: 618 * time.Second, End: 4726500 * time.Millisecond}, 9_114_000, "1:08:28.5 of 2:31:54", "1:08:28.5 selected of 2:31:54"},
		{imedia.Clip{Start: 618 * time.Second}, 0, "", ""},
		{imedia.Clip{}, 0, "", ""},
	} {
		shown, name := LengthText(c.clip, c.dur)
		if shown != c.shown || name != c.name {
			t.Errorf("%+v %d: %q %q, want %q %q", c.clip, c.dur, shown, name, c.shown, c.name)
		}
	}
}

// A track's size for a part is the part's estimate (ClipSize, "~"); the chosen tracks of a
// download that saves a part say so.
func TestClipTrackSizes(t *testing.T) {
	part := imedia.Clip{Start: 80 * time.Second, End: 165 * time.Second}
	tr := &downexv1.MediaTrack{Id: "v", Kind: "video", Size: 754_000_000}
	if got := ClipTrackSizeText(tr, imedia.Clip{}, 754_000); got != TrackSizeText(tr) {
		t.Errorf("whole %q", got)
	}
	if got, want := ClipTrackSizeText(tr, part, 754_000), PickSizeText(PickSize{Bytes: 90_000_000, Kind: SizeApprox}); got != want || got[0] != '~' {
		t.Errorf("part %q, want %q", got, want)
	}
	if got := ClipTrackSizeText(&downexv1.MediaTrack{}, part, 754_000); got != "" {
		t.Errorf("unknown %q", got)
	}
	info := &downexv1.MediaInfo{
		Origin: downexv1.MediaOrigin_MEDIA_ORIGIN_SITE, Format: "http", DurationMs: 754_000, SelectedVideo: "v", SelectedAudio: "a",
		Tracks: []*downexv1.MediaTrack{tr, {Id: "a", Kind: "audio", Size: 7_540_000, Codecs: "mp4a.40.2"}},
	}
	wholeV, wholeA := ChosenTracks(info)
	info.Clip = &downexv1.MediaClip{StartMs: 80_000, EndMs: 165_000}
	v, a := ChosenTracks(info)
	if v == wholeV || a == wholeA || !strings.Contains(v, PickSizeText(PickSize{Bytes: 90_000_000, Kind: SizeApprox})) || !strings.Contains(a, "~") {
		t.Errorf("chosen %q %q (whole %q %q)", v, a, wholeV, wholeA)
	}
}
