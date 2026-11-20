package progress

import (
	"slices"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/state"
	"downex/ui/views"
)

// The file check in the progress window (docs/DESKTOP-SPEC.md §9.7): the early note on the
// Status tab, the final note and Contents in the complete view, and Open held for DANGER.

func fileCheck(level downexv1.FileCheckLevel, stage downexv1.FileCheckStage, detail string) *downexv1.FileCheck {
	return &downexv1.FileCheck{Level: level, Stage: stage, Reason: "program_disguised",
		ContentLabel: "Windows program", ClaimLabel: "MP4 video",
		Summary: "This file is a Windows program, not an MP4 video.", Detail: detail}
}

const dangerDetail = "Files that pretend to be another type are a common way to spread malware. Don’t rename or run it unless you trust where it came from."

func TestProgressWindowFileCheck(t *testing.T) {
	t.Run("the Status tab shows the early note while it runs", func(t *testing.T) {
		r := show(t, running(func(d *downexv1.Download) {
			d.Filename = "movie.mp4"
			d.FileCheck = fileCheck(downexv1.FileCheckLevel_FILE_CHECK_LEVEL_DANGER, downexv1.FileCheckStage_FILE_CHECK_STAGE_EARLY,
				dangerDetail+" Downex checks it again when the download completes.")
		}))
		n := r.s().statusV.check
		if !visible(&n.Element) || n.Tone != views.NoteFail || n.Heading.Text() != "This file is not what its name says" ||
			n.Detail.Text() != dangerDetail+" Downex checks it again when the download completes." {
			t.Fatalf("early note shown %v: %q / %q", visible(&n.Element), n.Heading.Text(), n.Detail.Text())
		}
		// Pause and Cancel are unchanged.
		if r.s().actions.pause.Style.Hidden || r.s().actions.cancel.Style.Hidden {
			t.Error("Pause or Cancel hidden")
		}
		r.update(7, func(d *downexv1.Download) { d.FileCheck = nil })
		if visible(&n.Element) {
			t.Error("the note stayed without a check")
		}
	})

	t.Run("the complete view shows the final note and Contents; Open asks first", func(t *testing.T) {
		r := show(t, completed(func(d *downexv1.Download) {
			d.Filename = "movie.mp4"
			d.FileCheck = fileCheck(downexv1.FileCheckLevel_FILE_CHECK_LEVEL_DANGER, downexv1.FileCheckStage_FILE_CHECK_STAGE_FINAL, dangerDetail)
		}))
		if !r.completeShown() {
			t.Fatal("no complete view")
		}
		n := r.s().done.check
		if !visible(&n.Element) || n.Summary.Text() != "This file is a Windows program, not an MP4 video." {
			t.Fatalf("final note shown %v", visible(&n.Element))
		}
		if got, _ := r.row("Contents"); got != "Windows program · named as MP4 video" {
			t.Errorf("Contents %q", got)
		}
		r.click(r.s().done.open)
		want := "This file is a Windows program, not an MP4 video.\n\n" + dangerDetail
		if len(r.prompts) != 1 || r.prompts[0].Disguise != want || r.prompts[0].Executable || r.prompts[0].WithApp {
			t.Fatalf("prompts %+v", r.prompts)
		}
		if slices.Contains(r.api.Calls(), "openFile") {
			t.Fatal("opened without asking")
		}
	})

	t.Run("Open with… asks first too", func(t *testing.T) {
		r := show(t, done(func(d *downexv1.Download) {
			d.Filename = "movie.mp4"
			d.FileCheck = fileCheck(downexv1.FileCheckLevel_FILE_CHECK_LEVEL_DANGER, downexv1.FileCheckStage_FILE_CHECK_STAGE_FINAL, dangerDetail)
		}))
		r.click(r.s().done.openWith)
		if len(r.prompts) != 1 || !r.prompts[0].WithApp || r.prompts[0].Disguise == "" {
			t.Fatalf("prompts %+v", r.prompts)
		}
	})

	t.Run("CAUTION shows its note and opens at once", func(t *testing.T) {
		r := show(t, completed(func(d *downexv1.Download) {
			d.Filename = "video.mp4"
			d.FileCheck = fileCheck(downexv1.FileCheckLevel_FILE_CHECK_LEVEL_CAUTION, downexv1.FileCheckStage_FILE_CHECK_STAGE_FINAL, "")
		}))
		n := r.s().done.check
		if !visible(&n.Element) || n.Tone != views.NoteWarn || visible(&n.Detail.Element) {
			t.Fatalf("caution note shown %v tone %v", visible(&n.Element), n.Tone)
		}
		r.click(r.s().done.open)
		if len(r.prompts) != 0 || !slices.Contains(r.api.Calls(), "openFile") {
			t.Fatalf("prompts %v calls %v", r.prompts, r.api.Calls())
		}
	})

	t.Run("the window grows to show the note without scrolling, also at 20 px text", func(t *testing.T) {
		for _, font := range []int{16, 20} {
			r := newRig(t, completed(func(d *downexv1.Download) {
				d.Filename = "movie.mp4"
				d.FileCheck = fileCheck(downexv1.FileCheckLevel_FILE_CHECK_LEVEL_DANGER, downexv1.FileCheckStage_FILE_CHECK_STAGE_FINAL, dangerDetail)
			}), nil, rigOptions{font: font})
			if r.s().done.Overflows() {
				t.Errorf("%d px: the complete view scrolls", font)
			}
		}
	})

	t.Run("a completed download without a check shows neither", func(t *testing.T) {
		r := show(t, completed(func(d *downexv1.Download) { d.State = state.StateCompleted }))
		if visible(&r.s().done.check.Element) {
			t.Error("note")
		}
		if _, ok := r.row("Contents"); ok {
			t.Error("Contents")
		}
	})
}
