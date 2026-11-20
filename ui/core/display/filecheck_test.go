package display_test

import (
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/display"
)

// The file check's labels (docs/DESKTOP-SPEC.md §9.7).

func checked(level downexv1.FileCheckLevel, st downexv1.DownloadState) *downexv1.Download {
	return &downexv1.Download{Filename: "movie.mp4", State: st, FileCheck: &downexv1.FileCheck{
		Level: level, ContentLabel: "Windows program", ClaimLabel: "MP4 video",
		Summary: "This file is a Windows program, not an MP4 video.", Detail: "Files that pretend to be another type are a common way to spread malware.",
	}}
}

func TestFileCheckLabels(t *testing.T) {
	done := downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED
	running := downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING
	danger := checked(downexv1.FileCheckLevel_FILE_CHECK_LEVEL_DANGER, done)
	caution := checked(downexv1.FileCheckLevel_FILE_CHECK_LEVEL_CAUTION, done)
	note := checked(downexv1.FileCheckLevel_FILE_CHECK_LEVEL_NOTE, done)
	plain := &downexv1.Download{Filename: "movie.mp4", State: done}

	for _, c := range []struct {
		d       *downexv1.Download
		status  string
		tone    display.FileCheckTone
		heading string
	}{
		{danger, "Check file", display.FileCheckFail, "This file is not what its name says"},
		{caution, "Check file", display.FileCheckWarn, "This may not be the file you wanted"},
		{note, "Completed", display.FileCheckNone, ""},
		{plain, "Completed", display.FileCheckNone, ""},
		{checked(downexv1.FileCheckLevel_FILE_CHECK_LEVEL_DANGER, running), "Downloading", display.FileCheckFail, "This file is not what its name says"},
	} {
		if got := display.StatusText(c.d); got != c.status {
			t.Errorf("StatusText = %q, want %q", got, c.status)
		}
		if got := display.ToneOfFileCheck(c.d); got != c.tone {
			t.Errorf("ToneOfFileCheck = %v, want %v", got, c.tone)
		}
		if got := display.FileCheckHeading(c.d); got != c.heading {
			t.Errorf("FileCheckHeading = %q, want %q", got, c.heading)
		}
	}
	if got := display.ContentsText(danger); got != "Windows program · named as MP4 video" {
		t.Errorf("ContentsText = %q", got)
	}
	same := checked(downexv1.FileCheckLevel_FILE_CHECK_LEVEL_NOTE, done)
	same.FileCheck.ClaimLabel = "Windows program"
	if got := display.ContentsText(same); got != "Windows program" {
		t.Errorf("ContentsText without a different claim = %q", got)
	}
	if display.ContentsText(plain) != "" {
		t.Error("ContentsText of a download without a check")
	}
	if got := display.DisguiseText(danger); got != "This file is a Windows program, not an MP4 video.\n\nFiles that pretend to be another type are a common way to spread malware." {
		t.Errorf("DisguiseText = %q", got)
	}
	if display.DisguiseText(caution) != "" {
		t.Error("CAUTION is not asked about")
	}
}

// TestNeedsOpenConfirm: the one gate of every open path.
func TestNeedsOpenConfirm(t *testing.T) {
	done := downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED
	for _, c := range []struct {
		d    *downexv1.Download
		want bool
	}{
		{&downexv1.Download{Filename: "setup.exe", State: done}, true},
		{&downexv1.Download{Filename: "report‮txt.pdf", State: done}, true},
		{&downexv1.Download{Filename: "report.pdf", State: done}, false},
		{checked(downexv1.FileCheckLevel_FILE_CHECK_LEVEL_DANGER, done), true},
		{checked(downexv1.FileCheckLevel_FILE_CHECK_LEVEL_CAUTION, done), false},
	} {
		if got := categories.NeedsOpenConfirm(c.d); got != c.want {
			t.Errorf("NeedsOpenConfirm(%s, %v) = %v", c.d.GetFilename(), c.d.GetFileCheck().GetLevel(), got)
		}
	}
}
