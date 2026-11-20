package add

import (
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
)

// TestConfirmDialogNameWarning: the NAME stage of the file check under File name
// (docs/DESKTOP-SPEC.md §9.7): the daemon's result for the proposed name, the name as it is
// edited after that, DANGER only; the buttons and the default button stay.
func TestConfirmDialogNameWarning(t *testing.T) {
	d := fileDownload(func(d *downexv1.Download) {
		d.Filename, d.Category, d.Mime = "invoice.pdf.exe", "Programs", "application/pdf"
		d.FileCheck = &downexv1.FileCheck{Level: downexv1.FileCheckLevel_FILE_CHECK_LEVEL_DANGER,
			Stage: downexv1.FileCheckStage_FILE_CHECK_STAGE_NAME, Reason: "double_extension",
			Summary: "“invoice.pdf.exe” is a Windows program whose name imitates a PDF document."}
	})
	r := setupConfirm(t, confirmSetup{d: d})
	shown := func() bool { return !r.c.nameCheck.Style.Hidden }
	if !shown() || r.c.nameCheck.Heading.Text() != "This file is not what its name says" ||
		r.c.nameCheck.Summary.Text() != "“invoice.pdf.exe” is a Windows program whose name imitates a PDF document." {
		t.Fatalf("proposed name: shown %v, %q", shown(), r.c.nameCheck.Summary.Text())
	}
	typeIn(t, r.h, r.c.Name, "invoice.pdf")
	if shown() {
		t.Fatal("a plain name still warns")
	}
	typeIn(t, r.h, r.c.Name, "photo.jpg.scr")
	if !shown() || r.c.NameWarning() != "“photo.jpg.scr” is a Windows program whose name imitates a JPEG image." {
		t.Fatalf("edited name: shown %v, %q", shown(), r.c.NameWarning())
	}
	if r.c.start.Disabled || r.c.start.Label().Text() != "Start download" {
		t.Error("the warning changed the buttons")
	}
	// The served (hinted) type of a program name: served_as_other.
	typeIn(t, r.h, r.c.Name, "report.exe")
	if r.c.NameWarning() != "The server said this was a PDF document (application/pdf), but it is a Windows program." {
		t.Fatalf("hint: %q", r.c.NameWarning())
	}
}
