package main

import (
	"testing"

	"downex/internal/daemon"
	"downex/internal/model"
)

// TestFileCheckBalloon: the notification of a final file check (docs/DESKTOP-SPEC.md
// §9.7) in the error style for DANGER only.
func TestFileCheckBalloon(t *testing.T) {
	b := fileCheckBalloon(daemon.Notice{Kind: daemon.NoticeFileCheck, Text: "movie.mp4 is not what its name says.", Level: model.FileCheckDanger})
	if b.Title != "Check this download" || b.Text != "movie.mp4 is not what its name says." || !b.Error {
		t.Fatalf("danger balloon %+v", b)
	}
	b = fileCheckBalloon(daemon.Notice{Kind: daemon.NoticeFileCheck, Text: "video.mp4 may not be the file you wanted.\n", Level: model.FileCheckCaution})
	if b.Title != "Check this download" || b.Text != "video.mp4 may not be the file you wanted." || b.Error {
		t.Fatalf("caution balloon %+v", b)
	}
}
