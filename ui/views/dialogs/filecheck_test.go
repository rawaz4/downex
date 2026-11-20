package dialogs_test

import (
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/state"
	"downex/ui/views"
	"downex/ui/views/dialogs"
)

// disguised gives d the final DANGER file check of a Windows program named like a video
// (docs/DESKTOP-SPEC.md §9).
func disguised(d *downexv1.Download) *downexv1.Download {
	d.FileCheck = &downexv1.FileCheck{
		Level: downexv1.FileCheckLevel_FILE_CHECK_LEVEL_DANGER, Stage: downexv1.FileCheckStage_FILE_CHECK_STAGE_FINAL,
		Reason: "program_disguised", Content: "pe", ContentLabel: "Windows program", ClaimLabel: "MP4 video",
		Summary: "This file is a Windows program, not an MP4 video.",
		Detail:  "Files that pretend to be another type are a common way to spread malware. Don’t rename or run it unless you trust where it came from.",
	}
	return d
}

// TestOpenDialogDisguised: a file that is not what its name says is asked about with its
// own title and the check's texts; "Open anyway", "Run anyway" or "Choose app anyway…";
// Cancel first (DESKTOP-SPEC §9.7). A program that is not disguised keeps "Run this program?".
func TestOpenDialogDisguised(t *testing.T) {
	for _, c := range []struct {
		name, file string
		with       bool
		action     string
	}{
		{"a video that is a program", "movie.mp4", false, "Open anyway"},
		{"Open with…", "movie.mp4", true, "Choose app anyway…"},
		{"a program named twice", "invoice.pdf.exe", false, "Run anyway"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, _ := harness(t, disguised(file(1, c.file, state.StateCompleted)))
			opened := 0
			d := dialogs.NewOpen(h.App, views.OpenRequest{ID: 1, WithApp: c.with}, func() { opened++ })
			show(h, d)
			want := "This file is a Windows program, not an MP4 video.\n\nFiles that pretend to be another type are a common way to spread malware. Don’t rename or run it unless you trust where it came from."
			if d.Card().Semantics.Name != dialogs.DisguisedTitle || d.Card().Semantics.Description != want {
				t.Errorf("title %q description %q", d.Card().Semantics.Name, d.Card().Semantics.Description)
			}
			cancel, submit := d.Buttons()
			if h.Window.Focused() != &cancel.Element || submit.Label().Text() != c.action {
				t.Errorf("focus %v, submit %q", h.Window.Focused(), submit.Label().Text())
			}
			click(h, &submit.Element)
			if opened != 1 {
				t.Errorf("confirmed %d times", opened)
			}
		})
	}
	h, _ := harness(t, file(1, "setup.exe", state.StateCompleted))
	d := dialogs.NewOpen(h.App, views.OpenRequest{ID: 1}, func() {})
	show(h, d)
	if d.Card().Semantics.Name != "Run this program?" {
		t.Errorf("a program that is not disguised: %q", d.Card().Semantics.Name)
	}
}
